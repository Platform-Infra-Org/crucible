# Crucible on AWS: runbook

## One-time setup (per AWS account)
1. Install: terraform >= 1.10, AWS CLI v2 (logged in), Docker with buildx, Helm 3, Go 1.26. Run every command from the repo root. Either `go install ./cmd/crucible` (then use `crucible aws ...`) or prefix commands with `go run ./cmd/crucible`.
2. `go run ./cmd/crucible aws init --region eu-west-1 --domain crucible.example.com`. This creates the state and data buckets and the Cognito user pool (none of them are ever destroyed).
3. **Back up the persistent stack's state now.** `deploy/aws/persistent/terraform.tfstate` is local and git-ignored. Copy it somewhere safe (for example `aws s3 cp deploy/aws/persistent/terraform.tfstate s3://<state_bucket>/persistent.tfstate`, or a password manager) and repeat after every `init`.
4. `cp deploy/aws/main/terraform.tfvars.example deploy/aws/main/crucible.tfvars` and fill it in. `domain` there must equal the `--domain` you gave `init`.
   Sign-in needs no settings here; it is wired from the Cognito user pool automatically.
5. `go run ./cmd/crucible aws up` (`--var-file` defaults to `deploy/aws/main/crucible.tfvars` in the repo root).
   Right after the apply it prints `Point an A record <domain> → <ip>`: create that DNS record **while `up` waits** for the node (skip if `route53_zone_id` is set).
   The first HTTPS request may take ~1 min while Let's Encrypt issues the certificate. If `up` ends with "isn't answering yet", DNS or the certificate is still settling: wait, then run `crucible aws status` until `healthz: ok`.
6. Invite people and add them to teams: follow [cognito.md](cognito.md).

Second machine or fresh clone: copy the persistent state back first, then run `crucible aws up` (it is idempotent) to initialise the S3 backend before using `deploy`.

## Everyday
| Want | Command |
|---|---|
| Ship the current commit | `crucible aws deploy` |
| Take a snapshot now | `crucible aws snapshot` |
| Stop paying for compute tonight | `crucible aws sleep` (snapshot, then stop) |
| Start before the schedule | `crucible aws wake` |
| Is it up? | `crucible aws status` |
| Shell on the node | `aws ssm start-session --target <instance_id>` |
| Invite / remove a person | [cognito.md](cognito.md) |

The schedule wakes the node at 07:30 and sleeps it at 19:30 (Mon-Fri, `schedule_timezone`). The backup runs at 19:15.

`t3a` instances run in *unlimited* CPU-credit mode by default: sustained CPU above the baseline (40% for `t3a.xlarge`) is billed as surplus credits (about $0.05 per vCPU-hour). Busy labs can add to the bill; check the instance's `CPUSurplusCreditsCharged` metric.

**Changing settings.** Edit `crucible.tfvars` and run `crucible aws up`. Node settings (`domain`, `platform_branch`, `backup_cron`, `schedule_timezone`, …) are stored in the SSM parameter `/<name>/env`, and the node re-reads it on every deploy, which `up` runs. The sleep/wake schedule (`wake_cron`, `sleep_cron`, `schedule_enabled`) is EventBridge, so the terraform apply inside `up` changes it directly. A new `domain` also needs `crucible aws init --domain <new>` first (the Cognito callback) and a new DNS record. `up` snapshots a running node before applying, in case the apply replaces it.

**Skipping the safety snapshot.** `sleep`, `teardown` and `up` take a snapshot first and refuse to continue if it fails. If the node is broken and cannot snapshot, add `--no-snapshot`. It prints a loud warning: **everything since the last backup is lost** if the disk goes away.

## Disaster / full teardown
- `crucible aws teardown --yes`: takes a snapshot, then destroys the main stack. The buckets and Cognito pool survive.
- `crucible aws up`: rebuilds the node. The restore init container loads the newest snapshot into the empty database. Config and content come back from git.
- **Re-point DNS**: the Elastic IP changes after a teardown, so update the A record to the IP `up` prints (automatic with `route53_zone_id`).
- Dated snapshots under `snapshots/` expire after 30 days, but **the last snapshot is always kept**: every backup also writes `latest/crucible-latest.dump`, which never expires. After a long teardown, the restore falls back to it.
- The git webhook secret is regenerated on teardown → up: update it in your git host (see Secrets).

## Lost persistent state
If `deploy/aws/persistent/terraform.tfstate` is lost, first restore your backup (step 3). Without one, run `terraform -chdir=deploy/aws/persistent init`, then `terraform import` each of the 12 resources (addresses in `deploy/aws/persistent/*.tf`; use the real IDs from the console): `aws_s3_bucket.state`, `aws_s3_bucket_versioning.state`, `aws_s3_bucket_public_access_block.state`, `aws_s3_bucket.data`, `aws_s3_bucket_versioning.data`, `aws_s3_bucket_server_side_encryption_configuration.data`, `aws_s3_bucket_public_access_block.data`, `aws_s3_bucket_lifecycle_configuration.data`, `aws_cognito_user_pool.users`, `aws_cognito_user_pool_domain.login`, `aws_cognito_user_pool_client.crucible`, `aws_cognito_managed_login_branding.crucible`. Then run `terraform plan` (variables `region` and `domain` as for `init`) and import anything it still wants to create. Never `apply` against an empty state: it would try to create duplicates.

## Secrets
The API needs `CRUCIBLE_QUIZ_SECRET`. It is generated by Terraform into SSM Parameter Store automatically. Rotating it reshuffles every open quiz; learners mid-quiz get a new question order. To rotate: `terraform -chdir=deploy/aws/main apply -replace=random_password.quiz -var-file=<your tfvars> <the -var flags that `crucible aws up` passes>` (simplest: `terraform -chdir=deploy/aws/main taint random_password.quiz`, then `crucible aws up`), then on the node `sudo k3s kubectl -n crucible rollout restart deploy/crucible` so the API picks up the new value.

**Git webhook secret.** Point your git host's push webhook at `POST https://<domain>/api/git/hook` with header `X-Crucible-Secret` set to the value of:
`aws ssm get-parameter --with-decryption --name /<name>/git_hook_secret --query Parameter.Value --output text` (`<name>` defaults to `crucible`). It is regenerated on every teardown → up, so update the webhook afterwards.

## Troubleshooting
- **No certificate.** Check that DNS points at the Elastic IP and port 80 is reachable. Then run `sudo k3s kubectl -n kube-system logs deploy/traefik`. If the Traefik chart bundled with your k3s version rejects `ports.web.redirections`, use `ports.web.redirectTo: {port: websecure}` in `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml`.
- **Bootstrap failed.** `deploy` reports "node bootstrap failed" when `/opt/crucible/.failed` exists. Read `sudo tail -f /var/log/crucible-bootstrap.log` over SSM; `.ready` appears only after the first deploy attempt. After fixing the cause, clear the marker with `sudo rm /opt/crucible/.failed` and run `crucible aws deploy` again.
- **App logs.** `sudo k3s kubectl -n crucible logs deploy/crucible -c api` (and `-c restore`).
