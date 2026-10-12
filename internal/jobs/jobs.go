// Package jobs owns Crucible's River client (spec §3): retried deliveries and leader-elected periodic sweeps,
// stored in Postgres so they survive restarts.
package jobs

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Periodic is a job inserted every Every (and once at start) by the elected leader.
type Periodic struct {
	Every time.Duration
	Args  river.JobArgs
}

// RescueStuckAfter is how long a job may run before River retries it as stuck: above the longest worker timeout
// (the weekly price download's 2 h), so no job is run twice at once.
const RescueStuckAfter = 3 * time.Hour

// New builds a client working the default queue. Start it to work jobs; an unstarted client can still insert.
func New(pool *pgxpool.Pool, workers *river.Workers, periodic []Periodic, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	var pj []*river.PeriodicJob
	for _, p := range periodic {
		args := p.Args
		pj = append(pj, river.NewPeriodicJob(river.PeriodicInterval(p.Every),
			func() (river.JobArgs, *river.InsertOpts) { // one job per period, so a restart or new leader does not re-run it
				return args, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: p.Every}}
			},
			&river.PeriodicJobOpts{RunOnStart: true}))
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:               map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers:              workers,
		PeriodicJobs:         pj,
		Logger:               log,
		RescueStuckJobsAfter: RescueStuckAfter,
	})
}
