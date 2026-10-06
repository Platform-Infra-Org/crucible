package scoring

import (
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	id := func(r *http.Request) (int64, error) {
		v, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			return 0, apperr.Wrap(apperr.NotFound, "submission not found")
		}
		return v, nil
	}
	r.Get("/api/anvil", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := s.Queue(r.Context(), user(r), Filter{Training: q.Get("training"), Trainee: q.Get("trainee"), Type: q.Get("type")})
		reply(w, v, err)
	})
	r.Get("/api/anvil/signoffs", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.SignOffs(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/anvil/signoffs", func(w http.ResponseWriter, r *http.Request) {
		var in SignOffInput
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SignOff(r.Context(), user(r), in)
		replyScorer(w, v, err)
	})
	r.Get("/api/anvil/{id}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Detail(r.Context(), user(r), n)
		reply(w, v, err)
	})
	r.Post("/api/anvil/{id}/score", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Points   *float64 `json:"points"` // required: {} must never score 0
			Feedback string   `json:"feedback"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err == nil && body.Points == nil {
			err = apperr.Wrap(apperr.Invalid, "points are required")
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Score(r.Context(), user(r), n, *body.Points, body.Feedback)
		replyScorer(w, v, err)
	})
	r.Post("/api/anvil/{id}/return", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Feedback string `json:"feedback"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Return(r.Context(), user(r), n, body.Feedback)
		replyScorer(w, v, err)
	})
	r.Post("/api/anvil/{id}/override", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Task   string   `json:"task"`
			Points *float64 `json:"points"`
			Reason string   `json:"reason"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err == nil && body.Points == nil {
			err = apperr.Wrap(apperr.Invalid, "points are required")
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Override(r.Context(), user(r), n, body.Task, *body.Points, body.Reason)
		reply(w, v, err)
	})
	r.Get("/api/submissions/{id}/files/{n}", func(w http.ResponseWriter, r *http.Request) {
		sid, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		n, err := strconv.Atoi(chi.URLParam(r, "n"))
		if err != nil {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "file not found"))
			return
		}
		rc, name, err := s.File(r.Context(), user(r), sid, n)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		defer rc.Close()
		// Uploads are trainee-controlled: never rendered in our origin.
		w.Header().Set("Content-Type", "application/octet-stream")
		// FormatMediaType quotes ASCII names and RFC 2231/6266-encodes the rest as filename*=utf-8''….
		cd := mime.FormatMediaType("attachment", map[string]string{"filename": name})
		if cd == "" {
			cd = "attachment"
		}
		w.Header().Set("Content-Disposition", cd)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		_, _ = io.Copy(w, rc)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// replyScorer answers a scorer with the submission's rubric (Submission alone never serializes it).
func replyScorer(w http.ResponseWriter, v *Submission, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v.ScorerView())
}
