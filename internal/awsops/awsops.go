// Package awsops drives the AWS deployment: terraform for infrastructure, S3 for releases, SSM for the node.
package awsops

import (
	"cmp"
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
	"slices"
	"strconv"
	"strings"
	"time"
)

type Ops struct {
	Root   string // repo root
	Exec   func(ctx context.Context, dir, name string, args ...string) error
	Output func(ctx context.Context, dir, name string, args ...string) (string, error)
	Sleep  func(time.Duration)
	Get    func(url string) (*http.Response, error) // healthz probe; nil = real client with timeout
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
			var stderr strings.Builder
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stderr = dir, io.MultiWriter(os.Stderr, &stderr)
			out, err := cmd.Output()
			if err != nil && stderr.Len() > 0 { // callers match on the AWS error code
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
			}
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
		switch x := v.Value.(type) {
		case nil:
		case string:
			out[k] = x
		default: // numbers and lists as JSON, which is also HCL: they can be passed back as -var
			b, _ := json.Marshal(x)
			out[k] = string(b)
		}
	}
	need, first := map[string]string{"persistent": "state_bucket", "main": "instance_id", "labs": "lab_role_arn"}[stack], map[string]string{"persistent": "init", "main": "up", "labs": "labs-init"}[stack]
	if out[need] == "" {
		return nil, fmt.Errorf("the %s stack has no %s output: run `crucible aws %s` first", stack, need, first)
	}
	return out, nil
}

// InitOpts are `crucible aws init`'s flags. An empty Access, Domain or LocalPort keeps the stack's current value (a
// new stack: public, no domain, 18080), so a re-run never flips an ssm install back to public.
type InitOpts struct {
	Region, Domain, Access string
	LocalPort              int
}

func (o Ops) Init(ctx context.Context, in InitOpts) error {
	d := o.dir("persistent")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	cur, _ := o.outputs(ctx, "persistent") // nil on a new stack
	access, domain, port := cmp.Or(in.Access, cur["access"], "public"), cmp.Or(in.Domain, cur["domain"]), in.LocalPort
	if port == 0 {
		port, _ = strconv.Atoi(cur["local_port"])
	}
	if port == 0 {
		port = 18080
	}
	switch {
	case access != "public" && access != "ssm":
		return fmt.Errorf("--access must be public or ssm, not %q", access)
	case access == "public" && domain == "":
		return errors.New("--access public needs --domain (Crucible's public hostname)")
	case access == "ssm" && in.Domain != "":
		return errors.New("--domain is only for --access public: an ssm install is reached at http://localhost:<port>")
	case port < 1024 || port > 65535:
		return fmt.Errorf("--local-port %d: use a port from 1024 to 65535", port)
	}
	args := []string{"-chdir=" + d, "apply", "-var", "region=" + in.Region, "-var", "access=" + access, "-var", "local_port=" + strconv.Itoa(port)}
	if access == "public" {
		args = append(args, "-var", "domain="+domain)
	}
	return o.Exec(ctx, o.Root, "terraform", args...)
}

// mainVars passes the persistent stack's outputs (buckets, Cognito, access) into the main stack. A persistent stack
// made before access existed has no access or public_url: main then stays public.
func mainVars(p map[string]string) []string {
	v := []string{"-var", "region=" + p["region"], "-var", "data_bucket=" + p["data_bucket"],
		"-var", "oidc_issuer=" + p["oidc_issuer"], "-var", "oidc_client_id=" + p["oidc_client_id"],
		"-var", "cognito_user_pool_id=" + p["cognito_user_pool_id"]}
	for _, k := range []string{"access", "public_url"} {
		if p[k] != "" {
			v = append(v, "-var", k+"="+p[k])
		}
	}
	return v
}

// Up applies the main stack (snapshotting a running node first, in case the apply replaces it), then deploys.
// noLabs turns aws labs off; without it, Up refuses to drop the lab wiring main already has.
func (o Ops) Up(ctx context.Context, varFile string, noSnapshot, noLabs bool) error {
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
	wired := ""                                       // the lab role main is wired to now
	if m, err := o.outputs(ctx, "main"); err == nil { // a node already exists
		wired = m["lab_role_arn"]
		if err := o.snapshotFirst(ctx, m, noSnapshot, "applying"); err != nil {
			return err
		}
	}
	vars := mainVars(p)
	if !noLabs {
		l, err := o.outputs(ctx, "labs")
		switch {
		case err == nil:
			vars = append(vars, labVars(l)...)
		case wired != "":
			// Dropping the wiring strands running aws labs: no credential refresh, no destroy, no sweep.
			return fmt.Errorf("aws labs are on (%s) but the labs stack outputs cannot be read: %w\n"+
				"run `up` where deploy/aws/labs/terraform.tfstate lives, or pass --no-labs to turn aws labs off", wired, err)
		}
	} else if wired != "" {
		fmt.Fprintf(o.Log, "WARNING: --no-labs: turning aws labs off; running aws labs lose their credentials and cleanup\n")
	}
	abs, _ := filepath.Abs(varFile)
	if err := o.Exec(ctx, o.Root, "terraform", append([]string{"-chdir=" + d, "apply", "-var-file=" + abs}, vars...)...); err != nil {
		return err
	}
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "Point an A record %s → %s now (skip if route53_zone_id is set).\n", m["domain"], m["public_ip"])
	return o.Deploy(ctx)
}

// LabNet is where lab modules launch when the account has no default VPC (labs-init --vpc-id --subnet-ids).
type LabNet struct {
	VPCID     string
	SubnetIDs []string
}

// commaList splits a comma-separated output, dropping blanks.
func commaList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// LabsInit creates the shared lab account stack (roles, permission boundary, state bucket, billing export). Run it
// with credentials for the lab account; pass the Crucible account when that is a different one. budgetUSD (> 0) and
// budgetEmails set the account's AWS Budget alert; each one left unset keeps the stack's current value (its outputs),
// so a re-run never resets or deletes the budget. A new stack starts from the defaults (no emails: no budget).
// allowedRegions (the regions labs may use) must include region; left empty it keeps the stack's current list, and a
// new stack allows region alone. net, both fields or neither, sets the lab network; empty keeps the stack's current one.
func (o Ops) LabsInit(ctx context.Context, region, crucibleAccount string, budgetUSD float64, budgetEmails, allowedRegions []string, net LabNet) error {
	if len(allowedRegions) > 0 && !slices.Contains(allowedRegions, region) {
		return fmt.Errorf("the allowed regions %s must include --region %s (the lab state bucket's region)", strings.Join(allowedRegions, ","), region)
	}
	d := o.dir("labs")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	args := []string{"-chdir=" + d, "apply", "-var", "region=" + region}
	if crucibleAccount != "" {
		args = append(args, "-var", "crucible_account_id="+crucibleAccount)
	}
	cur, _ := o.outputs(ctx, "labs") // nil on a new stack
	if budgetUSD > 0 {
		args = append(args, "-var", "budget_monthly_usd="+strconv.FormatFloat(budgetUSD, 'f', -1, 64))
	} else if v := cur["budget_monthly_usd"]; v != "" {
		args = append(args, "-var", "budget_monthly_usd="+v)
	}
	if len(budgetEmails) > 0 {
		b, _ := json.Marshal(budgetEmails) // a JSON list is an HCL list
		args = append(args, "-var", "budget_emails="+string(b))
	} else if v := cur["budget_emails"]; v != "" && v != "[]" {
		args = append(args, "-var", "budget_emails="+v)
	}
	if len(allowedRegions) == 0 { // keep the stack's list; a single region is always its --region, the default
		if v := cur["regions"]; strings.Contains(v, ",") {
			allowedRegions = strings.Split(v, ",")
			if !slices.Contains(allowedRegions, region) {
				return fmt.Errorf("the lab stack allows %s, which lacks --region %s: pass --allowed-regions with the list you want", v, region)
			}
		}
	}
	if len(allowedRegions) > 0 {
		b, _ := json.Marshal(allowedRegions) // a JSON list is an HCL list
		args = append(args, "-var", "allowed_regions="+string(b))
	}
	vpc, subnets := net.VPCID, net.SubnetIDs
	if vpc == "" && len(subnets) == 0 { // keep the stack's lab network
		vpc, subnets = cur["lab_vpc_id"], commaList(cur["lab_subnet_ids"])
	}
	if (vpc == "") != (len(subnets) == 0) {
		return errors.New("--vpc-id and --subnet-ids go together: pass both, or neither to keep the current lab network")
	}
	if vpc != "" {
		b, _ := json.Marshal(subnets) // a JSON list is an HCL list
		args = append(args, "-var", "lab_vpc_id="+vpc, "-var", "lab_subnet_ids="+string(b))
	}
	// The roles trust the node role by exact ARN: a guessed name fails silently, at the first AssumeLab.
	if m, err := o.outputs(ctx, "main"); err == nil && m["node_role_name"] != "" {
		args = append(args, "-var", "node_role_name="+m["node_role_name"])
	} else {
		fmt.Fprintln(o.Log, "note: main's outputs are not readable here (not `up` yet, or other credentials), so the lab roles "+
			"trust <name>-node (crucible-node by default). If crucible.tfvars sets another name, run labs-init again after `up`.")
	}
	return o.Exec(ctx, o.Root, "terraform", args...)
}

// labVars wires the shared lab account into the node; without the labs stack, aws labs stay off.
func labVars(l map[string]string) []string {
	return []string{"-var", "lab_role_arn=" + l["lab_role_arn"], "-var", "lab_ops_role_arn=" + l["ops_role_arn"],
		"-var", "lab_state_bucket=" + l["state_bucket"], "-var", "lab_state_region=" + l["state_region"], "-var", "lab_regions=" + l["regions"],
		"-var", "lab_plan_role_arn=" + l["plan_role_arn"], "-var", "lab_export_bucket=" + l["export_bucket"],
		"-var", "lab_export_prefix=" + l["export_prefix"], "-var", "lab_export_name=" + l["export_name"],
		"-var", "lab_vpc_id=" + l["lab_vpc_id"], "-var", "lab_subnet_ids=" + l["lab_subnet_ids"]}
}

// snapshotFirst backs up a running node before a step that may lose its disk ("applying", "tearing down").
func (o Ops) snapshotFirst(ctx context.Context, m map[string]string, noSnapshot bool, what string) error {
	if noSnapshot {
		fmt.Fprintf(o.Log, "WARNING: --no-snapshot: skipping the snapshot before %s; everything since the last backup may be LOST\n", what)
		return nil
	}
	st, err := o.state(ctx, m)
	if err != nil {
		return fmt.Errorf("not %s: %w (use --no-snapshot to skip the snapshot)", what, err)
	}
	switch st {
	case "running":
		if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
			return fmt.Errorf("snapshot failed, not %s (use --no-snapshot to skip it): %w", what, err)
		}
	case "stopped":
		fmt.Fprintln(o.Log, "warning: node is stopped; relying on the last snapshot taken before it slept")
	case "terminated": // nothing left to back up
	default:
		return fmt.Errorf("node is %s; wait until the node is stable (running or stopped) and retry", st)
	}
	return nil
}

// Deploy builds the image for the current commit, publishes it to S3 and rolls it out on the node.
func (o Ops) Deploy(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if err := o.requireRunning(ctx, m); err != nil {
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
	_ = os.Remove(chart) // never upload a stale chart under a new sha
	for _, old := range globOrNil(filepath.Join(rel, "crucible-*.tgz")) {
		_ = os.Remove(old)
	}
	if err := o.Exec(ctx, o.Root, "helm", "package", filepath.Join(o.Root, "deploy", "helm", "crucible"),
		"-d", rel, "--app-version", sha); err != nil {
		return err
	}
	pkgs, _ := filepath.Glob(filepath.Join(rel, "crucible-*.tgz"))
	if len(pkgs) != 1 {
		return fmt.Errorf("helm package produced %d chart archives in %s, want 1", len(pkgs), rel)
	}
	if err := os.Rename(pkgs[0], chart); err != nil {
		return err
	}
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
	wait := `i=0; while [ ! -f /opt/crucible/.ready ] && [ ! -f /opt/crucible/.failed ]; do i=$((i+1)); [ $i -gt 240 ] && { echo "timed out waiting for node bootstrap" >&2; exit 1; }; sleep 5; done; ` +
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

func globOrNil(pattern string) []string { m, _ := filepath.Glob(pattern); return m }

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
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
		if !strings.Contains(err.Error(), "InvalidInstanceId") && !strings.Contains(err.Error(), "not registered") {
			return fmt.Errorf("ssm send-command: %w", err) // AccessDenied, ExpiredToken, …: retrying won't help
		}
		o.Sleep(15 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("node not reachable over SSM: %w", err)
	}
	deadline := time.Now().Add(45 * time.Minute)
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
	if err := o.requireRunning(ctx, m); err != nil {
		return err
	}
	return o.ssm(ctx, m, "/opt/crucible/snapshot.sh")
}

func (o Ops) state(ctx context.Context, m map[string]string) (string, error) {
	st, err := o.Output(ctx, o.Root, "aws", "ec2", "describe-instances", "--region", m["region"], "--instance-ids", m["instance_id"],
		"--query", "Reservations[0].Instances[0].State.Name", "--output", "text")
	if err != nil || st == "" {
		return "", fmt.Errorf("cannot determine node state: %v", err)
	}
	return st, nil
}

// requireRunning fails fast instead of retrying SSM against a stopped node.
func (o Ops) requireRunning(ctx context.Context, m map[string]string) error {
	st, err := o.state(ctx, m)
	if err != nil {
		return err
	}
	if st == "stopped" {
		return errors.New("node is stopped; run `crucible aws wake`")
	}
	return nil
}

// SleepNode snapshots, then stops the instance (compute billing stops; disk + IP keep costing ~$9/month).
func (o Ops) SleepNode(ctx context.Context, noSnapshot bool) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if !noSnapshot {
		st, err := o.state(ctx, m)
		if err != nil {
			return fmt.Errorf("not stopping: %w", err)
		}
		if st == "running" {
			if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
				return fmt.Errorf("snapshot failed, not stopping (use --no-snapshot to skip it): %w", err)
			}
		}
	} else {
		fmt.Fprintln(o.Log, "WARNING: --no-snapshot: stopping without a snapshot; everything since the last backup may be LOST if the disk is lost")
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

// healthy probes <url>/healthz once.
func (o Ops) healthy(url string) error {
	get := o.Get
	if get == nil {
		c := &http.Client{Timeout: 10 * time.Second}
		get = c.Get
	}
	res, err := get(url + "/healthz")
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

func (o Ops) waitHealthy(ctx context.Context, url string) error {
	for i := 0; i < 120; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if o.healthy(url) == nil {
			fmt.Fprintf(o.Log, "🔥 the forge is lit: %s\n", url)
			return nil
		}
		o.Sleep(5 * time.Second)
	}
	return fmt.Errorf("the node is up but %s/healthz isn't answering yet — if DNS or the certificate isn't ready, wait and run `crucible aws status`", url)
}

func (o Ops) Status(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	st, err := o.state(ctx, m)
	if err != nil {
		st = "unknown (" + err.Error() + ")"
	}
	health := "ok"
	if err := o.healthy(m["url"]); err != nil {
		health = "not answering (" + err.Error() + ")"
	}
	fmt.Fprintf(o.Log, "instance %s: %s\nurl: %s\nhealthz: %s\n", m["instance_id"], st, m["url"], health)
	return nil
}

func (o Ops) Teardown(ctx context.Context, varFile string, yes, noSnapshot bool) error {
	if !yes {
		return errors.New("teardown destroys the node (buckets and snapshots are kept); re-run with --yes to confirm")
	}
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if err := o.snapshotFirst(ctx, m, noSnapshot, "tearing down"); err != nil {
		return err
	}
	p, err := o.outputs(ctx, "persistent")
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(varFile)
	return o.Exec(ctx, o.Root, "terraform", append([]string{"-chdir=" + o.dir("main"), "destroy", "-auto-approve", "-var-file=" + abs}, mainVars(p)...)...)
}
