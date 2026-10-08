---
title: Forge settings
roles: [admin]
covers: [route:/admin/settings]
order: 20
---
# Forge settings

**Administrator** in the top bar gathers the admin pages: **Forge Status**, **Forge settings** and **Trainings**.
Crucible keeps all of its configuration in its own database, so you change it here, never in git.

Everything on these pages is checked before it is saved, and every save is written to the audit log with your name,
what it touched, and the values before and after.

## Forge settings

**Look** — the default theme for people who haven't chosen one, and the quotes shown while the forge loads, one per
line.

**Lab spending** — the three cost tiers that decide who may approve a lab:

- *Auto-approve under*: nobody is asked.
- *Program approver up to*: the program's approvers, or the team leader.
- *Admin approval above*: an admin only.

Also here: the **cluster lab rate** per hour, and the **hours before a waiting request escalates**. Leave the
cluster rate empty and cluster labs become unavailable rather than free — a missing price is never treated as no
cost. The tiers go in as a set: give all three or none.

**Forge ranks** — the percentage of a training a trainee must finish to reach each rank. Ranks only ever rise.

**Admins** — add an admin by email, remove one with **Remove**. The last admin can't be removed, and the page says
so: losing that account would lock the forge with nobody able to let anyone back in. If there is only one admin, the
page asks you to add a second. Two admins removing each other at the same moment can't both succeed.

**Lab schedules** — a named set of open windows in a time zone, such as `mon,tue,wed,thu,fri 08:00-19:00`. Programs
pick one in their settings. Saving under a name that exists replaces it. A schedule a program still uses can't be
deleted; change the program first. See
[Schedules, budgets and caps](/docs/leaders/schedules-budgets-and-caps) for what a schedule does to a lab.

## Moving the forge

**Move this forge**, at the bottom of Forge settings, carries an instance to a new environment, for example from a
test install to the real one, or from one identity provider to another.

**Download an export** saves one JSON file with everything people set up here: settings, admins, schedules, quotes,
trainings, teams with their people, mentors, budgets and webhooks, programs with their roles and enrollments, and
everyone's progress, quiz attempts, scored work, ranks and badges. Each export is written to the audit log.

The file is personal data: names, emails, answers, scores and webhook URLs. Keep it somewhere private, delete it when
the move is done, and never commit it to a repository.

Left behind: running and past labs, spend, sessions and laptop pairings (everyone signs in and pairs again), and the
audit log. Uploaded files stay where they are stored; they open on the new forge when it uses the same storage.

To import, sign in to the new forge as its first admin, open Forge settings, choose the file under **Export file** and
choose **Import**. It only goes into a fresh forge, one with no teams or trainings yet, and it replaces that forge's
settings, schedules and quotes. You stay an admin. The file lands whole or not at all: if anything in it is wrong, such
as a bad email or a repository URL this forge refuses, nothing is imported and the page says what was wrong.

People are matched by email. The first time someone signs in to the new forge, their progress is theirs again.

## Trainings

The trainings this Crucible knows about — registering one, pointing it at another repository, deleting it — are
on [Manage trainings](/docs/leaders/manage-trainings), under **Administrator → Trainings**.
