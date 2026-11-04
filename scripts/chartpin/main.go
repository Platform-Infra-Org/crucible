// chartpin pins a release's images into the chart's values.yaml by digest, in place, before `helm package`:
//
//	go run ./scripts/chartpin -values deploy/helm/crucible/values.yaml \
//	  -server <repo>@sha256:<64 hex> -workspace <repo>@sha256:<64 hex> -toolbox <repo>@sha256:<64 hex>
//
// It sets image.repository and image.digest (server), awsLabs.workspaceImage and toolboxImage, edits the YAML node
// tree so comments and key order survive, and refuses any reference that is not pinned by a full sha256 digest.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var digestRef = regexp.MustCompile(`^([^@\s]+)@(sha256:[0-9a-f]{64})$`)

func run(args []string) error {
	fs := flag.NewFlagSet("chartpin", flag.ContinueOnError)
	values := fs.String("values", "", "the chart's values.yaml, rewritten in place")
	server := fs.String("server", "", "server image <repo>@sha256:<digest>")
	workspace := fs.String("workspace", "", "workspace image <repo>@sha256:<digest>")
	toolbox := fs.String("toolbox", "", "toolbox image <repo>@sha256:<digest>")
	if err := fs.Parse(args); err != nil {
		return err
	}
	refs := map[string]string{"-server": *server, "-workspace": *workspace, "-toolbox": *toolbox}
	for name, ref := range refs {
		if !digestRef.MatchString(ref) {
			return fmt.Errorf("%s %q: want <repo>@sha256:<64 lowercase hex>", name, ref)
		}
	}
	m := digestRef.FindStringSubmatch(*server)

	b, err := os.ReadFile(*values)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("%s: %w", *values, err)
	}
	for path, v := range map[string]string{
		"image.repository":       m[1],
		"image.digest":           m[2],
		"awsLabs.workspaceImage": *workspace,
		"toolboxImage":           *toolbox,
	} {
		if err := set(&doc, path, v); err != nil {
			return fmt.Errorf("%s: %w", *values, err)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	return os.WriteFile(*values, out.Bytes(), 0o644)
}

// set replaces the scalar at a dotted path; the key must already exist (a missing key means the chart moved).
func set(doc *yaml.Node, path string, v string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("not a YAML document")
	}
	n := doc.Content[0]
	for _, key := range strings.Split(path, ".") {
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: not a mapping above %q", path, key)
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
			}
		}
		if next == nil {
			return fmt.Errorf("%s: key %q not found", path, key)
		}
		n = next
	}
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s: not a scalar", path)
	}
	n.Tag, n.Value, n.Style = "!!str", v, 0
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "chartpin:", err)
		os.Exit(1)
	}
}
