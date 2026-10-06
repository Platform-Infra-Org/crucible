package infracost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHourlyPricesALocalCopy(t *testing.T) {
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "main.tf"), []byte(`resource "aws_instance" "x" {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var dir string
	run := func(_ context.Context, d string, env []string, args ...string) ([]byte, error) {
		dir = d
		tf, _ := os.ReadFile(filepath.Join(d, "crucible.tf"))
		vars, _ := os.ReadFile(filepath.Join(d, "crucible.auto.tfvars.json"))
		var v map[string]string
		_ = json.Unmarshal(vars, &v)
		if !strings.Contains(string(tf), "region = var.crucible_region") || v["crucible_region"] != "us-east-2" {
			t.Fatalf("infracost must see Crucible's provider in the lab's region: %s %s", tf, vars)
		}
		if _, err := os.Stat(filepath.Join(d, "main.tf")); err != nil || !slices.Contains(env, "INFRACOST_SKIP_UPDATE_CHECK=true") {
			t.Fatalf("module copied, quiet CLI: %v %v", err, env)
		}
		if !slices.Equal(args, []string{"breakdown", "--path", ".", "--format", "json", "--log-level", "warn", "--no-cache"}) {
			t.Fatalf("args %v", args)
		}
		return []byte(`{"projects":[{"metadata":{}}],"totalHourlyCost":"0.0104","totalMonthlyCost":"7.592"}`), nil
	}
	h, err := Hourly(context.Background(), run, module, "us-east-2")
	if err != nil || h != 0.0104 {
		t.Fatalf("hourly %v %v", h, err)
	}
	if dir == module {
		t.Fatal("infracost runs on a copy: the content export is shared and immutable")
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Fatal("the copy is removed afterwards")
	}
	for _, out := range []string{`{"projects":[{}],"totalHourlyCost":"-1"}`, `{"projects":[{}],"totalHourlyCost":"NaN"}`, `<html>`, `{}`,
		`{"error":"invalid API key"}`, `{"projects":[{"name":"x"}]}`, `{"totalHourlyCost":null}`,
		`{"totalHourlyCost":null,"projects":[{"metadata":{"errors":[{"message":"parse failed"}]}}]}`,
		`{"totalHourlyCost":"0.01","projects":[{"metadata":{}},{"metadata":{"errors":[{"message":"x"}]}}]}`} {
		bad := func(context.Context, string, []string, ...string) ([]byte, error) { return []byte(out), nil }
		if _, err := Hourly(context.Background(), bad, module, "us-east-2"); err == nil {
			t.Fatalf("%s is an error, never a price", out)
		}
	}
	usageOnly := func(context.Context, string, []string, ...string) ([]byte, error) {
		return []byte(`{"totalHourlyCost":null,"projects":[{"metadata":{"errors":[]}}]}`), nil
	}
	if h, err := Hourly(context.Background(), usageOnly, module, "us-east-2"); err != nil || h != 0 {
		t.Fatalf("usage-based only (e.g. one S3 bucket) is $0: %v %v", h, err)
	}
}

// Exec runs a fake "infracost" script: no AWS credential reaches it, and runaway output is cut off.
func TestExecScrubsEnvAndCapsOutput(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = flood ]; then yes x | head -c 9000000; exit 0; fi\n/usr/bin/env\n"
	if err := os.WriteFile(filepath.Join(bin, "infracost"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sekrit")
	t.Setenv("DATABASE_URL", "postgres://sekrit")
	t.Setenv("INFRACOST_API_KEY", "ico-123")
	out, err := Exec(context.Background(), t.TempDir(), []string{"HOME=/tmp/h"}, "breakdown")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "sekrit") || !strings.Contains(string(out), "INFRACOST_API_KEY=ico-123") || !strings.Contains(string(out), "HOME=/tmp/h") {
		t.Fatalf("env not scrubbed:\n%s", out)
	}
	if _, err := Exec(context.Background(), t.TempDir(), nil, "flood"); err == nil {
		t.Fatal("output over the cap is an error")
	}
}
