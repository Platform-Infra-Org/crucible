# Crucible on AWS: runbook

## One-time setup (per AWS account)
1. Install: terraform >= 1.10, AWS CLI v2 (logged in) with its [Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) (`aws ssm start-session`, a shell on the node, needs it; check with `session-manager-plugin --version`), Docker with buildx, Helm 3, Go 1.26. A fresh clone has no `.local/tools`: `make tools` installs the repo's pinned Go and Terraform there (checksum-verified), then `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`. Run every command from the repo root. Either `go install ./cmd/crucible` (then use `crucible aws ...`) or prefix commands with `go run ./cmd/crucible`.
   **The region needs its default VPC**: the node goes into the default VPC's first default subnet, and `up` fails without one. Check with `aws ec2 describe-vpcs --region <region> --filters Name=is-default,Values=true --query 'Vpcs[].VpcId'` (an empty list means none); create one with `aws ec2 create-default-vpc --region <region>`.
2. `go run ./cmd/crucible aws init --region eu-west-1 --domain crucible.example.com`. This creates the state and data buckets, the Cognito user pool and the secret key that seals repository credentials (`/<name>/secret_key`; see Secrets). None of them is ever destroyed.
3. **The persistent stack's state is backed up for you.** `deploy/aws/persistent/terraform.tfstate` is local and git-ignored; after every successful apply, `init` copies it to `s3://<state_bucket>/persistent.tfstate` (private, versioned) and prints `state backed up to …`. If the copy fails, `init` exits with an error that gives the `aws s3 cp` to run by hand. On a machine without the file, `init` restores it from there first (it finds the bucket from your account: `crucible-<account>-tfstate`); with no backup it's a new stack, and if it can't tell (access denied) it stops before applying. The file holds the secret key and the Cognito client secret in plain text; a second copy outside the account (a password manager) protects against losing the account.
4. `cp deploy/aws/main/terraform.tfvars.example deploy/aws/main/crucible.tfvars` and fill it in. `domain` there must equal the `--domain` you gave `init`.
   **Set `ghcr_pull_token`** (required): a GitHub token with `read:packages` only, which the node uses to pull the private chart and server image from GHCR. It is stored as the SecureString `/<name>/ghcr_pull_token`. A wrong token stops `deploy.sh` before anything is applied, with a message that says what to fix (`GHCR refused the pull token …`).
   Sign-in needs no settings here; it is wired from the Cognito user pool automatically.
5. `go run ./cmd/crucible aws up` (`--var-file` defaults to `deploy/aws/main/crucible.tfvars` in the repo root).
   Right after the apply it prints `Point an A record <domain> → <ip>`: create that DNS record **while `up` waits** for the node (skip if `route53_zone_id` is set).
   The first HTTPS request may take ~1 min while Let's Encrypt issues the certificate. If `up` ends with "isn't answering yet", DNS or the certificate is still settling: wait, then run `crucible aws status` until `healthz: ok`.
6. Invite people and add them to teams: follow [cognito.md](cognito.md).

### Our account (access = ssm)

One account, no default VPC, no inbound path. These steps replace 2 to 5 above; step 6 is the same. Nothing here creates
an Elastic IP, a DNS record or a certificate.
1. `crucible aws init --region il-central-1 --access ssm --subnet-id subnet-0fa66f46aa023ab6e` (spokeSubnet1; add `--local-port N`
   to change `18080`; no `--domain`). init checks the subnet exists (read-only `describe-subnets`) before applying. This is the
   first contact with S3, Cognito and SSM Parameter Store in this account, none of which has been checked against the SCPs. If an
   action is denied by an SCP, stop and ask the platform team. A re-run without `--access`, `--subnet-id` or `--local-port` keeps the current ones.
2. Write `deploy/aws/main/crucible.tfvars` from the example's ssm part: `bootstrap_admin` and `git_credentials`, no `domain`,
   `acme_email` or `subnet_id` (`up` passes init's subnet, which wins over one set here). Set `schedule_enabled = false` if
   EventBridge Scheduler is denied.
3. `crucible aws up`. It prints no 'point an A record' line, waits for the node to register with SSM, and checks `/healthz` on the node. If it ends
   with "node not reachable over SSM", ask the network team to allow `ssm.il-central-1.amazonaws.com`,
   `ssmmessages.il-central-1.amazonaws.com` and `ec2messages.il-central-1.amazonaws.com`.
4. `crucible aws status`. Expect `ssm: Online`, `healthz: ok` and `egress: every host Crucible checks is reachable`. If `ssm:` is not `Online`, the checks stop
   and it names the three SSM hosts. If egress lists blocked hosts, send them to the network team. The list covers k3s, GitHub, Docker,
   apt (plain HTTP on port 80), the AWS CLI, Price List, S3, STS, SSM, Cognito (including its login domain), and the AWS-lab path
   (`registry.terraform.io`, `releases.hashicorp.com`, `iam`, `ec2`, `tagging`, `cloudtrail`), plus the OS-level lab hosts:
   `signin.aws.amazon.com` (console link), `ec2-instance-connect.<region>.amazonaws.com` (key pushes) and, over **TCP 22**, the
   region's EC2 Serial Console endpoint (`ec2-serial-console.il-central-1.api.aws` here; the name differs by region generation, so
   status looks it up in a table and checks only regions in it). `ghcr.io` and `pkg-containers.githubusercontent.com` (the workspace image's registry and blob host; the lab pod's Docker pulls it
   through the node). A blocked `host:22` line means `lab-console` can't connect from lab pods. The git host is not tested: check it by hand.
5. `crucible aws connect` (AWS CLI, Session Manager plugin and your SSO login). Leave it running, open `http://localhost:18080` and
   sign in. Ctrl-C ends the tunnel. A `--port` other than the one in the node's
   `crucible:public-url` tag is refused.
6. `crucible aws labs-init --region il-central-1 --vpc-id vpc-03aa39198d92c5a25 --subnet-ids subnet-0fa66f46aa023ab6e,subnet-08e721302b446603f`
   (both flags or neither; a re-run without them keeps the current ones), then `crucible aws up` again. Run one AWS lab that launches an
   instance (step 10 under "AWS labs").

The organization's after-hours automation may stop the node whatever `schedule_enabled` says; check with the platform team.

**First admin (`bootstrap_admin`).** Set `bootstrap_admin = "you@example.com"` in your tfvars. On the first start Crucible adds that email to its empty `admins` table. It grants nothing by itself: you still sign in through Cognito with that verified email, so invite it first (see `cognito.md`). It runs once per deployment: the `admin.bootstrap` audit row (actor `bootstrap`, target the email) marks it done, and every later start ignores the value, so removing every admin is never undone by a restart. To seed again after a database reset, the audit row must be gone too.

**Configuration lives in Postgres.** Sign in as the first admin and set cost tiers, schedules, quotes and admins under **Administrator → Forge settings**; create teams on the team pages; register trainings, start them for teams, enroll people and give out roles under **Manage trainings**. The fail-closed rules hold: no cost tiers means labs that cost money can't be requested. Cluster labs are free and run on trainees' own clusters through their agent (set `CRUCIBLE_TOOLBOX_IMAGE`, Helm `toolboxImage`); Crucible's own cluster (`CRUCIBLE_LAB_CLUSTER=1`) runs AWS lab workspaces. Nothing about people, roles or budgets is ever written to git.

**Git credentials (M7).** The bot credential (`git_credentials`) needs read access to every training repo you register and **push** access to them for content edits: edits made in the UI are pushed to `crucible/edit/<id>` branches, and approved ones are merged into the tracked branch by the bot. Branch protection on the tracked branch must let the bot push merge commits. Set `git_bot_email` (and optionally `git_bot_name`) in your tfvars to an author email your git host accepts for that bot; the default `crucible-bot@example.com` is rejected by hosts that require verified commit emails.

Second machine or fresh clone: run `crucible aws init` with the same flags (it restores the persistent state from the state bucket; its plan should change nothing), then `crucible aws up` (it is idempotent) to initialise the S3 backend before using `deploy`. With aws labs on, run `labs-init` with lab-account credentials first too: it restores the labs state the same way.

## Releases and GHCR

The server no longer ships through S3: CI publishes images and the chart to GHCR and the node pulls them. Nothing about a release lives in the data bucket.

- **Images:** `ghcr.io/platform-infra-org/crucible` (server, private), `crucible-workspace` and `crucible-toolbox` (public). **Chart:** `oci://ghcr.io/platform-infra-org/charts/crucible` (private); its version is the release version without the `v`.
- **One-time:** after the first release, make the `crucible-workspace` and `crucible-toolbox` packages public (Package settings → Change visibility). Set `ghcr_pull_token` (step 4) before `up`.
- **Firewall egress:** `ghcr.io` and `pkg-containers.githubusercontent.com`. `crucible aws status` checks both.
- **Workflows** (`.github/workflows`): `checks` runs on every PR and push to main. `release` builds everything without pushing when a PR is opened or updated; when it merges, git-cliff picks the version and rewrites `CHANGELOG.md`, the commit is tagged, the three images go to GHCR with provenance and SBOMs, the chart is pinned to their digests and pushed, the GitHub Release carries the binaries and `SHA256SUMS`, and `latest` moves last on the workspace and toolbox images. Docs-only and ci-only merges refresh the changelog without a release. `release-manual` (Actions tab, any branch) publishes a prerelease named `<next>-<branch>.<run>`, with no `latest` tags. `e2e` is manual.
- **Choosing a release:** `--version` takes releases and prereleases. `crucible aws status` shows `release:`.
- **Caveats:** a PR merged from a fork cannot release (no write token). A release that was tagged but never published is resumed by re-running the release job, but only for that PR's own tag and only when no GitHub Release exists for it. If a GitHub Release exists but is incomplete (an asset upload or the `latest` move failed), a re-run does nothing: fix it by hand with `gh release upload` or by re-tagging `latest`. Restrict who may push `v*` tags with a tag ruleset. Multi-arch builds run under QEMU and are slow (tens of minutes).
- **Migration:** a node created before this change keeps its old `deploy.sh`, and `deploy` refuses it. Run `crucible aws teardown --yes`, then `crucible aws up` (the snapshot restores the data). Old `releases/` objects in the data bucket can be deleted by hand.

## Everyday
| Want | Command |
|---|---|
| Run a release | `crucible aws deploy` (this binary's own release), `--version vX.Y.Z` (a release or prerelease from GHCR; `up` takes the same flags), or `--build` (builds this tree, pushes the server image and chart to GHCR as `0.0.0-dev.g<sha>`, plus `-dirty.<unix time>` when the tree has changes; needs your own `docker login ghcr.io` and `helm registry login ghcr.io`). A `dev` binary with neither flag refuses. |
| Take a snapshot now | `crucible aws snapshot` |
| Stop paying for compute tonight | `crucible aws sleep` (snapshot, then stop) |
| Start before the schedule | `crucible aws wake` |
| Is it up? / which release? | `crucible aws status` (shows `release:`) |
| Shell on the node | `aws ssm start-session --target <instance_id>` |
| Invite / remove a person | [cognito.md](cognito.md) |

The schedule wakes the node at 07:30 and sleeps it at 19:30 (Mon-Fri, `schedule_timezone`). The backup runs at 19:15.

`t3a` instances run in *unlimited* CPU-credit mode by default: sustained CPU above the baseline (40% for `t3a.xlarge`) is billed as surplus credits (about $0.05 per vCPU-hour). Busy labs can add to the bill; check the instance's `CPUSurplusCreditsCharged` metric.

**Changing settings.** Edit `crucible.tfvars` and run `crucible aws up`. Node settings (`domain`, `backup_cron`, `schedule_timezone`, …) are stored in the SSM parameter `/<name>/env`, and the node re-reads it on every deploy, which `up` runs. The sleep/wake schedule (`wake_cron`, `sleep_cron`, `schedule_enabled`) is EventBridge, so the terraform apply inside `up` changes it directly. A new `domain` also needs `crucible aws init --domain <new>` first (the Cognito callback) and a new DNS record. `up` snapshots a running node before applying, in case the apply replaces it.

**Skipping the safety snapshot.** `sleep`, `teardown` and `up` take a snapshot first and refuse to continue if it fails. If the node is broken and cannot snapshot, add `--no-snapshot`. It prints a loud warning: **everything since the last backup is lost** if the disk goes away.

## Disaster / full teardown
- `crucible aws teardown --yes`: takes a snapshot, then destroys the main stack. The buckets and Cognito pool survive.
- `crucible aws up`: rebuilds the node. The restore init container loads the newest snapshot into the empty database. Config and content come back from git.
- **Re-point DNS**: the Elastic IP changes after a teardown, so update the A record to the IP `up` prints (automatic with `route53_zone_id`).
- Dated snapshots under `snapshots/` expire after 30 days, but **the last snapshot is always kept**: every backup also writes `latest/crucible-latest.dump`, which never expires. After a long teardown, the restore falls back to it.
- The git webhook secret is regenerated on teardown → up: update it in your git host (see Secrets).
- The secret key is not: it lives in the persistent stack, so the restored repositories' tokens and deploy keys still open.

## Uploads and transcripts
Files trainees attach for scorers and recorded terminal sessions live under `s3://<data bucket>/uploads/`. That prefix has no lifecycle rule and is not touched by `crucible aws teardown`, so a rebuilt platform (snapshot restore) still serves them. The node role may write only there, to `snapshots/` and to `latest/crucible-latest.dump`.

## Lost persistent state
If `deploy/aws/persistent/terraform.tfstate` is lost, run `crucible aws init` again with the same flags: it restores the backup (step 3). Without one, run `terraform -chdir=deploy/aws/persistent init`, then `terraform import` each of the 14 resources (addresses in `deploy/aws/persistent/*.tf`; use the real IDs from the console): `aws_s3_bucket.state`, `aws_s3_bucket_versioning.state`, `aws_s3_bucket_public_access_block.state`, `aws_s3_bucket.data`, `aws_s3_bucket_versioning.data`, `aws_s3_bucket_server_side_encryption_configuration.data`, `aws_s3_bucket_public_access_block.data`, `aws_s3_bucket_lifecycle_configuration.data`, `aws_cognito_user_pool.users`, `aws_cognito_user_pool_domain.login`, `aws_cognito_user_pool_client.crucible`, `aws_cognito_managed_login_branding.crucible`, `aws_ssm_parameter.secret_key` (ID `/<name>/secret_key`) and `random_password.secret_key` (ID: the parameter's current value, read with `aws ssm get-parameter --with-decryption`). Import the secret key **before** any plan: without it Terraform would make a new key, and every stored repository credential would stop opening. Then run `terraform plan` and import anything it still wants to create. Give every `import` and the `plan` the variables `init` used: `-var region=<region> -var access=ssm -var local_port=<port>` for an ssm install (18080 unless you chose another), `-var region=<region> -var access=public -var domain=<host>` for a public one. A wrong `access` or port shows up in the plan as a change to the Cognito client's callback URLs. Never `apply` against an empty state: it would try to create duplicates.

## Secrets
The API needs `CRUCIBLE_QUIZ_SECRET`. It is generated by Terraform into SSM Parameter Store automatically. Rotating it reshuffles every open quiz; learners mid-quiz get a new question order. To rotate: `terraform -chdir=deploy/aws/main apply -replace=random_password.quiz -var-file=<your tfvars> <the -var flags that `crucible aws up` passes>` (simplest: `terraform -chdir=deploy/aws/main taint random_password.quiz`, then `crucible aws up`), then on the node `sudo k3s kubectl -n crucible rollout restart deploy/crucible` so the API picks up the new value.

**Secret key (`CRUCIBLE_SECRET_KEY`).** It seals the credentials admins give Crucible under **Administrator → Repositories** (access tokens and the deploy keys Crucible makes), so the database and its snapshots hold only ciphertext. Without it those pages refuse tokens and deploy keys; public repositories still work. `crucible aws init` creates it as the SecureString `/<name>/secret_key` in the persistent stack, so `teardown` → `up` keeps it, and `deploy` copies it into the `crucible-secrets` Secret. **An install made before this existed** has no such parameter: deploy prints `WARNING: no /<name>/secret_key …` and the API runs without the key. Fix it by re-running `crucible aws init` with the same `--region` and `--domain` (it re-applies the persistent stack where its state lives and adds only the key), then `crucible aws deploy`. A deploy of the release already running does not restart the API, so then also run `sudo k3s kubectl -n crucible rollout restart deploy/crucible` on the node. Back up the persistent state again afterwards (step 3). The node reads `/<name>/secret_key` with the `name` of your tfvars, and `init` always uses `crucible`: if your tfvars sets another `name`, copy the parameter to `/<that name>/secret_key`. **Never rotate or lose it**: every credential sealed with the old key becomes unreadable, and each repository that uses one must be reconnected (**Replace credential**, or a new deploy key added at the host). Terraform never changes the parameter once made; replacing it is a deliberate `aws ssm put-parameter --overwrite`, followed by `crucible aws deploy`, the same restart, and reconnecting those repositories.

**Git webhook secret.** Point your git host's push webhook at `POST https://<domain>/api/git/hook` with the hook secret, the value of
`aws ssm get-parameter --with-decryption --name /<name>/git_hook_secret --query Parameter.Value --output text` (`<name>` defaults to `crucible`). Crucible accepts it three ways:
- **GitHub:** content type `application/json`, the secret in the webhook's **Secret** field. GitHub signs each delivery (`X-Hub-Signature-256: sha256=<HMAC-SHA256 of the body>`) and Crucible checks the signature. Bodies over 1 MiB are refused.
- **GitLab:** the secret in the webhook's **Secret token** field (sent as `X-Gitlab-Token`).
- **Any other host:** a custom header `X-Crucible-Secret: <secret>`.

A right secret gets `202` and a sync of every training; anything else gets `404`. It is regenerated on every teardown → up, so update the webhook afterwards.

**Email (optional).** Set the SMTP settings in `crucible.tfvars` and run `crucible aws up`:

```hcl
smtp_addr     = "email-smtp.eu-west-1.amazonaws.com:587"   # host:port
smtp_from     = "Crucible <crucible@example.com>"
smtp_username = "AKIA…"
smtp_password = "…"                                         # stored as the SecureString /<name>/smtp_password
```

The address, sender and username go into `/<name>/env`; the password into its own SecureString. Every deploy copies the ones that are set into the `crucible-secrets` Secret as `CRUCIBLE_SMTP_*`, so they survive teardown → up. A deploy of the release already running doesn't restart the API: after changing them, run `sudo k3s kubectl -n crucible rollout restart deploy/crucible` on the node. Without `smtp_addr` Crucible sends no email; Slack/Teams webhooks still work.

A setting left empty is never written, so `CRUCIBLE_SMTP_*` keys you added by hand (`kubectl -n crucible edit secret crucible-secrets`) stay until you set the variable. **Nodes built before these variables existed** run the old deploy script (Terraform never replaces a node's first-boot script), which ignores them: keep the hand-added keys, or rebuild the node (`teardown`, then `up`) to pick the variables up.

## Crucible's lab cluster (sysbox, AWS lab workspaces)

Cloud-init installs Sysbox CE (pinned version, sha256-checked; `sysbox_version` and `sysbox_sha256_amd64` in `deploy/aws/main/variables.tf`, update both together, amd64 only) and registers the `sysbox-runc` runtime with k3s's containerd through `config-v3.toml.tmpl` (containerd 2.x, k3s >= 1.32). It also creates the `sysbox-runc` RuntimeClass, and the chart defaults to `clusterLabs.enabled: true`. This cluster runs AWS lab workspaces only (`CRUCIBLE_LAB_CLUSTER`); cluster labs run on trainees' own clusters through their agent and need none of this (they need `CRUCIBLE_TOOLBOX_IMAGE`). Lab pods run in `lab-<id>` namespaces with Pod Security `baseline`, a quota, and a NetworkPolicy that blocks `169.254.169.254` (IMDS) and every private range.

**The cluster-lab toolbox image.** Trainees' laptops pull it, on arm64 (Apple silicon) and amd64 alike. Every release builds it for both, pushes `ghcr.io/platform-infra-org/crucible-toolbox` (public) and pins the release's chart to its digest, so an AWS install running a release has cluster labs with nothing to set. To run your own toolbox instead, build it for both architectures, push it to a registry their laptops can pull from (public, or one they are signed in to), and set `CRUCIBLE_TOOLBOX_IMAGE` (Helm `toolboxImage`) to the pushed digest, never a tag:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -f deploy/toolbox/Dockerfile -t <registry>/crucible-toolbox:<version> --push .
docker buildx imagetools inspect <registry>/crucible-toolbox:<version>   # the manifest list's sha256 digest
# CRUCIBLE_TOOLBOX_IMAGE=<registry>/crucible-toolbox@sha256:<digest>
```

On AWS, set the override in `crucible.tfvars` and run `crucible aws up`; the node's Helm values carry it as `toolboxImage`:

```hcl
toolbox_image = "<registry>/crucible-toolbox@sha256:<digest>"
```

Terraform refuses a tag (`:latest`, `:1.2`): the value must be `<image>@sha256:<64 hex>`. Left empty (the default), the node keeps the toolbox the release's chart pins. A chart without a toolbox (a `--build` deploy, or the in-tree chart) leaves cluster labs off; laptop and AWS labs work either way.

Run one Crucible deployment per Kubernetes cluster: the orphan sweep deletes `lab-*` namespaces that are not active in its own database. Turning the lab cluster off leaves existing lab namespaces in place (their rows are marked destroyed with "cleanup skipped"); delete them by hand with `kubectl delete ns -l crucible.io/lab`.

A node built before M4 has no sysbox, because `user_data` changes never replace the node. Rebuild it with `crucible aws teardown` and then `crucible aws up` (data is restored from the latest snapshot).

Verify after `up` (over SSM, as root). Nothing here is covered by `terraform test`; it only checks the rendered script.
1. `systemctl is-active sysbox` prints `active`, and `k3s kubectl get runtimeclass sysbox-runc` lists it.
2. Start an AWS lab in the browser (its steps are below; a cluster lab no longer runs here). Then `k3s kubectl get pods -A -l crucible.io/lab` shows the `lab` workspace pod `Running`, and `k3s kubectl -n lab-<id> get pod lab -o jsonpath='{.spec.runtimeClassName}'` prints `sysbox-runc`.
3. `hostUsers: false` with `runtimeClassName: sysbox-runc` is accepted and runs on k3s: `k3s kubectl -n lab-<id> get pod lab -o jsonpath='{.spec.hostUsers}'` prints `false` and the pod is `Running`. If the pod is rejected or stuck, sysbox and the user-namespace field conflict on this kernel/containerd: drop `hostUsers: false` from the pod spec (sysbox already gives each pod its own user-namespace mapping) and note the change here.
4. Network isolation. Get a shell with `k3s kubectl -n lab-<id> exec -it lab -c dind -- sh`, then run the checks first in the lab pod itself and again from a compose container started inside it (`docker run --rm -it alpine sh`, or `docker compose exec <service> sh`). Each of these must FAIL (timeout or refused):
   - `wget -T 3 -qO- http://169.254.169.254/latest/meta-data/` (IMDS)
   - `wget -T 3 -qO- --no-check-certificate https://10.43.0.1:443/` (Kubernetes API via the service IP)
   - `nc -zw3 <node-private-ip> 10250` (kubelet) and `nc -zw3 <node-private-ip> 6443` (API server)

   Find the node IP with `hostname -I`. This must succeed from both places: `wget -T 5 -qO- https://registry-1.docker.io/v2/` (answers 401, which proves egress) and a real pull such as `docker pull alpine` inside the lab. If the compose-container checks pass through (traffic from nested containers is NATed to the pod IP, so the NetworkPolicy should still apply), treat it as a release blocker.
5. End the lab. The namespace disappears within a minute (`k3s kubectl get ns -l crucible.io/lab`).

**Lab stuck in "provisioning" or failing with "could not be started":** run `k3s kubectl -n lab-<id> describe pod lab`. `no runtime for "sysbox-runc"` means containerd did not load the template: check `/var/lib/rancher/k3s/agent/etc/containerd/config.toml` for the `sysbox-runc` block, then run `systemctl restart k3s`. If the pod is `Pending`/`ContainerCreating` with a runtime error, also read `journalctl -u sysbox -u sysbox-mgr --no-pager | tail -50`.

## Troubleshooting
- **Sysbox.** `sudo journalctl -u sysbox --no-pager | tail -50` shows why labs fail to start (also `-u sysbox-mgr`).
- **Secrets in pod env.** The `crucible` service account has cluster-wide `pods: get`, so it can read any pod's literal env values by name. Never put secrets in plain `env` on this cluster; use `secretKeyRef`.
- **No certificate.** Check that DNS points at the Elastic IP and port 80 is reachable. Then run `sudo k3s kubectl -n kube-system logs deploy/traefik`. If the Traefik chart bundled with your k3s version rejects `ports.web.redirections`, use `ports.web.redirectTo: {port: websecure}` in `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml`.
- **Bootstrap failed.** `deploy` reports "node bootstrap failed" when `/opt/crucible/.failed` exists. Read `sudo tail -f /var/log/crucible-bootstrap.log` over SSM; `.ready` appears only after the first deploy attempt. After fixing the cause, clear the marker with `sudo rm /opt/crucible/.failed` and run `crucible aws deploy` again.
- **App logs.** `sudo k3s kubectl -n crucible logs deploy/crucible -c api` (and `-c restore`).

## AWS labs (runtime: terraform, cloud: aws)

AWS labs run terraform in your **lab account** with one-hour credentials scoped to a single lab (spec §8.2). A
dedicated sandbox account is strongly recommended: the permission boundary limits labs to EC2 and S3, small
instance types and the allowed regions, but shared-account isolation is best-effort.

Crucible prices AWS labs itself and pays nothing for it (spec 2026-10-12, free FinOps): no infracost, no Cost
Explorer. Estimates come from a read-only `terraform plan` priced from the public AWS Price List. Spend is metered
from each lab's Terraform state, then replaced by the free CUR 2.0 billing export. A free AWS Budget is the backstop.

**Setup (once).**
1. With credentials for the lab account: `go run ./cmd/crucible aws labs-init --region eu-west-1` (add
   `--crucible-account <id>` when the lab account is not the Crucible account). labs-init
   copies `deploy/aws/labs/terraform.tfstate` to `s3://<name>-<account>-labstate/labs-stack.tfstate` after each apply (outside
   `labs/`, which lab sessions and the ops role read), and restores it from there on a machine without it. labs-init also turns on account-wide
   S3 Block Public Access in that account. The lab roles trust Crucible's node role by exact name: labs-init reads it
   from main's `node_role_name` output when it can, and otherwise trusts `crucible-node` and says so. If your
   `crucible.tfvars` sets another `name`, run labs-init again after the first `up`. **Lab regions:** labs use `--region`
   alone unless you pass `--allowed-regions eu-west-1,eu-central-1` (comma-separated, the stack's `allowed_regions`).
   The list must include `--region` (the state bucket's region); labs-init refuses it otherwise, before running
   terraform. A re-run without the flag keeps the current list. `crucible aws up` then passes the list to the node
   (`CRUCIBLE_AWS_LAB_REGIONS`), so run `up` after changing it.
   labs-init also creates, all free:
   - the read-only **plan role** that estimates use (`ec2:Describe*`, `s3:GetBucket*`, `s3:ListAllMyBuckets`, inside
     the lab permission boundary);
   - the **export bucket** (`<name>-<account>-labexports`) and a **CUR 2.0 export** into it (`<name>-labs`, prefix `cur`,
     daily, gzip CSV, overwritten in place, with `resource_tags` so each line carries its `crucible:lab-id`). Data
     Exports live in us-east-1 whatever `--region` is; the bucket is in `--region`;
   - the **lab instance role and profile** `crucible-lab-instance` (labs may attach the profile to their machines): the SSM agent's
     channel and Run Command only, written out instead of AWS's managed SSM core policy because that grants `ssm:GetParameter(s)`,
     and root on a lab machine could then read `/<name>/*` (secret key, database password, OIDC secret). It carries an explicit
     deny of `ssm:GetParameter*` on `/<name>` and `/<name>/*` as well;
   - an alert-only **AWS Budget** (`<name>-labs-monthly`) that emails at 80 % and 100 % of the month's actual spend,
     independent of Crucible. Set it with `--budget-usd 200 --budget-email ops@example.com` (repeat `--budget-email`,
     at most 10). On the first run, without `--budget-usd` the amount is $100 and without any `--budget-email` no
     budget is created. A negative `--budget-usd` is refused. **Re-running labs-init keeps the budget**: a flag you
     leave out keeps the stack's current value (read from its outputs), and a flag you pass replaces only its own
     value (`--budget-email` replaces the whole list). A stack created before these outputs existed has nothing to
     read back: pass both flags on the first re-run after upgrading. **To remove the budget**, labs-init can't (an
     empty list keeps the current one); run the labs stack's apply yourself with the same values labs-init passes
     and an empty list, with lab-account credentials, from the repo root (where `up` ran, so main's outputs are
     readable; leave out `crucible_account_id` if you never passed `--crucible-account`):
     ```bash
     terraform -chdir=deploy/aws/labs apply -var region=<region> -var crucible_account_id=<id> \
       -var node_role_name="$(terraform -chdir=deploy/aws/main output -raw node_role_name)" -var 'budget_emails=[]'
     ```
     The plan should destroy only `aws_budgets_budget.labs[0]`; if it shows anything else, a value differs from
     what labs-init passed: stop and compare.
   labs-init also reads the account's EC2 Serial Console setting (`aws ec2 get-serial-console-access-status`). It is already on in
   il-central-1 (managed by the account). When it is off, labs-init only prints a note with the command an account admin runs
   (it is account-wide per region): `aws ec2 enable-serial-console-access --region <region>`. It never turns it on itself.
   `labs-init` and `up` also hand the node the lab subnets' CIDRs and its own address, which `aws.direct_ssh` labs need.
2. **Activate the cost-allocation tag (the one manual step, free).** In Billing → Cost allocation tags (in the
   **management (payer) account** if the lab account is in an organization, else in the lab account), activate
   `crucible:lab-id`. It shows up there only after a tagged resource has existed, which can take up to 24 h, and
   activation is not retroactive unless you request a backfill. Until it is active the export has no lab ids, so
   billed spend stays empty and the Ledger keeps showing metered spend.
3. `crucible aws up` (with Crucible-account credentials). It reads the labs stack's outputs and turns aws labs on.
   **Run this first `up` where `deploy/aws/labs/terraform.tfstate` lives** (the machine you ran labs-init on, or one where
   labs-init has restored it). If it
   cannot read the labs outputs and aws labs are not wired yet, it leaves them off without an error.
   **A node built before M6 must be rebuilt** (`crucible aws teardown --yes`, then `crucible aws up`): deploy.sh
   changed and user data never updates in place.
   **Upgrading from a Crucible that used infracost:** re-run labs-init (with your budget flags) **before** `up`.
   `deploy/aws/main` refuses aws labs without the plan role and export outputs, and says so. Drop
   `infracost_api_key` from `crucible.tfvars`; it is no longer a variable.
4. **The first export arrives within about 24 h.** Check it by hand once (lab-account admin credentials):
   `aws bcm-data-exports list-exports --region us-east-1` lists `<name>-labs`, and after a day
   `aws s3 ls s3://<export_bucket>/cur/ --recursive` shows a `Manifest.json` and `.csv.gz` files under
   `<name>-labs/`. Then run `terraform -chdir=deploy/aws/labs plan` with the same `-var`s labs-init passed (region,
   budget, `crucible_account_id`, `node_role_name`): it must show no changes. The stack leaves the export's
   `BILLING_VIEW_ARN` to AWS's default (the primary billing view), so if the plan wants to change the export's
   `table_configurations`, record it here and fix the stack before the next labs-init.

**Server settings.** `crucible aws up` sets these from the labs stack's outputs; set them yourself only on another
host. With `CRUCIBLE_AWS_LABS=1`, crucible serve refuses to start without `CRUCIBLE_AWS_PLAN_ROLE_ARN`,
`CRUCIBLE_AWS_EXPORT_BUCKET` and `CRUCIBLE_AWS_EXPORT_NAME` (outputs `plan_role_arn`, `export_bucket`, `export_name`).
`CRUCIBLE_AWS_EXPORT_PREFIX` defaults to `cur`. `INFRACOST_API_KEY` and `CRUCIBLE_INFRACOST` are gone and ignored.

**Prices.** Crucible downloads the public AWS Price List (free, no credentials) for the allowed regions every week,
and at start when its prices are more than 6 days old. It keeps on-demand prices (Linux, shared tenancy). Admins see
**Price data from <date>** on the Ledger. If the prices are missing or more than 30 days old, that line turns red and
AWS labs can't be estimated or requested: estimates fail closed, never $0. Admins see the last refresh error there
too. A refresh failure is usually the node's outbound HTTPS to `pricing.us-east-1.amazonaws.com`; the job queue
retries a failed refresh with backoff. Restarting the api downloads again only when the stored prices are more than
6 days old: fresher prices are kept as they are.

**Estimates.** An estimate is a `terraform plan` against an empty state, run once per content version in a short-lived
pod in the `crucible-estimates` namespace (the Helm chart creates it, with a namespaced Role for Crucible there). The
pod gets only a 15-minute plan-role session and no state backend, so it can read but never create or change
anything. The plan is cached per version and priced fresh on every view; a failed plan is retried after 5 minutes.

**What lab modules must look like (content authors).** Crucible adds `crucible.tf` to every aws lab module: the S3
backend, and `provider "aws"` with the lab's region and `default_tags` carrying `crucible:lab-id`, `crucible:team`
and `crucible:training`. Modules must not declare their own provider or backend (lint), so every resource gets the
tags. IAM refuses any EC2 create that does not carry `crucible:lab-id`, so a missing tag fails the apply; it never
leaks. Lint also refuses any resource type Crucible can't price unless the lab declares it under `aws.prices` (the
authors' page *AWS labs* lists the types). Two more rules:
- **Every instance uses the lab's own security group** (`vpc_security_group_ids = [aws_security_group.x.id]`). The
  VPC default group is shared by all labs, so IAM denies launching into it (or any untagged group). Rules may be
  inline on `aws_security_group` or `aws_vpc_security_group_ingress_rule` / `_egress_rule`.
- **The root volume must be tagged at launch.** If the sandbox EC2 step (step 10) shows `default_tags` do not reach
  the root volume, `aws_instance` needs `volume_tags = { "crucible:lab-id" = var.crucible_lab_id }`; record it here.

**Verify on a real sandbox (by hand; nothing in `make` touches AWS).** Enrol yourself in Forge 401 (Team page),
request "Cloud Heat", approve it, then:
1. In the workspace terminal: `aws sts get-caller-identity` shows `assumed-role/crucible-lab/crucible-lab-<id>`.
   On the node: `k3s kubectl -n lab-<id> get pods` shows `lab` Running and `tf-apply` Completed.
2. Task 1 and task 2 both pass with **Check** (task 2: `echo hi > f && aws s3 cp f s3://crucible-lab-$CRUCIBLE_LAB_ID/forged.txt`).
3. **The boundary holds.** In the workspace, `L=$CRUCIBLE_LAB_ID`, `AMI=<an AL2023 x86_64 AMI id in your region>`,
   `T="ResourceType=instance,Tags=[{Key=crucible:lab-id,Value=$L}]" "ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=$L}]"`
   (use `$T` unquoted below), `V=<a volume you own>` from step 4's create, and the lab's own security group:
   `SG=$(aws ec2 create-security-group --group-name lab-$L --description lab --tag-specifications "ResourceType=security-group,Tags=[{Key=crucible:lab-id,Value=$L}]" --query GroupId --output text)`.
   Every `run-instances` below passes `--security-group-ids $SG` unless it says otherwise, so each must-fail fails
   only for its own reason. **Positive controls first:**
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --security-group-ids $SG --tag-specifications $T` returns
     **DryRunOperation** (allowed). If this fails, a deny below is too broad (most likely `ec2:Tenancy` or
     `ec2:VolumeType` missing from the request context): fix the labs stack before anything else.
   - `aws ec2 create-volume --dry-run --size 8 --volume-type gp3 --availability-zone eu-west-1a --tag-specifications "ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=$L}]"` returns DryRunOperation.

   Each of these must fail with AccessDenied / UnauthorizedOperation (the `--dry-run` ones with UnauthorizedOperation,
   never DryRunOperation):
   - `aws ec2 create-volume --size 1 --availability-zone eu-west-1a` (no lab tag)
   - the same with `--tag-specifications 'ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=000000000000}]'` (someone else's lab id)
   - the same with `Key=CRUCIBLE:LAB-ID,Value=$L` (key case variant, I5: the sweep would never see it)
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --tag-specifications $T` (**no**
     `--security-group-ids`: the VPC default security group, shared by every lab; ruling O1), and the same with
     `--security-group-ids <the default group's id>`
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.large --security-group-ids $SG --tag-specifications $T` (instance type;
     both instance and volume are tagged, so only SmallInstancesOnly can refuse it)
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --security-group-ids $SG --placement Tenancy=dedicated --tag-specifications $T` (tenancy)
   - `aws ec2 create-volume --dry-run --volume-type io2 --iops 1000 --size 100 --availability-zone eu-west-1a --tag-specifications "ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=$L}]"`,
     and the same with `--volume-type gp3 --size 51`, and with `--volume-type gp3 --size 8 --iops 16000` (volume type, size, IOPS)
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --security-group-ids $SG --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=500}' --tag-specifications $T` (root volume size)
   - `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --security-group-ids <a security group tagged with another lab's id> --tag-specifications $T` (I4)
   - `aws ec2 create-network-interface --subnet-id <default subnet> --tag-specifications "ResourceType=network-interface,Tags=[{Key=crucible:lab-id,Value=$L}]"` (no standalone ENIs)
   - `aws ec2 delete-tags --resources $V` (**no** `--tags`: would strip every tag, C1) and
     `aws ec2 delete-tags --resources $V --tags Key=crucible:lab-id` (lab tags are immutable)
   - `aws ec2 modify-instance-attribute --instance-id <your instance> --instance-type t3.large` (only termination protection can be turned off)
   - `aws ec2 modify-instance-attribute --instance-id <your instance> --no-disable-api-termination --instance-type Value=t3.large`
     (two attributes in one call: EC2 or IAM must refuse it; then `aws ec2 describe-instance-attribute --instance-id <your instance> --attribute instanceType`
     still shows the old type)
   - `aws ec2 modify-instance-attribute --instance-id <your instance> --groups <the default group's id>` (groupSet onto an untagged group)
   - `aws s3 mb s3://not-a-lab-bucket-$RANDOM` (bucket name)
   - `aws s3 mb s3://crucible-lab-$L-x --region <a region not in allowed_regions, e.g. us-west-2>` and
     `aws ec2 describe-vpcs --region us-west-2` (regions)
   - with your lab bucket `B=crucible-lab-$L`: `aws s3api put-object-lock-configuration --bucket $B --object-lock-configuration ObjectLockEnabled=Enabled`,
     `aws s3api put-bucket-policy --bucket $B --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:DeleteBucket","Resource":"arn:aws:s3:::'$B'"}]}'`,
     `aws s3api delete-bucket-tagging --bucket $B`, `aws s3api put-bucket-acl --bucket $B --acl private` (I2, I3)
   - `aws s3 cp s3://<labstate bucket>/labs/<another lab id>.tfstate -` (someone else's state)
   - `aws iam list-roles` (no IAM)
   - `timeout 3 bash -c 'exec 3<>/dev/tcp/169.254.169.254/80' && echo REACHABLE` must not print REACHABLE (IMDS,
     blocked by M4's NetworkPolicy; the workspace image has no curl). From a pod in the lab namespace, on the node:
     `k3s kubectl -n lab-<id> run imds --rm -i --restart=Never --image=busybox -- sh -c 'sleep 5; wget -T 3 -qO- http://169.254.169.254/'`
     must time out. The `sleep 5` matters: a brand-new pod can reach IMDS for about a second before the policy is
     in force, so an early pass proves nothing.
   - On the node, a nested container (like the workspace and M4 compose services) cannot get an IMDSv2 token even
     without the policy, because the node's hop limit is 2:
     `k3s kubectl -n lab-<id> exec lab -c dind -- docker run --rm curlimages/curl -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token`
     must print no token.

   **Protection does not stop the sweep (I2).** `aws ec2 run-instances --image-id $AMI --instance-type t3.micro --security-group-ids $SG --disable-api-termination --tag-specifications $T`
   succeeds (EC2 has no condition key for it). End the lab: the end-of-lab sweep turns termination protection off and
   terminates it. If the Ledger shows it as "failed" with OperationNotPermitted, the `ec2:Attribute/disableApiTermination`
   condition did not match: record the real key here.
   **An untagged bucket is swept (I3).** `aws s3api create-bucket --bucket crucible-lab-$L-untagged --create-bucket-configuration LocationConstraint=eu-west-1`
   (no tags), then end the lab: it is deleted (found by name). `put-bucket-tagging` with another lab's id is allowed
   (S3 has no tag condition key for it); the sweep goes by the name, but the billing export bills that bucket to the
   other lab. Accepted: S3 storage for one hour is cents.
   **Accepted reads.** `ec2:Describe*` and `tag:GetResources` are account-wide: a trainee can list other labs' ids and
   resource ARNs and the node's user data (which holds no secrets; keep it that way). Use a dedicated lab account if
   that matters.
4. **A tagged leak is swept.** Create a volume the lab owns:
   `aws ec2 create-volume --size 1 --availability-zone eu-west-1a --tag-specifications "ResourceType=volume,Tags=[{Key=crucible:lab-id,Value=$CRUCIBLE_LAB_ID}]"`.
   Click **End lab**. Within ~5 min the lab is "cooled". The Ledger (admin) shows the volume under Reaper findings as
   "end-of-lab sweep / deleted", and `aws resourcegroupstaggingapi get-resources --tag-filters Key=crucible:lab-id,Values=<id>`
   (admin credentials) returns nothing. Terminated instances can stay listed for about an hour; that is AWS.
5. **The reaper.** With admin credentials, create a volume tagged `crucible:lab-id=<id of a lab that ended over an
   hour ago>`, and another tagged with `cccccccccccc`. Ledger → **Refresh now**: the first is deleted, the second is
   reported and still exists (delete it by hand), and admins get one "reaper found" email.
6. **Credentials refresh.** Start a lab with a 2 h TTL (program `lab_defaults.ttl`), and after 65 minutes run
   `aws sts get-caller-identity` in the workspace: it still works.
7. **CloudTrail.** LookupEvents needs no trail (90-day event history). After step 3, Ledger → **Refresh now**: no
   "CloudTrail" finding should appear, because every successful create carried the tag. A finding here means a policy gap.
8. **Tags, session names and the policy variable.** (a) The instance step 10's terraform launched has its root
   volume tagged `crucible:lab-id=<id>` (`aws ec2 describe-volumes --filters Name=attachment.instance-id,Values=<i-…>`).
   An untagged root volume cannot happen silently: IAM requires the tag on `volume/*` at RunInstances, so the
   apply is **denied** instead (step 10 says what to do then).
   (b) CloudTrail → Event history for a create in step 3 shows **User name** = `crucible-lab-<id>` (the session
   name); the Ledger's CloudTrail check matches on it. (c) The deny in step 3 for someone else's lab id proves
   `${aws:PrincipalTag/crucible:lab-id}` resolves; if *your own* tagged create is also denied, the session tag did not
   reach the role (check `sts:TagSession` on the trust policy and the node role).
9. **Costs.** While the lab runs, its Ledger row shows a **Metered** figure (the resources in its Terraform state at
   list prices), and **Cost so far** follows it. 24–48 h after the lab ends (once the export has arrived and the tag
   is active), Ledger → **Refresh now** shows its **Billed** figure. Once settled, the row has no "(so far)",
   Estimate vs billed counts it, and the billed figure is what counts against budgets.
10. **The EC2 path.** In your sandbox content repo (not `examples/`), add this as `terraform/ec2.tf` next to Cloud
    Heat's `main.tf`, and sync:
    ```hcl
    data "aws_ami" "al2023" {
      owners      = ["amazon"]
      most_recent = true
      filter {
        name   = "name"
        values = ["al2023-ami-2023.*-x86_64"]
      }
    }
    resource "aws_security_group" "box" {
      name_prefix = "crucible-lab-${var.crucible_lab_id}-"
      vpc_id      = var.crucible_vpc_id != "" ? var.crucible_vpc_id : null
    }
    resource "aws_vpc_security_group_ingress_rule" "ssh" {
      security_group_id = aws_security_group.box.id
      cidr_ipv4         = "10.0.0.0/8"
      ip_protocol       = "tcp"
      from_port         = 22
      to_port           = 22
    }
    resource "aws_vpc_security_group_egress_rule" "out" {
      security_group_id = aws_security_group.box.id
      cidr_ipv4         = "0.0.0.0/0"
      ip_protocol       = "-1"
    }
    resource "aws_instance" "box" {
      ami                    = data.aws_ami.al2023.id
      instance_type          = "t3.nano"
      vpc_security_group_ids = [aws_security_group.box.id]
      subnet_id              = length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null
      # in an account without a default VPC, add: associate_public_ip_address = false
      root_block_device {
        volume_type = "gp3"
        volume_size = 8
      }
    }
    resource "aws_ebs_volume" "data" {
      availability_zone = aws_instance.box.availability_zone
      size              = 1
      type              = "gp3"
    }
    resource "aws_volume_attachment" "data" {
      device_name = "/dev/sdf"
      volume_id   = aws_ebs_volume.data.id
      instance_id = aws_instance.box.id
    }
    ```
    Request and approve the lab. Expected:
    - The request shows a non-zero estimate, and the lab page's **How this estimate was worked out** lists the
      instance (t3.nano with its 8 GiB gp3 root, no public IPv4), the data volume (1 GiB gp3), the attachment as
      free, and the security group and its rules as free.
    - The lab becomes ready. While `tf-apply` runs, `k3s kubectl -n lab-<id> top pod tf-apply` stays well under its
      1 Gi limit; record the peak here.
    - In the workspace: the group, both rules (`aws ec2 describe-security-group-rules --filters Name=group-id,Values=<sg>`),
      the instance, its root volume and the data volume all carry `crucible:lab-id=<id>`.
    - If the apply fails with UnauthorizedOperation on RunInstances, decode the message
      (`aws sts decode-authorization-message`, admin credentials). A `volume/*` resource means `default_tags` did
      not reach the root volume: add `volume_tags = { "crucible:lab-id" = var.crucible_lab_id }` to the instance,
      and record it here and under "What lab modules must look like". A `security-group-rule/*` resource means
      the rule resources need a change in the labs stack; until that lands, use inline rules.
    - A detached ENI is a known gap: `aws ec2 run-instances --dry-run --image-id $AMI --instance-type t3.micro --tag-specifications $T --network-interfaces "DeviceIndex=0,SubnetId=<default subnet>,Groups=$SG,DeleteOnTermination=false"`
      returns DryRunOperation (see the operational notes).
    - **End lab**: terraform destroy removes everything, the lab is "cooled", and the Ledger shows no "failed"
      findings for it. `aws resourcegroupstaggingapi get-resources --tag-filters Key=crucible:lab-id,Values=<id>`
      (admin credentials) lists at most the terminated instance (AWS keeps it listed for about an hour).
    - Run it again. Before End, create a leftover in the workspace:
      `aws ec2 create-security-group --group-name leak-$L --description leak --tag-specifications "ResourceType=security-group,Tags=[{Key=crucible:lab-id,Value=$L}]"`.
      After End, the Ledger shows it as "end-of-lab sweep / deleted".

Record anything that differed (IAM condition keys, CloudTrail field names, tag propagation to root volumes, rule
tagging, tf-apply memory) in this section, and fix the code or the labs stack before the release.

**OS-level labs (machines the trainee works on as root, boots, breaks and repairs).**

*The workspace image.* `deploy/workspace` is `amazon/aws-cli` plus ssh, the Session Manager plugin and the two helpers
`lab-ssh` and `lab-console`. Every release pushes it (`ghcr.io/platform-infra-org/crucible-workspace`, public) and pins the
release's chart to its digest, so an install running a release has those commands with nothing to set. Leave `workspace_image`
empty to keep that pin; set it only to override it. The in-tree chart (a `--build` deploy without the override) keeps plain
`amazon/aws-cli`, where those commands don't exist. To publish your own
(`make workspace` builds one local arch for a quick look; do not use it to publish):
```bash
docker buildx build --platform linux/amd64,linux/arm64 -f deploy/workspace/Dockerfile --push \
  -t ghcr.io/platform-infra-org/crucible-workspace:<version> deploy/workspace
docker buildx imagetools inspect ghcr.io/platform-infra-org/crucible-workspace:<version>   # note the Digest: sha256:...
```
Then in `deploy/aws/main/crucible.tfvars` set the override `workspace_image = "<registry>/crucible-workspace@sha256:<digest>"`
(a digest is required; a tag is refused) and run `crucible aws up`. `crucible aws status` must show no blocked host (it checks `ghcr.io` and `pkg-containers.githubusercontent.com`, which the node needs to pull the image). The serial console endpoints and host-key fingerprints are baked
into the image from `deploy/workspace/serial-console-endpoints`; `go test ./internal/awsops` fails if that file and the table
`crucible aws status` uses disagree.

*Three ways in, none needing an inbound path.* Session Manager (`aws ssm start-session`, `lab-ssh` through
`AWS-StartSSHSession`, `ssm send-command`); plain SSH from the workspace (`lab-ssh --direct`, only for labs that set
`aws.direct_ssh`); and the EC2 Serial Console (`lab-console`, or the console page). All use keys valid for one minute
(`ec2-instance-connect`). The AWS console button on a running AWS lab signs the trainee in with the lab's own session (the lab
role's permissions only, a fresh lab session per press, so it lasts an hour from the click; one link a minute per lab).

*What spec §9 already confirmed in our account (2026-11-04, read-only and dry run).* Serial console access is on in il-central-1;
its endpoint resolves and answers on TCP 22 from a laptop, with the RSA host key AWS publishes; `ec2-instance-connect`, `ssm-guiconnect`,
`ssm`, `ssmmessages` and `signin.aws.amazon.com` resolve; `RunInstances` dry runs (Ubuntu 24.04 t3.micro with a profile and a lab-id tag, Windows
Server 2022 t3.small) succeed in spokeSubnet1, as do `GetConsoleScreenshot` and `GetConsoleOutput`; instance profiles can be created; Session
Manager works from these subnets; the SCPs let `SendSSHPublicKey` and `SendSerialConsoleSSHPublicKey` through (both failed only with
"instance not running" on the stopped node); `getSigninToken` with an assumed-role session returned a token.

*Accepted deviation from spec §3.2.* The lab role allows `ssm:GetCommandInvocation` on `*`: IAM cannot scope it to instances. A
session needs the command id (an unguessable UUID) to read an invocation, and `ssm:ListCommandInvocations` and `ssm:ListCommands`
are **not** granted, so a trainee cannot discover other labs' commands. `SendCommand` itself is scoped to instances tagged with the lab's id.

*Still to verify by hand (spec §10), each with a real lab; record any difference here.* Use a lab whose module attaches the
`crucible-lab-instance` profile (Forge 402 uses it; `examples/forge-402`).
1. **Plain SSH wall.** Needs a `direct_ssh` lab. From its workspace terminal:
   `nc -z -w 3 <the node's private IP> 22; echo $?` must print a non-zero status after about 3 s (the lab pod's NetworkPolicy
   excepts the node's /32; k3s must enforce that, including for a pod's traffic to its own node). Then
   `lab-ssh --direct <lab instance id>` must give a shell. If `nc` succeeds, stop: the node's sshd is exposed to labs.
2. **GRUB rescue on the serial console.** Start the Forge 402 rescue module. In the workspace: `lab-console <instance id>`; press
   Enter. Expect the GRUB menu (and the initramfs shell after the break). Leave with Enter then `~.`. Then, in the AWS console
   button's session, EC2 → the instance → Connect → EC2 Serial Console shows the same.
3. **Fleet Manager remote desktop.** Start a lab with a Windows Server instance with the `crucible-lab-instance` profile. In
   the AWS console (button), Systems Manager → Fleet Manager → the instance → Connect with Remote Desktop. It must open; it needs the
   session's own `ssm:StartSession` and `ssm-guiconnect:*`, both in the lab role. If it fails, note the denied action in CloudTrail.
4. **Terminate and resume.** In the workspace: `aws ssm start-session --target <id>`, then in a second terminal
   `aws ssm describe-sessions --state Active` and `aws ssm terminate-session --session-id <that id>`: it works (the id starts with
   the session's `${aws:userid}`). Terminating a session id that is not the lab's own must be denied.
5. **Console sign-in.** Press the AWS console button on a running AWS lab: a new tab opens signed in as
   `crucible-lab-<id>`; the account's other resources are limited by the lab role; the console session ends an hour after the click (a role chained from
   the node role caps at one hour; each press makes a fresh lab session). The sign-in link works once, within 15 minutes, and a second press within a minute for the same lab is refused.
6. **Serial endpoint reachable.** `crucible aws status` must not list `ec2-serial-console.il-central-1.api.aws:22` under blocked
   hosts (egress line says every host is reachable). If it does, the firewall blocks TCP 22 to it and `lab-console` will hang.
7. **Run Command stays in the lab.** From a lab workspace, `aws ssm send-command --targets Key=tag:crucible:lab-id,Values=<another lab id>
   --document-name AWS-RunShellScript --parameters commands=true` is denied or reaches no instance.

**What trainees can see.** A lab session can read and write its own terraform state
(`s3://<labstate bucket>/labs/<id>.tfstate`), and the workspace has those credentials. **Content authors must not
put secrets in terraform state or outputs** (no generated passwords, keys or tokens the trainee should not see).

**Operational notes.**
- The reaper runs **hourly** (billed-spend ingestion from the export every 6 h). It deletes what a known lab left behind once
  the lab has been over for an hour, so leftovers, including any created later with credentials leaked from the
  workspace (valid up to 60 min after End), live at most about 2 h after End.
- **Removing a region.** The sweep and the reaper only look in `CRUCIBLE_AWS_LAB_REGIONS` (the labs stack's
  `allowed_regions`). A region you take out is no longer swept or reaped: end every lab in it, and check
  `aws resourcegroupstaggingapi get-resources --region <it> --tag-filters Key=crucible:lab-id` is empty, before
  re-running labs-init and `up` without it.
- **Detached ENIs can exist.** Labs cannot create a standalone ENI, but `run-instances` with
  `DeleteOnTermination=false` on an interface leaves an untagged ENI behind after the instance is terminated. The
  sweep does not find it (it is untagged, and ENIs are reported, not deleted). An ENI is free while detached, but it
  counts against the account's limits and keeps any Elastic IP associated with it. Now and then, with admin
  credentials: `aws ec2 describe-network-interfaces --filters Name=status,Values=available` and delete what labs left.
- **Lab bucket names are global.** OwnBuckets matches `crucible-lab-<id>*` by name, with no `s3:ResourceAccount`
  pin. A bucket of that name in another account is reachable only if that account's bucket policy allows it, so
  this is harmless, but a dedicated lab account is still the safer setup.
- If crucible serve restarts while an aws lab is being destroyed, the destroy is not resumed at once: the sweep picks
  the lab up as stuck after **70 minutes** and destroys it again (the hourly cost cap limits what that can cost).
- `CRUCIBLE_AWS_LABS=dryrun` (Helm `awsLabs.dryRun`) prices aws labs from `CRUCIBLE_DEV_LAB_USD_PER_HOUR` and seeds
  fixture list prices on every start, so metering works without AWS. Fixture prices never price a real lab: a
  database that ran in dry run needs one real price refresh (it happens at start) before real estimates work.
- `crucible aws up` needs the labs stack's outputs once aws labs are on: run it where `deploy/aws/labs/terraform.tfstate`
  lives. If it cannot read them it stops rather than turning aws labs off (running aws labs would lose credential
  refresh, destroy and the sweep). `--no-labs` turns aws labs off on purpose; end every aws lab first.
- `crucible aws teardown` never touches the labs stack; it has local state in `deploy/aws/labs` and
  `prevent_destroy` on the state bucket.

**Troubleshooting.**
- A lab sits in "provisioning" and then fails with `terraform apply failed: …`. The trainee sees the log tail. On the node, `k3s kubectl -n lab-<id> get pod tf-apply -o jsonpath='{.status.containerStatuses[0].state.terminated.message}'` shows the same.
- **A lab can't be requested.** The lab page says why when it is the lab's own limit ("This lab is priced at $X/h,
  above its $Y/h limit; its maintainers need to make it cheaper."), a region this server doesn't allow, "AWS price
  data is out of date; an admin needs to refresh it" (missing or over 30 days old: see the Ledger's price line and
  **Refresh now**) or "Crucible can't price: <types>; the lab must declare a price for them". When the lab itself
  fails to estimate (a failed plan, or a resource Crucible's pricer refuses, such as io2 or an unknown size), the
  training's editors and admins see the error on the lab page ("The estimate failed …"); participants see only "the
  lab can't be requested right now". Every failure is also in the api log:
  `k3s kubectl -n crucible logs deploy/crucible -c api | grep 'lab quote failed'`.
  `terraform plan failed: …` carries the plan's own error (often a data source the plan role can't read: it has
  only `ec2:Describe*`, `s3:GetBucket*` and `s3:ListAllMyBuckets`); `no aws price for <key> in <region>` names a
  price key the list doesn't have (an instance type not offered in that region, say).
  Plan pods: `k3s kubectl -n crucible-estimates get pods` (`tf-plan-*`; leftovers are removed after 15 minutes).
- If the Ledger says the billed figures are out of date, look at the export (setup step 4), the ops role's read of
  the export bucket, and the tag activation (step 2). Budgets keep using metered spend and estimates meanwhile.
- **Run one Crucible deployment per lab account.** The reaper never deletes resources of labs it does not know. It reports them instead, so a second deployment's labs show up as findings rather than being deleted.
