# A self-contained operator binary

Date: 2026-11-16
Status: design, approved for planning

## Intent

Running or reaching a Crucible install today means installing five things by hand: Go, Terraform,
the AWS CLI, the Session Manager plugin, and a git clone of this repository. That is a developer's
setup imposed on everyone, and it does not work on Windows at all, because the Session Manager
plugin and the AWS CLI both need separate installers and nothing in CI builds a Windows binary.

An operator should download one file for their platform, run it, and deploy and connect. The only
other thing they install is Terraform. Everything else moves inside the binary.

The owner's words: *"I want to install the binary and be able to deploy and connect to the
environment. I don't want user installing go, terraform, aws, ssm — I want it to come with the
app."* On Terraform specifically, after we established it cannot be compiled in: *"don't make it
download terraform. everything else is in the binary."*

## Goals

- An operator joining an existing install needs **one download**: `crucible`. Then
  `crucible aws login` and `crucible aws connect`, and nothing else.
- An operator creating an install needs **two downloads**: `crucible` and Terraform.
- Nobody needs the AWS CLI, the Session Manager plugin, Go, or a checkout of this repository.
- Linux and macOS, amd64 and arm64 — the four assets CI already builds.
- One hand-edited configuration file replaces remembering flags on each new machine.
- The install that already exists in the account is adopted, not replaced.
- `~/.aws/config` keeps working for anyone who already has it, so Crucible stays compatible with
  other AWS tooling.

## Non-goals

- **Developing Crucible.** Writing Go, running `go test`, `local up`, `cluster-check` and the
  Terraform suites all keep needing the toolchain. `make tools` stays exactly as it is, and this
  work does not touch it.
- **Downloading or bundling Terraform.** Explicitly ruled out by the owner. Terraform is a
  documented prerequisite for the four commands that need it, and the binary must never fetch it.
- **Removing the repository for developers.** The stacks stay in `deploy/aws/` as the source of
  truth; the binary embeds a copy at build time.
- Air-gapped operation. `crucible aws login` needs a browser and network, as any SSO flow does.
- **Windows, for now.** The owner has deferred it: *"don't concern yourself with windows now, I will
  create a vm or something or use my windows computer to build and test later. focus on linux and
  mac."* Nothing in this work may make Windows harder to add — no POSIX-only path handling, no
  assumption of a `$HOME`-shaped config directory — but no Windows asset is built and no Windows
  behaviour is claimed.
- **Two-account installs.** The owner's layout is one account holding both Crucible and the labs
  (*"just copy what my coworker did"*). The config carries one `aws:` block. The existing
  `labs-init` flags, including `--crucible-account`, and `--var-file` keep working unchanged, so a
  two-account install stays possible exactly as it is today — it simply gains nothing from the
  config file. Extending `aws:` to named per-account profiles is a later change if anyone needs it.

## Anti-goals

Things that would make this a failure even if it technically shipped:

- **An install that no longer matches itself.** Values the stacks compute (Cognito client details,
  the public URL, the fourteen `lab_*` variables, k3s and sysbox versions and checksums) must not
  become hand-editable. They stay wired from stack outputs.
- **A second source of truth for AWS credentials.** If both `crucible.yaml` and `~/.aws/config`
  describe a sign-in, the rule for which wins must be one sentence long and never surprise anyone.
- **Losing the recovery path.** `docs/aws-deploy.html` documents importing fourteen resources by
  hand when persistent state is lost. That procedure must remain possible with the embedded stacks.
- **A second install.** The account already holds a live install, and its persistent state is a
  local file in someone's checkout. Adopting the wrong state, or none, creates a duplicate
  persistent stack with duplicate Cognito pools and buckets. Nothing here may be able to do that
  silently.
- **Losing the only way in.** `connect` over SSM is the sole route to the app. The in-process tunnel
  may not replace the plugin until it has been proven against real AWS by hand.
- Silently different Terraform versions between two operators on the same Crucible release.

## Constraints

From `CLAUDE.md`, in force throughout:

- **Never create real AWS resources.** Terraform only with mock providers; AWS code tested against
  `awscloud.Fake` or `httptest`. Real-AWS verification is a by-hand runbook step for the user.
- Tests first for behaviour changes; every security or money path keeps a test that fails without
  the fix.
- Any capability change updates `docs/user` in the same commit. `go test ./internal/docs` fails when
  a page covers something that no longer exists.
- `docs/aws-deploy.html` and `docs/cli.md` both document every `crucible aws` step and flag, and
  both must change with this work.

## Current state, as measured

- `internal/awsops/awsops.go` shells out to the `aws` CLI at **27 call sites**: `ssm` 10, `ec2` 8,
  `s3`/`s3api` 3, `cognito-idp` 2, `sts` 1, `resourcegroupstaggingapi` 1, `aws configure get` 1.
  The `cognito-idp` calls (`Invite`, awsops.go:1008 and :1018) build their argv dynamically, so a
  literal grep misses them.
- `o.dir(stack)` (awsops.go:64) resolves stacks as `o.Root + "deploy/aws/" + stack`, which is why
  the checkout is required.
- `o.outputs(stack)` (awsops.go:66) runs `terraform output -json` and is called by **eleven**
  operations: `Init`, `LabsInit`, `Up`, `Teardown`, `Deploy`, `Status`, `Invite`, `Snapshot`,
  `SleepNode`, `Wake`, `backupState`. This is why Terraform is needed far beyond deploying.
- `Connect` is already Terraform-free: it finds the node by the `Name` tag and reads the
  `crucible:public-url` tag.
- A `LookPath` seam already exists for the Session Manager plugin (awsops.go:32, :57).
- Already direct dependencies: `aws-sdk-go-v2` with `config`, `credentials`, `ec2`, `s3`, `sts`,
  `resourcegroupstaggingapi`, `cloudtrail`; `sso` and `ssooidc` are present as indirect;
  `github.com/coder/websocket` is direct.
- `internal/db/db.go:16` embeds the migrations, so `go:embed` has precedent here.
- Persistent-stack state is a **local file** backed up to the state bucket; main and labs use the S3
  backend. Local state is unavoidable for persistent, because on the first `init` the bucket it
  would live in does not exist yet.
- `scripts/release-binaries.sh` builds darwin and linux × amd64 and arm64. No Windows.

**The whole change adds two new Go modules:** `aws-sdk-go-v2/service/ssm` and
`aws-sdk-go-v2/service/cognitoidentityprovider`. `sso` and `ssooidc` get promoted from indirect to
direct. The tunnel needs no new dependency because `coder/websocket` is already there.

## The design

### 1. Configuration

One file, read from the platform's configuration directory:

- Linux `~/.config/crucible/crucible.yaml`
- macOS `~/Library/Application Support/crucible/crucible.yaml`
- Windows `%AppData%\crucible\crucible.yaml`

Overridable with `--config` or `CRUCIBLE_CONFIG`. Mode 0600, because it holds tokens.

Shape, with every block that a human decides:

```yaml
install:
  name: crucible
  region: il-central-1

aws:                           # optional; absent means "use ~/.aws/config"
  sso_start_url: https://identitycenter.amazonaws.com/ssoins-…
  sso_region: il-central-1
  account: "…"                 # optional; discovered after login when unambiguous
  role: Workload-Admin-PS      # optional; likewise

access:
  mode: ssm                    # or: public
  subnet_id: subnet-…          # ssm only
  local_port: 18080            # ssm only
  # domain / acme_email / route53_zone_id for mode: public

server:
  bootstrap_admin: …
  ghcr_pull_token: …
  git_credentials: …
  git_bot_name: Crucible Bot
  git_bot_email: …

machine:   { instance_type: t3a.xlarge, disk_gb: 100 }
schedule:  { enabled: true, timezone: …, labs_bedtime: …, backup: …, sleep: …, wake: … }
email:     { smtp_addr: …, smtp_from: …, smtp_username: …, smtp_password: … }
labs:      { regions: [...], vpc_id: …, subnet_ids: [...], budget_usd: …, budget_emails: [...],
             monthly_account_limit_usd: …, billing_export: false,
             account_public_access_block: false }
```

Secret values accept `${ENV_VAR}` references so they need not sit in the file.

**An absent key means "keep what is deployed", not "use the default".** This preserves today's
behaviour, where an omitted flag on a re-run of `init` or `labs-init` keeps the stack's current
value and never resets it (awsops.go:100-130, and `LabsInit`'s doc comment). A declarative file
would otherwise invert that: deleting a `labs:` block would silently destroy the AWS Budget, and
dropping `access:` would flip the install back to public. Precedence is **flag > config file >
currently deployed value > Terraform default**. Removing a guardrail — a budget, the account limit,
the access mode — requires an explicit flag saying so, and carries a test that fails without it, as
CLAUDE.md requires of every money path.

The typed schema above does not cover all 47 variables in `deploy/aws/main/variables.tf`. A raw
`terraform_vars:` map passes anything else straight through (`route53_zone_id`, `workspace_image`,
`toolbox_image`, a pinned `k3s_version`), so an operator is never blocked on a schema change and a
release. Values the stacks compute stay out of both the schema and the passthrough.

The file replaces `crucible.tfvars` as the operator's input. The builder generates tfvars from it
when invoking Terraform; the tfvars file becomes an internal detail, not something anyone edits.
`--var-file` stays for developers.

**Credential resolution, in one sentence:** if `crucible.yaml` has an `aws:` block, Crucible uses
it; otherwise it falls back to the SDK's default chain, which reads `~/.aws/config` and its SSO
cache. Supporting the fallback costs no code — it is what `config.LoadDefaultConfig` does when told
nothing. `AWS_PROFILE` and the other `AWS_*` environment variables override the `aws:` block, so a
shell that is already pointed at an account wins.

**There is one token cache, and it is the standard one.** `crucible aws login` writes
`~/.aws/sso/cache` in the AWS CLI's own format rather than inventing a private store. An operator
who already ran `aws sso login` is already signed in as far as Crucible is concerned, and one who
runs `crucible aws login` is signed in as far as every other AWS tool is concerned. The cost is a
compatibility contract with that cache's file naming and JSON shape; the alternative is logging in
twice and two caches that can disagree.

### 2. No AWS CLI

All 27 call sites move to the SDK. Notes for the builder:

- `aws ec2 wait instance-stopped|instance-running` become the SDK's waiters.
- `aws s3 cp` and `s3api head-object` become `GetObject`/`PutObject` (or the transfer manager) and
  `HeadObject`.
- `aws configure get region` becomes the resolved config's region.
- `aws sts get-caller-identity` becomes `GetCallerIdentity`, which is also the check that the
  operator is signed in.

**`crucible aws login`** is new and performs the Identity Center device-authorization flow with
`ssooidc`: `RegisterClient`, `StartDeviceAuthorization`, open the verification URL in a browser,
poll `CreateToken`, cache the token. The SDK can refresh and read a cached SSO token but cannot
create one, so this command has to exist.

After a successful login, when `account` or `role` is absent, call `sso:ListAccounts` and
`sso:ListAccountRoles`. Exactly one result means use it and print which it chose; several mean ask
once and write the answer back into the config. The start URL is organisation-wide, not a secret
and not per-account, so it may ship as a build-time default.

### 3. No repository

`go:embed` carries `deploy/aws/{persistent,main,labs}` into the binary. On use, the files are
written to a durable application directory next to the config — **not** a temporary directory,
because the persistent stack's state file lives beside them and must survive.

That directory therefore becomes the thing worth backing up. `init` already restores persistent
state from the state bucket when it finds none locally, so the existing recovery path covers a lost
directory.

Two escape hatches, for the documented fourteen-resource import procedure:

- a command that prints where the stacks were written and which Terraform is in use;
- the written-out stacks stay in place rather than being cleaned up, so `terraform import` can be
  run against them by hand.

### 4. Terraform, required but never fetched

Terraform is resolved from `PATH` only. It is needed by exactly four commands — `init`,
`labs-init`, `up`, `teardown` — and by nothing else.

When it is missing, those four fail with a message naming the required version and the download
page. Every other command must not mention Terraform, must not look for it, and must work on a
machine where it was never installed.

Reaching that state requires removing Terraform from the other eleven operations. `outputs()` only
*reads* values Terraform computed earlier, so the fix is to publish them.

The existing `/<name>/env` parameter (main.tf:99) cannot serve: it is the node's own contract, read
by `deploy.sh`, and it carries none of `state_bucket`, `cognito_user_pool_id`, `local_port` or
`subnet_id` — exactly the values `backupState`, `invite`, `connect` and `teardown` need. So
**`init`, `labs-init` and `up` write a separate install parameter** (say `/<name>/install`) with the
SDK after a successful apply, holding precisely the values the eleven operations read from
`outputs()`. `/<name>/env` is left alone.

Every other command reads that parameter. A local file in the application directory may cache it to
save a round trip, but the parameter is the source of truth — which is what makes a second
operator's machine work with no Terraform and no checkout. Nothing repairs the cache by running
Terraform.

This needs no stack change, because `up` already holds the outputs when it writes them. It does
mean the first command run against the existing install must be an `up` (which needs Terraform, and
the owner has it); until then the parameter is absent, and commands that need it must say so and
name `crucible aws up` rather than fail obscurely.

Pinning the version is the builder's call between compiling the expected version in and checking
`terraform version` at startup; the requirement is only that two operators on the same Crucible
release cannot silently run different Terraform.

### 5. No Session Manager plugin

`crucible aws connect` runs the port-forward in-process. `ssm:StartSession` with
`AWS-StartPortForwardingSession` returns a stream URL and a token; the session is a WebSocket
carrying AWS's binary agent-message framing, which `coder/websocket` can drive. The existing
`LookPath` seam is removed along with the plugin requirement.

This is the highest-risk item in the design, because the framing is not a published API and
`connect` is the only route to the app. The owner confirmed it is in scope: *"I need the ssm no
other way."*

Therefore: **the plugin path stays as a fallback** behind the existing `LookPath` seam, selectable
by a flag, until the Go tunnel has been verified by hand against the real install. An automated
test can only check our encoder against our own decoder, so that by-hand run is the gate, and it
belongs in the runbook as a step the owner performs. The seam is deleted in a later change, not
this one.

`ssm:SendCommand` and `ssm:GetCommandInvocation` — how every remote command on the node runs — are
ordinary API calls and need none of this session machinery. They belong with the SDK migration in
step 1, not here.

`crucible aws deploy --build` shells out to a docker and make build of this tree. It cannot work on
Windows and must refuse there with a clear message rather than fail obscurely; `--version` is
unaffected.

### 6. Adopting the existing install

An install already exists in the account, and its persistent-stack state is a local file in a
checkout — the co-worker's, not this machine's. Moving the stacks into an application directory
must adopt that state, never start beside it.

Two ways in, and both must work:

- **From the state bucket.** `init` already restores persistent state from the bucket when it finds
  none locally, and that is the normal path for a new machine. The first `init` with the new binary
  on a machine with an empty application directory therefore adopts the live install by itself.
- **From an existing checkout.** Where a `deploy/aws/*/terraform.tfstate` already exists locally,
  the binary copies it into the application directory on first use and says that it did.

The safety requirement: if the application directory is empty *and* no backup can be read from the
bucket — including when the read fails with access denied rather than not-found — the command must
stop rather than apply. Today's `init` already refuses in exactly that case, and that refusal must
survive the move. A `terraform apply` against empty state is how a duplicate install gets created.

The documented fourteen-resource import procedure stays the last resort, not the upgrade path.

### 7. Windows, later

Out of scope for this work, at the owner's direction. CI keeps building the four existing assets.
Nothing here may make Windows harder: no POSIX-only path assumptions, the configuration directory
resolved through a per-platform helper from the start, and the in-process tunnel is what will make
Windows possible when it comes, since the Session Manager plugin is the piece with no good Windows
story.

## What is left to the builder

- Ordering and how many commits or PRs. The sensible order is: SDK migration and `login` first (it
  removes a tool from every command and adds nothing risky), then the install parameter, then the
  embedded stacks and state adoption, then the tunnel behind its fallback.
- Whether the outputs cache is one file or one per stack, and its format.
- How the config maps onto generated tfvars, and where the generated file goes.
- Whether the Terraform version is compiled in or checked at runtime.
- The exact YAML key names, within the shape above.
- Which `awscloud.Fake` or `httptest` fixtures the SDK migration needs, and how the tunnel is
  tested without a real session (a local WebSocket server exercising the framing is the obvious
  approach).
- Whether `crucible.tfvars` keeps working as an input during a transition period.

## Calls I made, for the owner to check

- The configuration directory locations, and `--config` / `CRUCIBLE_CONFIG`.
- The YAML shape and its grouping into `install` / `aws` / `access` / `server` / `machine` /
  `schedule` / `email` / `labs`.
- `${ENV_VAR}` references for secrets, rather than plaintext only.
- Caching stack outputs so eleven operations stop needing Terraform. This was not asked for, but
  without it "Terraform only for deploy-time changes" is not achievable.
- Writing the embedded stacks to a durable directory rather than a temporary one, on account of the
  persistent stack's local state.
- Auto-discovering `account` and `role` after login, and allowing the start URL to be a build-time
  default.
- Keeping `--var-file` and the repository path for developers.
- Precedence **flag > config > deployed value > default**, with an absent key meaning "keep what is
  deployed" so that a re-run never silently removes a budget or changes the access mode.
- A raw `terraform_vars:` passthrough for the stack variables the typed schema does not name.
- Writing the standard `~/.aws/sso/cache` rather than a private token store, and letting `AWS_*`
  environment variables override the `aws:` block.
