# Crucible

A self-hosted training platform: git-sourced trainings (readings, quizzes, labs), hands-on labs that run on the
trainee's laptop (Docker), in Kubernetes, or in a shared AWS account, human scoring, cost approvals and budgets,
forge ranks, and mentoring. One Go API serves a React SPA. Git is the source of truth for config and content; the UI
writes content edits back to git as a bot. Configuration and people live in Postgres.

- Spec: `docs/superpowers/specs/2026-10-05-crucible-design.md` (the authority on behaviour)
- Roadmap, coverage table (spec section → proving test), accepted deviations, decisions to revisit:
  `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`
- Per-milestone plans: `docs/superpowers/plans/`; rulings made during the build: `docs/superpowers/rulings-m3-m7.md`
- Operator guides: `docs/runbooks/aws.md`, `docs/runbooks/cognito.md`
- AWS for newcomers: `docs/aws-guide.html` — every AWS component, an architecture diagram, deploying step by step, and
  how AWS labs use Terraform and IAM. Update it when `deploy/aws` or the AWS lab path changes.
- Terraform or Crossplane for AWS labs: `docs/terraform-vs-crossplane.html` — how each would run a lab, with diagrams,
  and the trade-offs here.
- Manual test guide: `docs/test-guide.html` — what Crucible does, how AWS labs work, and a checklist of every capability
  from a fresh local stack to a real AWS sandbox. Update it when a capability or a test step changes.

## Toolchain

The system Go (1.20) and Terraform are too old. The repo brings its own Go 1.26 and Terraform in `.local/tools`.
Prefix every shell that builds or tests:

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
```

Docker Desktop (arm64, ~8 GiB) is required for tests (Postgres testcontainers), the local stack and kind.

## Commands

| What | Command |
|---|---|
| Build binaries (`bin/crucible`, `crucible-api`, `crucible-agent`) | `make build` |
| Go checks | `gofmt -l cmd internal`, `go vet ./...`, `go test -race ./...` |
| Cluster integration test (build tag) | `go vet -tags cluster ./internal/labs` |
| Web | `cd web && npm test && npx tsc -b && npm run build && npm run lint` |
| Terraform (mock providers only) | `cd deploy/aws/{persistent,main,labs} && terraform test` |
| Helm chart | `bash deploy/helm/test.sh` |
| Content lint | `./bin/crucible lint examples/<repo>` |
| Browser end-to-end, local stack | `KEYCLOAK_PORT=8082 make local-check` (port 8081 is taken on this machine) |
| Browser end-to-end on kind (cluster + AWS dry run) | `KEYCLOAK_PORT=8082 make cluster-check` |
| Leave either stack running afterwards | prefix with `KEEP=1` |
| Fresh local stack without tests | `make build && ./scripts/seed-git.sh && KEYCLOAK_PORT=8082 docker compose -f deploy/compose/docker-compose.yml up -d --build --wait` (the api imports `examples/platform` into an empty database via `CRUCIBLE_SEED_DIR`) |

Local users (password = username): `trainee`, `senior`, `leader`, `admin`. App at http://localhost:8080.

## Layout

- `cmd/crucible-api` server; `cmd/crucible` CLI (`lint`, `preview`, `aws …`); `cmd/crucible-agent` laptop lab agent
- `internal/` — one package per domain: `labs` (runners: local/cluster/aws, approvals, budgets, sweep, reaper,
  ledger), `learn` (progress, quizzes, ranks), `scoring` (submissions, Anvil), `edits` + `gitsync` (git mirror,
  write-back, content edit branches), `configapi`, `org` (Postgres-owned config and org data), `journey`, `rbac`, `auth`, `notify`, `jobs` (River), `httpapi`
  (routing, Origin guard, CSP), `content` (loader + lint, incl. hardened terraform lint), `awscloud`, `infracost`
- `internal/db/migrations` — goose, numbered; take the next free number and always write a Down
- `web/` — React 19 + Vite SPA; theme tokens in `web/src/theme/tokens.css`, styles in `web/src/theme/app.css`
- `.claude/skills/crucible-ui/` — the UI design skill (tokens, components, motion, accessibility, how to check a page)
- `examples/` — training repos (seeded into `.local/git` by `scripts/seed-git.sh`) and `platform`, the configuration
  seed every e2e starts from (imported into Postgres by `CRUCIBLE_SEED_DIR`)
- `deploy/compose` (local), `deploy/helm` (k3s), `deploy/aws/{persistent,main,labs}` (Terraform)

## Hard rules

- **Never create real AWS resources.** Terraform only with mock providers (`terraform test`, `validate`); AWS code is
  tested against `awscloud.Fake` or `httptest`. Real-AWS verification is a by-hand runbook step for the user.
- **Never touch `~/.kube/config` or the user's kind cluster `platform`.** `cluster-check` uses its own
  `KUBECONFIG=.local/kind/admin.kubeconfig` and only creates/deletes `crucible-m4`.
- **Docker hygiene:** this machine's Docker disk is small. Never `docker system prune` / `container prune` or remove
  other projects' containers, images or volumes. Clean only what you created (build cache and dangling images are
  OK). Run one heavy suite at a time; parallel `local-check` runs collide (shared compose project and `node_modules`).
- **Commits:** commit only your own files with the pathspec form `git commit -m "…" -- <paths>`; never `git add -A`
  or `commit -a` when other work may be in the tree. Never rewrite history or force-push. Trailer:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Tests first** for behaviour changes; every security or money path keeps a test that fails without the fix.
- **Docs:** any capability change updates `docs/user` in the same commit. `go test ./internal/docs` fails when a SPA
  route, a role or a catalog block has no page, or a page covers something that no longer exists.

## UI design

- **Use the `crucible-ui` skill for any UI design or styling task** (a page, a component, a restyle, motion, a theme or
  token change). It holds the project's design patterns; read it before writing JSX or CSS.
- **Keep it current:** a change that adds, alters or removes a design pattern updates
  `.claude/skills/crucible-ui/SKILL.md` in the same commit.

## Invariants worth knowing before changing code

- Anyone enrolled in a training never sees its answer keys (rubrics — `json:"-"`, only via `ScorerView` — raw files,
  edit diffs, Anvil data, peers' uploads), **unless they may edit it**: admins, its maintainers, and leaders/seniors of
  teams running it keep editing and reviewing while enrolled (owner's ruling, 2026-10-08; `TestEnrolledEditorsKeepEditing`).
  Enrolled users never score their own training (admins included).
- Git is the source of truth for **training content**. Configuration and org data (settings, tiers, schedules, quotes,
  admins, the training registry, teams, membership, mentors, webhooks, budgets, programs, roles, enrollments, pins)
  live in Postgres behind `internal/org`: validated, audited, permission-checked, versioned writes, and nowhere else (no
  git platform repo; a set `CRUCIBLE_PLATFORM_REPO` stops the server). A fresh instance needs only `DATABASE_URL` and
  `CRUCIBLE_BOOTSTRAP_ADMIN`; `CRUCIBLE_SEED_DIR` imports a YAML platform directory into an empty database once (dev,
  e2e, preview) through the same store writes. Content edits use per-edit branches merged by the bot after review,
  limited to `training.yaml` and `modules/<id>/…`. All git calls go through `gitsync.git` (isolated config,
  timeouts, `--end-of-options`); file:// remotes need `CRUCIBLE_GIT_ALLOW_FILE=1` (dev/compose only).
- Lab content runs at its exact content SHA (`trainingOf`/`Version`), never a newer version.
- Money: approvals use tiers by estimate; budgets fail closed; cluster labs priced by `cluster_usd_per_hour` (unset
  → unavailable, never $0); aws labs need infracost (or `CRUCIBLE_INFRACOST=off` in dryrun only).
- Final scores are final; an admin reset exists (`/api/admin/quiz-reset`, Anvil reset). Ranks only ever rise.
- One API replica (in-memory agent hub and locks); Helm pins `replicas: 1` and `Recreate`.
- Every state-changing request needs a same-origin Origin/Referer and a JSON or multipart body; only
  `/api/git/hook` (HMAC) is exempt. The SPA ships a strict CSP: no inline scripts, no external hosts.
- Write prose and UI copy from the user's side of the screen, plainly; Crucible's voice is the forge metaphor.
