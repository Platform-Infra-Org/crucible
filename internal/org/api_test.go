package org

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
)

type apiFixture struct {
	t         *testing.T
	s         *Store
	h         http.Handler
	mu        sync.Mutex
	snap      *config.Platform
	refreshes int
	pinCalls  []string
	pinErr    error
}

// newAPI seeds team "platform" (leader lead@x, senior senior@x, trainee new@x, other team "other" led by boss@x),
// admin@x as admin, and serves the routes with a snapshot that only changes when Refresh runs.
func newAPI(t *testing.T) *apiFixture {
	s, ctx := orgFixture(t)
	mustTeam(t, s, "other", TeamBody{Name: "Other", Leader: "boss@x"})
	if err := s.AddAdmin(ctx, "root", "admin@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{}); err != nil {
		t.Fatal(err)
	}
	f := &apiFixture{t: t, s: s}
	f.reload()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u := r.Header.Get("X-User"); u != "" {
				r = r.WithContext(auth.WithUser(r.Context(), &auth.User{Email: u}))
			}
			next.ServeHTTP(w, r)
		})
	})
	s.Routes(r, APIDeps{
		Platform: func() *config.Platform { f.mu.Lock(); defer f.mu.Unlock(); return f.snap },
		Refresh:  func(context.Context) error { f.mu.Lock(); f.refreshes++; f.mu.Unlock(); f.reload(); return nil },
		CheckPin: func(_ context.Context, _, sha string) error { f.pinCalls = append(f.pinCalls, sha); return f.pinErr },
	})
	f.h = r
	return f
}

func (f *apiFixture) reload() {
	p, err := f.s.Platform(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.snap = p
	f.mu.Unlock()
}

func (f *apiFixture) do(user, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("X-User", user)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

func (f *apiFixture) teamVersion(id string) int64 { return f.snap.Teams[id].Version }

func (f *apiFixture) roster(version int64, leader, name string, seniors ...string) string {
	return fmt.Sprintf(`{"version":%d,"name":%q,"leader":%q,"seniors":[%s],"trainees":["new@x"]}`, version, name, leader, quoteAll(seniors))
}

func quoteAll(in []string) string {
	q := make([]string, len(in))
	for i, s := range in {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ",")
}

func (f *apiFixture) settingsVersion() int64 {
	var v int64
	if err := f.s.DB.QueryRow(context.Background(), `SELECT version FROM settings WHERE id = 1`).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func TestAdminRoutesAreAdminOnly(t *testing.T) {
	f := newAPI(t)
	v := f.settingsVersion()
	type route struct{ method, path, body string }
	routes := []route{
		{"GET", "/api/admin/settings", ""},
		{"PUT", "/api/admin/settings", fmt.Sprintf(`{"version":%d,"default_theme":"anvil"}`, v)},
		{"PUT", "/api/admin/schedules/nights", `{"timezone":"UTC","windows":[{"days":["sat"],"start":"09:00","end":"12:00"}]}`},
		{"PUT", "/api/admin/quotes", `{"quotes":["hit it"]}`},
		{"GET", "/api/admin/admins", ""},
		{"POST", "/api/admin/admins", `{"email":"second@x"}`},
		{"GET", "/api/admin/trainings", ""},
		{"POST", "/api/admin/trainings", `{"id":"new-101","repo":"https://git.example.com/n.git"}`},
		{"POST", "/api/admin/teams/fresh", `{"name":"Fresh","leader":"f@x"}`},
		{"PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"https://hooks.example.com/secret-token"}`},
		{"DELETE", "/api/admin/teams/fresh", ""},
		{"DELETE", "/api/admin/trainings/new-101", ""},
		{"DELETE", "/api/admin/admins/second@x", ""},
		{"DELETE", "/api/admin/schedules/nights", ""},
	}
	before := count(t, f.s, `SELECT count(*) FROM audit_log`)
	for _, who := range []string{"lead@x", "senior@x", "new@x", "boss@x", "nobody@x"} {
		for _, rt := range routes {
			if w := f.do(who, rt.method, rt.path, rt.body); w.Code != 403 {
				t.Errorf("%s %s %s = %d, want 403: %s", who, rt.method, rt.path, w.Code, w.Body)
			}
		}
	}
	if w := f.do("", "GET", "/api/admin/admins", ""); w.Code != 401 {
		t.Errorf("signed out = %d, want 401", w.Code)
	}
	if f.refreshes != 0 || count(t, f.s, `SELECT count(*) FROM audit_log`) != before {
		t.Fatalf("refused requests changed something (refreshes %d)", f.refreshes)
	}
	for _, rt := range routes {
		if w := f.do("admin@x", rt.method, rt.path, rt.body); w.Code/100 != 2 {
			t.Errorf("admin %s %s = %d: %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

func TestSettingsPutRefreshesTheSnapshot(t *testing.T) {
	f := newAPI(t)
	v := f.settingsVersion()
	w := f.do("admin@x", "PUT", "/api/admin/settings", fmt.Sprintf(`{"version":%d,"default_theme":"anvil"}`, v))
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"version":%d`, v+1)) {
		t.Fatalf("PUT = %d %s, want the new version %d", w.Code, w.Body, v+1)
	}
	if f.refreshes != 1 || f.snap.Settings.DefaultTheme != "anvil" {
		t.Errorf("refreshes = %d, theme = %q: the next request must see the change", f.refreshes, f.snap.Settings.DefaultTheme)
	}
	if g := f.do("admin@x", "GET", "/api/admin/settings", ""); !strings.Contains(g.Body.String(), `"default_theme":"anvil"`) || !strings.Contains(g.Body.String(), fmt.Sprintf(`"version":%d`, v+1)) {
		t.Errorf("GET = %s", g.Body)
	}
}

func TestConflictIsReportedAsConflict(t *testing.T) {
	f := newAPI(t)
	v := f.settingsVersion()
	body := fmt.Sprintf(`{"version":%d,"default_theme":"anvil"}`, v)
	if w := f.do("admin@x", "PUT", "/api/admin/settings", body); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := f.do("admin@x", "PUT", "/api/admin/settings", body) // same, now stale, version
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "reload") {
		t.Fatalf("stale save = %d %s, want 409 telling the person to reload", w.Code, w.Body)
	}
	if w := f.do("admin@x", "POST", "/api/admin/teams/platform", `{"name":"Again","leader":"x@x"}`); w.Code != 409 {
		t.Errorf("duplicate team = %d, want 409", w.Code)
	}
	if w := f.do("admin@x", "DELETE", "/api/admin/trainings/forge-101", ""); w.Code != 409 {
		t.Errorf("training in use = %d, want 409", w.Code)
	}
}

func TestStatusesFollowTheErrorKind(t *testing.T) {
	f := newAPI(t)
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/admin/admins", `{"email":"not an email"}`, 400},
		{"DELETE", "/api/admin/teams/ghost", "", 404},
		{"DELETE", "/api/admin/schedules/ghost", "", 404},
		{"DELETE", "/api/admin/admins/admin@x", "", 400}, // the only admin
	} {
		if w := f.do("admin@x", c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s %s = %d, want %d: %s", c.method, c.path, w.Code, c.want, w.Body)
		}
	}
	f.s.DB.Close()
	if w := f.do("admin@x", "GET", "/api/admin/admins", ""); w.Code != 500 || strings.Contains(w.Body.String(), "pool") {
		t.Errorf("database failure = %d %s, want a bare 500", w.Code, w.Body)
	}
}

func TestUnknownFieldsAreRefusedOnEveryWriteRoute(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/admin/settings", `{"version":1,"escalation_hourz":2}`},
		{"PUT", "/api/admin/schedules/n", `{"timezone":"UTC","windowz":[]}`},
		{"PUT", "/api/admin/quotes", `{"quote":["x"]}`},
		{"POST", "/api/admin/admins", `{"emial":"a@x"}`},
		{"POST", "/api/admin/trainings", `{"id":"t-1","repo":"https://g/x.git","brnach":"m"}`},
		{"POST", "/api/admin/teams/fresh", `{"name":"F","leader":"f@x","leaders":["g@x"]}`},
		{"PUT", "/api/admin/teams/platform/webhooks/slack", `{"urll":"https://h/x"}`},
		{"PUT", "/api/org/teams/platform/roster", fmt.Sprintf(`{"version":%d,"name":"Platform","leader":"lead@x","seniors":["senior@x"],"traniees":["x@x"]}`, v)},
		{"PUT", "/api/org/teams/platform/programs/forge-101/pin", `{"shaa":"` + sha1 + `"}`},
	} {
		w := f.do("admin@x", c.method, c.path, c.body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "unknown field") {
			t.Errorf("%s %s = %d %s, want 400 naming the unknown field", c.method, c.path, w.Code, w.Body)
		}
	}
	if f.refreshes != 0 || len(f.pinCalls) != 0 {
		t.Error("a misspelled key must change nothing")
	}
}

func TestLeaderCannotChangeLeaderNameOrSeniors(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	for name, body := range map[string]string{
		"leader":  f.roster(v, "senior@x", "Platform", "senior@x"), // promote someone else, demote self
		"self":    f.roster(v, "lead@x", "Platform", "senior@x", "new@x"),
		"name":    f.roster(v, "lead@x", "Platform 2", "senior@x"),
		"seniors": f.roster(v, "lead@x", "Platform"), // demote the senior
	} {
		w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", body)
		if w.Code != 403 {
			t.Errorf("leader changing %s = %d %s, want 403", name, w.Code, w.Body)
		}
	}
	if f.refreshes != 0 || f.teamVersion("platform") != v || audits(t, f.s, "team.roster") != 0 {
		t.Fatal("a refused change was applied")
	}
	// An admin may change them.
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/roster", f.roster(v, "senior@x", "Platform", "lead@x")); w.Code != 200 {
		t.Errorf("admin changing the leader = %d %s", w.Code, w.Body)
	}
}

func TestLeaderEditsTheRestOfTheRoster(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	body := fmt.Sprintf(`{"version":%d,"name":"Platform","leader":"LEAD@x","seniors":["senior@x"],"trainees":["new@x","more@x"]}`, v)
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", body); w.Code != 200 {
		t.Fatalf("leader roster edit = %d %s", w.Code, w.Body)
	}
	if got := f.snap.Teams["platform"].Trainees; len(got) != 2 || f.snap.Teams["platform"].Version != v+1 {
		t.Errorf("trainees = %v version = %d", got, f.snap.Teams["platform"].Version)
	}
	// others may not
	for _, who := range []string{"senior@x", "new@x", "boss@x"} {
		if w := f.do(who, "PUT", "/api/org/teams/platform/roster", f.roster(v+1, "lead@x", "Platform", "senior@x")); w.Code != 403 {
			t.Errorf("%s = %d, want 403", who, w.Code)
		}
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/ghost/roster", f.roster(1, "a@x", "G")); w.Code != 404 {
		t.Errorf("admin, unknown team = %d, want 404", w.Code)
	}
	if w := f.do("lead@x", "PUT", "/api/org/teams/ghost/roster", f.roster(1, "a@x", "G")); w.Code != 403 {
		t.Errorf("leader, unknown team = %d, want 403 (no hint which teams exist)", w.Code)
	}
}

// The check is made on the team the client saw: a body for another version is a Conflict and is never judged against
// the newer snapshot, and the store only writes when the stored version still equals the one checked.
func TestRosterPermissionCheckMatchesTheVersionWritten(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	old := f.snap
	// An admin makes the senior the leader and the leader a senior.
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/roster", f.roster(v, "senior@x", "Platform", "lead@x")); w.Code != 200 {
		t.Fatal(w.Body)
	}
	// lead@x is no longer leader: the stale form is refused outright.
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.roster(v, "lead@x", "Platform", "senior@x")); w.Code != 403 {
		t.Errorf("former leader = %d, want 403", w.Code)
	}
	// The new leader sends the old version (the page was open before the change): Conflict, not a write.
	w := f.do("senior@x", "PUT", "/api/org/teams/platform/roster", f.roster(v, "senior@x", "Platform", "lead@x"))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "reload") {
		t.Errorf("stale version = %d %s, want 409", w.Code, w.Body)
	}
	// A snapshot that lags the database (refresh not run yet) still cannot write: the store's version check refuses.
	f.mu.Lock()
	f.snap = old
	f.mu.Unlock()
	lagged := f.teamVersion("platform")
	if f.teamVersion("platform") != lagged {
		t.Fatal("snapshot changed unexpectedly")
	}
	if p, _ := f.s.Platform(context.Background()); p.Teams["platform"].Leader != "senior@x" {
		t.Error("the stale write was applied")
	}
}

func TestWebhookURLNeverLeavesAdminContext(t *testing.T) {
	f := newAPI(t)
	const secret = "https://hooks.example.com/services/T000/B000/s3cr3t"
	if w := f.do("lead@x", "PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"`+secret+`"}`); w.Code != 403 || strings.Contains(w.Body.String(), "s3cr3t") {
		t.Fatalf("leader setting a webhook = %d %s", w.Code, w.Body)
	}
	w := f.do("admin@x", "PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"`+secret+`"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "s3cr3t") {
		t.Fatalf("admin set = %d %s", w.Code, w.Body)
	}
	if w := f.do("admin@x", "PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"http://hooks.example.com/s3cr3t"}`); w.Code != 400 || strings.Contains(w.Body.String(), "s3cr3t") {
		t.Errorf("rejected webhook echoed its URL: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ user, method, path, body string }{
		{"admin@x", "GET", "/api/admin/settings", ""}, {"admin@x", "GET", "/api/admin/admins", ""}, {"admin@x", "GET", "/api/admin/trainings", ""},
		{"lead@x", "PUT", "/api/org/teams/platform/roster", f.roster(f.teamVersion("platform"), "lead@x", "Platform", "senior@x")},
		{"lead@x", "PUT", "/api/org/teams/platform/roster", f.roster(1, "x@x", "Platform")},
		{"lead@x", "PUT", "/api/org/teams/platform/programs/forge-101/pin", `{"sha":"zz"}`},
	} {
		if w := f.do(c.user, c.method, c.path, c.body); strings.Contains(w.Body.String(), "s3cr3t") {
			t.Errorf("%s %s leaked the webhook: %s", c.method, c.path, w.Body)
		}
	}
	if n := count(t, f.s, `SELECT count(*) FROM audit_log WHERE detail::text LIKE '%s3cr3t%'`); n != 0 {
		t.Errorf("%d audit rows hold the webhook URL", n)
	}
	if n := f.snap.Teams["platform"].Notifications.SlackWebhook; n != secret {
		t.Errorf("stored webhook = %q", n)
	}
}

func TestPinMustResolveBeforeItIsStored(t *testing.T) {
	f := newAPI(t)
	path := "/api/org/teams/platform/programs/forge-101/pin"
	f.pinErr = apperr.Wrap(apperr.Invalid, "only a validated commit on the training's branch can be pinned")
	w := f.do("lead@x", "PUT", path, `{"sha":"`+sha1+`"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), sha1) {
		t.Fatalf("unresolvable pin = %d %s, want 400 naming the sha", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM programs WHERE pinned_ref IS NOT NULL`); n != 0 {
		t.Fatal("an unverified sha was stored")
	}
	f.pinErr = nil
	if w := f.do("lead@x", "PUT", path, `{"sha":"`+sha1+`"}`); w.Code != 200 {
		t.Fatalf("good pin = %d %s", w.Code, w.Body)
	}
	if f.snap.Teams["platform"].Programs["forge-101"].PinnedRef != sha1 || f.refreshes != 1 {
		t.Error("pin not stored or snapshot not refreshed")
	}
	f.pinErr = errors.New("git exploded")
	if w := f.do("lead@x", "PUT", path, `{"sha":"`+sha2+`"}`); w.Code != 500 {
		t.Errorf("mirror failure = %d, want 500", w.Code)
	}
	f.pinErr = nil
	for _, who := range []string{"senior@x", "new@x", "boss@x"} {
		if w := f.do(who, "PUT", path, `{"sha":"`+sha2+`"}`); w.Code != 403 {
			t.Errorf("%s pinning = %d, want 403", who, w.Code)
		}
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/programs/ghost/pin", `{"sha":"`+sha2+`"}`); w.Code != 404 {
		t.Errorf("unknown program = %d, want 404", w.Code)
	}
	if w := f.do("lead@x", "PUT", path, `{"sha":""}`); w.Code != 200 || f.snap.Teams["platform"].Programs["forge-101"].PinnedRef != "" {
		t.Errorf("clearing the pin = %d", w.Code)
	}
}
