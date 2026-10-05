package gitsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

// Writer commits config changes from the UI to the platform repo as the bot (spec §6): a direct commit with the
// message "crucible: <action> by <email>" and a Crucible-Actor trailer, pushed with optimistic concurrency.
type Writer struct {
	URL, Branch string
	Dir         string // the writer's own working clone
	Name, Email string // bot identity
	mu          sync.Mutex
}

// Change is one config edit.
type Change struct {
	Action string   // e.g. "update program forge/forge-101"
	Actor  string   // the user's email
	Base   string   // platform SHA the user's page showed; "" skips the stale check
	Paths  []string // repo-relative files the edit touches (checked against Base)
	Edit   func(dir string) error
}

var (
	ErrStale = apperr.Wrap(apperr.Conflict, "Someone changed this in git, reload")
	errRaced = errors.New("push rejected: the branch moved")
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Apply runs ch.Edit on the branch tip, validates the result with config.Load, commits and pushes. When the push is
// rejected because the branch moved, it starts again from the new tip and re-applies the edit (our "rebase"), up to
// 3 retries. A file changed since Base, or an invalid result, is returned to the user. It returns the new commit, or
// the tip when the edit changed nothing.
func (w *Writer) Apply(ctx context.Context, ch Change) (string, error) {
	if strings.HasPrefix(w.URL, "-") || strings.HasPrefix(w.Branch, "-") {
		return "", fmt.Errorf("invalid platform repo %q or branch %q", w.URL, w.Branch)
	}
	if ch.Base != "" && !shaRE.MatchString(ch.Base) {
		return "", apperr.Wrap(apperr.Invalid, "base_sha must be a 40-character commit id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for attempt := 0; ; attempt++ {
		sha, err := w.try(ctx, ch)
		if !errors.Is(err, errRaced) {
			return sha, err
		}
		if attempt == 3 {
			return "", apperr.Wrap(apperr.Conflict, "the platform repo is busy; try again")
		}
	}
}

func (w *Writer) try(ctx context.Context, ch Change) (string, error) {
	if err := w.reset(ctx); err != nil {
		return "", err
	}
	if ch.Base != "" {
		if _, err := git(ctx, w.Dir, "merge-base", "--is-ancestor", ch.Base, "HEAD"); err != nil {
			return "", ErrStale
		}
		if _, err := git(ctx, w.Dir, append([]string{"diff", "--quiet", ch.Base, "HEAD", "--"}, ch.Paths...)...); err != nil {
			return "", ErrStale
		}
	}
	if err := ch.Edit(w.Dir); err != nil {
		return "", err
	}
	if _, err := config.Load(w.Dir); err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		return "", apperr.Wrap(apperr.Invalid, "this change would make the platform config invalid: "+first)
	}
	if _, err := git(ctx, w.Dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := git(ctx, w.Dir, "diff", "--cached", "--quiet"); err == nil {
		return git(ctx, w.Dir, "rev-parse", "HEAD") // nothing changed
	}
	actor := strings.ToLower(ch.Actor)
	if _, err := git(ctx, w.Dir, "-c", "user.name="+w.Name, "-c", "user.email="+w.Email, "commit", "-q",
		"-m", "crucible: "+ch.Action+" by "+actor, "-m", "Crucible-Actor: "+actor); err != nil {
		return "", err
	}
	if _, err := git(ctx, w.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+w.Branch); err != nil {
		if msg := err.Error(); strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]") {
			return "", errRaced
		}
		return "", err
	}
	return git(ctx, w.Dir, "rev-parse", "HEAD")
}

// reset makes Dir a clean checkout of the remote branch tip, cloning it the first time.
func (w *Writer) reset(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err != nil {
		_ = os.RemoveAll(w.Dir)
		if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, "", "clone", "-q", "--branch", w.Branch, "--", w.URL, w.Dir); err != nil {
			return err
		}
	}
	if _, err := git(ctx, w.Dir, "fetch", "-q", "origin", w.Branch); err != nil {
		return err
	}
	if _, err := git(ctx, w.Dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	_, err := git(ctx, w.Dir, "clean", "-qfdx")
	return err
}
