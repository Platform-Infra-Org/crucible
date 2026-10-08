package blocks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/yamlx"
)

// Block is one entry of the catalog: what it is, the form that adds it, and how the form is written.
type Block struct {
	ID       string            `json:"id"`
	Group    string            `json:"group"`
	Title    string            `json:"title"`
	Summary  string            `json:"summary"`
	Doc      string            `json:"doc,omitempty"`
	FileKind string            `json:"file_kind"`
	Fields   []Field           `json:"fields"`
	Example  string            `json:"example"`
	GitOnly  bool              `json:"git_only,omitempty"`
	Insert   Insert            `json:"-"`
	Sample   map[string]string `json:"-"` // form values for the example and the tests
}

// Insert says how a form is written. Every string is a text/template over the form values plus "labdir" (the module's
// lab folder) and "service" (its first terminal's service). In YAML, values go through q, list, ints or pairs, which
// quote them, so a value is always text and never YAML.
type Insert struct {
	Files    map[string]string // new files: path → content; refused if the file exists
	Appends  []Append          // entries added to YAML files (existing, or created by Files)
	Open     string            // the file to open afterwards
	NeedsLab bool              // the target module must already have a lab
	GitOnly  bool              // shown in the catalog, added in git only (UI edits can't write those files)
	NewRepo  bool              // a whole repo (tests render it into an empty directory)
}

type Append struct {
	File string
	Path []string // yamlx.Insert path
	Item string
	Set  bool
}

type Result struct {
	Ops   []gitsync.Op `json:"ops"`
	Open  string       `json:"open"`
	Line  int          `json:"line"`  // where the insertion starts in Open
	Lines int          `json:"lines"` // how many lines it takes
}

var Groups = []string{"Training", "Module", "Reading", "Quiz", "Lab", "AWS"}

const gitOnlyWhy = "edits in Crucible write only .md, .yaml, .yml and .sh files: copy the example into the repo in git"

func in(name, typ, desc string) Field {
	return Field{Name: name, Type: typ, Required: true, Description: desc}
}
func opt(name, typ, desc string) Field { return Field{Name: name, Type: typ, Description: desc} }
func doc(key, name, typ string, required bool) Field {
	f := Fields[key]
	f.Name, f.Type, f.Required = name, typ, required
	return f
}

var (
	moduleTarget = Field{Name: "module", Type: "module", Required: true, Description: "Which module?"}
	taskTarget   = Field{Name: "task", Type: "task", Required: true, Description: "Which task of the module's lab?"}
	labTask      = "id: {{q .id}}\ninstructions: tasks/{{.id}}.md\n"
	taskDoc      = "# {{.title}}\n\nWhat to do, and how the trainee knows it worked.\n"
)

const (
	localCompose = "services:\n  shell:\n    image: alpine:3.22\n    command: [\"sleep\", \"infinity\"]\n"
	breakFixLab  = "id: {{q .id}}\nruntime: %s\nttl: 1h\nidle_timeout: 20m\nterminals:\n  - {name: shell, service: shell}\ntasks:\n" +
		"  - id: t1-relight\n    instructions: tasks/01-relight.md\n    setup: {script: setup/01-break.sh, run_in: shell}\n" +
		"    check: {script: checks/01-relight.sh, run_in: shell}\n    points: 2\n    hints:\n      - text: \"Read what the setup changed: cat /tmp/forge.conf\"\n"
)

func breakFix(runtime string) map[string]string {
	return map[string]string{
		"modules/{{.module}}/lab/lab.yaml":             fmt.Sprintf(breakFixLab, runtime),
		"modules/{{.module}}/lab/compose.yaml":         localCompose,
		"modules/{{.module}}/lab/tasks/01-relight.md":  "# Relight the forge\n\nThe setup turned the heat off in `/tmp/forge.conf`. Turn it back on.\n",
		"modules/{{.module}}/lab/setup/01-break.sh":    "#!/bin/sh\n# Breaks something on purpose; the task asks the trainee to fix it.\necho 'heat=off' > /tmp/forge.conf\n",
		"modules/{{.module}}/lab/checks/01-relight.sh": "#!/bin/sh\n# Exits 0 when the task is done.\ngrep -q 'heat=on' /tmp/forge.conf\n",
	}
}

func question(typ, title, summary string, extra []Field, body string, sample map[string]string) Block {
	s := map[string]string{"module": "01-welcome", "id": "q-" + typ, "prompt": "Which iron do you strike?"}
	for k, v := range sample {
		s[k] = v
	}
	return Block{ID: "quiz.question." + typ, Group: "Quiz", Title: title, FileKind: "quiz", Summary: summary, Doc: Fields["Question.type"].Description,
		Fields: slices.Concat([]Field{moduleTarget, doc("Question.id", "id", "id", true), doc("Question.prompt", "prompt", "text", true)}, extra, []Field{doc("Question.points", "points", "number", false)}),
		Sample: s,
		Insert: Insert{Open: "modules/{{.module}}/quiz.yaml", Appends: []Append{{File: "modules/{{.module}}/quiz.yaml", Path: []string{"questions"},
			Item: "id: {{q .id}}\ntype: " + typ + "\nprompt: {{q .prompt}}\n" + body + "{{if .points}}points: {{.points}}\n{{end}}"}}},
	}
}

var (
	options      = in("options", "list", "One choice per line.")
	caseField    = doc("Question.case_sensitive", "case_sensitive", "bool", false)
	caseBody     = "{{if eq .case_sensitive \"true\"}}case_sensitive: true\n{{end}}"
	rubric       = doc("Question.rubric", "rubric", "text", true)
	answerString = in("answer", "string", "The expected answer.")
)

// Catalog is every block, in the order the Blocks panel shows them. TestEveryBlockSampleLoadsAndLints inserts each
// Sample into a fixture repo and loads the result.
var Catalog = []Block{
	{ID: "template.training", Group: "Training", Title: "New training repo", FileKind: "training",
		Summary: "A whole training repo: training.yaml and a first module with a reading.",
		Doc:     "Trainings start in git: create a repo with these files, list it in the platform's trainings.yaml, and push.",
		Fields:  []Field{in("id", "id", "The training's id, as trainings.yaml will list it."), in("title", "string", "The training's title."), in("maintainer", "string", "Email of the first maintainer (reviews edits).")},
		Sample:  map[string]string{"id": "forge-999", "title": "Forge 999", "maintainer": "smith@example.com"},
		Insert: Insert{GitOnly: true, NewRepo: true, Open: "training.yaml", Files: map[string]string{
			"training.yaml":                       "id: {{q .id}}\ntitle: {{q .title}}\nmaintainers: [{{q .maintainer}}]\nprogression: linear\nmodules: [01-welcome]\n",
			"modules/01-welcome/module.yaml":      "title: Welcome\nitems:\n  - reading: reading/intro.md\n",
			"modules/01-welcome/reading/intro.md": "# Welcome to {{.title}}\n\nWhat this training forges, and how long it takes.\n",
		}}},

	{ID: "module", Group: "Module", Title: "Module with a reading", FileKind: "module",
		Summary: "A new module folder, added to training.yaml, with a first reading.",
		Fields:  []Field{in("id", "id", "Folder name under modules/, e.g. 02-heat. Modules run in the order training.yaml lists them."), doc("Module.title", "title", "string", true)},
		Sample:  map[string]string{"id": "03-extra", "title": "Extra heat"},
		Insert: Insert{Open: "modules/{{.id}}/module.yaml",
			Files:   map[string]string{"modules/{{.id}}/module.yaml": "title: {{q .title}}\nitems:\n  - reading: reading/intro.md\n", "modules/{{.id}}/reading/intro.md": "# {{.title}}\n\nWrite the reading here.\n"},
			Appends: []Append{{File: "training.yaml", Path: []string{"modules"}, Item: "{{q .id}}"}}}},
	{ID: "template.module-quiz", Group: "Module", Title: "Module with a reading and a quiz", FileKind: "module",
		Summary: "A new module with a reading and a one-question quiz, added to training.yaml.",
		Fields:  []Field{in("id", "id", "Folder name under modules/, e.g. 02-heat."), doc("Module.title", "title", "string", true)},
		Sample:  map[string]string{"id": "03-quizzed", "title": "Quizzed"},
		Insert: Insert{Open: "modules/{{.id}}/quiz.yaml", Files: map[string]string{
			"modules/{{.id}}/module.yaml":      "title: {{q .title}}\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
			"modules/{{.id}}/reading/intro.md": "# {{.title}}\n\nWrite the reading here.\n",
			"modules/{{.id}}/quiz.yaml":        "pass_threshold: 0.8\nquestions:\n  - id: q1\n    type: single\n    prompt: \"Which iron do you strike?\"\n    options: [\"Cold iron\", \"Hot iron\"]\n    answer: 1\n",
		}, Appends: []Append{{File: "training.yaml", Path: []string{"modules"}, Item: "{{q .id}}"}}}},

	{ID: "reading", Group: "Reading", Title: "Reading", FileKind: "reading",
		Summary: "A Markdown page in a module, added to its items.",
		Doc:     "Readings are Markdown: headings, lists, code, tables, callouts and mermaid diagrams. Link images from assets/; other relative links won't resolve.",
		Fields:  []Field{moduleTarget, in("name", "id", "File name without .md, e.g. tongs."), in("title", "string", "The reading's title (its first heading).")},
		Sample:  map[string]string{"module": "02-plain", "name": "tongs", "title": "Tongs"},
		Insert: Insert{Open: "modules/{{.module}}/reading/{{.name}}.md",
			Files:   map[string]string{"modules/{{.module}}/reading/{{.name}}.md": "# {{.title}}\n\nWrite the reading here.\n"},
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "reading: reading/{{.name}}.md"}}}},

	{ID: "quiz", Group: "Quiz", Title: "Quiz file", FileKind: "quiz",
		Summary: "The module's quiz.yaml with a first single-choice question, added to the module's items.",
		Fields: []Field{moduleTarget, doc("Quiz.pass_threshold", "pass_threshold", "number", false), doc("Quiz.max_attempts", "max_attempts", "integer", false),
			doc("Question.prompt", "prompt", "text", true), options, in("answer", "integer", "Number of the right option, counting from 0.")},
		Sample: map[string]string{"module": "02-plain", "prompt": "Which iron do you strike?", "options": "Cold iron\nHot iron", "answer": "1"},
		Insert: Insert{Open: "modules/{{.module}}/quiz.yaml",
			Files: map[string]string{"modules/{{.module}}/quiz.yaml": "pass_threshold: {{or .pass_threshold \"0.8\"}}\n{{if .max_attempts}}max_attempts: {{.max_attempts}}\n{{end}}" +
				"questions:\n  - id: q1\n    type: single\n    prompt: {{q .prompt}}\n    options: {{list .options}}\n    answer: {{.answer}}\n"},
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "quiz: quiz.yaml"}}}},
	question("single", "Single choice", "One right option.", []Field{options, in("answer", "integer", "Number of the right option, counting from 0.")},
		"options: {{list .options}}\nanswer: {{.answer}}\n", map[string]string{"options": "Cold iron\nHot iron", "answer": "1"}),
	question("multi", "Multiple choice", "Several right options.", []Field{options, in("answer", "ints", "Numbers of the right options, counting from 0, e.g. 0, 2.")},
		"options: {{list .options}}\nanswer: {{ints .answer}}\n", map[string]string{"options": "Tongs\nHammer\nFlour", "answer": "0, 1"}),
	question("exact", "Exact answer", "Typed text that must match.", []Field{answerString, caseField},
		"answer: {{q .answer}}\n"+caseBody, map[string]string{"answer": "80"}),
	question("regex", "Pattern answer", "Typed text that must match a pattern.", []Field{in("answer", "string", "A regular expression the whole answer must match, e.g. \\d+\\.\\d+."), caseField},
		"answer: {{q .answer}}\n"+caseBody, map[string]string{"answer": `\d+\.\d+\.\d+`}),
	question("order", "Put in order", "Items the trainee puts in order.", []Field{in("options", "list", "The items in their right order, one per line.")},
		"options: {{list .options}}\n", map[string]string{"options": "Heat\nStrike\nQuench"}),
	question("match", "Match pairs", "Pairs the trainee matches.", []Field{in("pairs", "pairs", "One pair per line: left = right.")},
		"pairs: {{pairs .pairs}}\n", map[string]string{"pairs": "Hammer = strike\nWater = quench"}),
	question("terminal", "Terminal question", "Answered in the lab and checked by a script; a lab task names it.", []Field{in("check", "path", "Check script, relative to the lab folder, e.g. checks/q-port.sh."), opt("run_in", "id", Fields["Question.run_in"].Description)},
		"check: {{q .check}}\n{{if .run_in}}run_in: {{q .run_in}}\n{{end}}", map[string]string{"check": "checks/q-terminal.sh"}),
	question("text", "Written answer", "A person scores it on the Anvil against the rubric.", []Field{rubric}, "rubric: {{q .rubric}}\n", map[string]string{"rubric": "Names the three heats."}),
	question("upload", "File upload", "A person scores the uploaded file on the Anvil.", []Field{rubric}, "rubric: {{q .rubric}}\n", map[string]string{"rubric": "A photo of the finished blade."}),
	question("signoff", "Sign-off", "A person confirms it in person on the Anvil.", nil, "", nil),

	{ID: "lab.local", Group: "Lab", Title: "Laptop lab with a break-fix task", FileKind: "lab",
		Summary: "A Docker lab on the trainee's laptop: one shell, a setup that breaks something and a check that it's fixed.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-lab"},
		Insert: Insert{Open: "modules/{{.module}}/lab/lab.yaml", Files: breakFix("local"),
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
	{ID: "lab.cluster", Group: "Lab", Title: "Cluster lab", FileKind: "lab",
		Summary: "The same break-fix lab, run on Crucible's cluster: nothing to install on the laptop.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-cluster"},
		Insert: Insert{Open: "modules/{{.module}}/lab/lab.yaml", Files: breakFix("cluster"),
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
	{ID: "lab.task.check", Group: "Lab", Title: "Task with a check", FileKind: "lab",
		Summary: "A task whose check script exits 0 when it's done.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.points", "points", "number", false), opt("run_in", "id", "Service the check runs in; defaults to the first terminal's.")},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-cast", "title": "Cast"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc, "modules/{{.module}}/{{.labdir}}/checks/{{.id}}.sh": "#!/bin/sh\n# Exits 0 when the task is done.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "check: {script: checks/{{.id}}.sh, run_in: {{q (or .run_in .service)}}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.task.setup", Group: "Lab", Title: "Break-fix task", FileKind: "lab",
		Summary: "A task whose setup breaks something on purpose and whose check sees it fixed.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.points", "points", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-relight", "title": "Relight"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc,
				"modules/{{.module}}/{{.labdir}}/setup/{{.id}}.sh":  "#!/bin/sh\n# Breaks something on purpose.\nexit 0\n",
				"modules/{{.module}}/{{.labdir}}/checks/{{.id}}.sh": "#!/bin/sh\n# Exits 0 when it's fixed.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "setup: {script: setup/{{.id}}.sh, run_in: {{q .service}}}\ncheck: {script: checks/{{.id}}.sh, run_in: {{q .service}}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.task.quiz", Group: "Lab", Title: "Task answered by a terminal question", FileKind: "lab",
		Summary: "A task scored by a terminal question from the module's quiz.yaml.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), in("question", "id", "The terminal question's id in quiz.yaml.")},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-prove", "title": "Prove it", "question": "q-term"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files:   map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"}, Item: labTask + "quiz: {{q .question}}\n"}}}},
	{ID: "lab.task.review", Group: "Lab", Title: "Human-scored review task", FileKind: "lab",
		Summary: "A task a person scores on the Anvil against a rubric.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.rubric", "rubric", "text", true), doc("Task.points", "points", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-proof", "title": "Show your work", "rubric": "The transcript shows the fix and the notes say why.", "points": "3"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "human_review: true\nrubric: {{q .rubric}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.hint", Group: "Lab", Title: "Hint", FileKind: "lab",
		Summary: "A hint the trainee can reveal for a task, at a cost.",
		Fields:  []Field{moduleTarget, taskTarget, doc("Hint.text", "text", "text", true), doc("Hint.cost", "cost", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "task": "t1", "text": "Try `echo`.", "cost": "0.5"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml", Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Path: []string{"tasks", "id={{.task}}", "hints"}, Item: "text: {{q .text}}\n{{if .cost}}cost: {{.cost}}\n{{end}}"}}}},
	{ID: "lab.hint.file", Group: "Lab", Title: "Hint from a file", FileKind: "lab",
		Summary: "A longer hint kept in its own Markdown file, e.g. a full solution.",
		Fields:  []Field{moduleTarget, taskTarget, in("name", "id", "File name without .md, e.g. t1-solution."), doc("Hint.cost", "cost", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "task": "t1", "name": "t1-solution"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/hints/{{.name}}.md",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/hints/{{.name}}.md": "Spell out the solution here.\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks", "id={{.task}}", "hints"},
				Item: "file: hints/{{.name}}.md\n{{if .cost}}cost: {{.cost}}\n{{end}}"}}}},
	{ID: "lab.terminal", Group: "Lab", Title: "Terminal", FileKind: "lab",
		Summary: "Another terminal tab, attached to a service of the lab.",
		Fields:  []Field{moduleTarget, doc("Terminal.name", "name", "id", true), doc("Terminal.service", "service", "id", true)},
		Sample:  map[string]string{"module": "01-welcome", "name": "second", "service": "shell"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml", Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Path: []string{"terminals"}, Item: "name: {{q .name}}\nservice: {{q .service}}"}}}},
	{ID: "lab.setup", Group: "Lab", Title: "Lab setup script", FileKind: "script",
		Summary: "A script run once when the lab starts.",
		Fields:  []Field{moduleTarget, opt("run_in", "id", "Service it runs in; defaults to the first terminal's.")},
		Sample:  map[string]string{"module": "01-welcome"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/setup/lab.sh",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/setup/lab.sh": "#!/bin/sh\n# Runs once when the lab starts.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"setup"}, Set: true,
				Item: "script: setup/lab.sh\nrun_in: {{q (or .run_in .service)}}"}}}},

	{ID: "template.lab.aws", Group: "AWS", Title: "AWS lab with an S3 bucket", FileKind: "lab",
		Summary: "A lab in the shared AWS account: Terraform creates a bucket, a check looks for a file in it.",
		Doc:     "AWS labs cost money: each start needs an approval by its estimate, and budgets fail closed. " + Fields["AWSConfig.max_hourly_usd"].Description + " Terraform files can't be edited in the browser.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true), doc("AWSConfig.region", "region", "string", true), doc("AWSConfig.max_hourly_usd", "max_hourly_usd", "number", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-aws", "region": "eu-west-1", "max_hourly_usd": "0.05"},
		Insert: Insert{GitOnly: true, Open: "modules/{{.module}}/lab/lab.yaml", Files: map[string]string{
			"modules/{{.module}}/lab/lab.yaml": "id: {{q .id}}\nruntime: aws\nttl: 1h\nidle_timeout: 30m\nterminals:\n  - {name: workspace, service: workspace}\ntasks:\n" +
				"  - id: t1-bucket\n    instructions: tasks/01-bucket.md\n    check: {script: checks/01-bucket.sh, run_in: workspace, timeout: 60s}\naws:\n  region: {{q .region}}\n  max_hourly_usd: {{.max_hourly_usd}}\n",
			"modules/{{.module}}/lab/terraform/main.tf":   "# The lab's own bucket. Crucible adds the provider and the state backend.\nresource \"aws_s3_bucket\" \"forge\" {\n  bucket        = \"crucible-lab-${var.crucible_lab_id}\"\n  force_destroy = true\n}\n",
			"modules/{{.module}}/lab/tasks/01-bucket.md":  "# Fill the bucket\n\nPut a file named forged.txt in your lab's bucket.\n",
			"modules/{{.module}}/lab/checks/01-bucket.sh": "#!/bin/sh\naws s3api head-object --bucket \"crucible-lab-$CRUCIBLE_LAB_ID\" --key forged.txt >/dev/null 2>&1\n",
		}, Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
}

func init() {
	for i := range Catalog {
		b := &Catalog[i]
		b.GitOnly = b.Insert.GitOnly
		b.Example = example(*b)
	}
}

// example is what the catalog shows, rendered from Sample: the file the block opens if it creates it, else the YAML
// entry it adds.
func example(b Block) string {
	data := map[string]string{"labdir": "lab", "service": "shell"}
	for k, v := range b.Sample {
		data[k] = v
	}
	for _, f := range b.Fields { // optional fields the sample leaves out render as empty
		if _, ok := data[f.Name]; !ok {
			data[f.Name] = ""
		}
	}
	open, _ := render(b.Insert.Open, data)
	for p, body := range b.Insert.Files {
		if rp, _ := render(p, data); rp == open {
			s, _ := render(body, data)
			return s
		}
	}
	if len(b.Insert.Appends) > 0 {
		s, _ := render(b.Insert.Appends[0].Item, data)
		return s
	}
	return ""
}

func Find(id string) (Block, bool) {
	i := slices.IndexFunc(Catalog, func(b Block) bool { return b.ID == id })
	if i < 0 {
		return Block{}, false
	}
	return Catalog[i], true
}

func invalidf(format string, a ...any) error {
	return apperr.Wrap(apperr.Invalid, fmt.Sprintf(format, a...))
}

// quote is s as a JSON string, which is a YAML double-quoted scalar. HTML escaping is off, so <, > and & read as typed.
func quote(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

var funcs = template.FuncMap{
	"q": quote,
	"list": func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, quote(l))
			}
		}
		return "[" + strings.Join(out, ", ") + "]"
	},
	"ints": func(s string) string {
		return "[" + strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), ", ") + "]"
	},
	"pairs": func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if left, right, ok := strings.Cut(l, "="); ok {
				out = append(out, "["+quote(strings.TrimSpace(left))+", "+quote(strings.TrimSpace(right))+"]")
			}
		}
		return "[" + strings.Join(out, ", ") + "]"
	},
}

func render(tpl string, data map[string]string) (string, error) {
	t, err := template.New("").Funcs(funcs).Option("missingkey=error").Parse(tpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

var (
	idRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,62}$`)
	pathRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*\.sh$`)
)

// checkValues validates form values against b's fields: known names only, required ones present, every value of its
// type. Ids and paths are plain names, so they can go into paths and YAML unquoted; text goes through q.
func checkValues(b Block, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k := range values {
		if !slices.ContainsFunc(b.Fields, func(f Field) bool { return f.Name == k }) {
			return nil, invalidf("%s is not a field of %s", k, b.Title)
		}
	}
	for _, f := range b.Fields {
		v := strings.TrimSpace(values[f.Name])
		if v == "" {
			v = f.Default
		}
		out[f.Name] = v
		if v == "" {
			if f.Required {
				return nil, invalidf("%s is required", f.Name)
			}
			continue
		}
		if len(v) > 4096 || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
			return nil, invalidf("%s: text of at most 4 KiB, without control characters", f.Name)
		}
		bad := false
		switch f.Type {
		case "id", "module", "task":
			bad = !idRE.MatchString(v)
		case "path":
			bad = !pathRE.MatchString(v) || strings.Contains(v, "..")
		case "string", "enum":
			bad = strings.ContainsAny(v, "\r\n") || len(v) > 200 || (f.Type == "enum" && !slices.Contains(f.Enum, v))
		// numbers are written as their parsed value: YAML would read the raw text 010 as octal 8
		case "number":
			n, err := strconv.ParseFloat(v, 64)
			bad = err != nil || math.IsNaN(n) || math.IsInf(n, 0) || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) || strings.ContainsAny(v, "xXpP_")
			out[f.Name] = strconv.FormatFloat(n, 'f', -1, 64)
		case "integer":
			n, err := strconv.Atoi(v)
			bad = err != nil || n < 0
			out[f.Name] = strconv.Itoa(n)
		case "ints":
			var ns []string
			for _, s := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
				n, err := strconv.Atoi(s)
				bad = bad || err != nil || n < 0
				ns = append(ns, strconv.Itoa(n))
			}
			out[f.Name] = strings.Join(ns, ", ")
		case "bool":
			bad = v != "true" && v != "false"
		case "duration":
			_, err := time.ParseDuration(v)
			bad = err != nil
		case "list", "pairs":
			lines := strings.Split(v, "\n")
			bad = len(lines) > 20 || (f.Type == "pairs" && slices.ContainsFunc(lines, func(l string) bool { return !strings.Contains(l, "=") }))
		}
		if bad {
			return nil, invalidf("%s: %q isn't a valid %s", f.Name, v, f.Type)
		}
	}
	return out, nil
}

// labOf is a module's lab folder ("" when it has none) and its first terminal's service.
func labOf(dir, module string) (string, string) {
	lab := gitsync.LabFolder(dir, module)
	if lab == "" {
		return "", ""
	}
	var l struct {
		Terminals []content.Terminal `yaml:"terminals"`
	}
	_ = yamlx.ReadLoose(filepath.Join(dir, "modules", module, filepath.FromSlash(lab), "lab.yaml"), &l)
	if len(l.Terminals) == 0 {
		return lab, ""
	}
	return lab, l.Terminals[0].Service
}

// Plan renders b with values against the repo at dir (read only): the files it changes (path → new text), the file to
// open, and the line and line count of the insertion there.
func Plan(dir string, b Block, values map[string]string) (map[string]string, string, int, int, error) {
	vals, err := checkValues(b, values)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if m := vals["module"]; m != "" {
		vals["labdir"], vals["service"] = labOf(dir, m)
		if _, err := os.Stat(filepath.Join(dir, "modules", m, "module.yaml")); err != nil {
			return nil, "", 0, 0, invalidf("there is no module %s", m)
		}
	}
	if b.Insert.NeedsLab && vals["labdir"] == "" {
		f := "modules/" + vals["module"] + "/module.yaml"
		var n yaml.Node
		if raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f))); err == nil {
			if err := yaml.Unmarshal(raw, &n); err != nil { // LabFolder can't tell broken YAML from no lab
				return nil, "", 0, 0, invalidf("%s: the YAML doesn't parse (%v); fix it first", f, err)
			}
		}
		return nil, "", 0, 0, invalidf("module %s has no lab yet: add a lab first", vals["module"])
	}
	for _, k := range []string{"labdir", "service"} {
		if _, ok := vals[k]; !ok {
			vals[k] = ""
		}
	}
	open, err := render(b.Insert.Open, vals)
	if err != nil {
		return nil, "", 0, 0, err
	}
	changed := map[string]string{}
	line, lines := 1, 0
	for p, body := range b.Insert.Files {
		rp, err := render(p, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rp))); err == nil {
			return nil, "", 0, 0, invalidf("%s already exists", rp)
		}
		if changed[rp], err = render(body, vals); err != nil {
			return nil, "", 0, 0, err
		}
		if rp == open {
			lines = strings.Count(changed[rp], "\n")
		}
	}
	for _, a := range b.Insert.Appends {
		file, err := render(a.File, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		segs := make([]string, len(a.Path))
		for i, s := range a.Path {
			if segs[i], err = render(s, vals); err != nil {
				return nil, "", 0, 0, err
			}
		}
		item, err := render(a.Item, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		cur, ok := changed[file]
		if !ok {
			if gitsync.CheckPath(file) != nil && !b.Insert.GitOnly {
				return nil, "", 0, 0, invalidf("%s can't be edited here", file)
			}
			if err := gitsync.NoSymlinks(dir, file); err != nil {
				return nil, "", 0, 0, invalidf("%s: %v", file, err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
			if err != nil {
				return nil, "", 0, 0, invalidf("%s doesn't exist", file)
			}
			cur = string(raw)
		}
		out, at, err := yamlx.Insert([]byte(cur), segs, item, a.Set)
		if err != nil {
			return nil, "", 0, 0, invalidf("%s: %v", file, err)
		}
		if file == open {
			line, lines = at, strings.Count(string(out), "\n")-strings.Count(cur, "\n")
		}
		changed[file] = string(out)
	}
	return changed, open, line, max(lines, 1), nil
}

// Apply is Plan for the editor: refused for git-only blocks, and returned as put ops that pass gitsync.CheckOps.
func Apply(dir, id string, values map[string]string) (*Result, error) {
	b, ok := Find(id)
	if !ok {
		return nil, apperr.Wrap(apperr.NotFound, "no such block")
	}
	if b.GitOnly {
		return nil, invalidf("%s can't be added in the browser: %s", b.Title, gitOnlyWhy)
	}
	changed, open, line, lines, err := Plan(dir, b, values)
	if err != nil {
		return nil, err
	}
	ops := gitsync.PutOps(changed)
	if err := gitsync.CheckOps(ops); err != nil {
		return nil, err
	}
	return &Result{Ops: ops, Open: open, Line: line, Lines: lines}, nil
}
