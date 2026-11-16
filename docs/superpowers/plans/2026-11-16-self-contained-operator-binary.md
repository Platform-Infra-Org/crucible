# Self-contained operator binary — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator downloads `crucible`, and nothing else but Terraform, to deploy and reach an install — no AWS CLI, no Session Manager plugin, no Go, no checkout.

**Architecture:** Replace the 27 `aws` CLI subprocesses in `internal/awsops` with the AWS SDK behind one narrow interface, following the `Cloud`/`Client`/`Fake` pattern already in `internal/awscloud`. Publish the stack outputs the eleven everyday operations read into an SSM parameter so only `init`, `labs-init`, `up` and `teardown` need Terraform. Embed `deploy/aws/**` with `go:embed` into an application directory that also holds the persistent stack's local state. Run the Session Manager port-forward in-process over `coder/websocket`, behind a fallback to the plugin.

**Tech Stack:** Go 1.26, `aws-sdk-go-v2` (`ssm` and `cognitoidentityprovider` are new modules; `sso`/`ssooidc` promoted from indirect), `coder/websocket`, `go:embed`, `gopkg.in/yaml.v3` if already present else `sigs.k8s.io/yaml`.

**Spec:** `docs/superpowers/specs/2026-11-16-self-contained-operator-binary-design.md`

## Global Constraints

- **Never create real AWS resources.** Every AWS path is tested against `httptest` (the `Endpoint` field pattern from `internal/awscloud/client.go:44`) or a package-local fake. Real-AWS verification is a by-hand runbook step for the owner.
- Terraform only with mock providers (`terraform test`, `validate`).
- Linux and macOS, amd64 and arm64 only. No Windows asset, no Windows behaviour claimed — but no POSIX-only path handling and no `$HOME`-shaped assumptions: the config and application directories resolve through one per-platform helper from the first task.
- One account holds Crucible and the labs. `labs-init`'s existing flags, `--crucible-account` and `--var-file` keep working unchanged.
- Checks that must pass before every commit: `gofmt -l cmd internal` (empty), `go vet ./...`, `go test -race ./...`.
- Prefix every shell: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`.
- `go test ./internal/docs` fails when a page documents something that no longer exists, so `docs/aws-deploy.html` and `docs/cli.md` change in the same commit as the behaviour they describe.
- Commit with the pathspec form: `git commit -m "…" -- <paths>`. Never `git add -A`. Trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

Five conditions the spec implies that no task's happy path exercises. Each has its test added to the task that owns the code.

1. **An expired or absent SSO token.** Every command must fail saying to run `crucible aws login`, never with a raw SDK credential error. Task 1.
2. **An empty application directory and an unreadable state bucket.** Access-denied must stop before any apply, exactly as today's `init` does — a `terraform apply` against empty state is how a duplicate persistent stack with duplicate Cognito pools gets created. Task 8.
3. **A config key absent on a re-run.** Must keep the deployed value, never reset it: dropping `labs:` must not destroy the AWS Budget, and dropping `access:` must not flip the install back to public. Task 6.
4. **The install parameter missing**, because the install was created by an older release. Commands must name `crucible aws up` rather than fail obscurely. Task 7.
5. **A port-forward that dies mid-stream**, or a node that is asleep. `connect` must not leave a local listener accepting connections while reporting success. Task 9.

---

### Task 1: An AWS client seam, and credential resolution

**Files:**
- Create: `internal/awsops/aws.go`
- Create: `internal/awsops/aws_test.go`
- Create: `internal/awsops/paths.go`
- Modify: `internal/awsops/awsops.go:26-60` (the `Ops` struct and `Default`)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type AWS interface` in `internal/awsops/aws.go`, grown one method group per later task. Task 1 defines it with `Account(ctx context.Context) (string, error)` and `Region() string`.
  - `func NewAWS(ctx context.Context, c AWSConfig) (AWS, error)`
  - `type AWSConfig struct { Region, Profile, SSOStartURL, SSORegion, Account, Role, Endpoint string }` — `Endpoint` is tests only, every service at that URL, mirroring `awscloud.Config.Endpoint`.
  - `Ops.AWS AWS` — a new field on `Ops`; `Default` leaves it nil and it is constructed by the caller in `cmd/crucible`.
  - `func ConfigDir() (string, error)` and `func AppDir() (string, error)` in `internal/awsops/paths.go` — per-platform, `CRUCIBLE_CONFIG` and `CRUCIBLE_HOME` overriding.
  - `var ErrNotSignedIn = errors.New("not signed in to AWS: run `crucible aws login`")`

- [ ] **Step 1: Write the failing tests**

```go
// aws_test.go
func TestNewAWSUsesTheConfiguredSSOProfile(t *testing.T)
// AWSConfig{SSOStartURL: "https://example/start", SSORegion: "il-central-1",
//   Account: "111122223333", Role: "Admin", Region: "il-central-1"}
// Asserts: the resolved aws.Config has Region "il-central-1" and a credentials
// provider that reads the SSO token cache, not the environment.

func TestNewAWSFallsBackToSharedConfig(t *testing.T)
// AWSConfig{Region: "il-central-1"} with no SSO fields and AWS_CONFIG_FILE
// pointed at a testdata profile. Asserts Region resolves from the file.

func TestAccountReportsNotSignedIn(t *testing.T)
// httptest server returning 400 with an ExpiredTokenException body.
// Asserts errors.Is(err, ErrNotSignedIn) and that err.Error() contains
// "crucible aws login".   <-- Review Focus 1

func TestAppDirHonoursOverride(t *testing.T)
// CRUCIBLE_HOME set -> AppDir returns it; unset -> a path under the
// per-platform user config dir, built with filepath.Join and no "/" literal.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestNewAWS|TestAccount|TestAppDir' -v`
Expected: FAIL, `undefined: NewAWS`.

- [ ] **Step 3: Add the SDK modules**

Run: `go get github.com/aws/aws-sdk-go-v2/service/ssm github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider github.com/aws/aws-sdk-go-v2/service/sso github.com/aws/aws-sdk-go-v2/service/ssooidc`

- [ ] **Step 4: Implement `NewAWS`, `AWSConfig`, `Account`, `Region`, `ConfigDir`, `AppDir`, `ErrNotSignedIn`**

`NewAWS` calls `awsconfig.LoadDefaultConfig` with `awsconfig.WithRegion` and, when `SSOStartURL` is set, an SSO credentials provider over the shared token cache; with it unset it passes only the region, so the default chain reads `~/.aws/config` for free. `AWS_PROFILE` and the other `AWS_*` variables must keep overriding, which `LoadDefaultConfig` gives as long as `AWSConfig.Profile` is passed through `WithSharedConfigProfile` only when non-empty. Map the SDK's `ExpiredToken`, `AccessDeniedException` on `sso`, and a missing token cache onto `ErrNotSignedIn` with `%w`. `ConfigDir`/`AppDir` build on `os.UserConfigDir`.

- [ ] **Step 5: Run the tests and the checks**

Run: `go test -race ./internal/awsops && gofmt -l cmd internal && go vet ./...`
Expected: PASS, `gofmt` prints nothing.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(awsops): an AWS SDK seam with SSO and shared-config credential resolution" -- internal/awsops go.mod go.sum
```

---

### Task 2: Migrate the SSM calls

**Files:**
- Modify: `internal/awsops/aws.go`, `internal/awsops/awsops.go`
- Modify: `internal/awsops/aws_test.go`, `internal/awsops/awsops_test.go`

**Interfaces:**
- Consumes: `AWS`, `AWSConfig`, `ErrNotSignedIn` from Task 1.
- Produces, added to `AWS`:
  - `GetParameter(ctx context.Context, name string, decrypt bool) (string, error)`
  - `PutParameter(ctx context.Context, name, value string, secure bool) error`
  - `DeleteParameter(ctx context.Context, name string) error`
  - `SendCommand(ctx context.Context, instanceID, document string, params map[string][]string) (string, error)` — returns the command id
  - `CommandResult(ctx context.Context, commandID, instanceID string) (status, stdout, stderr string, err error)`
  - `InstanceOnline(ctx context.Context, instanceID string) (bool, error)`

- [ ] **Step 1: Write the failing tests**

Against an `httptest` server asserting the request target and body for each call, plus:

```go
func TestGetParameterNotFoundIsDistinguishable(t *testing.T)
// ParameterNotFound must come back as a sentinel the callers can test,
// because Deploy's rollback path depends on telling "absent" from "failed".

func TestCommandResultSurfacesStderrOnFailure(t *testing.T)
// status "Failed" with a stderr body -> err names the stderr text, matching
// today's behaviour at awsops.go:636.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestGetParameter|TestCommandResult' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the six methods, then replace the 10 `"aws", "ssm"` call sites in `awsops.go`**

The sites are the three `get-command-invocation`, two `get-parameter`, and one each of `put-parameter`, `delete-parameter`, `send-command`, `describe-instance-information` and `start-session`. Leave `start-session` alone — Task 9 owns it. Keep the existing polling loop and its `Sleep` seam; only the call underneath changes.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/awsops`
Expected: PASS. The existing `awsops_test.go` recorder assertions for `aws ssm …` strings must be rewritten against the fake, not deleted.

- [ ] **Step 5: Commit**

```bash
git commit -m "refactor(awsops): SSM parameters and Run Command go through the SDK" -- internal/awsops
```

---

### Task 3: Migrate the EC2 calls

**Files:**
- Modify: `internal/awsops/aws.go`, `internal/awsops/awsops.go`, and both test files.

**Interfaces:**
- Consumes: Task 1, Task 2.
- Produces, added to `AWS`:
  - `type Instance struct { ID, State, PublicURL string }`
  - `FindInstances(ctx context.Context, nameTag string, states ...string) ([]Instance, error)`
  - `Subnet(ctx context.Context, id string) (vpcID, cidr string, err error)`
  - `StartInstance(ctx context.Context, id string) error`
  - `StopInstance(ctx context.Context, id string) error`
  - `WaitInstance(ctx context.Context, id, state string) error`
  - `SerialConsoleEnabled(ctx context.Context) (bool, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestFindInstancesReadsThePublicURLTag(t *testing.T)
// Asserts the crucible:public-url tag lands in Instance.PublicURL and an
// absent tag yields "", preserving Connect's checks at awsops.go.

func TestWaitInstanceStopsOnContextCancel(t *testing.T)
// A context cancelled mid-wait returns ctx.Err(), never blocks.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestFindInstances|TestWaitInstance' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the seven methods, then replace the 8 `"aws", "ec2"` call sites**

`WaitInstance` uses the SDK's `NewInstanceStoppedWaiter`/`NewInstanceRunningWaiter` rather than a hand-rolled loop.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/awsops`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "refactor(awsops): EC2 instance and subnet calls go through the SDK" -- internal/awsops
```

---

### Task 4: Migrate S3, Cognito and tagging — the AWS CLI is gone

**Files:**
- Modify: `internal/awsops/aws.go`, `internal/awsops/awsops.go`, both test files.
- Modify: `docs/aws-deploy.html` (the Tools table), `docs/cli.md`.

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces, added to `AWS`:
  - `GetObject(ctx context.Context, bucket, key, toFile string) error`
  - `PutObject(ctx context.Context, bucket, key, fromFile string) error`
  - `HeadObject(ctx context.Context, bucket, key string) (bool, error)`
  - `CreateUser(ctx context.Context, poolID, email, name string) error`
  - `ResendInvite(ctx context.Context, poolID, email string) error`
  - `UserStatus(ctx context.Context, poolID, email string) (string, error)`
  - `TaggedResources(ctx context.Context, tag string) ([]string, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateUserAlwaysVerifiesTheEmail(t *testing.T)
// Asserts the AdminCreateUser request carries email_verified=true and
// DesiredDeliveryMediums=[EMAIL]. This is the documented guarantee of
// `crucible aws invite`, so it keeps a test that fails without the fix.

func TestUserStatusNotFoundIsDistinguishable(t *testing.T)
// UserNotFoundException -> a sentinel, because Invite branches on it
// (awsops.go:1019) to choose create versus resend.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestCreateUser|TestUserStatus' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the seven methods and replace the remaining call sites, including `aws configure get region`, which becomes `AWS.Region()`**

- [ ] **Step 4: Verify no `aws` subprocess remains**

Run: `grep -n '"aws",' internal/awsops/*.go`
Expected: only the `start-session` site that Task 9 owns, and nothing in non-test files afterwards.

- [ ] **Step 5: Update the docs in the same commit**

Remove the AWS CLI row from the Tools table in `docs/aws-deploy.html` and strike the "needs the AWS CLI" phrases from `crucible aws connect`'s and the other entries' descriptions in `docs/cli.md`, leaving the Session Manager plugin until Task 9.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/awsops ./internal/docs && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git commit -m "refactor(awsops)!: S3, Cognito and tagging go through the SDK; the AWS CLI is no longer required" -- internal/awsops docs/aws-deploy.html docs/cli.md
```

---

### Task 5: `crucible aws login`

**Files:**
- Create: `internal/awsops/login.go`, `internal/awsops/login_test.go`
- Modify: `cmd/crucible/main.go` (subcommand registration), `docs/cli.md`, `docs/aws-deploy.html`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `func (o Ops) Login(ctx context.Context, in LoginOpts) error`
  - `type LoginOpts struct { StartURL, SSORegion, Account, Role string; Open func(url string) error }` — `Open` nil means the real browser opener; tests pass a recorder.
  - `func (o Ops) Whoami(ctx context.Context) (account, role, region string, err error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestLoginWritesTheSharedTokenCache(t *testing.T)
// httptest ssooidc: RegisterClient -> StartDeviceAuthorization -> CreateToken.
// Asserts a file appears under the shared cache directory in the CLI's own
// JSON shape (startUrl, region, accessToken, expiresAt) so `aws` and
// `crucible` are interchangeably signed in.

func TestLoginPollsUntilAuthorizationPending clears(t *testing.T)
// CreateToken returns AuthorizationPendingException twice then a token;
// asserts it retries at the interval the device response gives and succeeds.

func TestLoginDiscoversTheOnlyAccountAndRole(t *testing.T)
// ListAccounts and ListAccountRoles each return exactly one; asserts they are
// used without prompting and reported in the output.

func TestLoginAsksWhenSeveralAccountsMatch(t *testing.T)
// Two accounts -> returns an error naming both, rather than picking one.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run TestLogin -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Login`, `LoginOpts` and `Whoami`**

The device flow is `RegisterClient`, `StartDeviceAuthorization`, open `verificationUriComplete`, then poll `CreateToken` at the returned interval until it yields a token or the expiry passes. Write the cache file with mode 0600 at the path the AWS CLI uses (the SHA-1 of the start URL, hex, under the shared SSO cache directory) so that no second cache exists.

- [ ] **Step 4: Register the subcommand and document it**

Add `login` beside the other `crucible aws` subcommands in `cmd/crucible/main.go`; add its row to `docs/cli.md`'s "Setting up" table and replace the `aws sso login` instruction in `docs/aws-deploy.html` with it.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/awsops ./cmd/crucible ./internal/docs`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(aws): crucible aws login signs in through Identity Center without the AWS CLI" -- internal/awsops cmd/crucible docs/cli.md docs/aws-deploy.html
```

---

### Task 6: `crucible.yaml`

**Files:**
- Create: `internal/awsops/config.go`, `internal/awsops/config_test.go`, `internal/awsops/testdata/crucible.yaml`
- Modify: `cmd/crucible/main.go`, `docs/cli.md`, `docs/aws-deploy.html`

**Interfaces:**
- Consumes: `ConfigDir` from Task 1.
- Produces:
  - `type Config struct` with the nested `Install`, `AWS`, `Access`, `Server`, `Machine`, `Schedule`, `Email`, `Labs` structs and `TerraformVars map[string]string`, keyed exactly as the spec's YAML.
  - `func LoadConfig(path string) (*Config, error)` — `path` empty means the per-platform default; a missing file is not an error, it yields a zero `Config`.
  - `func (c *Config) TFVars(deployed map[string]string) (map[string]string, error)` — the generator. `deployed` is the current stack outputs; **a key absent from the config takes its value from `deployed`, and only then the Terraform default.**
  - `func (c *Config) AWSConfig() AWSConfig`

- [ ] **Step 1: Write the failing tests**

```go
func TestTFVarsKeepsDeployedValuesForAbsentKeys(t *testing.T)
// A config with no labs: block and deployed{"lab_budget_usd":"200"} must
// still yield lab_budget_usd=200. Dropping a block must never delete a
// budget.                                            <-- Review Focus 3

func TestTFVarsAbsentAccessKeepsTheDeployedMode(t *testing.T)
// No access: block, deployed{"access":"ssm"} -> access stays "ssm", never
// reverts to the "public" default.                   <-- Review Focus 3

func TestTFVarsFlagBeatsConfigBeatsDeployed(t *testing.T)
// Precedence, asserted with one key set in all three places.

func TestLoadConfigExpandsEnvReferences(t *testing.T)
// ghcr_pull_token: ${GHCR_TOKEN} -> the environment's value; an unset
// variable is an error naming the key, not an empty token.

func TestLoadConfigRejectsBothAccessModes(t *testing.T)
// mode: ssm together with a domain -> an error naming both.

func TestTerraformVarsPassthroughReachesTFVars(t *testing.T)
// terraform_vars: {k3s_version: v1.2.3} appears in the output; a key the
// typed schema already owns is an error rather than a silent duplicate.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestTFVars|TestLoadConfig|TestTerraformVars' -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Config`, `LoadConfig`, `TFVars`, `AWSConfig`**

Use the YAML library already in `go.mod`; add one only if none is there. Reject unknown top-level keys so a typo is caught rather than ignored.

- [ ] **Step 4: Wire `--config` and `CRUCIBLE_CONFIG` through `cmd/crucible`, and document the file**

`docs/cli.md` gains a section for the file with the full annotated example from the spec; `docs/aws-deploy.html`'s setup steps reference it instead of `crucible.tfvars`. `--var-file` stays documented for developers.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/awsops ./cmd/crucible ./internal/docs`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(aws): crucible.yaml replaces remembered flags; an absent key keeps the deployed value" -- internal/awsops cmd/crucible docs/cli.md docs/aws-deploy.html
```

---

### Task 7: The install parameter — eleven operations stop needing Terraform

**Files:**
- Modify: `internal/awsops/awsops.go` (`outputs` at :66, and its eleven callers)
- Create: `internal/awsops/install.go`, `internal/awsops/install_test.go`
- Modify: `docs/aws-deploy.html`, `docs/cli.md`

**Interfaces:**
- Consumes: `AWS.GetParameter`/`PutParameter` from Task 2.
- Produces:
  - `func (o Ops) publishInstall(ctx context.Context, m map[string]string) error` — writes `/<name>/install` as JSON after a successful apply.
  - `func (o Ops) install(ctx context.Context) (map[string]string, error)` — the replacement for `outputs()` in the eleven everyday operations; reads the parameter, caches it under `AppDir`.
  - `var ErrNoInstall = errors.New("this install predates the install parameter: run `crucible aws up`")`

- [ ] **Step 1: Write the failing tests**

```go
func TestInstallParameterRoundTrips(t *testing.T)
// publishInstall then install returns the same map, via the fake SSM.

func TestInstallSaysToRunUpWhenAbsent(t *testing.T)
// ParameterNotFound -> errors.Is(err, ErrNoInstall) and the message names
// "crucible aws up".                                 <-- Review Focus 4

func TestInstallDoesNotTouchTheEnvParameter(t *testing.T)
// Asserts /<name>/env is never written: it is the node's contract, read by
// deploy.sh.

func TestEverydayOperationsRunWithoutTerraform(t *testing.T)
// An Ops whose Exec fails the test if called with "terraform": Status,
// Deploy, Invite, Snapshot, SleepNode, Wake and Connect must all complete.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestInstall|TestEveryday' -v`
Expected: FAIL.

- [ ] **Step 3: Implement `publishInstall`, `install`, `ErrNoInstall`; call `publishInstall` at the end of `Init`, `LabsInit` and `Up`; switch the eleven everyday callers from `outputs()` to `install()`**

`outputs()` stays, used only by `Init`, `LabsInit`, `Up` and `Teardown`. The parameter holds exactly the keys those eleven read — at least `region`, `name`, `state_bucket`, `data_bucket`, `cognito_user_pool_id`, `instance_id`, `access`, `public_url`, `local_port`, `subnet_id` and the `lab_*` values `Status` reports.

- [ ] **Step 4: Make the four Terraform commands the only ones that look for it**

A missing `terraform` on `PATH` must produce an error naming the required version and `https://developer.hashicorp.com/terraform/install` from `Init`, `LabsInit`, `Up` and `Teardown`, and from nowhere else. Add `TestTerraformMissingOnlyAffectsTheFourCommands`.

- [ ] **Step 5: Document it**

`docs/aws-deploy.html`: Terraform's Tools row becomes "only `init`, `labs-init`, `up`, `teardown`", and the upgrade section says the first command against an existing install must be `crucible aws up`. `docs/cli.md`: the `crucible aws …` preamble says which commands need Terraform.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/awsops ./internal/docs && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git commit -m "feat(aws): the install's details live in SSM, so only init, labs-init, up and teardown need terraform" -- internal/awsops docs/aws-deploy.html docs/cli.md
```

---

### Task 8: Embedded stacks and state adoption

**Files:**
- Create: `internal/awsops/stacks.go`, `internal/awsops/stacks_test.go`
- Modify: `internal/awsops/awsops.go:64` (`dir`)
- Modify: `docs/aws-deploy.html`

**Interfaces:**
- Consumes: `AppDir` from Task 1.
- Produces:
  - `//go:embed all:stacks` over a generated copy of `deploy/aws/{persistent,main,labs}`, or `//go:embed` with a relative path if the build allows it; the plan leaves which to the implementer, but `go test ./...` from a fresh clone must pass without a generate step.
  - `func (o Ops) stackDir(ctx context.Context, stack string) (string, error)` — materialises the stack under `AppDir`, adopts existing state, returns the path. Replaces `dir`.
  - `var ErrNoState = errors.New("no local state and no backup could be read: refusing to apply, which would create a second install")`

- [ ] **Step 1: Write the failing tests**

```go
func TestStackDirWritesTheEmbeddedFiles(t *testing.T)
// A fresh AppDir gains main.tf and variables.tf whose bytes equal
// deploy/aws/<stack>'s.

func TestStackDirAdoptsStateFromACheckout(t *testing.T)
// A terraform.tfstate in an Ops.Root checkout is copied into AppDir on
// first use, and the log says so. A second call does not overwrite it.

func TestStackDirRefusesWhenTheBucketReadIsDenied(t *testing.T)
// Empty AppDir, no checkout, HeadObject returns AccessDenied ->
// errors.Is(err, ErrNoState) and no file is written.  <-- Review Focus 2

func TestStackDirProceedsWhenTheBackupIsAbsent(t *testing.T)
// Empty AppDir, HeadObject returns 404 -> no error: this is a genuinely
// new install, which init is allowed to create.

func TestStackDirKeepsFilesForHandImport(t *testing.T)
// The directory survives the call, so terraform import can be run against
// it by hand.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run TestStackDir -v`
Expected: FAIL.

- [ ] **Step 3: Implement the embed, `stackDir` and `ErrNoState`; replace every `o.dir(` call**

Distinguishing not-found from access-denied is the crux of Review Focus 2: a 404 means a new install, anything else means stop.

- [ ] **Step 4: Add the command that prints where things are**

`crucible aws where` (or a flag on `status` — implementer's choice, documented either way) prints the config path, the application directory, each stack's directory and the resolved Terraform version, for the documented fourteen-resource import procedure.

- [ ] **Step 5: Document it**

`docs/aws-deploy.html`: the Tools table loses the git row; the "Second machine or fresh clone" and "Lost persistent state" sections describe the application directory and name it as the thing to back up.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/awsops ./internal/docs && gofmt -l cmd internal`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git commit -m "feat(aws): the stacks ship inside the binary and adopt existing state; no checkout needed" -- internal/awsops docs/aws-deploy.html
```

---

### Task 9: The in-process port-forward

**Files:**
- Create: `internal/awsops/tunnel.go`, `internal/awsops/tunnel_test.go`
- Modify: `internal/awsops/awsops.go` (`Connect`, and the `LookPath` field)
- Modify: `docs/aws-deploy.html`, `docs/ssm-access.html`, `docs/cli.md`, `docs/runbooks/aws.md`

**Interfaces:**
- Consumes: Task 1, Task 3 (`FindInstances`).
- Produces, added to `AWS`:
  - `StartSession(ctx context.Context, instanceID, document string, params map[string][]string) (streamURL, token string, err error)`
- And in `tunnel.go`:
  - `func Forward(ctx context.Context, streamURL, token string, ln net.Listener, log io.Writer) error` — serves until the context ends or the stream closes, returning the first error.
  - `ConnectOpts` gains `UsePlugin bool`, set by `--use-plugin`.

- [ ] **Step 1: Write the failing tests**

```go
func TestForwardCarriesBytesBothWays(t *testing.T)
// A local httptest WebSocket server speaking the agent-message framing:
// bytes written to a connection on ln arrive as framed payloads, and framed
// payloads arrive back on the connection.

func TestForwardAcknowledgesAndOrdersSequences(t *testing.T)
// Out-of-order sequence numbers are reassembled; each data message is
// acknowledged. This is the part a wrong guess breaks silently.

func TestForwardClosesTheListenerWhenTheStreamDies(t *testing.T)
// The server closes mid-stream -> Forward returns an error AND ln no longer
// accepts, so nothing reports success with a dead tunnel.  <-- Review Focus 5

func TestConnectRefusesAnAsleepNode(t *testing.T)
// FindInstances returns nothing running -> the error names
// `crucible aws wake` and no listener is opened.          <-- Review Focus 5

func TestConnectWithUsePluginStillShellsOut(t *testing.T)
// --use-plugin -> the LookPath seam and the start-session subprocess are
// used, so the fallback is real.
```

- [ ] **Step 2: Run them and verify they fail**

Run: `go test ./internal/awsops -run 'TestForward|TestConnect' -v`
Expected: FAIL.

- [ ] **Step 3: Implement `StartSession` and `Forward`, and branch `Connect` on `UsePlugin`**

The session is a WebSocket to `streamURL` opened with `coder/websocket`, authenticated by sending the token in an initial handshake message, then binary agent-messages carrying sequence numbers, message type and payload, each data message acknowledged. Defaulting to the in-process path with `--use-plugin` as the escape is the shape; the plugin path and its `LookPath` seam stay exactly as they are.

- [ ] **Step 4: Document it, including the by-hand gate**

`docs/aws-deploy.html` and `docs/cli.md`: the Session Manager plugin becomes optional, needed only with `--use-plugin`. `docs/ssm-access.html`: the tunnel is in-process, with nothing to install. `docs/runbooks/aws.md`: a step for the owner to verify `crucible aws connect` against the real install, and a note that `--use-plugin` is the fallback until that has been done.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/awsops ./internal/docs && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(aws): crucible aws connect forwards the port itself; the Session Manager plugin is a fallback" -- internal/awsops docs cmd/crucible
```

---

### Task 10: The operator's two downloads, documented

**Files:**
- Modify: `docs/aws-deploy.html`, `docs/cli.md`, `README.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: every task.
- Produces: nothing in code.

- [ ] **Step 1: Rewrite the prerequisites**

`docs/aws-deploy.html`'s Tools table becomes two rows for an operator — `crucible` and Terraform — with Docker and Helm kept for `deploy --build` and Go moved to a developer-only note. Add the two get-started sequences from the spec: three steps for someone joining an install, six for someone creating one.

- [ ] **Step 2: Note what is not covered**

One line each: Windows is not built yet, and `deploy --build` refuses there; a two-account install uses the existing flags and `--var-file`.

- [ ] **Step 3: Update `CLAUDE.md`**

The Toolchain section says `make tools` is for developers, and the Commands table gains `crucible aws login` and the `crucible.yaml` path.

- [ ] **Step 4: Run the docs test**

Run: `go test ./internal/docs`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "docs: an operator needs crucible and terraform, and nothing else" -- docs CLAUDE.md README.md
```

---

## Self-review notes

- **Spec coverage.** §1 config → Task 6; §2 no AWS CLI → Tasks 1-4, login → Task 5; §3 no repository → Task 8; §4 Terraform for four commands → Task 7 steps 3-4; §5 no plugin → Task 9; §6 adopting the install → Task 8; §7 Windows → non-goal, held by the Global Constraints line and Task 10 step 2.
- **Review Focus coverage.** 1 → Task 1 step 1; 2 → Task 8 step 1; 3 → Task 6 step 1 (two tests); 4 → Task 7 step 1; 5 → Task 9 step 1 (two tests).
- **Deliberately left open:** which YAML library, whether the embed needs a generate step, whether `where` is a command or a `status` flag, the exact install-parameter key names beyond the listed minimum, and how far `awsops_test.go`'s existing recorder assertions are rewritten versus replaced.
- **Not in this plan:** deleting the `LookPath` seam, and any Windows asset. Both are later changes, gated on the owner's by-hand verification.
