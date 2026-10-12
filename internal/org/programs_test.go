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
	"crucible/internal/secrets"
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
	if err := s.AddTraining(context.Background(), "admin@x", id, config.TrainingRef{Repo: "https://git/" + id + ".git", Ref: "main"}); err != nil {
		t.Fatalf("AddTraining: %v", err)
	}
}

func audits(t *testing.T, s *Store, action string) int {
	t.Helper()
	return count(t, s, `SELECT count(*) FROM audit_log WHERE action = '`+action+`'`)
}

func orgFixture(t *testing.T) (*Store, context.Context) {
	box, err := secrets.New("a-long-enough-development-key")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: dbtest.New(t), Secrets: box, SSHDir: sshDir(t.TempDir())}
	mustTeam(t, s, "platform", body("Platform", "lead@x:leader", "senior@x:senior", "new@x:trainee"))
	mustTeam(t, s, "data", body("Data", "dl@x:leader", "ds@x:senior", "dt@x:trainee"))
	mustTraining(t, s, "forge-101")
	return s, context.Background()
}

// A training's roles go to people and groups, from any team. They are stored as given and resolved on every read, so
// a promotion changes who holds a role without touching the training.
func TestEnrollStoresRolesAsGiven(t *testing.T) {
	s, ctx := orgFixture(t)
	roles := config.Access{Participants: []string{"all_trainees", " Guest@Corp "}, Editors: []string{"platform_seniors"}, Admins: []string{"dl@x"}}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(0), Roles: roles}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM training_roles WHERE training = 'forge-101'`); n != 4 {
		t.Errorf("stored roles = %d, want 4", n)
	}
	p, _ := s.Platform(ctx)
	prog := p.Teams["platform"].Programs["forge-101"]
	if !slices.Equal(prog.Access.Participants, []string{"all_trainees", "guest@corp"}) {
		t.Errorf("as given = %v", prog.Access.Participants)
	}
	if !slices.Equal(prog.Enrolled, []string{"dt@x", "guest@corp", "new@x"}) || !slices.Equal(prog.Editors, []string{"dl@x", "senior@x"}) ||
		!slices.Equal(prog.Roles.Approvers, []string{"dl@x"}) {
		t.Errorf("resolved: enrolled %v editors %v approvers %v", prog.Enrolled, prog.Editors, prog.Roles.Approvers)
	}
	mustExec(t, s.DB, `UPDATE team_members SET role = 'senior' WHERE email = 'new@x'`) // a trainee promoted
	p, _ = s.Platform(ctx)
	prog = p.Teams["platform"].Programs["forge-101"]
	if slices.Contains(prog.Enrolled, "new@x") || !slices.Contains(prog.Editors, "new@x") {
		t.Errorf("after a promotion: enrolled %v editors %v", prog.Enrolled, prog.Editors)
	}
	if audits(t, s, "program.enroll") != 1 {
		t.Error("enroll must be audited once")
	}
	// one run per training: another team can't take it too
	if err := s.Enroll(ctx, "admin@x", "data", "forge-101", ProgramBody{BudgetUSDMonth: usd(0)}); !errors.Is(err, apperr.Conflict) {
		t.Errorf("a second team = %v, want Conflict", err)
	}
	if audits(t, s, "program.enroll") != 1 {
		t.Error("a refused enroll must not be audited")
	}
}

func TestSetProgramReplacesRoles(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(0), Roles: config.Access{Participants: []string{"new@x"}}}); err != nil {
		t.Fatal(err)
	}
	b := ProgramBody{BudgetUSDMonth: usd(0), Version: 1, Roles: config.Access{Participants: []string{"data"}, Editors: []string{"Lead@x"}}}
	if err := s.SetProgram(ctx, "lead@x", "platform", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	prog := p.Teams["platform"].Programs["forge-101"]
	if !slices.Equal(prog.Enrolled, []string{"dl@x", "ds@x", "dt@x"}) || !slices.Equal(prog.Editors, []string{"lead@x"}) || len(prog.Roles.Manager) != 0 {
		t.Errorf("enrolled = %v editors = %v admins = %v", prog.Enrolled, prog.Editors, prog.Roles.Manager)
	}
	if prog.Version != 2 {
		t.Errorf("version = %d", prog.Version)
	}
	if err := s.SetProgram(ctx, "admin@x", "platform", "forge-101", b); !errors.Is(err, apperr.Conflict) {
		t.Errorf("stale version = %v, want Conflict", err)
	}
	if err := s.SetProgram(ctx, "admin@x", "platform", "nope", ProgramBody{BudgetUSDMonth: usd(0), Version: 1}); !errors.Is(err, apperr.NotFound) {
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
		"unknown schedule": {ProgramBody{BudgetUSDMonth: usd(0), Schedule: "nights"}, "nights"},
		"both schedules":   {ProgramBody{BudgetUSDMonth: usd(0), Schedule: "nights", InlineSchedule: inline}, "not both"},
		"bad ttl":          {ProgramBody{BudgetUSDMonth: usd(0), TTL: "banana"}, "ttl"},
		"negative idle":    {ProgramBody{BudgetUSDMonth: usd(0), IdleTimeout: "-5m"}, "idle_timeout"},
		"bad extension":    {ProgramBody{BudgetUSDMonth: usd(0), MaxExtension: "2 hours"}, "max_extension"},
		"negative budget":  {ProgramBody{BudgetUSDMonth: usd(-1)}, "negative"},
		"bad ref":          {ProgramBody{BudgetUSDMonth: usd(0), Roles: config.Access{Participants: []string{"not an email"}}}, "not an email"},
		"unknown team":     {ProgramBody{BudgetUSDMonth: usd(0), Roles: config.Access{Editors: []string{"ghost_seniors"}}}, `no team "ghost"`},
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
	if err := s.Enroll(ctx, "admin@x", "platform", "ghost", ProgramBody{BudgetUSDMonth: usd(0)}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown training = %v", err)
	}
	if err := s.Enroll(ctx, "admin@x", "nope", "forge-101", ProgramBody{BudgetUSDMonth: usd(0)}); !errors.Is(err, apperr.NotFound) && !errors.Is(err, apperr.Invalid) {
		t.Errorf("unknown team = %v", err)
	}
	if err := s.SetSchedule(ctx, "admin@x", "days", *inline); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Schedule: "days", TTL: "2h", IdleTimeout: "20m", MaxExtension: "1h", BudgetUSDMonth: usd(50)}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	pr := p.Teams["platform"].Programs["forge-101"]
	if pr.Schedule != "days" || pr.LabDefaults.TTL.D() != 2*time.Hour || pr.BudgetUSDMonth != 50 {
		t.Errorf("program = %+v", pr)
	}
	// SetProgram with an unknown named schedule: refused naming it, no audit
	err := s.SetProgram(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(0), Version: 1, Schedule: "nights"})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "nights") {
		t.Errorf("update with unknown schedule = %v", err)
	}
	if audits(t, s, "program.update") != 0 {
		t.Error("refused update must not be audited")
	}
}

func TestSetPinRecordsTheSHAAndIsAudited(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(0)}); err != nil {
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

func TestPlatformIgnoresAProgramWhoseTrainingWasRemoved(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(0), Roles: config.Access{Participants: []string{"new@x"}}}); err != nil {
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

// An admin gives a training to another team: its run (pin, settings, roles) moves with it. A training no team owned
// gets a fresh run.
func TestSetOwnerMovesTheRun(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{BudgetUSDMonth: usd(7), Roles: config.Access{Participants: []string{"new@x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPin(ctx, "admin@x", "platform", "forge-101", sha1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner(ctx, "admin@x", "forge-101", "data"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if p.Teams["platform"].Programs["forge-101"] != nil {
		t.Fatal("platform still owns it")
	}
	pr := p.Teams["data"].Programs["forge-101"]
	if pr == nil || pr.PinnedRef != sha1 || pr.BudgetUSDMonth != 7 || !slices.Equal(pr.Enrolled, []string{"new@x"}) {
		t.Fatalf("the run moved with it: %+v", pr)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action = 'training.owner' AND detail->>'previous_team' = 'platform'`); n != 1 {
		t.Error("the move is audited with the previous team")
	}
	if err := s.SetOwner(ctx, "admin@x", "forge-101", "data"); err != nil || audits(t, s, "training.owner") != 1 {
		t.Errorf("the same owner again changes nothing: %v", err)
	}
	mustTraining(t, s, "orphan")
	if err := s.SetOwner(ctx, "admin@x", "orphan", "platform"); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.Platform(ctx); p.Teams["platform"].Programs["orphan"] == nil {
		t.Error("a training no team owned gets a run")
	}
	if err := s.SetOwner(ctx, "admin@x", "orphan", "ghost"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown team = %v", err)
	}
}

// Registering a training gives it to the team at once, with an empty run.
func TestCreateTraining(t *testing.T) {
	s, ctx := orgFixture(t)
	if err := s.CreateTraining(ctx, "senior@x", "k8s", config.TrainingRef{Repo: "https://git/k.git", Path: "courses/k8s"}, "platform"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if ref := p.Trainings["k8s"]; ref.Ref != "main" || ref.Path != "courses/k8s" || p.Teams["platform"].Programs["k8s"] == nil {
		t.Fatalf("k8s = %+v, run %v", ref, p.Teams["platform"].Programs["k8s"])
	}
	if err := s.CreateTraining(ctx, "senior@x", "k8s", config.TrainingRef{Repo: "https://git/k.git"}, "platform"); !errors.Is(err, apperr.Conflict) {
		t.Errorf("a taken id = %v, want Conflict", err)
	}
	if err := s.CreateTraining(ctx, "senior@x", "k9s", config.TrainingRef{Repo: "https://git/k.git"}, "ghost"); err == nil {
		t.Error("an unknown team is refused")
	}
	if _, ok := mustPlatform(t, s).Trainings["k9s"]; ok {
		t.Error("the refused create left the training behind")
	}
}

func mustPlatform(t *testing.T, s *Store) *config.Platform {
	t.Helper()
	p, err := s.Platform(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func usd(v float64) *float64 { return &v }

func TestProgramBudgetMustBeSent(t *testing.T) {
	s, ctx := orgFixture(t)
	err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Roles: config.Access{Participants: []string{"new@x"}}})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "budget_usd_month") {
		t.Fatalf("err = %v, want Invalid naming budget_usd_month", err)
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM programs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("programs stored = %d, %v; want none", n, err)
	}
}
