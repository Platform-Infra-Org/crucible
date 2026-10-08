package org

import (
	"context"
	"reflect"
	"testing"

	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

// The seed goes through the store's own writes, and what it stores reads back exactly as config.Load read the YAML.
func TestSeedImportsAPlatformOnce(t *testing.T) {
	ctx := context.Background()
	want, err := config.Load(writePlatformYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	if ok, err := s.Seed(ctx, want); err != nil || !ok {
		t.Fatalf("seed an empty instance: %v %v", ok, err)
	}
	got, err := s.Platform(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range got.Teams {
		tm.Version, tm.Budget.Version = 0, 0
		for _, pr := range tm.Programs {
			pr.Version = 0
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seeded platform differs from config.Load\n got: %s\nwant: %s", dump(got), dump(want))
	}
	var roles, audited int
	mustScan(t, pool, `SELECT count(*) FROM program_roles WHERE training = 'b'`, &roles)
	if roles != 0 {
		t.Errorf("program b sets no roles: its defaults must stay unset, got %d role rows", roles)
	}
	mustScan(t, pool, `SELECT count(*) FROM audit_log WHERE actor = 'seed' AND action = 'program.enroll'`, &audited)
	if audited != 2 {
		t.Errorf("every seeded write is audited as the seed: %d program.enroll rows", audited)
	}
	if ok, err := s.Seed(ctx, want); err != nil || ok {
		t.Fatalf("a second seed must do nothing: %v %v", ok, err)
	}
	mustExec(t, pool, `DELETE FROM teams`)
	mustExec(t, pool, `DELETE FROM trainings`)
	if ok, err := s.Seed(ctx, want); err != nil || ok {
		t.Fatalf("an instance emptied after its seed must stay empty: %v %v", ok, err)
	}
}

func TestSeedLeavesAConfiguredInstanceAlone(t *testing.T) {
	ctx := context.Background()
	p, err := config.Load(writePlatformYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t)
	mustExec(t, pool, `INSERT INTO trainings VALUES ('mine','https://git/mine.git','main')`)
	if ok, err := (&Store{DB: pool}).Seed(ctx, p); err != nil || ok {
		t.Fatalf("an instance with a training must not be seeded: %v %v", ok, err)
	}
	var n int
	mustScan(t, pool, `SELECT count(*) FROM teams`, &n)
	if n != 0 {
		t.Fatalf("nothing may be imported: %d teams", n)
	}
}

func mustScan(t *testing.T, pool DB, q string, dst any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), q).Scan(dst); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
