package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

func server(t *testing.T, h *Hub) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, 7) }))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// fakeAgent answers run_script with exit code 3 and echoes PTY input back.
func fakeAgent(ctx context.Context, ws *websocket.Conn) {
	send := func(m ap.Msg) { b, _ := json.Marshal(m); _ = ws.Write(ctx, websocket.MessageText, b) }
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m ap.Msg
		_ = json.Unmarshal(data, &m)
		switch m.Type {
		case ap.TRunScript:
			send(ap.Msg{Type: ap.TResult, ID: m.ID, ExitCode: 3, Data: []byte("out")})
		case ap.TPTYOpen:
			send(ap.Msg{Type: ap.TResult, ID: m.ID})
		case ap.TPTYData:
			send(ap.Msg{Type: ap.TPTYData, ID: m.ID, Data: m.Data})
		}
	}
}

func TestCallAndPTYRoundTrip(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	go fakeAgent(ctx, ws)
	waitFor(t, func() bool { return h.Online(7) })

	res, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript})
	if err != nil || res.ExitCode != 3 || string(res.Data) != "out" {
		t.Fatalf("call: %+v %v", res, err)
	}

	p, err := h.OpenPTY(ctx, 7, "lab1", "box", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := p.Read(buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("pty echo: %q %v", buf[:n], err)
	}

	_ = ws.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, func() bool { return !h.Online(7) })
	if _, err := p.Read(buf); err != io.EOF {
		t.Fatalf("pty read after disconnect: %v", err)
	}
	if _, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript}); !errors.Is(err, ErrOffline) {
		t.Fatalf("call while offline: %v", err)
	}
}

func TestPendingCallFailsFastOnDisconnect(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { // read one request, then drop the connection without answering
		_, _, _ = ws.Read(ctx)
		_ = ws.CloseNow()
	}()
	waitFor(t, func() bool { return h.Online(7) })
	start := time.Now()
	_, err = h.Call(ctx, 7, ap.Msg{Type: ap.TProvision})
	if !errors.Is(err, ErrOffline) || time.Since(start) > 2*time.Second {
		t.Fatalf("want fast ErrOffline, got %v after %v", err, time.Since(start))
	}
}

func TestReplacedAgentClosedAndNewServes(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := server(t, h)
	old, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan struct{})
	go func() { _, _, _ = old.Read(ctx); close(oldDone) }() // returns when the hub closes it
	waitFor(t, func() bool { return h.Online(7) })

	nw, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	go fakeAgent(ctx, nw)
	select {
	case <-oldDone:
	case <-time.After(3 * time.Second):
		t.Fatal("old conn not closed")
	}
	if !h.Online(7) {
		t.Fatal("offline after replace")
	}
	if res, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript}); err != nil || res.ExitCode != 3 {
		t.Fatalf("call on new agent: %+v %v", res, err)
	}
}

func TestCancelledCallKeepsConnection(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	go fakeAgent(ctx, ws)
	waitFor(t, func() bool { return h.Online(7) })

	cc, ccancel := context.WithCancel(ctx)
	ccancel()
	if _, err := h.Call(cc, 7, ap.Msg{Type: ap.TRunScript}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if !h.Online(7) {
		t.Fatal("cancelled call dropped the agent")
	}
	if res, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript}); err != nil || res.ExitCode != 3 {
		t.Fatalf("call after cancel: %+v %v", res, err)
	}
}
