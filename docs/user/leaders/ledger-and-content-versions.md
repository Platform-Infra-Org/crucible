---
title: The Ledger and content versions
roles: [leader, approver]
covers: [route:/ledger, feature:content.versions]
order: 50
---
# The Ledger and content versions

## The Ledger

**Ledger** shows in the top bar for team leaders, trainings' admins and Crucible admins. It covers this month's lab
spend for the teams you may see:

- **Budgets**: how much of each team's and training's budget is spent.
- **Day by day**: spend per day.
- **Running now** and **Labs this month**: each lab with who asked, its runtime, estimate and cost so far.
- **Top spenders**.

Cluster labs run on the trainee's own cluster and cost nothing in Crucible, so they are not in the Ledger.

Running labs count at their hourly estimate. With AWS labs there is more: a finished AWS lab switches to its real AWS
bill once that settles (about two days later), **Estimate vs actual** shows how close the estimates were, and the
reaper lists cloud resources left behind. If the AWS figures go stale, the Ledger says so; the estimates keep
enforcing the budgets meanwhile.

## Content versions

A training runs one version of its content. Labs always run on the exact version they started with.

By default a training follows its source, usually a branch: new content reaches participants as soon as it is
merged. A training registered at a tag or a commit stays there until its managers change its source. Its
managers can pin it instead, under **Content version** on
[Manage trainings](/docs/leaders/manage-trainings):

- When the training is behind, **Show what changed** lists the new commits and the files they change. Read them, then
  **Pin to** the new version.
- **Follow the source head** unpins it again.

Admins see every training's version, and whether it is behind, on Forge Status.
