package org

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/db/dbtest"
)

func TestAddTrainingValidatesIDAndRepo(t *testing.T) {
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "")
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for name, args := range map[string][3]string{
		"empty id":    {"", "https://git/x.git", "main"},
		"path-ish id": {"../x", "https://git/x.git", "main"},
		"slash id":    {"a/b", "https://git/x.git", "main"},
		"no repo":     {"x", "", "main"},
		"file url":    {"x", "file:///git/x.git", "main"},
		"local path":  {"x", "/git/x.git", "main"},
		"dash repo":   {"x", "--upload-pack=evil", "main"},
	} {
		if err := s.AddTraining(ctx, "admin@x", args[0], args[1], args[2]); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: want Invalid, got %v", name, err)
		}
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM trainings`).Scan(&n)
	if n != 0 {
		t.Errorf("refused adds stored %d rows", n)
	}
	if err := s.AddTraining(ctx, "admin@x", "forge-101", "https://git/x.git", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if ref := p.Trainings["forge-101"]; ref.Branch != "main" {
		t.Errorf("branch = %q, want the main default", ref.Branch)
	}
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "1")
	if err := s.AddTraining(ctx, "admin@x", "local", "file:///git/x.git", "main"); err != nil {
		t.Errorf("file url with CRUCIBLE_GIT_ALLOW_FILE=1: %v", err)
	}
}

func TestRemoveTrainingRefusedWhileAProgramUsesIt(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	if err := s.AddTraining(ctx, "admin@x", "forge-101", "https://git/x.git", "main"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge')`)
	mustExec(t, pool, `INSERT INTO programs (team, training) VALUES ('forge','forge-101')`)
	err := s.RemoveTraining(ctx, "admin@x", "forge-101")
	if !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "forge") || !strings.Contains(err.Error(), "forge-101") {
		t.Fatalf("want a Conflict naming team and training, got %v", err)
	}
	// the raw foreign key maps to the same kind
	if _, err := pool.Exec(ctx, `DELETE FROM trainings WHERE id = 'forge-101'`); err == nil {
		t.Fatal("foreign key should block the delete")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'training.remove'`).Scan(&n)
	if n != 0 {
		t.Errorf("refused removal wrote %d audit rows", n)
	}
	mustExec(t, pool, `DELETE FROM programs`)
	if err := s.RemoveTraining(ctx, "admin@x", "forge-101"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTraining(ctx, "admin@x", "forge-101"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("second remove = %v, want NotFound", err)
	}
}

func TestTrainingWritesAreAudited(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	count := func(action string) (n int) {
		s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n)
		return
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.AddTraining(ctx, "Boss@x", "t", "https://git/a.git", "main"))
	must(s.AddTraining(ctx, "boss@x", "t", "https://git/a.git", "")) // same again: nothing changed
	if n := count("training.add"); n != 1 {
		t.Fatalf("repeat add wrote audit rows: %d, want 1", n)
	}
	must(s.AddTraining(ctx, "boss@x", "t", "https://git/b.git", "dev"))
	var detail string
	err := s.DB.QueryRow(ctx, `SELECT detail::text FROM audit_log WHERE action = 'training.add' ORDER BY id DESC LIMIT 1`).Scan(&detail)
	if err != nil || !strings.Contains(detail, "a.git") || !strings.Contains(detail, "b.git") || !strings.Contains(detail, `"dev"`) {
		t.Errorf("repoint detail = %s, %v", detail, err)
	}
	p, _ := s.Platform(ctx)
	if ref := p.Trainings["t"]; ref.Repo != "https://git/b.git" || ref.Branch != "dev" {
		t.Errorf("not repointed: %+v", ref)
	}
	must(s.RemoveTraining(ctx, "boss@x", "t"))
	_ = s.RemoveTraining(ctx, "boss@x", "t")
	if n := count("training.remove"); n != 1 {
		t.Errorf("training.remove rows = %d, want 1", n)
	}
}
