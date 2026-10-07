package org

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/rbac"
)

func newTrainingRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for name, body := range map[string]string{
		"training.yaml":          "id: t1\ntitle: T1\nmodules: [m1]\n",
		"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: r.md\n",
		"modules/m1/r.md":        "# Hello\n",
	} {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "c"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestStoreDrivesTheSnapshotWithoutGit(t *testing.T) {
	gitsync.AllowFileTransport = true
	t.Cleanup(func() { gitsync.AllowFileTransport = false })
	ctx := context.Background()
	s := &Store{DB: dbtest.New(t)}
	repo := newTrainingRepo(t)
	sy := gitsync.New(t.TempDir(), "", "main", slog.Default())
	sy.Config = s.Platform

	// bootstrap admin in database mode: seeded row -> snapshot -> rbac
	if ok, err := s.SeedAdmin(ctx, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s.DB, `INSERT INTO trainings (id, repo, branch) VALUES ('t1', $1, 'main')`, repo) // AddTraining refuses local paths
	must(s.CreateTeam(ctx, "boss@example.com", "a", TeamBody{Name: "A", Leader: "l@x.com", Trainees: []string{"u@x.com"}}))
	must(s.Enroll(ctx, "boss@example.com", "a", "t1", ProgramBody{Enrolled: []string{"u@x.com"}}))
	must(sy.SyncOnce(ctx))
	st := sy.Current()
	if !(rbac.Checker{P: st.Platform}).IsAdmin("boss@example.com") {
		t.Fatalf("bootstrap admin is not an admin: %v", st.Platform.Admins)
	}
	if tr, _ := st.ProgramTraining("a", "t1"); tr == nil {
		t.Fatalf("content did not sync from the store's trainings row: %+v", st.Problems)
	}

	// a write through org, then a refresh, is visible with no git for config
	must(s.SetTeam(ctx, "boss@example.com", "a", TeamBody{Version: 1, Name: "A2", Leader: "l@x.com", Trainees: []string{"u@x.com"}}))
	must(sy.SyncOnce(ctx))
	if got := sy.Current().Platform.Teams["a"].Name; got != "A2" {
		t.Fatalf("team name = %q", got)
	}

	// a pin whose commit is not in the mirror degrades: the instance keeps serving
	_, good := sy.Current().ProgramTraining("a", "t1")
	must(s.SetPin(ctx, "boss@example.com", "a", "t1", strings.Repeat("a", 40)))
	if err := sy.SyncOnce(ctx); err != nil {
		t.Fatalf("an unresolvable pin must not fail the sync: %v", err)
	}
	st = sy.Current()
	if _, sha := st.ProgramTraining("a", "t1"); sha != good || len(st.Problems["a/t1"]) == 0 {
		t.Fatalf("want last good %s with a problem, got %q %+v", good, sha, st.Problems)
	}
}
