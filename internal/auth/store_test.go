package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/db/dbtest"
)

func TestUsersSessionsAndAgentTokens(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}

	u, err := s.UpsertUser(ctx, "sub-1", "Trainee@Crucible.local", "Tara")
	if err != nil || u.Email != "trainee@crucible.local" {
		t.Fatalf("upsert: %+v %v", u, err)
	}
	u2, _ := s.UpsertUser(ctx, "sub-1", "tara@crucible.local", "Tara T")
	if u2.ID != u.ID || u2.Email != "tara@crucible.local" {
		t.Fatalf("upsert should update the same user: %+v", u2)
	}

	tok, _ := s.CreateSession(ctx, u.ID, time.Hour)
	if got, _ := s.UserBySession(ctx, tok); got == nil || got.ID != u.ID {
		t.Fatalf("session lookup failed")
	}
	expired, _ := s.CreateSession(ctx, u.ID, -time.Minute)
	if got, _ := s.UserBySession(ctx, expired); got != nil {
		t.Fatal("expired session must not authenticate")
	}

	a1, _ := s.CreateAgentToken(ctx, u.ID)
	a2, _ := s.CreateAgentToken(ctx, u.ID)
	if got, _ := s.UserByAgentToken(ctx, a1); got != nil {
		t.Fatal("old agent token must be revoked by a new one")
	}
	if got, _ := s.UserByAgentToken(ctx, a2); got == nil || got.ID != u.ID {
		t.Fatal("new agent token must work")
	}
}

func TestMiddlewareAndRequireUser(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	u, _ := s.UpsertUser(ctx, "sub-2", "a@x", "A")
	tok, _ := s.CreateSession(ctx, u.ID, time.Hour)

	h := s.Middleware(RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(UserFrom(r.Context()).Email))
	})))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 401 {
		t.Fatalf("no cookie: got %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "a@x" {
		t.Fatalf("with cookie: %d %q", w.Code, w.Body.String())
	}
}

func TestRevokeAgentTokensIsAuditedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	u, _ := s.UpsertUser(ctx, "s1", "a@x", "A")
	tok, err := s.CreateAgentToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RevokeAgentTokens(ctx, u.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.UserByAgentToken(ctx, tok); got != nil {
		t.Fatal("a revoked token no longer pairs")
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'agent.token.revoke' AND actor = 'a@x'`).Scan(&n)
	if n != 1 {
		t.Fatalf("want exactly one audit row, got %d", n)
	}
}

// A person imported from another instance (org.Store.Import) has a placeholder sub until they first sign in; that
// login takes the row over, history and all, instead of starting a new user.
func TestImportRelinksUsersByEmail(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	var imported int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO users (sub, email, name) VALUES ('import:tara@x', 'tara@x', 'Tara') RETURNING id`).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	u, err := s.UpsertUser(ctx, "cognito-1", "Tara@X", "Tara T")
	if err != nil || u.ID != imported || u.Sub != "cognito-1" || u.Name != "Tara T" {
		t.Fatalf("first login = %+v %v, want the imported row %d with the new sub", u, err, imported)
	}
	if u, _ := s.UpsertUser(ctx, "cognito-2", "tara@x", "Tara"); u.ID == imported {
		t.Fatal("a second identity with the same email must not take over a row already linked")
	}
}

// A person picks one of the forge icons for their user card; anything else is refused and changes nothing.
func TestAvatarIsOneOfTheForgeIcons(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	u, err := s.UpsertUser(ctx, "sub-1", "a@x", "A")
	if err != nil || u.Avatar != "" {
		t.Fatalf("a new user has no icon: %+v %v", u, err)
	}
	if err := s.SetAvatar(ctx, u.ID, "anvil"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAvatar(ctx, u.ID, "<script>"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("unknown icon = %v, want invalid", err)
	}
	if got, _ := s.UpsertUser(ctx, "sub-1", "a@x", "A"); got.Avatar != "anvil" {
		t.Errorf("avatar = %q, want anvil kept through the refused change and a new login", got.Avatar)
	}
	if err := s.SetAvatar(ctx, u.ID, ""); err != nil {
		t.Errorf("going back to initials: %v", err)
	}
}

// The web app draws the same icons the server accepts (web/src/lib/avatars.ts).
func TestAvatarListMatchesTheWebApp(t *testing.T) {
	b, err := os.ReadFile("../../web/src/lib/avatars.ts")
	if err != nil {
		t.Fatal(err)
	}
	var web []string
	for _, m := range regexp.MustCompile(`\{ id: '([a-z]+)'`).FindAllStringSubmatch(string(b), -1) {
		web = append(web, m[1])
	}
	if !slices.Equal(web, Avatars) {
		t.Errorf("web icons %v, server icons %v", web, Avatars)
	}
}
