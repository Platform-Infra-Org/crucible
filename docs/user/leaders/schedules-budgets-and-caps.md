---
title: Schedules, budgets and caps
roles: [leader, approver]
covers: [feature:budgets, feature:schedules]
order: 20
---
# Schedules, budgets and caps

Labs cost money and time. These settings keep both in bounds.

## Schedules

Your admins name schedules, such as office hours, each a set of days and hours in a time zone. A training picks one
in its lab settings, or **Any time**. (A training can also carry hours of its own, rather than a named schedule.)

Inside a schedule:

- Labs can only be requested while a window is open. Outside it, the lab page says when the next one opens.
- A running lab ends when the window closes, and can't be extended past it.
- Approval escalation only counts open hours.

## Lab time limits

Each lab runs for a time limit (two hours unless the lab or training says otherwise) and stops early when left idle
(30 minutes by default). A lab's own settings win over the training's. A participant can extend a lab once, by at most the
training's **Max extension**.

## Budgets and hard caps

There are two levels, both counted per calendar month:

- **The team**: a monthly budget and a hard cap (the budget, unless set higher). Admins set these on the team page.
  Every training the team owns is charged here, whoever takes it.
- **The training**: its monthly budget, which is also its hard cap. The training's managers set it.

At 80% of a budget, and again at its hard cap, the team's leaders and the admins get an email (for a training's
budget, its admins too). A request that
would pass a hard cap needs an admin, and approving it is an audited override. A running lab ends when its cost
would reach the cap. Budgets fail closed: if Crucible can't read the spend, it starts nothing.

## How labs are priced

- **Laptop labs** are free.
- **Cluster labs** are free: they run on the trainee's own cluster.
- **AWS labs** are estimated from their Terraform with infracost. A lab priced above its own hourly limit, or in a
  region this Crucible doesn't allow, can't be requested.

The estimate is the hourly price times the lab's time limit.
