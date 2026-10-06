package httpapi

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"crucible/internal/apperr"
	"crucible/internal/httpx"
)

// csp is the SPA and API default. Handlers that serve user files set their own stricter `sandbox` policy first.
// style-src needs 'unsafe-inline' (mermaid and xterm inject <style>); fonts are self-hosted (web/src/theme/fonts.css);
// img-src has no remote hosts, so edit previews cannot load external images.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"font-src 'self'; img-src 'self' data: blob:; connect-src 'self' %WS%; " +
	"frame-ancestors 'none'; base-uri 'self'; object-src 'none'; form-action 'self'"

// securityHeaders sets the default headers; a handler's own Header().Set (the download sandbox) wins.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Spelled out because older Safari does not match ws: against 'self'.
		h.Set("Content-Security-Policy", strings.Replace(csp, "%WS%", "ws://"+r.Host+" wss://"+r.Host, 1))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin is the CSRF guard beyond SameSite=Lax (a sibling subdomain is same-site): every state-changing request
// must come from our origin. Browsers always send Origin on these methods; clients without Origin/Referer/Sec-Fetch-Site
// (the CLI, the agent, tests) carry no ambient browser cookies, so they pass. Bodies must be JSON (or multipart, which
// the upload routes guard with X-Crucible-Upload), so a cross-site text/plain form cannot smuggle a JSON body.
func sameOrigin(public string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/api/git/hook" { // server-to-server, secret-guarded
				next.ServeHTTP(w, r)
				return
			}
			if !originAllowed(r, public) {
				httpx.Error(w, apperr.Wrap(apperr.Forbidden, "cross-origin request refused"))
				return
			}
			if r.ContentLength != 0 {
				ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if ct != "application/json" && ct != "multipart/form-data" {
					httpx.JSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send application/json"})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(r *http.Request, public string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			origin = ref
		} else {
			s := r.Header.Get("Sec-Fetch-Site")
			return s == "" || s == "same-origin" || s == "none"
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // includes Origin: null
	}
	if p, err := url.Parse(public); err == nil && p.Host != "" && u.Scheme == p.Scheme && u.Host == p.Host {
		return true
	}
	// Dev and tests reach the API under another name than PublicURL (127.0.0.1 vs localhost); the browser sets Host
	// to the server it is talking to, so a page from another origin cannot match it.
	return u.Host == r.Host
}
