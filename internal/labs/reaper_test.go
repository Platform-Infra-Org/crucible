package labs

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/notify"
)

// awsRow inserts a Forge 401 lab row; endedAgo > 0 marks it destroyed that long ago.
func (f *fx) awsRow(t *testing.T, st State, endedAgo time.Duration) string {
	t.Helper()
	ctx, now := context.Background(), f.clk.Now()
	in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-401", Module: "01-cloud-heat", SHA: "abc",
		Runtime: "aws", State: st, CreatedAt: now.Add(-3 * time.Hour), LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "approver"}
	if err := f.s.insert(ctx, in); err != nil {
		t.Fatal(err)
	}
	if endedAgo > 0 {
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET destroyed_at = $2 WHERE id = $1`, in.ID, now.Add(-endedAgo)); err != nil {
			t.Fatal(err)
		}
	}
	return in.ID
}

func (n *fakeNotifier) count(kind notify.Kind) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, ev := range n.events {
		if ev.Kind == kind {
			c++
		}
	}
	return c
}

func vol(id string) string { return "arn:aws:ec2:eu-west-1:000000000000:volume/vol-" + id }

func (f *fx) findings(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	rows, err := f.s.DB.Query(context.Background(), `SELECT arn, source || '/' || action FROM reaper_findings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var arn, what string
		_ = rows.Scan(&arn, &what)
		got[arn] = what
	}
	return got
}

func TestReaperDeletesOnlyEndedKnownLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	live, ended, recent := f.awsRow(t, Ready, 0), f.awsRow(t, Destroyed, 2*time.Hour), f.awsRow(t, Destroyed, 10*time.Minute)
	// reconcileCluster deletes an orphaned aws lab namespace without terraform destroy: a failed lab, no destroyed_at.
	orphan := f.awsRow(t, Failed, 0)
	unknown := "cccccccccccc"
	for _, id := range []string{live, ended, recent, orphan, unknown} {
		cloud.Add("eu-west-1", awscloud.Resource{ARN: vol(id), LabID: id})
	}
	cloud.Add("eu-west-1", awscloud.Resource{ARN: vol("weird"), LabID: `x"; Deny`})
	cloud.AddEvent(awscloud.TrailEvent{ID: "ev-1", At: f.clk.Now().Add(-time.Hour), LabID: live, Event: "CreateVolume", Resources: []string{"vol-9"}})

	dead, cancel := context.WithCancel(ctx)
	cancel()
	_ = f.s.Reap(dead)
	if !cloud.Has(vol(ended)) {
		t.Fatal("without the database the reaper must not guess")
	}

	if err := f.s.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	for id, kept := range map[string]bool{live: true, recent: true, unknown: true, "weird": true, ended: false, orphan: false} {
		if cloud.Has(vol(id)) != kept {
			t.Fatalf("%s: kept=%v, want %v", id, cloud.Has(vol(id)), kept)
		}
	}
	want := map[string]string{vol(ended): "reaper/deleted", vol(orphan): "reaper/deleted", vol(unknown): "reaper/reported",
		vol("weird"): "reaper/reported", "cloudtrail:ev-1": "trail/reported"}
	if got := f.findings(t); !maps.Equal(got, want) {
		t.Fatalf("findings:\n got %v\nwant %v", got, want)
	}
	ev := f.notes.last(notify.ReaperReport)
	if ev == nil || !slices.Contains(ev.To, "admin@crucible.local") || ev.Link != "/ledger" || ev.Team != "" {
		t.Fatalf("admins get one summary, never a team channel: %+v", ev)
	}
	if f.notes.count(notify.ReaperReport) != 1 {
		t.Fatal("one summary per run")
	}
	_ = f.s.Reap(ctx)
	if f.notes.count(notify.ReaperReport) != 1 {
		t.Fatal("the same findings again are not news")
	}
	var okAt *time.Time
	_ = f.s.DB.QueryRow(ctx, `SELECT reap_ok_at FROM aws_ops`).Scan(&okAt)
	if okAt == nil {
		t.Fatal("a clean run is recorded for the Ledger")
	}
}

func TestReaperRetriesNotYetAndSurfacesTrailTruncation(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	ended := f.awsRow(t, Destroyed, 2*time.Hour)
	cloud.Add("eu-west-1", awscloud.Resource{ARN: vol(ended), LabID: ended})
	cloud.NotYet(vol(ended), 1)
	cloud.TrailTruncated = true

	_ = f.s.Reap(ctx)
	if !cloud.Has(vol(ended)) || len(f.findings(t)) != 0 || f.notes.count(notify.ReaperReport) != 0 {
		t.Fatalf("a volume still in use is neither deleted nor a finding: %v", f.findings(t))
	}
	var okAt *time.Time
	var msg string
	_ = f.s.DB.QueryRow(ctx, `SELECT reap_ok_at, reap_error FROM aws_ops`).Scan(&okAt, &msg)
	if okAt != nil || !strings.Contains(msg, awscloud.ErrTruncated.Error()) {
		t.Fatalf("a truncated CloudTrail lookup makes the run incomplete: %v %q", okAt, msg)
	}

	cloud.TrailTruncated = false
	_ = f.s.Reap(ctx)
	if cloud.Has(vol(ended)) || f.findings(t)[vol(ended)] != "reaper/deleted" {
		t.Fatal("the next run deletes it")
	}

	// A resource that never frees up (an object-locked bucket) is a failed finding a day after the lab ended, once.
	old := f.awsRow(t, Destroyed, 23*time.Hour)
	cloud.Add("eu-west-1", awscloud.Resource{ARN: vol(old), LabID: old})
	cloud.NotYet(vol(old), 100)
	_ = f.s.Reap(ctx)
	if _, ok := f.findings(t)[vol(old)]; ok {
		t.Fatal("not yet within a day is a quiet retry")
	}
	f.clk.Add(2 * time.Hour)
	before := f.notes.count(notify.ReaperReport)
	_ = f.s.Reap(ctx)
	_ = f.s.Reap(ctx)
	if f.findings(t)[vol(old)] != "reaper/failed" || f.notes.count(notify.ReaperReport) != before+1 {
		t.Fatalf("stuck a day: one failed finding, one email: %v", f.findings(t))
	}
	_ = f.s.DB.QueryRow(ctx, `SELECT reap_ok_at, reap_error FROM aws_ops`).Scan(&okAt, &msg)
	if okAt == nil || msg != "" {
		t.Fatalf("a clean run clears the error: %v %q", okAt, msg)
	}
}

func TestIngestCostsUpsertsAndMarksFailures(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) // the clock is 2026-10-05 09:00 UTC
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "aaaaaaaaaaaa", USD: 0.42})
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "not-a-lab", USD: 9})
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "bbbbbbbbbbbb", USD: -3})                  // a credit: never negative
	cloud.AddCost(awscloud.DailyCost{Day: day.AddDate(0, -1, 0), LabID: "aaaaaaaaaaaa", USD: 5}) // outside the window
	total := func() (v float64) {
		_ = f.s.DB.QueryRow(ctx, `SELECT coalesce(sum(usd), 0) FROM cost_actuals`).Scan(&v)
		return v
	}
	if err := f.s.IngestCosts(ctx); err != nil || total() != 0.42 {
		t.Fatalf("one valid row in the window: %v %v", total(), err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM cost_actuals WHERE lab_id = 'bbbbbbbbbbbb' AND usd = 0`).Scan(&n)
	if n != 1 {
		t.Fatal("a credit is stored as $0")
	}
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "aaaaaaaaaaaa", USD: 0.5}) // Cost Explorer revised the day
	_ = f.s.IngestCosts(ctx)
	if total() != 0.5 {
		t.Fatalf("a revised day replaces the old amount: %v", total())
	}
	okBefore := f.clk.Now()
	f.clk.Add(time.Hour)
	cloud.Err = errors.New("Cost Explorer is down")
	if err := f.s.IngestCosts(ctx); err != nil {
		t.Fatalf("a failed ingestion is recorded, not retried in a storm: %v", err)
	}
	var okAt time.Time
	var msg string
	_ = f.s.DB.QueryRow(ctx, `SELECT ingest_ok_at, ingest_error FROM aws_ops`).Scan(&okAt, &msg)
	if total() != 0.5 || !okAt.Equal(okBefore) || msg != "Cost Explorer is down" {
		t.Fatalf("old rows kept, last success kept, error shown: %v %v %q", total(), okAt, msg)
	}
}
