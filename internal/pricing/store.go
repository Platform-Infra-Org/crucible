package pricing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStale: prices are missing or older than maxAge; estimates fail closed until a refresh succeeds.
var ErrStale = errors.New("price data is missing or older than 30 days; estimates are unavailable until the weekly refresh succeeds")

const maxAge = 30 * 24 * time.Hour

type Row struct {
	Service, Region, Key, Unit string
	USD                        float64
}

type Store struct {
	DB     *pgxpool.Pool
	Now    func() time.Time
	Source string // where this process's prices come from: "fixture" (dry run) or "aws" ("" means aws)
}

// Snapshot is an in-memory copy of the price rows; it implements Lookup.
type Snapshot map[[3]string]float64

func (s Snapshot) Price(service, region, key string) (float64, bool) {
	p, ok := s[[3]string{service, region, key}]
	return p, ok
}

func (s *Store) source() string {
	if s.Source == "" {
		return "aws"
	}
	return s.Source
}

// refreshedAt is when the last refresh from this store's source succeeded; nil when never, or when the rows come from
// another source (a dry run's fixture never prices a real lab, nor passes for a fresh real refresh).
func (s *Store) refreshedAt(ctx context.Context) (*time.Time, error) {
	var okAt *time.Time
	var src string
	if err := s.DB.QueryRow(ctx, `SELECT ok_at, source FROM price_refresh WHERE id = 1`).Scan(&okAt, &src); err != nil {
		return nil, err
	}
	if src != s.source() {
		return nil, nil
	}
	return okAt, nil
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	okAt, err := s.refreshedAt(ctx)
	if err != nil {
		return nil, err
	}
	if okAt == nil || s.Now().Sub(*okAt) > maxAge {
		return nil, ErrStale
	}
	rows, err := s.DB.Query(ctx, `SELECT service, region, key, usd FROM prices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snap := Snapshot{}
	for rows.Next() {
		var k [3]string
		var usd float64
		if err := rows.Scan(&k[0], &k[1], &k[2], &usd); err != nil {
			return nil, err
		}
		snap[k] = usd
	}
	return snap, rows.Err()
}

// Replace swaps the aws rows for rows and records the refresh and its source, in one transaction.
func (s *Store) Replace(ctx context.Context, rows []Row) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := s.Now()
	if _, err := tx.Exec(ctx, `DELETE FROM prices WHERE service = 'aws'`); err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		b.Queue(`INSERT INTO prices (service, region, key, usd, unit, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			r.Service, r.Region, r.Key, r.USD, r.Unit, now)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE price_refresh SET ok_at = $1, error = '', error_at = NULL, source = $2 WHERE id = 1`, now, s.source()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Failed records why a refresh failed; the old rows stay.
func (s *Store) Failed(ctx context.Context, cause error) error {
	_, err := s.DB.Exec(ctx, `UPDATE price_refresh SET error = $1, error_at = $2 WHERE id = 1`, cause.Error(), s.Now())
	return err
}

// FreshWithin reports whether a refresh succeeded less than d ago.
func (s *Store) FreshWithin(ctx context.Context, d time.Duration) (bool, error) {
	okAt, err := s.refreshedAt(ctx)
	if err != nil {
		return false, err
	}
	return okAt != nil && s.Now().Sub(*okAt) < d, nil
}
