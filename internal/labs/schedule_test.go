package labs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
)

// Europe/Bucharest is UTC+3 in early October 2026; business-hours is Mon–Fri 08:00–19:00 there (05:00–16:00 UTC).
func onSchedule(f *fx) { f.plat.Teams["forge"].Programs["forge-101"].Schedule = "business-hours" }

func TestScheduleGatesRequestsAndApprovals(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)) // Saturday 10:00 local
	_, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "Next window opens Mon 08:00") {
		t.Fatalf("request outside the window: %v", err)
	}
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab"); !strings.Contains(ml.Blocked, "Labs for this program run") {
		t.Fatalf("lobby must say why: %+v", ml)
	}
	f.rates["first-heat"] = 0.5
	f.clk.Set(time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)) // Friday 18:00 local: open
	v := f.request(t, f.u)
	f.clk.Set(time.Date(2026, 10, 9, 16, 30, 0, 0, time.UTC)) // Friday 19:30 local: closed
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("approving outside the window must wait for it to open: %v", err)
	}
}

func TestLabEndsAtScheduleClose(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC)) // Wednesday 18:30 local; TTL 1h would end 19:30
	v := f.start(t)
	if v.LimitReason != "schedule" || !v.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) || v.CanExtend {
		t.Fatalf("ends at window close, no extension: %+v", v)
	}
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("extend past the window: %v", err)
	}
	f.clk.Add(31 * time.Minute)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Destroyed || got.EndReason != "schedule" {
		t.Fatalf("destroyed at window end: %+v", got)
	}
}

func TestExtensionIsCappedByTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 14, 45, 0, 0, time.UTC)) // 17:45 local: TTL ends 18:45, window 19:00
	v := f.start(t)
	if v.LimitReason != "ttl" || !v.CanExtend {
		t.Fatalf("%+v", v)
	}
	got, err := f.s.Extend(ctx, f.u, v.ID) // +30m would be 19:15: capped at 19:00
	if err != nil || got.LimitReason != "schedule" || !got.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("extension capped: %+v %v", got, err)
	}
}

func TestExtensionThatRaisesTheTierIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 4.5 // $4.50 for 1h: tier 1. +30m = $6.75: tier 2.
	v := f.request(t, f.u)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "new approval") {
		t.Fatalf("tier-raising extension: %v", err)
	}
}

// A schedule that can't fit the escalation period (AddOpen gives up) falls back to plain wall-clock time, never a zero deadline.
func TestEscalateAtFallsBackWhenScheduleIsDegenerate(t *testing.T) {
	f := setup(t, true)
	onSchedule(f)
	f.plat.Settings.EscalationHours = 5000 // more open time than AddOpen looks ahead
	f.rates["first-heat"] = 0.5
	now := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	f.clk.Set(now)
	v := f.request(t, f.u)
	if want := now.Add(5000 * time.Hour); v.EscalateAt == nil || !v.EscalateAt.Equal(want) {
		t.Fatalf("escalate_at %v, want %v", v.EscalateAt, want)
	}
}

func TestScheduleInfoNamesAnInlineSchedule(t *testing.T) {
	f := setup(t, true)
	pr := f.plat.Teams["forge"].Programs["forge-101"]
	now := f.clk.Now()
	if info := scheduleInfo(f.plat, "forge", "forge-101", now); info.Name != "" {
		t.Fatalf("any time: no name: %+v", info)
	}
	onSchedule(f)
	if info := scheduleInfo(f.plat, "forge", "forge-101", now); info.Name != "business-hours" {
		t.Fatalf("named: %+v", info)
	}
	pr.Schedule, pr.Inline = "", f.plat.Settings.Schedules["business-hours"]
	if info := scheduleInfo(f.plat, "forge", "forge-101", now); info.Name != "inline" || info.Text == "" {
		t.Fatalf("inline: %+v", info)
	}
}
