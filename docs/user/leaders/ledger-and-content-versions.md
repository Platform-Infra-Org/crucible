---
title: The Ledger and content versions
roles: [leader, approver]
covers: [route:/ledger, feature:content.versions]
order: 50
---
# The Ledger and content versions

## The Ledger

**Ledger** shows in the top bar for team leaders, program managers, approvers and admins. It covers this month's lab
spend for the teams you may see:

- **Budgets**: how much of each team's and program's budget is spent.
- **Day by day**: spend per day.
- **Running now** and **Labs this month**: each lab with who asked, its runtime, estimate and cost so far.
- **Top spenders**.

Running labs count at their hourly estimate. With AWS labs there is more: a finished AWS lab switches to its real AWS
bill once that settles (about two days later), **Estimate vs actual** shows how close the estimates were, and the
reaper lists cloud resources left behind. If the AWS figures go stale, the Ledger says so; the estimates keep
enforcing the budgets meanwhile.

## Content versions

A program runs one version of its training. Labs always run on the exact version they started with.

By default a program follows the training's branch: new content reaches trainees as soon as it is merged. Program
managers can pin it instead, under **Content version** in the program's panel on
[Manage trainings](/docs/leaders/manage-trainings):

- When the program is behind, **Show what changed** lists the new commits and the files they change. Read them, then
  **Pin to** the new version.
- **Follow the branch head** unpins it again.

Admins see every program's version, and whether it is behind, on Forge Status.
