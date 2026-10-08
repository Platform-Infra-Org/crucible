---
title: Approvals and extensions
roles: [approver, leader]
covers: [route:/approvals]
order: 30
---
# Approvals and extensions

**Approvals** shows in the top bar when you can approve labs: as a program approver, a team leader or an admin.

## Who decides what

Every lab request is routed by its estimate, using the tiers your admins set:

- **Auto**: laptop and cluster labs up to the auto-approve amount start at once. AWS labs never do.
- **Approver**: up to tier 1, the program's approvers decide.
- **Leader**: up to tier 2, the team leader.
- **Admin**: anything above, and anything that would pass a budget cap.

Someone higher can always decide a cheaper request. Nobody approves their own. A trainee whose lab failed to start
can ask again within the hour without a new approval.

## The queue

Each request shows who asked, the estimate and how it was worked out, who it is waiting for, the team's and the
program's spend this month, the program's schedule, and the trainee's recent labs. Add a note if you like, then
**Approve** or **Reject**. An approved lab starts at once; the trainee gets an email either way.

You can't approve while labs are paused or the program's schedule window is closed.

## Escalation

If nobody decides in time (the escalation hours, counted inside the schedule), the request moves up a tier and the
next people hear about it. If even the admins don't answer, it expires.

## Extensions

When a longer lab would cost enough to need a higher tier, the trainee's extension request lands here too, marked
**Extension:**, with the end time asked for. The same rules decide who may approve it. The lab keeps running while it
waits.
