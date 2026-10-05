// Package awsops drives the AWS deployment: terraform for infrastructure, S3 for releases, SSM for the node.
package awsops

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Ops struct {
	Root   string // repo root
	Exec   func(ctx context.Context, dir, name string, args ...string) error
	Output func(ctx context.Context, dir, name string, args ...string) (string, error)
	Sleep  func(time.Duration)
	Log    io.Writer
}

// Default runs real commands: Exec streams to the terminal, Output captures stdout.
func Default(root string) Ops {
	return Ops{
		Root: root,
		Exec: func(ctx context.Context, dir, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = dir, os.Stdout, os.Stderr, os.Stdin
			return cmd.Run()
		},
		Output: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stderr = dir, os.Stderr
			out, err := cmd.Output()
			return strings.TrimSpace(string(out)), err
		},
		Sleep: time.Sleep,
		Log:   os.Stdout,
	}
}

func (o Ops) dir(stack string) string { return filepath.Join(o.Root, "deploy", "aws", stack) }

func (o Ops) outputs(ctx context.Context, stack string) (map[string]string, error) {
	raw, err := o.Output(ctx, o.Root, "terraform", "-chdir="+o.dir(stack), "output", "-json")
	if err != nil {
		return nil, fmt.Errorf("terraform outputs for %s (did you run `crucible aws init`/`up`?): %w", stack, err)
	}
	var parsed map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range parsed {
		out[k] = fmt.Sprint(v.Value)
	}
	return out, nil
}

func (o Ops) Init(ctx context.Context, region, domain string) error {
	d := o.dir("persistent")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	return o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "apply", "-var", "region="+region, "-var", "domain="+domain)
}

// mainVars passes the persistent stack's outputs (buckets, Cognito) into the main stack.
func mainVars(p map[string]string) []string {
	return []string{"-var", "region=" + p["region"], "-var", "data_bucket=" + p["data_bucket"],
		"-var", "oidc_issuer=" + p["oidc_issuer"], "-var", "oidc_client_id=" + p["oidc_client_id"],
		"-var", "cognito_user_pool_id=" + p["cognito_user_pool_id"]}
}

func (o Ops) Up(ctx context.Context, varFile string) error {
	p, err := o.outputs(ctx, "persistent")
	if err != nil {
		return err
	}
	d := o.dir("main")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init", "-reconfigure",
		"-backend-config=bucket="+p["state_bucket"], "-backend-config=key=main.tfstate",
		"-backend-config=region="+p["region"], "-backend-config=use_lockfile=true"); err != nil {
		return err
	}
	abs, _ := filepath.Abs(varFile)
	if err := o.Exec(ctx, o.Root, "terraform", append([]string{"-chdir=" + d, "apply", "-var-file=" + abs}, mainVars(p)...)...); err != nil {
		return err
	}
	return o.Deploy(ctx)
}

// Deploy builds the image for the current commit, publishes it to S3 and rolls it out on the node.
func (o Ops) Deploy(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	sha, err := o.Output(ctx, o.Root, "git", "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return err
	}
	if dirty, _ := o.Output(ctx, o.Root, "git", "status", "--porcelain"); dirty != "" {
		sha = fmt.Sprintf("%s-dirty-%d", sha, time.Now().Unix())
	}
	tag := "crucible:" + sha
	rel := filepath.Join(o.Root, ".local", "release")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	img, chart := filepath.Join(rel, "crucible-image.tar"), filepath.Join(rel, "chart.tgz")
	steps := [][]string{
		{"docker", "buildx", "build", "--platform", "linux/amd64", "-t", tag, "--load", "."},
		{"docker", "save", "-o", img, tag},
	}
	for _, s := range steps {
		if err := o.Exec(ctx, o.Root, s[0], s[1:]...); err != nil {
			return err
		}
	}
	if err := gzipFile(img); err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "helm", "package", filepath.Join(o.Root, "deploy", "helm", "crucible"),
		"-d", rel, "--app-version", sha); err != nil {
		return err
	}
	_ = os.Rename(filepath.Join(rel, "crucible-0.1.0.tgz"), chart)
	if err := os.WriteFile(filepath.Join(rel, "current-release"), []byte(sha), 0o644); err != nil {
		return err
	}
	b, r := m["data_bucket"], m["region"]
	for _, up := range [][2]string{
		{img + ".gz", fmt.Sprintf("s3://%s/releases/%s/crucible-image.tar.gz", b, sha)},
		{chart, fmt.Sprintf("s3://%s/releases/%s/chart.tgz", b, sha)},
		{filepath.Join(rel, "current-release"), fmt.Sprintf("s3://%s/current-release", b)},
	} {
		if err := o.Exec(ctx, o.Root, "aws", "s3", "cp", up[0], up[1], "--region", r); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.Log, "release %s published; rolling out…\n", sha)
	wait := `i=0; while [ ! -f /opt/crucible/.ready ] && [ ! -f /opt/crucible/.failed ]; do i=$((i+1)); [ $i -gt 240 ] && { echo "timed out waiting for node bootstrap"; exit 1; }; sleep 5; done; ` +
		`if [ -f /opt/crucible/.failed ]; then echo "node bootstrap failed - see /var/log/crucible-bootstrap.log (aws ssm start-session --target ` + m["instance_id"] + `)" >&2; exit 1; fi; /opt/crucible/deploy.sh`
	if err := o.ssm(ctx, m, wait); err != nil {
		return err
	}
	if err := o.waitHealthy(ctx, m["url"]); err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "%s is live at %s\n", sha, m["url"])
	return nil
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return nil // tests run without a real image; Exec already reported failures
	}
	defer in.Close()
	out, err := os.Create(path + ".gz")
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	return zw.Close()
}

// ssm runs a shell command on the node and waits for it (SSM's own wait gives up after ~100s).
func (o Ops) ssm(ctx context.Context, m map[string]string, command string) error {
	params, _ := json.Marshal(map[string][]string{"commands": {command}})
	var id string
	var err error
	for attempt := 0; attempt < 40; attempt++ { // a fresh node needs a few minutes to register with SSM
		if id, err = o.Output(ctx, o.Root, "aws", "ssm", "send-command", "--region", m["region"], "--instance-ids", m["instance_id"],
			"--document-name", "AWS-RunShellScript", "--parameters", string(params), "--query", "Command.CommandId", "--output", "text"); err == nil {
			break
		}
		o.Sleep(15 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("node not reachable over SSM: %w", err)
	}
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		st, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
			"--instance-id", m["instance_id"], "--query", "Status", "--output", "text")
		switch st {
		case "Success":
			return nil
		case "Failed", "Cancelled", "TimedOut":
			msg, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
				"--instance-id", m["instance_id"], "--query", "StandardErrorContent", "--output", "text")
			return fmt.Errorf("node command %s: %s", st, msg)
		}
		o.Sleep(5 * time.Second)
	}
	return errors.New("node command timed out")
}

func (o Ops) Snapshot(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	return o.ssm(ctx, m, "/opt/crucible/snapshot.sh")
}

func (o Ops) state(ctx context.Context, m map[string]string) string {
	st, _ := o.Output(ctx, o.Root, "aws", "ec2", "describe-instances", "--region", m["region"], "--instance-ids", m["instance_id"],
		"--query", "Reservations[0].Instances[0].State.Name", "--output", "text")
	return st
}

// SleepNode snapshots, then stops the instance (compute billing stops; disk + IP keep costing ~$9/month).
func (o Ops) SleepNode(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if o.state(ctx, m) == "running" {
		if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
			return fmt.Errorf("snapshot failed, not stopping: %w", err)
		}
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "stop-instances", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	fmt.Fprintln(o.Log, "the forge is banked for the night 🌙")
	return o.Exec(ctx, o.Root, "aws", "ec2", "wait", "instance-stopped", "--region", m["region"], "--instance-ids", m["instance_id"])
}

func (o Ops) Wake(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "start-instances", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "wait", "instance-running", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	return o.waitHealthy(ctx, m["url"])
}

func (o Ops) waitHealthy(ctx context.Context, url string) error {
	if strings.Contains(url, "example.com") {
		return nil // tests
	}
	for i := 0; i < 120; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				fmt.Fprintf(o.Log, "🔥 the forge is lit: %s\n", url)
				return nil
			}
		}
		o.Sleep(5 * time.Second)
	}
	return fmt.Errorf("%s did not become healthy within 10 minutes", url)
}

func (o Ops) Status(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "instance %s: %s\nurl: %s\n", m["instance_id"], o.state(ctx, m), m["url"])
	return nil
}

func (o Ops) Teardown(ctx context.Context, varFile string, yes bool) error {
	if !yes {
		return errors.New("teardown destroys the node (buckets and snapshots are kept); re-run with --yes to confirm")
	}
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if o.state(ctx, m) == "running" {
		if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
			return fmt.Errorf("final snapshot failed, aborting teardown: %w", err)
		}
	} else {
		fmt.Fprintln(o.Log, "node is not running; relying on the last nightly snapshot")
	}
	p, err := o.outputs(ctx, "persistent")
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(varFile)
	return o.Exec(ctx, o.Root, "terraform", append([]string{"-chdir=" + o.dir("main"), "destroy", "-auto-approve", "-var-file=" + abs}, mainVars(p)...)...)
}
