package labs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// completeFirstLab marks module 02 done so the linear training unlocks 03-cluster-heat.
func (f *fx) completeFirstLab(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, it := range []string{"before-the-lab", "lab"} {
		if err := f.s.Learn.SetItem(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", it, "complete", 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClusterLabUnavailableWithoutRunner(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	m, err := f.s.ModuleLab(context.Background(), f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime != "cluster" || m.RuntimeReady || !strings.Contains(m.RuntimeMessage, "cluster labs are not available yet") {
		t.Fatalf("without a cluster runner the lobby must say so: %+v", m)
	}
}

func TestClusterLabChecksAreNotSelfReported(t *testing.T) {
	f := setup(t, true)
	f.completeFirstLab(t)
	f.s.Runners["cluster"], f.s.Estimators["cluster"] = f.run, f.rates
	ctx := context.Background()
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "03-cluster-heat")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && v.State != Ready; i++ {
		time.Sleep(10 * time.Millisecond)
		if v, err = f.s.Get(ctx, f.u, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	if v.State != Ready || v.Runtime != "cluster" || v.SelfReported {
		t.Fatalf("cluster lab view: state %s runtime %s self_reported %v", v.State, v.Runtime, v.SelfReported)
	}
	res, err := f.s.Check(ctx, f.u, v.ID, "t1-cast", "")
	if err != nil || !res.Passed {
		t.Fatalf("check: %+v %v", res, err)
	}
	var self bool
	if err := f.s.DB.QueryRow(ctx, `SELECT self_reported FROM check_runs WHERE lab_id = $1`, v.ID).Scan(&self); err != nil {
		t.Fatal(err)
	}
	if self {
		t.Fatal("cluster checks run server-side and must not be flagged self-reported")
	}
	if got := f.run.scripts[len(f.run.scripts)-1]; got.Service != "shell" || !strings.Contains(string(got.Script), "hello crucible") {
		t.Fatalf("the check must run the t1 script in the shell service: %+v", got)
	}
}
