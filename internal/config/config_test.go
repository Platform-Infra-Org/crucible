package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExamplePlatform(t *testing.T) {
	p, err := Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	team := p.Teams["forge"]
	if team == nil || team.Name != "The Forge" {
		t.Fatalf("team not loaded: %+v", team)
	}
	if got := team.RoleOf("TRAINEE@crucible.local"); got != "trainee" {
		t.Fatalf("RoleOf = %q", got)
	}
	prog := team.Programs["forge-101"]
	if prog == nil {
		t.Fatal("program missing")
	}
	// Spec §5.2 defaults: leader → manager + approver, seniors → scorers.
	if prog.Roles.Manager[0] != "leader@crucible.local" || prog.Roles.Approvers[0] != "leader@crucible.local" {
		t.Fatalf("role defaults: %+v", prog.Roles)
	}
	if prog.Roles.Scorers[0] != "senior@crucible.local" {
		t.Fatalf("scorer default: %+v", prog.Roles)
	}
	if prog.LabDefaults.TTL.D() != 2*time.Hour {
		t.Fatalf("ttl %v", prog.LabDefaults.TTL.D())
	}
	if p.Settings.DefaultTheme != "forge" || len(p.Settings.Quotes) != 3 {
		t.Fatalf("settings %+v", p.Settings)
	}
	if tiers := p.Settings.CostTiers; tiers == nil || tiers.AutoApproveUSD != 0 || tiers.Tier1USD != 5 || tiers.Tier2USD != 25 {
		t.Fatalf("cost tiers %+v", p.Settings.CostTiers)
	}
	if p.Settings.Escalation() != 4*time.Hour || p.Settings.Schedules["business-hours"] == nil {
		t.Fatalf("escalation %v schedules %v", p.Settings.Escalation(), p.Settings.Schedules)
	}
	if team.Budget.MonthlyUSD != 200 || team.Budget.HardCapUSD != 250 {
		t.Fatalf("team budget %+v", team.Budget)
	}
	if p.ProgramSchedule("forge", "forge-101") != nil {
		t.Fatal("forge-101 has no schedule: it runs any time")
	}
}

func copyTree(t *testing.T, src string) string {
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"duplicate role": {"teams/forge/team.yaml",
			"name: F\nleader: a@x\nseniors: [a@x]\ntrainees: []\n", "more than one team role"},
		"unknown training": {"teams/forge/programs/forge-101.yaml",
			"training: forge-999\n", "must match the file name"},
		"enrolled outsider": {"teams/forge/programs/forge-101.yaml",
			"training: forge-101\nenrolled: [stranger@x]\n", "not a member of team"},
		"training id dots":  {"trainings.yaml", "trainings:\n  \"..\": {repo: file:///x}\n", "invalid training id"},
		"training id slash": {"trainings.yaml", "trainings:\n  a/b: {repo: file:///x}\n", "invalid training id"},
		"bad theme":         {"platform.yaml", "default_theme: neon\n", "default_theme"},
		"unknown key":       {"platform.yaml", "default_theme: forge\ncolour: red\n", "colour"},
		"no cost tiers":     {"platform.yaml", "default_theme: forge\n", "cost_tiers is required"},
		"bad cost tiers":    {"platform.yaml", "cost_tiers: {auto_approve_usd: 9, tier1_usd: 5, tier2_usd: 25}\n", "cost_tiers must satisfy"},
		"bad schedule":      {"platform.yaml", "cost_tiers: {tier1_usd: 5, tier2_usd: 25}\nschedules:\n  night: {timezone: UTC, windows: [{days: [mon], start: \"22:00\", end: \"02:00\"}]}\n", "schedules.night"},
		"unknown sched":     {"teams/forge/programs/forge-101.yaml", "training: forge-101\nschedule: night\n", `unknown schedule "night"`},
		"negative budget":   {"teams/forge/programs/forge-101.yaml", "training: forge-101\nbudget_usd_month: -1\n", "budget_usd_month"},
		"http webhook":      {"teams/forge/team.yaml", "name: F\nleader: a@x\nnotifications: {slack_webhook: \"http://hooks.example\"}\n", "https://"},
		"cap below budget":  {"teams/forge/budget.yaml", "monthly_usd: 100\nhard_cap_usd: 50\n", "hard_cap_usd"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyTree(t, "../../examples/platform")
			if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}
