# AWS labs sleep with the platform Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When Crucible sleeps (the nightly schedule or `crucible aws sleep`), running AWS labs are kept but asleep —
machines hibernated where they can, else stopped, the workspace's disk kept — and each wakes when its trainee presses
*Wake it*; teardown ends every lab first.

**Architecture:** One database row (`platform_bedtime`) is the bedtime signal; a Helm CronJob or `crucible aws sleep`
writes it with `crucible labs bedtime`, and the server's 15-second sweep carries it out (closed to new labs, labs
starting get 10 minutes, then every ready AWS lab sleeps through a new `labSleeper` runner interface). Two new lab
states, `asleep` and `waking`, join every "alive" list. AWS calls go through two new `awscloud.Cloud` methods (`Sleep`,
`Wake`); the workspace moves from emptyDirs to one PVC so it survives. Teardown turns the kill switch on through
`crucible labs stop-all` and the server clears that switch when it next starts.

**Tech Stack:** Go 1.26 (pgx, River, client-go, aws-sdk-go-v2 EC2), Postgres migrations (goose), Helm, Terraform
(mock-provider tests), React 19 + Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-05-labs-sleep-overnight-design.md`

## Global Constraints

- Never create real AWS resources: AWS code is tested against `awscloud.Fake` or `httptest`; Terraform only with mock providers.
- Never touch `~/.kube/config` or the kind clusters `platform` / `crucible-m4` (cluster-check alone creates/deletes `crucible-m4`).
- Tests first for every behaviour change; every money path keeps a test that fails without the fix.
- Commits: pathspec form `git commit -m "…" -- <paths>`, trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Any change to what someone can do updates `docs/user` in the same commit; `go test ./internal/docs` passes.
- UI work reads `.claude/skills/crucible-ui/SKILL.md` first; colours from tokens only; a new pattern updates the skill.
- Copy, verbatim:
  - Banking (bedtime refuses starts, approvals and wakes): `The forge is banking for the night: labs start again in the morning.`
  - End reason `bedtime`: `Crucible went to sleep before your lab was ready. Start it again in the morning.`
  - End reason `workspace_full`: `Your workspace grew past 20 GiB, so the lab was put out.`
  - Workspace warning: `Your workspace holds more than 20 GiB: delete files you don't need, or the lab will be put out at the next check.`
  - Node disk: `The forge's disk is nearly full: try again later, or ask an admin.`
  - Asleep panel heading: `Your lab is asleep`; its button: `Wake it`.
  - Asleep detail: hibernated `Crucible hibernated your machines last night: they resume where you left them.`;
    stopped `Crucible stopped your machines last night: they restart from their disks.`
  - Waking loader: `Stoking the coals: starting your machines…`
  - Wake banner: hibernated `Your machines resumed where you left them.`; stopped `Your machines restarted from their disks; anything you hadn't saved is gone.`
  - Wake refused after the deadline: `Time ran out on this lab while it slept.`
  - Wake that never finished: `Waking took too long; press Wake it again.`
- Timings and sizes: settle 10 minutes; force-stop after 5 minutes stopping (`StopWait`); start wait 10 minutes
  (`StartWait`); a `waking` row older than 20 minutes goes back to `asleep`; workspace volume 20 GiB, measured every
  5 minutes, limit 20 GiB (20480 MiB); node free-disk floor 15 GiB; main stack `disk_gb` default 100;
  `labs_sleep_cron` default `0 19 * * *` (5-field Kubernetes cron in `schedule_timezone`).
- Bedtime phases (column `platform_bedtime.phase`): `settling` → `sleeping` → `asleep`. (Ruling: the spec's `closed`
  and `settling` are one phase, `settling`; starts are refused from the moment the row exists.)
- `slept_as`: `''` (no machines) | `hibernated` (every machine hibernated) | `stopped` (any machine stopped).
- Ruling (simplifies spec §4): only ready labs' workspaces are measured; an asleep workspace cannot grow, so its
  last measure stands (no short-lived pod on a sleeping volume).
- The teardown kill-switch actor is exactly `crucible aws teardown`; the server clears only a switch with that `changed_by`.
- On the node, every `crucible labs` call runs as `KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl -n crucible exec deploy/crucible -c api -- crucible labs <args>`.

## Review Focus

1. **A destroy racing a sleep or a wake** (End, the TTL or the kill switch while `waking`): the destroy must win, and
   the lab must never come back `ready` afterwards. Test: Task 5 `TestEndWhileWakingWins`.
2. **A restart mid-wake** leaves a row `waking` with no goroutine: after 20 minutes the sweep returns it to `asleep`
   with `Waking took too long; press Wake it again.` Test: Task 1 `TestAWakeThatNeverFinishedGoesBackToSleep`.
3. **`crucible aws sleep` whose bedtime fails part-way** must not leave the running platform closed: it cancels the
   bedtime and stops before the snapshot. Test: Task 8 `TestSleepCancelsAFailedBedtimeAndKeepsTheNodeUp`.
4. **Opening a sleeping lab whose TTL ran out overnight** is refused, and the sweep ends it without starting its
   machines. Tests: Task 5 `TestWakeAfterTheDeadlineIsRefused`, Task 1 `TestAnAsleepLabOutlivesItsIdleTimeoutAndEndsAtItsDeadline`.
5. **Metering across a sleep**: an instance asleep costs its disks only, and a lab destroyed while asleep ends those
   rows. Tests: Task 3 `TestMeterSleepPricesOnlyDisksUntilTheLabWakes`, `TestAnInstanceAsleepCostsOnlyItsDisks`.

## Lanes and order

Four lanes run in parallel worktrees; Task 11 runs after they merge.

| Lane | Tasks (in order) | Touches |
|---|---|---|
| core | 1 → 3 → 5 → 6 → 7 | `internal/labs` (service side), `internal/pricing`, `internal/journey`, migrations, `cmd/crucible/{serve,labs,main}.go`, Helm `crucible.yaml`/`values.yaml`/new `bedtime.yaml`, `deploy/aws/main` |
| cloud | 2 → 4 | `internal/awscloud`, `internal/labs/{aws,aws_objects,cluster,cluster_objects}.go`, Helm `rbac.yaml` |
| ops | 8 → 9 | `internal/awsops`, `cmd/crucible/main.go` (aws flags), `internal/content` lint, `deploy/aws/labs`, `examples/forge-402` |
| web | 10 | `web/src`, `.claude/skills/crucible-ui/SKILL.md` |

Expected merge conflicts (additive): `deploy/helm/test.sh` (Tasks 4, 6, 7), `cmd/crucible/main.go` (Tasks 7, 8).

---

### Task 1: Lab states `asleep` and `waking`, and every "alive" list

**Files:**
- Create: `internal/db/migrations/00036_lab_sleep.sql`
- Modify: `internal/labs/model.go` (State constants, Instance fields)
- Modify: `internal/labs/service.go` (instCols/scanInst, View fields, view(), Start's active check, Extend/requestExtension, destroy(), Sweep, reconcileCluster)
- Modify: `internal/labs/finops.go` (killAll), `internal/labs/approvals.go` (spend committed, extension lists), `internal/labs/reaper.go` (two lists), `internal/labs/mine.go`, `internal/journey/flags.go:49`
- Test: `internal/labs/sleep_states_test.go`

**Interfaces:**
- Produces: `labs.Asleep State = "asleep"`, `labs.Waking State = "waking"`; `Instance.SleptAt, WokeAt, WakingSince *time.Time`, `Instance.SleptAs string`; `View.SleptAs string (json "slept_as,omitempty")`, `View.SleptAt, View.WokeAt *time.Time (json "slept_at"/"woke_at", omitempty)`; DB: `platform_bedtime` table, `lab_resources.stopped_hourly_usd`, `lab_instances.{slept_at, slept_as, woke_at, waking_since}`; test helper `(*fx).asleep(t, id)`.

- [ ] **Step 1: Write the failing tests** — `internal/labs/sleep_states_test.go`:

```go
package labs

import (
	"context"
	"testing"
	"time"
)

// asleep puts a ready aws lab to sleep by hand, as the bedtime step does (Task 5).
func (f *fx) asleep(t *testing.T, id string) {
	t.Helper()
	if _, err := f.s.DB.Exec(context.Background(), `UPDATE lab_instances SET state = 'asleep', slept_at = $2, slept_as = 'stopped'
		WHERE id = $1 AND state = 'ready'`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAnAsleepLabOutlivesItsIdleTimeoutAndEndsAtItsDeadline(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	v := f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.clk.Add(time.Duration(v.IdleTimeoutS+60) * time.Second)
	if v.EndsAt == nil || !f.clk.Now().Before(*v.EndsAt) {
		t.Fatalf("fixture: the idle deadline must come before the lab's end: %+v", v)
	}
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Asleep || got.SleptAs != "stopped" || got.SleptAt == nil {
		t.Fatalf("nobody can be active while asleep: the idle rule skips it, got %+v", got)
	}
	f.clk.Set(v.EndsAt.Add(time.Second))
	f.s.Sweep(ctx)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != v.LimitReason {
		t.Fatalf("sleep counts towards the lab's time: ended %q, want %q", got.EndReason, v.LimitReason)
	}
}

func TestStartFindsTheSleepingLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat", StartOptions{})
	if err != nil || v.ID != id || v.State != Asleep {
		t.Fatalf("one lab per module: Start returns the sleeping one: %+v %v", v, err)
	}
}

func TestKillSwitchEndsSleepingLabs(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Destroyed)
}

func TestSleepingLabsAreNotEndedForTheReaper(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	ended, known, err := f.s.labsEnded(ctx, []string{id}, f.clk.Now())
	if err != nil || !known[id] {
		t.Fatalf("%v %v", known, err)
	}
	if _, over := ended[id]; over {
		t.Fatal("an asleep lab is alive: the reaper must not delete its machines")
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2 WHERE id = $1`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if ended, _, _ = f.s.labsEnded(ctx, []string{id}, f.clk.Now()); ended[id] != (time.Time{}) {
		t.Fatal("a waking lab is alive too")
	}
}

func TestSleepingLabsStayCommittedInSpend(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	before, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	f.asleep(t, id)
	after, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	if after.CommittedUSD < before.CommittedUSD-1e-9 {
		t.Fatalf("a sleeping lab still holds its cost up to its deadline: %v → %v", before.CommittedUSD, after.CommittedUSD)
	}
}

func TestAWakeThatNeverFinishedGoesBackToSleep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2 WHERE id = $1`, id, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(21 * time.Minute)
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, id)
	if got.State != Asleep || got.Error != "Waking took too long; press Wake it again." {
		t.Fatalf("a restart mid-wake leaves the lab asleep with a way forward: %+v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH && go test ./internal/labs -run 'Asleep|Sleeping|NeverFinished' -count=1`
Expected: FAIL to compile (`undefined: Asleep`, `SleptAs`).

- [ ] **Step 3: The migration** — `internal/db/migrations/00036_lab_sleep.sql`:

```sql
-- +goose Up
-- AWS labs sleep with the platform (spec 2026-11-05): ready → asleep → waking → ready.
ALTER TABLE lab_instances
  ADD COLUMN slept_at     TIMESTAMPTZ,
  ADD COLUMN slept_as     TEXT NOT NULL DEFAULT '',  -- '' (no machines) | hibernated | stopped (any machine stopped)
  ADD COLUMN woke_at      TIMESTAMPTZ,
  ADD COLUMN waking_since TIMESTAMPTZ;
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'asleep', 'waking', 'destroying');
-- What a resource costs per hour while its lab sleeps: an instance's disks; anything else its full price.
ALTER TABLE lab_resources ADD COLUMN stopped_hourly_usd DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (stopped_hourly_usd >= 0);
UPDATE lab_resources SET stopped_hourly_usd = hourly_usd; -- rows metered before this: the safe side
-- One row while the platform goes to sleep or sleeps; the server deletes it when it starts (labs.Service.Morning).
CREATE TABLE platform_bedtime (
  id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  requested_at TIMESTAMPTZ NOT NULL,
  requested_by TEXT NOT NULL,
  phase        TEXT NOT NULL DEFAULT 'settling' CHECK (phase IN ('settling', 'sleeping', 'asleep'))
);

-- +goose Down
DROP TABLE platform_bedtime;
ALTER TABLE lab_resources DROP COLUMN stopped_hourly_usd;
UPDATE lab_instances SET state = 'ready' WHERE state IN ('asleep', 'waking');
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
ALTER TABLE lab_instances DROP COLUMN slept_at, DROP COLUMN slept_as, DROP COLUMN woke_at, DROP COLUMN waking_since;
```

- [ ] **Step 4: States and fields** — `internal/labs/model.go`: after `Destroying` add

```go
	Asleep          State = "asleep" // an aws lab whose machines sleep while the platform does (spec 2026-11-05)
	Waking          State = "waking" // its trainee pressed Wake it: machines starting, workspace coming back
```

and to `Instance` add:

```go
	SleptAt, WokeAt, WakingSince *time.Time
	SleptAs                      string // "" | hibernated | stopped
```

- [ ] **Step 5: Read and show them** — `internal/labs/service.go`:
  - `instCols`: append `, slept_at, slept_as, woke_at, waking_since` after `cluster_context`.
  - `scanInst`: append `&in.SleptAt, &in.SleptAs, &in.WokeAt, &in.WakingSince` after `&in.ClusterContext`.
  - `View`: add after `ClusterContext`:

```go
	SleptAs          string             `json:"slept_as,omitempty"` // asleep, and after a wake: hibernated | stopped
	SleptAt          *time.Time         `json:"slept_at,omitempty"`
	WokeAt           *time.Time         `json:"woke_at,omitempty"`
```

  - In `view()`, set them in the `v := &View{…}` literal: `SleptAs: inst.SleptAs, SleptAt: inst.SleptAt, WokeAt: inst.WokeAt`,
    and replace the `if inst.State == Ready {` block with:

```go
	if inst.State == Ready {
		dl := inst.LastActivityAt.Add(inst.IdleTimeout)
		v.IdleDeadline = &dl
	}
	if inst.State == Ready || inst.State == Asleep { // a sleeping lab can still be extended
		v.ExtensionPending = inst.ExtUntil != nil
		v.CanExtend = !inst.Extended && inst.MaxExtension > 0 && inst.LimitReason != "schedule" && inst.LimitReason != "budget"
	}
```

- [ ] **Step 6: Every alive list** — exact edits:
  - `Start`: `inst.State == PendingApproval || inst.State == Provisioning || inst.State == Ready` →
    `slices.Contains([]State{PendingApproval, Provisioning, Ready, Asleep, Waking}, inst.State)`.
  - `Extend`: `if inst.State != Ready ||` → `if (inst.State != Ready && inst.State != Asleep) ||`; its UPDATE
    `AND state = 'ready'` → `AND state IN ('ready', 'asleep')`. Same SQL change in `requestExtension`.
  - `destroy()`: `l.state IN ('provisioning', 'ready')` → `l.state IN ('provisioning', 'ready', 'asleep', 'waking')`.
  - `Sweep` query: add two conditions before `OR (state = 'pending_approval' …`:

```sql
		   OR (state = 'asleep' AND ends_at <= $1)
		   OR (state = 'waking' AND waking_since < $1 - interval '20 minutes')
```

    and in the loop, as the first `case`:

```go
		case inst.State == Waking: // a wake that never finished (a restart mid-wake): back to sleep; the trainee tries again
			if _, busy := s.inflight.Load("wake:" + inst.ID); !busy {
				s.endRow(ctx, inst.ID, `UPDATE lab_instances SET state = 'asleep', waking_since = NULL, error = $2
					WHERE id = $1 AND state = 'waking'`, "Waking took too long; press Wake it again.")
			}
			continue
```

  - `reconcileCluster`: `state IN ('provisioning', 'ready', 'destroying')` → `state IN ('provisioning', 'ready', 'asleep', 'waking', 'destroying')`.
  - `finops.go` `killAll`: `state IN ('provisioning', 'ready')` → `state IN ('provisioning', 'ready', 'asleep', 'waking')`.
  - `approvals.go` `spend`: `WHEN state = 'ready' THEN` → `WHEN state IN ('ready', 'asleep', 'waking') THEN`; lines
    with `state = 'ready' AND ext_until` (three places: the extension list query and the two extension UPDATEs) →
    `state IN ('ready', 'asleep') AND ext_until`.
  - `reaper.go`: both `('pending_approval', 'provisioning', 'ready', 'destroying')` → `('pending_approval', 'provisioning', 'ready', 'asleep', 'waking', 'destroying')`.
  - `mine.go`: the same replacement in the ORDER BY.
  - `internal/journey/flags.go:49`: the same replacement.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/labs ./internal/journey ./internal/db -count=1`
Expected: PASS (the new tests and every existing one).

- [ ] **Step 8: Commit**

```bash
git add internal/db/migrations/00036_lab_sleep.sql internal/labs/sleep_states_test.go
git commit -m "feat(labs): asleep and waking lab states, alive everywhere a ready lab is

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00036_lab_sleep.sql internal/labs/sleep_states_test.go internal/labs/model.go internal/labs/service.go internal/labs/finops.go internal/labs/approvals.go internal/labs/reaper.go internal/labs/mine.go internal/journey/flags.go
```

---

### Task 2: `awscloud` Sleep and Wake

**Files:**
- Modify: `internal/awscloud/awscloud.go` (types, interface), `internal/awscloud/client.go`, `internal/awscloud/fake.go`
- Test: `internal/awscloud/client_test.go`, `internal/awscloud/fake_test.go`

**Interfaces:**
- Produces:

```go
type Slept struct {
	InstanceID string
	Hibernated bool
}
var StopWait = 5 * time.Minute   // then StopInstances --force
var StartWait = 10 * time.Minute
// in Cloud:
Sleep(ctx context.Context, region string, c Credentials, labID string) ([]Slept, error)
Wake(ctx context.Context, region string, c Credentials, labID string) error
// on *Fake:
func (f *Fake) AddInstance(region, labID, id string, hibernatable bool) // running
func (f *Fake) InstanceState(id string) string                         // "" when unknown
```

  `Fake.SimulateApply` adds the running instance `i-<labID>` (not hibernatable); `SimulateDestroy` removes it.

- [ ] **Step 1: Failing tests** — append to `internal/awscloud/client_test.go` (add `fmt`, `maps`, `slices` to its imports):

```go
// describeXML answers DescribeInstances from state, keeping only the states its instance-state-name filter names.
func describeXML(state map[string]string, form url.Values) string {
	want := map[string]bool{}
	for i := 1; form.Get(fmt.Sprintf("Filter.2.Value.%d", i)) != ""; i++ {
		want[form.Get(fmt.Sprintf("Filter.2.Value.%d", i))] = true
	}
	var b strings.Builder
	b.WriteString(`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet><item><instancesSet>`)
	for _, id := range slices.Sorted(maps.Keys(state)) {
		if len(want) == 0 || want[state[id]] {
			fmt.Fprintf(&b, `<item><instanceId>%s</instanceId><instanceState><code>0</code><name>%s</name></instanceState></item>`, id, state[id])
		}
	}
	b.WriteString(`</instancesSet></item></reservationSet></DescribeInstancesResponse>`)
	return b.String()
}

const ec2OK = `<%[1]sResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><instancesSet/></%[1]sResponse>`

func TestSleepHibernatesWhatItCanAndStopsTheRest(t *testing.T) {
	old := StopWait
	StopWait = time.Second
	t.Cleanup(func() { StopWait = old })
	var mu sync.Mutex
	state := map[string]string{"i-1": "running", "i-2": "running"}
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.form.Get("Action") {
		case "DescribeInstances":
			return 200, "text/xml", describeXML(state, c.form)
		case "StopInstances":
			id := c.form.Get("InstanceId.1")
			if id == "i-2" && c.form.Get("Hibernate") == "true" {
				return 400, "text/xml", `<Response><Errors><Error><Code>UnsupportedHibernationConfiguration</Code><Message>not set up for hibernation</Message></Error></Errors><RequestID>r</RequestID></Response>`
			}
			state[id] = "stopped"
			return 200, "text/xml", fmt.Sprintf(ec2OK, "StopInstances")
		}
		return 400, "text/plain", "unexpected"
	})
	cl.poll = time.Millisecond
	creds := Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}
	got, err := cl.Sleep(context.Background(), "eu-west-1", creds, "aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []Slept{{"i-1", true}, {"i-2", false}}) {
		t.Fatalf("i-1 hibernated, i-2 refused hibernation and stopped: %+v", got)
	}
	var stops []string
	for _, c := range calls() {
		switch c.form.Get("Action") {
		case "StopInstances":
			stops = append(stops, c.form.Get("InstanceId.1")+" hibernate="+c.form.Get("Hibernate"))
		case "DescribeInstances":
			if c.form.Get("Filter.1.Name") != "tag:crucible:lab-id" || c.form.Get("Filter.1.Value.1") != "aaaaaaaaaaaa" {
				t.Fatalf("only the lab's own machines are listed: %v", c.form)
			}
		}
	}
	if strings.Join(stops, ", ") != "i-1 hibernate=true, i-2 hibernate=true, i-2 hibernate=" {
		t.Fatalf("stops: %v", stops)
	}
}

func TestSleepForcesAMachineThatNeverStops(t *testing.T) {
	old := StopWait
	StopWait = 20 * time.Millisecond
	t.Cleanup(func() { StopWait = old })
	var mu sync.Mutex
	state := map[string]string{"i-1": "running"}
	cl, calls := fakeAWS(t, func(c call) (int, string, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.form.Get("Action") {
		case "DescribeInstances":
			return 200, "text/xml", describeXML(state, c.form)
		case "StopInstances":
			state["i-1"] = map[bool]string{true: "stopped", false: "stopping"}[c.form.Get("Force") == "true"]
			return 200, "text/xml", fmt.Sprintf(ec2OK, "StopInstances")
		}
		return 400, "text/plain", "unexpected"
	})
	cl.poll = time.Millisecond
	got, err := cl.Sleep(context.Background(), "eu-west-1", Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}, "aaaaaaaaaaaa")
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	forced := false
	for _, c := range calls() {
		forced = forced || (c.form.Get("Action") == "StopInstances" && c.form.Get("Force") == "true")
	}
	if !forced {
		t.Fatal("a guest that never answers the shutdown (a broken boot) is force-stopped")
	}
}

func TestWakeStartsTheLabsStoppedMachines(t *testing.T) {
	var mu sync.Mutex
	state := map[string]string{"i-1": "stopped", "i-2": "stopped"}
	starts := 0
	cl, _ := fakeAWS(t, func(c call) (int, string, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.form.Get("Action") {
		case "DescribeInstances":
			return 200, "text/xml", describeXML(state, c.form)
		case "StartInstances":
			starts++
			for i := 1; c.form.Get(fmt.Sprintf("InstanceId.%d", i)) != ""; i++ {
				state[c.form.Get(fmt.Sprintf("InstanceId.%d", i))] = "running"
			}
			return 200, "text/xml", fmt.Sprintf(ec2OK, "StartInstances")
		}
		return 400, "text/plain", "unexpected"
	})
	cl.poll = time.Millisecond
	if err := cl.Wake(context.Background(), "eu-west-1", Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t"}, "aaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || state["i-1"] != "running" || state["i-2"] != "running" {
		t.Fatalf("one start for both machines, both running: %d %v", starts, state)
	}
}
```

  and to `internal/awscloud/fake_test.go`:

```go
func TestFakeSleepStopsOnlyTheLabsOwnMachines(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	f.AddInstance("eu-west-1", "aaaaaaaaaaaa", "i-a", true)
	f.AddInstance("eu-west-1", "bbbbbbbbbbbb", "i-b", false)
	c, _ := f.AssumeLab(ctx, Session{LabID: "aaaaaaaaaaaa"})
	got, err := f.Sleep(ctx, "eu-west-1", c, "aaaaaaaaaaaa")
	if err != nil || !slices.Equal(got, []Slept{{"i-a", true}}) {
		t.Fatalf("%+v %v", got, err)
	}
	if f.InstanceState("i-a") != "stopped" || f.InstanceState("i-b") != "running" {
		t.Fatal("only the lab's own machine sleeps")
	}
	if _, err := f.Sleep(ctx, "eu-west-1", c, "bbbbbbbbbbbb"); err == nil {
		t.Fatal("one lab's session can't stop another lab's machines")
	}
	if err := f.Wake(ctx, "eu-west-1", c, "aaaaaaaaaaaa"); err != nil || f.InstanceState("i-a") != "running" {
		t.Fatalf("wake: %v %s", err, f.InstanceState("i-a"))
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/awscloud -run 'Sleep|Wake' -count=1`
Expected: FAIL to compile (`undefined: Slept`, `cl.poll`, `f.AddInstance`).

- [ ] **Step 3: Types and interface** — `internal/awscloud/awscloud.go`, after `TrailEvent`:

```go
// Slept is how one lab machine went to sleep: hibernated (its RAM kept on its encrypted root) or stopped.
type Slept struct {
	InstanceID string
	Hibernated bool
}

// StopWait is how long Sleep waits for machines to stop before forcing them: a guest stuck at GRUB or in an emergency
// shell may never answer the shutdown. StartWait bounds Wake. Vars for tests.
var (
	StopWait  = 5 * time.Minute
	StartWait = 10 * time.Minute
)
```

  and in `Cloud`, after `Delete`:

```go
	// Sleep, with lab credentials, hibernates every running or pending machine tagged with labID in region (a plain stop
	// where AWS refuses hibernation) and waits until they are stopped, forcing any still stopping after StopWait.
	Sleep(ctx context.Context, region string, c Credentials, labID string) ([]Slept, error)
	// Wake starts every stopped machine tagged with labID in region and waits until they run.
	Wake(ctx context.Context, region string, c Credentials, labID string) error
```

- [ ] **Step 4: The client** — `internal/awscloud/client.go`: add `poll time.Duration // 0 = 5 s; tests shorten it` to `Client`,
  add `"maps"` to the imports, refactor `Delete`'s client construction into `labEC2`, and add:

```go
// labEC2 is an EC2 client in region with a lab session's credentials.
func (c *Client) labEC2(region string, cr Credentials) *ec2.Client {
	cfg := c.node.Copy()
	cfg.Region = region
	cfg.Credentials = credentials.NewStaticCredentialsProvider(cr.AccessKeyID, cr.SecretAccessKey, cr.SessionToken)
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = c.endpoint() })
}

func (c *Client) every() time.Duration {
	if c.poll == 0 {
		return 5 * time.Second
	}
	return c.poll
}

// hibernationRefused: AWS won't hibernate this machine (launched without it, or a type or image that can't).
var hibernationRefused = map[string]bool{"UnsupportedHibernationConfiguration": true, "UnsupportedOperation": true}

// labInstances is the lab's machines in the given states, id → state.
func labInstances(ctx context.Context, e *ec2.Client, labID string, states ...string) (map[string]string, error) {
	out := map[string]string{}
	p := ec2.NewDescribeInstancesPaginator(e, &ec2.DescribeInstancesInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:" + TagLab), Values: []string{labID}},
		{Name: aws.String("instance-state-name"), Values: states}}})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				if i.State != nil {
					out[aws.ToString(i.InstanceId)] = string(i.State.Name)
				}
			}
		}
	}
	return out, nil
}

// waitState polls until every id is in want (or terminated), for at most d.
func (c *Client) waitState(ctx context.Context, e *ec2.Client, labID string, ids []string, want string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		all, err := labInstances(ctx, e, labID, "pending", "running", "stopping", "stopped", "shutting-down", "terminated")
		if err != nil {
			return err
		}
		done := true
		for _, id := range ids {
			if s := all[id]; s != want && s != "terminated" {
				done = false
			}
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the lab's machines were not %s after %s", want, d)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.every()):
		}
	}
}

func (c *Client) Sleep(ctx context.Context, region string, cr Credentials, labID string) ([]Slept, error) {
	if !ValidLabID(labID) {
		return nil, fmt.Errorf("invalid lab id %q", labID)
	}
	e := c.labEC2(region, cr)
	live, err := labInstances(ctx, e, labID, "pending", "running")
	if err != nil {
		return nil, err
	}
	ids := slices.Sorted(maps.Keys(live))
	var out []Slept
	for _, id := range ids {
		_, err := e.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}, Hibernate: aws.Bool(true)})
		hibernated := err == nil
		var api smithy.APIError
		if errors.As(err, &api) && hibernationRefused[api.ErrorCode()] {
			_, err = e.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}})
		}
		if err != nil {
			return out, fmt.Errorf("stopping %s: %w", id, err)
		}
		out = append(out, Slept{InstanceID: id, Hibernated: hibernated})
	}
	if len(ids) == 0 {
		return out, nil
	}
	if c.waitState(ctx, e, labID, ids, "stopped", StopWait) != nil {
		if _, err := e.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: ids, Force: aws.Bool(true)}); err != nil {
			return out, fmt.Errorf("forcing the stop: %w", err)
		}
		if err := c.waitState(ctx, e, labID, ids, "stopped", StopWait); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (c *Client) Wake(ctx context.Context, region string, cr Credentials, labID string) error {
	if !ValidLabID(labID) {
		return fmt.Errorf("invalid lab id %q", labID)
	}
	e := c.labEC2(region, cr)
	asleep, err := labInstances(ctx, e, labID, "stopping", "stopped")
	if err != nil {
		return err
	}
	ids := slices.Sorted(maps.Keys(asleep))
	if len(ids) == 0 {
		return nil
	}
	if err := c.waitState(ctx, e, labID, ids, "stopped", StopWait); err != nil { // one still hibernating finishes first
		return err
	}
	if _, err := e.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: ids}); err != nil {
		return err
	}
	return c.waitState(ctx, e, labID, ids, "running", StartWait)
}
```

- [ ] **Step 5: The fake** — `internal/awscloud/fake.go`: add `instances map[string]fakeInstance // by instance id` to `Fake`, and:

```go
type fakeInstance struct {
	region, labID, state string
	hibernatable         bool
}

// AddInstance adds a running lab machine; hibernatable means it was launched for hibernation.
func (f *Fake) AddInstance(region, labID, id string, hibernatable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.instances == nil {
		f.instances = map[string]fakeInstance{}
	}
	f.instances[id] = fakeInstance{region: region, labID: labID, state: "running", hibernatable: hibernatable}
}

// InstanceState is a machine's state, "" when there is no such machine.
func (f *Fake) InstanceState(id string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.instances[id].state }

// setStates moves the lab's machines in region from one state to another, with the lab's own session only.
func (f *Fake) setStates(region string, c Credentials, labID, from, to string) ([]Slept, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	if c.SessionToken != token(labID) {
		return nil, errors.New("AccessDenied: not this lab's machines")
	}
	var out []Slept
	for _, id := range slices.Sorted(maps.Keys(f.instances)) {
		if x := f.instances[id]; x.region == region && x.labID == labID && x.state == from {
			x.state = to
			f.instances[id] = x
			out = append(out, Slept{InstanceID: id, Hibernated: x.hibernatable})
		}
	}
	return out, nil
}

func (f *Fake) Sleep(_ context.Context, region string, c Credentials, labID string) ([]Slept, error) {
	return f.setStates(region, c, labID, "running", "stopped")
}

func (f *Fake) Wake(_ context.Context, region string, c Credentials, labID string) error {
	_, err := f.setStates(region, c, labID, "stopped", "running")
	return err
}
```

  (add `"maps"` to its imports). In `SimulateApply`, after the two `f.Add` calls, add
  `f.AddInstance(region, s.LabID, "i-"+s.LabID, false)` and say so in its comment ("and a running machine, which
  bedtime puts to sleep"); in `SimulateDestroy`, inside its locked section, add `delete(f.instances, "i-"+labID)`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/awscloud ./internal/labs -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git commit -m "feat(awscloud): Sleep hibernates or stops a lab's machines, Wake starts them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awscloud/awscloud.go internal/awscloud/client.go internal/awscloud/fake.go internal/awscloud/client_test.go internal/awscloud/fake_test.go
```

---

### Task 3: Metering a sleeping lab

**Files:**
- Modify: `internal/pricing/pricing.go` (Line), `internal/pricing/aws.go` (priceOwn, PriceAWS)
- Modify: `internal/labs/meter.go` (Meter's INSERT, new meterSleep)
- Test: `internal/pricing/pricing_test.go`, `internal/labs/meter_test.go`

**Interfaces:**
- Consumes: Task 1's `lab_resources.stopped_hourly_usd`.
- Produces: `pricing.Line.StoppedHourlyUSD float64` (json `-`); `func (s *Service) meterSleep(ctx context.Context, labID string, at time.Time) error`. Waking ends the stopped rows with the existing `EndMeter`.

- [ ] **Step 1: Failing tests** — `internal/pricing/pricing_test.go` (add `math` if missing):

```go
type stoppedPrices map[string]float64

func (m stoppedPrices) Price(_, _, key string) (float64, bool) { v, ok := m[key]; return v, ok }

func TestAnInstanceAsleepCostsOnlyItsDisks(t *testing.T) {
	prices := stoppedPrices{"ec2:t3.nano": 0.0052, "ebs:gp3": 0.088, "ipv4:hour": 0.005, "nat:hour": 0.048}
	inst := Resource{Address: "aws_instance.web", Type: "aws_instance", Values: map[string]any{"instance_type": "t3.nano",
		"associate_public_ip_address": true, "root_block_device": []any{map[string]any{"volume_size": 8.0, "volume_type": "gp3"}}}}
	nat := Resource{Address: "aws_nat_gateway.gw", Type: "aws_nat_gateway", Values: map[string]any{"connectivity_type": "private"}}
	b, err := PriceAWS([]Resource{inst, nat}, "eu-west-1", nil, nil, prices)
	if err != nil {
		t.Fatal(err)
	}
	disk := 8 * 0.088 / HoursPerMonth
	if l := b.Lines[0]; math.Abs(l.StoppedHourlyUSD-disk) > 1e-12 || l.HourlyUSD <= disk+0.0052 {
		t.Fatalf("an instance asleep pays for its disks; its compute and IPv4 stop: %+v", l)
	}
	if l := b.Lines[1]; l.StoppedHourlyUSD != l.HourlyUSD {
		t.Fatalf("anything else costs as much asleep: %+v", l)
	}
	d, err := PriceAWS([]Resource{inst}, "eu-west-1", map[string]float64{"aws_instance": 1}, nil, prices)
	if err != nil || d.Lines[0].Note != "declared" || d.Lines[0].StoppedHourlyUSD != 1 {
		t.Fatalf("a declared price stands for the whole machine, asleep too: %+v %v", d.Lines, err)
	}
}
```

  and `internal/labs/meter_test.go`:

```go
func TestMeterSleepPricesOnlyDisksUntilTheLabWakes(t *testing.T) {
	ctx := context.Background()
	f, cloud, inst, lab := meterFixture(t, corev1.PodSucceeded)
	cloud.SetState(inst.ID, stateWith("aws_instance.web"))
	if err := f.s.Meter(ctx, inst, lab); err != nil {
		t.Fatal(err)
	}
	full := openRows(t, f, inst.ID)["aws_instance.web"]
	f.clk.Add(time.Hour)
	slept := f.clk.Now()
	if err := f.s.meterSleep(ctx, inst.ID, slept); err != nil {
		t.Fatal(err)
	}
	disk := 8 * 0.088 / pricing.HoursPerMonth
	if got := openRows(t, f, inst.ID)["aws_instance.web"]; math.Abs(got-disk) > 1e-9 || got >= full {
		t.Fatalf("asleep, the instance's row costs its disks: %v (running %v)", got, full)
	}
	f.clk.Add(10 * time.Hour)
	if err := f.s.EndMeter(ctx, inst.ID, f.clk.Now()); err != nil { // the wake (or a destroy while asleep)
		t.Fatal(err)
	}
	if n := len(openRows(t, f, inst.ID)); n != 0 {
		t.Fatalf("waking or a destroy ends the sleeping rows: %d open", n)
	}
	var usd float64
	if err := f.s.DB.QueryRow(ctx, `SELECT sum((hourly_usd + usage_hourly_usd) * extract(epoch FROM ended_at - started_at) / 3600)
		FROM lab_resources WHERE lab_id = $1`, inst.ID).Scan(&usd); err != nil {
		t.Fatal(err)
	}
	if want := full*1 + disk*10; math.Abs(usd-want) > 1e-9 {
		t.Fatalf("an hour running and ten asleep: $%v, want $%v", usd, want)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/pricing ./internal/labs -run 'Asleep|MeterSleep' -count=1`
Expected: FAIL to compile (`StoppedHourlyUSD`, `meterSleep` undefined).

- [ ] **Step 3: Price the stopped hour** — `internal/pricing/pricing.go` `Line`, after `UsageHourlyUSD`:

```go
	StoppedHourlyUSD float64 `json:"-"` // per hour while the lab sleeps: an instance's disks only; anything else its HourlyUSD
```

  `internal/pricing/aws.go` `priceOwn`, `aws_instance` case: add `stopped := size * gbp / HoursPerMonth` right after
  `line.HourlyUSD += size * gbp / HoursPerMonth`; inside the `ebs` loop add `stopped += num(d, "volume_size") * gbp / HoursPerMonth`;
  after the loop (before the IPv4 block) add `line.StoppedHourlyUSD = stopped`. In `PriceAWS`, right after the
  `if p, ok := declared[r.Type]; …` block:

```go
		if line.Type != "aws_instance" || line.Note == "declared" { // only an instance's compute and IPv4 stop with it
			line.StoppedHourlyUSD = line.HourlyUSD
		}
```

- [ ] **Step 4: Meter it** — `internal/labs/meter.go`: in `Meter`, the INSERT becomes

```go
		if _, err := tx.Exec(ctx, `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, stopped_hourly_usd, started_at)
			SELECT $1, $2, $3, $4, $5, $6, $7 WHERE NOT EXISTS (
				SELECT 1 FROM lab_resources WHERE lab_id = $1 AND address = $2 AND ended_at IS NULL)`,
			inst.ID, l.Address, l.Type, l.HourlyUSD, l.UsageHourlyUSD, l.StoppedHourlyUSD, now); err != nil {
```

  and add:

```go
// meterSleep re-prices the lab's open rows from at for its sleep (spec 2026-11-05 §3): each continues at its stopped
// price (an instance's disks; its compute and IPv4 stop). Waking ends these rows with EndMeter, and the next sweep
// meters the running lab again; a destroy while asleep ends them the same way.
func (s *Service) meterSleep(ctx context.Context, labID string, at time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT address, type, usage_hourly_usd, stopped_hourly_usd, started_at FROM lab_resources
		WHERE lab_id = $1 AND ended_at IS NULL FOR UPDATE`, labID)
	if err != nil {
		return err
	}
	type open struct {
		addr, typ      string
		usage, stopped float64
		started        time.Time
	}
	var all []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.addr, &o.typ, &o.usage, &o.stopped, &o.started); err != nil {
			rows.Close()
			return err
		}
		all = append(all, o)
	}
	rows.Close()
	for _, o := range all {
		if o.started.Equal(at) { // metered this instant: price the row itself
			if _, err := tx.Exec(ctx, `UPDATE lab_resources SET hourly_usd = stopped_hourly_usd WHERE lab_id = $1 AND address = $2 AND started_at = $3`,
				labID, o.addr, at); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE lab_resources SET ended_at = $3 WHERE lab_id = $1 AND address = $2 AND ended_at IS NULL`, labID, o.addr, at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, stopped_hourly_usd, started_at)
			VALUES ($1, $2, $3, $4, $5, $4, $6)`, labID, o.addr, o.typ, o.stopped, o.usage, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/pricing ./internal/labs -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(labs): a sleeping lab is metered at its disks' price

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/pricing/pricing.go internal/pricing/aws.go internal/pricing/pricing_test.go internal/labs/meter.go internal/labs/meter_test.go
```

---

### Task 4: The workspace on a volume; AWSRunner SleepLab, WakeLab, WorkspaceMiB

**Files:**
- Modify: `internal/labs/cluster_objects.go` (labObjects.PVC), `internal/labs/aws_objects.go` (awsWorkspace), `internal/labs/cluster.go` (provision creates the PVC), `internal/labs/aws.go` (three methods)
- Modify: `deploy/helm/crucible/templates/rbac.yaml`, `deploy/helm/test.sh`
- Test: `internal/labs/aws_sleep_test.go`; update `TestAWSProvisionBuildsTheWorkspaceThenApplies` in `internal/labs/aws_test.go`

**Interfaces:**
- Consumes: Task 2's `awscloud.Cloud.Sleep/Wake`, `Fake.AddInstance/InstanceState`.
- Produces (on `*AWSRunner`, the shape Task 5's `labSleeper` and Task 6's `workspaceMeter` interfaces expect):

```go
func (a *AWSRunner) SleepLab(ctx context.Context, inst *Instance, lab *content.Lab) (string, error) // "" | hibernated | stopped
func (a *AWSRunner) WakeLab(ctx context.Context, inst *Instance, lab *content.Lab) error
func (a *AWSRunner) WorkspaceMiB(ctx context.Context, inst *Instance) (int64, error)
```

- [ ] **Step 1: Failing tests** — `internal/labs/aws_sleep_test.go`:

```go
package labs

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"

	"crucible/internal/awscloud"
)

func TestAWSWorkspaceKeepsItsDiskOnAVolume(t *testing.T) {
	o := awsWorkspace(clusterObjects(testID, workspaceCompose, false))
	if o.PVC == nil || o.PVC.Name != "workspace" || o.PVC.Spec.Resources.Requests.Storage().String() != "20Gi" {
		t.Fatalf("one 20 GiB volume per workspace: %+v", o.PVC)
	}
	mounts := map[string]string{}
	for _, m := range o.Pod.Spec.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m.Name + ":" + m.SubPath
	}
	if mounts["/var/lib/docker"] != "workspace:docker" || mounts["/lab"] != "workspace:lab" {
		t.Fatalf("Docker's storage and /lab live on the volume: %v", mounts)
	}
	for _, v := range o.Pod.Spec.Volumes {
		if v.EmptyDir != nil {
			t.Fatalf("nothing the trainee keeps is on an emptyDir: %s", v.Name)
		}
	}
	if q := o.Quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims]; q.String() != "1" {
		t.Fatalf("the quota allows that one volume: %v", q.String())
	}
	if c := clusterObjects(testID, "compose.yaml", false); c.PVC != nil {
		t.Fatal("other lab namespaces keep their emptyDirs")
	}
}

func TestSleepLabStopsTheWorkspaceThenTheMachines(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: labPod, Namespace: labNamespace(testID)}})
	a, cloud, fe := testAWS(cs)
	cloud.AddInstance("eu-west-1", testID, "i-1", false)
	how, err := a.SleepLab(ctx, &Instance{ID: testID, Team: "forge", Training: "forge-401"}, cloudHeat(t))
	if err != nil || how != "stopped" {
		t.Fatalf("%q %v", how, err)
	}
	if got := strings.Join(fe.last().cmd, " "); got != "docker compose stop" {
		t.Fatalf("the workspace's containers stop cleanly first: %s", got)
	}
	if _, err := cs.CoreV1().Pods(labNamespace(testID)).Get(ctx, labPod, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("the workspace pod goes; its volume stays")
	}
	if cloud.InstanceState("i-1") != "stopped" {
		t.Fatal("the lab's machine sleeps")
	}
	cloud.AddInstance("eu-west-1", testID, "i-2", true)
	cloud.AddInstance("eu-west-1", testID, "i-1", true) // running again, and this time launched for hibernation
	if how, err := a.SleepLab(ctx, &Instance{ID: testID}, cloudHeat(t)); err != nil || how != "hibernated" {
		t.Fatalf("every machine hibernated: %q %v", how, err)
	}
}

func TestWakeLabStartsTheMachinesAndTheSameContainer(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	kubelet(cs, corev1.PodSucceeded, "")
	a, cloud, fe := testAWS(cs)
	ns := labNamespace(testID)
	cloud.AddInstance("eu-west-1", testID, "i-1", false)
	c, _ := cloud.AssumeLab(ctx, awscloud.Session{LabID: testID})
	if _, err := cloud.Sleep(ctx, "eu-west-1", c, testID); err != nil {
		t.Fatal(err)
	}
	if err := a.WakeLab(ctx, &Instance{ID: testID, Team: "forge", Training: "forge-401"}, cloudHeat(t)); err != nil {
		t.Fatal(err)
	}
	if cloud.InstanceState("i-1") != "running" {
		t.Fatal("the machine runs again")
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, labPod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Spec.Volumes, func(v corev1.Volume) bool {
		return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "workspace"
	}) {
		t.Fatal("the pod comes back on the same volume")
	}
	if got := strings.Join(fe.last().cmd, " "); got != "docker compose up -d --no-recreate --wait --quiet-pull" {
		t.Fatalf("the trainee's own container starts again, never a new one: %s", got)
	}
	if !slices.Contains(steps(cs, "create", "update"), "create secrets") {
		t.Fatal("fresh credentials are mounted")
	}
}

func TestWorkspaceMiBAddsUpTheVolume(t *testing.T) {
	cs := fake.NewClientset()
	a, _, fe := testAWS(cs)
	fe.fn = func(_ context.Context, cmd []string, o remotecommand.StreamOptions) error {
		_, _ = o.Stdout.Write([]byte("1500\t/var/lib/docker\n24\t/lab\n"))
		return nil
	}
	if n, err := a.WorkspaceMiB(context.Background(), &Instance{ID: testID}); err != nil || n != 1524 {
		t.Fatalf("%d %v", n, err)
	}
	if got := strings.Join(fe.last().cmd, " "); got != "du -sxm /var/lib/docker /lab" {
		t.Fatalf("measured with du: %s", got)
	}
}
```

  (`fakeExec.fn` is the existing hook in `cluster_test.go`, `func(ctx context.Context, cmd []string, o remotecommand.StreamOptions) error`.) In `aws_test.go`
  `TestAWSProvisionBuildsTheWorkspaceThenApplies`, the expected create steps gain `"create persistentvolumeclaims"`
  between `"create configmaps"` and the first `"create pods"`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs -run 'AWSWorkspace|SleepLab|WakeLab|WorkspaceMiB|AWSProvisionBuilds' -count=1`
Expected: FAIL to compile (`o.PVC`, `SleepLab`, `WakeLab`, `WorkspaceMiB` undefined).

- [ ] **Step 3: The volume** — `cluster_objects.go`: add `PVC *corev1.PersistentVolumeClaim // aws labs: the workspace's disk (nil otherwise)` to `labObjects`.
  `aws_objects.go`: add `"slices"` to the imports, the constants `workspacePVC = "workspace"`, and
  `workspaceDisk = resource.MustParse("20Gi")` in the `var` block; in `awsWorkspace`, before `h := o.Quota.Spec.Hard`:

```go
	// The workspace's disk outlives its pod (spec 2026-11-05 §4): Docker's storage and /lab on one volume, so a lab that
	// slept comes back with the trainee's container, files and pulled images. Deleted with the namespace.
	o.PVC = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: workspacePVC, Namespace: o.Namespace.Name,
		Labels: map[string]string{labLabel: o.Namespace.Labels[labLabel]}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: workspaceDisk}}}}
	for i := range c.VolumeMounts {
		if n := c.VolumeMounts[i].Name; n == "docker" || n == "lab" {
			c.VolumeMounts[i].Name, c.VolumeMounts[i].SubPath = workspacePVC, n
		}
	}
	o.Pod.Spec.Volumes = slices.DeleteFunc(o.Pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == "docker" || v.Name == "lab" })
	o.Pod.Spec.Volumes = append(o.Pod.Spec.Volumes, corev1.Volume{Name: workspacePVC, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: workspacePVC}}})
```

  and after `h[corev1.ResourcePods] = resource.MustParse("2")`:

```go
	h[corev1.ResourcePersistentVolumeClaims] = resource.MustParse("1")
	h[corev1.ResourceRequestsStorage] = workspaceDisk
```

  Note: `docker` → SubPath `docker`, `lab` → SubPath `lab` (the loop uses the old name as the sub-path).
  `cluster.go` `provision`: after the `if awsSetup != nil { create = append(…awsSetup…) }` block, add

```go
	if o.PVC != nil {
		create = append(create, func() error {
			_, err := core.PersistentVolumeClaims(ns).Create(ctx, o.PVC, metav1.CreateOptions{})
			return err
		})
	}
```

- [ ] **Step 4: Sleep, wake, measure** — `internal/labs/aws.go` (add `"strconv"` and `ap "crucible/internal/agentproto"` to the imports):

```go
// SleepLab puts a ready lab to sleep (spec 2026-11-05 §3, §4): the workspace's containers stop cleanly, its pod goes (the
// volume keeps the disk), then the lab's machines hibernate where AWS can and stop otherwise. It returns "hibernated"
// when every machine hibernated, "stopped" when any one stopped, and "" when the lab has no machines.
func (a *AWSRunner) SleepLab(ctx context.Context, inst *Instance, lab *content.Lab) (string, error) {
	if !validLabID(inst.ID) || lab.AWS == nil {
		return "", errors.New("invalid aws lab")
	}
	release, err := a.acquire(ctx, inst.ID)
	if err != nil {
		return "", err
	}
	defer release()
	creds, err := a.cloud().AssumeLab(ctx, labSession(inst))
	if err != nil {
		return "", fmt.Errorf("getting lab credentials: %w", err)
	}
	ns := labNamespace(inst.ID)
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_ = a.Cluster.run(sctx, ns, []string{"docker", "compose", "stop"}, nil, &ap.Capped{}) // best effort: a pod already gone has nothing to stop
	cancel()
	if err := a.Cluster.deletePod(ctx, ns, labPod); err != nil {
		return "", fmt.Errorf("stopping the workspace: %w", err)
	}
	slept, err := a.cloud().Sleep(ctx, lab.AWS.Region, creds, inst.ID)
	if err != nil {
		return "", fmt.Errorf("putting the lab's machines to sleep: %w", err)
	}
	how := ""
	for _, s := range slept {
		if !s.Hibernated {
			return "stopped", nil
		}
		how = "hibernated"
	}
	return how, nil
}

// WakeLab brings a sleeping lab back: its machines start, its credentials are renewed, and its workspace pod comes back
// on the same volume, where Docker finds the trainee's container and starts it again (never a new one).
func (a *AWSRunner) WakeLab(ctx context.Context, inst *Instance, lab *content.Lab) error {
	if !validLabID(inst.ID) || lab.AWS == nil {
		return errors.New("invalid aws lab")
	}
	release, err := a.acquire(ctx, inst.ID)
	if err != nil {
		return err
	}
	defer release()
	creds, err := a.cloud().AssumeLab(ctx, labSession(inst))
	if err != nil {
		return fmt.Errorf("getting lab credentials: %w", err)
	}
	if err := a.putCreds(ctx, inst.ID, creds); err != nil {
		return fmt.Errorf("mounting the lab's credentials: %w", err)
	}
	if err := a.cloud().Wake(ctx, lab.AWS.Region, creds, inst.ID); err != nil {
		return fmt.Errorf("starting the lab's machines: %w", err)
	}
	ns := labNamespace(inst.ID)
	if err := a.Cluster.deletePod(ctx, ns, labPod); err != nil { // the dead pod a stopped node left behind
		return fmt.Errorf("starting the workspace: %w", err)
	}
	pod := awsWorkspace(clusterObjects(inst.ID, workspaceCompose, a.Cluster.Privileged)).Pod
	if _, err := a.Cluster.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("starting the workspace: %w", err)
	}
	if err := a.Cluster.waitReady(ctx, ns); err != nil {
		return err
	}
	up := &ap.Capped{}
	if err := a.Cluster.run(ctx, ns, []string{"docker", "compose", "up", "-d", "--no-recreate", "--wait", "--quiet-pull"}, nil, up); err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, tailOf(up.Bytes()))
	}
	return nil
}

// WorkspaceMiB is what the workspace's volume holds (Docker's storage and /lab), measured with du in its pod.
func (a *AWSRunner) WorkspaceMiB(ctx context.Context, inst *Instance) (int64, error) {
	if !validLabID(inst.ID) {
		return 0, errors.New("invalid lab id")
	}
	out := &ap.Capped{}
	if err := a.Cluster.run(ctx, labNamespace(inst.ID), []string{"du", "-sxm", "/var/lib/docker", "/lab"}, nil, out); err != nil {
		return 0, fmt.Errorf("measuring the workspace: %w", err)
	}
	var total int64
	for line := range strings.SplitSeq(strings.TrimSpace(string(out.Bytes())), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("du said %q", line)
		}
		total += n
	}
	return total, nil
}
```

- [ ] **Step 5: RBAC** — `deploy/helm/crucible/templates/rbac.yaml`: in the ClusterRole's `{{- if .Values.awsLabs.enabled }}` block add

```yaml
  - apiGroups: [""]
    resources: [persistentvolumeclaims]
    verbs: [create]               # aws labs: the workspace's disk; it goes with its namespace
```

  and in the ValidatingAdmissionPolicy's `{{- if .Values.awsLabs.enabled }}` resource rules add
  `- { apiGroups: [""], apiVersions: [v1], operations: [CREATE], resources: [persistentvolumeclaims] }`.
  `deploy/helm/test.sh`, after the `awsout=` assertions:

```bash
awsrbac=$(helm template t "$chart" $aws --show-only templates/rbac.yaml)
grep -q 'resources: \[persistentvolumeclaims\]' <<<"$awsrbac" || { echo "aws labs: the workspace volume may be created"; exit 1; }
grep -q 'operations: \[CREATE\], resources: \[persistentvolumeclaims\]' <<<"$awsrbac" || { echo "aws labs: admission confines the workspace volume to lab namespaces"; exit 1; }
if grep -q persistentvolumeclaims <<<"$rbac"; then echo "without aws labs, no volumes"; exit 1; fi
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/labs -count=1 && bash deploy/helm/test.sh`
Expected: PASS, `helm chart OK`.

- [ ] **Step 7: Commit**

```bash
git add internal/labs/aws_sleep_test.go
git commit -m "feat(labs): the aws workspace keeps its disk on a volume; SleepLab, WakeLab, WorkspaceMiB

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/aws_sleep_test.go internal/labs/aws_test.go internal/labs/cluster_objects.go internal/labs/aws_objects.go internal/labs/cluster.go internal/labs/aws.go deploy/helm/crucible/templates/rbac.yaml deploy/helm/test.sh
```

---

### Task 5: Bedtime, Wake it, the morning, and stop-all

**Files:**
- Create: `internal/labs/bedtime.go`
- Modify: `internal/labs/service.go` (Sweep calls bedtimeStep; quote's Blocked), `internal/labs/approvals.go` (Decide), `internal/labs/http.go` (wake route), `cmd/crucible/serve.go` (Morning at start)
- Test: `internal/labs/bedtime_test.go`; `internal/labs/aws_service_test.go` (fakeAWSRunner gains SleepLab/WakeLab)

**Interfaces:**
- Consumes: Task 1 states/columns, Task 3 `meterSleep`, `EndMeter`.
- Produces (package `labs`):

```go
const TeardownActor = "crucible aws teardown"
const bankingMsg = "The forge is banking for the night: labs start again in the morning."
type Bedtime struct { RequestedAt time.Time; By, Phase string }
func RequestBedtime(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error
func CancelBedtime(ctx context.Context, db *pgxpool.Pool) error
func ReadBedtime(ctx context.Context, db *pgxpool.Pool) (*Bedtime, error) // nil, nil when none
func AwakeAWSLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error) // "id (state)", aws labs provisioning/ready/waking
func ActiveLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error)   // "id (runtime, state)", every lab not yet ended
func StopAll(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error
func (s *Service) Morning(ctx context.Context) error
func (s *Service) Wake(ctx context.Context, u *auth.User, labID string) (*View, error)
type labSleeper interface {
	SleepLab(ctx context.Context, inst *Instance, lab *content.Lab) (string, error)
	WakeLab(ctx context.Context, inst *Instance, lab *content.Lab) error
}
```

  HTTP: `POST /api/labs/{id}/wake` → View.

- [ ] **Step 1: The fake runner sleeps too** — `internal/labs/aws_service_test.go`: add to `fakeAWSRunner`

```go
	slept, woke []string
	sleepAs     string        // "" = stopped
	sleepErr    error
	wakeErr     error
	wakeHold    chan struct{} // optional: WakeLab waits for it to close (machines slow to start)
```

  and the methods:

```go
func (f *fakeAWSRunner) SleepLab(_ context.Context, inst *Instance, _ *content.Lab) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slept = append(f.slept, inst.ID)
	if f.sleepAs == "" {
		return "stopped", f.sleepErr
	}
	return f.sleepAs, f.sleepErr
}

func (f *fakeAWSRunner) WakeLab(_ context.Context, inst *Instance, _ *content.Lab) error {
	if f.wakeHold != nil {
		<-f.wakeHold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.woke = append(f.woke, inst.ID)
	return f.wakeErr
}
```

- [ ] **Step 2: Failing tests** — `internal/labs/bedtime_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"crucible/internal/apperr"
)

func (f *fx) bedtime(t *testing.T) {
	t.Helper()
	if err := RequestBedtime(context.Background(), f.s.DB, "crucible labs bedtime", f.clk.Now()); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) phase(t *testing.T) string {
	t.Helper()
	b, err := ReadBedtime(context.Background(), f.s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		return ""
	}
	return b.Phase
}

func TestBedtimeClosesThenPutsReadyLabsToSleep(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.bedtime(t)
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || ml.Blocked != bankingMsg {
		t.Fatalf("no new lab starts at bedtime: %q %v", ml.Blocked, err)
	}
	f.s.Sweep(ctx) // settling: nothing is starting, so straight to sleeping
	got := f.waitState(t, f.u, id, Asleep)
	if got.SleptAs != "stopped" || got.SleptAt == nil {
		t.Fatalf("how it slept is kept: %+v", got)
	}
	run.mu.Lock()
	slept := slices.Clone(run.slept)
	run.mu.Unlock()
	if !slices.Equal(slept, []string{id}) {
		t.Fatalf("the runner put the lab to sleep once: %v", slept)
	}
	f.s.Sweep(ctx)
	if p := f.phase(t); p != "asleep" {
		t.Fatalf("every aws lab is asleep: phase %q", p)
	}
}

func TestBedtimeCancelsALabStillStartingAfterTenMinutes(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	run.provision = func() { <-hold }
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Provisioning)
	f.bedtime(t)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Provisioning {
		t.Fatalf("a lab still starting gets ten minutes: %s", got.State)
	}
	f.clk.Add(11 * time.Minute)
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, id)
	if (got.State != Destroying && got.State != Destroyed) || got.EndReason != "bedtime" {
		t.Fatalf("after ten minutes it is cancelled: %+v", got)
	}
}

func TestApprovalsWaitForTheMorning(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	v, err := f.s.Start(ctx, f.u, "forge", "forge-401", "01-cloud-heat", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.bedtime(t)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("an approval would start a lab: refused at bedtime: %v", err)
	}
}

func TestWakeBringsTheLabBack(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.bedtime(t)
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("nothing wakes while the platform is going to sleep: %v", err)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if p := f.phase(t); p != "" {
		t.Fatalf("the morning ends the bedtime: %q", p)
	}
	f.clk.Add(time.Minute)
	if _, err := f.s.Wake(ctx, f.other, id); !errors.Is(err, apperr.NotFound) && !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only its trainee wakes a lab: %v", err)
	}
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, id, Ready)
	if got.WokeAt == nil || !got.WokeAt.Equal(f.clk.Now()) || got.SleptAs != "stopped" {
		t.Fatalf("woke now, and the page knows how it slept: %+v", got)
	}
	if got.IdleDeadline == nil || !got.IdleDeadline.Equal(f.clk.Now().Add(time.Duration(got.IdleTimeoutS)*time.Second)) {
		t.Fatalf("the idle clock starts fresh: %+v", got.IdleDeadline)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if !slices.Equal(run.woke, []string{id}) {
		t.Fatalf("the runner woke it: %v", run.woke)
	}
}

func TestWakeAfterTheDeadlineIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	v := f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	f.clk.Set(v.EndsAt.Add(time.Minute))
	if _, err := f.s.Wake(ctx, f.u, id); !errors.Is(err, apperr.Conflict) || !containsMsg(err, "Time ran out on this lab while it slept.") {
		t.Fatalf("%v", err)
	}
}

func TestAFailedWakeLeavesTheLabAsleepWithItsError(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.wakeErr = errors.New("starting the lab's machines: InsufficientInstanceCapacity")
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, id, Asleep)
	got, _ := f.s.Get(ctx, f.u, id)
	if got.State != Asleep || got.Error == "" {
		t.Fatalf("asleep again, with what went wrong: %+v", got)
	}
}

func TestEndWhileWakingWins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	run.wakeHold = make(chan struct{})
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	if _, err := f.s.Wake(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.End(ctx, f.u, id); err != nil {
		t.Fatal(err)
	}
	close(run.wakeHold)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != "user" {
		t.Fatalf("the destroy wins over a wake in flight: %+v", got)
	}
}

func TestMorningEndsATeardownsKillSwitchButNotAnAdmins(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	if err := StopAll(ctx, f.s.DB, TeardownActor, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); !ks.Enabled || ks.ChangedBy != TeardownActor {
		t.Fatalf("teardown pauses every lab: %+v", ks)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); ks.Enabled {
		t.Fatal("the rebuilt platform is open again")
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('kill_switch.on', 'kill_switch.off')`).Scan(&n)
	if n != 2 {
		t.Fatalf("both changes are audited: %d", n)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Morning(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := f.s.KillSwitch(ctx); !ks.Enabled {
		t.Fatal("an admin's kill switch stays on")
	}
}

func TestStopAllListsWhatATeardownWaitsFor(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	f.asleep(t, id)
	left, err := ActiveLabs(ctx, f.s.DB)
	if err != nil || len(left) != 1 || left[0] != id+" (aws, asleep)" {
		t.Fatalf("%v %v", left, err)
	}
	if err := StopAll(ctx, f.s.DB, TeardownActor, f.clk.Now()); err != nil {
		t.Fatal(err)
	}
	f.s.Sweep(ctx) // the server ends every lab while the switch is on
	f.waitState(t, f.u, id, Destroyed)
	if left, _ := ActiveLabs(ctx, f.s.DB); len(left) != 0 {
		t.Fatalf("nothing left: %v", left)
	}
}

func containsMsg(err error, s string) bool { return err != nil && strings.Contains(err.Error(), s) }
```

  (add `"strings"` to the imports). Run: `go test ./internal/labs -run 'Bedtime|Approvals.*Morning|Wake|EndWhileWaking|Morning|StopAll' -count=1` → FAIL to compile.

- [ ] **Step 3: `internal/labs/bedtime.go`**

```go
package labs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/content"
)

// TeardownActor is who `crucible aws teardown` turns the kill switch on as; Morning turns off only its switch.
const TeardownActor = "crucible aws teardown"

const bankingMsg = "The forge is banking for the night: labs start again in the morning."

// settleFor is how long labs starting at bedtime get to finish before they are cancelled.
const settleFor = 10 * time.Minute

// Bedtime is the platform going to sleep (spec 2026-11-05 §2): one row while it lasts.
type Bedtime struct {
	RequestedAt time.Time
	By, Phase   string // settling | sleeping | asleep
}

// RequestBedtime starts the platform's bedtime (a no-op while one is under way); the server's sweep carries it out.
func RequestBedtime(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO platform_bedtime (requested_at, requested_by) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, now, by)
	return err
}

// CancelBedtime ends it: labs may start again (sleeping labs stay asleep until their trainees wake them).
func CancelBedtime(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `DELETE FROM platform_bedtime`)
	return err
}

func ReadBedtime(ctx context.Context, db *pgxpool.Pool) (*Bedtime, error) {
	var b Bedtime
	err := db.QueryRow(ctx, `SELECT requested_at, requested_by, phase FROM platform_bedtime`).Scan(&b.RequestedAt, &b.By, &b.Phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

func labList(ctx context.Context, db *pgxpool.Pool, sql, format string) ([]string, error) {
	rows, err := db.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := []string{}
	var id, runtime, state string
	_, err = pgx.ForEachRow(rows, []any{&id, &runtime, &state}, func() error {
		out = append(out, fmt.Sprintf(format, id, runtime, state))
		return nil
	})
	return out, err
}

// AwakeAWSLabs is every aws lab that would still run its machines overnight: starting, ready or waking.
func AwakeAWSLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	return labList(ctx, db, `SELECT id, runtime, state FROM lab_instances WHERE runtime = 'aws'
		AND state IN ('provisioning', 'ready', 'waking') ORDER BY id`, "%[1]s (%[3]s)")
}

// ActiveLabs is every lab not yet ended: what a teardown waits for.
func ActiveLabs(ctx context.Context, db *pgxpool.Pool) ([]string, error) {
	return labList(ctx, db, `SELECT id, runtime, state FROM lab_instances
		WHERE state IN ('provisioning', 'ready', 'asleep', 'waking', 'destroying') ORDER BY id`, "%s (%s, %s)")
}

// StopAll turns the kill switch on for a teardown, with its audit entry; the sweep then ends every lab.
func StopAll(ctx context.Context, db *pgxpool.Pool, by string, now time.Time) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE kill_switch SET enabled = true, changed_by = $1, changed_at = $2`, by, now); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, by, "kill_switch.on", "", map[string]any{"why": "teardown"}, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Morning runs once when the server starts (the platform is awake): its bedtime ends, and a kill switch a teardown
// turned on is turned off. Sleeping labs stay asleep until their trainees wake them; an admin's kill switch stays.
func (s *Service) Morning(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM platform_bedtime`); err != nil {
		return err
	}
	var since *time.Time
	err = tx.QueryRow(ctx, `SELECT changed_at FROM kill_switch WHERE enabled AND changed_by = $1 FOR UPDATE`, TeardownActor).Scan(&since)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		now := s.Now()
		if since != nil { // escalation was paused: give pending requests their remaining time back, as SetKillSwitch does
			if _, err := tx.Exec(ctx, `UPDATE lab_instances SET escalate_at = escalate_at + ($1::timestamptz - $2::timestamptz)
				WHERE state = 'pending_approval'`, now, *since); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE kill_switch SET enabled = false, changed_by = 'crucible serve', changed_at = $1`, now); err != nil {
			return err
		}
		if err := audit.Log(ctx, tx, "crucible serve", "kill_switch.off", "", map[string]any{"after": TeardownActor}, ""); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// labSleeper is a runtime whose labs can sleep with the platform (aws).
type labSleeper interface {
	SleepLab(ctx context.Context, inst *Instance, lab *content.Lab) (string, error)
	WakeLab(ctx context.Context, inst *Instance, lab *content.Lab) error
}

// bedtimeStep (every sweep) carries out a requested bedtime: labs starting get settleFor, then one still starting is
// cancelled; every ready aws lab is put to sleep in the background; the phase is asleep once none is awake.
func (s *Service) bedtimeStep(ctx context.Context) {
	b, err := ReadBedtime(ctx, s.DB)
	if err != nil || b == nil {
		return
	}
	now := s.Now()
	if b.Phase == "settling" {
		var starting int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_instances WHERE runtime = 'aws' AND state = 'provisioning'`).Scan(&starting); err != nil {
			s.Log.Error("bedtime: counting starting labs failed", "err", err)
			return
		}
		if starting > 0 && now.Before(b.RequestedAt.Add(settleFor)) {
			return
		}
		rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'provisioning'`)
		if err != nil {
			s.Log.Error("bedtime: listing starting labs failed", "err", err)
			return
		}
		late, err := collectInst(rows)
		if err != nil {
			s.Log.Error("bedtime: listing starting labs failed", "err", err)
			return
		}
		for _, inst := range late {
			s.destroy(ctx, inst, "bedtime")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'sleeping'`); err != nil {
			s.Log.Error("bedtime: moving on failed", "err", err)
			return
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("bedtime: listing ready labs failed", "err", err)
		return
	}
	ready, err := collectInst(rows)
	if err != nil {
		s.Log.Error("bedtime: listing ready labs failed", "err", err)
		return
	}
	for _, inst := range ready {
		s.once("sleep:"+inst.ID, func() { s.sleepLab(context.WithoutCancel(ctx), inst) }) // its own slot: a destroy keeps the lab id's
	}
	if awake, err := AwakeAWSLabs(ctx, s.DB); err == nil && len(awake) == 0 {
		_, _ = s.DB.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`)
	}
}

// sleepLab puts one ready aws lab to sleep. A failure leaves it ready (the next sweep tries again) and is logged.
func (s *Service) sleepLab(ctx context.Context, inst *Instance) {
	sl, ok := s.Runners[inst.Runtime].(labSleeper)
	if !ok {
		return
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		s.Log.Error("bedtime: the lab's content is gone", "lab", inst.ID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	how, err := sl.SleepLab(ctx, inst, lab)
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	if err != nil {
		s.Log.Error("bedtime: a lab did not go to sleep", "lab", inst.ID, "err", err)
		s.event(wctx, inst.ID, "sleep_failed", cleanText(err.Error()))
		return
	}
	now := s.Now()
	tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'asleep', slept_at = $2, slept_as = $3 WHERE id = $1 AND state = 'ready'`,
		inst.ID, now, how)
	if err != nil || tag.RowsAffected() == 0 {
		return // ended meanwhile: its destroy removes the sleeping machines
	}
	s.event(wctx, inst.ID, "sleep", how)
	if err := s.meterSleep(wctx, inst.ID, now); err != nil {
		s.Log.Error("metering the lab's sleep failed; it stays priced as running", "lab", inst.ID, "err", err)
	}
}

// Wake (the lab's trainee): a sleeping lab's machines start and its workspace comes back, in the background.
func (s *Service) Wake(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State != Asleep {
		return nil, apperr.Wrap(apperr.Conflict, "this lab isn't asleep")
	}
	if b, err := ReadBedtime(ctx, s.DB); err != nil {
		return nil, err
	} else if b != nil {
		return nil, apperr.Wrap(apperr.Conflict, bankingMsg)
	}
	if inst.EndsAt != nil && !inst.EndsAt.After(s.Now()) {
		return nil, apperr.Wrap(apperr.Conflict, "Time ran out on this lab while it slept.")
	}
	sl, ok := s.Runners[inst.Runtime].(labSleeper)
	if !ok {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs can't be woken on this server", inst.Runtime))
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		return nil, err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'waking', waking_since = $2, error = '' WHERE id = $1 AND state = 'asleep'`,
		inst.ID, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "the lab is changing state; try again")
	}
	s.event(ctx, inst.ID, "waking", "")
	// its own slot: a destroy (End during the wake) takes the lab id's and waits for the runner's per-lab lock
	s.once("wake:"+inst.ID, func() { s.wakeLab(context.WithoutCancel(ctx), inst, lab, sl) })
	return s.Get(ctx, u, labID)
}

func (s *Service) wakeLab(ctx context.Context, inst *Instance, lab *content.Lab, sl labSleeper) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := sl.WakeLab(ctx, inst, lab)
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	if err != nil {
		s.Log.Warn("waking a lab failed", "lab", inst.ID, "err", err)
		s.endRow(wctx, inst.ID, `UPDATE lab_instances SET state = 'asleep', waking_since = NULL, error = $2
			WHERE id = $1 AND state = 'waking'`, cleanText(err.Error()))
		s.event(wctx, inst.ID, "wake_failed", cleanText(err.Error()))
		return
	}
	now := s.Now()
	tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'ready', woke_at = $2, last_activity_at = $2, waking_since = NULL
		WHERE id = $1 AND state = 'waking'`, inst.ID, now)
	if err != nil || tag.RowsAffected() == 0 {
		return // ended while waking: that destroy removes everything
	}
	s.event(wctx, inst.ID, "woke", "")
	if err := s.EndMeter(wctx, inst.ID, now); err != nil { // the sleeping rows end; the next sweep meters it running
		s.Log.Error("ending the lab's sleeping meter failed", "lab", inst.ID, "err", err)
	}
}
```

- [ ] **Step 4: Wire it in**
  - `service.go` `Sweep`: right after the kill-switch block (`paused = true …}`), add `s.bedtimeStep(ctx)`.
  - `service.go` `quote`: replace the Blocked chain's first branches with

```go
	if ks.Enabled {
		q.Blocked = "Labs are paused by an admin."
	} else if b, err := ReadBedtime(ctx, s.DB); err != nil {
		return nil, err
	} else if b != nil {
		q.Blocked = bankingMsg
	} else if sc := p.ProgramSchedule(team, training); !sc.Open(now) {
```

    (and update the comment: `// Blocked precedence: kill switch, bedtime, then schedule window.`).
  - `approvals.go` `Decide`: right after the `paused` kill-switch check, add

```go
		if b, err := ReadBedtime(ctx, s.DB); err != nil {
			return "", err
		} else if b != nil {
			return "", apperr.Wrap(apperr.Conflict, bankingMsg)
		}
```

  - `http.go`: after the `/extend` route add

```go
	r.Post("/api/labs/{id}/wake", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Wake(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
```

  - `cmd/crucible/serve.go`: after `labSvc := &labs.Service{…}` is fully set up (after `labSvc.Scoring, labSvc.Blobs = …`), add

```go
	if err := labSvc.Morning(ctx); err != nil { // the platform is awake: its bedtime (and a teardown's kill switch) ends
		return fmt.Errorf("waking the labs: %w", err)
	}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/labs ./cmd/crucible -count=1`
Expected: PASS.

- [ ] **Step 6: Docs** — `docs/user/trainees/cluster-and-aws-labs.md`: add a section `## Overnight` saying, from the
  trainee's side: an AWS lab sleeps when Crucible does (about 19:00); its machines hibernate (they resume exactly) or
  stop (they restart from their disks); its workspace keeps its files; open it in the morning and press **Wake it**;
  the lab's time keeps running while it sleeps, so a lab that runs out overnight is over. `docs/user/admins/forge-status-and-kill-switch.md`:
  one paragraph that a teardown turns the kill switch on as `crucible aws teardown` and the rebuilt server turns that
  switch off when it starts; an admin's own switch stays on. Run `go test ./internal/docs -count=1` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/labs/bedtime.go internal/labs/bedtime_test.go
git commit -m "feat(labs): bedtime puts aws labs to sleep, Wake it brings one back, the morning reopens the platform

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/bedtime.go internal/labs/bedtime_test.go internal/labs/aws_service_test.go internal/labs/service.go internal/labs/approvals.go internal/labs/http.go cmd/crucible/serve.go docs/user/trainees/cluster-and-aws-labs.md docs/user/admins/forge-status-and-kill-switch.md
```

---

### Task 6: The workspace and node disk guards

**Files:**
- Create: `internal/labs/diskguard.go`
- Modify: `internal/labs/service.go` (Service.NodeStorage, measuredAt, Sweep, quote, View.WorkspaceWarning, view()), `cmd/crucible/serve.go`, `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/test.sh`
- Test: `internal/labs/diskguard_test.go`; `internal/labs/aws_service_test.go` (fakeAWSRunner.WorkspaceMiB)

**Interfaces:**
- Consumes: Task 4's `AWSRunner.WorkspaceMiB` (through the interface below), Task 5's sweep.
- Produces: `Service.NodeStorage string` (env `CRUCIBLE_NODE_STORAGE`); `View.WorkspaceWarning bool (json "workspace_warning,omitempty")`; `diskFullMsg`, `minNodeFree` (var), `workspaceLimitMiB = 20480`.

- [ ] **Step 1: The fake measures** — `aws_service_test.go` `fakeAWSRunner`: add `mib int64` and

```go
func (f *fakeAWSRunner) WorkspaceMiB(context.Context, *Instance) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mib, nil
}
```

- [ ] **Step 2: Failing tests** — `internal/labs/diskguard_test.go`:

```go
package labs

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestAWorkspaceOverTwentyGiBIsWarnedThenPutOut(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	run, _ := f.withAWS(t)
	id := f.approvedAWS(t)
	f.waitState(t, f.u, id, Ready)
	run.mu.Lock()
	run.mib = workspaceLimitMiB + 1
	run.mu.Unlock()
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Ready || !got.WorkspaceWarning {
		t.Fatalf("warned once first: %+v", got)
	}
	f.s.Sweep(ctx) // within five minutes: not measured again
	if got, _ := f.s.Get(ctx, f.u, id); got.State != Ready {
		t.Fatalf("measured every five minutes, not every sweep: %s", got.State)
	}
	f.clk.Add(5 * time.Minute)
	f.s.Sweep(ctx)
	if got := f.waitState(t, f.u, id, Destroyed); got.EndReason != "workspace_full" {
		t.Fatalf("still over at the next measure: put out: %q", got.EndReason)
	}
}

func TestANearlyFullNodeTakesNoNewAWSLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.withAWS(t)
	f.s.NodeStorage = t.TempDir()
	old := minNodeFree
	minNodeFree = math.MaxInt64 // whatever this disk has free, it is less
	t.Cleanup(func() { minNodeFree = old })
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat")
	if err != nil || ml.Blocked != diskFullMsg {
		t.Fatalf("%q %v", ml.Blocked, err)
	}
	minNodeFree = 0
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-401", "01-cloud-heat"); ml.Blocked != "" {
		t.Fatalf("room enough: %q", ml.Blocked)
	}
}
```

  Run: `go test ./internal/labs -run 'TwentyGiB|NearlyFull' -count=1` → FAIL to compile.

- [ ] **Step 3: `internal/labs/diskguard.go`**

```go
package labs

import (
	"context"
	"syscall"
	"time"
)

const (
	workspaceLimitMiB = 20 * 1024 // the workspace volume's size; local-path enforces none, so Crucible measures
	workspaceEvery    = 5 * time.Minute
	diskFullMsg       = "The forge's disk is nearly full: try again later, or ask an admin."
)

// minNodeFree is the node disk free space below which no new aws lab starts (a var for tests).
var minNodeFree = int64(15 << 30)

// workspaceMeter measures a lab's workspace volume (AWSRunner.WorkspaceMiB).
type workspaceMeter interface {
	WorkspaceMiB(ctx context.Context, inst *Instance) (int64, error)
}

// nodeFull reports whether the node's lab storage (CRUCIBLE_NODE_STORAGE, local-path's directory mounted read-only)
// has less than minNodeFree left. Unreadable counts as full: the operator set the path, so a wrong one must show.
func (s *Service) nodeFull() bool {
	if s.NodeStorage == "" {
		return false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.NodeStorage, &st); err != nil {
		s.Log.Error("reading the node's free disk failed", "path", s.NodeStorage, "err", err)
		return true
	}
	return int64(st.Bavail)*int64(st.Bsize) < minNodeFree
}

// guardWorkspaces (each sweep) measures every ready aws lab's workspace at most every workspaceEvery: over the limit it
// is warned once, and still over at the next measure the lab is put out.
func (s *Service) guardWorkspaces(ctx context.Context) {
	wm, ok := s.Runners["aws"].(workspaceMeter)
	if !ok {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("workspace guard query failed", "err", err)
		return
	}
	labs, err := collectInst(rows)
	if err != nil {
		s.Log.Error("workspace guard query failed", "err", err)
		return
	}
	now := s.Now()
	for _, inst := range labs {
		s.measureMu.Lock()
		if s.measuredAt == nil {
			s.measuredAt = map[string]time.Time{}
		}
		last, seen := s.measuredAt[inst.ID]
		if seen && now.Sub(last) < workspaceEvery {
			s.measureMu.Unlock()
			continue
		}
		s.measuredAt[inst.ID] = now
		s.measureMu.Unlock()
		mib, err := wm.WorkspaceMiB(ctx, inst)
		if err != nil {
			s.Log.Warn("measuring a workspace failed", "lab", inst.ID, "err", err)
			continue
		}
		if mib <= workspaceLimitMiB {
			continue
		}
		var warned bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'workspace_full_warning')`,
			inst.ID).Scan(&warned); err != nil {
			continue
		}
		if !warned {
			s.event(ctx, inst.ID, "workspace_full_warning", "")
			continue
		}
		s.destroy(ctx, inst, "workspace_full")
	}
}
```

- [ ] **Step 4: Wire it in** — `service.go`: add to `Service`

```go
	NodeStorage string // CRUCIBLE_NODE_STORAGE: the node's lab volume directory, read-only, for the free-disk guard; "" = off

	measureMu  sync.Mutex
	measuredAt map[string]time.Time // per aws lab: its last workspace measure in this process
```

  In `Sweep`, after `s.refreshAWS(ctx)` add `s.guardWorkspaces(ctx)`. In `quote`, in the `if lab.Runtime == "aws" && lab.AWS != nil && q.Blocked == ""` switch, add a last case
  `case s.nodeFull(): q.Blocked = diskFullMsg`. In `View` add
  `WorkspaceWarning bool `json:"workspace_warning,omitempty"` // over the workspace limit once: put out at the next measure`,
  and in `view()`, inside `if inst.State == Ready {`, add

```go
		_ = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'workspace_full_warning')`,
			inst.ID).Scan(&v.WorkspaceWarning)
```

  `cmd/crucible/serve.go`: in the `labs.Service{…}` literal add `NodeStorage: os.Getenv("CRUCIBLE_NODE_STORAGE")`.

- [ ] **Step 5: Helm** — `deploy/helm/crucible/templates/crucible.yaml`: inside the `{{- if .Values.awsLabs.enabled }}` env block, after `CRUCIBLE_NODE_IP`'s entry, add
  `- { name: CRUCIBLE_NODE_STORAGE, value: /node-storage }   # free-disk guard: no new aws lab below 15 GiB`;
  replace the `volumeMounts:` and `volumes:` blocks' tails with:

```yaml
          volumeMounts:
            - { name: data, mountPath: /data }
            - { name: git-credentials, mountPath: /etc/crucible/git-credentials, subPath: git-credentials, readOnly: true }
            {{- if .Values.awsLabs.enabled }}
            - { name: node-storage, mountPath: /node-storage, readOnly: true }
            {{- end }}
      volumes:
        - name: data
          emptyDir: {}        # git mirrors and exports: rebuilt from git on start
        - name: git-credentials
          secret: { secretName: {{ .Values.secretName }}, items: [{ key: git-credentials, path: git-credentials }] }
        {{- if .Values.awsLabs.enabled }}
        - name: node-storage  # k3s local-path: where workspace volumes live; read for its free space only
          hostPath: { path: /var/lib/rancher/k3s/storage, type: DirectoryOrCreate }
        {{- end }}
```

  `deploy/helm/test.sh`, after the `awsout` loop:

```bash
grep -q 'name: CRUCIBLE_NODE_STORAGE, value: /node-storage' <<<"$awsout" || { echo "aws labs: the free-disk guard"; exit 1; }
grep -q 'mountPath: /node-storage, readOnly: true' <<<"$awsout" || { echo "aws labs: the node storage is read-only"; exit 1; }
if grep -q node-storage <<<"$out"; then echo "no node storage without aws labs"; exit 1; fi
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/labs ./cmd/crucible -count=1 && bash deploy/helm/test.sh`
Expected: PASS, `helm chart OK`.

- [ ] **Step 7: Commit**

```bash
git add internal/labs/diskguard.go internal/labs/diskguard_test.go
git commit -m "feat(labs): a workspace over 20 GiB is warned then put out; a nearly full node takes no new aws lab

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/diskguard.go internal/labs/diskguard_test.go internal/labs/aws_service_test.go internal/labs/service.go cmd/crucible/serve.go deploy/helm/crucible/templates/crucible.yaml deploy/helm/test.sh
```

---

### Task 7: `crucible labs`, the nightly CronJob, and the main stack

**Files:**
- Create: `cmd/crucible/labs.go`, `cmd/crucible/labs_test.go`, `deploy/helm/crucible/templates/bedtime.yaml`
- Modify: `cmd/crucible/main.go` (dispatch, usage), `deploy/helm/crucible/values.yaml`, `deploy/helm/test.sh`,
  `deploy/aws/main/variables.tf`, `deploy/aws/main/main.tf` (helm_values), `deploy/aws/main/main.tftest.hcl`

**Interfaces:**
- Consumes: Task 5's `labs.RequestBedtime/CancelBedtime/ReadBedtime/AwakeAWSLabs/ActiveLabs/StopAll/TeardownActor`.
- Produces: `crucible labs bedtime [--wait DURATION] [--cancel]`, `crucible labs stop-all [--wait DURATION]`,
  `crucible labs status` (exit 0, or 1 when `--wait` ran out, listing what is left; 2 on usage errors); Helm value
  `labs.sleepSchedule`; main stack variable `labs_sleep_cron`; `disk_gb` default 100.

- [ ] **Step 1: Failing tests** — `cmd/crucible/labs_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"crucible/internal/db/dbtest"
	"crucible/internal/labs"
)

func TestLabsCommandsNeedADatabaseAndAKnownCommand(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var errw bytes.Buffer
	if code := runMain([]string{"labs", "bedtime"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "DATABASE_URL") {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	errw.Reset()
	if code := runMain([]string{"labs", "nap"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "crucible labs bedtime") {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
}

func TestLabsBedtimeWaitsUntilEveryLabIsAsleep(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	old := labsPoll
	labsPoll = time.Millisecond
	t.Cleanup(func() { labsPoll = old })
	if code := runMain([]string{"labs", "bedtime", "--wait", "50ms"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("no server carries it out here, so the wait runs out: exit %d", code)
	}
	if b, _ := labs.ReadBedtime(ctx, pool); b == nil || b.Phase != "settling" {
		t.Fatalf("the bedtime is requested: %+v", b)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_bedtime SET phase = 'asleep'`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runMain([]string{"labs", "bedtime", "--wait", "1s"}, &out, io.Discard); code != 0 || !strings.Contains(out.String(), "every AWS lab is asleep") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if code := runMain([]string{"labs", "bedtime", "--cancel"}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	if b, _ := labs.ReadBedtime(ctx, pool); b != nil {
		t.Fatalf("cancelled: %+v", b)
	}
}

func TestLabsStopAllTurnsTheKillSwitchOnAsTheTeardown(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	if code := runMain([]string{"labs", "stop-all", "--wait", "1s"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("no labs: done at once: exit %d", code)
	}
	var on bool
	var by string
	if err := pool.QueryRow(ctx, `SELECT enabled, changed_by FROM kill_switch`).Scan(&on, &by); err != nil || !on || by != labs.TeardownActor {
		t.Fatalf("%v %q %v", on, by, err)
	}
}
```

  Run: `go test ./cmd/crucible -run Labs -count=1` → FAIL (`labsPoll` undefined, `labs` unknown command).

- [ ] **Step 2: `cmd/crucible/labs.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"crucible/internal/db"
	"crucible/internal/labs"
)

const labsUsage = `usage:
  crucible labs bedtime [--wait DURATION] [--cancel]
  crucible labs stop-all [--wait DURATION]
  crucible labs status

These run where the server's database is (DATABASE_URL): the nightly CronJob, and crucible aws sleep / teardown over SSM.
bedtime asks the server to put every AWS lab to sleep (labs starting get 10 minutes); --wait waits until they are
asleep, --cancel ends the bedtime. stop-all turns the kill switch on as crucible aws teardown and, with --wait, waits
until every lab has ended. status prints the bedtime and the AWS labs by state.`

// labsPoll is how often --wait looks again (a var for tests).
var labsPoll = 5 * time.Second

func labsCmd(args []string, out, errw io.Writer) int {
	if len(args) == 0 || (args[0] != "bedtime" && args[0] != "stop-all" && args[0] != "status") {
		fmt.Fprintln(errw, labsUsage)
		return 2
	}
	fs := flag.NewFlagSet("labs "+args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	wait := fs.Duration("wait", 0, "wait this long for the server to finish")
	cancel := fs.Bool("cancel", false, "bedtime: end it (labs may start again)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(errw, "crucible labs needs DATABASE_URL (it runs in the server's pod)")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, url)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	defer pool.Close()
	// until polls left until it is empty or the wait runs out; it prints what is left when it ran out.
	until := func(left func() ([]string, error), done string) int {
		deadline := time.Now().Add(*wait)
		for {
			l, err := left()
			if err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
			if len(l) == 0 {
				fmt.Fprintln(out, done)
				return 0
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(errw, "still waiting after %s:\n  %s\n", *wait, strings.Join(l, "\n  "))
				return 1
			}
			select {
			case <-ctx.Done():
				return 1
			case <-time.After(labsPoll):
			}
		}
	}
	switch args[0] {
	case "bedtime":
		if *cancel {
			if err := labs.CancelBedtime(ctx, pool); err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
			fmt.Fprintln(out, "bedtime cancelled: labs may start again")
			return 0
		}
		if err := labs.RequestBedtime(ctx, pool, "crucible labs bedtime", time.Now()); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		if *wait == 0 {
			fmt.Fprintln(out, "bedtime requested: the server puts AWS labs to sleep")
			return 0
		}
		return until(func() ([]string, error) {
			b, err := labs.ReadBedtime(ctx, pool)
			if err != nil || b == nil || b.Phase == "asleep" {
				return nil, err
			}
			l, err := labs.AwakeAWSLabs(ctx, pool)
			if len(l) == 0 && err == nil {
				l = []string{"the server (phase " + b.Phase + ")"}
			}
			return l, err
		}, "every AWS lab is asleep")
	case "stop-all":
		if err := labs.StopAll(ctx, pool, labs.TeardownActor, time.Now()); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		return until(func() ([]string, error) { return labs.ActiveLabs(ctx, pool) }, "every lab has ended")
	default: // status
		b, err := labs.ReadBedtime(ctx, pool)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		phase := "none"
		if b != nil {
			phase = b.Phase + " since " + b.RequestedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(out, "bedtime: %s\n", phase)
		rows, err := pool.Query(ctx, `SELECT state, count(*) FROM lab_instances WHERE runtime = 'aws'
			AND state IN ('provisioning', 'ready', 'asleep', 'waking', 'destroying') GROUP BY state ORDER BY state`)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		defer rows.Close()
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				fmt.Fprintf(out, "aws labs %s: %d\n", st, n)
			}
		}
		if p := os.Getenv("CRUCIBLE_NODE_STORAGE"); p != "" {
			var st syscall.Statfs_t
			if syscall.Statfs(p, &st) == nil {
				fmt.Fprintf(out, "node disk free: %.1f GiB\n", float64(int64(st.Bavail)*int64(st.Bsize))/(1<<30))
			}
		}
		return 0
	}
}
```

  `cmd/crucible/main.go`: in `runMain` add `case "labs": return labsCmd(args[1:], out, errw)`; in `usage` add the line
  `  crucible labs bedtime [--wait D] [--cancel] | stop-all [--wait D] | status` after the `ready` line.

- [ ] **Step 3: The CronJob** — `deploy/helm/crucible/templates/bedtime.yaml`:

```yaml
{{- if and .Values.awsLabs.enabled .Values.labs.sleepSchedule }}
# Nightly bedtime (spec 2026-11-05 §2): asks the server to put every AWS lab to sleep before the node stops.
apiVersion: batch/v1
kind: CronJob
metadata: { name: crucible-bedtime }
spec:
  schedule: {{ .Values.labs.sleepSchedule | quote }}
  timeZone: {{ .Values.backup.timeZone | quote }}
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 2
      template:
        spec:
          restartPolicy: OnFailure
          {{- with .Values.image.pullSecret }}
          imagePullSecrets:
            - name: {{ . }}
          {{- end }}
          containers:
            - name: bedtime
              image: "{{ include "crucible.image" . }}"
              imagePullPolicy: {{ .Values.image.pullPolicy }}
              command: ["crucible", "labs", "bedtime"]
              resources: { requests: { cpu: 50m, memory: 64Mi }, limits: { cpu: 100m, memory: 128Mi } }
              env:
                - name: POSTGRES_PASSWORD
                  valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: POSTGRES_PASSWORD } }
                - { name: DATABASE_URL, value: "postgres://crucible:$(POSTGRES_PASSWORD)@postgres:5432/crucible?sslmode=disable" }
{{- end }}
```

  `values.yaml`, after the `backup:` block:

```yaml
labs:
  sleepSchedule: ""   # nightly bedtime for AWS labs (crucible labs bedtime), 5-field cron in backup.timeZone; empty = none
```

  `deploy/helm/test.sh`, after the Task 6 lines:

```bash
if grep -q crucible-bedtime <<<"$awsout"; then echo "no bedtime without a schedule"; exit 1; fi
bed=$(helm template t "$chart" $aws --set-string labs.sleepSchedule="0 19 * * 0-4")
for want in 'name: crucible-bedtime' 'schedule: "0 19 \* \* 0-4"' 'timeZone: "Europe/Bucharest"' 'command: \["crucible", "labs", "bedtime"\]'; do
  grep -q -- "$want" <<<"$bed" || { echo "missing in the bedtime CronJob: $want"; exit 1; }
done
```

- [ ] **Step 4: The main stack** — `deploy/aws/main/variables.tf`: `disk_gb` default `60` → `100`; after `backup_cron` add

```hcl
variable "labs_sleep_cron" {
  type        = string
  default     = "0 19 * * *"
  description = "Kubernetes cron (node timezone = schedule_timezone): AWS labs go to sleep (crucible labs bedtime); keep it 30 minutes before sleep_cron and before backup_cron"
  validation {
    condition     = can(regex("^\\S+ \\S+ \\S+ \\S+ \\S+$", var.labs_sleep_cron))
    error_message = "labs_sleep_cron is a 5-field Kubernetes cron, e.g. \"0 19 * * 0-4\""
  }
}
```

  `main.tf` `helm_values`: in the outer `merge({ awsLabs = …`, add a sibling key to `awsLabs`:
  `labs = { sleepSchedule = var.schedule_enabled ? var.labs_sleep_cron : "" }`. `main.tftest.hcl`:

```hcl
run "labs_sleep_cron_reaches_helm" {
  command = plan
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).labs.sleepSchedule == "0 19 * * *"
    error_message = "the nightly bedtime reaches the chart"
  }
  assert {
    condition     = aws_instance.node.root_block_device[0].volume_size == 100
    error_message = "the node disk holds sleeping workspaces: 100 GB by default"
  }
}

run "no_bedtime_without_the_schedule" {
  command = plan
  variables { schedule_enabled = false }
  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).labs.sleepSchedule == ""
    error_message = "no schedule, no nightly bedtime"
  }
}

run "labs_sleep_cron_is_a_kubernetes_cron" {
  command         = plan
  variables { labs_sleep_cron = "cron(0 19 ? * MON-FRI *)" }
  expect_failures = [var.labs_sleep_cron]
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/crucible -count=1 && bash deploy/helm/test.sh && (cd deploy/aws/main && terraform test)`
Expected: PASS, `helm chart OK`, `Success!`. (Never `terraform init` in `deploy/aws/main`: its s3 backend would reach AWS.)

- [ ] **Step 6: Commit**

```bash
git add cmd/crucible/labs.go cmd/crucible/labs_test.go deploy/helm/crucible/templates/bedtime.yaml
git commit -m "feat: crucible labs bedtime/stop-all/status, the nightly bedtime CronJob, labs_sleep_cron and a 100 GB node disk

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- cmd/crucible/labs.go cmd/crucible/labs_test.go cmd/crucible/main.go deploy/helm/crucible/templates/bedtime.yaml deploy/helm/crucible/values.yaml deploy/helm/test.sh deploy/aws/main/variables.tf deploy/aws/main/main.tf deploy/aws/main/main.tftest.hcl
```

---

### Task 8: `crucible aws sleep` and `teardown` look after the labs

**Files:**
- Modify: `internal/awsops/awsops.go` (SleepNode, Teardown, Status, helpers), `cmd/crucible/main.go` (`--keep-labs`, usage)
- Test: `internal/awsops/awsops_test.go` (new tests; update every existing `SleepNode(`/`Teardown(` call to the new signatures)

**Interfaces:**
- Consumes: the CLI strings of Task 7 only (`crucible labs bedtime --wait 20m`, `bedtime --cancel`, `stop-all --wait 30m`, `status`).
- Produces: `func (o Ops) SleepNode(ctx context.Context, noSnapshot, keepLabs bool) error`,
  `func (o Ops) Teardown(ctx context.Context, varFile string, yes, noSnapshot, keepLabs bool) error`.

- [ ] **Step 1: Failing tests** — append to `internal/awsops/awsops_test.go`:

```go
func TestSleepPutsTheLabsToSleepBeforeTheSnapshot(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).SleepNode(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	bed, snap, stop := indexOf(r.calls, "crucible labs bedtime --wait 20m"), indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, "stop-instances")
	if bed < 0 || !(bed < snap && snap < stop) {
		t.Fatalf("labs asleep, then the snapshot (which records them asleep), then the node: %v", r.calls)
	}
	if !strings.Contains(r.calls[bed], "KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl -n crucible exec deploy/crucible -c api -- crucible labs") {
		t.Fatalf("run in the server's pod: %s", r.calls[bed])
	}
}

func TestSleepCancelsAFailedBedtimeAndKeepsTheNodeUp(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "send-command") && strings.Contains(cmd, "bedtime --wait") {
			return errors.New("still waiting after 20m0s: 3c1f… (ready)")
		}
		return nil
	}}
	err := r.ops(t.TempDir()).SleepNode(context.Background(), false, false)
	if err == nil || !strings.Contains(err.Error(), "--keep-labs") {
		t.Fatalf("it says what to do: %v", err)
	}
	if indexOf(r.calls, "crucible labs bedtime --cancel") < 0 || indexOf(r.calls, "snapshot.sh") >= 0 || indexOf(r.calls, "stop-instances") >= 0 {
		t.Fatalf("the bedtime is cancelled and the node stays up: %v", r.calls)
	}
}

func TestSleepKeepLabsWarnsAndSkipsTheBedtime(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).SleepNode(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "crucible labs") >= 0 || !strings.Contains(r.log.String(), "keep running and billing") {
		t.Fatalf("%v\n%s", r.calls, r.log.String())
	}
}

func TestTeardownEndsEveryLabFirst(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false, false); err != nil {
		t.Fatal(err)
	}
	end, snap, destroy := indexOf(r.calls, "crucible labs stop-all --wait 30m"), indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, " destroy -auto-approve")
	if end < 0 || !(end < snap && snap < destroy) {
		t.Fatalf("labs end, then the snapshot, then the destroy: %v", r.calls)
	}
}

func TestTeardownRefusesAStoppedNodeUnlessKeepLabs(t *testing.T) {
	stopped := func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return "stopped"
		}
		return fakeAWS(cmd)
	}
	r := &recorder{output: stopped}
	err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false, false)
	if err == nil || !strings.Contains(err.Error(), "crucible aws wake") || indexOf(r.calls, " destroy ") >= 0 {
		t.Fatalf("a stopped server can't end its labs: %v %v", err, r.calls)
	}
	r = &recorder{output: stopped}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false, true); err != nil {
		t.Fatalf("--keep-labs tears down anyway: %v", err)
	}
}

func TestTeardownListsWhatIsStillTagged(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		switch {
		case strings.Contains(cmd, "labs output -json"):
			return `{"regions":{"value":"eu-west-1,eu-central-1"}}`
		case strings.Contains(cmd, "get-resources") && strings.Contains(cmd, "eu-west-1"):
			return "arn:aws:ec2:eu-west-1:1:instance/i-0abc"
		}
		return fakeAWS(cmd)
	}}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.log.String(), "arn:aws:ec2:eu-west-1:1:instance/i-0abc") || indexOf(r.calls, "get-resources --region eu-central-1") < 0 {
		t.Fatalf("every lab region is checked, leftovers named: %s", r.log.String())
	}
}
```

  Then update the existing tests that call `SleepNode(ctx, x)` → `SleepNode(ctx, x, true)` and
  `Teardown(ctx, f, yes, noSnapshot)` → `Teardown(ctx, f, yes, noSnapshot, true)` (they test the snapshot and node
  states, not the labs). Run: `go test ./internal/awsops -count=1` → FAIL to compile.

- [ ] **Step 2: Implement** — `internal/awsops/awsops.go`:

```go
// labsOnNode runs `crucible labs <args>` in the server's pod, as root on the node.
func labsOnNode(args string) string {
	return "KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl -n crucible exec deploy/crucible -c api -- crucible labs " + args
}
```

  `SleepNode(ctx, noSnapshot, keepLabs bool)`: right after reading `m`, before the snapshot block:

```go
	if keepLabs {
		fmt.Fprintln(o.Log, "WARNING: --keep-labs: AWS labs are left as they are; they keep running and billing while the node sleeps")
	} else if st, err := o.state(ctx, m); err == nil && st == "running" {
		if _, err := o.ssmOut(ctx, m, labsOnNode("bedtime --wait 20m")); err != nil {
			_ = o.ssm(ctx, m, labsOnNode("bedtime --cancel")) // the node stays up, so the platform must stay open
			return fmt.Errorf("the labs did not all go to sleep, so the node keeps running (the bedtime was cancelled; "+
				"`crucible aws sleep --keep-labs` stops it anyway): %w", err)
		}
		fmt.Fprintln(o.Log, "every AWS lab is asleep")
	}
```

  `Teardown(ctx, varFile, yes, noSnapshot, keepLabs bool)`: after reading `m`, before `snapshotFirst`:

```go
	if keepLabs {
		fmt.Fprintln(o.Log, "WARNING: --keep-labs: running labs are left as they are; nothing will clean them up, and they keep billing")
	} else {
		st, err := o.state(ctx, m)
		if err != nil {
			return fmt.Errorf("not tearing down: %w (use --keep-labs to skip ending the labs)", err)
		}
		if st != "running" {
			return fmt.Errorf("the node is %s, so it can't end its labs: wake it first (crucible aws wake), or pass --keep-labs", st)
		}
		if _, err := o.ssmOut(ctx, m, labsOnNode("stop-all --wait 30m")); err != nil {
			return fmt.Errorf("the labs did not all end, so nothing was torn down (`--keep-labs` tears down anyway): %w", err)
		}
		fmt.Fprintln(o.Log, "every lab has ended; the rebuilt server reopens labs when it starts")
	}
```

  and replace its final `return o.Exec(…destroy…)` with:

```go
	if err := o.Exec(ctx, o.Root, "terraform", append([]string{"-chdir=" + o.dir("main"), "destroy", "-auto-approve", "-var-file=" + abs}, mainVars(p)...)...); err != nil {
		return err
	}
	o.reportLeftovers(ctx)
	return nil
}

// reportLeftovers lists anything still tagged crucible:lab-id in the lab regions, with how to remove it. It deletes
// nothing: with the server gone, a person decides.
func (o Ops) reportLeftovers(ctx context.Context) {
	l, err := o.outputs(ctx, "labs")
	if err != nil || l["regions"] == "" {
		return
	}
	found := false
	for r := range strings.SplitSeq(l["regions"], ",") {
		out, err := o.Output(ctx, o.Root, "aws", "resourcegroupstaggingapi", "get-resources", "--region", r,
			"--tag-filters", "Key=crucible:lab-id", "--query", "ResourceTagMappingList[].ResourceARN", "--output", "text")
		if err != nil {
			fmt.Fprintf(o.Log, "lab leftovers in %s: not checked (%v)\n", r, err)
			continue
		}
		if arns := strings.Fields(out); len(arns) > 0 {
			found = true
			fmt.Fprintf(o.Log, "still tagged crucible:lab-id in %s (delete these by hand, e.g. aws ec2 terminate-instances):\n  %s\n", r, strings.Join(arns, "\n  "))
		}
	}
	if !found {
		fmt.Fprintln(o.Log, "no lab resources are left")
	}
}
```

  `Status`: after the egress `switch`, add

```go
	if out, err := o.ssmOut(ctx, m, labsOnNode("status")); err == nil {
		fmt.Fprintf(o.Log, "labs:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(out), "\n", "\n  "))
	}
```

  `cmd/crucible/main.go`: add `keepLabs := fs.Bool("keep-labs", false, "sleep/teardown: leave AWS labs as they are (they keep running and billing)")`;
  `case "sleep": err = ops.SleepNode(ctx, *noSnapshot, *keepLabs)`; `case "teardown": err = ops.Teardown(ctx, *varFile, *yes, *noSnapshot, *keepLabs)`;
  usage lines `crucible aws sleep [--no-snapshot] [--keep-labs]` and `crucible aws teardown [--var-file FILE] --yes [--no-snapshot] [--keep-labs]`.

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/awsops ./cmd/crucible -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(awsops): sleep puts the labs to sleep first, teardown ends them first and lists leftovers

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awsops/awsops.go internal/awsops/awsops_test.go cmd/crucible/main.go
```

---

### Task 9: Hibernation for lab authors

**Files:**
- Modify: `internal/content/load.go` (tfFile, new tfHibernation, tfBool), `deploy/aws/labs/main.tf` (boundary + lab role KMS), `deploy/aws/labs/labs.tftest.hcl`, `examples/forge-402/modules/02-locked-out/lab/terraform/main.tf`, `docs/user/authors/aws-labs.md`
- Test: `cmd/crucible/main_test.go` (lint), `deploy/aws/labs/labs.tftest.hcl`

**Interfaces:** none consumed; content rule and IAM only.

- [ ] **Step 1: Failing tests** — `cmd/crucible/main_test.go`:

```go
func TestLintWantsAnEncryptedRootForHibernation(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../examples/forge-401")); err != nil {
		t.Fatal(err)
	}
	tf := func(root string) string {
		return `resource "aws_instance" "hib" {
  ami           = "ami-0123456789abcdef0"
  instance_type = "t3.micro"
  hibernation   = true
  root_block_device {` + root + `
    volume_size = 8
  }
}
`
	}
	p := filepath.Join(dir, "modules/01-cloud-heat/lab/terraform/hib.tf")
	if err := os.WriteFile(p, []byte(tf("")), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 || !strings.Contains(out.String(), "hibernation needs root_block_device { encrypted = true }") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	if err := os.WriteFile(p, []byte(tf("\n    encrypted = true")), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := lint(dir, &out); code != 0 {
		t.Fatalf("an encrypted root may hibernate: exit %d\n%s", code, out.String())
	}
}
```

  `deploy/aws/labs/labs.tftest.hcl`:

```hcl
run "encrypted_volumes_only_through_ec2" {
  command = apply
  assert {
    condition = alltrue([for doc in [jsondecode(aws_iam_policy.boundary.policy), jsondecode(aws_iam_role_policy.lab.policy)] :
      one([for s in doc.Statement : s if s.Sid == "EbsEncryptionViaEC2"]).Condition.StringLike["kms:ViaService"] == "ec2.*.amazonaws.com" &&
      alltrue([for a in one([for s in doc.Statement : s if s.Sid == "EbsEncryptionViaEC2"]).Action : startswith(a, "kms:")])])
    error_message = "a hibernating lab machine's encrypted disk uses KMS only through EC2"
  }
}
```

  Run: `go test ./cmd/crucible -run Hibernation -count=1` → FAIL; `(cd deploy/aws/labs && terraform test)` → FAIL (no `EbsEncryptionViaEC2`).

- [ ] **Step 2: The lint rule** — `internal/content/load.go`: in `tfFile`'s `case "resource", "data", "ephemeral", "action":` add
  `if blk.Type == "resource" && blk.Labels[0] == "aws_instance" { l.tfHibernation(p, blk.Body) }`, and add:

```go
var (
	tfInstanceSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "hibernation"}},
		Blocks: []hcl.BlockHeaderSchema{{Type: "root_block_device"}}}
	tfRootDiskSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "encrypted"}}}
)

// tfHibernation: a hibernating machine writes its RAM to its root disk, and AWS hibernates only to an encrypted one
// (otherwise the launch fails after the trainee waited for it). hibernation set to anything but a literal false needs
// root_block_device { encrypted = true }.
func (l *loader) tfHibernation(p string, body hcl.Body) {
	c, _, _ := body.PartialContent(tfInstanceSchema)
	a := c.Attributes["hibernation"]
	if a == nil || tfBool(a.Expr) == "false" {
		return
	}
	for _, b := range c.Blocks {
		rc, _, _ := b.Body.PartialContent(tfRootDiskSchema)
		if e := rc.Attributes["encrypted"]; e != nil && tfBool(e.Expr) == "true" {
			return
		}
	}
	l.add(p, "aws_instance with hibernation needs root_block_device { encrypted = true } (AWS hibernates only to an encrypted root disk)")
}

// tfBool is a literal bool's value, "" for anything else (a variable, an expression).
func tfBool(e hcl.Expression) string {
	v, d := e.Value(nil)
	if d.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.Bool {
		return ""
	}
	if v.True() {
		return "true"
	}
	return "false"
}
```

- [ ] **Step 3: KMS through EC2** — `deploy/aws/labs/main.tf`: in the boundary's `Statement` list, after `PassOnlyTheLabInstanceRole`, add

```hcl
      # Encrypted lab disks (hibernation needs one) with the account's aws/ebs key, and only through EC2.
      { Sid = "EbsEncryptionViaEC2", Effect = "Allow", Resource = "*",
        Action = ["kms:CreateGrant", "kms:Decrypt", "kms:DescribeKey", "kms:GenerateDataKeyWithoutPlaintext", "kms:ReEncrypt*"],
      Condition = { StringLike = { "kms:ViaService" = "ec2.*.amazonaws.com" } } },
```

  and the same statement (same `Sid`) in `aws_iam_role_policy.lab`'s `Statement` list, after `PassLabInstanceRole`.

- [ ] **Step 4: The worked example** — `examples/forge-402/modules/02-locked-out/lab/terraform/main.tf`, `aws_instance.box`:
  add `hibernation = true` (comment: `# a healthy machine: it resumes with its shells after a night asleep`) and
  `encrypted = true` inside `root_block_device`. `docs/user/authors/aws-labs.md`: a section `## Overnight and hibernation`
  saying labs sleep with the platform; a machine with `hibernation = true` and `root_block_device { encrypted = true }`
  (root larger than its RAM, an image that can hibernate: Amazon Linux 2023, or Ubuntu with `ec2-hibinit-agent`)
  resumes with its processes; any other machine, or one that can't hibernate (a broken boot), is stopped and boots
  again from its disk; lint refuses hibernation without an encrypted root.

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/crucible ./internal/content ./internal/docs -count=1 && ./bin/crucible lint examples/forge-402 2>/dev/null || go run ./cmd/crucible lint examples/forge-402; (cd deploy/aws/labs && terraform test)`
Expected: PASS, forge-402 lints OK, `Success!`.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(content): hibernating lab machines need an encrypted root; KMS through EC2 for lab disks; forge-402 locked-out hibernates

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content/load.go cmd/crucible/main_test.go deploy/aws/labs/main.tf deploy/aws/labs/labs.tftest.hcl examples/forge-402/modules/02-locked-out/lab/terraform/main.tf docs/user/authors/aws-labs.md
```

---

### Task 10: The lab pages

**Files:**
- Modify: `web/src/types.ts`, `web/src/pages/Lab.tsx`, `web/src/pages/Labs.tsx`, `web/src/pages/Ledger.tsx`, `web/src/theme/app.css` (only if a class is new), `.claude/skills/crucible-ui/SKILL.md`
- Test: `web/src/pages/Lab.test.tsx`

**Interfaces:**
- Consumes (API shapes from Tasks 1, 5, 6): `LabView.state` gains `'asleep' | 'waking'`; `slept_as?: 'hibernated' | 'stopped'`,
  `slept_at?: string`, `woke_at?: string`, `workspace_warning?: boolean`; `POST /api/labs/{id}/wake` → `LabView`;
  `ModuleLab.blocked` carries the banking and node-disk messages (no client work: the lobby already shows `blocked`).

- [ ] **Step 1: Failing tests** — append to `web/src/pages/Lab.test.tsx` (read `.claude/skills/crucible-ui/SKILL.md` first):

```tsx
// LabPage loads its lab in an effect, which a static render never runs: render the asleep view itself.
const asleep = (slept: 'hibernated' | 'stopped', error?: string) => renderToStaticMarkup(
  <MemoryRouter><AsleepLab lab={{ id: 'l2', state: 'asleep', runtime: 'aws', slept_as: slept, slept_at: new Date().toISOString(), error,
    ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [], terminals: [] } as unknown as LabView}
  setLab={() => {}} title="Cloud heat" back="/" /></MemoryRouter>)

test('a sleeping lab says how it slept and offers Wake it', () => {
  let h = asleep('hibernated')
  expect(h).toContain('Your lab is asleep')
  expect(h).toContain('Crucible hibernated your machines last night: they resume where you left them.')
  expect(h).toMatch(/<button[^>]*class="primary"[^>]*>Wake it<\/button>/)
  h = asleep('stopped', "starting the lab's machines: InsufficientInstanceCapacity")
  expect(h).toContain('Crucible stopped your machines last night: they restart from their disks.')
  expect(h).toContain('InsufficientInstanceCapacity')
})

test('after a wake the workspace says once what came back', () => {
  const woke = (slept: string) => renderToStaticMarkup(
    <MemoryRouter><LabWorkspace lab={{
      id: 'l3', state: 'ready', runtime: 'aws', slept_as: slept, woke_at: new Date().toISOString(), log: [],
      ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [{ id: 't1', status: 'open', title: 'T', body: '' }], terminals: [{ name: 'main' }],
    } as unknown as LabView} setLab={() => {}} title="Heat" back="/" /></MemoryRouter>)
  expect(woke('hibernated')).toContain('Your machines resumed where you left them.')
  expect(woke('stopped')).toContain("Your machines restarted from their disks; anything you hadn't saved is gone.")
})

test('a workspace over its limit is warned', () => {
  const h = renderToStaticMarkup(<MemoryRouter><LabWorkspace lab={{
    id: 'l4', state: 'ready', runtime: 'aws', workspace_warning: true, log: [],
    ends_at: new Date(Date.now() + 3600e3).toISOString(), tasks: [{ id: 't1', status: 'open', title: 'T', body: '' }], terminals: [{ name: 'main' }],
  } as unknown as LabView} setLab={() => {}} title="Heat" back="/" /></MemoryRouter>)
  expect(h).toContain('Your workspace holds more than 20 GiB')
})
```

  Change the test file's import to `import { AsleepLab, LabPage, LabWorkspace } from './Lab'`. The waking loader is one
  line in `LabPage` (no static test: the page's effect never runs there). Run: `cd web && npx vitest run src/pages/Lab.test.tsx` → FAIL.

- [ ] **Step 2: Types** — `web/src/types.ts`: `LabState` adds `| 'asleep' | 'waking'`; `LabView` adds
  `slept_as?: 'hibernated' | 'stopped'; slept_at?: string; woke_at?: string; workspace_warning?: boolean`.

- [ ] **Step 3: The pages** — `web/src/pages/Lab.tsx`:
  - `endMessages`: add `bedtime: 'Crucible went to sleep before your lab was ready. Start it again in the morning.'` and
    `workspace_full: 'Your workspace grew past 20 GiB, so the lab was put out.'`.
  - The poll effect: interval `lab.state === 'provisioning' || lab.state === 'waking' ? 2000 : 5000`.
  - After the `destroying` loaders, before the `ready || destroying` workspace line:

```tsx
  if (lab?.state === 'waking') return <Loader label="Stoking the coals: starting your machines…" />
  if (lab?.state === 'asleep') return <AsleepLab lab={lab} setLab={setLab} title={info.title} back={back} />
```

  - The component:

```tsx
const sleptLine: Record<string, string> = {
  hibernated: 'Crucible hibernated your machines last night: they resume where you left them.',
  stopped: 'Crucible stopped your machines last night: they restart from their disks.',
}

// A lab that slept with the platform: how it slept, its deadline, and the one way back (spec 2026-11-05 §7).
export function AsleepLab({ lab, setLab, title, back }: { lab: LabView; setLab: (l: LabView) => void; title: string; back: string }) {
  const [err, setErr] = useState<string>()
  const [busy, setBusy] = useState(false)
  const act = async (method: 'POST' | 'DELETE') => {
    setBusy(true)
    setErr(undefined)
    try {
      setLab(await api<LabView>(method === 'POST' ? `/api/labs/${lab.id}/wake` : `/api/labs/${lab.id}`, { method, json: method === 'POST' ? {} : undefined }))
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="page lobby">
      <Link className="action back" to={back}>Back to the training</Link>
      <h1>{title}</h1>
      <section className="panel asleep" aria-labelledby="asleep-h">
        <h2 id="asleep-h">Your lab is asleep</h2>
        <p>{sleptLine[lab.slept_as ?? 'stopped']}</p>
        {lab.ends_at && <p className="muted">It ends at {new Date(lab.ends_at).toLocaleString()}; its time runs while it sleeps.</p>}
        {lab.error && <p className="error" role="alert">{lab.error}</p>}
        {err && <p className="error" role="alert">{err}</p>}
        <div className="row">
          <button className="primary" disabled={busy} onClick={() => act('POST')}>Wake it</button>
          <button className="ghost" disabled={busy} onClick={() => act('DELETE')}>End lab</button>
        </div>
      </section>
    </section>
  )
}
```

  - In `LabWorkspace`, at the top of its returned markup (inside the page section, before the terminals), add:

```tsx
      <WakeBanner lab={lab} />
      {lab.workspace_warning && (
        <p className="warn" role="status">Your workspace holds more than 20 GiB: delete files you don't need, or the lab will be put out at the next check.</p>
      )}
```

    and the banner, shown once per wake (remembered per browser, as the skill's "remembered per browser" pattern does):

```tsx
const wokeLine: Record<string, string> = {
  hibernated: 'Your machines resumed where you left them.',
  stopped: "Your machines restarted from their disks; anything you hadn't saved is gone.",
}

function WakeBanner({ lab }: { lab: LabView }) {
  const key = `crucible.woke.${lab.id}`
  const [seen] = useState(() => { try { return localStorage.getItem(key) === lab.woke_at } catch { return false } })
  useEffect(() => { try { if (lab.woke_at) localStorage.setItem(key, lab.woke_at) } catch { /* private window */ } }, [key, lab.woke_at])
  if (!lab.woke_at || !lab.slept_as || seen) return null
  return <p className="request" role="status">{wokeLine[lab.slept_as]}</p>
}
```

  - `web/src/pages/Labs.tsx`: `active` adds `'asleep', 'waking'`.
  - `web/src/pages/Ledger.tsx` line 33: show the end time for `ready` and `asleep`:
    `{l.state}{(l.state === 'ready' || l.state === 'asleep') && l.ends_at ? ` · ends ${…}` : ''}`.
  - `.claude/skills/crucible-ui/SKILL.md`, Components table: a row **Sleeping thing** — `section.panel.asleep` with an
    `h2` from the user's side (*Your lab is asleep*), one sentence on how it slept, its deadline in `.muted`, any error
    as `[role=alert].error`, then `button.primary` (*Wake it*) and `button.ghost`; waking uses `Loader`; after waking a
    one-time `p.request[role=status]` remembered per browser by `woke_at`.

- [ ] **Step 4: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: all pass.

- [ ] **Step 5: Look at it** — with a local stack running, open a lab page whose lab you set asleep by hand
  (`docker compose … exec postgres psql -U crucible -c "update lab_instances set state='asleep', slept_as='stopped' where id='…'"`)
  in Forge, Anvil, Parchment and High Contrast, at phone width and under Calm forge.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(web): a sleeping lab and Wake it; the wake banner; asleep in the lists

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/types.ts web/src/pages/Lab.tsx web/src/pages/Lab.test.tsx web/src/pages/Labs.tsx web/src/pages/Ledger.tsx .claude/skills/crucible-ui/SKILL.md
```

  (Add `web/src/theme/app.css` to the pathspec only if you added a rule; reuse `.panel` and `.row` before adding CSS.)

---

### Task 11: End to end, and the operator docs (after the lanes merge)

**Files:**
- Modify: `e2e/tests/aws-lab.spec.ts`, `scripts/local-check.sh`
- Modify: `docs/runbooks/aws.md`, `docs/aws-deploy.html`, `docs/deploy-architecture.html`, `docs/test-guide.html`, `CLAUDE.md`, `deploy/aws/main/terraform.tfvars.example`

- [ ] **Step 1: The journey** — `scripts/local-check.sh`: the Playwright line exports the API container too:
  `CRUCIBLE_AGENT="$root/bin/crucible" CRUCIBLE_E2E_API="$($compose ps -q api)" npx playwright test --project="$project"`.
  `e2e/tests/aws-lab.spec.ts` (add `import { execFileSync } from 'node:child_process'`):

```ts
// crucible labs in the server's container, as the nightly CronJob and crucible aws sleep run it.
function crucibleLabs(...args: string[]) {
  execFileSync('docker', ['exec', process.env.CRUCIBLE_E2E_API!, 'crucible', 'labs', ...args], { stdio: 'inherit' })
}

test('an aws lab sleeps with the platform and wakes, with its files, when its trainee opens it', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  await addParticipant(leader, 'forge-401', 'trainee@crucible.local')
  const trainee = await login(browser, 'trainee')
  await trainee.goto('/p/forge/forge-401/m/01-cloud-heat/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.04)' }).click()
  await leader.getByRole('link', { name: 'Approvals' }).click()
  await leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' }).getByRole('button', { name: 'Approve' }).click()
  await expect(trainee.getByRole('tab', { name: 'workspace', exact: true })).toBeVisible({ timeout: 10 * 60_000 })
  // Something only this container has, checked after the wake.
  await typeIn(trainee, 'workspace', "aws configure list | awk '/region/ {print $2}' > ~/region.txt")
  await trainee.waitForTimeout(5_000) // the aws CLI starts slowly in dind

  // Bedtime: the dry run's machine stops (it isn't launched for hibernation) and the workspace pod goes.
  crucibleLabs('bedtime', '--wait', '10m')
  await expect(trainee.getByRole('heading', { name: 'Your lab is asleep' })).toBeVisible({ timeout: 60_000 })
  await expect(trainee.getByText('Crucible stopped your machines last night: they restart from their disks.')).toBeVisible()

  // The morning: a real node restarts the server, which ends the bedtime; here it is cancelled.
  crucibleLabs('bedtime', '--cancel')
  await trainee.getByRole('button', { name: 'Wake it' }).click()
  await expect(trainee.getByText("Your machines restarted from their disks; anything you hadn't saved is gone.")).toBeVisible({ timeout: 5 * 60_000 })

  // The same container came back on its volume: the file is still there, and the lab's credentials work.
  await expect(async () => {
    await trainee.getByRole('button', { name: 'Check' }).click()
    await expect(trainee.getByText("Your CLI points at eu-west-1 with your lab's credentials.")).toBeVisible({ timeout: 5_000 })
  }).toPass({ timeout: 60_000 })
  await trainee.getByRole('button', { name: 'End lab' }).click()
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 5 * 60_000 })
})
```

  Fast loop (memory: iterate against a kept stack, one spec): `KEEP=1 KEYCLOAK_PORT=8082 make cluster-check` once, then
  `cd e2e && CRUCIBLE_E2E_API=… npx playwright test tests/aws-lab.spec.ts --project=cluster` while fixing.

- [ ] **Step 2: Operator docs**
  - `docs/runbooks/aws.md`:
    - in the schedule section: labs go to sleep at `labs_sleep_cron` (default 19:00, before the 19:15 backup and the
      19:30 sleep); set it with `backup_cron`'s days in `crucible.tfvars`, e.g. `labs_sleep_cron = "0 19 * * 0-4"`.
    - a `## Labs overnight` section: hibernate or stop; Wake it; the TTL runs while asleep; `crucible labs status`
      (through `crucible aws status`); `crucible labs bedtime --cancel` if a bedtime must end without a restart.
    - `crucible aws sleep` and `teardown`: they put labs to sleep / end them first; `--keep-labs`; a stopped node is
      refused by teardown; teardown lists leftovers.
    - `disk_gb` default 100 and the two disk guards.
    - By-hand checks on the account:
      1. a hibernation-enabled lab machine resumes with its shell;
      2. a GRUB-broken machine stops and wakes still broken;
      3. what `StopInstances --hibernate` does to a machine at a GRUB prompt (refusal, stop, or a hang the force-stop ends);
      4. an encrypted lab disk launches under the boundary;
      5. a workspace volume works under sysbox with user namespaces (`hostUsers: false`) on the real node.
  - `docs/aws-deploy.html`:
    - the sleep/wake section and the teardown section follow the runbook;
    - the variables table gains `labs_sleep_cron`;
    - `disk_gb` shows 100.
  - `docs/deploy-architecture.html` "An AWS lab's life": a sleep step: bedtime CronJob, the workspace volume, hibernate
    or stop, and Wake it.
  - `docs/test-guide.html`: checklist items for bedtime, Wake it, `aws sleep`, `aws teardown`, and the by-hand checks.
  - `CLAUDE.md` Invariants: one line — "AWS labs sleep with the platform (`platform_bedtime`, states `asleep`/`waking`;
    the server's sweep carries out a bedtime, `Morning()` ends it at start); teardown ends every lab first and the
    server clears only a kill switch set by `crucible aws teardown`."
  - `deploy/aws/main/terraform.tfvars.example`: a commented `labs_sleep_cron = "0 19 * * 0-4"` line beside `backup_cron`.

- [ ] **Step 3: Checks** — HTML tag balance and anchors on the changed pages; `go test ./internal/docs`; the full plan
  check list below.

- [ ] **Step 4: Commit**

```bash
git commit -m "test(e2e): an aws lab sleeps and wakes with its files; docs: labs overnight

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- e2e/tests/aws-lab.spec.ts scripts/local-check.sh docs/runbooks/aws.md docs/aws-deploy.html docs/deploy-architecture.html docs/test-guide.html CLAUDE.md deploy/aws/main/terraform.tfvars.example
```

## Final check (controller, after Task 11)

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
gofmt -l cmd internal scripts; go vet ./...; go vet -tags cluster ./internal/labs; go test -race ./...
for s in persistent main labs; do (cd deploy/aws/$s && terraform test); done   # never terraform init in main
bash deploy/helm/test.sh; bash deploy/workspace/test.sh; bash deploy/toolbox/test.sh; bash scripts/lint-workflows.sh
(cd web && npm test && npx tsc -b && npm run lint && npm run build)
make build && for r in examples/platform examples/forge-*; do ./bin/crucible lint "$r"; done
KEYCLOAK_PORT=8082 caffeinate -i make cluster-check   # includes local-check and the new journey
```
