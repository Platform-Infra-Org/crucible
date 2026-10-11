package db_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"crucible/internal/db/dbtest"
)

// 00028 drops the old personal ranks table; its Down recreates it.
func TestMigration00028(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(os.DirFS("."))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	ranks := func() bool {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('ranks') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if ranks() {
		t.Fatal("ranks must be gone after up")
	}
	if err := goose.DownTo(sqlDB, "migrations", 27); err != nil {
		t.Fatal(err)
	}
	if !ranks() {
		t.Fatal("down must recreate ranks")
	}
	if err := goose.UpTo(sqlDB, "migrations", 28); err != nil {
		t.Fatal(err)
	}
	if ranks() {
		t.Fatal("up must drop ranks again")
	}
}
