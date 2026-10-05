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
