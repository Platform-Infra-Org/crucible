package docs

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

func page(fm, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\n" + fm + "\n---\n" + body)}
}

func TestLoadParsesAndOrders(t *testing.T) {
	pages, err := Load(fstest.MapFS{
		"trainees/quizzes.md":        page("title: Quizzes\nroles: [trainee]\ncovers: [route:/p/:team/:training/m/:module/quiz]\norder: 20", "# Quizzes\n\n## Attempts\n\n```\n## not a heading\n```\n### Cooldown\n"),
		"trainees/readings.md":       page("title: Readings\nroles: [trainee]\norder: 10", "Read.\n"),
		"getting-started/hearth.md":  page("title: The Hearth\nroles: [everyone]\ncovers: [route:/]", "Home.\n"),
		"admins/kill-switch.md":      page("title: Kill switch\nroles: [admin]", "Stop.\n"),
		"getting-started/README.txt": {Data: []byte("ignored")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range pages {
		slugs = append(slugs, p.Slug)
	}
	if got := strings.Join(slugs, " "); got != "getting-started/hearth trainees/readings trainees/quizzes admins/kill-switch" {
		t.Fatalf("order: %s", got)
	}
	q := pages[2]
	if q.Section != "trainees" || strings.Join(q.Headings, "|") != "Attempts|Cooldown" || !strings.HasPrefix(q.Body, "# Quizzes") {
		t.Fatalf("page: %+v", q)
	}
}

func TestLoadRefusesBadPages(t *testing.T) {
	for name, f := range map[string]*fstest.MapFile{
		"no front matter": {Data: []byte("# Hi\n")},
		"no title":        page("roles: [trainee]", "x"),
		"no roles":        page("title: X", "x"),
		"unknown role":    page("title: X\nroles: [wizard]", "x"),
		"unknown key":     page("title: X\nroles: [trainee]\ncover: [route:/]", "x"),
	} {
		if _, err := Load(fstest.MapFS{"trainees/x.md": f}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(fstest.MapFS{"wizards/x.md": page("title: X\nroles: [trainee]", "x")}); err == nil {
		t.Error("unknown section accepted")
	}
}

func TestRoutes(t *testing.T) {
	pages, _ := Load(fstest.MapFS{"trainees/quizzes.md": page("title: Quizzes\nroles: [trainee]", "# Quizzes\n")})
	r := chi.NewRouter()
	New(pages).Routes(r)
	get := func(path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, body := get("/api/docs"); code != 200 || body["version"] != Version || len(body["pages"].([]any)) != 1 {
		t.Fatalf("index: %d %v", code, body)
	}
	if code, body := get("/api/docs/trainees/quizzes"); code != 200 || body["markdown"] != "# Quizzes\n" {
		t.Fatalf("page: %d %v", code, body)
	}
	for _, p := range []string{"/api/docs/trainees/nope", "/api/docs/../../etc/passwd", "/api/docs/trainees/quizzes.md", "/api/docs/%2e%2e/x"} {
		if code, _ := get(p); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}
