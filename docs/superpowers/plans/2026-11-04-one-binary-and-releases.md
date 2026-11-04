# One Binary and Releases Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One `crucible` binary (`serve`, `agent`, `ready`, `version` plus today's commands), CI that checks every push/PR and releases on merge (binary + all images + chart on GHCR), and the AWS node deploying a release pulled from GHCR instead of an S3 tarball.

**Architecture:** The three extra `cmd/` mains move into `cmd/crucible` as subcommand files. The Helm chart renders images by digest and an optional pull secret; at release CI pins the three image digests into the chart and pushes it to GHCR as an OCI chart. `crucible aws deploy` records a version in SSM; the node's `deploy.sh` logs in to GHCR with a read-only token from SSM and installs that chart version.

**Tech Stack:** Go 1.26, Terraform (mock-provider tests), Helm 3 (OCI), GitHub Actions, Docker buildx, git-cliff.

**Spec:** `docs/superpowers/specs/2026-11-04-one-binary-and-releases-design.md`

## Global Constraints

- Never create real AWS resources; Terraform only via `terraform test`/`validate` with mock providers; Go AWS code against fakes/recorders.
- Never touch `~/.kube/config` or the kind clusters `platform`/`crucible-m4`.
- Docker hygiene: never prune or remove anything you didn't create; one Docker-heavy suite at a time.
- Toolchain: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH` (in a worktree use the main checkout's absolute `.local/tools`).
- Tests first; commits with `git commit -m "…" -- <paths>` (`git add` new files first); trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Any capability or command-name change updates `docs/user` in the same commit; `go test ./internal/docs` passes.
- Image names: `ghcr.io/platform-infra-org/crucible` (server, private), `ghcr.io/platform-infra-org/crucible-workspace` and `ghcr.io/platform-infra-org/crucible-toolbox` (public), chart `oci://ghcr.io/platform-infra-org/charts/crucible` (private). Chart version = release version without the leading `v`.
- SSM: `/<name>/release` (String, the version with its `v`), `/<name>/ghcr_pull_token` (SecureString). The pull Secret in namespace `crucible` is `ghcr-pull`.
- Subcommands: `crucible serve`, `crucible agent [flags]`, `crucible agent cluster up|down`, `crucible ready [--json] <release…|all>`, `crucible version`.
- GitHub Actions: never interpolate `${{ github.event.* }}` text (PR titles, branch names) directly into a `run:` script; pass it through `env:`.

## Review Focus

1. **A `dev` binary runs `crucible aws deploy` with no flag** → it refuses with "pass --version vX.Y.Z or --build" and deploys nothing (Task 5 test).
2. **The node has no or a wrong GHCR token** → `deploy.sh` stops before any `kubectl apply` or `helm upgrade`, saying which parameter to set (Task 4 template-order assertions).
3. **An existing node still has the S3 `deploy.sh`** → `crucible aws deploy` refuses with "this node predates GHCR releases: run crucible aws teardown --yes, then crucible aws up" (Task 5 test).
4. **The chart has both a tag and a digest** → the digest wins; with neither repository override the local `docker.io/library/crucible:dev` default still renders for compose/kind (Task 3 helm test).
5. **A PR title or branch name with shell metacharacters** → workflows pass it via `env:` (Task 6/7 actionlint + a grep test that no `run:` contains `${{ github.event`).

## Lanes (for parallel execution)

- Lane binary: Task 1 → Task 2
- Lane deploy: Task 3 → Task 4 → Task 5
- Lane ci: Task 6 → Task 7
- After merging the lanes: Task 8 (operator docs and final consistency)

---

### Task 1: `crucible serve` and `crucible version`

**Files:**
- Create: `cmd/crucible/serve.go` (from `cmd/crucible-api/main.go`), `cmd/crucible/serve_test.go` (from `cmd/crucible-api/main_test.go`), `cmd/crucible/source_test.go` (from `cmd/crucible-api/source_test.go`)
- Delete: `cmd/crucible-api/`
- Modify: `cmd/crucible/main.go` (dispatch + usage), `Dockerfile`, `Makefile`
- Test: `cmd/crucible/main_test.go`

**Interfaces:**
- Produces: `func serve(args []string) int` in package main (runs what `crucible-api`'s `main` ran: `run(ctx)` with signal handling, exit 1 on error); `crucible version` prints `docs.Version`.

- [ ] **Step 1: Failing tests** in `cmd/crucible/main_test.go`:

```go
func TestVersionPrintsTheStampedVersion(t *testing.T) {
	var out bytes.Buffer
	if code := runMain([]string{"version"}, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != docs.Version {
		t.Fatalf("version: exit %d, %q", code, out.String())
	}
}

func TestUnknownCommandShowsUsage(t *testing.T) {
	var errw bytes.Buffer
	if code := runMain([]string{"nope"}, io.Discard, &errw); code != 2 || !strings.Contains(errw.String(), "crucible serve") {
		t.Fatalf("exit %d, usage %q", code, errw.String())
	}
}
```

`runMain(args []string, out, errw io.Writer) int` is a new testable dispatcher; `main()` becomes `os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))`. Run `go test ./cmd/crucible -run 'Version|UnknownCommand'`: FAIL (undefined runMain).

- [ ] **Step 2: Move the server.** `git mv cmd/crucible-api/main.go cmd/crucible/serve.go`, same for `main_test.go` → `serve_test.go` and `source_test.go`. In `serve.go`: keep `package main`, rename its `func main()` to `func serve(args []string) int` (no flags today: refuse any argument with usage, exit 2; on `run(ctx)` error print `error: …` to stderr and return 1). Rename colliding identifiers between the two files (`env`, `must`, `commaList`, `seed`, … — check with `go build ./cmd/crucible`; prefix server-side ones `srv` only where they collide). Update the moved tests' references.

- [ ] **Step 3: Dispatch.** In `cmd/crucible/main.go`: `runMain` switches on `args[0]`: `serve` → `serve(args[1:])`, `version` → print `docs.Version`, plus the existing `lint`/`preview`/`aws`/`local`; the usage text lists `crucible serve` and `crucible version` first.

- [ ] **Step 4: Build files.** `Dockerfile`: build only `./cmd/crucible` into `/out/crucible` (keep the `-X crucible/internal/docs.Version=${CRUCIBLE_VERSION}` ldflag) and `ENTRYPOINT ["crucible","serve"]`. `Makefile` `build` still builds `./cmd/...` (now one binary). grep the repo for `crucible-api` and fix every remaining reference that names the binary (compose, helm templates/comments, scripts, internal comments, docs/user, CLAUDE.md "Layout"); leave historical specs/plans alone.

- [ ] **Step 5: Verify.** `gofmt -l cmd internal`; `go vet ./...`; `go test ./cmd/crucible` (the moved serve tests included); `go build ./... && test ! -d cmd/crucible-api`; `make build && ./bin/crucible version`.

- [ ] **Step 6: Commit** `feat(cli)!: crucible serve replaces crucible-api; crucible version` with all touched paths (including the deleted directory).

### Task 2: `crucible agent` and `crucible ready`

**Files:**
- Create: `cmd/crucible/agent.go` (+ `agent_test.go`) from `cmd/crucible-agent/`, `cmd/crucible/ready.go` from `cmd/crucible-ready/main.go`
- Delete: `cmd/crucible-agent/`, `cmd/crucible-ready/`
- Modify: `cmd/crucible/main.go`, `deploy/toolbox/Dockerfile`, `internal/labs/service.go` (the two script strings ~1702, ~1727), `internal/labs/*_test.go` expecting `crucible-ready`, `internal/httpapi/server.go:173`, `internal/agent/*.go` messages, `scripts/local-check.sh:56`, `e2e/tests/{scoring,forge-101,approvals,cluster-lab}.spec.ts`, `web/src/pages/Connect.tsx`, `web/src/components/ClusterPick.tsx` + test, `docs/user/**` mentioning `crucible-agent`/`crucible-ready`, `CLAUDE.md`

**Interfaces:**
- Consumes: `runMain` from Task 1.
- Produces: `func agentCmd(args []string) int` (today's agent main; `cluster` subcommand kept), `func readyCmd(args []string) int`.

- [ ] **Step 1: Failing tests.** In `internal/labs`, change the expectations `exec crucible-ready shop db\n` → `exec crucible ready shop db\n`, `exec crucible-ready all\n` → `exec crucible ready all\n`, and `crucible-ready --json all` → `crucible ready --json all`; in `internal/httpapi` (find the test asserting the pairing command, or add one in `server_test.go` against the agent-token endpoint) expect `crucible agent --server`; in `web/src/components/ClusterPick.test.tsx` expect `crucible agent cluster up`; in `cmd/crucible/main_test.go`:

```go
func TestAgentAndReadyAreSubcommands(t *testing.T) {
	for _, args := range [][]string{{"agent", "cluster"}, {"ready"}} {
		var errw bytes.Buffer
		if code := runMain(args, io.Discard, &errw); code != 2 {
			t.Errorf("%v: exit %d (want usage, 2): %s", args, code, errw.String())
		}
	}
}
```

Run them: FAIL.

- [ ] **Step 2: Move the code** (`git mv`), rename `main` → `agentCmd(args []string) int` and `readyCmd(args []string) int`; the agent's `flag` usage uses a `flag.NewFlagSet("agent", flag.ContinueOnError)` instead of the global set; keep `clusterCmd`. Wire both into `runMain` and the usage.

- [ ] **Step 3: Callers.** `internal/labs/service.go`: `exec crucible ready …` and `crucible ready --json all`; `deploy/toolbox/Dockerfile`: build `./cmd/crucible` and copy it to `/usr/local/bin/crucible` (keep the image otherwise identical); the pairing command: `crucible agent --server …`; agent error messages say `crucible agent`; `scripts/local-check.sh`: `CRUCIBLE_AGENT="$root/bin/crucible"`; the e2e specs spawn `spawn(process.env.CRUCIBLE_AGENT!, ['agent', '--server', 'http://localhost:8080'], …)`; Connect page and ClusterPick text; `docs/user` pages (connect-your-laptop, cluster-and-aws-labs, agent-tokens, clusters-for-cluster-labs, authors/cluster-labs); `CLAUDE.md` Layout line (`cmd/crucible` is the only program: serve, agent, ready, lint, preview, aws, local).

- [ ] **Step 4: Verify.** `gofmt`, `go vet ./...`, `go test ./cmd/crucible ./internal/labs ./internal/httpapi ./internal/agent ./internal/docs`, `cd web && npm test && npx tsc -b && npm run lint`, `go build ./... && test ! -d cmd/crucible-agent && test ! -d cmd/crucible-ready`, `grep -rn 'crucible-agent\|crucible-ready' cmd internal web/src deploy scripts e2e/tests docs/user CLAUDE.md` prints nothing.

- [ ] **Step 5: Commit** `feat(cli)!: crucible agent and crucible ready replace crucible-agent and crucible-ready`.

### Task 3: The chart renders images by digest and with a pull secret

**Files:**
- Modify: `deploy/helm/crucible/values.yaml`, `deploy/helm/crucible/templates/crucible.yaml` (the server image ~45, and any other pod using `.Values.image`), `deploy/helm/test.sh`

**Interfaces:**
- Produces: values `image.repository`, `image.tag`, `image.digest` (""), `image.pullPolicy` (`IfNotPresent`), `image.pullSecret` (""); a template helper `crucible.image` returning `repo@digest` when digest is set else `repo:tag`.

- [ ] **Step 1: Failing helm tests** in `deploy/helm/test.sh` (follow its existing render-and-grep style):
  - default render contains `image: "docker.io/library/crucible:dev"`, `imagePullPolicy: IfNotPresent`, and no `imagePullSecrets`;
  - `--set image.repository=ghcr.io/platform-infra-org/crucible --set image.tag=1.2.3 --set image.digest=sha256:<64 a>` renders `ghcr.io/platform-infra-org/crucible@sha256:<64 a>` and not `:1.2.3`;
  - `--set image.pullSecret=ghcr-pull` renders `imagePullSecrets:` with `- name: ghcr-pull` on every pod spec that uses the server image.
  Run `bash deploy/helm/test.sh`: FAIL.

- [ ] **Step 2: Implement** `_helpers.tpl` (create if absent) `{{- define "crucible.image" -}}{{ .Values.image.repository }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ else }}:{{ .Values.image.tag }}{{ end }}{{- end -}}`; use it wherever `.Values.image.repository` is used; add `imagePullSecrets` under each such pod spec `{{- with .Values.image.pullSecret }} imagePullSecrets: [{ name: {{ . }} }]{{- end }}`; values comments say what each is (local default; releases pin `digest`).

- [ ] **Step 3: Verify** `bash deploy/helm/test.sh`; check `scripts/cluster-check.sh` and compose still deploy (they set repo/tag; with `IfNotPresent` a kind-loaded `crucible:<tag>` image is used — read cluster-check's helm call and adjust it to pass `image.pullPolicy=Never` only if it relied on `Never`).

- [ ] **Step 4: Commit** `feat(helm): images by digest, IfNotPresent, an optional pull secret`.

### Task 4: The node installs a GHCR release with a read-only token

**Files:**
- Modify: `deploy/aws/main/variables.tf` (+ `ghcr_pull_token`), `deploy/aws/main/main.tf` (SSM SecureString; node policy without release reads), `deploy/aws/main/bootstrap.sh.tftpl` (`deploy.sh`), `deploy/aws/main/main.tftest.hcl`, `deploy/aws/persistent/main.tf` (drop the `expire-releases` lifecycle rule and the `releases/`/`current-release` comment lines), `deploy/aws/persistent/persistent.tftest.hcl` if it asserts that rule

**Interfaces:**
- Produces: SSM `/<name>/ghcr_pull_token` (SecureString), node reads `/<name>/release` (String, written by Task 5); `deploy.sh` exits non-zero with "no release chosen: run crucible aws deploy" when `/<name>/release` is missing.

- [ ] **Step 1: Failing terraform tests** in `deploy/aws/main/main.tftest.hcl` (copy an existing run's variables and add `ghcr_pull_token = "ghp_test"`; every existing run gets it too):
  - `aws_ssm_parameter.ghcr_pull_token` (or the `secret` for_each including it) has type `SecureString`;
  - a run with `ghcr_pull_token = ""` expects `expect_failures = [var.ghcr_pull_token]`;
  - the rendered user data (`aws_instance.node.user_data` or the `templatefile` local the tests already inspect) contains `oci://ghcr.io/platform-infra-org/charts/crucible`, `helm registry login ghcr.io`, `/${name}/release` and `ghcr-pull`, and does not contain `current-release`, `releases/` or `ctr images import`;
  - ordering (the token is checked before anything is applied): `can(regex("(?s)helm registry login.*kubectl -n crucible create secret generic", <user data>))` and `!can(regex("(?s)kubectl -n crucible create secret generic.*helm registry login", <user data>))`;
  - the node policy has no statement whose resources contain `releases/`.
  Run `terraform test` in deploy/aws/main: FAIL.

- [ ] **Step 2: Implement.** `variable "ghcr_pull_token" { type = string, sensitive = true, description = "A GitHub token with read:packages only: the node pulls Crucible's private chart and server image from GHCR", validation { condition = var.ghcr_pull_token != "", error_message = "ghcr_pull_token is required (a GitHub token with read:packages)" } }`; add it to the SecureString parameters (`/<name>/ghcr_pull_token`); in `deploy.sh` replace the S3 block with:

```bash
version=$(aws ssm get-parameter --region "$REGION" --name "/$NAME/release" --query Parameter.Value --output text 2>"$work/err") || {
  if grep -q ParameterNotFound "$work/err"; then echo "no release chosen: run crucible aws deploy" >&2; else cat "$work/err" >&2; fi
  exit 1
}
token=$(aws ssm get-parameter --with-decryption --region "$REGION" --name "/$NAME/ghcr_pull_token" --query Parameter.Value --output text) || {
  echo "no GHCR pull token: set ghcr_pull_token in crucible.tfvars and run crucible aws up" >&2; exit 1; }
printf '%s' "$token" | helm registry login ghcr.io --username crucible --password-stdin
```

  then (after the namespace) `kubectl -n crucible create secret docker-registry ghcr-pull --docker-server=ghcr.io --docker-username=crucible --docker-password="$token" --dry-run=client -o yaml | kubectl apply -f -`; the helm call becomes `helm upgrade --install crucible oci://ghcr.io/platform-infra-org/charts/crucible --version "$${version#v}" -n crucible --set-string image.pullSecret=ghcr-pull …` (drop `image.tag`); end with `helm registry logout ghcr.io || true` and `echo "deployed $version"`. Remove the S3 reads of `releases/*`/`current-release` from the node policy only where they are release-specific (keep snapshot/upload access).

- [ ] **Step 3: Verify** `terraform fmt -check && terraform test` in deploy/aws/main and deploy/aws/persistent.

- [ ] **Step 4: Commit** `feat(aws)!: the node installs a GHCR release with a read-only pull token; no release tarballs in S3`.

### Task 5: `crucible aws deploy --version | --build`

**Files:**
- Modify: `internal/awsops/awsops.go` (`Deploy`, `Status`), `internal/awsops/awsops_test.go`, `cmd/crucible/main.go` (flags), `cmd/crucible/main_test.go`

**Interfaces:**
- Consumes: `/<name>/release` and `deploy.sh` from Task 4; chart OCI name from the Global Constraints; `docs.Version`.
- Produces: `type DeployOpts struct { Version string; Build bool }`; `func (o Ops) Deploy(ctx context.Context, in DeployOpts) error`; `Up` calls `Deploy(ctx, DeployOpts{})`.

- [ ] **Step 1: Failing tests** in `internal/awsops/awsops_test.go` (use the recorder; set the binary version via a package var `buildVersion = docs.Version` so tests can override it):
  - `buildVersion = "v1.4.0"`, `Deploy(ctx, DeployOpts{})` → an `aws ssm put-parameter --name /crucible/release --type String --value v1.4.0 --overwrite` call, then the SSM `send-command` running `/opt/crucible/deploy.sh`; no `docker`, no `helm package`, no `s3 cp`;
  - `buildVersion = "dev"`, no flags → error containing `pass --version vX.Y.Z or --build`, and no put-parameter;
  - `DeployOpts{Version: "v1.5.0-feat-x.12"}` → that value is put;
  - `DeployOpts{Version: "1.5"}` → refused (must be `v<semver>`);
  - `DeployOpts{Build: true}` with git rev `abc123def456` and a clean tree → `docker buildx build --platform linux/amd64 -t ghcr.io/platform-infra-org/crucible:0.0.0-dev.abc123def456 --push .`, `helm package deploy/helm/crucible --version 0.0.0-dev.abc123def456 --app-version 0.0.0-dev.abc123def456 …`, `helm push <tgz> oci://ghcr.io/platform-infra-org/charts`, then the pointer `v0.0.0-dev.abc123def456`; a dirty tree adds `-dirty.<unix>` (use the recorder's fixed clock);
  - an old node: the SSM fake answers `old` to the probe `grep -q current-release /opt/crucible/deploy.sh && echo old || echo new` → error containing `run crucible aws teardown --yes, then crucible aws up`, and no put-parameter;
  - `Status` prints `release: v1.4.0` from `aws ssm get-parameter --name /crucible/release`.
  `cmd/crucible/main_test.go`: `aws deploy --version v1 --build` → exit 2 (both flags).
  Run: FAIL.

- [ ] **Step 2: Implement** in `awsops.go`: `Deploy` validates (`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`), probes the node script first (only when the node is running; else the existing "node is stopped" error), builds/pushes for `--build` (server image only; the chart values keep their pinned workspace/toolbox images), puts the pointer, runs `deploy.sh` as today, waits for health as today. Remove the old build/save/gzip/S3 upload code and its now-unused helpers and tests. `cmd/crucible/main.go`: `--version` and `--build` flags for `aws deploy` (mutually exclusive), passed into `DeployOpts`; `up` keeps deploying with no flags (its own version) and gains the same two flags.

- [ ] **Step 3: Verify** `gofmt`, `go vet ./...`, `go test ./internal/awsops ./cmd/crucible`.

- [ ] **Step 4: Commit** `feat(aws)!: crucible aws deploy picks a GHCR release (--version) or builds and pushes one (--build)`.

### Task 6: CI checks, the PR-title gate, manual e2e, git-cliff config

**Files:**
- Create: `.github/workflows/checks.yml`, `.github/workflows/e2e.yml`, `cliff.toml`, `scripts/lint-workflows.sh`
- Delete: the guideline files that don't apply: `.github/workflows/docs.yml` (no Pages), and replace the guideline `checks.yml` content
- Modify: nothing else

**Interfaces:**
- Produces: `cliff.toml` used by Task 7; `scripts/lint-workflows.sh` (runs `docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.7 -color` and the `${{ github.event` grep) used by Task 7 too.

- [ ] **Step 1: Failing check** — write `scripts/lint-workflows.sh` first:

```bash
#!/usr/bin/env bash
# Lints the GitHub workflows: actionlint (pinned image) and a guard against event text in run: scripts.
set -euo pipefail
cd "$(dirname "$0")/.."
if grep -nE '^\s*run:.*\$\{\{\s*github\.event\.' .github/workflows/*.yml; then
  echo "pass github.event values to run: scripts through env:, never inline" >&2; exit 1
fi
docker run --rm -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:1.7.7 -color
```

  Also scan multi-line `run: |` bodies (an `awk` pass over the lines inside each `run: |` block, flagging `${{ github.event.`). RED: run it against a scratch workflow that inlines `${{ github.event.pull_request.title }}` in a `run:` — it must fail; then it must pass on Task 6's real files.

- [ ] **Step 2: `checks.yml`** — on `push: [main]` and `pull_request`; `permissions: contents: read`; job `checks` on `ubuntu-latest`: checkout; cache `~/go/pkg/mod`, `~/.cache/go-build` and `web/node_modules` keyed on `go.sum`/`web/package-lock.json`; `make tools` then `echo "$PWD/.local/tools/go/bin" >> "$GITHUB_PATH"; echo "$PWD/.local/tools" >> "$GITHUB_PATH"`; steps: `test -z "$(gofmt -l cmd internal)"`, `go vet ./...`, `go test -race ./...`, `for s in persistent main labs; do (cd deploy/aws/$s && terraform init -backend=false -input=false >/dev/null && terraform test); done`, install Helm (`azure/setup-helm@v4` with `version: v3.18.4`) then `bash deploy/helm/test.sh`, `bash deploy/workspace/test.sh` (shellcheck is on the runner), `make build && for r in examples/forge-*; do ./bin/crucible lint "$r"; done`, `cd web && npm ci && npm test && npx tsc -b && npm run lint && npm run build`. Job `pr-title` as the guideline (`amannn/action-semantic-pull-request@v5`, PR only). Header comment says releasing lives in release*.yml and e2e is manual.

- [ ] **Step 3: `e2e.yml`** — `workflow_dispatch` only, two jobs `local` and `cluster` (`needs: []`, run one after another with `max-parallel` irrelevant — make `cluster` `needs: local` so they never share Docker): `make tools`, PATH, `KEYCLOAK_PORT=8082 make local-check` / `KEYCLOAK_PORT=8082 make cluster-check` (kind comes from `make tools`).

- [ ] **Step 4: `cliff.toml`** — conventional commits; groups feat/fix/perf/refactor/docs/test/build/ci/chore; `tag_pattern = "v[0-9]*"`; bump rules default (feat → minor, fix → patch, `!` → major; while < 1.0.0 breaking bumps minor: `features_always_bump_minor = true`, `breaking_always_bump_major = false`); skip `chore(release)` commits.

- [ ] **Step 5: Verify** `bash scripts/lint-workflows.sh` passes on the new files (actionlint clean); `git-cliff` isn't installed locally — check `cliff.toml` parses with `docker run --rm -v "$PWD:/app" -w /app orhunp/git-cliff:2.13.1 --bumped-version` (prints a version).

- [ ] **Step 6: Commit** `ci: checks on every push and PR, the PR-title gate, manual e2e, git-cliff config` (include the deleted docs.yml).

### Task 7: Release workflows: binaries, images, chart

**Files:**
- Create: `.github/workflows/release.yml`, `.github/workflows/release-manual.yml` (replacing the guideline `release-pr.yml`/`release-manual.yml`), `scripts/chartpin/main.go` + `scripts/chartpin/main_test.go`, `scripts/release-binaries.sh`
- Delete: `.github/workflows/release-pr.yml`
- Modify: `Makefile` (target `release-binaries`)

**Interfaces:**
- Consumes: `cliff.toml`, `scripts/lint-workflows.sh` (Task 6); chart values from Task 3 (`image.repository`, `image.digest`, `awsLabs.workspaceImage`, `toolboxImage`).
- Produces: `go run ./scripts/chartpin -values deploy/helm/crucible/values.yaml -server ghcr.io/platform-infra-org/crucible@sha256:… -workspace …@sha256:… -toolbox …@sha256:…` (rewrites the file in place; refuses a reference without `@sha256:<64 hex>`); `scripts/release-binaries.sh <version> <outdir>` → `crucible_<version>_<os>_<arch>.tar.gz` for darwin/linux × amd64/arm64 + `SHA256SUMS`.

- [ ] **Step 1: Failing tests.** `scripts/chartpin/main_test.go`: pinning a copy of the real `values.yaml` sets `image.repository=ghcr.io/platform-infra-org/crucible`, `image.digest=sha256:<a…>`, `awsLabs.workspaceImage` and `toolboxImage` to the given refs, leaves every other key equal; a ref with a tag but no digest, or a short digest, exits non-zero. A shell test in `scripts/release-binaries.sh`'s own `--self-test` mode is unnecessary: verify by running it (Step 4). Run `go test ./scripts/chartpin`: FAIL.

- [ ] **Step 2: Implement `chartpin`** with `gopkg.in/yaml.v3` (node-level edit so comments and order survive), and `scripts/release-binaries.sh` (`CGO_ENABLED=0 GOOS=… GOARCH=… go build -trimpath -ldflags "-s -w -X crucible/internal/docs.Version=$1" -o … ./cmd/crucible`, tar, `shasum -a 256` / `sha256sum` whichever exists), `make release-binaries VERSION=…`.

- [ ] **Step 3: Workflows**, following the guideline's structure (git-cliff install pinned `2.13.1`, version computation and the PR-title fallback, CHANGELOG commit + tag, `[skip ci]`, release only for releasing commit types, App-token optional):
  - `release.yml`: `pull_request` to main `[opened, synchronize, reopened, closed]`. Not merged → `test-build` job: `make tools`, build the three images with `docker/build-push-action@v6` (`push: false`, platforms `linux/amd64,linux/arm64` via `docker/setup-qemu-action@v3` + `docker/setup-buildx-action@v3`): server `context: .`/`Dockerfile`, workspace `deploy/workspace`, toolbox `context: .`/`deploy/toolbox/Dockerfile`; `helm package deploy/helm/crucible`; `scripts/release-binaries.sh <next>-pr.<n> dist`; upload `dist/` as an artifact. Merged → `release` job: permissions `contents: write, packages: write, id-token: write, attestations: write`; version + CHANGELOG + tag as the guideline; login to GHCR with `GITHUB_TOKEN`; build+push each image tagged `<version>` (and `latest` for workspace/toolbox), capture digests (`steps.<id>.outputs.digest`); attest provenance + SBOM (`anchore/sbom-action@v0`) per image; `go run ./scripts/chartpin …` with the three `<repo>@<digest>`; `helm package deploy/helm/crucible --version "${VERSION#v}" --app-version "$VERSION"`; `helm push crucible-*.tgz oci://ghcr.io/platform-infra-org/charts` (login with `helm registry login ghcr.io -u "$GITHUB_ACTOR" --password-stdin`); `scripts/release-binaries.sh "$VERSION" dist`; notes from git-cliff plus a "Images" section with each `<repo>@<digest>` and the chart line `helm install … --version <v>`; `softprops/action-gh-release@v2` with `dist/*` and the SBOMs.
  - `release-manual.yml`: `workflow_dispatch` (optional `version` input); version `<next>-<sanitised branch>.<run_number>` unless overridden (branch name sanitised to `[0-9A-Za-z-]` in a step that reads it from `env:`); same artifacts; `prerelease: true`; no `latest` tags.
  - The image name stays the fixed `ghcr.io/platform-infra-org/...` from the Global Constraints (not computed from `github.repository`).

- [ ] **Step 4: Verify** `go test ./scripts/chartpin`; `bash scripts/release-binaries.sh v0.0.0-test /private/tmp/…/rb && ls` (4 tarballs + SHA256SUMS; delete them after); `bash scripts/lint-workflows.sh`.

- [ ] **Step 5: Commit** `ci: releases on merge (binaries, images, pinned OCI chart) and a manual prerelease workflow`.

### Task 8: Operator docs and consistency (after the lanes merge)

**Files:**
- Modify: `docs/runbooks/aws.md`, `docs/aws-deploy.html`, `docs/deploy-architecture.html` (#ship and the deploy.sh / start-up sections), `docs/test-guide.html`, `CLAUDE.md` (Commands table: build is one binary; `crucible serve`; deploying by version; CI), `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` (a decision line)

- [ ] **Step 1** Describe what exists, verified against the code: the release flow (what each workflow does and when), making the workspace/toolbox packages public once (Package settings → visibility), the read-only token (`read:packages`) as `ghcr_pull_token`, `crucible aws deploy` / `--version` / `--build` (needs `docker login ghcr.io` and `helm registry login ghcr.io`), the migration (teardown → up for an existing node), `status` showing the release, firewall hosts (`ghcr.io`, `pkg-containers.githubusercontent.com`), and that the server no longer ships through S3. Remove every description of release tarballs in S3.
- [ ] **Step 2** Checks: the HTML id/anchor loop and tag balance on changed pages; `go test ./internal/docs`; `grep -rn 'crucible-api\|crucible-agent\|crucible-ready\|current-release\|crucible-image.tar' docs/*.html docs/runbooks CLAUDE.md` prints nothing.
- [ ] **Step 3: Commit** `docs: one binary, releases from CI, deploying a GHCR release`.

## Final check (controller, after Task 8)

```bash
gofmt -l cmd internal scripts; go vet ./...; go test -race ./...
for s in persistent main labs; do (cd deploy/aws/$s && terraform test); done
bash deploy/helm/test.sh; bash deploy/workspace/test.sh; bash scripts/lint-workflows.sh
(cd web && npm test && npx tsc -b && npm run lint && npm run build)
make build && ./bin/crucible version && ./bin/crucible serve --help 2>&1 | head -2
KEYCLOAK_PORT=8082 make cluster-check   # includes local-check
```
