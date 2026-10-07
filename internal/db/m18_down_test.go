package db_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"crucible/internal/db/dbtest"
)

// 00018's Down must run, and the migration must come back up afterwards.
func TestMigration00018DownThenUp(t *testing.T) {
	pool := dbtest.New(t) // already migrated to head
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(os.DirFS("."))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	v, err := goose.GetDBVersion(sqlDB)
	if err != nil || v < 18 {
		t.Fatalf("version = %d, %v", v, err)
	}
	for v >= 18 {
		if err := goose.Down(sqlDB, "migrations"); err != nil {
			t.Fatalf("down from %d: %v", v, err)
		}
		if v, err = goose.GetDBVersion(sqlDB); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('settings','schedules','programs','admins','trainings','teams')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("org tables after down = %d, %v", n, err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('settings','schedules','programs','admins','trainings','teams')`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("org tables after up = %d, %v", n, err)
	}
}
