---
title: Forge Status and the kill switch
roles: [admin]
covers: [route:/admin]
order: 10
---
# Forge Status and the kill switch

**Forge Status** in the top bar is the admin's view of the whole Crucible.

## The kill switch

**Pause all labs** destroys every lab that is starting or running and blocks new requests until you choose
**Resume labs**. Crucible asks before doing either. While labs are paused, nobody can approve a request or an
extension, and pending requests don't escalate; they get their waiting time back when you resume. The page shows who
paused labs and when. Both actions are audited.

## Needs attention

- Content edits waiting for review, with a link to **Edits**.
- Labs that failed to start or are stuck being destroyed, with the trainee, program, state and error.

## Program versions

Every program, the content version it runs (and whether it is pinned), and the branch head. A program that follows
the branch but hasn't caught up is marked *behind*.

## Sync

When Crucible last synced and any error, with the platform repo commit it runs where the configuration lives in
git. Below it, every training: its repo, branch, head commit, and the problems that keep it from loading, if any.
Training content comes from git either way, so this list is always here.

## Platform settings

The cost tiers, escalation hours, schedules and admins this Crucible runs with, and where they are kept. Either:

- **In Crucible's own database** — change them under **Administrator → Forge settings**, and they take effect at
  once. See [Forge settings and the registry](/docs/admins/settings-and-registry).
- **In the platform repo** (`platform.yaml`, `admins.yaml`) — change them in git, and Crucible picks them up on the
  next sync.

Whoever installs this Crucible chooses. The **Administrator** menu tells you which: **Forge settings** and
**Registry** are there only in the first case.

## Recent privileged actions

The latest entries of the audit log. See [First admin, audit and runbooks](/docs/admins/first-admin-audit-and-runbooks).
