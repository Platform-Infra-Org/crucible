package yamlx

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "f.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDuration(t *testing.T) {
	var v struct {
		TTL Duration `yaml:"ttl"`
	}
	if err := ReadFile(write(t, "ttl: 90m\n"), &v, true); err != nil {
		t.Fatal(err)
	}
	if v.TTL.D() != 90*time.Minute {
		t.Fatalf("got %v", v.TTL.D())
	}
	if err := ReadFile(write(t, "ttl: soon\n"), &v, true); err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestStrictAndOptional(t *testing.T) {
	var v struct {
		A string `yaml:"a"`
	}
	err := ReadFile(write(t, "a: x\nb: y\n"), &v, true)
	if err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	if err := ReadFile(filepath.Join(t.TempDir(), "missing.yaml"), &v, false); err != nil {
		t.Fatalf("optional missing file: %v", err)
	}
	if err := ReadFile(filepath.Join(t.TempDir(), "missing.yaml"), &v, true); err == nil {
		t.Fatal("required missing file must error")
	}
}

func TestDurationMarshalsLikePeopleWriteIt(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Hour: "2h", 45 * time.Minute: "45m", 90 * time.Minute: "1h30m", 30 * time.Second: "30s"} {
		got, _ := Duration(d).MarshalYAML()
		if got != want {
			t.Errorf("%v → %v, want %s", d, got, want)
		}
	}
}

func TestUpdateKeepsCommentsAndOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "team.yaml")
	_ = os.WriteFile(p, []byte("# The forge team\nname: The Forge # shown in the UI\nleader: l@x\nseniors: [a@x]\nmembers: []\n"), 0o644)
	err := Update(p, map[string]any{"seniors": []string{"b@x"}, "members": nil, "trainees": []string{"t@x"},
		"lab_defaults": map[string]any{"ttl": Duration(2 * time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{"# The forge team", "name: The Forge # shown in the UI", "- b@x", "trainees:", "ttl: 2h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "members") || strings.Index(got, "name:") > strings.Index(got, "seniors:") {
		t.Fatalf("nil deletes, untouched keys keep their place:\n%s", got)
	}
	fresh := filepath.Join(t.TempDir(), "new", "p.yaml")
	if err := Update(fresh, map[string]any{"training": "x"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fresh); string(b) != "training: x\n" {
		t.Fatalf("new file: %q", b)
	}
}

func TestUpdateTreatsNullAndEmptyAsEmptyMapping(t *testing.T) {
	for _, body := range []string{"null\n", "~\n", "# just a comment\n", ""} {
		p := filepath.Join(t.TempDir(), "b.yaml")
		_ = os.WriteFile(p, []byte(body), 0o644)
		if err := Update(p, map[string]any{"monthly_usd": 5}); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if b, _ := os.ReadFile(p); !strings.Contains(string(b), "monthly_usd: 5") {
			t.Fatalf("%q → %q", body, b)
		}
	}
	p := filepath.Join(t.TempDir(), "list.yaml")
	_ = os.WriteFile(p, []byte("- a\n"), 0o644)
	if err := Update(p, map[string]any{"x": 1}); err == nil {
		t.Fatal("a list at the top level is an error")
	}
}

func TestInsertAppendsWithoutTouchingTheRest(t *testing.T) {
	src := "# Quiz for module 1\npass_threshold: 0.8   # keep it high\n\nquestions:\n  - id: q1   # the first\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\", \"yes\"]\n    answer: 1\n\n# trailing comment\n"
	out, line, err := Insert([]byte(src), []string{"questions"}, "id: q2\ntype: exact\nprompt: \"Port?\"\nanswer: \"80\"\n", false)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, "    answer: 1\n", "    answer: 1\n  - id: q2\n    type: exact\n    prompt: \"Port?\"\n    answer: \"80\"\n", 1)
	if string(out) != want || line != 10 {
		t.Fatalf("line %d:\n%s", line, out)
	}
}

func TestInsertFlowListsNestedListsAndKeys(t *testing.T) {
	out, line, err := Insert([]byte("id: t\nmodules: [01-welcome]  # order matters\n"), []string{"modules"}, "02-heat", false)
	if err != nil || string(out) != "id: t\nmodules: [01-welcome, 02-heat]  # order matters\n" || line != 2 {
		t.Fatalf("flow: %q %d %v", out, line, err)
	}
	if out, _, _ := Insert([]byte("modules: []\n"), []string{"modules"}, "01-a", false); string(out) != "modules: [01-a]\n" {
		t.Fatalf("empty flow: %q", out)
	}
	lab := "id: l\ntasks:\n  - id: t1\n    instructions: tasks/1.md\n    check: { script: c.sh, run_in: shell }\n  - id: t2\n    instructions: tasks/2.md\n    hints:\n"
	out, line, err = Insert([]byte(lab), []string{"tasks", "id=t1", "hints"}, "text: \"Try echo\"", false)
	if err != nil || line != 7 || !strings.Contains(string(out), "    check: { script: c.sh, run_in: shell }\n    hints:\n      - text: \"Try echo\"\n  - id: t2\n") {
		t.Fatalf("a missing nested list is created at the end of its mapping: %d %v\n%s", line, err, out)
	}
	out, line, err = Insert([]byte(lab), []string{"tasks", "id=t2", "hints"}, "text: a", false)
	if err != nil || line != 9 || !strings.HasSuffix(string(out), "    hints:\n      - text: a\n") {
		t.Fatalf("an empty key gets its first entry: %d %v\n%s", line, err, out)
	}
	out, _, err = Insert([]byte(lab), []string{"setup"}, "script: setup/lab.sh\nrun_in: shell", true)
	if err != nil || !strings.HasSuffix(string(out), "    hints:\nsetup:\n  script: setup/lab.sh\n  run_in: shell\n") {
		t.Fatalf("set a missing key: %v\n%s", err, out)
	}
	if _, _, err := Insert([]byte(lab), []string{"id"}, "x", true); err == nil {
		t.Fatal("set refuses a key that exists")
	}
	if out, line, err := Insert(nil, []string{"questions"}, "id: q", false); err != nil || string(out) != "questions:\n  - id: q\n" || line != 2 {
		t.Fatalf("empty file: %q %d %v", out, line, err)
	}
}

func TestInsertKeepsCRLF(t *testing.T) {
	out, _, err := Insert([]byte("questions:\r\n  - id: q1\r\n"), []string{"questions"}, "id: q2", false)
	if err != nil || string(out) != "questions:\r\n  - id: q1\r\n  - id: q2\r\n" {
		t.Fatalf("%q %v", out, err)
	}
}

// Review Focus 4: inserting into a file the author left broken says so instead of guessing.
func TestInsertBrokenYAML(t *testing.T) {
	for _, src := range []string{"questions:\n  - id: [\n", "questions:\n\t- id: q\n"} {
		if _, _, err := Insert([]byte(src), []string{"questions"}, "id: q2", false); err == nil || !strings.Contains(err.Error(), "fix it first") {
			t.Errorf("%q: %v", src, err)
		}
	}
	if _, _, err := Insert([]byte("questions: {a: 1}\n"), []string{"questions"}, "id: q2", false); err == nil {
		t.Error("appending to a mapping as if it were a list")
	}
	if _, _, err := Insert([]byte("tasks:\n  - id: t1\n"), []string{"tasks", "id=nope", "hints"}, "text: a", false); err == nil {
		t.Error("an unknown task")
	}
}

// Values with YAML-significant text, quoted by the caller, round-trip literally and the rest of the file is untouched.
func TestInsertLiteralValues(t *testing.T) {
	src := "# c\ntasks:\n  - id: t1\n    hints:\n      - text: old\n"
	for _, v := range []string{`say "hi"`, "a: b", "x # y", "- dash", "{{ .Name }}", "line1\nline2", "it's", "- ", "'", "[", "@at", "&a *b !t"} {
		item := "text: " + fmt.Sprintf("%q", v)
		out, _, err := Insert([]byte(src), []string{"tasks", "id=t1", "hints"}, item, false)
		if err != nil || !strings.HasPrefix(string(out), src) {
			t.Fatalf("%q: %v\n%s", v, err, out)
		}
		var got struct {
			Tasks []struct{ Hints []struct{ Text string } }
		}
		if err := yaml.Unmarshal(out, &got); err != nil || got.Tasks[0].Hints[1].Text != v {
			t.Fatalf("%q round-tripped as %+v (%v)\n%s", v, got, err, out)
		}
	}
}

// Broken input is refused and left alone; arbitrary input never panics.
func FuzzInsert(f *testing.F) {
	for _, s := range []string{"", "a: [", "q:\n  - id: x\n", "q: []\n", "q:\r\n- a\r\n", "q: |\n  x\n", "# c", "\t", "q: {a: 1}", "q: ~", "- a"} {
		f.Add(s, "q", "id: z", false)
		f.Add(s, "q", "z", true)
	}
	f.Fuzz(func(t *testing.T, src, key, item string, set bool) {
		in := []byte(src)
		Insert(in, []string{key}, item, set)
		Insert(in, []string{key, "id=x", "h"}, item, set)
		if string(in) != src {
			t.Fatal("input modified")
		}
	})
}
