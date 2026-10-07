// Package configapi lets leaders, managers and admins read and change platform config from the UI (spec §4.3, §5.3,
// §6). Every change is a bot commit to the platform repo; git stays the source of truth.
package configapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
	"crucible/internal/yamlx"
)

type Service struct {
	DB       *pgxpool.Pool
	State    func() *gitsync.State
	Writer   *gitsync.Writer
	Resync   func(ctx context.Context) error // re-read git after a write so the page shows the change at once
	Changes  func(ctx context.Context, training, from, to string) (*gitsync.Changes, error)
	CheckPin func(ctx context.Context, training, sha string) error // a loadable commit on the tracked branch
}

type TeamSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type RolesView struct {
	Manager   []string `json:"manager"`
	Scorers   []string `json:"scorers"`
	Approvers []string `json:"approvers"`
}

type LabDefaultsView struct {
	TTL          string `json:"ttl"`
	IdleTimeout  string `json:"idle_timeout"`
	MaxExtension string `json:"max_extension"`
}

type ProgramView struct {
	Training           string          `json:"training"`
	Title              string          `json:"title"`
	Enrolled           []string        `json:"enrolled"`
	Roles              RolesView       `json:"roles"`
	Schedule           string          `json:"schedule"`
	InlineSchedule     string          `json:"inline_schedule,omitempty"` // read-only: inline windows live in git
	LabDefaults        LabDefaultsView `json:"lab_defaults"`
	BudgetUSDMonth     float64         `json:"budget_usd_month"`
	ReviewSelfReported bool            `json:"review_self_reported"`
	CanManage          bool            `json:"can_manage"`
	RunningSHA         string          `json:"running_sha"`
	HeadSHA            string          `json:"head_sha"`
	PinnedRef          string          `json:"pinned_ref"`
}

type TrainingOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type TeamView struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Leader             string            `json:"leader"`
	Seniors            []string          `json:"seniors"`
	Members            []string          `json:"members"`
	Trainees           []string          `json:"trainees"`
	Mentors            map[string]string `json:"mentors"`
	Budget             *config.Budget    `json:"budget,omitempty"` // admin, leader, program managers and approvers
	Programs           []ProgramView     `json:"programs"`
	AvailableTrainings []TrainingOption  `json:"available_trainings"`
	Schedules          []string          `json:"schedules"`
	PlatformSHA        string            `json:"platform_sha"`
	CanEditTeam        bool              `json:"can_edit_team"`
	IsAdmin            bool              `json:"is_admin"`
}

type RosterBody struct {
	BaseSHA  string            `json:"base_sha"`
	Seniors  []string          `json:"seniors"`
	Members  []string          `json:"members"`
	Trainees []string          `json:"trainees"`
	Mentors  map[string]string `json:"mentors"`
}

type ProgramBody struct {
	BaseSHA            string          `json:"base_sha"`
	Enrolled           []string        `json:"enrolled"`
	Roles              RolesView       `json:"roles"`
	Schedule           string          `json:"schedule"`
	LabDefaults        LabDefaultsView `json:"lab_defaults"`
	BudgetUSDMonth     float64         `json:"budget_usd_month"`
	ReviewSelfReported *bool           `json:"review_self_reported"` // nil keeps what git has
}

type BudgetBody struct {
	BaseSHA    string  `json:"base_sha"`
	MonthlyUSD float64 `json:"monthly_usd"`
	HardCapUSD float64 `json:"hard_cap_usd"`
}

type TrainingStatus struct {
	ID       string   `json:"id"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Head     string   `json:"head"`
	Problems []string `json:"problems"`
}

type AttentionLab struct {
	ID       string    `json:"id"`
	Trainee  string    `json:"trainee"`
	Team     string    `json:"team"`
	Training string    `json:"training"`
	Module   string    `json:"module"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"since"`
}

type ProgramPin struct {
	Team     string `json:"team"`
	Training string `json:"training"`
	Running  string `json:"running"`
	Head     string `json:"head"`
	Pinned   string `json:"pinned_ref,omitempty"`
}

type PlatformView struct {
	PlatformSHA     string            `json:"platform_sha"`
	PlatformErr     string            `json:"platform_error,omitempty"`
	SyncedAt        time.Time         `json:"synced_at"`
	CostTiers       *config.CostTiers `json:"cost_tiers"`
	EscalationHours float64           `json:"escalation_hours"`
	Schedules       map[string]string `json:"schedules"`
	Admins          []string          `json:"admins"`
	Trainings       []TrainingStatus  `json:"trainings"`
	Audit           []audit.Entry     `json:"audit"`
	PendingEdits    int               `json:"pending_edits"`
	Attention       []AttentionLab    `json:"attention"`
	Programs        []ProgramPin      `json:"programs"`
}

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "config is still syncing, try again in a moment")
	}
	return st, nil
}

// emails lowercases, trims, drops blanks and duplicates; never nil (so YAML gets [] not null).
func emails(in []string) []string {
	out := []string{}
	for _, e := range in {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func checkEmail(field, e string) error { return config.CheckEmail(field, e) }

// validEmails is emails for user input: every entry must look like an email address.
func validEmails(field string, in []string) ([]string, error) {
	out := emails(in)
	for _, e := range out {
		if err := checkEmail(field, e); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Role is the user's team role, or else their strongest program role in the team (manager, approver, scorer), which
// lets them read the team page; "" means no role.
func Role(t *config.Team, email string) string {
	if r := t.RoleOf(email); r != "" {
		return r
	}
	email, role := strings.ToLower(email), ""
	for _, p := range t.Programs {
		switch {
		case slices.Contains(p.Roles.Manager, email):
			return "manager"
		case slices.Contains(p.Roles.Approvers, email):
			role = "approver"
		case role == "" && slices.Contains(p.Roles.Scorers, email):
			role = "scorer"
		}
	}
	return role
}

func (s *Service) Teams(u *auth.User) ([]TeamSummary, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	admin := rbac.Checker{P: st.Platform}.IsAdmin(u.Email)
	out := []TeamSummary{}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		t := st.Platform.Teams[id]
		role := Role(t, u.Email)
		if role == "" && admin {
			role = "admin"
		}
		if role != "" {
			out = append(out, TeamSummary{ID: id, Name: t.Name, Role: role})
		}
	}
	return out, nil
}

func trainingTitle(st *gitsync.State, team, id string) string {
	if t, _ := st.ProgramTraining(team, id); t != nil {
		return t.Title
	}
	if t := st.Training(id, st.Heads[id]); t != nil {
		return t.Title
	}
	return id
}

func dur(d yamlx.Duration) string {
	if d == 0 {
		return ""
	}
	s, _ := d.MarshalYAML()
	return s.(string)
}

func (s *Service) Team(u *auth.User, id string) (*TeamView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	c := rbac.Checker{P: st.Platform}
	t := st.Platform.Teams[id]
	if t == nil || (Role(t, u.Email) == "" && !c.IsAdmin(u.Email)) {
		return nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	v := &TeamView{ID: id, Name: t.Name, Leader: t.Leader, Seniors: emails(t.Seniors), Members: emails(t.Members),
		Trainees: emails(t.Trainees), Mentors: t.Mentors, Programs: []ProgramView{}, AvailableTrainings: []TrainingOption{},
		Schedules: slices.Sorted(maps.Keys(st.Platform.Settings.Schedules)), PlatformSHA: st.PlatformSHA,
		CanEditTeam: c.Can(u.Email, rbac.EditTeam, id, "", ""), IsAdmin: c.IsAdmin(u.Email)}
	if v.Schedules == nil {
		v.Schedules = []string{}
	}
	if v.Mentors == nil {
		v.Mentors = map[string]string{}
	}
	spend := c.Can(u.Email, rbac.ViewSpend, id, "", "") // admin or leader
	for _, tr := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		p := t.Programs[tr]
		if p == nil {
			v.AvailableTrainings = append(v.AvailableTrainings, TrainingOption{ID: tr, Title: trainingTitle(st, id, tr)})
			continue
		}
		spend = spend || c.Can(u.Email, rbac.ViewSpend, id, tr, "") // program managers and approvers
		inline := ""
		if p.Inline != nil {
			inline = p.Inline.String()
		}
		v.Programs = append(v.Programs, ProgramView{InlineSchedule: inline, Training: tr, Title: trainingTitle(st, id, tr), Enrolled: emails(p.Enrolled),
			Roles:    RolesView{Manager: emails(p.Roles.Manager), Scorers: emails(p.Roles.Scorers), Approvers: emails(p.Roles.Approvers)},
			Schedule: p.Schedule, BudgetUSDMonth: p.BudgetUSDMonth, ReviewSelfReported: p.ReviewSelfReported, CanManage: c.Can(u.Email, rbac.ManageProgram, id, tr, ""),
			RunningSHA: st.ProgramSHAs[id+"/"+tr], HeadSHA: st.Heads[tr], PinnedRef: p.PinnedRef,
			LabDefaults: LabDefaultsView{TTL: dur(p.LabDefaults.TTL), IdleTimeout: dur(p.LabDefaults.IdleTimeout), MaxExtension: dur(p.LabDefaults.MaxExtension)}})
	}
	if spend { // spec §5.3 "view team spend"
		v.Budget = &t.Budget
	}
	return v, nil
}

// gitWriteTimeout bounds one config commit and push (and the startup bootstrap seed).
const gitWriteTimeout = 2 * time.Minute

// write commits one change, records it in the audit log with its commit, and re-reads git. A change that alters
// nothing makes no commit and no audit entry, and returns the current sha.
func (s *Service) write(ctx context.Context, u *auth.User, ch gitsync.Change, auditAction, target string, detail map[string]any) (string, error) {
	if ch.Base == "" { // the stale check also covers permissions decided on a snapshot that may lag git
		return "", apperr.Wrap(apperr.Invalid, "base_sha is required")
	}
	ch.Actor = u.Email
	// A hung remote must not hold the Writer for as long as the client waits, and a client that leaves after the push
	// landed must not cost the audit row (as edits.decide does).
	ctx = context.WithoutCancel(ctx)
	wctx, cancel := context.WithTimeout(ctx, gitWriteTimeout)
	defer cancel()
	sha, changed, err := s.Writer.Apply(wctx, ch)
	if err != nil || !changed {
		return sha, err
	}
	if err := audit.Log(ctx, s.DB, u.Email, auditAction, target, detail, sha); err != nil {
		slog.Error("audit log failed", "action", auditAction, "err", err)
	}
	if s.Resync != nil {
		if err := s.Resync(ctx); err != nil {
			slog.Warn("re-sync after a config write failed; the poller will pick it up", "err", err)
		}
	}
	return sha, nil
}

// edit returns a Change.Edit that sets keys in one repo file, explaining a file it cannot edit.
func edit(rel string, set map[string]any) func(dir string) error {
	return func(dir string) error {
		if err := gitsync.NoSymlinks(dir, rel); err != nil {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s in the platform repo can't be edited (%v) — fix it in git", rel, err))
		}
		if err := yamlx.Update(filepath.Join(dir, rel), set); err != nil {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s in the platform repo can't be edited (%v) — fix it in git", rel, err))
		}
		return nil
	}
}

func (s *Service) team(id string) (*gitsync.State, *config.Team, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, err
	}
	t := st.Platform.Teams[id]
	if t == nil {
		return nil, nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	return st, t, nil
}

func (s *Service) SetRoster(ctx context.Context, u *auth.User, team string, b RosterBody) (string, error) {
	st, _, err := s.team(team)
	if err != nil {
		return "", err
	}
	allow := func(p *config.Platform) error {
		if !(rbac.Checker{P: p}).Can(u.Email, rbac.EditTeam, team, "", "") {
			return apperr.Wrap(apperr.Forbidden, "only the team leader or an admin can change the roster")
		}
		return nil
	}
	if err := allow(st.Platform); err != nil {
		return "", err
	}
	set := map[string]any{}
	for field, list := range map[string][]string{"seniors": b.Seniors, "members": b.Members, "trainees": b.Trainees} {
		if set[field], err = validEmails(field, list); err != nil {
			return "", err
		}
	}
	mentors := map[string]string{}
	for k, v := range b.Mentors {
		if k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v)); k != "" && v != "" {
			if err := errors.Join(checkEmail("mentors", k), checkEmail("mentors", v)); err != nil {
				return "", err
			}
			mentors[k] = v
		}
	}
	set["mentors"] = mentors
	rel := path.Join("teams", team, "team.yaml")
	return s.write(ctx, u, gitsync.Change{Action: "update roster " + team, Base: b.BaseSHA, Paths: []string{rel},
		Allow: allow, Edit: edit(rel, set)}, "team.roster", team, set)
}

func (s *Service) SetProgram(ctx context.Context, u *auth.User, team, training string, b ProgramBody) (string, error) {
	st, t, err := s.team(team)
	if err != nil {
		return "", err
	}
	if _, ok := st.Platform.Trainings[training]; !ok { // also keeps the id a safe file name
		return "", apperr.Wrap(apperr.NotFound, "unknown training")
	}
	exists := t.Programs[training] != nil
	allow := func(p *config.Platform) error {
		c, tt := rbac.Checker{P: p}, p.Teams[team]
		if tt == nil || (tt.Programs[training] != nil) != exists {
			return gitsync.ErrStale
		}
		if (exists && !c.Can(u.Email, rbac.ManageProgram, team, training, "")) || (!exists && !c.Can(u.Email, rbac.EditTeam, team, "", "")) {
			return apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can change this program")
		}
		return nil
	}
	if err := allow(st.Platform); err != nil {
		return "", err
	}
	defaults := map[string]any{}
	for k, v := range map[string]string{"ttl": b.LabDefaults.TTL, "idle_timeout": b.LabDefaults.IdleTimeout, "max_extension": b.LabDefaults.MaxExtension} {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return "", apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s must be a duration like 2h or 45m", k))
		}
		defaults[k] = yamlx.Duration(d)
	}
	if b.BudgetUSDMonth < 0 {
		return "", apperr.Wrap(apperr.Invalid, "the program budget must not be negative")
	}
	lists := map[string][]string{}
	for field, list := range map[string][]string{"enrolled": b.Enrolled, "manager": b.Roles.Manager, "scorers": b.Roles.Scorers, "approvers": b.Roles.Approvers} {
		if lists[field], err = validEmails(field, list); err != nil {
			return "", err
		}
	}
	set := map[string]any{"training": training, "enrolled": lists["enrolled"], "schedule": nil, "lab_defaults": nil, "budget_usd_month": nil,
		"roles": map[string][]string{"manager": lists["manager"], "scorers": lists["scorers"], "approvers": lists["approvers"]}}
	if b.Schedule != "" {
		set["schedule"] = b.Schedule
	}
	if len(defaults) > 0 {
		set["lab_defaults"] = defaults
	}
	if b.BudgetUSDMonth > 0 {
		set["budget_usd_month"] = b.BudgetUSDMonth
	}
	if b.ReviewSelfReported != nil {
		set["review_self_reported"] = nil // off is the default: drop the key
		if *b.ReviewSelfReported {
			set["review_self_reported"] = true
		}
	}
	schedule := b.Schedule
	if exists && t.Programs[training].Inline != nil {
		if b.Schedule != "" {
			return "", apperr.Wrap(apperr.Conflict, "this program has an inline schedule; edit it in git")
		}
		delete(set, "schedule") // inline windows live in git; the UI only picks named schedules, so keep them
		schedule = "inline"
	}
	rel := path.Join("teams", team, "programs", training+".yaml")
	action, auditAction := "update program "+team+"/"+training, "program.update"
	if !exists {
		action, auditAction = "enroll "+team+" in "+training, "program.enroll"
	}
	return s.write(ctx, u, gitsync.Change{Action: action, Base: b.BaseSHA, Paths: []string{rel}, Allow: allow, Edit: edit(rel, set)},
		auditAction, team+"/"+training, map[string]any{
			"enrolled": set["enrolled"], "roles": set["roles"], "schedule": schedule, "lab_defaults": b.LabDefaults, "budget_usd_month": b.BudgetUSDMonth,
			"review_self_reported": b.ReviewSelfReported})
}

func (s *Service) SetBudget(ctx context.Context, u *auth.User, team string, b BudgetBody) (string, error) {
	st, _, err := s.team(team)
	if err != nil {
		return "", err
	}
	allow := func(p *config.Platform) error {
		if !(rbac.Checker{P: p}).IsAdmin(u.Email) {
			return apperr.Wrap(apperr.Forbidden, "only admins set team budgets")
		}
		return nil
	}
	if err := allow(st.Platform); err != nil {
		return "", err
	}
	set := map[string]any{"monthly_usd": b.MonthlyUSD, "hard_cap_usd": nil}
	if b.HardCapUSD > 0 {
		set["hard_cap_usd"] = b.HardCapUSD
	}
	rel := path.Join("teams", team, "budget.yaml")
	return s.write(ctx, u, gitsync.Change{Action: "set budget " + team, Base: b.BaseSHA, Paths: []string{rel},
		Allow: allow, Edit: edit(rel, set)}, "team.budget", team, set)
}

// Platform is the admin's read-only view of platform.yaml, sync health and recent privileged actions.
func (s *Service) Platform(u *auth.User) (*PlatformView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	p := st.Platform
	if !(rbac.Checker{P: p}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "admins only")
	}
	v := &PlatformView{PlatformSHA: st.PlatformSHA, PlatformErr: st.PlatformErr, SyncedAt: st.SyncedAt, CostTiers: p.Settings.CostTiers,
		EscalationHours: p.Settings.EscalationHours, Schedules: map[string]string{}, Admins: p.Admins, Trainings: []TrainingStatus{}}
	for name, sc := range p.Settings.Schedules {
		v.Schedules[name] = sc.String()
	}
	for _, id := range slices.Sorted(maps.Keys(p.Trainings)) {
		ref := p.Trainings[id]
		ts := TrainingStatus{ID: id, Repo: ref.Repo, Branch: ref.Branch, Head: st.Heads[id], Problems: []string{}}
		for _, key := range []string{id, id + "@" + st.Heads[id]} {
			for _, pr := range st.Problems[key] {
				ts.Problems = append(ts.Problems, pr.String())
			}
		}
		v.Trainings = append(v.Trainings, ts)
	}
	return v, nil
}

// Status is the admin's Forge Status page: Platform plus audit, edits waiting, labs that need a look and the content
// version each program runs.
func (s *Service) Status(ctx context.Context, u *auth.User) (*PlatformView, error) {
	v, err := s.Platform(u)
	if err != nil {
		return nil, err
	}
	if v.Audit, err = audit.Recent(ctx, s.DB, 25); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM content_edits WHERE status = 'pending'`).Scan(&v.PendingEdits); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT li.id, u.email, li.team, li.training, li.module, li.state, li.error,
		coalesce(li.destroyed_at, li.created_at) AS since
		FROM lab_instances li JOIN users u ON u.id = li.user_id
		WHERE (li.state = 'failed' AND coalesce(li.destroyed_at, li.created_at) > now() - interval '24 hours')
		   OR (li.state = 'destroying' AND li.stuck_alerted_at IS NOT NULL)
		ORDER BY 8 DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	if v.Attention, err = pgx.CollectRows(rows, pgx.RowToStructByPos[AttentionLab]); err != nil {
		return nil, err
	}
	if v.Attention == nil {
		v.Attention = []AttentionLab{}
	}
	st := s.State() // Platform succeeded, so state is loaded
	v.Programs = []ProgramPin{}
	for _, team := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		for _, tr := range slices.Sorted(maps.Keys(st.Platform.Teams[team].Programs)) {
			v.Programs = append(v.Programs, ProgramPin{Team: team, Training: tr, Running: st.ProgramSHAs[team+"/"+tr],
				Head: st.Heads[tr], Pinned: st.Platform.Teams[team].Programs[tr].PinnedRef})
		}
	}
	return v, nil
}

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	reply := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, v)
	}
	sha := func(w http.ResponseWriter, sha string, err error) { reply(w, map[string]string{"sha": sha}, err) }
	r.Get("/api/teams", func(w http.ResponseWriter, r *http.Request) { v, err := s.Teams(user(r)); reply(w, v, err) })
	r.Get("/api/teams/{team}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Team(user(r), chi.URLParam(r, "team"))
		reply(w, v, err)
	})
	r.Get("/api/teams/{team}/programs/{training}/changes", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ProgramChanges(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"))
		reply(w, v, err)
	})
	r.Put("/api/teams/{team}/programs/{training}/pin", func(w http.ResponseWriter, r *http.Request) {
		var b PinBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetPin(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"), b)
		sha(w, v, err)
	})
	r.Put("/api/teams/{team}/roster", func(w http.ResponseWriter, r *http.Request) {
		var b RosterBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetRoster(r.Context(), user(r), chi.URLParam(r, "team"), b)
		sha(w, v, err)
	})
	r.Put("/api/teams/{team}/programs/{training}", func(w http.ResponseWriter, r *http.Request) {
		var b ProgramBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetProgram(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"), b)
		sha(w, v, err)
	})
	r.Put("/api/teams/{team}/budget", func(w http.ResponseWriter, r *http.Request) {
		var b BudgetBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetBudget(r.Context(), user(r), chi.URLParam(r, "team"), b)
		sha(w, v, err)
	})
	r.Get("/api/admin/platform", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Status(r.Context(), user(r))
		reply(w, v, err)
	})
}

type PinBody struct {
	BaseSHA string `json:"base_sha"`
	Ref     string `json:"ref"` // "" = track the branch head
}

var errCantBump = apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can bump content")

func (s *Service) manageable(u *auth.User, team, training string) (*gitsync.State, error) {
	st, t, err := s.team(team)
	if err != nil {
		return nil, err
	}
	if t.Programs[training] == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this team is not enrolled in that training")
	}
	if !(rbac.Checker{P: st.Platform}).Can(u.Email, rbac.ManageProgram, team, training, "") {
		return nil, errCantBump
	}
	return st, nil
}

// ProgramChanges is the diff summary from the version a program runs to its training's branch head. Only people who
// can bump see it.
func (s *Service) ProgramChanges(ctx context.Context, u *auth.User, team, training string) (*gitsync.Changes, error) {
	st, err := s.manageable(u, team, training)
	if err != nil {
		return nil, err
	}
	return s.Changes(ctx, training, st.ProgramSHAs[team+"/"+training], st.Heads[training])
}

// SetPin pins a program to a validated content version, or back to tracking the branch head (spec §6).
func (s *Service) SetPin(ctx context.Context, u *auth.User, team, training string, b PinBody) (string, error) {
	st, err := s.manageable(u, team, training)
	if err != nil {
		return "", err
	}
	allow := func(p *config.Platform) error {
		if !(rbac.Checker{P: p}).Can(u.Email, rbac.ManageProgram, team, training, "") {
			return errCantBump
		}
		return nil
	}
	ref := strings.ToLower(strings.TrimSpace(b.Ref))
	if ref != "" {
		if err := s.CheckPin(ctx, training, ref); err != nil {
			return "", err
		}
	}
	set := map[string]any{"pinned_ref": nil}
	action := "track the head of " + training + " in " + team
	if ref != "" {
		set["pinned_ref"], action = ref, "pin "+team+"/"+training+" to "+ref[:7]
	}
	rel := path.Join("teams", team, "programs", training+".yaml")
	return s.write(ctx, u, gitsync.Change{Action: action, Base: b.BaseSHA, Paths: []string{rel}, Allow: allow, Edit: edit(rel, set)},
		"program.pin", team+"/"+training, map[string]any{"from": st.ProgramSHAs[team+"/"+training], "to": ref})
}
