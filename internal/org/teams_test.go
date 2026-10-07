package org

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/db/dbtest"
)

func TestCreateTeamStoresLeaderAndRoster(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"senior@x"}, Trainees: []string{"new@x"},
		Mentors: map[string]string{"new@x": "senior@x"}}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	p, _ := s.Platform(ctx)
	team := p.Teams["platform"]
	if team == nil || team.Leader != "lead@x" || team.Name != "Platform" {
		t.Fatalf("team = %+v", team)
	}
	if team.Mentors["new@x"] != "senior@x" {
		t.Errorf("mentors = %v", team.Mentors)
	}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); !errors.Is(err, apperr.Conflict) {
		t.Errorf("duplicate id = %v, want Conflict", err)
	}
}

func TestTeamIDsAreSlugs(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	for _, id := range []string{"", "../etc", "Platform Team", "a/b", `a\b`, strings.Repeat("x", 100)} {
		if err := s.CreateTeam(context.Background(), "admin@x", id, TeamBody{Name: "x", Leader: "l@x"}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("id %q must be refused: ids appear in URLs and audit targets (%v)", id, err)
		}
	}
}

func TestEmailsAreNormalisedToOnePerson(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := TeamBody{Name: "Platform", Leader: "lead@x", Trainees: []string{"  Alice@Corp.COM ", "alice@corp.com"}}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	p, _ := s.Platform(ctx)
	if got := p.Teams["platform"].Trainees; len(got) != 1 || got[0] != "alice@corp.com" {
		t.Errorf("trainees = %v, want one lowercased address", got)
	}
}

func TestOneRolePerPersonPerTeam(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	b := TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"Dual@x"}, Trainees: []string{"dual@x"}}
	err := s.CreateTeam(context.Background(), "admin@x", "platform", b)
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "dual@x") {
		t.Errorf("same person in two roles = %v, want Invalid naming them", err)
	}
}

func TestTeamNeedsALeaderAndValidAddresses(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for _, b := range []TeamBody{{Name: "x"}, {Name: "x", Leader: "l@x", Members: []string{"nope"}}, {Leader: "l@x"}} {
		if err := s.CreateTeam(ctx, "a@x", "t", b); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%+v = %v, want Invalid", b, err)
		}
	}
}

func TestStaleTeamVersionIsRefused(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "admin@x", "platform", TeamBody{Name: "Platform", Leader: "lead@x"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	v := p.Teams["platform"].Version
	first := TeamBody{Version: v, Name: "Platform", Leader: "lead@x", Trainees: []string{"a@x"}}
	second := TeamBody{Version: v, Name: "Platform", Leader: "lead@x", Trainees: []string{"b@x"}}
	if err := s.SetTeam(ctx, "admin@x", "platform", first); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeam(ctx, "other@x", "platform", second); !errors.Is(err, apperr.Conflict) {
		t.Errorf("second writer = %v, want Conflict", err)
	}
	if p, _ = s.Platform(ctx); len(p.Teams["platform"].Trainees) != 1 || p.Teams["platform"].Trainees[0] != "a@x" {
		t.Error("the refused write must have changed nothing")
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'team.roster'`).Scan(&n)
	if n != 1 {
		t.Errorf("team.roster audit rows = %d, want 1 (the refused write leaves none)", n)
	}
	if err := s.SetTeam(ctx, "a@x", "ghost", first); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown team = %v, want NotFound", err)
	}
}

func TestMentorMustBeInTheTeamAndNotTheTraineeThemselves(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	base := func(m map[string]string) TeamBody {
		return TeamBody{Name: "P", Leader: "lead@x", Seniors: []string{"s@x"}, Trainees: []string{"t@x"}, Mentors: m}
	}
	for name, m := range map[string]map[string]string{
		"unknown mentor":  {"t@x": "stranger@x"},
		"unknown trainee": {"stranger@x": "s@x"},
		"self":            {"t@x": "T@x"},
	} {
		if err := s.CreateTeam(ctx, "a@x", "p", base(m)); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s = %v, want Invalid", name, err)
		}
	}
	if err := s.CreateTeam(ctx, "a@x", "p", base(map[string]string{"T@x": "lead@x"})); err != nil {
		t.Errorf("the leader may mentor: %v", err)
	}
}

func TestTeamWritesAreAudited(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := TeamBody{Name: "P", Leader: "lead@x", Trainees: []string{"new@x"}}
	if err := s.CreateTeam(ctx, "Boss@x", "p", b); err != nil {
		t.Fatal(err)
	}
	b.Version, b.Trainees = 1, []string{"other@x"}
	if err := s.SetTeam(ctx, "boss@x", "p", b); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, "boss@x", "p"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, "boss@x", "p"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("second delete = %v, want NotFound", err)
	}
	for _, c := range []struct{ action, want string }{{"team.create", "new@x"}, {"team.roster", "other@x"}, {"team.delete", "other@x"}} {
		var actor, detail string
		err := s.DB.QueryRow(context.Background(), `SELECT actor, detail::text FROM audit_log WHERE action = $1 AND target = 'p'`, c.action).Scan(&actor, &detail)
		if err != nil || actor != "boss@x" || !strings.Contains(detail, c.want) {
			t.Errorf("%s: actor=%q detail=%s err=%v", c.action, actor, detail, err)
		}
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'team.delete'`).Scan(&n)
	if n != 1 {
		t.Errorf("team.delete rows = %d, want 1", n)
	}
}

func TestDeleteTeamKeepsLearningHistory(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "a@x", "p", TeamBody{Name: "P", Leader: "lead@x", Trainees: []string{"t@x"}}); err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO users (sub, email) VALUES ('sub-t', 't@x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status) VALUES ($1, 'p', 'tr', 'm', 'i', 'complete')`, uid); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, "a@x", "p"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM item_progress WHERE team = 'p'`).Scan(&n)
	if n != 1 {
		t.Errorf("progress rows = %d, want 1: deleting a team must not delete learning history", n)
	}
	var rows int
	s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM team_members) + (SELECT count(*) FROM mentors)`).Scan(&rows)
	if rows != 0 {
		t.Errorf("roster rows left behind = %d", rows)
	}
}

func TestTeamWebhooks(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "a@x", "p", TeamBody{Name: "P", Leader: "lead@x"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ kind, url string }{{"slack", "http://x"}, {"discord", "https://x"}} {
		if err := s.SetTeamWebhook(ctx, "a@x", "p", c.kind, c.url); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%+v = %v, want Invalid", c, err)
		}
	}
	if err := s.SetTeamWebhook(ctx, "a@x", "ghost", "slack", "https://hooks/x"); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown team = %v, want NotFound", err)
	}
	if err := s.SetTeamWebhook(ctx, "a@x", "p", "slack", "https://hooks/x"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Platform(ctx); p.Teams["p"].Notifications.SlackWebhook != "https://hooks/x" {
		t.Errorf("notifications = %+v", p.Teams["p"].Notifications)
	}
	var leaked int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE detail::text LIKE '%hooks/x%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("the webhook URL is a secret and must not be audited")
	}
	if err := s.SetTeamWebhook(ctx, "a@x", "p", "slack", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Platform(ctx); p.Teams["p"].Notifications.SlackWebhook != "" {
		t.Error("empty url must clear the webhook")
	}
}
