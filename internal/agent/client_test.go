package agent_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	clusterSpecs    []ap.ClusterSpec
}

func (f *fakeExec) Provision(_ context.Context, labID string, _ []byte, compose string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisioned[labID] = compose
	return nil
}
func (f *fakeExec) ProvisionCluster(_ context.Context, labID string, _ []byte, s ap.ClusterSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusterSpecs = append(f.clusterSpecs, s)
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
	return startAgentKube(t, fx, nil)
}

func startAgentKube(t *testing.T, fx *fakeExec, kube *agent.Watcher) (*agenthub.Hub, func()) {
	t.Helper()
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = (&agent.Client{Server: srv.URL, Token: "x", Exec: fx, Log: slog.Default(), Kube: kube}).Run(ctx)
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

func TestReplacedAgentStopsInsteadOfReconnecting(t *testing.T) {
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newClient := func() *agent.Client {
		return &agent.Client{Server: srv.URL, Token: "t", Exec: &fakeExec{provisioned: map[string]string{}}, Log: slog.Default()}
	}
	first := make(chan error, 1)
	go func() { first <- newClient().Run(ctx) }()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	go func() { _ = newClient().Run(ctx) }()
	select {
	case err := <-first:
		if !errors.Is(err, agent.ErrReplaced) {
			t.Fatalf("first agent: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the replaced agent kept running")
	}
}

func TestRevokedAgentStopsWithItsOwnMessage(t *testing.T) {
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&agent.Client{Server: srv.URL, Token: "t", Exec: &fakeExec{provisioned: map[string]string{}}, Log: slog.Default()}).Run(ctx)
	}()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	hub.Drop(1)
	select {
	case err := <-done:
		if !errors.Is(err, agent.ErrRevoked) || errors.Is(err, agent.ErrReplaced) {
			t.Fatalf("got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the revoked agent kept running")
	}
}

func TestAgentContextsReachTheHub(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "config")
	write := func(names ...string) {
		y := "clusters:\n- name: c\n  cluster: {server: 'https://127.0.0.1:6443'}\nusers:\n- name: u\n  user: {}\ncontexts:\n"
		for _, n := range names {
			y += "- name: " + n + "\n  context: {cluster: c, user: u}\n"
		}
		if err := os.WriteFile(kc, []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("one")
	w := &agent.Watcher{Kubeconfig: kc, Every: 20 * time.Millisecond}
	wctx, stopW := context.WithCancel(context.Background())
	defer stopW()
	go w.Run(wctx)
	hub, stop := startAgentKube(t, &fakeExec{provisioned: map[string]string{}}, w)
	defer stop()
	waitCtx := func(n int) {
		for i := 0; i < 200 && len(hub.Contexts(1)) != n; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if got := hub.Contexts(1); len(got) != n {
			t.Fatalf("want %d contexts, got %v", n, got)
		}
	}
	waitCtx(1)
	write("one", "two")
	waitCtx(2)
}

func TestClusterProvisionReachesTheClusterExecutor(t *testing.T) {
	fx := &fakeExec{provisioned: map[string]string{}}
	hub, stop := startAgent(t, fx)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := hub.Call(ctx, 1, ap.Msg{Type: ap.TProvision, LabID: "0123456789ab", Cluster: &ap.ClusterSpec{Context: "kind-dev"}}); err != nil {
		t.Fatal(err)
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if len(fx.clusterSpecs) != 1 || fx.clusterSpecs[0].Context != "kind-dev" || len(fx.provisioned) != 0 {
		t.Fatalf("cluster %+v, compose %v", fx.clusterSpecs, fx.provisioned)
	}
}
