# Crucible — Configuration and Org in Postgres (Design)

**Date:** 2026-10-07
**Status:** Draft — for review before planning
**Supersedes:** spec §4.1, §4.3, §5.1 (bootstrap), §6.4 (config write-back) and the roadmap's
"Config and content" deviations, for configuration only. Training content (spec §4.2, §4.4, §4.5,
§6.1–§6.3, §6.5) is unchanged.

> *The forge keeps its own books; only the work is carried in from outside.*

---

## 1. Purpose

Today Crucible's configuration lives in a git **platform repo**: admins, teams, membership, mentor
pairs, budgets, cost tiers, schedules, rank thresholds, quotes, the training registry and every
team's enrollment of a training. The UI edits it by committing as a bot.

That has two costs the original design did not weigh:

- **Personal data in git.** Every team member, mentor pair, program role and enrollment is an email
  address in a file whose history is append-only and copied to every clone. A deletion request
  cannot be honoured, and repository read access becomes access to the staff list.
- **An operator has to own a repo to run the app.** Standing up an instance means creating a
  platform repo, giving a bot push access to it, and keeping branch protection compatible with that
  bot — before anyone can add a single person.

This design moves all configuration into Postgres, leaves git as the source of truth for training
content alone, and adds an export/import file so an instance can be moved between environments.

### Goals

- A fresh instance needs one value to start: the bootstrap admin's email. That person configures
  everything else in the UI.
- No email address, name or score is ever written to a git repository.
- An instance's configuration and learning history can be exported to one file and imported into a
  new instance, including one with a different identity provider.
- Training content keeps every property it has now: authored by push, validated on sync, pinned per
  program, labs run at an exact SHA, in-app edits via branches the bot merges.

### Non-goals

- Changing anything about labs, scoring, approvals, budgets *as behaviours*. The rules stay; only
  where their configuration is stored changes.
- Multi-tenancy, or more than one Crucible per database.
- Carrying running labs, spend history or audit entries across an import (§7).
- Replacing `pg_dump` backups. Export/import is for moving an instance deliberately, not for
  disaster recovery, which stays the nightly snapshot (spec §9.4).

---

## 2. Decisions

| Topic | Decision |
|---|---|
| Source of truth for configuration | Postgres |
| Source of truth for training content | git, unchanged |
| Platform repo | removed; `CRUCIBLE_PLATFORM_REPO`, `CRUCIBLE_PLATFORM_BRANCH` and the compose `file://` mount go with it |
| Bootstrap | `CRUCIBLE_BOOTSTRAP_ADMIN` inserts one `admins` row, only while the table is empty |
| Config write-back (bot commits to the platform repo) | deleted. The bot Writer survives for content-edit branches in training repos |
| Who may change configuration | global admins, except team-scoped settings already delegated by the permission matrix (team leader, program manager) — unchanged rules, new storage |
| Export contents | configuration, org, programs, enrollments, **and** learning history (progress, attempts, submissions, scores, badges, ranks, transcripts, uploads) |
| Export identity key | verified email, never OIDC `sub`, so a new identity provider re-links on first login |
| Excluded from export | sessions, agent tokens, lab instances and events, check runs, cost actuals, budget alerts, reaper findings, audit log, kill-switch state |
| Validation | the rules that reject a bad `platform.yaml` today run on save instead; an invalid change is refused, never stored |
| History of privilege changes | `audit_log`, which becomes load-bearing (§9) |
| Fixtures and e2e | `examples/platform` becomes a seed file imported through a dev-only endpoint (§8) |

---

## 3. What moves, what stays

**Moves to Postgres**

| Today | Becomes |
|---|---|
| `platform.yaml`: theme, cost tiers, `cluster_usd_per_hour`, escalation hours, rank thresholds | `settings` (one row) |
| `platform.yaml`: `schedules` | `schedules` |
| `quotes.yaml` | `quotes` |
| `admins.yaml` | `admins` |
| `trainings.yaml` | `trainings` |
| `teams/<id>/team.yaml`: name, leader, seniors, members, trainees | `teams`, `team_members` |
| `teams/<id>/team.yaml`: mentors, notification webhooks | `mentors`, `team_webhooks` |
| `teams/<id>/budget.yaml` | `team_budgets` |
| `teams/<id>/programs/<training>.yaml` | `programs`, `program_roles`, `enrollments` |

**Stays in git** — the training repos: `training.yaml`, `modules/<id>/…`, readings, quizzes, labs,
checks, setups, hints, assets. Registration of a repo moves to the `trainings` table; the repo's
contents do not move.

**Already in Postgres and untouched** — `users`, `sessions`, `agent_tokens`, `item_progress`,
`lab_task_progress`, `quiz_attempts`, `submissions`, `badges`, `ranks`, `lab_instances`,
`lab_events`, `check_runs`, `hint_reveals`, `setup_runs`, `terminal_transcripts`, `cost_actuals`,
`budget_alerts`, `kill_switch`, `notification_mutes`, `audit_log`, `content_edits`, `aws_ops`,
`reaper_findings`.

---

## 4. Architecture

A new package `internal/org` owns the configuration and org tables and the endpoints that change
them. Nothing else gains a dependency on Postgres that it did not have.

The rest of the code reads teams and programs through the same in-memory picture it reads today.
`gitsync.State` keeps its shape; where it was filled by parsing YAML after a git sync, it is filled
from `internal/org` at boot and refreshed after every successful write. That keeps `rbac`, `labs`
(approvals, finops, service), `learn/forge`, `journey`, `notify`, `edits` and `httpapi` — and the
tests that prove the permission matrix, tier routing, budget caps and scoring rules — unchanged by
this milestone.

```
   admin UI ──► internal/org ──► Postgres
                     │
                     └─► refresh ──► in-memory teams/programs ──► rbac · labs · learn · journey
   training repos ──► gitsync ─────► in-memory content versions ──► learn · labs · edits
```

`config.Team` and `config.Program` therefore keep living in a package named `config` while no longer
coming from configuration. That is a naming wart, deliberately accepted here and corrected in a
separate, logic-free rename commit after this milestone's tests are green (§10).

**One replica still assumed.** The refresh is in-process, like the existing content cache. This is
listed with the other single-replica assumptions in the roadmap; nothing here makes them worse.

---

## 5. Schema

One goose migration, with a Down that drops exactly what it creates.

- `settings` — single row, enforced by a `CHECK (id = 1)`: `default_theme`, `auto_approve_usd`,
  `tier1_usd`, `tier2_usd`, `cluster_usd_per_hour` (nullable: unset means cluster labs are
  unavailable, never free), `escalation_hours`, and the five rank thresholds.
- `schedules` — `name` (PK), `timezone`, `windows` JSONB (days, start, end), validated on write.
- `quotes` — `id`, `text`.
- `admins` — `email` (PK, lowercased).
- `trainings` — `id` (PK), `repo`, `branch`.
- `teams` — `id` (PK, slug), `name`.
- `team_members` — `(team, email, role)` with `role` in `leader|senior|member|trainee`, unique on
  `(team, email)`: one role per person per team, as the spec requires.
- `mentors` — `(team, trainee_email, mentor_email)`.
- `team_webhooks` — `(team, kind, url)`; URLs are treated as secrets and never returned to the
  browser once stored.
- `team_budgets` — `team` (PK), `monthly_usd`, `hard_cap_usd`.
- `programs` — `(team, training)` PK, `pinned_ref` (nullable: track the branch head),
  `schedule_name` (nullable), `inline_schedule` JSONB (nullable), `ttl`, `idle_timeout`,
  `max_extension`, `budget_usd_month`, `review_self_reported`.
- `program_roles` — `(team, training, email, role)` with `role` in `manager|scorer|approver`.
- `enrollments` — `(team, training, email)`.

Emails are stored lowercased and are the join key to `users`, matched on the verified email the
identity provider returns — the same rule as today, now enforced by the schema.

Deletes cascade from `teams` to its members, mentors, webhooks, budget, programs, program roles and
enrollments. Removing a team does **not** delete learning history: progress rows survive, because a
score is the trainee's, not the team's.

---

## 6. API and UI

Endpoints live under `/api/admin` for global settings and keep their current paths for team-scoped
ones, so the SPA's existing pages move with the smallest possible change:

| Page | Today | After |
|---|---|---|
| Team (roster, mentors) | `PUT /api/teams/{team}/roster` → bot commit | same path, writes rows |
| Team budget | `PUT /api/teams/{team}/budget` → bot commit | same path, writes rows |
| Program settings (roles, enrolled, schedule, limits) | `PUT /api/teams/{team}/programs/{training}` → bot commit | same path, writes rows |
| Pin bump | `POST /api/teams/{team}/programs/{training}/pin` → bot commit | same path, writes `programs.pinned_ref` |
| Global settings | editable only by committing `platform.yaml` | **new:** `GET/PUT /api/admin/settings`, `/schedules`, `/quotes`, `/admins` |
| Trainings registry | editable only by committing `trainings.yaml` | **new:** `GET/POST/DELETE /api/admin/trainings` |
| Teams themselves | created by adding a directory in git | **new:** `POST/DELETE /api/admin/teams` |

Every write keeps the three properties the git path had: a permission re-check at the moment of the
write, validation before the change is stored, and an `audit_log` row in the same transaction as the
change. The Origin/Referer and JSON-body requirements for state-changing requests are unchanged.

Validation moves, it does not weaken. A settings save is refused when it would leave `cost_tiers`
unset or inconsistent (`auto_approve_usd ≤ tier1_usd ≤ tier2_usd`), when a schedule window crosses
midnight or names an unknown day, when a rank threshold is out of order or `masterwork ≠ 100`, when
a program names a schedule that does not exist, or when `idle_warning ≥ idle_timeout` in a program's
lab defaults. Because an invalid state can no longer be stored, the "keep serving the last good
config" behaviour for *configuration* is no longer needed; it stays exactly as it is for *content*,
where a bad push still leaves the previous version live.

Forge Status keeps showing content sync problems and gains nothing else; the configuration half of
that page becomes the admin settings pages.

---

## 7. Export and import

Two CLI commands and two admin endpoints over the same code:

```
crucible export --db <url> > instance.json      # plus a blobs directory when uploads exist
crucible import --db <url> instance.json
GET  /api/admin/export      (admin only, audited, streams the file)
POST /api/admin/import      (admin only, audited, refuses a non-empty instance unless --force)
```

**Format.** A single JSON object with `"crucible_export": 1` as its first field, a `generated_at`
timestamp, and one key per section. Unknown version → refuse, with a message naming the version it
can read. The file is read whole and applied in one transaction: it either lands completely or not
at all.

**Sections.** `settings`, `schedules`, `quotes`, `admins`, `trainings`, `teams` (with members,
mentors, budget, webhooks), `programs` (with roles and enrollments), then learning history:
`item_progress`, `lab_task_progress`, `quiz_attempts`, `submissions` (with scores and feedback),
`badges`, `ranks`, `hint_reveals`, `terminal_transcripts` and `notification_mutes`.

**Identity.** Every person is a verified email address. `users` rows are recreated on import with no
OIDC `sub`; the `sub` is attached on that person's first login at the new instance. This is what
makes a move between identity providers work — Keycloak locally to Cognito in AWS, or one Cognito
pool to another.

**Blobs.** Uploads and terminal transcripts live in object storage or on disk, not in Postgres. The
export writes them to a directory beside the JSON and records their relative paths; the import reads
them back through the configured blob store. An export whose blobs are missing imports with those
submissions marked as having an unavailable file rather than failing.

**Excluded.** Sessions and agent tokens (everyone signs in again, every laptop re-pairs), lab
instances and events, check runs, setup runs, cost actuals, budget alerts, reaper findings,
kill-switch state and the audit log. A new instance starts with no running labs, no spend history
and its own audit trail — and the import itself is the first entry in it.

**This file is personal data.** It holds names, emails, answers and scores. The commands print that
on every export, the docs say it, and it is never written anywhere Crucible treats as public. It is
not a backup and not something to commit to a repository.

---

## 8. Fixtures, tests and the local stack

`examples/platform` stops being a git repo to seed and becomes `examples/seed.json` in the export
format. A dev-only `POST /api/admin/seed` — enabled by `CRUCIBLE_DEV_SEED=1`, refused in any other
mode — imports it, and `scripts/local-check.sh` and `scripts/cluster-check.sh` call it before
Playwright starts. The result is the same four local users in the same team with the same programs,
created the way a real operator creates them.

`scripts/seed-git.sh` keeps seeding the five training repos and drops `platform`. The compose file
loses `CRUCIBLE_PLATFORM_REPO` and keeps the `.local/git` mount for content.

**Proof this milestone owes:**

- `TestBootstrapAdminSeedsOnceIntoAnEmptyTable` — and does nothing on a second start.
- `TestSettingsSaveRefusesMissingCostTiers`, `TestSettingsSaveRefusesBadRankOrder`,
  `TestScheduleWindowValidationOnSave` — validation moved, not lost.
- `TestRosterWriteIsAudited`, `TestLeaderCannotWriteAnotherTeam`,
  `TestNobodyGrantsThemselvesAdmin` — the permission re-check survives the storage change.
- `TestExportImportRoundTrip` — a configured instance with progress exports, imports into an empty
  database, and the second instance answers the same API queries.
- `TestImportRelinksUsersByEmail` — a user imported with no `sub` gains one on first login and keeps
  their progress.
- `TestImportRefusesUnknownVersion`, `TestImportIsAllOrNothing` — a truncated file changes nothing.
- `TestImportRefusesNonEmptyInstance` — without the explicit override.
- `TestSeedEndpointIsDevOnly` — refused when `CRUCIBLE_DEV_SEED` is unset.
- Playwright: the existing suites pass against a seeded instance, plus one spec where an admin
  creates a team, adds a trainee and enrolls them with no YAML anywhere.

---

## 9. Risks and what we accept

- **Lockout.** One bootstrap admin means losing that account before a second admin exists locks the
  instance. Recovery: empty the `admins` table and restart with a new `CRUCIBLE_BOOTSTRAP_ADMIN`.
  The UI warns while exactly one admin exists.
- **`audit_log` becomes the only history** of privilege and budget changes; there is no `git log` to
  fall back on. It is already written in the same transaction as each change, which is why this is
  acceptable, but it is now load-bearing and must never become best-effort.
- **No review step.** Changing a cost tier or granting a role no longer passes through a pull
  request. That is the trade accepted in exchange for keeping people out of git.
- **A git host outage** no longer blocks configuration changes, only content sync. This is a gain,
  recorded here because it reverses a documented property.
- **The export is a new way to leak personal data.** Mitigated by admin-only access, an audit entry
  per export, and explicit documentation; not by encryption, which we are not building.

---

## 10. Build order

1. Migration and `internal/org`: tables, reads, writes, validation, audit.
2. Fill the in-memory teams/programs picture from `internal/org`; delete the platform-repo loader.
3. Bootstrap admin into `admins`; delete `configapi.SeedAdmin`'s git path.
4. Admin endpoints and pages: settings, schedules, quotes, admins, trainings, teams.
5. Repoint the existing Team, Budget, Program settings and Pin endpoints.
6. Delete the config write-back path; keep the Writer for content edits.
7. Export/import: format, CLI, endpoints, blobs, round-trip tests.
8. `examples/seed.json`, the dev-only seed endpoint, and both check scripts.
9. Docs: spec sections marked superseded, runbooks, README, CLAUDE.md.
10. Separately, after the suites are green: the logic-free rename of `config.Team`/`config.Program`
    to `org.Team`/`org.Program`.

Steps 1–6 are one reviewable sequence; 7–8 can land after it; 10 is deliberately its own commit so a
reviewer can confirm it changes no behaviour.

---

## 11. Open questions

None blocking. Two worth revisiting after it ships: whether team webhooks belong in the export at
all (they are secrets, and an import carries them to a new environment), and whether a second
instance importing the same file should be detectable, so two live instances cannot quietly diverge.
