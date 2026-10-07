package docs

import (
	"fmt"
	"reflect"
	"strings"

	userdocs "crucible/docs/user"
	"crucible/internal/content"
	"crucible/internal/content/blocks"
)

// All is every page: docs/user plus the generated ones, sorted.
func All() ([]Page, error) {
	pages, err := Load(userdocs.FS)
	if err != nil {
		return nil, err
	}
	pages = append(pages, Generated()...)
	Sort(pages)
	return pages, nil
}

var referenceTypes = []struct {
	v     any
	title string
}{
	{content.Training{}, "training.yaml"}, {content.Module{}, "module.yaml"}, {content.Quiz{}, "quiz.yaml"},
	{content.Question{}, "Quiz questions"}, {content.Lab{}, "lab.yaml"}, {content.Terminal{}, "Lab terminals"},
	{content.Script{}, "Scripts (lab setup, task check and setup)"}, {content.Task{}, "Lab tasks"}, {content.Hint{}, "Hints"},
	{content.AWSConfig{}, "AWS settings (lab.yaml aws:)"},
}

// Generated are the pages built from the block registry at startup, so they can't drift from the code.
func Generated() []Page {
	var b strings.Builder
	b.WriteString("Every key Crucible reads from a training repo. The editor shows the same text when you hover a key.\n")
	for _, rt := range referenceTypes {
		t := reflect.TypeOf(rt.v)
		fmt.Fprintf(&b, "\n## %s\n\n| Key | Type | Required | What it does |\n|---|---|---|---|\n", rt.title)
		for _, f := range blocks.YAMLFields(t) {
			d := blocks.Fields[t.Name()+"."+f.Key]
			req := ""
			if d.Required {
				req = "yes"
			}
			desc := d.Description
			if len(d.Enum) > 0 {
				desc += " One of: `" + strings.Join(d.Enum, "`, `") + "`."
			}
			if d.Default != "" {
				desc += " Default: `" + d.Default + "`."
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", f.Key, kindName(f.Type), req, strings.ReplaceAll(desc, "|", `\|`))
		}
	}
	body := b.String()
	return []Page{{Slug: "authors/building-blocks", Section: "authors", Title: "Building blocks: every key", Roles: []string{"author"},
		Covers: []string{"feature:blocks.reference"}, Order: 90, Headings: headings(body), Body: body}}
}

func kindName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Name() == "Duration":
		return "duration (`90s`, `20m`, `1h`)"
	case t.Name() == "Node":
		return "depends on the question type"
	}
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true / false"
	case reflect.Int:
		return "whole number"
	case reflect.Float64:
		return "number"
	case reflect.Slice:
		return "list"
	}
	return "mapping"
}
