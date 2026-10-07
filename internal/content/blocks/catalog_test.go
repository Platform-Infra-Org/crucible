package blocks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

// fixture is a minimal valid repo: 01-welcome has a reading, a quiz (one single and one terminal question) and a
// local lab; 02-plain has only a reading.
var fixture = map[string]string{
	"training.yaml":                           "id: fx\ntitle: Fixture\nmaintainers: [m@x]\nprogression: free\nmodules: [01-welcome, 02-plain]\n",
	"modules/01-welcome/module.yaml":          "title: Welcome\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n  - lab: lab\n",
	"modules/01-welcome/reading/intro.md":     "# Intro\n\nHello.\n",
	"modules/01-welcome/quiz.yaml":            "questions:\n  - {id: q1, type: single, prompt: \"Hot?\", options: [\"no\", \"yes\"], answer: 1}\n  - {id: q-term, type: terminal, prompt: \"Prove it\", check: checks/q-term.sh}\n",
	"modules/01-welcome/lab/lab.yaml":         "id: fx-lab\nruntime: local\nterminals:\n  - {name: shell, service: shell}\ntasks:\n  - id: t1\n    instructions: tasks/t1.md\n    check: {script: checks/t1.sh, run_in: shell}\n",
	"modules/01-welcome/lab/compose.yaml":     "services:\n  shell:\n    image: alpine:3.22\n    command: [\"sleep\", \"infinity\"]\n",
	"modules/01-welcome/lab/tasks/t1.md":      "# Task 1\n",
	"modules/01-welcome/lab/checks/t1.sh":     "#!/bin/sh\nexit 0\n",
	"modules/01-welcome/lab/checks/q-term.sh": "#!/bin/sh\nexit 0\n",
	"modules/02-plain/module.yaml":            "title: Plain\nitems:\n  - reading: reading/intro.md\n",
	"modules/02-plain/reading/intro.md":       "# Plain\n",
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { // lint wants scripts executable
		if err == nil && strings.HasSuffix(p, ".sh") {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

// Every block's sample, inserted as an author would, leaves a training that loads and lints clean. Git-only blocks
// are written straight into the repo, as an author does in git.
func TestEveryBlockSampleLoadsAndLints(t *testing.T) {
	for _, b := range Catalog {
		t.Run(b.ID, func(t *testing.T) {
			dir := t.TempDir()
			if !b.Insert.NewRepo {
				writeAll(t, dir, fixture)
			}
			if b.Insert.GitOnly {
				files, _, _, _, err := Plan(dir, b, b.Sample)
				if err != nil {
					t.Fatal(err)
				}
				writeAll(t, dir, files)
			} else {
				res, err := Apply(dir, b.ID, b.Sample)
				if err != nil {
					t.Fatal(err)
				}
				if res.Open == "" || res.Line < 1 || res.Lines < 1 {
					t.Fatalf("result: %+v", res)
				}
				if _, err := gitsync.ApplyOps(dir, res.Ops); err != nil {
					t.Fatal(err)
				}
				writeAll(t, dir, nil)
			}
			if _, probs := content.Load(dir); len(probs) > 0 {
				t.Fatalf("%s leaves problems: %v", b.ID, probs)
			}
			if strings.TrimSpace(b.Example) == "" || b.Summary == "" || !slices.Contains(Groups, b.Group) {
				t.Fatalf("catalog entry incomplete: %+v", b)
			}
		})
	}
}

func TestCatalogCoversTheContentModel(t *testing.T) {
	want := []string{"template.training", "module", "template.module-quiz", "reading", "quiz", "lab.local", "lab.cluster",
		"template.lab.aws", "lab.task.check", "lab.task.setup", "lab.task.quiz", "lab.task.review", "lab.hint", "lab.hint.file",
		"lab.terminal", "lab.setup"}
	for _, typ := range questionTypes {
		want = append(want, "quiz.question."+typ)
	}
	for _, id := range want {
		if _, ok := Find(id); !ok {
			t.Errorf("no block %s", id)
		}
	}
}

// Review Focus 3: form values are text, never YAML. Quotes, colons, #, leading dashes and {{ }} stay literal, and no
// other key changes.
func TestInsertValuesAreLiteral(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, fixture)
	nasty := "He said: \"hot\" # not a comment\n- maintainers: [evil@x]\n{{.module}} ' ` \\"
	res, err := Apply(dir, "quiz.question.single", map[string]string{"module": "01-welcome", "id": "q-nasty", "prompt": nasty, "options": "a: b\n#x\n- y", "answer": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitsync.ApplyOps(dir, res.Ops); err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load(dir)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	q := tr.Module("01-welcome").Quiz.Question("q-nasty")
	if q.Prompt != nasty || !slices.Equal(q.Options, []string{"a: b", "#x", "- y"}) || !slices.Equal(tr.Maintainers, []string{"m@x"}) {
		t.Fatalf("prompt %q options %q maintainers %v", q.Prompt, q.Options, tr.Maintainers)
	}
	for name, values := range map[string]map[string]string{
		"id with a newline":  {"id": "x\ny", "title": "T"},
		"id with a colon":    {"id": "a: b", "title": "T"},
		"title with newline": {"id": "04-x", "title": "a\nmaintainers: [evil]"},
		"unknown field":      {"id": "04-x", "title": "T", "maintainers": "evil"},
		"missing required":   {"id": "04-x"},
	} {
		if _, err := Apply(dir, "module", values); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Apply(dir, "template.lab.aws", map[string]string{"module": "02-plain"}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("git-only blocks are refused: %v", err)
	}
	if _, err := Apply(dir, "nope", nil); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown block: %v", err)
	}
	if _, err := Apply(dir, "lab.task.check", map[string]string{"module": "02-plain", "id": "t9", "title": "T"}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "no lab") {
		t.Errorf("a lab block in a module without a lab: %v", err)
	}
	if _, err := Apply(dir, "reading", map[string]string{"module": "01-welcome", "name": "intro", "title": "Again"}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("a file that exists is never overwritten: %v", err)
	}
}
