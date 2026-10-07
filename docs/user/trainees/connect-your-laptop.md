---
title: Connect your laptop
roles: [trainee]
covers: [route:/connect, feature:agent]
order: 40
---
# Connect your laptop

Laptop labs run through `crucible-agent`, a small program you start on your own machine. It needs Docker with the
compose plugin, on macOS, Linux, or Windows with WSL2.

## Pairing

1. Get `crucible-agent` for your platform from your admin.
2. Open **Connect your laptop** in the top bar and choose **Generate pairing token**.
3. Copy the command Crucible shows and run it in a terminal. Leave it running while you do labs.

The page says *Agent connected. Your forge is lit.* once the agent has reached Crucible.

The command passes the token in an environment variable, `CRUCIBLE_TOKEN`, not as an argument, so other people on
the same machine can't read it from the process list. The token is shown once; generate a new one if you lose it.

## One agent at a time

A new token replaces the old one, and the old one stops working. Run one agent per account: starting it on another
laptop disconnects this one.

## Revoking

**Revoke pairing** on the same page disconnects your agent and voids its token. Labs already running on your laptop
keep running until they go idle or reach their time limit. When someone leaves the company, an admin can revoke their
pairing too.
