package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testdata/values.yaml is a copy of the chart's values.yaml with the keys a release pins; integration re-runs
// this against deploy/helm/crucible/values.yaml.
const (
	srv = "ghcr.io/platform-infra-org/crucible@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ws  = "ghcr.io/platform-infra-org/crucible-workspace@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tb  = "ghcr.io/platform-infra-org/crucible-toolbox@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func fixture(t *testing.T) (path string, before []byte) {
	t.Helper()
	b, err := os.ReadFile("testdata/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, b
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPins(t *testing.T) {
	path, before := fixture(t)
	if err := run([]string{"-values", path, "-server", srv, "-workspace", ws, "-toolbox", tb}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, want := decode(t, after), decode(t, before)

	img := got["image"].(map[string]any)
	if img["repository"] != "ghcr.io/platform-infra-org/crucible" || img["digest"] != "sha256:"+strings.Repeat("a", 64) {
		t.Errorf("image = %v", img)
	}
	if got["awsLabs"].(map[string]any)["workspaceImage"] != ws {
		t.Errorf("workspaceImage = %v", got["awsLabs"].(map[string]any)["workspaceImage"])
	}
	if got["toolboxImage"] != tb {
		t.Errorf("toolboxImage = %v", got["toolboxImage"])
	}

	// Everything else is untouched.
	wimg := want["image"].(map[string]any)
	wimg["repository"], wimg["digest"] = img["repository"], img["digest"]
	want["awsLabs"].(map[string]any)["workspaceImage"] = ws
	want["toolboxImage"] = tb
	if !reflect.DeepEqual(got, want) {
		t.Errorf("other keys changed:\n got %v\nwant %v", got, want)
	}
	if !strings.Contains(string(after), "# cluster labs: the toolbox image by digest") {
		t.Error("comments were dropped")
	}
}

func TestRefusesNonDigestRefs(t *testing.T) {
	for _, bad := range []string{
		"ghcr.io/platform-infra-org/crucible:v1.2.3",
		"ghcr.io/platform-infra-org/crucible@sha256:abc",
		"ghcr.io/platform-infra-org/crucible@sha256:" + strings.Repeat("A", 64),
		"@sha256:" + strings.Repeat("a", 64),
		"",
	} {
		for _, flag := range []string{"-server", "-workspace", "-toolbox"} {
			path, before := fixture(t)
			args := map[string]string{"-server": srv, "-workspace": ws, "-toolbox": tb}
			args[flag] = bad
			err := run([]string{"-values", path, "-server", args["-server"], "-workspace", args["-workspace"], "-toolbox", args["-toolbox"]})
			if err == nil {
				t.Errorf("%s %q: accepted", flag, bad)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Errorf("%s %q: file changed on a refusal", flag, bad)
			}
		}
	}
}

func TestRefusesMissingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte("image:\n  repository: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-values", path, "-server", srv, "-workspace", ws, "-toolbox", tb}); err == nil {
		t.Error("a values file without the pinned keys was accepted")
	}
}
