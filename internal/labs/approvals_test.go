package labs

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

func (f *fx) request(t *testing.T, u *auth.User) *View {
	t.Helper()
	v, err := f.s.Start(context.Background(), u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *fx) waitState(t *testing.T, u *auth.User, id string, want State) *View {
	t.Helper()
	for i := 0; i < 300; i++ {
		if v, err := f.s.Get(context.Background(), u, id); err == nil && v.State == want {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lab %s never reached %s", id, want)
	return nil
}

func TestPaidLabNeedsApprovalAndNobodyApprovesTheirOwn(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5 // first-heat has ttl 1h → $0.50: above auto_approve_usd 0, within tier1 $5
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if err != nil || !ml.NeedsApproval || ml.EstimateUSD != 0.5 {
		t.Fatalf("lobby preview %+v %v", ml, err)
	}
	v := f.request(t, f.u)
	if v.State != PendingApproval || v.Tier != rbac.TierApprover || v.EstimateUSD != 0.5 {
		t.Fatalf("request %+v", v)
	}
	if v.EscalateAt == nil || !v.EscalateAt.Equal(f.clk.Now().Add(4*time.Hour)) {
		t.Fatalf("no schedule: escalate after 4 wall-clock hours, got %v", v.EscalateAt)
	}
	if again := f.request(t, f.u); again.ID != v.ID {
		t.Fatal("asking again returns the pending request")
	}
	ev := f.notes.last(notify.LabPending)
	if ev == nil || strings.Join(ev.To, ",") != "leader@crucible.local" || ev.Team != "forge" || ev.Link != "/approvals" {
		t.Fatalf("pending notification %+v", ev)
	}
	if list, _ := f.s.Approvals(ctx, f.other); len(list) != 0 {
		t.Fatalf("a senior who is not an approver sees nothing: %v", list)
	}
	list, err := f.s.Approvals(ctx, f.leader)
	if err != nil || len(list) != 1 {
		t.Fatalf("leader inbox %v %v", list, err)
	}
	if a := list[0]; a.Requester != "trainee@crucible.local" || a.LabTitle != "First Heat: Your First Lab" ||
		a.TeamSpend.BudgetUSD != 200 || a.TeamSpend.CapUSD != 250 || a.Schedule.Text != "any time" || a.Recent == nil {
		t.Fatalf("approval context %+v", a)
	}
	if _, err := f.s.Decide(ctx, f.u, v.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("self-approval must be forbidden: %v", err)
	}
	st, err := f.s.Decide(ctx, f.leader, v.ID, true, "have fun")
	if err != nil || st != Provisioning {
		t.Fatalf("approve: %v %v", st, err)
	}
	ready := f.waitState(t, f.u, v.ID, Ready)
	if ready.DecidedBy != "leader@crucible.local" || ready.DecisionNote != "have fun" {
		t.Fatalf("decision not shown to the trainee: %+v", ready)
	}
	if ev := f.notes.last(notify.LabApproved); ev == nil || ev.To[0] != "trainee@crucible.local" || ev.Link != "/p/forge/forge-101/m/02-first-lab/lab" {
		t.Fatalf("approved notification %+v", ev)
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, false, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a decided request cannot be decided again: %v", err)
	}
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1`, v.ID).Scan(&action)
	if action != "lab.approve" {
		t.Fatalf("audit action %q", action)
	}
}

func TestRejectWithdrawAndRequestAgain(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	v := f.request(t, f.u)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, false, "not today"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Rejected || got.DecisionNote != "not today" {
		t.Fatalf("rejected view %+v", got)
	}
	if f.notes.last(notify.LabRejected) == nil {
		t.Fatal("the trainee hears about the rejection")
	}
	v2 := f.request(t, f.u)
	if v2.ID == v.ID || v2.State != PendingApproval {
		t.Fatalf("a new request after a rejection: %+v", v2)
	}
	w, err := f.s.End(ctx, f.u, v2.ID)
	if err != nil || w.State != Expired || w.EndReason != "withdrawn" {
		t.Fatalf("withdraw: %+v %v", w, err)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatalf("withdrawn requests leave the inbox: %v", list)
	}
	if v3 := f.request(t, f.u); v3.ID == v2.ID || v3.State != PendingApproval {
		t.Fatalf("request after withdrawing: %+v", v3)
	}
}

func TestLeaderRequestRoutesPastThemselves(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	prog := f.plat.Teams["forge"].Programs["forge-101"]
	prog.Enrolled = append(prog.Enrolled, "leader@crucible.local")
	for _, it := range []string{"how-we-work", "quiz"} {
		_ = f.s.Learn.SetItem(ctx, f.leader.ID, "forge", "forge-101", "01-welcome", it, "complete", 1)
	}
	v := f.request(t, f.leader)
	if v.Tier != rbac.TierAdmin {
		t.Fatalf("the leader is the approver and the leader: route to admin, got %s", v.Tier)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatal("a request never shows in its requester's inbox")
	}
	if list, _ := f.s.Approvals(ctx, f.admin); len(list) != 1 {
		t.Fatal("the admin sees it")
	}
	if ev := f.notes.last(notify.LabPending); ev == nil || strings.Join(ev.To, ",") != "admin@crucible.local" {
		t.Fatalf("notify the admin, not the requester: %+v", ev)
	}
}

func TestReRequestAfterFailedStartSkipsApprovalForAnHour(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	v := f.request(t, f.u)
	f.run.mu.Lock()
	f.run.failProvision = errors.New("docker exploded")
	f.run.mu.Unlock()
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Failed)
	f.run.mu.Lock()
	f.run.failProvision = nil
	f.run.mu.Unlock()
	again := f.request(t, f.u)
	if again.State != Provisioning || again.Tier != rbac.TierAuto {
		t.Fatalf("re-request within an hour of an approved failure needs no approval (spec §14): %+v", again)
	}
	f.waitState(t, f.u, again.ID, Ready)
	if _, err := f.s.End(ctx, f.u, again.ID); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(61 * time.Minute)
	if later := f.request(t, f.u); later.State != PendingApproval {
		t.Fatalf("after an hour approval is needed again: %+v", later)
	}
}

func TestSpendSumsThisMonth(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	now := f.clk.Now() // 2026-10-05 09:00 UTC
	h := func(d float64) *time.Time { v := now.Add(time.Duration(d * float64(time.Hour))); return &v }
	seed := func(id, module, state string, hourly, estimate float64, created time.Time, ready, destroyed, ends *time.Time) {
		_, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at,
			last_activity_at, ready_at, ends_at, destroyed_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
			VALUES ($1, $2, 'forge', 'forge-101', $3, 'abc', 'local', $4, $5, $5, $6, $7, $8, 3600, 1800, 300, 0, $9, $10)`,
			id, f.u.ID, module, state, created, ready, ends, destroyed, hourly, estimate)
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("aaaaaaaaaaa1", "m1", "destroyed", 1.0, 2.0, *h(-3), h(-3), h(-1), h(-1))   // ran 2h at $1 → 2.00
	seed("aaaaaaaaaaa2", "m2", "ready", 0.5, 1.0, *h(-1), h(-1), nil, h(1))          // 1h so far, 1h to go at $0.50 → 0.50 / 1.00
	seed("aaaaaaaaaaa3", "m3", "provisioning", 3.0, 3.0, now, nil, nil, nil)         // committed 3.00
	seed("aaaaaaaaaaa4", "m4", "destroyed", 10, 10, *h(-240), h(-240), h(-239), nil) // September: not this month
	sp, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(sp.SpentUSD-2.5) > 1e-9 || math.Abs(sp.CommittedUSD-6.0) > 1e-9 || sp.BudgetUSD != 200 || sp.CapUSD != 250 {
		t.Fatalf("team spend %+v", sp)
	}
	if prog, _ := f.s.spend(ctx, f.plat, "forge", "forge-101"); prog.BudgetUSD != 0 || math.Abs(prog.SpentUSD-2.5) > 1e-9 {
		t.Fatalf("program spend %+v", prog)
	}
}

func TestSweepDoesNotKillALongPendingRequestOnceApproved(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	release := make(chan struct{})
	f.run.provision = func() { <-release }
	v := f.request(t, f.u)
	f.clk.Add(3 * time.Hour) // pending far longer than the 15 min provisioning limit
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Provisioning {
		t.Fatalf("sweep treated the approved request as a hung provision: %s", got.State)
	}
	close(release)
	f.waitState(t, f.u, v.ID, Ready)
}
