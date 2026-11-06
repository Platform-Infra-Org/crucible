package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A git cut off by its deadline takes its helpers with it: a local push's receive-pack and its hook must not run on,
// writing into the repo after the caller has moved on (and, in tests, while TempDir is being removed).
func TestCancelledGitLeavesNoHelpers(t *testing.T) {
	dir := t.TempDir()
	remote, work, beat := filepath.Join(dir, "r.git"), filepath.Join(dir, "w"), filepath.Join(dir, "beat")
	for _, args := range [][]string{{"init", "-q", "--bare", remote}, {"init", "-q", "-b", "main", work}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	hook := "#!/bin/sh\nwhile :; do touch '" + beat + "'; sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "c"}} {
		if _, err := git(ctx, work, args...); err != nil {
			t.Fatal(err)
		}
	}
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := git(short, work, "push", "-q", remote, "HEAD:main"); err == nil {
		t.Fatal("a push held by its hook must fail at the deadline")
	}
	st, err := os.Stat(beat)
	if err != nil {
		t.Fatalf("the hook never ran: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if after, _ := os.Stat(beat); after.ModTime().After(st.ModTime()) {
		t.Fatal("the hook is still running after git was cancelled")
	}
}
