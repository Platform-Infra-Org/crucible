package auth

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestClaimsRequireVerifiedEmail(t *testing.T) {
	cases := map[string]bool{
		`{"email":"a@b.c","email_verified":true}`:    true,
		`{"email":"a@b.c","email_verified":"true"}`:  true,
		`{"email":"a@b.c","email_verified":false}`:   false,
		`{"email":"a@b.c","email_verified":"false"}`: false,
		`{"email":"a@b.c"}`:                          false,
		`{"email_verified":true}`:                    false,
	}
	for raw, ok := range cases {
		var c idClaims
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		if got := c.problem() == ""; got != ok {
			t.Errorf("%s: allowed=%v, want %v (%q)", raw, got, ok, c.problem())
		}
	}
}

// Signing out of Crucible does not end the provider's session, so a plain sign-in returns the same person. The
// Change user button asks the provider to authenticate again; an ordinary sign-in must not.
func TestLoginAsksTheProviderAgainOnlyWhenSwitching(t *testing.T) {
	o := &OIDC{cfg: oauth2.Config{ClientID: "crucible", RedirectURL: "https://c.example.com/auth/callback",
		Endpoint: oauth2.Endpoint{AuthURL: "https://idp.example.com/authorize"}}}
	for query, wantPrompt := range map[string]bool{"": false, "?switch=1": true, "?switch=0": false, "?switch=yes": false} {
		w := httptest.NewRecorder()
		o.Login(w, httptest.NewRequest("GET", "/auth/login"+query, nil))
		loc := w.Header().Get("Location")
		if got := strings.Contains(loc, "prompt=login"); got != wantPrompt {
			t.Errorf("login%q redirected to %q; prompt=login present = %v, want %v", query, loc, got, wantPrompt)
		}
	}
}

// Change user must end the provider's session, not just ours: ours alone would hand back the same person. A
// provider that advertises no logout endpoint falls back to asking for the password again, which is all it can do.
func TestSwitchEndsTheProviderSession(t *testing.T) {
	base := oauth2.Config{ClientID: "crucible", RedirectURL: "https://c.example.com/auth/callback"}
	// No session cookie on the request, so the store is never reached.
	o := &OIDC{cfg: base, endSession: "https://idp.example.com/logout"}
	w := httptest.NewRecorder()
	o.Switch(w, httptest.NewRequest("GET", "/auth/switch", nil))
	loc := w.Header().Get("Location")
	for _, want := range []string{"https://idp.example.com/logout", "client_id=crucible",
		"post_logout_redirect_uri=https%3A%2F%2Fc.example.com%2Fauth%2Flogin"} {
		if !strings.Contains(loc, want) {
			t.Errorf("switch went to %q, which lacks %q", loc, want)
		}
	}
	// The session cookie goes whatever the provider does.
	if c := w.Result().Cookies(); len(c) == 0 || c[0].Name != SessionCookie || c[0].MaxAge >= 0 {
		t.Errorf("the session cookie was not cleared: %+v", c)
	}

	none := &OIDC{cfg: base}
	w = httptest.NewRecorder()
	none.Switch(w, httptest.NewRequest("GET", "/auth/switch", nil))
	if got := w.Header().Get("Location"); got != "https://c.example.com/auth/login?switch=1" {
		t.Errorf("without a logout endpoint, switch went to %q", got)
	}
}
