---
title: The editor
roles: [author]
covers: [route:/edits/new, route:/edits/drafts/:id]
order: 20
---
# The editor

You can change a training without leaving Crucible. You work in a *draft*; when it is ready, you submit it and it
becomes an *edit*: Crucible pushes it to its own branch, a maintainer reviews it, and the bot merges it.

## Who can edit

**Edits** shows in the top bar when you may edit at least one training. You may propose edits to a training if you are:

- an admin,
- one of the training's maintainers (listed in its `training.yaml`), or
- a leader or senior of a team that runs the training.

Never if you are enrolled in it, whatever your role: editing means seeing the answer keys.

## Starting an edit

Open **Edits**, choose the training and choose **Start an edit**. Crucible lays out a fresh draft and opens the
editor. Your drafts are listed under **My drafts** on the same page, so you can come back to one later.

## The explorer

The left side lists the training's files. Choose one to open it.

- **New file** asks for the path of the new file, such as `modules/01-sparks/reading/intro.md`.
- **Rename** and **Delete** sit beside each file. `training.yaml` stays put: you can change it, but not rename or
  delete it.
- Files you can't edit here are greyed, and say why (for example, a Terraform file).
- A • marks the files you have changed.

## Tabs

Every file you open gets a tab above the editor, so you can switch between them without losing your place or your
undo history. A • on a tab means the file has changes. **×** closes a tab; your changes stay in the draft.

Markdown files show a preview beside the editor, the way trainees will see them.

## Preview

The preview beside the editor follows the file you have open.

- A reading renders the way trainees will see it. Images you have only added to the draft don't show yet.
- A `quiz.yaml` becomes a playable quiz with the right answers marked. Only editors ever see those marks, and they
  can read the files anyway. Questions scored by a person or in a lab are described, not graded.
- A `lab.yaml` lists its tasks, terminals and what each hint costs.

Nothing runs in the preview. To try a lab, use `crucible preview` (see [Lint and preview](/docs/authors/lint-and-preview)).

## Saving

There is no save button. Crucible saves your draft on the server about 2 seconds after you stop typing, and the
status bar at the bottom says when it last saved. You can have 5 open drafts at a time; submit or discard one to start
another.

If a draft can't be saved as it is (too big, or a path that isn't allowed), the status bar says why. Fix it and saving
resumes.

## YAML help

In `training.yaml`, `module.yaml`, `quiz.yaml` and `lab.yaml`, the editor suggests the keys that fit where you are,
offers the allowed values, and shows what a key means when you hover over it.

## Problems

Crucible checks the training about a second after you stop typing, with the same rules as `crucible lint`. Problems
show as squiggles in the file and are listed under **Problems**; click one to jump to its line.

## Changes

**Changes** lists what a reviewer will see, file by file, in the same diff view. Hidden characters are marked.

## Keyboard

- **Ctrl/Cmd+P** goes to a file.
- **Ctrl/Cmd+Shift+P** opens the command palette.
- **Ctrl/Cmd+F** and **Ctrl/Cmd+H** find and replace.
- **Ctrl+Shift+F6** or **Ctrl+Alt+↑** leaves the editor.
- Every panel is reachable with Tab.

## Small screens

On a narrow screen, Files, Editor and Preview become tabs.

## Leaving the editor

The editor keeps the Tab key for indenting. To move focus out of it, press **Ctrl+Shift+F6** or **Ctrl+Alt+↑**.

## Submitting

Give the draft a short title and choose **Submit for review**. Crucible checks the training with your change before it
pushes anything. If the change would break the training, it says what is wrong and nothing is pushed.

A draft in review is read-only. To keep working on it, open its edit and withdraw it.

## Two tabs on one draft

If you open the same draft in a second browser tab and save there, the first tab stops saving and asks you to reload.
Reload it to pick up the latest version.

## What an edit may touch

- `training.yaml` and files under `modules/<id>/`, nothing else.
- Only `.md`, `.yaml`, `.yml` and `.sh` files.
- Up to 20 files per edit, each at most 256 KiB of text.

An edit can't change a training's `id` or its maintainers; change those in git.

After you submit, see [Review and merge](/docs/authors/review-and-merge).
