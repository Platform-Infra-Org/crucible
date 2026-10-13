package pricing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"crucible/internal/db/dbtest"
)

func TestStalePricesFailClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	s := &Store{DB: dbtest.New(t), Now: func() time.Time { return now }}
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("never refreshed: %v", err)
	}
	if err := s.Replace(ctx, []Row{{Service: "aws", Region: "eu-west-1", Key: "ec2:t3.micro", Unit: "Hrs", USD: 0.0114}}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := snap.Price("aws", "eu-west-1", "ec2:t3.micro"); !ok || p != 0.0114 {
		t.Fatalf("%v %v", p, ok)
	}
	if _, ok := snap.Price("aws", "us-east-1", "ec2:t3.micro"); ok {
		t.Fatal("other region has no price")
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("older than 30 days: %v", err)
	}
	// a failed refresh keeps the old rows and records why (shown to admins)
	if err := s.Failed(ctx, errors.New("blocked")); err != nil {
		t.Fatal(err)
	}
	var msg string
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT error, (SELECT count(*) FROM prices) FROM price_refresh`).Scan(&msg, &n); err != nil || msg != "blocked" || n != 1 {
		t.Fatalf("%q %d %v", msg, n, err)
	}
	// a good refresh replaces the rows and clears the error
	if err := s.Replace(ctx, []Row{{Service: "aws", Region: "eu-west-1", Key: "nat:hour", Unit: "Hrs", USD: 0.048}}); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Price("aws", "eu-west-1", "ec2:t3.micro"); ok {
		t.Fatal("old rows replaced")
	}
}

// Dry-run fixture prices never price a real lab: a real store sees them as stale and not fresh (so the refresh runs),
// and a dry run over real prices simply replaces them.
func TestFixturePricesNeverServeRealMode(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	pool := dbtest.New(t)
	dry := &Store{DB: pool, Now: clock, Source: "fixture"}
	real := &Store{DB: pool, Now: clock}
	if err := dry.Replace(ctx, Fixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := real.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("real mode must not price from the fixture: %v", err)
	}
	if fresh, err := real.FreshWithin(ctx, 6*24*time.Hour); err != nil || fresh {
		t.Fatalf("fixture prices must not count as a fresh real refresh: %v %v", fresh, err)
	}
	if err := real.Replace(ctx, []Row{{Service: "aws", Region: "eu-west-1", Key: "ec2:t3.micro", Unit: "Hrs", USD: 0.0114}}); err != nil {
		t.Fatal(err)
	}
	if _, err := real.Snapshot(ctx); err != nil {
		t.Fatalf("after its own refresh: %v", err)
	}
	if _, err := dry.Snapshot(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("a dry-run store doesn't use real prices before its own seed: %v", err)
	}
	if err := dry.Replace(ctx, Fixture()); err != nil {
		t.Fatal(err)
	}
	if snap, err := dry.Snapshot(ctx); err != nil || len(snap) != len(Fixture()) {
		t.Fatalf("a dry run over real prices replaces them: %d %v", len(snap), err)
	}
}

func TestRefreshErrorsAreCleaned(t *testing.T) {
	ctx := context.Background()
	s := &Store{DB: dbtest.New(t), Now: func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }}
	if err := s.Failed(ctx, errors.New("bad\x00body \xff"+strings.Repeat("x", 10000))); err != nil {
		t.Fatalf("a raw error with a NUL or invalid UTF-8 must still be recorded: %v", err)
	}
	_, _, msg, err := s.Status(ctx)
	if err != nil || len(msg) > 1100 || strings.ContainsRune(msg, 0) || !utf8.ValidString(msg) || !strings.HasPrefix(msg, "badbody") {
		t.Fatalf("recorded error must be clean and bounded: %d %q… %v", len(msg), msg[:min(len(msg), 20)], err)
	}
}
