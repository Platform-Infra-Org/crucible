# Teams, tags and training roles

Replaces spec §5.2–5.3 (team roles, program roles) and the per-team *program* model. Owner's decisions, 2026-10-11.

## People and teams

- **Crucible admins** (unchanged): every permission. They create and delete teams, add people to a team, remove them,
  and move them between teams, and run the instance.
- **One team per person.** Moving someone to another team takes them out of the old one (and out of its mentor pairs).
- **Tags.** Every member holds exactly one tag in their team: `leader`, `senior`, `junior` or `trainee` (*member* is
  now *junior*). A team has one or more leaders and never none.
- **Leaders** change the tags of their own team's members, including making another leader, and set mentor pairs.
  They pick only from people already on the team; they never add or remove people. Moving or removing the last leader
  of a team is refused.

## Groups

A role is given to a person (an email) or to a group:

| Ref | Who |
|---|---|
| `platform` | everyone on team `platform` |
| `platform_seniors` | the seniors of `platform` (also `_leaders`, `_juniors`, `_trainees`) |
| `all` | everyone on every team |
| `all_seniors` | the seniors of every team (also `all_leaders`, `all_juniors`, `all_trainees`) |

Groups resolve whenever the configuration is read, so promoting someone or moving them changes what they may do at
once. A ref naming a team that no longer exists resolves to nobody and is shown as unknown. `all` and team ids ending
in `_leaders`, `_seniors`, `_juniors` or `_trainees` are reserved.

## Trainings

- **Owned by one team.** Any leader, senior or junior can register a training (repo, ref, folder); their team owns it.
  An admin registers one for any team. One run per training: one pinned content version, schedule, lab defaults,
  budget and self-reported-lab review setting. Labs of a training are charged to its owning team.
- **Managers** are the owning team's leaders, seniors and juniors, the training's admins, and Crucible admins. They
  give roles, change the source, pin versions, change the run settings, and delete the training. Only a Crucible admin
  moves a training to another team.
- **Roles**, each a list of people and groups chosen from everyone Crucible knows:
  - **participant** takes the training.
  - **editor** proposes content edits and reviews other people's; scores submissions.
  - **admin** everything an editor does, plus manages the training and approves paid labs up to tier 1.
- **Paid labs**: training admins approve up to tier 1, the owning team's leaders up to tier 2, Crucible admins above.
  Nobody approves their own lab.
- **Scoring**: editors and training admins score. Unchanged: nobody scores a training they participate in (admins
  included) or their own submission.
- **Answer keys**: participants never see them unless they may edit the training (editors and admins keep editing and
  reviewing while they participate, as before).
- `maintainers` in `training.yaml` no longer grants anything; editors and admins review edits.

## Storage

- `team_members.role` ∈ leader, senior, junior, trainee; `email` unique (one team per person); several leaders.
- `programs` keeps one row per training (`UNIQUE (training)`); its `team` is the owner. A training with no row has no
  owner (registered before this change); an admin gives it to a team.
- `training_roles (training, ref, role)` replaces `enrollments` and `program_roles`. Migration: enrollments become
  participants, managers and approvers admins, scorers editors; a training run by several teams keeps the run with the
  most participants and gathers everyone's participants into it; a person on several teams keeps one membership.
- Visibility of a team page: its members and admins. Team leaders and seniors see their own members' progress.
