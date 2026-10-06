package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/db/dbtest"
	"crucible/internal/labs"
	"crucible/internal/learn"
)

func TestRevokeAgentTokensRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st := auth.Store{DB: dbtest.New(t)}
	hub := agenthub.New()
	r := NewRouter(Deps{Auth: st, Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: hub,
		IsAdmin: func(e string) bool { return e == "admin@x" }})
	srv := httptest.NewServer(r)
	defer srv.Close()

	admin, _ := st.UpsertUser(ctx, "s0", "admin@x", "Admin")
	user, _ := st.UpsertUser(ctx, "s1", "user@x", "User")
	other, _ := st.UpsertUser(ctx, "s2", "other@x", "Other")
	cookie := func(u *auth.User) *http.Cookie {
		tok, _ := st.CreateSession(ctx, u.ID, time.Hour)
		return &http.Cookie{Name: auth.SessionCookie, Value: tok}
	}
	del := func(path string, u *auth.User) int {
		req, _ := http.NewRequestWithContext(ctx, "DELETE", srv.URL+path, nil)
		req.AddCookie(cookie(u))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	connect := func(u *auth.User) (*websocket.Conn, string) {
		tok, _ := st.CreateAgentToken(ctx, u.ID)
		ws, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/api/agent/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
		if err != nil {
			t.Fatal(err)
		}
		return ws, tok
	}
	closedWith := func(ws *websocket.Conn) websocket.StatusCode {
		_, _, err := ws.Read(ctx)
		return websocket.CloseStatus(err)
	}

	// own tokens: DELETE /api/agent/tokens
	ws, tok := connect(user)
	if code := del("/api/agent/tokens", user); code != 204 {
		t.Fatalf("self revoke: %d", code)
	}
	if got := closedWith(ws); got != agenthub.CloseRevoked {
		t.Fatalf("live agent close: %v", got)
	}
	if u, _ := st.UserByAgentToken(ctx, tok); u != nil {
		t.Fatal("token still pairs")
	}
	if code := del("/api/agent/tokens", &auth.User{ID: user.ID}); code != 204 { // idempotent
		t.Fatalf("again: %d", code)
	}

	// admin revokes someone else's; non-admins cannot
	ws, tok = connect(other)
	if code := del("/api/admin/agent/tokens?email=Other@x", user); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	if u, _ := st.UserByAgentToken(ctx, tok); u == nil {
		t.Fatal("a forbidden call revoked the token")
	}
	if code := del("/api/admin/agent/tokens?email=nobody@x", admin); code != 404 {
		t.Fatalf("unknown user: %d", code)
	}
	if code := del("/api/admin/agent/tokens?email=Other@x", admin); code != 204 {
		t.Fatalf("admin: %d", code)
	}
	if got := closedWith(ws); got != agenthub.CloseRevoked {
		t.Fatalf("live agent close: %v", got)
	}
	var actor, target string
	if err := st.DB.QueryRow(ctx, `SELECT actor, target FROM audit_log WHERE action = 'agent.token.revoke' AND target = 'other@x'`).Scan(&actor, &target); err != nil || actor != "admin@x" {
		t.Fatalf("audit: %q %v", actor, err)
	}
}
