# Private Access over SSM Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `deploy/aws` deploy into our egress-only account (`access = "ssm"`: existing subnet, no Elastic IP, plain HTTP reached through a Session Manager port-forward at `http://localhost:18080`, labs in the same account) while the public install keeps working unchanged.

**Architecture:** One setting, `access`, chosen at `crucible aws init`, lives in the persistent stack, which computes the one public URL (Cognito, main stack, node and Helm all read it). The main stack and the node's boot/deploy scripts switch on it; Helm gains a no-TLS, any-host Ingress; the CLI checks health and egress on the node over SSM and gains `crucible aws connect`. Lab modules get `var.crucible_vpc_id` / `var.crucible_subnet_ids`, filled from new `labs-init` flags, because the account has no default VPC.

**Tech Stack:** Terraform 1.16 (mock-provider `terraform test`), Helm 3 templates (`deploy/helm/test.sh`), Go 1.26 (`internal/awsops`, `internal/content`, `internal/labs`, `cmd/crucible`, `cmd/crucible-api`), the AWS CLI and Session Manager plugin (called, never linked).

**Spec:** `docs/superpowers/specs/2026-10-30-private-access-over-ssm-design.md`

## Global Constraints

- **Never create real AWS resources.** Terraform only with mock providers (`terraform test`, `validate`); AWS code is tested with the `recorder` fake in `internal/awsops`. Real-AWS verification is the owner's by-hand step.
- Every shell that builds or tests: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH` (run from the repo root).
- **Commits:** only your own files, pathspec form: `git add <new files>` then `git commit -m "…" -- <paths>`; never `git add -A` or `commit -a`; never rewrite history or push. End every message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Tests first** for every behaviour change; each new test must fail before the change.
- `access` is exactly `"public"` or `"ssm"`. `public` is the default and behaves exactly as today.
- The `ssm` public URL is `http://localhost:<local_port>`; `local_port` defaults to `18080`, allowed 1024–65535.
- Our values (docs, examples, runbook only, never hard-coded in code): region `il-central-1`, VPC `vpc-03aa39198d92c5a25`, subnets `subnet-0fa66f46aa023ab6e` (spokeSubnet1), `subnet-08e721302b446603f` (spokeSubnet2).
- Copy (CLI messages, docs): plain, from the reader's side, short sentences; no em-dash asides.
- `gofmt -l cmd internal` prints nothing; `go vet ./...` passes.

## Review Focus

1. **Re-running `crucible aws init` without `--access` on an ssm install** must keep `ssm` (and its port), never flip back to public and break the Cognito callback. Pinned in Task 6 (`TestInitKeepsTheCurrentAccess`).
2. **An existing public install upgrading** must not lose its Elastic IP: `aws_eip.web` becomes `aws_eip.web[0]`, so a `moved` block is required, and a persistent stack made before `access` existed (no `access`/`public_url` outputs) must still `up` as public. Pinned in Task 2 (the `moved` block plus `public_access_is_unchanged`) and Task 6 (`TestUpOnAnOldPersistentStackStaysPublic`).
3. **`crucible aws connect` against a node made before this change** (no `crucible:public-url` tag) must say to run `up`, not forward to a wrong port. Pinned in Task 8 (`TestConnectNeedsTheTag`).
4. **`crucible aws status` on a stopped ssm node** must not sit in SSM's 10-minute registration retry. Pinned in Task 7 (`TestSSMModeStatusSkipsAStoppedNode`).
5. **A lab in a region other than the lab network's** must get an empty network, never another region's subnet, and an unconfigured network must be `[]`, not `null` (Terraform's `length()` fails on null). Pinned in Task 5 (`TestLabTFVarsCarryTheLabNetworkInItsRegion`).

## Before Task 1 (controller)

The working tree already holds uncommitted page updates describing our account (`docs/aws-deploy.html`, `docs/aws-primer.html`, `docs/deploy-architecture.html`, `docs/ssm-access.html`, `docs/test-guide.html`). Commit them as they are, so every task starts from a clean tree:

```bash
git commit -m "docs: our account (one account, no inbound path, SSM access, Cloudflare option)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs/aws-deploy.html docs/aws-primer.html docs/deploy-architecture.html docs/ssm-access.html docs/test-guide.html
```

## File Structure

| File | Responsibility | Tasks |
|---|---|---|
| `deploy/aws/persistent/main.tf`, `cognito.tf`, `persistent.tftest.hcl` | `access`, `local_port`, the one `public_url`; Cognito callback/logout/welcome email from it | 1 |
| `deploy/aws/main/variables.tf`, `main.tf`, `outputs.tf`, `bootstrap.sh.tftpl`, `main.tftest.hcl` | ssm mode: subnet, no EIP/DNS/inbound, node tag, env keys, Traefik and deploy.sh | 2, 4 |
| `deploy/helm/crucible/values.yaml`, `templates/crucible.yaml`, `deploy/helm/test.sh` | `tls.enabled`, empty `host`, URL/TLS check; lab network env | 3, 4 |
| `deploy/aws/labs/main.tf`, `labs.tftest.hcl` | `lab_vpc_id`, `lab_subnet_ids`, subnet check, outputs | 4 |
| `internal/content/awstf.go`, `internal/content/load_test.go` | `LabNetwork`, two more lab variables, `LabTFVars` fills them | 5 |
| `internal/labs/aws.go`, `bundle.go`, `aws_objects_test.go` | `AWSRunner.Network` passed into module and plan bundles | 5 |
| `cmd/crucible-api/main.go`, `main_test.go` | reads the lab network env | 5 |
| `docs/user/authors/aws-labs.md` | authors: the two variables and the portable form | 5 |
| `internal/awsops/awsops.go`, `awsops_test.go` | `InitOpts`, `mainVars`, `labVars`, `LabNet`; ssm-mode `up`/`deploy`/`wake`/`status`; `Connect` | 6, 7, 8 |
| `cmd/crucible/main.go` | flags and usage for init, labs-init, connect | 6, 8 |
| `docs/*.html`, `docs/runbooks/aws.md`, `CLAUDE.md` | the real steps replace "not made yet" | 9 |

---

### Task 1: Persistent stack: `access`, `local_port`, one public URL

**Files:**
- Modify: `deploy/aws/persistent/main.tf` (variables near line 15–23, outputs near line 121)
- Modify: `deploy/aws/persistent/cognito.tf` (invite message line 18, client lines 55–56)
- Test: `deploy/aws/persistent/persistent.tftest.hcl`

**Interfaces:**
- Produces: persistent outputs `access` (string), `local_port` (number), `domain` (string), `public_url` (string). Task 6 reads them via `terraform output -json` (numbers arrive as JSON text, e.g. `"18080"`).

- [ ] **Step 1: Write the failing tests.** Append to `deploy/aws/persistent/persistent.tftest.hcl`:

```hcl
run "public_access_is_unchanged" {
  command = plan

  assert {
    condition     = output.access == "public" && output.public_url == "https://crucible.example.com" && output.domain == "crucible.example.com"
    error_message = "public is the default and its URL is https://<domain>"
  }
}

run "ssm_access_uses_a_localhost_url" {
  command = plan
  variables {
    access = "ssm"
    domain = ""
  }

  assert {
    condition     = output.public_url == "http://localhost:18080" && output.local_port == 18080
    error_message = "ssm's URL is http://localhost:18080 by default"
  }
  assert {
    condition     = toset(aws_cognito_user_pool_client.crucible.callback_urls) == toset(["http://localhost:18080/auth/callback"]) && toset(aws_cognito_user_pool_client.crucible.logout_urls) == toset(["http://localhost:18080/"])
    error_message = "Cognito's callback and logout follow the public URL"
  }
  assert {
    condition     = strcontains(aws_cognito_user_pool.users.admin_create_user_config[0].invite_message_template[0].email_message, "http://localhost:18080") && strcontains(aws_cognito_user_pool.users.admin_create_user_config[0].invite_message_template[0].email_message, "crucible aws connect")
    error_message = "the invite names the localhost address and how to open the tunnel"
  }
}

run "ssm_access_ignores_a_domain" {
  command = plan
  variables {
    access     = "ssm"
    local_port = 18443
  }

  assert {
    condition     = output.public_url == "http://localhost:18443"
    error_message = "ssm ignores the domain and uses the chosen port"
  }
}

run "public_access_needs_a_domain" {
  command = plan
  variables {
    domain = ""
  }
  expect_failures = [var.domain]
}

run "local_port_must_be_a_user_port" {
  command = plan
  variables {
    access     = "ssm"
    local_port = 80
  }
  expect_failures = [var.local_port]
}

run "access_is_public_or_ssm" {
  command = plan
  variables {
    access = "vpn"
  }
  expect_failures = [var.access]
}
```

- [ ] **Step 2: Run the tests and see them fail.**

Run: `cd deploy/aws/persistent && terraform test`
Expected: FAIL: the new runs report unknown outputs / variables (`output.access`, `var.access`, `var.local_port` not declared).

- [ ] **Step 3: Implement.** In `deploy/aws/persistent/main.tf`, replace the `domain` variable with:

```hcl
variable "domain" {
  type        = string
  default     = ""
  description = "Crucible's public hostname (access = public); used for the Cognito callback URL"
  validation {
    condition     = var.access != "public" || var.domain != ""
    error_message = "access = public needs a domain: crucible aws init --domain crucible.example.com"
  }
}

variable "access" {
  type        = string
  default     = "public"
  description = "public: a domain, an Elastic IP and Let's Encrypt. ssm: no way in; people reach it with `crucible aws connect`"
  validation {
    condition     = contains(["public", "ssm"], var.access)
    error_message = "access must be public or ssm"
  }
}

variable "local_port" {
  type        = number
  default     = 18080
  description = "access = ssm: the laptop port everyone opens Crucible on (the public URL is http://localhost:<port>)"
  validation {
    condition     = var.local_port >= 1024 && var.local_port <= 65535 && floor(var.local_port) == var.local_port
    error_message = "local_port must be a whole number from 1024 to 65535"
  }
}

locals {
  # The one address people use. Cognito's callback, the main stack, the node and Helm all read it from here.
  public_url = var.access == "ssm" ? "http://localhost:${var.local_port}" : "https://${var.domain}"
}
```

Next to `output "region"` (near line 121) add:

```hcl
output "access" { value = var.access }
output "local_port" { value = var.local_port }
output "domain" { value = var.domain }
output "public_url" { value = local.public_url }
```

In `deploy/aws/persistent/cognito.tf`, change the invite message line to:

```hcl
      email_message = "Welcome to the forge. Sign in at ${local.public_url}${var.access == "ssm" ? " (open the tunnel first with `crucible aws connect`)" : ""} with {username} and the temporary password {####}. You'll choose your own password on first sign-in."
```

and the client's two URL lines to:

```hcl
  callback_urls                        = ["${local.public_url}/auth/callback"]
  logout_urls                          = ["${local.public_url}/"]
```

- [ ] **Step 4: Run the tests and see them pass.**

Run: `cd deploy/aws/persistent && terraform test`
Expected: PASS, every run including the existing `cognito_is_invite_only_and_protected` (its callback is still `https://crucible.example.com/auth/callback`).

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(aws): persistent stack chooses access (public | ssm) and computes the one public URL

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/persistent/main.tf deploy/aws/persistent/cognito.tf deploy/aws/persistent/persistent.tftest.hcl
```

---

### Task 2: Main stack and node scripts in `ssm` mode

**Files:**
- Modify: `deploy/aws/main/variables.tf` (`domain` and `acme_email` near lines 7–18; add `access`, `public_url`, `subnet_id`)
- Modify: `deploy/aws/main/main.tf` (data sources lines 1–12, security group lines 18–45, env parameter ~line 79, instance ~line 202, EIP and Route 53 lines 241–253)
- Modify: `deploy/aws/main/outputs.tf`
- Modify: `deploy/aws/main/bootstrap.sh.tftpl` (Traefik block lines 19–55, deploy.sh helm line ~148)
- Test: `deploy/aws/main/main.tftest.hcl`

**Interfaces:**
- Consumes: Task 1's persistent outputs `access`, `public_url`, passed by Task 6 as `-var access=…`, `-var public_url=…` (absent on old stacks).
- Produces: main outputs `access` (string), `url` (= the public URL), `public_ip` (`""` in ssm). Instance tag `crucible:public-url` (Task 8 reads it). `/<name>/env` keys `ACCESS`, `PUBLIC_URL`, `HOST`, `TLS_ENABLED` (Task 3's chart values via deploy.sh).

- [ ] **Step 1: Write the failing tests.** In `deploy/aws/main/main.tftest.hcl`, add inside the first `mock_provider "aws"` block:

```hcl
  mock_data "aws_subnet" {
    defaults = { vpc_id = "vpc-spoke" }
  }
```

and append these runs:

```hcl
run "public_access_is_unchanged" {
  command = plan

  assert {
    condition     = length(aws_eip.web) == 1 && output.url == "https://crucible.example.com" && output.access == "public"
    error_message = "public keeps its Elastic IP and https://<domain>"
  }
  assert {
    condition     = strcontains(aws_instance.node.user_data, "certificatesResolvers") && strcontains(aws_instance.node.user_data, "redirections")
    error_message = "public keeps Traefik's Let's Encrypt resolver and HTTPS redirect"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "ACCESS=public\n") && strcontains(aws_ssm_parameter.env.value, "PUBLIC_URL=https://crucible.example.com\n") && strcontains(aws_ssm_parameter.env.value, "HOST=crucible.example.com\n") && strcontains(aws_ssm_parameter.env.value, "TLS_ENABLED=true\n")
    error_message = "the node's env carries the public URL, host and TLS"
  }
  assert {
    condition     = aws_instance.node.tags["crucible:public-url"] == "https://crucible.example.com"
    error_message = "the node is tagged with its address"
  }
}

run "ssm_access_has_no_way_in" {
  command = plan
  variables {
    access     = "ssm"
    public_url = "http://localhost:18080"
    subnet_id  = "subnet-0fa66f46aa023ab6e"
    domain     = ""
    acme_email = ""
  }

  assert {
    condition     = length(aws_eip.web) == 0 && length(aws_route53_record.web) == 0 && output.public_ip == ""
    error_message = "ssm creates no Elastic IP and no DNS record"
  }
  assert {
    condition     = length(aws_security_group.web.ingress) == 0 && aws_security_group.web.vpc_id == "vpc-spoke"
    error_message = "ssm opens no inbound port, in the given subnet's VPC"
  }
  assert {
    condition     = aws_instance.node.subnet_id == "subnet-0fa66f46aa023ab6e" && aws_instance.node.associate_public_ip_address == false
    error_message = "the node runs in the given subnet without a public IP"
  }
  assert {
    condition     = aws_instance.node.tags["crucible:public-url"] == "http://localhost:18080" && output.url == "http://localhost:18080" && output.access == "ssm"
    error_message = "the node carries its localhost address for `crucible aws connect`"
  }
  assert {
    condition     = strcontains(aws_ssm_parameter.env.value, "PUBLIC_URL=http://localhost:18080\n") && strcontains(aws_ssm_parameter.env.value, "HOST=\n") && strcontains(aws_ssm_parameter.env.value, "TLS_ENABLED=false\n")
    error_message = "ssm: plain HTTP, any host"
  }
  assert {
    condition     = !strcontains(aws_instance.node.user_data, "certificatesResolvers") && !strcontains(aws_instance.node.user_data, "redirections")
    error_message = "ssm: no Let's Encrypt, no HTTPS redirect"
  }
}

run "ssm_access_needs_a_subnet" {
  command = plan
  variables {
    access     = "ssm"
    public_url = "http://localhost:18080"
    domain     = ""
    acme_email = ""
  }
  expect_failures = [var.subnet_id]
}

run "public_access_needs_a_domain" {
  command = plan
  variables {
    domain = ""
  }
  expect_failures = [var.domain]
}

run "public_url_defaults_to_https_domain_for_old_stacks" {
  command = plan
  variables {
    public_url = ""
  }

  assert {
    condition     = output.url == "https://crucible.example.com"
    error_message = "a persistent stack made before access existed passes no public_url: main builds https://<domain>"
  }
}

run "deploy_script_reads_the_url_from_env" {
  command = plan

  assert {
    condition     = strcontains(aws_instance.node.user_data, "publicURL=\"$${PUBLIC_URL:-https://$DOMAIN}\"") && strcontains(aws_instance.node.user_data, "host=\"$${HOST-$DOMAIN}\"") && strcontains(aws_instance.node.user_data, "tls.enabled=\"$${TLS_ENABLED:-true}\"")
    error_message = "deploy.sh takes the URL, host and TLS from /<name>/env, falling back to today's https://$DOMAIN"
  }
}
```

- [ ] **Step 2: Run the tests and see them fail.**

Run: `cd deploy/aws/main && terraform test`
Expected: FAIL: `var.access`, `var.public_url`, `var.subnet_id`, `output.access` undeclared; `aws_eip.web` has no `length` (not a counted resource).

- [ ] **Step 3: Implement the variables.** In `deploy/aws/main/variables.tf`, replace the `domain` and `acme_email` variables and add three new ones after them:

```hcl
variable "domain" {
  type        = string
  default     = ""
  description = "access = public: the public hostname, e.g. crucible.example.com (point an A record at the public_ip output, or set route53_zone_id)"
  validation {
    condition     = var.access != "public" || var.domain != ""
    error_message = "access = public needs domain"
  }
}
variable "route53_zone_id" {
  type    = string
  default = ""
}
variable "acme_email" {
  type        = string
  default     = ""
  description = "access = public: the Let's Encrypt account email"
  validation {
    condition     = var.access != "public" || var.acme_email != ""
    error_message = "access = public needs acme_email (Let's Encrypt)"
  }
}
variable "access" {
  type        = string
  default     = "public"
  description = "Set by `crucible aws up` from the persistent stack: public or ssm"
  validation {
    condition     = contains(["public", "ssm"], var.access)
    error_message = "access must be public or ssm"
  }
}
variable "public_url" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the persistent stack. Empty (a persistent stack made before access existed): https://<domain>"
  validation {
    condition     = var.public_url == "" || can(regex("^https?://[^/]+$", var.public_url))
    error_message = "public_url must be http(s)://host[:port] with no path"
  }
}
variable "subnet_id" {
  type        = string
  default     = ""
  description = "The subnet the node runs in. Required for access = ssm (an existing workload subnet). Empty in public mode: the default VPC's first default subnet"
  validation {
    condition     = (var.access != "ssm" || var.subnet_id != "") && (var.subnet_id == "" || can(regex("^subnet-[0-9a-f]+$", var.subnet_id)))
    error_message = "access = ssm needs subnet_id (subnet-<hex>), an existing subnet the node can reach the internet from"
  }
}
```

(Keep the existing `route53_zone_id` variable only once: if it already sits between `domain` and `acme_email`, this block replaces all three.)

- [ ] **Step 4: Implement the main stack.** In `deploy/aws/main/main.tf`, replace lines 1–12 (the two default-VPC data sources) with:

```hcl
locals {
  public     = var.access == "public"
  public_url = var.public_url != "" ? var.public_url : "https://${var.domain}"
  host       = local.public ? var.domain : "" # empty: the Ingress matches any Host (through the tunnel it's localhost:<port>)
}

data "aws_vpc" "default" {
  count   = var.subnet_id == "" ? 1 : 0
  default = true
}

data "aws_subnets" "default" {
  count = var.subnet_id == "" ? 1 : 0
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default[0].id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_subnet" "given" {
  count = var.subnet_id == "" ? 0 : 1
  id    = var.subnet_id
}

locals {
  vpc_id    = var.subnet_id == "" ? data.aws_vpc.default[0].id : data.aws_subnet.given[0].vpc_id
  subnet_id = var.subnet_id == "" ? sort(data.aws_subnets.default[0].ids)[0] : var.subnet_id
}
```

Replace the security group's `vpc_id` and its two `ingress` blocks (keep the `egress` block as it is):

```hcl
resource "aws_security_group" "web" {
  name   = "${var.name}-web"
  vpc_id = local.vpc_id
  # access = ssm opens nothing: people arrive through a Session Manager port-forward, which needs no inbound rule.
  dynamic "ingress" {
    for_each = { for port, what in { "80" = "HTTP (ACME challenge + redirect)", "443" = "HTTPS" } : port => what if local.public }
    content {
      description      = ingress.value
      from_port        = ingress.key
      to_port          = ingress.key
      protocol         = "tcp"
      cidr_blocks      = ["0.0.0.0/0"]
      ipv6_cidr_blocks = ["::/0"]
    }
  }
  egress {
```

In the `aws_ssm_parameter.env` heredoc, add four lines after `DOMAIN=${var.domain}`:

```
    ACCESS=${var.access}
    PUBLIC_URL=${local.public_url}
    HOST=${local.host}
    TLS_ENABLED=${local.public}
```

In `aws_instance.node`: replace `subnet_id = sort(data.aws_subnets.default.ids)[0]` with `subnet_id = local.subnet_id`, add after it `associate_public_ip_address = local.public ? null : false`, add `access = var.access` to the `templatefile(...)` map, and replace `tags = { Name = var.name }` with:

```hcl
  tags = { Name = var.name, "crucible:public-url" = local.public_url } # `crucible aws connect` finds the node and its port by these
```

Replace the EIP and Route 53 resources (lines 241–253) with:

```hcl
resource "aws_eip" "web" {
  count    = local.public ? 1 : 0
  instance = aws_instance.node.id
  domain   = "vpc"
}

# The Elastic IP became counted: keep existing public installs' address instead of replacing it.
moved {
  from = aws_eip.web
  to   = aws_eip.web[0]
}

resource "aws_route53_record" "web" {
  count   = local.public && var.route53_zone_id != "" ? 1 : 0
  zone_id = var.route53_zone_id
  name    = var.domain
  type    = "A"
  ttl     = 300
  records = [aws_eip.web[0].public_ip]
}
```

In `deploy/aws/main/outputs.tf`, replace the `public_ip` and `url` outputs and add `access`:

```hcl
output "public_ip" { value = local.public ? aws_eip.web[0].public_ip : "" }
output "url" { value = local.public_url }
output "access" { value = var.access }
```

- [ ] **Step 5: Implement the node scripts.** In `deploy/aws/main/bootstrap.sh.tftpl`, replace the comment line `# Traefik (bundled with k3s): Let's Encrypt via HTTP-01 and an HTTP→HTTPS redirect.` and wrap the Traefik manifest so it is written only in public mode. The block becomes:

```bash
mkdir -p /var/lib/rancher/k3s/server/manifests
%{ if access == "public" ~}
# Traefik (bundled with k3s): Let's Encrypt via HTTP-01 and an HTTP→HTTPS redirect. access = ssm keeps k3s's
# plain-HTTP Traefik: nothing outside the VPC can reach the node, so there is no certificate to get.
cat > /var/lib/rancher/k3s/server/manifests/traefik-config.yaml <<'YAML'
```

…the existing YAML lines unchanged…, and after the closing `YAML` line add:

```bash
%{ endif ~}
```

(The existing `mkdir -p /var/lib/rancher/k3s/server/manifests` line moves above the `%{ if` so it is kept once.)

In the deploy.sh heredoc, replace the helm line

```bash
  --set-string image.tag="$sha" --set host="$DOMAIN" --set publicURL="https://$DOMAIN" \
```

with

```bash
  --set-string image.tag="$sha" --set-string host="$${HOST-$DOMAIN}" --set-string publicURL="$${PUBLIC_URL:-https://$DOMAIN}" \
  --set tls.enabled="$${TLS_ENABLED:-true}" \
```

(`$${…}` is the template's escape for a literal `${…}`. The fallbacks keep a node whose env predates these keys on `https://$DOMAIN`.)

- [ ] **Step 6: Run the tests and see them pass.**

Run: `cd deploy/aws/main && terraform fmt -check && terraform test`
Expected: PASS, including the existing `node_is_locked_down` (ports 80 and 443 in public mode) and `node_settings_come_from_ssm`.

- [ ] **Step 7: Commit.**

```bash
git commit -m "feat(aws): main stack access = ssm: existing subnet, no Elastic IP or inbound rules, plain-HTTP Traefik

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/main/variables.tf deploy/aws/main/main.tf deploy/aws/main/outputs.tf deploy/aws/main/bootstrap.sh.tftpl deploy/aws/main/main.tftest.hcl
```

---

### Task 3: Helm: plain-HTTP, any-host Ingress

**Files:**
- Modify: `deploy/helm/crucible/values.yaml` (`host` line 5, `tls` lines 16–17)
- Modify: `deploy/helm/crucible/templates/crucible.yaml` (the Ingress, lines ~127–143)
- Test: `deploy/helm/test.sh`

**Interfaces:**
- Consumes: deploy.sh (Task 2) sets `host`, `publicURL`, `tls.enabled`.
- Produces: values `tls.enabled` (bool, default `true`), `host` may be `""`.

- [ ] **Step 1: Write the failing tests.** In `deploy/helm/test.sh`, after the line `need 'router.tls.certresolver: le'`, add:

```bash
need 'router.entrypoints: websecure'
```

and after the line `base="--set backup.bucket=b --set backup.region=eu-west-1 --set oidc.issuer=https://sso"` add:

```bash
# access = ssm: plain HTTP on Traefik's web entrypoint, any Host (through the tunnel it is localhost:<port>).
plain=$(helm template t "$chart" $base --set tls.enabled=false --set host= --set publicURL=http://localhost:18080)
ing=$(sed -n '/^kind: Ingress$/,/^---$/p' <<<"$plain")
grep -q 'router.entrypoints: web$' <<<"$ing" || { echo "tls off: the Ingress must use the web entrypoint"; exit 1; }
if grep -qE 'router.tls|certresolver|^ *host:' <<<"$ing"; then echo "tls off: no TLS, no cert resolver, no host rule"; exit 1; fi
grep -q 'CRUCIBLE_PUBLIC_URL, value: "http://localhost:18080"' <<<"$plain" || { echo "tls off: the API's public URL is the localhost one"; exit 1; }
if helm template t "$chart" $base --set tls.enabled=false >/dev/null 2>&1; then echo "tls off with an https publicURL must fail"; exit 1; fi
if helm template t "$chart" $base --set publicURL=http://localhost:18080 >/dev/null 2>&1; then echo "an http publicURL with tls on must fail"; exit 1; fi
```

(The chart renders the API's URL as `- { name: CRUCIBLE_PUBLIC_URL, value: {{ .Values.publicURL | quote }} }`, so the pattern above matches it as written.)

- [ ] **Step 2: Run the tests and see them fail.**

Run: `bash deploy/helm/test.sh`
Expected: FAIL with `tls off: the Ingress must use the web entrypoint`.

- [ ] **Step 3: Implement.** In `deploy/helm/crucible/values.yaml`:

```yaml
host: crucible.example.com   # empty: the Ingress matches any Host (access = ssm, reached through a tunnel)
```

```yaml
tls:
  enabled: true              # false: plain HTTP on Traefik's web entrypoint (access = ssm); must match publicURL's scheme
  certResolver: le
```

In `deploy/helm/crucible/templates/crucible.yaml`, replace the Ingress document with:

```yaml
{{- if ne .Values.tls.enabled (hasPrefix "https://" .Values.publicURL) }}
{{- fail (printf "tls.enabled=%v does not match publicURL %s: https needs TLS and http needs it off, or sign-in breaks" .Values.tls.enabled .Values.publicURL) }}
{{- end }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: crucible
  annotations:
    {{- if .Values.tls.enabled }}
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
    traefik.ingress.kubernetes.io/router.tls.certresolver: {{ .Values.tls.certResolver }}
    {{- else }}
    traefik.ingress.kubernetes.io/router.entrypoints: web
    {{- end }}
spec:
  ingressClassName: traefik
  rules:
    - {{- with .Values.host }}
      host: {{ . }}
      {{- end }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend: { service: { name: crucible, port: { number: 80 } } }
```

- [ ] **Step 4: Run the tests and see them pass.**

Run: `bash deploy/helm/test.sh`
Expected: exits 0 with no "missing" line.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(helm): tls.enabled=false serves plain HTTP for any host; a URL/TLS mismatch fails the render

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/helm/crucible/values.yaml deploy/helm/crucible/templates/crucible.yaml deploy/helm/test.sh
```

---

### Task 4: Lab network in the labs stack, main stack and chart

**Files:**
- Modify: `deploy/aws/labs/main.tf` (variables near lines 16–70, outputs near line 405)
- Test: `deploy/aws/labs/labs.tftest.hcl`
- Modify: `deploy/aws/main/variables.tf` (after `lab_export_name`), `deploy/aws/main/main.tf` (`helm_values` `awsLabs` map ~line 106)
- Test: `deploy/aws/main/main.tftest.hcl` (`aws_labs_wiring` assert ~line 199)
- Modify: `deploy/helm/crucible/values.yaml` (`awsLabs`), `deploy/helm/crucible/templates/crucible.yaml` (env after `CRUCIBLE_AWS_EXPORT_NAME`, ~line 105)
- Test: `deploy/helm/test.sh`

**Interfaces:**
- Produces: labs outputs `lab_vpc_id` (string), `lab_subnet_ids` (string, comma-separated); labs variables `lab_vpc_id` (string), `lab_subnet_ids` (list(string)) set by Task 6's `labs-init`. Main variables `lab_vpc_id`, `lab_subnet_ids` (both string). Helm values `awsLabs.vpcId`, `awsLabs.subnetIds`; API env `CRUCIBLE_AWS_LAB_VPC_ID`, `CRUCIBLE_AWS_LAB_SUBNET_IDS` (Task 5 reads them).

- [ ] **Step 1: Write the failing Terraform tests.** In `deploy/aws/labs/labs.tftest.hcl`, inside the first (unaliased) `mock_provider "aws"` block add:

```hcl
  mock_data "aws_subnet" {
    defaults = { vpc_id = "vpc-0abc" }
  }
```

and append:

```hcl
run "no_lab_network_by_default" {
  command = plan

  assert {
    condition     = output.lab_vpc_id == "" && output.lab_subnet_ids == ""
    error_message = "without --vpc-id and --subnet-ids, labs use the default VPC"
  }
}

run "lab_network_reaches_the_outputs" {
  command = plan
  variables {
    lab_vpc_id     = "vpc-0abc"
    lab_subnet_ids = ["subnet-01", "subnet-02"]
  }

  assert {
    condition     = output.lab_vpc_id == "vpc-0abc" && output.lab_subnet_ids == "subnet-01,subnet-02"
    error_message = "the lab network reaches the outputs `crucible aws up` passes on"
  }
}

run "lab_network_is_both_or_neither" {
  command = plan
  variables {
    lab_subnet_ids = ["subnet-01"]
  }
  expect_failures = [var.lab_subnet_ids]
}

run "lab_subnets_must_be_in_the_vpc" {
  command = plan
  variables {
    lab_vpc_id     = "vpc-0other"
    lab_subnet_ids = ["subnet-01"]
  }
  expect_failures = [data.aws_subnet.lab]
}
```

In `deploy/aws/main/main.tftest.hcl`, in run `aws_labs_wiring`, change the expected `awsLabs` map to end with `exportName = "crucible-labs", vpcId = "", subnetIds = "" }`, and append:

```hcl
run "lab_network_reaches_helm" {
  command = plan
  variables {
    lab_role_arn      = "arn:aws:iam::444455556666:role/crucible-lab"
    lab_ops_role_arn  = "arn:aws:iam::444455556666:role/crucible-lab-ops"
    lab_state_bucket  = "crucible-444455556666-labstate"
    lab_state_region  = "il-central-1"
    lab_plan_role_arn = "arn:aws:iam::444455556666:role/crucible-lab-plan"
    lab_export_bucket = "crucible-444455556666-exports"
    lab_export_name   = "crucible-labs"
    lab_vpc_id        = "vpc-0abc"
    lab_subnet_ids    = "subnet-01,subnet-02"
  }

  assert {
    condition     = yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.vpcId == "vpc-0abc" && yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.subnetIds == "subnet-01,subnet-02"
    error_message = "the lab network reaches the Helm values"
  }
}
```

- [ ] **Step 2: Write the failing chart test.** In `deploy/helm/test.sh`, after the `for want in 'name: CRUCIBLE_AWS_LABS, value: "1"' …; done` loop, add:

```bash
grep -q 'name: CRUCIBLE_AWS_LAB_SUBNET_IDS, value: ""' <<<"$awsout" || { echo "aws labs: an unset lab network is empty"; exit 1; }
net=$(helm template t "$chart" $aws --set awsLabs.vpcId=vpc-0abc --set awsLabs.subnetIds=subnet-01\\,subnet-02)
grep -q 'name: CRUCIBLE_AWS_LAB_VPC_ID, value: "vpc-0abc"' <<<"$net" || { echo "missing: lab VPC env"; exit 1; }
grep -q 'name: CRUCIBLE_AWS_LAB_SUBNET_IDS, value: "subnet-01,subnet-02"' <<<"$net" || { echo "missing: lab subnets env"; exit 1; }
```

- [ ] **Step 3: Run the tests and see them fail.**

Run: `(cd deploy/aws/labs && terraform test); (cd deploy/aws/main && terraform test); bash deploy/helm/test.sh`
Expected: FAIL: labs outputs `lab_vpc_id` / variables undeclared; main's `aws_labs_wiring` map mismatch; chart `aws labs: an unset lab network is empty`.

- [ ] **Step 4: Implement the labs stack.** In `deploy/aws/labs/main.tf`, after the `max_volume_gib` variable add:

```hcl
# Where lab modules launch into when the account has no default VPC (`crucible aws labs-init --vpc-id --subnet-ids`).
# Crucible hands them to every lab as var.crucible_vpc_id and var.crucible_subnet_ids. Empty: labs use the default VPC.
variable "lab_vpc_id" {
  type    = string
  default = ""
  validation {
    condition     = var.lab_vpc_id == "" || can(regex("^vpc-[0-9a-f]+$", var.lab_vpc_id))
    error_message = "lab_vpc_id must be vpc-<hex>"
  }
}
variable "lab_subnet_ids" {
  type    = list(string)
  default = []
  validation {
    condition     = alltrue([for s in var.lab_subnet_ids : can(regex("^subnet-[0-9a-f]+$", s))]) && (length(var.lab_subnet_ids) == 0) == (var.lab_vpc_id == "")
    error_message = "lab_subnet_ids must be subnet-<hex> values, given together with lab_vpc_id (both or neither)"
  }
}

# Each lab subnet must exist in this region and sit in lab_vpc_id: a lab would otherwise fail at apply.
data "aws_subnet" "lab" {
  for_each = toset(var.lab_subnet_ids)
  id       = each.value
  lifecycle {
    postcondition {
      condition     = self.vpc_id == var.lab_vpc_id
      error_message = "subnet ${self.id} is in ${self.vpc_id}, not ${var.lab_vpc_id}"
    }
  }
}
```

and next to the other outputs:

```hcl
output "lab_vpc_id" { value = var.lab_vpc_id }
output "lab_subnet_ids" { value = join(",", var.lab_subnet_ids) }
```

- [ ] **Step 5: Implement the main stack and chart.** In `deploy/aws/main/variables.tf`, after `lab_export_name`:

```hcl
variable "lab_vpc_id" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: the VPC lab modules launch into (empty: the default VPC)"
}
variable "lab_subnet_ids" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: comma-separated subnets lab modules launch into"
}
```

In `deploy/aws/main/main.tf`, in the `awsLabs` map of `aws_ssm_parameter.helm_values`, after `exportName = var.lab_export_name` add:

```hcl
      # where lab modules launch (var.crucible_vpc_id, var.crucible_subnet_ids); empty: the default VPC
      vpcId     = var.lab_vpc_id
      subnetIds = var.lab_subnet_ids
```

In `deploy/helm/crucible/values.yaml`, under `awsLabs`, after `exportName: ""`:

```yaml
  vpcId: ""                 # where lab modules launch (labs-init --vpc-id); empty: the default VPC
  subnetIds: ""             # comma-separated (labs-init --subnet-ids)
```

In `deploy/helm/crucible/templates/crucible.yaml`, after the `CRUCIBLE_AWS_EXPORT_NAME` env line:

```yaml
            - { name: CRUCIBLE_AWS_LAB_VPC_ID, value: {{ .Values.awsLabs.vpcId | quote }} }
            - { name: CRUCIBLE_AWS_LAB_SUBNET_IDS, value: {{ .Values.awsLabs.subnetIds | quote }} }
```

- [ ] **Step 6: Run the tests and see them pass.**

Run: `(cd deploy/aws/labs && terraform fmt -check && terraform test) && (cd deploy/aws/main && terraform test) && bash deploy/helm/test.sh`
Expected: all PASS.

- [ ] **Step 7: Commit.**

```bash
git commit -m "feat(aws): labs-init's lab VPC and subnets reach the API (labs stack → main → Helm)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/labs/main.tf deploy/aws/labs/labs.tftest.hcl deploy/aws/main/variables.tf deploy/aws/main/main.tf deploy/aws/main/main.tftest.hcl deploy/helm/crucible/values.yaml deploy/helm/crucible/templates/crucible.yaml deploy/helm/test.sh
```

---

### Task 5: Lab modules get `var.crucible_vpc_id` and `var.crucible_subnet_ids`

**Files:**
- Modify: `internal/content/awstf.go`
- Test: `internal/content/load_test.go` (`TestLabTFVarsIsJSON` ~line 614)
- Modify: `internal/labs/bundle.go` (lines 52–65), `internal/labs/aws.go` (struct ~line 31, call sites ~lines 234 and 393)
- Test: `internal/labs/aws_objects_test.go` (~line 275)
- Modify: `cmd/crucible-api/main.go` (aws labs setup ~line 266)
- Test: `cmd/crucible-api/main_test.go`
- Modify: `docs/user/authors/aws-labs.md` (after line 34)

**Interfaces:**
- Consumes: env `CRUCIBLE_AWS_LAB_VPC_ID`, `CRUCIBLE_AWS_LAB_SUBNET_IDS` (Task 4).
- Produces: `content.LabNetwork{Region, VPCID string; SubnetIDs []string}`; `content.LabTFVars(labID, team, training, region string, net content.LabNetwork) []byte`; `labs.ModuleBundle(lab *content.Lab, inst *Instance, net content.LabNetwork) ([]byte, error)`; `planBundle(lab *content.Lab, net content.LabNetwork)`; field `labs.AWSRunner.Network content.LabNetwork`; `commaList(s string) []string` in `cmd/crucible-api`.

- [ ] **Step 1: Write the failing tests.** In `internal/content/load_test.go`, replace `TestLabTFVarsIsJSON` with:

```go
func TestLabTFVarsIsJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(LabTFVars("abcdefabcdef", `te"am`, "tr", "eu-west-1", LabNetwork{}), &v); err != nil || v["crucible_team"] != `te"am` || v["crucible_region"] != "eu-west-1" {
		t.Fatalf("tfvars must be JSON so no value escapes its string: %v %v", v, err)
	}
	for _, want := range []string{`backend "s3" {}`, `provider "aws"`, `"crucible:lab-id"   = var.crucible_lab_id`,
		`variable "crucible_vpc_id" { type = string }`, `variable "crucible_subnet_ids" { type = list(string) }`} {
		if !strings.Contains(LabTF, want) {
			t.Fatalf("LabTF lacks %q", want)
		}
	}
}

// The lab network reaches labs in its own region only; elsewhere, and when unset, it is "" and [] (never null:
// Terraform's length() fails on null, and the portable form calls length(var.crucible_subnet_ids)).
func TestLabTFVarsCarryTheLabNetworkInItsRegion(t *testing.T) {
	net := LabNetwork{Region: "il-central-1", VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"}}
	decode := func(b []byte) map[string]any {
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	in := decode(LabTFVars("abcdefabcdef", "t", "tr", "il-central-1", net))
	if in["crucible_vpc_id"] != "vpc-1" || fmt.Sprint(in["crucible_subnet_ids"]) != "[subnet-a subnet-b]" {
		t.Fatalf("same region: %v", in)
	}
	other := decode(LabTFVars("abcdefabcdef", "t", "tr", "eu-west-1", net))
	if other["crucible_vpc_id"] != "" || fmt.Sprint(other["crucible_subnet_ids"]) != "[]" {
		t.Fatalf("another region must get no network: %v", other)
	}
	none := decode(LabTFVars("abcdefabcdef", "t", "tr", "eu-west-1", LabNetwork{}))
	if none["crucible_subnet_ids"] == nil || fmt.Sprint(none["crucible_subnet_ids"]) != "[]" || none["crucible_vpc_id"] != "" {
		t.Fatalf("an unset network is \"\" and [], never null: %v", none)
	}
}
```

(`internal/content/load_test.go` already imports `"fmt"`.)

In `internal/labs/aws_objects_test.go`, change `b, err := planBundle(lab)` to `b, err := planBundle(lab, content.LabNetwork{})` and append:

```go
func TestModuleBundlesCarryTheLabNetwork(t *testing.T) {
	lab := &content.Lab{Dir: t.TempDir(), AWS: &content.AWSConfig{Region: "il-central-1"}}
	if err := os.MkdirAll(filepath.Join(lab.Dir, "terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	net := content.LabNetwork{Region: "il-central-1", VPCID: "vpc-1", SubnetIDs: []string{"subnet-a"}}
	plan, err := planBundle(lab, net)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := ModuleBundle(lab, &Instance{ID: "abcdefabcdef", Team: "t", Training: "tr"}, net)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"plan": plan, "apply": apply} {
		vars := untar(t, b)[content.LabTFVarsFile]
		if !strings.Contains(vars, `"crucible_subnet_ids":["subnet-a"]`) || !strings.Contains(vars, `"crucible_vpc_id":"vpc-1"`) {
			t.Fatalf("%s module vars: %s", name, vars)
		}
	}
}
```

In `cmd/crucible-api/main_test.go`, append:

```go
func TestCommaList(t *testing.T) {
	for in, want := range map[string]string{"": "[]", "subnet-a": "[subnet-a]", " subnet-a , subnet-b ,": "[subnet-a subnet-b]"} {
		if got := fmt.Sprint(commaList(in)); got != want {
			t.Errorf("commaList(%q) = %s, want %s", in, got, want)
		}
	}
}
```

(Add `"fmt"` to `cmd/crucible-api/main_test.go`'s imports: it has none today.)

- [ ] **Step 2: Run the tests and see them fail.**

Run: `go test ./internal/content ./internal/labs ./cmd/crucible-api`
Expected: FAIL to compile: `undefined: LabNetwork`, too many arguments to `LabTFVars` / `planBundle` / `ModuleBundle`, `undefined: commaList`.

- [ ] **Step 3: Implement `content`.** In `internal/content/awstf.go`, add to `LabTF` after `variable "crucible_region" { type = string }`:

```hcl
variable "crucible_vpc_id" { type = string }           # "" when Crucible has no lab network (the default VPC)
variable "crucible_subnet_ids" { type = list(string) } # [] when it has none
```

and replace `LabTFVars` with:

```go
// LabNetwork is where lab modules launch when the account has no default VPC (`crucible aws labs-init --vpc-id
// --subnet-ids`). Region is the region those subnets are in: a lab elsewhere gets no network.
type LabNetwork struct {
	Region    string
	VPCID     string
	SubnetIDs []string
}

// LabTFVars fills LabTF's variables. JSON, so no value can break out of its string. The network is "" and [] (never
// null) when unset or in another region, so a module's `length(var.crucible_subnet_ids) > 0 ? … : null` falls back to
// the default VPC.
func LabTFVars(labID, team, training, region string, net LabNetwork) []byte {
	vpc, subnets := "", []string{}
	if net.VPCID != "" && net.Region == region && len(net.SubnetIDs) > 0 {
		vpc, subnets = net.VPCID, net.SubnetIDs
	}
	b, _ := json.Marshal(map[string]any{"crucible_lab_id": labID, "crucible_team": team,
		"crucible_training": training, "crucible_region": region, "crucible_vpc_id": vpc, "crucible_subnet_ids": subnets})
	return b
}
```

- [ ] **Step 4: Implement `labs`.** In `internal/labs/aws.go`, add to `AWSRunner` after `StateRegion string`:

```go
	Network        content.LabNetwork // labs-init --vpc-id --subnet-ids (CRUCIBLE_AWS_LAB_VPC_ID, …_SUBNET_IDS)
```

change `module, err := ModuleBundle(lab, inst)` to `module, err := ModuleBundle(lab, inst, a.Network)` and `module, err := planBundle(lab)` to `module, err := planBundle(lab, a.Network)`. In `internal/labs/bundle.go`:

```go
func ModuleBundle(lab *content.Lab, inst *Instance, net content.LabNetwork) ([]byte, error) {
	return moduleTgz(lab, content.LabTF, content.LabTFVars(inst.ID, inst.Team, inst.Training, lab.AWS.Region, net))
}
```

```go
func planBundle(lab *content.Lab, net content.LabNetwork) ([]byte, error) {
	return moduleTgz(lab, planTF, content.LabTFVars("estimate", "estimate", "estimate", lab.AWS.Region, net))
}
```

Run `grep -rn "ModuleBundle(\|planBundle(\|LabTFVars(" internal cmd` and pass `content.LabNetwork{}` (tests) or the runner's network at any other call site it lists.

- [ ] **Step 5: Implement the API.** In `cmd/crucible-api/main.go`, right after the `ar := &labs.AWSRunner{…}` statement in the `case "1", "dryrun":` branch, add:

```go
		ar.Network = content.LabNetwork{Region: ar.StateRegion, VPCID: os.Getenv("CRUCIBLE_AWS_LAB_VPC_ID"),
			SubnetIDs: commaList(os.Getenv("CRUCIBLE_AWS_LAB_SUBNET_IDS"))} // the labs stack's own region
```

and at the end of the file:

```go
// commaList splits a comma-separated env value, dropping blanks.
func commaList(s string) []string {
	out := []string{}
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
```

- [ ] **Step 6: Update the authors' doc.** In `docs/user/authors/aws-labs.md`, after the paragraph that ends "…`var.crucible_training`) when you need them." (line 34), add:

```markdown
- **Where to launch.** Some accounts have no default VPC. There Crucible gives every lab `var.crucible_vpc_id` and
  `var.crucible_subnet_ids` (set by the operator with `crucible aws labs-init --vpc-id … --subnet-ids …`). Write the
  network so the lab works in either kind of account:

  ```hcl
  resource "aws_security_group" "box" {
    vpc_id = var.crucible_vpc_id != "" ? var.crucible_vpc_id : null
  }
  resource "aws_instance" "box" {
    subnet_id                   = length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null
    associate_public_ip_address = false # labs reach the trainee through the workspace, not a public IP
  }
  ```

  `null` falls back to the default VPC where there is one. Never write a real VPC or subnet id in a training repo.
```

- [ ] **Step 7: Run the tests and see them pass.**

Run: `gofmt -l cmd internal; go vet ./internal/content ./internal/labs ./cmd/crucible-api && go test ./internal/content ./internal/labs ./cmd/crucible-api ./internal/docs`
Expected: no gofmt output; PASS.

- [ ] **Step 8: Commit.**

```bash
git commit -m "feat(labs): lab modules get var.crucible_vpc_id and var.crucible_subnet_ids in the lab network's region

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content/awstf.go internal/content/load_test.go internal/labs/aws.go internal/labs/bundle.go internal/labs/aws_objects_test.go cmd/crucible-api/main.go cmd/crucible-api/main_test.go docs/user/authors/aws-labs.md
```

---

### Task 6: `init --access`, the persistent outputs into `up`, `labs-init --vpc-id --subnet-ids`

**Files:**
- Modify: `internal/awsops/awsops.go` (`Init` line 85, `mainVars` ~line 94, `LabsInit` line 152, `labVars` line 199)
- Test: `internal/awsops/awsops_test.go`
- Modify: `cmd/crucible/main.go` (usage, `awsCmd` flags, `init` and `labs-init` cases)

**Interfaces:**
- Consumes: Task 1 persistent outputs `access`, `local_port`, `domain`, `public_url`; Task 4 labs outputs `lab_vpc_id`, `lab_subnet_ids`; labs variables `lab_vpc_id` (string), `lab_subnet_ids` (list).
- Produces: `awsops.InitOpts{Region, Domain, Access string; LocalPort int}`; `func (o Ops) Init(ctx context.Context, in InitOpts) error`; `awsops.LabNet{VPCID string; SubnetIDs []string}`; `func (o Ops) LabsInit(ctx context.Context, region, crucibleAccount string, budgetUSD float64, budgetEmails, allowedRegions []string, net LabNet) error`; `func commaList(s string) []string` in awsops.

- [ ] **Step 1: Write the failing tests.** In `internal/awsops/awsops_test.go`:

1. Append `, LabNet{}` as the last argument of every existing `LabsInit(` call (13 calls; `grep -n "LabsInit(" internal/awsops/awsops_test.go`).
2. In `TestInitAndUp`, change the `Init` call and its check to:

```go
	if err := o.Init(context.Background(), InitOpts{Region: "eu-west-1", Domain: "c.example.com"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=eu-west-1 -var access=public -var local_port=18080 -var domain=c.example.com") < 0 {
		t.Fatalf("init apply missing:\n%s", strings.Join(r.calls, "\n"))
	}
```

3. In `TestLabsInitAndUpWiresTheLabAccount`, add `"lab_vpc_id":{"value":"vpc-1"},"lab_subnet_ids":{"value":"subnet-a,subnet-b"}` to the `labs` JSON and `"-var lab_vpc_id=vpc-1", "-var lab_subnet_ids=subnet-a,subnet-b"` to its `want` list.
4. Append:

```go
// persistentOut is a persistent stack's outputs: extra is spliced in (e.g. `,"access":{"value":"ssm"}`).
func persistentOut(extra string) func(string) string {
	return func(cmd string) string {
		if strings.Contains(cmd, "persistent output -json") {
			return `{"state_bucket":{"value":"st"},"region":{"value":"il-central-1"}` + extra + `}`
		}
		return fakeAWS(cmd)
	}
}

func newStack(cmd string) error { // a persistent stack with no state yet
	if strings.Contains(cmd, "persistent output -json") {
		return errors.New("no outputs")
	}
	return nil
}

func TestInitSSMTakesNoDomain(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: newStack}
	if err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "il-central-1", Access: "ssm"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=il-central-1 -var access=ssm -var local_port=18080") < 0 || indexOf(r.calls, "-var domain") >= 0 {
		t.Fatalf("ssm init:\n%s", strings.Join(r.calls, "\n"))
	}
}

// Review focus 1: a re-run without --access keeps ssm and its port; it never flips back to public.
func TestInitKeepsTheCurrentAccess(t *testing.T) {
	r := &recorder{output: persistentOut(`,"access":{"value":"ssm"},"local_port":{"value":18443},"domain":{"value":""}`)}
	if err := r.ops(t.TempDir()).Init(context.Background(), InitOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "persistent apply -var region=il-central-1 -var access=ssm -var local_port=18443") < 0 {
		t.Fatalf("re-run must keep ssm and 18443:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestInitRefusesBadCombinations(t *testing.T) {
	for name, in := range map[string]InitOpts{
		"unknown access":          {Region: "il-central-1", Access: "vpn"},
		"public without a domain": {Region: "il-central-1", Access: "public"},
		"ssm with a domain":       {Region: "il-central-1", Access: "ssm", Domain: "c.example.com"},
		"privileged port":         {Region: "il-central-1", Access: "ssm", LocalPort: 80},
	} {
		r := &recorder{output: fakeAWS, outErr: newStack}
		if err := r.ops(t.TempDir()).Init(context.Background(), in); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if indexOf(r.calls, "persistent apply") >= 0 {
			t.Errorf("%s: nothing may be applied", name)
		}
	}
}

func TestUpPassesAccessFromThePersistentStack(t *testing.T) {
	r := &recorder{output: persistentOut(`,"data_bucket":{"value":"bkt"},"oidc_issuer":{"value":"https://x"},"oidc_client_id":{"value":"c"},"cognito_user_pool_id":{"value":"p"},"access":{"value":"ssm"},"public_url":{"value":"http://localhost:18080"}`)}
	_ = r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true)
	for _, want := range []string{"-var access=ssm", "-var public_url=http://localhost:18080"} {
		if indexOf(r.calls, want) < 0 {
			t.Fatalf("missing %q in:\n%s", want, strings.Join(r.calls, "\n"))
		}
	}
}

// Review focus 2: a persistent stack made before access existed has neither output; main must stay public.
func TestUpOnAnOldPersistentStackStaysPublic(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "-var access=") >= 0 || indexOf(r.calls, "-var public_url=") >= 0 {
		t.Fatalf("no access or public_url without the outputs:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitPassesTheLabNetwork(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil,
		LabNet{VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"}}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var lab_vpc_id=vpc-1 -var lab_subnet_ids=["subnet-a","subnet-b"]`) < 0 {
		t.Fatalf("labs-init:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitKeepsTheLabNetwork(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return `{"lab_role_arn":{"value":"arn:aws:iam::1:role/l"},"lab_vpc_id":{"value":"vpc-1"},"lab_subnet_ids":{"value":"subnet-a,subnet-b"}}`
		}
		return fakeAWS(cmd)
	}}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, `-var lab_vpc_id=vpc-1 -var lab_subnet_ids=["subnet-a","subnet-b"]`) < 0 {
		t.Fatalf("a re-run without the flags keeps the network:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestLabsInitNeedsBothVPCAndSubnets(t *testing.T) {
	r := &recorder{output: fakeAWS}
	if err := r.ops(t.TempDir()).LabsInit(context.Background(), "il-central-1", "", 0, nil, nil, LabNet{VPCID: "vpc-1"}); err == nil {
		t.Fatal("a VPC without subnets must be refused")
	}
	if indexOf(r.calls, "labs apply") >= 0 {
		t.Fatal("nothing may be applied")
	}
}
```

- [ ] **Step 2: Run the tests and see them fail.**

Run: `go test ./internal/awsops`
Expected: FAIL to compile: `undefined: InitOpts`, `undefined: LabNet`, wrong argument count for `Init` / `LabsInit`.

- [ ] **Step 3: Implement `Init`.** In `internal/awsops/awsops.go` add `"cmp"` to the imports and replace `Init` with:

```go
// InitOpts are `crucible aws init`'s flags. An empty Access, Domain or LocalPort keeps the stack's current value (a
// new stack: public, no domain, 18080), so a re-run never flips an ssm install back to public.
type InitOpts struct {
	Region, Domain, Access string
	LocalPort              int
}

func (o Ops) Init(ctx context.Context, in InitOpts) error {
	d := o.dir("persistent")
	if err := o.Exec(ctx, o.Root, "terraform", "-chdir="+d, "init"); err != nil {
		return err
	}
	cur, _ := o.outputs(ctx, "persistent") // nil on a new stack
	access, domain, port := cmp.Or(in.Access, cur["access"], "public"), cmp.Or(in.Domain, cur["domain"]), in.LocalPort
	if port == 0 {
		port, _ = strconv.Atoi(cur["local_port"])
	}
	if port == 0 {
		port = 18080
	}
	switch {
	case access != "public" && access != "ssm":
		return fmt.Errorf("--access must be public or ssm, not %q", access)
	case access == "public" && domain == "":
		return errors.New("--access public needs --domain (Crucible's public hostname)")
	case access == "ssm" && in.Domain != "":
		return errors.New("--domain is only for --access public: an ssm install is reached at http://localhost:<port>")
	case port < 1024 || port > 65535:
		return fmt.Errorf("--local-port %d: use a port from 1024 to 65535", port)
	}
	args := []string{"-chdir=" + d, "apply", "-var", "region=" + in.Region, "-var", "access=" + access, "-var", "local_port=" + strconv.Itoa(port)}
	if access == "public" {
		args = append(args, "-var", "domain="+domain)
	}
	return o.Exec(ctx, o.Root, "terraform", args...)
}
```

- [ ] **Step 4: Implement `mainVars`, `labVars`, `LabsInit`.** Replace `mainVars` with:

```go
// mainVars passes the persistent stack's outputs (buckets, Cognito, access) into the main stack. A persistent stack
// made before access existed has no access or public_url: main then stays public.
func mainVars(p map[string]string) []string {
	v := []string{"-var", "region=" + p["region"], "-var", "data_bucket=" + p["data_bucket"],
		"-var", "oidc_issuer=" + p["oidc_issuer"], "-var", "oidc_client_id=" + p["oidc_client_id"],
		"-var", "cognito_user_pool_id=" + p["cognito_user_pool_id"]}
	for _, k := range []string{"access", "public_url"} {
		if p[k] != "" {
			v = append(v, "-var", k+"="+p[k])
		}
	}
	return v
}
```

In `labVars`, append to the returned slice: `"-var", "lab_vpc_id=" + l["lab_vpc_id"], "-var", "lab_subnet_ids=" + l["lab_subnet_ids"]`.

Add above `LabsInit`:

```go
// LabNet is where lab modules launch when the account has no default VPC (labs-init --vpc-id --subnet-ids).
type LabNet struct {
	VPCID     string
	SubnetIDs []string
}

// commaList splits a comma-separated output, dropping blanks.
func commaList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
```

Change `LabsInit`'s signature to end with `allowedRegions []string, net LabNet) error`, document `net` in its comment ("net, both or neither, sets the lab network; empty keeps the stack's current one"), and insert before the `// The roles trust the node role by exact ARN` comment:

```go
	vpc, subnets := net.VPCID, net.SubnetIDs
	if vpc == "" && len(subnets) == 0 { // keep the stack's lab network
		vpc, subnets = cur["lab_vpc_id"], commaList(cur["lab_subnet_ids"])
	}
	if (vpc == "") != (len(subnets) == 0) {
		return errors.New("--vpc-id and --subnet-ids go together: pass both, or neither to keep the current lab network")
	}
	if vpc != "" {
		b, _ := json.Marshal(subnets) // a JSON list is an HCL list
		args = append(args, "-var", "lab_vpc_id="+vpc, "-var", "lab_subnet_ids="+string(b))
	}
```

- [ ] **Step 5: Implement the CLI.** In `cmd/crucible/main.go`:

Usage lines become:

```
  crucible aws init --region REGION (--domain HOSTNAME | --access ssm [--local-port 18080])
  crucible aws labs-init --region REGION [--allowed-regions R1,R2] [--crucible-account ID] [--budget-usd N] [--budget-email ADDR]... [--vpc-id VPC --subnet-ids S1,S2]
```

and add to the usage's explanation paragraph:

```
init --access ssm makes an install with no way in: no domain, no Elastic IP, no Let's Encrypt; everyone reaches it at
http://localhost:<--local-port> through `crucible aws connect`. A re-run without --access, --domain or --local-port
keeps the current ones. labs-init --vpc-id and --subnet-ids (both or neither) are where lab modules launch, for an
account without a default VPC; a re-run without them keeps the current ones.
```

Add flags after `domain`:

```go
	access := fs.String("access", "", "init: public (a domain, an Elastic IP, Let's Encrypt) or ssm (no way in; `crucible aws connect`); default: keep the current one, else public")
	localPort := fs.Int("local-port", 0, "init, access ssm: the laptop port everyone opens Crucible on (default: keep the current one, else 18080)")
	vpcID := fs.String("vpc-id", "", "labs-init: the VPC lab modules launch into (var.crucible_vpc_id), for an account without a default VPC")
	subnetIDs := fs.String("subnet-ids", "", "labs-init: comma-separated subnets in that VPC (var.crucible_subnet_ids)")
```

Replace the `init` case with:

```go
	case "init":
		if *region == "" {
			fmt.Fprintln(os.Stderr, "--region is required")
			return 2
		}
		err = ops.Init(ctx, awsops.InitOpts{Region: *region, Domain: *domain, Access: *access, LocalPort: *localPort})
```

In the `labs-init` case, before the call, build the subnets and pass them:

```go
		var subnets []string
		for _, s := range strings.Split(*subnetIDs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				subnets = append(subnets, s)
			}
		}
		err = ops.LabsInit(ctx, *region, *crucibleAccount, *budgetUSD, budgetEmails, regions, awsops.LabNet{VPCID: *vpcID, SubnetIDs: subnets})
```

- [ ] **Step 6: Run the tests and see them pass.**

Run: `gofmt -l cmd internal; go vet ./internal/awsops ./cmd/crucible && go test ./internal/awsops ./cmd/crucible`
Expected: no gofmt output; PASS.

- [ ] **Step 7: Commit.**

```bash
git commit -m "feat(aws): init --access ssm and --local-port; up passes access on; labs-init --vpc-id --subnet-ids

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awsops/awsops.go internal/awsops/awsops_test.go cmd/crucible/main.go
```

---

### Task 7: `up`, `deploy`, `wake` and `status` in `ssm` mode

**Files:**
- Modify: `internal/awsops/awsops.go` (`Up` DNS line ~142, `Deploy` health ~line 312, `ssm` ~line 326, `Wake` ~line 430, `Status` ~line 464)
- Test: `internal/awsops/awsops_test.go`

Spec §4's wait for the node to come online in Systems Manager is `ssmOut`'s existing registration retry (40 tries, 15 s apart: 10 minutes), which now ends with the firewall message; no separate `describe-instance-information` poll is needed.

**Interfaces:**
- Consumes: main outputs `access`, `url` (Task 2).
- Produces: `func (o Ops) ssmOut(ctx context.Context, m map[string]string, command string) (string, error)`; `func (o Ops) waitLive(ctx context.Context, m map[string]string) error`; `func egressHosts(region string) []string`; `func egressCheck(hosts []string) string`; const `nodeHealth`.

- [ ] **Step 1: Write the failing tests.** Append to `internal/awsops/awsops_test.go`:

```go
// fakeSSM is fakeAWS for an access = ssm install in il-central-1.
func fakeSSM(cmd string) string {
	if strings.Contains(cmd, "output -json") {
		return `{"instance_id":{"value":"i-123"},"region":{"value":"il-central-1"},"data_bucket":{"value":"bkt"},"domain":{"value":""},"url":{"value":"http://localhost:18080"},"public_ip":{"value":""},"access":{"value":"ssm"},"public_url":{"value":"http://localhost:18080"},"state_bucket":{"value":"st"},"oidc_issuer":{"value":"https://cognito-idp.il-central-1.amazonaws.com/il-central-1_abc"},"oidc_client_id":{"value":"client123"},"cognito_user_pool_id":{"value":"il-central-1_abc"}}`
	}
	return fakeAWS(cmd)
}

func TestSSMModeDeployChecksHealthOnTheNode(t *testing.T) {
	r := &recorder{output: fakeSSM, unhealthy: true} // the laptop can't reach the node: only an on-node check can pass
	if err := r.ops(t.TempDir()).Deploy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "http://localhost/healthz") < 0 {
		t.Fatalf("health must be checked on the node over SSM:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "crucible aws connect") {
		t.Fatalf("the live message must say how to reach it:\n%s", r.log.String())
	}
}

func TestSSMModeUpPrintsNoDNSLine(t *testing.T) {
	r := &recorder{output: fakeSSM, unhealthy: true}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.log.String(), "Point an A record") || !strings.Contains(r.log.String(), "no DNS record") {
		t.Fatalf("ssm has no DNS step:\n%s", r.log.String())
	}
}

func TestSSMModeStatusReportsBlockedHosts(t *testing.T) {
	last := ""
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "send-command") {
			last = cmd
		}
		if strings.Contains(cmd, "StandardOutputContent") && strings.Contains(last, "blocked") {
			return "blocked get.k3s.io\nblocked github.com"
		}
		return fakeSSM(cmd)
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"healthz: ok", "the firewall blocks these", "blocked get.k3s.io", "blocked github.com"} {
		if !strings.Contains(r.log.String(), want) {
			t.Fatalf("status lacks %q:\n%s", want, r.log.String())
		}
	}
	for _, host := range []string{"il-central-1.ec2.archive.ubuntu.com", "ssm.il-central-1.amazonaws.com", "get.helm.sh", "production.cloudflare.docker.com"} {
		if indexOf(r.calls, host) < 0 {
			t.Fatalf("egress check must try %s:\n%s", host, strings.Join(r.calls, "\n"))
		}
	}
}

// Review focus 4: a stopped node is not retried over SSM for ten minutes.
func TestSSMModeStatusSkipsAStoppedNode(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return "stopped"
		}
		return fakeSSM(cmd)
	}}
	if err := r.ops(t.TempDir()).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "send-command") >= 0 || !strings.Contains(r.log.String(), "not checked") {
		t.Fatalf("a stopped node gets no SSM checks:\n%s\n%s", strings.Join(r.calls, "\n"), r.log.String())
	}
}

func TestUnregisteredNodeNamesTheFirewallHosts(t *testing.T) {
	r := &recorder{output: fakeAWS, outErr: func(cmd string) error {
		if strings.Contains(cmd, "send-command") {
			return errors.New("exit status 254: An error occurred (InvalidInstanceId) when calling the SendCommand operation")
		}
		return nil
	}}
	err := r.ops(t.TempDir()).Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ssmmessages.eu-west-1.amazonaws.com") || !strings.Contains(err.Error(), "network team") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests and see them fail.**

Run: `go test ./internal/awsops -run 'SSMMode|Unregistered'`
Expected: FAIL: Deploy probes `http://localhost:18080/healthz` from the laptop and fails ("isn't answering yet"); status lacks the egress lines; the unregistered error has no firewall hosts.

- [ ] **Step 3: Implement `ssmOut`.** Replace `ssm` with:

```go
// ssm runs a shell command on the node and waits for it (SSM's own wait gives up after ~100s).
func (o Ops) ssm(ctx context.Context, m map[string]string, command string) error {
	_, err := o.ssmOut(ctx, m, command)
	return err
}

// ssmOut is ssm, returning the command's standard output.
func (o Ops) ssmOut(ctx context.Context, m map[string]string, command string) (string, error) {
	params, _ := json.Marshal(map[string][]string{"commands": {command}})
	var id string
	var err error
	for attempt := 0; attempt < 40; attempt++ { // a fresh node needs a few minutes to register with SSM
		if id, err = o.Output(ctx, o.Root, "aws", "ssm", "send-command", "--region", m["region"], "--instance-ids", m["instance_id"],
			"--document-name", "AWS-RunShellScript", "--parameters", string(params), "--query", "Command.CommandId", "--output", "text"); err == nil {
			break
		}
		if !strings.Contains(err.Error(), "InvalidInstanceId") && !strings.Contains(err.Error(), "not registered") {
			return "", fmt.Errorf("ssm send-command: %w", err) // AccessDenied, ExpiredToken, …: retrying won't help
		}
		o.Sleep(15 * time.Second)
	}
	if err != nil {
		return "", fmt.Errorf("node not reachable over SSM after 10 minutes: %w\nIf it is running, its SSM agent can't reach Systems Manager: "+
			"ask the network team to allow ssm.%[2]s.amazonaws.com, ssmmessages.%[2]s.amazonaws.com and ec2messages.%[2]s.amazonaws.com through the firewall", err, m["region"])
	}
	deadline := time.Now().Add(45 * time.Minute)
	for time.Now().Before(deadline) {
		st, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
			"--instance-id", m["instance_id"], "--query", "Status", "--output", "text")
		switch st {
		case "Success":
			return o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
				"--instance-id", m["instance_id"], "--query", "StandardOutputContent", "--output", "text")
		case "Failed", "Cancelled", "TimedOut":
			msg, _ := o.Output(ctx, o.Root, "aws", "ssm", "get-command-invocation", "--region", m["region"], "--command-id", id,
				"--instance-id", m["instance_id"], "--query", "StandardErrorContent", "--output", "text")
			return "", fmt.Errorf("node command %s: %s", st, msg)
		}
		o.Sleep(5 * time.Second)
	}
	return "", errors.New("node command timed out")
}
```

- [ ] **Step 4: Implement health, the DNS line and status.** Add after `waitHealthy`:

```go
// nodeHealth waits for /healthz on the node itself: with access = ssm nothing outside the VPC can reach it.
const nodeHealth = `for i in $(seq 120); do curl -fsS --max-time 5 -o /dev/null http://localhost/healthz && exit 0; sleep 5; done; ` +
	`echo "healthz isn't answering on the node" >&2; exit 1`

// waitLive waits until Crucible answers: from this laptop for a public install, on the node over SSM for access = ssm.
func (o Ops) waitLive(ctx context.Context, m map[string]string) error {
	if m["access"] != "ssm" {
		return o.waitHealthy(ctx, m["url"])
	}
	if err := o.ssm(ctx, m, nodeHealth); err != nil {
		return fmt.Errorf("the node is up but Crucible isn't answering on it (%w): run `crucible aws status`", err)
	}
	fmt.Fprintf(o.Log, "🔥 the forge is lit: open the tunnel with `crucible aws connect`, then %s\n", m["url"])
	return nil
}

// egressHosts are what the node downloads from or calls. Behind an egress firewall each one must be allowed.
func egressHosts(region string) []string {
	return []string{"get.k3s.io", "github.com", "objects.githubusercontent.com", "get.helm.sh",
		"registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com",
		region + ".ec2.archive.ubuntu.com", "security.ubuntu.com", "awscli.amazonaws.com", "pricing.us-east-1.amazonaws.com",
		"s3." + region + ".amazonaws.com", "sts." + region + ".amazonaws.com", "ssm." + region + ".amazonaws.com",
		"cognito-idp." + region + ".amazonaws.com"}
}

// egressCheck prints "blocked <host>" for each host the node can't open an HTTPS connection to. Any HTTP answer,
// even an error status, means the firewall let it through.
func egressCheck(hosts []string) string {
	return "for h in " + strings.Join(hosts, " ") + `; do curl -sS -o /dev/null --max-time 5 "https://$h" 2>/dev/null || echo "blocked $h"; done; true`
}
```

In `Deploy` and `Wake`, replace `o.waitHealthy(ctx, m["url"])` with `o.waitLive(ctx, m)`. In `Up`, replace the `fmt.Fprintf(o.Log, "Point an A record …")` line with:

```go
	if m["access"] == "ssm" {
		fmt.Fprintln(o.Log, "access ssm: no DNS record to point; once it's deployed, reach it with `crucible aws connect`.")
	} else {
		fmt.Fprintf(o.Log, "Point an A record %s → %s now (skip if route53_zone_id is set).\n", m["domain"], m["public_ip"])
	}
```

Replace `Status` with:

```go
func (o Ops) Status(ctx context.Context) error {
	m, err := o.outputs(ctx, "main")
	if err != nil {
		return err
	}
	st, err := o.state(ctx, m)
	if err != nil {
		st = "unknown (" + err.Error() + ")"
	}
	if m["access"] != "ssm" {
		health := "ok"
		if err := o.healthy(m["url"]); err != nil {
			health = "not answering (" + err.Error() + ")"
		}
		fmt.Fprintf(o.Log, "instance %s: %s\nurl: %s\nhealthz: %s\n", m["instance_id"], st, m["url"], health)
		return nil
	}
	fmt.Fprintf(o.Log, "instance %s: %s\nurl: %s (through `crucible aws connect`)\n", m["instance_id"], st, m["url"])
	if st != "running" {
		fmt.Fprintln(o.Log, "healthz and egress: not checked (the node isn't running)")
		return nil
	}
	health := "ok"
	if _, err := o.ssmOut(ctx, m, "curl -fsS --max-time 5 -o /dev/null http://localhost/healthz"); err != nil {
		health = "not answering (" + err.Error() + ")"
	}
	fmt.Fprintf(o.Log, "healthz: %s\n", health)
	out, err := o.ssmOut(ctx, m, egressCheck(egressHosts(m["region"])))
	switch {
	case err != nil:
		fmt.Fprintf(o.Log, "egress: not checked (%v)\n", err)
	case strings.TrimSpace(out) == "":
		fmt.Fprintln(o.Log, "egress: every host the node needs is reachable (check your git host by hand)")
	default:
		fmt.Fprintf(o.Log, "egress: the firewall blocks these, ask the network team to allow them:\n%s\n", strings.TrimSpace(out))
	}
	return nil
}
```

- [ ] **Step 5: Run the tests and see them pass.**

Run: `gofmt -l internal; go vet ./internal/awsops && go test ./internal/awsops`
Expected: PASS, including the existing `TestSSMRetriesOnlyWhileUnregistered` and `TestUnhealthyMessagesPointAtStatus`.

- [ ] **Step 6: Commit.**

```bash
git commit -m "feat(aws): access ssm checks health and egress on the node over SSM; no DNS step

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awsops/awsops.go internal/awsops/awsops_test.go
```

---

### Task 8: `crucible aws connect`

**Files:**
- Modify: `internal/awsops/awsops.go` (`Ops` struct, `Default`, new `Connect`)
- Test: `internal/awsops/awsops_test.go` (`recorder` gains `noPlugin`)
- Modify: `cmd/crucible/main.go` (usage, flags, `connect` case)

**Interfaces:**
- Consumes: the node's tags `Name`, `crucible:public-url` (Task 2).
- Produces: `awsops.ConnectOpts{Region, Name string; Port int}`; `func (o Ops) Connect(ctx context.Context, in ConnectOpts) error`; `Ops.LookPath func(string) (string, error)` (nil: `exec.LookPath`).

- [ ] **Step 1: Write the failing tests.** In `internal/awsops/awsops_test.go`, add `noPlugin bool // session-manager-plugin is not installed` to `recorder`, and in `recorder.ops` add to the returned `Ops`:

```go
		LookPath: func(string) (string, error) {
			if r.noPlugin {
				return "", errors.New("not found")
			}
			return "/usr/local/bin/session-manager-plugin", nil
		},
```

Append:

```go
// fakeNode answers connect's describe-instances with these "<id>\t<public-url tag>" lines.
func fakeNode(lines string) func(string) string {
	return func(cmd string) string {
		if strings.Contains(cmd, "describe-instances") {
			return lines
		}
		return fakeAWS(cmd)
	}
}

func TestConnectForwardsTheNodesPort(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "Name=tag:Name,Values=crucible Name=instance-state-name,Values=running") < 0 {
		t.Fatalf("connect finds the running node by its Name tag:\n%s", strings.Join(r.calls, "\n"))
	}
	if indexOf(r.calls, "aws ssm start-session --region il-central-1 --target i-0abc --document-name AWS-StartPortForwardingSession --parameters portNumber=80,localPortNumber=18080") < 0 {
		t.Fatalf("start-session:\n%s", strings.Join(r.calls, "\n"))
	}
	if !strings.Contains(r.log.String(), "http://localhost:18080") {
		t.Fatalf("connect says where Crucible is:\n%s", r.log.String())
	}
}

func TestConnectRefusesAnotherPort(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1", Port: 9000})
	if err == nil || !strings.Contains(err.Error(), "18080") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("a different port breaks sign-in: %v", err)
	}
}

func TestConnectRefusesAPublicInstall(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttps://c.example.com")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "eu-west-1"})
	if err == nil || !strings.Contains(err.Error(), "open https://c.example.com") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("a public install needs no tunnel: %v", err)
	}
}

func TestConnectNeedsExactlyOneNode(t *testing.T) {
	r := &recorder{output: fakeNode("")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err == nil || !strings.Contains(err.Error(), "crucible aws wake") {
		t.Fatalf("no node: %v", err)
	}
	r = &recorder{output: fakeNode("i-1\thttp://localhost:18080\ni-2\thttp://localhost:18080")}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"}); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("two nodes: %v", err)
	}
}

// Review focus 3: a node made before this change has no crucible:public-url tag.
func TestConnectNeedsTheTag(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\tNone")}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "crucible aws up") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("an untagged node: %v", err)
	}
}

func TestConnectNeedsThePlugin(t *testing.T) {
	r := &recorder{output: fakeNode("i-0abc\thttp://localhost:18080"), noPlugin: true}
	err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{Region: "il-central-1"})
	if err == nil || !strings.Contains(err.Error(), "session-manager-plugin") || indexOf(r.calls, "start-session") >= 0 {
		t.Fatalf("missing plugin: %v", err)
	}
}

func TestConnectRegion(t *testing.T) {
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "configure get region") {
			return "il-central-1"
		}
		return fakeNode("i-0abc\thttp://localhost:18080")(cmd)
	}}
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{}); err != nil || indexOf(r.calls, "describe-instances --region il-central-1") < 0 {
		t.Fatalf("region from the AWS CLI's config: %v\n%s", err, strings.Join(r.calls, "\n"))
	}
	r = &recorder{output: fakeAWS} // fakeAWS answers "configure get region" with ""
	if err := r.ops(t.TempDir()).Connect(context.Background(), ConnectOpts{}); err == nil || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("no region anywhere: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests and see them fail.**

Run: `go test ./internal/awsops -run Connect`
Expected: FAIL to compile: `unknown field LookPath`, `undefined: ConnectOpts`, `r.ops(…).Connect undefined`.

- [ ] **Step 3: Implement.** In `internal/awsops/awsops.go`, add to `Ops` after `Get`:

```go
	LookPath func(file string) (string, error) // finds session-manager-plugin; nil = exec.LookPath
```

add `LookPath: exec.LookPath,` to `Default`'s returned `Ops`, and add:

```go
// ConnectOpts are `crucible aws connect`'s flags.
type ConnectOpts struct {
	Region, Name string
	Port         int
}

// Connect opens a Session Manager port-forward from this laptop to the node's port 80, for access = ssm. It finds the
// node by its Name tag, so it needs no Terraform state: a colleague needs only the crucible binary, the AWS CLI, the
// Session Manager plugin and our SSO login. It runs in the foreground until Ctrl-C.
func (o Ops) Connect(ctx context.Context, in ConnectOpts) error {
	region := in.Region
	if region == "" {
		region, _ = o.Output(ctx, o.Root, "aws", "configure", "get", "region") // empty when none is set
	}
	if region == "" {
		return errors.New("no region: pass --region (or set one with `aws configure set region …`)")
	}
	name := cmp.Or(in.Name, "crucible")
	out, err := o.Output(ctx, o.Root, "aws", "ec2", "describe-instances", "--region", region,
		"--filters", "Name=tag:Name,Values="+name, "Name=instance-state-name,Values=running",
		"--query", "Reservations[].Instances[].[InstanceId,Tags[?Key==`crucible:public-url`]|[0].Value]", "--output", "text")
	if err != nil {
		return fmt.Errorf("finding the node: %w", err)
	}
	var nodes []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			nodes = append(nodes, l)
		}
	}
	switch {
	case len(nodes) == 0:
		return fmt.Errorf("no running instance named %s in %s: if it's asleep, run `crucible aws wake`", name, region)
	case len(nodes) > 1:
		return fmt.Errorf("%d running instances are named %s in %s (%s): pass --name for the one you want", len(nodes), name, region, strings.Join(nodes, "; "))
	}
	f := strings.Fields(nodes[0])
	id, url := f[0], ""
	if len(f) > 1 && f[1] != "None" {
		url = f[1]
	}
	switch {
	case url == "":
		return fmt.Errorf("%s has no crucible:public-url tag: run `crucible aws up` with this version of Crucible first", id)
	case !strings.HasPrefix(url, "http://localhost:"):
		return fmt.Errorf("this install has a public URL: open %s in a browser", url)
	}
	port, err := strconv.Atoi(strings.TrimPrefix(url, "http://localhost:"))
	if err != nil {
		return fmt.Errorf("%s's crucible:public-url %q isn't http://localhost:<port>", id, url)
	}
	if in.Port != 0 && in.Port != port {
		return fmt.Errorf("crucible's address is %s: a different port (%d) breaks sign-in and saving, so use --port %d or leave it out", url, in.Port, port)
	}
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look("session-manager-plugin"); err != nil {
		return errors.New("the AWS Session Manager plugin isn't installed (session-manager-plugin): " +
			"https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html")
	}
	fmt.Fprintf(o.Log, "Crucible is at %s while this runs; Ctrl-C closes it.\n", url)
	return o.Exec(ctx, o.Root, "aws", "ssm", "start-session", "--region", region, "--target", id,
		"--document-name", "AWS-StartPortForwardingSession", "--parameters", fmt.Sprintf("portNumber=80,localPortNumber=%d", port))
}
```

In `cmd/crucible/main.go`, add the usage line after `crucible aws deploy | snapshot | wake | status`:

```
  crucible aws connect [--region REGION] [--port PORT] [--name crucible]
```

and to the explanation: ``connect opens the tunnel to an --access ssm install (AWS CLI, Session Manager plugin and your SSO login needed) and keeps it open until Ctrl-C.`` Add flags:

```go
	name := fs.String("name", "", "connect: the node's Name tag (default crucible)")
	port := fs.Int("port", 0, "connect: the local port (default: the one in Crucible's address; another one breaks sign-in)")
```

and a case before `default:`:

```go
	case "connect":
		err = ops.Connect(ctx, awsops.ConnectOpts{Region: *region, Name: *name, Port: *port})
```

- [ ] **Step 4: Run the tests and see them pass.**

Run: `gofmt -l cmd internal; go vet ./internal/awsops ./cmd/crucible && go test ./internal/awsops ./cmd/crucible && go build -o /dev/null ./cmd/crucible`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git commit -m "feat(aws): crucible aws connect opens the SSM tunnel on the port Crucible's address uses

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awsops/awsops.go internal/awsops/awsops_test.go cmd/crucible/main.go
```

---

### Task 9: Docs: the real steps replace "not made yet"

**Files:**
- Modify: `docs/aws-deploy.html`, `docs/deploy-architecture.html`, `docs/ssm-access.html`, `docs/test-guide.html`, `docs/aws-primer.html`
- Modify: `docs/runbooks/aws.md`, `CLAUDE.md`

**Interfaces:**
- Consumes: every command, flag and output from Tasks 1–8, exactly as built: `crucible aws init --region il-central-1 --access ssm [--local-port 18080]`; `subnet_id` in `deploy/aws/main/crucible.tfvars`; `crucible aws up`; `crucible aws status` (healthz and egress over SSM); `crucible aws connect [--region] [--port] [--name]`; `crucible aws labs-init --region il-central-1 --vpc-id vpc-03aa39198d92c5a25 --subnet-ids subnet-0fa66f46aa023ab6e,subnet-08e721302b446603f`; `var.crucible_vpc_id`, `var.crucible_subnet_ids`.

Keep each page's look and section ids; plain, short sentences; no em-dash asides. Each item below says what must be true after the step.

- [ ] **Step 1: `docs/aws-deploy.html`.**
  - `#our-account`'s "Before this works here" list becomes "Our steps", in order: `init --access ssm`; `subnet_id = "subnet-0fa66f46aa023ab6e"` (and no `domain`/`acme_email`) in `crucible.tfvars`; `up` (no DNS line; it waits for SSM and checks health on the node); `status` and what its egress report means (blocked hosts go to the network team; the git host is checked by hand); `connect`; `labs-init` with `--vpc-id` and `--subnet-ids`.
  - Every "In our account" callout that says a change is not made yet now gives the real command or behaviour instead. The init section documents `--access` and `--local-port` and the keep-current re-run rule; the labs section documents `--vpc-id`/`--subnet-ids` (both or neither, kept on re-run); troubleshooting covers "node not reachable over SSM" (the three SSM hosts to allow) and `connect`'s refusals (another port, no tag, no plugin).
  - The table of contents still matches the sections.
- [ ] **Step 2: `docs/deploy-architecture.html`.**
  - The `#main-changes` note is replaced by a short description of the `access` setting (public | ssm, where it lives, the one public URL and who reads it), with the `#main-changes` id kept on it so links survive.
  - `#access-ssm` shows `crucible aws connect` (finds the node by tag, enforces the port) and the on-node health and egress checks.
  - Production and components describe both modes: `public` (Elastic IP, Let's Encrypt) and `ssm` (existing subnet, no inbound rules, plain-HTTP Traefik, node tag `crucible:public-url`).
  - The AWS lab section and the components table describe the lab network: `labs-init --vpc-id --subnet-ids` → labs outputs → `up` → Helm → `CRUCIBLE_AWS_LAB_VPC_ID`/`_SUBNET_IDS` → `var.crucible_vpc_id`/`var.crucible_subnet_ids`, filled only in the lab network's region.
- [ ] **Step 3: `docs/ssm-access.html`.** The connect script is replaced by `crucible aws connect` (what it needs installed, what it prints, Ctrl-C to close, `--port` refusal); the caveats that the app changes aren't made yet are removed; the firewall note names `crucible aws status`'s egress report and the "not reachable over SSM" message.
- [ ] **Step 4: `docs/test-guide.html`.** Step 9b uses `init --access ssm`, `subnet_id`, `up`, `status`, `connect` and `labs-init --vpc-id --subnet-ids`; the `aw-ec2` note uses the portable form (`length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null`, `var.crucible_vpc_id != "" ? var.crucible_vpc_id : null`, `associate_public_ip_address = false`) instead of hard-coded ids. Checklist ids unchanged.
- [ ] **Step 5: `docs/aws-primer.html`.** Only the "In our account" notes that say the deploy changes are pending: they now point to `crucible aws connect` and `aws-deploy.html#our-account`.
- [ ] **Step 6: `docs/runbooks/aws.md`.** Add an "Our account (access = ssm)" section with the same ordered steps as Step 1 and the by-hand checks from the spec §7: `init --access ssm` (first contact with the unverified S3, Cognito and SSM actions; if one is denied by an SCP, stop and ask the platform team), `up` with `subnet_id`, `status` egress report, `connect` and a sign-in at `http://localhost:18080`, `labs-init` with the VPC and subnets, one AWS lab that launches an instance; `schedule_enabled = false` if the Scheduler is denied; the organization's after-hours automation may stop the node. Its `ec2.tf` example uses the portable form from Task 5.
- [ ] **Step 7: `CLAUDE.md`.** In the Commands table add a row: `| Reach an --access ssm install | crucible aws connect (needs the AWS CLI, the Session Manager plugin and your SSO login; keeps the tunnel open until Ctrl-C) |`, and mention `--access ssm` in the AWS docs bullet for `docs/aws-deploy.html`.
- [ ] **Step 8: Check.**

Run:

```bash
go test ./internal/docs
python3 - <<'EOF'
import re, os, html.parser
d = 'docs'
pages = {f: open(os.path.join(d, f)).read() for f in os.listdir(d) if f.endswith('.html')}
ids = {f: set(re.findall(r'\bid="([^"]+)"', s)) for f, s in pages.items()}
bad = []
for f, s in pages.items():
    for h in re.findall(r'href="([^"]+)"', s):
        if h.startswith(('http', 'mailto')):
            continue
        page, _, anc = h.partition('#'); page = page or f
        if page not in pages or (anc and anc not in ids[page]):
            bad.append((f, h))
    class P(html.parser.HTMLParser):
        void = {'meta','link','br','img','input','path','line','rect','hr','circle','polygon','polyline','ellipse','use','stop','col','source','wbr'}
        def __init__(s): super().__init__(); s.st = []; s.err = []
        def handle_starttag(s, t, a):
            if t not in s.void: s.st.append(t)
        def handle_endtag(s, t):
            if t in s.void: return
            if s.st and s.st[-1] == t: s.st.pop()
            else: s.err.append((t, s.getpos()))
    p = P(); p.feed(s)
    if p.err or p.st: bad.append((f, 'html', p.err[:2], p.st[:2]))
print('problems:', bad)
EOF
grep -n "not made yet\|aren't made yet\|not made" docs/*.html docs/runbooks/aws.md
```

Expected: `go test` PASS; `problems: []`; the final grep prints nothing.

- [ ] **Step 9: Commit.**

```bash
git commit -m "docs: our account's real steps (init --access ssm, connect, status egress, labs-init network)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs/aws-deploy.html docs/deploy-architecture.html docs/ssm-access.html docs/test-guide.html docs/aws-primer.html docs/runbooks/aws.md CLAUDE.md
```

---

## Final check (controller, after Task 9)

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
gofmt -l cmd internal
go vet ./...
go test -race ./...
(cd deploy/aws/persistent && terraform test) && (cd deploy/aws/main && terraform test) && (cd deploy/aws/labs && terraform test)
bash deploy/helm/test.sh
```

Expected: no gofmt output; every suite PASS. No e2e suite is needed: the local stack and `cluster-check` don't use `deploy/aws` or the new Helm values (the chart defaults are unchanged).
