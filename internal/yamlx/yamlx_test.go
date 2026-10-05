package yamlx

import (
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
