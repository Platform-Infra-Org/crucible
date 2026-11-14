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

- **AWS account this month** (admins and approvers): *$X billed (as of HH:MM) + $Y not yet billed + $Z reserved by
  running labs = $T of $L*, with a meter. Approvers see it read-only; the next section covers what the limit does.
- **Budgets**: how much of each team's and training's budget is spent.
- **Day by day**: spend per day.
- **Running now** and **Labs this month**: each lab with who asked, its runtime and state, and four figures:
  - **Estimate**: what the lab was expected to cost for its full time when it was requested.
  - **Cost so far**: metered spend, or hourly estimate × time run; it counts against the budget until the bill settles.
  - **Metered** (AWS labs): what the lab actually built, priced at AWS list prices for as long as it existed. Crucible
    reads it from the lab's Terraform state after the lab's apply and every 15 minutes while it runs. Once an AWS lab has a metered figure, it becomes the cost so far.
  - **Billed** (AWS labs): what AWS charged, from the billing export, before credits, refunds and discounts. It arrives
    about a day late, and shows "(so far)" until it settles. An install whose AWS account may not have a billing export
    says so under the prices line, and Billed stays empty: spend then comes from Metered and Estimate.
- **Top spenders**.

### The account limit and the billed timeline

When the account's monthly limit is set (**Administrator → Forge settings → Lab spending**), a paid lab that would pass
it waits for an admin: Approvals says *Would pass the account's monthly limit ($X of $L): only an admin can approve
it.*, and the trainee sees *Waiting for an admin: the forge's monthly spend limit is reached.*

AWS bills a day or so late, so the line adds **not yet billed**: Crucible's own meter for the last 24 hours. Under
**Not yet on the AWS bill · last 24 hours** a timeline decides which part of that the bill already covers. Admins:

1. Open the AWS console's month-to-date cost and note the time it covers.
2. Drag the green account marker to that time. Marks snap to clock quarter-hours; the arrow keys move one by 15 minutes.
3. For one lab that the account marker doesn't suit, **Set separately** gives it its own blue marker (◆);
   **Follow account** takes it back. **Count all 24 h** counts the whole window as unbilled.

A band marks the hours the forge was asleep. **Refresh now** also reads the bill, at most once every 5 minutes for all
admins together. Approvers see the timeline but can't move the markers.

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
