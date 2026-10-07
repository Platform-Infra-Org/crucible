---
title: Review and merge
roles: [author]
covers: [route:/edits, route:/edits/:id]
order: 30
---
# Review and merge

Every edit is reviewed before trainees see it.

## The Edits list

**Edits** lists your own edits and the ones you may review, pending ones first, then the newest. Each shows its
status: *pending*, *merged*, *rejected*, *withdrawn* or *stale*. Open one to see the change
as a diff, and each changed file in full.

## Reviewing

A maintainer of the training, or an admin, reviews the edit. You never review your own, and nobody enrolled in the
training reviews it. A reviewer can leave a note and:

- **Approve and merge**: the bot merges exactly the commit that was reviewed, and Crucible picks up the new version.
- **Reject**: nothing changes in the training.

The author can **Withdraw** a pending edit. Crucible emails the author when the edit is decided, and the maintainers
when an edit is waiting for them.

If someone pushed to the edit's branch outside Crucible, approving puts the reviewed change back on the branch
instead, and the edit needs a fresh approval.

## Stale edits

If the training changed underneath an edit and the two no longer fit, the merge stops and the edit turns *stale*.
Nothing is lost: as the author, choose **Redo on the current version**. Crucible opens the current files with the old
diff beside them, so you can make your change again.

## Limits

You can have at most 5 edits waiting for review, and propose at most 10 edits an hour. Withdraw one, or wait for a
review, if you hit a limit.
