package org

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

// exported builds a configured instance with one trainee's learning history and returns its export.
func exported(t *testing.T) (*Store, []byte) {
	t.Helper()
	ctx := context.Background()
	p, err := config.Load(writePlatformYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: dbtest.New(t)}
	if ok, err := s.Seed(ctx, p); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	mustExec(t, s.DB, `INSERT INTO users (sub, email, name, theme, avatar) VALUES ('kc-1', 'tr@x', 'Tra Inee', 'anvil', 'flame'), ('kc-2', 'l@x', 'Lea Der', '', '')`)
	mustExec(t, s.DB, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at, last_activity_at,
		ttl_s, idle_timeout_s, idle_warning_s, max_extension_s) SELECT 'lab1', id, 't', 'a', 'm1', 'abc', 'local', 'destroyed', now(), now(), 1, 1, 1, 1
		FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO item_progress (user_id, team, training, module, item, status, score) SELECT id, 't', 'a', 'm1', 'r1', 'complete', 1 FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO quiz_attempts (user_id, team, training, module, sha, answers, score, max_score, passed)
		SELECT id, 't', 'a', 'm1', 'abc', '{"q1":["b"]}', 8, 10, true FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, lab_id, qtype, prompt, rubric, max_points,
		answer, files, file_keys, status, points, feedback, scored_by, scored_at)
		SELECT id, 't', 'a', 'm1', 'abc', 'task', 't1', 'lab1', 'upload', 'Show it', 'secret rubric', 5, 'done', '[{"name":"a.png","size":3}]',
		'{k1}', 'scored', 4, 'good', 's1@x', now() FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO ranks (user_id, level, seen) SELECT id, 2, true FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO badges (user_id, training, team) SELECT id, 'a', 't' FROM users WHERE email = 'tr@x'`)
	mustExec(t, s.DB, `INSERT INTO notification_mutes (user_id, kind) SELECT id, 'digest' FROM users WHERE email = 'l@x'`)
	mustExec(t, s.DB, `INSERT INTO sessions (id, user_id, expires_at) SELECT 'sess', id, now() + interval '1 day' FROM users WHERE email = 'tr@x'`)
	b, err := s.Export(ctx, "boss@x")
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

// history is the learning history as the API sees it: by email, without the instance's own ids.
func history(t *testing.T, s *Store) string {
	t.Helper()
	var out string
	mustScan(t, s.DB, `SELECT jsonb_build_object(
		'users', (SELECT jsonb_agg(jsonb_build_array(email, name, theme, calm_motion, avatar) ORDER BY email) FROM users),
		'items', (SELECT jsonb_agg(to_jsonb(x) - 'user_id' - 'updated_at' || jsonb_build_object('email', u.email)) FROM item_progress x JOIN users u ON u.id = x.user_id),
		'quiz', (SELECT jsonb_agg(to_jsonb(x) - 'user_id' - 'id' - 'created_at' || jsonb_build_object('email', u.email)) FROM quiz_attempts x JOIN users u ON u.id = x.user_id),
		'subs', (SELECT jsonb_agg(to_jsonb(x) - 'user_id' - 'id' - 'lab_id' - 'created_at' - 'scored_at' || jsonb_build_object('email', u.email)) FROM submissions x JOIN users u ON u.id = x.user_id),
		'ranks', (SELECT jsonb_agg(jsonb_build_array(u.email, x.level, x.seen)) FROM ranks x JOIN users u ON u.id = x.user_id),
		'badges', (SELECT jsonb_agg(jsonb_build_array(u.email, x.training, x.team)) FROM badges x JOIN users u ON u.id = x.user_id),
		'mutes', (SELECT jsonb_agg(jsonb_build_array(u.email, x.kind)) FROM notification_mutes x JOIN users u ON u.id = x.user_id))::text`, &out)
	return out
}

// A configured instance with progress exports and imports into an empty database, and the second instance holds
// the same configuration and the same history, keyed by email.
func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, file := exported(t)
	if !json.Valid(file) || !strings.HasPrefix(string(file), `{"crucible_export":1,`) {
		t.Fatalf("the file opens with its version: %.80s", file)
	}
	if strings.Contains(string(file), "kc-1") || strings.Contains(string(file), `"sess"`) {
		t.Error("the export carries no OIDC sub and no session")
	}
	var n int
	mustScan(t, src.DB, `SELECT count(*) FROM audit_log WHERE action = 'org.export' AND actor = 'boss@x'`, &n)
	if n != 1 {
		t.Errorf("each export is audited: %d rows", n)
	}

	dst := &Store{DB: dbtest.New(t)}
	if _, err := dst.SeedAdmin(ctx, "new-boss@x"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, dst.DB, `INSERT INTO users (sub, email, name) VALUES ('cognito-9', 'new-boss@x', 'New Boss')`)
	if err := dst.Import(ctx, "new-boss@x", file); err != nil {
		t.Fatal(err)
	}

	want, err := src.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want.Admins = append([]string{"boss@x"}, "new-boss@x") // the importing admin stays an admin
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imported configuration differs\n got: %s\nwant: %s", dump(got), dump(want))
	}
	var roles int
	mustScan(t, dst.DB, `SELECT count(*) FROM program_roles WHERE training = 'b'`, &roles)
	if roles != 0 {
		t.Errorf("default roles stay unset after an import: %d rows", roles)
	}
	var sub string
	mustScan(t, dst.DB, `SELECT sub FROM users WHERE email = 'new-boss@x'`, &sub)
	if sub != "cognito-9" {
		t.Errorf("someone already signed in here keeps their own row, got sub %q", sub)
	}
	mustExec(t, dst.DB, `DELETE FROM users WHERE email = 'new-boss@x'`) // has no history; the rest must match exactly
	if h, w := history(t, dst), history(t, src); h != w {
		t.Errorf("imported history differs\n got: %s\nwant: %s", h, w)
	}
	mustScan(t, dst.DB, `SELECT sub FROM users WHERE email = 'tr@x'`, &sub)
	if sub != "import:tr@x" {
		t.Errorf("an imported person waits for their first login with a placeholder sub, got %q", sub)
	}
	mustScan(t, dst.DB, `SELECT count(*) FROM audit_log WHERE action = 'org.import' AND actor = 'new-boss@x'`, &n)
	if n != 1 {
		t.Errorf("the import is audited: %d rows", n)
	}
	// The sequences moved past the imported ids: new rows still insert.
	mustExec(t, dst.DB, `INSERT INTO quiz_attempts (user_id, team, training, module, sha, answers, score, max_score, passed)
		SELECT id, 't', 'a', 'm1', 'abc', '{}', 0, 10, false FROM users WHERE email = 'tr@x'`)
	if err := dst.SetQuotes(ctx, "new-boss@x", []string{"three"}); err != nil {
		t.Fatal(err)
	}
}

func TestImportRefusesNonEmptyInstance(t *testing.T) {
	src, file := exported(t)
	err := src.Import(context.Background(), "boss@x", file)
	if !errors.Is(err, apperr.Conflict) {
		t.Fatalf("import into a configured instance = %v, want a conflict", err)
	}
}

func TestImportRefusesUnknownVersion(t *testing.T) {
	dst := &Store{DB: dbtest.New(t)}
	err := dst.Import(context.Background(), "a@x", []byte(`{"crucible_export":2,"tables":{}}`))
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "version 1") {
		t.Fatalf("unknown version = %v, want it refused naming the version this build reads", err)
	}
}

// A file that fails part-way changes nothing: a truncated one, one with a broken row, one naming a local repo.
func TestImportIsAllOrNothing(t *testing.T) {
	_, file := exported(t)
	dst := &Store{DB: dbtest.New(t)}
	broken := func(table, from, to string) []byte {
		var x map[string]json.RawMessage
		if err := json.Unmarshal(file, &x); err != nil {
			t.Fatal(err)
		}
		var tables map[string]json.RawMessage
		if err := json.Unmarshal(x["tables"], &tables); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(tables[table]), from) {
			t.Fatalf("%s has no %q: %s", table, from, tables[table])
		}
		tables[table] = json.RawMessage(strings.Replace(string(tables[table]), from, to, 1))
		x["tables"], _ = json.Marshal(tables)
		b, _ := json.Marshal(x)
		return b
	}
	for name, f := range map[string][]byte{
		"truncated":       file[:len(file)/2],
		"unknown person":  broken("badges", `"tr@x"`, `"ghost@x"`),
		"local repo":      broken("trainings", `https://git/a.git`, `/etc`),
		"option as repo":  broken("trainings", `https://git/a.git`, `--upload-pack=touch`),
		"bad email":       broken("team_members", `"m@x"`, `"not an email"`),
		"no leader":       broken("team_members", `"leader"`, `"member"`),
		"plain http hook": broken("team_webhooks", `https://hooks/s`, `http://hooks/s`),
	} {
		err := dst.Import(context.Background(), "a@x", f)
		if err == nil {
			t.Errorf("%s: imported", name)
			continue
		}
		var teams, users int
		mustScan(t, dst.DB, `SELECT (SELECT count(*) FROM teams) + (SELECT count(*) FROM trainings) + (SELECT count(*) FROM quotes)`, &teams)
		mustScan(t, dst.DB, `SELECT count(*) FROM users`, &users)
		if teams != 0 || users != 0 {
			t.Fatalf("%s: a failed import left %d config rows and %d users", name, teams, users)
		}
	}
}

// A file exported before people had icons has no avatar field: it imports, and everyone shows their initials.
func TestImportReadsAFileFromBeforeAvatars(t *testing.T) {
	_, file := exported(t)
	var x map[string]json.RawMessage
	var tables map[string]json.RawMessage
	var users []map[string]any
	if json.Unmarshal(file, &x) != nil || json.Unmarshal(x["tables"], &tables) != nil || json.Unmarshal(tables["users"], &users) != nil {
		t.Fatal("unreadable export")
	}
	for _, u := range users {
		delete(u, "avatar")
	}
	tables["users"], _ = json.Marshal(users)
	x["tables"], _ = json.Marshal(tables)
	old, _ := json.Marshal(x)
	dst := &Store{DB: dbtest.New(t)}
	if err := dst.Import(context.Background(), "a@x", old); err != nil {
		t.Fatal(err)
	}
	var avatar string
	mustScan(t, dst.DB, `SELECT avatar FROM users WHERE email = 'tr@x'`, &avatar)
	if avatar != "" {
		t.Errorf("avatar = %q, want initials", avatar)
	}
}
