# Free FinOps and a Terraform lab mode

Status: **draft for the owner's review** (sections 1, 2 and credentials agreed; 3–4 awaiting approval).
Replaces infracost and AWS Cost Explorer (spec §9.1, §9.3) and turns `runtime: aws` into a Terraform mode with a
cloud interface. Owner's decisions, 2026-10-12. Scope: **AWS only**; other clouds plug into the same interface later.

## Rules

- **No paid services for cost.** No infracost key or API, no Cost Explorer (paid per call). Only free public price
  data and free AWS features (Price List files, Data Exports, Budgets, cost-allocation tags).
- **Fail closed** as today: no price, no plan or stale price data means the lab can't be requested, never $0.

## 1. Terraform mode (agreed)

```yaml
runtime: terraform
cloud: aws
aws:
  region: eu-west-1
  max_hourly_usd: 0.20      # the ceiling; the estimate must fit under it
  prices:                   # only for resource types Crucible can't price yet: USD per hour
    aws_kinesis_stream: 0.015
  usage:                    # expected usage of usage-priced resources, by resource address
    aws_s3_bucket.data: { storage_gb: 5 }
```

- `runtime: aws` is an alias of `runtime: terraform` + `cloud: aws`; existing labs don't change.
- The author's module in `terraform/`, **local modules only** (registry modules may be vendored into the folder),
  the same hardened lint. Trainees work on the result from the workspace terminal, as today.
- **Region:** Crucible writes the provider block (lint refuses `provider` blocks) so it controls credentials, tags
  and state; it needs the region for that, for pricing, for the allowed-regions guardrail and for the reaper.
  Modules read it as `var.crucible_region`. **Required** (owner's decision): a lab names its region.
- Inside Crucible the AWS runner becomes a cloud-neutral Terraform runner plus a `Cloud` interface with one
  implementation (AWS): lab credentials (STS, as today) and read-only plan credentials; the injected provider and
  backend files with the lab-id tags; the lint provider allowlist; a pricer; a billing-export reader; the sweep and
  reaper. Workspace pod, apply/destroy pods, timers, approvals and budgets are unchanged.

## 2. Estimating (agreed)

- **Price data:** a weekly job downloads the free public AWS Price List files for the services labs may use (EC2,
  EBS, VPC/NAT/Elastic IP, ELB, S3, CloudWatch) in the allowed regions, keeps on-demand prices (Linux, shared
  tenancy) in a `prices` table (service, region, attributes, unit price). Admins see its age; older than 30 days or
  missing → estimates fail closed.
- **Pricers:** Go code per resource type turns a planned resource into an hourly price and usage prices
  (`aws_instance` by type/region/tenancy; `aws_ebs_volume` per GB-month; `aws_nat_gateway` per hour + per GB of
  stated usage; `aws_eip`, `aws_lb`, `aws_s3_bucket` per stated `storage_gb`, …). Free types (security groups, route
  tables, subnets, IAM within the boundary, …) are listed as free. Any other type needs a `prices:` entry or lint
  refuses the lab.
- **Plan:** `terraform plan` with a new **read-only plan role** in the lab account, at publish time, cached per
  content version (as the infracost estimate is today, two at a time, failures remembered briefly). Hourly =
  Σ hourly prices + Σ (stated usage × price) ÷ 730. Unstated usage counts $0 and is said so. Request estimate =
  hourly × TTL, routed as today. Lint and the lab page show the breakdown per resource.

## Credentials (agreed)

No stored keys: every credential is a short-lived AWS session for one job, and none leaves Crucible's cluster.

| Step | Who acts | Credential | What it can do |
|---|---|---|---|
| Crucible itself | the API pod | its node or pod identity | only `sts:AssumeRole` into the three roles below |
| Estimate (on publish) | a short-lived plan pod | **plan-role** session (~15 min) | read-only `Describe*`/`Get*` for data sources; plans against an empty state, never touches real state |
| Apply | the Terraform pod in `lab-<id>` | **lab-role** session (1 h), session tags `crucible:lab-id`, team, training | create only resources tagged with its lab id, change or delete only its own, its own state key only, inside the permission boundary |
| Trainee's terminal | the workspace pod | the same lab session, mounted at `/aws` | the same: only that lab's resources |
| Refresh | the sweep | a new lab session about every 45 min, into the lab's secret | no running lab holds a credential older than an hour |
| Destroy | a destroy pod | a fresh lab session for that lab id | its own resources only |
| Cleanup, metering, billed spend | Crucible's jobs | **ops-role** session | `tag:GetResources` (sweep, reaper), CloudTrail lookups, read lab state files, read the CUR export bucket (replaces `ce:GetCostAndUsage`) |

The lab role's permission boundary is the hard ceiling (services, regions, instance types, volume types and sizes, no
Marketplace AMIs); its trust policy requires the `crucible:lab-id` session tag. Pods can't reach the instance
metadata service, and the trainee's laptop gets no AWS credentials. New versus today: the plan role, and the ops
role's switch from Cost Explorer to the export bucket and state reads. In the design this is the `Cloud` interface's
credentials part.

## 3. Actual spend

- **Metered (live):** after each successful apply Crucible reads the lab's Terraform state (its S3 bucket) and
  records each priced resource in `lab_resources` (address, type, hourly price, stated usage, started_at); destroy or
  the reaper sets ended_at. Spent so far = Σ hourly × hours alive + stated usage prorated over hours alive, computed
  on read. It replaces estimate-based spend for running labs in budgets, caps and the Ledger.
- **Billed (free export):** `deploy/aws/labs` creates an AWS Data Exports (CUR 2.0) export — daily, Parquet, with the
  `crucible:lab-id` cost-allocation tag — into a bucket in the (standalone) lab account. A daily job reads new files
  with the ops role (S3 only) and upserts per-lab daily costs into `cost_actuals`. The settled rule stays (billed
  replaces metered once fresh, 48 h after the lab ends). The Ledger shows estimate, metered and billed per lab.
- **Backstop:** the same Terraform creates a free alert-only AWS Budget on the lab account (monthly amount from a
  variable) that emails admins at 80 % and 100 %, independent of Crucible.
- Budgets, caps, the kill switch, alerts and approvals are unchanged apart from reading metered spend.

## 4. Migration, setup and testing

- **Removed:** `internal/infracost`, `InfracostEstimator`, `INFRACOST_API_KEY`, `CRUCIBLE_INFRACOST` (dry run uses
  price fixtures), `infracost_api_key` in `deploy/aws/main`, the Cost Explorer client and `ce:GetCostAndUsage` on the
  ops role, and the runbook's infracost and Cost Explorer steps.
- **Setup:** `crucible aws labs-init` also creates the plan role, the export bucket and export, and the budget; the
  one manual step left is activating the `crucible:lab-id` cost-allocation tag in the Billing console (free).
- **Tests:** pricers per resource type against a fixture price list; the price download against an `httptest`
  stand-in (stale and missing cases); plan pricing from saved plan JSON; metering from state fixtures; the export
  reader from a sample Parquet file; `terraform test` with mock providers for the new resources; the dry-run AWS e2e
  still estimates, approves and meters. Every money path keeps a test that fails without it.
- **Docs:** the runbook, `aws-guide.html`, the test guide, spec §4.5/§9.1/§9.3, the authors' pages (`runtime:
  terraform`, `prices:`, `usage:`), `terraform-vs-crossplane.html`.
