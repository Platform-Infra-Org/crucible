// Package edits lets maintainers and leads change training content from the UI (spec §6): every edit is pushed to its
// own branch, reviewed in Crucible by a maintainer other than its author, and merged by the bot. Plain git, any host.
package edits

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type Service struct {
	DB     *pgxpool.Pool
	State  func() *gitsync.State
	Repo   func(training string) *gitsync.ContentRepo // the bot's clone of a training's repo, one value per repo; nil when unknown
	Notify Notifier
	Resync func(ctx context.Context) error // re-read git after a merge so trainees see it at once
	Log    *slog.Logger

	locks sync.Map // training id → chan struct{}: one decision at a time per training
}

type Edit struct {
	ID          int64             `json:"id"`
	Training    string            `json:"training"`
	Title       string            `json:"title"`
	Author      string            `json:"author"`
	BaseSHA     string            `json:"base_sha"`
	HeadSHA     string            `json:"head_sha"`
	Branch      string            `json:"branch"`
	Status      string            `json:"status"`
	Reviewer    string            `json:"reviewer,omitempty"`
	Note        string            `json:"note,omitempty"`
	MergeSHA    string            `json:"merge_sha,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	DecidedAt   *time.Time        `json:"decided_at,omitempty"`
	Files       map[string]string `json:"files,omitempty"` // detail only
	Diff        string            `json:"diff,omitempty"`  // detail only: git's unified diff, as is
	CanReview   bool              `json:"can_review"`
	CanWithdraw bool              `json:"can_withdraw"`
}

type NewEdit struct {
	Training string            `json:"training"`
	BaseSHA  string            `json:"base_sha"`
	Title    string            `json:"title"`
	Files    map[string]string `json:"files"`
}

type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type TrainingRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

const (
	maxTitle   = 200
	maxNote    = 2000
	maxOpen    = 5  // pending edits per author
	maxPerHour = 10 // edits proposed per author per hour
	cols       = `id, training, title, author, base_sha, head_sha, branch, status, reviewer, note, merge_sha, created_at, decided_at, files, diff`
)

// gitTimeout bounds every git section (fetch, merge, push) so a hanging git host fails the request instead of piling up.
var gitTimeout = 2 * time.Minute

func clean(s string) string { return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "") }

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// head is the training version at its tracked branch head: edits are based on it, and its maintainers review them.
func head(st *gitsync.State, training string) (*content.Training, string) {
	sha := st.Heads[training]
	return st.Training(training, sha), sha
}

// enrolled: anyone enrolled in the training (any team) could read its answer keys through this API, so they get none.
func enrolled(p *config.Platform, training, email string) bool {
	return rbac.Checker{P: p}.Enrolled(email, training)
}

func maintainer(t *content.Training, email string) bool {
	return t != nil && slices.ContainsFunc(t.Maintainers, func(m string) bool { return strings.EqualFold(m, email) })
}

// canPropose: admins, the training's maintainers, and leaders and seniors of teams with a program for the training;
// never anyone enrolled in it. Proposing means reading the raw files (answer keys, check scripts).
func canPropose(p *config.Platform, t *content.Training, email string) bool {
	email = strings.ToLower(email)
	if enrolled(p, t.ID, email) {
		return false
	}
	if (rbac.Checker{P: p}).IsAdmin(email) || maintainer(t, email) {
		return true
	}
	for _, team := range p.Teams {
		if r := team.RoleOf(email); team.Programs[t.ID] != nil && (r == "leader" || r == "senior") {
			return true
		}
	}
	return false
}

// canReview: admins and the head version's maintainers (spec §5.3), never the author, never someone enrolled. t is nil
// when the training has no valid head (or left the platform): then only admins can still decide its edits.
func canReview(p *config.Platform, training string, t *content.Training, email, author string) bool {
	email = strings.ToLower(email)
	return email != author && !enrolled(p, training, email) && ((rbac.Checker{P: p}).IsAdmin(email) || maintainer(t, email))
}

// checkPath allows exactly what gitsync lets an edit touch (training.yaml and modules/<id>/…, text extensions).
func checkPath(rel string) error { return gitsync.CheckEditFiles(map[string]string{rel: ""}) }

func (s *Service) training(u *auth.User, id string) (*gitsync.State, *content.Training, string, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, "", err
	}
	if _, ok := st.Platform.Trainings[id]; !ok {
		return nil, nil, "", apperr.Wrap(apperr.NotFound, "training not found")
	}
	t, sha := head(st, id)
	if t == nil {
		return nil, nil, "", apperr.Wrap(apperr.Unavailable, "this training has no valid content at its branch head; fix it in git first")
	}
	if !canPropose(st.Platform, t, u.Email) {
		return nil, nil, "", apperr.Wrap(apperr.Forbidden, "you can't edit this training")
	}
	return st, t, sha, nil
}

// Trainings lists the trainings the user may propose edits to.
func (s *Service) Trainings(u *auth.User) []TrainingRef {
	out := []TrainingRef{}
	st, err := s.state()
	if err != nil {
		return out
	}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		if t, _ := head(st, id); t != nil && canPropose(st.Platform, t, u.Email) {
			out = append(out, TrainingRef{ID: id, Title: t.Title})
		}
	}
	return out
}

// CanUse reports whether the user may propose or review any edit (the nav link).
func (s *Service) CanUse(email string) bool {
	st, err := s.state()
	if err != nil {
		return false
	}
	for id := range st.Platform.Trainings {
		if t, _ := head(st, id); t != nil && (canPropose(st.Platform, t, email) || canReview(st.Platform, id, t, email, "")) {
			return true
		}
	}
	return false
}

// Files lists the editable files of the training at its branch head, and that head (the base of a new edit).
func (s *Service) Files(u *auth.User, training string) (string, []FileInfo, error) {
	_, t, sha, err := s.training(u, training)
	if err != nil {
		return "", nil, err
	}
	out := []FileInfo{}
	err = filepath.WalkDir(t.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != t.Dir {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(t.Dir, p)
		rel = filepath.ToSlash(rel)
		if d.Type().IsRegular() && checkPath(rel) == nil {
			if fi, err := d.Info(); err == nil {
				out = append(out, FileInfo{Path: rel, Size: fi.Size()})
			}
		}
		return nil
	})
	return sha, out, err
}

func (s *Service) File(u *auth.User, training, rel string) (string, error) {
	if err := checkPath(rel); err != nil {
		return "", err
	}
	_, t, _, err := s.training(u, training)
	if err != nil {
		return "", err
	}
	if err := gitsync.NoSymlinks(t.Dir, rel); err != nil { // a link could point outside the content
		return "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	b, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	return string(b), nil
}

// Authorize returns the training's head version and sha when u may propose edits to it: never someone enrolled in it.
func (s *Service) Authorize(u *auth.User, training string) (*content.Training, string, error) {
	_, t, sha, err := s.training(u, training)
	return t, sha, err
}

// Workspace copies t into a temp dir and applies the ops that change something (a put equal to the current file is
// dropped). Ops must have passed gitsync.CheckOps. The caller loads the copy, then calls cleanup. New .sh files are
// made executable there because lint wants lab scripts executable; ContentRepo.PushEdit sets the modes git records.
func Workspace(t *content.Training, ops []gitsync.Op) (string, []gitsync.Op, func(), error) {
	var changed []gitsync.Op
	for _, op := range ops {
		if op.Op == "put" {
			if old, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(op.Path))); err == nil && string(old) == op.Content {
				continue
			}
		}
		changed = append(changed, op)
	}
	tmp, err := os.MkdirTemp("", "crucible-edit-*")
	if err != nil {
		return "", nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := os.CopyFS(tmp, os.DirFS(t.Dir)); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("copying the training to check the edit: %w", err)
	}
	created, err := gitsync.ApplyOps(tmp, changed)
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	for _, rel := range created {
		if strings.HasSuffix(rel, ".sh") {
			_ = os.Chmod(filepath.Join(tmp, filepath.FromSlash(rel)), 0o755)
		}
	}
	return tmp, changed, cleanup, nil
}

// Check applies ops to a copy of t and loads it: the ops that change something, and the problems the training would
// then have. err is for ops that are refused or can't apply, and for an edit that changes nothing.
func Check(t *content.Training, ops []gitsync.Op) ([]gitsync.Op, []content.Problem, error) {
	if err := gitsync.CheckOps(ops); err != nil {
		return nil, nil, err
	}
	dir, changed, cleanup, err := Workspace(t, ops)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	if len(changed) == 0 {
		return nil, nil, apperr.Wrap(apperr.Invalid, "nothing changed")
	}
	nt, probs := content.Load(dir)
	if len(probs) == 0 && nt.ID != t.ID {
		probs = []content.Problem{{File: "training.yaml", Msg: "the training id must stay " + t.ID}}
	}
	return changed, probs, nil
}

func broken(probs []content.Problem) error {
	msgs := []string{}
	for _, p := range probs[:min(len(probs), 10)] {
		msgs = append(msgs, p.String())
	}
	return apperr.Wrap(apperr.Invalid, "this edit would break the training: "+strings.Join(msgs, "; "))
}

// limits are the per-author volume limits. Create checks them cheaply first, then again, authoritatively, in the
// transaction that records the edit.
func limits(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, me string) error {
	var open, lastHour int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'pending'), count(*) FILTER (WHERE created_at > now() - interval '1 hour')
		FROM content_edits WHERE author = $1`, me).Scan(&open, &lastHour); err != nil {
		return err
	}
	switch {
	case open >= maxOpen:
		return apperr.Wrap(apperr.Conflict, fmt.Sprintf("you have %d open edits; wait for a review or withdraw one first", open))
	case lastHour >= maxPerHour:
		return apperr.Wrap(apperr.Conflict, fmt.Sprintf("you proposed %d edits in the last hour; try again later", lastHour))
	}
	return nil
}

// Create pushes the edit to its branch first and records it after, in a short transaction: no DB connection is held
// while git talks to the remote. The branch is deleted again if anything fails once the push has started.
func (s *Service) Create(ctx context.Context, u *auth.User, in NewEdit) (*Edit, error) {
	st, t, sha, err := s.training(u, in.Training)
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	if in.BaseSHA != sha {
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since you opened it; reload and redo your change")
	}
	title := strings.TrimSpace(clean(in.Title))
	if title == "" || len(title) > maxTitle {
		return nil, apperr.Wrap(apperr.Invalid, "give the edit a title of at most 200 characters")
	}
	repo := s.Repo(in.Training)
	if repo == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "this training's repo is not available for edits")
	}
	if err := limits(ctx, s.DB, me); err != nil { // before the copy validate makes
		return nil, err
	}
	changedOps, probs, err := Check(t, gitsync.PutOps(in.Files))
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		return nil, broken(probs)
	}
	changed := map[string]string{} // ponytail: map until Task 7 stores ops
	for _, op := range changedOps {
		changed[op.Path] = op.Content
	}
	var id int64
	if err := s.DB.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('content_edits', 'id'))`).Scan(&id); err != nil {
		return nil, err
	}
	branch := fmt.Sprintf("crucible/edit/%d", id)
	committed := false
	defer func() {
		if !committed { // nothing recorded, so no branch either (a no-op when the push never landed)
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitTimeout)
			defer cancel()
			if err := repo.DeleteBranch(dctx, branch); err != nil && s.Log != nil {
				s.Log.Warn("deleting an unrecorded edit branch failed", "branch", branch, "err", err)
			}
		}
	}()
	gctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	headSHA, diff, err := repo.PushEdit(gctx, branch, sha, changed, me, "crucible: "+title)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	// Serialized per author so parallel requests can't slip past the limits.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('content_edits:' || $1))`, me); err != nil {
		return nil, err
	}
	if err := limits(ctx, tx, me); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO content_edits (id, training, title, author, base_sha, files, status, branch, head_sha, diff)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9)`, id, in.Training, title, me, sha, changed, branch, headSHA, diff); err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, me, "content_edit.propose", in.Training, map[string]any{"edit": id, "title": title, "files": slices.Sorted(maps.Keys(changed))}, headSHA); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	var to []string
	for _, e := range slices.Concat(t.Maintainers, st.Platform.Admins) {
		e = strings.ToLower(e)
		if canReview(st.Platform, in.Training, t, e, me) && !slices.Contains(to, e) {
			to = append(to, e)
		}
	}
	s.notify(ctx, notify.Event{Kind: notify.ContentEdit, To: to, Subject: "Content edit waiting for review: " + title,
		Text: fmt.Sprintf("%s proposed a change to %s.", me, t.Title), Link: fmt.Sprintf("/edits/%d", id)})
	return s.Get(ctx, u, id)
}

func (s *Service) notify(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || len(ev.To) == 0 {
		return
	}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil && s.Log != nil {
		s.Log.Error("queueing a content-edit notification failed", "err", err)
	}
}

func scan(row pgx.Row) (*Edit, error) {
	var e Edit
	err := row.Scan(&e.ID, &e.Training, &e.Title, &e.Author, &e.BaseSHA, &e.HeadSHA, &e.Branch, &e.Status, &e.Reviewer, &e.Note,
		&e.MergeSHA, &e.CreatedAt, &e.DecidedAt, &e.Files, &e.Diff)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "edit not found")
	}
	return &e, err
}

// visible: the author and anyone who may review it; nobody enrolled in the training. Sets CanReview / CanWithdraw.
func visible(st *gitsync.State, u *auth.User, e *Edit) bool {
	me := strings.ToLower(u.Email)
	if enrolled(st.Platform, e.Training, me) {
		return false
	}
	t, _ := head(st, e.Training)
	e.CanReview = e.Status == "pending" && canReview(st.Platform, e.Training, t, me, e.Author)
	e.CanWithdraw = e.Status == "pending" && e.Author == me
	return e.Author == me || canReview(st.Platform, e.Training, t, me, "")
}

// List shows the newest edits the user may see, pending first, without files and diffs.
func (s *Service) List(ctx context.Context, u *auth.User) ([]Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	// ponytail: filters after a 200-row window; an index on visibility if the history gets long.
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM content_edits ORDER BY (status = 'pending') DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Edit{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if visible(st, u, e) {
			e.Files, e.Diff = nil, ""
			out = append(out, *e)
		}
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, u *auth.User, id int64) (*Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	e, err := scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM content_edits WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	if !visible(st, u, e) {
		return nil, apperr.Wrap(apperr.NotFound, "edit not found")
	}
	return e, nil
}

func (s *Service) Approve(ctx context.Context, u *auth.User, id int64, note string) (*Edit, error) {
	return s.decide(ctx, u, id, "merged", note)
}

func (s *Service) Reject(ctx context.Context, u *auth.User, id int64, note string) (*Edit, error) {
	return s.decide(ctx, u, id, "rejected", note)
}

func (s *Service) Withdraw(ctx context.Context, u *auth.User, id int64) (*Edit, error) {
	return s.decide(ctx, u, id, "withdrawn", "")
}

// lock serializes decisions per training. It is taken before any transaction, so waiting on it (or on git under it)
// never holds a DB connection.
func (s *Service) lock(ctx context.Context, training string) (func(), error) {
	v, _ := s.locks.LoadOrStore(training, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// mayDecide checks the edit is visible to u and that u may move it to `to`.
func mayDecide(st *gitsync.State, u *auth.User, e *Edit, to string) error {
	if !visible(st, u, e) {
		return apperr.Wrap(apperr.NotFound, "edit not found")
	}
	switch {
	case to == "withdrawn" && e.Author != strings.ToLower(u.Email):
		return apperr.Wrap(apperr.Forbidden, "only the author can withdraw an edit")
	case to != "withdrawn" && !e.CanReview && e.Status == "pending":
		return apperr.Wrap(apperr.Forbidden, "only the training's maintainers or an admin, other than the author, can review this edit")
	case e.Status != "pending":
		return apperr.Wrap(apperr.Conflict, "this edit was already decided")
	}
	return nil
}

// decide moves a pending edit to merged, rejected or withdrawn. Decisions on a training's edits run one at a time
// (s.lock), so two approvers can never both merge: the second waits, then finds the edit decided. The git work runs
// before the short transaction that records it, bounded by gitTimeout and detached from the request: once a merge is
// pushed it is recorded, audited and announced even if the reviewer went away. Only the commit that was reviewed
// (head_sha) is merged. A merge that conflicts, or would be invalid on the current content, marks the edit stale; an
// edit branch someone pushed to since is restored to the reviewed change and needs a new approval; any other failure
// leaves the edit pending, branch and all, so it can be approved again.
func (s *Service) decide(ctx context.Context, u *auth.User, id int64, to, note string) (*Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	get := func(ctx context.Context) (*Edit, error) {
		return scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM content_edits WHERE id = $1`, id))
	}
	e, err := get(ctx)
	if err != nil {
		return nil, err
	}
	if err := mayDecide(st, u, e, to); err != nil {
		return nil, err
	}
	unlock, err := s.lock(ctx, e.Training)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if e, err = get(ctx); err != nil { // again: another decision may have landed while we waited
		return nil, err
	}
	if err := mayDecide(st, u, e, to); err != nil {
		return nil, err
	}
	note = strings.TrimSpace(clean(note))
	if len(note) > maxNote {
		note = strings.ToValidUTF8(note[:maxNote], "")
	}
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitTimeout)
	defer cancel()
	status, mergeSHA, gitErr, repo := to, "", "", s.Repo(e.Training)
	if to == "merged" {
		if repo == nil {
			return nil, apperr.Wrap(apperr.Unavailable, "this training's repo is not available for edits")
		}
		mergeSHA, err = repo.Merge(gctx, e.Branch, e.HeadSHA, fmt.Sprintf("crucible: merge edit %d %q by %s", e.ID, e.Title, e.Author), me)
		switch {
		case errors.Is(err, gitsync.ErrEditMoved):
			return nil, s.restore(gctx, repo, e, me)
		case errors.Is(err, gitsync.ErrMergeConflict), errors.Is(err, gitsync.ErrMergeInvalid):
			status, gitErr, mergeSHA = "stale", err.Error(), ""
		case err != nil:
			return nil, err // transient: still pending, branch kept
		}
	}
	detail := map[string]any{"edit": e.ID, "title": e.Title, "author": e.Author, "note": note, "as": "author"}
	if to != "withdrawn" {
		detail["as"] = "maintainer"
		if t, _ := head(st, e.Training); !maintainer(t, me) {
			detail["as"] = "admin" // an override: admins review any training
		}
	}
	if gitErr != "" {
		detail["error"] = gitErr
	}
	tx, err := s.DB.Begin(gctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(gctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(gctx, `UPDATE content_edits SET status = $2, reviewer = $3, note = $4, merge_sha = $5, decided_at = now() WHERE id = $1`,
		e.ID, status, me, note, mergeSHA); err != nil {
		return nil, err
	}
	if err := audit.Log(gctx, tx, me, "content_edit."+status, e.Training, detail, mergeSHA); err != nil {
		return nil, err
	}
	if err := tx.Commit(gctx); err != nil {
		return nil, err
	}
	if repo != nil {
		if err := repo.DeleteBranch(gctx, e.Branch); err != nil && s.Log != nil {
			s.Log.Warn("deleting a decided edit branch failed", "branch", e.Branch, "err", err)
		}
	}
	if status == "merged" && s.Resync != nil {
		if err := s.Resync(gctx); err != nil && s.Log != nil {
			s.Log.Warn("re-sync after a content merge failed; the poller will pick it up", "err", err)
		}
	}
	if me != e.Author {
		s.notify(gctx, notify.Event{Kind: notify.ContentEdit, To: []string{e.Author}, Subject: "Your content edit was " + status + ": " + e.Title,
			Text: strings.TrimSpace(fmt.Sprintf("%s marked your edit %s. %s %s", me, status, gitErr, note)), Link: fmt.Sprintf("/edits/%d", e.ID)})
	}
	if status == "stale" {
		return nil, apperr.Wrap(apperr.Conflict, "this edit no longer applies to the current content; the author can redo it on the fresh version ("+gitErr+")")
	}
	return s.Get(gctx, u, e.ID)
}

// restore re-pushes the stored (reviewed) files to an edit branch someone changed outside Crucible. The new commit and
// diff replace the old ones, so nothing merges until a reviewer approves again. Returns ErrEditMoved when it worked.
func (s *Service) restore(ctx context.Context, repo *gitsync.ContentRepo, e *Edit, me string) error {
	headSHA, diff, err := repo.PushEdit(ctx, e.Branch, e.BaseSHA, e.Files, e.Author, "crucible: "+e.Title)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `UPDATE content_edits SET head_sha = $2, diff = $3 WHERE id = $1`, e.ID, headSHA, diff); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, me, "content_edit.restore", e.Training, map[string]any{"edit": e.ID, "moved_from": e.HeadSHA}, headSHA); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return gitsync.ErrEditMoved
}
