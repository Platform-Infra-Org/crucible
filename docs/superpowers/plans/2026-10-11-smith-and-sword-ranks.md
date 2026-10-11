# Smith and Sword Ranks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single personal forge rank with a sword rank per training and a global smith rank driven by an experience meter, shown with people's names across Crucible.

**Architecture:** A new leaf package `internal/smith` owns the smith ladder, the experience rules and a batched lookup of people's smith ranks. `learn.UpdateForge` raises per-training sword ranks (`sword_ranks`) and the smith's high-water experience and level (`smiths`), announcing each rise exactly once. Payloads that name a person gain a `smith` ref from one lookup per response; the SPA shows it with a `SmithMark`, the user card shows the meter, and the Hearth splits into *Your craft* and *Your swords*.

**Tech Stack:** Go 1.26, pgx/Postgres (goose migrations), React 19 + Vite, vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-11-smith-and-sword-ranks-design.md`

## Global Constraints

- Every shell that builds or tests starts with `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`.
- Sword ladder (unchanged names): Ore, Ingot, Tempered, Blade, Sword, Masterwork; thresholds from Settings (defaults 20/45/75/90/100 %).
- Smith ladder titles, in order: `Bellows Hand`, `Apprentice`, `Journeyman`, `Smith`, `Master Smith`, `Grandmaster`. Experience thresholds come from Settings; defaults 0 / 150 / 500 / 1200 / 2500 / 5000; each strictly higher than the one before; Bellows Hand is always 0.
- Experience = 10 per item point done (summed over every training the person takes, item-points weighted, as the sword %) + sword milestones per training: Ingot +20, Tempered +40, Blade +60, Sword +80, Masterwork +150 (cumulative: a Masterwork sword is worth 350). Experience is a high-water mark; ranks are raised only, never lowered.
- Sword rank-ups are in-app only (no email, no team post). Smith rank-ups use the existing `rank_up` notification kind ("<name> is now a Journeyman"). Each rise is announced exactly once; a multi-level jump announces the top; the first computation for a person is silent.
- Smith rank is shown with names on: Team roster, Manage trainings people picker, Journey, Anvil (trainee), Approvals (requester), content edits (author, reviewer), the user card. Not on the Ledger.
- Sword ranks: shown to the trainee, and on Journey rows the viewer may already see. Nothing anywhere is sorted, numbered or compared by rank or experience (no leaderboards).
- Next migration number: `00027`; it has a working `-- +goose Down`.
- Tests first; record RED and GREEN. Commits: `git commit -m "…" -- <paths>` (git add new files first), never `git add -A`; message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Any user-visible change updates `docs/user` in the same commit (`go test ./internal/docs` must pass). UI work follows `.claude/skills/crucible-ui/SKILL.md` and updates it when a pattern is added. Colours from tokens only.

## Review Focus

1. **Concurrent progress saves** (two tabs, a scorer scoring while the trainee works): each smith and sword rise must be announced once. Pinned in Task 3 (`TestSmithRankUpNotifiesOnceUnderConcurrency`).
2. **An admin reset or a training taken away** drops progress: experience and ranks must not drop, and milestones already earned stay. Pinned in Task 3 (`TestExperienceNeverDrops`).
3. **An admin lowers a smith threshold**: people above it rise (announced); raising one never lowers anyone. Pinned in Task 3 (`TestSmithThresholdChanges`).
4. **A person who never signed in, or never had a forge computed**, appears in a list: the payload says Bellows Hand, never null or missing, and the page doesn't break. Pinned in Task 2 (`TestLookupDefaultsToBellowsHand`) and Task 6 (`SmithMark` renders level 0).
5. **An older org export** (with a `ranks` table) is imported into a new instance: it must import, ignoring `ranks`. Pinned in Task 1 (`TestImportIgnoresTheOldRanksTable`).

---

### Task 1: Settings and storage for the two ladders

**Files:**
- Create: `internal/db/migrations/00027_smith_and_sword_ranks.sql`
- Modify: `internal/config/config.go` (+ `config_test.go`) — `SmithThresholds`
- Modify: `internal/org/org.go`, `internal/org/settings.go`, `internal/org/api.go`, `internal/org/seed.go`, `internal/org/export.go` and their tests
- Modify: `examples/platform/platform.yaml` only if it sets `ranks:` (leave it; `smith:` is optional)

**Interfaces:**
- Produces: `config.SmithThresholds{Apprentice, Journeyman, Smith, MasterSmith, Grandmaster int}` (json/yaml keys `apprentice`, `journeyman`, `smith`, `master_smith`, `grandmaster`), `config.DefaultSmith`, `(SmithThresholds).Steps() []int` (6 values, first 0), `(*SmithThresholds).Fill() error`; `config.Settings.Smith SmithThresholds` (yaml `smith`); `org.SettingsBody.Smith` (json `smith`); `GET /api/admin/settings` returns `smith`; tables `sword_ranks`, `smiths`; table `ranks` dropped.

- [ ] **Step 1: Write the failing tests**

In `internal/config/config_test.go`:

```go
func TestSmithThresholdsDefaultAndValidate(t *testing.T) {
	var s SmithThresholds
	if err := s.Fill(); err != nil || !slices.Equal(s.Steps(), []int{0, 150, 500, 1200, 2500, 5000}) {
		t.Fatalf("defaults: %v %v", s.Steps(), err)
	}
	bad := SmithThresholds{Apprentice: 500, Journeyman: 400}
	if err := bad.Fill(); err == nil || !strings.Contains(err.Error(), "more than the one before") {
		t.Fatalf("must rise: %v", err)
	}
	if err := (&SmithThresholds{Apprentice: -1}).Fill(); err == nil {
		t.Fatal("negative refused")
	}
}
```

In `internal/org/settings_test.go`, extend the existing store-and-audit test (or add):

```go
func TestSmithThresholdsAreStoredAndAudited(t *testing.T) {
	f := setup(t) // the file's fixture; use its real name
	b := f.settingsBody(t) // read current settings into a SettingsBody the way the other tests do
	b.Smith = config.SmithThresholds{Apprentice: 100, Journeyman: 400, Smith: 1000, MasterSmith: 2000, Grandmaster: 4000}
	if err := f.store.SetSettings(ctx, "admin@crucible.local", b); err != nil {
		t.Fatal(err)
	}
	st := f.platform(t).Settings // reload through Store.settings
	if st.Smith.Journeyman != 400 {
		t.Fatalf("stored: %+v", st.Smith)
	}
	// the audit row's detail carries "smith"
	b.Smith = config.SmithThresholds{Apprentice: 600, Journeyman: 500}
	if err := f.store.SetSettings(ctx, "admin@crucible.local", b); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("falling thresholds refused: %v", err)
	}
}
```

Use the file's existing helpers for the fixture, reading settings and checking the audit row (follow `TestSetSettingsStoresAndAudits`). In `internal/org/export_test.go` add `TestImportIgnoresTheOldRanksTable`: take the file's exported document fixture, add a `"ranks"` table with one row the way older files carried it, import into a fresh instance, and expect no error; and assert a fresh export lists `sword_ranks` and `smiths` and not `ranks`.

In `internal/db` (next to `m23_test.go`'s pattern) add `TestMigration00027`: migrate to 26, insert a user and a `ranks` row, migrate to 27, assert `ranks` is gone and `sword_ranks`, `smiths` exist and `settings.smith_journeyman = 500`; migrate down to 26 and assert `ranks` exists again (empty) and the new tables are gone.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config ./internal/org ./internal/db -run 'Smith|OldRanks|00027'`
Expected: FAIL to compile (`undefined: SmithThresholds`).

- [ ] **Step 3: The migration**

```sql
-- +goose Up
-- Smith and sword ranks (spec 2026-10-11): a sword rank per training and the smith's high-water experience and level.
-- Both are raised only. The single personal rank they replace goes.
ALTER TABLE settings
  ADD COLUMN smith_apprentice  INT NOT NULL DEFAULT 150,
  ADD COLUMN smith_journeyman  INT NOT NULL DEFAULT 500,
  ADD COLUMN smith_smith       INT NOT NULL DEFAULT 1200,
  ADD COLUMN smith_master      INT NOT NULL DEFAULT 2500,
  ADD COLUMN smith_grandmaster INT NOT NULL DEFAULT 5000;
CREATE TABLE sword_ranks (
  user_id   BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  training  TEXT NOT NULL,
  level     INT NOT NULL,                      -- 0 Ore … 5 Masterwork
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL,                  -- false until the trainee has been shown the rise
  PRIMARY KEY (user_id, training)
);
CREATE TABLE smiths (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  xp        INT NOT NULL,                      -- high-water experience
  level     INT NOT NULL,                      -- 0 Bellows Hand … 5 Grandmaster
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL
);
DROP TABLE ranks;

-- +goose Down
CREATE TABLE ranks (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  level     INT NOT NULL,
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL
);
DROP TABLE smiths, sword_ranks;
ALTER TABLE settings DROP COLUMN smith_apprentice, DROP COLUMN smith_journeyman, DROP COLUMN smith_smith,
  DROP COLUMN smith_master, DROP COLUMN smith_grandmaster;
```

- [ ] **Step 4: Config**

In `internal/config/config.go`, next to `RankThresholds`:

```go
// SmithThresholds are the experience at which each smith rank is earned (spec 2026-10-11). Bellows Hand is 0.
type SmithThresholds struct {
	Apprentice  int `yaml:"apprentice" json:"apprentice"`
	Journeyman  int `yaml:"journeyman" json:"journeyman"`
	Smith       int `yaml:"smith" json:"smith"`
	MasterSmith int `yaml:"master_smith" json:"master_smith"`
	Grandmaster int `yaml:"grandmaster" json:"grandmaster"`
}

var DefaultSmith = SmithThresholds{Apprentice: 150, Journeyman: 500, Smith: 1200, MasterSmith: 2500, Grandmaster: 5000}

// Steps lists the thresholds from Bellows Hand to Grandmaster.
func (s SmithThresholds) Steps() []int {
	return []int{0, s.Apprentice, s.Journeyman, s.Smith, s.MasterSmith, s.Grandmaster}
}

// Fill applies the defaults to unset keys and checks that every rank needs more than the one before.
func (s *SmithThresholds) Fill() error {
	d := DefaultSmith
	for _, f := range []struct{ v *int; def int }{{&s.Apprentice, d.Apprentice}, {&s.Journeyman, d.Journeyman},
		{&s.Smith, d.Smith}, {&s.MasterSmith, d.MasterSmith}, {&s.Grandmaster, d.Grandmaster}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	st := s.Steps()
	for i := 1; i < len(st); i++ {
		if st[i] <= st[i-1] {
			return fmt.Errorf("each smith rank must need more than the one before (0 < apprentice < journeyman < smith < master_smith < grandmaster), got %v", st[1:])
		}
	}
	return nil
}
```

Add `Smith SmithThresholds \`yaml:"smith"\`` to `Settings` (comment: "smith rank experience thresholds; missing keys take DefaultSmith"), and in `Load` call `p.Settings.Smith.Fill()` next to `p.Settings.Ranks.fill()`, appending `platform.yaml: smith: …` on error.

- [ ] **Step 5: Store, API, seed, export**

1. `internal/org/org.go` `settings()`: add `smith_apprentice, smith_journeyman, smith_smith, smith_master, smith_grandmaster` to the `SELECT` and scan into `&st.Smith.Apprentice, …`; then `st.Smith.Fill()` (wrap its error as `settings: …`).
2. `internal/org/settings.go`: add `Smith config.SmithThresholds \`json:"smith"\`` to `SettingsBody`; in `SetSettings` after the ranks check: `sm := b.Smith; if err := sm.Fill(); err != nil { return apperr.Wrap(apperr.Invalid, "smith ranks: "+err.Error()) }`; add `"smith": sm` to the audit detail; extend the `UPDATE` with `smith_apprentice = $11, smith_journeyman = $12, smith_smith = $13, smith_master = $14, smith_grandmaster = $15` and move the version to `$16`.
3. `internal/org/api.go` `getSettings` and `internal/org/seed.go`: pass `Smith: st.Smith` alongside `Ranks: st.Ranks`.
4. `internal/org/export.go`: in the tables list replace `{name: "ranks", byUser: true}` with `{name: "sword_ranks", byUser: true}, {name: "smiths", byUser: true}`; in import, skip a table named `ranks` (older files) instead of failing — find where import iterates the document's tables and `continue` for `ranks`, with the comment `// before 00027: one personal rank, recomputed from progress now`.

- [ ] **Step 6: Run the tests**

Run: `go vet ./... && go test ./internal/config ./internal/org ./internal/db ./internal/learn`
Expected: config/org/db PASS. `internal/learn` FAILS (it still writes `ranks`) — that is Task 3's job; do not touch learn here. Note the failure in the report.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/00027_smith_and_sword_ranks.sql
git commit -m "feat(ranks): smith thresholds in Settings; sword_ranks and smiths tables replace ranks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db internal/config internal/org
```

---

### Task 2: The `smith` package

**Files:**
- Create: `internal/smith/smith.go`, `internal/smith/smith_test.go`

**Interfaces:**
- Consumes: `config.SmithThresholds` (Task 1).
- Produces: `smith.Titles []string`; `smith.PerPoint = 10`; `smith.Milestones []int{0, 20, 40, 60, 80, 150}`; `smith.MilestoneXP(swordLevel int) int`; `smith.Step{Title string; At int}` (json `title`, `at`); `smith.Ladder(config.SmithThresholds) []Step`; `smith.LevelFor(xp int, ladder []Step) int`; `smith.Ref{Level int; Title string}` (json `level`, `title`); `smith.RefFor(level int) Ref`; `smith.Refs map[string]Ref` with `(Refs) Of(email string) Ref`; `smith.Lookup(ctx, q Querier, emails []string) (Refs, error)`; `smith.Querier` (`Query(ctx, sql string, args ...any) (pgx.Rows, error)`); `smith.Article(title string) string` ("a"/"an").

- [ ] **Step 1: Write the failing tests**

```go
package smith

import (
	"context"
	"testing"

	"crucible/internal/config"
	"crucible/internal/dbtest"
)

func TestLadderAndLevels(t *testing.T) {
	l := Ladder(config.DefaultSmith)
	if len(l) != 6 || l[0] != (Step{"Bellows Hand", 0}) || l[5] != (Step{"Grandmaster", 5000}) {
		t.Fatalf("%+v", l)
	}
	for xp, want := range map[int]int{0: 0, 149: 0, 150: 1, 499: 1, 500: 2, 1200: 3, 2499: 3, 2500: 4, 5000: 5, 99999: 5} {
		if got := LevelFor(xp, l); got != want {
			t.Errorf("%d xp: level %d, want %d", xp, got, want)
		}
	}
}

func TestMilestonesAreCumulative(t *testing.T) {
	for lvl, want := range []int{0, 20, 60, 120, 200, 350} {
		if got := MilestoneXP(lvl); got != want {
			t.Errorf("sword level %d: %d, want %d", lvl, got, want)
		}
	}
	if Article("Apprentice") != "an" || Article("Journeyman") != "a" {
		t.Fatal("article")
	}
}

func TestLookupDefaultsToBellowsHand(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO users (subject, email, name) VALUES ('s', 'Smith@Crucible.Local', 'S') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO smiths (user_id, xp, level, seen) VALUES ($1, 600, 2, true)`, id); err != nil {
		t.Fatal(err)
	}
	refs, err := Lookup(ctx, db, []string{"smith@crucible.local", "never@crucible.local"})
	if err != nil {
		t.Fatal(err)
	}
	if refs.Of("SMITH@crucible.local") != (Ref{2, "Journeyman"}) || refs.Of("never@crucible.local") != (Ref{0, "Bellows Hand"}) || refs.Of("") != (Ref{0, "Bellows Hand"}) {
		t.Fatalf("%+v", refs)
	}
}
```

(Check the `users` insert columns against an existing test's insert — e.g. `auth.Store.UpsertUser` — and use that helper instead if it is simpler.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/smith`
Expected: FAIL, no Go files.

- [ ] **Step 3: Implement**

```go
// Package smith is the smith rank (spec 2026-10-11): an experience meter across every training and a ladder about the
// smith's craft. It is a leaf: anything that names a person can look their smith rank up without importing learn.
package smith

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"crucible/internal/config"
)

var Titles = []string{"Bellows Hand", "Apprentice", "Journeyman", "Smith", "Master Smith", "Grandmaster"}

// PerPoint is the experience for each item point done; Milestones[i] the bonus for a sword reaching level i.
const PerPoint = 10

var Milestones = []int{0, 20, 40, 60, 80, 150}

// MilestoneXP is the bonus a sword at this level has earned in all: every milestone up to it.
func MilestoneXP(swordLevel int) int {
	n := 0
	for i := 1; i <= swordLevel && i < len(Milestones); i++ {
		n += Milestones[i]
	}
	return n
}

type Step struct {
	Title string `json:"title"`
	At    int    `json:"at"`
}

func Ladder(t config.SmithThresholds) []Step {
	out := make([]Step, len(Titles))
	for i, at := range t.Steps() {
		out[i] = Step{Title: Titles[i], At: at}
	}
	return out
}

func LevelFor(xp int, ladder []Step) int {
	lvl := 0
	for i, s := range ladder {
		if xp >= s.At {
			lvl = i
		}
	}
	return lvl
}

type Ref struct {
	Level int    `json:"level"`
	Title string `json:"title"`
}

func RefFor(level int) Ref {
	level = max(0, min(level, len(Titles)-1))
	return Ref{Level: level, Title: Titles[level]}
}

// Refs maps a lower-cased email to its smith rank; anyone missing is a Bellows Hand.
type Refs map[string]Ref

func (r Refs) Of(email string) Ref {
	if ref, ok := r[strings.ToLower(email)]; ok {
		return ref
	}
	return RefFor(0)
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Lookup finds the smith ranks of these people in one query.
func Lookup(ctx context.Context, q Querier, emails []string) (Refs, error) {
	lower := make([]string, 0, len(emails))
	for _, e := range emails {
		if e != "" {
			lower = append(lower, strings.ToLower(e))
		}
	}
	out := Refs{}
	if len(lower) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT lower(u.email), max(s.level) FROM smiths s JOIN users u ON u.id = s.user_id
		WHERE lower(u.email) = ANY($1) GROUP BY lower(u.email)`, lower)
	if err != nil {
		return nil, err
	}
	var e string
	var lvl int
	_, err = pgx.ForEachRow(rows, []any{&e, &lvl}, func() error { out[e] = RefFor(lvl); return nil })
	return out, err
}

// Article is "an" before a vowel sound, else "a" ("an Apprentice", "a Journeyman").
func Article(title string) string {
	if title != "" && strings.ContainsRune("AEIOU", rune(title[0])) {
		return "an"
	}
	return "a"
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/smith && go vet ./internal/smith`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/smith
git commit -m "feat(smith): the smith ladder, experience rules and a batched rank lookup

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/smith
```

---

### Task 3: Sword ranks per training, experience and the smith rank

**Files:**
- Modify: `internal/learn/forge.go` (rewrite `UpdateForge`, `ForgeView`, `rankUp`, `SeenRankUp`)
- Modify: `internal/learn/http.go` (`/api/me/forge/seen` body)
- Modify: `internal/learn/forge_test.go` (rewrite), `internal/learn/human_test.go` (`TestAdminResetRecomputesProgressAndKeepsRank` uses the new view)
- Modify: `internal/notify/notify.go` (the `RankUp` description)

**Interfaces:**
- Consumes: `smith.*` (Task 2), `config.Settings.Smith` (Task 1), existing `Standing`, `RankFor`, `Ladder`, `forgePercent`.
- Produces: `learn.ForgeView{Smith SmithView; Swords []Sword; Badges []Badge; SwordLadder []RankStep; SmithLadder []smith.Step; RankUp RankUps}` with JSON `smith`, `swords`, `badges`, `sword_ladder`, `smith_ladder`, `rank_up`; `learn.SmithView{XP int; Level int; Title string; Next *smith.Step}` (json `xp`, `level`, `title`, `next`); `learn.Sword{Training, Title string; Level int; Rank string; Percent float64; Next *RankStep}` (json `training`, `title`, `level`, `rank`, `percent`, `next`); `learn.RankUps{Smith *smith.Ref; Swords []SwordUp}` (json `smith`, `swords`); `learn.SwordUp{Training, Title string; Level int; Rank string}`; `(*Service).SeenRankUp(ctx, userID int64, kind, training string, level int) error`; `(*Service).SwordRanks(ctx, userIDs []int64) (map[int64]map[string]int, error)` (user → training → sword level, for Journey).
- `POST /api/me/forge/seen` body `{"kind": "smith"|"sword", "training": "…", "level": n}`.

- [ ] **Step 1: Rewrite the tests**

Replace the rank tests in `internal/learn/forge_test.go` (keep `fakeNotify`, `completeAll`, `enrolExtra`, `TestRankBoundaries`, `TestBadgeForACompletedTraining` adapted) with:

```go
func sword(v *ForgeView, training string) *Sword {
	for i := range v.Swords {
		if v.Swords[i].Training == training {
			return &v.Swords[i]
		}
	}
	return nil
}

func TestSwordRankPerTraining(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	enrolExtra(s, 5)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, x := sword(v, "forge-101"), sword(v, "extra")
	if f == nil || x == nil || f.Level < 1 || x.Level != 0 || x.Rank != "Ore" || f.Next == nil {
		t.Fatalf("each training forges its own sword: %+v", v.Swords)
	}
	if len(v.RankUp.Swords) != 1 || v.RankUp.Swords[0].Training != "forge-101" {
		t.Fatalf("the forge-101 rise waits to be shown: %+v", v.RankUp)
	}
}

func TestExperienceIsWorkPlusMilestones(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab", "03-cluster-heat")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := s.Standing(ctx, u.ID, "forge", "forge-101")
	if err != nil {
		t.Fatal(err)
	}
	want := int(math.Round(sd.Done*10)) + 350 // work + a Masterwork sword's milestones (190 + 350 for Forge 101's 19 points)
	if f := sword(v, "forge-101"); f == nil || f.Rank != "Masterwork" || v.Smith.XP != want || v.Smith.Level != smith.LevelFor(want, v.SmithLadder) {
		t.Fatalf("want %d: %+v %+v", want, v.Smith, v.Swords)
	}
}

func TestExperienceNeverDrops(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	xp, lvl, sw := v.Smith.XP, v.Smith.Level, sword(v, "forge-101").Level
	if _, err := s.DB.Exec(ctx, `DELETE FROM item_progress WHERE user_id = $1`, u.ID); err != nil { // as an admin reset would
		t.Fatal(err)
	}
	st := s.State()
	st.Platform.Teams["forge"].Programs["forge-101"].Enrolled = nil // and the training taken away
	v, err = s.UpdateForge(ctx, u.ID)
	if err != nil || v.Smith.XP != xp || v.Smith.Level != lvl {
		t.Fatalf("experience and smith rank keep their high-water: %+v %v", v.Smith, err)
	}
	var stored int
	if err := s.DB.QueryRow(ctx, `SELECT level FROM sword_ranks WHERE user_id = $1 AND training = 'forge-101'`, u.ID).Scan(&stored); err != nil || stored != sw {
		t.Fatalf("the sword rank stays: %d %v", stored, err)
	}
}

func TestSmithRankUpNotifiesOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil { // the silent first computation
		t.Fatal(err)
	}
	tr := s.State().Trainings["forge-101@abc"]
	for _, m := range tr.Modules { // straight to the table: only the UpdateForge calls below may raise
		for _, it := range m.Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, m.ID, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.UpdateForge(ctx, u.ID) }()
	}
	wg.Wait()
	ups := notes.kind(notify.RankUp)
	if len(ups) != 1 || !strings.Contains(ups[0].Subject, "is now a Journeyman") || !slices.Equal(ups[0].To, []string{"trainee@crucible.local"}) {
		t.Fatalf("one smith notification naming the top rank: %+v", ups)
	}
	v, _ := s.UpdateForge(ctx, u.ID)
	if v.RankUp.Smith == nil || v.RankUp.Smith.Title != "Journeyman" {
		t.Fatalf("the smith rise waits to be shown: %+v", v.RankUp)
	}
	if err := s.SeenRankUp(ctx, u.ID, "smith", "", v.Smith.Level); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UpdateForge(ctx, u.ID); v.RankUp.Smith != nil || len(v.RankUp.Swords) != 1 {
		t.Fatalf("seen clears the smith banner only: %+v", v.RankUp)
	}
}

func TestSwordRankUpNeverEmails(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	s.State().Platform.Settings.Smith = config.SmithThresholds{Apprentice: 100000, Journeyman: 200000, Smith: 300000, MasterSmith: 400000, Grandmaster: 500000}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab") // a sword rise (at least Ingot); the smith can't rise here
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || len(v.RankUp.Swords) == 0 || v.Smith.Level != 0 || len(notes.evs) != 0 {
		t.Fatalf("in-app only: %+v %v %+v", v.RankUp, err, notes.evs)
	}
}

func TestFirstComputationIsSilent(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab", "03-cluster-heat") // progress before any forge row
	if _, err := s.DB.Exec(ctx, `DELETE FROM sword_ranks; DELETE FROM smiths`); err != nil { // as just after the upgrade
		t.Fatal(err)
	}
	notes.evs = nil
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || v.Smith.Level == 0 || v.RankUp.Smith != nil || len(v.RankUp.Swords) != 0 || len(notes.evs) != 0 {
		t.Fatalf("backfilled silently: %+v %v", v, err)
	}
}

func TestSmithThresholdChanges(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, _ := s.UpdateForge(ctx, u.ID)
	lvl := v.Smith.Level
	st := s.State()
	st.Platform.Settings.Smith = config.SmithThresholds{Apprentice: 1, Journeyman: 2, Smith: 3, MasterSmith: 4, Grandmaster: v.Smith.XP}
	v, _ = s.UpdateForge(ctx, u.ID)
	if v.Smith.Level != 5 || len(notes.kind(notify.RankUp)) == 0 {
		t.Fatalf("a lowered threshold raises and announces: %+v", v.Smith)
	}
	st.Platform.Settings.Smith = config.SmithThresholds{Apprentice: 100000, Journeyman: 200000, Smith: 300000, MasterSmith: 400000, Grandmaster: 500000}
	if v, _ = s.UpdateForge(ctx, u.ID); v.Smith.Level != 5 || v.Smith.Next != nil || lvl >= 5 {
		t.Fatalf("a raised threshold never lowers: %+v", v.Smith)
	}
}
```

Rewrite `TestForgeHTTP` for the new body: `GET` returns the new view for the caller only (the leader's view has no swords); `POST /api/me/forge/seen` with `{"kind":"sword","training":"forge-101","level":N}` by another user leaves the trainee's banner; by the trainee at a stale level leaves it; at the current level clears it (twice: idempotent); `{"kind":"nope"}` and `nope` are 400. Keep `TestRankBoundaries` (sword ladder) and `TestBadgeForACompletedTraining` (assert via `v.Badges`). Adjust `TestForgeWithNoEnrolments`: `v.Swords` empty, `v.Smith == {0, 0, "Bellows Hand", next Apprentice at 150}`. In `human_test.go`, `TestAdminResetRecomputesProgressAndKeepsRank` asserts the forge-101 sword level and the smith level stay after the reset.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/learn -run 'Sword|Experience|Smith|FirstComputation|ForgeHTTP|NoEnrolments|Badge|Boundaries|AdminReset'`
Expected: FAIL to compile (`v.Swords undefined`).

- [ ] **Step 3: Implement**

Replace `ForgeView`, `UpdateForge`, `rankUp` and `SeenRankUp` in `internal/learn/forge.go`:

```go
type Sword struct {
	Training string    `json:"training"`
	Title    string    `json:"title"`
	Level    int       `json:"level"` // highest ever reached
	Rank     string    `json:"rank"`
	Percent  float64   `json:"percent"` // of this training, weighted by item points, one decimal
	Next     *RankStep `json:"next"`
}

type SmithView struct {
	XP    int         `json:"xp"`
	Level int         `json:"level"`
	Title string      `json:"title"`
	Next  *smith.Step `json:"next"`
}

type SwordUp struct {
	Training string `json:"training"`
	Title    string `json:"title"`
	Level    int    `json:"level"`
	Rank     string `json:"rank"`
}

type RankUps struct {
	Smith  *smith.Ref `json:"smith"`
	Swords []SwordUp  `json:"swords"`
}

type ForgeView struct {
	Smith       SmithView    `json:"smith"`
	Swords      []Sword      `json:"swords"`
	Badges      []Badge      `json:"badges"`
	SwordLadder []RankStep   `json:"sword_ladder"`
	SmithLadder []smith.Step `json:"smith_ladder"`
	RankUp      RankUps      `json:"rank_up"`
}

// raise stores level for a key the first time silently (seen) and afterwards raises it only, unseen. It reports
// whether this caller raised it: only that caller announces the rise, so it is announced once.
func raise(ctx context.Context, db *pgxpool.Pool, insert, update string, args ...any) (bool, error) {
	tag, err := db.Exec(ctx, insert, args...)
	if err != nil || tag.RowsAffected() > 0 {
		return false, err
	}
	var lvl int
	err = db.QueryRow(ctx, update, args...).Scan(&lvl)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UpdateForge recomputes the user's swords, badges and experience and raises what they reached. Nothing is lowered.
func (s *Service) UpdateForge(ctx context.Context, userID int64) (*ForgeView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var email, name string
	if err := s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name); err != nil {
		return nil, err
	}
	v := &ForgeView{SwordLadder: Ladder(st.Platform.Settings.Ranks), SmithLadder: smith.Ladder(st.Platform.Settings.Smith),
		Swords: []Sword{}, Badges: []Badge{}, RankUp: RankUps{Swords: []SwordUp{}}}
	titles := map[string]string{}
	work := 0.0
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(email) {
		sd, err := s.Standing(ctx, userID, e.Team.ID, e.Program.Training)
		if err != nil {
			return nil, err
		}
		if sd == nil || sd.Total == 0 {
			continue // ponytail: unavailable content neither helps nor hurts until it syncs again
		}
		id := sd.Training.ID
		titles[id] = sd.Training.Title
		work += sd.Done * smith.PerPoint
		pct := forgePercent(sd.Done, sd.Total)
		if _, err := raise(ctx, s.DB,
			`INSERT INTO sword_ranks (user_id, training, level, seen) VALUES ($1, $2, $3, true) ON CONFLICT DO NOTHING`,
			`UPDATE sword_ranks SET level = $3, earned_at = now(), seen = false WHERE user_id = $1 AND training = $2 AND level < $3 RETURNING level`,
			userID, id, RankFor(pct, v.SwordLadder)); err != nil {
			return nil, err
		}
		v.Swords = append(v.Swords, Sword{Training: id, Title: sd.Training.Title, Percent: pct})
		if sd.Done >= sd.Total-1e-9 {
			if _, err := s.DB.Exec(ctx, `INSERT INTO badges (user_id, training, team) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				userID, id, e.Team.ID); err != nil {
				return nil, err
			}
		}
	}
	// Stored swords, the ones of trainings no longer taken included: their milestones are kept.
	rows, err := s.DB.Query(ctx, `SELECT training, level, NOT seen FROM sword_ranks WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	stored, milestones := map[string]int{}, 0
	var tr string
	var lvl int
	var unseen bool
	if _, err := pgx.ForEachRow(rows, []any{&tr, &lvl, &unseen}, func() error {
		stored[tr], milestones = lvl, milestones+smith.MilestoneXP(lvl)
		if unseen && titles[tr] != "" {
			v.RankUp.Swords = append(v.RankUp.Swords, SwordUp{Training: tr, Title: titles[tr], Level: lvl, Rank: v.SwordLadder[lvl].Name})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for i := range v.Swords {
		sw := &v.Swords[i]
		sw.Level = stored[sw.Training]
		sw.Rank = v.SwordLadder[sw.Level].Name
		if sw.Level+1 < len(v.SwordLadder) {
			sw.Next = &v.SwordLadder[sw.Level+1]
		}
	}
	xp := int(math.Round(work)) + milestones
	if _, err := s.DB.Exec(ctx, `INSERT INTO smiths (user_id, xp, level, seen) VALUES ($1, $2, $3, true)
		ON CONFLICT (user_id) DO UPDATE SET xp = GREATEST(smiths.xp, EXCLUDED.xp)`, userID, xp, smith.LevelFor(xp, v.SmithLadder)); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT xp FROM smiths WHERE user_id = $1`, userID).Scan(&v.Smith.XP); err != nil {
		return nil, err
	}
	// The row exists now (inserted seen, silently, on the first computation): from here the level only rises, and the
	// one caller whose update returns a row announces it.
	var raisedTo int
	err = s.DB.QueryRow(ctx, `UPDATE smiths SET level = $2, earned_at = now(), seen = false WHERE user_id = $1 AND level < $2 RETURNING level`,
		userID, smith.LevelFor(v.Smith.XP, v.SmithLadder)).Scan(&raisedTo)
	raised := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT level, NOT seen FROM smiths WHERE user_id = $1`, userID).Scan(&v.Smith.Level, &unseen); err != nil {
		return nil, err
	}
	ref := smith.RefFor(v.Smith.Level)
	v.Smith.Title = ref.Title
	if v.Smith.Level+1 < len(v.SmithLadder) {
		v.Smith.Next = &v.SmithLadder[v.Smith.Level+1]
	}
	if unseen {
		v.RankUp.Smith = &ref
	}
	if raised {
		s.rankUp(ctx, email, name, ref.Title)
	}
	// badges: unchanged from before (query, titles fallback)
	…
	return v, nil
}
```

Keep the existing badges code (query `badges`, title fallback) at the `…`. `s.DB` is the service's `*pgxpool.Pool` (check the field's type and use it in `raise`'s signature). The first ever smith row is inserted `seen = true` by the `INSERT … ON CONFLICT` (silent backfill); an existing row only has its `xp` raised to the higher value there.

`rankUp`:

```go
func (s *Service) rankUp(ctx context.Context, email, name, title string) {
	if s.Notify == nil {
		return
	}
	who := name
	if who == "" {
		who = email
	}
	a := smith.Article(title)
	ev := notify.Event{Kind: notify.RankUp, To: []string{email}, Subject: who + " is now " + a + " " + title,
		Text: who + " is now " + a + " " + title + " of the Crucible. The forge remembers.", Link: "/"}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		slog.Error("queueing the rank-up notification failed", "err", err)
	}
}

// SeenRankUp clears one banner, only at the level the client was shown; a rank raised since stays unseen.
func (s *Service) SeenRankUp(ctx context.Context, userID int64, kind, training string, level int) error {
	switch kind {
	case "smith":
		_, err := s.DB.Exec(ctx, `UPDATE smiths SET seen = true WHERE user_id = $1 AND level = $2`, userID, level)
		return err
	case "sword":
		_, err := s.DB.Exec(ctx, `UPDATE sword_ranks SET seen = true WHERE user_id = $1 AND training = $2 AND level = $3`, userID, training, level)
		return err
	}
	return apperr.Wrap(apperr.Invalid, `kind must be "smith" or "sword"`)
}

// SwordRanks are these people's sword levels by training, for Journey.
func (s *Service) SwordRanks(ctx context.Context, userIDs []int64) (map[int64]map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT user_id, training, level FROM sword_ranks WHERE user_id = ANY($1)`, userIDs)
	if err != nil {
		return nil, err
	}
	out := map[int64]map[string]int{}
	var id int64
	var tr string
	var lvl int
	_, err = pgx.ForEachRow(rows, []any{&id, &tr, &lvl}, func() error {
		if out[id] == nil {
			out[id] = map[string]int{}
		}
		out[id][tr] = lvl
		return nil
	})
	return out, err
}
```

Update the file's top comment: `// Sword ranks, the smith rank and badges (spec 2026-10-11). Nothing here orders one person against another.` Update `RankStep`'s comment to "% of this training needed".

In `internal/learn/http.go` replace the seen handler's body type with `{Kind string \`json:"kind"\`; Training string \`json:"training"\`; Level int \`json:"level"\`}` and call `s.SeenRankUp(ctx, id, in.Kind, in.Training, in.Level)` (a decode error is still 400; an unknown kind comes back 400 from `SeenRankUp`).

In `internal/notify/notify.go` change the `RankUp` description to `"I reached a new smith rank"`.

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/learn ./internal/notify ./internal/scoring ./internal/journey`
Expected: PASS (fix any other package that used `ForgeView.Level/Rank/RankUp`; `grep -rn "\.RankUp\b\|ForgeView" internal`).

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(learn): a sword rank per training, experience and the smith rank, each rise announced once

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/learn internal/notify
```

---

### Task 4: Smith ranks with people's names in the API

**Files:**
- Modify: `internal/org/view.go` (`Member`, `Person` + where they are built), `internal/journey/journey.go` (`Row`), `internal/scoring/anvil.go` (`SignOff`), `internal/scoring/scoring.go` (`Submission`), `internal/labs/approvals.go` (`Approval`), `internal/edits/edits.go` (`ContentEdit`) and the handlers that list them
- Test: one test per package (below)

**Interfaces:**
- Consumes: `smith.Lookup`, `smith.Refs.Of`, `smith.Ref` (Task 2); `learn.SwordRanks` (Task 3); `learn.Ladder`.
- Produces JSON: `members[].smith`, people picker `[].smith`, Journey rows `smith` and `sword: {level, rank}` (omitted when there is no stored sword), Anvil sign-offs `trainee_smith`, submissions `trainee_smith`, approvals `requester_smith`, content edits `author_smith` and `reviewer_smith` (`omitempty` when there is no reviewer). Every one a `smith.Ref`; never null for a named person.

- [ ] **Step 1: Write the failing tests**

For each package add a test that builds its list the way its existing list test does, with one named person who has a `smiths` row (`INSERT INTO smiths (user_id, xp, level, seen) VALUES (…, 600, 2, true)`) and one who has none, and asserts the JSON carries `"smith":{"level":2,"title":"Journeyman"}` (or the package's field name) for the first and `{"level":0,"title":"Bellows Hand"}` for the second:

- `internal/org`: `TestTeamPageCarriesSmithRanks` (GET the team page; `members[].smith`) and `TestPeoplePickerCarriesSmithRanks`.
- `internal/journey`: `TestJourneyRowsCarrySmithAndSword` — the trainee has a `sword_ranks` row for forge-101 at level 3; the leader's Journey row for them has `smith` and `sword: {level: 3, rank: "Blade"}`; a row with no stored sword has no `sword` key.
- `internal/scoring`: `TestAnvilNamesCarrySmithRanks` (sign-offs and the queue).
- `internal/labs`: `TestApprovalsCarryTheRequestersSmithRank`.
- `internal/edits`: `TestEditsCarryAuthorAndReviewerSmithRanks`.

Also add to the journey test: rows come back in the same order with and without smith rows (nothing sorts by rank).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/org ./internal/journey ./internal/scoring ./internal/labs ./internal/edits -run Smith`
Expected: FAIL (unknown fields).

- [ ] **Step 3: Implement**

The pattern, once per list, after the slice is built and before it is returned:

```go
emails := make([]string, len(items))
for i, it := range items {
	emails[i] = it.Email // the field that holds the person's email
}
refs, err := smith.Lookup(ctx, db, emails)
if err != nil {
	return nil, err
}
for i := range items {
	items[i].Smith = refs.Of(items[i].Email)
}
```

- `org.Member` gets `Smith smith.Ref \`json:"smith"\``; fill in `members()` (it already queries users for these emails). `org.Person` the same, in `getPeople`.
- `journey.Row` gets `Smith smith.Ref \`json:"smith"\`` and `Sword *SwordRef \`json:"sword,omitempty"\`` with `type SwordRef struct { Level int \`json:"level"\`; Rank string \`json:"rank"\` }`. In `rows()`, after people are known: one `smith.Lookup` over the row emails; one `learn.Service.SwordRanks` over the people's user ids (the journey service already holds the learn service — use the field it has); set `Sword` when a level is stored for (person, row.Training), with `Rank: learn.Ladder(st.Platform.Settings.Ranks)[lvl].Name`.
- `scoring.SignOff` gets `TraineeSmith smith.Ref \`json:"trainee_smith"\``; `scoring.Submission` gets `TraineeSmith smith.Ref \`json:"trainee_smith"\`` (fill in the Anvil queue/list functions, not in single-row scans used elsewhere if those never reach the client — check each `Submission` returned by an HTTP handler).
- `labs.Approval` gets `RequesterSmith smith.Ref \`json:"requester_smith"\``.
- `edits.ContentEdit` gets `AuthorSmith smith.Ref \`json:"author_smith"\`` and `ReviewerSmith *smith.Ref \`json:"reviewer_smith,omitempty"\`` (set only when `Reviewer != ""`), filled in the list and detail handlers.

`smith` imports only `config` and pgx, so none of these packages gains a cycle.

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/org ./internal/journey ./internal/scoring ./internal/labs ./internal/edits ./internal/httpapi`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(api): people's smith ranks wherever a page names them; sword ranks on Journey rows

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/org internal/journey internal/scoring internal/labs internal/edits
```

---

### Task 5: Hearth, the user card and the training page

**Files:**
- Modify: `web/src/types.ts` (`Forge`, `SmithRef`, `Sword`)
- Create: `web/src/components/SmithMark.tsx`, `web/src/components/SmithMark.test.tsx`
- Modify: `web/src/components/RankCard.tsx` (+ `RankCard.test.tsx`), `web/src/pages/Hearth.tsx`, `web/src/components/UserMenu.tsx`, `web/src/pages/Training.tsx`
- Modify: `web/src/theme/app.css`, `.claude/skills/crucible-ui/SKILL.md`

**Interfaces:**
- Consumes: `GET /api/me/forge`, `POST /api/me/forge/seen` (Task 3).
- Produces: `SmithMark({ smith }: { smith?: SmithRef })`, `CraftCard({ forge })`, `SwordList({ forge })`, `RankUp` (props `{ kind: 'smith' | 'sword'; training?: string; title: string; level: number; onDone }`), `ExperienceMeter({ smith })`.

- [ ] **Step 1: Types**

```ts
export type SmithRef = { level: number; title: string }
export type Sword = { training: string; title: string; level: number; rank: string; percent: number; next: { name: string; at: number } | null }
export type Forge = {
  smith: { xp: number; level: number; title: string; next: { title: string; at: number } | null }
  swords: Sword[]
  badges: { training: string; title: string; earned_at: string }[]
  sword_ladder: { name: string; at: number }[]
  smith_ladder: { title: string; at: number }[]
  rank_up: { smith: SmithRef | null; swords: { training: string; title: string; level: number; rank: string }[] }
}
```

- [ ] **Step 2: Write the failing tests**

`web/src/components/SmithMark.test.tsx`:

```tsx
import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { SmithMark } from './SmithMark'

describe('the smith mark', () => {
  test('carries the title in words and its level as a class', () => {
    const h = renderToStaticMarkup(<SmithMark smith={{ level: 2, title: 'Journeyman' }} />)
    expect(h).toContain('Journeyman')
    expect(h).toContain('smith-mark l2')
  })
  test('a person with no smith rank yet is a Bellows Hand, and nothing is said about position', () => {
    const h = renderToStaticMarkup(<SmithMark smith={{ level: 0, title: 'Bellows Hand' }} />)
    expect(h).toContain('Bellows Hand')
    expect(h).not.toMatch(/rank #|top |leaderboard/i)
  })
  test('nothing when the payload has no smith rank', () => {
    expect(renderToStaticMarkup(<SmithMark />)).toBe('')
  })
})
```

Rewrite `RankCard.test.tsx` for `CraftCard` and `SwordList` with a `Forge` fixture: CraftCard shows the title, "720 / 1,200 to Smith" and a meter; at Grandmaster no "to"; SwordList shows "Forge 101", "Tempered", "52%", "Blade at 75%", and a Masterwork sword says it is finished; badges render; the smith RankUp says "You are now a Journeyman" and the sword one "Your Forge 101 sword is now a Blade"; calm motion renders the static line; nothing matches `/leaderboard|rank #|position/i`.

- [ ] **Step 3: Run them to see them fail**

Run: `cd web && npx vitest run src/components/SmithMark.test.tsx src/components/RankCard.test.tsx`
Expected: FAIL.

- [ ] **Step 4: Implement**

`web/src/components/SmithMark.tsx`:

```tsx
import type { SmithRef } from '../types'

// SmithMark is a person's smith rank beside their name: a hammer whose metal deepens with the rank, and the title in
// words (never colour alone). It never says where anyone stands against anyone else.
export function SmithMark({ smith }: { smith?: SmithRef }) {
  if (!smith) return null
  return (
    <span className={`smith-mark l${smith.level}`}>
      <span aria-hidden="true">⚒</span> {smith.title}
    </span>
  )
}
```

`RankCard.tsx` becomes (keep `RankUpSlot` as is):

```tsx
export function ExperienceMeter({ smith }: { smith: Forge['smith'] }) {
  const pct = smith.next ? Math.min(100, Math.floor((smith.xp / smith.next.at) * 100)) : 100
  const caption = smith.next ? `${smith.xp.toLocaleString()} / ${smith.next.at.toLocaleString()} to ${smith.next.title}` : `${smith.xp.toLocaleString()} experience`
  return <MoltenBar percent={pct} label="Experience" caption={caption} />
}

export function CraftCard({ forge }: { forge: Forge }) {
  return (
    <section className="rank-card craft" aria-labelledby="craft-heading">
      <h2 id="craft-heading">Your craft</h2>
      <p className="rank-name"><SmithMark smith={forge.smith} /></p>
      <ExperienceMeter smith={forge.smith} />
      <p className="muted">{forge.smith.next ? `Next: ${forge.smith.next.title} at ${forge.smith.next.at.toLocaleString()} experience` : 'The highest rank. The forge salutes you.'}</p>
    </section>
  )
}

export function SwordList({ forge }: { forge: Forge }) {
  return (
    <section className="rank-card swords" aria-labelledby="swords-heading">
      <h2 id="swords-heading">Your swords</h2>
      {forge.swords.length === 0 ? <p className="muted empty">No trainings yet: each one you take forges a sword.</p> : (
        <ul className="sword-list">
          {forge.swords.map((s) => (
            <li key={s.training}>
              <strong>{s.title}</strong> <span className="badge">{s.rank}</span> <span className="muted">{s.percent}%{s.next ? ` · ${s.next.name} at ${s.next.at}%` : ' · finished'}</span>
            </li>
          ))}
        </ul>
      )}
      {forge.badges.length > 0 && (
        <ul className="badges" data-testid="badges" aria-label="Badges">
          {forge.badges.map((b) => <li key={b.training} className="badge-token"><span aria-hidden="true">🏅</span> {b.title} <span className="muted">· earned {new Date(b.earned_at).toLocaleDateString()}</span></li>)}
        </ul>
      )}
    </section>
  )
}

// title is the new rank's name; training (for a sword) is the training it belongs to: its id for the server, its title
// for the words.
export function RankUp({ kind, training, title, level, onDone }: { kind: 'smith' | 'sword'; training?: { id: string; title: string }; title: string; level: number; onDone: () => void }) {
  const calm = useCalm()
  useEffect(() => {
    api('/api/me/forge/seen', { method: 'POST', json: { kind, training: training?.id ?? '', level } }).catch(() => {})
  }, [kind, training?.id, level])
  // … the existing hammer and glow, unchanged …
  return (
    <section className="rank-up" aria-label="Rank up">
      {/* existing motion spans */}
      <strong>{kind === 'smith' ? `You are now ${article(title)} ${title}.` : `Your ${training?.title} sword is now ${article(title)} ${title}.`}</strong>{' '}
      <span className="muted">{kind === 'smith' ? 'Your craft grows.' : 'Steel is forged in fire.'}</span>
      <button className="ghost" onClick={onDone}>Nice</button>
    </section>
  )
}
```

(`article` = `/^[AEIOU]/.test(t) ? 'an' : 'a'`, in `web/src/lib/forge.ts` with a test. A sword rise comes from `rank_up.swords[]` as `{training, title, level, rank}`: pass `training={{ id: up.training, title: up.title }}` and `title={up.rank}`.)

`Hearth.tsx`: render `<CraftCard>` and `<SwordList>` in `.forge-slot`; in the `RankUpSlot`, show the smith rise first if present, else the first sword rise, one at a time (dismissing shows the next).

`UserMenu.tsx`: when the card is open, `useFetch<Forge>(open ? '/api/me/forge' : null)` and show `<SmithMark>` and `<ExperienceMeter>` under the name in `.user-card-head`.

`Training.tsx`: `useFetch<Forge>('/api/me/forge')` and, when this training is among `swords`, a line at the top: `Your sword: <rank> · <percent>% · next <name> at <at>%` (or `· finished`).

`app.css` (tokens only; with a one-line comment each):

```css
/* A person's smith rank beside their name: the hammer's metal deepens with the rank, the title says it in words. */
.smith-mark { display: inline-flex; gap: 0.25em; align-items: center; font-size: 0.8rem; color: var(--muted); white-space: nowrap; }
.smith-mark > span[aria-hidden] { color: color-mix(in srgb, var(--accent) 35%, var(--muted)); }
.smith-mark.l2 > span[aria-hidden] { color: color-mix(in srgb, var(--accent) 60%, var(--muted)); }
.smith-mark.l3 > span[aria-hidden] { color: var(--accent); }
.smith-mark.l4 > span[aria-hidden] { color: var(--accent-2); }
.smith-mark.l5 > span[aria-hidden] { color: var(--spark); text-shadow: 0 0 6px var(--glow); }
/* Your swords on the Hearth: one line per training. */
.sword-list { list-style: none; padding: 0; margin: 0 0 0.75rem; display: grid; gap: 0.35rem; }
```

Check the new text pairs in `web/src/lib/contrast.test.ts` (High Contrast AAA) and add pairs if a new text/background combination appears.

`SKILL.md` patterns table: add **Smith mark** (`SmithMark` beside a name: hammer glyph tinted by level from tokens + the title in text; never ordered or numbered) and **Experience meter** (`MoltenBar` with "N / M to <next>"; static under calm).

- [ ] **Step 5: Run the web checks and look at it**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: PASS. Then screenshot the Hearth, the open user card and a training page with the dev server and a mocked API (`page.route('**/api/**', …)`, as in the cluster-labs plan's UI task) in Forge, Anvil, Parchment and High Contrast, at 390 px, and under calm motion; nothing scrolls sideways. Do not rebuild or restart any Docker stack.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/SmithMark.tsx web/src/components/SmithMark.test.tsx
git commit -m "feat(web): your craft and your swords on the Hearth, the smith rank and meter on your card and training page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src .claude/skills/crucible-ui/SKILL.md
```

---

### Task 6: Smith marks beside people's names; sword ranks on Journey

**Files:**
- Modify: `web/src/types.ts` (person payload fields), `web/src/pages/Team.tsx`, `web/src/pages/ManageTrainings.tsx`, `web/src/pages/Journey.tsx`, `web/src/components/HeatMap.tsx` (if Journey rows render there), `web/src/pages/Anvil.tsx`, `web/src/pages/Approvals.tsx`, `web/src/pages/Edits.tsx`, `web/src/pages/EditReview.tsx`, and their tests

**Interfaces:**
- Consumes: the JSON fields from Task 4; `SmithMark` (Task 5).

- [ ] **Step 1: Types**

Add `smith?: SmithRef` to the team member and person types, `smith?: SmithRef; sword?: { level: number; rank: string }` to `JourneyRow`, `trainee_smith?: SmithRef` to the Anvil sign-off and submission types, `requester_smith?: SmithRef` to the approval type, `author_smith?: SmithRef; reviewer_smith?: SmithRef` to `ContentEdit`.

- [ ] **Step 2: Write the failing render tests**

In each page's existing render test (`Team.test.tsx`, `ManageTrainings.test.tsx`, `Journey.test.tsx`, `EditReview.test.tsx`; add `Anvil.test.tsx` and `Approvals.test.tsx` following the same mocking pattern of `../useFetch` and `../me`), give the fixture's person `smith: { level: 3, title: 'Smith' }` and assert the markup contains `smith-mark l3` and `Smith` next to that person; for Journey also a row with `sword: { level: 3, rank: 'Blade' }` shows `Blade`; and every page's markup still has no `/rank #|top |leaderboard/i`. The Ledger page is unchanged.

- [ ] **Step 3: Run them to see them fail**

Run: `cd web && npx vitest run src/pages`
Expected: FAIL on the new assertions.

- [ ] **Step 4: Implement**

Render `<SmithMark smith={…} />` right after the person's name in: the Team roster name cell, the Manage trainings people picker option row, Journey's person header and row name, the Anvil queue row, sign-off row and detail header, the Approvals requester, the edits list author and the edit review's author and reviewer. On Journey rows render `<span className="badge">{row.sword.rank}</span>` beside the percent when `row.sword` is set. Keep every existing order; never sort by these fields.

- [ ] **Step 5: Run the web checks**

Run: `cd web && npm test && npx tsc -b && npm run lint && npm run build`
Expected: PASS. Screenshot Team and Journey (mocked API) at 1280 and 390 px in Forge and High Contrast.

- [ ] **Step 6: Commit**

```bash
git commit -m "feat(web): people's smith ranks beside their names; sword ranks on Journey rows

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src
```

---

### Task 7: Smith thresholds in Admin Settings

**Files:**
- Modify: `web/src/pages/AdminSettings.tsx`, `web/src/pages/AdminSettings.test.tsx`, `web/src/types.ts` (settings type)

**Interfaces:**
- Consumes: `GET/PUT /api/admin/settings` `smith: {apprentice, journeyman, smith, master_smith, grandmaster}` (Task 1).

- [ ] **Step 1: Write the failing test**

In `AdminSettings.test.tsx`, with a settings fixture carrying `smith: { apprentice: 150, journeyman: 500, smith: 1200, master_smith: 2500, grandmaster: 5000 }`, assert the page renders a "Smith ranks" section with five number fields labelled `Apprentice at (experience)`, `Journeyman at (experience)`, `Smith at (experience)`, `Master Smith at (experience)`, `Grandmaster at (experience)` holding those values, and the existing section is titled "Sword ranks".

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/pages/AdminSettings.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

Rename the "Forge ranks" heading to "Sword ranks" (its text: "The percent of a training a trainee must complete for each sword rank."). Add, after it, a "Smith ranks" section mirroring the ranks state: `const [smith, setSmith] = useState(() => Object.fromEntries(Object.entries(s.smith).map(([k, v]) => [k, String(v)])))`, a short line "The experience a smith needs for each rank. Bellows Hand is always 0.", and one `<label>` per key with a human label (`master_smith` → "Master Smith"), `type="number" min={1} step={1}`. Send `smith` (numbers) in the save body next to `ranks`.

- [ ] **Step 4: Run the web checks**

Run: `cd web && npm test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(web): admins set the experience each smith rank needs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/AdminSettings.tsx web/src/pages/AdminSettings.test.tsx web/src/types.ts
```

---

### Task 8: Docs

**Files:**
- Modify: `docs/user/trainees/ranks-and-badges.md`, `docs/user/getting-started/the-hearth.md`, `docs/user/getting-started/notifications.md`, `docs/user/admins/settings-and-registry.md`, `docs/user/scorers/rubrics-and-sign-offs.md` (rank mention), `docs/superpowers/specs/2026-10-05-crucible-design.md` (§2, §7, §10, §13), `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` (coverage row)

- [ ] **Step 1: Rewrite the trainees' page**

`docs/user/trainees/ranks-and-badges.md` keeps its front matter (`covers: [feature:ranks]`) and becomes:

```markdown
# Ranks and badges

You climb two kinds of rank in Crucible.

## Your swords

Every training you take forges a sword. It starts as **Ore** and becomes an **Ingot**, **Tempered**, a **Blade**, a
**Sword** and finally a **Masterwork** as you complete more of that training. Your Hearth lists your swords and how
far each has to go; a training's page shows its own.

## Your craft

Everything you do makes you a better smith. Each point of work you complete in any training is worth 10 experience,
and each time one of your swords reaches a new rank you earn a bonus (Ingot 20, Tempered 40, Blade 60, Sword 80,
Masterwork 150). Experience lifts your smith rank: **Bellows Hand**, **Apprentice**, **Journeyman**, **Smith**,
**Master Smith**, **Grandmaster**. Your card (your name, top right) shows your rank and how much experience the next
one needs.

## Ranks only rise

Neither a sword nor your smith rank is ever lowered, and experience never goes down, even if a training is reset or
taken away.

## Badges

Finish a training completely and you earn its badge, shown on your Hearth.

## Who sees them

Your smith rank is shown with your name wherever Crucible names you: your team's page, Journey, the Anvil, approvals,
content edits. Your swords are yours, and the people who already follow your progress in that training on Journey see
that sword too. Nobody is ever ranked against anyone else: there are no leaderboards.
```

- [ ] **Step 2: The other pages and the spec**

- `the-hearth.md`: the rank card line becomes "*Your craft* (your smith rank and experience) and *Your swords* (one per training)".
- `notifications.md`: the rank-up line becomes "when you reach a new smith rank (sword ranks show on the Hearth only)".
- `settings-and-registry.md`: the ranks paragraph becomes two: "Sword ranks — the % of a training needed for each sword rank" and "Smith ranks — the experience needed for each smith rank; lowering one can raise people's rank, raising one never lowers anyone".
- `rubrics-and-sign-offs.md`: "ranks are never lowered by a reset" → "sword and smith ranks are never lowered by a reset".
- Design spec §2: the decision line becomes "Ranks: a sword per training (% of that training) and a smith rank across Crucible (experience); see 2026-10-11-smith-and-sword-ranks-design.md". §7 replaces the forge-rank bullet with a two-sentence summary and the link; it removes "personal"/"no one else sees it" and keeps "no leaderboards". §10's rank-up event becomes "smith rank-up". §13 replaces `ranks` with `sword_ranks` and `smiths`.
- Roadmap: a coverage row `| Smith and sword ranks (2026-10-11 spec) | 2026-10 | TestSwordRankPerTraining; TestExperienceIsWorkPlusMilestones; TestExperienceNeverDrops; TestSmithRankUpNotifiesOnceUnderConcurrency; TestSmithThresholdChanges; TestJourneyRowsCarrySmithAndSword; SmithMark.test.tsx; forge-101.spec.ts | P |`.

- [ ] **Step 3: Run the docs test**

Run: `go test ./internal/docs`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "docs: swords per training and the smith rank, for trainees, admins and the spec

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs
```

---

### Task 9: End to end

**Files:**
- Modify: `e2e/tests/forge-101.spec.ts`, `e2e/tests/forge-people.spec.ts`

- [ ] **Step 1: Extend the journeys**

In `forge-101.spec.ts`, after the trainee completes module 1 and module 2 (where it already checks the Hearth rank today), replace any assertion on the old single rank (`getByTestId('rank')`) with:

```ts
await page.getByRole('link', { name: 'Hearth', exact: true }).click()
await expect(page.getByRole('heading', { name: 'Your swords' })).toBeVisible()
await expect(page.locator('.sword-list')).toContainText('Forge 101')
await page.getByRole('button', { name: /trainee|Tara/ }).click() // the user menu
await expect(page.locator('.user-card-head .smith-mark')).toBeVisible()
await page.keyboard.press('Escape')
await page.getByRole('link', { name: /Forge 101/ }).click()
await expect(page.getByText(/Your sword: /)).toBeVisible()
```

In `forge-people.spec.ts`, where the leader opens the team page and Journey, assert the trainee's row shows a `.smith-mark` with a title in text on both.

- [ ] **Step 2: Run the affected specs (fast loop)**

With a stack from this branch (the user's local stack is rebuilt only with their go-ahead; otherwise run the full `KEYCLOAK_PORT=8082 KEEP=1 make local-check`, which wipes local data — ask first): `cd e2e && CRUCIBLE_AGENT=$PWD/../bin/crucible-agent npx playwright test tests/forge-101.spec.ts tests/forge-people.spec.ts --project=local`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git commit -m "test(e2e): swords on the Hearth and training page, the smith rank on the card, Team and Journey

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- e2e/tests/forge-101.spec.ts e2e/tests/forge-people.spec.ts
```
