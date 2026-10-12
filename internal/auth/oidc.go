package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Issuer       string // issuer as the browser sees it (must match tokens' iss)
	DiscoveryURL string // optional: where the server reaches the IdP (e.g. inside docker compose)
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type OIDC struct {
	cfg      oauth2.Config
	verifier *oidc.IDTokenVerifier
	store    Store
	secure   bool
	// endSession is the provider's RP-initiated logout endpoint, if it advertises one. Ending the provider's own
	// session is the only way to come back as somebody else: prompt=login only asks the same person for their
	// password again.
	endSession string
}

const oauthCookie = "crucible_oauth"
const sessionTTL = 12 * time.Hour

func NewOIDC(ctx context.Context, c OIDCConfig, store Store, secure bool) (*OIDC, error) {
	disc := c.Issuer
	if c.DiscoveryURL != "" && c.DiscoveryURL != c.Issuer {
		// The IdP is reachable at a different URL from the server than from the browser.
		ctx = oidc.InsecureIssuerURLContext(ctx, c.Issuer)
		disc = c.DiscoveryURL
	}
	p, err := oidc.NewProvider(ctx, disc)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery at %s: %w", disc, err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&meta) // optional: a provider without it falls back to asking for the password again
	return &OIDC{
		cfg: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}},
		verifier:   p.Verifier(&oidc.Config{ClientID: c.ClientID}),
		store:      store,
		secure:     secure,
		endSession: meta.EndSession,
	}, nil
}

// Login starts the sign-in. With ?switch=1 it asks the identity provider to authenticate again (OIDC prompt=login)
// rather than reuse the session it still holds: signing out of Crucible does not end that session, so without this
// the provider hands back the same person without asking, and nobody can change who they are signed in as.
func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	state, verifier := newToken(), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state + "." + verifier, Path: "/auth",
		MaxAge: 600, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if r.URL.Query().Get("switch") == "1" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "login"))
	}
	http.Redirect(w, r, o.cfg.AuthCodeURL(state, opts...), http.StatusFound)
}

func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oauthCookie)
	state, verifier, ok := strings.Cut(valueOr(c, err), ".")
	if !ok || r.URL.Query().Get("state") != state {
		http.Error(w, "login expired, please try again", http.StatusBadRequest)
		return
	}
	tok, err := o.cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "login failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := o.verifier.Verify(r.Context(), raw)
	if err != nil {
		http.Error(w, "invalid id token", http.StatusBadGateway)
		return
	}
	var claims idClaims
	if err := idt.Claims(&claims); err != nil {
		http.Error(w, "invalid id token claims", http.StatusBadGateway)
		return
	}
	if msg := claims.problem(); msg != "" {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	if claims.Name == "" {
		claims.Name = claims.Username
	}
	u, err := o.store.UpsertUser(r.Context(), idt.Subject, claims.Email, claims.Name)
	if err != nil {
		http.Error(w, "could not save user", http.StatusInternalServerError)
		return
	}
	session, err := o.store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/auth", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: session, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = o.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// Switch signs out here and, where the provider allows it, there too, then starts a fresh sign-in. Without ending
// the provider's session it would hand back the same person; with prompt=login alone it asks that person for their
// password rather than letting anyone else in.
func (o *OIDC) Switch(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = o.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1})
	back := strings.TrimSuffix(o.cfg.RedirectURL, "/auth/callback") + "/auth/login"
	if o.endSession == "" {
		http.Redirect(w, r, back+"?switch=1", http.StatusFound)
		return
	}
	q := url.Values{"client_id": {o.cfg.ClientID}, "post_logout_redirect_uri": {back}}
	http.Redirect(w, r, o.endSession+"?"+q.Encode(), http.StatusFound)
}

type idClaims struct {
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // bool, or "true"/"false" from some IdPs
	Name          string `json:"name"`
	Username      string `json:"preferred_username"`
}

// problem explains why these claims cannot log in, or returns "". Accounts are keyed by email, so an
// unverified email could claim someone else's enrolments.
func (c idClaims) problem() string {
	if c.Email == "" {
		return "your identity provider did not send an email address"
	}
	if v := c.EmailVerified; v != true && v != "true" {
		return "your email address is not verified with the identity provider; verify it and sign in again"
	}
	return ""
}

func valueOr(c *http.Cookie, err error) string {
	if err != nil {
		return ""
	}
	return c.Value
}
