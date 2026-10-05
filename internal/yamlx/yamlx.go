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
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
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
