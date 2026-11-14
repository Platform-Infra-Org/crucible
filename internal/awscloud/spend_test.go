package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func spendClient(t *testing.T, source string, reply func(target string, body map[string]any) string) (*Client, func() []call) {
	t.Helper()
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		var body map[string]any
		_ = json.Unmarshal([]byte(c.body), &body)
		return 200, "application/x-amz-json-1.1", reply(c.target, body)
	})
	cl.cfg.SpendSource, cl.cfg.BudgetName = source, "crucible-labs-monthly"
	return cl, calls
}

func TestAccountSpendFromTheBudget(t *testing.T) {
	c, calls := spendClient(t, "budget", func(target string, body map[string]any) string {
		if target != "AWSBudgetServiceGateway.DescribeBudget" || body["AccountId"] != "444455556666" || body["BudgetName"] != "crucible-labs-monthly" {
			t.Errorf("request: %s %v", target, body)
		}
		return `{"Budget":{"BudgetName":"crucible-labs-monthly","CalculatedSpend":{"ActualSpend":{"Amount":"412.37","Unit":"USD"}}}}`
	})
	usd, err := c.AccountSpend(context.Background())
	if err != nil || usd != 412.37 {
		t.Fatalf("%v %v", usd, err)
	}
	if n := len(calls()); n != 2 { // the ops role's AssumeRole, then the budget read
		t.Fatalf("calls: %d", n)
	}
}

func TestAccountSpendFromCostExplorer(t *testing.T) {
	c, _ := spendClient(t, "cost_explorer", func(target string, body map[string]any) string {
		tp, _ := body["TimePeriod"].(map[string]any)
		if target != "AWSInsightsIndexService.GetCostAndUsage" || body["Granularity"] != "MONTHLY" || tp["Start"] != "2026-11-01" || tp["End"] != "2026-11-15" {
			t.Errorf("request: %s %v", target, body)
		}
		return `{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"38.5","Unit":"USD"}}}]}`
	})
	c.now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	if usd, err := c.AccountSpend(context.Background()); err != nil || usd != 38.5 {
		t.Fatalf("%v %v", usd, err)
	}
}

func TestAccountSpendRefusesAMalformedAmount(t *testing.T) {
	for _, amt := range []string{`"abc"`, `"-3"`, `"NaN"`, `"Inf"`} {
		c, _ := spendClient(t, "budget", func(string, map[string]any) string {
			return `{"Budget":{"CalculatedSpend":{"ActualSpend":{"Amount":` + amt + `,"Unit":"USD"}}}}`
		})
		if _, err := c.AccountSpend(context.Background()); err == nil {
			t.Errorf("amount %s must be an error", amt)
		}
	}
}

func TestAccountSpendWithoutASourceSaysWhatToRun(t *testing.T) {
	c, calls := spendClient(t, "", func(string, map[string]any) string { return "" })
	if _, err := c.AccountSpend(context.Background()); !errors.Is(err, ErrNoSpendSource) || len(calls()) != 0 {
		t.Fatalf("no call, ErrNoSpendSource: %v %v", err, calls())
	}
}
