package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	ap "crucible/internal/agentproto"
)

func TestRunScriptPassesEnvAsOneArgvElement(t *testing.T) {
	fe := &fakeExec{}
	r := testRunner(fake.NewClientset(), fe)
	evil := `$(touch /tmp/pwned); ' " ; rm -rf /`
	_, err := r.RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "web", Script: []byte("echo hi"), Env: map[string]string{"CRUCIBLE_ANSWER": evil, "A": "1"}, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c := fe.last()
	want := []string{"timeout", "-s", "KILL", "35", "docker", "compose", "exec", "-T",
		"-e", "A=1", "-e", "CRUCIBLE_ANSWER=" + evil, "web", "sh", "-s"}
	if !slices.Equal(c.cmd, want) || c.tty || string(c.stdin) != "echo hi" || c.ns != "lab-"+testID {
		t.Fatalf("argv/stdin:\n got %q\nwant %q\nstdin %q", c.cmd, want, c.stdin)
	}
}

func TestRunScriptExitCodes(t *testing.T) {
	fe := &fakeExec{fn: func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write([]byte("nope"))
		return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 3"), Code: 3}
	}}
	res, err := testRunner(fake.NewClientset(), fe).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Script: []byte("exit 3"), Timeout: time.Second})
	if err != nil || res.ExitCode != 3 || res.Output != "nope" || res.TimedOut {
		t.Fatalf("%+v %v", res, err)
	}
	fe.fn = func(context.Context, []string, remotecommand.StreamOptions) error {
		return errors.New("pods \"lab\" not found")
	}
	if _, err := testRunner(fake.NewClientset(), fe).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: time.Second}); err == nil {
		t.Fatal("an exec that could not run is an error, not a failed check")
	}
}

func TestRunScriptTimeoutCapAndCancel(t *testing.T) {
	hang := &fakeExec{fn: func(ctx context.Context, _ []string, _ remotecommand.StreamOptions) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	start := time.Now()
	res, err := testRunner(fake.NewClientset(), hang).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Script: []byte("sleep 600"), Timeout: 50 * time.Millisecond})
	if err != nil || !res.TimedOut || res.ExitCode != -1 || !strings.HasSuffix(res.Output, "[crucible] script timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %+v %v after %v", res, err, time.Since(start))
	}

	flood := &fakeExec{fn: func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write(bytes.Repeat([]byte("x"), 100<<10))
		return nil
	}}
	res, err = testRunner(fake.NewClientset(), flood).RunScript(context.Background(), &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: time.Second})
	if err != nil || len(res.Output) != ap.MaxOutput {
		t.Fatalf("output cap: %d %v", len(res.Output), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	res, err = testRunner(fake.NewClientset(), hang).RunScript(ctx, &Instance{ID: testID},
		ScriptSpec{Service: "shell", Timeout: 10 * time.Second})
	if err == nil || res.TimedOut {
		t.Fatalf("a cancelled request is an error, not a timed-out check: %+v %v", res, err)
	}
}

func TestRunScriptRejectsBadTargets(t *testing.T) {
	fe := &fakeExec{}
	r := testRunner(fake.NewClientset(), fe)
	for _, tc := range []struct{ id, svc string }{{"x", "shell"}, {testID, ""}, {testID, "-it"}} {
		if _, err := r.RunScript(context.Background(), &Instance{ID: tc.id}, ScriptSpec{Service: tc.svc, Timeout: time.Second}); err == nil {
			t.Fatalf("%+v must be refused", tc)
		}
		if _, err := r.OpenPTY(context.Background(), &Instance{ID: tc.id}, tc.svc, 80, 24); err == nil {
			t.Fatalf("pty %+v must be refused", tc)
		}
	}
	if len(fe.calls) != 0 {
		t.Fatal("nothing may be executed")
	}
}

// echoUpper is a fake shell: it upper-cases stdin to stdout and records terminal sizes.
func echoUpper(sizes chan remotecommand.TerminalSize) func(context.Context, []string, remotecommand.StreamOptions) error {
	return func(_ context.Context, _ []string, o remotecommand.StreamOptions) error {
		go func() {
			for s := o.TerminalSizeQueue.Next(); s != nil; s = o.TerminalSizeQueue.Next() {
				sizes <- *s
			}
		}()
		buf := make([]byte, 64)
		for {
			n, err := o.Stdin.Read(buf)
			if err != nil {
				return nil
			}
			_, _ = o.Stdout.Write(bytes.ToUpper(buf[:n]))
		}
	}
}

func TestPTYStreamsAndResizes(t *testing.T) {
	sizes := make(chan remotecommand.TerminalSize, 8)
	fe := &fakeExec{fn: echoUpper(sizes)}
	p, err := testRunner(fake.NewClientset(), fe).OpenPTY(context.Background(), &Instance{ID: testID}, "shell", 120, 32)
	if err != nil {
		t.Fatal(err)
	}
	if s := <-sizes; s.Width != 120 || s.Height != 32 {
		t.Fatalf("initial size %+v", s)
	}
	if _, err := p.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := p.Read(buf)
	if err != nil || string(buf[:n]) != "HI\n" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	_ = p.Resize(100, 40)
	if s := <-sizes; s.Width != 100 || s.Height != 40 {
		t.Fatalf("resize %+v", s)
	}
	c := fe.last()
	if !c.tty || !slices.Equal(c.cmd[:4], []string{"docker", "compose", "exec", "shell"}) {
		t.Fatalf("pty exec: %+v", c)
	}
	_ = p.Close()
	_ = p.Close()
	_ = p.Resize(10, 10) // after close: no panic, no block
	if _, err := p.Read(buf); err == nil {
		t.Fatal("read after close must fail")
	}
}

func TestPTYEndsWhenExecEnds(t *testing.T) {
	fe := &fakeExec{fn: func(context.Context, []string, remotecommand.StreamOptions) error { return errors.New("pod gone") }}
	p, err := testRunner(fake.NewClientset(), fe).OpenPTY(context.Background(), &Instance{ID: testID}, "shell", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(p); done <- err }()
	select {
	case <-done: // the websocket handler sees the read end and closes the terminal
	case <-time.After(2 * time.Second):
		t.Fatal("terminal must end when the exec stream ends")
	}
	if _, err := p.Write([]byte("x")); err == nil {
		t.Fatal("write after the stream ended must fail")
	}
	_ = p.Close()
}
