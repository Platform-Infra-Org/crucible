package labs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"crucible/internal/awscloud"
	"crucible/internal/content"
	"crucible/internal/pricing"
)

func stateWith(res ...string) []byte { // res: "aws_instance.web" or "aws_nat_gateway.gw"
	var b strings.Builder
	b.WriteString(`{"version":4,"resources":[`)
	for i, r := range res {
		typ, name, _ := strings.Cut(r, ".")
		attrs := `{}`
		if typ == "aws_instance" { // a real state always says whether the instance has a public IPv4
			attrs = `{"instance_type":"t3.nano","associate_public_ip_address":false,"root_block_device":[{"volume_size":8,"volume_type":"gp3"}]}`
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"mode":"managed","type":%q,"name":%q,"instances":[{"attributes":%s}]}`, typ, name, attrs)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func openRows(t *testing.T, f *fx, labID string) map[string]float64 {
	rows, err := f.s.DB.Query(context.Background(), `SELECT address, hourly_usd FROM lab_resources WHERE lab_id = $1 AND ended_at IS NULL`, labID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	var a string
	var h float64
	if _, err := pgx.ForEachRow(rows, []any{&a, &h}, func() error { out[a] = h; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// meterFixture: a ready aws lab in eu-west-1 (estimate $1/h, ready now) on a real AWSRunner over a fake cluster whose
// terraform pods end with apply, the Fake as its cloud, and eu-west-1 list prices.
func meterFixture(t *testing.T, apply corev1.PodPhase) (*fx, *awscloud.Fake, *Instance, *content.Lab) {
	t.Helper()
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	cs := fake.NewClientset()
	kubelet(cs, apply, "terraform "+string(apply))
	ar, cloud, _ := testAWS(cs)
	cloud.Now, ar.Now, ar.StateBucket = f.clk.Now, f.clk.Now, "state"
	f.s.Runners["aws"] = ar
	f.s.Prices = &pricing.Store{DB: f.s.DB, Now: f.clk.Now}
	if err := f.s.Prices.Replace(ctx, []pricing.Row{
		{Service: "aws", Region: "eu-west-1", Key: "ec2:t3.nano", Unit: "Hrs", USD: 0.0052},
		{Service: "aws", Region: "eu-west-1", Key: "ebs:gp3", Unit: "GB-Mo", USD: 0.088},
		{Service: "aws", Region: "eu-west-1", Key: "nat:hour", Unit: "Hrs", USD: 0.048},
		{Service: "aws", Region: "eu-west-1", Key: "ipv4:hour", Unit: "Hrs", USD: 0.005},
	}); err != nil {
		t.Fatal(err)
	}
	t0 := f.clk.Now()
	inst := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-401", Module: "01-cloud-heat", SHA: "abc",
		Runtime: "aws", State: Ready, CreatedAt: t0, ReadyAt: &t0, LastActivityAt: t0, TTL: 24 * time.Hour,
		IdleTimeout: 24 * time.Hour, HourlyUSD: 1, EstimateUSD: 24, Tier: "approver"}
	if err := f.s.insert(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ready_at = $2, ends_at = $3 WHERE id = $1`,
		inst.ID, t0, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return f, cloud, inst, cloudHeat(t)
}

func TestMeterRecordsStateAndEndsOnDestroy(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodSucceeded) // inst is a ready aws lab in eu-west-1
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil {
		t.Fatal(err)
	}
	want := 0.0052 + 8*0.088/730
	if r := openRows(t, f, inst.ID); len(r) != 1 || math.Abs(r["aws_instance.web"]-want) > 1e-12 {
		t.Fatalf("%v", r)
	}
	if err := f.s.Meter(ctx, inst, lab); err != nil || len(openRows(t, f, inst.ID)) != 1 {
		t.Fatalf("metering twice adds nothing: %v", err)
	}
	fake.SetState(inst.ID, stateWith()) // the instance is gone from the state
	if err := f.s.Meter(ctx, inst, lab); err != nil || len(openRows(t, f, inst.ID)) != 0 {
		t.Fatalf("a resource gone from the state ends: %v", err)
	}
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	_ = f.s.Meter(ctx, inst, lab)
	if err := f.s.EndMeter(ctx, inst.ID, f.clk.Now()); err != nil || len(openRows(t, f, inst.ID)) != 0 {
		t.Fatalf("EndMeter ends everything: %v", err)
	}
}

func TestPartialApplyIsMetered(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodFailed) // runTF("apply") reports failure
	fake.SetState(inst.ID, stateWith("aws_nat_gateway.gw")) // the apply failed after creating the NAT gateway
	err := f.s.provisionLab(ctx, f.s.Runners["aws"].(*AWSRunner), inst, lab)
	if err == nil {
		t.Fatal("the apply failed")
	}
	if r := openRows(t, f, inst.ID); math.Abs(r["aws_nat_gateway.gw"]-(0.048+0.005)) > 1e-12 {
		t.Fatalf("what the failed apply created is metered: %v", r)
	}
}

func TestMeteredSpendReplacesTheEstimate(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodSucceeded) // inst.HourlyUSD = 1.00 (the estimate), ready at t0
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(10 * time.Hour)
	sp, err := f.s.spend(ctx, f.plat, inst.Team, inst.Training)
	if err != nil {
		t.Fatal(err)
	}
	want := 10 * (0.0052 + 8*0.088/730) // metered, not 10 h × the $1 estimate
	if math.Abs(sp.SpentUSD-want) > 1e-9 {
		t.Fatalf("spent %v, want %v", sp.SpentUSD, want)
	}
}

func TestMeterFailureKeepsTheEstimate(t *testing.T) {
	ctx := context.Background()
	f, _, inst, lab := meterFixture(t, corev1.PodSucceeded) // the apply wrote no state: metering fails
	if err := f.s.provisionLab(ctx, f.s.Runners["aws"].(*AWSRunner), inst, lab); err != nil {
		t.Fatalf("metering never fails the lab: %v", err)
	}
	f.clk.Add(10 * time.Hour)
	sp, err := f.s.spend(ctx, f.plat, inst.Team, inst.Training)
	if err != nil || math.Abs(sp.SpentUSD-10) > 1e-9 {
		t.Fatalf("without metered rows spend stays 10 h × the $1 estimate: %v %v", sp.SpentUSD, err)
	}
}

func TestMeterIgnoresALabAlreadyEnding(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodSucceeded)
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', destroyed_at = $2 WHERE id = $1`, inst.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Meter(ctx, inst, lab); err != nil || len(openRows(t, f, inst.ID)) != 0 {
		t.Fatalf("a cancelled apply metered after its destroy began would never end: %v", err)
	}
}

func TestEndingTheLabEndsTheMeter(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodSucceeded)
	fake.SetState(inst.ID, stateWith("aws_instance.web", "aws_nat_gateway.gw"))
	if err := f.s.provisionLab(ctx, f.s.Runners["aws"].(*AWSRunner), inst, lab); err != nil {
		t.Fatal(err)
	}
	if len(openRows(t, f, inst.ID)) != 2 {
		t.Fatal("a successful apply is metered")
	}
	f.clk.Add(2 * time.Hour)
	if _, err := f.s.End(ctx, f.u, inst.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, inst.ID, Destroyed)
	if r := openRows(t, f, inst.ID); len(r) != 0 {
		t.Fatalf("terraform destroy ends the meter: %v", r)
	}
	var hours float64
	_ = f.s.DB.QueryRow(ctx, `SELECT extract(epoch FROM max(ended_at) - min(started_at)) / 3600 FROM lab_resources WHERE lab_id = $1`, inst.ID).Scan(&hours)
	if hours != 2 {
		t.Fatalf("metered from the apply to the destroy: %v h", hours)
	}
}

func TestReaperEndsTheMeterOfEndedLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	live, ended, recent, leftover := f.awsRow(t, Ready, 0), f.awsRow(t, Destroyed, 2*time.Hour), f.awsRow(t, Destroyed, 10*time.Minute),
		f.awsRow(t, Destroyed, 2*time.Hour)
	cloud.Add("eu-west-1", awscloud.Resource{ARN: vol(leftover), LabID: leftover})
	cloud.NotYet(vol(leftover), 1)                               // still attached: the first delete is refused
	for _, id := range []string{live, ended, recent, leftover} { // terraform destroy failed: the meters stayed open
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_resources VALUES ($1, 'aws_instance.web', 'aws_instance', 0.01, 0, $2, NULL)`,
			id, f.clk.Now().Add(-3*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	open := func(id string) bool { return len(openRows(t, f, id)) == 1 }
	f.s.AWSRegions = nil
	_ = f.s.Reap(ctx)
	if !open(ended) {
		t.Fatal("no region listed is no complete run: the meter stays open")
	}
	f.s.AWSRegions = []string{"eu-west-1"}
	cloud.Err = errors.New("throttled")
	_ = f.s.Reap(ctx)
	if !open(ended) {
		t.Fatal("without the inventory the reaper can't know nothing is left: the meter stays open")
	}
	cloud.Err = nil
	_ = f.s.Reap(ctx)
	for id, want := range map[string]bool{live: true, recent: true, ended: false, leftover: true} {
		if open(id) != want {
			t.Fatalf("lab %s: meter open = %v, want %v (a leftover AWS refused to delete still bills)", id, !want, want)
		}
	}
	_ = f.s.Reap(ctx) // deletes the leftover: listed in this run, so its meter still runs
	if cloud.Has(vol(leftover)) || !open(leftover) {
		t.Fatal("a lab listed in this run keeps its meter")
	}
	_ = f.s.Reap(ctx)
	if open(leftover) {
		t.Fatal("the first complete run that lists nothing for the lab ends its meter")
	}
}

func TestDestroyWithoutTerraformKeepsTheMeter(t *testing.T) {
	ctx := context.Background()
	f, cloud, inst, lab := meterFixture(t, corev1.PodSucceeded)
	cloud.SetState(inst.ID, stateWith("aws_instance.web"))
	ar := f.s.Runners["aws"].(*AWSRunner)
	if err := f.s.provisionLab(ctx, ar, inst, lab); err != nil {
		t.Fatal(err)
	}
	// the namespace is already terminating: terraform destroy can't run, so everything it created still runs
	ar.Cluster.Client.(*fake.Clientset).PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 403,
			Reason: metav1.StatusReasonForbidden, Details: &metav1.StatusDetails{
				Causes: []metav1.StatusCause{{Type: corev1.NamespaceTerminatingCause}}}}}
	})
	if err := f.s.destroyRuntime(ctx, inst); err != nil {
		t.Fatalf("as before, a lab whose namespace is going away counts as destroyed: %v", err)
	}
	if len(openRows(t, f, inst.ID)) != 1 {
		t.Fatal("terraform destroy never ran: the meter runs until the reaper has cleaned up")
	}
}

// Trainees run terraform from the workspace terminal: the sweep meters each ready aws lab again every 15 minutes.
func TestReadyLabsAreMeteredAgain(t *testing.T) {
	ctx := context.Background()
	f, fake, inst, lab := meterFixture(t, corev1.PodSucceeded)
	fake.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil { // the apply's meter
		t.Fatal(err)
	}
	fake.SetState(inst.ID, stateWith("aws_instance.web", "aws_nat_gateway.gw")) // the trainee applies more by hand
	f.clk.Add(14 * time.Minute)
	f.s.Sweep(ctx)
	if r := openRows(t, f, inst.ID); len(r) != 1 {
		t.Fatalf("at most once every 15 minutes per lab: %v", r)
	}
	f.clk.Add(time.Minute)
	f.s.Sweep(ctx)
	if r := openRows(t, f, inst.ID); len(r) != 2 {
		t.Fatalf("what the trainee added is metered on the next sweep after 15 minutes: %v", r)
	}
}

// A ready lab's committed figure is what it has spent (metered) plus its hourly estimate for the time it has left.
func TestCommittedNeverSitsBelowSpent(t *testing.T) {
	ctx := context.Background()
	f, _, inst, _ := meterFixture(t, corev1.PodSucceeded) // $1/h estimate, ready at t0, ends at t0+24h
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_resources VALUES ($1, 'aws_nat_gateway.big', 'aws_nat_gateway', 20, 0, $2, NULL)`,
		inst.ID, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(2 * time.Hour)
	sp, err := f.s.spend(ctx, f.plat, inst.Team, inst.Training)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(sp.SpentUSD-40) > 1e-9 || math.Abs(sp.CommittedUSD-(40+22)) > 1e-9 {
		t.Fatalf("spent %v (want 40), committed %v (want 40 spent + 22 h × $1)", sp.SpentUSD, sp.CommittedUSD)
	}
}
