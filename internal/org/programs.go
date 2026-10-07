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

// ProgramBody is what an admin saves for a team's enrollment of a training. Version is the one the page read.
// Exactly one of Schedule (a named schedule) and InlineSchedule may be set; neither means any time.
type ProgramBody struct {
	Version            int64            `json:"version"`
	Roles              config.Roles     `json:"roles"`
	Enrolled           []string         `json:"enrolled"`
	Schedule           string           `json:"schedule"`
	InlineSchedule     *config.Schedule `json:"inline_schedule"`
	TTL                string           `json:"ttl"`
	IdleTimeout        string           `json:"idle_timeout"`
	MaxExtension       string           `json:"max_extension"`
	BudgetUSDMonth     float64          `json:"budget_usd_month"`
	ReviewSelfReported bool             `json:"review_self_reported"`
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// program is a validated body: addresses normalised and deduplicated, durations canonical, schedule resolved.
type program struct {
	roles    map[string][]string // role -> sorted emails
	enrolled []string
	schedule *string
	inline   []byte
	durs     [3]*string // ttl, idle_timeout, max_extension
	budget   float64
	review   bool
}

func emails(field string, in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range in {
		e = em(e)
		if err := config.CheckEmail(field, e); err != nil {
			return nil, err
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (b ProgramBody) validate() (*program, error) {
	p := &program{roles: map[string][]string{}, budget: b.BudgetUSDMonth, review: b.ReviewSelfReported}
	var err error
	for _, g := range []struct {
		role string
		in   []string
	}{{"manager", b.Roles.Manager}, {"scorer", b.Roles.Scorers}, {"approver", b.Roles.Approvers}} {
		if p.roles[g.role], err = emails(g.role, g.in); err != nil {
			return nil, err
		}
	}
	if p.enrolled, err = emails("enrolled", b.Enrolled); err != nil {
		return nil, err
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
	if err := (&config.Budget{MonthlyUSD: b.BudgetUSDMonth}).Validate(); err != nil {
		return nil, apperr.Wrap(apperr.Invalid, "budget_usd_month: "+err.Error())
	}
	return p, nil
}

func (p *program) detail(version int64) map[string]any {
	return map[string]any{"version": version, "manager": p.roles["manager"], "scorers": p.roles["scorer"],
		"approvers": p.roles["approver"], "enrolled": p.enrolled, "schedule": p.schedule, "inline_schedule": p.inline != nil,
		"ttl": p.durs[0], "idle_timeout": p.durs[1], "max_extension": p.durs[2], "budget_usd_month": p.budget,
		"review_self_reported": p.review}
}

// teamRoster returns each member's role, or NotFound when the team does not exist.
func teamRoster(ctx context.Context, tx pgx.Tx, team string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT email, role FROM team_members WHERE team = $1`, team)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for rows.Next() {
		var e, r string
		if err := rows.Scan(&e, &r); err != nil {
			rows.Close()
			return nil, err
		}
		m[em(e)] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
	}
	return m, nil
}

// checkMembers refuses anyone outside the team, naming them.
func (p *program) checkMembers(roster map[string]string) error {
	for _, g := range []struct {
		what string
		in   []string
	}{{"manager", p.roles["manager"]}, {"scorer", p.roles["scorer"]}, {"approver", p.roles["approver"]}, {"enrolled", p.enrolled}} {
		for _, e := range g.in {
			if _, ok := roster[e]; !ok {
				return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s %s is not a member of this team", g.what, e))
			}
		}
	}
	return nil
}

func (p *program) writeChildren(ctx context.Context, tx pgx.Tx, team, training string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM program_roles WHERE team = $1 AND training = $2`, team, training); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM enrollments WHERE team = $1 AND training = $2`, team, training); err != nil {
		return err
	}
	for role, es := range p.roles {
		for _, e := range es {
			if _, err := tx.Exec(ctx, `INSERT INTO program_roles (team, training, email, role) VALUES ($1,$2,$3,$4)`, team, training, e, role); err != nil {
				return err
			}
		}
	}
	for _, e := range p.enrolled {
		if _, err := tx.Exec(ctx, `INSERT INTO enrollments (team, training, email) VALUES ($1,$2,$3)`, team, training, e); err != nil {
			return err
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

// Enroll creates the program. Only roles named in the body are stored. An empty list means the spec default (the
// leader manages and approves, the seniors score), resolved at read time against the team as it is then, as in
// config.Load, so a new leader or a promoted senior is followed without touching the program.
func (s *Store) Enroll(ctx context.Context, actor, team, training string, b ProgramBody) error {
	p, err := b.validate()
	if err != nil {
		return err
	}
	detail := map[string]any{}
	return s.inTx(ctx, actor, "program.enroll", team+"/"+training, detail, func(tx pgx.Tx) error {
		roster, err := teamRoster(ctx, tx, team)
		if err != nil {
			return err
		}
		if err := p.checkMembers(roster); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO programs (team, training, schedule_name, inline_schedule, ttl, idle_timeout,
			max_extension, budget_usd_month, review_self_reported) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			team, training, p.schedule, p.inline, p.durs[0], p.durs[1], p.durs[2], p.budget, p.review)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return apperr.Wrap(apperr.Conflict, fmt.Sprintf("team %s is already enrolled in %s", team, training))
		}
		if err != nil {
			return fkError(err, team, training, p.schedule)
		}
		if err := p.writeChildren(ctx, tx, team, training); err != nil {
			return err
		}
		for k, v := range p.detail(1) { // audit.Log runs after fn
			detail[k] = v
		}
		return nil
	})
}

// SetProgram replaces a program's roles, enrollments, schedule, lab defaults and budget. The pin is not touched.
func (s *Store) SetProgram(ctx context.Context, actor, team, training string, b ProgramBody) error {
	p, err := b.validate()
	if err != nil {
		return err
	}
	return s.inTx(ctx, actor, "program.update", team+"/"+training, p.detail(b.Version+1), func(tx pgx.Tx) error {
		roster, err := teamRoster(ctx, tx, team)
		if err != nil {
			return err
		}
		if err := p.checkMembers(roster); err != nil {
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
			return apperr.Wrap(apperr.Conflict, "someone changed this program, reload")
		}
		return p.writeChildren(ctx, tx, team, training)
	})
}

// SetPin moves the commit trainees' attempts run at; an empty sha tracks the branch head again. The audit row holds
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

// DeleteProgram removes the enrollment, roles and trainees (cascade). Progress, submissions, badges and ranks carry
// plain team/training text with no foreign key, so a trainee's history stays.
func (s *Store) DeleteProgram(ctx context.Context, actor, team, training string) error {
	detail := map[string]any{}
	return s.inTx(ctx, actor, "program.delete", team+"/"+training, detail, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT email FROM enrollments WHERE team = $1 AND training = $2 ORDER BY email`, team, training)
		if err != nil {
			return err
		}
		enrolled, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		var pin *string
		err = tx.QueryRow(ctx, `DELETE FROM programs WHERE team = $1 AND training = $2 RETURNING pinned_ref`, team, training).Scan(&pin)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("team %s has no program %s", team, training))
		}
		if err != nil {
			return err
		}
		detail["enrolled"], detail["pinned_sha"] = enrolled, pin
		return nil
	})
}
