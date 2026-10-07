// Package config loads the platform repo: teams, roles, programs, trainings registry.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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
	DefaultTheme      string               `yaml:"default_theme"`
	CostTiers         *CostTiers           `yaml:"cost_tiers"`
	ClusterUSDPerHour *float64             `yaml:"cluster_usd_per_hour"` // rate card for cluster labs (spec §9.1); 0 = free on the node; unset = cluster labs unavailable
	EscalationHours   float64              `yaml:"escalation_hours"`     // default 4, counted inside the program's schedule
	Schedules         map[string]*Schedule `yaml:"schedules"`
	Ranks             RankThresholds       `yaml:"ranks"` // forge rank thresholds; missing keys take DefaultRanks
	Quotes            []string             `yaml:"-"`
}

// CostTiers routes lab requests by estimate (spec §9.1). There are no built-in defaults: platform.yaml must set them.
type CostTiers struct {
	AutoApproveUSD float64 `yaml:"auto_approve_usd" json:"auto_approve_usd"`
	Tier1USD       float64 `yaml:"tier1_usd" json:"tier1_usd"`
	Tier2USD       float64 `yaml:"tier2_usd" json:"tier2_usd"`
}

// Validate checks the tier ordering.
func (t *CostTiers) Validate() error {
	if t.AutoApproveUSD < 0 || t.Tier1USD <= 0 || t.Tier1USD < t.AutoApproveUSD || t.Tier2USD < t.Tier1USD {
		return errors.New("cost_tiers must satisfy 0 <= auto_approve_usd <= tier1_usd <= tier2_usd and tier1_usd > 0")
	}
	return nil
}

// Fill applies the rank defaults to unset thresholds and checks their order.
func (r *RankThresholds) Fill() error { return r.fill() }

// RankThresholds are the % of enrolled training completed at which each forge rank is earned (spec §7). Ore is 0.
type RankThresholds struct {
	Ingot      float64 `yaml:"ingot" json:"ingot"`
	Tempered   float64 `yaml:"tempered" json:"tempered"`
	Blade      float64 `yaml:"blade" json:"blade"`
	Sword      float64 `yaml:"sword" json:"sword"`
	Masterwork float64 `yaml:"masterwork" json:"masterwork"`
}

// DefaultRanks are spec §7's thresholds: Ore 0% → Ingot 20% → Tempered 45% → Blade 75% → Sword 90% → Masterwork 100%.
var DefaultRanks = RankThresholds{Ingot: 20, Tempered: 45, Blade: 75, Sword: 90, Masterwork: 100}

// Steps lists the thresholds from Ore to Masterwork.
func (r RankThresholds) Steps() []float64 {
	return []float64{0, r.Ingot, r.Tempered, r.Blade, r.Sword, r.Masterwork}
}

// fill applies the defaults to unset keys and checks that every rank needs more than the one before.
func (r *RankThresholds) fill() error {
	d := DefaultRanks
	for _, f := range []struct {
		v   *float64
		def float64
	}{{&r.Ingot, d.Ingot}, {&r.Tempered, d.Tempered}, {&r.Blade, d.Blade}, {&r.Sword, d.Sword}, {&r.Masterwork, d.Masterwork}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	s := r.Steps()
	for i := 1; i < len(s); i++ {
		if s[i] <= s[i-1] {
			return fmt.Errorf("each rank must need more than the one before (0 < ingot < tempered < blade < sword < masterwork), got %v", s[1:])
		}
	}
	if r.Masterwork != 100 {
		return errors.New("masterwork must be 100")
	}
	return nil
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
	Version    int64   `yaml:"-" json:"-"`
}

// Validate resolves an unset hard cap to the monthly budget, then checks 0 <= monthly_usd <= hard_cap_usd and that
// both are finite. It is the one budget rule, shared by config.Load and the Postgres write path.
func (b *Budget) Validate() error {
	if math.IsNaN(b.MonthlyUSD) || math.IsInf(b.MonthlyUSD, 0) || math.IsNaN(b.HardCapUSD) || math.IsInf(b.HardCapUSD, 0) {
		return errors.New("budget amounts must be finite numbers")
	}
	if b.HardCapUSD == 0 {
		b.HardCapUSD = b.MonthlyUSD
	}
	if b.MonthlyUSD < 0 || b.HardCapUSD < 0 {
		return errors.New("budget amounts cannot be negative")
	}
	if b.HardCapUSD < b.MonthlyUSD {
		return fmt.Errorf("the hard cap ($%g) is below the monthly budget ($%g); need 0 <= monthly_usd <= hard_cap_usd", b.HardCapUSD, b.MonthlyUSD)
	}
	return nil
}

type TrainingRef struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
}

type Team struct {
	ID       string              `yaml:"-"`
	Version  int64               `yaml:"-"`
	Name     string              `yaml:"name"`
	Leader   string              `yaml:"leader"`
	Seniors  []string            `yaml:"seniors"`
	Members  []string            `yaml:"members"`
	Trainees []string            `yaml:"trainees"`
	Mentors  map[string]string   `yaml:"mentors"` // trainee email → mentor email
	Programs map[string]*Program `yaml:"-"`       // by training id

	Notifications TeamNotifications `yaml:"notifications" json:"-"` // webhook URLs are secrets: never marshalled
	Budget        Budget            `yaml:"-"`                      // from budget.yaml
}

type Program struct {
	Training    string      `yaml:"training"`
	Version     int64       `yaml:"-"`
	PinnedRef   string      `yaml:"pinned_ref"`
	Roles       Roles       `yaml:"roles"`
	Enrolled    []string    `yaml:"enrolled"`
	LabDefaults LabDefaults `yaml:"lab_defaults"`

	ScheduleSpec       ScheduleRef `yaml:"schedule"`             // a schedule name from platform.yaml, or inline windows (spec §4.3)
	Schedule           string      `yaml:"-"`                    // the named schedule; "" = inline or any time
	Inline             *Schedule   `yaml:"-"`                    // inline windows, validated
	BudgetUSDMonth     float64     `yaml:"budget_usd_month"`     // the program's monthly budget and hard cap; 0 = none
	ReviewSelfReported bool        `yaml:"review_self_reported"` // spec §8.2: completed local labs wait for a scorer
}

// ScheduleRef is a program's `schedule:` value: a name, or a mapping with timezone and windows.
type ScheduleRef struct {
	Name   string
	Inline *Schedule
}

func (r *ScheduleRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&r.Name)
	}
	b, err := yaml.Marshal(n) // re-decode strictly, like a named schedule in platform.yaml
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	r.Inline = &Schedule{}
	return dec.Decode(r.Inline)
}

// ProgramSchedule returns the schedule a program runs on, or nil for "any time".
func (p *Platform) ProgramSchedule(team, training string) *Schedule {
	t := p.Teams[team]
	if t == nil || t.Programs[training] == nil {
		return nil
	}
	if pr := t.Programs[training]; pr.Inline != nil {
		return pr.Inline
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

// ValidTrainingID reports whether id can name a training: it becomes a directory name in the content mirror.
func ValidTrainingID(id string) bool {
	return id != "" && filepath.IsLocal(id) && !strings.ContainsAny(id, `/\`)
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
	} else if err := t.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("platform.yaml: %w", err))
	}
	if r := p.Settings.ClusterUSDPerHour; r != nil && (*r < 0 || math.IsNaN(*r) || math.IsInf(*r, 0)) {
		errs = append(errs, errors.New("platform.yaml: cluster_usd_per_hour must be a number >= 0"))
	}
	if p.Settings.EscalationHours == 0 {
		p.Settings.EscalationHours = 4
	}
	if p.Settings.EscalationHours < 0 {
		errs = append(errs, errors.New("platform.yaml: escalation_hours must be positive"))
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "platform.yaml")); len(raw) > 0 {
		var top map[string]yaml.Node // a present-but-empty ranks list is a mistake, not "use the defaults"
		if yaml.Unmarshal(raw, &top) == nil {
			if n, ok := top["ranks"]; ok && (n.Tag == "!!null" || (n.Kind == yaml.MappingNode && len(n.Content) == 0)) {
				errs = append(errs, errors.New("platform.yaml: ranks is empty; list the thresholds or remove the key to use the defaults"))
			}
		}
	}
	if err := p.Settings.Ranks.fill(); err != nil {
		errs = append(errs, fmt.Errorf("platform.yaml: ranks: %w", err))
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
		if !ValidTrainingID(id) {
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
	if err := t.Budget.Validate(); err != nil {
		bad("budget.yaml: %v", err)
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
			bad("programs/%v", err) // ReadFile prefixes the base name
			continue
		}
		pr.Schedule, pr.Inline = pr.ScheduleSpec.Name, pr.ScheduleSpec.Inline
		if pr.Inline != nil {
			if err := pr.Inline.validate(); err != nil {
				bad("programs/%s.yaml: schedule: %v", name, err)
				continue
			}
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
