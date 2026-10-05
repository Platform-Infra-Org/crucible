package awsops

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	calls  []string
	output func(cmd string) string
}

func (r *recorder) ops(root string) Ops {
	return Ops{
		Root: root,
		Exec: func(_ context.Context, _ string, name string, args ...string) error {
			r.calls = append(r.calls, name+" "+strings.Join(args, " "))
			return nil
		},
		Output: func(_ context.Context, _ string, name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			r.calls = append(r.calls, cmd)
			return r.output(cmd), nil
		},
		Sleep: func(time.Duration) {},
		Log:   io.Discard,
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
