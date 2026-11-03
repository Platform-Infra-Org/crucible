# OS-level AWS Labs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AWS labs that work on a lab EC2 machine's OS: SSM shells, opt-in plain SSH, the EC2 serial console (GRUB, Windows SAC), console screenshots, and a per-lab AWS console sign-in link.

**Architecture:** Every path is an AWS API call made with the lab's own tag-scoped session (ABAC on `crucible:lab-id`). The labs stack adds a shared, minimal lab instance role and the lab-role permissions; Crucible wires the instance profile into lab modules, opens TCP 22 in a lab's NetworkPolicy only when the lab sets `aws.direct_ssh` (never to the node), and mints console sign-in URLs. A new workspace image carries `ssh`, the Session Manager plugin and two helpers.

**Tech Stack:** Terraform (mock-provider `terraform test`), Go 1.26 (client-go, AWS SDK v2), Helm, React 19 + Vite, bash.

**Spec:** `docs/superpowers/specs/2026-11-04-os-level-aws-labs-design.md`

## Global Constraints

- Never create real AWS resources. Terraform only with mock providers (`terraform test`, `validate`); Go AWS code against `awscloud.Fake` or `httptest`.
- Prefix every build/test shell with `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`.
- Tests first: write the test, run it, see it fail for the right reason, then implement.
- Commits: `git commit -m "…" -- <paths>` (pathspec form; `git add` new files first). Never `git add -A`, never rewrite history. Trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Any capability change updates `docs/user` in the same commit; `go test ./internal/docs` must pass.
- UI work uses the `crucible-ui` skill (`.claude/skills/crucible-ui/SKILL.md`): tokens only, `button.ghost` for secondary actions, copy from the user's side.
- The lab NetworkPolicy keeps today's exact shape for every lab without `aws.direct_ssh: true`.
- The lab instance role never gets `ssm:GetParameter*` and carries an explicit Deny on `arn:aws:ssm:*:*:parameter/<name>/*`.
- `ssm:StartSession` is allowed only with `SSM-SessionManagerRunShell`, `AWS-StartSSHSession`, `AWS-StartPortForwardingSession`; the boundary denies every other document.
- The console sign-in URL and token are never logged, stored or put in the audit detail.

## Review Focus

1. **Root on a lab instance reads Crucible's SSM parameters** (spec §3.1) → Task 1 asserts the instance policy has no `ssm:GetParameter*` allow and has the Deny.
2. **A session document that forwards past the instance** (`AWS-StartPortForwardingSessionToRemoteHost`) → Task 2 asserts the boundary denies every document but the three.
3. **A `direct_ssh` lab reaches the node** → Task 5 asserts the node's /32 is in `except` on the subnet that contains it, and that a missing node IP or CIDR list refuses to start.
4. **Someone other than the lab's trainee gets a console link, or the URL lands in a log/audit** → Task 6 asserts 404 for another user, and that the audit detail and the runner log never contain the token.
5. **A labs stack from before this change** (no `lab_instance_profile`, no `lab_subnet_cidrs` outputs) → Task 3 asserts `up` still works and passes empty values; Task 4 asserts `crucible_instance_profile` is `""` then.

## Rulings made while planning

- Spec §4.1 says lint "keeps refusing any other IAM": there is no such lint today; the permission boundary has no `iam:*` except `iam:PassRole` on the lab instance role, so any other IAM in a module fails at apply. No lint change. Cost if wrong: an author learns at apply instead of at lint.
- Spec §4.2 asks for a 15-minute session for the console link; the plan reuses `Cloud.AssumeLab` (one hour, the lab's own session tags) rather than adding a second assume path. The sign-in link itself still expires in 15 minutes, and the console session is no stronger than the workspace's credentials. Cost if wrong: a console tab stays signed in up to an hour instead of 15 minutes.
- The existing labs test `no IAM for lab roles` contradicts spec §3.2 (PassRole). Task 2 replaces it with "the only IAM action is `iam:PassRole`, on the lab instance role, to EC2".

---

### Task 1: Labs stack: lab instance role, profile, serial console, new outputs

**Files:**
- Modify: `deploy/aws/labs/main.tf` (new resources after `aws_iam_role_policy.plan`; outputs at the end)
- Modify: `deploy/aws/labs/labs.tftest.hcl`

**Interfaces:**
- Produces: outputs `lab_instance_profile` (string, the profile name, `"<name>-lab-instance"`), `lab_instance_role_arn` (string), `lab_subnet_cidrs` (comma-joined CIDRs of `var.lab_subnet_ids`, `""` when none); variable `serial_console` (bool, default true).

- [ ] **Step 1: Write the failing tests** (append to `deploy/aws/labs/labs.tftest.hcl`)

```hcl
run "lab_instances_get_a_minimal_ssm_role" {
  command = apply

  assert {
    condition     = aws_iam_instance_profile.lab_instance.name == "crucible-lab-instance" && output.lab_instance_profile == "crucible-lab-instance"
    error_message = "one shared instance profile, named for the stack"
  }
  assert {
    condition     = jsondecode(aws_iam_role.lab_instance.assume_role_policy).Statement[0].Principal.Service == "ec2.amazonaws.com"
    error_message = "only EC2 assumes the lab instance role"
  }
  assert {
    condition     = !anytrue([for s in jsondecode(aws_iam_role_policy.lab_instance.policy).Statement : anytrue([for a in flatten([s.Action]) : startswith(a, "ssm:GetParameter") || a == "ssm:*"]) if s.Effect == "Allow"])
    error_message = "lab instances must never read SSM parameters (Crucible's secrets live there)"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab_instance.policy).Statement : s if s.Sid == "NeverCrucibleParameters"]).Effect == "Deny" && one([for s in jsondecode(aws_iam_role_policy.lab_instance.policy).Statement : s if s.Sid == "NeverCrucibleParameters"]).Resource == "arn:aws:ssm:*:*:parameter/crucible/*"
    error_message = "an explicit deny on /<name>/* parameters"
  }
  assert {
    condition     = !strcontains(file("${path.module}/main.tf"), "AmazonSSMManagedInstanceCore")
    error_message = "no managed SSM policy (AmazonSSMManagedInstanceCore grants ssm:GetParameter)"
  }
  assert {
    condition     = aws_ec2_serial_console_access.labs[0].enabled
    error_message = "serial console access is on by default"
  }
}

run "serial_console_can_be_left_off" {
  command = plan
  variables {
    serial_console = false
  }
  assert {
    condition     = length(aws_ec2_serial_console_access.labs) == 0
    error_message = "serial_console = false leaves the account setting alone"
  }
}

run "lab_subnet_cidrs_reach_the_outputs" {
  command = apply
  variables {
    lab_vpc_id     = "vpc-0abc"
    lab_subnet_ids = ["subnet-0a", "subnet-0b"]
  }
  assert {
    condition     = length(split(",", output.lab_subnet_cidrs)) == 2
    error_message = "one CIDR per lab subnet, comma-joined"
  }
}

run "no_lab_subnet_cidrs_by_default" {
  command = plan
  assert {
    condition     = output.lab_subnet_cidrs == ""
    error_message = "no lab network, no CIDRs"
  }
}
```

Also add to the first `mock_provider "aws"` block: `mock_data "aws_subnet" { defaults = { vpc_id = "vpc-0abc", cidr_block = "10.2.129.0/24" } }` (replace the existing `aws_subnet` mock, keeping `vpc_id`), and `mock_resource "aws_iam_instance_profile" { defaults = { arn = "arn:aws:iam::444455556666:instance-profile/crucible-lab-instance" } }`. The `aws_iam_role` mock already returns a fixed ARN; that is fine.

- [ ] **Step 2: Run to verify they fail**

Run: `cd deploy/aws/labs && terraform test`
Expected: FAIL: `Reference to undeclared resource "aws_iam_instance_profile" "lab_instance"` (and the others).

- [ ] **Step 3: Implement** (in `deploy/aws/labs/main.tf`)

```hcl
variable "serial_console" {
  type        = bool
  default     = true
  description = "Turn on EC2 Serial Console access in --region (account-wide, per region): labs reach a machine's GRUB menu and boot console"
}

# Every lab's instances share this role: the SSM agent's channel and Run Command, nothing else. Written out rather than
# AmazonSSMManagedInstanceCore, which grants ssm:GetParameter(s): root on a lab instance could then read /<name>/*
# (the secret key, database password, OIDC secret) in this account.
resource "aws_iam_role" "lab_instance" {
  name = "${var.name}-lab-instance"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "lab_instance" {
  role = aws_iam_role.lab_instance.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "SessionManagerAgent", Effect = "Allow", Resource = "*",
        Action = ["ssm:UpdateInstanceInformation", "ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"] },
      { Sid = "RunCommandAgent", Effect = "Allow", Resource = "*",
        Action = ["ec2messages:AcknowledgeMessage", "ec2messages:DeleteMessage", "ec2messages:FailMessage",
      "ec2messages:GetEndpoint", "ec2messages:GetMessages", "ec2messages:SendReply"] },
      { Sid = "NeverCrucibleParameters", Effect = "Deny", Action = "ssm:GetParameter*", Resource = "arn:aws:ssm:*:*:parameter/${var.name}/*" },
    ]
  })
}

resource "aws_iam_instance_profile" "lab_instance" {
  name = "${var.name}-lab-instance"
  role = aws_iam_role.lab_instance.name
}

# Account-wide in this region: anyone allowed SendSerialConsoleSSHPublicKey reaches any instance's serial port. The
# lab role is tag-scoped; the Crucible node has no password user.
resource "aws_ec2_serial_console_access" "labs" {
  count   = var.serial_console ? 1 : 0
  enabled = true
}

output "lab_instance_profile" { value = aws_iam_instance_profile.lab_instance.name }
output "lab_instance_role_arn" { value = aws_iam_role.lab_instance.arn }
output "lab_subnet_cidrs" { value = join(",", [for id in var.lab_subnet_ids : data.aws_subnet.lab[id].cidr_block]) }
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd deploy/aws/labs && terraform fmt -check && terraform test`
Expected: PASS, all runs (21 existing + 4 new).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs-stack): a minimal shared lab instance role and profile, serial console access, subnet CIDRs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/labs/main.tf deploy/aws/labs/labs.tftest.hcl
```

---

### Task 2: Labs stack: lab role and boundary permissions for sessions, keys and consoles

**Files:**
- Modify: `deploy/aws/labs/main.tf` (`aws_iam_policy.boundary`, `aws_iam_role_policy.lab`, `locals`)
- Modify: `deploy/aws/labs/labs.tftest.hcl` (replace the `no IAM for lab roles` assertion; add a run)

**Interfaces:**
- Consumes: `aws_iam_role.lab_instance` (Task 1).
- Produces: nothing new for Go; the lab role can now pass the lab instance role, start sessions, push keys, read console screenshots, run commands, use Fleet Manager.

- [ ] **Step 1: Write the failing tests**

In `run "lab_role_is_bounded_tagged_and_short_lived"`, replace the assertion with `error_message = "no IAM for lab roles"` by:

```hcl
  assert {
    condition     = toset(flatten([for s in concat(jsondecode(aws_iam_role_policy.lab.policy).Statement, jsondecode(aws_iam_policy.boundary.policy).Statement) : [for a in flatten([lookup(s, "Action", [])]) : a if startswith(a, "iam:")] if s.Effect == "Allow"])) == toset(["iam:PassRole"])
    error_message = "the only IAM a lab has is passing the lab instance role"
  }
```

Append:

```hcl
run "lab_sessions_keys_and_consoles_stay_on_own_instances" {
  command = apply

  assert {
    condition     = one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "PassLabInstanceRole"]).Resource == aws_iam_role.lab_instance.arn && one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "PassLabInstanceRole"]).Condition.StringEquals["iam:PassedToService"] == "ec2.amazonaws.com"
    error_message = "PassRole: the lab instance role only, to EC2 only"
  }
  assert {
    condition     = alltrue([for sid in ["SessionsToOwn", "KeysToOwn", "ConsoleOfOwn", "CommandsToOwn"] : one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == sid]).Condition.StringEquals[sid == "KeysToOwn" || sid == "ConsoleOfOwn" ? "aws:ResourceTag/crucible:lab-id" : "ssm:resourceTag/crucible:lab-id"] == "$${aws:PrincipalTag/crucible:lab-id}"])
    error_message = "every new instance action carries the session's lab id"
  }
  assert {
    condition     = toset([for r in one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "SessionDocuments"]).Resource : r]) == toset(["arn:aws:ssm:*::document/AWS-StartSSHSession", "arn:aws:ssm:*::document/AWS-StartPortForwardingSession", "arn:aws:ssm:*:*:document/SSM-SessionManagerRunShell"])
    error_message = "sessions only with the shell, ssh and local port-forward documents"
  }
  assert {
    condition     = one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "OnlyLabSessionDocuments"]).Effect == "Deny" && contains(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "OnlyLabSessionDocuments"]).NotResource, "arn:aws:ssm:*::document/AWS-StartSSHSession") && !contains(one([for s in jsondecode(aws_iam_policy.boundary.policy).Statement : s if s.Sid == "OnlyLabSessionDocuments"]).NotResource, "arn:aws:ssm:*::document/AWS-StartPortForwardingSessionToRemoteHost")
    error_message = "the boundary denies every other session document, ToRemoteHost included"
  }
  assert {
    condition     = toset(one([for s in jsondecode(aws_iam_role_policy.lab.policy).Statement : s if s.Sid == "OwnSessionsOnly"]).Resource) == toset(["arn:aws:ssm:*:*:session/$${aws:userid}-*"])
    error_message = "terminate and resume only your own sessions"
  }
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd deploy/aws/labs && terraform test`
Expected: FAIL on the replaced IAM assertion (only `iam:PassRole` expected, none present) and the new run (undeclared Sids).

- [ ] **Step 3: Implement**

Boundary (`aws_iam_policy.boundary`): change the `Ceiling` statement's Action to add `"ssm:*", "ssm-guiconnect:*", "ec2-instance-connect:*"`, and add after `Ceiling`:

```hcl
      { Sid = "PassOnlyTheLabInstanceRole", Effect = "Allow", Action = "iam:PassRole", Resource = aws_iam_role.lab_instance.arn },
      # A lab instance must never become a tunnel to the node or Postgres: only the shell, ssh and local port-forward.
      { Sid = "OnlyLabSessionDocuments", Effect = "Deny", Action = "ssm:StartSession",
        NotResource = ["arn:aws:ec2:*:*:instance/*", "arn:aws:ssm:*::document/AWS-StartSSHSession",
      "arn:aws:ssm:*::document/AWS-StartPortForwardingSession", "arn:aws:ssm:*:*:document/SSM-SessionManagerRunShell"] },
```

Lab role (`aws_iam_role_policy.lab`), add to `Statement`:

```hcl
      { Sid = "PassLabInstanceRole", Effect = "Allow", Action = "iam:PassRole", Resource = aws_iam_role.lab_instance.arn,
      Condition = { StringEquals = { "iam:PassedToService" = "ec2.amazonaws.com" } } },
      { Sid = "ProfileOnOwn", Effect = "Allow", Action = "ec2:AssociateIamInstanceProfile", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "SessionsToOwn", Effect = "Allow", Action = "ssm:StartSession", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringEquals = { "ssm:resourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "SessionDocuments", Effect = "Allow", Action = "ssm:StartSession",
        Resource = ["arn:aws:ssm:*::document/AWS-StartSSHSession", "arn:aws:ssm:*::document/AWS-StartPortForwardingSession",
      "arn:aws:ssm:*:*:document/SSM-SessionManagerRunShell"] },
      { Sid = "OwnSessionsOnly", Effect = "Allow", Action = ["ssm:TerminateSession", "ssm:ResumeSession"],
      Resource = ["arn:aws:ssm:*:*:session/$${aws:userid}-*"] },
      { Sid = "KeysToOwn", Effect = "Allow", Resource = "arn:aws:ec2:*:*:instance/*",
        Action = ["ec2-instance-connect:SendSSHPublicKey", "ec2-instance-connect:SendSerialConsoleSSHPublicKey"],
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "ConsoleOfOwn", Effect = "Allow", Resource = "arn:aws:ec2:*:*:instance/*",
        Action = ["ec2:GetConsoleScreenshot", "ec2:GetConsoleOutput"],
      Condition = { StringEquals = { "aws:ResourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "CommandsToOwn", Effect = "Allow", Action = "ssm:SendCommand", Resource = "arn:aws:ec2:*:*:instance/*",
      Condition = { StringEquals = { "ssm:resourceTag/crucible:lab-id" = local.lab } } },
      { Sid = "CommandDocuments", Effect = "Allow", Action = "ssm:SendCommand",
      Resource = ["arn:aws:ssm:*::document/AWS-RunShellScript", "arn:aws:ssm:*::document/AWS-RunPowerShellScript"] },
      { Sid = "ConsoleReads", Effect = "Allow", Resource = "*",
        Action = ["ssm:GetCommandInvocation", "ssm:ListCommandInvocations", "ssm:DescribeInstanceInformation",
          "ssm:GetConnectionStatus", "ssm:DescribeSessions", "ec2:GetSerialConsoleAccessStatus",
      "ssm-guiconnect:StartConnection", "ssm-guiconnect:GetConnection", "ssm-guiconnect:CancelConnection"] },
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd deploy/aws/labs && terraform fmt -check && terraform test`
Expected: PASS, every run.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs-stack): lab sessions, Instance Connect keys, console screenshots and Run Command on own instances only

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/aws/labs/main.tf deploy/aws/labs/labs.tftest.hcl
```

---

### Task 3: Wire the instance profile, subnet CIDRs, node IP and workspace image through up, main and Helm; status egress

**Files:**
- Modify: `internal/awsops/awsops.go` (`labVars`, `egressHosts`, `egressCheck`, `Status` call site)
- Modify: `internal/awsops/awsops_test.go`
- Modify: `deploy/aws/main/variables.tf`, `deploy/aws/main/main.tf` (the `awsLabs` map), `deploy/aws/main/main.tftest.hcl`
- Modify: `deploy/helm/crucible/values.yaml`, `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/test.sh`

**Interfaces:**
- Consumes: labs outputs `lab_instance_profile`, `lab_subnet_cidrs` (Task 1).
- Produces: env `CRUCIBLE_AWS_LAB_INSTANCE_PROFILE`, `CRUCIBLE_AWS_LAB_SUBNET_CIDRS` (comma-separated), `CRUCIBLE_NODE_IP` (Downward API `status.hostIP`) on the API container; main variables `lab_instance_profile`, `lab_subnet_cidrs`, `workspace_image`.

- [ ] **Step 1: Write the failing tests**

`internal/awsops/awsops_test.go`: in `TestLabsInitAndUpWiresTheLabAccount`, add to the `labs` JSON `"lab_instance_profile":{"value":"crucible-lab-instance"},"lab_subnet_cidrs":{"value":"10.2.129.0/24,10.2.130.0/24"}` and to the `want` list `"-var lab_instance_profile=crucible-lab-instance"`, `"-var lab_subnet_cidrs=10.2.129.0/24,10.2.130.0/24"`. Add:

```go
// A labs stack from before OS-level labs has neither output: up still applies, passing empty values.
func TestUpWithAnOlderLabsStack(t *testing.T) {
	labs := `{"lab_role_arn":{"value":"arn:aws:iam::444455556666:role/crucible-lab"},"state_bucket":{"value":"b"},"state_region":{"value":"eu-west-1"}}`
	r := &recorder{output: func(cmd string) string {
		if strings.Contains(cmd, "labs output -json") {
			return labs
		}
		return fakeAWS(cmd)
	}}
	if err := r.ops(t.TempDir()).Up(context.Background(), "x.tfvars", true, false); err != nil {
		t.Fatal(err)
	}
	if indexOf(r.calls, "-var lab_instance_profile= -var lab_subnet_cidrs=") < 0 {
		t.Fatalf("an older labs stack passes an empty profile:\n%s", strings.Join(r.calls, "\n"))
	}
}

func TestEgressCheckCoversOSLabHosts(t *testing.T) {
	hosts := egressHosts("il-central-1")
	for _, want := range []string{"https://signin.aws.amazon.com", "https://ec2-instance-connect.il-central-1.amazonaws.com"} {
		if !slices.Contains(hosts, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(egressCheck(hosts, []string{"serial-console.ec2-instance-connect.il-central-1.aws:22"}), "/dev/tcp/serial-console.ec2-instance-connect.il-central-1.aws/22") {
		t.Fatal("the serial console endpoint is checked on TCP 22")
	}
}
```

(`slices` is already imported in the package; add it to the test file's imports if not.)

`deploy/aws/main/main.tftest.hcl`: add a run that sets `lab_role_arn`, `lab_instance_profile = "crucible-lab-instance"`, `lab_subnet_cidrs = "10.2.129.0/24"`, `workspace_image = "ghcr.io/platform-infra-org/crucible-workspace@sha256:${repeat("a", 64)}"` (plus the other lab vars the existing labs-on run sets) and asserts `yamldecode(aws_ssm_parameter.helm_values.value).awsLabs.instanceProfile == "crucible-lab-instance"`, `.subnetCidrs == "10.2.129.0/24"`, `.workspaceImage == var.workspace_image`; and a run with `workspace_image = "ghcr.io/x/y:latest"` with `expect_failures = [var.workspace_image]`; and one with no `workspace_image` asserting `!contains(keys(yamldecode(aws_ssm_parameter.helm_values.value).awsLabs), "workspaceImage")`. Use the exact helm-values resource name and decode function the existing main tests use (open `main.tftest.hcl` and copy the pattern of the existing `lab_vpc_id` assertion).

`deploy/helm/test.sh`: add a render with `--set awsLabs.instanceProfile=crucible-lab-instance --set awsLabs.subnetCidrs=10.2.129.0/24` (alongside the existing awsLabs render) and `grep` for `CRUCIBLE_AWS_LAB_INSTANCE_PROFILE`, `CRUCIBLE_AWS_LAB_SUBNET_CIDRS` and `fieldPath: status.hostIP`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/awsops -run 'LabsInitAndUp|OlderLabsStack|EgressCheck'` → FAIL (missing vars; `egressCheck` takes one argument).
Run: `cd deploy/aws/main && terraform test` → FAIL (undeclared variables).
Run: `bash deploy/helm/test.sh` → FAIL (env missing).

- [ ] **Step 3: Implement**

`labVars` (append to its slice): `"-var", "lab_instance_profile=" + l["lab_instance_profile"], "-var", "lab_subnet_cidrs=" + l["lab_subnet_cidrs"]`.

`egressHosts`: add after the AWS-lab path entries: `"https://signin.aws.amazon.com", "https://ec2-instance-connect." + region + ".amazonaws.com"`.

`egressCheck` becomes:

```go
// egressCheck prints "blocked <url>" for each URL the node can't open, and for each host:port it can't connect to over
// TCP (the serial console speaks SSH, not HTTP). Any HTTP answer, even an error status, means the firewall let it through.
func egressCheck(urls, tcp []string) string {
	s := "for u in " + strings.Join(urls, " ") + `; do curl -sS -o /dev/null --max-time 5 "$u" 2>/dev/null || echo "blocked $u"; done; `
	for _, hp := range tcp {
		host, port, _ := strings.Cut(hp, ":")
		s += fmt.Sprintf(`timeout 5 bash -c '</dev/tcp/%s/%s' 2>/dev/null || echo "blocked %s"; `, host, port, hp)
	}
	return s + "true"
}
```

Update its caller in `Status` to pass `[]string{"serial-console.ec2-instance-connect." + region + ".aws:22"}` (where `region` is the variable already used there for `egressHosts`). Fix any other test that calls `egressCheck(x)` to `egressCheck(x, nil)`.

`deploy/aws/main/variables.tf`:

```hcl
variable "lab_instance_profile" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: the instance profile lab modules may attach (var.crucible_instance_profile)"
}
variable "lab_subnet_cidrs" {
  type        = string
  default     = ""
  description = "Set by `crucible aws up` from the labs stack: comma-separated CIDRs of the lab subnets (direct_ssh labs)"
}
variable "workspace_image" {
  type        = string
  default     = ""
  description = "The AWS lab workspace image by digest (deploy/workspace); empty keeps the chart's amazon/aws-cli"
  validation {
    condition     = var.workspace_image == "" || can(regex("^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$", var.workspace_image))
    error_message = "workspace_image must be empty or pinned by digest: <image>@sha256:<64 hex>"
  }
}
```

`deploy/aws/main/main.tf`: in the `awsLabs = { … }` map add `instanceProfile = var.lab_instance_profile` and `subnetCidrs = var.lab_subnet_cidrs`, and wrap the map as `awsLabs = merge({ …existing… }, var.workspace_image == "" ? {} : { workspaceImage = var.workspace_image })`.

`deploy/helm/crucible/values.yaml` under `awsLabs:` add:

```yaml
  instanceProfile: ""       # labs stack output lab_instance_profile (var.crucible_instance_profile)
  subnetCidrs: ""           # comma-separated lab subnet CIDRs (direct_ssh labs reach TCP 22 there, never the node)
```

`deploy/helm/crucible/templates/crucible.yaml`, after `CRUCIBLE_AWS_LAB_SUBNET_IDS`:

```yaml
            - { name: CRUCIBLE_AWS_LAB_INSTANCE_PROFILE, value: {{ .Values.awsLabs.instanceProfile | quote }} }
            - { name: CRUCIBLE_AWS_LAB_SUBNET_CIDRS, value: {{ .Values.awsLabs.subnetCidrs | quote }} }
            - name: CRUCIBLE_NODE_IP   # direct_ssh labs: excepted from their TCP 22 rule
              valueFrom: { fieldRef: { fieldPath: status.hostIP } }
```

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l internal && go vet ./internal/awsops && go test ./internal/awsops`; `cd deploy/aws/main && terraform fmt -check && terraform test`; `bash deploy/helm/test.sh`.
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(aws): up passes the lab instance profile, subnet CIDRs and workspace image to the node; status checks the OS-lab hosts

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awsops/awsops.go internal/awsops/awsops_test.go deploy/aws/main/variables.tf deploy/aws/main/main.tf deploy/aws/main/main.tftest.hcl deploy/helm/crucible/values.yaml deploy/helm/crucible/templates/crucible.yaml deploy/helm/test.sh
```

---

### Task 4: Content: `aws.direct_ssh`, the instance profile and SSH source variables for lab modules

**Files:**
- Modify: `internal/content/types.go` (`AWSConfig`), `internal/content/awstf.go` (`LabTF`, `LabNetwork`, `LabTFVars`)
- Modify: `internal/content/awstf_test.go`
- Modify: `internal/labs/bundle.go` (`ModuleBundle`, `planBundle` call `LabTFVars` with the new argument)
- Modify: `cmd/crucible-api/main.go` (fill the new `LabNetwork` fields from env)
- Modify: `docs/user/authors/aws-labs.md`

**Interfaces:**
- Produces:
  - `content.AWSConfig.DirectSSH bool` (`yaml:"direct_ssh"`)
  - `content.LabNetwork{Region, VPCID string; SubnetIDs []string; InstanceProfile string; SubnetCIDRs []string; NodeIP string}`
  - `func LabTFVars(labID, team, training, region string, net LabNetwork, directSSH bool) []byte` adding `crucible_instance_profile` (always `net.InstanceProfile`, `""` when unset) and `crucible_ssh_source_cidr` (`net.NodeIP + "/32"` only when `directSSH && net.NodeIP != ""`, else `""`)
  - `LabTF` declares `variable "crucible_instance_profile" { type = string }` and `variable "crucible_ssh_source_cidr" { type = string }`

- [ ] **Step 1: Write the failing tests** (append to `internal/content/awstf_test.go`)

```go
func TestLabTFVarsInstanceProfileAndSSHSource(t *testing.T) {
	net := LabNetwork{Region: "il-central-1", VPCID: "vpc-1", SubnetIDs: []string{"subnet-a"}, InstanceProfile: "crucible-lab-instance", NodeIP: "10.2.129.17"}
	var v map[string]any
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", net, true), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_instance_profile"] != "crucible-lab-instance" || v["crucible_ssh_source_cidr"] != "10.2.129.17/32" {
		t.Fatalf("direct_ssh lab: %v", v)
	}
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", net, false), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_ssh_source_cidr"] != "" {
		t.Fatalf("no direct_ssh, no source CIDR: %v", v)
	}
	if err := json.Unmarshal(LabTFVars("abc", "t", "tr", "il-central-1", LabNetwork{}, true), &v); err != nil {
		t.Fatal(err)
	}
	if v["crucible_instance_profile"] != "" || v["crucible_ssh_source_cidr"] != "" {
		t.Fatalf("an older labs stack: empty strings, never null: %v", v)
	}
	for _, name := range []string{"crucible_instance_profile", "crucible_ssh_source_cidr"} {
		if !strings.Contains(LabTF, `variable "`+name+`" { type = string }`) {
			t.Errorf("LabTF must declare %s", name)
		}
	}
}

func TestDirectSSHParses(t *testing.T) {
	var c AWSConfig
	if err := yaml.Unmarshal([]byte("region: il-central-1\nmax_hourly_usd: 1\ndirect_ssh: true\n"), &c); err != nil || !c.DirectSSH {
		t.Fatalf("direct_ssh: %v %+v", err, c)
	}
}
```

Use the YAML package the content package already imports (check `load.go`'s imports and use the same one in the test).

Also update every existing `LabTFVars(…, net)` call in content and labs tests to pass `false` as the last argument.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/content -run 'InstanceProfile|DirectSSH'`
Expected: build failure: too many arguments in call to `LabTFVars`; unknown field `DirectSSH`.

- [ ] **Step 3: Implement**

`types.go` `AWSConfig`: add `DirectSSH bool `yaml:"direct_ssh"` // plain SSH from the workspace to the lab subnets on TCP 22 (never the node)`.

`awstf.go`: add to `LabTF` after `crucible_subnet_ids`:

```hcl
variable "crucible_instance_profile" { type = string } # attach to instances for SSM sessions; "" on an older labs stack
variable "crucible_ssh_source_cidr" { type = string }  # aws.direct_ssh: allow TCP 22 from this; "" otherwise
```

`LabNetwork` gains `InstanceProfile string`, `SubnetCIDRs []string`, `NodeIP string` (comment: "the labs stack's instance profile; the lab subnets' CIDRs and the node's address, for direct_ssh labs"). `LabTFVars`:

```go
func LabTFVars(labID, team, training, region string, net LabNetwork, directSSH bool) []byte {
	vpc, subnets := "", []string{}
	if net.VPCID != "" && net.Region == region && len(net.SubnetIDs) > 0 {
		vpc, subnets = net.VPCID, net.SubnetIDs
	}
	source := ""
	if directSSH && net.NodeIP != "" {
		source = net.NodeIP + "/32" // pod traffic leaves the node as the node's address
	}
	b, _ := json.Marshal(map[string]any{"crucible_lab_id": labID, "crucible_team": team,
		"crucible_training": training, "crucible_region": region, "crucible_vpc_id": vpc, "crucible_subnet_ids": subnets,
		"crucible_instance_profile": net.InstanceProfile, "crucible_ssh_source_cidr": source})
	return b
}
```

`internal/labs/bundle.go`: `ModuleBundle` passes `lab.AWS.DirectSSH`; `planBundle` passes `lab.AWS.DirectSSH` too (the estimate plans the same module).

`cmd/crucible-api/main.go`, where `ar.Network` is set:

```go
		ar.Network = content.LabNetwork{Region: ar.StateRegion, VPCID: os.Getenv("CRUCIBLE_AWS_LAB_VPC_ID"),
			SubnetIDs: commaList(os.Getenv("CRUCIBLE_AWS_LAB_SUBNET_IDS")), // the labs stack's own region
			InstanceProfile: os.Getenv("CRUCIBLE_AWS_LAB_INSTANCE_PROFILE"),
			SubnetCIDRs:     commaList(os.Getenv("CRUCIBLE_AWS_LAB_SUBNET_CIDRS")), NodeIP: os.Getenv("CRUCIBLE_NODE_IP")}
```

`docs/user/authors/aws-labs.md`: add a section "Labs on the machine itself" with: `iam_instance_profile = var.crucible_instance_profile` (with an `!= "" ? … : null` guard, the same portable form the page already shows for subnets); AMIs that ship the SSM agent; `aws.direct_ssh: true` plus a security group rule `from_port = 22, to_port = 22, cidr_blocks = [var.crucible_ssh_source_cidr]`; the trainee's tools (`aws ssm start-session`, `lab-ssh`, `lab-console`, `get-console-screenshot`); GRUB on serial (`GRUB_TERMINAL="serial console"`, `GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200"`, `GRUB_TIMEOUT=10`, `update-grub`); Windows SAC (`bcdedit /ems {current} on`, `bcdedit /emssettings EMSPORT:1 EMSBAUDRATE:115200`); "never put a password in user data or state"; checks with `aws ssm send-command` and `aws ssm get-command-invocation`.

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l cmd internal && go vet ./... && go test ./internal/content ./internal/labs ./internal/docs ./cmd/crucible-api`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(content): aws.direct_ssh; lab modules get crucible_instance_profile and crucible_ssh_source_cidr

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content/types.go internal/content/awstf.go internal/content/awstf_test.go internal/labs/bundle.go cmd/crucible-api/main.go docs/user/authors/aws-labs.md
```

(Add any other test files you had to touch for the new `LabTFVars` argument to the pathspec.)

---

### Task 5: Labs runner: a `direct_ssh` lab's NetworkPolicy reaches TCP 22 in the lab subnets, never the node

**Files:**
- Modify: `internal/labs/cluster_objects.go` (new `sshEgress`), `internal/labs/cluster.go` (`provision` takes extra egress rules), `internal/labs/aws.go` (pass them; refuse when unset)
- Modify: `internal/labs/cluster_objects_test.go`, `internal/labs/aws_test.go`

**Interfaces:**
- Consumes: `content.LabNetwork.SubnetCIDRs`, `.NodeIP`, `content.AWSConfig.DirectSSH` (Task 4).
- Produces: `func sshEgress(net content.LabNetwork) (networkingv1.NetworkPolicyEgressRule, error)`; `func (c *ClusterRunner) provision(ctx context.Context, inst *Instance, bundle []byte, compose string, awsSetup func(ns string) error, extra []networkingv1.NetworkPolicyEgressRule) error`.

- [ ] **Step 1: Write the failing tests**

`internal/labs/cluster_objects_test.go`:

```go
func TestSSHEgressExceptsTheNode(t *testing.T) {
	r, err := sshEgress(content.LabNetwork{SubnetCIDRs: []string{"10.2.129.0/24", "10.2.130.0/24"}, NodeIP: "10.2.129.17"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Ports) != 1 || r.Ports[0].Port.IntValue() != 22 || *r.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("TCP 22 only: %+v", r.Ports)
	}
	if len(r.To) != 2 || r.To[0].IPBlock.CIDR != "10.2.129.0/24" || !slices.Equal(r.To[0].IPBlock.Except, []string{"10.2.129.17/32"}) ||
		r.To[1].IPBlock.CIDR != "10.2.130.0/24" || len(r.To[1].IPBlock.Except) != 0 {
		t.Fatalf("the node's /32 is excepted on its own subnet only: %+v", r.To)
	}
	for _, bad := range []content.LabNetwork{{NodeIP: "10.2.129.17"}, {SubnetCIDRs: []string{"10.2.129.0/24"}},
		{SubnetCIDRs: []string{"not-a-cidr"}, NodeIP: "10.2.129.17"}, {SubnetCIDRs: []string{"10.2.129.0/24"}, NodeIP: "nope"},
		{SubnetCIDRs: []string{"0.0.0.0/0"}, NodeIP: "10.2.129.17"}} {
		if _, err := sshEgress(bad); err == nil {
			t.Errorf("%+v: want an error (fail closed)", bad)
		}
	}
}
```

`internal/labs/aws_test.go` (use the runner/test helpers that file already uses to provision an aws lab against the fake clientset; copy the setup of the nearest existing provision test):

- a lab with `AWS.DirectSSH = true` and `a.Network` carrying `SubnetCIDRs` and `NodeIP`: the created `lab-default-deny` NetworkPolicy has 3 egress rules, the third equal to `sshEgress(a.Network)`;
- the same lab with `NodeIP: ""`: provisioning returns an error containing `direct_ssh`, and no NetworkPolicy and no Pod were created;
- a lab without `DirectSSH`: exactly 2 egress rules (today's policy).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/labs -run 'SSHEgress|DirectSSH'`
Expected: build failure: undefined `sshEgress`.

- [ ] **Step 3: Implement**

`cluster_objects.go`:

```go
// sshEgress is a direct_ssh lab's extra egress: TCP 22 to the lab subnets, except the Crucible node itself (a pod's
// traffic to its own node never crosses a security group, so this except is the only wall in front of the node's
// sshd). Kubernetes rejects an except outside its CIDR, so the node's /32 goes only on the subnet that contains it.
// Fails closed: no CIDRs, no node address, or a CIDR that isn't a private subnet.
func sshEgress(net content.LabNetwork) (networkingv1.NetworkPolicyEgressRule, error) {
	node, err := netip.ParseAddr(net.NodeIP)
	if err != nil || len(net.SubnetCIDRs) == 0 {
		return networkingv1.NetworkPolicyEgressRule{}, errors.New("direct_ssh needs the lab subnets' CIDRs and the node's address (CRUCIBLE_AWS_LAB_SUBNET_CIDRS, CRUCIBLE_NODE_IP): run `crucible aws labs-init` and `up` with this version")
	}
	r := networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(22))}}}
	for _, c := range net.SubnetCIDRs {
		p, err := netip.ParsePrefix(c)
		if err != nil || !p.Addr().IsPrivate() || p.Bits() < 16 {
			return networkingv1.NetworkPolicyEgressRule{}, fmt.Errorf("direct_ssh: %q is not a private lab subnet CIDR", c)
		}
		b := &networkingv1.IPBlock{CIDR: p.String()}
		if p.Contains(node) {
			b.Except = []string{node.String() + "/32"}
		}
		r.To = append(r.To, networkingv1.NetworkPolicyPeer{IPBlock: b})
	}
	return r, nil
}
```

Add imports `errors`, `fmt`, `net/netip`, `crucible/internal/content` as needed.

`cluster.go`: `provision` gains `extra []networkingv1.NetworkPolicyEgressRule`; right after `o = awsWorkspace(o)` add `o.Network.Spec.Egress = append(o.Network.Spec.Egress, extra...)`. `Provision` passes `nil`.

`aws.go`, in the provisioning function, before building the bundle:

```go
	var extra []networkingv1.NetworkPolicyEgressRule
	if lab.AWS.DirectSSH {
		r, err := sshEgress(a.Network)
		if err != nil {
			return err
		}
		extra = append(extra, r)
	}
```

and pass `extra` as the new last argument of `a.Cluster.provision(…)`.

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l internal && go vet ./internal/labs && go test ./internal/labs`
Expected: PASS (`TestLabNetworkPolicy` unchanged and passing).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): direct_ssh labs reach TCP 22 in the lab subnets, never the node; fail closed without the network

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/labs/cluster_objects.go internal/labs/cluster.go internal/labs/aws.go internal/labs/cluster_objects_test.go internal/labs/aws_test.go
```

---

### Task 6: The console link: `POST /api/labs/{id}/console`

**Files:**
- Create: `internal/awscloud/console.go`, `internal/awscloud/console_test.go`
- Modify: `internal/labs/aws.go` (`AWSRunner` fields `SigninEndpoint`, `Issuer`, `HTTP`; method `ConsoleURL`), `internal/labs/service.go` (`Console`), `internal/labs/http.go` (route)
- Modify: `internal/labs/http_test.go` or `internal/labs/aws_service_test.go` (whichever already tests aws lab routes with the fake)
- Modify: `cmd/crucible-api/main.go` (set `Issuer` to the public URL)

**Interfaces:**
- Produces:
  - `func ConsoleURL(ctx context.Context, hc *http.Client, endpoint string, c Credentials, issuer, destination string) (string, error)` in `awscloud`
  - `const SigninEndpoint = "https://signin.aws.amazon.com/federation"` in `awscloud`
  - `type ConsoleLink struct { URL string `json:"url"` }` and `func (s *Service) Console(ctx context.Context, u *auth.User, labID string) (*ConsoleLink, error)` in `labs`
  - Route `POST /api/labs/{id}/console` → `{"url": "…"}`

- [ ] **Step 1: Write the failing tests**

`internal/awscloud/console_test.go`:

```go
func TestConsoleURL(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"SigninToken":"tok123"}`))
	}))
	defer srv.Close()
	c := Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "sk", SessionToken: "st"}
	u, err := ConsoleURL(context.Background(), srv.Client(), srv.URL, c, "https://crucible.example.com", "https://il-central-1.console.aws.amazon.com/ec2/home?region=il-central-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("Action") != "getSigninToken" || !strings.Contains(got.Get("Session"), `"sessionId":"ASIA1"`) || !strings.Contains(got.Get("Session"), `"sessionToken":"st"`) {
		t.Fatalf("token request: %v", got)
	}
	p, _ := url.Parse(u)
	q := p.Query()
	if q.Get("Action") != "login" || q.Get("SigninToken") != "tok123" || q.Get("Issuer") != "https://crucible.example.com" ||
		!strings.HasPrefix(q.Get("Destination"), "https://il-central-1.console.aws.amazon.com/ec2/") {
		t.Fatalf("login URL: %s", u)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	if _, err := ConsoleURL(context.Background(), srv.Client(), srv.URL, c, "i", "d"); err == nil || strings.Contains(err.Error(), "sk") {
		t.Fatalf("a refused token is an error that never echoes the secret: %v", err)
	}
}
```

Labs (in the file that already exercises aws lab HTTP routes with `awscloud.Fake`; copy its setup of a ready aws lab owned by user A):

- user A `POST /api/labs/{id}/console` with body `{}` → 200, `url` starts with the test federation server's URL and contains `SigninToken=`; the federation server (an `httptest.Server` set as `AWSRunner.SigninEndpoint` with `AWSRunner.HTTP = srv.Client()`) received credentials from `Fake.AssumeLab` for that lab id;
- user B on A's lab → 404;
- A on a lab that is not `ready`, or not `runtime: aws` → 404 or a 409 with "only a running AWS lab";
- the audit table has one `lab.console` row for that lab whose detail JSON does not contain `tok123` and does not contain `SigninToken`;
- a second call within a minute → 409 with "one console link a minute".

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/awscloud -run ConsoleURL; go test ./internal/labs -run Console`
Expected: build failure: undefined `ConsoleURL`, `Console`.

- [ ] **Step 3: Implement**

`internal/awscloud/console.go`:

```go
package awscloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// SigninEndpoint is AWS's federation endpoint: it turns temporary credentials into an AWS console sign-in link.
const SigninEndpoint = "https://signin.aws.amazon.com/federation"

// ConsoleURL makes a console sign-in URL from c (an assumed-role session): the console session has exactly c's
// permissions and lasts until c expires. The URL works once, within 15 minutes; never log or store it.
func ConsoleURL(ctx context.Context, hc *http.Client, endpoint string, c Credentials, issuer, destination string) (string, error) {
	sess, _ := json.Marshal(map[string]string{"sessionId": c.AccessKeyID, "sessionKey": c.SecretAccessKey, "sessionToken": c.SessionToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+url.Values{"Action": {"getSigninToken"}, "Session": {string(sess)}}.Encode(), nil)
	if err != nil {
		return "", err
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", errors.New("the AWS sign-in endpoint is unreachable") // the request URL carries the secret: never wrap err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the AWS sign-in endpoint refused the lab session (HTTP %d)", res.StatusCode)
	}
	var body struct{ SigninToken string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&body); err != nil || body.SigninToken == "" {
		return "", errors.New("the AWS sign-in endpoint sent no sign-in token")
	}
	return endpoint + "?" + url.Values{"Action": {"login"}, "Issuer": {issuer}, "Destination": {destination}, "SigninToken": {body.SigninToken}}.Encode(), nil
}
```

`internal/labs/aws.go`: `AWSRunner` gains

```go
	SigninEndpoint string       // awscloud.SigninEndpoint; tests point it at httptest
	Issuer         string       // Crucible's public URL: where an expired console session sends people back
	HTTP           *http.Client // nil = a client with a 10 s timeout
	consoleAt      map[string]time.Time // lab id → last console link (one a minute)
```

and

```go
var errConsoleTooSoon = apperr.Wrap(apperr.Conflict, "one console link a minute: use the one you have, or wait")

// ConsoleURL mints a console sign-in link for a running lab, from a fresh session with the lab's tags (the mounted
// Secret is never read back). The link and token are never logged.
func (a *AWSRunner) ConsoleURL(ctx context.Context, inst *Instance, region string) (string, error) {
	a.mu.Lock()
	if a.consoleAt == nil {
		a.consoleAt = map[string]time.Time{}
	}
	if t, ok := a.consoleAt[inst.ID]; ok && a.now().Sub(t) < time.Minute {
		a.mu.Unlock()
		return "", errConsoleTooSoon
	}
	a.consoleAt[inst.ID] = a.now()
	a.mu.Unlock()
	if a.DryRun != nil {
		return "", apperr.Wrap(apperr.Unavailable, "the AWS console isn't available in dry-run mode")
	}
	creds, err := a.cloud().AssumeLab(ctx, labSession(inst))
	if err != nil {
		return "", fmt.Errorf("getting lab credentials: %w", err)
	}
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	dest := fmt.Sprintf("https://%[1]s.console.aws.amazon.com/ec2/home?region=%[1]s#Instances:tag:crucible:lab-id=%[2]s", region, inst.ID)
	return awscloud.ConsoleURL(ctx, hc, cmp.Or(a.SigninEndpoint, awscloud.SigninEndpoint), creds, a.Issuer, dest)
}
```

`apperr` has no 429 kind; a second link within a minute is a 409 Conflict. Tests of the link set `DryRun` to nil and use `Cloud: fake`.

`internal/labs/service.go`:

```go
type ConsoleLink struct {
	URL string `json:"url"`
}

// Console gives the lab's own trainee an AWS console sign-in link for their running AWS lab (spec 2026-11-04 §4.2).
func (s *Service) Console(ctx context.Context, u *auth.User, labID string) (*ConsoleLink, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	ar, ok := s.Runners["aws"].(*AWSRunner)
	if !ok || inst.Runtime != "aws" || inst.State != Ready {
		return nil, apperr.Wrap(apperr.Conflict, "only a running AWS lab has an AWS console")
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		return nil, err
	}
	link, err := ar.ConsoleURL(ctx, inst, lab.AWS.Region)
	if err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, s.DB, u.Email, "lab.console", inst.ID, map[string]any{"team": inst.Team, "training": inst.Training}, ""); err != nil {
		return nil, err
	}
	return &ConsoleLink{URL: link}, nil
}
```

`internal/labs/http.go`, next to `/extend`:

```go
	r.Post("/api/labs/{id}/console", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Console(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
```

`cmd/crucible-api/main.go`: where the `AWSRunner` is built, set `Issuer: public` (the same public URL value passed to `httpapi` as `PublicURL`).

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l cmd internal && go vet ./... && go test ./internal/awscloud ./internal/labs ./cmd/crucible-api`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(labs): a running AWS lab's trainee gets an AWS console sign-in link, audited, never logged

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/awscloud/console.go internal/awscloud/console_test.go internal/labs/aws.go internal/labs/service.go internal/labs/http.go cmd/crucible-api/main.go <the labs test file you changed>
```

---

### Task 7: The Console button on a running AWS lab

**Files:**
- Modify: `web/src/pages/Lab.tsx` (toolbar near `Timer` / `End lab`, around line 300)
- Modify: `web/src/pages/Lab.test.tsx`
- Modify: `docs/user/trainees/cluster-and-aws-labs.md`

**Interfaces:**
- Consumes: `POST /api/labs/{id}/console` → `{ url: string }` (Task 6).

- [ ] **Step 1: Read the skill and write the failing test**

Read `.claude/skills/crucible-ui/SKILL.md`. In `Lab.test.tsx`, following the file's existing `renderToStaticMarkup` pattern with mocked `../useFetch`, render a ready lab with `runtime: 'aws'` and assert the markup contains a button with text `AWS console`; render a ready `runtime: 'cluster'` lab and a `provisioning` aws lab and assert it is absent.

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && npx vitest run src/pages/Lab.test.tsx`
Expected: FAIL: no `AWS console` button.

- [ ] **Step 3: Implement**

In `Lab.tsx`, beside the existing `extend` handler:

```tsx
  const openConsole = async () => {
    const tab = window.open('', '_blank') // opened in the click, so the browser doesn't block it
    try {
      const v = await api<{ url: string }>(`/api/labs/${lab.id}/console`, { method: 'POST', json: {} })
      if (tab) tab.location.href = v.url
      else window.location.assign(v.url)
    } catch (e) {
      tab?.close()
      toast(e instanceof Error ? e.message : 'The AWS console could not be opened')
    }
  }
```

and in the toolbar, before `End lab`, only when `lab.runtime === 'aws' && lab.state === 'ready'`:

```tsx
        <button className="ghost" onClick={openConsole} title="Opens the AWS console for this lab's resources, in a new tab">AWS console</button>
```

Import `toast` from `../lib/alerts` if the file doesn't already. The window opener: set `tab.opener = null` before navigating (`if (tab) { tab.opener = null; tab.location.href = v.url }`).

`docs/user/trainees/cluster-and-aws-labs.md`: add "Working on the machine itself": the **AWS console** button (what it opens, that it lasts up to an hour, that it shows only your lab's resources, the EC2 serial console page for a machine that won't boot, Fleet Manager for Windows desktops); in the workspace terminal `aws ssm start-session --target <id>`, `lab-ssh <id>`, `lab-ssh --direct <id>` (labs that offer plain SSH), `lab-console <id>` (leave with Enter, then `~.`), `aws ec2 get-console-screenshot`.

- [ ] **Step 4: Run to verify it passes**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`; `go test ./internal/docs`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(web): an AWS console button on a running AWS lab

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/Lab.tsx web/src/pages/Lab.test.tsx docs/user/trainees/cluster-and-aws-labs.md
```

---

### Task 8: The workspace image: ssh, the Session Manager plugin, `lab-ssh`, `lab-console`

**Files:**
- Create: `deploy/workspace/Dockerfile`, `deploy/workspace/lab-ssh`, `deploy/workspace/lab-console`, `deploy/workspace/test.sh`
- Modify: `Makefile` (target `workspace`)

**Interfaces:**
- Produces: image `crucible-workspace:<tag>`; commands `lab-ssh [--direct] <instance-id> [user]`, `lab-console <instance-id>` inside it.

- [ ] **Step 1: Write the failing test** (`deploy/workspace/test.sh`)

```bash
#!/usr/bin/env bash
# Checks the workspace helpers without AWS: syntax, shellcheck when installed, and their argument handling.
set -euo pipefail
cd "$(dirname "$0")"
for f in lab-ssh lab-console; do
  bash -n "$f"
  if command -v shellcheck >/dev/null; then shellcheck "$f"; fi
done
# No arguments: usage on stderr, exit 2, no AWS call (aws is not on PATH here).
for f in lab-ssh lab-console; do
  set +e; out=$(PATH=/usr/bin:/bin ./"$f" 2>&1); code=$?; set -e
  [ "$code" = 2 ] || { echo "$f without arguments: exit $code, want 2"; exit 1; }
  grep -q usage <<<"$out" || { echo "$f: no usage line"; exit 1; }
done
# A malformed instance id is refused before any AWS call.
for f in lab-ssh lab-console; do
  set +e; PATH=/usr/bin:/bin ./"$f" 'i-123;reboot' >/dev/null 2>&1; code=$?; set -e
  [ "$code" = 2 ] || { echo "$f accepted a malformed instance id"; exit 1; }
done
echo "workspace helpers OK"
```

- [ ] **Step 2: Run to verify it fails**

Run: `bash deploy/workspace/test.sh`
Expected: FAIL: `lab-ssh: No such file or directory`.

- [ ] **Step 3: Implement**

`deploy/workspace/lab-ssh`:

```bash
#!/usr/bin/env bash
# lab-ssh [--direct] <instance-id> [user]: ssh to one of this lab's instances with a one-minute key from EC2 Instance
# Connect. Default: through Session Manager (no network path needed). --direct: to the private IP on TCP 22 (labs with
# aws.direct_ssh). The key never leaves this container and is gone when the command ends.
set -euo pipefail
usage() { echo "usage: lab-ssh [--direct] <instance-id> [user]   (user defaults to ubuntu)" >&2; exit 2; }
direct=0
if [ "${1:-}" = --direct ]; then direct=1; shift; fi
id=${1:-}; user=${2:-ubuntu}
[[ $id =~ ^i-[0-9a-f]{8,17}$ ]] || usage
[[ $user =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || usage
dir=$(mktemp -d); trap 'rm -rf "$dir"' EXIT
ssh-keygen -q -t ed25519 -N '' -f "$dir/key"
aws ec2-instance-connect send-ssh-public-key --instance-id "$id" --instance-os-user "$user" \
  --ssh-public-key "file://$dir/key.pub" >/dev/null
if [ "$direct" = 1 ]; then
  ip=$(aws ec2 describe-instances --instance-ids "$id" --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)
  ssh -i "$dir/key" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$dir/known_hosts" "$user@$ip"
  exit
fi
ssh -i "$dir/key" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$dir/known_hosts" \
  -o ProxyCommand="aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p" "$user@$id"
```

No `exec`: the `EXIT` trap must still run to remove the key directory.

`deploy/workspace/lab-console`:

```bash
#!/usr/bin/env bash
# lab-console <instance-id>: the instance's serial console (GRUB menu, boot messages, a login or emergency shell,
# Windows SAC), even when it doesn't boot. Leave with Enter, then ~. (ssh's escape).
set -euo pipefail
usage() { echo "usage: lab-console <instance-id>" >&2; exit 2; }
id=${1:-}
[[ $id =~ ^i-[0-9a-f]{8,17}$ ]] || usage
region=${AWS_REGION:?AWS_REGION is not set}
host="serial-console.ec2-instance-connect.$region.aws"
dir=$(mktemp -d); trap 'rm -rf "$dir"' EXIT
ssh-keygen -q -t ed25519 -N '' -f "$dir/key"
aws ec2-instance-connect send-serial-console-ssh-public-key --instance-id "$id" --serial-port 0 \
  --ssh-public-key "file://$dir/key.pub" >/dev/null
# AWS publishes each region's serial console host key fingerprint; refuse any other key.
want=$(awk -v r="$region" '$1 == r {print $2}' /etc/crucible/serial-console-fingerprints)
[ -n "$want" ] || { echo "no pinned serial console fingerprint for $region" >&2; exit 1; }
ssh-keyscan -t ed25519 "$host" 2>/dev/null >"$dir/known_hosts"
got=$(ssh-keygen -lf "$dir/known_hosts" | awk '{print $2}')
[ "$got" = "$want" ] || { echo "serial console host key $got does not match AWS's published $want" >&2; exit 1; }
echo "Connected to $id's serial console. Press Enter if the screen is blank; leave with Enter then ~." >&2
ssh -i "$dir/key" -o UserKnownHostsFile="$dir/known_hosts" "$id.port0@$host"
```

`deploy/workspace/Dockerfile`:

```dockerfile
# The AWS lab workspace: the AWS CLI plus what OS-level labs need (ssh, the Session Manager plugin, lab-ssh,
# lab-console). Published by digest (runbook: the workspace image); main's workspace_image points at it.
FROM amazon/aws-cli:2.27.0@sha256:e3e329e1d2894b7b4bbb0aacacd0a155262159b2e7a3b4275eb1f24046d3e06c
ARG TARGETARCH
ARG SSM_PLUGIN=1.2.707.0
ARG SSM_PLUGIN_SHA256_AMD64=<sha256 of linux_64bit/session-manager-plugin.rpm at SSM_PLUGIN>
ARG SSM_PLUGIN_SHA256_ARM64=<sha256 of linux_arm64/session-manager-plugin.rpm at SSM_PLUGIN>
RUN yum install -y openssh-clients && yum clean all
RUN set -eu; case "$TARGETARCH" in amd64) d=linux_64bit; s=$SSM_PLUGIN_SHA256_AMD64 ;; arm64) d=linux_arm64; s=$SSM_PLUGIN_SHA256_ARM64 ;; *) exit 1 ;; esac; \
    curl -fsSL -o /tmp/ssm.rpm "https://s3.amazonaws.com/session-manager-downloads/plugin/$SSM_PLUGIN/$d/session-manager-plugin.rpm"; \
    echo "$s  /tmp/ssm.rpm" | sha256sum -c -; yum install -y /tmp/ssm.rpm; rm /tmp/ssm.rpm; yum clean all
COPY serial-console-fingerprints /etc/crucible/serial-console-fingerprints
COPY --chmod=755 lab-ssh lab-console /usr/local/bin/
```

Fill the two `ARG …SHA256…` defaults before committing: pick the newest version on https://docs.aws.amazon.com/systems-manager/latest/userguide/plugin-version-history.html, set `SSM_PLUGIN` to it, then run `curl -fsSL "https://s3.amazonaws.com/session-manager-downloads/plugin/<v>/linux_64bit/session-manager-plugin.rpm" | shasum -a 256` (and `linux_arm64`) and paste the hashes. A downloaded file goes in a new empty scratch directory.

Create `deploy/workspace/serial-console-fingerprints` with one line per region, `<region> SHA256:<fingerprint>`, copied from AWS's "EC2 Serial Console endpoints and fingerprints" page (https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/connect-to-serial-console.html), for every region that page lists; it must include `il-central-1` and `eu-west-1`. Add a check to `test.sh`: `grep -q '^il-central-1 SHA256:' serial-console-fingerprints`.

`Makefile`: add `workspace` to `.PHONY` and

```make
WORKSPACE_TAG ?= dev
workspace:
	docker build -f deploy/workspace/Dockerfile -t crucible-workspace:$(WORKSPACE_TAG) deploy/workspace
```

- [ ] **Step 4: Run to verify it passes**

Run: `bash deploy/workspace/test.sh`; `make workspace` (local arch only; it pulls the pinned base image and the plugin; skip if Docker is down and say so in the report).
Expected: `workspace helpers OK`; the image builds.

- [ ] **Step 5: Commit**

```bash
git add deploy/workspace
git commit -m "feat(workspace): an AWS lab workspace image with ssh, the Session Manager plugin, lab-ssh and lab-console

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- deploy/workspace Makefile
```

---

### Task 9: Example: `examples/forge-402`, GRUB rescue and Locked out

**Files:**
- Create: `examples/forge-402/training.yaml`, `examples/forge-402/modules/01-grub-rescue/…`, `examples/forge-402/modules/02-locked-out/…`
- Modify: `cmd/crucible/main_test.go` (`TestLintExamplesPass` lists repos: add `"../../examples/forge-402"` to its `dir` list)
- Modify: `scripts/seed-git.sh` if it lists example repos by name

**Interfaces:**
- Consumes: `aws.direct_ssh`, `var.crucible_instance_profile`, `var.crucible_ssh_source_cidr`, `var.crucible_subnet_ids`, `var.crucible_vpc_id` (Task 4).

- [ ] **Step 1: Copy the shape of an existing AWS example**

Open `examples/forge-401` (`training.yaml`, `modules/01-cloud-heat/module.yaml`, `modules/01-cloud-heat/lab/lab.yaml`, `lab/terraform/`, `lab/tasks/`, `lab/checks/`) and mirror its file layout and keys exactly: each module's lab lives under `modules/<id>/lab/`.

- [ ] **Step 2: Write the GRUB lab**

`modules/01-grub-rescue/lab/lab.yaml` (keys as in forge-401): `runtime: aws`, `aws: { region: eu-west-1, max_hourly_usd: 0.05 }` (eu-west-1 like forge-401: the dry run's fixture prices cover it; an install copies the lab and changes the region), one task "Boot it again" whose check script runs:

```bash
#!/usr/bin/env bash
set -euo pipefail
id=$(aws ec2 describe-instances --filters "Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID" "Name=instance-state-name,Values=running" --query 'Reservations[0].Instances[0].InstanceId' --output text)
cmd=$(aws ssm send-command --instance-ids "$id" --document-name AWS-RunShellScript --parameters 'commands=["test -s /boot/grub/grub.cfg && systemctl is-system-running --wait"]' --query Command.CommandId --output text)
aws ssm wait command-executed --command-id "$cmd" --instance-id "$id"
```

`terraform/main.tf`: an Ubuntu 24.04 `t3.micro` (`data "aws_ami"` owner `099720109477`, name `ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*`), `subnet_id`/`vpc_id` in the portable form from `docs/user/authors/aws-labs.md`, `iam_instance_profile = var.crucible_instance_profile != "" ? var.crucible_instance_profile : null`, a security group with no ingress and egress to 0.0.0.0/0, `associate_public_ip_address = false`, gp3 8 GiB, and `user_data`:

```bash
#!/bin/bash
set -eu
cat >/etc/default/grub.d/99-crucible-serial.cfg <<'EOF'
GRUB_TERMINAL="serial console"
GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200"
GRUB_TIMEOUT_STYLE=menu
GRUB_TIMEOUT=10
EOF
update-grub
# The exercise: no grub.cfg. GRUB stops at `grub rescue>` / `grub>` on the next boot.
mv /boot/grub/grub.cfg /boot/grub/grub.cfg.lost
reboot
```

Instructions (Markdown in the task): use `lab-console <id>` or the AWS console button → EC2 serial console; at `grub>` boot by hand (`set root=(hd0,gpt1)`, `linux /boot/vmlinuz root=/dev/nvme0n1p1`, `initrd /boot/initrd.img`, `boot`), then `sudo update-grub`; alternative: the rescue-volume method.

- [ ] **Step 3: Write the Locked out lab**

`modules/02-locked-out`: same instance shape plus `aws.direct_ssh: true` in `lab.yaml`, an ingress rule `tcp 22` from `[var.crucible_ssh_source_cidr]` (only when it is not `""`), and `user_data` that sets `Port 2222` and `PasswordAuthentication no` in `/etc/ssh/sshd_config.d/99-crucible.conf` and `AuthorizedKeysCommand /bin/false` (breaking Instance Connect), then `systemctl restart ssh`. Task "Let yourself back in": get in with `aws ssm start-session` or `lab-console`, restore sshd on 22; check script: `timeout 10 lab-ssh --direct "$id" true` exit 0 (Instance Connect works again only when `AuthorizedKeysCommand` is restored).

- [ ] **Step 4: Lint, price, test**

Run: `make build && ./bin/crucible lint examples/forge-402`
Expected: `… module(s) OK. Ready for the forge.` (If `aws_iam_instance_profile`-free modules still trip a price rule, declare the type under `aws.prices` the way forge-401 does.)
Run: `go test ./cmd/crucible -run LintExamples`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add examples/forge-402
git commit -m "feat(examples): forge-402, OS-level AWS labs: GRUB rescue on the serial console, locked out of SSH

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- examples/forge-402 cmd/crucible/main_test.go
```

(Add `scripts/seed-git.sh` to the pathspec if you changed it.)

---

### Task 10: Operator docs

**Files:**
- Modify: `docs/runbooks/aws.md`, `docs/aws-deploy.html`, `docs/deploy-architecture.html`, `docs/test-guide.html`, `docs/aws-primer.html`

- [ ] **Step 1: Runbook** (`docs/runbooks/aws.md`, the AWS labs section and "Our account")

Add: labs-init now creates `crucible-lab-instance` (role + profile, no `GetParameter`) and turns on serial console access in `--region` (account-wide; `--` none, it's the stack default; `serial_console = false` to skip, by running the labs apply as the budget-removal example does); the workspace image (`make workspace` per arch with `docker buildx build --platform linux/amd64,linux/arm64 -f deploy/workspace/Dockerfile --push -t ghcr.io/platform-infra-org/crucible-workspace:<v> deploy/workspace`, then `docker buildx imagetools inspect` for the digest, then `workspace_image = "…@sha256:…"` in `crucible.tfvars` and `up`); the new egress hosts; the by-hand checks from spec §9 with exact commands.

- [ ] **Step 2: HTML pages**

- `aws-deploy.html` (AWS labs section and our-account notes): the same steps as the runbook, in the page's `steps` style, with how to check each.
- `deploy-architecture.html`: in `#iam-roles`, a row for the lab instance role and the new lab-role rows; in the AWS lab's-life section, the three access paths and the `direct_ssh` NetworkPolicy rule with the node excepted; a sentence on the console link.
- `test-guide.html`: checklist items for an SSM shell, `lab-ssh`, `lab-ssh --direct` (and that ssh to the node's IP times out), `lab-console` at a GRUB prompt, the AWS console button, Fleet Manager on a Windows lab, and `crucible aws status` reporting the serial endpoint.
- `aws-primer.html`: a Q&A "How do you reach a machine that won't boot on AWS?" (serial console, screenshots, rescue volume) with the account's facts.

- [ ] **Step 3: Check the pages**

Run (from the repo root), for each changed HTML file: the duplicate-id, anchor and tag-balance checks used earlier in this repo:

```bash
for f in docs/aws-deploy.html docs/deploy-architecture.html docs/test-guide.html docs/aws-primer.html; do
  grep -o 'id="[^"]*"' "$f" | sort | uniq -d
  for a in $(grep -o 'href="#[^"]*"' "$f" | sed 's/href="#//;s/"//' | sort -u); do grep -q "id=\"$a\"" "$f" || echo "$f: missing #$a"; done
done
go test ./internal/docs
```

Expected: no output from the loops; docs tests PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "docs: OS-level AWS labs for operators: the instance role, serial console, workspace image, checks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs/runbooks/aws.md docs/aws-deploy.html docs/deploy-architecture.html docs/test-guide.html docs/aws-primer.html
```

---

## Final check (after every task)

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
gofmt -l cmd internal; go vet ./...; go test -race ./...
for s in persistent main labs; do (cd deploy/aws/$s && terraform test); done
bash deploy/helm/test.sh; bash deploy/workspace/test.sh
(cd web && npm test && npx tsc -b && npm run build && npm run lint)
KEYCLOAK_PORT=8082 make cluster-check   # the AWS dry run, including forge-402
```
