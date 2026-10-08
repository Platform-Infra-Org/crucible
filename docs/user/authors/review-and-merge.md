---
title: Review and merge
roles: [author]
covers: [route:/edits, route:/edits/:id]
order: 30
---
# Review and merge

Every edit is reviewed before trainees see it.

## The Edits list

A training's **Content edits** panel on [Manage trainings](/docs/leaders/manage-trainings) lists its edits: your
own and the ones you may review. **All trainings' edits** there opens the **Edits** list, the same for every training
at once, pending ones first, then the newest. Each shows its status: *pending*, *merged*, *rejected*, *withdrawn* or *stale*. Open one to see the change
as a diff, and each changed file in full.

## What reviewers see

An edit can change files, and it can also rename and delete them. The diff shows all of it:

- A renamed file shows as *rename from* and *rename to*, with any changes made to it on the way.
- A deleted file shows in full, every line marked as removed, so you see exactly what goes.
- A changed file's new text is listed under **Files** as well.

Scripts (`.sh`) inside the module's lab folder become executable; anywhere else they don't. `training.yaml` can be
changed but never renamed or deleted.

## Reviewing

A maintainer of the training, or an admin, reviews the edit, whether or not they are enrolled in it. You never review
your own. A reviewer can leave a note and:

- **Approve and merge**: the bot merges exactly the commit that was reviewed, and Crucible picks up the new version.
- **Reject**: nothing changes in the training.

The author can **Withdraw** a pending edit. Crucible emails the author when the edit is decided, and the maintainers
when an edit is waiting for them.

If someone pushed to the edit's branch outside Crucible, approving puts the reviewed change back on the branch
instead, and the edit needs a fresh approval.

## Stale edits

If the training changed underneath an edit and the two no longer fit, the merge stops and the edit turns *stale*.
Nothing is lost: as the author, choose **Reopen in the editor**. The link is also on your rejected and withdrawn edits.
Crucible opens the draft you submitted it from, or, if you no longer have it, a new draft holding all of the edit's
changes, renames and deletes included, on the version of the training they were made against. **Newer content: rebase
draft** then brings it up to date (see [The editor](/docs/authors/editing-content)).

## Returned edits

A rejected, withdrawn or stale edit reopens in the editor from its page, with your draft as you left it. A merged
draft disappears from your list. Once you change the reopened draft, it is no longer tied to the old edit: submitting
it makes a new one.

## Limits

You can have at most 5 edits waiting for review, and propose at most 10 edits an hour. Withdraw one, or wait for a
review, if you hit a limit.
