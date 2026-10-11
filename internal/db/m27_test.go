package db_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"crucible/internal/db/dbtest"
)

// 00027 adds the smith thresholds and the sword_ranks and smiths tables (ranks stays until the code stops writing it).
func TestMigration00027(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(os.DirFS("."))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 26); err != nil {
		t.Fatal(err)
	}
	exists := func(tbl string) bool {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists("sword_ranks") || exists("smiths") {
		t.Fatal("new tables must not exist at 26")
	}
	if err := goose.UpTo(sqlDB, "migrations", 27); err != nil {
		t.Fatal(err)
	}
	if !exists("ranks") || !exists("sword_ranks") || !exists("smiths") {
		t.Fatal("after up: ranks, sword_ranks and smiths must exist")
	}
	var j int
	if err := pool.QueryRow(ctx, `SELECT smith_journeyman FROM settings WHERE id = 1`).Scan(&j); err != nil || j != 500 {
		t.Fatalf("smith_journeyman = %d, %v", j, err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 26); err != nil {
		t.Fatal(err)
	}
	if !exists("ranks") || exists("sword_ranks") || exists("smiths") {
		t.Fatal("after down: ranks stays, the new tables go")
	}
}
