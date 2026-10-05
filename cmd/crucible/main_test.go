package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintExamplesPass(t *testing.T) {
	for _, dir := range []string{"../../examples/forge-101", "../../examples/platform"} {
		var out bytes.Buffer
		if code := lint(dir, &out); code != 0 {
			t.Fatalf("%s: exit %d\n%s", dir, code, out.String())
		}
	}
}

func TestLintReportsProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "training.yaml"), []byte("id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "title is required") {
		t.Fatalf("output: %s", out.String())
	}
}
