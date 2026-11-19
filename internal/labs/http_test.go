package labs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
)

type echoPTY struct {
	r       *io.PipeReader
	w       *io.PipeWriter
	mu      sync.Mutex
	resized [2]int
	closed  atomic.Bool
}

func (e *echoPTY) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echoPTY) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *echoPTY) Close() error                { e.closed.Store(true); return e.w.Close() }
func (e *echoPTY) Resize(c, r int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resized = [2]int{c, r}
	return nil
}

type ptyRunner struct {
	fakeRunner
	pty    *echoPTY
	opens  atomic.Int32
	opened atomic.Value // [2]int: the size the last PTY was opened at
}

func (p *ptyRunner) OpenPTY(_ context.Context, _ *Instance, _ string, cols, rows int) (PTY, error) {
	p.opens.Add(1)
	p.opened.Store([2]int{cols, rows})
	return p.pty, nil
}

func TestTerminalBridge(t *testing.T) {
	f := setup(t, true)
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/labs/"+v.ID+"/terminals/shell/ws?cols=100&rows=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil || string(data) != "ls\n" {
		t.Fatalf("echo: %q %v", data, err)
	}
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"cols":120,"rows":40}`))
	for i := 0; i < 100; i++ {
		pr.pty.mu.Lock()
		got := pr.pty.resized
		pr.pty.mu.Unlock()
		if got == [2]int{120, 40} {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resize not forwarded")
}

func TestTerminalOriginAndCleanup(t *testing.T) {
	f := setup(t, true)
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/labs/" + v.ID + "/terminals/shell/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}); err == nil {
		ws.CloseNow()
		t.Fatal("cross-origin dial accepted")
	}
	if n := pr.opens.Load(); n != 0 {
		t.Fatalf("OpenPTY called %d times for cross-origin request", n)
	}

	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("x"))
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	ws.CloseNow()
	// The shell outlives the websocket: a refresh comes back to it. Only closing the terminal ends it.
	time.Sleep(100 * time.Millisecond)
	if pr.pty.closed.Load() {
		t.Fatal("pty closed when the websocket went away; a refresh would lose the shell")
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/labs/"+v.ID+"/terminals/shell", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for i := 0; i < 200 && !pr.pty.closed.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !pr.pty.closed.Load() {
		t.Fatal("pty not closed when the trainee closed the terminal")
	}
}

// A refresh, or walking off the lab page and back, reattaches to the shell that is already running instead of opening
// a second one beside it: a machine's serial console allows one connection, and the one left behind held it.
func TestTerminalReattachesAfterRefresh(t *testing.T) {
	f := setup(t, true)
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/labs/" + v.ID + "/terminals/shell/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("tempered\n"))
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	ws.CloseNow() // the browser goes away

	again, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.CloseNow()
	_, data, err := again.Read(ctx) // what the terminal printed is replayed onto the new connection
	if err != nil || !strings.Contains(string(data), "tempered") {
		t.Fatalf("replay: %q %v", data, err)
	}
	if n := pr.opens.Load(); n != 1 {
		t.Fatalf("OpenPTY called %d times; the shell should have been reused", n)
	}
	// The lab says which terminals are still running, so the page opens the same tabs again.
	view, err := f.s.Get(ctx, f.u, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OpenTerminals) != 1 || view.OpenTerminals[0] != "shell" {
		t.Fatalf("open terminals: %v", view.OpenTerminals)
	}
}

// A second copy of a terminal ("shell #2") is its own shell; a tab that is not one of the lab's terminals is refused.
func TestTerminalTabs(t *testing.T) {
	f := setup(t, true)
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/labs/" + v.ID + "/terminals/shell/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	one, _, err := websocket.Dial(ctx, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer one.CloseNow()
	two, _, err := websocket.Dial(ctx, base+"?tab=shell+%232", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer two.CloseNow()
	for i := 0; i < 200 && pr.opens.Load() != 2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := pr.opens.Load(); n != 2 {
		t.Fatalf("OpenPTY called %d times for two tabs", n)
	}
	if ws, _, err := websocket.Dial(ctx, base+"?tab=other", nil); err == nil {
		ws.CloseNow()
		t.Fatal("a tab of another terminal was accepted")
	}
}

// A terminal with a fixed size (lab.yaml size: 80x24) opens at that size whatever the browser asks, and ignores resizes:
// a serial console's GRUB assumes 80x24.
func TestFixedSizeTerminalKeepsItsSize(t *testing.T) {
	f := setup(t, true)
	for _, m := range f.s.Learn.State().Trainings["forge-101@abc"].Modules {
		if m.Lab != nil {
			for i := range m.Lab.Terminals {
				m.Lab.Terminals[i].Size = "80x24"
			}
		}
	}
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/labs/"+v.ID+"/terminals/shell/ws?cols=150&rows=50", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if got, _ := pr.opened.Load().([2]int); got != [2]int{80, 24} {
		t.Fatalf("opened at %v, want 80x24", got)
	}
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"cols":200,"rows":60}`))
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("ls\n")) // after the resize message: once it echoes, the resize was handled
	if _, data, err := ws.Read(ctx); err != nil || string(data) != "ls\n" {
		t.Fatalf("echo: %q %v", data, err)
	}
	pr.pty.mu.Lock()
	defer pr.pty.mu.Unlock()
	if pr.pty.resized != [2]int{} {
		t.Fatalf("a fixed-size terminal was resized to %v", pr.pty.resized)
	}
}
