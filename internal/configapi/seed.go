package configapi

import (
	"context"
	"strings"

	"crucible/internal/audit"
	"crucible/internal/gitsync"
	"crucible/internal/org"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedAdmin inserts the bootstrap admin when the admins table is empty (spec §5.1: the bootstrap value only lets the
// first person in). Once any admin exists it does nothing.
func SeedAdmin(ctx context.Context, db *pgxpool.Pool, email string) (bool, error) {
	return (&org.Store{DB: db}).SeedAdmin(ctx, email)
}

// BootstrapAdmin seeds at most once per deployment: the first run leaves an admin.bootstrap audit row (actor
// "bootstrap", target the email), and later starts skip, so removing every admin is not undone by a restart.
func BootstrapAdmin(ctx context.Context, db *pgxpool.Pool, _ *gitsync.Writer, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var done bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_log WHERE action = 'admin.bootstrap')`).Scan(&done); err != nil || done {
		return false, err
	}
	ok, err := SeedAdmin(ctx, db, email)
	if err != nil {
		return false, err // no marker: the next start tries again
	}
	if ok {
		return true, nil // SeedAdmin wrote the admin.bootstrap row
	}
	// admins already existed (seeded=false): the first start has happened either way
	return false, audit.Log(context.WithoutCancel(ctx), db, "bootstrap", "admin.bootstrap", email, map[string]any{"seeded": ok}, "")
}
