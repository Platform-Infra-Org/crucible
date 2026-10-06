package auth

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
)

// PreviewEmail is the author crucible preview signs in as.
const PreviewEmail = "preview@crucible.local"

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// PreviewAllowed refuses preview mode anywhere but an author's own machine: plain http on a loopback host and a long
// random token. crucible preview sets both; the Helm chart and the compose stack never set CRUCIBLE_PREVIEW_TOKEN.
func PreviewAllowed(publicURL, token string) error {
	if len(token) < 32 {
		return errors.New("CRUCIBLE_PREVIEW_TOKEN must be at least 32 characters")
	}
	if u, err := url.Parse(publicURL); err == nil && u.Scheme == "http" && loopback(u.Hostname()) {
		return nil
	}
	return errors.New("preview mode needs CRUCIBLE_PUBLIC_URL on http://localhost")
}

// PreviewLogin signs the browser in as the preview author when the request is addressed to a loopback host and
// ?token= matches; anything else is a plain 404. The Host check keeps a preview reachable by another name (a
// forwarded port, DNS rebinding) from signing anyone in; the 127.0.0.1-only port binding does the rest.
func (s Store) PreviewLogin(token string, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if token == "" || !loopback(host) || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			http.NotFound(w, r)
			return
		}
		u, err := s.UpsertUser(r.Context(), "crucible-preview", PreviewEmail, "Preview author")
		if err != nil {
			http.Error(w, "preview sign-in failed", http.StatusInternalServerError)
			return
		}
		sess, err := s.CreateSession(r.Context(), u.ID, sessionTTL)
		if err != nil {
			http.Error(w, "preview sign-in failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess, Path: "/", MaxAge: int(sessionTTL.Seconds()),
			HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
