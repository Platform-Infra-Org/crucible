# AWS labs sleep with the platform, and teardown ends them — design

Status: approved in conversation (2026-11-05). Decisions, in the owner's words where given:

- When Crucible sleeps (the nightly schedule or `crucible aws sleep`), running AWS labs are **kept in their state but
  asleep**, so a trainee can carry on the next day: "the lab's state to be as it was last night exactly so", including an
  intentionally corrupted boot and whatever the trainee did to it.
- **Hibernate where the machine can, else stop** (option B): a healthy machine keeps its processes and shells; one that
  can't hibernate (a broken boot has no running OS) keeps its disk exactly.
- **Sleep counts towards the lab's TTL** (wall clock). The idle timeout does not run while asleep.
- A sleeping lab **wakes when its trainee opens it** and presses *Wake it*, not when the platform wakes.
- **Bedtime timeline** as in §2 (closed at `labs_sleep_cron`, 10 minutes to settle, then asleep, before the backup).
- **The workspace comes back too**: its Docker storage and `/lab` live on a persistent volume.
- **Node disk default 100 GB**, with per-workspace and node-wide guards.
- **Teardown ends every lab first** (kill switch), and the server clears a teardown-set switch when it next starts.

Local and cluster labs (on trainees' own machines) are not affected.

## 1. States

`lab_instances.state` gains `asleep` and `waking`:

```
ready --bedtime--> asleep --Wake it--> waking --machines running, workspace back--> ready
asleep --ends_at passed / user ends / kill switch--> destroying
waking --start failed--> asleep (error shown; Wake it again)
```

Every hard-coded "alive" list includes both new states: the reaper's active set and its "ended" exclusion
(`reaper.go`), `reconcileCluster`'s active set (else the sweep deletes the namespace as an orphan), `labCostSQL` and
`spend()` (`finops.go`), `killAll`, the ledger, `mine.go`, and the web's `LabState`, `Labs.tsx` active list and
`Lab.tsx` state lists. A lab row records `slept_at` and, per instance, how it slept (`hibernated` | `stopped`) in a
`lab_events` row (`kind = 'sleep'`).

## 2. Bedtime

A single database row `platform_bedtime (requested_at, phase, by)` is the signal; only the server acts on it.

- **Nightly:** a Helm CronJob `crucible-bedtime` at `labs.sleepSchedule` with `timeZone` (as the backup CronJob)
  runs `crucible labs bedtime` in the server image with the server's database env: it inserts or refreshes the row and
  exits. Main stack variable `labs_sleep_cron` (5-field cron, default 30 minutes before `sleep_cron`, same days; the
  repo's tfvars: `0 19 * * 0-4`), passed to Helm like `backup_cron`. With `schedule_enabled = false` no CronJob is
  rendered.
- **By hand:** `crucible aws sleep` runs, over SSM, `kubectl -n crucible exec deploy/crucible -- crucible labs bedtime
  --wait 20m` before its snapshot. `--wait` polls the row and lab states and exits 0 when the phase is `asleep`, else 1
  listing labs not asleep; `aws sleep` then stops before the snapshot and says so. `--keep-labs` skips the step with a
  warning ("labs keep running and billing while the node sleeps").
- **The server's 15-second sweep** drives the phases:
  1. `closed` (on seeing the row): new lab requests and approvals that would start a lab are refused with
     *"The forge is banking for the night: labs start again in the morning."* Pending approvals stay pending.
  2. `settling`, up to 10 minutes from `requested_at`: labs `provisioning` or `destroying` finish normally. At the end,
     a lab still `provisioning` is destroyed with `end_reason = 'bedtime'` (*"Crucible went to sleep before your lab
     was ready. Start it again in the morning."*).
  3. `asleep`: every `ready` AWS lab is put to sleep (§3). The phase is `asleep` when no AWS lab is `ready`,
     `provisioning`, `waking` or mid-sleep.
- **Waking:** at start, the server deletes the bedtime row (labs may start again). Sleeping labs stay asleep. The
  default `backup_cron` (19:15) runs after a 19:00 bedtime, so the nightly snapshot records labs asleep.

## 3. Sleeping and waking a lab's machines

New `awscloud.Cloud` methods, on the client (EC2 API) and `Fake`:

- `Sleep(ctx, creds, region, instanceIDs) (map[id]mode, error)`: `StopInstances` with `Hibernate=true` per instance;
  on `UnsupportedHibernationConfiguration` (or any hibernation refusal) a plain `StopInstances`; waits until `stopped`;
  an instance still `stopping` after 5 minutes gets `StopInstances --force`.
- `Wake(ctx, creds, region, instanceIDs) error`: `StartInstances`, wait until `running`.

The lab's own role is used (`AssumeLab`); it already allows `ec2:StopInstances` / `StartInstances` on its tagged
instances (`deploy/aws/labs/main.tf`, `ManageOwn`). Instances are found by the `crucible:lab-id` tag, `running` (sleep)
or `stopped` (wake), in the lab's regions.

**Wake it** (`POST /api/labs/{id}/wake`, the lab's owner; refused during bedtime): state `waking` → `Wake` → renew
credentials → workspace back (§4) → `ready`, `last_activity_at = now`. The page then shows, once, *"Your machines resumed
where you left them"* (all hibernated) or *"Your machines restarted from their disks; anything you hadn't saved is
gone"* (any stopped). Readiness is "AWS reports running", never SSM: a broken boot never answers.

**Clocks:** `ends_at` is unchanged by sleep. The sweep ends an `asleep` lab whose `ends_at` passed (destroy runs on
stopped instances; nothing is started). The idle rule skips `asleep` and `waking`. Extend works while asleep, with its
usual rules.

**Money:** the meter prices compute only for intervals the lab was not asleep (disks and IPv4 as today; an automatic
public IPv4 is released on stop, Elastic IPs are denied in labs). Committed spend keeps `hourly × (ends_at − now)`
(overstates a sleeping lab: fails safe). Estimates are unchanged.

## 4. The workspace

- Each AWS lab's namespace gets a PVC `workspace` (StorageClass `local-path`, k3s's default, request 20Gi) holding the
  dind pod's `/var/lib/docker` and `/lab` (sub-paths), in place of the two emptyDirs. It is deleted with the namespace.
  The admission policy and the lab namespace quota allow exactly one such PVC.
- **Wake** deletes the old (failed) pod and creates it again on the same PVC; Docker comes back with its containers and
  Crucible runs `docker start workspace` (never a new `docker run`), so the container's own filesystem — home, files,
  installed tools, pulled images — is kept. Shells and processes in the workspace do not survive (the node was off).
- **Guards:** every 5 minutes the server measures each live or asleep workspace (`du -sx` through the pod, or through a
  short-lived pod on the PVC when asleep); over 20 GiB it warns once on the lab page, and over 20 GiB at the next
  measure the lab is ended (`end_reason = 'workspace_full'`, *"Your workspace grew past 20 GiB, so the lab was put
  out."*). A new AWS lab is refused while the node's free disk (local-path's directory, read through a read-only
  hostPath mount in the server pod) is under 15 GiB (*"The forge's disk is nearly full: try again later, or ask an
  admin."*). `crucible aws status` reports the free space and the number of labs asleep.
- Main stack `disk_gb` default becomes 100.

## 5. Hibernation for lab authors

A lab machine is hibernatable when its terraform sets `hibernation = true` on the `aws_instance` with an encrypted
root (`root_block_device { encrypted = true }`) larger than its RAM, on an instance type and image that support it
(Amazon Linux 2023; Ubuntu with `ec2-hibinit-agent`). Content lint refuses `hibernation = true` without
`encrypted = true` on the root block device. The labs stack's permission boundary and the lab role allow
`kms:CreateGrant`, `kms:Decrypt`, `kms:DescribeKey`, `kms:GenerateDataKeyWithoutPlaintext`, `kms:ReEncrypt*` only with
`kms:ViaService = ec2.<region>.amazonaws.com` (the default `aws/ebs` key). forge-402's broken-SSH lab turns hibernation
on as the worked example; the GRUB lab stays a plain stop.

## 6. Teardown ends every lab

`crucible aws teardown --yes` first runs, over SSM, `kubectl -n crucible exec deploy/crucible -- crucible labs stop-all
--wait 30m`: it turns the kill switch on with `changed_by = 'crucible aws teardown'` (and an audit row), and waits until
no lab is `provisioning`, `ready`, `asleep`, `waking` or `destroying`. If labs remain, teardown stops before the snapshot
and names them. A stopped node is refused (*"wake it first: crucible aws wake"*). `--keep-labs` skips with a warning.
After `terraform destroy`, teardown lists anything still tagged `crucible:lab-id` in the lab regions
(`resourcegroupstaggingapi get-resources`) with the command to remove it; it deletes nothing itself. On start, the
server turns the kill switch off when `changed_by` is `crucible aws teardown`; a switch an admin set stays on.

## 7. UI and docs

- Lab page: an *asleep* panel (how it slept, the deadline counting down, one `button.primary` **Wake it**); *waking*
  uses the forge Loader (*"Stoking the coals: starting your machines…"*); the one-time resumed/restarted banner; during
  bedtime the start button is disabled with the banking line; end messages for `bedtime` and `workspace_full`.
- Labs page and Ledger: an **Asleep** badge in words, the deadline shown.
- `docs/user`: trainees (labs overnight), authors (hibernation in lab terraform), admins (bedtime, the kill switch after
  teardown). Runbook and `aws-deploy.html`: sleep, teardown, `labs_sleep_cron`, `disk_gb`, `--keep-labs`, the by-hand
  checks. `deploy-architecture.html`: an AWS lab's life including sleep. `test-guide.html`: the new items. CLAUDE.md: an
  invariant line.

## 8. Testing (tests first)

- Go with Postgres testcontainers and `awscloud.Fake`: bedtime phases (closed refuses starts; settling waits, then
  cancels a still-provisioning lab; ready labs sleep, hibernation refusal falls back to stop, force-stop after the
  timeout; `--wait` reports stragglers); wake (start, same PVC, `docker start`, idle reset, the banner kind); an asleep
  lab survives the idle rule, the reaper and the orphan cleanup, and ends at `ends_at`; the meter skips compute while
  asleep; the kill switch is cleared at start only when teardown set it; the disk guards refuse and end.
- `awscloud.Client` Sleep/Wake against `httptest`. `awsops`: `sleep` runs bedtime before the snapshot, a failed bedtime
  stops before it, `--keep-labs`; `teardown` ends labs first, refuses a stopped node, reports leftovers (recorder).
- Terraform: `labs_sleep_cron` reaches Helm, `disk_gb` default 100, the boundary's KMS-via-EC2 statement (mock
  providers). Helm: the bedtime CronJob with its timezone, absent when the schedule is off. Content lint: hibernation
  without an encrypted root. Web: render tests for asleep, waking, the badges.
- E2E: on the dry-run AWS lab in `cluster-check`: bedtime → Asleep → Wake it → Ready.
- By hand on our account (runbook): a hibernated lab machine resumes with its shell; a GRUB-broken machine stops and
  wakes still broken; what `StopInstances --hibernate` does to a machine at a GRUB prompt (refusal, stop, or a hang the
  force-stop ends); an encrypted lab disk launches under the boundary.

## 9. Out of scope

Labs surviving a teardown; waking labs automatically with the platform; hibernation for cluster or local labs; a size
quota enforced by the storage itself (local-path has none; the guard measures).
