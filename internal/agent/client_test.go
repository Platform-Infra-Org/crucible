package agent_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"crucible/internal/agent"
	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
)

type echo struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (e *echo) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echo) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *echo) Close() error                { return e.w.Close() }
func (e *echo) Resize(int, int) error       { return nil }

// fakeSess: eof sessions end immediately; others block in Write until Close.
type fakeSess struct {
	closed  chan struct{}
	unblock chan struct{}
	once    sync.Once
	eof     bool
}

func (s *fakeSess) Read([]byte) (int, error) {
	if s.eof {
		return 0, io.EOF
	}
	<-s.closed
	return 0, io.EOF
}
func (s *fakeSess) Write(b []byte) (int, error) { <-s.closed; return 0, io.ErrClosedPipe }
func (s *fakeSess) Close() error                { s.once.Do(func() { close(s.closed) }); return nil }
func (s *fakeSess) Resize(int, int) error       { return nil }

type fakeExec struct {
	sess            *fakeSess
	mu              sync.Mutex
	provisioned     map[string]string
	destroyAllCalls int
}

func (f *fakeExec) Provision(_ context.Context, labID string, _ []byte, compose string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisioned[labID] = compose
	return nil
}
func (f *fakeExec) Destroy(context.Context, string) error { return nil }
func (f *fakeExec) RunScript(_ context.Context, _, service string, script []byte, env map[string]string, _ time.Duration) (ap.Msg, error) {
	code := 1
	if string(script) == "ok" && env["CRUCIBLE_ANSWER"] == "42" {
		code = 0
	}
	return ap.Msg{ExitCode: code, Data: []byte("ran in " + service)}, nil
}
func (f *fakeExec) StartPTY(_, service string, _, _ int) (agent.Session, error) {
	switch service {
	case "exit":
		s := &fakeSess{closed: make(chan struct{}), unblock: make(chan struct{}), eof: true}
		f.mu.Lock()
		f.sess = s
		f.mu.Unlock()
		return s, nil
	case "stuck":
		s := &fakeSess{closed: make(chan struct{}), unblock: make(chan struct{})}
		return s, nil
	}
	r, w := io.Pipe()
	return &echo{r, w}, nil
}
func (f *fakeExec) Labs() []string { return []string{"0123456789ab"} }
func (f *fakeExec) DestroyAll(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyAllCalls++
}

func TestClientServesHubRequests(t *testing.T) {
	hub := agenthub.New()
	hello := make(chan []string, 1)
	hub.OnHello = func(uid int64, ids []string) {
		if uid == 1 {
			hello <- ids
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "nope", 401)
			return
		}
		hub.Serve(w, r, 1)
	}))
	defer srv.Close()

	fx := &fakeExec{provisioned: map[string]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = (&agent.Client{Server: srv.URL, Token: "secret", Exec: fx, Log: slog.Default()}).Run(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case ids := <-hello:
		if len(ids) != 1 || ids[0] != "0123456789ab" {
			t.Fatalf("hello must list the labs on the laptop, got %v", ids)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never said hello")
	}

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if _, err := hub.Call(cctx, 1, ap.Msg{Type: ap.TProvision, LabID: "lab1", Compose: "compose.yaml"}); err != nil {
		t.Fatal(err)
	}
	res, err := hub.Call(cctx, 1, ap.Msg{Type: ap.TRunScript, LabID: "lab1", Service: "web", Data: []byte("ok"), Env: map[string]string{"CRUCIBLE_ANSWER": "42"}})
	if err != nil || res.ExitCode != 0 || string(res.Data) != "ran in web" {
		t.Fatalf("run_script: %+v %v", res, err)
	}
	p, err := hub.OpenPTY(cctx, 1, "lab1", "shell", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write([]byte("ls\n"))
	buf := make([]byte, 16)
	n, _ := p.Read(buf)
	if string(buf[:n]) != "ls\n" {
		t.Fatalf("pty echo got %q", buf[:n])
	}

	cancel()
	<-done
	if fx.destroyAllCalls != 2 {
		t.Fatalf("agent must clean up on start and on shutdown, got %d calls", fx.destroyAllCalls)
	}
}

func startAgent(t *testing.T, fx *fakeExec) (*agenthub.Hub, func()) {
	t.Helper()
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = (&agent.Client{Server: srv.URL, Token: "x", Exec: fx, Log: slog.Default()}).Run(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return hub, func() { cancel(); <-done; srv.Close() }
}

func TestExitedPTYIsClosed(t *testing.T) {
	fx := &fakeExec{provisioned: map[string]string{}}
	hub, stop := startAgent(t, fx)
	defer stop()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if _, err := hub.OpenPTY(ctx, 1, "lab1", "exit", 80, 24); err != nil {
		t.Fatal(err)
	}
	fx.mu.Lock()
	s := fx.sess
	fx.mu.Unlock()
	select {
	case <-s.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("session Close not called after EOF")
	}
}

func TestStuckPTYWriteDoesNotBlockAgent(t *testing.T) {
	fx := &fakeExec{provisioned: map[string]string{}}
	hub, stop := startAgent(t, fx)
	defer stop()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	p, err := hub.OpenPTY(ctx, 1, "lab1", "stuck", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, _ = p.Write([]byte("x"))
	}
	rctx, rc := context.WithTimeout(context.Background(), 2*time.Second)
	defer rc()
	if _, err := hub.Call(rctx, 1, ap.Msg{Type: ap.TRunScript, LabID: "lab1", Service: "web", Data: []byte("ok")}); err != nil {
		t.Fatalf("agent blocked by stuck PTY write: %v", err)
	}
}
