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

	once sync.Once
	sem  chan struct{} // one write at a time; a channel so waiting respects ctx
}

// Change is one config edit.
type Change struct {
	Action string   // e.g. "update program forge/forge-101"
	Actor  string   // the user's email
	Base   string   // platform SHA the user's page showed; "" skips the stale check
	Paths  []string // repo-relative files the edit touches (checked against Base)
	// Allow, if set, re-checks the actor's permission against the config at the branch tip, so someone removed in git
	// since the page loaded cannot write.
	Allow func(p *config.Platform) error
	Edit  func(dir string) error
}

var (
	ErrStale = apperr.Wrap(apperr.Conflict, "Someone changed this in git, reload")
	errRaced = errors.New("push rejected: the branch moved")
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Apply runs ch.Edit on the branch tip, validates the result with config.Load, commits and pushes. When the push is
// rejected because the branch moved, it starts again from the new tip and re-applies the edit (our "rebase"), up to
// 3 retries. A file changed since Base, or an invalid result, is returned to the user. It returns the new commit and
// changed=true, or the tip and changed=false when the edit changed nothing (no commit is made).
func (w *Writer) Apply(ctx context.Context, ch Change) (sha string, changed bool, err error) {
	if strings.HasPrefix(w.URL, "-") || strings.HasPrefix(w.Branch, "-") {
		return "", false, fmt.Errorf("invalid platform repo %q or branch %q", w.URL, w.Branch)
	}
	if ch.Base != "" && !shaRE.MatchString(ch.Base) {
		return "", false, apperr.Wrap(apperr.Invalid, "base_sha must be a 40-character commit id")
	}
	w.once.Do(func() { w.sem = make(chan struct{}, 1) })
	select {
	case w.sem <- struct{}{}:
		defer func() { <-w.sem }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
	for attempt := 0; ; attempt++ {
		sha, changed, err := w.try(ctx, ch)
		if !errors.Is(err, errRaced) {
			return sha, changed, err
		}
		if attempt == 3 {
			return "", false, apperr.Wrap(apperr.Conflict, "the platform repo is busy; try again")
		}
	}
}

func (w *Writer) try(ctx context.Context, ch Change) (string, bool, error) {
	if err := w.reset(ctx); err != nil {
		return "", false, err
	}
	if ch.Base != "" {
		if _, err := git(ctx, w.Dir, "merge-base", "--is-ancestor", ch.Base, "HEAD"); err != nil {
			return "", false, ErrStale
		}
		if _, err := git(ctx, w.Dir, append([]string{"diff", "--quiet", ch.Base, "HEAD", "--"}, ch.Paths...)...); err != nil {
			return "", false, ErrStale
		}
	}
	tip, tipErr := config.Load(w.Dir) // Load returns what it could read even when some files are invalid
	if ch.Allow != nil {
		if err := ch.Allow(tip); err != nil {
			if tipErr != nil {
				return "", false, repoBroken(tipErr.Error())
			}
			return "", false, err
		}
	}
	if err := ch.Edit(w.Dir); err != nil {
		return "", false, err
	}
	if _, err := config.Load(w.Dir); err != nil {
		return "", false, invalid(err, tipErr)
	}
	if _, err := git(ctx, w.Dir, "add", "-A"); err != nil {
		return "", false, err
	}
	if _, err := git(ctx, w.Dir, "diff", "--cached", "--quiet"); err == nil {
		sha, err := git(ctx, w.Dir, "rev-parse", "HEAD") // nothing changed
		return sha, false, err
	}
	actor := strings.ToLower(ch.Actor)
	if _, err := git(ctx, w.Dir, "-c", "user.name="+w.Name, "-c", "user.email="+w.Email, "commit", "-q",
		"-m", "crucible: "+ch.Action+" by "+actor, "-m", "Crucible-Actor: "+actor); err != nil {
		return "", false, err
	}
	if _, err := git(ctx, w.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+w.Branch); err != nil {
		if msg := err.Error(); strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]") {
			return "", false, errRaced
		}
		return "", false, err
	}
	sha, err := git(ctx, w.Dir, "rev-parse", "HEAD")
	return sha, err == nil, err
}

// invalid explains a config.Load failure after the edit: an error that was not there before is the user's change;
// otherwise the repo was already broken by a file this change did not touch.
func invalid(after, before error) error {
	old := map[string]bool{}
	if before != nil {
		for _, l := range strings.Split(before.Error(), "\n") {
			old[l] = true
		}
	}
	lines := strings.Split(after.Error(), "\n")
	for _, l := range lines {
		if !old[l] {
			return apperr.Wrap(apperr.Invalid, "this change would make the platform config invalid: "+l)
		}
	}
	return repoBroken(lines[0])
}

// repoBroken names the file of a config error ("teams/forge: programs/x.yaml: …" → teams/forge/programs/x.yaml).
func repoBroken(msg string) error {
	first, _, _ := strings.Cut(msg, "\n")
	var file []string
	for _, part := range strings.Split(first, ": ") {
		if !strings.Contains(part, "/") && !strings.HasSuffix(part, ".yaml") {
			break
		}
		file = append(file, part)
	}
	name := strings.Join(file, "/")
	if name == "" {
		name = "the config"
	}
	return apperr.Wrap(apperr.Invalid, "the platform repo currently has errors in "+name+" — fix it in git (see Forge Status) before saving")
}

// reset makes Dir a clean checkout of the remote branch tip. A broken working copy is removed and cloned again once.
func (w *Writer) reset(ctx context.Context) error {
	err := w.checkout(ctx)
	if err != nil && ctx.Err() == nil {
		_ = os.RemoveAll(w.Dir)
		err = w.checkout(ctx)
	}
	return err
}

func (w *Writer) checkout(ctx context.Context) error {
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
