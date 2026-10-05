package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// base is a minimal valid training. Tests override single files to break it.
var base = map[string]string{
	"training.yaml":               "id: t1\ntitle: T1\nmodules: [m1]\n",
	"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n  - lab: lab\n",
	"modules/m1/reading/intro.md": "# Intro to the Forge\nHello.\n",
	"modules/m1/quiz.yaml": `questions:
  - {id: q1, type: single, prompt: P, options: [a, b], answer: 1}
  - {id: q2, type: terminal, prompt: "Port?", check: checks/q2.sh}
`,
	"modules/m1/lab/lab.yaml": `id: l1
runtime: local
terminals: [{name: shell, service: box}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: box}
    points: 2
    hints:
      - text: nudge
      - file: hints/sol.md
        cost: 1
  - id: t2
    instructions: tasks/t2.md
    setup: {script: setup/t2.sh, run_in: box}
    quiz: q2
`,
	"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n",
	"modules/m1/lab/tasks/t1.md":  "Do t1",
	"modules/m1/lab/tasks/t2.md":  "Do t2",
	"modules/m1/lab/checks/t1.sh": "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/checks/q2.sh": "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/setup/t2.sh":  "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/hints/sol.md": "the answer",
}

func tree(t *testing.T, override map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for k, v := range base {
		files[k] = v
	}
	for k, v := range override {
		files[k] = v
	}
	for name, body := range files {
		if body == "<delete>" {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") && body != "<noexec>" {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	tr, probs := Load(tree(t, nil))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if tr.Progression != "linear" {
		t.Errorf("progression default = %q", tr.Progression)
	}
	m := tr.Module("m1")
	if m.Items[0].Title != "Intro to the Forge" || m.Items[0].ID != "intro" {
		t.Errorf("reading item = %+v", m.Items[0])
	}
	if m.Quiz.PassThreshold != 0.8 || m.Quiz.Question("q1").Points != 1 {
		t.Errorf("quiz defaults: %+v", m.Quiz)
	}
	lab := m.Lab
	if lab.IdleWarning.D() != 5*time.Minute || lab.TaskOrder != "linear" || lab.Compose != "compose.yaml" {
		t.Errorf("lab defaults: %+v", lab)
	}
	t1 := lab.Task("t1")
	if t1.Check.Timeout.D() != 30*time.Second {
		t.Errorf("check timeout default = %v", t1.Check.Timeout.D())
	}
	if t1.Hints[0].EffectiveCost(lab) != 0 || t1.Hints[1].EffectiveCost(lab) != 1 {
		t.Errorf("hint costs wrong")
	}
	t2 := lab.Task("t2")
	if t2.Setup.Timeout.D() != 60*time.Second || t2.Points != 1 {
		t.Errorf("t2 = %+v", t2)
	}
	q2 := m.Quiz.Question("q2")
	if q2.Script == nil || q2.Script.RunIn != "box" {
		t.Errorf("terminal question script not resolved: %+v", q2.Script)
	}
}

func TestLoadProblems(t *testing.T) {
	cases := map[string]struct {
		override map[string]string
		want     string
	}{
		"missing title":     {map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"}, "title is required"},
		"bad runtime":       {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "local", "moon", 1)}, "runtime must be"},
		"unknown service":   {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "service: box", "service: nope", 1)}, `service "nope"`},
		"not executable":    {map[string]string{"modules/m1/lab/checks/t1.sh": "<noexec>"}, "must be executable"},
		"idle warning":      {map[string]string{"modules/m1/lab/lab.yaml": "idle_timeout: 5m\nidle_warning: 5m\n" + base["modules/m1/lab/lab.yaml"]}, "idle_warning must be shorter"},
		"answer range":      {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b], answer: 7}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer must be an option index"},
		"missing answer":    {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b]}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer is required"},
		"quiz not terminal": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "quiz: q2", "quiz: q1", 1)}, "terminal question"},
		"hint too costly":   {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "cost: 1", "cost: 5", 1)}, "hint 2 cost"},
		"path escape":       {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: ../../../etc/passwd\n"}, "must stay inside"},
		"bad regex":         {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: regex, prompt: P, answer: '('}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "regex"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr, probs := Load(tree(t, c.override))
			if tr != nil {
				t.Fatal("expected nil training when problems exist")
			}
			var all []string
			for _, p := range probs {
				all = append(all, p.String())
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want problem containing %q, got:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}
