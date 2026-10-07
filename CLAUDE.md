# Crucible

A self-hosted training platform: git-sourced trainings (readings, quizzes, labs), hands-on labs that run on the
trainee's laptop (Docker), in Kubernetes, or in a shared AWS account, human scoring, cost approvals and budgets,
forge ranks, and mentoring. One Go API serves a React SPA. Git is the source of truth for config and content; the UI
writes back to git as a bot.

- Spec: `docs/superpowers/specs/2026-10-05-crucible-design.md` (the authority on behaviour)
- Roadmap, coverage table (spec section → proving test), accepted deviations, decisions to revisit:
  `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`
- Per-milestone plans: `docs/superpowers/plans/`; rulings made during the build: `docs/superpowers/rulings-m3-m7.md`
- Operator guides: `docs/runbooks/aws.md`, `docs/runbooks/cognito.md`

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
| Fresh local stack without tests | `make build && ./scripts/seed-git.sh && KEYCLOAK_PORT=8082 docker compose -f deploy/compose/docker-compose.yml up -d --build --wait` |

Local users (password = username): `trainee`, `senior`, `leader`, `admin`. App at http://localhost:8080.

## Layout

- `cmd/crucible-api` server; `cmd/crucible` CLI (`lint`, `preview`, `aws …`); `cmd/crucible-agent` laptop lab agent
- `internal/` — one package per domain: `labs` (runners: local/cluster/aws, approvals, budgets, sweep, reaper,
  ledger), `learn` (progress, quizzes, ranks), `scoring` (submissions, Anvil), `edits` + `gitsync` (git mirror,
  write-back, content edit branches), `configapi`, `journey`, `rbac`, `auth`, `notify`, `jobs` (River), `httpapi`
  (routing, Origin guard, CSP), `content` (loader + lint, incl. hardened terraform lint), `awscloud`, `infracost`
- `internal/db/migrations` — goose, numbered; take the next free number and always write a Down
- `web/` — React 19 + Vite SPA; theme tokens in `web/src/theme/tokens.css`
- `examples/` — platform config + training repos used by every e2e (seeded into `.local/git` by `scripts/seed-git.sh`)
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

## Invariants worth knowing before changing code

- Anyone enrolled in a training never sees its answer keys: rubrics (`json:"-"`, only via `ScorerView`), raw files,
  edit diffs, Anvil data, peers' uploads. Enrolled users never score their own training (admins included).
- Git is the source of truth. UI saves go through the bot Writer with `base_sha`, a permission re-check at the tip,
  validation before push, and an audit row. Content edits use per-edit branches merged by the bot after review,
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
