package labs

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"crucible/internal/apperr"
	"github.com/riverqueue/river"

	"crucible/internal/awscloud"
	"crucible/internal/jobs"
)

func TestLedgerScopes(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	_ = f.s.finding(ctx, "reaper", "", "arn:aws:ec2:eu-west-1:1:volume/vol-x", "reported", "check it")
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_error = 'AccessDenied', ingest_error_at = $1`, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ledger(ctx, f.u); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees never see the Ledger: %v", err)
	}
	l, err := f.s.Ledger(ctx, f.leader)
	if err != nil || len(l.Teams) != 1 || l.Teams[0].ID != "forge" || l.Findings != nil || l.ActualsError != "" || l.CanRefresh {
		t.Fatalf("leaders see their team's spend only: %+v %v", l, err)
	}
	a, _ := f.s.Ledger(ctx, f.admin)
	if len(a.Findings) != 1 || a.ActualsError != "AccessDenied" || !a.CanRefresh || !a.ActualsStale || !a.ReaperStale {
		t.Fatalf("admins see findings, errors and the refresh button: %+v", a)
	}
	if _, err := f.s.RefreshFinOps(ctx, f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("refresh is admin-only: %v", err)
	}
}

func TestLedgerNumbersAndStaleness(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	now := f.clk.Now()
	f.spent(t, "aaaaaaaaaaa1", "forge-101", 2) // est $2, will settle at $0.40
	f.endedAgo(t, "aaaaaaaaaaa1", 72*time.Hour)
	f.spent(t, "aaaaaaaaaaa2", "forge-101", 1) // est $1, Cost Explorer never reported it
	run := &Instance{ID: "aaaaaaaaaaa3", UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: "02-first-lab", SHA: "abc",
		Runtime: "local", State: Ready, CreatedAt: now, LastActivityAt: now, TTL: 2 * time.Hour, IdleTimeout: 30 * time.Minute,
		Tier: "approver", HourlyUSD: 1, EstimateUSD: 2}
	if err := f.s.insert(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ready_at = $2, ends_at = $3 WHERE id = $1`, run.ID, now.Add(-30*time.Minute), now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ('aaaaaaaaaaa1', $1::timestamptz::date, 0.4, $1::timestamptz)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, now); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Ledger(ctx, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if l.ActualsStale || len(l.Running) != 1 || l.Running[0].ID != run.ID || !closeTo(l.Running[0].CostUSD, 0.5) {
		t.Fatalf("running lab, cost so far $0.50, actuals fresh: %+v", l)
	}
	if len(l.Labs) != 3 || l.Labs[0].ID != "aaaaaaaaaaa2" || l.Labs[2].ID != "aaaaaaaaaaa1" || !l.Labs[2].Settled || *l.Labs[2].ActualUSD != 0.4 {
		t.Fatalf("most expensive first, using settled actuals ($1 > $0.50 > $0.40): %+v", l.Labs)
	}
	if l.Accuracy.Labs != 1 || !closeTo(l.Accuracy.EstimateUSD, 2) || !closeTo(l.Accuracy.ActualUSD, 0.4) {
		t.Fatalf("estimate vs actual over settled labs: %+v", l.Accuracy)
	}
	if len(l.TopSpenders) != 1 || l.TopSpenders[0].Requester != "trainee@crucible.local" || !closeTo(l.TopSpenders[0].USD, 1.9) {
		t.Fatalf("top spenders: %+v", l.TopSpenders)
	}
	today := l.Daily[len(l.Daily)-1]
	if today.Day != now.Format(time.DateOnly) || !closeTo(today.ActualUSD, 0.4) || len(l.Daily) != now.Day() {
		t.Fatalf("one entry per day this month, actuals on Cost Explorer's day: %+v", l.Daily)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, now.Add(-40*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.s.Ledger(ctx, f.admin); !l.ActualsStale {
		t.Fatal("no successful ingestion for 36 hours: stale")
	}
}

func TestRefreshQueuesUniqueJobsRateLimitedAndAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	if _, err := f.s.RefreshFinOps(ctx, f.admin); err == nil {
		t.Fatal("no queue: refresh must fail, not pretend")
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &CostWorker{S: f.s})
	river.AddWorker(workers, &ReapWorker{S: f.s})
	client, err := jobs.New(f.s.DB, workers, nil, slog.Default()) // never started: insert only
	if err != nil {
		t.Fatal(err)
	}
	f.s.Jobs = client
	if _, err := f.s.RefreshFinOps(ctx, f.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RefreshFinOps(ctx, f.admin); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a second click right away is rate limited: %v", err)
	}
	f.clk.t = f.clk.t.Add(time.Minute)
	if _, err := f.s.RefreshFinOps(ctx, f.admin); err != nil {
		t.Fatal(err)
	}
	var n, audits int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind IN ('aws_costs', 'aws_reap')`).Scan(&n)
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'finops.refresh' AND actor = 'admin@crucible.local'`).Scan(&audits)
	if n != 2 || audits != 2 {
		t.Fatalf("jobs stay unique while queued (got %d), both refreshes audited (got %d)", n, audits)
	}
}
