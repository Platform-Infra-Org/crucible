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
	for i := 0; i < 200 && !pr.pty.closed.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !pr.pty.closed.Load() {
		t.Fatal("pty not closed after disconnect")
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
