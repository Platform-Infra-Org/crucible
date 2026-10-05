# Crucible — Design Spec (DRAFT)

**Date:** 2026-10-05
**Status:** Draft — all grilling questions resolved; awaiting final spec review.

> *"The crucible doesn't break the metal. It reveals it."*

---

## 1. Purpose

Crucible trains new coworkers from zero to hero. It delivers **reading material**, **quizzes** (instantly scored or human-scored), and **hands-on labs** (local Docker, in-cluster Docker, or AWS), all authored as **structured git repos**. It enforces **FinOps** controls on lab spend and **RBAC** across global admins, teams, and per-training roles. The UI carries a forge/crucible motif: themes, molten animations, rank progression, quotes on loading screens.

### Goals
- Anyone with repo access can add a training, reading, quiz, or lab by pushing to git.
- Trainees get a KodeKloud-style lab experience: tasks/quizzes left, tabbed terminals right, "Check" button per task.
- No cloud lab runs without cost-aware approval; every lab dies on TTL/idle/schedule.
- The whole platform can be torn down and rebuilt from git + a DB snapshot.

### Non-goals (v1)
- Competitions / leaderboards between trainees (explicitly rejected — bad for training).
- External "overseer" role (dropped).
- Cloud providers other than AWS. Multi-tenant (multi-org) hosting.
- PR integration with specific git hosts (works with any plain git server).

---

## 2. Decisions log (from grilling session)

| Topic | Decision |
|---|---|
| Scale | < 100 users, single org |
| Hosting | Kubernetes (Helm chart) |
| SSO | Generic OIDC (Keycloak or any compliant IdP) |
| Source of truth | Config + content in **git**; runtime data in **Postgres** |
| UI → git writes | Config changes = direct bot commit; content edits = Crucible-native review then merge |
| Git host | Generic git (SSH/HTTPS), polling + optional webhook |
| Repo layout | 1 platform repo + N content repos |
| Per-training roles | Assigned in UI (with defaults), persisted to platform repo |
| Trainings scope | Global catalog; enrollment, scoring, approvals per team |
| Lab approval | Cost-tiered |
| Approval timeout | Escalate after N hours |
| Lab runtimes | Per lab: `cluster` (sysbox pod), `local` (trainee laptop via agent), `aws` |
| AWS isolation | One shared account, tag + IAM permission boundary |
| Lab checks | Script per task, exit 0 = pass, stdout = feedback |
| Lab timer & idle | Countdown to the earliest of TTL / schedule end / budget cap; "Are you still there?" modal before idle destroy; browser notifications when tab is hidden |
| Task setup | Optional per-task and lab-level setup scripts (break-fix scenarios), run out of band on first task open |
| Progression | Configurable per training: `linear` (gated) or `free` |
| Instant quiz types | single/multi choice, exact/regex, ordering/matching, terminal-inspection |
| Human-scored items | free text, file/link uploads, lab submissions (incl. override), live sign-offs |
| FinOps v1 | TTL + idle destroy, schedules, budgets + hard caps, infracost estimate, in-app dashboard |
| Teardown | Global lab kill switch **and** full platform hibernate/restore |
| Extras approved | Forge ranks, `crucible lint`/preview CLI, email + Slack/Teams notifications, mentor pairing + journey view |
| Themes | Built-in set, per-user choice, admin default |
| Motion | Rich, fully disabled under `prefers-reduced-motion` or user toggle |
| Release | Everything in one release (built in dependency order, §15) |
| Architecture | Go modular monolith + React SPA (§3); Educates rejected (§3) |
| Git access | All repos on one git server, one bot credential |
| Score privacy | Trainees cannot see other trainees' scores |
| Cluster | Bring-your-own cluster supported; optional shipped EKS terraform module |
| Snapshots | Separate persistent S3 bucket (own terraform stack, never touched by teardown) |
| Escalation default | 4 business hours (counted inside schedule window) |
| Cost tiers | No defaults — set at install in `platform.yaml` |
| Forge ranks | % of assigned (enrolled) training completed |

---

## 3. Architecture

```
                 ┌───────────────────────── Kubernetes ─────────────────────────┐
 Browser ──HTTPS─┤  crucible-web (React SPA, static)                            │
   │             │  crucible-api (Go monolith) ── Postgres                       │
   │  WS (PTY)   │     modules: auth · rbac · gitsync · content · assessment ·   │
   └─────────────┤              labs · finops · notify · journey · platform      │
                 │     River job queue (in Postgres)                             │
                 │  lab namespaces: lab-<id> (sysbox pods, workspace pods)       │
                 │  terraform runner Jobs (AWS labs)                             │
                 └───────────────────────────────────────────────────────────────┘
 Trainee laptop: crucible-agent ──outbound WS──► crucible-api
 Git server ◄── bot (clone/pull/push) ── gitsync
 AWS shared lab account ◄── terraform Jobs / Cost Explorer
 Keycloak / OIDC IdP ◄── auth
```

- **One Go binary** (`crucible-api`) with internal modules that talk only through Go interfaces; each module owns its tables. Monolith because <100 users; module boundaries let us split later if ever needed.
- **Postgres** for runtime data; **River** (Postgres-backed job queue) for async work: sync, provisioning, TTL sweeps, escalations, notifications, cost ingestion.
- **React + Vite SPA**, xterm.js for terminals, Framer Motion for animation, CSS custom-property design tokens for themes.
- **Lab runners** behind one interface:
  ```go
  type Runner interface {
      Provision(ctx, lab LabSpec, inst Instance) error
      Terminals(inst Instance) []TerminalSpec
      OpenPTY(ctx, inst Instance, terminal string) (PTY, error)
      RunScript(ctx, inst Instance, s ScriptSpec, env map[string]string) (ScriptResult, error) // checks + setups, out of band
      Destroy(ctx, inst Instance) error
  }
  ```
  Implementations: `ClusterRunner`, `LocalAgentRunner`, `AWSRunner` (AWSRunner = terraform Job + a `ClusterRunner` workspace pod for the terminals).
- **CLIs** (Go, single binaries): `crucible` (lint, preview, hibernate, up) and `crucible-agent` (local labs).

Alternatives considered:
- **Microservices** — ops overhead without benefit at this scale.
- **Educates as the `cluster` runner** — Educates (Apache-2.0) provides isolated per-user K8s sessions, docker-in-session, an examiner (check scripts), a portal REST API, and iframe embedding. But it covers only one of the three runtimes, and has no quizzes/stored scores, team RBAC, approvals, or FinOps. The `local` runner forces us to build the PTY proxy + xterm tabbed terminal UI anyway, so Educates would save only the namespace/sysbox provisioning — while adding a second system to operate, a lab.yaml → Workshop CRD translation, and an iframe UI that resists the forge theme and our left-panel task/check/quiz flow. Rejected; revisit only if the cluster runner proves harder than expected.

---

## 4. Repositories

### 4.1 Platform repo (one)
```
crucible-platform/
  platform.yaml          # org settings: default theme, escalation hours, cost tiers, rank thresholds, notification webhooks, snapshot bucket
  admins.yaml            # global admins (by OIDC email/subject)
  quotes.yaml            # extra loading-screen quotes (built-ins always present)
  trainings.yaml         # registry: training id → content repo URL + branch
  teams/
    <team-id>/
      team.yaml          # name, leader, seniors, members, trainees, mentor pairs
      budget.yaml        # monthly budget + hard cap (USD), per-training sub-budgets
      programs/
        <training-id>.yaml   # team's enrollment of a global training (see 4.3)
```

### 4.2 Content repo (one per training)
```
<training-repo>/
  training.yaml          # id, title, description, maintainers, progression: linear|free, modules order, pass thresholds, estimated hours
  modules/
    01-intro/
      module.yaml        # title, items order, completion rule
      reading/*.md       # Markdown (+ mermaid, code blocks, images in assets/)
      quiz.yaml          # questions (see 4.4)
    02-docker-basics/
      module.yaml
      lab/
        lab.yaml         # runtime, terminals, TTL, tasks (see 4.5)
        compose.yaml     # for runtime cluster|local
        terraform/       # for runtime aws
        tasks/01-*.md    # task instructions
        checks/01-*.sh   # check scripts
  assets/
```

### 4.3 Program file (team × training) — written by UI, readable by humans
```yaml
training: k8s-fundamentals
pinned_ref: 3f2c9ab            # content commit trainees run; manager bumps it in UI
roles:
  manager:  [alice@corp]       # defaults: team leader
  scorers:  [bob@corp, carol@corp]   # defaults: seniors
  approvers: [alice@corp]      # defaults: team leader
enrolled: [dave@corp, erin@corp]
schedule: business-hours       # named schedule from platform.yaml, or inline windows
lab_defaults: { ttl: 4h, idle_timeout: 45m, max_extension: 1h }
budget_usd_month: 150
```

### 4.4 Quiz schema (excerpt)
```yaml
pass_threshold: 0.8
questions:
  - id: q1
    type: single            # single | multi | exact | regex | order | match | terminal | text | upload | signoff
    prompt: "Which command lists running containers?"
    options: ["docker ps", "docker ls", "docker run"]
    answer: 0
    points: 1
  - id: q2
    type: terminal          # answered by inspecting the lab; validated by a check script
    prompt: "What port is the nginx container listening on?"
    check: checks/q2.sh     # receives answer in $CRUCIBLE_ANSWER
  - id: q3
    type: text              # human-scored
    prompt: "Explain when you'd choose a StatefulSet over a Deployment."
    rubric: "Mentions stable identity, ordered rollout, persistent volumes."
    points: 5
```
`text`, `upload`, `signoff` → human-scored. Everything else → instant.

### 4.5 Lab manifest
```yaml
id: docker-networking
runtime: cluster            # cluster | local | aws
ttl: 2h
idle_timeout: 30m
idle_warning: 5m            # "Are you still there?" shown this long before idle destroy
task_order: linear         # linear (default) | free
terminals:                  # one tab each
  - { name: host,  service: workstation }
  - { name: db,    service: postgres }
tasks:
  - id: t1
    instructions: tasks/01-create-network.md
    check: { script: checks/01.sh, run_in: workstation, timeout: 30s }
    points: 2
    hints:                  # revealed one at a time, in order (see §8.4)
      - text: "Look at `docker network --help`."
      - text: "You need the `create` subcommand with `--driver bridge`."
        cost: 0.5           # points deducted if revealed (default from lab-level hint_cost, else 0)
      - file: hints/01-solution.md   # final hint may be the full solution
        cost: 1
  - id: t2
    instructions: tasks/02-inspect.md
    quiz: q2                # inline terminal quiz in the task panel
  - id: t3
    instructions: tasks/03-design.md
    human_review: true      # scorer reviews / may override
  - id: t4                  # break-fix task (see §8.5)
    instructions: tasks/04-fix-nginx.md
    setup: { script: setup/04-break-nginx.sh, run_in: workstation, timeout: 60s }
    check: { script: checks/04.sh, run_in: workstation, timeout: 30s }
    points: 3
# aws only:
aws:
  region: eu-west-1
  max_hourly_usd: 0.50      # lint fails if infracost estimate exceeds this
```

---

## 5. Identity & RBAC

### 5.1 Authentication
OIDC authorization-code + PKCE against the configured IdP. Users are auto-provisioned on first login; identity key = OIDC `sub`, matched to git config by email. `crucible-agent` uses OIDC device-code flow. A bootstrap admin email in Helm values exists only to seed `admins.yaml` on first start.

### 5.2 Roles
- **Global:** `admin` — everything, including kill switch, hibernate, budgets, cross-team views.
- **Team:** `leader`, `senior`, `member`, `trainee` (one role per team; a user may be in multiple teams).
- **Per program (team × training):** `manager`, `scorer`, `approver`. Defaults on enrollment: leader → manager + approver; seniors → scorers. Overridable in UI → committed to the program file.
- **Mentor:** a pairing (senior/member → trainee) in `team.yaml`, not a role.

### 5.3 Permission matrix (team scope)

| Action | admin | leader | senior | member | trainee | program manager | scorer | approver |
|---|---|---|---|---|---|---|---|---|
| Edit team membership / mentors | ✓ | ✓ | | | | | | |
| Enroll team in training, set program roles | ✓ | ✓ | | | | ✓ | | |
| Bump pinned content ref, set schedule/TTL | ✓ | ✓ | | | | ✓ | | |
| Take training / request lab | ✓ | ✓ | ✓ | ✓ | ✓ | | | |
| Score human items | ✓ | | | | | | ✓ | |
| Approve lab request (within tier) | ✓ | tier 2 | | | | | | tier 1 |
| View trainee progress | ✓ | ✓ | ✓ | | own | ✓ | ✓ | |
| View team spend | ✓ | ✓ | | | | ✓ | | ✓ |
| Review/merge content edits | ✓ | | | | | | | |

Content edits are also reviewable by the training's `maintainers` (from `training.yaml`), regardless of team role.

Rules: nobody approves their own lab request or scores their own submission. A trainee sees only their own scores; mentors see their mentees.

---

## 6. Git sync & content pipeline

- **gitsync** keeps a bare mirror of the platform repo and each registered content repo. Triggers: poll every 60 s, plus `POST /api/git/hook/<repo>` (any host's webhook, shared secret).
- On new commit: parse + validate (same validator as `crucible lint`). **Valid →** store as a new content version (by SHA). **Invalid →** keep previous active version, show errors on the admin *Forge Status* page, notify maintainers.
- Programs pin a SHA. In-progress attempts finish on their pinned version; manager bumps via UI (diff summary shown first).
- **Config writes (UI → platform repo):** bot commits directly with message `crucible: <action> by <user email>` and a `Crucible-Actor:` trailer. Optimistic concurrency: fetch → apply → push; on non-fast-forward, rebase and retry up to 3×; a true conflict returns an error to the user. DB caches config but git is authoritative — a manual git edit wins on next sync.
- **Content edits from UI:** pushed to branch `crucible/edit/<edit-id>`; Crucible shows rendered preview + diff; a training maintainer other than the author approves → bot merges to the tracked branch (merge commit). Merge conflict → edit marked *stale*, author re-edits on fresh base.
- **`crucible lint <repo>`**: schema validation, broken links/assets, referenced scripts exist and are executable, check scripts `shellcheck`ed, quiz answers within option ranges, AWS labs pass infracost against `max_hourly_usd`.
- **`crucible preview <repo>`**: runs the SPA + a local content server against the working tree (reading, quizzes, and `local`-runtime labs).

---

## 7. Learning & assessment

- **Training → modules → items** (reading, quiz, lab). `progression: linear` locks module N+1 until module N meets its completion rule (`all_items` or `score >= threshold`); `free` locks nothing.
- **Reading:** Markdown rendered with syntax highlighting, mermaid, callouts; "mark as read" + scroll tracking.
- **Instant quizzes:** scored server-side; answers never sent to the client before submission. Attempts limit and cooldown configurable per quiz (default unlimited, no cooldown); best score counts.
- **Human scoring:** submissions land in the program's **Scoring Queue** (Anvil view): filter by training/trainee/type, rubric shown, score + written feedback, return-for-rework. Lab submissions show the check results, a terminal transcript (recorded PTY output), and uploaded artifacts; scorers can override auto-check results with a reason (audited). Live **sign-offs** are created by a scorer ("Mark passed after live demo") with notes.
- **Forge ranks:** rank = % of the trainee's enrolled programs completed (weighted by item points) — **Ore 0% → Ingot 20% → Tempered 45% → Blade 75% → Master Smith 100%** (defaults; overridable in `platform.yaml`). Enrolling in a new program can lower the %, but a rank once earned is never lost. Per-training completion grants a badge. Rank-up triggers the hammer-strike animation. Ranks are personal; no leaderboards.

---

## 8. Labs

### 8.1 Lifecycle
```
requested ─► pending_approval ─► approved ─► provisioning ─► ready ─► (expiring) ─► destroying ─► destroyed
     │              │  └─escalated─┘                │                                   ▲
     └─auto-approved┘ └─► rejected / expired        └─► failed ─────────────────────────┘
```
Every transition is a River job, idempotent, recorded in `lab_events`. `failed` always attempts destroy; stuck destroys alert admins.

### 8.2 Runtimes
- **cluster:** namespace `lab-<id>` with ResourceQuota + default-deny NetworkPolicy (egress allowlist from lab.yaml). One pod using the **sysbox** runtime class runs dockerd and `docker compose up` on the lab's compose file. Terminals = `docker exec` into named services, proxied over WebSocket. Checks run via exec in `run_in` service — tamper-resistant.
- **local:** trainee installs `crucible-agent` (macOS, Linux, Windows/WSL2; requires Docker). Agent logs in (device code), holds an outbound WebSocket to the API, receives the compose bundle, runs it, streams PTYs, runs checks. Check results from local labs are flagged **self-reported**; a program can require human review for them. No cloud cost → always auto-approved.
- **aws:** a terraform runner Job (`terraform apply` with the lab's module, state in S3 keyed by lab id) plus a cluster **workspace pod** (aws cli, terraform, kubectl…) for the terminals. Credentials: STS `AssumeRole` into a lab role with **permission boundary** and session tag `crucible:lab-id=<id>`; IAM conditions require that tag on create (`aws:RequestTag`) and on modify/delete (`aws:ResourceTag`). Provider `default_tags` add `crucible:lab-id`, `crucible:team`, `crucible:training`. Destroy = `terraform destroy`, then a tag sweep via Resource Groups Tagging API. A nightly **reaper** lists all tagged resources with no live lab and deletes them, and reports untagged resources created by lab roles (CloudTrail) to admins. Known limitation: shared-account isolation is best-effort — services without tag-condition support are excluded from lab roles by the boundary.

### 8.3 Lab UI (KodeKloud-style)
- **Left panel (≈40%):** task list with status pips; current task's Markdown; inline terminal-quiz input; **Check** button (spinner → spark burst on pass, shake + feedback on fail); **Hint** button (§8.4).
- **Header bar:** lab timer (§8.6), lab name, runtime badge, "End lab" button.
- **Right panel:** xterm.js terminal with **tabs**, one per `terminals` entry (plus "+" for extra shells on the same service); reconnect on drop; copy/paste; font size control.
- Resizable splitter; collapse left panel; full-screen terminal mode.
- Loading/provisioning screen: molten crucible animation + rotating quotes + live provisioning log stream.

### 8.4 Hints
- Each lab task may define an ordered list of **hints** (inline `text` or a Markdown `file`) in `lab.yaml`; authors typically escalate from a nudge to the full solution.
- The **Hint** button reveals the next hint only; earlier hints stay visible. Before revealing a hint with a cost, the UI shows a confirm: "This hint costs 0.5 points."
- **Cost:** per hint `cost`, falling back to lab-level `hint_cost`, else 0. Deductions apply to the task's points and never go below 0.
- Hint text is fetched from the server on reveal, never shipped to the client in advance.
- Every reveal is recorded in `hint_reveals`. Scorers see hints used next to check results; the journey view treats a revealed final hint as a "stuck" signal.
- `crucible lint` checks that hint files exist and that each cost is ≥ 0 and ≤ the task's points.

### 8.5 Task setup scripts (break-fix and scenario prep)
- Any task may declare `setup: { script, run_in, timeout }`. The script prepares the scenario for that task, e.g. corrupting a config, stopping a service, filling a disk, or deleting a route. The trainee's job is to diagnose and fix it, and the task's check script verifies the fix.
- A lab may also declare a lab-level `setup` that runs once, after the lab reaches `ready` and before the trainee gets the terminals.
- **When a task's setup runs:** the first time the trainee opens that task. In a lab with `task_order: linear` (the default), a task opens only after the previous one passes, so breakage never collides with earlier work. With `task_order: free`, each task's setup still runs on first open, and authors must make setups independent of each other.
- **Execution:** like checks, setup runs out of band (exec for `cluster`, agent for `local`, the workspace pod with lab credentials for `aws`), never inside the trainee's terminal session. Its output is hidden from the trainee and stored for scorers/maintainers. The task panel shows "Preparing scenario…" with the forge loader until it finishes; the Check button is disabled meanwhile.
- **Rules for authors:** setup must be idempotent (safe to re-run) and must exit 0 on success.
- **Failure:** a non-zero exit or timeout is retried once. If it still fails, the task is marked *setup failed*, the trainee sees "This scenario couldn't be prepared", maintainers are notified, and the trainee can skip the task without penalty or restart the lab.
- **Reset scenario:** a per-task button re-runs the task's setup (only for tasks with `setup`, once per 5 min). It's for when the trainee has made things worse; it does not undo other changes, so authors should write setups that converge to the broken state rather than assume a clean one.
- **Integrity:** setup scripts are never sent to the browser. In `local` labs the trainee could read them on their own machine, which is acceptable because local results are already flagged self-reported.
- `crucible lint` checks that setup scripts exist and are executable, and that `run_in` names a service or terminal defined in the lab. `crucible preview` runs setups so authors can test their break-fix scenarios locally.

### 8.6 Lab timer and idle check
**Timer**
- Always visible in the lab header. It counts down to the lab's **effective end** = the earliest of: TTL expiry, the end of the program's schedule window, and the team/program hard budget cap being reached (AWS labs, projected from the hourly estimate).
- A tooltip/label says which limit applies ("Ends at schedule close, 19:00").
- The server is authoritative: the client receives `ends_at` plus the server time and corrects for clock skew; the countdown re-syncs on every WebSocket reconnect. Multiple open tabs show the same value.
- **States:** normal (> 15 min) → **cooling** (≤ 15 min: amber, ember glow) → **critical** (≤ 5 min: red, slow pulse). Under reduced motion the colours change without the glow/pulse.
- **Warnings** at 15 and 5 min: an in-page toast, plus a browser notification and a title-bar badge ("⏳ 5 min · Crucible") if the tab is in the background. Browser notification permission is requested on the trainee's first lab, with an explanation; denying it only drops the background notification.
- **Extend:** a button beside the timer while extension is allowed (once per lab, up to the program's `max_extension`, never past the schedule window or hard cap). Extensions that push the estimate over the approver's tier go back through approval, and the timer shows "Extension pending".
- **At zero:** terminals freeze, a "The forge has cooled" screen appears with a summary (tasks passed, points, hints used), and the lab is destroyed. Scores and task results are kept. Requesting the same lab again creates a fresh environment; passed tasks stay passed, and the trainee resumes at the first unfinished task (its setup runs again).

**Idle check ("Are you still there?")**
- **Activity** = keystrokes in any terminal, Check/Hint/quiz actions, or task-panel interaction (scroll, task switch) — tracked client-side and sent as a heartbeat at most once a minute. Merely having the tab open is not activity. A long-running command producing output does not count either, so authors of labs with long waits should raise `idle_timeout`.
- When `idle_warning` (default 5 min) remains before `idle_timeout`, a modal appears: **"Are you still there? The forge is cooling…"** with a countdown and an **"I'm here"** button. If the tab is in the background, the same browser notification and title badge are used.
- "I'm here" (or any activity) resets the idle clock and dismisses the modal in all open tabs.
- If the countdown runs out: `cluster`/`aws` labs are destroyed exactly as at TTL zero, with "Your lab was closed after N minutes of inactivity". `local` labs have their containers stopped (no cloud cost, but frees the trainee's machine).
- Idle and timer warnings are in-app/browser only. No email or Slack, since they would arrive too late to act on.
- Lab-level settings in `lab.yaml`: `idle_timeout`, `idle_warning`; program-level `max_extension` in the program file. Lint rejects `idle_warning >= idle_timeout`.

---

## 9. FinOps

### 9.1 Request & approval
1. Trainee requests a lab → Crucible computes **estimate**: `cluster` = pod resources × internal rate card from `platform.yaml`; `aws` = infracost on the module × requested TTL; `local` = $0.
2. **Cost tiers** (`platform.yaml`; no built-in defaults — install fails validation until set):
   - `local`, `cluster` under `auto_approve_usd` → auto-approved.
   - Estimate ≤ `tier1_usd` → program **approver**.
   - Estimate ≤ `tier2_usd` → team **leader**.
   - Above → global **admin**.
3. Approver sees: estimate, the team's month-to-date spend vs budget, the program's spend, the trainee's recent lab history, the active schedule window.
4. **Hard cap:** if estimate would exceed team or program monthly hard cap → request blocked; only admin can override (audited).
5. **Escalation:** unanswered after `escalation_hours` (default **4 business hours**, counted only inside the program's schedule window) → escalates one tier up (approver → leader → admin) with notification; trainee sees status throughout.

### 9.2 Cost controls
- **TTL** (hard max lifetime) and **idle timeout** per lab, surfaced to the trainee as the lab timer and the "Are you still there?" prompt (§8.6).
- **Schedules:** named windows (e.g. `business-hours: Mon–Fri 08:00–19:00 Europe/Bucharest`). Labs can't be requested outside windows; running labs are destroyed at window end (warned 15 min ahead).
- **Budgets:** monthly per team and per program; 80% alert, 100% hard cap.
- **Global kill switch** (admin): destroys all running labs immediately and blocks new requests until re-enabled.

### 9.3 Cost data & dashboard
- Live spend = runtime estimates per running lab. Actual AWS spend = Cost Explorer daily ingestion grouped by cost-allocation tags (`crucible:*`), reconciled against estimates (~24 h lag shown explicitly).
- **FinOps page** (themed, in-app): spend by team / training / lab over time, budget burn-down, running labs with cost-so-far and time-to-death, top spenders, estimate-vs-actual accuracy, reaper findings.

### 9.4 Hibernate & restore
- **Sleep (in-app, admin or schedule):** kill switch → `pg_dump` to the snapshot bucket → scale API/web to zero, leaving a tiny `crucible-waker` pod serving the "The forge is cold" page with a **Wake** button (admin login) → scale back up.
- **Cluster ownership — both supported:** Crucible installs via Helm into any cluster (bring-your-own). Optionally, `deploy/eks/` terraform creates a dedicated EKS cluster.
- **Persistent stack:** `deploy/persistent/` terraform creates the snapshot S3 bucket (versioned, KMS-encrypted) and terraform-state bucket once; teardown never touches it.
- **Full teardown (CLI, run outside the cluster):** `crucible hibernate --full` = sleep + `helm uninstall` + (if the EKS module was used) `terraform destroy` of `deploy/eks/`. On bring-your-own clusters it removes Crucible, lab namespaces and its CRDs/PVCs only.
- **Restore:** `crucible up` = (EKS mode: terraform apply) → helm install → restore latest snapshot from the persistent bucket → gitsync. Config and content come from git, so only the snapshot is needed for progress data.
- Snapshots also run nightly regardless (retain 30 days).

---

## 10. Notifications
Channels: email (SMTP) and Slack/Teams incoming webhooks (per team in `team.yaml`). Events: lab request pending / escalated / approved / rejected; submission awaiting scoring; scored/returned; budget 80%/100%; content sync failure; rank-up (to trainee + mentor). Users can mute per event type (email) in settings.

## 11. Mentor & journey view
- Each trainee may have a mentor (in `team.yaml`). Mentor dashboard: mentees' progress, pending submissions, recent lab failures.
- **Journey view** (leader/mentor/manager): per trainee, a module-by-module **heat map** — cold (not started) → glowing (in progress) → forged (completed) — with "stuck" flags: ≥3 failed checks on the same task, the final (solution) hint revealed, no activity for 5 business days, or a submission returned twice.

---

## 12. UI & UX

- **Themes** (design tokens via CSS custom properties; per-user choice, admin default):
  - **Forge** — dark charcoal, ember orange/molten gold accents (default)
  - **Anvil** — light steel greys, iron blue accents
  - **Quench** — deep navy, cool cyan accents
  - **High Contrast** — WCAG AAA
- **Motion:** molten-metal fill progress bars, ember particles on the loading screen, spark burst on a passed check, hammer strike + glow on rank-up, heat-shimmer on hovering cards. All motion off under `prefers-reduced-motion` or the user's "Calm forge" toggle; essential state changes still show statically.
- **Quotes:** loading and provisioning screens rotate quotes (built-in set + `quotes.yaml`), e.g. *"Steel is forged in fire."*, *"Pressure makes diamonds; heat makes blades."*
- **Navigation:** Hearth (home/dashboard) · Trainings · Labs · Anvil (scoring queue) · Ledger (FinOps) · Forge Status (admin) · Team.
- **Accessibility:** keyboard-navigable everything (including terminal tab switching), focus rings, ARIA live regions for check results, contrast checked per theme.

## 13. Data model (Postgres, core tables)
`users`, `teams_cache`, `programs_cache` (git mirrors, rebuildable) · `content_versions` (repo, sha, parsed manifest JSON, valid, errors) · `enrollments` · `item_progress` · `quiz_attempts` · `submissions` (type, payload, files → object storage) · `scores` (auto/human, scorer, override reason) · `lab_instances` (runtime, state, program, requester, ttl, estimate) · `lab_requests` + `approvals` (tier, approver, escalations) · `lab_events` · `check_runs` (stdout, exit code, self_reported) · `hint_reveals` (lab instance, task, hint index, cost, timestamp) · `setup_runs` (lab instance, task or lab-level, attempt, exit code, output, duration) · `terminal_transcripts` (object storage ref) · `cost_samples` (estimates) + `cost_actuals` (Cost Explorer) · `notifications` · `audit_log` (every privileged action, git commit SHA where applicable) · `ranks`.
Files (uploads, transcripts, snapshots) → S3-compatible object storage.

## 14. Errors, security, testing

**Errors**
- Provisioning failure → `failed`, automatic destroy, trainee sees the log tail and can re-request (no re-approval if within 1 h).
- gitsync failure → previous version stays live; Forge Status shows the error; maintainers notified.
- Git push conflict on config → user sees "Someone changed this in git, reload".
- Agent disconnect → terminals show reconnecting; lab keeps running until idle/TTL.
- Cost Explorer unavailable → dashboard marks actuals stale; estimates still enforce caps.

**Security**
- Check scripts and lab containers never run in the API pod; cluster labs are namespace-isolated with quotas and network policies; sysbox (no privileged pods).
- AWS lab credentials are short-lived (1 h, auto-refreshed), scoped by permission boundary; never exposed to the browser except inside the workspace pod's shell.
- Quiz answers and check scripts are never sent to the client.
- Bot git credential and IdP client secret in K8s Secrets; all privileged actions in `audit_log`.
- Local-runtime results are marked self-reported.

**Testing**
- Go unit tests per module; integration tests with Postgres (testcontainers); runner tests against kind + sysbox; AWS runner tested against a real sandbox account in a nightly job (LocalStack for fast CI).
- `crucible lint` runs against a fixture content repo with known-good and known-bad cases.
- Frontend: component tests (Vitest) + Playwright e2e for: SSO login, read → quiz → lab → check pass, approval flow, scoring queue.
- Ship a **sample content repo** ("Forge 101": Docker basics, one cluster lab, one local lab, one AWS lab) that doubles as the e2e fixture and the authoring example.

## 15. Build order (single release, dependency-ordered)
1. Skeleton: Go API, Postgres, River, React shell, themes/tokens, OIDC login, Helm chart.
2. gitsync + platform repo config + RBAC + config write-back.
3. Content model + `crucible lint`/`preview` + sample repo + reading + instant quizzes + progression.
4. Cluster runner + lab UI (tasks/terminal tabs/checks) + TTL/idle.
5. Approvals (tiers, escalation) + schedules + kill switch + notifications.
6. Human scoring queue + submissions + sign-offs.
7. Local agent runner.
8. AWS runner + infracost + reaper + Cost Explorer ingestion + budgets/caps + FinOps page.
9. Hibernate/restore + snapshots + `deploy/` terraform.
10. Forge ranks, mentor/journey view, content edit review flow, motion polish, quotes.

## 16. Open questions
None — all resolved in the decisions log (§2). Cost tier values are chosen at install time.
