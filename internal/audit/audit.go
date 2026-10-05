// Package audit records privileged actions (spec §14: "all privileged actions in audit_log").
package audit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	Detail    map[string]any `json:"detail"`
	CommitSHA string         `json:"commit_sha,omitempty"`
}

// Execer is satisfied by *pgxpool.Pool and pgx.Tx, so an entry can be written in the same transaction as the action.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func Log(ctx context.Context, db Execer, actor, action, target string, detail map[string]any, commitSHA string) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := db.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail, commit_sha) VALUES (lower($1), $2, $3, $4, $5)`,
		actor, action, target, detail, commitSHA)
	return err
}

// Recent returns the newest entries first.
func Recent(ctx context.Context, db *pgxpool.Pool, limit int) ([]Entry, error) {
	rows, err := db.Query(ctx, `SELECT at, actor, action, target, detail, commit_sha FROM audit_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.At, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.CommitSHA)
		return e, err
	})
}
