package org

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
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
		if err := s.AddTraining(ctx, "admin@x", args[0], config.TrainingRef{Repo: args[1], Ref: args[2]}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: want Invalid, got %v", name, err)
		}
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM trainings`).Scan(&n)
	if n != 0 {
		t.Errorf("refused adds stored %d rows", n)
	}
	if err := s.AddTraining(ctx, "admin@x", "forge-101", config.TrainingRef{Repo: "https://git/x.git", Ref: ""}); err != nil {
		t.Fatal(err)
	}
	p, perr := s.Platform(ctx)
	if perr != nil {
		t.Fatal(perr)
	}
	if ref := p.Trainings["forge-101"]; ref.Ref != "main" {
		t.Errorf("ref = %q, want the main default", ref.Ref)
	}
	for _, ok := range []string{"http://h/x.git", "ssh://git@h/x.git", "git://h/x.git", "git@github.com:o/x.git", "HTTPS://h/x"} {
		if err := s.AddTraining(ctx, "admin@x", "net", config.TrainingRef{Repo: ok, Ref: "main"}); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "1")
	for _, bad := range []string{"--upload-pack=evil", "-oProxyCommand=evil", "-x\nfoo"} {
		if err := s.AddTraining(ctx, "admin@x", "dash", config.TrainingRef{Repo: bad, Ref: "main"}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%q with the env var set: want Invalid, got %v", bad, err)
		}
	}
	for _, local := range []string{"./x", "FILE:///x", "repos/x.git", `C:\x`} {
		if err := s.AddTraining(ctx, "admin@x", "loc", config.TrainingRef{Repo: local, Ref: "main"}); err != nil {
			t.Errorf("%s with the env var set: %v", local, err)
		}
	}
	if err := s.AddTraining(ctx, "admin@x", "local", config.TrainingRef{Repo: "file:///git/x.git", Ref: "main"}); err != nil {
		t.Errorf("file url with CRUCIBLE_GIT_ALLOW_FILE=1: %v", err)
	}
}

// Deleting a training removes Crucible's connection to its repository (the repository itself is untouched), its run
// and its roles, in one audited step. Everyone's progress stays.
func TestRemoveTrainingStopsIt(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	if err := s.AddTraining(ctx, "admin@x", "forge-101", config.TrainingRef{Repo: "https://git/x.git", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge'), ('anvil', 'Anvil')`)
	mustExec(t, pool, `INSERT INTO programs (team, training) VALUES ('forge','forge-101')`)
	mustExec(t, pool, `INSERT INTO training_roles (training, ref, role) VALUES ('forge-101','a@x','participant'), ('forge-101','anvil','participant')`)
	mustExec(t, pool, `INSERT INTO users (sub, email) VALUES ('s1', 'a@x')`)
	mustExec(t, pool, `INSERT INTO item_progress (user_id, team, training, module, item, status) SELECT id, 'forge', 'forge-101', 'm', 'r', 'complete' FROM users`)
	if err := s.RemoveTraining(ctx, "admin@x", "forge-101"); err != nil {
		t.Fatal(err)
	}
	var left, progress int
	mustScan(t, pool, `SELECT (SELECT count(*) FROM trainings) + (SELECT count(*) FROM programs) + (SELECT count(*) FROM training_roles)`, &left)
	mustScan(t, pool, `SELECT count(*) FROM item_progress`, &progress)
	if left != 0 || progress != 1 {
		t.Errorf("after delete: %d training/program/role rows left, %d progress rows (want 0 and 1)", left, progress)
	}
	var stopped string
	mustScan(t, pool, `SELECT detail->>'stopped_for' FROM audit_log WHERE action = 'training.remove'`, &stopped)
	if stopped != `["forge"]` {
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
	must(s.AddTraining(ctx, "Boss@x", "t", config.TrainingRef{Repo: "https://git/a.git", Ref: "main"}))
	must(s.AddTraining(ctx, "boss@x", "t", config.TrainingRef{Repo: "https://git/a.git", Ref: ""})) // same again: nothing changed
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
	must(s.AddTraining(ctx, "boss@x", "t", config.TrainingRef{Repo: "https://git/b.git", Ref: "dev"}))
	d := detailOf()
	if d["repo"] != "https://git/b.git" || d["ref"] != "dev" || d["previous_repo"] != "https://git/a.git" || d["previous_ref"] != "main" || d["programs_affected"] != float64(1) {
		t.Errorf("repoint detail = %v", d)
	}
	must(s.AddTraining(ctx, "boss@x", "t", config.TrainingRef{Repo: "https://git/b.git", Ref: "stable"}))
	d = detailOf()
	if _, has := d["programs_affected"]; has || d["ref"] != "stable" || d["previous_ref"] != "dev" || d["previous_repo"] != "https://git/b.git" {
		t.Errorf("ref-only detail = %v", d)
	}
	must(s.AddTraining(ctx, "boss@x", "t", config.TrainingRef{Repo: "https://git/b.git", Ref: "dev"}))
	mustExec(t, s.DB, `DELETE FROM teams`)
	p, perr := s.Platform(ctx)
	if perr != nil {
		t.Fatal(perr)
	}
	if ref := p.Trainings["t"]; ref.Repo != "https://git/b.git" || ref.Ref != "dev" {
		t.Errorf("not repointed: %+v", ref)
	}
	must(s.RemoveTraining(ctx, "boss@x", "t"))
	_ = s.RemoveTraining(ctx, "boss@x", "t")
	if n := count("training.remove"); n != 1 {
		t.Errorf("training.remove rows = %d, want 1", n)
	}
}

// A training can follow a tag or a commit and live in a folder of its repo; both are checked, stored and audited.
func TestAddTrainingRefAndPath(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for name, ref := range map[string]config.TrainingRef{
		"option ref":   {Repo: "https://git/x.git", Ref: "--upload-pack=x"},
		"revision":     {Repo: "https://git/x.git", Ref: "main~1"},
		"escaping dir": {Repo: "https://git/x.git", Path: "../etc"},
		"option dir":   {Repo: "https://git/x.git", Path: "-x"},
	} {
		if err := s.AddTraining(ctx, "admin@x", "t", ref); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: want Invalid, got %v", name, err)
		}
	}
	if err := s.AddTraining(ctx, "admin@x", "t", config.TrainingRef{Repo: "https://git/x.git", Ref: "v1.0", Path: "/courses/t/"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraining(ctx, "admin@x", "t", config.TrainingRef{Repo: "https://git/x.git", Ref: "v1.0", Path: "courses/u"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Trainings["t"]; got != (config.TrainingRef{Repo: "https://git/x.git", Ref: "v1.0", Path: "courses/u"}) {
		t.Fatalf("stored %+v", got)
	}
	var raw []byte
	if err := s.DB.QueryRow(ctx, `SELECT detail FROM audit_log WHERE action = 'training.add' ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"path": "courses/u"`) || !strings.Contains(string(raw), `"previous_path": "courses/t"`) {
		t.Fatalf("audit detail = %s", raw)
	}
}

// The screen never sees a stored repo's credentials, so a source change that leaves the repo empty keeps it.
func TestAddTrainingKeepsRepo(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	secret := "https://bot:s3cret@git.example.com/x.git"
	if err := s.AddTraining(ctx, "admin@x", "t", config.TrainingRef{Repo: secret}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraining(ctx, "admin@x", "t", config.TrainingRef{Ref: "v2", Path: "courses/t"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Trainings["t"]; got != (config.TrainingRef{Repo: secret, Ref: "v2", Path: "courses/t"}) {
		t.Fatalf("stored %+v", got)
	}
	if err := s.AddTraining(ctx, "admin@x", "new", config.TrainingRef{Ref: "main"}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a new training without a repo: want Invalid, got %v", err)
	}
}
