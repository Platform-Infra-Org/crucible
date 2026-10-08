---
title: Manage trainings
roles: [leader, admin, author]
covers: [route:/trainings/manage, route:/trainings/manage/:training]
order: 5
---
# Manage trainings

**Manage trainings** is the one place for everything about who takes a training and who runs it: start a training
for a team, enroll people, see who is enrolled, hand out roles and edit the content. Admins also register trainings
here.

Open it with **Manage trainings** at the top of the **Trainings** page, by choosing a training's card there (it opens
that training), from **Administrator → Trainings**, or with
**Manage** next to a program on your team page. It is there for admins, team leaders, program managers, and anyone who may edit a training's content.

The page lists the trainings on the left; pick one to see it on the right. Under each training's name you see how
many teams run it and how many people are enrolled. You see the teams you belong to; an admin sees every team.

## Registering a training

A training's readings, quizzes and labs live in a git repository: `training.yaml` and `modules/…`. Crucible reads
the content from there. Who takes it, who scores it and who approves its labs are kept in Crucible, never in git.

An admin chooses **+ New training** and gives:

- **Training id** — what programs refer to, so keep it short and stable (`forge-101`).
- **Repository URL** — an `https://`, `http://`, `ssh://`, `git://` or `user@host:path` URL. Local paths are refused
  on an ordinary install. Credentials in the URL are stored so cloning works, but the page and the audit log show
  them as `***@`.
- **Branch** — usually `main`.

Under **Source**, an admin sees where a training's content comes from:

- **Change source** points it at another repository or branch, for a move between hosts, say. Crucible warns you
  first and names how many teams run it, because the content versions they are pinned to belong to the repository you
  are leaving.
- **Delete training** removes the training from Crucible: its connection to the repository, and its program in every
  team that runs it (Crucible names those teams and asks first). The repository itself is not touched, and everyone's
  progress, scores and badges are kept. Register it again and start it for a team to bring it back.

What Crucible found in the repository — the head commit, and anything that stopped it loading — is under **Sync** on
[Forge Status](/docs/admins/forge-status-and-kill-switch).

## Content edits

If you may edit a training, its page has a **Content edits** panel. **Edit content** opens the editor on a fresh
draft; below it are your drafts of this training and its edits waiting for review. See
[The editor](/docs/authors/editing-content) and [Review and merge](/docs/authors/review-and-merge).

## Starting a training for a team

**Start it for a team** lists the teams that don't run the training yet and that you lead (an admin sees every team).
Pick one and choose **Start for this team**. That team now has a *program*: one training, run by one team. Nobody is
enrolled at first.

**Stop for this team** ends the program. Everyone's progress, scores and badges are kept, but nobody can continue
until the training is started for the team again.

## Enrolling people

Each team that runs the training has its own panel. **Enrolled** shows everyone taking it: only they see it on their
Hearth.

To enroll someone, type their email in the box under the list and choose **Enroll**. The box suggests the team's
people who aren't enrolled yet.

- Someone already on the team is enrolled at once.
- Someone who isn't on the team yet: if you lead the team (or are an admin), Crucible asks, then adds them to the team
  as a trainee and enrolls them. Otherwise it tells you to ask the team leader to add them first.

Choose **×** on a name to unenroll them. Their progress is kept.

## Roles

Each program has three roles:

- **Managers** change the program: who is enrolled, the roles, its lab settings and its content version.
- **Scorers** score written answers, uploads and lab reviews on the [Anvil](/docs/scorers/the-anvil). Nobody scores a
  training they are enrolled in.
- **Approvers** approve lab requests that cost money, within the
  [cost tiers](/docs/leaders/schedules-budgets-and-caps).

Leave a role empty and it takes the default: the leader manages and approves, the seniors score. The panel shows who
that is today. The default follows the team, so a new leader or a newly promoted senior takes it over without anyone
touching the program.

To give someone a role, pick them from **Add a manager**, **Add a scorer** or **Add an approver**. Only the team's
people are offered. Choose **×** to take a role away; when the last name goes, the default applies again.

## Lab settings and content version

Open **Lab settings** for the schedule, the lab time limit, the idle timeout, the longest extension a trainee may
take, the program's monthly budget, and whether scorers review self-reported (laptop) lab results. Choose **Save lab
settings**. See [Schedules, budgets and caps](/docs/leaders/schedules-budgets-and-caps).

Open **Content version** to see which version of the training the program runs, and to pin it. See
[The Ledger and content versions](/docs/leaders/ledger-and-content-versions).

## Who can change what

Admins can change everything. The team leader and the program's managers change their programs; the leader also
starts programs for the team and adds new people to it. Everyone else on the team sees the panel read-only.

Every change is checked again when it reaches the server and written to the audit log with your name and the values
before and after.

## When someone else saved first

Every change carries the version it started from. If someone else saved in between, Crucible refuses yours rather
than overwrite their change, and offers **Reload**. Reload, and make your change again.
