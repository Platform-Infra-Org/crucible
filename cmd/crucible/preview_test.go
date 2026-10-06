package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/config"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	_ = os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
	if err := os.WriteFile(filepath.Join(root, rel), []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(root, rel), mode)
}

func TestSnapshotCopiesTrackedFilesOnly(t *testing.T) {
	src, work := t.TempDir(), t.TempDir()
	gitIn(t, src, "init", "-q")
	writeFile(t, src, "training.yaml", "id: t1\ntitle: T1\nprogression: linear\nmodules: [m1]\n", 0o644)
	writeFile(t, src, "modules/m1/checks/c.sh", "#!/bin/sh\n", 0o755)
	writeFile(t, src, ".gitignore", "secret.txt\n", 0o644)
	gitIn(t, src, "add", ".")
	writeFile(t, src, ".env", "AWS_SECRET=leak\n", 0o600) // untracked: never previewed
	writeFile(t, src, "secret.txt", "leak\n", 0o600)      // ignored
	changed, err := snapshot(src, work, false)
	if err != nil || !changed {
		t.Fatalf("first snapshot: %v %v", changed, err)
	}
	bare := filepath.Join(work, "git", "content.git")
	if mode := gitIn(t, bare, "ls-tree", "main", "modules/m1/checks/c.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("exec bit kept: %q", mode)
	}
	if out := gitIn(t, bare, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, ".env") || strings.Contains(out, "secret.txt") || strings.Contains(out, ".git/") {
		t.Fatalf("only tracked files are snapshotted: %s", out)
	}
	if changed, _ := snapshot(src, work, false); changed {
		t.Fatal("no change, no commit")
	}
	writeFile(t, src, "training.yaml", "id: t1\ntitle: T1 edited\nprogression: linear\nmodules: [m1]\n", 0o644)
	if changed, _ := snapshot(src, work, false); !changed {
		t.Fatal("an unstaged edit of a tracked file is previewed")
	}
	writeFile(t, src, "modules/m1/new.md", "new\n", 0o644)
	gitIn(t, src, "add", "modules/m1/new.md")
	if changed, _ := snapshot(src, work, false); !changed || !strings.Contains(gitIn(t, bare, "ls-tree", "-r", "--name-only", "main"), "new.md") {
		t.Fatal("a staged new file is previewed")
	}
	_ = os.Remove(filepath.Join(src, "modules/m1/checks/c.sh"))
	if changed, _ := snapshot(src, work, true); !changed {
		t.Fatal("a deleted file is a change")
	}
	if got := gitIn(t, bare, "show", "main:training.yaml"); !strings.Contains(got, "progression: free") {
		t.Fatalf("--free rewrites progression in the snapshot only: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "training.yaml")); !strings.Contains(string(b), "linear") {
		t.Fatal("the author's file is untouched")
	}
}

func TestSnapshotNeedsAGitRepo(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "training.yaml", "id: t1\n", 0o644)
	if _, err := snapshot(src, t.TempDir(), false); err == nil {
		t.Fatal("without git we cannot tell content from secrets: refuse")
	}
}

func TestPreviewPlatformIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := writePlatform(dir, "forge-101"); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Admins, ","), "preview@crucible.local") || len((p.Teams["preview"].Programs["forge-101"]).Enrolled) != 1 {
		t.Fatalf("preview platform %+v", p)
	}
	if p.Trainings["forge-101"].Repo != "file:///git/content.git" {
		t.Fatal("content comes from the snapshot repo")
	}
}

func TestFingerprintNoticesEditsAndStaging(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, dir, "a.md", "a", 0o644)
	gitIn(t, dir, "add", "a.md")
	a, _ := fingerprint(dir)
	writeFile(t, dir, "a.md", "ab", 0o644)
	b, _ := fingerprint(dir)
	if a == b {
		t.Fatal("an edit changes the fingerprint")
	}
	writeFile(t, dir, "b.md", "b", 0o644)
	if c, _ := fingerprint(dir); c != b {
		t.Fatal("untracked files are not previewed, so they do not count")
	}
	gitIn(t, dir, "add", "b.md")
	if c, _ := fingerprint(dir); c == b {
		t.Fatal("staging a new file changes the fingerprint")
	}
}

func TestComposeFileStaysOnLoopback(t *testing.T) {
	c := composeFile("p1", "crucible:dev", 8090, "tok", "hook", "pw1", "/tmp/git")
	for _, want := range []string{`"127.0.0.1:8090:8080"`, `CRUCIBLE_PREVIEW_TOKEN: "tok"`, `CRUCIBLE_GIT_ALLOW_FILE: "1"`, `CRUCIBLE_PUBLIC_URL: http://localhost:8090`, `name: p1`} {
		if !strings.Contains(c, want) {
			t.Errorf("compose file lacks %s:\n%s", want, c)
		}
	}
	if strings.Count(c, "ports:") != 1 {
		t.Error("only the API publishes a port")
	}
}

// fakeDocker records docker invocations and fails the ones whose joined args contain fail.
func fakeDocker(t *testing.T, fail string) *[]string {
	var calls []string
	old := docker
	docker = func(_ io.Writer, args ...string) error {
		line := strings.Join(args, " ")
		calls = append(calls, line)
		if fail != "" && strings.Contains(line, fail) {
			return errors.New("boom")
		}
		return nil
	}
	t.Cleanup(func() { docker = old })
	return &calls
}

func TestPreviewTearsDownWhenStartFails(t *testing.T) {
	calls := fakeDocker(t, " up ")
	if code := preview([]string{"../../examples/forge-101"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	all := strings.Join(*calls, "\n")
	if !strings.Contains(all, "image inspect crucible:dev") || !strings.Contains(all, "up -d --wait") || !strings.Contains(all, "down -v") {
		t.Fatalf("calls:\n%s", all)
	}
}

func TestPreviewNeedsTheImage(t *testing.T) {
	calls := fakeDocker(t, "image inspect")
	if code := preview([]string{"../../examples/forge-101", "--image", "nope:1"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "nope:1") {
		t.Fatalf("nothing starts without the image: %v", *calls)
	}
}

func TestWriteComposeIsPrivateWithRandomPassword(t *testing.T) {
	work := t.TempDir()
	path, tok, _, err := writeCompose(work, "crucible:dev", 8090)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("compose.yml mode %o, want 600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "crucible:crucible@") || strings.Contains(string(b), "POSTGRES_PASSWORD: crucible,") {
		t.Error("the Postgres password must be random per run")
	}
	if !strings.Contains(string(b), tok) {
		t.Error("token missing")
	}
	for _, want := range []string{"read_only: true", "cap_drop: [ALL]", "no-new-privileges:true"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("api hardening %q missing", want)
		}
	}
	p2, _, _, _ := writeCompose(t.TempDir(), "crucible:dev", 8090)
	b2, _ := os.ReadFile(p2)
	if string(b) == string(b2) {
		t.Error("two runs must not share secrets")
	}
}

func TestSnapshotSkipsSymlinkedDirsAndKeepsForceTracked(t *testing.T) {
	src, work, outside := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, src, "training.yaml", "id: t1\n", 0o644)
	writeFile(t, src, ".gitignore", "ign.md\n", 0o644)
	writeFile(t, src, "ign.md", "x", 0o644)
	writeFile(t, src, "modules/m1/a.md", "a", 0o644)
	writeFile(t, outside, "s.txt", "secret", 0o644)
	gitIn(t, src, "init", "-q")
	gitIn(t, src, "add", "-f", ".")
	if err := os.RemoveAll(filepath.Join(src, "modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "modules")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, outside, "m1/a.md", "leak", 0o644)
	if _, err := snapshot(src, work, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "src", "modules", "m1", "a.md")); err == nil {
		t.Error("file reached through a symlinked directory was copied")
	}
	if _, err := os.Stat(filepath.Join(work, "src", "ign.md")); err != nil {
		t.Error("force-tracked ignored file was dropped")
	}
}
