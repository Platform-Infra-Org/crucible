package labs

import (
	"context"
	"crucible/internal/awscloud"
	"errors"
	"testing"
	"time"
)

// asleep puts a ready aws lab to sleep by hand, as the bedtime step does (Task 5).
func (f *fx) asleep(t *testing.T, id string) {
	t.Helper()
	if _, err := f.s.DB.Exec(context.Background(), `UPDATE lab_instances SET state = 'asleep', slept_at = $2, slept_as = 'stopped'
		WHERE id = $1 AND state = 'ready'`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAnAsleepLabOutlivesItsIdleTimeoutAndEndsAtItsDeadline(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	v := f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.clk.Add(time.Duration(v.IdleTimeoutS+60) * time.Second)
	if v.EndsAt == nil || !f.clk.Now().Before(*v.EndsAt) {
		t.Fatalf("fixture: the idle deadline must come before the lab's end: %+v", v)
	}
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Asleep || got.SleptAs != "stopped" || got.SleptAt == nil {
		t.Fatalf("nobody can be active while asleep: the idle rule skips it, got %+v", got)
	}
	f.clk.Set(v.EndsAt.Add(time.Second))
	f.s.Sweep(ctx)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != v.LimitReason {
		t.Fatalf("sleep counts towards the lab's time: ended %q, want %q", got.EndReason, v.LimitReason)
	}
}

func TestStartFindsTheSleepingLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat", StartOptions{})
	if err != nil || v.ID != id || v.State != Asleep {
		t.Fatalf("one lab per module: Start returns the sleeping one: %+v %v", v, err)
	}
}

func TestKillSwitchEndsSleepingLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Destroyed)
}

func TestSleepingLabsAreNotEndedForTheReaper(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	ended, known, err := f.s.labsEnded(ctx, []string{id}, f.clk.Now())
	if err != nil || !known[id] {
		t.Fatalf("%v %v", known, err)
	}
	if _, over := ended[id]; over {
		t.Fatal("an asleep lab is alive: the reaper must not delete its machines")
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2 WHERE id = $1`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if ended, _, _ = f.s.labsEnded(ctx, []string{id}, f.clk.Now()); ended[id] != (time.Time{}) {
		t.Fatal("a waking lab is alive too")
	}
}

func TestSleepingLabsStayCommittedInSpend(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	before, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	f.asleep(t, id)
	after, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	if after.CommittedUSD < before.CommittedUSD-1e-9 {
		t.Fatalf("a sleeping lab still holds its cost up to its deadline: %v → %v", before.CommittedUSD, after.CommittedUSD)
	}
}

func TestAWakeThatNeverFinishedGoesBackToSleep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2 WHERE id = $1`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(21 * time.Minute)
	run.mu.Lock()
	run.sleepErr = errors.New("throttled")
	run.mu.Unlock()
	f.s.Sweep(ctx)
	f.settled(t, "sleep:"+id)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Waking {
		t.Fatalf("machines that may run are never called asleep: %+v", got)
	}
	run.mu.Lock()
	run.sleepErr = nil
	run.mu.Unlock()
	f.clk.Add(6 * time.Minute) // past the failed sleep's back-off
	f.s.Sweep(ctx)
	got := f.waitState(t, f.u, id, Asleep)
	if got.Error != "Waking took too long; press Wake it again." {
		t.Fatalf("a restart mid-wake leaves the lab asleep with a way forward: %+v", got)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.slept) != 2 || run.slept[1] != id {
		t.Fatalf("whatever the dead wake started is put back to sleep through the runner: %v", run.slept)
	}
}

func TestASleepingLabsPendingExtensionCanBeDecided(t *testing.T) {
	ctx := context.Background()
	for _, approve := range []bool{true, false} {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		if got, err := f.s.Extend(ctx, f.u, ready.ID); err != nil || !got.ExtensionPending {
			t.Fatalf("fixture: %+v %v", got, err)
		}
		f.asleep(t, ready.ID)
		if v, _ := f.s.Get(ctx, f.u, ready.ID); v.State != Asleep || !v.ExtensionPending {
			t.Fatalf("a sleeping lab keeps its pending extension: %+v", v)
		}
		if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 1 {
			t.Fatalf("approvals list: %+v", list)
		}
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, approve, ""); err != nil {
			t.Fatalf("approve=%v: %v", approve, err)
		}
		v, _ := f.s.Get(ctx, f.u, ready.ID)
		want := *ready.EndsAt
		if approve {
			want = want.Add(30 * time.Minute)
		}
		if v.ExtensionPending || !v.EndsAt.Equal(want) {
			t.Fatalf("approve=%v: ends %v want %v, %+v", approve, v.EndsAt, want, v)
		}
	}
}

func TestLedgerListsSleepingAndWakingLabsAsRunning(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	now := f.clk.Now()
	for id, st := range map[string]State{"ddddddddddd1": Asleep, "ddddddddddd2": Waking} {
		in := &Instance{ID: id, UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: "m-" + id, SHA: "abc",
			Runtime: "aws", State: st, CreatedAt: now, ReadyAt: &now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute,
			Tier: "approver", HourlyUSD: 1, EstimateUSD: 1}
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ready_at = $1 WHERE id LIKE 'ddddddddddd%'`, now); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Ledger(ctx, f.admin)
	if err != nil || len(l.Running) != 2 {
		t.Fatalf("both still cost their disks: %+v %v", l.Running, err)
	}
}

func TestAWakeThatNeverFinishedPastItsDeadlineIsDestroyed(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2, ends_at = $3 WHERE id = $1`,
		id, f.clk.Now(), f.clk.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(21 * time.Minute)
	f.s.Sweep(ctx)
	f.waitState(t, f.u, id, Destroyed)
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.slept) != 0 {
		t.Fatalf("a lab out of time is destroyed, not put to sleep: %v", run.slept)
	}
}
