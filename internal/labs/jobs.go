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
