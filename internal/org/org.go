// Package org owns Crucible's configuration and org data: settings, schedules, quotes, admins, the
// training registry, teams, membership, mentors, budgets, programs, roles and enrollments.
// It returns config.Platform so every consumer of the old git-loaded config is unchanged.
package org

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/config"
	"crucible/internal/yamlx"
)

type Store struct{ DB *pgxpool.Pool }

// Platform reads every table into the struct config.Load returns.
func (s *Store) Platform(ctx context.Context) (*config.Platform, error) {
	p := &config.Platform{Trainings: map[string]config.TrainingRef{}, Teams: map[string]*config.Team{}}
	if err := s.settings(ctx, &p.Settings); err != nil {
		return nil, err
	}
	var err error
	if p.Admins, err = s.strings(ctx, `SELECT email FROM admins ORDER BY email`); err != nil {
		return nil, err
	}
	if p.Settings.Quotes, err = s.strings(ctx, `SELECT text FROM quotes ORDER BY id`); err != nil {
		return nil, err
	}
	if err := s.trainings(ctx, p); err != nil {
		return nil, err
	}
	if err := s.teams(ctx, p); err != nil {
		return nil, err
	}
	if err := s.programs(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// em normalises an email the way config.Load does.
func em(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (s *Store) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func (s *Store) settings(ctx context.Context, st *config.Settings) error {
	var auto, t1, t2, cluster *float64
	r := &st.Ranks
	err := s.DB.QueryRow(ctx, `SELECT default_theme, auto_approve_usd::float8, tier1_usd::float8, tier2_usd::float8,
		cluster_usd_per_hour::float8, escalation_hours::float8, rank_ingot::float8, rank_tempered::float8,
		rank_blade::float8, rank_sword::float8, rank_masterwork::float8 FROM settings WHERE id = 1`).
		Scan(&st.DefaultTheme, &auto, &t1, &t2, &cluster, &st.EscalationHours, &r.Ingot, &r.Tempered, &r.Blade, &r.Sword, &r.Masterwork)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if st.DefaultTheme == "" {
		st.DefaultTheme = "forge"
	}
	if !slices.Contains(config.Themes, st.DefaultTheme) {
		return fmt.Errorf("settings: default_theme %q must be one of %v", st.DefaultTheme, config.Themes)
	}
	if auto != nil && t1 != nil && t2 != nil {
		st.CostTiers = &config.CostTiers{AutoApproveUSD: *auto, Tier1USD: *t1, Tier2USD: *t2}
		if err := st.CostTiers.Validate(); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
	}
	if cluster != nil && *cluster < 0 {
		return fmt.Errorf("settings: cluster_usd_per_hour must be >= 0")
	}
	st.ClusterUSDPerHour = cluster
	if st.EscalationHours == 0 {
		st.EscalationHours = 4
	}
	if st.EscalationHours < 0 {
		return fmt.Errorf("settings: escalation_hours must be positive")
	}
	if err := r.Fill(); err != nil {
		return fmt.Errorf("settings: ranks: %w", err)
	}
	st.Schedules = map[string]*config.Schedule{}
	rows, err := s.DB.Query(ctx, `SELECT name, timezone, windows FROM schedules`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		sc := &config.Schedule{}
		if err := rows.Scan(&name, &sc.Timezone, &sc.Windows); err != nil {
			return fmt.Errorf("schedules: %w", err)
		}
		if err := sc.Validate(); err != nil {
			return fmt.Errorf("schedule %s: %w", name, err)
		}
		st.Schedules[name] = sc
	}
	return rows.Err()
}

func (s *Store) trainings(ctx context.Context, p *config.Platform) error {
	rows, err := s.DB.Query(ctx, `SELECT id, repo, branch FROM trainings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ref config.TrainingRef
		if err := rows.Scan(&id, &ref.Repo, &ref.Branch); err != nil {
			return err
		}
		p.Trainings[id] = ref
	}
	return rows.Err()
}

func (s *Store) teams(ctx context.Context, p *config.Platform) error {
	rows, err := s.DB.Query(ctx, `SELECT id, name, version FROM teams`)
	if err != nil {
		return err
	}
	for rows.Next() {
		t := &config.Team{Programs: map[string]*config.Program{}, Mentors: map[string]string{}}
		if err := rows.Scan(&t.ID, &t.Name, &t.Version); err != nil {
			rows.Close()
			return err
		}
		p.Teams[t.ID] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, email, role FROM team_members ORDER BY email`); err != nil {
		return err
	}
	for rows.Next() {
		var team, email, role string
		if err := rows.Scan(&team, &email, &role); err != nil {
			rows.Close()
			return err
		}
		t := p.Teams[team]
		switch role {
		case "leader":
			t.Leader = em(email)
		case "senior":
			t.Seniors = append(t.Seniors, em(email))
		case "member":
			t.Members = append(t.Members, em(email))
		case "trainee":
			t.Trainees = append(t.Trainees, em(email))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range p.Teams {
		if t.Leader == "" {
			return fmt.Errorf("teams/%s: team has no leader", t.ID)
		}
		t.Seniors, t.Members, t.Trainees = nonNil(t.Seniors), nonNil(t.Members), nonNil(t.Trainees)
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, trainee, mentor FROM mentors`); err != nil {
		return err
	}
	for rows.Next() {
		var team, trainee, mentor string
		if err := rows.Scan(&team, &trainee, &mentor); err != nil {
			rows.Close()
			return err
		}
		p.Teams[team].Mentors[em(trainee)] = em(mentor)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, kind, url FROM team_webhooks`); err != nil {
		return err
	}
	for rows.Next() {
		var team, kind, url string
		if err := rows.Scan(&team, &kind, &url); err != nil {
			rows.Close()
			return err
		}
		if !strings.HasPrefix(url, "https://") {
			rows.Close()
			return fmt.Errorf("teams/%s: notifications: webhook URLs must start with https://", team)
		}
		if kind == "slack" {
			p.Teams[team].Notifications.SlackWebhook = url
		} else {
			p.Teams[team].Notifications.TeamsWebhook = url
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, monthly_usd::float8, hard_cap_usd::float8, version FROM team_budgets`); err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var team string
		var b config.Budget
		if err := rows.Scan(&team, &b.MonthlyUSD, &b.HardCapUSD, &b.Version); err != nil {
			return err
		}
		if b.HardCapUSD == 0 {
			b.HardCapUSD = b.MonthlyUSD
		}
		p.Teams[team].Budget = b
	}
	return rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func optDuration(s *string) (yamlx.Duration, error) {
	if s == nil || *s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(*s)
	return yamlx.Duration(d), err
}

func (s *Store) programs(ctx context.Context, p *config.Platform) error {
	rows, err := s.DB.Query(ctx, `SELECT team, training, pinned_ref, schedule_name, inline_schedule, ttl, idle_timeout,
		max_extension, budget_usd_month::float8, review_self_reported, version FROM programs`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var team string
		var pinned, name *string
		pr := &config.Program{}
		var inline []byte
		var ttl, idle, ext *string
		if err := rows.Scan(&team, &pr.Training, &pinned, &name, &inline, &ttl, &idle, &ext, &pr.BudgetUSDMonth, &pr.ReviewSelfReported, &pr.Version); err != nil {
			rows.Close()
			return err
		}
		if _, ok := p.Trainings[pr.Training]; !ok { // the foreign key forbids this; only corruption leaves it. Drop one program, not the whole config.
			slog.Warn("org: skipping program whose training row is missing", "team", team, "training", pr.Training)
			continue
		}
		fail := func(err error) error {
			rows.Close()
			return fmt.Errorf("program %s/%s: %w", team, pr.Training, err)
		}
		if pinned != nil {
			pr.PinnedRef = *pinned
		}
		if name != nil {
			pr.Schedule = *name
			if p.Settings.Schedules[pr.Schedule] == nil {
				return fail(fmt.Errorf("unknown schedule %q", pr.Schedule))
			}
		}
		if inline != nil {
			pr.Inline = &config.Schedule{}
			if err := json.Unmarshal(inline, pr.Inline); err != nil {
				return fail(err)
			}
			if err := pr.Inline.Validate(); err != nil {
				return fail(fmt.Errorf("schedule: %w", err))
			}
		}
		pr.ScheduleSpec = config.ScheduleRef{Name: pr.Schedule, Inline: pr.Inline}
		for _, d := range []struct {
			in  *string
			out *yamlx.Duration
		}{{ttl, &pr.LabDefaults.TTL}, {idle, &pr.LabDefaults.IdleTimeout}, {ext, &pr.LabDefaults.MaxExtension}} {
			if *d.out, err = optDuration(d.in); err != nil {
				return fail(err)
			}
		}
		pr.Enrolled, pr.Roles.Manager, pr.Roles.Scorers, pr.Roles.Approvers = []string{}, []string{}, []string{}, []string{}
		p.Teams[team].Programs[pr.Training] = pr
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, training, email, role FROM program_roles ORDER BY email`); err != nil {
		return err
	}
	for rows.Next() {
		var team, training, email, role string
		if err := rows.Scan(&team, &training, &email, &role); err != nil {
			rows.Close()
			return err
		}
		pr := p.Teams[team].Programs[training]
		if pr == nil {
			continue
		}
		r := &pr.Roles
		switch role {
		case "manager":
			r.Manager = append(r.Manager, em(email))
		case "scorer":
			r.Scorers = append(r.Scorers, em(email))
		case "approver":
			r.Approvers = append(r.Approvers, em(email))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if rows, err = s.DB.Query(ctx, `SELECT team, training, email FROM enrollments ORDER BY email`); err != nil {
		return err
	}
	for rows.Next() {
		var team, training, email string
		if err := rows.Scan(&team, &training, &email); err != nil {
			rows.Close()
			return err
		}
		pr := p.Teams[team].Programs[training]
		if pr == nil {
			continue
		}
		pr.Enrolled = append(pr.Enrolled, em(email))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Defaults config.Load applies to a program with no explicit roles.
	for _, t := range p.Teams {
		for _, pr := range t.Programs {
			if len(pr.Roles.Manager) == 0 {
				pr.Roles.Manager = []string{t.Leader}
			}
			if len(pr.Roles.Approvers) == 0 {
				pr.Roles.Approvers = []string{t.Leader}
			}
			if len(pr.Roles.Scorers) == 0 {
				pr.Roles.Scorers = slices.Clone(t.Seniors)
			}
		}
	}
	return nil
}
