package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crucible/internal/db/dbtest"
)

const goodToken = "0123456789abcdef0123456789abcdef"

func TestPreviewAllowed(t *testing.T) {
	for _, ok := range []string{"http://localhost:8090", "http://127.0.0.1:8090", "http://[::1]:8090"} {
		if err := PreviewAllowed(ok, goodToken); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for url, tok := range map[string]string{
		"https://crucible.example.com": goodToken, "http://crucible.example.com": goodToken, "http://10.0.0.5:8080": goodToken,
		"http://localhost:8090": "short-token", "not a url": goodToken, "https://localhost:8090": goodToken,
	} {
		if err := PreviewAllowed(url, tok); err == nil {
			t.Errorf("%s with %q must be refused", url, tok)
		}
	}
}

func TestPreviewLogin(t *testing.T) {
	s := Store{DB: dbtest.New(t)}
	h := s.PreviewLogin(goodToken, false)
	get := func(host, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/auth/preview?"+query, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	if w := get("localhost:8090", "token=wrong"); w.Code != http.StatusNotFound || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("wrong token: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := get("localhost:8090", ""); w.Code != http.StatusNotFound {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := get("crucible.example.com", "token="+goodToken); w.Code != http.StatusNotFound || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("a non-loopback host never signs in: %d", w.Code)
	}
	w := get("127.0.0.1:8090", "token="+goodToken)
	c := w.Result().Cookies()
	if w.Code != http.StatusFound || len(c) != 1 || c[0].Name != SessionCookie || !c[0].HttpOnly {
		t.Fatalf("login: %d %+v", w.Code, c)
	}
	u, err := s.UserBySession(context.Background(), c[0].Value)
	if err != nil || u == nil || !strings.EqualFold(u.Email, PreviewEmail) {
		t.Fatalf("session user %+v %v", u, err)
	}
}
