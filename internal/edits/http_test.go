package edits

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
)

// One request per route: status codes, who gets what, and the action pattern.
func TestRoutes(t *testing.T) {
	f := setup(t)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { // X-User stands in for the session middleware
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), &auth.User{Email: req.Header.Get("X-User")})))
		})
	})
	f.s.Routes(r)
	call := func(method, path, user, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-User", user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	const leader, senior, trainee = "leader@crucible.local", "senior@crucible.local", "trainee@crucible.local"
	for _, c := range []struct {
		method, path, user, body string
		want                     int
	}{
		{"GET", "/api/content", trainee, "", 200},
		{"GET", "/api/content/t1/files", leader, "", 200},
		{"GET", "/api/content/t1/files", trainee, "", 403},
		{"GET", "/api/content/nope/files", leader, "", 404},
		{"GET", "/api/content/t1/file?path=modules/m1/quiz.yaml", leader, "", 200},
		{"GET", "/api/content/t1/file?path=modules/m1/quiz.yaml", trainee, "", 403},
		{"GET", "/api/content/t1/file?path=../x.md", leader, "", 400},
		{"GET", "/api/edits", trainee, "", 200},
		{"POST", "/api/edits", trainee, `{"training":"t1","base_sha":"` + f.head() + `","title":"x","files":{"modules/m1/reading/intro.md":"# Hi\n"}}`, 403},
		{"POST", "/api/edits", leader, `{"bogus":1}`, 400},
	} {
		if got, body := call(c.method, c.path, c.user, c.body); got != c.want {
			t.Errorf("%s %s as %s: %d %v, want %d", c.method, c.path, c.user, got, body, c.want)
		}
	}
	code, e := call("POST", "/api/edits", leader, `{"training":"t1","base_sha":"`+f.head()+`","title":"x","files":{"modules/m1/reading/intro.md":"# Hi\n"}}`)
	if code != 200 {
		t.Fatalf("propose: %d %v", code, e)
	}
	id := "/api/edits/" + itoa(int64(e["id"].(float64)))
	for _, c := range []struct {
		method, path, user, body string
		want                     int
	}{
		{"GET", id, leader, "", 200},
		{"GET", id, trainee, "", 404},
		{"GET", "/api/edits/abc", leader, "", 404},
		{"POST", id + "/approveX", senior, "{}", 404},
		{"POST", id + "/xwithdraw", leader, "{}", 404},
		{"POST", id + "/approve", leader, "{}", 403},
		{"POST", id + "/reject", trainee, "{}", 404},
		{"POST", id + "/withdraw", senior, "{}", 403},
		{"POST", id + "/approve", senior, "{}", 200},
		{"POST", id + "/reject", senior, "{}", 409},
	} {
		if got, body := call(c.method, c.path, c.user, c.body); got != c.want {
			t.Errorf("%s %s as %s: %d %v, want %d", c.method, c.path, c.user, got, body, c.want)
		}
	}
	code, e = call("POST", "/api/edits", leader, `{"training":"t1","base_sha":"`+f.s.State().Heads["t1"]+`","title":"y","files":{"modules/m1/reading/intro.md":"# Yo\n"}}`)
	if code != 200 {
		t.Fatalf("propose: %d %v", code, e)
	}
	if code, body := call("POST", "/api/edits/"+itoa(int64(e["id"].(float64)))+"/withdraw", leader, "{}"); code != 200 || body["status"] != "withdrawn" {
		t.Fatalf("withdraw: %d %v", code, body)
	}
}
