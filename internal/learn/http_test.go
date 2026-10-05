package learn

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

func TestAssetsServeOnlySandboxedAllowlistedFiles(t *testing.T) {
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"logo.png": "\x89PNG", "x.svg": "<svg/>", "evil.html": "<script>alert(1)</script>", "sub/a.js": "alert(1)"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, "assets", name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "assets", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-101@abc": {ID: "forge-101", Dir: dir}},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}}
	s := &Service{State: func() *gitsync.State { return st }}
	r := chi.NewRouter()
	s.Routes(r)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/programs/forge/forge-101/assets/"+path, nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 1, Email: "trainee@crucible.local"}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, ok := range []string{"logo.png", "x.svg"} {
		w := get(ok)
		if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: code %d headers %v", ok, w.Code, w.Header())
		}
	}
	for _, bad := range []string{"evil.html", "sub/a.js", "sub", "sub/", "missing.png"} {
		if w := get(bad); w.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", bad, w.Code)
		}
	}
}
