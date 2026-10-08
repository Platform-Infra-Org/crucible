package org

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

func (s *Store) Admins(ctx context.Context) ([]string, error) {
	return s.strings(ctx, `SELECT email FROM admins ORDER BY email`)
}

func (s *Store) AddAdmin(ctx context.Context, actor, email string) error {
	email = em(email)
	if err := config.CheckEmail("admin", email); err != nil {
		return err
	}
	detail := map[string]any{"email": email}
	return s.inTx(ctx, actor, "admin.add", email, detail, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO admins (email) VALUES ($1) ON CONFLICT DO NOTHING`, email)
		if err == nil && tag.RowsAffected() == 0 {
			detail["already"] = true // no grant happened: the audit row must not read as one
		}
		return err
	})
}

// RemoveAdmin refuses to remove the last admin. FOR UPDATE locks every admin row, so two concurrent removals
// serialise and the second sees the first's result.
func (s *Store) RemoveAdmin(ctx context.Context, actor, email string) error {
	email = em(email)
	return s.inTx(ctx, actor, "admin.remove", email, map[string]any{"email": email}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT email FROM admins FOR UPDATE`)
		if err != nil {
			return err
		}
		all, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		found := false
		for _, a := range all {
			found = found || a == email
		}
		if !found {
			return apperr.Wrap(apperr.NotFound, email+" is not an admin")
		}
		if len(all) == 1 {
			return apperr.Wrap(apperr.Invalid, "this is the only admin; add another before removing this one")
		}
		_, err = tx.Exec(ctx, `DELETE FROM admins WHERE email = $1`, email)
		return err
	})
}

var errNotSeeded = errors.New("admins already set")

// SeedAdmin inserts email only while no admin exists; true when it inserted. Audited as admin.bootstrap.
func (s *Store) SeedAdmin(ctx context.Context, email string) (bool, error) {
	email = em(email)
	if err := config.CheckEmail("bootstrap admin", email); err != nil {
		return false, err
	}
	// ponytail: two simultaneous first starts could both seed; the API runs one replica.
	err := s.inTx(ctx, "bootstrap", "admin.bootstrap", email, map[string]any{"bootstrap": true, "email": email}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO admins (email) SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM admins)`, email)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotSeeded // rolls back, no audit row
		}
		return nil
	})
	if errors.Is(err, errNotSeeded) {
		return false, nil
	}
	return err == nil, err
}
