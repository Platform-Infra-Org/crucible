# App Access Plan: SSM Session Manager Tunnel

## The problem

The app runs in a k3s cluster on a single EC2 instance, inside a VPC whose
subnets are **egress-only by design** (confirmed by testing: every spoke
subnet's default route goes through a Gateway Load Balancer endpoint ->
Network Firewall -> a dedicated NAT subnet -> an Internet Gateway literally
named `egress-igw`). There is no subnet in this VPC with an inbound path
from the internet - not for this instance, not for a load balancer, not for
anything placed in the existing subnets. A direct public IP was tested and
confirmed unreachable for exactly this reason.

So: no URL to just hand someone. Anyone who needs to reach the app has to
tunnel in through something that only needs *outbound* access - which is
what AWS Systems Manager (SSM) Session Manager is for.

## Who this plan is for

Colleagues who already have the same AWS SSO access we do
(`Workload-Admin-PS`) - e.g. Josh. **Not** a general "internal users" or
public-facing solution - anyone without that same AWS access can't use this
path at all. If this ever needs to reach people without AWS access, that's
a different, bigger problem (a real ingress path from the networking team,
or a VPN) - out of scope for this plan.

## How it actually works

1. The EC2 instance's SSM Agent holds a permanent *outbound* connection to
   AWS's Systems Manager service - it registered itself the moment it
   booted, using the IAM role attached to the instance.
2. When someone runs `aws ssm start-session`, their laptop opens its own
   *outbound* connection to the same AWS service, carrying their SSO
   identity.
3. AWS bridges the two already-open outbound connections together. Neither
   side ever accepted an inbound connection from the other - that's why
   this works despite the VPC blocking all inbound traffic.
4. For port-forwarding specifically, the `session-manager-plugin` (a
   separate binary from the AWS CLI) opens a plain local port on the
   laptop and shuttles bytes between that local port and the tunnel.

Both sides have to prove their identity before AWS will bridge them: the
instance via its IAM role, the person via their SSO login. Someone without
valid, permitted AWS credentials never gets bridged - there's nothing to
"knock on" even if they tried.

## One-time setup (each person does this once)

1. Install AWS CLI v2.
2. Install `session-manager-plugin` (not bundled with the CLI - a known
   point of friction, has its own installer per OS).
3. `aws configure sso` - point at our org's Identity Center portal, select
   `Workload-Admin-PS`.

## Connecting (each session)

A small script should wrap these three steps so nobody has to remember the
exact commands:

```bash
#!/bin/bash
PROFILE="workload-admin-ps"
INSTANCE_ID="<instance-id>"
LOCAL_PORT=18080
REMOTE_PORT=80   # whatever port the app's Service is actually bound to

aws sso login --profile "$PROFILE"

aws ssm start-session \
  --profile "$PROFILE" \
  --target "$INSTANCE_ID" \
  --document-name AWS-StartPortForwardingSession \
  --parameters "{\"portNumber\":[\"$REMOTE_PORT\"],\"localPortNumber\":[\"$LOCAL_PORT\"]}" &

sleep 3   # crude wait for the tunnel to come up; replace with a real readiness check
open "http://localhost:$LOCAL_PORT" 2>/dev/null || xdg-open "http://localhost:$LOCAL_PORT" 2>/dev/null || start "http://localhost:$LOCAL_PORT"
```

`aws sso login` pops the browser once for approval; the tunnel then starts
in the background; the last line opens a second browser tab at the app
itself. (Windows equivalent: a `.ps1`/`.bat` wrapper doing the same three
calls plus `start http://localhost:18080`.)

## Caveats - real, not hypothetical

- **k3s port binding**: the tunnel reaches a port *on the EC2 instance
  itself*, not automatically into a pod. The app's Service needs to
  actually be bound on the instance's host network on `REMOTE_PORT` (k3s's
  default Traefik/ServiceLB does this for a `LoadBalancer`-type Service) -
  otherwise the tunnel connects to nothing.
- **Not "set up once, forget forever"**: the SSO login expires (hours, org
  policy dependent) and the SSM session itself times out after ~20 minutes
  idle. Re-running the connect script is a recurring thing, not a one-time
  event.
- **One tunnel per person, not a shared gateway**: each person who wants in
  runs their own session on their own local port. No conflict between
  people, but also nothing to "route users to" collectively - it isn't a
  reverse proxy.
- **Confirm `ssm:StartSession` is actually granted** on this instance for
  `Workload-Admin-PS` before assuming it works - likely already covered
  given how broad that role is, but worth checking once rather than finding
  out mid-demo.
- **Doesn't scale past people who already have our AWS access.** This is a
  stopgap for a couple of colleagues, not the app's real access story.

## If this needs to grow beyond a couple of people

The actual fix is a real ingress path - a load balancer sitting in a
subnet with genuine inbound reachability, which doesn't exist anywhere in
this VPC today and would need the networking team to provision. Worth
raising with them if/when this plan starts feeling like it's being used as
more than a stopgap.
