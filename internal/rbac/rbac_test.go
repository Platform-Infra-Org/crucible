package rbac

import (
	"slices"
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
		{senior, ViewProgress, trainee, true}, // a senior of the trainee's team
		{leader, ManageProgram, "", true},
		{senior, ManageProgram, "", true}, // the owning team's seniors and juniors manage its trainings too
		{trainee, ManageProgram, "", false},
		{senior, Score, trainee, true},
		{senior, Score, senior, false}, // never score yourself
		{admin, Score, admin, false},   // admins can't score themselves either
		{leader, ApproveLabs, trainee, true},
		{leader, ApproveLabs, leader, false}, // never approve your own request
		{admin, ApproveLabs, admin, false},   // admins can't approve themselves either
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

	// Test unknown training (should not panic, should return false)
	if got := c.Can(leader, TakeTraining, "forge", "nope", ""); got != false {
		t.Errorf("unknown training: got %v want false", got)
	}

	// Test unknown team (should not panic, should return false)
	if got := c.Can(leader, TakeTraining, "nope", "forge-101", ""); got != false {
		t.Errorf("unknown team: got %v want false", got)
	}

	if n := len(c.Enrollments("TRAINEE@crucible.local")); n != 2 { // forge-101 and forge-103
		t.Errorf("enrollments = %d", n)
	}
}

func TestTierRouting(t *testing.T) {
	tiers := config.CostTiers{AutoApproveUSD: 0, Tier1USD: 5, Tier2USD: 25}
	cases := []struct {
		runtime string
		est     float64
		want    string
	}{
		{"local", 0, TierAuto}, {"cluster", 0, TierAuto}, {"aws", 0, TierApprover}, {"local", 0.5, TierApprover},
		{"cluster", 5, TierApprover}, {"cluster", 5.01, TierLeader}, {"aws", 25, TierLeader}, {"aws", 25.5, TierAdmin},
	}
	for _, c := range cases {
		if got := Tier(c.runtime, c.est, tiers); got != c.want {
			t.Errorf("Tier(%s, %v) = %s, want %s", c.runtime, c.est, got, c.want)
		}
	}
	if NextTier(TierApprover) != TierLeader || NextTier(TierLeader) != TierAdmin || NextTier(TierAdmin) != "" {
		t.Error("escalation ladder: approver → leader → admin → none")
	}
}

func TestApprovalRules(t *testing.T) {
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
	)
	may := func(actor, requester string, est float64, overCap bool) bool {
		return c.MayApprove(actor, requester, "forge", "forge-101", est, overCap)
	}
	switch {
	case !may(leader, trainee, 4, false):
		t.Error("the leader (default approver) approves tier 1")
	case !may(leader, trainee, 25, false):
		t.Error("the leader approves up to tier 2")
	case may(leader, trainee, 26, false):
		t.Error("above tier 2 needs an admin")
	case may(leader, "LEADER@crucible.local", 1, false):
		t.Error("nobody approves their own request")
	case may(senior, trainee, 1, false):
		t.Error("a senior who is not an approver cannot approve")
	case may(leader, trainee, 1, true):
		t.Error("over-cap requests are admin-only")
	case !may(admin, trainee, 1000, true):
		t.Error("admins approve anything, including over-cap overrides")
	case may(admin, admin, 1, false):
		t.Error("admins cannot approve their own request either")
	}
	if got := c.Route(TierApprover, "forge", "forge-101", leader); got != TierAdmin {
		t.Errorf("leader's own request skips the approver and leader tiers: %s", got)
	}
	if got := c.Route(TierApprover, "forge", "forge-101", trainee); got != TierApprover {
		t.Errorf("trainee's request starts with the approver: %s", got)
	}
	if got := c.TierApprovers(TierAdmin, "forge", "forge-101", admin); len(got) != 0 {
		t.Errorf("the requester is never listed: %v", got)
	}
	p.Teams["forge"].Programs["forge-101"].Roles.Approvers = []string{senior}
	if !may(senior, trainee, 5, false) || may(senior, trainee, 5.5, false) {
		t.Error("a program approver approves up to tier 1 only")
	}
}
func TestSpendTeams(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	c := Checker{P: p}
	if got := c.SpendTeams("admin@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("admins see every team: %v", got)
	}
	if got := c.SpendTeams("leader@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("leaders see their team: %v", got)
	}
	if got := c.SpendTeams("trainee@crucible.local"); len(got) != 0 {
		t.Fatalf("trainees see no spend: %v", got)
	}
	p.Teams["forge"].Programs["forge-101"].Roles.Approvers = []string{"senior@crucible.local"}
	if got := c.SpendTeams("senior@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("a program approver sees the team's spend (spec §5.3): %v", got)
	}
}

// The model of 2026-10-11: a training belongs to one team, whose leaders, seniors and juniors manage it; its roles go
// to people and groups from any team.
func TestTeamsAndTrainingRoles(t *testing.T) {
	p := &config.Platform{Teams: map[string]*config.Team{
		"platform": {ID: "platform", Leaders: []string{"pl@x", "pl2@x"}, Seniors: []string{"ps@x"}, Juniors: []string{"pj@x"}, Trainees: []string{"pt@x"}},
		"data":     {ID: "data", Leaders: []string{"dl@x"}, Seniors: []string{"ds@x"}, Trainees: []string{"dt@x"}, Programs: map[string]*config.Program{}},
	}}
	p.Teams["platform"].Programs = map[string]*config.Program{"k8s": {Training: "k8s", Access: config.Access{
		Participants: []string{"all_trainees"}, Editors: []string{"data_seniors"}, Admins: []string{"dl@x"}}}}
	p.Settings.CostTiers = &config.CostTiers{Tier1USD: 5, Tier2USD: 25}
	p.Resolve()
	c := Checker{P: p}
	can := func(actor string, a Action, subject string) bool { return c.Can(actor, a, "platform", "k8s", subject) }
	for _, tc := range []struct {
		actor   string
		action  Action
		subject string
		want    bool
	}{
		{"pj@x", ManageProgram, "", true},  // a junior of the owning team
		{"pt@x", ManageProgram, "", false}, // never a trainee
		{"dl@x", ManageProgram, "", true},  // the training's admin, from another team
		{"ds@x", ManageProgram, "", false}, // an editor does not manage
		{"dt@x", TakeTraining, "", true},   // all_trainees reaches every team
		{"pt@x", TakeTraining, "", true},
		{"ps@x", TakeTraining, "", false},
		{"ds@x", Score, "dt@x", true},  // editors score
		{"dl@x", Score, "pt@x", true},  // admins score
		{"pj@x", Score, "pt@x", false}, // managing is not scoring
		{"dl@x", ViewProgress, "dt@x", true},
		{"pj@x", ViewProgress, "dt@x", true}, // the owning team follows its training's participants
		{"pl2@x", ApproveLabs, "dt@x", true}, // any of several leaders
		{"dl@x", ApproveLabs, "pt@x", true},  // training admins approve (tier 1)
	} {
		if got := can(tc.actor, tc.action, tc.subject); got != tc.want {
			t.Errorf("%s %v subject=%s: got %v want %v", tc.actor, tc.action, tc.subject, got, tc.want)
		}
	}
	// A team's leaders and seniors follow their own people in trainings other teams own; its juniors don't.
	p.Teams["data"].Programs["etl"] = &config.Program{Training: "etl", Access: config.Access{Participants: []string{"pt@x"}}}
	p.Resolve()
	for actor, want := range map[string]bool{"pl@x": true, "ps@x": true, "pj@x": false, "dt@x": false} {
		if got := c.Can(actor, ViewProgress, "data", "etl", "pt@x"); got != want {
			t.Errorf("%s follows pt@x in data's etl: got %v want %v", actor, got, want)
		}
	}
	if !c.MayApprove("pl2@x", "pt@x", "platform", "k8s", 20, false) || c.MayApprove("dl@x", "pt@x", "platform", "k8s", 20, false) {
		t.Error("leaders approve up to tier 2, training admins up to tier 1")
	}
	for who, want := range map[string]bool{"pl@x": true, "ps@x": true, "pj@x": true, "pt@x": false, "nobody@x": false} {
		if got := c.CanCreateTraining(who); got != want {
			t.Errorf("CanCreateTraining(%s) = %v", who, got)
		}
	}
	if got := c.TierApprovers(TierLeader, "platform", "k8s", "pl@x"); !slices.Equal(got, []string{"pl2@x"}) {
		t.Errorf("the leader tier is every leader but the requester: %v", got)
	}
}
