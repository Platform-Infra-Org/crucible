package labs

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/pricing"
)

type fakePlanner struct {
	calls int
	plan  []byte
	err   error
}

func (f *fakePlanner) Plan(context.Context, *content.Lab) ([]byte, error) {
	f.calls++
	return f.plan, f.err
}

// planFunc is a Planner for tests that need to hold, count or break a run.
type planFunc func(context.Context, *content.Lab) ([]byte, error)

func (f planFunc) Plan(ctx context.Context, lab *content.Lab) ([]byte, error) { return f(ctx, lab) }

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testPrices are the list prices internal/pricing's tests use for eu-west-1.
func testPrices() pricing.Snapshot {
	k := func(key string) [3]string { return [3]string{"aws", "eu-west-1", key} }
	return pricing.Snapshot{k("ec2:t3.micro"): 0.0114, k("ebs:gp3"): 0.088, k("nat:hour"): 0.048, k("nat:gb"): 0.048,
		k("ipv4:hour"): 0.005, k("elb:application"): 0.0252, k("elb:network"): 0.0252, k("s3:standard"): 0.023}
}

func fixedPrices(context.Context) (pricing.Lookup, error) { return testPrices(), nil }

// tfLab is an aws lab whose content version is dir, with room under its ceiling for the fixture plan.
func tfLab(dir string) *content.Lab {
	return &content.Lab{ID: "l", Dir: dir, Runtime: "aws", AWS: &content.AWSConfig{Region: "eu-west-1", MaxHourlyUSD: 1,
		Usage: map[string]map[string]float64{"aws_s3_bucket.data": {"storage_gb": 100}}}}
}

func TestTerraformEstimatePricesThePlanOncePerVersion(t *testing.T) {
	ctx := context.Background()
	p := &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}
	e := &TerraformEstimator{Plan: p, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }}
	lab := &content.Lab{ID: "l", Dir: "/content/abc/lab", Runtime: "aws", AWS: &content.AWSConfig{Region: "eu-west-1", MaxHourlyUSD: 1,
		Usage: map[string]map[string]float64{"aws_s3_bucket.data": {"storage_gb": 100}}}}
	h, err := e.HourlyUSD(ctx, lab)
	if err != nil || h <= 0 {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := e.HourlyUSD(ctx, lab); err != nil || p.calls != 1 {
		t.Fatalf("cached per content version: %d calls, %v", p.calls, err)
	}
	b, err := e.Breakdown(ctx, lab)
	if err != nil || b.HourlyUSD != h || len(b.Lines) == 0 || p.calls != 1 {
		t.Fatalf("the breakdown is the cached run's: %+v %v (%d calls)", b, err, p.calls)
	}
	declared := tfLab("/content/def/lab")
	declared.AWS.Prices = map[string]float64{"aws_lb": 2}
	declared.AWS.MaxHourlyUSD = 5
	if h2, err := e.HourlyUSD(ctx, declared); err != nil || h2 < 2 || p.calls != 2 {
		t.Fatalf("a new version is planned again, with the lab's declared prices: %v %v (%d calls)", h2, err, p.calls)
	}
}

func TestTerraformEstimateFailsClosed(t *testing.T) {
	ctx := context.Background()
	lab := &content.Lab{ID: "l", Dir: "/c/1", Runtime: "aws", AWS: &content.AWSConfig{Region: "eu-west-1", MaxHourlyUSD: 0.01}}
	for name, e := range map[string]*TerraformEstimator{
		"plan fails":   {Plan: &fakePlanner{err: errors.New("boom")}, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }},
		"stale prices": {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return nil, pricing.ErrStale }},
		"over ceiling": {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return testPrices(), nil }},
	} {
		h, err := e.HourlyUSD(ctx, lab)
		if err == nil || h != 0 {
			t.Errorf("%s: must fail, got %v %v", name, h, err)
		}
	}
}

// The plan is cached per content version, the price is not: stale prices block a lab priced before, and a new
// weekly price reaches it, both within a minute and without planning again. A price failure is never remembered.
func TestTerraformEstimateRepricesEveryCall(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1000, 0)}
	p := &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}
	var fetches int
	stale := false
	e := &TerraformEstimator{Plan: p, Now: clk.Now, Prices: func(context.Context) (pricing.Lookup, error) {
		fetches++
		if stale {
			return nil, pricing.ErrStale
		}
		return testPrices(), nil
	}}
	lab := tfLab("/c/fresh")
	if h, err := e.HourlyUSD(ctx, lab); err != nil || h <= 0 {
		t.Fatalf("%v %v", h, err)
	}
	stale = true
	if _, err := e.HourlyUSD(ctx, lab); err != nil || fetches != 1 {
		t.Fatalf("prices are kept for a minute: %v (%d fetches)", err, fetches)
	}
	clk.Add(61 * time.Second)
	if h, err := e.HourlyUSD(ctx, lab); !errors.Is(err, pricing.ErrStale) || h != 0 {
		t.Fatalf("stale prices block a lab priced before: %v %v", h, err)
	}
	if b, err := e.Breakdown(ctx, lab); err == nil || b.HourlyUSD != 0 {
		t.Fatalf("and its breakdown: %+v %v", b, err)
	}
	stale = false
	if h, err := e.HourlyUSD(ctx, lab); err != nil || h <= 0 {
		t.Fatalf("a price failure is not remembered: %v %v", h, err)
	}
	if p.calls != 1 {
		t.Fatalf("the plan is cached per version: %d plans", p.calls)
	}
}

func TestTerraformEstimateFollowsNewPrices(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1000, 0)}
	p := &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}
	ec2 := 0.0114
	e := &TerraformEstimator{Plan: p, Now: clk.Now, Prices: func(context.Context) (pricing.Lookup, error) {
		s := testPrices()
		s[[3]string{"aws", "eu-west-1", "ec2:t3.micro"}] = ec2
		return s, nil
	}}
	lab := tfLab("/c/weekly")
	before, err := e.HourlyUSD(ctx, lab)
	if err != nil {
		t.Fatal(err)
	}
	ec2 += 0.1
	clk.Add(2 * time.Minute)
	after, err := e.HourlyUSD(ctx, lab)
	if err != nil || math.Abs(after-before-0.1) > 1e-9 || p.calls != 1 {
		t.Fatalf("the hourly figure follows the price: %v → %v %v (%d plans)", before, after, err, p.calls)
	}
}

// More ways to get a $0 or a wrong price: each is an error, never a number.
func TestTerraformEstimateNeverQuotesWithoutAPrice(t *testing.T) {
	ctx := context.Background()
	lab := tfLab("/c/2")
	free := []byte(`{"resource_changes":[{"address":"aws_security_group.x","mode":"managed","type":"aws_security_group",
		"change":{"actions":["create"],"after":{}}}]}`)
	unknown := []byte(`{"resource_changes":[{"address":"aws_kinesis_stream.x","mode":"managed","type":"aws_kinesis_stream",
		"change":{"actions":["create"],"after":{}}}]}`)
	for name, e := range map[string]*TerraformEstimator{
		"plan is not json":    {Plan: &fakePlanner{plan: []byte("Error: no credentials")}, Prices: fixedPrices},
		"nothing costs money": {Plan: &fakePlanner{plan: free}, Prices: fixedPrices},
		"unpriced type":       {Plan: &fakePlanner{plan: unknown}, Prices: fixedPrices},
		"no prices at all":    {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return pricing.Snapshot{}, nil }},
		"price store down":    {Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: func(context.Context) (pricing.Lookup, error) { return nil, errors.New("db down") }},
	} {
		if h, err := e.HourlyUSD(ctx, lab); err == nil || h != 0 {
			t.Errorf("%s: must fail, got %v %v", name, h, err)
		}
		if b, err := e.Breakdown(ctx, lab); err == nil || b.HourlyUSD != 0 {
			t.Errorf("%s: no breakdown either, got %+v %v", name, b, err)
		}
	}
	over := &TerraformEstimator{Plan: &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}, Prices: fixedPrices}
	lab.AWS.MaxHourlyUSD = 0.01
	if _, err := over.HourlyUSD(ctx, lab); !errors.Is(err, apperr.Invalid) ||
		!strings.Contains(err.Error(), "This lab is priced at $0.1120/h, above its $0.01/h limit; its maintainers need to make it cheaper.") {
		t.Fatalf("over the ceiling says why: %v", err)
	}
	if _, err := over.HourlyUSD(ctx, &content.Lab{Dir: "/c/3", Runtime: "aws"}); err == nil {
		t.Fatal("a lab without an aws block has no estimate")
	}
}

// Trainee lobby views drive the estimator: one run per lab version at a time, failures are remembered for a while,
// and at most two runs go at once; a caller without a slot is told to come back instead of queueing.
func TestTerraformEstimatorBoundsRuns(t *testing.T) {
	plan := mustRead(t, "../pricing/testdata/plan.json")
	lab := tfLab("/c/a")
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 10)
	e := &TerraformEstimator{Prices: fixedPrices, Plan: planFunc(func(context.Context, *content.Lab) ([]byte, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return plan, nil
	})}
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
	go func() { _, _ = e.HourlyUSD(context.Background(), tfLab("/c/b")) }()
	waitStarted()
	third, cancel := context.WithTimeout(context.Background(), 2*time.Second) // a missing limit fails, not hangs
	defer cancel()
	if _, err := e.HourlyUSD(third, tfLab("/c/c")); !errors.Is(err, apperr.Unavailable) ||
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
	failing := &TerraformEstimator{Now: func() time.Time { return now }, Prices: fixedPrices,
		Plan: planFunc(func(context.Context, *content.Lab) ([]byte, error) {
			fails.Add(1)
			return nil, errors.New("no plan role")
		})}
	for range 3 {
		if _, err := failing.HourlyUSD(context.Background(), lab); err == nil || !strings.Contains(err.Error(), "no plan role") {
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
func TestTerraformEstimatorSurvivesPanickingRun(t *testing.T) {
	lab := tfLab("/c/p")
	var runs atomic.Int32
	e := &TerraformEstimator{Prices: fixedPrices, Plan: planFunc(func(context.Context, *content.Lab) ([]byte, error) {
		runs.Add(1)
		panic("boom")
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 2 {
		if h, err := e.HourlyUSD(ctx, lab); err == nil || h != 0 || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("the panic is a failure: %v %v", h, err)
		}
	}
	e.mu.Lock()
	running, inflight := e.running, len(e.inflight)
	e.mu.Unlock()
	if running != 0 || inflight != 0 || runs.Load() != 1 {
		t.Fatalf("slot released and failure remembered: running=%d inflight=%d runs=%d", running, inflight, runs.Load())
	}
}

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

// The lobby shows what the plan priced, line by line; over the ceiling it says so and shows no breakdown.
func TestModuleLabCarriesTheEstimateBreakdown(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	f.s.Runners["aws"] = &fakeRunner{}
	p := &fakePlanner{plan: mustRead(t, "../pricing/testdata/plan.json")}
	f.s.Estimators["aws"] = &TerraformEstimator{Plan: p, Prices: fixedPrices}
	m, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat") // the fixture plan is over its $0.05/h
	if err != nil || m.Estimate != nil || m.Blocked != "This lab is priced at $0.1088/h, above its $0.05/h limit; its maintainers need to make it cheaper." {
		t.Fatalf("over the ceiling: %+v %v", m, err)
	}
	f.s.Learn.State().Trainings["forge-401@abc"].Module("01-cloud-heat").Lab.AWS.MaxHourlyUSD = 1
	f.s.Learn.State().Trainings["forge-401@abc"].Module("01-cloud-heat").Lab.Dir += "-v2" // a new content version
	m, err = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || m.Blocked != "" || m.Estimate == nil || len(m.Estimate.Lines) == 0 || m.Estimate.HourlyUSD <= 0 || m.EstimateUSD <= 0 {
		t.Fatalf("the breakdown comes with the quote: %+v %v", m, err)
	}
	if p.calls != 2 {
		t.Fatalf("one plan per version, the breakdown reuses it: %d", p.calls)
	}
}

type pendingEstimator struct{}

func (pendingEstimator) HourlyUSD(context.Context, *content.Lab) (float64, error) {
	return 0, apperr.Wrap(apperr.Unavailable, "estimate pending, try again shortly")
}
