package org

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"crucible/internal/audit"
	"crucible/internal/config"
)

// Seed imports a platform read from YAML (config.Load of a seed directory: examples/platform for the local stack and
// the e2e suite, a generated one for crucible preview) into an empty instance. It goes through the same validated,
// audited writes the admin pages use, as the actor "seed", so a seed can never store what the UI would refuse.
//
// It runs at most once per database: an instance that already has a team or a training, or was seeded before, is
// left alone, so deleting everything later is not undone by a restart. True when it imported.
func (s *Store) Seed(ctx context.Context, p *config.Platform) (bool, error) {
	var busy bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams) OR EXISTS (SELECT 1 FROM trainings)
		OR EXISTS (SELECT 1 FROM audit_log WHERE action = 'seed.import')`).Scan(&busy); err != nil || busy {
		return false, err
	}
	const actor = "seed"
	st := p.Settings
	var v int64
	if err := s.DB.QueryRow(ctx, `SELECT version FROM settings WHERE id = 1`).Scan(&v); err != nil {
		return false, err
	}
	if err := s.SetSettings(ctx, actor, SettingsBody{Version: v, DefaultTheme: st.DefaultTheme, CostTiers: st.CostTiers,
		EscalationHours: st.EscalationHours, Ranks: st.Ranks}); err != nil {
		return false, fmt.Errorf("settings: %w", err)
	}
	for _, name := range slices.Sorted(maps.Keys(st.Schedules)) {
		if err := s.SetSchedule(ctx, actor, name, *st.Schedules[name]); err != nil {
			return false, fmt.Errorf("schedule %s: %w", name, err)
		}
	}
	if len(st.Quotes) > 0 {
		if err := s.SetQuotes(ctx, actor, st.Quotes); err != nil {
			return false, fmt.Errorf("quotes: %w", err)
		}
	}
	for _, a := range p.Admins {
		if err := s.AddAdmin(ctx, actor, a); err != nil {
			return false, fmt.Errorf("admin %s: %w", a, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.Trainings)) {
		if err := s.AddTraining(ctx, actor, id, p.Trainings[id]); err != nil {
			return false, fmt.Errorf("training %s: %w", id, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.Teams)) {
		if err := s.seedTeam(ctx, actor, id, p.Teams[id]); err != nil {
			return false, fmt.Errorf("team %s: %w", id, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.Teams)) {
		if err := s.seedPrograms(ctx, actor, id, p.Teams[id]); err != nil {
			return false, fmt.Errorf("team %s: %w", id, err)
		}
	}
	err := audit.Log(ctx, s.DB, actor, "seed.import", "platform", map[string]any{"teams": len(p.Teams), "trainings": len(p.Trainings)}, "")
	return err == nil, err
}

func (s *Store) seedTeam(ctx context.Context, actor, id string, t *config.Team) error {
	if err := s.CreateTeam(ctx, actor, id, BodyOf(t)); err != nil {
		return err
	}
	if b := t.Budget; b.MonthlyUSD > 0 || b.HardCapUSD > 0 {
		if err := s.SetBudget(ctx, actor, id, BudgetBody{MonthlyUSD: &b.MonthlyUSD, HardCapUSD: &b.HardCapUSD}); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	for kind, url := range map[string]string{"slack": t.Notifications.SlackWebhook, "teams": t.Notifications.TeamsWebhook} {
		if url == "" {
			continue
		}
		if err := s.SetTeamWebhook(ctx, actor, id, kind, url); err != nil {
			return fmt.Errorf("%s webhook: %w", kind, err)
		}
	}
	return nil
}

// seedPrograms gives each training its owning team's run, after every team exists (a role may name another team's
// group).
func (s *Store) seedPrograms(ctx context.Context, actor, id string, t *config.Team) error {
	for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
		p := t.Programs[tr]
		budget := p.BudgetUSDMonth
		if err := s.Enroll(ctx, actor, id, tr, ProgramBody{Roles: p.Access, Schedule: p.Schedule,
			InlineSchedule: p.Inline, TTL: dur(p.LabDefaults.TTL), IdleTimeout: dur(p.LabDefaults.IdleTimeout),
			MaxExtension: dur(p.LabDefaults.MaxExtension), BudgetUSDMonth: &budget, ReviewSelfReported: p.ReviewSelfReported}); err != nil {
			return fmt.Errorf("program %s: %w", tr, err)
		}
		if p.PinnedRef != "" {
			if err := s.SetPin(ctx, actor, id, tr, p.PinnedRef); err != nil {
				return fmt.Errorf("program %s pin: %w", tr, err)
			}
		}
	}
	return nil
}
