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

The platform repo commit Crucible runs, when it last synced, and any error. Below it, every training: its repo,
branch, head commit, and the problems that keep it from loading, if any.

## Platform settings

The cost tiers, escalation hours, schedules and admins this Crucible runs with. They live in the platform repo
(`platform.yaml`, `admins.yaml`): change them there, and Crucible picks them up on the next sync.

## Recent privileged actions

The latest entries of the audit log. See [First admin, audit and runbooks](/docs/admins/first-admin-audit-and-runbooks).
