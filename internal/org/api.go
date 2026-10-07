package org

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

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
}

type api struct {
	s *Store
	d APIDeps
}

// Routes mounts the org routes inside an authenticated group. The caller's router supplies the Origin/Referer and
// JSON-body guard; every body here is decoded with httpx.Read, which rejects unknown fields.
//
// /api/admin/... is admin-only. The two team-scoped routes (/api/org/...) check the caller against the snapshot.
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
	r.Get("/api/admin/trainings", a.admin(a.getTrainings))
	r.Post("/api/admin/trainings", a.admin(a.postTraining))
	r.Delete("/api/admin/trainings/{id}", a.admin(a.deleteTraining))
	r.Post("/api/admin/teams/{id}", a.admin(a.createTeam))
	r.Delete("/api/admin/teams/{id}", a.admin(a.deleteTeam))
	r.Put("/api/admin/teams/{id}/webhooks/{kind}", a.admin(a.putWebhook))
	r.Put("/api/org/teams/{id}/roster", a.handle(a.putRoster))
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
	if err := a.s.RemoveAdmin(r.Context(), actor, chi.URLParam(r, "email")); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) getTrainings(w http.ResponseWriter, r *http.Request, _ string, c rbac.Checker) error {
	type row struct {
		ID     string `json:"id"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	out := []row{}
	for id, t := range c.P.Trainings {
		out = append(out, row{id, redactRepo(t.Repo), t.Branch}) // credentials in the stored URL never reach the screen
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	httpx.JSON(w, http.StatusOK, map[string]any{"trainings": out})
	return nil
}

func (a *api) postTraining(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b struct {
		ID     string `json:"id"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if err := a.s.AddTraining(r.Context(), actor, b.ID, b.Repo, b.Branch); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

func (a *api) deleteTraining(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	if err := a.s.RemoveTraining(r.Context(), actor, chi.URLParam(r, "id")); err != nil {
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

func sortedEmails(in []string) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = em(e)
	}
	slices.Sort(out)
	return out
}

// putRoster is the leader's route. SetTeam trusts its whole body, so a non-admin may not change the leader, the
// name or the seniors (that would let a leader promote themselves or demote the leader). The permission check runs
// on the team snapshot at body.Version, and the store only writes when the stored version is still that one, so the
// check and the write see the same team; a client on another version gets a Conflict before any check is trusted.
func (a *api) putRoster(w http.ResponseWriter, r *http.Request, actor string, c rbac.Checker) error {
	id := chi.URLParam(r, "id")
	admin := c.IsAdmin(actor)
	if !admin && !c.Can(actor, rbac.EditTeam, id, "", "") {
		return apperr.Wrap(apperr.Forbidden, "only the team leader or an admin can change the roster")
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
		const only = "only an admin can change the team's %s"
		switch {
		case em(b.Leader) != t.Leader:
			return apperr.Wrap(apperr.Forbidden, fmt.Sprintf(only, "leader"))
		case strings.TrimSpace(b.Name) != t.Name:
			return apperr.Wrap(apperr.Forbidden, fmt.Sprintf(only, "name"))
		case !slices.Equal(sortedEmails(b.Seniors), sortedEmails(t.Seniors)):
			return apperr.Wrap(apperr.Forbidden, fmt.Sprintf(only, "seniors"))
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
		return apperr.Wrap(apperr.Forbidden, "only a program manager, the team leader or an admin can pin a version")
	}
	if t := c.P.Teams[team]; t == nil || t.Programs[training] == nil {
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s has no program %s", team, training))
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
