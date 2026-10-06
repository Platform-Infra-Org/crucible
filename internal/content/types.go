// Package content loads and validates a training content repo (spec §4.2–4.5).
package content

import (
	"gopkg.in/yaml.v3"

	"crucible/internal/yamlx"
)

type Problem struct{ File, Msg string }

func (p Problem) String() string { return p.File + ": " + p.Msg }

type Training struct {
	ID             string    `yaml:"id"`
	Title          string    `yaml:"title"`
	Description    string    `yaml:"description"`
	Maintainers    []string  `yaml:"maintainers"`
	Progression    string    `yaml:"progression"` // linear | free
	EstimatedHours float64   `yaml:"estimated_hours" json:"estimated_hours"`
	ModuleIDs      []string  `yaml:"modules"`
	Modules        []*Module `yaml:"-"`
	Dir            string    `yaml:"-"`
}

type Module struct {
	ID         string              `yaml:"-"`
	Title      string              `yaml:"title"`
	Completion string              `yaml:"completion"` // all_items (default) | score
	Threshold  float64             `yaml:"threshold"`  // score only: 0 < t <= 1
	RawItems   []map[string]string `yaml:"items"`
	Items      []Item              `yaml:"-"`
	Quiz       *Quiz               `yaml:"-"`
	Lab        *Lab                `yaml:"-"`
	Dir        string              `yaml:"-"`
}

type Item struct {
	Kind  string `json:"kind"` // reading | quiz | lab
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"-"`
}

type Quiz struct {
	PassThreshold float64        `yaml:"pass_threshold"`
	MaxAttempts   int            `yaml:"max_attempts"` // 0 = unlimited
	Cooldown      yamlx.Duration `yaml:"cooldown"`     // 0 = none
	Questions     []*Question    `yaml:"questions"`
}

type Question struct {
	ID            string     `yaml:"id"`
	Type          string     `yaml:"type"` // single multi exact regex order match terminal | text upload signoff
	Prompt        string     `yaml:"prompt"`
	Options       []string   `yaml:"options"`
	Pairs         [][]string `yaml:"pairs"`
	Answer        yaml.Node  `yaml:"answer"`
	CaseSensitive bool       `yaml:"case_sensitive"`
	Points        float64    `yaml:"points"`
	Check         string     `yaml:"check"`  // terminal: script path relative to the module's lab dir
	RunIn         string     `yaml:"run_in"` // terminal: service; defaults to the first terminal's service
	Rubric        string     `yaml:"rubric"`
	Script        *Script    `yaml:"-"` // resolved from Check/RunIn by the lab loader
}

type Lab struct {
	ID          string         `yaml:"id"`
	Runtime     string         `yaml:"runtime"` // local | cluster | aws
	TTL         yamlx.Duration `yaml:"ttl"`     // 0 = use the program default
	IdleTimeout yamlx.Duration `yaml:"idle_timeout"`
	IdleWarning yamlx.Duration `yaml:"idle_warning"`
	TaskOrder   string         `yaml:"task_order"`
	HintCost    float64        `yaml:"hint_cost"`
	Compose     string         `yaml:"compose"`
	Terminals   []Terminal     `yaml:"terminals"`
	Setup       *Script        `yaml:"setup"`
	Tasks       []*Task        `yaml:"tasks"`
	AWS         *AWSConfig     `yaml:"aws"`
	Dir         string         `yaml:"-"`
}

type AWSConfig struct {
	Region       string  `yaml:"region"`
	MaxHourlyUSD float64 `yaml:"max_hourly_usd"`
}

type Terminal struct {
	Name    string `yaml:"name" json:"name"`
	Service string `yaml:"service" json:"service"`
}

type Script struct {
	Script  string         `yaml:"script"`
	RunIn   string         `yaml:"run_in"`
	Timeout yamlx.Duration `yaml:"timeout"`
}

type Task struct {
	ID           string  `yaml:"id"`
	Instructions string  `yaml:"instructions"`
	Check        *Script `yaml:"check"`
	Setup        *Script `yaml:"setup"`
	Quiz         string  `yaml:"quiz"`
	Points       float64 `yaml:"points"`
	HumanReview  bool    `yaml:"human_review"`
	Rubric       string  `yaml:"rubric"` // human_review only: what the scorer looks for; never sent to trainees
	Hints        []*Hint `yaml:"hints"`
}

type Hint struct {
	Text string   `yaml:"text"`
	File string   `yaml:"file"`
	Cost *float64 `yaml:"cost"`
}

func (h *Hint) EffectiveCost(lab *Lab) float64 {
	if h.Cost != nil {
		return *h.Cost
	}
	return lab.HintCost
}

func (t *Training) Module(id string) *Module {
	for _, m := range t.Modules {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (q *Quiz) Question(id string) *Question {
	for _, x := range q.Questions {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func (l *Lab) Task(id string) *Task {
	for _, t := range l.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func IsHuman(questionType string) bool {
	return questionType == "text" || questionType == "upload" || questionType == "signoff"
}

// AssetTypes are the only files served from a training's assets/ (images and fonts).
var AssetTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true, ".woff2": true}
