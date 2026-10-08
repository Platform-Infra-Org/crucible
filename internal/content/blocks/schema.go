package blocks

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"crucible/internal/content"
	"crucible/internal/yamlx"
)

// Kinds are the YAML files the editor validates with a schema, by kind.
var Kinds = map[string]any{"training": content.Training{}, "module": content.Module{}, "quiz": content.Quiz{}, "lab": content.Lab{}}

const durationPattern = `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`

// itemSchema is one module item: exactly one of reading, quiz, lab.
var itemSchema = map[string]any{"type": "object", "minProperties": 1, "maxProperties": 1, "additionalProperties": false,
	"properties": map[string]any{
		"reading": map[string]any{"type": "string", "description": "A Markdown file in the module, e.g. reading/intro.md."},
		"quiz":    map[string]any{"type": "string", "enum": []string{"quiz.yaml"}, "description": "The module's quiz.yaml."},
		"lab":     map[string]any{"type": "string", "description": "The lab's folder in the module, e.g. lab."},
	}}

// docsLink ends every hover (spec §4: editor hovers link to the building-blocks reference). Monaco renders
// markdownDescription as Markdown, so the plain description is escaped first. Monaco drops relative links from hovers;
// a file: URI survives, and the editor's link opener opens its path (/docs/…) in a new tab.
const docsLink = "[Building blocks: every key](file:///docs/authors/building-blocks)"

var mdEscape = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "<", `\<`, ">", `\>`, "[", `\[`, "]", `\]`, "`", "\\`")

// Schema is the JSON Schema (draft-07 subset) of one file kind, generated from the content types and Fields.
func Schema(kind string) map[string]any {
	s := schemaOf(reflect.TypeOf(Kinds[kind]))
	s["$schema"] = "http://json-schema.org/draft-07/schema#"
	return s
}

func schemaOf(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case reflect.TypeOf(yamlx.Duration(0)):
		return map[string]any{"type": "string", "pattern": durationPattern}
	case reflect.TypeOf(yaml.Node{}):
		return map[string]any{} // any value; the loader checks it per question type
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Struct:
		props, required := map[string]any{}, []string{}
		for _, f := range YAMLFields(t) {
			key := t.Name() + "." + f.Key
			p := schemaOf(f.Type)
			if key == "Module.items" {
				p = map[string]any{"type": "array", "items": itemSchema}
			}
			d := Fields[key]
			p["description"] = d.Description
			p["markdownDescription"] = mdEscape.Replace(d.Description) + "\n\n" + docsLink
			if len(d.Enum) > 0 {
				p["enum"] = d.Enum
			}
			if d.Min != nil {
				p["minimum"] = *d.Min
			}
			if d.Max != nil {
				p["maximum"] = *d.Max
			}
			if d.Required {
				required = append(required, f.Key)
			}
			props[f.Key] = p
		}
		s := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	panic("blocks: no schema for " + t.String())
}

// YAMLField is one key YAML reads into a struct.
type YAMLField struct {
	Key  string
	Type reflect.Type
}

// YAMLFields lists t's exported fields that YAML reads, by key, in declaration order.
func YAMLFields(t reflect.Type) []YAMLField {
	var out []YAMLField
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if f.IsExported() && name != "" && name != "-" {
			out = append(out, YAMLField{name, f.Type})
		}
	}
	return out
}
