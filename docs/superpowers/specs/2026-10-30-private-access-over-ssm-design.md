# Private access over SSM: deploying Crucible into an egress-only account

Status: **agreed** with the owner, 2026-10-30 (all sections, in chat).
Makes `deploy/aws` work in our AWS account, which the stack as built cannot deploy into, and keeps today's public
install as the other mode. Owner's decisions, 2026-10-30: add a mode rather than replace the public setup; a
`crucible aws connect` command rather than a hand-edited script; Crucible passes lab VPC and subnets to lab modules
rather than authors hard-coding them.

## Why: our account (verified 2026-10-29, read-only and `--dry-run`)

- One account (`133089468093`, `il-central-1`), an AWS Organizations landing zone; we sign in through Identity Center
  as `Workload-Admin-PS`. No default VPC.
- One spoke VPC `vpc-03aa39198d92c5a25` (`10.2.128.0/22`), built by the networking team. Workloads go in
  `spokeSubnet1`/`2` (`subnet-0fa66f46aa023ab6e`, `subnet-08e721302b446603f`). Traffic only leaves: spoke → central
  inspection firewall (GWLB endpoint) → NAT → egress internet gateway. No inbound path, no Transit Gateway attachment,
  no VPC endpoints for SSM, S3 or STS.
- SCPs **allow** `ec2:RunInstances` in the spoke subnets, security groups and `iam:CreateRole`. They **deny** Elastic
  IPs, internet gateways, load balancers (internal and internet-facing), VPC endpoints, subnets, routes and Client VPN.
- **Not verified** (no dry run exists, and the IAM policy simulator wrongly denies everything for our role): S3
  `CreateBucket`, Cognito `CreateUserPool`, SSM `PutParameter`, EventBridge Scheduler, Budgets, Data Exports,
  `ssm:StartSession`.
- What breaks today: `deploy/aws/main` looks up the default VPC, creates an Elastic IP and an optional Route 53 record,
  opens 80/443 to the internet, and has Traefik get Let's Encrypt certificates and redirect to HTTPS; the node's deploy
  script hard-codes `publicURL=https://$DOMAIN`; the Helm Ingress matches only the configured host over TLS; Cognito's
  callback and welcome email use `https://<domain>`; `crucible aws up` prints "point an A record" and `up`/`status`
  probe `https://<domain>/healthz` from the operator's laptop.

## 1. One setting: `access`

`access = "public" | "ssm"`, chosen once at `crucible aws init`:

```
crucible aws init --region il-central-1 --access ssm [--local-port 18080]
crucible aws init --region eu-west-1 --domain crucible.example.com          # access public, as today
```

- `public` (the default) behaves exactly as today and still requires `--domain`.
- `ssm` takes no domain. `--local-port` defaults to `18080`, a number in 1024–65535.
- The persistent stack stores `access` and `local_port` as variables and computes **one public URL**, exported as
  output `public_url`: `https://<domain>` for `public`, `http://localhost:<local_port>` for `ssm`. Everything else
  reads that output: the Cognito callback (`<public_url>/auth/callback`) and logout URL (`<public_url>/`), the welcome
  email's sign-in link, the main stack (passed by `crucible aws up` like the other persistent outputs), and the node's
  deploy script.
- Cognito accepts plain `http` callbacks only for `localhost`; the `ssm` URL is always `localhost`.
- The API needs no change: it already makes cookies non-`Secure` when the public URL is `http://`, and its Origin check
  compares requests with the public URL, which is why **everyone must use the same local port**.
- A later Cloudflare Tunnel becomes a third value (`cloudflare`), with the public URL on the Cloudflare hostname. It is
  not built here; nothing in this design blocks it.
- Changing `access` on an existing install is a re-run of `init` then `up`; the persistent stack's Cognito client
  updates in place.

## 2. The main stack in `ssm` mode (`deploy/aws/main`)

- New variables: `access` (from the persistent stack), `public_url` (from the persistent stack), and `subnet_id`
  (required when `access = "ssm"`, validated as `subnet-<hex>`; for us `subnet-0fa66f46aa023ab6e`). The VPC comes from
  `data "aws_subnet"`. In `public` mode `subnet_id` is optional: empty keeps today's default-VPC subnet, so the
  default-VPC data sources are read only when `subnet_id` is empty.
- `domain`, `acme_email` and `route53_zone_id` are needed only in `public` mode (validation: required there, ignored in
  `ssm`).
- **No Elastic IP and no Route 53 record** in `ssm` mode (`count = 0`).
- **Security group with no inbound rules** in `ssm` mode; outbound stays open (it then passes the central firewall).
- Instance: no public IP requested; tagged `Name = var.name` (as today) and `crucible:public-url = <public_url>`, which
  `connect` reads.
- `/<name>/env` carries `ACCESS`, `PUBLIC_URL` and `HOST` (the domain, or empty in `ssm`). The node's deploy script
  passes `--set publicURL="$PUBLIC_URL"`, `--set host="$HOST"` and `--set tls.enabled=<true|false>`.
- The boot script writes Traefik's Let's Encrypt resolver and HTTP→HTTPS redirect only in `public` mode; in `ssm` mode
  Traefik keeps k3s's defaults (plain HTTP on 80 through ServiceLB).
- Outputs: `access` (new), `url` (= `public_url`), `public_ip` (empty in `ssm`), the rest unchanged.
- Existing `public` nodes keep working: the node's scripts are written at first boot (`user_data` is ignored after
  that), and today's deploy script ignores the new env keys and keeps building `https://$DOMAIN`, which is the same
  URL. New nodes get the new script.

## 3. Helm (`deploy/helm/crucible`)

- `tls.enabled` (default `true`). When `false`, the Ingress uses entrypoint `web`, no TLS and no cert resolver.
- `host` may be empty: the Ingress rule then has no `host`, so any `Host` header matches (through the tunnel the
  browser sends `Host: localhost:18080`).
- `tls.enabled: false` with a non-`http://` public URL, or `true` with an `http://` one, fails the render (a mismatch
  breaks sign-in).

## 4. The CLI (`internal/awsops`, `cmd/crucible`)

- `init --access`, `--local-port` as in §1; `--domain` required only for `public`.
- `up` in `ssm` mode: no "point an A record" line. After the apply it waits (up to 10 minutes) for the node to appear
  in `ssm describe-instance-information` as `Online`. If it never does, it stops with: the node never reached Systems
  Manager; ask the network team to allow `ssm.<region>.amazonaws.com`, `ssmmessages.<region>.amazonaws.com` and
  `ec2messages.<region>.amazonaws.com` through the firewall. Then it deploys and checks health **on the node over SSM**
  (`curl -fsS --max-time 5 http://localhost/healthz`), not from the laptop.
- `status` in `ssm` mode: instance state, SSM ping status, health over SSM, and an **egress check** run on the node over
  SSM: a request with a 5-second timeout to each host the node needs, listing the ones that fail. SSM ping status comes
  first; if it is not `Online`, the node checks are skipped and the three SSM hosts are named. Hosts over HTTPS:
  `get.k3s.io`, `github.com`, `objects.githubusercontent.com` (k3s and sysbox releases), `get.helm.sh`,
  `registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com` (k3s system images, Postgres),
  `awscli.amazonaws.com`, `pricing.us-east-1.amazonaws.com`, Cognito's login domain (read from the persistent stack's
  `cognito_login_url`; the API exchanges the OIDC code server-side), the AWS-lab path (`registry.terraform.io`,
  `releases.hashicorp.com`, `iam.amazonaws.com`), and in the stack's region `s3`, `sts`, `ssm`, `cognito-idp`, `ec2`,
  `tagging`, `cloudtrail`. Over plain HTTP (apt): `<region>.ec2.archive.ubuntu.com` and `security.ubuntu.com`. A failure is reported, not fatal. The git hosts of connected
  repositories are not in the list (awsops doesn't know them); the docs say to check those by hand.
- `public` mode keeps today's `up`, `status` and laptop-side health probe.
- **New `crucible aws connect [--region R] [--port P] [--name crucible]`:**
  - finds the running instance tagged `Name = <name>` (default `crucible`) with `ec2 describe-instances`; no Terraform
    state needed. Region from `--region`, else the AWS CLI's configured region; none → a clear error;
  - reads the instance's `crucible:public-url` tag; if `--port` is given and differs from the URL's port, refuses
    ("Crucible's address is http://localhost:18080; a different port breaks sign-in"); with no `--port` it uses the
    URL's port;
  - refuses on a `public`-mode install ("this install has a public URL: open it in a browser");
  - none running → says so and suggests `crucible aws wake`; more than one → lists them and asks for `--name`;
  - checks that the Session Manager plugin is on `PATH` (`session-manager-plugin`), else prints where to get it;
  - runs `aws ssm start-session --target <id> --document-name AWS-StartPortForwardingSession --parameters
    portNumber=80,localPortNumber=<port>` in the foreground, after printing "Crucible is at http://localhost:<port>
    while this runs; Ctrl-C closes it". It does not open a browser.
- Usage text and `CLAUDE.md`'s command table gain `connect` and the new `init` flags.

## 5. Lab networking

- `crucible aws labs-init` gains `--vpc-id` and `--subnet-ids` (comma-separated). Both or neither; validated as
  `vpc-<hex>` / `subnet-<hex>`. The labs stack checks with a precondition that every subnet is in that VPC and in the
  stack's region. A re-run without them keeps the current values (like the budget flags).
- New labs stack outputs `lab_vpc_id`, `lab_subnet_ids` (comma-separated). `crucible aws up` passes them to the main
  stack (`lab_vpc_id`, `lab_subnet_ids`), which puts them in the Helm values (`awsLabs.vpcId`, `awsLabs.subnetIds`); the
  chart sets `CRUCIBLE_AWS_LAB_VPC_ID` and `CRUCIBLE_AWS_LAB_SUBNET_IDS` on the API.
- `content.LabTF` declares two more variables and `LabTFVars` fills them for every lab and every estimate:
  ```hcl
  variable "crucible_vpc_id" { type = string }        # "" when not configured
  variable "crucible_subnet_ids" { type = list(string) } # [] when not configured
  ```
  They are filled only for labs in the stack's own region (the one the subnets are in); a lab in another allowed region
  gets `""` and `[]`.
- The portable form for authors, used in the docs, the runbook's `ec2.tf` and any example that launches an instance:
  ```hcl
  subnet_id = length(var.crucible_subnet_ids) > 0 ? var.crucible_subnet_ids[0] : null
  vpc_id    = var.crucible_vpc_id != "" ? var.crucible_vpc_id : null   # on aws_security_group
  ```
  `null` falls back to the default VPC in accounts that have one.
- No IAM change: the lab role already launches into any subnet and creates security groups in any VPC, with its own
  tags. Restricting it to the configured subnets is a possible later hardening, not part of this.
- For us: `crucible aws labs-init --region il-central-1 --vpc-id vpc-03aa39198d92c5a25 --subnet-ids
  subnet-0fa66f46aa023ab6e,subnet-08e721302b446603f` (no `--crucible-account`: same account).

## 6. What doesn't change

The `public` path's behaviour; the lab IAM fence; backups and restore; the API's code (beyond reading the two lab
network variables into its lab config); the release pipeline (`crucible aws deploy` already runs over SSM).

## 7. Testing

- **Terraform (mock providers):** persistent: `ssm` gives `public_url = http://localhost:18080` and that callback;
  `public` unchanged; `ssm` with a domain is ignored, `public` without one is refused; a port out of range is refused.
  main: `ssm` creates no `aws_eip` or `aws_route53_record`, the security group has no ingress, the instance uses
  `subnet_id` and carries the `crucible:public-url` tag; `ssm` without `subnet_id` is refused; `public` plans as today.
  labs: the VPC/subnet variables, the both-or-neither rule and the outputs.
- **Helm (`deploy/helm/test.sh`):** `tls.enabled=false` renders an Ingress on `web` with no TLS; empty `host` renders a
  rule without `host`; the URL/TLS mismatch fails the render.
- **Go:** awsops with the existing fakes: `init` passes `access`/`local_port`/`domain` correctly per mode and refuses
  bad combinations; `up` in `ssm` mode waits for SSM, deploys and checks health over SSM, prints no 'point an A record' line, and stops
  with the firewall message when SSM never comes online; `status` reports health and the egress check over SSM;
  `connect` finds the instance by tag, enforces the port, refuses public installs, handles none/several instances and a
  missing plugin, and builds the exact `start-session` call. `content`/`labs`: `LabTFVars` with and without network
  values, region mismatch gives empty values, estimates get them too.
- **Docs:** `go test ./internal/docs` (docs/user pages).
- **By hand (the owner, real AWS):** `init --access ssm` (first contact with the unverified S3/Cognito/SSM actions);
  `up` with `subnet_id`; `status` egress report; `connect` and a sign-in at `http://localhost:18080`; `labs-init` with
  the VPC and subnets; one AWS lab that launches an instance. Listed in the runbook. If the Scheduler is denied, set
  `schedule_enabled = false`; the organization's after-hours automation may stop the node regardless.

## 8. Docs to update in the same change

- `docs/aws-deploy.html`: the `#our-account` section and its "Before this works here" list become the real steps
  (`init --access ssm`, `subnet_id` in `crucible.tfvars`, `up`, `status` and its egress report, `connect`,
  `labs-init` with the VPC and subnets); the "In our account" callouts drop "not made yet".
- `docs/deploy-architecture.html`: `#main-changes` is replaced by the `access` mode description; `#access-ssm` shows the
  mode, `connect` and the egress check; production and components show both modes; lab networking in the AWS lab
  section and the components.
- `docs/ssm-access.html`: the connect script becomes `crucible aws connect`; the caveats that the changes aren't made
  are removed.
- `docs/test-guide.html`: step 9b uses `init --access ssm`, `connect`, and the lab subnet variables; the `ec2.tf` note
  uses the portable form.
- `docs/runbooks/aws.md`: the same steps and the by-hand checks above; its `ec2.tf` uses the portable form.
- `docs/user/authors/aws-labs.md`: `var.crucible_vpc_id` and `var.crucible_subnet_ids`, with the portable form.
- `docs/aws-primer.html`: only the "In our account" notes that mention the pending deploy changes.
- `CLAUDE.md`: the `connect` command and the `init` flags in the command table.
