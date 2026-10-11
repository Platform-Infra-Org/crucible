package org

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
)

func repoID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.Wrap(apperr.Invalid, "that is not a repository")
	}
	return id, nil
}

func (a *api) getRepos(w http.ResponseWriter, r *http.Request, _ string, _ rbac.Checker) error {
	repos, err := a.s.Repos(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"repos": repos, "can_keep_secrets": a.s.Secrets != nil})
	return nil
}

func (a *api) postRepo(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	var b RepoBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	id, err := a.s.ConnectRepo(r.Context(), actor, 0, b, a.s.Secrets)
	if err != nil {
		return err
	}
	a.check(r, id)
	return a.done(w, r, 0)
}

func (a *api) putRepo(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	id, err := repoID(r)
	if err != nil {
		return err
	}
	var b RepoBody
	if err := httpx.Read(r, &b); err != nil {
		return err
	}
	if _, err := a.s.ConnectRepo(r.Context(), actor, id, b, a.s.Secrets); err != nil {
		return err
	}
	a.check(r, id)
	return a.done(w, r, 0)
}

func (a *api) checkRepoNow(w http.ResponseWriter, r *http.Request, _ string, _ rbac.Checker) error {
	id, err := repoID(r)
	if err != nil {
		return err
	}
	a.check(r, id)
	return a.done(w, r, 0)
}

func (a *api) deleteRepo(w http.ResponseWriter, r *http.Request, actor string, _ rbac.Checker) error {
	id, err := repoID(r)
	if err != nil {
		return err
	}
	if err := a.s.DeleteRepo(r.Context(), actor, id); err != nil {
		return err
	}
	return a.done(w, r, 0)
}

// check asks the connection test to run, if one is wired. Its result is stored on the repository, so a failure shows
// on the Repositories page rather than as an error on the save the admin just made.
func (a *api) check(r *http.Request, id int64) {
	if a.d.CheckRepo != nil {
		a.d.CheckRepo(r.Context(), id)
	}
}
