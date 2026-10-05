# Crucible Implementation Roadmap

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md`

The spec asks for one release containing everything (§2, "Release"). We still build it as seven milestones in dependency order. Each milestone ends with working, tested software, so problems surface early, and the release is cut after M7.

Detailed task-by-task plans exist for **M1** and **M2**. M3–M7 get their own plans when they start. That way each plan is written against the code that actually exists by then, rather than against guesses.

| # | Milestone | Plan | Done when |
|---|---|---|---|
| **M1** | **Local Forge**: SSO; git-sourced config and content; reading; instant quizzes; linear/free progression; local labs via `crucible-agent`; KodeKloud-style lab UI with tabbed terminals, checks, setup scripts, hints, timer and idle prompt; themes, motion, quotes; `crucible lint` | `2026-10-05-m1-local-forge.md` (20 tasks) | `make local-check` passes: a trainee completes Forge 101 end to end using only a laptop lab |
| **M2** | **AWS Deploy**: single-node k3s on EC2 (~$50/month on schedule); Cognito sign-in; S3 backups and restore; scheduled sleep/wake; `crucible aws` CLI | `2026-10-05-m2-aws-deploy.md` (6 tasks) | Sandbox acceptance A1–A6: live HTTPS URL, sleep/wake, teardown → rebuild restores data |
| M3 | **Approvals & FinOps core**: River job queue; lab request → cost-tiered approval → escalation after 4 business hours; schedules (lab windows + effective end); budgets and hard caps; kill switch; email + Slack/Teams notifications; config write-back to the platform repo (program roles, schedules) | written at M3 start | A cloud-tier request needs an approver, escalates when ignored, is blocked over the cap; labs die at window close |
| M4 | **Cluster runtime**: sysbox on k3s; namespace per lab with quota and default-deny NetworkPolicy (blocks IMDS); exec-based PTYs and tamper-proof checks | written at M4 start | Forge 101's lab runs with `runtime: cluster` and checks are not self-reported |
| M5 | **Human scoring**: text/upload/sign-off questions; review tasks; Anvil scoring queue with rubric, feedback, return-for-rework, audited overrides; terminal transcripts; S3 uploads | written at M5 start | A scorer grades a free-text answer and a lab submission; the trainee sees the feedback |
| M6 | **AWS labs & Ledger**: shared lab account roles with permission boundary + session tags; terraform runner Jobs + workspace pod; infracost estimates; nightly reaper; Cost Explorer ingestion; FinOps dashboard | written at M6 start | An AWS lab is estimated, approved, provisioned, checked, destroyed and swept; spend shows on the Ledger |
| M7 | **Forge & people**: forge ranks (Ore → Masterwork) and badges; mentor dashboard; journey heat map with stuck signals; in-app content edit + maintainer review; `crucible preview`; Forge Status page; motion polish | written at M7 start | Release candidate: every spec section is checked off in the coverage table below |

## Spec coverage

| Spec section | Milestone |
|---|---|
| §1 Goals / non-goals | M1 (no competitions, no overseer) |
| §3 Architecture (Go monolith, React SPA, runner interface, CLIs) | M1 (River job queue in M3) |
| §4.1–4.5 Repos, program file, quiz and lab schemas | M1 (writing program files back to git in M3) |
| §5.1 OIDC login, agent pairing tokens | M1 |
| §5.2–5.3 Roles and permission matrix | M1 checker; approver, scorer and leader screens in M3, M5, M7 |
| §6 Git sync, last-good fallback, lint | M1; UI content edits + review and `preview` in M7 |
| §7 Reading, instant quizzes, progression | M1; human scoring in M5; forge ranks in M7 |
| §8.1 Lifecycle | M1 (provisioning → ready → destroyed); approval states in M3 |
| §8.2 Runtimes | `local` in M1, `cluster` in M4, `aws` in M6 |
| §8.3 Lab UI | M1 |
| §8.4 Hints | M1 |
| §8.5 Task setup scripts | M1 (maintainer notification in M3) |
| §8.6 Lab timer and idle check | M1 for TTL; schedule and budget limits feed the same timer in M3 and M6 |
| §9.1–9.2 Approvals, tiers, escalation, schedules, budgets, kill switch | M3 |
| §9.3 Cost data and dashboard | M6 |
| §9.4 Deployment, hibernate, restore | M2 |
| §10 Notifications | M3 |
| §11 Mentor and journey view | M7 |
| §12 Themes, motion, quotes, accessibility | M1 (polish in M7) |
| §13 Data model | Grows per milestone through numbered goose migrations |
| §14 Errors, security, testing | Every milestone (each plan has a Review Focus section) |

## Known decisions to revisit
- **One API replica.** The agent hub lives in process memory. Scaling out would need a shared hub, such as Postgres LISTEN/NOTIFY or Redis. That isn't needed below about 100 users.
- **No container registry.** Releases are S3 tarballs imported into containerd. When more nodes are added (M4 upgrade path), switch to ECR with the kubelet credential provider.
- **Identity provider.** AWS uses an invite-only Amazon Cognito user pool, created by M2's persistent stack (guide: `docs/runbooks/cognito.md`). Local development uses Keycloak. Crucible only speaks standard OIDC, so a company identity provider (Entra, Okta, IAM Identity Center) can later be federated into Cognito without code changes.
