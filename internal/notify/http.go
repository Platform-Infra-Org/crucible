package notify

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/httpx"
)

type kindPref struct {
	KindInfo
	Muted bool `json:"muted"`
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		muted, err := s.Mutes(r.Context(), auth.UserFrom(r.Context()).ID)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		out := []kindPref{}
		for _, k := range Kinds {
			out = append(out, kindPref{k, slices.Contains(muted, k.Kind)})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"email_enabled": s.SMTP.Addr != "", "kinds": out})
	})
	r.Put("/api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Muted []Kind `json:"muted"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		if err := s.SetMutes(r.Context(), auth.UserFrom(r.Context()).ID, body.Muted); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
