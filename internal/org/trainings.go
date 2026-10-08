package org

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/gitsync"
)

// remoteURL is the allowlist of network remotes; anything else (file:, bare paths, ext::, C:\, a leading "-") is local.
var remoteURL = regexp.MustCompile(`(?i)^((https?|ssh|git)://[^\s-]\S*|[A-Za-z0-9_][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9.-]*:\S*)$`)

var scpUser = regexp.MustCompile(`^[^@/:\s]+@`)

var userinfo = regexp.MustCompile(`(://)([^/?#\s]*)@`)

// redactRepo masks userinfo in a repo URL for display and the audit log: "git@" is kept (the ssh username, no
// secret), anything else becomes "***@" so a reader sees credentials were present. A username can be the token
// (https://ghp_...@host). The stored value keeps them so cloning works.
func redactRepo(repo string) string {
	if !strings.Contains(repo, "://") { // scp-style user@host:path
		return scpUser.ReplaceAllStringFunc(repo, func(m string) string {
			if m == "git@" {
				return m
			}
			return "***@"
		})
	}
	return userinfo.ReplaceAllStringFunc(repo, func(m string) string {
		if strings.HasSuffix(m, "://git@") {
			return m
		}
		return userinfo.ReplaceAllString(m, "$1***@")
	})
}

var errNoChange = errors.New("no change")

// AddTraining registers a training, or repoints a registered one; an empty branch means main. Registering the
// same repo and branch again changes nothing and writes no audit row. A repoint records the previous repo and branch.
func (s *Store) AddTraining(ctx context.Context, actor, id, repo, branch string) error {
	repo, branch = strings.TrimSpace(repo), strings.TrimSpace(branch)
	if !config.ValidTrainingID(id) {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("invalid training id %q", id))
	}
	if repo == "" {
		return apperr.Wrap(apperr.Invalid, "a training needs a repo URL")
	}
	if branch == "" {
		branch = "main"
	}
	if err := checkRepo(repo); err != nil {
		return err
	}
	detail := map[string]any{"repo": redactRepo(repo), "branch": branch, "previous_repo": nil, "previous_branch": nil}
	err := s.inTx(ctx, actor, "training.add", id, detail, func(tx pgx.Tx) error {
		var pr, pb string
		err := tx.QueryRow(ctx, `SELECT repo, branch FROM trainings WHERE id = $1 FOR UPDATE`, id).Scan(&pr, &pb)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(ctx, `INSERT INTO trainings (id, repo, branch) VALUES ($1, $2, $3)`, id, repo, branch)
			return err
		case err != nil:
			return err
		case pr == repo && pb == branch:
			return errNoChange
		}
		detail["previous_repo"], detail["previous_branch"] = redactRepo(pr), pb // audit.Log runs after fn
		if pr != repo {                                                         // pinned SHAs belong to the old repository; make the move visible
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM programs WHERE training = $1`, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				detail["programs_affected"] = n
			}
		}
		_, err = tx.Exec(ctx, `UPDATE trainings SET repo = $2, branch = $3 WHERE id = $1`, id, repo, branch)
		return err
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// checkRepo refuses a repo URL git could read as an option or a local path (unless this instance allows file remotes).
func checkRepo(repo string) error {
	if strings.IndexFunc(repo, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return apperr.Wrap(apperr.Invalid, "repo URL cannot contain whitespace or control characters")
	}
	if strings.HasPrefix(repo, "-") { // before the env bypass: no setting lets an option through as a path
		return apperr.Wrap(apperr.Invalid, "repo URL cannot start with '-'")
	}
	if !remoteURL.MatchString(repo) && !gitsync.AllowFileFromEnv(os.Getenv) {
		return apperr.Wrap(apperr.Invalid, "repo must be an https://, http://, ssh://, git:// or user@host:path URL; local paths are not allowed on this instance")
	}
	return nil
}

// RemoveTraining deletes Crucible's connection to a training's repository (the repository is untouched) and stops the
// training for every team that runs it, in one step. Progress, scores and badges stay: they are keyed by name, not by
// these rows, and come back if the training is registered again and started.
func (s *Store) RemoveTraining(ctx context.Context, actor, id string) error {
	detail := map[string]any{}
	return s.inTx(ctx, actor, "training.remove", id, detail, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM programs WHERE training = $1 RETURNING team`, id) // roles and enrollments cascade
		if err != nil {
			return err
		}
		stopped, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		slices.Sort(stopped)
		detail["stopped_for"] = nonNil(stopped)
		var repo, branch string
		err = tx.QueryRow(ctx, `DELETE FROM trainings WHERE id = $1 RETURNING repo, branch`, id).Scan(&repo, &branch)
		var pg *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no training %q", id))
		case errors.As(err, &pg) && pg.Code == "23503": // a team started it while this ran
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("a team started %q just now; try again", id))
		case err != nil:
			return err
		}
		detail["repo"], detail["branch"] = redactRepo(repo), branch
		return nil
	})
}
