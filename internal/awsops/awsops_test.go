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
	if err := o.Init(context.Background(), "eu-west-1", "c.example.com"); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=eu-west-1 -var domain=c.example.com") < 0 {
		t.Fatalf("init apply missing:\n%s", strings.Join(r.calls, "\n"))
	}
	r.calls = nil
	if err := o.Up(context.Background(), "x.tfvars", false); err != nil {
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
	if err := o.Up(context.Background(), "x.tfvars", false); err != nil {
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
	if err := o.Up(context.Background(), "x.tfvars", false); err == nil || !strings.Contains(err.Error(), "crucible aws init") {
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
	labs := `{"lab_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab"},"ops_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab-ops"},"state_bucket":{"value":"crucible-444455556666-labstate"},"state_region":{"value":"eu-west-1"},"regions":{"value":"eu-west-1"}}`
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return labs
		}
		return fakeAWS(cmd)
	}}
	o := r.ops(t.TempDir())
	if err := o.LabsInit(context.Background(), "eu-west-1", "111122223333"); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "labs apply -var region=eu-west-1 -var crucible_account_id=111122223333") < 0 {
		t.Fatalf("labs-init apply:\n%s", strings.Join(r.calls, "\n"))
	}
	r.calls = nil
	if err := o.Up(context.Background(), "x.tfvars", false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-var lab_role_arn=arn:aws:iam::444455556666:role/crucible-lab", "-var lab_ops_role_arn=arn:aws:iam::444455556666:role/crucible-lab-ops",
		"-var lab_state_bucket=crucible-444455556666-labstate", "-var lab_state_region=eu-west-1", "-var lab_regions=eu-west-1"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}
