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

## The layout

The editor is laid out like VS Code. Down the far left, the activity bar switches the side panel between
**Explorer**, **Problems**, **Changes** and **Blocks**; Problems and Changes show their count on the icon. The editor
sits in the middle with a tab for each open file, and the preview on the right. Above them, the bar holds **All edits**
(back to the list), the training's name and the draft's title. The code editor takes its colours from your theme and
changes with it.

Drag the line between two panes to resize them: the side panel, the editor and the preview each keep a minimum width.
You can also focus a line with Tab and use the arrow keys, or **Home** and **End** for the narrowest and widest.
Double-click a line to put its default width back. The **×** in the preview's header hides it; the button at the right
of the tabs shows it again. Crucible remembers your sizes in this browser.

## The explorer

The explorer shows the training's files as a folder tree. Choose a folder to open or close it, and a file to open it.
The folders of the file you have open always open to show it; **Collapse all** in the explorer's header closes them all.

- **New file** in the header asks for the path of the new file, such as `modules/01-sparks/reading/intro.md`. You type
  it in place in the tree: **Enter** creates it, **Esc** cancels, and a path that isn't allowed says why under it.
- **New module** opens the Module block under **Blocks**.
- Right-click a file for **New file** (in its folder), **Rename** and **Delete**; right-click a folder for **New file**
  in it. Rename edits the path in place, like New file. `training.yaml` stays put: you can change it, but not rename
  or delete it.
- Files you can't edit here are greyed, and say why when you point at them (for example, a Terraform file).
- A • marks the files you have changed.

From the keyboard, the arrow keys move through the tree (**→** opens a folder, **←** closes it or goes to its folder),
**Enter** opens a file, **F2** renames it, **Delete** deletes it (after asking), and the menu key or **Shift+F10** opens
the same menu as a right-click.

## Blocks

**Blocks**, in the activity bar, lists every building block by group: modules, readings, quizzes and their questions, labs
and their tasks, hints and terminals. Choose one to fill in its form.

- Required fields are marked with `*`, and each field has a line of help under it.
- Where a block goes into a module, you pick the module from a list; a hint asks for the task too, picked from the
  module's lab.
- **Add to the draft** writes the files. The file it changes opens with the new lines selected, and the training is
  checked for problems at once. If that file was already open in a tab, **Ctrl/Cmd+Z** there takes the new lines out
  again. If something is wrong, such as a module without a lab, the form says so.
- If you change one of the block's files while it is being added, nothing is added and the form asks you to add it
  again, so your typing is never overwritten.
- You can still type the same YAML by hand; a block only saves you the typing.
- A new training repo and an AWS lab can't be added here: a new training is a new repo, and an AWS lab needs Terraform
  files, which the browser editor doesn't write. Their blocks show an example to copy into git.

Every block's fields and an example of what it writes are in the reference:
[Training](/docs/authors/blocks/training), [Module](/docs/authors/blocks/module),
[Reading](/docs/authors/blocks/reading), [Quiz](/docs/authors/blocks/quiz), [Lab](/docs/authors/blocks/lab) and
[AWS](/docs/authors/blocks/aws).

## Tabs

Every file you open gets a tab above the editor, so you can switch between them without losing your place or your
undo history. A • on a tab means the file has changes. **×** closes a tab; your changes stay in the draft.

## Preview

The preview beside the editor follows the file you have open.

- A reading renders the way trainees will see it, except for images, which don't show in the preview.
- A `quiz.yaml` becomes a playable quiz with the right answers marked. Only editors ever see those marks, and they
  can read the files anyway. **Try it** checks your answer to a question. Questions scored by a person or in a lab
  are described, not graded. A pattern answer is checked by your browser here; Crucible's own check can differ on an
  unusual pattern, so try those in `crucible preview`.
- A `lab.yaml` lists its tasks, terminals and what each hint costs.

Nothing runs in the preview. To try a lab, use `crucible preview` (see [Lint and preview](/docs/authors/lint-and-preview)).

## Saving

There is no save button. Crucible saves your draft on the server about 2 seconds after you stop typing, and the
status bar at the bottom says when it last saved. You can have 5 open drafts at a time; submit or discard one to start
another.

If a draft can't be saved as it is (over 1 MiB, or a path that isn't allowed), the status bar says why. Fix it and
saving resumes. If you close or reload the page before your latest change is saved, the browser asks first. If you
leave the editor through a link in Crucible, your latest change is saved on the way out; when it can't be saved (the
status bar says why), Crucible asks before it leaves.

## YAML help

In `training.yaml`, `module.yaml`, `quiz.yaml` and `lab.yaml`, the editor suggests the keys that fit where you are,
offers the allowed values, and shows what a key means when you hover over it. The hover ends with a link to
[Building blocks: every key](/docs/authors/building-blocks), which opens in a new tab.

## Problems

Crucible checks the training about a second after you stop typing, with the same rules as `crucible lint`. Problems
show as squiggles in the file and are listed under **Problems**; click one to jump to its line.

## Changes

**Changes** lists what a reviewer will see, file by file, in the same diff view. Hidden characters are marked.

## Keyboard

- **Ctrl/Cmd+P** goes to a file.
- In the explorer: **F2** renames, **Delete** deletes, **Shift+F10** opens the file's menu.
- **Ctrl/Cmd+Shift+P** opens the command palette.
- **Ctrl/Cmd+F** and **Ctrl/Cmd+H** find and replace.
- **Ctrl+Shift+F6** or **Ctrl+Alt+↑** leaves the editor.
- Every panel is reachable with Tab.

## Small screens

On a narrow screen, Files, Editor and Preview become tabs, and the panes don't resize.

## Leaving the editor

The editor keeps the Tab key for indenting. To move focus out of it, press **Ctrl+Shift+F6** or **Ctrl+Alt+↑**.

## Submitting

Give the draft a short title and choose **Submit for review**. Crucible checks the training with your change before it
pushes anything. If the change would break the training, it says what is wrong and nothing is pushed.

A draft in review is read-only. To keep working on it, open its edit and withdraw it.

## Newer content

When the training moved on while you worked, the editor offers **Newer content: rebase draft**. The editor is read-only while
it checks. Files you didn't touch update silently. For a file changed on both sides, you see the newest version on the left and yours on the right. Edit
the right side and choose **Use the right side**, or choose **Drop my change (take theirs)**. A rename or delete of a file that changed upstream can be kept or
dropped; if the file is gone upstream, or a rename's new name now exists, you can only drop it. If you renamed a file and
changed it, and the original changed upstream, you merge upstream's version into yours the same way; if you drop the
rename, the text you keep goes back under the original name. If the original was deleted upstream, the rename can only
be dropped, and the text you keep stays under the new name as a new file. **Cancel** leaves the draft as it was. You need to rebase before you can submit.

## Discarding a draft

**Discard draft**, in the status bar or beside the draft under **My drafts**, deletes the draft after asking. It frees
one of your 5 drafts. An edit already submitted from it stays as it is.

## Two tabs on one draft

If you open the same draft in a second browser tab and save there, the first tab stops saving and asks you to reload.
Reload it to pick up the latest version.

## What an edit may touch

- `training.yaml` and files under `modules/<id>/`, nothing else.
- Only `.md`, `.yaml`, `.yml` and `.sh` files.
- Up to 20 files per edit, each at most 256 KiB of text.
- A rename that only changes the case of letters, such as `Intro.md` to `intro.md`, is refused.

An edit can't change a training's `id` or its maintainers; change those in git.

After you submit, see [Review and merge](/docs/authors/review-and-merge).
