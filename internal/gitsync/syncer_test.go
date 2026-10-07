package gitsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-qm", "change")
}

func newRepo(t *testing.T, files map[string]string) string {
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	commit(t, repo, files)
	return repo
}

var training = map[string]string{
	"training.yaml":          "id: t1\ntitle: T1\nmodules: [m1]\n",
	"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: r.md\n",
	"modules/m1/r.md":        "# Hello\n",
}

func setup(t *testing.T) (*Syncer, string, string) {
	trainingRepo := newRepo(t, training)
	platformRepo := newRepo(t, map[string]string{
		"platform.yaml":            "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n",
		"trainings.yaml":           "trainings:\n  t1: {repo: " + trainingRepo + "}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	})
	s := New(t.TempDir(), platformRepo, "main", slog.Default())
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, platformRepo, trainingRepo
}

func TestSyncLoadsHead(t *testing.T) {
	s, _, _ := setup(t)
	tr, sha := s.Current().ProgramTraining("a", "t1")
	if tr == nil || tr.Title != "T1" || len(sha) != 40 {
		t.Fatalf("training not loaded: %v %q %+v", tr, sha, s.Current().Problems)
	}
}

func TestBadHeadKeepsLastGood(t *testing.T) {
	s, _, trainingRepo := setup(t)
	_, goodSHA := s.Current().ProgramTraining("a", "t1")
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"}) // title removed
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.Current()
	tr, sha := st.ProgramTraining("a", "t1")
	if tr == nil || sha != goodSHA {
		t.Fatalf("program should stay on last good sha %s, got %q", goodSHA, sha)
	}
	if len(st.Problems["t1@"+st.Heads["t1"]]) == 0 {
		t.Fatalf("problems for bad head not recorded: %+v", st.Problems)
	}
}

func TestBadPlatformKeepsState(t *testing.T) {
	s, platformRepo, _ := setup(t)
	before := s.Current().PlatformSHA
	commit(t, platformRepo, map[string]string{"platform.yaml": "default_theme: forge\nbogus: 1\n"})
	if err := s.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected error for bad platform config")
	}
	st := s.Current()
	if st.PlatformSHA != before || st.Platform == nil || st.PlatformErr == "" {
		t.Fatalf("state not kept: sha %s err %q", st.PlatformSHA, st.PlatformErr)
	}
}

func TestBadPinKeepsLastGood(t *testing.T) {
	s, platformRepo, _ := setup(t)
	_, good := s.Current().ProgramTraining("a", "t1")
	commit(t, platformRepo, map[string]string{"teams/a/programs/t1.yaml": "enrolled: [u@x]\npinned_ref: nope\n"})
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, sha := s.Current().ProgramTraining("a", "t1"); sha != good {
		t.Fatalf("want %s got %q", good, sha)
	}
}

func TestExportFailureRetried(t *testing.T) {
	s, _, trainingRepo := setup(t)
	dir := filepath.Join(s.DataDir, "content", "t1")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\ntitle: T2\nmodules: [m1]\n"})
	_ = s.SyncOnce(context.Background())
	if tr, _ := s.Current().ProgramTraining("a", "t1"); tr.Title != "T1" {
		t.Skip("export did not fail (running as root?)")
	}
	os.Chmod(dir, 0o755)
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tr, _ := s.Current().ProgramTraining("a", "t1"); tr.Title != "T2" {
		t.Fatalf("export failure was cached: %+v", s.Current().Problems)
	}
}

func TestOnProblemFiresOncePerNewProblem(t *testing.T) {
	s, _, trainingRepo := setup(t) // first sync happened before the hook was set: nothing to announce
	var keys []string
	s.OnProblem = func(key string, _ []content.Problem) { keys = append(keys, key) }
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"})
	for range 2 {
		if err := s.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 1 || keys[0] != "t1@"+s.Current().Heads["t1"] {
		t.Fatalf("OnProblem keys = %v", keys)
	}
}

// After a restart only the versions programs run are in memory; an older one still loads from the mirror on demand.
func TestVersionLoadsAnOldSHAAfterARestart(t *testing.T) {
	s, platformRepo, trainingRepo := setup(t)
	_, old := s.Current().ProgramTraining("a", "t1")
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\ntitle: T1 v2\nmodules: [m1]\n"})
	fresh := New(t.TempDir(), platformRepo, "main", slog.Default()) // the restarted API
	if err := fresh.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fresh.Current().Training("t1", old) != nil {
		t.Fatal("the old version should not be loaded yet")
	}
	if tr := fresh.Version(context.Background(), "t1", old); tr == nil || tr.Title != "T1" {
		t.Fatalf("old version: %+v %v", tr, fresh.Current().Problems)
	}
	if cur, _ := fresh.Current().ProgramTraining("a", "t1"); cur == nil || cur.Title != "T1 v2" {
		t.Fatalf("program still on head: %+v", cur)
	}
	if err := fresh.SyncOnce(context.Background()); err != nil || fresh.Current().Training("t1", old) == nil {
		t.Fatalf("a loaded version survives the next sync: %v", err)
	}
	if fresh.Version(context.Background(), "t1", "0000000000000000000000000000000000000000") != nil ||
		fresh.Version(context.Background(), "nope", old) != nil {
		t.Fatal("unknown sha / training must be nil")
	}
	out := filepath.Join(t.TempDir(), "pwn")
	for _, bad := range []string{"HEAD", "main", "HEAD~0", "--output=" + out, "../../etc", strings.ToUpper(old)} {
		if fresh.Version(context.Background(), "t1", bad) != nil {
			t.Fatalf("%q is not a commit sha and must not load", bad)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a sha must never reach git as an option")
	}
}

func TestChangesAndCheckPin(t *testing.T) {
	ctx := context.Background()
	s, _, repo := setup(t)
	first := s.Current().Heads["t1"]
	run(t, repo, "checkout", "-qb", "side")
	commit(t, repo, map[string]string{"modules/m1/r.md": "# side\n"})
	side := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	run(t, repo, "checkout", "-q", "main")
	commit(t, repo, map[string]string{"modules/m1/r.md": "# Two\n"})
	run(t, repo, "commit", "-q", "--amend", "-m", "second change")
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	second := s.Current().Heads["t1"]
	ch, err := s.Changes(ctx, "t1", first, second)
	if err != nil || len(ch.Commits) != 1 || !strings.HasSuffix(ch.Commits[0], "second change") || !strings.Contains(ch.Stat, "r.md") {
		t.Fatalf("changes: %+v %v", ch, err)
	}
	if _, err := s.Changes(ctx, "t1", "HEAD~1", second); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("only full commit ids: %v", err)
	}
	if _, err := s.Changes(ctx, "nope", first, second); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training: %v", err)
	}
	if err := s.CheckPin(ctx, "t1", first); err != nil {
		t.Fatalf("an older commit on the branch can be pinned: %v", err)
	}
	if err := s.CheckPin(ctx, "t1", side); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a commit off the branch must be refused: %v", err)
	}
}

func TestCheckPinRefusesAnAncestorThatFailsToLoad(t *testing.T) {
	ctx := context.Background()
	s, _, repo := setup(t)
	commit(t, repo, map[string]string{"training.yaml": "id: t1\ntitle: [broken\n"})
	bad := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	commit(t, repo, map[string]string{"training.yaml": "id: t1\ntitle: T1 fixed\nmodules: [m1]\n"})
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPin(ctx, "t1", bad); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("an ancestor whose content fails validation must be refused: %v", err)
	}
}

func TestChangesFlagsTruncationAndReportsGitFailures(t *testing.T) {
	ctx := context.Background()
	s, _, repo := setup(t)
	first := s.Current().Heads["t1"]
	for i := 0; i < 52; i++ {
		commit(t, repo, map[string]string{"modules/m1/r.md": fmt.Sprintf("# v%d\n", i)})
	}
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	head := s.Current().Heads["t1"]
	ch, err := s.Changes(ctx, "t1", first, head)
	if err != nil || len(ch.Commits) != 50 || !ch.More {
		t.Fatalf("50 commits and more=true expected: %d %v %v", len(ch.Commits), ch != nil && ch.More, err)
	}
	// a git that cannot run is an unavailable service, not a bad request
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Changes(cctx, "t1", first, head); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("a cancelled git must be Unavailable: %v", err)
	}
	if err := s.CheckPin(cctx, "t1", first); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("a cancelled git must be Unavailable: %v", err)
	}
}

func TestConfigFuncReplacesThePlatformRepo(t *testing.T) {
	trainingRepo := newRepo(t, training)
	plat := &config.Platform{Admins: []string{"a@x"}, Trainings: map[string]config.TrainingRef{"t1": {Repo: trainingRepo, Branch: "main"}},
		Teams: map[string]*config.Team{"a": {Programs: map[string]*config.Program{"t1": {}}}}}
	s := New(t.TempDir(), "", "main", slog.Default())
	s.Config = func(context.Context) (*config.Platform, error) { return plat, nil }
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.Current()
	if st.Platform != plat || st.PlatformSHA != "" {
		t.Fatalf("platform should come from Config with no sha: %v %q", st.Platform, st.PlatformSHA)
	}
	if tr, _ := st.ProgramTraining("a", "t1"); tr == nil || tr.Title != "T1" {
		t.Fatalf("content should still sync from git: %+v", st.Problems)
	}
	s.Config = func(context.Context) (*config.Platform, error) { return nil, errors.New("db down") }
	if err := s.SyncOnce(context.Background()); err == nil || s.Current().Platform != plat || s.Current().PlatformErr == "" {
		t.Fatalf("a failing store keeps the last good platform: %v", err)
	}
}
