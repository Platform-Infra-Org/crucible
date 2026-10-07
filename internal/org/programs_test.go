package org

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

const sha1 = "0123456789abcdef0123456789abcdef01234567"
const sha2 = "fedcba9876543210fedcba9876543210fedcba98"

func mustTeam(t *testing.T, s *Store, id string, b TeamBody) {
	t.Helper()
	if err := s.CreateTeam(context.Background(), "admin@x", id, b); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
}

func mustTraining(t *testing.T, s *Store, id string) {
	t.Helper()
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "1")
	if err := s.AddTraining(context.Background(), "admin@x", id, "https://git/"+id+".git", "main"); err != nil {
		t.Fatalf("AddTraining: %v", err)
	}
}

func audits(t *testing.T, s *Store, action string) int {
	t.Helper()
	return count(t, s, `SELECT count(*) FROM audit_log WHERE action = '`+action+`'`)
}

func orgFixture(t *testing.T) (*Store, context.Context) {
	s := &Store{DB: dbtest.New(t)}
	mustTeam(t, s, "platform", TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"senior@x"}, Trainees: []string{"new@x"}})
	mustTraining(t, s, "forge-101")
	return s, context.Background()
}

func TestEnrollAppliesRoleDefaults(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	prog := p.Teams["platform"].Programs["forge-101"]
	if len(prog.Roles.Manager) != 1 || prog.Roles.Manager[0] != "lead@x" {
		t.Errorf("manager = %v", prog.Roles.Manager)
	}
	if len(prog.Roles.Approvers) != 1 || prog.Roles.Approvers[0] != "lead@x" {
		t.Errorf("approvers = %v", prog.Roles.Approvers)
	}
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "senior@x" {
		t.Errorf("scorers = %v", prog.Roles.Scorers)
	}
	if n := count(t, s, `SELECT count(*) FROM program_roles`); n != 0 {
		t.Errorf("stored roles = %d: defaults are resolved at read time, never stored", n)
	}
	// the default follows the team: a new leader becomes manager and approver with no program edit
	mustExec(t, s.DB, `UPDATE team_members SET role = 'senior' WHERE team = 'platform' AND email = 'lead@x'`)
	mustExec(t, s.DB, `UPDATE team_members SET role = 'leader' WHERE team = 'platform' AND email = 'senior@x'`)
	p, _ = s.Platform(ctx)
	prog = p.Teams["platform"].Programs["forge-101"]
	if len(prog.Roles.Manager) != 1 || prog.Roles.Manager[0] != "senior@x" || len(prog.Roles.Approvers) != 1 || prog.Roles.Approvers[0] != "senior@x" {
		t.Errorf("after a leader change manager = %v approvers = %v, want senior@x", prog.Roles.Manager, prog.Roles.Approvers)
	}
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "lead@x" {
		t.Errorf("scorers = %v, want the new senior", prog.Roles.Scorers)
	}
	if audits(t, s, "program.enroll") != 1 {
		t.Error("enroll must be audited once")
	}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{}); !errors.Is(err, apperr.Conflict) {
		t.Errorf("second enroll = %v, want Conflict", err)
	}
	if audits(t, s, "program.enroll") != 1 {
		t.Error("a refused enroll must not be audited")
	}
}

func TestSetProgramStoresExplicitRoles(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{}); err != nil {
		t.Fatal(err)
	}
	// an admin overrides the scorers with the leader; an empty list would restore the default instead
	b := ProgramBody{Version: 1, Roles: config.Roles{Manager: []string{"Lead@x"}, Scorers: []string{"lead@x"}, Approvers: []string{"lead@x"}}, Enrolled: []string{" New@x "}}
	if err := s.SetProgram(ctx, "admin@x", "platform", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	prog := p.Teams["platform"].Programs["forge-101"]
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "lead@x" || len(prog.Enrolled) != 1 || prog.Enrolled[0] != "new@x" {
		t.Errorf("scorers = %v enrolled = %v", prog.Roles.Scorers, prog.Enrolled)
	}
	if prog.Version != 2 {
		t.Errorf("version = %d", prog.Version)
	}
	if err := s.SetProgram(ctx, "admin@x", "platform", "forge-101", b); !errors.Is(err, apperr.Conflict) {
		t.Errorf("stale version = %v, want Conflict", err)
	}
	if err := s.SetProgram(ctx, "admin@x", "platform", "nope", ProgramBody{Version: 1}); !errors.Is(err, apperr.NotFound) {
		t.Errorf("missing program = %v, want NotFound", err)
	}
	if audits(t, s, "program.update") != 1 {
		t.Errorf("update audits = %d, want 1: refused saves are not changes", audits(t, s, "program.update"))
	}
}

func TestProgramRefusesUnknownScheduleAndBadLabDefaults(t *testing.T) {
	s, ctx := orgFixture(t)
	inline := &config.Schedule{Timezone: "UTC", Windows: []config.Window{{Days: []string{"mon"}, Start: "08:00", End: "19:00"}}}
	for name, c := range map[string]struct {
		b    ProgramBody
		want string
	}{
		"unknown schedule": {ProgramBody{Schedule: "nights"}, "nights"},
		"both schedules":   {ProgramBody{Schedule: "nights", InlineSchedule: inline}, "not both"},
		"bad ttl":          {ProgramBody{TTL: "banana"}, "ttl"},
		"negative idle":    {ProgramBody{IdleTimeout: "-5m"}, "idle_timeout"},
		"bad extension":    {ProgramBody{MaxExtension: "2 hours"}, "max_extension"},
		"negative budget":  {ProgramBody{BudgetUSDMonth: -1}, "negative"},
		"bad email":        {ProgramBody{Enrolled: []string{"not-an-email"}}, "not-an-email"},
	} {
		err := s.Enroll(ctx, "admin@x", "platform", "forge-101", c.b)
		if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want Invalid mentioning %q", name, err, c.want)
		}
	}
	if audits(t, s, "program.enroll") != 0 || count(t, s, `SELECT count(*) FROM programs`) != 0 {
		t.Error("refused enrolls must leave no program and no audit row")
	}
	// an unknown training is refused too
	if err := s.Enroll(ctx, "admin@x", "platform", "ghost", ProgramBody{}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown training = %v", err)
	}
	if err := s.Enroll(ctx, "admin@x", "nope", "forge-101", ProgramBody{}); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown team = %v", err)
	}
	if err := s.SetSchedule(ctx, "admin@x", "days", *inline); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Schedule: "days", TTL: "2h", IdleTimeout: "20m", MaxExtension: "1h", BudgetUSDMonth: 50}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	pr := p.Teams["platform"].Programs["forge-101"]
	if pr.Schedule != "days" || pr.LabDefaults.TTL.D() != 2*time.Hour || pr.BudgetUSDMonth != 50 {
		t.Errorf("program = %+v", pr)
	}
	// SetProgram with an unknown named schedule: refused naming it, no audit
	err := s.SetProgram(ctx, "admin@x", "platform", "forge-101", ProgramBody{Version: 1, Schedule: "nights"})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "nights") {
		t.Errorf("update with unknown schedule = %v", err)
	}
	if audits(t, s, "program.update") != 0 {
		t.Error("refused update must not be audited")
	}
}

func TestEnrollRefusesSomeoneOutsideTheTeam(t *testing.T) {
	s, ctx := orgFixture(t)
	err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"Stranger@x"}})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "stranger@x") {
		t.Errorf("enrolling a stranger = %v", err)
	}
	err = s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Roles: config.Roles{Scorers: []string{"outsider@x"}}})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "outsider@x") {
		t.Errorf("role for an outsider = %v", err)
	}
	if audits(t, s, "program.enroll") != 0 || count(t, s, `SELECT count(*) FROM programs`) != 0 {
		t.Error("refused enroll left a trace")
	}
}

func TestSetPinRecordsTheSHAAndIsAudited(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"main", "abc", sha1[:39], sha1 + "0", strings.Repeat("g", 40)} {
		if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", bad); !errors.Is(err, apperr.Invalid) {
			t.Errorf("pin %q = %v, want Invalid", bad, err)
		}
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "nope", sha1); !errors.Is(err, apperr.NotFound) {
		t.Errorf("pin on missing program = %v", err)
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", strings.ToUpper(sha1)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", sha1); err != nil { // same commit: nothing changed
		t.Fatal(err)
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", sha2); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if got := p.Teams["platform"].Programs["forge-101"].PinnedRef; got != sha2 {
		t.Errorf("pinned = %q", got)
	}
	if n := audits(t, s, "program.pin"); n != 2 {
		t.Errorf("pin audits = %d, want 2 (the repeat changed nothing)", n)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action='program.pin' AND target='platform/forge-101' AND detail->>'sha'='`+sha2+`' AND detail->>'previous_sha'='`+sha1+`'`); n != 1 {
		t.Error("the log must say which commit the program moved from and to")
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action='program.pin' AND detail->>'sha'='' AND detail->>'previous_sha'='`+sha2+`'`); n != 1 {
		t.Error("clearing the pin must be audited with the previous sha")
	}
	p, _ = s.Platform(ctx)
	if p.Teams["platform"].Programs["forge-101"].PinnedRef != "" {
		t.Error("empty sha must clear the pin")
	}
}

func progressFixture(t *testing.T, s *Store) int64 {
	var uid int64
	if err := s.DB.QueryRow(context.Background(), `INSERT INTO users (sub, email) VALUES ('s1','new@x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
		VALUES ($1,'platform','forge-101','01-foundations','reading','complete',1)`, uid)
	return uid
}

func TestDeleteProgramKeepsLearningHistory(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil {
		t.Fatal(err)
	}
	uid := progressFixture(t, s)
	if err := s.DeleteProgram(ctx, "admin@x", "platform", "forge-101"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM item_progress WHERE user_id = $1`, uid); n != 1 {
		t.Fatalf("progress rows after deleting the program = %d, want 1: a score is the trainee's", n)
	}
	if n := count(t, s, `SELECT count(*) FROM enrollments`) + count(t, s, `SELECT count(*) FROM program_roles`); n != 0 {
		t.Errorf("children left behind = %d", n)
	}
	if audits(t, s, "program.delete") != 1 {
		t.Error("delete must be audited once")
	}
	if err := s.DeleteProgram(ctx, "admin@x", "platform", "forge-101"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("second delete = %v, want NotFound", err)
	}
	if audits(t, s, "program.delete") != 1 {
		t.Error("deleting nothing must not be audited")
	}
}

func TestDeleteTeamKeepsLearningHistoryAndDropsItsPrograms(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil {
		t.Fatal(err)
	}
	uid := progressFixture(t, s)
	if err := s.DeleteTeam(ctx, "admin@x", "platform"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM programs`) + count(t, s, `SELECT count(*) FROM enrollments`) + count(t, s, `SELECT count(*) FROM program_roles`); n != 0 {
		t.Errorf("program rows left = %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM item_progress WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("progress rows = %d, want 1", n)
	}
}

func TestPlatformIgnoresAProgramWhoseTrainingWasRemoved(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil {
		t.Fatal(err)
	}
	// a restored snapshot can lack the foreign key's guarantee; replica mode skips the check
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SET LOCAL session_replication_role = replica`, `DELETE FROM trainings WHERE id = 'forge-101'`} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.Platform(ctx)
	if err != nil {
		t.Fatalf("Platform: %v", err)
	}
	if p.Teams["platform"] == nil || len(p.Teams["platform"].Programs) != 0 {
		t.Errorf("teams = %+v, want the team without the orphaned program", p.Teams["platform"])
	}
}

func TestEnrollStoresExplicitRoles(t *testing.T) {
	s, ctx := orgFixture(t)
	b := ProgramBody{Roles: config.Roles{Manager: []string{"senior@x"}, Scorers: []string{"Lead@x"}, Approvers: []string{"senior@x"}}}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM program_roles WHERE team='platform' AND training='forge-101'`); n != 3 {
		t.Errorf("stored roles = %d, want the 3 named", n)
	}
	p, _ := s.Platform(ctx)
	r := p.Teams["platform"].Programs["forge-101"].Roles
	if !slices.Equal(r.Manager, []string{"senior@x"}) || !slices.Equal(r.Scorers, []string{"lead@x"}) || !slices.Equal(r.Approvers, []string{"senior@x"}) {
		t.Errorf("roles = %+v: explicit roles must replace the defaults", r)
	}
}
