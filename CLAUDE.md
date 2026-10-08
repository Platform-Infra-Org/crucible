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
- `web/` — React 19 + Vite SPA; theme tokens in `web/src/theme/tokens.css`
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

---

## Handoff — `victa_dev` → `main`, 2026-10-08

**Temporary. Delete this section in the commit that merges `victa_dev` into `main`.** Written for whoever picks the
branch up next; everything above it is the standing instruction set.

`victa_dev` is pushed (`origin/victa_dev`) and carries M8a **plus `main` merged into it**. `main` itself is
untouched. A PR into `main` was not opened — the token lacked the scope — so open it from
`compare/main...victa_dev`; a prepared body sits outside the repo at `~/crucible-pr-body.md`.

### What this branch did

Configuration and org data moved out of the platform repo into Postgres, behind `internal/org`. Git keeps
**training content**. (M8a bridged both sources; the second session below removed git mode.) See the
Invariants section above, the spec at `docs/superpowers/specs/2026-10-07-db-owned-config-design.md` and the plan at
`docs/superpowers/plans/2026-10-07-m8-config-in-db.md` (its "As built" notes record where the build diverged).

New UI: an **Administrator** menu (top right) holding Forge Status, **Forge settings** and **Trainings**.

### What the merge required

- Our migration was renumbered `00018_org_config.sql` → **`00020`** (`main` took 00018 and 00019; goose refuses a
  duplicate version). A local database created from the branch before this will fail to migrate — drop it.
- Seven conflicts, all "both branches appended to the same list"; `git rerere` has the resolutions recorded.
- `55772d5` — `main` fails `go test ./internal/configapi` on its own (13 tests, one cause): `examples/platform`
  enrols the forge team in forge-103 while `configapi_test.go` overrides `trainings.yaml` with a two-training
  registry. Fixed by registering forge-103 in that fixture. **Replace it if you'd rather fix it another way.**
- `523e68e` — on a Linux host the api container (uid 10001) owns the git objects it pushes into the `.local/git`
  bind mount, so `seed-git.sh` could not remove them and `authoring.spec.ts` could not chmod them. Both now fall
  back to a throwaway root container. Docker Desktop and rootless Podman map those writes to the host user, so
  nothing changes there.
- Your rewrite of `internal/edits` fixed `TestApproveMovedEdit`, which fails on `main` and on this branch's base
  but passes merged.

### Verified on the merged branch

`go build`, `go vet`, `gofmt`, `go test ./...` (31/31 packages), `go test -race ./...` (**no data races**),
`npx tsc -b`, `npm run lint`, `npm test` (32 files, 289 tests), `shellcheck scripts/seed-git.sh`, and
`KEYCLOAK_PORT=8082 make local-check` — 9 of 9 browser journeys, including the authoring and docs ones.

Known red: `TestAWSLabModuleRejectsHostileInputFast/token_flood` fails under `-race` only — it asserts the terraform
lint finishes inside a second and measures ~1.2s with the detector's instrumentation. Not a race; the threshold
should scale or skip under `-race`.

### Second session, 2026-10-08: git mode removed, one Trainings page

- **Postgres only.** `CRUCIBLE_PLATFORM_REPO`/`_BRANCH`, the platform-repo sync, `gitsync.Writer` (config write-back)
  and the git-backed `configapi` writes are gone; `configapi` keeps its reads (team list, Forge Status, program diff).
  `org.Routes` is always mounted; `config_in_db` on `/api/me` became `can_manage_trainings`. A set
  `CRUCIBLE_PLATFORM_REPO` stops the server with instructions (`configSource`). Helm and Terraform lost
  `platformBranch`/`platform_repo`/`platform_branch`.
- **Seed.** `CRUCIBLE_SEED_DIR` → `org.Store.Seed`: `config.Load` of a YAML platform dir, written through the store's
  own validated, audited writes (actor `seed`), once per database (`seed.import` audit row). Compose mounts
  `examples/platform` at `/seed`; `crucible preview` writes its seed to `/git/seed`. `TestSeedImportsAPlatformOnce`
  proves the seeded rows read back exactly as `config.Load` read the YAML.
- **Manage trainings** (`/trainings/manage[/:training]`, `GET /api/org/trainings`): register/repoint/unregister
  (admins), start or stop a training for a team, enroll (a new email joins the team as a trainee first), roles,
  lab settings, content version. It replaced the Registry and Program settings pages; the Team page links to it. The
  four e2e journeys that enrolled through Program settings use `enroll()` in `e2e/tests/helpers.ts`.

### What still needs doing

1. **Look at the nav in a browser.** Docs, the `?` link and the Administrator menu now share the right-hand side.
   No test can judge whether that reads well.
2. **Export/import** is built (spec §14): Forge settings → **Move this forge**, `GET /api/admin/export`,
   `POST /api/admin/import` (fresh instances only). Not built: a CLI, `--force`, copying upload blobs.
3. **First-run flow.** An admin on a fresh Postgres instance has rights over nothing: no trainings, so
   `Edits.CanUse` is false and the **Edits** button is absent; empty Hearth; no teams. The path works (Registry →
   Team → roster) but nothing guides you through it. Consider a first-run checklist.
4. **Orphan programs** are skipped with a `slog.Warn` where `config.Load` failed the whole load. Availability over
   strictness, but it deserves an operator-visible marker on Forge Status.
5. The `-race` timing assertion in (Known red).

### The owner's concerns, in their words

- **"no personal data in git, only application global config."** People, permissions, budgets and teams belong in
  the database. This is the principle the whole change serves — keep it when extending `internal/org`.
- **"I want 1 admin user when booting the app first. then that user will configure all else. everything is saved
  to db and can be exported or imported into a new instance if env change."** The bootstrap admin and DB-owned
  config are done; **export/import is not** and is the part they asked for that is still missing.
- **"the application management should be done in the application"** — not by committing YAML. Changing a
  permission, quota, team or budget should never require git.
- **They want git mode gone**, keeping Postgres mode only. Done in the second session.
- **Two people, two agents, one repo.** Verify each side independently, then merge deliberately — that is how this
  merge was done. Prefer small pathspec commits; never rewrite shared history.
- **They want to understand and explain every change themselves.** Say what you plan to do before doing it, and
  keep commit messages explanatory rather than terse.
