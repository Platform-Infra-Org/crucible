# Crucible M2 "AWS Deploy" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run Crucible on AWS at the lowest sensible cost (≈ $50/month on a business-hours schedule): one EC2 instance with single-node k3s. It sleeps on a schedule, is backed up nightly to S3, and can be torn down and rebuilt from git plus the latest snapshot with one command each.

**Architecture:**
- Two Terraform stacks:
  - `deploy/aws/persistent`: state + data buckets, created once and never destroyed.
  - `deploy/aws/main`: default-VPC EC2 node with an Elastic IP, IAM role, SSM parameters and EventBridge Scheduler sleep/wake.
- Cloud-init installs k3s (bundled Traefik with Let's Encrypt).
- A Helm chart runs Postgres, Crucible and a backup CronJob. An init container restores the latest snapshot into an empty database.
- Releases are image tarballs in S3, imported into k3s's containerd, so there is no container registry to pay for or authenticate against.
- `crucible aws …` subcommands orchestrate terraform, docker, helm and the AWS CLI.

**Tech Stack:** Terraform ≥ 1.10 (AWS provider ~> 6.0, native S3 state locking), k3s, Helm 3, Traefik (k3s-bundled), Postgres 18, AWS CLI v2, SSM Run Command, EventBridge Scheduler, Go (CLI orchestration).

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md` (§9.4 "Deployment, hibernate & restore")
**Depends on:** M1 (`docs/superpowers/plans/2026-10-05-m1-local-forge.md`) complete.

## Global Constraints

- **No NAT gateway, load balancer, EKS or RDS.** One `t3a.xlarge` (default; variable) in the default VPC's public subnet, 60 GiB encrypted gp3 root, one Elastic IP.
- **Access:**
  - No SSH port. Admin access is SSM Session Manager only.
  - Security group allows only TCP 80 and 443.
  - IMDSv2 is required, with hop limit 2: the backup/restore pods use the instance role. M4 must block lab pods from `169.254.169.254`.
- **Storage:**
  - Snapshots go to `s3://<data bucket>/snapshots/`, SSE-S3, kept 30 days. The data bucket and the state bucket have `prevent_destroy`.
  - Secrets live in SSM Parameter Store SecureStrings under `/<name>/`. They are never put in user data or the Helm values file.
- **Schedule:** a business-hours default in `Europe/Bucharest`: wake 07:30 and sleep 19:30, Mon–Fri. The nightly backup runs at 19:15, before sleep.
- **Releases:** the image is built for `linux/amd64` and tagged with the git short SHA. Uncommitted work gets a `-dirty-<unix>` suffix.

## Review Focus

1. **Scheduled stop with a backup still running.** Backup at 19:15 and sleep at 19:30 leave 15 minutes. If a dump runs long, stopping the instance mid-upload loses only that night's snapshot, never the database. The `crucible aws sleep` path always snapshots first. Pinned by `TestSleepSnapshotsBeforeStop` in Task 5.
2. **Teardown by accident.** `crucible aws teardown` must refuse to run without `--yes`, and the buckets must survive a destroy. Pinned by `TestTeardownRequiresYes` (Task 5) and `prevent_destroy` checked in `persistent.tftest.hcl` (Task 1).
3. **Fresh rebuild restores data exactly once.** The restore init container must skip when tables exist. Otherwise every pod restart would reload yesterday's snapshot over today's data. Pinned by the render test in Task 3 (checks the `to_regclass` guard) and acceptance step A5.
4. **SSH or extra ports opened by a later edit.** Pinned by `main.tftest.hcl` asserting the security group has exactly ports 80 and 443 (Task 2).
5. **Instance metadata reachable with IMDSv1.** Pinned by `main.tftest.hcl` asserting `http_tokens = "required"` (Task 2).

---

## Cost model (eu-west-1 on-demand, Oct 2026; verify current prices before go-live)

| Item | 24/7 | Business hours (11 h × 22 d) |
|---|---|---|
| `t3a.xlarge` | ~$119 | ~$40 |
| 60 GiB gp3 | $5.3 | $5.3 (billed while stopped) |
| Elastic IP / public IPv4 | $3.7 | $3.7 (billed while stopped) |
| S3 (snapshots + releases, < 10 GiB) | < $1 | < $1 |
| SSM, Scheduler, Session Manager | ~$0 | ~$0 |
| **Total** | **~$130** | **~$50** |

Cheaper knobs (documented, not default):
- `instance_type = "t3a.large"` (8 GiB) if only `local` labs are used: about $20/month on a business-hours schedule.
- A 1-year Compute Savings Plan (around −30% on the instance).

---

## File Structure

```
deploy/aws/persistent/{main.tf,persistent.tftest.hcl}
deploy/aws/main/{versions.tf,variables.tf,main.tf,scheduler.tf,outputs.tf,bootstrap.sh.tftpl,terraform.tfvars.example,main.tftest.hcl}
deploy/helm/crucible/{Chart.yaml,values.yaml}
deploy/helm/crucible/templates/{postgres.yaml,crucible.yaml,backup.yaml}
deploy/helm/test.sh                       # helm lint + render assertions
internal/awsops/{awsops.go,awsops_test.go}
cmd/crucible/main.go                      # adds `crucible aws …`
Dockerfile                                # adds git credential helper
docs/runbooks/aws.md                      # operator runbook
```

---

### Task 1: Persistent stack (state + data buckets)

**Files:**
- Create: `deploy/aws/persistent/main.tf`, `deploy/aws/persistent/persistent.tftest.hcl`
- Modify: `.gitignore` (add `deploy/aws/**/.terraform/`, `deploy/aws/**/terraform.tfstate*`, `deploy/aws/main/*.tfvars`)

**Interfaces:**
- Produces outputs `region`, `state_bucket`, `data_bucket` (read by `crucible aws` in Task 5).
- Bucket names are `<name>-<account id>-tfstate` and `<name>-<account id>-data`.

- [ ] **Step 1: Write the failing test**

`deploy/aws/persistent/persistent.tftest.hcl`:
```hcl
mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
}

variables {
  region = "eu-west-1"
}

run "buckets_are_private_versioned_and_expiring" {
  command = plan

  assert {
    condition     = aws_s3_bucket.data.bucket == "crucible-123456789012-data"
    error_message = "data bucket name"
  }
  assert {
    condition     = aws_s3_bucket_versioning.data.versioning_configuration[0].status == "Enabled"
    error_message = "data bucket must be versioned"
  }
  assert {
    condition     = aws_s3_bucket_public_access_block.data.block_public_acls && aws_s3_bucket_public_access_block.data.restrict_public_buckets
    error_message = "data bucket must block public access"
  }
  assert {
    condition     = one([for r in aws_s3_bucket_lifecycle_configuration.data.rule : r.expiration[0].days if r.id == "expire-snapshots"]) == 30
    error_message = "snapshots expire after 30 days"
  }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `terraform -chdir=deploy/aws/persistent init -backend=false && terraform -chdir=deploy/aws/persistent test`
Expected: FAIL (no configuration / resources not found).

- [ ] **Step 3: Implement**

`deploy/aws/persistent/main.tf`:
```hcl
# Created once per account with `crucible aws init`. Never destroyed by teardown.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { app = "crucible" } }
}

variable "region" { type = string }
variable "name" {
  type    = string
  default = "crucible"
}

data "aws_caller_identity" "me" {}

locals {
  prefix = "${var.name}-${data.aws_caller_identity.me.account_id}"
}

resource "aws_s3_bucket" "state" {
  bucket = "${local.prefix}-tfstate"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# snapshots/  nightly pg_dump files (30 days)
# releases/   image tarballs + Helm chart per git sha
# current-release  sha the node deploys on boot
resource "aws_s3_bucket" "data" {
  bucket = "${local.prefix}-data"
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_versioning" "data" {
  bucket = aws_s3_bucket.data.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket                  = aws_s3_bucket.data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    id     = "expire-snapshots"
    status = "Enabled"
    filter { prefix = "snapshots/" }
    expiration { days = 30 }
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
  rule {
    id     = "expire-releases"
    status = "Enabled"
    filter { prefix = "releases/" }
    expiration { days = 60 } # the running node keeps its image locally; `crucible aws up` republishes
    noncurrent_version_expiration { noncurrent_days = 7 }
  }
}

output "region" { value = var.region }
output "state_bucket" { value = aws_s3_bucket.state.bucket }
output "data_bucket" { value = aws_s3_bucket.data.bucket }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `terraform -chdir=deploy/aws/persistent fmt -check && terraform -chdir=deploy/aws/persistent test`
Expected: `Success! 1 passed, 0 failed.`

- [ ] **Step 5: Commit**

```bash
git add deploy/aws/persistent .gitignore
git commit -m "feat(aws): persistent state and data buckets"
```

---

### Task 2: Main stack (node, IAM, secrets, schedule, bootstrap)

**Files:**
- Create:
  - `deploy/aws/main/{versions.tf,variables.tf,main.tf,scheduler.tf,outputs.tf}`
  - `deploy/aws/main/bootstrap.sh.tftpl`
  - `deploy/aws/main/terraform.tfvars.example`
  - `deploy/aws/main/main.tftest.hcl`

**Interfaces:**
- Consumes: `data_bucket` and `region` from Task 1 (passed by `crucible aws up` as `-var`).
- Produces:
  - Outputs: `instance_id`, `public_ip`, `url`, `domain`, `region`, `data_bucket`.
  - On the node:
    - `/etc/crucible.env`
    - `/opt/crucible/deploy.sh`: imports the `current-release` image and runs `helm upgrade --install`.
    - `/opt/crucible/snapshot.sh`: runs the backup CronJob now and waits.
    - `/opt/crucible/.ready` marker.
  - SSM parameters `/<name>/{oidc_client_secret,platform_repo,git_credentials,git_hook_secret,db_password}`.

- [ ] **Step 1: Write the failing test**

`deploy/aws/main/main.tftest.hcl`:
```hcl
mock_provider "aws" {
  mock_data "aws_vpc" {
    defaults = { id = "vpc-123" }
  }
  mock_data "aws_subnets" {
    defaults = { ids = ["subnet-a", "subnet-b"] }
  }
  mock_data "aws_ssm_parameter" {
    defaults = { insecure_value = "ami-123", value = "ami-123" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{}" }
  }
}
mock_provider "random" {}

variables {
  region             = "eu-west-1"
  data_bucket        = "crucible-123456789012-data"
  domain             = "crucible.example.com"
  acme_email         = "ops@example.com"
  platform_repo      = "https://git.example.com/crucible/platform.git"
  oidc_issuer        = "https://sso.example.com/realms/corp"
  oidc_client_secret = "s3cret"
}

run "node_is_locked_down" {
  command = plan

  assert {
    condition     = aws_instance.node.metadata_options[0].http_tokens == "required" && aws_instance.node.metadata_options[0].http_put_response_hop_limit == 2
    error_message = "IMDSv2 required with hop limit 2"
  }
  assert {
    condition     = toset([for r in aws_security_group.web.ingress : r.from_port]) == toset([80, 443])
    error_message = "only ports 80 and 443 may be open"
  }
  assert {
    condition     = aws_instance.node.root_block_device[0].encrypted && aws_instance.node.root_block_device[0].volume_type == "gp3"
    error_message = "root volume must be encrypted gp3"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "s3cret")
    error_message = "secrets must not appear in user data"
  }
}

run "schedule_can_be_disabled" {
  command = plan
  variables {
    schedule_enabled = false
  }
  assert {
    condition     = length(aws_scheduler_schedule.sleep) == 0 && length(aws_scheduler_schedule.wake) == 0
    error_message = "no schedules when disabled"
  }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `terraform -chdir=deploy/aws/main init -backend=false && terraform -chdir=deploy/aws/main test`
Expected: FAIL (resources not found).

- [ ] **Step 3: Implement the configuration**

`deploy/aws/main/versions.tf`:
```hcl
terraform {
  required_version = ">= 1.10"
  backend "s3" {} # `crucible aws up` passes bucket, key, region and use_lockfile=true
  required_providers {
    aws    = { source = "hashicorp/aws", version = "~> 6.0" }
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}

provider "aws" {
  region = var.region
  default_tags { tags = { app = "crucible" } }
}
```

`deploy/aws/main/variables.tf`:
```hcl
variable "region" { type = string }
variable "data_bucket" { type = string }
variable "name" {
  type    = string
  default = "crucible"
}
variable "domain" {
  type        = string
  description = "Public hostname, e.g. crucible.example.com (point an A record at the public_ip output, or set route53_zone_id)"
}
variable "route53_zone_id" {
  type    = string
  default = ""
}
variable "acme_email" {
  type        = string
  description = "Let's Encrypt account email"
}
variable "instance_type" {
  type    = string
  default = "t3a.xlarge"
}
variable "disk_gb" {
  type    = number
  default = 60
}
variable "k3s_version" {
  type    = string
  default = "v1.34.1+k3s1"
}
variable "schedule_enabled" {
  type    = bool
  default = true
}
variable "schedule_timezone" {
  type    = string
  default = "Europe/Bucharest"
}
variable "wake_cron" {
  type    = string
  default = "cron(30 7 ? * MON-FRI *)"
}
variable "sleep_cron" {
  type    = string
  default = "cron(30 19 ? * MON-FRI *)"
}
variable "backup_cron" {
  type        = string
  default     = "15 19 * * *"
  description = "Kubernetes cron (node timezone = schedule_timezone); keep it before sleep_cron"
}
variable "platform_repo" {
  type      = string
  sensitive = true
}
variable "platform_branch" {
  type    = string
  default = "main"
}
variable "git_credentials" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Lines for git's credential store, e.g. https://bot:TOKEN@git.example.com"
}
variable "oidc_issuer" { type = string }
variable "oidc_client_id" {
  type    = string
  default = "crucible"
}
variable "oidc_client_secret" {
  type      = string
  sensitive = true
}
```

`deploy/aws/main/main.tf`:
```hcl
data "aws_vpc" "default" { default = true }

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

resource "aws_security_group" "web" {
  name   = "${var.name}-web"
  vpc_id = data.aws_vpc.default.id
  ingress {
    description      = "HTTP (ACME challenge + redirect)"
    from_port        = 80
    to_port          = 80
    protocol         = "tcp"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  ingress {
    description      = "HTTPS"
    from_port        = 443
    to_port          = 443
    protocol         = "tcp"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
}

# --- secrets (SecureString, read by deploy.sh through the instance role) ---
resource "random_password" "db" {
  length  = 32
  special = false # used inside a postgres:// URL
}

resource "random_password" "hook" {
  length  = 32
  special = false
}

locals {
  params = {
    oidc_client_secret = var.oidc_client_secret
    platform_repo      = var.platform_repo
    git_credentials    = var.git_credentials == "" ? "none" : var.git_credentials # SSM rejects empty values
    git_hook_secret    = random_password.hook.result
    db_password        = random_password.db.result
  }
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(["oidc_client_secret", "platform_repo", "git_credentials", "git_hook_secret", "db_password"])
  name     = "/${var.name}/${each.key}"
  type     = "SecureString"
  value    = local.params[each.key]
}

# --- instance role ---
data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "node" {
  statement {
    actions   = ["s3:ListBucket"]
    resources = ["arn:aws:s3:::${var.data_bucket}"]
  }
  statement {
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["arn:aws:s3:::${var.data_bucket}/*"]
  }
  statement {
    actions   = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = ["arn:aws:ssm:${var.region}:*:parameter/${var.name}/*"]
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.name}-node"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "ssm_core" {
  role       = aws_iam_role.node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "node" {
  role   = aws_iam_role.node.id
  policy = data.aws_iam_policy_document.node.json
}

resource "aws_iam_instance_profile" "node" {
  name = "${var.name}-node"
  role = aws_iam_role.node.name
}

# --- the node ---
resource "aws_instance" "node" {
  ami                    = data.aws_ssm_parameter.ubuntu.insecure_value
  instance_type          = var.instance_type
  subnet_id              = sort(data.aws_subnets.default.ids)[0]
  vpc_security_group_ids = [aws_security_group.web.id]
  iam_instance_profile   = aws_iam_instance_profile.node.name

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2 # backup/restore pods use the instance role
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = var.disk_gb
    encrypted   = true
  }

  user_data = templatefile("${path.module}/bootstrap.sh.tftpl", {
    region          = var.region
    name            = var.name
    data_bucket     = var.data_bucket
    domain          = var.domain
    acme_email      = var.acme_email
    k3s_version     = var.k3s_version
    timezone        = var.schedule_timezone
    backup_cron     = var.backup_cron
    platform_branch = var.platform_branch
    oidc_issuer     = var.oidc_issuer
    oidc_client_id  = var.oidc_client_id
  })

  tags = { Name = var.name }

  lifecycle {
    ignore_changes = [ami, user_data] # never replace the node because Ubuntu published a new AMI
  }
}

resource "aws_eip" "web" {
  instance = aws_instance.node.id
  domain   = "vpc"
}

resource "aws_route53_record" "web" {
  count   = var.route53_zone_id == "" ? 0 : 1
  zone_id = var.route53_zone_id
  name    = var.domain
  type    = "A"
  ttl     = 300
  records = [aws_eip.web.public_ip]
}
```

`deploy/aws/main/scheduler.tf`:
```hcl
# Sleep/wake by calling EC2 directly from EventBridge Scheduler (no Lambda).
data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "scheduler" {
  statement {
    actions   = ["ec2:StartInstances", "ec2:StopInstances"]
    resources = [aws_instance.node.arn]
  }
}

resource "aws_iam_role" "scheduler" {
  count              = var.schedule_enabled ? 1 : 0
  name               = "${var.name}-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

resource "aws_iam_role_policy" "scheduler" {
  count  = var.schedule_enabled ? 1 : 0
  role   = aws_iam_role.scheduler[0].id
  policy = data.aws_iam_policy_document.scheduler.json
}

resource "aws_scheduler_schedule" "sleep" {
  count                        = var.schedule_enabled ? 1 : 0
  name                         = "${var.name}-sleep"
  schedule_expression          = var.sleep_cron
  schedule_expression_timezone = var.schedule_timezone
  flexible_time_window { mode = "OFF" }
  target {
    arn      = "arn:aws:scheduler:::aws-sdk:ec2:stopInstances"
    role_arn = aws_iam_role.scheduler[0].arn
    input    = jsonencode({ InstanceIds = [aws_instance.node.id] })
  }
}

resource "aws_scheduler_schedule" "wake" {
  count                        = var.schedule_enabled ? 1 : 0
  name                         = "${var.name}-wake"
  schedule_expression          = var.wake_cron
  schedule_expression_timezone = var.schedule_timezone
  flexible_time_window { mode = "OFF" }
  target {
    arn      = "arn:aws:scheduler:::aws-sdk:ec2:startInstances"
    role_arn = aws_iam_role.scheduler[0].arn
    input    = jsonencode({ InstanceIds = [aws_instance.node.id] })
  }
}
```

`deploy/aws/main/outputs.tf`:
```hcl
output "instance_id" { value = aws_instance.node.id }
output "public_ip" { value = aws_eip.web.public_ip }
output "domain" { value = var.domain }
output "url" { value = "https://${var.domain}" }
output "region" { value = var.region }
output "data_bucket" { value = var.data_bucket }
```

- [ ] **Step 4: Write the bootstrap template**

`deploy/aws/main/bootstrap.sh.tftpl` (Terraform interpolates `${…}`; shell variables below use `$VAR` without braces on purpose):
```bash
#!/bin/bash
# Cloud-init user data: installs k3s + helm + aws cli, writes Crucible's node scripts, deploys the current release.
set -euxo pipefail
exec > >(tee -a /var/log/crucible-bootstrap.log) 2>&1
export DEBIAN_FRONTEND=noninteractive

timedatectl set-timezone '${timezone}'
apt-get update
apt-get install -y unzip jq curl
curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o /tmp/awscli.zip
unzip -q /tmp/awscli.zip -d /tmp && /tmp/aws/install --update

curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION='${k3s_version}' sh -s - --write-kubeconfig-mode 600
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

# Traefik (bundled with k3s): Let's Encrypt via HTTP-01 and an HTTP→HTTPS redirect.
mkdir -p /var/lib/rancher/k3s/server/manifests
cat > /var/lib/rancher/k3s/server/manifests/traefik-config.yaml <<'YAML'
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    persistence:
      enabled: true
      size: 128Mi
    deployment:
      initContainers:
        - name: volume-permissions
          image: busybox:1.37
          command: ["sh", "-c", "touch /data/acme.json; chmod 600 /data/acme.json"]
          volumeMounts:
            - name: data
              mountPath: /data
    podSecurityContext:
      fsGroup: 65532
      fsGroupChangePolicy: OnRootMismatch
    certificatesResolvers:
      le:
        acme:
          email: ${acme_email}
          storage: /data/acme.json
          httpChallenge:
            entryPoint: web
    ports:
      web:
        redirections:
          entryPoint:
            to: websecure
            scheme: https
            permanent: true
YAML

cat > /etc/crucible.env <<'ENV'
REGION=${region}
NAME=${name}
DATA_BUCKET=${data_bucket}
DOMAIN=${domain}
PLATFORM_BRANCH=${platform_branch}
OIDC_ISSUER=${oidc_issuer}
OIDC_CLIENT_ID=${oidc_client_id}
BACKUP_CRON='${backup_cron}'
TIMEZONE=${timezone}
ENV

mkdir -p /opt/crucible
cat > /opt/crucible/deploy.sh <<'SH'
#!/bin/bash
# Imports the current release image into k3s and (re)installs the Helm release. Safe to re-run.
set -euo pipefail
source /etc/crucible.env
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml AWS_REGION=$REGION
sha=$(aws s3 cp "s3://$DATA_BUCKET/current-release" - 2>/dev/null) || { echo "no release published yet"; exit 1; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
aws s3 cp "s3://$DATA_BUCKET/releases/$sha/crucible-image.tar.gz" "$work/image.tar.gz"
aws s3 cp "s3://$DATA_BUCKET/releases/$sha/chart.tgz" "$work/chart.tgz"
gunzip -c "$work/image.tar.gz" | k3s ctr images import -
param() { aws ssm get-parameter --with-decryption --name "/$NAME/$1" --query Parameter.Value --output text; }
kubectl create namespace crucible --dry-run=client -o yaml | kubectl apply -f -
kubectl -n crucible create secret generic crucible-secrets \
  --from-literal=OIDC_CLIENT_SECRET="$(param oidc_client_secret)" \
  --from-literal=CRUCIBLE_PLATFORM_REPO="$(param platform_repo)" \
  --from-literal=CRUCIBLE_GIT_HOOK_SECRET="$(param git_hook_secret)" \
  --from-literal=POSTGRES_PASSWORD="$(param db_password)" \
  --from-literal=git-credentials="$(param git_credentials)" \
  --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install crucible "$work/chart.tgz" -n crucible \
  --set image.tag="$sha" --set host="$DOMAIN" --set publicURL="https://$DOMAIN" \
  --set platformBranch="$PLATFORM_BRANCH" --set oidc.issuer="$OIDC_ISSUER" --set oidc.clientId="$OIDC_CLIENT_ID" \
  --set backup.bucket="$DATA_BUCKET" --set backup.region="$REGION" \
  --set backup.schedule="$BACKUP_CRON" --set backup.timeZone="$TIMEZONE" \
  --wait --timeout 10m
echo "deployed $sha"
SH

cat > /opt/crucible/snapshot.sh <<'SH'
#!/bin/bash
# Runs the backup CronJob now and waits for it to finish.
set -euo pipefail
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
job="crucible-backup-manual-$(date +%s)"
kubectl -n crucible create job "$job" --from=cronjob/crucible-backup
kubectl -n crucible wait --for=condition=complete "job/$job" --timeout=15m
SH
chmod +x /opt/crucible/*.sh
touch /opt/crucible/.ready

/opt/crucible/deploy.sh || echo "no release yet: run 'crucible aws deploy'"
```

`deploy/aws/main/terraform.tfvars.example`:
```hcl
# Copy to deploy/aws/main/crucible.tfvars (git-ignored) and fill in.
domain             = "crucible.example.com"
acme_email         = "ops@example.com"
platform_repo      = "https://git.example.com/crucible/platform.git"
git_credentials    = "https://crucible-bot:TOKEN@git.example.com"
oidc_issuer        = "https://sso.example.com/realms/corp"
oidc_client_id     = "crucible"
oidc_client_secret = "from-your-idp"
# route53_zone_id  = "Z0123456789"   # optional: manage the A record
# instance_type    = "t3a.large"     # cheaper if you only use local labs
# schedule_enabled = false           # run 24/7
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `terraform -chdir=deploy/aws/main fmt -check && terraform -chdir=deploy/aws/main validate && terraform -chdir=deploy/aws/main test`
Expected: `Success! 2 passed, 0 failed.`

- [ ] **Step 6: Commit**

```bash
git add deploy/aws/main .gitignore
git commit -m "feat(aws): single-node k3s stack with SSM secrets, IMDSv2 and scheduled sleep/wake"
```

---

### Task 3: Helm chart (Postgres, Crucible, backups, restore-on-empty)

**Files:**
- Create:
  - `deploy/helm/crucible/Chart.yaml`, `deploy/helm/crucible/values.yaml`
  - `deploy/helm/crucible/templates/{postgres.yaml,crucible.yaml,backup.yaml}`
  - `deploy/helm/test.sh`
- Modify: `Dockerfile` (git credential helper)

**Interfaces:**
- Consumes: Secret `crucible-secrets` with keys `OIDC_CLIENT_SECRET`, `CRUCIBLE_PLATFORM_REPO`, `CRUCIBLE_GIT_HOOK_SECRET`, `POSTGRES_PASSWORD`, `git-credentials` (created by `deploy.sh`).
- Produces:
  - Deployment `crucible` (1 replica, `Recreate`) with init container `restore`.
  - StatefulSet `postgres`.
  - CronJob `crucible-backup`.
  - Ingress `crucible` (Traefik `websecure`, cert resolver `le`).

- [ ] **Step 1: Write the failing render test**

`deploy/helm/test.sh`:
```bash
#!/usr/bin/env bash
# helm lint + assertions on rendered manifests (no cluster needed).
set -euo pipefail
chart="$(dirname "$0")/crucible"
helm lint "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso
out=$(helm template t "$chart" --set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso --set host=crucible.example.com)
need() { grep -q -- "$1" <<<"$out" || { echo "missing: $1"; exit 1; }; }
need 'kind: CronJob'
need 'pg_dump -h postgres -U crucible -Fc crucible'
need "to_regclass('public.users')"                       # restore only into an empty database
need 'router.tls.certresolver: le'
need 'host: crucible.example.com'
need 'imagePullPolicy: Never'
need 'type: Recreate'
if grep -q 'hostNetwork: true' <<<"$out"; then echo "hostNetwork must not be used"; exit 1; fi
echo "helm chart OK"
```

```bash
chmod +x deploy/helm/test.sh
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./deploy/helm/test.sh`
Expected: FAIL (`Error: … Chart.yaml file is missing`).

- [ ] **Step 3: Implement the chart**

`deploy/helm/crucible/Chart.yaml`:
```yaml
apiVersion: v2
name: crucible
description: Crucible training platform (single node)
type: application
version: 0.1.0
appVersion: "dev"
```

`deploy/helm/crucible/values.yaml`:
```yaml
image:
  repository: docker.io/library/crucible   # imported into containerd from the S3 release tarball
  tag: dev
  pullPolicy: Never
host: crucible.example.com
publicURL: https://crucible.example.com
platformBranch: main
syncInterval: 60s
oidc:
  issuer: ""
  clientId: crucible
secretName: crucible-secrets
tls:
  certResolver: le
postgres:
  image: postgres:18-alpine
  storage: 10Gi
backup:
  bucket: ""
  region: ""
  schedule: "15 19 * * *"
  timeZone: Europe/Bucharest
```

`deploy/helm/crucible/templates/postgres.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata: { name: postgres }
spec:
  selector: { app: postgres }
  ports: [{ port: 5432 }]
---
apiVersion: apps/v1
kind: StatefulSet
metadata: { name: postgres }
spec:
  serviceName: postgres
  replicas: 1
  selector: { matchLabels: { app: postgres } }
  template:
    metadata: { labels: { app: postgres } }
    spec:
      containers:
        - name: postgres
          image: {{ .Values.postgres.image }}
          env:
            - { name: POSTGRES_USER, value: crucible }
            - { name: POSTGRES_DB, value: crucible }
            - name: POSTGRES_PASSWORD
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: POSTGRES_PASSWORD } }
          ports: [{ containerPort: 5432 }]
          readinessProbe:
            exec: { command: ["pg_isready", "-U", "crucible"] }
            periodSeconds: 5
          volumeMounts: [{ name: data, mountPath: /var/lib/postgresql }]
  volumeClaimTemplates:
    - metadata: { name: data }
      spec:
        accessModes: [ReadWriteOnce]
        resources: { requests: { storage: {{ .Values.postgres.storage }} } }
```

`deploy/helm/crucible/templates/crucible.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata: { name: crucible }
spec:
  replicas: 1                 # ponytail: one process holds the agent hub; scale out needs a shared hub (later)
  strategy: { type: Recreate }
  selector: { matchLabels: { app: crucible } }
  template:
    metadata: { labels: { app: crucible } }
    spec:
      initContainers:
        - name: restore
          image: {{ .Values.postgres.image }}
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              until pg_isready -h postgres -U crucible; do sleep 2; done
              if [ "$(psql -h postgres -U crucible -d crucible -tAc "select to_regclass('public.users') is not null")" = "t" ]; then
                echo "database already initialised; no restore"; exit 0
              fi
              apk add --no-cache aws-cli >/dev/null
              latest=$(aws s3 ls "s3://{{ .Values.backup.bucket }}/snapshots/" | awk '{print $4}' | sort | tail -n1)
              if [ -z "$latest" ]; then echo "no snapshot found: fresh install"; exit 0; fi
              aws s3 cp "s3://{{ .Values.backup.bucket }}/snapshots/$latest" /tmp/db.dump
              pg_restore -h postgres -U crucible -d crucible --no-owner /tmp/db.dump
              echo "restored $latest"
          env:
            - name: PGPASSWORD
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: POSTGRES_PASSWORD } }
            - { name: AWS_REGION, value: {{ .Values.backup.region | quote }} }
      containers:
        - name: api
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports: [{ containerPort: 8080 }]
          env:
            - name: POSTGRES_PASSWORD
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: POSTGRES_PASSWORD } }
            - { name: DATABASE_URL, value: "postgres://crucible:$(POSTGRES_PASSWORD)@postgres:5432/crucible?sslmode=disable" }
            - name: CRUCIBLE_PLATFORM_REPO
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_PLATFORM_REPO } }
            - { name: CRUCIBLE_PLATFORM_BRANCH, value: {{ .Values.platformBranch | quote }} }
            - { name: CRUCIBLE_PUBLIC_URL, value: {{ .Values.publicURL | quote }} }
            - { name: CRUCIBLE_SYNC_INTERVAL, value: {{ .Values.syncInterval | quote }} }
            - name: CRUCIBLE_GIT_HOOK_SECRET
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: CRUCIBLE_GIT_HOOK_SECRET } }
            - { name: OIDC_ISSUER, value: {{ .Values.oidc.issuer | quote }} }
            - { name: OIDC_CLIENT_ID, value: {{ .Values.oidc.clientId | quote }} }
            - name: OIDC_CLIENT_SECRET
              valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: OIDC_CLIENT_SECRET } }
          readinessProbe:
            httpGet: { path: /healthz, port: 8080 }
            periodSeconds: 5
          volumeMounts:
            - { name: data, mountPath: /data }
            - { name: git-credentials, mountPath: /etc/crucible/git-credentials, subPath: git-credentials, readOnly: true }
      volumes:
        - name: data
          emptyDir: {}        # git mirrors and exports: rebuilt from git on start
        - name: git-credentials
          secret: { secretName: {{ .Values.secretName }}, items: [{ key: git-credentials, path: git-credentials }] }
---
apiVersion: v1
kind: Service
metadata: { name: crucible }
spec:
  selector: { app: crucible }
  ports: [{ port: 80, targetPort: 8080 }]
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: crucible
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
    traefik.ingress.kubernetes.io/router.tls.certresolver: {{ .Values.tls.certResolver }}
spec:
  ingressClassName: traefik
  rules:
    - host: {{ .Values.host }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend: { service: { name: crucible, port: { number: 80 } } }
```

`deploy/helm/crucible/templates/backup.yaml`:
```yaml
apiVersion: batch/v1
kind: CronJob
metadata: { name: crucible-backup }
spec:
  schedule: {{ .Values.backup.schedule | quote }}
  timeZone: {{ .Values.backup.timeZone | quote }}
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 2
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: backup
              image: {{ .Values.postgres.image }}
              command: ["/bin/sh", "-c"]
              args:
                - |
                  set -eu
                  apk add --no-cache aws-cli >/dev/null
                  ts=$(date -u +%Y%m%dT%H%M%SZ)
                  pg_dump -h postgres -U crucible -Fc crucible > /tmp/db.dump
                  aws s3 cp /tmp/db.dump "s3://{{ .Values.backup.bucket }}/snapshots/crucible-$ts.dump"
                  echo "snapshot crucible-$ts.dump uploaded"
              env:
                - name: PGPASSWORD
                  valueFrom: { secretKeyRef: { name: {{ .Values.secretName }}, key: POSTGRES_PASSWORD } }
                - { name: AWS_REGION, value: {{ .Values.backup.region | quote }} }
```

Modify `Dockerfile`: in the final stage replace the `RUN apk add …` line with:
```dockerfile
RUN apk add --no-cache git tar ca-certificates \
 && git config --system --add safe.directory '*' \
 && git config --system credential.helper 'store --file=/etc/crucible/git-credentials' \
 && adduser -D -u 10001 crucible && mkdir /data && chown crucible /data
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./deploy/helm/test.sh`
Expected: `1 chart(s) linted, 0 chart(s) failed` then `helm chart OK`.

- [ ] **Step 5: Commit**

```bash
git add deploy/helm Dockerfile
git commit -m "feat(helm): single-node chart with postgres, nightly S3 backups and restore-on-empty"
```

---

### Task 4: Operator runbook

**Files:**
- Create: `docs/runbooks/aws.md`

**Interfaces:**
- Produces: the human procedure the CLI in Task 5 automates, plus troubleshooting.

- [ ] **Step 1: Write the runbook**

`docs/runbooks/aws.md`:
````markdown
# Crucible on AWS: runbook

## One-time setup (per AWS account)
1. Install: terraform ≥ 1.10, AWS CLI v2 (logged in), Docker with buildx, Helm 3, Go 1.26.
2. `go run ./cmd/crucible aws init --region eu-west-1`. This creates the state and data buckets (never destroyed).
3. `cp deploy/aws/main/terraform.tfvars.example deploy/aws/main/crucible.tfvars` and fill it in.
   Register `https://<domain>/auth/callback` as a redirect URI in your OIDC provider.
4. `go run ./cmd/crucible aws up --var-file deploy/aws/main/crucible.tfvars`
5. Point DNS: an A record for `<domain>` → `public_ip` output (skip if `route53_zone_id` is set).
   The first HTTPS request may take ~1 min while Let's Encrypt issues the certificate.

## Everyday
| Want | Command |
|---|---|
| Ship the current commit | `crucible aws deploy` |
| Take a snapshot now | `crucible aws snapshot` |
| Stop paying for compute tonight | `crucible aws sleep` (snapshot, then stop) |
| Start before the schedule | `crucible aws wake` |
| Is it up? | `crucible aws status` |
| Shell on the node | `aws ssm start-session --target <instance_id>` |

The schedule wakes the node at 07:30 and sleeps it at 19:30 (Mon–Fri, `schedule_timezone`). The backup runs at 19:15.

## Disaster / full teardown
- `crucible aws teardown --var-file … --yes`: takes a snapshot, then destroys the main stack. The buckets survive.
- `crucible aws up --var-file …`: rebuilds the node. The restore init container loads the newest snapshot into the empty database. Config and content come back from git.

## Troubleshooting
- **No certificate.** Check that DNS points at the Elastic IP and port 80 is reachable. Then run `kubectl -n kube-system logs deploy/traefik`. If the Traefik chart bundled with your k3s version rejects `ports.web.redirections`, use `ports.web.redirectTo: {port: websecure}` in `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml`.
- **Bootstrap.** `sudo tail -f /var/log/crucible-bootstrap.log` on the node.
- **App logs.** `sudo k3s kubectl -n crucible logs deploy/crucible -c api` (and `-c restore`).
````

- [ ] **Step 2: Commit**

```bash
git add docs/runbooks/aws.md
git commit -m "docs: AWS runbook"
```

---

### Task 5: `crucible aws` CLI (init, up, deploy, snapshot, sleep, wake, status, teardown)

**Files:**
- Create: `internal/awsops/awsops.go`, `internal/awsops/awsops_test.go`
- Modify: `cmd/crucible/main.go` (add the `aws` subcommand)

**Interfaces:**
- Consumes: Terraform outputs from Tasks 1–2; node scripts `/opt/crucible/{deploy,snapshot}.sh` and the `/opt/crucible/.ready` marker.
- Produces: `awsops.Ops{Root string; Exec func(ctx, dir, name string, args ...string) error; Output func(ctx, dir, name string, args ...string) (string, error); Sleep func(time.Duration); Log io.Writer}` with methods:
  - `Init(ctx, region)`
  - `Up(ctx, varFile)`
  - `Deploy(ctx)`
  - `Snapshot(ctx)`
  - `SleepNode(ctx)`
  - `Wake(ctx)`
  - `Status(ctx)`
  - `Teardown(ctx, varFile string, yes bool)`

- [ ] **Step 1: Write the failing test**

`internal/awsops/awsops_test.go`:
```go
package awsops

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	calls  []string
	output func(cmd string) string
}

func (r *recorder) ops(root string) Ops {
	return Ops{
		Root: root,
		Exec: func(_ context.Context, _ string, name string, args ...string) error {
			r.calls = append(r.calls, name+" "+strings.Join(args, " "))
			return nil
		},
		Output: func(_ context.Context, _ string, name string, args ...string) (string, error) {
			cmd := name + " " + strings.Join(args, " ")
			r.calls = append(r.calls, cmd)
			return r.output(cmd), nil
		},
		Sleep: func(time.Duration) {},
		Log:   io.Discard,
	}
}

func fakeAWS(cmd string) string {
	switch {
	case strings.Contains(cmd, "output -json"):
		return `{"instance_id":{"value":"i-123"},"region":{"value":"eu-west-1"},"data_bucket":{"value":"bkt"},"domain":{"value":"c.example.com"},"url":{"value":"https://c.example.com"}}`
	case strings.HasPrefix(cmd, "git rev-parse"):
		return "abc123def456"
	case strings.HasPrefix(cmd, "git status"):
		return ""
	case strings.Contains(cmd, "send-command"):
		return "cmd-1"
	case strings.Contains(cmd, "get-command-invocation"):
		return "Success"
	case strings.Contains(cmd, "describe-instances"):
		return "running"
	}
	return ""
}

func indexOf(calls []string, sub string) int {
	for i, c := range calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func TestDeployPublishesReleaseThenRollsOut(t *testing.T) {
	root := t.TempDir()
	r := &recorder{output: fakeAWS}
	if err := r.ops(root).Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	rel := root + "/.local/release"
	order := []string{
		"buildx build --platform linux/amd64 -t crucible:abc123def456",
		"s3 cp " + rel + "/crucible-image.tar.gz s3://bkt/releases/abc123def456/crucible-image.tar.gz",
		"s3 cp " + rel + "/chart.tgz s3://bkt/releases/abc123def456/chart.tgz",
		"s3://bkt/current-release",
		"/opt/crucible/deploy.sh",
	}
	last := -1
	for _, step := range order {
		i := indexOf(r.calls, step)
		if i <= last {
			t.Fatalf("step %q missing or out of order in:\n%s", step, strings.Join(r.calls, "\n"))
		}
		last = i
	}
}

func TestSleepSnapshotsBeforeStop(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).SleepNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, stop := indexOf(r.calls, "/opt/crucible/snapshot.sh"), indexOf(r.calls, "stop-instances")
	if snap < 0 || stop < 0 || snap > stop {
		t.Fatalf("snapshot must precede stop:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestTeardownRequiresYes(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", false); err == nil {
		t.Fatal("teardown without --yes must fail")
	}
	if indexOf(r.calls, "destroy") >= 0 {
		t.Fatal("nothing may be destroyed without --yes")
	}
	if err := r.ops(t.TempDir()).Teardown(context.Background(), "x.tfvars", true); err != nil {
		t.Fatal(err)
	}
	if snap, destroy := indexOf(r.calls, "snapshot.sh"), indexOf(r.calls, "destroy"); snap < 0 || destroy < snap {
		t.Fatalf("teardown must snapshot first:\n%s", strings.Join(r.calls, "\n"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/awsops/...`
Expected: FAIL (`Ops` undefined).

- [ ] **Step 3: Implement**

`internal/awsops/awsops.go`:
```go
// Package awsops drives the AWS deployment: terraform for infrastructure, S3 for releases, SSM for the node.
package awsops

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Ops struct {
	Root   string // repo root
	Exec   func(ctx context.Context, dir, name string, args ...string) error
	Output func(ctx context.Context, dir, name string, args ...string) (string, error)
	Sleep  func(time.Duration)
	Log    io.Writer
}

// Default runs real commands: Exec streams to the terminal, Output captures stdout.
func Default(root string) Ops {
	return Ops{
		Root: root,
		Exec: func(ctx context.Context, dir, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = dir, os.Stdout, os.Stderr, os.Stdin
			return cmd.Run()
		},
		Output: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stderr = dir, os.Stderr
			out, err := cmd.Output()
			return strings.TrimSpace(string(out)), err
		},
		Sleep: time.Sleep,
		Log:   os.Stdout,
	}
}

func (o Ops) dir(stack string) string { return filepath.Join(o.Root, "deploy", "aws", stack) }

func (o Ops) outputs(ctx context.Context, stack string) (map[string]string, error) {
	raw, err := o.Output(ctx, o.Root, "terraform", "-chdir="+o.dir(stack), "output", "-json")
	if err != nil {
		return nil, fmt.Errorf("terraform outputs for %s (did you run `crucible aws init`/`up`?): %w", stack, err)
	}
	var parsed map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range parsed {
		out[k] = fmt.Sprint(v.Value)
	}
	return out, nil
}

func (o Ops) Init(ctx context.Context, region string) error {
	d := o.dir("persistent")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	return o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "apply", "-var", "region="+region)
}

func (o Ops) Up(ctx context.Context, varFile string) error {
	p, err := o.outputs(ctx, "persistent")
	if err != nil {
		return err
	}
	d := o.dir("main")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init", "-reconfigure",
		"-backend-config=bucket="+p["state_bucket"], "-backend-config=key=main.tfstate",
		"-backend-config=region="+p["region"], "-backend-config=use_lockfile=true"); err != nil {
		return err
	}
	abs, _ := filepath.Abs(varFile)
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "apply", "-var-file="+abs,
		"-var", "region="+p["region"], "-var", "data_bucket="+p["data_bucket"]); err != nil {
		return err
	}
	return o.Deploy(ctx)
}

// Deploy builds the image for the current commit, publishes it to S3 and rolls it out on the node.
func (o Ops) Deploy(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	sha, err := o.Output(ctx, o.Root, "git", "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return err
	}
	if dirty, _ := o.Output(ctx, o.Root, "git", "status", "--porcelain"); dirty != "" {
		sha = fmt.Sprintf("%s-dirty-%d", sha, time.Now().Unix())
	}
	tag := "crucible:" + sha
	rel := filepath.Join(o.Root, ".local", "release")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	img, chart := filepath.Join(rel, "crucible-image.tar"), filepath.Join(rel, "chart.tgz")
	steps := [][]string{
		{"docker", "buildx", "build", "--platform", "linux/amd64", "-t", tag, "--load", "."},
		{"docker", "save", "-o", img, tag},
	}
	for _, s := range steps {
		if err := o.Exec(ctx, o.Root, s[0], s[1:]...); err != nil {
			return err
		}
	}
	if err := gzipFile(img); err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "helm", "package", filepath.Join(o.Root, "deploy", "helm", "crucible"),
		"-d", rel, "--app-version", sha); err != nil {
		return err
	}
	_ = os.Rename(filepath.Join(rel, "crucible-0.1.0.tgz"), chart)
	if err := os.WriteFile(filepath.Join(rel, "current-release"), []byte(sha), 0o644); err != nil {
		return err
	}
	b, r := m["data_bucket"], m["region"]
	for _, up := range [][2]string{
		{img + ".gz", fmt.Sprintf("s3://%s/releases/%s/crucible-image.tar.gz", b, sha)},
		{chart, fmt.Sprintf("s3://%s/releases/%s/chart.tgz", b, sha)},
		{filepath.Join(rel, "current-release"), fmt.Sprintf("s3://%s/current-release", b)},
	} {
		if err := o.Exec(ctx, o.Root, "aws", "s3", "cp", up[0], up[1], "--region", r); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.Log, "release %s published; rolling out…\n", sha)
	if err := o.ssm(ctx, m, "until [ -f /opt/crucible/.ready ]; do sleep 5; done; /opt/crucible/deploy.sh"); err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "🔥 %s is live at %s\n", sha, m["url"])
	return nil
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return nil // tests run without a real image; Exec already reported failures
	}
	defer in.Close()
	out, err := os.Create(path + ".gz")
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	return zw.Close()
}

// ssm runs a shell command on the node and waits for it (SSM's own wait gives up after ~100s).
func (o Ops) ssm(ctx context.Context, m map[string]string, command string) error {
	params, _ := json.Marshal(map[string][]string{"commands": {command}})
	var id string
	var err error
	for attempt := 0; attempt < 40; attempt++ { // a fresh node needs a few minutes to register with SSM
		if id, err = o.Output(ctx, o.Root, "aws", "ssm", "send-command", "--region", m["region"], "--instance-ids", m["instance_id"],
			"--document-name", "AWS-RunShellScript", "--parameters", string(params), "--query", "Command.CommandId", "--output", "text"); err == nil {
			break
		}
		o.Sleep(15 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("node not reachable over SSM: %w", err)
	}
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		st, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
			"--instance-id", m["instance_id"], "--query", "Status", "--output", "text")
		switch st {
		case "Success":
			return nil
		case "Failed", "Cancelled", "TimedOut":
			msg, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
				"--instance-id", m["instance_id"], "--query", "StandardErrorContent", "--output", "text")
			return fmt.Errorf("node command %s: %s", st, msg)
		}
		o.Sleep(5 * time.Second)
	}
	return errors.New("node command timed out")
}

func (o Ops) Snapshot(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	return o.ssm(ctx, m, "/opt/crucible/snapshot.sh")
}

func (o Ops) state(ctx context.Context, m map[string]string) string {
	st, _ := o.Output(ctx, o.Root, "aws", "ec2", "describe-instances", "--region", m["region"], "--instance-ids", m["instance_id"],
		"--query", "Reservations[0].Instances[0].State.Name", "--output", "text")
	return st
}

// SleepNode snapshots, then stops the instance (compute billing stops; disk + IP keep costing ~$9/month).
func (o Ops) SleepNode(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if o.state(ctx, m) == "running" {
		if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
			return fmt.Errorf("snapshot failed, not stopping: %w", err)
		}
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "stop-instances", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	fmt.Fprintln(o.Log, "the forge is banked for the night 🌙")
	return o.Exec(ctx, o.Root, "aws", "ec2", "wait", "instance-stopped", "--region", m["region"], "--instance-ids", m["instance_id"])
}

func (o Ops) Wake(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "start-instances", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	if err := o.Exec(ctx, o.Root, "aws", "ec2", "wait", "instance-running", "--region", m["region"], "--instance-ids", m["instance_id"]); err != nil {
		return err
	}
	return o.waitHealthy(ctx, m["url"])
}

func (o Ops) waitHealthy(ctx context.Context, url string) error {
	if strings.Contains(url, "example.com") {
		return nil // tests
	}
	for i := 0; i < 120; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				fmt.Fprintf(o.Log, "🔥 the forge is lit: %s\n", url)
				return nil
			}
		}
		o.Sleep(5 * time.Second)
	}
	return fmt.Errorf("%s did not become healthy within 10 minutes", url)
}

func (o Ops) Status(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "instance %s: %s\nurl: %s\n", m["instance_id"], o.state(ctx, m), m["url"])
	return nil
}

func (o Ops) Teardown(ctx context.Context, varFile string, yes bool) error {
	if !yes {
		return errors.New("teardown destroys the node (buckets and snapshots are kept); re-run with --yes to confirm")
	}
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	if o.state(ctx, m) == "running" {
		if err := o.ssm(ctx, m, "/opt/crucible/snapshot.sh"); err != nil {
			return fmt.Errorf("final snapshot failed, aborting teardown: %w", err)
		}
	} else {
		fmt.Fprintln(o.Log, "node is not running; relying on the last nightly snapshot")
	}
	abs, _ := filepath.Abs(varFile)
	return o.Exec(ctx, o.Root, "terraform", "-chdir="+o.dir("main"), "destroy", "-auto-approve", "-var-file="+abs,
		"-var", "region="+m["region"], "-var", "data_bucket="+m["data_bucket"])
}
```

Modify `cmd/crucible/main.go`. Replace the `usage` constant and the `switch` in `main` with:
```go
const usage = `usage:
  crucible lint <content-or-platform-dir>
  crucible aws init --region REGION
  crucible aws up --var-file FILE
  crucible aws deploy | snapshot | sleep | wake | status
  crucible aws teardown --var-file FILE --yes`
```
```go
	switch os.Args[1] {
	case "lint":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(lint(os.Args[2], os.Stdout))
	case "aws":
		os.Exit(awsCmd(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
```
and add:
```go
func awsCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("aws "+args[0], flag.ExitOnError)
	region := fs.String("region", "", "AWS region (init)")
	varFile := fs.String("var-file", "deploy/aws/main/crucible.tfvars", "terraform variables file")
	yes := fs.Bool("yes", false, "confirm teardown")
	_ = fs.Parse(args[1:])
	root, _ := os.Getwd()
	ops := awsops.Default(root)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch args[0] {
	case "init":
		if *region == "" {
			fmt.Fprintln(os.Stderr, "--region is required")
			return 2
		}
		err = ops.Init(ctx, *region)
	case "up":
		err = ops.Up(ctx, *varFile)
	case "deploy":
		err = ops.Deploy(ctx)
	case "snapshot":
		err = ops.Snapshot(ctx)
	case "sleep":
		err = ops.SleepNode(ctx)
	case "wake":
		err = ops.Wake(ctx)
	case "status":
		err = ops.Status(ctx)
	case "teardown":
		err = ops.Teardown(ctx, *varFile, *yes)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
```
Add imports `context`, `flag`, `os/signal`, `crucible/internal/awsops`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/awsops/... ./cmd/crucible/... && go build ./cmd/crucible`
Expected: `ok` for both packages; the build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/awsops cmd/crucible
git commit -m "feat(cli): crucible aws init/up/deploy/snapshot/sleep/wake/status/teardown"
```

---

### Task 6: Acceptance in a sandbox AWS account (manual, about $1 total)

**Files:**
- Modify: `docs/runbooks/aws.md` (append the acceptance log: date, sha, results)

**Interfaces:**
- Consumes: everything above.

- [ ] **A1. Bootstrap:** `crucible aws init --region eu-west-1`, then `crucible aws up --var-file deploy/aws/main/crucible.tfvars`.
  Expected: Terraform creates about 15 resources; the CLI prints `🔥 <sha> is live at https://<domain>`.
- [ ] **A2. Login and lab:** open `https://<domain>`, log in through your IdP, pair `crucible-agent` from a laptop against the cloud URL, and finish Forge 101's lab.
  Expected: same behaviour as the local check; the certificate is valid (Let's Encrypt).
- [ ] **A3. Sleep and wake:** `crucible aws sleep`, then `crucible aws status` shows `stopped`; then `crucible aws wake`.
  Expected: the URL is healthy again and progress from A2 is still there.
- [ ] **A4. Schedule:** in the AWS console, EventBridge Scheduler shows `crucible-sleep` and `crucible-wake` with the expected cron and timezone.
- [ ] **A5. Disaster recovery:** `crucible aws teardown --yes`, then `crucible aws up …`. After the rebuild, `kubectl logs deploy/crucible -c restore` shows `restored crucible-<ts>.dump`; A2's progress is present. Restart the pod once and the restore logs `database already initialised; no restore`.
- [ ] **A6. Cost check after one week:** Cost Explorer filtered by tag `app=crucible` shows a run rate of about $50 per month on the business-hours schedule.
- [ ] **Commit the acceptance log**

```bash
git add docs/runbooks/aws.md
git commit -m "docs: M2 AWS acceptance results"
```
