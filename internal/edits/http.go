package edits

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
	id := func(r *http.Request) (int64, error) {
		n, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			return 0, apperr.Wrap(apperr.NotFound, "edit not found")
		}
		return n, nil
	}
	r.Get("/api/content", func(w http.ResponseWriter, r *http.Request) { reply(w, s.Trainings(user(r)), nil) })
	r.Get("/api/content/{training}/files", func(w http.ResponseWriter, r *http.Request) {
		sha, files, err := s.Files(user(r), chi.URLParam(r, "training"))
		reply(w, map[string]any{"head_sha": sha, "files": files}, err)
	})
	r.Get("/api/content/{training}/file", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		body, err := s.File(user(r), chi.URLParam(r, "training"), p)
		reply(w, map[string]string{"path": p, "content": body}, err)
	})
	r.Get("/api/edits", func(w http.ResponseWriter, r *http.Request) { v, err := s.List(r.Context(), user(r)); reply(w, v, err) })
	r.Post("/api/edits", func(w http.ResponseWriter, r *http.Request) {
		var in NewEdit
		if err := httpx.Read(r, &in); err != nil { // 1 MiB cap for the whole edit
			httpx.Error(w, err)
			return
		}
		e, err := s.Create(r.Context(), user(r), in)
		reply(w, e, err)
	})
	r.Get("/api/edits/{id}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		e, err := s.Get(r.Context(), user(r), n)
		reply(w, e, err)
	})
	r.Post("/api/edits/{id}/{action:(approve|reject|withdraw)}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		var body struct {
			Note string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		var e *Edit
		switch chi.URLParam(r, "action") {
		case "approve":
			e, err = s.Approve(r.Context(), user(r), n, body.Note)
		case "reject":
			e, err = s.Reject(r.Context(), user(r), n, body.Note)
		default:
			e, err = s.Withdraw(r.Context(), user(r), n)
		}
		reply(w, e, err)
	})
}
