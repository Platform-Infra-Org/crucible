package org

import (
	"context"
	"encoding/json"
	"errors"
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
		"dot path":    {"x", "./x", "main"},
		"FILE upper":  {"x", "FILE:///x", "main"},
		"bare rel":    {"x", "repos/x.git", "main"},
		"bare name":   {"x", "x", "main"},
		"ext":         {"x", "ext::sh -c id", "main"},
		"newline":     {"x", "https://h/x\nfoo", "main"},
		"tab":         {"x", "https://h/x\ty", "main"},
		"windows":     {"x", `C:\x`, "main"},
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
	p, perr := s.Platform(ctx)
	if perr != nil {
		t.Fatal(perr)
	}
	if ref := p.Trainings["forge-101"]; ref.Branch != "main" {
		t.Errorf("branch = %q, want the main default", ref.Branch)
	}
	for _, ok := range []string{"http://h/x.git", "ssh://git@h/x.git", "git://h/x.git", "git@github.com:o/x.git", "HTTPS://h/x"} {
		if err := s.AddTraining(ctx, "admin@x", "net", ok, "main"); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "1")
	for _, bad := range []string{"--upload-pack=evil", "-oProxyCommand=evil", "-x\nfoo"} {
		if err := s.AddTraining(ctx, "admin@x", "dash", bad, "main"); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%q with the env var set: want Invalid, got %v", bad, err)
		}
	}
	for _, local := range []string{"./x", "FILE:///x", "repos/x.git", `C:\x`} {
		if err := s.AddTraining(ctx, "admin@x", "loc", local, "main"); err != nil {
			t.Errorf("%s with the env var set: %v", local, err)
		}
	}
	if err := s.AddTraining(ctx, "admin@x", "local", "file:///git/x.git", "main"); err != nil {
		t.Errorf("file url with CRUCIBLE_GIT_ALLOW_FILE=1: %v", err)
	}
}

// Deleting a training removes Crucible's connection to its repository (the repository itself is untouched) and stops
// it for every team that ran it, in one audited step. Everyone's progress stays.
func TestRemoveTrainingStopsItForEveryTeam(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	if err := s.AddTraining(ctx, "admin@x", "forge-101", "https://git/x.git", "main"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge'), ('anvil', 'Anvil')`)
	mustExec(t, pool, `INSERT INTO programs (team, training) VALUES ('forge','forge-101'), ('anvil','forge-101')`)
	mustExec(t, pool, `INSERT INTO enrollments (team, training, email) VALUES ('forge','forge-101','a@x')`)
	mustExec(t, pool, `INSERT INTO users (sub, email) VALUES ('s1', 'a@x')`)
	mustExec(t, pool, `INSERT INTO item_progress (user_id, team, training, module, item, status) SELECT id, 'forge', 'forge-101', 'm', 'r', 'complete' FROM users`)
	if err := s.RemoveTraining(ctx, "admin@x", "forge-101"); err != nil {
		t.Fatal(err)
	}
	var left, progress int
	mustScan(t, pool, `SELECT (SELECT count(*) FROM trainings) + (SELECT count(*) FROM programs) + (SELECT count(*) FROM enrollments)`, &left)
	mustScan(t, pool, `SELECT count(*) FROM item_progress`, &progress)
	if left != 0 || progress != 1 {
		t.Errorf("after delete: %d training/program/enrollment rows left, %d progress rows (want 0 and 1)", left, progress)
	}
	var stopped string
	mustScan(t, pool, `SELECT detail->>'stopped_for' FROM audit_log WHERE action = 'training.remove'`, &stopped)
	if stopped != `["anvil", "forge"]` {
		t.Errorf("audit names the teams it was stopped for: %s", stopped)
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
	mustExec(t, s.DB, `INSERT INTO teams (id, name) VALUES ('forge', 'F')`)
	mustExec(t, s.DB, `INSERT INTO programs (team, training, pinned_ref) VALUES ('forge','t','abc123')`)
	detailOf := func() map[string]any {
		var raw []byte
		if err := s.DB.QueryRow(ctx, `SELECT detail FROM audit_log WHERE action = 'training.add' ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	must(s.AddTraining(ctx, "boss@x", "t", "https://git/b.git", "dev"))
	d := detailOf()
	if d["repo"] != "https://git/b.git" || d["branch"] != "dev" || d["previous_repo"] != "https://git/a.git" || d["previous_branch"] != "main" || d["programs_affected"] != float64(1) {
		t.Errorf("repoint detail = %v", d)
	}
	must(s.AddTraining(ctx, "boss@x", "t", "https://git/b.git", "stable"))
	d = detailOf()
	if _, has := d["programs_affected"]; has || d["branch"] != "stable" || d["previous_branch"] != "dev" || d["previous_repo"] != "https://git/b.git" {
		t.Errorf("branch-only detail = %v", d)
	}
	must(s.AddTraining(ctx, "boss@x", "t", "https://git/b.git", "dev"))
	mustExec(t, s.DB, `DELETE FROM teams`)
	p, perr := s.Platform(ctx)
	if perr != nil {
		t.Fatal(perr)
	}
	if ref := p.Trainings["t"]; ref.Repo != "https://git/b.git" || ref.Branch != "dev" {
		t.Errorf("not repointed: %+v", ref)
	}
	must(s.RemoveTraining(ctx, "boss@x", "t"))
	_ = s.RemoveTraining(ctx, "boss@x", "t")
	if n := count("training.remove"); n != 1 {
		t.Errorf("training.remove rows = %d, want 1", n)
	}
}
