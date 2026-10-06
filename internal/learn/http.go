package learn

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
	"crucible/internal/scoring"
)

func (s *Service) Routes(r chi.Router) {
	const p = "/api/programs/{team}/{training}"
	r.Get("/api/programs", func(w http.ResponseWriter, r *http.Request) {
		cards, err := s.Programs(r.Context(), auth.UserFrom(r.Context()))
		reply(w, cards, err)
	})
	r.Get(p, func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Outline(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"))
		reply(w, o, err)
	})
	r.Get(p+"/assets/*", func(w http.ResponseWriter, r *http.Request) {
		_, t, _, err := s.Program(auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"))
		if err != nil {
			httpx.Error(w, err)
			return
		}
		rel := filepath.FromSlash(chi.URLParam(r, "*"))
		if !filepath.IsLocal(rel) {
			httpx.Error(w, apperr.Wrap(apperr.Invalid, "bad asset path"))
			return
		}
		p := filepath.Join(t.Dir, "assets", rel)
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || !assetTypes[strings.ToLower(filepath.Ext(p))] {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "asset not found"))
			return
		}
		// Assets are author-controlled: never let one run script in our origin.
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, p)
	})
	r.Get(p+"/modules/{module}/reading/{item}", func(w http.ResponseWriter, r *http.Request) {
		title, md, err := s.Reading(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), param(r, "item"))
		reply(w, map[string]string{"title": title, "markdown": md}, err)
	})
	r.Post(p+"/modules/{module}/reading/{item}/read", func(w http.ResponseWriter, r *http.Request) {
		err := s.MarkRead(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), param(r, "item"))
		reply(w, nil, err)
	})
	r.Get(p+"/modules/{module}/quiz", func(w http.ResponseWriter, r *http.Request) {
		q, err := s.Quiz(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"))
		reply(w, q, err)
	})
	r.Post(p+"/modules/{module}/quiz/attempts", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answers map[string]json.RawMessage `json:"answers"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		res, err := s.SubmitQuiz(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), body.Answers)
		reply(w, res, err)
	})
	r.Post(p+"/modules/{module}/quiz/questions/{question}/answer", func(w http.ResponseWriter, r *http.Request) {
		answer, files, done, err := scoring.ReadForm(w, r)
		defer done()
		if err != nil {
			httpx.Error(w, err)
			return
		}
		q, err := s.AnswerHuman(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"),
			param(r, "module"), param(r, "question"), answer, files)
		reply(w, q, err)
	})
}

// assetTypes are the only files served from a training's assets/ (images and fonts).
var assetTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true, ".woff2": true}

func param(r *http.Request, k string) string { return chi.URLParam(r, k) }

func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err != nil:
		httpx.Error(w, err)
	case v == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.JSON(w, http.StatusOK, v)
	}
}
