package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

// ProgramBody is a training's run as its managers save it: who holds which role, and the run settings. Version is the
// one the page read. Exactly one of Schedule (a named schedule) and InlineSchedule may be set; neither means any time.
type ProgramBody struct {
	Version            int64            `json:"version"`
	Roles              config.Access    `json:"roles"`
	Schedule           string           `json:"schedule"`
	InlineSchedule     *config.Schedule `json:"inline_schedule"`
	TTL                string           `json:"ttl"`
	IdleTimeout        string           `json:"idle_timeout"`
	MaxExtension       string           `json:"max_extension"`
	BudgetUSDMonth     *float64         `json:"budget_usd_month"` // required: 0 means "no program budget", so a missing key must not store it
	ReviewSelfReported bool             `json:"review_self_reported"`
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// program is a validated body: refs normalised and deduplicated, durations canonical, schedule resolved.
type program struct {
	roles    map[string][]string // participant, editor, admin -> sorted refs
	schedule *string
	inline   []byte
	durs     [3]*string // ttl, idle_timeout, max_extension
	budget   float64
	review   bool
}

// refs normalises a role's holders: emails and group refs, deduplicated and sorted. Groups are checked against the
// teams in checkGroups.
func refs(role string, in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range in {
		r = em(r)
		if r == "" {
			continue
		}
		if strings.Contains(r, "@") {
			if err := config.CheckEmail(role, r); err != nil {
				return nil, err
			}
		} else if strings.ContainsFunc(r, func(c rune) bool { return c == ' ' || c == '/' }) {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s %q is neither an email nor a group", role, r))
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (b ProgramBody) validate() (*program, error) {
	if b.BudgetUSDMonth == nil {
		return nil, apperr.Wrap(apperr.Invalid, "budget_usd_month is required; send 0 for no program budget")
	}
	p := &program{roles: map[string][]string{}, budget: *b.BudgetUSDMonth, review: b.ReviewSelfReported}
	var err error
	for _, g := range []struct {
		role string
		in   []string
	}{{"participant", b.Roles.Participants}, {"editor", b.Roles.Editors}, {"admin", b.Roles.Admins}} {
		if p.roles[g.role], err = refs(g.role, g.in); err != nil {
			return nil, err
		}
	}
	// Someone has to be able to manage it: a training with no admins could be changed only by a Crucible admin, and
	// the people who run it would be locked out of their own training. One person or one group is enough.
	if len(p.roles["admin"]) == 0 {
		return nil, apperr.Wrap(apperr.Invalid, "a training needs at least one admin: keep one person or one group before removing the rest")
	}
	if name := strings.TrimSpace(b.Schedule); name != "" {
		if b.InlineSchedule != nil {
			return nil, apperr.Wrap(apperr.Invalid, "a program uses a named schedule or an inline one, not both")
		}
		p.schedule = &name
	}
	if b.InlineSchedule != nil {
		if err := b.InlineSchedule.Validate(); err != nil {
			return nil, apperr.Wrap(apperr.Invalid, "schedule: "+err.Error())
		}
		if p.inline, err = json.Marshal(b.InlineSchedule); err != nil {
			return nil, err
		}
	}
	for i, d := range []struct{ name, v string }{{"ttl", b.TTL}, {"idle_timeout", b.IdleTimeout}, {"max_extension", b.MaxExtension}} {
		if strings.TrimSpace(d.v) == "" {
			continue
		}
		dur, err := time.ParseDuration(strings.TrimSpace(d.v))
		if err != nil || dur < 0 {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s %q must be a duration such as 30m or 4h", d.name, d.v))
		}
		s := dur.String()
		p.durs[i] = &s
	}
	if err := (&config.Budget{MonthlyUSD: p.budget}).Validate(); err != nil {
		return nil, apperr.Wrap(apperr.Invalid, "budget_usd_month: "+err.Error())
	}
	return p, nil
}

func (p *program) detail(version int64) map[string]any {
	return map[string]any{"version": version, "participants": p.roles["participant"], "editors": p.roles["editor"],
		"admins": p.roles["admin"], "schedule": p.schedule, "inline_schedule": p.inline != nil,
		"ttl": p.durs[0], "idle_timeout": p.durs[1], "max_extension": p.durs[2], "budget_usd_month": p.budget,
		"review_self_reported": p.review}
}

// checkGroups refuses a group of a team that does not exist, naming it. Emails may be anyone: a role can go to someone
// who has not signed in yet.
func (p *program) checkGroups(ctx context.Context, tx pgx.Tx) error {
	for _, role := range []string{"participant", "editor", "admin"} {
		for _, r := range p.roles[role] {
			if strings.Contains(r, "@") {
				continue
			}
			team, _ := config.SplitGroup(r)
			if team == "" {
				continue
			}
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM teams WHERE id = $1`, team).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s %q: there is no team %q (groups are all, all_seniors, <team> and <team>_seniors, …)", role, r, team))
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *program) writeChildren(ctx context.Context, tx pgx.Tx, training string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM training_roles WHERE training = $1`, training); err != nil {
		return err
	}
	for role, rs := range p.roles {
		for _, r := range rs {
			if _, err := tx.Exec(ctx, `INSERT INTO training_roles (training, ref, role) VALUES ($1,$2,$3)`, training, r, role); err != nil {
				return err
			}
		}
	}
	return nil
}

// fkError turns a foreign-key failure into the sentence an admin needs, instead of a Postgres error.
func fkError(err error, team, training string, schedule *string) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23503" {
		return err
	}
	switch {
	case strings.Contains(pg.ConstraintName, "schedule"):
		name := ""
		if schedule != nil {
			name = *schedule
		}
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("no schedule named %q", name))
	case strings.Contains(pg.ConstraintName, "training"):
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("training %q is not registered", training))
	default:
		return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
	}
}

// Enroll gives training to team: its one run, with the roles and settings in the body. The seed uses it; a training
// registered in the UI gets its run from CreateTraining, and an admin moves one with SetOwner.
func (s *Store) Enroll(ctx context.Context, actor, team, training string, b ProgramBody) error {
	p, err := b.validate()
	if err != nil {
		return err
	}
	detail := map[string]any{}
	return s.inTx(ctx, actor, "program.enroll", team+"/"+training, detail, func(tx pgx.Tx) error {
		if err := p.checkGroups(ctx, tx); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO programs (team, training, schedule_name, inline_schedule, ttl, idle_timeout,
			max_extension, budget_usd_month, review_self_reported) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			team, training, p.schedule, p.inline, p.durs[0], p.durs[1], p.durs[2], p.budget, p.review)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("%s already belongs to a team", training))
		}
		if err != nil {
			return fkError(err, team, training, p.schedule)
		}
		if err := p.writeChildren(ctx, tx, training); err != nil {
			return err
		}
		for k, v := range p.detail(1) { // audit.Log runs after fn
			detail[k] = v
		}
		return nil
	})
}

// SetProgram replaces a training's roles and its run's schedule, lab defaults and budget. The pin is not touched.
func (s *Store) SetProgram(ctx context.Context, actor, team, training string, b ProgramBody) error {
	p, err := b.validate()
	if err != nil {
		return err
	}
	return s.inTx(ctx, actor, "program.update", team+"/"+training, p.detail(b.Version+1), func(tx pgx.Tx) error {
		if err := p.checkGroups(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE programs SET schedule_name = $3, inline_schedule = $4, ttl = $5, idle_timeout = $6,
			max_extension = $7, budget_usd_month = $8, review_self_reported = $9, version = version + 1
			WHERE team = $1 AND training = $2 AND version = $10`,
			team, training, p.schedule, p.inline, p.durs[0], p.durs[1], p.durs[2], p.budget, p.review, b.Version)
		if err != nil {
			return fkError(err, team, training, p.schedule)
		}
		if tag.RowsAffected() == 0 {
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM programs WHERE team = $1 AND training = $2`, team, training).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s has no program %s", team, training))
			}
			return apperr.Wrap(apperr.Conflict, "someone changed this training, reload")
		}
		return p.writeChildren(ctx, tx, training)
	})
}

// SetPin moves the commit participants' attempts run at; an empty sha follows the source head again. The audit row holds
// the previous and new commit. The route checks that the commit exists in the mirror.
func (s *Store) SetPin(ctx context.Context, actor, team, training, sha string) error {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha != "" && !shaRE.MatchString(sha) {
		return apperr.Wrap(apperr.Invalid, "a pin is a full 40-character commit SHA")
	}
	detail := map[string]any{"sha": sha, "previous_sha": nil}
	err := s.inTx(ctx, actor, "program.pin", team+"/"+training, detail, func(tx pgx.Tx) error {
		var prev *string
		err := tx.QueryRow(ctx, `SELECT pinned_ref FROM programs WHERE team = $1 AND training = $2 FOR UPDATE`, team, training).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s has no program %s", team, training))
		}
		if err != nil {
			return err
		}
		if (prev == nil && sha == "") || (prev != nil && *prev == sha) {
			return errNoChange
		}
		if prev != nil {
			detail["previous_sha"] = *prev // audit.Log runs after fn
		}
		var val *string
		if sha != "" {
			val = &sha
		}
		_, err = tx.Exec(ctx, `UPDATE programs SET pinned_ref = $3, version = version + 1 WHERE team = $1 AND training = $2`, team, training, val)
		return err
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// SetOwner gives training to team: its run (pin, schedule, budget, roles) moves with it, or starts empty when no team
// owned it. Admins only.
func (s *Store) SetOwner(ctx context.Context, actor, training, team string) error {
	detail := map[string]any{"team": team, "previous_team": nil}
	err := s.inTx(ctx, actor, "training.owner", training, detail, func(tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, `SELECT team FROM programs WHERE training = $1 FOR UPDATE`, training).Scan(&prev)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(ctx, `INSERT INTO programs (team, training) VALUES ($1, $2)`, team, training)
		case err != nil:
			return err
		case prev == team:
			return errNoChange
		default:
			detail["previous_team"] = prev // audit.Log runs after fn
			_, err = tx.Exec(ctx, `UPDATE programs SET team = $2, version = version + 1 WHERE training = $1`, training, team)
		}
		return fkError(err, team, training, nil)
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}
