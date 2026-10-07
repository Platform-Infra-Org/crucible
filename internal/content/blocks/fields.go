// Package blocks describes the building blocks of a training repo. Fields documents every YAML key the content
// loader reads (the source of the editor's JSON Schema and hover text and of the Docs reference); catalog.go lists the
// blocks authors insert through forms. content.Load and lint stay the authority on validity: tests keep both in step.
package blocks

import "strings"

// Field describes one YAML key (in Fields) or one form input (in a catalog Block).
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"` // form inputs: id | string | text | number | integer | bool | enum | list | ints | pairs | duration | module | task
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

func num(v float64) *float64 { return &v }

var questionTypes = []string{"single", "multi", "exact", "regex", "order", "match", "terminal", "text", "upload", "signoff"}

// Fields describes every YAML key of the content types, keyed "<Go type>.<key>". TestEveryContentFieldIsDescribed
// fails when a content field has no entry here.
var Fields = map[string]Field{
	"Training.id":              {Required: true, Description: "The training's id: its key in the platform's trainings.yaml. Edits can't change it; progress is stored under it."},
	"Training.title":           {Required: true, Description: "Shown on the Hearth, in the catalog and on every page of the training."},
	"Training.description":     {Description: "One or two sentences for the catalog."},
	"Training.maintainers":     {Description: "Emails of the people who review content edits to this training. Change it in git only."},
	"Training.progression":     {Enum: []string{"linear", "free"}, Default: "linear", Description: "linear: modules unlock in order. free: any module can be opened at any time."},
	"Training.estimated_hours": {Min: num(0), Description: "Rough hours to finish, shown in the catalog."},
	"Training.modules":         {Required: true, Description: "Module folder names under modules/, in the order trainees take them."},

	"Module.title":      {Required: true, Description: "Shown in the training outline."},
	"Module.completion": {Enum: []string{"all_items", "score"}, Default: "all_items", Description: "all_items: every item must be done. score: the module completes when the trainee's share of points reaches threshold."},
	"Module.threshold":  {Min: num(0), Max: num(1), Description: "Only with completion: score. The share of points needed, above 0 and at most 1 (0.7 = 70%)."},
	"Module.items":      {Required: true, Description: "The module's readings, quiz and lab, in order. Each entry is one of reading: <file>, quiz: quiz.yaml or lab: <folder>."},

	"Quiz.pass_threshold": {Min: num(0), Max: num(1), Default: "0.8", Description: "Share of points needed to pass (default 0.8)."},
	"Quiz.max_attempts":   {Min: num(0), Description: "Attempts allowed; 0 or unset means unlimited."},
	"Quiz.cooldown":       {Description: "Wait between attempts, e.g. 10m. Unset means none."},
	"Quiz.questions":      {Description: "The questions. Terminal questions are answered inside the module's lab."},

	"Question.id":             {Required: true, Description: "Unique within the quiz. Lab tasks name terminal questions by it."},
	"Question.type":           {Required: true, Enum: questionTypes, Description: "single, multi, exact, regex, order and match are scored at once; terminal is checked by a script in the lab; text, upload and signoff are scored by a person on the Anvil."},
	"Question.prompt":         {Required: true, Description: "The question, in Markdown."},
	"Question.options":        {Description: "single and multi: the choices. order: the items in their correct order (trainees see them shuffled)."},
	"Question.pairs":          {Description: "match: [left, right] pairs in their correct matching."},
	"Question.answer":         {Description: "single: the index of the right option (0 is the first). multi: a list of indexes. exact: the expected text. regex: a pattern the whole answer must match."},
	"Question.case_sensitive": {Description: "exact and regex: compare letter case too (default: ignore it)."},
	"Question.points":         {Min: num(0), Default: "1", Description: "Points the question is worth (default 1)."},
	"Question.check":          {Description: "terminal: the check script, relative to the module's lab folder."},
	"Question.run_in":         {Description: "terminal: the service the check runs in; defaults to the first terminal's service."},
	"Question.rubric":         {Description: "text and upload: what the scorer looks for. Never shown to trainees."},

	"Lab.id":           {Required: true, Description: "The lab's id."},
	"Lab.runtime":      {Required: true, Enum: []string{"local", "cluster", "aws"}, Description: "local: Docker on the trainee's laptop. cluster: Crucible's Kubernetes cluster. aws: a shared AWS account, after cost approval."},
	"Lab.ttl":          {Description: "How long the lab may run, e.g. 1h; unset uses the program's default."},
	"Lab.idle_timeout": {Description: "Stop the lab after this long without terminal activity."},
	"Lab.idle_warning": {Default: "5m", Description: "Warn the trainee this long before the idle stop (default 5m); shorter than idle_timeout."},
	"Lab.task_order":   {Enum: []string{"linear", "free"}, Default: "linear", Description: "linear: tasks unlock in order. free: any order."},
	"Lab.hint_cost":    {Min: num(0), Description: "Points a hint costs unless the hint sets its own cost."},
	"Lab.compose":      {Default: "compose.yaml", Description: "local and cluster: the Docker Compose file, relative to the lab folder."},
	"Lab.terminals":    {Required: true, Description: "The terminals the trainee gets, each attached to a service."},
	"Lab.setup":        {Description: "A script run once when the lab starts."},
	"Lab.tasks":        {Required: true, Description: "The lab's tasks."},
	"Lab.aws":          {Description: "runtime: aws only: the region and the hourly cost ceiling."},

	"AWSConfig.region":         {Required: true, Description: "AWS region for the lab's resources, e.g. eu-west-1."},
	"AWSConfig.max_hourly_usd": {Required: true, Min: num(0), Description: "The most the lab's resources may cost per hour, in US dollars; above 0."},

	"Terminal.name":    {Required: true, Description: "The terminal tab's label; unique within the lab."},
	"Terminal.service": {Required: true, Description: "The compose service (workspace for aws labs) the terminal opens in."},

	"Script.script":  {Required: true, Description: "The script's path, relative to the lab folder. It must be executable."},
	"Script.run_in":  {Required: true, Description: "The service the script runs in."},
	"Script.timeout": {Description: "Longest run time (default 30s for checks, 60s for setup)."},

	"Task.id":           {Required: true, Description: "Unique within the lab."},
	"Task.instructions": {Required: true, Description: "Markdown file with the task's instructions, relative to the lab folder."},
	"Task.check":        {Description: "A script that exits 0 when the task is done."},
	"Task.setup":        {Description: "A script run when the task starts, e.g. to break something on purpose."},
	"Task.quiz":         {Description: "The id of a terminal question in the module's quiz.yaml that scores this task."},
	"Task.points":       {Min: num(0), Default: "1", Description: "Points the task is worth (default 1, or the question's points with quiz)."},
	"Task.human_review": {Description: "A person scores the task on the Anvil instead of a check."},
	"Task.rubric":       {Description: "human_review only: what the scorer looks for. Never shown to trainees."},
	"Task.hints":        {Description: "Hints the trainee can reveal, each costing points."},

	"Hint.text": {Description: "The hint, in Markdown. Use either text or file."},
	"Hint.file": {Description: "A Markdown file with the hint, relative to the lab folder."},
	"Hint.cost": {Min: num(0), Description: "Points this hint costs; defaults to the lab's hint_cost. At most the task's points."},
}

func init() {
	for k, f := range Fields {
		f.Name = k[strings.LastIndex(k, ".")+1:]
		Fields[k] = f
	}
}
