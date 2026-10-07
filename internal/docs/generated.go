package docs

import (
	"fmt"
	"path"
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
	const title = "Building blocks: every key"
	var b strings.Builder
	b.WriteString("# " + title + "\n\nEvery key Crucible reads from a training repo. The editor shows the same text when you hover a key.\n")
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
	pages := []Page{{Slug: "authors/building-blocks", Section: "authors", Title: title, Roles: []string{"author"},
		Covers: []string{"feature:blocks.reference"}, Order: 90, Headings: headings(body), Body: body}}
	for i, g := range blocks.Groups {
		var b strings.Builder
		var covers []string
		fmt.Fprintf(&b, "# Building blocks: %s\n\nThe %s blocks of the editor's Blocks panel. Each one is a form; what it writes is shown below.\n", g, strings.ToLower(g))
		for _, bl := range blocks.Catalog {
			if bl.Group != g {
				continue
			}
			covers = append(covers, "block:"+bl.ID)
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", bl.Title, bl.Summary)
			if bl.Doc != "" {
				fmt.Fprintf(&b, "\n%s\n", bl.Doc)
			}
			if bl.GitOnly {
				b.WriteString("\n> [!NOTE]\n> Add this in git: this block can't be added in the browser.\n")
			}
			b.WriteString("\n| Field | Required | What it is |\n|---|---|---|\n")
			for _, f := range bl.Fields {
				req := ""
				if f.Required {
					req = "yes"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", f.Name, req, strings.ReplaceAll(f.Description, "|", `\|`))
			}
			lang := map[string]string{".md": "markdown", ".sh": "sh"}[path.Ext(bl.Insert.Open)] // the example is the file it opens
			if lang == "" {
				lang = "yaml"
			}
			fmt.Fprintf(&b, "\nExample:\n\n```%s\n%s\n```\n", lang, strings.TrimRight(bl.Example, "\n"))
		}
		body := b.String()
		pages = append(pages, Page{Slug: "authors/blocks/" + strings.ToLower(g), Section: "authors", Title: "Building blocks: " + g,
			Roles: []string{"author"}, Covers: covers, Order: 100 + i, Headings: headings(body), Body: body})
	}
	return pages
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
