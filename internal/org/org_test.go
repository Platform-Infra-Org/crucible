package org

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/config"
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

func platform(t *testing.T, pool *pgxpool.Pool) (*config.Platform, error) {
	t.Helper()
	return (&Store{DB: pool}).Platform(context.Background())
}

func TestSettingsTiersAndFreeClusterRate(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `UPDATE settings SET auto_approve_usd=1, tier1_usd=5, tier2_usd=25, cluster_usd_per_hour=0`)
	p, err := platform(t, pool)
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Settings.CostTiers; c == nil || c.AutoApproveUSD != 1 || c.Tier1USD != 5 || c.Tier2USD != 25 {
		t.Errorf("tiers = %+v", c)
	}
	if r := p.Settings.ClusterUSDPerHour; r == nil || *r != 0 {
		t.Errorf("a rate of 0 must stay a non-nil 0 (free on the node), got %v", r)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE settings SET tier1_usd=NULL`); err == nil {
		t.Error("partly-set tiers must be rejected")
	}
}

func TestInlineScheduleRoundTripsThroughJSONB(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	mustExec(t, pool, `INSERT INTO team_members VALUES ('t','l@x','leader')`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo) VALUES ('tr','r')`)
	mustExec(t, pool, `INSERT INTO programs (team, training, inline_schedule) VALUES ('t','tr',
		'{"timezone":"Europe/Bucharest","windows":[{"days":["sat"],"start":"09:00","end":"12:00"}]}')`)
	p, err := platform(t, pool)
	if err != nil {
		t.Fatal(err)
	}
	in := p.Teams["t"].Programs["tr"].Inline
	if in == nil || in.Timezone != "Europe/Bucharest" || len(in.Windows) != 1 ||
		!slices.Equal(in.Windows[0].Days, []string{"sat"}) || in.Windows[0].Start != "09:00" || in.Windows[0].End != "12:00" {
		t.Fatalf("inline = %+v", in)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO schedules VALUES ('n','UTC','[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE programs SET schedule_name='n'`); err == nil {
		t.Error("a program with both a named and an inline schedule must be rejected")
	}
}

func TestMembersAreSortedAndNeverNil(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	mustExec(t, pool, `INSERT INTO team_members VALUES ('t','l@x','leader'),('t','s3@x','senior'),('t','s1@x','senior'),
		('t','s2@x','senior'),('t','b@x','trainee'),('t','a@x','trainee')`)
	p, err := platform(t, pool)
	if err != nil {
		t.Fatal(err)
	}
	tm := p.Teams["t"]
	if !slices.Equal(tm.Seniors, []string{"s1@x", "s2@x", "s3@x"}) || !slices.Equal(tm.Trainees, []string{"a@x", "b@x"}) {
		t.Errorf("seniors=%v trainees=%v", tm.Seniors, tm.Trainees)
	}
	if tm.Members == nil {
		t.Error("Members must be an empty slice, not nil (JSON null breaks the SPA)")
	}
}

func TestLeaderRules(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	if _, err := platform(t, pool); err == nil || !strings.Contains(err.Error(), "teams/t: team has no leader") {
		t.Errorf("leaderless team: err = %v", err)
	}
	mustExec(t, pool, `INSERT INTO team_members VALUES ('t','a@x','leader')`)
	if _, err := pool.Exec(context.Background(), `INSERT INTO team_members VALUES ('t','b@x','leader')`); err == nil {
		t.Error("a second leader must be rejected")
	}
}

func TestMixedCaseEmailsAreRejected(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo) VALUES ('tr','r')`)
	mustExec(t, pool, `INSERT INTO programs (team, training) VALUES ('t','tr')`)
	for _, q := range []string{
		`INSERT INTO admins VALUES ('A@x')`,
		`INSERT INTO team_members VALUES ('t','A@x','member')`,
		`INSERT INTO team_members VALUES ('t',' a@x','member')`,
		`INSERT INTO mentors VALUES ('t','a@x','B@x')`,
		`INSERT INTO mentors VALUES ('t','A@x','b@x')`,
		`INSERT INTO program_roles VALUES ('t','tr','A@x','scorer')`,
		`INSERT INTO enrollments VALUES ('t','tr','A@x')`,
	} {
		if _, err := pool.Exec(context.Background(), q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
}

func TestWebhooksMustBeHTTPS(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	mustExec(t, pool, `INSERT INTO team_members VALUES ('t','l@x','leader')`)
	mustExec(t, pool, `INSERT INTO team_webhooks VALUES ('t','slack','http://hooks')`)
	if _, err := platform(t, pool); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Errorf("err = %v", err)
	}
}

func TestProgramNeedsARegisteredTraining(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	if _, err := pool.Exec(context.Background(), `INSERT INTO programs (team, training) VALUES ('t','nope')`); err == nil {
		t.Error("program for an unregistered training must be rejected")
	}
}

// The same small org as YAML and as rows must give the same *config.Platform, so the two paths cannot drift.
// writePlatformYAML writes a platform directory that uses every field config.Load reads.
func writePlatformYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		f := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("platform.yaml", `default_theme: anvil
cost_tiers: { auto_approve_usd: 1, tier1_usd: 5, tier2_usd: 25 }
cluster_usd_per_hour: 0
escalation_hours: 6
schedules:
  bh:
    timezone: Europe/Bucharest
    windows:
      - { days: [mon, tue], start: "08:00", end: "19:00" }
`)
	write("admins.yaml", "admins: [boss@x]\n")
	write("quotes.yaml", "quotes: [one, two]\n")
	write("trainings.yaml", "trainings:\n  a: {repo: https://git/a.git, branch: dev}\n  b: {repo: https://git/b.git}\n")
	write("teams/t/team.yaml", `name: T
leader: l@x
seniors: [s1@x, s2@x]
members: [m@x]
trainees: [tr@x]
mentors: { tr@x: s1@x }
notifications: { slack_webhook: "https://hooks/s", teams_webhook: "https://hooks/t" }
`)
	write("teams/t/budget.yaml", "monthly_usd: 200\nhard_cap_usd: 250\n")
	write("teams/t/programs/a.yaml", `training: a
pinned_ref: 0123456789abcdef0123456789abcdef01234567
roles: { manager: [m@x], scorers: [s2@x], approvers: [s1@x] }
enrolled: [tr@x]
lab_defaults: { ttl: 2h, idle_timeout: 30m, max_extension: 45m }
schedule: bh
budget_usd_month: 50
review_self_reported: true
`)
	write("teams/t/programs/b.yaml", `schedule: { timezone: UTC, windows: [ { days: [sat], start: "09:00", end: "12:00" } ] }
`)
	return dir
}

func TestPlatformMatchesConfigLoad(t *testing.T) {
	dir := writePlatformYAML(t)
	want, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	pool := dbtest.New(t)
	for _, q := range []string{
		`UPDATE settings SET default_theme='anvil', auto_approve_usd=1, tier1_usd=5, tier2_usd=25, cluster_usd_per_hour=0, escalation_hours=6`,
		`INSERT INTO schedules VALUES ('bh','Europe/Bucharest','[{"days":["mon","tue"],"start":"08:00","end":"19:00"}]')`,
		`INSERT INTO admins VALUES ('boss@x')`,
		`INSERT INTO quotes (text) VALUES ('one'),('two')`,
		`INSERT INTO trainings VALUES ('a','https://git/a.git','dev'),('b','https://git/b.git','main')`,
		`INSERT INTO teams (id, name) VALUES ('t','T')`,
		`INSERT INTO team_members VALUES ('t','l@x','leader'),('t','s2@x','senior'),('t','s1@x','senior'),('t','m@x','member'),('t','tr@x','trainee')`,
		`INSERT INTO mentors VALUES ('t','tr@x','s1@x')`,
		`INSERT INTO team_webhooks VALUES ('t','slack','https://hooks/s'),('t','teams','https://hooks/t')`,
		`INSERT INTO team_budgets (team, monthly_usd, hard_cap_usd) VALUES ('t',200,250)`,
		`INSERT INTO programs (team, training, pinned_ref, schedule_name, ttl, idle_timeout, max_extension, budget_usd_month, review_self_reported)
			VALUES ('t','a','0123456789abcdef0123456789abcdef01234567','bh','2h','30m','45m',50,true)`,
		`INSERT INTO programs (team, training, inline_schedule) VALUES ('t','b','{"timezone":"UTC","windows":[{"days":["sat"],"start":"09:00","end":"12:00"}]}')`,
		`INSERT INTO program_roles VALUES ('t','a','m@x','manager'),('t','a','s2@x','scorer'),('t','a','s1@x','approver')`,
		`INSERT INTO enrollments VALUES ('t','a','tr@x')`,
	} {
		mustExec(t, pool, q)
	}
	got, err := platform(t, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range got.Teams {
		tm.Version, tm.Budget.Version = 0, 0
		for _, pr := range tm.Programs {
			pr.Version = 0
		}
	}
	if pr := got.Teams["t"].Programs["b"]; !slices.Equal(pr.Roles.Manager, []string{"l@x"}) || !slices.Equal(pr.Roles.Scorers, []string{"s1@x", "s2@x"}) {
		t.Errorf("unset roles must default to the leader and seniors: %+v", pr.Roles)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SQL path and config.Load differ\n got: %s\nwant: %s", dump(got), dump(want))
	}
}

func dump(p *config.Platform) string {
	var b strings.Builder
	b.WriteString(reflectString(p.Settings) + reflectString(p.Admins) + reflectString(p.Trainings))
	for id, tm := range p.Teams {
		b.WriteString(id + reflectString(*tm))
		for k, pr := range tm.Programs {
			b.WriteString(k + reflectString(*pr))
		}
	}
	return b.String()
}

func reflectString(v any) string { return fmt.Sprintf("\n%+v", v) }

// An empty URL is a bad row, not "no webhook": Load skips empty values only because the YAML key is absent.
func TestEmptyWebhookURLIsRejected(t *testing.T) {
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('t','T')`)
	mustExec(t, pool, `INSERT INTO team_members VALUES ('t','l@x','leader')`)
	mustExec(t, pool, `INSERT INTO team_webhooks VALUES ('t','slack','')`)
	if _, err := platform(t, pool); err == nil {
		t.Error("an empty webhook url must be an error")
	}
}

// The jsonb column's keys are a storage contract; the tags must name them on purpose.
func TestScheduleJSONTagsAreTheDocumentedKeys(t *testing.T) {
	for typ, want := range map[reflect.Type]map[string]string{
		reflect.TypeOf(config.Window{}):   {"Days": "days", "Start": "start", "End": "end"},
		reflect.TypeOf(config.Schedule{}): {"Timezone": "timezone", "Windows": "windows"},
	} {
		for field, tag := range want {
			f, ok := typ.FieldByName(field)
			if !ok || f.Tag.Get("json") != tag {
				t.Errorf("%s.%s json tag = %q, want %q", typ.Name(), field, f.Tag.Get("json"), tag)
			}
		}
	}
}
