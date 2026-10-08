---
title: Teams and programs
roles: [leader]
covers: [route:/teams, route:/teams/:team, route:/teams/:team/programs/:training]
order: 10
---
# Teams and programs

**Team** in the top bar opens your team, or the list of your teams if you are on more than one.

Everything you save here is kept either in Crucible's own database, with an audit entry naming you and the values
before and after, or — where the configuration lives in git — as a commit the Crucible bot makes to the platform
repo. Either way the history is kept and your save is permission-checked before it lands.

## People

A team has one **leader**, and **seniors**, **members** and **trainees**. **Mentors** pair a trainee with the person
who looks out for them.

Where the configuration lives in Crucible's own database, an admin creates a team on this page — an id, a name and
the leader's email — and the leader fills it from there.

The leader edits the roster: one email per line, mentors as `trainee = mentor`. Everyone else on the team sees it
read-only.

## Programs

A program is one training run by one team. The **Programs** table lists the team's trainings, how many people are
enrolled, the lab schedule and the budget.

To start a new one, pick a training and choose **Enroll the team**. You land on its settings.

## Program settings

The leader and the program's managers can change these; everyone else sees them read-only.

- **Enrolled**: tick the people taking the training. Only they see it on their Hearth.
- **Roles**: managers, scorers and approvers, one email per line. Left empty, the leader manages and approves, and the
  seniors score.
- **Labs**: the schedule, lab time limit, idle timeout, the longest extension a trainee may take, and the program's
  monthly budget. See [Schedules, budgets and caps](/docs/leaders/schedules-budgets-and-caps).
- **Scorers review self-reported (laptop) lab results**: laptop labs then wait for a scorer before they count.

Choose **Save program**. Where the configuration lives in git, the page shows the commit it made.

## When someone else saved first

Every save carries the version it started from — the program's own version number, or the platform repo commit.
If someone else saved in between, Crucible refuses yours rather than overwrite their change. Reload, and make your
change again.
