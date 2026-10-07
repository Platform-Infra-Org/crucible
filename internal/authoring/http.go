package authoring

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	reply := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, v)
	}
	r.Get("/api/authoring/schema", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Schemas(user(r), r.URL.Query().Get("training"))
		reply(w, v, err)
	})
	r.Post("/api/authoring/validate", func(w http.ResponseWriter, r *http.Request) {
		var in ValidateReq
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		probs, err := s.Validate(r.Context(), user(r), in)
		reply(w, map[string]any{"problems": probs}, err)
	})
	withID := func(fn func(w http.ResponseWriter, r *http.Request, n int64)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			n, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			if err != nil {
				httpx.Error(w, apperr.Wrap(apperr.NotFound, "draft not found"))
				return
			}
			fn(w, r, n)
		}
	}
	r.Get("/api/authoring/drafts", func(w http.ResponseWriter, r *http.Request) { v, err := s.List(r.Context(), user(r)); reply(w, v, err) })
	r.Post("/api/authoring/drafts", func(w http.ResponseWriter, r *http.Request) {
		var in NewDraft
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Create(r.Context(), user(r), in)
		reply(w, v, err)
	})
	r.Get("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		v, err := s.Get(r.Context(), user(r), n)
		reply(w, v, err)
	}))
	r.Put("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		var in SaveDraft
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Save(r.Context(), user(r), n, in)
		reply(w, v, err)
	}))
	r.Delete("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		if err := s.Discard(r.Context(), user(r), n); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r.Post("/api/authoring/drafts/{id}/submit", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		v, err := s.Submit(r.Context(), user(r), n)
		reply(w, v, err)
	}))
	r.Post("/api/authoring/drafts/{id}/rebase", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		v, err := s.Rebase(r.Context(), user(r), n)
		reply(w, v, err)
	}))
	r.Get("/api/authoring/drafts/{id}/files", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		v, err := s.Files(r.Context(), user(r), n)
		reply(w, v, err)
	}))
	r.Get("/api/authoring/drafts/{id}/file", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		p := r.URL.Query().Get("path")
		body, err := s.File(r.Context(), user(r), n, p)
		reply(w, map[string]string{"path": p, "content": body}, err)
	}))
}
