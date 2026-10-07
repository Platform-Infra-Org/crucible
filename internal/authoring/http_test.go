package authoring

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
)

func router(f *fx) chi.Router {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { // X-User stands in for the session middleware
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), &auth.User{Email: req.Header.Get("X-User")})))
		})
	})
	f.s.Routes(r)
	return r
}

func call(t *testing.T, r chi.Router, method, path, user, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAuthoringRoutes(t *testing.T) {
	f := setup(t)
	r := router(f)
	const leader, trainee = "leader@crucible.local", "trainee@crucible.local"
	for _, c := range []struct {
		method, path, user, body string
		want                     int
	}{
		{"GET", "/api/authoring/schema?training=t1", leader, "", 200},
		{"GET", "/api/authoring/schema?training=t1", trainee, "", 403},
		{"GET", "/api/authoring/schema?training=nope", leader, "", 404},
		{"POST", "/api/authoring/validate", leader, `{"training":"t1","base_sha":"` + f.head() + `","ops":[]}`, 200},
		{"POST", "/api/authoring/validate", trainee, `{"training":"t1","base_sha":"` + f.head() + `","ops":[]}`, 403},
		{"POST", "/api/authoring/validate", leader, `{"training":"t1","base_sha":"` + f.head() + `","ops":[{"op":"chmod","path":"x"}]}`, 400},
		{"POST", "/api/authoring/validate", leader, `{"bogus":1}`, 400},
	} {
		if got, body := call(t, r, c.method, c.path, c.user, c.body); got != c.want {
			t.Errorf("%s %s as %s: %d %v, want %d", c.method, c.path, c.user, got, body, c.want)
		}
	}
	_, body := call(t, r, "GET", "/api/authoring/schema?training=t1", leader, "")
	if _, ok := body["quiz"].(map[string]any)["properties"]; !ok {
		t.Fatalf("schema: %v", body)
	}
}
