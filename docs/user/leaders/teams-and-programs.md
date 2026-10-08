---
title: Teams and programs
roles: [leader]
covers: [route:/teams, route:/teams/:team]
order: 10
---
# Teams and programs

**Teams** in the top bar opens your team, or the list of your teams if you are on more than one. Your user card (your
icon at the top right) lists your teams too. Each team on the list
is a card with your role in it.

A team's page opens with its leader, a **Journey** button, and four numbers: how many people are on the team, how many
of them are trainees, how many trainings it runs and how many people are enrolled in them. Below come **People**,
**Trainings**, **Budget** and, for admins, **Laptop agents** and **Delete this team**.

Everything you save here is kept in Crucible's own database, with an audit entry naming you and the values before and
after. Your save is permission-checked again before it lands.

## People

A team has one **leader**, and **seniors**, **members** and **trainees**. **Mentors** pair a trainee with the person
who looks out for them.

An admin creates a team from the list of teams: choose **+ Start a team**, then give an id, a name and the leader's
email, and choose **Create team**. The leader fills it from there.

**People** shows everyone by role, and each mentor pair as *trainee → mentor*. The leader opens **Edit the roster**
to change it: one email per line, mentors as `trainee = mentor`, then **Save roster**. Everyone else on the team sees
it read-only.

An admin deletes a team with **Delete team** at the bottom of its page. Crucible asks first. The team, its roster,
mentors and budget go, and every training it runs stops for it; everyone's progress, scores and badges are kept.

## Programs

A program is one training run by one team. The **Trainings** table lists the team's trainings, how many people are
enrolled, the lab schedule and the budget.

Starting a training for the team, enrolling people and giving out roles all happen on
[Manage trainings](/docs/leaders/manage-trainings). **Manage** next to a program opens it there.

## When someone else saved first

Every save carries the version it started from. If someone else saved in between, Crucible refuses yours rather than
overwrite their change. Reload, and make your change again.
