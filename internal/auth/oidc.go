package auth

import (
	"context"
	"fmt"
	"net/http"
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
	return &OIDC{
		cfg: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}},
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
		store:    store,
		secure:   secure,
	}, nil
}

func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	state, verifier := newToken(), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state + "." + verifier, Path: "/auth",
		MaxAge: 600, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, o.cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
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
	var claims struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil || claims.Email == "" {
		http.Error(w, "your identity provider did not send an email address", http.StatusForbidden)
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

func valueOr(c *http.Cookie, err error) string {
	if err != nil {
		return ""
	}
	return c.Value
}
