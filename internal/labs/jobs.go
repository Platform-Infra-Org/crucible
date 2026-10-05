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

// Timeout allows a sweep to destroy several labs (each destroy is bounded at 2 minutes).
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
