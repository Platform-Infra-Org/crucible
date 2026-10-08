package gitsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain allows the file transport: these tests use local bare repos as remotes.
func TestMain(m *testing.M) {
	AllowFileTransport = true
	os.Exit(m.Run())
}

// bare returns a bare repo seeded with files (pushes need a bare remote).
func bare(t *testing.T, files map[string]string) string {
	t.Helper()
	work := newRepo(t, files)
	dir := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "clone", "-q", "--bare", work, dir)
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(dir, rel, body string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
}

func TestSyncCloneRecoversABrokenWorkingCopy(t *testing.T) {
	remote := bare(t, map[string]string{"a.txt": "one\n"})
	dir := filepath.Join(t.TempDir(), "clone")
	ctx := context.Background()
	if err := syncClone(ctx, remote, "main", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncClone(ctx, remote, "main", dir); err != nil {
		t.Fatalf("re-clone a broken working copy: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(b) != "one\n" {
		t.Fatalf("checkout after recovery: %q %v", b, err)
	}
	if err := syncClone(ctx, "--upload-pack=x", "main", dir); err == nil {
		t.Fatal("an option-looking url must be refused")
	}
}
