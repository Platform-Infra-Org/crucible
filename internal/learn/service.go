package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"mime/multipart"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/rbac"
	"crucible/internal/scoring"
)

type Service struct {
	DB    *pgxpool.Pool
	State func() *gitsync.State
	// QuizSecret is mixed into quiz choice-id seeds so learners cannot predict them; it must be stable across restarts.
	QuizSecret string
	// Scoring stores human-scored answers (M5). nil in tests that only exercise instant quizzes.
	Scoring *scoring.Service
	// Versions loads a training at an exact sha from the mirror when it isn't in memory (gitsync.Syncer.Version).
	// nil: only in-memory versions (tests).
	Versions func(ctx context.Context, id, sha string) *content.Training
}

// Version returns training id at exactly sha, or nil if that version isn't available. Never another version: labs
// and answers are scored against the content they ran on.
func (s *Service) Version(ctx context.Context, id, sha string) *content.Training {
	if t := s.State().Training(id, sha); t != nil || s.Versions == nil {
		return t
	}
	return s.Versions(ctx, id, sha)
}

type ProgramCard struct {
	Team        string `json:"team"`
	TeamName    string `json:"team_name"`
	Training    string `json:"training"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Percent     int    `json:"percent"`
	Available   bool   `json:"available"`
}

type Outline struct {
	Team        string       `json:"team"`
	Training    string       `json:"training"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Progression string       `json:"progression"`
	Percent     int          `json:"percent"`
	Modules     []ModuleView `json:"modules"`
}

type ModuleView struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Locked   bool       `json:"locked"`
	Complete bool       `json:"complete"`
	Items    []ItemView `json:"items"`
}

type ItemView struct {
	content.Item
	Status string `json:"status"` // new | in_progress | complete
}

type QuizView struct {
	PassThreshold float64          `json:"pass_threshold"`
	Questions     []PublicQuestion `json:"questions"`
	Status        string           `json:"status"`
	AttemptsLeft  *int             `json:"attempts_left,omitempty"`   // only when the quiz sets max_attempts
	NextAttemptAt *time.Time       `json:"next_attempt_at,omitempty"` // only inside a cooldown
}

type progress map[string]string // "module/item" → status
type scores map[string]float64  // "module/item" → best score 0..1

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Program checks enrolment and returns the training version this team's program runs.
func (s *Service) Program(u *auth.User, team, training string) (*gitsync.State, *content.Training, string, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, "", err
	}
	if !(rbac.Checker{P: st.Platform}).Can(u.Email, rbac.TakeTraining, team, training, "") {
		return nil, nil, "", apperr.Wrap(apperr.Forbidden, "you are not enrolled in this training")
	}
	t, sha := st.ProgramTraining(team, training)
	if t == nil {
		return nil, nil, "", apperr.Wrap(apperr.Unavailable, "this training's content is unavailable right now")
	}
	return st, t, sha, nil
}

func (s *Service) progress(ctx context.Context, userID int64, team, training string) (progress, scores, error) {
	rows, err := s.DB.Query(ctx, `SELECT module, item, status, score FROM item_progress
		WHERE user_id = $1 AND team = $2 AND training = $3`, userID, team, training)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	p, sc := progress{}, scores{}
	for rows.Next() {
		var m, i, st string
		var v float64
		if err := rows.Scan(&m, &i, &st, &v); err != nil {
			return nil, nil, err
		}
		p[m+"/"+i], sc[m+"/"+i] = st, v
	}
	return p, sc, rows.Err()
}

// Weight is an item's share of its training (spec §7 "weighted by item points"): a reading counts 1, a quiz its
// question points, a lab its task points.
func Weight(m *content.Module, it content.Item) float64 {
	w := 0.0
	switch it.Kind {
	case "quiz":
		if m.Quiz != nil {
			for _, q := range m.Quiz.Questions {
				w += q.Points
			}
		}
	case "lab":
		if m.Lab != nil {
			for _, t := range m.Lab.Tasks {
				w += t.Points
			}
		}
	}
	if w <= 0 {
		return 1
	}
	return w
}

// moduleComplete applies the module's completion rule (spec §7): every item complete, or the weighted score reaching
// the threshold.
func moduleComplete(m *content.Module, prog progress, sc scores) bool {
	if m.Completion == "score" {
		var got, total float64
		for _, it := range m.Items {
			w := Weight(m, it)
			total += w
			got += w * sc[m.ID+"/"+it.ID]
		}
		return total > 0 && got/total >= m.Threshold-1e-9
	}
	for _, it := range m.Items {
		if prog[m.ID+"/"+it.ID] != "complete" {
			return false
		}
	}
	return true
}

// completion is the weighted share of t the user has finished. A forged module counts fully.
func completion(t *content.Training, prog progress, sc scores) (done, total float64) {
	for _, m := range t.Modules {
		forged := moduleComplete(m, prog, sc)
		for _, it := range m.Items {
			w := Weight(m, it)
			total += w
			if forged || prog[m.ID+"/"+it.ID] == "complete" {
				done += w
			}
		}
	}
	return done, total
}

func percent(t *content.Training, prog progress, sc scores) int {
	done, total := completion(t, prog, sc)
	if total == 0 {
		return 0
	}
	return int(done * 100 / total)
}

func outline(t *content.Training, prog progress, sc scores) []ModuleView {
	out := []ModuleView{}
	allPrevComplete := true
	for _, m := range t.Modules {
		mv := ModuleView{ID: m.ID, Title: m.Title, Locked: t.Progression == "linear" && !allPrevComplete, Complete: moduleComplete(m, prog, sc)}
		for _, it := range m.Items {
			st := prog[m.ID+"/"+it.ID]
			if st == "" {
				st = "new"
			}
			mv.Items = append(mv.Items, ItemView{Item: it, Status: st})
		}
		allPrevComplete = allPrevComplete && mv.Complete
		out = append(out, mv)
	}
	return out
}

// Standing is one user's position in one program, for ranks and journey views.
type Standing struct {
	Training    *content.Training
	Modules     []ModuleView
	Percent     int
	Done, Total float64           // weighted
	Status      map[string]string // "module/item" → status
}

// Standing returns nil, nil when the program's content is unavailable (still syncing or invalid).
func (s *Service) Standing(ctx context.Context, userID int64, team, training string) (*Standing, error) {
	st := s.State()
	if st == nil {
		return nil, nil
	}
	t, _ := st.ProgramTraining(team, training)
	if t == nil {
		return nil, nil
	}
	prog, sc, err := s.progress(ctx, userID, team, t.ID)
	if err != nil {
		return nil, err
	}
	done, total := completion(t, prog, sc)
	return &Standing{Training: t, Modules: outline(t, prog, sc), Percent: percent(t, prog, sc), Done: done, Total: total, Status: prog}, nil
}

func (s *Service) Programs(ctx context.Context, u *auth.User) ([]ProgramCard, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	cards := []ProgramCard{}
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(u.Email) {
		c := ProgramCard{Team: e.Team.ID, TeamName: e.Team.Name, Training: e.Program.Training, Title: e.Program.Training}
		if t, _ := st.ProgramTraining(e.Team.ID, e.Program.Training); t != nil {
			prog, sc, err := s.progress(ctx, u.ID, e.Team.ID, t.ID)
			if err != nil {
				return nil, err
			}
			c.Title, c.Description, c.Available, c.Percent = t.Title, t.Description, true, percent(t, prog, sc)
		}
		cards = append(cards, c)
	}
	return cards, nil
}

func (s *Service) Outline(ctx context.Context, u *auth.User, team, training string) (*Outline, error) {
	_, t, _, err := s.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	prog, sc, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	return &Outline{Team: team, Training: t.ID, Title: t.Title, Description: t.Description,
		Progression: t.Progression, Percent: percent(t, prog, sc), Modules: outline(t, prog, sc)}, nil
}

// EnsureUnlocked returns the module, or apperr.Locked if a linear training has unfinished earlier modules.
func (s *Service) EnsureUnlocked(ctx context.Context, u *auth.User, team string, t *content.Training, module string) (*content.Module, error) {
	m := t.Module(module)
	if m == nil {
		return nil, apperr.Wrap(apperr.NotFound, "module not found")
	}
	prog, sc, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	for _, mv := range outline(t, prog, sc) {
		if mv.ID == module && mv.Locked {
			return nil, apperr.Wrap(apperr.Locked, "finish the earlier modules first")
		}
	}
	return m, nil
}

// SetItem records progress; a completed item never goes back to in_progress.
func (s *Service) SetItem(ctx context.Context, userID int64, team, training, module, item, status string, score float64) error {
	_, err := s.DB.Exec(ctx, `
		INSERT INTO item_progress (user_id, team, training, module, item, status, score)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, team, training, module, item) DO UPDATE SET
		  status = CASE WHEN item_progress.status = 'complete' THEN 'complete' ELSE EXCLUDED.status END,
		  score = GREATEST(item_progress.score, EXCLUDED.score),
		  updated_at = now()`, userID, team, training, module, item, status, score)
	return err
}

func findItem(m *content.Module, kind, id string) *content.Item {
	for i := range m.Items {
		if m.Items[i].Kind == kind && m.Items[i].ID == id {
			return &m.Items[i]
		}
	}
	return nil
}

func (s *Service) Reading(ctx context.Context, u *auth.User, team, training, module, item string) (string, string, error) {
	_, t, _, err := s.Program(u, team, training)
	if err != nil {
		return "", "", err
	}
	m, err := s.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return "", "", err
	}
	it := findItem(m, "reading", item)
	if it == nil {
		return "", "", apperr.Wrap(apperr.NotFound, "reading not found")
	}
	b, err := os.ReadFile(it.Path)
	if err != nil {
		return "", "", err
	}
	return it.Title, string(b), nil
}

func (s *Service) MarkRead(ctx context.Context, u *auth.User, team, training, module, item string) error {
	if _, _, err := s.Reading(ctx, u, team, training, module, item); err != nil {
		return err
	}
	return s.SetItem(ctx, u.ID, team, training, module, item, "complete", 1)
}

func (s *Service) seedFor(userID int64, team, training, module string) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%d/%s/%s/%s", s.QuizSecret, userID, team, training, module)
	return h.Sum64()
}

func (s *Service) quizModule(ctx context.Context, u *auth.User, team, training, module string) (*content.Training, string, *content.Module, error) {
	_, t, sha, err := s.Program(u, team, training)
	if err != nil {
		return nil, "", nil, err
	}
	m, err := s.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, "", nil, err
	}
	if findItem(m, "quiz", "quiz") == nil {
		return nil, "", nil, apperr.Wrap(apperr.NotFound, "this module has no quiz")
	}
	return t, sha, m, nil
}

func (s *Service) Quiz(ctx context.Context, u *auth.User, team, training, module string) (*QuizView, error) {
	t, _, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	prog, _, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	status := prog[module+"/quiz"]
	if status == "" {
		status = "new"
	}
	subs, err := s.latest(ctx, u.ID, team, t.ID, module)
	if err != nil {
		return nil, err
	}
	if status != "complete" && decided(subs) { // settle a decision whose refresh failed (it runs after the scoring tx)
		if status, _, err = s.refreshQuiz(ctx, u.ID, team, t, module); err != nil {
			return nil, err
		}
	}
	qs := PublicQuiz(m.Quiz, s.seedFor(u.ID, team, training, module))
	for i := range qs {
		qs[i].Submission = subs[qs[i].ID].Feedback() // nil-safe; Feedback never carries the rubric
	}
	left, next, err := attemptGate(ctx, s.DB, u.ID, team, t.ID, module, m.Quiz)
	if err != nil {
		return nil, err
	}
	return &QuizView{PassThreshold: m.Quiz.PassThreshold, Questions: qs, Status: status, AttemptsLeft: left, NextAttemptAt: next}, nil
}

// attemptGate reports how many instant attempts remain and when the cooldown ends (spec §7; defaults: unlimited, no
// cooldown). Human answers (AnswerHuman) are not attempts.
func attemptGate(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID int64, team, training, module string, q *content.Quiz) (left *int, next *time.Time, err error) {
	if q.MaxAttempts == 0 && q.Cooldown == 0 {
		return nil, nil, nil
	}
	var n int
	var last *time.Time
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT count(*), max(created_at), now() FROM quiz_attempts
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, userID, team, training, module).Scan(&n, &last, &now); err != nil {
		return nil, nil, err
	}
	if q.MaxAttempts > 0 {
		l := max(q.MaxAttempts-n, 0)
		left = &l
	}
	if q.Cooldown > 0 && last != nil {
		if at := last.Add(q.Cooldown.D()); at.After(now) {
			next = &at
		}
	}
	return left, next, nil
}

func (s *Service) SubmitQuiz(ctx context.Context, u *auth.User, team, training, module string, answers map[string]json.RawMessage) (*Result, error) {
	t, sha, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	res := Score(m.Quiz, s.seedFor(u.ID, team, training, module), answers)
	stored, _ := json.Marshal(answers)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	// Serialise this user's attempts on this quiz so two tabs cannot both take the last attempt.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("quiz-attempt/%d/%s/%s/%s", u.ID, team, t.ID, module)); err != nil {
		return nil, err
	}
	left, next, err := attemptGate(ctx, tx, u.ID, team, t.ID, module, m.Quiz)
	if err != nil {
		return nil, err
	}
	if left != nil && *left == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "you have used every attempt for this quiz")
	}
	if next != nil {
		wait := time.Until(*next).Truncate(time.Minute) + time.Minute // round up: "in 0s" helps nobody
		return nil, apperr.Wrap(apperr.Conflict, fmt.Sprintf("the next attempt opens at %s (in %s)",
			next.UTC().Format("15:04 UTC"), strings.TrimSuffix(wait.String(), "0s")))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO quiz_attempts (user_id, team, training, module, sha, answers, score, max_score, passed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, u.ID, team, t.ID, module, sha, stored, res.Score, res.Max, res.Passed); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	status, pct, err := s.refreshQuiz(ctx, u.ID, team, t, module)
	if err != nil {
		return nil, err
	}
	res.Status, res.Percent, res.Passed = status, pct, status == "complete"
	return &res, nil
}

func (s *Service) latest(ctx context.Context, userID int64, team, training, module string) (map[string]*scoring.Submission, error) {
	if s.Scoring == nil {
		return nil, nil
	}
	return s.Scoring.Latest(ctx, userID, team, training, module, scoring.KindQuestion)
}

// refreshQuiz recomputes the quiz item from the best instant attempt ("best score counts", spec §7) and human scores.
func (s *Service) refreshQuiz(ctx context.Context, userID int64, team string, t *content.Training, module string) (string, float64, error) {
	m := t.Module(module)
	if m == nil || m.Quiz == nil {
		return "", 0, apperr.Wrap(apperr.NotFound, "this module has no quiz")
	}
	var best float64
	var attempts int
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(max(score), 0), count(*) FROM quiz_attempts
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, userID, team, t.ID, module).Scan(&best, &attempts); err != nil {
		return "", 0, err
	}
	subs, err := s.latest(ctx, userID, team, t.ID, module)
	if err != nil {
		return "", 0, err
	}
	status, pct := quizOutcome(m.Quiz, best, attempts > 0, subs)
	return status, pct, s.SetItem(ctx, userID, team, t.ID, module, "quiz", status, pct)
}

// AnswerHuman hands a text or upload answer to the program's scorers (spec §4.4, §7). Sign-offs come from scorers.
func (s *Service) AnswerHuman(ctx context.Context, u *auth.User, team, training, module, question, answer string, files []*multipart.FileHeader) (*QuizView, error) {
	t, sha, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	x := m.Quiz.Question(question)
	if x == nil || !content.IsHuman(x.Type) {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	answer = strings.TrimSpace(answer)
	switch x.Type {
	case "signoff":
		return nil, apperr.Wrap(apperr.Conflict, "a scorer signs this off after a live demo")
	case "text":
		if answer == "" || len(files) > 0 {
			return nil, apperr.Wrap(apperr.Invalid, "write your answer (text questions take no files)")
		}
	case "upload":
		if answer == "" && len(files) == 0 {
			return nil, apperr.Wrap(apperr.Invalid, "attach a file or paste a link")
		}
		if answer != "" && !httpLink(answer) {
			return nil, apperr.Wrap(apperr.Invalid, "a link must start with http:// or https://")
		}
	}
	if s.Scoring == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "scoring is not available")
	}
	if _, err := s.Scoring.Submit(ctx, u, &scoring.Submission{Team: team, Training: t.ID, Module: module, SHA: sha,
		Kind: scoring.KindQuestion, Item: x.ID, QType: x.Type, Prompt: x.Prompt, Rubric: x.Rubric, MaxPoints: x.Points,
		Answer: answer}, files); err != nil {
		return nil, err
	}
	if _, _, err := s.refreshQuiz(ctx, u.ID, team, t, module); err != nil {
		return nil, err
	}
	return s.Quiz(ctx, u, team, training, module)
}

// httpLink accepts only absolute http(s) URLs: scorers click these, so javascript: and friends never get stored.
func httpLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\r\n")
}

// Refresh implements scoring.Progress: a scorer decided on one of this trainee's answers.
func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error {
	st, err := s.state()
	if err != nil {
		return err
	}
	t, _ := st.ProgramTraining(sub.Team, sub.Training)
	if t == nil || t.Module(sub.Module) == nil {
		t = s.Version(ctx, sub.Training, sub.SHA)
	}
	if t == nil {
		return apperr.Wrap(apperr.Unavailable, "this training's content is unavailable right now")
	}
	_, _, err = s.refreshQuiz(ctx, sub.UserID, sub.Team, t, sub.Module)
	return err
}

// ForceScore sets an item's score exactly. SetItem only ever raises it; an override may lower it (labs).
func (s *Service) ForceScore(ctx context.Context, userID int64, team, training, module, item string, score float64) error {
	_, err := s.DB.Exec(ctx, `UPDATE item_progress SET score = $6, updated_at = now()
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 AND item = $5`, userID, team, training, module, item, score)
	return err
}

func decided(subs map[string]*scoring.Submission) bool {
	for _, x := range subs {
		if x.Status != scoring.Pending {
			return true
		}
	}
	return false
}
