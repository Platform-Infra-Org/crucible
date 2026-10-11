package org

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
)

// APIDeps connects the routes to the live config. Platform is the snapshot used for permission checks; Refresh
// re-reads it after a write so the next request sees the change; CheckPin proves a commit exists in the content
// mirror (the store has no syncer).
type APIDeps struct {
	Platform func() *config.Platform
	Refresh  func(context.Context) error
	CheckPin func(ctx context.Context, training, sha string) error
	// Content names a training and the commits it runs and tracks, for the team page. Nil: the id, no commits.
	Content func(team, training string) (title, running, head string)
	// CheckRepo tries a connected repository and records what it found (Store.RecordCheck). Nil: no test is run and
	// a repository stays "unknown" until something reads from it.
	CheckRepo func(ctx context.Context, id int64)
}

type api struct {
	s *Store
	d APIDeps
}

// Routes mounts the org routes inside an authenticated group. The caller's router supplies the Origin/Referer and
// JSON-body guard; every body here is decoded with httpx.Read, which rejects unknown fields.
//
// /api/admin/... is admin-only. The /api/org/... routes check the caller against the snapshot.
func (s *Store) Routes(r chi.Router, d APIDeps) {
	a := &api{s, d}
	r.Get("/api/admin/settings", a.admin(a.getSettings))
	r.Put("/api/admin/settings", a.admin(a.putSettings))
	r.Put("/api/admin/schedules/{name}", a.admin(a.putSchedule))
	r.Delete("/api/admin/schedules/{name}", a.admin(a.deleteSchedule))
	r.Put("/api/admin/quotes", a.admin(a.putQuotes))
	r.Get("/api/admin/admins", a.admin(a.getAdmins))
	r.Post("/api/admin/admins", a.admin(a.postAdmin))
	r.Delete("/api/admin/admins/{email}", a.admin(a.deleteAdmin))
	r.Get("/api/admin/repos", a.admin(a.getRepos))
	r.Post("/api/admin/repos", a.admin(a.postRepo))
	r.Put("/api/admin/repos/{id}", a.admin(a.putRepo))
	r.Post("/api/admin/repos/{id}/check", a.admin(a.checkRepoNow))
	r.Delete("/api/admin/repos/{id}", a.admin(a.deleteRepo))
	r.Get("/api/admin/trainings", a.admin(a.getTrainings))
	r.Put("/api/admin/trainings/{id}/owner", a.admin(a.putOwner))
	r.Post("/api/admin/teams/{id}", a.admin(a.createTeam))
	r.Delete("/api/admin/teams/{id}", a.admin(a.deleteTeam))
	r.Put("/api/admin/teams/{id}/webhooks/{kind}", a.admin(a.putWebhook))
	r.Get("/api/admin/export", a.admin(a.getExport))
	r.Post("/api/admin/import", a.admin(a.postImport))
	r.Get("/api/org/people", a.handle(a.getPeople))
	r.Get("/api/org/trainings", a.handle(a.trainingsPage))
	r.Post("/api/org/trainings", a.handle(a.postTraining))
	r.Put("/api/org/trainings/{id}/source", a.handle(a.putSource))
	r.Delete("/api/org/trainings/{id}", a.handle(a.deleteTraining))
	r.Get("/api/org/teams/{id}", a.handle(a.getTeam))
	r.Put("/api/org/teams/{id}/roster", a.handle(a.putRoster))
	r.Put("/api/org/teams/{id}/budget", a.admin(a.putBudget))
	r.Put("/api/org/teams/{team}/programs/{training}", a.handle(a.programRoute(putProgram)))
	r.Put("/api/org/teams/{team}/programs/{training}/pin", a.handle(a.putPin))
}

type handler func(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error

// handle loads the signed-in user and the snapshot, runs fn, and turns its error into a response.
func (a *api) handle(fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFrom(r.Context())
		var p *config.Platform
		if a.d.Platform != nil {
			p = a.d.Platform()
		}
		switch {
		case u == nil:
			httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		case p == nil:
			httpx.Error(w, apperr.Wrap(apperr.Unavailable, "configuration is not loaded yet; try again in a moment"))
			return
		}
		if err := fn(w, r, strings.ToLower(u.Email), rbac.Checker{P: p}); err != nil {
			httpx.Error(w, err)
		}
	}
}

// admin is handle plus the admin check, made before fn reads the request.
func (a *api) admin(fn handler) http.HandlerFunc {
	return a.handle(func(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
		if !c.IsAdmin(actor) {
			return apperr.Wrap(apperr.Forbidden, "admins only")
		}
		return fn(w, r, actor, c)
	})
}

// done refreshes the snapshot after a write and answers with the new version (0 when the thing has none).
func (a *api) done(w http.ResponseWriter, r *http.Request, version int64) error {
	if a.d.Refresh != nil {
		if err := a.d.Refresh(r.Context()); err != nil {
			return apperr.Wrap(apperr.Unavailable, "your change was saved, but the page could not reload the configuration; refresh in a moment")
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"version": version})
	return nil
}

func (a *api) getExport(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	b, err := a.s.Export(r.Context(), actor)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="crucible-export-%s.json"`, time.Now().UTC().Format("2006-01-02")))
	_, err = w.Write(b)
	return err
}

// maxImport bounds an import file; httpx.Read's 1 MiB is too small for an instance with history.
const maxImport = 256 << 20

func (a *api) postImport(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImport))
	if err != nil {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("the file could not be read (the limit is %d MiB): %v", maxImport>>20, err))
	}
	if err := a.s.Import(r.Context(), actor, b); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) getSettings(w http.ResponseWriter, r *http.Request, _ string, _ rbac.Checker) error {
	// Version first: a save between the two reads then conflicts instead of overwriting the newer values.
	var v int64
	if err := a.s.DB.QueryRow(r.Context(), `SELECT version FROM settings WHERE id = 1`).Scan(&v); err != nil {
		return err
	}
	var st config.Settings
	if err := a.s.settings(r.Context(), &st); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, SettingsBody{Version: v, DefaultTheme: st.DefaultTheme, CostTiers: st.CostTiers,
		ClusterUSDPerHour: st.ClusterUSDPerHour, EscalationHours: st.EscalationHours, Ranks: st.Ranks})
	return nil
}

func (a *api) putSettings(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b SettingsBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.SetSettings(r.Context(), actor, b); err != nil {
		return err
	}
	return a.done(w, r, b.Version+1)
}

func (a *api) putSchedule(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var sc config.Schedule
	if err := httpx.Read(r, &sc); err != nil {
		return err
	}
	if err := a.s.SetSchedule(r.Context(), actor, chi.URLParam(r, "name"), sc); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) deleteSchedule(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	if err := a.s.DeleteSchedule(r.Context(), actor, chi.URLParam(r, "name")); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) putQuotes(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b struct {
		Quotes []string `json:"quotes"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.SetQuotes(r.Context(), actor, b.Quotes); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) getAdmins(w http.ResponseWriter, r *http.Request, _ string, _ rbac.Checker) error {
	list, err := a.s.Admins(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string][]string{"admins": list})
	return nil
}

func (a *api) postAdmin(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b struct {
		Email string `json:"email"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.AddAdmin(r.Context(), actor, b.Email); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) deleteAdmin(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	email, err := url.PathUnescape(chi.URLParam(r, "email")) // chi routes on RawPath: the param can still be percent-encoded
	if err != nil {
		return apperr.Wrap(apperr.Invalid, "that email address is not valid")
	}
	if err := a.s.RemoveAdmin(r.Context(), actor, email); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) getTrainings(w http.ResponseWriter, r *http.Request, _ string, c rbac.Checker) error {
	type row struct {
		ID   string `json:"id"`
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
		Path string `json:"path"`
	}
	out := []row{}
	for id, t := range c.P.Trainings {
		out = append(out, row{id, redactRepo(t.Repo), t.Ref, t.Path}) // credentials in the stored URL never reach the screen
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	httpx.JSON(w, http.StatusOK, map[string]any{"trainings": out})
	return nil
}

// postTraining registers a training. A leader, senior or junior registers it for their own team; an admin names the
// team.
func (a *api) postTraining(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	if !c.CanCreateTraining(actor) {
		return apperr.Wrap(apperr.Forbidden, "only a team's leaders, seniors and juniors, or an admin, can register a training")
	}
	var b struct {
		ID   string `json:"id"`
		Team string `json:"team"`
		config.TrainingRef
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	team := strings.TrimSpace(b.Team)
	if own := c.P.TeamOf(actor); !c.IsAdmin(actor) || team == "" {
		if own == nil {
			return apperr.Wrap(apperr.Invalid, "choose the team that will own this training")
		}
		if team != "" && team != own.ID {
			return apperr.Wrap(apperr.Forbidden, "you can register a training only for your own team")
		}
		team = own.ID
	}
	if err := a.s.CreateTraining(r.Context(), actor, strings.TrimSpace(b.ID), b.TrainingRef, team); err != nil {
		return err
	}
	return a.done(w, r, 1)
}

// manage checks that actor may manage training (its owning team's leaders, seniors and juniors, its admins, Crucible
// admins); an admin may also manage a training no team owns.
func manage(c rbac.Checker, actor, training string) error {
	if _, ok := c.P.Trainings[training]; !ok {
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no training %q", training))
	}
	if c.IsAdmin(actor) {
		return nil
	}
	if t, _ := c.P.Owner(training); t != nil && c.Can(actor, rbac.ManageProgram, t.ID, training, "") {
		return nil
	}
	return apperr.Wrap(apperr.Forbidden, "only the training's managers (its team's leaders, seniors and juniors, and its admins) can change it")
}

// putSource points a training at another repo, ref or folder. An empty repo keeps the stored one.
func (a *api) putSource(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	if err := manage(c, actor, id); err != nil {
		return err
	}
	var b config.TrainingRef
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.AddTraining(r.Context(), actor, id, b); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) deleteTraining(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	if err := manage(c, actor, id); err != nil {
		return err
	}
	if err := a.s.RemoveTraining(r.Context(), actor, id); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

// putOwner gives a training to another team (admins only); its run moves with it.
func (a *api) putOwner(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	if _, ok := c.P.Trainings[id]; !ok {
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no training %q", id))
	}
	var b struct {
		Team string `json:"team"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if c.P.Teams[b.Team] == nil {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("no team %q", b.Team))
	}
	if err := a.s.SetOwner(r.Context(), actor, id, b.Team); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) createTeam(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b TeamBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.CreateTeam(r.Context(), actor, chi.URLParam(r, "id"), b); err != nil {
		return err
	}
	return a.done(w, r, 1)
}

func (a *api) deleteTeam(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	if err := a.s.DeleteTeam(r.Context(), actor, chi.URLParam(r, "id")); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

// putWebhook: the URL is a secret. It is read from the body and stored; no response, error or audit row repeats it.
func (a *api) putWebhook(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b struct {
		URL string `json:"url"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.SetTeamWebhook(r.Context(), actor, chi.URLParam(r, "id"), chi.URLParam(r, "kind"), b.URL); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

// putRoster saves a team's members and tags. An admin may change anything: add people (moving them from another
// team), remove them, rename the team. A leader only changes the tags of the people already on it (any tag, leader
// included). The permission check runs on the team snapshot at body.Version, and the store only
// writes when the stored version is still that one, so the check and the write see the same team.
func (a *api) putRoster(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	admin := c.IsAdmin(actor)
	if !admin && !c.Can(actor, rbac.EditTeam, id, "", "") {
		return apperr.Wrap(apperr.Forbidden, "only the team's leaders or an admin can change its tags")
	}
	t := c.P.Teams[id]
	if t == nil {
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", id))
	}
	var b TeamBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if b.Version != t.Version {
		return apperr.Wrap(apperr.Conflict, "someone changed this team, reload and try again")
	}
	if !admin {
		var people []string
		for e := range b.Members {
			people = append(people, em(e))
		}
		slices.Sort(people)
		switch {
		case !slices.Equal(slices.Compact(people), t.People()):
			return apperr.Wrap(apperr.Forbidden, "only an admin adds people to a team or takes them off it")
		case strings.TrimSpace(b.Name) != t.Name:
			return apperr.Wrap(apperr.Forbidden, "only an admin can rename the team")
		}
	}
	if err := a.s.SetTeam(r.Context(), actor, id, b); err != nil {
		return err
	}
	return a.done(w, r, b.Version+1)
}

// putPin moves a program's pinned commit. The commit must resolve in the content mirror before it is stored.
func (a *api) putPin(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	team, training := chi.URLParam(r, "team"), chi.URLParam(r, "training")
	if !c.Can(actor, rbac.ManageProgram, team, training, "") {
		return apperr.Wrap(apperr.Forbidden, "only the training's managers or an admin can pin a version")
	}
	if t := c.P.Teams[team]; t == nil || t.Programs[training] == nil {
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s does not own %s", team, training))
	}
	var b struct {
		SHA string `json:"sha"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	sha := strings.ToLower(strings.TrimSpace(b.SHA))
	if sha != "" { // an empty sha follows the branch head again; nothing to verify
		if a.d.CheckPin == nil {
			return apperr.Wrap(apperr.Unavailable, "pinning is not available right now")
		}
		if err := a.d.CheckPin(r.Context(), training, sha); err != nil {
			if errors.Is(err, apperr.Invalid) {
				return apperr.Wrap(apperr.Invalid, fmt.Sprintf("commit %q is not a validated commit on %s's tracked branch; pick the current head or an earlier commit from the list", sha, training))
			}
			return err
		}
	}
	if err := a.s.SetPin(r.Context(), actor, team, training, sha); err != nil {
		return err
	}
	return a.done(w, r, 0)
}
