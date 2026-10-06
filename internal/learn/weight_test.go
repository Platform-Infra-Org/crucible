package learn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

func TestCompletionIsWeightedAndHonoursTheScoreRule(t *testing.T) {
	m1 := &content.Module{ID: "m1", Completion: "all_items", Items: []content.Item{{Kind: "reading", ID: "r"}, {Kind: "quiz", ID: "quiz"}},
		Quiz: &content.Quiz{Questions: []*content.Question{{Points: 3}}}}
	m2 := &content.Module{ID: "m2", Completion: "score", Threshold: 0.5, Items: []content.Item{{Kind: "lab", ID: "lab"}},
		Lab: &content.Lab{Tasks: []*content.Task{{Points: 2}, {Points: 2}}}}
	tr := &content.Training{Progression: "linear", Modules: []*content.Module{m1, m2}}

	done, total := completion(tr, progress{"m1/r": "complete"}, scores{"m1/r": 1})
	if done != 1 || total != 8 {
		t.Fatalf("reading 1 + quiz 3 + lab 4 = 8; done %v total %v", done, total)
	}
	if p := percent(tr, progress{"m1/r": "complete"}, scores{"m1/r": 1}); p != 12 {
		t.Fatalf("1/8 = 12%%, got %d", p)
	}
	prog := progress{"m1/r": "complete", "m1/quiz": "complete", "m2/lab": "complete"}
	sc := scores{"m1/r": 1, "m1/quiz": 1, "m2/lab": 0.6}
	o := outline(tr, prog, sc)
	if !o[0].Complete || o[1].Locked || !o[1].Complete {
		t.Fatalf("score rule: a lab at 0.6 forges a 0.5-threshold module: %+v", o)
	}
	if p := percent(tr, prog, sc); p != 100 {
		t.Fatalf("a forged module counts fully: %d", p)
	}
	sc["m2/lab"] = 0.4
	prog["m2/lab"] = "complete"
	if o := outline(tr, prog, sc); o[1].Complete {
		t.Fatal("0.4 is under the threshold")
	}
}

func TestQuizAttemptLimitAndCooldown(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	q := s.State().Trainings["forge-101@abc"].Module("01-welcome").Quiz
	q.MaxAttempts = 2
	bad := correctAnswers()
	bad["q-port"] = raw(`"1"`)
	for i := 0; i < 2; i++ {
		if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers())); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "every attempt") {
		t.Fatalf("third attempt: %v", err)
	}
	v, _ := s.Quiz(ctx, u, "forge", "forge-101", "01-welcome")
	if v.AttemptsLeft == nil || *v.AttemptsLeft != 0 {
		t.Fatalf("view shows no attempts left: %+v", v.AttemptsLeft)
	}
	q.MaxAttempts, q.Cooldown = 0, yamlx.Duration(time.Hour)
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "next attempt") || !strings.Contains(err.Error(), "in 1h)") {
		t.Fatalf("inside the cooldown: %v", err)
	}
	if v, _ := s.Quiz(ctx, u, "forge", "forge-101", "01-welcome"); v.NextAttemptAt == nil {
		t.Fatal("view shows when the next attempt opens")
	}
	if _, err := s.DB.Exec(ctx, `UPDATE quiz_attempts SET created_at = created_at - interval '61 minutes' WHERE user_id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
}

// Two tabs submitting at once must not get past max_attempts.
func TestConcurrentAttemptsRespectTheLimit(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	s.State().Trainings["forge-101@abc"].Module("01-welcome").Quiz.MaxAttempts = 1
	answers := welcomeAnswers(s, u, correctAnswers())
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", answers); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, apperr.Conflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d attempts got through a limit of 1", ok)
	}
}

func TestStanding(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	st, err := s.Standing(ctx, u.ID, "forge", "forge-101")
	if err != nil || st == nil || st.Total != 19 || st.Done != 1 || st.Percent != 5 || st.Status["01-welcome/how-we-work"] != "complete" || len(st.Modules) != 3 {
		t.Fatalf("standing: %+v %v", st, err)
	}
	if st, err := s.Standing(ctx, u.ID, "forge", "nope"); st != nil || err != nil {
		t.Fatalf("unknown program: %+v %v", st, err)
	}
}

func TestPercentReaches100WithFractionalPoints(t *testing.T) {
	for _, pts := range [][]float64{{1, 0.1, 0.1, 0.2}, {1, 0.1, 0.8, 0.9}} {
		m := &content.Module{ID: "m", Completion: "all_items", Lab: &content.Lab{}}
		for _, p := range pts {
			m.Lab.Tasks = append(m.Lab.Tasks, &content.Task{Points: p})
		}
		m.Items = []content.Item{{Kind: "lab", ID: "lab"}}
		prog := progress{"m/lab": "complete"}
		tr := &content.Training{Modules: []*content.Module{m}}
		if p := percent(tr, prog, scores{}); p != 100 {
			t.Fatalf("%v complete = %d%%", pts, p)
		}
	}
}

func TestTerminalQuestionsAreNotQuizWeight(t *testing.T) {
	m := &content.Module{Quiz: &content.Quiz{Questions: []*content.Question{{Type: "single", Points: 2}, {Type: "terminal", Points: 5}}}}
	if w := Weight(m, content.Item{Kind: "quiz"}); w != 2 {
		t.Fatalf("quiz weight %v, want 2", w)
	}
}

func TestScoreRuleIgnoresPendingItems(t *testing.T) {
	m := &content.Module{ID: "m", Completion: "score", Threshold: 0.5, Items: []content.Item{{Kind: "quiz", ID: "quiz"}},
		Quiz: &content.Quiz{Questions: []*content.Question{{Points: 1}}}}
	if moduleComplete(m, progress{"m/quiz": "pending_review"}, scores{"m/quiz": 1}) {
		t.Fatal("a pending quiz must not forge the module")
	}
	if !moduleComplete(m, progress{"m/quiz": "complete"}, scores{"m/quiz": 1}) {
		t.Fatal("a complete quiz at 1.0 forges it")
	}
}
