---
title: Teams and programs
roles: [leader]
covers: [route:/teams, route:/teams/:team]
order: 10
---
# Teams and programs

**Team** in the top bar opens your team, or the list of your teams if you are on more than one.

Everything you save here is kept in Crucible's own database, with an audit entry naming you and the values before and
after. Your save is permission-checked again before it lands.

## People

A team has one **leader**, and **seniors**, **members** and **trainees**. **Mentors** pair a trainee with the person
who looks out for them.

An admin creates a team from the list of teams: an id, a name and the leader's email. The leader fills it from there.

The leader edits the roster: one email per line, mentors as `trainee = mentor`. Everyone else on the team sees it
read-only.

## Programs

A program is one training run by one team. The **Programs** table lists the team's trainings, how many people are
enrolled, the lab schedule and the budget.

Starting a training for the team, enrolling people and giving out roles all happen on
[Manage trainings](/docs/leaders/manage-trainings). **Manage** next to a program opens it there.

## When someone else saved first

Every save carries the version it started from. If someone else saved in between, Crucible refuses yours rather than
overwrite their change. Reload, and make your change again.
