# One `crucible` binary, CI releases, and the node pulling from GHCR — design

Status: approved in conversation (2026-11-04). Decisions: one binary with subcommands; a release on merge to main
publishes the binary and all images to GHCR; the server image and chart are private (the node pulls them with a
read-only token), the workspace and toolbox images public; unreleased branches go out through a manual prerelease
workflow or a local `--build`; CI runs only fast checks on push/PR, the browser suites by hand; no GitHub Pages.

## 1. One binary

`cmd/crucible` is the only program. The other three `cmd/` folders are removed; their code moves under `cmd/crucible`
(package main, one file per command: `serve.go`, `agent.go`, `ready.go`) or stays in its `internal/` package.

| Was | Becomes |
|---|---|
| `crucible-api` | `crucible serve` (same env configuration, same behaviour) |
| `crucible-agent [flags]`, `crucible-agent cluster up/down` | `crucible agent [flags]`, `crucible agent cluster up/down` |
| `crucible-ready [--json] <release…|all>` | `crucible ready [--json] <release…|all>` |
| (none) | `crucible version` (the version stamped at build; `dev` otherwise) |

The version is stamped with `-ldflags "-X crucible/internal/docs.Version=<v>"` as today (one variable, also shown in the
app). Every reference to the old names changes: the server Dockerfile (`ENTRYPOINT ["crucible","serve"]`), the toolbox
Dockerfile (ships `crucible`; ready checks run `crucible ready`), `internal/labs` (`exec crucible ready …`,
`crucible ready --json all`), the agent pairing command the server shows (`crucible agent --server …`), compose and
the scripts, e2e (`CRUCIBLE_AGENT` becomes the `crucible` path, spawned with `agent` first), the web Connect page and
ClusterPick, `docs/user`, CLAUDE.md, the runbook and the docs pages. A release pins the server and the toolbox images
together, so a server never calls `crucible ready` in an older toolbox.

## 2. Images and the chart

| Artifact | Reference | Visibility | Platforms |
|---|---|---|---|
| server | `ghcr.io/platform-infra-org/crucible:<version>` | private | linux/amd64, linux/arm64 |
| workspace | `ghcr.io/platform-infra-org/crucible-workspace:<version>` | public | linux/amd64, linux/arm64 |
| toolbox | `ghcr.io/platform-infra-org/crucible-toolbox:<version>` | public | linux/amd64, linux/arm64 |
| Helm chart | `oci://ghcr.io/platform-infra-org/charts/crucible`, chart version = `<version>` without the `v` | private | — |

At release the chart's values are rewritten before packaging: `image.repository` + `image.digest` (server),
`awsLabs.workspaceImage` and `toolboxImage` as `<repo>@sha256:<digest>`. The chart template uses the digest when set
(`repo@digest`), else `repo:tag` (local and dev builds). `image.pullPolicy` becomes `IfNotPresent`; `imagePullSecrets`
lists `ghcr-pull` when `image.pullSecret` is set. Local compose, `local-check` and `cluster-check` keep building images
locally, unchanged apart from names.

## 3. Releases (CI)

- `.github/workflows/checks.yml` — every push to main and every PR: `make tools`, `gofmt -l`, `go vet ./...`,
  `go test -race ./...`, `terraform test` in the three stacks, `bash deploy/helm/test.sh`,
  `bash deploy/workspace/test.sh`, `crucible lint` on the example repos, web `npm ci && npm test && npx tsc -b &&
  npm run lint && npm run build`; plus the PR-title conventional-commit gate.
- `.github/workflows/release.yml` — on a merged PR to main (the guideline's release-pr flow): git-cliff (`cliff.toml`)
  bumps the version and tags the merge commit (amended 2026-11-04: nothing is pushed to main, which the `protect-main` ruleset guards; the changelog since the last release is the Release's notes, not a `CHANGELOG.md`); builds and pushes the three images (provenance +
  SBOM); packages and pushes the chart with the digests; builds `crucible` for darwin/linux × amd64/arm64 with the
  version stamped, plus `SHA256SUMS`; creates the GitHub Release with the binaries, checksums and SBOMs, and notes that
  list the image digests and the chart version. Docs-only / ci-only merges release nothing. A
  PR run (not merged) builds everything without pushing.
- `.github/workflows/release-manual.yml` — `workflow_dispatch` on any branch: version `<next>-<branch>.<run_number>`
  (or an override); same artifacts as a prerelease; no `latest` tag; the tag points at that branch's HEAD.
- `.github/workflows/e2e.yml` — `workflow_dispatch` only: `local-check` and `cluster-check` (kind on the runner).
- The guideline's `docs.yml` is not adopted (no Pages: the app has its docs and the docs pages describe our account).

## 4. Deploying a release

- `crucible aws deploy [--version vX.Y.Z | --build]`. Without flags a released binary deploys its own version; a `dev`
  binary refuses and says to pass `--version` or `--build`. `--version` accepts releases and prereleases.
- Deploy writes the version to the SSM parameter `/<name>/release` (String) and runs `/opt/crucible/deploy.sh` over
  SSM as today, then checks health. No image is built, saved or uploaded; S3 is no longer involved in releases.
- The node's `deploy.sh`: reads `/<name>/release` (refuses when unset: "no release chosen: run crucible aws deploy");
  reads the GHCR pull token from the SecureString `/<name>/ghcr_pull_token`; `helm registry login ghcr.io` with it
  (`--password-stdin`); creates/updates the `ghcr-pull` docker-registry Secret in namespace `crucible`; runs
  `helm upgrade --install crucible oci://ghcr.io/platform-infra-org/charts/crucible --version <v without v>` with the
  same `--set` values as today plus `image.pullSecret=ghcr-pull`, and `-f` the SSM helm-values; logs out.
- The token: main stack variable `ghcr_pull_token` (sensitive, required: validation refuses an empty value), stored as
  the SecureString `/<name>/ghcr_pull_token`; a GitHub token with `read:packages` only.
- `workspace_image` / `toolbox_image` in `crucible.tfvars` stay optional overrides; empty means the release's pinned
  images (the chart's values).
- `--build`: builds the server image (linux/amd64) and packages the chart locally, pushes both to GHCR as
  `0.0.0-dev.<shortsha>[-dirty.<unix>]` using the operator's own `docker login ghcr.io` / `helm registry login`, then
  deploys that version. Workspace and toolbox images in a dev build keep the chart's current pinned values.
- `crucible aws status` reports the deployed release (`/<name>/release`) and keeps checking `ghcr.io` and
  `pkg-containers.githubusercontent.com` egress.
- The persistent stack drops the `releases/` lifecycle rule; the node policy drops reads of `releases/*` and
  `current-release`; nothing else in the data bucket changes.

## 5. Migration

- An existing node keeps the `deploy.sh` written at its first boot (user data never updates). After upgrading, the
  operator runs `crucible aws teardown --yes` then `crucible aws up` (the snapshot restores the data). The runbook and
  aws-deploy say so; `deploy` on an old node fails with a message saying exactly that (it detects the old script:
  `grep -q current-release /opt/crucible/deploy.sh`).
- `ghcr_pull_token` must be set before `up`.

## 6. Testing

- Go: subcommand dispatch (`serve`, `agent`, `ready`, `version`, unknown → usage, exit 2); the agent's and ready's
  existing tests move with them; `awsops.Deploy` with `--version` writes the SSM pointer and runs deploy.sh, refuses a
  `dev` binary without a flag, `--build` runs the build/push/package commands with the dev version (against the
  recorder); the pairing command text; labs' `crucible ready` scripts.
- Terraform (main): `ghcr_pull_token` stored as a SecureString, required by default; the user-data template has no
  `current-release` and contains the OCI chart install; node policy without `releases/`.
- Helm: digest vs tag rendering; `imagePullSecrets` only with a pull secret; `IfNotPresent`.
- Workflows: `actionlint` (run with Docker `rhysd/actionlint` image, pinned) on every workflow file; a dry `act`-free
  check that each `run:` script passes `bash -n` (extracted).
- By hand (runbook): the first real release; the node pulling the private chart and image with the token; a
  `--build` deploy; teardown → up on our account.

## 7. Out of scope

Windows binaries (the agent runs under WSL2: the linux binary); signing the binaries beyond SHA256SUMS + provenance;
moving AWS lab workspaces off Docker-in-a-pod.
