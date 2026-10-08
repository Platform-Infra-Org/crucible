// Package configapi serves read-only views of the live configuration: the teams a user belongs to, Forge Status, and
// what changed in a program's training since the version it runs. Configuration is written through internal/org.
package configapi

import (
	"context"
	"maps"
	"net/http"
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
)

type Service struct {
	DB      *pgxpool.Pool
	State   func() *gitsync.State
	Changes func(ctx context.Context, training, from, to string) (*gitsync.Changes, error)
}

type TeamSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
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
	v := &PlatformView{PlatformErr: st.PlatformErr, SyncedAt: st.SyncedAt, CostTiers: p.Settings.CostTiers,
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
	r.Get("/api/teams", func(w http.ResponseWriter, r *http.Request) { v, err := s.Teams(user(r)); reply(w, v, err) })
	r.Get("/api/teams/{team}/programs/{training}/changes", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ProgramChanges(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"))
		reply(w, v, err)
	})
	r.Get("/api/admin/platform", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Status(r.Context(), user(r))
		reply(w, v, err)
	})
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
