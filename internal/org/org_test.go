package org

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/db/dbtest"
)

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestPlatformOnAFreshDatabase(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	p, err := s.Platform(context.Background())
	if err != nil {
		t.Fatalf("Platform: %v", err)
	}
	if p.Settings.DefaultTheme != "forge" {
		t.Errorf("default theme = %q, want forge", p.Settings.DefaultTheme)
	}
	if p.Settings.CostTiers != nil {
		t.Error("a fresh instance must have no cost tiers: paid labs stay refused until an admin sets them")
	}
	if p.Settings.ClusterUSDPerHour != nil {
		t.Error("a fresh instance must have no cluster rate: cluster labs unavailable, never free")
	}
	if p.Settings.EscalationHours != 4 {
		t.Errorf("escalation hours = %v, want the 4-hour default", p.Settings.EscalationHours)
	}
	if got := p.Settings.Ranks; got.Masterwork != 100 || got.Ingot != 20 {
		t.Errorf("ranks = %+v, want the spec defaults", got)
	}
	if len(p.Admins) != 0 || len(p.Teams) != 0 || len(p.Trainings) != 0 {
		t.Errorf("fresh instance must be empty: admins=%v teams=%v trainings=%v", p.Admins, p.Teams, p.Trainings)
	}
}

func TestPlatformReadsTeamsProgramsAndRoles(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge')`)
	mustExec(t, pool, `INSERT INTO team_members (team, email, role) VALUES
		('forge','leader@x','leader'), ('forge','senior@x','senior'), ('forge','trainee@x','trainee')`)
	mustExec(t, pool, `INSERT INTO mentors (team, trainee, mentor) VALUES ('forge','trainee@x','senior@x')`)
	mustExec(t, pool, `INSERT INTO team_budgets (team, monthly_usd, hard_cap_usd) VALUES ('forge', 200, 250)`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo, branch) VALUES ('forge-101','https://git/x.git','main')`)
	mustExec(t, pool, `INSERT INTO schedules (name, timezone, windows) VALUES
		('days','UTC','[{"days":["mon","tue"],"start":"08:00","end":"19:00"}]')`)
	mustExec(t, pool, `INSERT INTO programs (team, training, budget_usd_month, schedule_name, ttl) VALUES ('forge','forge-101', 100, 'days', '2h')`)
	mustExec(t, pool, `INSERT INTO program_roles (team, training, email, role) VALUES ('forge','forge-101','senior@x','scorer')`)
	mustExec(t, pool, `INSERT INTO enrollments (team, training, email) VALUES ('forge','forge-101','trainee@x')`)

	p, err := (&Store{DB: pool}).Platform(ctx)
	if err != nil {
		t.Fatalf("Platform: %v", err)
	}
	team := p.Teams["forge"]
	if team == nil || team.Leader != "leader@x" || len(team.Trainees) != 1 || team.Version != 1 {
		t.Fatalf("team = %+v", team)
	}
	if team.Mentors["trainee@x"] != "senior@x" {
		t.Errorf("mentors = %v", team.Mentors)
	}
	if team.Budget.MonthlyUSD != 200 || team.Budget.HardCapUSD != 250 || team.Budget.Version != 1 {
		t.Errorf("budget = %+v", team.Budget)
	}
	prog := team.Programs["forge-101"]
	if prog == nil || prog.Training != "forge-101" || prog.Version != 1 {
		t.Fatalf("program = %+v", prog)
	}
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "senior@x" {
		t.Errorf("roles = %+v", prog.Roles)
	}
	if len(prog.Roles.Manager) != 1 || prog.Roles.Manager[0] != "leader@x" {
		t.Errorf("manager should default to the leader: %+v", prog.Roles)
	}
	if len(prog.Enrolled) != 1 || prog.Enrolled[0] != "trainee@x" {
		t.Errorf("enrolled = %v", prog.Enrolled)
	}
	if prog.LabDefaults.TTL.D().Hours() != 2 {
		t.Errorf("ttl = %v", prog.LabDefaults.TTL.D())
	}
	if p.ProgramSchedule("forge", "forge-101") == nil {
		t.Error("named schedule did not resolve")
	}
}
