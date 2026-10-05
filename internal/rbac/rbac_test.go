package rbac

import (
	"testing"

	"crucible/internal/config"
)

func TestMatrix(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	c := Checker{P: p}
	const (
		admin   = "admin@crucible.local"
		leader  = "leader@crucible.local"
		senior  = "senior@crucible.local"
		trainee = "trainee@crucible.local"
		other   = "stranger@crucible.local"
	)
	cases := []struct {
		actor   string
		action  Action
		subject string
		want    bool
	}{
		{admin, EditTeam, "", true},
		{trainee, TakeTraining, "", true},
		{leader, TakeTraining, "", false}, // not enrolled
		{other, TakeTraining, "", false},
		{trainee, ViewProgress, trainee, true},
		{trainee, ViewProgress, senior, false},
		{senior, ViewProgress, trainee, true}, // senior + mentor
		{leader, ManageProgram, "", true},
		{senior, ManageProgram, "", false},
		{senior, Score, trainee, true},
		{senior, Score, senior, false}, // never score yourself
		{leader, ApproveLabs, trainee, true},
		{leader, ApproveLabs, leader, false}, // never approve your own request
		{trainee, ApproveLabs, trainee, false},
		{leader, ViewSpend, "", true},
		{trainee, ViewSpend, "", false},
		{leader, EditTeam, "", true},
		{senior, EditTeam, "", false},
	}
	for _, tc := range cases {
		if got := c.Can(tc.actor, tc.action, "forge", "forge-101", tc.subject); got != tc.want {
			t.Errorf("%s %v subject=%s: got %v want %v", tc.actor, tc.action, tc.subject, got, tc.want)
		}
	}
	if n := len(c.Enrollments("TRAINEE@crucible.local")); n != 1 {
		t.Errorf("enrollments = %d", n)
	}
}
