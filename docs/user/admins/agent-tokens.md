---
title: Revoking laptop agents
roles: [admin]
covers: [feature:agent.revoke]
order: 20
---
# Revoking laptop agents

Trainees pair their laptops with Crucible through `crucible-agent` and a pairing token. When someone leaves, or a
laptop goes missing, revoke their pairing.

## How

1. Open **Teams** and choose the person's team.
2. Under **Laptop agents**, find their email and choose **Revoke agent**. Crucible asks you to confirm.

Their token stops working and their agent is disconnected at once. The revocation is recorded in the audit log under
your name.

## What keeps running

Labs already running on their laptop are not stopped by the revocation: Crucible can no longer reach them, so they end
when they go idle or reach their time limit, as after any disconnect. To stop every lab at once, use the kill switch
on [Forge Status](/docs/admins/forge-status-and-kill-switch).

## Trainees can do it too

Anyone can revoke their own pairing with **Revoke pairing** on **Connect your laptop**, and generating a new token
always revokes the old one.
