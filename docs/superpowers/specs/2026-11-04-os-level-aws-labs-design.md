# OS-level AWS labs: shells, serial console and the AWS console — design

Status: draft for the owner's review (2026-11-04). Decisions made in conversation: Linux first, Windows supported;
trainees may open the AWS console for their lab; shells go over SSM by default, and a lab may opt in to plain SSH
over the network (our networks use SSH directly, and broken-SSH exercises need the real path); the workspace image is
published to GHCR under the org.

## 1. Goal

AWS labs where the trainee works on a lab EC2 machine's operating system: a shell on it, and labs where the machine
does not boot (corrupted GRUB, broken fstab, bad kernel, Windows boot loop) and needs console-level access, the way a
vSphere web console gives it. Nothing new may become reachable from lab pods by default; a lab that opts in to plain
SSH reaches TCP 22 in the lab subnets and nothing else, never the Crucible node.

## 2. Approach

Every access path is an AWS API call authorized by the lab's own session credentials and the existing ABAC rule
(`aws:ResourceTag/crucible:lab-id` = the session's lab id). By default no network path from lab pods into the VPC is
added and the lab NetworkPolicy (`internal/labs/cluster_objects.go`, `blockedEgress`) is unchanged; plain SSH is a
per-lab opt-in (§4.3).

| Need | Path | Works when the machine… |
|---|---|---|
| Linux shell | `aws ssm start-session` (document `SSM-SessionManagerRunShell`) | boots and runs the SSM agent |
| `ssh` / `scp` (Linux) | ssh through SSM (`AWS-StartSSHSession` as ProxyCommand) with a 60-second key from EC2 Instance Connect | boots, runs sshd and the SSM agent |
| Plain `ssh` over the network (opt-in, `aws.direct_ssh: true`) | ssh to the instance's private IP on TCP 22, with an Instance Connect key or the trainee's own | boots and runs sshd; no SSM needed |
| Console: GRUB, boot log, emergency shell, Windows SAC | EC2 Serial Console: `ssh <id>.port0@serial-console.ec2-instance-connect.<region>.aws` from the workspace, or the console's EC2 serial console page | is powered on, booting or not |
| Windows desktop | Fleet Manager Remote Desktop (browser RDP over SSM) through the console link | boots and runs the SSM agent |
| What the screen shows | `ec2 get-console-screenshot`, `get-console-output` | is powered on |
| Repair a root disk | rescue volume: stop, detach, attach to a helper instance, chroot (already allowed) | always |

Out of scope: Linux graphical desktops, per-lab instance roles, ports other than 22 for direct SSH.

## 3. Labs stack (`deploy/aws/labs`)

### 3.1 Lab instance role and profile

`<name>-lab-instance` (role, trust `ec2.amazonaws.com`) and an instance profile of the same name, shared by every
lab's instances. Its policy is written out, not AWS's `AmazonSSMManagedInstanceCore`, which grants
`ssm:GetParameter(s)`: with it, root on a lab instance could read `/crucible/*` (secret key, database password, OIDC
secret) in the same account.

- Allow: `ssm:UpdateInstanceInformation`; `ssmmessages:CreateControlChannel`, `CreateDataChannel`,
  `OpenControlChannel`, `OpenDataChannel`; `ec2messages:AcknowledgeMessage`, `DeleteMessage`, `FailMessage`,
  `GetEndpoint`, `GetMessages`, `SendReply`.
- Deny: `ssm:GetParameter*` on `arn:aws:ssm:*:*:parameter/<name>/*`.

Output `lab_instance_profile` (the profile name). `crucible aws up` passes it to main, main to Helm, Helm to the API
(`CRUCIBLE_AWS_LAB_INSTANCE_PROFILE`), the same path as `lab_subnet_ids`.

### 3.2 Lab role additions

Each is also allowed by the permission boundary's ceiling (`ssm:*`, `ssm-guiconnect:*`, `ec2-instance-connect:*`,
and `iam:PassRole` on the lab instance role only). Instance resources carry the ABAC condition on the lab id.

| Purpose | Actions | Scope |
|---|---|---|
| Launch with the role | `iam:PassRole` | the lab instance role; `iam:PassedToService` = `ec2.amazonaws.com` |
| Attach later | `ec2:AssociateIamInstanceProfile` | own instances |
| Sessions | `ssm:StartSession` | own instances (`ssm:resourceTag/crucible:lab-id`) and the documents `SSM-SessionManagerRunShell`, `AWS-StartSSHSession`, `AWS-StartPortForwardingSession` |
| Own sessions | `ssm:TerminateSession`, `ssm:ResumeSession` | `session/${aws:userid}-*` |
| Keys | `ec2-instance-connect:SendSSHPublicKey`, `SendSerialConsoleSSHPublicKey` | own instances |
| Screen, boot log | `ec2:GetConsoleScreenshot`, `ec2:GetConsoleOutput` | own instances |
| Checks | `ssm:SendCommand` (`AWS-RunShellScript`, `AWS-RunPowerShellScript`), `ssm:GetCommandInvocation`, `ssm:ListCommandInvocations` | own instances |
| Fleet Manager | `ssm-guiconnect:StartConnection`, `GetConnection`, `CancelConnection` | — |
| Console reads | `ssm:DescribeInstanceInformation`, `ssm:GetConnectionStatus`, `ssm:DescribeSessions`, `ec2:GetSerialConsoleAccessStatus` | read-only |

The boundary denies `ssm:StartSession` with any other document, in particular
`AWS-StartPortForwardingSessionToRemoteHost` (it would turn a lab instance into a tunnel to the node or Postgres) and
`AWS-StartInteractiveCommand`.

### 3.3 Serial console access

`aws_ec2_serial_console_access { enabled = true }` in the labs stack, behind `var.serial_console` (default true).
It is an account-wide, per-region setting, applied in `--region` only. Any principal with
`SendSerialConsoleSSHPublicKey` could then reach any instance's serial port, the Crucible node included; the node has
no password user, and the lab role is tag-scoped. The runbook says so.

## 4. Crucible

### 4.1 Module variable

`var.crucible_instance_profile` (string, "" when the labs stack predates it) in the generated `crucible.tf` and
`crucible.auto.tfvars.json` (`content.LabTFVars`, `content.LabNetwork` grows an `InstanceProfile`). Lint accepts
`iam_instance_profile = var.crucible_instance_profile` and keeps refusing any other IAM.

### 4.2 Console link

`POST /api/labs/{id}/console` (JSON body, same-origin as every state change):

- Allowed for the lab's own trainee while the lab is running on the aws runner; anyone else gets 404.
- Crucible assumes the lab role afresh with the lab's session tags (`labSession(inst)`), 15-minute duration; it never
  reads the workspace Secret back.
- Calls `https://signin.aws.amazon.com/federation?Action=getSigninToken` with those credentials, then returns
  `https://signin.aws.amazon.com/federation?Action=login&Issuer=Crucible&Destination=<EC2 instances in the lab's
  region, filtered by tag crucible:lab-id>&SigninToken=…`.
- Writes an audit row (`lab.console`, the lab id); the URL and token are never logged or stored.
- At most one link per lab per minute.
- The SPA shows a **Console** button on a running AWS lab and opens the URL in a new tab.

### 4.3 Plain SSH, opt-in per lab

A lab declares `aws.direct_ssh: true` in `lab.yaml` (lint: aws runtime only). Its namespace's NetworkPolicy then gets
one more egress rule, and only that lab's:

- TCP 22 to each lab subnet's CIDR, **except the Crucible node's own address (/32)** on the subnet that contains it
  (Kubernetes rejects an `except` outside its CIDR, so the other subnets carry none). Traffic from a pod to its own
  node never crosses a security group, so the except is the only thing keeping lab pods off the node's sshd.
- The subnet CIDRs come from the labs stack (new output `lab_subnet_cidrs`, from the `aws_subnet` data it already
  reads), through up, Helm and `CRUCIBLE_AWS_LAB_SUBNET_CIDRS`. The node's address comes from the API pod's Downward
  API `status.hostIP` (`CRUCIBLE_NODE_IP`). If either is empty, a `direct_ssh` lab refuses to start (fails closed).
- Pod traffic leaves the node masqueraded as the node's address, so the lab's security group must allow TCP 22 from
  it: modules get `var.crucible_ssh_source_cidr` (the node's address /32, "" unless `direct_ssh`).

Keys: `lab-ssh --direct <instance-id> [user]` pushes an Instance Connect key and connects to the private IP; a trainee
may also push their own public key with `send-ssh-public-key`. Breaking sshd, `authorized_keys` or the security group
is then a real exercise, with the serial console or SSM as the way back in.

What this opens: from that lab's pods, port 22 on anything in the lab subnets except the node, including other labs'
instances whose security groups admit the node's address. Those still need a key the trainee can only push to their
own lab's instances (ABAC).

### 4.4 Status egress

`crucible aws status` adds `signin.aws.amazon.com`, `ec2-instance-connect.<region>.amazonaws.com` and
`serial-console.ec2-instance-connect.<region>.aws` (TCP 22) when aws labs are on.

## 5. Workspace image

`deploy/workspace/Dockerfile`, from the pinned `amazon/aws-cli` digest, adds:

- `openssh-clients`, and the Session Manager plugin at a pinned version, checked by sha256;
- `/etc/ssh/ssh_known_hosts` entries for the serial console endpoints, from AWS's published fingerprints;
- `lab-ssh [--direct] <instance-id> [user]`: with `--direct`, pushes the key and connects to the private IP (§4.3);
  without it, makes a throwaway key, sends it with `send-ssh-public-key`, runs ssh with
  `ProxyCommand aws ssm start-session --target %h --document-name AWS-StartSSHSession --parameters portNumber=%p`;
- `lab-console <instance-id>`: sends a serial key (`send-serial-console-ssh-public-key --serial-port 0`), runs
  `ssh -i <key> <id>.port0@serial-console.ec2-instance-connect.$AWS_REGION.aws`.

Built for linux/amd64 and linux/arm64 (`make workspace`) and pushed by the operator to
`ghcr.io/platform-infra-org/crucible-workspace`. Main gets `workspace_image` (by digest, like `toolbox_image`; empty
keeps `amazon/aws-cli`), passed to Helm's `awsLabs.workspaceImage`.

## 6. Authoring

`docs/user/authors/aws-labs.md` gains "Labs on the machine itself":

- `iam_instance_profile = var.crucible_instance_profile`; AMIs with the SSM agent (Ubuntu, Amazon Linux 2023, Windows).
- GRUB on serial: `GRUB_TERMINAL="serial console"`, `GRUB_SERIAL_COMMAND="serial --unit=0 --speed=115200"`,
  `GRUB_TIMEOUT` above 0, `update-grub`, all in user data before any breakage.
- Windows SAC: `bcdedit /ems {current} on` and `bcdedit /emssettings EMSPORT:1 EMSBAUDRATE:115200` in user data.
- Breaking it: after the serial setup, then reboot from user data.
- Passwords never go in user data or state: the trainee sets one over `lab-ssh` before a break, or the lab uses
  `init=/bin/bash` from GRUB, or the rescue volume.
- Checks verify the machine with `aws ssm send-command` and `get-command-invocation`.
- Plain SSH labs: `aws.direct_ssh: true`, and a security group rule allowing TCP 22 from
  `var.crucible_ssh_source_cidr`. Example exercises: sshd moved off 22, a broken `sshd_config`, wrong permissions on
  `authorized_keys`, a security group without the rule.

Example: `examples/forge-402`, module "GRUB rescue": Ubuntu on t3.micro; user data puts GRUB on serial, removes
`/boot/grub/grub.cfg` and reboots to `grub rescue>`; the check confirms the machine is up and `grub.cfg` exists.
A second module, "Locked out" (`direct_ssh: true`): user data sets `PasswordAuthentication no`, breaks
`AuthorizedKeysCommand` and moves sshd to 2222; the trainee gets back in over SSM or the serial console and restores
SSH on 22; the check runs `ssh` to the private IP from the check script. Both lint, price, and run in the dry-run e2e.

## 7. Testing

- Terraform (labs): the instance role has no `ssm:GetParameter*` allow and has the `/<name>/*` deny; PassRole is
  scoped to that role and EC2; StartSession allows only the three documents and the boundary denies the rest; every
  new instance action carries the lab-id condition; serial console access is on by default and off with the variable;
  the profile is an output.
- Go: a `direct_ssh` lab's NetworkPolicy adds TCP 22 to the subnet CIDRs with the node's /32 excepted, and a lab
  without it gets exactly today's policy; a `direct_ssh` lab with no CIDRs or no node address refuses to start;
  `crucible_ssh_source_cidr` is the node /32 only for `direct_ssh`; lint refuses `direct_ssh` outside aws labs;
  the module variable reaches tfvars and `crucible.tf`; lint accepts the instance profile and refuses other IAM;
  the console endpoint answers only the lab's trainee, audits without the URL, rate-limits, and builds the URL against
  an `httptest` federation endpoint; the workspace compose uses the configured image; status lists the new hosts.
- Web: the Console button shows only on a running AWS lab and opens the returned URL.

## 8. Review focus

1. Root on a lab instance reading Crucible's SSM parameters: the custom instance policy and the explicit deny (§3.1).
2. A session document that forwards beyond the instance (`ToRemoteHost`): boundary deny (§3.2).
3. Lab A reaching lab B's instance through any new action: every instance-scoped action keeps the lab-id condition.
4. The console link reaching someone other than the trainee, or the URL leaking into logs or the audit (§4.2).
5. A labs stack from before this change: an empty `crucible_instance_profile` must not break existing labs.
6. A `direct_ssh` lab reaching the node: the /32 except, and failing closed without the node's address (§4.3).

## 9. Verify by hand in our account (read-only or dry-run first)

- `aws ec2 get-serial-console-access-status --region il-central-1`; `aws ec2 enable-serial-console-access --dry-run`.
- labs-init creating the instance profile and passing the role (IAM has no dry run; labs-init stops on AccessDenied).
- Fleet Manager Remote Desktop availability in il-central-1.
- Federated console sign-in allowed by the organization.
- The firewall allowing TCP 22 to `serial-console.ec2-instance-connect.il-central-1.aws` from the node.
- A `direct_ssh` lab: `ssh` from the workspace to a lab instance works, and to the node's private IP times out.
