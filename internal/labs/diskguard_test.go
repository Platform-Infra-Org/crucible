package labs

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestAWorkspaceOverTwentyGiBIsWarnedThenPutOut(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	run.mu.Lock()
	run.mib = workspaceLimitMiB + 1
	run.mu.Unlock()
	f.s.Sweep(ctx)
	f.settled(t, "measure:"+id)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Ready || !got.WorkspaceWarning {
		t.Fatalf("warned once first: %+v", got)
	}
	f.s.Sweep(ctx) // within five minutes: not measured again
	f.settled(t, "measure:"+id)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Ready {
		t.Fatalf("measured every five minutes, not every sweep: %s", got.State)
	}
	f.clk.Add(5 * time.Minute)
	f.s.Sweep(ctx)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != "workspace_full" {
		t.Fatalf("still over at the next measure: put out: %q", got.EndReason)
	}
}

func TestANearlyFullNodeTakesNoNewAWSLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	f.s.NodeStorage = t.TempDir()
	old := minNodeFree
	minNodeFree = math.MaxInt64 // whatever this disk has free, it is less
	t.Cleanup(func() { minNodeFree = old })
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || ml.Blocked != diskFullMsg {
		t.Fatalf("%q %v", ml.Blocked, err)
	}
	minNodeFree = 0
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat"); ml.Blocked != "" {
		t.Fatalf("room enough: %q", ml.Blocked)
	}
}

func TestASlowMeasureNeverHoldsTheSweep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	run.mibHold = make(chan struct{})
	done := make(chan struct{})
	go func() { f.s.Sweep(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("a du still running held up the sweep")
	}
	close(run.mibHold)
	<-done
	f.settled(t, "measure:"+id)
}
