package jobs

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"crucible/internal/db/dbtest"
)

type pingArgs struct {
	N int `json:"n"`
}

func (pingArgs) Kind() string { return "test_ping" }

type pingWorker struct {
	river.WorkerDefaults[pingArgs]
	got chan int
}

func (w *pingWorker) Work(_ context.Context, j *river.Job[pingArgs]) error {
	w.got <- j.Args.N
	return nil
}

func TestPeriodicAndInsertedJobsRun(t *testing.T) {
	pool := dbtest.New(t) // db.Open ran the River migrations too
	w := &pingWorker{got: make(chan int, 16)}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	c, err := New(pool, workers, []Periodic{{Every: time.Hour, Args: pingArgs{N: 1}}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	if _, err := c.Insert(ctx, pingArgs{N: 2}, nil); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	timeout := time.After(30 * time.Second) // periodic jobs wait for leader election
	for !seen[1] || !seen[2] {
		select {
		case n := <-w.got:
			seen[n] = true
		case <-timeout:
			t.Fatalf("jobs did not run: %v", seen)
		}
	}
}
