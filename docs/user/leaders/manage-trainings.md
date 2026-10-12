---
title: Manage trainings
roles: [leader, admin, author]
covers: [route:/trainings/manage, route:/trainings/manage/:training]
order: 5
---
# Manage trainings

**Manage trainings** is the one place for everything about a training: register it, point it at its content, choose
who takes it, who edits it and who runs it, set how its labs run, and edit the content.

Open it with **Manage trainings** at the top of the **Trainings** page, from **Administrator → Trainings**, or with
**Manage** next to a training on your team page. It is there for leaders, seniors and juniors, for anyone who edits or
runs a training, and for admins.

The page lists, on the left, the trainings you work on: the ones your team owns, the ones you are an editor or admin
of, and every training for an admin. Under each name you see the team that owns it and how many people take it.

## Registering a training

A training's readings, quizzes and labs live in a git repository: `training.yaml` and `modules/…`. Crucible reads
the content from there. Who takes it, edits it and runs it is kept in Crucible, never in git.

Any leader, senior or junior can choose **+ New training**; their team owns what they register. An admin picks the
team. Give:

- **Repository**: one an admin has connected, under
  [Administrator → Repositories](/docs/admins/repositories). The credential, if it needs one, lives with the
  connection, so you never type one here.
- **Branch, tag or commit**: what Crucible follows, usually `main`. A branch moves: what is merged there reaches
  participants, and the training takes content edits from Crucible. A tag or a commit id stays on that one version,
  and changes only in git, so Crucible offers no **Edit content** for it.
- **Folder**: where `training.yaml` sits in the repository, such as `trainings/forge-101`. Leave it empty when it
  is at the root. One repository can hold several trainings this way, each in its own folder.

You are not asked for an id or a title: Crucible reads them from the `training.yaml` it finds, because they belong to
the content. Rename a training by changing its `title` there. Its `id` is what programs, progress and scores are
keyed by, so changing that makes a different training.

**A repository with nothing in it yet** gets a training to start from — a `training.yaml` and a first module with a
reading, committed for you and named after the folder you registered (or the repository, at its root). Connect a new
repository, register it, and go straight to **Edit content**. Crucible only does this when the repository has no
commits at all; one that has content but no `training.yaml` where you pointed is an error, not an invitation to
write one.

A training belongs to one team. Its **managers** are that team's leaders, seniors and juniors, and the training's own
admins. Only a Crucible admin gives a training to another team, under **Team**; its roles, version, schedule and
budget move with it. A training no team owns yet (registered before teams owned trainings) waits there for an admin to
give it to one.

Under **Source**, managers see where the content comes from:

- **Change source** points it at another repository, ref or folder, for a move between hosts, say, or a release
  tag. Leave the repository URL empty to keep the current one (with its credentials). When the repository or folder
  changes and the training is pinned, Crucible warns you first: the pinned version belongs to the content you are
  leaving.
- **Delete training** removes the training from Crucible: its connection to the repository, its roles and its
  settings. Crucible asks first. The repository itself is not touched, and everyone's progress, scores and badges are
  kept. Register it again to bring it back.

What Crucible found in the repository (the head commit, and anything that stopped it loading) is under **Sync** on
[Forge Status](/docs/admins/forge-status-and-kill-switch).

## Content edits

If you may edit a training, its page has a **Content edits** panel. **Edit content** opens the editor on a fresh
draft; below it are your drafts of this training and its edits waiting for review. See
[The editor](/docs/authors/editing-content) and [Review and merge](/docs/authors/review-and-merge).

## Roles

Each training has three roles:

- **Participants** take the training: only they see it on their Hearth.
- **Editors** propose content edits and review other people's, and score written answers, uploads and lab reviews on
  the [Anvil](/docs/scorers/the-anvil). Nobody scores a training they take, or their own work.
- **Admins** do what editors do, manage the training like its team's leaders, seniors and juniors, and approve its
  paid labs up to the first cost line (see [Approvals](/docs/leaders/approvals-and-extensions)).

Give a role to anyone Crucible knows, from any team, or to a group (see
[Teams and tags](/docs/leaders/teams-and-programs)): `all`, `all_trainees`, `platform`, `platform_seniors`… Type in the
box under a role and pick from the suggestions (people with their team and tag, and every group with who it is), or
type an email for someone who hasn't signed in yet; press Enter or **Add**. A group shows with a flag, and the line
above says how many people it is now. Choose **×** to take a role away; their progress is kept.

Groups follow the teams: when a trainee is promoted, they leave `platform_trainees` and join `platform_seniors`, and
what they may do in each training changes at once, without anyone touching the training.

## Lab settings and content version

Open **Lab settings** for the schedule, the lab time limit, the idle timeout, the longest extension a participant may
take, the training's monthly budget, and whether editors and admins review self-reported (laptop) lab results. Choose
**Save lab settings**. Labs are charged to the team that owns the training. See
[Schedules, budgets and caps](/docs/leaders/schedules-budgets-and-caps).

Open **Content version** to see which version of the training runs, and to pin it. See
[The Ledger and content versions](/docs/leaders/ledger-and-content-versions).

## Who can change what

Admins can change everything. A training's managers change its source, roles, settings and version, and delete it.
Its editors see it read-only here and work on its content. Every change is checked again when it reaches the server
and written to the audit log with your name and the values before and after.

## When someone else saved first

Every change carries the version it started from. If someone else saved in between, Crucible refuses yours rather
than overwrite their change, and offers **Reload**. Reload, and make your change again.
