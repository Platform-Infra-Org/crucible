// Package agenthub keeps one WebSocket per user's laptop agent and lets the API call it.
package agenthub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

var ErrOffline = errors.New("agent offline")

type Hub struct {
	mu     sync.Mutex
	agents map[int64]*conn
}

func New() *Hub { return &Hub{agents: map[int64]*conn{}} }

type conn struct {
	ws      *websocket.Conn
	ctx     context.Context
	mu      sync.Mutex
	pending map[string]chan ap.Msg
	ptys    map[string]*PTY
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *conn) send(ctx context.Context, m ap.Msg) error {
	b, _ := json.Marshal(m)
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Serve upgrades the request and serves the agent until it disconnects. A newer agent replaces an older one.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID int64) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, ctx: ctx, pending: map[string]chan ap.Msg{}, ptys: map[string]*PTY{}}

	h.mu.Lock()
	if old := h.agents[userID]; old != nil {
		_ = old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer agent")
	}
	h.agents[userID] = c
	h.mu.Unlock()

	defer func() {
		cancel()
		h.mu.Lock()
		if h.agents[userID] == c {
			delete(h.agents, userID)
		}
		h.mu.Unlock()
		c.closeAll()
		_ = ws.CloseNow()
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m ap.Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		c.dispatch(m)
	}
}

func (c *conn) dispatch(m ap.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case ap.TResult:
		if ch := c.pending[m.ID]; ch != nil {
			delete(c.pending, m.ID)
			ch <- m // buffered, never blocks
		}
	case ap.TPTYData:
		if p := c.ptys[m.ID]; p != nil {
			p.push(m.Data)
		}
	case ap.TPTYClose:
		if p := c.ptys[m.ID]; p != nil {
			delete(c.ptys, m.ID)
			p.closeRemote()
		}
	}
}

func (c *conn) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	for id, p := range c.ptys {
		delete(c.ptys, id)
		p.closeRemote()
	}
}

func (h *Hub) get(userID int64) (*conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.agents[userID]; c != nil {
		return c, nil
	}
	return nil, ErrOffline
}

func (h *Hub) Online(userID int64) bool {
	_, err := h.get(userID)
	return err == nil
}

// Call sends a request and waits for its result. A disconnect fails pending calls immediately.
func (h *Hub) Call(ctx context.Context, userID int64, m ap.Msg) (ap.Msg, error) {
	c, err := h.get(userID)
	if err != nil {
		return ap.Msg{}, err
	}
	if m.ID == "" {
		m.ID = newID()
	}
	ch := make(chan ap.Msg, 1)
	c.mu.Lock()
	c.pending[m.ID] = ch
	c.mu.Unlock()
	if err := c.send(ctx, m); err != nil {
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
		return ap.Msg{}, ErrOffline
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return ap.Msg{}, ErrOffline
		}
		if res.Error != "" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
		return ap.Msg{}, ctx.Err()
	}
}

func (h *Hub) OpenPTY(ctx context.Context, userID int64, labID, service string, cols, rows int) (*PTY, error) {
	c, err := h.get(userID)
	if err != nil {
		return nil, err
	}
	p := &PTY{c: c, id: newID(), data: make(chan []byte, 256), closed: make(chan struct{})}
	c.mu.Lock()
	c.ptys[p.id] = p
	c.mu.Unlock()
	if _, err := h.Call(ctx, userID, ap.Msg{Type: ap.TPTYOpen, ID: p.id, LabID: labID, Service: service, Cols: cols, Rows: rows}); err != nil {
		c.mu.Lock()
		delete(c.ptys, p.id)
		c.mu.Unlock()
		return nil, err
	}
	return p, nil
}

// PTY is a terminal session on the agent, read/written like a stream.
type PTY struct {
	c      *conn
	id     string
	data   chan []byte
	buf    []byte
	closed chan struct{}
	once   sync.Once
}

// push is called with conn.mu held; it drops output if the reader is 256 chunks behind.
// ponytail: drop-on-overflow instead of back-pressure; fine for interactive shells, revisit for bulk output.
func (p *PTY) push(b []byte) {
	select {
	case p.data <- b:
	default:
	}
}

func (p *PTY) closeRemote() { p.once.Do(func() { close(p.closed) }) }

func (p *PTY) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		select {
		case d := <-p.data:
			p.buf = d
		case <-p.closed:
			select {
			case d := <-p.data:
				p.buf = d
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *PTY) Write(b []byte) (int, error) {
	if err := p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYData, ID: p.id, Data: b}); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *PTY) Resize(cols, rows int) error {
	return p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYResize, ID: p.id, Cols: cols, Rows: rows})
}

func (p *PTY) Close() error {
	p.c.mu.Lock()
	delete(p.c.ptys, p.id)
	p.c.mu.Unlock()
	p.closeRemote()
	return p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYClose, ID: p.id})
}
