package learn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/scoring"
)

const team, f301, temper = "forge", "forge-301", "01-temper"

func fixture301(t *testing.T) (*Service, *scoring.Service, *auth.User, *auth.User) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	plat.Teams["forge"].Programs["forge-301"] = &config.Program{Training: "forge-301", Enrolled: []string{"trainee@crucible.local"},
		Roles: config.Roles{Scorers: []string{"senior@crucible.local"}}}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-301@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-301": "abc"}}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	senior, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	s := &Service{DB: pool, QuizSecret: "test-secret", State: func() *gitsync.State { return st }}
	sc := &scoring.Service{DB: pool, Blobs: blob.Disk{Dir: t.TempDir()}, State: s.State, Quiz: s, Log: slog.Default()}
	s.Scoring = sc
	return s, sc, trainee, senior
}

func submissionIDs(v *QuizView) map[string]int64 {
	ids := map[string]int64{}
	for _, q := range v.Questions {
		if q.Submission != nil {
			ids[q.ID] = q.Submission.ID
		}
	}
	return ids
}

func TestHumanQuestionsHoldTheModuleUntilScored(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	quiz := s.State().Trainings["forge-301@abc"].Module(temper).Quiz
	res, err := s.SubmitQuiz(ctx, u, team, f301, temper, asPublic(quiz, s.seedFor(u.ID, team, f301, temper), map[string]json.RawMessage{"q-quench": raw("0")}))
	if err != nil || res.Passed || res.Status != "in_progress" || !res.PendingHuman {
		t.Fatalf("instant part alone: %+v %v", res, err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Quenched steel is brittle; tempering makes it tough.", nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-log", "https://logs.example.com/run/42", nil)
	if err != nil || v.Status != "pending_review" {
		t.Fatalf("only scorers are missing now: %+v %v", v, err)
	}
	o, _ := s.Outline(ctx, u, team, f301)
	if !o.Modules[1].Locked || o.Modules[0].Items[0].Status != "pending_review" {
		t.Fatalf("linear progression waits for the scorer: %+v", o.Modules)
	}
	ids := submissionIDs(v)
	if _, err := sc.Score(ctx, senior, ids["q-why"], 5, "Good."); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Score(ctx, senior, ids["q-log"], 2, ""); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.Outline(ctx, u, team, f301); !o.Modules[1].Locked {
		t.Fatal("the live sign-off is still missing")
	}
	if _, err := sc.SignOff(ctx, senior, scoring.SignOffInput{Team: team, Training: f301, Module: temper, Question: "q-demo", Trainee: u.Email}); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Outline(ctx, u, team, f301)
	if o.Modules[1].Locked || !o.Modules[0].Complete {
		t.Fatalf("all scored and over the threshold: %+v", o.Modules)
	}
	v, _ = s.Quiz(ctx, u, team, f301, temper)
	for _, q := range v.Questions {
		if q.ID == "q-why" && (q.Submission == nil || q.Submission.Feedback != "Good." || q.Submission.Points != 5) {
			t.Fatalf("trainee sees the feedback: %+v", q.Submission)
		}
	}
}

func TestReturnedAnswerReopensTheQuestion(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	v, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Because.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Return(ctx, senior, submissionIDs(v)["q-why"], "Say what quenching does to the steel."); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Quiz(ctx, u, team, f301, temper)
	if v.Status != "in_progress" {
		t.Fatalf("returned work is the trainee's move again: %s", v.Status)
	}
	v, err = s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Quenching makes it brittle; tempering restores toughness.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Score(ctx, senior, submissionIDs(v)["q-why"], 5, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "again", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a scored answer is final: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-demo", "I did it", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("sign-offs come from scorers: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-quench", "0", nil); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("instant questions go through attempts: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "x", []*multipart.FileHeader{{Filename: "a.txt", Size: 1}}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("text questions take no files: %v", err)
	}
}

func TestUploadLinkMustBeHTTP(t *testing.T) {
	ctx := context.Background()
	s, _, u, _ := fixture301(t)
	for _, bad := range []string{"", "javascript:alert(1)", "ftp://logs.example.com/x", "https://", "https://a b"} {
		if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-log", bad, nil); !errors.Is(err, apperr.Invalid) {
			t.Errorf("link %q: %v", bad, err)
		}
	}
}

func TestTraineeNeverSeesRubric(t *testing.T) {
	ctx := context.Background()
	s, _, u, _ := fixture301(t)
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Brittle.", nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.Quiz(ctx, u, team, f301, temper)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "Half marks") || strings.Contains(string(b), "Return screenshots") || strings.Contains(string(b), "rubric") {
		t.Fatalf("rubric leaked: %s", b)
	}
}
