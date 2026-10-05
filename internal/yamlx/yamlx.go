// Package yamlx reads YAML files strictly and adds a human-friendly Duration type.
package yamlx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
