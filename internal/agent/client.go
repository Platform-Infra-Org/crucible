// Package agent is the laptop side of local labs: it connects out to Crucible and runs docker compose.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"crucible/internal/agenthub"
	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

// ErrReplaced means another crucible-agent connected with the same account (one laptop agent per user).
var ErrReplaced = errors.New("another crucible-agent connected with your account; this one stops (run one agent per user)")

// ErrRevoked means the pairing token was revoked (by the user or an admin); the agent stops.
var ErrRevoked = errors.New("this pairing token was revoked; generate a new one on the Connect page to start the agent again")

var errRejected = errors.New("pairing token rejected — generate a new one")

type Client struct {
	Server string // e.g. https://crucible.example.com
	Token  string
	Exec   Executor
	Log    *slog.Logger
}

// Run keeps the agent connected until ctx ends. It tears down local labs on start (crash leftovers) and on exit.
func (c *Client) Run(ctx context.Context) error {
	c.Exec.DestroyAll(ctx) // leftovers from a crashed previous run
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c.Log.Info("cooling the forge: removing local labs")
		c.Exec.DestroyAll(cleanup)
	}()
	for {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errRejected) || errors.Is(err, ErrReplaced) || errors.Is(err, ErrRevoked) {
			return err
		}
		c.Log.Warn("disconnected from Crucible, retrying in 3s", "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	url := "ws" + strings.TrimPrefix(c.Server, "http") + "/api/agent/ws"
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return errRejected
		}
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20) // lab bundles
	c.Log.Info("connected. The forge is lit", "server", c.Server)

	s := &session{ws: ws, exec: c.Exec, ptys: map[string]*ptyEntry{}}
	defer s.closeAll()
	live, _ := json.Marshal(c.Exec.Labs()) // lets the server reconcile labs that drifted while we were away
	s.send(ctx, ap.Msg{Type: ap.THello, Data: live})
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case agenthub.CloseReplaced:
				return ErrReplaced
			case agenthub.CloseRevoked:
				return ErrRevoked
			}
			return err
		}
		var m ap.Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type { // PTY traffic is handled in order; everything else may take long, so it runs concurrently
		case ap.TPTYData:
			s.enqueue(m)
		case ap.TPTYResize:
			s.enqueue(m)
		case ap.TPTYClose:
			if p := s.drop(m.ID); p != nil {
				p.close()
			}
		default:
			go s.handle(ctx, m)
		}
	}
}

type session struct {
	ws   *websocket.Conn
	exec Executor
	mu   sync.Mutex
	ptys map[string]*ptyEntry
}

// ptyEntry gives each PTY its own writer goroutine so a stuck tty never blocks the websocket read loop.
type ptyEntry struct {
	sess Session
	q    chan ap.Msg
	done chan struct{}
	once sync.Once
}

func newEntry(sess Session) *ptyEntry {
	e := &ptyEntry{sess: sess, q: make(chan ap.Msg, 256), done: make(chan struct{})}
	go func() {
		for {
			select {
			case m := <-e.q:
				if m.Type == ap.TPTYData {
					_, _ = sess.Write(m.Data)
				} else {
					_ = sess.Resize(m.Cols, m.Rows)
				}
			case <-e.done:
				return
			}
		}
	}()
	return e
}

func (e *ptyEntry) close() {
	e.once.Do(func() {
		close(e.done)
		_ = e.sess.Close() // also unblocks a stuck Write
	})
}

// enqueue never blocks; on overflow the chunk is dropped (the program is not reading its stdin).
func (s *session) enqueue(m ap.Msg) {
	if e := s.pty(m.ID); e != nil {
		select {
		case e.q <- m:
		default:
		}
	}
}

func (s *session) send(ctx context.Context, m ap.Msg) {
	b, _ := json.Marshal(m)
	_ = s.ws.Write(ctx, websocket.MessageText, b)
}

func (s *session) reply(ctx context.Context, id string, res ap.Msg, err error) {
	res.Type, res.ID = ap.TResult, id
	if err != nil {
		res.Error = err.Error()
	}
	s.send(ctx, res)
}

func (s *session) pty(id string) *ptyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptys[id]
}

func (s *session) drop(id string) *ptyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.ptys[id]
	delete(s.ptys, id)
	return p
}

func (s *session) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.ptys {
		p.close()
		delete(s.ptys, id)
	}
}

func (s *session) handle(ctx context.Context, m ap.Msg) {
	switch m.Type {
	case ap.TProvision:
		s.reply(ctx, m.ID, ap.Msg{}, s.exec.Provision(ctx, m.LabID, m.Data, m.Compose))
	case ap.TDestroy:
		s.reply(ctx, m.ID, ap.Msg{}, s.exec.Destroy(ctx, m.LabID))
	case ap.TRunScript:
		res, err := s.exec.RunScript(ctx, m.LabID, m.Service, m.Data, m.Env, time.Duration(m.TimeoutMS)*time.Millisecond)
		s.reply(ctx, m.ID, res, err)
	case ap.TPTYOpen:
		p, err := s.exec.StartPTY(m.LabID, m.Service, m.Cols, m.Rows)
		if err != nil {
			s.reply(ctx, m.ID, ap.Msg{}, err)
			return
		}
		e := newEntry(p)
		s.mu.Lock()
		s.ptys[m.ID] = e
		s.mu.Unlock()
		s.reply(ctx, m.ID, ap.Msg{}, nil)
		go s.pump(ctx, m.ID, p, e)
	}
}

func (s *session) pump(ctx context.Context, id string, p Session, e *ptyEntry) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			s.send(ctx, ap.Msg{Type: ap.TPTYData, ID: id, Data: slices.Clone(buf[:n])})
		}
		if err != nil {
			s.send(ctx, ap.Msg{Type: ap.TPTYClose, ID: id})
			s.drop(id)
			e.close()
			return
		}
	}
}
