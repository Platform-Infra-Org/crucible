// Package httpapi wires every module's routes into one router.
package httpapi

import (
	"crypto/subtle"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"crucible/internal/agenthub"
	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/configapi"
	"crucible/internal/gitsync"
	"crucible/internal/httpx"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

type Deps struct {
	Auth       auth.Store
	OIDC       *auth.OIDC
	Sync       *gitsync.Syncer
	Learn      *learn.Service
	Labs       *labs.Service
	Notify     *notify.Service
	Config     *configapi.Service
	Hub        *agenthub.Hub
	PublicURL  string
	HookSecret string
	WebDir     string
}

func NewRouter(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	if d.OIDC != nil {
		r.Get("/auth/login", d.OIDC.Login)
		r.Get("/auth/callback", d.OIDC.Callback)
		r.Post("/auth/logout", d.OIDC.Logout)
	}
	r.Get("/api/meta", func(w http.ResponseWriter, _ *http.Request) {
		meta := map[string]any{"quotes": []string{}, "default_theme": "forge"}
		if st := state(d); st != nil && st.Platform != nil {
			meta["quotes"], meta["default_theme"] = st.Platform.Settings.Quotes, st.Platform.Settings.DefaultTheme
		}
		httpx.JSON(w, http.StatusOK, meta)
	})
	r.Post("/api/git/hook", func(w http.ResponseWriter, r *http.Request) {
		if d.HookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Crucible-Secret")), []byte(d.HookSecret)) != 1 {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "not found"))
			return
		}
		d.Sync.Trigger()
		w.WriteHeader(http.StatusAccepted)
	})
	r.Get("/api/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, err := d.Auth.UserByAgentToken(r.Context(), tok)
		if err != nil || u == nil || tok == "" {
			httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid pairing token"})
			return
		}
		d.Hub.Serve(w, r, u.ID)
	})

	r.Group(func(r chi.Router) {
		r.Use(d.Auth.Middleware, auth.RequireUser)
		r.Get("/api/me", func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context())
			admin, theme, teams, canApprove := false, "forge", []string{}, false
			if st := state(d); st != nil && st.Platform != nil {
				c := rbac.Checker{P: st.Platform}
				admin, theme = c.IsAdmin(u.Email), st.Platform.Settings.DefaultTheme
				canApprove = admin
				for id, t := range st.Platform.Teams {
					if t.RoleOf(u.Email) != "" {
						teams = append(teams, id)
					}
					canApprove = canApprove || t.Leader == u.Email
					for _, p := range t.Programs {
						canApprove = canApprove || slices.Contains(p.Roles.Approvers, u.Email)
					}
				}
				sort.Strings(teams)
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "is_admin": admin, "default_theme": theme, "teams": teams, "can_approve": canApprove})
		})
		r.Put("/api/me/prefs", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Theme      string `json:"theme"`
				CalmMotion bool   `json:"calm_motion"`
			}
			if err := httpx.Read(r, &body); err != nil {
				httpx.Error(w, err)
				return
			}
			if body.Theme != "" && !contains(config.Themes, body.Theme) {
				httpx.Error(w, apperr.Wrap(apperr.Invalid, "unknown theme"))
				return
			}
			if err := d.Auth.SetPrefs(r.Context(), auth.UserFrom(r.Context()).ID, body.Theme, body.CalmMotion); err != nil {
				httpx.Error(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		r.Post("/api/agent/tokens", func(w http.ResponseWriter, r *http.Request) {
			tok, err := d.Auth.CreateAgentToken(r.Context(), auth.UserFrom(r.Context()).ID)
			if err != nil {
				httpx.Error(w, err)
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]string{"token": tok,
				"command": "CRUCIBLE_TOKEN=" + tok + " crucible-agent --server " + d.PublicURL}) // env, not argv: other local users can read argv
		})
		r.Get("/api/agent/status", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, http.StatusOK, map[string]bool{"online": d.Hub.Online(auth.UserFrom(r.Context()).ID)})
		})
		d.Learn.Routes(r)
		d.Labs.Routes(r)
		if d.Notify != nil {
			d.Notify.Routes(r)
		}
		if d.Config != nil {
			d.Config.Routes(r)
		}
	})

	r.NotFound(spa(d.WebDir))
	return r
}

func state(d Deps) *gitsync.State {
	if d.Sync == nil {
		return nil
	}
	return d.Sync.Current()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// spa serves built frontend files and falls back to index.html for client-side routes.
func spa(dir string) http.HandlerFunc {
	files := http.FileServer(http.Dir(dir))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "not found"))
			return
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path.Clean(r.URL.Path)))); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	}
}
