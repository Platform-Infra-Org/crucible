package learn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
)

func fixture(t *testing.T) (*Service, *auth.User, *auth.User) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	st := &gitsync.State{Platform: plat,
		Trainings:   map[string]*content.Training{"forge-101@abc": forge101(t)},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	leader, _ := store.UpsertUser(ctx, "s2", "leader@crucible.local", "Leo")
	return &Service{DB: pool, QuizSecret: "test-secret", State: func() *gitsync.State { return st }}, trainee, leader
}

// welcomeAnswers converts authored-index answers into the public ids this user would see.
func welcomeAnswers(s *Service, u *auth.User, a map[string]json.RawMessage) map[string]json.RawMessage {
	return asPublic(s.State().Trainings["forge-101@abc"].Module("01-welcome").Quiz, s.seedFor(u.ID, "forge", "forge-101", "01-welcome"), a)
}

func TestProgressionUnlocksModuleTwo(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, _, err := s.Reading(ctx, u, "forge", "forge-101", "02-first-lab", "before-the-lab"); !errors.Is(err, apperr.Locked) {
		t.Fatalf("module 2 must be locked, got %v", err)
	}
	if err := s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work"); err != nil {
		t.Fatal(err)
	}
	res, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers()))
	if err != nil || !res.Passed {
		t.Fatalf("quiz: %+v %v", res, err)
	}
	o, err := s.Outline(ctx, u, "forge", "forge-101")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Modules[0].Complete || o.Modules[1].Locked || o.Percent != 40 {
		t.Fatalf("outline after module 1: %+v", o)
	}
	_, md, err := s.Reading(ctx, u, "forge", "forge-101", "02-first-lab", "before-the-lab")
	if err != nil || !strings.Contains(md, "crucible-agent") {
		t.Fatalf("reading module 2: %v", err)
	}
}

func TestFailedQuizKeepsLockAndPassedQuizStaysPassed(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	bad := correctAnswers()
	bad["q-port"], bad["q-version"] = raw(`"1"`), raw(`"nope"`)
	if res, _ := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); res.Passed {
		t.Fatal("should fail")
	}
	o, _ := s.Outline(ctx, u, "forge", "forge-101")
	if !o.Modules[1].Locked {
		t.Fatal("module 2 must stay locked after a failed quiz")
	}
	_, _ = s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers()))
	_, _ = s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)) // a later failed retry
	o, _ = s.Outline(ctx, u, "forge", "forge-101")
	if o.Modules[1].Locked {
		t.Fatal("a later failed attempt must not re-lock a passed module")
	}
}

func TestNotEnrolledIsForbidden(t *testing.T) {
	ctx := context.Background()
	s, _, leader := fixture(t)
	cards, _ := s.Programs(ctx, leader)
	if len(cards) != 0 {
		t.Fatalf("leader is not enrolled: %+v", cards)
	}
	if _, err := s.Outline(ctx, leader, "forge", "forge-101"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("got %v", err)
	}
}
