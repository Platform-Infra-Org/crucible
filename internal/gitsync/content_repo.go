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
	ErrMergeInvalid  = apperr.Wrap(apperr.Conflict, "merged with the current content, this edit would be invalid")
	editBranchRE     = regexp.MustCompile(`^crucible/edit/[0-9]+$`)
	editExts         = []string{".md", ".yaml", ".yml", ".sh"}
)

const (
	maxDiff      = 256 << 10
	maxEditFiles = 20
	maxEditFile  = 256 << 10
)

func (c *ContentRepo) lock(ctx context.Context) (func(), error) {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// checkEditFiles enforces what a UI edit may touch: 1–20 text files (.md/.yaml/.yml/.sh, ≤256 KiB) at clean paths
// inside the repo. There are no deletes: every entry is the file's new content.
func checkEditFiles(files map[string]string) error {
	if len(files) == 0 || len(files) > maxEditFiles {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("an edit changes 1 to %d files", maxEditFiles))
	}
	for rel, body := range files {
		if err := cleanRel(rel); err != nil {
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

// PushEdit commits files (repo-relative path → new content) on top of base as the user, pushes them as branch, and
// returns the commit and its unified diff against base (capped at 256 KiB). New *.sh files are executable; existing
// files keep their mode. base must be on the tracked branch, so the diff shows everything a merge would bring in.
func (c *ContentRepo) PushEdit(ctx context.Context, branch, base string, files map[string]string, author, msg string) (string, string, error) {
	if !editBranchRE.MatchString(branch) || !shaRE.MatchString(base) {
		return "", "", fmt.Errorf("invalid edit branch %q or base %q", branch, base)
	}
	author = strings.ToLower(author)
	if author == "" || strings.ContainsAny(author, "<>\n\r\t ") {
		return "", "", apperr.Wrap(apperr.Invalid, "invalid author")
	}
	if err := checkEditFiles(files); err != nil {
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
	before, beforeErr := maintainers(c.Dir)
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
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(files[rel]), mode); err != nil {
			return "", "", err
		}
		if err := os.Chmod(p, mode); err != nil {
			return "", "", err
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
		diff = strings.ToValidUTF8(diff[:maxDiff], "") + "\n… (diff truncated)"
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", "", err
	}
	return sha, diff, nil
}

// Merge merges branch into the tracked branch with a bot merge commit and pushes it. When the push is rejected because
// the branch moved, it starts again from the new tip, up to 3 retries. A conflict (ErrMergeConflict) or a merged tree
// that content.Load rejects or whose maintainers differ (ErrMergeInvalid) pushes nothing.
func (c *ContentRepo) Merge(ctx context.Context, branch, msg, actor string) (string, error) {
	if !editBranchRE.MatchString(branch) {
		return "", fmt.Errorf("invalid edit branch %q", branch)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	for attempt := 0; ; attempt++ {
		sha, err := c.tryMerge(ctx, branch, msg, strings.ToLower(actor))
		if !errors.Is(err, errRaced) {
			return sha, err
		}
		if attempt == 3 {
			return "", apperr.Wrap(apperr.Conflict, "the content repo is busy; try again")
		}
	}
}

func (c *ContentRepo) tryMerge(ctx context.Context, branch, msg, actor string) (string, error) {
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", err
	}
	if _, err := git(ctx, c.Dir, "fetch", "-q", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return "", err
	}
	before, beforeErr := maintainers(c.Dir)
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "merge", "-q", "--no-ff", "--no-edit",
		"-m", msg, "-m", "Crucible-Actor: "+actor, "--end-of-options", "refs/remotes/origin/"+branch); err != nil {
		_, _ = git(ctx, c.Dir, "merge", "--abort")
		return "", ErrMergeConflict
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
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return err
	}
	_, err = git(ctx, c.Dir, "push", "-q", "origin", "--delete", "refs/heads/"+branch)
	if err != nil && strings.Contains(err.Error(), "remote ref does not exist") {
		return nil
	}
	return err
}
