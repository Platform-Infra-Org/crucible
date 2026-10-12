package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"crucible/internal/smith"
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

// newAPI seeds team "platform" (leader lead@x, senior senior@x, trainee new@x), "data" (leader dl@x, senior ds@x,
// trainee dt@x), "other" led by boss@x, admin@x as admin, forge-101 owned by platform, and serves the routes with a
// snapshot that only changes when Refresh runs.
func newAPI(t *testing.T) *apiFixture { return newAPIWith(t, true) }

func newAPIWith(t *testing.T, withPinCheck bool) *apiFixture {
	s, ctx := orgFixture(t)
	mustTeam(t, s, "other", body("Other", "boss@x:leader"))
	if err := s.AddAdmin(ctx, "root", "admin@x"); err != nil {
		t.Fatal(err)
	}
	// forge-101 is managed by the team's leader and edited by its seniors, which is what registering as a leader gives.
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101",
		ProgramBody{Roles: config.Access{Admins: []string{"lead@x"}, Editors: []string{"platform_seniors"}}, BudgetUSDMonth: usd(0)}); err != nil {
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
	deps := APIDeps{
		Platform: func() *config.Platform { f.mu.Lock(); defer f.mu.Unlock(); return f.snap },
		Refresh:  func(context.Context) error { f.mu.Lock(); f.refreshes++; f.mu.Unlock(); f.reload(); return nil },
		CheckPin: func(_ context.Context, _, sha string) error { f.pinCalls = append(f.pinCalls, sha); return f.pinErr },
		BotName:  "Crucible",
		BotEmail: "crucible@localhost",
	}
	if !withPinCheck {
		deps.CheckPin = nil
	}
	s.Routes(r, deps)
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

// roster is a roster body from "email:tag" pairs.
func (f *apiFixture) roster(version int64, name string, members ...string) string {
	b := body(name, members...)
	b.Version = version
	j, _ := json.Marshal(b)
	return string(j)
}

// platform is team platform's roster with its tags changed.
func (f *apiFixture) platform(version int64, lead, senior, trainee string) string {
	return f.roster(version, "Platform", "lead@x:"+lead, "senior@x:"+senior, "new@x:"+trainee)
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
		{"PUT", "/api/admin/trainings/forge-101/owner", `{"team":"other"}`},
		{"POST", "/api/admin/teams/fresh", `{"name":"Fresh","members":{"f@x":"leader"}}`},
		{"PUT", "/api/admin/teams/platform/webhooks/slack", `{"url":"https://hooks.example.com/secret-token"}`},
		{"DELETE", "/api/admin/teams/fresh", ""},
		{"DELETE", "/api/admin/admins/second@x", ""},
		{"DELETE", "/api/admin/schedules/nights", ""},
		{"GET", "/api/admin/export", ""},
		{"POST", "/api/admin/import", `{"crucible_export":1}`},
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
	for _, rt := range routes[:len(routes)-1] { // the import has its own test
		if w := f.do("admin@x", rt.method, rt.path, rt.body); w.Code/100 != 2 {
			t.Errorf("admin %s %s = %d: %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// The export downloads as a file; importing it back into the configured instance it came from is refused.
func TestExportDownloadsAndImportRefusesAConfiguredInstance(t *testing.T) {
	f := newAPI(t)
	w := f.do("admin@x", "GET", "/api/admin/export", "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="crucible-export-`) ||
		!strings.Contains(w.Body.String(), `"lead@x"`) {
		t.Fatalf("export = %d %q %.100s", w.Code, w.Header().Get("Content-Disposition"), w.Body)
	}
	if i := f.do("admin@x", "POST", "/api/admin/import", w.Body.String()); i.Code != 409 || f.refreshes != 0 {
		t.Fatalf("import into a configured instance = %d %s (refreshes %d), want 409", i.Code, i.Body, f.refreshes)
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
	if w := f.do("admin@x", "POST", "/api/admin/teams/platform", `{"name":"Again","members":{"x@x":"leader"}}`); w.Code != 409 {
		t.Errorf("duplicate team = %d, want 409", w.Code)
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
	f.s.DB.(*pgxpool.Pool).Close()
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
		{"POST", "/api/org/trainings", `{"id":"t-1","team":"platform","repo":"https://g/x.git","brnach":"m"}`},
		{"PUT", "/api/org/trainings/forge-101/source", `{"repo":"https://g/x.git","reff":"v1"}`},
		{"PUT", "/api/admin/trainings/forge-101/owner", `{"teams":"other"}`},
		{"POST", "/api/admin/teams/fresh", `{"name":"F","members":{"f@x":"leader"},"leader":"g@x"}`},
		{"PUT", "/api/admin/teams/platform/webhooks/slack", `{"urll":"https://h/x"}`},
		{"PUT", "/api/org/teams/platform/roster", fmt.Sprintf(`{"version":%d,"name":"Platform","members":{"lead@x":"leader"},"memberz":{}}`, v)},
		{"PUT", "/api/org/teams/platform/programs/forge-101", `{"version":1,"budget_usd_month":0,"roles":{"participantz":[]}}`},
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

// A leader retags the people already on the team, any tag including leader, but never adds or removes anyone or
// renames the team; only an admin does that. A team always keeps a leader.
func TestLeaderRetagsButNeverAddsOrRemoves(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	for name, body := range map[string]string{
		"add":    f.roster(v, "Platform", "lead@x:leader", "senior@x:senior", "new@x:trainee", "more@x:trainee"),
		"remove": f.roster(v, "Platform", "lead@x:leader", "senior@x:senior"),
		"swap":   f.roster(v, "Platform", "lead@x:leader", "senior@x:senior", "dt@x:trainee"), // someone from another team
		"rename": f.roster(v, "Platform 2", "lead@x:leader", "senior@x:senior", "new@x:trainee"),
	} {
		if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", body); w.Code != 403 {
			t.Errorf("leader trying to %s = %d %s, want 403", name, w.Code, w.Body)
		}
	}
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.platform(v, "trainee", "senior", "trainee")); w.Code != 400 {
		t.Errorf("no leader left = %d %s, want 400", w.Code, w.Body)
	}
	if f.refreshes != 0 || f.teamVersion("platform") != v || audits(t, f.s, "team.roster") != 0 {
		t.Fatal("a refused change was applied")
	}
	for _, who := range []string{"senior@x", "new@x", "boss@x"} {
		if w := f.do(who, "PUT", "/api/org/teams/platform/roster", f.platform(v, "leader", "leader", "junior")); w.Code != 403 {
			t.Errorf("%s = %d, want 403", who, w.Code)
		}
	}
	// The leader promotes the senior to leader and the trainee to junior: two leaders now.
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.platform(v, "leader", "leader", "junior")); w.Code != 200 {
		t.Fatalf("leader retagging = %d %s", w.Code, w.Body)
	}
	if tm := f.snap.Teams["platform"]; !slices.Equal(tm.Leaders, []string{"lead@x", "senior@x"}) || tm.RoleOf("new@x") != "junior" || tm.Version != v+1 {
		t.Errorf("team = %+v", tm)
	}
	// An admin adds, removes and renames.
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/roster", f.roster(v+1, "Platform 2", "lead@x:leader", "more@x:trainee")); w.Code != 200 {
		t.Errorf("admin = %d %s", w.Code, w.Body)
	}
	if w := f.do("admin@x", "PUT", "/api/org/teams/ghost/roster", f.roster(1, "G", "a@x:leader")); w.Code != 404 {
		t.Errorf("admin, unknown team = %d, want 404", w.Code)
	}
	if w := f.do("lead@x", "PUT", "/api/org/teams/ghost/roster", f.roster(1, "G", "a@x:leader")); w.Code != 403 {
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
	if w := f.do("admin@x", "PUT", "/api/org/teams/platform/roster", f.platform(v, "senior", "leader", "trainee")); w.Code != 200 {
		t.Fatal(w.Body)
	}
	// lead@x is no longer leader: the stale form is refused outright.
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.platform(v, "leader", "senior", "trainee")); w.Code != 403 {
		t.Errorf("former leader = %d, want 403", w.Code)
	}
	// The new leader sends the old version (the page was open before the change): Conflict, not a write.
	w := f.do("senior@x", "PUT", "/api/org/teams/platform/roster", f.platform(v, "senior", "leader", "trainee"))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "reload") {
		t.Errorf("stale version = %d %s, want 409", w.Code, w.Body)
	}
	// The demoted leader's snapshot is stale: it still shows lead@x as leader at version v. Two requests, each
	// refused by a different guard: the route's version check (body at the real version) and the store's (body at v).
	f.mu.Lock()
	f.snap = old
	f.mu.Unlock()
	rows := count(t, f.s, `SELECT count(*) FROM audit_log`)
	for name, ver := range map[string]int64{"client at the stale version": v, "client at the real version": v + 1} {
		w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.platform(ver, "leader", "senior", "trainee"))
		if w.Code != 409 {
			t.Errorf("%s: demoted leader on a stale snapshot = %d %s, want 409", name, w.Code, w.Body)
		}
	}
	if count(t, f.s, `SELECT count(*) FROM audit_log`) != rows {
		t.Error("a refused write left an audit row")
	}
	if p, _ := f.s.Platform(context.Background()); !slices.Equal(p.Teams["platform"].Leaders, []string{"senior@x"}) {
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
		{"lead@x", "PUT", "/api/org/teams/platform/roster", f.platform(f.teamVersion("platform"), "leader", "senior", "trainee")},
		{"lead@x", "PUT", "/api/org/teams/platform/roster", f.roster(1, "Platform", "x@x:leader")},
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
	for _, who := range []string{"new@x", "boss@x"} { // a trainee of the owning team; another team's leader
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

func TestLeaderGuardComparesPeopleNotSpelling(t *testing.T) {
	f := newAPI(t)
	v := f.teamVersion("platform")
	// the same people in another case: not adding anyone
	if w := f.do("lead@x", "PUT", "/api/org/teams/platform/roster", f.roster(v, "Platform", "LEAD@x:leader", "Senior@x:senior", "new@x:trainee")); w.Code != 200 {
		t.Fatalf("same people in another case = %d %s", w.Code, w.Body)
	}
}

func TestPinWithoutACheckerIsRefused(t *testing.T) {
	f := newAPIWith(t, false)
	w := f.do("lead@x", "PUT", "/api/org/teams/platform/programs/forge-101/pin", `{"sha":"`+sha1+`"}`)
	if w.Code != 503 {
		t.Errorf("no checker = %d %s, want 503, never an unchecked store", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM programs WHERE pinned_ref IS NOT NULL`); n != 0 {
		t.Error("an unchecked sha was stored")
	}
}

// A repository's credential is kept sealed and never leaves: not in a listing, not in an audit row. A URL that
// carries userinfo of its own is shown masked.
func TestRepoCredentialsAreStoredButNeverShownOrAudited(t *testing.T) {
	f := newAPI(t)
	for _, b := range []string{
		`{"url":"https://git.example.com/org/r.git","auth":"token","secret":"t0psecret"}`,
		`{"url":"https://ghp_t0kenonly@git.example.com/o/t.git","auth":"none"}`,
		`{"url":"ssh://git@git.example.com/o/s.git","auth":"none"}`,
	} {
		if w := f.do("admin@x", "POST", "/api/admin/repos", b); w.Code != 200 {
			t.Fatalf("connect %s: %d %s", b, w.Code, w.Body)
		}
	}
	w := f.do("admin@x", "GET", "/api/admin/repos", "")
	body := w.Body.String()
	if strings.Contains(body, "t0psecret") || strings.Contains(body, "t0kenonly") {
		t.Errorf("a credential reached the listing: %s", body)
	}
	for _, want := range []string{`"has_secret":true`, "https://***@git.example.com/o/t.git", "ssh://git@git.example.com/o/s.git"} {
		if !strings.Contains(body, want) {
			t.Errorf("the listing lacks %q: %s", want, body)
		}
	}
	if n := count(t, f.s, `SELECT count(*) FROM audit_log WHERE detail::text LIKE '%t0psecret%' OR detail::text LIKE '%t0kenonly%'`); n != 0 {
		t.Errorf("%d audit rows hold a credential", n)
	}
	if n := count(t, f.s, `SELECT count(*) FROM audit_log WHERE action = 'repo.connect' AND detail::text LIKE '%git.example.com%'`); n != 3 {
		t.Errorf("audit rows naming the repository = %d, want 3", n)
	}
	// What is stored is ciphertext, and only the key opens it.
	var sealed string
	mustScan(t, f.s.DB, `SELECT secret FROM repositories WHERE auth = 'token'`, &sealed)
	if sealed == "" || strings.Contains(sealed, "t0psecret") {
		t.Errorf("the stored secret is not sealed: %q", sealed)
	}
}

// Leaders, seniors and juniors register trainings for their own team; trainees don't; admins pick the team.
// Leaders, seniors and juniors register trainings for their own team; trainees don't; admins pick the team. The id
// and title come from the repository's training.yaml, never from the request.
func TestWhoRegistersATraining(t *testing.T) {
	f := newAPI(t)
	mustExec(t, f.s.DB, `UPDATE team_members SET role = 'junior' WHERE email = 'senior@x'`)
	f.reload()
	create := func(who, id, team string) (int, string) {
		rid := repoWithTraining(t, f.s, id, strings.ToUpper(id), "courses/"+id)
		w := f.do(who, "POST", "/api/org/trainings", fmt.Sprintf(`{"repository":%d,"team":%q,"path":"courses/%s"}`, rid, team, id))
		return w.Code, w.Body.String()
	}
	if c, b := create("senior@x", "jr-101", ""); c != 200 || f.snap.Teams["platform"].Programs["jr-101"] == nil ||
		f.snap.Trainings["jr-101"].Path != "courses/jr-101" || !strings.Contains(b, `"title":"JR-101"`) {
		t.Fatalf("a junior registers for their team = %d %s", c, b)
	}
	for who, want := range map[string]int{"new@x": 403, "nobody@x": 403} {
		if c, _ := create(who, "x-1", ""); c != want {
			t.Errorf("%s = %d, want %d", who, c, want)
		}
	}
	if c, _ := create("lead@x", "x-2", "other"); c != 403 {
		t.Errorf("a leader registering for another team = %d, want 403", c)
	}
}

// A repository with no commits gets a training to start from, named after the folder it was registered in, so
// somebody can connect a new repository and go straight to editing.
func TestRegisteringAnEmptyRepositoryWritesAStarter(t *testing.T) {
	f := newAPI(t)
	rid, dir := emptyRepo(t, f.s)
	w := f.do("admin@x", "POST", "/api/org/trainings", fmt.Sprintf(`{"repository":%d,"team":"platform"}`, rid))
	if w.Code != 200 {
		t.Fatalf("register = %d %s", w.Code, w.Body)
	}
	for _, want := range []string{`"id":"kubernetes-course"`, `"started":true`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("the answer lacks %q: %s", want, w.Body)
		}
	}
	// The starter is really in the repository, and it is a training Crucible will load.
	out, err := exec.Command("git", "-C", dir, "show", "main:training.yaml").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "kubernetes-course") || !strings.Contains(string(out), "modules:") {
		t.Fatalf("training.yaml in the repository: %v\n%s", err, out)
	}
	if f.snap.Trainings["kubernetes-course"].Repo != dir {
		t.Errorf("the training does not read from the repository: %+v", f.snap.Trainings)
	}
}

func TestTrainingManagers(t *testing.T) {
	f := newAPI(t)
	put := func(who, roles string) *httptest.ResponseRecorder {
		v := f.snap.Teams["platform"].Programs["forge-101"].Version
		return f.do(who, "PUT", "/api/org/teams/platform/programs/forge-101", fmt.Sprintf(`{"version":%d,"budget_usd_month":0,"roles":%s}`, v, roles))
	}
	for _, who := range []string{"new@x", "boss@x", "dl@x"} {
		if w := put(who, `{"participants":["all"]}`); w.Code != 403 {
			t.Errorf("%s giving roles = %d, want 403", who, w.Code)
		}
	}
	// forge-101 names only its leader as admin, so a senior of the owning team is not one of its managers.
	if w := put("senior@x", `{"participants":["all"],"admins":["dl@x"]}`); w.Code != 403 {
		t.Fatalf("a senior who is not an admin of it = %d %s", w.Code, w.Body)
	}
	if w := put("lead@x", `{"participants":["all_trainees","Guest@corp"],"editors":["data_seniors"],"admins":["dl@x","platform_leaders"]}`); w.Code != 200 {
		t.Fatalf("the training's admin = %d %s", w.Code, w.Body)
	}
	pr := f.snap.Teams["platform"].Programs["forge-101"]
	if !slices.Equal(pr.Enrolled, []string{"dt@x", "guest@corp", "new@x"}) || !slices.Equal(pr.Editors, []string{"dl@x", "ds@x", "lead@x"}) {
		t.Fatalf("resolved: %v %v", pr.Enrolled, pr.Editors)
	}
	if w := put("lead@x", `{"editors":["ghost_seniors"],"admins":["platform_leaders"]}`); w.Code != 400 {
		t.Errorf("a group of no team = %d, want 400", w.Code)
	}
	// dl@x is now one of the training's admins: they manage it from another team.
	if w := put("dl@x", `{"participants":["data"],"admins":["dl@x"]}`); w.Code != 200 {
		t.Errorf("the training's admin = %d %s", w.Code, w.Body)
	}
	if w := f.do("dl@x", "PUT", "/api/org/trainings/forge-101/source", `{"ref":"v2"}`); w.Code != 200 || f.snap.Trainings["forge-101"].Ref != "v2" {
		t.Errorf("the training's admin changes the source = %d %s", w.Code, w.Body)
	}
	if w := f.do("boss@x", "DELETE", "/api/org/trainings/forge-101", ""); w.Code != 403 {
		t.Errorf("another team's leader deleting = %d, want 403", w.Code)
	}
	if w := f.do("lead@x", "PUT", "/api/admin/trainings/forge-101/owner", `{"team":"data"}`); w.Code != 403 {
		t.Errorf("only an admin moves a training to another team: %d", w.Code)
	}
}

// The people list feeds the role pickers: everyone known, with their team and tag, and every group.
func TestPeopleList(t *testing.T) {
	f := newAPI(t)
	mustExec(t, f.s.DB, `INSERT INTO users (sub, email, name) VALUES ('s1', 'Loner@x', 'Lone Smith')`)
	w := f.do("senior@x", "GET", "/api/org/people", "")
	var got struct {
		People []Person `json:"people"`
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("people = %d %s", w.Code, w.Body)
	}
	if !slices.Contains(got.People, Person{Email: "new@x", Team: "platform", Tag: "trainee", Smith: smith.RefFor(0)}) ||
		!slices.Contains(got.People, Person{Email: "loner@x", Name: "Lone Smith", Smith: smith.RefFor(0)}) || !slices.Contains(got.Groups, "data_seniors") {
		t.Errorf("people %+v groups %v", got.People, got.Groups)
	}
	if w := f.do("new@x", "GET", "/api/org/people", ""); w.Code != 403 {
		t.Errorf("a trainee = %d, want 403", w.Code)
	}
}

func TestTeamNeverMarshalsItsWebhooks(t *testing.T) {
	b, _ := json.Marshal(&config.Team{Notifications: config.TeamNotifications{SlackWebhook: "https://h/s3cr3t"}})
	if strings.Contains(string(b), "s3cr3t") {
		t.Errorf("marshalled team holds the webhook: %s", b)
	}
}

func TestAdminWithAnEncodedAddressCanBeRemoved(t *testing.T) {
	f := newAPI(t)
	if w := f.do("admin@x", "POST", "/api/admin/admins", `{"email":"a+b@x"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := f.do("admin@x", "DELETE", "/api/admin/admins/a%2Bb%40x", ""); w.Code != 200 {
		t.Fatalf("encoded delete = %d %s", w.Code, w.Body)
	}
	if n := count(t, f.s, `SELECT count(*) FROM admins WHERE email = 'a+b@x'`); n != 0 {
		t.Error("the admin is still there: an admin that cannot be removed is a permanent privilege")
	}
}

func TestRedactRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@host.com:o/r.git":            "git@host.com:o/r.git",
		"deploy@host.com:o/r.git":         "***@host.com:o/r.git",
		"ghp_t0ken@host.com:o/r.git":      "***@host.com:o/r.git",
		"ssh://git@host.com/o/r.git":      "ssh://git@host.com/o/r.git",
		"https://u:p@host.com/o/r.git":    "https://***@host.com/o/r.git",
		"https://host.com/o/r.git":        "https://host.com/o/r.git",
		"https://host.com/o/a@b.git":      "https://host.com/o/a@b.git",
		"https://gitlab@host.com/o/r.git": "https://***@host.com/o/r.git",
	} {
		if got := redactRepo(in); got != want {
			t.Errorf("redactRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

// repoWithTraining makes a local repository holding one training at root ("" is the repository root) and connects it,
// returning the repositories row id. Registration reads training.yaml from it, so it has to be a real repository.
func repoWithTraining(t *testing.T, s *Store, id, title, root string) int64 {
	t.Helper()
	gitsync.AllowFileTransport = true
	t.Cleanup(func() { gitsync.AllowFileTransport = false })
	dir := t.TempDir()
	for name, body := range map[string]string{
		"training.yaml":          "id: " + id + "\ntitle: " + title + "\nmodules: [m1]\n",
		"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: r.md\n",
		"modules/m1/r.md":        "# Hello\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(path.Join(root, name)))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "c"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var rid int64
	if err := s.DB.QueryRow(context.Background(), `INSERT INTO repositories (url) VALUES ($1) RETURNING id`, dir).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	return rid
}

// emptyRepo connects a repository with no commits, which registration fills with a training to start from.
func emptyRepo(t *testing.T, s *Store) (int64, string) {
	t.Helper()
	gitsync.AllowFileTransport = true
	t.Cleanup(func() { gitsync.AllowFileTransport = false })
	dir := filepath.Join(t.TempDir(), "kubernetes-course.git")
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	var rid int64
	if err := s.DB.QueryRow(context.Background(), `INSERT INTO repositories (url) VALUES ($1) RETURNING id`, dir).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	return rid, dir
}
