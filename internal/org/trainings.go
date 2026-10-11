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

// AddTraining registers a training, or repoints a registered one; an empty ref means main and an empty path the repo
// root. An empty repo keeps a registered training's repo: the screen never sees its credentials, so it can't send them
// back. Registering the same repo, ref and path again changes nothing and writes no audit row. A repoint records the
// previous ones.
func (s *Store) AddTraining(ctx context.Context, actor, id string, ref config.TrainingRef) error {
	if !config.ValidTrainingID(id) {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("invalid training id %q", id))
	}
	if strings.TrimSpace(ref.Repo) == "" {
		// ponytail: read outside the write's transaction; a concurrent repoint of the same training by another admin
		// loses its repo to this one, as any last write would.
		if err := s.DB.QueryRow(ctx, `SELECT repo FROM trainings WHERE id = $1`, id).Scan(&ref.Repo); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	ref, err := cleanTrainingRef(ref)
	if err != nil {
		return err
	}
	detail := map[string]any{"repo": redactRepo(ref.Repo), "ref": ref.Ref, "path": ref.Path, "previous_repo": nil, "previous_ref": nil, "previous_path": nil}
	err = s.inTx(ctx, actor, "training.add", id, detail, func(tx pgx.Tx) error {
		var prev config.TrainingRef
		err := tx.QueryRow(ctx, `SELECT repo, ref, path FROM trainings WHERE id = $1 FOR UPDATE`, id).Scan(&prev.Repo, &prev.Ref, &prev.Path)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(ctx, `INSERT INTO trainings (id, repo, ref, path) VALUES ($1, $2, $3, $4)`, id, ref.Repo, ref.Ref, ref.Path)
			return err
		case err != nil:
			return err
		case prev == ref:
			return errNoChange
		}
		detail["previous_repo"], detail["previous_ref"], detail["previous_path"] = redactRepo(prev.Repo), prev.Ref, prev.Path // audit.Log runs after fn
		if prev.Repo != ref.Repo || prev.Path != ref.Path {                                                                   // pinned SHAs belong to the old content; make the move visible
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM programs WHERE training = $1`, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				detail["programs_affected"] = n
			}
		}
		_, err = tx.Exec(ctx, `UPDATE trainings SET repo = $2, ref = $3, path = $4 WHERE id = $1`, id, ref.Repo, ref.Ref, ref.Path)
		return err
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// cleanTrainingRef trims and checks a training's repo, ref and path, filling in the defaults.
func cleanTrainingRef(ref config.TrainingRef) (config.TrainingRef, error) {
	ref.Repo = strings.TrimSpace(ref.Repo)
	if ref.Repo == "" {
		return ref, apperr.Wrap(apperr.Invalid, "a training needs a repo URL")
	}
	if err := checkRepo(ref.Repo); err != nil {
		return ref, err
	}
	var err error
	if ref.Ref, err = config.CleanRef(ref.Ref); err != nil {
		return ref, apperr.Wrap(apperr.Invalid, err.Error())
	}
	if ref.Path, err = config.CleanPath(ref.Path); err != nil {
		return ref, apperr.Wrap(apperr.Invalid, err.Error())
	}
	return ref, nil
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
		var repo, ref, root string
		err = tx.QueryRow(ctx, `DELETE FROM trainings WHERE id = $1 RETURNING repo, ref, path`, id).Scan(&repo, &ref, &root)
		var pg *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no training %q", id))
		case errors.As(err, &pg) && pg.Code == "23503": // a team started it while this ran
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("a team started %q just now; try again", id))
		case err != nil:
			return err
		}
		detail["repo"], detail["ref"], detail["path"] = redactRepo(repo), ref, root
		return nil
	})
}
