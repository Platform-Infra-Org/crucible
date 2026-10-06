package labs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content"
)

// withForge401 enrols the trainee in Forge 401 (one aws lab, cloud-heat) and prices it with f.rates.
func (f *fx) withForge401(t *testing.T) {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := f.s.Learn.State()
	st.Trainings["forge-401@abc"] = tr
	st.ProgramSHAs["forge/forge-401"] = "abc"
	f.plat.Teams["forge"].Programs["forge-401"] = &config.Program{Training: "forge-401", Enrolled: []string{"trainee@crucible.local"}}
	f.s.Estimators["aws"] = f.rates
	f.s.AWSRegions = []string{"eu-west-1"}
}

func TestInfracostEstimatorCachesPerContentVersion(t *testing.T) {
	var calls atomic.Int32
	e := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"projects":[{}],"totalHourlyCost":"0.02"}`), nil
	}}
	tr, _ := content.Load("../../examples/forge-401")
	lab := tr.Module("01-cloud-heat").Lab
	for range 2 {
		if h, err := e.HourlyUSD(context.Background(), lab); err != nil || h != 0.02 {
			t.Fatalf("%v %v", h, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("one infracost run per content version, got %d", calls.Load())
	}
	failing := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		return nil, errors.New("no API key")
	}}
	if _, err := failing.HourlyUSD(context.Background(), lab); err == nil {
		t.Fatal("errors are returned")
	}
}

func TestAWSQuoteBlocksOverPricedAndForeignLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	f.s.Runners["aws"] = &fakeRunner{}
	f.rates["cloud-heat"] = 0.0501 // its lab.yaml allows $0.05/h
	m, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || !strings.Contains(m.Blocked, "priced at $0.0501/h, above its $0.05/h limit") {
		t.Fatalf("over the author's ceiling: %+v %v", m, err)
	}
	f.rates["cloud-heat"] = 0.01
	f.s.AWSRegions = []string{"us-east-1"}
	m, _ = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if !strings.Contains(m.Blocked, "runs in eu-west-1, which this server does not allow") {
		t.Fatalf("region outside the lab account's allowed regions: %+v", m)
	}
	f.s.AWSRegions = nil
	m, _ = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if m.Blocked == "" {
		t.Fatal("no allowed regions configured fails closed")
	}
	f.s.AWSRegions = []string{"eu-west-1"}
	m, _ = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if m.Blocked != "" || !m.NeedsApproval || m.EstimateUSD != 0.01 {
		t.Fatalf("a cheap aws lab still needs an approver (aws never auto-approves): %+v", m)
	}
	f.s.Estimators["aws"] = pendingEstimator{}
	if m, err = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat"); err != nil || m.Blocked != "estimate pending, try again shortly" {
		t.Fatalf("a busy estimator asks the trainee to come back: %+v %v", m, err)
	}
}

type pendingEstimator struct{}

func (pendingEstimator) HourlyUSD(context.Context, *content.Lab) (float64, error) {
	return 0, apperr.Wrap(apperr.Unavailable, "estimate pending, try again shortly")
}

// Trainee lobby views drive the estimator: one run per lab version at a time, failures are remembered for a while,
// and at most two runs go at once; a caller without a slot is told to come back instead of queueing.
func TestInfracostEstimatorBoundsRuns(t *testing.T) {
	tr, _ := content.Load("../../examples/forge-401")
	lab := tr.Module("01-cloud-heat").Lab
	other := func() *content.Lab {
		l := *lab
		l.Dir = t.TempDir()
		if err := os.Mkdir(filepath.Join(l.Dir, "terraform"), 0o755); err != nil {
			t.Fatal(err)
		}
		return &l
	}
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 10)
	e := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return []byte(`{"projects":[{}],"totalHourlyCost":"0.02"}`), nil
	}}
	// singleflight: concurrent views of one lab version share one run
	var wg sync.WaitGroup
	results := make(chan error, 5)
	for range 5 {
		wg.Go(func() { _, err := e.HourlyUSD(context.Background(), lab); results <- err })
	}
	waitStarted := func() {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("no run started")
		}
	}
	waitStarted()
	// a second lab takes the other slot; a third finds none and is told to retry, without waiting
	go func() { _, _ = e.HourlyUSD(context.Background(), other()) }()
	waitStarted()
	third, cancel := context.WithTimeout(context.Background(), 2*time.Second) // a missing limit fails, not hangs
	defer cancel()
	if _, err := e.HourlyUSD(third, other()); !errors.Is(err, apperr.Unavailable) ||
		!strings.Contains(err.Error(), "estimate pending, try again shortly") {
		t.Fatalf("no free slot: %v", err)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("one run per lab version in flight, got %d", calls.Load())
	}

	// failures are cached for a few minutes
	now := time.Unix(0, 0)
	var fails atomic.Int32
	failing := &InfracostEstimator{Now: func() time.Time { return now }, Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		fails.Add(1)
		return nil, errors.New("no API key")
	}}
	for range 3 {
		if _, err := failing.HourlyUSD(context.Background(), lab); err == nil || !strings.Contains(err.Error(), "no API key") {
			t.Fatalf("the failure is returned: %v", err)
		}
	}
	if fails.Load() != 1 {
		t.Fatalf("a failure is remembered, got %d runs", fails.Load())
	}
	now = now.Add(6 * time.Minute)
	_, _ = failing.HourlyUSD(context.Background(), lab)
	if fails.Load() != 2 {
		t.Fatal("and retried after a few minutes")
	}
}

// A panicking run must not leak its slot or leave its waiters hanging: it is a remembered failure.
func TestInfracostEstimatorSurvivesPanickingRun(t *testing.T) {
	tr, _ := content.Load("../../examples/forge-401")
	lab := tr.Module("01-cloud-heat").Lab
	var runs atomic.Int32
	e := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		runs.Add(1)
		panic("boom")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 2 {
		if _, err := e.HourlyUSD(ctx, lab); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("the panic is a failure: %v", err)
		}
	}
	e.mu.Lock()
	running, inflight := e.running, len(e.inflight)
	e.mu.Unlock()
	if running != 0 || inflight != 0 || runs.Load() != 1 {
		t.Fatalf("slot released and failure remembered: running=%d inflight=%d runs=%d", running, inflight, runs.Load())
	}
}
