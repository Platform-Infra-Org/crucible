package learn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

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

func TestForceScoreSetsExactlyAndNeverCreatesARow(t *testing.T) {
	ctx := context.Background()
	s, _, u, _ := fixture301(t)
	if err := s.ForceScore(ctx, u.ID, team, f301, temper, "quiz", 0.5); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM item_progress WHERE user_id = $1`, u.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no progress row: a no-op (%d rows, %v)", n, err)
	}
	if err := s.SetItem(ctx, u.ID, team, f301, temper, "quiz", "complete", 0.9); err != nil {
		t.Fatal(err)
	}
	if err := s.ForceScore(ctx, u.ID, team, f301, temper, "quiz", 0.4); err != nil {
		t.Fatal(err)
	}
	var st string
	var score float64
	if err := s.DB.QueryRow(ctx, `SELECT status, score FROM item_progress WHERE user_id = $1`, u.ID).Scan(&st, &score); err != nil ||
		st != "complete" || score != 0.4 {
		t.Fatalf("forced score may go down, status kept: %s %v %v", st, score, err)
	}
}

// A decision whose progress refresh failed (it runs after the scoring transaction) is applied when the quiz is next viewed.
func TestQuizSettlesDecisionsOnPageLoad(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	quiz := s.State().Trainings["forge-301@abc"].Module(temper).Quiz
	if _, err := s.SubmitQuiz(ctx, u, team, f301, temper, asPublic(quiz, s.seedFor(u.ID, team, f301, temper), map[string]json.RawMessage{"q-quench": raw("0")})); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Brittle, then tough.", nil)
	v, _ := s.AnswerHuman(ctx, u, team, f301, temper, "q-log", "https://logs.example.com/run/42", nil)
	sc.Quiz = nil // the refresh after a decision never happens
	ids := submissionIDs(v)
	_, _ = sc.Score(ctx, senior, ids["q-why"], 5, "")
	_, _ = sc.Score(ctx, senior, ids["q-log"], 2, "")
	if _, err := sc.SignOff(ctx, senior, scoring.SignOffInput{Team: team, Training: f301, Module: temper, Question: "q-demo", Trainee: u.Email}); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.Outline(ctx, u, team, f301); !o.Modules[1].Locked {
		t.Fatal("refresh was skipped, so the item should be stale")
	}
	if v, _ = s.Quiz(ctx, u, team, f301, temper); v.Status != "complete" {
		t.Fatalf("settled on load: %s", v.Status)
	}
	if o, _ := s.Outline(ctx, u, team, f301); o.Modules[1].Locked {
		t.Fatal("the stored progress was settled too")
	}
}

type tripwire struct{ t *testing.T }

func (r tripwire) Read([]byte) (int, error) {
	r.t.Error("the body was read before enrolment was checked")
	return 0, io.EOF
}

func TestAnswerChecksEnrolmentBeforeReadingTheBody(t *testing.T) {
	s, _, _, _ := fixture301(t)
	r := chi.NewRouter()
	s.Routes(r)
	req := httptest.NewRequest(http.MethodPost, "/api/programs/forge/forge-301/modules/01-temper/quiz/questions/q-why/answer", tripwire{t})
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 99, Email: "stranger@crucible.local"}))
	req.Header.Set("X-Crucible-Upload", "1")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("not enrolled: %d", w.Code)
	}
}

// An admin reset lowers the item, reopens the score and keeps the forge rank (spec §7: ranks are never lost).
func TestAdminResetRecomputesProgressAndKeepsRank(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	admin, _ := auth.Store{DB: s.DB}.UpsertUser(ctx, "s9", "admin@crucible.local", "Ada")
	quiz := s.State().Trainings["forge-301@abc"].Module(temper).Quiz
	if _, err := s.SubmitQuiz(ctx, u, team, f301, temper, asPublic(quiz, s.seedFor(u.ID, team, f301, temper), map[string]json.RawMessage{"q-quench": raw("0")})); err != nil {
		t.Fatal(err)
	}
	v, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Quenched steel is brittle; tempering makes it tough.", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := submissionIDs(v)["q-why"]
	if v, err = s.AnswerHuman(ctx, u, team, f301, temper, "q-log", "https://logs.example.com/run/42", nil); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		id int64
		p  float64
	}{{id, 5}, {submissionIDs(v)["q-log"], 2}} {
		if _, err := sc.Score(ctx, senior, x.id, x.p, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sc.SignOff(ctx, senior, scoring.SignOffInput{Team: team, Training: f301, Module: temper, Question: "q-demo", Trainee: u.Email}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Standing(ctx, u.ID, team, f301)
	f0, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Percent == 0 {
		t.Fatalf("expected progress before the reset: %+v", before)
	}
	if _, err := sc.Reset(ctx, senior, id, scoring.ResetInput{Reason: "x", Score: true}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("a scorer may not reset: %v", err)
	}
	if _, err := sc.Reset(ctx, admin, id, scoring.ResetInput{Score: true}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a reason is required: %v", err)
	}
	if _, err := sc.Reset(ctx, admin, id, scoring.ResetInput{Reason: "rubric changed", Attempts: true, Score: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Standing(ctx, u.ID, team, f301)
	if after.Percent >= before.Percent || after.Status[temper+"/quiz"] == "complete" {
		t.Fatalf("progress must drop: before %d after %+v", before.Percent, after)
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM quiz_attempts WHERE user_id = $1`, u.ID).Scan(&n)
	var st string
	_ = s.DB.QueryRow(ctx, `SELECT status FROM submissions WHERE id = $1`, id).Scan(&st)
	var audits int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'submission.reset' AND detail->>'reason' = 'rubric changed'`).Scan(&audits)
	if n != 0 || st != "pending" || audits != 1 {
		t.Fatalf("attempts %d, status %s, audits %d", n, st, audits)
	}
	if _, err := sc.Reset(ctx, admin, id, scoring.ResetInput{Reason: "again", Score: true}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a pending submission cannot be reopened: %v", err)
	}
	f, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Level != f0.Level || f.Percent >= f0.Percent {
		t.Fatalf("percent drops, the rank stays: before %+v after %+v", f0, f)
	}
}

// An instant-only quiz at max_attempts has no submission: the admin resets it by trainee and module (spec §7).
func TestAdminQuizResetReopensAnInstantQuiz(t *testing.T) {
	ctx := context.Background()
	s, u, leader := fixture(t)
	admin, _ := auth.Store{DB: s.DB}.UpsertUser(ctx, "s9", "admin@crucible.local", "Ada")
	notes := &fakeNotify{}
	sc := &scoring.Service{DB: s.DB, State: s.State, Quiz: s, Notify: notes, Log: slog.Default()}
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	s.State().Trainings["forge-101@abc"].Module("01-welcome").Quiz.MaxAttempts = 1
	bad := correctAnswers()
	bad["q-port"] = raw(`"1"`)
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers())); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("locked out: %v", err)
	}
	post := func(who *auth.User, body string) int {
		r := chi.NewRouter()
		sc.Routes(r)
		req := httptest.NewRequest(http.MethodPost, "/api/admin/quiz-reset", strings.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), who))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	body := `{"email":"TRAINEE@crucible.local","team":"forge","training":"forge-101","module":"01-welcome","attempts":true,"reason":"typo in q-port"}`
	if c := post(leader, body); c != http.StatusForbidden {
		t.Fatalf("a leader may not reset: %d", c)
	}
	if c := post(admin, strings.Replace(body, "01-welcome", "99-nope", 1)); c != http.StatusNotFound {
		t.Fatalf("unknown module: %d", c)
	}
	if c := post(admin, body); c != http.StatusNoContent {
		t.Fatalf("admin reset: %d", c)
	}
	var audits int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'quiz.reset' AND detail->>'reason' = 'typo in q-port'
		AND (detail->>'attempts_cleared')::int = 1`).Scan(&audits)
	if audits != 1 || len(notes.evs) != 1 || notes.evs[0].To[0] != "trainee@crucible.local" || notes.evs[0].Team != "" {
		t.Fatalf("audited once (%d), trainee told by email only: %+v", audits, notes.evs)
	}
	if res, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers())); err != nil || !res.Passed {
		t.Fatalf("attemptable again: %+v %v", res, err)
	}
}
