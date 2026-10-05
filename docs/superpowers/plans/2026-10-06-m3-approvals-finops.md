# M3 Approvals & FinOps Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cloud-tier lab requests need a cost-tiered approver, escalate when ignored (counted in business hours), are blocked over a hard cap unless an admin overrides; labs die at schedule close; an admin kill switch stops everything; email + Slack/Teams notifications; leaders and managers change team/program config from the UI as audited bot commits to the platform repo.

**Architecture:** The approval state machine lives in `internal/labs` on the existing `lab_instances` table (new states `pending_approval`, `rejected`, `expired`, plus estimate/tier/decision columns), so the lab page, the one-active-lab index and the sweep all keep working. Routing rules live in `internal/rbac`; schedule arithmetic in `internal/config`. River (Postgres) runs the durable work: the lab sweep (TTL, idle, schedule close, escalations, kill switch) and budget checks as leader-elected periodic jobs, and every email/webhook delivery as a retried job (`internal/notify`). UI config edits go through `gitsync.Writer` (a working clone that re-applies the edit on the new tip when a push races) and are read back immediately by a synchronous re-sync.

**Tech Stack:** Go 1.26, pgx v5, goose, River (`github.com/riverqueue/river` + `riverdriver/riverpgxv5`), `net/smtp`, yaml.v3 node editing, React 19 + Vite, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md` (§2, §4.3, §5, §6, §8.1, §8.6, §9.1, §9.2, §10, §12, §13, §14). Program context: `.superpowers/sdd/program-context.md`.

## Global Constraints

- Every shell starts with `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH` (go1.26.8). Never install system-wide.
- NEVER touch real AWS (no terraform plan/apply, no aws CLI against AWS, no docker push).
- `gofmt -l .` prints nothing; `go vet ./...` is clean; `go test -race ./...` passes (Docker Desktop must be running: dbtest uses testcontainers Postgres 18).
- Acceptance: `KEYCLOAK_PORT=8082 make local-check` ends with `🔥 Local check passed. The forge holds.` Run it at the end of Tasks 9 and 12 (it tears the stack down unless `KEEP=1`).
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (shown in each commit step).
- Never edit `internal/db/migrations/00001_init.sql`; each task adds its own numbered goose file.
- Emails are lowercased everywhere they are stored or compared.
- Quiz answers, check scripts, setup scripts and hint text never reach the browser before reveal (unchanged; don't add them to any new view).
- Spec §2 / §9.1: "Cost tiers | No defaults — set at install in `platform.yaml`"; "Escalation default | 4 business hours (counted inside schedule window)"; "nobody approves their own lab request".
- Spec §6: config writes are "bot commits directly with message `crucible: <action> by <user email>` and a `Crucible-Actor:` trailer … on non-fast-forward, rebase and retry up to 3×; a true conflict returns an error to the user"; §14: "Git push conflict on config → user sees 'Someone changed this in git, reload'".
- Spec §8.6: "Idle and timer warnings are in-app/browser only. No email or Slack" — schedule-close warnings ride the existing lab timer (15/5-minute toasts), never notifications.
- Spec §9.2: "Labs can't be requested outside windows; running labs are destroyed at window end (warned 15 min ahead)"; "Budgets: monthly per team and per program; 80% alert, 100% hard cap"; "Global kill switch (admin): destroys all running labs immediately and blocks new requests until re-enabled".
- UI: themes forge|anvil|quench|contrast via existing tokens; calm motion respected (no new animation that ignores `useCalm`); no leaderboards or cross-trainee comparisons.
- Keep the KodeKloud lab UI and `e2e/tests/forge-101.spec.ts` working unchanged.

## Rulings made in this plan (read before starting)

1. **No `lab_requests`/`approvals` tables.** A request is a `lab_instances` row in state `pending_approval`; decisions are columns (`decided_by`, `decided_at`, `decision_note`) plus `lab_events` and `audit_log`. Spec §8.1's `requested` and `approved` are recorded as `lab_events` (`requested`, `approved`) because the row moves straight on to `pending_approval` / `provisioning`.
2. **Over the hard cap = admin tier.** A request that would exceed the team or program cap is created `pending_approval` with `over_cap = true` and tier `admin`; only an admin can approve it, and that approval is audited as `lab.budget_override`. A $0 estimate is never blocked by money.
3. **Who may approve is decided by amount, not by the current tier.** Program approvers up to `tier1_usd`, the team leader up to `tier2_usd`, admins anything. Escalation changes who is *notified* (and who is next in line), it does not take the request away from lower tiers. Tiers whose only member is the requester are skipped when routing.
4. **Escalation and expiry are done by the lab sweep** (`escalate_at` column), not a separate scheduled job; after the admin tier also times out the request becomes `expired` (`end_reason = unanswered`). A trainee may withdraw a pending request (`expired`, `withdrawn`).
5. **River** runs the sweep (every 15 s) and budget checks (every 5 min) as periodic jobs, and email/webhook deliveries as jobs with up to 8 attempts. The in-process `RunSweeper` ticker is deleted: periodic jobs are leader-elected (safe if a second replica ever runs) and one mechanism schedules everything. Lab provisioning stays a goroutine in M3 (`// ponytail:` note); it becomes a job when the cluster runner (M4) makes it long-running.
6. **Spend in M3 is derived, not sampled:** month-to-date spend = Σ `hourly_usd` × running time over this month's labs; "committed" adds each running lab's remaining time to its `ends_at` (and the full estimate of a provisioning lab). No `cost_samples` table until M6 needs actuals to reconcile against.
7. **Budget files:** `teams/<team>/budget.yaml` holds `monthly_usd` and optional `hard_cap_usd` (defaults to `monthly_usd`); a program's `budget_usd_month` is both its budget and its cap. Team budgets are admin-only in the UI (spec §5.2 "admin — … budgets"); program budgets follow the program-manager rule.
8. **Schedules are named only.** A program's `schedule:` names one in `platform.yaml`; inline windows (spec §4.3 "or inline windows") are not implemented. No schedule = any time (escalation counts wall-clock hours).
9. **Extensions** are capped at the schedule window close; an extension whose added cost would move the estimate into a higher tier is refused with a message (`// ponytail:` the "Extension pending" re-approval flow ships with real cloud costs in M6).
10. **One agent per user:** the hub already replaces an older agent with a newer one; the replaced agent now receives close code 4001 and exits with a clear message instead of reconnecting (which caused the reconnect flap that destroyed labs).
11. **Test-only pricing:** `CRUCIBLE_DEV_LAB_USD_PER_HOUR=paid-heat=0.5` prices named local labs so the e2e check exercises approval with a $0 laptop lab. Production local labs are always $0.
12. **No `notifications` table:** River's job rows are the delivery log. Email is skipped entirely when `CRUCIBLE_SMTP_ADDR` is unset.

## Review Focus

1. **A request made late on Friday inside business hours** must escalate on Monday morning (4 *open* hours later), not on Saturday, and still be right across the October DST change. Pinned by `TestScheduleArithmetic` (Task 2) and `TestEscalationCountsBusinessHoursOnly` (Task 6).
2. **The requester is the only person at a tier** (a leader who is also the default approver requests a paid lab): the request must route to the next staffed tier and never appear in the requester's own inbox. Pinned by `TestApprovalRules` (Task 5, rbac) and `TestLeaderRequestRoutesPastThemselves` (Task 5, labs).
3. **Someone edits the same file in git between page load and Save:** the UI must answer "Someone changed this in git, reload", never silently overwrite; an unrelated concurrent commit must not block the save. Pinned by `TestWriterStaleBase` and `TestWriterRetriesWhenBranchMoves` (Task 9) and `TestStaleEditIsRefused` (Task 9, configapi).
4. **The kill switch is pulled while a lab is still provisioning:** the lab must end destroyed with `end_reason = kill_switch`, and containers that come up after the switch must be removed. Pinned by `TestKillSwitchStopsProvisioningLab` (Task 8).
5. **Lab and module titles flow into email headers:** a CR/LF in a title must not inject headers; a $0 lab must never be blocked by an exhausted budget. Pinned by `TestEmailIsSentWithoutHeaderInjection` (Task 4) and `TestFreeLabNeverBlockedByCap` (Task 8).

---

## File Structure

```
internal/content/load.go                 Task 1  local compose allowlist (replaces localService)
internal/agent/compose.go                Task 1  scrubbed docker env
internal/agent/client.go                 Task 1  ErrReplaced on close 4001
internal/agenthub/hub.go                 Task 1  close older agent with 4001
internal/config/config.go                Task 2  cost tiers, escalation, schedules, budgets, webhooks, program schedule/budget
internal/config/schedule.go              Task 2  Schedule: Open/End/NextOpen/AddOpen/String/Location
internal/db/db.go                        Task 3  River migrations after goose
internal/db/migrations/00002_audit.sql   Task 3
internal/audit/audit.go                  Task 3  Log / Recent
internal/jobs/jobs.go                    Task 3  River client with periodic jobs
internal/labs/jobs.go                    Task 3  SweepArgs/SweepWorker (Task 8 adds BudgetArgs/BudgetWorker)
internal/db/migrations/00003_notifications.sql  Task 4
internal/notify/notify.go                Task 4  Kinds, Event, Notify, email + webhook jobs, sync-problem report
internal/notify/http.go                  Task 4  GET/PUT /api/me/notifications
internal/gitsync/syncer.go               Task 4  OnProblem hook
internal/db/migrations/00004_lab_requests.sql   Task 5
internal/rbac/rbac.go                    Task 5  Tier, NextTier, TierApprovers, Route, MayApprove
internal/labs/model.go                   Task 5  new states, Instance fields, Estimator, FixedRates
internal/labs/service.go                 Tasks 4,5,6,7,8  quote, Start, End, view, Extend, Sweep
internal/labs/approvals.go               Tasks 5,6  Spend, Approvals inbox, Decide, escalate
internal/labs/finops.go                  Task 8  overCap, CheckBudgets, kill switch
internal/db/migrations/00005_finops.sql  Task 8
internal/yamlx/yamlx.go                  Task 9  Duration.MarshalYAML, Update
internal/gitsync/writer.go               Task 9  Writer.Apply
internal/configapi/configapi.go          Task 9  team/program/budget reads + writes, admin platform view
internal/httpapi/server.go               Tasks 4,9  Deps.Notify, Deps.Config, /api/me teams + can_approve
cmd/crucible-api/main.go                 Tasks 2,3,4,5,8,9  wiring
web/src/lib/lists.ts                     Task 10 parseEmails/parseMentors/formatMentors
web/src/pages/Team.tsx                   Task 10 TeamsIndex, TeamPage
web/src/pages/ProgramSettings.tsx        Task 10
web/src/pages/Approvals.tsx              Task 11
web/src/pages/ForgeStatus.tsx            Task 11
web/src/pages/Lab.tsx                    Task 11 request status in the lobby
web/src/pages/Settings.tsx               Task 4  email notification mutes
examples/forge-201/                      Task 12 fixture training with one local lab (priced in e2e)
e2e/tests/approvals.spec.ts              Task 12
```

Task order and dependencies: 1 and 2 are independent; 3 needs nothing; 4 needs 3; 5 needs 2–4; 6–8 need 5; 9 needs 2–3; 10 needs 9; 11 needs 5–9; 12 needs all.

---
### Task 1: Local labs stay on the laptop's side of the fence: compose allowlist, scrubbed agent env, one agent per user

Carry-over from M1/M2. The local-lab compose check becomes an allowlist; the agent stops leaking its environment (including `CRUCIBLE_TOKEN`) into compose interpolation; a replaced agent exits instead of flapping.

**Files:**
- Modify: `internal/content/load.go` (the `case "local", "cluster":` block in `lab()` and the whole `localService` function at the end of the file)
- Modify: `internal/content/load_test.go` (`TestLoadProblems` cases, `TestLocalComposeAllowsNamedVolumesAndRelativeBinds`)
- Modify: `internal/agent/compose.go` (all four `exec.Command*("docker", …)` calls)
- Modify: `internal/agent/compose_test.go`
- Modify: `internal/agenthub/hub.go` (the replace-older-agent block in `Serve`)
- Modify: `internal/agent/client.go` (`Run`, `runOnce`)
- Modify: `internal/agent/client_test.go`
- Modify: `web/src/pages/Connect.tsx` (one sentence)

**Interfaces:**
- Consumes: nothing new.
- Produces: `agent.ErrReplaced` (exported error), `agenthub.CloseReplaced` (`websocket.StatusCode(4001)`). No other task depends on these.

- [ ] **Step 1: Write the failing content tests**

In `internal/content/load_test.go`, replace the four host-namespace cases whose wanted text changes (the allowlist reports the key, not "key: host"):

```go
		"local host net":    {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    network_mode: host\n"}, "network_mode is not allowed"},
		"local pid host":    {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    pid: host\n"}, "pid is not allowed"},
		"local ipc host":    {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    ipc: host\n"}, "ipc is not allowed"},
```

and add these cases to the same map:

```go
		"local security_opt":  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    security_opt: [\"seccomp:unconfined\"]\n"}, "security_opt"},
		"local userns":        {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    userns_mode: host\n"}, "userns_mode"},
		"local uts":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    uts: host\n"}, "uts"},
		"local cgroup":        {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    cgroup: host\n"}, "cgroup"},
		"local ports":         {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    ports: [\"80:80\"]\n"}, "ports"},
		"local build":         {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    build: /\n"}, "build"},
		"local volumes_from":  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes_from: [other]\n"}, "volumes_from"},
		"local extra_hosts":   {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    extra_hosts: [\"h:host-gateway\"]\n"}, "extra_hosts"},
		"local npipe volume":  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: npipe, source: x, target: /x}]\n"}, `volume type "npipe"`},
		"local env_file abs":  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: /etc/environment\n"}, "env_file"},
		"local env_file home": {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: [\"~/.aws/credentials\"]\n"}, "env_file"},
		"local env_file long": {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: [{path: ../../x}]\n"}, "env_file"},
		"top secrets":         {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nsecrets:\n  k:\n    file: /etc/shadow\n"}, "secrets is not allowed"},
		"top configs":         {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nconfigs:\n  c:\n    file: /etc/hosts\n"}, "configs is not allowed"},
		"top include":         {map[string]string{"modules/m1/lab/compose.yaml": "include: [/etc/x.yaml]\nservices:\n  box:\n    image: alpine:3.22\n"}, "include is not allowed"},
		"volume driver_opts":  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nvolumes:\n  data:\n    driver_opts: {type: none, o: bind, device: /}\n"}, "driver_opts"},
		"external volume":     {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nvolumes:\n  data:\n    external: true\n"}, "external"},
		"host network driver": {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nnetworks:\n  n:\n    driver: host\n"}, `network "n": driver`},
```

Replace `TestLocalComposeAllowsNamedVolumesAndRelativeBinds` with a version that also covers the newly allowed shapes:

```go
func TestLocalComposeAllowsNamedVolumesAndRelativeBinds(t *testing.T) {
	compose := `x-common: &common {image: "alpine:3.22"}
services:
  box:
    <<: *common
    cap_drop: [ALL]
    env_file: [lab.env, {path: more.env, required: false}]
    networks: [inner]
    volumes: ["data:/data", "./files:/files:ro", /scratch, {type: volume, source: data, target: /d2}, {type: tmpfs, target: /t}]
volumes: {data: {}, logs: {labels: {a: b}}}
networks: {inner: {internal: true}, plain: {driver: bridge}}
`
	_, probs := Load(tree(t, map[string]string{"modules/m1/lab/compose.yaml": compose}))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
}
```

- [ ] **Step 2: Run the content tests to see them fail**

Run: `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:$PATH && cd /Users/adelin/Projects/Crucible && go test ./internal/content/ -run 'TestLoadProblems|TestLocalCompose' 2>&1 | tail -20`
Expected: FAIL — e.g. `TestLoadProblems/local_security_opt`, `top_secrets`, `local_ports`, `network_mode is not allowed` not found.

- [ ] **Step 3: Implement the allowlist**

In `internal/content/load.go`, add `"maps"` and `"slices"` to the imports (if not already there). In `lab()`, replace the loop body that calls `l.localService(p, name, &svc)`:

```go
	case "local", "cluster":
		if p, ok := l.file(dir, lab.Compose, lf); ok {
			var c struct {
				Services map[string]yaml.Node `yaml:"services"`
			}
			if err := yamlx.ReadLoose(p, &c); err != nil {
				l.add(p, "%v", err)
			}
			for name := range c.Services {
				services[name] = true
			}
			if lab.Runtime == "local" {
				l.localCompose(p)
			}
		}
```

Replace the whole `localService` function (from its doc comment to the end of the file) with:

```go
// localServiceKeys are the compose service settings a local lab may use. It is an allowlist: anything else
// (privileged, *_mode/pid/ipc/uts/cgroup, devices, cap_add, security_opt, ports, build, extra_hosts, secrets,
// configs, volumes_from, logging, …) could reach the trainee's laptop.
var localServiceKeys = map[string]bool{
	"image": true, "command": true, "entrypoint": true, "environment": true, "env_file": true, "working_dir": true,
	"user": true, "hostname": true, "expose": true, "volumes": true, "tmpfs": true, "healthcheck": true,
	"depends_on": true, "restart": true, "networks": true, "labels": true, "stop_signal": true,
	"stop_grace_period": true, "tty": true, "stdin_open": true, "init": true, "read_only": true, "cap_drop": true,
	"mem_limit": true, "cpus": true, "dns": true, "platform": true, "pull_policy": true,
}

// localPath reports whether p is a path inside the lab directory (no absolute, ~, $VAR or ../ escapes).
func localPath(p string) bool {
	return p != "" && !strings.ContainsAny(p, "~$") && filepath.IsLocal(filepath.Clean(p))
}

// localCompose rejects compose settings that would give a local lab access to the trainee's laptop.
func (l *loader) localCompose(file string) {
	var top map[string]yaml.Node
	if err := yamlx.ReadLoose(file, &top); err != nil {
		return // the services decode in lab() already reported it
	}
	bad := func(where, what string) {
		l.add(file, "%s: %s is not allowed for runtime: local (it can reach the trainee's laptop); use runtime: cluster", where, what)
	}
	for _, k := range slices.Sorted(maps.Keys(top)) {
		v := top[k]
		switch {
		case strings.HasPrefix(k, "x-"), k == "version", k == "name":
		case k == "services":
			var svcs map[string]yaml.Node
			if err := v.Decode(&svcs); err != nil {
				l.add(file, "services: %v", err)
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(svcs)) {
				n := svcs[name]
				l.localService(file, name, &n, bad)
			}
		case k == "volumes", k == "networks":
			var defs map[string]map[string]yaml.Node
			if err := v.Decode(&defs); err != nil {
				l.add(file, "%s: %v", k, err)
				continue
			}
			kind := strings.TrimSuffix(k, "s")
			for _, name := range slices.Sorted(maps.Keys(defs)) {
				for _, opt := range slices.Sorted(maps.Keys(defs[name])) {
					o := defs[name][opt]
					switch {
					case opt == "labels", kind == "network" && opt == "internal":
					case kind == "network" && opt == "driver" && o.Value == "bridge":
					default:
						bad(fmt.Sprintf("%s %q", kind, name), opt)
					}
				}
			}
		default:
			bad("compose file", k)
		}
	}
}

func (l *loader) localService(file, name string, n *yaml.Node, bad func(where, what string)) {
	var svc map[string]yaml.Node
	if err := n.Decode(&svc); err != nil {
		l.add(file, "service %q: %v", name, err)
		return
	}
	where := fmt.Sprintf("service %q", name)
	for _, k := range slices.Sorted(maps.Keys(svc)) {
		v := svc[k]
		switch {
		case k == "<<" || strings.HasPrefix(k, "x-"): // YAML merge keys and extensions; merged keys are checked too
		case k == "volumes":
			localVolumes(where, &v, bad)
		case k == "env_file":
			for _, p := range envFiles(&v) {
				if !localPath(p) {
					bad(where, fmt.Sprintf("env_file %q (a host path outside the lab)", p))
				}
			}
		case !localServiceKeys[k]:
			bad(where, k)
		}
	}
}

func localVolumes(where string, n *yaml.Node, bad func(where, what string)) {
	if n.Kind != yaml.SequenceNode {
		bad(where, "volumes that are not a list")
		return
	}
	for _, v := range n.Content {
		var src string
		if v.Kind == yaml.ScalarNode {
			if parts := strings.Split(v.Value, ":"); len(parts) > 1 {
				src = parts[0]
			}
		} else {
			var long struct {
				Type   string `yaml:"type"`
				Source string `yaml:"source"`
			}
			_ = v.Decode(&long)
			switch long.Type {
			case "volume", "tmpfs":
				continue
			case "bind":
				src = long.Source
			default:
				bad(where, fmt.Sprintf("volume type %q", long.Type))
				continue
			}
		}
		if src == "" {
			continue
		}
		if strings.HasPrefix(src, ".") || strings.ContainsAny(src, `/\~$`) { // a bind mount, not a named volume
			if !localPath(src) {
				bad(where, fmt.Sprintf("volume %q (a host path outside the lab)", src))
			}
		}
	}
}

// envFiles lists env_file paths in any of compose's shapes: "a", ["a", …], [{path: a}, …].
func envFiles(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, it := range n.Content {
			if it.Kind == yaml.ScalarNode {
				out = append(out, it.Value)
				continue
			}
			var long struct {
				Path string `yaml:"path"`
			}
			_ = it.Decode(&long)
			out = append(out, long.Path)
		}
		return out
	}
	return []string{""} // malformed: rejected as a non-local path
}
```

Note: a service built with `<<: *common` has its merged keys present in the decoded map (yaml.v3 resolves merges when decoding into a map), so they are checked like any other key; the literal `<<` key is skipped.

- [ ] **Step 4: Run the content tests to see them pass**

Run: `go test ./internal/content/ ./cmd/crucible/ 2>&1 | tail -5`
Expected: `ok  	crucible/internal/content` and `ok  	crucible/cmd/crucible` (the latter lints `examples/forge-101`, whose compose uses only `image` and `command`).

- [ ] **Step 5: Write the failing agent tests**

Append to `internal/agent/compose_test.go`:

```go
func TestComposeEnvKeepsOnlyDockerEssentials(t *testing.T) {
	got := composeEnv([]string{"PATH=/bin", "HOME=/h", "CRUCIBLE_TOKEN=secret", "AWS_SECRET_ACCESS_KEY=k",
		"DOCKER_HOST=unix:///x", "USER=u", "GITHUB_TOKEN=g", "XDG_RUNTIME_DIR=/run/u"})
	want := []string{"PATH=/bin", "HOME=/h", "DOCKER_HOST=unix:///x", "USER=u", "XDG_RUNTIME_DIR=/run/u"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("composeEnv = %v", got)
	}
}
```

(add `"strings"` to that file's imports).

Append to `internal/agent/client_test.go`:

```go
func TestReplacedAgentStopsInsteadOfReconnecting(t *testing.T) {
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Serve(w, r, 1) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newClient := func() *agent.Client {
		return &agent.Client{Server: srv.URL, Token: "t", Exec: &fakeExec{provisioned: map[string]string{}}, Log: slog.Default()}
	}
	first := make(chan error, 1)
	go func() { first <- newClient().Run(ctx) }()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	go func() { _ = newClient().Run(ctx) }()
	select {
	case err := <-first:
		if !errors.Is(err, agent.ErrReplaced) {
			t.Fatalf("first agent: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the replaced agent kept running")
	}
}
```

(add `"errors"` to that file's imports).

- [ ] **Step 6: Run them to see them fail**

Run: `go test ./internal/agent/ 2>&1 | tail -5`
Expected: FAIL — `undefined: composeEnv`, `undefined: agent.ErrReplaced`.

- [ ] **Step 7: Implement the env scrub and the replaced-agent exit**

In `internal/agent/compose.go` add:

```go
// composeEnv is the environment docker compose runs with: just enough to find and reach Docker. Compose files
// interpolate ${VAR}; with the agent's own environment a lab could read CRUCIBLE_TOKEN or cloud credentials.
func composeEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "USER" || k == "TMPDIR" || k == "XDG_RUNTIME_DIR" || strings.HasPrefix(k, "DOCKER_") {
			out = append(out, kv)
		}
	}
	return out
}

func docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = composeEnv(os.Environ())
	return cmd
}
```

and switch the four call sites:
- Provision: `out, err := docker(ctx, c.args(labID, "up", "-d", "--wait")...).CombinedOutput()`
- Destroy: `out, err := docker(ctx, c.args(labID, "down", "-v", "--remove-orphans")...).CombinedOutput()`
- RunScript: `cmd := docker(context.Background(), args...)` (RunLimited applies the timeout)
- StartPTY: `cmd := docker(context.Background(), c.args(labID, "exec", service, "sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi")...)`

In `internal/agenthub/hub.go`, add below `var ErrOffline`:

```go
// CloseReplaced tells an agent that a newer agent for the same user took over; it must stop, not reconnect.
const CloseReplaced = websocket.StatusCode(4001)
```

and in `Serve` change the close of the older connection to:

```go
		go func() { _ = old.ws.Close(CloseReplaced, "replaced by a newer agent") }()
```

In `internal/agent/client.go` add `"crucible/internal/agenthub"` to the imports and:

```go
// ErrReplaced means another crucible-agent connected with the same account (one laptop agent per user).
var ErrReplaced = errors.New("another crucible-agent connected with your account; this one stops (run one agent per user)")
```

In `Run`, extend the stop condition:

```go
		if errors.Is(err, errRejected) || errors.Is(err, ErrReplaced) {
			return err
		}
```

In `runOnce`, in the read loop:

```go
		_, data, err := ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == agenthub.CloseReplaced {
				return ErrReplaced
			}
			return err
		}
```

(`internal/agent` importing `internal/agenthub` is fine: agenthub does not import agent.)

In `web/src/pages/Connect.tsx`, change the second list item to:

```tsx
        <li>Generate a pairing token. A new token revokes the previous one. Run one agent per account: starting it on another laptop stops this one.</li>
```

- [ ] **Step 8: Run the agent tests and the full suite**

Run: `go test -race ./internal/agent/... ./internal/agenthub/... ./internal/content/... && gofmt -l . && go vet ./...`
Expected: three `ok` lines, no gofmt output, vet clean.

- [ ] **Step 9: Commit**

```bash
git add internal/content internal/agent internal/agenthub web/src/pages/Connect.tsx
git commit -m "fix(labs): allowlist local compose settings, scrub agent env, stop replaced agents

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 2: Platform config for FinOps: cost tiers, escalation hours, schedules, budgets, team webhooks

**Files:**
- Create: `internal/config/schedule.go`, `internal/config/schedule_test.go`
- Modify: `internal/config/config.go` (Settings, Team, Program, Load, loadTeam)
- Modify: `internal/config/config_test.go`
- Modify: `examples/platform/platform.yaml`; Create: `examples/platform/teams/forge/budget.yaml`
- Modify: `internal/gitsync/syncer_test.go:52` (fixture platform.yaml)
- Modify: `cmd/crucible-api/main.go`, `cmd/crucible/main.go` (embed tzdata)
- Modify: `docs/runbooks/aws.md` (one paragraph)

**Interfaces:**
- Produces (used by Tasks 5–11):
  - `config.Settings{DefaultTheme string; CostTiers *CostTiers; EscalationHours float64; Schedules map[string]*Schedule; Quotes []string}`
  - `func (s Settings) Escalation() time.Duration`
  - `config.CostTiers{AutoApproveUSD, Tier1USD, Tier2USD float64}` (json `auto_approve_usd`, `tier1_usd`, `tier2_usd`)
  - `config.Team.Notifications TeamNotifications{SlackWebhook, TeamsWebhook string}`, `config.Team.Budget Budget`
  - `config.Budget{MonthlyUSD, HardCapUSD float64}` (json `monthly_usd`, `hard_cap_usd`)
  - `config.Program.Schedule string`, `config.Program.BudgetUSDMonth float64`
  - `func (p *Platform) ProgramSchedule(team, training string) *Schedule` (nil = any time)
  - `*Schedule` methods, all nil-safe (nil = always open): `Open(t) bool`, `End(t) time.Time` (zero = no end), `NextOpen(t) time.Time` (zero = none within a week), `AddOpen(t, d) time.Time`, `String() string`, `Location() *time.Location`

- [ ] **Step 1: Write the failing schedule tests**

Create `internal/config/schedule_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func businessHours(t *testing.T) *Schedule {
	t.Helper()
	s := &Schedule{Timezone: "Europe/Bucharest", Windows: []Window{{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "19:00"}}}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	return s
}

// local parses a Bucharest wall-clock time. 2026-10-05 is a Monday; DST ends on Sunday 2026-10-25.
func local(t *testing.T, s string) time.Time {
	t.Helper()
	loc, _ := time.LoadLocation("Europe/Bucharest")
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestScheduleArithmetic(t *testing.T) {
	s := businessHours(t)
	for in, want := range map[string]bool{"2026-10-09 18:59": true, "2026-10-09 19:00": false, "2026-10-10 10:00": false, "2026-10-12 08:00": true} {
		if got := s.Open(local(t, in)); got != want {
			t.Errorf("Open(%s) = %v", in, got)
		}
	}
	if got := s.End(local(t, "2026-10-07 10:00")); !got.Equal(local(t, "2026-10-07 19:00")) {
		t.Errorf("End = %v", got)
	}
	if got := s.End(local(t, "2026-10-10 10:00")); !got.IsZero() {
		t.Errorf("End outside a window must be zero, got %v", got)
	}
	if got := s.NextOpen(local(t, "2026-10-09 20:00")); !got.Equal(local(t, "2026-10-12 08:00")) {
		t.Errorf("NextOpen = %v", got)
	}
	cases := []struct{ from, want string }{
		{"2026-10-09 17:00", "2026-10-12 10:00"}, // 2h on Friday + 2h on Monday
		{"2026-10-10 12:00", "2026-10-12 12:00"}, // requested on Saturday: the clock starts Monday 08:00
		{"2026-10-07 09:00", "2026-10-07 13:00"},
		{"2026-10-23 17:00", "2026-10-26 10:00"}, // across the DST change, wall-clock still right
	}
	for _, c := range cases {
		if got := s.AddOpen(local(t, c.from), 4*time.Hour); !got.Equal(local(t, c.want)) {
			t.Errorf("AddOpen(%s, 4h) = %v, want %s", c.from, got.In(s.Location()), c.want)
		}
	}
	if got := s.String(); got != "mon,tue,wed,thu,fri 08:00–19:00 (Europe/Bucharest)" {
		t.Errorf("String = %q", got)
	}
}

func TestAlwaysOpenAndNilSchedules(t *testing.T) {
	all := &Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, Start: "00:00", End: "24:00"}}}
	if err := all.validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if !all.Open(now) || !all.End(now).IsZero() {
		t.Fatalf("24/7 schedule: open %v end %v", all.Open(now), all.End(now))
	}
	var none *Schedule
	if !none.Open(now) || !none.End(now).IsZero() || !none.NextOpen(now).Equal(now) || !none.AddOpen(now, time.Hour).Equal(now.Add(time.Hour)) || none.String() != "any time" {
		t.Fatal("a nil schedule means any time")
	}
}

func TestScheduleValidation(t *testing.T) {
	cases := map[string]struct {
		s    Schedule
		want string
	}{
		"bad zone":     {Schedule{Timezone: "Mars/Olympus", Windows: []Window{{Days: []string{"mon"}, Start: "08:00", End: "09:00"}}}, "timezone"},
		"no windows":   {Schedule{Timezone: "UTC"}, "at least one window"},
		"unknown day":  {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"funday"}, Start: "08:00", End: "09:00"}}}, "unknown day"},
		"short time":   {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"mon"}, Start: "8:00", End: "09:00"}}}, "HH:MM"},
		"end <= start": {Schedule{Timezone: "UTC", Windows: []Window{{Days: []string{"mon"}, Start: "22:00", End: "02:00"}}}, "end must be after start"},
	}
	for name, c := range cases {
		if err := c.s.validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config/ -run Schedule 2>&1 | tail -5`
Expected: FAIL — `undefined: Schedule`.

- [ ] **Step 3: Implement `internal/config/schedule.go`**

```go
package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} // index = time.Weekday

// Schedule is a named set of weekly windows in one time zone (spec §9.2), e.g. Mon–Fri 08:00–19:00 Europe/Bucharest.
// A nil *Schedule means "any time": every method treats it as always open.
type Schedule struct {
	Timezone string   `yaml:"timezone"`
	Windows  []Window `yaml:"windows"`
	loc      *time.Location
}

type Window struct {
	Days  []string `yaml:"days"`  // mon … sun
	Start string   `yaml:"start"` // "08:00"
	End   string   `yaml:"end"`   // "19:00"; "24:00" is midnight; after Start (a window cannot cross midnight)
	days  [7]bool
	start int // minutes after midnight
	end   int
}

func parseHHMM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || len(s) != 5 || h < 0 || m < 0 || m > 59 || h*60+m > 24*60 {
		return 0, fmt.Errorf("time %q must be HH:MM between 00:00 and 24:00", s)
	}
	return h*60 + m, nil
}

func (s *Schedule) validate() error {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Timezone == "" {
		return fmt.Errorf("timezone %q is not a known IANA zone", s.Timezone)
	}
	s.loc = loc
	if len(s.Windows) == 0 {
		return fmt.Errorf("at least one window is required")
	}
	for i := range s.Windows {
		w := &s.Windows[i]
		if len(w.Days) == 0 {
			return fmt.Errorf("window %d: days are required", i+1)
		}
		for _, d := range w.Days {
			idx := slices.Index(weekdays, strings.ToLower(d))
			if idx < 0 {
				return fmt.Errorf("window %d: unknown day %q (use mon … sun)", i+1, d)
			}
			w.days[idx] = true
		}
		if w.start, err = parseHHMM(w.Start); err != nil {
			return fmt.Errorf("window %d: %w", i+1, err)
		}
		if w.end, err = parseHHMM(w.End); err != nil {
			return fmt.Errorf("window %d: %w", i+1, err)
		}
		if w.end <= w.start {
			return fmt.Errorf("window %d: end must be after start (a window cannot cross midnight; add a second window)", i+1)
		}
	}
	return nil
}

type span struct{ from, to time.Time }

// spans returns the open intervals from the day before t through `days` days after it, merged and in order.
// Building each window with time.Date in the schedule's zone keeps wall-clock times right across DST changes.
func (s *Schedule) spans(t time.Time, days int) []span {
	lt := t.In(s.loc)
	day0 := time.Date(lt.Year(), lt.Month(), lt.Day()-1, 0, 0, 0, 0, s.loc)
	var out []span
	for i := 0; i <= days; i++ {
		d := day0.AddDate(0, 0, i)
		for _, w := range s.Windows {
			if w.days[d.Weekday()] {
				out = append(out, span{
					time.Date(d.Year(), d.Month(), d.Day(), 0, w.start, 0, 0, s.loc),
					time.Date(d.Year(), d.Month(), d.Day(), 0, w.end, 0, 0, s.loc),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	merged := out[:0]
	for _, sp := range out {
		if n := len(merged); n > 0 && !sp.from.After(merged[n-1].to) {
			if sp.to.After(merged[n-1].to) {
				merged[n-1].to = sp.to
			}
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

const horizon = 8 // days looked ahead; an open stretch running past a week is treated as "never closes"

func (s *Schedule) Open(t time.Time) bool {
	if s == nil {
		return true
	}
	for _, sp := range s.spans(t, 2) {
		if !t.Before(sp.from) && t.Before(sp.to) {
			return true
		}
	}
	return false
}

// End is when the open stretch containing t closes. Zero when t is outside the schedule or it never closes (24/7).
func (s *Schedule) End(t time.Time) time.Time {
	if s == nil {
		return time.Time{}
	}
	for _, sp := range s.spans(t, horizon) {
		if !t.Before(sp.from) && t.Before(sp.to) {
			if sp.to.Sub(t) > 7*24*time.Hour {
				return time.Time{}
			}
			return sp.to
		}
	}
	return time.Time{}
}

// NextOpen is t when open, else the next opening within a week (zero if none).
func (s *Schedule) NextOpen(t time.Time) time.Time {
	if s == nil {
		return t
	}
	for _, sp := range s.spans(t, horizon) {
		if t.Before(sp.to) {
			if t.Before(sp.from) {
				return sp.from
			}
			return t
		}
	}
	return time.Time{}
}

// AddOpen returns the instant when d of open time has passed since t: business-hours arithmetic for escalations (spec §9.1).
func (s *Schedule) AddOpen(t time.Time, d time.Duration) time.Time {
	if s == nil {
		return t.Add(d)
	}
	for range 60 { // each round looks a week ahead; escalations are a few hours
		for _, sp := range s.spans(t, horizon) {
			if !sp.to.After(t) {
				continue
			}
			from := sp.from
			if t.After(from) {
				from = t
			}
			if left := sp.to.Sub(from); d <= left {
				return from.Add(d)
			} else {
				d -= left
			}
			t = sp.to
		}
	}
	return time.Time{}
}

// String describes the schedule for people, e.g. "mon,tue,wed,thu,fri 08:00–19:00 (Europe/Bucharest)".
func (s *Schedule) String() string {
	if s == nil {
		return "any time"
	}
	var parts []string
	for _, w := range s.Windows {
		parts = append(parts, strings.Join(w.Days, ",")+" "+w.Start+"–"+w.End)
	}
	return strings.Join(parts, "; ") + " (" + s.Timezone + ")"
}

// Location is the schedule's time zone (UTC for "any time"); format user-facing times in it.
func (s *Schedule) Location() *time.Location {
	if s == nil {
		return time.UTC
	}
	return s.loc
}
```

- [ ] **Step 4: Run the schedule tests**

Run: `go test -race ./internal/config/ -run Schedule -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)'`
Expected: `--- PASS: TestScheduleArithmetic`, `--- PASS: TestAlwaysOpenAndNilSchedules`, `--- PASS: TestScheduleValidation`.

- [ ] **Step 5: Write the failing config tests**

In `internal/config/config_test.go`, extend `TestLoadExamplePlatform` (before its last closing brace):

```go
	if tiers := p.Settings.CostTiers; tiers == nil || tiers.AutoApproveUSD != 0 || tiers.Tier1USD != 5 || tiers.Tier2USD != 25 {
		t.Fatalf("cost tiers %+v", p.Settings.CostTiers)
	}
	if p.Settings.Escalation() != 4*time.Hour || p.Settings.Schedules["business-hours"] == nil {
		t.Fatalf("escalation %v schedules %v", p.Settings.Escalation(), p.Settings.Schedules)
	}
	if team.Budget.MonthlyUSD != 200 || team.Budget.HardCapUSD != 250 {
		t.Fatalf("team budget %+v", team.Budget)
	}
	if p.ProgramSchedule("forge", "forge-101") != nil {
		t.Fatal("forge-101 has no schedule: it runs any time")
	}
```

and add these cases to the `TestLoadRejectsBadConfig` map:

```go
		"no cost tiers":   {"platform.yaml", "default_theme: forge\n", "cost_tiers is required"},
		"bad cost tiers":  {"platform.yaml", "cost_tiers: {auto_approve_usd: 9, tier1_usd: 5, tier2_usd: 25}\n", "cost_tiers must satisfy"},
		"bad schedule":    {"platform.yaml", "cost_tiers: {tier1_usd: 5, tier2_usd: 25}\nschedules:\n  night: {timezone: UTC, windows: [{days: [mon], start: \"22:00\", end: \"02:00\"}]}\n", "schedules.night"},
		"unknown sched":   {"teams/forge/programs/forge-101.yaml", "training: forge-101\nschedule: night\n", `unknown schedule "night"`},
		"negative budget": {"teams/forge/programs/forge-101.yaml", "training: forge-101\nbudget_usd_month: -1\n", "budget_usd_month"},
		"http webhook":    {"teams/forge/team.yaml", "name: F\nleader: a@x\nnotifications: {slack_webhook: \"http://hooks.example\"}\n", "https://"},
		"cap below budget": {"teams/forge/budget.yaml", "monthly_usd: 100\nhard_cap_usd: 50\n", "hard_cap_usd"},
```


- [ ] **Step 6: Run them to see them fail**

Run: `go test ./internal/config/ 2>&1 | tail -15`
Expected: FAIL — `p.Settings.CostTiers undefined`, `team.Budget undefined`.

- [ ] **Step 7: Implement the config additions**

In `internal/config/config.go` add `"time"` to the imports and replace `Settings`, add the new types, and extend `Team` and `Program`:

```go
type Settings struct {
	DefaultTheme    string               `yaml:"default_theme"`
	CostTiers       *CostTiers           `yaml:"cost_tiers"`
	EscalationHours float64              `yaml:"escalation_hours"` // default 4, counted inside the program's schedule
	Schedules       map[string]*Schedule `yaml:"schedules"`
	Quotes          []string             `yaml:"-"`
}

// CostTiers routes lab requests by estimate (spec §9.1). There are no built-in defaults: platform.yaml must set them.
type CostTiers struct {
	AutoApproveUSD float64 `yaml:"auto_approve_usd" json:"auto_approve_usd"`
	Tier1USD       float64 `yaml:"tier1_usd" json:"tier1_usd"`
	Tier2USD       float64 `yaml:"tier2_usd" json:"tier2_usd"`
}

// Escalation is how long a lab request may wait at one tier before it moves up.
func (s Settings) Escalation() time.Duration { return time.Duration(s.EscalationHours * float64(time.Hour)) }

// TeamNotifications are the team's Slack / Teams incoming webhooks (spec §10). Both must be https.
type TeamNotifications struct {
	SlackWebhook string `yaml:"slack_webhook"`
	TeamsWebhook string `yaml:"teams_webhook"`
}

// Budget is teams/<team>/budget.yaml: the monthly lab budget (80% alert) and the hard cap that blocks requests.
type Budget struct {
	MonthlyUSD float64 `yaml:"monthly_usd" json:"monthly_usd"`
	HardCapUSD float64 `yaml:"hard_cap_usd" json:"hard_cap_usd"` // defaults to monthly_usd; 0 = no cap
}
```

`Team` gains (keep existing fields):

```go
	Notifications TeamNotifications `yaml:"notifications"`
	Budget        Budget            `yaml:"-"` // from budget.yaml
```

`Program` gains:

```go
	Schedule       string  `yaml:"schedule"`         // named schedule from platform.yaml; "" = any time
	BudgetUSDMonth float64 `yaml:"budget_usd_month"` // the program's monthly budget and hard cap; 0 = none
```

Add the helper:

```go
// ProgramSchedule returns the schedule a program runs on, or nil for "any time".
func (p *Platform) ProgramSchedule(team, training string) *Schedule {
	t := p.Teams[team]
	if t == nil || t.Programs[training] == nil {
		return nil
	}
	return p.Settings.Schedules[t.Programs[training].Schedule]
}
```

In `Load`, right after the default-theme checks, add:

```go
	if t := p.Settings.CostTiers; t == nil {
		errs = append(errs, errors.New("platform.yaml: cost_tiers is required (auto_approve_usd, tier1_usd, tier2_usd); Crucible has no built-in defaults"))
	} else if t.AutoApproveUSD < 0 || t.Tier1USD <= 0 || t.Tier1USD < t.AutoApproveUSD || t.Tier2USD < t.Tier1USD {
		errs = append(errs, errors.New("platform.yaml: cost_tiers must satisfy 0 <= auto_approve_usd <= tier1_usd <= tier2_usd and tier1_usd > 0"))
	}
	if p.Settings.EscalationHours == 0 {
		p.Settings.EscalationHours = 4
	}
	if p.Settings.EscalationHours < 0 {
		errs = append(errs, errors.New("platform.yaml: escalation_hours must be positive"))
	}
	for name, s := range p.Settings.Schedules {
		if s == nil {
			errs = append(errs, fmt.Errorf("platform.yaml: schedules.%s is empty", name))
			continue
		}
		if err := s.validate(); err != nil {
			errs = append(errs, fmt.Errorf("platform.yaml: schedules.%s: %w", name, err))
		}
	}
```

Change the team loop call to pass schedules: `t, terrs := loadTeam(filepath.Join(dir, "teams", e.Name()), e.Name(), p.Trainings, p.Settings.Schedules)` and the signature to `func loadTeam(dir, id string, trainings map[string]TrainingRef, schedules map[string]*Schedule) (*Team, []error)`.

In `loadTeam`, after the mentor checks, add:

```go
	for _, u := range []string{t.Notifications.SlackWebhook, t.Notifications.TeamsWebhook} {
		if u != "" && !strings.HasPrefix(u, "https://") {
			bad("notifications: webhook URLs must start with https://")
		}
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "budget.yaml"), &t.Budget, false); err != nil {
		bad("%v", err)
	}
	if t.Budget.HardCapUSD == 0 {
		t.Budget.HardCapUSD = t.Budget.MonthlyUSD
	}
	if t.Budget.MonthlyUSD < 0 || t.Budget.HardCapUSD < t.Budget.MonthlyUSD {
		bad("budget.yaml: need 0 <= monthly_usd <= hard_cap_usd")
	}
```

and in the program loop, after the enrolled check:

```go
		if pr.Schedule != "" && schedules[pr.Schedule] == nil {
			bad("programs/%s.yaml: unknown schedule %q (define it under schedules in platform.yaml)", name, pr.Schedule)
		}
		if pr.BudgetUSDMonth < 0 {
			bad("programs/%s.yaml: budget_usd_month must not be negative", name)
		}
```

Update `examples/platform/platform.yaml`:

```yaml
default_theme: forge
# Lab requests are routed by estimated cost in USD (spec §9.1). Crucible has no defaults: set these at install.
cost_tiers: { auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25 }
escalation_hours: 4   # business hours, counted inside the program's schedule
schedules:
  business-hours:
    timezone: Europe/Bucharest
    windows:
      - { days: [mon, tue, wed, thu, fri], start: "08:00", end: "19:00" }
```

Create `examples/platform/teams/forge/budget.yaml`:

```yaml
monthly_usd: 200   # 80% sends an alert
hard_cap_usd: 250  # requests that would pass this need an admin
```

In `internal/gitsync/syncer_test.go`, change the fixture line to:

```go
		"platform.yaml":            "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n",
```

Add `_ "time/tzdata" // schedules name IANA zones; the runtime image has no zoneinfo` to the imports of `cmd/crucible-api/main.go` and `cmd/crucible/main.go` (the Alpine image has no `/usr/share/zoneinfo`).

In `docs/runbooks/aws.md`, add after the installation list:

```markdown
**Platform repo prerequisites (M3).** `platform.yaml` must define `cost_tiers` (`auto_approve_usd`, `tier1_usd`, `tier2_usd`); Crucible refuses a platform config without them and keeps serving the last good one. Programs may name a `schedule` defined under `schedules`. The bot git credential now needs push access: the UI commits config changes (team roster, program settings, budgets) to the platform repo.
```

- [ ] **Step 8: Run config, gitsync and the CLI lint test**

Run: `go test -race ./internal/config/... ./internal/gitsync/... ./internal/rbac/... ./internal/labs/... ./internal/learn/... ./cmd/... && gofmt -l . && go vet ./...`
Expected: all `ok` (labs/learn/rbac load `examples/platform`, which is now valid with tiers and the budget file).

- [ ] **Step 9: Commit**

```bash
git add internal/config internal/gitsync/syncer_test.go examples/platform cmd docs/runbooks/aws.md
git commit -m "feat(config): cost tiers, escalation hours, schedules, budgets and team webhooks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: Durable jobs on River, the lab sweep as a periodic job, and the audit log

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `internal/db/db.go` (`Migrate`)
- Create: `internal/db/migrations/00002_audit.sql`
- Create: `internal/audit/audit.go`, `internal/audit/audit_test.go`
- Create: `internal/jobs/jobs.go`, `internal/jobs/jobs_test.go`
- Create: `internal/labs/jobs.go`
- Modify: `internal/labs/service.go` (delete `RunSweeper`; add a non-overlap guard to `Sweep`)
- Modify: `cmd/crucible-api/main.go`

**Interfaces:**
- Produces:
  - `audit.Log(ctx context.Context, db *pgxpool.Pool, actor, action, target string, detail map[string]any, commitSHA string) error`
  - `audit.Recent(ctx context.Context, db *pgxpool.Pool, limit int) ([]audit.Entry, error)`; `audit.Entry{At time.Time; Actor, Action, Target string; Detail map[string]any; CommitSHA string}` (json `at, actor, action, target, detail, commit_sha`)
  - `jobs.Periodic{Every time.Duration; Args river.JobArgs}`; `jobs.New(pool *pgxpool.Pool, workers *river.Workers, periodic []jobs.Periodic, log *slog.Logger) (*river.Client[pgx.Tx], error)`
  - `labs.SweepArgs` (kind `lab_sweep`), `labs.SweepWorker{S *labs.Service}`
- Audit action names used by later tasks: `lab.approve`, `lab.reject`, `lab.budget_override`, `kill_switch.on`, `kill_switch.off`, `team.roster`, `program.enroll`, `program.update`, `team.budget`.

- [ ] **Step 1: Add River**

Run:
```bash
export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:$PATH && cd /Users/adelin/Projects/Crucible
go get github.com/riverqueue/river@latest github.com/riverqueue/river/riverdriver/riverpgxv5@latest
go mod tidy
grep riverqueue go.mod
```
Expected: `github.com/riverqueue/river vX.Y.Z` and `github.com/riverqueue/river/riverdriver/riverpgxv5 vX.Y.Z` (same version).

- [ ] **Step 2: Write the failing tests**

Create `internal/audit/audit_test.go`:

```go
package audit

import (
	"context"
	"testing"

	"crucible/internal/db/dbtest"
)

func TestLogAndRecent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if err := Log(ctx, pool, "Admin@Crucible.local", "kill_switch.on", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := Log(ctx, pool, "leader@crucible.local", "program.enroll", "forge/forge-201", map[string]any{"enrolled": []string{"t@x"}}, "abc123"); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(ctx, pool, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("recent: %v %v", got, err)
	}
	if got[0].Action != "program.enroll" || got[0].CommitSHA != "abc123" || got[0].Detail["enrolled"] == nil {
		t.Fatalf("newest first with detail: %+v", got[0])
	}
	if got[1].Actor != "admin@crucible.local" {
		t.Fatalf("actor must be lowercased: %q", got[1].Actor)
	}
}
```

Create `internal/jobs/jobs_test.go`:

```go
package jobs

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"crucible/internal/db/dbtest"
)

type pingArgs struct {
	N int `json:"n"`
}

func (pingArgs) Kind() string { return "test_ping" }

type pingWorker struct {
	river.WorkerDefaults[pingArgs]
	got chan int
}

func (w *pingWorker) Work(_ context.Context, j *river.Job[pingArgs]) error {
	w.got <- j.Args.N
	return nil
}

func TestPeriodicAndInsertedJobsRun(t *testing.T) {
	pool := dbtest.New(t) // db.Open ran the River migrations too
	w := &pingWorker{got: make(chan int, 16)}
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	c, err := New(pool, workers, []Periodic{{Every: time.Hour, Args: pingArgs{N: 1}}}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(context.Background()) }()
	if _, err := c.Insert(ctx, pingArgs{N: 2}, nil); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	timeout := time.After(30 * time.Second) // periodic jobs wait for leader election
	for !seen[1] || !seen[2] {
		select {
		case n := <-w.got:
			seen[n] = true
		case <-timeout:
			t.Fatalf("jobs did not run: %v", seen)
		}
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/audit/ ./internal/jobs/ 2>&1 | tail -6`
Expected: FAIL — `undefined: Log`, `undefined: New` (package has no non-test files yet).

- [ ] **Step 4: Implement migrations, audit and jobs**

`internal/db/migrations/00002_audit.sql`:

```sql
-- +goose Up
-- Every privileged action (spec §14), with the platform-repo commit when the action wrote config.
CREATE TABLE audit_log (
  id         BIGSERIAL PRIMARY KEY,
  at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor      TEXT NOT NULL,                -- email, lowercased
  action     TEXT NOT NULL,                -- lab.approve, kill_switch.on, program.update, …
  target     TEXT NOT NULL DEFAULT '',
  detail     JSONB NOT NULL DEFAULT '{}',
  commit_sha TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log (at DESC);

-- +goose Down
DROP TABLE audit_log;
```

`internal/db/db.go`: add imports `"github.com/riverqueue/river/riverdriver/riverpgxv5"` and `"github.com/riverqueue/river/rivermigrate"`, and end `Migrate` with:

```go
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// River keeps its own versioned tables (river_job, river_leader, …).
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	if _, err := m.Migrate(context.Background(), rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	return nil
```

(If the fetched River version's `rivermigrate.New` returns a single value, drop the `err` from that line; the rest is unchanged.)

`internal/audit/audit.go`:

```go
// Package audit records privileged actions (spec §14: "all privileged actions in audit_log").
package audit

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	Detail    map[string]any `json:"detail"`
	CommitSHA string         `json:"commit_sha,omitempty"`
}

func Log(ctx context.Context, db *pgxpool.Pool, actor, action, target string, detail map[string]any, commitSHA string) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := db.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail, commit_sha) VALUES (lower($1), $2, $3, $4, $5)`,
		actor, action, target, detail, commitSHA)
	return err
}

// Recent returns the newest entries first.
func Recent(ctx context.Context, db *pgxpool.Pool, limit int) ([]Entry, error) {
	rows, err := db.Query(ctx, `SELECT at, actor, action, target, detail, commit_sha FROM audit_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.At, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.CommitSHA)
		return e, err
	})
}
```

`internal/jobs/jobs.go`:

```go
// Package jobs owns Crucible's River client (spec §3): retried deliveries and leader-elected periodic sweeps,
// stored in Postgres so they survive restarts.
package jobs

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Periodic is a job inserted every Every (and once at start) by the elected leader.
type Periodic struct {
	Every time.Duration
	Args  river.JobArgs
}

// New builds a client working the default queue. Start it to work jobs; an unstarted client can still insert.
func New(pool *pgxpool.Pool, workers *river.Workers, periodic []Periodic, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	var pj []*river.PeriodicJob
	for _, p := range periodic {
		args := p.Args
		pj = append(pj, river.NewPeriodicJob(river.PeriodicInterval(p.Every),
			func() (river.JobArgs, *river.InsertOpts) { return args, nil },
			&river.PeriodicJobOpts{RunOnStart: true}))
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers:      workers,
		PeriodicJobs: pj,
		Logger:       log,
	})
}
```

- [ ] **Step 5: Run the new tests**

Run: `go test -race ./internal/audit/ ./internal/jobs/ ./internal/db/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestLogAndRecent`, `--- PASS: TestPeriodicAndInsertedJobsRun`, three `ok` lines.

- [ ] **Step 6: Move the lab sweep onto River**

Create `internal/labs/jobs.go`:

```go
package labs

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// SweepArgs is the periodic lab sweep: TTL and idle ends, hung provisioning, stuck destroys (later: schedule
// close, escalations, kill switch).
type SweepArgs struct{}

func (SweepArgs) Kind() string { return "lab_sweep" }

type SweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	S *Service
}

func (w *SweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	w.S.Sweep(ctx)
	return nil
}

// Timeout allows a sweep to destroy several labs (each destroy is bounded at 2 minutes).
func (w *SweepWorker) Timeout(*river.Job[SweepArgs]) time.Duration { return 15 * time.Minute }
```

In `internal/labs/service.go`: delete `RunSweeper` entirely; add `sweepMu sync.Mutex // one sweep at a time in this process` to `Service`; start `Sweep` with:

```go
func (s *Service) Sweep(ctx context.Context) {
	if !s.sweepMu.TryLock() {
		return // a slow sweep is still running; the next periodic job picks up whatever it missed
	}
	defer s.sweepMu.Unlock()
	now := s.Now()
```

In `cmd/crucible-api/main.go` add imports `"github.com/riverqueue/river"` and `"crucible/internal/jobs"`, delete `go labSvc.RunSweeper(ctx, 15*time.Second)`, and after `hub.OnHello = …` add:

```go
	workers := river.NewWorkers()
	river.AddWorker(workers, &labs.SweepWorker{S: labSvc})
	riverLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	jobClient, err := jobs.New(pool, workers, []jobs.Periodic{{Every: 15 * time.Second, Args: labs.SweepArgs{}}}, riverLog)
	if err != nil {
		return err
	}
	if err := jobClient.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = jobClient.Stop(stop)
	}()
```

- [ ] **Step 7: Run everything**

Run: `go build ./... && go test -race ./... 2>&1 | grep -v '^ok' ; gofmt -l . ; go vet ./...`
Expected: no output other than possibly `?   … [no test files]` lines.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/db internal/audit internal/jobs internal/labs cmd/crucible-api/main.go
git commit -m "feat(jobs): River job queue, lab sweep as a periodic job, audit log

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: Notifications: email + Slack/Teams webhooks as River jobs, per-user email mutes, sync and setup failures

**Files:**
- Create: `internal/db/migrations/00003_notifications.sql`
- Create: `internal/notify/notify.go`, `internal/notify/http.go`, `internal/notify/notify_test.go`
- Modify: `internal/gitsync/syncer.go` (`Syncer.OnProblem`, two call sites in `SyncOnce`), `internal/gitsync/syncer_test.go`
- Modify: `internal/labs/service.go` (`Notifier`, `Service.Notify`, `notify` helper, `runSetup`), `internal/labs/service_test.go` (fixture + assertion)
- Modify: `internal/httpapi/server.go` (`Deps.Notify`, routes)
- Modify: `cmd/crucible-api/main.go`
- Modify: `deploy/helm/crucible/templates/crucible.yaml` (optional SMTP env)
- Modify: `web/src/pages/Settings.tsx`, `web/src/types.ts`

**Interfaces:**
- Consumes: `jobs` River client (Task 3), `config.Team.Notifications` (Task 2).
- Produces (used by Tasks 5–8):
  - `notify.Kind` constants: `LabPending = "lab_request_pending"`, `LabEscalated = "lab_request_escalated"`, `LabApproved = "lab_request_approved"`, `LabRejected = "lab_request_rejected"`, `BudgetAlert = "budget_alert"`, `SyncFailed = "content_sync_failed"`, `SetupFailed = "setup_failed"`
  - `notify.Event{Kind Kind; To []string; Team string; Subject, Text, Link string}` — `Team` non-empty also posts to that team's webhooks; `Link` is an app path appended to the email body as `PublicURL + Link`
  - `(*notify.Service).Notify(ctx context.Context, ev notify.Event) error`
  - `labs.Notifier interface { Notify(ctx context.Context, ev notify.Event) error }`; `labs.Service.Notify Notifier`; `(*labs.Service).notify(ctx, ev)` (nil-safe, logs errors)
  - `gitsync.Syncer.OnProblem func(key string, problems []content.Problem)`
  - HTTP: `GET /api/me/notifications` → `{"email_enabled": bool, "kinds": [{"kind": "lab_request_pending", "label": "…", "muted": false}, …]}`; `PUT /api/me/notifications` body `{"muted": ["budget_alert"]}` → 204

- [ ] **Step 1: Write the failing notify tests**

Create `internal/notify/notify_test.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/jobs"
)

// fakeSMTP accepts one plain SMTP session and hands back the message body it received.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		_ = tp.PrintfLine("220 fake ESMTP")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line + " x")[0]) {
			case "EHLO", "HELO":
				_ = tp.PrintfLine("250 fake")
			case "DATA":
				_ = tp.PrintfLine("354 go ahead")
				b, _ := tp.ReadDotBytes()
				got <- string(b)
				_ = tp.PrintfLine("250 ok")
			case "QUIT":
				_ = tp.PrintfLine("221 bye")
				return
			default: // MAIL, RCPT, RSET, NOOP
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestEmailIsSentWithoutHeaderInjection(t *testing.T) {
	addr, got := fakeSMTP(t)
	s := &Service{SMTP: SMTPConfig{Addr: addr, From: "crucible@example.com"}}
	err := s.sendEmail(EmailArgs{To: "trainee@crucible.local", Subject: "Lab \"x\"\r\nBcc: evil@example.com", Body: "Hello.\n.\nhttps://crucible.example/approvals"})
	if err != nil {
		t.Fatal(err)
	}
	msg := <-got
	if strings.Contains(msg, "\nBcc:") {
		t.Fatalf("header injected:\n%s", msg)
	}
	for _, want := range []string{"To: trainee@crucible.local", "Subject: ", "Content-Type: text/plain; charset=UTF-8", "https://crucible.example/approvals"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestWebhooksPostSlackTextAndTeamsCards(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodies = append(bodies, m)
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	plat.Teams["forge"].Notifications = config.TeamNotifications{SlackWebhook: srv.URL + "/slack", TeamsWebhook: srv.URL + "/broken"}
	st := &gitsync.State{Platform: plat}
	s := &Service{State: func() *gitsync.State { return st }, HTTP: srv.Client()}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "forge", Flavor: "slack", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["text"] != "hi" {
		t.Fatalf("slack payload %v", bodies[0])
	}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "forge", Flavor: "teams", Text: "hi"}); err == nil {
		t.Fatal("a non-2xx answer must fail the job so River retries it")
	}
	if bodies[1]["type"] != "message" || bodies[1]["attachments"] == nil {
		t.Fatalf("teams payload %v", bodies[1])
	}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "gone", Flavor: "slack", Text: "hi"}); err != nil {
		t.Fatalf("a team removed since queueing is skipped, got %v", err)
	}
}

func TestNotifyQueuesUnmutedEmailsAndTeamPosts(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, _ := config.Load("../../examples/platform")
	plat.Teams["forge"].Notifications.SlackWebhook = "https://hooks.example/slack"
	st := &gitsync.State{Platform: plat}
	s := &Service{DB: pool, State: func() *gitsync.State { return st }, PublicURL: "https://crucible.example",
		SMTP: SMTPConfig{Addr: "127.0.0.1:1", From: "c@x"}, Log: slog.Default()}
	workers := river.NewWorkers()
	river.AddWorker(workers, &EmailWorker{S: s})
	river.AddWorker(workers, &WebhookWorker{S: s})
	client, err := jobs.New(pool, workers, nil, slog.Default()) // never started: insert only
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs = client
	trainee, _ := auth.Store{DB: pool}.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	if err := s.SetMutes(ctx, trainee.ID, []Kind{LabApproved}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMutes(ctx, trainee.ID, []Kind{"bogus"}); err == nil {
		t.Fatal("unknown kinds must be rejected")
	}
	err = s.Notify(ctx, Event{Kind: LabApproved, To: []string{"Trainee@crucible.local", "leader@crucible.local", "leader@crucible.local"},
		Team: "forge", Subject: "Approved", Text: "Go.", Link: "/p/forge/forge-101/m/02-first-lab/lab"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := pool.Query(ctx, `SELECT kind, args FROM river_job ORDER BY id`)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind string
		var args map[string]any
		_ = rows.Scan(&kind, &args)
		got = append(got, kind+":"+anyStr(args["to"])+anyStr(args["flavor"]))
		if kind == "notify_email" && !strings.Contains(anyStr(args["body"]), "https://crucible.example/p/forge/forge-101/m/02-first-lab/lab") {
			t.Fatalf("email body lacks the link: %v", args)
		}
	}
	if strings.Join(got, ",") != "notify_email:leader@crucible.local,notify_webhook:slack" {
		t.Fatalf("queued %v (the muted trainee gets no email, duplicates collapse)", got)
	}
}

func anyStr(v any) string { s, _ := v.(string); return s }

```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/notify/ 2>&1 | tail -5`
Expected: FAIL — `undefined: Service`.

- [ ] **Step 3: Implement the migration and the notify package**

`internal/db/migrations/00003_notifications.sql`:

```sql
-- +goose Up
-- Email kinds a user turned off (spec §10: "Users can mute per event type (email) in settings").
CREATE TABLE notification_mutes (
  user_id BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  kind    TEXT NOT NULL,
  PRIMARY KEY (user_id, kind)
);

-- +goose Down
DROP TABLE notification_mutes;
```

`internal/notify/notify.go`:

```go
// Package notify delivers Crucible events by email (SMTP) and Slack/Teams incoming webhooks (spec §10).
// Each delivery is a River job, so a flaky mail server or webhook is retried instead of lost.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

type Kind string

const (
	LabPending   Kind = "lab_request_pending"
	LabEscalated Kind = "lab_request_escalated"
	LabApproved  Kind = "lab_request_approved"
	LabRejected  Kind = "lab_request_rejected" // also expired
	BudgetAlert  Kind = "budget_alert"
	SyncFailed   Kind = "content_sync_failed"
	SetupFailed  Kind = "setup_failed"
	// Submission and rank-up kinds arrive with scoring (M5) and ranks (M7).
)

type KindInfo struct {
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
}

// Kinds lists every event a user can mute by email, with the label the settings page shows.
var Kinds = []KindInfo{
	{LabPending, "A lab request is waiting for my approval"},
	{LabEscalated, "A lab request was escalated to me"},
	{LabApproved, "My lab request was approved"},
	{LabRejected, "My lab request was rejected or expired"},
	{BudgetAlert, "A budget reaches 80% or its hard cap"},
	{SyncFailed, "Content I maintain failed to sync"},
	{SetupFailed, "A lab scenario I maintain failed to prepare"},
}

type Event struct {
	Kind    Kind
	To      []string // recipient emails
	Team    string   // when set, also posted to the team's Slack/Teams webhooks
	Subject string
	Text    string
	Link    string // app path, e.g. /approvals
}

type SMTPConfig struct{ Addr, From, Username, Password string } // Addr "" turns email off

type Service struct {
	DB        *pgxpool.Pool
	Jobs      *river.Client[pgx.Tx]
	State     func() *gitsync.State
	PublicURL string
	SMTP      SMTPConfig
	HTTP      *http.Client // webhooks; nil = 10 s timeout, no redirects
	Log       *slog.Logger
}

var defaultHTTP = &http.Client{Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // no redirect hops to internal hosts

const maxAttempts = 8

// Notify queues one email per unmuted recipient and one post per configured team webhook.
func (s *Service) Notify(ctx context.Context, ev Event) error {
	body := ev.Text
	if ev.Link != "" {
		body += "\n\n" + s.PublicURL + ev.Link
	}
	opts := &river.InsertOpts{MaxAttempts: maxAttempts}
	if s.SMTP.Addr != "" {
		to, err := s.unmuted(ctx, ev.Kind, ev.To)
		if err != nil {
			return err
		}
		for _, addr := range to {
			if _, err := s.Jobs.Insert(ctx, EmailArgs{To: addr, Subject: ev.Subject, Body: body}, opts); err != nil {
				return err
			}
		}
	}
	if ev.Team != "" {
		for _, flavor := range []string{"slack", "teams"} {
			if s.webhookURL(ev.Team, flavor) == "" {
				continue
			}
			if _, err := s.Jobs.Insert(ctx, WebhookArgs{Team: ev.Team, Flavor: flavor, Text: ev.Subject + "\n" + body}, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) unmuted(ctx context.Context, kind Kind, to []string) ([]string, error) {
	var list []string
	for _, e := range to {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && !slices.Contains(list, e) {
			list = append(list, e)
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT u.email FROM notification_mutes m JOIN users u ON u.id = m.user_id
		WHERE m.kind = $1 AND u.email = ANY($2)`, string(kind), list)
	if err != nil {
		return nil, err
	}
	muted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list, func(e string) bool { return slices.Contains(muted, e) }), nil
}

func (s *Service) webhookURL(team, flavor string) string {
	st := s.State()
	if st == nil || st.Platform == nil || st.Platform.Teams[team] == nil {
		return ""
	}
	n := st.Platform.Teams[team].Notifications
	if flavor == "teams" {
		return n.TeamsWebhook
	}
	return n.SlackWebhook
}

type EmailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (EmailArgs) Kind() string { return "notify_email" }

type EmailWorker struct {
	river.WorkerDefaults[EmailArgs]
	S *Service
}

func (w *EmailWorker) Work(_ context.Context, j *river.Job[EmailArgs]) error { return w.S.sendEmail(j.Args) }

var headerSafe = strings.NewReplacer("\r", " ", "\n", " ")

func (s *Service) sendEmail(a EmailArgs) error {
	if s.SMTP.Addr == "" {
		return nil
	}
	msg := "From: " + s.SMTP.From + "\r\nTo: " + headerSafe.Replace(a.To) +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", headerSafe.Replace(a.Subject)) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(a.Body, "\n", "\r\n")
	var auth smtp.Auth
	if s.SMTP.Username != "" {
		host, _, _ := net.SplitHostPort(s.SMTP.Addr)
		auth = smtp.PlainAuth("", s.SMTP.Username, s.SMTP.Password, host) // net/smtp refuses PLAIN without TLS except on localhost
	}
	return smtp.SendMail(s.SMTP.Addr, auth, s.SMTP.From, []string{a.To}, []byte(msg))
}

type WebhookArgs struct {
	Team   string `json:"team"`
	Flavor string `json:"flavor"` // slack | teams
	Text   string `json:"text"`
}

func (WebhookArgs) Kind() string { return "notify_webhook" }

type WebhookWorker struct {
	river.WorkerDefaults[WebhookArgs]
	S *Service
}

func (w *WebhookWorker) Work(ctx context.Context, j *river.Job[WebhookArgs]) error {
	return w.S.postWebhook(ctx, j.Args)
}

// postWebhook resolves the URL at send time (it lives in team.yaml, not in the job row).
func (s *Service) postWebhook(ctx context.Context, a WebhookArgs) error {
	url := s.webhookURL(a.Team, a.Flavor)
	if url == "" {
		return nil // removed from team.yaml since it was queued
	}
	var payload any = map[string]string{"text": a.Text}
	if a.Flavor == "teams" {
		payload = teamsCard(a.Text)
	}
	b, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.HTTP
	if client == nil {
		client = defaultHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s webhook for team %s: HTTP %d", a.Flavor, a.Team, resp.StatusCode)
	}
	return nil
}

// teamsCard wraps text in the Adaptive Card envelope that Teams "Workflows" incoming webhooks accept.
func teamsCard(text string) any {
	return map[string]any{"type": "message", "attachments": []any{map[string]any{
		"contentType": "application/vnd.microsoft.card.adaptive",
		"content": map[string]any{"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
			"body": []any{map[string]any{"type": "TextBlock", "text": text, "wrap": true}}},
	}}}
}

// Mutes lists the kinds a user muted.
func (s *Service) Mutes(ctx context.Context, userID int64) ([]Kind, error) {
	rows, err := s.DB.Query(ctx, `SELECT kind FROM notification_mutes WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[Kind])
}

// SetMutes replaces the user's muted kinds.
func (s *Service) SetMutes(ctx context.Context, userID int64, kinds []Kind) error {
	for _, k := range kinds {
		if !slices.ContainsFunc(Kinds, func(i KindInfo) bool { return i.Kind == k }) {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("unknown notification kind %q", k))
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM notification_mutes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, k := range kinds {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_mutes (user_id, kind) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, string(k)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReportSyncProblem tells admins, and the maintainers of the affected training, that a new commit was rejected
// (spec §6, §10). key is "platform", "<training>", "<training>@<sha>" or "<team>/<training>" (a bad pin).
func (s *Service) ReportSyncProblem(ctx context.Context, key string, probs []content.Problem) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return
	}
	to := slices.Clone(st.Platform.Admins)
	what := "The platform config"
	if key != "platform" {
		training, _, _ := strings.Cut(key, "@")
		if _, tr, ok := strings.Cut(training, "/"); ok {
			training = tr
		}
		what = "Training " + training
		for _, t := range st.Trainings {
			if t.ID == training {
				to = append(to, t.Maintainers...)
			}
		}
	}
	var lines []string
	for i, p := range probs {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("…and %d more", len(probs)-5))
			break
		}
		lines = append(lines, p.String())
	}
	err := s.Notify(ctx, Event{Kind: SyncFailed, To: to, Subject: what + " failed to sync",
		Text: what + " has a new commit that Crucible rejected; the previous version stays live.\n\n" + strings.Join(lines, "\n"),
		Link: "/admin"})
	if err != nil {
		s.Log.Error("queueing sync-failure notification failed", "key", key, "err", err)
	}
}
```

`internal/notify/http.go`:

```go
package notify

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/httpx"
)

type kindPref struct {
	KindInfo
	Muted bool `json:"muted"`
}

func (s *Service) Routes(r chi.Router) {
	r.Get("/api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		muted, err := s.Mutes(r.Context(), auth.UserFrom(r.Context()).ID)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		out := []kindPref{}
		for _, k := range Kinds {
			out = append(out, kindPref{k, slices.Contains(muted, k.Kind)})
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"email_enabled": s.SMTP.Addr != "", "kinds": out})
	})
	r.Put("/api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Muted []Kind `json:"muted"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		if err := s.SetMutes(r.Context(), auth.UserFrom(r.Context()).ID, body.Muted); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
```

- [ ] **Step 4: Run the notify tests**

Run: `go test -race ./internal/notify/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: three `--- PASS` lines and `ok  	crucible/internal/notify`.

- [ ] **Step 5: Write the failing gitsync and labs tests**

Append to `internal/gitsync/syncer_test.go`:

```go
func TestOnProblemFiresOncePerNewProblem(t *testing.T) {
	s, _, trainingRepo := setup(t) // first sync happened before the hook was set: nothing to announce
	var keys []string
	s.OnProblem = func(key string, _ []content.Problem) { keys = append(keys, key) }
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"})
	for range 2 {
		if err := s.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 1 || keys[0] != "t1@"+s.Current().Heads["t1"] {
		t.Fatalf("OnProblem keys = %v", keys)
	}
}
```

(add `"crucible/internal/content"` to that file's imports).

In `internal/labs/service_test.go` add a recorder and wire it into the fixture:

```go
type fakeNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (n *fakeNotifier) Notify(_ context.Context, ev notify.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
	return nil
}

// last returns the most recent event of kind, or nil.
func (n *fakeNotifier) last(kind notify.Kind) *notify.Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := len(n.events) - 1; i >= 0; i-- {
		if n.events[i].Kind == kind {
			ev := n.events[i]
			return &ev
		}
	}
	return nil
}
```

Add `notes *fakeNotifier` to `fx`; in `setup` create `notes := &fakeNotifier{}`, set `Notify: notes` on the `Service`, and return it in the fixture. Import `"crucible/internal/notify"`. At the end of `TestSetupFailureAllowsSkipAndResetIsRateLimited`, add:

```go
	if ev := f.notes.last(notify.SetupFailed); ev == nil || len(ev.To) != 1 || ev.To[0] != "senior@crucible.local" {
		t.Fatalf("maintainers must hear about a failed scenario: %+v", ev)
	}
```

- [ ] **Step 6: Run them to see them fail**

Run: `go test ./internal/gitsync/ ./internal/labs/ 2>&1 | tail -6`
Expected: FAIL — `s.OnProblem undefined`, `unknown field Notify in struct literal`.

- [ ] **Step 7: Implement the hook and the labs notifier**

`internal/gitsync/syncer.go`: add the field to `Syncer`:

```go
	// OnProblem, if set, is called for every problem key that is new compared with the previous sync. The first sync
	// after start never calls it, so a restart does not re-announce old failures. Keys: "platform", "<training>",
	// "<training>@<sha>", "<team>/<training>". It runs inside SyncOnce and must not call SyncOnce.
	OnProblem func(key string, problems []content.Problem)
```

In `SyncOnce`, inside the `if err != nil {` branch after `config.Load`, before `s.cur.Store(&next)`:

```go
		if prev != nil && prev.PlatformErr != err.Error() && s.OnProblem != nil {
			s.OnProblem("platform", []content.Problem{{File: "platform", Msg: err.Error()}})
		}
```

and just before the final `s.cur.Store(st)`:

```go
	if prev != nil && s.OnProblem != nil {
		for key, probs := range st.Problems {
			if _, seen := prev.Problems[key]; !seen {
				s.OnProblem(key, probs)
			}
		}
	}
```

`internal/labs/service.go`: add the import `"crucible/internal/notify"`, the interface and field:

```go
// Notifier queues notifications (implemented by *notify.Service).
type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}
```

`Service` gains `Notify Notifier`. Add the helper:

```go
// notify queues ev; a failure is logged, never returned: the lab action itself already happened.
func (s *Service) notify(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || (len(ev.To) == 0 && ev.Team == "") {
		return
	}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		s.Log.Error("queueing notification failed", "kind", ev.Kind, "err", err)
	}
}
```

In `runSetup`, replace the `// ponytail: maintainers are notified …` comment and the `s.Log.Warn` line with:

```go
	s.Log.Warn("setup script failed twice", "lab", inst.ID, "task", taskID)
	if t := s.trainingOf(inst); t != nil {
		step := "the lab-level setup"
		if taskID != "" {
			step = "the setup for task " + taskID
		}
		s.notify(ctx, notify.Event{Kind: notify.SetupFailed, To: t.Maintainers,
			Subject: fmt.Sprintf("Lab scenario failed to prepare: %s / %s", inst.Training, inst.Module),
			Text: fmt.Sprintf("%s in %s/%s exited non-zero twice (lab %s, team %s). The trainee was offered a skip. Script output is stored in setup_runs.",
				step, inst.Training, inst.Module, inst.ID, inst.Team)})
	}
```

(keep the existing `s.event(ctx, inst.ID, "setup_failed", taskID)` and `return last`), and add the helper next to `labContent`:

```go
// trainingOf returns the content version a lab runs, or nil while syncing / if it vanished.
func (s *Service) trainingOf(inst *Instance) *content.Training {
	if st := s.Learn.State(); st != nil {
		return st.Training(inst.Training, inst.SHA)
	}
	return nil
}
```

`internal/httpapi/server.go`: import `"crucible/internal/notify"`, add `Notify *notify.Service` to `Deps`, and inside the authenticated group after `d.Labs.Routes(r)`:

```go
		if d.Notify != nil {
			d.Notify.Routes(r)
		}
```

`cmd/crucible-api/main.go`: import `"crucible/internal/content"` and `"crucible/internal/notify"`. Before `labSvc := …`:

```go
	notifySvc := &notify.Service{DB: pool, State: syncer.Current, PublicURL: public, Log: slog.Default(),
		SMTP: notify.SMTPConfig{Addr: os.Getenv("CRUCIBLE_SMTP_ADDR"), From: env("CRUCIBLE_SMTP_FROM", "crucible@localhost"),
			Username: os.Getenv("CRUCIBLE_SMTP_USERNAME"), Password: os.Getenv("CRUCIBLE_SMTP_PASSWORD")}}
	if notifySvc.SMTP.Addr == "" {
		slog.Warn("CRUCIBLE_SMTP_ADDR is not set: email notifications are off (Slack/Teams webhooks still work)")
	}
```

Set `Notify: notifySvc` in the `labs.Service` literal. Register the workers next to the sweep worker and give the client to the service after it is built:

```go
	river.AddWorker(workers, &notify.EmailWorker{S: notifySvc})
	river.AddWorker(workers, &notify.WebhookWorker{S: notifySvc})
	// … jobs.New(...) as before …
	notifySvc.Jobs = jobClient
	syncer.OnProblem = func(key string, probs []content.Problem) { notifySvc.ReportSyncProblem(ctx, key, probs) }
```

Move `go syncer.Run(ctx, every)` to after the `syncer.OnProblem` assignment (so the hook is set before the loop starts). Add `Notify: notifySvc` to `httpapi.Deps`.

`deploy/helm/crucible/templates/crucible.yaml`: in the api container `env:` list, after `OIDC_CLIENT_SECRET`, add:

```yaml
            - name: CRUCIBLE_SMTP_ADDR
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_SMTP_ADDR, optional: true } }
            - name: CRUCIBLE_SMTP_FROM
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_SMTP_FROM, optional: true } }
            - name: CRUCIBLE_SMTP_USERNAME
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_SMTP_USERNAME, optional: true } }
            - name: CRUCIBLE_SMTP_PASSWORD
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_SMTP_PASSWORD, optional: true } }
```

Add to `docs/runbooks/aws.md` under Secrets: `**Email (optional).** Add CRUCIBLE_SMTP_ADDR (host:port), CRUCIBLE_SMTP_FROM, CRUCIBLE_SMTP_USERNAME and CRUCIBLE_SMTP_PASSWORD to the crucible-secrets Secret (kubectl -n crucible edit secret crucible-secrets) and restart the api deployment. Without them Crucible sends no email; Slack/Teams webhooks in team.yaml still work.`

- [ ] **Step 8: Settings page: email mutes**

`web/src/types.ts` add:

```ts
export type NotificationPrefs = { email_enabled: boolean; kinds: { kind: string; label: string; muted: boolean }[] }
```

In `web/src/pages/Settings.tsx` add imports `useState` from 'react', `api` from '../api', `useFetch` from '../useFetch', `toast` from '../lib/alerts', type `NotificationPrefs`; render `<NotificationSettings />` after the Motion section, and add:

```tsx
function NotificationSettings() {
  const { data, error, reload } = useFetch<NotificationPrefs>('/api/me/notifications')
  const [saving, setSaving] = useState(false)
  if (error) return <p className="error">{error.message}</p>
  if (!data) return null
  const toggle = async (kind: string, muted: boolean) => {
    const next = data.kinds.filter((k) => (k.kind === kind ? muted : k.muted)).map((k) => k.kind)
    setSaving(true)
    try {
      await api('/api/me/notifications', { method: 'PUT', json: { muted: next } })
      reload()
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setSaving(false)
    }
  }
  return (
    <>
      <h2>Email notifications</h2>
      {!data.email_enabled && <p className="muted">Email is not configured on this Crucible yet; these choices apply once it is.</p>}
      <fieldset className="stack" disabled={saving}>
        <legend className="muted">Email me when…</legend>
        {data.kinds.map((k) => (
          <label key={k.kind}>
            <input type="checkbox" checked={!k.muted} onChange={(e) => toggle(k.kind, !e.target.checked)} /> {k.label}
          </label>
        ))}
      </fieldset>
    </>
  )
}
```

Add to `web/src/theme/app.css`:

```css
.stack { display: grid; gap: 0.6rem; max-width: 44rem; border: 0; padding: 0; margin: 0 0 1.5rem; }
.stack label { display: grid; gap: 0.25rem; }
.stack label:has(> input[type='checkbox']) { display: flex; gap: 0.5rem; align-items: center; }
```

- [ ] **Step 9: Run everything**

Run: `go test -race ./... 2>&1 | grep -v -E '^(ok|\?)' ; gofmt -l . ; go vet ./... ; (cd web && npm run build >/dev/null && npm test 2>&1 | tail -3)`
Expected: no Go failures; vitest `Tests  N passed`.

- [ ] **Step 10: Commit**

```bash
git add internal/db/migrations/00003_notifications.sql internal/notify internal/gitsync internal/labs internal/httpapi cmd/crucible-api/main.go deploy/helm docs/runbooks/aws.md web/src
git commit -m "feat(notify): email and Slack/Teams notifications as River jobs, email mutes, sync and setup failures

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 5: Lab requests with cost estimates and tiered approvals

A request is priced (`Estimator` per runtime × TTL), routed to a tier, and either starts at once (`auto`) or waits in `pending_approval` until someone allowed to approve it decides. Approvers get an inbox with the estimate and spend context.

**Files:**
- Create: `internal/db/migrations/00004_lab_requests.sql`
- Modify: `internal/rbac/rbac.go`, `internal/rbac/rbac_test.go`
- Modify: `internal/labs/model.go` (states, `Instance`, `Estimator`, `FixedRates`, `ParseRates`)
- Modify: `internal/labs/service.go` (`instCols`, `scanInst`, `Service`, `View`, `ModuleLab`, `view`, `ModuleLab()`, `Start`, `End`; new `quote`, `insert`, `recentlyApproved`, `collectInst`)
- Create: `internal/labs/approvals.go`, `internal/labs/approvals_test.go`
- Modify: `internal/labs/http.go` (two routes)
- Modify: `internal/labs/service_test.go` (fixture), `internal/labs/model_test.go` (ParseRates)
- Modify: `cmd/crucible-api/main.go`

**Interfaces:**
- Consumes: `config.CostTiers`, `Settings.Escalation()`, `Platform.ProgramSchedule`, `*Schedule` methods (Task 2); `audit.Log` (Task 3); `notify.Event`, kinds, `labs.Service.notify` (Task 4).
- Produces (used by Tasks 6–8, 11):
  - rbac: `TierAuto="auto"`, `TierApprover="approver"`, `TierLeader="leader"`, `TierAdmin="admin"`; `Tier(runtime string, estimateUSD float64, t config.CostTiers) string`; `NextTier(tier string) string`; `(Checker) TierApprovers(tier, team, training, requester string) []string`; `(Checker) Route(tier, team, training, requester string) string`; `(Checker) MayApprove(actor, requester, team, training string, estimateUSD float64, overCap bool) bool`
  - labs states: `PendingApproval="pending_approval"`, `Rejected="rejected"`, `Expired="expired"` (plus the existing ones)
  - `labs.Instance` new fields: `HourlyUSD, EstimateUSD float64; Tier string; OverCap bool; EscalateAt, DecidedAt *time.Time; DecidedBy, DecisionNote string`
  - `labs.Estimator interface { HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error) }`; `labs.FixedRates map[string]float64` (by lab id); `labs.ParseRates(s string) (FixedRates, error)`; `labs.Service.Estimators map[string]Estimator`
  - `type quote struct { Timing Timing; HourlyUSD, EstimateUSD float64; Tier string; OverCap bool; Blocked string }` and `(s *Service) quote(ctx, p *config.Platform, u *auth.User, team, training, module string, lab *content.Lab) (*quote, error)` — Tasks 7 and 8 add checks inside it
  - `labs.Spend{SpentUSD, CommittedUSD, BudgetUSD, CapUSD float64}` (json `spent_usd, committed_usd, budget_usd, cap_usd`); `(s *Service) spend(ctx, p *config.Platform, team, training string) (Spend, error)`; `monthStart(t time.Time) time.Time`
  - `labs.Approval`, `labs.RecentLab`, `labs.ScheduleInfo` (JSON below); `(s *Service) Approvals(ctx, u *auth.User) ([]Approval, error)`; `(s *Service) Decide(ctx, u *auth.User, labID string, approve bool, note string) (State, error)`
  - helpers `(s *Service) requester(ctx, userID int64) (email, name string, err error)`, `(s *Service) notifyRequest(ctx, p *config.Platform, inst *Instance, requester string, kind notify.Kind)`, `labLink(inst *Instance) string`, `scheduleInfo(p *config.Platform, team, training string, now time.Time) ScheduleInfo`, `collectInst(rows pgx.Rows) ([]*Instance, error)`
  - `View` gains `estimate_usd, tier, escalate_at?, decided_by?, decision_note?, over_cap`; `ModuleLab` gains `estimate_usd, needs_approval, blocked?`
  - HTTP: `GET /api/approvals` → `Approval[]`; `POST /api/approvals/{id}` body `{"approve": true, "note": "…"}` → `{"state": "provisioning"|"rejected"}`

`Approval` JSON (exact):

```json
{"id":"0a1b2c3d4e5f","requester":"trainee@crucible.local","requester_name":"Tara","team":"forge","training":"forge-101",
 "module":"02-first-lab","lab_title":"First Heat: Your First Lab","runtime":"local","hourly_usd":0.5,"estimate_usd":0.5,
 "ttl_s":3600,"tier":"approver","over_cap":false,"requested_at":"…","escalate_at":"…",
 "team_spend":{"spent_usd":0,"committed_usd":0,"budget_usd":200,"cap_usd":250},
 "program_spend":{"spent_usd":0,"committed_usd":0,"budget_usd":0,"cap_usd":0},
 "recent":[{"module":"02-first-lab","state":"destroyed","end_reason":"user","estimate_usd":0,"created_at":"…"}],
 "schedule":{"name":"","text":"any time","open":true}}
```

- [ ] **Step 1: Write the failing rbac tests**

Append to `internal/rbac/rbac_test.go`:

```go
func TestTierRouting(t *testing.T) {
	tiers := config.CostTiers{AutoApproveUSD: 0, Tier1USD: 5, Tier2USD: 25}
	cases := []struct {
		runtime string
		est     float64
		want    string
	}{
		{"local", 0, TierAuto}, {"cluster", 0, TierAuto}, {"aws", 0, TierApprover}, {"local", 0.5, TierApprover},
		{"cluster", 5, TierApprover}, {"cluster", 5.01, TierLeader}, {"aws", 25, TierLeader}, {"aws", 25.5, TierAdmin},
	}
	for _, c := range cases {
		if got := Tier(c.runtime, c.est, tiers); got != c.want {
			t.Errorf("Tier(%s, %v) = %s, want %s", c.runtime, c.est, got, c.want)
		}
	}
	if NextTier(TierApprover) != TierLeader || NextTier(TierLeader) != TierAdmin || NextTier(TierAdmin) != "" {
		t.Error("escalation ladder: approver → leader → admin → none")
	}
}

func TestApprovalRules(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	c := Checker{P: p}
	const (
		admin   = "admin@crucible.local"
		leader  = "leader@crucible.local"
		senior  = "senior@crucible.local"
		trainee = "trainee@crucible.local"
	)
	may := func(actor, requester string, est float64, overCap bool) bool {
		return c.MayApprove(actor, requester, "forge", "forge-101", est, overCap)
	}
	switch {
	case !may(leader, trainee, 4, false):
		t.Error("the leader (default approver) approves tier 1")
	case !may(leader, trainee, 25, false):
		t.Error("the leader approves up to tier 2")
	case may(leader, trainee, 26, false):
		t.Error("above tier 2 needs an admin")
	case may(leader, "LEADER@crucible.local", 1, false):
		t.Error("nobody approves their own request")
	case may(senior, trainee, 1, false):
		t.Error("a senior who is not an approver cannot approve")
	case may(leader, trainee, 1, true):
		t.Error("over-cap requests are admin-only")
	case !may(admin, trainee, 1000, true):
		t.Error("admins approve anything, including over-cap overrides")
	case may(admin, admin, 1, false):
		t.Error("admins cannot approve their own request either")
	}
	if got := c.Route(TierApprover, "forge", "forge-101", leader); got != TierAdmin {
		t.Errorf("leader's own request skips the approver and leader tiers: %s", got)
	}
	if got := c.Route(TierApprover, "forge", "forge-101", trainee); got != TierApprover {
		t.Errorf("trainee's request starts with the approver: %s", got)
	}
	if got := c.TierApprovers(TierAdmin, "forge", "forge-101", admin); len(got) != 0 {
		t.Errorf("the requester is never listed: %v", got)
	}
	p.Teams["forge"].Programs["forge-101"].Roles.Approvers = []string{senior}
	if !may(senior, trainee, 5, false) || may(senior, trainee, 5.5, false) {
		t.Error("a program approver approves up to tier 1 only")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/rbac/ 2>&1 | tail -4`
Expected: FAIL — `undefined: Tier`.

- [ ] **Step 3: Implement the rbac rules**

Append to `internal/rbac/rbac.go`:

```go
// Approval tiers, lowest first (spec §9.1).
const (
	TierAuto     = "auto"
	TierApprover = "approver"
	TierLeader   = "leader"
	TierAdmin    = "admin"
)

// Tier routes a lab request by its estimate: local/cluster at or under auto_approve_usd start at once (aws never
// does); up to tier1 a program approver decides; up to tier2 the team leader; above that an admin.
func Tier(runtime string, estimateUSD float64, t config.CostTiers) string {
	switch {
	case runtime != "aws" && estimateUSD <= t.AutoApproveUSD:
		return TierAuto
	case estimateUSD <= t.Tier1USD:
		return TierApprover
	case estimateUSD <= t.Tier2USD:
		return TierLeader
	}
	return TierAdmin
}

// NextTier is one escalation step: approver → leader → admin → "" (nobody left: the request expires).
func NextTier(tier string) string {
	switch tier {
	case TierApprover:
		return TierLeader
	case TierLeader:
		return TierAdmin
	}
	return ""
}

// TierApprovers lists who decides at a tier, never the requester. Used to route and to notify.
func (c Checker) TierApprovers(tier, team, training, requester string) []string {
	requester = strings.ToLower(requester)
	var list []string
	t := c.P.Teams[team]
	switch {
	case tier == TierApprover && t != nil && t.Programs[training] != nil:
		list = t.Programs[training].Roles.Approvers
	case tier == TierLeader && t != nil:
		list = []string{t.Leader}
	case tier == TierAdmin:
		list = c.P.Admins
	}
	return slices.DeleteFunc(slices.Clone(list), func(e string) bool { return e == requester || e == "" })
}

// Route returns the first tier at or above tier with someone other than the requester to decide. Admin is the
// last stop even when it is empty; such a request expires unanswered.
func (c Checker) Route(tier, team, training, requester string) string {
	for t := tier; t != ""; t = NextTier(t) {
		if t == TierAdmin || len(c.TierApprovers(t, team, training, requester)) > 0 {
			return t
		}
	}
	return TierAdmin
}

// MayApprove: admins approve any amount and over-cap overrides; the team leader up to tier2; program approvers up
// to tier1; nobody their own request (spec §5.3). Eligibility follows the amount, not the request's current tier:
// escalation adds deciders, it never removes them.
func (c Checker) MayApprove(actor, requester, team, training string, estimateUSD float64, overCap bool) bool {
	actor = strings.ToLower(actor)
	if actor == strings.ToLower(requester) {
		return false
	}
	if c.IsAdmin(actor) {
		return true
	}
	tiers, t := c.P.Settings.CostTiers, c.P.Teams[team]
	if overCap || tiers == nil || t == nil {
		return false
	}
	if t.Leader == actor {
		return estimateUSD <= tiers.Tier2USD
	}
	if p := t.Programs[training]; p != nil && slices.Contains(p.Roles.Approvers, actor) {
		return estimateUSD <= tiers.Tier1USD
	}
	return false
}
```

- [ ] **Step 4: Run the rbac tests**

Run: `go test -race ./internal/rbac/`
Expected: `ok  	crucible/internal/rbac`

- [ ] **Step 5: Write the failing labs tests**

In `internal/labs/service_test.go`:
- add `failProvision error` to `fakeRunner` and make `Provision` return it:

```go
func (f *fakeRunner) Provision(context.Context, *Instance, []byte, string) error {
	if f.provision != nil {
		f.provision()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failProvision
}
```

- extend `fx` and `setup`:

```go
type fx struct {
	s             *Service
	run           *fakeRunner
	clk           *clock
	u, other      *auth.User
	leader, admin *auth.User
	notes         *fakeNotifier
	rates         FixedRates
	plat          *config.Platform
}
```

In `setup`, after `other`:

```go
	leader, _ := store.UpsertUser(ctx, "s3", "leader@crucible.local", "Lee")
	admin, _ := store.UpsertUser(ctx, "s4", "admin@crucible.local", "Ada")
	rates := FixedRates{}
```

build the service with `Estimators: map[string]Estimator{"local": rates}` and return `&fx{s: s, run: run, clk: clk, u: u, other: other, leader: leader, admin: admin, notes: notes, rates: rates, plat: plat}`.

Append to `internal/labs/model_test.go`:

```go
func TestParseRates(t *testing.T) {
	r, err := ParseRates(" paid-heat=0.5, other=2 ")
	if err != nil || r["paid-heat"] != 0.5 || r["other"] != 2 {
		t.Fatalf("%v %v", r, err)
	}
	if r, err := ParseRates(""); err != nil || len(r) != 0 {
		t.Fatalf("empty: %v %v", r, err)
	}
	for _, bad := range []string{"x", "x=-1", "x=abc"} {
		if _, err := ParseRates(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
```

Create `internal/labs/approvals_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

func (f *fx) request(t *testing.T, u *auth.User) *View {
	t.Helper()
	v, err := f.s.Start(context.Background(), u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *fx) waitState(t *testing.T, u *auth.User, id string, want State) *View {
	t.Helper()
	for i := 0; i < 300; i++ {
		if v, err := f.s.Get(context.Background(), u, id); err == nil && v.State == want {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lab %s never reached %s", id, want)
	return nil
}

func TestPaidLabNeedsApprovalAndNobodyApprovesTheirOwn(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5 // first-heat has ttl 1h → $0.50: above auto_approve_usd 0, within tier1 $5
	ml, err := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if err != nil || !ml.NeedsApproval || ml.EstimateUSD != 0.5 {
		t.Fatalf("lobby preview %+v %v", ml, err)
	}
	v := f.request(t, f.u)
	if v.State != PendingApproval || v.Tier != rbac.TierApprover || v.EstimateUSD != 0.5 {
		t.Fatalf("request %+v", v)
	}
	if v.EscalateAt == nil || !v.EscalateAt.Equal(f.clk.Now().Add(4*time.Hour)) {
		t.Fatalf("no schedule: escalate after 4 wall-clock hours, got %v", v.EscalateAt)
	}
	if again := f.request(t, f.u); again.ID != v.ID {
		t.Fatal("asking again returns the pending request")
	}
	ev := f.notes.last(notify.LabPending)
	if ev == nil || strings.Join(ev.To, ",") != "leader@crucible.local" || ev.Team != "forge" || ev.Link != "/approvals" {
		t.Fatalf("pending notification %+v", ev)
	}
	if list, _ := f.s.Approvals(ctx, f.other); len(list) != 0 {
		t.Fatalf("a senior who is not an approver sees nothing: %v", list)
	}
	list, err := f.s.Approvals(ctx, f.leader)
	if err != nil || len(list) != 1 {
		t.Fatalf("leader inbox %v %v", list, err)
	}
	if a := list[0]; a.Requester != "trainee@crucible.local" || a.LabTitle != "First Heat: Your First Lab" ||
		a.TeamSpend.BudgetUSD != 200 || a.TeamSpend.CapUSD != 250 || a.Schedule.Text != "any time" || a.Recent == nil {
		t.Fatalf("approval context %+v", a)
	}
	if _, err := f.s.Decide(ctx, f.u, v.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("self-approval must be forbidden: %v", err)
	}
	st, err := f.s.Decide(ctx, f.leader, v.ID, true, "have fun")
	if err != nil || st != Provisioning {
		t.Fatalf("approve: %v %v", st, err)
	}
	ready := f.waitState(t, f.u, v.ID, Ready)
	if ready.DecidedBy != "leader@crucible.local" || ready.DecisionNote != "have fun" {
		t.Fatalf("decision not shown to the trainee: %+v", ready)
	}
	if ev := f.notes.last(notify.LabApproved); ev == nil || ev.To[0] != "trainee@crucible.local" || ev.Link != "/p/forge/forge-101/m/02-first-lab/lab" {
		t.Fatalf("approved notification %+v", ev)
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, false, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a decided request cannot be decided again: %v", err)
	}
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1`, v.ID).Scan(&action)
	if action != "lab.approve" {
		t.Fatalf("audit action %q", action)
	}
}

func TestRejectWithdrawAndRequestAgain(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	v := f.request(t, f.u)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, false, "not today"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Rejected || got.DecisionNote != "not today" {
		t.Fatalf("rejected view %+v", got)
	}
	if f.notes.last(notify.LabRejected) == nil {
		t.Fatal("the trainee hears about the rejection")
	}
	v2 := f.request(t, f.u)
	if v2.ID == v.ID || v2.State != PendingApproval {
		t.Fatalf("a new request after a rejection: %+v", v2)
	}
	w, err := f.s.End(ctx, f.u, v2.ID)
	if err != nil || w.State != Expired || w.EndReason != "withdrawn" {
		t.Fatalf("withdraw: %+v %v", w, err)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatalf("withdrawn requests leave the inbox: %v", list)
	}
	if v3 := f.request(t, f.u); v3.ID == v2.ID || v3.State != PendingApproval {
		t.Fatalf("request after withdrawing: %+v", v3)
	}
}

func TestLeaderRequestRoutesPastThemselves(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	prog := f.plat.Teams["forge"].Programs["forge-101"]
	prog.Enrolled = append(prog.Enrolled, "leader@crucible.local")
	for _, it := range []string{"how-we-work", "quiz"} {
		_ = f.s.Learn.SetItem(ctx, f.leader.ID, "forge", "forge-101", "01-welcome", it, "complete", 1)
	}
	v := f.request(t, f.leader)
	if v.Tier != rbac.TierAdmin {
		t.Fatalf("the leader is the approver and the leader: route to admin, got %s", v.Tier)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatal("a request never shows in its requester's inbox")
	}
	if list, _ := f.s.Approvals(ctx, f.admin); len(list) != 1 {
		t.Fatal("the admin sees it")
	}
	if ev := f.notes.last(notify.LabPending); ev == nil || strings.Join(ev.To, ",") != "admin@crucible.local" {
		t.Fatalf("notify the admin, not the requester: %+v", ev)
	}
}

func TestReRequestAfterFailedStartSkipsApprovalForAnHour(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	v := f.request(t, f.u)
	f.run.mu.Lock()
	f.run.failProvision = errors.New("docker exploded")
	f.run.mu.Unlock()
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Failed)
	f.run.mu.Lock()
	f.run.failProvision = nil
	f.run.mu.Unlock()
	again := f.request(t, f.u)
	if again.State != Provisioning || again.Tier != rbac.TierAuto {
		t.Fatalf("re-request within an hour of an approved failure needs no approval (spec §14): %+v", again)
	}
	f.waitState(t, f.u, again.ID, Ready)
	if _, err := f.s.End(ctx, f.u, again.ID); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(61 * time.Minute)
	if later := f.request(t, f.u); later.State != PendingApproval {
		t.Fatalf("after an hour approval is needed again: %+v", later)
	}
}

func TestSpendSumsThisMonth(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	now := f.clk.Now() // 2026-10-05 09:00 UTC
	h := func(d float64) *time.Time { v := now.Add(time.Duration(d * float64(time.Hour))); return &v }
	seed := func(id, module, state string, hourly, estimate float64, created time.Time, ready, destroyed, ends *time.Time) {
		_, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, created_at,
			last_activity_at, ready_at, ends_at, destroyed_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
			VALUES ($1, $2, 'forge', 'forge-101', $3, 'abc', 'local', $4, $5, $5, $6, $7, $8, 3600, 1800, 300, 0, $9, $10)`,
			id, f.u.ID, module, state, created, ready, ends, destroyed, hourly, estimate)
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("aaaaaaaaaaa1", "m1", "destroyed", 1.0, 2.0, *h(-3), h(-3), h(-1), h(-1))           // ran 2h at $1 → 2.00
	seed("aaaaaaaaaaa2", "m2", "ready", 0.5, 1.0, *h(-1), h(-1), nil, h(1))                  // 1h so far, 1h to go at $0.50 → 0.50 / 1.00
	seed("aaaaaaaaaaa3", "m3", "provisioning", 3.0, 3.0, now, nil, nil, nil)                 // committed 3.00
	seed("aaaaaaaaaaa4", "m4", "destroyed", 10, 10, *h(-240), h(-240), h(-239), nil)         // September: not this month
	sp, err := f.s.spend(ctx, f.plat, "forge", "")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(sp.SpentUSD-2.5) > 1e-9 || math.Abs(sp.CommittedUSD-6.0) > 1e-9 || sp.BudgetUSD != 200 || sp.CapUSD != 250 {
		t.Fatalf("team spend %+v", sp)
	}
	if prog, _ := f.s.spend(ctx, f.plat, "forge", "forge-101"); prog.BudgetUSD != 0 || math.Abs(prog.SpentUSD-2.5) > 1e-9 {
		t.Fatalf("program spend %+v", prog)
	}
}
```

- [ ] **Step 6: Run them to see them fail**

Run: `go test ./internal/labs/ 2>&1 | tail -6`
Expected: FAIL — `undefined: FixedRates`, `unknown field Estimators`, `undefined: PendingApproval`.

- [ ] **Step 7: Migration**

`internal/db/migrations/00004_lab_requests.sql`:

```sql
-- +goose Up
-- A lab request is a lab_instances row (spec §8.1): pending_approval → provisioning … or rejected / expired.
ALTER TABLE lab_instances
  ADD COLUMN hourly_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
  ADD COLUMN estimate_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,  -- hourly × TTL when requested
  ADD COLUMN tier          TEXT NOT NULL DEFAULT 'auto',         -- auto | approver | leader | admin (current tier)
  ADD COLUMN over_cap      BOOLEAN NOT NULL DEFAULT false,       -- would pass a hard cap: admin only, audited
  ADD COLUMN escalate_at   TIMESTAMPTZ,                          -- pending: when it moves up a tier (or expires)
  ADD COLUMN decided_by    TEXT NOT NULL DEFAULT '',
  ADD COLUMN decided_at    TIMESTAMPTZ,
  ADD COLUMN decision_note TEXT NOT NULL DEFAULT '';
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
CREATE INDEX lab_instances_pending ON lab_instances (escalate_at) WHERE state = 'pending_approval';

-- +goose Down
DROP INDEX lab_instances_pending;
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('provisioning', 'ready', 'destroying');
ALTER TABLE lab_instances DROP COLUMN hourly_usd, DROP COLUMN estimate_usd, DROP COLUMN tier, DROP COLUMN over_cap,
  DROP COLUMN escalate_at, DROP COLUMN decided_by, DROP COLUMN decided_at, DROP COLUMN decision_note;
```

- [ ] **Step 8: Model additions**

`internal/labs/model.go` — add `"fmt"`, `"strconv"`, `"strings"` to imports; extend the state block and `Instance`:

```go
const (
	PendingApproval State = "pending_approval"
	Provisioning    State = "provisioning"
	Ready           State = "ready"
	Destroying      State = "destroying"
	Destroyed       State = "destroyed"
	Failed          State = "failed"
	Rejected        State = "rejected"
	Expired         State = "expired" // unanswered at every tier, or withdrawn by the trainee
)
```

`Instance` gains:

```go
	HourlyUSD, EstimateUSD  float64
	Tier                    string
	OverCap                 bool
	EscalateAt, DecidedAt   *time.Time
	DecidedBy, DecisionNote string
```

and add:

```go
// Estimator prices one hour of a lab for approval routing and budgets (spec §9.1). local labs are free; cluster
// (M4: internal rate card from platform.yaml) and aws (M6: infracost on the module) plug in here per runtime.
type Estimator interface {
	HourlyUSD(ctx context.Context, lab *content.Lab) (float64, error)
}

// FixedRates prices labs by lab id; unlisted labs are free. Production uses it empty for local labs; the local
// e2e check sets CRUCIBLE_DEV_LAB_USD_PER_HOUR so a laptop lab exercises the approval path.
type FixedRates map[string]float64

func (f FixedRates) HourlyUSD(_ context.Context, lab *content.Lab) (float64, error) { return f[lab.ID], nil }

// ParseRates reads "lab-id=0.5,other=1".
func ParseRates(s string) (FixedRates, error) {
	out := FixedRates{}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		id, v, ok := strings.Cut(kv, "=")
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if !ok || err != nil || f < 0 || strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("bad lab rate %q (want lab-id=usd-per-hour)", kv)
		}
		out[strings.TrimSpace(id)] = f
	}
	return out, nil
}
```

- [ ] **Step 9: Service changes**

In `internal/labs/service.go` add imports `"math"`, `"crucible/internal/config"`, `"crucible/internal/rbac"` (notify is already imported).

`Service` gains `Estimators map[string]Estimator // by runtime; a runtime without one cannot be requested`.

`View` gains:

```go
	EstimateUSD  float64    `json:"estimate_usd"`
	Tier         string     `json:"tier"`
	OverCap      bool       `json:"over_cap"`
	EscalateAt   *time.Time `json:"escalate_at,omitempty"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecisionNote string     `json:"decision_note,omitempty"`
```

`ModuleLab` gains (before `Lab`):

```go
	EstimateUSD   float64 `json:"estimate_usd"`
	NeedsApproval bool    `json:"needs_approval"`
	Blocked       string  `json:"blocked,omitempty"` // why a request can't be made now (schedule, kill switch)
```

Replace `instCols` and `scanInst`:

```go
const instCols = `id, user_id, team, training, module, sha, runtime, state, error, created_at, ready_at, ends_at,
	limit_reason, end_reason, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, extended,
	hourly_usd, estimate_usd, tier, over_cap, escalate_at, decided_by, decided_at, decision_note`

func scanInst(row pgx.Row) (*Instance, error) {
	var in Instance
	var ttl, idle, warn, ext int
	err := row.Scan(&in.ID, &in.UserID, &in.Team, &in.Training, &in.Module, &in.SHA, &in.Runtime, &in.State, &in.Error,
		&in.CreatedAt, &in.ReadyAt, &in.EndsAt, &in.LimitReason, &in.EndReason, &in.LastActivityAt, &ttl, &idle, &warn, &ext, &in.Extended,
		&in.HourlyUSD, &in.EstimateUSD, &in.Tier, &in.OverCap, &in.EscalateAt, &in.DecidedBy, &in.DecidedAt, &in.DecisionNote)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	if err != nil {
		return nil, err
	}
	in.TTL, in.IdleTimeout = time.Duration(ttl)*time.Second, time.Duration(idle)*time.Second
	in.IdleWarning, in.MaxExtension = time.Duration(warn)*time.Second, time.Duration(ext)*time.Second
	return &in, nil
}

func collectInst(rows pgx.Rows) ([]*Instance, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Instance, error) { return scanInst(r) })
}
```

In `view`, extend the `v := &View{…}` literal with `EstimateUSD: inst.EstimateUSD, Tier: inst.Tier, OverCap: inst.OverCap, EscalateAt: inst.EscalateAt, DecidedBy: inst.DecidedBy, DecisionNote: inst.DecisionNote`.

Add the quote, insert and re-approval helpers:

```go
// quote is what a request for this lab would cost and who would decide it. The lobby preview and the real request
// use the same function so they never disagree. Tasks 7 and 8 add the schedule, cap and kill-switch checks here.
type quote struct {
	Timing      Timing
	HourlyUSD   float64
	EstimateUSD float64
	Tier        string
	OverCap     bool   // would pass a team or program hard cap (Task 8)
	Blocked     string // why no request can be made right now (Tasks 7, 8); "" = it can
}

func (s *Service) quote(ctx context.Context, p *config.Platform, u *auth.User, team, training, module string, lab *content.Lab) (*quote, error) {
	est := s.Estimators[lab.Runtime]
	if est == nil {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("no cost estimate is available for %s labs", lab.Runtime))
	}
	if p.Settings.CostTiers == nil { // config.Load requires them; guards hand-built states
		return nil, apperr.Wrap(apperr.Unavailable, "cost tiers are not configured")
	}
	hourly, err := est.HourlyUSD(ctx, lab)
	if err != nil {
		return nil, fmt.Errorf("estimating lab cost: %w", err)
	}
	q := &quote{Timing: ResolveTiming(lab, p.Teams[team].Programs[training].LabDefaults), HourlyUSD: hourly}
	q.EstimateUSD = math.Round(hourly*q.Timing.TTL.Hours()*100) / 100
	q.Tier = rbac.Tier(lab.Runtime, q.EstimateUSD, *p.Settings.CostTiers)
	if q.Tier != rbac.TierAuto {
		again, err := s.recentlyApproved(ctx, u.ID, team, training, module)
		if err != nil {
			return nil, err
		}
		if again {
			q.Tier = rbac.TierAuto // spec §14: re-request after a failed start needs no re-approval within 1 h
		} else {
			q.Tier = rbac.Checker{P: p}.Route(q.Tier, team, training, u.Email)
		}
	}
	return q, nil
}

func (s *Service) recentlyApproved(ctx context.Context, userID int64, team, training, module string) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_instances WHERE user_id = $1 AND team = $2 AND training = $3
		AND module = $4 AND state = 'failed' AND decided_by <> '' AND decided_at > $5)`,
		userID, team, training, module, s.Now().Add(-time.Hour)).Scan(&ok)
	return ok, err
}

func (s *Service) insert(ctx context.Context, in *Instance) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s,
		hourly_usd, estimate_usd, tier, over_cap, escalate_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		in.ID, in.UserID, in.Team, in.Training, in.Module, in.SHA, in.Runtime, in.State, in.CreatedAt,
		int(in.TTL.Seconds()), int(in.IdleTimeout.Seconds()), int(in.IdleWarning.Seconds()), int(in.MaxExtension.Seconds()),
		in.HourlyUSD, in.EstimateUSD, in.Tier, in.OverCap, in.EscalateAt)
	return err
}
```

Replace `Start`:

```go
// Start requests a lab: free labs within auto_approve_usd start at once; others wait for an approver (spec §9.1).
func (s *Service) Start(ctx context.Context, u *auth.User, team, training, module string) (*View, error) {
	st, t, sha, err := s.Learn.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	m, err := s.Learn.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, err
	}
	if m.Lab == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this module has no lab")
	}
	r := s.Runners[m.Lab.Runtime]
	if r == nil {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs are not available yet", m.Lab.Runtime))
	}
	if inst, err := s.active(ctx, u.ID, team, training, module); err != nil {
		return nil, err
	} else if inst != nil && (inst.State == PendingApproval || inst.State == Provisioning || inst.State == Ready) {
		return s.view(ctx, inst)
	}
	q, err := s.quote(ctx, st.Platform, u, team, training, module, m.Lab)
	if err != nil {
		return nil, err
	}
	if q.Blocked != "" {
		return nil, apperr.Wrap(apperr.Conflict, q.Blocked)
	}
	now := s.Now()
	inst := &Instance{ID: newLabID(), UserID: u.ID, Team: team, Training: training, Module: module, SHA: sha,
		Runtime: m.Lab.Runtime, State: Provisioning, CreatedAt: now, LastActivityAt: now,
		TTL: q.Timing.TTL, IdleTimeout: q.Timing.IdleTimeout, IdleWarning: q.Timing.IdleWarning, MaxExtension: q.Timing.MaxExtension,
		HourlyUSD: q.HourlyUSD, EstimateUSD: q.EstimateUSD, Tier: q.Tier, OverCap: q.OverCap}
	if q.Tier == rbac.TierAuto {
		if err := r.Available(inst); err != nil {
			return nil, err
		}
	} else {
		inst.State = PendingApproval
		at := st.Platform.ProgramSchedule(team, training).AddOpen(now, st.Platform.Settings.Escalation())
		inst.EscalateAt = &at
	}
	err = s.insert(ctx, inst)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // a concurrent Start won the race
		existing, err := s.active(ctx, u.ID, team, training, module)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, apperr.Wrap(apperr.Conflict, "the lab is changing state; try again")
		}
		return s.view(ctx, existing)
	}
	if err != nil {
		return nil, err
	}
	s.event(ctx, inst.ID, "requested", fmt.Sprintf("%s, estimate $%.2f, tier %s", inst.Runtime, inst.EstimateUSD, inst.Tier))
	if inst.State == Provisioning {
		s.event(ctx, inst.ID, "approved", "auto")
		go s.provision(context.WithoutCancel(ctx), inst, m.Lab) // ponytail: a goroutine, not a River job, until provisioning gets long (M4)
	} else {
		s.notifyRequest(ctx, st.Platform, inst, u.Email, notify.LabPending)
	}
	return s.view(ctx, inst)
}
```

In `ModuleLab`, capture the state (`st, t, _, err := s.Learn.Program(u, team, training)`) and, after the runtime readiness block, add:

```go
	if q, err := s.quote(ctx, st.Platform, u, team, training, module, m.Lab); err != nil {
		out.Blocked = strings.TrimSuffix(err.Error(), ": "+apperr.Unavailable.Error())
	} else {
		out.EstimateUSD, out.NeedsApproval, out.Blocked = q.EstimateUSD, q.Tier != rbac.TierAuto, q.Blocked
	}
```

In `End`, withdraw a pending request instead of destroying:

```go
func (s *Service) End(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State == PendingApproval {
		if _, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'expired', end_reason = 'withdrawn', destroyed_at = $2
			WHERE id = $1 AND state = 'pending_approval'`, inst.ID, s.Now()); err != nil {
			return nil, err
		}
		s.event(ctx, inst.ID, "withdrawn", "")
		return s.Get(ctx, u, labID)
	}
	s.destroy(ctx, inst, "user")
	return s.Get(ctx, u, labID)
}
```

- [ ] **Step 10: The approvals file**

Create `internal/labs/approvals.go`:

```go
package labs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// Spend is month-to-date lab spend for a team or one program, from per-lab estimates (actual AWS costs arrive in M6).
type Spend struct {
	SpentUSD     float64 `json:"spent_usd"`     // hourly estimate × time each lab has run this month
	CommittedUSD float64 `json:"committed_usd"` // spent + the rest of every lab still starting or running, to its end
	BudgetUSD    float64 `json:"budget_usd"`    // 0 = no budget
	CapUSD       float64 `json:"cap_usd"`       // 0 = no hard cap
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// spend sums this calendar month (UTC) for a team, or one program when training != "".
// ponytail: a lab counts in the month it was requested; one running across midnight on the 1st stays in the old month.
func (s *Service) spend(ctx context.Context, p *config.Platform, team, training string) (Spend, error) {
	now := s.Now()
	var sp Spend
	err := s.DB.QueryRow(ctx, `SELECT
		coalesce(sum(CASE WHEN ready_at IS NULL THEN 0
			ELSE hourly_usd * extract(epoch FROM least(coalesce(destroyed_at, $3), $3) - ready_at)::float8 / 3600 END), 0),
		coalesce(sum(CASE
			WHEN state = 'provisioning' THEN estimate_usd
			WHEN state = 'ready' THEN hourly_usd * extract(epoch FROM greatest(ends_at, $3) - ready_at)::float8 / 3600
			WHEN ready_at IS NULL THEN 0
			ELSE hourly_usd * extract(epoch FROM coalesce(destroyed_at, $3) - ready_at)::float8 / 3600 END), 0)
		FROM lab_instances WHERE team = $1 AND ($2 = '' OR training = $2) AND created_at >= $4`,
		team, training, now, monthStart(now)).Scan(&sp.SpentUSD, &sp.CommittedUSD)
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

type RecentLab struct {
	Module      string    `json:"module"`
	State       State     `json:"state"`
	EndReason   string    `json:"end_reason,omitempty"`
	EstimateUSD float64   `json:"estimate_usd"`
	CreatedAt   time.Time `json:"created_at"`
}

type ScheduleInfo struct {
	Name     string     `json:"name"` // "" = any time
	Text     string     `json:"text"`
	Open     bool       `json:"open"`
	ClosesAt *time.Time `json:"closes_at,omitempty"`
	NextOpen *time.Time `json:"next_open,omitempty"`
}

// Approval is one pending request with what an approver needs to decide (spec §9.1.3).
type Approval struct {
	ID            string       `json:"id"`
	Requester     string       `json:"requester"`
	RequesterName string       `json:"requester_name"`
	Team          string       `json:"team"`
	Training      string       `json:"training"`
	Module        string       `json:"module"`
	LabTitle      string       `json:"lab_title"`
	Runtime       string       `json:"runtime"`
	HourlyUSD     float64      `json:"hourly_usd"`
	EstimateUSD   float64      `json:"estimate_usd"`
	TTLS          int          `json:"ttl_s"`
	Tier          string       `json:"tier"`
	OverCap       bool         `json:"over_cap"`
	RequestedAt   time.Time    `json:"requested_at"`
	EscalateAt    *time.Time   `json:"escalate_at,omitempty"`
	TeamSpend     Spend        `json:"team_spend"`
	ProgramSpend  Spend        `json:"program_spend"`
	Recent        []RecentLab  `json:"recent"`
	Schedule      ScheduleInfo `json:"schedule"`
}

func scheduleInfo(p *config.Platform, team, training string, now time.Time) ScheduleInfo {
	sc := p.ProgramSchedule(team, training)
	info := ScheduleInfo{Text: sc.String(), Open: sc.Open(now)}
	if t := p.Teams[team]; t != nil && t.Programs[training] != nil {
		info.Name = t.Programs[training].Schedule
	}
	if end := sc.End(now); !end.IsZero() {
		info.ClosesAt = &end
	}
	if !info.Open {
		if n := sc.NextOpen(now); !n.IsZero() {
			info.NextOpen = &n
		}
	}
	return info
}

func (s *Service) requester(ctx context.Context, userID int64) (email, name string, err error) {
	err = s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name)
	return
}

func labLink(inst *Instance) string {
	return fmt.Sprintf("/p/%s/%s/m/%s/lab", inst.Team, inst.Training, inst.Module)
}

// notifyRequest tells the people at the request's current tier (and the team channel) that it waits for them.
func (s *Service) notifyRequest(ctx context.Context, p *config.Platform, inst *Instance, requester string, kind notify.Kind) {
	verb := "is waiting for approval"
	if kind == notify.LabEscalated {
		verb = "was escalated: nobody answered in time"
	}
	s.notify(ctx, notify.Event{Kind: kind, To: rbac.Checker{P: p}.TierApprovers(inst.Tier, inst.Team, inst.Training, requester),
		Team:    inst.Team,
		Subject: fmt.Sprintf("Lab request from %s (%s, est. $%.2f)", requester, inst.Training, inst.EstimateUSD),
		Text: fmt.Sprintf("%s requested the %s lab in %s/%s, estimated at $%.2f. It %s.",
			requester, inst.Module, inst.Team, inst.Training, inst.EstimateUSD, verb),
		Link: "/approvals"})
}

func (s *Service) platform() (*gitsync.State, error) {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Approvals lists the pending requests the user may decide, oldest first.
func (s *Service) Approvals(ctx context.Context, u *auth.User) ([]Approval, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE state = 'pending_approval' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	pending, err := collectInst(rows)
	if err != nil {
		return nil, err
	}
	c := rbac.Checker{P: st.Platform}
	out := []Approval{}
	for _, inst := range pending {
		email, name, err := s.requester(ctx, inst.UserID)
		if err != nil {
			return nil, err
		}
		if !c.MayApprove(u.Email, email, inst.Team, inst.Training, inst.EstimateUSD, inst.OverCap) {
			continue
		}
		a, err := s.approval(ctx, st, inst, email, name)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

func (s *Service) approval(ctx context.Context, st *gitsync.State, inst *Instance, email, name string) (*Approval, error) {
	a := &Approval{ID: inst.ID, Requester: email, RequesterName: name, Team: inst.Team, Training: inst.Training,
		Module: inst.Module, LabTitle: inst.Module, Runtime: inst.Runtime, HourlyUSD: inst.HourlyUSD, EstimateUSD: inst.EstimateUSD,
		TTLS: int(inst.TTL.Seconds()), Tier: inst.Tier, OverCap: inst.OverCap, RequestedAt: inst.CreatedAt, EscalateAt: inst.EscalateAt,
		Schedule: scheduleInfo(st.Platform, inst.Team, inst.Training, s.Now())}
	if t := st.Training(inst.Training, inst.SHA); t != nil {
		if m := t.Module(inst.Module); m != nil {
			a.LabTitle = m.Title
		}
	}
	var err error
	if a.TeamSpend, err = s.spend(ctx, st.Platform, inst.Team, ""); err != nil {
		return nil, err
	}
	if a.ProgramSpend, err = s.spend(ctx, st.Platform, inst.Team, inst.Training); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT module, state, end_reason, estimate_usd, created_at FROM lab_instances
		WHERE user_id = $1 AND id <> $2 ORDER BY created_at DESC LIMIT 5`, inst.UserID, inst.ID)
	if err != nil {
		return nil, err
	}
	if a.Recent, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RecentLab]); err != nil {
		return nil, err
	}
	if a.Recent == nil {
		a.Recent = []RecentLab{}
	}
	return a, nil
}

// Decide approves (provisioning starts at once) or rejects a pending request.
func (s *Service) Decide(ctx context.Context, u *auth.User, labID string, approve bool, note string) (State, error) {
	st, err := s.platform()
	if err != nil {
		return "", err
	}
	p := st.Platform
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
	if err != nil {
		return "", err
	}
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		return "", err
	}
	if !(rbac.Checker{P: p}).MayApprove(u.Email, email, inst.Team, inst.Training, inst.EstimateUSD, inst.OverCap) {
		return "", apperr.Wrap(apperr.Forbidden, "you can't decide this request")
	}
	if inst.State != PendingApproval {
		return "", apperr.Wrap(apperr.Conflict, "this request was already decided")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	note = strings.TrimSpace(cleanText(note))
	next := Rejected
	var lab *content.Lab
	if approve {
		// Tasks 7 and 8 add: schedule window open, kill switch off, cap re-check.
		if lab, _, err = s.labContent(inst); err != nil {
			return "", err
		}
		next = Provisioning
	}
	now := s.Now()
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = $2, decided_by = $3, decided_at = $4, decision_note = $5,
		destroyed_at = CASE WHEN $2 = 'rejected' THEN $4::timestamptz END
		WHERE id = $1 AND state = 'pending_approval'`, inst.ID, string(next), strings.ToLower(u.Email), now, note)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", apperr.Wrap(apperr.Conflict, "this request was already decided")
	}
	action := "lab.reject"
	if approve {
		action = "lab.approve"
		if inst.OverCap {
			action = "lab.budget_override"
		}
	}
	if err := audit.Log(ctx, s.DB, u.Email, action, inst.ID, map[string]any{"requester": email, "team": inst.Team,
		"training": inst.Training, "module": inst.Module, "estimate_usd": inst.EstimateUSD, "note": note}, ""); err != nil {
		s.Log.Error("audit log failed", "action", action, "lab", inst.ID, "err", err)
	}
	verb, kind := "rejected", notify.LabRejected
	if approve {
		verb, kind = "approved", notify.LabApproved
	}
	s.event(ctx, inst.ID, verb, strings.ToLower(u.Email)+": "+note)
	text := fmt.Sprintf("%s %s your request for the %s lab (%s/%s).", u.Email, verb, inst.Module, inst.Team, inst.Training)
	if note != "" {
		text += " Note: " + note
	}
	s.notify(ctx, notify.Event{Kind: kind, To: []string{email}, Subject: fmt.Sprintf("Your %s lab request was %s", inst.Training, verb),
		Text: text, Link: labLink(inst)})
	if approve {
		inst.State = Provisioning
		go s.provision(context.WithoutCancel(ctx), inst, lab)
	}
	return next, nil
}
```

`internal/labs/http.go`, inside `Routes`:

```go
	r.Get("/api/approvals", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Approvals(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approve bool   `json:"approve"`
			Note    string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		st, err := s.Decide(r.Context(), user(r), p(r, "id"), body.Approve, body.Note)
		reply(w, map[string]State{"state": st}, err)
	})
```

`cmd/crucible-api/main.go`, before `labSvc := …`:

```go
	rates, err := labs.ParseRates(os.Getenv("CRUCIBLE_DEV_LAB_USD_PER_HOUR"))
	if err != nil {
		return fmt.Errorf("CRUCIBLE_DEV_LAB_USD_PER_HOUR: %w", err)
	}
	if len(rates) > 0 {
		slog.Warn("CRUCIBLE_DEV_LAB_USD_PER_HOUR is set: these local labs are priced for testing approvals", "rates", rates)
	}
```

and add `Estimators: map[string]labs.Estimator{"local": rates}` to the `labs.Service` literal.

- [ ] **Step 11: Run the labs tests and the whole suite**

Run: `go test -race ./internal/labs/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'; go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'; gofmt -l .; go vet ./...`
Expected: `ok  	crucible/internal/labs`; nothing else printed.

- [ ] **Step 12: Commit**

```bash
git add internal/db/migrations/00004_lab_requests.sql internal/rbac internal/labs cmd/crucible-api/main.go
git commit -m "feat(labs): cost-estimated lab requests with tiered approvals and an approvals inbox

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: Escalation after N business hours, then expiry

The sweep moves an unanswered request one tier up when `escalate_at` passes (approver → leader → admin, skipping tiers staffed only by the requester), notifies the new tier, and expires the request when even the admin tier did not answer. Time counts only inside the program's schedule.

**Files:**
- Modify: `internal/labs/approvals.go` (add `escalate`)
- Modify: `internal/labs/service.go` (`Sweep` query and loop)
- Modify: `internal/labs/approvals_test.go`, `internal/labs/service_test.go` (clock `Set`)

**Interfaces:**
- Consumes: `rbac.NextTier`, `Checker.Route`, `notifyRequest`, `requester`, `labLink`, `Schedule.AddOpen`, `Settings.Escalation()` (Tasks 2, 5).
- Produces: `(s *Service) escalate(ctx context.Context, inst *Instance)`; lab events `escalated` (detail `"approver → leader"`) and `expired`; `end_reason = "unanswered"`.

- [ ] **Step 1: Write the failing tests**

Add a setter to the test clock in `internal/labs/service_test.go` (next to `Add`):

```go
func (c *clock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
```

Append to `internal/labs/approvals_test.go`:

```go
func TestEscalationClimbsTiersThenExpires(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	f.plat.Teams["forge"].Programs["forge-101"].Roles.Approvers = []string{"senior@crucible.local"}
	v := f.request(t, f.u)
	tier := func() string { got, _ := f.s.Get(ctx, f.u, v.ID); return got.Tier }
	f.clk.Add(4*time.Hour - time.Minute)
	f.s.Sweep(ctx)
	if tier() != rbac.TierApprover {
		t.Fatal("not due yet")
	}
	f.clk.Add(time.Minute)
	f.s.Sweep(ctx)
	if tier() != rbac.TierLeader {
		t.Fatalf("after 4h: leader, got %s", tier())
	}
	if ev := f.notes.last(notify.LabEscalated); ev == nil || strings.Join(ev.To, ",") != "leader@crucible.local" {
		t.Fatalf("leader notified: %+v", ev)
	}
	if list, _ := f.s.Approvals(ctx, f.other); len(list) != 1 {
		t.Fatal("the original approver can still decide after escalation")
	}
	f.clk.Add(4 * time.Hour)
	f.s.Sweep(ctx)
	if tier() != rbac.TierAdmin {
		t.Fatalf("after 8h: admin, got %s", tier())
	}
	if ev := f.notes.last(notify.LabEscalated); ev == nil || strings.Join(ev.To, ",") != "admin@crucible.local" {
		t.Fatalf("admin notified: %+v", ev)
	}
	f.clk.Add(4 * time.Hour)
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, v.ID)
	if got.State != Expired || got.EndReason != "unanswered" {
		t.Fatalf("after 12h unanswered: %+v", got)
	}
	if ev := f.notes.last(notify.LabRejected); ev == nil || ev.To[0] != "trainee@crucible.local" {
		t.Fatalf("trainee told it expired: %+v", ev)
	}
}

func TestEscalationCountsBusinessHoursOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 0.5
	f.plat.Teams["forge"].Programs["forge-101"].Schedule = "business-hours" // Mon–Fri 08:00–19:00 Europe/Bucharest (UTC+3 in early October)
	f.clk.Set(time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC))                // Friday 17:00 in Bucharest
	v := f.request(t, f.u)
	want := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC) // Monday 10:00 in Bucharest: 2h Friday + 2h Monday
	if v.EscalateAt == nil || !v.EscalateAt.Equal(want) {
		t.Fatalf("escalate_at %v, want %v", v.EscalateAt, want)
	}
	f.clk.Set(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)) // Saturday
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.Tier != rbac.TierApprover {
		t.Fatalf("no escalation over the weekend, got %s", got.Tier)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run Escalation 2>&1 | tail -6`
Expected: FAIL — `after 4h: leader, got approver` (the sweep ignores pending requests).

- [ ] **Step 3: Implement**

Append to `internal/labs/approvals.go`:

```go
// escalate moves an unanswered request one tier up (approver → leader → admin, skipping tiers with nobody but the
// requester) or, after the admin tier also timed out, expires it (spec §9.1.5). Guarded by the current tier so a
// concurrent decision or a second sweep never applies it twice.
func (s *Service) escalate(ctx context.Context, inst *Instance) {
	st, err := s.platform()
	if err != nil {
		return
	}
	p := st.Platform
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		s.Log.Error("escalation: requester lookup failed", "lab", inst.ID, "err", err)
		return
	}
	now := s.Now()
	next := rbac.NextTier(inst.Tier)
	if next != "" {
		next = rbac.Checker{P: p}.Route(next, inst.Team, inst.Training, email)
	}
	if next == "" {
		tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'expired', end_reason = 'unanswered', destroyed_at = $2
			WHERE id = $1 AND state = 'pending_approval' AND tier = $3`, inst.ID, now, inst.Tier)
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		s.event(ctx, inst.ID, "expired", "nobody answered at any tier")
		s.notify(ctx, notify.Event{Kind: notify.LabRejected, To: []string{email},
			Subject: fmt.Sprintf("Your %s lab request expired", inst.Training),
			Text:    fmt.Sprintf("Nobody answered your request for the %s lab (%s/%s) in time. You can request it again.", inst.Module, inst.Team, inst.Training),
			Link:    labLink(inst)})
		return
	}
	at := p.ProgramSchedule(inst.Team, inst.Training).AddOpen(now, p.Settings.Escalation())
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET tier = $2, escalate_at = $3
		WHERE id = $1 AND state = 'pending_approval' AND tier = $4`, inst.ID, next, at, inst.Tier)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	s.event(ctx, inst.ID, "escalated", inst.Tier+" → "+next)
	inst.Tier = next
	s.notifyRequest(ctx, p, inst, email, notify.LabEscalated)
}
```

In `internal/labs/service.go` `Sweep`, extend the query's `WHERE` with a fourth alternative:

```go
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE (state = 'ready' AND (ends_at <= $1 OR last_activity_at + idle_timeout_s * interval '1 second' <= $1))
		   OR (state = 'provisioning' AND created_at < $1 - interval '15 minutes')
		   OR (state = 'destroying' AND destroyed_at < $1 - interval '10 minutes')
		   OR (state = 'pending_approval' AND escalate_at <= $1)`, now)
```

and make the first case of the loop's `switch`:

```go
		case inst.State == PendingApproval:
			s.escalate(ctx, inst)
			continue
```

- [ ] **Step 4: Run the labs tests**

Run: `go test -race ./internal/labs/`
Expected: `ok  	crucible/internal/labs`

- [ ] **Step 5: Commit**

```bash
git add internal/labs
git commit -m "feat(labs): escalate unanswered lab requests after N business hours, then expire them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Schedule windows on labs: request gate, timer limit, capped extensions, close at window end

**Files:**
- Modify: `internal/labs/service.go` (`quote`, `provision`, `view`, `Extend`, `Sweep` end reason; new `scheduleLimit`)
- Modify: `internal/labs/approvals.go` (`Decide` window check)
- Create: `internal/labs/schedule_test.go`

**Interfaces:**
- Consumes: `Platform.ProgramSchedule`, `Schedule.Open/End/NextOpen/String/Location` (Task 2); `quote.Blocked` (Task 5); `rbac.Tier` (Task 5).
- Produces: `limit_reason = "schedule"` on labs whose effective end is the window close; `end_reason = "schedule"` when the sweep destroys them; `(s *Service) scheduleLimit(inst *Instance, now time.Time) Limit`. Message formats (asserted by tests and shown verbatim in the lobby): `"Labs for this program run <Schedule.String()>. Next window opens Mon 08:00."`, `"The program's schedule window is closed; approve it when it opens."`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/schedule_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
)

// Europe/Bucharest is UTC+3 in early October 2026; business-hours is Mon–Fri 08:00–19:00 there (05:00–16:00 UTC).
func onSchedule(f *fx) { f.plat.Teams["forge"].Programs["forge-101"].Schedule = "business-hours" }

func TestScheduleGatesRequestsAndApprovals(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)) // Saturday 10:00 local
	_, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "Next window opens Mon 08:00") {
		t.Fatalf("request outside the window: %v", err)
	}
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab"); !strings.Contains(ml.Blocked, "Labs for this program run") {
		t.Fatalf("lobby must say why: %+v", ml)
	}
	f.rates["first-heat"] = 0.5
	f.clk.Set(time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)) // Friday 18:00 local: open
	v := f.request(t, f.u)
	f.clk.Set(time.Date(2026, 10, 9, 16, 30, 0, 0, time.UTC)) // Friday 19:30 local: closed
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("approving outside the window must wait for it to open: %v", err)
	}
}

func TestLabEndsAtScheduleClose(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC)) // Wednesday 18:30 local; TTL 1h would end 19:30
	v := f.start(t)
	if v.LimitReason != "schedule" || !v.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) || v.CanExtend {
		t.Fatalf("ends at window close, no extension: %+v", v)
	}
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("extend past the window: %v", err)
	}
	f.clk.Add(31 * time.Minute)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Destroyed || got.EndReason != "schedule" {
		t.Fatalf("destroyed at window end: %+v", got)
	}
}

func TestExtensionIsCappedByTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.clk.Set(time.Date(2026, 10, 7, 14, 45, 0, 0, time.UTC)) // 17:45 local: TTL ends 18:45, window 19:00
	v := f.start(t)
	if v.LimitReason != "ttl" || !v.CanExtend {
		t.Fatalf("%+v", v)
	}
	got, err := f.s.Extend(ctx, f.u, v.ID) // +30m would be 19:15: capped at 19:00
	if err != nil || got.LimitReason != "schedule" || !got.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("extension capped: %+v %v", got, err)
	}
}

func TestExtensionThatRaisesTheTierIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.rates["first-heat"] = 4.5 // $4.50 for 1h: tier 1. +30m = $6.75: tier 2.
	v := f.request(t, f.u)
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "new approval") {
		t.Fatalf("tier-raising extension: %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'Schedule|Extension' 2>&1 | tail -8`
Expected: FAIL — the Saturday request is accepted; `LimitReason` is `ttl`.

- [ ] **Step 3: Implement**

`internal/labs/service.go`. Add the helper:

```go
// scheduleLimit is the end of the program's open window (spec §8.6). Zero when the program runs any time; now
// when the window is already closed (a lab approved just before close must not run on overnight).
func (s *Service) scheduleLimit(inst *Instance, now time.Time) Limit {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return Limit{}
	}
	sc := st.Platform.ProgramSchedule(inst.Team, inst.Training)
	if sc != nil && !sc.Open(now) {
		return Limit{At: now, Reason: "schedule"}
	}
	return Limit{At: sc.End(now), Reason: "schedule"}
}
```

In `quote`, after computing `q.Tier` (end of the function, before `return q, nil`):

```go
	if sc, now := p.ProgramSchedule(team, training), s.Now(); !sc.Open(now) {
		q.Blocked = "Labs for this program run " + sc.String() + "."
		if n := sc.NextOpen(now); !n.IsZero() {
			q.Blocked += " Next window opens " + n.In(sc.Location()).Format("Mon 15:04") + "."
		}
	}
```

In `provision`, replace the end computation:

```go
	now := s.Now()
	end := EffectiveEnd(Limit{At: now.Add(inst.TTL), Reason: "ttl"}, s.scheduleLimit(inst, now))
```

In `view`, the ready block becomes:

```go
	if inst.State == Ready {
		dl := inst.LastActivityAt.Add(inst.IdleTimeout)
		v.IdleDeadline = &dl
		v.CanExtend = !inst.Extended && inst.MaxExtension > 0 && inst.LimitReason != "schedule"
	}
```

Replace `Extend`:

```go
func (s *Service) Extend(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State != Ready || inst.Extended || inst.MaxExtension == 0 || inst.EndsAt == nil {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	end, reason := inst.EndsAt.Add(inst.MaxExtension), "ttl"
	if lim := s.scheduleLimit(inst, s.Now()); !lim.At.IsZero() && lim.At.Before(end) {
		end, reason = lim.At, lim.Reason
	}
	if !end.After(*inst.EndsAt) {
		return nil, apperr.Wrap(apperr.Conflict, "the schedule window closes first; this lab can't be extended")
	}
	// ponytail: spec §8.6 "Extension pending" (send the extension back through approval) ships with real cloud costs
	// in M6; until then an extension that would lift the estimate into a higher tier is refused.
	if st := s.Learn.State(); inst.HourlyUSD > 0 && st != nil && st.Platform != nil && st.Platform.Settings.CostTiers != nil {
		tiers := *st.Platform.Settings.CostTiers
		more := inst.EstimateUSD + inst.HourlyUSD*end.Sub(*inst.EndsAt).Hours()
		if rbac.Tier(inst.Runtime, more, tiers) != rbac.Tier(inst.Runtime, inst.EstimateUSD, tiers) {
			return nil, apperr.Wrap(apperr.Conflict, "this extension would need a new approval; end the lab and request it again, or ask your approver")
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET ends_at = $2, limit_reason = $3, extended = true, last_activity_at = $4
		WHERE id = $1 AND NOT extended AND state = 'ready'`, inst.ID, end, reason, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	s.event(ctx, inst.ID, "extended", end.Sub(*inst.EndsAt).String())
	return s.Get(ctx, u, labID)
}
```

In `Sweep`, the end-time case uses the recorded limit:

```go
		case inst.EndsAt != nil && !inst.EndsAt.After(now):
			reason = inst.LimitReason // ttl or schedule
			if reason == "" {
				reason = "ttl"
			}
```

`internal/labs/approvals.go`, in `Decide`, replace the `// Tasks 7 and 8 add:` comment line with:

```go
		if sc := p.ProgramSchedule(inst.Team, inst.Training); !sc.Open(s.Now()) {
			return "", apperr.Wrap(apperr.Conflict, "The program's schedule window is closed; approve it when it opens.")
		}
		// Task 8 adds: kill switch off, cap re-check.
```

- [ ] **Step 4: Run the labs tests**

Run: `go test -race ./internal/labs/`
Expected: `ok  	crucible/internal/labs` (the existing `TestSweepExtendAndOwnership` still passes: forge-101 has no schedule).

- [ ] **Step 5: Commit**

```bash
git add internal/labs
git commit -m "feat(labs): schedule windows gate requests, end labs at window close and cap extensions

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Budgets, hard caps with audited admin override, 80%/100% alerts, and the kill switch

**Files:**
- Create: `internal/db/migrations/00005_finops.sql`
- Create: `internal/labs/finops.go`, `internal/labs/finops_test.go`
- Modify: `internal/labs/service.go` (`quote`, `Sweep` start)
- Modify: `internal/labs/approvals.go` (`Decide`)
- Modify: `internal/labs/jobs.go` (`BudgetArgs`, `BudgetWorker`)
- Modify: `internal/labs/http.go` (kill switch routes)
- Modify: `cmd/crucible-api/main.go` (worker + periodic job)

**Interfaces:**
- Consumes: `spend`, `quote`, `Decide`, `audit.Log`, `notify` (Tasks 3–5).
- Produces:
  - `labs.KillSwitch{Enabled bool; ChangedBy string; ChangedAt *time.Time}` (json `enabled, changed_by?, changed_at?`)
  - `(s *Service) KillSwitch(ctx) (KillSwitch, error)`; `(s *Service) SetKillSwitch(ctx, u *auth.User, enabled bool) (KillSwitch, error)`
  - `(s *Service) overCap(ctx, p *config.Platform, team, training string, estimateUSD float64) (bool, error)`; `(s *Service) CheckBudgets(ctx) error`; `alertLevels(sp Spend) []int`
  - `labs.BudgetArgs` (kind `budget_check`), `labs.BudgetWorker{S *Service}`
  - HTTP: `GET /api/admin/kill-switch` → `KillSwitch`; `POST /api/admin/kill-switch` body `{"enabled": true}` → `KillSwitch` (admin only)
  - Lobby message when paused: `"Labs are paused by an admin."`; `end_reason = "kill_switch"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/finops_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// spent records a finished lab this month that cost usd (1 hour at usd/h) for the trainee in module.
func (f *fx) spent(t *testing.T, id, training string, usd float64) {
	t.Helper()
	ready := f.clk.Now().Add(-2 * time.Hour)
	_, err := f.s.DB.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ready_at, destroyed_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, hourly_usd, estimate_usd)
		VALUES ($1, $2, 'forge', $3, 'old', 'abc', 'local', 'destroyed', $4, $4, $4, $5, 3600, 1800, 300, 0, $6, $6)`,
		id, f.u.ID, training, ready, ready.Add(time.Hour), usd)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHardCapRoutesToAdminAndTheOverrideIsAudited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 249) // team cap is $250
	f.rates["first-heat"] = 2
	v := f.request(t, f.u)
	if v.Tier != rbac.TierAdmin || !v.OverCap {
		t.Fatalf("$249 + $2 > $250: admin only, got %+v", v)
	}
	if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
		t.Fatal("the leader cannot approve past the cap")
	}
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader override: %v", err)
	}
	if _, err := f.s.Decide(ctx, f.admin, v.ID, true, "training week"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, f.u, v.ID, Ready)
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1`, v.ID).Scan(&action)
	if action != "lab.budget_override" {
		t.Fatalf("audited as %q", action)
	}
}

func TestFreeLabNeverBlockedByCap(t *testing.T) {
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 300) // already over the cap
	v := f.request(t, f.u)
	if v.State != Provisioning || v.OverCap {
		t.Fatalf("a $0 lab costs nothing: %+v", v)
	}
	f.waitState(t, f.u, v.ID, Ready)
}

func TestProgramCapAndApprovalTimeRecheck(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.plat.Teams["forge"].Programs["forge-101"].BudgetUSDMonth = 10
	f.rates["first-heat"] = 2
	v := f.request(t, f.u) // $0 spent: within the $10 program cap
	if v.OverCap || v.Tier != rbac.TierApprover {
		t.Fatalf("%+v", v)
	}
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 9) // meanwhile the program spent $9
	if _, err := f.s.Decide(ctx, f.leader, v.ID, true, ""); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("approval re-checks the cap: %v", err)
	}
	if got, _ := f.s.Get(ctx, f.u, v.ID); !got.OverCap || got.Tier != rbac.TierAdmin {
		t.Fatalf("passed to an admin: %+v", got)
	}
}

func TestBudgetAlertsOncePerLevelPerMonth(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.spent(t, "bbbbbbbbbbb1", "forge-101", 170) // 85% of the $200 team budget
	if err := f.s.CheckBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	ev := f.notes.last(notify.BudgetAlert)
	if ev == nil || !slices.Contains(ev.To, "leader@crucible.local") || !slices.Contains(ev.To, "admin@crucible.local") || ev.Team != "forge" {
		t.Fatalf("80%% alert %+v", ev)
	}
	n := len(f.notes.events)
	_ = f.s.CheckBudgets(ctx)
	if len(f.notes.events) != n {
		t.Fatal("an alert is sent once per level per month")
	}
	f.spent(t, "bbbbbbbbbbb2", "forge-101", 90) // $260 ≥ $250 cap
	_ = f.s.CheckBudgets(ctx)
	if ev := f.notes.last(notify.BudgetAlert); ev == nil || !strings.Contains(ev.Subject, "hard cap") {
		t.Fatalf("100%% alert %+v", ev)
	}
}

func TestKillSwitch(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	if _, err := f.s.SetKillSwitch(ctx, f.u, true); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only admins: %v", err)
	}
	ks, err := f.s.SetKillSwitch(ctx, f.admin, true)
	if err != nil || !ks.Enabled || ks.ChangedBy != "admin@crucible.local" {
		t.Fatalf("%+v %v", ks, err)
	}
	got := f.waitState(t, f.u, v.ID, Destroyed)
	if got.EndReason != "kill_switch" {
		t.Fatalf("end reason %q", got.EndReason)
	}
	if _, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("requests blocked while paused: %v", err)
	}
	if ml, _ := f.s.ModuleLab(ctx, f.u, "forge", "forge-101", "02-first-lab"); ml.Blocked != "Labs are paused by an admin." {
		t.Fatalf("lobby: %+v", ml)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, false); err != nil {
		t.Fatal(err)
	}
	if n := f.start(t); n.State != Ready {
		t.Fatal("labs run again after re-enabling")
	}
	var actions []string
	rows, _ := f.s.DB.Query(ctx, `SELECT action FROM audit_log ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	if strings.Join(actions, ",") != "kill_switch.on,kill_switch.off" {
		t.Fatalf("audit %v", actions)
	}
}

func TestKillSwitchStopsProvisioningLab(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	release := make(chan struct{})
	f.run.provision = func() { <-release }
	v, err := f.s.Start(ctx, f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetKillSwitch(ctx, f.admin, true); err != nil {
		t.Fatal(err)
	}
	got := f.waitState(t, f.u, v.ID, Destroyed)
	before := f.run.destroyedN()
	close(release)
	for i := 0; i < 200 && f.run.destroyedN() == before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got.EndReason != "kill_switch" || f.run.destroyedN() == before {
		t.Fatalf("late containers must be removed too: %+v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'Cap|Budget|Kill' 2>&1 | tail -6`
Expected: FAIL — `undefined: (*Service).CheckBudgets`, `SetKillSwitch`.

- [ ] **Step 3: Migration**

`internal/db/migrations/00005_finops.sql`:

```sql
-- +goose Up
-- Global lab kill switch (spec §9.2): exactly one row.
CREATE TABLE kill_switch (
  id         BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  enabled    BOOLEAN NOT NULL DEFAULT false,
  changed_by TEXT NOT NULL DEFAULT '',
  changed_at TIMESTAMPTZ
);
INSERT INTO kill_switch DEFAULT VALUES;

-- Budget alerts already sent: once per scope, month and level.
CREATE TABLE budget_alerts (
  scope TEXT NOT NULL,         -- "<team>" or "<team>/<training>"
  month DATE NOT NULL,
  level INT NOT NULL,          -- 80 | 100
  at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, month, level)
);

-- +goose Down
DROP TABLE budget_alerts, kill_switch;
```

- [ ] **Step 4: Implement `internal/labs/finops.go`**

```go
package labs

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// overCap reports whether starting a lab of estimateUSD would push the team's or the program's month past its
// hard cap. Committed spend counts labs still running to their end.
func (s *Service) overCap(ctx context.Context, p *config.Platform, team, training string, estimateUSD float64) (bool, error) {
	for _, scope := range []string{"", training} {
		sp, err := s.spend(ctx, p, team, scope)
		if err != nil {
			return false, err
		}
		if sp.CapUSD > 0 && sp.CommittedUSD+estimateUSD > sp.CapUSD {
			return true, nil
		}
	}
	return false, nil
}

// alertLevels: 80 once spend reaches 80% of the budget, 100 once it reaches the hard cap.
func alertLevels(sp Spend) []int {
	var out []int
	if sp.BudgetUSD > 0 && sp.SpentUSD >= 0.8*sp.BudgetUSD {
		out = append(out, 80)
	}
	if sp.CapUSD > 0 && sp.SpentUSD >= sp.CapUSD {
		out = append(out, 100)
	}
	return out
}

// CheckBudgets sends the 80% and hard-cap alerts, once per scope, month and level (spec §9.2).
func (s *Service) CheckBudgets(ctx context.Context) error {
	st, err := s.platform()
	if err != nil {
		return nil // nothing to check until config is loaded
	}
	p := st.Platform
	month := monthStart(s.Now())
	for _, teamID := range slices.Sorted(maps.Keys(p.Teams)) {
		t := p.Teams[teamID]
		for _, tr := range append([]string{""}, slices.Sorted(maps.Keys(t.Programs))...) {
			sp, err := s.spend(ctx, p, teamID, tr)
			if err != nil {
				return err
			}
			scope, who, to := teamID, "Team "+t.Name, append([]string{t.Leader}, p.Admins...)
			if tr != "" {
				scope, who, to = teamID+"/"+tr, "Program "+teamID+"/"+tr, append(to, t.Programs[tr].Roles.Manager...)
			}
			for _, lvl := range alertLevels(sp) {
				tag, err := s.DB.Exec(ctx, `INSERT INTO budget_alerts (scope, month, level) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, scope, month, lvl)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue
				}
				subject := fmt.Sprintf("%s used 80%% of its monthly lab budget", who)
				text := fmt.Sprintf("%s has spent $%.2f of its $%.2f lab budget this month.", who, sp.SpentUSD, sp.BudgetUSD)
				if lvl == 100 {
					subject = fmt.Sprintf("%s reached its hard cap", who)
					text = fmt.Sprintf("%s has spent $%.2f, at its $%.2f hard cap. New paid lab requests now need an admin.", who, sp.SpentUSD, sp.CapUSD)
				}
				s.notify(ctx, notify.Event{Kind: notify.BudgetAlert, To: to, Team: teamID, Subject: subject, Text: text, Link: "/teams/" + teamID})
			}
		}
	}
	return nil
}

type KillSwitch struct {
	Enabled   bool       `json:"enabled"`
	ChangedBy string     `json:"changed_by,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

func (s *Service) KillSwitch(ctx context.Context) (KillSwitch, error) {
	var k KillSwitch
	err := s.DB.QueryRow(ctx, `SELECT enabled, changed_by, changed_at FROM kill_switch`).Scan(&k.Enabled, &k.ChangedBy, &k.ChangedAt)
	return k, err
}

// SetKillSwitch (admin): on destroys every starting or running lab and blocks new requests until turned off.
func (s *Service) SetKillSwitch(ctx context.Context, u *auth.User, enabled bool) (KillSwitch, error) {
	st, err := s.platform()
	if err != nil {
		return KillSwitch{}, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return KillSwitch{}, apperr.Wrap(apperr.Forbidden, "only admins can use the kill switch")
	}
	if _, err := s.DB.Exec(ctx, `UPDATE kill_switch SET enabled = $1, changed_by = lower($2), changed_at = $3`, enabled, u.Email, s.Now()); err != nil {
		return KillSwitch{}, err
	}
	action := map[bool]string{true: "kill_switch.on", false: "kill_switch.off"}[enabled]
	if err := audit.Log(ctx, s.DB, u.Email, action, "", nil, ""); err != nil {
		s.Log.Error("audit log failed", "action", action, "err", err)
	}
	if enabled {
		go s.killAll(context.WithoutCancel(ctx)) // the sweep repeats it every 15 s while the switch is on
	}
	return s.KillSwitch(ctx)
}

// killAll destroys every provisioning or ready lab. destroy() is guarded by state, so overlapping runs are harmless.
// ponytail: sequential; offline agents fail fast, so this stays quick below ~100 labs.
func (s *Service) killAll(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE state IN ('provisioning', 'ready')`)
	if err != nil {
		s.Log.Error("kill switch: listing labs failed", "err", err)
		return
	}
	all, err := collectInst(rows)
	if err != nil {
		s.Log.Error("kill switch: listing labs failed", "err", err)
		return
	}
	for _, inst := range all {
		s.destroy(ctx, inst, "kill_switch")
	}
}
```

Append to `internal/labs/jobs.go`:

```go
// BudgetArgs is the periodic budget check (80% / hard-cap alerts).
type BudgetArgs struct{}

func (BudgetArgs) Kind() string { return "budget_check" }

type BudgetWorker struct {
	river.WorkerDefaults[BudgetArgs]
	S *Service
}

func (w *BudgetWorker) Work(ctx context.Context, _ *river.Job[BudgetArgs]) error { return w.S.CheckBudgets(ctx) }
```

- [ ] **Step 5: Wire caps and the kill switch into requests, approvals and the sweep**

`internal/labs/service.go`, in `quote`, after the tier/re-approval block and before the schedule check:

```go
	if q.EstimateUSD > 0 { // a free lab never costs money, so a spent budget never blocks it
		over, err := s.overCap(ctx, p, team, training, q.EstimateUSD)
		if err != nil {
			return nil, err
		}
		if over {
			q.OverCap, q.Tier = true, rbac.TierAdmin
		}
	}
```

and as the very last check before `return q, nil` (it wins over the schedule message):

```go
	if ks, err := s.KillSwitch(ctx); err != nil {
		return nil, err
	} else if ks.Enabled {
		q.Blocked = "Labs are paused by an admin."
	}
```

At the top of `Sweep`, right after the `TryLock` guard:

```go
	if ks, err := s.KillSwitch(ctx); err == nil && ks.Enabled {
		s.killAll(ctx)
	}
```

`internal/labs/approvals.go`, in `Decide`, replace `// Task 8 adds: kill switch off, cap re-check.` with:

```go
		if ks, err := s.KillSwitch(ctx); err != nil {
			return "", err
		} else if ks.Enabled {
			return "", apperr.Wrap(apperr.Conflict, "Labs are paused by an admin.")
		}
		if !inst.OverCap && inst.EstimateUSD > 0 {
			over, err := s.overCap(ctx, p, inst.Team, inst.Training, inst.EstimateUSD)
			if err != nil {
				return "", err
			}
			if over {
				_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET over_cap = true, tier = 'admin' WHERE id = $1 AND state = 'pending_approval'`, inst.ID)
				if !(rbac.Checker{P: p}).IsAdmin(u.Email) {
					return "", apperr.Wrap(apperr.Conflict, "approving this would now pass a budget cap, so it has been passed to an admin")
				}
				inst.OverCap = true // an admin approving it now is an audited override
			}
		}
```

`internal/labs/http.go`, inside `Routes`:

```go
	r.Get("/api/admin/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.KillSwitch(r.Context())
		reply(w, v, err)
	})
	r.Post("/api/admin/kill-switch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetKillSwitch(r.Context(), user(r), body.Enabled)
		reply(w, v, err)
	})
```

`cmd/crucible-api/main.go`: `river.AddWorker(workers, &labs.BudgetWorker{S: labSvc})` and add `{Every: 5 * time.Minute, Args: labs.BudgetArgs{}}` to the periodic list.

- [ ] **Step 6: Run everything**

Run: `go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'; gofmt -l .; go vet ./...`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/00005_finops.sql internal/labs cmd/crucible-api/main.go
git commit -m "feat(finops): budgets and hard caps with audited admin override, budget alerts, kill switch

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: Config write-back: bot commits from the UI (team roster, enrolment, program roles/schedule/lab defaults/budget, team budget)

**Files:**
- Modify: `internal/yamlx/yamlx.go` (`Duration.MarshalYAML`, `Update`), `internal/yamlx/yamlx_test.go`
- Create: `internal/gitsync/writer.go`, `internal/gitsync/writer_test.go`
- Create: `internal/configapi/configapi.go`, `internal/configapi/configapi_test.go`
- Modify: `internal/httpapi/server.go` (`Deps.Config`, routes, `/api/me`)
- Modify: `cmd/crucible-api/main.go`
- Modify: `deploy/compose/docker-compose.yml` (git mount read-write), `scripts/seed-git.sh` (writable bare repos)

**Interfaces:**
- Consumes: `config.Load`, `config.Budget` (Task 2); `audit.Log`, `audit.Recent` (Task 3); `rbac.Checker.Can(EditTeam|ManageProgram)`.
- Produces (used by Tasks 10–12):
  - `yamlx.Update(path string, set map[string]any) error` (nil value deletes a key; keeps comments of untouched keys)
  - `gitsync.Writer{URL, Branch, Dir, Name, Email string}`; `gitsync.Change{Action, Actor, Base string; Paths []string; Edit func(dir string) error}`; `(w *Writer) Apply(ctx, ch Change) (sha string, err error)`; `gitsync.ErrStale` (Conflict: "Someone changed this in git, reload")
  - `configapi.Service{DB *pgxpool.Pool; State func() *gitsync.State; Writer *gitsync.Writer; Resync func(ctx context.Context) error}` with `Routes(r chi.Router)`
  - HTTP (all JSON; writes return `{"sha": "<40 hex>"}`):
    - `GET /api/teams` → `[{"id":"forge","name":"The Forge","role":"leader"}]` (admins see every team, role `"admin"` when not a member)
    - `GET /api/teams/{team}` → `TeamView` (below); 404 unless the user has a team role or is admin
    - `PUT /api/teams/{team}/roster` body `{"base_sha","seniors":[],"members":[],"trainees":[],"mentors":{"trainee":"mentor"}}` — leader/admin (`EditTeam`); audit `team.roster`
    - `PUT /api/teams/{team}/programs/{training}` body `{"base_sha","enrolled":[],"roles":{"manager":[],"scorers":[],"approvers":[]},"schedule":"","lab_defaults":{"ttl":"","idle_timeout":"","max_extension":""},"budget_usd_month":0}` — creates the program (enrol the team; leader/admin) or updates it (leader/program manager/admin); audit `program.enroll` / `program.update`
    - `PUT /api/teams/{team}/budget` body `{"base_sha","monthly_usd":200,"hard_cap_usd":250}` — admin only; audit `team.budget`
    - `GET /api/admin/platform` → `PlatformView` (below), admin only
    - `/api/me` adds `"teams": ["forge"]` (ids where the user has a team role) and `"can_approve": bool` (admin, a team leader, or a program approver anywhere)

`TeamView` JSON:

```json
{"id":"forge","name":"The Forge","leader":"leader@crucible.local","seniors":["senior@crucible.local"],"members":[],
 "trainees":["trainee@crucible.local"],"mentors":{"trainee@crucible.local":"senior@crucible.local"},
 "budget":{"monthly_usd":200,"hard_cap_usd":250},
 "programs":[{"training":"forge-101","title":"Forge 101: First Heat","enrolled":["trainee@crucible.local"],
   "roles":{"manager":["leader@crucible.local"],"scorers":["senior@crucible.local"],"approvers":["leader@crucible.local"]},
   "schedule":"","lab_defaults":{"ttl":"2h","idle_timeout":"30m","max_extension":"30m"},"budget_usd_month":0,"can_manage":true}],
 "available_trainings":[{"id":"forge-201","title":"Forge 201: Paid Heat"}],"schedules":["business-hours"],
 "platform_sha":"<40 hex>","can_edit_team":true,"is_admin":false}
```

`PlatformView` JSON:

```json
{"platform_sha":"…","platform_error":"…","synced_at":"…","cost_tiers":{"auto_approve_usd":0,"tier1_usd":5,"tier2_usd":25},
 "escalation_hours":4,"schedules":{"business-hours":"mon,tue,wed,thu,fri 08:00–19:00 (Europe/Bucharest)"},
 "admins":["admin@crucible.local"],
 "trainings":[{"id":"forge-101","repo":"file:///git/forge-101.git","branch":"main","head":"…","problems":[]}],
 "audit":[{"at":"…","actor":"…","action":"program.enroll","target":"forge/forge-201","detail":{},"commit_sha":"…"}]}
```

- [ ] **Step 1: Write the failing yamlx tests**

Append to `internal/yamlx/yamlx_test.go`:

```go
func TestDurationMarshalsLikePeopleWriteIt(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Hour: "2h", 45 * time.Minute: "45m", 90 * time.Minute: "1h30m", 30 * time.Second: "30s"} {
		got, _ := Duration(d).MarshalYAML()
		if got != want {
			t.Errorf("%v → %v, want %s", d, got, want)
		}
	}
}

func TestUpdateKeepsCommentsAndOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "team.yaml")
	_ = os.WriteFile(p, []byte("# The forge team\nname: The Forge # shown in the UI\nleader: l@x\nseniors: [a@x]\nmembers: []\n"), 0o644)
	err := Update(p, map[string]any{"seniors": []string{"b@x"}, "members": nil, "trainees": []string{"t@x"},
		"lab_defaults": map[string]any{"ttl": Duration(2 * time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	for _, want := range []string{"# The forge team", "name: The Forge # shown in the UI", "- b@x", "trainees:", "ttl: 2h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "members") || strings.Index(got, "name:") > strings.Index(got, "seniors:") {
		t.Fatalf("nil deletes, untouched keys keep their place:\n%s", got)
	}
	fresh := filepath.Join(t.TempDir(), "new", "p.yaml")
	if err := Update(fresh, map[string]any{"training": "x"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fresh); string(b) != "training: x\n" {
		t.Fatalf("new file: %q", b)
	}
}
```

(add `"os"`, `"path/filepath"`, `"strings"`, `"time"` to that file's imports as needed).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/yamlx/ 2>&1 | tail -4`
Expected: FAIL — `undefined: Update`, `Duration has no method MarshalYAML`.

- [ ] **Step 3: Implement yamlx additions**

Add to `internal/yamlx/yamlx.go` (imports: `"bytes"`, `"maps"`, `"slices"`, `"strings"`):

```go
// MarshalYAML writes durations the way people type them: "2h", "45m", "1h30m".
func (d Duration) MarshalYAML() (any, error) {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s, nil
}

// Update sets top-level keys of the YAML mapping in path and writes it back, keeping the comments and order of
// untouched keys. A nil value removes the key; new keys are appended in sorted order; a missing file starts empty.
func Update(path string, set map[string]any) error {
	var doc yaml.Node
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level is not a mapping", filepath.Base(path))
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		at := -1
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				at = i
				break
			}
		}
		if set[k] == nil {
			if at >= 0 {
				m.Content = slices.Delete(m.Content, at, at+2)
			}
			continue
		}
		var val yaml.Node
		if err := val.Encode(set[k]); err != nil {
			return err
		}
		if at >= 0 {
			val.LineComment = m.Content[at+1].LineComment
			m.Content[at+1] = &val
		} else {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &val)
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}
```

Callers must pass a literal `nil` (untyped) to delete a key; a typed nil slice encodes as `[]`.

Run: `go test -race ./internal/yamlx/` → Expected: `ok  	crucible/internal/yamlx`.

- [ ] **Step 4: Write the failing writer tests**

Create `internal/gitsync/writer_test.go`:

```go
package gitsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

const validPlatform = "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n"

// bare returns a bare repo seeded with files (pushes need a bare remote).
func bare(t *testing.T, files map[string]string) string {
	t.Helper()
	work := newRepo(t, files)
	dir := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "clone", "-q", "--bare", work, dir)
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func platformFiles() map[string]string {
	return map[string]string{
		"platform.yaml":            validPlatform,
		"trainings.yaml":           "trainings:\n  t1: {repo: file:///nowhere}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	}
}

func writeFile(dir, rel, body string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)
}

func newWriter(t *testing.T, remote string) *Writer {
	return &Writer{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "writer"), Name: "Crucible", Email: "bot@crucible.local"}
}

func TestWriterCommitsWithTrailer(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	sha, err := w.Apply(context.Background(), Change{Action: "update roster a", Actor: "L@X", Paths: []string{"teams/a/team.yaml"},
		Edit: func(dir string) error { return writeFile(dir, "teams/a/team.yaml", "name: A\nleader: l@x\nseniors: [s@x]\ntrainees: [u@x]\n") }})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitOut(t, remote, "rev-parse", "main"); head != sha {
		t.Fatalf("pushed %s, remote at %s", sha, head)
	}
	msg := gitOut(t, remote, "log", "-1", "--format=%an <%ae>%n%B", "main")
	if !strings.Contains(msg, "Crucible <bot@crucible.local>") || !strings.Contains(msg, "crucible: update roster a by l@x") || !strings.Contains(msg, "Crucible-Actor: l@x") {
		t.Fatalf("commit:\n%s", msg)
	}
	again, err := w.Apply(context.Background(), Change{Action: "noop", Actor: "l@x", Edit: func(string) error { return nil }})
	if err != nil || again != sha {
		t.Fatalf("an edit that changes nothing makes no commit: %s %v", again, err)
	}
}

func TestWriterRetriesWhenBranchMoves(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	calls := 0
	_, err := w.Apply(context.Background(), Change{Action: "update program a/t1", Actor: "l@x", Paths: []string{"teams/a/programs/t1.yaml"},
		Edit: func(dir string) error {
			calls++
			if calls == 1 { // someone else pushes an unrelated commit before ours
				other := filepath.Join(t.TempDir(), "other")
				run(t, "", "clone", "-q", remote, other)
				if err := writeFile(other, "quotes.yaml", "quotes: [\"x\"]\n"); err != nil {
					return err
				}
				run(t, other, "add", "-A")
				run(t, other, "commit", "-qm", "quotes")
				run(t, other, "push", "-q", "origin", "HEAD:main")
			}
			return writeFile(dir, "teams/a/programs/t1.yaml", "enrolled: [u@x]\nschedule: \"\"\n")
		}})
	if err != nil || calls != 2 {
		t.Fatalf("retry on the new tip: calls=%d err=%v", calls, err)
	}
	files := gitOut(t, remote, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "quotes.yaml") {
		t.Fatalf("the concurrent commit must survive:\n%s", files)
	}
}

func TestWriterStaleBase(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	base := gitOut(t, remote, "rev-parse", "main")
	edit := func(rel, body string) Change {
		return Change{Action: "edit " + rel, Actor: "l@x", Base: base, Paths: []string{rel},
			Edit: func(dir string) error { return writeFile(dir, rel, body) }}
	}
	if _, err := w.Apply(context.Background(), edit("teams/a/team.yaml", "name: A2\nleader: l@x\ntrainees: [u@x]\n")); err != nil {
		t.Fatal(err)
	}
	_, err := w.Apply(context.Background(), edit("teams/a/team.yaml", "name: A3\nleader: l@x\ntrainees: [u@x]\n"))
	if !errors.Is(err, ErrStale) || !errors.Is(err, apperr.Conflict) {
		t.Fatalf("same file changed since the page loaded: %v", err)
	}
	if _, err := w.Apply(context.Background(), edit("quotes.yaml", "quotes: [\"y\"]\n")); err != nil {
		t.Fatalf("a different file is not stale: %v", err)
	}
	if _, err := w.Apply(context.Background(), Change{Action: "x", Actor: "l@x", Base: "--upload-pack=evil", Edit: func(string) error { return nil }}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("base must be a sha: %v", err)
	}
}

func TestWriterRejectsInvalidConfig(t *testing.T) {
	remote := bare(t, platformFiles())
	w := newWriter(t, remote)
	before := gitOut(t, remote, "rev-parse", "main")
	_, err := w.Apply(context.Background(), Change{Action: "break", Actor: "l@x",
		Edit: func(dir string) error { return writeFile(dir, "teams/a/team.yaml", "name: A\ntrainees: [u@x]\n") }})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "no leader") {
		t.Fatalf("invalid config: %v", err)
	}
	if after := gitOut(t, remote, "rev-parse", "main"); after != before {
		t.Fatal("nothing may be pushed")
	}
}
```

`run(t, "", …)` with an empty dir runs in the test's working directory (the existing helper sets `cmd.Dir = dir`; `""` means the current directory).

- [ ] **Step 5: Run them to see them fail**

Run: `go test ./internal/gitsync/ -run Writer 2>&1 | tail -4`
Expected: FAIL — `undefined: Writer`.

- [ ] **Step 6: Implement the writer**

Create `internal/gitsync/writer.go`:

```go
package gitsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"crucible/internal/apperr"
	"crucible/internal/config"
)

// Writer commits config changes from the UI to the platform repo as the bot (spec §6): a direct commit with the
// message "crucible: <action> by <email>" and a Crucible-Actor trailer, pushed with optimistic concurrency.
type Writer struct {
	URL, Branch string
	Dir         string // the writer's own working clone
	Name, Email string // bot identity
	mu          sync.Mutex
}

// Change is one config edit.
type Change struct {
	Action string   // e.g. "update program forge/forge-101"
	Actor  string   // the user's email
	Base   string   // platform SHA the user's page showed; "" skips the stale check
	Paths  []string // repo-relative files the edit touches (checked against Base)
	Edit   func(dir string) error
}

var (
	ErrStale = apperr.Wrap(apperr.Conflict, "Someone changed this in git, reload")
	errRaced = errors.New("push rejected: the branch moved")
	shaRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Apply runs ch.Edit on the branch tip, validates the result with config.Load, commits and pushes. When the push is
// rejected because the branch moved, it starts again from the new tip and re-applies the edit (our "rebase"), up to
// 3 retries. A file changed since Base, or an invalid result, is returned to the user. It returns the new commit, or
// the tip when the edit changed nothing.
func (w *Writer) Apply(ctx context.Context, ch Change) (string, error) {
	if ch.Base != "" && !shaRE.MatchString(ch.Base) {
		return "", apperr.Wrap(apperr.Invalid, "base_sha must be a 40-character commit id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for attempt := 0; ; attempt++ {
		sha, err := w.try(ctx, ch)
		if !errors.Is(err, errRaced) {
			return sha, err
		}
		if attempt == 3 {
			return "", apperr.Wrap(apperr.Conflict, "the platform repo is busy; try again")
		}
	}
}

func (w *Writer) try(ctx context.Context, ch Change) (string, error) {
	if err := w.reset(ctx); err != nil {
		return "", err
	}
	if ch.Base != "" {
		if _, err := git(ctx, w.Dir, "merge-base", "--is-ancestor", ch.Base, "HEAD"); err != nil {
			return "", ErrStale
		}
		if _, err := git(ctx, w.Dir, append([]string{"diff", "--quiet", ch.Base, "HEAD", "--"}, ch.Paths...)...); err != nil {
			return "", ErrStale
		}
	}
	if err := ch.Edit(w.Dir); err != nil {
		return "", err
	}
	if _, err := config.Load(w.Dir); err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		return "", apperr.Wrap(apperr.Invalid, "this change would make the platform config invalid: "+first)
	}
	if _, err := git(ctx, w.Dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := git(ctx, w.Dir, "diff", "--cached", "--quiet"); err == nil {
		return git(ctx, w.Dir, "rev-parse", "HEAD") // nothing changed
	}
	actor := strings.ToLower(ch.Actor)
	if _, err := git(ctx, w.Dir, "-c", "user.name="+w.Name, "-c", "user.email="+w.Email, "commit", "-q",
		"-m", "crucible: "+ch.Action+" by "+actor, "-m", "Crucible-Actor: "+actor); err != nil {
		return "", err
	}
	if _, err := git(ctx, w.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+w.Branch); err != nil {
		if msg := err.Error(); strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]") {
			return "", errRaced
		}
		return "", err
	}
	return git(ctx, w.Dir, "rev-parse", "HEAD")
}

// reset makes Dir a clean checkout of the remote branch tip, cloning it the first time.
func (w *Writer) reset(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err != nil {
		_ = os.RemoveAll(w.Dir)
		if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, "", "clone", "-q", "--branch", w.Branch, "--", w.URL, w.Dir); err != nil {
			return err
		}
	}
	if _, err := git(ctx, w.Dir, "fetch", "-q", "origin", w.Branch); err != nil {
		return err
	}
	if _, err := git(ctx, w.Dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	_, err := git(ctx, w.Dir, "clean", "-qfdx")
	return err
}
```

Run: `go test -race ./internal/gitsync/` → Expected: `ok  	crucible/internal/gitsync`.

- [ ] **Step 7: Write the failing configapi tests**

Create `internal/configapi/configapi_test.go`:

```go
package configapi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareFrom copies src into a fresh repo and returns a bare clone of it; rewrite edits files before the commit.
func bareFrom(t *testing.T, src string, rewrite map[string]string) string {
	t.Helper()
	work := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", work).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	for rel, body := range rewrite {
		_ = os.WriteFile(filepath.Join(work, rel), []byte(body), 0o644)
	}
	sh(t, work, "init", "-q", "-b", "main")
	sh(t, work, "add", "-A")
	sh(t, work, "commit", "-qm", "seed")
	out := filepath.Join(t.TempDir(), "r.git")
	sh(t, "", "clone", "-q", "--bare", work, out)
	return out
}

type fx struct {
	s                              *Service
	sync                           *gitsync.Syncer
	remote                         string
	admin, leader, senior, trainee *auth.User
}

func setup(t *testing.T) *fx {
	t.Helper()
	content := bareFrom(t, "../../examples/forge-101", nil)
	remote := bareFrom(t, "../../examples/platform", map[string]string{
		"trainings.yaml": "trainings:\n  forge-101: {repo: " + content + "}\n  forge-201: {repo: " + content + "}\n"})
	syncer := gitsync.New(t.TempDir(), remote, "main", slog.Default())
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	pool := dbtest.New(t)
	s := &Service{DB: pool, State: syncer.Current, Resync: syncer.SyncOnce,
		Writer: &gitsync.Writer{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "w"), Name: "Crucible", Email: "bot@x"}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: s, sync: syncer, remote: remote, admin: u("admin@crucible.local"), leader: u("leader@crucible.local"),
		senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) sha() string { return f.sync.Current().PlatformSHA }

func emptyProgram(base string) ProgramBody {
	return ProgramBody{BaseSHA: base, Roles: RolesView{Manager: []string{}, Scorers: []string{}, Approvers: []string{}}}
}

func TestLeaderEnrollsTheTeamAndATrainee(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	view, err := f.s.Team(f.leader, "forge")
	if err != nil || !view.CanEditTeam || len(view.AvailableTrainings) != 1 || view.AvailableTrainings[0].ID != "forge-201" {
		t.Fatalf("team view %+v %v", view, err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", emptyProgram(f.sha())); err != nil {
		t.Fatal(err)
	}
	body := emptyProgram(f.sha())
	body.Enrolled = []string{"TRAINEE@crucible.local"}
	body.LabDefaults = LabDefaultsView{TTL: "90m"}
	sha, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-201", body)
	if err != nil {
		t.Fatal(err)
	}
	st := f.sync.Current()
	prog := st.Platform.Teams["forge"].Programs["forge-201"]
	if st.PlatformSHA != sha || prog == nil || prog.Enrolled[0] != "trainee@crucible.local" || prog.LabDefaults.TTL.D().Minutes() != 90 {
		t.Fatalf("resynced program %+v at %s (want %s)", prog, st.PlatformSHA, sha)
	}
	log := sh(t, f.remote, "log", "--format=%B", "-2", "main")
	if !strings.Contains(log, "crucible: enroll forge in forge-201 by leader@crucible.local") ||
		!strings.Contains(log, "crucible: update program forge/forge-201 by leader@crucible.local") || !strings.Contains(log, "Crucible-Actor: leader@crucible.local") {
		t.Fatalf("commits:\n%s", log)
	}
	var action, commit string
	_ = f.s.DB.QueryRow(ctx, `SELECT action, commit_sha FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action, &commit)
	if action != "program.update" || commit != sha {
		t.Fatalf("audit %s %s", action, commit)
	}
}

func TestPermissions(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.SetProgram(ctx, f.trainee, "forge", "forge-101", emptyProgram(f.sha())); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee edits a program: %v", err)
	}
	if _, err := f.s.SetRoster(ctx, f.senior, "forge", RosterBody{BaseSHA: f.sha()}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("senior edits the roster: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.leader, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 1}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader edits the team budget: %v", err)
	}
	if _, err := f.s.SetBudget(ctx, f.admin, "forge", BudgetBody{BaseSHA: f.sha(), MonthlyUSD: 300, HardCapUSD: 350}); err != nil {
		t.Fatal(err)
	}
	if b := f.sync.Current().Platform.Teams["forge"].Budget; b.MonthlyUSD != 300 || b.HardCapUSD != 350 {
		t.Fatalf("budget %+v", b)
	}
	if _, err := f.s.Team(&auth.User{Email: "stranger@x"}, "forge"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("outsiders: %v", err)
	}
	if teams, _ := f.s.Teams(f.admin); len(teams) != 1 || teams[0].Role != "admin" {
		t.Fatalf("admins see every team: %+v", teams)
	}
	if _, err := f.s.Platform(f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("platform view is admin-only: %v", err)
	}
}

func TestStaleEditIsRefused(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	old := f.sha()
	body := emptyProgram(old)
	body.Enrolled = []string{"trainee@crucible.local"}
	body.Schedule = "business-hours"
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "Someone changed this in git, reload") {
		t.Fatalf("second save from the same stale page: %v", err)
	}
	roster := RosterBody{BaseSHA: old, Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "senior@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); err != nil {
		t.Fatalf("a different file from the same page is fine: %v", err)
	}
}

func TestInvalidEditIsExplained(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	roster := RosterBody{BaseSHA: f.sha(), Seniors: []string{"senior@crucible.local"}, Trainees: []string{"trainee@crucible.local"},
		Mentors: map[string]string{"trainee@crucible.local": "trainee@crucible.local"}}
	if _, err := f.s.SetRoster(ctx, f.leader, "forge", roster); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "mentor pairing") {
		t.Fatalf("invalid mentor: %v", err)
	}
	body := emptyProgram(f.sha())
	body.LabDefaults = LabDefaultsView{TTL: "forever"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", body); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("bad duration: %v", err)
	}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "../../etc", emptyProgram(f.sha())); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training id: %v", err)
	}
}
```

- [ ] **Step 8: Run them to see them fail**

Run: `go test ./internal/configapi/ 2>&1 | tail -4`
Expected: FAIL — package has no non-test files (`undefined: Service`).

- [ ] **Step 9: Implement `internal/configapi/configapi.go`**

```go
// Package configapi lets leaders, managers and admins read and change platform config from the UI (spec §4.3, §5.3,
// §6). Every change is a bot commit to the platform repo; git stays the source of truth.
package configapi

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"crucible/internal/httpx"
	"crucible/internal/rbac"
	"crucible/internal/yamlx"
)

type Service struct {
	DB     *pgxpool.Pool
	State  func() *gitsync.State
	Writer *gitsync.Writer
	Resync func(ctx context.Context) error // re-read git after a write so the page shows the change at once
}

type TeamSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type RolesView struct {
	Manager   []string `json:"manager"`
	Scorers   []string `json:"scorers"`
	Approvers []string `json:"approvers"`
}

type LabDefaultsView struct {
	TTL          string `json:"ttl"`
	IdleTimeout  string `json:"idle_timeout"`
	MaxExtension string `json:"max_extension"`
}

type ProgramView struct {
	Training       string          `json:"training"`
	Title          string          `json:"title"`
	Enrolled       []string        `json:"enrolled"`
	Roles          RolesView       `json:"roles"`
	Schedule       string          `json:"schedule"`
	LabDefaults    LabDefaultsView `json:"lab_defaults"`
	BudgetUSDMonth float64         `json:"budget_usd_month"`
	CanManage      bool            `json:"can_manage"`
}

type TrainingOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type TeamView struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Leader             string            `json:"leader"`
	Seniors            []string          `json:"seniors"`
	Members            []string          `json:"members"`
	Trainees           []string          `json:"trainees"`
	Mentors            map[string]string `json:"mentors"`
	Budget             config.Budget     `json:"budget"`
	Programs           []ProgramView     `json:"programs"`
	AvailableTrainings []TrainingOption  `json:"available_trainings"`
	Schedules          []string          `json:"schedules"`
	PlatformSHA        string            `json:"platform_sha"`
	CanEditTeam        bool              `json:"can_edit_team"`
	IsAdmin            bool              `json:"is_admin"`
}

type RosterBody struct {
	BaseSHA  string            `json:"base_sha"`
	Seniors  []string          `json:"seniors"`
	Members  []string          `json:"members"`
	Trainees []string          `json:"trainees"`
	Mentors  map[string]string `json:"mentors"`
}

type ProgramBody struct {
	BaseSHA        string          `json:"base_sha"`
	Enrolled       []string        `json:"enrolled"`
	Roles          RolesView       `json:"roles"`
	Schedule       string          `json:"schedule"`
	LabDefaults    LabDefaultsView `json:"lab_defaults"`
	BudgetUSDMonth float64         `json:"budget_usd_month"`
}

type BudgetBody struct {
	BaseSHA    string  `json:"base_sha"`
	MonthlyUSD float64 `json:"monthly_usd"`
	HardCapUSD float64 `json:"hard_cap_usd"`
}

type TrainingStatus struct {
	ID       string   `json:"id"`
	Repo     string   `json:"repo"`
	Branch   string   `json:"branch"`
	Head     string   `json:"head"`
	Problems []string `json:"problems"`
}

type PlatformView struct {
	PlatformSHA     string            `json:"platform_sha"`
	PlatformErr     string            `json:"platform_error,omitempty"`
	SyncedAt        time.Time         `json:"synced_at"`
	CostTiers       *config.CostTiers `json:"cost_tiers"`
	EscalationHours float64           `json:"escalation_hours"`
	Schedules       map[string]string `json:"schedules"`
	Admins          []string          `json:"admins"`
	Trainings       []TrainingStatus  `json:"trainings"`
	Audit           []audit.Entry     `json:"audit"`
}

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "config is still syncing, try again in a moment")
	}
	return st, nil
}

// emails lowercases, trims, drops blanks and duplicates; never nil (so YAML gets [] not null).
func emails(in []string) []string {
	out := []string{}
	for _, e := range in {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Service) Teams(u *auth.User) ([]TeamSummary, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	admin := rbac.Checker{P: st.Platform}.IsAdmin(u.Email)
	out := []TeamSummary{}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		t := st.Platform.Teams[id]
		role := t.RoleOf(u.Email)
		if role == "" && admin {
			role = "admin"
		}
		if role != "" {
			out = append(out, TeamSummary{ID: id, Name: t.Name, Role: role})
		}
	}
	return out, nil
}

func trainingTitle(st *gitsync.State, team, id string) string {
	if t, _ := st.ProgramTraining(team, id); t != nil {
		return t.Title
	}
	if t := st.Training(id, st.Heads[id]); t != nil {
		return t.Title
	}
	return id
}

func dur(d yamlx.Duration) string {
	if d == 0 {
		return ""
	}
	s, _ := d.MarshalYAML()
	return s.(string)
}

func (s *Service) Team(u *auth.User, id string) (*TeamView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	c := rbac.Checker{P: st.Platform}
	t := st.Platform.Teams[id]
	if t == nil || (t.RoleOf(u.Email) == "" && !c.IsAdmin(u.Email)) {
		return nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	v := &TeamView{ID: id, Name: t.Name, Leader: t.Leader, Seniors: emails(t.Seniors), Members: emails(t.Members),
		Trainees: emails(t.Trainees), Mentors: t.Mentors, Budget: t.Budget, Programs: []ProgramView{}, AvailableTrainings: []TrainingOption{},
		Schedules: slices.Sorted(maps.Keys(st.Platform.Settings.Schedules)), PlatformSHA: st.PlatformSHA,
		CanEditTeam: c.Can(u.Email, rbac.EditTeam, id, "", ""), IsAdmin: c.IsAdmin(u.Email)}
	if v.Schedules == nil {
		v.Schedules = []string{}
	}
	for _, tr := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		p := t.Programs[tr]
		if p == nil {
			v.AvailableTrainings = append(v.AvailableTrainings, TrainingOption{ID: tr, Title: trainingTitle(st, id, tr)})
			continue
		}
		v.Programs = append(v.Programs, ProgramView{Training: tr, Title: trainingTitle(st, id, tr), Enrolled: emails(p.Enrolled),
			Roles:    RolesView{Manager: emails(p.Roles.Manager), Scorers: emails(p.Roles.Scorers), Approvers: emails(p.Roles.Approvers)},
			Schedule: p.Schedule, BudgetUSDMonth: p.BudgetUSDMonth, CanManage: c.Can(u.Email, rbac.ManageProgram, id, tr, ""),
			LabDefaults: LabDefaultsView{TTL: dur(p.LabDefaults.TTL), IdleTimeout: dur(p.LabDefaults.IdleTimeout), MaxExtension: dur(p.LabDefaults.MaxExtension)}})
	}
	return v, nil
}

// write commits one change, records it in the audit log with its commit, and re-reads git.
func (s *Service) write(ctx context.Context, u *auth.User, ch gitsync.Change, auditAction, target string, detail map[string]any) (string, error) {
	ch.Actor = u.Email
	sha, err := s.Writer.Apply(ctx, ch)
	if err != nil {
		return "", err
	}
	if err := audit.Log(ctx, s.DB, u.Email, auditAction, target, detail, sha); err != nil {
		slog.Error("audit log failed", "action", auditAction, "err", err)
	}
	if s.Resync != nil {
		if err := s.Resync(ctx); err != nil {
			slog.Warn("re-sync after a config write failed; the poller will pick it up", "err", err)
		}
	}
	return sha, nil
}

func (s *Service) team(id string) (*gitsync.State, *config.Team, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, err
	}
	t := st.Platform.Teams[id]
	if t == nil {
		return nil, nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	return st, t, nil
}

func (s *Service) SetRoster(ctx context.Context, u *auth.User, team string, b RosterBody) (string, error) {
	st, _, err := s.team(team)
	if err != nil {
		return "", err
	}
	if !(rbac.Checker{P: st.Platform}).Can(u.Email, rbac.EditTeam, team, "", "") {
		return "", apperr.Wrap(apperr.Forbidden, "only the team leader or an admin can change the roster")
	}
	mentors := map[string]string{}
	for k, v := range b.Mentors {
		if k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v)); k != "" && v != "" {
			mentors[k] = v
		}
	}
	rel := path.Join("teams", team, "team.yaml")
	set := map[string]any{"seniors": emails(b.Seniors), "members": emails(b.Members), "trainees": emails(b.Trainees), "mentors": mentors}
	return s.write(ctx, u, gitsync.Change{Action: "update roster " + team, Base: b.BaseSHA, Paths: []string{rel},
		Edit: func(dir string) error { return yamlx.Update(filepath.Join(dir, rel), set) }}, "team.roster", team, set)
}

func (s *Service) SetProgram(ctx context.Context, u *auth.User, team, training string, b ProgramBody) (string, error) {
	st, t, err := s.team(team)
	if err != nil {
		return "", err
	}
	if _, ok := st.Platform.Trainings[training]; !ok { // also keeps the id a safe file name
		return "", apperr.Wrap(apperr.NotFound, "unknown training")
	}
	c := rbac.Checker{P: st.Platform}
	exists := t.Programs[training] != nil
	if (exists && !c.Can(u.Email, rbac.ManageProgram, team, training, "")) || (!exists && !c.Can(u.Email, rbac.EditTeam, team, "", "")) {
		return "", apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can change this program")
	}
	defaults := map[string]any{}
	for k, v := range map[string]string{"ttl": b.LabDefaults.TTL, "idle_timeout": b.LabDefaults.IdleTimeout, "max_extension": b.LabDefaults.MaxExtension} {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return "", apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s must be a duration like 2h or 45m", k))
		}
		defaults[k] = yamlx.Duration(d)
	}
	if b.BudgetUSDMonth < 0 {
		return "", apperr.Wrap(apperr.Invalid, "the program budget must not be negative")
	}
	set := map[string]any{"training": training, "enrolled": emails(b.Enrolled), "schedule": nil, "lab_defaults": nil, "budget_usd_month": nil,
		"roles": map[string][]string{"manager": emails(b.Roles.Manager), "scorers": emails(b.Roles.Scorers), "approvers": emails(b.Roles.Approvers)}}
	if b.Schedule != "" {
		set["schedule"] = b.Schedule
	}
	if len(defaults) > 0 {
		set["lab_defaults"] = defaults
	}
	if b.BudgetUSDMonth > 0 {
		set["budget_usd_month"] = b.BudgetUSDMonth
	}
	rel := path.Join("teams", team, "programs", training+".yaml")
	action, auditAction := "update program "+team+"/"+training, "program.update"
	if !exists {
		action, auditAction = "enroll "+team+" in "+training, "program.enroll"
	}
	return s.write(ctx, u, gitsync.Change{Action: action, Base: b.BaseSHA, Paths: []string{rel},
		Edit: func(dir string) error { return yamlx.Update(filepath.Join(dir, rel), set) }}, auditAction, team+"/"+training, map[string]any{
		"enrolled": set["enrolled"], "roles": set["roles"], "schedule": b.Schedule, "lab_defaults": b.LabDefaults, "budget_usd_month": b.BudgetUSDMonth})
}

func (s *Service) SetBudget(ctx context.Context, u *auth.User, team string, b BudgetBody) (string, error) {
	st, _, err := s.team(team)
	if err != nil {
		return "", err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return "", apperr.Wrap(apperr.Forbidden, "only admins set team budgets")
	}
	set := map[string]any{"monthly_usd": b.MonthlyUSD, "hard_cap_usd": nil}
	if b.HardCapUSD > 0 {
		set["hard_cap_usd"] = b.HardCapUSD
	}
	rel := path.Join("teams", team, "budget.yaml")
	return s.write(ctx, u, gitsync.Change{Action: "set budget " + team, Base: b.BaseSHA, Paths: []string{rel},
		Edit: func(dir string) error { return yamlx.Update(filepath.Join(dir, rel), set) }}, "team.budget", team, set)
}

// Platform is the admin's read-only view of platform.yaml, sync health and recent privileged actions.
func (s *Service) Platform(u *auth.User) (*PlatformView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	p := st.Platform
	if !(rbac.Checker{P: p}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "admins only")
	}
	v := &PlatformView{PlatformSHA: st.PlatformSHA, PlatformErr: st.PlatformErr, SyncedAt: st.SyncedAt, CostTiers: p.Settings.CostTiers,
		EscalationHours: p.Settings.EscalationHours, Schedules: map[string]string{}, Admins: p.Admins, Trainings: []TrainingStatus{}}
	for name, sc := range p.Settings.Schedules {
		v.Schedules[name] = sc.String()
	}
	for _, id := range slices.Sorted(maps.Keys(p.Trainings)) {
		ref := p.Trainings[id]
		ts := TrainingStatus{ID: id, Repo: ref.Repo, Branch: ref.Branch, Head: st.Heads[id], Problems: []string{}}
		for _, key := range []string{id, id + "@" + st.Heads[id]} {
			for _, pr := range st.Problems[key] {
				ts.Problems = append(ts.Problems, pr.String())
			}
		}
		v.Trainings = append(v.Trainings, ts)
	}
	return v, nil
}

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	reply := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, v)
	}
	sha := func(w http.ResponseWriter, sha string, err error) { reply(w, map[string]string{"sha": sha}, err) }
	r.Get("/api/teams", func(w http.ResponseWriter, r *http.Request) { v, err := s.Teams(user(r)); reply(w, v, err) })
	r.Get("/api/teams/{team}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Team(user(r), chi.URLParam(r, "team"))
		reply(w, v, err)
	})
	r.Put("/api/teams/{team}/roster", func(w http.ResponseWriter, r *http.Request) {
		var b RosterBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetRoster(r.Context(), user(r), chi.URLParam(r, "team"), b)
		sha(w, v, err)
	})
	r.Put("/api/teams/{team}/programs/{training}", func(w http.ResponseWriter, r *http.Request) {
		var b ProgramBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetProgram(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"), b)
		sha(w, v, err)
	})
	r.Put("/api/teams/{team}/budget", func(w http.ResponseWriter, r *http.Request) {
		var b BudgetBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetBudget(r.Context(), user(r), chi.URLParam(r, "team"), b)
		sha(w, v, err)
	})
	r.Get("/api/admin/platform", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Platform(user(r))
		if err == nil {
			v.Audit, err = audit.Recent(r.Context(), s.DB, 25)
		}
		reply(w, v, err)
	})
}
```

- [ ] **Step 10: Run the configapi tests**

Run: `go test -race ./internal/configapi/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: four `--- PASS` lines, `ok  	crucible/internal/configapi`.

- [ ] **Step 11: Wire it into the server, main, compose and the seed script**

`internal/httpapi/server.go`: import `"slices"`, `"sort"` and `"crucible/internal/configapi"`; add `Config *configapi.Service` to `Deps`; after the `d.Notify` routes:

```go
		if d.Config != nil {
			d.Config.Routes(r)
		}
```

Replace the `/api/me` handler body:

```go
			u := auth.UserFrom(r.Context())
			admin, theme, teams, canApprove := false, "forge", []string{}, false
			if st := state(d); st != nil && st.Platform != nil {
				c := rbac.Checker{P: st.Platform}
				admin, theme = c.IsAdmin(u.Email), st.Platform.Settings.DefaultTheme
				canApprove = admin
				for id, t := range st.Platform.Teams {
					if t.RoleOf(u.Email) != "" {
						teams = append(teams, id)
					}
					canApprove = canApprove || t.Leader == u.Email
					for _, p := range t.Programs {
						canApprove = canApprove || slices.Contains(p.Roles.Approvers, u.Email)
					}
				}
				sort.Strings(teams)
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "is_admin": admin, "default_theme": theme, "teams": teams, "can_approve": canApprove})
```

(`u.Email` is stored lowercased by `UpsertUser`.)

`cmd/crucible-api/main.go`: import `"path/filepath"`, `"crucible/internal/configapi"`; after `syncer := gitsync.New(…)`:

```go
	writer := &gitsync.Writer{URL: must("CRUCIBLE_PLATFORM_REPO"), Branch: env("CRUCIBLE_PLATFORM_BRANCH", "main"),
		Dir:  filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "writer"),
		Name: env("CRUCIBLE_GIT_BOT_NAME", "Crucible"), Email: env("CRUCIBLE_GIT_BOT_EMAIL", "crucible@localhost")}
```

and next to the other services:

```go
	cfgSvc := &configapi.Service{DB: pool, State: syncer.Current, Writer: writer, Resync: syncer.SyncOnce}
```

with `Config: cfgSvc` in `httpapi.Deps`.

`deploy/compose/docker-compose.yml`: change the api volume to `"../../.local/git:/git"` (drop `:ro`; the UI now pushes to the seeded bare repos).

`scripts/seed-git.sh`: after `git clone -q --bare "$work" "$out/$name.git"` add:

```bash
  git -C "$out/$name.git" config core.sharedRepository world
  chmod -R a+rwX "$out/$name.git"   # the api container (uid 10001) pushes config commits here
```

- [ ] **Step 12: Run everything, including the local check**

Run: `go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'; gofmt -l .; go vet ./...; KEYCLOAK_PORT=8082 make local-check 2>&1 | tail -3`
Expected: no Go output; the local check ends with `🔥 Local check passed. The forge holds.`

- [ ] **Step 13: Commit**

```bash
git add internal/yamlx internal/gitsync internal/configapi internal/httpapi cmd/crucible-api/main.go deploy/compose/docker-compose.yml scripts/seed-git.sh
git commit -m "feat(config): write team and program config back to the platform repo as audited bot commits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 10: Web: Team page and Program settings (config write-back UI)

**Files:**
- Create: `web/src/lib/lists.ts`, `web/src/lib/lists.test.ts`
- Create: `web/src/pages/Team.tsx`, `web/src/pages/ProgramSettings.tsx`
- Modify: `web/src/types.ts`, `web/src/App.tsx` (routes), `web/src/components/Nav.tsx`, `web/src/theme/app.css`

**Interfaces:**
- Consumes: `/api/me` `teams`, `/api/teams`, `/api/teams/{team}`, the three PUT endpoints (Task 9).
- Produces (selectors Task 12 relies on):
  - Nav link text `Team` → `/teams` (shown when `me.teams.length > 0 || me.is_admin`)
  - Team page: `<h1>` = team name; `<select aria-label="Training to enroll">` with option values = training ids; button `Enroll the team` (navigates to the program settings page after the commit)
  - Program settings page: `<h1>Program settings: {title}</h1>`; one checkbox per team member whose accessible name is the email; textareas labelled `Managers`, `Scorers`, `Approvers`; select labelled `Schedule`; inputs `Lab TTL`, `Idle timeout`, `Max extension`, `Monthly budget (USD, 0 = none)`; button `Save program`; after success `<p role="status">Saved to git (abc1234)</p>`
  - Errors from saves (including 409 "Someone changed this in git, reload") appear as toasts.

- [ ] **Step 1: Write the failing list-parsing test**

Create `web/src/lib/lists.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { formatMentors, parseEmails, parseMentors } from './lists'

describe('list fields', () => {
  it('reads one email per line or comma separated, lowercased, without duplicates', () => {
    expect(parseEmails(' A@x.io\nb@x.io, a@x.io\n\n')).toEqual(['a@x.io', 'b@x.io'])
  })
  it('reads and writes "trainee = mentor" lines', () => {
    const m = parseMentors('T@x = S@x\n\n u@x=s@x ')
    expect(m).toEqual({ 't@x': 's@x', 'u@x': 's@x' })
    expect(formatMentors(m)).toBe('t@x = s@x\nu@x = s@x')
  })
  it('rejects a line without "="', () => {
    expect(() => parseMentors('t@x s@x')).toThrow(/trainee = mentor/)
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/lib/lists.test.ts 2>&1 | tail -4`
Expected: FAIL — `Failed to resolve import "./lists"`.

- [ ] **Step 3: Implement `web/src/lib/lists.ts`**

```ts
// One email per line (or comma separated) → lowercased, de-duplicated list.
export function parseEmails(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).map((s) => s.trim().toLowerCase()).filter(Boolean))]
}

// "trainee = mentor" per line → { trainee: mentor }.
export function parseMentors(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const [trainee, mentor, extra] = line.split('=').map((s) => s.trim().toLowerCase())
    if (!trainee || !mentor || extra !== undefined) throw new Error(`Mentor lines look like "trainee = mentor": ${line}`)
    out[trainee] = mentor
  }
  return out
}

export function formatMentors(m: Record<string, string>): string {
  return Object.entries(m).sort(([a], [b]) => a.localeCompare(b)).map(([t, s]) => `${t} = ${s}`).join('\n')
}
```

Run: `cd web && npx vitest run src/lib/lists.test.ts` → Expected: `3 passed`.

- [ ] **Step 4: Types**

`web/src/types.ts` — change `Me` and add the team types:

```ts
export type Me = { user: User; is_admin: boolean; default_theme: string; teams: string[]; can_approve: boolean }
export type TeamSummary = { id: string; name: string; role: string }
export type Roles = { manager: string[]; scorers: string[]; approvers: string[] }
export type LabDefaults = { ttl: string; idle_timeout: string; max_extension: string }
export type ProgramConfig = {
  training: string; title: string; enrolled: string[]; roles: Roles; schedule: string
  lab_defaults: LabDefaults; budget_usd_month: number; can_manage: boolean
}
export type TeamView = {
  id: string; name: string; leader: string; seniors: string[]; members: string[]; trainees: string[]
  mentors: Record<string, string>; budget: { monthly_usd: number; hard_cap_usd: number }; programs: ProgramConfig[]
  available_trainings: { id: string; title: string }[]; schedules: string[]; platform_sha: string
  can_edit_team: boolean; is_admin: boolean
}
```

- [ ] **Step 5: Team page**

Create `web/src/pages/Team.tsx`:

```tsx
import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { TeamSummary, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { formatMentors, parseEmails, parseMentors } from '../lib/lists'

const usd = (n: number) => `$${n.toFixed(2)}`

export function TeamsIndex() {
  const { data, error } = useFetch<TeamSummary[]>('/api/teams')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  if (data.length === 1) return <Navigate to={`/teams/${data[0].id}`} replace />
  return (
    <section className="page">
      <h1>Teams</h1>
      {data.length === 0 && <p className="muted">You're not on a team yet.</p>}
      <ul>
        {data.map((t) => (
          <li key={t.id}><Link to={`/teams/${t.id}`}>{t.name}</Link> <span className="muted">{t.role}</span></li>
        ))}
      </ul>
    </section>
  )
}

export function TeamPage() {
  const { team } = useParams()
  const { data, error, reload } = useFetch<TeamView>(`/api/teams/${team}`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Gathering the smiths…" />
  return (
    <section className="page">
      <h1>{data.name}</h1>
      <p className="lede">Led by {data.leader}</p>
      <Roster key={`r-${data.platform_sha}`} team={data} onSaved={reload} />
      <Programs team={data} />
      <Budget key={`b-${data.platform_sha}`} team={data} onSaved={reload} />
    </section>
  )
}

function Roster({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [seniors, setSeniors] = useState(team.seniors.join('\n'))
  const [members, setMembers] = useState(team.members.join('\n'))
  const [trainees, setTrainees] = useState(team.trainees.join('\n'))
  const [mentors, setMentors] = useState(formatMentors(team.mentors))
  const [busy, setBusy] = useState(false)
  if (!team.can_edit_team) {
    return (
      <>
        <h2>People</h2>
        <dl className="facts">
          <dt>Seniors</dt><dd>{team.seniors.join(', ') || '—'}</dd>
          <dt>Members</dt><dd>{team.members.join(', ') || '—'}</dd>
          <dt>Trainees</dt><dd>{team.trainees.join(', ') || '—'}</dd>
          <dt>Mentors</dt><dd>{formatMentors(team.mentors) || '—'}</dd>
        </dl>
      </>
    )
  }
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api(`/api/teams/${team.id}/roster`, { method: 'PUT', json: {
        base_sha: team.platform_sha, seniors: parseEmails(seniors), members: parseEmails(members),
        trainees: parseEmails(trainees), mentors: parseMentors(mentors) } })
      toast('Saved to git')
      onSaved()
    } catch (err) {
      toast((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack" onSubmit={save}>
      <h2>People</h2>
      <p className="muted">One email per line. Changes are committed to the platform repo.</p>
      <label>Seniors <textarea rows={3} value={seniors} onChange={(e) => setSeniors(e.target.value)} /></label>
      <label>Members <textarea rows={3} value={members} onChange={(e) => setMembers(e.target.value)} /></label>
      <label>Trainees <textarea rows={4} value={trainees} onChange={(e) => setTrainees(e.target.value)} /></label>
      <label>Mentors (one “trainee = mentor” per line) <textarea rows={3} value={mentors} onChange={(e) => setMentors(e.target.value)} /></label>
      <button className="primary" disabled={busy}>Save roster</button>
    </form>
  )
}

function Programs({ team }: { team: TeamView }) {
  const navigate = useNavigate()
  const [pick, setPick] = useState(team.available_trainings[0]?.id ?? '')
  const [busy, setBusy] = useState(false)
  const enroll = async () => {
    setBusy(true)
    try {
      await api(`/api/teams/${team.id}/programs/${pick}`, { method: 'PUT', json: {
        base_sha: team.platform_sha, enrolled: [], roles: { manager: [], scorers: [], approvers: [] }, schedule: '',
        lab_defaults: { ttl: '', idle_timeout: '', max_extension: '' }, budget_usd_month: 0 } })
      navigate(`/teams/${team.id}/programs/${pick}`)
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <h2>Programs</h2>
      {team.programs.length === 0 && <p className="muted">This team isn't enrolled in any training yet.</p>}
      {team.programs.length > 0 && (
        <table className="grid">
          <thead><tr><th>Training</th><th>Enrolled</th><th>Schedule</th><th>Budget</th><th /></tr></thead>
          <tbody>
            {team.programs.map((p) => (
              <tr key={p.training}>
                <td>{p.title}</td>
                <td>{p.enrolled.length}</td>
                <td>{p.schedule || 'any time'}</td>
                <td>{p.budget_usd_month ? `${usd(p.budget_usd_month)}/month` : '—'}</td>
                <td>{p.can_manage && <Link to={`/teams/${team.id}/programs/${p.training}`}>Settings</Link>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {team.can_edit_team && team.available_trainings.length > 0 && (
        <div className="row">
          <select aria-label="Training to enroll" value={pick} onChange={(e) => setPick(e.target.value)}>
            {team.available_trainings.map((t) => <option key={t.id} value={t.id}>{t.title}</option>)}
          </select>
          <button className="primary" disabled={busy || !pick} onClick={enroll}>Enroll the team</button>
        </div>
      )}
    </>
  )
}

function Budget({ team, onSaved }: { team: TeamView; onSaved: () => void }) {
  const [monthly, setMonthly] = useState(String(team.budget.monthly_usd))
  const [cap, setCap] = useState(String(team.budget.hard_cap_usd))
  const summary = team.budget.monthly_usd
    ? `${usd(team.budget.monthly_usd)} a month · hard cap ${usd(team.budget.hard_cap_usd)}`
    : 'No team budget set.'
  if (!team.is_admin) return (<><h2>Budget</h2><p>{summary}</p></>)
  const save = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await api(`/api/teams/${team.id}/budget`, { method: 'PUT', json: { base_sha: team.platform_sha, monthly_usd: Number(monthly) || 0, hard_cap_usd: Number(cap) || 0 } })
      toast('Saved to git')
      onSaved()
    } catch (err) {
      toast((err as Error).message)
    }
  }
  return (
    <form className="stack" onSubmit={save}>
      <h2>Budget</h2>
      <p className="muted">{summary} An alert goes out at 80%; requests that would pass the cap need an admin.</p>
      <label>Monthly budget (USD) <input type="number" min={0} step="0.01" value={monthly} onChange={(e) => setMonthly(e.target.value)} /></label>
      <label>Hard cap (USD, empty = the budget) <input type="number" min={0} step="0.01" value={cap} onChange={(e) => setCap(e.target.value)} /></label>
      <button className="primary">Save budget</button>
    </form>
  )
}
```

- [ ] **Step 6: Program settings page**

Create `web/src/pages/ProgramSettings.tsx`:

```tsx
import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router'
import { api, ApiError } from '../api'
import { useFetch } from '../useFetch'
import type { ProgramConfig, TeamView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { parseEmails } from '../lib/lists'

export function ProgramSettingsPage() {
  const { team, training } = useParams()
  const { data, error, reload } = useFetch<TeamView>(`/api/teams/${team}`)
  const [saved, setSaved] = useState<string>()
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the blueprint…" />
  const prog = data.programs.find((p) => p.training === training)
  if (!prog) return <ErrorBox error={new ApiError(404, 'This team is not enrolled in that training.')} />
  return (
    <section className="page">
      <Link to={`/teams/${data.id}`}>← {data.name}</Link>
      <h1>Program settings: {prog.title}</h1>
      {saved && <p role="status" className="pass">Saved to git ({saved})</p>}
      <ProgramForm key={data.platform_sha} team={data} prog={prog} onSaved={(sha) => { setSaved(sha.slice(0, 7)); reload() }} />
    </section>
  )
}

function ProgramForm({ team, prog, onSaved }: { team: TeamView; prog: ProgramConfig; onSaved: (sha: string) => void }) {
  const people = [team.leader, ...team.seniors, ...team.members, ...team.trainees]
  const [enrolled, setEnrolled] = useState(() => new Set(prog.enrolled))
  const [managers, setManagers] = useState(prog.roles.manager.join('\n'))
  const [scorers, setScorers] = useState(prog.roles.scorers.join('\n'))
  const [approvers, setApprovers] = useState(prog.roles.approvers.join('\n'))
  const [schedule, setSchedule] = useState(prog.schedule)
  const [ttl, setTtl] = useState(prog.lab_defaults.ttl)
  const [idle, setIdle] = useState(prog.lab_defaults.idle_timeout)
  const [ext, setExt] = useState(prog.lab_defaults.max_extension)
  const [budget, setBudget] = useState(String(prog.budget_usd_month))
  const [busy, setBusy] = useState(false)
  const toggle = (p: string, on: boolean) => {
    const next = new Set(enrolled)
    if (on) next.add(p)
    else next.delete(p)
    setEnrolled(next)
  }
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await api<{ sha: string }>(`/api/teams/${team.id}/programs/${prog.training}`, { method: 'PUT', json: {
        base_sha: team.platform_sha, enrolled: [...enrolled],
        roles: { manager: parseEmails(managers), scorers: parseEmails(scorers), approvers: parseEmails(approvers) },
        schedule, lab_defaults: { ttl, idle_timeout: idle, max_extension: ext }, budget_usd_month: Number(budget) || 0 } })
      onSaved(res.sha)
    } catch (err) {
      toast((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="stack" onSubmit={save}>
      <fieldset className="stack" disabled={!prog.can_manage || busy}>
        <legend>Enrolled</legend>
        {people.map((p) => (
          <label key={p}><input type="checkbox" checked={enrolled.has(p)} onChange={(e) => toggle(p, e.target.checked)} /> {p}</label>
        ))}
      </fieldset>
      <fieldset className="stack" disabled={!prog.can_manage || busy}>
        <legend>Roles (one email per line; empty = the defaults: leader manages and approves, seniors score)</legend>
        <label>Managers <textarea rows={2} value={managers} onChange={(e) => setManagers(e.target.value)} /></label>
        <label>Scorers <textarea rows={2} value={scorers} onChange={(e) => setScorers(e.target.value)} /></label>
        <label>Approvers <textarea rows={2} value={approvers} onChange={(e) => setApprovers(e.target.value)} /></label>
      </fieldset>
      <fieldset className="stack" disabled={!prog.can_manage || busy}>
        <legend>Labs</legend>
        <label>Schedule
          <select value={schedule} onChange={(e) => setSchedule(e.target.value)}>
            <option value="">Any time</option>
            {team.schedules.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
        <label>Lab TTL <input value={ttl} placeholder="lab default, e.g. 2h" onChange={(e) => setTtl(e.target.value)} /></label>
        <label>Idle timeout <input value={idle} placeholder="lab default, e.g. 30m" onChange={(e) => setIdle(e.target.value)} /></label>
        <label>Max extension <input value={ext} placeholder="none, e.g. 30m" onChange={(e) => setExt(e.target.value)} /></label>
        <label>Monthly budget (USD, 0 = none) <input type="number" min={0} step="0.01" value={budget} onChange={(e) => setBudget(e.target.value)} /></label>
      </fieldset>
      <button className="primary" disabled={!prog.can_manage || busy}>Save program</button>
    </form>
  )
}
```

- [ ] **Step 7: Routes, nav, styles**

`web/src/App.tsx`: import `{ TeamPage, TeamsIndex }` from './pages/Team' and `{ ProgramSettingsPage }` from './pages/ProgramSettings'; add before the `*` route:

```tsx
          <Route path="/teams" element={<TeamsIndex />} />
          <Route path="/teams/:team" element={<TeamPage />} />
          <Route path="/teams/:team/programs/:training" element={<ProgramSettingsPage />} />
```

`web/src/components/Nav.tsx`, after the Hearth link:

```tsx
      {(me.teams.length > 0 || me.is_admin) && <NavLink to="/teams">Team</NavLink>}
```

Append to `web/src/theme/app.css`:

```css
.grid { border-collapse: collapse; width: 100%; margin: 0.5rem 0 1rem; }
.grid th, .grid td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border); }
.facts { display: grid; grid-template-columns: max-content 1fr; gap: 0.3rem 1rem; }
.facts dt { color: var(--muted); }
.stack textarea, .stack input:not([type='checkbox']), .stack select { font: inherit; padding: 0.4rem; background: var(--surface-2); color: var(--text); border: 1px solid var(--border); border-radius: 6px; }
```

(`--border`, `--muted`, `--surface-2`, `--text` are the theme tokens from `web/src/theme/tokens.css`; every theme defines them.)

- [ ] **Step 8: Build, lint and test the SPA**

Run: `cd web && npm run build 2>&1 | tail -3 && npm run lint 2>&1 | tail -2 && npm test 2>&1 | tail -3`
Expected: build succeeds; oxlint `Found 0 errors`; vitest all passed.

- [ ] **Step 9: Commit**

```bash
git add web/src
git commit -m "feat(web): team page and program settings that commit to the platform repo

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 11: Web: approvals inbox, request status in the lab lobby, Forge Status (kill switch + platform settings)

**Files:**
- Create: `web/src/pages/Approvals.tsx`, `web/src/pages/ForgeStatus.tsx`, `web/src/lib/money.ts`, `web/src/lib/money.test.ts`
- Modify: `web/src/types.ts`, `web/src/App.tsx`, `web/src/components/Nav.tsx`, `web/src/pages/Lab.tsx` (`endMessages`, the polling effect, the lobby), `web/src/theme/app.css`

**Interfaces:**
- Consumes: `GET /api/approvals`, `POST /api/approvals/{id}` (Task 5); `View`/`ModuleLab` additions (Task 5); `GET/POST /api/admin/kill-switch` (Task 8); `GET /api/admin/platform` (Task 9); `/api/me` `can_approve`.
- Produces (selectors Task 12 relies on):
  - Nav links `Approvals` (when `me.can_approve`) → `/approvals`; `Forge Status` (when `me.is_admin`) → `/admin`
  - Approvals: each request is `<li aria-label="Request from {email}">` with `data-testid="estimate"` holding `$0.50 …`, buttons `Approve` / `Reject`, a `Note` input; empty state text `Nothing waiting. The forge is quiet.`
  - Lab lobby: start button text `Ignite the forge` / `Ignite again` (auto) or `Request approval ($0.50)` (needs approval); while pending `data-testid="request-status"` contains `Waiting for approval`; button `Withdraw request`; `blocked` text shown as a warning (e.g. `Labs are paused by an admin.`) and the start button disabled; end messages for `schedule` (`The program's schedule window closed.`) and `kill_switch` (`An admin paused all labs.`)
  - Forge Status: `data-testid="kill-switch-status"` with `Labs are running` / `Labs are paused …`; buttons `Pause all labs` / `Resume labs` (confirm dialog)

- [ ] **Step 1: Write the failing money-format test**

Create `web/src/lib/money.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { hours, usd } from './money'

describe('money and time labels', () => {
  it('formats dollars with cents', () => {
    expect(usd(0.5)).toBe('$0.50')
    expect(usd(1234)).toBe('$1234.00')
  })
  it('formats lab lengths', () => {
    expect(hours(3600)).toBe('1h')
    expect(hours(5400)).toBe('1h 30m')
    expect(hours(1800)).toBe('30m')
  })
})
```

Run: `cd web && npx vitest run src/lib/money.test.ts 2>&1 | tail -3` → Expected: FAIL (`Failed to resolve import "./money"`).

- [ ] **Step 2: Implement `web/src/lib/money.ts`**

```ts
export const usd = (n: number) => `$${n.toFixed(2)}`

export function hours(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.round((seconds % 3600) / 60)
  return [h ? `${h}h` : '', m ? `${m}m` : ''].filter(Boolean).join(' ') || '0m'
}
```

Run: `cd web && npx vitest run src/lib/money.test.ts` → Expected: `2 passed`.

- [ ] **Step 3: Types**

In `web/src/types.ts` replace `LabState`, extend `LabView` and `ModuleLab`, and add the rest:

```ts
export type LabState = 'pending_approval' | 'provisioning' | 'ready' | 'destroying' | 'destroyed' | 'failed' | 'rejected' | 'expired'
export type Tier = 'auto' | 'approver' | 'leader' | 'admin'
```

`LabView` gains `estimate_usd: number; tier: Tier; over_cap: boolean; escalate_at?: string; decided_by?: string; decision_note?: string`.
`ModuleLab` gains `estimate_usd: number; needs_approval: boolean; blocked?: string`.

```ts
export type Spend = { spent_usd: number; committed_usd: number; budget_usd: number; cap_usd: number }
export type ScheduleInfo = { name: string; text: string; open: boolean; closes_at?: string; next_open?: string }
export type RecentLab = { module: string; state: LabState; end_reason?: string; estimate_usd: number; created_at: string }
export type Approval = {
  id: string; requester: string; requester_name: string; team: string; training: string; module: string; lab_title: string
  runtime: string; hourly_usd: number; estimate_usd: number; ttl_s: number; tier: Tier; over_cap: boolean
  requested_at: string; escalate_at?: string; team_spend: Spend; program_spend: Spend; recent: RecentLab[]; schedule: ScheduleInfo
}
export type KillSwitch = { enabled: boolean; changed_by?: string; changed_at?: string }
export type AuditEntry = { at: string; actor: string; action: string; target: string; detail: Record<string, unknown>; commit_sha?: string }
export type TrainingStatus = { id: string; repo: string; branch: string; head: string; problems: string[] }
export type PlatformView = {
  platform_sha: string; platform_error?: string; synced_at: string
  cost_tiers: { auto_approve_usd: number; tier1_usd: number; tier2_usd: number } | null
  escalation_hours: number; schedules: Record<string, string>; admins: string[]; trainings: TrainingStatus[]; audit: AuditEntry[]
}
```

- [ ] **Step 4: Approvals page**

Create `web/src/pages/Approvals.tsx`:

```tsx
import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { Approval, Spend, Tier } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { hours, usd } from '../lib/money'

export const tierLabel: Record<Tier, string> = { auto: 'auto-approved', approver: 'a program approver', leader: 'the team leader', admin: 'an admin' }

export function ApprovalsPage() {
  const { data, error, reload } = useFetch<Approval[]>('/api/approvals', 15_000)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Checking the queue…" />
  return (
    <section className="page">
      <h1>Approvals</h1>
      {data.length === 0 && <p className="muted">Nothing waiting. The forge is quiet.</p>}
      <ul className="approvals">
        {data.map((a) => <ApprovalCard key={a.id} a={a} onDone={reload} />)}
      </ul>
    </section>
  )
}

function SpendLine({ s }: { s: Spend }) {
  if (!s.budget_usd) return <>{usd(s.spent_usd)} spent · no budget set</>
  return (
    <>
      <meter min={0} max={s.cap_usd || s.budget_usd} low={0.8 * s.budget_usd} high={s.budget_usd} value={s.spent_usd} /> {usd(s.spent_usd)} of{' '}
      {usd(s.budget_usd)}{s.cap_usd > s.budget_usd ? ` (cap ${usd(s.cap_usd)})` : ''} · {usd(s.committed_usd)} committed
    </>
  )
}

function ApprovalCard({ a, onDone }: { a: Approval; onDone: () => void }) {
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const decide = async (approve: boolean) => {
    setBusy(true)
    try {
      await api(`/api/approvals/${a.id}`, { method: 'POST', json: { approve, note } })
      toast(approve ? 'Approved: the lab is starting.' : 'Rejected.')
      onDone()
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="card approval" aria-label={`Request from ${a.requester}`}>
      <h2>{a.lab_title} <small className="muted">{a.team} / {a.training}</small></h2>
      <p>{a.requester_name || a.requester} <span className="muted">{a.requester}</span> · requested {new Date(a.requested_at).toLocaleString()}</p>
      <dl className="facts">
        <dt>Estimate</dt><dd data-testid="estimate">{usd(a.estimate_usd)} ({usd(a.hourly_usd)}/h × {hours(a.ttl_s)}, {a.runtime})</dd>
        <dt>Waiting for</dt>
        <dd>
          {tierLabel[a.tier]}
          {a.escalate_at && <span className="muted"> · escalates {new Date(a.escalate_at).toLocaleString()}</span>}
          {a.over_cap && <strong className="warn"> · would pass a budget cap: approving is an audited admin override</strong>}
        </dd>
        <dt>Team this month</dt><dd><SpendLine s={a.team_spend} /></dd>
        <dt>Program this month</dt><dd><SpendLine s={a.program_spend} /></dd>
        <dt>Schedule</dt><dd>{a.schedule.text}{!a.schedule.open && ' · closed now'}</dd>
        <dt>Recent labs</dt>
        <dd>{a.recent.length === 0 ? 'none' : a.recent.map((r) => `${r.module} (${r.state}${r.end_reason ? `, ${r.end_reason}` : ''})`).join(' · ')}</dd>
      </dl>
      <div className="row">
        <label>Note <input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} /></label>
        <button className="primary" disabled={busy} onClick={() => decide(true)}>Approve</button>
        <button className="ghost" disabled={busy} onClick={() => decide(false)}>Reject</button>
      </div>
    </li>
  )
}
```

- [ ] **Step 5: Forge Status page**

Create `web/src/pages/ForgeStatus.tsx`:

```tsx
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { KillSwitch, PlatformView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { toast } from '../lib/alerts'
import { usd } from '../lib/money'

export function ForgeStatusPage() {
  const plat = useFetch<PlatformView>('/api/admin/platform')
  const ks = useFetch<KillSwitch>('/api/admin/kill-switch')
  if (plat.error) return <ErrorBox error={plat.error} />
  if (!plat.data || !ks.data) return <Loader label="Reading the gauges…" />
  const p = plat.data
  const k = ks.data
  const toggle = async () => {
    const enable = !k.enabled
    if (!confirm(enable ? 'Destroy every running lab and block new requests until you resume?' : 'Let labs run again?')) return
    try {
      await api('/api/admin/kill-switch', { method: 'POST', json: { enabled: enable } })
      ks.reload()
      plat.reload()
    } catch (e) {
      toast((e as Error).message)
    }
  }
  return (
    <section className="page">
      <h1>Forge Status</h1>
      <h2>Lab kill switch</h2>
      <p role="status" data-testid="kill-switch-status" className={k.enabled ? 'error' : 'pass'}>
        {k.enabled ? `Labs are paused (by ${k.changed_by}, ${new Date(k.changed_at ?? '').toLocaleString()}).` : 'Labs are running.'}
      </p>
      <button className={k.enabled ? 'primary' : 'danger'} onClick={toggle}>{k.enabled ? 'Resume labs' : 'Pause all labs'}</button>

      <h2>Sync</h2>
      <dl className="facts">
        <dt>Platform commit</dt><dd><code>{p.platform_sha.slice(0, 12)}</code></dd>
        <dt>Last sync</dt><dd>{new Date(p.synced_at).toLocaleString()}</dd>
        {p.platform_error && (<><dt>Platform error</dt><dd className="error">{p.platform_error}</dd></>)}
      </dl>
      <table className="grid">
        <thead><tr><th>Training</th><th>Repo</th><th>Head</th><th>Problems</th></tr></thead>
        <tbody>
          {p.trainings.map((t) => (
            <tr key={t.id}>
              <td>{t.id}</td><td><code>{t.repo}</code> ({t.branch})</td><td><code>{t.head.slice(0, 7)}</code></td>
              <td>{t.problems.length === 0 ? '✓' : <ul>{t.problems.map((m) => <li key={m} className="error">{m}</li>)}</ul>}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>Platform settings</h2>
      <p className="muted">These live in the platform repo (platform.yaml, admins.yaml). Change them there; Crucible picks them up on the next sync.</p>
      <dl className="facts">
        <dt>Cost tiers</dt>
        <dd>{p.cost_tiers ? `auto ≤ ${usd(p.cost_tiers.auto_approve_usd)} · approver ≤ ${usd(p.cost_tiers.tier1_usd)} · leader ≤ ${usd(p.cost_tiers.tier2_usd)} · admin above` : 'not set'}</dd>
        <dt>Escalation</dt><dd>after {p.escalation_hours} business hours per tier</dd>
        <dt>Schedules</dt><dd>{Object.entries(p.schedules).map(([n, d]) => `${n}: ${d}`).join(' · ') || 'none'}</dd>
        <dt>Admins</dt><dd>{p.admins.join(', ')}</dd>
      </dl>

      <h2>Recent privileged actions</h2>
      <table className="grid">
        <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Target</th><th>Commit</th></tr></thead>
        <tbody>
          {p.audit.map((e, i) => (
            <tr key={i}>
              <td>{new Date(e.at).toLocaleString()}</td><td>{e.actor}</td><td>{e.action}</td><td>{e.target}</td>
              <td>{e.commit_sha ? <code>{e.commit_sha.slice(0, 7)}</code> : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}
```

- [ ] **Step 6: Lab lobby: request status, blocked reasons, new end messages**

In `web/src/pages/Lab.tsx`:

1. Import `{ usd }` from '../lib/money' and `{ tierLabel }` from './Approvals'.
2. Extend `endMessages`:

```ts
  schedule: "The program's schedule window closed.",
  kill_switch: 'An admin paused all labs.',
```

3. In the lab polling effect, stop polling for the final states and poll pending requests every 5 s:

```tsx
  useEffect(() => {
    if (!lab || ['destroyed', 'failed', 'rejected', 'expired'].includes(lab.state)) return
    const id = setInterval(async () => {
      try {
        setLab(await api<LabView>(`/api/labs/${lab.id}`))
      } catch { /* transient; next poll retries */ }
    }, lab.state === 'provisioning' ? 2000 : 5000)
    return () => clearInterval(id)
  }, [lab?.id, lab?.state])
```

4. In the lobby `return (…)`, add a `withdraw` handler next to `start`:

```tsx
  const withdraw = async () => {
    if (!lab) return
    try {
      setLab(await api<LabView>(`/api/labs/${lab.id}`, { method: 'DELETE' }))
    } catch (e) {
      setStartErr((e as Error).message)
    }
  }
  const pending = lab?.state === 'pending_approval'
```

and replace everything from `{lab?.state === 'failed' && …}` to the end of the start `<button>` with:

```tsx
      {lab?.state === 'failed' && <p className="error">The lab failed to start: {lab.error}</p>}
      {pending && lab && (
        <div className="request" role="status" data-testid="request-status">
          <p>
            Waiting for approval from {tierLabel[lab.tier]} · estimated {usd(lab.estimate_usd)}
            {lab.escalate_at && <> · moves up a tier at {new Date(lab.escalate_at).toLocaleString()}</>}
          </p>
          {lab.over_cap && <p className="warn">This would pass a budget cap, so only an admin can approve it.</p>}
          <button className="ghost" onClick={withdraw}>Withdraw request</button>
        </div>
      )}
      {lab?.state === 'rejected' && (
        <p className="error" data-testid="request-status">
          Your request was rejected by {lab.decided_by}{lab.decision_note ? `: “${lab.decision_note}”` : '.'}
        </p>
      )}
      {lab?.state === 'expired' && (
        <p className="muted" data-testid="request-status">
          {lab.end_reason === 'withdrawn' ? 'You withdrew your request.' : 'Nobody answered your request in time.'}
        </p>
      )}
      {!info.runtime_ready && (
        <p className="warn">
          {info.runtime_message} {info.runtime === 'local' && <Link to="/connect">Connect your laptop</Link>}
        </p>
      )}
      {info.blocked && <p className="warn">{info.blocked}</p>}
      {startErr && <p className="error">{startErr}</p>}
      {!pending && (
        <button className="primary big" disabled={!info.runtime_ready || !!info.blocked || starting} onClick={start}>
          {info.needs_approval ? `Request approval (${usd(info.estimate_usd)})` : cooled ? 'Ignite again' : 'Ignite the forge'}
        </button>
      )}
```

The `info` (ModuleLab) is fetched once; after a kill-switch change the trainee reloads the page to see the new `blocked` text (the e2e does).

- [ ] **Step 7: Routes, nav, styles**

`web/src/App.tsx`: import `{ ApprovalsPage }` from './pages/Approvals' and `{ ForgeStatusPage }` from './pages/ForgeStatus'; add routes:

```tsx
          <Route path="/approvals" element={<ApprovalsPage />} />
          <Route path="/admin" element={<ForgeStatusPage />} />
```

`web/src/components/Nav.tsx`, after the Team link:

```tsx
      {me.can_approve && <NavLink to="/approvals">Approvals</NavLink>}
      {me.is_admin && <NavLink to="/admin">Forge Status</NavLink>}
```

Append to `web/src/theme/app.css`:

```css
button.danger { background: var(--danger); color: var(--bg); border: none; font-weight: 600; }
.approvals { list-style: none; padding: 0; display: grid; gap: 1rem; }
.approval h2 small { font-weight: normal; }
.request { border: 1px solid var(--border); border-radius: 8px; padding: 0.75rem 1rem; background: var(--surface); }
meter { width: 8rem; vertical-align: middle; }
```

- [ ] **Step 8: Build, lint, test**

Run: `cd web && npm run build 2>&1 | tail -3 && npm run lint 2>&1 | tail -2 && npm test 2>&1 | tail -3`
Expected: build ok, `Found 0 errors`, vitest all passed.

- [ ] **Step 9: Commit**

```bash
git add web/src
git commit -m "feat(web): approvals inbox, request status in the lab lobby, Forge Status with the kill switch

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 12: End-to-end: enrol via the UI, approve a paid lab, pull the kill switch

**Files:**
- Create: `examples/forge-201/training.yaml`, `examples/forge-201/modules/01-paid-lab/module.yaml`, `examples/forge-201/modules/01-paid-lab/lab/{lab.yaml,compose.yaml,tasks/01-mark.md,checks/01-mark.sh}`
- Modify: `examples/platform/trainings.yaml`
- Modify: `scripts/seed-git.sh` (seed forge-201), `scripts/local-check.sh` (lint it)
- Modify: `deploy/compose/docker-compose.yml` (dev lab rate)
- Create: `e2e/tests/approvals.spec.ts`

**Interfaces:**
- Consumes: every selector listed in Tasks 10 and 11; `CRUCIBLE_DEV_LAB_USD_PER_HOUR` (Task 5); writable git mount (Task 9).
- Produces: the M3 acceptance run. Playwright runs spec files alphabetically: `approvals.spec.ts` runs before `forge-101.spec.ts` and must leave the kill switch off.

- [ ] **Step 1: Fixture training**

`examples/forge-201/training.yaml`:

```yaml
id: forge-201
title: "Forge 201: Paid Heat"
description: One small lab. The local check gives it a test price so the approval flow runs end to end.
maintainers: [senior@crucible.local]
progression: free
modules: [01-paid-lab]
```

`examples/forge-201/modules/01-paid-lab/module.yaml`:

```yaml
title: "Paid Heat"
items:
  - lab: lab
```

`examples/forge-201/modules/01-paid-lab/lab/lab.yaml`:

```yaml
id: paid-heat
runtime: local
ttl: 1h
idle_timeout: 20m
terminals:
  - { name: shell, service: shell }
tasks:
  - id: t1-mark
    instructions: tasks/01-mark.md
    check: { script: checks/01-mark.sh, run_in: shell }
```

`examples/forge-201/modules/01-paid-lab/lab/compose.yaml`:

```yaml
services:
  shell:
    image: alpine:3.22
    command: ["sleep", "infinity"]
```

`examples/forge-201/modules/01-paid-lab/lab/tasks/01-mark.md`:

```markdown
# Leave your mark

Create the file `/tmp/paid` in the shell terminal, then press **Check**.
```

`examples/forge-201/modules/01-paid-lab/lab/checks/01-mark.sh` (then `chmod +x` it):

```sh
#!/bin/sh
if [ -f /tmp/paid ]; then
  echo "Paid in full."
  exit 0
fi
echo "No /tmp/paid yet."
exit 1
```

Run: `chmod +x examples/forge-201/modules/01-paid-lab/lab/checks/01-mark.sh && go run ./cmd/crucible lint examples/forge-201`
Expected: the lint success line (same as for forge-101), exit 0.

- [ ] **Step 2: Register and seed it**

`examples/platform/trainings.yaml`:

```yaml
trainings:
  forge-101:
    repo: file:///git/forge-101.git   # local check mounts seeded bare repos at /git
    branch: main
  forge-201:
    repo: file:///git/forge-201.git   # fixture for the approvals e2e; no team is enrolled until the UI does it
    branch: main
```

`scripts/seed-git.sh`: `for name in platform forge-101 forge-201; do`.
`scripts/local-check.sh`, after `./bin/crucible lint examples/forge-101`: `./bin/crucible lint examples/forge-201`.
`deploy/compose/docker-compose.yml`, api `environment`: `CRUCIBLE_DEV_LAB_USD_PER_HOUR: "paid-heat=0.5"   # test price: makes the Forge 201 lab need approval`.

Run: `go test ./internal/config/ ./internal/rbac/ ./cmd/...`
Expected: `ok` (the example platform is still valid; nothing is enrolled in forge-201).

- [ ] **Step 3: Write the e2e spec**

Create `e2e/tests/approvals.spec.ts`:

```ts
import { expect, test, type Browser, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

// Each person gets their own browser context (own session cookie), signed in through Keycloak.
async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept()) // kill switch and "End lab" confirm
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

test('a leader enrolls a trainee via git, approves a paid lab, and an admin pauses all labs', async ({ browser }) => {
  // The leader enrolls the team in Forge 201 and the trainee in it: two bot commits to the platform repo.
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await expect(leader.getByRole('heading', { name: 'The Forge' })).toBeVisible()
  await leader.getByLabel('Training to enroll').selectOption('forge-201')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // The trainee now sees Forge 201 (sync picked up the commit), pairs the laptop and asks for the paid lab.
  const trainee = await login(browser, 'trainee')
  await expect(trainee.getByRole('link', { name: /Forge 201/ })).toBeVisible()
  await trainee.getByRole('link', { name: 'Connect your laptop' }).click()
  await trainee.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await trainee.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080'], { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })
  await expect(trainee.getByRole('status').filter({ hasText: /Agent connected/ })).toBeVisible({ timeout: 30_000 })
  await trainee.goto('/p/forge/forge-201/m/01-paid-lab/lab')
  await trainee.getByRole('button', { name: 'Request approval ($0.50)' }).click()
  await expect(trainee.getByTestId('request-status')).toContainText('Waiting for approval')

  // The leader (default approver, $0.50 is within tier 1) approves from the inbox.
  await leader.getByRole('link', { name: 'Approvals' }).click()
  const card = leader.getByRole('listitem', { name: 'Request from trainee@crucible.local' })
  await expect(card.getByTestId('estimate')).toContainText('$0.50')
  await card.getByRole('button', { name: 'Approve' }).click()
  await expect(leader.getByText('Nothing waiting. The forge is quiet.')).toBeVisible()

  // The approved lab starts on the trainee's laptop (the lobby polls the pending request).
  await expect(trainee.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })

  // An admin pulls the kill switch: the lab dies and new requests are blocked.
  const admin = await login(browser, 'admin')
  await admin.getByRole('link', { name: 'Forge Status' }).click()
  await admin.getByRole('button', { name: 'Pause all labs' }).click()
  await expect(admin.getByTestId('kill-switch-status')).toContainText('Labs are paused')
  await expect(trainee.getByText('An admin paused all labs.')).toBeVisible({ timeout: 60_000 })
  await trainee.reload()
  await expect(trainee.getByText('Labs are paused by an admin.')).toBeVisible()

  // Resume so the rest of the suite can run labs.
  await admin.getByRole('button', { name: 'Resume labs' }).click()
  await expect(admin.getByTestId('kill-switch-status')).toContainText('Labs are running')
})
```

- [ ] **Step 4: Run the full local check**

Run: `KEYCLOAK_PORT=8082 make local-check 2>&1 | tail -8`
Expected: Playwright reports `2 passed` and the script ends with `🔥 Local check passed. The forge holds.`

If it fails, rerun with `KEEP=1 KEYCLOAK_PORT=8082 make local-check`, inspect `e2e/test-results/*/trace.zip` (`npx playwright show-trace …`) and `docker compose -f deploy/compose/docker-compose.yml logs api`, fix, and run again. A push failure in the api log (`git push … permission denied`) means the bare repo is not writable: check Step 11 of Task 9 (`chmod -R a+rwX`, no `:ro`).

- [ ] **Step 5: Final hygiene**

Run: `gofmt -l . ; go vet ./... ; go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add examples scripts deploy/compose/docker-compose.yml e2e/tests/approvals.spec.ts
git commit -m "test(e2e): enrol via git, approve a paid lab and pull the kill switch

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage (M3)

| Spec item | Task |
|---|---|
| §2 cost tiers with no defaults; escalation 4 business hours | 2, 5, 6 |
| §3 River job queue (sweeps, escalations, notifications) | 3, 4, 6 (escalation runs in the River-driven sweep) |
| §4.1 `budget.yaml`, team webhooks; §4.3 program `schedule`, `lab_defaults`, `budget_usd_month`, roles written by the UI | 2, 9, 10 |
| §5.3 enrol / roles / schedule / TTL (leader, manager); team membership (leader); approve within tier; nobody approves own | 5, 9 |
| §6 config write-back: bot commit, `crucible: … by …`, `Crucible-Actor:`, retry ×3, conflict message | 9 |
| §8.1 requested / pending_approval / approved / rejected / expired | 5, 6 |
| §8.5 maintainers notified on setup failure | 4 |
| §8.6 effective end includes the schedule window; "Ends at schedule close"; extensions never past the window | 7 (timer label already exists) |
| §9.1 estimate, tiers, approver context, hard cap → admin override, escalation | 5, 6, 8 |
| §9.2 schedules, budgets 80%/100%, kill switch | 7, 8 |
| §10 email + Slack/Teams, events, per-kind email mutes | 4 (submission/rank-up kinds arrive with M5/M7) |
| §12 nav: Team, Approvals (Anvil/Ledger come in M5/M6), Forge Status | 10, 11 |
| §13 `audit_log`, notifications | 3, 4 |
| §14 re-request after a failed start within 1 h needs no approval; config conflict message; privileged actions audited | 5, 9 |
| Carry-overs: local compose allowlist; one agent per user | 1 |

**Deliberately not in M3 (with where they land):** inline schedule windows in program files (named schedules only); the "Extension pending" re-approval flow (M6, with real cloud costs); budget limit on the lab timer (M6, AWS hourly projection); `cost_samples`/`cost_actuals` (M6); provisioning as a River job (M4); pinned-ref bump UI with diff (M7, spec §6); editing platform.yaml/admins.yaml from the UI (git only, by design).
