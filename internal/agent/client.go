// Package agent is the laptop side of local labs: it connects out to Crucible and runs docker compose.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

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
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20) // lab bundles
	c.Log.Info("connected. The forge is lit", "server", c.Server)

	s := &session{ws: ws, exec: c.Exec, ptys: map[string]Session{}}
	defer s.closeAll()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var m ap.Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type { // PTY traffic is handled in order; everything else may take long, so it runs concurrently
		case ap.TPTYData:
			if p := s.pty(m.ID); p != nil {
				_, _ = p.Write(m.Data)
			}
		case ap.TPTYResize:
			if p := s.pty(m.ID); p != nil {
				_ = p.Resize(m.Cols, m.Rows)
			}
		case ap.TPTYClose:
			if p := s.drop(m.ID); p != nil {
				_ = p.Close()
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
	ptys map[string]Session
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

func (s *session) pty(id string) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptys[id]
}

func (s *session) drop(id string) Session {
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
		_ = p.Close()
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
		s.mu.Lock()
		s.ptys[m.ID] = p
		s.mu.Unlock()
		s.reply(ctx, m.ID, ap.Msg{}, nil)
		go s.pump(ctx, m.ID, p)
	}
}

func (s *session) pump(ctx context.Context, id string, p Session) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			s.send(ctx, ap.Msg{Type: ap.TPTYData, ID: id, Data: slices.Clone(buf[:n])})
		}
		if err != nil {
			s.send(ctx, ap.Msg{Type: ap.TPTYClose, ID: id})
			s.drop(id)
			return
		}
	}
}
