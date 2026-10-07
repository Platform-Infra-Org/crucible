package org

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
	"crucible/internal/yamlx"
)

// BudgetView carries the budget's own version: 0 means no budget row yet.
type BudgetView struct {
	Version    int64   `json:"version"`
	MonthlyUSD float64 `json:"monthly_usd"`
	HardCapUSD float64 `json:"hard_cap_usd"`
}

type ProgramView struct {
	Training           string            `json:"training"`
	Title              string            `json:"title"`
	Version            int64             `json:"version"`
	Enrolled           []string          `json:"enrolled"`
	Roles              config.Roles      `json:"roles"`           // as stored: empty means "use the defaults"
	EffectiveRoles     config.Roles      `json:"effective_roles"` // who holds each role now; read-only, never sent back
	Schedule           string            `json:"schedule"`
	InlineSchedule     *config.Schedule  `json:"inline_schedule,omitempty"`
	LabDefaults        map[string]string `json:"lab_defaults"`
	BudgetUSDMonth     float64           `json:"budget_usd_month"`
	ReviewSelfReported bool              `json:"review_self_reported"`
	PinnedRef          string            `json:"pinned_ref"`
	RunningSHA         string            `json:"running_sha"`
	HeadSHA            string            `json:"head_sha"`
	CanManage          bool              `json:"can_manage"`
}

type TrainingOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// TeamView is the team as the page needs it. It has no field for a webhook URL, on purpose.
type TeamView struct {
	ID                 string            `json:"id"`
	Version            int64             `json:"version"`
	Name               string            `json:"name"`
	Leader             string            `json:"leader"`
	Seniors            []string          `json:"seniors"`
	Members            []string          `json:"members"`
	Trainees           []string          `json:"trainees"`
	Mentors            map[string]string `json:"mentors"`
	Budget             *BudgetView       `json:"budget,omitempty"`
	Programs           []ProgramView     `json:"programs"`
	AvailableTrainings []TrainingOption  `json:"available_trainings"`
	Schedules          []string          `json:"schedules"`
	CanEditTeam        bool              `json:"can_edit_team"`
	IsAdmin            bool              `json:"is_admin"`
}

func dur(d yamlx.Duration) string {
	if d == 0 {
		return ""
	}
	s, _ := d.MarshalYAML()
	return s.(string)
}

// teamRole is configapi.Role (which this package cannot import): the team role, else the strongest program role.
func teamRole(t *config.Team, email string) string {
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

// getTeam follows configapi.Team: anyone with a team or program role may read it, and so may an admin; everyone
// else gets a 404 so team ids are not probed. The budget goes only to those who may see spend.
func (a *api) getTeam(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	t := c.P.Teams[id]
	if t == nil || (teamRole(t, actor) == "" && !c.IsAdmin(actor)) {
		return apperr.Wrap(apperr.NotFound, "team not found")
	}
	v := TeamView{ID: id, Version: t.Version, Name: t.Name, Leader: t.Leader, Seniors: nonNil(t.Seniors), Members: nonNil(t.Members),
		Trainees: nonNil(t.Trainees), Mentors: t.Mentors, Programs: []ProgramView{}, AvailableTrainings: []TrainingOption{},
		Schedules:   nonNil(slices.Sorted(maps.Keys(c.P.Settings.Schedules))),
		CanEditTeam: c.Can(actor, rbac.EditTeam, id, "", ""), IsAdmin: c.IsAdmin(actor)}
	if v.Mentors == nil {
		v.Mentors = map[string]string{}
	}
	spend := c.Can(actor, rbac.ViewSpend, id, "", "")
	stored, err := a.storedRoles(r, id)
	if err != nil {
		return err
	}
	for _, tr := range slices.Sorted(maps.Keys(c.P.Trainings)) {
		title, running, head := tr, "", ""
		if a.d.Content != nil {
			title, running, head = a.d.Content(id, tr)
		}
		p := t.Programs[tr]
		if p == nil {
			v.AvailableTrainings = append(v.AvailableTrainings, TrainingOption{ID: tr, Title: title})
			continue
		}
		spend = spend || c.Can(actor, rbac.ViewSpend, id, tr, "")
		v.Programs = append(v.Programs, ProgramView{Training: tr, Title: title, Version: p.Version, Enrolled: nonNil(p.Enrolled),
			Roles:          config.Roles{Manager: nonNil(stored[tr].Manager), Scorers: nonNil(stored[tr].Scorers), Approvers: nonNil(stored[tr].Approvers)},
			EffectiveRoles: config.Roles{Manager: nonNil(p.Roles.Manager), Scorers: nonNil(p.Roles.Scorers), Approvers: nonNil(p.Roles.Approvers)},
			Schedule:       p.Schedule, InlineSchedule: p.Inline, BudgetUSDMonth: p.BudgetUSDMonth, ReviewSelfReported: p.ReviewSelfReported,
			PinnedRef: p.PinnedRef, RunningSHA: running, HeadSHA: head, CanManage: c.Can(actor, rbac.ManageProgram, id, tr, ""),
			LabDefaults: map[string]string{"ttl": dur(p.LabDefaults.TTL), "idle_timeout": dur(p.LabDefaults.IdleTimeout),
				"max_extension": dur(p.LabDefaults.MaxExtension)}})
	}
	if spend {
		v.Budget = &BudgetView{Version: t.Budget.Version, MonthlyUSD: t.Budget.MonthlyUSD, HardCapUSD: t.Budget.HardCapUSD}
	}
	httpx.JSON(w, http.StatusOK, v)
	return nil
}

// programRoute checks the caller before the body is read: program managers (the leader, or a named manager) and
// admins, as configapi does for an existing program. On a program that does not exist yet only the leader and admins pass.
func (a *api) programRoute(fn func(a *api, r *http.Request, actor, team, training string) (int64, error)) handler {
	return func(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
		team, training := chi.URLParam(r, "team"), chi.URLParam(r, "training")
		if !c.Can(actor, rbac.ManageProgram, team, training, "") {
			return apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can change this program")
		}
		if c.P.Teams[team] == nil {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
		}
		v, err := fn(a, r, actor, team, training)
		if err != nil {
			return err
		}
		return a.done(w, r, v)
	}
}

func putProgram(a *api, r *http.Request, actor, team, training string) (int64, error) {
	var b ProgramBody
	if err := httpx.Read(r, &b); err != nil {
		return 0, err
	}
	return b.Version + 1, a.s.SetProgram(r.Context(), actor, team, training, b)
}

func postProgram(a *api, r *http.Request, actor, team, training string) (int64, error) {
	var b ProgramBody
	if err := httpx.Read(r, &b); err != nil {
		return 0, err
	}
	return 1, a.s.Enroll(r.Context(), actor, team, training, b)
}

func deleteProgram(a *api, r *http.Request, actor, team, training string) (int64, error) {
	return 0, a.s.DeleteProgram(r.Context(), actor, team, training)
}

// putBudget: admins only, as configapi.SetBudget.
func (a *api) putBudget(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b BudgetBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.SetBudget(r.Context(), actor, chi.URLParam(r, "id"), b); err != nil {
		return err
	}
	return a.done(w, r, b.Version+1)
}

// storedRoles reads the explicit role rows. The snapshot holds the defaults already filled in, and a save that
// echoed those would freeze them against today's leader and seniors.
func (a *api) storedRoles(r *http.Request, team string) (map[string]config.Roles, error) {
	rows, err := a.s.DB.Query(r.Context(), `SELECT training, role, email FROM program_roles WHERE team = $1 ORDER BY email`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]config.Roles{}
	for rows.Next() {
		var tr, role, email string
		if err := rows.Scan(&tr, &role, &email); err != nil {
			return nil, err
		}
		x := out[tr]
		switch role {
		case "manager":
			x.Manager = append(x.Manager, email)
		case "scorer":
			x.Scorers = append(x.Scorers, email)
		case "approver":
			x.Approvers = append(x.Approvers, email)
		}
		out[tr] = x
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for tr, x := range out {
		out[tr] = config.Roles{Manager: nonNil(x.Manager), Scorers: nonNil(x.Scorers), Approvers: nonNil(x.Approvers)}
	}
	return out, nil
}
