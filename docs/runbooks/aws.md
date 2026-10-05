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

**Platform repo prerequisites (M3).** `platform.yaml` must define `cost_tiers` (`auto_approve_usd`, `tier1_usd`, `tier2_usd`); Crucible refuses a platform config without them and keeps serving the last good one. Programs may name a `schedule` defined under `schedules`. The bot git credential now needs push access: the UI commits config changes (team roster, program settings, budgets) to the platform repo. Set `git_bot_email` (and optionally `git_bot_name`) in your tfvars to an author email your git host accepts for that bot; the default `crucible-bot@example.com` is rejected by hosts that require verified commit emails.

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

**Email (optional).** Add CRUCIBLE_SMTP_ADDR (host:port), CRUCIBLE_SMTP_FROM, CRUCIBLE_SMTP_USERNAME and CRUCIBLE_SMTP_PASSWORD to the crucible-secrets Secret (kubectl -n crucible edit secret crucible-secrets) and restart the api deployment. Without them Crucible sends no email; Slack/Teams webhooks in team.yaml still work.

## Cluster labs (sysbox)

Cloud-init installs Sysbox CE (pinned version, sha256-checked; `sysbox_version` and `sysbox_sha256_amd64` in `deploy/aws/main/variables.tf`, update both together, amd64 only) and registers the `sysbox-runc` runtime with k3s's containerd through `config-v3.toml.tmpl` (containerd 2.x, k3s >= 1.32). It also creates the `sysbox-runc` RuntimeClass, and the chart defaults to `clusterLabs.enabled: true`. Lab pods run in `lab-<id>` namespaces with Pod Security `baseline`, a quota, and a NetworkPolicy that blocks `169.254.169.254` (IMDS) and every private range.

A node built before M4 has no sysbox, because `user_data` changes never replace the node. Rebuild it with `crucible aws teardown` and then `crucible aws up` (data is restored from the latest snapshot).

Verify after `up` (over SSM, as root). Nothing here is covered by `terraform test`; it only checks the rendered script.
1. `systemctl is-active sysbox` prints `active`, and `k3s kubectl get runtimeclass sysbox-runc` lists it.
2. Start Forge 101's "Into the Crucible" lab in the browser. Then `k3s kubectl get pods -A -l crucible.io/lab` shows one `lab` pod `Running`, and `k3s kubectl -n lab-<id> get pod lab -o jsonpath='{.spec.runtimeClassName}'` prints `sysbox-runc`.
3. `hostUsers: false` with `runtimeClassName: sysbox-runc` is accepted and runs on k3s: `k3s kubectl -n lab-<id> get pod lab -o jsonpath='{.spec.hostUsers}'` prints `false` and the pod is `Running`. If the pod is rejected or stuck, sysbox and the user-namespace field conflict on this kernel/containerd: drop `hostUsers: false` from the pod spec (sysbox already gives each pod its own user-namespace mapping) and note the change here.
4. Network isolation. Get a shell with `k3s kubectl -n lab-<id> exec -it lab -c <container> -- sh`, then run the checks first in the lab pod itself and again from a compose container started inside it (`docker run --rm -it alpine sh`, or `docker compose exec <service> sh`). Each of these must FAIL (timeout or refused):
   - `wget -T 3 -qO- http://169.254.169.254/latest/meta-data/` (IMDS)
   - `wget -T 3 -qO- --no-check-certificate https://10.43.0.1:443/` (Kubernetes API via the service IP)
   - `nc -zw3 <node-private-ip> 10250` (kubelet) and `nc -zw3 <node-private-ip> 6443` (API server)

   Find the node IP with `hostname -I`. This must succeed from both places: `wget -T 5 -qO- https://registry-1.docker.io/v2/` (answers 401, which proves egress) and a real pull such as `docker pull alpine` inside the lab. If the compose-container checks pass through (traffic from nested containers is NATed to the pod IP, so the NetworkPolicy should still apply), treat it as a release blocker.
5. End the lab. The namespace disappears within a minute (`k3s kubectl get ns -l crucible.io/lab`).

**Lab stuck in "provisioning" or failing with "could not be started":** run `k3s kubectl -n lab-<id> describe pod lab`. `no runtime for "sysbox-runc"` means containerd did not load the template: check `/var/lib/rancher/k3s/agent/etc/containerd/config.toml` for the `sysbox-runc` block, then run `systemctl restart k3s`.

## Troubleshooting
- **No certificate.** Check that DNS points at the Elastic IP and port 80 is reachable. Then run `sudo k3s kubectl -n kube-system logs deploy/traefik`. If the Traefik chart bundled with your k3s version rejects `ports.web.redirections`, use `ports.web.redirectTo: {port: websecure}` in `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml`.
- **Bootstrap failed.** `deploy` reports "node bootstrap failed" when `/opt/crucible/.failed` exists. Read `sudo tail -f /var/log/crucible-bootstrap.log` over SSM; `.ready` appears only after the first deploy attempt. After fixing the cause, clear the marker with `sudo rm /opt/crucible/.failed` and run `crucible aws deploy` again.
- **App logs.** `sudo k3s kubectl -n crucible logs deploy/crucible -c api` (and `-c restore`).
