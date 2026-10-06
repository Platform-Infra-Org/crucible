package labs

import (
	"context"
	"errors"
	"math"
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

func TestOverCapHandOffAnnouncesToAdminAndRestartsTheClock(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.plat.Teams["forge"].Programs["forge-101"].BudgetUSDMonth = 10
	f.rates["first-heat"] = 2
	v := f.request(t, f.u) // escalates (leader to admin) 4 h from now
	f.clk.Add(3 * time.Hour)
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 9)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatal(err)
	}
	if ev := f.notes.last(notify.LabPending); ev == nil || !slices.Contains(ev.To, "admin@crucible.local") {
		t.Fatalf("admins told: %+v", ev)
	}
	f.clk.Add(90 * time.Minute) // past the original deadline
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != PendingApproval || got.Tier != rbac.TierAdmin {
		t.Fatalf("must not expire at the old deadline: %+v", got)
	}
}

func TestKillSwitchPausesEscalation(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 2
	v := f.request(t, f.u)
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("decide while paused: %v", err)
	}
	f.clk.Add(10 * time.Hour)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != PendingApproval || got.Tier != rbac.TierApprover {
		t.Fatalf("paused: %+v", got)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, false); err != nil {
		t.Fatal(err)
	}
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != PendingApproval || got.Tier != rbac.TierApprover {
		t.Fatalf("resumed without instantly escalating: %+v", got)
	}
	f.clk.Add(5 * time.Hour)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.Tier == rbac.TierApprover {
		t.Fatalf("escalates after the remaining time: %+v", got)
	}
}

func TestBudgetAlertsFireAgainNextMonth(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 170)
	_ = f.s.CheckBudgets(ctx)
	n := len(f.notes.events)
	f.clk.Set(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 170)
	_ = f.s.CheckBudgets(ctx)
	if len(f.notes.events) == n {
		t.Fatal("a new month alerts again")
	}
}

func TestRecentApprovalDoesNotBypassTheCap(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 2
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 249)
	_, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at, last_activity_at,
		ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, decided_by, decided_at)
		VALUES ('bbbbbbbbbbb3', $1, 'forge', 'forge-101', '02-first-lab', 'abc', 'local', 'failed', $2, $2, 3600, 1800, 300, 0, 'leader@crucible.local', $2)`,
		f.u.ID, f.clk.Now().Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if v := f.request(t, f.u); v.Tier != rbac.TierAdmin || !v.OverCap {
		t.Fatalf("%+v", v)
	}
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// endedAgo moves a lab (inserted by f.spent: one hour of running) so that it ended d ago.
func (f *fx) endedAgo(t *testing.T, id string, d time.Duration) {
	t.Helper()
	end := f.clk.Now().Add(-d)
	if _, err := f.s.DB.Exec(context.Background(), `UPDATE lab_instances SET ready_at = $2::timestamptz - interval '1 hour',
		destroyed_at = $2 WHERE id = $1`, id, end); err != nil {
		t.Fatal(err)
	}
}

func TestSpendUsesSettledActualsOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "aaaaaaaaaaa1", "forge-101", 2) // ended 3 days ago, Cost Explorer says $0.40
	f.spent(t, "aaaaaaaaaaa2", "forge-101", 2) // ended an hour ago, Cost Explorer says $5 so far (not settled)
	f.spent(t, "aaaaaaaaaaa3", "forge-101", 2) // ended 3 days ago, never reported (tags not activated?)
	f.endedAgo(t, "aaaaaaaaaaa1", 72*time.Hour)
	f.endedAgo(t, "aaaaaaaaaaa3", 72*time.Hour)
	for id, usd := range map[string]float64{"aaaaaaaaaaa1": 0.4, "aaaaaaaaaaa2": 5} {
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ($1, $2::timestamptz::date, $3, $2)`,
			id, f.clk.Now(), usd); err != nil {
			t.Fatal(err)
		}
	}
	sp, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil || !closeTo(sp.SpentUSD, 6) || sp.ActualUSD != 0 {
		t.Fatalf("no successful ingestion yet: estimates only, got %+v %v", sp, err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	sp, _ = f.s.spend(ctx, f.plat, "forge", "")
	if !closeTo(sp.SpentUSD, 4.4) || !closeTo(sp.ActualUSD, 0.4) || !closeTo(sp.CommittedUSD, 4.4) {
		t.Fatalf("only the settled, reported lab uses its actual ($0.40 + $2 + $2): %+v", sp)
	}
}

func TestBudgetCapSetsTheTimer(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 12 // 1 h TTL → $12: the team leader's tier
	v := f.request(t, f.u)
	// Approval re-checks the cap, so the squeeze happens while the lab provisions: another lab books $240 of the
	// team's $250 hard cap.
	f.run.provision = func() { f.spent(t, "bbbbbbbbbbb9", "forge-101", 240) }
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, v.ID, Ready)
	inst, _ := f.s.owned(ctx, f.u, v.ID)
	if got.LimitReason != "budget" || !inst.EndsAt.Equal(inst.ReadyAt.Add(50*time.Minute)) || got.CanExtend {
		t.Fatalf("$10 of headroom at $12/h is 50 minutes, ending at the cap, no extension: %+v ends %v", got, inst.EndsAt)
	}
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "budget cap") {
		t.Fatalf("extending a budget-limited lab: %v", err)
	}
	inst.OverCap = true
	if lim, _ := f.s.budgetLimit(ctx, inst, f.clk.Now()); !lim.At.IsZero() {
		t.Fatal("a lab an admin approved over the cap has no budget limit")
	}
	inst.OverCap, inst.HourlyUSD = false, 0
	if lim, _ := f.s.budgetLimit(ctx, inst, f.clk.Now()); !lim.At.IsZero() {
		t.Fatal("a free lab has no budget limit")
	}
}

// squeeze starts a $12/h lab whose approval races another lab booking usd of the team's $250 cap.
func (f *fx) squeeze(t *testing.T, usd float64) *View {
	t.Helper()
	f.rates["first-heat"] = 12
	v := f.request(t, f.u)
	f.run.provision = func() { f.spent(t, "bbbbbbbbbbb9", "forge-101", usd) }
	if _, err := f.s.Decide(context.Background(), f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSettleRuleNeedsPositiveFreshActuals(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "aaaaaaaaaaa1", "forge-101", 2) // Cost Explorer says $0 so far
	f.spent(t, "aaaaaaaaaaa2", "forge-101", 2) // $0.40, but the row was last read 1 day after the lab ended
	f.endedAgo(t, "aaaaaaaaaaa1", 72*time.Hour)
	f.endedAgo(t, "aaaaaaaaaaa2", 72*time.Hour)
	for id, row := range map[string][2]any{"aaaaaaaaaaa1": {0.0, 0}, "aaaaaaaaaaa2": {0.4, 48}} {
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at)
			SELECT $1, destroyed_at::date, $2, destroyed_at + $3 * interval '1 hour' FROM lab_instances WHERE id = $1`,
			id, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	// lab 2's row: destroyed 72h ago, updated at +48h is fine, so make it stale instead
	if _, err := f.s.DB.Exec(ctx, `UPDATE cost_actuals SET updated_at = updated_at - interval '1 day' WHERE lab_id = 'aaaaaaaaaaa2'`); err != nil {
		t.Fatal(err)
	}
	sp, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil || !closeTo(sp.SpentUSD, 4) || sp.ActualUSD != 0 {
		t.Fatalf("a $0 row and a stale row keep the estimates ($2 + $2): %+v %v", sp, err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE cost_actuals SET updated_at = updated_at + interval '1 day' WHERE lab_id = 'aaaaaaaaaaa2'`); err != nil {
		t.Fatal(err)
	}
	if sp, _ = f.s.spend(ctx, f.plat, "forge", ""); !closeTo(sp.ActualUSD, 0.4) {
		t.Fatalf("a positive row read after the 48 h catch-up settles: %+v", sp)
	}
}

func TestCostActualsRejectsNegative(t *testing.T) {
	f := setup(t, true)
	if _, err := f.s.DB.Exec(context.Background(), `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ('x', '2026-10-01', -1, now())`); err == nil {
		t.Fatal("a negative row must be refused")
	}
}

func TestBudgetLimitMonthBoundary(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb9", "forge-101", 238)
	lab := func(created time.Time) *Instance {
		f.s.DB.Exec(ctx, `DELETE FROM lab_instances WHERE id = 'cccccccccccc'`) //nolint:errcheck
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
			created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
			VALUES ('cccccccccccc', $1, 'forge', 'forge-101', 'old', 'abc', 'local', 'provisioning', $2, $2, 3600, 1800, 300, 0, 12, 12)`,
			f.u.ID, created); err != nil {
			t.Fatal(err)
		}
		return &Instance{ID: "cccccccccccc", Team: "forge", Training: "forge-101", HourlyUSD: 12, EstimateUSD: 12, CreatedAt: created}
	}
	now := f.clk.Now()
	if lim, err := f.s.budgetLimit(ctx, lab(now), now); err != nil || !lim.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("this month: its own $12 is in committed, so it comes back out ($12 headroom = 1 h): %+v %v", lim, err)
	}
	// Created before the 1st it is not in this month's sum: subtracting would overstate headroom ($24 = 2 h).
	if lim, err := f.s.budgetLimit(ctx, lab(monthStart(now).Add(-time.Hour)), now); err != nil || !lim.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("created before the 1st: nothing to subtract: %+v %v", lim, err)
	}
}

func TestBudgetLimitFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb9", "forge-101", 262) // the cap is gone
	inst := &Instance{ID: "cccccccccccc", Team: "forge", Training: "forge-101", HourlyUSD: 12, EstimateUSD: 12, CreatedAt: f.clk.Now()}
	if _, err := f.s.budgetLimit(ctx, inst, f.clk.Now()); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("no headroom refuses: %v", err)
	}
	inst.HourlyUSD = 1e-12                                                  // tiny rate, huge headroom: clamped, no overflow
	f.s.DB.Exec(ctx, `DELETE FROM lab_instances WHERE id = 'bbbbbbbbbbb9'`) //nolint:errcheck
	if lim, err := f.s.budgetLimit(ctx, inst, f.clk.Now()); err != nil || !lim.At.After(f.clk.Now()) {
		t.Fatalf("clamped: %+v %v", lim, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.s.budgetLimit(cctx, inst, f.clk.Now()); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("a spend error fails closed: %v", err)
	}
}

func TestNoHeadroomLabFailsInsteadOfProvisioningDead(t *testing.T) {
	f := setup(t, true)
	v := f.squeeze(t, 250)
	if got := f.waitState(t, f.u, v.ID, Failed); !strings.Contains(got.Error, "budget hard cap") {
		t.Fatalf("refused with the over-cap message: %+v", got)
	}
}

func TestBudgetVersusScheduleAndTTL(t *testing.T) {
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC)) // window closes in 30 min
	v := f.squeeze(t, 248)                                    // $2 headroom at $12/h: 10 min
	if got := f.waitState(t, f.u, v.ID, Ready); got.LimitReason != "budget" {
		t.Fatalf("budget ends before the window: %+v", got)
	}
}

func TestScheduleBeatsALaterBudget(t *testing.T) {
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC))
	v := f.squeeze(t, 241) // $9 headroom: 45 min, after the window
	if got := f.waitState(t, f.u, v.ID, Ready); got.LimitReason != "schedule" {
		t.Fatalf("the window closes first: %+v", got)
	}
}

func TestTTLBeatsALaterBudget(t *testing.T) {
	f := setup(t, true)
	v := f.squeeze(t, 100) // plenty of headroom, TTL is 1 h
	if got := f.waitState(t, f.u, v.ID, Ready); got.LimitReason != "ttl" {
		t.Fatalf("ttl first: %+v", got)
	}
}
