// Package yamlx reads YAML files strictly and adds a human-friendly Duration type.
package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration accepts Go duration strings such as "90m" or "2h".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// ReadFile decodes path into out, rejecting unknown keys. A missing optional file is not an error.
func ReadFile(path string, out any, required bool) error {
	return read(path, out, required, true)
}

// ReadLoose decodes path into out, ignoring unknown keys (used for docker compose files).
func ReadLoose(path string, out any) error { return read(path, out, true, false) }

func read(path string, out any, required, strict bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: file not found", filepath.Base(path))
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(strict)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// MarshalYAML writes durations the way people type them: "2h", "45m", "1h30m".
func (d Duration) MarshalYAML() (any, error) {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s, nil
}

// Update sets top-level keys of the YAML mapping in path and writes it back, keeping the comments and order of
// untouched keys. A nil value removes the key; new keys are appended in sorted order; a missing file starts empty.
func Update(path string, set map[string]any) error {
	var doc yaml.Node
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 { // missing, empty or comment-only file
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	if m.Kind == yaml.ScalarNode && m.ShortTag() == "!!null" { // "null" or "~" is an empty mapping
		m = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content[0] = m
	}
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level is not a mapping", filepath.Base(path))
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		at := -1
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				at = i
				break
			}
		}
		if set[k] == nil {
			if at >= 0 {
				m.Content = slices.Delete(m.Content, at, at+2)
			}
			continue
		}
		var val yaml.Node
		if err := val.Encode(set[k]); err != nil {
			return err
		}
		if at >= 0 {
			val.LineComment = m.Content[at+1].LineComment
			m.Content[at+1] = &val
		} else {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &val)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// Insert adds item to the YAML text src and returns the new text and the 1-based line where item starts. Only the added
// lines change: comments, blank lines, quoting and flow styles elsewhere stay byte for byte (a re-encode would reformat
// the whole file and bury the change in the reviewer's diff). CRLF files stay CRLF.
//
// path walks mapping keys; a "key=value" segment picks the entry of a list whose mapping has key: value. Without set,
// item (YAML for one entry, without the leading "- ") is appended to the list at path, and a missing or empty last key
// is created at the end of its mapping. With set, path's last key must not exist yet and gets item as its value.
// ponytail: positions come from yaml.v3 nodes; an entry's end is its deepest node's line (block scalars counted), so a
// flow collection whose closing bracket sits alone on a later line, or a folded (>) scalar, can end up mis-placed.
func Insert(src []byte, path []string, item string, set bool) (out []byte, line int, err error) {
	// ponytail: a safety net for positions yaml.v3 reports oddly; the result must also parse, or the edit is refused.
	defer func() {
		if r := recover(); r != nil {
			out, line, err = nil, 0, fmt.Errorf("can't insert here (%v); edit the file by hand", r)
		}
		if err == nil {
			var chk yaml.Node
			if yaml.Unmarshal(out, &chk) != nil {
				out, line, err = nil, 0, errors.New("the entry doesn't fit there; edit the file by hand")
			}
		}
	}()
	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	var lines []string
	if text != "" {
		lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	if strings.Contains(text, "\r") { // a lone CR is a line break to YAML but not to us
		return nil, 0, errors.New("the file has stray carriage returns; fix it first")
	}
	item = strings.TrimRight(strings.ReplaceAll(item, "\r\n", "\n"), "\n")
	if len(path) == 0 {
		return nil, 0, errors.New("nothing to insert into")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, 0, fmt.Errorf("the YAML doesn't parse (%v); fix it first", err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 { // empty or comment-only file
		if len(path) != 1 {
			return nil, 0, fmt.Errorf("%s is missing", path[0])
		}
		return splice(lines, len(lines), block(0, path[0], item, set), nl), len(lines) + 2, nil
	}
	cur := doc.Content[0]
	for i, seg := range path {
		last := i == len(path)-1
		if k, v, ok := strings.Cut(seg, "="); ok {
			if cur.Kind != yaml.SequenceNode {
				return nil, 0, fmt.Errorf("%s: not a list", seg)
			}
			var hit *yaml.Node
			for _, it := range cur.Content {
				if _, val := lookup(it, k); val != nil && val.Value == v {
					hit = it
					break
				}
			}
			if hit == nil {
				return nil, 0, fmt.Errorf("no entry with %s", seg)
			}
			cur = hit
			continue
		}
		if cur.Kind != yaml.MappingNode {
			return nil, 0, fmt.Errorf("%s: not a mapping", seg)
		}
		k, v := lookup(cur, seg)
		if v == nil || (v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null") {
			if !last {
				return nil, 0, fmt.Errorf("%s is missing", seg)
			}
			if v != nil { // "hints:" with nothing after it: the entries go right under the key
				if !strings.HasSuffix(strings.TrimSpace(lines[k.Line-1]), ":") {
					return nil, 0, fmt.Errorf("%s is set to null; remove that line first", seg)
				}
				return splice(lines, k.Line, entry(k.Column-1+2, item, set), nl), k.Line + 1, nil
			}
			at := end(cur)
			return splice(lines, at, block(cur.Content[0].Column-1, seg, item, set), nl), at + 2, nil
		}
		cur = v
	}
	if set {
		return nil, 0, fmt.Errorf("%s is already set; change it in place", path[len(path)-1])
	}
	if cur.Kind != yaml.SequenceNode {
		return nil, 0, fmt.Errorf("%s is not a list", path[len(path)-1])
	}
	if cur.Style&yaml.FlowStyle != 0 {
		return flowAppend(lines, cur, item, nl)
	}
	first := lines[cur.Content[0].Line-1]
	at := end(cur)
	return splice(lines, at, entry(len(first)-len(strings.TrimLeft(first, " ")), item, false), nl), at + 1, nil
}

func lookup(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// end is the last line (1-based) n's text occupies.
func end(n *yaml.Node) int {
	last := n.Line
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		last += strings.Count(strings.TrimRight(n.Value, "\n"), "\n") + 1
	}
	for _, c := range n.Content {
		last = max(last, end(c))
	}
	return last
}

// entry indents item as one list entry ("- " first) or, with set, as a mapping value.
func entry(ind int, item string, set bool) []string {
	pad := strings.Repeat(" ", ind)
	var out []string
	for i, l := range strings.Split(item, "\n") {
		switch {
		case l == "":
			out = append(out, "")
		case set:
			out = append(out, pad+l)
		case i == 0:
			out = append(out, pad+"- "+l)
		default:
			out = append(out, pad+"  "+l)
		}
	}
	return out
}

func block(ind int, key, item string, set bool) []string {
	return append([]string{strings.Repeat(" ", ind) + key + ":"}, entry(ind+2, item, set)...)
}

func splice(lines []string, at int, ins []string, nl string) []byte {
	out := slices.Concat(lines[:at], ins, lines[at:])
	return []byte(strings.Join(out, nl) + nl)
}

// flowAppend adds a one-line item to a [flow] list that opens and closes on one line.
func flowAppend(lines []string, seq *yaml.Node, item, nl string) ([]byte, int, error) {
	if strings.Contains(item, "\n") {
		return nil, 0, errors.New("a [flow] list takes one-line entries; write the list one entry per line first")
	}
	l := lines[seq.Line-1]
	start := seq.Column - 1 // the '['
	depth, quote := 0, byte(0)
	for i := start; i < len(l); i++ {
		c := l[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			if depth--; depth > 0 {
				continue
			}
			j := i
			for j > start+1 && l[j-1] == ' ' {
				j--
			}
			sep := ", "
			if strings.TrimSpace(l[start+1:i]) == "" {
				sep = ""
			}
			lines[seq.Line-1] = l[:j] + sep + item + l[i:]
			return []byte(strings.Join(lines, nl) + nl), seq.Line, nil
		}
	}
	return nil, 0, errors.New("a [flow] list spread over several lines: edit it by hand")
}
