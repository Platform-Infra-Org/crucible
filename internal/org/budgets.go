package org

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

// BudgetBody is a team's monthly budget (alerts at 80%) and hard cap (blocks lab requests above it).
// Version is the one the page read; 0 means "no budget saved yet". MonthlyUSD is required so a misspelled key
// cannot save 0/0 (which lifts the cap); HardCapUSD nil or 0 defaults to the monthly budget, as in budget.yaml.
type BudgetBody struct {
	Version    int64    `json:"version"`
	MonthlyUSD *float64 `json:"monthly_usd"`
	HardCapUSD *float64 `json:"hard_cap_usd"`
}

// SetBudget saves the budget. The resolved hard cap is what is stored and audited, with the previous values.
func (s *Store) SetBudget(ctx context.Context, actor, team string, b BudgetBody) error {
	if b.MonthlyUSD == nil {
		return apperr.Wrap(apperr.Invalid, "monthly_usd is required")
	}
	nb := config.Budget{MonthlyUSD: *b.MonthlyUSD}
	if b.HardCapUSD != nil {
		nb.HardCapUSD = *b.HardCapUSD
	}
	defaulted := nb.HardCapUSD == 0
	if err := nb.Validate(); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	detail := map[string]any{"version": b.Version + 1, "monthly_usd": nb.MonthlyUSD, "hard_cap_usd": nb.HardCapUSD,
		"hard_cap_defaulted": defaulted, "previous_monthly_usd": nil, "previous_hard_cap_usd": nil}
	return s.inTx(ctx, actor, "team.budget", team, detail, func(tx pgx.Tx) error {
		var pm, pc float64
		if err := tx.QueryRow(ctx, `SELECT monthly_usd, hard_cap_usd FROM team_budgets WHERE team = $1`, team).Scan(&pm, &pc); err == nil {
			detail["previous_monthly_usd"], detail["previous_hard_cap_usd"] = pm, pc // audit.Log runs after fn
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if b.Version == 0 {
			tag, err := tx.Exec(ctx, `INSERT INTO team_budgets (team, monthly_usd, hard_cap_usd, version) VALUES ($1, $2, $3, 1)
				ON CONFLICT (team) DO NOTHING`, team, nb.MonthlyUSD, nb.HardCapUSD)
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
			WHERE team = $1 AND version = $4`, team, nb.MonthlyUSD, nb.HardCapUSD, b.Version)
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
