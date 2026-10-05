package awsops

import (
	"context"
	"io"
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
	noPackage bool // helm package produces nothing
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
			return r.output(cmd), nil
		},
		Sleep: func(time.Duration) {},
		Log:   io.Discard,
		Get:   func(string) (*http.Response, error) { return &http.Response{StatusCode: 200, Body: http.NoBody}, nil },
	}
}

func fakeAWS(cmd string) string {
	switch {
	case strings.Contains(cmd, "output -json"):
		return `{"instance_id":{"value":"i-123"},"region":{"value":"eu-west-1"},"data_bucket":{"value":"bkt"},"domain":{"value":"c.example.com"},"url":{"value":"https://c.example.com"},"state_bucket":{"value":"st"},"oidc_issuer":{"value":"https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_abc"},"oidc_client_id":{"value":"client123"},"cognito_user_pool_id":{"value":"eu-west-1_abc"}}`
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
	if err := r.ops(t.TempDir()).SleepNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, stop := indexOf(r.calls, "/opt/crucible/snapshot.sh"), indexOf(r.calls, "stop-instances")
	if snap < 0 || stop < 0 || snap > stop {
		t.Fatalf("snapshot must precede stop:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestTeardownRequiresYes(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", false); err == nil {
		t.Fatal("teardown without --yes must fail")
	}
	if indexOf(r.calls, "destroy") >= 0 {
		t.Fatal("nothing may be destroyed without --yes")
	}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true); err != nil {
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
		err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true)
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
	if err := r.ops(t.TempDir()).SleepNode(context.Background()); err == nil {
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
	if err := o.Up(context.Background(), "x.tfvars"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-backend-config=bucket=st", "-backend-config=key=main.tfstate", "-backend-config=use_lockfile=true",
		"-var data_bucket=bkt", "-var oidc_client_id=client123", "-var cognito_user_pool_id=eu-west-1_abc", "-var oidc_issuer=https://cognito-idp"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}
