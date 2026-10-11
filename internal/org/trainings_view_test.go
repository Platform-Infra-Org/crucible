package org

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"crucible/internal/config"
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

func ids(v TrainingsView) []string {
	var out []string
	for _, t := range v.Trainings {
		out = append(out, t.ID)
	}
	return out
}

// The trainings page lists what the caller works on: everything for an admin; what their team owns for its leaders,
// seniors and juniors; what they are an editor or admin of. Only managers see where the content comes from.
func TestTrainingsPageShowsWhatEachCallerWorksOn(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if err := f.s.CreateTraining(ctx, "admin@x", "forge-201", config.TrainingRef{Repo: "https://bot:s3cret@git.example.com/f.git"}, "other"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetProgram(ctx, "boss@x", "other", "forge-201", ProgramBody{Version: 1, BudgetUSDMonth: usd(0), Roles: config.Access{Editors: []string{"ds@x"}}}); err != nil {
		t.Fatal(err)
	}
	f.reload()
	for who, want := range map[string][]string{"admin@x": {"forge-101", "forge-201"}, "lead@x": {"forge-101"}, "senior@x": {"forge-101"},
		"boss@x": {"forge-201"}, "ds@x": {"forge-201"}} {
		v, code := f.trainingsPage(who)
		if code != 200 || !slices.Equal(ids(v), want) {
			t.Errorf("%s: %d %v, want %v", who, code, ids(v), want)
			continue
		}
		for _, tr := range v.Trainings {
			if (tr.Repo != "") != tr.CanManage {
				t.Errorf("%s sees repo %q for %s; only managers see the source", who, tr.Repo, tr.ID)
			}
		}
	}
	admin, _ := f.trainingsPage("admin@x")
	if strings.Contains(admin.Trainings[1].Repo, "s3cret") || !strings.Contains(admin.Trainings[1].Repo, "***@") {
		t.Errorf("credentials in a repo URL must be masked: %q", admin.Trainings[1].Repo)
	}
	if len(admin.Teams) != 3 || !admin.IsAdmin {
		t.Errorf("an admin picks among every team: %+v", admin.Teams)
	}
	lead, _ := f.trainingsPage("lead@x")
	if tr := lead.Trainings[0]; tr.Team != "platform" || !tr.CanManage || tr.Program == nil || !tr.Program.CanManage || !lead.CanCreate || lead.MyTeam != "platform" {
		t.Errorf("the leader manages their team's training: %+v", lead)
	}
	editor, _ := f.trainingsPage("ds@x")
	if tr := editor.Trainings[0]; tr.CanManage || tr.Program.CanManage || tr.Repo != "" {
		t.Errorf("an editor reads but does not manage: %+v", tr)
	}
	if v, code := f.trainingsPage("new@x"); code != 403 {
		t.Errorf("a trainee who works on nothing = %d %+v, want 403", code, v)
	}
	if _, code := f.trainingsPage(""); code != 401 {
		t.Errorf("signed out = %d, want 401", code)
	}
}
