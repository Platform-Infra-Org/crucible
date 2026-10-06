package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/blob"
)

func TestTailKeepsTheEnd(t *testing.T) {
	var tl tail
	tl.Write(bytes.Repeat([]byte("a"), transcriptMax))
	tl.Write([]byte("b"))
	got, truncated := tl.Bytes()
	if len(got) != transcriptMax || !truncated || got[len(got)-1] != 'b' || got[0] != 'a' {
		t.Fatalf("len %d truncated %v", len(got), truncated)
	}
	for i := 0; i < 5; i++ {
		tl.Write(bytes.Repeat([]byte("c"), transcriptMax))
	}
	if got, _ := tl.Bytes(); len(got) != transcriptMax || len(tl.buf) > 2*transcriptMax+transcriptMax {
		t.Fatalf("memory must stay bounded: kept %d, buffer %d", len(got), len(tl.buf))
	}
	var small tail
	small.Write([]byte("hi"))
	if got, truncated := small.Bytes(); string(got) != "hi" || truncated {
		t.Fatalf("small: %q %v", got, truncated)
	}
}

func (f *fx) routes(t *testing.T, u *auth.User) *httptest.Server {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

func TestTerminalSessionIsRecorded(t *testing.T) {
	f := setup(t, true)
	f.s.Blobs = blob.Disk{Dir: t.TempDir()}
	r, w := io.Pipe()
	f.s.Runners["local"] = &ptyRunner{pty: &echoPTY{r: r, w: w}}
	v := f.start(t)
	srv := f.routes(t, f.u)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/labs/"+v.ID+"/terminals/shell/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("echo tempered\n"))
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	ws.CloseNow()
	var id int64
	var key, terminal string
	for i := 0; i < 200 && key == ""; i++ {
		if err := f.s.DB.QueryRow(ctx, `SELECT id, blob_key, terminal FROM terminal_transcripts WHERE lab_id = $1`, v.ID).Scan(&id, &key, &terminal); err != nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if key == "" || terminal != "shell" {
		t.Fatal("no transcript saved when the session closed")
	}
	resp, err := http.Get(srv.URL + "/api/labs/" + v.ID + "/transcripts/" + strconv.FormatInt(id, 10))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != 200 || !strings.Contains(string(b), "echo tempered") || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("Content-Security-Policy") != "sandbox" ||
		!strings.HasPrefix(h.Get("Content-Type"), "text/plain") || h.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download %d %q %v", resp.StatusCode, b, h)
	}
}

func TestTranscriptOnlyForOwnerAndScorers(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	var tl tail
	tl.Write([]byte("$ ls\r\nforge\r\n"))
	f.s.saveTranscript(ctx, v.ID, "shell", f.clk.Now(), &tl)
	var id int64
	if err := f.s.DB.QueryRow(ctx, `SELECT id FROM terminal_transcripts WHERE lab_id = $1`, v.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, who := range []*auth.User{f.u, f.other} { // the trainee and the program's scorer
		rc, err := f.s.Transcript(ctx, who, v.ID, id)
		if err != nil {
			t.Fatalf("%s: %v", who.Email, err)
		}
		rc.Close()
	}
	stranger, _ := auth.Store{DB: f.s.DB}.UpsertUser(ctx, "s9", "stranger@crucible.local", "Stan")
	if _, err := f.s.Transcript(ctx, stranger, v.ID, id); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := f.s.Transcript(ctx, f.u, "000000000000", id); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("wrong lab: %v", err)
	}
	ev, err := f.s.Evidence(ctx, v.ID)
	if err != nil || len(ev.Transcripts) != 1 || ev.Transcripts[0].Terminal != "shell" {
		t.Fatalf("evidence lists the transcript: %+v %v", ev, err)
	}
}
