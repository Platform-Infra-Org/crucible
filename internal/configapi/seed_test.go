package configapi

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/db/dbtest"
	"crucible/internal/org"
)

func admins(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	got, err := (&org.Store{DB: db}).Admins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSeedAdminOnlyIntoAnEmptyAdminsFile(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	if ok, err := SeedAdmin(ctx, db, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if got := admins(t, db); len(got) != 1 || got[0] != "boss@example.com" {
		t.Fatalf("admins = %v", got)
	}
	if ok, _ := SeedAdmin(ctx, db, "other@example.com"); ok {
		t.Fatal("never twice")
	}
	if _, err := SeedAdmin(ctx, dbtest.New(t), "not an email"); err == nil {
		t.Fatal("must look like an email")
	}
}

func TestBootstrapAdminRunsOnce(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	if ok, err := BootstrapAdmin(ctx, db, nil, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	var actor, target string
	if err := db.QueryRow(ctx, `SELECT actor, target FROM audit_log WHERE action = 'admin.bootstrap'`).Scan(&actor, &target); err != nil ||
		actor != "bootstrap" || target != "boss@example.com" {
		t.Fatalf("audit: %q %q %v", actor, target, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM admins`); err != nil { // everyone removed on purpose
		t.Fatal(err)
	}
	if ok, err := BootstrapAdmin(ctx, db, nil, "boss@example.com"); err != nil || ok {
		t.Fatalf("a restart must not seed again: %v %v", ok, err)
	}
	if got := admins(t, db); len(got) != 0 {
		t.Fatalf("re-seeded: %v", got)
	}
}
