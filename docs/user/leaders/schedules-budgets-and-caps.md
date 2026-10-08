---
title: Schedules, budgets and caps
roles: [leader, approver]
covers: [feature:budgets, feature:schedules]
order: 20
---
# Schedules, budgets and caps

Labs cost money and time. These settings keep both in bounds.

## Schedules

Your admins name schedules, such as office hours, each a set of days and hours in a time zone. A program picks one
in its settings, or **Any time**. (A program can also carry hours of its own, rather than a named schedule.)

Inside a schedule:

- Labs can only be requested while a window is open. Outside it, the lab page says when the next one opens.
- A running lab ends when the window closes, and can't be extended past it.
- Approval escalation only counts open hours.

## Lab time limits

Each lab runs for a time limit (two hours unless the lab or program says otherwise) and stops early when left idle
(30 minutes by default). A lab's own settings win over the program's. A trainee can extend a lab once, by at most the
program's **Max extension**.

## Budgets and hard caps

There are two levels, both counted per calendar month:

- **The team**: a monthly budget and a hard cap (the budget, unless set higher). Admins set these on the team page.
- **The program**: its monthly budget, which is also its hard cap. The program's managers set it.

At 80% of a budget, and again at its hard cap, the team leader and the admins get an email (for a program's
budget, its managers too). A request that
would pass a hard cap needs an admin, and approving it is an audited override. A running lab ends when its cost
would reach the cap. Budgets fail closed: if Crucible can't read the spend, it starts nothing.

## How labs are priced

- **Laptop labs** are free.
- **Cluster labs** cost the platform's hourly rate. If your admins haven't set one, cluster labs can't be started at
  all, rather than run for free.
- **AWS labs** are estimated from their Terraform with infracost. A lab priced above its own hourly limit, or in a
  region this Crucible doesn't allow, can't be requested.

The estimate is the hourly price times the lab's time limit.
