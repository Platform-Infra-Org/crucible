package db_test

import (
	"context"
	"testing"

	"crucible/internal/db"
	"crucible/internal/db/dbtest"
)

func TestMigrationsCreateTablesAndAreIdempotent(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	for _, table := range []string{"users", "sessions", "agent_tokens", "item_progress", "quiz_attempts",
		"lab_instances", "lab_events", "lab_task_progress", "check_runs", "setup_runs", "hint_reveals"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(ops) FROM content_edits").Scan(&n); err != nil {
		t.Fatalf("content_edits.ops: %v", err)
	}
	if err := db.Migrate(pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
