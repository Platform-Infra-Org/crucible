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

// Crucible prices aws labs itself; lint refuses a resource type it can't price that the lab doesn't declare under
// aws.prices, with no network and no AWS (no price check runs at all).
func TestLintRefusesUnpricedTypesOffline(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../examples/forge-401")); err != nil {
		t.Fatal(err)
	}
	tf := `resource "aws_kinesis_stream" "s" {
  name        = "s-${var.crucible_lab_id}"
  shard_count = 1
}
`
	if err := os.WriteFile(filepath.Join(dir, "modules/01-cloud-heat/lab/terraform/stream.tf"), []byte(tf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1") // any network call would fail
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 || !strings.Contains(out.String(), "aws_kinesis_stream") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
}
