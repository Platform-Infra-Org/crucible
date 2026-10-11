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
- Labs that failed to start or are stuck being destroyed, with the trainee, training, state and error.

## Training versions

Every training with the team that owns it, the content version it runs (and whether it is pinned), and the head of
its source. A training that follows its source but hasn't caught up is marked *behind*.

## Sync

When Crucible last fetched training content, and any error reading its configuration. Below it, every training: its
repo, the branch, tag or commit it follows (and its folder, if it isn't at the root), head commit, and the problems that keep it from loading, if any.

## Forge settings

The cost tiers, escalation hours, schedules and admins this Crucible runs with. Change them under **Administrator →
Forge settings**, and they take effect at once. See [Forge settings](/docs/admins/settings-and-registry).

## Recent privileged actions

The latest entries of the audit log. See [First admin, audit and runbooks](/docs/admins/first-admin-audit-and-runbooks).
