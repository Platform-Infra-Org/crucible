package awsops

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	calls     []string
	output    func(cmd string) string
	outErr    func(cmd string) error // optional: make an Output call fail
	noPackage bool                   // helm package produces nothing
	log       bytes.Buffer
	unhealthy bool // /healthz never answers
	noPlugin  bool // session-manager-plugin is not installed
}

func (r *recorder) ops(root string) Ops {
	return Ops{
		Root: root,
		Exec: func(_ context.Context, _ string, name string, args ...string) error {
			r.calls = append(r.calls, name+" "+strings.Join(args, " "))
			switch {
			case name == "docker" && args[0] == "save":
				return os.WriteFile(args[2], []byte("tar"), 0o644)
			case name == "helm" && !r.noPackage:
				for i, a := range args {
					if a == "-d" {
						return os.WriteFile(filepath.Join(args[i+1], "crucible-9.9.9.tgz"), []byte("chart"), 0o644)
					}
				}
			}
			return nil
		},
		Output: func(_ context.Context, _ string, name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			r.calls = append(r.calls, cmd)
			if r.outErr != nil {
				if err := r.outErr(cmd); err != nil {
					return "", err
				}
			}
			return r.output(cmd), nil
		},
		Sleep: func(time.Duration) {},
		Log:   &r.log,
		LookPath: func(string) (string, error) {
			if r.noPlugin {
				return "", errors.New("not found")
			}
			return "/usr/local/bin/session-manager-plugin", nil
		},
		Get: func(string) (*http.Response, error) {
			if r.unhealthy {
				return nil, errors.New("connection refused")
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		},
	}
}

func fakeAWS(cmd string) string {
	switch {
	case strings.Contains(cmd, "output -json"):
		return `{"instance_id":{"value":"i-123"},"region":{"value":"eu-west-1"},"data_bucket":{"value":"bkt"},"domain":{"value":"c.example.com"},"url":{"value":"https://c.example.com"},"public_ip":{"value":"203.0.113.7"},"state_bucket":{"value":"st"},"oidc_issuer":{"value":"https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_abc"},"oidc_client_id":{"value":"client123"},"cognito_user_pool_id":{"value":"eu-west-1_abc"}}`
	case strings.HasPrefix(cmd, "git rev-parse"):
		return "abc123def456"
	case strings.HasPrefix(cmd, "git status"):
		return ""
	case strings.Contains(cmd, "send-command"):
		return "cmd-1"
	case strings.Contains(cmd, "get-command-invocation"):
		return "Success"
	case strings.Contains(cmd, "describe-instances"):
		return "running"
	case strings.Contains(cmd, "describe-instance-information"):
		return "Online"
	}
	return ""
}

func indexOf(calls []string, sub string) int {
	for i, c := range calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func TestDeployPublishesReleaseThenRollsOut(t *testing.T) {
	root := t.TempDir()
	r := &recorder{output: fakeAWS}
	if err := r.ops(root).Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	rel := root + "/.local/release"
	order := []string{
		"buildx build --platform linux/amd64 -t crucible:abc123def456",
		"s3 cp " + rel + "/crucible-image.tar.gz s3://bkt/releases/abc123def456/crucible-image.tar.gz",
		"s3 cp " + rel + "/chart.tgz s3://bkt/releases/abc123def456/chart.tgz",
		"s3://bkt/current-release",
		"/opt/crucible/.failed",
	}
	last := -1
	for _, step := range order {
		i := indexOf(r.calls, step)
		if i <= last {
			t.Fatalf("step %q missing or out of order in:\n%s", step, strings.Join(r.calls, "\n"))
		}
		last = i
	}
	c := r.calls[indexOf(r.calls, "/opt/crucible/.failed")]
	if !strings.Contains(c, "bootstrap failed") || strings.LastIndex(c, "/opt/crucible/deploy.sh") < strings.Index(c, ".failed") {
		t.Fatal("wait command must report bootstrap failure, then run deploy.sh")
	}
}

func TestSleepSnapshotsBeforeStop(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).SleepNode(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	snap, stop := indexOf(r.calls, "/opt/crucible/snapshot.sh"), indexOf(r.calls, "stop-instances")
	if snap < 0 || stop < 0 || snap > stop {
		t.Fatalf("snapshot must precede stop:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestTeardownRequiresYes(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", false, false); err == nil {
		t.Fatal("teardown without --yes must fail")
	}
	if indexOf(r.calls, "destroy") >= 0 {
		t.Fatal("nothing may be destroyed without --yes")
	}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false); err != nil {
		t.Fatal(err)
	}
	if snap, destroy := indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, "destroy"); snap < 0 || destroy < snap {
		t.Fatalf("teardown must snapshot first:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.calls[indexOf(r.calls, "destroy")], "cognito_user_pool_id=eu-west-1_abc") {
		t.Fatal("destroy must receive the Cognito variables from the persistent stack")
	}
}

func stateAWS(state string) func(string) string {
	return func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return state
		}
		return fakeAWS(cmd)
	}
}

func TestDeployRejectsStaleChart(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join(root, ".local", "release")
	_ = os.MkdirAll(rel, 0o755)
	_ = os.WriteFile(filepath.Join(rel, "chart.tgz"), []byte("stale"), 0o644)
	r := &recorder{output: fakeAWS, noPackage: true}
	if err := r.ops(root).Deploy(context.Background()); err == nil {
		t.Fatal("deploy must fail when helm package produced nothing")
	}
	if indexOf(r.calls, "chart.tgz s3://") >= 0 {
		t.Fatal("stale chart uploaded")
	}
}

func TestDeployAndSnapshotRefuseStoppedNode(t *testing.T) {
	r := &recorder{output: stateAWS("stopped")}
	if err := r.ops(t.TempDir()).Deploy(context.Background()); err == nil || !strings.Contains(err.Error(), "wake") {
		t.Fatalf("deploy: %v", err)
	}
	if err := r.ops(t.TempDir()).Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "wake") {
		t.Fatalf("snapshot: %v", err)
	}
	if indexOf(r.calls, "send-command") >= 0 {
		t.Fatal("no SSM against a stopped node")
	}
}

func TestTeardownStates(t *testing.T) {
	for state, wantDestroy := range map[string]bool{"": false, "stopping": false, "pending": false, "stopped": true} {
		r := &recorder{output: stateAWS(state)}
		err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false)
		if got := indexOf(r.calls, "destroy") >= 0; got != wantDestroy || (err == nil) != wantDestroy {
			t.Fatalf("state %q: destroyed=%v err=%v", state, got, err)
		}
		if indexOf(r.calls, "send-command") >= 0 {
			t.Fatalf("state %q: no snapshot expected", state)
		}
	}
}

func TestSleepAbortsWhenStateUnknown(t *testing.T) {
	r := &recorder{output: stateAWS("")}
	if err := r.ops(t.TempDir()).SleepNode(context.Background(), false); err == nil {
		t.Fatal("must abort")
	}
	if indexOf(r.calls, "stop-instances") >= 0 {
		t.Fatal("must not stop")
	}
}

func TestInitAndUp(t *testing.T) {
	r := &recorder{output: fakeAWS}
	root := t.TempDir()
	o := r.ops(root)
	if err := o.Init(context.Background(), InitOpts{Region: "eu-west-1", Domain: "c.example.com"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=eu-west-1 -var access=public -var local_port=18080 -var domain=c.example.com") < 0 {
		t.Fatalf("init apply missing:\n%s", strings.Join(r.calls, "\n"))
	}
	r.calls = nil
	if err := o.Up(context.Background(), "x.tfvars", false, false); err != nil {
		t.Fatal(err)
	}
	if snap, apply := indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, "main apply"); snap < 0 || apply < snap {
		t.Fatalf("up must snapshot a running node before apply:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "Point an A record c.example.com → 203.0.113.7") {
		t.Fatalf("up must print the DNS hint, got:\n%s", r.log.String())
	}
	for _, want := range []string{"-backend-config=bucket=st", "-backend-config=key=main.tfstate", "-backend-config=use_lockfile=true",
		"-var data_bucket=bkt", "-var oidc_client_id=client123", "-var cognito_user_pool_id=eu-west-1_abc", "-var oidc_issuer=https://cognito-idp"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}

func TestUpFirstTimeSkipsSnapshot(t *testing.T) {
	applied := false
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "main output -json") && !applied {
			return `{}`
		}
		return fakeAWS(cmd)
	}}
	o := r.ops(t.TempDir())
	exec := o.Exec
	o.Exec = func(ctx context.Context, dir, name string, args ...string) error {
		if len(args) > 1 && args[1] == "apply" {
			applied = true
		}
		return exec(ctx, dir, name, args...)
	}
	if err := o.Up(context.Background(), "x.tfvars", false, false); err != nil {
		t.Fatal(err)
	}
	if snap, apply := indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, "main apply"); apply < 0 || (snap >= 0 && snap < apply) {
		t.Fatalf("no pre-apply snapshot without a node:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestOutputsRequireInitAndUp(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "output -json") {
			return `{"instance_id":{"value":null}}`
		}
		return fakeAWS(cmd)
	}}
	o := r.ops(t.TempDir())
	if err := o.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "crucible aws up") {
		t.Fatalf("main: %v", err)
	}
	if err := o.Up(context.Background(), "x.tfvars", false, false); err == nil || !strings.Contains(err.Error(), "crucible aws init") {
		t.Fatalf("persistent: %v", err)
	}
}

func TestNoSnapshotSkipsSnapshotLoudly(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).SleepNode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "send-command") >= 0 || indexOf(r.calls, "stop-instances") < 0 || indexOf(r.calls, "destroy") < 0 {
		t.Fatalf("--no-snapshot must stop/destroy without a snapshot:\n%s", strings.Join(r.calls, "\n"))
	}
	if strings.Count(r.log.String(), "WARNING: --no-snapshot") != 2 {
		t.Fatalf("missing loud warning:\n%s", r.log.String())
	}
}

func TestSSMRetriesOnlyWhileUnregistered(t *testing.T) {
	sends := func(r *recorder) int { return strings.Count(strings.Join(r.calls, "\n"), "send-command") }
	denied := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "send-command") {
			return errors.New("exit status 254: An error occurred (AccessDeniedException) when calling the SendCommand operation")
		}
		return nil
	}}
	if err := denied.ops(t.TempDir()).Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("want AccessDenied, got %v", err)
	}
	if n := sends(denied); n != 1 {
		t.Fatalf("AccessDenied must fail fast, sent %d times", n)
	}
	n := 0
	fresh := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "send-command") {
			if n++; n < 3 {
				return errors.New("exit status 254: An error occurred (InvalidInstanceId) when calling the SendCommand operation")
			}
		}
		return nil
	}}
	if err := fresh.ops(t.TempDir()).Snapshot(context.Background()); err != nil || sends(fresh) != 3 {
		t.Fatalf("unregistered node must be retried: err=%v sends=%d", err, sends(fresh))
	}
}

func TestUnhealthyMessagesPointAtStatus(t *testing.T) {
	r := &recorder{output: fakeAWS, unhealthy: true}
	err := r.ops(t.TempDir()).Deploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "https://c.example.com/healthz isn't answering yet") || !strings.Contains(err.Error(), "crucible aws status") {
		t.Fatalf("got %v", err)
	}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.log.String(), "healthz: not answering") {
		t.Fatalf("status must probe /healthz:\n%s", r.log.String())
	}
	ok := &recorder{output: fakeAWS}
	_ = ok.ops(t.TempDir()).Status(context.Background())
	if !strings.Contains(ok.log.String(), "healthz: ok") {
		t.Fatalf("status:\n%s", ok.log.String())
	}
}

func TestDefaultOutputIncludesStderrInError(t *testing.T) {
	_, err := Default(t.TempDir()).Output(context.Background(), "", "sh", "-c", "echo 'An error occurred (ExpiredToken)' >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "ExpiredToken") {
		t.Fatalf("got %v", err)
	}
}

func TestLabsInitAndUpWiresTheLabAccount(t *testing.T) {
	labs := `{"lab_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab"},"ops_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab-ops"},"state_bucket":{"value":"crucible-444455556666-labstate"},"state_region":{"value":"eu-west-1"},"regions":{"value":"eu-west-1"},"plan_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab-plan"},"export_bucket":{"value":"crucible-444455556666-exports"},"export_prefix":{"value":"cur"},"export_name":{"value":"crucible-labs"},"lab_vpc_id":{"value":"vpc-1"},"lab_subnet_ids":{"value":"subnet-a,subnet-b"}}`
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return labs
		}
		return fakeAWS(cmd)
	}}
	o := r.ops(t.TempDir())
	if err := o.LabsInit(context.Background(), "eu-west-1", "111122223333", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "labs apply -var region=eu-west-1 -var crucible_account_id=111122223333") < 0 {
		t.Fatalf("labs-init apply:\n%s", strings.Join(r.calls, "\n"))
	}
	r.calls = nil
	if err := o.Up(context.Background(), "x.tfvars", false, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-var lab_role_arn=arn:aws:iam::444455556666:role/crucible-lab", "-var lab_ops_role_arn=arn:aws:iam::444455556666:role/crucible-lab-ops",
		"-var lab_state_bucket=crucible-444455556666-labstate", "-var lab_state_region=eu-west-1", "-var lab_regions=eu-west-1",
		"-var lab_plan_role_arn=arn:aws:iam::444455556666:role/crucible-lab-plan", "-var lab_export_bucket=crucible-444455556666-exports",
		"-var lab_export_prefix=cur", "-var lab_export_name=crucible-labs", "-var lab_vpc_id=vpc-1", "-var lab_subnet_ids=subnet-a,subnet-b"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}

// task-13-review I7: a missing labs state must not silently turn aws labs off.
func TestUpRefusesToDropLabsItCannotRead(t *testing.T) {
	wiredMain := func(cmd string) string {
		if strings.Contains(cmd, "main output -json") {
			return strings.Replace(fakeAWS(cmd), `{"instance_id"`, `{"lab_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab"},"instance_id"`, 1)
		}
		return fakeAWS(cmd)
	}
	labsGone := func(cmd string) error {
		if strings.Contains(cmd, "labs output -json") {
			return errors.New("no state")
		}
		return nil
	}
	r := &recorder{output: wiredMain, outErr: labsGone}
	err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", false, false)
	if err == nil || !strings.Contains(err.Error(), "--no-labs") || !strings.Contains(err.Error(), "crucible-lab") || indexOf(r.calls, "main apply") >= 0 {
		t.Fatalf("up must refuse before applying: %v\n%s", err, strings.Join(r.calls, "\n"))
	}

	r = &recorder{output: wiredMain, outErr: labsGone}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", false, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "main apply") < 0 || indexOf(r.calls, "-var lab_role_arn") >= 0 || !strings.Contains(r.log.String(), "--no-labs") {
		t.Fatalf("--no-labs applies without the lab vars, loudly:\n%s\n%s", strings.Join(r.calls, "\n"), r.log.String())
	}

	r = &recorder{output: fakeAWS, outErr: labsGone} // never wired: labs stay off without a flag
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", false, false); err != nil || indexOf(r.calls, "main apply") < 0 {
		t.Fatalf("no labs before, none now: %v", err)
	}
}

// fixwave A: the lab roles trust the node role main actually created, not a guessed "crucible-node".
func TestLabsInitTrustsMainsNodeRole(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "main output -json") {
			return strings.Replace(fakeAWS(cmd), `{"instance_id"`, `{"node_role_name":{"value":"forge-node"},"instance_id"`, 1)
		}
		return fakeAWS(cmd)
	}}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "labs apply -var region=eu-west-1 -var node_role_name=forge-node") < 0 {
		t.Fatalf("labs-init must pass main's node role:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitBeforeMainSaysWhichRoleItTrusts(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "main output -json") {
			return errors.New("no state")
		}
		return nil
	}}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "node_role_name") >= 0 {
		t.Fatalf("no main outputs: the labs stack default applies:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "crucible-node") || !strings.Contains(r.log.String(), "labs-init again") {
		t.Fatalf("must warn which role is trusted, got %q", r.log.String())
	}
}

// labs-init sets the account's AWS Budget (a free backstop) when asked; without the flags the stack defaults apply.
func TestLabsInitPassesTheBudget(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 250, []string{"ops@example.com", "fin@example.com"}, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var budget_monthly_usd=250 -var budget_emails=["ops@example.com","fin@example.com"]`) < 0 {
		t.Fatalf("labs-init must pass the budget:\n%s", strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "budget_") >= 0 {
		t.Fatalf("no flags, no budget vars:\n%s", strings.Join(r.calls, "\n"))
	}
}

// Re-running labs-init without the budget flags keeps the stack's budget; a flag given replaces its own value only.
func TestLabsInitKeepsTheBudget(t *testing.T) {
	labs := func(cmd string) string {
		if strings.Contains(cmd, filepath.Join("deploy", "aws", "labs")+" output -json") {
			return `{"lab_role_arn":{"value":"arn:aws:iam::1:role/crucible-lab"},"budget_monthly_usd":{"value":250},"budget_emails":{"value":["ops@example.com","fin@example.com"]}}`
		}
		return fakeAWS(cmd)
	}
	r := &recorder{output: labs}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var budget_monthly_usd=250 -var budget_emails=["ops@example.com","fin@example.com"]`) < 0 {
		t.Fatalf("without the flags the current budget is kept, not reset:\n%s", strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: labs}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 300, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var budget_monthly_usd=300 -var budget_emails=["ops@example.com","fin@example.com"]`) < 0 {
		t.Fatalf("a new amount keeps the emails:\n%s", strings.Join(r.calls, "\n"))
	}
}

// labs-init --allowed-regions sets the regions labs may use (an HCL list); without it a new stack has only --region.
func TestLabsInitPassesTheAllowedRegions(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, []string{"eu-west-1", "eu-central-1"}, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var allowed_regions=["eu-west-1","eu-central-1"]`) < 0 {
		t.Fatalf("labs-init must pass the allowed regions:\n%s", strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "allowed_regions") >= 0 {
		t.Fatalf("no flag, no allowed_regions var:\n%s", strings.Join(r.calls, "\n"))
	}
}

// Re-running labs-init without --allowed-regions keeps the stack's regions (its comma-joined regions output).
func TestLabsInitKeepsTheAllowedRegions(t *testing.T) {
	labs := func(regions string) func(string) string {
		return func(cmd string) string {
			if strings.Contains(cmd, filepath.Join("deploy", "aws", "labs")+" output -json") {
				return `{"lab_role_arn":{"value":"arn:aws:iam::1:role/crucible-lab"},"regions":{"value":"` + regions + `"}}`
			}
			return fakeAWS(cmd)
		}
	}
	r := &recorder{output: labs("eu-west-1,eu-central-1")}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var allowed_regions=["eu-west-1","eu-central-1"]`) < 0 {
		t.Fatalf("without the flag the current regions are kept, not reset:\n%s", strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: labs("eu-west-1,eu-central-1")}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, []string{"eu-west-1"}, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var allowed_regions=["eu-west-1"]`) < 0 || indexOf(r.calls, "eu-central-1") >= 0 {
		t.Fatalf("the flag replaces the kept regions:\n%s", strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: labs("eu-west-1,eu-central-1")}
	err := r.ops(t.TempDir()).LabsInit(context.Background(), "us-east-1", "", 0, nil, nil, LabNet{})
	if err == nil || !strings.Contains(err.Error(), "--allowed-regions") || indexOf(r.calls, "labs apply") >= 0 {
		t.Fatalf("kept regions without the new --region must be refused before the apply: %v\n%s", err, strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitRefusesRegionsWithoutItsRegion(t *testing.T) {
	r := &recorder{output: fakeAWS}
	err := r.ops(t.TempDir()).LabsInit(context.Background(), "eu-west-1", "", 0, nil, []string{"eu-central-1"}, LabNet{})
	if err == nil || !strings.Contains(err.Error(), "eu-west-1") || len(r.calls) != 0 {
		t.Fatalf("must refuse before terraform runs: %v\n%s", err, strings.Join(r.calls, "\n"))
	}
}

// persistentOut is a persistent stack's outputs: extra is spliced in (e.g. `,"access":{"value":"ssm"}`).
func persistentOut(extra string) func(string) string {
	return func(cmd string) string {
		if strings.Contains(cmd, "persistent output -json") {
			return `{"state_bucket":{"value":"st"},"region":{"value":"il-central-1"}` + extra + `}`
		}
		return fakeAWS(cmd)
	}
}

func newStack(cmd string) error { // a persistent stack with no state yet
	if strings.Contains(cmd, "persistent output -json") {
		return errors.New("no outputs")
	}
	return nil
}

func TestInitSSMTakesNoDomain(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: newStack}
	if err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "il-central-1", Access: "ssm"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=il-central-1 -var access=ssm -var local_port=18080") < 0 || indexOf(r.calls, "-var domain") >= 0 {
		t.Fatalf("ssm init:\n%s", strings.Join(r.calls, "\n"))
	}
}

// Review focus 1: a re-run without --access keeps ssm and its port; it never flips back to public.
func TestInitKeepsTheCurrentAccess(t *testing.T) {
	r := &recorder{output: persistentOut(`,"access":{"value":"ssm"},"local_port":{"value":18443},"domain":{"value":""}`)}
	if err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=il-central-1 -var access=ssm -var local_port=18443") < 0 {
		t.Fatalf("re-run must keep ssm and 18443:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestInitRefusesBadCombinations(t *testing.T) {
	for name, in := range map[string]InitOpts{
		"unknown access":          {Region: "il-central-1", Access: "vpn"},
		"public without a domain": {Region: "il-central-1", Access: "public"},
		"ssm with a domain":       {Region: "il-central-1", Access: "ssm", Domain: "c.example.com"},
		"privileged port":         {Region: "il-central-1", Access: "ssm", LocalPort: 80},
	} {
		r := &recorder{output: fakeAWS, outErr: newStack}
		if err := r.ops(t.TempDir()).Init(context.Background(), in); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if indexOf(r.calls, "persistent apply") >= 0 {
			t.Errorf("%s: nothing may be applied", name)
		}
	}
}

func TestUpPassesAccessFromThePersistentStack(t *testing.T) {
	r := &recorder{output: persistentOut(`,"data_bucket":{"value":"bkt"},"oidc_issuer":{"value":"https://x"},"oidc_client_id":{"value":"c"},"cognito_user_pool_id":{"value":"p"},"access":{"value":"ssm"},"public_url":{"value":"http://localhost:18080"}`)}
	_ = r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true)
	for _, want := range []string{"-var access=ssm", "-var public_url=http://localhost:18080"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}

// Review focus 2: a persistent stack made before access existed has neither output; main must stay public.
func TestUpOnAnOldPersistentStackStaysPublic(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "-var access=") >= 0 || indexOf(r.calls, "-var public_url=") >= 0 {
		t.Fatalf("no access or public_url without the outputs:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitPassesTheLabNetwork(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil,
		LabNet{VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"}}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var lab_vpc_id=vpc-1 -var lab_subnet_ids=["subnet-a","subnet-b"]`) < 0 {
		t.Fatalf("labs-init:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitKeepsTheLabNetwork(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return `{"lab_role_arn":{"value":"arn:aws:iam::1:role/l"},"lab_vpc_id":{"value":"vpc-1"},"lab_subnet_ids":{"value":"subnet-a,subnet-b"}}`
		}
		return fakeAWS(cmd)
	}}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var lab_vpc_id=vpc-1 -var lab_subnet_ids=["subnet-a","subnet-b"]`) < 0 {
		t.Fatalf("a re-run without the flags keeps the network:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitNeedsBothVPCAndSubnets(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{VPCID: "vpc-1"}); err == nil {
		t.Fatal("a VPC without subnets must be refused")
	}
	if indexOf(r.calls, "labs apply") >= 0 {
		t.Fatal("nothing may be applied")
	}
}

// fakeSSM is fakeAWS for an access = ssm install in il-central-1.
func fakeSSM(cmd string) string {
	if strings.Contains(cmd, "output -json") {
		return `{"instance_id":{"value":"i-123"},"region":{"value":"il-central-1"},"data_bucket":{"value":"bkt"},"domain":{"value":""},"url":{"value":"http://localhost:18080"},"public_ip":{"value":""},"access":{"value":"ssm"},"public_url":{"value":"http://localhost:18080"},"state_bucket":{"value":"st"},"oidc_issuer":{"value":"https://cognito-idp.il-central-1.amazonaws.com/il-central-1_abc"},"oidc_client_id":{"value":"client123"},"cognito_user_pool_id":{"value":"il-central-1_abc"}}`
	}
	return fakeAWS(cmd)
}

func TestSSMModeDeployChecksHealthOnTheNode(t *testing.T) {
	r := &recorder{output: fakeSSM, unhealthy: true} // the laptop can't reach the node: only an on-node check can pass
	if err := r.ops(t.TempDir()).Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "http://localhost/healthz") < 0 {
		t.Fatalf("health must be checked on the node over SSM:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "crucible aws connect") {
		t.Fatalf("the live message must say how to reach it:\n%s", r.log.String())
	}
}

func TestSSMModeUpPrintsNoDNSLine(t *testing.T) {
	r := &recorder{output: fakeSSM, unhealthy: true}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.log.String(), "Point an A record") || !strings.Contains(r.log.String(), "no DNS record") {
		t.Fatalf("ssm has no DNS step:\n%s", r.log.String())
	}
}

func TestSSMModeStatusReportsBlockedHosts(t *testing.T) {
	last := ""
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "send-command") {
			last = cmd
		}
		if strings.Contains(cmd, "StandardOutputContent") && strings.Contains(last, "blocked") {
			return "blocked get.k3s.io\nblocked github.com"
		}
		return fakeSSM(cmd)
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"healthz: ok", "the firewall blocks these", "blocked get.k3s.io", "blocked github.com"} {
		if !strings.Contains(r.log.String(), want) {
			t.Fatalf("status lacks %q:\n%s", want, r.log.String())
		}
	}
	for _, host := range []string{"http://il-central-1.ec2.archive.ubuntu.com", "http://security.ubuntu.com", "https://get.k3s.io", "https://ssm.il-central-1.amazonaws.com", "https://get.helm.sh", "https://production.cloudflare.docker.com",
		"https://registry.terraform.io", "https://releases.hashicorp.com", "https://iam.amazonaws.com", "https://ec2.il-central-1.amazonaws.com", "https://tagging.il-central-1.amazonaws.com", "https://cloudtrail.il-central-1.amazonaws.com"} {
		if indexOf(r.calls, host) < 0 {
			t.Fatalf("egress check must try %s:\n%s", host, strings.Join(r.calls, "\n"))
		}
	}
}

func TestSSMModeStatusChecksTheCognitoHost(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "output -json") && strings.Contains(cmd, "persistent") {
			return `{"state_bucket":{"value":"st"},"cognito_login_url":{"value":"https://crucible-x.auth.il-central-1.amazoncognito.com"}}`
		}
		if strings.Contains(cmd, "StandardOutputContent") {
			return ""
		}
		return fakeSSM(cmd)
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "https://crucible-x.auth.il-central-1.amazoncognito.com") < 0 || !strings.Contains(r.log.String(), "every host Crucible checks is reachable") {
		t.Fatalf("cognito host:\n%s\n%s", strings.Join(r.calls, "\n"), r.log.String())
	}
}

func TestSSMModeStatusSkipsChecksWhenSSMIsNotOnline(t *testing.T) {
	for _, ping := range []string{"ConnectionLost", "", "None"} {
		r := &recorder{output: func(cmd string) string {
			if strings.Contains(cmd, "describe-instance-information") {
				return ping
			}
			return fakeSSM(cmd)
		}}
		if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		if indexOf(r.calls, "send-command") >= 0 || !strings.Contains(r.log.String(), "ssm: ") || !strings.Contains(r.log.String(), "ssmmessages.il-central-1.amazonaws.com") ||
			!strings.Contains(r.log.String(), "ec2messages.il-central-1.amazonaws.com") {
			t.Fatalf("ping %q:\n%s\n%s", ping, strings.Join(r.calls, "\n"), r.log.String())
		}
	}
}

func TestSSMModeStatusSaysWhenTheStateIsUnknown(t *testing.T) {
	r := &recorder{output: fakeSSM, outErr: func(cmd string) error {
		if strings.Contains(cmd, "describe-instances") {
			return errors.New("boom")
		}
		return nil
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.log.String(), "instance state unknown") || strings.Contains(r.log.String(), "isn't running") {
		t.Fatalf("got:\n%s", r.log.String())
	}
}

func TestConnectEndsQuietlyOnCtrlC(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	ctx, cancel := context.WithCancel(context.Background())
	o := r.ops(t.TempDir())
	o.Exec = func(context.Context, string, string, ...string) error { cancel(); return errors.New("signal: killed") }
	if err := o.Connect(ctx, ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatalf("Ctrl-C is the normal way out: %v", err)
	}
}

// Review focus 4: a stopped node is not retried over SSM for ten minutes.
func TestSSMModeStatusSkipsAStoppedNode(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return "stopped"
		}
		return fakeSSM(cmd)
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "send-command") >= 0 || !strings.Contains(r.log.String(), "not checked") {
		t.Fatalf("a stopped node gets no SSM checks:\n%s\n%s", strings.Join(r.calls, "\n"), r.log.String())
	}
}

func TestUnregisteredNodeNamesTheFirewallHosts(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "send-command") {
			return errors.New("exit status 254: An error occurred (InvalidInstanceId) when calling the SendCommand operation")
		}
		return nil
	}}
	err := r.ops(t.TempDir()).Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ssmmessages.eu-west-1.amazonaws.com") || !strings.Contains(err.Error(), "network team") {
		t.Fatalf("got %v", err)
	}
}

// fakeNode answers connect's describe-instances with these "<id>\t<public-url tag>" lines.
func fakeNode(lines string) func(string) string {
	return func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return lines
		}
		return fakeAWS(cmd)
	}
}

func TestConnectForwardsTheNodesPort(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "Name=tag:Name,Values=crucible Name=instance-state-name,Values=running") < 0 {
		t.Fatalf("connect finds the running node by its Name tag:\n%s", strings.Join(r.calls, "\n"))
	}
	if indexOf(r.calls, "aws ssm start-session --region il-central-1 --target i-0abc --document-name AWS-StartPortForwardingSession --parameters portNumber=80,localPortNumber=18080") < 0 {
		t.Fatalf("start-session:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "http://localhost:18080") {
		t.Fatalf("connect says where Crucible is:\n%s", r.log.String())
	}
}

func TestConnectRefusesAnotherPort(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1", Port: 9000})
	if err == nil || !strings.Contains(err.Error(), "18080") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("a different port breaks sign-in: %v", err)
	}
}

func TestConnectRefusesAPublicInstall(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttps://c.example.com")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "eu-west-1"})
	if err == nil || !strings.Contains(err.Error(), "open https://c.example.com") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("a public install needs no tunnel: %v", err)
	}
}

func TestConnectNeedsExactlyOneNode(t *testing.T) {
	r := &recorder{output: fakeNode("")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err == nil || !strings.Contains(err.Error(), "crucible aws wake") {
		t.Fatalf("no node: %v", err)
	}
	r = &recorder{output: fakeNode("i-1\thttp://localhost:18080\ni-2\thttp://localhost:18080")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("two nodes: %v", err)
	}
}

// Review focus 3: a node made before this change has no crucible:public-url tag.
func TestConnectNeedsTheTag(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\tNone")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "crucible aws up") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("an untagged node: %v", err)
	}
}

func TestConnectNeedsThePlugin(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080"), noPlugin: true}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "session-manager-plugin") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("missing plugin: %v", err)
	}
}

func TestConnectRegion(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "configure get region") {
			return "il-central-1"
		}
		return fakeNode("i-0abc\thttp://localhost:18080")(cmd)
	}}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{}); err != nil || indexOf(r.calls, "describe-instances --region il-central-1") < 0 {
		t.Fatalf("region from the AWS CLI's config: %v\n%s", err, strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: fakeAWS} // fakeAWS answers "configure get region" with ""
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{}); err == nil || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("no region anywhere: %v", err)
	}
}
