package authoring

import (
	"net/http"

	"github.com/go-chi/chi/v5"

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
}
