package authoring

import (
	"context"
	"encoding/json"
	"fmt"
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
	const leader, senior, trainee = "leader@crucible.local", "senior@crucible.local", "trainee@crucible.local"
	d, err := f.s.Create(context.Background(), f.leader, NewDraft{Training: "t1", Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	draft := fmt.Sprintf("/api/authoring/drafts/%d", d.ID)
	big := `{"title":"x","base_sha":"` + d.BaseSHA + `","ops":[{"op":"put","path":"modules/m1/reading/intro.md","content":"` + strings.Repeat("x", 1<<20+1<<17) + `"}]}`
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
		{"GET", "/api/authoring/blocks?training=t1", leader, "", 200},
		{"GET", "/api/authoring/blocks?training=t1", trainee, "", 403},
		{"POST", "/api/authoring/insert", leader, `{"training":"t1","base_sha":"` + f.head() + `","block":"nope"}`, 404},
		{"POST", "/api/authoring/insert", leader, `{"training":"t1","base_sha":"` + f.head() + `","block":"template.lab.aws","values":{"module":"m1","id":"x","region":"eu-west-1","max_hourly_usd":"1"}}`, 400},
		{"POST", "/api/authoring/insert", trainee, `{"training":"t1","base_sha":"` + f.head() + `","block":"reading"}`, 403},
		{"GET", "/api/authoring/drafts", leader, "", 200},
		{"GET", "/api/authoring/drafts", trainee, "", 200},
		{"POST", "/api/authoring/drafts", leader, `{"training":"t1"}`, 200},
		{"POST", "/api/authoring/drafts", trainee, `{"training":"t1"}`, 403},
		{"GET", draft, leader, "", 200},
		{"GET", draft, senior, "", 404},
		{"GET", "/api/authoring/drafts/abc", leader, "", 404},
		{"GET", draft + "/files", leader, "", 200},
		{"GET", draft + "/file?path=../x.md", leader, "", 400},
		{"GET", draft + "/file?path=modules/m1/quiz.yaml", leader, "", 200},
		{"PUT", draft, leader, big, 400},
		{"POST", draft + "/rebase", leader, `{}`, 200},
		{"POST", draft + "/submit", leader, `{"updated_at":"2000-01-01T00:00:00Z"}`, 409},
		{"DELETE", draft, leader, "", 204},
	} {
		if got, body := call(t, r, c.method, c.path, c.user, c.body); got != c.want {
			t.Errorf("%s %s as %s: %d %v, want %d", c.method, c.path, c.user, got, body, c.want)
		}
	}
	d2, _ := f.s.Create(context.Background(), f.leader, NewDraft{Training: "t1"})
	if code, body := call(t, r, "PUT", fmt.Sprintf("/api/authoring/drafts/%d", d2.ID), leader, big); code != 400 || !strings.Contains(fmt.Sprint(body["error"]), "over 1 MiB") {
		t.Fatalf("an oversize draft says so: %d %v", code, body)
	}
	_, body := call(t, r, "GET", "/api/authoring/schema?training=t1", leader, "")
	if _, ok := body["quiz"].(map[string]any)["properties"]; !ok {
		t.Fatalf("schema: %v", body)
	}
}
