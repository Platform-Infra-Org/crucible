package configapi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
)

// TestMain allows the file transport: these tests use local bare repos as git remotes.
func TestMain(m *testing.M) {
	gitsync.AllowFileTransport = true
	os.Exit(m.Run())
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareFrom copies src into a fresh repo and returns a bare clone of it; rewrite edits files before the commit.
func bareFrom(t *testing.T, src string, rewrite map[string]string) string {
	t.Helper()
	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	for rel, body := range rewrite {
		_ = os.WriteFile(filepath.Join(work, rel), []byte(body), 0o644)
	}
	sh(t, work, "init", "-q", "-b", "main")
	sh(t, work, "add", "-A")
	sh(t, work, "commit", "-qm", "seed")
	out := filepath.Join(t.TempDir(), "r.git")
	sh(t, "", "clone", "-q", "--bare", work, out)
	return out
}

type fx struct {
	s                              *Service
	sync                           *gitsync.Syncer
	remote, content                string
	admin, leader, senior, trainee *auth.User
}

func setup(t *testing.T) *fx {
	t.Helper()
	content := bareFrom(t, "../../examples/forge-101", nil)
	remote := bareFrom(t, "../../examples/platform", map[string]string{
		// forge-103 is registered because examples/platform now enrols the forge team in it; a program whose
		// training is missing from the registry fails the whole platform load.
		"trainings.yaml": "trainings:\n  forge-101: {repo: " + content + "}\n  forge-201: {repo: " + content + "}\n  forge-103: {repo: " + content + "}\n"})
	syncer := gitsync.New(t.TempDir(), remote, "main", slog.Default())
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t)
	s := &Service{DB: pool, State: syncer.Current, Resync: syncer.SyncOnce, Changes: syncer.Changes, CheckPin: syncer.CheckPin,
		Writer: &gitsync.Writer{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "w"), Name: "Crucible", Email: "bot@x"}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: s, sync: syncer, remote: remote, content: content, admin: u("admin@crucible.local"), leader: u("leader@crucible.local"),
		senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) sha() string { return f.sync.Current().PlatformSHA }

func emptyProgram(base string) ProgramBody {
	return ProgramBody{BaseSHA: base, Roles: RolesView{Manager: []string{}, Scorers: []string{}, Approvers: []string{}}}
}

func TestLeaderEnrollsTheTeamAndATrainee(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	view, err := f.s.Team(f.leader, "forge")
	if err != nil || !view.CanEditTeam || len(view.AvailableTrainings) != 1 || view.AvailableTrainings[0].ID != "forge-201" {
		t.Fatalf("team view %+v %v", view, err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", emptyProgram(f.sha())); err != nil {
		t.Fatal(err)
	}
	body := emptyProgram(f.sha())
	body.Enrolled = []string{"TRAINEE@crucible.local"}
	body.LabDefaults = LabDefaultsView{TTL: "90m"}
	sha, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", body)
	if err != nil {
		t.Fatal(err)
	}
	st := f.sync.Current()
	prog := st.Platform.Teams["forge"].Programs["forge-201"]
	if st.PlatformSHA != sha || prog == nil || prog.Enrolled[0] != "trainee@crucible.local" || prog.LabDefaults.TTL.D().Minutes() != 90 {
		t.Fatalf("resynced program %+v at %s (want %s)", prog, st.PlatformSHA, sha)
	}
	log := sh(t, f.remote, "log", "--format=%B", "-2", "main")
	if !strings.Contains(log, "crucible: enroll forge in forge-201 by leader@crucible.local") ||
		!strings.Contains(log, "crucible: update program forge/forge-201 by leader@crucible.local") || !strings.Contains(log, "Crucible-Actor: leader@crucible.local") {
		t.Fatalf("commits:\n%s", log)
	}
	var action, commit string
	_ = f.s.DB.QueryRow(ctx, `SELECT action, commit_sha FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action, &commit)
	if action != "program.update" || commit != sha {
		t.Fatalf("audit %s %s", action, commit)
	}
}

func TestPermissions(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.SetProgram(ctx, f.trainee, "forge", "forge-101", emptyProgram(f.sha())); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee edits a program: %v", err)
	}
	if _, err := f.s.SetRoster(ctx, f.senior, "forge", RosterBody{BaseSHA: f.sha()}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("senior edits the roster: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.leader, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 1}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader edits the team budget: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.admin, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 300, HardCapUSD: 350}); err != nil {
		t.Fatal(err)
	}
	if b := f.sync.Current().Platform.Teams["forge"].Budget; b.MonthlyUSD != 300 || b.HardCapUSD != 350 {
		t.Fatalf("budget %+v", b)
	}
	if _, err := f.s.Team(&auth.User{Email: "stranger@x"}, "forge"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("outsiders: %v", err)
	}
	if teams, _ := f.s.Teams(f.admin); len(teams) != 1 || teams[0].Role != "admin" {
		t.Fatalf("admins see every team: %+v", teams)
	}
	if _, err := f.s.Platform(f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("platform view is admin-only: %v", err)
	}
}

func TestStaleEditIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	old := f.sha()
	body := emptyProgram(old)
	body.Enrolled = []string{"trainee@crucible.local"}
	body.Schedule = "business-hours"
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "Someone changed this in git, reload") {
		t.Fatalf("second save from the same stale page: %v", err)
	}
	roster := RosterBody{BaseSHA: old, Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "senior@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); err != nil {
		t.Fatalf("a different file from the same page is fine: %v", err)
	}
}

func TestInvalidEditIsExplained(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	roster := RosterBody{BaseSHA: f.sha(), Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "trainee@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "mentor pairing") {
		t.Fatalf("invalid mentor: %v", err)
	}
	body := emptyProgram(f.sha())
	body.LabDefaults = LabDefaultsView{TTL: "forever"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("bad duration: %v", err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "../../etc", emptyProgram(f.sha())); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training id: %v", err)
	}
}

// push commits files to the remote from another clone, as someone editing git directly.
func (f *fx) push(t *testing.T, files map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	sh(t, "", "clone", "-q", f.remote, dir)
	for rel, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
	}
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-qm", "edit in git")
	sh(t, dir, "push", "-q", "origin", "HEAD:main")
}

func TestManagerUpdatesButCannotEnrol(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	body := emptyProgram(f.sha())
	body.Enrolled = []string{"trainee@crucible.local"}
	body.Roles.Manager = []string{"senior@crucible.local"}
	body.Roles.Approvers = []string{"approver@elsewhere.local"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); err != nil {
		t.Fatal(err)
	}
	body.BaseSHA, body.Schedule = f.sha(), "business-hours"
	if _, err := f.s.SetProgram(ctx, f.senior, "forge", "forge-101", body); err != nil {
		t.Fatalf("a program manager updates their program: %v", err)
	}
	if _, err := f.s.SetProgram(ctx, f.senior, "forge", "forge-201", emptyProgram(f.sha())); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("a program manager enrols the team in another training: %v", err)
	}
	approver := &auth.User{Email: "approver@elsewhere.local"}
	v, err := f.s.Team(approver, "forge")
	if err != nil || v.CanEditTeam {
		t.Fatalf("approver reads the team: %+v %v", v, err)
	}
	if v.Budget == nil {
		t.Fatal("approvers see team spend")
	}
	if teams, _ := f.s.Teams(approver); len(teams) != 1 || teams[0].Role != "approver" {
		t.Fatalf("approver's teams: %+v", teams)
	}
	if v, _ := f.s.Team(f.trainee, "forge"); v.Budget != nil {
		t.Fatal("trainees do not see team spend")
	}
}

func TestAdminEditsRosterAndBaseIsRequired(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	roster := RosterBody{BaseSHA: f.sha(), Seniors: []string{"senior@crucible.local"}, Members: []string{"new@crucible.local"},
		Trainees: []string{"trainee@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.admin, "forge", roster); err != nil {
		t.Fatalf("an admin who is not on the team edits the roster: %v", err)
	}
	roster.BaseSHA = ""
	if _, err := f.s.SetRoster(ctx, f.admin, "forge", roster); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "base_sha is required") {
		t.Fatalf("missing base_sha: %v", err)
	}
	roster.BaseSHA, roster.Members = f.sha(), []string{"not an email"}
	if _, err := f.s.SetRoster(ctx, f.admin, "forge", roster); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("bad email: %v", err)
	}
}

func TestLeaderRemovedInGitCannotWrite(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	old := f.sha() // the page (and the syncer snapshot) still show leader@ as the leader
	f.push(t, map[string]string{"teams/forge/team.yaml": "name: The Forge\nleader: boss@crucible.local\nseniors: [senior@crucible.local]\n" +
		"members: [leader@crucible.local]\ntrainees: [trainee@crucible.local]\nmentors: {trainee@crucible.local: senior@crucible.local}\n"})
	body := emptyProgram(old)
	body.Enrolled = []string{"trainee@crucible.local"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("permission is re-checked at the tip: %v", err)
	}
}

func TestNoOpSaveMakesNoCommit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	body := emptyProgram(f.sha())
	body.Enrolled = []string{"trainee@crucible.local"}
	first, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body)
	if err != nil {
		t.Fatal(err)
	}
	body.BaseSHA = f.sha()
	again, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body)
	if err != nil || again != first || sh(t, f.remote, "rev-parse", "main") != first {
		t.Fatalf("no-op save: %s vs %s, %v", again, first, err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit entries: %d", n)
	}
}

func TestBrokenRepoIsNamed(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	old := f.sha()
	f.push(t, map[string]string{"teams/forge/programs/forge-101.yaml": "enrolled: [trainee@crucible.local]\nschedule: nope\n"})
	roster := RosterBody{BaseSHA: old, Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"}}
	_, err := f.s.SetRoster(ctx, f.leader, "forge", roster)
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "currently has errors in teams/forge/programs/forge-101.yaml") {
		t.Fatalf("pre-existing error: %v", err)
	}
}

func TestWriterRefusesSymlinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "teams"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(dir, "linkdir"))
	_ = os.Symlink(filepath.Join(outside, "x.yaml"), filepath.Join(dir, "teams", "budget.yaml"))
	for _, rel := range []string{"linkdir/budget.yaml", "teams/budget.yaml"} {
		if err := edit(rel, map[string]any{"a": 1})(dir); err == nil {
			t.Fatalf("%s: symlink followed", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "x.yaml")); err == nil {
		t.Fatal("wrote outside the clone")
	}
	if err := edit("teams/new.yaml", map[string]any{"a": 1})(dir); err != nil {
		t.Fatalf("plain file: %v", err)
	}
}

func TestSetProgramKeepsInlineSchedule(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	inline := "training: forge-101\nenrolled: [trainee@crucible.local]\nreview_self_reported: true\nschedule:\n  timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	f.push(t, map[string]string{"teams/forge/programs/forge-101.yaml": inline})
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	tv, err := f.s.Team(f.leader, "forge")
	if err != nil || tv.Programs[0].InlineSchedule == "" || tv.Programs[0].Schedule != "" {
		t.Fatalf("view shows the inline schedule: %+v %v", tv.Programs, err)
	}
	b := emptyProgram(f.sha())
	b.Enrolled = []string{"trainee@crucible.local"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	got := sh(t, "", "--git-dir", f.remote, "show", "main:teams/forge/programs/forge-101.yaml")
	if !strings.Contains(got, "timezone: Europe/Bucharest") || !strings.Contains(got, "review_self_reported: true") {
		t.Fatalf("saving must keep the inline schedule and the review flag:\n%s", got)
	}
	var detail string
	_ = f.s.DB.QueryRow(ctx, `SELECT detail::text FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&detail)
	if !strings.Contains(detail, `"schedule": "inline"`) {
		t.Fatalf("audit must record the kept inline schedule: %s", detail)
	}
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if tv, _ = f.s.Team(f.leader, "forge"); !tv.Programs[0].ReviewSelfReported {
		t.Fatal("the view shows the review flag")
	}
	off := false
	b.BaseSHA, b.ReviewSelfReported = f.sha(), &off
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:teams/forge/programs/forge-101.yaml"); strings.Contains(got, "review_self_reported") {
		t.Fatalf("turning the flag off drops it:\n%s", got)
	}
}

func TestSetProgramRefusesToReplaceInlineSchedule(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	inline := "training: forge-101\nenrolled: [trainee@crucible.local]\nschedule:\n  timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	f.push(t, map[string]string{"teams/forge/programs/forge-101.yaml": inline})
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	b := emptyProgram(f.sha())
	b.Enrolled = []string{"trainee@crucible.local"}
	b.Schedule = "business-hours"
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", b); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "edit it in git") {
		t.Fatalf("a named schedule over inline windows must be refused: %v", err)
	}
	got := sh(t, "", "--git-dir", f.remote, "show", "main:teams/forge/programs/forge-101.yaml")
	if !strings.Contains(got, "timezone: Europe/Bucharest") {
		t.Fatalf("inline windows must survive the refused save:\n%s", got)
	}
}

func TestPinBump(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	head := f.sync.Current().Heads["forge-101"]
	if _, err := f.s.SetPin(ctx, f.trainee, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: head}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees can't pin: %v", err)
	}
	if _, err := f.s.ProgramChanges(ctx, f.trainee, "forge", "forge-101"); err == nil {
		t.Fatal("trainees can't see the diff")
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: head}); err != nil {
		t.Fatal(err)
	}
	// Someone pushes new content, and an unmerged edit branch; the program stays on its pin.
	work := t.TempDir()
	sh(t, "", "clone", "-q", f.content, work)
	sh(t, work, "checkout", "-qb", "crucible/edit-x")
	_ = os.WriteFile(filepath.Join(work, "modules/01-welcome/reading/how-we-work.md"), []byte("# Unmerged\n"), 0o644)
	sh(t, work, "commit", "-qam", "unmerged")
	unmerged := sh(t, work, "rev-parse", "HEAD")
	sh(t, work, "push", "-q", "origin", "HEAD:crucible/edit-x")
	sh(t, work, "checkout", "-q", "main")
	_ = os.WriteFile(filepath.Join(work, "modules/01-welcome/reading/how-we-work.md"), []byte("# How We Work\n\nNew words.\n"), 0o644)
	sh(t, work, "commit", "-qam", "new words")
	sh(t, work, "push", "-q", "origin", "HEAD:main")
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	tv, _ := f.s.Team(f.leader, "forge")
	pv := tv.Programs[0]
	if pv.RunningSHA != head || pv.HeadSHA == head || pv.PinnedRef != head {
		t.Fatalf("pinned program view: %+v", pv)
	}
	ch, err := f.s.ProgramChanges(ctx, f.leader, "forge", "forge-101")
	if err != nil || len(ch.Commits) != 1 || !strings.Contains(ch.Commits[0], "new words") {
		t.Fatalf("diff summary: %+v %v", ch, err)
	}
	for _, bad := range []string{strings.Repeat("a", 40), unmerged, "main", "--output=x"} {
		if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: bad}); !errors.Is(err, apperr.Invalid) {
			t.Fatalf("%q must not be pinnable: %v", bad, err)
		}
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: pv.HeadSHA}); err != nil {
		t.Fatal(err)
	}
	if got := f.sync.Current().ProgramSHAs["forge/forge-101"]; got != pv.HeadSHA {
		t.Fatalf("bumped: running %s", got)
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha()}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='program.pin'`).Scan(&n)
	if n != 3 || f.sync.Current().Platform.Teams["forge"].Programs["forge-101"].PinnedRef != "" {
		t.Fatalf("unpin / audit: %d", n)
	}
}

func TestForgeStatusShowsAttention(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	var uid int64
	if err := f.s.DB.QueryRow(ctx, `INSERT INTO users (sub, email) VALUES ('s9', 'trainee@crucible.local') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, error, created_at,
		last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s) VALUES
		('aaaaaaaaaaaa', $1, 'forge', 'forge-101', '02-first-lab', 'x', 'local', 'failed', 'compose up failed', now(), now(), 3600, 1800, 300, 0)`, uid); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at, destroyed_at, stuck_alerted_at,
		last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s) VALUES
		($2, $1, 'forge', 'forge-101', '02-first-lab', 'x', 'aws', 'destroying', now(), now() - interval '30 minutes', $3, now(), 3600, 1800, 300, 0)`
	user := func(n string) (id int64) {
		if err := f.s.DB.QueryRow(ctx, `INSERT INTO users (sub, email) VALUES ($1, $1 || '@crucible.local') RETURNING id`, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := f.s.DB.Exec(ctx, ins, user("u2"), "bbbbbbbbbbbb", nil); err != nil { // a normal aws destroy: not stuck
		t.Fatal(err)
	}
	// failed long ago, created recently: outside the 24h window by failure time
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at, destroyed_at,
		last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s) VALUES
		('dddddddddddd', $1, 'forge', 'forge-101', '02-first-lab', 'x', 'local', 'failed', now(), now() - interval '2 days', now(), 3600, 1800, 300, 0)`, user("u3")); err != nil {
		t.Fatal(err)
	}
	v, err := f.s.Status(ctx, f.admin)
	if err != nil || len(v.Attention) != 1 || v.Attention[0].Trainee != "trainee@crucible.local" || v.PendingEdits != 0 || len(v.Programs) == 0 {
		t.Fatalf("status %+v %v", v, err)
	}
	if _, err := f.s.DB.Exec(ctx, ins, user("u4"), "cccccccccccc", time.Now()); err != nil { // alerted: listed
		t.Fatal(err)
	}
	if v, err = f.s.Status(ctx, f.admin); err != nil || len(v.Attention) != 2 {
		t.Fatalf("alerted destroy must be listed: %+v %v", v.Attention, err)
	}
	if _, err := f.s.Status(ctx, f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("admins only: %v", err)
	}
}
