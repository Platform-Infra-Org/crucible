package labs

import (
	"context"
	"errors"
	"testing"
	"time"

	"crucible/internal/awscloud"
)

func TestReadAccountSpendStoresTheBillAndKeepsItOnFailure(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.SetAccountSpend(412.37)
	f.s.Cloud = cloud
	if err := f.s.ReadAccountSpend(ctx, false); err != nil {
		t.Fatal(err)
	}
	b, _ := f.s.accountBill(ctx)
	if b.USD != 412.37 || b.ReadAt == nil || !b.ReadAt.Equal(f.clk.Now()) || b.Error != "" {
		t.Fatalf("stored: %+v", b)
	}
	f.clk.Add(time.Hour)
	cloud.FailAccountSpend(errors.New("AccessDenied: budgets:ViewBudget"))
	_ = f.s.ReadAccountSpend(ctx, false)
	b, _ = f.s.accountBill(ctx)
	if b.USD != 412.37 || !b.ReadAt.Equal(f.clk.Now().Add(-time.Hour)) || b.Error != "AccessDenied: budgets:ViewBudget" {
		t.Fatalf("a failure keeps the last figure and records the error: %+v", b)
	}
}

func TestCostExplorerIsReadAtMostHourly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud, f.s.SpendSource = cloud, "cost_explorer"
	_ = f.s.ReadAccountSpend(ctx, false)
	f.clk.Add(50 * time.Minute)
	_ = f.s.ReadAccountSpend(ctx, false)
	f.clk.Add(15 * time.Minute)
	_ = f.s.ReadAccountSpend(ctx, false)
	if cloud.AccountSpendCalls != 2 {
		t.Fatalf("Cost Explorer costs $0.01 a call: read at 0 and 65 min only, got %d", cloud.AccountSpendCalls)
	}
}

func TestManualRefreshIsOncePerFiveMinutesForEveryone(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	ok, wait, err := f.s.claimAccountRefresh(ctx)
	if err != nil || !ok {
		t.Fatalf("the first refresh wins: %v %v", ok, err)
	}
	ok, wait, _ = f.s.claimAccountRefresh(ctx)
	if ok || wait != accountRefreshEvery {
		t.Fatalf("a second one now waits 5 minutes: %v %v", ok, wait)
	}
	f.clk.Add(accountRefreshEvery)
	if ok, _, _ = f.s.claimAccountRefresh(ctx); !ok {
		t.Fatal("after 5 minutes it may run again")
	}
	// Two admins at once: exactly one wins.
	f.clk.Add(accountRefreshEvery)
	wins := make(chan bool, 2)
	for range 2 {
		go func() { ok, _, _ := f.s.claimAccountRefresh(ctx); wins <- ok }()
	}
	if a, b := <-wins, <-wins; a == b {
		t.Fatalf("one of two concurrent refreshes wins: %v %v", a, b)
	}
}

func TestABillIsStaleAfter36HoursOrWhenNeverRead(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	read := now.Add(-35 * time.Hour)
	if (AccountBill{}).Stale(now) == false || (AccountBill{ReadAt: &read}).Stale(now) {
		t.Fatal("never read is stale; 35 h old is not")
	}
	old := now.Add(-37 * time.Hour)
	if !(AccountBill{ReadAt: &old}).Stale(now) {
		t.Fatal("37 h old is stale")
	}
}

func TestNoSpendSourceIsRecordedSoTheLedgerCanSayWhy(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.FailAccountSpend(awscloud.ErrNoSpendSource)
	f.s.Cloud = cloud
	_ = f.s.ReadAccountSpend(ctx, false)
	if b, _ := f.s.accountBill(ctx); b.Error != awscloud.ErrNoSpendSource.Error() || !b.Stale(f.clk.Now()) {
		t.Fatalf("recorded and stale: %+v", b)
	}
}
