---
title: Editing content in Crucible
roles: [author]
covers: [route:/edits/new]
order: 20
---
# Editing content in Crucible

You can change a training without leaving Crucible. Every change becomes an *edit*: Crucible pushes it to its own
branch, a maintainer reviews it, and the bot merges it.

## Who can edit

**Edits** shows in the top bar when you may edit at least one training. You may propose edits to a training if you are:

- an admin,
- one of the training's maintainers (listed in its `training.yaml`), or
- a leader or senior of a team that runs the training.

Never if you are enrolled in it, whatever your role: editing means seeing the answer keys.

## Making an edit

1. Open **Edits**, pick the training and choose **Start an edit**.
2. Choose a file on the left. Change it in the text box; Markdown files show a preview beside it. A `*` marks the
   files you have changed.
3. Give the edit a short title and choose **Submit for review**.

Crucible checks the training with your change before it pushes anything. If the change would break the training, it
says what is wrong and nothing is pushed.

## What an edit may touch

- `training.yaml` and files under `modules/<id>/`, nothing else.
- Only `.md`, `.yaml`, `.yml` and `.sh` files.
- Up to 20 files per edit, each at most 256 KiB of text.

An edit can't change a training's `id` or its maintainers; change those in git.

After you submit, see [Review and merge](/docs/authors/review-and-merge).
