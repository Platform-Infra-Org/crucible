package gitsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func trainingFiles() map[string]string {
	return map[string]string{
		"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [m@x]\nmodules: [m1]\n",
		"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n",
		"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
	}
}

func contentRepo(t *testing.T, remote string) *ContentRepo {
	return &ContentRepo{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "edits"), Name: "Crucible", Email: "bot@x"}
}

// pushTo commits files to the remote's main from a separate clone (someone else pushing).
func pushTo(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "other")
	run(t, "", "clone", "-q", remote, work)
	for rel, body := range files {
		if err := writeFile(work, rel, body); err != nil {
			t.Fatal(err)
		}
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qm", "other change")
	run(t, work, "push", "-q", "origin", "HEAD:main")
}

func TestPushEditAndMerge(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	sha, diff, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{
		"modules/m1/reading/intro.md":    "# Intro\n\nHello, smith.\n",
		"modules/m1/checks/new-check.sh": "#!/bin/sh\nexit 0\n",
	}, "A@X", "Greet the smith")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+Hello, smith.") || gitOut(t, remote, "rev-parse", "crucible/edit/1") != sha {
		t.Fatalf("diff %q / branch", diff)
	}
	if got := gitOut(t, remote, "log", "-1", "--format=%an <%ae>|%cn|%B", sha); !strings.Contains(got, "a@x <a@x>|Crucible|Greet the smith") || !strings.Contains(got, "Crucible-Actor: a@x") {
		t.Fatalf("author is the user, committer the bot: %q", got)
	}
	if mode := gitOut(t, remote, "ls-tree", sha, "modules/m1/checks/new-check.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("new scripts are executable: %q", mode)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("pushing an edit never touches main")
	}
	merged, err := c.Merge(ctx, "crucible/edit/1", "crucible: merge edit 1", "m@x")
	if err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(gitOut(t, remote, "rev-list", "--parents", "-n1", "main")); len(parents) != 3 || parents[0] != merged {
		t.Fatalf("a merge commit on main: %v", parents)
	}
	if body := gitOut(t, remote, "show", "main:modules/m1/reading/intro.md"); !strings.Contains(body, "smith") {
		t.Fatal("merged content")
	}
	if got := gitOut(t, remote, "log", "-1", "--format=%an|%B", "main"); !strings.HasPrefix(got, "Crucible|") || !strings.Contains(got, "Crucible-Actor: m@x") {
		t.Fatalf("merge commit is the bot's with the approver's trailer: %q", got)
	}
	if err := c.DeleteBranch(ctx, "crucible/edit/1"); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, remote, "branch", "--list", "crucible/edit/1"); out != "" {
		t.Fatalf("branch deleted: %q", out)
	}
	if err := c.DeleteBranch(ctx, "crucible/edit/1"); err != nil {
		t.Fatalf("deleting a gone branch is fine: %v", err)
	}
}

func TestGitErrorsHideCredentials(t *testing.T) {
	_, err := git(context.Background(), "", "ls-remote", "--", "https://bot:s3cret@127.0.0.1:1/x.git")
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "https://***@127.0.0.1") {
		t.Fatalf("credentials in error: %v", err)
	}
}

func TestMergeConflictPushesNothing(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/2", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine"); err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nTheirs.\n"})
	tip := gitOut(t, remote, "rev-parse", "main")
	if _, err := c.Merge(ctx, "crucible/edit/2", "merge", "m@x"); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != tip {
		t.Fatal("a conflicting merge pushes nothing")
	}
}

func TestMergeLandsOnTopOfNewerCommits(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/3", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine"); err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/extra.md": "# Extra\n"})
	if _, err := c.Merge(ctx, "crucible/edit/3", "merge", "m@x"); err != nil {
		t.Fatal(err)
	}
	if gitOut(t, remote, "show", "main:modules/m1/reading/extra.md") == "" || !strings.Contains(gitOut(t, remote, "show", "main:modules/m1/reading/intro.md"), "Mine.") {
		t.Fatal("both changes on main")
	}
}

func TestMergeRefusesInvalidResult(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/4", base, map[string]string{"training.yaml": "id: t1\ntitle: T1\nmaintainers: [m@x]\nmodules: [m1, missing]\n"}, "a@x", "break it"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, "crucible/edit/4", "merge", "m@x"); !errors.Is(err, ErrMergeInvalid) || !errors.Is(err, apperr.Conflict) {
		t.Fatalf("invalid result: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("nothing pushed")
	}
}

func TestMergeRefusesMaintainerChangeOnTheBranch(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	c := contentRepo(t, remote)
	// someone with git access pushes a maintainers change straight to an edit branch
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "clone", "-q", remote, work)
	if err := writeFile(work, "training.yaml", "id: t1\ntitle: T1\nmaintainers: [a@x]\nmodules: [m1]\n"); err != nil {
		t.Fatal(err)
	}
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qam", "me")
	run(t, work, "push", "-q", "origin", "HEAD:refs/heads/crucible/edit/9")
	base := gitOut(t, remote, "rev-parse", "main")
	if _, err := c.Merge(ctx, "crucible/edit/9", "merge", "m@x"); !errors.Is(err, ErrMergeInvalid) {
		t.Fatalf("maintainers change: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("nothing pushed")
	}
}

func TestPushEditRefusesSymlinksAndBadNames(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "clone", "-q", remote, work)
	if err := os.Symlink("/etc", filepath.Join(work, "modules", "m1", "linked")); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qm", "symlink")
	run(t, work, "push", "-q", "origin", "HEAD:main")
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/5", base, map[string]string{"modules/m1/linked/passwd.md": "x"}, "a@x", "x"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("writing through a symlinked folder: %v", err)
	}
	for _, b := range []string{"main", "crucible/edit/x", "--upload-pack=evil"} {
		if _, _, err := c.PushEdit(ctx, b, base, map[string]string{"a.md": "x"}, "a@x", "x"); err == nil {
			t.Fatalf("branch %q accepted", b)
		}
	}
}

func TestPushEditRefusesBadFiles(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	many := map[string]string{}
	for i := range 21 {
		many[filepath.Join("modules", "m1", "reading", string(rune('a'+i))+".md")] = "x"
	}
	cases := map[string]map[string]string{
		"dotdot":       {"../escape.md": "x"},
		"inner dotdot": {"modules/../../escape.md": "x"},
		"absolute":     {"/tmp/x.md": "x"},
		"git dir":      {".git/hooks/pre-commit.sh": "x"},
		"nested git":   {"modules/.GIT/config.yaml": "x"},
		"backslash":    {`modules\m1\x.md`: "x"},
		"nul":          {"a\x00.md": "x"},
		"extension":    {"modules/m1/main.tf": "x"},
		"binary":       {"a.md": "x\x00y"},
		"too big":      {"a.md": strings.Repeat("x", 256<<10+1)},
		"too many":     many,
		"empty":        {},
		"maintainers":  {"training.yaml": "id: t1\ntitle: T1\nmaintainers: [a@x]\nmodules: [m1]\n"},
		"unclean":      {"modules//m1/x.md": "x"},
	}
	for name, files := range cases {
		if _, _, err := c.PushEdit(ctx, "crucible/edit/6", base, files, "a@x", "x"); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := c.PushEdit(ctx, "crucible/edit/6", base, map[string]string{"a.md": "x"}, "a@x>\n-x <e", "x"); !errors.Is(err, apperr.Invalid) {
		t.Errorf("author: %v", err)
	}
	if out := gitOut(t, remote, "branch", "--list", "crucible/edit/6"); out != "" {
		t.Fatalf("nothing pushed: %q", out)
	}
}

func TestPushEditRefusesABaseOffTheBranch(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	other, _, err := c.PushEdit(ctx, "crucible/edit/7", base, map[string]string{"a.md": "unreviewed\n"}, "a@x", "x")
	if err != nil {
		t.Fatal(err)
	}
	// basing an edit on another unmerged edit would smuggle its changes past the diff
	if _, _, err := c.PushEdit(ctx, "crucible/edit/8", other, map[string]string{"b.md": "x"}, "a@x", "x"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("base off the tracked branch: %v", err)
	}
}
