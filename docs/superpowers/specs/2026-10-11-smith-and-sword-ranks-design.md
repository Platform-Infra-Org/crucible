# Smith and sword ranks

Replaces the single personal forge rank (spec §7, "Forge ranks"; §13 `ranks`). Owner's decisions, 2026-10-11.

## Two ladders

**Sword rank, per training.** Each training a person takes forges its own sword: Ore → Ingot → Tempered → Blade →
Sword → Masterwork, by the share of *that* training done (item-points weighted, as today), against the admin
thresholds in Settings (defaults 20 / 45 / 75 / 90 / 100 %). A sword rank never drops. Badges are unchanged: one per
completed training (a Masterwork sword and its badge arrive together).

**Smith rank, across Crucible.** An experience meter fills from everything a person does, and climbs a fixed ladder
about their craft:

| Level | Smith rank | Experience |
|---|---|---|
| 0 | Bellows Hand | 0 |
| 1 | Apprentice | 150 |
| 2 | Journeyman | 500 |
| 3 | Smith | 1,200 |
| 4 | Master Smith | 2,500 |
| 5 | Grandmaster | 5,000 |

Admins change the experience each smith rank needs in Settings, as they change the sword thresholds (the table gives
the defaults; each must be higher than the one before, and Bellows Hand is always 0). The titles, the experience per
point and the milestone bonuses below live in code. Lowering a threshold can raise people's smith rank (announced as
any rank-up); raising one never lowers a rank already reached.

## Experience

Experience = **work + milestones**, kept as a high-water mark: it never drops, whatever happens to progress (an admin
reset, a training taken away).

- **Work:** 10 experience per item point done, summed over every training the person takes (the same done/total the
  sword rank uses).
- **Milestones:** a bonus for each sword level reached, per training: Ingot +20, Tempered +40, Blade +60, Sword +80,
  Masterwork +150 (350 for a finished sword). Milestones come from the stored sword ranks, so they are never lost and
  never counted twice.

A Forge-101-sized training (about 19 points) is worth about 190 + 350 = 540 experience when mastered: one mastered
training makes a Journeyman.

## Rank-ups

- **Sword:** the trainee sees the hammer-strike banner in the app, naming the training ("Your Forge 101 sword is now a
  Blade"). No email or team post.
- **Smith:** its own banner, and the notification that `rank_up` sends today, now saying "<name> is now a Journeyman".
  Muteable as today.
- Exactly once: only the caller whose conditional update raised the level shows or sends it; a jump of two levels
  announces the top one. Each banner is acknowledged separately (`POST /api/me/forge/seen` names the smith rank or a
  training's sword).
- The first computation for a person after the upgrade fills sword ranks and experience silently.

## Who sees what

- **Smith rank** is shown with a person's name: the user's own card, and wherever people are the subject — the Team
  roster, the people picker on Manage trainings, Journey (person headers and rows), the Anvil (the trainee), Approvals
  (the requester), content edits (author and reviewer). Not on money pages (the Ledger).
- **Sword ranks** are seen by the trainee, and on Journey by whoever already sees that person's row for the training
  there (who may view their progress: the training's admins and scorers, the owning team's leaders, seniors and
  juniors, the person's own team's leaders and seniors). They already see the module-by-module progress the sword rank
  sums up. Nobody else.
- **No leaderboards:** nothing is ordered, numbered or compared by smith rank or experience.

## Where it shows

- **User card** (the user menu): smith title, the smith mark, and the experience meter ("Journeyman · 720 / 1,200 to
  Smith"); at the top rank, no "to next".
- **Hearth:** *Your craft* (smith rank, meter, next title) and *Your swords* (one line per training: sword rank, % done,
  next rank and threshold), then badges.
- **A training's page:** "Your sword: Tempered · 52 % · next Blade at 75 %".
- **Next to names:** the smith mark (a hammer glyph whose metal changes with level) plus the title in text, so colour
  never carries the meaning alone.
- **Journey rows** (followers only): the sword rank beside the %.

## Storage and API

- `sword_ranks (user_id, training, level, earned_at, seen, PRIMARY KEY (user_id, training))` — raised only.
- `smiths (user_id PRIMARY KEY, xp, level, earned_at, seen)` — `xp` high-water, `level` raised only.
- The old `ranks` table is dropped (the migration's Down recreates it empty).
- `GET /api/me/forge` returns `{smith: {xp, level, title, next: {title, at} | null}, swords: [{training, title, level,
  rank, percent, next: {rank, at} | null}], badges, sword_ladder, smith_ladder, rank_up: {smith: {level, title} | null,
  swords: [{training, level, rank}]}}`.
- Payloads that name a person (team members, Journey rows, Anvil, approvals, edits, the people picker) carry
  `smith: {level, title}`, filled by one batched lookup per response. Journey rows also carry `sword: {level, rank}` when
  the viewer may see it.
- Org export and import carry `sword_ranks` and `smiths`; an older file's `ranks` is accepted and ignored.
- Admin Settings has two threshold forms: "Sword ranks" (the existing % form) and "Smith ranks" (experience for
  Apprentice, Journeyman, Smith, Master Smith, Grandmaster). They are stored in `settings` (new columns
  `smith_apprentice` … `smith_grandmaster`, defaults as above), validated, audited and versioned like the sword
  thresholds, and exported with the other settings.

## Testing

- Go: sword and smith ladder boundaries; smith thresholds from Settings (defaults, validation, audit, a lowered
  threshold raising a rank, a raised one never lowering it); experience arithmetic; experience and ranks never drop (admin reset, a
  training removed); each milestone counted once; silent backfill; a smith rank-up notifies exactly once under
  concurrent saves and a two-level jump announces the top; a sword rank-up never emails; `seen` clears only the named
  banner; sword ranks reach Journey only on rows the viewer may already see; every person payload above carries `smith`;
  nothing sorts by rank.
- Web: render tests for the user card meter, Your craft / Your swords, the training-page line, the smith mark (every
  theme, phone width, calm motion; title always in text).
- End to end: the Forge 101 journey checks the sword rank on the training page and the smith title in the user card;
  the people journey checks a leader sees the trainee's smith mark on Team and Journey.

## Docs

Trainees' "Ranks and badges" rewritten (two ladders, experience, who sees what); the Hearth, notifications and admin
settings pages; the design spec §2, §7, §10, §13; the crucible-ui skill (the smith mark and the experience meter).
