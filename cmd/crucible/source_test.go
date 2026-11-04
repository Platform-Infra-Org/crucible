package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/db/dbtest"
	"crucible/internal/org"
)

func TestAPlatformRepoIsRefused(t *testing.T) {
	get := func(v string) func(string) string { return func(string) string { return v } }
	if err := configSource(get("")); err != nil {
		t.Fatalf("no platform repo: %v", err)
	}
	if err := configSource(get("file:///git/platform.git")); err == nil || !strings.Contains(err.Error(), "CRUCIBLE_SEED_DIR") {
		t.Fatalf("a leftover platform repo must stop the server and say what to do: %v", err)
	}
}

func TestBootstrapAdminSeedsAnEmptyInstance(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	seedBootstrapAdmin(ctx, pool, "Boss@example.com")
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admins WHERE email = 'boss@example.com'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("bootstrap admin: %d %v", n, err)
	}
}

func TestSeedDirImportsOnceAndRefusesABrokenDir(t *testing.T) {
	t.Setenv("CRUCIBLE_GIT_ALLOW_FILE", "1") // examples/platform registers file:// repos, as the local stack does
	pool := dbtest.New(t)
	ctx := context.Background()
	s := &org.Store{DB: pool}
	if err := seed(ctx, s, ""); err != nil {
		t.Fatalf("no seed dir is a no-op: %v", err)
	}
	if err := seed(ctx, s, "../../examples/platform"); err != nil {
		t.Fatal(err)
	}
	p, err := s.Platform(ctx)
	if err != nil || p.Teams["forge"] == nil || len(p.Teams["forge"].Programs) == 0 || len(p.Admins) != 1 {
		t.Fatalf("seeded platform: %+v %v", p, err)
	}
	if err := seed(ctx, s, "../../examples/platform"); err != nil {
		t.Fatalf("a second start with the seed set: %v", err)
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "platform.yaml"), []byte("bogus: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seed(ctx, &org.Store{DB: dbtest.New(t)}, bad); err == nil {
		t.Fatal("a seed dir that does not load must stop the server")
	}
}
