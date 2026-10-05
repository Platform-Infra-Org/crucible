// Package config loads the platform repo: teams, roles, programs, trainings registry.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"crucible/internal/yamlx"
)

var Themes = []string{"forge", "anvil", "quench", "contrast"}

type Platform struct {
	Settings  Settings
	Admins    []string
	Trainings map[string]TrainingRef
	Teams     map[string]*Team
}

type Settings struct {
	DefaultTheme    string               `yaml:"default_theme"`
	CostTiers       *CostTiers           `yaml:"cost_tiers"`
	EscalationHours float64              `yaml:"escalation_hours"` // default 4, counted inside the program's schedule
	Schedules       map[string]*Schedule `yaml:"schedules"`
	Quotes          []string             `yaml:"-"`
}

// CostTiers routes lab requests by estimate (spec §9.1). There are no built-in defaults: platform.yaml must set them.
type CostTiers struct {
	AutoApproveUSD float64 `yaml:"auto_approve_usd" json:"auto_approve_usd"`
	Tier1USD       float64 `yaml:"tier1_usd" json:"tier1_usd"`
	Tier2USD       float64 `yaml:"tier2_usd" json:"tier2_usd"`
}

// Escalation is how long a lab request may wait at one tier before it moves up.
func (s Settings) Escalation() time.Duration {
	return time.Duration(s.EscalationHours * float64(time.Hour))
}

// TeamNotifications are the team's Slack / Teams incoming webhooks (spec §10). Both must be https.
type TeamNotifications struct {
	SlackWebhook string `yaml:"slack_webhook"`
	TeamsWebhook string `yaml:"teams_webhook"`
}

// Budget is teams/<team>/budget.yaml: the monthly lab budget (80% alert) and the hard cap that blocks requests.
type Budget struct {
	MonthlyUSD float64 `yaml:"monthly_usd" json:"monthly_usd"`
	HardCapUSD float64 `yaml:"hard_cap_usd" json:"hard_cap_usd"` // defaults to monthly_usd; 0 = no cap
}

type TrainingRef struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
}

type Team struct {
	ID       string              `yaml:"-"`
	Name     string              `yaml:"name"`
	Leader   string              `yaml:"leader"`
	Seniors  []string            `yaml:"seniors"`
	Members  []string            `yaml:"members"`
	Trainees []string            `yaml:"trainees"`
	Mentors  map[string]string   `yaml:"mentors"` // trainee email → mentor email
	Programs map[string]*Program `yaml:"-"`       // by training id

	Notifications TeamNotifications `yaml:"notifications"`
	Budget        Budget            `yaml:"-"` // from budget.yaml
}

type Program struct {
	Training    string      `yaml:"training"`
	PinnedRef   string      `yaml:"pinned_ref"`
	Roles       Roles       `yaml:"roles"`
	Enrolled    []string    `yaml:"enrolled"`
	LabDefaults LabDefaults `yaml:"lab_defaults"`

	Schedule       string  `yaml:"schedule"`         // named schedule from platform.yaml; "" = any time
	BudgetUSDMonth float64 `yaml:"budget_usd_month"` // the program's monthly budget and hard cap; 0 = none
}

// ProgramSchedule returns the schedule a program runs on, or nil for "any time".
func (p *Platform) ProgramSchedule(team, training string) *Schedule {
	t := p.Teams[team]
	if t == nil || t.Programs[training] == nil {
		return nil
	}
	return p.Settings.Schedules[t.Programs[training].Schedule]
}

type Roles struct {
	Manager   []string `yaml:"manager"`
	Scorers   []string `yaml:"scorers"`
	Approvers []string `yaml:"approvers"`
}

type LabDefaults struct {
	TTL          yamlx.Duration `yaml:"ttl"`
	IdleTimeout  yamlx.Duration `yaml:"idle_timeout"`
	MaxExtension yamlx.Duration `yaml:"max_extension"`
}

// RoleOf returns leader, senior, member, trainee or "" for an email.
func (t *Team) RoleOf(email string) string {
	email = strings.ToLower(email)
	switch {
	case email == t.Leader:
		return "leader"
	case slices.Contains(t.Seniors, email):
		return "senior"
	case slices.Contains(t.Members, email):
		return "member"
	case slices.Contains(t.Trainees, email):
		return "trainee"
	}
	return ""
}

func Load(dir string) (*Platform, error) {
	p := &Platform{Trainings: map[string]TrainingRef{}, Teams: map[string]*Team{}}
	var errs []error
	if err := yamlx.ReadFile(filepath.Join(dir, "platform.yaml"), &p.Settings, true); err != nil {
		errs = append(errs, err)
	}
	if p.Settings.DefaultTheme == "" {
		p.Settings.DefaultTheme = "forge"
	}
	if !slices.Contains(Themes, p.Settings.DefaultTheme) {
		errs = append(errs, fmt.Errorf("platform.yaml: default_theme must be one of %v", Themes))
	}
	if t := p.Settings.CostTiers; t == nil {
		errs = append(errs, errors.New("platform.yaml: cost_tiers is required (auto_approve_usd, tier1_usd, tier2_usd); Crucible has no built-in defaults"))
	} else if t.AutoApproveUSD < 0 || t.Tier1USD <= 0 || t.Tier1USD < t.AutoApproveUSD || t.Tier2USD < t.Tier1USD {
		errs = append(errs, errors.New("platform.yaml: cost_tiers must satisfy 0 <= auto_approve_usd <= tier1_usd <= tier2_usd and tier1_usd > 0"))
	}
	if p.Settings.EscalationHours == 0 {
		p.Settings.EscalationHours = 4
	}
	if p.Settings.EscalationHours < 0 {
		errs = append(errs, errors.New("platform.yaml: escalation_hours must be positive"))
	}
	for name, s := range p.Settings.Schedules {
		if s == nil {
			errs = append(errs, fmt.Errorf("platform.yaml: schedules.%s is empty", name))
			continue
		}
		if err := s.validate(); err != nil {
			errs = append(errs, fmt.Errorf("platform.yaml: schedules.%s: %w", name, err))
		}
	}

	var admins struct {
		Admins []string `yaml:"admins"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "admins.yaml"), &admins, false); err != nil {
		errs = append(errs, err)
	}
	p.Admins = lower(admins.Admins)

	var reg struct {
		Trainings map[string]TrainingRef `yaml:"trainings"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "trainings.yaml"), &reg, false); err != nil {
		errs = append(errs, err)
	}
	for id, ref := range reg.Trainings {
		if id == "" || !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) { // ids become directory names
			errs = append(errs, fmt.Errorf("trainings.yaml: invalid training id %q", id))
			continue
		}
		if ref.Repo == "" {
			errs = append(errs, fmt.Errorf("trainings.yaml: %s has no repo", id))
			continue
		}
		if ref.Branch == "" {
			ref.Branch = "main"
		}
		p.Trainings[id] = ref
	}

	var quotes struct {
		Quotes []string `yaml:"quotes"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "quotes.yaml"), &quotes, false); err != nil {
		errs = append(errs, err)
	}
	p.Settings.Quotes = quotes.Quotes

	entries, _ := os.ReadDir(filepath.Join(dir, "teams")) // no teams yet is fine
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, terrs := loadTeam(filepath.Join(dir, "teams", e.Name()), e.Name(), p.Trainings, p.Settings.Schedules)
		errs = append(errs, terrs...)
		if t != nil {
			p.Teams[t.ID] = t
		}
	}
	return p, errors.Join(errs...)
}

func loadTeam(dir, id string, trainings map[string]TrainingRef, schedules map[string]*Schedule) (*Team, []error) {
	t := &Team{ID: id, Programs: map[string]*Program{}}
	if err := yamlx.ReadFile(filepath.Join(dir, "team.yaml"), t, true); err != nil {
		return nil, []error{fmt.Errorf("teams/%s: %w", id, err)}
	}
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("teams/%s: "+format, append([]any{id}, a...)...))
	}

	t.Leader = strings.ToLower(t.Leader)
	t.Seniors, t.Members, t.Trainees = lower(t.Seniors), lower(t.Members), lower(t.Trainees)
	mentors := map[string]string{}
	for trainee, mentor := range t.Mentors {
		mentors[strings.ToLower(trainee)] = strings.ToLower(mentor)
	}
	t.Mentors = mentors

	for _, u := range []string{t.Notifications.SlackWebhook, t.Notifications.TeamsWebhook} {
		if u != "" && !strings.HasPrefix(u, "https://") {
			bad("notifications: webhook URLs must start with https://")
		}
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "budget.yaml"), &t.Budget, false); err != nil {
		bad("%v", err)
	}
	if t.Budget.HardCapUSD == 0 {
		t.Budget.HardCapUSD = t.Budget.MonthlyUSD
	}
	if t.Budget.MonthlyUSD < 0 || t.Budget.HardCapUSD < t.Budget.MonthlyUSD {
		bad("budget.yaml: need 0 <= monthly_usd <= hard_cap_usd")
	}

	if t.Leader == "" {
		bad("team has no leader")
	}
	seen := map[string]bool{}
	for _, e := range slices.Concat([]string{t.Leader}, t.Seniors, t.Members, t.Trainees) {
		if seen[e] {
			bad("%s has more than one team role", e)
		}
		seen[e] = true
	}
	for trainee, mentor := range t.Mentors {
		if t.RoleOf(trainee) != "trainee" {
			bad("mentor pairing: %s is not a trainee", trainee)
		}
		if r := t.RoleOf(mentor); r == "" || r == "trainee" {
			bad("mentor pairing: %s must be a non-trainee team member", mentor)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "programs", "*.yaml"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		pr := &Program{}
		if err := yamlx.ReadFile(f, pr, true); err != nil {
			bad("%v", err)
			continue
		}
		if pr.Training == "" {
			pr.Training = name
		}
		if pr.Training != name {
			bad("programs/%s.yaml: training %q must match the file name", name, pr.Training)
			continue
		}
		if _, ok := trainings[pr.Training]; !ok {
			bad("programs/%s.yaml: training %q is not in trainings.yaml", name, pr.Training)
			continue
		}
		pr.Enrolled = lower(pr.Enrolled)
		pr.Roles.Manager, pr.Roles.Scorers, pr.Roles.Approvers = lower(pr.Roles.Manager), lower(pr.Roles.Scorers), lower(pr.Roles.Approvers)
		if len(pr.Roles.Manager) == 0 {
			pr.Roles.Manager = []string{t.Leader}
		}
		if len(pr.Roles.Approvers) == 0 {
			pr.Roles.Approvers = []string{t.Leader}
		}
		if len(pr.Roles.Scorers) == 0 {
			pr.Roles.Scorers = slices.Clone(t.Seniors)
		}
		for _, e := range pr.Enrolled {
			if t.RoleOf(e) == "" {
				bad("programs/%s.yaml: %s is not a member of team %s", name, e, id)
			}
		}
		if pr.Schedule != "" && schedules[pr.Schedule] == nil {
			bad("programs/%s.yaml: unknown schedule %q (define it under schedules in platform.yaml)", name, pr.Schedule)
		}
		if pr.BudgetUSDMonth < 0 {
			bad("programs/%s.yaml: budget_usd_month must not be negative", name)
		}
		t.Programs[pr.Training] = pr
	}
	return t, errs
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}
