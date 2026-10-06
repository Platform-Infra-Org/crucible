package configapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"crucible/internal/audit"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errSeeded = errors.New("admins already set")

// SeedAdmin writes the bootstrap admin into admins.yaml when the platform repo names no admin yet (spec §5.1: the Helm
// value exists only to seed admins.yaml on first start). A repo that already has an admin is never touched, so git stays
// the source of truth: afterwards the bootstrap value changes nothing, and removing the admin in git removes them.
func SeedAdmin(ctx context.Context, w *gitsync.Writer, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := checkEmail("bootstrap admin", email); err != nil {
		return false, err
	}
	_, changed, err := w.Apply(ctx, gitsync.Change{Action: "seed the bootstrap admin", Actor: email, Paths: []string{"admins.yaml"},
		Allow: func(p *config.Platform) error {
			if _, err := config.Load(w.Dir); err != nil { // a file that fails to parse can look like "no admins": never overwrite it
				return fmt.Errorf("the platform repo does not load cleanly, not seeding the bootstrap admin: %w", err)
			}
			if len(p.Admins) > 0 {
				return errSeeded
			}
			return nil
		},
		Edit: edit("admins.yaml", map[string]any{"admins": []string{email}})})
	if errors.Is(err, errSeeded) {
		return false, nil
	}
	return changed, err
}

// BootstrapAdmin seeds at most once per deployment: the first run leaves an admin.bootstrap audit row (actor
// "bootstrap", target the email), and later starts skip, so emptying admins.yaml in git is not undone by a restart.
func BootstrapAdmin(ctx context.Context, db *pgxpool.Pool, w *gitsync.Writer, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var done bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_log WHERE action = 'admin.bootstrap')`).Scan(&done); err != nil || done {
		return false, err
	}
	sctx, cancel := context.WithTimeout(ctx, gitWriteTimeout)
	defer cancel()
	ok, err := SeedAdmin(sctx, w, email)
	if err != nil {
		return false, err // no marker: the next start tries again
	}
	// also recorded when admins.yaml already named admins (seeded=false): the first start has happened either way
	return ok, audit.Log(context.WithoutCancel(ctx), db, "bootstrap", "admin.bootstrap", email, map[string]any{"seeded": ok}, "")
}
