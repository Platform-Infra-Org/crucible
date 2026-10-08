package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crucible/internal/agenthub"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/org"
)

// TestMain allows the file transport: these tests use local bare repos as git remotes.
func TestMain(m *testing.M) {
	gitsync.AllowFileTransport = true
	os.Exit(m.Run())
}

func TestStateChangingRequestsNeedSameOrigin(t *testing.T) {
	reached := false
	h := sameOrigin("https://crucible.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for name, c := range map[string]struct {
		method, path string
		headers      map[string]string
		body         string
		want         int
	}{
		"cross-origin POST":            {"POST", "/api/admin/kill-switch", map[string]string{"Origin": "https://evil.example.com"}, "", 403},
		"sibling subdomain POST":       {"POST", "/api/admin/kill-switch", map[string]string{"Origin": "https://x.crucible.example.com"}, "", 403},
		"scheme downgrade":             {"POST", "/api/admin/kill-switch", map[string]string{"Origin": "http://crucible.example.com"}, "", 403},
		"Origin null":                  {"DELETE", "/api/agent/tokens", map[string]string{"Origin": "null"}, "", 403},
		"same-origin POST":             {"POST", "/api/admin/kill-switch", map[string]string{"Origin": "https://crucible.example.com"}, "", 204},
		"same host (dev name)":         {"PUT", "/api/me/prefs", map[string]string{"Origin": "http://example.com"}, "", 204}, // httptest Host is example.com
		"cross-site Referer":           {"POST", "/auth/logout", map[string]string{"Referer": "https://evil.example.com/page"}, "", 403},
		"same-origin Referer":          {"POST", "/auth/logout", map[string]string{"Referer": "https://crucible.example.com/settings"}, "", 204},
		"no Origin, cross-site fetch":  {"POST", "/api/labs/1/extend", map[string]string{"Sec-Fetch-Site": "cross-site"}, "", 403},
		"no Origin, same-site fetch":   {"POST", "/api/labs/1/extend", map[string]string{"Sec-Fetch-Site": "same-site"}, "", 403},
		"no headers (CLI, agent)":      {"POST", "/api/labs/1/extend", nil, "", 204},
		"GET with foreign Origin":      {"GET", "/api/me", map[string]string{"Origin": "https://evil.example.com"}, "", 204},
		"hook is exempt":               {"POST", "/api/git/hook", map[string]string{"Origin": "https://evil.example.com"}, "x", 204},
		"JSON body":                    {"POST", "/api/edits", map[string]string{"Content-Type": "application/json; charset=utf-8"}, "{}", 204},
		"text/plain body":              {"POST", "/api/edits", map[string]string{"Content-Type": "text/plain"}, "{}", 415},
		"form body":                    {"POST", "/api/edits", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, "a=b", 415},
		"multipart (upload routes)":    {"POST", "/api/x/upload", map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, "--x--", 204},
		"same-origin but form content": {"POST", "/api/edits", map[string]string{"Origin": "https://crucible.example.com", "Content-Type": "text/plain"}, "{}", 415},
	} {
		reached = false
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want || reached != (c.want == 204) {
			t.Errorf("%s: got %d (handler reached %v), want %d", name, w.Code, reached, c.want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	web := t.TempDir()
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>forge</html>"), 0o644)
	r := NewRouter(Deps{Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New(), WebDir: web})
	for _, p := range []string{"/p/forge/forge-101", "/api/meta", "/api/nope"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		h := w.Header()
		csp := h.Get("Content-Security-Policy")
		if strings.Contains(csp, "https:") {
			t.Errorf("%s: CSP %q allows a remote host (fonts are self-hosted)", p, csp)
		}
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "font-src 'self';", "style-src 'self' 'unsafe-inline';", "frame-ancestors 'none'", "object-src 'none'", "connect-src 'self' ws://example.com wss://example.com"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", p, csp, want)
			}
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("Permissions-Policy") == "" {
			t.Errorf("%s: headers %v", p, h)
		}
	}
	// Download handlers (assets, uploads, transcripts) set their own sandbox policy; it must win.
	w := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "sandbox")
	})).ServeHTTP(w, httptest.NewRequest("GET", "/api/x/assets/a.svg", nil))
	if got := w.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("download CSP = %q, want sandbox", got)
	}
}

// logCount counts "git sync failed" records: a Syncer pointed at a missing repo logs one per sync the hook triggers.
type logCount chan struct{}

func (c logCount) Enabled(context.Context, slog.Level) bool { return true }
func (c logCount) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "git sync failed" {
		c <- struct{}{}
	}
	return nil
}
func (c logCount) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c logCount) WithGroup(string) slog.Handler      { return c }

func TestGitHookNeedsTheSecret(t *testing.T) {
	syncs := make(logCount, 10)
	noConfig := func(context.Context) (*config.Platform, error) { return nil, errors.New("no configuration") }
	syncer := gitsync.New(t.TempDir(), noConfig, slog.New(syncs))
	r := NewRouter(Deps{Sync: syncer, HookSecret: "s3cret", Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New()})
	hook := func(secret string) int {
		req := httptest.NewRequest("POST", "/api/git/hook", strings.NewReader(`{"ref":"refs/heads/main"}`))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Crucible-Secret", secret)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if c := hook(""); c != 404 {
		t.Fatalf("no secret: %d", c)
	}
	if c := hook("wrong"); c != 404 {
		t.Fatalf("wrong secret: %d", c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go syncer.Run(ctx, time.Hour) // only a trigger makes it sync
	select {
	case <-syncs:
		t.Fatal("a rejected hook triggered a sync")
	case <-time.After(300 * time.Millisecond):
	}
	if c := hook("s3cret"); c != 202 {
		t.Fatalf("right secret: %d", c)
	}
	select {
	case <-syncs:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook did not trigger a sync")
	}

	// No secret configured: the hook is off, even for an empty header.
	off := NewRouter(Deps{Sync: syncer, Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New()})
	w := httptest.NewRecorder()
	off.ServeHTTP(w, httptest.NewRequest("POST", "/api/git/hook", nil))
	if w.Code != 404 {
		t.Fatalf("hook without a configured secret: %d", w.Code)
	}
}

func TestMetaServesThemeAndQuotes(t *testing.T) {
	get := func(r http.Handler) (meta struct {
		Quotes       []string `json:"quotes"`
		DefaultTheme string   `json:"default_theme"`
	}) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/meta", nil))
		if w.Code != 200 {
			t.Fatalf("meta: %d", w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
			t.Fatal(err)
		}
		return meta
	}
	// Before the first sync: defaults, and quotes is [] (not null) for the SPA.
	if m := get(NewRouter(Deps{Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New()})); m.DefaultTheme != "forge" || m.Quotes == nil || len(m.Quotes) != 0 {
		t.Fatalf("defaults: %+v", m)
	}

	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", "../../examples/platform/.", work).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	_ = os.WriteFile(filepath.Join(work, "platform.yaml"), []byte(strings.Replace(mustRead(t, filepath.Join(work, "platform.yaml")), "default_theme: forge", "default_theme: quench", 1)), 0o644)
	syncer := gitsync.New(t.TempDir(), func(context.Context) (*config.Platform, error) { return config.Load(work) }, slog.Default())
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := get(NewRouter(Deps{Sync: syncer, Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New()}))
	if m.DefaultTheme != "quench" || len(m.Quotes) != len(syncer.Current().Platform.Settings.Quotes) || len(m.Quotes) == 0 {
		t.Fatalf("synced meta: %+v", m)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The org routes sit behind the same guard as every other state-changing route: a browser request from elsewhere, or
// with a non-JSON body, never reaches them (it would otherwise answer 401 for a signed-out caller).
func TestOrgRoutesNeedSameOrigin(t *testing.T) {
	r := NewRouter(Deps{Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New(), Org: &org.Store{}, OrgAPI: org.APIDeps{},
		PublicURL: "https://crucible.example.com"})
	for _, c := range []struct {
		name, method, path string
		headers            map[string]string
		want               int
	}{
		{"cross-origin PUT", "PUT", "/api/admin/settings", map[string]string{"Origin": "https://evil.example.com"}, 403},
		{"Origin null", "POST", "/api/admin/admins", map[string]string{"Origin": "null"}, 403},
		{"no Origin, cross-site fetch", "DELETE", "/api/admin/teams/x", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"leader route, cross-origin", "PUT", "/api/org/teams/x/roster", map[string]string{"Origin": "https://evil.example.com"}, 403},
		{"pin route, cross-origin", "PUT", "/api/org/teams/x/programs/y/pin", map[string]string{"Origin": "https://evil.example.com"}, 403},
		{"form body", "PUT", "/api/admin/quotes", map[string]string{"Origin": "https://crucible.example.com", "Content-Type": "text/plain"}, 415},
		{"same origin, signed out: route exists, login required", "PUT", "/api/admin/settings", map[string]string{"Origin": "https://crucible.example.com", "Content-Type": "application/json"}, 401},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// Monaco must not loosen the CSP: its workers are same-origin files (docs-and-editor spec §5, §9).
func TestCSPStaysStrict(t *testing.T) {
	dirs := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, val, _ := strings.Cut(strings.TrimSpace(d), " ")
		dirs[name] = val
	}
	if dirs["script-src"] != "'self'" || dirs["default-src"] != "'self'" {
		t.Fatalf("script-src %q, default-src %q: never widen these for the editor", dirs["script-src"], dirs["default-src"])
	}
	if _, ok := dirs["worker-src"]; ok {
		t.Fatal("worker-src stays unset: it falls back to script-src 'self', which is what same-origin workers need")
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(dirs["connect-src"], "http") {
		t.Fatalf("csp: %s", csp)
	}
}
