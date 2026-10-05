package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	w := httptest.NewRecorder()
	NewRouter(Deps{}).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}
