package gitsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

const validPlatform = "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n"

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

func platformFiles() map[string]string {
	return map[string]string{
		"platform.yaml":            validPlatform,
		"trainings.yaml":           "trainings:\n  t1: {repo: file:///nowhere}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	}
}

func writeFile(dir, rel, body string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
}

func newWriter(t *testing.T, remote string) *Writer {
	return &Writer{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "writer"), Name: "Crucible", Email: "bot@crucible.local"}
}

func TestWriterCommitsWithTrailer(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	sha, _, err := w.Apply(context.Background(), Change{Action: "update roster a", Actor: "L@X", Paths: []string{"teams/a/team.yaml"},
		Edit: func(dir string) error {
			return writeFile(dir, "teams/a/team.yaml", "name: A\nleader: l@x\nseniors: [s@x]\ntrainees: [u@x]\n")
		}})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitOut(t, remote, "rev-parse", "main"); head != sha {
		t.Fatalf("pushed %s, remote at %s", sha, head)
	}
	msg := gitOut(t, remote, "log", "-1", "--format=%an <%ae>%n%B", "main")
	if !strings.Contains(msg, "Crucible <bot@crucible.local>") || !strings.Contains(msg, "crucible: update roster a by l@x") || !strings.Contains(msg, "Crucible-Actor: l@x") {
		t.Fatalf("commit:\n%s", msg)
	}
	again, changed, err := w.Apply(context.Background(), Change{Action: "noop", Actor: "l@x", Edit: func(string) error { return nil }})
	if err != nil || again != sha || changed {
		t.Fatalf("an edit that changes nothing makes no commit: %s %v", again, err)
	}
}

func TestWriterRetriesWhenBranchMoves(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	calls := 0
	_, _, err := w.Apply(context.Background(), Change{Action: "update program a/t1", Actor: "l@x", Paths: []string{"teams/a/programs/t1.yaml"},
		Edit: func(dir string) error {
			calls++
			if calls == 1 { // someone else pushes an unrelated commit before ours
				other := filepath.Join(t.TempDir(), "other")
				run(t, "", "clone", "-q", remote, other)
				if err := writeFile(other, "quotes.yaml", "quotes: [\"x\"]\n"); err != nil {
					return err
				}
				run(t, other, "add", "-A")
				run(t, other, "commit", "-qm", "quotes")
				run(t, other, "push", "-q", "origin", "HEAD:main")
			}
			return writeFile(dir, "teams/a/programs/t1.yaml", "enrolled: [u@x]\nschedule: \"\"\n")
		}})
	if err != nil || calls != 2 {
		t.Fatalf("retry on the new tip: calls=%d err=%v", calls, err)
	}
	files := gitOut(t, remote, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "quotes.yaml") {
		t.Fatalf("the concurrent commit must survive:\n%s", files)
	}
}

func TestWriterStaleBase(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	base := gitOut(t, remote, "rev-parse", "main")
	edit := func(rel, body string) Change {
		return Change{Action: "edit " + rel, Actor: "l@x", Base: base, Paths: []string{rel},
			Edit: func(dir string) error { return writeFile(dir, rel, body) }}
	}
	if _, _, err := w.Apply(context.Background(), edit("teams/a/team.yaml", "name: A2\nleader: l@x\ntrainees: [u@x]\n")); err != nil {
		t.Fatal(err)
	}
	_, _, err := w.Apply(context.Background(), edit("teams/a/team.yaml", "name: A3\nleader: l@x\ntrainees: [u@x]\n"))
	if !errors.Is(err, ErrStale) || !errors.Is(err, apperr.Conflict) {
		t.Fatalf("same file changed since the page loaded: %v", err)
	}
	if _, _, err := w.Apply(context.Background(), edit("quotes.yaml", "quotes: [\"y\"]\n")); err != nil {
		t.Fatalf("a different file is not stale: %v", err)
	}
	if _, _, err := w.Apply(context.Background(), Change{Action: "x", Actor: "l@x", Base: "--upload-pack=evil", Edit: func(string) error { return nil }}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("base must be a sha: %v", err)
	}
}

func TestWriterRejectsInvalidConfig(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	before := gitOut(t, remote, "rev-parse", "main")
	_, _, err := w.Apply(context.Background(), Change{Action: "break", Actor: "l@x",
		Edit: func(dir string) error { return writeFile(dir, "teams/a/team.yaml", "name: A\ntrainees: [u@x]\n") }})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "no leader") {
		t.Fatalf("invalid config: %v", err)
	}
	if after := gitOut(t, remote, "rev-parse", "main"); after != before {
		t.Fatal("nothing may be pushed")
	}
}

func TestWriterRecoversABrokenWorkingCopy(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	edit := func(body string) Change {
		return Change{Action: "edit", Actor: "l@x", Edit: func(dir string) error { return writeFile(dir, "quotes.yaml", body) }}
	}
	if _, _, err := w.Apply(context.Background(), edit("quotes: [\"a\"]\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, ".git", "HEAD"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := w.Apply(context.Background(), edit("quotes: [\"b\"]\n")); err != nil || !changed {
		t.Fatalf("re-clone a broken working copy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.sem <- struct{}{} // someone else is writing
	cancel()
	if _, _, err := w.Apply(ctx, edit("quotes: [\"c\"]\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting for the lock respects ctx: %v", err)
	}
}

func TestWriterAllowSeesTheTip(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	other := filepath.Join(t.TempDir(), "other")
	run(t, "", "clone", "-q", remote, other)
	if err := writeFile(other, "teams/a/team.yaml", "name: A\nleader: n@x\ntrainees: [u@x]\n"); err != nil {
		t.Fatal(err)
	}
	run(t, other, "commit", "-qam", "new leader")
	run(t, other, "push", "-q", "origin", "HEAD:main")
	var leader string
	_, _, err := w.Apply(context.Background(), Change{Action: "x", Actor: "l@x",
		Allow: func(p *config.Platform) error {
			leader = p.Teams["a"].Leader
			return apperr.Forbidden
		}, Edit: func(string) error { t.Fatal("no edit after a refusal"); return nil }})
	if !errors.Is(err, apperr.Forbidden) || leader != "n@x" {
		t.Fatalf("allow at tip: %v %q", err, leader)
	}
}

func TestWriterNamesAFileThatWasAlreadyBroken(t *testing.T) {
	files := platformFiles()
	files["teams/a/programs/t1.yaml"] = "enrolled: [u@x]\nschedule: nope\n"
	w := newWriter(t, bare(t, files))
	_, _, err := w.Apply(context.Background(), Change{Action: "x", Actor: "l@x",
		Edit: func(dir string) error { return writeFile(dir, "quotes.yaml", "quotes: [\"q\"]\n") }})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "currently has errors in teams/a/programs/t1.yaml") {
		t.Fatalf("pre-existing error: %v", err)
	}
}
