package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// platformDir writes a minimal valid platform repo plus overrides.
func platformDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"platform.yaml":            "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n",
		"trainings.yaml":           "trainings:\n  t1: {repo: file:///nowhere}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	}
	for k, v := range files {
		all[k] = v
	}
	for rel, body := range all {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRankLadderFromConfig(t *testing.T) {
	tiers := "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n"
	p, err := Load(platformDir(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Settings.Ranks.Steps(); !slices.Equal(got, []float64{0, 20, 45, 75, 90, 100}) {
		t.Fatalf("defaults: %v", got)
	}
	p, err = Load(platformDir(t, map[string]string{"platform.yaml": tiers + "ranks: {blade: 70}\n"}))
	if err != nil || !slices.Equal(p.Settings.Ranks.Steps(), []float64{0, 20, 45, 70, 90, 100}) {
		t.Fatalf("one override: %v %v", p.Settings.Ranks.Steps(), err)
	}
	for _, bad := range []string{"ranks: {ingot: 50, tempered: 40}\n", "ranks: {masterwork: 120}\n", "ranks: {masterwork: 95}\n", "ranks: {ingot: -1}\n", "ranks: {blade: 95}\n", "ranks: {}\n", "ranks:\n"} {
		if _, err := Load(platformDir(t, map[string]string{"platform.yaml": tiers + bad})); err == nil || !strings.Contains(err.Error(), "ranks") {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
	if _, err := Load(platformDir(t, map[string]string{"platform.yaml": tiers + "ranks: 0\n"})); err == nil {
		t.Error("ranks: 0 must be rejected")
	}
}

func TestInlineProgramScheduleAndReviewFlag(t *testing.T) {
	prog := "enrolled: [u@x]\nreview_self_reported: true\nschedule:\n  timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	p, err := Load(platformDir(t, map[string]string{"teams/a/programs/t1.yaml": prog}))
	if err != nil {
		t.Fatal(err)
	}
	pr := p.Teams["a"].Programs["t1"]
	if pr.Schedule != "" || pr.Inline == nil || !pr.ReviewSelfReported || p.ProgramSchedule("a", "t1") != pr.Inline {
		t.Fatalf("inline schedule: %+v", pr)
	}
	if !strings.Contains(pr.Inline.String(), "08:00") {
		t.Fatalf("inline schedule is validated and printable: %q", pr.Inline.String())
	}
	for _, sched := range []string{
		"timezone: Mars/Olympus\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]",
		"timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"10:00\", end: \"08:00\"}]",
		"timezone: Europe/Bucharest\n  windows: []",
		"timezone: UTC\n  tz: x\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]",
	} {
		bad := "enrolled: [u@x]\nschedule:\n  " + sched + "\n"
		if _, err := Load(platformDir(t, map[string]string{"teams/a/programs/t1.yaml": bad})); err == nil || !strings.Contains(err.Error(), "programs/t1.yaml") {
			t.Fatalf("a bad inline schedule names programs/t1.yaml: %v", err)
		}
	}
	named, err := Load(platformDir(t, map[string]string{
		"platform.yaml":            "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\nschedules:\n  bh: {timezone: UTC, windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]}\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\nschedule: bh\n"}))
	if err != nil || named.Teams["a"].Programs["t1"].Schedule != "bh" || named.ProgramSchedule("a", "t1") == nil {
		t.Fatalf("named schedule still works: %v", err)
	}
}

func TestClusterRateFromConfig(t *testing.T) {
	tiers := "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n"
	load := func(extra string) (*Platform, error) {
		return Load(platformDir(t, map[string]string{"platform.yaml": tiers + extra}))
	}
	if p, err := load(""); err != nil || p.Settings.ClusterUSDPerHour != nil {
		t.Fatalf("unset stays nil (cluster labs unavailable): %v", err)
	}
	if _, err := load("cluster_usd_per_hour: -1\n"); err == nil || !strings.Contains(err.Error(), "cluster_usd_per_hour") {
		t.Fatalf("negative: %v", err)
	}
	if p, err := load("cluster_usd_per_hour: 0.5\n"); err != nil || p.Settings.ClusterUSDPerHour == nil || *p.Settings.ClusterUSDPerHour != 0.5 {
		t.Fatalf("0.5: %v", err)
	}
	if p, err := load("cluster_usd_per_hour: 0\n"); err != nil || p.Settings.ClusterUSDPerHour == nil || *p.Settings.ClusterUSDPerHour != 0 {
		t.Fatalf("0: %v", err)
	}
}

// The server accepts exactly the themes the web app offers (web/src/theme/theme.ts), so a theme added on one side only
// fails here rather than as a refused save or a missing choice in Settings.
func TestThemeListMatchesTheWebApp(t *testing.T) {
	b, err := os.ReadFile("../../web/src/theme/theme.ts")
	if err != nil {
		t.Fatal(err)
	}
	var web []string
	for _, m := range regexp.MustCompile(`\{ id: '([a-z]+)'`).FindAllStringSubmatch(string(b), -1) {
		web = append(web, m[1])
	}
	if !slices.Equal(web, Themes) {
		t.Errorf("web themes %v, server themes %v", web, Themes)
	}
}
