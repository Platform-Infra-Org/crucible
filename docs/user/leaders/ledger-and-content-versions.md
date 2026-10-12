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
- **Running now** and **Labs this month**: each lab with who asked, its runtime and state, and four figures:
  - **Estimate**: what the lab was expected to cost for its full time when it was requested.
  - **Cost so far**: metered spend, or hourly estimate × time run; it counts against the budget until the bill settles.
  - **Metered** (AWS labs): what the lab actually built, priced at AWS list prices for as long as it existed. Crucible
    reads it from the lab's Terraform state. Once an AWS lab has a metered figure, it becomes the cost so far.
  - **Billed** (AWS labs): what AWS charged, from the billing export. It arrives about a day late, and shows "(so far)"
    until it settles.
- **Top spenders**.

Cluster labs run on the trainee's own cluster and cost nothing in Crucible, so they are not in the Ledger.

Running labs count at their hourly estimate, or at their metered cost once there is one. With AWS labs there is more:
a finished AWS lab switches to its AWS bill once that settles (about two days later), **Estimate vs billed** shows how
close the estimates were, and the reaper lists cloud resources left behind. If the AWS figures go stale, the Ledger
says so; the estimates keep enforcing the budgets meanwhile.

Under the title, **Price data from** gives the date of the AWS list prices Crucible estimates and meters with. They
refresh every week. If they are missing or more than 30 days old, the line turns red and says they are out of date:
AWS labs can't be estimated or requested until the next refresh succeeds. Admins also see why the last refresh failed.

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
