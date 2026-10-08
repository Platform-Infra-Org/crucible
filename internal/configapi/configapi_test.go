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
	"crucible/internal/config"
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

// copyDir copies src into a fresh directory; rewrite replaces files in the copy.
func copyDir(t *testing.T, src string, rewrite map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	for rel, body := range rewrite {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// bareFrom returns a bare git repo holding a copy of src.
func bareFrom(t *testing.T, src string) string {
	t.Helper()
	work := copyDir(t, src, nil)
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
	platform, content              string
	admin, leader, senior, trainee *auth.User
}

// setup reads the configuration from a copy of examples/platform (the seed), with every training served by one
// bare copy of forge-101. Tests change the configuration by rewriting files in f.platform and re-syncing.
func setup(t *testing.T) *fx {
	t.Helper()
	content := bareFrom(t, "../../examples/forge-101")
	dir := copyDir(t, "../../examples/platform", map[string]string{
		"trainings.yaml": "trainings:\n  forge-101: {repo: " + content + "}\n  forge-201: {repo: " + content + "}\n  forge-103: {repo: " + content + "}\n"})
	syncer := gitsync.New(t.TempDir(), func(context.Context) (*config.Platform, error) { return config.Load(dir) }, slog.Default())
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: dbtest.New(t), State: syncer.Current, Changes: syncer.Changes}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: s, sync: syncer, platform: dir, content: content, admin: u("admin@crucible.local"), leader: u("leader@crucible.local"),
		senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func TestReadPermissions(t *testing.T) {
	f := setup(t)
	if teams, _ := f.s.Teams(f.admin); len(teams) != 1 || teams[0].Role != "admin" {
		t.Fatalf("admins see every team: %+v", teams)
	}
	if teams, _ := f.s.Teams(f.trainee); len(teams) != 1 || teams[0].Role != "trainee" {
		t.Fatalf("members see their team with their role: %+v", teams)
	}
	if teams, err := f.s.Teams(&auth.User{Email: "stranger@x"}); err != nil || len(teams) != 0 {
		t.Fatalf("outsiders see no team: %+v %v", teams, err)
	}
	if _, err := f.s.Platform(f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("platform view is admin-only: %v", err)
	}
}

// A pinned program stays on its commit while the branch moves; whoever manages it sees what the bump would bring.
func TestProgramChangesShowWhatABumpBrings(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	head := f.sync.Current().Heads["forge-101"]
	prog := filepath.Join(f.platform, "teams", "forge", "programs", "forge-101.yaml")
	b, err := os.ReadFile(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prog, append(b, []byte("pinned_ref: "+head+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	sh(t, "", "clone", "-q", f.content, work)
	_ = os.WriteFile(filepath.Join(work, "modules/01-welcome/reading/how-we-work.md"), []byte("# How We Work\n\nNew words.\n"), 0o644)
	sh(t, work, "commit", "-qam", "new words")
	sh(t, work, "push", "-q", "origin", "HEAD:main")
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.sync.Current().ProgramSHAs["forge/forge-101"]; got != head {
		t.Fatalf("the pinned program moved to %s", got)
	}
	if _, err := f.s.ProgramChanges(ctx, f.trainee, "forge", "forge-101"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees can't see the diff: %v", err)
	}
	ch, err := f.s.ProgramChanges(ctx, f.leader, "forge", "forge-101")
	if err != nil || len(ch.Commits) != 1 || !strings.Contains(ch.Commits[0], "new words") {
		t.Fatalf("diff summary: %+v %v", ch, err)
	}
	if _, err := f.s.ProgramChanges(ctx, f.leader, "forge", "forge-201"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("a training the team does not run: %v", err)
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
