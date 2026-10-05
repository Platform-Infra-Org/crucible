package labs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// spent records a finished lab this month that cost usd (1 hour at usd/h) for the trainee in module.
func (f *fx) spent(t *testing.T, id, training string, usd float64) {
	t.Helper()
	ready := f.clk.Now().Add(-2 * time.Hour)
	_, err := f.s.DB.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ready_at, destroyed_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
		VALUES ($1, $2, 'forge', $3, 'old', 'abc', 'local', 'destroyed', $4, $4, $4, $5, 3600, 1800, 300, 0, $6, $6)`,
		id, f.u.ID, training, ready, ready.Add(time.Hour), usd)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHardCapRoutesToAdminAndTheOverrideIsAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 249) // team cap is $250
	f.rates["first-heat"] = 2
	v := f.request(t, f.u)
	if v.Tier != rbac.TierAdmin || !v.OverCap {
		t.Fatalf("$249 + $2 > $250: admin only, got %+v", v)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatal("the leader cannot approve past the cap")
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader override: %v", err)
	}
	if _, err := f.s.Decide(ctx, f.admin, v.ID, true, "training week"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1`, v.ID).Scan(&action)
	if action != "lab.budget_override" {
		t.Fatalf("audited as %q", action)
	}
}

func TestFreeLabNeverBlockedByCap(t *testing.T) {
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 300) // already over the cap
	v := f.request(t, f.u)
	if v.State != Provisioning || v.OverCap {
		t.Fatalf("a $0 lab costs nothing: %+v", v)
	}
	f.waitState(t, f.u, v.ID, Ready)
}

func TestProgramCapAndApprovalTimeRecheck(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.plat.Teams["forge"].Programs["forge-101"].BudgetUSDMonth = 10
	f.rates["first-heat"] = 2
	v := f.request(t, f.u) // $0 spent: within the $10 program cap
	if v.OverCap || v.Tier != rbac.TierApprover {
		t.Fatalf("%+v", v)
	}
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 9) // meanwhile the program spent $9
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("approval re-checks the cap: %v", err)
	}
	if got, _ := f.s.Get(ctx, f.u, v.ID); !got.OverCap || got.Tier != rbac.TierAdmin {
		t.Fatalf("passed to an admin: %+v", got)
	}
}

func TestBudgetAlertsOncePerLevelPerMonth(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 170) // 85% of the $200 team budget
	if err := f.s.CheckBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	ev := f.notes.last(notify.BudgetAlert)
	if ev == nil || !slices.Contains(ev.To, "leader@crucible.local") || !slices.Contains(ev.To, "admin@crucible.local") || ev.Team != "forge" {
		t.Fatalf("80%% alert %+v", ev)
	}
	n := len(f.notes.events)
	_ = f.s.CheckBudgets(ctx)
	if len(f.notes.events) != n {
		t.Fatal("an alert is sent once per level per month")
	}
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 90) // $260 ≥ $250 cap
	_ = f.s.CheckBudgets(ctx)
	if ev := f.notes.last(notify.BudgetAlert); ev == nil || !strings.Contains(ev.Subject, "hard cap") {
		t.Fatalf("100%% alert %+v", ev)
	}
}

func TestKillSwitch(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	if _, err := f.s.SetKillSwitch(ctx, f.u, true); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only admins: %v", err)
	}
	ks, err := f.s.SetKillSwitch(ctx, f.admin, true)
	if err != nil || !ks.Enabled || ks.ChangedBy != "admin@crucible.local" {
		t.Fatalf("%+v %v", ks, err)
	}
	got := f.waitState(t, f.u, v.ID, Destroyed)
	if got.EndReason != "kill_switch" {
		t.Fatalf("end reason %q", got.EndReason)
	}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("requests blocked while paused: %v", err)
	}
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab"); ml.Blocked != "Labs are paused by an admin." {
		t.Fatalf("lobby: %+v", ml)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, false); err != nil {
		t.Fatal(err)
	}
	if n := f.start(t); n.State != Ready {
		t.Fatal("labs run again after re-enabling")
	}
	var actions []string
	rows, _ := f.s.DB.Query(ctx, `SELECT action FROM audit_log ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	if strings.Join(actions, ",") != "kill_switch.on,kill_switch.off" {
		t.Fatalf("audit %v", actions)
	}
}

func TestKillSwitchStopsProvisioningLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	release := make(chan struct{})
	f.run.provision = func() { <-release }
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, v.ID, Destroyed)
	before := f.run.destroyedN()
	close(release)
	for i := 0; i < 200 && f.run.destroyedN() == before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got.EndReason != "kill_switch" || f.run.destroyedN() == before {
		t.Fatalf("late containers must be removed too: %+v", got)
	}
}
