package pricing

import (
	"context"
	"errors"
	"testing"
	"time"

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
