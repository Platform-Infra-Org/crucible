package journey

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	r.Get("/api/teams/{team}/journey", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.Team(r.Context(), auth.UserFrom(r.Context()), chi.URLParam(r, "team"))
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, rows)
	})
}
