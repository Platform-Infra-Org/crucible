package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/config"
)

// SettingsBody is what an admin saves. Version is the one the page read; a mismatch means someone else saved first.
type SettingsBody struct {
	Version           int64                 `json:"version"`
	DefaultTheme      string                `json:"default_theme"`
	CostTiers         *config.CostTiers     `json:"cost_tiers"`
	ClusterUSDPerHour *float64              `json:"cluster_usd_per_hour"`
	EscalationHours   float64               `json:"escalation_hours"`
	Ranks             config.RankThresholds `json:"ranks"`
}

// inTx runs fn and the audit row in one transaction: both land or neither does.
func (s *Store) inTx(ctx context.Context, actor, action, target string, detail map[string]any, fn func(pgx.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, actor, action, target, detail, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetSettings(ctx context.Context, actor string, b SettingsBody) error {
	if b.DefaultTheme == "" {
		b.DefaultTheme = "forge"
	}
	if !slices.Contains(config.Themes, b.DefaultTheme) {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("default theme must be one of %v", config.Themes))
	}
	var auto, t1, t2 *float64
	if t := b.CostTiers; t != nil {
		if err := t.Validate(); err != nil {
			return apperr.Wrap(apperr.Invalid, err.Error())
		}
		auto, t1, t2 = &t.AutoApproveUSD, &t.Tier1USD, &t.Tier2USD
	}
	if r := b.ClusterUSDPerHour; r != nil && (*r < 0 || math.IsNaN(*r) || math.IsInf(*r, 0)) {
		return apperr.Wrap(apperr.Invalid, "cluster rate must be a number >= 0")
	}
	if b.EscalationHours == 0 {
		b.EscalationHours = 4
	}
	if b.EscalationHours < 0 || math.IsNaN(b.EscalationHours) || math.IsInf(b.EscalationHours, 0) {
		return apperr.Wrap(apperr.Invalid, "escalation hours must be positive")
	}
	r := b.Ranks
	if err := r.Fill(); err != nil {
		return apperr.Wrap(apperr.Invalid, "ranks: "+err.Error())
	}
	return s.inTx(ctx, actor, "settings.update", "settings", map[string]any{"version": b.Version}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE settings SET default_theme = $1, auto_approve_usd = $2, tier1_usd = $3, tier2_usd = $4,
			cluster_usd_per_hour = $5, escalation_hours = $6, rank_ingot = $7, rank_tempered = $8, rank_blade = $9,
			rank_sword = $10, rank_masterwork = $11, version = version + 1 WHERE id = 1 AND version = $12`,
			b.DefaultTheme, auto, t1, t2, b.ClusterUSDPerHour, b.EscalationHours,
			r.Ingot, r.Tempered, r.Blade, r.Sword, r.Masterwork, b.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.Wrap(apperr.Conflict, "someone changed the settings, reload")
		}
		return nil
	})
}

func (s *Store) SetSchedule(ctx context.Context, actor, name string, sched config.Schedule) error {
	if strings.TrimSpace(name) == "" {
		return apperr.Wrap(apperr.Invalid, "a schedule needs a name")
	}
	if err := sched.Validate(); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	windows, err := json.Marshal(sched.Windows)
	if err != nil {
		return err
	}
	return s.inTx(ctx, actor, "schedule.update", name, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO schedules (name, timezone, windows) VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET timezone = EXCLUDED.timezone, windows = EXCLUDED.windows`,
			name, sched.Timezone, windows)
		return err
	})
}

func (s *Store) DeleteSchedule(ctx context.Context, actor, name string) error {
	return s.inTx(ctx, actor, "schedule.delete", name, nil, func(tx pgx.Tx) error {
		var team, training string
		err := tx.QueryRow(ctx, `SELECT team, training FROM programs WHERE schedule_name = $1 ORDER BY team, training LIMIT 1`, name).Scan(&team, &training)
		if err == nil {
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("schedule %q is used by team %s, training %s; move it to another schedule first", name, team, training))
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM schedules WHERE name = $1`, name)
		if err != nil {
			return scheduleInUse(err, name)
		}
		if tag.RowsAffected() == 0 {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no schedule named %q", name))
		}
		return nil
	})
}

// scheduleInUse maps the programs.schedule_name foreign key firing (a program claimed the schedule after our
// check) to the same Conflict the check gives. Other errors pass through.
func scheduleInUse(err error, name string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return apperr.Wrap(apperr.Conflict, fmt.Sprintf("schedule %q is used by a program; move it to another schedule first", name))
	}
	return err
}

// SetQuotes replaces the stored list; the built-in quotes are added at serve time.
func (s *Store) SetQuotes(ctx context.Context, actor string, quotes []string) error {
	for i, q := range quotes {
		if strings.TrimSpace(q) == "" {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("quote %d is empty", i+1))
		}
	}
	return s.inTx(ctx, actor, "quotes.update", "quotes", map[string]any{"count": len(quotes)}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM quotes`); err != nil {
			return err
		}
		for _, q := range quotes {
			if _, err := tx.Exec(ctx, `INSERT INTO quotes (text) VALUES ($1)`, q); err != nil {
				return err
			}
		}
		return nil
	})
}
