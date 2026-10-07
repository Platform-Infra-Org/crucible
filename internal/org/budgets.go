package org

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
)

// BudgetBody is a team's monthly budget (alerts at 80%) and hard cap (blocks lab requests above it).
// Version is the one the page read; 0 means "no budget saved yet".
type BudgetBody struct {
	Version    int64   `json:"version"`
	MonthlyUSD float64 `json:"monthly_usd"`
	HardCapUSD float64 `json:"hard_cap_usd"`
}

// SetBudget saves the budget. An unset hard cap (0) is stored as the monthly budget, as config.Load does, so
// what is stored and audited is the number that will be enforced.
func (s *Store) SetBudget(ctx context.Context, actor, team string, b BudgetBody) error {
	for _, v := range []float64{b.MonthlyUSD, b.HardCapUSD} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return apperr.Wrap(apperr.Invalid, "budget amounts must be finite numbers")
		}
	}
	if b.MonthlyUSD < 0 || b.HardCapUSD < 0 {
		return apperr.Wrap(apperr.Invalid, "budget amounts cannot be negative")
	}
	if b.HardCapUSD == 0 {
		b.HardCapUSD = b.MonthlyUSD
	}
	if b.HardCapUSD < b.MonthlyUSD {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("the hard cap ($%g) is below the monthly budget ($%g); the cap must be at least the budget", b.HardCapUSD, b.MonthlyUSD))
	}
	detail := map[string]any{"version": b.Version + 1, "monthly_usd": b.MonthlyUSD, "hard_cap_usd": b.HardCapUSD}
	return s.inTx(ctx, actor, "team.budget", team, detail, func(tx pgx.Tx) error {
		if b.Version == 0 {
			tag, err := tx.Exec(ctx, `INSERT INTO team_budgets (team, monthly_usd, hard_cap_usd, version) VALUES ($1, $2, $3, 1)
				ON CONFLICT (team) DO NOTHING`, team, b.MonthlyUSD, b.HardCapUSD)
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23503" {
				return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
			}
			if err == nil && tag.RowsAffected() == 0 {
				return apperr.Wrap(apperr.Conflict, "someone set this budget already, reload")
			}
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE team_budgets SET monthly_usd = $2, hard_cap_usd = $3, version = version + 1
			WHERE team = $1 AND version = $4`, team, b.MonthlyUSD, b.HardCapUSD, b.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM teams WHERE id = $1`, team).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
			}
			return apperr.Wrap(apperr.Conflict, "someone changed this budget, reload")
		}
		return nil
	})
}
