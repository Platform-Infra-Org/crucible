package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/rbac"
)

type Service struct {
	DB    *pgxpool.Pool
	State func() *gitsync.State
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
}

type progress map[string]string // "module/item" → status

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

func (s *Service) progress(ctx context.Context, userID int64, team, training string) (progress, error) {
	rows, err := s.DB.Query(ctx, `SELECT module, item, status FROM item_progress
		WHERE user_id = $1 AND team = $2 AND training = $3`, userID, team, training)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := progress{}
	for rows.Next() {
		var m, i, st string
		if err := rows.Scan(&m, &i, &st); err != nil {
			return nil, err
		}
		p[m+"/"+i] = st
	}
	return p, rows.Err()
}

func outline(t *content.Training, prog progress) []ModuleView {
	out := []ModuleView{}
	allPrevComplete := true
	for _, m := range t.Modules {
		mv := ModuleView{ID: m.ID, Title: m.Title, Locked: t.Progression == "linear" && !allPrevComplete, Complete: true}
		for _, it := range m.Items {
			st := prog[m.ID+"/"+it.ID]
			if st == "" {
				st = "new"
			}
			if st != "complete" {
				mv.Complete = false
			}
			mv.Items = append(mv.Items, ItemView{Item: it, Status: st})
		}
		allPrevComplete = allPrevComplete && mv.Complete
		out = append(out, mv)
	}
	return out
}

func percent(t *content.Training, prog progress) int {
	total, done := 0, 0
	for _, m := range t.Modules {
		for _, it := range m.Items {
			total++
			if prog[m.ID+"/"+it.ID] == "complete" {
				done++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return done * 100 / total
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
			prog, err := s.progress(ctx, u.ID, e.Team.ID, t.ID)
			if err != nil {
				return nil, err
			}
			c.Title, c.Description, c.Available, c.Percent = t.Title, t.Description, true, percent(t, prog)
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
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	return &Outline{Team: team, Training: t.ID, Title: t.Title, Description: t.Description,
		Progression: t.Progression, Percent: percent(t, prog), Modules: outline(t, prog)}, nil
}

// EnsureUnlocked returns the module, or apperr.Locked if a linear training has unfinished earlier modules.
func (s *Service) EnsureUnlocked(ctx context.Context, u *auth.User, team string, t *content.Training, module string) (*content.Module, error) {
	m := t.Module(module)
	if m == nil {
		return nil, apperr.Wrap(apperr.NotFound, "module not found")
	}
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	for _, mv := range outline(t, prog) {
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

func seedFor(userID int64, team, training, module string) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d/%s/%s/%s", userID, team, training, module)
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
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	status := prog[module+"/quiz"]
	if status == "" {
		status = "new"
	}
	return &QuizView{PassThreshold: m.Quiz.PassThreshold, Questions: PublicQuiz(m.Quiz, seedFor(u.ID, team, training, module)), Status: status}, nil
}

func (s *Service) SubmitQuiz(ctx context.Context, u *auth.User, team, training, module string, answers map[string]json.RawMessage) (*Result, error) {
	t, sha, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	res := Score(m.Quiz, answers)
	stored, _ := json.Marshal(answers)
	if _, err := s.DB.Exec(ctx, `INSERT INTO quiz_attempts (user_id, team, training, module, sha, answers, score, max_score, passed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, u.ID, team, t.ID, module, sha, stored, res.Score, res.Max, res.Passed); err != nil {
		return nil, err
	}
	status := "in_progress"
	if res.Passed {
		status = "complete"
	}
	if err := s.SetItem(ctx, u.ID, team, t.ID, module, "quiz", status, res.Percent); err != nil {
		return nil, err
	}
	return &res, nil
}
