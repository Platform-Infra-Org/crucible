package org

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

// body is a team from "email:tag" pairs.
func body(name string, members ...string) TeamBody {
	b := TeamBody{Name: name, Members: map[string]string{}}
	for _, m := range members {
		e, tag, _ := strings.Cut(m, ":")
		b.Members[e] = tag
	}
	return b
}

func TestCreateTeamStoresTags(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := body("Platform", "lead@x:leader", "lead2@x:leader", "senior@x:senior", "jun@x:junior", "new@x:trainee")
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	p, _ := s.Platform(ctx)
	team := p.Teams["platform"]
	if team == nil || !slices.Equal(team.Leaders, []string{"lead2@x", "lead@x"}) || team.Name != "Platform" ||
		!slices.Equal(team.Juniors, []string{"jun@x"}) || team.RoleOf("new@x") != "trainee" {
		t.Fatalf("team = %+v", team)
	}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); !errors.Is(err, apperr.Conflict) {
		t.Errorf("duplicate id = %v, want Conflict", err)
	}
}

func TestTeamIDsAreSlugs(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	for _, id := range []string{"", "../etc", "Platform Team", "a/b", `a\b`, strings.Repeat("x", 100), "all", "ops_seniors", "x_trainees", "a@b"} {
		if err := s.CreateTeam(context.Background(), "admin@x", id, body("x", "l@x:leader")); !errors.Is(err, apperr.Invalid) {
			t.Errorf("id %q must be refused: ids appear in URLs, audit targets and group refs (%v)", id, err)
		}
	}
}

func TestEmailsAreNormalisedToOnePerson(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := body("Platform", "lead@x:leader", "  Alice@Corp.COM :trainee", "alice@corp.com:trainee")
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	p, _ := s.Platform(ctx)
	if got := p.Teams["platform"].Trainees; len(got) != 1 || got[0] != "alice@corp.com" {
		t.Errorf("trainees = %v, want one lowercased address", got)
	}
}

func TestOneTagPerPerson(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	b := body("Platform", "lead@x:leader", "Dual@x:senior", "dual@x:trainee")
	err := s.CreateTeam(context.Background(), "admin@x", "platform", b)
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "dual@x") {
		t.Errorf("same person with two tags = %v, want Invalid naming them", err)
	}
}

func TestTeamNeedsALeaderAndValidAddresses(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for _, b := range []TeamBody{body("x"), body("x", "s@x:senior"), body("x", "l@x:leader", "nope:junior"),
		body("x", "l@x:leader", "m@x:member"), body("", "l@x:leader")} {
		if err := s.CreateTeam(ctx, "a@x", "t", b); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%+v = %v, want Invalid", b, err)
		}
	}
}

// A person is on one team. An admin who adds them to another moves them: they leave the old team, and the old team's
// version moves on. Taking a team's last leader is refused.
func TestAddingSomeoneMovesThemBetweenTeams(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	a := body("A", "la@x:leader", "mover@x:senior", "ta@x:trainee")
	if err := s.CreateTeam(ctx, "admin@x", "a", a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTeam(ctx, "admin@x", "b", body("B", "lb@x:leader", "mover@x:junior")); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if p.TeamOf("mover@x").ID != "b" || p.Teams["a"].RoleOf("mover@x") != "" || p.Teams["a"].Version != 2 {
		t.Fatalf("mover@x should now be on b only: a=%+v", p.Teams["a"])
	}
	var detail string
	s.DB.QueryRow(ctx, `SELECT detail::text FROM audit_log WHERE action = 'team.create' AND target = 'b'`).Scan(&detail)
	if !strings.Contains(detail, `"moved_from": {"mover@x": "a"}`) {
		t.Errorf("the move is audited: %s", detail)
	}
	err := s.CreateTeam(ctx, "admin@x", "c", body("C", "la@x:leader"))
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "last leader out of team a") {
		t.Fatalf("taking a's only leader = %v", err)
	}
	if p, _ = s.Platform(ctx); p.Teams["c"] != nil || p.TeamOf("la@x").ID != "a" {
		t.Error("the refused move changed nothing")
	}
}

func TestStaleTeamVersionIsRefused(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "admin@x", "platform", body("Platform", "lead@x:leader")); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	v := p.Teams["platform"].Version
	first, second := body("Platform", "lead@x:leader", "a@x:trainee"), body("Platform", "lead@x:leader", "b@x:trainee")
	first.Version, second.Version = v, v
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

func TestTeamWritesAreAudited(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := body("P", "lead@x:leader", "new@x:trainee")
	if err := s.CreateTeam(ctx, "Boss@x", "p", b); err != nil {
		t.Fatal(err)
	}
	b = body("P", "lead@x:leader", "other@x:trainee")
	b.Version = 1
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
	if err := s.CreateTeam(ctx, "a@x", "p", body("P", "lead@x:leader", "t@x:trainee")); err != nil {
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
	s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM team_members)`).Scan(&rows)
	if rows != 0 {
		t.Errorf("roster rows left behind = %d", rows)
	}
}

func TestTeamWebhooks(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "a@x", "p", body("P", "lead@x:leader")); err != nil {
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
	for _, c := range []struct{ team, url string }{{"p", "http://hooks/x"}, {"ghost", "https://hooks/x"}} {
		if err := s.SetTeamWebhook(ctx, "a@x", c.team, "slack", c.url); err == nil || strings.Contains(err.Error(), "hooks/x") {
			t.Errorf("%+v: error %v must exist and must not echo the URL", c, err)
		}
	}
	if err := s.SetTeamWebhook(ctx, "a@x", "p", "teams", ""); !errors.Is(err, apperr.NotFound) {
		t.Errorf("clearing a never-set webhook = %v, want NotFound", err)
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
	var rows int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'team.webhook'`).Scan(&rows)
	if rows != 2 { // the set and the real clear; the failed clear and the refused writes leave none
		t.Errorf("team.webhook audit rows = %d, want 2", rows)
	}
}

// A team that owns trainings can't be deleted: the trainings would lose their run (pin, schedule, budget).
func TestDeleteTeamThatOwnsTrainingsIsRefused(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "a@x", "p", body("P", "lead@x:leader")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTraining(ctx, "lead@x", "k8s", config.TrainingRef{Repo: "https://git/k.git"}, "p", "leader"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeam(ctx, "a@x", "p"); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "k8s") {
		t.Fatalf("deleting a team that owns k8s = %v", err)
	}
}
