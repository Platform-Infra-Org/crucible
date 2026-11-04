package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/docs"
)

func TestLintExamplesPass(t *testing.T) {
	for _, dir := range []string{"../../examples/forge-101", "../../examples/forge-402", "../../examples/platform"} {
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

// Task 2's offline rule, surfaced through the CLI: lint refuses an aws resource type Crucible can't price that the lab
// doesn't declare under aws.prices, and names it.
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
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 || !strings.Contains(out.String(), "aws_kinesis_stream") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
}

// A negative budget is refused before anything runs (terraform would reject it only after init).
func TestLabsInitRefusesANegativeBudget(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no terraform or aws on PATH: a regression fails, it never reaches AWS
	if code := awsCmd([]string{"labs-init", "--region", "eu-west-1", "--budget-usd", "-5"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// An allowed-regions list without --region is refused before anything runs (terraform would refuse it only after init).
func TestLabsInitRefusesAllowedRegionsWithoutItsRegion(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no terraform or aws on PATH: a regression fails, it never reaches AWS
	if code := awsCmd([]string{"labs-init", "--region", "eu-west-1", "--allowed-regions", "eu-central-1, us-east-1"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestInviteNeedsOneEmail(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no terraform or aws on PATH: a regression fails, it never reaches AWS
	for _, args := range [][]string{{"invite"}, {"invite", "--name", "Alice"}, {"invite", "a@example.com", "b@example.com"}} {
		if code := awsCmd(args); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	// EMAIL then --name parses (flags stop at a plain argument): it gets as far as reading the stack, which fails here.
	if code := awsCmd([]string{"invite", "alice@example.com", "--name", "Alice Smith"}); code != 1 {
		t.Fatalf("invite EMAIL --name: exit %d, want 1 (past argument checks)", code)
	}
}

func TestVersionPrintsTheStampedVersion(t *testing.T) {
	var out bytes.Buffer
	if code := runMain([]string{"version"}, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != docs.Version {
		t.Fatalf("version: exit %d, %q", code, out.String())
	}
}

func TestUnknownCommandShowsUsage(t *testing.T) {
	var errw bytes.Buffer
	if code := runMain([]string{"nope"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "crucible serve") {
		t.Fatalf("exit %d, usage %q", code, errw.String())
	}
}

func TestAgentAndReadyAreSubcommands(t *testing.T) {
	for _, k := range []string{"CRUCIBLE_SERVER", "CRUCIBLE_TOKEN", "LAB_NAMESPACE"} {
		t.Setenv(k, "")
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"agent", "cluster"}, "crucible agent cluster up|down"},
		{[]string{"ready"}, "crucible ready [--json]"},
		{[]string{"agent"}, "crucible agent --server"},
	} {
		var errw bytes.Buffer
		code := runMain(c.args, io.Discard, &errw)
		if code != 2 || !strings.Contains(errw.String(), c.want) || strings.Contains(errw.String(), "crucible serve") {
			t.Errorf("%v: exit %d, stderr %q (want 2 and %q)", c.args, code, errw.String(), c.want)
		}
	}
	for _, args := range [][]string{{"agent", "-h"}, {"ready", "-h"}} {
		if code := runMain(args, io.Discard, io.Discard); code != 0 {
			t.Errorf("%v: exit %d, want 0", args, code)
		}
	}
}
