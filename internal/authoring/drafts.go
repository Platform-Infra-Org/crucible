package authoring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

const (
	maxDrafts = 5 // open drafts per author
	draftCols = `d.id, d.training, d.title, d.base_sha, d.ops, d.updated_at, d.submitted_edit_id, coalesce(e.status, '')`
	draftFrom = `content_drafts d LEFT JOIN content_edits e ON e.id = d.submitted_edit_id`
	// bump keeps updated_at strictly increasing, so compare-and-set never sees two saves with one stamp.
	bump = `GREATEST(now(), updated_at + interval '1 microsecond')`
)

type Draft struct {
	ID        int64        `json:"id"`
	Training  string       `json:"training"`
	Title     string       `json:"title"`
	BaseSHA   string       `json:"base_sha"`
	HeadSHA   string       `json:"head_sha"` // the tracked branch now: differs from BaseSHA when a rebase is due
	Ops       []gitsync.Op `json:"ops"`
	UpdatedAt time.Time    `json:"updated_at"`
	EditID    *int64       `json:"edit_id,omitempty"`
	State     string       `json:"state"` // editing | in_review | returned | merged
}

type NewDraft struct {
	Training string `json:"training"`
	Title    string `json:"title"`
	FromEdit int64  `json:"from_edit,omitempty"` // reopen one of my stale, rejected or withdrawn edits, at its base
}

type SaveDraft struct {
	Title     string       `json:"title"`
	BaseSHA   string       `json:"base_sha"`
	Ops       []gitsync.Op `json:"ops"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type Conflict struct {
	Path        string     `json:"path"`
	Base        string     `json:"base"`         // the file when the draft started ("" if it didn't exist)
	Head        string     `json:"head"`         // the file now ("" if gone)
	HeadMissing bool       `json:"head_missing"` // deleted or moved away upstream
	Mine        string     `json:"mine"`         // the draft's text for a put
	Op          gitsync.Op `json:"op"`           // the draft's op that touches Path
}

type RebaseResult struct {
	Draft     *Draft     `json:"draft"`
	Conflicts []Conflict `json:"conflicts"`
}

func title(s string) (string, error) {
	t := strings.TrimSpace(edits.Clean(s))
	if len(t) > edits.MaxTitle {
		return "", apperr.Wrap(apperr.Invalid, "give the draft a title of at most 200 characters")
	}
	return t, nil
}

func stateOf(editStatus string) string {
	switch editStatus {
	case "":
		return "editing"
	case "pending":
		return "in_review"
	case "merged":
		return "merged"
	}
	return "returned" // rejected, withdrawn, stale
}

func scanDraft(row pgx.Row) (*Draft, error) {
	var d Draft
	var status string
	err := row.Scan(&d.ID, &d.Training, &d.Title, &d.BaseSHA, &d.Ops, &d.UpdatedAt, &d.EditID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "draft not found")
	}
	if err != nil {
		return nil, err
	}
	d.State = stateOf(status)
	return &d, nil
}

// List is my drafts, newest first, for trainings I may still edit. Drafts whose edit merged are done: deleted here.
func (s *Service) List(ctx context.Context, u *auth.User) ([]Draft, error) {
	me := strings.ToLower(u.Email)
	if _, err := s.DB.Exec(ctx, `DELETE FROM content_drafts d USING content_edits e
		WHERE d.submitted_edit_id = e.id AND e.status = 'merged' AND d.author = $1`, me); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+draftCols+` FROM `+draftFrom+` WHERE d.author = $1 ORDER BY d.updated_at DESC`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		if _, head, err := s.Edits.Authorize(u, d.Training); err == nil {
			d.HeadSHA, d.Ops = head, nil
			out = append(out, *d)
		}
	}
	return out, rows.Err()
}

// Get is one of my drafts, while I may still edit its training.
func (s *Service) Get(ctx context.Context, u *auth.User, id int64) (*Draft, error) {
	d, err := scanDraft(s.DB.QueryRow(ctx, `SELECT `+draftCols+` FROM `+draftFrom+` WHERE d.id = $1 AND d.author = $2`, id, strings.ToLower(u.Email)))
	if err != nil {
		return nil, err
	}
	_, head, err := s.Edits.Authorize(u, d.Training)
	if err != nil {
		return nil, err
	}
	d.HeadSHA = head
	return d, nil
}

func (s *Service) Create(ctx context.Context, u *auth.User, in NewDraft) (*Draft, error) {
	_, head, err := s.Edits.Authorize(u, in.Training)
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	name, base, ops := in.Title, head, []gitsync.Op{}
	if in.FromEdit != 0 {
		e, err := s.Edits.Get(ctx, u, in.FromEdit)
		if err != nil {
			return nil, err
		}
		if e.Author != me || e.Training != in.Training || e.Status == "pending" || e.Status == "merged" {
			return nil, apperr.Wrap(apperr.Invalid, "only your own stale, rejected or withdrawn edits of this training can be reopened")
		}
		name, base, ops = e.Title, e.BaseSHA, e.Ops
	}
	if name, err = title(name); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('content_drafts:' || $1))`, me); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT d.training FROM `+draftFrom+` WHERE d.author = $1 AND coalesce(e.status, '') <> 'merged'`, me)
	if err != nil {
		return nil, err
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	n := 0
	for _, tr := range open { // only drafts I can still see (List's filter): hidden ones can't be discarded
		if _, _, err := s.Edits.Authorize(u, tr); err == nil {
			n++
		}
	}
	if n >= maxDrafts {
		return nil, apperr.Wrap(apperr.Conflict, fmt.Sprintf("you have %d open drafts; submit or discard one first", n))
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO content_drafts (author, training, title, base_sha, ops) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		me, in.Training, name, base, ops).Scan(&id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, u, id)
}

// Save is autosave: it replaces the draft's title, base and ops if updated_at still matches (another tab didn't save
// in between). The base may stay or move to the head (after a rebase), nothing else. Saving a returned draft unlinks it.
func (s *Service) Save(ctx context.Context, u *auth.User, id int64, in SaveDraft) (*Draft, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.State == "in_review":
		return nil, apperr.Wrap(apperr.Conflict, "this draft is in review; withdraw the edit to keep working on it")
	case d.State == "merged":
		return nil, apperr.Wrap(apperr.Conflict, "this draft was merged")
	case in.BaseSHA != d.BaseSHA && in.BaseSHA != d.HeadSHA:
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it")
	}
	name, err := title(in.Title)
	if err != nil {
		return nil, err
	}
	if in.Ops == nil {
		in.Ops = []gitsync.Op{}
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE content_drafts SET title = $3, base_sha = $4, ops = $5, updated_at = `+bump+`, submitted_edit_id = NULL
		WHERE id = $1 AND author = $2 AND updated_at = $6`, id, strings.ToLower(u.Email), name, in.BaseSHA, in.Ops, in.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed in another tab or window; reload it")
	}
	return s.Get(ctx, u, id)
}

func (s *Service) Discard(ctx context.Context, u *auth.User, id int64) error {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return err
	}
	me := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM content_drafts WHERE id = $1 AND author = $2`, id, me); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, me, "content_draft.discard", d.Training, map[string]any{"draft": id, "title": d.Title}, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Submit turns the draft into an edit through the normal path (validation, its own branch, review) and links them.
// at is the updated_at the client last saw: like Save, it is compare-and-set, so a stale tab, a double click or an
// autosave racing the submit fails with Conflict and the edit it created is withdrawn again.
func (s *Service) Submit(ctx context.Context, u *auth.User, id int64, at time.Time) (*edits.Edit, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.State == "in_review" || d.State == "merged":
		return nil, apperr.Wrap(apperr.Conflict, "this draft was already submitted")
	case !d.UpdatedAt.Equal(at):
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed in another tab or window; reload it")
	case d.BaseSHA != d.HeadSHA:
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it, then submit")
	case len(d.Ops) == 0:
		return nil, apperr.Wrap(apperr.Invalid, "nothing changed")
	}
	e, err := s.Edits.Create(ctx, u, edits.NewEdit{Training: d.Training, BaseSHA: d.BaseSHA, Title: d.Title, Ops: d.Ops})
	if err != nil {
		return nil, err
	}
	linked := false
	defer func() {
		if !linked { // the edit holds ops the draft no longer has, or a second copy: take it back
			if _, err := s.Edits.Withdraw(context.WithoutCancel(ctx), u, e.ID); err != nil && s.Log != nil {
				s.Log.Error("withdrawing an unlinked draft edit failed", "edit", e.ID, "err", err)
			}
		}
	}()
	me := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	tag, err := tx.Exec(ctx, `UPDATE content_drafts SET submitted_edit_id = $3, updated_at = `+bump+` WHERE id = $1 AND author = $2 AND updated_at = $4`,
		id, me, e.ID, d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed while it was being submitted; reload it")
	}
	if err := audit.Log(ctx, tx, me, "content_draft.submit", d.Training, map[string]any{"draft": id, "edit": e.ID}, e.HeadSHA); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	linked = true
	return e, nil
}

// Rebase moves a draft to the training's current head. Files the draft doesn't touch simply follow. A file the draft
// changes (put, rename source or target, delete) that also changed upstream is a conflict: nothing is written, and the
// editor saves the author's resolution with base_sha = head.
func (s *Service) Rebase(ctx context.Context, u *auth.User, id int64) (*RebaseResult, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if d.BaseSHA == d.HeadSHA {
		return &RebaseResult{Draft: d, Conflicts: []Conflict{}}, nil
	}
	if d.State == "in_review" {
		return nil, apperr.Wrap(apperr.Conflict, "this draft is in review; withdraw the edit to keep working on it")
	}
	old, _, err := s.Edits.At(ctx, u, d.Training, d.BaseSHA)
	if err != nil {
		return nil, err
	}
	now, _, err := s.Edits.At(ctx, u, d.Training, d.HeadSHA)
	if err != nil {
		return nil, err
	}
	read := func(t *content.Training, rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel))) // rel passed CheckOps when it was saved
		return string(b), err == nil
	}
	conflicts := []Conflict{}
	for _, op := range d.Ops {
		p := op.Path
		if op.Op == "rename" {
			p = op.From
			if body, exists := read(now, op.To); exists { // the target appeared upstream
				conflicts = append(conflicts, Conflict{Path: op.To, Head: body, Op: op})
			}
		}
		was, had := read(old, p)
		is, has := read(now, p)
		if had == has && was == is {
			continue
		}
		c := Conflict{Path: p, Base: was, Head: is, HeadMissing: !has, Op: op}
		if op.Op == "put" {
			c.Mine = op.Content
		}
		conflicts = append(conflicts, c)
	}
	if len(conflicts) > 0 {
		return &RebaseResult{Draft: d, Conflicts: conflicts}, nil
	}
	tag, err := s.DB.Exec(ctx, `UPDATE content_drafts SET base_sha = $3, updated_at = `+bump+` WHERE id = $1 AND author = $2 AND updated_at = $4`,
		id, strings.ToLower(u.Email), d.HeadSHA, d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed in another tab or window; reload it")
	}
	d, err = s.Get(ctx, u, id)
	return &RebaseResult{Draft: d, Conflicts: []Conflict{}}, err
}

// Files lists the training at the draft's base, marking what may be edited.
func (s *Service) Files(ctx context.Context, u *auth.User, id int64) ([]edits.FileInfo, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	t, _, err := s.Edits.At(ctx, u, d.Training, d.BaseSHA)
	if err != nil {
		return nil, err
	}
	return edits.ListFiles(t.Dir)
}

// File is one editable file at the draft's base (a sha this draft stored, so edits may trust it).
func (s *Service) File(ctx context.Context, u *auth.User, id int64, rel string) (string, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return "", err
	}
	return s.Edits.File(ctx, u, d.Training, d.BaseSHA, rel)
}
