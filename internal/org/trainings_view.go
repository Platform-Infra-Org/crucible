package org

import (
	"maps"
	"net/http"
	"slices"

	"crucible/internal/apperr"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
)

// TeamOption is a team an admin can give a training to.
type TeamOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TrainingView struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Available bool         `json:"available"`      // its content has loaded at least once
	Team      string       `json:"team"`           // the owning team; "" when none owns it yet
	Repo      string       `json:"repo,omitempty"` // managers only, credentials masked
	Ref       string       `json:"ref,omitempty"`
	Path      string       `json:"path,omitempty"`
	CanManage bool         `json:"can_manage"`
	Program   *ProgramView `json:"program,omitempty"` // its run; nil when no team owns it
}

type TrainingsView struct {
	IsAdmin   bool           `json:"is_admin"`
	CanCreate bool           `json:"can_create"` // may register a training
	MyTeam    string         `json:"my_team"`    // the team a training registered here belongs to ("" for an admin on no team)
	Schedules []string       `json:"schedules"`
	Teams     []TeamOption   `json:"teams"` // admins only: who a training can be given to
	Trainings []TrainingView `json:"trainings"`
	Repos     []RepoOption   `json:"repos"` // the connected repositories a training can be registered from
}

// RepoOption is a connected repository as the registration form needs it: no credential, URL already masked.
type RepoOption struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

// trainingsPage lists the trainings the caller works on: the ones they manage (their team owns it, or they are one of
// its admins) or edit; admins see every one. Participants find theirs on the Hearth, not here.
func (a *api) trainingsPage(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	admin := c.IsAdmin(actor)
	v := TrainingsView{IsAdmin: admin, CanCreate: c.CanCreateTraining(actor), Schedules: nonNil(slices.Sorted(maps.Keys(c.P.Settings.Schedules))),
		Teams: []TeamOption{}, Trainings: []TrainingView{}, Repos: []RepoOption{}}
	if v.CanCreate { // only someone who may register a training needs to know what it could come from
		rows, err := a.s.DB.Query(r.Context(), `SELECT id, url FROM repositories ORDER BY url`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o RepoOption
			if err := rows.Scan(&o.ID, &o.URL); err != nil {
				return err
			}
			o.URL = redactRepo(o.URL)
			v.Repos = append(v.Repos, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if t := c.P.TeamOf(actor); t != nil {
		v.MyTeam = t.ID
	}
	if admin {
		for _, id := range slices.Sorted(maps.Keys(c.P.Teams)) {
			v.Teams = append(v.Teams, TeamOption{ID: id, Name: c.P.Teams[id].Name})
		}
	}
	for _, tr := range slices.Sorted(maps.Keys(c.P.Trainings)) {
		owner, p := c.P.Owner(tr)
		team := ""
		if owner != nil {
			team = owner.ID
		}
		manages := admin || (owner != nil && c.Can(actor, rbac.ManageProgram, team, tr, ""))
		if !manages && (p == nil || !slices.Contains(p.Editors, actor)) {
			continue
		}
		title, running, head := tr, "", ""
		if a.d.Content != nil {
			title, running, head = a.d.Content(team, tr)
		}
		tv := TrainingView{ID: tr, Title: title, Available: head != "", Team: team, CanManage: manages}
		if manages {
			ref := c.P.Trainings[tr]
			tv.Repo, tv.Ref, tv.Path = redactRepo(ref.Repo), ref.Ref, ref.Path
		}
		if p != nil {
			pv := programView(c, actor, team, tr, p, title, running, head)
			tv.Program = &pv
		}
		v.Trainings = append(v.Trainings, tv)
	}
	if !v.CanCreate && len(v.Trainings) == 0 {
		return apperr.Wrap(apperr.Forbidden, "you don't manage or edit any training")
	}
	httpx.JSON(w, http.StatusOK, v)
	return nil
}
