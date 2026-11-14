# A monthly limit on the AWS account's real bill — design

Status: approved in conversation (2026-11-14). Builds on `fix/labs-org-managed-account` (labs-init switches; no billing
export in our account). Decisions, in the owner's words where given:

- Admins set an **upper limit on the account's overall monthly spend**; once it is reached, **only an admin's approval
  can start a new (paid) lab**.
- The limit counts **the account's real bill, the total one** ("I can see it in the aws console"), not only labs.
- **Source: an AWS Budget first, Cost Explorer hourly as the fallback** (the owner chose this over Cost Explorer every
  10 minutes after the cost projection).
- The bill is read **every 10 minutes** (budget); admins may **refresh at most once every 5 minutes**, shared by all
  admins. Cost Explorer, which costs $0.01 per call, is read **hourly** instead.
- The account's total is visible to **admins and approvers**.
- AWS's figure lags, so Crucible **also counts its own meter for the last 24 hours**, and **admins can take time out of
  that count** once they see in the console that AWS has billed it: on the Ledger, a timeline of the last 24 hours with
  **one account marker by default**, and **any lab set on its own** if needed (mockup approved 2026-11-14:
  `.superpowers/brainstorm/*/content/billed-marker-v2.html`).

## 1. The bill

**Source: an AWS Budget** (`spend_source = "budget"`, the default). The labs stack's account-wide monthly cost budget
`<name>-labs-monthly` (`aws_budgets_budget.labs`) is **always created**; its notifications exist only when
`budget_emails` is set (today the whole budget is created only then). Crucible reads one field of
`budgets:DescribeBudget`: `Budget.CalculatedSpend.ActualSpend.Amount` (USD, month to date, the account's actual
unblended cost, updated by AWS a few times a day). The budget's own amount (`--budget-usd`) only drives AWS's optional
alert emails; Crucible's limit is separate (§2). AWS Budgets without actions are free and no per-call price is published
for the Budgets API; the runbook's by-hand check confirms on the first bill that the reads cost nothing.

**Fallback: Cost Explorer** (`labs-init --spend-source=cost-explorer`, kept on re-runs like the other labs-init
switches): no budget is created by this setting (an existing one with emails stays, as before), and Crucible calls
`ce:GetCostAndUsage` once per read: `TimePeriod` = the 1st of the month (UTC) to tomorrow, `Granularity = MONTHLY`,
`Metrics = [UnblendedCost]`, no grouping, summed. $0.01 per call (each page is a call; this query has one page).

**Who reads:** the ops role (`<name>-lab-ops`, read-only, assumed by the node). The labs stack grants exactly what the
source needs: `budgets:ViewBudget` on `arn:aws:budgets::<account>:budget/<name>-labs-monthly`, or `ce:GetCostAndUsage`
on `*`. In our setup the lab account is the only account, so the budget is the whole bill (the node included). With a
separate lab account it is the lab account's bill; the docs say so.

**Outputs and wiring:** the labs stack outputs `spend_source` and `budget_name`; `crucible aws up` passes them to the main
stack (`lab_spend_source`, `lab_budget_name`), then Helm (`awsLabs.spendSource`, `awsLabs.budgetName`), then the server
(`CRUCIBLE_AWS_SPEND_SOURCE` = `budget` | `cost_explorer`, `CRUCIBLE_AWS_BUDGET_NAME`). A labs stack from before this
change has neither output: `up` passes empty values and the server reads nothing (no limit can be enforced; §3).

**Reads:**
- `awscloud.Cloud.AccountSpend(ctx) (usd float64, err error)` on the client (the source picked by its config) and
  `Fake` (`SetAccountSpend`, `FailAccountSpend`). A dry run's fake returns a fixed $12.34 so the Ledger has a figure.
- A periodic job (`labs.AccountSpendArgs`) every 10 minutes. With `cost_explorer` it reads only when the last attempt is
  an hour old or more. It runs at start too, like the other AWS jobs.
- **Refresh now** (admins): `POST /api/admin/account-spend/refresh`. One atomic
  `UPDATE aws_ops SET account_refresh_at = now() WHERE account_refresh_at IS NULL OR account_refresh_at <= now() - '5 minutes'`
  wins; a loser gets 429 with the seconds left, so two admins clicking together cause one read. The reply carries the
  new figure or the error.
- Migration `00037_account_spend.sql`: `aws_ops` gains `account_usd double precision`, `account_read_at timestamptz` (the
  last success), `account_error text NOT NULL DEFAULT ''`, `account_error_at timestamptz`, `account_refresh_at
  timestamptz`; the settings table gains `account_monthly_limit_usd double precision NOT NULL DEFAULT 0`. With a Down.
- A failed read keeps the last good figure and its time, and records the error (as billed-cost ingestion does). A
  negative or non-finite amount is an error, never stored.

## 2. The limit

- **Setting:** `account_monthly_limit_usd` in *Administrator → Forge settings* (`internal/org` settings: validated
  finite and ≥ 0, audited, versioned like the others). 0 or empty means no limit. Copy: **Monthly account limit (USD)**
  with the hint *The AWS account's whole bill this month, as AWS reports it. Past it, new paid labs need an admin.*
- **The check** joins `overCap` (`internal/labs/finops.go`) as a third scope beside the team and program caps, so its
  three callers apply it: a request (`quote`), an approval (`Decide`'s re-check, which hands an approver's approval to
  an admin), and an extension. A request is over the account limit when the limit is set and

  > billed this month + not yet billed + reserved + this request's estimate > limit

  where *billed* is `aws_ops.account_usd`, *not yet billed* is §2b, and *reserved* is, over every lab not yet ended (all teams, whichever month it
  started in): a
  `provisioning` lab's `estimate_usd`, and a `ready`, `asleep` or `waking` lab's `hourly_usd × max(ends_at − now, 0)`.
  Reserved counts only the time left, so it does not count again what the bill already holds.
- **Fails closed:** with a limit set, a bill never read, or not read successfully for 36 hours, makes every paid request
  over the limit.
- **Unaffected:** free labs (`hourly_usd = 0`: laptop and cluster labs) are never checked, as with team caps; running
  labs keep running; the team budgets and caps are unchanged. Without AWS labs (`Cloud == nil`) there is no bill, and
  the limit is shown as not enforced (paid labs exist only as AWS labs).
- **Over the limit** is the existing `over_cap`: the tier becomes `admin`, only an admin may approve, and the approval
  is audited as `lab.budget_override`. A new reason distinguishes it for the copy: `lab_instances.cap_reason`
  (`''` | `team` | `account`), set where `over_cap` is set (migration 00037).

## 2b. Not yet on the bill: the last 24 hours of Crucible's meter

AWS's figure trails the usage by up to about a day, so labs that ran (or ended) since AWS's last update would count
nowhere. Crucible adds its own meter for that window:

> not yet billed = Σ over `lab_resources` rows of `(hourly_usd + usage_hourly_usd) × |[started_at, ended_at or now] ∩
> [cut, now]|` in hours, where `cut` is the lab's own mark if it has one, else the account mark, else `now − 24 h`,
> and never earlier than `now − 24 h`

(the same integral as `labCostSQL`'s metered spend, with a lower bound; a sleeping lab's rows are at its disk price, as
metered today). When AWS's figure is already fresh this counts up to a day of lab spend twice: the limit then asks an
admin a little early, never late. Usage that is not a lab's (the node) still trails, and the docs say so.

**Marks** (admins only; audited `account.billed_mark`): a mark says "AWS has billed this up to T".
- **The account mark** moves every lab's cut; **a lab's own mark** overrides it for that lab ("Set separately"), and
  removing it ("Follow account") puts the lab back on the account mark. **Count all 24 h** removes every mark.
- Stored in `billed_marks (lab_id TEXT PRIMARY KEY, billed_to TIMESTAMPTZ NOT NULL, set_by TEXT NOT NULL, set_at
  TIMESTAMPTZ NOT NULL)`, `lab_id = ''` for the account mark (migration 00037). A mark older than the window is ignored
  (the clamp); the sweep deletes marks older than 48 hours, and a lab's mark when its lab row is purged.
- `PUT /api/admin/account-spend/marks` with `{"lab_id": "" | "<id>", "billed_to": "<RFC 3339>" | null}` sets or removes
  one mark; `{"reset": true}` removes all. `billed_to` must lie in `[now − 24 h, now]` (refused otherwise, *Pick a time
  in the last 24 hours*); a lab's mark only for a lab with metered usage in the window. The reply is the new Ledger
  `account` block, so the page redraws from the server's figures.
- **The night band:** the server records `aws_ops.forge_slept_at` when a bedtime reaches `asleep` and
  `aws_ops.forge_woke_at` in `Morning()`; the timeline shades that interval when it falls in the window (*forge asleep*).

## 3. Who sees it, and where

- **Ledger** (`GET /api/ledger`): a new `account` block for **admins and approvers**: admins, team leaders and program
  approvers, everyone who can decide a lab request (program managers who are not approvers do not see it):
  `{billed_usd, read_at, unbilled_usd, reserved_usd, limit_usd, source, stale, error (admins), refresh_after (admins),
  window: {from, to, slept_from, slept_to, account_mark, labs: [{id, team, training, module, requester, hourly_usd,
  intervals: [{from, to, hourly_usd}], own_mark, unbilled_usd}]}}`. The page shows,
  above the budgets: *AWS account this month: $412.37 billed (as of 14:20) + $4.13 not yet billed + $38.00 reserved by
  running labs = $454.50 of $600.00*, a `meter` (molten → `--accent-2` from 80% → `--danger` at 100%), and the muted line *AWS updates the bill
  a few times a day; reserved is what running labs may still spend.* Without a limit: the same line ending at the total.
  Stale: the line in `error` with *Out of date: new paid labs need an admin until the bill is read again.* Admins get
  **Refresh now**, disabled while the cooldown runs (*Next refresh in 3:12*).
- **The timeline** (`components/BilledTimeline.tsx`, under the account line; read-only for approvers): the last 24
  hours, one row per lab with metered usage in it (its name, requester and hourly price), each usage interval a bar,
  hatched grey left of the lab's cut (*In the AWS bill*) and molten right of it (*Counted by Crucible*); the green
  account marker with its label *Account billed up to 09:00 ⇆*; per row **Set separately** / **Follow account**, a set
  lab's blue marker and a ◆ by its name; **Count all 24 h**; the summary *Crucible adds $4.13 from 3 labs on top of the
  AWS bill · 1 lab set on its own*; a legend in words. Markers are `role="slider"` with labels, move by 15 minutes with
  the arrow keys, snap to 15 minutes when dragged, and save when released (a toast; a failure puts the marker back and
  shows the error). Calm forge stops nothing that matters (no animation is needed). At phone width the names sit above
  their bars. The crucible-ui skill gains the pattern.
- **Approvals page:** a request with `cap_reason = account` says *Would pass the account's monthly limit ($450.37 of
  $600.00): only an admin can approve it.*
- **The trainee's lab page** (pending): *Waiting for an admin: the forge's monthly spend limit is reached.*
- **Emails to admins** (`CheckBudgets`, scope `account`, once per month and level through `budget_alerts`): at 80% and
  at 100% of the limit (billed + reserved), and once when the bill is stale (`budget_alerts.level` is an integer: 0
  means stale).

## 4. Docs

`docs/user/admins` (the limit in Forge settings, the Ledger block, Refresh now), `docs/user/leaders/
ledger-and-content-versions.md` (approvers see the account line), `docs/runbooks/aws.md` and `docs/aws-deploy.html`
(`--spend-source`, the always-on budget, the by-hand checks), `docs/deploy-architecture.html` (the ops role's new read,
the budget), `docs/test-guide.html`, `docs/cli.md` (`--spend-source`), the crucible-ui skill if the meter pattern
changes, and a CLAUDE.md invariant line (the account limit fails closed).

## 5. Testing (tests first)

- Go: *not yet billed* integrates rows across the window's edges, an open row up to now, the account mark, a lab's own
  mark overriding it, a mark older than 24 h clamped, a sleeping lab at its disk price; marks: admins only, outside the
  window refused, a lab without usage in the window refused, reset, audited, purged after 48 h.
- Go, Postgres testcontainers and `awscloud.Fake`: under and over the limit (billed + not yet billed + reserved +
  estimate); reserved
  counts provisioning estimates and running/asleep/waking time left, not ended labs; a stale (37 h) or never-read bill
  is over; free labs unchecked; `Decide` hands an approver's approval to an admin when the limit is now passed; an
  extension over the limit; `cap_reason`; the Ledger block only for admins and approvers; the refresh cooldown (two
  concurrent refreshes, one read, the loser's seconds left); the job's hourly gate for Cost Explorer; a failed read
  keeps the last figure; 80%/100%/stale emails once each; settings validation (negative, NaN, Inf refused).
- `awscloud.Client`: `DescribeBudget` and `GetCostAndUsage` against `httptest` (the request shape, parsing, a
  malformed amount refused).
- Terraform (mock providers): the budget exists without emails and has no notifications; with emails, two; the ops
  role's statement per source; outputs; `cost_explorer` creates no budget. Main: the two variables reach Helm. Helm:
  the env vars.
- `awsops`: `--spend-source` passed and kept on a re-run; `up` passes the outputs.
- Web: the timeline's pure geometry and sums (`lib/billedTimeline.ts`: cut per lab, hatched/molten split, totals,
  15-minute snapping, keyboard steps) with tests; render tests for the timeline (admin with controls, approver
  read-only, a lab set on its own, the night band) and the Ledger block (limit, no limit, stale, cooldown), the settings field, the approvals reason,
  the pending lab line.
- E2E on the dry-run cluster: an admin sets the limit below the fake's $12.34, the trainee's request shows the admin
  wait, the leader cannot approve it, the admin approves it as an override.
- By hand on our account (runbook): labs-init creates the budget (or the SCP refuses it: then `--spend-source=
  cost-explorer`); the Ledger's billed figure matches the console's month-to-date cost; the next bill shows no charge
  for Budgets reads.

## 6. Out of scope

Per-lab billed figures (they need the `crucible:lab-id` cost-allocation tag, activated in the management account);
forecasts; stopping running labs at the limit; a limit per region or service; syncing the AWS budget's amount with
Crucible's limit.
