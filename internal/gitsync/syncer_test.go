package gitsync

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
