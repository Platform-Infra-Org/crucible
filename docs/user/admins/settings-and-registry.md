---
title: Forge settings and the registry
roles: [admin]
covers: [route:/admin/settings, route:/admin/trainings]
order: 20
---
# Forge settings and the registry

**Administrator** in the top bar gathers the admin-only pages. Two of them, **Forge settings** and **Registry**,
appear only when this Crucible keeps its configuration in its own database. If you don't see them, your
configuration lives in the platform repo instead: change it in git, and Crucible picks it up on the next sync. See
**Sync** on [Forge Status](/docs/admins/forge-status-and-kill-switch).

Everything on these pages is checked before it is saved, and every save is written to the audit log with your name,
what it touched, and the values before and after.

## Forge settings

**Look** — the default theme for people who haven't chosen one, and the quotes shown while the forge loads, one per
line.

**Lab spending** — the three cost tiers that decide who may approve a lab:

- *Auto-approve under*: nobody is asked.
- *Program approver up to*: the program's approvers, or the team leader.
- *Admin approval above*: an admin only.

Also here: the **cluster lab rate** per hour, and the **hours before a waiting request escalates**. Leave the
cluster rate empty and cluster labs become unavailable rather than free — a missing price is never treated as no
cost. The tiers go in as a set: give all three or none.

**Forge ranks** — the percentage of a training a trainee must finish to reach each rank. Ranks only ever rise.

**Admins** — add an admin by email, remove one with **Remove**. The last admin can't be removed, and the page says
so: losing that account would lock the forge with nobody able to let anyone back in. If there is only one admin, the
page asks you to add a second. Two admins removing each other at the same moment can't both succeed.

**Lab schedules** — a named set of open windows in a time zone, such as `mon,tue,wed,thu,fri 08:00-19:00`. Programs
pick one in their settings. Saving under a name that exists replaces it. A schedule a program still uses can't be
deleted; change the program first. See
[Schedules, budgets and caps](/docs/leaders/schedules-budgets-and-caps) for what a schedule does to a lab.

## Registry

**Registry** is the list of trainings this Crucible knows about: an id, a repository and a branch. A training has to
be registered before any team can be enrolled in it.

- **Register a training** — the id is what programs refer to, so keep it short and stable (`forge-101`). The
  repository must be an `https://`, `http://`, `ssh://`, `git://` or `user@host:path` URL. Local paths are refused
  on an ordinary install. Credentials in the URL are stored so cloning works, but the page and the audit log show
  them as `***@`.
- **Edit** fills the form from a registered training so you can **repoint** it at another repository or branch — for
  a move between hosts, say. Crucible warns you first and names how many programs are affected, because the content
  versions those programs are pinned to belong to the repository you are leaving. Registering the same repository
  and branch again changes nothing and writes no audit row.
- **Unregister** removes a training. One that a program still uses can't be unregistered; remove that program first.

Content itself always comes from git, whichever way this Crucible stores its configuration. The registry only says
where to look. What Crucible found there — the head commit, and anything that stopped it loading — is under **Sync**
on Forge Status.
