package awscloud

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// ErrNoSpendSource: the labs stack predates spend_source, so nothing says where the account's bill is read.
var ErrNoSpendSource = errors.New("no spend source: re-run crucible aws labs-init, then crucible aws up")

// AccountSpend is the account's month-to-date bill in USD, as the ops role reads it: the AWS Budget's actual spend
// (free, updated by AWS a few times a day) or Cost Explorer's unblended cost this month ($0.01 per call).
func (c *Client) AccountSpend(ctx context.Context) (float64, error) {
	switch c.cfg.SpendSource {
	case "budget":
		acct, err := accountOf(c.cfg.OpsRoleARN)
		if err != nil {
			return 0, err
		}
		cl := budgets.NewFromConfig(c.ops, func(o *budgets.Options) { o.Region = "us-east-1"; o.BaseEndpoint = c.endpoint() })
		out, err := cl.DescribeBudget(ctx, &budgets.DescribeBudgetInput{AccountId: aws.String(acct), BudgetName: aws.String(c.cfg.BudgetName)})
		if err != nil {
			return 0, err
		}
		if out.Budget == nil || out.Budget.CalculatedSpend == nil || out.Budget.CalculatedSpend.ActualSpend == nil {
			return 0, fmt.Errorf("budget %s has no actual spend yet", c.cfg.BudgetName)
		}
		return usdAmount(aws.ToString(out.Budget.CalculatedSpend.ActualSpend.Amount))
	case "cost_explorer":
		now := c.clock().UTC()
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		cl := costexplorer.NewFromConfig(c.ops, func(o *costexplorer.Options) { o.Region = "us-east-1"; o.BaseEndpoint = c.endpoint() })
		out, err := cl.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{Granularity: cetypes.GranularityMonthly,
			Metrics:    []string{"UnblendedCost"},
			TimePeriod: &cetypes.DateInterval{Start: aws.String(start.Format("2006-01-02")), End: aws.String(now.AddDate(0, 0, 1).Format("2006-01-02"))}})
		if err != nil {
			return 0, err
		}
		var sum float64
		for _, r := range out.ResultsByTime {
			v, err := usdAmount(aws.ToString(r.Total["UnblendedCost"].Amount))
			if err != nil {
				return 0, err
			}
			sum += v
		}
		return sum, nil
	default:
		return 0, ErrNoSpendSource
	}
}

// usdAmount parses a billing amount; a negative, NaN or infinite one is an error, never a figure.
func usdAmount(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	return v, nil
}

// accountOf is the account id in a role ARN (arn:aws:iam::<account>:role/<name>).
func accountOf(arn string) (string, error) {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || len(parts[4]) != 12 {
		return "", fmt.Errorf("no account id in %q", arn)
	}
	return parts[4], nil
}
