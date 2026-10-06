package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/content"
	"crucible/internal/infracost"
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

func TestPriceCheck(t *testing.T) {
	tr, probs := content.Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	price := func(p string) infracost.Runner {
		return func(context.Context, string, []string, ...string) ([]byte, error) {
			return []byte(`{"projects":[{}],"totalHourlyCost":"` + p + `"}`), nil
		}
	}
	var out bytes.Buffer
	if p := priceCheck(tr, &out, nil); len(p) != 0 || !strings.Contains(out.String(), "price check skipped") {
		t.Fatalf("no key: a note, no problems: %v %q", p, out.String())
	}
	if p := priceCheck(tr, &out, price("0.04")); len(p) != 0 {
		t.Fatalf("within the ceiling: %v", p)
	}
	if p := priceCheck(tr, &out, price("0.06")); len(p) != 1 || !strings.Contains(p[0].Msg, "above aws.max_hourly_usd") {
		t.Fatalf("over the ceiling: %v", p)
	}
	failing := func(context.Context, string, []string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	if p := priceCheck(tr, &out, failing); len(p) != 1 {
		t.Fatalf("a failing estimate fails lint: %v", p)
	}
}

// infracost never runs on content lint has just rejected.
func TestLintSkipsPriceCheckOnProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../examples/forge-401")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "modules/01-cloud-heat/lab/terraform/p.tf"), []byte(`provider "aws" {}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	old := priceRunner
	priceRunner = func() infracost.Runner {
		return func(context.Context, string, []string, ...string) ([]byte, error) {
			ran = true
			return []byte(`{"projects":[{}],"totalHourlyCost":"0.01"}`), nil
		}
	}
	defer func() { priceRunner = old }()
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 || ran {
		t.Fatalf("exit %d, infracost ran: %v\n%s", code, ran, out.String())
	}
}
