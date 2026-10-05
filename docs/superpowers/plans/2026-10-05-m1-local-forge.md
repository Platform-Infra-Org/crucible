# Crucible M1 "Local Forge" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A runnable Crucible that a trainee can log into with SSO, read material, pass instant-scored quizzes, and complete a KodeKloud-style lab running on their own laptop. The whole flow is proven by one command, `make local-check`.

**Architecture:** One Go binary (`crucible-api`) with internal packages per domain (auth, rbac, gitsync, content, learn, labs, agenthub). It uses Postgres for runtime data and serves the React SPA as static files. Config and content are read from git repos (platform repo + content repos). Local labs run on the trainee's machine through `crucible-agent`, which holds an outbound WebSocket to the API, runs `docker compose`, streams PTYs and runs check/setup scripts. Locally everything runs under Docker Compose with Keycloak as the OIDC provider.

**Tech Stack:** Go 1.26 (chi, pgx/v5, goose, go-oidc/v3, x/oauth2, coder/websocket, creack/pty, yaml.v3, testcontainers-go), Postgres 18, Keycloak 26.4, React 19 + Vite + TypeScript, xterm.js, motion (Framer Motion), react-markdown, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md`
**Roadmap:** `docs/superpowers/plans/2026-10-05-crucible-roadmap.md` (M1 is the first of seven milestones)

## Global Constraints

- Go module path: `crucible`. Go 1.26. Node 24 LTS. Postgres image `postgres:18-alpine`. Keycloak image `quay.io/keycloak/keycloak:26.4`.
- One container image serves the API and the SPA (`CRUCIBLE_WEB_DIR`).
- Emails are lowercased everywhere they are stored or compared.
- Quiz answers, check scripts, setup scripts, and hint text are never sent to the browser before they are revealed (spec §7, §8.4, §8.5).
- Check scripts: exit 0 = pass, combined stdout/stderr = feedback (spec §2). Results from `local` labs are stored with `self_reported = true` (spec §8.2).
- Defaults (spec §4.5, §8.4–8.6): lab TTL 2h, `idle_timeout` 30m, `idle_warning` 5m, check timeout 30s, setup timeout 60s, `hint_cost` 0, script output capped at 64 KiB, setup retried once on failure, "Reset scenario" at most once per 5 minutes, quiz `pass_threshold` 0.8.
- Timer states: normal → cooling at ≤ 15 min → critical at ≤ 5 min; warnings at 15 and 5 min (spec §8.6).
- Themes: `forge` (default), `anvil`, `quench`, `contrast` (spec §12). All motion is off under `prefers-reduced-motion` or the user's "Calm forge" setting.
- Lab UI layout: tasks left (≈40%), tabbed terminals right, resizable splitter (spec §8.3).
- M1 implements only the `local` runtime. `cluster` and `aws` labs are rejected at start with a clear "runtime not available yet" message.
- No competitions or leaderboards anywhere (spec non-goals).

## Review Focus

1. **Agent not connected / disconnects mid-lab.** Starting a local lab without a connected agent must fail fast with "your laptop agent is not connected". A disconnect must make pending calls return `ErrOffline` immediately rather than hang. Pinned by tests in Task 11 (hub) and Task 14 (labs service).
2. **Answer leakage.** The public quiz payload must not contain `answer`, check paths, or rubric. Pinned by `TestPublicQuizHidesAnswers` in Task 9.
3. **Bad content pushed to git.** An invalid commit on a training's branch must not take that training offline. Programs keep the last valid version and the problem is recorded. Pinned by `TestBadHeadKeepsLastGood` in Task 6.
4. **Clock skew and multiple tabs on the lab timer.** A trainee whose laptop clock is 10 minutes off must still see the server's remaining time. Pinned by Vitest `timer.test.ts` in Task 19.
5. **Runaway scripts.** A check script that never exits, or prints megabytes, must time out and be capped instead of hanging the request or exhausting memory. Pinned by `TestRunLimited*` in Task 12.

---

## File Structure

```
go.mod, go.sum, Makefile, Dockerfile, .gitignore
cmd/
  crucible-api/main.go          # wiring: env config, DB, syncer, services, HTTP server
  crucible/main.go              # CLI: `crucible lint <dir>` (M2 adds `crucible aws …`)
  crucible-agent/main.go        # laptop agent
internal/
  apperr/apperr.go              # error kinds shared by services (NotFound, Forbidden, …)
  httpx/httpx.go                # JSON read/write + error→status mapping
  httpapi/server.go             # router: mounts all modules + SPA fallback
  db/db.go, db/migrations/*.sql # pgx pool + goose migrations
  db/dbtest/dbtest.go           # per-test Postgres database via testcontainers
  yamlx/yamlx.go                # strict YAML file reading + Duration type
  config/config.go              # platform repo model + loader/validator
  content/{types,load}.go       # content repo model + loader/validator (also used by lint)
  gitsync/{mirror,syncer}.go    # git mirrors, exports per SHA, in-memory State
  auth/{store,oidc,middleware}.go
  rbac/rbac.go
  learn/{quiz,service,http}.go  # reading, quizzes, progress, gating
  agentproto/proto.go           # agent ↔ API message format
  agenthub/hub.go               # server side of agent connections
  agent/{client,compose,exec}.go# agent side: WebSocket client, docker compose executor
  labs/{model,bundle,local,service,http}.go
web/                            # React SPA (Vite)
  src/{main.tsx,App.tsx,api.ts,types.ts,useFetch.ts}
  src/theme/{tokens.css,app.css,theme.ts}
  src/lib/{timer.ts,timer.test.ts,quotes.ts,alerts.ts}
  src/components/{Loader,Embers,Nav,Markdown,SparkBurst,Terminal,Timer,IdleModal}.tsx
  src/pages/{Hearth,Training,Reading,Quiz,Lab,Connect,Settings}.tsx
examples/
  platform/                     # sample platform repo (team "forge")
  forge-101/                    # sample training: reading, quiz, local break-fix lab
deploy/compose/                 # docker-compose.yml + keycloak realm for local check
scripts/{seed-git.sh,local-check.sh}
e2e/                            # Playwright end-to-end local check
```

---

### Task 1: Project scaffold, error kinds, health endpoint

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `cmd/crucible-api/main.go`
- Create: `internal/apperr/apperr.go`, `internal/httpx/httpx.go`, `internal/httpx/httpx_test.go`
- Create: `internal/httpapi/server.go`, `internal/httpapi/server_test.go`

**Interfaces:**
- Produces: `apperr.NotFound|Forbidden|Locked|Conflict|Unavailable|Invalid` (sentinel errors), `apperr.Wrap(kind error, msg string) error`; `httpx.JSON(w, status, v)`, `httpx.Read(r, v) error`, `httpx.Error(w, err)`; `httpapi.NewRouter(d Deps) chi.Router`.

- [ ] **Step 1: Initialise the module and tooling files**

```bash
cd /Users/adelin/Projects/Crucible
go mod init crucible
go mod edit -go=1.26
go get github.com/go-chi/chi/v5@latest
```

`.gitignore`:
```gitignore
bin/
.local/
web/node_modules/
web/dist/
e2e/node_modules/
e2e/test-results/
e2e/playwright-report/
```

`Makefile`:
```make
.PHONY: test build web local-check
test:
	go test ./...
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -o bin/ ./cmd/...
web:
	cd web && npm ci && npm run build
local-check:
	./scripts/local-check.sh
```

- [ ] **Step 2: Write the failing tests**

`internal/httpx/httpx_test.go`:
```go
package httpx

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func TestErrorMapsKinds(t *testing.T) {
	cases := map[error]int{
		apperr.Wrap(apperr.NotFound, "no lab"):      404,
		apperr.Wrap(apperr.Forbidden, "nope"):       403,
		apperr.Wrap(apperr.Locked, "finish first"):  423,
		apperr.Wrap(apperr.Conflict, "busy"):        409,
		apperr.Wrap(apperr.Unavailable, "offline"):  503,
		apperr.Wrap(apperr.Invalid, "bad json"):     400,
		errors.New("database exploded"):             500,
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		Error(w, err)
		if w.Code != want {
			t.Errorf("%v: got %d want %d", err, w.Code, want)
		}
	}
}

func TestErrorHidesInternalMessages(t *testing.T) {
	w := httptest.NewRecorder()
	Error(w, errors.New("password=hunter2"))
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("internal error leaked: %s", w.Body.String())
	}
}
```

`internal/httpapi/server_test.go`:
```go
package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	w := httptest.NewRecorder()
	NewRouter(Deps{}).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/...`
Expected: FAIL (packages `apperr`, `httpx`, `httpapi` have no non-test files).

- [ ] **Step 4: Implement**

`internal/apperr/apperr.go`:
```go
// Package apperr defines error kinds that services return and HTTP maps to status codes.
package apperr

import (
	"errors"
	"fmt"
)

var (
	NotFound    = errors.New("not found")
	Forbidden   = errors.New("forbidden")
	Locked      = errors.New("locked")
	Conflict    = errors.New("conflict")
	Unavailable = errors.New("unavailable")
	Invalid     = errors.New("invalid")
)

// Wrap attaches a user-facing message to an error kind.
func Wrap(kind error, msg string) error { return fmt.Errorf("%s: %w", msg, kind) }
```

`internal/httpx/httpx.go`:
```go
// Package httpx holds the JSON and error helpers every handler uses.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"crucible/internal/apperr"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Read decodes a JSON body (max 1 MiB) and rejects unknown fields.
func Read(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	return nil
}

func Error(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, apperr.NotFound):
		status = http.StatusNotFound
	case errors.Is(err, apperr.Forbidden):
		status = http.StatusForbidden
	case errors.Is(err, apperr.Locked):
		status = http.StatusLocked
	case errors.Is(err, apperr.Conflict):
		status = http.StatusConflict
	case errors.Is(err, apperr.Unavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, apperr.Invalid):
		status = http.StatusBadRequest
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		slog.Error("request failed", "err", err)
		msg = "internal error"
	} else if i := strings.LastIndex(msg, ": "); i > 0 {
		msg = msg[:i] // drop the trailing kind ("…: not found")
	}
	JSON(w, status, map[string]string{"error": msg})
}
```

`internal/httpapi/server.go`:
```go
// Package httpapi wires every module's routes into one router.
package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Deps grows as modules are added; Task 16 wires the full set.
type Deps struct{}

func NewRouter(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return r
}
```

`cmd/crucible-api/main.go`:
```go
package main

import (
	"log/slog"
	"net/http"
	"os"

	"crucible/internal/httpapi"
)

func main() {
	addr := env("CRUCIBLE_ADDR", ":8080")
	slog.Info("crucible-api listening", "addr", addr)
	if err := http.ListenAndServe(addr, httpapi.NewRouter(httpapi.Deps{})); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/...`
Expected: `ok` for `httpx` and `httpapi`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum Makefile .gitignore cmd internal
git commit -m "feat: scaffold crucible-api with error kinds and health endpoint"
```

---

### Task 2: Database, migrations, test helper

**Files:**
- Create: `internal/db/db.go`, `internal/db/migrations/00001_init.sql`, `internal/db/dbtest/dbtest.go`, `internal/db/db_test.go`

**Interfaces:**
- Produces: `db.Open(ctx, url string) (*pgxpool.Pool, error)` (connects + migrates); `dbtest.New(t *testing.T) *pgxpool.Pool` (fresh migrated database per test). Tables listed in the migration are used by Tasks 7, 10, 14.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/jackc/pgx/v5@latest github.com/pressly/goose/v3@latest \
  github.com/testcontainers/testcontainers-go@latest \
  github.com/testcontainers/testcontainers-go/modules/postgres@latest
```

- [ ] **Step 2: Write the failing test**

`internal/db/db_test.go`:
```go
package db_test

import (
	"context"
	"testing"

	"crucible/internal/db"
	"crucible/internal/db/dbtest"
)

func TestMigrationsCreateTablesAndAreIdempotent(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	for _, table := range []string{"users", "sessions", "agent_tokens", "item_progress", "quiz_attempts",
		"lab_instances", "lab_events", "lab_task_progress", "check_runs", "setup_runs", "hint_reveals"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	if err := db.Migrate(pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/db/...`
Expected: FAIL (`dbtest.New` / `db.Migrate` undefined).

- [ ] **Step 4: Implement**

`internal/db/migrations/00001_init.sql`:
```sql
-- +goose Up
CREATE TABLE users (
  id          BIGSERIAL PRIMARY KEY,
  sub         TEXT NOT NULL UNIQUE,
  email       TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  theme       TEXT NOT NULL DEFAULT '',
  calm_motion BOOLEAN NOT NULL DEFAULT false,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX users_email ON users (email);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,               -- sha256 of the cookie value
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE agent_tokens (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  revoked_at   TIMESTAMPTZ
);

CREATE TABLE item_progress (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  item       TEXT NOT NULL,
  status     TEXT NOT NULL,                  -- in_progress | complete
  score      DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, item)
);

CREATE TABLE quiz_attempts (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  sha        TEXT NOT NULL,
  answers    JSONB NOT NULL,
  score      DOUBLE PRECISION NOT NULL,
  max_score  DOUBLE PRECISION NOT NULL,
  passed     BOOLEAN NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE lab_instances (
  id               TEXT PRIMARY KEY,
  user_id          BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team             TEXT NOT NULL,
  training         TEXT NOT NULL,
  module           TEXT NOT NULL,
  sha              TEXT NOT NULL,
  runtime          TEXT NOT NULL,
  state            TEXT NOT NULL,            -- provisioning | ready | destroying | destroyed | failed
  error            TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL,
  ready_at         TIMESTAMPTZ,
  ends_at          TIMESTAMPTZ,
  limit_reason     TEXT NOT NULL DEFAULT '', -- which limit sets ends_at (ttl, …)
  end_reason       TEXT NOT NULL DEFAULT '', -- why the lab was destroyed
  last_activity_at TIMESTAMPTZ NOT NULL,
  ttl_s            INT NOT NULL,
  idle_timeout_s   INT NOT NULL,
  idle_warning_s   INT NOT NULL,
  max_extension_s  INT NOT NULL,
  extended         BOOLEAN NOT NULL DEFAULT false,
  destroyed_at     TIMESTAMPTZ
);
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('provisioning', 'ready', 'destroying');

CREATE TABLE lab_events (
  id     BIGSERIAL PRIMARY KEY,
  lab_id TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind   TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);

-- Task results survive lab re-creation (spec §8.6: "passed tasks stay passed").
CREATE TABLE lab_task_progress (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  task       TEXT NOT NULL,
  status     TEXT NOT NULL,                  -- passed | skipped
  points     DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, task)
);

CREATE TABLE check_runs (
  id            BIGSERIAL PRIMARY KEY,
  lab_id        TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  task          TEXT NOT NULL,
  exit_code     INT NOT NULL,
  output        TEXT NOT NULL,
  answer        TEXT NOT NULL DEFAULT '',
  self_reported BOOLEAN NOT NULL,
  at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE setup_runs (
  id          BIGSERIAL PRIMARY KEY,
  lab_id      TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  task        TEXT NOT NULL DEFAULT '',      -- '' = lab-level setup
  attempt     INT NOT NULL,
  exit_code   INT NOT NULL,
  output      TEXT NOT NULL,
  duration_ms INT NOT NULL,
  at          TIMESTAMPTZ NOT NULL
);

-- Keyed by user/module/task so a fresh lab never charges for the same hint twice.
CREATE TABLE hint_reveals (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  task       TEXT NOT NULL,
  hint_index INT NOT NULL,
  cost       DOUBLE PRECISION NOT NULL,
  lab_id     TEXT NOT NULL,
  at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, task, hint_index)
);

-- +goose Down
DROP TABLE hint_reveals, setup_runs, check_runs, lab_task_progress, lab_events,
  lab_instances, quiz_attempts, item_progress, agent_tokens, sessions, users;
```

`internal/db/db.go`:
```go
// Package db opens the Postgres pool and applies embedded migrations.
package db

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := Migrate(pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Migrate(pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	goose.SetLogger(goose.NopLogger())
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
```

`internal/db/dbtest/dbtest.go`:
```go
// Package dbtest gives each test its own migrated Postgres database inside one shared container.
package dbtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"crucible/internal/db"
)

var (
	once     sync.Once
	baseURL  string
	startErr error
	counter  atomic.Int64
)

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	once.Do(func() {
		c, err := postgres.Run(ctx, "postgres:18-alpine",
			postgres.WithDatabase("crucible"), postgres.WithUsername("crucible"), postgres.WithPassword("crucible"),
			postgres.BasicWaitStrategies())
		if err != nil {
			startErr = err
			return
		}
		baseURL, startErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if startErr != nil {
		t.Fatalf("start postgres (is Docker running?): %v", startErr)
	}
	name := fmt.Sprintf("t_%d_%d", counter.Add(1), os.Getpid())
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	u, _ := url.Parse(baseURL)
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go mod tidy && go test ./internal/db/...`
Expected: `ok crucible/internal/db` (needs Docker running; first run pulls `postgres:18-alpine`).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/db
git commit -m "feat: postgres pool, initial schema, per-test database helper"
```

---

### Task 3: Platform config (yamlx + config) and the sample platform repo

**Files:**
- Create: `internal/yamlx/yamlx.go`, `internal/yamlx/yamlx_test.go`
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `examples/platform/{platform.yaml,admins.yaml,trainings.yaml,quotes.yaml}`, `examples/platform/teams/forge/team.yaml`, `examples/platform/teams/forge/programs/forge-101.yaml`

**Interfaces:**
- Produces:
  - `yamlx.Duration` (`D() time.Duration`), `yamlx.ReadFile(path string, out any, required bool) error` (strict: unknown keys are errors), `yamlx.ReadLoose(path string, out any) error`.
  - `config.Load(dir string) (*config.Platform, error)`; types `Platform{Settings, Admins []string, Trainings map[string]TrainingRef, Teams map[string]*Team}`, `Settings{DefaultTheme string, Quotes []string}`, `TrainingRef{Repo, Branch string}`, `Team{ID, Name, Leader string; Seniors, Members, Trainees []string; Mentors map[string]string; Programs map[string]*Program}`, `(*Team).RoleOf(email) string`, `Program{Training, PinnedRef string; Roles Roles; Enrolled []string; LabDefaults LabDefaults}`, `Roles{Manager, Scorers, Approvers []string}`, `LabDefaults{TTL, IdleTimeout, MaxExtension yamlx.Duration}`, `config.Themes []string`.

- [ ] **Step 1: Add dependency**

```bash
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 2: Create the sample platform repo (it is also the test fixture)**

`examples/platform/platform.yaml`:
```yaml
default_theme: forge
```
`examples/platform/admins.yaml`:
```yaml
admins: [admin@crucible.local]
```
`examples/platform/trainings.yaml`:
```yaml
trainings:
  forge-101:
    repo: file:///git/forge-101.git   # local check mounts seeded bare repos at /git
    branch: main
```
`examples/platform/quotes.yaml`:
```yaml
quotes:
  - "Steel is forged in fire."
  - "Pressure makes diamonds; heat makes blades."
  - "Every master was once a lump of ore."
```
`examples/platform/teams/forge/team.yaml`:
```yaml
name: The Forge
leader: leader@crucible.local
seniors: [senior@crucible.local]
members: []
trainees: [trainee@crucible.local]
mentors:
  trainee@crucible.local: senior@crucible.local
```
`examples/platform/teams/forge/programs/forge-101.yaml`:
```yaml
training: forge-101
enrolled: [trainee@crucible.local]
lab_defaults: { ttl: 2h, idle_timeout: 30m, max_extension: 30m }
```

- [ ] **Step 3: Write the failing tests**

`internal/yamlx/yamlx_test.go`:
```go
package yamlx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "f.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDuration(t *testing.T) {
	var v struct{ TTL Duration `yaml:"ttl"` }
	if err := ReadFile(write(t, "ttl: 90m\n"), &v, true); err != nil {
		t.Fatal(err)
	}
	if v.TTL.D() != 90*time.Minute {
		t.Fatalf("got %v", v.TTL.D())
	}
	if err := ReadFile(write(t, "ttl: soon\n"), &v, true); err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestStrictAndOptional(t *testing.T) {
	var v struct{ A string `yaml:"a"` }
	err := ReadFile(write(t, "a: x\nb: y\n"), &v, true)
	if err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	if err := ReadFile(filepath.Join(t.TempDir(), "missing.yaml"), &v, false); err != nil {
		t.Fatalf("optional missing file: %v", err)
	}
	if err := ReadFile(filepath.Join(t.TempDir(), "missing.yaml"), &v, true); err == nil {
		t.Fatal("required missing file must error")
	}
}
```

`internal/config/config_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExamplePlatform(t *testing.T) {
	p, err := Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	team := p.Teams["forge"]
	if team == nil || team.Name != "The Forge" {
		t.Fatalf("team not loaded: %+v", team)
	}
	if got := team.RoleOf("TRAINEE@crucible.local"); got != "trainee" {
		t.Fatalf("RoleOf = %q", got)
	}
	prog := team.Programs["forge-101"]
	if prog == nil {
		t.Fatal("program missing")
	}
	// Spec §5.2 defaults: leader → manager + approver, seniors → scorers.
	if prog.Roles.Manager[0] != "leader@crucible.local" || prog.Roles.Approvers[0] != "leader@crucible.local" {
		t.Fatalf("role defaults: %+v", prog.Roles)
	}
	if prog.Roles.Scorers[0] != "senior@crucible.local" {
		t.Fatalf("scorer default: %+v", prog.Roles)
	}
	if prog.LabDefaults.TTL.D() != 2*time.Hour {
		t.Fatalf("ttl %v", prog.LabDefaults.TTL.D())
	}
	if p.Settings.DefaultTheme != "forge" || len(p.Settings.Quotes) != 3 {
		t.Fatalf("settings %+v", p.Settings)
	}
}

func copyTree(t *testing.T, src string) string {
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"duplicate role": {"teams/forge/team.yaml",
			"name: F\nleader: a@x\nseniors: [a@x]\ntrainees: []\n", "more than one team role"},
		"unknown training": {"teams/forge/programs/forge-101.yaml",
			"training: forge-999\n", "must match the file name"},
		"enrolled outsider": {"teams/forge/programs/forge-101.yaml",
			"training: forge-101\nenrolled: [stranger@x]\n", "not a member of team"},
		"bad theme": {"platform.yaml", "default_theme: neon\n", "default_theme"},
		"unknown key": {"platform.yaml", "default_theme: forge\ncolour: red\n", "colour"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyTree(t, "../../examples/platform")
			if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/yamlx/... ./internal/config/...`
Expected: FAIL (undefined `ReadFile`, `Duration`, `Load`).

- [ ] **Step 5: Implement**

`internal/yamlx/yamlx.go`:
```go
// Package yamlx reads YAML files strictly and adds a human-friendly Duration type.
package yamlx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration accepts Go duration strings such as "90m" or "2h".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// ReadFile decodes path into out, rejecting unknown keys. A missing optional file is not an error.
func ReadFile(path string, out any, required bool) error {
	return read(path, out, required, true)
}

// ReadLoose decodes path into out, ignoring unknown keys (used for docker compose files).
func ReadLoose(path string, out any) error { return read(path, out, true, false) }

func read(path string, out any, required, strict bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: file not found", filepath.Base(path))
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(strict)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}
```

`internal/config/config.go`:
```go
// Package config loads the platform repo: teams, roles, programs, trainings registry.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"crucible/internal/yamlx"
)

var Themes = []string{"forge", "anvil", "quench", "contrast"}

type Platform struct {
	Settings  Settings
	Admins    []string
	Trainings map[string]TrainingRef
	Teams     map[string]*Team
}

type Settings struct {
	DefaultTheme string   `yaml:"default_theme"`
	Quotes       []string `yaml:"-"`
}

type TrainingRef struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
}

type Team struct {
	ID       string              `yaml:"-"`
	Name     string              `yaml:"name"`
	Leader   string              `yaml:"leader"`
	Seniors  []string            `yaml:"seniors"`
	Members  []string            `yaml:"members"`
	Trainees []string            `yaml:"trainees"`
	Mentors  map[string]string   `yaml:"mentors"` // trainee email → mentor email
	Programs map[string]*Program `yaml:"-"`       // by training id
}

type Program struct {
	Training    string      `yaml:"training"`
	PinnedRef   string      `yaml:"pinned_ref"`
	Roles       Roles       `yaml:"roles"`
	Enrolled    []string    `yaml:"enrolled"`
	LabDefaults LabDefaults `yaml:"lab_defaults"`
}

type Roles struct {
	Manager   []string `yaml:"manager"`
	Scorers   []string `yaml:"scorers"`
	Approvers []string `yaml:"approvers"`
}

type LabDefaults struct {
	TTL          yamlx.Duration `yaml:"ttl"`
	IdleTimeout  yamlx.Duration `yaml:"idle_timeout"`
	MaxExtension yamlx.Duration `yaml:"max_extension"`
}

// RoleOf returns leader, senior, member, trainee or "" for an email.
func (t *Team) RoleOf(email string) string {
	email = strings.ToLower(email)
	switch {
	case email == t.Leader:
		return "leader"
	case slices.Contains(t.Seniors, email):
		return "senior"
	case slices.Contains(t.Members, email):
		return "member"
	case slices.Contains(t.Trainees, email):
		return "trainee"
	}
	return ""
}

func Load(dir string) (*Platform, error) {
	p := &Platform{Trainings: map[string]TrainingRef{}, Teams: map[string]*Team{}}
	var errs []error
	if err := yamlx.ReadFile(filepath.Join(dir, "platform.yaml"), &p.Settings, true); err != nil {
		errs = append(errs, err)
	}
	if p.Settings.DefaultTheme == "" {
		p.Settings.DefaultTheme = "forge"
	}
	if !slices.Contains(Themes, p.Settings.DefaultTheme) {
		errs = append(errs, fmt.Errorf("platform.yaml: default_theme must be one of %v", Themes))
	}

	var admins struct {
		Admins []string `yaml:"admins"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "admins.yaml"), &admins, false); err != nil {
		errs = append(errs, err)
	}
	p.Admins = lower(admins.Admins)

	var reg struct {
		Trainings map[string]TrainingRef `yaml:"trainings"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "trainings.yaml"), &reg, false); err != nil {
		errs = append(errs, err)
	}
	for id, ref := range reg.Trainings {
		if ref.Repo == "" {
			errs = append(errs, fmt.Errorf("trainings.yaml: %s has no repo", id))
			continue
		}
		if ref.Branch == "" {
			ref.Branch = "main"
		}
		p.Trainings[id] = ref
	}

	var quotes struct {
		Quotes []string `yaml:"quotes"`
	}
	if err := yamlx.ReadFile(filepath.Join(dir, "quotes.yaml"), &quotes, false); err != nil {
		errs = append(errs, err)
	}
	p.Settings.Quotes = quotes.Quotes

	entries, _ := os.ReadDir(filepath.Join(dir, "teams")) // no teams yet is fine
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, terrs := loadTeam(filepath.Join(dir, "teams", e.Name()), e.Name(), p.Trainings)
		errs = append(errs, terrs...)
		if t != nil {
			p.Teams[t.ID] = t
		}
	}
	return p, errors.Join(errs...)
}

func loadTeam(dir, id string, trainings map[string]TrainingRef) (*Team, []error) {
	t := &Team{ID: id, Programs: map[string]*Program{}}
	if err := yamlx.ReadFile(filepath.Join(dir, "team.yaml"), t, true); err != nil {
		return nil, []error{fmt.Errorf("teams/%s: %w", id, err)}
	}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("teams/%s: "+format, append([]any{id}, a...)...)) }

	t.Leader = strings.ToLower(t.Leader)
	t.Seniors, t.Members, t.Trainees = lower(t.Seniors), lower(t.Members), lower(t.Trainees)
	mentors := map[string]string{}
	for trainee, mentor := range t.Mentors {
		mentors[strings.ToLower(trainee)] = strings.ToLower(mentor)
	}
	t.Mentors = mentors

	if t.Leader == "" {
		bad("team has no leader")
	}
	seen := map[string]bool{}
	for _, e := range slices.Concat([]string{t.Leader}, t.Seniors, t.Members, t.Trainees) {
		if seen[e] {
			bad("%s has more than one team role", e)
		}
		seen[e] = true
	}
	for trainee, mentor := range t.Mentors {
		if t.RoleOf(trainee) != "trainee" {
			bad("mentor pairing: %s is not a trainee", trainee)
		}
		if r := t.RoleOf(mentor); r == "" || r == "trainee" {
			bad("mentor pairing: %s must be a non-trainee team member", mentor)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "programs", "*.yaml"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		pr := &Program{}
		if err := yamlx.ReadFile(f, pr, true); err != nil {
			bad("%v", err)
			continue
		}
		if pr.Training == "" {
			pr.Training = name
		}
		if pr.Training != name {
			bad("programs/%s.yaml: training %q must match the file name", name, pr.Training)
			continue
		}
		if _, ok := trainings[pr.Training]; !ok {
			bad("programs/%s.yaml: training %q is not in trainings.yaml", name, pr.Training)
			continue
		}
		pr.Enrolled = lower(pr.Enrolled)
		pr.Roles.Manager, pr.Roles.Scorers, pr.Roles.Approvers = lower(pr.Roles.Manager), lower(pr.Roles.Scorers), lower(pr.Roles.Approvers)
		if len(pr.Roles.Manager) == 0 {
			pr.Roles.Manager = []string{t.Leader}
		}
		if len(pr.Roles.Approvers) == 0 {
			pr.Roles.Approvers = []string{t.Leader}
		}
		if len(pr.Roles.Scorers) == 0 {
			pr.Roles.Scorers = slices.Clone(t.Seniors)
		}
		for _, e := range pr.Enrolled {
			if t.RoleOf(e) == "" {
				bad("programs/%s.yaml: %s is not a member of team %s", name, e, id)
			}
		}
		t.Programs[pr.Training] = pr
	}
	return t, errs
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/yamlx/... ./internal/config/...`
Expected: `ok` for both.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/yamlx internal/config examples/platform
git commit -m "feat: platform repo config loader with role defaults and sample platform"
```

---

### Task 4: Content model and loader/validator

**Files:**
- Create: `internal/content/types.go`, `internal/content/load.go`, `internal/content/load_test.go`

**Interfaces:**
- Consumes: `yamlx.ReadFile`, `yamlx.ReadLoose`, `yamlx.Duration`.
- Produces:
  - `content.Load(dir string) (*Training, []Problem)`. Returns a nil training when there are problems.
  - Types:
    - `Problem{File, Msg string}` (`String()`)
    - `Training{ID, Title, Description string; Maintainers []string; Progression string; Modules []*Module; Dir string}` with `(*Training).Module(id) *Module`
    - `Module{ID, Title string; Items []Item; Quiz *Quiz; Lab *Lab; Dir string}`
    - `Item{Kind, ID, Title, Path string}`
    - `Quiz{PassThreshold float64; Questions []*Question}` with `(*Quiz).Question(id)`
    - `Question{ID, Type, Prompt string; Options []string; Pairs [][]string; Answer yaml.Node; CaseSensitive bool; Points float64; Check, RunIn, Rubric string; Script *Script}`
    - `Lab{ID, Runtime string; TTL, IdleTimeout, IdleWarning yamlx.Duration; TaskOrder string; HintCost float64; Compose string; Terminals []Terminal; Setup *Script; Tasks []*Task; Dir string}` with `(*Lab).Task(id)`
    - `Terminal{Name, Service string}` (JSON tags `name`, `service`)
    - `Script{Script, RunIn string; Timeout yamlx.Duration}`
    - `Task{ID, Instructions string; Check, Setup *Script; Quiz string; Points float64; HumanReview bool; Hints []*Hint}`
    - `Hint{Text, File string; Cost *float64}` with `(*Hint).EffectiveCost(lab *Lab) float64`
  - `content.IsHuman(questionType string) bool`.
- Rules (from spec §4.2–4.5, §8.4, §8.5):
  - Terminal-question `check` paths resolve relative to the module's `lab/` directory.
  - Terminal questions default `run_in` to the lab's first terminal service.
  - `aws` labs have a single service named `workspace`.

- [ ] **Step 1: Write the failing tests**

`internal/content/load_test.go`:
```go
package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// base is a minimal valid training. Tests override single files to break it.
var base = map[string]string{
	"training.yaml": "id: t1\ntitle: T1\nmodules: [m1]\n",
	"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n  - lab: lab\n",
	"modules/m1/reading/intro.md": "# Intro to the Forge\nHello.\n",
	"modules/m1/quiz.yaml": `questions:
  - {id: q1, type: single, prompt: P, options: [a, b], answer: 1}
  - {id: q2, type: terminal, prompt: Port?, check: checks/q2.sh}
`,
	"modules/m1/lab/lab.yaml": `id: l1
runtime: local
terminals: [{name: shell, service: box}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: box}
    points: 2
    hints:
      - text: nudge
      - file: hints/sol.md
        cost: 1
  - id: t2
    instructions: tasks/t2.md
    setup: {script: setup/t2.sh, run_in: box}
    quiz: q2
`,
	"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n",
	"modules/m1/lab/tasks/t1.md":   "Do t1",
	"modules/m1/lab/tasks/t2.md":   "Do t2",
	"modules/m1/lab/checks/t1.sh":  "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/checks/q2.sh":  "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/setup/t2.sh":   "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/hints/sol.md":  "the answer",
}

func tree(t *testing.T, override map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for k, v := range base {
		files[k] = v
	}
	for k, v := range override {
		files[k] = v
	}
	for name, body := range files {
		if body == "<delete>" {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") && body != "<noexec>" {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	tr, probs := Load(tree(t, nil))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if tr.Progression != "linear" {
		t.Errorf("progression default = %q", tr.Progression)
	}
	m := tr.Module("m1")
	if m.Items[0].Title != "Intro to the Forge" || m.Items[0].ID != "intro" {
		t.Errorf("reading item = %+v", m.Items[0])
	}
	if m.Quiz.PassThreshold != 0.8 || m.Quiz.Question("q1").Points != 1 {
		t.Errorf("quiz defaults: %+v", m.Quiz)
	}
	lab := m.Lab
	if lab.IdleWarning.D() != 5*time.Minute || lab.TaskOrder != "linear" || lab.Compose != "compose.yaml" {
		t.Errorf("lab defaults: %+v", lab)
	}
	t1 := lab.Task("t1")
	if t1.Check.Timeout.D() != 30*time.Second {
		t.Errorf("check timeout default = %v", t1.Check.Timeout.D())
	}
	if t1.Hints[0].EffectiveCost(lab) != 0 || t1.Hints[1].EffectiveCost(lab) != 1 {
		t.Errorf("hint costs wrong")
	}
	t2 := lab.Task("t2")
	if t2.Setup.Timeout.D() != 60*time.Second || t2.Points != 1 {
		t.Errorf("t2 = %+v", t2)
	}
	q2 := m.Quiz.Question("q2")
	if q2.Script == nil || q2.Script.RunIn != "box" {
		t.Errorf("terminal question script not resolved: %+v", q2.Script)
	}
}

func TestLoadProblems(t *testing.T) {
	cases := map[string]struct {
		override map[string]string
		want     string
	}{
		"missing title":     {map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"}, "title is required"},
		"bad runtime":       {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "local", "moon", 1)}, "runtime must be"},
		"unknown service":   {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "service: box", "service: nope", 1)}, `service "nope"`},
		"not executable":    {map[string]string{"modules/m1/lab/checks/t1.sh": "<noexec>"}, "must be executable"},
		"idle warning":      {map[string]string{"modules/m1/lab/lab.yaml": "idle_timeout: 5m\nidle_warning: 5m\n" + base["modules/m1/lab/lab.yaml"]}, "idle_warning must be shorter"},
		"answer range":      {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b], answer: 7}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer must be an option index"},
		"missing answer":    {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b]}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer is required"},
		"quiz not terminal": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "quiz: q2", "quiz: q1", 1)}, "terminal question"},
		"hint too costly":   {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "cost: 1", "cost: 5", 1)}, "hint 2 cost"},
		"path escape":       {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: ../../../etc/passwd\n"}, "must stay inside"},
		"bad regex":         {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: regex, prompt: P, answer: '('}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "regex"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr, probs := Load(tree(t, c.override))
			if tr != nil {
				t.Fatal("expected nil training when problems exist")
			}
			var all []string
			for _, p := range probs {
				all = append(all, p.String())
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want problem containing %q, got:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/content/...`
Expected: FAIL (undefined `Load`).

- [ ] **Step 3: Implement the types**

`internal/content/types.go`:
```go
// Package content loads and validates a training content repo (spec §4.2–4.5).
package content

import (
	"gopkg.in/yaml.v3"

	"crucible/internal/yamlx"
)

type Problem struct{ File, Msg string }

func (p Problem) String() string { return p.File + ": " + p.Msg }

type Training struct {
	ID          string    `yaml:"id"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Maintainers []string  `yaml:"maintainers"`
	Progression string    `yaml:"progression"` // linear | free
	ModuleIDs   []string  `yaml:"modules"`
	Modules     []*Module `yaml:"-"`
	Dir         string    `yaml:"-"`
}

type Module struct {
	ID       string              `yaml:"-"`
	Title    string              `yaml:"title"`
	RawItems []map[string]string `yaml:"items"`
	Items    []Item              `yaml:"-"`
	Quiz     *Quiz               `yaml:"-"`
	Lab      *Lab                `yaml:"-"`
	Dir      string              `yaml:"-"`
}

type Item struct {
	Kind  string `json:"kind"` // reading | quiz | lab
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"-"`
}

type Quiz struct {
	PassThreshold float64     `yaml:"pass_threshold"`
	Questions     []*Question `yaml:"questions"`
}

type Question struct {
	ID            string     `yaml:"id"`
	Type          string     `yaml:"type"` // single multi exact regex order match terminal | text upload signoff
	Prompt        string     `yaml:"prompt"`
	Options       []string   `yaml:"options"`
	Pairs         [][]string `yaml:"pairs"`
	Answer        yaml.Node  `yaml:"answer"`
	CaseSensitive bool       `yaml:"case_sensitive"`
	Points        float64    `yaml:"points"`
	Check         string     `yaml:"check"`  // terminal: script path relative to the module's lab dir
	RunIn         string     `yaml:"run_in"` // terminal: service; defaults to the first terminal's service
	Rubric        string     `yaml:"rubric"`
	Script        *Script    `yaml:"-"` // resolved from Check/RunIn by the lab loader
}

type Lab struct {
	ID          string         `yaml:"id"`
	Runtime     string         `yaml:"runtime"` // local | cluster | aws
	TTL         yamlx.Duration `yaml:"ttl"`     // 0 = use the program default
	IdleTimeout yamlx.Duration `yaml:"idle_timeout"`
	IdleWarning yamlx.Duration `yaml:"idle_warning"`
	TaskOrder   string         `yaml:"task_order"`
	HintCost    float64        `yaml:"hint_cost"`
	Compose     string         `yaml:"compose"`
	Terminals   []Terminal     `yaml:"terminals"`
	Setup       *Script        `yaml:"setup"`
	Tasks       []*Task        `yaml:"tasks"`
	AWS         *AWSConfig     `yaml:"aws"`
	Dir         string         `yaml:"-"`
}

type AWSConfig struct {
	Region       string  `yaml:"region"`
	MaxHourlyUSD float64 `yaml:"max_hourly_usd"`
}

type Terminal struct {
	Name    string `yaml:"name" json:"name"`
	Service string `yaml:"service" json:"service"`
}

type Script struct {
	Script  string         `yaml:"script"`
	RunIn   string         `yaml:"run_in"`
	Timeout yamlx.Duration `yaml:"timeout"`
}

type Task struct {
	ID           string  `yaml:"id"`
	Instructions string  `yaml:"instructions"`
	Check        *Script `yaml:"check"`
	Setup        *Script `yaml:"setup"`
	Quiz         string  `yaml:"quiz"`
	Points       float64 `yaml:"points"`
	HumanReview  bool    `yaml:"human_review"`
	Hints        []*Hint `yaml:"hints"`
}

type Hint struct {
	Text string   `yaml:"text"`
	File string   `yaml:"file"`
	Cost *float64 `yaml:"cost"`
}

func (h *Hint) EffectiveCost(lab *Lab) float64 {
	if h.Cost != nil {
		return *h.Cost
	}
	return lab.HintCost
}

func (t *Training) Module(id string) *Module {
	for _, m := range t.Modules {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (q *Quiz) Question(id string) *Question {
	for _, x := range q.Questions {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func (l *Lab) Task(id string) *Task {
	for _, t := range l.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func IsHuman(questionType string) bool {
	return questionType == "text" || questionType == "upload" || questionType == "signoff"
}
```

- [ ] **Step 4: Implement the loader**

`internal/content/load.go`:
```go
package content

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"crucible/internal/yamlx"
)

type loader struct {
	root  string
	probs []Problem
}

func (l *loader) add(path, format string, a ...any) {
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		rel = path
	}
	l.probs = append(l.probs, Problem{File: filepath.ToSlash(rel), Msg: fmt.Sprintf(format, a...)})
}

func (l *loader) read(path string, out any) bool {
	if err := yamlx.ReadFile(path, out, true); err != nil {
		l.add(path, "%v", err)
		return false
	}
	return true
}

// file checks that rel stays inside base and exists; it returns the joined path.
func (l *loader) file(base, rel, where string) (string, bool) {
	if !filepath.IsLocal(rel) {
		l.add(where, "path %q must stay inside %s", rel, filepath.Base(base))
		return "", false
	}
	p := filepath.Join(base, rel)
	if _, err := os.Stat(p); err != nil {
		l.add(p, "file not found")
		return "", false
	}
	return p, true
}

// Load parses and validates a content repo. It returns nil and the problems if anything is wrong.
func Load(dir string) (*Training, []Problem) {
	l := &loader{root: dir}
	tf := filepath.Join(dir, "training.yaml")
	t := &Training{Dir: dir}
	if !l.read(tf, t) {
		return nil, l.probs
	}
	if t.ID == "" {
		l.add(tf, "id is required")
	}
	if t.Title == "" {
		l.add(tf, "title is required")
	}
	if t.Progression == "" {
		t.Progression = "linear"
	}
	if t.Progression != "linear" && t.Progression != "free" {
		l.add(tf, "progression must be linear or free")
	}
	if len(t.ModuleIDs) == 0 {
		l.add(tf, "at least one module is required")
	}
	for i, m := range t.Maintainers {
		t.Maintainers[i] = strings.ToLower(m)
	}
	for _, id := range t.ModuleIDs {
		if !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
			l.add(tf, "invalid module id %q", id)
			continue
		}
		if m := l.module(filepath.Join(dir, "modules", id), id); m != nil {
			t.Modules = append(t.Modules, m)
		}
	}
	if len(l.probs) > 0 {
		return nil, l.probs
	}
	return t, nil
}

func (l *loader) module(dir, id string) *Module {
	mf := filepath.Join(dir, "module.yaml")
	m := &Module{ID: id, Dir: dir}
	if !l.read(mf, m) {
		return nil
	}
	if m.Title == "" {
		l.add(mf, "title is required")
	}
	if _, err := os.Stat(filepath.Join(dir, "quiz.yaml")); err == nil {
		m.Quiz = l.quiz(filepath.Join(dir, "quiz.yaml"))
	}
	seen := map[string]bool{}
	for _, raw := range m.RawItems {
		if len(raw) != 1 {
			l.add(mf, "each item must have exactly one of reading, quiz, lab")
			continue
		}
		for kind, rel := range raw {
			switch kind {
			case "reading":
				p, ok := l.file(dir, rel, mf)
				if !ok {
					continue
				}
				b, _ := os.ReadFile(p)
				itemID := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
				if seen[itemID] {
					l.add(mf, "duplicate reading id %q", itemID)
				}
				seen[itemID] = true
				m.Items = append(m.Items, Item{Kind: "reading", ID: itemID, Title: mdTitle(b, itemID), Path: p})
			case "quiz":
				if filepath.Clean(rel) != "quiz.yaml" || m.Quiz == nil {
					l.add(mf, "quiz item must point to an existing quiz.yaml")
					continue
				}
				if !hasStandaloneQuestions(m.Quiz) {
					l.add(mf, "quiz item needs at least one non-terminal question (terminal questions live in labs)")
				}
				m.Items = append(m.Items, Item{Kind: "quiz", ID: "quiz", Title: "Quiz", Path: filepath.Join(dir, "quiz.yaml")})
			case "lab":
				p, ok := l.file(dir, rel, mf)
				if !ok {
					continue
				}
				if m.Lab != nil {
					l.add(mf, "only one lab per module")
					continue
				}
				if m.Lab = l.lab(p, m.Quiz); m.Lab != nil {
					m.Items = append(m.Items, Item{Kind: "lab", ID: "lab", Title: "Lab", Path: p})
				}
			default:
				l.add(mf, "unknown item kind %q", kind)
			}
		}
	}
	if len(m.Items) == 0 {
		l.add(mf, "module has no items")
	}
	return m
}

func hasStandaloneQuestions(q *Quiz) bool {
	for _, x := range q.Questions {
		if x.Type != "terminal" {
			return true
		}
	}
	return false
}

func mdTitle(b []byte, fallback string) string {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if s := strings.TrimSpace(string(line)); strings.HasPrefix(s, "# ") {
			return strings.TrimSpace(s[2:])
		}
	}
	return fallback
}

func (l *loader) quiz(path string) *Quiz {
	q := &Quiz{}
	if !l.read(path, q) {
		return nil
	}
	if q.PassThreshold == 0 {
		q.PassThreshold = 0.8
	}
	if q.PassThreshold < 0 || q.PassThreshold > 1 {
		l.add(path, "pass_threshold must be between 0 and 1")
	}
	ids := map[string]bool{}
	for i, x := range q.Questions {
		where := fmt.Sprintf("question %d (%s)", i+1, x.ID)
		bad := func(format string, a ...any) { l.add(path, where+": "+format, a...) }
		if x.ID == "" {
			bad("id is required")
		}
		if ids[x.ID] {
			bad("duplicate id")
		}
		ids[x.ID] = true
		if x.Prompt == "" {
			bad("prompt is required")
		}
		if x.Points == 0 {
			x.Points = 1
		}
		if x.Points < 0 {
			bad("points must be positive")
		}
		needAnswer := map[string]bool{"single": true, "multi": true, "exact": true, "regex": true}
		if needAnswer[x.Type] && x.Answer.Kind == 0 {
			bad("answer is required")
			continue
		}
		switch x.Type {
		case "single":
			var a int
			if len(x.Options) < 2 {
				bad("needs at least 2 options")
			} else if err := x.Answer.Decode(&a); err != nil || a < 0 || a >= len(x.Options) {
				bad("answer must be an option index (0-%d)", len(x.Options)-1)
			}
		case "multi":
			var a []int
			if len(x.Options) < 2 {
				bad("needs at least 2 options")
			} else if err := x.Answer.Decode(&a); err != nil || len(a) == 0 {
				bad("answer must be a list of option indexes")
			} else {
				for _, v := range a {
					if v < 0 || v >= len(x.Options) {
						bad("answer must be an option index (0-%d)", len(x.Options)-1)
					}
				}
			}
		case "exact":
			var s string
			if err := x.Answer.Decode(&s); err != nil || strings.TrimSpace(s) == "" {
				bad("answer must be a non-empty string")
			}
		case "regex":
			var s string
			if err := x.Answer.Decode(&s); err != nil {
				bad("answer must be a regex string")
			} else if _, err := regexp.Compile("^(?:" + s + ")$"); err != nil {
				bad("invalid regex: %v", err)
			}
		case "order":
			if len(x.Options) < 2 {
				bad("needs at least 2 options, listed in the correct order")
			}
		case "match":
			if len(x.Pairs) < 2 {
				bad("needs at least 2 pairs")
			}
			for _, p := range x.Pairs {
				if len(p) != 2 {
					bad("each pair must have exactly 2 entries")
				}
			}
		case "terminal":
			if x.Check == "" {
				bad("terminal questions need a check script")
			}
		case "text", "upload", "signoff":
		default:
			bad("unknown type %q", x.Type)
		}
	}
	return q
}

func (l *loader) lab(dir string, quiz *Quiz) *Lab {
	lf := filepath.Join(dir, "lab.yaml")
	lab := &Lab{Dir: dir}
	if !l.read(lf, lab) {
		return nil
	}
	if lab.ID == "" {
		l.add(lf, "id is required")
	}
	if lab.IdleTimeout > 0 && lab.IdleWarning > 0 && lab.IdleWarning >= lab.IdleTimeout {
		l.add(lf, "idle_warning must be shorter than idle_timeout")
	}
	if lab.IdleWarning == 0 {
		lab.IdleWarning = yamlx.Duration(5 * time.Minute)
	}
	if lab.TaskOrder == "" {
		lab.TaskOrder = "linear"
	}
	if lab.TaskOrder != "linear" && lab.TaskOrder != "free" {
		l.add(lf, "task_order must be linear or free")
	}
	if lab.HintCost < 0 {
		l.add(lf, "hint_cost must not be negative")
	}
	if lab.Compose == "" {
		lab.Compose = "compose.yaml"
	}

	services := map[string]bool{}
	switch lab.Runtime {
	case "local", "cluster":
		if p, ok := l.file(dir, lab.Compose, lf); ok {
			var c struct {
				Services map[string]yaml.Node `yaml:"services"`
			}
			if err := yamlx.ReadLoose(p, &c); err != nil {
				l.add(p, "%v", err)
			}
			for name := range c.Services {
				services[name] = true
			}
		}
	case "aws":
		services["workspace"] = true // AWS labs get one workspace pod (spec §8.2)
	default:
		l.add(lf, "runtime must be cluster, local or aws")
	}

	if len(lab.Terminals) == 0 {
		l.add(lf, "at least one terminal is required")
	}
	names := map[string]bool{}
	for _, term := range lab.Terminals {
		if names[term.Name] {
			l.add(lf, "duplicate terminal name %q", term.Name)
		}
		names[term.Name] = true
		if !services[term.Service] {
			l.add(lf, "terminal %q: service %q is not defined in the lab", term.Name, term.Service)
		}
	}
	if lab.Setup != nil {
		l.script(dir, lf, "lab setup", lab.Setup, services, 60*time.Second)
	}
	if len(lab.Tasks) == 0 {
		l.add(lf, "at least one task is required")
	}
	taskIDs := map[string]bool{}
	for _, t := range lab.Tasks {
		where := "task " + t.ID
		if t.ID == "" {
			l.add(lf, "every task needs an id")
		}
		if taskIDs[t.ID] {
			l.add(lf, "duplicate task id %q", t.ID)
		}
		taskIDs[t.ID] = true
		if t.Instructions == "" {
			l.add(lf, "%s: instructions are required", where)
		} else {
			l.file(dir, t.Instructions, lf)
		}
		if t.Check != nil && t.Quiz != "" {
			l.add(lf, "%s: use either check or quiz, not both", where)
		}
		if t.Check == nil && t.Quiz == "" && !t.HumanReview {
			l.add(lf, "%s: needs a check, a quiz or human_review", where)
		}
		if t.Check != nil {
			l.script(dir, lf, where+" check", t.Check, services, 30*time.Second)
		}
		if t.Setup != nil {
			l.script(dir, lf, where+" setup", t.Setup, services, 60*time.Second)
		}
		if t.Quiz != "" {
			var q *Question
			if quiz != nil {
				q = quiz.Question(t.Quiz)
			}
			if q == nil || q.Type != "terminal" {
				l.add(lf, "%s: quiz %q must be a terminal question in the module's quiz.yaml", where, t.Quiz)
			} else {
				runIn := q.RunIn
				if runIn == "" && len(lab.Terminals) > 0 {
					runIn = lab.Terminals[0].Service
				}
				q.Script = &Script{Script: q.Check, RunIn: runIn}
				l.script(dir, lf, where+" quiz check", q.Script, services, 30*time.Second)
				if t.Points == 0 {
					t.Points = q.Points
				}
			}
		}
		if t.Points == 0 {
			t.Points = 1
		}
		if t.Points < 0 {
			l.add(lf, "%s: points must be positive", where)
		}
		for i, h := range t.Hints {
			if (h.Text == "") == (h.File == "") {
				l.add(lf, "%s: hint %d needs exactly one of text or file", where, i+1)
			}
			if h.File != "" {
				l.file(dir, h.File, lf)
			}
			if c := h.EffectiveCost(lab); c < 0 || c > t.Points {
				l.add(lf, "%s: hint %d cost %.2f must be between 0 and the task's %.2f points", where, i+1, c, t.Points)
			}
		}
	}
	return lab
}

func (l *loader) script(dir, lf, what string, s *Script, services map[string]bool, def time.Duration) {
	if s.Script == "" {
		l.add(lf, "%s: script is required", what)
		return
	}
	if p, ok := l.file(dir, s.Script, lf); ok {
		if st, err := os.Stat(p); err == nil && st.Mode()&0o111 == 0 {
			l.add(p, "must be executable (chmod +x)")
		}
	}
	switch {
	case s.RunIn == "":
		l.add(lf, "%s: run_in is required", what)
	case !services[s.RunIn]:
		l.add(lf, "%s: run_in %q is not a service in the lab", what, s.RunIn)
	}
	if s.Timeout == 0 {
		s.Timeout = yamlx.Duration(def)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/content/...`
Expected: `ok crucible/internal/content`

- [ ] **Step 6: Commit**

```bash
git add internal/content
git commit -m "feat: content repo loader and validator (training, modules, quizzes, labs)"
```

---
### Task 5: "Forge 101" sample training and `crucible lint`

**Files:**
- Create: `examples/forge-101/**` (all files listed below)
- Create: `cmd/crucible/main.go`, `cmd/crucible/main_test.go`

**Interfaces:**
- Consumes: `content.Load`, `config.Load`.
- Produces:
  - The fixture training `forge-101` used by Tasks 9, 10, 14 and the e2e test:
    - module `01-welcome`: reading `how-we-work` + quiz;
    - module `02-first-lab`: reading `before-the-lab` + a local lab with tasks `t1-forge-file`, `t2-find-port` (setup + terminal quiz), `t3-fix-nginx`; terminals `shell`, `web`.
  - `lint(dir string, w io.Writer) int` in package main.

- [ ] **Step 1: Write the sample training**

`examples/forge-101/training.yaml`:
```yaml
id: forge-101
title: "Forge 101: First Heat"
description: Your first steps in the crucible — read, prove it, then get your hands dirty.
maintainers: [senior@crucible.local]
progression: linear
modules: [01-welcome, 02-first-lab]
```

`examples/forge-101/modules/01-welcome/module.yaml`:
```yaml
title: Welcome to the Forge
items:
  - reading: reading/how-we-work.md
  - quiz: quiz.yaml
```

`examples/forge-101/modules/01-welcome/reading/how-we-work.md`:
```markdown
# How We Work

Every engineer here was once new. The forge doesn't break metal — it reveals it.

- We ship small changes, often.
- We read before we run.
- We leave every system cleaner than we found it.

> Tip: images live in the training's `assets/` folder, e.g. `![anvil](assets/anvil.svg)`.
```

`examples/forge-101/modules/01-welcome/quiz.yaml`:
```yaml
pass_threshold: 0.8
questions:
  - id: q-ps
    type: single
    prompt: Which command lists running containers?
    options: ["docker ps", "docker ls", "docker run"]
    answer: 0
  - id: q-registries
    type: multi
    prompt: Which of these are container image registries?
    options: ["Docker Hub", "GitHub Container Registry", "crontab", "Amazon ECR"]
    answer: [0, 1, 3]
  - id: q-port
    type: exact
    prompt: What TCP port does plain HTTP use by default?
    answer: "80"
  - id: q-version
    type: regex
    prompt: Type any semantic version, for example 1.2.3
    answer: '\d+\.\d+\.\d+'
  - id: q-order
    type: order
    prompt: Put the forging steps in order.
    options: ["Heat", "Hammer", "Quench", "Temper"]
  - id: q-match
    type: match
    prompt: Match each tool to its job.
    pairs:
      - ["git", "version control"]
      - ["docker", "containers"]
      - ["terraform", "infrastructure as code"]
```

`examples/forge-101/modules/02-first-lab/module.yaml`:
```yaml
title: "First Heat: Your First Lab"
items:
  - reading: reading/before-the-lab.md
  - lab: lab
```

`examples/forge-101/modules/02-first-lab/reading/before-the-lab.md`:
```markdown
# Before the Lab

Labs run on **your laptop** through `crucible-agent`. Tasks are on the left; terminals are on the right.
Press **Check** when you think a task is done. Hints are there if you're stuck — some cost points.
```

`examples/forge-101/modules/02-first-lab/quiz.yaml`:
```yaml
questions:
  - id: q-broken-port
    type: terminal
    prompt: The web server was knocked off its port. Which port is nginx listening on now?
    check: checks/q-broken-port.sh
    run_in: web
```

`examples/forge-101/modules/02-first-lab/lab/lab.yaml`:
```yaml
id: first-heat
runtime: local
ttl: 1h
idle_timeout: 20m
idle_warning: 5m
task_order: linear
hint_cost: 0.25
terminals:
  - { name: shell, service: shell }
  - { name: web, service: web }
tasks:
  - id: t1-forge-file
    instructions: tasks/01-forge-file.md
    check: { script: checks/01-forge-file.sh, run_in: shell }
    points: 2
    hints:
      - text: "Use `echo` and output redirection (`>`)."
      - text: "Try: `echo 'hello forge' > /tmp/forged.txt`"
        cost: 1
  - id: t2-find-port
    instructions: tasks/02-find-port.md
    setup: { script: setup/02-break-nginx.sh, run_in: web }
    quiz: q-broken-port
  - id: t3-fix-nginx
    instructions: tasks/03-fix-nginx.md
    check: { script: checks/03-fix-nginx.sh, run_in: web }
    points: 3
    hints:
      - text: "Look at the `listen` lines in `/etc/nginx/conf.d/default.conf`."
      - file: hints/03-solution.md
        cost: 1.5
```

`examples/forge-101/modules/02-first-lab/lab/compose.yaml`:
```yaml
services:
  shell:
    image: alpine:3.22
    command: ["sleep", "infinity"]
  web:
    image: nginx:1.29-alpine
```

`examples/forge-101/modules/02-first-lab/lab/tasks/01-forge-file.md`:
```markdown
### Cast your first ingot

In the **shell** terminal, create `/tmp/forged.txt` containing exactly `hello forge`.
```
`examples/forge-101/modules/02-first-lab/lab/tasks/02-find-port.md`:
```markdown
### Something broke

We just knocked the web server off its usual port. Use the **web** terminal to find out which port nginx listens on now, and answer below.
```
`examples/forge-101/modules/02-first-lab/lab/tasks/03-fix-nginx.md`:
```markdown
### Put it back

Make nginx answer on port **80** again, then reload it.
```
`examples/forge-101/modules/02-first-lab/lab/hints/03-solution.md`:
```markdown
Run in the **web** terminal:

    sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload
```

`examples/forge-101/modules/02-first-lab/lab/checks/01-forge-file.sh`:
```sh
#!/bin/sh
if grep -qx 'hello forge' /tmp/forged.txt 2>/dev/null; then
  echo "Your first ingot is cast."
  exit 0
fi
echo "/tmp/forged.txt must contain exactly: hello forge"
exit 1
```
`examples/forge-101/modules/02-first-lab/lab/checks/q-broken-port.sh`:
```sh
#!/bin/sh
# Runs in the "web" service. $CRUCIBLE_ANSWER holds the trainee's answer.
if [ "$(echo "$CRUCIBLE_ANSWER" | tr -d ' ')" = "8081" ]; then
  echo "Correct: the forge moved to 8081."
  exit 0
fi
echo "Not quite. Try: grep listen /etc/nginx/conf.d/default.conf"
exit 1
```
`examples/forge-101/modules/02-first-lab/lab/checks/03-fix-nginx.sh`:
```sh
#!/bin/sh
if wget -q -T 2 -O /dev/null http://127.0.0.1:80/; then
  echo "The forge burns bright on port 80 again."
  exit 0
fi
echo "nginx is not answering on port 80 yet. Did you reload it after editing?"
exit 1
```
`examples/forge-101/modules/02-first-lab/lab/setup/02-break-nginx.sh`:
```sh
#!/bin/sh
# Idempotent: always converges to "nginx listens on 8081" (spec §8.5).
set -e
sed -i -e 's/listen\([[:space:]]*\)80;/listen\18081;/' -e 's/\[::\]:80;/[::]:8081;/' /etc/nginx/conf.d/default.conf
nginx -s reload
```

```bash
chmod +x examples/forge-101/modules/02-first-lab/lab/checks/*.sh examples/forge-101/modules/02-first-lab/lab/setup/*.sh
```

- [ ] **Step 2: Write the failing test**

`cmd/crucible/main_test.go`:
```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintExamplesPass(t *testing.T) {
	for _, dir := range []string{"../../examples/forge-101", "../../examples/platform"} {
		var out bytes.Buffer
		if code := lint(dir, &out); code != 0 {
			t.Fatalf("%s: exit %d\n%s", dir, code, out.String())
		}
	}
}

func TestLintReportsProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "training.yaml"), []byte("id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := lint(dir, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "title is required") {
		t.Fatalf("output: %s", out.String())
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/crucible/...`
Expected: FAIL (`lint` undefined).

- [ ] **Step 4: Implement**

`cmd/crucible/main.go`:
```go
// Command crucible is the authoring and operations CLI.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"crucible/internal/config"
	"crucible/internal/content"
)

const usage = `usage:
  crucible lint <content-or-platform-dir>`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "lint":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		os.Exit(lint(os.Args[2], os.Stdout))
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func lint(dir string, w io.Writer) int {
	if _, err := os.Stat(filepath.Join(dir, "platform.yaml")); err == nil {
		if _, err := config.Load(dir); err != nil {
			fmt.Fprintln(w, err)
			return 1
		}
		fmt.Fprintln(w, "platform config OK. The forge is ready.")
		return 0
	}
	t, probs := content.Load(dir)
	probs = append(probs, shellcheck(dir)...)
	for _, p := range probs {
		fmt.Fprintln(w, p)
	}
	if len(probs) > 0 {
		fmt.Fprintf(w, "%d problem(s). The metal isn't ready yet.\n", len(probs))
		return 1
	}
	fmt.Fprintf(w, "%s: %d module(s) OK. Ready for the forge.\n", t.ID, len(t.Modules))
	return 0
}

// shellcheck runs shellcheck on every *.sh file if it is installed (spec §6). Missing shellcheck is not an error.
func shellcheck(dir string) []content.Problem {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		return nil
	}
	var probs []content.Problem
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sh") {
			return err
		}
		if out, err := exec.Command("shellcheck", "-S", "warning", p).CombinedOutput(); err != nil {
			rel, _ := filepath.Rel(dir, p)
			probs = append(probs, content.Problem{File: rel, Msg: "shellcheck: " + strings.TrimSpace(string(out))})
		}
		return nil
	})
	return probs
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/crucible/... && go run ./cmd/crucible lint examples/forge-101`
Expected: `ok`, then `forge-101: 2 module(s) OK. Ready for the forge.`

- [ ] **Step 6: Commit**

```bash
git add examples/forge-101 cmd/crucible
git commit -m "feat: Forge 101 sample training and crucible lint"
```

---

### Task 6: Git sync (mirrors, per-SHA exports, last-good state)

**Files:**
- Create: `internal/gitsync/mirror.go`, `internal/gitsync/syncer.go`, `internal/gitsync/syncer_test.go`

**Interfaces:**
- Consumes: `config.Load`, `content.Load`.
- Produces:
  - `gitsync.New(dataDir, platformRepo, platformBranch string, log *slog.Logger) *Syncer`.
  - Syncer methods: `SyncOnce(ctx) error`, `Run(ctx, every time.Duration)`, `Trigger()`, `Current() *State`.
  - `State{Platform *config.Platform; PlatformSHA, PlatformErr string; Trainings map[string]*content.Training /* key "id@sha" */; Heads map[string]string; ProgramSHAs map[string]string /* key "team/training" */; Problems map[string][]content.Problem; SyncedAt time.Time}`.
  - State methods: `(*State).Training(id, sha string) *content.Training`, `(*State).ProgramTraining(team, training string) (*content.Training, string)`.

- [ ] **Step 1: Write the failing test**

`internal/gitsync/syncer_test.go`:
```go
package gitsync

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-qm", "change")
}

func newRepo(t *testing.T, files map[string]string) string {
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	commit(t, repo, files)
	return repo
}

var training = map[string]string{
	"training.yaml":          "id: t1\ntitle: T1\nmodules: [m1]\n",
	"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: r.md\n",
	"modules/m1/r.md":        "# Hello\n",
}

func setup(t *testing.T) (*Syncer, string, string) {
	trainingRepo := newRepo(t, training)
	platformRepo := newRepo(t, map[string]string{
		"platform.yaml":  "default_theme: forge\n",
		"trainings.yaml": "trainings:\n  t1: {repo: " + trainingRepo + "}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	})
	s := New(t.TempDir(), platformRepo, "main", slog.Default())
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, platformRepo, trainingRepo
}

func TestSyncLoadsHead(t *testing.T) {
	s, _, _ := setup(t)
	tr, sha := s.Current().ProgramTraining("a", "t1")
	if tr == nil || tr.Title != "T1" || len(sha) != 40 {
		t.Fatalf("training not loaded: %v %q %+v", tr, sha, s.Current().Problems)
	}
}

func TestBadHeadKeepsLastGood(t *testing.T) {
	s, _, trainingRepo := setup(t)
	_, goodSHA := s.Current().ProgramTraining("a", "t1")
	commit(t, trainingRepo, map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"}) // title removed
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := s.Current()
	tr, sha := st.ProgramTraining("a", "t1")
	if tr == nil || sha != goodSHA {
		t.Fatalf("program should stay on last good sha %s, got %q", goodSHA, sha)
	}
	if len(st.Problems["t1@"+st.Heads["t1"]]) == 0 {
		t.Fatalf("problems for bad head not recorded: %+v", st.Problems)
	}
}

func TestBadPlatformKeepsState(t *testing.T) {
	s, platformRepo, _ := setup(t)
	before := s.Current().PlatformSHA
	commit(t, platformRepo, map[string]string{"platform.yaml": "default_theme: forge\nbogus: 1\n"})
	if err := s.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected error for bad platform config")
	}
	st := s.Current()
	if st.PlatformSHA != before || st.Platform == nil || st.PlatformErr == "" {
		t.Fatalf("state not kept: sha %s err %q", st.PlatformSHA, st.PlatformErr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gitsync/...`
Expected: FAIL (`New` undefined).

- [ ] **Step 3: Implement the mirror**

`internal/gitsync/mirror.go`:
```go
// Package gitsync mirrors the platform and content repos and keeps an in-memory State of what is valid.
package gitsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Mirror is a bare `git clone --mirror` of a remote, driven through the git CLI so any host and auth method works.
type Mirror struct{ URL, Dir string }

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func (m Mirror) Fetch(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(m.Dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(m.Dir), 0o755); err != nil {
			return err
		}
		_, err := git(ctx, "", "clone", "--mirror", "--quiet", m.URL, m.Dir)
		return err
	}
	_, err := git(ctx, m.Dir, "remote", "update", "--prune")
	return err
}

func (m Mirror) Resolve(ctx context.Context, ref string) (string, error) {
	return git(ctx, m.Dir, "rev-parse", "--verify", ref+"^{commit}")
}

// Export writes the tree at sha into dest once; exports are immutable and reused.
func (m Mirror) Export(ctx context.Context, sha, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tarPath := tmp + ".tar"
	defer os.Remove(tarPath)
	if _, err := git(ctx, m.Dir, "archive", "--format=tar", "-o", tarPath, sha); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xf", tarPath, "-C", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("untar %s: %w: %s", sha, err, out)
	}
	return os.Rename(tmp, dest)
}
```

- [ ] **Step 4: Implement the syncer**

`internal/gitsync/syncer.go`:
```go
package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
)

type State struct {
	Platform    *config.Platform
	PlatformSHA string
	PlatformErr string                          // last platform config error; config stays at PlatformSHA
	Trainings   map[string]*content.Training     // "id@sha" → valid training
	Heads       map[string]string                // training id → tracked branch HEAD
	ProgramSHAs map[string]string                // "team/training" → sha the program runs
	Problems    map[string][]content.Problem     // "id@sha" → why that version is invalid
	SyncedAt    time.Time
}

func (s *State) Training(id, sha string) *content.Training { return s.Trainings[id+"@"+sha] }

func (s *State) ProgramTraining(team, training string) (*content.Training, string) {
	sha := s.ProgramSHAs[team+"/"+training]
	return s.Training(training, sha), sha
}

type Syncer struct {
	DataDir, PlatformRepo, PlatformBranch string
	Log                                   *slog.Logger

	cur     atomic.Pointer[State]
	mu      sync.Mutex
	trigger chan struct{}
}

func New(dataDir, platformRepo, platformBranch string, log *slog.Logger) *Syncer {
	return &Syncer{DataDir: dataDir, PlatformRepo: platformRepo, PlatformBranch: platformBranch, Log: log, trigger: make(chan struct{}, 1)}
}

// Current returns the latest state, or nil before the first successful sync.
func (s *Syncer) Current() *State { return s.cur.Load() }

// Trigger requests an immediate sync (git webhook).
func (s *Syncer) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Syncer) Run(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		case <-s.trigger:
		}
		if err := s.SyncOnce(ctx); err != nil {
			s.Log.Error("git sync failed", "err", err)
		}
	}
}

func (s *Syncer) mirror(url string) Mirror {
	h := sha256.Sum256([]byte(url))
	return Mirror{URL: url, Dir: filepath.Join(s.DataDir, "mirrors", hex.EncodeToString(h[:8]))}
}

func (s *Syncer) SyncOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cur.Load()

	pm := s.mirror(s.PlatformRepo)
	if err := pm.Fetch(ctx); err != nil {
		return err
	}
	psha, err := pm.Resolve(ctx, s.PlatformBranch)
	if err != nil {
		return err
	}
	pdir := filepath.Join(s.DataDir, "platform", psha)
	if err := pm.Export(ctx, psha, pdir); err != nil {
		return err
	}
	plat, err := config.Load(pdir)
	if err != nil {
		// Keep serving the last good config; surface the error on Forge Status.
		next := State{PlatformErr: err.Error()}
		if prev != nil {
			next = *prev
			next.PlatformErr = err.Error()
		}
		s.cur.Store(&next)
		return fmt.Errorf("platform config at %.7s: %w", psha, err)
	}

	st := &State{Platform: plat, PlatformSHA: psha, Trainings: map[string]*content.Training{},
		Heads: map[string]string{}, ProgramSHAs: map[string]string{}, Problems: map[string][]content.Problem{}, SyncedAt: time.Now()}
	if prev != nil {
		// ponytail: every version ever loaded stays in memory so running labs keep their content after a pin bump.
		// Ceiling: memory grows with commits; prune versions with no program and no live lab if it ever matters.
		maps.Copy(st.Trainings, prev.Trainings)
	}

	for id, ref := range plat.Trainings {
		m := s.mirror(ref.Repo)
		if err := m.Fetch(ctx); err != nil {
			st.Problems[id] = []content.Problem{{File: ref.Repo, Msg: err.Error()}}
			s.keepPrevPrograms(st, prev, plat, id)
			continue
		}
		head, err := m.Resolve(ctx, ref.Branch)
		if err != nil {
			st.Problems[id] = []content.Problem{{File: ref.Repo, Msg: err.Error()}}
			s.keepPrevPrograms(st, prev, plat, id)
			continue
		}
		st.Heads[id] = head
		s.load(ctx, st, prev, m, id, head)
		for teamID, team := range plat.Teams {
			p, ok := team.Programs[id]
			if !ok {
				continue
			}
			key := teamID + "/" + id
			sha := head
			if p.PinnedRef != "" {
				if sha, err = m.Resolve(ctx, p.PinnedRef); err != nil {
					st.Problems[key] = []content.Problem{{File: "teams/" + teamID + "/programs/" + id + ".yaml", Msg: err.Error()}}
					continue
				}
				s.load(ctx, st, prev, m, id, sha)
			}
			st.ProgramSHAs[key] = sha
			if st.Training(id, sha) == nil && prev != nil && prev.Training(id, prev.ProgramSHAs[key]) != nil {
				st.ProgramSHAs[key] = prev.ProgramSHAs[key] // invalid new version: stay on the last good one
			}
		}
	}
	s.cur.Store(st)
	return nil
}

func (s *Syncer) keepPrevPrograms(st, prev *State, plat *config.Platform, id string) {
	if prev == nil {
		return
	}
	for teamID := range plat.Teams {
		if sha, ok := prev.ProgramSHAs[teamID+"/"+id]; ok {
			st.ProgramSHAs[teamID+"/"+id] = sha
		}
	}
}

func (s *Syncer) load(ctx context.Context, st, prev *State, m Mirror, id, sha string) {
	key := id + "@" + sha
	if st.Trainings[key] != nil || st.Problems[key] != nil {
		return
	}
	if prev != nil && prev.Problems[key] != nil {
		st.Problems[key] = prev.Problems[key]
		return
	}
	dir := filepath.Join(s.DataDir, "content", id, sha)
	if err := m.Export(ctx, sha, dir); err != nil {
		st.Problems[key] = []content.Problem{{File: id, Msg: err.Error()}}
		return
	}
	t, probs := content.Load(dir)
	if t != nil && t.ID != id {
		probs, t = append(probs, content.Problem{File: "training.yaml", Msg: fmt.Sprintf("id %q does not match registry id %q", t.ID, id)}), nil
	}
	if len(probs) > 0 {
		st.Problems[key] = probs
		s.Log.Warn("training version rejected", "training", id, "sha", sha[:7], "problems", len(probs))
		return
	}
	st.Trainings[key] = t
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/gitsync/...`
Expected: `ok crucible/internal/gitsync`

- [ ] **Step 6: Commit**

```bash
git add internal/gitsync
git commit -m "feat: git sync with per-SHA exports and last-good fallback"
```

---

### Task 7: Authentication (OIDC login, sessions, agent pairing tokens)

**Files:**
- Create: `internal/auth/store.go`, `internal/auth/oidc.go`, `internal/auth/middleware.go`, `internal/auth/store_test.go`

**Interfaces:**
- Consumes: `dbtest.New`, `httpx`.
- Produces:
  - Types: `auth.User{ID int64; Sub, Email, Name, Theme string; CalmMotion bool}` (JSON `id,email,name,theme,calm_motion`), `auth.Store{DB *pgxpool.Pool}`.
  - Store methods:
    - `UpsertUser(ctx, sub, email, name) (*User, error)`
    - `CreateSession(ctx, userID int64, ttl time.Duration) (string, error)`
    - `UserBySession(ctx, token) (*User, error)`
    - `DeleteSession(ctx, token) error`
    - `SetPrefs(ctx, userID int64, theme string, calm bool) error`
    - `CreateAgentToken(ctx, userID int64) (string, error)`
    - `UserByAgentToken(ctx, token) (*User, error)`
    - `Middleware(http.Handler) http.Handler`
  - Functions: `auth.RequireUser(http.Handler) http.Handler`, `auth.UserFrom(ctx) *User`, `auth.WithUser(ctx, *User) context.Context` (for tests).
  - OIDC: `auth.NewOIDC(ctx, OIDCConfig, Store, secure bool) (*OIDC, error)` with handlers `Login`, `Callback`, `Logout`; `OIDCConfig{Issuer, DiscoveryURL, ClientID, ClientSecret, RedirectURL string}`. `auth.SessionCookie = "crucible_session"`.

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/coreos/go-oidc/v3@latest golang.org/x/oauth2@latest
```

- [ ] **Step 2: Write the failing test**

`internal/auth/store_test.go`:
```go
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"crucible/internal/db/dbtest"
)

func TestUsersSessionsAndAgentTokens(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}

	u, err := s.UpsertUser(ctx, "sub-1", "Trainee@Crucible.local", "Tara")
	if err != nil || u.Email != "trainee@crucible.local" {
		t.Fatalf("upsert: %+v %v", u, err)
	}
	u2, _ := s.UpsertUser(ctx, "sub-1", "tara@crucible.local", "Tara T")
	if u2.ID != u.ID || u2.Email != "tara@crucible.local" {
		t.Fatalf("upsert should update the same user: %+v", u2)
	}

	tok, _ := s.CreateSession(ctx, u.ID, time.Hour)
	if got, _ := s.UserBySession(ctx, tok); got == nil || got.ID != u.ID {
		t.Fatalf("session lookup failed")
	}
	expired, _ := s.CreateSession(ctx, u.ID, -time.Minute)
	if got, _ := s.UserBySession(ctx, expired); got != nil {
		t.Fatal("expired session must not authenticate")
	}

	a1, _ := s.CreateAgentToken(ctx, u.ID)
	a2, _ := s.CreateAgentToken(ctx, u.ID)
	if got, _ := s.UserByAgentToken(ctx, a1); got != nil {
		t.Fatal("old agent token must be revoked by a new one")
	}
	if got, _ := s.UserByAgentToken(ctx, a2); got == nil || got.ID != u.ID {
		t.Fatal("new agent token must work")
	}
}

func TestMiddlewareAndRequireUser(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	u, _ := s.UpsertUser(ctx, "sub-2", "a@x", "A")
	tok, _ := s.CreateSession(ctx, u.ID, time.Hour)

	h := s.Middleware(RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(UserFrom(r.Context()).Email))
	})))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 401 {
		t.Fatalf("no cookie: got %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "a@x" {
		t.Fatalf("with cookie: %d %q", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/auth/...`
Expected: FAIL (undefined `Store`).

- [ ] **Step 4: Implement the store**

`internal/auth/store.go`:
```go
// Package auth handles OIDC login, sessions and agent pairing tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID         int64  `json:"id"`
	Sub        string `json:"-"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Theme      string `json:"theme"`
	CalmMotion bool   `json:"calm_motion"`
}

type Store struct{ DB *pgxpool.Pool }

const userCols = "u.id, u.sub, u.email, u.name, u.theme, u.calm_motion"

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Sub, &u.Email, &u.Name, &u.Theme, &u.CalmMotion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s Store) UpsertUser(ctx context.Context, sub, email, name string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `
		INSERT INTO users AS u (sub, email, name) VALUES ($1, lower($2), $3)
		ON CONFLICT (sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name
		RETURNING `+userCols, sub, email, name))
}

func (s Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	tok := newToken()
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3)`,
		hash(tok), userID, time.Now().Add(ttl))
	return tok, err
}

func (s Store) UserBySession(ctx context.Context, token string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now()`, hash(token)))
}

func (s Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, hash(token))
	return err
}

func (s Store) SetPrefs(ctx context.Context, userID int64, theme string, calm bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET theme = $2, calm_motion = $3 WHERE id = $1`, userID, theme, calm)
	return err
}

// CreateAgentToken issues a new pairing token and revokes the user's previous ones (one laptop at a time).
func (s Store) CreateAgentToken(ctx context.Context, userID int64) (string, error) {
	tok := newToken()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE agent_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_tokens (user_id, token_hash) VALUES ($1, $2)`, userID, hash(tok)); err != nil {
		return "", err
	}
	return tok, tx.Commit(ctx)
}

func (s Store) UserByAgentToken(ctx context.Context, token string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `
		WITH t AS (UPDATE agent_tokens SET last_used_at = now()
		           WHERE token_hash = $1 AND revoked_at IS NULL RETURNING user_id)
		SELECT `+userCols+` FROM t JOIN users u ON u.id = t.user_id`, hash(token)))
}
```

- [ ] **Step 5: Implement middleware and OIDC handlers**

`internal/auth/middleware.go`:
```go
package auth

import (
	"context"
	"net/http"

	"crucible/internal/httpx"
)

const SessionCookie = "crucible_session"

type ctxKey struct{}

func WithUser(ctx context.Context, u *User) context.Context { return context.WithValue(ctx, ctxKey{}, u) }

func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Middleware attaches the session's user (if any) to the request context.
func (s Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			if u, err := s.UserBySession(r.Context(), c.Value); err == nil && u != nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

`internal/auth/oidc.go`:
```go
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Issuer       string // issuer as the browser sees it (must match tokens' iss)
	DiscoveryURL string // optional: where the server reaches the IdP (e.g. inside docker compose)
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type OIDC struct {
	cfg      oauth2.Config
	verifier *oidc.IDTokenVerifier
	store    Store
	secure   bool
}

const oauthCookie = "crucible_oauth"
const sessionTTL = 12 * time.Hour

func NewOIDC(ctx context.Context, c OIDCConfig, store Store, secure bool) (*OIDC, error) {
	disc := c.Issuer
	if c.DiscoveryURL != "" && c.DiscoveryURL != c.Issuer {
		// The IdP is reachable at a different URL from the server than from the browser.
		ctx = oidc.InsecureIssuerURLContext(ctx, c.Issuer)
		disc = c.DiscoveryURL
	}
	p, err := oidc.NewProvider(ctx, disc)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery at %s: %w", disc, err)
	}
	return &OIDC{
		cfg: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}},
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
		store:    store,
		secure:   secure,
	}, nil
}

func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	state, verifier := newToken(), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state + "." + verifier, Path: "/auth",
		MaxAge: 600, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, o.cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oauthCookie)
	state, verifier, ok := strings.Cut(valueOr(c, err), ".")
	if !ok || r.URL.Query().Get("state") != state {
		http.Error(w, "login expired, please try again", http.StatusBadRequest)
		return
	}
	tok, err := o.cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "login failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := o.verifier.Verify(r.Context(), raw)
	if err != nil {
		http.Error(w, "invalid id token", http.StatusBadGateway)
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil || claims.Email == "" {
		http.Error(w, "your identity provider did not send an email address", http.StatusForbidden)
		return
	}
	if claims.Name == "" {
		claims.Name = claims.Username
	}
	u, err := o.store.UpsertUser(r.Context(), idt.Subject, claims.Email, claims.Name)
	if err != nil {
		http.Error(w, "could not save user", http.StatusInternalServerError)
		return
	}
	session, err := o.store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/auth", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: session, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = o.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func valueOr(c *http.Cookie, err error) string {
	if err != nil {
		return ""
	}
	return c.Value
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/auth/...`
Expected: `ok crucible/internal/auth`. The OIDC round trip is covered end to end by the local check in Task 20.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/auth
git commit -m "feat: OIDC login with PKCE, hashed sessions, agent pairing tokens"
```

---

### Task 8: RBAC

**Files:**
- Create: `internal/rbac/rbac.go`, `internal/rbac/rbac_test.go`

**Interfaces:**
- Consumes: `config.Platform`, `config.Team.RoleOf`.
- Produces:
  - `rbac.Checker{P *config.Platform}` with methods `IsAdmin(email) bool`, `Can(actor string, a Action, team, training, subject string) bool`, `Enrollments(email string) []Enrollment`.
  - Actions: `TakeTraining, ViewProgress, ManageProgram, Score, ApproveLabs, ViewSpend, EditTeam`.
  - `Enrollment{Team *config.Team; Program *config.Program}`.
  - Rules are the spec §5.3 matrix. Nobody scores or approves for themselves (`subject == actor`).

- [ ] **Step 1: Write the failing test**

`internal/rbac/rbac_test.go`:
```go
package rbac

import (
	"testing"

	"crucible/internal/config"
)

func TestMatrix(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	c := Checker{P: p}
	const (
		admin   = "admin@crucible.local"
		leader  = "leader@crucible.local"
		senior  = "senior@crucible.local"
		trainee = "trainee@crucible.local"
		other   = "stranger@crucible.local"
	)
	cases := []struct {
		actor   string
		action  Action
		subject string
		want    bool
	}{
		{admin, EditTeam, "", true},
		{trainee, TakeTraining, "", true},
		{leader, TakeTraining, "", false}, // not enrolled
		{other, TakeTraining, "", false},
		{trainee, ViewProgress, trainee, true},
		{trainee, ViewProgress, senior, false},
		{senior, ViewProgress, trainee, true}, // senior + mentor
		{leader, ManageProgram, "", true},
		{senior, ManageProgram, "", false},
		{senior, Score, trainee, true},
		{senior, Score, senior, false}, // never score yourself
		{leader, ApproveLabs, trainee, true},
		{leader, ApproveLabs, leader, false}, // never approve your own request
		{trainee, ApproveLabs, trainee, false},
		{leader, ViewSpend, "", true},
		{trainee, ViewSpend, "", false},
		{leader, EditTeam, "", true},
		{senior, EditTeam, "", false},
	}
	for _, tc := range cases {
		if got := c.Can(tc.actor, tc.action, "forge", "forge-101", tc.subject); got != tc.want {
			t.Errorf("%s %v subject=%s: got %v want %v", tc.actor, tc.action, tc.subject, got, tc.want)
		}
	}
	if n := len(c.Enrollments("TRAINEE@crucible.local")); n != 1 {
		t.Errorf("enrollments = %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rbac/...`
Expected: FAIL (`Checker` undefined).

- [ ] **Step 3: Implement**

`internal/rbac/rbac.go`:
```go
// Package rbac answers "may this person do this?" from the platform config (spec §5.3).
package rbac

import (
	"slices"
	"sort"
	"strings"

	"crucible/internal/config"
)

type Action int

const (
	TakeTraining Action = iota
	ViewProgress
	ManageProgram
	Score
	ApproveLabs
	ViewSpend
	EditTeam
)

type Checker struct{ P *config.Platform }

type Enrollment struct {
	Team    *config.Team
	Program *config.Program
}

func (c Checker) IsAdmin(email string) bool {
	return slices.Contains(c.P.Admins, strings.ToLower(email))
}

func (c Checker) Can(actor string, a Action, team, training, subject string) bool {
	actor, subject = strings.ToLower(actor), strings.ToLower(subject)
	if c.IsAdmin(actor) {
		return true
	}
	t := c.P.Teams[team]
	if t == nil {
		return false
	}
	role := t.RoleOf(actor)
	p := t.Programs[training]
	in := func(list []string) bool { return p != nil && slices.Contains(list, actor) }

	switch a {
	case TakeTraining:
		return in(p.Enrolled)
	case ViewProgress:
		return (subject == actor && in(p.Enrolled)) || role == "leader" || role == "senior" ||
			in(p.Roles.Manager) || in(p.Roles.Scorers) || (subject != "" && t.Mentors[subject] == actor)
	case ManageProgram:
		return role == "leader" || in(p.Roles.Manager)
	case Score:
		return subject != actor && in(p.Roles.Scorers)
	case ApproveLabs:
		return subject != actor && (role == "leader" || in(p.Roles.Approvers))
	case ViewSpend:
		return role == "leader" || in(p.Roles.Manager) || in(p.Roles.Approvers)
	case EditTeam:
		return role == "leader"
	}
	return false
}

// Enrollments lists the programs the user is enrolled in, sorted by team then training.
func (c Checker) Enrollments(email string) []Enrollment {
	email = strings.ToLower(email)
	var out []Enrollment
	for _, t := range c.P.Teams {
		for _, p := range t.Programs {
			if slices.Contains(p.Enrolled, email) {
				out = append(out, Enrollment{Team: t, Program: p})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Team.ID != out[j].Team.ID {
			return out[i].Team.ID < out[j].Team.ID
		}
		return out[i].Program.Training < out[j].Program.Training
	})
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rbac/...`
Expected: `ok crucible/internal/rbac`

- [ ] **Step 5: Commit**

```bash
git add internal/rbac
git commit -m "feat: RBAC checker implementing the spec permission matrix"
```

---
### Task 9: Quiz scoring and public quiz payload

**Files:**
- Create: `internal/learn/quiz.go`, `internal/learn/quiz_test.go`

**Interfaces:**
- Consumes: `content.Quiz`, `content.Question`, `content.IsHuman`.
- Produces:
  - Types:
    - `learn.Choice{ID int; Text string}`
    - `learn.PublicQuestion{ID, Type, Prompt string; Points float64; Options []Choice; Left []string; Right []Choice; Human bool}`
    - `learn.Result{Score, Max, Percent float64; Passed bool; Correct map[string]bool; PendingHuman bool}`
  - `learn.PublicQuiz(q *content.Quiz, seed uint64) []PublicQuestion`: terminal questions are excluded; `order` options and `match` right-hand choices are shuffled deterministically by seed.
  - `learn.Score(q *content.Quiz, answers map[string]json.RawMessage) Result`.
  - Answer wire formats:
    - `single`: choice id `int`
    - `multi`: `[]int`
    - `exact` and `regex`: `string`
    - `order`: choice ids in chosen order
    - `match`: for each left item in order, the chosen right choice id.

- [ ] **Step 1: Write the failing test**

`internal/learn/quiz_test.go`:
```go
package learn

import (
	"encoding/json"
	"strings"
	"testing"

	"crucible/internal/content"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func forge101(t *testing.T) *content.Training {
	t.Helper()
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatalf("fixture invalid: %v", probs)
	}
	return tr
}

func correctAnswers() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"q-ps": raw(`0`), "q-registries": raw(`[3,0,1]`), "q-port": raw(`" 80 "`),
		"q-version": raw(`"2.10.0"`), "q-order": raw(`[0,1,2,3]`), "q-match": raw(`[0,1,2]`),
	}
}

func TestScore(t *testing.T) {
	q := forge101(t).Module("01-welcome").Quiz
	res := Score(q, correctAnswers())
	if res.Score != 6 || res.Max != 6 || !res.Passed {
		t.Fatalf("all correct: %+v", res)
	}

	one := correctAnswers()
	one["q-port"] = raw(`"8080"`)
	if res := Score(q, one); !res.Passed || res.Correct["q-port"] {
		t.Fatalf("5/6 = 83%% should pass the 80%% threshold: %+v", res)
	}

	two := correctAnswers()
	two["q-port"], two["q-order"] = raw(`"8080"`), raw(`[1,0,2,3]`)
	if res := Score(q, two); res.Passed {
		t.Fatalf("4/6 must fail: %+v", res)
	}

	if res := Score(q, map[string]json.RawMessage{"q-ps": raw(`"not a number"`)}); res.Score != 0 {
		t.Fatalf("garbage answers score 0: %+v", res)
	}
}

func TestPublicQuizHidesAnswers(t *testing.T) {
	tr := forge101(t)
	b, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 42))
	s := string(b)
	for _, leak := range []string{`"answer"`, `"rubric"`, `checks/`} {
		if strings.Contains(s, leak) {
			t.Fatalf("public quiz leaks %s: %s", leak, s)
		}
	}
	if got := PublicQuiz(tr.Module("02-first-lab").Quiz, 42); len(got) != 0 {
		t.Fatalf("terminal questions must not appear in the standalone quiz: %+v", got)
	}
	a, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 7))
	b2, _ := json.Marshal(PublicQuiz(tr.Module("01-welcome").Quiz, 7))
	if string(a) != string(b2) {
		t.Fatal("shuffle must be deterministic per seed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/learn/...`
Expected: FAIL (`Score` undefined).

- [ ] **Step 3: Implement**

`internal/learn/quiz.go`:
```go
// Package learn serves trainings: outlines, reading, quizzes and progress.
package learn

import (
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"

	"crucible/internal/content"
)

type Choice struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type PublicQuestion struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Prompt  string   `json:"prompt"`
	Points  float64  `json:"points"`
	Options []Choice `json:"options,omitempty"`
	Left    []string `json:"left,omitempty"`
	Right   []Choice `json:"right,omitempty"`
	Human   bool     `json:"human,omitempty"`
}

type Result struct {
	Score        float64         `json:"score"`
	Max          float64         `json:"max"`
	Percent      float64         `json:"percent"`
	Passed       bool            `json:"passed"`
	Correct      map[string]bool `json:"correct"`
	PendingHuman bool            `json:"pending_human"`
}

// PublicQuiz is what the browser receives: no answers, and terminal questions are left to labs.
func PublicQuiz(q *content.Quiz, seed uint64) []PublicQuestion {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	shuffle := func(c []Choice) { r.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] }) }
	out := []PublicQuestion{}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		pq := PublicQuestion{ID: x.ID, Type: x.Type, Prompt: x.Prompt, Points: x.Points, Human: content.IsHuman(x.Type)}
		switch x.Type {
		case "single", "multi", "order":
			for i, o := range x.Options {
				pq.Options = append(pq.Options, Choice{ID: i, Text: o})
			}
			if x.Type == "order" {
				shuffle(pq.Options)
			}
		case "match":
			for i, p := range x.Pairs {
				pq.Left = append(pq.Left, p[0])
				pq.Right = append(pq.Right, Choice{ID: i, Text: p[1]})
			}
			shuffle(pq.Right)
		}
		out = append(out, pq)
	}
	return out
}

func Score(q *content.Quiz, answers map[string]json.RawMessage) Result {
	res := Result{Correct: map[string]bool{}}
	for _, x := range q.Questions {
		if x.Type == "terminal" {
			continue
		}
		if content.IsHuman(x.Type) {
			res.PendingHuman = true // scored by people in M5
			continue
		}
		res.Max += x.Points
		ok := correct(x, answers[x.ID])
		res.Correct[x.ID] = ok
		if ok {
			res.Score += x.Points
		}
	}
	if res.Max > 0 {
		res.Percent = res.Score / res.Max
	}
	res.Passed = res.Max > 0 && !res.PendingHuman && res.Percent >= q.PassThreshold-1e-9
	return res
}

func correct(x *content.Question, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch x.Type {
	case "single":
		var got, want int
		return json.Unmarshal(raw, &got) == nil && x.Answer.Decode(&want) == nil && got == want
	case "multi":
		var got, want []int
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&want) != nil {
			return false
		}
		slices.Sort(got)
		slices.Sort(want)
		return slices.Equal(slices.Compact(got), slices.Compact(want))
	case "exact":
		var got, want string
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&want) != nil {
			return false
		}
		got, want = strings.TrimSpace(got), strings.TrimSpace(want)
		if x.CaseSensitive {
			return got == want
		}
		return strings.EqualFold(got, want)
	case "regex":
		var got, pat string
		if json.Unmarshal(raw, &got) != nil || x.Answer.Decode(&pat) != nil {
			return false
		}
		flags := "(?i)"
		if x.CaseSensitive {
			flags = ""
		}
		re, err := regexp.Compile(flags + "^(?:" + pat + ")$")
		return err == nil && re.MatchString(strings.TrimSpace(got))
	case "order", "match":
		var got []int
		n := len(x.Options)
		if x.Type == "match" {
			n = len(x.Pairs)
		}
		if json.Unmarshal(raw, &got) != nil || len(got) != n {
			return false
		}
		for i, v := range got {
			if v != i { // options/pairs are authored in the correct order
				return false
			}
		}
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/learn/...`
Expected: `ok crucible/internal/learn`

- [ ] **Step 5: Commit**

```bash
git add internal/learn
git commit -m "feat: instant quiz scoring and answer-free public quiz payload"
```

---

### Task 10: Learning service and HTTP (programs, outline, reading, quiz, progression)

**Files:**
- Create: `internal/learn/service.go`, `internal/learn/http.go`, `internal/learn/service_test.go`

**Interfaces:**
- Consumes: `gitsync.State`, `rbac.Checker`, `auth.User`, `apperr`, `httpx`, `PublicQuiz`, `Score`.
- Produces:
  - `learn.Service{DB *pgxpool.Pool; State func() *gitsync.State}` with methods:
    - `Program(u, team, training) (*gitsync.State, *content.Training, string, error)`
    - `EnsureUnlocked(ctx, u, team string, t *content.Training, module string) (*content.Module, error)`
    - `SetItem(ctx, userID int64, team, training, module, item, status string, score float64) error`
    - `Programs`, `Outline`, `Reading`, `MarkRead`, `Quiz`, `SubmitQuiz`
    - `Routes(r chi.Router)`
  - Labs (Task 14) use `Program`, `EnsureUnlocked` and `SetItem`.
  - HTTP:
    - `GET /api/programs`
    - `GET /api/programs/{team}/{training}`
    - `GET /api/programs/{team}/{training}/assets/*`
    - `GET /api/programs/{team}/{training}/modules/{module}/reading/{item}` → `{title, markdown}`
    - `POST …/reading/{item}/read`
    - `GET …/modules/{module}/quiz` → `QuizView`
    - `POST …/modules/{module}/quiz/attempts` with `{answers}` → `Result`

- [ ] **Step 1: Write the failing test**

`internal/learn/service_test.go`:
```go
package learn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
)

func fixture(t *testing.T) (*Service, *auth.User, *auth.User) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	st := &gitsync.State{Platform: plat,
		Trainings:   map[string]*content.Training{"forge-101@abc": forge101(t)},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	leader, _ := store.UpsertUser(ctx, "s2", "leader@crucible.local", "Leo")
	return &Service{DB: pool, State: func() *gitsync.State { return st }}, trainee, leader
}

func TestProgressionUnlocksModuleTwo(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, _, err := s.Reading(ctx, u, "forge", "forge-101", "02-first-lab", "before-the-lab"); !errors.Is(err, apperr.Locked) {
		t.Fatalf("module 2 must be locked, got %v", err)
	}
	if err := s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work"); err != nil {
		t.Fatal(err)
	}
	res, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", correctAnswers())
	if err != nil || !res.Passed {
		t.Fatalf("quiz: %+v %v", res, err)
	}
	o, err := s.Outline(ctx, u, "forge", "forge-101")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Modules[0].Complete || o.Modules[1].Locked || o.Percent != 50 {
		t.Fatalf("outline after module 1: %+v", o)
	}
	_, md, err := s.Reading(ctx, u, "forge", "forge-101", "02-first-lab", "before-the-lab")
	if err != nil || !strings.Contains(md, "crucible-agent") {
		t.Fatalf("reading module 2: %v", err)
	}
}

func TestFailedQuizKeepsLockAndPassedQuizStaysPassed(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	bad := correctAnswers()
	bad["q-port"], bad["q-version"] = raw(`"1"`), raw(`"nope"`)
	if res, _ := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", bad); res.Passed {
		t.Fatal("should fail")
	}
	o, _ := s.Outline(ctx, u, "forge", "forge-101")
	if !o.Modules[1].Locked {
		t.Fatal("module 2 must stay locked after a failed quiz")
	}
	_, _ = s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", correctAnswers())
	_, _ = s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", bad) // a later failed retry
	o, _ = s.Outline(ctx, u, "forge", "forge-101")
	if o.Modules[1].Locked {
		t.Fatal("a later failed attempt must not re-lock a passed module")
	}
}

func TestNotEnrolledIsForbidden(t *testing.T) {
	ctx := context.Background()
	s, _, leader := fixture(t)
	cards, _ := s.Programs(ctx, leader)
	if len(cards) != 0 {
		t.Fatalf("leader is not enrolled: %+v", cards)
	}
	if _, err := s.Outline(ctx, leader, "forge", "forge-101"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/learn/...`
Expected: FAIL (`Service` undefined).

- [ ] **Step 3: Implement the service**

`internal/learn/service.go`:
```go
package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/rbac"
)

type Service struct {
	DB    *pgxpool.Pool
	State func() *gitsync.State
}

type ProgramCard struct {
	Team        string `json:"team"`
	TeamName    string `json:"team_name"`
	Training    string `json:"training"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Percent     int    `json:"percent"`
	Available   bool   `json:"available"`
}

type Outline struct {
	Team        string       `json:"team"`
	Training    string       `json:"training"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Progression string       `json:"progression"`
	Percent     int          `json:"percent"`
	Modules     []ModuleView `json:"modules"`
}

type ModuleView struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Locked   bool       `json:"locked"`
	Complete bool       `json:"complete"`
	Items    []ItemView `json:"items"`
}

type ItemView struct {
	content.Item
	Status string `json:"status"` // new | in_progress | complete
}

type QuizView struct {
	PassThreshold float64          `json:"pass_threshold"`
	Questions     []PublicQuestion `json:"questions"`
	Status        string           `json:"status"`
}

type progress map[string]string // "module/item" → status

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Program checks enrolment and returns the training version this team's program runs.
func (s *Service) Program(u *auth.User, team, training string) (*gitsync.State, *content.Training, string, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, "", err
	}
	if !(rbac.Checker{P: st.Platform}).Can(u.Email, rbac.TakeTraining, team, training, "") {
		return nil, nil, "", apperr.Wrap(apperr.Forbidden, "you are not enrolled in this training")
	}
	t, sha := st.ProgramTraining(team, training)
	if t == nil {
		return nil, nil, "", apperr.Wrap(apperr.Unavailable, "this training's content is unavailable right now")
	}
	return st, t, sha, nil
}

func (s *Service) progress(ctx context.Context, userID int64, team, training string) (progress, error) {
	rows, err := s.DB.Query(ctx, `SELECT module, item, status FROM item_progress
		WHERE user_id = $1 AND team = $2 AND training = $3`, userID, team, training)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := progress{}
	for rows.Next() {
		var m, i, st string
		if err := rows.Scan(&m, &i, &st); err != nil {
			return nil, err
		}
		p[m+"/"+i] = st
	}
	return p, rows.Err()
}

func outline(t *content.Training, prog progress) []ModuleView {
	out := []ModuleView{}
	allPrevComplete := true
	for _, m := range t.Modules {
		mv := ModuleView{ID: m.ID, Title: m.Title, Locked: t.Progression == "linear" && !allPrevComplete, Complete: true}
		for _, it := range m.Items {
			st := prog[m.ID+"/"+it.ID]
			if st == "" {
				st = "new"
			}
			if st != "complete" {
				mv.Complete = false
			}
			mv.Items = append(mv.Items, ItemView{Item: it, Status: st})
		}
		allPrevComplete = allPrevComplete && mv.Complete
		out = append(out, mv)
	}
	return out
}

func percent(t *content.Training, prog progress) int {
	total, done := 0, 0
	for _, m := range t.Modules {
		for _, it := range m.Items {
			total++
			if prog[m.ID+"/"+it.ID] == "complete" {
				done++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return done * 100 / total
}

func (s *Service) Programs(ctx context.Context, u *auth.User) ([]ProgramCard, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	cards := []ProgramCard{}
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(u.Email) {
		c := ProgramCard{Team: e.Team.ID, TeamName: e.Team.Name, Training: e.Program.Training, Title: e.Program.Training}
		if t, _ := st.ProgramTraining(e.Team.ID, e.Program.Training); t != nil {
			prog, err := s.progress(ctx, u.ID, e.Team.ID, t.ID)
			if err != nil {
				return nil, err
			}
			c.Title, c.Description, c.Available, c.Percent = t.Title, t.Description, true, percent(t, prog)
		}
		cards = append(cards, c)
	}
	return cards, nil
}

func (s *Service) Outline(ctx context.Context, u *auth.User, team, training string) (*Outline, error) {
	_, t, _, err := s.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	return &Outline{Team: team, Training: t.ID, Title: t.Title, Description: t.Description,
		Progression: t.Progression, Percent: percent(t, prog), Modules: outline(t, prog)}, nil
}

// EnsureUnlocked returns the module, or apperr.Locked if a linear training has unfinished earlier modules.
func (s *Service) EnsureUnlocked(ctx context.Context, u *auth.User, team string, t *content.Training, module string) (*content.Module, error) {
	m := t.Module(module)
	if m == nil {
		return nil, apperr.Wrap(apperr.NotFound, "module not found")
	}
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	for _, mv := range outline(t, prog) {
		if mv.ID == module && mv.Locked {
			return nil, apperr.Wrap(apperr.Locked, "finish the earlier modules first")
		}
	}
	return m, nil
}

// SetItem records progress; a completed item never goes back to in_progress.
func (s *Service) SetItem(ctx context.Context, userID int64, team, training, module, item, status string, score float64) error {
	_, err := s.DB.Exec(ctx, `
		INSERT INTO item_progress (user_id, team, training, module, item, status, score)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, team, training, module, item) DO UPDATE SET
		  status = CASE WHEN item_progress.status = 'complete' THEN 'complete' ELSE EXCLUDED.status END,
		  score = GREATEST(item_progress.score, EXCLUDED.score),
		  updated_at = now()`, userID, team, training, module, item, status, score)
	return err
}

func findItem(m *content.Module, kind, id string) *content.Item {
	for i := range m.Items {
		if m.Items[i].Kind == kind && m.Items[i].ID == id {
			return &m.Items[i]
		}
	}
	return nil
}

func (s *Service) Reading(ctx context.Context, u *auth.User, team, training, module, item string) (string, string, error) {
	_, t, _, err := s.Program(u, team, training)
	if err != nil {
		return "", "", err
	}
	m, err := s.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return "", "", err
	}
	it := findItem(m, "reading", item)
	if it == nil {
		return "", "", apperr.Wrap(apperr.NotFound, "reading not found")
	}
	b, err := os.ReadFile(it.Path)
	if err != nil {
		return "", "", err
	}
	return it.Title, string(b), nil
}

func (s *Service) MarkRead(ctx context.Context, u *auth.User, team, training, module, item string) error {
	if _, _, err := s.Reading(ctx, u, team, training, module, item); err != nil {
		return err
	}
	return s.SetItem(ctx, u.ID, team, training, module, item, "complete", 1)
}

func seedFor(userID int64, team, training, module string) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d/%s/%s/%s", userID, team, training, module)
	return h.Sum64()
}

func (s *Service) quizModule(ctx context.Context, u *auth.User, team, training, module string) (*content.Training, string, *content.Module, error) {
	_, t, sha, err := s.Program(u, team, training)
	if err != nil {
		return nil, "", nil, err
	}
	m, err := s.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, "", nil, err
	}
	if findItem(m, "quiz", "quiz") == nil {
		return nil, "", nil, apperr.Wrap(apperr.NotFound, "this module has no quiz")
	}
	return t, sha, m, nil
}

func (s *Service) Quiz(ctx context.Context, u *auth.User, team, training, module string) (*QuizView, error) {
	t, _, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	prog, err := s.progress(ctx, u.ID, team, t.ID)
	if err != nil {
		return nil, err
	}
	status := prog[module+"/quiz"]
	if status == "" {
		status = "new"
	}
	return &QuizView{PassThreshold: m.Quiz.PassThreshold, Questions: PublicQuiz(m.Quiz, seedFor(u.ID, team, training, module)), Status: status}, nil
}

func (s *Service) SubmitQuiz(ctx context.Context, u *auth.User, team, training, module string, answers map[string]json.RawMessage) (*Result, error) {
	t, sha, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	res := Score(m.Quiz, answers)
	stored, _ := json.Marshal(answers)
	if _, err := s.DB.Exec(ctx, `INSERT INTO quiz_attempts (user_id, team, training, module, sha, answers, score, max_score, passed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, u.ID, team, t.ID, module, sha, stored, res.Score, res.Max, res.Passed); err != nil {
		return nil, err
	}
	status := "in_progress"
	if res.Passed {
		status = "complete"
	}
	if err := s.SetItem(ctx, u.ID, team, t.ID, module, "quiz", status, res.Percent); err != nil {
		return nil, err
	}
	return &res, nil
}
```

- [ ] **Step 4: Implement the HTTP handlers**

`internal/learn/http.go`:
```go
package learn

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	const p = "/api/programs/{team}/{training}"
	r.Get("/api/programs", func(w http.ResponseWriter, r *http.Request) {
		cards, err := s.Programs(r.Context(), auth.UserFrom(r.Context()))
		reply(w, cards, err)
	})
	r.Get(p, func(w http.ResponseWriter, r *http.Request) {
		o, err := s.Outline(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"))
		reply(w, o, err)
	})
	r.Get(p+"/assets/*", func(w http.ResponseWriter, r *http.Request) {
		_, t, _, err := s.Program(auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"))
		if err != nil {
			httpx.Error(w, err)
			return
		}
		rel := filepath.FromSlash(chi.URLParam(r, "*"))
		if !filepath.IsLocal(rel) {
			httpx.Error(w, apperr.Wrap(apperr.Invalid, "bad asset path"))
			return
		}
		http.ServeFile(w, r, filepath.Join(t.Dir, "assets", rel))
	})
	r.Get(p+"/modules/{module}/reading/{item}", func(w http.ResponseWriter, r *http.Request) {
		title, md, err := s.Reading(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), param(r, "item"))
		reply(w, map[string]string{"title": title, "markdown": md}, err)
	})
	r.Post(p+"/modules/{module}/reading/{item}/read", func(w http.ResponseWriter, r *http.Request) {
		err := s.MarkRead(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), param(r, "item"))
		reply(w, nil, err)
	})
	r.Get(p+"/modules/{module}/quiz", func(w http.ResponseWriter, r *http.Request) {
		q, err := s.Quiz(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"))
		reply(w, q, err)
	})
	r.Post(p+"/modules/{module}/quiz/attempts", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answers map[string]json.RawMessage `json:"answers"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		res, err := s.SubmitQuiz(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"), param(r, "module"), body.Answers)
		reply(w, res, err)
	})
}

func param(r *http.Request, k string) string { return chi.URLParam(r, k) }

func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err != nil:
		httpx.Error(w, err)
	case v == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.JSON(w, http.StatusOK, v)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/learn/...`
Expected: `ok crucible/internal/learn`

- [ ] **Step 6: Commit**

```bash
git add internal/learn
git commit -m "feat: learning service with linear gating, reading, quizzes and progress"
```

---

### Task 11: Agent protocol and hub (server side of laptop agents)

**Files:**
- Create: `internal/agentproto/proto.go`, `internal/agenthub/hub.go`, `internal/agenthub/hub_test.go`

**Interfaces:**
- Produces:
  - `agentproto.Msg{Type, ID, LabID, Service, Compose string; Data []byte; Env map[string]string; TimeoutMS int64; Cols, Rows, ExitCode int; TimedOut bool; Error string}`.
  - Message type constants: `TProvision, TDestroy, TRunScript, TPTYOpen, TPTYData, TPTYResize, TPTYClose, TResult`.
  - `agentproto.MaxOutput = 64 << 10`.
  - Hub API:
    - `agenthub.New() *Hub`
    - `(*Hub).Serve(w, r, userID int64)` (blocks for the connection's lifetime)
    - `(*Hub).Online(userID) bool`
    - `(*Hub).Call(ctx, userID, agentproto.Msg) (agentproto.Msg, error)`
    - `(*Hub).OpenPTY(ctx, userID int64, labID, service string, cols, rows int) (*PTY, error)`
  - `*agenthub.PTY` implements `Read`, `Write`, `Close`, `Resize(cols, rows int) error`.
  - `agenthub.ErrOffline`.
- Protocol rules:
  - Requests carry an `ID`; the agent answers with `TResult` and the same `ID` (`Error` set on failure).
  - For a PTY, the request `ID` is also the PTY session id used by later `pty_data` / `pty_resize` / `pty_close`.

- [ ] **Step 1: Add dependency**

```bash
go get github.com/coder/websocket@latest
```

- [ ] **Step 2: Write the failing test**

`internal/agenthub/hub_test.go`:
```go
package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

func server(t *testing.T, h *Hub) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, 7) }))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// fakeAgent answers run_script with exit code 3 and echoes PTY input back.
func fakeAgent(ctx context.Context, ws *websocket.Conn) {
	send := func(m ap.Msg) { b, _ := json.Marshal(m); _ = ws.Write(ctx, websocket.MessageText, b) }
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m ap.Msg
		_ = json.Unmarshal(data, &m)
		switch m.Type {
		case ap.TRunScript:
			send(ap.Msg{Type: ap.TResult, ID: m.ID, ExitCode: 3, Data: []byte("out")})
		case ap.TPTYOpen:
			send(ap.Msg{Type: ap.TResult, ID: m.ID})
		case ap.TPTYData:
			send(ap.Msg{Type: ap.TPTYData, ID: m.ID, Data: m.Data})
		}
	}
}

func TestCallAndPTYRoundTrip(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	go fakeAgent(ctx, ws)
	waitFor(t, func() bool { return h.Online(7) })

	res, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript})
	if err != nil || res.ExitCode != 3 || string(res.Data) != "out" {
		t.Fatalf("call: %+v %v", res, err)
	}

	p, err := h.OpenPTY(ctx, 7, "lab1", "box", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := p.Read(buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("pty echo: %q %v", buf[:n], err)
	}

	_ = ws.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, func() bool { return !h.Online(7) })
	if _, err := p.Read(buf); err != io.EOF {
		t.Fatalf("pty read after disconnect: %v", err)
	}
	if _, err := h.Call(ctx, 7, ap.Msg{Type: ap.TRunScript}); !errors.Is(err, ErrOffline) {
		t.Fatalf("call while offline: %v", err)
	}
}

func TestPendingCallFailsFastOnDisconnect(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server(t, h), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { // read one request, then drop the connection without answering
		_, _, _ = ws.Read(ctx)
		_ = ws.CloseNow()
	}()
	waitFor(t, func() bool { return h.Online(7) })
	start := time.Now()
	_, err = h.Call(ctx, 7, ap.Msg{Type: ap.TProvision})
	if !errors.Is(err, ErrOffline) || time.Since(start) > 2*time.Second {
		t.Fatalf("want fast ErrOffline, got %v after %v", err, time.Since(start))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/agenthub/...`
Expected: FAIL (`New` undefined).

- [ ] **Step 4: Implement the protocol**

`internal/agentproto/proto.go`:
```go
// Package agentproto is the JSON message format between crucible-api and crucible-agent.
package agentproto

const (
	TProvision = "provision"  // API → agent: Data = lab bundle (tar.gz), Compose = compose file name
	TDestroy   = "destroy"    // API → agent
	TRunScript = "run_script" // API → agent: Service, Data = script, Env, TimeoutMS
	TPTYOpen   = "pty_open"   // API → agent: ID = PTY session id, Service, Cols, Rows
	TPTYData   = "pty_data"   // both ways: ID = PTY session id
	TPTYResize = "pty_resize" // API → agent
	TPTYClose  = "pty_close"  // both ways
	TResult    = "result"     // agent → API: answer to a request with the same ID
)

// MaxOutput caps script output kept and sent back (spec: 64 KiB).
const MaxOutput = 64 << 10

type Msg struct {
	Type      string            `json:"type"`
	ID        string            `json:"id,omitempty"`
	LabID     string            `json:"lab_id,omitempty"`
	Service   string            `json:"service,omitempty"`
	Compose   string            `json:"compose,omitempty"`
	Data      []byte            `json:"data,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	ExitCode  int               `json:"exit_code"`
	TimedOut  bool              `json:"timed_out,omitempty"`
	Error     string            `json:"error,omitempty"`
}
```

- [ ] **Step 5: Implement the hub**

`internal/agenthub/hub.go`:
```go
// Package agenthub keeps one WebSocket per user's laptop agent and lets the API call it.
package agenthub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

var ErrOffline = errors.New("agent offline")

type Hub struct {
	mu     sync.Mutex
	agents map[int64]*conn
}

func New() *Hub { return &Hub{agents: map[int64]*conn{}} }

type conn struct {
	ws      *websocket.Conn
	ctx     context.Context
	mu      sync.Mutex
	pending map[string]chan ap.Msg
	ptys    map[string]*PTY
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *conn) send(ctx context.Context, m ap.Msg) error {
	b, _ := json.Marshal(m)
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// Serve upgrades the request and serves the agent until it disconnects. A newer agent replaces an older one.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, userID int64) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{ws: ws, ctx: ctx, pending: map[string]chan ap.Msg{}, ptys: map[string]*PTY{}}

	h.mu.Lock()
	if old := h.agents[userID]; old != nil {
		_ = old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer agent")
	}
	h.agents[userID] = c
	h.mu.Unlock()

	defer func() {
		cancel()
		h.mu.Lock()
		if h.agents[userID] == c {
			delete(h.agents, userID)
		}
		h.mu.Unlock()
		c.closeAll()
		_ = ws.CloseNow()
	}()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m ap.Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		c.dispatch(m)
	}
}

func (c *conn) dispatch(m ap.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case ap.TResult:
		if ch := c.pending[m.ID]; ch != nil {
			delete(c.pending, m.ID)
			ch <- m // buffered, never blocks
		}
	case ap.TPTYData:
		if p := c.ptys[m.ID]; p != nil {
			p.push(m.Data)
		}
	case ap.TPTYClose:
		if p := c.ptys[m.ID]; p != nil {
			delete(c.ptys, m.ID)
			p.closeRemote()
		}
	}
}

func (c *conn) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	for id, p := range c.ptys {
		delete(c.ptys, id)
		p.closeRemote()
	}
}

func (h *Hub) get(userID int64) (*conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.agents[userID]; c != nil {
		return c, nil
	}
	return nil, ErrOffline
}

func (h *Hub) Online(userID int64) bool {
	_, err := h.get(userID)
	return err == nil
}

// Call sends a request and waits for its result. A disconnect fails pending calls immediately.
func (h *Hub) Call(ctx context.Context, userID int64, m ap.Msg) (ap.Msg, error) {
	c, err := h.get(userID)
	if err != nil {
		return ap.Msg{}, err
	}
	if m.ID == "" {
		m.ID = newID()
	}
	ch := make(chan ap.Msg, 1)
	c.mu.Lock()
	c.pending[m.ID] = ch
	c.mu.Unlock()
	if err := c.send(ctx, m); err != nil {
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
		return ap.Msg{}, ErrOffline
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return ap.Msg{}, ErrOffline
		}
		if res.Error != "" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
		return ap.Msg{}, ctx.Err()
	}
}

func (h *Hub) OpenPTY(ctx context.Context, userID int64, labID, service string, cols, rows int) (*PTY, error) {
	c, err := h.get(userID)
	if err != nil {
		return nil, err
	}
	p := &PTY{c: c, id: newID(), data: make(chan []byte, 256), closed: make(chan struct{})}
	c.mu.Lock()
	c.ptys[p.id] = p
	c.mu.Unlock()
	if _, err := h.Call(ctx, userID, ap.Msg{Type: ap.TPTYOpen, ID: p.id, LabID: labID, Service: service, Cols: cols, Rows: rows}); err != nil {
		c.mu.Lock()
		delete(c.ptys, p.id)
		c.mu.Unlock()
		return nil, err
	}
	return p, nil
}

// PTY is a terminal session on the agent, read/written like a stream.
type PTY struct {
	c      *conn
	id     string
	data   chan []byte
	buf    []byte
	closed chan struct{}
	once   sync.Once
}

// push is called with conn.mu held; it drops output if the reader is 256 chunks behind.
// ponytail: drop-on-overflow instead of back-pressure; fine for interactive shells, revisit for bulk output.
func (p *PTY) push(b []byte) {
	select {
	case p.data <- b:
	default:
	}
}

func (p *PTY) closeRemote() { p.once.Do(func() { close(p.closed) }) }

func (p *PTY) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		select {
		case d := <-p.data:
			p.buf = d
		case <-p.closed:
			select {
			case d := <-p.data:
				p.buf = d
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *PTY) Write(b []byte) (int, error) {
	if err := p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYData, ID: p.id, Data: b}); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *PTY) Resize(cols, rows int) error {
	return p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYResize, ID: p.id, Cols: cols, Rows: rows})
}

func (p *PTY) Close() error {
	p.c.mu.Lock()
	delete(p.c.ptys, p.id)
	p.c.mu.Unlock()
	p.closeRemote()
	return p.c.send(p.c.ctx, ap.Msg{Type: ap.TPTYClose, ID: p.id})
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/agenthub/...`
Expected: `ok crucible/internal/agenthub`

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/agentproto internal/agenthub
git commit -m "feat: agent protocol and hub with fail-fast calls and PTY streams"
```

---

### Task 12: `crucible-agent` (WebSocket client + docker compose executor)

**Files:**
- Create: `internal/agent/exec.go`, `internal/agent/compose.go`, `internal/agent/client.go`
- Create: `internal/agent/exec_test.go`, `internal/agent/client_test.go`
- Create: `cmd/crucible-agent/main.go`

**Interfaces:**
- Consumes: `agentproto`, and `agenthub` in tests.
- Produces:
  - `agent.Executor` interface, implemented by `agent.Compose{Dir string}`, with methods:
    - `Provision(ctx, labID string, bundle []byte, compose string) error`
    - `Destroy(ctx, labID string) error`
    - `RunScript(ctx, labID, service string, script []byte, env map[string]string, timeout time.Duration) (agentproto.Msg, error)`
    - `StartPTY(labID, service string, cols, rows int) (Session, error)`
    - `DestroyAll(ctx)`
  - `agent.Session` interface: `io.ReadWriteCloser` + `Resize(cols, rows int) error`.
  - `agent.Client{Server, Token string; Exec Executor; Log *slog.Logger}` with `Run(ctx) error`. It reconnects every 3s, and on shutdown calls `Exec.DestroyAll`.
  - `agent.RunLimited(ctx, *exec.Cmd, timeout) (agentproto.Msg, error)`.
  - `agent.Untar(bundle []byte, dir string) error`, which rejects paths that escape `dir`.

- [ ] **Step 1: Add dependency**

```bash
go get github.com/creack/pty@latest
```

- [ ] **Step 2: Write the failing tests**

`internal/agent/exec_test.go`:
```go
package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	ap "crucible/internal/agentproto"
)

func TestRunLimitedExitCodeAndOutput(t *testing.T) {
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "echo hi; exit 3"), 5*time.Second)
	if err != nil || res.ExitCode != 3 || strings.TrimSpace(string(res.Data)) != "hi" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunLimitedTimesOut(t *testing.T) {
	start := time.Now()
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "sleep 10"), 200*time.Millisecond)
	if err != nil || !res.TimedOut || res.ExitCode != -1 || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v %v after %v", res, err, time.Since(start))
	}
}

func TestRunLimitedCapsOutput(t *testing.T) {
	res, err := RunLimited(context.Background(), exec.Command("sh", "-c", "yes forge | head -c 500000"), 5*time.Second)
	if err != nil || len(res.Data) != ap.MaxOutput {
		t.Fatalf("len %d err %v", len(res.Data), err)
	}
}

func TestUntarRejectsEscapes(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.sh", Mode: 0o755, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	if err := Untar(buf.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("got %v", err)
	}
}
```

`internal/agent/client_test.go`:
```go
package agent_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"crucible/internal/agent"
	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
)

type echo struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (e *echo) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echo) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *echo) Close() error                { return e.w.Close() }
func (e *echo) Resize(int, int) error       { return nil }

type fakeExec struct {
	mu              sync.Mutex
	provisioned     map[string]string
	destroyAllCalls int
}

func (f *fakeExec) Provision(_ context.Context, labID string, _ []byte, compose string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisioned[labID] = compose
	return nil
}
func (f *fakeExec) Destroy(context.Context, string) error { return nil }
func (f *fakeExec) RunScript(_ context.Context, _, service string, script []byte, env map[string]string, _ time.Duration) (ap.Msg, error) {
	code := 1
	if string(script) == "ok" && env["CRUCIBLE_ANSWER"] == "42" {
		code = 0
	}
	return ap.Msg{ExitCode: code, Data: []byte("ran in " + service)}, nil
}
func (f *fakeExec) StartPTY(string, string, int, int) (agent.Session, error) {
	r, w := io.Pipe()
	return &echo{r, w}, nil
}
func (f *fakeExec) DestroyAll(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyAllCalls++
}

func TestClientServesHubRequests(t *testing.T) {
	hub := agenthub.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "nope", 401)
			return
		}
		hub.Serve(w, r, 1)
	}))
	defer srv.Close()

	fx := &fakeExec{provisioned: map[string]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = (&agent.Client{Server: srv.URL, Token: "secret", Exec: fx, Log: slog.Default()}).Run(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !hub.Online(1); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if _, err := hub.Call(cctx, 1, ap.Msg{Type: ap.TProvision, LabID: "lab1", Compose: "compose.yaml"}); err != nil {
		t.Fatal(err)
	}
	res, err := hub.Call(cctx, 1, ap.Msg{Type: ap.TRunScript, LabID: "lab1", Service: "web", Data: []byte("ok"), Env: map[string]string{"CRUCIBLE_ANSWER": "42"}})
	if err != nil || res.ExitCode != 0 || string(res.Data) != "ran in web" {
		t.Fatalf("run_script: %+v %v", res, err)
	}
	p, err := hub.OpenPTY(cctx, 1, "lab1", "shell", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write([]byte("ls\n"))
	buf := make([]byte, 16)
	n, _ := p.Read(buf)
	if string(buf[:n]) != "ls\n" {
		t.Fatalf("pty echo got %q", buf[:n])
	}

	cancel()
	<-done
	if fx.destroyAllCalls != 2 {
		t.Fatalf("agent must clean up on start and on shutdown, got %d calls", fx.destroyAllCalls)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/...`
Expected: FAIL (undefined `RunLimited`, `Client`, `Session`).

- [ ] **Step 4: Implement process helpers**

`internal/agent/exec.go`:
```go
package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ap "crucible/internal/agentproto"
)

type capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := ap.MaxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}

// RunLimited runs cmd with a timeout and keeps at most MaxOutput bytes of combined output.
// ponytail: killing `docker compose exec` stops the client; the in-container process may linger until the lab is destroyed.
func RunLimited(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (ap.Msg, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return ap.Msg{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			return ap.Msg{ExitCode: ee.ExitCode(), Data: out.Bytes()}, nil
		case err != nil:
			return ap.Msg{}, err
		}
		return ap.Msg{Data: out.Bytes()}, nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ap.Msg{ExitCode: -1, TimedOut: true, Data: append(out.Bytes(), "\n[crucible] script timed out"...)}, nil
	}
}

// Untar unpacks a gzip'd tar into dir, refusing any entry that would land outside it.
func Untar(bundle []byte, dir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimSuffix(h.Name, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path %q in lab bundle", h.Name)
		}
		p := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, 64<<20))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}
```

- [ ] **Step 5: Implement the compose executor**

`internal/agent/compose.go`:
```go
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"

	ap "crucible/internal/agentproto"
)

type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type Executor interface {
	Provision(ctx context.Context, labID string, bundle []byte, compose string) error
	Destroy(ctx context.Context, labID string) error
	RunScript(ctx context.Context, labID, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error)
	StartPTY(labID, service string, cols, rows int) (Session, error)
	DestroyAll(ctx context.Context)
}

// Compose runs each lab as a docker compose project named crucible-<labID> under Dir/<labID>.
type Compose struct{ Dir string }

const composeMarker = ".crucible-compose"

func (c Compose) labDir(labID string) string { return filepath.Join(c.Dir, labID) }

func (c Compose) args(labID string, rest ...string) []string {
	name, _ := os.ReadFile(filepath.Join(c.labDir(labID), composeMarker))
	file := filepath.Join(c.labDir(labID), strings.TrimSpace(string(name)))
	return append([]string{"compose", "-p", "crucible-" + labID, "-f", file}, rest...)
}

func (c Compose) Provision(ctx context.Context, labID string, bundle []byte, compose string) error {
	if !filepath.IsLocal(labID) || !filepath.IsLocal(compose) {
		return errors.New("invalid lab id or compose file name")
	}
	dir := c.labDir(labID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := Untar(bundle, dir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, composeMarker), []byte(compose), 0o644); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "docker", c.args(labID, "up", "-d", "--wait")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, tail(out))
	}
	return nil
}

func (c Compose) Destroy(ctx context.Context, labID string) error {
	dir := c.labDir(labID)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	out, err := exec.CommandContext(ctx, "docker", c.args(labID, "down", "-v", "--remove-orphans")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down: %w: %s", err, tail(out))
	}
	return os.RemoveAll(dir)
}

func (c Compose) DestroyAll(ctx context.Context) {
	entries, _ := os.ReadDir(c.Dir)
	for _, e := range entries {
		if e.IsDir() {
			_ = c.Destroy(ctx, e.Name())
		}
	}
}

func (c Compose) RunScript(ctx context.Context, labID, service string, script []byte, env map[string]string, timeout time.Duration) (ap.Msg, error) {
	args := c.args(labID, "exec", "-T")
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, service, "sh", "-s")
	cmd := exec.Command("docker", args...)
	cmd.Stdin = bytes.NewReader(script)
	return RunLimited(ctx, cmd, timeout)
}

func (c Compose) StartPTY(labID, service string, cols, rows int) (Session, error) {
	cmd := exec.Command("docker", c.args(labID, "exec", service, "sh", "-c",
		"if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi")...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &ptySession{f: f, cmd: cmd}, nil
}

type ptySession struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
func (p *ptySession) Close() error {
	_ = p.f.Close()
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	return nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "…" + s[len(s)-2000:]
	}
	return s
}
```

- [ ] **Step 6: Implement the client**

`internal/agent/client.go`:
```go
// Package agent is the laptop side of local labs: it connects out to Crucible and runs docker compose.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	ap "crucible/internal/agentproto"
)

type Client struct {
	Server string // e.g. https://crucible.example.com
	Token  string
	Exec   Executor
	Log    *slog.Logger
}

// Run keeps the agent connected until ctx ends. It tears down local labs on start (crash leftovers) and on exit.
func (c *Client) Run(ctx context.Context) error {
	c.Exec.DestroyAll(ctx) // leftovers from a crashed previous run
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c.Log.Info("cooling the forge: removing local labs")
		c.Exec.DestroyAll(cleanup)
	}()
	for {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.Log.Warn("disconnected from Crucible, retrying in 3s", "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) runOnce(ctx context.Context) error {
	url := "ws" + strings.TrimPrefix(c.Server, "http") + "/api/agent/ws"
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20) // lab bundles
	c.Log.Info("connected. The forge is lit", "server", c.Server)

	s := &session{ws: ws, exec: c.Exec, ptys: map[string]Session{}}
	defer s.closeAll()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var m ap.Msg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type { // PTY traffic is handled in order; everything else may take long, so it runs concurrently
		case ap.TPTYData:
			if p := s.pty(m.ID); p != nil {
				_, _ = p.Write(m.Data)
			}
		case ap.TPTYResize:
			if p := s.pty(m.ID); p != nil {
				_ = p.Resize(m.Cols, m.Rows)
			}
		case ap.TPTYClose:
			if p := s.drop(m.ID); p != nil {
				_ = p.Close()
			}
		default:
			go s.handle(ctx, m)
		}
	}
}

type session struct {
	ws   *websocket.Conn
	exec Executor
	mu   sync.Mutex
	ptys map[string]Session
}

func (s *session) send(ctx context.Context, m ap.Msg) {
	b, _ := json.Marshal(m)
	_ = s.ws.Write(ctx, websocket.MessageText, b)
}

func (s *session) reply(ctx context.Context, id string, res ap.Msg, err error) {
	res.Type, res.ID = ap.TResult, id
	if err != nil {
		res.Error = err.Error()
	}
	s.send(ctx, res)
}

func (s *session) pty(id string) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptys[id]
}

func (s *session) drop(id string) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.ptys[id]
	delete(s.ptys, id)
	return p
}

func (s *session) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.ptys {
		_ = p.Close()
		delete(s.ptys, id)
	}
}

func (s *session) handle(ctx context.Context, m ap.Msg) {
	switch m.Type {
	case ap.TProvision:
		s.reply(ctx, m.ID, ap.Msg{}, s.exec.Provision(ctx, m.LabID, m.Data, m.Compose))
	case ap.TDestroy:
		s.reply(ctx, m.ID, ap.Msg{}, s.exec.Destroy(ctx, m.LabID))
	case ap.TRunScript:
		res, err := s.exec.RunScript(ctx, m.LabID, m.Service, m.Data, m.Env, time.Duration(m.TimeoutMS)*time.Millisecond)
		s.reply(ctx, m.ID, res, err)
	case ap.TPTYOpen:
		p, err := s.exec.StartPTY(m.LabID, m.Service, m.Cols, m.Rows)
		if err != nil {
			s.reply(ctx, m.ID, ap.Msg{}, err)
			return
		}
		s.mu.Lock()
		s.ptys[m.ID] = p
		s.mu.Unlock()
		s.reply(ctx, m.ID, ap.Msg{}, nil)
		go s.pump(ctx, m.ID, p)
	}
}

func (s *session) pump(ctx context.Context, id string, p Session) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			s.send(ctx, ap.Msg{Type: ap.TPTYData, ID: id, Data: slices.Clone(buf[:n])})
		}
		if err != nil {
			s.send(ctx, ap.Msg{Type: ap.TPTYClose, ID: id})
			s.drop(id)
			return
		}
	}
}
```

`cmd/crucible-agent/main.go`:
```go
// Command crucible-agent runs local labs on a trainee's laptop (macOS, Linux, or Windows via WSL2).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"crucible/internal/agent"
)

func main() {
	home, _ := os.UserHomeDir()
	server := flag.String("server", os.Getenv("CRUCIBLE_SERVER"), "Crucible URL, e.g. https://crucible.example.com")
	token := flag.String("token", os.Getenv("CRUCIBLE_TOKEN"), "pairing token from the “Connect your laptop” page")
	labs := flag.String("labs-dir", filepath.Join(home, ".crucible", "labs"), "where lab files are unpacked")
	flag.Parse()
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: crucible-agent --server URL --token TOKEN")
		os.Exit(2)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "crucible-agent needs Docker with the compose plugin on your PATH")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &agent.Client{Server: strings.TrimRight(*server, "/"), Token: *token, Exec: agent.Compose{Dir: *labs}, Log: slog.Default()}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/agent/... && go build ./cmd/crucible-agent`
Expected: `ok crucible/internal/agent`, and the build succeeds.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/agent cmd/crucible-agent
git commit -m "feat: crucible-agent with compose executor, capped scripts and PTY streaming"
```

---
### Task 13: Lab model, timing, bundles, Runner interface, local runner

**Files:**
- Create: `internal/labs/model.go`, `internal/labs/bundle.go`, `internal/labs/local.go`, `internal/labs/model_test.go`

**Interfaces:**
- Consumes: `content.Lab`, `config.LabDefaults`, `agenthub.Hub`, `agentproto`.
- Produces:
  - Types:
    - `labs.State`, with constants `Provisioning, Ready, Destroying, Destroyed, Failed`
    - `labs.Instance{ID string; UserID int64; Team, Training, Module, SHA, Runtime string; State State; Error string; CreatedAt time.Time; ReadyAt, EndsAt *time.Time; LimitReason, EndReason string; LastActivityAt time.Time; TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration; Extended bool}`
    - `labs.Limit{At time.Time; Reason string}`
    - `labs.Timing{TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration}`
  - Functions:
    - `labs.EffectiveEnd(limits ...Limit) Limit`: earliest non-zero.
    - `labs.ResolveTiming(lab *content.Lab, d config.LabDefaults) Timing`: lab value > program default > built-in default.
    - `labs.Bundle(labDir string) ([]byte, error)`: tar.gz of the lab dir without `tasks/ checks/ setup/ hints/ lab.yaml`.
  - Runner:
    - `labs.Runner` interface:
      - `Available(*Instance) error`
      - `Provision(ctx, *Instance, bundle []byte, compose string) error`
      - `OpenPTY(ctx, *Instance, service string, cols, rows int) (PTY, error)`
      - `RunScript(ctx, *Instance, ScriptSpec) (ScriptResult, error)`
      - `Destroy(ctx, *Instance) error`
    - `labs.PTY` interface: `io.ReadWriteCloser` + `Resize`.
    - `labs.ScriptSpec{Service string; Script []byte; Env map[string]string; Timeout time.Duration}`
    - `labs.ScriptResult{ExitCode int; Output string; TimedOut bool}`
    - `labs.LocalRunner{Hub *agenthub.Hub}`
  - The spec §3 `Terminals()` method is not on the interface. Terminals come from the lab manifest, the same for every runtime.

- [ ] **Step 1: Write the failing test**

`internal/labs/model_test.go`:
```go
package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

func TestEffectiveEndPicksEarliest(t *testing.T) {
	base := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	got := EffectiveEnd(Limit{base.Add(2 * time.Hour), "ttl"}, Limit{}, Limit{base.Add(time.Hour), "schedule"})
	if got.Reason != "schedule" || !got.At.Equal(base.Add(time.Hour)) {
		t.Fatalf("%+v", got)
	}
}

func TestResolveTimingPrecedence(t *testing.T) {
	lab := &content.Lab{IdleWarning: yamlx.Duration(5 * time.Minute)}
	d := config.LabDefaults{TTL: yamlx.Duration(4 * time.Hour), MaxExtension: yamlx.Duration(30 * time.Minute)}
	tm := ResolveTiming(lab, d)
	if tm.TTL != 4*time.Hour || tm.IdleTimeout != 30*time.Minute || tm.MaxExtension != 30*time.Minute {
		t.Fatalf("program defaults: %+v", tm)
	}
	lab.TTL, lab.IdleTimeout = yamlx.Duration(time.Hour), yamlx.Duration(4*time.Minute)
	tm = ResolveTiming(lab, d)
	if tm.TTL != time.Hour || tm.IdleTimeout != 4*time.Minute || tm.IdleWarning != 2*time.Minute {
		t.Fatalf("lab overrides + warning clamp: %+v", tm)
	}
}

func TestBundleExcludesSecrets(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"compose.yaml", "conf/nginx.conf", "lab.yaml", "checks/1.sh", "setup/1.sh", "tasks/1.md", "hints/1.md"} {
		_ = os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	b, err := Bundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(bytes.NewReader(b))
	tr := tar.NewReader(gz)
	var files []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if h.Typeflag == tar.TypeReg {
			files = append(files, h.Name)
		}
	}
	sort.Strings(files)
	if len(files) != 2 || files[0] != "compose.yaml" || files[1] != "conf/nginx.conf" {
		t.Fatalf("bundle contains %v", files)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/labs/...`
Expected: FAIL (undefined `EffectiveEnd`, `ResolveTiming`, `Bundle`).

- [ ] **Step 3: Implement**

`internal/labs/model.go`:
```go
// Package labs runs lab instances: lifecycle, tasks, checks, setup scripts, hints, timers and idle detection.
package labs

import (
	"context"
	"io"
	"time"

	"crucible/internal/config"
	"crucible/internal/content"
)

type State string

const (
	Provisioning State = "provisioning"
	Ready        State = "ready"
	Destroying   State = "destroying"
	Destroyed    State = "destroyed"
	Failed       State = "failed"
)

type Instance struct {
	ID                                       string
	UserID                                   int64
	Team, Training, Module, SHA, Runtime     string
	State                                    State
	Error                                    string
	CreatedAt                                time.Time
	ReadyAt, EndsAt                          *time.Time
	LimitReason, EndReason                   string
	LastActivityAt                           time.Time
	TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration
	Extended                                 bool
}

// Limit is one candidate end time for a lab (TTL now; schedule window and budget cap arrive in M3/M6).
type Limit struct {
	At     time.Time
	Reason string
}

func EffectiveEnd(limits ...Limit) Limit {
	var best Limit
	for _, l := range limits {
		if l.At.IsZero() {
			continue
		}
		if best.At.IsZero() || l.At.Before(best.At) {
			best = l
		}
	}
	return best
}

type Timing struct{ TTL, IdleTimeout, IdleWarning, MaxExtension time.Duration }

func ResolveTiming(lab *content.Lab, d config.LabDefaults) Timing {
	t := Timing{TTL: 2 * time.Hour, IdleTimeout: 30 * time.Minute, IdleWarning: lab.IdleWarning.D(), MaxExtension: d.MaxExtension.D()}
	if d.TTL > 0 {
		t.TTL = d.TTL.D()
	}
	if lab.TTL > 0 {
		t.TTL = lab.TTL.D()
	}
	if d.IdleTimeout > 0 {
		t.IdleTimeout = d.IdleTimeout.D()
	}
	if lab.IdleTimeout > 0 {
		t.IdleTimeout = lab.IdleTimeout.D()
	}
	if t.IdleWarning <= 0 || t.IdleWarning >= t.IdleTimeout {
		t.IdleWarning = t.IdleTimeout / 2
	}
	return t
}

type ScriptSpec struct {
	Service string
	Script  []byte
	Env     map[string]string
	Timeout time.Duration
}

type ScriptResult struct {
	ExitCode int
	Output   string
	TimedOut bool
}

type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Runner hosts lab environments for one runtime (local now; cluster in M4, aws in M6).
type Runner interface {
	Available(inst *Instance) error
	Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error
	OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error)
	RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error)
	Destroy(ctx context.Context, inst *Instance) error
}
```

`internal/labs/bundle.go`:
```go
package labs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Files the trainee's environment must never see: instructions, checks, setups and hints are sent per call.
var bundleSkip = map[string]bool{"tasks": true, "checks": true, "setup": true, "hints": true, "lab.yaml": true}

func Bundle(labDir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(labDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(labDir, p)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if bundleSkip[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

`internal/labs/local.go`:
```go
package labs

import (
	"context"

	"crucible/internal/agenthub"
	ap "crucible/internal/agentproto"
	"crucible/internal/apperr"
)

// LocalRunner runs labs on the trainee's laptop through crucible-agent.
type LocalRunner struct{ Hub *agenthub.Hub }

var errAgentOffline = apperr.Wrap(apperr.Unavailable, "your laptop agent is not connected. Open “Connect your laptop”")

func (l LocalRunner) Available(inst *Instance) error {
	if !l.Hub.Online(inst.UserID) {
		return errAgentOffline
	}
	return nil
}

func (l LocalRunner) Provision(ctx context.Context, inst *Instance, bundle []byte, compose string) error {
	_, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TProvision, LabID: inst.ID, Data: bundle, Compose: compose})
	return err
}

func (l LocalRunner) Destroy(ctx context.Context, inst *Instance) error {
	_, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TDestroy, LabID: inst.ID})
	return err
}

func (l LocalRunner) RunScript(ctx context.Context, inst *Instance, s ScriptSpec) (ScriptResult, error) {
	res, err := l.Hub.Call(ctx, inst.UserID, ap.Msg{Type: ap.TRunScript, LabID: inst.ID, Service: s.Service,
		Data: s.Script, Env: s.Env, TimeoutMS: s.Timeout.Milliseconds()})
	if err != nil {
		return ScriptResult{}, err
	}
	return ScriptResult{ExitCode: res.ExitCode, Output: string(res.Data), TimedOut: res.TimedOut}, nil
}

func (l LocalRunner) OpenPTY(ctx context.Context, inst *Instance, service string, cols, rows int) (PTY, error) {
	return l.Hub.OpenPTY(ctx, inst.UserID, inst.ID, service, cols, rows)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/labs/...`
Expected: `ok crucible/internal/labs`

- [ ] **Step 5: Commit**

```bash
git add internal/labs
git commit -m "feat: lab model, timing resolution, lab bundles and local runner"
```

---

### Task 14: Lab service (lifecycle, tasks, checks, setup, hints, timer, idle)

**Files:**
- Create: `internal/labs/service.go`, `internal/labs/service_test.go`

**Interfaces:**
- Consumes:
  - `learn.Service` (`State`, `Program`, `EnsureUnlocked`, `SetItem`)
  - `labs.Runner`, `apperr`, `agenthub.ErrOffline`
  - Tables `lab_instances`, `lab_events`, `lab_task_progress`, `check_runs`, `setup_runs`, `hint_reveals`.
- Produces:
  - `labs.Service{DB; Learn *learn.Service; Runners map[string]Runner; Now func() time.Time; Log *slog.Logger}` with methods:
    - `ModuleLab(ctx, u, team, training, module) (*ModuleLab, error)`
    - `Start(ctx, u, team, training, module) (*View, error)`
    - `Get(ctx, u, labID) (*View, error)`
    - `OpenTask(ctx, u, labID, taskID) (*TaskDetail, error)`
    - `Check(ctx, u, labID, taskID, answer) (*CheckResult, error)`
    - `RevealHint(ctx, u, labID, taskID) (*HintResult, error)`
    - `ResetTask(ctx, u, labID, taskID) (*View, error)`
    - `Skip(ctx, u, labID, taskID) (*View, error)`
    - `Activity(ctx, u, labID) (*View, error)`
    - `Extend(ctx, u, labID) (*View, error)`
    - `End(ctx, u, labID) (*View, error)`
    - `Sweep(ctx)`
    - `RunSweeper(ctx, every)`
    - `Touch(ctx, labID string)`
    - `owned(ctx, u, labID) (*Instance, error)`
    - `labContent(inst) (*content.Lab, *content.Quiz, error)`
  - View types (JSON shapes are the API contract for Task 19):
    - `View{id,state,error,runtime,team,training,module,terminals[{name,service}],task_order,tasks[TaskView],server_now,ends_at,limit_reason,end_reason,idle_deadline,idle_warning_s,can_extend,self_reported,complete,score,max_score}`
    - `TaskView{id,title,status(locked|open|setup_failed|passed|skipped),kind(check|quiz|review),points,awarded,quiz_prompt,has_setup,hints_total,hints_revealed,next_hint_cost}`
    - `TaskDetail{…TaskView, instructions, hints[], setup_error}`
    - `CheckResult{passed,output,timed_out,awarded,lab}`
    - `HintResult{index,text,cost,lab}`
    - `ModuleLab{title,runtime,runtime_ready,runtime_message,lab}`
  - End reasons: `user`, `ttl`, `idle`, `provision_timeout`.

- [ ] **Step 1: Write the failing tests**

`internal/labs/service_test.go`:
```go
package labs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time       { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type fakeRunner struct {
	mu          sync.Mutex
	unavailable error
	scripts     []ScriptSpec
	exit        func(ScriptSpec) int
	destroyed   []string
}

func (f *fakeRunner) Available(*Instance) error { return f.unavailable }
func (f *fakeRunner) Provision(context.Context, *Instance, []byte, string) error { return nil }
func (f *fakeRunner) OpenPTY(context.Context, *Instance, string, int, int) (PTY, error) {
	return nil, io.EOF
}
func (f *fakeRunner) RunScript(_ context.Context, _ *Instance, sp ScriptSpec) (ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, sp)
	code := 0
	if f.exit != nil {
		code = f.exit(sp)
	}
	return ScriptResult{ExitCode: code, Output: "out"}, nil
}
func (f *fakeRunner) Destroy(_ context.Context, in *Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, in.ID)
	return nil
}

type fx struct {
	s     *Service
	run   *fakeRunner
	clk   *clock
	u     *auth.User
	other *auth.User
}

func setup(t *testing.T, unlock bool) *fx {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-101@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}}
	ls := &learn.Service{DB: pool, State: func() *gitsync.State { return st }}
	store := auth.Store{DB: pool}
	u, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	other, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	if unlock {
		_ = ls.SetItem(ctx, u.ID, "forge", "forge-101", "01-welcome", "how-we-work", "complete", 1)
		_ = ls.SetItem(ctx, u.ID, "forge", "forge-101", "01-welcome", "quiz", "complete", 1)
	}
	run := &fakeRunner{}
	clk := &clock{t: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)}
	s := &Service{DB: pool, Learn: ls, Runners: map[string]Runner{"local": run}, Now: clk.Now, Log: slog.Default()}
	return &fx{s: s, run: run, clk: clk, u: u, other: other}
}

func (f *fx) start(t *testing.T) *View {
	t.Helper()
	v, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		got, _ := f.s.Get(context.Background(), f.u, v.ID)
		if got.State == Ready {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lab never became ready")
	return nil
}

func statusOf(v *View, task string) string {
	for _, tv := range v.Tasks {
		if tv.ID == task {
			return tv.Status
		}
	}
	return ""
}

func TestStartIsIdempotentAndBecomesReady(t *testing.T) {
	f := setup(t, true)
	v := f.start(t)
	again, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab")
	if err != nil || again.ID != v.ID {
		t.Fatalf("second start must return the same lab: %v %v", again, err)
	}
	if v.EndsAt == nil || !v.EndsAt.Equal(f.clk.Now().Add(time.Hour)) || v.LimitReason != "ttl" || v.IdleWarningS != 300 {
		t.Fatalf("timer fields: ends %v reason %q warn %d", v.EndsAt, v.LimitReason, v.IdleWarningS)
	}
	if statusOf(v, "t1-forge-file") != "open" || statusOf(v, "t2-find-port") != "locked" || !v.SelfReported {
		t.Fatalf("task statuses: %+v", v.Tasks)
	}
}

func TestStartGuards(t *testing.T) {
	f := setup(t, false)
	if _, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Locked) {
		t.Fatalf("locked module: %v", err)
	}
	f = setup(t, true)
	f.run.unavailable = apperr.Wrap(apperr.Unavailable, "your laptop agent is not connected")
	if _, err := f.s.Start(context.Background(), f.u, "forge", "forge-101", "02-first-lab"); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("offline agent: %v", err)
	}
	var n int
	_ = f.s.DB.QueryRow(context.Background(), "SELECT count(*) FROM lab_instances").Scan(&n)
	if n != 0 {
		t.Fatal("no lab row may be created when the runtime is unavailable")
	}
}

func TestFullLabFlow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.run.exit = func(sp ScriptSpec) int {
		if a, ok := sp.Env["CRUCIBLE_ANSWER"]; ok && a != "8081" {
			return 1
		}
		return 0
	}
	v := f.start(t)

	res, err := f.s.Check(ctx, f.u, v.ID, "t1-forge-file", "")
	if err != nil || !res.Passed || res.Awarded != 2 {
		t.Fatalf("t1: %+v %v", res, err)
	}
	if last := f.run.scripts[len(f.run.scripts)-1]; last.Service != "shell" || !strings.Contains(string(last.Script), "hello forge") {
		t.Fatalf("t1 check ran wrong script: %+v", last)
	}

	d, err := f.s.OpenTask(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || d.SetupError != "" || !strings.Contains(d.Instructions, "Something broke") {
		t.Fatalf("open t2: %+v %v", d, err)
	}
	if last := f.run.scripts[len(f.run.scripts)-1]; last.Service != "web" || !strings.Contains(string(last.Script), "8081") {
		t.Fatalf("setup script not run in web: %+v", last)
	}
	if res, _ := f.s.Check(ctx, f.u, v.ID, "t2-find-port", "9999"); res.Passed {
		t.Fatal("wrong terminal-quiz answer passed")
	}
	if res, _ := f.s.Check(ctx, f.u, v.ID, "t2-find-port", "8081"); !res.Passed {
		t.Fatal("right terminal-quiz answer failed")
	}

	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx"); err != nil {
		t.Fatal(err)
	}
	h, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx")
	if err != nil || h.Cost != 1.5 || !strings.Contains(h.Text, "nginx -s reload") {
		t.Fatalf("second hint: %+v %v", h, err)
	}
	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t3-fix-nginx"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("no more hints: %v", err)
	}
	res, _ = f.s.Check(ctx, f.u, v.ID, "t3-fix-nginx", "")
	// first hint costs the lab's hint_cost (0.25), second 1.5 → 3 − 1.75 = 1.25
	if !res.Passed || res.Awarded != 1.25 || !res.Lab.Complete || res.Lab.Score != 4.25 {
		t.Fatalf("t3: %+v lab %+v", res, res.Lab)
	}
	o, _ := f.s.Learn.Outline(ctx, f.u, "forge", "forge-101")
	if !o.Modules[1].Complete {
		t.Fatal("finishing the lab must complete the module")
	}
}

func TestSetupFailureAllowsSkipAndResetIsRateLimited(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	failSetup := true
	f.run.exit = func(sp ScriptSpec) int {
		if failSetup && strings.Contains(string(sp.Script), "nginx -s reload") && sp.Env == nil {
			return 1
		}
		return 0
	}
	v := f.start(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-forge-file", "")
	d, err := f.s.OpenTask(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || d.SetupError == "" || d.Status != "setup_failed" {
		t.Fatalf("setup failure: %+v %v", d, err)
	}
	var runs int
	_ = f.s.DB.QueryRow(ctx, "SELECT count(*) FROM setup_runs WHERE task = 't2-find-port'").Scan(&runs)
	if runs != 2 {
		t.Fatalf("setup must be retried once, ran %d times", runs)
	}

	failSetup = false
	if _, err := f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("reset within 5 minutes must be refused: %v", err)
	}
	f.clk.Add(6 * time.Minute)
	if _, err := f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port"); err != nil {
		t.Fatalf("reset after 6 minutes: %v", err)
	}

	failSetup = true
	f.clk.Add(6 * time.Minute)
	_, _ = f.s.ResetTask(ctx, f.u, v.ID, "t2-find-port")
	after, err := f.s.Skip(ctx, f.u, v.ID, "t2-find-port")
	if err != nil || statusOf(after, "t2-find-port") != "skipped" || statusOf(after, "t3-fix-nginx") != "open" {
		t.Fatalf("skip: %v %+v", err, after)
	}
}

func TestSweepExtendAndOwnership(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)

	if _, err := f.s.Get(ctx, f.other, v.ID); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("other users must not see the lab: %v", err)
	}

	ext, err := f.s.Extend(ctx, f.u, v.ID)
	if err != nil || !ext.EndsAt.Equal(v.EndsAt.Add(30*time.Minute)) || ext.CanExtend {
		t.Fatalf("extend: %+v %v", ext, err)
	}
	if _, err := f.s.Extend(ctx, f.u, v.ID); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("second extend: %v", err)
	}

	f.clk.Add(19 * time.Minute)
	f.s.Sweep(ctx)
	if got, _ := f.s.Get(ctx, f.u, v.ID); got.State != Ready {
		t.Fatalf("swept too early: %s", got.State)
	}
	f.clk.Add(2 * time.Minute) // 21 min idle > 20 min idle_timeout
	f.s.Sweep(ctx)
	got, _ := f.s.Get(ctx, f.u, v.ID)
	if got.State != Destroyed || got.EndReason != "idle" || len(f.run.destroyed) != 1 {
		t.Fatalf("idle sweep: %+v destroyed %v", got, f.run.destroyed)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/labs/...`
Expected: FAIL (`Service` undefined).

- [ ] **Step 3: Implement**

`internal/labs/service.go`:
```go
package labs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/agenthub"
	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/learn"
)

type Service struct {
	DB      *pgxpool.Pool
	Learn   *learn.Service
	Runners map[string]Runner
	Now     func() time.Time
	Log     *slog.Logger

	touchMu sync.Mutex
	touched map[string]time.Time
}

type TaskView struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Status        string  `json:"status"` // locked | open | setup_failed | passed | skipped
	Kind          string  `json:"kind"`   // check | quiz | review
	Points        float64 `json:"points"`
	Awarded       float64 `json:"awarded"`
	QuizPrompt    string  `json:"quiz_prompt,omitempty"`
	HasSetup      bool    `json:"has_setup"`
	HintsTotal    int     `json:"hints_total"`
	HintsRevealed int     `json:"hints_revealed"`
	NextHintCost  float64 `json:"next_hint_cost"`
}

type View struct {
	ID           string             `json:"id"`
	State        State              `json:"state"`
	Error        string             `json:"error,omitempty"`
	Runtime      string             `json:"runtime"`
	Team         string             `json:"team"`
	Training     string             `json:"training"`
	Module       string             `json:"module"`
	Terminals    []content.Terminal `json:"terminals"`
	TaskOrder    string             `json:"task_order"`
	Tasks        []TaskView         `json:"tasks"`
	ServerNow    time.Time          `json:"server_now"`
	EndsAt       *time.Time         `json:"ends_at,omitempty"`
	LimitReason  string             `json:"limit_reason,omitempty"`
	EndReason    string             `json:"end_reason,omitempty"`
	IdleDeadline *time.Time         `json:"idle_deadline,omitempty"`
	IdleWarningS int                `json:"idle_warning_s"`
	CanExtend    bool               `json:"can_extend"`
	SelfReported bool               `json:"self_reported"`
	Complete     bool               `json:"complete"`
	Score        float64            `json:"score"`
	MaxScore     float64            `json:"max_score"`
}

type TaskDetail struct {
	TaskView
	Instructions string   `json:"instructions"`
	Hints        []string `json:"hints"`
	SetupError   string   `json:"setup_error,omitempty"`
}

type CheckResult struct {
	Passed   bool    `json:"passed"`
	Output   string  `json:"output"`
	TimedOut bool    `json:"timed_out"`
	Awarded  float64 `json:"awarded"`
	Lab      *View   `json:"lab"`
}

type HintResult struct {
	Index int     `json:"index"`
	Text  string  `json:"text"`
	Cost  float64 `json:"cost"`
	Lab   *View   `json:"lab"`
}

type ModuleLab struct {
	Title          string `json:"title"`
	Runtime        string `json:"runtime"`
	RuntimeReady   bool   `json:"runtime_ready"`
	RuntimeMessage string `json:"runtime_message,omitempty"`
	Lab            *View  `json:"lab"`
}

const instCols = `id, user_id, team, training, module, sha, runtime, state, error, created_at, ready_at, ends_at,
	limit_reason, end_reason, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, extended`

func scanInst(row pgx.Row) (*Instance, error) {
	var in Instance
	var ttl, idle, warn, ext int
	err := row.Scan(&in.ID, &in.UserID, &in.Team, &in.Training, &in.Module, &in.SHA, &in.Runtime, &in.State, &in.Error,
		&in.CreatedAt, &in.ReadyAt, &in.EndsAt, &in.LimitReason, &in.EndReason, &in.LastActivityAt, &ttl, &idle, &warn, &ext, &in.Extended)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	if err != nil {
		return nil, err
	}
	in.TTL, in.IdleTimeout = time.Duration(ttl)*time.Second, time.Duration(idle)*time.Second
	in.IdleWarning, in.MaxExtension = time.Duration(warn)*time.Second, time.Duration(ext)*time.Second
	return &in, nil
}

func newLabID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) event(ctx context.Context, labID, kind, detail string) {
	_, _ = s.DB.Exec(ctx, `INSERT INTO lab_events (lab_id, kind, detail) VALUES ($1, $2, $3)`, labID, kind, detail)
}

func (s *Service) runnerErr(err error) error {
	if errors.Is(err, agenthub.ErrOffline) {
		return errAgentOffline
	}
	return err
}

func (s *Service) labContent(inst *Instance) (*content.Lab, *content.Quiz, error) {
	st := s.Learn.State()
	if st == nil {
		return nil, nil, apperr.Wrap(apperr.Unavailable, "content is still syncing")
	}
	t := st.Training(inst.Training, inst.SHA)
	if t == nil {
		return nil, nil, apperr.Wrap(apperr.Unavailable, "this lab's content is no longer available")
	}
	m := t.Module(inst.Module)
	if m == nil || m.Lab == nil {
		return nil, nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	return m.Lab, m.Quiz, nil
}

func (s *Service) owned(ctx context.Context, u *auth.User, labID string) (*Instance, error) {
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
	if err != nil {
		return nil, err
	}
	if inst.UserID != u.ID {
		return nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	return inst, nil
}

type taskRow struct {
	Status string
	Points float64
}

func (s *Service) taskRows(ctx context.Context, inst *Instance) (map[string]taskRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT task, status, points FROM lab_task_progress
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, inst.UserID, inst.Team, inst.Training, inst.Module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]taskRow{}
	for rows.Next() {
		var id string
		var r taskRow
		if err := rows.Scan(&id, &r.Status, &r.Points); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

func (s *Service) hintCounts(ctx context.Context, inst *Instance) (map[string]int, map[string]float64, error) {
	rows, err := s.DB.Query(ctx, `SELECT task, count(*), coalesce(sum(cost), 0) FROM hint_reveals
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 GROUP BY task`, inst.UserID, inst.Team, inst.Training, inst.Module)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	counts, costs := map[string]int{}, map[string]float64{}
	for rows.Next() {
		var id string
		var n int
		var c float64
		if err := rows.Scan(&id, &n, &c); err != nil {
			return nil, nil, err
		}
		counts[id], costs[id] = n, c
	}
	return counts, costs, rows.Err()
}

// setupStatus returns task → "ok" | "failed" from the latest setup run in this lab instance.
func (s *Service) setupStatus(ctx context.Context, labID string) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT ON (task) task, exit_code FROM setup_runs
		WHERE lab_id = $1 ORDER BY task, id DESC`, labID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var task string
		var code int
		if err := rows.Scan(&task, &code); err != nil {
			return nil, err
		}
		out[task] = map[bool]string{true: "ok", false: "failed"}[code == 0]
	}
	return out, rows.Err()
}

func taskStatuses(lab *content.Lab, done map[string]taskRow, setups map[string]string) map[string]string {
	out := map[string]string{}
	opened := false
	for _, t := range lab.Tasks {
		if r, ok := done[t.ID]; ok {
			out[t.ID] = r.Status
			continue
		}
		if lab.TaskOrder == "linear" && opened {
			out[t.ID] = "locked"
			continue
		}
		opened = true
		if setups[t.ID] == "failed" {
			out[t.ID] = "setup_failed"
		} else {
			out[t.ID] = "open"
		}
	}
	return out
}

func taskTitle(lab *content.Lab, t *content.Task) string {
	b, _ := os.ReadFile(filepath.Join(lab.Dir, t.Instructions))
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return t.ID
}

func (s *Service) view(ctx context.Context, inst *Instance) (*View, error) {
	lab, quiz, err := s.labContent(inst)
	if err != nil {
		return nil, err
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return nil, err
	}
	counts, _, err := s.hintCounts(ctx, inst)
	if err != nil {
		return nil, err
	}
	setups, err := s.setupStatus(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	statuses := taskStatuses(lab, done, setups)
	v := &View{ID: inst.ID, State: inst.State, Error: inst.Error, Runtime: inst.Runtime, Team: inst.Team,
		Training: inst.Training, Module: inst.Module, Terminals: lab.Terminals, TaskOrder: lab.TaskOrder,
		ServerNow: s.Now(), EndsAt: inst.EndsAt, LimitReason: inst.LimitReason, EndReason: inst.EndReason,
		IdleWarningS: int(inst.IdleWarning.Seconds()), SelfReported: inst.Runtime == "local", Complete: true}
	for _, t := range lab.Tasks {
		tv := TaskView{ID: t.ID, Title: taskTitle(lab, t), Status: statuses[t.ID], Points: t.Points,
			Awarded: done[t.ID].Points, HasSetup: t.Setup != nil, HintsTotal: len(t.Hints), HintsRevealed: counts[t.ID]}
		switch {
		case t.Quiz != "":
			tv.Kind, tv.QuizPrompt = "quiz", quiz.Question(t.Quiz).Prompt
		case t.Check != nil:
			tv.Kind = "check"
		default:
			tv.Kind = "review" // human scoring arrives in M5
		}
		if n := counts[t.ID]; n < len(t.Hints) {
			tv.NextHintCost = t.Hints[n].EffectiveCost(lab)
		}
		if tv.Status != "passed" && tv.Status != "skipped" {
			v.Complete = false
		}
		v.Score += tv.Awarded
		v.MaxScore += t.Points
		v.Tasks = append(v.Tasks, tv)
	}
	if inst.State == Ready {
		dl := inst.LastActivityAt.Add(inst.IdleTimeout)
		v.IdleDeadline = &dl
		v.CanExtend = !inst.Extended && inst.MaxExtension > 0
	}
	return v, nil
}

func (s *Service) active(ctx context.Context, userID int64, team, training, module string) (*Instance, error) {
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 ORDER BY created_at DESC LIMIT 1`,
		userID, team, training, module))
	if errors.Is(err, apperr.NotFound) {
		return nil, nil
	}
	return inst, err
}

func (s *Service) ModuleLab(ctx context.Context, u *auth.User, team, training, module string) (*ModuleLab, error) {
	_, t, _, err := s.Learn.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	m, err := s.Learn.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, err
	}
	if m.Lab == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this module has no lab")
	}
	out := &ModuleLab{Title: m.Title, Runtime: m.Lab.Runtime, RuntimeReady: true}
	if r := s.Runners[m.Lab.Runtime]; r == nil {
		out.RuntimeReady, out.RuntimeMessage = false, fmt.Sprintf("%s labs are not available yet", m.Lab.Runtime)
	} else if err := r.Available(&Instance{UserID: u.ID}); err != nil {
		out.RuntimeReady, out.RuntimeMessage = false, strings.TrimSuffix(err.Error(), ": "+apperr.Unavailable.Error())
	}
	inst, err := s.active(ctx, u.ID, team, training, module)
	if err != nil {
		return nil, err
	}
	if inst != nil {
		if out.Lab, err = s.view(ctx, inst); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) Start(ctx context.Context, u *auth.User, team, training, module string) (*View, error) {
	st, t, sha, err := s.Learn.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	m, err := s.Learn.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, err
	}
	if m.Lab == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this module has no lab")
	}
	r := s.Runners[m.Lab.Runtime]
	if r == nil {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs are not available yet", m.Lab.Runtime))
	}
	if inst, err := s.active(ctx, u.ID, team, training, module); err != nil {
		return nil, err
	} else if inst != nil && (inst.State == Provisioning || inst.State == Ready) {
		return s.view(ctx, inst)
	}
	tm := ResolveTiming(m.Lab, st.Platform.Teams[team].Programs[training].LabDefaults)
	now := s.Now()
	inst := &Instance{ID: newLabID(), UserID: u.ID, Team: team, Training: training, Module: module, SHA: sha,
		Runtime: m.Lab.Runtime, State: Provisioning, CreatedAt: now, LastActivityAt: now,
		TTL: tm.TTL, IdleTimeout: tm.IdleTimeout, IdleWarning: tm.IdleWarning, MaxExtension: tm.MaxExtension}
	if err := r.Available(inst); err != nil {
		return nil, err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $11, $12, $13)`,
		inst.ID, inst.UserID, team, training, module, sha, inst.Runtime, inst.State, now,
		int(tm.TTL.Seconds()), int(tm.IdleTimeout.Seconds()), int(tm.IdleWarning.Seconds()), int(tm.MaxExtension.Seconds()))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // a concurrent Start won the race
		existing, err := s.active(ctx, u.ID, team, training, module)
		if err != nil {
			return nil, err
		}
		return s.view(ctx, existing)
	}
	if err != nil {
		return nil, err
	}
	s.event(ctx, inst.ID, "requested", "auto-approved: "+inst.Runtime)
	go s.provision(context.WithoutCancel(ctx), inst, m.Lab)
	return s.view(ctx, inst)
}

func (s *Service) provision(ctx context.Context, inst *Instance, lab *content.Lab) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	bundle, err := Bundle(lab.Dir)
	if err == nil {
		err = s.Runners[inst.Runtime].Provision(ctx, inst, bundle, lab.Compose)
	}
	if err == nil && lab.Setup != nil {
		err = s.runSetup(ctx, inst, lab, "", lab.Setup)
	}
	if err != nil {
		s.Log.Warn("lab provisioning failed", "lab", inst.ID, "err", err)
		_ = s.Runners[inst.Runtime].Destroy(ctx, inst)
		_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'failed', error = $2, destroyed_at = $3 WHERE id = $1`,
			inst.ID, s.runnerErr(err).Error(), s.Now())
		s.event(ctx, inst.ID, "failed", err.Error())
		return
	}
	now := s.Now()
	end := EffectiveEnd(Limit{At: now.Add(inst.TTL), Reason: "ttl"})
	_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'ready', ready_at = $2, ends_at = $3, limit_reason = $4,
		last_activity_at = $2 WHERE id = $1 AND state = 'provisioning'`, inst.ID, now, end.At, end.Reason)
	s.event(ctx, inst.ID, "ready", "")
}

func (s *Service) runScript(ctx context.Context, inst *Instance, lab *content.Lab, sc *content.Script, env map[string]string) (ScriptResult, error) {
	body, err := os.ReadFile(filepath.Join(lab.Dir, sc.Script))
	if err != nil {
		return ScriptResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, sc.Timeout.D()+15*time.Second)
	defer cancel()
	return s.Runners[inst.Runtime].RunScript(ctx, inst, ScriptSpec{Service: sc.RunIn, Script: body, Env: env, Timeout: sc.Timeout.D()})
}

// runSetup runs a setup script, retrying once (spec §8.5). It returns an error if the scenario could not be prepared.
func (s *Service) runSetup(ctx context.Context, inst *Instance, lab *content.Lab, taskID string, sc *content.Script) error {
	var last error
	for attempt := 1; attempt <= 2; attempt++ {
		start := s.Now()
		res, err := s.runScript(ctx, inst, lab, sc, nil)
		if err != nil {
			return s.runnerErr(err) // could not run at all: not a scenario failure
		}
		_, _ = s.DB.Exec(ctx, `INSERT INTO setup_runs (lab_id, task, attempt, exit_code, output, duration_ms, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, inst.ID, taskID, attempt, res.ExitCode, res.Output,
			s.Now().Sub(start).Milliseconds(), s.Now())
		if res.ExitCode == 0 {
			return nil
		}
		last = fmt.Errorf("setup exited with %d", res.ExitCode)
	}
	// ponytail: maintainers are notified by M3 notifications; until then this is logged.
	s.Log.Warn("setup script failed twice", "lab", inst.ID, "task", taskID)
	s.event(ctx, inst.ID, "setup_failed", taskID)
	return last
}

// readyTask loads a lab the user owns, requires it to be ready, and resolves the task.
func (s *Service) readyTask(ctx context.Context, u *auth.User, labID, taskID string) (*Instance, *content.Lab, *content.Quiz, *content.Task, map[string]string, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if inst.State != Ready {
		return nil, nil, nil, nil, nil, apperr.Wrap(apperr.Conflict, "the lab is not ready")
	}
	lab, quiz, err := s.labContent(inst)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	task := lab.Task(taskID)
	if task == nil {
		return nil, nil, nil, nil, nil, apperr.Wrap(apperr.NotFound, "task not found")
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	setups, err := s.setupStatus(ctx, inst.ID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	statuses := taskStatuses(lab, done, setups)
	statuses["_setup:"+taskID] = setups[taskID]
	return inst, lab, quiz, task, statuses, nil
}

func (s *Service) Get(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, inst)
}

func (s *Service) OpenTask(ctx context.Context, u *auth.User, labID, taskID string) (*TaskDetail, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] == "locked" {
		return nil, apperr.Wrap(apperr.Locked, "finish the earlier tasks first")
	}
	setupErr := ""
	if statuses[taskID] == "open" && task.Setup != nil && statuses["_setup:"+taskID] == "" {
		if err := s.runSetup(ctx, inst, lab, taskID, task.Setup); err != nil {
			if errors.Is(err, apperr.Unavailable) {
				return nil, err
			}
			setupErr = "This scenario couldn't be prepared. You can skip this task without penalty, or restart the lab."
		}
	}
	if statuses[taskID] == "setup_failed" {
		setupErr = "This scenario couldn't be prepared. You can skip this task without penalty, or restart the lab."
	}
	s.Touch(ctx, inst.ID)
	v, err := s.view(ctx, inst)
	if err != nil {
		return nil, err
	}
	d := &TaskDetail{SetupError: setupErr}
	for _, tv := range v.Tasks {
		if tv.ID == taskID {
			d.TaskView = tv
		}
	}
	b, err := os.ReadFile(filepath.Join(lab.Dir, task.Instructions))
	if err != nil {
		return nil, err
	}
	d.Instructions = string(b)
	for i := 0; i < d.HintsRevealed && i < len(task.Hints); i++ {
		d.Hints = append(d.Hints, hintText(lab, task.Hints[i]))
	}
	return d, nil
}

func hintText(lab *content.Lab, h *content.Hint) string {
	if h.File == "" {
		return h.Text
	}
	b, _ := os.ReadFile(filepath.Join(lab.Dir, h.File))
	return string(b)
}

func (s *Service) Check(ctx context.Context, u *auth.User, labID, taskID, answer string) (*CheckResult, error) {
	inst, lab, quiz, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	switch statuses[taskID] {
	case "passed", "skipped":
		v, err := s.view(ctx, inst)
		return &CheckResult{Passed: statuses[taskID] == "passed", Lab: v}, err
	case "locked":
		return nil, apperr.Wrap(apperr.Locked, "finish the earlier tasks first")
	case "setup_failed":
		return nil, apperr.Wrap(apperr.Conflict, "this scenario couldn't be prepared; skip the task instead")
	}
	if task.Setup != nil && statuses["_setup:"+taskID] != "ok" {
		return nil, apperr.Wrap(apperr.Conflict, "the scenario is still being prepared")
	}
	var sc *content.Script
	env := map[string]string{}
	switch {
	case task.Quiz != "":
		sc = quiz.Question(task.Quiz).Script
		env["CRUCIBLE_ANSWER"] = answer
	case task.Check != nil:
		sc = task.Check
	default:
		return nil, apperr.Wrap(apperr.Conflict, "this task is reviewed by a scorer")
	}
	res, err := s.runScript(ctx, inst, lab, sc, env)
	if err != nil {
		return nil, s.runnerErr(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO check_runs (lab_id, task, exit_code, output, answer, self_reported)
		VALUES ($1, $2, $3, $4, $5, $6)`, inst.ID, taskID, res.ExitCode, res.Output, answer, inst.Runtime == "local"); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	out := &CheckResult{Passed: res.ExitCode == 0, Output: res.Output, TimedOut: res.TimedOut}
	if out.Passed {
		_, costs, err := s.hintCounts(ctx, inst)
		if err != nil {
			return nil, err
		}
		out.Awarded = max(0, task.Points-costs[taskID])
		if err := s.finishTask(ctx, inst, lab, taskID, "passed", out.Awarded); err != nil {
			return nil, err
		}
	}
	out.Lab, err = s.view(ctx, inst)
	return out, err
}

func (s *Service) finishTask(ctx context.Context, inst *Instance, lab *content.Lab, taskID, status string, points float64) error {
	if _, err := s.DB.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status, points)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, status, points); err != nil {
		return err
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return err
	}
	var score, maxScore float64
	for _, t := range lab.Tasks {
		r, ok := done[t.ID]
		if !ok {
			return nil // not finished yet
		}
		score += r.Points
		maxScore += t.Points
	}
	s.event(ctx, inst.ID, "completed", fmt.Sprintf("%.2f/%.2f", score, maxScore))
	return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "complete", score/maxScore)
}

func (s *Service) RevealHint(ctx context.Context, u *auth.User, labID, taskID string) (*HintResult, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] != "open" {
		return nil, apperr.Wrap(apperr.Conflict, "hints are available for the current task only")
	}
	counts, _, err := s.hintCounts(ctx, inst)
	if err != nil {
		return nil, err
	}
	n := counts[taskID]
	if n >= len(task.Hints) {
		return nil, apperr.Wrap(apperr.Conflict, "no more hints for this task")
	}
	h := task.Hints[n]
	cost := h.EffectiveCost(lab)
	if _, err := s.DB.Exec(ctx, `INSERT INTO hint_reveals (user_id, team, training, module, task, hint_index, cost, lab_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, n, cost, inst.ID); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	v, err := s.view(ctx, inst)
	return &HintResult{Index: n, Text: hintText(lab, h), Cost: cost, Lab: v}, err
}

func (s *Service) ResetTask(ctx context.Context, u *auth.User, labID, taskID string) (*View, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if task.Setup == nil {
		return nil, apperr.Wrap(apperr.Conflict, "this task has no scenario to reset")
	}
	if st := statuses[taskID]; st == "passed" || st == "skipped" || st == "locked" {
		return nil, apperr.Wrap(apperr.Conflict, "only the current task can be reset")
	}
	var last time.Time
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(max(at), 'epoch') FROM setup_runs WHERE lab_id = $1 AND task = $2`,
		inst.ID, taskID).Scan(&last); err != nil {
		return nil, err
	}
	if wait := 5*time.Minute - s.Now().Sub(last); wait > 0 {
		return nil, apperr.Wrap(apperr.Conflict, fmt.Sprintf("you can reset this scenario again in %s", wait.Round(time.Second)))
	}
	if err := s.runSetup(ctx, inst, lab, taskID, task.Setup); errors.Is(err, apperr.Unavailable) {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	return s.view(ctx, inst)
}

func (s *Service) Skip(ctx context.Context, u *auth.User, labID, taskID string) (*View, error) {
	inst, lab, _, _, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] != "setup_failed" {
		return nil, apperr.Wrap(apperr.Conflict, "only tasks whose scenario failed can be skipped")
	}
	if err := s.finishTask(ctx, inst, lab, taskID, "skipped", 0); err != nil {
		return nil, err
	}
	return s.view(ctx, inst)
}

// Touch records trainee activity, at most every 30 seconds per lab.
func (s *Service) Touch(ctx context.Context, labID string) {
	now := s.Now()
	s.touchMu.Lock()
	if s.touched == nil {
		s.touched = map[string]time.Time{}
	}
	if now.Sub(s.touched[labID]) < 30*time.Second {
		s.touchMu.Unlock()
		return
	}
	s.touched[labID] = now
	s.touchMu.Unlock()
	_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET last_activity_at = $2 WHERE id = $1 AND state = 'ready'`, labID, now)
}

// Activity is the explicit "I'm here" from the idle prompt; it always writes.
func (s *Service) Activity(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	s.touchMu.Lock()
	delete(s.touched, labID)
	s.touchMu.Unlock()
	s.Touch(ctx, labID)
	return s.Get(ctx, u, inst.ID)
}

func (s *Service) Extend(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State != Ready || inst.Extended || inst.MaxExtension == 0 || inst.EndsAt == nil {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	// ponytail: M3 re-checks the schedule window and cost tier here (spec §8.6); local labs cost nothing.
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET ends_at = $2, extended = true, last_activity_at = $3
		WHERE id = $1 AND NOT extended`, inst.ID, inst.EndsAt.Add(inst.MaxExtension), s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	s.event(ctx, inst.ID, "extended", inst.MaxExtension.String())
	return s.Get(ctx, u, labID)
}

func (s *Service) End(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	s.destroy(ctx, inst, "user")
	return s.Get(ctx, u, labID)
}

func (s *Service) destroy(ctx context.Context, inst *Instance, reason string) {
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', end_reason = $2
		WHERE id = $1 AND state IN ('provisioning', 'ready')`, inst.ID, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	note := ""
	if err := s.Runners[inst.Runtime].Destroy(ctx, inst); err != nil {
		note = "cleanup failed: " + err.Error()
		if errors.Is(err, agenthub.ErrOffline) {
			note = "agent offline; its containers are removed when the agent next starts or stops"
		}
		s.Log.Warn("lab destroy incomplete", "lab", inst.ID, "err", err)
	}
	_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroyed', destroyed_at = $2, error = $3 WHERE id = $1`,
		inst.ID, s.Now(), note)
	s.event(ctx, inst.ID, "destroyed", reason)
}

// Sweep destroys labs past their end time or idle deadline, and provisioning that hung.
func (s *Service) Sweep(ctx context.Context) {
	now := s.Now()
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE (state = 'ready' AND (ends_at <= $1 OR last_activity_at + idle_timeout_s * interval '1 second' <= $1))
		   OR (state = 'provisioning' AND created_at < $1 - interval '15 minutes')`, now)
	if err != nil {
		s.Log.Error("lab sweep query failed", "err", err)
		return
	}
	var due []*Instance
	for rows.Next() {
		inst, err := scanInst(rows)
		if err == nil {
			due = append(due, inst)
		}
	}
	rows.Close()
	for _, inst := range due {
		reason := "idle"
		switch {
		case inst.State == Provisioning:
			reason = "provision_timeout"
		case inst.EndsAt != nil && !inst.EndsAt.After(now):
			reason = "ttl"
		}
		s.destroy(ctx, inst, reason)
	}
}

func (s *Service) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/labs/...`
Expected: `ok crucible/internal/labs`

- [ ] **Step 5: Commit**

```bash
git add internal/labs
git commit -m "feat: lab service with tasks, checks, setup retries, hints, extend, idle and TTL sweeps"
```

---

### Task 15: Lab HTTP API and terminal WebSocket bridge

**Files:**
- Create: `internal/labs/http.go`, `internal/labs/http_test.go`

**Interfaces:**
- Consumes: `labs.Service`, `auth.UserFrom`, `httpx`.
- Produces:
  - `(*labs.Service).Routes(r chi.Router)`:
    - `GET|POST /api/programs/{team}/{training}/modules/{module}/lab`
    - `GET /api/labs/{id}`
    - `GET /api/labs/{id}/tasks/{task}`
    - `POST /api/labs/{id}/tasks/{task}/{check|hint|reset|skip}` (check body `{answer}`)
    - `POST /api/labs/{id}/{activity|extend}`
    - `DELETE /api/labs/{id}`
    - `GET /api/labs/{id}/terminals/{name}/ws?cols&rows`
  - Terminal wire format: browser→server binary = keystrokes; text JSON `{"cols":n,"rows":n}` = resize; server→browser binary = output.

- [ ] **Step 1: Write the failing test**

`internal/labs/http_test.go`:
```go
package labs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
)

type echoPTY struct {
	r       *io.PipeReader
	w       *io.PipeWriter
	mu      sync.Mutex
	resized [2]int
}

func (e *echoPTY) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echoPTY) Write(b []byte) (int, error) { return e.w.Write(b) }
func (e *echoPTY) Close() error                { return e.w.Close() }
func (e *echoPTY) Resize(c, r int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resized = [2]int{c, r}
	return nil
}

type ptyRunner struct {
	fakeRunner
	pty *echoPTY
}

func (p *ptyRunner) OpenPTY(context.Context, *Instance, string, int, int) (PTY, error) { return p.pty, nil }

func TestTerminalBridge(t *testing.T) {
	f := setup(t, true)
	r, w := io.Pipe()
	pr := &ptyRunner{pty: &echoPTY{r: r, w: w}}
	f.s.Runners["local"] = pr
	v := f.start(t)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.u)))
		})
	})
	f.s.Routes(router)
	srv := httptest.NewServer(router)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/labs/"+v.ID+"/terminals/shell/ws?cols=100&rows=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil || string(data) != "ls\n" {
		t.Fatalf("echo: %q %v", data, err)
	}
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"cols":120,"rows":40}`))
	for i := 0; i < 100; i++ {
		pr.pty.mu.Lock()
		got := pr.pty.resized
		pr.pty.mu.Unlock()
		if got == [2]int{120, 40} {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resize not forwarded")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/labs/ -run TestTerminalBridge`
Expected: FAIL (`Routes` undefined).

- [ ] **Step 3: Implement**

`internal/labs/http.go`:
```go
package labs

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	const mod = "/api/programs/{team}/{training}/modules/{module}/lab"
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	p := func(r *http.Request, k string) string { return chi.URLParam(r, k) }

	r.Get(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ModuleLab(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
		reply(w, v, err)
	})
	r.Post(mod, func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Start(r.Context(), user(r), p(r, "team"), p(r, "training"), p(r, "module"))
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Delete("/api/labs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.End(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/activity", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Activity(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/extend", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Extend(r.Context(), user(r), p(r, "id"))
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.OpenTask(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answer string `json:"answer"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Check(r.Context(), user(r), p(r, "id"), p(r, "task"), body.Answer)
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/hint", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.RevealHint(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/reset", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ResetTask(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Post("/api/labs/{id}/tasks/{task}/skip", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Skip(r.Context(), user(r), p(r, "id"), p(r, "task"))
		reply(w, v, err)
	})
	r.Get("/api/labs/{id}/terminals/{name}/ws", s.terminal)
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (s *Service) terminal(w http.ResponseWriter, r *http.Request) {
	inst, err := s.owned(r.Context(), auth.UserFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if inst.State != Ready {
		httpx.Error(w, apperr.Wrap(apperr.Conflict, "the lab is not ready"))
		return
	}
	lab, _, err := s.labContent(inst)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	service := ""
	for _, t := range lab.Terminals {
		if t.Name == chi.URLParam(r, "name") {
			service = t.Service
		}
	}
	if service == "" {
		httpx.Error(w, apperr.Wrap(apperr.NotFound, "terminal not found"))
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 32
	}
	pty, err := s.Runners[inst.Runtime].OpenPTY(r.Context(), inst, service, cols, rows)
	if err != nil {
		httpx.Error(w, s.runnerErr(err))
		return
	}
	defer pty.Close()
	ws, err := websocket.Accept(w, r, nil) // same-origin only (default)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()

	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 && ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
			if err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "terminal closed")
				return
			}
		}
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var size struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(data, &size) == nil && size.Cols > 0 && size.Rows > 0 {
				_ = pty.Resize(size.Cols, size.Rows)
			}
			continue
		}
		s.Touch(ctx, inst.ID)
		if _, err := pty.Write(data); err != nil {
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/labs/...`
Expected: `ok crucible/internal/labs`

- [ ] **Step 5: Commit**

```bash
git add internal/labs
git commit -m "feat: lab HTTP API and terminal WebSocket bridge"
```

---

### Task 16: Wire the API, container image, local stack (Postgres + Keycloak)

**Files:**
- Modify: `internal/httpapi/server.go` (full replacement), `internal/httpapi/server_test.go`, `cmd/crucible-api/main.go` (full replacement)
- Create: `Dockerfile`, `.dockerignore`, `deploy/compose/docker-compose.yml`, `deploy/compose/keycloak/crucible-realm.json`, `scripts/seed-git.sh`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - HTTP endpoints (beyond module routes):
    - `GET /healthz`
    - `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout`
    - `GET /api/meta` → `{quotes, default_theme}` (public)
    - `GET /api/me` → `{user, is_admin, default_theme}`
    - `PUT /api/me/prefs` with `{theme, calm_motion}`
    - `POST /api/agent/tokens` → `{token, command}`
    - `GET /api/agent/status` → `{online}`
    - `GET /api/agent/ws` (Bearer pairing token)
    - `POST /api/git/hook` (header `X-Crucible-Secret`)
    - The SPA is served from `CRUCIBLE_WEB_DIR` with an `index.html` fallback.
  - Environment variables:
    - `DATABASE_URL`, `CRUCIBLE_PLATFORM_REPO`, `CRUCIBLE_PLATFORM_BRANCH` (main), `CRUCIBLE_DATA_DIR` (/data)
    - `CRUCIBLE_SYNC_INTERVAL` (60s), `CRUCIBLE_PUBLIC_URL`, `CRUCIBLE_GIT_HOOK_SECRET`, `CRUCIBLE_WEB_DIR`, `CRUCIBLE_ADDR` (:8080)
    - `OIDC_ISSUER`, `OIDC_DISCOVERY_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`
  - Local users (password = username): `trainee`, `leader`, `senior`, `admin` @crucible.local.

- [ ] **Step 1: Update the router test**

Replace `internal/httpapi/server_test.go`:
```go
package httpapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"crucible/internal/agenthub"
	"crucible/internal/labs"
	"crucible/internal/learn"
)

func TestHealthzAndSPAFallback(t *testing.T) {
	web := t.TempDir()
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>forge</html>"), 0o644)
	r := NewRouter(Deps{Learn: &learn.Service{}, Labs: &labs.Service{}, Hub: agenthub.New(), WebDir: web})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/p/forge/forge-101", nil))
	if w.Code != 200 || w.Body.String() != "<html>forge</html>" {
		t.Fatalf("spa fallback: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/nope", nil))
	if w.Code != 404 {
		t.Fatalf("unknown api route must 404, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/...`
Expected: FAIL (unknown fields `Learn`, `Labs`, `Hub`, `WebDir` in `Deps`).

- [ ] **Step 3: Implement the router**

Replace `internal/httpapi/server.go`:
```go
// Package httpapi wires every module's routes into one router.
package httpapi

import (
	"crypto/subtle"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"crucible/internal/agenthub"
	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/gitsync"
	"crucible/internal/httpx"
	"crucible/internal/labs"
	"crucible/internal/learn"
	"crucible/internal/rbac"
)

type Deps struct {
	Auth       auth.Store
	OIDC       *auth.OIDC
	Sync       *gitsync.Syncer
	Learn      *learn.Service
	Labs       *labs.Service
	Hub        *agenthub.Hub
	PublicURL  string
	HookSecret string
	WebDir     string
}

func NewRouter(d Deps) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	if d.OIDC != nil {
		r.Get("/auth/login", d.OIDC.Login)
		r.Get("/auth/callback", d.OIDC.Callback)
		r.Post("/auth/logout", d.OIDC.Logout)
	}
	r.Get("/api/meta", func(w http.ResponseWriter, _ *http.Request) {
		meta := map[string]any{"quotes": []string{}, "default_theme": "forge"}
		if st := state(d); st != nil && st.Platform != nil {
			meta["quotes"], meta["default_theme"] = st.Platform.Settings.Quotes, st.Platform.Settings.DefaultTheme
		}
		httpx.JSON(w, http.StatusOK, meta)
	})
	r.Post("/api/git/hook", func(w http.ResponseWriter, r *http.Request) {
		if d.HookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Crucible-Secret")), []byte(d.HookSecret)) != 1 {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "not found"))
			return
		}
		d.Sync.Trigger()
		w.WriteHeader(http.StatusAccepted)
	})
	r.Get("/api/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, err := d.Auth.UserByAgentToken(r.Context(), tok)
		if err != nil || u == nil || tok == "" {
			httpx.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid pairing token"})
			return
		}
		d.Hub.Serve(w, r, u.ID)
	})

	r.Group(func(r chi.Router) {
		r.Use(d.Auth.Middleware, auth.RequireUser)
		r.Get("/api/me", func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context())
			admin, theme := false, "forge"
			if st := state(d); st != nil && st.Platform != nil {
				admin, theme = (rbac.Checker{P: st.Platform}).IsAdmin(u.Email), st.Platform.Settings.DefaultTheme
			}
			httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "is_admin": admin, "default_theme": theme})
		})
		r.Put("/api/me/prefs", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Theme      string `json:"theme"`
				CalmMotion bool   `json:"calm_motion"`
			}
			if err := httpx.Read(r, &body); err != nil {
				httpx.Error(w, err)
				return
			}
			if body.Theme != "" && !contains(config.Themes, body.Theme) {
				httpx.Error(w, apperr.Wrap(apperr.Invalid, "unknown theme"))
				return
			}
			if err := d.Auth.SetPrefs(r.Context(), auth.UserFrom(r.Context()).ID, body.Theme, body.CalmMotion); err != nil {
				httpx.Error(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		r.Post("/api/agent/tokens", func(w http.ResponseWriter, r *http.Request) {
			tok, err := d.Auth.CreateAgentToken(r.Context(), auth.UserFrom(r.Context()).ID)
			if err != nil {
				httpx.Error(w, err)
				return
			}
			httpx.JSON(w, http.StatusOK, map[string]string{"token": tok,
				"command": "crucible-agent --server " + d.PublicURL + " --token " + tok})
		})
		r.Get("/api/agent/status", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, http.StatusOK, map[string]bool{"online": d.Hub.Online(auth.UserFrom(r.Context()).ID)})
		})
		d.Learn.Routes(r)
		d.Labs.Routes(r)
	})

	r.NotFound(spa(d.WebDir))
	return r
}

func state(d Deps) *gitsync.State {
	if d.Sync == nil {
		return nil
	}
	return d.Sync.Current()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// spa serves built frontend files and falls back to index.html for client-side routes.
func spa(dir string) http.HandlerFunc {
	files := http.FileServer(http.Dir(dir))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "not found"))
			return
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path.Clean(r.URL.Path)))); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	}
}
```

Replace `cmd/crucible-api/main.go`:
```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"crucible/internal/agenthub"
	"crucible/internal/auth"
	"crucible/internal/db"
	"crucible/internal/gitsync"
	"crucible/internal/httpapi"
	"crucible/internal/labs"
	"crucible/internal/learn"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("crucible-api failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	pool, err := db.Open(ctx, must("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	syncer := gitsync.New(env("CRUCIBLE_DATA_DIR", "/data"), must("CRUCIBLE_PLATFORM_REPO"), env("CRUCIBLE_PLATFORM_BRANCH", "main"), slog.Default())
	if err := syncer.SyncOnce(ctx); err != nil {
		slog.Warn("initial git sync failed; retrying in the background", "err", err)
	}
	every, err := time.ParseDuration(env("CRUCIBLE_SYNC_INTERVAL", "60s"))
	if err != nil {
		return fmt.Errorf("CRUCIBLE_SYNC_INTERVAL: %w", err)
	}
	go syncer.Run(ctx, every)

	public := strings.TrimRight(env("CRUCIBLE_PUBLIC_URL", "http://localhost:8080"), "/")
	store := auth.Store{DB: pool}
	var oidcH *auth.OIDC
	for attempt := 1; ; attempt++ { // the IdP may still be starting
		oidcH, err = auth.NewOIDC(ctx, auth.OIDCConfig{Issuer: must("OIDC_ISSUER"), DiscoveryURL: os.Getenv("OIDC_DISCOVERY_URL"),
			ClientID: must("OIDC_CLIENT_ID"), ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"), RedirectURL: public + "/auth/callback"},
			store, strings.HasPrefix(public, "https://"))
		if err == nil || attempt == 30 {
			break
		}
		slog.Info("waiting for the identity provider", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return err
	}

	hub := agenthub.New()
	learnSvc := &learn.Service{DB: pool, State: syncer.Current}
	labSvc := &labs.Service{DB: pool, Learn: learnSvc, Runners: map[string]labs.Runner{"local": labs.LocalRunner{Hub: hub}},
		Now: time.Now, Log: slog.Default()}
	go labSvc.RunSweeper(ctx, 15*time.Second)

	srv := &http.Server{
		Addr: env("CRUCIBLE_ADDR", ":8080"),
		Handler: httpapi.NewRouter(httpapi.Deps{Auth: store, OIDC: oidcH, Sync: syncer, Learn: learnSvc, Labs: labSvc, Hub: hub,
			PublicURL: public, HookSecret: os.Getenv("CRUCIBLE_GIT_HOOK_SECRET"), WebDir: env("CRUCIBLE_WEB_DIR", "web/dist")}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("crucible-api listening. The forge is lit", "addr", srv.Addr, "public_url", public)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing required environment variable", "name", k)
		os.Exit(2)
	}
	return v
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/... && go build ./...`
Expected: `ok crucible/internal/httpapi`, and the build succeeds.

- [ ] **Step 5: Container image and local stack**

`Dockerfile`:
```dockerfile
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o /out/crucible-api ./cmd/crucible-api && CGO_ENABLED=0 go build -o /out/crucible ./cmd/crucible

FROM alpine:3.22
RUN apk add --no-cache git tar ca-certificates \
 && git config --system --add safe.directory '*' \
 && adduser -D -u 10001 crucible && mkdir /data && chown crucible /data
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/web/dist /app/web
USER crucible
ENV CRUCIBLE_WEB_DIR=/app/web CRUCIBLE_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["crucible-api"]
```

`.dockerignore`:
```
bin
.local
web/node_modules
web/dist
e2e
docs
```

`deploy/compose/docker-compose.yml`:
```yaml
name: crucible-local
services:
  postgres:
    image: postgres:18-alpine
    environment: { POSTGRES_USER: crucible, POSTGRES_PASSWORD: crucible, POSTGRES_DB: crucible }
    volumes: [pgdata:/var/lib/postgresql]
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U crucible"], interval: 2s, retries: 30 }

  keycloak:
    image: quay.io/keycloak/keycloak:26.4
    command: ["start-dev", "--import-realm"]
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: admin
      KC_HOSTNAME: http://localhost:8081            # what the browser uses
      KC_HOSTNAME_BACKCHANNEL_DYNAMIC: "true"       # the API reaches it as http://keycloak:8080
      KC_HEALTH_ENABLED: "true"
    ports: ["8081:8080"]
    volumes: ["./keycloak:/opt/keycloak/data/import:ro"]
    healthcheck:
      test: ["CMD-SHELL", "exec 3<>/dev/tcp/127.0.0.1/9000 && printf 'GET /health/ready HTTP/1.1\\r\\nHost: localhost\\r\\nConnection: close\\r\\n\\r\\n' >&3 && grep -q UP <&3"]
      interval: 5s
      retries: 60

  api:
    build: { context: ../.., dockerfile: Dockerfile }
    environment:
      DATABASE_URL: postgres://crucible:crucible@postgres:5432/crucible?sslmode=disable
      CRUCIBLE_PLATFORM_REPO: file:///git/platform.git
      CRUCIBLE_PUBLIC_URL: http://localhost:8080
      CRUCIBLE_SYNC_INTERVAL: 10s
      OIDC_ISSUER: http://localhost:8081/realms/crucible
      OIDC_DISCOVERY_URL: http://keycloak:8080/realms/crucible
      OIDC_CLIENT_ID: crucible
      OIDC_CLIENT_SECRET: crucible-dev-secret
    ports: ["8080:8080"]
    volumes: ["../../.local/git:/git:ro", "apidata:/data"]
    depends_on:
      postgres: { condition: service_healthy }
      keycloak: { condition: service_healthy }
    healthcheck: { test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/healthz"], interval: 3s, retries: 40 }

volumes: { pgdata: {}, apidata: {} }
```

`deploy/compose/keycloak/crucible-realm.json`:
```json
{
  "realm": "crucible",
  "enabled": true,
  "sslRequired": "none",
  "clients": [
    {
      "clientId": "crucible",
      "enabled": true,
      "protocol": "openid-connect",
      "publicClient": false,
      "secret": "crucible-dev-secret",
      "standardFlowEnabled": true,
      "directAccessGrantsEnabled": false,
      "redirectUris": ["http://localhost:8080/auth/callback", "http://localhost:5173/auth/callback"],
      "webOrigins": ["+"],
      "attributes": { "pkce.code.challenge.method": "S256" }
    }
  ],
  "users": [
    { "username": "trainee", "email": "trainee@crucible.local", "emailVerified": true, "enabled": true,
      "firstName": "Tara", "lastName": "Trainee", "credentials": [{ "type": "password", "value": "trainee", "temporary": false }] },
    { "username": "leader", "email": "leader@crucible.local", "emailVerified": true, "enabled": true,
      "firstName": "Leo", "lastName": "Leader", "credentials": [{ "type": "password", "value": "leader", "temporary": false }] },
    { "username": "senior", "email": "senior@crucible.local", "emailVerified": true, "enabled": true,
      "firstName": "Sam", "lastName": "Senior", "credentials": [{ "type": "password", "value": "senior", "temporary": false }] },
    { "username": "admin", "email": "admin@crucible.local", "emailVerified": true, "enabled": true,
      "firstName": "Ada", "lastName": "Admin", "credentials": [{ "type": "password", "value": "admin", "temporary": false }] }
  ]
}
```

`scripts/seed-git.sh`:
```bash
#!/usr/bin/env bash
# Turns examples/{platform,forge-101} into bare git repos under .local/git (mounted at /git in the api container).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/.local/git"
rm -rf "$out"
mkdir -p "$out"
for name in platform forge-101; do
  work=$(mktemp -d)
  cp -R "$root/examples/$name/." "$work/"
  git -C "$work" init -q -b main
  git -C "$work" add -A
  git -C "$work" -c user.name=crucible -c user.email=crucible@local commit -qm "seed $name"
  git clone -q --bare "$work" "$out/$name.git"
  rm -rf "$work"
done
echo "seeded $out"
```

```bash
chmod +x scripts/seed-git.sh
```

- [ ] **Step 6: Validate the compose file and seed script**

Run: `./scripts/seed-git.sh && docker compose -f deploy/compose/docker-compose.yml config --quiet && echo compose-ok`
Expected: `seeded …/.local/git` then `compose-ok`. The full containerised stack needs the frontend (Task 17) and is exercised end to end in Task 20.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi cmd/crucible-api Dockerfile .dockerignore deploy/compose scripts/seed-git.sh
git commit -m "feat: wire crucible-api, container image and local compose stack with Keycloak"
```

---
### Task 17: Frontend scaffold: themes, motion, loader, quotes, navigation, settings

**Files:**
- Create: `web/` via Vite, then replace or create:
  - `web/vite.config.ts`, `web/index.html`
  - `web/src/{main.tsx,App.tsx,api.ts,useFetch.ts,types.ts}`
  - `web/src/theme/{tokens.css,app.css,theme.ts}`
  - `web/src/lib/{quotes.ts,alerts.ts}`
  - `web/src/components/{Loader,Embers,Nav,Markdown,SparkBurst,MoltenBar,ErrorBox,Toaster}.tsx`
  - `web/src/pages/Settings.tsx`
  - Stub pages, replaced in Tasks 18–19: `web/src/pages/{Hearth,Training,Reading,Quiz,Lab,Connect}.tsx`

**Interfaces:**
- Consumes: `GET /api/me`, `GET /api/meta`, `PUT /api/me/prefs`, `POST /auth/logout`.
- Produces (used by Tasks 18–19):
  - API helpers:
    - `api<T>(path, init?: RequestInit & {json?: unknown}): Promise<T>` (throws `ApiError{status, message}`; redirects to `/auth/login` on 401)
    - `useFetch<T>(path | null, intervalMs?) → {data, error, reload}`
    - `useMe() → {me, setPrefs(theme, calm)}`
  - Components: `<Loader label lines?>`, `<Embers>`, `<Markdown text assetBase?>`, `<SparkBurst trigger>`, `<MoltenBar percent>`, `<ErrorBox error>`, `<Toaster>`
  - Libraries: `alertUser(msg)`, `notificationsUndecided()`, `askNotifications()`, `randomQuote()`, `addQuotes()`
  - Theme ids `forge|anvil|quench|contrast` via `<html data-theme>`; calm motion via `<html data-calm="true">` plus `MotionConfig reducedMotion="always"`.

- [ ] **Step 1: Create the Vite app and install dependencies**

```bash
cd /Users/adelin/Projects/Crucible
npm create vite@latest web -- --template react-ts
cd web
npm install
npm install react-router @xterm/xterm @xterm/addon-fit motion react-markdown remark-gfm
npm install -D vitest
rm -rf src/App.css src/index.css src/assets public/vite.svg
npm pkg set scripts.test="vitest run"
```

- [ ] **Step 2: Config and entry files**

`web/vite.config.ts`:
```ts
/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/auth': 'http://localhost:8080',
    },
  },
  test: { environment: 'node' },
})
```

`web/index.html`:
```html
<!doctype html>
<html lang="en" data-theme="forge">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Crucible</title>
    <link rel="preconnect" href="https://fonts.googleapis.com" />
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />
    <link href="https://fonts.googleapis.com/css2?family=Cinzel:wght@600;700&family=Inter:wght@400;500;600&family=JetBrains+Mono&display=swap" rel="stylesheet" />
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`web/src/main.tsx`:
```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import '@xterm/xterm/css/xterm.css'
import './theme/tokens.css'
import './theme/app.css'
import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
```

- [ ] **Step 3: API helpers and types**

`web/src/api.ts`:
```ts
export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}

export async function api<T>(path: string, init: RequestInit & { json?: unknown } = {}): Promise<T> {
  const { json, ...rest } = init
  const res = await fetch(path, {
    ...rest,
    credentials: 'same-origin',
    headers: json !== undefined ? { 'Content-Type': 'application/json' } : rest.headers,
    body: json !== undefined ? JSON.stringify(json) : rest.body,
  })
  if (res.status === 401) {
    window.location.href = '/auth/login'
    throw new ApiError(401, 'login required')
  }
  const text = await res.text()
  const body = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new ApiError(res.status, body?.error ?? res.statusText)
  return body as T
}
```

`web/src/useFetch.ts`:
```ts
import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'

export function useFetch<T>(path: string | null, intervalMs?: number) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<ApiError>()
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (!path) return
    let live = true
    api<T>(path)
      .then((d) => live && (setData(d), setError(undefined)))
      .catch((e: ApiError) => live && setError(e))
    return () => {
      live = false
    }
  }, [path, tick])
  useEffect(() => {
    if (!intervalMs) return
    const id = setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  const reload = useCallback(() => setTick((t) => t + 1), [])
  return { data, error, reload }
}
```

`web/src/types.ts`:
```ts
export type User = { id: number; email: string; name: string; theme: string; calm_motion: boolean }
export type Me = { user: User; is_admin: boolean; default_theme: string }
export type ProgramCard = { team: string; team_name: string; training: string; title: string; description: string; percent: number; available: boolean }
export type ItemView = { kind: 'reading' | 'quiz' | 'lab'; id: string; title: string; status: 'new' | 'in_progress' | 'complete' }
export type ModuleView = { id: string; title: string; locked: boolean; complete: boolean; items: ItemView[] }
export type Outline = { team: string; training: string; title: string; description: string; progression: string; percent: number; modules: ModuleView[] }
export type Choice = { id: number; text: string }
export type PublicQuestion = { id: string; type: string; prompt: string; points: number; options?: Choice[]; left?: string[]; right?: Choice[]; human?: boolean }
export type QuizView = { pass_threshold: number; questions: PublicQuestion[]; status: string }
export type QuizResult = { score: number; max: number; percent: number; passed: boolean; correct: Record<string, boolean>; pending_human: boolean }
export type Terminal = { name: string; service: string }
export type TaskStatus = 'locked' | 'open' | 'setup_failed' | 'passed' | 'skipped'
export type TaskView = {
  id: string; title: string; status: TaskStatus; kind: 'check' | 'quiz' | 'review'; points: number; awarded: number
  quiz_prompt?: string; has_setup: boolean; hints_total: number; hints_revealed: number; next_hint_cost: number
}
export type LabState = 'provisioning' | 'ready' | 'destroying' | 'destroyed' | 'failed'
export type LabView = {
  id: string; state: LabState; error?: string; runtime: string; team: string; training: string; module: string
  terminals: Terminal[]; task_order: string; tasks: TaskView[]; server_now: string; ends_at?: string
  limit_reason?: string; end_reason?: string; idle_deadline?: string; idle_warning_s: number
  can_extend: boolean; self_reported: boolean; complete: boolean; score: number; max_score: number
}
export type TaskDetail = TaskView & { instructions: string; hints: string[] | null; setup_error?: string }
export type CheckResult = { passed: boolean; output: string; timed_out: boolean; awarded: number; lab: LabView }
export type HintResult = { index: number; text: string; cost: number; lab: LabView }
export type ModuleLab = { title: string; runtime: string; runtime_ready: boolean; runtime_message?: string; lab: LabView | null }
```

- [ ] **Step 4: Theme tokens and base styles**

`web/src/theme/theme.ts`:
```ts
export const THEMES = [
  { id: 'forge', name: 'Forge', hint: 'Dark charcoal, molten ember' },
  { id: 'anvil', name: 'Anvil', hint: 'Light steel, iron blue' },
  { id: 'quench', name: 'Quench', hint: 'Deep navy, cool cyan' },
  { id: 'contrast', name: 'High Contrast', hint: 'Maximum legibility' },
] as const

export function applyTheme(theme: string, calm: boolean) {
  document.documentElement.dataset.theme = theme
  document.documentElement.dataset.calm = String(calm)
}
```

`web/src/theme/tokens.css`:
```css
:root, [data-theme='forge'] {
  --bg: #14110f; --surface: #1f1a17; --surface-2: #2a231f; --text: #f3e9df; --muted: #b5a596;
  --accent: #ff7a1a; --accent-2: #ffc24b; --danger: #ff5a4d; --ok: #7bd88f; --border: #3a302a;
  --glow: 0 0 24px rgba(255, 122, 26, 0.45); --term-bg: #0f0c0a; --term-fg: #f3e9df;
}
[data-theme='anvil'] {
  --bg: #eef1f4; --surface: #ffffff; --surface-2: #e3e8ee; --text: #1c232b; --muted: #55616e;
  --accent: #2f5d8a; --accent-2: #c26b1e; --danger: #c62828; --ok: #2e7d32; --border: #cbd3dc;
  --glow: 0 0 18px rgba(47, 93, 138, 0.25); --term-bg: #1c232b; --term-fg: #eef1f4;
}
[data-theme='quench'] {
  --bg: #0b1424; --surface: #111e33; --surface-2: #182a45; --text: #e6f1ff; --muted: #93a8c6;
  --accent: #3cc8e8; --accent-2: #7fe3c5; --danger: #ff6b81; --ok: #7fe3c5; --border: #24395a;
  --glow: 0 0 22px rgba(60, 200, 232, 0.4); --term-bg: #070e1a; --term-fg: #e6f1ff;
}
[data-theme='contrast'] {
  --bg: #000; --surface: #000; --surface-2: #111; --text: #fff; --muted: #e6e6e6;
  --accent: #ffd400; --accent-2: #00e5ff; --danger: #ff6b6b; --ok: #5dff7a; --border: #fff;
  --glow: none; --term-bg: #000; --term-fg: #fff;
}
```

`web/src/theme/app.css`:
```css
* { box-sizing: border-box; }
html, body, #root { height: 100%; margin: 0; }
body { background: var(--bg); color: var(--text); font: 15px/1.6 Inter, system-ui, sans-serif; }
h1, h2, h3 { font-family: Cinzel, Georgia, serif; letter-spacing: 0.02em; line-height: 1.25; }
a { color: var(--accent); }
code, pre { font-family: 'JetBrains Mono', monospace; }
:focus-visible { outline: 3px solid var(--accent-2); outline-offset: 2px; }
.muted { color: var(--muted); }
.error { color: var(--danger); }
.warn { color: var(--accent-2); }
.pass { color: var(--ok); }
.spacer { flex: 1; }
.row { display: flex; gap: 0.75rem; align-items: center; flex-wrap: wrap; margin: 1rem 0; position: relative; }
.center { display: grid; place-items: center; min-height: 60vh; text-align: center; }

button { font: inherit; border-radius: 8px; padding: 0.5rem 1rem; cursor: pointer; border: 1px solid var(--border); background: var(--surface-2); color: var(--text); }
button:disabled { opacity: 0.5; cursor: not-allowed; }
button.primary { background: linear-gradient(135deg, var(--accent), var(--accent-2)); color: #1a0f05; border: none; font-weight: 600; box-shadow: var(--glow); }
button.primary.big { font-size: 1.15rem; padding: 0.8rem 1.8rem; }
button.ghost { background: transparent; }
input, select, textarea { font: inherit; color: var(--text); background: var(--surface-2); border: 1px solid var(--border); border-radius: 6px; padding: 0.45rem 0.6rem; }

.nav { display: flex; gap: 1rem; align-items: center; padding: 0.6rem 1.25rem; background: var(--surface); border-bottom: 1px solid var(--border); }
.nav a { color: var(--muted); text-decoration: none; }
.nav a.active, .nav a:hover { color: var(--text); }
.brand { font-family: Cinzel, serif; font-size: 1.2rem; color: var(--text) !important; }
.brand-mark { color: var(--accent); text-shadow: var(--glow); }

.page { max-width: 960px; margin: 0 auto; padding: 2rem 1.25rem; position: relative; }
.lede { font-size: 1.1rem; color: var(--muted); }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 1rem; }
.card { display: block; padding: 1.25rem; border-radius: 14px; background: var(--surface); border: 1px solid var(--border); color: var(--text); text-decoration: none; transition: transform 0.2s, box-shadow 0.2s; }
.card:hover { transform: translateY(-3px); box-shadow: var(--glow); }

.molten-bar { height: 10px; border-radius: 999px; background: var(--surface-2); overflow: hidden; margin: 0.75rem 0 0.25rem; }
.molten-bar .fill { height: 100%; background: linear-gradient(90deg, var(--accent), var(--accent-2)); box-shadow: var(--glow); }

.modules { list-style: none; padding: 0; }
.module { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 1rem 1.25rem; margin: 1rem 0; }
.module.locked { opacity: 0.6; }
.module.complete { border-color: var(--ok); }
.module ul { list-style: none; padding: 0; }
.item { display: flex; justify-content: space-between; padding: 0.35rem 0; border-top: 1px dashed var(--border); }
.badge { font-size: 0.75rem; padding: 0.1rem 0.55rem; border-radius: 999px; background: var(--surface-2); color: var(--muted); }
.badge.complete { color: var(--ok); }
.badge.in_progress { color: var(--accent-2); }
.badge.warn { color: var(--accent-2); }

.prose { max-width: 72ch; }
.prose pre { background: var(--surface-2); padding: 0.75rem; border-radius: 8px; overflow: auto; }
.prose blockquote { border-left: 3px solid var(--accent); margin: 0; padding-left: 1rem; color: var(--muted); }

.question { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 1rem 1.25rem; margin: 1rem 0; }
.question legend { font-weight: 600; }
.question.right { border-color: var(--ok); }
.question.wrong { border-color: var(--danger); }
.question label { display: block; margin: 0.3rem 0; }
.order-list { list-style: none; padding: 0; }
.order-list li { display: flex; gap: 0.5rem; align-items: center; margin: 0.25rem 0; }
.result { padding: 1rem; border-radius: 10px; background: var(--surface); border: 1px solid var(--border); }

/* loader */
.loader { position: relative; display: grid; place-items: center; gap: 0.75rem; min-height: 50vh; text-align: center; overflow: hidden; }
.crucible { width: 110px; filter: drop-shadow(0 0 18px rgba(255, 122, 26, 0.35)); }
.crucible-shell { fill: none; stroke: var(--muted); stroke-width: 4; }
.molten { fill: var(--accent); animation: fill 2.4s ease-in-out infinite; }
@keyframes fill { 0% { transform: translateY(70px); fill: var(--accent); } 50% { fill: var(--accent-2); } 100% { transform: translateY(0); fill: var(--accent); } }
.loader blockquote { font-style: italic; color: var(--muted); max-width: 46ch; margin: 0; }
.loader-label { font-family: Cinzel, serif; font-size: 1.1rem; }
.loader-log { text-align: left; max-width: 640px; max-height: 160px; overflow: auto; background: var(--surface-2); padding: 0.5rem; border-radius: 6px; font-size: 0.8rem; }

/* embers */
.embers { position: absolute; inset: 0; pointer-events: none; overflow: hidden; z-index: 0; }
.embers span { position: absolute; bottom: -10px; border-radius: 50%; background: var(--accent-2); box-shadow: 0 0 8px var(--accent); animation: rise linear infinite; opacity: 0; }
@keyframes rise { 0% { transform: translateY(0); opacity: 0; } 15% { opacity: 0.9; } 100% { transform: translateY(-110vh) translateX(30px); opacity: 0; } }

/* sparks */
.sparks { position: absolute; left: 50%; top: 50%; pointer-events: none; }
.sparks span { position: absolute; width: 6px; height: 6px; border-radius: 50%; background: var(--accent-2); box-shadow: 0 0 10px var(--accent); }

/* settings */
.themes { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 0.75rem; }
.themes button { display: grid; gap: 0.25rem; text-align: left; }
.themes button[aria-checked='true'] { border-color: var(--accent); box-shadow: var(--glow); }
.swatch-preview { display: block; height: 36px; border-radius: 6px; background: linear-gradient(90deg, var(--bg) 0 40%, var(--accent) 40% 70%, var(--accent-2) 70%); border: 1px solid var(--border); }

/* toasts */
.toasts { position: fixed; right: 1rem; bottom: 1rem; display: grid; gap: 0.5rem; z-index: 50; }
.toast { background: var(--surface); border: 1px solid var(--accent); border-radius: 10px; padding: 0.6rem 1rem; box-shadow: var(--glow); }

/* calm forge + reduced motion: no ambient animation */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { animation: none !important; transition: none !important; }
  .embers { display: none; }
}
[data-calm='true'] *, [data-calm='true'] *::before, [data-calm='true'] *::after { animation: none !important; transition: none !important; }
[data-calm='true'] .embers { display: none; }
```

- [ ] **Step 5: Shared library and components**

`web/src/lib/quotes.ts`:
```ts
const builtIn = [
  'Steel is forged in fire.',
  "The crucible doesn't break the metal. It reveals it.",
  'Every master was once a lump of ore.',
  'Heat, hammer, repeat.',
  'Pressure makes diamonds; heat makes blades.',
  'Strike while the iron is hot.',
  'A blade is only as good as its tempering.',
  'Sparks fly when skill meets effort.',
]
let quotes = [...builtIn]

export function addQuotes(extra: string[]) {
  quotes = Array.from(new Set([...quotes, ...extra]))
}

export function randomQuote(): string {
  return quotes[Math.floor(Math.random() * quotes.length)]
}
```

`web/src/lib/alerts.ts`:
```ts
const BASE_TITLE = 'Crucible'

export function toast(msg: string) {
  window.dispatchEvent(new CustomEvent('crucible-toast', { detail: msg }))
}

// alertUser shows an in-page toast and, when the tab is hidden, a browser notification + title badge (spec §8.6).
export function alertUser(msg: string, short = msg) {
  toast(msg)
  if (!document.hidden) return
  document.title = `⏳ ${short} · ${BASE_TITLE}`
  if ('Notification' in window && Notification.permission === 'granted') new Notification(BASE_TITLE, { body: msg })
  const restore = () => {
    document.title = BASE_TITLE
    document.removeEventListener('visibilitychange', restore)
  }
  document.addEventListener('visibilitychange', restore)
}

export function notificationsUndecided(): boolean {
  return 'Notification' in window && Notification.permission === 'default'
}

export async function askNotifications() {
  await Notification.requestPermission()
}
```

`web/src/components/Toaster.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'motion/react'

export function Toaster() {
  const [items, setItems] = useState<{ id: number; msg: string }[]>([])
  useEffect(() => {
    const on = (e: Event) => {
      const id = Date.now() + Math.random()
      setItems((xs) => [...xs, { id, msg: (e as CustomEvent<string>).detail }])
      setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== id)), 6000)
    }
    window.addEventListener('crucible-toast', on)
    return () => window.removeEventListener('crucible-toast', on)
  }, [])
  return (
    <div className="toasts" role="status" aria-live="polite">
      <AnimatePresence>
        {items.map((t) => (
          <motion.div key={t.id} className="toast" initial={{ opacity: 0, y: 12 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}>
            {t.msg}
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}
```

`web/src/components/Embers.tsx`:
```tsx
import { useMemo } from 'react'

export function Embers({ count = 18 }: { count?: number }) {
  const embers = useMemo(
    () => Array.from({ length: count }, (_, i) => ({ i, left: Math.random() * 100, delay: Math.random() * 6, dur: 4 + Math.random() * 4, size: 2 + Math.random() * 3 })),
    [count],
  )
  return (
    <div className="embers" aria-hidden="true">
      {embers.map((e) => (
        <span key={e.i} style={{ left: `${e.left}%`, animationDelay: `${e.delay}s`, animationDuration: `${e.dur}s`, width: e.size, height: e.size }} />
      ))}
    </div>
  )
}
```

`web/src/components/Loader.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { randomQuote } from '../lib/quotes'
import { Embers } from './Embers'

const BOWL = 'M20 30 h80 l-10 70 a10 10 0 0 1 -10 8 h-40 a10 10 0 0 1 -10 -8z'

export function Loader({ label, lines }: { label: string; lines?: string[] }) {
  const [quote, setQuote] = useState(randomQuote)
  useEffect(() => {
    const id = setInterval(() => setQuote(randomQuote()), 4500)
    return () => clearInterval(id)
  }, [])
  return (
    <div className="loader" role="status" aria-live="polite">
      <Embers />
      <svg className="crucible" viewBox="0 0 120 120" aria-hidden="true">
        <defs>
          <clipPath id="bowl">
            <path d={BOWL} />
          </clipPath>
        </defs>
        <g clipPath="url(#bowl)">
          <rect className="molten" x="0" y="30" width="120" height="90" />
        </g>
        <path className="crucible-shell" d={BOWL} />
      </svg>
      <p className="loader-label">{label}</p>
      <AnimatePresence mode="wait">
        <motion.blockquote key={quote} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.5 }}>
          “{quote}”
        </motion.blockquote>
      </AnimatePresence>
      {lines && lines.length > 0 && <pre className="loader-log">{lines.join('\n')}</pre>}
    </div>
  )
}
```

`web/src/components/Markdown.tsx`:
```tsx
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'

// assetBase rewrites `assets/x.png` links to the training's asset endpoint.
export function Markdown({ text, assetBase }: { text: string; assetBase?: string }) {
  return (
    <div className="prose">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={(url) => (assetBase && url.startsWith('assets/') ? `${assetBase}/${url.slice('assets/'.length)}` : defaultUrlTransform(url))}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
```

`web/src/components/SparkBurst.tsx`:
```tsx
import { motion } from 'motion/react'

export function SparkBurst({ trigger }: { trigger: number }) {
  if (!trigger) return null
  return (
    <div className="sparks" aria-hidden="true" key={trigger}>
      {Array.from({ length: 14 }, (_, i) => {
        const a = (i / 14) * Math.PI * 2
        return (
          <motion.span key={i} initial={{ x: 0, y: 0, opacity: 1, scale: 1 }} animate={{ x: Math.cos(a) * 70, y: Math.sin(a) * 70, opacity: 0, scale: 0.4 }} transition={{ duration: 0.8, ease: 'easeOut' }} />
        )
      })}
    </div>
  )
}
```

`web/src/components/MoltenBar.tsx`:
```tsx
import { motion } from 'motion/react'

export function MoltenBar({ percent }: { percent: number }) {
  return (
    <div>
      <div className="molten-bar" role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} aria-label="Progress">
        <motion.div className="fill" initial={{ width: 0 }} animate={{ width: `${percent}%` }} transition={{ duration: 1.2, ease: 'easeOut' }} />
      </div>
      <small className="muted">{percent}% forged</small>
    </div>
  )
}
```

`web/src/components/ErrorBox.tsx`:
```tsx
import { Link } from 'react-router'
import { ApiError } from '../api'

export function ErrorBox({ error }: { error: ApiError }) {
  const msg = error.status === 423 ? 'Finish the earlier modules first.' : error.message
  return (
    <div className="center">
      <div>
        <h2>The forge sputtered</h2>
        <p className="error">{msg}</p>
        <Link to="/">Back to the Hearth</Link>
      </div>
    </div>
  )
}
```

`web/src/components/Nav.tsx`:
```tsx
import { Link, NavLink } from 'react-router'
import { useMe } from '../App'

export function Nav() {
  const { me } = useMe()
  const logout = async () => {
    await fetch('/auth/logout', { method: 'POST' })
    window.location.href = '/auth/login'
  }
  return (
    <nav className="nav" aria-label="Main">
      <Link to="/" className="brand">
        <span className="brand-mark">⚒</span> Crucible
      </Link>
      <NavLink to="/" end>Hearth</NavLink>
      <NavLink to="/connect">Connect your laptop</NavLink>
      <NavLink to="/settings">Settings</NavLink>
      <span className="spacer" />
      <span className="muted">{me.user.name || me.user.email}</span>
      <button className="ghost" onClick={logout}>Log out</button>
    </nav>
  )
}
```

- [ ] **Step 6: App shell and settings page**

`web/src/App.tsx`:
```tsx
import { createContext, useContext, useEffect, useState } from 'react'
import { Link, Route, Routes } from 'react-router'
import { MotionConfig } from 'motion/react'
import { api } from './api'
import type { Me } from './types'
import { applyTheme } from './theme/theme'
import { addQuotes } from './lib/quotes'
import { Loader } from './components/Loader'
import { Nav } from './components/Nav'
import { Toaster } from './components/Toaster'
import { Hearth } from './pages/Hearth'
import { TrainingPage } from './pages/Training'
import { ReadingPage } from './pages/Reading'
import { QuizPage } from './pages/Quiz'
import { LabPage } from './pages/Lab'
import { ConnectPage } from './pages/Connect'
import { SettingsPage } from './pages/Settings'

type MeCtx = { me: Me; setPrefs: (theme: string, calm: boolean) => Promise<void> }
const MeContext = createContext<MeCtx | null>(null)

export function useMe(): MeCtx {
  const v = useContext(MeContext)
  if (!v) throw new Error('useMe outside <App>')
  return v
}

export default function App() {
  const [me, setMe] = useState<Me>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    api<Me>('/api/me')
      .then((m) => {
        setMe(m)
        applyTheme(m.user.theme || m.default_theme, m.user.calm_motion)
      })
      .catch((e: Error) => setError(e.message))
    api<{ quotes: string[] }>('/api/meta').then((m) => addQuotes(m.quotes ?? [])).catch(() => {})
  }, [])

  if (error) return <div className="center"><p className="error">{error}</p></div>
  if (!me) return <Loader label="Stoking the forge…" />

  const setPrefs = async (theme: string, calm: boolean) => {
    await api('/api/me/prefs', { method: 'PUT', json: { theme, calm_motion: calm } })
    setMe({ ...me, user: { ...me.user, theme, calm_motion: calm } })
    applyTheme(theme, calm)
  }
  return (
    <MeContext.Provider value={{ me, setPrefs }}>
      <MotionConfig reducedMotion={me.user.calm_motion ? 'always' : 'user'}>
        <Nav />
        <Routes>
          <Route path="/" element={<Hearth />} />
          <Route path="/p/:team/:training" element={<TrainingPage />} />
          <Route path="/p/:team/:training/m/:module/read/:item" element={<ReadingPage />} />
          <Route path="/p/:team/:training/m/:module/quiz" element={<QuizPage />} />
          <Route path="/p/:team/:training/m/:module/lab" element={<LabPage />} />
          <Route path="/connect" element={<ConnectPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<div className="center"><div><h1>Lost in the smoke</h1><Link to="/">Back to the Hearth</Link></div></div>} />
        </Routes>
        <Toaster />
      </MotionConfig>
    </MeContext.Provider>
  )
}
```

`web/src/pages/Settings.tsx`:
```tsx
import { useMe } from '../App'
import { THEMES } from '../theme/theme'

export function SettingsPage() {
  const { me, setPrefs } = useMe()
  const theme = me.user.theme || me.default_theme
  return (
    <section className="page">
      <h1>Settings</h1>
      <h2>Theme</h2>
      <div className="themes" role="radiogroup" aria-label="Theme">
        {THEMES.map((t) => (
          <button key={t.id} role="radio" aria-checked={theme === t.id} onClick={() => setPrefs(t.id, me.user.calm_motion)}>
            <span className="swatch-preview" data-theme={t.id} />
            <strong>{t.name}</strong>
            <small className="muted">{t.hint}</small>
          </button>
        ))}
      </div>
      <h2>Motion</h2>
      <label>
        <input type="checkbox" checked={me.user.calm_motion} onChange={(e) => setPrefs(theme, e.target.checked)} /> Calm forge (turn off animations)
      </label>
    </section>
  )
}
```

Create one-line stubs so the app compiles until Tasks 18–19 replace them, for example `web/src/pages/Hearth.tsx`:
```tsx
export function Hearth() { return <section className="page"><h1>Hearth</h1></section> }
```
Create the same stub for `Training.tsx` (`TrainingPage`), `Reading.tsx` (`ReadingPage`), `Quiz.tsx` (`QuizPage`), `Lab.tsx` (`LabPage`) and `Connect.tsx` (`ConnectPage`), each returning `<section className="page" />`.

- [ ] **Step 7: Build to verify**

Run: `cd web && npm run build`
Expected: `vite build` finishes with `dist/index.html` written and no TypeScript errors.

- [ ] **Step 8: Commit**

```bash
git add web
git commit -m "feat(web): app shell with forge themes, calm motion, quote loader and settings"
```

---

### Task 18: Frontend learning pages (Hearth, Training, Reading, Quiz)

**Files:**
- Modify (replace stubs): `web/src/pages/Hearth.tsx`, `web/src/pages/Training.tsx`, `web/src/pages/Reading.tsx`, `web/src/pages/Quiz.tsx`

**Interfaces:**
- Consumes: Task 10 endpoints; `useFetch`, `api`, `Loader`, `MoltenBar`, `Markdown`, `SparkBurst`, `ErrorBox`, `Embers`.
- Produces (selectors used by the e2e test in Task 20):
  - Training links named by card title; `data-testid="module-<id>"` with `data-locked`.
  - Reading links named by title; button "Mark as read".
  - Quiz fields:
    - single/multi: inputs labelled by option text
    - exact/regex: `data-testid="answer-<qid>"`
    - order: list `data-testid="order-<qid>"` with buttons "Move <text> up/down"
    - match: selects labelled by the left text
  - Quiz actions: button "Submit answers"; result region with text "Passed" or "Not yet".

- [ ] **Step 1: Hearth**

`web/src/pages/Hearth.tsx`:
```tsx
import { useMemo } from 'react'
import { Link } from 'react-router'
import { useMe } from '../App'
import { useFetch } from '../useFetch'
import type { ProgramCard } from '../types'
import { randomQuote } from '../lib/quotes'
import { Embers } from '../components/Embers'
import { Loader } from '../components/Loader'
import { MoltenBar } from '../components/MoltenBar'

export function Hearth() {
  const { me } = useMe()
  const { data, error } = useFetch<ProgramCard[]>('/api/programs')
  const quote = useMemo(randomQuote, [])
  const first = (me.user.name || me.user.email).split(/[ @]/)[0]
  return (
    <section className="page">
      <Embers count={12} />
      <h1>Hearth</h1>
      <p className="lede">
        Welcome back, {first}. <em>“{quote}”</em>
      </p>
      {error && <p className="error">{error.message}</p>}
      {!data && !error && <Loader label="Gathering your trainings…" />}
      {data && data.length === 0 && <p className="muted">You're not enrolled in any training yet. Your team leader can enroll you.</p>}
      {data && data.length > 0 && (
        <div className="cards">
          {data.map((c) => (
            <Link key={c.team + c.training} to={`/p/${c.team}/${c.training}`} className="card" aria-label={c.title}>
              <h2>{c.title}</h2>
              <p className="muted">{c.team_name}</p>
              <p>{c.description}</p>
              <MoltenBar percent={c.percent} />
              {!c.available && <p className="warn">Content unavailable right now</p>}
            </Link>
          ))}
        </div>
      )}
    </section>
  )
}
```

- [ ] **Step 2: Training outline**

`web/src/pages/Training.tsx`:
```tsx
import { Link, useParams } from 'react-router'
import { useFetch } from '../useFetch'
import type { ItemView, Outline } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { MoltenBar } from '../components/MoltenBar'

const statusLabel = { new: 'Cold', in_progress: 'Heating', complete: 'Forged' } as const

function itemPath(base: string, module: string, it: ItemView) {
  return it.kind === 'reading' ? `${base}/${module}/read/${it.id}` : `${base}/${module}/${it.kind}`
}

export function TrainingPage() {
  const { team, training } = useParams()
  const { data, error } = useFetch<Outline>(`/api/programs/${team}/${training}`)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the blueprint…" />
  const base = `/p/${team}/${training}/m`
  return (
    <section className="page">
      <Link to="/">← Hearth</Link>
      <h1>{data.title}</h1>
      <p className="lede">{data.description}</p>
      <MoltenBar percent={data.percent} />
      <ol className="modules">
        {data.modules.map((m) => (
          <li key={m.id} className={`module ${m.locked ? 'locked' : ''} ${m.complete ? 'complete' : ''}`} data-testid={`module-${m.id}`} data-locked={m.locked}>
            <h2>
              <span aria-hidden="true">{m.locked ? '🔒' : m.complete ? '✦' : '◆'}</span> {m.title}
            </h2>
            <ul>
              {m.items.map((it) => (
                <li key={it.kind + it.id} className="item">
                  {m.locked ? (
                    <span className="muted">{it.kind === 'reading' ? it.title : it.kind === 'quiz' ? 'Quiz' : 'Lab'}</span>
                  ) : (
                    <Link to={itemPath(base, m.id, it)}>{it.kind === 'reading' ? it.title : it.kind === 'quiz' ? 'Quiz' : 'Lab'}</Link>
                  )}
                  <span className={`badge ${it.status}`}>{statusLabel[it.status]}</span>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
    </section>
  )
}
```

- [ ] **Step 3: Reading**

`web/src/pages/Reading.tsx`:
```tsx
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'

export function ReadingPage() {
  const { team, training, module, item } = useParams()
  const nav = useNavigate()
  const path = `/api/programs/${team}/${training}/modules/${module}/reading/${item}`
  const { data, error } = useFetch<{ title: string; markdown: string }>(path)
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Unrolling the scroll…" />
  const markRead = async () => {
    setBusy(true)
    await api(`${path}/read`, { method: 'POST' })
    nav(`/p/${team}/${training}`)
  }
  return (
    <article className="page">
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <Markdown text={data.markdown} assetBase={`/api/programs/${team}/${training}/assets`} />
      <button className="primary" disabled={busy} onClick={markRead}>Mark as read</button>
    </article>
  )
}
```

- [ ] **Step 4: Quiz**

`web/src/pages/Quiz.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { PublicQuestion, QuizResult, QuizView } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { SparkBurst } from '../components/SparkBurst'

type Answers = Record<string, unknown>

function Question({ q, n, value, onChange, verdict }: { q: PublicQuestion; n: number; value: unknown; onChange: (v: unknown) => void; verdict?: boolean }) {
  const cls = `question ${verdict === undefined ? '' : verdict ? 'right' : 'wrong'}`
  const legend = (
    <legend>
      {n}. {q.prompt} {verdict !== undefined && <span aria-label={verdict ? 'correct' : 'incorrect'}>{verdict ? '✓' : '✗'}</span>}
    </legend>
  )
  switch (q.type) {
    case 'single':
      return (
        <fieldset className={cls}>
          {legend}
          {q.options!.map((o) => (
            <label key={o.id}>
              <input type="radio" name={q.id} checked={value === o.id} onChange={() => onChange(o.id)} /> {o.text}
            </label>
          ))}
        </fieldset>
      )
    case 'multi': {
      const set = new Set((value as number[] | undefined) ?? [])
      return (
        <fieldset className={cls}>
          {legend}
          {q.options!.map((o) => (
            <label key={o.id}>
              <input type="checkbox" checked={set.has(o.id)} onChange={(e) => { const s = new Set(set); if (e.target.checked) s.add(o.id); else s.delete(o.id); onChange([...s]) }} /> {o.text}
            </label>
          ))}
        </fieldset>
      )
    }
    case 'exact':
    case 'regex':
      return (
        <fieldset className={cls}>
          {legend}
          <input data-testid={`answer-${q.id}`} aria-label={q.prompt} value={(value as string) ?? ''} onChange={(e) => onChange(e.target.value)} />
        </fieldset>
      )
    case 'order': {
      const ids = (value as number[]) ?? q.options!.map((o) => o.id)
      const text = (id: number) => q.options!.find((o) => o.id === id)!.text
      const move = (i: number, d: number) => { const next = [...ids]; [next[i], next[i + d]] = [next[i + d], next[i]]; onChange(next) }
      return (
        <fieldset className={cls}>
          {legend}
          <ol className="order-list" data-testid={`order-${q.id}`}>
            {ids.map((id, i) => (
              <li key={id}>
                <span>{text(id)}</span>
                <button type="button" className="ghost" aria-label={`Move ${text(id)} up`} disabled={i === 0} onClick={() => move(i, -1)}>↑</button>
                <button type="button" className="ghost" aria-label={`Move ${text(id)} down`} disabled={i === ids.length - 1} onClick={() => move(i, 1)}>↓</button>
              </li>
            ))}
          </ol>
        </fieldset>
      )
    }
    case 'match': {
      const picks = (value as number[]) ?? q.left!.map(() => -1)
      return (
        <fieldset className={cls}>
          {legend}
          {q.left!.map((left, i) => (
            <label key={left}>
              {left}{' '}
              <select aria-label={left} value={picks[i]} onChange={(e) => { const next = [...picks]; next[i] = Number(e.target.value); onChange(next) }}>
                <option value={-1}>Choose…</option>
                {q.right!.map((r) => <option key={r.id} value={r.id}>{r.text}</option>)}
              </select>
            </label>
          ))}
        </fieldset>
      )
    }
    default:
      return (
        <fieldset className={cls}>
          {legend}
          <p className="muted">A person scores this question; that arrives in a later release.</p>
        </fieldset>
      )
  }
}

export function QuizPage() {
  const { team, training, module } = useParams()
  const base = `/api/programs/${team}/${training}/modules/${module}/quiz`
  const { data, error } = useFetch<QuizView>(base)
  const [answers, setAnswers] = useState<Answers>({})
  const [result, setResult] = useState<QuizResult>()
  const [spark, setSpark] = useState(0)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (!data) return
    const init: Answers = {}
    for (const q of data.questions) {
      if (q.type === 'order') init[q.id] = q.options!.map((o) => o.id)
      if (q.type === 'match') init[q.id] = q.left!.map(() => -1)
    }
    setAnswers(init)
  }, [data])
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Heating the test piece…" />
  const submit = async () => {
    setBusy(true)
    try {
      const r = await api<QuizResult>(`${base}/attempts`, { method: 'POST', json: { answers } })
      setResult(r)
      if (r.passed) setSpark((s) => s + 1)
    } finally {
      setBusy(false)
    }
  }
  const pct = result ? Math.round(result.percent * 100) : 0
  return (
    <section className="page">
      <Link to={`/p/${team}/${training}`}>← Back to the training</Link>
      <h1>Prove your temper</h1>
      <p className="muted">Pass mark: {Math.round(data.pass_threshold * 100)}%</p>
      {data.questions.map((q, i) => (
        <Question key={q.id} q={q} n={i + 1} value={answers[q.id]} onChange={(v) => setAnswers((a) => ({ ...a, [q.id]: v }))} verdict={result?.correct[q.id]} />
      ))}
      <div className="row">
        <button className="primary" disabled={busy} onClick={submit}>Submit answers</button>
        <SparkBurst trigger={spark} />
      </div>
      {result && (
        <div role="status" className={`result ${result.passed ? 'pass' : 'fail'}`}>
          {result.passed ? `Passed: ${pct}%. Tempered!` : `Not yet: ${pct}%. Reheat and try again.`}{' '}
          {result.passed && <Link to={`/p/${team}/${training}`}>Back to the training</Link>}
        </div>
      )}
    </section>
  )
}
```

- [ ] **Step 5: Build to verify**

Run: `cd web && npm run build`
Expected: build succeeds with no TypeScript errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/pages
git commit -m "feat(web): hearth, training outline, reading and quiz pages"
```

---

### Task 19: Frontend lab workspace (tasks, tabbed terminals, timer, idle prompt) and Connect page

**Files:**
- Create: `web/src/lib/timer.ts`, `web/src/lib/timer.test.ts`, `web/src/components/{Terminal,Timer,IdleModal}.tsx`
- Modify (replace stubs): `web/src/pages/Lab.tsx`, `web/src/pages/Connect.tsx`

**Interfaces:**
- Consumes: Task 15 endpoints, Task 16 agent endpoints; `alertUser`, `Markdown`, `SparkBurst`, `Loader`, `Embers`.
- Produces:
  - Timer functions: `clockOffset(serverNowIso, clientNow)`, `remainingMs(endsAtIso, offset, now)`, `timerState(ms)`, `formatRemaining(ms)`, `crossedWarnings(prevMs, ms)`, `idleWarningVisible(deadlineIso, warnS, offset, now)`.
  - Selectors used by e2e:
    - Connect page: button "Generate pairing token", `data-testid="pairing-token"`, text "Agent connected"
    - Lab start: button "Ignite the forge"
    - Lab workspace: tabs (role `tab`) named by terminal; terminal containers `[data-terminal="<tab>"]`
    - Task actions: buttons "Check", "Hint …", "Reset scenario", "End lab"; input labelled "Your answer"
    - Status text: "Lab forged", `data-testid="lab-timer"`, "The forge has cooled"

- [ ] **Step 1: Write the failing timer tests**

`web/src/lib/timer.test.ts`:
```ts
import { describe, expect, it } from 'vitest'
import { clockOffset, crossedWarnings, formatRemaining, idleWarningVisible, remainingMs, timerState } from './timer'

const MIN = 60_000

describe('lab timer', () => {
  it('uses the server clock even when the laptop clock is 10 minutes fast', () => {
    const serverNow = '2026-10-05T09:00:00Z'
    const laptopNow = Date.parse(serverNow) + 10 * MIN // laptop is ahead
    const offset = clockOffset(serverNow, laptopNow)
    expect(remainingMs('2026-10-05T09:30:00Z', offset, laptopNow)).toBe(30 * MIN)
    expect(remainingMs('2026-10-05T09:30:00Z', offset, laptopNow + 31 * MIN)).toBe(0)
  })

  it('moves through normal → cooling → critical → expired', () => {
    expect(timerState(16 * MIN)).toBe('normal')
    expect(timerState(15 * MIN)).toBe('cooling')
    expect(timerState(5 * MIN)).toBe('critical')
    expect(timerState(0)).toBe('expired')
  })

  it('fires each warning exactly once when crossing 15 and 5 minutes', () => {
    expect(crossedWarnings(15 * MIN + 500, 15 * MIN - 500)).toEqual([15])
    expect(crossedWarnings(14 * MIN, 13 * MIN)).toEqual([])
    expect(crossedWarnings(5 * MIN + 1, 5 * MIN)).toEqual([5])
  })

  it('formats remaining time', () => {
    expect(formatRemaining(65 * MIN + 5000)).toBe('1:05:05')
    expect(formatRemaining(4 * MIN + 9000)).toBe('04:09')
    expect(formatRemaining(0)).toBe('00:00')
  })

  it('shows the idle prompt only inside the warning window', () => {
    const now = Date.parse('2026-10-05T09:00:00Z')
    expect(idleWarningVisible('2026-10-05T09:06:00Z', 300, 0, now)).toBe(false)
    expect(idleWarningVisible('2026-10-05T09:04:00Z', 300, 0, now)).toBe(true)
    expect(idleWarningVisible('2026-10-05T08:59:00Z', 300, 0, now)).toBe(false)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test`
Expected: FAIL (`./timer` cannot be resolved).

- [ ] **Step 3: Implement the timer library**

`web/src/lib/timer.ts`:
```ts
export type TimerState = 'normal' | 'cooling' | 'critical' | 'expired'

const MIN = 60_000

// offset = server clock − client clock; add it to Date.now() to get server time.
export function clockOffset(serverNowIso: string, clientNow: number): number {
  return Date.parse(serverNowIso) - clientNow
}

export function remainingMs(endsAtIso: string, offset: number, clientNow: number): number {
  return Math.max(0, Date.parse(endsAtIso) - (clientNow + offset))
}

export function timerState(ms: number): TimerState {
  if (ms <= 0) return 'expired'
  if (ms <= 5 * MIN) return 'critical'
  if (ms <= 15 * MIN) return 'cooling'
  return 'normal'
}

export function formatRemaining(ms: number): string {
  const s = Math.ceil(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = String(Math.floor((s % 3600) / 60)).padStart(2, '0')
  const sec = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${m}:${sec}` : `${m}:${sec}`
}

export function crossedWarnings(prevMs: number, ms: number): number[] {
  return [15, 5].filter((min) => prevMs > min * MIN && ms <= min * MIN)
}

export function idleWarningVisible(deadlineIso: string, warnS: number, offset: number, now: number): boolean {
  const left = Date.parse(deadlineIso) - (now + offset)
  return left > 0 && left <= warnS * 1000
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test`
Expected: 5 tests pass.

- [ ] **Step 5: Terminal, Timer, IdleModal components**

`web/src/components/Terminal.tsx`:
```tsx
import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function Terminal({ labId, name, tabKey, active }: { labId: string; name: string; tabKey: string; active: boolean }) {
  const el = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      fontSize: 14,
      theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg'), cursor: cssVar('--accent') },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el.current!)
    try { fit.fit() } catch { /* hidden tab: fitted when shown */ }
    let ws: WebSocket | null = null
    let closed = false
    let retry = 0
    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/labs/${labId}/terminals/${encodeURIComponent(name)}/ws?cols=${term.cols}&rows=${term.rows}`)
      ws.binaryType = 'arraybuffer'
      ws.onmessage = (e) => term.write(new Uint8Array(e.data as ArrayBuffer))
      ws.onclose = () => {
        if (closed) return
        term.write('\r\n\x1b[33m[forge] connection lost, reconnecting…\x1b[0m\r\n')
        retry = window.setTimeout(connect, 2000)
      }
    }
    connect()
    const enc = new TextEncoder()
    const input = term.onData((d) => ws?.readyState === WebSocket.OPEN && ws.send(enc.encode(d)))
    const ro = new ResizeObserver(() => {
      if (!el.current?.offsetWidth) return
      fit.fit()
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }))
    })
    ro.observe(el.current!)
    return () => {
      closed = true
      clearTimeout(retry)
      input.dispose()
      ro.disconnect()
      ws?.close()
      term.dispose()
    }
  }, [labId, name])
  return <div ref={el} className="terminal" data-terminal={tabKey} style={{ display: active ? 'block' : 'none' }} />
}
```

`web/src/components/Timer.tsx`:
```tsx
import { useEffect, useRef, useState } from 'react'
import type { LabView } from '../types'
import { crossedWarnings, formatRemaining, remainingMs, timerState } from '../lib/timer'
import { alertUser } from '../lib/alerts'

export function useNow(intervalMs = 1000) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

function limitLabel(lab: LabView) {
  if (!lab.ends_at) return ''
  const at = new Date(lab.ends_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (lab.limit_reason === 'schedule') return `Ends at schedule close, ${at}`
  if (lab.limit_reason === 'budget') return `Ends at the budget cap, ${at}`
  return `Ends at ${at}`
}

export function Timer({ lab, offset, onExtend }: { lab: LabView; offset: number; onExtend: () => void }) {
  const now = useNow()
  const ms = lab.ends_at ? remainingMs(lab.ends_at, offset, now) : 0
  const prev = useRef(ms)
  useEffect(() => {
    for (const m of crossedWarnings(prev.current, ms)) alertUser(`${m} minutes left before your lab cools down`, `${m} min`)
    prev.current = ms
  }, [ms])
  return (
    <div className={`timer ${timerState(ms)}`} title={limitLabel(lab)}>
      <span aria-hidden="true">⏳</span> <strong data-testid="lab-timer">{formatRemaining(ms)}</strong>
      <small className="muted">{limitLabel(lab)}</small>
      {lab.can_extend && <button className="ghost" onClick={onExtend}>Extend</button>}
    </div>
  )
}
```

`web/src/components/IdleModal.tsx`:
```tsx
import { useEffect } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import type { LabView } from '../types'
import { formatRemaining, idleWarningVisible } from '../lib/timer'
import { alertUser } from '../lib/alerts'
import { useNow } from './Timer'

export function IdleModal({ lab, offset, onHere }: { lab: LabView; offset: number; onHere: () => void }) {
  const now = useNow()
  const visible = !!lab.idle_deadline && idleWarningVisible(lab.idle_deadline, lab.idle_warning_s, offset, now)
  const left = lab.idle_deadline ? Date.parse(lab.idle_deadline) - (now + offset) : 0
  useEffect(() => {
    if (visible) alertUser('Are you still there? Your lab is cooling down.', 'Still there?')
  }, [visible])
  return (
    <AnimatePresence>
      {visible && (
        <motion.div className="modal-backdrop" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
          <div className="modal" role="alertdialog" aria-modal="true" aria-labelledby="idle-title">
            <h2 id="idle-title">Are you still there?</h2>
            <p>
              The forge is cooling… your lab closes in <strong>{formatRemaining(left)}</strong>.
            </p>
            <button className="primary" autoFocus onClick={onHere}>I'm here</button>
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
```

Append to `web/src/theme/app.css`:
```css
/* lab workspace */
.lab { display: grid; grid-template-rows: auto auto 1fr; height: calc(100vh - 53px); }
.lab-head { display: flex; gap: 0.75rem; align-items: center; padding: 0.5rem 1rem; background: var(--surface); border-bottom: 1px solid var(--border); }
.lab-head h1 { font-size: 1.1rem; margin: 0; }
.lab-body { display: grid; min-height: 0; }
.tasks { overflow: auto; padding: 1rem 1.25rem; background: var(--surface); position: relative; }
.task-pips { display: flex; gap: 0.4rem; list-style: none; padding: 0; margin: 0 0 1rem; }
.pip { width: 2.2rem; height: 2.2rem; border-radius: 50%; padding: 0; }
.pip.passed { background: var(--ok); color: #071; border-color: var(--ok); }
.pip.skipped { opacity: 0.6; text-decoration: line-through; }
.pip.current { box-shadow: var(--glow); border-color: var(--accent); }
.splitter { cursor: col-resize; background: var(--border); }
.splitter:hover, .splitter:focus-visible { background: var(--accent); }
.terms { display: grid; grid-template-rows: auto 1fr; min-height: 0; background: var(--term-bg); }
.tabs { display: flex; gap: 0.25rem; padding: 0.35rem; background: var(--surface-2); }
.tabs [role='tab'][aria-selected='true'] { border-color: var(--accent); color: var(--accent); }
.terminal { height: 100%; padding: 0.4rem; }
.hint { border-left: 3px solid var(--accent-2); padding-left: 0.75rem; margin: 0.75rem 0; }
.check-output { white-space: pre-wrap; padding: 0.75rem; border-radius: 8px; background: var(--surface-2); }
.check-output.ok { border-left: 3px solid var(--ok); }
.check-output.bad { border-left: 3px solid var(--danger); }
.quiz-input { display: grid; gap: 0.4rem; margin: 1rem 0; }
.banner { padding: 0.5rem 1rem; text-align: center; background: var(--surface-2); border-bottom: 1px solid var(--border); }
.banner.pass { color: var(--ok); }
.timer { display: flex; gap: 0.4rem; align-items: center; padding: 0.2rem 0.7rem; border-radius: 999px; border: 1px solid var(--border); }
.timer.cooling { border-color: var(--accent-2); color: var(--accent-2); box-shadow: 0 0 14px rgba(255, 194, 75, 0.35); }
.timer.critical, .timer.expired { border-color: var(--danger); color: var(--danger); animation: pulse 1.6s ease-in-out infinite; }
@keyframes pulse { 50% { box-shadow: 0 0 18px var(--danger); } }
.modal-backdrop { position: fixed; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.55); z-index: 40; }
.modal { background: var(--surface); border: 1px solid var(--accent); border-radius: 14px; padding: 1.5rem 2rem; box-shadow: var(--glow); max-width: 420px; text-align: center; }
.lobby { text-align: center; }
.cooled { margin: 1.5rem auto; max-width: 520px; padding: 1rem; border-radius: 12px; background: var(--surface); border: 1px solid var(--border); }
pre.command { background: var(--surface-2); padding: 0.75rem; border-radius: 8px; overflow: auto; }
```

- [ ] **Step 6: Lab page**

`web/src/pages/Lab.tsx`:
```tsx
import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Link, useParams } from 'react-router'
import { motion } from 'motion/react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { CheckResult, HintResult, LabView, ModuleLab, TaskDetail } from '../types'
import { clockOffset } from '../lib/timer'
import { askNotifications, notificationsUndecided } from '../lib/alerts'
import { Embers } from '../components/Embers'
import { ErrorBox } from '../components/ErrorBox'
import { IdleModal } from '../components/IdleModal'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'
import { SparkBurst } from '../components/SparkBurst'
import { Terminal } from '../components/Terminal'
import { Timer } from '../components/Timer'

const endMessages: Record<string, string> = {
  idle: 'Your lab was closed after a period of inactivity.',
  ttl: 'Time ran out on this lab.',
  user: 'You ended the lab.',
  provision_timeout: 'The lab took too long to start.',
}

function firstOpen(lab: LabView): string {
  return (lab.tasks.find((t) => t.status === 'open' || t.status === 'setup_failed') ?? lab.tasks[lab.tasks.length - 1]).id
}

export function LabPage() {
  const { team, training, module } = useParams()
  const modPath = `/api/programs/${team}/${training}/modules/${module}/lab`
  const { data: info, error } = useFetch<ModuleLab>(modPath)
  const [lab, setLab] = useState<LabView | null>(null)
  const [starting, setStarting] = useState(false)
  const [startErr, setStartErr] = useState<string>()
  useEffect(() => {
    if (info) setLab(info.lab)
  }, [info])
  useEffect(() => {
    if (!lab || lab.state === 'destroyed' || lab.state === 'failed') return
    const id = setInterval(async () => {
      try {
        setLab(await api<LabView>(`/api/labs/${lab.id}`))
      } catch { /* transient; next poll retries */ }
    }, lab.state === 'provisioning' ? 2000 : 5000)
    return () => clearInterval(id)
  }, [lab?.id, lab?.state])

  const back = `/p/${team}/${training}`
  if (error) return <ErrorBox error={error} />
  if (!info) return <Loader label="Opening the workshop…" />
  if (lab?.state === 'provisioning') return <Loader label="Heating the crucible: provisioning your lab…" />
  if (lab && (lab.state === 'ready' || lab.state === 'destroying')) return <LabWorkspace lab={lab} setLab={setLab} title={info.title} back={back} />

  const start = async () => {
    setStarting(true)
    setStartErr(undefined)
    try {
      setLab(await api<LabView>(modPath, { method: 'POST' }))
    } catch (e) {
      setStartErr((e as Error).message)
    } finally {
      setStarting(false)
    }
  }
  const cooled = lab?.state === 'destroyed'
  return (
    <section className="page lobby">
      <Embers />
      <Link to={back}>← Back to the training</Link>
      <h1>{info.title}</h1>
      {cooled && lab && (
        <div className="cooled" role="status">
          <h2>The forge has cooled</h2>
          <p>{endMessages[lab.end_reason ?? ''] ?? 'This lab has ended.'} Your progress is saved; passed tasks stay passed.</p>
          <p>
            Score {lab.score} / {lab.max_score} · {lab.tasks.filter((t) => t.status === 'passed').length} of {lab.tasks.length} tasks passed · hints used{' '}
            {lab.tasks.reduce((n, t) => n + t.hints_revealed, 0)}
          </p>
        </div>
      )}
      {lab?.state === 'failed' && <p className="error">The lab failed to start: {lab.error}</p>}
      {!info.runtime_ready && (
        <p className="warn">
          {info.runtime_message} {info.runtime === 'local' && <Link to="/connect">Connect your laptop</Link>}
        </p>
      )}
      {startErr && <p className="error">{startErr}</p>}
      <button className="primary big" disabled={!info.runtime_ready || starting} onClick={start}>
        {cooled ? 'Ignite again' : 'Ignite the forge'}
      </button>
    </section>
  )
}

function LabWorkspace({ lab, setLab, title, back }: { lab: LabView; setLab: (l: LabView) => void; title: string; back: string }) {
  const [split, setSplit] = useState(40)
  const [taskId, setTaskId] = useState(() => firstOpen(lab))
  const [active, setActive] = useState(lab.terminals[0].name)
  const [extra, setExtra] = useState<{ key: string; name: string }[]>([])
  const [askAlerts, setAskAlerts] = useState(() => notificationsUndecided() && localStorage.getItem('crucible-alerts') !== 'no')
  const offset = useMemo(() => clockOffset(lab.server_now, Date.now()), [lab.server_now])
  const tabs = [...lab.terminals.map((t) => ({ key: t.name, name: t.name })), ...extra]

  const lastBeat = useRef(0)
  const beat = () => {
    if (Date.now() - lastBeat.current < 60_000) return
    lastBeat.current = Date.now()
    api(`/api/labs/${lab.id}/activity`, { method: 'POST' }).catch(() => {})
  }
  const end = async () => {
    if (!confirm('End this lab? Your progress is kept.')) return
    setLab(await api<LabView>(`/api/labs/${lab.id}`, { method: 'DELETE' }))
  }
  const extend = async () => setLab(await api<LabView>(`/api/labs/${lab.id}/extend`, { method: 'POST' }))
  const addShell = () => {
    const name = tabs.find((t) => t.key === active)?.name ?? lab.terminals[0].name
    const key = `${name} #${extra.filter((e) => e.name === name).length + 2}`
    setExtra((x) => [...x, { key, name }])
    setActive(key)
  }
  const startDrag = (e: ReactPointerEvent<HTMLDivElement>) => {
    const body = e.currentTarget.parentElement!
    e.currentTarget.setPointerCapture(e.pointerId)
    const move = (ev: PointerEvent) => {
      const r = body.getBoundingClientRect()
      setSplit(Math.min(70, Math.max(25, ((ev.clientX - r.left) / r.width) * 100)))
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  return (
    <div className="lab">
      <header className="lab-head">
        <Link to={back} aria-label="Back to the training">←</Link>
        <h1>{title}</h1>
        <span className="badge">{lab.runtime === 'local' ? 'Your laptop' : lab.runtime}</span>
        {lab.self_reported && <span className="badge warn" title="Checks run on your own machine">self-reported</span>}
        <span className="spacer" />
        <Timer lab={lab} offset={offset} onExtend={extend} />
        <button className="ghost" onClick={end}>End lab</button>
      </header>
      <div>
        {lab.complete && <div className="banner pass" role="status">✦ Lab forged: {lab.score} / {lab.max_score} points</div>}
        {askAlerts && (
          <div className="banner">
            Get a heads-up when your lab is about to cool down, even from another tab.{' '}
            <button className="ghost" onClick={async () => { await askNotifications(); setAskAlerts(false) }}>Enable alerts</button>
            <button className="ghost" onClick={() => { localStorage.setItem('crucible-alerts', 'no'); setAskAlerts(false) }}>No thanks</button>
          </div>
        )}
      </div>
      <div className="lab-body" style={{ gridTemplateColumns: `${split}% 6px 1fr` }}>
        <aside className="tasks" onPointerDown={beat} onKeyDown={beat} onScroll={beat}>
          <ol className="task-pips">
            {lab.tasks.map((t, i) => (
              <li key={t.id}>
                <button className={`pip ${t.status} ${t.id === taskId ? 'current' : ''}`} disabled={t.status === 'locked'} onClick={() => setTaskId(t.id)} aria-label={`Task ${i + 1}: ${t.title} (${t.status})`}>
                  {i + 1}
                </button>
              </li>
            ))}
          </ol>
          <TaskPanel key={taskId} lab={lab} taskId={taskId} onLab={setLab} onAdvance={(l) => setTaskId(firstOpen(l))} />
        </aside>
        <div
          className="splitter" role="separator" aria-orientation="vertical" aria-label="Resize panels" aria-valuenow={Math.round(split)} tabIndex={0}
          onPointerDown={startDrag}
          onKeyDown={(e) => {
            if (e.key === 'ArrowLeft') setSplit((s) => Math.max(25, s - 2))
            if (e.key === 'ArrowRight') setSplit((s) => Math.min(70, s + 2))
          }}
        />
        <section className="terms">
          <div className="tabs" role="tablist" aria-label="Terminals">
            {tabs.map((t) => (
              <button key={t.key} role="tab" aria-selected={active === t.key} onClick={() => setActive(t.key)}>{t.key}</button>
            ))}
            <button className="ghost" aria-label="Open another shell" onClick={addShell}>+</button>
          </div>
          {tabs.map((t) => <Terminal key={t.key} labId={lab.id} name={t.name} tabKey={t.key} active={active === t.key} />)}
        </section>
      </div>
      <IdleModal lab={lab} offset={offset} onHere={async () => setLab(await api<LabView>(`/api/labs/${lab.id}/activity`, { method: 'POST' }))} />
    </div>
  )
}

function TaskPanel({ lab, taskId, onLab, onAdvance }: { lab: LabView; taskId: string; onLab: (l: LabView) => void; onAdvance: (l: LabView) => void }) {
  const task = lab.tasks.find((t) => t.id === taskId)!
  const [detail, setDetail] = useState<TaskDetail>()
  const [err, setErr] = useState<string>()
  const [answer, setAnswer] = useState('')
  const [output, setOutput] = useState<{ ok: boolean; text: string }>()
  const [busy, setBusy] = useState(false)
  const [spark, setSpark] = useState(0)
  const [shake, setShake] = useState(0)
  const base = `/api/labs/${lab.id}/tasks/${taskId}`

  useEffect(() => {
    api<TaskDetail>(base).then(setDetail).catch((e: Error) => setErr(e.message))
  }, [base])

  if (err) return <p className="error">{err}</p>
  if (!detail) return <Loader label={task.has_setup && task.status === 'open' ? 'Preparing scenario…' : 'Reading the runes…'} />

  const done = task.status === 'passed' || task.status === 'skipped'
  const check = async () => {
    setBusy(true)
    try {
      const r = await api<CheckResult>(`${base}/check`, { method: 'POST', json: { answer } })
      setOutput({ ok: r.passed, text: r.output || (r.passed ? 'Passed.' : 'Not yet.') })
      onLab(r.lab)
      if (r.passed) {
        setSpark((s) => s + 1)
        setTimeout(() => onAdvance(r.lab), 1200)
      } else setShake((s) => s + 1)
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  const hint = async () => {
    const cost = task.next_hint_cost
    if (cost > 0 && !confirm(`This hint costs ${cost} point${cost === 1 ? '' : 's'}. Reveal it?`)) return
    const r = await api<HintResult>(`${base}/hint`, { method: 'POST' })
    setDetail((d) => d && { ...d, hints: [...(d.hints ?? []), r.text] })
    onLab(r.lab)
  }
  const reset = async () => {
    try {
      onLab(await api<LabView>(`${base}/reset`, { method: 'POST' }))
      setOutput({ ok: true, text: 'Scenario reset.' })
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    }
  }
  const skip = async () => {
    const l = await api<LabView>(`${base}/skip`, { method: 'POST' })
    onLab(l)
    onAdvance(l)
  }

  return (
    <motion.div key={shake} animate={shake ? { x: [0, -8, 8, -5, 5, 0] } : {}} transition={{ duration: 0.4 }}>
      <Markdown text={detail.instructions} />
      {detail.setup_error && <p className="warn">{detail.setup_error}</p>}
      {task.kind === 'quiz' && (
        <label className="quiz-input">
          {task.quiz_prompt}
          <input aria-label="Your answer" value={answer} disabled={done} onChange={(e) => setAnswer(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && check()} />
        </label>
      )}
      {(detail.hints ?? []).map((h, i) => (
        <div key={i} className="hint">
          <strong>Hint {i + 1}</strong>
          <Markdown text={h} />
        </div>
      ))}
      <div className="row">
        {task.kind !== 'review' && task.status === 'open' && (
          <button className="primary" disabled={busy} onClick={check}>{busy ? 'Checking…' : 'Check'}</button>
        )}
        {task.kind === 'review' && <span className="muted">A scorer reviews this task.</span>}
        {!done && task.hints_revealed < task.hints_total && (
          <button className="ghost" onClick={hint}>Hint {task.next_hint_cost > 0 ? `(−${task.next_hint_cost} pts)` : '(free)'}</button>
        )}
        {!done && task.has_setup && <button className="ghost" onClick={reset}>Reset scenario</button>}
        {task.status === 'setup_failed' && <button className="ghost" onClick={skip}>Skip task</button>}
        <SparkBurst trigger={spark} />
      </div>
      {task.status === 'passed' && <p className="pass">✦ Passed: {task.awarded} / {task.points} points</p>}
      {output && <pre className={`check-output ${output.ok ? 'ok' : 'bad'}`} role="status">{output.text}</pre>}
    </motion.div>
  )
}
```

- [ ] **Step 7: Connect page**

`web/src/pages/Connect.tsx`:
```tsx
import { useState } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'

export function ConnectPage() {
  const [cmd, setCmd] = useState<{ token: string; command: string }>()
  const { data: status } = useFetch<{ online: boolean }>('/api/agent/status', 3000)
  return (
    <section className="page">
      <h1>Connect your laptop</h1>
      <p>
        Local labs run in Docker on your own machine through <code>crucible-agent</code>. You need Docker with the compose plugin (macOS, Linux, or Windows with WSL2).
      </p>
      <p className={status?.online ? 'pass' : 'muted'} role="status">
        {status?.online ? '● Agent connected. Your forge is lit.' : '○ No agent connected yet.'}
      </p>
      <ol>
        <li>Get <code>crucible-agent</code> for your platform from your admin (or build it with <code>make build</code>).</li>
        <li>Generate a pairing token. A new token revokes the previous one.</li>
        <li>Run the command below and leave it running while you do labs.</li>
      </ol>
      <button className="primary" onClick={async () => setCmd(await api('/api/agent/tokens', { method: 'POST' }))}>Generate pairing token</button>
      {cmd && (
        <>
          <pre className="command">{cmd.command}</pre>
          <p className="muted">
            Token (shown once): <code data-testid="pairing-token">{cmd.token}</code>
          </p>
          <button className="ghost" onClick={() => navigator.clipboard.writeText(cmd.command)}>Copy command</button>
        </>
      )}
    </section>
  )
}
```

- [ ] **Step 8: Build and test**

Run: `cd web && npm test && npm run build`
Expected: 5 Vitest tests pass; the build succeeds.

- [ ] **Step 9: Commit**

```bash
git add web
git commit -m "feat(web): KodeKloud-style lab workspace with tabbed terminals, timer, idle prompt; connect page"
```

---

### Task 20: The local check (`make local-check`)

**Files:**
- Create: `scripts/local-check.sh`, `e2e/package.json`, `e2e/playwright.config.ts`, `e2e/tests/forge-101.spec.ts`
- Modify: `README.md` (create) with how to run the local check

**Interfaces:**
- Consumes: everything. This is the M1 acceptance test: it uses only local labs.
- Produces: `make local-check`, which exits 0 only if this whole flow passes:
  1. Lint the content.
  2. Build and start the stack.
  3. SSO login, then pair an agent.
  4. Read, then pass the quiz.
  5. Unlock module 2 and start the local lab.
  6. Terminals, checks, break-fix setup, terminal quiz, hint, timer.
  7. End the lab and confirm progress was saved.

- [ ] **Step 1: Write the end-to-end test**

`e2e/package.json`:
```json
{
  "name": "crucible-e2e",
  "private": true,
  "devDependencies": { "@playwright/test": "^1.56.0" }
}
```

`e2e/playwright.config.ts`:
```ts
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  timeout: 8 * 60_000,
  expect: { timeout: 20_000 },
  workers: 1,
  use: { baseURL: 'http://localhost:8080', trace: 'retain-on-failure', viewport: { width: 1440, height: 900 } },
})
```

`e2e/tests/forge-101.spec.ts`:
```ts
import { expect, test, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  const term = page.locator(`[data-terminal="${tab}"]`)
  await term.click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

async function sortOrder(page: Page, testId: string, target: string[]) {
  for (let i = 0; i < target.length; i++) {
    const current = (await page.getByTestId(testId).locator('li > span').allTextContents()).map((s) => s.trim())
    for (let j = current.indexOf(target[i]); j > i; j--) {
      await page.getByRole('button', { name: `Move ${target[i]} up` }).click()
    }
  }
}

test('a trainee completes Forge 101 using only a local lab', async ({ page }) => {
  // SSO login through Keycloak
  await page.goto('/')
  await page.locator('#username').fill('trainee')
  await page.locator('#password').fill('trainee')
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()

  // Pair the laptop agent
  await page.getByRole('link', { name: 'Connect your laptop' }).click()
  await page.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await page.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080', '--token', token], { stdio: 'inherit' })
  await expect(page.getByText('Agent connected')).toBeVisible({ timeout: 30_000 })

  // Module 1: read + quiz; module 2 starts locked
  await page.getByRole('link', { name: 'Hearth', exact: true }).click()
  await page.getByRole('link', { name: /Forge 101/ }).click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveAttribute('data-locked', 'true')
  await page.getByRole('link', { name: 'How We Work' }).click()
  await page.getByRole('button', { name: 'Mark as read' }).click()
  await page.getByTestId('module-01-welcome').getByRole('link', { name: 'Quiz' }).click()

  await page.getByLabel('docker ps').check()
  for (const r of ['Docker Hub', 'GitHub Container Registry', 'Amazon ECR']) await page.getByLabel(r).check()
  await page.getByTestId('answer-q-port').fill('80')
  await page.getByTestId('answer-q-version').fill('1.2.3')
  await sortOrder(page, 'order-q-order', ['Heat', 'Hammer', 'Quench', 'Temper'])
  await page.getByLabel('git', { exact: true }).selectOption({ label: 'version control' })
  await page.getByLabel('docker', { exact: true }).selectOption({ label: 'containers' })
  await page.getByLabel('terraform', { exact: true }).selectOption({ label: 'infrastructure as code' })
  await page.getByRole('button', { name: 'Submit answers' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Passed' })).toBeVisible()

  // Module 2 unlocked: read, then the local lab
  await page.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveAttribute('data-locked', 'false')
  await page.getByRole('link', { name: 'Before the Lab' }).click()
  await page.getByRole('button', { name: 'Mark as read' }).click()
  await page.getByTestId('module-02-first-lab').getByRole('link', { name: 'Lab' }).click()
  await page.getByRole('button', { name: 'Ignite the forge' }).click()
  await expect(page.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 }) // first run pulls images
  await expect(page.getByTestId('lab-timer')).toHaveText(/\d{2}:\d{2}/)

  // Task 1: create the file in the shell terminal, then Check
  await typeIn(page, 'shell', "echo 'hello forge' > /tmp/forged.txt")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Your first ingot is cast.')).toBeVisible()

  // Task 2: setup breaks nginx; answer the terminal quiz
  await expect(page.getByLabel('Your answer')).toBeVisible({ timeout: 90_000 })
  await page.getByLabel('Your answer').fill('8081')
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('Correct: the forge moved to 8081.')).toBeVisible()

  // Task 3: reveal the first hint, fix nginx in the web terminal, Check
  await expect(page.getByText('Put it back')).toBeVisible({ timeout: 30_000 })
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: /^Hint/ }).click()
  await expect(page.getByText('Hint 1')).toBeVisible()
  await typeIn(page, 'web', "sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload")
  await page.getByRole('button', { name: 'Check' }).click()
  await expect(page.getByText('The forge burns bright on port 80 again.')).toBeVisible()
  await expect(page.getByText(/Lab forged/)).toBeVisible()

  // End the lab: summary shows, progress kept
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'End lab' }).click()
  await expect(page.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })
  await page.getByRole('link', { name: 'Back to the training' }).click()
  await expect(page.getByTestId('module-02-first-lab')).toHaveClass(/complete/)
})
```

- [ ] **Step 2: Write the runner script**

`scripts/local-check.sh`:
```bash
#!/usr/bin/env bash
# M1 acceptance: build everything, start Postgres + Keycloak + Crucible, and drive a trainee
# through Forge 101 using only a local (laptop) lab. KEEP=1 leaves the stack running afterwards.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

for bin in docker go node npm git; do
  command -v "$bin" >/dev/null || { echo "missing prerequisite: $bin"; exit 1; }
done
docker compose version >/dev/null || { echo "Docker compose plugin is required"; exit 1; }

echo "== build"
make build
(cd web && npm ci && npm test)

echo "== lint content"
./bin/crucible lint examples/forge-101
./bin/crucible lint examples/platform

echo "== seed git repos"
./scripts/seed-git.sh

compose="docker compose -f deploy/compose/docker-compose.yml"
cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then
    $compose down -v >/dev/null 2>&1 || true
  else
    echo "KEEP=1: stack left running at http://localhost:8080 (users: trainee/trainee, leader/leader)"
  fi
}
trap cleanup EXIT

echo "== start stack"
$compose up -d --build --wait

echo "== end-to-end"
(cd e2e && npm install --no-audit --no-fund && npx playwright install chromium \
  && CRUCIBLE_AGENT="$root/bin/crucible-agent" npx playwright test)

echo "🔥 Local check passed. The forge holds."
```

```bash
chmod +x scripts/local-check.sh
```

`README.md`:
````markdown
# Crucible

Forge new teammates from zero to hero: reading, quizzes, and hands-on labs, all authored in git.

## Local check (no cloud needed)

Prerequisites: Docker with the compose plugin, Go 1.26, Node 24, git.

```bash
make local-check          # build, start Postgres + Keycloak + Crucible, run the Forge 101 end-to-end test
KEEP=1 make local-check   # same, but leave the stack running at http://localhost:8080
```

Local users (username = password): `trainee`, `senior`, `leader`, `admin`.
To try a lab by hand: log in as `trainee`, open **Connect your laptop**, generate a token and run the
printed `./bin/crucible-agent …` command in a terminal.

## Authoring

Content lives in git: see `examples/forge-101` and the spec in `docs/superpowers/specs/`.
Validate a content repo with `go run ./cmd/crucible lint <dir>`.
````

- [ ] **Step 3: Run the local check**

Run: `make local-check`
Expected (last lines): Playwright reports `1 passed`, then `🔥 Local check passed. The forge holds.`

If it fails, run `KEEP=1 make local-check` and look at `docker compose -f deploy/compose/docker-compose.yml logs api` and the Playwright trace (`e2e/test-results/`).

- [ ] **Step 4: Commit**

```bash
git add scripts/local-check.sh e2e README.md
git commit -m "test: end-to-end local check of Forge 101 using only a laptop lab"
```

---

## Self-review notes (what M1 deliberately leaves for later milestones)

| Deferred | Where it lands |
|---|---|
| Approval flow, cost tiers, schedules, budgets, kill switch, notifications, River job queue | M3 |
| `cluster` runtime (sysbox pods, NetworkPolicy blocking instance metadata) | M4 |
| Human scoring (text / upload / signoff / review tasks), terminal transcripts | M5 |
| `aws` runtime, infracost, reaper, Cost Explorer, FinOps dashboard | M6 |
| Forge ranks, mentor dashboard, journey heat map, UI content-edit review, `crucible preview`, Forge Status page, config write-back | M7 |
| AWS deployment (k3s on EC2, backups, sleep/wake/teardown) | M2 |
