package org

import (
	"maps"
	"net/http"
	"slices"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
)

// TeamRoster is a team as the trainings page needs it: who can be enrolled or given a role, and the version a roster
// save must echo (enrolling someone new adds them to the team as a trainee first).
type TeamRoster struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     int64             `json:"version"`
	Leader      string            `json:"leader"`
	Seniors     []string          `json:"seniors"`
	Members     []string          `json:"members"`
	Trainees    []string          `json:"trainees"`
	Mentors     map[string]string `json:"mentors"`
	CanEditTeam bool              `json:"can_edit_team"` // admin or leader: may add people and start programs
}

// TeamProgram is one team's run of a training.
type TeamProgram struct {
	Team string `json:"team"`
	ProgramView
}

type TrainingView struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Available bool          `json:"available"`        // its content has loaded at least once
	Repo      string        `json:"repo,omitempty"`   // admins only, credentials masked
	Branch    string        `json:"branch,omitempty"` // admins only
	Programs  []TeamProgram `json:"programs"`
}

type TrainingsView struct {
	IsAdmin   bool           `json:"is_admin"`
	Schedules []string       `json:"schedules"`
	Teams     []TeamRoster   `json:"teams"`
	Trainings []TrainingView `json:"trainings"`
}

// trainingsPage is the trainings page: every registered training, and for each the teams running it with who is
// enrolled and who holds which role. The visibility rule is getTeam's: a caller sees the teams in which they hold a
// team or program role, an admin sees all. Only admins see where the content comes from.
func (a *api) trainingsPage(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	admin := c.IsAdmin(actor)
	v := TrainingsView{IsAdmin: admin, Schedules: nonNil(slices.Sorted(maps.Keys(c.P.Settings.Schedules))), Teams: []TeamRoster{},
		Trainings: []TrainingView{}}
	stored := map[string]map[string]config.Roles{}
	for _, id := range slices.Sorted(maps.Keys(c.P.Teams)) {
		t := c.P.Teams[id]
		if !admin && teamRole(t, actor) == "" {
			continue
		}
		roles, err := a.storedRoles(r, id)
		if err != nil {
			return err
		}
		stored[id] = roles
		mentors := t.Mentors
		if mentors == nil {
			mentors = map[string]string{}
		}
		v.Teams = append(v.Teams, TeamRoster{ID: id, Name: t.Name, Version: t.Version, Leader: t.Leader, Seniors: nonNil(t.Seniors),
			Members: nonNil(t.Members), Trainees: nonNil(t.Trainees), Mentors: mentors, CanEditTeam: c.Can(actor, rbac.EditTeam, id, "", "")})
	}
	if !admin && len(v.Teams) == 0 {
		return apperr.Wrap(apperr.Forbidden, "you are not on any team; ask an admin or your team leader")
	}
	for _, tr := range slices.Sorted(maps.Keys(c.P.Trainings)) {
		tv := TrainingView{ID: tr, Title: tr, Programs: []TeamProgram{}}
		if admin {
			tv.Repo, tv.Branch = redactRepo(c.P.Trainings[tr].Repo), c.P.Trainings[tr].Branch
		}
		for _, team := range v.Teams {
			title, running, head := tr, "", ""
			if a.d.Content != nil {
				title, running, head = a.d.Content(team.ID, tr)
			}
			tv.Title, tv.Available = title, tv.Available || head != ""
			if p := c.P.Teams[team.ID].Programs[tr]; p != nil {
				tv.Programs = append(tv.Programs, TeamProgram{Team: team.ID, ProgramView: programView(c, actor, team.ID, tr, p,
					stored[team.ID][tr], title, running, head)})
			}
		}
		if len(v.Teams) == 0 && a.d.Content != nil { // an admin with no teams yet still sees titles
			title, _, head := a.d.Content("", tr)
			tv.Title, tv.Available = title, head != ""
		}
		v.Trainings = append(v.Trainings, tv)
	}
	httpx.JSON(w, http.StatusOK, v)
	return nil
}
