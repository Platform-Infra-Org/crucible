package labs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// SweepArgs is the periodic lab sweep: TTL and idle ends, hung provisioning, stuck destroys (later: schedule
// close, escalations, kill switch).
type SweepArgs struct{}

func (SweepArgs) Kind() string { return "lab_sweep" }

type SweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	S *Service
}

func (w *SweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	w.S.Sweep(ctx)
	return nil
}

// Timeout allows a sweep to destroy several labs: a local or cluster destroy is bounded at 2 minutes, and an aws
// destroy runs in the background (terraform takes minutes), so the sweep never waits for one.
func (w *SweepWorker) Timeout(*river.Job[SweepArgs]) time.Duration { return 15 * time.Minute }

// BudgetArgs is the periodic budget check (80% / hard-cap alerts).
type BudgetArgs struct{}

func (BudgetArgs) Kind() string { return "budget_check" }

type BudgetWorker struct {
	river.WorkerDefaults[BudgetArgs]
	S *Service
}

func (w *BudgetWorker) Work(ctx context.Context, _ *river.Job[BudgetArgs]) error {
	return w.S.CheckBudgets(ctx)
}

// ReapArgs is the AWS reaper; CostArgs is Cost Explorer ingestion. Both run every 6 h and at start (the AWS node
// sleeps at night, so "nightly" means "whenever it is up") and do nothing without aws labs.
type ReapArgs struct{}

func (ReapArgs) Kind() string { return "aws_reap" }

type ReapWorker struct {
	river.WorkerDefaults[ReapArgs]
	S *Service
}

func (w *ReapWorker) Work(ctx context.Context, _ *river.Job[ReapArgs]) error { return w.S.Reap(ctx) }
func (w *ReapWorker) Timeout(*river.Job[ReapArgs]) time.Duration             { return 30 * time.Minute }

type CostArgs struct{}

func (CostArgs) Kind() string { return "aws_costs" }

type CostWorker struct {
	river.WorkerDefaults[CostArgs]
	S *Service
}

func (w *CostWorker) Work(ctx context.Context, _ *river.Job[CostArgs]) error {
	return w.S.IngestCosts(ctx)
}
