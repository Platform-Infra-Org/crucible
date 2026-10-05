package labs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type fakeRunner struct {
	mu          sync.Mutex
	unavailable error
	scripts     []ScriptSpec
	exit        func(ScriptSpec) int
	destroyed   []string
}

func (f *fakeRunner) Available(*Instance) error                                  { return f.unavailable }
func (f *fakeRunner) Provision(context.Context, *Instance, []byte, string) error { return nil }
func (f *fakeRunner) OpenPTY(context.Context, *Instance, string, int, int) (PTY, error) {
	return nil, io.EOF
}
func (f *fakeRunner) RunScript(_ context.Context, _ *Instance, sp ScriptSpec) (ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, sp)
	code := 0
	if f.exit != nil {
		code = f.exit(sp)
	}
	return ScriptResult{ExitCode: code, Output: "out"}, nil
}
func (f *fakeRunner) Destroy(_ context.Context, in *Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, in.ID)
	return nil
}

type fx struct {
	s     *Service
	run   *fakeRunner
	clk   *clock
	u     *auth.User
	other *auth.User
}

func setup(t *testing.T, unlock bool) *fx {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-101@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}}
	ls := &learn.Service{DB: pool, State: func() *gitsync.State { return st }}
	store := auth.Store{DB: pool}
	u, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	other, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	if unlock {
		_ = ls.SetItem(ctx, u.ID, "forge", "forge-101", "01-welcome", "how-we-work", "complete", 1)
		_ = ls.SetItem(ctx, u.ID, "forge", "forge-101", "01-welcome", "quiz", "complete", 1)
	}
	run := &fakeRunner{}
	clk := &clock{t: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)}
	s := &Service{DB: pool, Learn: ls, Runners: map[string]Runner{"local": run}, Now: clk.Now, Log: slog.Default()}
	return &fx{s: s, run: run, clk: clk, u: u, other: other}
}

func (f *fx) start(t *testing.T) *View {
	t.Helper()
	v, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		got, _ := f.s.Get(context.Background(), f.u, v.ID)
		if got.State == Ready {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lab never became ready")
	return nil
}

func statusOf(v *View, task string) string {
	for _, tv := range v.Tasks {
		if tv.ID == task {
			return tv.Status
		}
	}
	return ""
}

func TestStartIsIdempotentAndBecomesReady(t *testing.T) {
	f := setup(t, true)
	v := f.start(t)
	again, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab")
	if err != nil || again.ID != v.ID {
		t.Fatalf("second start must return the same lab: %v %v", again, err)
	}
	if v.EndsAt == nil || !v.EndsAt.Equal(f.clk.Now().Add(time.Hour)) || v.LimitReason != "ttl" || v.IdleWarningS != 300 {
		t.Fatalf("timer fields: ends %v reason %q warn %d", v.EndsAt, v.LimitReason, v.IdleWarningS)
	}
	if statusOf(v, "t1-forge-file") != "open" || statusOf(v, "t2-find-port") != "locked" || !v.SelfReported {
		t.Fatalf("task statuses: %+v", v.Tasks)
	}
}

func TestStartGuards(t *testing.T) {
	f := setup(t, false)
	if _, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Locked) {
		t.Fatalf("locked module: %v", err)
	}
	f = setup(t, true)
	f.run.unavailable = apperr.Wrap(apperr.Unavailable, "your laptop agent is not connected")
	if _, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("offline agent: %v", err)
	}
	var n int
	_ = f.s.DB.QueryRow(context.Background(), "SELECT count(*) FROM lab_instances").Scan(&n)
	if n != 0 {
		t.Fatal("no lab row may be created when the runtime is unavailable")
	}
}

func TestFullLabFlow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.run.exit = func(sp ScriptSpec) int {
		if a, ok := sp.Env["CRUCIBLE_ANSWER"]; ok && a != "8081" {
			return 1
		}
		return 0
	}
	v := f.start(t)

	res, err := f.s.Check(ctx, f.u, v.ID, "t1-forge-file", "")
	if err != nil || !res.Passed || res.Awarded != 2 {
		t.Fatalf("t1: %+v %v", res, err)
	}
	if last := f.run.scripts[len(f.run.scripts)-1]; last.Service != "shell" || !strings.Contains(string(last.Script), "hello forge") {
		t.Fatalf("t1 check ran wrong script: %+v", last)
	}

	d, err := f.s.OpenTask(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || d.SetupError != "" || !strings.Contains(d.Instructions, "Something broke") {
		t.Fatalf("open t2: %+v %v", d, err)
	}
	if last := f.run.scripts[len(f.run.scripts)-1]; last.Service != "web" || !strings.Contains(string(last.Script), "8081") {
		t.Fatalf("setup script not run in web: %+v", last)
	}
	if res, _ := f.s.Check(ctx, f.u, v.ID, "t2-find-port", "9999"); res.Passed {
		t.Fatal("wrong terminal-quiz answer passed")
	}
	if res, _ := f.s.Check(ctx, f.u, v.ID, "t2-find-port", "8081"); !res.Passed {
		t.Fatal("right terminal-quiz answer failed")
	}

	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx"); err != nil {
		t.Fatal(err)
	}
	h, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx")
	if err != nil || h.Cost != 1.5 || !strings.Contains(h.Text, "nginx -s reload") {
		t.Fatalf("second hint: %+v %v", h, err)
	}
	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("no more hints: %v", err)
	}
	res, _ = f.s.Check(ctx, f.u, v.ID, "t3-fix-nginx", "")
	// first hint costs the lab's hint_cost (0.25), second 1.5 → 3 − 1.75 = 1.25
	if !res.Passed || res.Awarded != 1.25 || !res.Lab.Complete || res.Lab.Score != 4.25 {
		t.Fatalf("t3: %+v lab %+v", res, res.Lab)
	}
	// the module also has a reading item; the lab alone does not finish it
	_ = f.s.Learn.SetItem(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", "before-the-lab", "complete", 1)
	o, _ := f.s.Learn.Outline(ctx, f.u, "forge", "forge-101")
	if !o.Modules[1].Complete {
		t.Fatal("finishing the lab must complete the module")
	}
}

func TestSetupFailureAllowsSkipAndResetIsRateLimited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	failSetup := true
	f.run.exit = func(sp ScriptSpec) int {
		if failSetup && strings.Contains(string(sp.Script), "nginx -s reload") && sp.Env == nil {
			return 1
		}
		return 0
	}
	v := f.start(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-forge-file", "")
	d, err := f.s.OpenTask(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || d.SetupError == "" || d.Status != "setup_failed" {
		t.Fatalf("setup failure: %+v %v", d, err)
	}
	var runs int
	_ = f.s.DB.QueryRow(ctx, "SELECT count(*) FROM setup_runs WHERE task = 't2-find-port'").Scan(&runs)
	if runs != 2 {
		t.Fatalf("setup must be retried once, ran %d times", runs)
	}

	failSetup = false
	if _, err := f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("reset within 5 minutes must be refused: %v", err)
	}
	f.clk.Add(6 * time.Minute)
	if _, err := f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port"); err != nil {
		t.Fatalf("reset after 6 minutes: %v", err)
	}

	failSetup = true
	f.clk.Add(6 * time.Minute)
	_, _ = f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port")
	after, err := f.s.Skip(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || statusOf(after, "t2-find-port") != "skipped" || statusOf(after, "t3-fix-nginx") != "open" {
		t.Fatalf("skip: %v %+v", err, after)
	}
}

func TestSweepExtendAndOwnership(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)

	if _, err := f.s.Get(ctx, f.other, v.ID); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("other users must not see the lab: %v", err)
	}

	ext, err := f.s.Extend(ctx, f.u, v.ID)
	if err != nil || !ext.EndsAt.Equal(v.EndsAt.Add(30*time.Minute)) || ext.CanExtend {
		t.Fatalf("extend: %+v %v", ext, err)
	}
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("second extend: %v", err)
	}

	f.clk.Add(19 * time.Minute)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Ready {
		t.Fatalf("swept too early: %s", got.State)
	}
	f.clk.Add(2 * time.Minute) // 21 min idle > 20 min idle_timeout
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, v.ID)
	if got.State != Destroyed || got.EndReason != "idle" || len(f.run.destroyed) != 1 {
		t.Fatalf("idle sweep: %+v destroyed %v", got, f.run.destroyed)
	}
}
