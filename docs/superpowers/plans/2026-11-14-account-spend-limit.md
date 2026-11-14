# Account Spend Limit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admins set a monthly limit on the AWS account's real bill (read from an AWS Budget, else Cost Explorer hourly);
past it, new paid labs need an admin. Crucible adds its own meter for the last 24 hours, and admins mark on a timeline
what AWS has already billed.

**Architecture:** A new `Cloud.AccountSpend` read (budget or Cost Explorer, ops role) runs as a River job and lands in
`aws_ops`. A third scope in `overCap` compares *billed + not yet billed (24 h meter after the marks) + reserved +
estimate* with the `account_monthly_limit_usd` setting and fails closed. The Ledger gains an `account` block (admins and
approvers) with a timeline of the last 24 hours whose markers admins drag (`billed_marks`).

**Tech Stack:** Go 1.26 (pgx, River, chi, aws-sdk-go-v2 + `service/budgets` + `service/costexplorer`), Postgres (goose),
Terraform (mock providers only), Helm, React 19 + Vite + vitest.

**Spec:** `docs/superpowers/specs/2026-11-14-account-spend-limit-design.md`

**Base branch:** `feat/account-spend-limit`, stacked on `fix/labs-org-managed-account` (labs-init switches; no billing
export). Run `git log --oneline -3` first: `7d7b932`, `da0b62d`, `ec100bd`.

## Global Constraints

- Toolchain: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH` in every shell; run from the repo root.
- **Never create real AWS resources.** Terraform only with mock providers (`terraform test`, `validate`); AWS code tested
  against `awscloud.Fake` or `httptest`. Never `terraform init` in `deploy/aws/main` (its s3 backend reaches AWS);
  `deploy/aws/labs` has local state: never `apply` there.
- Commits: pathspec form `git commit -m "…" -- <paths>`, never `git add -A` / `commit -a`; never rewrite history or
  force-push. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Tests first for every behaviour change; every money path keeps a test that fails without the fix.
- Any capability change updates `docs/user` in the same commit (`go test ./internal/docs`).
- `internal/labs` tests share one Postgres testcontainer: run them with `go test -p 2 -parallel 2 ./internal/labs`.
- Docker hygiene: never `docker system prune`; clean only build cache / dangling images you made.
- Migrations: `internal/db/migrations/00037_account_spend.sql`, goose, with a Down.
- UI: tokens only (no hex in `app.css`), every theme, motion stops under Calm forge; read `.claude/skills/crucible-ui/SKILL.md` first and add the timeline pattern to it.
- Copy (verbatim from the spec): setting **Monthly account limit (USD)**, hint *The AWS account's whole bill this month,
  as AWS reports it. Past it, new paid labs need an admin.*; Ledger *AWS account this month: $412.37 billed (as of
  14:20) + $4.13 not yet billed + $38.00 reserved by running labs = $454.50 of $600.00*; *AWS updates the bill a few times
  a day; reserved is what running labs may still spend.*; stale *Out of date: new paid labs need an admin until the bill
  is read again.*; Approvals *Would pass the account's monthly limit ($450.37 of $600.00): only an admin can approve it.*;
  lab page *Waiting for an admin: the forge's monthly spend limit is reached.*; timeline *Account billed up to 09:00 ⇆*,
  **Set separately** / **Follow account**, **Count all 24 h**, *Crucible adds $4.13 from 3 labs on top of the AWS bill · 1
  lab set on its own*, legend *In the AWS bill*, *Counted by Crucible*, *forge asleep*; mark error *Pick a time in the
  last 24 hours*.
- Numbers: read every **10 minutes** (budget), Cost Explorer reads **hourly**, manual refresh **once per 5 minutes**
  shared, stale after **36 hours**, the window is **24 hours**, marks snap and step by **15 minutes**, marks purged after
  **48 hours**.

## Review Focus

1. **An install whose labs stack predates `spend_source`** (no output, `CRUCIBLE_AWS_SPEND_SOURCE=""`) with a limit set:
   every paid request goes to an admin, and the Ledger says why (*no spend source: re-run labs-init, then up*), never a
   silent block. Test in Task 4.
2. **A mark sent with a time-zone offset** (`2026-11-14T09:00:00+02:00`) is stored as the same instant and the reply
   shows it in UTC; a mark exactly at the window's edges is accepted. Test in Task 5.
3. **The limit is lowered below today's total while requests wait**: an approver's approval of a waiting request is
   handed to an admin (Decide's re-check), with `cap_reason = account`. Test in Task 6.
4. **A lab with its own mark is destroyed**: it keeps counting (after its mark) until its usage leaves the window; its
   mark is purged after 48 hours, and a mark for an unknown lab is refused. Test in Task 5.
5. **Two admins move marks at once**: both writes are audited, the last one wins, and each reply is the server's state
   (the page redraws from it). Test in Task 5.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/db/migrations/00037_account_spend.sql` | `aws_ops` account columns, forge night stamps, `settings.account_monthly_limit_usd`, `lab_instances.cap_reason`, `billed_marks` |
| `internal/config/config.go`, `internal/org/{settings,org,api,seed}.go` | the setting: YAML, load, save, validate, audit |
| `internal/awscloud/{awscloud,client,fake}.go`, new `internal/awscloud/spend.go` | `AccountSpend` (budget / Cost Explorer) and the fake |
| `deploy/aws/labs/main.tf`, `deploy/aws/main/{variables,main}.tf`, Helm | `spend_source`, the always-on budget, the ops role's read, the env vars |
| `internal/awsops/awsops.go`, `cmd/crucible/main.go` | `labs-init --spend-source`, `up` wiring |
| new `internal/labs/account.go` | reading the bill, the job, unbilled (24 h meter), reserved, the totals, marks, the gate's account scope, the Ledger block |
| `internal/labs/{finops,approvals,service,ledger,jobs,http,bedtime}.go` | the gate's reason, alerts, routes, night stamps |
| `internal/rbac/rbac.go` | `IsApprover` (admins, team leaders, program approvers) |
| new `web/src/lib/billedTimeline.ts`, new `web/src/components/BilledTimeline.tsx` | the timeline's geometry and the component |
| `web/src/pages/{Ledger,AdminSettings,Approvals,Lab}.tsx`, `web/src/types.ts`, `web/src/theme/app.css` | the UI |

Lanes (independent until Task 4): **core** T1 → T4 → T5 → T6 → T7; **cloud** T2; **ops** T3; **web** T8 after T7's JSON;
then T9 docs, T10 e2e. T4 consumes T1 and T2; T3 is needed only by T10.

---

### Task 1: Migration and the setting

**Files:**
- Create: `internal/db/migrations/00037_account_spend.sql`
- Modify: `internal/config/config.go:34-42` (Settings), `internal/org/settings.go` (SettingsBody, SetSettings),
  `internal/org/org.go:85-120` (settings load), `internal/org/api.go:153-166` (getSettings), `internal/org/seed.go:31-33`
- Test: `internal/org/settings_test.go`

**Interfaces:**
- Produces: `config.Settings.AccountMonthlyLimitUSD float64` (yaml `account_monthly_limit_usd`);
  `org.SettingsBody.AccountMonthlyLimitUSD float64` (json `account_monthly_limit_usd`); the migration's columns (used by
  T4–T7).

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- A monthly limit on the AWS account's real bill (spec 2026-11-14).
ALTER TABLE aws_ops
  ADD COLUMN account_usd DOUBLE PRECISION CHECK (account_usd >= 0), -- month-to-date bill, last good read
  ADD COLUMN account_read_at TIMESTAMPTZ,                              -- last successful read
  ADD COLUMN account_attempt_at TIMESTAMPTZ,                           -- last attempt (Cost Explorer's hourly gate)
  ADD COLUMN account_error TEXT NOT NULL DEFAULT '',
  ADD COLUMN account_error_at TIMESTAMPTZ,
  ADD COLUMN account_refresh_at TIMESTAMPTZ,                           -- last manual refresh (the 5-minute cooldown)
  ADD COLUMN forge_slept_at TIMESTAMPTZ,                               -- the timeline's night band
  ADD COLUMN forge_woke_at TIMESTAMPTZ;
ALTER TABLE settings ADD COLUMN account_monthly_limit_usd DOUBLE PRECISION NOT NULL DEFAULT 0
  CHECK (account_monthly_limit_usd >= 0);
ALTER TABLE lab_instances ADD COLUMN cap_reason TEXT NOT NULL DEFAULT '' CHECK (cap_reason IN ('', 'team', 'account'));
UPDATE lab_instances SET cap_reason = 'team' WHERE over_cap;
-- "AWS has billed this up to billed_to": lab_id '' is the account mark.
CREATE TABLE billed_marks (
  lab_id    TEXT PRIMARY KEY,
  billed_to TIMESTAMPTZ NOT NULL,
  set_by    TEXT NOT NULL,
  set_at    TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE billed_marks;
ALTER TABLE lab_instances DROP COLUMN cap_reason;
ALTER TABLE settings DROP COLUMN account_monthly_limit_usd;
ALTER TABLE aws_ops DROP COLUMN account_usd, DROP COLUMN account_read_at, DROP COLUMN account_attempt_at,
  DROP COLUMN account_error, DROP COLUMN account_error_at, DROP COLUMN account_refresh_at,
  DROP COLUMN forge_slept_at, DROP COLUMN forge_woke_at;
```

- [ ] **Step 2: Write the failing tests** (in `internal/org/settings_test.go`, beside the existing SetSettings tests;
  reuse their store setup helper)

```go
func TestAccountLimitIsSavedLoadedAndAudited(t *testing.T) {
	s, ctx := newStore(t) // the helper the other settings tests use
	b := currentSettings(t, s) // read GET's body: version and the rest unchanged
	b.AccountMonthlyLimitUSD = 600
	if err := s.SetSettings(ctx, "admin@x", b); err != nil {
		t.Fatal(err)
	}
	var st config.Settings
	if err := s.settings(ctx, &st); err != nil || st.AccountMonthlyLimitUSD != 600 {
		t.Fatalf("loaded %v: %v", st.AccountMonthlyLimitUSD, err)
	}
	var detail string
	_ = s.DB.QueryRow(ctx, `SELECT detail::text FROM audit_log WHERE action = 'settings.update' ORDER BY id DESC LIMIT 1`).Scan(&detail)
	if !strings.Contains(detail, `"account_monthly_limit_usd": 600`) {
		t.Fatalf("the audit row records the limit: %s", detail)
	}
}

func TestAccountLimitMustBeFiniteAndNotNegative(t *testing.T) {
	s, ctx := newStore(t)
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		b := currentSettings(t, s)
		b.AccountMonthlyLimitUSD = v
		if err := s.SetSettings(ctx, "admin@x", b); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%v must be refused: %v", v, err)
		}
	}
}
```

  If the file has no `currentSettings`/`newStore` helpers, add them next to the tests: `newStore` is whatever the
  existing tests call to get a `*Store` on a fresh database; `currentSettings` reads `version` and `s.settings` into a
  `SettingsBody` exactly as `getSettings` does (`internal/org/api.go:153-166`).

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/org -run 'AccountLimit' -v`
Expected: FAIL to compile (`AccountMonthlyLimitUSD` undefined).

- [ ] **Step 4: Implement**
  - `config.Settings`: `AccountMonthlyLimitUSD float64 \`yaml:"account_monthly_limit_usd"\` // the AWS account's bill
    this month past which paid labs need an admin; 0 = none`. In `config.Load`'s settings checks (near
    `config.go:518-522`) refuse a negative or non-finite value: `platform.yaml: account_monthly_limit_usd must be 0 or
    more`.
  - `SettingsBody`: `AccountMonthlyLimitUSD float64 \`json:"account_monthly_limit_usd"\``.
  - `SetSettings`: after the escalation check,
    ```go
    if b.AccountMonthlyLimitUSD < 0 || math.IsNaN(b.AccountMonthlyLimitUSD) || math.IsInf(b.AccountMonthlyLimitUSD, 0) {
    	return apperr.Wrap(apperr.Invalid, "the monthly account limit must be 0 (none) or more")
    }
    ```
    add `"account_monthly_limit_usd": b.AccountMonthlyLimitUSD` to `detail`, and `account_monthly_limit_usd = $17` to the
    UPDATE (shift `version = $16` to stay the WHERE's parameter: pass `b.AccountMonthlyLimitUSD` as `$17`).
  - `Store.settings`: select `account_monthly_limit_usd` and scan into `st.AccountMonthlyLimitUSD`.
  - `getSettings` and `seed.go`: pass `AccountMonthlyLimitUSD: st.AccountMonthlyLimitUSD` in the `SettingsBody` literals.
  - Grep `SettingsBody{` and `EscalationHours: st.EscalationHours` across `internal/` and add the field to every literal
    (export/import must carry it).

- [ ] **Step 5: Run the org and config tests**

Run: `go test ./internal/org ./internal/config ./internal/db/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(org): the monthly account limit setting; migration 00037 for the account bill, marks and cap reasons

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00037_account_spend.sql internal/config/config.go internal/org/settings.go internal/org/org.go internal/org/api.go internal/org/seed.go internal/org/settings_test.go
```

(Add any other file Step 4's grep touched to the pathspec.)

---

### Task 2: `Cloud.AccountSpend` (AWS Budget or Cost Explorer)

**Files:**
- Create: `internal/awscloud/spend.go`, `internal/awscloud/spend_test.go`
- Modify: `internal/awscloud/awscloud.go` (interface), `internal/awscloud/client.go:36-46` (Config), `internal/awscloud/fake.go`, `go.mod`, `go.sum`
- Test: `internal/awscloud/spend_test.go`, `internal/awscloud/fake_test.go`

**Interfaces:**
- Produces:
  - `Cloud.AccountSpend(ctx context.Context) (float64, error)`: the account's month-to-date USD.
  - `awscloud.Config.SpendSource string` (`"budget"` | `"cost_explorer"` | `""`), `Config.BudgetName string`.
  - `awscloud.ErrNoSpendSource = errors.New("no spend source: re-run crucible aws labs-init, then crucible aws up")`.
  - `Fake.SetAccountSpend(usd float64)`, `Fake.FailAccountSpend(err error)`; `Fake.AccountSpend` counts calls in
    `Fake.AccountSpendCalls int`.

- [ ] **Step 1: Add the SDK modules**

Run: `go get github.com/aws/aws-sdk-go-v2/service/budgets github.com/aws/aws-sdk-go-v2/service/costexplorer && go mod tidy`
Expected: `go.mod` gains both (same `aws-sdk-go-v2` family as the other services).

- [ ] **Step 2: Write the failing tests** (`internal/awscloud/spend_test.go`; `httptest` the way `client_test.go` does,
  with `Config.Endpoint` pointing at the server)

```go
package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func spendServer(t *testing.T, reply func(target string, body map[string]any) (int, string)) (*Client, *[]string) {
	t.Helper()
	var targets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Form.Get("Action") == "AssumeRole" || strings.Contains(r.URL.RawQuery, "AssumeRole") || r.Header.Get("X-Amz-Target") == "" {
			assumeRoleReply(w, r) // the helper client_test.go uses for STS
			return
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		targets = append(targets, r.Header.Get("X-Amz-Target"))
		code, out := reply(r.Header.Get("X-Amz-Target"), body)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	return testClient(t, Config{LabRoleARN: "arn:aws:iam::444455556666:role/crucible-lab",
		OpsRoleARN: "arn:aws:iam::444455556666:role/crucible-lab-ops", Endpoint: srv.URL,
		SpendSource: "budget", BudgetName: "crucible-labs-monthly"}), &targets
}

func TestAccountSpendFromTheBudget(t *testing.T) {
	c, targets := spendServer(t, func(target string, body map[string]any) (int, string) {
		if target != "AWSBudgetServiceGateway.DescribeBudget" || body["AccountId"] != "444455556666" || body["BudgetName"] != "crucible-labs-monthly" {
			t.Errorf("request: %s %v", target, body)
		}
		return 200, `{"Budget":{"BudgetName":"crucible-labs-monthly","CalculatedSpend":{"ActualSpend":{"Amount":"412.37","Unit":"USD"}}}}`
	})
	usd, err := c.AccountSpend(context.Background())
	if err != nil || usd != 412.37 || len(*targets) != 1 {
		t.Fatalf("%v %v %v", usd, err, *targets)
	}
}

func TestAccountSpendFromCostExplorer(t *testing.T) {
	c, _ := spendServer(t, func(target string, body map[string]any) (int, string) {
		tp, _ := body["TimePeriod"].(map[string]any)
		if target != "AWSInsightsIndexService.GetCostAndUsage" || body["Granularity"] != "MONTHLY" || tp["Start"] != "2026-11-01" || tp["End"] != "2026-11-15" {
			t.Errorf("request: %s %v", target, body)
		}
		return 200, `{"ResultsByTime":[{"Total":{"UnblendedCost":{"Amount":"38.5","Unit":"USD"}}}]}`
	})
	c.cfg.SpendSource = "cost_explorer"
	c.now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	if usd, err := c.AccountSpend(context.Background()); err != nil || usd != 38.5 {
		t.Fatalf("%v %v", usd, err)
	}
}

func TestAccountSpendRefusesAMalformedAmount(t *testing.T) {
	for _, amt := range []string{`"abc"`, `"-3"`, `"NaN"`, `"Inf"`} {
		c, _ := spendServer(t, func(string, map[string]any) (int, string) {
			return 200, `{"Budget":{"CalculatedSpend":{"ActualSpend":{"Amount":` + amt + `,"Unit":"USD"}}}}`
		})
		if _, err := c.AccountSpend(context.Background()); err == nil {
			t.Errorf("amount %s must be an error", amt)
		}
	}
}

func TestAccountSpendWithoutASourceSaysWhatToRun(t *testing.T) {
	c, targets := spendServer(t, func(string, map[string]any) (int, string) { return 500, "" })
	c.cfg.SpendSource = ""
	if _, err := c.AccountSpend(context.Background()); !errors.Is(err, ErrNoSpendSource) || len(*targets) != 0 {
		t.Fatalf("no call, ErrNoSpendSource: %v %v", err, *targets)
	}
}
```

  `assumeRoleReply` and `testClient` are whatever `client_test.go` already uses to fake STS and build a `*Client`
  against `httptest`; if their names differ, use those. The STS branch condition above must match how that file
  recognises STS requests (copy it).

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/awscloud -run AccountSpend -v`
Expected: FAIL to compile (`AccountSpend`, `SpendSource`, `ErrNoSpendSource`, `c.now` undefined).

- [ ] **Step 4: Implement** (`internal/awscloud/spend.go`)

```go
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
```

  - `Config`: add `SpendSource string // "budget" | "cost_explorer" | "": where the account's bill is read
    (labs stack output spend_source)` and `BudgetName string // the labs stack's budget (output budget_name)`.
  - `Client`: add `now func() time.Time` and `func (c *Client) clock() time.Time { if c.now != nil { return c.now() };
    return time.Now() }`.
  - Interface (`awscloud.go`, beside `Costs`): `// AccountSpend is the account's month-to-date bill (budget or Cost
    Explorer, per Config.SpendSource); ErrNoSpendSource when none is set.` `AccountSpend(ctx context.Context) (float64, error)`.
  - `Fake`: fields `accountUSD float64`, `accountErr error`, `AccountSpendCalls int` (under its mutex); `SetAccountSpend`,
    `FailAccountSpend`, and `AccountSpend` returning them and incrementing the count. Add a `fake_test.go` case:
    `SetAccountSpend(12.34)` → `12.34, nil`; `FailAccountSpend(e)` → `0, e`; calls counted.
  - `labs` has other `Cloud` implementations in tests (grep `awscloud.Cloud` in `internal/labs/*_test.go`, e.g.
    `cancelCloud`): if they embed `*awscloud.Fake` nothing changes; otherwise add the method.

- [ ] **Step 5: Run**

Run: `go vet ./... && go test ./internal/awscloud ./internal/labs -run 'AccountSpend|Fake' -p 2 -parallel 2`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(awscloud): AccountSpend reads the account's month-to-date bill from the labs AWS Budget, or Cost Explorer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- go.mod go.sum internal/awscloud/spend.go internal/awscloud/spend_test.go internal/awscloud/awscloud.go internal/awscloud/client.go internal/awscloud/fake.go internal/awscloud/fake_test.go
```

---

### Task 3: The labs stack, `labs-init --spend-source`, and the wiring to the server

**Files:**
- Modify: `deploy/aws/labs/main.tf` (variable, `aws_budgets_budget.labs`, ops policy, outputs), `deploy/aws/labs/labs.tftest.hcl`,
  `deploy/aws/main/variables.tf`, `deploy/aws/main/main.tf:131-150`, `deploy/aws/main/main.tftest.hcl` (`aws_labs_wiring`),
  `deploy/helm/crucible/values.yaml`, `deploy/helm/crucible/templates/crucible.yaml:96-112`, `deploy/helm/test.sh`,
  `internal/awsops/awsops.go` (`LabsInit`, `labSwitches`, `labVars`), `internal/awsops/awsops_test.go`,
  `cmd/crucible/main.go` (flags, `labSwitches`), `cmd/crucible/main_test.go`, `cmd/crucible/serve.go` (`awsSetup`), `cmd/crucible/serve_test.go`

**Interfaces:**
- Consumes: `awscloud.Config.SpendSource`, `.BudgetName` (Task 2).
- Produces: labs outputs `spend_source` (`"budget"` | `"cost_explorer"`), `budget_name`; main variables
  `lab_spend_source`, `lab_budget_name`; Helm `awsLabs.spendSource`, `awsLabs.budgetName`; env
  `CRUCIBLE_AWS_SPEND_SOURCE`, `CRUCIBLE_AWS_BUDGET_NAME`. `LabsInit`'s last parameter becomes
  `settings map[string]string` (Terraform variable → value).

- [ ] **Step 1: Write the failing Terraform tests** (append to `deploy/aws/labs/labs.tftest.hcl`; replace the existing
  `no_budget_without_emails` run with the first one below)

```hcl
run "the_budget_exists_without_emails_and_alerts_nobody" {
  command = apply
  assert {
    condition     = length(aws_budgets_budget.labs) == 1 && length(aws_budgets_budget.labs[0].notification) == 0
    error_message = "Crucible reads the budget's actual spend, so it always exists; it alerts only with emails"
  }
  assert {
    condition     = output.spend_source == "budget" && output.budget_name == "crucible-labs-monthly"
    error_message = "the server learns where to read the bill"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.ops.policy).Statement : s if try(s.Sid, "") == "ReadAccountSpend"]).Action == "budgets:ViewBudget" && one([for s in jsondecode(aws_iam_role_policy.ops.policy).Statement : s if try(s.Sid, "") == "ReadAccountSpend"]).Resource == "arn:aws:budgets::444455556666:budget/crucible-labs-monthly"
    error_message = "the ops role may view only that budget"
  }
  assert {
    condition     = !strcontains(aws_iam_role_policy.ops.policy, "ce:")
    error_message = "no Cost Explorer with the budget source"
  }
}

run "cost_explorer_source_reads_ce_and_makes_no_budget" {
  command = apply
  variables { spend_source = "cost_explorer" }
  assert {
    condition     = length(aws_budgets_budget.labs) == 0 && output.spend_source == "cost_explorer" && output.budget_name == ""
    error_message = "no budget unless alerts are wanted"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.ops.policy).Statement : s if try(s.Sid, "") == "ReadAccountSpend"]).Action == "ce:GetCostAndUsage"
    error_message = "the ops role reads Cost Explorer"
  }
}

run "spend_source_is_one_of_two" {
  command = plan
  variables { spend_source = "invoice" }
  expect_failures = [var.spend_source]
}
```

  Keep `export_and_budget` (emails → two notifications) passing.

  In `deploy/aws/main/main.tftest.hcl`'s `aws_labs_wiring` run add `lab_spend_source = "budget"` and
  `lab_budget_name = "crucible-labs-monthly"` to `variables`, and extend the expected `awsLabs` map with
  `spendSource = "budget", budgetName = "crucible-labs-monthly"`.

  In `deploy/helm/test.sh`, after the `noexp` check:

```bash
spend=$(helm template t "$chart" $aws --set awsLabs.spendSource=budget --set awsLabs.budgetName=crucible-labs-monthly)
for want in 'name: CRUCIBLE_AWS_SPEND_SOURCE, value: "budget"' 'name: CRUCIBLE_AWS_BUDGET_NAME, value: "crucible-labs-monthly"'; do
  grep -q -- "$want" <<<"$spend" || { echo "missing: $want"; exit 1; }
done
```

- [ ] **Step 2: Run them to see them fail**

Run: `(cd deploy/aws/labs && terraform test) ; (cd deploy/aws/main && terraform test) ; bash deploy/helm/test.sh`
Expected: FAIL (unknown variable `spend_source`, outputs, env vars missing).

- [ ] **Step 3: Implement the stacks and chart**
  - `deploy/aws/labs/main.tf`, beside `billing_export`:
    ```hcl
    variable "spend_source" {
      type        = string
      default     = "budget"
      description = "Where Crucible reads the account's bill: budget (the labs AWS Budget, free) or cost_explorer ($0.01 per call, read hourly)."
      validation {
        condition     = contains(["budget", "cost_explorer"], var.spend_source)
        error_message = "spend_source is budget or cost_explorer"
      }
    }
    ```
  - `aws_budgets_budget.labs`: `count = var.spend_source == "budget" || length(var.budget_emails) > 0 ? 1 : 0`; its
    `dynamic "notification"` gets `for_each = length(var.budget_emails) > 0 ? [80, 100] : []`. Update its comment: *The
    account's bill this month, which Crucible reads (spend_source = budget); alerts only with budget_emails.*
  - Ops policy: add to the `concat(...)` a third list:
    ```hcl
    [var.spend_source == "budget"
      ? { Sid = "ReadAccountSpend", Effect = "Allow", Action = "budgets:ViewBudget", Resource = "arn:aws:budgets::${local.account}:budget/${var.name}-labs-monthly" }
      : { Sid = "ReadAccountSpend", Effect = "Allow", Action = "ce:GetCostAndUsage", Resource = "*" }],
    ```
  - Outputs: `output "spend_source" { value = var.spend_source }` and
    `output "budget_name" { value = var.spend_source == "budget" ? "${var.name}-labs-monthly" : "" }`.
  - Main stack variables: `lab_spend_source` (string, default `""`, description *Set by up from the labs stack: budget
    or cost_explorer; empty: the account's bill is not read*) and `lab_budget_name` (string, default `""`); in the Helm
    values `awsLabs` map: `spendSource = var.lab_spend_source`, `budgetName = var.lab_budget_name`.
  - `values.yaml` `awsLabs`: `spendSource: ""   # budget | cost_explorer: where the account's bill is read (empty: not read)`
    and `budgetName: ""`. `crucible.yaml`: two env lines after `CRUCIBLE_AWS_EXPORT_NAME`:
    `- { name: CRUCIBLE_AWS_SPEND_SOURCE, value: {{ .Values.awsLabs.spendSource | quote }} }` and
    `- { name: CRUCIBLE_AWS_BUDGET_NAME, value: {{ .Values.awsLabs.budgetName | quote }} }`.

- [ ] **Step 4: Write the failing Go tests**
  - `internal/awsops/awsops_test.go`: change every `LabsInit(…, LabNet{…}, nil)` and `…, off)` call to pass a
    `map[string]string` (`nil` stays `nil`; `off` becomes `map[string]string{"billing_export": "false",
    "account_public_access_block": "false"}`; `map[string]bool{"billing_export": true}` becomes
    `map[string]string{"billing_export": "true"}`). Add:
    ```go
    func TestLabsInitPassesAndKeepsTheSpendSource(t *testing.T) {
    	r := &recorder{output: fakeAWS}
    	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{}, map[string]string{"spend_source": "cost_explorer"}); err != nil {
    		t.Fatal(err)
    	}
    	if indexOf(r.calls, "-var spend_source=cost_explorer") < 0 {
    		t.Fatalf("passed:\n%s", strings.Join(r.calls, "\n"))
    	}
    	labs := func(cmd string) string {
    		if strings.Contains(cmd, filepath.Join("deploy", "aws", "labs")+" output -json") {
    			return `{"lab_role_arn":{"value":"arn:aws:iam::1:role/crucible-lab"},"spend_source":{"value":"cost_explorer"}}`
    		}
    		return fakeAWS(cmd)
    	}
    	r = &recorder{output: labs}
    	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{}, nil); err != nil {
    		t.Fatal(err)
    	}
    	if indexOf(r.calls, "-var spend_source=cost_explorer") < 0 {
    		t.Fatalf("kept on a re-run:\n%s", strings.Join(r.calls, "\n"))
    	}
    }
    ```
    and in `TestLabsInitAndUpWiresTheLabAccount` add `"spend_source":{"value":"budget"},"budget_name":{"value":"crucible-labs-monthly"}`
    to the outputs JSON and `"-var lab_spend_source=budget"`, `"-var lab_budget_name=crucible-labs-monthly"` to `want`.
  - `cmd/crucible/main_test.go`: `TestLabSwitchesAreOnlyTheFlagsGiven` now expects `map[string]string{"billing_export":
    "false"}`; add a case: `--spend-source=cost-explorer` → `{"spend_source": "cost_explorer"}`, and
    `--spend-source=invoice` → an error from `labSwitches` (signature `labSwitches(fs *flag.FlagSet) (map[string]string, error)`).
  - `cmd/crucible/serve_test.go`: in `TestRealAWSLabsEstimateFromATerraformPlan` set `CRUCIBLE_AWS_SPEND_SOURCE=budget`,
    `CRUCIBLE_AWS_BUDGET_NAME=b` in the env (add them to `real`) and assert `got.SpendSource == "budget" && got.BudgetName == "b"`;
    add `TestDryRunHasAnAccountBill`: after `awsSetup(…, "dryrun", ar, …)`, `ar.Cloud.AccountSpend(ctx)` returns `12.34`.

- [ ] **Step 5: Implement the Go side**
  - `awsops.go`: `labSwitches` → `labSettings = []string{"account_public_access_block", "billing_export", "spend_source"}`;
    `LabsInit(..., net LabNet, settings map[string]string)`; the loop:
    ```go
    for _, k := range labSettings {
    	if v, ok := settings[k]; ok {
    		args = append(args, "-var", k+"="+v)
    	} else if v := cur[k]; v != "" {
    		args = append(args, "-var", k+"="+v)
    	}
    }
    ```
    `labVars`: append `"-var", "lab_spend_source=" + l["spend_source"], "-var", "lab_budget_name=" + l["budget_name"]`.
  - `main.go`: `fs.String("spend-source", "", "labs-init: where Crucible reads the account's bill: budget (an AWS Budget, free) or cost-explorer ($0.01 a call, hourly); default: keep the current one, else budget")`;
    `labSwitches` returns `(map[string]string, error)`: the two bool flags as `"true"`/`"false"`, and
    `spend-source` mapped `budget`→`budget`, `cost-explorer`→`cost_explorer`, anything else → `fmt.Errorf("--spend-source is budget or cost-explorer, not %q", v)`
    (the caller prints it and returns 2). Usage text: add `[--spend-source budget|cost-explorer]` to the labs-init line
    and one sentence to the notes: *labs-init --spend-source picks where the account's monthly bill is read for the
    account limit: budget (default, free) or cost-explorer ($0.01 a call, read hourly).*
  - `serve.go` `awsSetup`: pass `SpendSource: getenv("CRUCIBLE_AWS_SPEND_SOURCE"), BudgetName: getenv("CRUCIBLE_AWS_BUDGET_NAME")`
    in `awscloud.Config`; in the dry-run branch, `fake.SetAccountSpend(12.34) // the Ledger's account line has a figure in a dry run`.

- [ ] **Step 6: Run everything this task touched**

Run: `(cd deploy/aws/labs && terraform fmt -check && terraform test) && (cd deploy/aws/main && terraform test) && bash deploy/helm/test.sh && go vet ./... && go test ./internal/awsops ./cmd/crucible`
Expected: PASS (labs 31, main 39).

- [ ] **Step 7: Commit**

```bash
git commit -m "feat(aws): the labs budget always exists for Crucible to read; labs-init --spend-source budget|cost-explorer; wired to the server

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/labs/main.tf deploy/aws/labs/labs.tftest.hcl deploy/aws/main/variables.tf deploy/aws/main/main.tf deploy/aws/main/main.tftest.hcl deploy/helm/crucible/values.yaml deploy/helm/crucible/templates/crucible.yaml deploy/helm/test.sh internal/awsops/awsops.go internal/awsops/awsops_test.go cmd/crucible/main.go cmd/crucible/main_test.go cmd/crucible/serve.go cmd/crucible/serve_test.go
```

---

### Task 4: Reading the bill (job, refresh cooldown, staleness)

**Files:**
- Create: `internal/labs/account.go`, `internal/labs/account_test.go`
- Modify: `internal/labs/jobs.go` (AccountSpendArgs + worker), `internal/labs/ledger.go:284-315` (RefreshFinOps),
  `cmd/crucible/serve.go:335-336` (register worker), `cmd/crucible/serve.go:479-482` (periodic job)

**Interfaces:**
- Consumes: `Cloud.AccountSpend` (T2), the `aws_ops` columns (T1).
- Produces:
  - `const accountReadEvery = 10 * time.Minute`, `ceReadEvery = time.Hour`, `accountRefreshEvery = 5 * time.Minute`, `accountStaleAfter = 36 * time.Hour`.
  - `func (s *Service) ReadAccountSpend(ctx context.Context, manual bool) error`.
  - `type AccountBill struct { USD float64; ReadAt, AttemptAt, RefreshAt *time.Time; Error string }` and
    `func (s *Service) accountBill(ctx context.Context) (AccountBill, error)`; `func (b AccountBill) Stale(now time.Time) bool`.
  - `labs.Service.SpendSource string` (set in serve from `CRUCIBLE_AWS_SPEND_SOURCE`; `"cost_explorer"` turns on the hourly gate).
  - `labs.AccountSpendArgs{Manual bool}` (`Kind() = "aws_account_spend"`), `labs.AccountSpendWorker`.

- [ ] **Step 1: Write the failing tests** (`internal/labs/account_test.go`; `setup(t, true)` is the existing fixture
  with `f.s`, `f.clk`, `f.admin`)

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test -p 2 -parallel 2 ./internal/labs -run 'AccountSpend|CostExplorer|ManualRefresh|Stale|SpendSource' -v`
Expected: FAIL to compile.

- [ ] **Step 3: Implement** (`internal/labs/account.go`)

```go
package labs

import (
	"context"
	"time"
)

const (
	accountReadEvery    = 10 * time.Minute // the periodic job (an AWS Budget read is free)
	ceReadEvery         = time.Hour        // Cost Explorer costs $0.01 a call
	accountRefreshEvery = 5 * time.Minute  // Refresh now, shared by every admin
	accountStaleAfter   = 36 * time.Hour   // past it, the limit fails closed
)

// AccountBill is the account's month-to-date bill as last read (aws_ops).
type AccountBill struct {
	USD                         float64
	ReadAt, AttemptAt, RefreshAt *time.Time
	Error                       string
}

// Stale: never read successfully, or not for accountStaleAfter. With a limit set, a stale bill sends paid requests to an admin.
func (b AccountBill) Stale(now time.Time) bool {
	return b.ReadAt == nil || now.Sub(*b.ReadAt) > accountStaleAfter
}

func (s *Service) accountBill(ctx context.Context) (AccountBill, error) {
	var b AccountBill
	var usd *float64
	err := s.DB.QueryRow(ctx, `SELECT account_usd, account_read_at, account_attempt_at, account_refresh_at, account_error FROM aws_ops`).
		Scan(&usd, &b.ReadAt, &b.AttemptAt, &b.RefreshAt, &b.Error)
	if usd != nil {
		b.USD = *usd
	}
	return b, err
}

// ReadAccountSpend reads the account's bill into aws_ops. The periodic run reads Cost Explorer at most hourly; a manual
// one (Refresh now, already past claimAccountRefresh) always reads. A failure keeps the last good figure.
func (s *Service) ReadAccountSpend(ctx context.Context, manual bool) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now()
	if !manual && s.SpendSource == "cost_explorer" {
		b, err := s.accountBill(ctx)
		if err != nil {
			return err
		}
		if b.AttemptAt != nil && now.Sub(*b.AttemptAt) < ceReadEvery {
			return nil
		}
	}
	usd, err := s.Cloud.AccountSpend(ctx)
	if err != nil {
		s.Log.Warn("reading the account's bill failed", "err", err)
		wctx, cancel := finalCtx(ctx)
		defer cancel()
		_, err = s.DB.Exec(wctx, `UPDATE aws_ops SET account_attempt_at = $1, account_error = $2, account_error_at = $1`, now, cleanText(err.Error()))
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE aws_ops SET account_usd = $1, account_read_at = $2, account_attempt_at = $2, account_error = ''`, usd, now)
	return err
}

// claimAccountRefresh takes the shared 5-minute refresh slot atomically; wait is how long until the next one when it was taken.
func (s *Service) claimAccountRefresh(ctx context.Context) (ok bool, wait time.Duration, err error) {
	now := s.Now()
	tag, err := s.DB.Exec(ctx, `UPDATE aws_ops SET account_refresh_at = $1
		WHERE account_refresh_at IS NULL OR account_refresh_at <= $1 - make_interval(secs => $2)`, now, accountRefreshEvery.Seconds())
	if err != nil || tag.RowsAffected() == 1 {
		return err == nil, 0, err
	}
	b, err := s.accountBill(ctx)
	if err != nil || b.RefreshAt == nil {
		return false, accountRefreshEvery, err
	}
	return false, b.RefreshAt.Add(accountRefreshEvery).Sub(now), nil
}
```

  Note: `wait` from `b.RefreshAt.Add(5m).Sub(now)` is exactly `accountRefreshEvery` in the test (same clock instant).
  - `Service`: add `SpendSource string // CRUCIBLE_AWS_SPEND_SOURCE: "cost_explorer" reads hourly; "budget" every 10 minutes`.
  - `jobs.go`:
    ```go
    // AccountSpendArgs reads the AWS account's month-to-date bill (every 10 minutes; Cost Explorer at most hourly).
    // Manual is Refresh now, which already took the 5-minute slot.
    type AccountSpendArgs struct {
    	Manual bool `json:"manual"`
    }

    func (AccountSpendArgs) Kind() string { return "aws_account_spend" }

    type AccountSpendWorker struct {
    	river.WorkerDefaults[AccountSpendArgs]
    	S *Service
    }

    func (w *AccountSpendWorker) Work(ctx context.Context, j *river.Job[AccountSpendArgs]) error {
    	return w.S.ReadAccountSpend(ctx, j.Args.Manual)
    }
    ```
  - `RefreshFinOps`: after the existing CostArgs/ReapArgs inserts,
    ```go
    if ok, _, err := s.claimAccountRefresh(ctx); err != nil {
    	return nil, err
    } else if ok {
    	if _, err := s.Jobs.Insert(ctx, AccountSpendArgs{Manual: true}, once); err != nil {
    		return nil, err
    	}
    }
    ```
    (the 30-second guard for the cost and reaper runs stays; the Ledger shows when the bill may be read again).
  - `serve.go`: `river.AddWorker(workers, &labs.AccountSpendWorker{S: labSvc})`; periodic `{Every: 10 * time.Minute, Args: labs.AccountSpendArgs{}}`
    (it runs at start like the others); `SpendSource: os.Getenv("CRUCIBLE_AWS_SPEND_SOURCE")` in the `labs.Service` literal.
  - Extend `TestRefreshQueuesUniqueJobsRateLimitedAndAudited` (`internal/labs/ledger_test.go:95`): the first refresh
    also queues `aws_account_spend` (manual), and a second one within 5 minutes (past the 30 s guard) queues cost and
    reap but no second account read.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test -p 2 -parallel 2 ./internal/labs -run 'AccountSpend|CostExplorer|ManualRefresh|Stale|SpendSource|Refresh' && go test ./cmd/crucible`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): read the AWS account's bill every 10 minutes (Cost Explorer hourly); Refresh now once per 5 minutes for everyone

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/account.go internal/labs/account_test.go internal/labs/jobs.go internal/labs/ledger.go internal/labs/ledger_test.go internal/labs/service.go cmd/crucible/serve.go
```

---

### Task 5: Not yet billed (the 24-hour meter) and the marks

**Files:**
- Modify: `internal/labs/account.go`, `internal/labs/account_test.go`, `internal/labs/http.go` (route),
  `internal/labs/bedtime.go:201` and `:106-114` (night stamps), `internal/labs/reaper.go` or the sweep (purge)

**Interfaces:**
- Consumes: `lab_resources` (rows priced per hour, a sleeping lab's rows at its disk price), `billed_marks` (T1).
- Produces:
  - `const unbilledWindow = 24 * time.Hour`, `markStep = 15 * time.Minute`, `markKeep = 48 * time.Hour`.
  - `type UnbilledLab struct { ID, Team, Training, Module, Requester string; HourlyUSD float64; Intervals []UsageInterval; OwnMark *time.Time; UnbilledUSD float64 }` (json `id, team, training, module, requester, hourly_usd, intervals, own_mark, unbilled_usd`).
  - `type UsageInterval struct { From, To time.Time; HourlyUSD float64 }` (json `from, to, hourly_usd`).
  - `type UnbilledWindow struct { From, To time.Time; SleptFrom, SleptTo *time.Time; AccountMark *time.Time; Labs []UnbilledLab; USD float64 }` (json `from, to, slept_from, slept_to, account_mark, labs, unbilled_usd`).
  - `func (s *Service) unbilled(ctx context.Context) (UnbilledWindow, error)`.
  - `type MarkBody struct { LabID string \`json:"lab_id"\`; BilledTo *time.Time \`json:"billed_to"\`; Reset bool \`json:"reset"\` }`.
  - `func (s *Service) SetBilledMark(ctx context.Context, u *auth.User, b MarkBody) (*AccountBlock, error)` (the block is Task 7's; until then return `s.accountBlock(ctx, u)` stubbed in this task as returning the window only: define `AccountBlock` here with just `Window UnbilledWindow \`json:"window"\`` and let Task 7 add the rest).

- [ ] **Step 1: Write the failing tests** (append to `internal/labs/account_test.go`; `insertResource` is a small helper
  you add there: `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, stopped_hourly_usd, started_at, ended_at) VALUES ($1, $2, 'aws_instance', $3, 0, $3, $4, $5)`;
  `f.newAWSLab(t)` stands for however the existing aws tests insert a lab row: grep `aws_service_test.go` for the
  helper that makes a `ready` aws lab and use it)

```go
func TestUnbilledIntegratesTheLast24HoursAfterTheMarks(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	a, b := f.newAWSLab(t), f.newAWSLab(t)
	insertResource(t, f, a.ID, "x", 1.0, now.Add(-30*time.Hour), ptr(now.Add(-20*time.Hour))) // 4 h inside the window
	insertResource(t, f, b.ID, "y", 2.0, now.Add(-2*time.Hour), nil)                            // open: 2 h up to now
	w, err := f.s.unbilled(ctx)
	if err != nil || !closeTo(w.USD, 4*1.0+2*2.0) || !w.From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("no marks: the whole window: %v %+v", err, w)
	}
	mark(t, f, "", now.Add(-21*time.Hour)) // the account mark: a's last hour and all of b
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 1*1.0+2*2.0) {
		t.Fatalf("account mark: %v", w.USD)
	}
	mark(t, f, b.ID, now.Add(-1*time.Hour)) // b's own mark overrides it
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 1*1.0+1*2.0) {
		t.Fatalf("own mark: %v", w.USD)
	}
	mark(t, f, "", now.Add(-40*time.Hour)) // older than the window: clamped to its start
	if w, _ = f.s.unbilled(ctx); !closeTo(w.USD, 4*1.0+1*2.0) {
		t.Fatalf("clamped: %v", w.USD)
	}
	for _, l := range w.Labs {
		if l.ID == b.ID && (l.OwnMark == nil || len(l.Intervals) != 1) {
			t.Fatalf("the lab's row carries its mark and intervals: %+v", l)
		}
	}
}

func TestMarksAreForAdminsInTheWindowAndAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	lab := f.newAWSLab(t)
	insertResource(t, f, lab.ID, "x", 1.0, now.Add(-3*time.Hour), nil)
	at := now.Add(-2 * time.Hour)
	if _, err := f.s.SetBilledMark(ctx, f.leader, MarkBody{BilledTo: &at}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("admins only: %v", err)
	}
	for _, bad := range []time.Time{now.Add(-25 * time.Hour), now.Add(time.Minute)} {
		if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &bad}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "Pick a time in the last 24 hours") {
			t.Fatalf("%v must be refused: %v", bad, err)
		}
	}
	edge := now.Add(-24 * time.Hour)
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &edge}); err != nil {
		t.Fatalf("the window's edge is accepted: %v", err)
	}
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{LabID: "aaaaaaaaaaaa", BilledTo: &at}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a lab with no usage in the window is refused: %v", err)
	}
	offset := at.In(time.FixedZone("+02", 2*3600))
	blk, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{LabID: lab.ID, BilledTo: &offset})
	if err != nil || blk.Window.Labs[0].OwnMark == nil || !blk.Window.Labs[0].OwnMark.Equal(at) {
		t.Fatalf("an offset is the same instant: %v %+v", err, blk)
	}
	if _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{Reset: true}); err != nil {
		t.Fatal(err)
	}
	var marks, audits int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM billed_marks`).Scan(&marks)
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'account.billed_mark'`).Scan(&audits)
	if marks != 0 || audits != 3 {
		t.Fatalf("reset removes all; every change audited: %d marks, %d audits", marks, audits)
	}
}

func TestConcurrentMarksLastWriteWins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now}
	now := f.clk.Now()
	t1, t2 := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	done := make(chan error, 2)
	for _, at := range []time.Time{t1, t2} {
		go func() { _, err := f.s.SetBilledMark(ctx, f.admin, MarkBody{BilledTo: &at}); done <- err }()
	}
	if e1, e2 := <-done, <-done; e1 != nil || e2 != nil {
		t.Fatal(e1, e2)
	}
	var got time.Time
	_ = f.s.DB.QueryRow(ctx, `SELECT billed_to FROM billed_marks WHERE lab_id = ''`).Scan(&got)
	if !got.Equal(t1) && !got.Equal(t2) {
		t.Fatalf("one of the two: %v", got)
	}
}

func TestOldMarksArePurged(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	now := f.clk.Now()
	mark(t, f, "", now.Add(-49*time.Hour))
	mark(t, f, "bbbbbbbbbbbb", now.Add(-47*time.Hour))
	f.s.Sweep(ctx)
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM billed_marks`).Scan(&n)
	if n != 1 {
		t.Fatalf("marks older than 48 h go: %d left", n)
	}
}
```

  Helpers in the test file: `func ptr[T any](v T) *T { return &v }`; `mark(t, f, lab, at)` inserts into `billed_marks`
  directly (`set_by 'admin@x'`, `set_at` now); `closeTo` already exists in the package's tests. `f.leader` is a
  non-admin team leader in the fixture (use the fixture's leader field; if it has none, `&auth.User{Email: "leader@x"}`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test -p 2 -parallel 2 ./internal/labs -run 'Unbilled|Marks|OldMarks' -v`
Expected: FAIL to compile.

- [ ] **Step 3: Implement** (in `internal/labs/account.go`)

```go
const (
	unbilledWindow = 24 * time.Hour // Crucible's own meter covers this much behind AWS's figure
	markKeep       = 48 * time.Hour // the sweep drops older marks
)

type UsageInterval struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	HourlyUSD float64   `json:"hourly_usd"`
}

type UnbilledLab struct {
	ID          string          `json:"id"`
	Team        string          `json:"team"`
	Training    string          `json:"training"`
	Module      string          `json:"module"`
	Requester   string          `json:"requester"`
	HourlyUSD   float64         `json:"hourly_usd"`
	Intervals   []UsageInterval `json:"intervals"`
	OwnMark     *time.Time      `json:"own_mark,omitempty"`
	UnbilledUSD float64         `json:"unbilled_usd"`
}

type UnbilledWindow struct {
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	SleptFrom   *time.Time    `json:"slept_from,omitempty"`
	SleptTo     *time.Time    `json:"slept_to,omitempty"`
	AccountMark *time.Time    `json:"account_mark,omitempty"`
	Labs        []UnbilledLab `json:"labs"`
	USD         float64       `json:"unbilled_usd"`
}

// unbilled is Crucible's meter over the last 24 hours, after each lab's cut: its own mark, else the account mark, else
// the window's start (never earlier). Labs are ordered by their first usage in the window.
func (s *Service) unbilled(ctx context.Context) (UnbilledWindow, error) {
	now := s.Now()
	w := UnbilledWindow{From: now.Add(-unbilledWindow), To: now, Labs: []UnbilledLab{}}
	if err := s.DB.QueryRow(ctx, `SELECT forge_slept_at, forge_woke_at FROM aws_ops`).Scan(&w.SleptFrom, &w.SleptTo); err != nil {
		return w, err
	}
	if w.SleptFrom != nil && (w.SleptTo == nil || w.SleptTo.Before(w.From) || w.SleptTo.Before(*w.SleptFrom)) {
		w.SleptFrom, w.SleptTo = nil, nil // no night in this window
	}
	if err := s.DB.QueryRow(ctx, `SELECT billed_to FROM billed_marks WHERE lab_id = ''`).Scan(&w.AccountMark); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	rows, err := s.DB.Query(ctx, `SELECT r.lab_id, l.team, l.training, l.module, u.email, l.hourly_usd,
			greatest(r.started_at, $2), least(coalesce(r.ended_at, $1), $1), r.hourly_usd + r.usage_hourly_usd, m.billed_to
		FROM lab_resources r JOIN lab_instances l ON l.id = r.lab_id JOIN users u ON u.id = l.user_id
		LEFT JOIN billed_marks m ON m.lab_id = r.lab_id
		WHERE coalesce(r.ended_at, $1) > $2 AND r.started_at < $1
		ORDER BY r.lab_id, r.started_at`, now, w.From)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var l UnbilledLab
		var iv UsageInterval
		if err := rows.Scan(&l.ID, &l.Team, &l.Training, &l.Module, &l.Requester, &l.HourlyUSD, &iv.From, &iv.To, &iv.HourlyUSD, &l.OwnMark); err != nil {
			return w, err
		}
		i, ok := idx[l.ID]
		if !ok {
			i, idx[l.ID] = len(w.Labs), len(w.Labs)
			w.Labs = append(w.Labs, l)
		}
		w.Labs[i].Intervals = append(w.Labs[i].Intervals, iv)
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	for i := range w.Labs {
		l := &w.Labs[i]
		cut := w.From
		if l.OwnMark != nil {
			cut = maxTime(cut, *l.OwnMark)
		} else if w.AccountMark != nil {
			cut = maxTime(cut, *w.AccountMark)
		}
		for _, iv := range l.Intervals {
			if from := maxTime(iv.From, cut); iv.To.After(from) {
				l.UnbilledUSD += iv.HourlyUSD * iv.To.Sub(from).Hours()
			}
		}
		w.USD += l.UnbilledUSD
	}
	return w, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

type MarkBody struct {
	LabID    string     `json:"lab_id"`
	BilledTo *time.Time `json:"billed_to"`
	Reset    bool       `json:"reset"`
}

// SetBilledMark sets or removes one "AWS has billed this up to" mark (lab_id "" is the account's), or all of them
// (reset). Admins only, audited; the reply is the account block as the server now sees it.
func (s *Service) SetBilledMark(ctx context.Context, u *auth.User, b MarkBody) (*AccountBlock, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "only admins can mark what AWS has billed")
	}
	now := s.Now()
	actor := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	detail := map[string]any{"lab_id": b.LabID, "reset": b.Reset}
	switch {
	case b.Reset:
		_, err = tx.Exec(ctx, `DELETE FROM billed_marks`)
	case b.BilledTo == nil:
		_, err = tx.Exec(ctx, `DELETE FROM billed_marks WHERE lab_id = $1`, b.LabID)
	default:
		at := b.BilledTo.UTC()
		if at.Before(now.Add(-unbilledWindow)) || at.After(now) {
			return nil, apperr.Wrap(apperr.Invalid, "Pick a time in the last 24 hours")
		}
		if b.LabID != "" {
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_resources WHERE lab_id = $1 AND coalesce(ended_at, $2) > $3)`,
				b.LabID, now, now.Add(-unbilledWindow)).Scan(&used); err != nil {
				return nil, err
			}
			if !used {
				return nil, apperr.Wrap(apperr.Invalid, "this lab used nothing in the last 24 hours")
			}
		}
		detail["billed_to"] = at
		_, err = tx.Exec(ctx, `INSERT INTO billed_marks (lab_id, billed_to, set_by, set_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (lab_id) DO UPDATE SET billed_to = excluded.billed_to, set_by = excluded.set_by, set_at = excluded.set_at`,
			b.LabID, at, actor, now)
	}
	if err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, actor, "account.billed_mark", b.LabID, detail, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.accountBlock(ctx, u)
}

// AccountBlock is the Ledger's account line (Task 7 adds the bill, the limit and the rest).
type AccountBlock struct {
	Window UnbilledWindow `json:"window"`
}

func (s *Service) accountBlock(ctx context.Context, _ *auth.User) (*AccountBlock, error) {
	w, err := s.unbilled(ctx)
	if err != nil {
		return nil, err
	}
	return &AccountBlock{Window: w}, nil
}
```

  Imports: `errors`, `strings`, `github.com/jackc/pgx/v5`, `crucible/internal/apperr`, `crucible/internal/audit`,
  `crucible/internal/auth`, `crucible/internal/rbac`. (Check the users table and column names in `labCostSQL`'s
  neighbours, e.g. `s.requester`: if requesters are looked up some other way, join the same way.)
  - Route (`http.go`, after the finops refresh):
    ```go
    r.Put("/api/admin/account-spend/marks", func(w http.ResponseWriter, r *http.Request) {
    	var b MarkBody
    	if err := httpx.Read(r, &b); err != nil {
    		httpx.Error(w, err)
    		return
    	}
    	v, err := s.SetBilledMark(r.Context(), user(r), b)
    	reply(w, v, err)
    })
    ```
  - Night stamps: in `bedtime.go` where the phase becomes `asleep` (line ~201) also
    `UPDATE aws_ops SET forge_slept_at = $1` (now); in `Morning()` inside its transaction
    `UPDATE aws_ops SET forge_woke_at = $1`. Test (append): after `bedtimeStep` reaches `asleep` and `Morning`, the
    window's `SleptFrom`/`SleptTo` are those instants.
  - Purge: in `Sweep` (or the reaper's start), `DELETE FROM billed_marks WHERE set_at < $1` with `now - markKeep` —
    ignore errors except logging, as the sweep's other housekeeping does.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test -p 2 -parallel 2 ./internal/labs -run 'Unbilled|Marks|OldMarks|Bedtime|Morning'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): count Crucible's meter for the last 24 h on top of the bill; admins mark what AWS already billed (account or per lab)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/account.go internal/labs/account_test.go internal/labs/http.go internal/labs/bedtime.go internal/labs/reaper.go
```

(Use the files you actually touched for the purge and the night stamps.)

---

### Task 6: The gate (`overCap`'s account scope, `cap_reason`) and the alerts

**Files:**
- Modify: `internal/labs/finops.go:20-31` (`overCap`), `:46-82` (`CheckBudgets`), `internal/labs/service.go` (quote at
  `:628-636`, the extension calls at `:1375` and `:1399`, `Instance`/its JSON and the SELECT/INSERT column lists at
  `:194-202`, `:710-714`), `internal/labs/approvals.go` (`Approval.CapReason`, `Decide` at `:455-475`, extensions at `:241`)
- Test: `internal/labs/account_test.go`, existing finops/approvals tests still pass

**Interfaces:**
- Consumes: `accountBill`, `unbilled` (T4, T5), `Settings.AccountMonthlyLimitUSD` (T1).
- Produces:
  - `overCap(ctx, p, team, training string, estimateUSD float64) (reason string, err error)`: `""`, `"team"` or `"account"` (team first).
  - `type AccountTotals struct { BilledUSD, UnbilledUSD, ReservedUSD, LimitUSD float64; Stale bool; Bill AccountBill; Window UnbilledWindow }`,
    `func (s *Service) accountTotals(ctx context.Context, p *config.Platform) (AccountTotals, error)`, `func (t AccountTotals) TotalUSD() float64` (billed + unbilled + reserved).
  - `Instance.CapReason string` (json `cap_reason`), `Approval.CapReason string` (json `cap_reason`), and on an
    approval over the account limit `Approval.AccountUSD float64` (json `account_usd`: billed + not yet billed +
    reserved) and `Approval.AccountLimitUSD float64` (json `account_limit_usd`), read once per `Approvals` call with
    `accountTotals` and only when some listed request has `cap_reason = account`.

- [ ] **Step 1: Write the failing tests** (append to `internal/labs/account_test.go`; `setLimit(t, f, 600)` updates the
  fixture platform's `Settings.AccountMonthlyLimitUSD` the way other tests change platform settings in the fixture;
  `f.request(t, …)` is the helper the approval tests use to request a paid aws lab with a given hourly price, which
  returns the `*Instance`; adapt to the real helper names)

```go
func TestAccountLimitSendsPaidRequestsToAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 600)
	cloud.SetAccountSpend(590)
	_ = f.s.ReadAccountSpend(ctx, false)
	reason, err := f.s.overCap(ctx, f.platform(), f.team, f.training, 5)
	if err != nil || reason != "" {
		t.Fatalf("590 + 5 is under 600: %q %v", reason, err)
	}
	if reason, _ = f.s.overCap(ctx, f.platform(), f.team, f.training, 11); reason != "account" {
		t.Fatalf("590 + 11 passes 600: %q", reason)
	}
}

func TestReservedAndUnbilledCountTowardTheLimit(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 100)
	cloud.SetAccountSpend(80)
	_ = f.s.ReadAccountSpend(ctx, false)
	now := f.clk.Now()
	run := f.newAWSLab(t)                                                                          // ready, $1/h, 5 h left
	_, _ = f.s.DB.Exec(ctx, `UPDATE lab_instances SET hourly_usd = 1, ends_at = $2 WHERE id = $1`, run.ID, now.Add(5*time.Hour))
	insertResource(t, f, run.ID, "x", 1.0, now.Add(-10*time.Hour), nil)                             // 10 h unbilled
	tot, err := f.s.accountTotals(ctx, f.platform())
	if err != nil || !closeTo(tot.ReservedUSD, 5) || !closeTo(tot.UnbilledUSD, 10) || !closeTo(tot.TotalUSD(), 95) {
		t.Fatalf("80 billed + 10 unbilled + 5 reserved: %+v %v", tot, err)
	}
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 6); r != "account" {
		t.Fatal("95 + 6 passes 100")
	}
}

func TestAStaleOrMissingBillFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 1000)
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 1); r != "account" {
		t.Fatal("never read: an admin decides")
	}
	_ = f.s.ReadAccountSpend(ctx, false)
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 1); r != "" {
		t.Fatal("fresh and far under")
	}
	f.clk.Add(37 * time.Hour)
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 1); r != "account" {
		t.Fatal("37 h old: an admin decides")
	}
}

func TestNoLimitOrNoAWSMeansNoAccountCheck(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 1e6); r == "account" {
		t.Fatal("no Cloud: no account bill, no account check")
	}
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now} // never read, but no limit set
	if r, _ := f.s.overCap(ctx, f.platform(), f.team, f.training, 1e6); r == "account" {
		t.Fatal("no limit: no account check")
	}
}

func TestApprovingAfterTheLimitIsReachedGoesToAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 600)
	cloud.SetAccountSpend(100)
	_ = f.s.ReadAccountSpend(ctx, false)
	inst := f.request(t, 2.0) // waits for the team's approver, under the limit
	cloud.SetAccountSpend(599)
	_ = f.s.ReadAccountSpend(ctx, false)
	if _, err := f.s.Decide(ctx, f.approver, inst.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("handed to an admin: %v", err)
	}
	var reason, tier string
	_ = f.s.DB.QueryRow(ctx, `SELECT cap_reason, tier FROM lab_instances WHERE id = $1`, inst.ID).Scan(&reason, &tier)
	if reason != "account" || tier != "admin" {
		t.Fatalf("%q %q", reason, tier)
	}
	if st, err := f.s.Decide(ctx, f.admin, inst.ID, true, "checked the bill"); err != nil || st != Provisioning {
		t.Fatalf("an admin approves it as an override: %v %v", st, err)
	}
}

func TestFreeLabsAreNeverCheckedAgainstTheAccountLimit(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{Now: f.clk.Now} // never read: would fail closed
	setLimit(t, f, 1)
	inst := f.request(t, 0) // a free lab
	if inst.OverCap || inst.CapReason != "" {
		t.Fatalf("a free lab never costs money: %+v", inst)
	}
	_ = ctx
}

func TestAccountAlertsOncePerLevel(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	setLimit(t, f, 100)
	cloud.SetAccountSpend(85)
	_ = f.s.ReadAccountSpend(ctx, false)
	_ = f.s.CheckBudgets(ctx)
	_ = f.s.CheckBudgets(ctx)
	if n := f.notes.count(notify.BudgetAlert); n != 1 {
		t.Fatalf("80%%: one email: %d", n)
	}
	cloud.SetAccountSpend(101)
	_ = f.s.ReadAccountSpend(ctx, false)
	_ = f.s.CheckBudgets(ctx)
	f.clk.Add(37 * time.Hour)
	_ = f.s.CheckBudgets(ctx)
	_ = f.s.CheckBudgets(ctx)
	if n := f.notes.count(notify.BudgetAlert); n != 3 {
		t.Fatalf("then 100%% and stale, once each: %d", n)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test -p 2 -parallel 2 ./internal/labs -run 'AccountLimit|Reserved|Stale|NoLimit|ApprovingAfter|FreeLabs|AccountAlerts' -v`
Expected: FAIL to compile (`accountTotals`, `CapReason`; `overCap` returns bool).

- [ ] **Step 3: Implement**
  - `account.go`:
    ```go
    type AccountTotals struct {
    	BilledUSD, UnbilledUSD, ReservedUSD, LimitUSD float64
    	Stale                                         bool
    	Bill                                          AccountBill
    	Window                                        UnbilledWindow
    }

    func (t AccountTotals) TotalUSD() float64 { return t.BilledUSD + t.UnbilledUSD + t.ReservedUSD }

    // accountTotals: the bill, Crucible's meter since the marks, and what labs not yet ended may still spend (a starting
    // lab's estimate; a running, sleeping or waking lab's hourly price for its time left), across every team.
    func (s *Service) accountTotals(ctx context.Context, p *config.Platform) (AccountTotals, error) {
    	t := AccountTotals{LimitUSD: p.Settings.AccountMonthlyLimitUSD}
    	var err error
    	if t.Bill, err = s.accountBill(ctx); err != nil {
    		return t, err
    	}
    	t.BilledUSD, t.Stale = t.Bill.USD, t.Bill.Stale(s.Now())
    	if t.Window, err = s.unbilled(ctx); err != nil {
    		return t, err
    	}
    	t.UnbilledUSD = t.Window.USD
    	err = s.DB.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN state = 'provisioning' THEN estimate_usd
    			ELSE hourly_usd * extract(epoch FROM greatest(coalesce(ends_at, $1) - $1, interval '0'))::float8 / 3600 END), 0)
    		FROM lab_instances WHERE state IN ('provisioning', 'ready', 'asleep', 'waking')`, s.Now()).Scan(&t.ReservedUSD)
    	return t, err
    }
    ```
  - `overCap` (finops.go) returns `(string, error)`:
    ```go
    func (s *Service) overCap(ctx context.Context, p *config.Platform, team, training string, estimateUSD float64) (string, error) {
    	for _, scope := range []string{"", training} {
    		sp, err := s.spend(ctx, p, team, scope)
    		if err != nil {
    			return "", err
    		}
    		if sp.CapUSD > 0 && sp.CommittedUSD+estimateUSD > sp.CapUSD {
    			return "team", nil
    		}
    	}
    	if s.Cloud == nil || p.Settings.AccountMonthlyLimitUSD <= 0 {
    		return "", nil
    	}
    	t, err := s.accountTotals(ctx, p)
    	if err != nil {
    		return "", err
    	}
    	if t.Stale || t.TotalUSD()+estimateUSD > t.LimitUSD {
    		return "account", nil // a stale bill fails closed: an admin decides
    	}
    	return "", nil
    }
    ```
    Update every caller: `quote` sets `q.OverCap, q.Tier, q.CapReason = true, rbac.TierAdmin, reason` when `reason != ""`
    (add `CapReason string` to `quote` and carry it into the INSERT at `service.go:710-714` and the `Instance` it
    returns); the extension callers treat `reason != ""` as today's `over` and expose it as `Approval.CapReason`;
    `Decide`'s re-check updates `SET over_cap = true, cap_reason = $3, tier = 'admin', escalate_at = $2` and its message
    for the account reason reads *approving this would now pass the account's monthly limit, so it has been passed to an
    admin* (the team one keeps today's words).
  - `Instance`: `CapReason string \`json:"cap_reason,omitempty"\``; add `cap_reason` to the SELECT list and Scan at
    `service.go:194-202` and to the view at `:431`. `Approval`: `CapReason string \`json:"cap_reason,omitempty"\``
    filled from the instance (and from the live extension check at `approvals.go:241`).
  - `CheckBudgets`: after the team loop,
    ```go
    if s.Cloud != nil && p.Settings.AccountMonthlyLimitUSD > 0 {
    	t, err := s.accountTotals(ctx, p)
    	if err != nil {
    		return err
    	}
    	var levels []int
    	if t.Stale {
    		levels = append(levels, 0) // 0 = the bill could not be read
    	}
    	if t.TotalUSD() >= 0.8*t.LimitUSD {
    		levels = append(levels, 80)
    	}
    	if t.TotalUSD() >= t.LimitUSD {
    		levels = append(levels, 100)
    	}
    	for _, lvl := range levels {
    		tag, err := s.DB.Exec(ctx, `INSERT INTO budget_alerts (scope, month, level) VALUES ('account', $1, $2) ON CONFLICT DO NOTHING`, month, lvl)
    		if err != nil {
    			return err
    		}
    		if tag.RowsAffected() == 0 {
    			continue
    		}
    		subject, text := "The AWS account used 80% of its monthly limit",
    			fmt.Sprintf("The AWS account is at $%.2f of its $%.2f monthly limit (billed, not yet billed and reserved).", t.TotalUSD(), t.LimitUSD)
    		switch lvl {
    		case 100:
    			subject, text = "The AWS account reached its monthly limit",
    				fmt.Sprintf("The AWS account is at $%.2f, past its $%.2f monthly limit. New paid lab requests now need an admin.", t.TotalUSD(), t.LimitUSD)
    		case 0:
    			subject, text = "Crucible can't read the AWS account's bill",
    				"The account's bill hasn't been read for 36 hours, so new paid lab requests need an admin. See the Ledger: "+t.Bill.Error
    		}
    		s.notify(ctx, notify.Event{Kind: notify.BudgetAlert, To: p.Admins, Subject: subject, Text: text, Link: "/ledger"})
    	}
    }
    ```

- [ ] **Step 4: Run all labs tests**

Run: `go vet ./... && go test -race -p 2 -parallel 2 ./internal/labs`
Expected: PASS (every existing over-cap test still passes with the `(string, error)` signature).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): past the account's monthly limit (billed + not yet billed + reserved), paid labs need an admin; it fails closed; admins are emailed

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/account.go internal/labs/account_test.go internal/labs/finops.go internal/labs/service.go internal/labs/approvals.go
```

---

### Task 7: The Ledger's account block (admins and approvers)

**Files:**
- Modify: `internal/labs/account.go` (`AccountBlock`, `accountBlock`), `internal/labs/ledger.go` (`Ledger.Account`),
  `internal/rbac/rbac.go` (`IsApprover`), `internal/rbac/rbac_test.go`
- Test: `internal/labs/account_test.go`

**Interfaces:**
- Produces (JSON the web consumes; field names exact):
  ```go
  type AccountBlock struct {
  	BilledUSD    float64        `json:"billed_usd"`
  	ReadAt       *time.Time     `json:"read_at,omitempty"`
  	UnbilledUSD  float64        `json:"unbilled_usd"`
  	ReservedUSD  float64        `json:"reserved_usd"`
  	LimitUSD     float64        `json:"limit_usd"` // 0 = none
  	Source       string         `json:"source"`    // budget | cost_explorer | ""
  	Stale        bool           `json:"stale"`
  	Error        string         `json:"error,omitempty"`          // admins only
  	RefreshAfter *time.Time     `json:"refresh_after,omitempty"` // admins only: when Refresh now may read the bill again
  	CanMark      bool           `json:"can_mark"`                // admins
  	Window       UnbilledWindow `json:"window"`
  }
  ```
  `Ledger.Account *AccountBlock \`json:"account,omitempty"\`` (nil without AWS labs or for viewers who are neither
  admins nor approvers). `rbac.Checker.IsApprover(email string) bool`: an admin, a leader of any team, or in any
  program's `Roles.Approvers`.

- [ ] **Step 1: Write the failing tests**

```go
func TestTheLedgerShowsTheAccountToAdminsAndApproversOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	cloud.SetAccountSpend(412.37)
	f.s.Cloud, f.s.SpendSource = cloud, "budget"
	setLimit(t, f, 600)
	_ = f.s.ReadAccountSpend(ctx, false)
	a, err := f.s.Ledger(ctx, f.admin)
	if err != nil || a.Account == nil || a.Account.BilledUSD != 412.37 || a.Account.LimitUSD != 600 || !a.Account.CanMark || a.Account.Source != "budget" {
		t.Fatalf("admin: %+v %v", a.Account, err)
	}
	ap, _ := f.s.Ledger(ctx, f.approver)
	if ap.Account == nil || ap.Account.CanMark || ap.Account.Error != "" || ap.Account.RefreshAfter != nil {
		t.Fatalf("an approver sees the totals, read-only, without admin detail: %+v", ap.Account)
	}
	if m, err := f.s.Ledger(ctx, f.manager); err == nil && m.Account != nil {
		t.Fatal("a program manager who doesn't approve doesn't see the account")
	}
}
```

  plus an `rbac_test.go` table test for `IsApprover` (admin yes, a team's leader yes, a program approver yes, a program
  manager no, a trainee no). `f.approver` / `f.manager` are fixture users with those roles; add them to the fixture's
  platform if missing.

- [ ] **Step 2: Run to fail**

Run: `go test ./internal/rbac -run IsApprover && go test -p 2 -parallel 2 ./internal/labs -run TheLedgerShowsTheAccount -v`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**
  - `accountBlock(ctx, u)`:
    ```go
    func (s *Service) accountBlock(ctx context.Context, u *auth.User) (*AccountBlock, error) {
    	st, err := s.platform()
    	if err != nil {
    		return nil, err
    	}
    	c := rbac.Checker{P: st.Platform}
    	if s.Cloud == nil || !c.IsApprover(u.Email) {
    		return nil, nil
    	}
    	t, err := s.accountTotals(ctx, st.Platform)
    	if err != nil {
    		return nil, err
    	}
    	b := &AccountBlock{BilledUSD: t.BilledUSD, ReadAt: t.Bill.ReadAt, UnbilledUSD: t.UnbilledUSD, ReservedUSD: t.ReservedUSD,
    		LimitUSD: t.LimitUSD, Source: s.SpendSource, Stale: t.Stale, Window: t.Window}
    	if c.IsAdmin(u.Email) {
    		b.Error, b.CanMark = t.Bill.Error, true
    		if t.Bill.RefreshAt != nil {
    			after := t.Bill.RefreshAt.Add(accountRefreshEvery)
    			b.RefreshAfter = &after
    		}
    	}
    	return b, nil
    }
    ```
    `SetBilledMark` now returns this full block.
  - `Ledger`: `if l.Account, err = s.accountBlock(ctx, u); err != nil { return nil, err }` near the other AWS fields.
  - `rbac.IsApprover`:
    ```go
    // IsApprover: anyone who can decide a lab request somewhere (admins, team leaders, program approvers).
    func (c Checker) IsApprover(email string) bool {
    	if c.IsAdmin(email) {
    		return true
    	}
    	for _, t := range c.P.Teams {
    		if slices.Contains(t.Leaders, email) {
    			return true
    		}
    		for _, p := range t.Programs {
    			if slices.Contains(p.Roles.Approvers, email) {
    				return true
    			}
    		}
    	}
    	return false
    }
    ```
    (match the email normalisation the other `Checker` methods use, e.g. `strings.ToLower`).

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./internal/rbac && go test -race -p 2 -parallel 2 ./internal/labs && go test ./internal/docs`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): the Ledger's account block (bill, not yet billed, reserved, limit, the 24 h window) for admins and approvers

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/account.go internal/labs/account_test.go internal/labs/ledger.go internal/rbac/rbac.go internal/rbac/rbac_test.go
```

---

### Task 8: The UI (Ledger account line, the timeline, settings, approvals, the lab page)

Read `.claude/skills/crucible-ui/SKILL.md` and `web/src/pages/Ledger.tsx` first. The approved mockup is
`.superpowers/brainstorm/*/content/billed-marker-v2.html`: match its behaviour, in the app's tokens and components.

**Files:**
- Create: `web/src/lib/billedTimeline.ts`, `web/src/lib/billedTimeline.test.ts`, `web/src/components/BilledTimeline.tsx`,
  `web/src/components/BilledTimeline.test.tsx`
- Modify: `web/src/types.ts`, `web/src/pages/Ledger.tsx`, `web/src/pages/Ledger.test.tsx`, `web/src/pages/AdminSettings.tsx`,
  `web/src/pages/Approvals.tsx:69`, `web/src/pages/Lab.tsx:161`, their tests, `web/src/theme/app.css`, `.claude/skills/crucible-ui/SKILL.md`

**Interfaces:**
- Consumes: Task 7's `account` JSON, `PUT /api/admin/account-spend/marks` (Task 5), `cap_reason` (Task 6),
  `account_monthly_limit_usd` in `/api/admin/settings` (Task 1).

- [ ] **Step 1: Types** (`types.ts`)

```ts
export type UsageInterval = { from: string; to: string; hourly_usd: number }
export type UnbilledLab = { id: string; team: string; training: string; module: string; requester: string; hourly_usd: number
  intervals: UsageInterval[]; own_mark?: string; unbilled_usd: number }
export type UnbilledWindow = { from: string; to: string; slept_from?: string; slept_to?: string; account_mark?: string
  labs: UnbilledLab[]; unbilled_usd: number }
export type AccountBlock = { billed_usd: number; read_at?: string; unbilled_usd: number; reserved_usd: number; limit_usd: number
  source: '' | 'budget' | 'cost_explorer'; stale: boolean; error?: string; refresh_after?: string; can_mark: boolean; window: UnbilledWindow }
```

  Add `account?: AccountBlock` to `Ledger`, `cap_reason?: '' | 'team' | 'account'` to the lab instance and approval
  types, `account_monthly_limit_usd: number` to `AdminSettings`.

- [ ] **Step 2: Write the failing pure-logic tests** (`web/src/lib/billedTimeline.test.ts`)

```ts
import { expect, test } from 'vitest'
import { cutFor, split, unbilledTotal, snap, step, at, summary } from './billedTimeline'
import type { UnbilledWindow } from '../types'

const H = 3600_000
const to = Date.parse('2026-11-14T14:00:00Z'), from = to - 24 * H
const iso = (ms: number) => new Date(ms).toISOString()
const win = (extra: Partial<UnbilledWindow> = {}): UnbilledWindow => ({ from: iso(from), to: iso(to), unbilled_usd: 0, labs: [
  { id: 'a', team: 't', training: 'forge-401', module: 'm', requester: 'dana@x', hourly_usd: 1, unbilled_usd: 0,
    intervals: [{ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }] },
  { id: 'b', team: 't', training: 'forge-402', module: 'm', requester: 'omer@x', hourly_usd: 2, unbilled_usd: 0,
    intervals: [{ from: iso(to - 2 * H), to: iso(to), hourly_usd: 2 }] },
], ...extra })

test('a lab is cut at its own mark, else the account mark, else the window start', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(cutFor(w.labs[0], w.account_mark, {}, from)).toBe(from + 3 * H)
  expect(cutFor(w.labs[0], w.account_mark, { a: from + 4 * H }, from)).toBe(from + 4 * H)
  expect(cutFor(w.labs[0], undefined, {}, from)).toBe(from)
  expect(cutFor(w.labs[0], iso(from - 5 * H), {}, from)).toBe(from) // clamped
})

test('an interval splits into its billed and counted parts at the cut', () => {
  expect(split({ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }, from + 2 * H)).toEqual({ billedFrac: 0.25, usd: 3 })
  expect(split({ from: iso(from + 1 * H), to: iso(from + 5 * H), hourly_usd: 1 }, from + 9 * H)).toEqual({ billedFrac: 1, usd: 0 })
})

test('the total and the summary follow the marks', () => {
  const w = win({ account_mark: iso(from + 3 * H) })
  expect(unbilledTotal(w, w.account_mark, {})).toEqual({ usd: 2 + 4, labs: 2 })
  expect(summary(w, w.account_mark, { b: to - 1 * H })).toBe('Crucible adds $4.00 from 2 labs on top of the AWS bill · 1 lab set on its own')
  expect(summary(w, iso(to), {})).toBe('Crucible adds $0.00 from 0 labs on top of the AWS bill')
})

test('marks snap to 15 minutes, step by 15 with the arrow keys, and stay in the window', () => {
  expect(snap(from + 7 * 60_000, from, to)).toBe(from + 0)
  expect(snap(from + 8 * 60_000, from, to)).toBe(from + 15 * 60_000)
  expect(step(from, -1, from, to)).toBe(from)
  expect(step(to - 10 * 60_000, 1, from, to)).toBe(to)
  expect(at(from + 12 * H, from, to)).toBe(50) // percent across
})
```

- [ ] **Step 3: Run to fail**

Run: `cd web && npx vitest run src/lib/billedTimeline.test.ts`
Expected: FAIL (module not found).

- [ ] **Step 4: Implement `web/src/lib/billedTimeline.ts`**

```ts
import type { UnbilledLab, UnbilledWindow, UsageInterval } from '../types'

const Q = 15 * 60_000 // marks snap and step by 15 minutes
const ms = (s?: string) => (s ? Date.parse(s) : undefined)

// cutFor: a lab's own mark, else the account mark, else the window's start; never before the window.
export function cutFor(lab: UnbilledLab, accountMark: string | undefined, own: Record<string, number>, from: number): number {
  const c = own[lab.id] ?? ms(lab.own_mark) ?? ms(accountMark) ?? from
  return Math.max(c, from)
}

// split: how much of an interval lies left of the cut (already billed), and the dollars right of it.
export function split(iv: UsageInterval, cut: number): { billedFrac: number; usd: number } {
  const f = Date.parse(iv.from), t = Date.parse(iv.to)
  const b = Math.min(Math.max(cut, f), t)
  return { billedFrac: t > f ? (b - f) / (t - f) : 1, usd: iv.hourly_usd * (t - b) / 3_600_000 }
}

export function unbilledTotal(w: UnbilledWindow, accountMark: string | undefined, own: Record<string, number>) {
  let usd = 0, labs = 0
  for (const l of w.labs) {
    const cut = cutFor(l, accountMark, own, Date.parse(w.from))
    const u = l.intervals.reduce((s, iv) => s + split(iv, cut).usd, 0)
    if (u > 0.004) { usd += u; labs++ }
  }
  return { usd, labs }
}

export function summary(w: UnbilledWindow, accountMark: string | undefined, own: Record<string, number>): string {
  const { usd, labs } = unbilledTotal(w, accountMark, own)
  const sep = w.labs.filter((l) => own[l.id] !== undefined || l.own_mark).length
  return `Crucible adds $${usd.toFixed(2)} from ${labs} lab${labs === 1 ? '' : 's'} on top of the AWS bill` +
    (sep ? ` · ${sep} lab${sep === 1 ? '' : 's'} set on ${sep === 1 ? 'its' : 'their'} own` : '')
}

export const snap = (t: number, from: number, to: number) => Math.min(Math.max(from + Math.round((t - from) / Q) * Q, from), to)
export const step = (t: number, dir: -1 | 1, from: number, to: number) => Math.min(Math.max(t + dir * Q, from), to)
export const at = (t: number, from: number, to: number) => ((t - from) / (to - from)) * 100
```

  (The `own` map holds a lab's mark while it is being dragged, before the server's reply; `lab.own_mark` is the saved
  one.) Run the test: PASS.

- [ ] **Step 5: The component** (`web/src/components/BilledTimeline.tsx`). Write its render test first
  (`BilledTimeline.test.tsx`, `renderToStaticMarkup`):
  - admin (`can_mark`): the account marker `role="slider"` with `aria-label="Account: AWS has billed usage up to"` and
    the text *Account billed up to 09:00 ⇆*; each row a **Set separately** button; **Count all 24 h**; the summary line.
  - a lab with `own_mark`: a ◆ before its name, a second slider labelled *Billed up to, forge-402 (omer@x), set on its
    own*, and **Follow account**.
  - approver (`can_mark: false`): bars and summary, no sliders, no buttons.
  - `slept_from`/`slept_to` in the window: an element with the text *forge asleep*.
  - the legend in words: *In the AWS bill*, *Counted by Crucible*, *Account marker*, *◆ This lab set on its own*.

  Then implement:
  - Props: `{ w: UnbilledWindow; canMark: boolean; onSaved: (b: AccountBlock) => void }`.
  - Layout: `section.panel.billed-timeline` with an `h5` *Not yet on the AWS bill · last 24 hours*, the summary
    (`p[role=status]`), one muted line *Check the month-to-date cost in the AWS console, then drag the green marker to
    the time it covers. Time left of a marker is already in the bill; Crucible adds only what's right of it.*, the axis
    (every 4 hours, *now* at the end, times in the browser's zone, `HH:MM`), the night band, one row per lab (name,
    requester, `$x.xx/h`; bars positioned with `at()`; each bar's inner `.billed` width `billedFrac` and `.hot` the
    rest), the account marker, own markers, the legend.
  - Dragging: pointer events on the marker (`setPointerCapture`), position → time → `snap`; keep the dragged value in
    local state; on `pointerup` send `PUT /api/admin/account-spend/marks` with `{ lab_id: '' | id, billed_to: ISO }`
    through the app's `api()` helper; on success `onSaved(reply)` and `toast('Marked as billed up to HH:MM.')`; on
    failure put the marker back and `reportSaveError` (the app's helper) shows the message.
  - Keyboard: the slider takes `ArrowLeft`/`ArrowRight` (`step`), `Home`/`End` (window start / now); `aria-valuemin`,
    `aria-valuemax`, `aria-valuenow` as epoch ms and `aria-valuetext` as `HH:MM`; it saves on `keyup` of an arrow key
    after 400 ms without another press.
  - **Set separately** sends the lab's mark at the account mark's time (or the window start); **Follow account** sends
    `billed_to: null`; **Count all 24 h** sends `{ reset: true }` after `window.confirm('Count all of the last 24 hours on top of the AWS bill? This removes every mark.')`.
  - At ≤640px the row's name sits above its bar (CSS grid change), as the skill's breakpoints say.
  - CSS (`app.css`, a new block *Billed timeline: the Ledger's last 24 hours*): bars `--surface-2`; `.billed` a hatch
    from `color-mix(in srgb, var(--muted) 45%, transparent)`; `.hot` the molten gradient used by `MoltenBar`
    (`--accent` → `--accent-2`); the account marker `--ok`; own markers `--accent-2`'s complement is not a token, so use
    `--text` with a `--accent-2` ring; the night band a hatch of `--border`. No hex in `app.css`.

- [ ] **Step 6: The Ledger, settings, approvals and lab page**
  - `Ledger.tsx`: when `l.account`, above *Budgets*: the line *AWS account this month: $412.37 billed (as of 14:20) +
    $4.13 not yet billed + $38.00 reserved by running labs = $454.50 of $600.00* (without a limit: *… = $454.50*), a
    `meter` (`min 0`, `max limit`, `low 0.8×limit`, `high limit`) when a limit is set, the muted explanation line, the
    stale line in `error` (*Out of date: new paid labs need an admin until the bill is read again.*, then admins see
    *Last error: …*), and the `BilledTimeline`. The existing Refresh button's note gains, for admins with
    `refresh_after` in the future, *The AWS bill can be read again in m:ss.* (a 1-second ticking text; stops at 0).
    Tests in `Ledger.test.tsx`: the line with and without a limit, stale, the cooldown note, no block without `account`.
  - `AdminSettings.tsx` *Lab spending* section: `<label>Monthly account limit (USD) <input type="number" min={0}
    step="0.01" …/></label>` with the hint as a `p.muted` after it; empty sends `0`. A test that the field renders the
    saved value.
  - `Approvals.tsx:69`: `cap_reason === 'account'` → *Would pass the account's monthly limit ($450.37 of $600.00): only
    an admin can approve it.* from `account_usd` and `account_limit_usd`; otherwise today's words. A render test.
  - `Lab.tsx:161`: `cap_reason === 'account'` → *Waiting for an admin: the forge's monthly spend limit is reached.*;
    otherwise today's words.
  - `.claude/skills/crucible-ui/SKILL.md`: a **Billed timeline** row in the patterns table (what it's for, the marker
    roles and keyboard, the hatched/molten split, the summary, save on release with a toast).

- [ ] **Step 7: Run the web checks**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: PASS. Then look at it (the skill's step 3) in Forge, Anvil, Parchment and High Contrast, at phone width, and
with Calm forge, against a running stack with a dry-run AWS lab (`KEEP=1` cluster-check, or after Task 10).

- [ ] **Step 8: Commit**

```bash
git commit -m "feat(web): the Ledger's account line and the billed timeline (drag what AWS already billed); the account limit setting; over-limit copy

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/types.ts web/src/lib/billedTimeline.ts web/src/lib/billedTimeline.test.ts web/src/components/BilledTimeline.tsx web/src/components/BilledTimeline.test.tsx web/src/pages/Ledger.tsx web/src/pages/Ledger.test.tsx web/src/pages/AdminSettings.tsx web/src/pages/Approvals.tsx web/src/pages/Lab.tsx web/src/theme/app.css .claude/skills/crucible-ui/SKILL.md
```

(Add the page tests you touched.)

---

### Task 9: Docs

**Files:** `docs/user/admins/` (the page about Forge settings and the one about the Ledger/kill switch: grep for
*cost tiers* and *Refresh now*), `docs/user/leaders/ledger-and-content-versions.md`, `docs/runbooks/aws.md`,
`docs/aws-deploy.html`, `docs/deploy-architecture.html`, `docs/test-guide.html`, `docs/cli.md`, `CLAUDE.md`

- [ ] **Step 1: Write them**
  - Admins: the **Monthly account limit (USD)** setting (what it counts: the account's bill, Crucible's meter for the
    last 24 hours, what running labs have reserved; past it, paid labs need an admin; a stale bill does the same); the
    Ledger's account line; the timeline (check the console's month-to-date cost, drag the green marker; *Set separately*
    for one lab; *Count all 24 h*); Refresh now once per 5 minutes.
  - Leaders: approvers see the account line read-only.
  - Runbook *Labs overnight*-style section **The account limit**: `--spend-source`, the budget always exists (alerts only
    with emails), Cost Explorer's cost ($0.01 a call, hourly, about $2.60 a month while the node is up), the 36-hour
    fail-closed rule, and the by-hand checks: labs-init creates the budget (or the SCP refuses it: then
    `--spend-source=cost-explorer`); the Ledger's billed figure matches the console's month-to-date cost; the next bill
    shows no charge for Budgets reads. Our account's step 6 gains nothing (budget is the default).
  - `aws-deploy.html` (labs-init flags, the budget), `deploy-architecture.html` (the ops role's `budgets:ViewBudget` /
    `ce:GetCostAndUsage`; the budget row), `test-guide.html` (a checklist item: set a low limit, the next request waits
    for an admin; drag the marker and watch the total), `docs/cli.md` (`--spend-source` in the labs-init rows).
  - `CLAUDE.md` *Money* invariant: *the account limit (Forge settings) counts the AWS account's bill (an AWS Budget, else
    Cost Explorer) + Crucible's meter for the last 24 h after admins' marks + what running labs reserve; past it, or
    with a bill older than 36 h, paid labs need an admin.*

- [ ] **Step 2: Run** `go test ./internal/docs` → PASS.

- [ ] **Step 3: Commit** with the pathspec of every doc touched: `docs: the account limit, the billed timeline, --spend-source`.

---

### Task 10: The end-to-end journey

**Files:** `e2e/tests/aws-lab.spec.ts`

- [ ] **Step 1: Write the journey** (beside the sleep journey; reuse its `login`, `addParticipant` helpers)

```ts
test('past the account limit a paid lab waits for an admin, who approves it as an override', async ({ browser }) => {
  const admin = await login(browser, 'admin')
  await admin.goto('/admin/settings')
  await admin.getByLabel('Monthly account limit (USD)').fill('1') // the dry run's bill is $12.34
  await admin.getByRole('button', { name: 'Save settings' }).click()
  await expect(admin.getByText('Settings saved.')).toBeVisible()

  const leader = await login(browser, 'leader')
  await addParticipant(leader, 'forge-401', 'trainee@crucible.local')
  const trainee = await login(browser, 'trainee')
  await trainee.goto('/p/forge/forge-401/m/01-cloud-heat/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.04)' }).click()
  await expect(trainee.getByText("Waiting for an admin: the forge's monthly spend limit is reached.")).toBeVisible()

  await leader.getByRole('link', { name: 'Approvals' }).click()
  await expect(leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' })).toHaveCount(0)

  await admin.getByRole('link', { name: 'Ledger' }).click()
  await expect(admin.getByText(/AWS account this month: \$12\.34 billed/)).toBeVisible()
  await admin.getByRole('link', { name: 'Approvals' }).click()
  const req = admin.getByRole('listitem', { name: 'Request from trainee@crucible.local' })
  await expect(req.getByText(/Would pass the account's monthly limit \(\$\d+\.\d\d of \$1\.00\): only an admin can approve it\./)).toBeVisible()
  await req.getByRole('button', { name: 'Approve' }).click()
  await expect(trainee.getByRole('tab', { name: 'workspace', exact: true })).toBeVisible({ timeout: 10 * 60_000 })

  await trainee.getByRole('button', { name: 'End lab' }).click()
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 5 * 60_000 })
  await admin.goto('/admin/settings') // leave the forge as the other journeys expect it
  await admin.getByLabel('Monthly account limit (USD)').fill('')
  await admin.getByRole('button', { name: 'Save settings' }).click()
})
```

  Adjust the settings route and link names to the app's real ones (grep `AdminSettings` in `web/src/App.tsx`).

- [ ] **Step 2: Run it against a kept stack, then the full suite** (memory *fast e2e loop*: iterate one spec against
  `KEEP=1`, full `cluster-check` only at the end)

Run: `KEEP=1 KEYCLOAK_PORT=8082 make cluster-check` (once), iterate with the single spec, then the full
`KEYCLOAK_PORT=8082 make cluster-check`.
Expected: every journey passes (15 with this one).

- [ ] **Step 3: Commit** `-- e2e/tests/aws-lab.spec.ts`: `test(e2e): past the account limit a paid lab waits for an admin`.

---

## Final checks (before the branch review)

`gofmt -l cmd internal scripts` (empty), `go vet ./...`, `go vet -tags cluster ./internal/labs`,
`TESTCONTAINERS_RYUK_DISABLED=true go test -race -p 2 -parallel 2 ./...`, `terraform test` in `deploy/aws/{persistent,main,labs}`,
`bash deploy/helm/test.sh`, `cd web && npm test && npx tsc -b && npm run lint && npm run build`, content lint for every
example, `bash scripts/lint-workflows.sh`, and the full `cluster-check`.
