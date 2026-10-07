package org

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/db/dbtest"
)

func TestSeedAdminOnlyIntoAnEmptyTable(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	ok, err := s.SeedAdmin(ctx, " Admin@Corp.COM ")
	if err != nil || !ok {
		t.Fatalf("first seed: ok=%v err=%v", ok, err)
	}
	admins, _ := s.Admins(ctx)
	if len(admins) != 1 || admins[0] != "admin@corp.com" {
		t.Fatalf("admins = %v, want the lowercased trimmed address", admins)
	}
	ok, err = s.SeedAdmin(ctx, "second@corp.com")
	if err != nil || ok {
		t.Errorf("a second seed must do nothing: ok=%v err=%v", ok, err)
	}
	if admins, _ = s.Admins(ctx); len(admins) != 1 {
		t.Errorf("admins = %v", admins)
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'admin.bootstrap'`).Scan(&n)
	if n != 1 {
		t.Errorf("admin.bootstrap rows = %d, want 1 (the no-op leaves none)", n)
	}
	if _, err := (&Store{DB: dbtest.New(t)}).SeedAdmin(ctx, "not an email"); !errors.Is(err, apperr.Invalid) {
		t.Errorf("bad address = %v", err)
	}
}

func TestRemoveLastAdminIsRefused(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if _, err := s.SeedAdmin(ctx, "admin@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAdmin(ctx, "admin@x", "admin@x"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("removing the only admin must be refused: %v", err)
	}
	if err := s.AddAdmin(ctx, "admin@x", "second@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAdmin(ctx, "admin@x", "admin@x"); err != nil {
		t.Errorf("removing one of two admins must be allowed: %v", err)
	}
	if err := s.RemoveAdmin(ctx, "second@x", "ghost@x"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("removing a non-admin = %v, want NotFound", err)
	}
}

func TestConcurrentRemovalsLeaveOneAdmin(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for _, e := range []string{"a@x", "b@x"} {
		if err := s.AddAdmin(ctx, "a@x", e); err != nil {
			t.Fatal(err)
		}
	}
	// Hold the admin rows locked so both removals start, and are waiting, before either can look.
	hold, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(ctx, `SELECT email FROM admins FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, e := range []string{"a@x", "b@x"} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = s.RemoveAdmin(ctx, "a@x", e) }()
	}
	time.Sleep(500 * time.Millisecond) // without FOR UPDATE both removals finish in this window
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if admins, _ := s.Admins(ctx); len(admins) != 1 {
		t.Fatalf("admins = %v (errs %v), want exactly one left", admins, errs)
	}
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Errorf("exactly one removal must succeed: %v", errs)
	}
}

func TestAdminWritesAreAudited(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.AddAdmin(ctx, "Boss@X", "b@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAdmin(ctx, "boss@x", "c@x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAdmin(ctx, "boss@x", "b@x"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ action, target string }{{"admin.add", "b@x"}, {"admin.add", "c@x"}, {"admin.remove", "b@x"}} {
		var actor, email string
		err := s.DB.QueryRow(ctx, `SELECT actor, detail->>'email' FROM audit_log WHERE action = $1 AND target = $2`, c.action, c.target).Scan(&actor, &email)
		if err != nil || actor != "boss@x" || email != c.target {
			t.Errorf("%s %s: actor=%q email=%q err=%v", c.action, c.target, actor, email, err)
		}
	}
}

func TestAddAdminIsIdempotentAndValidatesTheAddress(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.AddAdmin(ctx, "a@x", "notanemail"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("notanemail = %v", err)
	}
	for _, e := range []string{"New@X", " new@x "} {
		if err := s.AddAdmin(ctx, "a@x", e); err != nil {
			t.Fatal(err)
		}
	}
	if admins, _ := s.Admins(ctx); len(admins) != 1 || admins[0] != "new@x" {
		t.Errorf("admins = %v, want one row", admins)
	}
	var grants, repeats int
	s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE detail->>'already' IS NULL), count(*) FILTER (WHERE detail->>'already' = 'true')
		FROM audit_log WHERE action = 'admin.add' AND target = 'new@x'`).Scan(&grants, &repeats)
	if grants != 1 || repeats != 1 {
		t.Errorf("audit: %d grants, %d marked already; want 1 and 1", grants, repeats)
	}
}
