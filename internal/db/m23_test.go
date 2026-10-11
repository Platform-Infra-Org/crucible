package db_test

import (
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"crucible/internal/db/dbtest"
)

// 00023 turns teams with one role per person and per-team programs into tags, one team per person and one run per
// training, without losing who could do what; its Down puts back what it can.
func TestMigration00023ConvertsTeamsAndRoles(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(os.DirFS("."))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(sqlDB, "migrations", 22); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO teams (id, name) VALUES ('a', 'A'), ('b', 'B')`,
		// x leads a and is a trainee in b: they stay a's leader. y leads b only and is a member of a: they stay b's leader.
		`INSERT INTO team_members VALUES ('a','x@x','leader'), ('a','m@x','member'), ('a','t@x','trainee'), ('a','y@x','member'),
			('b','y@x','leader'), ('b','x@x','trainee'), ('b','u@x','trainee')`,
		`INSERT INTO mentors VALUES ('a','t@x','m@x'), ('b','x@x','y@x')`,
		`INSERT INTO trainings (id, repo) VALUES ('k', 'r'), ('solo', 'r')`, // at 22: trainings still carry the URL
		`INSERT INTO programs (team, training) VALUES ('a','k'), ('b','k')`,
		`INSERT INTO enrollments VALUES ('a','k','t@x'), ('b','k','u@x'), ('b','k','x@x')`,
		`INSERT INTO program_roles VALUES ('a','k','m@x','scorer'), ('b','k','y@x','approver')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := goose.UpTo(sqlDB, "migrations", 23); err != nil {
		t.Fatal(err)
	}
	rows := func(q string) []string {
		t.Helper()
		r, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(r, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := rows(`SELECT team || ':' || email || ':' || role FROM team_members ORDER BY 1`); !slices.Equal(got,
		[]string{"a:m@x:junior", "a:t@x:trainee", "a:x@x:leader", "b:u@x:trainee", "b:y@x:leader"}) {
		t.Errorf("members = %v", got)
	}
	if got := rows(`SELECT team || ':' || trainee || ':' || mentor FROM mentors`); !slices.Equal(got, []string{"a:t@x:m@x"}) {
		t.Errorf("mentors = %v: pairs whose people left go", got)
	}
	if got := rows(`SELECT team || ':' || training FROM programs`); !slices.Equal(got, []string{"b:k"}) {
		t.Errorf("programs = %v: k keeps the run with the most participants", got)
	}
	if got := rows(`SELECT role || ':' || ref FROM training_roles WHERE training = 'k' ORDER BY 1`); !slices.Equal(got, []string{
		"admin:y@x", "editor:b_leaders", "editor:b_seniors", "editor:m@x", "participant:t@x", "participant:u@x", "participant:x@x"}) {
		t.Errorf("roles = %v", got)
	}
	if err := goose.DownTo(sqlDB, "migrations", 22); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := rows(`SELECT team || ':' || email FROM enrollments ORDER BY 1`); !slices.Equal(got, []string{"b:t@x", "b:u@x", "b:x@x"}) {
		t.Errorf("enrollments after down = %v", got)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
