// Package config holds the configuration types (settings, teams, roles, programs, the trainings registry) and Load,
// which reads them from a platform directory in YAML: the seed format (CRUCIBLE_SEED_DIR, examples/platform).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"crucible/internal/apperr"
	"crucible/internal/yamlx"
)

var Themes = []string{"forge", "anvil", "quench", "damascus", "verdigris", "starmetal", "parchment", "contrast"}

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
	Smith           SmithThresholds      `yaml:"smith"` // smith rank experience thresholds; missing keys take DefaultSmith
	Ranks           RankThresholds       `yaml:"ranks"` // forge rank thresholds; missing keys take DefaultRanks
	Quotes          []string             `yaml:"-"`
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

// SmithThresholds are the experience at which each smith rank is earned (spec 2026-10-11). Bellows Hand is 0.
type SmithThresholds struct {
	Apprentice  int `yaml:"apprentice" json:"apprentice"`
	Journeyman  int `yaml:"journeyman" json:"journeyman"`
	Smith       int `yaml:"smith" json:"smith"`
	MasterSmith int `yaml:"master_smith" json:"master_smith"`
	Grandmaster int `yaml:"grandmaster" json:"grandmaster"`
}

var DefaultSmith = SmithThresholds{Apprentice: 150, Journeyman: 500, Smith: 1200, MasterSmith: 2500, Grandmaster: 5000}

// Steps lists the thresholds from Bellows Hand to Grandmaster.
func (s SmithThresholds) Steps() []int {
	return []int{0, s.Apprentice, s.Journeyman, s.Smith, s.MasterSmith, s.Grandmaster}
}

// Fill applies the defaults to unset keys and checks that every rank needs more than the one before.
func (s *SmithThresholds) Fill() error {
	d := DefaultSmith
	for _, f := range []struct {
		v   *int
		def int
	}{{&s.Apprentice, d.Apprentice}, {&s.Journeyman, d.Journeyman}, {&s.Smith, d.Smith},
		{&s.MasterSmith, d.MasterSmith}, {&s.Grandmaster, d.Grandmaster}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	st := s.Steps()
	for i := 1; i < len(st); i++ {
		if st[i] > 1_000_000_000 {
			return fmt.Errorf("a smith rank needs at most 1,000,000,000 experience, got %d", st[i])
		}
		if st[i] <= st[i-1] {
			return fmt.Errorf("each smith rank must need more than the one before (0 < apprentice < journeyman < smith < master_smith < grandmaster), got %v", st[1:])
		}
	}
	return nil
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

// TrainingRef is where a training's content lives: a repo, the git ref it follows (a branch, a tag or a commit) and the
// folder inside the repo that holds training.yaml ("" is the repo root).
type TrainingRef struct {
	Repo string `json:"repo" yaml:"repo"`
	Ref  string `json:"ref" yaml:"ref"`
	Path string `json:"path" yaml:"path,omitempty"`
}

var (
	refRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	pathRE = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)
)

// CleanRef checks a training's git ref ("" means main): a branch, tag or commit name git can never read as an option,
// a revision expression or a path.
func CleanRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "main", nil
	}
	if len(ref) > 200 || !refRE.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return "", fmt.Errorf("invalid ref %q: use a branch, a tag or a commit id", ref)
	}
	return ref, nil
}

// CleanPath checks the folder inside the repo that holds a training ("" or "/" is the repo root): a relative path
// that stays inside the repo, returned without leading or trailing slashes.
func CleanPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" || p == "." {
		return "", nil
	}
	c := path.Clean(p)
	if len(p) > 200 || c != p || !filepath.IsLocal(c) || !pathRE.MatchString(c) {
		return "", fmt.Errorf("invalid path %q: use a folder inside the repo, like trainings/forge-101", p)
	}
	return c, nil
}

// Team is a team and its members, each holding one tag (leader, senior, junior, trainee). A person is on one team.
type Team struct {
	ID       string              `yaml:"-"`
	Version  int64               `yaml:"-"`
	Name     string              `yaml:"name"`
	Leaders  []string            `yaml:"leaders"`
	Seniors  []string            `yaml:"seniors"`
	Juniors  []string            `yaml:"juniors"`
	Trainees []string            `yaml:"trainees"`
	Programs map[string]*Program `yaml:"-"` // the trainings this team owns, by training id: one run each

	Notifications TeamNotifications `yaml:"notifications" json:"-"` // webhook URLs are secrets: never marshalled
	Budget        Budget            `yaml:"-"`                      // from budget.yaml
}

// Program is a training's one run, under the team that owns it: who holds its roles, the version it runs, its
// schedule, lab defaults and budget.
type Program struct {
	Training    string      `yaml:"training"`
	Version     int64       `yaml:"-"`
	PinnedRef   string      `yaml:"pinned_ref"`
	Access      Access      `yaml:"roles"` // as given: people and groups
	LabDefaults LabDefaults `yaml:"lab_defaults"`

	// Resolved from Access and the teams by Platform.Resolve, never stored.
	Enrolled []string `yaml:"-"` // participants
	Editors  []string `yaml:"-"` // may propose and review content edits: editors and the training's admins
	Roles    Roles    `yaml:"-"`

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

// Access is who holds a training's roles: emails and group refs (see Platform.Members).
type Access struct {
	Participants []string `yaml:"participants" json:"participants"`
	Editors      []string `yaml:"editors" json:"editors"`
	Admins       []string `yaml:"admins" json:"admins"`
}

// Roles are the people a program's permissions go to, resolved from its Access. Manager is the training's admins;
// the owning team's leaders, seniors and juniors manage it too (rbac).
type Roles struct {
	Manager   []string `json:"manager"`   // training admins
	Scorers   []string `json:"scorers"`   // editors and training admins
	Approvers []string `json:"approvers"` // training admins: paid labs up to tier 1
}

type LabDefaults struct {
	TTL          yamlx.Duration `yaml:"ttl"`
	IdleTimeout  yamlx.Duration `yaml:"idle_timeout"`
	MaxExtension yamlx.Duration `yaml:"max_extension"`
}

// Tags are the team tags, strongest first.
var Tags = []string{"leader", "senior", "junior", "trainee"}

// groupTags maps a group suffix to its tag: platform_seniors, all_trainees.
var groupTags = map[string]string{"leaders": "leader", "seniors": "senior", "juniors": "junior", "trainees": "trainee"}

// Tagged returns the members holding tag.
func (t *Team) Tagged(tag string) []string {
	switch tag {
	case "leader":
		return t.Leaders
	case "senior":
		return t.Seniors
	case "junior":
		return t.Juniors
	case "trainee":
		return t.Trainees
	}
	return nil
}

// RoleOf returns the tag email holds in the team (leader, senior, junior, trainee), or "".
func (t *Team) RoleOf(email string) string {
	email = strings.ToLower(email)
	for _, tag := range Tags {
		if slices.Contains(t.Tagged(tag), email) {
			return tag
		}
	}
	return ""
}

// People lists every member, sorted.
func (t *Team) People() []string {
	return slices.Sorted(slices.Values(slices.Concat(t.Leaders, t.Seniors, t.Juniors, t.Trainees)))
}

// Manages reports whether email runs the team's trainings: its leaders, seniors and juniors.
func (t *Team) Manages(email string) bool {
	r := t.RoleOf(email)
	return r != "" && r != "trainee"
}

// ReservedTeamID reports a team id that would read as a group: "all", or one ending in _leaders, _seniors, _juniors
// or _trainees.
func ReservedTeamID(id string) bool {
	if id == "all" {
		return true
	}
	for suffix := range groupTags {
		if strings.HasSuffix(id, "_"+suffix) {
			return true
		}
	}
	return false
}

// SplitGroup reads a group ref (not an email): "all", "all_seniors", "platform", "platform_seniors". team is "" for
// all teams and tag "" for every tag. It does not check that the team exists.
func SplitGroup(ref string) (team, tag string) {
	for suffix, t := range groupTags {
		if base, ok := strings.CutSuffix(ref, "_"+suffix); ok && base != "" {
			if base == "all" {
				return "", t
			}
			return base, t
		}
	}
	if ref == "all" {
		return "", ""
	}
	return ref, ""
}

// Members resolves a ref to people: an email is itself; a group is its members now. An unknown team is nobody.
func (p *Platform) Members(ref string) []string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if strings.Contains(ref, "@") {
		return []string{ref}
	}
	if ref == "" {
		return nil // never "everyone"
	}
	team, tag := SplitGroup(ref)
	var teams []*Team
	if team == "" {
		for _, id := range slices.Sorted(maps.Keys(p.Teams)) {
			teams = append(teams, p.Teams[id])
		}
	} else if t := p.Teams[team]; t != nil {
		teams = []*Team{t}
	}
	var out []string
	for _, t := range teams {
		if tag == "" {
			out = append(out, t.People()...)
		} else {
			out = append(out, t.Tagged(tag)...)
		}
	}
	return out
}

// CheckRef checks a role holder: a valid email, or a group of an existing team (or of all teams).
func (p *Platform) CheckRef(role, ref string) error {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if strings.Contains(ref, "@") {
		return CheckEmail(role, ref)
	}
	if team, _ := SplitGroup(ref); ref == "" || (team != "" && p.Teams[team] == nil) {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s %q is neither an email nor a group (all, all_seniors, <team>, <team>_seniors, … of an existing team)", role, ref))
	}
	return nil
}

// Groups lists every group ref there is now: all, all_<tags>, and each team and its tags.
func (p *Platform) Groups() []string {
	out := []string{"all"}
	for _, tag := range Tags {
		out = append(out, "all_"+tag+"s")
	}
	for _, id := range slices.Sorted(maps.Keys(p.Teams)) {
		out = append(out, id)
		for _, tag := range Tags {
			out = append(out, id+"_"+tag+"s")
		}
	}
	return out
}

// resolve turns refs into a sorted set of people.
func (p *Platform) resolve(refs ...[]string) []string {
	var out []string
	for _, list := range refs {
		for _, r := range list {
			out = append(out, p.Members(r)...)
		}
	}
	slices.Sort(out)
	return nonNilStrings(slices.Compact(out))
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Resolve fills every program's Enrolled, Editors and Roles from its Access and the teams as they are now.
func (p *Platform) Resolve() {
	for _, t := range p.Teams {
		for _, pr := range t.Programs {
			admins := p.resolve(pr.Access.Admins)
			pr.Enrolled = p.resolve(pr.Access.Participants)
			pr.Editors = p.resolve(pr.Access.Editors, pr.Access.Admins)
			pr.Roles = Roles{Manager: admins, Scorers: slices.Clone(pr.Editors), Approvers: slices.Clone(admins)}
		}
	}
}

// Owner returns the team that owns training and its run, or nil, nil when no team owns it.
func (p *Platform) Owner(training string) (*Team, *Program) {
	for _, t := range p.Teams {
		if pr := t.Programs[training]; pr != nil {
			return t, pr
		}
	}
	return nil, nil
}

// TeamOf returns the team email is on, or nil.
func (p *Platform) TeamOf(email string) *Team {
	email = strings.ToLower(email)
	for _, t := range p.Teams {
		if t.RoleOf(email) != "" {
			return t
		}
	}
	return nil
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
	if err := p.Settings.Smith.Fill(); err != nil {
		errs = append(errs, fmt.Errorf("platform.yaml: smith: %w", err))
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
		Trainings map[string]struct {
			TrainingRef `yaml:",inline"`
			Branch      string `yaml:"branch"` // before refs: a branch only
		} `yaml:"trainings"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "trainings.yaml"), &reg, false); err != nil {
		errs = append(errs, err)
	}
	for id, r := range reg.Trainings {
		ref := r.TrainingRef
		if !ValidTrainingID(id) {
			errs = append(errs, fmt.Errorf("trainings.yaml: invalid training id %q", id))
			continue
		}
		if ref.Repo == "" {
			errs = append(errs, fmt.Errorf("trainings.yaml: %s has no repo", id))
			continue
		}
		if ref.Ref == "" {
			ref.Ref = r.Branch
		}
		var err error
		if ref.Ref, err = CleanRef(ref.Ref); err != nil {
			errs = append(errs, fmt.Errorf("trainings.yaml: %s: %w", id, err))
			continue
		}
		if ref.Path, err = CleanPath(ref.Path); err != nil {
			errs = append(errs, fmt.Errorf("trainings.yaml: %s: %w", id, err))
			continue
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
	errs = append(errs, p.Check()...)
	p.Resolve()
	return p, errors.Join(errs...)
}

// Check applies the rules that span teams: one team per person, one owner per training, role holders that resolve.
func (p *Platform) Check() []error {
	var errs []error
	teamOf, owner := map[string]string{}, map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(p.Teams)) {
		t := p.Teams[id]
		for _, e := range t.People() {
			if prev, ok := teamOf[e]; ok {
				errs = append(errs, fmt.Errorf("teams/%s: %s is already on team %s; a person is on one team", id, e, prev))
			}
			teamOf[e] = id
		}
		for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
			if prev, ok := owner[tr]; ok {
				errs = append(errs, fmt.Errorf("teams/%s/programs/%s.yaml: team %s already owns %s; a training has one team", id, tr, prev, tr))
			}
			owner[tr] = id
			a := t.Programs[tr].Access
			for _, g := range []struct {
				role string
				refs []string
			}{{"participant", a.Participants}, {"editor", a.Editors}, {"admin", a.Admins}} {
				for _, r := range g.refs {
					if err := p.CheckRef(g.role, r); err != nil {
						errs = append(errs, fmt.Errorf("teams/%s/programs/%s.yaml: %w", id, tr, err))
					}
				}
			}
		}
	}
	return errs
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
	if ReservedTeamID(id) {
		bad("the id reads as a group (all, or a name ending in _leaders, _seniors, _juniors or _trainees)")
	}
	t.Leaders, t.Seniors, t.Juniors, t.Trainees = lower(t.Leaders), lower(t.Seniors), lower(t.Juniors), lower(t.Trainees)
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
	if len(t.Leaders) == 0 {
		bad("team has no leader")
	}
	seen := map[string]bool{}
	for _, e := range slices.Concat(t.Leaders, t.Seniors, t.Juniors, t.Trainees) {
		if seen[e] {
			bad("%s has more than one tag", e)
		}
		seen[e] = true
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
		sorted := func(in []string) []string { return nonNilStrings(slices.Sorted(slices.Values(lower(in)))) } // as the database reads them back
		pr.Access = Access{Participants: sorted(pr.Access.Participants), Editors: sorted(pr.Access.Editors), Admins: sorted(pr.Access.Admins)}
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
