package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"crucible/internal/agenthub"
	"crucible/internal/labs"
	"crucible/internal/learn"
)

func TestHealthzAndSPAFallback(t *testing.T) {
	web := t.TempDir()
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>forge</html>"), 0o644)
	r := NewRouter(Deps{Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New(), WebDir: web})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/p/forge/forge-101", nil))
	if w.Code != 200 || w.Body.String() != "<html>forge</html>" {
		t.Fatalf("spa fallback: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/nope", nil))
	if w.Code != 404 {
		t.Fatalf("unknown api route must 404, got %d", w.Code)
	}
}
