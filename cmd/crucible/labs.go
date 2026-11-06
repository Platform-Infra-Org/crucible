package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"crucible/internal/db"
	"crucible/internal/labs"
)

const labsUsage = `usage:
  crucible labs bedtime [--wait DURATION] [--cancel]
  crucible labs stop-all [--wait DURATION]
  crucible labs status

These run where the server's database is (DATABASE_URL): the nightly CronJob, and crucible aws sleep / teardown over SSM.
bedtime asks the server to put every AWS lab to sleep (labs starting get 10 minutes); --wait waits until they are
asleep, --cancel ends the bedtime. stop-all turns the kill switch on as crucible aws teardown and, with --wait, waits
until every lab has ended. status prints the bedtime and the AWS labs by state.`

// labsPoll is how often --wait looks again (a var for tests).
var labsPoll = 5 * time.Second

func labsCmd(args []string, out, errw io.Writer) int {
	if len(args) == 0 || (args[0] != "bedtime" && args[0] != "stop-all" && args[0] != "status") {
		fmt.Fprintln(errw, labsUsage)
		return 2
	}
	fs := flag.NewFlagSet("labs "+args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	wait := fs.Duration("wait", 0, "wait this long for the server to finish")
	cancel := fs.Bool("cancel", false, "bedtime: end it (labs may start again)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(errw, "crucible labs needs DATABASE_URL (it runs in the server's pod)")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, url)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	defer pool.Close()
	// until polls left until it is empty or the wait runs out; it prints what is left when it ran out.
	until := func(left func() ([]string, error), done string) int {
		deadline := time.Now().Add(*wait)
		for {
			l, err := left()
			if err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
			if len(l) == 0 {
				fmt.Fprintln(out, done)
				return 0
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(errw, "still waiting after %s:\n  %s\n", *wait, strings.Join(l, "\n  "))
				return 1
			}
			select {
			case <-ctx.Done():
				return 1
			case <-time.After(labsPoll):
			}
		}
	}
	switch args[0] {
	case "bedtime":
		if *cancel {
			if err := labs.CancelBedtime(ctx, pool); err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
			fmt.Fprintln(out, "bedtime cancelled: labs may start again")
			return 0
		}
		if err := labs.RequestBedtime(ctx, pool, "crucible labs bedtime", time.Now()); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		if *wait == 0 {
			fmt.Fprintln(out, "bedtime requested: the server puts AWS labs to sleep")
			return 0
		}
		return until(func() ([]string, error) {
			b, err := labs.ReadBedtime(ctx, pool)
			if err != nil || b == nil || b.Phase == "asleep" {
				return nil, err
			}
			l, err := labs.AwakeAWSLabs(ctx, pool)
			if len(l) == 0 && err == nil {
				l = []string{"the server (phase " + b.Phase + ")"}
			}
			return l, err
		}, "every AWS lab is asleep")
	case "stop-all":
		if err := labs.StopAll(ctx, pool, labs.TeardownActor, time.Now()); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		return until(func() ([]string, error) { return labs.ActiveLabs(ctx, pool) }, "every lab has ended")
	default: // status
		b, err := labs.ReadBedtime(ctx, pool)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		phase := "none"
		if b != nil {
			phase = b.Phase + " since " + b.RequestedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(out, "bedtime: %s\n", phase)
		rows, err := pool.Query(ctx, `SELECT state, count(*) FROM lab_instances WHERE runtime = 'aws'
			AND state IN ('provisioning', 'ready', 'asleep', 'waking', 'destroying') GROUP BY state ORDER BY state`)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		defer rows.Close()
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				fmt.Fprintf(out, "aws labs %s: %d\n", st, n)
			}
		}
		if p := os.Getenv("CRUCIBLE_NODE_STORAGE"); p != "" {
			var st syscall.Statfs_t
			if syscall.Statfs(p, &st) == nil {
				fmt.Fprintf(out, "node disk free: %.1f GiB\n", float64(int64(st.Bavail)*int64(st.Bsize))/(1<<30))
			}
		}
		return 0
	}
}
