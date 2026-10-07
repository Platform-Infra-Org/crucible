package org

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

// TeamBody is what an admin saves. Version is the one the page read; a mismatch means someone else saved first.
type TeamBody struct {
	Version  int64             `json:"version"`
	Name     string            `json:"name"`
	Leader   string            `json:"leader"`
	Seniors  []string          `json:"seniors"`
	Members  []string          `json:"members"`
	Trainees []string          `json:"trainees"`
	Mentors  map[string]string `json:"mentors"`
}

func checkTeamID(id string) error {
	if id == "" || !filepath.IsLocal(id) || strings.ContainsAny(id, "/\\ ") || len(id) > 64 {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("team id %q must be 1-64 characters with no slash or space", id))
	}
	return nil
}

// roster is a validated body: addresses normalised, one role per person, mentors checked.
type roster struct {
	name    string
	roles   map[string]string // email -> role
	mentors map[string]string
}

func (b TeamBody) roster() (*roster, error) {
	if strings.TrimSpace(b.Name) == "" {
		return nil, apperr.Wrap(apperr.Invalid, "a team needs a name")
	}
	r := &roster{name: b.Name, roles: map[string]string{}, mentors: map[string]string{}}
	add := func(role string, emails ...string) error {
		for _, e := range emails {
			e = em(e)
			if err := config.CheckEmail(role, e); err != nil {
				return err
			}
			if prev, ok := r.roles[e]; ok && prev != role {
				return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s is listed as both %s and %s; a person has one role per team", e, prev, role))
			}
			r.roles[e] = role
		}
		return nil
	}
	if em(b.Leader) == "" {
		return nil, apperr.Wrap(apperr.Invalid, "a team needs a leader")
	}
	for _, g := range []struct {
		role   string
		emails []string
	}{{"leader", []string{b.Leader}}, {"senior", b.Seniors}, {"member", b.Members}, {"trainee", b.Trainees}} {
		if err := add(g.role, g.emails...); err != nil {
			return nil, err
		}
	}
	for t, m := range b.Mentors {
		t, m = em(t), em(m)
		if t == m {
			return nil, apperr.Wrap(apperr.Invalid, t+" cannot mentor themselves")
		}
		for _, e := range []string{t, m} {
			if _, ok := r.roles[e]; !ok {
				return nil, apperr.Wrap(apperr.Invalid, e+" is in a mentor pair but is not on this team")
			}
		}
		if prev, ok := r.mentors[t]; ok && prev != m {
			return nil, apperr.Wrap(apperr.Invalid, t+" is given two different mentors")
		}
		r.mentors[t] = m
	}
	return r, nil
}

// detail is the audit record: with no git history, this is the only account of who joined or left.
func (r *roster) detail(version int64) map[string]any {
	by := map[string][]string{}
	for e, role := range r.roles {
		by[role] = append(by[role], e)
	}
	for _, v := range by {
		slices.Sort(v)
	}
	return map[string]any{"version": version, "name": r.name, "leader": by["leader"][0], "seniors": by["senior"],
		"members": by["member"], "trainees": by["trainee"], "mentors": r.mentors}
}

func (r *roster) write(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM team_members WHERE team = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mentors WHERE team = $1`, id); err != nil {
		return err
	}
	for e, role := range r.roles {
		if _, err := tx.Exec(ctx, `INSERT INTO team_members (team, email, role) VALUES ($1, $2, $3)`, id, e, role); err != nil {
			return err
		}
	}
	for t, m := range r.mentors {
		if _, err := tx.Exec(ctx, `INSERT INTO mentors (team, trainee, mentor) VALUES ($1, $2, $3)`, id, t, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateTeam(ctx context.Context, actor, id string, b TeamBody) error {
	if err := checkTeamID(id); err != nil {
		return err
	}
	r, err := b.roster()
	if err != nil {
		return err
	}
	return s.inTx(ctx, actor, "team.create", id, r.detail(1), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO teams (id, name) VALUES ($1, $2)`, id, r.name); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return apperr.Wrap(apperr.Conflict, fmt.Sprintf("team %q already exists", id))
			}
			return err
		}
		return r.write(ctx, tx, id)
	})
}

func (s *Store) SetTeam(ctx context.Context, actor, id string, b TeamBody) error {
	if err := checkTeamID(id); err != nil {
		return err
	}
	r, err := b.roster()
	if err != nil {
		return err
	}
	return s.inTx(ctx, actor, "team.roster", id, r.detail(b.Version+1), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE teams SET name = $2, version = version + 1 WHERE id = $1 AND version = $3`, id, r.name, b.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var one int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM teams WHERE id = $1`, id).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", id))
			}
			return apperr.Wrap(apperr.Conflict, "someone changed this team, reload")
		}
		return r.write(ctx, tx, id)
	})
}

// DeleteTeam removes the team and its roster, mentors, webhooks, budget and programs (cascade). Learning history
// (item_progress, submissions, badges, ranks) has no foreign key to teams and is kept.
func (s *Store) DeleteTeam(ctx context.Context, actor, id string) error {
	detail := map[string]any{"team": id}
	return s.inTx(ctx, actor, "team.delete", id, detail, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT email FROM team_members WHERE team = $1 ORDER BY email`, id)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", id))
		}
		detail["members"] = gone // audit.Log runs after fn, so the map is read after this
		return nil
	})
}

// SetTeamWebhook sets the slack or teams webhook; an empty url clears it. The URL is a secret, so the audit row
// records only the kind and whether it was set.
func (s *Store) SetTeamWebhook(ctx context.Context, actor, team, kind, url string) error {
	if kind != "slack" && kind != "teams" {
		return apperr.Wrap(apperr.Invalid, "webhook kind must be slack or teams")
	}
	url = strings.TrimSpace(url)
	if url != "" && !strings.HasPrefix(url, "https://") {
		return apperr.Wrap(apperr.Invalid, "webhook URLs must start with https://")
	}
	return s.inTx(ctx, actor, "team.webhook", team, map[string]any{"kind": kind, "set": url != ""}, func(tx pgx.Tx) error {
		if url == "" {
			_, err := tx.Exec(ctx, `DELETE FROM team_webhooks WHERE team = $1 AND kind = $2`, team, kind)
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO team_webhooks (team, kind, url) VALUES ($1, $2, $3)
			ON CONFLICT (team, kind) DO UPDATE SET url = EXCLUDED.url`, team, kind, url)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23503" {
			return apperr.Wrap(apperr.NotFound, fmt.Sprintf("no team %q", team))
		}
		return err
	})
}
