package blocks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"crucible/internal/content"
)

var contentTypes = []any{content.Training{}, content.Module{}, content.Quiz{}, content.Question{}, content.Lab{},
	content.AWSConfig{}, content.Terminal{}, content.Script{}, content.Task{}, content.Hint{}}

// Adding a content field without describing it fails here: the editor's hover text and the Docs reference come from Fields.
func TestEveryContentFieldIsDescribed(t *testing.T) {
	want := map[string]bool{}
	for _, v := range contentTypes {
		typ := reflect.TypeOf(v)
		for _, f := range YAMLFields(typ) {
			key := typ.Name() + "." + f.Key
			want[key] = true
			if strings.TrimSpace(Fields[key].Description) == "" {
				t.Errorf("%s has no description in blocks.Fields", key)
			}
		}
	}
	for key := range Fields {
		if !want[key] {
			t.Errorf("blocks.Fields describes %s, which the content types no longer have", key)
		}
	}
}

// jsonSchema round-trips Schema through JSON: the browser gets exactly this.
func jsonSchema(t *testing.T, kind string) map[string]any {
	t.Helper()
	b, err := json.Marshal(Schema(kind))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// validate checks v against the subset of JSON Schema that Schema emits. No library: the subset is small and fixed.
func validate(s map[string]any, v any, at string) []string {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, at+": "+fmt.Sprintf(f, a...)) }
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		bad("%v is not one of %v", v, enum)
	}
	num := func(x any) (float64, bool) {
		switch n := x.(type) {
		case int:
			return float64(n), true
		case float64:
			return n, true
		}
		return 0, false
	}
	switch s["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			bad("want a mapping, got %T", v)
			return errs
		}
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if _, ok := m[r.(string)]; !ok {
				bad("%s is required", r)
			}
		}
		if n, ok := s["minProperties"].(float64); ok && float64(len(m)) < n {
			bad("needs at least %v keys", n)
		}
		if n, ok := s["maxProperties"].(float64); ok && float64(len(m)) > n {
			bad("allows at most %v keys", n)
		}
		for k, x := range m {
			if p, ok := props[k].(map[string]any); ok {
				errs = append(errs, validate(p, x, at+"."+k)...)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					bad("unknown key %s", k)
				}
			case map[string]any:
				errs = append(errs, validate(ap, x, at+"."+k)...)
			}
		}
	case "array":
		l, ok := v.([]any)
		if !ok {
			bad("want a list, got %T", v)
			return errs
		}
		for i, x := range l {
			errs = append(errs, validate(s["items"].(map[string]any), x, fmt.Sprintf("%s[%d]", at, i))...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			bad("want a string, got %T", v)
			return errs
		}
		if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(str) {
			bad("%q doesn't match %s", str, p)
		}
	case "number", "integer":
		n, ok := num(v)
		if _, isInt := v.(int); !ok || (s["type"] == "integer" && !isInt) {
			bad("want a %s, got %v", s["type"], v)
			return errs
		}
		if m, ok := s["minimum"].(float64); ok && n < m {
			bad("%v is below %v", n, m)
		}
		if m, ok := s["maximum"].(float64); ok && n > m {
			bad("%v is above %v", n, m)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			bad("want true or false, got %v", v)
		}
	}
	return errs
}

func kindOf(p string) string {
	switch filepath.Base(p) {
	case "training.yaml":
		return "training"
	case "module.yaml":
		return "module"
	case "quiz.yaml":
		return "quiz"
	case "lab.yaml":
		return "lab"
	}
	return ""
}

func TestSchemaAcceptsEveryExample(t *testing.T) {
	var files []string
	for _, g := range []string{"training.yaml", "modules/*/module.yaml", "modules/*/quiz.yaml", "modules/*/*/lab.yaml"} {
		m, _ := filepath.Glob(filepath.Join("../../../examples/*", g))
		files = append(files, m...)
	}
	if len(files) < 15 {
		t.Fatalf("found only %d example files", len(files))
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(p, err)
		}
		for _, e := range validate(jsonSchema(t, kindOf(p)), doc, p) {
			t.Error(e)
		}
	}
}

func TestSchemaRejects(t *testing.T) {
	for kind, src := range map[string]string{
		"training": "id: t\ntitle: T\nmodules: [m]\nmaintainer: [x]\n", // typo'd key
		"module":   "title: M\nitems:\n  - reading: a.md\n    quiz: quiz.yaml\n",
		"quiz":     "questions:\n  - {id: q, type: essay, prompt: P}\n",
		"lab":      "id: l\nruntime: local\nttl: soon\nterminals: [{name: s, service: s}]\ntasks: [{id: t, instructions: t.md}]\n",
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatal(err)
		}
		if errs := validate(jsonSchema(t, kind), doc, kind); len(errs) == 0 {
			t.Errorf("%s: schema accepted %q", kind, src)
		}
	}
}

func TestSchemaHasHoverText(t *testing.T) {
	q := jsonSchema(t, "quiz")["properties"].(map[string]any)["questions"].(map[string]any)["items"].(map[string]any)
	typ := q["properties"].(map[string]any)["type"].(map[string]any)
	if d, ok := typ["description"].(string); !ok || d == "" || len(typ["enum"].([]any)) != 10 {
		t.Fatalf("question type: %v", typ)
	}
}
