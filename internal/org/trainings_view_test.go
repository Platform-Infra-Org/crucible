package org

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func (f *apiFixture) trainingsPage(user string) (TrainingsView, int) {
	f.t.Helper()
	w := f.do(user, "GET", "/api/org/trainings", "")
	var v TrainingsView
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			f.t.Fatal(err)
		}
	}
	return v, w.Code
}

func teamIDs(v TrainingsView) []string {
	var ids []string
	for _, t := range v.Teams {
		ids = append(ids, t.ID)
	}
	return ids
}

// Everyone sees the trainings; each caller sees only the teams they belong to (an admin sees every team), and only
// an admin sees where the content comes from.
func TestTrainingsPageShowsEachCallerTheirOwnTeams(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if err := f.s.AddTraining(ctx, "admin@x", "forge-201", "https://bot:s3cret@git.example.com/f.git", "main"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enroll(ctx, "admin@x", "other", "forge-201", ProgramBody{BudgetUSDMonth: usd(0)}); err != nil {
		t.Fatal(err)
	}
	f.reload()
	for who, want := range map[string][]string{"admin@x": {"other", "platform"}, "lead@x": {"platform"}, "new@x": {"platform"}, "boss@x": {"other"}} {
		v, code := f.trainingsPage(who)
		if code != 200 || !slices.Equal(teamIDs(v), want) || len(v.Trainings) != 2 {
			t.Errorf("%s: %d teams %v, %d trainings", who, code, teamIDs(v), len(v.Trainings))
			continue
		}
		for _, tr := range v.Trainings {
			if (tr.Repo != "") != (who == "admin@x") {
				t.Errorf("%s sees repo %q for %s; only admins see the source", who, tr.Repo, tr.ID)
			}
			for _, p := range tr.Programs {
				if !slices.Contains(want, p.Team) {
					t.Errorf("%s sees team %s's program in %s", who, p.Team, tr.ID)
				}
			}
		}
	}
	admin, _ := f.trainingsPage("admin@x")
	if strings.Contains(admin.Trainings[1].Repo, "s3cret") || !strings.Contains(admin.Trainings[1].Repo, "***@") {
		t.Errorf("credentials in a repo URL must be masked: %q", admin.Trainings[1].Repo)
	}
	lead, _ := f.trainingsPage("lead@x")
	p := lead.Trainings[0].Programs
	if len(p) != 1 || p[0].Team != "platform" || !p[0].CanManage || !lead.Teams[0].CanEditTeam ||
		!slices.Equal(p[0].EffectiveRoles.Manager, []string{"lead@x"}) || len(p[0].Roles.Manager) != 0 {
		t.Errorf("the leader manages their program; roles show stored (none) and effective (the leader): %+v %+v", p, lead.Teams)
	}
	trainee, _ := f.trainingsPage("new@x")
	if trainee.Trainings[0].Programs[0].CanManage || trainee.Teams[0].CanEditTeam {
		t.Errorf("a trainee reads but cannot manage: %+v", trainee)
	}
	if _, code := f.trainingsPage("nobody@x"); code != 403 {
		t.Errorf("someone on no team = %d, want 403", code)
	}
	if _, code := f.trainingsPage(""); code != 401 {
		t.Errorf("signed out = %d, want 401", code)
	}
}
