# Crucible CLI reference

`crucible` is the only program: the server, the laptop agent, the content tools and the operator commands for AWS and
the local stack. `crucible` with no arguments prints the usage.

Run it from the repo root with the repo's toolchain on the `PATH`:

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH   # `make tools` installs them once
go run ./cmd/crucible <command>     # or `make build` and use bin/crucible, or `go install ./cmd/crucible`
```

The full step-by-step guides are [aws-deploy.html](aws-deploy.html) and [runbooks/aws.md](runbooks/aws.md). This page
lists the commands and what each one is for.

## Which command when

| I want to… | Command |
|---|---|
| Set up AWS the first time | `crucible aws init`, (`crucible aws labs-init`), `crucible aws up` |
| Ship a release to the node | `crucible aws deploy --version vX.Y.Z` |
| Change a setting in `crucible.yaml` | `crucible aws up` |
| Stop paying for compute tonight, keeping labs | `crucible aws sleep` |
| Start the node before the schedule | `crucible aws wake` |
| Check the node, the release and the labs | `crucible aws status` |
| Take a backup now | `crucible aws snapshot` |
| See where the config, the stacks and the state live | `crucible aws where` |
| Destroy the node (data kept in S3) | `crucible aws teardown --yes` |
| Reach an `--access ssm` install | `crucible aws connect` |
| Let someone sign in | `crucible aws invite EMAIL` |
| Put AWS labs to sleep without stopping the node | `crucible labs bedtime --wait 20m` (on the node) |
| End every lab | `crucible labs stop-all --wait 30m` (on the node) |
| Run Crucible on my laptop | `crucible local up` / `down` / `status` |
| Check a training repo | `crucible lint <dir>` / `crucible preview <dir>` |

## The configuration file

`crucible.yaml` holds everything an operator decides, so there are no flags to remember on a new machine. It is read
from `$CRUCIBLE_CONFIG`, else `crucible.yaml` in the platform's configuration directory (`~/.config/crucible/` on Linux,
`~/Library/Application Support/crucible/` on macOS); `--config FILE` overrides both. Give it mode `0600`: it holds tokens.

Any value may be written `${VAR}` to take it from the environment instead; a referenced variable that is unset is an
error naming it, never an empty token. An unknown key is an error too, so a typo cannot silently drop a setting.

**An absent key keeps what is deployed.** Deleting the `labs:` block does not destroy the AWS Budget, and deleting
`access:` does not flip the install back to public. Precedence is flag, then the file, then the deployed value, then the
Terraform default. Removing a guardrail is done on purpose, with a flag, not by deleting lines.

```yaml
install:
  name: crucible                 # stack name and the SSM prefix
  region: il-central-1

aws:                             # optional: leave it out to use ~/.aws/config
  sso_start_url: https://identitycenter.amazonaws.com/ssoins-…
  sso_region: il-central-1
  sso_session: Platform-AWS      # when ~/.aws/config uses an [sso-session]; keys the token cache as the CLI keys it
  account: "111122223333"        # optional: discovered at login when there is only one
  role: Workload-Admin-PS        # optional: likewise

access:                          # exactly one mode
  mode: ssm                      # no inbound path: everyone uses `crucible aws connect`
  subnet_id: subnet-…
  local_port: 18080              # the same for everyone, or sign-in breaks
# access:
#   mode: public
#   domain: crucible.example.com
#   acme_email: ops@example.com
#   route53_zone_id: Z0123456789

server:
  bootstrap_admin: you@example.com
  ghcr_pull_token: ${GHCR_TOKEN} # classic PAT, read:packages only
  git_credentials: https://crucible-bot:TOKEN@github.com
  git_bot_name: Crucible Bot
  git_bot_email: crucible-bot@example.com

machine:                         # every block below here is optional
  instance_type: t3a.xlarge
  disk_gb: 100

schedule:
  enabled: true
  timezone: Europe/Bucharest
  labs_bedtime: "0 19 * * 1-5"
  backup: "15 19 * * *"
  sleep: "cron(30 19 ? * MON-FRI *)"
  wake: "cron(30 7 ? * MON-FRI *)"

email:                           # omit entirely for no email
  smtp_addr: email-smtp.il-central-1.amazonaws.com:587
  smtp_from: Crucible <crucible@example.com>
  smtp_username: AKIA…
  smtp_password: ${SMTP_PASSWORD}

labs:                            # omit entirely to run without AWS labs
  regions: [il-central-1]
  vpc_id: vpc-…
  subnet_ids: [subnet-…, subnet-…]
  budget_usd: 200
  budget_emails: [you@example.com]
  billing_export: false          # our organization's SCP denies it
  account_public_access_block: false

terraform_vars:                  # anything the schema above does not name
  k3s_version: v1.34.1+k3s1
```

The Cognito client, the public URL, the `lab_*` wiring and the sysbox checksums are deliberately absent: `up` reads
those from the stacks. `terraform_vars` may not name a setting the schema owns, so no variable has two sources.

## AWS: `crucible aws …`

These run on your laptop with your AWS credentials and drive Terraform, GHCR and the node (over SSM, never SSH).
They call AWS through the SDK built into `crucible`, so the AWS CLI is not required at all: even `connect`,
which drives AWS's Session Manager plugin, carries that plugin inside the binary. `crucible aws login` signs you in.

**Terraform is needed by four commands only** — `init`, `labs-init`, `up` and `teardown`, the ones that create or destroy
AWS resources. Every other command reads the install's details from `/<name>/install` in SSM, which those four publish
after a successful apply, so a machine that only deploys and connects needs no Terraform and no checkout. `crucible`
never downloads Terraform; install it yourself. The first command you run against an install created by an older
release must be `crucible aws up`, which publishes that parameter.
Operator settings come from `crucible.yaml` (above); `--var-file`, which defaults to
`deploy/aws/main/crucible.tfvars`, stays for developers working in a checkout. Commands that might lose the node's disk (`up`, `sleep`,
`teardown`) take a snapshot first and stop if it fails; `--no-snapshot` skips it, and everything since the last backup is
lost if the disk goes.

### Setting up

| Command | Purpose |
|---|---|
| `crucible aws login [--sso-start-url URL] [--sso-region R]` | Signs you in through IAM Identity Center: it opens your browser, you approve, and the token is cached where the AWS CLI caches it — so `aws sso login` and this are interchangeable and you are never signed in twice. With no account or role configured and exactly one of each available, it uses and names them; with several it lists them and asks you to set `aws.account` and `aws.role`. |
| `crucible aws init --region R --domain HOST` | Creates the **persistent stack**: the Terraform state and data buckets, the Cognito user pool and the secret key that seals repository credentials. Nothing in it is ever destroyed. A re-run on another machine restores its state from the bucket. |
| `crucible aws init --region R --access ssm --subnet-id S [--local-port 18080]` | The same, for an install with no way in from outside (no domain, Elastic IP or Let's Encrypt): everyone opens it at `http://localhost:<port>` through `crucible aws connect`. A re-run without `--access`, `--domain` or `--local-port` keeps the current ones. |
| `crucible aws labs-init --region R [--allowed-regions R1,R2] [--crucible-account ID] [--budget-usd N] [--budget-email ADDR]… [--spend-source budget|cost-explorer] [--vpc-id V --subnet-ids S1,S2]` | Run with **lab-account** credentials. Creates the **labs stack**: the role and permission boundary AWS labs run under, the regions they may use, the lab network, and the AWS Budget `crucible-labs-monthly` (always made; alert emails only with `--budget-email`). `--spend-source` picks where the monthly account limit reads the bill: the budget (default, free, every 10 minutes) or `cost-explorer` ($0.01 a call, hourly: about $0.24 on a day the node is up all day, about $7 a month around the clock or about $2.60 a month on the default weekday schedule). A re-run without a flag keeps its current value. Run `crucible aws up` afterwards to turn AWS labs on. |
| `crucible aws labs-init … --billing-export=false --account-public-access-block=false` | For an account whose organization denies these by SCP (ours does). The first skips the billing export, so the Ledger has no Billed figures and spend comes from the meter and estimates. The second leaves the account-wide S3 public access block to the organization. A re-run keeps each setting; `=true` turns one back on. |
| `crucible aws up [--version vX.Y.Z \| --build] [--no-labs] [--no-snapshot]` | Applies the **main stack** (the node, its network, IAM, the sleep and wake schedule), then deploys a release. Idempotent: it's also how you apply any change to `crucible.tfvars`. It snapshots a running node first, in case the apply replaces it. `--no-labs` turns AWS labs off (running ones lose their credentials and cleanup). |

### Everyday

| Command | Purpose |
|---|---|
| `crucible aws deploy --version vX.Y.Z` | Records the release in SSM (`/<name>/release`) and runs the node's `deploy.sh`: pull chart `X.Y.Z` from GHCR, refresh the secrets from SSM and `helm upgrade --install`. If the rollout fails it points `/<name>/release` back at the old one. |
| `crucible aws deploy --build` | Builds this tree, pushes the server image and chart to GHCR as `0.0.0-dev.g<sha>`, and deploys it. Needs your own `docker login ghcr.io` and `helm registry login ghcr.io` with a `write:packages` token. |
| `crucible aws deploy` | Deploys this binary's own release. A binary built from source names no release, so it refuses. |
| `crucible aws where` | Prints the configuration file, the application directory (the one thing worth backing up: it holds the persistent stack's state), each stack's directory and the Terraform in use. This is what the fourteen-resource recovery needs in order to run `terraform import` by hand. |
| `crucible aws status` | The node's state and address, `release:`, SSM reachability, `/healthz`, the hosts the node can't reach, the bedtime, and the AWS labs by state. |
| `crucible aws snapshot` | Runs the backup now (a `pg_dump` to the data bucket) and waits for it. |
| `crucible aws connect [--region R] [--port P] [--name crucible]` | Opens a Session Manager port-forward to an `--access ssm` install and keeps it open until Ctrl-C. Needs nothing installed: it starts the session through the SDK and runs the Session Manager plugin carried inside the binary, which it unpacks into `~/.config/crucible/bin` the first time. All it needs of you is your sign-in (`crucible aws login`). Use Crucible's own port, or sign-in breaks. |
| `crucible aws invite EMAIL [--name "Full Name"]` | Adds someone to the Cognito pool with a verified email and sends the invite. Run it again to resend an unused invite. |

### Night, morning and teardown

| Command | Purpose |
|---|---|
| `crucible aws sleep [--keep-labs] [--no-snapshot]` | Puts every AWS lab to sleep (`crucible labs bedtime --wait 20m` on the node), snapshots, then **stops the node**: compute billing stops, and the disk and IP keep costing about $9 a month. If something fails after the bedtime, it cancels the bedtime again. `--keep-labs` skips the labs: they keep running and billing, and the stopped server can't look after them until `wake`. |
| `crucible aws wake` | Starts the node and waits until Crucible answers. The server ends any bedtime when it starts. Sleeping labs stay asleep until their trainee presses **Wake it**. |
| `crucible aws teardown --yes [--keep-labs] [--no-snapshot]` | **Ends every lab** (`crucible labs stop-all --wait 30m`), snapshots, then destroys the main stack. The persistent stack (buckets, Cognito, secret key) and the labs stack survive. With AWS labs on it refuses a node that isn't running (run `wake` first) or one whose release is too old to end labs (before v0.2.0); with AWS labs off it goes on with a note, since nothing in AWS can be left behind. It lists anything still tagged `crucible:lab-id` without deleting it. `crucible aws up` rebuilds the node from the latest snapshot. |

The schedule does the same every weekday without you: `labs_sleep_cron` (19:00) runs the bedtime, `backup_cron` (19:15)
the backup, `sleep_cron` (19:30) stops the node, and `wake_cron` (07:30) starts it. All of these apply only with
`schedule_enabled`.

## Labs on the server: `crucible labs …`

These run **where the server's database is** (they need `DATABASE_URL`). Only the server acts on what they write. They
run in the server pod, from the nightly CronJob, or from `crucible aws sleep` and `teardown`. To run one yourself on the
node (`aws ssm start-session --target <instance id>`):

```bash
sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl -n crucible exec deploy/crucible -c api -- crucible labs <args>
```

On the local stack: `docker exec crucible-local-api-1 crucible labs <args>`.

| Command | Purpose |
|---|---|
| `crucible labs bedtime` | Asks the server to put every AWS lab to sleep, then exits. New starts, approvals and wakes are refused while it lasts. Labs starting or being destroyed get 10 minutes to finish, and a lab still starting after that is ended (`bedtime`). Then each ready lab sleeps: its machines are hibernated where possible and otherwise stopped, and its workspace pod is removed while the workspace volume stays. |
| `crucible labs bedtime --wait 20m` | The same, then waits until every AWS lab is asleep. It fails and names the labs that aren't. |
| `crucible labs bedtime --cancel` | Ends the bedtime, so labs may start and wake again. The server also ends it when it starts, and a bedtime ends by itself 3 hours after its node failed to go to sleep. |
| `crucible labs stop-all [--wait 30m]` | Turns the **kill switch** on as `crucible aws teardown`: the server ends every lab (AWS, cluster and laptop) and refuses new ones. `--wait` waits until none is left. The server turns this switch off when it next starts; a switch an admin set stays on. |
| `crucible labs status` | Prints the bedtime (if any) and the AWS labs by state. |

`bedtime` keeps labs so trainees can carry on, and `stop-all` ends them for good.

## Local stack: `crucible local …`

Postgres, Keycloak and Crucible with Docker Compose, at http://localhost:8080. The users are `trainee`, `senior`,
`leader` and `admin`, and each password is the username.

| Command | Purpose |
|---|---|
| `crucible local up [--keycloak-port 8082] [--no-build]` | Builds the binaries and toolbox, seeds the example training repos and starts the stack. `--no-build` uses what is already in `bin/`. `--keycloak-port` defaults to `$KEYCLOAK_PORT`, else 8081; use 8082 on this machine. |
| `crucible local down [--wipe]` | Stops the stack and keeps its data. `--wipe` also deletes the database and Keycloak volumes. |
| `crucible local status` | Shows what is running. |

## Training content

| Command | Purpose |
|---|---|
| `crucible lint <dir>` | Checks a training repo or a platform directory offline: structure, quizzes, labs, and the hardened terraform rules for AWS labs (for example, `hibernation = true` needs an encrypted root disk, and every resource must be priceable). |
| `crucible preview <dir> [--port 8090] [--image crucible:dev] [--free]` | Runs the real Crucible image against a git snapshot of your working tree and re-syncs within seconds of every save. Only tracked or staged files are included. `--free` opens every module. |

## Trainees' laptops

| Command | Purpose |
|---|---|
| `CRUCIBLE_TOKEN=TOKEN crucible agent --server URL` | Pairs the laptop with Crucible and runs its laptop labs (Docker) and cluster labs (through your kubeconfig). Get the token in the app. Pass it in the environment, not as `--token`, because other local users can see command-line flags. |
| `crucible agent cluster up [--name crucible]` | Makes a kind cluster for cluster labs. Its kubeconfig goes only to `~/.crucible/kubeconfig`, never `~/.kube/config`. |
| `crucible agent cluster down [--name crucible]` | Deletes that cluster and its kubeconfig entries. |

## Inside images

| Command | Purpose |
|---|---|
| `crucible serve` | The server: API, SPA and background jobs. It is configured by environment variables only (`DATABASE_URL`, `CRUCIBLE_BOOTSTRAP_ADMIN`, …). |
| `crucible ready [--json] <release>… \| all` | Judges a cluster lab's Helm releases. It runs in the lab's toolbox and needs `LAB_NAMESPACE`. |
| `crucible version` | Prints the build version. |

## Make targets (development)

| Target | Purpose |
|---|---|
| `make tools` | Installs the pinned Go, Terraform and kind into `.local/tools`, checksum-verified. |
| `make build` | Builds `bin/crucible`. |
| `make test` | The Go tests. |
| `make toolbox` / `make workspace` | Builds the cluster-lab toolbox image / the AWS-lab workspace image. |
| `KEYCLOAK_PORT=8082 make local-check` | Every browser journey against a fresh local stack. |
| `KEYCLOAK_PORT=8082 make cluster-check` | The same plus cluster and dry-run AWS labs on a kind cluster (`crucible-m4`). It never touches AWS, and it wipes the local stack's data first. Prefix either check with `KEEP=1` to leave the stack running. |
| `make release-binaries VERSION=vX.Y.Z` | `crucible` for macOS and Linux, amd64 and arm64, with `SHA256SUMS`, into `dist/`. |
