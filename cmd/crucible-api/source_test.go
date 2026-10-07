package main

import (
	"context"
	"testing"

	"crucible/internal/db/dbtest"
)

func TestUseStore(t *testing.T) {
	for in, want := range map[string]bool{"": true, " ": true, "\t\n": true, "https://git/x.git": false} {
		if got := useStore(in); got != want {
			t.Errorf("useStore(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBootstrapAdminOnlyInDatabaseMode(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	count := func(q string) (n int) {
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	seedBootstrapAdmin(ctx, pool, false, "boss@example.com")
	if n := count(`SELECT count(*) FROM admins`) + count(`SELECT count(*) FROM audit_log WHERE action = 'admin.bootstrap'`); n != 0 {
		t.Fatalf("git mode must write no admins or bootstrap audit rows, got %d", n)
	}
	seedBootstrapAdmin(ctx, pool, true, "boss@example.com")
	if count(`SELECT count(*) FROM admins`) != 1 {
		t.Fatal("database mode must seed the admin")
	}
}
