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
)

func (f *fx) bedtime(t *testing.T) {
	t.Helper()
	if err := RequestBedtime(context.Background(), f.s.DB, "crucible labs bedtime", f.clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) phase(t *testing.T) string {
	t.Helper()
	b, err := ReadBedtime(context.Background(), f.s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		return ""
	}
	return b.Phase
}

func TestBedtimeClosesThenPutsReadyLabsToSleep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.bedtime(t)
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || ml.Blocked != bankingMsg {
		t.Fatalf("no new lab starts at bedtime: %q %v", ml.Blocked, err)
	}
	f.s.Sweep(ctx) // settling: nothing is starting, so straight to sleeping
	got := f.waitState(t, f.u, id, Asleep)
	if got.SleptAs != "stopped" || got.SleptAt == nil {
		t.Fatalf("how it slept is kept: %+v", got)
	}
	run.mu.Lock()
	slept := slices.Clone(run.slept)
	run.mu.Unlock()
	if !slices.Equal(slept, []string{id}) {
		t.Fatalf("the runner put the lab to sleep once: %v", slept)
	}
	f.s.Sweep(ctx)
	if p := f.phase(t); p != "asleep" {
		t.Fatalf("every aws lab is asleep: phase %q", p)
	}
}

func TestBedtimeCancelsALabStillStartingAfterTenMinutes(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	run.provision = func() { <-hold }
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Provisioning)
	f.bedtime(t)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Provisioning {
		t.Fatalf("a lab still starting gets ten minutes: %s", got.State)
	}
	f.clk.Add(11 * time.Minute)
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, id)
	if (got.State != Destroying && got.State != Destroyed) || got.EndReason != "bedtime" {
		t.Fatalf("after ten minutes it is cancelled: %+v", got)
	}
}

func TestApprovalsWaitForTheMorning(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.bedtime(t)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("an approval would start a lab: refused at bedtime: %v", err)
	}
}

func TestWakeBringsTheLabBack(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.bedtime(t)
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("nothing wakes while the platform is going to sleep: %v", err)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if p := f.phase(t); p != "" {
		t.Fatalf("the morning ends the bedtime: %q", p)
	}
	f.clk.Add(time.Minute)
	if _, err := f.s.Wake(ctx, f.other, id); !errors.Is(err, apperr.NotFound) && !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only its trainee wakes a lab: %v", err)
	}
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, id, Ready)
	if got.WokeAt == nil || !got.WokeAt.Equal(f.clk.Now()) || got.SleptAs != "stopped" {
		t.Fatalf("woke now, and the page knows how it slept: %+v", got)
	}
	if got.IdleDeadline == nil || !got.IdleDeadline.Equal(f.clk.Now().Add(time.Duration(got.IdleTimeoutS)*time.Second)) {
		t.Fatalf("the idle clock starts fresh: %+v", got.IdleDeadline)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if !slices.Equal(run.woke, []string{id}) {
		t.Fatalf("the runner woke it: %v", run.woke)
	}
}

func TestWakeAfterTheDeadlineIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	v := f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.clk.Set(v.EndsAt.Add(time.Minute))
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) || !containsMsg(err, "Time ran out on this lab while it slept.") {
		t.Fatalf("%v", err)
	}
}

func TestAFailedWakeLeavesTheLabAsleepWithItsError(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.wakeErr = errors.New("starting the lab's machines: InsufficientInstanceCapacity")
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Asleep)
	got, _ := f.s.Get(ctx, f.u, id)
	if got.State != Asleep || got.Error == "" {
		t.Fatalf("asleep again, with what went wrong: %+v", got)
	}
}

// A money path: a wake whose machines may still run is never called asleep (nothing revisits an asleep lab before its
// deadline); it stays waking, and the stale-wake sweep puts it back to sleep through the runner.
func TestAWakeThatMayLeaveMachinesRunningStaysWaking(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.wakeErr = fmt.Errorf("starting the workspace: quota exceeded; and putting its machines back to sleep failed: %w", errMachinesMayRun)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	f.settled(t, "wake:"+id)
	got, _ := f.s.Get(ctx, f.u, id)
	if got.State != Waking || got.Error == "" {
		t.Fatalf("machines that may run stay waking, with what went wrong: %+v", got)
	}
	var failed bool
	if err := f.s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'wake_failed')`, id).Scan(&failed); err != nil || !failed {
		t.Fatalf("the failed wake is recorded: %v %v", failed, err)
	}
	f.clk.Add(21 * time.Minute)
	f.s.Sweep(ctx)
	if got := f.waitState(t, f.u, id, Asleep); got.Error != "Waking took too long; press Wake it again." {
		t.Fatalf("the stale wake goes back to sleep: %+v", got)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if !slices.Equal(run.slept, []string{id}) {
		t.Fatalf("its machines are put to sleep through the runner: %v", run.slept)
	}
}

func TestEndWhileWakingWins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.wakeHold = make(chan struct{})
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.End(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	close(run.wakeHold)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != "user" {
		t.Fatalf("the destroy wins over a wake in flight: %+v", got)
	}
}

func TestMorningEndsATeardownsKillSwitchButNotAnAdmins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	if err := StopAll(ctx, f.s.DB, TeardownActor, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); !ks.Enabled || ks.ChangedBy != TeardownActor {
		t.Fatalf("teardown pauses every lab: %+v", ks)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); ks.Enabled {
		t.Fatal("the rebuilt platform is open again")
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('kill_switch.on', 'kill_switch.off')`).Scan(&n)
	if n != 2 {
		t.Fatalf("both changes are audited: %d", n)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); !ks.Enabled {
		t.Fatal("an admin's kill switch stays on")
	}
}

func TestStopAllListsWhatATeardownWaitsFor(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	left, err := ActiveLabs(ctx, f.s.DB)
	if err != nil || len(left) != 1 || left[0] != id+" (aws, asleep)" {
		t.Fatalf("%v %v", left, err)
	}
	if err := StopAll(ctx, f.s.DB, TeardownActor, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.s.Sweep(ctx) // the server ends every lab while the switch is on
	f.waitState(t, f.u, id, Destroyed)
	if left, _ := ActiveLabs(ctx, f.s.DB); len(left) != 0 {
		t.Fatalf("nothing left: %v", left)
	}
}

func containsMsg(err error, s string) bool { return err != nil && strings.Contains(err.Error(), s) }

func TestBedtimeWaitsForALabBeingDestroyed(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.hold = make(chan struct{})
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	if _, err := f.s.End(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Destroying)
	f.bedtime(t)
	f.s.Sweep(ctx)
	f.clk.Add(11 * time.Minute)
	f.s.Sweep(ctx)
	if awake, _ := AwakeAWSLabs(ctx, f.s.DB); len(awake) != 1 || f.phase(t) == "asleep" {
		t.Fatalf("a destroy in flight is waited for: %v %q", awake, f.phase(t))
	}
	close(run.hold)
	f.waitState(t, f.u, id, Destroyed)
	f.s.Sweep(ctx)
	if p := f.phase(t); p != "asleep" {
		t.Fatalf("asleep once the destroy finished: %q", p)
	}
}

func TestStopAllLeavesAnAdminsSwitchTheirs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	if err := StopAll(ctx, f.s.DB, TeardownActor, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	ks, _ := f.s.KillSwitch(ctx)
	if !ks.Enabled || ks.ChangedBy == TeardownActor {
		t.Fatalf("the admin's switch stays on and theirs: %+v", ks)
	}
}

func TestRequestBedtimeRestartsAStaleOneButKeepsTonights(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.bedtime(t)
	if _, err := f.s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(time.Hour)
	f.bedtime(t)
	if p := f.phase(t); p != "asleep" {
		t.Fatalf("the same night keeps its bedtime: %q", p)
	}
	f.clk.Add(24 * time.Hour)
	f.bedtime(t)
	b, _ := ReadBedtime(ctx, f.s.DB)
	if b.Phase != "settling" || !b.RequestedAt.Equal(f.clk.Now()) {
		t.Fatalf("a bedtime from an earlier night starts over: %+v", b)
	}
}

func TestAFailedSleepIsRetriedEveryFiveMinutes(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.sleepErr = errors.New("boom")
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.bedtime(t)
	count := func() int { run.mu.Lock(); defer run.mu.Unlock(); return len(run.slept) }
	waitCount := func(n int) {
		for i := 0; i < 200 && count() < n; i++ {
			time.Sleep(25 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond) // let the failure be recorded
		if count() != n {
			t.Fatalf("want %d attempts, got %d", n, count())
		}
	}
	f.s.Sweep(ctx)
	waitCount(1)
	f.clk.Add(15 * time.Second)
	f.s.Sweep(ctx)
	waitCount(1)
	f.clk.Add(5 * time.Minute)
	f.s.Sweep(ctx)
	waitCount(2)
}

func TestWakeRefusedWhilePausedAndAWakeInFlightIsNotLeftWaking(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	pause := func(on bool) { // straight to the row: the switch's own kill-all would end the lab
		if _, err := f.s.DB.Exec(ctx, `UPDATE kill_switch SET enabled = $1`, on); err != nil {
			t.Fatal(err)
		}
	}
	pause(true)
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) || !containsMsg(err, "Labs are paused by an admin.") {
		t.Fatalf("%v", err)
	}
	pause(false)
	f.s.inflight.Store("wake:"+id, true) // a previous wake goroutine still holds the slot
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("%v", err)
	}
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Asleep {
		t.Fatalf("not left waking: %s", got.State)
	}
}

func TestASleepWithoutItsContentBacksOffLikeAFailedSleep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET sha = 'gone' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	f.bedtime(t)
	failures := func() (n int) {
		f.settled(t, "sleep:"+id)
		_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_events WHERE lab_id = $1 AND kind = 'sleep_failed'`, id).Scan(&n)
		return n
	}
	f.s.Sweep(ctx)
	if n := failures(); n != 1 {
		t.Fatalf("a lab whose content is gone records a failed sleep: %d", n)
	}
	f.clk.Add(time.Minute)
	f.s.Sweep(ctx)
	if n := failures(); n != 1 {
		t.Fatalf("and waits five minutes like any failed sleep: %d", n)
	}
}

func TestABedtimeOutlivingItsNodeEnds(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		ago  time.Duration
		kept bool
	}{{2 * time.Hour, true}, {3*time.Hour + time.Minute, false}} {
		f := setup(t, true)
		f.bedtime(t)
		if _, err := f.s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`); err != nil {
			t.Fatal(err)
		}
		f.clk.Add(tc.ago)
		f.s.Sweep(ctx)
		if kept := f.phase(t) == "asleep"; kept != tc.kept {
			t.Fatalf("asleep %s ago: kept %v (the node did not stop, so labs may start again)", tc.ago, kept)
		}
	}
}
