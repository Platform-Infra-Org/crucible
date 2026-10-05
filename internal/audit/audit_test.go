package audit

import (
	"context"
	"testing"

	"crucible/internal/db/dbtest"
)

func TestLogAndRecent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if err := Log(ctx, pool, "Admin@Crucible.local", "kill_switch.on", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := Log(ctx, pool, "leader@crucible.local", "program.enroll", "forge/forge-201", map[string]any{"enrolled": []string{"t@x"}}, "abc123"); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(ctx, pool, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("recent: %v %v", got, err)
	}
	if got[0].Action != "program.enroll" || got[0].CommitSHA != "abc123" || got[0].Detail["enrolled"] == nil {
		t.Fatalf("newest first with detail: %+v", got[0])
	}
	if got[1].Actor != "admin@crucible.local" {
		t.Fatalf("actor must be lowercased: %q", got[1].Actor)
	}
}

func TestLogRollsBackWithTheTransaction(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Log(ctx, tx, "a@x", "lab.approve", "l1", nil, ""); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if got, _ := Recent(ctx, pool, 10); len(got) != 0 {
		t.Fatalf("entry survived the rollback: %v", got)
	}
}
