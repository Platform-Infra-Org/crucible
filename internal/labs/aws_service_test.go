package labs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

// fakeAWSRunner is an aws runtime without Kubernetes: "apply" adds the lab's bucket and a hand-made volume to the
// fake cloud; "destroy" removes the bucket only, as terraform would.
type fakeAWSRunner struct {
	fakeRunner
	cloud *awscloud.Fake
	labs  []*content.Lab
	hold  chan struct{} // optional: Destroy waits for it to close (a long terraform destroy)
}

func (f *fakeAWSRunner) ProvisionLab(_ context.Context, inst *Instance, lab *content.Lab) error {
	if f.provision != nil {
		f.provision()
	}
	f.mu.Lock()
	f.labs = append(f.labs, lab)
	err := f.failProvision
	f.mu.Unlock()
	if err == nil {
		f.cloud.SimulateApply(lab.AWS.Region, awscloud.Session{LabID: inst.ID})
	}
	return err
}

func (f *fakeAWSRunner) Destroy(ctx context.Context, inst *Instance) error {
	if f.hold != nil {
		<-f.hold
	}
	f.cloud.SimulateDestroy(inst.ID)
	return f.fakeRunner.Destroy(ctx, inst)
}

func (f *fx) withAWS(t *testing.T) (*fakeAWSRunner, *awscloud.Fake) {
	t.Helper()
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	run := &fakeAWSRunner{cloud: cloud}
	f.s.Runners["aws"], f.s.Cloud = run, cloud
	f.rates["cloud-heat"] = 0.04
	return run, cloud
}

func TestAWSLabFromRequestToTagSweep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, cloud := f.withAWS(t)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || v.State != PendingApproval || v.EstimateUSD != 0.04 {
		t.Fatalf("an aws lab always waits for an approver: %+v %v", v, err)
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	run.mu.Lock()
	got := slices.Clone(run.labs)
	run.mu.Unlock()
	if len(got) != 1 || got[0].ID != "cloud-heat" {
		t.Fatalf("the aws runner got the lab content: %+v", got)
	}
	ended, err := f.s.End(ctx, f.u, v.ID)
	if err != nil || (ended.State != Destroying && ended.State != Destroyed) {
		t.Fatalf("End returns without waiting for terraform: %+v %v", ended, err)
	}
	f.waitState(t, f.u, v.ID, Destroyed)
	f.settled(t, v.ID)
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-" + v.ID
	if cloud.Has(vol) {
		t.Fatal("the tag sweep deletes what terraform did not manage")
	}
	var action string
	if err := f.s.DB.QueryRow(ctx, `SELECT action FROM reaper_findings WHERE source = 'destroy' AND arn = $1`, vol).Scan(&action); err != nil || action != "deleted" {
		t.Fatalf("finding: %q %v", action, err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM reaper_findings WHERE arn LIKE 'arn:aws:s3:::%'`).Scan(&n)
	if n != 0 {
		t.Fatal("what terraform destroyed itself is not a finding")
	}
	// Spec §14: credentials never leave the workspace.
	view, _ := f.s.Get(ctx, f.u, v.ID)
	b, _ := json.Marshal(view)
	var events string
	_ = f.s.DB.QueryRow(ctx, `SELECT coalesce(string_agg(kind || ' ' || detail, E'\n'), '') FROM lab_events WHERE lab_id = $1`, v.ID).Scan(&events)
	for _, secret := range []string{"fake-token", "fake-secret", "ASIAFAKE"} {
		if strings.Contains(string(b), secret) || strings.Contains(events, secret) {
			t.Fatalf("%s leaked into the view or the lab events", secret)
		}
	}
}

func TestStuckAWSDestroyWaitsLonger(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	now := f.clk.Now()
	stuck := func(training, module, runtime string) string {
		in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: training, Module: module, SHA: "abc", Runtime: runtime,
			State: Destroying, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET destroyed_at = $2 WHERE id = $1`, in.ID, now.Add(-15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	awsID, localID := stuck("forge-401", "01-cloud-heat", "aws"), stuck("forge-101", "02-first-lab", "local")
	state := func(id string) (st State) {
		_ = f.s.DB.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1`, id).Scan(&st)
		return st
	}
	f.s.Sweep(ctx)
	if state(localID) != Destroyed || state(awsID) != Destroying {
		t.Fatal("a local or cluster destroy is retried after 10 minutes; terraform gets longer than its own destroy budget")
	}
	f.clk.Add(40 * time.Minute) // 55 minutes: an aws destroy may still be running
	f.s.Sweep(ctx)
	if state(awsID) != Destroying {
		t.Fatal("an aws destroy is not retried while a first attempt may still run")
	}
	f.clk.Add(20 * time.Minute) // 75 minutes
	f.s.Sweep(ctx)
	f.s.Sweep(ctx) // while the retry runs, another sweep must not start a second one
	for i := 0; i < 300 && state(awsID) != Destroyed; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	run.mu.Lock()
	n := len(slices.DeleteFunc(slices.Clone(run.destroyed), func(id string) bool { return id != awsID }))
	run.mu.Unlock()
	if state(awsID) != Destroyed || n != 1 {
		t.Fatalf("one retry, then destroyed: state %s, %d destroys", state(awsID), n)
	}
}

func TestAWSProvisionIsNotSweptAsHungWhileTerraformMayRun(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	now := f.clk.Now()
	in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-401", Module: "01-cloud-heat", SHA: "abc", Runtime: "aws",
		State: Provisioning, CreatedAt: now.Add(-30 * time.Minute), LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
	if err := f.s.insert(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.s.Sweep(ctx)
	if run.destroyedN() != 0 {
		t.Fatal("terraform apply may run 50 minutes; 30 minutes of provisioning is not hung")
	}
	f.clk.Add(40 * time.Minute)
	f.s.Sweep(ctx)
	for i := 0; i < 300 && run.destroyedN() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if run.destroyedN() != 1 {
		t.Fatal("70 minutes of provisioning is hung")
	}
}

func TestTagSweepLeavesNotYetDeletableForTheNextRun(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	_, cloud := f.withAWS(t)
	id := newLabID()
	cloud.SimulateApply("eu-west-1", awscloud.Session{LabID: id})
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-" + id
	inst := "arn:aws:ec2:eu-west-1:000000000000:instance/i-" + id
	cloud.Add("eu-west-1", awscloud.Resource{ARN: inst, LabID: id})
	cloud.NotYet(vol, 1) // attached to the instance until it has terminated
	creds, _ := cloud.AssumeLab(ctx, awscloud.Session{LabID: id})
	res, _ := cloud.Tagged(ctx, "eu-west-1", id)
	fresh := f.s.deleteAll(ctx, "eu-west-1", creds, "destroy", res, false)
	if !cloud.Has(vol) || cloud.Has(inst) || len(fresh) != 2 { // the instance and the bucket
		t.Fatalf("instances first; a volume still in use waits: %v", fresh)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM reaper_findings WHERE arn = $1`, vol).Scan(&n)
	if n != 0 {
		t.Fatal("'not yet' is no finding: not failed, just early")
	}
	res, _ = cloud.Tagged(ctx, "eu-west-1", id)
	if fresh := f.s.deleteAll(ctx, "eu-west-1", creds, "destroy", res, false); !slices.Equal(fresh, []string{vol}) || cloud.Has(vol) {
		t.Fatalf("the next run deletes it: %v", fresh)
	}
}

func TestSweepRefreshesReadyAWSLabCredentials(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	now := f.clk.Now()
	in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-401", Module: "01-cloud-heat", SHA: "abc", Runtime: "aws",
		State: Ready, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
	if err := f.s.insert(ctx, in); err != nil {
		t.Fatal(err)
	}
	end := now.Add(time.Hour)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ends_at = $2, ready_at = $3 WHERE id = $1`, in.ID, end, now); err != nil {
		t.Fatal(err)
	}
	a, cloud, _ := testAWS(fake.NewClientset(labNS(in.ID, false)))
	cloud.Now, a.Now = f.clk.Now, f.clk.Now
	f.s.Runners["aws"] = a
	f.s.Sweep(ctx)
	if s := cloud.Assumed(); len(s) != 1 || s[0].LabID != in.ID || s[0].Training != "forge-401" {
		t.Fatalf("after a restart the first sweep remounts fresh credentials: %+v", s)
	}
	f.clk.Add(10 * time.Minute)
	f.s.Sweep(ctx)
	if len(cloud.Assumed()) != 1 {
		t.Fatal("no STS call while the credentials last")
	}
}

// settled waits until no background destroy for the lab is running (it writes an event after the final state).
func (f *fx) settled(t *testing.T, id string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if _, busy := f.s.inflight.Load(id); !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lab %s: background destroy never finished", id)
}

func (f *fx) approvedAWS(t *testing.T) string {
	t.Helper()
	v, err := f.s.Start(context.Background(), f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Decide(context.Background(), f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func TestDestroyWritesItsEndAfterTheContextRanOut(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying' WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	spent, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second)) // terraform used up the destroy budget
	cancel()
	f.s.finishDestroy(spent, &Instance{ID: v.ID, Runtime: "local"}, "user")
	var st State
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1`, v.ID).Scan(&st)
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_events WHERE lab_id = $1 AND kind = 'destroyed'`, v.ID).Scan(&n)
	if st != Destroyed || n != 1 {
		t.Fatalf("state %s, %d destroyed events", st, n)
	}
}

func TestFailedAWSApplyShowsItsErrorWhileTerraformCleansUp(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.failProvision = errors.New("apply: quota exceeded")
	run.hold = make(chan struct{})
	id := f.approvedAWS(t)
	v := f.waitState(t, f.u, id, Destroying)
	var events string
	_ = f.s.DB.QueryRow(ctx, `SELECT coalesce(string_agg(kind || ' ' || detail, E'\n'), '') FROM lab_events WHERE lab_id = $1`, id).Scan(&events)
	if !strings.Contains(v.Error, "quota exceeded") || v.EndReason != "failed" || !strings.Contains(events, "failed apply: quota exceeded") {
		t.Fatalf("the trainee sees the error at once, before the destroy: %+v\n%s", v, events)
	}
	if again, err := f.s.recentlyApproved(ctx, f.u.ID, "forge", "forge-401", "01-cloud-heat"); err != nil || !again {
		t.Fatalf("still a recent failure while cleaning up: %v %v", again, err)
	}
	f.clk.Add(66 * time.Minute) // past the hung-provisioning threshold: the sweep must not take the failure over
	f.s.Sweep(ctx)
	close(run.hold)
	v = f.waitState(t, f.u, id, Failed)
	if !strings.Contains(v.Error, "quota exceeded") || run.destroyedN() != 1 {
		t.Fatalf("ends failed with its error after one destroy: %+v, %d destroys", v, run.destroyedN())
	}
}

func TestAWSLabEndedWhileProvisioningIsDestroyedOnce(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	release := make(chan struct{})
	run.provision = func() { <-release }
	run.failProvision = errors.New("apply interrupted") // AWSRunner.Destroy cancels a running apply
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Provisioning)
	if _, err := f.s.End(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Destroyed)
	close(release)
	for i := 0; i < 300; i++ {
		run.mu.Lock()
		n := len(run.labs)
		run.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // give provision's failure path time to (wrongly) destroy again
	v, _ := f.s.Get(ctx, f.u, id)
	if run.destroyedN() != 1 || v.State != Destroyed || v.EndReason != "user" {
		t.Fatalf("End's destroy is the only one: %d destroys, %+v", run.destroyedN(), v)
	}
}

func TestKillSwitchDoesNotWaitForTerraform(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	run.hold = make(chan struct{})
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	f.s.killAll(ctx) // returns while terraform destroy is held
	f.s.Sweep(ctx)   // the switch is still on: no second destroy
	f.waitState(t, f.u, id, Destroying)
	close(run.hold)
	v := f.waitState(t, f.u, id, Destroyed)
	f.settled(t, id)
	if v.EndReason != "kill_switch" || run.destroyedN() != 1 {
		t.Fatalf("%+v, %d destroys", v, run.destroyedN())
	}
}

// listCloud hands out credentials for any lab id and fails the test if it is asked to list anything.
type listCloud struct {
	awscloud.Cloud
	t *testing.T
}

func (listCloud) AssumeLab(context.Context, awscloud.Session) (awscloud.Credentials, error) {
	return awscloud.Credentials{}, nil
}
func (c listCloud) Tagged(context.Context, string, string) ([]awscloud.Resource, error) {
	c.t.Error("an empty lab id lists every lab's resources")
	return nil, nil
}

func TestTagSweepRefusesAnInvalidLabID(t *testing.T) {
	f := setup(t, true)
	f.withAWS(t)
	f.s.Cloud = listCloud{t: t}
	f.s.sweepLab(context.Background(), "")
}
