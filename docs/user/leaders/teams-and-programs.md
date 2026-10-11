---
title: Teams and tags
roles: [leader]
covers: [route:/teams, route:/teams/:team]
order: 10
---
# Teams and tags

**Teams** in the top bar opens your team, or the list of teams if you are an admin. Your user card (your icon at the
top right) shows your team too. Each team on the list is a card with your tag in it.

A team's page opens with its leaders, a **Journey** button, and four numbers: how many people are on the team, how
many of them are trainees, how many trainings it owns and how many people are taking them. Below come **People**,
**Trainings it owns**, **Budget** and, for admins, **Laptop agents** and **Delete this team**.

Everything you save here is kept in Crucible's own database, with an audit entry naming you and the values before and
after. Your save is permission-checked again before it lands.

## Tags

Everyone on a team holds one **tag**:

- **Leaders** run the team: they change its people's tags and mentor pairs, and approve its trainings' paid labs up
  to the second cost line. A team can have several leaders, and always has at least one.
- **Seniors** and **juniors** run the trainings the team owns, with the leaders: they register trainings and give
  out their roles (see [Manage trainings](/docs/leaders/manage-trainings)).
- **Trainees** take trainings.

A person is on one team. **Mentors** pair a trainee with a leader, senior or junior of the same team who looks out for
them.

## Groups

Tags make groups, which is how a training's roles reach many people at once:

| Group | Who |
|---|---|
| `platform` | everyone on the team `platform` |
| `platform_seniors` | its seniors (also `_leaders`, `_juniors`, `_trainees`) |
| `all` | everyone on every team |
| `all_seniors` | the seniors of every team (also `all_leaders`, `all_juniors`, `all_trainees`) |

A group always means the people who are in it now: promote someone, or move them to another team, and what they may
do changes with it.

## People

**People** shows everyone by tag, and each mentor pair as *trainee → mentor*.

- A **leader** changes a tag under **Change a tag**: start typing, pick a member of the team from the suggestions,
  choose the new tag and **Set tag**. Any tag, leader included, so a leader can make someone else leader too. Crucible
  refuses a change that would leave the team with no leader. **Mentor pairs** holds the pairs, one
  `trainee = mentor` per line.
- An **admin** also adds people under **Add someone**: an email (the box suggests everyone Crucible knows), their tag,
  then **Add to team**. Someone on another team moves: they leave it and its mentor pairs, and Crucible asks first. If
  they were that team's only leader, Crucible refuses; make someone else its leader first. **×** next to a name takes
  someone off the team; their progress is kept.

Everyone else on the team sees the list read-only.

An admin creates a team from the list of teams: choose **+ Start a team**, then give an id, a name and the first
leader's email, and choose **Create team**. The id names the team's groups too, so `all` and ids ending in
`_leaders`, `_seniors`, `_juniors` or `_trainees` are refused.

An admin deletes a team with **Delete team** at the bottom of its page. Crucible asks first. A team that still owns
trainings can't be deleted: give them to another team, or delete them, first. The team, its tags, mentors and budget
go; everyone's progress, scores and badges are kept.

## Trainings it owns

The table lists the trainings the team owns, how many people take each, the lab schedule and the budget. **Manage**
opens one on [Manage trainings](/docs/leaders/manage-trainings), and **Register a training** starts a new one there.

## When someone else saved first

Every save carries the version it started from. If someone else saved in between, Crucible refuses yours rather than
overwrite their change. Reload, and make your change again.
