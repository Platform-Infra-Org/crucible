# M6 AWS Labs & Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `runtime: aws` lab is priced with infracost, approved, provisioned (terraform in the cluster with short-lived, tag-scoped credentials), worked in a workspace terminal, checked server-side, destroyed with a tag sweep, and anything it leaked is later swept by the reaper. Actual AWS spend from Cost Explorer replaces estimates where it has settled, and everything appears on a new **Ledger** page.

**Architecture:** A new `AWSRunner` in `internal/labs` reuses M4's `ClusterRunner`. The lab namespace holds a workspace pod: dind plus one generated compose service `workspace` running the AWS CLI, with the lab's STS credentials mounted from a Secret at `/aws`. It also holds one-shot terraform runner pods (`tf-apply`, `tf-destroy`) that run the lab's `terraform/` module plus a Crucible-injected provider/backend file. Credentials come from STS `AssumeRole` into a lab role that has a permission boundary and session tags. The API refreshes them every hour. A new package `internal/awscloud` puts every AWS call behind one `Cloud` interface: STS, the Resource Groups Tagging API, EC2/S3 deletes, CloudTrail and Cost Explorer. A real `Client` (aws-sdk-go-v2, tested only against `httptest`) and an in-memory `Fake` both implement it. The Fake also powers a `CRUCIBLE_AWS_LABS=dryrun` mode. In that mode the e2e on kind runs the real workspace and runner pods, but no terraform command touches AWS. Periodic River jobs run the reaper and the Cost Explorer ingestion. `spend()` swaps in settled actuals. The SPA gets a Ledger page. `deploy/aws/labs` is a new Terraform stack that creates the lab and ops roles and a state bucket in the shared lab account. It is tested with mock providers only.

**Tech Stack:** Go 1.26, pgx v5, goose, River, client-go (M4), `github.com/aws/aws-sdk-go-v2` (already added by M5: `config`, `s3`; M6 adds `service/sts`, `service/resourcegroupstaggingapi`, `service/costexplorer`, `service/cloudtrail`, `service/ec2`), the infracost CLI (pinned in the image), Terraform 1.16 (`terraform test` + `mock_provider`), Helm 3.17, React 19 + Vite + Vitest, Playwright on kind (`make cluster-check`).

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md`. Sections: §2 (AWS isolation: "one shared account, tag + IAM permission boundary"), §3 (`AWSRunner = terraform Job + a ClusterRunner workspace pod`), §4.5 (`aws: region, max_hourly_usd`), §8.1 (failed → destroy), §8.2 aws runtime and reaper, §8.5 ("the workspace pod with lab credentials"), §8.6 (budget cap feeds the timer), §9.1 (`aws = infracost on the module × requested TTL`), §9.2, §9.3 (cost data and the FinOps page), §12 (navigation: "Ledger (FinOps)"), §13 (`cost_samples` + `cost_actuals`), §14 (credentials, Cost Explorer outage, testing). Program context: `.superpowers/sdd/program-context.md`.

## Global Constraints

- Every shell starts with `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH` (go1.26.8, terraform 1.16.5). Never install anything system-wide.
- **NEVER touch real AWS and never create real AWS resources.** Concretely:
  - Do not run `terraform plan/apply` outside `terraform test` with `mock_provider`.
  - Never set `CRUCIBLE_AWS_LABS=1` on this machine. Only `dryrun` exists here.
  - Construct `awscloud.Client` only in tests, pointed at an `httptest` server.
  - No `aws` CLI calls.
  - Do not set `INFRACOST_API_KEY`, so `crucible lint` skips the price check.
  - Do not run `crucible aws labs-init`.
  - Pulling the public `amazon/aws-cli` and `hashicorp/terraform` images from Docker Hub inside kind is allowed, because it touches no AWS account.
  - Everything that needs a real account is in the runbook section that Task 13 writes, for a human to run.
- `gofmt -l .` prints nothing, `go vet ./...` is clean, and `go test -race ./...` passes. Docker Desktop must be running for testcontainers Postgres.
- Acceptance: `KEYCLOAK_PORT=8082 make local-check` ends with `🔥 Local check passed. The forge holds.`, and `KEYCLOAK_PORT=8082 make cluster-check` ends with `🔥 Cluster check passed. The crucible holds.`
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Start after M5 is merged on `feat/m3-m7`.** Rebase onto it first and keep its changes. These files are touched by both M5 and M6:
  - `go.mod`/`go.sum`: M5 adds aws-sdk-go-v2 `config` + `s3`. Add the M6 services with `go get` at the **same** release line.
  - `internal/db/migrations`: M5 adds `000NN_scoring.sql`. Run `ls internal/db/migrations` and use the next free number. This plan calls it `000NN_aws_ledger.sql` (expected `00008`). Never edit an existing migration.
  - `internal/notify/notify.go`: M5 adds `SubmissionPending`/`SubmissionScored`. Append `ReaperReport` after them.
  - `internal/labs/service.go`, `internal/labs/http.go`, `cmd/crucible-api/main.go`, `internal/httpapi/server.go` (`/api/me`): M5 adds review/transcript code, the `scoring` and `blob` wiring, and `can_score`. Add next to it.
  - `web/src/{App.tsx,components/Nav.tsx,types.ts,pages/Lab.tsx}`: M5 adds Anvil, `can_score` and the review form.
  - `scripts/seed-git.sh`, `scripts/local-check.sh`, `examples/platform/trainings.yaml`: M5 adds `forge-301`. M6 adds `forge-401` the same way.
  - `deploy/helm/crucible/templates/crucible.yaml`, `deploy/aws/main/{main.tf,bootstrap.sh.tftpl}`: M5 points the blob store at `backup.bucket`, adds `s3:PutObject` on `uploads/*`, and may add a `--set` to `deploy.sh`.
- Spec §14: "AWS lab credentials are short-lived (1 h, auto-refreshed), scoped by permission boundary; never exposed to the browser except inside the workspace pod's shell." No API response, lab event, log line or error message may contain an access key, secret or session token.
- Spec §14: "Check scripts and lab containers never run in the API pod." Terraform runs only in runner pods inside the lab namespace. The API pod runs only `infracost breakdown`, which parses HCL and fetches prices from Infracost's pricing API. Lint and the estimator refuse non-local module sources, so infracost never downloads code.
- Lab ids are 12 lowercase hex characters. Every AWS call validates the id first: STS session tags, deletes, the reaper. **Tag values read back from AWS are untrusted input.**
- Spec §1 non-goal: no competitions or leaderboards. The Ledger's "top spenders" is a cost table for people who may see team spend (spec §5.3: admin, leader, program manager, approver). Trainees never see the Ledger.
- UI: themes forge|anvil|quench|contrast through the existing tokens. Every animation respects `useCalm`. Emails are lowercased.
- These e2e specs keep passing: `forge-101.spec.ts`, `approvals.spec.ts` (Task 14 appends three Ledger assertions to it), M4's `cluster-lab.spec.ts`, and M5's scoring spec.

## Rulings made in this plan (read before starting)

1. **The workspace is a cluster lab.** `AWSRunner` asks `ClusterRunner` for a normal lab namespace. That namespace has the quota, the default-deny NetworkPolicy that blocks IMDS, and the dind pod. The bundle contains a generated `crucible-workspace.yaml` with one service, `workspace` (`amazon/aws-cli`, entrypoint `sleep infinity`). The service bind-mounts `/aws` read-only from the dind container, which mounts the `aws-creds` Secret there. Terminals, checks and setups therefore reuse M4's exec code unchanged (spec §8.5: "the workspace pod with lab credentials").
2. **Terraform runs in one-shot pods, not `batch/v1` Jobs.** The spec says "terraform runner Job". The pods `tf-apply` and `tf-destroy` sit in the lab namespace with `restartPolicy: Never`, the credentials Secret, and a ConfigMap `tf-module` that holds `module.tgz` and `backend.hcl`. A Job would add retries we must switch off, a second API group in RBAC and admission, and a `pods list` permission to find its pod's logs (M4 removed `pods list` on purpose). A bare pod with a fixed name, read through `pods get` with `terminationMessagePolicy: FallbackToLogsOnError`, gives us the log tail with no new read permission. *(Open decision 1.)*
3. **Credentials.** crucible-api uses the node's instance role (IMDSv2, hop limit 2, M2). It calls `sts:AssumeRole` on the lab role for **1 hour**, the role-chaining maximum and the spec's value. The session name is `crucible-lab-<id>` and the session tags are `crucible:lab-id`, `crucible:team` and `crucible:training`. The credentials file lives only in the lab's Secret. The sweep re-assumes the role when less than 15 minutes are left, and kubelet updates the mounted file in place. RBAC gains `secrets: create, update` (never `get/list/watch`), `configmaps: create` and `pods: delete`. The existing admission policy confines all of them to `lab-*` namespaces.
4. **Crucible owns the provider and the backend.** It adds `crucible.tf` (S3 backend, `provider "aws"` with the lab region and `crucible:*` `default_tags`, and variables `crucible_lab_id/team/training/region`) plus `crucible.auto.tfvars.json`. Lab modules must not declare `provider "aws"` or a backend, and module `source`s must be local (`./…`). Lint enforces this. State goes to the lab account's state bucket under `labs/<id>.tfstate` with `use_lockfile`. Destroy runs with `-lock=false`, but only after the apply pod has been deleted and is gone.
5. **The shared lab account** is a new stack, `deploy/aws/labs`, with its own local state like `persistent`. It contains:
   - the **lab role**: a permission boundary, ABAC on `${aws:PrincipalTag/crucible:lab-id}`, tag-on-create required, `crucible:*` tags immutable, only the allowed regions, only nano/micro instance types;
   - an **ops role**: `tag:GetResources`, `ce:GetCostAndUsage`, `cloudtrail:LookupEvents`;
   - a versioned **state bucket**;
   - account-level S3 Block Public Access.

   It uses the Crucible account by default. A separate account works by setting `crucible_account_id`, and the runbook recommends one. **Services in v1:** EC2 (instances, volumes, security groups, network interfaces) and S3 buckets named `crucible-lab-<id>…`. Nothing else, and no IAM.
6. **Destroying an AWS lab is asynchronous.** The time budget is 20 minutes instead of 2, and a stuck `destroying` row is retried after 30 minutes instead of 10. After `terraform destroy`, `Service.destroyRuntime` runs a **tag sweep**: everything still tagged with the lab id is deleted with the lab's own role. A finding is recorded only when something was actually deleted, failed to delete, or cannot be deleted automatically.
7. **The reaper** runs every 6 h and on every start. The node sleeps at night (M2), so "nightly" means "every time it is up". It deletes only resources whose lab this database knows and that ended more than an hour ago. Resources with an unknown or malformed lab id are **reported, never deleted**. If the database cannot be read, nothing is deleted. **CloudTrail:** a successful create-like call by a `crucible-lab-*` session whose request carried no `crucible:lab-id` tag is reported as a policy gap. `CreateBucket` is exempt because buckets are scoped by name. Admins get one summary notification per run that finds something new.
8. **Cost data:** Cost Explorer daily `UnblendedCost` grouped by the `crucible:lab-id` tag. Each run reads month-to-date plus the previous 3 days, every 6 h and on start. Untagged spend is not stored. **An actual replaces an estimate for a lab only when Cost Explorer reported a cost for that lab and the lab ended at least 48 h before the last successful ingestion.** Otherwise the estimate stands. This is conservative: a lab never counts as $0 just because tags were not activated. Actuals are **stale** after 36 h without a successful ingestion or when the last attempt failed. Caps always use the same `spend()`, so estimates keep enforcing them (spec §14). `cost_samples` from spec §13 is not created, because the estimates already live on `lab_instances` (YAGNI).
9. **Budget cap on the timer (spec §8.6).** When a lab becomes ready, its end is also bounded by the hard-cap headroom divided by its hourly rate, with reason `budget`. Labs an admin approved over the cap are exempt. **"Extension pending"** (re-approval of an extension) stays deferred to the M7 coverage pass. Today a tier-raising extension is refused with a clear message. Update the `ponytail:` comment in `Extend` to say so. *(Open decision 4.)*
10. **Infracost:**
    - The CLI is pinned in the image and its results are cached per content version (`lab.Dir` is immutable).
    - Without `INFRACOST_API_KEY`, aws labs show "no cost estimate is available for aws labs" and cannot be requested.
    - `CRUCIBLE_INFRACOST=off` prices aws labs with the existing `FixedRates` (`CRUCIBLE_DEV_LAB_USD_PER_HOUR`). This is for dev and the e2e.
    - The quote blocks a lab priced above its `aws.max_hourly_usd`, or one whose region the server does not allow.
    - `crucible lint` runs the price check when infracost and a key are present, and otherwise prints that it skipped it.
11. **No LocalStack.** It would mean a >1 GB image and an auth token on current releases, plus a second fake AWS whose IAM behaviour, the part that matters here, is not faithful. Every AWS call already sits behind `awscloud.Cloud`. The SDK adapter is tested against `httptest` for exact request shapes: session tags, tag filters, group-by. The runner is tested with the in-memory `Fake`. What only real AWS can prove (IAM conditions, Cost Explorer tags, CloudTrail fields) LocalStack cannot prove either, so it goes in the runbook (spec §14 "nightly sandbox job" → a manual sandbox checklist; no CI exists). *(Open decision 2.)*
12. **Testing levels:**
    - Unit: Go fakes, the client-go fake clientset, `httptest`, and `terraform test` with mock providers.
    - e2e: `make cluster-check` with `CRUCIBLE_AWS_LABS=dryrun`. The real kind workspace pod and the real runner pods run, but the runner pods only unpack the module and print `terraform version`, and the fake cloud stands in for STS, the tag inventory, deletes and Cost Explorer. A trainee requests the lab, a leader approves it, the trainee checks a task and ends the lab, and an admin sees the tag-swept resource and the actual cost on the Ledger.
    - `make local-check` gains a Ledger smoke test in `approvals.spec.ts`.
    *(Open decision 3: the AWS e2e lives in cluster-check, not local-check.)*
13. **Fixture:** `examples/forge-401` ("Forge 401: Cloud Heat", `progression: free`) has one aws lab, `cloud-heat`:
    - task `t1-region` is offline-checkable: the CLI region plus mounted credentials;
    - task `t2-bucket` needs real AWS: upload to `crucible-lab-<id>`.

    The fixture is registered in `trainings.yaml` and **not enrolled**; the e2e enrols through the UI. Forge 101 is not touched. Spec §14 asks Forge 101 to carry the AWS lab, but its linear progression would gate the AWS lab behind the cluster lab, which local-check cannot run.
14. **Who sees the Ledger:**
    - admins see everything;
    - a team is visible to its leader and to the managers and approvers of any of its programs (spec §5.3 "View team spend");
    - findings, actuals errors and the **Refresh now** button are admin-only.
    `/api/me` gains `can_view_spend` for the nav link.

## Review Focus

1. **The reaper is pointed at the wrong things.** Cases: a tagged resource of a lab that is still running; a resource of a lab ended 10 minutes ago, still listed because tagging lags; a lab id this database has never seen (another deployment, a restored snapshot); a tag value like `x"; Deny`; or the database is down mid-run. Only the long-ended known lab's resources are deleted, with that lab's own credentials. The unknown and malformed ones are reported. A database error deletes nothing. Pinned by `TestReaperDeletesOnlyEndedKnownLabs` (Task 9).
2. **Ending an AWS lab or killing all labs while terraform is slow, or the API restarting mid-apply.** `End`, the sweep and the kill switch return at once (state `destroying`). A running `tf-apply` is stopped before `tf-destroy` starts. A second destroy waits on the same pod instead of starting another. The sweep does not retry for 30 minutes, and two retries never run at once. Pinned by `TestAWSDestroyStopsTheApplyFirst` and `TestAWSDestroyRetriesAFailedDestroyPodOnce` (Task 7), and `TestAWSLabFromRequestToTagSweep` and `TestStuckAWSDestroyWaitsLonger` (Task 8).
3. **A hostile or careless lab module:** its own `provider "aws"` pointing elsewhere, its own backend, `source = "git::https://…"` or a registry module, or a 600 KiB file. Lint rejects each one with a clear message. A provider `source = "hashicorp/aws"` in `required_providers` is still allowed. Pinned by `TestAWSLabModuleRules` (Task 1) and `TestHourlyPricesALocalCopy` (Task 5).
4. **Credentials leaking or dying.** No view JSON, lab event or error contains the key, secret or token. Credentials are refreshed before the 1-hour expiry, and only then. Crucible's RBAC can create and update secrets but never read them. Pinned by `TestAWSRefreshOnlyNearExpiry` (Task 7), `TestAWSLabFromRequestToTagSweep`'s leak assertion (Task 8), and the helm assertions (Task 12).
5. **Cost Explorer is down, never enabled, or the cost-allocation tags were never activated.** Caps keep using estimates, no lab silently becomes $0, the Ledger says actuals are stale, and a failed ingestion keeps the previous rows. Pinned by `TestSpendUsesSettledActualsOnly` (Task 2), `TestIngestCostsUpsertsAndMarksFailures` (Task 9) and `TestLedgerNumbersAndStaleness` (Task 10).

---

## File Structure

```
examples/forge-401/**                                      Task 1  Forge 401 "Cloud Heat": one aws lab with a terraform/ module
examples/platform/trainings.yaml                           Task 1  register forge-401 (not enrolled)
scripts/seed-git.sh, scripts/local-check.sh                Task 1  seed + lint forge-401
internal/content/load.go, load_test.go                     Task 1  aws lab rules (region, price ceiling, module rules)
internal/content/awstf.go                                  Task 1  LabTF (injected provider/backend), LabTFVars
internal/labs/bundle.go                                    Task 1  terraform/ never reaches the workspace
internal/db/migrations/000NN_aws_ledger.sql                Task 2  cost_actuals, aws_ops, reaper_findings
internal/labs/approvals.go                                 Task 2  Spend.ActualUSD, labCostSQL, spend() with settled actuals
internal/labs/finops.go, service.go, finops_test.go        Task 2  budgetLimit on the timer; CanExtend
internal/awscloud/awscloud.go, fake.go, fake_test.go       Task 3  Cloud interface, types, in-memory Fake
go.mod, go.sum                                             Task 4  sts, tagging, costexplorer, cloudtrail, ec2
internal/awscloud/client.go, client_test.go                Task 4  SDK Client, tested against httptest
internal/infracost/infracost.go, infracost_test.go         Task 5  Hourly(): infracost on a local copy of the module
internal/labs/aws_estimate.go, aws_estimate_test.go        Task 5  InfracostEstimator, Forge 401 test fixture
internal/labs/service.go                                   Task 5  quote: max_hourly_usd, allowed regions
cmd/crucible/main.go                                       Task 5  lint: optional infracost price check
internal/labs/cluster.go                                   Task 6  provision(…, awsSetup), stuck(), waitDone, deletePod
internal/labs/aws_objects.go, aws_objects_test.go          Task 6  workspace mount + quota, tfPod, tfScript
internal/labs/bundle.go                                    Task 7  tarGz, BundleWith, ModuleBundle
internal/labs/aws.go, aws_test.go                          Task 7  AWSRunner: ProvisionLab, Destroy, Refresh, dry run
internal/labs/service.go, aws_sweep.go, aws_service_test.go Task 8 labProvisioner, async destroy, tag sweep, refresh
cmd/crucible-api/main.go                                   Task 8  CRUCIBLE_AWS_LABS=1|dryrun wiring
internal/labs/reaper.go, reaper_test.go, jobs.go           Task 9  Reap, IngestCosts, River workers
internal/notify/notify.go                                  Task 9  ReaperReport
internal/labs/ledger.go, ledger_test.go, http.go           Task 10 Ledger API, admin refresh
internal/httpapi/server.go                                 Task 10 can_view_spend on /api/me
web/src/{types.ts,lib/ledger.ts,lib/ledger.test.ts}        Task 11
web/src/pages/Ledger.tsx, App.tsx, components/Nav.tsx      Task 11
web/src/pages/Lab.tsx, theme/app.css                       Task 11 aws destroy screen, ledger bars
Dockerfile                                                 Task 12 infracost CLI (pinned, checksummed)
deploy/helm/crucible/{values.yaml,templates/*.yaml}, test.sh  Task 12 awsLabs values, env, RBAC, admission
deploy/aws/labs/{main.tf,labs.tftest.hcl}                  Task 13 lab account stack
deploy/aws/main/{main.tf,variables.tf,bootstrap.sh.tftpl,main.tftest.hcl}  Task 13 node role, helm-values, infracost key
internal/awsops/awsops.go, awsops_test.go, cmd/crucible/main.go  Task 13 `crucible aws labs-init`, labs outputs → up
docs/runbooks/aws.md                                       Task 13 "AWS labs" setup + human sandbox verification
deploy/compose/cluster.yml, scripts/cluster-check.sh       Task 14 dry-run AWS labs on kind
e2e/playwright.config.ts, e2e/tests/aws-lab.spec.ts        Task 14
e2e/tests/approvals.spec.ts                                Task 14 Ledger smoke in local-check
```

Order and dependencies:
- 1 → 2.
- 3 → 4.
- 5 needs 1 and 3. 6 needs nothing.
- 7 needs 1, 3 and 6. 8 needs 2, 5 and 7. 9 needs 8. 10 needs 9.
- 11 needs 10. 12 needs 8. 13 needs 12.
- 14 needs everything.
- 3, 4 and 6 can run in parallel with 1 and 2.

---

### Task 1: Content: the aws lab contract, lint rules, and the Forge 401 fixture

**Files:**
- Create: `internal/content/awstf.go`
- Modify: `internal/content/load.go` (the `case "aws":` branch, plus a new `awsModule` method)
- Modify: `internal/content/load_test.go`
- Modify: `internal/labs/bundle.go` (`bundleSkip`)
- Create: `examples/forge-401/training.yaml`, `examples/forge-401/modules/01-cloud-heat/module.yaml`
- Create: `examples/forge-401/modules/01-cloud-heat/lab/{lab.yaml,terraform/main.tf,tasks/01-region.md,tasks/02-bucket.md,checks/01-region.sh,checks/02-bucket.sh}`
- Modify: `examples/platform/trainings.yaml`, `scripts/seed-git.sh`, `scripts/local-check.sh`

**Interfaces:**
- Produces:
  - `content.LabTFFile = "crucible.tf"`, `content.LabTFVarsFile = "crucible.auto.tfvars.json"`, `content.LabTF` (string).
  - `content.LabTFVars(labID, team, training, region string) []byte` (JSON).
  - Lint rules for `runtime: aws`. The `terraform/` directory never enters a workspace bundle.
  - Fixture strings used by Tasks 5, 8 and 14:
    - training `forge-401`, module `01-cloud-heat`, lab id `cloud-heat`, region `eu-west-1`, `max_hourly_usd: 0.05`, terminal `workspace`, TTL `1h`;
    - task `t1-region`: check output `Your CLI points at eu-west-1 with your lab's credentials.`;
    - task `t2-bucket`: check output `forged.txt is in your lab bucket.`;
    - task titles `Find your region`, `Forge a file in your bucket`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/content/load_test.go` (add `"maps"` to the imports):

```go
// awsTree is base with its lab turned into a minimal valid aws lab; override replaces or adds files on top.
func awsTree(t *testing.T, override map[string]string) string {
	t.Helper()
	files := map[string]string{
		"modules/m1/lab/lab.yaml": `id: l1
runtime: aws
terminals: [{name: ws, service: workspace}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: workspace}
aws: {region: eu-west-1, max_hourly_usd: 0.1}
`,
		"modules/m1/lab/compose.yaml":      "<delete>",
		"modules/m1/lab/setup/t2.sh":       "<delete>",
		"modules/m1/lab/hints/sol.md":      "<delete>",
		"modules/m1/lab/terraform/main.tf": "resource \"aws_s3_bucket\" \"b\" {\n  bucket = \"crucible-lab-${var.crucible_lab_id}\"\n}\n",
	}
	maps.Copy(files, override)
	return tree(t, files)
}

func TestAWSLabModuleRules(t *testing.T) {
	if _, probs := Load(awsTree(t, nil)); len(probs) > 0 {
		t.Fatalf("a minimal aws lab must load: %v", probs)
	}
	withAWS := func(aws string) map[string]string {
		lab := strings.Replace(`id: l1
runtime: aws
terminals: [{name: ws, service: workspace}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: workspace}
AWS`, "AWS", aws, 1)
		return map[string]string{"modules/m1/lab/lab.yaml": lab}
	}
	tf := func(name, body string) map[string]string { return map[string]string{"modules/m1/lab/terraform/" + name: body} }
	cases := map[string]struct {
		override map[string]string
		want     string
	}{
		"no region":      {withAWS("aws: {max_hourly_usd: 0.1}\n"), "aws.region is required"},
		"odd region":     {withAWS("aws: {region: \"eu-west-1; rm -rf\", max_hourly_usd: 0.1}\n"), "aws.region is required"},
		"no ceiling":     {withAWS("aws: {region: eu-west-1}\n"), "aws.max_hourly_usd must be set"},
		"no module":      {tf("main.tf", "<delete>"), "needs a terraform/ directory"},
		"own provider":   {tf("p.tf", "provider \"aws\" {\n  region = \"us-east-1\"\n}\n"), `do not declare provider "aws"`},
		"own backend":    {tf("b.tf", "terraform {\n  backend \"local\" {}\n}\n"), "do not declare a backend"},
		"registry mod":   {tf("m.tf", "module \"vpc\" {\n  source = \"terraform-aws-modules/vpc/aws\"\n}\n"), "must be a local path"},
		"git module":     {tf("m.tf", "module \"x\" {\n  source = \"git::https://example.com/x.git\"\n}\n"), "must be a local path"},
		"too big":        {tf("big.tf", "# "+strings.Repeat("x", 600<<10)+"\n"), "keep it under 512 KiB"},
		"wrong terminal": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(withAWS("aws: {region: eu-west-1, max_hourly_usd: 0.1}\n")["modules/m1/lab/lab.yaml"], "service: workspace}]", "service: box}]", 1)}, `service "box"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, probs := Load(awsTree(t, c.override))
			for _, p := range probs {
				if strings.Contains(p.Msg, c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %v", c.want, probs)
		})
	}
	// Provider sources in required_providers are not module sources; local modules are fine.
	ok := awsTree(t, map[string]string{
		"modules/m1/lab/terraform/versions.tf": "terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n  }\n}\n",
		"modules/m1/lab/terraform/mod.tf":      "module \"x\" {\n  source = \"./x\"\n}\n",
		"modules/m1/lab/terraform/x/main.tf":   "# a local module\n",
	})
	if _, probs := Load(ok); len(probs) > 0 {
		t.Fatalf("provider sources and local modules are allowed: %v", probs)
	}
}

func TestForge401IsAnAWSLab(t *testing.T) {
	tr, probs := Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatalf("forge-401: %v", probs)
	}
	lab := tr.Module("01-cloud-heat").Lab
	if lab.ID != "cloud-heat" || lab.Runtime != "aws" || lab.AWS.Region != "eu-west-1" || lab.AWS.MaxHourlyUSD != 0.05 || len(lab.Tasks) != 2 {
		t.Fatalf("cloud-heat: %+v", lab)
	}
}

func TestLabTFVarsIsJSON(t *testing.T) {
	var v map[string]string
	if err := json.Unmarshal(LabTFVars("abcdefabcdef", `te"am`, "tr", "eu-west-1"), &v); err != nil || v["crucible_team"] != `te"am` || v["crucible_region"] != "eu-west-1" {
		t.Fatalf("tfvars must be JSON so no value escapes its string: %v %v", v, err)
	}
	for _, want := range []string{`backend "s3" {}`, `provider "aws"`, `"crucible:lab-id"   = var.crucible_lab_id`} {
		if !strings.Contains(LabTF, want) {
			t.Fatalf("LabTF lacks %q", want)
		}
	}
}
```

(Also add `"encoding/json"` to the imports.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/content/ -run 'AWSLab|Forge401|LabTFVars' -count=1 2>&1 | tail -8`
Expected: FAIL to compile (`undefined: LabTFVars`, `undefined: LabTF`).

- [ ] **Step 3: Implement**

Create `internal/content/awstf.go`:

```go
package content

import "encoding/json"

// File names Crucible adds next to an aws lab's terraform/ module (spec §8.2).
const (
	LabTFFile     = "crucible.tf"
	LabTFVarsFile = "crucible.auto.tfvars.json"
)

// LabTF is added to every aws lab module. It declares the S3 backend (configured per lab at init), the AWS provider
// with the lab's region and the crucible:* default tags that IAM, the tag sweep, the reaper and Cost Explorer rely
// on, and the variables a module may use (var.crucible_lab_id names lab buckets: crucible-lab-<id>…). Lab modules
// must not declare a provider "aws" or a backend themselves (lint).
const LabTF = `# Added by Crucible. Lab modules must not declare provider "aws" or a backend.
terraform {
  backend "s3" {}
}

variable "crucible_lab_id" { type = string }
variable "crucible_team" { type = string }
variable "crucible_training" { type = string }
variable "crucible_region" { type = string }

provider "aws" {
  region = var.crucible_region
  default_tags {
    tags = {
      "crucible:lab-id"   = var.crucible_lab_id
      "crucible:team"     = var.crucible_team
      "crucible:training" = var.crucible_training
    }
  }
}
`

// LabTFVars fills LabTF's variables. JSON, so no value can break out of its string.
func LabTFVars(labID, team, training, region string) []byte {
	b, _ := json.Marshal(map[string]string{"crucible_lab_id": labID, "crucible_team": team,
		"crucible_training": training, "crucible_region": region})
	return b
}
```

In `internal/content/load.go`, replace the aws case:

```go
	case "aws":
		services["workspace"] = true // AWS labs get one workspace container (spec §8.2)
		l.awsModule(dir, lf, lab)
```

and add (with `"io/fs"`, `"os"` and `"regexp"` in the imports if they are missing):

```go
var (
	awsRegionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	tfProvider  = regexp.MustCompile(`(?m)^\s*provider\s+"aws"`)
	tfBackend   = regexp.MustCompile(`(?m)^\s*backend\s+"`)
	tfSource    = regexp.MustCompile(`(?m)^\s*source\s*=\s*"([^"]*)"`)
	// a provider source in required_providers ("hashicorp/aws", "registry.terraform.io/hashicorp/aws"), not a module
	providerSource = regexp.MustCompile(`^([a-z0-9-]+\.[a-z0-9.-]+/)?[a-z0-9-]+/[a-z0-9-]+$`)
)

const maxModuleBytes = 512 << 10 // the module travels in a ConfigMap (1 MiB, base64)

// awsModule checks an aws lab (spec §4.5, §8.2). Region and price ceiling are required. terraform/ holds a module
// Crucible runs as-is after adding its own provider and backend, so the module must not declare them, and module
// sources must be local so neither infracost (in the API pod) nor terraform fetches code from the network.
func (l *loader) awsModule(dir, lf string, lab *Lab) {
	if lab.AWS == nil || !awsRegionRe.MatchString(lab.AWS.Region) {
		l.add(lf, "aws.region is required for runtime: aws (e.g. eu-west-1)")
	}
	if lab.AWS == nil || lab.AWS.MaxHourlyUSD <= 0 {
		l.add(lf, "aws.max_hourly_usd must be set above 0 for runtime: aws")
	}
	files, size := 0, int64(0)
	_ = filepath.WalkDir(filepath.Join(dir, "terraform"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
		}
		if !strings.HasSuffix(p, ".tf") {
			return nil
		}
		files++
		b, err := os.ReadFile(p)
		if err != nil {
			l.add(p, "%v", err)
			return nil
		}
		if tfProvider.Match(b) {
			l.add(p, `do not declare provider "aws": Crucible adds it with the lab's region and tags`)
		}
		if tfBackend.Match(b) {
			l.add(p, "do not declare a backend: Crucible stores state per lab")
		}
		for _, m := range tfSource.FindAllSubmatch(b, -1) {
			if s := string(m[1]); !strings.HasPrefix(s, "./") && !providerSource.MatchString(s) {
				l.add(p, "module source %q must be a local path (./…)", s)
			}
		}
		return nil
	})
	if files == 0 {
		l.add(lf, "runtime: aws needs a terraform/ directory with at least one .tf file")
	}
	if size > maxModuleBytes {
		l.add(lf, "terraform/ is %d KiB; keep it under %d KiB", size>>10, maxModuleBytes>>10)
	}
}
```

(Symlinks anywhere in a lab are already refused by `TestLoadRejectsSymlinks`'s rule.)

In `internal/labs/bundle.go`, add `"terraform": true` to `bundleSkip` and extend its comment: `terraform/ is the aws lab's module: it runs in the runner pod, never in the trainee's workspace.`

- [ ] **Step 4: The Forge 401 fixture**

`examples/forge-401/training.yaml`:

```yaml
id: forge-401
title: "Forge 401: Cloud Heat"
description: One AWS lab. make cluster-check runs it in dry-run mode; a sandbox account runs it for real (docs/runbooks/aws.md).
maintainers: [senior@crucible.local]
progression: free
modules: [01-cloud-heat]
```

`examples/forge-401/modules/01-cloud-heat/module.yaml`:

```yaml
title: "Cloud Heat"
items:
  - lab: lab
```

`examples/forge-401/modules/01-cloud-heat/lab/lab.yaml`:

```yaml
id: cloud-heat
runtime: aws
ttl: 1h
idle_timeout: 30m
task_order: free
terminals:
  - { name: workspace, service: workspace }
tasks:
  - id: t1-region
    instructions: tasks/01-region.md
    check: { script: checks/01-region.sh, run_in: workspace }
    points: 1
  - id: t2-bucket
    instructions: tasks/02-bucket.md
    check: { script: checks/02-bucket.sh, run_in: workspace, timeout: 60s }
    points: 2
aws:
  region: eu-west-1
  max_hourly_usd: 0.05
```

`examples/forge-401/modules/01-cloud-heat/lab/terraform/main.tf`:

```hcl
# The lab's own bucket. Crucible adds the provider (region, crucible:* default tags) and the state backend.
resource "aws_s3_bucket" "forge" {
  bucket        = "crucible-lab-${var.crucible_lab_id}"
  force_destroy = true
}
```

`tasks/01-region.md`:

```markdown
# Find your region

Your lab runs in one AWS region, and the CLI in this terminal already has short-lived credentials for it.
Run `aws configure list`, then write the region it shows into `~/region.txt` and press **Check**.
```

`tasks/02-bucket.md`:

```markdown
# Forge a file in your bucket

Crucible created a bucket for this lab: `crucible-lab-$CRUCIBLE_LAB_ID`.
Upload any file to it under the key `forged.txt` (`aws s3 cp`), then press **Check**.
```

`checks/01-region.sh` (then `chmod 755`):

```sh
#!/bin/sh
# Offline on purpose: the dry-run e2e has no AWS behind it.
got=$(tr -d '[:space:]' < "$HOME/region.txt" 2>/dev/null)
if [ -z "$got" ]; then
  echo "No ~/region.txt yet."
  exit 1
fi
if [ "$got" != "$AWS_REGION" ]; then
  echo "~/region.txt says $got, but your lab runs somewhere else."
  exit 1
fi
if [ ! -s "$AWS_SHARED_CREDENTIALS_FILE" ]; then
  echo "Your lab credentials are missing; end the lab and start it again."
  exit 1
fi
echo "Your CLI points at $AWS_REGION with your lab's credentials."
```

`checks/02-bucket.sh` (then `chmod 755`):

```sh
#!/bin/sh
if aws s3api head-object --bucket "crucible-lab-$CRUCIBLE_LAB_ID" --key forged.txt >/dev/null 2>&1; then
  echo "forged.txt is in your lab bucket."
  exit 0
fi
echo "No forged.txt in s3://crucible-lab-$CRUCIBLE_LAB_ID yet."
exit 1
```

Append to `examples/platform/trainings.yaml`:

```yaml
  forge-401:
    repo: file:///git/forge-401.git   # aws lab e2e fixture (dry run on kind); the e2e enrols the team through the UI
    branch: main
```

In `scripts/seed-git.sh`, add `forge-401` to the `for name in …` list (and to the header comment). In `scripts/local-check.sh`, add `./bin/crucible lint examples/forge-401` after the other lint lines.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/content/ ./internal/config/ ./internal/labs/ -count=1 2>&1 | tail -5 && go run ./cmd/crucible lint examples/forge-401 && go run ./cmd/crucible lint examples/platform`
Expected: `ok` for each package. Lint prints `forge-401: 1 module(s) OK. Ready for the forge.` and `platform config OK. The forge is ready.` If a Go test counts trainings in `examples/platform`, update that count. M5 met the same thing for `forge-301`.

- [ ] **Step 6: Commit**

```bash
git add internal/content internal/labs/bundle.go examples/forge-401 examples/platform/trainings.yaml scripts/seed-git.sh scripts/local-check.sh
git commit -m "feat(content): aws lab contract (region, price ceiling, module rules) and the Forge 401 fixture

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Settled actuals in spend, and the budget cap on the timer

**Files:**
- Create: `internal/db/migrations/000NN_aws_ledger.sql` (next free number)
- Modify: `internal/labs/approvals.go` (`Spend`, `spend`, new `labCostSQL`)
- Modify: `internal/labs/finops.go` (add `budgetLimit`)
- Modify: `internal/labs/service.go` (`provision` end time, `view` CanExtend, the `Extend` ponytail comment)
- Modify: `internal/labs/finops_test.go`

**Interfaces:**
- Consumes: `f.spent(t, id, training, usd)`, `f.request`, `f.waitState` (existing test helpers).
- Produces:
  - Tables `cost_actuals(lab_id, day, usd, updated_at)`, `aws_ops` (one row), `reaper_findings(source, arn, lab_id, action, detail, first_at, last_at)`.
  - `Spend.ActualUSD float64` (`json:"actual_usd"`).
  - `labCostSQL`: a `SELECT` over `lab_instances l` with the extra columns `actual`, `settled` and `est_spent`. `$1` must be "now". Tasks 10 and later reuse it.
  - `(*Service).budgetLimit(ctx, inst, now) Limit`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/labs/finops_test.go`:

```go
func closeTo(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// endedAgo moves a lab (inserted by f.spent: one hour of running) so that it ended d ago.
func (f *fx) endedAgo(t *testing.T, id string, d time.Duration) {
	t.Helper()
	end := f.clk.Now().Add(-d)
	if _, err := f.s.DB.Exec(context.Background(), `UPDATE lab_instances SET ready_at = $2::timestamptz - interval '1 hour',
		destroyed_at = $2 WHERE id = $1`, id, end); err != nil {
		t.Fatal(err)
	}
}

func TestSpendUsesSettledActualsOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "aaaaaaaaaaa1", "forge-101", 2) // ended 3 days ago, Cost Explorer says $0.40
	f.spent(t, "aaaaaaaaaaa2", "forge-101", 2) // ended an hour ago, Cost Explorer says $5 so far (not settled)
	f.spent(t, "aaaaaaaaaaa3", "forge-101", 2) // ended 3 days ago, never reported (tags not activated?)
	f.endedAgo(t, "aaaaaaaaaaa1", 72*time.Hour)
	f.endedAgo(t, "aaaaaaaaaaa3", 72*time.Hour)
	for id, usd := range map[string]float64{"aaaaaaaaaaa1": 0.4, "aaaaaaaaaaa2": 5} {
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ($1, $2, $3, $2)`,
			id, f.clk.Now(), usd); err != nil {
			t.Fatal(err)
		}
	}
	sp, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil || !closeTo(sp.SpentUSD, 6) || sp.ActualUSD != 0 {
		t.Fatalf("no successful ingestion yet: estimates only, got %+v %v", sp, err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	sp, _ = f.s.spend(ctx, f.plat, "forge", "")
	if !closeTo(sp.SpentUSD, 4.4) || !closeTo(sp.ActualUSD, 0.4) || !closeTo(sp.CommittedUSD, 4.4) {
		t.Fatalf("only the settled, reported lab uses its actual ($0.40 + $2 + $2): %+v", sp)
	}
}

func TestBudgetCapSetsTheTimer(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 8 // 2 h TTL → $16: the team leader's tier
	v := f.request(t, f.u)
	// Approval re-checks the cap, so the squeeze happens while the lab provisions: another lab books $240 of the
	// team's $250 hard cap. (f.spent only fails on a broken database.)
	f.run.provision = func() { f.spent(t, "bbbbbbbbbbb9", "forge-101", 240) }
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, v.ID, Ready)
	inst, _ := f.s.owned(ctx, f.u, v.ID)
	if got.LimitReason != "budget" || !inst.EndsAt.Equal(inst.ReadyAt.Add(75*time.Minute)) || got.CanExtend {
		t.Fatalf("$10 of headroom at $8/h is 75 minutes, ending at the cap, no extension: %+v ends %v", got, inst.EndsAt)
	}
	inst.OverCap = true
	if lim := f.s.budgetLimit(ctx, inst, f.clk.Now()); !lim.At.IsZero() {
		t.Fatal("a lab an admin approved over the cap has no budget limit")
	}
	inst.OverCap, inst.HourlyUSD = false, 0
	if lim := f.s.budgetLimit(ctx, inst, f.clk.Now()); !lim.At.IsZero() {
		t.Fatal("a free lab has no budget limit")
	}
}
```

(Add `"math"` to the test imports. If a `closeTo` helper already exists in the package, reuse it.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'SettledActuals|BudgetCapSetsTheTimer' -count=1 2>&1 | tail -6`
Expected: FAIL. `cost_actuals` does not exist, and `budgetLimit` and `ActualUSD` are undefined.

- [ ] **Step 3: The migration**

Create `internal/db/migrations/000NN_aws_ledger.sql`:

```sql
-- +goose Up
-- AWS lab costs and clean-up (spec §8.2, §9.3).
-- Daily cost per lab from Cost Explorer, grouped by the crucible:lab-id tag. No foreign key: Cost Explorer can
-- report labs this database no longer has.
CREATE TABLE cost_actuals (
  lab_id     TEXT NOT NULL,
  day        DATE NOT NULL,
  usd        DOUBLE PRECISION NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (lab_id, day)
);
CREATE INDEX cost_actuals_day ON cost_actuals (day);

-- One row: the last cost ingestion and reaper run, and their last errors.
CREATE TABLE aws_ops (
  id              BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  ingest_ok_at    TIMESTAMPTZ,
  ingest_error    TEXT NOT NULL DEFAULT '',
  ingest_error_at TIMESTAMPTZ,
  reap_ok_at      TIMESTAMPTZ,
  reap_error      TEXT NOT NULL DEFAULT '',
  reap_error_at   TIMESTAMPTZ
);
INSERT INTO aws_ops DEFAULT VALUES;

-- What the destroy-time tag sweep, the reaper and the CloudTrail check found. Seeing the same thing again updates it.
CREATE TABLE reaper_findings (
  source   TEXT NOT NULL CHECK (source IN ('destroy', 'reaper', 'trail')),
  arn      TEXT NOT NULL,             -- resource ARN, or cloudtrail:<event id>
  lab_id   TEXT NOT NULL DEFAULT '',
  action   TEXT NOT NULL CHECK (action IN ('deleted', 'failed', 'reported')),
  detail   TEXT NOT NULL DEFAULT '',
  first_at TIMESTAMPTZ NOT NULL,
  last_at  TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (source, arn)
);
CREATE INDEX reaper_findings_last ON reaper_findings (last_at DESC);

-- +goose Down
DROP TABLE reaper_findings, aws_ops, cost_actuals;
```

- [ ] **Step 4: Spend with settled actuals**

In `internal/labs/approvals.go`, change the `Spend` comment and add the field, then replace `spend`:

```go
// Spend is month-to-date lab spend for a team or one program: estimates, with Cost Explorer actuals swapped in for
// labs whose actual cost has settled (spec §9.3).
type Spend struct {
	SpentUSD     float64 `json:"spent_usd"`     // per lab: settled actual, else hourly estimate × time run this month
	CommittedUSD float64 `json:"committed_usd"` // spent + the rest of every lab still starting or running, to its end
	ActualUSD    float64 `json:"actual_usd"`    // the part of spent that comes from settled actuals
	BudgetUSD    float64 `json:"budget_usd"`    // 0 = no budget
	CapUSD       float64 `json:"cap_usd"`       // 0 = no hard cap
}

// labCostSQL selects lab_instances (alias l) with three more columns: actual (Cost Explorer total, NULL when never
// reported), settled (use the actual instead of the estimate: reported, and the lab ended 48 h before the last
// successful ingestion, so Cost Explorer has caught up) and est_spent (hourly estimate × time run). $1 is now.
// Conservative on purpose: a lab Cost Explorer never reported keeps its estimate (tags not activated ≠ free).
const labCostSQL = `SELECT l.*, a.usd AS actual,
	coalesce(a.usd IS NOT NULL AND l.destroyed_at < (SELECT ingest_ok_at FROM aws_ops) - interval '48 hours', false) AS settled,
	CASE WHEN l.ready_at IS NULL THEN 0
		ELSE l.hourly_usd * extract(epoch FROM least(coalesce(l.destroyed_at, $1), $1) - l.ready_at)::float8 / 3600 END AS est_spent
	FROM lab_instances l LEFT JOIN (SELECT lab_id, sum(usd) AS usd FROM cost_actuals GROUP BY lab_id) a ON a.lab_id = l.id`

// spend sums this calendar month (UTC) for a team, or one program when training != "".
// ponytail: a lab counts in the month it was requested; one running across midnight on the 1st stays in the old month.
func (s *Service) spend(ctx context.Context, p *config.Platform, team, training string) (Spend, error) {
	now := s.Now()
	var sp Spend
	err := s.DB.QueryRow(ctx, `WITH labs AS (`+labCostSQL+`
		WHERE l.team = $2 AND ($3 = '' OR l.training = $3) AND l.created_at >= $4)
		SELECT
			coalesce(sum(CASE WHEN settled THEN actual ELSE est_spent END), 0),
			coalesce(sum(CASE
				WHEN settled THEN actual
				WHEN state = 'provisioning' THEN estimate_usd
				WHEN state = 'ready' THEN hourly_usd * extract(epoch FROM greatest(ends_at, $1) - ready_at)::float8 / 3600
				ELSE est_spent END), 0),
			coalesce(sum(CASE WHEN settled THEN actual ELSE 0 END), 0)
		FROM labs`, now, team, training, monthStart(now)).Scan(&sp.SpentUSD, &sp.CommittedUSD, &sp.ActualUSD)
	if err != nil {
		return sp, err
	}
	if t := p.Teams[team]; t != nil {
		if training == "" {
			sp.BudgetUSD, sp.CapUSD = t.Budget.MonthlyUSD, t.Budget.HardCapUSD
		} else if pr := t.Programs[training]; pr != nil {
			sp.BudgetUSD, sp.CapUSD = pr.BudgetUSDMonth, pr.BudgetUSDMonth
		}
	}
	return sp, nil
}
```

`labs.*` in the CTE includes `ends_at`, `hourly_usd`, `state` and `estimate_usd`, so the outer `SELECT` reads them directly.

- [ ] **Step 5: The budget limit**

Append to `internal/labs/finops.go`:

```go
// budgetLimit is when this lab's running cost would reach the team's or the program's hard cap (spec §8.6): the
// headroom other labs leave, divided by this lab's hourly rate, from when it is ready. Called while the lab is
// still provisioning, so spend() counts its whole estimate: take that back out. Labs an admin approved over the cap
// and free labs have no budget limit.
func (s *Service) budgetLimit(ctx context.Context, inst *Instance, now time.Time) Limit {
	st := s.Learn.State()
	if inst.HourlyUSD <= 0 || inst.OverCap || st == nil || st.Platform == nil {
		return Limit{}
	}
	var best Limit
	for _, scope := range []string{"", inst.Training} {
		sp, err := s.spend(ctx, st.Platform, inst.Team, scope)
		if err != nil {
			s.Log.Warn("budget limit: reading spend failed", "lab", inst.ID, "err", err)
			continue
		}
		if sp.CapUSD <= 0 {
			continue
		}
		headroom := max(0, sp.CapUSD-(sp.CommittedUSD-inst.EstimateUSD))
		at := now.Add(time.Duration(headroom / inst.HourlyUSD * float64(time.Hour)))
		best = EffectiveEnd(best, Limit{At: at, Reason: "budget"})
	}
	return best
}
```

In `internal/labs/service.go` `provision`, change the end-time line to:

```go
	end := EffectiveEnd(Limit{At: now.Add(inst.TTL), Reason: "ttl"}, s.scheduleLimit(inst, now), s.budgetLimit(ctx, inst, now))
```

In `view`, change `CanExtend` to `!inst.Extended && inst.MaxExtension > 0 && inst.LimitReason != "schedule" && inst.LimitReason != "budget"`. In `Extend`, replace the ponytail comment with: `// ponytail: spec §8.6 "Extension pending" (send the extension back through approval) is deferred to the M7 coverage pass; until then an extension that would lift the estimate into a higher tier is refused.`

The web `Timer` already labels `limit_reason === 'budget'`.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/labs/ -count=1 2>&1 | tail -4`
Expected: `ok`. The existing spend, cap and alert tests still pass, because nothing has been ingested, so every lab uses its estimate as before.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations internal/labs/approvals.go internal/labs/finops.go internal/labs/service.go internal/labs/finops_test.go
git commit -m "feat(finops): settled Cost Explorer actuals replace estimates in spend; budget cap ends the lab timer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: `internal/awscloud`: the Cloud interface and an in-memory fake lab account

**Files:**
- Create: `internal/awscloud/awscloud.go`
- Create: `internal/awscloud/fake.go`
- Create: `internal/awscloud/fake_test.go`

**Interfaces:**
- Produces (used by Tasks 4, 7, 8, 9, 14):

```go
const TagLab, TagTeam, TagTraining = "crucible:lab-id", "crucible:team", "crucible:training"
type Session struct{ LabID, Team, Training string }
type Credentials struct { AccessKeyID, SecretAccessKey, SessionToken string; Expires time.Time }
func (c Credentials) File() []byte                         // shared credentials file, profile [default]
type Resource struct{ ARN, LabID string }
type DailyCost struct { Day time.Time; LabID string; USD float64 }
type TrailEvent struct { ID string; At time.Time; LabID, Event string; Resources []string }
var ErrUnsupported error
type Cloud interface {
	AssumeLab(ctx context.Context, s Session) (Credentials, error)
	Tagged(ctx context.Context, region, labID string) ([]Resource, error)
	Delete(ctx context.Context, region string, c Credentials, arn string) (deleted bool, err error)
	Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error)
	LabWrites(ctx context.Context, region string, since time.Time) ([]TrailEvent, error)
}
func ValidLabID(id string) bool
func SessionName(labID string) string                      // "crucible-lab-<id>"
type Fake struct { Now func() time.Time; Err error /* … */ }
func (f *Fake) Add(region string, r Resource); AddCost(DailyCost); AddEvent(TrailEvent); Has(arn string) bool
func (f *Fake) Assumed() []Session
func (f *Fake) SimulateApply(region string, s Session); SimulateDestroy(labID string)
```

- [ ] **Step 1: Write the failing test**

Create `internal/awscloud/fake_test.go`:

```go
package awscloud

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFakeBehavesLikeTheLabAccount(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	f := &Fake{Now: func() time.Time { return now }}
	if _, err := f.AssumeLab(ctx, Session{LabID: "../etc"}); err == nil {
		t.Fatal("a malformed lab id must never become a session tag")
	}
	a, _ := f.AssumeLab(ctx, Session{LabID: "aaaaaaaaaaaa", Team: "forge"})
	b, _ := f.AssumeLab(ctx, Session{LabID: "bbbbbbbbbbbb"})
	if !a.Expires.Equal(now.Add(time.Hour)) || !strings.Contains(string(a.File()), "aws_session_token = ") {
		t.Fatalf("one-hour credentials as a shared credentials file: %+v\n%s", a, a.File())
	}
	f.SimulateApply("eu-west-1", Session{LabID: "aaaaaaaaaaaa"})
	got, _ := f.Tagged(ctx, "eu-west-1", "aaaaaaaaaaaa")
	if len(got) != 2 {
		t.Fatalf("apply leaves the lab bucket and a hand-made volume: %+v", got)
	}
	if other, _ := f.Tagged(ctx, "us-east-1", ""); len(other) != 0 {
		t.Fatalf("inventory is per region: %+v", other)
	}
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-aaaaaaaaaaaa"
	if _, err := f.Delete(ctx, "eu-west-1", b, vol); err == nil {
		t.Fatal("like IAM, another lab's session cannot delete this lab's resources")
	}
	f.SimulateDestroy("aaaaaaaaaaaa")
	if f.Has("arn:aws:s3:::crucible-lab-aaaaaaaaaaaa") || !f.Has(vol) {
		t.Fatal("terraform destroy removes the bucket it manages, not the hand-made volume")
	}
	if ok, err := f.Delete(ctx, "eu-west-1", a, vol); !ok || err != nil || f.Has(vol) {
		t.Fatalf("the lab's own session deletes it: %v %v", ok, err)
	}
	if ok, err := f.Delete(ctx, "eu-west-1", a, vol); ok || err != nil {
		t.Fatalf("an already deleted resource is (false, nil): %v %v", ok, err)
	}
	if _, err := f.Delete(ctx, "eu-west-1", a, "arn:aws:rds:eu-west-1:1:db:x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown types are reported, not guessed at: %v", err)
	}
	if c, _ := f.Costs(ctx, now.Add(-24*time.Hour), now.Add(24*time.Hour)); len(c) != 1 || c[0].USD != 0.11 {
		t.Fatalf("apply books a small actual cost for the Ledger: %+v", c)
	}
	f.Err = errors.New("throttled")
	if _, err := f.Costs(ctx, now, now); err == nil {
		t.Fatal("Err makes every call fail")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/awscloud/ -count=1 2>&1 | tail -3`
Expected: FAIL (`no Go files` / undefined).

- [ ] **Step 3: Implement**

Create `internal/awscloud/awscloud.go`:

```go
// Package awscloud is Crucible's narrow view of the shared AWS lab account (spec §8.2, §9.3): lab credentials with
// session tags, the tag inventory, deleting leftovers, CloudTrail create calls and Cost Explorer. Client talks to
// AWS; Fake keeps everything in memory for tests and for CRUCIBLE_AWS_LABS=dryrun.
package awscloud

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
)

// Tags Crucible puts on every lab session and every lab resource (provider default_tags, IAM conditions).
const (
	TagLab      = "crucible:lab-id"
	TagTeam     = "crucible:team"
	TagTraining = "crucible:training"
)

type Session struct{ LabID, Team, Training string }

type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expires                                    time.Time
}

// File is the shared credentials file the workspace and the terraform pods read (AWS_SHARED_CREDENTIALS_FILE).
func (c Credentials) File() []byte {
	return []byte("[default]\naws_access_key_id = " + c.AccessKeyID + "\naws_secret_access_key = " + c.SecretAccessKey +
		"\naws_session_token = " + c.SessionToken + "\n")
}

// Resource is one tagged resource. LabID is the raw crucible:lab-id tag value: untrusted, check ValidLabID.
type Resource struct{ ARN, LabID string }

type DailyCost struct {
	Day   time.Time // UTC midnight
	LabID string
	USD   float64
}

// TrailEvent is a create call by a lab session that carried no crucible:lab-id tag (see Client.LabWrites).
type TrailEvent struct {
	ID        string
	At        time.Time
	LabID     string
	Event     string
	Resources []string
}

var ErrUnsupported = errors.New("Crucible cannot delete this resource type; delete it by hand")

type Cloud interface {
	// AssumeLab returns one-hour credentials for the lab role, tagged with the session's lab, team and training.
	AssumeLab(ctx context.Context, s Session) (Credentials, error)
	// Tagged lists resources in region tagged crucible:lab-id (= labID, or any value when labID is "").
	Tagged(ctx context.Context, region, labID string) ([]Resource, error)
	// Delete removes one resource with lab credentials. deleted is false when it was already gone;
	// ErrUnsupported for types Crucible does not delete.
	Delete(ctx context.Context, region string, c Credentials, arn string) (deleted bool, err error)
	// Costs is daily cost per lab for [from, to); untagged spend is left out.
	Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error)
	// LabWrites lists successful create calls by lab sessions since `since` whose request had no lab tag.
	LabWrites(ctx context.Context, region string, since time.Time) ([]TrailEvent, error)
}

var labIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ValidLabID guards every value that becomes a session tag or decides a deletion.
func ValidLabID(id string) bool { return labIDRe.MatchString(id) }

func SessionName(labID string) string { return "crucible-lab-" + labID }

// kind is the resource type Delete knows how to remove, and its id; "" when it does not.
func kind(s string) (typ, id string) {
	a, err := arn.Parse(s)
	if err != nil {
		return "", ""
	}
	switch a.Service {
	case "ec2":
		t, id, _ := strings.Cut(a.Resource, "/")
		if t == "instance" || t == "volume" || t == "security-group" {
			return t, id
		}
	case "s3":
		if a.Resource != "" && !strings.Contains(a.Resource, "/") {
			return "bucket", a.Resource
		}
	}
	return "", ""
}
```

Create `internal/awscloud/fake.go`:

```go
package awscloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory lab account. Deletes follow the lab role's rule: a session may delete only its own lab's
// resources. It backs unit tests and CRUCIBLE_AWS_LABS=dryrun (see SimulateApply).
type Fake struct {
	Now func() time.Time
	Err error // when set, every call fails with it (throttling, Cost Explorer down…)

	mu        sync.Mutex
	resources map[string]fakeRes // by ARN
	assumed   []Session
	costs     []DailyCost
	events    []TrailEvent
}

type fakeRes struct {
	region string
	r      Resource
}

var _ Cloud = (*Fake)(nil)

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fake) Add(region string, r Resource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resources == nil {
		f.resources = map[string]fakeRes{}
	}
	f.resources[r.ARN] = fakeRes{region, r}
}

func (f *Fake) AddCost(c DailyCost) { f.mu.Lock(); defer f.mu.Unlock(); f.costs = append(f.costs, c) }

func (f *Fake) AddEvent(e TrailEvent) { f.mu.Lock(); defer f.mu.Unlock(); f.events = append(f.events, e) }

func (f *Fake) Has(arn string) bool { f.mu.Lock(); defer f.mu.Unlock(); _, ok := f.resources[arn]; return ok }

func (f *Fake) Assumed() []Session { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.assumed) }

func token(labID string) string { return "fake-token-" + labID }

func (f *Fake) AssumeLab(_ context.Context, s Session) (Credentials, error) {
	if !ValidLabID(s.LabID) {
		return Credentials{}, fmt.Errorf("invalid lab id %q", s.LabID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Credentials{}, f.Err
	}
	f.assumed = append(f.assumed, s)
	return Credentials{AccessKeyID: "ASIAFAKE" + strings.ToUpper(s.LabID[:8]), SecretAccessKey: "fake-secret",
		SessionToken: token(s.LabID), Expires: f.now().Add(time.Hour)}, nil
}

func (f *Fake) Tagged(_ context.Context, region, labID string) ([]Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []Resource
	for _, x := range f.resources {
		if x.region == region && (labID == "" || x.r.LabID == labID) {
			out = append(out, x.r)
		}
	}
	slices.SortFunc(out, func(a, b Resource) int { return strings.Compare(a.ARN, b.ARN) })
	return out, nil
}

func (f *Fake) Delete(_ context.Context, region string, c Credentials, arn string) (bool, error) {
	if t, _ := kind(arn); t == "" {
		return false, ErrUnsupported
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return false, f.Err
	}
	x, ok := f.resources[arn]
	if !ok || x.region != region {
		return false, nil
	}
	if c.SessionToken != token(x.r.LabID) {
		return false, errors.New("AccessDenied: not this lab's resource")
	}
	delete(f.resources, arn)
	return true, nil
}

func (f *Fake) Costs(_ context.Context, from, to time.Time) ([]DailyCost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []DailyCost
	for _, c := range f.costs {
		if !c.Day.Before(from) && c.Day.Before(to) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *Fake) LabWrites(_ context.Context, _ string, since time.Time) ([]TrailEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []TrailEvent
	for _, e := range f.events {
		if !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

// SimulateApply stands in for `terraform apply` in dry-run mode. It adds the lab's bucket (managed by terraform)
// and a volume "made by hand in the workspace", which only the destroy-time tag sweep removes. It also books $0.11
// of cost today, so the Ledger has an actual to show.
func (f *Fake) SimulateApply(region string, s Session) {
	f.Add(region, Resource{ARN: "arn:aws:s3:::crucible-lab-" + s.LabID, LabID: s.LabID})
	f.Add(region, Resource{ARN: fmt.Sprintf("arn:aws:ec2:%s:000000000000:volume/vol-%s", region, s.LabID), LabID: s.LabID})
	f.AddCost(DailyCost{Day: f.now().UTC().Truncate(24 * time.Hour), LabID: s.LabID, USD: 0.11})
}

// SimulateDestroy stands in for `terraform destroy`: it removes only what terraform manages (the bucket).
func (f *Fake) SimulateDestroy(labID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, "arn:aws:s3:::crucible-lab-"+labID)
}
```

`github.com/aws/aws-sdk-go-v2/aws/arn` is part of the core SDK module that M5 already requires. If `go build` says the module is missing, run `go get github.com/aws/aws-sdk-go-v2` at the version already in `go.mod`.

- [ ] **Step 4: Run the test**

Run: `go test -race ./internal/awscloud/ -count=1 -v 2>&1 | tail -4`
Expected: `--- PASS: TestFakeBehavesLikeTheLabAccount`, `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/awscloud go.mod go.sum
git commit -m "feat(awscloud): one interface for the lab account, with an in-memory fake

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `awscloud.Client`: STS, tagging, deletes, Cost Explorer and CloudTrail through aws-sdk-go-v2

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/awscloud/client.go`
- Create: `internal/awscloud/client_test.go`

**Interfaces:**
- Consumes: Task 3 types.
- Produces:
  - `type Config struct{ LabRoleARN, OpsRoleARN, Endpoint string }`.
  - `func New(ctx context.Context, c Config) (*Client, error)`, which uses the default credential chain: the node's instance role in production.
  - `*Client` implements `Cloud`.

- [ ] **Step 1: Add the SDK services (same release line as M5's `config`/`s3`)**

Run: `go get github.com/aws/aws-sdk-go-v2/service/sts github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi github.com/aws/aws-sdk-go-v2/service/costexplorer github.com/aws/aws-sdk-go-v2/service/cloudtrail github.com/aws/aws-sdk-go-v2/service/ec2 github.com/aws/aws-sdk-go-v2/credentials`
Expected: `go.mod` gains the five services. `credentials` and `smithy-go` are already indirect dependencies through `config`. Run `go mod tidy` after Step 4.

- [ ] **Step 2: Write the failing tests**

Create `internal/awscloud/client_test.go`:

```go
package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

type call struct {
	target, method, path, query, body string
	form                              url.Values
}

const assumeXML = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>
<AccessKeyId>ASIA%s</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token-%s</SessionToken>
<Expiration>2026-10-05T10:00:00Z</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::1:assumed-role/r/s</Arn>
<AssumedRoleId>AROA:s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`

// fakeAWS answers every service on one httptest server. The ops role is assumed transparently; reply answers the rest.
func fakeAWS(t *testing.T, reply func(c call) (code int, contentType, body string)) (*Client, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := call{target: r.Header.Get("X-Amz-Target"), method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(b)}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			c.form, _ = url.ParseQuery(string(b))
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if c.form.Get("Action") == "AssumeRole" {
			w.Header().Set("Content-Type", "text/xml")
			name := c.form.Get("RoleSessionName")
			_, _ = io.WriteString(w, strings.ReplaceAll(assumeXML, "%s", name))
			return
		}
		code, ct, body := reply(c)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	node := aws.Config{Region: "eu-west-1", RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider("AKIDNODE", "secret", "")}
	cl := newClient(node, Config{LabRoleARN: "arn:aws:iam::444455556666:role/crucible-lab",
		OpsRoleARN: "arn:aws:iam::444455556666:role/crucible-lab-ops", Endpoint: srv.URL})
	return cl, func() []call { mu.Lock(); defer mu.Unlock(); return append([]call(nil), calls...) }
}

func TestAssumeLabTagsTheSession(t *testing.T) {
	cl, calls := fakeAWS(t, func(call) (int, string, string) { return 500, "text/plain", "" })
	c, err := cl.AssumeLab(context.Background(), Session{LabID: "aaaaaaaaaaaa", Team: "forge", Training: "forge-401"})
	if err != nil || c.AccessKeyID != "ASIAcrucible-lab-aaaaaaaaaaaa" || !c.Expires.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("credentials: %+v %v", c, err)
	}
	f := calls()[0].form
	want := map[string]string{"RoleArn": "arn:aws:iam::444455556666:role/crucible-lab", "RoleSessionName": "crucible-lab-aaaaaaaaaaaa",
		"DurationSeconds": "3600", "Tags.member.1.Key": "crucible:lab-id", "Tags.member.1.Value": "aaaaaaaaaaaa",
		"Tags.member.2.Key": "crucible:team", "Tags.member.2.Value": "forge", "Tags.member.3.Key": "crucible:training"}
	for k, v := range want {
		if f.Get(k) != v {
			t.Fatalf("%s = %q, want %q (form %v)", k, f.Get(k), v, f)
		}
	}
	if _, err := cl.AssumeLab(context.Background(), Session{LabID: "AAAA; Deny"}); err == nil || len(calls()) != 1 {
		t.Fatal("a malformed lab id never reaches STS")
	}
}

func TestTaggedUsesTheOpsRoleAndFiltersByLab(t *testing.T) {
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		if c.target != "ResourceGroupsTaggingAPI_20170126.GetResources" {
			return 400, "text/plain", "unexpected " + c.target
		}
		return 200, "application/x-amz-json-1.1", `{"PaginationToken":"","ResourceTagMappingList":[
			{"ResourceARN":"arn:aws:s3:::crucible-lab-aaaaaaaaaaaa","Tags":[{"Key":"crucible:lab-id","Value":"aaaaaaaaaaaa"},{"Key":"app","Value":"x"}]}]}`
	})
	got, err := cl.Tagged(context.Background(), "eu-west-1", "aaaaaaaaaaaa")
	if err != nil || len(got) != 1 || got[0].LabID != "aaaaaaaaaaaa" || got[0].ARN != "arn:aws:s3:::crucible-lab-aaaaaaaaaaaa" {
		t.Fatalf("tagged: %+v %v", got, err)
	}
	all := calls()
	if all[0].form.Get("RoleSessionName") != "crucible-ops" || all[0].form.Get("RoleArn") != "arn:aws:iam::444455556666:role/crucible-lab-ops" {
		t.Fatalf("inventory runs as the ops role: %+v", all[0])
	}
	var in struct{ TagFilters []struct{ Key string; Values []string } }
	if err := json.Unmarshal([]byte(all[1].body), &in); err != nil || in.TagFilters[0].Key != "crucible:lab-id" || in.TagFilters[0].Values[0] != "aaaaaaaaaaaa" {
		t.Fatalf("tag filter: %s", all[1].body)
	}
}

func TestDeleteKnowsItsTypes(t *testing.T) {
	ec2XML := func(op, inner string) string {
		return `<` + op + `Response xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r</requestId>` + inner + `</` + op + `Response>`
	}
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		switch {
		case c.form.Get("Action") == "DescribeInstances":
			return 200, "text/xml", ec2XML("DescribeInstances", `<reservationSet><item><instancesSet><item><instanceId>i-1</instanceId><instanceState><code>16</code><name>running</name></instanceState></item></instancesSet></item></reservationSet>`)
		case c.form.Get("Action") == "TerminateInstances":
			return 200, "text/xml", ec2XML("TerminateInstances", `<instancesSet/>`)
		case c.form.Get("Action") == "DeleteVolume":
			return 400, "text/xml", `<Response><Errors><Error><Code>InvalidVolume.NotFound</Code><Message>gone</Message></Error></Errors><RequestID>r</RequestID></Response>`
		case c.method == "GET" && strings.Contains(c.query, "versions"):
			return 200, "application/xml", `<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>crucible-lab-aaaaaaaaaaaa</Name><IsTruncated>false</IsTruncated><Version><Key>forged.txt</Key><VersionId>v1</VersionId><IsLatest>true</IsLatest></Version></ListVersionsResult>`
		case c.method == "POST" && strings.Contains(c.query, "delete"):
			return 200, "application/xml", `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`
		case c.method == "DELETE":
			return 204, "application/xml", ""
		}
		return 400, "text/plain", "unexpected"
	})
	ctx, creds := context.Background(), Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:instance/i-1"); !ok || err != nil {
		t.Fatalf("running instance: %v %v", ok, err)
	}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:ec2:eu-west-1:1:volume/vol-1"); ok || err != nil {
		t.Fatalf("a volume that is already gone is (false, nil): %v %v", ok, err)
	}
	n := len(calls())
	if _, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:rds:eu-west-1:1:db:x"); !errors.Is(err, ErrUnsupported) || len(calls()) != n {
		t.Fatalf("unknown types make no call: %v", err)
	}
	if ok, err := cl.Delete(ctx, "eu-west-1", creds, "arn:aws:s3:::crucible-lab-aaaaaaaaaaaa"); !ok || err != nil {
		t.Fatalf("bucket: %v %v", ok, err)
	}
	var methods []string
	for _, c := range calls()[n:] {
		methods = append(methods, c.method)
	}
	if strings.Join(methods, " ") != "GET POST DELETE" {
		t.Fatalf("a bucket is emptied (every version) before it is deleted: %v", methods)
	}
}

func TestCostsGroupsByLabAndSkipsUntaggedSpend(t *testing.T) {
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		return 200, "application/x-amz-json-1.1", `{"ResultsByTime":[{"TimePeriod":{"Start":"2026-10-04","End":"2026-10-05"},"Groups":[
			{"Keys":["crucible:lab-id$aaaaaaaaaaaa"],"Metrics":{"UnblendedCost":{"Amount":"0.4213","Unit":"USD"}}},
			{"Keys":["crucible:lab-id$"],"Metrics":{"UnblendedCost":{"Amount":"12.5","Unit":"USD"}}}],"Estimated":true}]}`
	})
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, err := cl.Costs(context.Background(), from, from.AddDate(0, 0, 5))
	if err != nil || len(got) != 1 || got[0].LabID != "aaaaaaaaaaaa" || got[0].USD != 0.4213 || !got[0].Day.Equal(from.AddDate(0, 0, 3)) {
		t.Fatalf("costs: %+v %v", got, err)
	}
	last := calls()[len(calls())-1]
	for _, want := range []string{`"Granularity":"DAILY"`, `"Key":"crucible:lab-id"`, `"Type":"TAG"`, `"Start":"2026-10-01"`, `"End":"2026-10-06"`, `"UnblendedCost"`} {
		if last.target != "AWSInsightsIndexService.GetCostAndUsage" || !strings.Contains(last.body, want) {
			t.Fatalf("missing %s in %s %s", want, last.target, last.body)
		}
	}
}

func TestLabWritesKeepsUntaggedCreatesOnly(t *testing.T) {
	ev := func(id, user, name, raw string) map[string]any {
		return map[string]any{"EventId": id, "EventName": name, "EventTime": 1.7596e9, "Username": user, "CloudTrailEvent": raw,
			"Resources": []map[string]string{{"ResourceType": "AWS::EC2::Volume", "ResourceName": "vol-" + id}}}
	}
	body, _ := json.Marshal(map[string]any{"Events": []any{
		ev("1", "crucible-lab-aaaaaaaaaaaa", "CreateVolume", `{"requestParameters":{"size":1}}`),                                   // kept
		ev("2", "crucible-lab-aaaaaaaaaaaa", "RunInstances", `{"requestParameters":{"tagSpecificationSet":{"items":[{"tags":[{"key":"crucible:lab-id"}]}]}}}`),
		ev("3", "crucible-lab-aaaaaaaaaaaa", "CreateVolume", `{"errorCode":"Client.UnauthorizedOperation","requestParameters":{}}`),
		ev("4", "crucible-lab-aaaaaaaaaaaa", "CreateBucket", `{"requestParameters":{"bucketName":"crucible-lab-aaaaaaaaaaaa"}}`),
		ev("5", "crucible-lab-aaaaaaaaaaaa", "CreateTags", `{"requestParameters":{}}`),
		ev("6", "alice", "CreateVolume", `{"requestParameters":{}}`),
		ev("7", "crucible-lab-aaaaaaaaaaaa", "DeleteVolume", `{"requestParameters":{}}`),
	}})
	cl, calls := fakeAWS(t, func(c call) (int, string, string) { return 200, "application/x-amz-json-1.1", string(body) })
	got, err := cl.LabWrites(context.Background(), "eu-west-1", time.Now().Add(-24*time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != "1" || got[0].LabID != "aaaaaaaaaaaa" || got[0].Resources[0] != "vol-1" {
		t.Fatalf("only the successful untagged create by a lab session: %+v %v", got, err)
	}
	if last := calls()[len(calls())-1]; last.target != "CloudTrail_20131101.LookupEvents" || !strings.Contains(last.body, `"AttributeKey":"ReadOnly"`) {
		t.Fatalf("lookup: %+v", last)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/awscloud/ -run 'AssumeLab|Tagged|Delete|Costs|LabWrites' -count=1 2>&1 | tail -3`
Expected: FAIL (`undefined: newClient`).

- [ ] **Step 4: Implement**

Create `internal/awscloud/client.go`:

```go
package awscloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	tagging "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
)

type Config struct {
	LabRoleARN string // assumed per lab, with session tags (deploy/aws/labs output lab_role_arn)
	OpsRoleARN string // inventory, CloudTrail and Cost Explorer (output ops_role_arn)
	Endpoint   string // tests only: every service at this URL (S3 path-style)
}

// Client is the real lab account. Its own identity is the default credential chain: the node's instance role.
type Client struct {
	cfg  Config
	node aws.Config
	ops  aws.Config
}

var _ Cloud = (*Client)(nil)

func New(ctx context.Context, c Config) (*Client, error) {
	if c.LabRoleARN == "" || c.OpsRoleARN == "" {
		return nil, errors.New("aws labs need CRUCIBLE_AWS_LAB_ROLE_ARN and CRUCIBLE_AWS_OPS_ROLE_ARN")
	}
	node, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return newClient(node, c), nil
}

func newClient(node aws.Config, c Config) *Client {
	cl := &Client{cfg: c, node: node}
	cl.ops = node.Copy()
	cl.ops.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(cl.sts(), c.OpsRoleARN,
		func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = "crucible-ops" }))
	return cl
}

func (c *Client) endpoint() *string {
	if c.cfg.Endpoint == "" {
		return nil
	}
	return aws.String(c.cfg.Endpoint)
}

func (c *Client) sts() *sts.Client {
	return sts.NewFromConfig(c.node, func(o *sts.Options) { o.BaseEndpoint = c.endpoint() })
}

func (c *Client) AssumeLab(ctx context.Context, s Session) (Credentials, error) {
	if !ValidLabID(s.LabID) {
		return Credentials{}, fmt.Errorf("invalid lab id %q", s.LabID)
	}
	tags := []ststypes.Tag{{Key: aws.String(TagLab), Value: aws.String(s.LabID)}}
	if s.Team != "" {
		tags = append(tags, ststypes.Tag{Key: aws.String(TagTeam), Value: aws.String(s.Team)})
	}
	if s.Training != "" {
		tags = append(tags, ststypes.Tag{Key: aws.String(TagTraining), Value: aws.String(s.Training)})
	}
	out, err := c.sts().AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(c.cfg.LabRoleARN),
		RoleSessionName: aws.String(SessionName(s.LabID)), DurationSeconds: aws.Int32(3600), Tags: tags})
	if err != nil {
		return Credentials{}, err
	}
	k := out.Credentials
	return Credentials{AccessKeyID: aws.ToString(k.AccessKeyId), SecretAccessKey: aws.ToString(k.SecretAccessKey),
		SessionToken: aws.ToString(k.SessionToken), Expires: aws.ToTime(k.Expiration)}, nil
}

func (c *Client) Tagged(ctx context.Context, region, labID string) ([]Resource, error) {
	cl := tagging.NewFromConfig(c.ops, func(o *tagging.Options) { o.Region = region; o.BaseEndpoint = c.endpoint() })
	filter := tagtypes.TagFilter{Key: aws.String(TagLab)}
	if labID != "" {
		filter.Values = []string{labID}
	}
	var out []Resource
	p := tagging.NewGetResourcesPaginator(cl, &tagging.GetResourcesInput{TagFilters: []tagtypes.TagFilter{filter}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range page.ResourceTagMappingList {
			r := Resource{ARN: aws.ToString(m.ResourceARN)}
			for _, t := range m.Tags {
				if aws.ToString(t.Key) == TagLab {
					r.LabID = aws.ToString(t.Value)
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *Client) Delete(ctx context.Context, region string, cr Credentials, s string) (bool, error) {
	typ, id := kind(s)
	if typ == "" {
		return false, ErrUnsupported
	}
	cfg := c.node.Copy()
	cfg.Region = region
	cfg.Credentials = credentials.NewStaticCredentialsProvider(cr.AccessKeyID, cr.SecretAccessKey, cr.SessionToken)
	var err error
	if typ == "bucket" {
		err = deleteBucket(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint, o.UsePathStyle = c.endpoint(), c.cfg.Endpoint != ""
		}), id)
	} else {
		e := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = c.endpoint() })
		switch typ {
		case "instance":
			// terminated instances stay listed for about an hour: they are already gone, not deleted by us
			d, derr := e.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
			if derr == nil && len(d.Reservations) > 0 && len(d.Reservations[0].Instances) > 0 {
				if st := d.Reservations[0].Instances[0].State; st != nil && (st.Name == "terminated" || st.Name == "shutting-down") {
					return false, nil
				}
			}
			if err = derr; err == nil {
				_, err = e.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
			}
		case "volume":
			_, err = e.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(id)})
		case "security-group":
			_, err = e.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(id)})
		}
	}
	var api smithy.APIError
	if errors.As(err, &api) && (strings.HasSuffix(api.ErrorCode(), ".NotFound") || api.ErrorCode() == "NoSuchBucket") {
		return false, nil
	}
	return err == nil, err
}

// deleteBucket removes every object version and delete marker, then the bucket.
func deleteBucket(ctx context.Context, cl *s3.Client, bucket string) error {
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)}
	for {
		out, err := cl.ListObjectVersions(ctx, in)
		if err != nil {
			return err
		}
		var ids []s3types.ObjectIdentifier
		for _, v := range out.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(ids) > 0 {
			if _, err := cl.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)}}); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	_, err := cl.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	return err
}

func (c *Client) Costs(ctx context.Context, from, to time.Time) ([]DailyCost, error) {
	cl := costexplorer.NewFromConfig(c.ops, func(o *costexplorer.Options) { o.Region = "us-east-1"; o.BaseEndpoint = c.endpoint() })
	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(from.Format(time.DateOnly)), End: aws.String(to.Format(time.DateOnly))},
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []cetypes.GroupDefinition{{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(TagLab)}},
	}
	var out []DailyCost
	for {
		res, err := cl.GetCostAndUsage(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, r := range res.ResultsByTime {
			day, err := time.Parse(time.DateOnly, aws.ToString(r.TimePeriod.Start))
			if err != nil {
				return nil, err
			}
			for _, g := range r.Groups {
				if len(g.Keys) == 0 {
					continue
				}
				id := strings.TrimPrefix(g.Keys[0], TagLab+"$")
				if id == "" {
					continue // untagged spend: not a lab's
				}
				usd, err := strconv.ParseFloat(aws.ToString(g.Metrics["UnblendedCost"].Amount), 64)
				if err != nil {
					return nil, err
				}
				out = append(out, DailyCost{Day: day, LabID: id, USD: usd})
			}
		}
		if res.NextPageToken == nil {
			return out, nil
		}
		in.NextPageToken = res.NextPageToken
	}
}

func (c *Client) LabWrites(ctx context.Context, region string, since time.Time) ([]TrailEvent, error) {
	cl := cloudtrail.NewFromConfig(c.ops, func(o *cloudtrail.Options) { o.Region = region; o.BaseEndpoint = c.endpoint() })
	in := &cloudtrail.LookupEventsInput{StartTime: aws.Time(since), LookupAttributes: []cttypes.LookupAttribute{
		{AttributeKey: cttypes.LookupAttributeKeyReadOnly, AttributeValue: aws.String("false")}}}
	var out []TrailEvent
	// ponytail: at most 20 pages (1000 write events per region per run); LookupEvents allows 2 calls a second.
	for page := 0; page < 20; page++ {
		res, err := cl.LookupEvents(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, e := range res.Events {
			if ev, ok := untaggedCreate(e); ok {
				out = append(out, ev)
			}
		}
		if res.NextToken == nil {
			break
		}
		in.NextToken = res.NextToken
	}
	return out, nil
}

var createVerbs = []string{"Create", "Run", "Allocate", "Import", "Copy", "Register"}

// untaggedCreate keeps a successful create-like call by a lab session whose request carried no crucible:lab-id tag.
// IAM requires that tag on every create a lab role may make, so each hit is a policy gap worth a human look.
// CreateBucket (lab buckets are scoped by name, tagged right after) and CreateTags are not resource creates.
func untaggedCreate(e cttypes.Event) (TrailEvent, bool) {
	id, ok := strings.CutPrefix(aws.ToString(e.Username), "crucible-lab-")
	name := aws.ToString(e.EventName)
	if !ok || name == "CreateBucket" || name == "CreateTags" ||
		!slices.ContainsFunc(createVerbs, func(v string) bool { return strings.HasPrefix(name, v) }) {
		return TrailEvent{}, false
	}
	var raw struct {
		ErrorCode         string          `json:"errorCode"`
		RequestParameters json.RawMessage `json:"requestParameters"`
	}
	if json.Unmarshal([]byte(aws.ToString(e.CloudTrailEvent)), &raw) != nil || raw.ErrorCode != "" ||
		bytes.Contains(raw.RequestParameters, []byte(TagLab)) {
		return TrailEvent{}, false
	}
	ev := TrailEvent{ID: aws.ToString(e.EventId), At: aws.ToTime(e.EventTime), LabID: id, Event: name}
	for _, r := range e.Resources {
		ev.Resources = append(ev.Resources, aws.ToString(r.ResourceName))
	}
	return ev, true
}
```

If the SDK version in `go.mod` names a field differently (for example `RetryMaxAttempts`, or the tagging paginator constructor), follow the compiler. The behaviour the tests pin is what matters.

- [ ] **Step 5: Run the tests**

Run: `go mod tidy && go test -race ./internal/awscloud/ -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: all six tests PASS, `ok`. No request leaves the httptest server: every client has `BaseEndpoint` set, and the static node credentials are fake.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/awscloud
git commit -m "feat(awscloud): SDK client for lab credentials, tag inventory, deletes, Cost Explorer and CloudTrail

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Infracost estimates, the aws quote rules, and the lint price check

**Files:**
- Create: `internal/infracost/infracost.go`, `internal/infracost/infracost_test.go`
- Create: `internal/labs/aws_estimate.go`, `internal/labs/aws_estimate_test.go`
- Modify: `internal/labs/service.go` (Service fields `Cloud`, `AWSRegions`; the end of `quote`)
- Modify: `cmd/crucible/main.go` (`lint`)

**Interfaces:**
- Consumes: `content.LabTF`, `content.LabTFVars`, `content.LabTFFile`, `content.LabTFVarsFile` (Task 1); `awscloud.Cloud` (Task 3).
- Produces:
  - `infracost.Runner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)`;
  - `infracost.Exec` (the real CLI);
  - `infracost.Hourly(ctx, run Runner, moduleDir, region string) (float64, error)`;
  - `labs.InfracostEstimator{Run infracost.Runner}`;
  - `Service.Cloud awscloud.Cloud` and `Service.AWSRegions []string`;
  - test helper `(*fx).withForge401(t)`, used by Tasks 8–10.

- [ ] **Step 1: Write the failing tests**

Create `internal/infracost/infracost_test.go`:

```go
package infracost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHourlyPricesALocalCopy(t *testing.T) {
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "main.tf"), []byte(`resource "aws_instance" "x" {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var dir string
	run := func(_ context.Context, d string, env []string, args ...string) ([]byte, error) {
		dir = d
		tf, _ := os.ReadFile(filepath.Join(d, "crucible.tf"))
		vars, _ := os.ReadFile(filepath.Join(d, "crucible.auto.tfvars.json"))
		var v map[string]string
		_ = json.Unmarshal(vars, &v)
		if !strings.Contains(string(tf), "region = var.crucible_region") || v["crucible_region"] != "us-east-2" {
			t.Fatalf("infracost must see Crucible's provider in the lab's region: %s %s", tf, vars)
		}
		if _, err := os.Stat(filepath.Join(d, "main.tf")); err != nil || !slices.Contains(env, "INFRACOST_SKIP_UPDATE_CHECK=true") {
			t.Fatalf("module copied, quiet CLI: %v %v", err, env)
		}
		if !slices.Equal(args, []string{"breakdown", "--path", ".", "--format", "json", "--log-level", "warn"}) {
			t.Fatalf("args %v", args)
		}
		return []byte(`{"totalHourlyCost":"0.0104","totalMonthlyCost":"7.592"}`), nil
	}
	h, err := Hourly(context.Background(), run, module, "us-east-2")
	if err != nil || h != 0.0104 {
		t.Fatalf("hourly %v %v", h, err)
	}
	if dir == module {
		t.Fatal("infracost runs on a copy: the content export is shared and immutable")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the copy is removed afterwards")
	}
	usageOnly := func(context.Context, string, []string, ...string) ([]byte, error) { return []byte(`{"totalHourlyCost":null}`), nil }
	if h, err := Hourly(context.Background(), usageOnly, module, "us-east-2"); err != nil || h != 0 {
		t.Fatalf("usage-based only (e.g. one S3 bucket) is $0: %v %v", h, err)
	}
	garbage := func(context.Context, string, []string, ...string) ([]byte, error) { return []byte(`<html>`), nil }
	if _, err := Hourly(context.Background(), garbage, module, "us-east-2"); err == nil {
		t.Fatal("unreadable output is an error, never $0")
	}
}
```

Create `internal/labs/aws_estimate_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"crucible/internal/config"
	"crucible/internal/content"
)

// withForge401 enrols the trainee in Forge 401 (one aws lab, cloud-heat) and prices it with f.rates.
func (f *fx) withForge401(t *testing.T) {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := f.s.Learn.State()
	st.Trainings["forge-401@abc"] = tr
	st.ProgramSHAs["forge/forge-401"] = "abc"
	f.plat.Teams["forge"].Programs["forge-401"] = &config.Program{Training: "forge-401", Enrolled: []string{"trainee@crucible.local"}}
	f.s.Estimators["aws"] = f.rates
	f.s.AWSRegions = []string{"eu-west-1"}
}

func TestInfracostEstimatorCachesPerContentVersion(t *testing.T) {
	var calls atomic.Int32
	e := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"totalHourlyCost":"0.02"}`), nil
	}}
	tr, _ := content.Load("../../examples/forge-401")
	lab := tr.Module("01-cloud-heat").Lab
	for range 2 {
		if h, err := e.HourlyUSD(context.Background(), lab); err != nil || h != 0.02 {
			t.Fatalf("%v %v", h, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("one infracost run per content version, got %d", calls.Load())
	}
	failing := &InfracostEstimator{Run: func(context.Context, string, []string, ...string) ([]byte, error) { return nil, errors.New("no API key") }}
	if _, err := failing.HourlyUSD(context.Background(), lab); err == nil {
		t.Fatal("errors are returned")
	}
}

func TestAWSQuoteBlocksOverPricedAndForeignLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	f.s.Runners["aws"] = &fakeRunner{}
	f.rates["cloud-heat"] = 0.06 // its lab.yaml allows $0.05/h
	m, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || !strings.Contains(m.Blocked, "above its $0.05/h limit") {
		t.Fatalf("over the author's ceiling: %+v %v", m, err)
	}
	f.rates["cloud-heat"] = 0.01
	f.s.AWSRegions = []string{"us-east-1"}
	m, _ = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if !strings.Contains(m.Blocked, "runs in eu-west-1, which this server does not allow") {
		t.Fatalf("region outside the lab account's allowed regions: %+v", m)
	}
	f.s.AWSRegions = []string{"eu-west-1"}
	m, _ = f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if m.Blocked != "" || !m.NeedsApproval || m.EstimateUSD != 0.01 {
		t.Fatalf("a cheap aws lab still needs an approver (aws never auto-approves): %+v", m)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/infracost/ ./internal/labs/ -run 'Hourly|Infracost|AWSQuote' -count=1 2>&1 | tail -4`
Expected: FAIL (undefined `Hourly`, `InfracostEstimator`, `AWSRegions`).

- [ ] **Step 3: Implement infracost**

Create `internal/infracost/infracost.go`:

```go
// Package infracost prices an aws lab's terraform module with the infracost CLI (spec §9.1: "aws = infracost on the
// module × requested TTL"). Prices come from Infracost's pricing API, never from AWS. Lint keeps module sources
// local, so the CLI never downloads code.
package infracost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"crucible/internal/content"
)

// Runner runs the infracost CLI in dir and returns its stdout.
type Runner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)

// Exec is the real CLI (INFRACOST_API_KEY comes from the environment).
func Exec(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "infracost", args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return nil, fmt.Errorf("infracost: %w: %s", err, msg)
	}
	return out, nil
}

// Hourly prices the module in moduleDir as it would run in region, with Crucible's provider file added (on a temp
// copy: content exports are shared). Usage-based resources (S3 requests, data transfer) count as $0: the result is
// a floor, which authors bound with aws.max_hourly_usd.
func Hourly(ctx context.Context, run Runner, moduleDir, region string) (float64, error) {
	tmp, err := os.MkdirTemp("", "crucible-infracost-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, os.DirFS(moduleDir)); err != nil { // refuses symlinks
		return 0, err
	}
	files := map[string][]byte{content.LabTFFile: []byte(content.LabTF),
		content.LabTFVarsFile: content.LabTFVars("000000000000", "estimate", "estimate", region)}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), b, 0o644); err != nil {
			return 0, err
		}
	}
	out, err := run(ctx, tmp, []string{"INFRACOST_SKIP_UPDATE_CHECK=true", "INFRACOST_NO_COLOR=true"},
		"breakdown", "--path", ".", "--format", "json", "--log-level", "warn")
	if err != nil {
		return 0, err
	}
	var r struct {
		TotalHourlyCost *string `json:"totalHourlyCost"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return 0, fmt.Errorf("reading infracost output: %w", err)
	}
	if r.TotalHourlyCost == nil {
		return 0, nil
	}
	return strconv.ParseFloat(*r.TotalHourlyCost, 64)
}
```

- [ ] **Step 4: The estimator and the quote rules**

Create `internal/labs/aws_estimate.go`:

```go
package labs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"crucible/internal/content"
	"crucible/internal/infracost"
)

// InfracostEstimator prices aws labs (spec §9.1). Results are cached per content version: lab.Dir is an immutable
// export, so the CLI runs once per lab per pushed commit (the first lobby view after a sync waits for it).
// ponytail: the cache never shrinks; it holds one float per lab version.
type InfracostEstimator struct {
	Run infracost.Runner

	mu    sync.Mutex
	cache map[string]float64
}

func (e *InfracostEstimator) HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error) {
	if lab.AWS == nil {
		return 0, errors.New("not an aws lab")
	}
	e.mu.Lock()
	h, ok := e.cache[lab.Dir]
	e.mu.Unlock()
	if ok {
		return h, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	h, err := infracost.Hourly(ctx, e.Run, filepath.Join(lab.Dir, "terraform"), lab.AWS.Region)
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	if e.cache == nil {
		e.cache = map[string]float64{}
	}
	e.cache[lab.Dir] = h
	e.mu.Unlock()
	return h, nil
}
```

In `internal/labs/service.go`, add to `Service` (with `"crucible/internal/awscloud"` imported):

```go
	Cloud      awscloud.Cloud // the shared AWS lab account; nil when aws labs are off
	AWSRegions []string       // regions aws labs may run in (the lab account's allowed_regions)
```

At the end of `quote`, just before `return q, nil`:

```go
	if lab.Runtime == "aws" && lab.AWS != nil && q.Blocked == "" {
		switch {
		case hourly > lab.AWS.MaxHourlyUSD:
			q.Blocked = fmt.Sprintf("This lab is priced at $%.2f/h, above its $%.2f/h limit; its maintainers need to make it cheaper.",
				hourly, lab.AWS.MaxHourlyUSD)
		case !slices.Contains(s.AWSRegions, lab.AWS.Region):
			q.Blocked = fmt.Sprintf("This lab runs in %s, which this server does not allow.", lab.AWS.Region)
		}
	}
```

- [ ] **Step 5: Lint checks the price when infracost is configured**

In `cmd/crucible/main.go` `lint`, after `probs = append(probs, shellcheck(dir)...)`, add `probs = append(probs, priceCheck(t, w)...)`. Then add:

```go
// priceCheck fails aws labs whose infracost estimate exceeds aws.max_hourly_usd (spec §4.5). It needs the infracost
// CLI and INFRACOST_API_KEY; without them it says so and checks nothing.
func priceCheck(t *content.Training, w io.Writer) []content.Problem {
	if t == nil {
		return nil
	}
	var probs []content.Problem
	for _, m := range t.Modules {
		if m.Lab == nil || m.Lab.Runtime != "aws" || m.Lab.AWS == nil {
			continue
		}
		if _, err := exec.LookPath("infracost"); err != nil || os.Getenv("INFRACOST_API_KEY") == "" {
			fmt.Fprintf(w, "note: %s: price check skipped (needs the infracost CLI and INFRACOST_API_KEY)\n", m.Lab.ID)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		h, err := infracost.Hourly(ctx, infracost.Exec, filepath.Join(m.Lab.Dir, "terraform"), m.Lab.AWS.Region)
		cancel()
		switch {
		case err != nil:
			probs = append(probs, content.Problem{File: m.Lab.ID, Msg: "infracost: " + err.Error()})
		case h > m.Lab.AWS.MaxHourlyUSD:
			probs = append(probs, content.Problem{File: m.Lab.ID,
				Msg: fmt.Sprintf("infracost prices this lab at $%.4f/h, above aws.max_hourly_usd $%.2f", h, m.Lab.AWS.MaxHourlyUSD)})
		}
	}
	return probs
}
```

(Imports: `"crucible/internal/infracost"`, plus `"time"` and `"context"` if they are missing. `content.Training.Modules` is the loaded module list. If its element type differs, iterate the way `content.Load`'s callers already do.)

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/infracost/ ./internal/labs/ ./cmd/crucible/ -count=1 2>&1 | tail -4 && go run ./cmd/crucible lint examples/forge-401`
Expected: `ok` ×3. Lint prints `note: cloud-heat: price check skipped (needs the infracost CLI and INFRACOST_API_KEY)` and then `forge-401: 1 module(s) OK. Ready for the forge.`

- [ ] **Step 7: Commit**

```bash
git add internal/infracost internal/labs/aws_estimate.go internal/labs/aws_estimate_test.go internal/labs/service.go cmd/crucible/main.go
git commit -m "feat(finops): infracost estimates for aws labs; quotes respect the price ceiling and allowed regions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: Cluster pieces for aws labs: workspace mount and quota, terraform runner pods, waiting for them

**Files:**
- Modify: `internal/labs/cluster.go` (`Provision` → `provision(…, awsSetup)`, extract `stuck`, add `waitDone`, `deletePod`)
- Create: `internal/labs/aws_objects.go`
- Create: `internal/labs/aws_objects_test.go`

**Interfaces:**
- Consumes: `clusterObjects`, `labObjects`, `labNamespace`, `labLabel`, `testRunner`, `onPodCreate`, `podReady`, `fakeExec` (M4).
- Produces (Task 7):
  - `awsSecret = "aws-creds"`, `tfConfigMap = "tf-module"`, `tfApplyPod = "tf-apply"`, `tfDestroyPod = "tf-destroy"`;
  - `awsWorkspace(o labObjects) labObjects`;
  - `tfPod(id, name, image, script string) *corev1.Pod`;
  - `tfScript(action string, dryRun bool) string` (`action` is `"apply"` or `"destroy"`);
  - `(*ClusterRunner).provision(ctx, inst, bundle []byte, compose string, awsSetup func(ns string) error) error`;
  - `(*ClusterRunner).waitDone(ctx, ns, name string) (ok bool, msg string, err error)`;
  - `(*ClusterRunner).deletePod(ctx, ns, name string) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/aws_objects_test.go`:

```go
package labs

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAWSWorkspaceObjects(t *testing.T) {
	o := awsWorkspace(clusterObjects(testID, "crucible-workspace.yaml", false))
	c := o.Pod.Spec.Containers[0]
	m, v := c.VolumeMounts[len(c.VolumeMounts)-1], o.Pod.Spec.Volumes[len(o.Pod.Spec.Volumes)-1]
	if m.MountPath != "/aws" || !m.ReadOnly || v.Secret == nil || v.Secret.SecretName != awsSecret {
		t.Fatalf("the workspace mounts the credentials read-only at /aws: %+v %+v", m, v)
	}
	h := o.Quota.Spec.Hard
	if h.Pods().Value() != 2 {
		t.Fatalf("room for the workspace and one terraform pod: %v", h.Pods())
	}
	for k, want := range map[corev1.ResourceName]string{corev1.ResourceLimitsCPU: "3", corev1.ResourceLimitsMemory: "5Gi",
		corev1.ResourceLimitsEphemeralStorage: "24Gi", corev1.ResourceRequestsCPU: "750m"} {
		if got := h[k]; got.Cmp(resource.MustParse(want)) != 0 {
			t.Fatalf("%s = %s, want %s", k, got.String(), want)
		}
	}
	if base := clusterObjects(testID, "compose.yaml", false); base.Quota.Spec.Hard.Pods().Value() != 1 {
		t.Fatal("cluster labs keep their one-pod quota")
	}
}

func TestTFPodIsLockedDown(t *testing.T) {
	p := tfPod(testID, tfApplyPod, DefaultTerraformImage, tfScript("apply", false))
	s, c := p.Spec, p.Spec.Containers[0]
	switch {
	case p.Namespace != labNamespace(testID) || p.Labels[labLabel] != testID:
		t.Fatalf("lives in the lab namespace: %+v", p.ObjectMeta)
	case s.RestartPolicy != corev1.RestartPolicyNever || *s.AutomountServiceAccountToken || *s.EnableServiceLinks:
		t.Fatal("one attempt, no Kubernetes token, no service env")
	case !*s.SecurityContext.RunAsNonRoot || *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem ||
		c.SecurityContext.Privileged != nil || !slices.Equal(c.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}):
		t.Fatal("non-root, read-only root, no privilege, no capabilities")
	case c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError:
		t.Fatal("the log tail must come back through `pods get`")
	case len(c.EnvFrom) != 0 || slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.ValueFrom != nil }):
		t.Fatal("credentials come only from the mounted file, never env")
	}
	var mounts []string
	for _, v := range s.Volumes {
		switch {
		case v.Secret != nil:
			mounts = append(mounts, "secret:"+v.Secret.SecretName)
		case v.ConfigMap != nil:
			mounts = append(mounts, "configmap:"+v.ConfigMap.Name)
		}
	}
	if !slices.Equal(mounts, []string{"secret:" + awsSecret, "configmap:" + tfConfigMap}) {
		t.Fatalf("volumes %v", mounts)
	}
	if lim := c.Resources.Limits[corev1.ResourceEphemeralStorage]; lim.IsZero() {
		t.Fatal("the lab quota tracks ephemeral storage: every pod needs a limit")
	}
}

func TestTFScripts(t *testing.T) {
	apply, destroy, dry := tfScript("apply", false), tfScript("destroy", false), tfScript("destroy", true)
	if !strings.Contains(apply, "terraform init -input=false -backend-config=/module/backend.hcl") ||
		!strings.Contains(apply, "terraform apply -input=false -auto-approve") || strings.Contains(apply, "-lock=false") {
		t.Fatalf("apply:\n%s", apply)
	}
	if !strings.Contains(destroy, "terraform destroy -input=false -auto-approve -lock=false") {
		t.Fatalf("destroy runs after the apply pod is gone, so a stale lock must not block it:\n%s", destroy)
	}
	if strings.Contains(dry, "terraform init") || strings.Contains(dry, "terraform destroy") || !strings.Contains(dry, "terraform version") {
		t.Fatalf("a dry run touches no backend and no cloud:\n%s", dry)
	}
}

func TestProvisionCreatesAWSObjectsBeforeThePod(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	onPodCreate(cs, podReady)
	r := testRunner(cs, &fakeExec{})
	setup := func(ns string) error {
		_, err := cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: awsSecret, Namespace: ns}}, metav1.CreateOptions{})
		return err
	}
	if err := r.provision(ctx, &Instance{ID: testID}, []byte("tgz"), "crucible-workspace.yaml", setup); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" {
			order = append(order, a.GetResource().Resource)
		}
	}
	if !slices.Equal(order, []string{"namespaces", "resourcequotas", "limitranges", "networkpolicies", "secrets", "pods"}) {
		t.Fatalf("the secret the pod mounts exists first, behind the policies: %v", order)
	}
	pod, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, labPod, metav1.GetOptions{})
	if !slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil }) {
		t.Fatal("aws labs get the workspace mount")
	}
}

func TestWaitDoneAndDeletePod(t *testing.T) {
	ctx, ns := context.Background(), labNamespace(testID)
	pod := func(name string, st corev1.PodStatus) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Status: st}
	}
	term := func(phase corev1.PodPhase, msg string) corev1.PodStatus {
		return corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Message: msg}}}}}
	}
	cs := fake.NewClientset(
		pod("ok", term(corev1.PodSucceeded, "Apply complete!")),
		pod("bad", term(corev1.PodFailed, "Error: AccessDenied")),
		pod("img", corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "no such image"}}}}}))
	r := testRunner(cs, &fakeExec{})
	for name, want := range map[string]struct {
		ok  bool
		msg string
	}{"ok": {true, "Apply complete!"}, "bad": {false, "Error: AccessDenied"}, "img": {false, "ImagePullBackOff: no such image"}} {
		ok, msg, err := r.waitDone(ctx, ns, name)
		if err != nil || ok != want.ok || msg != want.msg {
			t.Fatalf("%s: %v %q %v", name, ok, msg, err)
		}
	}
	if err := r.deletePod(ctx, ns, "bad"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods(ns).Get(ctx, "bad", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("deletePod returns once the pod is gone")
	}
	if err := r.deletePod(ctx, ns, "never-existed"); err != nil {
		t.Fatalf("a missing pod is already deleted: %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'AWSWorkspace|TFPod|TFScripts|AWSObjectsBefore|WaitDone' -count=1 2>&1 | tail -3`
Expected: FAIL to compile (`undefined: awsWorkspace`).

- [ ] **Step 3: Implement the objects**

Create `internal/labs/aws_objects.go`:

```go
package labs

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	awsSecret    = "aws-creds" // the lab's STS credentials file; mounted at /aws in the workspace and runner pods
	tfConfigMap  = "tf-module" // module.tgz (terraform/ + crucible.tf + tfvars) and backend.hcl
	tfApplyPod   = "tf-apply"
	tfDestroyPod = "tf-destroy"
)

var (
	runnerRequests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")}
	// the aws provider plugin alone unpacks to ~0.6 GiB
	runnerLimits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("3Gi")}
)

// awsWorkspace turns a cluster lab into an aws lab's workspace (spec §8.2): the dind pod mounts the credentials
// secret at /aws (the workspace service bind-mounts it), and the quota leaves room for one terraform runner pod.
func awsWorkspace(o labObjects) labObjects {
	c := &o.Pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "aws", MountPath: "/aws", ReadOnly: true})
	o.Pod.Spec.Volumes = append(o.Pod.Spec.Volumes, corev1.Volume{Name: "aws",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: awsSecret}}})
	h := o.Quota.Spec.Hard
	h[corev1.ResourcePods] = resource.MustParse("2")
	add := func(k corev1.ResourceName, v resource.Quantity) {
		q := h[k].DeepCopy()
		q.Add(v)
		h[k] = q
	}
	add(corev1.ResourceRequestsCPU, runnerRequests[corev1.ResourceCPU])
	add(corev1.ResourceRequestsMemory, runnerRequests[corev1.ResourceMemory])
	add(corev1.ResourceLimitsCPU, runnerLimits[corev1.ResourceCPU])
	add(corev1.ResourceLimitsMemory, runnerLimits[corev1.ResourceMemory])
	add(corev1.ResourceLimitsEphemeralStorage, runnerLimits[corev1.ResourceEphemeralStorage])
	return o
}

// tfPod is one terraform run (apply or destroy) in the lab namespace, under the lab's NetworkPolicy (IMDS and
// private ranges blocked, AWS endpoints reachable). It is a bare pod rather than a batch/v1 Job: one attempt is
// what we want, a fixed name lets a retry find it, and FallbackToLogsOnError puts the log tail in the pod status,
// which `pods get` (already granted) reads. Credentials come only from the mounted file.
func tfPod(id, name, image, script string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: labNamespace(id), Labels: map[string]string{labLabel: id}},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  ptr.To(false),
			EnableServiceLinks:            ptr.To(false),
			TerminationGracePeriodSeconds: ptr.To(int64(120)), // terraform stops cleanly on SIGTERM
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)),
				RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name:       "terraform",
				Image:      image,
				Command:    []string{"/bin/sh", "-c", script},
				WorkingDir: "/w",
				Env: []corev1.EnvVar{
					{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/aws/credentials"},
					{Name: "HOME", Value: "/tmp"},
					{Name: "TF_IN_AUTOMATION", Value: "1"},
					{Name: "TF_INPUT", Value: "0"},
				},
				Resources:                corev1.ResourceRequirements{Requests: runnerRequests, Limits: runnerLimits},
				TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "aws", MountPath: "/aws", ReadOnly: true},
					{Name: "module", MountPath: "/module", ReadOnly: true},
					{Name: "work", MountPath: "/w"},
					{Name: "tmp", MountPath: "/tmp"},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "aws", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: awsSecret}}},
				{Name: "module", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: tfConfigMap}}}},
				{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

// tfScript is the runner pod's shell script. Destroy runs only after the apply pod is gone, so -lock=false cannot
// race an apply, and a lock left by a killed apply cannot wedge the destroy. A dry run (CRUCIBLE_AWS_LABS=dryrun)
// proves the pod, its mounts and the module unpack, and touches no backend and no cloud.
func tfScript(action string, dryRun bool) string {
	s := "set -eu\ntar xzf /module/module.tgz -C /w\n"
	if dryRun {
		return s + "terraform version\nls /w\necho 'dry run (CRUCIBLE_AWS_LABS=dryrun): nothing was applied or destroyed'\n"
	}
	s += "terraform init -input=false -backend-config=/module/backend.hcl\n"
	if action == "destroy" {
		return s + "terraform destroy -input=false -auto-approve -lock=false\n"
	}
	return s + "terraform apply -input=false -auto-approve\n"
}
```

- [ ] **Step 4: The runner helpers in `cluster.go`**

Rename the body of `Provision` to `provision` and keep `Provision` as a wrapper:

```go
func (c *ClusterRunner) Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error {
	return c.provision(ctx, inst, bundle, compose, nil)
}

// provision creates the lab namespace, its policies and the dind pod, unpacks the bundle and starts compose.
// awsSetup (aws labs only) runs once the namespace and its policies exist and before the pod: it creates the
// credentials secret the pod mounts and the module config map the terraform pods mount. It must be idempotent.
func (c *ClusterRunner) provision(ctx context.Context, inst *Instance, bundle []byte, compose string, awsSetup func(ns string) error) error {
	if !validLabID(inst.ID) || !filepath.IsLocal(compose) {
		return errors.New("invalid lab id or compose file name")
	}
	o := clusterObjects(inst.ID, compose, c.Privileged)
	if awsSetup != nil {
		o = awsWorkspace(o)
	}
	core, ns := c.Client.CoreV1(), o.Namespace.Name
	create := []func() error{ /* the four existing closures: namespace, quota, limits, network policy */ }
	if awsSetup != nil {
		create = append(create, func() error { return awsSetup(ns) })
	}
	create = append(create, func() error { _, err := core.Pods(ns).Create(ctx, o.Pod, metav1.CreateOptions{}); return err }) // last: isolated from its first packet
	// … the rest of the existing body (create loop, waitReady, untar, compose up) unchanged …
}
```

Extract the bad-image check from `waitReady` so `waitDone` shares it. In `waitReady`, replace the `for _, cs := range p.Status.ContainerStatuses { … }` block with:

```go
		if reason, msg := stuck(p); reason != "" {
			return fmt.Errorf("the lab image could not be started (%s): %s", reason, msg)
		}
```

and add:

```go
// stuck names a container state that will not heal on its own (bad image or config), or "".
func stuck(p *corev1.Pod) (reason, msg string) {
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImageNeverPull", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
				return w.Reason, w.Message
			}
		}
	}
	return "", ""
}

func (c *ClusterRunner) poll() time.Duration {
	if c.Poll == 0 {
		return 2 * time.Second
	}
	return c.Poll
}

// waitDone polls a one-shot pod until it has finished. ok reports success; msg is the container's termination
// message (FallbackToLogsOnError: the log tail on failure). A bad image fails fast, as in waitReady.
func (c *ClusterRunner) waitDone(ctx context.Context, ns, name string) (ok bool, msg string, err error) {
	for {
		p, err := c.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		if reason, m := stuck(p); reason != "" {
			return false, reason + ": " + m, nil
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			for _, cs := range p.Status.ContainerStatuses {
				if t := cs.State.Terminated; t != nil {
					msg = t.Message
				}
			}
			return p.Status.Phase == corev1.PodSucceeded, msg, nil
		}
		select {
		case <-ctx.Done():
			return false, "", ctx.Err()
		case <-time.After(c.poll()):
		}
	}
}

// deletePod deletes a pod and returns once it is gone (terraform gets its grace period to stop cleanly).
func (c *ClusterRunner) deletePod(ctx context.Context, ns, name string) error {
	pods := c.Client.CoreV1().Pods(ns)
	if err := pods.Delete(ctx, name, metav1.DeleteOptions{}); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	for {
		if _, err := pods.Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.poll()):
		}
	}
}
```

Use `c.poll()` in `waitReady` as well, replacing its local `poll` variable.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/labs/ -count=1 2>&1 | tail -3 && go vet -tags cluster ./internal/labs/`
Expected: `ok`. M4's `TestProvisionCreatesIsolatedLab` and `TestProvisionFailsFast` still pass unchanged: the cluster path creates the same objects in the same order, and `stuck` keeps the same error text.

- [ ] **Step 6: Commit**

```bash
git add internal/labs/cluster.go internal/labs/aws_objects.go internal/labs/aws_objects_test.go
git commit -m "feat(labs): aws workspace mount and quota, locked-down terraform runner pods, wait and delete helpers

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: `AWSRunner`: provision, destroy, credential refresh, dry run

**Files:**
- Modify: `internal/labs/bundle.go` (`tarGz`, `BundleWith`, `ModuleBundle`)
- Create: `internal/labs/aws.go`
- Create: `internal/labs/aws_test.go`

**Interfaces:**
- Consumes:
  - Tasks 1, 3 and 6;
  - `(*ClusterRunner).OpenPTY` and `RunScript` (M4);
  - `labNS(id, deleting)` (M4 test helper).
- Produces (Task 8, main):
  - `type AWSRunner struct{ Cluster *ClusterRunner; Cloud awscloud.Cloud; StateBucket, StateRegion, WorkspaceImage, TerraformImage string; DryRun *awscloud.Fake; Now func() time.Time }`;
  - `DefaultWorkspaceImage = "amazon/aws-cli:2.27.0"`, `DefaultTerraformImage = "hashicorp/terraform:1.16.5"`;
  - `(*AWSRunner).ProvisionLab(ctx, inst, lab *content.Lab) error`;
  - `(*AWSRunner).Destroy(ctx, inst) error`;
  - `(*AWSRunner).Refresh(ctx, inst) error`;
  - `ModuleBundle(lab, inst) ([]byte, error)` and `BundleWith(labDir string, extra map[string][]byte) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/aws_test.go`:

```go
package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

func testAWS(cs *fake.Clientset) (*AWSRunner, *awscloud.Fake, *fakeExec) {
	fe, cloud := &fakeExec{}, &awscloud.Fake{}
	return &AWSRunner{Cluster: testRunner(cs, fe), Cloud: cloud, StateBucket: "crucible-444455556666-labstate",
		StateRegion: "eu-west-1", WorkspaceImage: DefaultWorkspaceImage, TerraformImage: DefaultTerraformImage}, cloud, fe
}

// kubelet decides how pods end: the lab pod becomes ready; terraform pods finish with phase and message.
func kubelet(cs *fake.Clientset, phase corev1.PodPhase, msg string) {
	onPodCreate(cs, func(p *corev1.Pod) {
		if p.Name == labPod {
			podReady(p)
			return
		}
		p.Status.Phase = phase
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Message: msg}}}}
	})
}

func cloudHeat(t *testing.T) *content.Lab {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return tr.Module("01-cloud-heat").Lab
}

func untar(t *testing.T, tgz []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	out, tr := map[string]string{}, tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func steps(cs *fake.Clientset, verbs ...string) []string {
	var out []string
	for _, a := range cs.Actions() {
		if slices.Contains(verbs, a.GetVerb()) {
			out = append(out, a.GetVerb()+" "+a.GetResource().Resource)
		}
	}
	return out
}

func TestAWSProvisionBuildsTheWorkspaceThenApplies(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "Apply complete!")
	a, cloud, fe := testAWS(cs)
	inst := &Instance{ID: testID, Team: "forge", Training: "forge-401"}
	if err := a.ProvisionLab(ctx, inst, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"create namespaces", "create resourcequotas", "create limitranges", "create networkpolicies",
		"create secrets", "create configmaps", "create pods", "create pods"}
	if got := steps(cs, "create"); !slices.Equal(got, want) {
		t.Fatalf("policies, then credentials and module, then the workspace, then terraform: %v", got)
	}
	if s := cloud.Assumed(); len(s) != 1 || s[0] != (awscloud.Session{LabID: testID, Team: "forge", Training: "forge-401"}) {
		t.Fatalf("one tagged session: %+v", s)
	}
	ns := labNamespace(testID)
	sec, _ := cs.CoreV1().Secrets(ns).Get(ctx, awsSecret, metav1.GetOptions{})
	if !strings.Contains(string(sec.Data["credentials"]), "aws_session_token = fake-token-"+testID) {
		t.Fatalf("credentials file: %s", sec.Data["credentials"])
	}
	cm, _ := cs.CoreV1().ConfigMaps(ns).Get(ctx, tfConfigMap, metav1.GetOptions{})
	if !strings.Contains(cm.Data["backend.hcl"], `"labs/`+testID+`.tfstate"`) || !strings.Contains(cm.Data["backend.hcl"], "use_lockfile = true") {
		t.Fatalf("state per lab: %s", cm.Data["backend.hcl"])
	}
	mod := untar(t, cm.BinaryData["module.tgz"])
	if !strings.Contains(mod["main.tf"], "aws_s3_bucket") || mod[content.LabTFFile] != content.LabTF ||
		!strings.Contains(mod[content.LabTFVarsFile], `"crucible_lab_id":"`+testID+`"`) {
		t.Fatalf("module: %v", mod)
	}
	ws := untar(t, fe.calls[0].stdin) // the first exec unpacks the workspace bundle
	compose := ws[workspaceCompose]
	if !strings.Contains(compose, `image: "amazon/aws-cli:2.27.0"`) || !strings.Contains(compose, `AWS_REGION: "eu-west-1"`) ||
		!strings.Contains(compose, `"/aws:/aws:ro"`) {
		t.Fatalf("workspace compose:\n%s", compose)
	}
	for name := range ws {
		if strings.HasPrefix(name, "terraform/") || strings.HasPrefix(name, "checks/") || name == "lab.yaml" {
			t.Fatalf("%s must never reach the workspace", name)
		}
	}
	apply, _ := cs.CoreV1().Pods(ns).Get(ctx, tfApplyPod, metav1.GetOptions{})
	if !strings.Contains(apply.Spec.Containers[0].Command[2], "terraform apply") {
		t.Fatal("the apply pod runs terraform apply")
	}
}

func TestAWSProvisionReportsTerraformFailure(t *testing.T) {
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodFailed, "Error: creating S3 Bucket: AccessDenied")
	a, _, _ := testAWS(cs)
	err := a.ProvisionLab(context.Background(), &Instance{ID: testID}, cloudHeat(t))
	if err == nil || !strings.Contains(err.Error(), "terraform apply failed: Error: creating S3 Bucket: AccessDenied") {
		t.Fatalf("the trainee sees the log tail: %v", err)
	}
	if strings.Contains(err.Error(), "fake-token") || strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("never credentials")
	}
}

func TestAWSDestroyStopsTheApplyFirst(t *testing.T) {
	ctx := context.Background()
	running := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tfApplyPod, Namespace: labNamespace(testID)},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	cs := fake.NewClientset(labNS(testID, false), running)
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, _, _ := testAWS(cs)
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	want := []string{"delete pods", "create secrets", "create pods", "delete namespaces"}
	if got := steps(cs, "create", "update", "delete"); !slices.Equal(got, want) {
		t.Fatalf("stop apply, fresh credentials, terraform destroy, then the namespace: %v", got)
	}
	d, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfDestroyPod, metav1.GetOptions{})
	if !strings.Contains(d.Spec.Containers[0].Command[2], "-lock=false") {
		t.Fatal("destroy pod")
	}
}

func TestAWSDestroyRetriesAFailedDestroyPodOnce(t *testing.T) {
	ctx := context.Background()
	failed := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tfDestroyPod, Namespace: labNamespace(testID)},
		Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	cs := fake.NewClientset(labNS(testID, false), failed)
	kubelet(cs, corev1.PodSucceeded, "Destroy complete!")
	a, _, _ := testAWS(cs)
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatalf("an earlier failed destroy is run again: %v", err)
	}
	if got := steps(cs, "create"); !slices.Equal(got, []string{"create secrets", "create pods", "create pods"}) {
		t.Fatalf("create (exists), delete the failed one, create again: %v", got)
	}
}

func TestAWSDestroyWithoutNamespace(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, labNamespace(testID))
	})
	a, _, _ := testAWS(cs)
	if err := a.Destroy(context.Background(), &Instance{ID: testID}); err != nil {
		t.Fatalf("no namespace: nothing to run terraform in; the tag sweep cleans up: %v", err)
	}
	if got := steps(cs, "create"); slices.Contains(got, "create pods") {
		t.Fatalf("no terraform pod without its namespace: %v", got)
	}
}

func TestAWSRefreshOnlyNearExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(labNS(testID, false))
	a, cloud, _ := testAWS(cs)
	cloud.Now, a.Now = func() time.Time { return now }, func() time.Time { return now }
	inst := &Instance{ID: testID}
	refreshes := func() int { return len(cloud.Assumed()) }
	if err := a.Refresh(ctx, inst); err != nil || refreshes() != 1 {
		t.Fatalf("nothing remembered (fresh process): refresh at once: %v", err)
	}
	now = now.Add(40 * time.Minute) // 20 minutes left
	if _ = a.Refresh(ctx, inst); refreshes() != 1 {
		t.Fatal("no STS call while more than 15 minutes are left")
	}
	now = now.Add(6 * time.Minute) // 14 minutes left
	if err := a.Refresh(ctx, inst); err != nil || refreshes() != 2 || !slices.Contains(steps(cs, "update"), "update secrets") {
		t.Fatalf("refreshed in place: %v %v", err, steps(cs, "create", "update"))
	}
}

func TestAWSDryRunSimulatesTheCloud(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "dry run")
	a, cloud, _ := testAWS(cs)
	a.DryRun = cloud
	if err := a.ProvisionLab(ctx, &Instance{ID: testID}, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	bucket, vol := "arn:aws:s3:::crucible-lab-"+testID, "arn:aws:ec2:eu-west-1:000000000000:volume/vol-"+testID
	apply, _ := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, tfApplyPod, metav1.GetOptions{})
	if !cloud.Has(bucket) || !cloud.Has(vol) || strings.Contains(apply.Spec.Containers[0].Command[2], "terraform apply") {
		t.Fatal("dry run: the fake cloud gets the resources; the pod applies nothing")
	}
	if err := a.Destroy(ctx, &Instance{ID: testID}); err != nil {
		t.Fatal(err)
	}
	if cloud.Has(bucket) || !cloud.Has(vol) {
		t.Fatal("terraform destroy removes its bucket; the hand-made volume is left for the tag sweep")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'TestAWS' -count=1 2>&1 | tail -3`
Expected: FAIL to compile (`undefined: AWSRunner`).

- [ ] **Step 3: Bundles**

In `internal/labs/bundle.go`, turn `Bundle`'s body into `tarGz` and add the two helpers (imports: `"slices"`, `"maps"`, `"time"`, `"crucible/internal/content"`):

```go
func tooBig(limit int) error {
	return fmt.Errorf("lab bundle exceeds %d KiB compressed; keep large files out of the lab directory (pull images instead)", limit>>10)
}

// maxModuleBundle keeps an aws lab's module under the 1 MiB ConfigMap limit (binaryData is base64 in etcd).
const maxModuleBundle = 700 << 10

func Bundle(labDir string) ([]byte, error) { return BundleWith(labDir, nil) }

// BundleWith is Bundle plus generated files at its root (an aws lab's workspace compose file).
func BundleWith(labDir string, extra map[string][]byte) ([]byte, error) {
	return tarGz(labDir, bundleSkip, extra, maxBundleBytes)
}

// ModuleBundle is an aws lab's terraform/ directory plus Crucible's provider file and this lab's variables.
func ModuleBundle(lab *content.Lab, inst *Instance) ([]byte, error) {
	return tarGz(filepath.Join(lab.Dir, "terraform"), nil, map[string][]byte{
		content.LabTFFile:     []byte(content.LabTF),
		content.LabTFVarsFile: content.LabTFVars(inst.ID, inst.Team, inst.Training, lab.AWS.Region),
	}, maxModuleBundle)
}

// tarGz packs dir (minus top-level names in skip, minus symlinks) and then the extra files, gzipped, up to limit
// bytes. An extra file replaces a file of the same name in dir.
func tarGz(dir string, skip map[string]bool, extra map[string][]byte, limit int) ([]byte, error) {
	// … the old Bundle body, with these changes:
	//   - `bundleSkip[top]` → `skip[top]`; `maxBundleBytes` → `limit`; `tooBig()` → `tooBig(limit)`
	//   - right after computing rel: `if _, ok := extra[filepath.ToSlash(rel)]; ok { return nil }`
	// then, after the walk and before tw.Close():
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		b := extra[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	// … tw.Close, gz.Close, the final size check against limit, return buf.Bytes() …
}
```

If an existing test asserts the old `tooBig` text ("MiB"), keep its wording: format `limit>>20` MiB when `limit >= 1<<20`, otherwise KiB.

- [ ] **Step 4: The runner**

Create `internal/labs/aws.go`:

```go
package labs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

const (
	DefaultWorkspaceImage = "amazon/aws-cli:2.27.0"      // bump deliberately: dind pulls it once per lab
	DefaultTerraformImage = "hashicorp/terraform:1.16.5" // the node pulls it once
	workspaceCompose      = "crucible-workspace.yaml"
	refreshBefore         = 15 * time.Minute
)

// AWSRunner runs runtime: aws labs (spec §8.2). The workspace is a ClusterRunner lab whose only service is the
// workspace container, with the lab's credentials mounted at /aws. One-shot terraform pods in the same namespace
// apply and destroy the lab's module with the same credentials; state lives in the lab account's state bucket under
// labs/<id>.tfstate. Credentials are one-hour STS sessions tagged with the lab id, refreshed by the sweep.
type AWSRunner struct {
	Cluster        *ClusterRunner
	Cloud          awscloud.Cloud
	StateBucket    string // deploy/aws/labs output state_bucket
	StateRegion    string
	WorkspaceImage string
	TerraformImage string
	DryRun         *awscloud.Fake // dry-run mode: terraform pods only unpack the module; this fake stands in for AWS
	Now            func() time.Time

	mu      sync.Mutex
	expires map[string]time.Time   // lab id → when its mounted credentials expire (empty after a restart)
	locks   map[string]*sync.Mutex // one provision/destroy/refresh per lab at a time in this process
}

var _ Runner = (*AWSRunner)(nil)

func (a *AWSRunner) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// ponytail: in-process per-lab lock and expiry map, like setupLocks; a second API replica would need DB state.
func (a *AWSRunner) lock(id string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks == nil {
		a.locks = map[string]*sync.Mutex{}
	}
	if a.locks[id] == nil {
		a.locks[id] = &sync.Mutex{}
	}
	return a.locks[id]
}

func (a *AWSRunner) remember(id string, exp time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.expires == nil {
		a.expires = map[string]time.Time{}
	}
	a.expires[id] = exp
}

func (a *AWSRunner) forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.expires, id)
}

func (a *AWSRunner) Available(*Instance) error { return nil }

func (a *AWSRunner) Provision(context.Context, *Instance, []byte, string) error {
	return errors.New("aws labs are provisioned with ProvisionLab")
}

func (a *AWSRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	return a.Cluster.OpenPTY(ctx, inst, service, cols, rows)
}

func (a *AWSRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	return a.Cluster.RunScript(ctx, inst, s)
}

func labSession(inst *Instance) awscloud.Session {
	return awscloud.Session{LabID: inst.ID, Team: inst.Team, Training: inst.Training}
}

// workspaceComposeFile is the aws lab's compose project inside the dind pod: one workspace container with the AWS
// CLI. /aws is the mounted credentials secret; kubelet refreshes it in place, so no restart is needed.
func workspaceComposeFile(image, region, labID string) []byte {
	return fmt.Appendf(nil, `# Written by Crucible: the aws lab workspace (spec §8.2).
services:
  workspace:
    image: %q
    entrypoint: ["sleep", "infinity"]
    working_dir: /root
    environment:
      AWS_SHARED_CREDENTIALS_FILE: /aws/credentials
      AWS_REGION: %q
      CRUCIBLE_LAB_ID: %q
    volumes: ["/aws:/aws:ro"]
`, image, region, labID)
}

func (a *AWSRunner) backend(id string) string {
	return fmt.Sprintf("bucket       = %q\nkey          = %q\nregion       = %q\nuse_lockfile = true\n",
		a.StateBucket, "labs/"+id+".tfstate", a.StateRegion)
}

// putCreds writes the credentials file into the lab's secret, creating or replacing it (RBAC: create, update;
// Crucible can never read a secret back).
func (a *AWSRunner) putCreds(ctx context.Context, ns string, c awscloud.Credentials) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: awsSecret, Namespace: ns,
		Labels: map[string]string{labLabel: strings.TrimPrefix(ns, "lab-")}}, Data: map[string][]byte{"credentials": c.File()}}
	secrets := a.Cluster.Client.CoreV1().Secrets(ns)
	_, err := secrets.Create(ctx, s, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = secrets.Update(ctx, s, metav1.UpdateOptions{})
	}
	return err
}

// ProvisionLab: lab credentials, the workspace (namespace, policies, credentials secret, module config map, dind pod
// with the workspace container), then terraform apply in a runner pod. Each step is idempotent.
func (a *AWSRunner) ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error {
	if !validLabID(inst.ID) || lab.AWS == nil {
		return errors.New("invalid aws lab")
	}
	l := a.lock(inst.ID)
	l.Lock()
	defer l.Unlock()
	creds, err := a.Cloud.AssumeLab(ctx, labSession(inst))
	if err != nil {
		return fmt.Errorf("getting lab credentials: %w", err)
	}
	module, err := ModuleBundle(lab, inst)
	if err != nil {
		return err
	}
	bundle, err := BundleWith(lab.Dir, map[string][]byte{workspaceCompose: workspaceComposeFile(a.WorkspaceImage, lab.AWS.Region, inst.ID)})
	if err != nil {
		return err
	}
	setup := func(ns string) error {
		if err := a.putCreds(ctx, ns, creds); err != nil {
			return err
		}
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: tfConfigMap, Namespace: ns, Labels: map[string]string{labLabel: inst.ID}},
			BinaryData: map[string][]byte{"module.tgz": module}, Data: map[string]string{"backend.hcl": a.backend(inst.ID)}}
		_, err := a.Cluster.Client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	if err := a.Cluster.provision(ctx, inst, bundle, workspaceCompose, setup); err != nil {
		return err
	}
	a.remember(inst.ID, creds.Expires)
	if err := a.runTF(ctx, inst.ID, tfApplyPod, "apply"); err != nil {
		return err
	}
	if a.DryRun != nil {
		a.DryRun.SimulateApply(lab.AWS.Region, labSession(inst))
	}
	return nil
}

// runTF runs terraform <action> in a one-shot pod and waits for it. A pod left by an earlier attempt is reused
// (still running: wait for it; finished: its result stands), except a failed destroy, which runs again.
func (a *AWSRunner) runTF(ctx context.Context, id, name, action string) error {
	ns := labNamespace(id)
	pods := a.Cluster.Client.CoreV1().Pods(ns)
	pod := tfPod(id, name, a.TerraformImage, tfScript(action, a.DryRun != nil))
	_, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		err = nil
		if action == "destroy" {
			if p, gerr := pods.Get(ctx, name, metav1.GetOptions{}); gerr == nil && p.Status.Phase == corev1.PodFailed {
				if err = a.Cluster.deletePod(ctx, ns, name); err == nil {
					_, err = pods.Create(ctx, pod, metav1.CreateOptions{})
				}
			}
		}
	}
	if err != nil {
		return fmt.Errorf("starting terraform %s: %w", action, err)
	}
	ok, msg, err := a.Cluster.waitDone(ctx, ns, name)
	if err != nil {
		return fmt.Errorf("terraform %s: %w", action, err)
	}
	if !ok {
		return fmt.Errorf("terraform %s failed: %s", action, tailOf([]byte(msg)))
	}
	return nil
}

// Destroy stops a running apply, refreshes the credentials, runs terraform destroy, then deletes the namespace.
// Service.destroyRuntime runs the tag sweep afterwards. Without a namespace there is nothing to run terraform in:
// the tag sweep and the reaper clean up instead.
func (a *AWSRunner) Destroy(ctx context.Context, inst *Instance) error {
	if !validLabID(inst.ID) {
		return errors.New("invalid lab id")
	}
	l := a.lock(inst.ID)
	l.Lock()
	defer l.Unlock()
	ns := labNamespace(inst.ID)
	var errs []error
	if err := a.Cluster.deletePod(ctx, ns, tfApplyPod); err != nil {
		errs = append(errs, fmt.Errorf("stopping terraform apply: %w", err))
	}
	creds, err := a.Cloud.AssumeLab(ctx, labSession(inst))
	if err == nil {
		err = a.putCreds(ctx, ns, creds)
	}
	if err == nil {
		err = a.runTF(ctx, inst.ID, tfDestroyPod, "destroy")
	}
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if a.DryRun != nil {
		a.DryRun.SimulateDestroy(inst.ID)
	}
	a.forget(inst.ID)
	if err := a.Cluster.Destroy(ctx, inst); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Refresh re-assumes the lab role when the mounted credentials have less than 15 minutes left (spec §14: one hour,
// auto-refreshed); kubelet updates the mounted file within about a minute. After a restart nothing is remembered,
// so the first sweep refreshes every ready lab once. A lab busy provisioning or destroying is skipped this round.
func (a *AWSRunner) Refresh(ctx context.Context, inst *Instance) error {
	a.mu.Lock()
	exp, ok := a.expires[inst.ID]
	a.mu.Unlock()
	if ok && a.now().Add(refreshBefore).Before(exp) {
		return nil
	}
	l := a.lock(inst.ID)
	if !l.TryLock() {
		return nil
	}
	defer l.Unlock()
	creds, err := a.Cloud.AssumeLab(ctx, labSession(inst))
	if err != nil {
		return err
	}
	if err := a.putCreds(ctx, labNamespace(inst.ID), creds); err != nil {
		return err
	}
	a.remember(inst.ID, creds.Expires)
	return nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/labs/ -count=1 2>&1 | tail -3`
Expected: `ok`, with every `TestAWS…` passing and the bundle tests unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/labs/bundle.go internal/labs/aws.go internal/labs/aws_test.go
git commit -m "feat(labs): AWSRunner: workspace pod plus terraform runner pods with refreshed, tag-scoped lab credentials

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: The aws runtime in the lab service: slow destroys, tag sweep, credential refresh, wiring

**Files:**
- Modify: `internal/labs/service.go` (`Service.inflight`, `once`, `labProvisioner`, `provision`, `destroyTimeout`, `destroy`, `destroyRuntime`, `Sweep`, new `retryDestroy`)
- Create: `internal/labs/aws_sweep.go`
- Create: `internal/labs/aws_service_test.go`
- Modify: `cmd/crucible-api/main.go`

**Interfaces:**
- Consumes: Tasks 2, 3, 5 and 7; `f.withForge401`, `f.waitState`.
- Produces:
  - `(*Service).sweepLab(ctx, labID)`;
  - `(*Service).deleteAll(ctx, region, creds, source string, res []awscloud.Resource) []string`, which returns ARNs recorded for the first time;
  - `(*Service).finding(ctx, source, labID, arn, action, detail string) bool`;
  - `(*Service).refreshAWS(ctx)`;
  - `(*Service).once(labID string, fn func()) bool`;
  - test helper `(*fx).withAWS(t) (*fakeAWSRunner, *awscloud.Fake)`;
  - environment variables `CRUCIBLE_AWS_LABS` (`1`|`dryrun`), `CRUCIBLE_AWS_LAB_ROLE_ARN`, `CRUCIBLE_AWS_OPS_ROLE_ARN`, `CRUCIBLE_AWS_STATE_BUCKET`, `CRUCIBLE_AWS_STATE_REGION`, `CRUCIBLE_AWS_LAB_REGIONS` (comma-separated, default `eu-west-1`), `CRUCIBLE_AWS_WORKSPACE_IMAGE`, `CRUCIBLE_TERRAFORM_IMAGE`, `CRUCIBLE_INFRACOST=off`, `INFRACOST_API_KEY`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/aws_service_test.go`:

```go
package labs

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/content"
)

// fakeAWSRunner is an aws runtime without Kubernetes: "apply" adds the lab's bucket and a hand-made volume to the
// fake cloud; "destroy" removes the bucket only, as terraform would.
type fakeAWSRunner struct {
	fakeRunner
	cloud *awscloud.Fake
	labs  []*content.Lab
}

func (f *fakeAWSRunner) ProvisionLab(_ context.Context, inst *Instance, lab *content.Lab) error {
	f.mu.Lock()
	f.labs = append(f.labs, lab)
	err := f.failProvision
	f.mu.Unlock()
	if err == nil {
		f.cloud.SimulateApply(lab.AWS.Region, awscloud.Session{LabID: inst.ID})
	}
	return err
}

func (f *fakeAWSRunner) Destroy(ctx context.Context, inst *Instance) error {
	f.cloud.SimulateDestroy(inst.ID)
	return f.fakeRunner.Destroy(ctx, inst)
}

func (f *fx) withAWS(t *testing.T) (*fakeAWSRunner, *awscloud.Fake) {
	t.Helper()
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	run := &fakeAWSRunner{cloud: cloud}
	f.s.Runners["aws"], f.s.Cloud = run, cloud
	f.rates["cloud-heat"] = 0.04
	return run, cloud
}

func TestAWSLabFromRequestToTagSweep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, cloud := f.withAWS(t)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || v.State != PendingApproval || v.EstimateUSD != 0.04 {
		t.Fatalf("an aws lab always waits for an approver: %+v %v", v, err)
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	if len(run.labs) != 1 || run.labs[0].ID != "cloud-heat" {
		t.Fatalf("the aws runner got the lab content: %+v", run.labs)
	}
	got, err := f.s.End(ctx, f.u, v.ID)
	if err != nil || (got.State != Destroying && got.State != Destroyed) {
		t.Fatalf("End returns without waiting for terraform: %+v %v", got, err)
	}
	f.waitState(t, f.u, v.ID, Destroyed)
	vol := "arn:aws:ec2:eu-west-1:000000000000:volume/vol-" + v.ID
	if cloud.Has(vol) {
		t.Fatal("the tag sweep deletes what terraform did not manage")
	}
	var action string
	if err := f.s.DB.QueryRow(ctx, `SELECT action FROM reaper_findings WHERE source = 'destroy' AND arn = $1`, vol).Scan(&action); err != nil || action != "deleted" {
		t.Fatalf("finding: %q %v", action, err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM reaper_findings WHERE arn LIKE 'arn:aws:s3:::%'`).Scan(&n)
	if n != 0 {
		t.Fatal("what terraform destroyed itself is not a finding")
	}
	// Spec §14: credentials never leave the workspace.
	view, _ := f.s.Get(ctx, f.u, v.ID)
	b, _ := json.Marshal(view)
	var events string
	_ = f.s.DB.QueryRow(ctx, `SELECT coalesce(string_agg(kind || ' ' || detail, E'\n'), '') FROM lab_events WHERE lab_id = $1`, v.ID).Scan(&events)
	for _, secret := range []string{"fake-token", "fake-secret", "ASIAFAKE"} {
		if strings.Contains(string(b), secret) || strings.Contains(events, secret) {
			t.Fatalf("%s leaked into the view or the lab events", secret)
		}
	}
}

func TestStuckAWSDestroyWaitsLonger(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	now := f.clk.Now()
	stuck := func(training, module, runtime string) string {
		in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: training, Module: module, SHA: "abc", Runtime: runtime,
			State: Destroying, CreatedAt: now, LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "auto"}
		if err := f.s.insert(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET destroyed_at = $2 WHERE id = $1`, in.ID, now.Add(-15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	awsID, localID := stuck("forge-401", "01-cloud-heat", "aws"), stuck("forge-101", "02-first-lab", "local")
	state := func(id string) (st State) {
		_ = f.s.DB.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1`, id).Scan(&st)
		return st
	}
	f.s.Sweep(ctx)
	if state(localID) != Destroyed || state(awsID) != Destroying {
		t.Fatal("a local or cluster destroy is retried after 10 minutes; terraform gets 30")
	}
	f.clk.Add(20 * time.Minute)
	f.s.Sweep(ctx)
	f.s.Sweep(ctx) // while the retry runs, another sweep must not start a second one
	for i := 0; i < 300 && state(awsID) != Destroyed; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	run.mu.Lock()
	n := len(slices.DeleteFunc(slices.Clone(run.destroyed), func(id string) bool { return id != awsID }))
	run.mu.Unlock()
	if state(awsID) != Destroyed || n != 1 {
		t.Fatalf("one retry, then destroyed: state %s, %d destroys", state(awsID), n)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'TagSweep|StuckAWS' -count=1 2>&1 | tail -4`
Expected: FAIL. The aws lab is provisioned through `Provision` (the fake ignores it, so `run.labs` is empty), and nothing sweeps tags.

- [ ] **Step 3: The service changes**

In `internal/labs/service.go`:

1. Add the field `inflight sync.Map // lab ids with a slow (aws) destroy running in this process` to `Service`, and this helper:

```go
// once runs fn in the background unless a run for this lab is already in flight in this process.
func (s *Service) once(labID string, fn func()) bool {
	if _, busy := s.inflight.LoadOrStore(labID, true); busy {
		return false
	}
	go func() {
		defer s.inflight.Delete(labID)
		fn()
	}()
	return true
}

// labProvisioner is a runner that needs the lab itself, not a bundle (aws: module, region, workspace compose).
type labProvisioner interface {
	ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error
}

// destroyTimeout bounds one destroy: terraform destroy plus the tag sweep for aws labs, a namespace or a compose
// project otherwise.
func destroyTimeout(runtime string) time.Duration {
	if runtime == "aws" {
		return 20 * time.Minute
	}
	return 2 * time.Minute
}
```

2. In `provision`, replace the start of the body up to `if err == nil && lab.Setup != nil` with:

```go
	r, err := s.runner(inst.Runtime)
	if lp, ok := r.(labProvisioner); ok {
		err = lp.ProvisionLab(ctx, inst, lab) // aws: builds its own workspace bundle and terraform module
	} else if err == nil {
		var bundle []byte
		if bundle, err = Bundle(lab.Dir); err == nil {
			err = r.Provision(ctx, inst, bundle, lab.Compose)
		}
	}
```

In both cleanup paths of `provision` (the failure path and the "ended while provisioning" path), use `destroyTimeout(inst.Runtime)` instead of `2*time.Minute`, and call `_ = s.destroyRuntime(dctx, inst)` instead of `if r != nil { _ = r.Destroy(dctx, inst) }`. `destroyRuntime` already handles a missing runner, and for aws it also sweeps tags.

3. Replace `destroy` and `destroyRuntime`:

```go
func (s *Service) destroy(ctx context.Context, inst *Instance, reason string) {
	// the request may be cancelled mid-way; a half-finished destroy would wedge the lab in 'destroying'
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
	// destroyed_at doubles as "destroying since" until the final update (see Sweep)
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', end_reason = $2, destroyed_at = $3
		WHERE id = $1 AND state IN ('provisioning', 'ready')`, inst.ID, reason, s.Now())
	if err != nil || tag.RowsAffected() == 0 {
		cancel()
		return
	}
	if inst.Runtime == "aws" { // terraform destroy takes minutes: End, the sweep and the kill switch must not wait
		if !s.once(inst.ID, func() { defer cancel(); s.finishDestroy(ctx, inst, reason) }) {
			cancel()
		}
		return
	}
	defer cancel()
	s.finishDestroy(ctx, inst, reason)
}

// destroyRuntime tears a lab's environment down. For aws labs, terraform destroy is followed by a tag sweep (spec §8.2).
func (s *Service) destroyRuntime(ctx context.Context, inst *Instance) error {
	r, err := s.runner(inst.Runtime)
	if err != nil {
		return err
	}
	err = r.Destroy(ctx, inst)
	if inst.Runtime == "aws" {
		s.sweepLab(ctx, inst.ID)
	}
	return err
}
```

4. In `Sweep`, change the stuck-destroy condition to
`OR (state = 'destroying' AND destroyed_at < $1 - CASE WHEN runtime = 'aws' THEN interval '30 minutes' ELSE interval '10 minutes' END)`,
replace the body of `case inst.State == "destroying":` with:

```go
		case inst.State == "destroying": // a destroy that never finished: one more attempt, then give up
			if inst.Runtime == "aws" {
				s.once(inst.ID, func() { s.retryDestroy(context.WithoutCancel(ctx), inst) }) // never hold up the sweep
			} else {
				s.retryDestroy(ctx, inst)
			}
			continue
```

add `s.refreshAWS(ctx)` after `s.reconcileCluster(ctx)`, and move the old inline code into:

```go
// retryDestroy is the last attempt for a destroy that never finished; the row ends 'destroyed' either way.
func (s *Service) retryDestroy(ctx context.Context, inst *Instance) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
	defer cancel()
	err := s.destroyRuntime(ctx, inst)
	note := map[bool]string{true: "cleanup timed out", false: ""}[err != nil]
	if errors.Is(err, apperr.Unavailable) {
		note = "cleanup skipped: " + err.Error() + "; delete the lab namespace by hand"
	}
	_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroyed', destroyed_at = $2, error = $3
		WHERE id = $1 AND state = 'destroying'`, inst.ID, s.Now(), note)
}
```

Create `internal/labs/aws_sweep.go`:

```go
package labs

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"crucible/internal/awscloud"
)

// sweepLab deletes whatever still carries this lab's tag after terraform destroy (spec §8.2): resources the trainee
// made by hand in the workspace, or what a failed destroy left. What it deletes, or cannot, is recorded.
func (s *Service) sweepLab(ctx context.Context, labID string) {
	if s.Cloud == nil {
		return
	}
	creds, err := s.Cloud.AssumeLab(ctx, awscloud.Session{LabID: labID})
	if err != nil {
		s.Log.Warn("tag sweep: no lab credentials; the reaper will retry", "lab", labID, "err", err)
		return
	}
	for _, region := range s.AWSRegions {
		res, err := s.Cloud.Tagged(ctx, region, labID)
		if err != nil {
			s.Log.Warn("tag sweep: listing failed; the reaper will retry", "lab", labID, "region", region, "err", err)
			continue
		}
		s.deleteAll(ctx, region, creds, "destroy", res)
	}
}

// deleteAll deletes resources with one lab's credentials, instances first (their volumes and security groups are
// only free once they are gone), and records each result. Already-gone resources (terraform got them; the tag
// inventory lags) are not findings. It returns the ARNs recorded for the first time.
func (s *Service) deleteAll(ctx context.Context, region string, creds awscloud.Credentials, source string, res []awscloud.Resource) []string {
	first := func(r awscloud.Resource) int {
		if strings.Contains(r.ARN, ":instance/") {
			return 0
		}
		return 1
	}
	res = slices.Clone(res)
	slices.SortStableFunc(res, func(a, b awscloud.Resource) int { return cmp.Compare(first(a), first(b)) })
	var fresh []string
	for _, r := range res {
		deleted, err := s.Cloud.Delete(ctx, region, creds, r.ARN)
		action, detail := "deleted", ""
		switch {
		case errors.Is(err, awscloud.ErrUnsupported):
			action, detail = "reported", err.Error()
		case err != nil:
			action, detail = "failed", err.Error()
		case !deleted:
			continue
		}
		if s.finding(ctx, source, r.LabID, r.ARN, action, detail) {
			fresh = append(fresh, r.ARN)
		}
	}
	return fresh
}

// finding records one result; the same (source, ARN) again updates its row. It reports whether the row is new.
func (s *Service) finding(ctx context.Context, source, labID, arn, action, detail string) bool {
	var inserted bool
	err := s.DB.QueryRow(ctx, `INSERT INTO reaper_findings (source, arn, lab_id, action, detail, first_at, last_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (source, arn) DO UPDATE SET lab_id = excluded.lab_id, action = excluded.action,
			detail = excluded.detail, last_at = excluded.last_at
		RETURNING xmax = 0`, source, cleanText(arn), cleanText(labID), action, cleanText(detail), s.Now()).Scan(&inserted)
	if err != nil {
		s.Log.Error("recording a reaper finding failed", "arn", arn, "err", err)
	}
	return inserted
}

// refreshAWS keeps the credentials of running aws labs fresh (spec §14). Every sweep calls it; Refresh only
// calls STS when less than 15 minutes are left.
func (s *Service) refreshAWS(ctx context.Context) {
	ar, ok := s.Runners["aws"].(*AWSRunner)
	if !ok {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("aws credential refresh query failed", "err", err)
		return
	}
	labs, err := collectInst(rows)
	if err != nil {
		s.Log.Error("aws credential refresh query failed", "err", err)
		return
	}
	for _, inst := range labs {
		if err := ar.Refresh(ctx, inst); err != nil {
			s.Log.Warn("refreshing aws lab credentials failed", "lab", inst.ID, "err", err)
		}
	}
}
```

- [ ] **Step 4: Wire it into crucible-api**

In `cmd/crucible-api/main.go`, after the cluster block and before `labSvc := …`, add (imports: `"crucible/internal/awscloud"`, `"crucible/internal/infracost"`):

```go
	var cloud awscloud.Cloud
	var awsRegions []string
	switch mode := os.Getenv("CRUCIBLE_AWS_LABS"); mode {
	case "":
	case "1", "dryrun":
		cr, ok := runners["cluster"].(*labs.ClusterRunner)
		if !ok {
			return errors.New("CRUCIBLE_AWS_LABS needs CRUCIBLE_CLUSTER_LABS=1: the workspace and terraform pods run in the cluster")
		}
		ar := &labs.AWSRunner{Cluster: cr, StateBucket: os.Getenv("CRUCIBLE_AWS_STATE_BUCKET"), StateRegion: os.Getenv("CRUCIBLE_AWS_STATE_REGION"),
			WorkspaceImage: env("CRUCIBLE_AWS_WORKSPACE_IMAGE", labs.DefaultWorkspaceImage),
			TerraformImage: env("CRUCIBLE_TERRAFORM_IMAGE", labs.DefaultTerraformImage)}
		awsRegions = strings.Split(env("CRUCIBLE_AWS_LAB_REGIONS", "eu-west-1"), ",")
		if mode == "dryrun" {
			fake := &awscloud.Fake{}
			ar.Cloud, ar.DryRun = fake, fake
			slog.Warn("CRUCIBLE_AWS_LABS=dryrun: aws labs run without AWS (terraform pods only unpack the module; a fake lab account stands in). Development only")
		} else {
			if ar.StateBucket == "" || ar.StateRegion == "" {
				return errors.New("CRUCIBLE_AWS_LABS=1 needs CRUCIBLE_AWS_STATE_BUCKET and CRUCIBLE_AWS_STATE_REGION")
			}
			if ar.Cloud, err = awscloud.New(ctx, awscloud.Config{LabRoleARN: os.Getenv("CRUCIBLE_AWS_LAB_ROLE_ARN"),
				OpsRoleARN: os.Getenv("CRUCIBLE_AWS_OPS_ROLE_ARN")}); err != nil {
				return fmt.Errorf("aws labs: %w", err)
			}
		}
		runners["aws"], cloud = ar, ar.Cloud
		switch {
		case os.Getenv("CRUCIBLE_INFRACOST") == "off":
			estimators["aws"] = rates
			slog.Warn("CRUCIBLE_INFRACOST=off: aws labs are priced from CRUCIBLE_DEV_LAB_USD_PER_HOUR")
		case os.Getenv("INFRACOST_API_KEY") == "":
			slog.Warn("INFRACOST_API_KEY is not set: aws labs show no estimate and cannot be requested")
		default:
			estimators["aws"] = &labs.InfracostEstimator{Run: infracost.Exec}
		}
		slog.Info("aws labs enabled", "mode", mode, "regions", awsRegions)
	default:
		return fmt.Errorf("CRUCIBLE_AWS_LABS must be 1, dryrun or empty, not %q", mode)
	}
```

and add `Cloud: cloud, AWSRegions: awsRegions` to the `labs.Service{…}` literal.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/labs/ ./cmd/... -count=1 2>&1 | tail -4 && go build ./...`
Expected: `ok`. The existing destroy, sweep and kill-switch tests still pass, because local and cluster destroys stay synchronous with their 2-minute and 10-minute limits.

- [ ] **Step 6: Commit**

```bash
git add internal/labs/service.go internal/labs/aws_sweep.go internal/labs/aws_service_test.go cmd/crucible-api/main.go
git commit -m "feat(labs): aws runtime in the lab service: async destroys with a tag sweep, credential refresh, dry-run wiring

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: The reaper and Cost Explorer ingestion as periodic jobs

**Files:**
- Create: `internal/labs/reaper.go`, `internal/labs/reaper_test.go`
- Modify: `internal/labs/jobs.go` (two workers)
- Modify: `internal/notify/notify.go` (`ReaperReport`)
- Modify: `cmd/crucible-api/main.go` (register the workers and the 6-hourly periodic jobs)

**Interfaces:**
- Consumes: `deleteAll`, `finding` (Task 8); `awscloud.Cloud` (Task 3); `monthStart`, `validLabID`, `cleanText`.
- Produces:
  - `(*Service).Reap(ctx) error` and `(*Service).IngestCosts(ctx) error`. Both are no-ops without `s.Cloud`, and both record their outcome in `aws_ops`.
  - `labs.ReapArgs` / `ReapWorker`, `labs.CostArgs` / `CostWorker`.
  - `notify.ReaperReport Kind = "reaper_report"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/reaper_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/notify"
)

// awsRow inserts a Forge 401 lab row; endedAgo > 0 marks it destroyed that long ago.
func (f *fx) awsRow(t *testing.T, st State, endedAgo time.Duration) string {
	t.Helper()
	ctx, now := context.Background(), f.clk.Now()
	in := &Instance{ID: newLabID(), UserID: f.u.ID, Team: "forge", Training: "forge-401", Module: "01-cloud-heat", SHA: "abc",
		Runtime: "aws", State: st, CreatedAt: now.Add(-3 * time.Hour), LastActivityAt: now, TTL: time.Hour, IdleTimeout: 30 * time.Minute, Tier: "approver"}
	if err := f.s.insert(ctx, in); err != nil {
		t.Fatal(err)
	}
	if endedAgo > 0 {
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET destroyed_at = $2 WHERE id = $1`, in.ID, now.Add(-endedAgo)); err != nil {
			t.Fatal(err)
		}
	}
	return in.ID
}

func (n *fakeNotifier) count(kind notify.Kind) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, ev := range n.events {
		if ev.Kind == kind {
			c++
		}
	}
	return c
}

func TestReaperDeletesOnlyEndedKnownLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withForge401(t)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	live, ended, recent := f.awsRow(t, Ready, 0), f.awsRow(t, Destroyed, 2*time.Hour), f.awsRow(t, Destroyed, 10*time.Minute)
	unknown := "cccccccccccc"
	vol := func(id string) string { return "arn:aws:ec2:eu-west-1:000000000000:volume/vol-" + id }
	for _, id := range []string{live, ended, recent, unknown} {
		cloud.Add("eu-west-1", awscloud.Resource{ARN: vol(id), LabID: id})
	}
	cloud.Add("eu-west-1", awscloud.Resource{ARN: vol("weird"), LabID: `x"; Deny`})
	cloud.AddEvent(awscloud.TrailEvent{ID: "ev-1", At: f.clk.Now().Add(-time.Hour), LabID: live, Event: "CreateVolume", Resources: []string{"vol-9"}})

	dead, cancel := context.WithCancel(ctx)
	cancel()
	_ = f.s.Reap(dead)
	if !cloud.Has(vol(ended)) {
		t.Fatal("without the database the reaper must not guess")
	}

	if err := f.s.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	for id, kept := range map[string]bool{live: true, recent: true, unknown: true, "weird": true, ended: false} {
		if cloud.Has(vol(id)) != kept {
			t.Fatalf("%s: kept=%v, want %v", id, cloud.Has(vol(id)), kept)
		}
	}
	got := map[string]string{}
	rows, _ := f.s.DB.Query(ctx, `SELECT arn, source || '/' || action FROM reaper_findings`)
	for rows.Next() {
		var arn, what string
		_ = rows.Scan(&arn, &what)
		got[arn] = what
	}
	rows.Close()
	want := map[string]string{vol(ended): "reaper/deleted", vol(unknown): "reaper/reported", vol("weird"): "reaper/reported",
		"cloudtrail:ev-1": "trail/reported"}
	if !maps.Equal(got, want) {
		t.Fatalf("findings:\n got %v\nwant %v", got, want)
	}
	ev := f.notes.last(notify.ReaperReport)
	if ev == nil || !slices.Contains(ev.To, "admin@crucible.local") || ev.Link != "/ledger" || ev.Team != "" {
		t.Fatalf("admins get one summary, never a team channel: %+v", ev)
	}
	before := f.notes.count(notify.ReaperReport)
	_ = f.s.Reap(ctx)
	if f.notes.count(notify.ReaperReport) != before {
		t.Fatal("the same findings again are not news")
	}
	var okAt *time.Time
	_ = f.s.DB.QueryRow(ctx, `SELECT reap_ok_at FROM aws_ops`).Scan(&okAt)
	if okAt == nil {
		t.Fatal("a clean run is recorded for the Ledger")
	}
}

func TestIngestCostsUpsertsAndMarksFailures(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	cloud := &awscloud.Fake{Now: f.clk.Now}
	f.s.Cloud = cloud
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) // the clock is 2026-10-05 09:00 UTC
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "aaaaaaaaaaaa", USD: 0.42})
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "not-a-lab", USD: 9})
	cloud.AddCost(awscloud.DailyCost{Day: day.AddDate(0, -1, 0), LabID: "aaaaaaaaaaaa", USD: 5}) // outside the window
	total := func() (v float64) {
		_ = f.s.DB.QueryRow(ctx, `SELECT coalesce(sum(usd), 0) FROM cost_actuals`).Scan(&v)
		return v
	}
	if err := f.s.IngestCosts(ctx); err != nil || total() != 0.42 {
		t.Fatalf("one valid row in the window: %v %v", total(), err)
	}
	cloud.AddCost(awscloud.DailyCost{Day: day, LabID: "aaaaaaaaaaaa", USD: 0.5}) // Cost Explorer revised the day
	_ = f.s.IngestCosts(ctx)
	if total() != 0.5 {
		t.Fatalf("a revised day replaces the old amount: %v", total())
	}
	okBefore := f.clk.Now()
	f.clk.Add(time.Hour)
	cloud.Err = errors.New("Cost Explorer is down")
	if err := f.s.IngestCosts(ctx); err != nil {
		t.Fatalf("a failed ingestion is recorded, not retried in a storm: %v", err)
	}
	var okAt time.Time
	var msg string
	_ = f.s.DB.QueryRow(ctx, `SELECT ingest_ok_at, ingest_error FROM aws_ops`).Scan(&okAt, &msg)
	if total() != 0.5 || !okAt.Equal(okBefore) || msg != "Cost Explorer is down" {
		t.Fatalf("old rows kept, last success kept, error shown: %v %v %q", total(), okAt, msg)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'Reaper|IngestCosts' -count=1 2>&1 | tail -3`
Expected: FAIL to compile (`undefined: notify.ReaperReport`, `Reap`, `IngestCosts`).

- [ ] **Step 3: Implement**

In `internal/notify/notify.go`, add `ReaperReport Kind = "reaper_report"` to the constants and `{ReaperReport, "Leftover AWS lab resources need attention"}` to `Kinds`.

Create `internal/labs/reaper.go`:

```go
package labs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/notify"
)

// Reap is the periodic reaper (spec §8.2). For each allowed region it lists every resource tagged crucible:lab-id.
// Resources of a lab this database knows and that ended over an hour ago are deleted with that lab's own role
// (the destroy-time sweep had its chance, and the tag inventory lags). Resources with an unknown or malformed lab
// id are reported, never deleted: they may belong to another deployment or to a restored database. Successful
// create calls by lab sessions without the lab tag (CloudTrail) are reported. If the database cannot be read,
// nothing is deleted. Admins get one summary when something new turns up.
func (s *Service) Reap(ctx context.Context) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now()
	var fresh []string
	var errs []error
	report := func(source, labID, arn, detail string) {
		if s.finding(ctx, source, labID, arn, "reported", detail) {
			fresh = append(fresh, arn)
		}
	}
	for _, region := range s.AWSRegions {
		res, err := s.Cloud.Tagged(ctx, region, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: listing tagged resources: %w", region, err))
			continue
		}
		byLab := map[string][]awscloud.Resource{}
		var ids []string
		for _, r := range res {
			if !validLabID(r.LabID) {
				report("reaper", "", r.ARN, fmt.Sprintf("tagged crucible:lab-id=%q, which is not a Crucible lab id: check it by hand", r.LabID))
				continue
			}
			if byLab[r.LabID] == nil {
				ids = append(ids, r.LabID)
			}
			byLab[r.LabID] = append(byLab[r.LabID], r)
		}
		ended, known, err := s.labsEnded(ctx, ids, now.Add(-time.Hour))
		if err != nil {
			errs = append(errs, err)
			continue // never guess: without the database nothing is deleted
		}
		for _, id := range ids {
			switch {
			case !known[id]:
				for _, r := range byLab[id] {
					report("reaper", id, r.ARN, "no lab with this id in this Crucible (another deployment, or a restored database?): delete it by hand if nobody uses it")
				}
			case ended[id]:
				creds, err := s.Cloud.AssumeLab(ctx, awscloud.Session{LabID: id})
				if err != nil {
					errs = append(errs, fmt.Errorf("lab %s: credentials: %w", id, err))
					continue
				}
				fresh = append(fresh, s.deleteAll(ctx, region, creds, "reaper", byLab[id])...)
			}
		}
		events, err := s.Cloud.LabWrites(ctx, region, now.Add(-24*time.Hour))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: CloudTrail: %w", region, err))
			continue
		}
		for _, e := range events {
			report("trail", e.LabID, "cloudtrail:"+e.ID, fmt.Sprintf("%s at %s by lab %s created %s without the crucible:lab-id tag: check the resource and the lab role's policy",
				e.Event, e.At.UTC().Format(time.RFC3339), e.LabID, strings.Join(e.Resources, ", ")))
		}
	}
	if err := errors.Join(errs...); err != nil {
		s.Log.Warn("reaper run incomplete", "err", err)
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET reap_error = $1, reap_error_at = $2`, cleanText(err.Error()), now)
	} else {
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET reap_ok_at = $1, reap_error = ''`, now)
	}
	if len(fresh) > 0 {
		if st, err := s.platform(); err == nil {
			s.notify(ctx, notify.Event{Kind: notify.ReaperReport, To: st.Platform.Admins, Link: "/ledger",
				Subject: fmt.Sprintf("The reaper found %d leftover AWS lab resource(s)", len(fresh)),
				Text:    "Deleted, failed, or waiting for a human: see Reaper findings on the Ledger.\n\n" + strings.Join(fresh, "\n")})
		}
	}
	return nil
}

// labsEnded looks lab ids up: known = this database has the lab; ended = it is over and ended before cutoff.
func (s *Service) labsEnded(ctx context.Context, ids []string, cutoff time.Time) (ended, known map[string]bool, err error) {
	ended, known = map[string]bool{}, map[string]bool{}
	if len(ids) == 0 {
		return ended, known, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT id, state NOT IN ('pending_approval', 'provisioning', 'ready', 'destroying')
		AND coalesce(destroyed_at, created_at) < $2 FROM lab_instances WHERE id = ANY($1)`, ids, cutoff)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var over bool
		if err := rows.Scan(&id, &over); err != nil {
			return nil, nil, err
		}
		known[id], ended[id] = true, over
	}
	return ended, known, rows.Err()
}

// IngestCosts stores Cost Explorer's daily cost per lab (spec §9.3): month to date plus the three days before,
// because a day's figure keeps changing for a while. A failure is recorded for the Ledger ("actuals stale") and
// retried by the next periodic run; caps keep using estimates meanwhile (spec §14).
func (s *Service) IngestCosts(ctx context.Context) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now().UTC()
	from, to := monthStart(now.AddDate(0, 0, -3)), now.Truncate(24*time.Hour).AddDate(0, 0, 1)
	costs, err := s.Cloud.Costs(ctx, from, to)
	if err != nil {
		s.Log.Warn("cost explorer ingestion failed; the Ledger marks actuals stale", "err", err)
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_error = $1, ingest_error_at = $2`, cleanText(err.Error()), now)
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	for _, c := range costs {
		if !validLabID(c.LabID) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (lab_id, day) DO UPDATE SET usd = excluded.usd, updated_at = excluded.updated_at`,
			c.LabID, c.Day, c.USD, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1, ingest_error = ''`, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
```

Append to `internal/labs/jobs.go`:

```go
// ReapArgs is the AWS reaper; CostArgs is Cost Explorer ingestion. Both run every 6 h and at start (the AWS node
// sleeps at night, so "nightly" means "whenever it is up") and do nothing without aws labs.
type ReapArgs struct{}

func (ReapArgs) Kind() string { return "aws_reap" }

type ReapWorker struct {
	river.WorkerDefaults[ReapArgs]
	S *Service
}

func (w *ReapWorker) Work(ctx context.Context, _ *river.Job[ReapArgs]) error { return w.S.Reap(ctx) }
func (w *ReapWorker) Timeout(*river.Job[ReapArgs]) time.Duration         { return 30 * time.Minute }

type CostArgs struct{}

func (CostArgs) Kind() string { return "aws_costs" }

type CostWorker struct {
	river.WorkerDefaults[CostArgs]
	S *Service
}

func (w *CostWorker) Work(ctx context.Context, _ *river.Job[CostArgs]) error { return w.S.IngestCosts(ctx) }
```

In `cmd/crucible-api/main.go`, add `river.AddWorker(workers, &labs.ReapWorker{S: labSvc})` and `river.AddWorker(workers, &labs.CostWorker{S: labSvc})`, and append `{Every: 6 * time.Hour, Args: labs.ReapArgs{}}, {Every: 6 * time.Hour, Args: labs.CostArgs{}}` to the periodic list.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/labs/ ./internal/notify/ -count=1 2>&1 | tail -3`
Expected: `ok` ×2. If a notify test pins the length of `Kinds`, bump it by one.

- [ ] **Step 5: Commit**

```bash
git add internal/labs/reaper.go internal/labs/reaper_test.go internal/labs/jobs.go internal/notify/notify.go cmd/crucible-api/main.go
git commit -m "feat(finops): periodic reaper (deletes only ended known labs, reports the rest) and Cost Explorer ingestion

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: The Ledger API

**Files:**
- Modify: `internal/rbac/rbac.go`, `internal/rbac/rbac_test.go` (`SpendTeams`)
- Create: `internal/labs/ledger.go`, `internal/labs/ledger_test.go`
- Modify: `internal/labs/http.go` (two routes)
- Modify: `internal/httpapi/server.go` (`can_view_spend` on `/api/me`)

**Interfaces:**
- Consumes:
  - `labCostSQL` and `spend` (Task 2);
  - `IngestCosts` and `Reap` (Task 9);
  - `audit.Log(ctx, db, actor, action, target, detail, sha)`.
- Produces (consumed by Task 11):
  - `func (c rbac.Checker) SpendTeams(email string) []string`;
  - `GET /api/ledger`, which returns `Ledger` JSON;
  - `POST /api/admin/finops/refresh` (admin), which returns `Ledger`;
  - `/api/me` gains `can_view_spend: bool`.

```ts
// JSON (Task 11 mirrors it in types.ts)
Ledger = { month, aws, actuals_as_of?, actuals_stale, actuals_error?, reaped_at?, can_refresh,
  teams: [{ id, name, spend: Spend, programs: [{ training, spend: Spend }] }],
  daily: [{ day, estimate_usd, actual_usd }], running: LedgerLab[], labs: LedgerLab[],
  top_spenders: [{ requester, team, usd }], accuracy: { labs, estimate_usd, actual_usd }, findings?: Finding[] }
LedgerLab = { id, team, training, module, requester, runtime, state, hourly_usd, estimate_usd, cost_usd, actual_usd?, settled, created_at, ends_at? }
Finding = { source, lab_id, arn, action, detail, first_at, last_at }
```

- [ ] **Step 1: Write the failing tests**

Append to `internal/rbac/rbac_test.go` (use the platform fixture the file already builds; if it loads `examples/platform`, the forge team's leader is `leader@crucible.local`):

```go
func TestSpendTeams(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	c := Checker{P: p}
	if got := c.SpendTeams("admin@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("admins see every team: %v", got)
	}
	if got := c.SpendTeams("leader@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("leaders see their team: %v", got)
	}
	if got := c.SpendTeams("trainee@crucible.local"); len(got) != 0 {
		t.Fatalf("trainees see no spend: %v", got)
	}
	p.Teams["forge"].Programs["forge-101"].Roles.Approvers = []string{"senior@crucible.local"}
	if got := c.SpendTeams("senior@crucible.local"); !slices.Equal(got, []string{"forge"}) {
		t.Fatalf("a program approver sees the team's spend (spec §5.3): %v", got)
	}
}
```

Create `internal/labs/ledger_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/awscloud"
)

func TestLedgerScopes(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	_ = f.s.finding(ctx, "reaper", "", "arn:aws:ec2:eu-west-1:1:volume/vol-x", "reported", "check it")
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_error = 'AccessDenied', ingest_error_at = $1`, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ledger(ctx, f.u); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees never see the Ledger: %v", err)
	}
	l, err := f.s.Ledger(ctx, f.leader)
	if err != nil || len(l.Teams) != 1 || l.Teams[0].ID != "forge" || l.Findings != nil || l.ActualsError != "" || l.CanRefresh {
		t.Fatalf("leaders see their team's spend only: %+v %v", l, err)
	}
	a, _ := f.s.Ledger(ctx, f.admin)
	if len(a.Findings) != 1 || a.ActualsError != "AccessDenied" || !a.CanRefresh || !a.ActualsStale {
		t.Fatalf("admins see findings, errors and the refresh button: %+v", a)
	}
	if _, err := f.s.RefreshFinOps(ctx, f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("refresh is admin-only: %v", err)
	}
}

func TestLedgerNumbersAndStaleness(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Cloud = &awscloud.Fake{}
	now := f.clk.Now()
	f.spent(t, "aaaaaaaaaaa1", "forge-101", 2) // est $2, will settle at $0.40
	f.endedAgo(t, "aaaaaaaaaaa1", 72*time.Hour)
	f.spent(t, "aaaaaaaaaaa2", "forge-101", 1) // est $1, Cost Explorer never reported it
	run := &Instance{ID: "aaaaaaaaaaa3", UserID: f.u.ID, Team: "forge", Training: "forge-101", Module: "02-first-lab", SHA: "abc",
		Runtime: "local", State: Ready, CreatedAt: now, LastActivityAt: now, TTL: 2 * time.Hour, IdleTimeout: 30 * time.Minute,
		Tier: "approver", HourlyUSD: 1, EstimateUSD: 2}
	if err := f.s.insert(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET ready_at = $2, ends_at = $3 WHERE id = $1`, run.ID, now.Add(-30*time.Minute), now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ('aaaaaaaaaaa1', $1, 0.4, $1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, now); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Ledger(ctx, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if l.ActualsStale || len(l.Running) != 1 || l.Running[0].ID != run.ID || !closeTo(l.Running[0].CostUSD, 0.5) {
		t.Fatalf("running lab, cost so far $0.50, actuals fresh: %+v", l)
	}
	if len(l.Labs) != 3 || l.Labs[0].ID != "aaaaaaaaaaa2" || l.Labs[2].ID != "aaaaaaaaaaa1" || !l.Labs[2].Settled || *l.Labs[2].ActualUSD != 0.4 {
		t.Fatalf("most expensive first, using settled actuals ($1 > $0.50 > $0.40): %+v", l.Labs)
	}
	if l.Accuracy.Labs != 1 || !closeTo(l.Accuracy.EstimateUSD, 2) || !closeTo(l.Accuracy.ActualUSD, 0.4) {
		t.Fatalf("estimate vs actual over settled labs: %+v", l.Accuracy)
	}
	if len(l.TopSpenders) != 1 || l.TopSpenders[0].Requester != "trainee@crucible.local" || !closeTo(l.TopSpenders[0].USD, 1.9) {
		t.Fatalf("top spenders: %+v", l.TopSpenders)
	}
	today := l.Daily[len(l.Daily)-1]
	if today.Day != now.Format(time.DateOnly) || !closeTo(today.ActualUSD, 0.4) || len(l.Daily) != now.Day() {
		t.Fatalf("one entry per day this month, actuals on Cost Explorer's day: %+v", l.Daily)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1`, now.Add(-40*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.s.Ledger(ctx, f.admin); !l.ActualsStale {
		t.Fatal("no successful ingestion for 36 hours: stale")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/rbac/ ./internal/labs/ -run 'SpendTeams|Ledger' -count=1 2>&1 | tail -3`
Expected: FAIL to compile (`SpendTeams`, `Ledger`, `RefreshFinOps` undefined).

- [ ] **Step 3: Implement**

Append to `internal/rbac/rbac.go` (imports `maps` and `slices`):

```go
// SpendTeams lists, sorted, the teams whose lab spend email may see (spec §5.3 "View team spend"): every team for
// admins; otherwise teams they lead, or where they manage or approve any program.
func (c Checker) SpendTeams(email string) []string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(c.P.Teams)) {
		ok := c.IsAdmin(email) || c.Can(email, ViewSpend, id, "", "")
		for tr := range c.P.Teams[id].Programs {
			ok = ok || c.Can(email, ViewSpend, id, tr, "")
		}
		if ok {
			out = append(out, id)
		}
	}
	return out
}
```

Create `internal/labs/ledger.go`:

```go
package labs

import (
	"context"
	"maps"
	"slices"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/rbac"
)

type LedgerTeam struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Spend    Spend           `json:"spend"`
	Programs []LedgerProgram `json:"programs"`
}

type LedgerProgram struct {
	Training string `json:"training"`
	Spend    Spend  `json:"spend"`
}

type LedgerDay struct {
	Day         string  `json:"day"`          // 2026-10-04
	EstimateUSD float64 `json:"estimate_usd"` // estimated cost of the labs started that day
	ActualUSD   float64 `json:"actual_usd"`   // Cost Explorer's cost on that day
}

type LedgerLab struct {
	ID          string     `json:"id"`
	Team        string     `json:"team"`
	Training    string     `json:"training"`
	Module      string     `json:"module"`
	Requester   string     `json:"requester"`
	Runtime     string     `json:"runtime"`
	State       State      `json:"state"`
	HourlyUSD   float64    `json:"hourly_usd"`
	EstimateUSD float64    `json:"estimate_usd"`
	CostUSD     float64    `json:"cost_usd"`             // hourly estimate × time run so far
	ActualUSD   *float64   `json:"actual_usd,omitempty"` // Cost Explorer's total so far
	Settled     bool       `json:"settled"`              // the actual replaces the estimate in spend
	CreatedAt   time.Time  `json:"created_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
}

func (l LedgerLab) usd() float64 {
	if l.Settled {
		return *l.ActualUSD
	}
	return l.CostUSD
}

type Spender struct {
	Requester string  `json:"requester"`
	Team      string  `json:"team"`
	USD       float64 `json:"usd"`
}

type Accuracy struct {
	Labs        int     `json:"labs"`
	EstimateUSD float64 `json:"estimate_usd"`
	ActualUSD   float64 `json:"actual_usd"`
}

type Finding struct {
	Source  string    `json:"source"`
	LabID   string    `json:"lab_id"`
	ARN     string    `json:"arn"`
	Action  string    `json:"action"`
	Detail  string    `json:"detail"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// Ledger is the FinOps page (spec §9.3), scoped to the teams the viewer may see spend for.
type Ledger struct {
	Month        string       `json:"month"`
	AWS          bool         `json:"aws"`
	ActualsAsOf  *time.Time   `json:"actuals_as_of,omitempty"`
	ActualsStale bool         `json:"actuals_stale"`
	ActualsError string       `json:"actuals_error,omitempty"` // admins only
	ReapedAt     *time.Time   `json:"reaped_at,omitempty"`
	CanRefresh   bool         `json:"can_refresh"`
	Teams        []LedgerTeam `json:"teams"`
	Daily        []LedgerDay  `json:"daily"`
	Running      []LedgerLab  `json:"running"`
	Labs         []LedgerLab  `json:"labs"` // this month, most expensive first (at most 100)
	TopSpenders  []Spender    `json:"top_spenders"`
	Accuracy     Accuracy     `json:"accuracy"`
	Findings     []Finding    `json:"findings,omitempty"` // admins only
}

func (s *Service) Ledger(ctx context.Context, u *auth.User) (*Ledger, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	p, c := st.Platform, rbac.Checker{P: st.Platform}
	admin, teams := c.IsAdmin(u.Email), c.SpendTeams(u.Email)
	if len(teams) == 0 {
		return nil, apperr.Wrap(apperr.Forbidden, "the Ledger is for team leaders, program managers, approvers and admins")
	}
	now := s.Now()
	month := monthStart(now)
	l := &Ledger{Month: month.Format("2006-01"), AWS: s.Cloud != nil, CanRefresh: admin && s.Cloud != nil,
		Teams: []LedgerTeam{}, Daily: []LedgerDay{}, Running: []LedgerLab{}, Labs: []LedgerLab{}, TopSpenders: []Spender{}}
	for _, id := range teams {
		t := p.Teams[id]
		sp, err := s.spend(ctx, p, id, "")
		if err != nil {
			return nil, err
		}
		lt := LedgerTeam{ID: id, Name: t.Name, Spend: sp, Programs: []LedgerProgram{}}
		for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
			psp, err := s.spend(ctx, p, id, tr)
			if err != nil {
				return nil, err
			}
			lt.Programs = append(lt.Programs, LedgerProgram{Training: tr, Spend: psp})
		}
		l.Teams = append(l.Teams, lt)
	}

	var ingestOK, ingestErrAt *time.Time
	var ingestErr string
	if err := s.DB.QueryRow(ctx, `SELECT ingest_ok_at, ingest_error, ingest_error_at, reap_ok_at FROM aws_ops`).
		Scan(&ingestOK, &ingestErr, &ingestErrAt, &l.ReapedAt); err != nil {
		return nil, err
	}
	l.ActualsAsOf = ingestOK
	l.ActualsStale = l.AWS && (ingestOK == nil || ingestOK.Before(now.Add(-36*time.Hour)) ||
		(ingestErrAt != nil && ingestErrAt.After(*ingestOK)))
	if admin {
		l.ActualsError = ingestErr
	}

	rows, err := s.DB.Query(ctx, `WITH labs AS (`+labCostSQL+` WHERE l.team = ANY($2) AND l.created_at >= $3)
		SELECT labs.id, labs.team, labs.training, labs.module, coalesce(u.email, ''), labs.runtime, labs.state,
			labs.hourly_usd, labs.estimate_usd, labs.est_spent, labs.actual, labs.settled, labs.created_at, labs.ends_at
		FROM labs LEFT JOIN users u ON u.id = labs.user_id
		WHERE labs.ready_at IS NOT NULL OR labs.state = 'provisioning'`, now, teams, month)
	if err != nil {
		return nil, err
	}
	var all []LedgerLab
	for rows.Next() {
		var x LedgerLab
		if err := rows.Scan(&x.ID, &x.Team, &x.Training, &x.Module, &x.Requester, &x.Runtime, &x.State, &x.HourlyUSD,
			&x.EstimateUSD, &x.CostUSD, &x.ActualUSD, &x.Settled, &x.CreatedAt, &x.EndsAt); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	day := map[string]int{} // "2026-10-04" → index in l.Daily: one entry per day of the month so far
	for d := month; !d.After(now); d = d.AddDate(0, 0, 1) {
		day[d.Format(time.DateOnly)] = len(l.Daily)
		l.Daily = append(l.Daily, LedgerDay{Day: d.Format(time.DateOnly)})
	}
	spenders := map[[2]string]float64{}
	for _, x := range all {
		if x.State == Provisioning || x.State == Ready {
			l.Running = append(l.Running, x)
		}
		if i, ok := day[x.CreatedAt.UTC().Format(time.DateOnly)]; ok {
			l.Daily[i].EstimateUSD += x.CostUSD
		}
		spenders[[2]string{x.Requester, x.Team}] += x.usd()
		if x.Settled {
			l.Accuracy.Labs++
			l.Accuracy.EstimateUSD += x.CostUSD
			l.Accuracy.ActualUSD += *x.ActualUSD
		}
	}
	arows, err := s.DB.Query(ctx, `SELECT c.day, sum(c.usd) FROM cost_actuals c JOIN lab_instances l ON l.id = c.lab_id
		WHERE l.team = ANY($1) AND c.day >= $2 GROUP BY c.day`, teams, month)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var d time.Time
		var usd float64
		if err := arows.Scan(&d, &usd); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := day[d.Format(time.DateOnly)]; ok {
			l.Daily[i].ActualUSD = usd
		}
	}
	arows.Close()

	slices.SortStableFunc(all, func(a, b LedgerLab) int {
		if a.usd() != b.usd() {
			if a.usd() > b.usd() {
				return -1
			}
			return 1
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	l.Labs = append(l.Labs, all[:min(len(all), 100)]...)
	for k, usd := range spenders {
		if usd > 0 {
			l.TopSpenders = append(l.TopSpenders, Spender{Requester: k[0], Team: k[1], USD: usd})
		}
	}
	slices.SortFunc(l.TopSpenders, func(a, b Spender) int {
		if a.USD > b.USD {
			return -1
		}
		if a.USD < b.USD {
			return 1
		}
		return 0
	})
	l.TopSpenders = l.TopSpenders[:min(len(l.TopSpenders), 5)]

	if admin {
		frows, err := s.DB.Query(ctx, `SELECT source, lab_id, arn, action, detail, first_at, last_at FROM reaper_findings
			ORDER BY last_at DESC LIMIT 100`)
		if err != nil {
			return nil, err
		}
		for frows.Next() {
			var x Finding
			if err := frows.Scan(&x.Source, &x.LabID, &x.ARN, &x.Action, &x.Detail, &x.FirstAt, &x.LastAt); err != nil {
				frows.Close()
				return nil, err
			}
			l.Findings = append(l.Findings, x)
		}
		frows.Close()
	}
	return l, nil
}

// RefreshFinOps (admin) ingests costs and runs the reaper now, instead of waiting for the 6-hourly jobs.
func (s *Service) RefreshFinOps(ctx context.Context, u *auth.User) (*Ledger, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "only admins can refresh costs and run the reaper")
	}
	if err := audit.Log(ctx, s.DB, u.Email, "finops.refresh", "", nil, ""); err != nil {
		return nil, err
	}
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	_ = s.IngestCosts(work)
	_ = s.Reap(work)
	return s.Ledger(ctx, u)
}
```

The test expects `TopSpenders[0].USD ≈ 1.9`: $0.40 settled, plus the unreported lab's $1, plus $0.50 so far on the running lab. All three were requested by the trainee in team forge. `Accuracy.EstimateUSD` is `est_spent` of the settled lab: f.spent runs it for one hour at $2/h, so $2.

Add routes in `internal/labs/http.go` (inside `Routes`):

```go
	r.Get("/api/ledger", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Ledger(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/admin/finops/refresh", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.RefreshFinOps(r.Context(), user(r))
		reply(w, v, err)
	})
```

In `internal/httpapi/server.go` `/api/me`, compute `canSpend := false`. Inside the `if st := state(d); …` block, set `canSpend = len(c.SpendTeams(u.Email)) > 0`, and add `"can_view_spend": canSpend` to the JSON map.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/rbac/ ./internal/labs/ ./internal/httpapi/ -count=1 2>&1 | tail -4`
Expected: `ok` ×3.

- [ ] **Step 5: Commit**

```bash
git add internal/rbac internal/labs/ledger.go internal/labs/ledger_test.go internal/labs/http.go internal/httpapi/server.go
git commit -m "feat(finops): Ledger API: spend by team and program, daily estimates vs actuals, running labs, reaper findings

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 11: The Ledger page

**Files:**
- Modify: `web/src/types.ts`
- Create: `web/src/lib/ledger.ts`, `web/src/lib/ledger.test.ts`
- Create: `web/src/pages/Ledger.tsx`
- Modify: `web/src/App.tsx` (route `/ledger`), `web/src/components/Nav.tsx` (link), `web/src/pages/Lab.tsx` (aws destroy screen), `web/src/theme/app.css`

**Interfaces:**
- Consumes: `GET /api/ledger`, `POST /api/admin/finops/refresh`, `/api/me` `can_view_spend` (Task 10).
- Produces the selectors Task 14 relies on:
  - nav link `Ledger`;
  - button `Refresh now` (admins with aws labs);
  - `<meter>` named `<team name> spend`;
  - `data-testid` `ledger-labs`, `ledger-running`, `reaper-findings` and `actuals-status`;
  - lab rows read `<training> / <module>`, and the AWS bill column shows `$0.11 (so far)` until it settles.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/ledger.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { accuracyPct, barHeights, limitOf } from './ledger'

describe('ledger helpers', () => {
  it('measures burn against the hard cap, else the budget', () => {
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 200, cap_usd: 250 })).toBe(250)
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 200, cap_usd: 0 })).toBe(200)
    expect(limitOf({ spent_usd: 1, committed_usd: 1, actual_usd: 0, budget_usd: 0, cap_usd: 0 })).toBe(0)
  })
  it('scales bars to the busiest day and survives an empty month', () => {
    expect(barHeights([{ estimate_usd: 2, actual_usd: 1 }, { estimate_usd: 0, actual_usd: 4 }])).toEqual([
      { estimate: 50, actual: 25 },
      { estimate: 0, actual: 100 },
    ])
    expect(barHeights([{ estimate_usd: 0, actual_usd: 0 }])).toEqual([{ estimate: 0, actual: 0 }])
  })
  it('reports accuracy only once something has settled', () => {
    expect(accuracyPct({ labs: 0, estimate_usd: 0, actual_usd: 0 })).toBeNull()
    expect(accuracyPct({ labs: 2, estimate_usd: 2, actual_usd: 0.4 })).toBe(20)
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/lib/ledger.test.ts 2>&1 | tail -4`
Expected: FAIL (`Failed to resolve import "./ledger"`).

- [ ] **Step 3: Types and helpers**

In `web/src/types.ts`:
- add `actual_usd: number` to `Spend`;
- add `can_view_spend: boolean` to `Me`;
- append:

```ts
export type LedgerLab = {
  id: string; team: string; training: string; module: string; requester: string; runtime: string; state: LabState
  hourly_usd: number; estimate_usd: number; cost_usd: number; actual_usd?: number; settled: boolean; created_at: string; ends_at?: string
}
export type Finding = { source: 'destroy' | 'reaper' | 'trail'; lab_id: string; arn: string; action: 'deleted' | 'failed' | 'reported'; detail: string; first_at: string; last_at: string }
export type Accuracy = { labs: number; estimate_usd: number; actual_usd: number }
export type Ledger = {
  month: string; aws: boolean; actuals_as_of?: string; actuals_stale: boolean; actuals_error?: string; reaped_at?: string; can_refresh: boolean
  teams: { id: string; name: string; spend: Spend; programs: { training: string; spend: Spend }[] }[]
  daily: { day: string; estimate_usd: number; actual_usd: number }[]
  running: LedgerLab[]; labs: LedgerLab[]; top_spenders: { requester: string; team: string; usd: number }[]
  accuracy: Accuracy; findings?: Finding[]
}
```

Create `web/src/lib/ledger.ts`:

```ts
import type { Accuracy, Spend } from '../types'

// What a burn-down bar measures against: the hard cap if there is one, else the budget; 0 = neither.
export const limitOf = (s: Spend): number => s.cap_usd || s.budget_usd

// Bar heights in % of the busiest day, for a CSS bar row (no chart library).
export function barHeights(days: { estimate_usd: number; actual_usd: number }[]): { estimate: number; actual: number }[] {
  const top = Math.max(0, ...days.flatMap((d) => [d.estimate_usd, d.actual_usd]))
  return days.map((d) => (top === 0 ? { estimate: 0, actual: 0 } : {
    estimate: Math.round((d.estimate_usd / top) * 100),
    actual: Math.round((d.actual_usd / top) * 100),
  }))
}

// AWS's bill as a % of the estimate for settled labs; null until one has settled.
export const accuracyPct = (a: Accuracy): number | null =>
  a.labs === 0 || a.estimate_usd <= 0 ? null : Math.round((a.actual_usd / a.estimate_usd) * 100)
```

- [ ] **Step 4: The page**

Create `web/src/pages/Ledger.tsx`:

```tsx
import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Ledger, LedgerLab, Spend } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { usd } from '../lib/money'
import { accuracyPct, barHeights, limitOf } from '../lib/ledger'

function Burn({ label, s }: { label: string; s: Spend }) {
  const limit = limitOf(s)
  return (
    <div className="burn">
      <span>{label}</span>
      {limit > 0 ? <meter aria-label={`${label} spend`} min={0} max={limit} low={limit * 0.8} high={limit} optimum={0} value={s.spent_usd} /> : <span />}
      <span className="muted">
        {usd(s.spent_usd)}{limit > 0 && ` of ${usd(limit)}`} · committed {usd(s.committed_usd)}
        {s.actual_usd > 0 && ` · ${usd(s.actual_usd)} from settled AWS bills`}
      </span>
    </div>
  )
}

function LabRows({ labs, testid }: { labs: LedgerLab[]; testid: string }) {
  return (
    <table className="grid" data-testid={testid}>
      <thead><tr><th>Lab</th><th>Team</th><th>Requested by</th><th>Runtime</th><th>State</th><th>Estimate</th><th>Cost so far</th><th>AWS bill</th></tr></thead>
      <tbody>
        {labs.map((l) => (
          <tr key={l.id}>
            <td>{l.training} / {l.module}</td><td>{l.team}</td><td>{l.requester}</td><td>{l.runtime}</td>
            <td>{l.state}{l.state === 'ready' && l.ends_at ? ` · ends ${new Date(l.ends_at).toLocaleTimeString()}` : ''}</td>
            <td>{usd(l.estimate_usd)}</td><td>{usd(l.cost_usd)}</td>
            <td>{l.actual_usd == null ? '—' : `${usd(l.actual_usd)}${l.settled ? '' : ' (so far)'}`}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function LedgerPage() {
  const { data: l, error, reload } = useFetch<Ledger>('/api/ledger', 60_000)
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!l) return <Loader label="Weighing the ledger…" />
  const refresh = async () => {
    setBusy(true)
    try {
      await api('/api/admin/finops/refresh', { method: 'POST' })
      reload()
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const bars = barHeights(l.daily)
  const acc = accuracyPct(l.accuracy)
  return (
    <section className="page">
      <h1>Ledger</h1>
      <p className="muted">
        Lab spend for {l.month}. Running labs count from their hourly estimate. A finished AWS lab switches to its AWS bill
        once the bill has settled (about two days after the lab ends).
      </p>
      {l.aws && (
        <p role="status" data-testid="actuals-status" className={l.actuals_stale ? 'error' : 'muted'}>
          {l.actuals_as_of ? `AWS costs as of ${new Date(l.actuals_as_of).toLocaleString()} (AWS reports about 24 h late).` : 'No AWS costs have been read yet.'}
          {l.actuals_stale && ' Actuals are stale; estimates keep enforcing the budgets.'}
          {l.actuals_error && ` Last error: ${l.actuals_error}`}
        </p>
      )}
      {l.can_refresh && <button onClick={refresh} disabled={busy}>{busy ? 'Refreshing…' : 'Refresh now'}</button>}

      <h2>Budgets</h2>
      {l.teams.map((t) => (
        <div key={t.id} className="card">
          <Burn label={t.name} s={t.spend} />
          {t.programs.map((p) => <Burn key={p.training} label={`${t.name} / ${p.training}`} s={p.spend} />)}
        </div>
      ))}

      <h2>Day by day</h2>
      <p className="muted legend"><span className="swatch est" /> estimate (by start day) <span className="swatch act" /> AWS bill</p>
      <div className="bars" role="img" aria-label="Daily lab spend this month; the numbers are in the table below">
        {l.daily.map((d, i) => (
          <div key={d.day} className="bar-day" title={`${d.day}: estimate ${usd(d.estimate_usd)}, AWS ${usd(d.actual_usd)}`}>
            <span className="bar est" style={{ height: `${bars[i].estimate}%` }} />
            <span className="bar act" style={{ height: `${bars[i].actual}%` }} />
          </div>
        ))}
      </div>
      <details>
        <summary>Numbers</summary>
        <table className="grid">
          <thead><tr><th>Day</th><th>Estimate</th><th>AWS bill</th></tr></thead>
          <tbody>{l.daily.map((d) => <tr key={d.day}><td>{d.day}</td><td>{usd(d.estimate_usd)}</td><td>{usd(d.actual_usd)}</td></tr>)}</tbody>
        </table>
      </details>

      <h2>Running now</h2>
      {l.running.length === 0 ? <p className="muted">No labs are running.</p> : <LabRows labs={l.running} testid="ledger-running" />}

      <h2>Labs this month</h2>
      {l.labs.length === 0 ? <p className="muted">No lab has run this month.</p> : <LabRows labs={l.labs} testid="ledger-labs" />}

      <h2>Top spenders</h2>
      {l.top_spenders.length === 0 ? <p className="muted">Nothing spent yet.</p> : (
        <table className="grid">
          <thead><tr><th>Requested by</th><th>Team</th><th>This month</th></tr></thead>
          <tbody>{l.top_spenders.map((s) => <tr key={s.requester + s.team}><td>{s.requester}</td><td>{s.team}</td><td>{usd(s.usd)}</td></tr>)}</tbody>
        </table>
      )}

      <h2>Estimate vs actual</h2>
      <p>{acc === null ? 'No finished AWS lab has a settled bill yet.'
        : `${l.accuracy.labs} settled lab(s): AWS billed ${usd(l.accuracy.actual_usd)} against ${usd(l.accuracy.estimate_usd)} estimated (${acc}%).`}</p>

      {l.can_refresh && (
        <>
          <h2>Reaper findings</h2>
          <p className="muted">{l.reaped_at ? `Last reaper run: ${new Date(l.reaped_at).toLocaleString()}.` : 'The reaper has not run yet.'}</p>
          <table className="grid" data-testid="reaper-findings">
            <thead><tr><th>Last seen</th><th>Found by</th><th>Lab</th><th>Resource</th><th>Action</th><th>Detail</th></tr></thead>
            <tbody>
              {(l.findings ?? []).map((f) => (
                <tr key={f.source + f.arn}>
                  <td>{new Date(f.last_at).toLocaleString()}</td><td>{{ destroy: 'end-of-lab sweep', reaper: 'reaper', trail: 'CloudTrail' }[f.source]}</td>
                  <td>{f.lab_id}</td><td><code>{f.arn}</code></td><td className={f.action === 'deleted' ? 'pass' : 'error'}>{f.action}</td><td>{f.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </section>
  )
}
```

Wire it up:
- In `App.tsx`, import `LedgerPage` and add `<Route path="/ledger" element={<LedgerPage />} />` next to `/approvals`.
- In `Nav.tsx`, add `{me.can_view_spend && <NavLink to="/ledger">Ledger</NavLink>}` after Approvals. Spec §12's order is Hearth · Trainings · Labs · Anvil · Ledger · Forge Status · Team.
- In `Lab.tsx`, before the line that renders `LabWorkspace` for `ready`/`destroying`, add `if (lab?.state === 'destroying' && lab.runtime === 'aws') return <Loader label="Quenching: terraform is tearing down your cloud resources…" />`. The existing poll keeps running every 5 s until the lab is `destroyed`, then the "cooled" summary shows.

Append to `web/src/theme/app.css`:

```css
.burn { display: grid; grid-template-columns: minmax(10rem, max-content) minmax(8rem, 1fr) max-content; gap: 0.75rem; align-items: center; margin: 0.25rem 0; }
.burn meter { width: 100%; }
.bars { display: flex; align-items: flex-end; gap: 3px; height: 120px; padding: 0.5rem 0; border-bottom: 1px solid var(--border); }
.bar-day { flex: 1; display: flex; align-items: flex-end; gap: 1px; height: 100%; }
.bar { flex: 1; min-height: 1px; border-radius: 2px 2px 0 0; }
.bar.est, .swatch.est { background: var(--accent); opacity: 0.55; }
.bar.act, .swatch.act { background: var(--accent-2); }
.swatch { display: inline-block; width: 0.8em; height: 0.8em; border-radius: 2px; vertical-align: middle; }
```

No motion is added, so `useCalm` is unaffected. The bars are colour plus height, and the same numbers are in the table, so the page works without colour.

- [ ] **Step 5: Run the checks**

Run: `cd web && npm test 2>&1 | tail -4 && npm run build 2>&1 | tail -2 && npm run lint 2>&1 | tail -2`
Expected: Vitest passes, including `ledger helpers`. `tsc -b && vite build` succeeds and oxlint is clean.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): Ledger: budget burn, daily estimates vs AWS bills, running labs, top spenders, reaper findings

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Image and chart: infracost in the image; aws labs values, env, RBAC and admission

**Files:**
- Modify: `Dockerfile`
- Modify: `deploy/helm/crucible/values.yaml`
- Modify: `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/crucible/templates/rbac.yaml`
- Modify: `deploy/helm/test.sh`

**Interfaces:**
- Consumes: the environment variables from Task 8.
- Produces (Tasks 13, 14):
  - Helm values `awsLabs.{enabled,labRoleArn,opsRoleArn,stateBucket,stateRegion,regions,workspaceImage,terraformImage}`;
  - optional secret key `INFRACOST_API_KEY` in `crucible-secrets`.

- [ ] **Step 1: Write the failing chart assertions**

Append to `deploy/helm/test.sh`, before `echo "helm chart OK"`:

```bash
# AWS labs (M6): off by default; when on, Crucible may write the credentials secret but never read any secret.
if grep -q 'CRUCIBLE_AWS_LABS' <<<"$out"; then echo "aws labs must be opt-in"; exit 1; fi
aws="$base --set awsLabs.enabled=true --set awsLabs.labRoleArn=arn:aws:iam::1:role/l --set awsLabs.opsRoleArn=arn:aws:iam::1:role/o --set awsLabs.stateBucket=sb"
awsout=$(helm template t "$chart" $aws)
for want in 'name: CRUCIBLE_AWS_LABS' 'name: CRUCIBLE_AWS_LAB_ROLE_ARN' 'name: CRUCIBLE_AWS_STATE_BUCKET' 'key: INFRACOST_API_KEY, optional: true' 'name: AWS_REGION'; do
  grep -q -- "$want" <<<"$awsout" || { echo "missing with aws labs: $want"; exit 1; }
done
awsrbac=$(helm template t "$chart" $aws --show-only templates/rbac.yaml)
grep -A1 '^    resources: \[secrets\]' <<<"$awsrbac" | grep -q 'verbs: \[create, update\]' || { echo "secrets: create and update only"; exit 1; }
if grep -A1 'resources: \[secrets\]' <<<"$awsrbac" | grep -qE '\b(get|list|watch)\b'; then echo "crucible must never read secrets"; exit 1; fi
grep -q 'operations: \[CREATE, UPDATE\], resources: \[secrets\]' <<<"$awsrbac" || { echo "the admission policy must confine secret writes to lab namespaces"; exit 1; }
grep -q 'operations: \[DELETE\], resources: \[pods\]' <<<"$awsrbac" || { echo "the admission policy must confine pod deletes to lab namespaces"; exit 1; }
if helm template t "$chart" $aws --set clusterLabs.enabled=false >/dev/null 2>&1; then echo "aws labs need cluster labs"; exit 1; fi
if helm template t "$chart" $base --set awsLabs.enabled=true >/dev/null 2>&1; then echo "aws labs need the lab account's role ARNs"; exit 1; fi
```

Run: `bash deploy/helm/test.sh`
Expected: FAIL (`missing with aws labs: name: CRUCIBLE_AWS_LABS`).

- [ ] **Step 2: Values and the Deployment**

Append to `values.yaml`:

```yaml
awsLabs:
  enabled: false            # runtime: aws labs; needs clusterLabs.enabled and the deploy/aws/labs stack (docs/runbooks/aws.md)
  labRoleArn: ""            # deploy/aws/labs outputs; `crucible aws up` fills these in
  opsRoleArn: ""
  stateBucket: ""
  stateRegion: ""           # default: backup.region
  regions: eu-west-1        # comma-separated; must match the labs stack's allowed_regions
  workspaceImage: amazon/aws-cli:2.27.0
  terraformImage: hashicorp/terraform:1.16.5
```

In `templates/crucible.yaml`, inside the `api` container's `env:` list after the cluster-labs block:

```yaml
            {{- if .Values.awsLabs.enabled }}
            {{- if not .Values.clusterLabs.enabled }}{{ fail "awsLabs.enabled needs clusterLabs.enabled: the workspace and terraform pods run in the cluster" }}{{ end }}
            {{- if or (not .Values.awsLabs.labRoleArn) (not .Values.awsLabs.opsRoleArn) (not .Values.awsLabs.stateBucket) }}{{ fail "awsLabs needs labRoleArn, opsRoleArn and stateBucket (the deploy/aws/labs outputs)" }}{{ end }}
            - { name: CRUCIBLE_AWS_LABS, value: "1" }
            - { name: CRUCIBLE_AWS_LAB_ROLE_ARN, value: {{ .Values.awsLabs.labRoleArn | quote }} }
            - { name: CRUCIBLE_AWS_OPS_ROLE_ARN, value: {{ .Values.awsLabs.opsRoleArn | quote }} }
            - { name: CRUCIBLE_AWS_STATE_BUCKET, value: {{ .Values.awsLabs.stateBucket | quote }} }
            - { name: CRUCIBLE_AWS_STATE_REGION, value: {{ .Values.awsLabs.stateRegion | default .Values.backup.region | quote }} }
            - { name: CRUCIBLE_AWS_LAB_REGIONS, value: {{ .Values.awsLabs.regions | quote }} }
            - { name: CRUCIBLE_AWS_WORKSPACE_IMAGE, value: {{ .Values.awsLabs.workspaceImage | quote }} }
            - { name: CRUCIBLE_TERRAFORM_IMAGE, value: {{ .Values.awsLabs.terraformImage | quote }} }
            - { name: AWS_REGION, value: {{ .Values.backup.region | quote }} }   # STS regional endpoint (skip if M5 already sets AWS_REGION)
            - name: INFRACOST_API_KEY
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: INFRACOST_API_KEY, optional: true } }
            {{- end }}
```

The `fail` calls live here, not in `rbac.yaml`. Helm evaluates every template even with `--show-only`, which is why `scripts/cluster-check.sh` (Task 14) passes placeholder ARNs.

- [ ] **Step 3: RBAC and admission**

In `templates/rbac.yaml`, ClusterRole `crucible-labs`:
- change the pods rule to `verbs: [get, create{{ if .Values.awsLabs.enabled }}, delete{{ end }}]` and add `# aws labs: delete stops a running terraform apply before destroy`;
- after the `networkpolicies` rule, add:

```yaml
  {{- if .Values.awsLabs.enabled }}
  - apiGroups: [""]
    resources: [secrets]
    verbs: [create, update]       # aws labs: write the lab's credentials file; Crucible can never read a secret back
  - apiGroups: [""]
    resources: [configmaps]
    verbs: [create]               # aws labs: the terraform module
  {{- end }}
```

In the `ValidatingAdmissionPolicy` `resourceRules`, add:

```yaml
      {{- if .Values.awsLabs.enabled }}
      - { apiGroups: [""], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [secrets] }
      - { apiGroups: [""], apiVersions: [v1], operations: [CREATE], resources: [configmaps] }
      - { apiGroups: [""], apiVersions: [v1], operations: [DELETE], resources: [pods] }
      {{- end }}
```

The existing second validation ("only inside lab namespaces") already covers them. `variables.target` uses `oldObject` for UPDATE and DELETE.

- [ ] **Step 4: infracost in the image**

In `Dockerfile`, add a stage before the final one:

```dockerfile
FROM alpine:3.22 AS infracost
ARG TARGETARCH=amd64
ARG INFRACOST_VERSION=v0.10.41
RUN apk add --no-cache curl \
 && base="https://github.com/infracost/infracost/releases/download/${INFRACOST_VERSION}/infracost-linux-${TARGETARCH}.tar.gz" \
 && curl -fsSL "$base" -o /tmp/i.tgz && curl -fsSL "$base.sha256" -o /tmp/i.sha256 \
 && echo "$(cut -d' ' -f1 /tmp/i.sha256)  /tmp/i.tgz" | sha256sum -c - \
 && tar -xzf /tmp/i.tgz -C /tmp && install -m 0755 "/tmp/infracost-linux-${TARGETARCH}" /usr/local/bin/infracost
```

and in the final stage add `COPY --from=infracost /usr/local/bin/infracost /usr/local/bin/` next to the other `COPY --from` lines, plus `INFRACOST_SKIP_UPDATE_CHECK=true` in its `ENV`. If `v0.10.41` or its `.sha256` asset is missing on the releases page, pin the newest `v0.10.x` that ships one. Do not drop the checksum.

- [ ] **Step 5: Run the checks**

Run: `bash deploy/helm/test.sh && docker build -q -t crucible-m6-check . && docker run --rm --entrypoint infracost crucible-m6-check --version`
Expected: `helm chart OK`, then the image id, then `Infracost v0.10.41`. The build only downloads from GitHub, not AWS.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile deploy/helm
git commit -m "feat(helm): aws labs values, env and least-privilege RBAC (write-only secrets); infracost in the image

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: The shared lab account stack, node wiring, `crucible aws labs-init`, and the runbook

**Files:**
- Create: `deploy/aws/labs/main.tf`, `deploy/aws/labs/labs.tftest.hcl`
- Modify: `deploy/aws/main/{variables.tf,main.tf,bootstrap.sh.tftpl,main.tftest.hcl,terraform.tfvars.example}`
- Modify: `internal/awsops/awsops.go`, `internal/awsops/awsops_test.go`, `cmd/crucible/main.go`
- Modify: `docs/runbooks/aws.md`

**Interfaces:**
- Consumes: the Helm values from Task 12.
- Produces:
  - `deploy/aws/labs` outputs: `lab_role_arn`, `ops_role_arn`, `state_bucket`, `state_region`, `regions` (comma-separated).
  - Main-stack variables: `lab_role_arn`, `lab_ops_role_arn`, `lab_state_bucket`, `lab_state_region`, `lab_regions`, `infracost_api_key`.
  - SSM parameter `/<name>/helm-values` (YAML).
  - `(awsops.Ops).LabsInit(ctx, region, crucibleAccount string) error`, and the CLI `crucible aws labs-init --region R [--crucible-account ID]`.

- [ ] **Step 1: Write the failing Terraform tests**

Create `deploy/aws/labs/labs.tftest.hcl`:

```hcl
mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "444455556666" }
  }
}

variables {
  region = "eu-west-1"
}

run "lab_role_is_bounded_tagged_and_short_lived" {
  command = apply # mock provider only: computed ARNs get fake values, nothing reaches AWS

  assert {
    condition     = aws_iam_role.lab.max_session_duration == 3600 && aws_iam_role.lab.permissions_boundary == aws_iam_policy.boundary.arn
    error_message = "one-hour sessions inside the permission boundary"
  }
  assert {
    condition = jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.ArnEquals["aws:PrincipalArn"] == "arn:aws:iam::444455556666:role/crucible-node" && jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.StringLike["aws:RequestTag/crucible:lab-id"] == "?*"
    error_message = "only Crucible's node role may assume it, and only with a lab-id session tag"
  }
  assert {
    condition = alltrue([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s.Condition.StringEquals["aws:RequestTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}" if s.Sid == "CreateTagged"])
    error_message = "creates must carry the session's own lab id"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "ManageOwn"]).Condition.StringEquals["aws:ResourceTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}"
    error_message = "modify/delete only resources tagged with the session's lab id"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "KeepCrucibleTags"]).Effect == "Deny"
    error_message = "crucible:* tags cannot be changed after create"
  }
  assert {
    condition     = contains(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "OwnState"]).Resource, "arn:aws:s3:::crucible-444455556666-labstate/labs/$${aws:PrincipalTag/crucible:lab-id}.tfstate")
    error_message = "a lab reads and writes only its own terraform state"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "OnlyLabRegions"]).Condition.StringNotEquals["aws:RequestedRegion"] == ["eu-west-1"]
    error_message = "labs stay in the allowed regions"
  }
  assert {
    condition     = contains(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "SmallInstancesOnly"]).Condition.StringNotEquals["ec2:InstanceType"], "t3.micro")
    error_message = "only small instance types"
  }
  assert {
    condition     = !anytrue([for s in concat(jsondecode(aws_iam_role_policy.lab.policy).Statement, jsondecode(aws_iam_policy.boundary.policy).Statement) : anytrue([for a in flatten([lookup(s, "Action", [])]) : startswith(a, "iam:")])])
    error_message = "no IAM for lab roles"
  }
  assert {
    condition     = toset(jsondecode(aws_iam_role_policy.ops.policy).Statement[0].Action) == toset(["tag:GetResources", "ce:GetCostAndUsage", "cloudtrail:LookupEvents"])
    error_message = "the ops role only reads the inventory, costs and CloudTrail"
  }
  assert {
    condition     = aws_s3_account_public_access_block.labs.block_public_policy && aws_s3_account_public_access_block.labs.restrict_public_buckets
    error_message = "lab buckets can never be made public"
  }
  assert {
    condition     = aws_s3_bucket.state.bucket == "crucible-444455556666-labstate" && aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled"
    error_message = "versioned lab state bucket"
  }
}

run "a_separate_lab_account_trusts_the_crucible_account" {
  command = plan
  variables {
    crucible_account_id = "111122223333"
    allowed_regions     = ["eu-west-1", "eu-central-1"]
  }
  assert {
    condition     = jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Principal.AWS == "arn:aws:iam::111122223333:root" && jsondecode(aws_iam_role.lab.assume_role_policy).Statement[0].Condition.ArnEquals["aws:PrincipalArn"] == "arn:aws:iam::111122223333:role/crucible-node"
    error_message = "cross-account trust names the Crucible node role"
  }
  assert {
    condition     = output.regions == "eu-west-1,eu-central-1"
    error_message = "regions output feeds CRUCIBLE_AWS_LAB_REGIONS"
  }
}
```

Append to `deploy/aws/main/main.tftest.hcl`:

```hcl
run "aws_labs_off_by_default" {
  command = plan

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.enabled == false
    error_message = "aws labs stay off until the labs stack exists"
  }
  assert {
    condition     = !anytrue([for s in data.aws_iam_policy_document.node.statement : contains(s.actions, "sts:AssumeRole")])
    error_message = "the node assumes no lab role by default"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "/helm-values") && strcontains(aws_instance.node.user_data, "-f \"$work/values.yaml\"") && strcontains(aws_instance.node.user_data, "INFRACOST_API_KEY")
    error_message = "deploy.sh passes the helm values from SSM and the infracost key"
  }
}

run "aws_labs_wiring" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "eu-west-1"
    lab_regions       = "eu-west-1"
    infracost_api_key = "ico-test"
  }

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs == { enabled = true, labRoleArn = "arn:aws:iam::444455556666:role/crucible-lab", opsRoleArn = "arn:aws:iam::444455556666:role/crucible-lab-ops", stateBucket = "crucible-444455556666-labstate", stateRegion = "eu-west-1", regions = "eu-west-1" }
    error_message = "helm values carry the labs stack outputs"
  }
  assert {
    condition     = toset(one([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "sts:TagSession")])) == toset(["arn:aws:iam::444455556666:role/crucible-lab", "arn:aws:iam::444455556666:role/crucible-lab-ops"])
    error_message = "the node may assume exactly the two lab account roles"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "ico-test") && aws_ssm_parameter.secret["infracost_api_key"].type == "SecureString"
    error_message = "the infracost key is a SecureString, never in user data"
  }
}
```

Run: `cd deploy/aws/labs && terraform init -backend=false >/dev/null; terraform test; cd ../main && terraform init -backend=false >/dev/null && terraform test`
Expected: FAIL. `deploy/aws/labs` has no configuration, and `aws_ssm_parameter.helm_values` is not declared. (`terraform init -backend=false` downloads the provider from the Terraform registry, not AWS. `terraform test` with `mock_provider` never calls AWS.)

- [ ] **Step 2: The labs stack**

Create `deploy/aws/labs/main.tf`:

```hcl
# The shared AWS lab account (spec §2, §8.2). Apply once with `crucible aws labs-init` in the account labs run in
# (the Crucible account by default; a dedicated sandbox account is safer: set crucible_account_id). Local state,
# like deploy/aws/persistent: back it up. Never destroyed by `crucible aws teardown`.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { app = "crucible" } }
}

variable "region" {
  type        = string
  description = "Region of the lab state bucket; also the only lab region unless allowed_regions is set"
}
variable "name" {
  type    = string
  default = "crucible"
}
variable "crucible_account_id" {
  type        = string
  default     = ""
  description = "Account whose <name>-node role runs Crucible. Empty = this account."
}
variable "allowed_regions" {
  type    = list(string)
  default = []
}
variable "allowed_instance_types" {
  type    = list(string)
  default = ["t3.nano", "t3.micro", "t3a.nano", "t3a.micro", "t4g.nano", "t4g.micro"]
}

data "aws_caller_identity" "me" {}

locals {
  account   = data.aws_caller_identity.me.account_id
  crucible  = var.crucible_account_id == "" ? local.account : var.crucible_account_id
  node_role = "arn:aws:iam::${local.crucible}:role/${var.name}-node"
  regions   = length(var.allowed_regions) == 0 ? [var.region] : var.allowed_regions
  lab       = "$${aws:PrincipalTag/crucible:lab-id}" # an IAM policy variable, not a Terraform one
  state     = "arn:aws:s3:::${aws_s3_bucket.state.bucket}"
  creates   = ["RunInstances", "CreateVolume", "CreateSecurityGroup", "CreateNetworkInterface"]
  # Trust the node role by ARN through the account root, so the trust works before the role exists.
  trust = { Effect = "Allow", Principal = { AWS = "arn:aws:iam::${local.crucible}:root" } }
}

resource "aws_s3_account_public_access_block" "labs" {
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket" "state" {
  bucket = "${var.name}-${local.account}-labstate"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "prune-old-state"
    status = "Enabled"
    filter { prefix = "labs/" }
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
}

# The ceiling for every lab session: EC2 and S3 only, allowed regions only, small instances only.
resource "aws_iam_policy" "boundary" {
  name = "${var.name}-lab-boundary"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Ceiling", Effect = "Allow", Action = ["ec2:*", "s3:*", "sts:GetCallerIdentity", "tag:GetResources"], Resource = "*" },
      { Sid = "OnlyLabRegions", Effect = "Deny", NotAction = ["sts:GetCallerIdentity", "s3:ListAllMyBuckets", "s3:GetBucketLocation"],
      Resource = "*", Condition = { StringNotEquals = { "aws:RequestedRegion" = local.regions } } },
      { Sid = "SmallInstancesOnly", Effect = "Deny", Action = "ec2:RunInstances", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringNotEquals = { "ec2:InstanceType" = var.allowed_instance_types } } },
    ]
  })
}

resource "aws_iam_role" "lab" {
  name                 = "${var.name}-lab"
  max_session_duration = 3600
  permissions_boundary = aws_iam_policy.boundary.arn
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [merge(local.trust, {
      Action    = ["sts:AssumeRole", "sts:TagSession"]
      Condition = { ArnEquals = { "aws:PrincipalArn" = local.node_role }, StringLike = { "aws:RequestTag/crucible:lab-id" = "?*" } }
    })]
  })
}

# ABAC (spec §8.2): creates must carry the session's lab id; modify/delete only what carries it.
resource "aws_iam_role_policy" "lab" {
  role = aws_iam_role.lab.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Read", Effect = "Allow", Resource = "*",
      Action = ["ec2:Describe*", "s3:ListAllMyBuckets", "s3:GetBucketLocation", "sts:GetCallerIdentity", "tag:GetResources"] },
      { Sid = "CreateTagged", Effect = "Allow", Action = [for a in local.creates : "ec2:${a}"],
        Resource  = ["arn:aws:ec2:*:*:instance/*", "arn:aws:ec2:*:*:volume/*", "arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"],
      Condition = { StringEquals = { "aws:RequestTag/crucible:lab-id" = local.lab } } },
      { Sid = "LaunchFromShared", Effect = "Allow", Action = "ec2:RunInstances",
      Resource = ["arn:aws:ec2:*::image/*", "arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:key-pair/*", "arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:network-interface/*"] },
      { Sid = "CreateInShared", Effect = "Allow", Action = ["ec2:CreateSecurityGroup", "ec2:CreateNetworkInterface"],
      Resource = ["arn:aws:ec2:*:*:vpc/*", "arn:aws:ec2:*:*:subnet/*"] },
      { Sid = "TagOnCreate", Effect = "Allow", Action = "ec2:CreateTags", Resource = "*",
      Condition = { StringEquals = { "ec2:CreateAction" = local.creates } } },
      { Sid = "ManageOwn", Effect = "Allow", Resource = "*",
        Action = ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances", "ec2:RebootInstances", "ec2:DeleteVolume",
          "ec2:AttachVolume", "ec2:DetachVolume", "ec2:DeleteSecurityGroup", "ec2:AuthorizeSecurityGroupIngress",
          "ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupIngress", "ec2:RevokeSecurityGroupEgress",
        "ec2:ModifySecurityGroupRules", "ec2:DeleteNetworkInterface", "ec2:CreateTags", "ec2:DeleteTags"],
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "KeepCrucibleTags", Effect = "Deny", Action = ["ec2:CreateTags", "ec2:DeleteTags"], Resource = "*",
      Condition = { "ForAnyValue:StringLike" = { "aws:TagKeys" = ["crucible:*"] }, Null = { "ec2:CreateAction" = "true" } } },
      { Sid = "OwnBuckets", Effect = "Allow", Action = "s3:*",
      Resource = ["arn:aws:s3:::crucible-lab-${local.lab}*", "arn:aws:s3:::crucible-lab-${local.lab}*/*"] },
      { Sid = "OwnState", Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      Resource = ["${local.state}/labs/${local.lab}.tfstate", "${local.state}/labs/${local.lab}.tfstate.tflock"] },
      { Sid = "ListOwnState", Effect = "Allow", Action = "s3:ListBucket", Resource = local.state,
      Condition = { StringLike = { "s3:prefix" = "labs/${local.lab}.tfstate*" } } },
    ]
  })
}

resource "aws_iam_role" "ops" {
  name = "${var.name}-lab-ops"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [merge(local.trust, { Action = "sts:AssumeRole", Condition = { ArnEquals = { "aws:PrincipalArn" = local.node_role } } })]
  })
}

resource "aws_iam_role_policy" "ops" {
  role = aws_iam_role.ops.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["tag:GetResources", "ce:GetCostAndUsage", "cloudtrail:LookupEvents"], Resource = "*" }]
  })
}

output "lab_role_arn" { value = aws_iam_role.lab.arn }
output "ops_role_arn" { value = aws_iam_role.ops.arn }
output "state_bucket" { value = aws_s3_bucket.state.bucket }
output "state_region" { value = var.region }
output "regions" { value = join(",", local.regions) }
```

In HCL, `"crucible-lab-${local.lab}*"` renders `crucible-lab-${aws:PrincipalTag/crucible:lab-id}*`, because `local.lab` holds the escaped `$${…}`. The tests compare against `"$${aws:PrincipalTag/crucible:lab-id}"` for the same reason. `jsonencode` accepts statements with different keys, and the tests filter by `Sid` before they read `Condition`, so a statement without one is never dereferenced.

- [ ] **Step 3: The main stack**

`deploy/aws/main/variables.tf`: append

```hcl
# Set by `crucible aws up` from the deploy/aws/labs outputs; empty = aws labs off.
variable "lab_role_arn" {
  type    = string
  default = ""
}
variable "lab_ops_role_arn" {
  type    = string
  default = ""
}
variable "lab_state_bucket" {
  type    = string
  default = ""
}
variable "lab_state_region" {
  type    = string
  default = ""
}
variable "lab_regions" {
  type        = string
  default     = ""
  description = "Comma-separated regions aws labs may use (the labs stack's allowed_regions)"
}
variable "infracost_api_key" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Free key from `infracost auth login`. Without it aws labs cannot be priced, so they cannot be requested."
}
```

`deploy/aws/main/main.tf`:
- in `locals.params` add `infracost_api_key = var.infracost_api_key == "" ? "none" : var.infracost_api_key`, and add `"infracost_api_key"` to the `aws_ssm_parameter.secret` `toset([...])`;
- in `data "aws_iam_policy_document" "node"` add:

```hcl
  dynamic "statement" {
    for_each = var.lab_role_arn == "" ? [] : [1]
    content {
      actions   = ["sts:AssumeRole", "sts:TagSession"] # lab sessions carry crucible:* tags
      resources = [var.lab_role_arn, var.lab_ops_role_arn]
    }
  }
```

- add:

```hcl
# Helm values deploy.sh passes with -f. Changing them needs only `crucible aws up`, no node rebuild.
resource "aws_ssm_parameter" "helm_values" {
  name = "/${var.name}/helm-values"
  type = "String"
  value = yamlencode({
    awsLabs = {
      enabled     = var.lab_role_arn != ""
      labRoleArn  = var.lab_role_arn
      opsRoleArn  = var.lab_ops_role_arn
      stateBucket = var.lab_state_bucket
      stateRegion = var.lab_state_region
      regions     = var.lab_regions == "" ? var.region : var.lab_regions
    }
  })
}
```

- add `aws_ssm_parameter.helm_values` to `aws_instance.node`'s `depends_on`.

`deploy/aws/main/bootstrap.sh.tftpl`, inside `deploy.sh`:
- after `aws s3 cp … chart.tgz …`, add `aws ssm get-parameter --region "$REGION" --name "/$NAME/helm-values" --query Parameter.Value --output text > "$work/values.yaml"`;
- before the `kubectl -n crucible create secret generic …` line, add `infracost_key=$(param infracost_api_key); [ "$infracost_key" = none ] && infracost_key=""`;
- add `--from-literal=INFRACOST_API_KEY="$infracost_key" \` to that secret;
- add `-f "$work/values.yaml" \` to the `helm upgrade --install` line, before `--wait`.

`deploy.sh` lives in `user_data`, so **existing nodes need a rebuild** (teardown → up), as M4's sysbox did. Task 13 Step 5 puts this in the runbook.

`terraform.tfvars.example`: add a commented `# infracost_api_key = "ico-…"   # aws labs: free key from infracost auth login`.

- [ ] **Step 4: `crucible aws labs-init` and labs outputs into `up`**

Append to `internal/awsops/awsops_test.go`:

```go
func TestLabsInitAndUpWiresTheLabAccount(t *testing.T) {
	labs := `{"lab_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab"},"ops_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab-ops"},"state_bucket":{"value":"crucible-444455556666-labstate"},"state_region":{"value":"eu-west-1"},"regions":{"value":"eu-west-1"}}`
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return labs
		}
		return fakeAWS(cmd)
	}}
	o := r.ops(t.TempDir())
	if err := o.LabsInit(context.Background(), "eu-west-1", "111122223333"); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "labs apply -var region=eu-west-1 -var crucible_account_id=111122223333") < 0 {
		t.Fatalf("labs-init apply:\n%s", strings.Join(r.calls, "\n"))
	}
	r.calls = nil
	if err := o.Up(context.Background(), "x.tfvars", false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-var lab_role_arn=arn:aws:iam::444455556666:role/crucible-lab", "-var lab_ops_role_arn=arn:aws:iam::444455556666:role/crucible-lab-ops",
		"-var lab_state_bucket=crucible-444455556666-labstate", "-var lab_state_region=eu-west-1", "-var lab_regions=eu-west-1"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}
```

(`TestInitAndUp` keeps passing: its fake `labs output -json` has no `lab_role_arn`, so `up` leaves aws labs off.)

In `internal/awsops/awsops.go`:
- extend the `need`/`first` maps in `outputs` with `"labs": "lab_role_arn"` and `"labs": "labs-init"`;
- in `Up`, replace `mainVars(p)` in the apply call with a `vars` slice built as `vars := mainVars(p); if l, err := o.outputs(ctx, "labs"); err == nil { vars = append(vars, labVars(l)...) }`;
- add:

```go
// LabsInit creates the shared lab account stack (roles, permission boundary, state bucket). Run it with credentials
// for the lab account; pass the Crucible account when that is a different one.
func (o Ops) LabsInit(ctx context.Context, region, crucibleAccount string) error {
	d := o.dir("labs")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	args := []string{"-chdir=" + d, "apply", "-var", "region=" + region}
	if crucibleAccount != "" {
		args = append(args, "-var", "crucible_account_id="+crucibleAccount)
	}
	return o.Exec(ctx, o.Root, "terraform", args...)
}

// labVars wires the shared lab account into the node; without the labs stack, aws labs stay off.
func labVars(l map[string]string) []string {
	return []string{"-var", "lab_role_arn=" + l["lab_role_arn"], "-var", "lab_ops_role_arn=" + l["ops_role_arn"],
		"-var", "lab_state_bucket=" + l["state_bucket"], "-var", "lab_state_region=" + l["state_region"], "-var", "lab_regions=" + l["regions"]}
}
```

In `cmd/crucible/main.go` `awsCmd`:
- add the flag `crucibleAccount := fs.String("crucible-account", "", "labs-init: account id that runs Crucible, when labs live in another account")`;
- add the case `"labs-init"`. It requires `--region` and calls `ops.LabsInit(ctx, *region, *crucibleAccount)`;
- add `crucible aws labs-init --region R [--crucible-account ID]` to `usage`.

Run: `go test ./internal/awsops/ ./cmd/crucible/ -count=1 && (cd deploy/aws/labs && terraform fmt -check && terraform validate && terraform test) && (cd deploy/aws/main && terraform fmt -check && terraform validate && terraform test)`
Expected: `ok` ×2, `Success! The configuration is valid.` ×2, and every terraform run passes (labs: 2 runs; main: the existing runs plus 2).

- [ ] **Step 5: The runbook**

Append to `docs/runbooks/aws.md` a section `## AWS labs (runtime: aws)` with this content:

```markdown
## AWS labs (runtime: aws)

AWS labs run terraform in your **lab account** with one-hour credentials scoped to a single lab (spec §8.2). A
dedicated sandbox account is strongly recommended: the permission boundary limits labs to EC2 and S3, small
instance types and the allowed regions, but shared-account isolation is best-effort.

**Setup (once).**
1. With credentials for the lab account: `go run ./cmd/crucible aws labs-init --region eu-west-1` (add
   `--crucible-account <id>` when the lab account is not the Crucible account). Back up
   `deploy/aws/labs/terraform.tfstate` as you did for the persistent stack. labs-init also turns on account-wide
   S3 Block Public Access in that account.
2. In the **management (payer) account**, open Billing → Cost allocation tags and activate `crucible:lab-id`,
   `crucible:team` and `crucible:training` (they appear after the first lab has run; activation takes up to 24 h
   and is not retroactive unless you request a backfill). Open Cost Explorer once in the lab account so its API is
   enabled. Cost Explorer charges $0.01 per API call; Crucible makes about 4–8 a day.
3. `infracost auth login` (free), then put the key in `crucible.tfvars` as `infracost_api_key`.
4. `crucible aws up` (with Crucible-account credentials). It reads the labs stack's outputs and turns aws labs on.
   **A node built before M6 must be rebuilt** (`crucible aws teardown --yes`, then `crucible aws up`): deploy.sh
   changed and user data never updates in place.

**Verify on a real sandbox (by hand; nothing in `make` touches AWS).** Enrol yourself in Forge 401 (Team page),
request "Cloud Heat", approve it, then:
1. In the workspace terminal: `aws sts get-caller-identity` shows `assumed-role/crucible-lab/crucible-lab-<id>`.
   On the node: `k3s kubectl -n lab-<id> get pods` shows `lab` Running and `tf-apply` Completed.
2. Task 1 and task 2 both pass with **Check** (task 2: `echo hi > f && aws s3 cp f s3://crucible-lab-$CRUCIBLE_LAB_ID/forged.txt`).
3. **The boundary holds.** Each of these must fail with AccessDenied / UnauthorizedOperation:
   - `aws ec2 create-volume --size 1 --availability-zone eu-west-1a` (no lab tag)
   - the same with `--tag-specifications 'ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=000000000000}]'` (someone else's lab id)
   - `aws ec2 run-instances --image-id <any AL2023 AMI> --instance-type t3.large --tag-specifications 'ResourceType=instance,Tags=[{Key=crucible:lab-id,Value=<id>}]'` (instance type)
   - `aws s3 mb s3://not-a-lab-bucket-$RANDOM` (bucket name)
   - `aws s3 ls --region us-east-1` against a bucket in another region, or `aws ec2 describe-vpcs --region us-west-2` if us-west-2 is not allowed
   - `aws s3 cp s3://<labstate bucket>/labs/<another lab id>.tfstate -` (someone else's state)
   - `aws ec2 delete-tags --resources <your volume> --tags Key=crucible:lab-id` (lab tags are immutable)
   - `aws iam list-roles` (no IAM)
   - `curl -m 3 http://169.254.169.254/` (IMDS, from M4's NetworkPolicy)
4. **A tagged leak is swept.** Create a volume the lab owns:
   `aws ec2 create-volume --size 1 --availability-zone eu-west-1a --tag-specifications "ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=$CRUCIBLE_LAB_ID}]"`.
   Click **End lab**. Within ~5 min the lab is "cooled". The Ledger (admin) shows the volume under Reaper findings as
   "end-of-lab sweep / deleted", and `aws resourcegroupstaggingapi get-resources --tag-filters Key=crucible:lab-id,Values=<id>`
   (admin credentials) returns nothing. Terminated instances can stay listed for about an hour; that is AWS.
5. **The reaper.** With admin credentials, create a volume tagged `crucible:lab-id=<id of a lab that ended over an
   hour ago>`, and another tagged with `cccccccccccc`. Ledger → **Refresh now**: the first is deleted, the second is
   reported and still exists (delete it by hand), and admins get one "reaper found" email.
6. **Credentials refresh.** Start a lab with a 2 h TTL (program `lab_defaults.ttl`), and after 65 minutes run
   `aws sts get-caller-identity` in the workspace: it still works.
7. **CloudTrail.** LookupEvents needs no trail (90-day event history). After step 3, Ledger → **Refresh now**: no
   "CloudTrail" finding should appear, because every successful create carried the tag. A finding here means a policy gap.
8. **Costs.** 24–48 h after a lab ends, Ledger → **Refresh now** shows its AWS bill. Once settled, the lab's row has no
   "(so far)" and Estimate vs actual counts it.

Record anything that differed (IAM condition keys, CloudTrail field names, tag propagation to root volumes) in this
section, and fix the code or the labs stack before the release.

**Troubleshooting.**
- A lab sits in "provisioning" and then fails with `terraform apply failed: …`. The trainee sees the log tail. On the node, `k3s kubectl -n lab-<id> get pod tf-apply -o jsonpath='{.status.containerStatuses[0].state.terminated.message}'` shows the same.
- "no cost estimate is available for aws labs" means `INFRACOST_API_KEY` is unset, or infracost cannot reach its pricing API. Check `k3s kubectl -n crucible logs deploy/crucible -c api | grep infracost`.
- If the Ledger says actuals are stale, look at Cost Explorer access (the ops role) and the tag activation (step 2). Budgets keep using estimates meanwhile.
- **Run one Crucible deployment per lab account.** The reaper never deletes resources of labs it does not know. It reports them instead, so a second deployment's labs show up as findings rather than being deleted.
```

- [ ] **Step 6: Commit**

```bash
git add deploy/aws internal/awsops cmd/crucible/main.go docs/runbooks/aws.md
git commit -m "feat(aws): shared lab account stack (bounded, tag-scoped lab role; ops role; state bucket), node wiring, labs-init, runbook

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: End to end: a dry-run aws lab on kind, and the Ledger in local-check

**Files:**
- Modify: `deploy/compose/cluster.yml` (M4)
- Modify: `scripts/cluster-check.sh` (the rbac render enables aws labs)
- Modify: `e2e/playwright.config.ts`
- Create: `e2e/tests/aws-lab.spec.ts`
- Modify: `e2e/tests/approvals.spec.ts` (Ledger smoke)

**Interfaces:**
- Consumes:
  - everything above;
  - M4's `cluster` Playwright project and `CLUSTER=1` switch;
  - the Forge 401 strings (Task 1);
  - the Ledger selectors (Task 11).

- [ ] **Step 1: The dry-run stack on kind**

In `deploy/compose/cluster.yml`, add to `services.api.environment`:

```yaml
      CRUCIBLE_AWS_LABS: dryrun          # aws labs on kind: real workspace + terraform pods, a fake lab account (never AWS)
      CRUCIBLE_INFRACOST: "off"          # price aws labs from CRUCIBLE_DEV_LAB_USD_PER_HOUR
      CRUCIBLE_DEV_LAB_USD_PER_HOUR: "paid-heat=0.5,cloud-heat=0.04"
```

In `scripts/cluster-check.sh` `render()`, add these flags to the `helm template` call, so the API's service account gets the aws-lab RBAC and admission rules:

```bash
    --set awsLabs.enabled=true --set awsLabs.labRoleArn=unused --set awsLabs.opsRoleArn=unused --set awsLabs.stateBucket=unused \
```

In `e2e/playwright.config.ts`, change both project matchers from `/cluster-lab\.spec\.ts/` to `/(cluster|aws)-lab\.spec\.ts/`. `local` uses it as `testIgnore` and `cluster` as `testMatch`.

- [ ] **Step 2: The aws spec**

Create `e2e/tests/aws-lab.spec.ts`:

```ts
import { expect, test, type Browser, type Page } from '@playwright/test'

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test('an aws lab is estimated, approved, provisioned, checked, destroyed and swept; spend shows on the Ledger', async ({ browser }) => {
  // The leader enrols the team and the trainee in Forge 401 (two bot commits), as approvals.spec.ts does for 201.
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await leader.getByLabel('Training to enroll').selectOption('forge-401')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // Estimated ($0.04/h × 1 h in dry-run pricing). AWS labs always need an approver.
  const trainee = await login(browser, 'trainee')
  await expect(trainee.getByRole('link', { name: /Forge 401/ })).toBeVisible({ timeout: 60_000 })
  await trainee.goto('/p/forge/forge-401/m/01-cloud-heat/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.04)' }).click()
  await expect(trainee.getByTestId('request-status')).toContainText('Waiting for approval')

  // Approved.
  await leader.getByRole('link', { name: 'Approvals' }).click()
  const card = leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' })
  await expect(card.getByTestId('estimate')).toContainText('$0.04')
  await card.getByRole('button', { name: 'Approve' }).click()

  // Provisioned: the namespace, the credentials secret, the dind workspace (pulls amazon/aws-cli) and tf-apply.
  await expect(trainee.getByRole('tab', { name: 'workspace', exact: true })).toBeVisible({ timeout: 10 * 60_000 })

  // Checked server-side, in the workspace, with the mounted lab credentials.
  await typeIn(trainee, 'workspace', "aws configure list | awk '/region/ {print $2}' > ~/region.txt")
  await trainee.getByRole('button', { name: 'Check' }).click()
  await expect(trainee.getByText("Your CLI points at eu-west-1 with your lab's credentials.")).toBeVisible()

  // Destroyed: End returns at once; tf-destroy and the tag sweep run in the background.
  await trainee.getByRole('button', { name: 'End lab' }).click()
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 5 * 60_000 })

  // Swept, and on the Ledger: the hand-made volume the dry run left behind, and the lab's (fake) AWS bill.
  const admin = await login(browser, 'admin')
  await admin.getByRole('link', { name: 'Ledger' }).click()
  await admin.getByRole('button', { name: 'Refresh now' }).click()
  const findings = admin.getByTestId('reaper-findings')
  await expect(findings).toContainText(/volume\/vol-[0-9a-f]{12}/)
  await expect(findings).toContainText('end-of-lab sweep')
  await expect(findings).toContainText('deleted')
  const row = admin.getByTestId('ledger-labs').getByRole('row').filter({ hasText: 'forge-401 / 01-cloud-heat' })
  await expect(row).toContainText('$0.11 (so far)')
})
```

- [ ] **Step 3: The Ledger smoke in local-check**

Append inside the test in `e2e/tests/approvals.spec.ts`, after "Resume so the rest of the suite can run labs":

```ts
  // The Ledger shows the leader the team's budget burn and the paid lab (local-check has no AWS: estimates only).
  await leader.getByRole('link', { name: 'Ledger' }).click()
  await expect(leader.getByRole('meter', { name: 'The Forge spend' })).toBeVisible()
  await expect(leader.getByTestId('ledger-labs')).toContainText('forge-201 / 01-paid-lab')
```

- [ ] **Step 4: Run both acceptance checks**

Run:
```bash
KEYCLOAK_PORT=8082 make local-check 2>&1 | tail -6
KEYCLOAK_PORT=8082 make cluster-check 2>&1 | tail -12
```
Expected:
- local-check ends with `🔥 Local check passed. The forge holds.`, and `approvals.spec.ts` passes with the Ledger lines.
- cluster-check shows the kind subtests passing, then Playwright reporting `aws-lab.spec.ts` and `cluster-lab.spec.ts` passed after the `local` project, then `🔥 Cluster check passed. The crucible holds.`

The first cluster run pulls `hashicorp/terraform` into kind and `amazon/aws-cli` inside dind. That can take several minutes on this 8 GiB machine.

If the aws spec fails, rerun with `KEEP=1 KEYCLOAK_PORT=8082 make cluster-check`, then inspect:
- `docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/cluster.yml logs api | grep -iE 'aws|terraform|sweep'`
- `kubectl --context kind-crucible-m4 -n lab-<id> get pods,secrets,configmaps`
- `kubectl … get pod tf-apply -o yaml`

`forbidden … secrets` means the rbac render in `cluster-check.sh` lacks `awsLabs.enabled=true`.

- [ ] **Step 5: Final hygiene**

Run: `gofmt -l . ; go vet ./... ; go vet -tags cluster ./internal/labs/ ; go test -race ./... 2>&1 | grep -v -E '^(ok|\?)' ; bash deploy/helm/test.sh ; (cd web && npm run lint)`
Expected: no output except `helm chart OK` and a clean oxlint summary.

- [ ] **Step 6: Commit**

```bash
git add deploy/compose/cluster.yml scripts/cluster-check.sh e2e
git commit -m "test(e2e): a dry-run aws lab on kind from request to tag sweep; the Ledger in local-check

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage (M6)

| Spec item | Task |
|---|---|
| §2 AWS isolation: one shared account, tag + IAM permission boundary | 13 (boundary, ABAC, immutable tags), 7 (session tags) |
| §3 `AWSRunner` = terraform runner + `ClusterRunner` workspace pod | 6, 7 (one-shot pods instead of Jobs: ruling 2) |
| §4.5 `aws: region, max_hourly_usd` (lint fails over the ceiling) | 1 (schema, module rules), 5 (lint price check, quote block) |
| §8.1 failed → destroy; stuck destroys retried | 8 (aws: 20 min budget, retry after 30 min, async) |
| §8.2 terraform apply with the lab's module, state in S3 keyed by lab id | 1 (injected provider/backend), 6, 7, 13 (state bucket) |
| §8.2 STS AssumeRole, permission boundary, session tag, RequestTag/ResourceTag conditions, `default_tags` | 1 (`LabTF`), 4, 7, 13 |
| §8.2 destroy = terraform destroy, then a tag sweep (Resource Groups Tagging API) | 7, 8 |
| §8.2 nightly reaper; untagged resources created by lab roles (CloudTrail) reported to admins | 9 (every 6 h + on start: ruling 7) |
| §8.5 setups and checks run in the workspace pod with lab credentials | 7 (`RunScript` → workspace service), 14 |
| §8.6 effective end includes the hard budget cap | 2 (`budgetLimit`; the web Timer already labels it) |
| §9.1 aws estimate = infracost on the module × TTL; aws never auto-approves | 5 (existing `rbac.Tier`) |
| §9.3 live spend = estimates; actuals = Cost Explorer daily by `crucible:*` tags, reconciled, lag shown | 2 (settled rule), 9, 10, 11 |
| §9.3 FinOps page: spend by team/training/lab over time, burn-down, running labs with cost and time-to-death, top spenders, estimate vs actual, reaper findings | 10, 11 |
| §12 navigation "Ledger (FinOps)" | 11 |
| §13 `cost_actuals` (`cost_samples`: estimates already on `lab_instances`, ruling 8) | 2 |
| §14 credentials 1 h, auto-refreshed, never in the browser | 7, 8, 12 (write-only secrets RBAC) |
| §14 Cost Explorer unavailable → actuals stale, estimates enforce caps | 2, 9, 10, 11 |
| §14 testing: AWS runner against a sandbox (nightly) / LocalStack | 4 (httptest), 7 (fake clientset + Fake), 14 (dry run on kind), 13 (manual sandbox checklist: ruling 11) |

**Deliberately not in M6 (and where they land):**
- "Extension pending" re-approval (spec §8.6): the M7 coverage pass (ruling 9).
- AWS services beyond EC2 and S3, and IAM inside labs: widen the labs stack policy when a lab needs it (ruling 5).
- Terraform in the trainee's workspace: set `awsLabs.workspaceImage` to an image that has it (open decision 6).
- Untagged spend in the lab account: not stored. Untagged *resources* are the reaper's job.
- A nightly CI job against a real sandbox: no CI exists. Task 13's runbook checklist is the manual equivalent.
- A shared agent hub / second API replica: the per-lab locks, credential expiry and the `once` guard are in-process (`// ponytail:` notes), as in M3/M4.

## Open decisions (defaults applied in this plan)

1. **Runner pods vs `batch/v1` Jobs.** Default: bare one-shot pods. This adds no new API group, needs no `pods list`, and reads the log tail via the termination message. Switch to Jobs only if Kubernetes-level retries or TTL cleanup become wanted.
2. **LocalStack.** Default: none. Fakes plus `httptest` cover our code, and the real-IAM behaviour LocalStack cannot reproduce goes to the sandbox runbook.
3. **Where the AWS e2e runs.** Default: `make cluster-check` (kind, real workspace and runner pods, fake cloud). `local-check` only gains the Ledger smoke test. The alternative is a laptop-agent workspace in local-check, which would test less real code.
4. **"Extension pending".** Default: deferred to M7. Tier-raising extensions stay refused with a clear message.
5. **Lab account.** Default: the Crucible account. A separate account works with one flag (`--crucible-account`), and the runbook recommends one.
6. **Workspace image.** Default: `amazon/aws-cli:2.27.0` (CLI only). A custom image with terraform and kubectl can come later through `awsLabs.workspaceImage`.
7. **Settlement and staleness thresholds.** Defaults: an actual replaces the estimate 48 h after the lab ends, if Cost Explorer reported it; actuals are stale after 36 h without a success.
8. **Without an infracost key.** Default: aws labs cannot be requested. The alternative is a platform rate card fallback.
9. **Fixture placement.** Default: a separate Forge 401. Spec §14 puts the AWS lab in Forge 101, whose linear progression would gate it behind the cluster lab.
