# M8a — Configuration and Org in Postgres: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every piece of Crucible's configuration — settings, schedules, quotes, admins, the
training registry, teams, membership, mentors, budgets, programs, roles and enrollments — out of the
git platform repo and into Postgres, with the admin UI writing rows instead of commits.

**Architecture:** A new `internal/org` package owns the new tables and returns the *existing*
`*config.Platform` struct, so `gitsync.Syncer` swaps `config.Load(dir)` for `org.Store.Platform(ctx)`
and every consumer (`rbac`, `labs`, `learn`, `journey`, `notify`, `edits`, `httpapi`) and its tests
stay unchanged. Writes go through `internal/org` in one transaction with their `audit_log` row, then
trigger the same state refresh a git sync triggers. Optimistic concurrency moves from `base_sha` to a
per-row `version` integer.

**Tech Stack:** Go 1.26, pgx/v5, goose migrations, chi router, React 19 + Vite SPA, Playwright,
testcontainers via `internal/db/dbtest`.

**Spec:** `docs/superpowers/specs/2026-10-07-db-owned-config-design.md`

**Out of scope, planned separately:** the logic-free rename of `config.Team`/`config.Program` to
`org.Team`/`org.Program` (spec §10 step 10) happens after this plan's suites are green, as its own
commit. And as M8b: export/import (`crucible export|import`,
`/api/admin/export|import`), `examples/seed.json`, the dev-only seed endpoint, and the e2e migration
away from the git-seeded platform repo. **Until M8b lands, `examples/platform` and
`CRUCIBLE_PLATFORM_REPO` stay in place for the e2e suites** and Task 9 keeps them working.

## Global Constraints

- Toolchain: `export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH` before any build or test.
- Checks that must pass at every commit: `gofmt -l cmd internal` (empty), `go vet ./...`,
  `go test -race ./...`.
- Migrations: `internal/db/migrations`, next free number, **always write a Down**.
- Never create real AWS resources. Terraform only with mock providers.
- Docker hygiene: never `docker system prune`; run one heavy suite at a time.
- Commits: pathspec form only — `git commit -m "…" -- <paths>`. Never `git add -A`. Trailer
  `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Every state-changing request keeps its same-origin Origin/Referer and JSON-body requirement.
- Rubrics, answer keys, raw content files and peers' uploads stay invisible to anyone enrolled.
- Money paths fail closed: no cost tiers → paid labs are refused, never priced at $0; unset
  `cluster_usd_per_hour` → cluster labs unavailable.
- Emails are stored lowercased and trimmed; they are the join key to `users`.
- Write prose and UI copy from the user's side of the screen; Crucible's voice is the forge metaphor.

## Review Focus

Five things the spec implies but no task's own happy path proves. Each has a test pinned to the task
that owns the code.

1. **Removing the last admin locks everyone out.** `DELETE /api/admin/admins/{email}` on the only
   remaining admin must be refused with a clear message — Task 3.
2. **A fresh instance has no cost tiers.** The app must still start and serve, with paid lab
   requests refused ("an admin must set cost tiers first") and cluster labs unavailable, never free
   — Task 1 (the fresh-row shape) and Task 2 (the refusal).
3. **Two admins editing one roster.** The git path had `base_sha`; rows need a `version` the client
   echoes back, and a stale write must be refused with "Someone changed this, reload" rather than
   silently overwriting — Task 4.
4. **Deleting a team or program must not delete learning history.** `item_progress`,
   `submissions`, `scores` and `ranks` key on `user_id` plus a plain `team` text column with no
   foreign key, so they survive a team delete by construction — and then reference a team id that
   no longer exists. Progress must survive, and Journey, Anvil and the Hearth must render without
   panicking on those orphaned rows — Task 7.
5. **Case and whitespace in emails.** `  Alice@Corp.COM ` and `alice@corp.com` are one person: one
   membership row, one enrollment, and the person keeps their progress — Task 4.

---

## File Structure

**Created**

- `internal/db/migrations/00018_org_config.sql` — the eleven tables, with a Down that drops them.
- `internal/org/org.go` — `Store`, `Platform(ctx)` (reads everything into `*config.Platform`).
- `internal/org/settings.go` — settings, schedules, quotes reads and writes + validation.
- `internal/org/admins.go` — admins list, add, remove, bootstrap.
- `internal/org/teams.go` — teams, membership, mentors, budgets.
- `internal/org/programs.go` — programs, roles, enrollments, pin.
- `internal/org/trainings.go` — the training registry.
- `internal/org/api.go` — chi routes for everything above; thin, permission-checking handlers.
- `internal/org/*_test.go` — one test file per source file, all on `dbtest.New(t)`.
- `web/src/pages/AdminSettings.tsx` — the page that replaces editing `platform.yaml`.
- `web/src/pages/AdminTrainings.tsx` — register and unregister training repos.

**Modified**

- `internal/gitsync/syncer.go` — `SyncOnce` takes the platform from `org`, not from a mirror.
- `internal/configapi/configapi.go` — roster, budget, program, pin handlers write rows; the
  `gitsync.Writer` path for config goes.
- `internal/configapi/seed.go` — bootstrap admin writes a row.
- `cmd/crucible-api/main.go` — construct `org.Store`, wire it into the syncer and the router.
- `web/src/pages/{Team,ProgramSettings}.tsx` — send `version` instead of `base_sha`.
- `deploy/compose/docker-compose.yml`, `deploy/helm/crucible/{values.yaml,templates/*}` — platform
  repo values become optional.
- `CLAUDE.md`, `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` — record the change.

**Unchanged on purpose:** `internal/config` keeps `Platform`, `Team`, `Program`, `Settings` and all
validation helpers; only its YAML `Load` loses the team/program half. `internal/rbac`,
`internal/labs`, `internal/learn`, `internal/journey`, `internal/notify`, `internal/scoring` and
their tests are not touched by this plan.

---

### Task 1: Tables and reads — `org.Store.Platform`

**Files:**
- Create: `internal/db/migrations/00018_org_config.sql`
- Create: `internal/org/org.go`
- Test: `internal/org/org_test.go`

**Interfaces:**
- Consumes: `internal/db/dbtest.New(t) *pgxpool.Pool`, `config.Platform`, `config.Team`,
  `config.Program`, `config.Settings`.
- Produces:
  ```go
  type Store struct{ DB *pgxpool.Pool }
  func (s *Store) Platform(ctx context.Context) (*config.Platform, error)
  ```
  `Platform` returns the same shape `config.Load` returns: `Settings` (with `Quotes` filled),
  `Admins`, `Trainings`, `Teams` keyed by id, each `Team.Programs` keyed by training id, each
  `Program` with `Schedule`/`Inline` resolved exactly as the YAML loader resolves them.

- [ ] **Step 1: Write the failing test**

```go
// internal/org/org_test.go
package org

import (
	"context"
	"testing"

	"crucible/internal/db/dbtest"
)

func TestPlatformOnAFreshDatabase(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	p, err := s.Platform(context.Background())
	if err != nil {
		t.Fatalf("Platform: %v", err)
	}
	if p.Settings.DefaultTheme != "forge" {
		t.Errorf("default theme = %q, want forge", p.Settings.DefaultTheme)
	}
	if p.Settings.CostTiers != nil {
		t.Error("a fresh instance must have no cost tiers: paid labs stay refused until an admin sets them")
	}
	if p.Settings.ClusterUSDPerHour != nil {
		t.Error("a fresh instance must have no cluster rate: cluster labs unavailable, never free")
	}
	if p.Settings.EscalationHours != 4 {
		t.Errorf("escalation hours = %v, want the 4-hour default", p.Settings.EscalationHours)
	}
	if got := p.Settings.Ranks; got.Masterwork != 100 || got.Ingot != 20 {
		t.Errorf("ranks = %+v, want the spec defaults", got)
	}
	if len(p.Admins) != 0 || len(p.Teams) != 0 || len(p.Trainings) != 0 {
		t.Errorf("fresh instance must be empty: admins=%v teams=%v trainings=%v", p.Admins, p.Teams, p.Trainings)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
go test ./internal/org/ -run TestPlatformOnAFreshDatabase
```
Expected: build failure — package `org` does not exist.

- [ ] **Step 3: Write the migration**

```sql
-- internal/db/migrations/00018_org_config.sql
-- +goose Up
-- Configuration and org move out of the git platform repo (spec: 2026-10-07-db-owned-config-design.md).
CREATE TABLE settings (
  id                   int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  default_theme        text NOT NULL DEFAULT 'forge',
  auto_approve_usd     numeric,          -- NULL together with the tiers below: tiers unset, paid labs refused
  tier1_usd            numeric,
  tier2_usd            numeric,
  cluster_usd_per_hour numeric,          -- NULL: cluster labs unavailable, never free
  escalation_hours     numeric NOT NULL DEFAULT 4,
  rank_ingot           numeric NOT NULL DEFAULT 20,
  rank_tempered        numeric NOT NULL DEFAULT 45,
  rank_blade           numeric NOT NULL DEFAULT 75,
  rank_sword           numeric NOT NULL DEFAULT 90,
  rank_masterwork      numeric NOT NULL DEFAULT 100,
  version              bigint  NOT NULL DEFAULT 1
);
INSERT INTO settings (id) VALUES (1);

CREATE TABLE schedules (
  name     text PRIMARY KEY,
  timezone text  NOT NULL,
  windows  jsonb NOT NULL                -- [{days:[mon..sun], start:"08:00", end:"19:00"}]
);

CREATE TABLE quotes (id bigserial PRIMARY KEY, text text NOT NULL);

CREATE TABLE admins (email text PRIMARY KEY);

CREATE TABLE trainings (
  id     text PRIMARY KEY,
  repo   text NOT NULL,
  branch text NOT NULL DEFAULT 'main'
);

CREATE TABLE teams (
  id      text PRIMARY KEY,
  name    text   NOT NULL,
  version bigint NOT NULL DEFAULT 1
);

CREATE TABLE team_members (
  team  text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  email text NOT NULL,
  role  text NOT NULL CHECK (role IN ('leader', 'senior', 'member', 'trainee')),
  PRIMARY KEY (team, email)              -- one role per person per team (spec 5.2)
);

CREATE TABLE mentors (
  team    text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  trainee text NOT NULL,
  mentor  text NOT NULL,
  PRIMARY KEY (team, trainee)
);

CREATE TABLE team_webhooks (
  team text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('slack', 'teams')),
  url  text NOT NULL,
  PRIMARY KEY (team, kind)
);

CREATE TABLE team_budgets (
  team         text PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,
  monthly_usd  numeric NOT NULL DEFAULT 0,
  hard_cap_usd numeric NOT NULL DEFAULT 0,
  version      bigint  NOT NULL DEFAULT 1
);

CREATE TABLE programs (
  team                 text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  training             text NOT NULL,
  pinned_ref           text,             -- NULL: track the tracked branch head
  schedule_name        text,
  inline_schedule      jsonb,
  ttl                  text,
  idle_timeout         text,
  max_extension        text,
  budget_usd_month     numeric NOT NULL DEFAULT 0,
  review_self_reported boolean NOT NULL DEFAULT false,
  version              bigint  NOT NULL DEFAULT 1,
  PRIMARY KEY (team, training)
);

CREATE TABLE program_roles (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL,
  role     text NOT NULL CHECK (role IN ('manager', 'scorer', 'approver')),
  PRIMARY KEY (team, training, email, role),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);

CREATE TABLE enrollments (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL,
  PRIMARY KEY (team, training, email),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE enrollments, program_roles, programs, team_budgets, team_webhooks, mentors,
  team_members, teams, trainings, admins, quotes, schedules, settings;
```

- [ ] **Step 4: Write the store's reads**

`internal/org/org.go`. Shape it as one query per table and assemble in Go — at this scale that is
eight small queries, and it keeps each row-to-struct mapping readable:

```go
// Package org owns Crucible's configuration and org data: settings, schedules, quotes, admins, the
// training registry, teams, membership, mentors, budgets, programs, roles and enrollments.
// It returns config.Platform so every consumer of the old git-loaded config is unchanged.
package org

type Store struct{ DB *pgxpool.Pool }

func (s *Store) Platform(ctx context.Context) (*config.Platform, error) {
	p := &config.Platform{Trainings: map[string]config.TrainingRef{}, Teams: map[string]*config.Team{}}
	if err := s.settings(ctx, &p.Settings); err != nil { return nil, err }
	// … schedules, quotes, admins, trainings, teams+members+mentors+webhooks+budgets, programs+roles+enrollments
	return p, nil
}
```

Rules this read must honour, all of them already decided by `config.Load`:

- `default_theme` defaults to `forge`; an unknown theme is a stored-data error, not a panic.
- NULL tiers → `Settings.CostTiers == nil`. NULL cluster rate → `ClusterUSDPerHour == nil`.
- `escalation_hours` 0 → 4.
- Rank thresholds come from the row; call the existing `Ranks.fill()` so missing values take the
  spec defaults and ordering is enforced.
- `Team.Leader` is the single `team_members` row with role `leader`; `Seniors`, `Members`,
  `Trainees` are the other roles, sorted, so output is stable.
- `Program.ScheduleSpec`/`Schedule`/`Inline` resolve the same way: a `schedule_name` must exist in
  `schedules` or the program is reported as a problem; `inline_schedule` is validated with the
  existing `Schedule.validate()`.
- `Program.Training` is set from the row's `training`.

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/org/ -run TestPlatformOnAFreshDatabase -v
```
Expected: PASS.

- [ ] **Step 6: Add the round-trip read test**

```go
func TestPlatformReadsTeamsProgramsAndRoles(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	mustExec(t, pool, `INSERT INTO teams (id, name) VALUES ('forge', 'The Forge')`)
	mustExec(t, pool, `INSERT INTO team_members (team, email, role) VALUES
		('forge','leader@x','leader'), ('forge','senior@x','senior'), ('forge','trainee@x','trainee')`)
	mustExec(t, pool, `INSERT INTO mentors (team, trainee, mentor) VALUES ('forge','trainee@x','senior@x')`)
	mustExec(t, pool, `INSERT INTO team_budgets (team, monthly_usd, hard_cap_usd) VALUES ('forge', 200, 250)`)
	mustExec(t, pool, `INSERT INTO trainings (id, repo, branch) VALUES ('forge-101','https://git/x.git','main')`)
	mustExec(t, pool, `INSERT INTO programs (team, training, budget_usd_month) VALUES ('forge','forge-101', 100)`)
	mustExec(t, pool, `INSERT INTO program_roles (team, training, email, role) VALUES ('forge','forge-101','senior@x','scorer')`)
	mustExec(t, pool, `INSERT INTO enrollments (team, training, email) VALUES ('forge','forge-101','trainee@x')`)

	p, err := (&Store{DB: pool}).Platform(ctx)
	if err != nil { t.Fatalf("Platform: %v", err) }
	team := p.Teams["forge"]
	if team == nil || team.Leader != "leader@x" || len(team.Trainees) != 1 {
		t.Fatalf("team = %+v", team)
	}
	if team.Mentors["trainee@x"] != "senior@x" { t.Errorf("mentors = %v", team.Mentors) }
	if team.Budget.MonthlyUSD != 200 || team.Budget.HardCapUSD != 250 { t.Errorf("budget = %+v", team.Budget) }
	prog := team.Programs["forge-101"]
	if prog == nil || prog.Training != "forge-101" { t.Fatalf("program = %+v", prog) }
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "senior@x" { t.Errorf("roles = %+v", prog.Roles) }
	if len(prog.Enrolled) != 1 || prog.Enrolled[0] != "trainee@x" { t.Errorf("enrolled = %v", prog.Enrolled) }
}
```

Add `mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any)` as a small helper in the same file.

- [ ] **Step 7: Verify the whole suite and the migration test**

```bash
go test ./internal/org/ ./internal/db/... -race
gofmt -l cmd internal && go vet ./...
```
Expected: PASS, no gofmt output. `TestMigrationsCreateTablesAndAreIdempotent` must still pass —
it walks the migrations, so the new file is covered by it automatically.

- [ ] **Step 8: Commit**

```bash
git commit -m "feat(org): configuration and org tables, read into config.Platform

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/db/migrations/00018_org_config.sql internal/org/
```

---

### Task 2: Settings, schedules and quotes writes

**Files:**
- Create: `internal/org/settings.go`, `internal/org/settings_test.go`
- Modify: `internal/org/api.go` (created here), `internal/org/org.go` if a helper moves

**Interfaces:**
- Consumes: Task 1's `Store`, `config.Settings`, `config.CostTiers`, `config.RankThresholds`,
  `audit.Log`.
- Produces:
  ```go
  type SettingsBody struct {
      Version           int64                  `json:"version"`
      DefaultTheme      string                 `json:"default_theme"`
      CostTiers         *config.CostTiers      `json:"cost_tiers"`
      ClusterUSDPerHour *float64               `json:"cluster_usd_per_hour"`
      EscalationHours   float64                `json:"escalation_hours"`
      Ranks             config.RankThresholds  `json:"ranks"`
  }
  func (s *Store) SetSettings(ctx context.Context, actor string, b SettingsBody) error
  func (s *Store) SetSchedule(ctx context.Context, actor, name string, sched config.Schedule) error
  func (s *Store) DeleteSchedule(ctx context.Context, actor, name string) error
  func (s *Store) SetQuotes(ctx context.Context, actor string, quotes []string) error
  ```
  All four write their `audit_log` row in the same transaction as the change, with actions
  `settings.update`, `schedule.update`, `schedule.delete`, `quotes.update`.

- [ ] **Step 1: Write the failing validation tests**

```go
func TestSetSettingsRefusesBadValues(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for name, b := range map[string]SettingsBody{
		"tiers out of order":  {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{AutoApproveUSD: 10, Tier1USD: 5, Tier2USD: 25}},
		"tier1 zero":          {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{Tier1USD: 0, Tier2USD: 1}},
		"unknown theme":       {Version: 1, DefaultTheme: "chrome", CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}},
		"negative escalation": {Version: 1, DefaultTheme: "forge", EscalationHours: -1, CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}},
		"masterwork not 100":  {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}, Ranks: config.RankThresholds{Ingot: 20, Tempered: 45, Blade: 75, Sword: 90, Masterwork: 99}},
		"negative rate":       {Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}, ClusterUSDPerHour: ptr(-1.0)},
	} {
		if err := s.SetSettings(ctx, "admin@x", b); err == nil {
			t.Errorf("%s: want a refusal, got nil", name)
		}
	}
	p, _ := s.Platform(ctx) // nothing invalid may be stored
	if p.Settings.CostTiers != nil {
		t.Error("a refused save must not have written anything")
	}
}

func TestSetSettingsStoresAndAudits(t *testing.T) {
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	b := SettingsBody{Version: 1, DefaultTheme: "anvil", EscalationHours: 6,
		CostTiers: &config.CostTiers{AutoApproveUSD: 0, Tier1USD: 5, Tier2USD: 25}, ClusterUSDPerHour: ptr(0.0)}
	if err := s.SetSettings(ctx, "admin@x", b); err != nil { t.Fatalf("SetSettings: %v", err) }
	p, _ := s.Platform(ctx)
	if p.Settings.DefaultTheme != "anvil" || p.Settings.CostTiers.Tier2USD != 25 {
		t.Fatalf("settings = %+v", p.Settings)
	}
	if *p.Settings.ClusterUSDPerHour != 0 {
		t.Error("an explicit 0 cluster rate means free on the node, and must survive as 0, not NULL")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'settings.update' AND actor = 'admin@x'`).Scan(&n)
	if n != 1 { t.Errorf("audit rows = %d, want 1", n) }
}

func TestSetSettingsRefusesAStaleVersion(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	ok := SettingsBody{Version: 1, DefaultTheme: "forge", CostTiers: &config.CostTiers{Tier1USD: 5, Tier2USD: 25}}
	if err := s.SetSettings(ctx, "admin@x", ok); err != nil { t.Fatal(err) }
	if err := s.SetSettings(ctx, "other@x", ok); err == nil {
		t.Error("a second save at version 1 must be refused: someone else changed this, reload")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/org/ -run TestSetSettings
```
Expected: FAIL — `SetSettings` undefined.

- [ ] **Step 3: Implement `SetSettings` and friends**

Reuse the existing validation rather than restating it: `config.Settings` already knows how to
check tiers, theme, escalation, ranks and schedules in `config.Load`. Extract those checks from
`Load` into an exported `func (s *Settings) Validate() error` in `internal/config/config.go` and
call it from both places, so git-loaded and DB-loaded settings can never diverge.

Write with `UPDATE settings SET … WHERE id = 1 AND version = $n RETURNING version`; zero rows
means a stale version → `apperr.Wrap(apperr.Conflict, "someone changed the settings, reload")`.
Wrap the UPDATE and `audit.Log` in one `pgx.Tx`.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/org/ -run TestSetSettings -v
go test ./internal/config/ -race     # the extracted Validate must not change YAML behaviour
```
Expected: PASS, including every existing `internal/config` test.

- [ ] **Step 5: Add the Review Focus #2 test — a fresh instance refuses paid labs**

```go
func TestNoCostTiersRefusesPaidLabsAndNeverPricesThemFree(t *testing.T) {
	p, _ := (&Store{DB: dbtest.New(t)}).Platform(context.Background())
	if p.Settings.CostTiers != nil { t.Fatal("precondition: fresh instance has no tiers") }
	if tier := rbac.Tier("aws", 3, config.CostTiers{}); tier == "auto" {
		t.Error("with no tiers configured an aws lab must never auto-approve")
	}
}
```
If `rbac.Tier` cannot express "unset", add the guard where the estimate is routed and assert the
request is refused with a message naming cost tiers — the rule is that paid labs fail closed.

- [ ] **Step 6: Schedules and quotes**

```go
func TestSetScheduleValidatesWindows(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	bad := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"funday"}, Start: "08:00", End: "19:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "business-hours", bad); err == nil {
		t.Error("an unknown day must be refused")
	}
	crossesMidnight := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"mon"}, Start: "20:00", End: "04:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "nights", crossesMidnight); err == nil {
		t.Error("a window crossing midnight must be refused")
	}
	good := config.Schedule{Timezone: "Europe/Bucharest", Windows: []config.Window{{Days: []string{"mon", "fri"}, Start: "08:00", End: "19:00"}}}
	if err := s.SetSchedule(ctx, "admin@x", "business-hours", good); err != nil { t.Fatal(err) }
	p, _ := s.Platform(ctx)
	if p.Settings.Schedules["business-hours"] == nil { t.Error("the schedule must read back") }
}

func TestDeleteScheduleRefusedWhileAProgramUsesIt(t *testing.T) { /* insert team+program with schedule_name, expect refusal naming the program */ }

func TestSetQuotesReplacesTheList(t *testing.T) { /* set two, set one, expect one; built-ins are served by meta, not stored */ }
```

- [ ] **Step 7: Run everything and commit**

```bash
go test ./internal/org/ ./internal/config/ -race && gofmt -l cmd internal && go vet ./...
git commit -m "feat(org): settings, schedules and quotes writes with validation and audit

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/ internal/config/config.go
```

---

### Task 3: Admins and the bootstrap admin

**Files:**
- Create: `internal/org/admins.go`, `internal/org/admins_test.go`
- Modify: `internal/configapi/seed.go`, `internal/configapi/seed_test.go`

**Interfaces:**
- Consumes: Task 1's `Store`.
- Produces:
  ```go
  func (s *Store) Admins(ctx context.Context) ([]string, error)
  func (s *Store) AddAdmin(ctx context.Context, actor, email string) error
  func (s *Store) RemoveAdmin(ctx context.Context, actor, email string) error
  func (s *Store) SeedAdmin(ctx context.Context, email string) (bool, error) // true when it inserted
  ```
  `SeedAdmin` replaces `configapi.SeedAdmin`'s git write and keeps its contract: it does nothing
  once any admin exists, and is audited as `admin.bootstrap`.

- [ ] **Step 1: Write the failing tests, including Review Focus #1**

```go
func TestSeedAdminOnlyIntoAnEmptyTable(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	ok, err := s.SeedAdmin(ctx, " Admin@Corp.COM ")
	if err != nil || !ok { t.Fatalf("first seed: ok=%v err=%v", ok, err) }
	admins, _ := s.Admins(ctx)
	if len(admins) != 1 || admins[0] != "admin@corp.com" {
		t.Fatalf("admins = %v, want the lowercased trimmed address", admins)
	}
	ok, err = s.SeedAdmin(ctx, "second@corp.com")
	if err != nil || ok { t.Errorf("a second seed must do nothing: ok=%v err=%v", ok, err) }
	if admins, _ = s.Admins(ctx); len(admins) != 1 { t.Errorf("admins = %v", admins) }
}

func TestRemoveLastAdminIsRefused(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if _, err := s.SeedAdmin(ctx, "admin@x"); err != nil { t.Fatal(err) }
	if err := s.RemoveAdmin(ctx, "admin@x", "admin@x"); err == nil {
		t.Fatal("removing the only admin must be refused: it locks everyone out")
	}
	if err := s.AddAdmin(ctx, "admin@x", "second@x"); err != nil { t.Fatal(err) }
	if err := s.RemoveAdmin(ctx, "admin@x", "admin@x"); err != nil {
		t.Errorf("removing one of two admins must be allowed: %v", err)
	}
}

func TestAdminWritesAreAudited(t *testing.T) { /* admin.add, admin.remove rows with the actor */ }
func TestAddAdminIsIdempotentAndValidatesTheAddress(t *testing.T) { /* "notanemail" refused; adding twice is one row */ }
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/org/ -run 'TestSeedAdmin|TestRemoveLastAdmin|TestAdmin|TestAddAdmin'
```
Expected: FAIL — undefined methods.

- [ ] **Step 3: Implement**

`SeedAdmin`: `INSERT INTO admins (email) SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM admins)` inside a
transaction with the audit row; the insert's row count is the `bool`. `RemoveAdmin`: delete inside a
transaction that first counts admins, refusing with
`apperr.Wrap(apperr.Invalid, "this is the only admin; add another before removing this one")`.
Reuse `configapi.checkEmail` by exporting it as `config.CheckEmail` so both packages validate
addresses identically.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/org/ -run 'TestSeedAdmin|TestRemoveLastAdmin|TestAdmin|TestAddAdmin' -v
```
Expected: PASS.

- [ ] **Step 5: Repoint the bootstrap path**

Replace the body of `configapi.SeedAdmin` with a call to `org.Store.SeedAdmin`, keeping its exported
signature so `cmd/crucible-api/main.go` does not change in this task. Update
`internal/configapi/seed_test.go`: the existing `TestSeedAdminOnlyIntoAnEmptyAdminsFile` and
`TestBootstrapAdminRunsOnce` keep their names and intent, with the assertions moved from
`admins.yaml` to the `admins` table.

- [ ] **Step 6: Run and commit**

```bash
go test ./internal/org/ ./internal/configapi/ -race && gofmt -l cmd internal && go vet ./...
git commit -m "feat(org): admins in Postgres, bootstrap admin seeds a row

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/ internal/configapi/
```

---

### Task 4: Teams, membership and mentors

**Files:**
- Create: `internal/org/teams.go`, `internal/org/teams_test.go`

**Interfaces:**
- Consumes: Task 1's `Store`, `rbac.Checker`, `config.Platform`.
- Produces:
  ```go
  type TeamBody struct {
      Version  int64             `json:"version"`
      Name     string            `json:"name"`
      Leader   string            `json:"leader"`
      Seniors  []string          `json:"seniors"`
      Members  []string          `json:"members"`
      Trainees []string          `json:"trainees"`
      Mentors  map[string]string `json:"mentors"`
  }
  func (s *Store) CreateTeam(ctx context.Context, actor, id string, b TeamBody) error
  func (s *Store) SetTeam(ctx context.Context, actor, id string, b TeamBody) error
  func (s *Store) DeleteTeam(ctx context.Context, actor, id string) error
  ```

- [ ] **Step 1: Write the failing tests, including Review Focus #3 and #5**

```go
func TestCreateTeamStoresLeaderAndRoster(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"senior@x"}, Trainees: []string{"new@x"},
		Mentors: map[string]string{"new@x": "senior@x"}}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil { t.Fatalf("CreateTeam: %v", err) }
	p, _ := s.Platform(ctx)
	team := p.Teams["platform"]
	if team == nil || team.Leader != "lead@x" || team.Name != "Platform" { t.Fatalf("team = %+v", team) }
	if team.Mentors["new@x"] != "senior@x" { t.Errorf("mentors = %v", team.Mentors) }
}

func TestTeamIDsAreSlugs(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	for _, id := range []string{"", "../etc", "Platform Team", "a/b", strings.Repeat("x", 100)} {
		if err := s.CreateTeam(context.Background(), "admin@x", id, TeamBody{Name: "x", Leader: "l@x"}); err == nil {
			t.Errorf("id %q must be refused: ids appear in URLs and audit targets", id)
		}
	}
}

func TestEmailsAreNormalisedToOnePerson(t *testing.T) { // Review Focus #5
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	b := TeamBody{Name: "Platform", Leader: "lead@x", Trainees: []string{"  Alice@Corp.COM ", "alice@corp.com"}}
	if err := s.CreateTeam(ctx, "admin@x", "platform", b); err != nil { t.Fatalf("CreateTeam: %v", err) }
	p, _ := s.Platform(ctx)
	if got := p.Teams["platform"].Trainees; len(got) != 1 || got[0] != "alice@corp.com" {
		t.Errorf("trainees = %v, want one lowercased address", got)
	}
}

func TestOneRolePerPersonPerTeam(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	b := TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"dual@x"}, Trainees: []string{"dual@x"}}
	if err := s.CreateTeam(context.Background(), "admin@x", "platform", b); err == nil {
		t.Error("the same person in two roles must be refused (spec 5.2: one role per team)")
	}
}

func TestStaleTeamVersionIsRefused(t *testing.T) { // Review Focus #3
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "admin@x", "platform", TeamBody{Name: "Platform", Leader: "lead@x"}); err != nil { t.Fatal(err) }
	p, _ := s.Platform(ctx)
	v := p.Teams["platform"].Version // Platform() must carry the version for the client to echo
	first := TeamBody{Version: v, Name: "Platform", Leader: "lead@x", Trainees: []string{"a@x"}}
	second := TeamBody{Version: v, Name: "Platform", Leader: "lead@x", Trainees: []string{"b@x"}}
	if err := s.SetTeam(ctx, "admin@x", "platform", first); err != nil { t.Fatal(err) }
	if err := s.SetTeam(ctx, "other@x", "platform", second); err == nil {
		t.Error("the second writer must be told to reload, not silently win")
	}
	if p, _ = s.Platform(ctx); len(p.Teams["platform"].Trainees) != 1 || p.Teams["platform"].Trainees[0] != "a@x" {
		t.Error("the refused write must have changed nothing")
	}
}

func TestMentorMustBeInTheTeamAndNotTheTraineeThemselves(t *testing.T) { /* refuse unknown mentor, refuse self-mentoring */ }
func TestDeleteTeamKeepsLearningHistory(t *testing.T) { /* see Task 7 for the full version */ }
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/org/ -run 'TestCreateTeam|TestTeamIDs|TestEmailsAre|TestOneRole|TestStaleTeam|TestMentor'
```
Expected: FAIL — undefined methods. Note `config.Team` needs a `Version int64` field added with
`yaml:"-"`, so the YAML loader is unaffected.

- [ ] **Step 3: Implement**

One transaction per write: bump `teams.version` guarded by the caller's version, delete and reinsert
`team_members` and `mentors` for that team, write the audit row with action `team.create`,
`team.roster` or `team.delete` and target the team id. Normalise every address with
`strings.ToLower(strings.TrimSpace(…))` **before** the duplicate checks, so Review Focus #5 is
structural rather than a special case. Validate the id with
`id != "" && filepath.IsLocal(id) && !strings.ContainsAny(id, "/\\ ") && len(id) <= 64`.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/org/ -run 'TestCreateTeam|TestTeamIDs|TestEmailsAre|TestOneRole|TestStaleTeam|TestMentor' -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go test ./internal/org/ -race && gofmt -l cmd internal && go vet ./...
git commit -m "feat(org): teams, membership and mentors with optimistic versions

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/ internal/config/config.go
```

---

### Task 5: Budgets

**Files:**
- Create: `internal/org/budgets.go` (or extend `teams.go` if it is still short), `internal/org/budgets_test.go`

**Interfaces:**
- Produces:
  ```go
  type BudgetBody struct {
      Version     int64   `json:"version"`
      MonthlyUSD  float64 `json:"monthly_usd"`
      HardCapUSD  float64 `json:"hard_cap_usd"`
  }
  func (s *Store) SetBudget(ctx context.Context, actor, team string, b BudgetBody) error
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestSetBudgetStoresAndDefaultsTheCap(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	if err := s.CreateTeam(ctx, "admin@x", "platform", TeamBody{Name: "Platform", Leader: "lead@x"}); err != nil { t.Fatal(err) }
	if err := s.SetBudget(ctx, "admin@x", "platform", BudgetBody{Version: 1, MonthlyUSD: 200}); err != nil { t.Fatal(err) }
	p, _ := s.Platform(ctx)
	if b := p.Teams["platform"].Budget; b.MonthlyUSD != 200 || b.HardCapUSD != 200 {
		t.Errorf("budget = %+v; an unset hard cap defaults to the monthly budget", b)
	}
}

func TestSetBudgetRefusesNegativeAndCapBelowBudget(t *testing.T) {
	// monthly -1 refused; hard_cap 50 with monthly 200 refused, naming both numbers
}

func TestSetBudgetIsAudited(t *testing.T) { /* action team.budget, target the team */ }
```

- [ ] **Step 2: Run and watch fail; Step 3: implement; Step 4: run and pass**

```bash
go test ./internal/org/ -run TestSetBudget -v
```
Implementation mirrors Task 4's transaction shape against `team_budgets`, with
`INSERT … ON CONFLICT (team) DO UPDATE … WHERE team_budgets.version = $n`.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(org): team budgets in Postgres

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/
```

---

### Task 6: The training registry

**Files:**
- Create: `internal/org/trainings.go`, `internal/org/trainings_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) AddTraining(ctx context.Context, actor, id, repo, branch string) error
  func (s *Store) RemoveTraining(ctx context.Context, actor, id string) error
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestAddTrainingValidatesIDAndRepo(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	for name, args := range map[string][3]string{
		"empty id":     {"", "https://git/x.git", "main"},
		"path-ish id":  {"../x", "https://git/x.git", "main"},
		"no repo":      {"x", "", "main"},
		"file url":     {"x", "file:///git/x.git", "main"}, // only with CRUCIBLE_GIT_ALLOW_FILE
	} {
		if err := s.AddTraining(ctx, "admin@x", args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
	if err := s.AddTraining(ctx, "admin@x", "forge-101", "https://git/x.git", ""); err != nil { t.Fatal(err) }
	p, _ := s.Platform(ctx)
	if ref := p.Trainings["forge-101"]; ref.Branch != "main" {
		t.Errorf("branch = %q, want the main default", ref.Branch)
	}
}

func TestRemoveTrainingRefusedWhileAProgramUsesIt(t *testing.T) {
	// a program for that training must block removal, naming the team; unregistering must never
	// orphan a program row
}

func TestTrainingWritesAreAudited(t *testing.T) { /* training.add, training.remove */ }
```

- [ ] **Step 2: Run and watch fail; Step 3: implement; Step 4: run and pass**

```bash
go test ./internal/org/ -run 'TestAddTraining|TestRemoveTraining|TestTrainingWrites' -v
```
Honour `CRUCIBLE_GIT_ALLOW_FILE`: a `file://` repo is refused unless it is set, matching
`gitsync`'s existing rule, so a production instance cannot be pointed at a local path.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(org): training registry in Postgres

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/
```

---

### Task 7: Programs, roles, enrollments and the pin

**Files:**
- Create: `internal/org/programs.go`, `internal/org/programs_test.go`

**Interfaces:**
- Produces:
  ```go
  type ProgramBody struct {
      Version            int64             `json:"version"`
      Roles              config.Roles      `json:"roles"`
      Enrolled           []string          `json:"enrolled"`
      Schedule           string            `json:"schedule"`
      InlineSchedule     *config.Schedule  `json:"inline_schedule"`
      TTL                string            `json:"ttl"`
      IdleTimeout        string            `json:"idle_timeout"`
      MaxExtension       string            `json:"max_extension"`
      BudgetUSDMonth     float64           `json:"budget_usd_month"`
      ReviewSelfReported bool              `json:"review_self_reported"`
  }
  func (s *Store) Enroll(ctx context.Context, actor, team, training string, b ProgramBody) error // creates the program
  func (s *Store) SetProgram(ctx context.Context, actor, team, training string, b ProgramBody) error
  func (s *Store) SetPin(ctx context.Context, actor, team, training, sha string) error
  func (s *Store) DeleteProgram(ctx context.Context, actor, team, training string) error
  ```

- [ ] **Step 1: Write the failing tests, including Review Focus #4**

```go
func TestEnrollAppliesRoleDefaults(t *testing.T) {
	s := &Store{DB: dbtest.New(t)}
	ctx := context.Background()
	mustTeam(t, s, "platform", TeamBody{Name: "Platform", Leader: "lead@x", Seniors: []string{"senior@x"}, Trainees: []string{"new@x"}})
	mustTraining(t, s, "forge-101")
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil { t.Fatal(err) }
	p, _ := s.Platform(ctx)
	prog := p.Teams["platform"].Programs["forge-101"]
	if len(prog.Roles.Manager) != 1 || prog.Roles.Manager[0] != "lead@x" {
		t.Errorf("manager = %v; the leader is the default manager (spec 5.2)", prog.Roles.Manager)
	}
	if len(prog.Roles.Approvers) != 1 || prog.Roles.Approvers[0] != "lead@x" {
		t.Errorf("approvers = %v; the leader is the default approver", prog.Roles.Approvers)
	}
	if len(prog.Roles.Scorers) != 1 || prog.Roles.Scorers[0] != "senior@x" {
		t.Errorf("scorers = %v; seniors are the default scorers", prog.Roles.Scorers)
	}
}

func TestProgramRefusesUnknownScheduleAndBadLabDefaults(t *testing.T) {
	// schedule: "nights" with no such schedule → refused, naming it
	// idle_timeout 20m with idle_warning ≥ it is a lab.yaml rule, but ttl "banana" must be refused here
}

func TestEnrollRefusesSomeoneOutsideTheTeam(t *testing.T) { /* enrolling a stranger is refused */ }

func TestSetPinRecordsTheSHAAndIsAudited(t *testing.T) { /* program.pin with the sha in the detail */ }

func TestDeleteProgramKeepsLearningHistory(t *testing.T) { // Review Focus #4
	pool := dbtest.New(t)
	s := &Store{DB: pool}
	ctx := context.Background()
	mustTeam(t, s, "platform", TeamBody{Name: "Platform", Leader: "lead@x", Trainees: []string{"new@x"}})
	mustTraining(t, s, "forge-101")
	if err := s.Enroll(ctx, "admin@x", "platform", "forge-101", ProgramBody{Enrolled: []string{"new@x"}}); err != nil { t.Fatal(err) }
	var uid int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (sub, email) VALUES ('s1','new@x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
		VALUES ($1,'platform','forge-101','01-foundations','reading','complete',1)`, uid)
	if err := s.DeleteProgram(ctx, "admin@x", "platform", "forge-101"); err != nil { t.Fatal(err) }
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM item_progress WHERE user_id = $1`, uid).Scan(&n)
	if n != 1 { t.Fatalf("progress rows after deleting the program = %d, want 1: a score is the trainee's", n) }
}

func TestDeleteTeamKeepsLearningHistoryAndDropsItsPrograms(t *testing.T) {
	// same shape, via DeleteTeam: programs, roles and enrollments cascade; item_progress survives
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
go test ./internal/org/ -run 'TestEnroll|TestProgram|TestSetPin|TestDelete'
```
Expected: FAIL — undefined methods. Read the real column names in
`internal/db/migrations/00001_init.sql` before writing the progress insert; do not guess them.

- [ ] **Step 3: Implement**

Role defaults on `Enroll` only (never on a later `SetProgram`, or an admin could not remove a
role): leader → `manager` + `approver`, seniors → `scorer`. Store `schedule_name` **or**
`inline_schedule`, never both. Parse `ttl`, `idle_timeout` and `max_extension` with `time.ParseDuration` before storing, so a bad
value is refused at the write and not at lab request time, and map them into
`config.LabDefaults`, whose fields are `yamlx.Duration` (not strings) — build them with
`yamlx.Duration(d)` when assembling the `*config.Program` in Task 1's read.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/org/ -race -v
```
Expected: PASS.

- [ ] **Step 5: Prove the read side survives a missing program**

```go
func TestPlatformIgnoresAProgramWhoseTrainingWasRemoved(t *testing.T) {
	// delete the training row out from under a program (direct SQL, simulating a restored snapshot)
	// Platform() must return the team without that program and must not error
}
```

- [ ] **Step 6: Commit**

```bash
go test ./internal/org/ -race && gofmt -l cmd internal && go vet ./...
git commit -m "feat(org): programs, roles, enrollments and pins in Postgres

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/
```

---

### Task 8: HTTP routes

**Files:**
- Create: `internal/org/api.go`, `internal/org/api_test.go`
- Modify: `internal/httpapi/server.go` (mount the routes), `cmd/crucible-api/main.go` (construct the store)

**Interfaces:**
- Consumes: every method from Tasks 2–7, `auth.User` from the request context, `rbac.Checker`.
- Produces:
  ```go
  func (s *Store) Routes(r chi.Router, deps APIDeps)
  type APIDeps struct {
      Platform func() *config.Platform // the live snapshot, for permission checks
      Refresh  func(context.Context) error
  }
  ```
  Routes: `GET/PUT /api/admin/settings`, `PUT/DELETE /api/admin/schedules/{name}`,
  `PUT /api/admin/quotes`, `GET/POST /api/admin/admins`, `DELETE /api/admin/admins/{email}`,
  `GET/POST /api/admin/trainings`, `DELETE /api/admin/trainings/{id}`,
  `POST/DELETE /api/admin/teams/{id}`.

- [ ] **Step 1: Write the failing permission tests**

```go
func TestAdminRoutesAreAdminOnly(t *testing.T) {
	// for each route: a leader, a senior and a trainee get 403; an admin gets 2xx
}

func TestStateChangingAdminRoutesNeedSameOrigin(t *testing.T) {
	// a PUT with no Origin header is refused, like every other state-changing route
	// (internal/httpapi.TestStateChangingRequestsNeedSameOrigin is the model)
}

func TestSettingsPutRefreshesTheSnapshot(t *testing.T) {
	// after a successful PUT, APIDeps.Refresh has been called once, so the next request sees it
}

func TestConflictIsReportedAsConflict(t *testing.T) {
	// a stale version returns 409 with a message a person can act on, not 500
}
```

- [ ] **Step 2: Run and watch fail**

```bash
go test ./internal/org/ -run 'TestAdminRoutes|TestStateChanging|TestSettingsPut|TestConflict'
```

- [ ] **Step 3: Implement the handlers**

Thin: decode, check `rbac.Checker{P: deps.Platform()}.IsAdmin(u.Email)` (or the team-scoped action
for team routes), call the store, call `deps.Refresh`, return the new version. Map `apperr` kinds to
status codes the way the existing handlers do — reuse, do not reinvent.

- [ ] **Step 4: Run and pass; Step 5: wire into main**

In `cmd/crucible-api/main.go`, construct `store := &org.Store{DB: pool}` and pass it to the router
alongside the existing services. Do not yet remove the platform-repo wiring — Task 9 does that, so
this task stays independently revertible.

- [ ] **Step 6: Commit**

```bash
go test ./internal/org/ ./internal/httpapi/ -race && gofmt -l cmd internal && go vet ./...
git commit -m "feat(org): admin routes for settings, schedules, quotes, admins, trainings, teams

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/org/ internal/httpapi/server.go cmd/crucible-api/main.go
```

---

### Task 9: The switch — the syncer reads config from Postgres

**Files:**
- Modify: `internal/gitsync/syncer.go`, `internal/gitsync/syncer_test.go`
- Modify: `internal/config/config.go` (`Load` keeps settings/admins/trainings/quotes, drops `teams/`)
- Modify: `internal/configapi/configapi.go` (roster, budget, program, pin handlers call `org`)
- Modify: `cmd/crucible-api/main.go`
- Modify: `deploy/compose/docker-compose.yml`

**Interfaces:**
- Consumes: `org.Store.Platform`, every write method.
- Produces: `gitsync.Syncer.Org` — when set, `SyncOnce` takes the platform from it instead of from
  the platform mirror. `config.Load` keeps working for whatever the e2e still needs until M8b.

- [ ] **Step 1: Write the failing test**

```go
func TestSyncTakesThePlatformFromTheStore(t *testing.T) {
	// a Syncer with Org set and no platform repo configured:
	//  - SyncOnce succeeds
	//  - Current().Platform is the store's platform
	//  - content repos still sync from git, from the store's trainings table
}

func TestConfigWriteRefreshesTheSnapshotWithoutGit(t *testing.T) {
	// SetTeam through configapi, then Current().Platform shows the new roster with no git involved
}
```

- [ ] **Step 2: Run and watch fail**

```bash
go test ./internal/gitsync/ -run TestSyncTakesThePlatform
```

- [ ] **Step 3: Implement the swap**

In `SyncOnce`, when `s.Org != nil`, build the next `State` with `s.Org.Platform(ctx)` and leave
`PlatformSHA` empty; the content half of the function is untouched. Replace the four
`configapi.Service` write paths (`SetRoster`, `SetBudget`, `SetProgram`, `SetPin`) with calls to the
corresponding `org.Store` methods, keeping their exported signatures and their HTTP paths. Delete
`configapi.write`'s `gitsync.Writer` use for config; the Writer stays constructed for
`internal/edits`.

- [ ] **Step 4: Run every Go test**

```bash
go test -race ./... 2>&1 | tail -30
```
Expected: PASS. Failures in `internal/configapi` tests that assert on git commits are expected —
rewrite each to assert on rows and the audit entry, keeping the test's name and intent. Do not
delete a test to make the suite green.

- [ ] **Step 5: Make the local stack start with no platform repo**

Add to `deploy/compose/docker-compose.yml` a commented-out `CRUCIBLE_PLATFORM_REPO` and a
`CRUCIBLE_BOOTSTRAP_ADMIN: admin@crucible.local`, then verify by hand:

```bash
./bin/crucible-api  # with DATABASE_URL set and no CRUCIBLE_PLATFORM_REPO
curl -s localhost:8080/healthz
```
Expected: the API starts, logs that no platform repo is configured, seeds the bootstrap admin, and
serves. **The e2e suites still run with the platform repo set** until M8b repoints them.

- [ ] **Step 6: Commit**

```bash
gofmt -l cmd internal && go vet ./... && go test -race ./...
git commit -m "feat(org): the syncer and config writes use Postgres, platform repo optional

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- internal/gitsync/ internal/config/ internal/configapi/ cmd/crucible-api/main.go deploy/compose/docker-compose.yml
```

---

### Task 10: Admin UI

**Files:**
- Create: `web/src/pages/AdminSettings.tsx`, `web/src/pages/AdminSettings.test.tsx`
- Create: `web/src/pages/AdminTrainings.tsx`
- Modify: `web/src/pages/Team.tsx`, `web/src/pages/ProgramSettings.tsx` (version, not `base_sha`)
- Modify: `web/src/App.tsx`, `web/src/components/Nav.tsx`

**Interfaces:**
- Consumes: Task 8's routes.
- Produces: no new API.

- [ ] **Step 1: Write the failing component test**

```tsx
// AdminSettings.test.tsx
it('refuses to save tiers out of order and says why', async () => {
  render(<AdminSettings />)
  await userEvent.clear(screen.getByLabelText(/auto-approve under/i))
  await userEvent.type(screen.getByLabelText(/auto-approve under/i), '50')
  await userEvent.click(screen.getByRole('button', { name: /save/i }))
  expect(await screen.findByRole('status')).toHaveTextContent(/auto-approve.*cannot be more than/i)
  expect(fetchMock).not.toHaveBeenCalledWith(expect.stringContaining('/api/admin/settings'), expect.objectContaining({ method: 'PUT' }))
})
it('warns while only one admin exists', async () => { /* "Add a second admin: losing this account locks the forge." */ })
it('shows the forge-cooled banner while cost tiers are unset', async () => {
  // "No cost tiers are set, so paid labs cannot be requested yet."
})
```

- [ ] **Step 2: Run and watch fail**

```bash
cd web && npm test -- AdminSettings
```

- [ ] **Step 3: Build the pages**

Follow the existing pages' patterns: `useFetch`, theme tokens from `web/src/theme/tokens.css`, no
inline styles that break the CSP, keyboard-reachable controls, `role="status"` for save results.
Copy from the user's side of the screen: "Lab requests above $5 go to the program's approver", not
"tier1_usd".

- [ ] **Step 4: Run the web checks**

```bash
cd web && npm test && npx tsc -b && npm run build && npm run lint
```
Expected: all green, including `contrast.test.ts` for any new colour pair.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(web): admin settings and trainings pages, versioned roster saves

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- web/src
```

---

### Task 11: Docs and the roadmap

**Files:**
- Modify: `CLAUDE.md`, `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`,
  `docs/runbooks/aws.md`, `README.md`, `deploy/helm/crucible/values.yaml`

- [ ] **Step 1: Record what changed**

- CLAUDE.md: the invariant "Git is the source of truth" becomes "Git is the source of truth for
  training content; configuration and org live in Postgres"; name `internal/org`; keep the
  content-edit rules as they are.
- Roadmap: add an M8a row with this plan's path and its proving tests; move the platform-repo
  deviations to a "superseded by M8a" note rather than deleting them.
- `docs/runbooks/aws.md`: the bot credential now needs push on content repos only; the platform
  repo prerequisites paragraph is replaced by "set `CRUCIBLE_BOOTSTRAP_ADMIN` and configure the
  rest in the UI".
- Helm values: `platformRepo` becomes optional with a comment; `bootstrapAdmin` gains the note that
  it is the only thing a fresh install needs.

- [ ] **Step 2: Verify nothing claims the old behaviour**

```bash
grep -rn "platform repo" CLAUDE.md README.md docs/runbooks/ | grep -v superseded
```
Expected: every remaining hit is about content repos or explicitly marked superseded.

- [ ] **Step 3: Commit**

```bash
git commit -m "docs: configuration and org live in Postgres

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>" -- CLAUDE.md README.md docs/ deploy/helm/crucible/values.yaml
```

---

## Done when

- `go test -race ./...`, `gofmt -l cmd internal` (empty), `go vet ./...` all pass.
- `cd web && npm test && npx tsc -b && npm run build && npm run lint` passes.
- `KEYCLOAK_PORT=8082 make local-check` passes — still against the git-seeded platform repo, which
  M8b removes.
- An API started with only `DATABASE_URL` and `CRUCIBLE_BOOTSTRAP_ADMIN` serves, that admin signs
  in, creates a team, registers a training, enrolls a trainee and sets cost tiers, all in the UI,
  with no YAML and no git push anywhere.
- No email address is written to any git repository by Crucible.
