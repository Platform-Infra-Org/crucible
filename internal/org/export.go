package org

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/config"
)

// Export and import move an instance to a new environment (spec §7): configuration, org data and learning history
// in one JSON file. A person is their email, never their OIDC sub, so a new identity provider (Keycloak → Cognito,
// one pool → another) links each person back to their history at their first login (auth.Store.UpsertUser).
//
// Left out, on purpose: sessions and agent tokens (everyone signs in and pairs again), labs and everything hanging
// off them (events, check and setup runs, terminal transcripts), spend, alerts, reaper findings, the kill switch, the
// audit log and content edits. Uploaded files stay in blob storage: submissions keep their keys, so they open again
// when the new instance uses the same store.
//
// The file is personal data: names, emails, answers, scores, webhook URLs and rubrics.

// ExportVersion is the file format this build writes and reads.
const ExportVersion = 1

type exportFile struct {
	Version     int                        `json:"crucible_export"`
	GeneratedAt time.Time                  `json:"generated_at"`
	Tables      map[string]json.RawMessage `json:"tables"`
}

// table is one section of the file, in insert order: parents before children.
type table struct {
	name   string
	byUser bool   // user_id is written as the person's email
	extra  string // jsonb merged into each row on import
}

var exportTables = []table{
	{name: "settings"}, {name: "schedules"}, {name: "quotes"}, {name: "admins"}, {name: "trainings"},
	{name: "teams"}, {name: "team_members"}, {name: "mentors"}, {name: "team_webhooks"}, {name: "team_budgets"},
	{name: "programs"}, {name: "program_roles"}, {name: "enrollments"},
	{name: "users"},
	{name: "item_progress", byUser: true}, {name: "lab_task_progress", byUser: true}, {name: "quiz_attempts", byUser: true},
	{name: "submissions", byUser: true, extra: `'{"lab_id": null}'`}, // labs stay behind
	{name: "badges", byUser: true}, {name: "ranks", byUser: true}, {name: "hint_reveals", byUser: true},
	{name: "notification_mutes", byUser: true},
}

// Export writes the file, read from one consistent snapshot, and audits who took it.
func (s *Store) Export(ctx context.Context, actor string) ([]byte, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return nil, err
	}
	f := exportFile{Version: ExportVersion, GeneratedAt: time.Now().UTC(), Tables: map[string]json.RawMessage{}}
	counts := map[string]any{}
	for _, t := range exportTables {
		q := fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x)), '[]'), count(*) FROM %s x`, t.name)
		switch {
		case t.name == "users": // one row per email: the oldest, as UpsertUser links a login to
			q = `SELECT coalesce(jsonb_agg(to_jsonb(x)), '[]'), count(*) FROM (SELECT DISTINCT ON (email) email, name, theme,
				calm_motion, avatar, created_at FROM users ORDER BY email, id) x`
		case t.byUser:
			q = fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) - 'user_id' || jsonb_build_object('email', u.email)), '[]'), count(*)
				FROM %s x JOIN users u ON u.id = x.user_id`, t.name)
		}
		var rows string
		var n int
		if err := tx.QueryRow(ctx, q).Scan(&rows, &n); err != nil {
			return nil, fmt.Errorf("export %s: %w", t.name, err)
		}
		f.Tables[t.name], counts[t.name] = json.RawMessage(rows), n
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, s.DB, actor, "org.export", "platform", counts, ""); err != nil {
		return nil, err
	}
	return json.Marshal(f)
}

// Import loads an export into a fresh instance, one with no teams and no trainings, in one transaction: the file
// lands whole or not at all. What it wrote is read back and checked as the admin pages check a save before it
// commits. The importing admin stays an admin; every person in the file waits for their first login.
func (s *Store) Import(ctx context.Context, actor string, b []byte) error {
	var f exportFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return apperr.Wrap(apperr.Invalid, "this is not a Crucible export file: "+err.Error())
	}
	if f.Version != ExportVersion {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("this file is export version %d; this Crucible reads version %d", f.Version, ExportVersion))
	}
	for _, t := range exportTables {
		if f.Tables[t.name] == nil {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("the file has no %s section", t.name))
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams) OR EXISTS (SELECT 1 FROM trainings)`).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return apperr.Wrap(apperr.Conflict, "this instance already has teams or trainings; import only into a fresh one")
	}
	// A fresh instance may have settings, schedules and quotes of its own; the file replaces them.
	if _, err := tx.Exec(ctx, `DELETE FROM quotes; DELETE FROM schedules; DELETE FROM settings`); err != nil {
		return err
	}
	counts := map[string]any{}
	for _, t := range exportTables {
		q := fmt.Sprintf(`INSERT INTO %s SELECT r.* FROM jsonb_array_elements($1::jsonb) e, jsonb_populate_record(NULL::%[1]s, e) r`, t.name)
		switch {
		case t.name == "admins":
			q += ` ON CONFLICT DO NOTHING`
		case t.name == "users": // someone who already signed in here keeps their row
			q = `INSERT INTO users (sub, email, name, theme, calm_motion, avatar, created_at)
				SELECT 'import:' || lower(r.email), lower(r.email), r.name, r.theme, r.calm_motion, coalesce(r.avatar, ''), r.created_at
				FROM jsonb_populate_recordset(NULL::users, $1::jsonb) r WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.email = lower(r.email))`
		case t.byUser: // an email with no user in the file leaves user_id null, and the insert fails
			extra := ""
			if t.extra != "" {
				extra = " || " + t.extra
			}
			q = fmt.Sprintf(`INSERT INTO %s SELECT r.* FROM jsonb_array_elements($1::jsonb) e, jsonb_populate_record(NULL::%[1]s,
				e || jsonb_build_object('user_id', (SELECT min(id) FROM users WHERE email = lower(e->>'email')))%s) r`, t.name, extra)
		}
		tag, err := tx.Exec(ctx, q, string(f.Tables[t.name]))
		if err != nil {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("the %s section does not fit: %v", t.name, err))
		}
		counts[t.name] = tag.RowsAffected()
	}
	for _, t := range []string{"quotes", "quiz_attempts", "submissions"} { // past the imported ids
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%[1]s', 'id'), coalesce(max(id), 0) + 1, false) FROM %[1]s`, t)); err != nil {
			return err
		}
	}
	p, err := (&Store{DB: tx}).Platform(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Invalid, "the file's configuration is not valid: "+err.Error())
	}
	if err := checkImported(p); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, actor, "org.import", "platform", counts, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// checkImported runs the checks the admin pages make on a save that the read-back (Store.Platform) does not.
func checkImported(p *config.Platform) error {
	for id, tr := range p.Trainings {
		if !config.ValidTrainingID(id) {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("invalid training id %q", id))
		}
		if err := checkRepo(tr.Repo); err != nil {
			return fmt.Errorf("training %s: %w", id, err)
		}
	}
	for _, e := range p.Admins {
		if err := config.CheckEmail("admin", e); err != nil {
			return err
		}
	}
	for id, t := range p.Teams {
		if err := checkTeamID(id); err != nil {
			return err
		}
		if _, err := (TeamBody{Name: t.Name, Leader: t.Leader, Seniors: t.Seniors, Members: t.Members, Trainees: t.Trainees,
			Mentors: t.Mentors}).roster(); err != nil {
			return fmt.Errorf("team %s: %w", id, err)
		}
		for tr, pr := range t.Programs {
			for _, e := range slices.Concat(pr.Enrolled, pr.Roles.Manager, pr.Roles.Scorers, pr.Roles.Approvers) {
				if err := config.CheckEmail("program "+tr, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
