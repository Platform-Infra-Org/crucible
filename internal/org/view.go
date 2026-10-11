package org

import (
	"crucible/internal/smith"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

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

// ProgramView is a training's run as the team and trainings pages show it.
type ProgramView struct {
	Training           string            `json:"training"`
	Title              string            `json:"title"`
	Team               string            `json:"team"` // the owning team
	Version            int64             `json:"version"`
	Roles              config.Access     `json:"roles"`    // as given: people and groups; what a save sends back
	Resolved           config.Access     `json:"resolved"` // who that is now; read-only
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

// Member is someone on a team, their name ("" until they first sign in) and their tag.
type Member struct {
	Email string    `json:"email"`
	Name  string    `json:"name"`
	Tag   string    `json:"tag"`
	Smith smith.Ref `json:"smith"`
}

// TeamView is the team as the page needs it. It has no field for a webhook URL, on purpose.
type TeamView struct {
	ID          string        `json:"id"`
	Version     int64         `json:"version"`
	Name        string        `json:"name"`
	Members     []Member      `json:"members"` // by tag, strongest first, then email
	Budget      *BudgetView   `json:"budget,omitempty"`
	Programs    []ProgramView `json:"programs"` // the trainings it owns
	Schedules   []string      `json:"schedules"`
	CanEditTeam bool          `json:"can_edit_team"` // a leader or an admin: may change tags
	IsAdmin     bool          `json:"is_admin"`      // may add, remove and move people
}

func dur(d yamlx.Duration) string {
	if d == 0 {
		return ""
	}
	s, _ := d.MarshalYAML()
	return s.(string)
}

// members lists t's people by tag, strongest first, then email, with the name each signed in with.
func (a *api) members(r *http.Request, t *config.Team) ([]Member, error) {
	rows, err := a.s.DB.Query(r.Context(), `SELECT lower(email), max(name) FROM users WHERE lower(email) = ANY($1) GROUP BY lower(email)`, t.People())
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	var e, n string
	if _, err := pgx.ForEachRow(rows, []any{&e, &n}, func() error { names[e] = n; return nil }); err != nil {
		return nil, err
	}
	refs, err := smith.Lookup(r.Context(), a.s.DB, t.People())
	if err != nil {
		return nil, err
	}
	out := []Member{}
	for _, tag := range config.Tags {
		for _, e := range slices.Sorted(slices.Values(t.Tagged(tag))) {
			out = append(out, Member{Email: e, Name: names[e], Tag: tag, Smith: refs.Of(e)})
		}
	}
	return out, nil
}

// getTeam: its members and admins may read it; everyone else gets a 404 so team ids are not probed. The budget goes
// only to those who may see spend.
func (a *api) getTeam(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	t := c.P.Teams[id]
	if t == nil || (t.RoleOf(actor) == "" && !c.IsAdmin(actor)) {
		return apperr.Wrap(apperr.NotFound, "team not found")
	}
	people, err := a.members(r, t)
	if err != nil {
		return err
	}
	v := TeamView{ID: id, Version: t.Version, Name: t.Name, Members: people, Programs: []ProgramView{},
		Schedules:   nonNil(slices.Sorted(maps.Keys(c.P.Settings.Schedules))),
		CanEditTeam: c.Can(actor, rbac.EditTeam, id, "", ""), IsAdmin: c.IsAdmin(actor)}
	spend := c.Can(actor, rbac.ViewSpend, id, "", "")
	for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
		title, running, head := tr, "", ""
		if a.d.Content != nil {
			title, running, head = a.d.Content(id, tr)
		}
		spend = spend || c.Can(actor, rbac.ViewSpend, id, tr, "")
		v.Programs = append(v.Programs, programView(c, actor, id, tr, t.Programs[tr], title, running, head))
	}
	if spend {
		v.Budget = &BudgetView{Version: t.Budget.Version, MonthlyUSD: t.Budget.MonthlyUSD, HardCapUSD: t.Budget.HardCapUSD}
	}
	httpx.JSON(w, http.StatusOK, v)
	return nil
}

func programView(c rbac.Checker, actor, team, tr string, p *config.Program, title, running, head string) ProgramView {
	a := p.Access
	return ProgramView{Training: tr, Title: title, Team: team, Version: p.Version,
		Roles:    config.Access{Participants: nonNil(a.Participants), Editors: nonNil(a.Editors), Admins: nonNil(a.Admins)},
		Resolved: config.Access{Participants: nonNil(p.Enrolled), Editors: nonNil(p.Editors), Admins: nonNil(p.Roles.Manager)},
		Schedule: p.Schedule, InlineSchedule: p.Inline, BudgetUSDMonth: p.BudgetUSDMonth, ReviewSelfReported: p.ReviewSelfReported,
		PinnedRef: p.PinnedRef, RunningSHA: running, HeadSHA: head, CanManage: c.IsAdmin(actor) || c.Can(actor, rbac.ManageProgram, team, tr, ""),
		LabDefaults: map[string]string{"ttl": dur(p.LabDefaults.TTL), "idle_timeout": dur(p.LabDefaults.IdleTimeout),
			"max_extension": dur(p.LabDefaults.MaxExtension)}}
}

// programRoute checks the caller before the body is read: the training's managers and admins. team must own training.
func (a *api) programRoute(fn func(a *api, r *http.Request, actor, team, training string) (int64, error)) handler {
	return func(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
		team, training := chi.URLParam(r, "team"), chi.URLParam(r, "training")
		if t := c.P.Teams[team]; t == nil || t.Programs[training] == nil {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s does not own %s", team, training))
		}
		if err := manage(c, actor, training); err != nil {
			return err
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

// Person is someone Crucible knows: signed in at least once, on a team, or an admin.
type Person struct {
	Email string    `json:"email"`
	Name  string    `json:"name"`
	Team  string    `json:"team"` // "" when on no team
	Tag   string    `json:"tag"`
	Smith smith.Ref `json:"smith"`
}

// getPeople lists everyone and every group, for picking who holds a training's roles. Leaders, seniors, juniors and
// admins may read it: the people who manage trainings.
func (a *api) getPeople(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	if !c.CanCreateTraining(actor) {
		return apperr.Wrap(apperr.Forbidden, "only a team's leaders, seniors and juniors, or an admin, can list people")
	}
	rows, err := a.s.DB.Query(r.Context(), `SELECT lower(email), max(name) FROM users WHERE email <> '' GROUP BY lower(email)`)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for rows.Next() {
		var e, n string
		if err := rows.Scan(&e, &n); err != nil {
			rows.Close()
			return err
		}
		names[e] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range c.P.Teams {
		for _, e := range t.People() {
			if _, ok := names[e]; !ok {
				names[e] = ""
			}
		}
	}
	for _, e := range c.P.Admins {
		if _, ok := names[e]; !ok {
			names[e] = ""
		}
	}
	keys := slices.Sorted(maps.Keys(names))
	refs, err := smith.Lookup(r.Context(), a.s.DB, keys)
	if err != nil {
		return err
	}
	people := []Person{}
	for _, e := range keys {
		p := Person{Email: e, Name: names[e], Smith: refs.Of(e)}
		if t := c.P.TeamOf(e); t != nil {
			p.Team, p.Tag = t.ID, t.RoleOf(e)
		}
		people = append(people, p)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"people": people, "groups": c.P.Groups()})
	return nil
}
