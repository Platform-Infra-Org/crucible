package gitsync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

// ContentRepo is the bot's working clone of one content repo for edits made in the UI (spec §6). An edit is pushed to its
// own branch; once a maintainer approves it, the bot merges that branch into the tracked branch with a merge commit.
// It needs nothing but plain git, so it works with any host.
type ContentRepo struct {
	URL, Branch string // the training's repo and tracked branch (trainings.yaml)
	Dir         string // working clone, owned by this value
	Name, Email string // bot identity (committer)

	once sync.Once
	sem  chan struct{} // one git operation at a time per repo; a channel so waiting respects ctx
}

var (
	ErrMergeConflict = apperr.Wrap(apperr.Conflict, "this edit no longer applies cleanly to the current content")
	ErrEditMoved     = apperr.Wrap(apperr.Conflict, "this edit changed since it was reviewed; review it again")
	ErrMergeInvalid  = apperr.Wrap(apperr.Conflict, "merged with the current content, this edit would be invalid")
	editBranchRE     = regexp.MustCompile(`^crucible/edit/[0-9]+$`)
	editExts         = []string{".md", ".yaml", ".yml", ".sh"}
)

const (
	maxDiff      = 256 << 10
	maxEditFiles = 20
	maxEditFile  = 256 << 10
	maxMsg       = 4 << 10
)

// cleanEmail lowercases an author or actor and refuses anything that could break out of a commit header or trailer.
func cleanEmail(s string) (string, error) {
	s = strings.ToLower(s)
	if s == "" || len(s) > 254 || strings.ContainsAny(s, "<>\n\r\t \x00") {
		return "", apperr.Wrap(apperr.Invalid, "invalid author")
	}
	return s, nil
}

// cleanMsg caps a commit message from user text and drops lines that would forge the Crucible-Actor trailer.
func cleanMsg(msg string) (string, error) {
	if len(msg) > maxMsg || strings.ContainsRune(msg, 0) || !utf8.ValidString(msg) {
		return "", apperr.Wrap(apperr.Invalid, "the message must be text of at most 4 KiB")
	}
	lines := strings.Split(msg, "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "crucible-actor:")
	})
	if msg = strings.TrimSpace(strings.Join(lines, "\n")); msg == "" {
		msg = "crucible: edit"
	}
	return msg, nil
}

func (c *ContentRepo) lock(ctx context.Context) (func(), error) {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CheckEditFiles enforces what a UI edit may touch: 1–20 text files (.md/.yaml/.yml/.sh, ≤256 KiB): training.yaml or
// files inside modules/<id>/ (editPath). There are no deletes: every entry is the file's new content.
func CheckEditFiles(files map[string]string) error {
	if len(files) == 0 || len(files) > maxEditFiles {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("an edit changes 1 to %d files", maxEditFiles))
	}
	for rel, body := range files {
		if err := editPath(rel); err != nil {
			return apperr.Wrap(apperr.Invalid, err.Error())
		}
		if !slices.Contains(editExts, strings.ToLower(path.Ext(rel))) {
			return apperr.Wrap(apperr.Invalid, rel+": only .md, .yaml, .yml and .sh files can be edited here")
		}
		if len(body) > maxEditFile || strings.ContainsRune(body, 0) || !utf8.ValidString(body) {
			return apperr.Wrap(apperr.Invalid, rel+": must be text of at most 256 KiB")
		}
	}
	return nil
}

// maintainers reads training.yaml's maintainers: the people who review edits, so an edit must never change them.
func maintainers(dir string) ([]string, error) {
	var t struct {
		Maintainers []string `yaml:"maintainers"`
	}
	err := yamlx.ReadLoose(filepath.Join(dir, "training.yaml"), &t)
	return t.Maintainers, err
}

// labDir is modules/<id>/<lab>/ when the module's module.yaml has a lab item, else "". Only scripts under it are made
// executable: the content model runs setup and check scripts from the lab directory, nothing else.
func labDir(dir, rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 {
		return ""
	}
	var m struct {
		Items []map[string]string `yaml:"items"`
	}
	if yamlx.ReadLoose(filepath.Join(dir, "modules", parts[1], "module.yaml"), &m) != nil {
		return ""
	}
	for _, it := range m.Items {
		if lab, ok := it["lab"]; ok && filepath.IsLocal(lab) {
			return path.Join("modules", parts[1], path.Clean(lab)) + "/"
		}
	}
	return ""
}

// caseClash reports an edited path that differs only in case from an existing path (file or folder) at HEAD, or from
// another edited path: those collide on macOS and Windows checkouts.
func caseClash(ctx context.Context, dir string, files map[string]string) error {
	out, err := git(ctx, dir, "ls-tree", "-r", "-t", "--name-only", "HEAD")
	if err != nil {
		return err
	}
	have := map[string]string{} // lowercased → actual
	for p := range strings.SplitSeq(out, "\n") {
		have[strings.ToLower(p)] = p
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		parts := strings.Split(rel, "/")
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			if got, ok := have[strings.ToLower(p)]; ok && got != p {
				return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s: %s already exists with different case", rel, got))
			}
			have[strings.ToLower(p)] = p
		}
	}
	return nil
}

// PushEdit commits files (repo-relative path → new content) on top of base as the user, pushes them as branch, and
// returns the commit and its full unified diff against base; an edit whose diff is over 256 KiB is refused, so the
// reviewer always sees everything. New *.sh files inside the module's lab are executable, other new files are not;
// existing files keep their mode. base must be on the tracked branch, so the diff shows everything a merge would bring in.
func (c *ContentRepo) PushEdit(ctx context.Context, branch, base string, files map[string]string, author, msg string) (string, string, error) {
	if !editBranchRE.MatchString(branch) || !shaRE.MatchString(base) {
		return "", "", fmt.Errorf("invalid edit branch %q or base %q", branch, base)
	}
	author, err := cleanEmail(author)
	if err != nil {
		return "", "", err
	}
	if msg, err = cleanMsg(msg); err != nil {
		return "", "", err
	}
	if err := CheckEditFiles(files); err != nil {
		return "", "", err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", "", err
	}
	stale := apperr.Wrap(apperr.Conflict, "the content changed since you opened it; reload and redo your change")
	if _, err := git(ctx, c.Dir, "merge-base", "--is-ancestor", "--end-of-options", base, "HEAD"); err != nil {
		return "", "", stale
	}
	// base is 40 hex (checked above), so it can't be an option; checkout --detach rejects --end-of-options before git 2.43
	if _, err := git(ctx, c.Dir, "checkout", "-q", "--detach", base); err != nil {
		return "", "", stale
	}
	if err := caseClash(ctx, c.Dir, files); err != nil {
		return "", "", err
	}
	before, beforeErr := maintainers(c.Dir)
	var scripts []string
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if err := NoSymlinks(c.Dir, rel); err != nil {
			return "", "", apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s can't be edited: %v", rel, err))
		}
		p := filepath.Join(c.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", "", apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s can't be edited: %v", rel, err))
		}
		mode := os.FileMode(0o644)
		if fi, err := os.Lstat(p); err == nil {
			if !fi.Mode().IsRegular() {
				return "", "", apperr.Wrap(apperr.Invalid, rel+" can't be edited: not a regular file")
			}
			mode = fi.Mode().Perm()
		} else if strings.HasSuffix(rel, ".sh") {
			scripts = append(scripts, rel) // made executable below if module.yaml (maybe in this edit) puts it in the lab
		}
		if err := os.WriteFile(p, []byte(files[rel]), mode); err != nil {
			return "", "", err
		}
		if err := os.Chmod(p, mode); err != nil {
			return "", "", err
		}
	}
	for _, rel := range scripts {
		if lab := labDir(c.Dir, rel); lab != "" && strings.HasPrefix(rel, lab) {
			if err := os.Chmod(filepath.Join(c.Dir, filepath.FromSlash(rel)), 0o755); err != nil {
				return "", "", err
			}
		}
	}
	if _, ok := files["training.yaml"]; ok {
		after, err := maintainers(c.Dir)
		if beforeErr != nil || err != nil || !slices.Equal(before, after) {
			return "", "", apperr.Wrap(apperr.Invalid, "training.yaml: maintainers can only be changed in git")
		}
	}
	if _, err := git(ctx, c.Dir, "add", "-A"); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "commit", "-q", "--allow-empty",
		"--author", author+" <"+author+">", "-m", msg, "-m", "Crucible-Actor: "+author); err != nil {
		return "", "", err
	}
	sha, err := git(ctx, c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	diff, err := git(ctx, c.Dir, "diff", "--no-color", "--end-of-options", base, sha)
	if err != nil {
		return "", "", err
	}
	if len(diff) > maxDiff {
		return "", "", apperr.Wrap(apperr.Invalid, "this edit is too large to review here (diff over 256 KiB); split it or change it in git")
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", "", err
	}
	return sha, diff, nil
}

// Merge merges sha, the edit commit that was reviewed, into the tracked branch with a bot merge commit and pushes it.
// sha must still be branch's tip (ErrEditMoved otherwise: someone pushed to the branch after review). When the push is
// rejected because the tracked branch moved, it starts again from the new tip, up to 3 retries. A conflict
// (ErrMergeConflict) or a merged tree that content.Load rejects or whose maintainers differ (ErrMergeInvalid) pushes
// nothing; any other failure is returned as is, so the caller can retry it.
func (c *ContentRepo) Merge(ctx context.Context, branch, sha, msg, actor string) (string, error) {
	if !editBranchRE.MatchString(branch) || !shaRE.MatchString(sha) {
		return "", fmt.Errorf("invalid edit branch %q or sha %q", branch, sha)
	}
	actor, err := cleanEmail(actor)
	if err != nil {
		return "", err
	}
	if msg, err = cleanMsg(msg); err != nil {
		return "", err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	for attempt := 0; ; attempt++ {
		merged, err := c.tryMerge(ctx, branch, sha, msg, actor)
		if !errors.Is(err, errRaced) {
			return merged, err
		}
		if attempt == 3 {
			return "", apperr.Wrap(apperr.Conflict, "the content repo is busy; try again")
		}
	}
}

func (c *ContentRepo) tryMerge(ctx context.Context, branch, sha, msg, actor string) (string, error) {
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", err
	}
	if _, err := git(ctx, c.Dir, "fetch", "-q", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return "", err
	}
	if tip, err := git(ctx, c.Dir, "rev-parse", "--verify", "refs/remotes/origin/"+branch+"^{commit}"); err != nil {
		return "", err
	} else if tip != sha {
		return "", ErrEditMoved
	}
	before, beforeErr := maintainers(c.Dir)
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "merge", "-q", "--no-ff", "--no-edit",
		"-m", msg, "-m", "Crucible-Actor: "+actor, "--end-of-options", sha); err != nil {
		unmerged, uerr := git(context.WithoutCancel(ctx), c.Dir, "diff", "--name-only", "--diff-filter=U")
		_, _ = git(context.WithoutCancel(ctx), c.Dir, "merge", "--abort")
		if uerr == nil && unmerged != "" {
			return "", ErrMergeConflict
		}
		return "", fmt.Errorf("merging %s: %w", branch, err)
	}
	if _, probs := content.Load(c.Dir); len(probs) > 0 {
		p := probs[0]
		if rel, err := filepath.Rel(c.Dir, p.File); err == nil {
			p.File = rel
		}
		return "", fmt.Errorf("%w: %s", ErrMergeInvalid, p)
	}
	if after, err := maintainers(c.Dir); beforeErr == nil && (err != nil || !slices.Equal(before, after)) {
		return "", fmt.Errorf("%w: training.yaml: maintainers can only be changed in git", ErrMergeInvalid)
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+c.Branch); err != nil {
		if raced(err) {
			return "", errRaced
		}
		return "", err
	}
	return git(ctx, c.Dir, "rev-parse", "HEAD")
}

// DeleteBranch removes an edit branch from the remote once the edit is decided. A branch that is already gone is fine.
func (c *ContentRepo) DeleteBranch(ctx context.Context, branch string) error {
	if !editBranchRE.MatchString(branch) {
		return fmt.Errorf("invalid edit branch %q", branch)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if strings.HasPrefix(c.URL, "-") {
		return fmt.Errorf("invalid repo %q", redact(c.URL))
	}
	if _, err := os.Stat(filepath.Join(c.Dir, ".git")); err != nil { // push needs a repo, not an up-to-date one
		if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
			return err
		}
	}
	_, err = git(ctx, c.Dir, "push", "-q", "--delete", c.URL, "refs/heads/"+branch)
	if err != nil && strings.Contains(err.Error(), "remote ref does not exist") {
		return nil
	}
	return err
}
