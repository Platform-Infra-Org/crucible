package org

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/db/dbtest"
)

func f(v float64) *float64 { return &v }

func budgetStore(t *testing.T) (*Store, context.Context) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "admin@x", "platform", TeamBody{Name: "Platform", Leader: "lead@x"}); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	var n int
	if err := s.DB.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetBudgetStoresAndDefaultsTheCap(t *testing.T) {
	s, ctx := budgetStore(t)
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{MonthlyUSD: f(200)}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if b := p.Teams["platform"].Budget; b.MonthlyUSD != 200 || b.HardCapUSD != 200 || b.Version != 1 {
		t.Errorf("budget = %+v; an unset hard cap defaults to the monthly budget", b)
	}
	// the audit row records the resolved cap
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action='team.budget' AND target='platform' AND detail->>'hard_cap_usd'='200' AND detail->>'monthly_usd'='200'`); n != 1 {
		t.Errorf("audit rows with resolved cap = %d, want 1", n)
	}
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{Version: 1, MonthlyUSD: f(100), HardCapUSD: f(150)}); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Platform(ctx)
	if b := p.Teams["platform"].Budget; b.MonthlyUSD != 100 || b.HardCapUSD != 150 || b.Version != 2 {
		t.Errorf("budget = %+v", b)
	}
}

func TestSetBudgetRefusalsLeaveNothing(t *testing.T) {
	s, ctx := budgetStore(t)
	for name, b := range map[string]BudgetBody{
		"negative monthly": {MonthlyUSD: f(-1)},
		"negative cap":     {MonthlyUSD: f(0), HardCapUSD: f(-5)},
		"cap below budget": {MonthlyUSD: f(200), HardCapUSD: f(50)},
		"missing monthly":  {HardCapUSD: f(5)},
		"nan":              {MonthlyUSD: f(math.NaN())},
		"inf":              {MonthlyUSD: f(10), HardCapUSD: f(math.Inf(1))},
	} {
		err := s.SetBudget(ctx, "admin@x", "platform", b)
		if !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s = %v, want Invalid", name, err)
		}
		if name == "negative cap" && !strings.Contains(err.Error(), "negative") {
			t.Errorf("negative cap must be refused as negative: %v", err)
		}
		if name == "cap below budget" && (!strings.Contains(err.Error(), "50") || !strings.Contains(err.Error(), "200")) {
			t.Errorf("message must name both numbers: %v", err)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM team_budgets`); n != 0 {
		t.Errorf("refusals left %d budget rows", n)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action = 'team.budget'`); n != 0 {
		t.Errorf("refusals left %d audit rows", n)
	}
}

func TestSetBudgetVersions(t *testing.T) {
	s, ctx := budgetStore(t)
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{MonthlyUSD: f(100)}); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]BudgetBody{"zero over a row": {MonthlyUSD: f(1)}, "stale": {Version: 7, MonthlyUSD: f(1)}} {
		if err := s.SetBudget(ctx, "other@x", "platform", b); !errors.Is(err, apperr.Conflict) {
			t.Errorf("%s = %v, want Conflict", name, err)
		}
	}
	p, _ := s.Platform(ctx)
	if b := p.Teams["platform"].Budget; b.MonthlyUSD != 100 {
		t.Errorf("a conflicting save overwrote the budget: %+v", b)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE actor = 'other@x'`); n != 0 {
		t.Errorf("conflicts left %d audit rows", n)
	}
}

func TestSetBudgetUnknownTeamIsNotFound(t *testing.T) {
	s, ctx := budgetStore(t)
	for _, v := range []int64{0, 1} {
		if err := s.SetBudget(ctx, "admin@x", "ghost", BudgetBody{Version: v, MonthlyUSD: f(10)}); !errors.Is(err, apperr.NotFound) {
			t.Errorf("version %d unknown team = %v, want NotFound", v, err)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action = 'team.budget'`); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

func TestSetBudgetZeroMeansNoBudgetAndAuditRecordsChange(t *testing.T) {
	s, ctx := budgetStore(t)
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{MonthlyUSD: f(200)}); err != nil {
		t.Fatal(err)
	}
	// an explicit 0/0 is stored as 0/0 (no budget, nothing blocked), exactly as budget.yaml does
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{Version: 1, MonthlyUSD: f(0), HardCapUSD: f(0)}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Platform(ctx)
	if b := p.Teams["platform"].Budget; b.MonthlyUSD != 0 || b.HardCapUSD != 0 {
		t.Errorf("0/0 = %+v, want 0/0", b)
	}
	if n := count(t, s, `SELECT count(*) FROM audit_log WHERE action='team.budget' AND (detail->>'previous_monthly_usd')::numeric = 200 AND (detail->>'previous_hard_cap_usd')::numeric = 200 AND detail->>'hard_cap_defaulted' = 'true'`); n != 1 {
		t.Errorf("audit rows recording the previous values and the defaulted cap = %d, want 1", n)
	}
}
