// Package scoring is the Anvil (spec §7): human-scored submissions (text and upload answers, review tasks, live
// sign-offs), the scoring queue, feedback, return-for-rework and audited overrides. It owns the submissions table.
// learn and labs call Submit; scoring calls them back through Progress/Labs after each decision.
package scoring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime/multipart"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

const (
	KindQuestion = "question"
	KindTask     = "task"

	Pending  = "pending"
	Scored   = "scored"
	Returned = "returned"

	MaxFiles     = 5
	MaxFileBytes = 20 << 20
	maxAnswer    = 20000
	maxFeedback  = 5000
)

type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Submission struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"-"`
	Email     string     `json:"trainee"`
	Name      string     `json:"trainee_name"`
	Team      string     `json:"team"`
	Training  string     `json:"training"`
	Module    string     `json:"module"`
	SHA       string     `json:"-"`
	Kind      string     `json:"kind"`
	Item      string     `json:"item"`
	LabID     string     `json:"lab_id,omitempty"`
	QType     string     `json:"type"`
	Prompt    string     `json:"prompt"`
	Rubric    string     `json:"-"` // never serialized: scorers get ScorerView(), trainees Feedback()
	MaxPoints float64    `json:"max_points"`
	Answer    string     `json:"answer"`
	Files     []File     `json:"files"`
	Keys      []string   `json:"-"`
	Status    string     `json:"status"`
	Points    float64    `json:"points"`
	Note      string     `json:"feedback"` // the scorer's written feedback
	ScoredBy  string     `json:"scored_by,omitempty"`
	ScoredAt  *time.Time `json:"scored_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Feedback is what trainees see of their own submission. It deliberately has no rubric (an answer key).
type Feedback struct {
	ID       int64   `json:"id"`
	Status   string  `json:"status"`
	Answer   string  `json:"answer"`
	Files    []File  `json:"files"`
	Points   float64 `json:"points"`
	Max      float64 `json:"max_points"`
	Feedback string  `json:"feedback"`
	ScoredBy string  `json:"scored_by,omitempty"`
}

func (x *Submission) Feedback() *Feedback {
	if x == nil {
		return nil
	}
	return &Feedback{ID: x.ID, Status: x.Status, Answer: x.Answer, Files: x.Files, Points: x.Points, Max: x.MaxPoints,
		Feedback: x.Note, ScoredBy: x.ScoredBy}
}

// ScorerView is the Submission plus its rubric, for the Anvil's scorer endpoints only. Encoding a bare Submission
// omits the rubric, so forgetting to convert fails closed.
func (x *Submission) ScorerView() any {
	return struct {
		*Submission
		Rubric string `json:"rubric"`
	}{x, x.Rubric}
}

// Progress recomputes the trainee's item (quiz or lab) after a submission was scored or returned.
type Progress interface {
	Refresh(ctx context.Context, sub *Submission) error
}

// Labs is what the Anvil needs from the lab module (implemented by *labs.Service).
type Labs interface {
	Progress
	Evidence(ctx context.Context, labID string) (*LabEvidence, error)
	// Override sets a non-review task's awarded points; record runs inside the same transaction (the audit entry)
	// and receives the previous points.
	Override(ctx context.Context, labID, task string, points float64, record func(context.Context, pgx.Tx, float64) error) error
}

type LabEvidence struct {
	Runtime      string         `json:"runtime"`
	SelfReported bool           `json:"self_reported"`
	Tasks        []TaskEvidence `json:"tasks"`
	Transcripts  []Transcript   `json:"transcripts"`
}

type TaskEvidence struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Kind      string     `json:"kind"` // check | quiz | review
	Status    string     `json:"status"`
	Points    float64    `json:"points"`
	Awarded   float64    `json:"awarded"`
	HintsUsed int        `json:"hints_used"`
	HintCost  float64    `json:"hint_cost"`
	Checks    []CheckRun `json:"checks"`
}

type CheckRun struct {
	LabID        string    `json:"lab_id"`
	At           time.Time `json:"at"`
	ExitCode     int       `json:"exit_code"`
	Output       string    `json:"output"`
	Answer       string    `json:"answer,omitempty"`
	SelfReported bool      `json:"self_reported"`
}

type Transcript struct {
	ID        int64     `json:"id"`
	LabID     string    `json:"lab_id"`
	Terminal  string    `json:"terminal"`
	Bytes     int       `json:"bytes"`
	Truncated bool      `json:"truncated"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Notifier queues notifications (implemented by *notify.Service).
type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type Service struct {
	DB     *pgxpool.Pool
	Blobs  blob.Store
	State  func() *gitsync.State
	Notify Notifier
	Quiz   Progress // *learn.Service
	Labs   Labs     // *labs.Service
	Log    *slog.Logger
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) checker() (rbac.Checker, *gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return rbac.Checker{}, nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return rbac.Checker{P: st.Platform}, st, nil
}

// Clean makes people's text safe for Postgres TEXT: valid UTF-8, no NUL bytes.
func Clean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// fileName keeps only the last path element of what the browser sent (either slash), without control or Unicode
// format characters (U+202E and friends), at most 128 runes; "file" when nothing is left.
func fileName(name string) string {
	n := path.Base(strings.ReplaceAll(Clean(name), `\`, "/"))
	n = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, n)
	if n == "." || n == ".." || n == "/" || n == "" {
		n = "file"
	}
	if r := []rune(n); len(r) > 128 {
		n = string(r[:128])
	}
	return n
}

func short(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

const subCols = `s.id, s.user_id, u.email, u.name, s.team, s.training, s.module, s.sha, s.kind, s.item, coalesce(s.lab_id, ''),
	s.qtype, s.prompt, s.rubric, s.max_points, s.answer, s.files, s.file_keys, s.status, s.points, s.feedback, s.scored_by,
	s.scored_at, s.created_at`
const subFrom = ` FROM submissions s JOIN users u ON u.id = s.user_id `

func scanSub(row pgx.Row) (*Submission, error) {
	var x Submission
	err := row.Scan(&x.ID, &x.UserID, &x.Email, &x.Name, &x.Team, &x.Training, &x.Module, &x.SHA, &x.Kind, &x.Item, &x.LabID,
		&x.QType, &x.Prompt, &x.Rubric, &x.MaxPoints, &x.Answer, &x.Files, &x.Keys, &x.Status, &x.Points, &x.Note, &x.ScoredBy,
		&x.ScoredAt, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "submission not found")
	}
	return &x, err
}

func collectSubs(rows pgx.Rows, err error) ([]*Submission, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Submission, error) { return scanSub(r) })
}

func (s *Service) get(ctx context.Context, id int64) (*Submission, error) {
	return scanSub(s.DB.QueryRow(ctx, `SELECT `+subCols+subFrom+`WHERE s.id = $1`, id))
}

// Latest returns the newest submission per item of one kind for a trainee's module.
func (s *Service) Latest(ctx context.Context, userID int64, team, training, module, kind string) (map[string]*Submission, error) {
	list, err := collectSubs(s.DB.Query(ctx, `SELECT DISTINCT ON (s.item) `+subCols+subFrom+`
		WHERE s.user_id = $1 AND s.team = $2 AND s.training = $3 AND s.module = $4 AND s.kind = $5
		ORDER BY s.item, s.id DESC`, userID, team, training, module, kind))
	if err != nil {
		return nil, err
	}
	out := map[string]*Submission{}
	for _, x := range list {
		out[x.Item] = x
	}
	return out, nil
}

func uniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

const alreadyLive = "this answer is already with a scorer or scored"

// Submit stores the files and the submission (pending), then tells the program's scorers. Callers have already checked
// that u may answer this item; Submit fills in the user and validates sizes.
func (s *Service) Submit(ctx context.Context, u *auth.User, sub *Submission, files []*multipart.FileHeader) (*Submission, error) {
	sub.Answer = Clean(strings.TrimSpace(sub.Answer))
	if utf8.RuneCountInString(sub.Answer) > maxAnswer {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("answers are limited to %d characters", maxAnswer))
	}
	if sub.Answer == "" && len(files) == 0 {
		return nil, apperr.Wrap(apperr.Invalid, "there is nothing to score: write an answer or attach a file")
	}
	if len(files) > MaxFiles {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("attach at most %d files", MaxFiles))
	}
	for _, fh := range files {
		if fh.Size > MaxFileBytes {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s is larger than 20 MiB", fileName(fh.Filename)))
		}
	}
	var live bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM submissions WHERE user_id = $1 AND team = $2 AND training = $3
		AND module = $4 AND kind = $5 AND item = $6 AND status IN ('pending', 'scored'))`,
		u.ID, sub.Team, sub.Training, sub.Module, sub.Kind, sub.Item).Scan(&live); err != nil {
		return nil, err
	}
	if live { // checked before storing files so a refused answer leaves no blobs behind
		return nil, apperr.Wrap(apperr.Conflict, alreadyLive)
	}
	sub.UserID, sub.Email, sub.Name = u.ID, strings.ToLower(u.Email), u.Name
	sub.Files, sub.Keys = []File{}, []string{}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		key := "uploads/files/" + randHex(16)
		err = s.Blobs.Put(ctx, key, f, fh.Size)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("storing %s: %w", fileName(fh.Filename), err)
		}
		sub.Files = append(sub.Files, File{Name: fileName(fh.Filename), Size: fh.Size})
		sub.Keys = append(sub.Keys, key)
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, lab_id, qtype, prompt,
		rubric, max_points, answer, files, file_keys, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10, $11, $12, $13, $14, $15, 'pending') RETURNING id, created_at`,
		sub.UserID, sub.Team, sub.Training, sub.Module, sub.SHA, sub.Kind, sub.Item, sub.LabID, sub.QType, Clean(sub.Prompt),
		Clean(sub.Rubric), sub.MaxPoints, sub.Answer, sub.Files, sub.Keys).Scan(&sub.ID, &sub.CreatedAt)
	if uniqueViolation(err) {
		return nil, apperr.Wrap(apperr.Conflict, alreadyLive) // ponytail: a lost race leaves its blobs orphaned
	}
	if err != nil {
		return nil, err
	}
	sub.Status = Pending
	s.tellScorers(ctx, sub)
	return sub, nil
}

func (s *Service) canScore(u *auth.User, team, training, owner string) (bool, error) {
	c, _, err := s.checker()
	if err != nil {
		return false, err
	}
	return c.Can(u.Email, rbac.Score, team, training, owner), nil
}

// CanView: the trainee themself, their program's scorers and managers, team leader/seniors, their mentor, admins
// (spec §5.3 "View trainee progress").
func (s *Service) CanView(u *auth.User, team, training, owner string) bool {
	c, _, err := s.checker()
	return err == nil && c.Can(u.Email, rbac.ViewProgress, team, training, owner)
}

func (s *Service) mayScore(u *auth.User, sub *Submission) error {
	ok, err := s.canScore(u, sub.Team, sub.Training, sub.Email)
	switch {
	case err != nil:
		return err
	case strings.EqualFold(u.Email, sub.Email):
		return apperr.Wrap(apperr.Forbidden, "nobody scores their own submission")
	case !ok:
		return apperr.Wrap(apperr.Forbidden, "you are not a scorer of this program")
	}
	return nil
}

func (s *Service) Score(ctx context.Context, u *auth.User, id int64, points float64, feedback string) (*Submission, error) {
	return s.decide(ctx, u, id, Scored, points, feedback)
}

func (s *Service) Return(ctx context.Context, u *auth.User, id int64, feedback string) (*Submission, error) {
	if strings.TrimSpace(feedback) == "" {
		return nil, apperr.Wrap(apperr.Invalid, "say what to rework")
	}
	return s.decide(ctx, u, id, Returned, 0, feedback)
}

// decide stores one decision on a pending submission. The WHERE status = 'pending' makes concurrent decisions safe:
// exactly one wins (spec §5.3 rules are checked first).
func (s *Service) decide(ctx context.Context, u *auth.User, id int64, status string, points float64, feedback string) (*Submission, error) {
	feedback = Clean(strings.TrimSpace(feedback))
	if utf8.RuneCountInString(feedback) > maxFeedback {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("feedback is limited to %d characters", maxFeedback))
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayScore(u, sub); err != nil {
		return nil, err
	}
	if status == Scored && (math.IsNaN(points) || points < 0 || points > sub.MaxPoints) {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("points must be between 0 and %g", sub.MaxPoints))
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE submissions SET status = $2, points = $3, feedback = $4, scored_by = lower($5), scored_at = $6
		WHERE id = $1 AND status = 'pending'`, id, status, points, feedback, u.Email, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "someone already scored or returned this submission")
	}
	action := map[string]string{Scored: "submission.score", Returned: "submission.return"}[status]
	if err := audit.Log(ctx, tx, u.Email, action, fmt.Sprintf("submission/%d", id), map[string]any{"trainee": sub.Email,
		"item": sub.Team + "/" + sub.Training + "/" + sub.Module + "/" + sub.Item, "points": points, "max": sub.MaxPoints}, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sub.Status, sub.Points, sub.Note, sub.ScoredBy, sub.ScoredAt = status, points, feedback, strings.ToLower(u.Email), &now
	s.refresh(ctx, sub)
	s.tellTrainee(ctx, sub)
	return sub, nil
}

type SignOffInput struct {
	Team     string   `json:"team"`
	Training string   `json:"training"`
	Module   string   `json:"module"`
	Question string   `json:"question"`
	Trainee  string   `json:"trainee"`
	Points   *float64 `json:"points"` // nil = full marks
	Notes    string   `json:"notes"`
}

// SignOff records a live demo a scorer watched (spec §7 "Mark passed after live demo") as a scored submission.
func (s *Service) SignOff(ctx context.Context, u *auth.User, in SignOffInput) (*Submission, error) {
	c, st, err := s.checker()
	if err != nil {
		return nil, err
	}
	trainee := strings.ToLower(strings.TrimSpace(in.Trainee))
	if strings.EqualFold(u.Email, trainee) {
		return nil, apperr.Wrap(apperr.Forbidden, "nobody signs off their own demo")
	}
	if !c.Can(u.Email, rbac.Score, in.Team, in.Training, trainee) {
		return nil, apperr.Wrap(apperr.Forbidden, "you are not a scorer of this program")
	}
	if !c.Can(trainee, rbac.TakeTraining, in.Team, in.Training, "") {
		return nil, apperr.Wrap(apperr.Invalid, "that person is not enrolled in this training")
	}
	t, sha := st.ProgramTraining(in.Team, in.Training)
	if t == nil || t.Module(in.Module) == nil || t.Module(in.Module).Quiz == nil {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	q := t.Module(in.Module).Quiz.Question(in.Question)
	if q == nil || q.Type != "signoff" {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	points := q.Points
	if in.Points != nil {
		points = *in.Points
		if math.IsNaN(points) || points < 0 || points > q.Points {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("points must be between 0 and %g", q.Points))
		}
	}
	notes := Clean(strings.TrimSpace(in.Notes))
	if utf8.RuneCountInString(notes) > maxFeedback {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("notes are limited to %d characters", maxFeedback))
	}
	var userID int64
	err = s.DB.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 ORDER BY id LIMIT 1`, trainee).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "that trainee has not signed in to Crucible yet")
	}
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype, prompt, rubric,
		max_points, status, points, feedback, scored_by, scored_at)
		VALUES ($1, $2, $3, $4, $5, 'question', $6, 'signoff', $7, $8, $9, 'scored', $10, $11, lower($12), $13) RETURNING id`,
		userID, in.Team, in.Training, in.Module, sha, in.Question, q.Prompt, q.Rubric, q.Points, points, notes, u.Email, s.now()).Scan(&id)
	if uniqueViolation(err) {
		return nil, apperr.Wrap(apperr.Conflict, "this demo is already signed off")
	}
	if err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, u.Email, "submission.signoff", fmt.Sprintf("submission/%d", id), map[string]any{"trainee": trainee,
		"item": in.Team + "/" + in.Training + "/" + in.Module + "/" + in.Question, "points": points}, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refresh(ctx, sub)
	s.tellTrainee(ctx, sub)
	return sub, nil
}

// refresh recomputes the trainee's item. A failure is logged, never shown to the scorer: the decision is stored.
func (s *Service) refresh(ctx context.Context, sub *Submission) {
	var p Progress = s.Quiz
	if sub.Kind == KindTask {
		p = s.Labs
	}
	if p == nil {
		return
	}
	if err := p.Refresh(ctx, sub); err != nil {
		s.log().Error("updating progress after a scoring decision failed", "submission", sub.ID, "err", err)
	}
}

func (s *Service) send(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || len(ev.To) == 0 {
		return
	}
	if err := s.Notify.Notify(ctx, ev); err != nil {
		s.log().Error("queueing a scoring notification failed", "kind", ev.Kind, "err", err)
	}
}

// tellScorers: the program's scorers, or the admins when a program has none (spec §10 "submission awaiting scoring").
func (s *Service) tellScorers(ctx context.Context, sub *Submission) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return
	}
	var to []string
	if t := st.Platform.Teams[sub.Team]; t != nil && t.Programs[sub.Training] != nil {
		to = slices.Clone(t.Programs[sub.Training].Roles.Scorers)
	}
	if len(to) == 0 {
		to = slices.Clone(st.Platform.Admins)
	}
	to = slices.DeleteFunc(to, func(e string) bool { return strings.EqualFold(e, sub.Email) })
	who := sub.Name
	if who == "" {
		who = sub.Email
	}
	s.send(ctx, notify.Event{Kind: notify.SubmissionPending, To: to, Team: sub.Team, // no points: safe for the team channel
		Subject: "Waiting on the anvil: " + sub.Training,
		Text:    fmt.Sprintf("%s submitted %q (%s / %s) for scoring.", who, short(sub.Prompt), sub.Training, sub.Module),
		Link:    fmt.Sprintf("/anvil/%d", sub.ID)})
}

// tellTrainee: by email only. Scores are private (spec §2 "Score privacy"), so never to a team webhook.
func (s *Service) tellTrainee(ctx context.Context, sub *Submission) {
	verb := "was returned for rework"
	if sub.Status == Scored {
		verb = fmt.Sprintf("was scored %g/%g", sub.Points, sub.MaxPoints)
	}
	page := "quiz"
	if sub.Kind == KindTask {
		page = "lab"
	}
	s.send(ctx, notify.Event{Kind: notify.SubmissionScored, To: []string{sub.Email},
		Subject: "From the anvil: " + sub.Training,
		Text:    fmt.Sprintf("Your answer to %q %s. The feedback is on the page.", short(sub.Prompt), verb),
		Link:    fmt.Sprintf("/p/%s/%s/m/%s/%s", sub.Team, sub.Training, sub.Module, page)})
}
