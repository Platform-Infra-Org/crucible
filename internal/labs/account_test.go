package labs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/awscloud"
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
