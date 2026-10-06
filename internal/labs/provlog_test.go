package labs

import (
	"context"
	"strings"
	"testing"
	"time"

	"crucible/internal/content"
	"crucible/internal/notify"
)

// firstHeat is the fixture lab of this test's own content load; changing it changes only this test.
func (f *fx) firstHeat() *content.Lab {
	return f.s.Learn.State().Training("forge-101", "abc").Module("02-first-lab").Lab
}

func TestProvisioningViewCarriesTheEventLog(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	release := make(chan struct{})
	f.run.provision = func() { <-release }
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	var got *View
	for i := 0; i < 200; i++ {
		if got, _ = f.s.Get(ctx, f.u, v.ID); len(got.Log) > 0 && got.Log[len(got.Log)-1] == "starting services" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want := []string{"requested: local", "approved: auto", "preparing workspace", "starting services"}
	if got.State != Provisioning || len(got.Log) != len(want) {
		t.Fatalf("provisioning log: %s %q", got.State, got.Log)
	}
	for i, w := range want {
		if !strings.HasPrefix(got.Log[i], w) {
			t.Fatalf("line %d: %q, want %q…", i, got.Log[i], w)
		}
	}
	close(release)
	if ready := f.waitState(t, f.u, v.ID, Ready); len(ready.Log) != 0 {
		t.Fatalf("a ready lab carries no log: %q", ready.Log)
	}
}

func TestLabLevelSetupRunsOnceBeforeReady(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.firstHeat().Setup = &content.Script{Script: "checks/01-forge-file.sh", RunIn: "web"}
	v := f.start(t)
	if _, err := f.s.OpenTask(ctx, f.u, v.ID, "t1-forge-file"); err != nil {
		t.Fatal(err)
	}
	_, _ = f.s.Get(ctx, f.u, v.ID)
	var runs int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM setup_runs WHERE lab_id = $1 AND task = ''`, v.ID).Scan(&runs)
	if runs != 1 {
		t.Fatalf("lab-level setup ran %d times", runs)
	}
	var order string
	_ = f.s.DB.QueryRow(ctx, `SELECT string_agg(coalesce(nullif(detail, ''), kind), ',' ORDER BY id) FROM lab_events
		WHERE lab_id = $1 AND kind IN ('progress', 'ready')`, v.ID).Scan(&order)
	if order != "preparing workspace,starting services,running lab setup,ready" {
		t.Fatalf("setup runs before ready: %s", order)
	}
}

func TestLabLevelSetupFailureFailsTheLab(t *testing.T) {
	f := setup(t, true)
	f.firstHeat().Setup = &content.Script{Script: "checks/01-forge-file.sh", RunIn: "web"}
	f.run.exit = func(sp ScriptSpec) int {
		if sp.Service == "web" {
			return 1
		}
		return 0
	}
	v, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, v.ID, Failed)
	if !strings.Contains(got.Error, "setup exited with 1") || f.run.destroyedN() != 1 {
		t.Fatalf("failed lab: %+v destroyed %d", got, f.run.destroyedN())
	}
	if ev := f.notes.last(notify.SetupFailed); ev == nil || !strings.Contains(ev.Text, "lab-level setup") {
		t.Fatalf("maintainers hear about it: %+v", ev)
	}
}

func TestFreeTaskOrderOpensEveryTask(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.firstHeat().TaskOrder = "free"
	v := f.start(t)
	for _, tv := range v.Tasks {
		if tv.Status != "open" {
			t.Fatalf("free order locks nothing: %+v", v.Tasks)
		}
	}
	if _, err := f.s.OpenTask(ctx, f.u, v.ID, "t3-fix-nginx"); err != nil {
		t.Fatal(err)
	}
	if res, err := f.s.Check(ctx, f.u, v.ID, "t3-fix-nginx", ""); err != nil || !res.Passed || statusOf(res.Lab, "t1-forge-file") != "open" {
		t.Fatalf("the last task first: %+v %v", res, err)
	}
}
