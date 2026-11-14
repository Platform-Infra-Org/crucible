package labs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/awscloud"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

func TestReadAccountSpendStoresTheBillAndKeepsItOnFailure(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.SetAccountSpend(412.37)
	f.s.Cloud = cloud
	if err := f.s.ReadAccountSpend(ctx, false); err != nil {
		t.Fatal(err)
	}
	b, _ := f.s.accountBill(ctx)
	if b.USD != 412.37 || b.ReadAt == nil || !b.ReadAt.Equal(f.clk.Now()) || b.Error != "" {
		t.Fatalf("stored: %+v", b)
	}
	f.clk.Add(time.Hour)
	cloud.FailAccountSpend(errors.New("AccessDenied: budgets:ViewBudget"))
	_ = f.s.ReadAccountSpend(ctx, false)
	b, _ = f.s.accountBill(ctx)
	if b.USD != 412.37 || !b.ReadAt.Equal(f.clk.Now().Add(-time.Hour)) || b.Error != "AccessDenied: budgets:ViewBudget" {
		t.Fatalf("a failure keeps the last figure and records the error: %+v", b)
	}
}

func TestCostExplorerIsReadAtMostHourly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud, f.s.SpendSource = cloud, "cost_explorer"
	_ = f.s.ReadAccountSpend(ctx, false)
	f.clk.Add(50 * time.Minute)
	_ = f.s.ReadAccountSpend(ctx, false)
	f.clk.Add(15 * time.Minute)
	_ = f.s.ReadAccountSpend(ctx, false)
	if cloud.AccountSpendCalls != 2 {
		t.Fatalf("Cost Explorer costs $0.01 a call: read at 0 and 65 min only, got %d", cloud.AccountSpendCalls)
	}
}

func TestManualRefreshIsOncePerFiveMinutesForEveryone(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	ok, wait, err := f.s.claimAccountRefresh(ctx)
	if err != nil || !ok {
		t.Fatalf("the first refresh wins: %v %v", ok, err)
	}
	ok, wait, _ = f.s.claimAccountRefresh(ctx)
	if ok || wait != accountRefreshEvery {
		t.Fatalf("a second one now waits 5 minutes: %v %v", ok, wait)
	}
	f.clk.Add(accountRefreshEvery)
	if ok, _, _ = f.s.claimAccountRefresh(ctx); !ok {
		t.Fatal("after 5 minutes it may run again")
	}
	// Two admins at once: exactly one wins.
	f.clk.Add(accountRefreshEvery)
	wins := make(chan bool, 2)
	for range 2 {
		go func() { ok, _, _ := f.s.claimAccountRefresh(ctx); wins <- ok }()
	}
	if a, b := <-wins, <-wins; a == b {
		t.Fatalf("one of two concurrent refreshes wins: %v %v", a, b)
	}
}

func TestABillIsStaleAfter36HoursOrWhenNeverRead(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	read := now.Add(-35 * time.Hour)
	if (AccountBill{}).Stale(now) == false || (AccountBill{ReadAt: &read}).Stale(now) {
		t.Fatal("never read is stale; 35 h old is not")
	}
	old := now.Add(-37 * time.Hour)
	if !(AccountBill{ReadAt: &old}).Stale(now) {
		t.Fatal("37 h old is stale")
	}
}

func TestNoSpendSourceIsRecordedSoTheLedgerCanSayWhy(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.FailAccountSpend(awscloud.ErrNoSpendSource)
	f.s.Cloud = cloud
	_ = f.s.ReadAccountSpend(ctx, false)
	if b, _ := f.s.accountBill(ctx); b.Error != awscloud.ErrNoSpendSource.Error() || !b.Stale(f.clk.Now()) {
		t.Fatalf("recorded and stale: %+v", b)
	}
}

func TestUnbilledIntegratesTheLast24HoursAfterTheMarks(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	a, b := f.newAWSLab(t), f.newAWSLab(t)
	insertResource(t, f, a.ID, "x", 1.0, now.Add(-30*time.Hour), tp(now.Add(-20*time.Hour))) // 4 h inside the window
	insertResource(t, f, b.ID, "y", 2.0, now.Add(-2*time.Hour), nil)                         // open: 2 h up to now
	w, err := f.s.unbilled(ctx)
	if err != nil || !closeTo(w.USD, 4*1.0+2*2.0) || !w.From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("no marks: the whole window: %v %+v", err, w)
	}
	mark(t, f, "", now.Add(-21*time.Hour)) // the account mark: a's last hour and all of b
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 1*1.0+2*2.0) {
		t.Fatalf("account mark: %v", w.USD)
	}
	mark(t, f, b.ID, now.Add(-1*time.Hour)) // b's own mark overrides it
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 1*1.0+1*2.0) {
		t.Fatalf("own mark: %v", w.USD)
	}
	mark(t, f, "", now.Add(-40*time.Hour)) // older than the window: clamped to its start
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 4*1.0+1*2.0) {
		t.Fatalf("clamped: %v", w.USD)
	}
	for _, l := range w.Labs {
		if l.ID == b.ID && (l.OwnMark == nil || len(l.Intervals) != 1) {
			t.Fatalf("the lab's row carries its mark and intervals: %+v", l)
		}
	}
}

func TestMarksAreForAdminsInTheWindowAndAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	lab := f.newAWSLab(t)
	insertResource(t, f, lab.ID, "x", 1.0, now.Add(-3*time.Hour), nil)
	at := now.Add(-2 * time.Hour)
	if _, err := f.s.SetBilledMark(ctx, f.leader, MarkBody{BilledTo: &at}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("admins only: %v", err)
	}
	for _, bad := range []time.Time{now.Add(-25 * time.Hour), now.Add(3 * time.Minute), now.Add(-24*time.Hour - 3*time.Minute)} {
		if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &bad}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "Pick a time in the last 24 hours") {
			t.Fatalf("%v must be refused: %v", bad, err)
		}
	}
	edge := now.Add(-24 * time.Hour)
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &edge}); err != nil {
		t.Fatalf("the window's edge is accepted: %v", err)
	}
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{LabID: "aaaaaaaaaaaa", BilledTo: &at}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a lab with no usage in the window is refused: %v", err)
	}
	offset := at.In(time.FixedZone("+02", 2*3600))
	blk, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{LabID: lab.ID, BilledTo: &offset})
	if err != nil || blk.Window.Labs[0].OwnMark == nil || !blk.Window.Labs[0].OwnMark.Equal(at) {
		t.Fatalf("an offset is the same instant: %v %+v", err, blk)
	}
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{Reset: true}); err != nil {
		t.Fatal(err)
	}
	var marks, audits int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM billed_marks`).Scan(&marks)
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'account.billed_mark'`).Scan(&audits)
	if marks != 0 || audits != 3 {
		t.Fatalf("reset removes all; every change audited: %d marks, %d audits", marks, audits)
	}
}

func TestConcurrentMarksLastWriteWins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	t1, t2 := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	type res struct {
		blk *AccountBlock
		err error
	}
	done := make(chan res, 2)
	for _, at := range []time.Time{t1, t2} {
		go func() { b, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &at}); done <- res{b, err} }()
	}
	for range 2 {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		if m := r.blk.Window.AccountMark; m == nil || (!m.Equal(t1) && !m.Equal(t2)) {
			t.Fatalf("each reply is the server's state: %v", m)
		}
	}
	var got time.Time
	_ = f.s.DB.QueryRow(ctx, `SELECT billed_to FROM billed_marks WHERE lab_id = ''`).Scan(&got)
	if !got.Equal(t1) && !got.Equal(t2) {
		t.Fatalf("one of the two: %v", got)
	}
	var audits int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'account.billed_mark'`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("both writes audited: %d", audits)
	}
}

func TestMarksNearTheEdgesAreClamped(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	for _, c := range []struct{ in, want time.Time }{
		{now.Add(90 * time.Second), now},
		{now.Add(-24*time.Hour - 90*time.Second), now.Add(-24 * time.Hour)},
	} {
		blk, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &c.in})
		if err != nil || blk.Window.AccountMark == nil || !blk.Window.AccountMark.Equal(c.want.Truncate(time.Microsecond)) {
			t.Fatalf("%v clamps to %v: %v %+v", c.in, c.want, err, blk)
		}
	}
}

func TestOldMarksArePurged(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	now := f.clk.Now()
	mark(t, f, "", now.Add(-49*time.Hour))
	mark(t, f, "bbbbbbbbbbbb", now.Add(-47*time.Hour))
	f.s.Sweep(ctx)
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM billed_marks`).Scan(&n)
	if n != 1 {
		t.Fatalf("marks older than 48 h go: %d left", n)
	}
}

func tp[T any](v T) *T { return &v }

var labSeq int

// newAWSLab inserts a ready aws lab row (its own module, so the one-active-lab index never refuses it).
func (f *fx) newAWSLab(t *testing.T) *Instance {
	t.Helper()
	labSeq++
	id := fmt.Sprintf("%012x", labSeq)
	at := f.clk.Now().Add(-30 * time.Hour)
	_, err := f.s.DB.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ready_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
		VALUES ($1, $2, 'forge', 'forge-401', $3, 'abc', 'aws', 'ready', $4, $4, $4, 3600, 1800, 300, 0, 1, 1)`,
		id, f.u.ID, fmt.Sprintf("m%d", labSeq), at)
	if err != nil {
		t.Fatal(err)
	}
	return &Instance{ID: id}
}

func insertResource(t *testing.T, f *fx, lab, addr string, hourly float64, start time.Time, end *time.Time) {
	t.Helper()
	if _, err := f.s.DB.Exec(context.Background(), `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, stopped_hourly_usd, started_at, ended_at)
		VALUES ($1, $2, 'aws_instance', $3, 0, $3, $4, $5)`, lab, addr, hourly, start, end); err != nil {
		t.Fatal(err)
	}
}

func mark(t *testing.T, f *fx, lab string, at time.Time) {
	t.Helper()
	if _, err := f.s.DB.Exec(context.Background(), `INSERT INTO billed_marks (lab_id, billed_to, set_by, set_at) VALUES ($1, $2, 'admin@x', $3)
		ON CONFLICT (lab_id) DO UPDATE SET billed_to = excluded.billed_to, set_at = excluded.set_at`, lab, at, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestADestroyedLabsOwnMarkKeepsCountingUntilItsUsageLeavesTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	now := f.clk.Now()
	lab := f.newAWSLab(t)
	insertResource(t, f, lab.ID, "x", 1.0, now.Add(-10*time.Hour), tp(now.Add(-4*time.Hour)))
	mark(t, f, lab.ID, now.Add(-6*time.Hour))
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroyed', destroyed_at = $2 WHERE id = $1`, lab.ID, now.Add(-4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w, err := f.s.unbilled(ctx); err != nil || !closeTo(w.USD, 2) {
		t.Fatalf("the destroyed lab counts after its mark: %v %+v", err, w)
	}
	f.clk.Add(21 * time.Hour) // its usage has left the window
	if w, _ := f.s.unbilled(ctx); len(w.Labs) != 0 || w.USD != 0 {
		t.Fatalf("gone from the window: %+v", w)
	}
}

func TestNightStampsReachTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.bedtime(t)
	f.s.Sweep(ctx)
	f.waitState(t, f.u, id, Asleep)
	f.s.Sweep(ctx)
	slept := f.clk.Now()
	f.clk.Add(time.Hour)
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := f.s.unbilled(ctx)
	if err != nil || w.SleptFrom == nil || w.SleptTo == nil || !w.SleptFrom.Equal(slept.Truncate(time.Microsecond)) || !w.SleptTo.Equal(f.clk.Now().Truncate(time.Microsecond)) {
		t.Fatalf("the night: %v %+v", err, w)
	}
}

func setLimit(t *testing.T, f *fx, usd float64) {
	t.Helper()
	f.plat.Settings.AccountMonthlyLimitUSD = usd
}

func TestAccountLimitSendsPaidRequestsToAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 600)
	cloud.SetAccountSpend(590)
	_ = f.s.ReadAccountSpend(ctx, false)
	reason, err := f.s.overCap(ctx, f.plat, "forge", "forge-101", 5)
	if err != nil || reason != "" {
		t.Fatalf("590 + 5 is under 600: %q %v", reason, err)
	}
	if reason, _ = f.s.overCap(ctx, f.plat, "forge", "forge-101", 11); reason != "account" {
		t.Fatalf("590 + 11 passes 600: %q", reason)
	}
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 249) // the team's $250 cap: team first
	if reason, _ = f.s.overCap(ctx, f.plat, "forge", "forge-101", 11); reason != "team" {
		t.Fatalf("past both: the team's cap is named: %q", reason)
	}
}

func TestReservedAndUnbilledCountTowardTheLimit(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 100)
	cloud.SetAccountSpend(80)
	_ = f.s.ReadAccountSpend(ctx, false)
	now := f.clk.Now()
	run := f.newAWSLab(t) // ready, $1/h, 5 h left
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET hourly_usd = 1, ends_at = $2 WHERE id = $1`, run.ID, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	insertResource(t, f, run.ID, "x", 1.0, now.Add(-10*time.Hour), nil) // 10 h unbilled
	tot, err := f.s.accountTotals(ctx, f.plat)
	if err != nil || !closeTo(tot.ReservedUSD, 5) || !closeTo(tot.UnbilledUSD, 10) || !closeTo(tot.TotalUSD(), 95) {
		t.Fatalf("80 billed + 10 unbilled + 5 reserved: %+v %v", tot, err)
	}
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 6); r != "account" {
		t.Fatal("95 + 6 passes 100")
	}
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 4); r != "" {
		t.Fatalf("95 + 4 is under 100: %q", r)
	}
	starting := f.newAWSLab(t) // a starting lab reserves its whole estimate
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'provisioning', estimate_usd = 3 WHERE id = $1`, starting.ID); err != nil {
		t.Fatal(err)
	}
	if tot, _ = f.s.accountTotals(ctx, f.plat); !closeTo(tot.ReservedUSD, 8) {
		t.Fatalf("5 + a starting lab's 3: %+v", tot)
	}
}

func TestAStaleOrMissingBillFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 1000)
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 1); r != "account" {
		t.Fatal("never read: an admin decides")
	}
	_ = f.s.ReadAccountSpend(ctx, false)
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 1); r != "" {
		t.Fatal("fresh and far under")
	}
	f.clk.Add(37 * time.Hour)
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 1); r != "account" {
		t.Fatal("37 h old: an admin decides")
	}
}

func TestNoLimitOrNoAWSMeansNoAccountCheck(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	setLimit(t, f, 1)
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 1); r != "" {
		t.Fatalf("no Cloud: no account bill, no account check: %q", r)
	}
	setLimit(t, f, 0)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now} // never read, but no limit set
	if r, _ := f.s.overCap(ctx, f.plat, "forge", "forge-101", 1); r != "" {
		t.Fatalf("no limit: no account check: %q", r)
	}
}

func TestApprovingAfterTheLimitIsReachedGoesToAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 600)
	cloud.SetAccountSpend(100)
	_ = f.s.ReadAccountSpend(ctx, false)
	f.rates["first-heat"] = 2
	inst := f.request(t, f.u) // waits for the team's approver, under the limit
	if inst.State != PendingApproval || inst.Tier != rbac.TierApprover || inst.OverCap || inst.CapReason != "" {
		t.Fatalf("under the limit: the approver decides: %+v", inst)
	}
	cloud.SetAccountSpend(599)
	_ = f.s.ReadAccountSpend(ctx, false)
	if _, err := f.s.Decide(ctx, f.leader, inst.ID, true, ""); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "account's monthly limit") {
		t.Fatalf("handed to an admin: %v", err)
	}
	var reason, tier string
	_ = f.s.DB.QueryRow(ctx, `SELECT cap_reason, tier FROM lab_instances WHERE id = $1`, inst.ID).Scan(&reason, &tier)
	if reason != "account" || tier != "admin" {
		t.Fatalf("%q %q", reason, tier)
	}
	list, err := f.s.Approvals(ctx, f.admin)
	if err != nil || len(list) != 1 || list[0].CapReason != "account" || !closeTo(list[0].AccountUSD, 599) || list[0].AccountLimitUSD != 600 {
		t.Fatalf("the admin sees why: %+v %v", list, err)
	}
	if st, err := f.s.Decide(ctx, f.admin, inst.ID, true, "checked the bill"); err != nil || st != Provisioning {
		t.Fatalf("an admin approves it as an override: %v %v", st, err)
	}
}

func TestAccountLimitAtRequestTimeSetsTheReason(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 600)
	cloud.SetAccountSpend(599)
	_ = f.s.ReadAccountSpend(ctx, false)
	f.rates["first-heat"] = 2
	v := f.request(t, f.u)
	if v.Tier != rbac.TierAdmin || !v.OverCap || v.CapReason != "account" {
		t.Fatalf("599 + 2 passes 600: %+v", v)
	}
	var reason string
	_ = f.s.DB.QueryRow(ctx, `SELECT cap_reason FROM lab_instances WHERE id = $1`, v.ID).Scan(&reason)
	if reason != "account" {
		t.Fatalf("stored: %q", reason)
	}
}

func TestFreeLabsAreNeverCheckedAgainstTheAccountLimit(t *testing.T) {
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now} // never read: would fail closed
	setLimit(t, f, 1)
	inst := f.request(t, f.u) // a free lab
	if inst.OverCap || inst.CapReason != "" || inst.State != Provisioning {
		t.Fatalf("a free lab never costs money: %+v", inst)
	}
}

func TestAccountAlertsOncePerLevel(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 100)
	cloud.SetAccountSpend(85)
	_ = f.s.ReadAccountSpend(ctx, false)
	_ = f.s.CheckBudgets(ctx)
	_ = f.s.CheckBudgets(ctx)
	if n := f.notes.count(notify.BudgetAlert); n != 1 {
		t.Fatalf("80%%: one email: %d", n)
	}
	if ev := f.notes.last(notify.BudgetAlert); !strings.Contains(ev.Subject, "80%") || !slices.Contains(ev.To, "admin@crucible.local") {
		t.Fatalf("to the admins: %+v", ev)
	}
	cloud.SetAccountSpend(101)
	_ = f.s.ReadAccountSpend(ctx, false)
	_ = f.s.CheckBudgets(ctx)
	f.clk.Add(37 * time.Hour)
	_ = f.s.CheckBudgets(ctx)
	_ = f.s.CheckBudgets(ctx)
	if n := f.notes.count(notify.BudgetAlert); n != 3 {
		t.Fatalf("then 100%% and stale, once each: %d", n)
	}
	if ev := f.notes.last(notify.BudgetAlert); !strings.Contains(ev.Subject, "can't read") {
		t.Fatalf("the stale alert: %+v", ev)
	}
}

// withApproverAndManager adds a program approver and a program manager (not an approver) to the fixture's first program.
func withApproverAndManager(t *testing.T, f *fx) {
	t.Helper()
	ctx := context.Background()
	store := auth.Store{DB: f.s.DB}
	f.approver, _ = store.UpsertUser(ctx, "s5", "approver@crucible.local", "Avi")
	f.manager, _ = store.UpsertUser(ctx, "s6", "manager@crucible.local", "Mo")
	for _, tm := range f.plat.Teams {
		for _, p := range tm.Programs {
			p.Roles.Approvers = append(p.Roles.Approvers, f.approver.Email)
			p.Roles.Manager = append(p.Roles.Manager, f.manager.Email)
			return
		}
	}
	t.Fatal("no program")
}

func TestTheLedgerShowsTheAccountToAdminsAndApproversOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	withApproverAndManager(t, f)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.SetAccountSpend(412.37)
	f.s.Cloud, f.s.SpendSource = cloud, "budget"
	setLimit(t, f, 600)
	_ = f.s.ReadAccountSpend(ctx, false)
	a, err := f.s.Ledger(ctx, f.admin)
	if err != nil || a.Account == nil || a.Account.BilledUSD != 412.37 || a.Account.LimitUSD != 600 || !a.Account.CanMark || a.Account.Source != "budget" {
		t.Fatalf("admin: %+v %v", a.Account, err)
	}
	ap, err := f.s.Ledger(ctx, f.approver)
	if err != nil || ap.Account == nil || ap.Account.CanMark || ap.Account.Error != "" || ap.Account.RefreshAfter != nil {
		t.Fatalf("an approver sees the totals, read-only, without admin detail: %+v %v", ap.Account, err)
	}
	if m, err := f.s.Ledger(ctx, f.manager); err == nil && m.Account != nil {
		t.Fatal("a program manager who doesn't approve doesn't see the account")
	}
}
