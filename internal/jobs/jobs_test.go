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

// A restart (a new client, a new leader) must not re-run a periodic job that already ran this period.
func TestPeriodicJobNotRepeatedByRestart(t *testing.T) {
	pool := dbtest.New(t)
	w := &pingWorker{got: make(chan int, 16)}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	run := func() {
		c, err := New(pool, workers, []Periodic{{Every: time.Hour, Args: pingArgs{N: 7}}}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := c.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Stop(context.Background()) }()
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			var id string
			if err := pool.QueryRow(ctx, `SELECT leader_id FROM river_leader`).Scan(&id); err == nil && id == c.ID() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("never elected")
			}
		}
		time.Sleep(4 * time.Second) // the start-up insert follows election, not instantly
	}
	count := func() (n int) {
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'test_ping'`).Scan(&n)
		return n
	}
	run()
	if count() != 1 {
		t.Fatalf("the first start inserts the job: %d", count())
	}
	run()
	if n := count(); n != 1 {
		t.Fatalf("a second start in the same period inserts nothing: %d", n)
	}
}
