package auth

import (
	"context"
	"net/http"

	"crucible/internal/httpx"
)

const SessionCookie = "crucible_session"

type ctxKey struct{}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Middleware attaches the session's user (if any) to the request context.
func (s Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			if u, err := s.UserBySession(r.Context(), c.Value); err == nil && u != nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
