package labs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

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
		return []byte(`{"totalHourlyCost":"0.02"}`), nil
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
	f.rates["cloud-heat"] = 0.06 // its lab.yaml allows $0.05/h
	m, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || !strings.Contains(m.Blocked, "above its $0.05/h limit") {
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
}
