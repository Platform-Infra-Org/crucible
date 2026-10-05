package labs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
}

func (e *echoPTY) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echoPTY) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *echoPTY) Close() error                { return e.w.Close() }
func (e *echoPTY) Resize(c, r int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resized = [2]int{c, r}
	return nil
}

type ptyRunner struct {
	fakeRunner
	pty *echoPTY
}

func (p *ptyRunner) OpenPTY(context.Context, *Instance, string, int, int) (PTY, error) {
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
