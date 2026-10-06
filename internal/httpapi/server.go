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
	"crucible/internal/edits"
	"crucible/internal/gitsync"
	"crucible/internal/httpx"
	"crucible/internal/journey"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/notify"
	"crucible/internal/rbac"
	"crucible/internal/scoring"
)

type Deps struct {
	Auth       auth.Store
	OIDC       *auth.OIDC
	Sync       *gitsync.Syncer
	Learn      *learn.Service
	Labs       *labs.Service
	Scoring    *scoring.Service
	Notify     *notify.Service
	Config     *configapi.Service
	Journey    *journey.Service
	Edits      *edits.Service
	Hub        *agenthub.Hub
	IsAdmin    func(email string) bool // tests; nil means "listed in admins.yaml"
	PublicURL  string
	HookSecret string
	WebDir     string
	// PreviewToken is set only by crucible preview (see auth.PreviewAllowed): /auth/preview replaces the OIDC sign-in.
	PreviewToken string
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
	if d.PreviewToken != "" {
		r.Get("/auth/preview", d.Auth.PreviewLogin(d.PreviewToken, false))
		r.Get("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Crucible preview: open the sign-in link that `crucible preview` printed in your terminal.\n"))
		})
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
			admin, theme, teams, canApprove, scorer, canSpend, mentor := false, "forge", []string{}, false, false, false, false
			if st := state(d); st != nil && st.Platform != nil {
				c := rbac.Checker{P: st.Platform}
				admin, theme = c.IsAdmin(u.Email), st.Platform.Settings.DefaultTheme
				canApprove, scorer = admin, canScore(st.Platform, u.Email)
				canSpend = len(c.SpendTeams(u.Email)) > 0
				mentor = isMentor(st.Platform, u.Email)
				for id, t := range st.Platform.Teams {
					if configapi.Role(t, u.Email) != "" { // team role or a program role in one of its programs
						teams = append(teams, id)
					}
					canApprove = canApprove || t.Leader == u.Email
					for _, p := range t.Programs {
						canApprove = canApprove || slices.Contains(p.Roles.Approvers, u.Email)
					}
				}
				sort.Strings(teams)
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "is_admin": admin, "default_theme": theme, "teams": teams, "can_approve": canApprove, "can_score": scorer, "can_view_spend": canSpend, "is_mentor": mentor,
				"can_edit_content": d.Edits != nil && d.Edits.CanUse(u.Email)})
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
		r.Delete("/api/agent/tokens", func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context()) // the caller's own tokens
			if err := d.Auth.RevokeAgentTokens(r.Context(), u.ID, ""); err != nil {
				httpx.Error(w, err)
				return
			}
			d.Hub.Drop(u.ID) // the laptop's labs keep running until idle/TTL, as on any disconnect (spec §14)
			w.WriteHeader(http.StatusNoContent)
		})
		r.Delete("/api/admin/agent/tokens", func(w http.ResponseWriter, r *http.Request) { // offboarding: any user's pairing
			u := auth.UserFrom(r.Context())
			if !isAdmin(d, u.Email) {
				httpx.Error(w, apperr.Wrap(apperr.Forbidden, "admins only"))
				return
			}
			email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
			id, err := d.Auth.UserIDByEmail(r.Context(), email)
			if err == nil && id == 0 {
				err = apperr.Wrap(apperr.NotFound, "nobody with that email has signed in")
			}
			if err == nil {
				err = d.Auth.RevokeAgentTokens(r.Context(), id, u.Email)
			}
			if err != nil {
				httpx.Error(w, err)
				return
			}
			d.Hub.Drop(id)
			w.WriteHeader(http.StatusNoContent)
		})
		r.Get("/api/agent/status", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, http.StatusOK, map[string]bool{"online": d.Hub.Online(auth.UserFrom(r.Context()).ID)})
		})
		d.Learn.Routes(r)
		d.Labs.Routes(r)
		if d.Scoring != nil {
			d.Scoring.Routes(r)
		}
		if d.Notify != nil {
			d.Notify.Routes(r)
		}
		if d.Config != nil {
			d.Config.Routes(r)
		}
		if d.Journey != nil {
			d.Journey.Routes(r)
		}
		if d.Edits != nil {
			d.Edits.Routes(r)
		}
	})

	r.NotFound(spa(d.WebDir))
	return r
}

func isAdmin(d Deps, email string) bool {
	if d.IsAdmin != nil {
		return d.IsAdmin(email)
	}
	st := state(d)
	return st != nil && st.Platform != nil && rbac.Checker{P: st.Platform}.IsAdmin(email)
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

// isMentor: someone in team.yaml mentors a trainee (spec §11); they get the mentor dashboard.
func isMentor(p *config.Platform, email string) bool {
	for _, t := range p.Teams {
		for _, m := range t.Mentors {
			if m == strings.ToLower(email) {
				return true
			}
		}
	}
	return false
}

// canScore: admins, and anyone listed as a scorer of some program (the Anvil link in the nav).
func canScore(p *config.Platform, email string) bool {
	if (rbac.Checker{P: p}).IsAdmin(email) {
		return true
	}
	for _, t := range p.Teams {
		for _, pr := range t.Programs {
			if slices.Contains(pr.Roles.Scorers, strings.ToLower(email)) {
				return true
			}
		}
	}
	return false
}
