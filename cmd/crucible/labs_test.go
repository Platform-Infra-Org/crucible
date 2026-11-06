package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"crucible/internal/db/dbtest"
	"crucible/internal/labs"
)

func TestLabsCommandsNeedADatabaseAndAKnownCommand(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var errw bytes.Buffer
	if code := runMain([]string{"labs", "bedtime"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "DATABASE_URL") {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	errw.Reset()
	if code := runMain([]string{"labs", "nap"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "crucible labs bedtime") {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
}

func TestLabsBedtimeWaitsUntilEveryLabIsAsleep(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	old := labsPoll
	labsPoll = time.Millisecond
	t.Cleanup(func() { labsPoll = old })
	if code := runMain([]string{"labs", "bedtime", "--wait", "50ms"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("no server carries it out here, so the wait runs out: exit %d", code)
	}
	if b, _ := labs.ReadBedtime(ctx, pool); b == nil || b.Phase != "settling" {
		t.Fatalf("the bedtime is requested: %+v", b)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runMain([]string{"labs", "bedtime", "--wait", "1s"}, &out, io.Discard); code != 0 || !strings.Contains(out.String(), "every AWS lab is asleep") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if code := runMain([]string{"labs", "bedtime", "--cancel"}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	if b, _ := labs.ReadBedtime(ctx, pool); b != nil {
		t.Fatalf("cancelled: %+v", b)
	}
}

func TestLabsStopAllTurnsTheKillSwitchOnAsTheTeardown(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	if code := runMain([]string{"labs", "stop-all", "--wait", "1s"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("no labs: done at once: exit %d", code)
	}
	var on bool
	var by string
	if err := pool.QueryRow(ctx, `SELECT enabled, changed_by FROM kill_switch`).Scan(&on, &by); err != nil || !on || by != labs.TeardownActor {
		t.Fatalf("%v %q %v", on, by, err)
	}
}
