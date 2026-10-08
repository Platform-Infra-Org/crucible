# Docs Tab, VS Code-Style Editor and Building-Blocks Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an in-app Docs tab that explains every capability by role and stays current (a coverage test fails on gaps). Replace the textarea content editor with a Monaco IDE: drafts are autosaved on the server, edits become put/rename/delete operations, and a Blocks catalog writes valid content through registry-generated forms.

**Architecture:**
- `internal/content/blocks` is the registry. In Task 2 it describes every YAML field and generates the JSON Schema per file kind. In Task 15 it gains the catalog of insertable blocks. `content.Load` and lint stay the authority on validity.
- `gitsync` grows `Op` and the guards and appliers every edit goes through (`CheckOps`, `ApplyOps`). `edits` stores ops instead of a files map, and still accepts the old map for one release. `ContentRepo.PushEdit` commits ops and diffs with rename detection.
- A new package, `internal/authoring`, serves schema, validate, drafts, files-at-base, blocks and insert. It reuses the edit permission rules (`edits.Service.Authorize`, so nobody enrolled gets anything).
- A new package, `internal/docs`, serves the Markdown pages in `docs/user`, embedded through `crucible/docs/user`, plus pages generated from the registry.
- The web IDE is a lazily loaded route chunk (`web/src/pages/editor/*`) built on `monaco-editor` and `monaco-yaml`. Their workers are imported with Vite's `?worker`, so they are same-origin files and the CSP does not change. A build step asserts that the main chunk has no Monaco in it.

**Tech Stack:** Go 1.26, pgx v5, goose, chi, gopkg.in/yaml.v3, git CLI (no new Go dependencies). React 19 + Vite 8 + Vitest 5 (`react-dom/server` for component tests). New web dependencies, versions checked with `npm view` on 2026-10-07: `monaco-editor@0.57.0`, `monaco-yaml@5.5.1` (peer `monaco-editor >=0.36`), `yaml@2.9.1` (client-side preview parsing; already a dependency of monaco-yaml), `diff@9.0.0` (the Changes panel's unified diff; it ships its own types). Playwright for e2e.

**Spec:** `docs/superpowers/specs/2026-10-07-docs-and-editor-design.md`. It builds on `docs/superpowers/specs/2026-10-05-crucible-design.md` §6 and §12, and on the M7 content-edit work. Read `CLAUDE.md` first: toolchain, commands, hard rules, invariants.

## Global Constraints

- Every shell that builds or tests starts with `cd /Users/adelin/Projects/Crucible && export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH`. Docker Desktop must be running (Postgres testcontainers).
- Go checks: `gofmt -l cmd internal docs` prints nothing, `go vet ./...` is clean, `go test -race ./...` passes. Web checks: `cd web && npm test && npx tsc -b && npm run build && npm run lint`.
- **Commits:** pathspec form only. Run `git add -- <paths>` for new files, then `git commit -m "<subject>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- <paths>`. Never `git add -A`, `commit -a`, history rewrites or force-push. Branch: `feat/docs-and-editor`.
- **Migrations:** the next free numbers are `00018` (Task 7) and `00019` (Task 8). Run `ls internal/db/migrations` first. If someone took them, use the next free ones. Always write a Down. Never edit an existing migration.
- **Docs rule (from Task 4 on):** any capability change updates `docs/user` in the same commit. `go test ./internal/docs` (the coverage test) must pass at every commit from Task 5 on.
- Spec §5: "Monaco (the VS Code editor), loaded only on the editor page". "a build test asserts the main chunk does not include it. Its web workers are bundled as same-origin assets, so the CSP keeps `script-src 'self'` and no new hosts." "dark editor for Forge, Quench and High contrast; light for Anvil."
- Spec §5: "`POST /api/authoring/validate` runs about a second after typing stops". Spec §6: "autosave via `PUT` debounced about 2 s, with an `updated_at` compare-and-set". Limits: "5 open drafts per author; the same file-count and size caps as edits". Those caps are 20 files, 256 KiB per file, a diff of at most 256 KiB, and 1 MiB per request. Drafts are open only to people allowed to propose edits to that training, never to enrolled users.
- Spec §7, every op path: "`training.yaml` or `modules/<id>/…`; safe characters; no dot components; case-collision check against the tree and within the edit; executable only inside the lab directory named by `module.yaml`; no change to `maintainers`." "`training.yaml` cannot be deleted or renamed." Diff: "`git diff -M --text --no-ext-diff --no-textconv`, shows deleted files in full, and stays under the existing full-diff size cap (edits whose diff would be truncated are refused)." "Existing `files`-map requests are accepted and translated to `put` ops for one release, then removed."
- Spec §9: authoring endpoints refuse enrolled users (`rbac.Enrolled`). Validate and insert run in a temp copy with no network, the existing terraform-lint caps, one concurrent run per user and a timeout. The quiz preview shows answers only to editors.
- Spec §4: page front matter is `title`, `roles` (one of `trainee | author | scorer | approver | leader | admin | everyone`), `covers`, `order`. API: `GET /api/docs` and `GET /api/docs/{slug}`, open to any signed-in user. Pages never contain training content. Rendering uses the existing Markdown pipeline (no raw HTML).
- Non-goals (v1): running draft labs in the browser, binary uploads, org-specific docs, real-time co-editing, a full-text search service.
- The invariants in `CLAUDE.md` hold, especially: enrolled users never see answer keys, all git goes through `gitsync.git`, and the SPA's strict CSP stays as it is (no inline scripts, no external hosts).
- UI copy is written from the user's side of the screen, plainly, in the forge voice. New nav link names must not collide with link names in `e2e/tests/*.spec.ts` (`grep -n "getByRole('link'" e2e/tests/*.ts`).
- No new Go modules. The only new npm packages are the four named in Tech Stack.

## Review Focus

These are the five inputs the spec implies but no feature test exercises, most likely first. Each has a pinning test in the task that owns the code.

1. **A draft larger than the 1 MiB request cap** (one big pasted file, or many files). Today's `httpx.Read` silently truncates the body and answers "unexpected EOF". Expected: the server says "the request is over 1 MiB", and the editor warns before autosave ever sends it. Pinned in Task 8 (`TestReadRefusesOversizeBodies`) and Task 9 (`opsProblem` total-size test).
2. **The same draft open in two tabs.** Expected: the stale tab gets 409 and stops autosaving. It shows "changed in another tab, reload", never overwrites silently and never retries in a loop. Pinned in Task 8 (`TestDraftCompareAndSet`) and Task 9 (`afterSave` 409 test).
3. **Block form values with YAML-significant text** (quotes, `: `, `#`, a leading `-`, `{{ }}`, newlines). Expected: the value is stored literally, the file still loads, and no other key changes (`maintainers` above all). Pinned in Task 15 (`TestInsertValuesAreLiteral`).
4. **Inserting into a file the author left as broken YAML.** Expected: 400 "fix the YAML in <file> first", and the draft is untouched, never a 500. Pinned in Task 14 (`TestInsertBrokenYAML`) and Task 15 (`TestInsertIntoBrokenFile`).
5. **Rebasing when upstream deleted or renamed a file the draft changes.** Expected: a conflict the author resolves (keep mine / drop mine), never a 500 or a silent loss. Pinned in Task 8 (`TestRebaseConflictWhenUpstreamDeleted`).

## Rulings made in this plan (spec gaps, decided here)

1. **Ops apply order.** Renames first, in two phases: every source is read before any target is written, so an a↔b swap works. Then deletes, then puts. The server applies this order however the list is ordered, and the client always emits it.
2. **Base SHAs are never taken from the client on trust.** The bot's mirror (`clone --mirror`) also holds unmerged `crucible/edit/*` branches, so `Syncer.Version(sha)` could export someone else's unreviewed change. Authoring accepts a base only if it is the training's head or the `base_sha` of one of the caller's own drafts for that training.
3. **Draft lifecycle.** States are `editing`, `in_review` (linked edit pending), `returned` (linked edit rejected, withdrawn or stale) and merged. A `returned` draft is editable again, and its next save unlinks it. A merged draft is deleted the next time its author lists drafts. A draft in review can be discarded but not saved.
4. **`content_drafts` has no `team` column.** The spec lists one, but permission is per training (`canPropose`) and nothing reads a team. It gains `title` and `created_at`.
5. **Problem lines are heuristics.** `content.Problem` has no position. Authoring takes `line N` from YAML errors, finds `id: <x>` for `question N (x)` and `task x`, and points a "file not found" at the YAML line that names the missing file. Otherwise it uses line 1. Each heuristic carries a `ponytail:` comment.
6. **Git-only blocks.** UI edits may write only `.md/.yaml/.yml/.sh`, so the AWS lab template (it needs `terraform/main.tf`) and the AWS settings block are shown in the catalog with "add this in git" and refused by insert. "New training" is also git-only, because an edit cannot create a repo. Their examples are still load-tested.
7. **Case-only renames are refused,** as the existing case-clash rule already does for puts. Make them in git.
8. **Executable bit.** A new or renamed `.sh` inside the lab directory named by `module.yaml` is `100755`. Any other new or renamed path is `100644`. Puts on existing files keep their mode, as today.
9. **The registry ships in two parts.** Field docs and schema come first (delivery step 1). The catalog of insertable blocks follows in step 5, where its forms and inserts live.
10. **Generated docs pages.** At startup, `internal/docs` renders "Building blocks" pages from the compiled registry: one reference page in Task 5, then one page per catalog group in Task 15 (`authors/blocks/<group>`). "Last updated with Crucible `<version>`" reads `internal/docs.Version`, set by `-ldflags` in `make build` and the Dockerfile, with `dev` as the fallback.
11. **Covers.** `route:` covers must name a route in `web/src/App.tsx`, and every route there must be covered. `block:` covers must name a catalog block, and every block must be covered. `feature:` covers are free-form labels and are not checked.
12. **Roles in the client.** Everyone gets `everyone` and `trainee`. `can_edit_content` adds `author`, `can_score` adds `scorer`, `can_approve` adds `approver` and `leader`, and `is_admin` adds `admin`. Docs opens on the first page of the user's highest section: admin > leader/approver > scorer > author > trainee > everyone.
13. **The editor's reading preview has no asset base.** Draft images are not served yet; lint still checks the links.

---

## File Structure

**Go: new**
- `internal/gitsync/ops.go` (+ `ops_test.go`): `Op`, `CheckPath`, `CheckOps`, `PutOps`, `Targets`, `ApplyOps`. Guards and the applier shared by validate, drafts and push.
- `internal/content/blocks/fields.go`: `Field`, `Fields` (a description for every YAML key of the content types).
- `internal/content/blocks/schema.go`: `Kinds`, `Schema(kind)`, `YAMLFields`.
- `internal/content/blocks/registry_test.go`: reflection coverage, schema against `examples/`.
- `internal/content/blocks/catalog.go`: `Block`, `Insert`, `Append`, `Catalog`, `Find`, `Render`, `Apply` (Task 15).
- `internal/content/blocks/catalog_test.go`: every block's sample renders, loads and lints; inserts preserve text; values are literal.
- `internal/authoring/authoring.go`: `Service`, schema, validate, files at base, `Problem`, `locate`, the per-user `bounded` runner.
- `internal/authoring/drafts.go`: drafts CRUD, submit, discard, rebase.
- `internal/authoring/insert.go`: blocks list and insert (Task 15).
- `internal/authoring/http.go`: routes.
- `internal/authoring/*_test.go`.
- `internal/docs/docs.go` (+ `docs_test.go`): page loading, front matter, routes, `Version`.
- `internal/docs/generated.go`: registry-generated pages.
- `internal/docs/coverage_test.go`: the docs coverage test.
- `docs/user/embed.go`: package `userdocs`, `//go:embed */*.md`.
- `docs/user/<section>/<page>.md`: the pages.
- `internal/db/migrations/00018_edit_ops.sql`, `00019_content_drafts.sql`.
- `.github/pull_request_template.md`.

**Go: modified**
- `internal/yamlx/yamlx.go`: `Insert` (text-preserving list append and key set) (Task 14).
- `internal/gitsync/content_repo.go`: `PushEdit` takes ops; rename-aware diff; mode rules; `CheckEditFiles` removed (Task 7).
- `internal/edits/edits.go`, `http.go`: `Authorize`, `Workspace`, `Check`, `ListFiles`, `At`, `Version`; `NewEdit.Ops` plus the `Files` back-compat path; `Edit.Ops`.
- `internal/httpx/httpx.go`: `Read` reports an over-1-MiB body (Task 8).
- `internal/httpapi/server.go`: `Deps.Docs`, `Deps.Authoring`. `security_test.go`: CSP stays strict.
- `cmd/crucible-api/main.go`: wiring. `Makefile`, `Dockerfile`, `.dockerignore`: version ldflags; `docs/user` in the image build.
- `CLAUDE.md`: the docs rule.

**Web: new**
- `web/src/lib/docs.ts` (+ test), `web/src/pages/Docs.tsx`, `web/src/components/HelpLink.tsx`.
- `web/src/pages/NewDraft.tsx`: creates a draft and redirects (main bundle, tiny).
- `web/src/pages/editor/`: `model.ts`, `diff.ts`, `autosave.ts`, `preview.ts`, `forms.ts` (+ tests). Also `monaco.ts`, `yaml.worker.ts`, `CodeEditor.tsx`, `Ide.tsx`, `Explorer.tsx`, `ProblemsPanel.tsx`, `ChangesPanel.tsx`, `Preview.tsx`, `GoToFile.tsx`, `RebasePanel.tsx`, `BlocksPanel.tsx` (+ component tests for the ones that don't import Monaco).
- `web/scripts/check-chunks.mjs`: the main-chunk build test.

**Web: modified**
- `web/package.json`, `web/src/App.tsx`, `web/src/components/Nav.tsx`, `web/src/types.ts`, `web/src/lib/editLimits.ts`, `web/src/lib/editDraft.ts`, `web/src/pages/Edits.tsx`, `web/src/pages/EditReview.tsx`, `web/src/theme/app.css`, `web/src/components/DiffView.test.tsx`.
- `web/src/pages/EditFiles.tsx` is deleted in Task 10.

**E2E**
- `e2e/tests/helpers.ts` (login, CSP watcher, Monaco text helper), `e2e/tests/authoring.spec.ts`, `e2e/tests/docs.spec.ts`. `e2e/tests/forge-people.spec.ts` is updated for the new editor in Task 10.
- `examples/forge-103/**` (the authoring fixture), `examples/platform/trainings.yaml`, `examples/platform/teams/forge/programs/forge-103.yaml`, `scripts/seed-git.sh`, `scripts/local-check.sh`.

---
## Delivery step 1: registry, schema, validate

### Task 1: Edit operations: `gitsync.Op`, `CheckOps`, `ApplyOps`

**Files:**
- Create: `internal/gitsync/ops.go`, `internal/gitsync/ops_test.go`
- Modify: `internal/gitsync/content_repo.go` (re-implement `CheckEditFiles` on top of `CheckOps`; Task 7 removes it)

**Interfaces:**
- Consumes: `editPath(rel string) error`, `NoSymlinks(dir, rel string) error`, `editExts`, `maxEditFiles`, `maxEditFile` (all existing, package `gitsync`).
- Produces:
  - `type Op struct { Op, Path, From, To, Content string }` with JSON tags `op`, `path,omitempty`, `from,omitempty`, `to,omitempty`, `content`.
  - `func CheckPath(rel string) error` (`apperr.Invalid`).
  - `func CheckOps(ops []Op) error` (`apperr.Invalid`).
  - `func PutOps(files map[string]string) []Op`, sorted by path.
  - `func Targets(ops []Op) []string`: put paths and rename targets.
  - `func ApplyOps(dir string, ops []Op) ([]string, error)`: returns the paths that are new at `dir` (created by a put or a rename target), in apply order.

- [ ] **Step 1: Write the failing tests**

`internal/gitsync/ops_test.go`:

```go
package gitsync

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func TestCheckOps(t *testing.T) {
	ok := [][]Op{
		{{Op: "put", Path: "modules/m1/reading/a.md", Content: "# A\n"}},
		{{Op: "put", Path: "training.yaml", Content: "id: t1\n"}},
		{{Op: "rename", From: "modules/m1/reading/a.md", To: "modules/m2/reading/a.md"}},
		{{Op: "delete", Path: "modules/m1/lab/hints/h2.md"}},
		{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "rename", From: "modules/m1/b.md", To: "modules/m1/a.md"}}, // swap
		{{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "put", Path: "modules/m1/b.md", Content: "x"}},
	}
	for i, ops := range ok {
		if err := CheckOps(ops); err != nil {
			t.Errorf("ok %d: %v", i, err)
		}
	}
	many := []Op{}
	for i := range 21 {
		many = append(many, Op{Op: "put", Path: "modules/m1/" + string(rune('a'+i)) + ".md"})
	}
	bad := map[string][]Op{
		"none":              {},
		"too many":          many,
		"unknown op":        {{Op: "chmod", Path: "modules/m1/a.sh"}},
		"rename training":   {{Op: "rename", From: "training.yaml", To: "modules/m1/t.yaml"}},
		"rename to train":   {{Op: "rename", From: "modules/m1/t.yaml", To: "training.yaml"}},
		"delete training":   {{Op: "delete", Path: "training.yaml"}},
		"rename to itself":  {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/a.md"}},
		"put with from":     {{Op: "put", Path: "modules/m1/a.md", From: "modules/m1/b.md"}},
		"rename w/ content": {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md", Content: "x"}},
		"delete w/ content": {{Op: "delete", Path: "modules/m1/a.md", Content: "x"}},
		"from twice":        {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "rename", From: "modules/m1/a.md", To: "modules/m1/c.md"}},
		"to twice":          {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/c.md"}, {Op: "rename", From: "modules/m1/b.md", To: "modules/m1/c.md"}},
		"put twice":         {{Op: "put", Path: "modules/m1/a.md"}, {Op: "put", Path: "modules/m1/a.md"}},
		"renamed+deleted":   {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "delete", Path: "modules/m1/a.md"}},
		"deleted target":    {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"}, {Op: "delete", Path: "modules/m1/b.md"}},
		"rename tf":         {{Op: "rename", From: "modules/m1/lab/terraform/main.tf", To: "modules/m1/lab/terraform/x.tf"}},
		"delete png":        {{Op: "delete", Path: "modules/m1/reading/a.png"}},
		"dotdot from":       {{Op: "rename", From: "modules/m1/../../x.md", To: "modules/m1/a.md"}},
		"git dir target":    {{Op: "rename", From: "modules/m1/a.yaml", To: "modules/m1/.git/config.yaml"}},
		"backslash":         {{Op: "delete", Path: `modules\m1\a.md`}},
		"unicode":           {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/ä.md"}},
		"outside modules":   {{Op: "rename", From: "modules/m1/a.md", To: "a.md"}},
		"nul":               {{Op: "put", Path: "modules/m1/a.md", Content: "a\x00b"}},
		"too big":           {{Op: "put", Path: "modules/m1/a.md", Content: strings.Repeat("x", 256<<10+1)}},
		"bad utf8":          {{Op: "put", Path: "modules/m1/a.md", Content: "\xff"}},
	}
	for name, ops := range bad {
		if err := CheckOps(ops); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// FuzzCheckOps: whatever CheckOps accepts names only plain ASCII paths inside training.yaml or modules/<id>/, with a
// text extension, and never renames or deletes training.yaml. Run longer with
// go test -run=^$ -fuzz=FuzzCheckOps -fuzztime=30s ./internal/gitsync
func FuzzCheckOps(f *testing.F) {
	for _, s := range []string{"modules/m1/a.md", "training.yaml", "../x.md", "modules/m1/.git/x.md", "modules/M1/a.md",
		"modules/m1/a.tf", `modules\m1\a.md`, "modules/m1/a.md\x00", "modules/m1/ä.md", "modules//a.md", "modules/m1/a.md/",
		"modules/m1/a..md", "modules/m1/-x.sh", "modules/m1/x .md", "/etc/passwd.md", "modules/m1/​.md"} {
		f.Add("put", s, "")
		f.Add("rename", "modules/m1/a.md", s)
		f.Add("rename", s, "modules/m1/a.md")
		f.Add("delete", s, "")
	}
	f.Fuzz(func(t *testing.T, kind, a, b string) {
		op := Op{Op: kind}
		switch kind {
		case "put":
			op.Path, op.Content = a, b
		case "rename":
			op.From, op.To = a, b
		default:
			op.Path = a
		}
		if CheckOps([]Op{op}) != nil {
			return
		}
		for _, p := range []string{op.Path, op.From, op.To} {
			if p == "" {
				continue
			}
			parts := strings.Split(p, "/")
			if p != "training.yaml" && (len(parts) < 3 || parts[0] != "modules") {
				t.Fatalf("accepted %q outside modules/<id>/", p)
			}
			for _, part := range parts {
				if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
					t.Fatalf("accepted %q (empty or dot component)", p)
				}
			}
			if strings.IndexFunc(p, func(r rune) bool { return r > 126 || r < 33 || r == '\\' }) >= 0 {
				t.Fatalf("accepted %q (non-ASCII, space, control or backslash)", p)
			}
			if !slices.Contains([]string{".md", ".yaml", ".yml", ".sh"}, strings.ToLower(path.Ext(p))) {
				t.Fatalf("accepted extension of %q", p)
			}
			if kind != "put" && p == "training.yaml" {
				t.Fatalf("%s of training.yaml accepted", kind)
			}
		}
	})
}

func TestApplyOps(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{"modules/m1/a.md": "A", "modules/m1/b.md": "B", "modules/m1/c.md": "C", "modules/m1/e.md": "E"} {
		if err := writeFile(dir, rel, body); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	created, err := ApplyOps(dir, []Op{
		{Op: "put", Path: "modules/m1/b.md", Content: "B2"}, // listed first, applied last: edits b after the swap
		{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/b.md"},
		{Op: "rename", From: "modules/m1/b.md", To: "modules/m1/a.md"},
		{Op: "delete", Path: "modules/m1/c.md"},
		{Op: "put", Path: "modules/m1/new/d.md", Content: "D"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{read("modules/m1/a.md"), read("modules/m1/b.md"), read("modules/m1/c.md"), read("modules/m1/new/d.md"), read("modules/m1/e.md")}; !slices.Equal(got, []string{"B", "B2", "<missing>", "D", "E"}) {
		t.Fatalf("tree after ops: %v", got)
	}
	slices.Sort(created)
	if !slices.Equal(created, []string{"modules/m1/a.md", "modules/m1/b.md", "modules/m1/new/d.md"}) {
		t.Fatalf("created: %v", created)
	}
	if err := os.Symlink("/etc", filepath.Join(dir, "modules", "m1", "link")); err != nil {
		t.Fatal(err)
	}
	for name, ops := range map[string][]Op{
		"missing source": {{Op: "rename", From: "modules/m1/nope.md", To: "modules/m1/x.md"}},
		"target exists":  {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/e.md"}},
		"delete missing": {{Op: "delete", Path: "modules/m1/nope.md"}},
		"put via link":   {{Op: "put", Path: "modules/m1/link/passwd.md", Content: "x"}},
		"rename to link": {{Op: "rename", From: "modules/m1/a.md", To: "modules/m1/link/x.md"}},
		"delete a dir":   {{Op: "delete", Path: "modules/m1/new"}},
	} {
		if _, err := ApplyOps(dir, ops); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `cd /Users/adelin/Projects/Crucible && export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH && go test ./internal/gitsync -run 'TestCheckOps|FuzzCheckOps|TestApplyOps'`
Expected: build failure, `undefined: Op` / `CheckOps` / `ApplyOps`.

- [ ] **Step 3: Implement `internal/gitsync/ops.go`**

```go
package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"crucible/internal/apperr"
)

// Op is one change in a content edit or draft: "put" writes Path with Content (creating it if needed), "rename" moves
// From to To, "delete" removes Path. However they are listed, ApplyOps runs renames first, then deletes, then puts, so a
// put can change a file renamed in the same edit.
type Op struct {
	Op      string `json:"op"`
	Path    string `json:"path,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Content string `json:"content"` // always sent, so a put of an empty file still carries it
}

func invalidf(format string, a ...any) error { return apperr.Wrap(apperr.Invalid, fmt.Sprintf(format, a...)) }

// CheckPath allows exactly what an edit may touch: training.yaml or a file inside modules/<id>/ (editPath) with a text
// extension (.md/.yaml/.yml/.sh).
func CheckPath(rel string) error {
	if err := editPath(rel); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	if !slices.Contains(editExts, strings.ToLower(path.Ext(rel))) {
		return invalidf("%s: only .md, .yaml, .yml and .sh files can be edited here", rel)
	}
	return nil
}

// PutOps turns the pre-ops edit shape ({path: new content}) into put ops, sorted by path.
func PutOps(files map[string]string) []Op {
	ops := []Op{}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		ops = append(ops, Op{Op: "put", Path: p, Content: files[p]})
	}
	return ops
}

// Targets are the paths an edit writes: puts and rename targets (what the case-collision check looks at).
func Targets(ops []Op) []string {
	var out []string
	for _, op := range ops {
		switch op.Op {
		case "put":
			out = append(out, op.Path)
		case "rename":
			out = append(out, op.To)
		}
	}
	return out
}

// CheckOps enforces what one edit may do: 1–20 ops, every path passing CheckPath, puts of at most 256 KiB of UTF-8
// text without NUL, no path named twice in the same role, nothing both renamed and deleted, and training.yaml never
// renamed or deleted.
func CheckOps(ops []Op) error {
	if len(ops) == 0 || len(ops) > maxEditFiles {
		return invalidf("an edit changes 1 to %d files", maxEditFiles)
	}
	seen := map[string]bool{}
	once := func(role, p string) error {
		if seen[role+"\x00"+p] {
			return invalidf("%s: named twice in one edit", p)
		}
		seen[role+"\x00"+p] = true
		return nil
	}
	for _, op := range ops {
		var paths []string
		var err error
		switch op.Op {
		case "put":
			if op.From != "" || op.To != "" {
				return invalidf("a put takes a path and content")
			}
			if len(op.Content) > maxEditFile || strings.ContainsRune(op.Content, 0) || !utf8.ValidString(op.Content) {
				return invalidf("%s: must be text of at most 256 KiB", op.Path)
			}
			paths, err = []string{op.Path}, once("put", op.Path)
		case "rename":
			switch {
			case op.Path != "" || op.Content != "":
				return invalidf("a rename takes from and to")
			case op.From == "training.yaml" || op.To == "training.yaml":
				return invalidf("training.yaml can't be renamed or deleted")
			case op.From == op.To:
				return invalidf("%s: renamed to itself", op.From)
			}
			if err = once("from", op.From); err == nil {
				err = once("to", op.To)
			}
			paths = []string{op.From, op.To}
		case "delete":
			switch {
			case op.From != "" || op.To != "" || op.Content != "":
				return invalidf("a delete takes only a path")
			case op.Path == "training.yaml":
				return invalidf("training.yaml can't be renamed or deleted")
			}
			paths, err = []string{op.Path}, once("delete", op.Path)
		default:
			return invalidf("unknown op %q: use put, rename or delete", op.Op)
		}
		if err != nil {
			return err
		}
		for _, p := range paths {
			if err := CheckPath(p); err != nil {
				return err
			}
		}
	}
	for _, op := range ops {
		if op.Op == "delete" && (seen["from\x00"+op.Path] || seen["to\x00"+op.Path]) {
			return invalidf("%s: renamed and deleted in one edit", op.Path)
		}
	}
	return nil
}

// ApplyOps applies ops (already accepted by CheckOps) to the tree at dir: renames first (every source is read before any
// target is written, so swaps work), then deletes, then puts. Nothing is written through a symlink. It returns the
// paths that are new at dir (put on a missing path, or a rename target); the caller decides their file mode.
// Existing files keep their mode; rename targets start with the source's mode.
func ApplyOps(dir string, ops []Op) ([]string, error) {
	regular := func(rel string) (string, fs.FileMode, error) {
		if err := NoSymlinks(dir, rel); err != nil {
			return "", 0, invalidf("%s: %v", rel, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", 0, invalidf("%s: no such file", rel)
		case err != nil:
			return "", 0, err
		case !fi.Mode().IsRegular():
			return "", 0, invalidf("%s: not a regular file", rel)
		}
		return p, fi.Mode().Perm(), nil
	}
	write := func(rel string, body []byte, mode fs.FileMode) error {
		if err := NoSymlinks(dir, rel); err != nil {
			return invalidf("%s: %v", rel, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return invalidf("%s: %v", rel, err)
		}
		if err := os.WriteFile(p, body, mode); err != nil {
			return err
		}
		return os.Chmod(p, mode)
	}
	type move struct {
		src, to string
		body    []byte
		mode    fs.FileMode
	}
	var moves []move
	for _, op := range ops {
		if op.Op != "rename" {
			continue
		}
		p, mode, err := regular(op.From)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move{p, op.To, b, mode})
	}
	for _, m := range moves {
		if err := os.Remove(m.src); err != nil {
			return nil, err
		}
	}
	var created []string
	for _, m := range moves {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(m.to))); err == nil {
			return nil, invalidf("%s: already exists", m.to)
		}
		if err := write(m.to, m.body, m.mode); err != nil {
			return nil, err
		}
		created = append(created, m.to)
	}
	for _, op := range ops {
		if op.Op != "delete" {
			continue
		}
		p, _, err := regular(op.Path)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(p); err != nil {
			return nil, err
		}
	}
	for _, op := range ops {
		if op.Op != "put" {
			continue
		}
		if err := NoSymlinks(dir, op.Path); err != nil {
			return nil, invalidf("%s: %v", op.Path, err)
		}
		mode, isNew := fs.FileMode(0o644), true
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(op.Path))); err == nil {
			if !fi.Mode().IsRegular() {
				return nil, invalidf("%s: not a regular file", op.Path)
			}
			mode, isNew = fi.Mode().Perm(), false
		}
		if err := write(op.Path, []byte(op.Content), mode); err != nil {
			return nil, err
		}
		if isNew && !slices.Contains(created, op.Path) {
			created = append(created, op.Path)
		}
	}
	return created, nil
}
```

In `internal/gitsync/content_repo.go`, replace the body of `CheckEditFiles` so the files-map path and the ops path share one guard. Keep the old doc comment and add "(Task 7 removes this)":

```go
func CheckEditFiles(files map[string]string) error { return CheckOps(PutOps(files)) }
```

and drop the `unicode/utf8` import if it becomes unused (`cleanMsg` still uses it, so it probably stays).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitsync ./internal/edits`
Expected: PASS. The existing `TestPushEditRefusesBadFiles` and `TestEditPathRules` still pass through `CheckEditFiles`.

- [ ] **Step 5: Commit**

```bash
git add -- internal/gitsync/ops.go internal/gitsync/ops_test.go
git commit -m "feat(gitsync): edit operations (put, rename, delete) with shared guards and a two-phase applier" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/gitsync/ops.go internal/gitsync/ops_test.go internal/gitsync/content_repo.go
```

---

### Task 2: Field registry and JSON Schema per file kind

**Files:**
- Create: `internal/content/blocks/fields.go`, `internal/content/blocks/schema.go`, `internal/content/blocks/registry_test.go`

**Interfaces:**
- Consumes: the content types in `internal/content/types.go`, `yamlx.Duration`.
- Produces:
  - `type Field struct { Name, Type string; Required bool; Enum []string; Default, Description string; Min, Max *float64 }` with JSON tags `name,type,required,enum,default,description,min,max` (omitempty except name and description).
  - `var Fields map[string]Field`, keyed `"<GoType>.<yamlkey>"`.
  - `var Kinds map[string]any`, with keys `training`, `module`, `quiz`, `lab`.
  - `func Schema(kind string) map[string]any`.
  - `type YAMLField struct { Key string; Type reflect.Type }`.
  - `func YAMLFields(t reflect.Type) []YAMLField`.

- [ ] **Step 1: Write the failing tests**

`internal/content/blocks/registry_test.go`:

```go
package blocks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"crucible/internal/content"
)

var contentTypes = []any{content.Training{}, content.Module{}, content.Quiz{}, content.Question{}, content.Lab{},
	content.AWSConfig{}, content.Terminal{}, content.Script{}, content.Task{}, content.Hint{}}

// Adding a content field without describing it fails here: the editor's hover text and the Docs reference come from Fields.
func TestEveryContentFieldIsDescribed(t *testing.T) {
	want := map[string]bool{}
	for _, v := range contentTypes {
		typ := reflect.TypeOf(v)
		for _, f := range YAMLFields(typ) {
			key := typ.Name() + "." + f.Key
			want[key] = true
			if strings.TrimSpace(Fields[key].Description) == "" {
				t.Errorf("%s has no description in blocks.Fields", key)
			}
		}
	}
	for key := range Fields {
		if !want[key] {
			t.Errorf("blocks.Fields describes %s, which the content types no longer have", key)
		}
	}
}

// jsonSchema round-trips Schema through JSON: the browser gets exactly this.
func jsonSchema(t *testing.T, kind string) map[string]any {
	t.Helper()
	b, err := json.Marshal(Schema(kind))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// validate checks v against the subset of JSON Schema that Schema emits. No library: the subset is small and fixed.
func validate(s map[string]any, v any, at string) []string {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, at+": "+fmt.Sprintf(f, a...)) }
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		bad("%v is not one of %v", v, enum)
	}
	num := func(x any) (float64, bool) {
		switch n := x.(type) {
		case int:
			return float64(n), true
		case float64:
			return n, true
		}
		return 0, false
	}
	switch s["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			bad("want a mapping, got %T", v)
			return errs
		}
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if _, ok := m[r.(string)]; !ok {
				bad("%s is required", r)
			}
		}
		if n, ok := s["minProperties"].(float64); ok && float64(len(m)) < n {
			bad("needs at least %v keys", n)
		}
		if n, ok := s["maxProperties"].(float64); ok && float64(len(m)) > n {
			bad("allows at most %v keys", n)
		}
		for k, x := range m {
			if p, ok := props[k].(map[string]any); ok {
				errs = append(errs, validate(p, x, at+"."+k)...)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					bad("unknown key %s", k)
				}
			case map[string]any:
				errs = append(errs, validate(ap, x, at+"."+k)...)
			}
		}
	case "array":
		l, ok := v.([]any)
		if !ok {
			bad("want a list, got %T", v)
			return errs
		}
		for i, x := range l {
			errs = append(errs, validate(s["items"].(map[string]any), x, fmt.Sprintf("%s[%d]", at, i))...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			bad("want a string, got %T", v)
			return errs
		}
		if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(str) {
			bad("%q doesn't match %s", str, p)
		}
	case "number", "integer":
		n, ok := num(v)
		if _, isInt := v.(int); !ok || (s["type"] == "integer" && !isInt) {
			bad("want a %s, got %v", s["type"], v)
			return errs
		}
		if m, ok := s["minimum"].(float64); ok && n < m {
			bad("%v is below %v", n, m)
		}
		if m, ok := s["maximum"].(float64); ok && n > m {
			bad("%v is above %v", n, m)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			bad("want true or false, got %v", v)
		}
	}
	return errs
}

func kindOf(p string) string {
	switch filepath.Base(p) {
	case "training.yaml":
		return "training"
	case "module.yaml":
		return "module"
	case "quiz.yaml":
		return "quiz"
	case "lab.yaml":
		return "lab"
	}
	return ""
}

func TestSchemaAcceptsEveryExample(t *testing.T) {
	var files []string
	for _, g := range []string{"training.yaml", "modules/*/module.yaml", "modules/*/quiz.yaml", "modules/*/*/lab.yaml"} {
		m, _ := filepath.Glob(filepath.Join("../../../examples/*", g))
		files = append(files, m...)
	}
	if len(files) < 15 {
		t.Fatalf("found only %d example files", len(files))
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(p, err)
		}
		for _, e := range validate(jsonSchema(t, kindOf(p)), doc, p) {
			t.Error(e)
		}
	}
}

func TestSchemaRejects(t *testing.T) {
	for kind, src := range map[string]string{
		"training": "id: t\ntitle: T\nmodules: [m]\nmaintainer: [x]\n", // typo'd key
		"module":   "title: M\nitems:\n  - reading: a.md\n    quiz: quiz.yaml\n",
		"quiz":     "questions:\n  - {id: q, type: essay, prompt: P}\n",
		"lab":      "id: l\nruntime: local\nttl: soon\nterminals: [{name: s, service: s}]\ntasks: [{id: t, instructions: t.md}]\n",
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatal(err)
		}
		if errs := validate(jsonSchema(t, kind), doc, kind); len(errs) == 0 {
			t.Errorf("%s: schema accepted %q", kind, src)
		}
	}
}

func TestSchemaHasHoverText(t *testing.T) {
	q := jsonSchema(t, "quiz")["properties"].(map[string]any)["questions"].(map[string]any)["items"].(map[string]any)
	typ := q["properties"].(map[string]any)["type"].(map[string]any)
	if typ["description"] == "" || len(typ["enum"].([]any)) != 10 {
		t.Fatalf("question type: %v", typ)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/content/blocks`
Expected: build failure, `undefined: Fields` / `Schema` / `YAMLFields`.

- [ ] **Step 3: Write `fields.go`**

```go
// Package blocks describes the building blocks of a training repo. Fields documents every YAML key the content
// loader reads (the source of the editor's JSON Schema and hover text and of the Docs reference); catalog.go lists the
// blocks authors insert through forms. content.Load and lint stay the authority on validity: tests keep both in step.
package blocks

// Field describes one YAML key (in Fields) or one form input (in a catalog Block).
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"` // form inputs: id | string | text | number | integer | bool | enum | list | ints | pairs | duration | module | task
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

func num(v float64) *float64 { return &v }

var questionTypes = []string{"single", "multi", "exact", "regex", "order", "match", "terminal", "text", "upload", "signoff"}

// Fields describes every YAML key of the content types, keyed "<Go type>.<key>". TestEveryContentFieldIsDescribed
// fails when a content field has no entry here.
var Fields = map[string]Field{
	"Training.id":              {Required: true, Description: "The training's id: its key in the platform's trainings.yaml. Edits can't change it; progress is stored under it."},
	"Training.title":           {Required: true, Description: "Shown on the Hearth, in the catalog and on every page of the training."},
	"Training.description":     {Description: "One or two sentences for the catalog."},
	"Training.maintainers":     {Description: "Emails of the people who review content edits to this training. Change it in git only."},
	"Training.progression":     {Enum: []string{"linear", "free"}, Default: "linear", Description: "linear: modules unlock in order. free: any module can be opened at any time."},
	"Training.estimated_hours": {Min: num(0), Description: "Rough hours to finish, shown in the catalog."},
	"Training.modules":         {Required: true, Description: "Module folder names under modules/, in the order trainees take them."},

	"Module.title":      {Required: true, Description: "Shown in the training outline."},
	"Module.completion": {Enum: []string{"all_items", "score"}, Default: "all_items", Description: "all_items: every item must be done. score: the module completes when the trainee's share of points reaches threshold."},
	"Module.threshold":  {Min: num(0), Max: num(1), Description: "Only with completion: score. The share of points needed, above 0 and at most 1 (0.7 = 70%)."},
	"Module.items":      {Required: true, Description: "The module's readings, quiz and lab, in order. Each entry is one of reading: <file>, quiz: quiz.yaml or lab: <folder>."},

	"Quiz.pass_threshold": {Min: num(0), Max: num(1), Default: "0.8", Description: "Share of points needed to pass (default 0.8)."},
	"Quiz.max_attempts":   {Min: num(0), Description: "Attempts allowed; 0 or unset means unlimited."},
	"Quiz.cooldown":       {Description: "Wait between attempts, e.g. 10m. Unset means none."},
	"Quiz.questions":      {Description: "The questions. Terminal questions are answered inside the module's lab."},

	"Question.id":             {Required: true, Description: "Unique within the quiz. Lab tasks name terminal questions by it."},
	"Question.type":           {Required: true, Enum: questionTypes, Description: "single, multi, exact, regex, order and match are scored at once; terminal is checked by a script in the lab; text, upload and signoff are scored by a person on the Anvil."},
	"Question.prompt":         {Required: true, Description: "The question, in Markdown."},
	"Question.options":        {Description: "single and multi: the choices. order: the items in their correct order (trainees see them shuffled)."},
	"Question.pairs":          {Description: "match: [left, right] pairs in their correct matching."},
	"Question.answer":         {Description: "single: the index of the right option (0 is the first). multi: a list of indexes. exact: the expected text. regex: a pattern the whole answer must match."},
	"Question.case_sensitive": {Description: "exact and regex: compare letter case too (default: ignore it)."},
	"Question.points":         {Min: num(0), Default: "1", Description: "Points the question is worth (default 1)."},
	"Question.check":          {Description: "terminal: the check script, relative to the module's lab folder."},
	"Question.run_in":         {Description: "terminal: the service the check runs in; defaults to the first terminal's service."},
	"Question.rubric":         {Description: "text and upload: what the scorer looks for. Never shown to trainees."},

	"Lab.id":           {Required: true, Description: "The lab's id."},
	"Lab.runtime":      {Required: true, Enum: []string{"local", "cluster", "aws"}, Description: "local: Docker on the trainee's laptop. cluster: Crucible's Kubernetes cluster. aws: a shared AWS account, after cost approval."},
	"Lab.ttl":          {Description: "How long the lab may run, e.g. 1h; unset uses the program's default."},
	"Lab.idle_timeout": {Description: "Stop the lab after this long without terminal activity."},
	"Lab.idle_warning": {Default: "5m", Description: "Warn the trainee this long before the idle stop (default 5m); shorter than idle_timeout."},
	"Lab.task_order":   {Enum: []string{"linear", "free"}, Default: "linear", Description: "linear: tasks unlock in order. free: any order."},
	"Lab.hint_cost":    {Min: num(0), Description: "Points a hint costs unless the hint sets its own cost."},
	"Lab.compose":      {Default: "compose.yaml", Description: "local and cluster: the Docker Compose file, relative to the lab folder."},
	"Lab.terminals":    {Required: true, Description: "The terminals the trainee gets, each attached to a service."},
	"Lab.setup":        {Description: "A script run once when the lab starts."},
	"Lab.tasks":        {Required: true, Description: "The lab's tasks."},
	"Lab.aws":          {Description: "runtime: aws only: the region and the hourly cost ceiling."},

	"AWSConfig.region":         {Required: true, Description: "AWS region for the lab's resources, e.g. eu-west-1."},
	"AWSConfig.max_hourly_usd": {Required: true, Min: num(0), Description: "The most the lab's resources may cost per hour, in US dollars; above 0."},

	"Terminal.name":    {Required: true, Description: "The terminal tab's label; unique within the lab."},
	"Terminal.service": {Required: true, Description: "The compose service (workspace for aws labs) the terminal opens in."},

	"Script.script":  {Required: true, Description: "The script's path, relative to the lab folder. It must be executable."},
	"Script.run_in":  {Required: true, Description: "The service the script runs in."},
	"Script.timeout": {Description: "Longest run time (default 30s for checks, 60s for setup)."},

	"Task.id":           {Required: true, Description: "Unique within the lab."},
	"Task.instructions": {Required: true, Description: "Markdown file with the task's instructions, relative to the lab folder."},
	"Task.check":        {Description: "A script that exits 0 when the task is done."},
	"Task.setup":        {Description: "A script run when the task starts, e.g. to break something on purpose."},
	"Task.quiz":         {Description: "The id of a terminal question in the module's quiz.yaml that scores this task."},
	"Task.points":       {Min: num(0), Default: "1", Description: "Points the task is worth (default 1, or the question's points with quiz)."},
	"Task.human_review": {Description: "A person scores the task on the Anvil instead of a check."},
	"Task.rubric":       {Description: "human_review only: what the scorer looks for. Never shown to trainees."},
	"Task.hints":        {Description: "Hints the trainee can reveal, each costing points."},

	"Hint.text": {Description: "The hint, in Markdown. Use either text or file."},
	"Hint.file": {Description: "A Markdown file with the hint, relative to the lab folder."},
	"Hint.cost": {Min: num(0), Description: "Points this hint costs; defaults to the lab's hint_cost. At most the task's points."},
}

func init() {
	for k, f := range Fields {
		_, f.Name, _ = cutLast(k)
		Fields[k] = f
	}
}

func cutLast(s string) (string, string, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}
```

Before committing, check each description against `internal/content/load.go` and the code that grades and scores (`internal/learn/quiz.go`, `internal/labs`). Fix any claim that is not true, especially the case-sensitivity default and the hint-cost rules.

- [ ] **Step 4: Write `schema.go`**

```go
package blocks

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"crucible/internal/content"
	"crucible/internal/yamlx"
)

// Kinds are the YAML files the editor validates with a schema, by kind.
var Kinds = map[string]any{"training": content.Training{}, "module": content.Module{}, "quiz": content.Quiz{}, "lab": content.Lab{}}

const durationPattern = `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`

// itemSchema is one module item: exactly one of reading, quiz, lab.
var itemSchema = map[string]any{"type": "object", "minProperties": 1, "maxProperties": 1, "additionalProperties": false,
	"properties": map[string]any{
		"reading": map[string]any{"type": "string", "description": "A Markdown file in the module, e.g. reading/intro.md."},
		"quiz":    map[string]any{"type": "string", "enum": []string{"quiz.yaml"}, "description": "The module's quiz.yaml."},
		"lab":     map[string]any{"type": "string", "description": "The lab's folder in the module, e.g. lab."},
	}}

// Schema is the JSON Schema (draft-07 subset) of one file kind, generated from the content types and Fields.
func Schema(kind string) map[string]any {
	s := schemaOf(reflect.TypeOf(Kinds[kind]))
	s["$schema"] = "http://json-schema.org/draft-07/schema#"
	return s
}

func schemaOf(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case reflect.TypeOf(yamlx.Duration(0)):
		return map[string]any{"type": "string", "pattern": durationPattern}
	case reflect.TypeOf(yaml.Node{}):
		return map[string]any{} // any value; the loader checks it per question type
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Struct:
		props, required := map[string]any{}, []string{}
		for _, f := range YAMLFields(t) {
			key := t.Name() + "." + f.Key
			p := schemaOf(f.Type)
			if key == "Module.items" {
				p = map[string]any{"type": "array", "items": itemSchema}
			}
			d := Fields[key]
			p["description"] = d.Description
			if len(d.Enum) > 0 {
				p["enum"] = d.Enum
			}
			if d.Min != nil {
				p["minimum"] = *d.Min
			}
			if d.Max != nil {
				p["maximum"] = *d.Max
			}
			if d.Required {
				required = append(required, f.Key)
			}
			props[f.Key] = p
		}
		s := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	panic("blocks: no schema for " + t.String())
}

// YAMLField is one key YAML reads into a struct.
type YAMLField struct {
	Key  string
	Type reflect.Type
}

// YAMLFields lists t's exported fields that YAML reads, by key, in declaration order.
func YAMLFields(t reflect.Type) []YAMLField {
	var out []YAMLField
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if f.IsExported() && name != "" && name != "-" {
			out = append(out, YAMLField{name, f.Type})
		}
	}
	return out
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/content/blocks`
Expected: PASS. If an example fails the schema, the example or the schema is wrong. Fix whichever disagrees with `content.Load`: run `./bin/crucible lint examples/<repo>` to see which one is the authority's view.

- [ ] **Step 6: Commit**

```bash
git add -- internal/content/blocks
git commit -m "feat(blocks): field registry and generated JSON Schema per content file kind" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content/blocks
```

---

### Task 3: `internal/authoring`: schema and validate endpoints

**Files:**
- Modify: `internal/edits/edits.go` (export `Authorize`, `Workspace`, `Check`; `Create` uses `Check`)
- Create: `internal/authoring/authoring.go`, `internal/authoring/http.go`, `internal/authoring/authoring_test.go`, `internal/authoring/http_test.go`
- Modify: `internal/httpapi/server.go` (`Deps.Authoring`), `cmd/crucible-api/main.go` (wiring)

**Interfaces:**
- Consumes: `gitsync.Op`, `gitsync.CheckOps`, `gitsync.ApplyOps`, `gitsync.PutOps` (Task 1); `blocks.Schema`, `blocks.Kinds` (Task 2).
- Produces:
  - `func (s *edits.Service) Authorize(u *auth.User, training string) (*content.Training, string, error)` returns the head version and its sha. Forbidden for anyone who may not propose (enrolled users included), NotFound for an unknown training.
  - `func edits.Workspace(t *content.Training, ops []gitsync.Op) (dir string, changed []gitsync.Op, cleanup func(), err error)`
  - `func edits.Check(t *content.Training, ops []gitsync.Op) ([]gitsync.Op, []content.Problem, error)`
  - `type authoring.Service struct { DB *pgxpool.Pool; Edits *edits.Service; Log *slog.Logger }`
  - `type authoring.Problem struct { File string; Line int; Msg string }` with JSON `file,line,msg`
  - `type authoring.ValidateReq struct { Training, BaseSHA string; Ops []gitsync.Op }` with JSON `training,base_sha,ops`
  - `func (s *Service) Validate(ctx, u, in ValidateReq) ([]Problem, error)`
  - `func (s *Service) base(ctx, u, training, sha string) (*content.Training, error)`. Task 8 extends it.
  - `func (s *Service) bounded(ctx context.Context, email string, fn func()) error`
  - Routes: `GET /api/authoring/schema?training=` returns `{"training": {...}, "module": {...}, "quiz": {...}, "lab": {...}}`. `POST /api/authoring/validate` returns `{"problems": [...]}`.

- [ ] **Step 1: Refactor `edits.validate` into `Workspace` + `Check` (tests stay green)**

In `internal/edits/edits.go`, delete `func validate(...)` and add:

```go
// Authorize returns the training's head version and sha when u may propose edits to it: never someone enrolled in it.
func (s *Service) Authorize(u *auth.User, training string) (*content.Training, string, error) {
	_, t, sha, err := s.training(u, training)
	return t, sha, err
}

// Workspace copies t into a temp dir and applies the ops that change something (a put equal to the current file is
// dropped). Ops must have passed gitsync.CheckOps. The caller loads the copy, then calls cleanup. New .sh files are
// made executable there because lint wants lab scripts executable; ContentRepo.PushEdit sets the modes git records.
func Workspace(t *content.Training, ops []gitsync.Op) (string, []gitsync.Op, func(), error) {
	var changed []gitsync.Op
	for _, op := range ops {
		if op.Op == "put" {
			if old, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(op.Path))); err == nil && string(old) == op.Content {
				continue
			}
		}
		changed = append(changed, op)
	}
	tmp, err := os.MkdirTemp("", "crucible-edit-*")
	if err != nil {
		return "", nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := os.CopyFS(tmp, os.DirFS(t.Dir)); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("copying the training to check the edit: %w", err)
	}
	created, err := gitsync.ApplyOps(tmp, changed)
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	for _, rel := range created {
		if strings.HasSuffix(rel, ".sh") {
			_ = os.Chmod(filepath.Join(tmp, filepath.FromSlash(rel)), 0o755)
		}
	}
	return tmp, changed, cleanup, nil
}

// Check applies ops to a copy of t and loads it: the ops that change something, and the problems the training would
// then have. err is for ops that are refused or can't apply, and for an edit that changes nothing.
func Check(t *content.Training, ops []gitsync.Op) ([]gitsync.Op, []content.Problem, error) {
	if err := gitsync.CheckOps(ops); err != nil {
		return nil, nil, err
	}
	dir, changed, cleanup, err := Workspace(t, ops)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	if len(changed) == 0 {
		return nil, nil, apperr.Wrap(apperr.Invalid, "nothing changed")
	}
	nt, probs := content.Load(dir)
	if len(probs) == 0 && nt.ID != t.ID {
		probs = []content.Problem{{File: "training.yaml", Msg: "the training id must stay " + t.ID}}
	}
	return changed, probs, nil
}

func broken(probs []content.Problem) error {
	msgs := []string{}
	for _, p := range probs[:min(len(probs), 10)] {
		msgs = append(msgs, p.String())
	}
	return apperr.Wrap(apperr.Invalid, "this edit would break the training: "+strings.Join(msgs, "; "))
}
```

In `Create`, replace the `changed, err := validate(t, in.Files)` block with the following. Task 7 makes ops the stored shape and drops the map.

```go
	changedOps, probs, err := Check(t, gitsync.PutOps(in.Files))
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		return nil, broken(probs)
	}
	changed := map[string]string{} // ponytail: map until Task 7 stores ops
	for _, op := range changedOps {
		changed[op.Path] = op.Content
	}
```

Run: `go test -race ./internal/edits`
Expected: PASS, unchanged behaviour. `TestCreateValidates` still sees "would break" and "nothing changed".

- [ ] **Step 2: Write the failing authoring tests**

`internal/authoring/authoring_test.go`. The fixture mirrors `edits_test.go`: training t1 in a bare repo, senior@ maintainer, trainee@ enrolled through team forge. The DB is created now because Task 8 needs it.

```go
package authoring

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

func TestMain(m *testing.M) {
	gitsync.AllowFileTransport = true
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

var seed = map[string]string{
	"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local]\nprogression: free\nmodules: [m1]\n",
	"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
	"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
	"modules/m1/quiz.yaml":        "questions:\n  - id: q1\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\", \"yes\"]\n    answer: 1\n",
}

type fx struct {
	s                       *Service
	edits                   *edits.Service
	st                      *gitsync.State
	remote, work            string
	leader, senior, trainee *auth.User
}

func setup(t *testing.T) *fx {
	t.Helper()
	work := t.TempDir()
	for rel, body := range seed {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "init", "-q", "-b", "main")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "seed")
	remote := filepath.Join(t.TempDir(), "t1.git")
	git(t, "", "clone", "-q", "--bare", work, remote)
	head := git(t, remote, "rev-parse", "main")
	tr, probs := content.Load(work)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	plat.Trainings["t1"] = config.TrainingRef{Repo: remote, Branch: "main"}
	plat.Teams["forge"].Programs["t1"] = &config.Program{Training: "t1", Enrolled: []string{"trainee@crucible.local"}}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"t1@" + head: tr}, Heads: map[string]string{"t1": head}}
	repo := &gitsync.ContentRepo{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "edits"), Name: "Crucible", Email: "bot@x"}
	db := dbtest.New(t)
	es := &edits.Service{DB: db, State: func() *gitsync.State { return st },
		Repo: func(id string) *gitsync.ContentRepo {
			if id == "t1" {
				return repo
			}
			return nil
		}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: &Service{DB: db, Edits: es}, edits: es, st: st, remote: remote, work: work,
		leader: u("leader@crucible.local"), senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) head() string { return f.st.Heads["t1"] }

func TestValidateReportsProblemsWithLines(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head()})
	if err != nil || len(probs) != 0 {
		t.Fatalf("the head is clean: %v %v", probs, err)
	}
	probs, err = f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: []gitsync.Op{
		{Op: "put", Path: "modules/m1/quiz.yaml", Content: "questions:\n  - id: q1\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\"]\n    answer: 1\n"},
		{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"modules/m1/quiz.yaml": 2, "modules/m1/module.yaml": 3} // "- id: q1" line; the item naming the moved file
	for _, p := range probs {
		if line, ok := want[p.File]; ok && p.Line == line {
			delete(want, p.File)
		}
	}
	if len(want) > 0 {
		t.Fatalf("problems %+v; missing %v", probs, want)
	}
	probs, _ = f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: []gitsync.Op{
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: [\n"},
	}})
	if len(probs) == 0 || probs[0].File != "modules/m1/module.yaml" || probs[0].Line < 3 {
		t.Fatalf("a YAML syntax error carries its line: %+v", probs)
	}
}

func TestAuthoringRefusesEnrolledAndBadInput(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.Validate(ctx, f.trainee, ValidateReq{Training: "t1", BaseSHA: f.head()}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("an enrolled trainee may not validate (the problems quote answer keys): %v", err)
	}
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "nope", BaseSHA: f.head()}); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training: %v", err)
	}
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: strings.Repeat("a", 40)}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a base that is neither the head nor one of my drafts' bases: %v", err)
	}
	many := []gitsync.Op{}
	for i := range 21 {
		many = append(many, gitsync.Op{Op: "put", Path: "modules/m1/reading/" + string(rune('a'+i)) + ".md", Content: "x"})
	}
	for name, ops := range map[string][]gitsync.Op{
		"caps":      many,
		"traversal": {{Op: "put", Path: "modules/m1/../../../etc/x.md", Content: "x"}},
		"exec ext":  {{Op: "put", Path: "modules/m1/x.tf", Content: "x"}},
		"missing":   {{Op: "delete", Path: "modules/m1/reading/nope.md"}},
	} {
		if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: ops}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOneCheckAtATimePerUser(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	release := make(chan struct{})
	started := make(chan struct{})
	first := make(chan error)
	go func() { first <- f.s.bounded(ctx, "leader@crucible.local", func() { close(started); <-release }) }()
	<-started
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head()}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a second check while one runs: %v", err)
	}
	if _, err := f.s.Validate(ctx, f.senior, ValidateReq{Training: "t1", BaseSHA: f.head()}); err != nil {
		t.Fatalf("other users are not blocked: %v", err)
	}
	close(release)
	if err := <-first; err != nil { // returned before checkTimeout changes below (the race detector watches that var)
		t.Fatal(err)
	}
	old := checkTimeout
	checkTimeout = 20 * time.Millisecond
	defer func() { checkTimeout = old }()
	hold := make(chan struct{})
	defer close(hold)
	if err := f.s.bounded(ctx, "senior@crucible.local", func() { <-hold }); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("timeout: %v", err)
	}
	if err := f.s.bounded(ctx, "senior@crucible.local", func() {}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("the slot is held until the slow check really ends: %v", err)
	}
}
```

`internal/authoring/http_test.go`:

```go
package authoring

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
)

func router(f *fx) chi.Router {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { // X-User stands in for the session middleware
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), &auth.User{Email: req.Header.Get("X-User")})))
		})
	})
	f.s.Routes(r)
	return r
}

func call(t *testing.T, r chi.Router, method, path, user, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAuthoringRoutes(t *testing.T) {
	f := setup(t)
	r := router(f)
	const leader, trainee = "leader@crucible.local", "trainee@crucible.local"
	for _, c := range []struct {
		method, path, user, body string
		want                     int
	}{
		{"GET", "/api/authoring/schema?training=t1", leader, "", 200},
		{"GET", "/api/authoring/schema?training=t1", trainee, "", 403},
		{"GET", "/api/authoring/schema?training=nope", leader, "", 404},
		{"POST", "/api/authoring/validate", leader, `{"training":"t1","base_sha":"` + f.head() + `","ops":[]}`, 200},
		{"POST", "/api/authoring/validate", trainee, `{"training":"t1","base_sha":"` + f.head() + `","ops":[]}`, 403},
		{"POST", "/api/authoring/validate", leader, `{"training":"t1","base_sha":"` + f.head() + `","ops":[{"op":"chmod","path":"x"}]}`, 400},
		{"POST", "/api/authoring/validate", leader, `{"bogus":1}`, 400},
	} {
		if got, body := call(t, r, c.method, c.path, c.user, c.body); got != c.want {
			t.Errorf("%s %s as %s: %d %v, want %d", c.method, c.path, c.user, got, body, c.want)
		}
	}
	_, body := call(t, r, "GET", "/api/authoring/schema?training=t1", leader, "")
	if _, ok := body["quiz"].(map[string]any)["properties"]; !ok {
		t.Fatalf("schema: %v", body)
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

Run: `go test ./internal/authoring`
Expected: build failure (package has no non-test files).

- [ ] **Step 4: Implement `internal/authoring/authoring.go`**

```go
// Package authoring serves the content editor (spec docs-and-editor §3, §5, §6, §8): schema, validation, drafts and
// block inserts. Everything is gated by edits.Service.Authorize: only people allowed to propose edits to a training,
// never anyone enrolled in it.
package authoring

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/content/blocks"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

type Service struct {
	DB    *pgxpool.Pool
	Edits *edits.Service
	Log   *slog.Logger

	busy sync.Map // lowercased email → struct{}: one validate or insert at a time per user
}

// checkTimeout bounds one validate or insert. A var so tests can shorten it.
var checkTimeout = 20 * time.Second

type Problem struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Msg  string `json:"msg"`
}

type ValidateReq struct {
	Training string       `json:"training"`
	BaseSHA  string       `json:"base_sha"`
	Ops      []gitsync.Op `json:"ops"`
}

// base is the training at sha for u. A base from the client is never trusted on its own (the bot's mirror also holds
// unmerged edit branches): it must be the head. Task 8 also allows the base of one of u's own drafts.
func (s *Service) base(_ context.Context, u *auth.User, training, sha string) (*content.Training, error) {
	t, head, err := s.Edits.Authorize(u, training)
	if err != nil {
		return nil, err
	}
	if sha != head {
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it")
	}
	return t, nil
}

// bounded runs fn with checkTimeout, one at a time per user. The user's slot is released only when fn really ends,
// so retrying a slow lint can't stack copies of it.
func (s *Service) bounded(ctx context.Context, email string, fn func()) error {
	key := strings.ToLower(email)
	if _, busy := s.busy.LoadOrStore(key, struct{}{}); busy {
		return apperr.Wrap(apperr.Conflict, "a check is already running; try again in a moment")
	}
	done := make(chan struct{})
	go func() {
		defer s.busy.Delete(key)
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return nil
	case <-time.After(checkTimeout):
		return apperr.Wrap(apperr.Unavailable, "the check took too long; try again")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Schemas returns the JSON Schema of every file kind, for someone allowed to edit training.
func (s *Service) Schemas(u *auth.User, training string) (map[string]any, error) {
	if _, _, err := s.Edits.Authorize(u, training); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for kind := range blocks.Kinds {
		out[kind] = blocks.Schema(kind)
	}
	return out, nil
}

// Validate loads the draft's base with in.Ops applied and returns its problems, each with a file and a line.
func (s *Service) Validate(ctx context.Context, u *auth.User, in ValidateReq) ([]Problem, error) {
	t, err := s.base(ctx, u, in.Training, in.BaseSHA)
	if err != nil {
		return nil, err
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	out := []Problem{}
	var ferr error
	if err := s.bounded(ctx, u.Email, func() {
		dir, _, cleanup, err := edits.Workspace(t, in.Ops)
		if err != nil {
			ferr = err
			return
		}
		defer cleanup()
		_, probs := content.Load(dir)
		out = locate(dir, probs)
	}); err != nil {
		return nil, err
	}
	return out, ferr
}

var (
	lineRE = regexp.MustCompile(`line (\d+)`)
	idRE   = regexp.MustCompile(`(?:question \d+ \(|task )([A-Za-z0-9_.-]+)`)
)

// locate gives each problem a file the author can open and a line in it.
// ponytail: heuristics over the loader's messages, which carry no positions: YAML's "line N"; "id: x" for a named
// question or task; a missing file is pinned on the YAML line that names it. Line 1 otherwise. Give content.Problem
// real positions if authors find these wrong.
func locate(dir string, probs []content.Problem) []Problem {
	out := []Problem{}
	for _, p := range probs {
		q := Problem{File: filepath.ToSlash(p.File), Line: 1, Msg: p.Msg}
		if m := lineRE.FindStringSubmatch(p.Msg); m != nil {
			q.Line, _ = strconv.Atoi(m[1])
		} else if m := idRE.FindStringSubmatch(p.Msg); m != nil {
			if n := lineOf(filepath.Join(dir, filepath.FromSlash(q.File)), "id: "+m[1]); n > 0 {
				q.Line = n
			}
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(q.File))); err != nil {
			if f, n := mention(dir, q.File); f != "" {
				q.Msg = q.File + ": " + p.Msg
				q.File, q.Line = f, n
			}
		}
		out = append(out, q)
	}
	return out
}

// lineOf is the 1-based line of the first line containing s, or 0.
func lineOf(file, s string) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	return 0
}

// mention finds the YAML file under modules/ (or training.yaml) that names missing, by its path relative to that
// file's folder, and the line.
func mention(dir, missing string) (string, int) {
	found, line := "", 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		target := strings.TrimPrefix(missing, path.Dir(rel)+"/")
		if target == missing && path.Dir(rel) != "." {
			return nil // the missing file is not under this YAML's folder
		}
		if n := lineOf(p, target); n > 0 {
			found, line = rel, n
		}
		return nil
	})
	return found, line
}
```

`internal/authoring/http.go`:

```go
package authoring

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	reply := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, v)
	}
	r.Get("/api/authoring/schema", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Schemas(user(r), r.URL.Query().Get("training"))
		reply(w, v, err)
	})
	r.Post("/api/authoring/validate", func(w http.ResponseWriter, r *http.Request) {
		var in ValidateReq
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		probs, err := s.Validate(r.Context(), user(r), in)
		reply(w, map[string]any{"problems": probs}, err)
	})
}
```

- [ ] **Step 5: Wire it**

In `internal/httpapi/server.go`, add `Authoring *authoring.Service` to `Deps` (import `crucible/internal/authoring`). Inside the authenticated group, after `d.Edits.Routes(r)`:

```go
		if d.Authoring != nil {
			d.Authoring.Routes(r)
		}
```

In `cmd/crucible-api/main.go`, after `editsSvc` is built:

```go
	authoringSvc := &authoring.Service{DB: pool, Edits: editsSvc, Log: slog.Default()}
```

and pass `Authoring: authoringSvc` in `httpapi.Deps`.

- [ ] **Step 6: Run everything Go**

Run: `gofmt -l cmd internal && go vet ./... && go test -race ./internal/authoring ./internal/edits ./internal/httpapi ./cmd/...`
Expected: no gofmt output; PASS.

If `TestValidateReportsProblemsWithLines` misses a line, print the problems (`t.Logf("%+v", probs)`) and fix the heuristic, not the test's expected lines. The lines in the test are what an author would want to jump to.

- [ ] **Step 7: Commit**

```bash
git add -- internal/authoring
git commit -m "feat(authoring): schema and validate endpoints with line-located problems, one check per user and a timeout" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/authoring internal/edits/edits.go internal/httpapi/server.go cmd/crucible-api/main.go
```

---
## Delivery step 2: the Docs tab

### Task 4: `internal/docs`: pages, front matter, API

**Files:**
- Create: `internal/docs/docs.go`, `internal/docs/docs_test.go`

**Interfaces:**
- Produces:
  - `type Page struct { Slug, Section, Title string; Roles, Covers []string; Order int; Headings []string; Body string }` with JSON `slug,section,title,roles,covers,order,headings`. `Body` is `json:"-"`.
  - `var Roles = []string{"everyone","trainee","author","scorer","approver","leader","admin"}`
  - `var Sections = []string{"getting-started","trainees","authors","scorers","leaders","admins"}`
  - `var Version = "dev"` (set with `-ldflags "-X crucible/internal/docs.Version=…"`)
  - `func Load(fsys fs.FS) ([]Page, error)`: sorted by section order, then `order`, then slug.
  - `type Service struct`; `func New(pages []Page) *Service`; `func (s *Service) Routes(r chi.Router)`: `GET /api/docs` returns `{version, pages}`, `GET /api/docs/*` returns `{slug,title,markdown,version}`.

- [ ] **Step 1: Write the failing test**

`internal/docs/docs_test.go`:

```go
package docs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
)

func page(fm, body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte("---\n" + fm + "\n---\n" + body)} }

func TestLoadParsesAndOrders(t *testing.T) {
	pages, err := Load(fstest.MapFS{
		"trainees/quizzes.md":        page("title: Quizzes\nroles: [trainee]\ncovers: [route:/p/:team/:training/m/:module/quiz]\norder: 20", "# Quizzes\n\n## Attempts\n\n```\n## not a heading\n```\n### Cooldown\n"),
		"trainees/readings.md":       page("title: Readings\nroles: [trainee]\norder: 10", "Read.\n"),
		"getting-started/hearth.md":  page("title: The Hearth\nroles: [everyone]\ncovers: [route:/]", "Home.\n"),
		"admins/kill-switch.md":      page("title: Kill switch\nroles: [admin]", "Stop.\n"),
		"getting-started/README.txt": {Data: []byte("ignored")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range pages {
		slugs = append(slugs, p.Slug)
	}
	if got := strings.Join(slugs, " "); got != "getting-started/hearth trainees/readings trainees/quizzes admins/kill-switch" {
		t.Fatalf("order: %s", got)
	}
	q := pages[2]
	if q.Section != "trainees" || strings.Join(q.Headings, "|") != "Attempts|Cooldown" || !strings.HasPrefix(q.Body, "# Quizzes") {
		t.Fatalf("page: %+v", q)
	}
}

func TestLoadRefusesBadPages(t *testing.T) {
	for name, f := range map[string]*fstest.MapFile{
		"no front matter": {Data: []byte("# Hi\n")},
		"no title":        page("roles: [trainee]", "x"),
		"no roles":        page("title: X", "x"),
		"unknown role":    page("title: X\nroles: [wizard]", "x"),
		"unknown key":     page("title: X\nroles: [trainee]\ncover: [route:/]", "x"),
	} {
		if _, err := Load(fstest.MapFS{"trainees/x.md": f}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(fstest.MapFS{"wizards/x.md": page("title: X\nroles: [trainee]", "x")}); err == nil {
		t.Error("unknown section accepted")
	}
}

func TestRoutes(t *testing.T) {
	pages, _ := Load(fstest.MapFS{"trainees/quizzes.md": page("title: Quizzes\nroles: [trainee]", "# Quizzes\n")})
	r := chi.NewRouter()
	New(pages).Routes(r)
	get := func(path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, body := get("/api/docs"); code != 200 || body["version"] != Version || len(body["pages"].([]any)) != 1 {
		t.Fatalf("index: %d %v", code, body)
	}
	if code, body := get("/api/docs/trainees/quizzes"); code != 200 || body["markdown"] != "# Quizzes\n" {
		t.Fatalf("page: %d %v", code, body)
	}
	for _, p := range []string{"/api/docs/trainees/nope", "/api/docs/../../etc/passwd", "/api/docs/trainees/quizzes.md", "/api/docs/%2e%2e/x"} {
		if code, _ := get(p); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/docs`
Expected: build failure.

- [ ] **Step 3: Implement `internal/docs/docs.go`**

```go
// Package docs serves the in-app guide (the Docs tab): Markdown pages from docs/user with front matter, plus pages
// generated from the block registry. Pages explain Crucible itself and never contain training content, so any
// signed-in user may read all of them.
package docs

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"crucible/internal/apperr"
	"crucible/internal/httpx"
)

// Version is the Crucible build, set with -ldflags "-X crucible/internal/docs.Version=<git describe>".
var Version = "dev"

var (
	Roles    = []string{"everyone", "trainee", "author", "scorer", "approver", "leader", "admin"}
	Sections = []string{"getting-started", "trainees", "authors", "scorers", "leaders", "admins"}
)

type Page struct {
	Slug     string   `json:"slug"`
	Section  string   `json:"section"`
	Title    string   `json:"title"`
	Roles    []string `json:"roles"`
	Covers   []string `json:"covers"`
	Order    int      `json:"order"`
	Headings []string `json:"headings"`
	Body     string   `json:"-"`
}

type front struct {
	Title  string   `yaml:"title"`
	Roles  []string `yaml:"roles"`
	Covers []string `yaml:"covers"`
	Order  int      `yaml:"order"`
}

// Load reads every *.md under fsys (one folder per section) and sorts them by section, order and slug.
func Load(fsys fs.FS) ([]Page, error) {
	var pages []Page
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		pg, err := parse(strings.TrimSuffix(p, ".md"), string(b))
		if err != nil {
			return fmt.Errorf("docs/user/%s: %w", p, err)
		}
		pages = append(pages, pg)
		return nil
	})
	Sort(pages)
	return pages, err
}

// Sort orders pages by section, then order, then slug.
func Sort(pages []Page) {
	slices.SortFunc(pages, func(a, b Page) int {
		return cmp.Or(cmp.Compare(slices.Index(Sections, a.Section), slices.Index(Sections, b.Section)), cmp.Compare(a.Order, b.Order), cmp.Compare(a.Slug, b.Slug))
	})
}

func parse(slug, src string) (Page, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	rest, ok := strings.CutPrefix(src, "---\n")
	fm, body, ok2 := strings.Cut(rest, "\n---\n")
	if !ok || !ok2 {
		return Page{}, errors.New("a page starts with a --- front matter block")
	}
	var f front
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Page{}, fmt.Errorf("front matter: %w", err)
	}
	section, _, _ := strings.Cut(slug, "/")
	switch {
	case f.Title == "":
		return Page{}, errors.New("title is required")
	case len(f.Roles) == 0:
		return Page{}, errors.New("roles is required")
	case !slices.Contains(Sections, section) || !strings.Contains(slug, "/"):
		return Page{}, fmt.Errorf("pages live in one of %v", Sections)
	}
	for _, r := range f.Roles {
		if !slices.Contains(Roles, r) {
			return Page{}, fmt.Errorf("unknown role %q (one of %v)", r, Roles)
		}
	}
	body = strings.TrimLeft(body, "\n")
	return Page{Slug: slug, Section: section, Title: f.Title, Roles: f.Roles, Covers: f.Covers, Order: f.Order, Headings: headings(body), Body: body}, nil
}

// headings are the ## and ### titles outside code fences (client-side search matches them).
func headings(body string) []string {
	out, fence := []string{}, false
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if h, ok := strings.CutPrefix(t, "## "); ok {
			out = append(out, strings.TrimSpace(h))
		} else if h, ok := strings.CutPrefix(t, "### "); ok {
			out = append(out, strings.TrimSpace(h))
		}
	}
	return out
}

type Service struct {
	pages  []Page
	bySlug map[string]Page
}

func New(pages []Page) *Service {
	s := &Service{pages: pages, bySlug: map[string]Page{}}
	for _, p := range pages {
		s.bySlug[p.Slug] = p
	}
	return s
}

// Routes: the slug is only ever a map key, never a file path.
func (s *Service) Routes(r chi.Router) {
	r.Get("/api/docs", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"version": Version, "pages": s.pages})
	})
	r.Get("/api/docs/*", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.bySlug[chi.URLParam(r, "*")]
		if !ok {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "no such page"))
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"slug": p.Slug, "title": p.Title, "markdown": p.Body, "version": Version})
	})
}
```

- [ ] **Step 4: Run it**

Run: `gofmt -l internal && go test ./internal/docs`
Expected: PASS.

- [ ] **Step 5: Update docs/user**

There are no pages yet, and this task changes no user-visible capability: the routes are not wired until Task 5. Nothing to update.

- [ ] **Step 6: Commit**

```bash
git add -- internal/docs
git commit -m "feat(docs): page loader with strict front matter and the docs API" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/docs
```

---

### Task 5: The starter pages, the coverage test, the reference page and the docs rule

**Files:**
- Create: `docs/user/embed.go`, `docs/user/**/*.md` (27 pages, listed below), `internal/docs/generated.go`, `internal/docs/coverage_test.go`, `.github/pull_request_template.md`
- Modify: `internal/httpapi/server.go` (`Deps.Docs`), `cmd/crucible-api/main.go`, `Makefile`, `Dockerfile`, `.dockerignore`, `CLAUDE.md`

**Interfaces:**
- Consumes: `docs.Load`, `docs.New`, `docs.Sort`, `docs.Version` (Task 4); `blocks.Fields`, `blocks.YAMLFields` (Task 2).
- Produces:
  - package `userdocs` (`crucible/docs/user`): `var FS embed.FS`
  - `func docs.Generated() []docs.Page`: one page in this task (`authors/building-blocks`); Task 15 adds the catalog pages.
  - `func docs.All() ([]Page, error)`: embedded pages plus generated ones, sorted.

- [ ] **Step 1: Write the failing coverage test**

`internal/docs/coverage_test.go`:

```go
package docs

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestDocsCoverEveryRouteAndRole keeps the Docs tab current (CLAUDE.md: a capability change updates docs/user in the
// same commit). It fails when an SPA route or a role has no page, or a page covers a route that no longer exists.
func TestDocsCoverEveryRouteAndRole(t *testing.T) {
	pages, err := All()
	if err != nil {
		t.Fatal(err)
	}
	app, err := os.ReadFile("../../web/src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for _, m := range regexp.MustCompile(`<Route path="([^"]+)"`).FindAllStringSubmatch(string(app), -1) {
		if m[1] != "*" {
			routes[m[1]] = false
		}
	}
	if len(routes) < 20 {
		t.Fatalf("found only %d routes in App.tsx: did the route syntax change?", len(routes))
	}
	roles := map[string]bool{}
	for _, p := range pages {
		if strings.TrimSpace(p.Body) == "" {
			t.Errorf("%s is empty", p.Slug)
		}
		for _, r := range p.Roles {
			roles[r] = true
		}
		for _, c := range p.Covers {
			kind, v, _ := strings.Cut(c, ":")
			switch kind {
			case "route":
				if _, ok := routes[v]; !ok {
					t.Errorf("%s covers route %s, which web/src/App.tsx no longer has", p.Slug, v)
				}
				routes[v] = true
			case "feature":
			default:
				t.Errorf("%s: unknown cover %q (use route:, block: or feature:)", p.Slug, c)
			}
		}
	}
	for r, covered := range routes {
		if !covered {
			t.Errorf("no page in docs/user covers route %s: add `route:%s` to the covers of the page that explains it", r, r)
		}
	}
	for _, r := range Roles {
		if !roles[r] {
			t.Errorf("no page for role %s", r)
		}
	}
	if !slices.ContainsFunc(pages, func(p Page) bool { return p.Slug == "authors/building-blocks" }) {
		t.Error("the generated building-blocks page is missing")
	}
}
```

Run: `go test ./internal/docs -run Coverage`
Expected: build failure (`undefined: All`).

- [ ] **Step 2: Embed the pages and generate the reference page**

`docs/user/embed.go`:

```go
// Package userdocs embeds the in-app guide (the Docs tab). One folder per section; every page starts with front matter
// (title, roles, covers, order). Any capability change updates these pages in the same commit (CLAUDE.md).
package userdocs

import "embed"

//go:embed */*.md
var FS embed.FS
```

`internal/docs/generated.go`:

```go
package docs

import (
	"fmt"
	"reflect"
	"strings"

	userdocs "crucible/docs/user"
	"crucible/internal/content"
	"crucible/internal/content/blocks"
)

// All is every page: docs/user plus the generated ones, sorted.
func All() ([]Page, error) {
	pages, err := Load(userdocs.FS)
	if err != nil {
		return nil, err
	}
	pages = append(pages, Generated()...)
	Sort(pages)
	return pages, nil
}

var referenceTypes = []struct {
	v     any
	title string
}{
	{content.Training{}, "training.yaml"}, {content.Module{}, "module.yaml"}, {content.Quiz{}, "quiz.yaml"},
	{content.Question{}, "Quiz questions"}, {content.Lab{}, "lab.yaml"}, {content.Terminal{}, "Lab terminals"},
	{content.Script{}, "Scripts (lab setup, task check and setup)"}, {content.Task{}, "Lab tasks"}, {content.Hint{}, "Hints"},
	{content.AWSConfig{}, "AWS settings (lab.yaml aws:)"},
}

// Generated are the pages built from the block registry at startup, so they can't drift from the code.
func Generated() []Page {
	var b strings.Builder
	b.WriteString("Every key Crucible reads from a training repo. The editor shows the same text when you hover a key.\n")
	for _, rt := range referenceTypes {
		t := reflect.TypeOf(rt.v)
		fmt.Fprintf(&b, "\n## %s\n\n| Key | Type | Required | What it does |\n|---|---|---|---|\n", rt.title)
		for _, f := range blocks.YAMLFields(t) {
			d := blocks.Fields[t.Name()+"."+f.Key]
			req := ""
			if d.Required {
				req = "yes"
			}
			desc := d.Description
			if len(d.Enum) > 0 {
				desc += " One of: `" + strings.Join(d.Enum, "`, `") + "`."
			}
			if d.Default != "" {
				desc += " Default: `" + d.Default + "`."
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", f.Key, kindName(f.Type), req, strings.ReplaceAll(desc, "|", `\|`))
		}
	}
	body := b.String()
	return []Page{{Slug: "authors/building-blocks", Section: "authors", Title: "Building blocks: every key", Roles: []string{"author"},
		Covers: []string{"feature:blocks.reference"}, Order: 90, Headings: headings(body), Body: body}}
}

func kindName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Name() == "Duration":
		return "duration (`90s`, `20m`, `1h`)"
	case t.Name() == "Node":
		return "depends on the question type"
	}
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "true / false"
	case reflect.Int:
		return "whole number"
	case reflect.Float64:
		return "number"
	case reflect.Slice:
		return "list"
	}
	return "mapping"
}
```

- [ ] **Step 3: Write the 27 starter pages**

Every page has front matter, an H1 that matches its title, and plain prose written from the user's side of the screen, in the forge voice, at most about 60 lines. Before writing a page, verify every fact in its bullets against the source file named in brackets, and correct any bullet the code contradicts. Link between pages with absolute `/docs/<slug>` links. Never quote real training content.

Model page (write exactly this, then the others in the same style). `docs/user/getting-started/the-hearth.md`:

```markdown
---
title: The Hearth
roles: [everyone]
covers: [route:/, route:/trainings]
order: 20
---
# The Hearth

The Hearth is your home in Crucible: everything you are working on, and what needs you next.

## What you see

- **Your trainings.** One card per training you are enrolled in, with its heat bar: how much of it you have forged.
  Open a card to see its modules.
- **What needs you.** Labs waiting for you and anything a scorer sent back.
- **Your rank.** Ore to Masterwork. A rank you have earned is never lost.

## Finding more trainings

**Trainings** in the top bar lists every training on this Crucible, with its estimate and who in your team is
enrolled. Ask your team leader to enrol you in one.

## Help on any page

The **?** next to the menu opens the page of these docs about the screen you are on.
```

The other pages. Slug, `roles`, `covers`, `order`, then what the page must say:

| Slug | roles | covers | order | Must say [verify in] |
|---|---|---|---|---|
| `getting-started/signing-in` | everyone | `feature:auth` | 10 | Sign in with your company account (OIDC; Keycloak or Cognito); sessions; logging out; what a 401 sends you to [internal/auth/oidc.go, web/src/api.ts] |
| `getting-started/themes-and-calm` | everyone | `route:/settings` | 30 | The four themes Forge, Anvil, Quench, High Contrast; calm mode turns animation off, and so does the OS reduce-motion setting [web/src/theme/theme.ts, pages/Settings.tsx] |
| `getting-started/notifications` | everyone | `feature:notifications` | 40 | Which events notify you (approvals, scores, content edits, rank-ups) and where you change notification settings [internal/notify/notify.go, pages/Settings.tsx] |
| `getting-started/using-the-docs` | everyone | `feature:docs` (Task 6 adds `route:/docs/*` along with the route) | 50 | Docs opens on your role's section; the **All** filter; search matches titles and headings; the ? link [this plan, Task 6] |
| `trainees/trainings-and-readings` | trainee | `route:/p/:team/:training`, `route:/p/:team/:training/m/:module/read/:item` | 10 | Linear vs free progression; module completion (all items, or a score threshold); readings marked done [internal/learn, content.Module] |
| `trainees/quizzes` | trainee | `route:/p/:team/:training/m/:module/quiz` | 20 | Pass threshold; attempts and cooldown; question kinds; human-scored answers wait on the Anvil; final scores are final [internal/learn/quiz.go] |
| `trainees/laptop-labs` | trainee | `route:/p/:team/:training/m/:module/lab`, `feature:lab.local` | 30 | A local lab runs in Docker on your laptop through the agent; tasks and checks; TTL and idle stop; the terminal's leave chord Ctrl+Alt+↑ or Ctrl+Shift+F6 [internal/labs/local.go, components/Terminal.tsx, lib/advanceFocus.ts] |
| `trainees/connect-your-laptop` | trainee | `route:/connect`, `feature:agent` | 40 | Generate a pairing token and run the printed `crucible-agent` command; the token sits in an env var, not argv; revoke it from the same page [internal/httpapi/server.go /api/agent/tokens, pages/Connect.tsx] |
| `trainees/cluster-and-aws-labs` | trainee | `route:/labs`, `feature:lab.cluster`, `feature:lab.aws` | 50 | Cluster labs need nothing installed; AWS labs may need an approval because they cost money; the Labs page lists your running labs [internal/labs/cluster.go, aws.go, approvals.go, pages/Labs.tsx] |
| `trainees/hints-and-extensions` | trainee | `feature:hints`, `feature:extensions` | 60 | A hint costs points (the lab's hint_cost or its own cost); asking for more time creates an extension request a leader approves [content.Hint.EffectiveCost, internal/labs extension code] |
| `trainees/ranks-and-badges` | trainee | `feature:ranks` | 70 | Ranks Ore → Masterwork by weighted completion across your programs; ranks only rise; a badge per finished training; no leaderboards [internal/learn/forge.go] |
| `trainees/feedback-from-scorers` | trainee | `feature:feedback` | 80 | Human-scored answers and review tasks; a returned answer with feedback; resubmitting [internal/scoring/scoring.go] |
| `authors/repo-layout` | author | `feature:repo.layout` | 10 | training.yaml, modules/<id>/module.yaml, reading/*.md, quiz.yaml, lab/ (lab.yaml, compose.yaml, tasks/, checks/, setup/, hints/), assets/ for images; links to `/docs/authors/building-blocks` [content.Load] |
| `authors/editing-content` | author | `route:/edits/new` | 20 | Today's editor: choose a training on **Edits**, open a file, change it, give a title, submit for review; who may edit (admins, maintainers, leaders and seniors of teams running it, never anyone enrolled); limits (20 files, 256 KiB each) [internal/edits/edits.go canPropose, gitsync CheckOps]. Task 10 rewrites this page for the IDE. |
| `authors/review-and-merge` | author | `route:/edits`, `route:/edits/:id` | 30 | Every edit is pushed to its own branch; a maintainer other than you (or an admin) approves; the bot merges exactly the reviewed commit; stale edits and "Redo on the current version"; 5 open edits and 10 per hour [internal/edits decide, limits] |
| `authors/lint-and-preview` | author | `feature:cli.lint`, `feature:cli.preview` | 40 | `crucible lint <repo>` runs the same checks as the server; `crucible preview` runs a draft training (labs included) on your machine; Docker required [cmd/crucible/main.go lint, preview.go] |
| `scorers/the-anvil` | scorer | `route:/anvil`, `route:/anvil/:id` | 10 | The queue of answers and review tasks to score; you never score a training you are enrolled in; what the Anvil shows (transcript, uploads) [internal/scoring] |
| `scorers/rubrics-and-sign-offs` | scorer | `feature:rubrics`, `feature:signoff` | 20 | Rubrics are written by authors and never shown to trainees; scoring, returning with feedback, overriding; sign-off questions; admin resets [internal/scoring, /api/admin/quiz-reset] |
| `leaders/teams-and-programs` | leader | `route:/teams`, `route:/teams/:team`, `route:/teams/:team/programs/:training` | 10 | Teams, roles (leader, senior, member, trainee, mentors); enrolling a team or a person; program settings saved to git by the bot [internal/configapi] |
| `leaders/schedules-budgets-and-caps` | leader, approver | `feature:budgets`, `feature:schedules` | 20 | Lab schedules; team budgets fail closed; caps; cluster labs priced per hour; AWS labs estimated with infracost [internal/labs/finops.go, config/schedule.go] |
| `leaders/approvals-and-extensions` | approver, leader | `route:/approvals` | 30 | Approval tiers by estimate; who can approve what; extension requests; you never approve your own [rbac.MayApprove, Tier] |
| `leaders/journey-and-mentor` | leader | `route:/teams/:team/journey`, `route:/mentor` | 40 | The heat map per person and module; stuck flags (failed checks, final hint, inactive, returned twice, not started); mentors see their mentees [internal/journey/flags.go] |
| `leaders/ledger-and-content-versions` | leader, approver | `route:/ledger`, `feature:content.versions` | 50 | The Ledger of lab spend; a program runs a pinned content version; bumping it shows the commits and changed files first [internal/labs/ledger.go, gitsync Changes] |
| `admins/forge-status-and-kill-switch` | admin | `route:/admin` | 10 | What Forge Status shows; the kill switch stops new labs [pages/ForgeStatus.tsx, configapi] |
| `admins/agent-tokens` | admin | `feature:agent.revoke` | 20 | Revoking any user's laptop pairing (offboarding); their labs keep running until idle or TTL [DELETE /api/admin/agent/tokens] |
| `admins/first-admin-audit-and-runbooks` | admin | `feature:audit`, `feature:bootstrap-admin` | 30 | The first admin (admins.yaml, bootstrap); the audit log of privileged actions; links to the AWS and Cognito runbooks (`docs/runbooks/aws.md`, `cognito.md`), described in prose because they live in the code repo [internal/audit, configapi seed] |

`getting-started/the-hearth` (the model above) is the 27th page.

- [ ] **Step 4: Wire the docs into the server and the build**

`internal/httpapi/server.go`: add `Docs *docs.Service` to `Deps`, and inside the authenticated group:

```go
		if d.Docs != nil {
			d.Docs.Routes(r)
		}
```

`cmd/crucible-api/main.go`, before the server is built:

```go
	pages, err := docs.All()
	if err != nil {
		return fmt.Errorf("docs: %w", err) // a broken page is a build bug: the coverage test catches it first
	}
```

and pass `Docs: docs.New(pages)` in `httpapi.Deps`. Use whatever error-return shape the surrounding function already uses.

`Makefile`, the `build` target:

```make
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "-X crucible/internal/docs.Version=$$(git describe --always --dirty 2>/dev/null || echo dev)" -o bin/ ./cmd/...
```

`Dockerfile`, the `go` stage (after `COPY internal/ internal/`):

```dockerfile
COPY docs/user/ docs/user/
ARG CRUCIBLE_VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X crucible/internal/docs.Version=${CRUCIBLE_VERSION}" -o /out/crucible-api ./cmd/crucible-api && CGO_ENABLED=0 go build -o /out/crucible ./cmd/crucible
```

This replaces the existing `RUN` line. `.dockerignore`: keep `docs` and add the line `!docs/user` right after it.

- [ ] **Step 5: Add the docs rule to `CLAUDE.md` and the pull-request checklist**

In `CLAUDE.md` under **Hard rules**, add:

```markdown
- **Docs:** any capability change updates `docs/user` in the same commit. `go test ./internal/docs` fails when a SPA
  route, a role or a catalog block has no page, or a page covers something that no longer exists.
```

`.github/pull_request_template.md`:

```markdown
## What changed

## Checklist

- [ ] Tests first for behaviour changes; security and money paths keep a test that fails without the fix
- [ ] Any capability change updates `docs/user` in this same change (`go test ./internal/docs` passes)
- [ ] Commits use the pathspec form and only touch my files
```

- [ ] **Step 6: Run the checks**

Run: `gofmt -l cmd internal docs && go vet ./... && go test ./internal/docs ./internal/httpapi ./cmd/... && make build`
Expected: PASS. The coverage test lists any route left uncovered: add it to the right page's `covers`, not to an exception list.

- [ ] **Step 7: Commit**

```bash
git add -- docs/user .github/pull_request_template.md internal/docs/generated.go internal/docs/coverage_test.go
git commit -m "docs(user): starter Docs pages by role, generated key reference, coverage test and the docs rule" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs/user .github/pull_request_template.md internal/docs internal/httpapi/server.go cmd/crucible-api/main.go Makefile Dockerfile .dockerignore CLAUDE.md
```

---

### Task 6: The Docs tab in the SPA, the nav link and the ? links

**Files:**
- Create: `web/src/lib/docs.ts`, `web/src/lib/docs.test.ts`, `web/src/pages/Docs.tsx`, `web/src/components/HelpLink.tsx`
- Modify: `web/src/App.tsx` (route `/docs/*`), `web/src/components/Nav.tsx`, `web/src/theme/app.css`, `docs/user/getting-started/using-the-docs.md`

**Interfaces:**
- Consumes: `GET /api/docs` returns `{version, pages: DocPage[]}`; `GET /api/docs/{slug}` returns `{slug,title,markdown,version}` (Task 4).
- Produces (TS):
  - `type DocPage = { slug; section; title; roles: string[]; covers: string[]; order: number; headings: string[] }`
  - `rolesOf(me: Me): string[]`
  - `docSections(pages, roles, all: boolean): { id: string; title: string; mine: boolean; pages: DocPage[] }[]`
  - `landing(pages, roles): string | undefined`
  - `helpFor(pages, pathname): DocPage | undefined`
  - `searchDocs(pages, q): DocPage[]`

- [ ] **Step 1: Write the failing tests**

`web/src/lib/docs.test.ts`:

```ts
import { expect, test } from 'vitest'
import { docSections, helpFor, landing, rolesOf, searchDocs, type DocPage } from './docs'
import type { Me } from '../types'

const p = (slug: string, roles: string[], covers: string[] = [], order = 0, headings: string[] = []): DocPage =>
  ({ slug, section: slug.split('/')[0], title: slug.split('/')[1], roles, covers, order, headings })
const pages = [
  p('getting-started/the-hearth', ['everyone'], ['route:/', 'route:/trainings'], 20),
  p('getting-started/signing-in', ['everyone'], [], 10),
  p('trainees/laptop-labs', ['trainee'], ['route:/p/:team/:training/m/:module/lab'], 30, ['Leaving the terminal']),
  p('authors/editing-content', ['author'], ['route:/edits/new'], 20),
  p('leaders/approvals', ['approver', 'leader'], ['route:/approvals'], 30),
  p('admins/forge-status', ['admin'], ['route:/admin'], 10),
]
const me = (over: Partial<Me>): Me => ({ user: { id: 1, email: 'x@y', name: 'X', theme: '', calm_motion: false }, is_admin: false, default_theme: 'forge',
  teams: [], can_approve: false, can_score: false, can_view_spend: false, is_mentor: false, can_edit_content: false, ...over })

test('roles come from /api/me flags', () => {
  expect(rolesOf(me({}))).toEqual(['everyone', 'trainee'])
  expect(rolesOf(me({ can_edit_content: true, can_approve: true, is_admin: true }))).toEqual(['everyone', 'trainee', 'author', 'approver', 'leader', 'admin'])
})

test('my sections come first, ordered by my highest role; All shows the rest', () => {
  const admin = rolesOf(me({ is_admin: true }))
  expect(docSections(pages, admin, false).map((s) => s.id)).toEqual(['admins', 'trainees', 'getting-started'])
  expect(docSections(pages, admin, true).map((s) => s.id)).toEqual(['admins', 'trainees', 'getting-started', 'leaders', 'authors'])
  expect(docSections(pages, rolesOf(me({})), false)[0].pages.map((x) => x.slug)).toEqual(['trainees/laptop-labs'])
  expect(docSections(pages, rolesOf(me({})), false)[1].pages.map((x) => x.slug)).toEqual(['getting-started/signing-in', 'getting-started/the-hearth'])
})

test('Docs opens on the first page of my highest section', () => {
  expect(landing(pages, rolesOf(me({ is_admin: true })))).toBe('admins/forge-status')
  expect(landing(pages, rolesOf(me({ can_approve: true })))).toBe('leaders/approvals')
  expect(landing(pages, rolesOf(me({})))).toBe('trainees/laptop-labs')
})

test('? links match route patterns exactly', () => {
  expect(helpFor(pages, '/')?.slug).toBe('getting-started/the-hearth')
  expect(helpFor(pages, '/p/forge/forge-101/m/02-first-lab/lab')?.slug).toBe('trainees/laptop-labs')
  expect(helpFor(pages, '/edits/new')?.slug).toBe('authors/editing-content')
  expect(helpFor(pages, '/nowhere')).toBeUndefined()
})

test('search matches titles and headings, case-insensitively', () => {
  expect(searchDocs(pages, 'TERMINAL').map((x) => x.slug)).toEqual(['trainees/laptop-labs'])
  expect(searchDocs(pages, 'forge-st').map((x) => x.slug)).toEqual(['admins/forge-status'])
  expect(searchDocs(pages, '')).toHaveLength(pages.length)
})
```

Run: `cd web && npx vitest run src/lib/docs.test.ts`
Expected: FAIL, cannot find `./docs`.

- [ ] **Step 2: Implement `web/src/lib/docs.ts`**

```ts
import { matchPath } from 'react-router'
import type { Me } from '../types'

export type DocPage = { slug: string; section: string; title: string; roles: string[]; covers: string[]; order: number; headings: string[] }
export type DocSection = { id: string; title: string; mine: boolean; pages: DocPage[] }

const SECTIONS: { id: string; title: string; roles: string[] }[] = [
  { id: 'admins', title: 'Admins', roles: ['admin'] },
  { id: 'leaders', title: 'Leaders and approvers', roles: ['leader', 'approver'] },
  { id: 'scorers', title: 'Scorers', roles: ['scorer'] },
  { id: 'authors', title: 'Authors', roles: ['author'] },
  { id: 'trainees', title: 'Trainees', roles: ['trainee'] },
  { id: 'getting-started', title: 'Getting started', roles: ['everyone'] },
] // highest role first: Docs opens on the first section that is mine

export function rolesOf(me: Me): string[] {
  const r = ['everyone', 'trainee']
  if (me.can_edit_content) r.push('author')
  if (me.can_score) r.push('scorer')
  if (me.can_approve) r.push('approver', 'leader')
  if (me.is_admin) r.push('admin')
  return r
}

export function docSections(pages: DocPage[], roles: string[], all: boolean): DocSection[] {
  const out = SECTIONS.map((s) => ({
    id: s.id, title: s.title, mine: s.roles.some((r) => roles.includes(r)),
    pages: pages.filter((p) => p.section === s.id).sort((a, b) => a.order - b.order || a.slug.localeCompare(b.slug)),
  })).filter((s) => s.pages.length > 0 && (all || s.mine))
  return [...out.filter((s) => s.mine), ...out.filter((s) => !s.mine)]
}

export const landing = (pages: DocPage[], roles: string[]) => docSections(pages, roles, false)[0]?.pages[0]?.slug

export function helpFor(pages: DocPage[], pathname: string): DocPage | undefined {
  for (const s of docSections(pages, ['everyone'], true)) {
    for (const p of s.pages) {
      if (p.covers.some((c) => c.startsWith('route:') && matchPath(c.slice('route:'.length), pathname))) return p
    }
  }
}

export function searchDocs(pages: DocPage[], q: string): DocPage[] {
  const s = q.trim().toLowerCase()
  return s ? pages.filter((p) => [p.title, ...p.headings].some((t) => t.toLowerCase().includes(s))) : pages
}
```

Run the test again. Expected: PASS.

- [ ] **Step 3: The page, the ? link and the nav**

`web/src/components/HelpLink.tsx`:

```tsx
import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { api } from '../api'
import { helpFor, type DocPage } from '../lib/docs'

let index: Promise<DocPage[]> | undefined // fetched once per page load
export const docIndex = () => (index ??= api<{ pages: DocPage[] }>('/api/docs').then((d) => d.pages).catch(() => { index = undefined; return [] }))

// HelpLink is the small "?" in the nav: the Docs page that covers the current route.
export function HelpLink() {
  const { pathname } = useLocation()
  const [pages, setPages] = useState<DocPage[]>([])
  useEffect(() => { docIndex().then(setPages) }, [])
  const page = helpFor(pages, pathname)
  if (!page || pathname.startsWith('/docs')) return null
  return <Link to={`/docs/${page.slug}`} className="help-link" aria-label="Help for this page" title={`Help: ${page.title}`}>?</Link>
}
```

`web/src/pages/Docs.tsx`:

```tsx
import { useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router'
import { useMe } from '../me'
import { useFetch } from '../useFetch'
import { docSections, landing, rolesOf, searchDocs, type DocPage } from '../lib/docs'
import { docIndex } from '../components/HelpLink'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'

export function DocsPage() {
  const slug = useParams()['*'] ?? ''
  const { me } = useMe()
  const roles = rolesOf(me)
  const [pages, setPages] = useState<DocPage[]>()
  const [all, setAll] = useState(false)
  const [q, setQ] = useState('')
  useEffect(() => { docIndex().then(setPages) }, [])
  const page = useFetch<{ title: string; markdown: string; version: string }>(slug ? `/api/docs/${slug}` : null)
  if (!pages) return <Loader label="Opening the docs…" />
  if (!slug) {
    const to = landing(pages, roles)
    return to ? <Navigate to={`/docs/${to}`} replace /> : <section className="page"><p>No docs yet.</p></section>
  }
  const found = new Set(searchDocs(pages, q).map((p) => p.slug))
  return (
    <section className="page docs">
      <nav className="docs-nav" aria-label="Docs">
        <label>Search the docs <input type="search" value={q} onChange={(e) => setQ(e.target.value)} /></label>
        <label><input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> All</label>
        {docSections(pages, roles, all || q !== '').map((s) => {
          const list = s.pages.filter((p) => found.has(p.slug))
          return list.length > 0 && (
            <div key={s.id}>
              <h2>{s.title}</h2>
              <ul>{list.map((p) => <li key={p.slug}><Link to={`/docs/${p.slug}`} aria-current={p.slug === slug ? 'page' : undefined}>{p.title}</Link></li>)}</ul>
            </div>
          )
        })}
      </nav>
      <article>
        {page.error ? <ErrorBox error={page.error} /> : !page.data ? <Loader label="Reading…" /> : (
          <>
            <Markdown text={page.data.markdown} />
            <p className="muted">Last updated with Crucible {page.data.version}</p>
          </>
        )}
      </article>
    </section>
  )
}
```

In `App.tsx`, import `DocsPage` and add `<Route path="/docs/*" element={<DocsPage />} />` before the `*` route. In `Nav.tsx`, import `HelpLink` and add `<NavLink to="/docs">Docs</NavLink>` before **Settings**, and `<HelpLink />` right after the spacer. Check first that `grep -n "'Docs'" e2e/tests/*.ts` finds nothing.

`app.css`, appended. Use the existing tokens only, so the contrast tests keep holding:

```css
.docs { display: grid; grid-template-columns: 240px 1fr; gap: 2rem; max-width: 1200px; }
.docs-nav ul { list-style: none; padding: 0; margin: 0 0 1rem; }
.docs-nav h2 { font-size: 0.9rem; text-transform: uppercase; color: var(--muted); }
.docs-nav a[aria-current='page'] { font-weight: 700; }
.help-link { border: 1px solid var(--accent); border-radius: 50%; width: 1.6rem; height: 1.6rem; display: inline-grid; place-items: center; }
@media (max-width: 900px) { .docs { grid-template-columns: 1fr; } }
```

- [ ] **Step 4: Update docs/user in the same commit**

In `docs/user/getting-started/using-the-docs.md`, add `route:/docs/*` to `covers` and describe what shipped: the **Docs** link in the top bar; the page opens on the section for your role; **All** shows every section; the search box filters by title and heading; the **?** in the top bar opens the page about the screen you are on.

- [ ] **Step 5: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint && cd .. && go test ./internal/docs`
Expected: PASS. The coverage test sees `/docs/*` covered by `using-the-docs`.

- [ ] **Step 6: Commit**

```bash
git add -- web/src/lib/docs.ts web/src/lib/docs.test.ts web/src/pages/Docs.tsx web/src/components/HelpLink.tsx
git commit -m "feat(web): Docs tab by role with search, and a ? link on every page" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/lib/docs.ts web/src/lib/docs.test.ts web/src/pages/Docs.tsx web/src/components/HelpLink.tsx web/src/App.tsx web/src/components/Nav.tsx web/src/theme/app.css docs/user/getting-started/using-the-docs.md
```

---
## Delivery step 3: edits as operations, drafts with rebase

### Task 7: Edits become operations (rename, delete), with a rename-aware diff and the files-map back-compat

**Files:**
- Create: `internal/db/migrations/00018_edit_ops.sql`
- Modify: `internal/gitsync/content_repo.go`, `internal/gitsync/content_repo_test.go`, `internal/edits/edits.go`, `internal/edits/edits_test.go`, `internal/edits/http_test.go`, `internal/db/db_test.go`
- Modify (web): `web/src/types.ts`, `web/src/pages/EditReview.tsx`, `web/src/pages/EditFiles.tsx`
- Modify (docs): `docs/user/authors/review-and-merge.md`

**Interfaces:**
- Consumes: `gitsync.Op`, `CheckOps`, `ApplyOps`, `Targets`, `PutOps`, `CheckPath` (Task 1); `edits.Check` (Task 3).
- Produces:
  - `func (c *ContentRepo) PushEdit(ctx, branch, base string, ops []Op, author, msg string) (sha, diff string, err error)`. `CheckEditFiles` is deleted.
  - `edits.NewEdit{Training, BaseSHA, Title string; Ops []gitsync.Op; Files map[string]string}`. Files is the old shape, accepted for one release.
  - `edits.Edit.Ops []gitsync.Op` (`json:"ops,omitempty"`, detail only). It replaces `Edit.Files`.
  - `type edits.FileInfo struct { Path string; Size int64; Editable bool; Reason string }` with JSON `path,size,editable,reason,omitempty`.
  - `func edits.ListFiles(dir string) ([]FileInfo, error)`
  - TS: `export type EditOp = { op: 'put'; path: string; content: string } | { op: 'rename'; from: string; to: string } | { op: 'delete'; path: string }`, `ContentEdit.ops?: EditOp[]` (replacing `files`), `export type FileEntry = { path: string; size: number; editable: boolean; reason?: string }`.

- [ ] **Step 1: Write the failing gitsync tests**

In `content_repo_test.go`, first switch every existing `PushEdit(..., map[string]string{...}, ...)` argument to `PutOps(map[string]string{...})`. 18 calls across `internal/gitsync` and `internal/edits` tests use the old shape: `grep -n 'PushEdit(' internal/*/*_test.go`. Then add:

```go
func TestPushEditRenameDeleteAndModes(t *testing.T) {
	ctx := context.Background()
	files := trainingFiles()
	files["modules/m1/module.yaml"] = "title: M1\nitems:\n  - reading: reading/intro.md\n  - lab: lab\n"
	files["modules/m1/reading/notes.md"] = "old notes\nline two\n"
	files["modules/m1/reading/tool.sh"] = "#!/bin/sh\necho reading\n"
	remote := bare(t, files)
	work := filepath.Join(t.TempDir(), "w") // an executable lab script on main (bare() writes 0644)
	run(t, "", "clone", "-q", remote, work)
	if err := writeFile(work, "modules/m1/lab/checks/a.sh", "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, "modules/m1/lab/checks/a.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qm", "lab script")
	run(t, work, "push", "-q", "origin", "HEAD:main")
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	sha, diff, err := c.PushEdit(ctx, "crucible/edit/1", base, []Op{
		{Op: "rename", From: "modules/m1/lab/checks/a.sh", To: "modules/m1/reading/a.sh"},     // leaves the lab: loses +x
		{Op: "rename", From: "modules/m1/reading/tool.sh", To: "modules/m1/lab/checks/tool.sh"}, // enters the lab: gains +x
		{Op: "delete", Path: "modules/m1/reading/notes.md"},
	}, "a@x", "move scripts")
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"modules/m1/reading/a.sh": "100644", "modules/m1/lab/checks/tool.sh": "100755"} {
		if mode := gitOut(t, remote, "ls-tree", sha, p); !strings.HasPrefix(mode, want) {
			t.Errorf("%s: %q, want %s", p, mode, want)
		}
	}
	for _, s := range []string{"rename from modules/m1/reading/tool.sh", "rename to modules/m1/lab/checks/tool.sh",
		"deleted file mode", "-old notes", "-line two", "old mode 100755", "new mode 100644"} {
		if !strings.Contains(diff, s) {
			t.Errorf("diff lacks %q:\n%s", s, diff)
		}
	}
}

func TestPushEditOpsRefusals(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	for name, ops := range map[string][]Op{
		"case-only rename": {{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/Intro.md"}},
		"case dir rename":  {{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/M1/reading/x.md"}},
		"rename training":  {{Op: "rename", From: "training.yaml", To: "modules/m1/t.yaml"}},
		"delete training":  {{Op: "delete", Path: "training.yaml"}},
		"missing source":   {{Op: "rename", From: "modules/m1/reading/nope.md", To: "modules/m1/reading/x.md"}},
		"onto a file":      {{Op: "rename", From: "modules/m1/module.yaml", To: "modules/m1/reading/intro.md"}},
		"delete missing":   {{Op: "delete", Path: "modules/m1/reading/nope.md"}},
	} {
		if _, _, err := c.PushEdit(ctx, "crucible/edit/9", base, ops, "a@x", "x"); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if out := gitOut(t, remote, "branch", "--list", "crucible/edit/9"); out != "" {
		t.Fatalf("nothing pushed: %q", out)
	}
}
```

Run: `go test ./internal/gitsync -run 'PushEdit'`
Expected: build failure (PushEdit still takes a map).

- [ ] **Step 2: Implement ops in `ContentRepo.PushEdit`**

In `content_repo.go`:
- Delete `CheckEditFiles` and its comment. `edits` switches to `gitsync.CheckPath` / `CheckOps` in Step 4.
- Change `caseClash(ctx, dir string, files map[string]string)` to `caseClash(ctx context.Context, dir string, paths []string) error`, iterating `slices.Sorted(slices.Values(paths))`.
- Replace `PushEdit` with:

```go
// PushEdit commits ops on top of base as the user, pushes them as branch, and returns the commit and its full,
// rename-aware unified diff against base (deleted files in full). An edit whose diff is over 256 KiB is refused, so the
// reviewer always sees everything. A new or renamed *.sh inside the module's lab (named by module.yaml, maybe in this
// edit) is executable; any other new or renamed file is not; files changed in place keep their mode. base must be on
// the tracked branch, so the diff shows everything a merge would bring in.
func (c *ContentRepo) PushEdit(ctx context.Context, branch, base string, ops []Op, author, msg string) (string, string, error) {
	if !editBranchRE.MatchString(branch) || !shaRE.MatchString(base) {
		return "", "", fmt.Errorf("invalid edit branch %q or base %q", branch, base)
	}
	author, err := cleanEmail(author)
	if err != nil {
		return "", "", err
	}
	if msg, err = cleanMsg(msg); err != nil {
		return "", "", err
	}
	if err := CheckOps(ops); err != nil {
		return "", "", err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", "", err
	}
	stale := apperr.Wrap(apperr.Conflict, "the content changed since you opened it; reload and redo your change")
	if _, err := git(ctx, c.Dir, "merge-base", "--is-ancestor", "--end-of-options", base, "HEAD"); err != nil {
		return "", "", stale
	}
	// base is 40 hex (checked above), so it can't be an option; checkout --detach rejects --end-of-options before git 2.43
	if _, err := git(ctx, c.Dir, "checkout", "-q", "--detach", base); err != nil {
		return "", "", stale
	}
	if err := caseClash(ctx, c.Dir, Targets(ops)); err != nil {
		return "", "", err
	}
	before, beforeErr := maintainers(c.Dir)
	created, err := ApplyOps(c.Dir, ops)
	if err != nil {
		return "", "", err
	}
	for _, rel := range created {
		mode := os.FileMode(0o644)
		if lab := labDir(c.Dir, rel); lab != "" && strings.HasSuffix(rel, ".sh") && strings.HasPrefix(rel, lab) {
			mode = 0o755
		}
		if err := os.Chmod(filepath.Join(c.Dir, filepath.FromSlash(rel)), mode); err != nil {
			return "", "", err
		}
	}
	if slices.Contains(Targets(ops), "training.yaml") {
		after, err := maintainers(c.Dir)
		if beforeErr != nil || err != nil || !slices.Equal(before, after) {
			return "", "", apperr.Wrap(apperr.Invalid, "training.yaml: maintainers can only be changed in git")
		}
	}
	if _, err := git(ctx, c.Dir, "add", "-A"); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "commit", "-q", "--allow-empty",
		"--author", author+" <"+author+">", "-m", msg, "-m", "Crucible-Actor: "+author); err != nil {
		return "", "", err
	}
	sha, err := git(ctx, c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	diff, err := git(ctx, c.Dir, "diff", "--no-color", "-M", "--text", "--no-ext-diff", "--no-textconv", "--end-of-options", base, sha)
	if err != nil {
		return "", "", err
	}
	if len(diff) > maxDiff {
		return "", "", apperr.Wrap(apperr.Invalid, "this edit is too large to review here (diff over 256 KiB); split it or change it in git")
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", "", err
	}
	return sha, diff, nil
}
```

Run: `go test -race ./internal/gitsync`
Expected: PASS, including `TestEditDiffIgnoresRepoAttributes`, `TestPushEditScriptModes` and the new tests.

- [ ] **Step 3: Write the failing edits tests**

Add to `edits_test.go` (import `crucible/internal/gitsync`, which is already imported):

```go
func TestEditOpsRenameAndDelete(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.s.Create(ctx, f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Move intro", Ops: []gitsync.Op{
		{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: reading/start.md\n  - quiz: quiz.yaml\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Diff, "rename from modules/m1/reading/intro.md") || len(e.Ops) != 2 {
		t.Fatalf("rename-aware diff and stored ops: %s %+v", e.Diff, e.Ops)
	}
	if _, err := f.s.Approve(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	if out := git(t, f.remote, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "intro.md") || !strings.Contains(out, "start.md") {
		t.Fatalf("merged tree: %s", out)
	}
}

func TestEditOpsAreValidated(t *testing.T) {
	f := setup(t)
	for name, ops := range map[string][]gitsync.Op{
		"orphaned item":   {{Op: "delete", Path: "modules/m1/reading/intro.md"}}, // module.yaml still lists it: lint refuses
		"delete training": {{Op: "delete", Path: "training.yaml"}},
		"rename training": {{Op: "rename", From: "training.yaml", To: "modules/m1/x.yaml"}},
	} {
		if _, err := f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "x", Ops: ops}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e, err := f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Drop intro", Ops: []gitsync.Op{
		{Op: "delete", Path: "modules/m1/reading/intro.md"},
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - quiz: quiz.yaml\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Diff, "deleted file mode") || !strings.Contains(e.Diff, "-Hello.") {
		t.Fatalf("a deleted file shows in full: %s", e.Diff)
	}
}

// The pre-ops request shape keeps working for one release (ponytail: drop Files after it ships; roadmap).
func TestFilesMapIsTranslatedToPuts(t *testing.T) {
	f := setup(t)
	e, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Ops) != 1 || e.Ops[0] != (gitsync.Op{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nHi.\n"}) {
		t.Fatalf("stored ops: %+v", e.Ops)
	}
	_, err = f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "x",
		Files: map[string]string{"modules/m1/reading/intro.md": "a"}, Ops: []gitsync.Op{{Op: "delete", Path: "modules/m1/quiz.yaml"}}})
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("both shapes at once: %v", err)
	}
}

func TestFilesListsEverythingWithWhatIsEditable(t *testing.T) {
	f := setup(t)
	if err := os.WriteFile(filepath.Join(f.s.State().Training("t1", f.head()).Dir, "modules", "m1", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, files, err := f.s.Files(f.leader, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var txt *FileInfo
	for i := range files {
		if files[i].Path == "modules/m1/notes.txt" {
			txt = &files[i]
		}
	}
	if txt == nil || txt.Editable || !strings.Contains(txt.Reason, "only .md, .yaml, .yml and .sh") {
		t.Fatalf("non-editable files are listed, greyed with the reason: %+v", files)
	}
}
```

In `http_test.go`, add one row to the first table:
`{"POST", "/api/edits", leader, `{"training":"t1","base_sha":"x","title":"x","ops":[{"op":"rename","from":"training.yaml","to":"modules/m1/t.yaml"}]}`, 400}`.
The base check comes after `in.ops()`, so this returns 400 or 409. Assert 400 by giving the real head: build the body with `f.head()` the way the existing rows do.

`TestEditPathRules` currently expects 4 files from `Files`. Since `ListFiles` now lists every file, it still sees 4 (all are editable). Keep it.

- [ ] **Step 4: Implement ops in `edits`**

In `edits.go`:

```go
type NewEdit struct {
	Training string       `json:"training"`
	BaseSHA  string       `json:"base_sha"`
	Title    string       `json:"title"`
	Ops      []gitsync.Op `json:"ops"`
	// Files is the pre-ops shape ({path: new content}), accepted as puts for one release.
	// ponytail: remove after the release that ships ops (roadmap "decisions to revisit").
	Files map[string]string `json:"files,omitempty"`
}

func (in NewEdit) ops() ([]gitsync.Op, error) {
	if len(in.Files) > 0 && len(in.Ops) > 0 {
		return nil, apperr.Wrap(apperr.Invalid, "send ops or files, not both")
	}
	if len(in.Files) > 0 {
		return gitsync.PutOps(in.Files), nil
	}
	return in.Ops, nil
}

type FileInfo struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Editable bool   `json:"editable"`
	Reason   string `json:"reason,omitempty"` // why it can't be edited here
}

// ListFiles lists every regular file of a training checkout (hidden folders skipped), marking what an edit may touch.
func ListFiles(dir string) ([]FileInfo, error) {
	out := []FileInfo{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != dir {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		f := FileInfo{Path: rel, Size: fi.Size(), Editable: true}
		if err := gitsync.CheckPath(rel); err != nil {
			f.Editable, f.Reason = false, strings.TrimSuffix(err.Error(), ": invalid")
		}
		out = append(out, f)
		return nil
	})
	return out, err
}

// putFiles is the legacy files column: the puts, kept for one release so a Down migration loses no content it had.
func putFiles(ops []gitsync.Op) map[string]string {
	out := map[string]string{}
	for _, op := range ops {
		if op.Op == "put" {
			out[op.Path] = op.Content
		}
	}
	return out
}

// touched lists every path ops name, for the audit row.
func touched(ops []gitsync.Op) []string {
	var out []string
	for _, op := range ops {
		for _, p := range []string{op.Path, op.From, op.To} {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}
```

`apperr.Wrap(kind, msg)` formats as `msg + ": " + kind`, so the `TrimSuffix` above leaves the plain reason.

Then:
- `Edit.Files map[string]string` becomes `Ops []gitsync.Op \`json:"ops,omitempty"\`` (detail only).
- `cols` ends with `merge_sha, created_at, decided_at, ops, diff`.
- `scan` scans `&e.Ops`.
- `List` clears `e.Ops, e.Diff = nil, ""`.
- `checkPath` is deleted; `File` calls `gitsync.CheckPath(rel)`.
- `Files` returns `ListFiles(t.Dir)` with the sha.
- In `Create`, replace the Task 3 map block with:

```go
	ops, err := in.ops()
	if err != nil {
		return nil, err
	}
	changed, probs, err := Check(t, ops)
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		return nil, broken(probs)
	}
```

Then call `repo.PushEdit(gctx, branch, sha, changed, me, "crucible: "+title)`. The insert becomes:

```go
	if _, err := tx.Exec(ctx, `INSERT INTO content_edits (id, training, title, author, base_sha, files, ops, status, branch, head_sha, diff)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10)`, id, in.Training, title, me, sha, putFiles(changed), changed, branch, headSHA, diff); err != nil {
		return nil, err
	}
```

with audit detail `"files": touched(changed)`. `restore` pushes `e.Ops`.

- [ ] **Step 5: The migration**

`internal/db/migrations/00018_edit_ops.sql`:

```sql
-- +goose Up
-- Content edits become ordered operations (put, rename, delete). files keeps the puts for one release, so a Down
-- loses no content it had.
ALTER TABLE content_edits ADD COLUMN ops JSONB NOT NULL DEFAULT '[]';
UPDATE content_edits SET ops = coalesce((SELECT jsonb_agg(jsonb_build_object('op', 'put', 'path', f.key, 'content', f.value) ORDER BY f.key)
  FROM jsonb_each_text(files) f), '[]'::jsonb);
ALTER TABLE content_edits ALTER COLUMN files SET DEFAULT '{}';

-- +goose Down
ALTER TABLE content_edits ALTER COLUMN files DROP DEFAULT;
ALTER TABLE content_edits DROP COLUMN ops;
```

In `internal/db/db_test.go`, after the table loop, assert the column exists:
`pool.QueryRow(ctx, "SELECT count(ops) FROM content_edits")` must not error.

- [ ] **Step 6: Web: show ops on the review page**

`types.ts`: add `EditOp` and `FileEntry` (Interfaces above). In `ContentEdit`, replace `files?: Record<string, string>` with `ops?: EditOp[]`.

`EditReview.tsx`: replace the **Files** section with:

```tsx
      <h2>Files</h2>
      {(e.ops ?? []).map((op, i) =>
        op.op === 'put' ? (
          <details key={i}>
            <summary>{op.path}</summary>
            {op.path.endsWith('.md') ? <Markdown text={op.content} /> : <pre>{op.content}</pre>}
          </details>
        ) : op.op === 'rename' ? <p key={i}>Renamed <code>{op.from}</code> to <code>{op.to}</code></p>
          : <p key={i}>Deleted <code>{op.path}</code> (its full text is in the changes above)</p>,
      )}
```

`EditFiles.tsx`, until Task 10 replaces it:
- Change the `FileList` type to `{ head_sha: string; files: FileEntry[] }`.
- Render only `list.files.filter((f) => f.editable)`.
- In the redo effect, use `(e.ops ?? []).flatMap((op) => (op.op === 'put' ? [op.path] : []))` instead of `Object.keys(e.files ?? {})`.

- [ ] **Step 7: Update docs/user in the same commit**

In `docs/user/authors/review-and-merge.md`, add a "What reviewers see" section. An edit can now rename and delete files as well as change them. Renames show as "rename from / rename to" with any changes. Deleted files show in full. Scripts are executable only inside the lab folder. `training.yaml` can't be renamed or deleted.

- [ ] **Step 8: Run everything**

Run: `gofmt -l cmd internal docs && go vet ./... && go test -race ./internal/gitsync ./internal/edits ./internal/authoring ./internal/db ./internal/docs && cd web && npm test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add -- internal/db/migrations/00018_edit_ops.sql
git commit -m "feat(edits): edits are put/rename/delete ops with a rename-aware diff; files-map requests still accepted" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00018_edit_ops.sql internal/db/db_test.go internal/gitsync internal/edits web/src/types.ts web/src/pages/EditReview.tsx web/src/pages/EditFiles.tsx docs/user/authors/review-and-merge.md
```

---

### Task 8: Drafts: autosave with compare-and-set, submit, discard, returned edits, rebase

**Files:**
- Create: `internal/db/migrations/00019_content_drafts.sql`, `internal/authoring/drafts.go`, `internal/authoring/drafts_test.go`
- Modify: `internal/httpx/httpx.go`, `internal/httpx/httpx_test.go`, `internal/edits/edits.go` (`Version`, `At`), `internal/authoring/authoring.go` (`base`), `internal/authoring/authoring_test.go` (fixture `Version`, `advance`), `internal/authoring/http.go`, `internal/authoring/http_test.go`, `internal/db/db_test.go`, `cmd/crucible-api/main.go`

**Interfaces:**
- Consumes: `edits.Service.Authorize`, `edits.Service.Create`, `edits.Service.Get`, `edits.ListFiles`, `edits.Edit.Ops`, `gitsync.CheckOps`, `gitsync.CheckPath`, `gitsync.NoSymlinks`.
- Produces:
  - `edits.Service.Version func(ctx context.Context, id, sha string) *content.Training` (wired to `syncer.Version`)
  - `func (s *edits.Service) At(ctx, u, training, sha string) (*content.Training, string /*head*/, error)`
  - `authoring.Draft{ID int64; Training, Title, BaseSHA, HeadSHA string; Ops []gitsync.Op; UpdatedAt time.Time; EditID *int64; State string}` with JSON `id,training,title,base_sha,head_sha,ops,updated_at,edit_id,state`
  - `authoring.NewDraft{Training, Title string; FromEdit int64}` with JSON `training,title,from_edit`
  - `authoring.SaveDraft{Title, BaseSHA string; Ops []gitsync.Op; UpdatedAt time.Time}` with JSON `title,base_sha,ops,updated_at`
  - `authoring.Conflict{Path, Base, Head, Mine string; HeadMissing bool; Op gitsync.Op}` with JSON `path,base,head,mine,head_missing,op`
  - `authoring.RebaseResult{Draft *Draft; Conflicts []Conflict}`
  - Methods `List`, `Create`, `Get`, `Save`, `Discard`, `Submit`, `Rebase`, `Files`, `File` on `*authoring.Service`
  - Routes: `GET/POST /api/authoring/drafts`; `GET/PUT/DELETE /api/authoring/drafts/{id}`; `POST /api/authoring/drafts/{id}/submit`; `POST /api/authoring/drafts/{id}/rebase`; `GET /api/authoring/drafts/{id}/files`; `GET /api/authoring/drafts/{id}/file?path=`

- [ ] **Step 1: Review Focus 1. A body over 1 MiB is reported, not truncated**

Add to `internal/httpx/httpx_test.go`:

```go
func TestReadRefusesOversizeBodies(t *testing.T) {
	var v map[string]string
	big := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 1<<20)+`"}`))
	if err := Read(big, &v); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "over 1 MiB") {
		t.Fatalf("an oversize body says so (was: unexpected EOF): %v", err)
	}
	ok := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 1<<19)+`"}`))
	if err := Read(ok, &v); err != nil || len(v["a"]) != 1<<19 {
		t.Fatalf("half a MiB is fine: %v", err)
	}
}
```

Run: `go test ./internal/httpx -run Oversize`
Expected: FAIL ("unexpected EOF").

Then in `httpx.go`:

```go
const maxBody = 1 << 20

// Read decodes a JSON body of at most 1 MiB and rejects unknown fields. A larger body is refused as such, never
// truncated into a confusing parse error.
func Read(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	if len(b) > maxBody {
		return apperr.Wrap(apperr.Invalid, "the request is over 1 MiB; make it smaller")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.Wrap(apperr.Invalid, err.Error())
	}
	return nil
}
```

Run: `go test ./internal/httpx`. Expected: PASS.

- [ ] **Step 2: The migration**

`internal/db/migrations/00019_content_drafts.sql`:

```sql
-- +goose Up
-- Work in progress in the content editor: autosaved ops on a base commit. "Submit for review" turns one into an edit.
CREATE TABLE content_drafts (
  id                BIGSERIAL PRIMARY KEY,
  author            TEXT NOT NULL,                  -- email, lowercased
  training          TEXT NOT NULL,
  title             TEXT NOT NULL DEFAULT '',
  base_sha          TEXT NOT NULL,                  -- the tracked-branch head when the draft started or was last rebased
  ops               JSONB NOT NULL DEFAULT '[]',
  submitted_edit_id BIGINT REFERENCES content_edits (id) ON DELETE SET NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now() -- autosave's compare-and-set token
);
CREATE INDEX content_drafts_author ON content_drafts (author, training);

-- +goose Down
DROP TABLE content_drafts;
```

Add `"content_drafts"` to the table list in `internal/db/db_test.go`.

- [ ] **Step 3: `edits.Service.At` and `Version`**

Add the field `Version func(ctx context.Context, id, sha string) *content.Training // older versions (drafts' bases); nil: head only` to `edits.Service`, and:

```go
// At is training at sha for someone allowed to edit it: the head, or an older commit a draft started on. The caller
// vouches for sha (the head or a draft's stored base), never a value straight from a request: the mirror also holds
// unmerged edit branches. Permission is decided at the head.
func (s *Service) At(ctx context.Context, u *auth.User, training, sha string) (*content.Training, string, error) {
	_, t, head, err := s.training(u, training)
	if err != nil || sha == head {
		return t, head, err
	}
	if s.Version != nil {
		if old := s.Version(ctx, training, sha); old != nil {
			return old, head, nil
		}
	}
	return nil, head, apperr.Wrap(apperr.Conflict, "the version this draft started on is no longer available; rebase it")
}
```

In `cmd/crucible-api/main.go`, set `Version: syncer.Version` in `editsSvc`.

- [ ] **Step 4: Extend `authoring.base` to drafts' bases**

```go
// base is the training at sha for u: the head, or the base of one of u's own drafts of that training. A base from the
// client is never trusted on its own: the bot's mirror also holds unmerged edit branches.
func (s *Service) base(ctx context.Context, u *auth.User, training, sha string) (*content.Training, error) {
	_, head, err := s.Edits.Authorize(u, training)
	if err != nil {
		return nil, err
	}
	if sha != head {
		var mine bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM content_drafts WHERE author = $1 AND training = $2 AND base_sha = $3)`,
			strings.ToLower(u.Email), training, sha).Scan(&mine); err != nil {
			return nil, err
		}
		if !mine {
			return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it")
		}
	}
	t, _, err := s.Edits.At(ctx, u, training, sha)
	return t, err
}
```

In the `authoring_test.go` fixture, set `es.Version = func(_ context.Context, id, sha string) *content.Training { return st.Training(id, sha) }` after `es` is built, and add:

```go
// advance pushes a commit to t1's main from another clone (someone working in git) and makes it the head. The old
// version keeps its own directory, as the real syncer's exports do.
func (f *fx) advance(t *testing.T, put map[string]string, del ...string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "other")
	git(t, "", "clone", "-q", f.remote, work)
	for rel, body := range put {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range del {
		if err := os.Remove(filepath.Join(work, rel)); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "upstream")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	head := git(t, work, "rev-parse", "HEAD")
	tr, probs := content.Load(work)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	f.st.Trainings["t1@"+head] = tr
	f.st.Heads["t1"] = head
	return head
}
```

- [ ] **Step 5: Write the failing drafts tests**

`internal/authoring/drafts_test.go`:

```go
package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

var moveIntro = []gitsync.Op{
	{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
	{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: reading/start.md\n  - quiz: quiz.yaml\n"},
}

func TestDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Move the intro"})
	if err != nil || d.State != "editing" || d.BaseSHA != f.head() || d.HeadSHA != f.head() {
		t.Fatalf("create: %+v %v", d, err)
	}
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: d.Title, BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	e, err := f.s.Submit(ctx, f.leader, d.ID)
	if err != nil || e.Status != "pending" {
		t.Fatalf("submit: %+v %v", e, err)
	}
	d, _ = f.s.Get(ctx, f.leader, d.ID)
	if d.State != "in_review" || d.EditID == nil || *d.EditID != e.ID {
		t.Fatalf("in review: %+v", d)
	}
	if _, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: d.Title, BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("no saves while in review: %v", err)
	}
	if _, err := f.edits.Reject(ctx, f.senior, e.ID, "Keep intro.md"); err != nil {
		t.Fatal(err)
	}
	d, _ = f.s.Get(ctx, f.leader, d.ID)
	if d.State != "returned" {
		t.Fatalf("a rejected edit returns its draft: %+v", d)
	}
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Move the intro, take 2", BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt})
	if err != nil || d.State != "editing" || d.EditID != nil {
		t.Fatalf("saving a returned draft unlinks it: %+v %v", d, err)
	}
	e, err = f.s.Submit(ctx, f.leader, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.edits.Approve(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	if list, err := f.s.List(ctx, f.leader); err != nil || len(list) != 0 {
		t.Fatalf("a merged draft is done: %+v %v", list, err)
	}
	other, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Scrap"})
	if err := f.s.Discard(ctx, f.leader, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Get(ctx, f.leader, other.ID); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("discarded: %v", err)
	}
	var n int
	if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('content_draft.submit', 'content_draft.discard')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("submit ×2 and discard are audited, autosaves are not: %d %v", n, err)
	}
}

// Review Focus 2: two tabs on one draft. The stale tab can't overwrite the other's work.
func TestDraftCompareAndSet(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Warmer"})
	tabA, tabB := d.UpdatedAt, d.UpdatedAt
	put := []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nA.\n"}}
	a, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: d.BaseSHA, Ops: put, UpdatedAt: tabA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: d.BaseSHA, UpdatedAt: tabB}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "another tab") {
		t.Fatalf("the stale tab must not overwrite: %v", err)
	}
	got, _ := f.s.Get(ctx, f.leader, d.ID)
	if len(got.Ops) != 1 || !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("tab A's save stands: %+v", got)
	}
}

func TestDraftLimitsAndAccess(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.Create(ctx, f.trainee, NewDraft{Training: "t1"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("enrolled users get no drafts: %v", err)
	}
	var ids []int64
	for range maxDrafts {
		d, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	if _, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "open drafts") {
		t.Fatalf("draft limit: %v", err)
	}
	if _, err := f.s.Get(ctx, f.leader, ids[0]); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("someone else's draft: %v", err)
	}
	if _, err := f.s.Save(ctx, f.senior, ids[0], SaveDraft{BaseSHA: strings.Repeat("b", 40), UpdatedAt: time.Now()}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a base that is neither the draft's nor the head: %v", err)
	}
	// The senior becomes enrolled: their drafts, files and checks close at once.
	f.st.Platform.Teams["forge"].Programs["t1"] = &config.Program{Training: "t1", Enrolled: []string{"trainee@crucible.local", "senior@crucible.local"}}
	for name, err := range map[string]error{
		"get":      second(f.s.Get(ctx, f.senior, ids[0])),
		"files":    second(f.s.Files(ctx, f.senior, ids[0])),
		"file":     second(f.s.File(ctx, f.senior, ids[0], "modules/m1/quiz.yaml")),
		"validate": second(f.s.Validate(ctx, f.senior, ValidateReq{Training: "t1", BaseSHA: f.head()})),
		"rebase":   second(f.s.Rebase(ctx, f.senior, ids[0])),
		"submit":   second(f.s.Submit(ctx, f.senior, ids[0])),
	} {
		if !errors.Is(err, apperr.Forbidden) {
			t.Errorf("%s while enrolled: %v", name, err)
		}
	}
	if list, _ := f.s.List(ctx, f.senior); len(list) != 0 {
		t.Fatalf("drafts of a training I'm enrolled in are hidden: %+v", list)
	}
}

func second[T any](_ T, err error) error { return err }

func TestDraftFromAReturnedEdit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.edits.Create(ctx, f.leader, edits.NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Move", Ops: moveIntro})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", FromEdit: e.ID}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a pending edit can't be reopened: %v", err)
	}
	if _, err := f.edits.Reject(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", FromEdit: e.ID})
	if err != nil || d.Title != "Move" || d.BaseSHA != e.BaseSHA || len(d.Ops) != 2 {
		t.Fatalf("reopened: %+v %v", d, err)
	}
}

func TestRebaseFollowsUntouchedFiles(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Intro"})
	d, _ = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: d.BaseSHA, UpdatedAt: d.UpdatedAt,
		Ops: []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nWarmer.\n"}}})
	old := d.BaseSHA
	head := f.advance(t, map[string]string{"modules/m1/quiz.yaml": seed["modules/m1/quiz.yaml"] + "pass_threshold: 1\n"})
	if probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: old, Ops: d.Ops}); err != nil || len(probs) != 0 {
		t.Fatalf("validate works on my draft's older base: %v %v", probs, err)
	}
	r, err := f.s.Rebase(ctx, f.leader, d.ID)
	if err != nil || len(r.Conflicts) != 0 || r.Draft.BaseSHA != head {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	if _, err := f.s.Submit(ctx, f.leader, d.ID); err != nil {
		t.Fatalf("a rebased draft submits: %v", err)
	}
}

// Review Focus 5: upstream deleted a file the draft changes. That's a conflict for the author, never a 500 or a loss.
func TestRebaseConflictWhenUpstreamDeleted(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Intro"})
	mine := "# Intro\n\nMine.\n"
	d, _ = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: d.BaseSHA, UpdatedAt: d.UpdatedAt,
		Ops: []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: mine}}})
	head := f.advance(t, map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - quiz: quiz.yaml\n"}, "modules/m1/reading/intro.md")
	r, err := f.s.Rebase(ctx, f.leader, d.ID)
	if err != nil || len(r.Conflicts) != 1 {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	c := r.Conflicts[0]
	if c.Path != "modules/m1/reading/intro.md" || !c.HeadMissing || c.Mine != mine || c.Base != seed["modules/m1/reading/intro.md"] {
		t.Fatalf("conflict: %+v", c)
	}
	if got, _ := f.s.Get(ctx, f.leader, d.ID); got.BaseSHA == head {
		t.Fatal("nothing moves while there are conflicts")
	}
	// The author drops their change: saved on the new head.
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: head, UpdatedAt: r.Draft.UpdatedAt})
	if err != nil || d.BaseSHA != head {
		t.Fatalf("resolved: %+v %v", d, err)
	}
}
```

Add `"time"` to the imports for `time.Now()`.

Add routes to the `http_test.go` table, with a draft created first via `f.s.Create`:
- `GET /api/authoring/drafts` → 200 for leader, 200 (empty) for trainee.
- `POST /api/authoring/drafts` `{"training":"t1"}` → 200 for leader, 403 for trainee.
- `GET /api/authoring/drafts/{id}` → 200 for its author, 404 for senior, 404 for `abc`.
- `GET /api/authoring/drafts/{id}/files` → 200. `GET .../file?path=../x.md` → 400.
- `PUT /api/authoring/drafts/{id}` with a 1.1 MiB body → 400, and the error mentions "over 1 MiB" (Review Focus 1).
- `POST /api/authoring/drafts/{id}/rebase` `{}` → 200.
- `DELETE /api/authoring/drafts/{id}` → 204.

Run: `go test ./internal/authoring`
Expected: build failure (`undefined: NewDraft`, …).

- [ ] **Step 6: Implement `internal/authoring/drafts.go`**

```go
package authoring

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/content"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

const (
	maxDrafts = 5   // open drafts per author
	maxTitle  = 200 // same as an edit's
	draftCols = `d.id, d.training, d.title, d.base_sha, d.ops, d.updated_at, d.submitted_edit_id, coalesce(e.status, '')`
	draftFrom = `content_drafts d LEFT JOIN content_edits e ON e.id = d.submitted_edit_id`
	// bump keeps updated_at strictly increasing, so compare-and-set never sees two saves with one stamp.
	bump = `GREATEST(now(), updated_at + interval '1 microsecond')`
)

type Draft struct {
	ID        int64        `json:"id"`
	Training  string       `json:"training"`
	Title     string       `json:"title"`
	BaseSHA   string       `json:"base_sha"`
	HeadSHA   string       `json:"head_sha"` // the tracked branch now: differs from BaseSHA when a rebase is due
	Ops       []gitsync.Op `json:"ops"`
	UpdatedAt time.Time    `json:"updated_at"`
	EditID    *int64       `json:"edit_id,omitempty"`
	State     string       `json:"state"` // editing | in_review | returned | merged
}

type NewDraft struct {
	Training string `json:"training"`
	Title    string `json:"title"`
	FromEdit int64  `json:"from_edit,omitempty"` // reopen one of my stale, rejected or withdrawn edits, at its base
}

type SaveDraft struct {
	Title     string       `json:"title"`
	BaseSHA   string       `json:"base_sha"`
	Ops       []gitsync.Op `json:"ops"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type Conflict struct {
	Path        string     `json:"path"`
	Base        string     `json:"base"`         // the file when the draft started ("" if it didn't exist)
	Head        string     `json:"head"`         // the file now ("" if gone)
	HeadMissing bool       `json:"head_missing"` // deleted or moved away upstream
	Mine        string     `json:"mine"`         // the draft's text for a put
	Op          gitsync.Op `json:"op"`           // the draft's op that touches Path
}

type RebaseResult struct {
	Draft     *Draft     `json:"draft"`
	Conflicts []Conflict `json:"conflicts"`
}

func clean(s string) string { return strings.TrimSpace(strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")) }

func stateOf(editStatus string) string {
	switch editStatus {
	case "":
		return "editing"
	case "pending":
		return "in_review"
	case "merged":
		return "merged"
	}
	return "returned" // rejected, withdrawn, stale
}

func scanDraft(row pgx.Row) (*Draft, error) {
	var d Draft
	var status string
	err := row.Scan(&d.ID, &d.Training, &d.Title, &d.BaseSHA, &d.Ops, &d.UpdatedAt, &d.EditID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "draft not found")
	}
	d.State = stateOf(status)
	return &d, err
}

// List is my drafts, newest first, for trainings I may still edit. Drafts whose edit merged are done: deleted here.
func (s *Service) List(ctx context.Context, u *auth.User) ([]Draft, error) {
	me := strings.ToLower(u.Email)
	if _, err := s.DB.Exec(ctx, `DELETE FROM content_drafts d USING content_edits e
		WHERE d.submitted_edit_id = e.id AND e.status = 'merged' AND d.author = $1`, me); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+draftCols+` FROM `+draftFrom+` WHERE d.author = $1 ORDER BY d.updated_at DESC`, me)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		if _, head, err := s.Edits.Authorize(u, d.Training); err == nil {
			d.HeadSHA, d.Ops = head, nil
			out = append(out, *d)
		}
	}
	return out, rows.Err()
}

// Get is one of my drafts, while I may still edit its training.
func (s *Service) Get(ctx context.Context, u *auth.User, id int64) (*Draft, error) {
	d, err := scanDraft(s.DB.QueryRow(ctx, `SELECT `+draftCols+` FROM `+draftFrom+` WHERE d.id = $1 AND d.author = $2`, id, strings.ToLower(u.Email)))
	if err != nil {
		return nil, err
	}
	_, head, err := s.Edits.Authorize(u, d.Training)
	if err != nil {
		return nil, err
	}
	d.HeadSHA = head
	return d, nil
}

func (s *Service) Create(ctx context.Context, u *auth.User, in NewDraft) (*Draft, error) {
	_, head, err := s.Edits.Authorize(u, in.Training)
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	title, base, ops := clean(in.Title), head, []gitsync.Op{}
	if in.FromEdit != 0 {
		e, err := s.Edits.Get(ctx, u, in.FromEdit)
		if err != nil {
			return nil, err
		}
		if e.Author != me || e.Training != in.Training || e.Status == "pending" || e.Status == "merged" {
			return nil, apperr.Wrap(apperr.Invalid, "only your own stale, rejected or withdrawn edits of this training can be reopened")
		}
		title, base, ops = e.Title, e.BaseSHA, e.Ops
	}
	if len(title) > maxTitle {
		return nil, apperr.Wrap(apperr.Invalid, "give the draft a title of at most 200 characters")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('content_drafts:' || $1))`, me); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+draftFrom+` WHERE d.author = $1 AND coalesce(e.status, '') <> 'merged'`, me).Scan(&n); err != nil {
		return nil, err
	}
	if n >= maxDrafts {
		return nil, apperr.Wrap(apperr.Conflict, fmt.Sprintf("you have %d open drafts; submit or discard one first", n))
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO content_drafts (author, training, title, base_sha, ops) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		me, in.Training, title, base, ops).Scan(&id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, u, id)
}

// Save is autosave: it replaces the draft's title, base and ops if updated_at still matches (another tab didn't save
// in between). The base may stay or move to the head (after a rebase), nothing else. Saving a returned draft unlinks it.
func (s *Service) Save(ctx context.Context, u *auth.User, id int64, in SaveDraft) (*Draft, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.State == "in_review":
		return nil, apperr.Wrap(apperr.Conflict, "this draft is in review; withdraw the edit to keep working on it")
	case d.State == "merged":
		return nil, apperr.Wrap(apperr.Conflict, "this draft was merged")
	case in.BaseSHA != d.BaseSHA && in.BaseSHA != d.HeadSHA:
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it")
	}
	title := clean(in.Title)
	if len(title) > maxTitle {
		return nil, apperr.Wrap(apperr.Invalid, "give the draft a title of at most 200 characters")
	}
	if in.Ops == nil {
		in.Ops = []gitsync.Op{}
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE content_drafts SET title = $3, base_sha = $4, ops = $5, updated_at = `+bump+`, submitted_edit_id = NULL
		WHERE id = $1 AND author = $2 AND updated_at = $6`, id, strings.ToLower(u.Email), title, in.BaseSHA, in.Ops, in.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed in another tab or window; reload it")
	}
	return s.Get(ctx, u, id)
}

func (s *Service) Discard(ctx context.Context, u *auth.User, id int64) error {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return err
	}
	me := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `DELETE FROM content_drafts WHERE id = $1 AND author = $2`, id, me); err != nil {
		return err
	}
	if err := audit.Log(ctx, tx, me, "content_draft.discard", d.Training, map[string]any{"draft": id, "title": d.Title}, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Submit turns the draft into an edit through the normal path (validation, its own branch, review) and links them.
func (s *Service) Submit(ctx context.Context, u *auth.User, id int64) (*edits.Edit, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	switch {
	case d.State == "in_review" || d.State == "merged":
		return nil, apperr.Wrap(apperr.Conflict, "this draft was already submitted")
	case d.BaseSHA != d.HeadSHA:
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since this draft started; rebase it, then submit")
	case len(d.Ops) == 0:
		return nil, apperr.Wrap(apperr.Invalid, "nothing changed")
	}
	e, err := s.Edits.Create(ctx, u, edits.NewEdit{Training: d.Training, BaseSHA: d.BaseSHA, Title: d.Title, Ops: d.Ops})
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `UPDATE content_drafts SET submitted_edit_id = $2, updated_at = `+bump+` WHERE id = $1`, id, e.ID); err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, me, "content_draft.submit", d.Training, map[string]any{"draft": id, "edit": e.ID}, e.HeadSHA); err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

// Rebase moves a draft to the training's current head. Files the draft doesn't touch simply follow. A file the draft
// changes (put, rename source or target, delete) that also changed upstream is a conflict: nothing is written, and the
// editor saves the author's resolution with base_sha = head.
func (s *Service) Rebase(ctx context.Context, u *auth.User, id int64) (*RebaseResult, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if d.BaseSHA == d.HeadSHA {
		return &RebaseResult{Draft: d, Conflicts: []Conflict{}}, nil
	}
	if d.State == "in_review" {
		return nil, apperr.Wrap(apperr.Conflict, "this draft is in review; withdraw the edit to keep working on it")
	}
	old, _, err := s.Edits.At(ctx, u, d.Training, d.BaseSHA)
	if err != nil {
		return nil, err
	}
	now, _, err := s.Edits.At(ctx, u, d.Training, d.HeadSHA)
	if err != nil {
		return nil, err
	}
	read := func(t *content.Training, rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel))) // rel passed CheckOps when it was saved
		return string(b), err == nil
	}
	conflicts := []Conflict{}
	for _, op := range d.Ops {
		p := op.Path
		if op.Op == "rename" {
			p = op.From
			if body, exists := read(now, op.To); exists { // the target appeared upstream
				conflicts = append(conflicts, Conflict{Path: op.To, Head: body, Op: op})
			}
		}
		was, had := read(old, p)
		is, has := read(now, p)
		if had == has && was == is {
			continue
		}
		c := Conflict{Path: p, Base: was, Head: is, HeadMissing: !has, Op: op}
		if op.Op == "put" {
			c.Mine = op.Content
		}
		conflicts = append(conflicts, c)
	}
	if len(conflicts) > 0 {
		return &RebaseResult{Draft: d, Conflicts: conflicts}, nil
	}
	tag, err := s.DB.Exec(ctx, `UPDATE content_drafts SET base_sha = $3, updated_at = `+bump+` WHERE id = $1 AND author = $2 AND updated_at = $4`,
		id, strings.ToLower(u.Email), d.HeadSHA, d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this draft changed in another tab or window; reload it")
	}
	d, err = s.Get(ctx, u, id)
	return &RebaseResult{Draft: d, Conflicts: []Conflict{}}, err
}

// Files lists the training at the draft's base, marking what may be edited.
func (s *Service) Files(ctx context.Context, u *auth.User, id int64) ([]edits.FileInfo, error) {
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return nil, err
	}
	t, _, err := s.Edits.At(ctx, u, d.Training, d.BaseSHA)
	if err != nil {
		return nil, err
	}
	return edits.ListFiles(t.Dir)
}

// File is one editable file at the draft's base.
func (s *Service) File(ctx context.Context, u *auth.User, id int64, rel string) (string, error) {
	if err := gitsync.CheckPath(rel); err != nil {
		return "", err
	}
	d, err := s.Get(ctx, u, id)
	if err != nil {
		return "", err
	}
	t, _, err := s.Edits.At(ctx, u, d.Training, d.BaseSHA)
	if err != nil {
		return "", err
	}
	if err := gitsync.NoSymlinks(t.Dir, rel); err != nil {
		return "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	b, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	return string(b), nil
}
```

`Files` and `File` call `Get`, which runs `Authorize`, before checking the path. The `"file"` row in `TestDraftLimitsAndAccess` uses a valid path, so it reaches `Forbidden`.

Routes, added to `http.go` (also import `strconv`; the `id` helper mirrors `edits/http.go`):

```go
	id := func(r *http.Request) (int64, error) {
		n, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			return 0, apperr.Wrap(apperr.NotFound, "draft not found")
		}
		return n, nil
	}
	withID := func(fn func(w http.ResponseWriter, r *http.Request, n int64)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			n, err := id(r)
			if err != nil {
				httpx.Error(w, err)
				return
			}
			fn(w, r, n)
		}
	}
	r.Get("/api/authoring/drafts", func(w http.ResponseWriter, r *http.Request) { v, err := s.List(r.Context(), user(r)); reply(w, v, err) })
	r.Post("/api/authoring/drafts", func(w http.ResponseWriter, r *http.Request) {
		var in NewDraft
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Create(r.Context(), user(r), in)
		reply(w, v, err)
	})
	r.Get("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) { v, err := s.Get(r.Context(), user(r), n); reply(w, v, err) }))
	r.Put("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		var in SaveDraft
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Save(r.Context(), user(r), n, in)
		reply(w, v, err)
	}))
	r.Delete("/api/authoring/drafts/{id}", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		if err := s.Discard(r.Context(), user(r), n); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r.Post("/api/authoring/drafts/{id}/submit", withID(func(w http.ResponseWriter, r *http.Request, n int64) { v, err := s.Submit(r.Context(), user(r), n); reply(w, v, err) }))
	r.Post("/api/authoring/drafts/{id}/rebase", withID(func(w http.ResponseWriter, r *http.Request, n int64) { v, err := s.Rebase(r.Context(), user(r), n); reply(w, v, err) }))
	r.Get("/api/authoring/drafts/{id}/files", withID(func(w http.ResponseWriter, r *http.Request, n int64) { v, err := s.Files(r.Context(), user(r), n); reply(w, v, err) }))
	r.Get("/api/authoring/drafts/{id}/file", withID(func(w http.ResponseWriter, r *http.Request, n int64) {
		p := r.URL.Query().Get("path")
		body, err := s.File(r.Context(), user(r), n, p)
		reply(w, map[string]string{"path": p, "content": body}, err)
	}))
```

- [ ] **Step 7: Run everything**

Run: `gofmt -l cmd internal docs && go vet ./... && go test -race ./internal/authoring ./internal/edits ./internal/httpx ./internal/db ./internal/httpapi ./cmd/...`
Expected: PASS.

- [ ] **Step 8: Update docs/user in the same commit**

No user-visible change yet: the editor starts using drafts in Task 10, and that task rewrites the authoring pages. Confirm that `go test ./internal/docs` still passes.

- [ ] **Step 9: Commit**

```bash
git add -- internal/db/migrations/00019_content_drafts.sql internal/authoring/drafts.go internal/authoring/drafts_test.go
git commit -m "feat(authoring): server-side drafts with compare-and-set autosave, submit, discard, returned edits and rebase" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/db/migrations/00019_content_drafts.sql internal/db/db_test.go internal/authoring internal/edits/edits.go internal/httpx cmd/crucible-api/main.go
```

---
## Delivery step 4: the editor

### Task 9: Editor state as pure functions: draft ops, diffs, autosave states, client limits

**Files:**
- Create: `web/src/pages/editor/model.ts`, `web/src/pages/editor/model.test.ts`, `web/src/pages/editor/diff.ts`, `web/src/pages/editor/diff.test.ts`, `web/src/pages/editor/autosave.ts`, `web/src/pages/editor/autosave.test.ts`, `web/src/lib/editLimits.test.ts`
- Modify: `web/src/lib/editLimits.ts`, `web/src/types.ts`, `web/package.json` (+`diff`)

**Interfaces:**
- Consumes: `EditOp`, `FileEntry` (Task 7); `byteLen` (`lib/editDraft.ts`); `ApiError` (`api.ts`).
- Produces (TS):
  - `type DraftOps = { renames: Record<string, string>; deletes: string[]; puts: Record<string, string> }`, `emptyOps()`
  - `fromOps`, `toOps`, `origin`, `currentPaths`, `putText`, `renamePath`, `deletePath`, `changeList`, `matchFiles`, `languageOf`, `monacoTheme`
  - `type Change = { kind: 'added' | 'changed' | 'renamed' | 'deleted'; path: string; from?: string }`
  - `unifiedDiff(from: string | undefined, to: string | undefined, before: string, after: string): string`
  - `type SaveState`, `afterSave(err, now)`, `canAutosave(s)`, `saveLabel(s, now)`
  - `opsProblem(ops: EditOp[]): string | undefined`, `pathProblem(p: string): string | undefined`, `MAX_DRAFT_BYTES`
  - `types.ts`: `DraftState`, `DraftInfo`, `Problem`

- [ ] **Step 1: Install `diff` and add the types**

Run: `cd web && npm install diff@9.0.0`

`types.ts`:

```ts
export type DraftState = 'editing' | 'in_review' | 'returned' | 'merged'
export type DraftInfo = { id: number; training: string; title: string; base_sha: string; head_sha: string; ops: EditOp[]; updated_at: string; edit_id?: number; state: DraftState }
export type Problem = { file: string; line: number; msg: string }
```

- [ ] **Step 2: Write the failing tests**

`web/src/pages/editor/model.test.ts`:

```ts
import { expect, test } from 'vitest'
import { changeList, currentPaths, deletePath, emptyOps, fromOps, languageOf, matchFiles, monacoTheme, origin, putText, renamePath, toOps } from './model'

const base = ['training.yaml', 'modules/m1/module.yaml', 'modules/m1/reading/a.md', 'modules/m1/reading/b.md']

test('ops round-trip in the order the server applies them', () => {
  const ops = [
    { op: 'put' as const, path: 'modules/m1/reading/c.md', content: 'C' },
    { op: 'delete' as const, path: 'modules/m1/reading/b.md' },
    { op: 'rename' as const, from: 'modules/m1/reading/a.md', to: 'modules/m1/reading/z.md' },
  ]
  expect(toOps(fromOps(ops)).map((o) => o.op)).toEqual(['rename', 'delete', 'put'])
  expect(currentPaths(base, fromOps(ops))).toEqual(['modules/m1/module.yaml', 'modules/m1/reading/c.md', 'modules/m1/reading/z.md', 'training.yaml'])
})

test('a file edited back to its original leaves the draft', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  expect(d.puts).toEqual({ 'modules/m1/reading/a.md': 'A2' })
  d = putText(d, 'modules/m1/reading/a.md', 'A', 'A')
  expect(toOps(d)).toEqual([])
})

test('rename keeps unsaved text, chains collapse, renaming back undoes it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/a.md', 'A2', 'A')
  d = renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = renamePath(base, d, 'modules/m1/reading/x.md', 'modules/m2/reading/y.md')
  expect(d.renames).toEqual({ 'modules/m2/reading/y.md': 'modules/m1/reading/a.md' })
  expect(d.puts).toEqual({ 'modules/m2/reading/y.md': 'A2' })
  expect(origin(base, d, 'modules/m2/reading/y.md')).toBe('modules/m1/reading/a.md')
  expect(origin(base, d, 'modules/m1/reading/a.md')).toBeUndefined()
  d = renamePath(base, d, 'modules/m2/reading/y.md', 'modules/m1/reading/a.md')
  expect(d.renames).toEqual({})
  expect(() => renamePath(base, d, 'modules/m1/reading/a.md', 'modules/m1/reading/b.md')).toThrow(/already exists/)
})

test('renaming a new file moves its put; deleting it forgets it', () => {
  let d = putText(emptyOps(), 'modules/m1/reading/n.md', 'N', undefined)
  d = renamePath(base, d, 'modules/m1/reading/n.md', 'modules/m1/reading/m.md')
  expect(toOps(d)).toEqual([{ op: 'put', path: 'modules/m1/reading/m.md', content: 'N' }])
  expect(toOps(deletePath(base, d, 'modules/m1/reading/m.md'))).toEqual([])
})

test('deleting a renamed file deletes its original; a deleted name is not reused by a rename', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/x.md')
  expect(toOps(d)).toEqual([{ op: 'delete', path: 'modules/m1/reading/a.md' }])
  expect(() => renamePath(base, d, 'modules/m1/reading/b.md', 'modules/m1/reading/a.md')).toThrow(/deleted/)
})

test('changes list what a reviewer will see', () => {
  let d = renamePath(base, emptyOps(), 'modules/m1/reading/a.md', 'modules/m1/reading/x.md')
  d = deletePath(base, d, 'modules/m1/reading/b.md')
  d = putText(d, 'modules/m1/module.yaml', 'title: M\n', 'title: M1\n')
  d = putText(d, 'modules/m1/reading/new.md', '# New\n', undefined)
  expect(changeList(base, d)).toEqual([
    { kind: 'changed', path: 'modules/m1/module.yaml' },
    { kind: 'deleted', path: 'modules/m1/reading/b.md' },
    { kind: 'added', path: 'modules/m1/reading/new.md' },
    { kind: 'renamed', path: 'modules/m1/reading/x.md', from: 'modules/m1/reading/a.md' },
  ])
})

test('go to file matches in order, best name first', () => {
  expect(matchFiles(base, 'rdb')).toEqual(['modules/m1/reading/b.md'])
  expect(matchFiles(base, 'module')[0]).toBe('modules/m1/module.yaml')
  expect(matchFiles(base, '')).toEqual(base)
})

test('languages and themes', () => {
  expect([languageOf('a.md'), languageOf('a.sh'), languageOf('a.yml'), languageOf('a.yaml')]).toEqual(['markdown', 'shell', 'yaml', 'yaml'])
  expect([monacoTheme('forge'), monacoTheme('quench'), monacoTheme('contrast'), monacoTheme('anvil')]).toEqual(['vs-dark', 'vs-dark', 'hc-black', 'vs'])
})
```

`web/src/pages/editor/diff.test.ts`:

```ts
import { expect, test } from 'vitest'
import { unifiedDiff } from './diff'

test('a change reads like git', () => {
  const d = unifiedDiff('modules/m1/a.md', 'modules/m1/a.md', '# A\nold\n', '# A\nnew\n')
  expect(d).toContain('diff --git a/modules/m1/a.md b/modules/m1/a.md')
  expect(d).toContain('--- a/modules/m1/a.md')
  expect(d).toContain('+++ b/modules/m1/a.md')
  expect(d).toContain('-old')
  expect(d).toContain('+new')
  expect(d).not.toContain('====')
})

test('renames, additions and deletions', () => {
  expect(unifiedDiff('m/a.md', 'm/b.md', 'x\n', 'x\n')).toContain('rename from m/a.md\nrename to m/b.md')
  expect(unifiedDiff(undefined, 'm/n.md', '', 'hi\n')).toContain('--- /dev/null')
  expect(unifiedDiff('m/o.md', undefined, 'bye\n', '')).toContain('-bye')
})
```

`web/src/pages/editor/autosave.test.ts`:

```ts
import { expect, test } from 'vitest'
import { ApiError } from '../../api'
import { afterSave, canAutosave, saveLabel } from './autosave'

// Review Focus 2: when another tab saved, this tab stops autosaving and says why. No silent overwrite, no retry loop.
test('409 stops autosave; 400 blocks until fixed; other failures retry on the next change', () => {
  const conflict = afterSave(new ApiError(409, 'this draft changed in another tab or window; reload it'), 0)
  expect(conflict.kind).toBe('conflict')
  expect(canAutosave(conflict)).toBe(false)
  expect(saveLabel(conflict, 0)).toContain('another tab')
  expect(afterSave(new ApiError(400, 'the request is over 1 MiB; make it smaller'), 0).kind).toBe('blocked')
  expect(canAutosave(afterSave(new ApiError(503, 'unavailable'), 0))).toBe(true)
})

test('the saved label ages', () => {
  expect(saveLabel({ kind: 'saved', at: 0 }, 2_000)).toBe('Draft saved just now')
  expect(saveLabel({ kind: 'saved', at: 0 }, 30_000)).toBe('Draft saved 30s ago')
  expect(saveLabel({ kind: 'saved', at: 0 }, 180_000)).toBe('Draft saved 3m ago')
})
```

`web/src/lib/editLimits.test.ts`:

```ts
import { expect, test } from 'vitest'
import { opsProblem, pathProblem } from './editLimits'

test('paths follow the server rules', () => {
  expect(pathProblem('modules/m1/reading/a.md')).toBeUndefined()
  expect(pathProblem('training.yaml')).toBeUndefined()
  for (const p of ['README.md', 'modules/a.md', 'modules/m1/.hidden.md', 'modules/m1/x .md', 'modules/m1/ä.md', 'modules/m1/../x.md', 'modules/m1/main.tf'])
    expect(pathProblem(p), p).toBeTruthy()
})

// Review Focus 1: a draft over the 1 MiB request cap is caught before autosave sends it.
test('a draft over 1 MiB is refused with a reason', () => {
  const big = Array.from({ length: 5 }, (_, i) => ({ op: 'put' as const, path: `modules/m1/reading/${i}.md`, content: 'x'.repeat(250_000) }))
  expect(opsProblem(big)).toMatch(/over 1 MiB/)
  expect(opsProblem(big.slice(0, 3))).toBeUndefined()
})

test('op rules', () => {
  expect(opsProblem([{ op: 'delete', path: 'training.yaml' }])).toMatch(/training.yaml/)
  expect(opsProblem([{ op: 'rename', from: 'training.yaml', to: 'modules/m1/t.yaml' }])).toMatch(/training.yaml/)
  expect(opsProblem(Array.from({ length: 21 }, (_, i) => ({ op: 'delete' as const, path: `modules/m1/${i}.md` })))).toMatch(/at most 20/)
  expect(opsProblem([{ op: 'put', path: 'modules/m1/a.md', content: 'a\0b' }])).toMatch(/256 KiB/)
})
```

Run: `cd web && npx vitest run src/pages/editor src/lib/editLimits.test.ts`
Expected: FAIL, modules not found.

- [ ] **Step 3: Implement `model.ts`**

```ts
import type { EditOp } from '../../types'

// DraftOps is a draft's changes in normal form: base paths renamed (current path → base path), base paths deleted, and
// the text of every new or changed file at its current path. toOps emits renames, deletes, puts: the order
// gitsync.ApplyOps applies them in.
export type DraftOps = { renames: Record<string, string>; deletes: string[]; puts: Record<string, string> }
export type Change = { kind: 'added' | 'changed' | 'renamed' | 'deleted'; path: string; from?: string }

export const emptyOps = (): DraftOps => ({ renames: {}, deletes: [], puts: {} })

export function fromOps(ops: EditOp[]): DraftOps {
  const d = emptyOps()
  for (const op of ops) {
    if (op.op === 'rename') d.renames[op.to] = op.from
    else if (op.op === 'delete') d.deletes.push(op.path)
    else d.puts[op.path] = op.content ?? ''
  }
  return d
}

const byKey = <T,>(a: [string, T], b: [string, T]) => a[0].localeCompare(b[0])

export function toOps(d: DraftOps): EditOp[] {
  return [
    ...Object.entries(d.renames).sort(byKey).map(([to, from]) => ({ op: 'rename' as const, from, to })),
    ...[...d.deletes].sort().map((path) => ({ op: 'delete' as const, path })),
    ...Object.entries(d.puts).sort(byKey).map(([path, content]) => ({ op: 'put' as const, path, content })),
  ]
}

const movedAway = (d: DraftOps, p: string) => Object.values(d.renames).includes(p)

// origin is the base path whose text `path` starts from; undefined for a file new in this draft.
export function origin(base: string[], d: DraftOps, path: string): string | undefined {
  if (path in d.renames) return d.renames[path]
  return base.includes(path) && !movedAway(d, path) && !d.deletes.includes(path) ? path : undefined
}

export function currentPaths(base: string[], d: DraftOps): string[] {
  const out = new Set(base.filter((p) => !movedAway(d, p) && !d.deletes.includes(p)))
  for (const p of Object.keys(d.renames)) out.add(p)
  for (const p of Object.keys(d.puts)) out.add(p)
  return [...out].sort()
}

// putText records path's text. original is the text the file starts from (undefined for a new file): a file edited
// back to its original leaves the draft.
export function putText(d: DraftOps, path: string, text: string, original: string | undefined): DraftOps {
  const puts = { ...d.puts }
  if (original !== undefined && text === original) delete puts[path]
  else puts[path] = text
  return { ...d, puts }
}

export function renamePath(base: string[], d: DraftOps, from: string, to: string): DraftOps {
  if (from === to) return d
  if (currentPaths(base, d).includes(to)) throw new Error(`${to} already exists`)
  if (d.deletes.includes(to)) throw new Error(`${to} was deleted in this draft; pick another name`)
  const o = origin(base, d, from)
  const renames = { ...d.renames }
  delete renames[from]
  if (o !== undefined && o !== to) renames[to] = o
  const puts = { ...d.puts }
  if (from in puts) {
    puts[to] = puts[from]
    delete puts[from]
  }
  return { ...d, renames, puts }
}

export function deletePath(base: string[], d: DraftOps, path: string): DraftOps {
  const o = origin(base, d, path)
  const renames = { ...d.renames }
  const puts = { ...d.puts }
  delete renames[path]
  delete puts[path]
  return { renames, puts, deletes: o === undefined ? d.deletes : [...d.deletes, o] }
}

export function changeList(base: string[], d: DraftOps): Change[] {
  const out: Change[] = []
  for (const [to, from] of Object.entries(d.renames)) out.push({ kind: 'renamed', path: to, from })
  for (const p of d.deletes) out.push({ kind: 'deleted', path: p })
  for (const p of Object.keys(d.puts)) {
    if (!(p in d.renames)) out.push({ kind: origin(base, d, p) === undefined ? 'added' : 'changed', path: p })
  }
  return out.sort((a, b) => a.path.localeCompare(b.path))
}

// matchFiles is "go to file": the letters of q in order, files whose name holds q as a run first, then shorter paths.
export function matchFiles(paths: string[], q: string): string[] {
  const s = q.toLowerCase()
  if (!s) return paths
  const inOrder = (p: string) => { let i = 0; for (const c of p.toLowerCase()) if (c === s[i]) i++; return i === s.length }
  const name = (p: string) => p.slice(p.lastIndexOf('/') + 1).toLowerCase()
  return paths.filter(inOrder).sort((a, b) => Number(name(b).includes(s)) - Number(name(a).includes(s)) || a.length - b.length)
}

export const languageOf = (path: string) =>
  path.endsWith('.md') ? 'markdown' : path.endsWith('.sh') ? 'shell' : /\.ya?ml$/.test(path) ? 'yaml' : 'plaintext'

// Spec: a dark editor for Forge, Quench and High Contrast (high-contrast black there), light for Anvil.
export const monacoTheme = (theme: string | undefined) => (theme === 'anvil' ? 'vs' : theme === 'contrast' ? 'hc-black' : 'vs-dark')
```

- [ ] **Step 4: Implement `diff.ts`, `autosave.ts`, and the limits**

`diff.ts`:

```ts
import { createTwoFilesPatch } from 'diff'

// unifiedDiff renders one file's change the way git does, so the Changes panel can reuse the reviewers' DiffView
// (which marks hidden and control characters). undefined from/to means the file is added/deleted.
export function unifiedDiff(from: string | undefined, to: string | undefined, before: string, after: string): string {
  const head = `diff --git a/${from ?? to} b/${to ?? from}\n` + (from && to && from !== to ? `rename from ${from}\nrename to ${to}\n` : '')
  const patch = createTwoFilesPatch(from ? `a/${from}` : '/dev/null', to ? `b/${to}` : '/dev/null', before, after, undefined, undefined, { context: 3 })
  return head + patch.split('\n').filter((l) => !l.startsWith('====') && !l.startsWith('Index: ')).join('\n')
}
```

If `npx tsc -b` rejects the `undefined` header arguments under `diff@9`'s types, pass `''` instead and strip the trailing tab from the `---`/`+++` lines.

`autosave.ts`:

```ts
import type { ApiError } from '../../api'

export type SaveState =
  | { kind: 'saved'; at: number }
  | { kind: 'dirty' }
  | { kind: 'saving' }
  | { kind: 'blocked'; reason: string } // the draft can't be saved as it is (too big, a bad path): fixing it resumes autosave
  | { kind: 'conflict'; reason: string } // another tab saved, or the draft went into review: reload; autosave stops
  | { kind: 'offline'; reason: string } // network or server trouble: the next change tries again

export function afterSave(err: ApiError | undefined, now: number): SaveState {
  if (!err) return { kind: 'saved', at: now }
  if (err.status === 409) return { kind: 'conflict', reason: err.message }
  if (err.status === 400 || err.status === 413) return { kind: 'blocked', reason: err.message }
  return { kind: 'offline', reason: err.message }
}

export const canAutosave = (s: SaveState) => s.kind !== 'conflict'

export function saveLabel(s: SaveState, now: number): string {
  switch (s.kind) {
    case 'saved': {
      const sec = Math.max(0, Math.round((now - s.at) / 1000))
      return sec < 5 ? 'Draft saved just now' : `Draft saved ${sec < 60 ? `${sec}s` : `${Math.round(sec / 60)}m`} ago`
    }
    case 'dirty':
      return 'Unsaved changes'
    case 'saving':
      return 'Saving…'
    case 'blocked':
    case 'conflict':
      return `Not saved: ${s.reason}`
    case 'offline':
      return `Not saved yet: ${s.reason}`
  }
}
```

`editLimits.ts`: keep `editProblem` (Task 10 deletes it with `EditFiles.tsx`) and add:

```ts
import type { EditOp } from '../types'
import { byteLen } from './editDraft'

// The server reads at most 1 MiB per request; this leaves room for the JSON around the ops.
export const MAX_DRAFT_BYTES = 1_000_000
const part = /^[A-Za-z0-9_-]([A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$/

// pathProblem mirrors gitsync.CheckPath; the server stays the authority.
export function pathProblem(p: string): string | undefined {
  if (p !== 'training.yaml' && !/^modules\/[^/]+\/.+/.test(p)) return `${p}: only training.yaml and files under modules/<id>/ can be edited here.`
  if (p.length > 255 || !p.split('/').every((x) => part.test(x))) return `${p}: use letters, digits, '.', '_' and '-' in each part, not starting or ending with '.'.`
  if (!exts.some((e) => p.toLowerCase().endsWith(e))) return `${p}: only .md, .yaml, .yml and .sh files can be edited here.`
}

// opsProblem mirrors gitsync.CheckOps for drafts (no lower bound: an empty draft is fine) plus the 1 MiB request cap.
export function opsProblem(ops: EditOp[]): string | undefined {
  if (ops.length > MAX_EDIT_FILES) return `A draft changes at most ${MAX_EDIT_FILES} files.`
  for (const op of ops) {
    for (const p of op.op === 'rename' ? [op.from, op.to] : [op.path]) {
      const bad = pathProblem(p)
      if (bad) return bad
    }
    if ((op.op === 'delete' && op.path === 'training.yaml') || (op.op === 'rename' && (op.from === 'training.yaml' || op.to === 'training.yaml')))
      return "training.yaml can't be renamed or deleted."
    if (op.op === 'put' && (byteLen(op.content) > MAX_EDIT_FILE_BYTES || op.content.includes('\0'))) return `${op.path}: must be text of at most 256 KiB.`
  }
  if (byteLen(JSON.stringify(ops)) > MAX_DRAFT_BYTES) return 'This draft is over 1 MiB. Split it into smaller drafts.'
}
```

- [ ] **Step 5: Run the tests**

Run: `cd web && npm test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 6: Update docs/user in the same commit**

No user-visible change in this task (pure functions the editor uses from Task 10). Confirm that `go test ./internal/docs` still passes.

- [ ] **Step 7: Commit**

```bash
git add -- web/src/pages/editor web/src/lib/editLimits.test.ts
git commit -m "feat(web): editor state as pure, tested functions (draft ops, diffs, autosave, limits)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/editor web/src/lib/editLimits.ts web/src/lib/editLimits.test.ts web/src/types.ts web/package.json web/package-lock.json
```

---

### Task 10: Monaco IDE shell: lazy chunk, same-origin workers, explorer, tabs, autosave, submit

**Files:**
- Create: `web/src/pages/editor/monaco.ts`, `web/src/pages/editor/yaml.worker.ts`, `web/src/pages/editor/CodeEditor.tsx`, `web/src/pages/editor/Explorer.tsx`, `web/src/pages/editor/Explorer.test.tsx`, `web/src/pages/editor/Preview.tsx`, `web/src/pages/editor/Ide.tsx`, `web/src/pages/NewDraft.tsx`, `web/scripts/check-chunks.mjs`, `e2e/tests/helpers.ts`
- Modify: `web/package.json` (+`monaco-editor`, `monaco-yaml`; build script), `web/vite.config.ts`, `web/src/App.tsx`, `web/src/pages/Edits.tsx`, `web/src/pages/EditReview.tsx`, `web/src/lib/editLimits.ts`, `web/src/lib/editDraft.ts`, `web/src/components/DiffView.test.tsx`, `web/src/theme/app.css`, `internal/httpapi/security_test.go`, `e2e/tests/forge-people.spec.ts`, `docs/user/authors/editing-content.md`
- Delete: `web/src/pages/EditFiles.tsx`

**Interfaces:**
- Consumes: the Task 8 drafts API; the Task 9 model and autosave functions; `GET /api/authoring/schema`; `isLeaveChord` (`lib/advanceFocus.ts`); `Markdown`.
- Produces:
  - Route `/edits/drafts/:id`, which renders the lazily loaded default export of `pages/editor/Ide.tsx`.
  - Route `/edits/new?training=&from=`, which renders `NewDraftPage`: it creates a draft and replaces the URL.
  - `CodeEditor` props: `{ draftId: number; path: string; text: string; readOnly: boolean; markers: { line: number; message: string }[]; reveal?: { line: number; n: number }; onChange(path, text); onLeave(); onGoToFile() }`
  - `Explorer` props: `{ paths: string[]; greyed: { path: string; reason: string }[]; changed: Set<string>; active: string; readOnly: boolean; onOpen(p); onNew(p); onRename(from, to); onDelete(p); onNewModule?(): void }`
  - `Preview` props: `{ path: string; text: string; read(p: string): string | undefined }`
  - `e2e/tests/helpers.ts`: `login(browser, user)`, `setEditorText(page, text)`

- [ ] **Step 1: Write the failing tests: CSP stays strict, the main chunk has no Monaco, the explorer's rules**

`internal/httpapi/security_test.go`, appended:

```go
// Monaco must not loosen the CSP: its workers are same-origin files (docs-and-editor spec §5, §9).
func TestCSPStaysStrict(t *testing.T) {
	dirs := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, val, _ := strings.Cut(strings.TrimSpace(d), " ")
		dirs[name] = val
	}
	if dirs["script-src"] != "'self'" || dirs["default-src"] != "'self'" {
		t.Fatalf("script-src %q, default-src %q: never widen these for the editor", dirs["script-src"], dirs["default-src"])
	}
	if _, ok := dirs["worker-src"]; ok {
		t.Fatal("worker-src stays unset: it falls back to script-src 'self', which is what same-origin workers need")
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(dirs["connect-src"], "http") {
		t.Fatalf("csp: %s", csp)
	}
}
```

This passes immediately. It is a guard that keeps the CSP from being widened later. Run: `go test ./internal/httpapi -run CSP`. Expected: PASS.

`web/scripts/check-chunks.mjs`:

```js
// Build test (spec: "a build test asserts the main chunk does not include it"): Monaco may only be in lazily loaded
// chunks. Walks dist/index.html's entry script and everything it imports statically; dynamic import() is not followed.
import { readdirSync, readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist')
const marker = 'MonacoEnvironment' // read by Monaco itself and set by pages/editor/monaco.ts; survives minification
const entry = /<script type="module"[^>]*src="\/([^"]+)"/.exec(readFileSync(join(dist, 'index.html'), 'utf8'))?.[1]
if (!entry) throw new Error('check-chunks: no entry script in dist/index.html')
const seen = new Set()
const walk = (file) => {
  if (seen.has(file)) return
  seen.add(file)
  const src = readFileSync(join(dist, file), 'utf8')
  if (src.includes(marker)) throw new Error(`check-chunks: Monaco reached the main bundle (${file}); import it only from pages/editor`)
  for (const m of src.matchAll(/(?:import|from)\s*["']\.\/([^"']+\.js)["']/g)) walk(join(dirname(file), m[1]))
}
walk(entry)
const lazy = readdirSync(join(dist, 'assets')).filter((f) => f.endsWith('.js') && readFileSync(join(dist, 'assets', f), 'utf8').includes(marker))
if (lazy.length === 0) throw new Error('check-chunks: no chunk contains Monaco at all, so this check proves nothing; update the marker')
console.log(`check-chunks: main bundle is ${seen.size} files without Monaco; Monaco is in ${lazy.join(', ')}`)
```

`web/package.json` scripts: `"build": "tsc -b && vite build && node scripts/check-chunks.mjs"`.

`web/src/pages/editor/Explorer.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Explorer } from './Explorer'

const noop = () => {}
const html = renderToStaticMarkup(
  <Explorer paths={['training.yaml', 'modules/m1/reading/a.md']} greyed={[{ path: 'modules/m1/lab/terraform/main.tf', reason: 'only .md, .yaml, .yml and .sh files can be edited here' }]}
    changed={new Set(['modules/m1/reading/a.md'])} active="modules/m1/reading/a.md" readOnly={false}
    onOpen={noop} onNew={noop} onRename={noop} onDelete={noop} />,
)

test('training.yaml cannot be renamed or deleted; other files can', () => {
  expect(html).not.toContain('aria-label="Rename training.yaml"')
  expect(html).not.toContain('aria-label="Delete training.yaml"')
  expect(html).toContain('aria-label="Rename modules/m1/reading/a.md"')
  expect(html).toContain('aria-label="Delete modules/m1/reading/a.md"')
})

test('files that cannot be edited are greyed with the reason', () => {
  expect(html).toContain('modules/m1/lab/terraform/main.tf (can&#x27;t be edited here: only .md, .yaml, .yml and .sh files can be edited here)')
})

test('changed files are marked, the open file is current', () => {
  expect(html).toContain('aria-current="true"')
  expect(html).toContain('(changed)')
})
```

Run: `cd web && npx vitest run src/pages/editor/Explorer.test.tsx`
Expected: FAIL (no `Explorer`).

- [ ] **Step 2: Install Monaco and configure same-origin workers**

Run: `cd web && npm install monaco-editor@0.57.0 monaco-yaml@5.5.1`

`web/vite.config.ts`: add `worker: { format: 'es' },` next to `plugins`. Module workers, so Monaco's and monaco-yaml's worker code can import chunks.

`web/src/pages/editor/yaml.worker.ts`. This is monaco-yaml's documented Vite workaround (its README: "Why doesn't it work with Vite?"):

```ts
import 'monaco-yaml/yaml.worker.js'
```

`web/src/pages/editor/monaco.ts`:

```ts
// Monaco is set up here only. This file is reachable only from the editor route's lazy chunk; scripts/check-chunks.mjs
// fails the build if Monaco reaches the main bundle. Workers come from Vite ?worker imports: same-origin files, so the
// CSP keeps script-src 'self' with no blob: and no new hosts.
import * as monaco from 'monaco-editor'
import { configureMonacoYaml } from 'monaco-yaml'
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'
import YamlWorker from './yaml.worker?worker'

;(self as unknown as { MonacoEnvironment: monaco.Environment }).MonacoEnvironment = {
  getWorker: (_id: string, label: string) => (label === 'yaml' ? new YamlWorker() : new EditorWorker()),
}

let yamlReady = false

// setupYaml gives YAML files autocomplete, enum suggestions and hover docs from the generated schemas (once per page).
export function setupYaml(schemas: Record<string, object>) {
  if (yamlReady) return
  yamlReady = true
  configureMonacoYaml(monaco, {
    enableSchemaRequest: false, // schemas are inline; nothing is fetched
    schemas: (['training', 'module', 'quiz', 'lab'] as const).map((kind) => ({
      uri: `inmemory://crucible/${kind}.schema.json`,
      fileMatch: [`**/${kind}.yaml`],
      schema: schemas[kind],
    })),
  })
}

export { monaco }
```

Check that `monaco-editor/editor/editor.worker` resolves: monaco-editor 0.57's `exports` maps `./*` to `./esm/vs/*.js`, so this is `esm/vs/editor/editor.worker.js`. Also check the export name of `Environment`: if `tsc` can't find `monaco.Environment`, use `{ getWorker(id: string, label: string): Worker }` instead.

- [ ] **Step 3: `CodeEditor.tsx`, `Explorer.tsx`, `Preview.tsx`**

`CodeEditor.tsx`:

```tsx
import { useEffect, useRef } from 'react'
import { monaco } from './monaco'
import { isLeaveChord } from '../../lib/advanceFocus'
import { languageOf, monacoTheme } from './model'

type Props = {
  draftId: number; path: string; text: string; readOnly: boolean
  markers: { line: number; message: string }[]; reveal?: { line: number; n: number }
  onChange: (path: string, text: string) => void; onLeave: () => void; onGoToFile: () => void
}

const uriOf = (draftId: number, path: string) => monaco.Uri.parse(`file:///draft-${draftId}/${path}`)
const pathOf = (m: monaco.editor.ITextModel) => m.uri.path.split('/').slice(2).join('/')

// CodeEditor is one Monaco editor; each open file is its own model, so undo history and cursor survive tab switches.
// A model keeps the file's line endings (CRLF stays CRLF).
export function CodeEditor({ draftId, path, text, readOnly, markers, reveal, onChange, onLeave, onGoToFile }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const ed = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)
  const cb = useRef({ onChange, onLeave, onGoToFile })
  cb.current = { onChange, onLeave, onGoToFile }
  useEffect(() => {
    const e = monaco.editor.create(host.current!, {
      automaticLayout: true, accessibilitySupport: 'on', minimap: { enabled: false }, wordWrap: 'on',
      fontFamily: "'JetBrains Mono', monospace", theme: monacoTheme(document.documentElement.dataset.theme),
      renderControlCharacters: true, unicodeHighlight: { invisibleCharacters: true, ambiguousCharacters: true },
      ariaLabel: 'File editor. Press Control+Shift+F6 to leave.',
    })
    e.onKeyDown((k) => {
      if (isLeaveChord(k.browserEvent)) {
        k.preventDefault()
        k.stopPropagation()
        cb.current.onLeave()
      }
    })
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyP, () => cb.current.onGoToFile())
    e.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.KeyP, () => e.trigger('keyboard', 'editor.action.quickCommand', null))
    e.onDidChangeModelContent(() => {
      const m = e.getModel()
      if (m) cb.current.onChange(pathOf(m), m.getValue())
    })
    ed.current = e
    return () => {
      e.dispose()
      for (const m of monaco.editor.getModels()) if (m.uri.path.startsWith(`/draft-${draftId}/`)) m.dispose()
    }
  }, [draftId])
  useEffect(() => {
    const uri = uriOf(draftId, path)
    const m = monaco.editor.getModel(uri) ?? monaco.editor.createModel(text, languageOf(path), uri)
    if (m.getValue() !== text) m.setValue(text) // changed outside the editor (an insert, a rebase)
    if (ed.current?.getModel() !== m) ed.current?.setModel(m)
  }, [draftId, path, text])
  useEffect(() => { ed.current?.updateOptions({ readOnly }) }, [readOnly])
  useEffect(() => {
    const m = ed.current?.getModel()
    if (m) monaco.editor.setModelMarkers(m, 'crucible', markers.map((x) => ({
      severity: monaco.MarkerSeverity.Error, message: x.message, startLineNumber: x.line, startColumn: 1, endLineNumber: x.line, endColumn: m.getLineMaxColumn(Math.min(x.line, m.getLineCount())),
    })))
  }, [markers, path])
  useEffect(() => {
    if (!reveal || !ed.current) return
    ed.current.revealLineInCenter(reveal.line)
    ed.current.setPosition({ lineNumber: reveal.line, column: 1 })
    ed.current.focus()
  }, [reveal])
  return <div ref={host} className="code-editor" data-testid="code-editor" />
}
```

`Explorer.tsx`:

```tsx
import { useState } from 'react'
import { pathProblem } from '../../lib/editLimits'

type Props = {
  paths: string[]; greyed: { path: string; reason: string }[]; changed: Set<string>; active: string; readOnly: boolean
  onOpen: (p: string) => void; onNew: (p: string) => void; onRename: (from: string, to: string) => void; onDelete: (p: string) => void
  onNewModule?: () => void
}

// Explorer is the training at the draft's base plus the draft's changes. Disallowed paths are greyed with the reason;
// training.yaml can be edited but never renamed or deleted.
export function Explorer({ paths, greyed, changed, active, readOnly, onOpen, onNew, onRename, onDelete, onNewModule }: Props) {
  const [mode, setMode] = useState<{ kind: 'new' } | { kind: 'rename'; path: string } | null>(null)
  const [value, setValue] = useState('')
  const problem = mode && value ? pathProblem(value) : undefined
  const done = (e: React.FormEvent) => {
    e.preventDefault()
    if (!mode || problem || !value) return
    if (mode.kind === 'new') onNew(value)
    else onRename(mode.path, value)
    setMode(null)
  }
  return (
    <div className="explorer" role="region" aria-label="Explorer">
      {!readOnly && (
        <p>
          <button className="ghost" onClick={() => { setMode({ kind: 'new' }); setValue(active.includes('/') ? active.slice(0, active.lastIndexOf('/') + 1) : 'modules/') }}>New file</button>
          {onNewModule && <> <button className="ghost" onClick={onNewModule}>New module</button></>}
        </p>
      )}
      {mode && (
        <form onSubmit={done}>
          <label>{mode.kind === 'new' ? 'Path of the new file' : `New path for ${mode.path}`}{' '}
            <input autoFocus value={value} onChange={(e) => setValue(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setMode(null)} />
          </label>{' '}
          <button type="submit" disabled={!value || !!problem}>{mode.kind === 'new' ? 'Create' : 'Rename'}</button>{' '}
          <button type="button" className="ghost" onClick={() => setMode(null)}>Cancel</button>
          <span role="status" className="muted"> {problem}</span>
        </form>
      )}
      <ul>
        {paths.map((p) => (
          <li key={p}>
            <button className={p === active ? '' : 'ghost'} aria-label={p} aria-current={p === active ? 'true' : undefined} onClick={() => onOpen(p)}>
              {p}{changed.has(p) && <span aria-hidden="true"> •</span>}
            </button>
            {changed.has(p) && <span className="sr-only"> (changed)</span>}
            {!readOnly && p !== 'training.yaml' && (
              <>
                {' '}<button className="ghost small" aria-label={`Rename ${p}`} onClick={() => { setMode({ kind: 'rename', path: p }); setValue(p) }}>Rename</button>
                {' '}<button className="ghost small" aria-label={`Delete ${p}`} onClick={() => window.confirm(`Delete ${p}?`) && onDelete(p)}>Delete</button>
              </>
            )}
          </li>
        ))}
        {greyed.map((g) => (
          <li key={g.path}><span className="muted" title={g.reason} aria-label={`${g.path} (can't be edited here: ${g.reason})`}>{g.path}</span></li>
        ))}
      </ul>
    </div>
  )
}
```

`Preview.tsx` (Task 12 extends it to quizzes and labs):

```tsx
import { Markdown } from '../../components/Markdown'

// Preview renders the open file the way trainees will see it. Nothing executes.
export function Preview({ path, text }: { path: string; text: string; read: (p: string) => string | undefined }) {
  if (path.endsWith('.md')) return <Markdown text={text} />
  return <pre>{text}</pre>
}
```

- [ ] **Step 4: `Ide.tsx`**

```tsx
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, type ApiError } from '../../api'
import type { ContentEdit, DraftInfo, FileEntry } from '../../types'
import { ErrorBox } from '../../components/ErrorBox'
import { Loader } from '../../components/Loader'
import { opsProblem } from '../../lib/editLimits'
import { CodeEditor } from './CodeEditor'
import { Explorer } from './Explorer'
import { Preview } from './Preview'
import { setupYaml } from './monaco'
import { afterSave, canAutosave, saveLabel, type SaveState } from './autosave'
import { changeList, currentPaths, deletePath, emptyOps, fromOps, origin, putText, renamePath, toOps, type DraftOps } from './model'

const AUTOSAVE_MS = 2000

export default function Ide() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const [draft, setDraft] = useState<DraftInfo>()
  const [files, setFiles] = useState<FileEntry[]>([])
  const [work, setWork] = useState<DraftOps>(emptyOps)
  const [baseText, setBaseText] = useState<Record<string, string>>({})
  const [tabs, setTabs] = useState<string[]>([])
  const [active, setActive] = useState('')
  const [title, setTitle] = useState('')
  const [save, setSave] = useState<SaveState>({ kind: 'saved', at: Date.now() })
  const [msg, setMsg] = useState('')
  const [fatal, setFatal] = useState<ApiError>()
  const [now, setNow] = useState(Date.now())
  const explorerRef = useRef<HTMLDivElement>(null)

  const load = useCallback(async () => {
    const [d, f] = await Promise.all([api<DraftInfo>(`/api/authoring/drafts/${id}`), api<FileEntry[]>(`/api/authoring/drafts/${id}/files`)])
    setupYaml(await api<Record<string, object>>(`/api/authoring/schema?training=${encodeURIComponent(d.training)}`))
    setDraft(d); setFiles(f); setWork(fromOps(d.ops)); setTitle(d.title); setBaseText({})
    setSave({ kind: 'saved', at: Date.now() })
  }, [id])
  useEffect(() => { load().catch(setFatal) }, [load])
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(t) }, [])

  const base = useMemo(() => files.filter((f) => f.editable).map((f) => f.path), [files])
  const greyed = useMemo(() => files.filter((f) => !f.editable).map((f) => ({ path: f.path, reason: f.reason ?? '' })), [files])
  const paths = useMemo(() => currentPaths(base, work), [base, work])
  const changes = useMemo(() => changeList(base, work), [base, work])
  const ops = useMemo(() => toOps(work), [work])
  const problem = opsProblem(ops)
  const originalOf = (p: string) => { const o = origin(base, work, p); return o === undefined ? undefined : baseText[o] }
  const textOf = (p: string) => work.puts[p] ?? originalOf(p) ?? ''

  const loadBase = async (p: string | undefined) => {
    if (!p || p in baseText) return
    const { content } = await api<{ content: string }>(`/api/authoring/drafts/${id}/file?path=${encodeURIComponent(p)}`)
    setBaseText((b) => ({ ...b, [p]: content }))
  }
  const change = (next: DraftOps) => { setWork(next); setSave((s) => (canAutosave(s) ? { kind: 'dirty' } : s)) }
  const open = async (p: string) => {
    try { await loadBase(origin(base, work, p)) } catch (e) { return setMsg((e as Error).message) }
    setTabs((t) => (t.includes(p) ? t : [...t, p]))
    setActive(p)
  }
  const edit = (p: string, text: string) => { if (text !== textOf(p)) change(putText(work, p, text, originalOf(p))) }
  const create = (p: string) => {
    if (paths.includes(p)) return setMsg(`${p} already exists.`)
    change(putText(work, p, '', undefined)); setTabs((t) => [...t, p]); setActive(p)
  }
  const rename = async (from: string, to: string) => {
    try { await loadBase(origin(base, work, from)); change(renamePath(base, work, from, to)) } catch (e) { return setMsg((e as Error).message) }
    setTabs((t) => t.map((x) => (x === from ? to : x)))
    if (active === from) setActive(to)
  }
  const remove = (p: string) => {
    change(deletePath(base, work, p)); setTabs((t) => t.filter((x) => x !== p))
    if (active === p) setActive('')
  }
  const close = (p: string) => { setTabs((t) => t.filter((x) => x !== p)); if (active === p) setActive(tabs.find((x) => x !== p) ?? '') }
  const leave = () => explorerRef.current?.querySelector<HTMLButtonElement>('button[aria-current="true"], button')?.focus()

  const saveNow = useCallback(async (): Promise<boolean> => {
    if (!draft) return false
    if (problem) { setSave({ kind: 'blocked', reason: problem }); return false }
    setSave({ kind: 'saving' })
    try {
      const d = await api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: draft.base_sha, ops, updated_at: draft.updated_at } })
      setDraft(d)
      setSave(afterSave(undefined, Date.now()))
      return true
    } catch (e) {
      setSave(afterSave(e as ApiError, Date.now()))
      return false
    }
  }, [draft, id, title, ops, problem])
  useEffect(() => { // autosave about 2 s after the last change; never once another tab owns the draft
    if (save.kind !== 'dirty') return
    const t = setTimeout(saveNow, AUTOSAVE_MS)
    return () => clearTimeout(t)
  }, [save, saveNow])

  const submit = async () => {
    setMsg('')
    if (!title.trim()) return setMsg("Give the draft a title first: it becomes the edit's title.")
    if (save.kind !== 'saved' && !(await saveNow())) return
    try {
      const e = await api<ContentEdit>(`/api/authoring/drafts/${id}/submit`, { method: 'POST', json: {} })
      nav(`/edits/${e.id}`)
    } catch (e) { setMsg((e as Error).message) }
  }

  if (fatal) return <ErrorBox error={fatal} />
  if (!draft) return <Loader label="Heating the editor…" />
  const readOnly = draft.state === 'in_review' || save.kind === 'conflict'
  return (
    <section className="ide" aria-label={`Editing ${draft.training}`}>
      <header className="ide-bar">
        <Link to="/edits">All edits</Link>
        <strong>{draft.training}</strong>
        <label>Title <input value={title} maxLength={200} disabled={readOnly} onChange={(e) => { setTitle(e.target.value); setSave((s) => (canAutosave(s) ? { kind: 'dirty' } : s)) }} /></label>
        {draft.base_sha !== draft.head_sha && <span role="note">Newer content is on the branch.</span>}
      </header>
      {draft.state === 'in_review' && <p role="note">This draft is in review. <Link to={`/edits/${draft.edit_id}`}>Open the edit</Link> and withdraw it to keep working here.</p>}
      {save.kind === 'conflict' && <p role="alert" className="error">{save.reason} <button onClick={() => load().catch(setFatal)}>Reload</button></p>}
      <p role="alert" className="error">{msg}</p>
      <div className="ide-main">
        <div ref={explorerRef}>
          <Explorer paths={paths} greyed={greyed} changed={new Set(changes.map((c) => c.path))} active={active} readOnly={readOnly}
            onOpen={open} onNew={create} onRename={rename} onDelete={remove} />
        </div>
        <div className="ide-editor">
          <div className="ide-tabs" role="toolbar" aria-label="Open files">
            {tabs.map((t) => (
              <span key={t}>
                <button className={t === active ? '' : 'ghost'} aria-current={t === active ? 'true' : undefined} onClick={() => setActive(t)} title={t}>
                  {t.slice(t.lastIndexOf('/') + 1)}{t in work.puts && <span aria-label=" (unsaved changes)"> •</span>}
                </button>
                <button className="ghost small" aria-label={`Close ${t}`} onClick={() => close(t)}>×</button>
              </span>
            ))}
          </div>
          {active
            ? <CodeEditor draftId={id} path={active} text={textOf(active)} readOnly={readOnly} markers={[]} onChange={edit} onLeave={leave} onGoToFile={() => {}} />
            : <p className="muted">Open a file from the explorer.</p>}
        </div>
        {active && <div className="ide-preview" data-testid="edit-preview"><Preview path={active} text={textOf(active)} read={(p) => (paths.includes(p) ? textOf(p) : undefined)} /></div>}
      </div>
      <footer className="ide-status" role="status" aria-live="polite">
        <span>{saveLabel(save, now)}</span>
        <span>base {draft.base_sha.slice(0, 7)}</span>
        <button disabled={readOnly} onClick={submit}>Submit for review</button>
      </footer>
    </section>
  )
}
```

`onGoToFile={() => {}}` is wired to the go-to-file dialog in Task 11, the next task.

`app.css`, appended (existing tokens only):

```css
.ide { display: grid; grid-template-rows: auto auto auto 1fr auto; height: calc(100vh - 4rem); padding: 0 1rem; }
.ide-bar { display: flex; gap: 1rem; align-items: center; flex-wrap: wrap; padding: .5rem 0; }
.ide-main { display: grid; grid-template-columns: 260px minmax(0, 1fr) minmax(0, 1fr); gap: .75rem; min-height: 0; }
.ide-main > * { min-height: 0; overflow: auto; }
.ide-editor { display: grid; grid-template-rows: auto 1fr; min-height: 0; }
.code-editor { min-height: 60vh; height: 100%; }
.ide-tabs { display: flex; gap: .25rem; flex-wrap: wrap; }
.ide-status { display: flex; gap: 1rem; align-items: center; padding: .5rem 0; border-top: 1px solid var(--surface-2); }
.explorer ul { list-style: none; padding: 0; margin: 0; }
.explorer button.small, .ide-tabs button.small { padding: 0 .3rem; font-size: .8rem; }
```

- [ ] **Step 5: Routes, the Edits page, redo, and removing the textarea editor**

`web/src/pages/NewDraft.tsx`:

```tsx
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api, type ApiError } from '../api'
import type { DraftInfo } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

// NewDraftPage creates a draft (optionally reopening a returned edit) and replaces itself with the editor.
export function NewDraftPage() {
  const [q] = useSearchParams()
  const nav = useNavigate()
  const [err, setErr] = useState<ApiError>()
  const started = useRef(false) // StrictMode runs effects twice in dev: one draft, not two
  useEffect(() => {
    if (started.current) return
    started.current = true
    const from = Number(q.get('from') ?? 0)
    api<DraftInfo>('/api/authoring/drafts', { method: 'POST', json: { training: q.get('training') ?? '', title: '', ...(from ? { from_edit: from } : {}) } })
      .then((d) => nav(`/edits/drafts/${d.id}`, { replace: true }))
      .catch(setErr)
  }, [q, nav])
  return err ? <ErrorBox error={err} /> : <Loader label="Laying out a fresh draft…" />
}
```

`App.tsx`:
- Remove the `EditFilesPage` import and route.
- Add `const Ide = lazy(() => import('./pages/editor/Ide'))` (`lazy`, `Suspense` from react).
- Add `<Route path="/edits/new" element={<NewDraftPage />} />`.
- Add `<Route path="/edits/drafts/:id" element={<Suspense fallback={<Loader label="Heating the editor…" />}><Ide /></Suspense>} />`.
- Delete `web/src/pages/EditFiles.tsx`.

`Edits.tsx`: fetch `useFetch<DraftInfo[]>('/api/authoring/drafts')` and render it above the edits list:

```tsx
      <h2>My drafts</h2>
      {(drafts.data ?? []).length === 0 && <p className="muted">No drafts. Start an edit to open the editor.</p>}
      <ul>
        {(drafts.data ?? []).map((d) => (
          <li key={d.id}>
            <Link to={`/edits/drafts/${d.id}`}>{d.title || 'Untitled draft'}</Link>{' '}
            <span className="badge">{d.state === 'in_review' ? 'in review' : d.state === 'returned' ? 'returned' : 'draft'}</span>{' '}
            <span className="muted">{d.training}</span>
          </li>
        ))}
      </ul>
```

The **Start an edit** button keeps its label and still navigates to `/edits/new?training=…`.

`EditReview.tsx`: the stale-edit link becomes **Reopen in the editor**, shown for the author on `stale`, `rejected` and `withdrawn` edits. It goes to `/edits/new?training=${encodeURIComponent(e.training)}&from=${e.id}`.

Remove the now-unused code:
- `editProblem` (and `exts` only if nothing else uses it; `pathProblem` does) from `editLimits.ts`.
- `changedFiles`, `restoreEol`, `headVersions` from `editDraft.ts` (keep `byteLen`).
- Their tests from `DiffView.test.tsx`.

Run `grep -rn "editProblem\|changedFiles\|headVersions\|restoreEol" web/src` to confirm nothing still uses them.

- [ ] **Step 6: E2E: the existing content-edit journey drives the new editor**

`e2e/tests/helpers.ts`:

```ts
import { expect, type Browser, type Page } from '@playwright/test'

export async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

// setEditorText replaces the open file's text in Monaco: select all, then one multi-character input (Monaco applies no
// auto-indent or auto-close to it). Callers check the result through the preview or the saved draft.
export async function setEditorText(page: Page, text: string) {
  await page.getByTestId('code-editor').locator('.view-lines').click()
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.insertText(text)
}
```

In `forge-people.spec.ts`, import `setEditorText` from `./helpers`. Add `exact: true` to the `getByRole('button', { name: 'modules/01-sparks/reading/sparks.md' })` call: Playwright matches names by substring, and the explorer also has `Rename …`/`Delete …` buttons for that path. Then replace the four textarea lines (from `const editor = …` through `editor.fill(…)`) with:

```ts
  const sparks = await (await leader.request.get('/api/content/forge-102/file?path=modules/01-sparks/reading/sparks.md')).json()
  expect(sparks.content).toMatch(/Every blade starts as a spark/)
  await setEditorText(leader, sparks.content + `\nThe anvil remembers ${run}.\n`)
```

The `edit-preview` assertion, the Title field and **Submit for review** stay as they are. Leave the local `login` in this spec alone, to keep the diff small.

- [ ] **Step 7: Update docs/user in the same commit**

Rewrite `docs/user/authors/editing-content.md`:
- Front matter: `title: The editor`, and `covers: [route:/edits/new, route:/edits/drafts/:id]`.
- How to start: Edits, choose the training, **Start an edit**.
- The explorer: new file, rename, delete. Greyed files say why. `training.yaml` stays put.
- Tabs and the • for unsaved changes.
- Autosave: drafts are saved on the server about 2 seconds after you stop typing, and you can have 5 open drafts.
- The YAML help: autocomplete, allowed values and hover text.
- Leaving the editor with Ctrl+Shift+F6 or Ctrl+Alt+↑.
- **Submit for review**.
- A draft in review is read-only until its edit is withdrawn.
- A second tab on the same draft stops saving and asks you to reload.

- [ ] **Step 8: Run all checks, including the chunk test**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint && cd .. && go test ./internal/httpapi ./internal/docs`
Expected: PASS. `npm run build` ends with `check-chunks: main bundle is N files without Monaco; Monaco is in Ide-….js`.

Then run the browser journey: `KEYCLOAK_PORT=8082 make local-check`. Expected: it ends with `🔥 Local check passed. The forge holds.` If `setEditorText` leaves auto-inserted characters, switch the helper to a clipboard paste: grant `clipboard-read`/`clipboard-write` on the context, write the text with `navigator.clipboard.writeText` in `page.evaluate`, then press `ControlOrMeta+V`.

- [ ] **Step 9: Commit**

```bash
git add -- web/src/pages/editor web/src/pages/NewDraft.tsx web/scripts/check-chunks.mjs e2e/tests/helpers.ts
git rm -q -- web/src/pages/EditFiles.tsx
git commit -m "feat(web): Monaco editor as a lazy chunk with same-origin workers, explorer, tabs and server-side drafts" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web internal/httpapi/security_test.go e2e/tests/helpers.ts e2e/tests/forge-people.spec.ts docs/user/authors/editing-content.md
```

---

### Task 11: Problems, Changes, go to file, the command palette, keyboard reach, narrow screens

**Files:**
- Create: `web/src/pages/editor/ProblemsPanel.tsx`, `web/src/pages/editor/ChangesPanel.tsx`, `web/src/pages/editor/GoToFile.tsx`, `web/src/pages/editor/panels.test.tsx`
- Modify: `web/src/pages/editor/Ide.tsx`, `web/src/theme/app.css`, `docs/user/authors/editing-content.md`

**Interfaces:**
- Consumes: `POST /api/authoring/validate` (Task 3/8), `Problem`, `changeList`, `unifiedDiff`, `matchFiles`, `DiffView`.
- Produces:
  - `ProblemsPanel({ problems: Problem[]; known: (file: string) => boolean; onJump(p: Problem) })`
  - `ChangesPanel({ changes: Change[]; diffOf(c: Change): string | undefined; onOpen(path) })`
  - `GoToFile({ paths: string[]; onPick(p); onClose() })`

- [ ] **Step 1: Write the failing component tests**

`web/src/pages/editor/panels.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ProblemsPanel } from './ProblemsPanel'
import { ChangesPanel } from './ChangesPanel'
import { GoToFile } from './GoToFile'

const noop = () => {}

test('problems are buttons that name file and line; unknown files are plain text', () => {
  const html = renderToStaticMarkup(<ProblemsPanel problems={[
    { file: 'modules/m1/quiz.yaml', line: 2, msg: 'question 1 (q1): needs at least 2 options' },
    { file: 'modules/m1/reading/gone.md', line: 1, msg: 'file not found' },
  ]} known={(f) => f.endsWith('quiz.yaml')} onJump={noop} />)
  expect(html).toContain('<button')
  expect(html).toContain('modules/m1/quiz.yaml:2')
  expect(html).toContain('needs at least 2 options')
  expect(html.match(/<button/g)).toHaveLength(1)
})

test('no problems says so', () => {
  expect(renderToStaticMarkup(<ProblemsPanel problems={[]} known={() => true} onJump={noop} />)).toContain('No problems')
})

test('changes show their kind and the reviewers’ diff view', () => {
  const html = renderToStaticMarkup(<ChangesPanel changes={[{ kind: 'renamed', path: 'm/b.md', from: 'm/a.md' }, { kind: 'deleted', path: 'm/c.md' }]}
    diffOf={(c) => (c.kind === 'deleted' ? '-bye​' : undefined)} onOpen={noop} />)
  expect(html).toContain('Renamed')
  expect(html).toContain('m/a.md')
  expect(html).toContain('Deleted')
  expect(html).toContain('data-testid="diff"')
  expect(html).toContain('‹U+200B›') // hidden characters are marked, as reviewers see them
})

test('go to file lists matches as options', () => {
  const html = renderToStaticMarkup(<GoToFile paths={['training.yaml', 'modules/m1/quiz.yaml']} onPick={noop} onClose={noop} />)
  expect(html).toContain('role="dialog"')
  expect(html).toContain('role="listbox"')
  expect(html.match(/role="option"/g)).toHaveLength(2)
})
```

Run: `cd web && npx vitest run src/pages/editor/panels.test.tsx`. Expected: FAIL.

- [ ] **Step 2: Implement the panels**

`ProblemsPanel.tsx`:

```tsx
import type { Problem } from '../../types'

export function ProblemsPanel({ problems, known, onJump }: { problems: Problem[]; known: (file: string) => boolean; onJump: (p: Problem) => void }) {
  if (problems.length === 0) return <p className="muted" role="status">No problems. The training loads and lints clean.</p>
  return (
    <ul className="problems" aria-label="Problems">
      {problems.map((p, i) => (
        <li key={i}>
          {known(p.file)
            ? <button className="ghost" onClick={() => onJump(p)}><code>{`${p.file}:${p.line}`}</code> {p.msg}</button>
            : <span><code>{p.file}</code> {p.msg}</span>}
        </li>
      ))}
    </ul>
  )
}
```

`ChangesPanel.tsx`:

```tsx
import { DiffView } from '../../components/DiffView'
import type { Change } from './model'

const label = { added: 'Added', changed: 'Changed', renamed: 'Renamed', deleted: 'Deleted' }

// ChangesPanel shows what a reviewer will see, with the same diff view (hidden and control characters marked).
export function ChangesPanel({ changes, diffOf, onOpen }: { changes: Change[]; diffOf: (c: Change) => string | undefined; onOpen: (p: string) => void }) {
  if (changes.length === 0) return <p className="muted">No changes yet.</p>
  return (
    <ul className="changes" aria-label="Changes">
      {changes.map((c) => {
        const diff = diffOf(c)
        return (
          <li key={c.kind + c.path}>
            <details>
              <summary>{label[c.kind]} <code>{c.from ? `${c.from} → ${c.path}` : c.path}</code></summary>
              {c.kind !== 'deleted' && <button className="ghost small" onClick={() => onOpen(c.path)}>Open</button>}
              {diff === undefined ? <p className="muted">Loading the original…</p> : <DiffView diff={diff} />}
            </details>
          </li>
        )
      })}
    </ul>
  )
}
```

`GoToFile.tsx`:

```tsx
import { useState } from 'react'
import { matchFiles } from './model'

// GoToFile is Ctrl/Cmd+P: type part of a path, Enter opens the first match, arrows move, Escape closes.
export function GoToFile({ paths, onPick, onClose }: { paths: string[]; onPick: (p: string) => void; onClose: () => void }) {
  const [q, setQ] = useState('')
  const [i, setI] = useState(0)
  const hits = matchFiles(paths, q).slice(0, 50)
  const key = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') onClose()
    else if (e.key === 'ArrowDown') { e.preventDefault(); setI((x) => Math.min(x + 1, hits.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setI((x) => Math.max(x - 1, 0)) }
    else if (e.key === 'Enter' && hits[i]) onPick(hits[i])
  }
  return (
    <div className="goto" role="dialog" aria-modal="true" aria-label="Go to file">
      <input autoFocus aria-label="File name" aria-controls="goto-list" aria-activedescendant={hits[i] ? `goto-${i}` : undefined}
        value={q} onChange={(e) => { setQ(e.target.value); setI(0) }} onKeyDown={key} />
      <ul id="goto-list" role="listbox">
        {hits.map((p, n) => <li key={p} id={`goto-${n}`} role="option" aria-selected={n === i} onMouseDown={() => onPick(p)}>{p}</li>)}
      </ul>
    </div>
  )
}
```

- [ ] **Step 3: Wire them into `Ide.tsx`**

Add these state and effects, using the names Task 10 defined:

```tsx
  const VALIDATE_MS = 1000
  const [problems, setProblems] = useState<Problem[]>([])
  const [panel, setPanel] = useState<'explorer' | 'problems' | 'changes'>('explorer')
  const [reveal, setReveal] = useState<{ line: number; n: number }>()
  const [goto, setGoto] = useState(false)
  const [view, setView] = useState<'files' | 'editor' | 'preview'>('editor') // below tablet width only

  useEffect(() => { // problems about a second after typing stops; a 409 means a check is still running, and the next change re-runs it
    if (!draft || problem) return
    const t = setTimeout(() => {
      api<{ problems: Problem[] }>('/api/authoring/validate', { method: 'POST', json: { training: draft.training, base_sha: draft.base_sha, ops } })
        .then((r) => setProblems(r.problems))
        .catch((e: ApiError) => { if (e.status !== 409) setMsg(e.message) })
    }, VALIDATE_MS)
    return () => clearTimeout(t)
  }, [draft?.training, draft?.base_sha, ops, problem]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { // Ctrl/Cmd+P outside the editor too (Monaco handles it inside)
    const k = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 'p') { e.preventDefault(); setGoto(true) }
    }
    window.addEventListener('keydown', k)
    return () => window.removeEventListener('keydown', k)
  }, [])

  const jump = async (p: Problem) => { await open(p.file); setReveal({ line: p.line, n: Date.now() }) }
  const diffOf = (c: Change): string | undefined => {
    const o = c.kind === 'added' ? undefined : (c.from ?? c.path)
    if (o !== undefined && !(o in baseText)) { loadBase(o).catch(() => {}); return undefined }
    const before = o === undefined ? '' : baseText[o]
    return unifiedDiff(o, c.kind === 'deleted' ? undefined : c.path, before, c.kind === 'deleted' ? '' : textOf(c.path))
  }
```

Replace the explorer column with an activity bar plus the selected panel:

```tsx
        <div ref={explorerRef} className="ide-side">
          <div role="toolbar" aria-label="Panels" className="ide-activity">
            <button aria-pressed={panel === 'explorer'} onClick={() => setPanel('explorer')}>Explorer</button>
            <button aria-pressed={panel === 'problems'} onClick={() => setPanel('problems')}>Problems ({problems.length})</button>
            <button aria-pressed={panel === 'changes'} onClick={() => setPanel('changes')}>Changes ({changes.length})</button>
          </div>
          {panel === 'explorer' && <Explorer … (as before) />}
          {panel === 'problems' && <ProblemsPanel problems={problems} known={(f) => paths.includes(f)} onJump={jump} />}
          {panel === 'changes' && <ChangesPanel changes={changes} diffOf={diffOf} onOpen={open} />}
        </div>
```

Pass `markers={problems.filter((p) => p.file === active).map((p) => ({ line: p.line, message: p.msg }))}`, `reveal={reveal}` and `onGoToFile={() => setGoto(true)}` to `CodeEditor`. Render `{goto && <GoToFile paths={paths} onPick={(p) => { setGoto(false); open(p) }} onClose={() => setGoto(false)} />}`. Add `<span>{problems.length} problems</span>` to the status bar.

Narrow screens: add `data-view={view}` to `.ide-main`, and before it:

```tsx
      <div role="tablist" aria-label="Editor views" className="ide-views">
        {(['files', 'editor', 'preview'] as const).map((v) => <button key={v} role="tab" aria-selected={view === v} onClick={() => setView(v)}>{v[0].toUpperCase() + v.slice(1)}</button>)}
      </div>
```

Give the three columns the classes `ide-side`, `ide-editor`, `ide-preview`. In `app.css`:

```css
.ide-views { display: none; }
.ide-activity { display: flex; gap: .25rem; flex-wrap: wrap; margin-bottom: .5rem; }
.goto { position: fixed; top: 15vh; left: 50%; transform: translateX(-50%); width: min(600px, 90vw); background: var(--surface); border: 1px solid var(--accent); padding: .5rem; z-index: 20; }
.goto input { width: 100%; }
.goto [aria-selected='true'] { background: var(--surface-2); }
@media (max-width: 900px) {
  .ide-views { display: flex; gap: .25rem; }
  .ide-main { grid-template-columns: 1fr; }
  .ide-main[data-view='files'] > :not(.ide-side), .ide-main[data-view='editor'] > :not(.ide-editor), .ide-main[data-view='preview'] > :not(.ide-preview) { display: none; }
}
```

Imports to add to `Ide.tsx`: `Problem` from types; `ProblemsPanel`, `ChangesPanel`, `GoToFile`; `unifiedDiff`; `type Change` from `./model`.

- [ ] **Step 4: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint`
Expected: PASS, and check-chunks is still clean.

- [ ] **Step 5: Update docs/user in the same commit**

Add these sections to `docs/user/authors/editing-content.md`:
- **Problems**: checked about a second after you stop typing, with the same rules as `crucible lint`. Squiggles in the file. Click a problem to jump to its line.
- **Changes**: what a reviewer will see, hidden characters marked.
- **Keyboard**: Ctrl/Cmd+P goes to a file. Ctrl/Cmd+Shift+P opens the command palette. Ctrl/Cmd+F and Ctrl/Cmd+H find and replace. Ctrl+Shift+F6 or Ctrl+Alt+↑ leaves the editor. Every panel is reachable with Tab.
- **Small screens**: Files, Editor and Preview become tabs.

- [ ] **Step 6: Commit**

```bash
git add -- web/src/pages/editor/ProblemsPanel.tsx web/src/pages/editor/ChangesPanel.tsx web/src/pages/editor/GoToFile.tsx web/src/pages/editor/panels.test.tsx
git commit -m "feat(web): editor problems with squiggles, changes with the reviewers' diff, go to file, palette, small-screen tabs" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/editor web/src/theme/app.css docs/user/authors/editing-content.md
```

---

### Task 12: Preview: readings, a playable quiz with answers marked, a lab's tasks, terminals and hint costs

**Files:**
- Create: `web/src/pages/editor/preview.ts`, `web/src/pages/editor/preview.test.ts`, `web/src/pages/editor/Preview.test.tsx`
- Modify: `web/src/pages/editor/Preview.tsx`, `web/src/pages/editor/Ide.tsx` (load the files a lab preview reads), `web/package.json` (+`yaml`), `docs/user/authors/editing-content.md`

**Interfaces:**
- Produces:
  - `parseQuiz(text): { quiz?: QuizView; error?: string }`
  - `QuizView = { passThreshold: number; questions: QuestionView[] }`
  - `QuestionView = { id; type; prompt; options: string[]; pairs: string[][]; answer: unknown; caseSensitive: boolean; points: number; rubric?: string }`
  - `grade(q: QuestionView, response: unknown): boolean | undefined` (`undefined` means not scored in the browser)
  - `parseLab(text): { lab?: LabView; error?: string }`
  - `LabView = { runtime; terminals: {name; service}[]; tasks: TaskView[] }`
  - `TaskView = { id; instructions; points; kind: 'check' | 'quiz' | 'review'; hints: { label: string; cost: number }[] }`
  - `Preview({ path, text, read })`. `read(path)` returns a draft file's text, or `undefined` when it isn't loaded or doesn't exist.

- [ ] **Step 1: Install `yaml`, then write the failing tests**

Run: `cd web && npm install yaml@2.9.1`

Before writing `grade`, read `internal/learn/quiz.go`. Note how the server compares exact (trim? case?), regex (anchored, case flag) and order/match answers, and make `grade` and these tests match it exactly. The expectations below assume trim plus case-insensitive by default, and an anchored regex.

`web/src/pages/editor/preview.test.ts`:

```ts
import { expect, test } from 'vitest'
import { grade, parseLab, parseQuiz } from './preview'

const quiz = `pass_threshold: 0.5
questions:
  - {id: s, type: single, prompt: Hot?, options: ["no", "yes"], answer: 1}
  - {id: m, type: multi, prompt: Which?, options: [a, b, c], answer: [0, 2]}
  - {id: e, type: exact, prompt: Port?, answer: "80"}
  - {id: r, type: regex, prompt: Version?, answer: '\\d+\\.\\d+'}
  - {id: o, type: order, prompt: Order?, options: [one, two]}
  - {id: x, type: match, prompt: Match?, pairs: [[a, "1"], [b, "2"]]}
  - {id: t, type: text, prompt: Explain, rubric: Mentions heat}
`

test('a quiz parses, with defaults', () => {
  const { quiz: q } = parseQuiz(quiz)
  expect(q?.passThreshold).toBe(0.5)
  expect(q?.questions.map((x) => x.points)).toEqual([1, 1, 1, 1, 1, 1, 1])
  expect(parseQuiz('questions: [').error).toBeTruthy()
})

test('answers are graded the way the server grades them', () => {
  const qs = parseQuiz(quiz).quiz!.questions
  expect(grade(qs[0], 1)).toBe(true)
  expect(grade(qs[0], 0)).toBe(false)
  expect(grade(qs[1], [2, 0])).toBe(true)
  expect(grade(qs[1], [0])).toBe(false)
  expect(grade(qs[2], ' 80 ')).toBe(true)
  expect(grade(qs[3], '1.2')).toBe(true)
  expect(grade(qs[3], 'v1.2')).toBe(false)
  expect(grade(qs[4], ['one', 'two'])).toBe(true)
  expect(grade(qs[5], { a: '1', b: '2' })).toBe(true)
  expect(grade(qs[6], 'anything')).toBeUndefined()
})

test('a lab lists tasks, kinds and effective hint costs', () => {
  const { lab } = parseLab(`id: l
runtime: local
hint_cost: 0.25
terminals: [{name: shell, service: shell}]
tasks:
  - {id: t1, instructions: tasks/1.md, check: {script: c.sh, run_in: shell}, points: 2, hints: [{text: Try echo}, {file: hints/h.md, cost: 1}]}
  - {id: t2, instructions: tasks/2.md, human_review: true, rubric: R}
`)
  expect(lab?.terminals).toEqual([{ name: 'shell', service: 'shell' }])
  expect(lab?.tasks.map((t) => t.kind)).toEqual(['check', 'review'])
  expect(lab?.tasks[0].hints.map((h) => h.cost)).toEqual([0.25, 1])
  expect(lab?.tasks[1].points).toBe(1)
})
```

`web/src/pages/editor/Preview.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Preview } from './Preview'

test('a quiz preview marks the right answers (only editors ever see it)', () => {
  const html = renderToStaticMarkup(<Preview path="modules/m1/quiz.yaml" read={() => undefined}
    text={'questions:\n  - {id: s, type: single, prompt: Hot?, options: ["no", "yes"], answer: 1}\n'} />)
  expect(html).toContain('Hot?')
  expect(html).toMatch(/yes[^<]*<\/span>\s*<span[^>]*>\s*✓ right answer/)
})

test('a lab preview shows tasks, terminals and hint costs; nothing runs', () => {
  const html = renderToStaticMarkup(<Preview path="modules/m1/lab/lab.yaml" read={(p) => (p === 'modules/m1/lab/tasks/1.md' ? '# Fix it' : undefined)}
    text={'id: l\nruntime: local\nterminals: [{name: shell, service: shell}]\ntasks:\n  - {id: t1, instructions: tasks/1.md, check: {script: c.sh, run_in: shell}, hints: [{text: Try, cost: 0.5}]}\n'} />)
  expect(html).toContain('shell')
  expect(html).toContain('t1')
  expect(html).toContain('costs 0.5')
})

test('a broken file shows the parse error instead of a preview', () => {
  expect(renderToStaticMarkup(<Preview path="modules/m1/quiz.yaml" read={() => undefined} text="questions: [" />)).toContain('role="alert"')
})
```

The Markdown component is lazy, so `renderToStaticMarkup` renders its Suspense fallback. That is why the test asserts on the task id and not on `Fix it`. React's server renderer puts `<!-- -->` between adjacent text expressions, so any text a test matches as one string is built as one template literal in the JSX (`{`Hint ${i + 1}: …`}`).

Run: `cd web && npx vitest run src/pages/editor/preview.test.ts src/pages/editor/Preview.test.tsx`. Expected: FAIL.

- [ ] **Step 2: Implement `preview.ts`**

```ts
import { parse } from 'yaml'

export type QuestionView = { id: string; type: string; prompt: string; options: string[]; pairs: string[][]; answer: unknown; caseSensitive: boolean; points: number; rubric?: string }
export type QuizView = { passThreshold: number; questions: QuestionView[] }
export type TaskView = { id: string; instructions: string; points: number; kind: 'check' | 'quiz' | 'review'; hints: { label: string; cost: number }[] }
export type LabView = { runtime: string; terminals: { name: string; service: string }[]; tasks: TaskView[] }

type Raw = Record<string, unknown>
const read = (text: string): { doc?: Raw; error?: string } => {
  try { return { doc: (parse(text) ?? {}) as Raw } } catch (e) { return { error: (e as Error).message } }
}
const arr = <T,>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : [])

export function parseQuiz(text: string): { quiz?: QuizView; error?: string } {
  const { doc, error } = read(text)
  if (!doc) return { error }
  return {
    quiz: {
      passThreshold: Number(doc.pass_threshold ?? 0.8),
      questions: arr<Raw>(doc.questions).map((q) => ({
        id: String(q.id ?? ''), type: String(q.type ?? ''), prompt: String(q.prompt ?? ''), options: arr<unknown>(q.options).map(String),
        pairs: arr<unknown[]>(q.pairs).map((p) => arr<unknown>(p).map(String)), answer: q.answer, caseSensitive: q.case_sensitive === true,
        points: Number(q.points ?? 1) || 1, rubric: q.rubric === undefined ? undefined : String(q.rubric),
      })),
    },
  }
}

// grade mirrors internal/learn/quiz.go for the instantly scored types; undefined = scored in the lab or by a person.
export function grade(q: QuestionView, response: unknown): boolean | undefined {
  const norm = (s: unknown) => (q.caseSensitive ? String(s).trim() : String(s).trim().toLowerCase())
  switch (q.type) {
    case 'single': return response === q.answer
    case 'multi': return JSON.stringify([...arr<number>(response)].sort()) === JSON.stringify([...arr<number>(q.answer)].sort())
    case 'exact': return norm(response) === norm(q.answer)
    case 'regex': try { return new RegExp(`^(?:${String(q.answer)})$`, q.caseSensitive ? '' : 'i').test(String(response).trim()) } catch { return false }
    case 'order': return JSON.stringify(response) === JSON.stringify(q.options)
    case 'match': return q.pairs.every(([l, r]) => (response as Record<string, string>)?.[l] === r)
  }
  return undefined
}

export function parseLab(text: string): { lab?: LabView; error?: string } {
  const { doc, error } = read(text)
  if (!doc) return { error }
  const hintCost = Number(doc.hint_cost ?? 0)
  return {
    lab: {
      runtime: String(doc.runtime ?? ''),
      terminals: arr<Raw>(doc.terminals).map((t) => ({ name: String(t.name ?? ''), service: String(t.service ?? '') })),
      tasks: arr<Raw>(doc.tasks).map((t) => ({
        id: String(t.id ?? ''), instructions: String(t.instructions ?? ''), points: Number(t.points ?? 1) || 1,
        kind: t.human_review === true ? 'review' : t.quiz ? 'quiz' : 'check',
        hints: arr<Raw>(t.hints).map((h) => ({ label: h.file ? `File ${String(h.file)}` : String(h.text ?? ''), cost: h.cost === undefined ? hintCost : Number(h.cost) })),
      })),
    },
  }
}
```

- [ ] **Step 3: Implement `Preview.tsx`**

```tsx
import { useState } from 'react'
import { Markdown } from '../../components/Markdown'
import { grade, parseLab, parseQuiz, type QuestionView } from './preview'

const dir = (p: string) => p.slice(0, p.lastIndexOf('/') + 1)

// Preview renders the open file the way trainees will see it. Nothing executes: no checks, no labs.
export function Preview({ path, text, read }: { path: string; text: string; read: (p: string) => string | undefined }) {
  if (path.endsWith('.md')) return <Markdown text={text} />
  if (path.endsWith('/quiz.yaml')) return <QuizPreview text={text} />
  if (path.endsWith('/lab.yaml')) return <LabPreview text={text} labDir={dir(path)} read={read} />
  return <pre>{text}</pre>
}

function QuizPreview({ text }: { text: string }) {
  const { quiz, error } = parseQuiz(text)
  if (!quiz) return <p role="alert" className="error">Can't preview: {error}</p>
  return (
    <div className="quiz-preview">
      <p className="muted">Pass at {Math.round(quiz.passThreshold * 100)}%. Right answers are marked: only editors see this.</p>
      {quiz.questions.map((q) => <QuestionPreview key={q.id} q={q} />)}
    </div>
  )
}

function QuestionPreview({ q }: { q: QuestionView }) {
  const [resp, setResp] = useState<unknown>(q.type === 'multi' ? [] : '')
  const [checked, setChecked] = useState<boolean>()
  const right = (i: number) => (q.type === 'single' ? q.answer === i : q.type === 'multi' && Array.isArray(q.answer) && q.answer.includes(i))
  return (
    <fieldset>
      <legend>{q.id} · {q.type} · {q.points} pt</legend>
      <Markdown text={q.prompt} />
      {(q.type === 'single' || q.type === 'multi') && q.options.map((o, i) => (
        <label key={i} style={{ display: 'block' }}>
          <input type={q.type === 'single' ? 'radio' : 'checkbox'} name={q.id}
            onChange={(e) => setResp(q.type === 'single' ? i : e.target.checked ? [...(resp as number[]), i] : (resp as number[]).filter((x) => x !== i))} />
          <span>{o}</span>{right(i) && <span className="pass"> ✓ right answer</span>}
        </label>
      ))}
      {(q.type === 'exact' || q.type === 'regex') && (
        <>
          <input aria-label={`Answer to ${q.id}`} value={String(resp)} onChange={(e) => setResp(e.target.value)} />
          <span className="pass"> ✓ {q.type === 'exact' ? `"${String(q.answer)}"` : `matches /${String(q.answer)}/`}</span>
        </>
      )}
      {q.type === 'order' && <p className="pass">✓ Right order: {q.options.join(' → ')} (trainees see them shuffled)</p>}
      {q.type === 'match' && <p className="pass">✓ Right pairs: {q.pairs.map((p) => p.join(' = ')).join(', ')}</p>}
      {q.type === 'terminal' && <p className="muted">Answered in the lab: a task with quiz: {q.id} runs its check.</p>}
      {['text', 'upload', 'signoff'].includes(q.type) && <p className="muted">Scored by a person on the Anvil.{q.rubric && ` Rubric: ${q.rubric}`}</p>}
      {['single', 'multi', 'exact', 'regex'].includes(q.type) && (
        <p><button className="ghost" onClick={() => setChecked(grade(q, resp))}>Try it</button>{' '}
          {checked !== undefined && <span role="status" className={checked ? 'pass' : 'error'}>{checked ? 'Right' : 'Not right'}</span>}</p>
      )}
    </fieldset>
  )
}

function LabPreview({ text, labDir, read }: { text: string; labDir: string; read: (p: string) => string | undefined }) {
  const { lab, error } = parseLab(text)
  if (!lab) return <p role="alert" className="error">Can't preview: {error}</p>
  return (
    <div className="lab-preview">
      <p className="muted">{lab.runtime} lab · terminals: {lab.terminals.map((t) => `${t.name} (${t.service})`).join(', ')}. Nothing runs in the preview; use crucible preview to try it.</p>
      <ol>
        {lab.tasks.map((t) => (
          <li key={t.id}>
            <strong>{t.id}</strong> · {t.points} pt · {t.kind === 'review' ? 'scored by a person' : t.kind === 'quiz' ? 'answered by a terminal question' : 'checked by a script'}
            {read(labDir + t.instructions) !== undefined ? <Markdown text={read(labDir + t.instructions)!} /> : <p className="muted">{t.instructions}</p>}
            {t.hints.length > 0 && <ul>{t.hints.map((h, i) => <li key={i}>{`Hint ${i + 1}: ${h.label} (costs ${h.cost} pt)`}</li>)}</ul>}
          </li>
        ))}
      </ol>
    </div>
  )
}
```

In `Ide.tsx`, the `read` passed to `Preview` must also trigger loading of files the preview needs. When `active` is a `lab.yaml`, load each task's instructions file in an effect:

```tsx
  useEffect(() => {
    if (!active.endsWith('/lab.yaml')) return
    const labDir = active.slice(0, active.lastIndexOf('/') + 1)
    for (const t of parseLab(textOf(active)).lab?.tasks ?? []) {
      const p = labDir + t.instructions
      if (paths.includes(p)) loadBase(origin(base, work, p)).catch(() => {})
    }
  }, [active, work, baseText]) // eslint-disable-line react-hooks/exhaustive-deps
```

- [ ] **Step 4: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint`
Expected: PASS.

- [ ] **Step 5: Update docs/user in the same commit**

Add a **Preview** section to `docs/user/authors/editing-content.md`:
- Readings render as trainees see them.
- `quiz.yaml` becomes a playable quiz with the right answers marked. Only editors ever see it, and they can read the files anyway.
- `lab.yaml` shows its tasks, terminals and hint costs.
- Nothing runs: try a lab with `crucible preview` (link `/docs/authors/lint-and-preview`).

- [ ] **Step 6: Commit**

```bash
git add -- web/src/pages/editor/preview.ts web/src/pages/editor/preview.test.ts web/src/pages/editor/Preview.test.tsx
git commit -m "feat(web): editor preview for readings, playable quizzes with answers marked, and labs with hint costs" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/editor web/package.json web/package-lock.json docs/user/authors/editing-content.md
```

---

### Task 13: Rebase in the editor (diff editor for conflicts) and reopening returned edits

**Files:**
- Create: `web/src/pages/editor/rebase.ts`, `web/src/pages/editor/rebase.test.ts`, `web/src/pages/editor/RebasePanel.tsx`
- Modify: `web/src/pages/editor/Ide.tsx`, `web/src/pages/EditReview.tsx`, `web/src/types.ts`, `docs/user/authors/review-and-merge.md`, `docs/user/authors/editing-content.md`

**Interfaces:**
- Consumes: `POST /api/authoring/drafts/{id}/rebase` returns `{draft, conflicts: Conflict[]}` (Task 8); `PUT` draft with `base_sha = head`.
- Produces:
  - TS `type Conflict = { path: string; base: string; head: string; head_missing: boolean; mine: string; op: EditOp }`
  - `type Choice = 'drop' | 'keep' | { put: string }`
  - `resolveOps(ops: EditOp[], conflicts: Conflict[], choices: Choice[]): EditOp[]`
  - `canKeep(c: Conflict): boolean`

- [ ] **Step 1: Write the failing tests**

`web/src/pages/editor/rebase.test.ts`:

```ts
import { expect, test } from 'vitest'
import { canKeep, resolveOps } from './rebase'
import type { Conflict } from '../../types'

const put = { op: 'put' as const, path: 'm/a.md', content: 'mine' }
const del = { op: 'delete' as const, path: 'm/b.md' }
const ren = { op: 'rename' as const, from: 'm/c.md', to: 'm/d.md' }
const c = (op: Conflict['op'], path: string, head_missing = false): Conflict => ({ path, base: 'old', head: head_missing ? '' : 'theirs', head_missing, mine: op.op === 'put' ? op.content : '', op })

test('each conflict is kept, dropped or replaced by the merged text; untouched ops stay', () => {
  const other = { op: 'put' as const, path: 'm/z.md', content: 'z' }
  const ops = [ren, del, put, other]
  const conflicts = [c(put, 'm/a.md'), c(del, 'm/b.md'), c(ren, 'm/c.md')]
  expect(resolveOps(ops, conflicts, [{ put: 'merged' }, 'drop', 'keep'])).toEqual([ren, { ...put, content: 'merged' }, other])
})

test('a rename or delete of a file that is gone upstream can only be dropped', () => {
  expect(canKeep(c(del, 'm/b.md', true))).toBe(false)
  expect(canKeep(c(ren, 'm/c.md', true))).toBe(false)
  expect(canKeep(c(put, 'm/a.md', true))).toBe(true) // keeping recreates the file
  expect(canKeep({ ...c(ren, 'm/d.md'), head_missing: false, base: '' })).toBe(false) // the target appeared upstream
})
```

Run: `cd web && npx vitest run src/pages/editor/rebase.test.ts`. Expected: FAIL.

- [ ] **Step 2: Implement `rebase.ts` and the `Conflict` type**

`types.ts`: `export type Conflict = { path: string; base: string; head: string; head_missing: boolean; mine: string; op: EditOp }`

`rebase.ts`:

```ts
import type { Conflict, EditOp } from '../../types'

export type Choice = 'drop' | 'keep' | { put: string }

const same = (a: EditOp, b: EditOp) => JSON.stringify(a) === JSON.stringify(b)

// canKeep: a put can always be kept (it recreates a file deleted upstream); a rename or delete needs its source to
// still exist upstream, and a rename can't land on a path that appeared upstream.
export function canKeep(c: Conflict): boolean {
  if (c.op.op === 'put') return true
  if (c.head_missing) return false
  return !(c.op.op === 'rename' && c.path === c.op.to)
}

// resolveOps applies the author's choice for each conflict to the draft's ops; ops without a conflict stay as they are.
export function resolveOps(ops: EditOp[], conflicts: Conflict[], choices: Choice[]): EditOp[] {
  return ops.flatMap((op) => {
    const i = conflicts.findIndex((c) => same(c.op, op))
    if (i < 0) return [op]
    const ch = choices[i]
    if (ch === 'drop') return []
    if (ch === 'keep') return [op]
    return op.op === 'put' ? [{ ...op, content: ch.put }] : [op]
  })
}
```

- [ ] **Step 3: `RebasePanel.tsx` (Monaco diff editor) and the rebase flow in `Ide.tsx`**

```tsx
import { useEffect, useRef, useState } from 'react'
import { monaco } from './monaco'
import { languageOf, monacoTheme } from './model'
import { canKeep, type Choice } from './rebase'
import type { Conflict } from '../../types'

// RebasePanel resolves files changed both in the draft and upstream. Left: the file now; right: yours, editable.
export function RebasePanel({ conflicts, onDone, onCancel }: { conflicts: Conflict[]; onDone: (choices: Choice[]) => void; onCancel: () => void }) {
  const [choices, setChoices] = useState<(Choice | undefined)[]>(conflicts.map(() => undefined))
  const [i, setI] = useState(0)
  const host = useRef<HTMLDivElement>(null)
  const diff = useRef<monaco.editor.IStandaloneDiffEditor | null>(null)
  const c = conflicts[i]
  useEffect(() => {
    if (!host.current || c.op.op !== 'put') return
    const d = monaco.editor.createDiffEditor(host.current, { automaticLayout: true, originalEditable: false, renderSideBySide: true, theme: monacoTheme(document.documentElement.dataset.theme), accessibilitySupport: 'on' })
    d.setModel({ original: monaco.editor.createModel(c.head, languageOf(c.path)), modified: monaco.editor.createModel(c.mine, languageOf(c.path)) })
    diff.current = d
    return () => { const m = d.getModel(); d.dispose(); m?.original.dispose(); m?.modified.dispose() }
  }, [c])
  const choose = (ch: Choice) => {
    const next = choices.map((x, n) => (n === i ? ch : x))
    setChoices(next)
    if (next.every((x) => x !== undefined)) onDone(next as Choice[])
    else setI(next.findIndex((x) => x === undefined))
  }
  const what = c.op.op === 'put' ? (c.head_missing ? 'You changed it; it was deleted or moved upstream.' : 'Changed both here and upstream.')
    : c.op.op === 'delete' ? 'You deleted it; it changed upstream.' : c.path === c.op.to ? `You renamed ${c.op.from} to it; it now exists upstream.` : `You renamed it to ${c.op.to}; it changed upstream.`
  return (
    <section className="rebase" aria-label="Resolve newer content">
      <h2>Newer content: {i + 1} of {conflicts.length}</h2>
      <p><code>{c.path}</code>: {what}</p>
      {c.op.op === 'put' && <div ref={host} className="code-editor" aria-label={`Upstream on the left, your version on the right, editable: ${c.path}`} />}
      <p>
        {c.op.op === 'put' && <button onClick={() => choose({ put: diff.current?.getModel()?.modified.getValue() ?? c.mine })}>Use the right side</button>}{' '}
        {c.op.op !== 'put' && <button disabled={!canKeep(c)} title={canKeep(c) ? '' : 'The file it acts on is gone upstream'} onClick={() => choose('keep')}>Keep my change</button>}{' '}
        <button className="ghost" onClick={() => choose('drop')}>Drop my change (take theirs)</button>{' '}
        <button className="ghost" onClick={onCancel}>Cancel</button>
      </p>
    </section>
  )
}
```

In `Ide.tsx`:

```tsx
  const [conflicts, setConflicts] = useState<Conflict[]>()
  const rebase = async () => {
    setMsg('')
    if (save.kind !== 'saved' && !(await saveNow())) return
    try {
      const r = await api<{ draft: DraftInfo; conflicts: Conflict[] }>(`/api/authoring/drafts/${id}/rebase`, { method: 'POST', json: {} })
      if (r.conflicts.length === 0) { await load(); setMsg('Rebased onto the newest content.') } else setConflicts(r.conflicts)
    } catch (e) { setMsg((e as Error).message) }
  }
  const resolved = async (choices: Choice[]) => {
    if (!draft || !conflicts) return
    try {
      await api<DraftInfo>(`/api/authoring/drafts/${id}`, { method: 'PUT', json: { title, base_sha: draft.head_sha, ops: resolveOps(draft.ops, conflicts, choices), updated_at: draft.updated_at } })
      setConflicts(undefined)
      await load() // new base: new file list, base texts reloaded on demand
      setMsg('Rebased onto the newest content.')
    } catch (e) { setMsg((e as Error).message) }
  }
```

In the header, replace the "Newer content is on the branch." note with `<button onClick={rebase}>Newer content: rebase draft</button>`. When `conflicts` is set, render `<RebasePanel conflicts={conflicts} onDone={resolved} onCancel={() => setConflicts(undefined)} />` in place of `.ide-main`.

- [ ] **Step 4: Reopening a returned edit from its review page**

In `EditReview.tsx`, fetch `useFetch<DraftInfo[]>(me.can_edit_content ? '/api/authoring/drafts' : null)`. For the author of a `rejected`, `withdrawn` or `stale` edit:
- If a draft has `edit_id === e.id`, link to `/edits/drafts/${draft.id}` with the text **Reopen in the editor**.
- Otherwise, keep the Task 10 link to `/edits/new?training=…&from=${e.id}`.

- [ ] **Step 5: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint`
Expected: PASS.

- [ ] **Step 6: Update docs/user in the same commit**

In `docs/user/authors/editing-content.md`, add **Newer content**. When the training moved on while you worked, the editor offers **Newer content: rebase draft**. Files you didn't touch update silently. For a file changed on both sides, you see the newest version on the left and yours on the right. Edit the right side and use it, or drop your change.

In `docs/user/authors/review-and-merge.md`, add **Returned edits**. A rejected, withdrawn or stale edit reopens in the editor from its page, with your draft as you left it. A merged draft disappears from your list.

- [ ] **Step 7: Commit**

```bash
git add -- web/src/pages/editor/rebase.ts web/src/pages/editor/rebase.test.ts web/src/pages/editor/RebasePanel.tsx
git commit -m "feat(web): rebase drafts onto newer content with a diff editor for conflicts; reopen returned edits" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/editor web/src/pages/EditReview.tsx web/src/types.ts docs/user/authors
```

---
## Delivery step 5: the Blocks catalog

### Task 14: `yamlx.Insert`: append to a YAML list or set a key without reformatting the file

**Files:**
- Modify: `internal/yamlx/yamlx.go`, `internal/yamlx/yamlx_test.go`

**Interfaces:**
- Produces: `func Insert(src []byte, path []string, item string, set bool) (out []byte, line int, err error)`. `line` is the 1-based line where the inserted entry starts.

- [ ] **Step 1: Write the failing tests**

Append to `internal/yamlx/yamlx_test.go`:

```go
func TestInsertAppendsWithoutTouchingTheRest(t *testing.T) {
	src := "# Quiz for module 1\npass_threshold: 0.8   # keep it high\n\nquestions:\n  - id: q1   # the first\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\", \"yes\"]\n    answer: 1\n\n# trailing comment\n"
	out, line, err := Insert([]byte(src), []string{"questions"}, "id: q2\ntype: exact\nprompt: \"Port?\"\nanswer: \"80\"\n", false)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, "    answer: 1\n", "    answer: 1\n  - id: q2\n    type: exact\n    prompt: \"Port?\"\n    answer: \"80\"\n", 1)
	if string(out) != want || line != 10 {
		t.Fatalf("line %d:\n%s", line, out)
	}
}

func TestInsertFlowListsNestedListsAndKeys(t *testing.T) {
	out, line, err := Insert([]byte("id: t\nmodules: [01-welcome]  # order matters\n"), []string{"modules"}, "02-heat", false)
	if err != nil || string(out) != "id: t\nmodules: [01-welcome, 02-heat]  # order matters\n" || line != 2 {
		t.Fatalf("flow: %q %d %v", out, line, err)
	}
	if out, _, _ := Insert([]byte("modules: []\n"), []string{"modules"}, "01-a", false); string(out) != "modules: [01-a]\n" {
		t.Fatalf("empty flow: %q", out)
	}
	lab := "id: l\ntasks:\n  - id: t1\n    instructions: tasks/1.md\n    check: { script: c.sh, run_in: shell }\n  - id: t2\n    instructions: tasks/2.md\n    hints:\n"
	out, line, err = Insert([]byte(lab), []string{"tasks", "id=t1", "hints"}, "text: \"Try echo\"", false)
	if err != nil || line != 7 || !strings.Contains(string(out), "    check: { script: c.sh, run_in: shell }\n    hints:\n      - text: \"Try echo\"\n  - id: t2\n") {
		t.Fatalf("a missing nested list is created at the end of its mapping: %d %v\n%s", line, err, out)
	}
	out, line, err = Insert([]byte(lab), []string{"tasks", "id=t2", "hints"}, "text: a", false)
	if err != nil || line != 9 || !strings.HasSuffix(string(out), "    hints:\n      - text: a\n") {
		t.Fatalf("an empty key gets its first entry: %d %v\n%s", line, err, out)
	}
	out, _, err = Insert([]byte(lab), []string{"setup"}, "script: setup/lab.sh\nrun_in: shell", true)
	if err != nil || !strings.HasSuffix(string(out), "    hints:\nsetup:\n  script: setup/lab.sh\n  run_in: shell\n") {
		t.Fatalf("set a missing key: %v\n%s", err, out)
	}
	if _, _, err := Insert([]byte(lab), []string{"id"}, "x", true); err == nil {
		t.Fatal("set refuses a key that exists")
	}
	if out, line, err := Insert(nil, []string{"questions"}, "id: q", false); err != nil || string(out) != "questions:\n  - id: q\n" || line != 2 {
		t.Fatalf("empty file: %q %d %v", out, line, err)
	}
}

func TestInsertKeepsCRLF(t *testing.T) {
	out, _, err := Insert([]byte("questions:\r\n  - id: q1\r\n"), []string{"questions"}, "id: q2", false)
	if err != nil || string(out) != "questions:\r\n  - id: q1\r\n  - id: q2\r\n" {
		t.Fatalf("%q %v", out, err)
	}
}

// Review Focus 4: inserting into a file the author left broken says so instead of guessing.
func TestInsertBrokenYAML(t *testing.T) {
	for _, src := range []string{"questions:\n  - id: [\n", "questions:\n\t- id: q\n"} {
		if _, _, err := Insert([]byte(src), []string{"questions"}, "id: q2", false); err == nil || !strings.Contains(err.Error(), "fix it first") {
			t.Errorf("%q: %v", src, err)
		}
	}
	if _, _, err := Insert([]byte("questions: {a: 1}\n"), []string{"questions"}, "id: q2", false); err == nil {
		t.Error("appending to a mapping as if it were a list")
	}
	if _, _, err := Insert([]byte("tasks:\n  - id: t1\n"), []string{"tasks", "id=nope", "hints"}, "text: a", false); err == nil {
		t.Error("an unknown task")
	}
}
```

Run: `go test ./internal/yamlx -run Insert`. Expected: build failure.

- [ ] **Step 2: Implement `Insert`**

```go
// Insert adds item to the YAML text src and returns the new text and the 1-based line where item starts. Only the added
// lines change: comments, blank lines, quoting and flow styles elsewhere stay byte for byte (a re-encode would reformat
// the whole file and bury the change in the reviewer's diff). CRLF files stay CRLF.
//
// path walks mapping keys; a "key=value" segment picks the entry of a list whose mapping has key: value. Without set,
// item (YAML for one entry, without the leading "- ") is appended to the list at path, and a missing or empty last key
// is created at the end of its mapping. With set, path's last key must not exist yet and gets item as its value.
// ponytail: positions come from yaml.v3 nodes; an entry's end is its deepest node's line (block scalars counted), so a
// flow collection whose closing bracket sits alone on a later line, or a folded (>) scalar, can end up mis-placed.
func Insert(src []byte, path []string, item string, set bool) ([]byte, int, error) {
	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	var lines []string
	if text != "" {
		lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	item = strings.TrimRight(strings.ReplaceAll(item, "\r\n", "\n"), "\n")
	if len(path) == 0 {
		return nil, 0, errors.New("nothing to insert into")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, 0, fmt.Errorf("the YAML doesn't parse (%v); fix it first", err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 { // empty or comment-only file
		if len(path) != 1 {
			return nil, 0, fmt.Errorf("%s is missing", path[0])
		}
		return splice(lines, len(lines), block(0, path[0], item, set), nl), len(lines) + 2, nil
	}
	cur := doc.Content[0]
	for i, seg := range path {
		last := i == len(path)-1
		if k, v, ok := strings.Cut(seg, "="); ok {
			if cur.Kind != yaml.SequenceNode {
				return nil, 0, fmt.Errorf("%s: not a list", seg)
			}
			var hit *yaml.Node
			for _, it := range cur.Content {
				if _, val := lookup(it, k); val != nil && val.Value == v {
					hit = it
					break
				}
			}
			if hit == nil {
				return nil, 0, fmt.Errorf("no entry with %s", seg)
			}
			cur = hit
			continue
		}
		if cur.Kind != yaml.MappingNode {
			return nil, 0, fmt.Errorf("%s: not a mapping", seg)
		}
		k, v := lookup(cur, seg)
		if v == nil || (v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null") {
			if !last {
				return nil, 0, fmt.Errorf("%s is missing", seg)
			}
			if v != nil { // "hints:" with nothing after it: the entries go right under the key
				if !strings.HasSuffix(strings.TrimSpace(lines[k.Line-1]), ":") {
					return nil, 0, fmt.Errorf("%s is set to null; remove that line first", seg)
				}
				return splice(lines, k.Line, entry(k.Column-1+2, item, set), nl), k.Line + 1, nil
			}
			at := end(cur)
			return splice(lines, at, block(cur.Content[0].Column-1, seg, item, set), nl), at + 2, nil
		}
		cur = v
	}
	if set {
		return nil, 0, fmt.Errorf("%s is already set; change it in place", path[len(path)-1])
	}
	if cur.Kind != yaml.SequenceNode {
		return nil, 0, fmt.Errorf("%s is not a list", path[len(path)-1])
	}
	if cur.Style&yaml.FlowStyle != 0 {
		return flowAppend(lines, cur, item, nl)
	}
	first := lines[cur.Content[0].Line-1]
	at := end(cur)
	return splice(lines, at, entry(len(first)-len(strings.TrimLeft(first, " ")), item, false), nl), at + 1, nil
}

func lookup(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// end is the last line (1-based) n's text occupies.
func end(n *yaml.Node) int {
	last := n.Line
	if n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		last += strings.Count(strings.TrimRight(n.Value, "\n"), "\n") + 1
	}
	for _, c := range n.Content {
		last = max(last, end(c))
	}
	return last
}

// entry indents item as one list entry ("- " first) or, with set, as a mapping value.
func entry(ind int, item string, set bool) []string {
	pad := strings.Repeat(" ", ind)
	var out []string
	for i, l := range strings.Split(item, "\n") {
		switch {
		case l == "":
			out = append(out, "")
		case set:
			out = append(out, pad+l)
		case i == 0:
			out = append(out, pad+"- "+l)
		default:
			out = append(out, pad+"  "+l)
		}
	}
	return out
}

func block(ind int, key, item string, set bool) []string {
	return append([]string{strings.Repeat(" ", ind) + key + ":"}, entry(ind+2, item, set)...)
}

func splice(lines []string, at int, ins []string, nl string) []byte {
	out := slices.Concat(lines[:at], ins, lines[at:])
	return []byte(strings.Join(out, nl) + nl)
}

// flowAppend adds a one-line item to a [flow] list that opens and closes on one line.
func flowAppend(lines []string, seq *yaml.Node, item, nl string) ([]byte, int, error) {
	if strings.Contains(item, "\n") {
		return nil, 0, errors.New("a [flow] list takes one-line entries; write the list one entry per line first")
	}
	l := lines[seq.Line-1]
	start := seq.Column - 1 // the '['
	depth, quote := 0, byte(0)
	for i := start; i < len(l); i++ {
		c := l[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			if depth--; depth > 0 {
				continue
			}
			j := i
			for j > start+1 && l[j-1] == ' ' {
				j--
			}
			sep := ", "
			if strings.TrimSpace(l[start+1:i]) == "" {
				sep = ""
			}
			lines[seq.Line-1] = l[:j] + sep + item + l[i:]
			return []byte(strings.Join(lines, nl) + nl), seq.Line, nil
		}
	}
	return nil, 0, errors.New("a [flow] list spread over several lines: edit it by hand")
}
```

`lines[:at]` in `splice` shares its backing array with `lines`; `slices.Concat` copies, so this is safe.

- [ ] **Step 3: Run the tests**

Run: `go test -race ./internal/yamlx`. Expected: PASS.

If yaml.v3 reports a flow sequence's `Column` one past the `[`, find the `[` by scanning from `seq.Column-1` and adjust. The `modules: []` test catches that.

- [ ] **Step 4: Update docs/user in the same commit**

No user-visible change yet (Task 15 uses this). Confirm that `go test ./internal/docs` still passes.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(yamlx): insert list entries and keys into YAML text without reformatting it" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/yamlx
```

---

### Task 15: The catalog: blocks, starter templates, the insert endpoint, generated block pages

**Files:**
- Create: `internal/content/blocks/catalog.go`, `internal/content/blocks/catalog_test.go`, `internal/authoring/insert.go`, `internal/authoring/insert_test.go`
- Modify: `internal/authoring/http.go`, `internal/authoring/http_test.go`, `internal/docs/generated.go`, `internal/docs/coverage_test.go`, `docs/user/authors/repo-layout.md`

**Interfaces:**
- Consumes: `blocks.Fields`, `yamlx.Insert`, `gitsync.CheckOps`, `edits.Workspace`, `authoring.base`, `authoring.bounded`.
- Produces:
  - `type Block struct { ID, Group, Title, Summary, Doc, FileKind string; Fields []Field; Example string; GitOnly bool; Insert Insert; Sample map[string]string }` with JSON `id,group,title,summary,doc,file_kind,fields,example,git_only`. `Insert` and `Sample` are `json:"-"`.
  - `type Insert struct { Files map[string]string; Appends []Append; Open string; NeedsLab, GitOnly, NewRepo bool }`
  - `type Append struct { File string; Path []string; Item string; Set bool }` (all templates)
  - `var Catalog []Block`; `var Groups = []string{"Training", "Module", "Reading", "Quiz", "Lab", "AWS"}`
  - `func Find(id string) (Block, bool)`
  - `type Result struct { Ops []gitsync.Op; Open string; Line, Lines int }` with JSON `ops,open,line,lines`
  - `func Apply(dir, id string, values map[string]string) (*Result, error)`. GitOnly blocks are refused; ops pass `CheckOps`.
  - `func Plan(dir string, b Block, values map[string]string) (map[string]string, string, int, int, error)`: the changed files (path → new text), open, line, lines. No GitOnly check: tests use it to write git-only blocks straight into a fixture.
  - Authoring: `GET /api/authoring/blocks?training=` returns `{"groups": [...], "blocks": [...]}`. `POST /api/authoring/insert {training, base_sha, ops, block, values}` returns `Result`.

- [ ] **Step 1: Write the failing catalog tests**

`internal/content/blocks/catalog_test.go`:

```go
package blocks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

// fixture is a minimal valid repo: 01-welcome has a reading, a quiz (one single and one terminal question) and a
// local lab; 02-plain has only a reading.
var fixture = map[string]string{
	"training.yaml":                          "id: fx\ntitle: Fixture\nmaintainers: [m@x]\nprogression: free\nmodules: [01-welcome, 02-plain]\n",
	"modules/01-welcome/module.yaml":         "title: Welcome\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n  - lab: lab\n",
	"modules/01-welcome/reading/intro.md":    "# Intro\n\nHello.\n",
	"modules/01-welcome/quiz.yaml":           "questions:\n  - {id: q1, type: single, prompt: \"Hot?\", options: [\"no\", \"yes\"], answer: 1}\n  - {id: q-term, type: terminal, prompt: \"Prove it\", check: checks/q-term.sh}\n",
	"modules/01-welcome/lab/lab.yaml":        "id: fx-lab\nruntime: local\nterminals:\n  - {name: shell, service: shell}\ntasks:\n  - id: t1\n    instructions: tasks/t1.md\n    check: {script: checks/t1.sh, run_in: shell}\n",
	"modules/01-welcome/lab/compose.yaml":    "services:\n  shell:\n    image: alpine:3.22\n    command: [\"sleep\", \"infinity\"]\n",
	"modules/01-welcome/lab/tasks/t1.md":     "# Task 1\n",
	"modules/01-welcome/lab/checks/t1.sh":    "#!/bin/sh\nexit 0\n",
	"modules/01-welcome/lab/checks/q-term.sh": "#!/bin/sh\nexit 0\n",
	"modules/02-plain/module.yaml":           "title: Plain\nitems:\n  - reading: reading/intro.md\n",
	"modules/02-plain/reading/intro.md":      "# Plain\n",
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { // lint wants scripts executable
		if err == nil && strings.HasSuffix(p, ".sh") {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

// Every block's sample, inserted as an author would, leaves a training that loads and lints clean. Git-only blocks
// are written straight into the repo, as an author does in git.
func TestEveryBlockSampleLoadsAndLints(t *testing.T) {
	for _, b := range Catalog {
		t.Run(b.ID, func(t *testing.T) {
			dir := t.TempDir()
			if !b.Insert.NewRepo {
				writeAll(t, dir, fixture)
			}
			if b.Insert.GitOnly {
				files, _, _, _, err := Plan(dir, b, b.Sample)
				if err != nil {
					t.Fatal(err)
				}
				writeAll(t, dir, files)
			} else {
				res, err := Apply(dir, b.ID, b.Sample)
				if err != nil {
					t.Fatal(err)
				}
				if res.Open == "" || res.Line < 1 || res.Lines < 1 {
					t.Fatalf("result: %+v", res)
				}
				if _, err := gitsync.ApplyOps(dir, res.Ops); err != nil {
					t.Fatal(err)
				}
				writeAll(t, dir, nil)
			}
			if _, probs := content.Load(dir); len(probs) > 0 {
				t.Fatalf("%s leaves problems: %v", b.ID, probs)
			}
			if strings.TrimSpace(b.Example) == "" || b.Summary == "" || !slices.Contains(Groups, b.Group) {
				t.Fatalf("catalog entry incomplete: %+v", b)
			}
		})
	}
}

func TestCatalogCoversTheContentModel(t *testing.T) {
	want := []string{"template.training", "module", "template.module-quiz", "reading", "quiz", "lab.local", "lab.cluster",
		"template.lab.aws", "lab.task.check", "lab.task.setup", "lab.task.quiz", "lab.task.review", "lab.hint", "lab.hint.file",
		"lab.terminal", "lab.setup"}
	for _, typ := range questionTypes {
		want = append(want, "quiz.question."+typ)
	}
	for _, id := range want {
		if _, ok := Find(id); !ok {
			t.Errorf("no block %s", id)
		}
	}
}

// Review Focus 3: form values are text, never YAML. Quotes, colons, #, leading dashes and {{ }} stay literal, and no
// other key changes.
func TestInsertValuesAreLiteral(t *testing.T) {
	dir := t.TempDir()
	writeAll(t, dir, fixture)
	nasty := "He said: \"hot\" # not a comment\n- maintainers: [evil@x]\n{{.module}} ' ` \\"
	res, err := Apply(dir, "quiz.question.single", map[string]string{"module": "01-welcome", "id": "q-nasty", "prompt": nasty, "options": "a: b\n#x\n- y", "answer": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitsync.ApplyOps(dir, res.Ops); err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load(dir)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	q := tr.Module("01-welcome").Quiz.Question("q-nasty")
	if q.Prompt != nasty || !slices.Equal(q.Options, []string{"a: b", "#x", "- y"}) || !slices.Equal(tr.Maintainers, []string{"m@x"}) {
		t.Fatalf("prompt %q options %q maintainers %v", q.Prompt, q.Options, tr.Maintainers)
	}
	for name, values := range map[string]map[string]string{
		"id with a newline":  {"id": "x\ny", "title": "T"},
		"id with a colon":    {"id": "a: b", "title": "T"},
		"title with newline": {"id": "04-x", "title": "a\nmaintainers: [evil]"},
		"unknown field":      {"id": "04-x", "title": "T", "maintainers": "evil"},
		"missing required":   {"id": "04-x"},
	} {
		if _, err := Apply(dir, "module", values); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Apply(dir, "template.lab.aws", map[string]string{"module": "02-plain"}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("git-only blocks are refused: %v", err)
	}
	if _, err := Apply(dir, "nope", nil); !errors.Is(err, apperr.NotFound) {
		t.Errorf("unknown block: %v", err)
	}
	if _, err := Apply(dir, "lab.task.check", map[string]string{"module": "02-plain", "id": "t9", "title": "T"}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "no lab") {
		t.Errorf("a lab block in a module without a lab: %v", err)
	}
	if _, err := Apply(dir, "reading", map[string]string{"module": "01-welcome", "name": "intro", "title": "Again"}); !errors.Is(err, apperr.Invalid) {
		t.Errorf("a file that exists is never overwritten: %v", err)
	}
}
```

The unescaped `nasty` value goes in with a leading `He`, so `TrimSpace` leaves it unchanged: `q.Prompt` must equal it exactly.

Run: `go test ./internal/content/blocks -run 'Block|Catalog|Literal'`. Expected: build failure.

- [ ] **Step 2: Implement `catalog.go`**

```go
package blocks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/yamlx"
)

// Block is one entry of the catalog: what it is, the form that adds it, and how the form is written.
type Block struct {
	ID       string            `json:"id"`
	Group    string            `json:"group"`
	Title    string            `json:"title"`
	Summary  string            `json:"summary"`
	Doc      string            `json:"doc,omitempty"`
	FileKind string            `json:"file_kind"`
	Fields   []Field           `json:"fields"`
	Example  string            `json:"example"`
	GitOnly  bool              `json:"git_only,omitempty"`
	Insert   Insert            `json:"-"`
	Sample   map[string]string `json:"-"` // form values for the example and the tests
}

// Insert says how a form is written. Every string is a text/template over the form values plus "labdir" (the module's
// lab folder) and "service" (its first terminal's service). In YAML, values go through q, list, ints or pairs, which
// quote them, so a value is always text and never YAML.
type Insert struct {
	Files    map[string]string // new files: path → content; refused if the file exists
	Appends  []Append          // entries added to YAML files (existing, or created by Files)
	Open     string            // the file to open afterwards
	NeedsLab bool              // the target module must already have a lab
	GitOnly  bool              // shown in the catalog, added in git only (UI edits can't write those files)
	NewRepo  bool              // a whole repo (tests render it into an empty directory)
}

type Append struct {
	File string
	Path []string // yamlx.Insert path
	Item string
	Set  bool
}

type Result struct {
	Ops   []gitsync.Op `json:"ops"`
	Open  string       `json:"open"`
	Line  int          `json:"line"`  // where the insertion starts in Open
	Lines int          `json:"lines"` // how many lines it takes
}

var Groups = []string{"Training", "Module", "Reading", "Quiz", "Lab", "AWS"}

const gitOnlyWhy = "edits in Crucible write only .md, .yaml, .yml and .sh files: copy the example into the repo in git"

func in(name, typ, desc string) Field  { return Field{Name: name, Type: typ, Required: true, Description: desc} }
func opt(name, typ, desc string) Field { return Field{Name: name, Type: typ, Description: desc} }
func doc(key, name, typ string, required bool) Field {
	f := Fields[key]
	f.Name, f.Type, f.Required = name, typ, required
	return f
}

var (
	moduleTarget = Field{Name: "module", Type: "module", Required: true, Description: "Which module?"}
	taskTarget   = Field{Name: "task", Type: "task", Required: true, Description: "Which task of the module's lab?"}
	labTask      = "id: {{q .id}}\ninstructions: tasks/{{.id}}.md\n"
	taskDoc      = "# {{.title}}\n\nWhat to do, and how the trainee knows it worked.\n"
)

const (
	localCompose = "services:\n  shell:\n    image: alpine:3.22\n    command: [\"sleep\", \"infinity\"]\n"
	breakFixLab  = "id: {{q .id}}\nruntime: %s\nttl: 1h\nidle_timeout: 20m\nterminals:\n  - {name: shell, service: shell}\ntasks:\n" +
		"  - id: t1-relight\n    instructions: tasks/01-relight.md\n    setup: {script: setup/01-break.sh, run_in: shell}\n" +
		"    check: {script: checks/01-relight.sh, run_in: shell}\n    points: 2\n    hints:\n      - text: \"Read what the setup changed: cat /tmp/forge.conf\"\n"
)

func breakFix(runtime string) map[string]string {
	return map[string]string{
		"modules/{{.module}}/lab/lab.yaml":              fmt.Sprintf(breakFixLab, runtime),
		"modules/{{.module}}/lab/compose.yaml":          localCompose,
		"modules/{{.module}}/lab/tasks/01-relight.md":   "# Relight the forge\n\nThe setup turned the heat off in `/tmp/forge.conf`. Turn it back on.\n",
		"modules/{{.module}}/lab/setup/01-break.sh":     "#!/bin/sh\n# Breaks something on purpose; the task asks the trainee to fix it.\necho 'heat=off' > /tmp/forge.conf\n",
		"modules/{{.module}}/lab/checks/01-relight.sh":  "#!/bin/sh\n# Exits 0 when the task is done.\ngrep -q 'heat=on' /tmp/forge.conf\n",
	}
}

func question(typ, title, summary string, extra []Field, body string, sample map[string]string) Block {
	s := map[string]string{"module": "01-welcome", "id": "q-" + typ, "prompt": "Which iron do you strike?"}
	for k, v := range sample {
		s[k] = v
	}
	return Block{ID: "quiz.question." + typ, Group: "Quiz", Title: title, FileKind: "quiz", Summary: summary, Doc: Fields["Question.type"].Description,
		Fields: slices.Concat([]Field{moduleTarget, doc("Question.id", "id", "id", true), doc("Question.prompt", "prompt", "text", true)}, extra, []Field{doc("Question.points", "points", "number", false)}),
		Sample: s,
		Insert: Insert{Open: "modules/{{.module}}/quiz.yaml", Appends: []Append{{File: "modules/{{.module}}/quiz.yaml", Path: []string{"questions"},
			Item: "id: {{q .id}}\ntype: " + typ + "\nprompt: {{q .prompt}}\n" + body + "{{if .points}}points: {{.points}}\n{{end}}"}}},
	}
}

var (
	options      = in("options", "list", "One choice per line.")
	caseField    = doc("Question.case_sensitive", "case_sensitive", "bool", false)
	caseBody     = "{{if eq .case_sensitive \"true\"}}case_sensitive: true\n{{end}}"
	rubric       = doc("Question.rubric", "rubric", "text", true)
	answerString = in("answer", "string", "The expected answer.")
)

// Catalog is every block, in the order the Blocks panel shows them. TestEveryBlockSampleLoadsAndLints inserts each
// Sample into a fixture repo and loads the result.
var Catalog = []Block{
	{ID: "template.training", Group: "Training", Title: "New training repo", FileKind: "training",
		Summary: "A whole training repo: training.yaml and a first module with a reading.",
		Doc:     "Trainings start in git: create a repo with these files, list it in the platform's trainings.yaml, and push.",
		Fields:  []Field{in("id", "id", "The training's id, as trainings.yaml will list it."), in("title", "string", "The training's title."), in("maintainer", "string", "Email of the first maintainer (reviews edits).")},
		Sample:  map[string]string{"id": "forge-999", "title": "Forge 999", "maintainer": "smith@example.com"},
		Insert: Insert{GitOnly: true, NewRepo: true, Open: "training.yaml", Files: map[string]string{
			"training.yaml":                       "id: {{q .id}}\ntitle: {{q .title}}\nmaintainers: [{{q .maintainer}}]\nprogression: linear\nmodules: [01-welcome]\n",
			"modules/01-welcome/module.yaml":      "title: Welcome\nitems:\n  - reading: reading/intro.md\n",
			"modules/01-welcome/reading/intro.md": "# Welcome to {{.title}}\n\nWhat this training forges, and how long it takes.\n",
		}}},

	{ID: "module", Group: "Module", Title: "Module with a reading", FileKind: "module",
		Summary: "A new module folder, added to training.yaml, with a first reading.",
		Fields:  []Field{in("id", "id", "Folder name under modules/, e.g. 02-heat. Modules run in the order training.yaml lists them."), doc("Module.title", "title", "string", true)},
		Sample:  map[string]string{"id": "03-extra", "title": "Extra heat"},
		Insert: Insert{Open: "modules/{{.id}}/module.yaml",
			Files: map[string]string{"modules/{{.id}}/module.yaml": "title: {{q .title}}\nitems:\n  - reading: reading/intro.md\n", "modules/{{.id}}/reading/intro.md": "# {{.title}}\n\nWrite the reading here.\n"},
			Appends: []Append{{File: "training.yaml", Path: []string{"modules"}, Item: "{{.id}}"}}}},
	{ID: "template.module-quiz", Group: "Module", Title: "Module with a reading and a quiz", FileKind: "module",
		Summary: "A new module with a reading and a one-question quiz, added to training.yaml.",
		Fields:  []Field{in("id", "id", "Folder name under modules/, e.g. 02-heat."), doc("Module.title", "title", "string", true)},
		Sample:  map[string]string{"id": "03-quizzed", "title": "Quizzed"},
		Insert: Insert{Open: "modules/{{.id}}/quiz.yaml", Files: map[string]string{
			"modules/{{.id}}/module.yaml":      "title: {{q .title}}\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
			"modules/{{.id}}/reading/intro.md": "# {{.title}}\n\nWrite the reading here.\n",
			"modules/{{.id}}/quiz.yaml":        "pass_threshold: 0.8\nquestions:\n  - id: q1\n    type: single\n    prompt: \"Which iron do you strike?\"\n    options: [\"Cold iron\", \"Hot iron\"]\n    answer: 1\n",
		}, Appends: []Append{{File: "training.yaml", Path: []string{"modules"}, Item: "{{.id}}"}}}},

	{ID: "reading", Group: "Reading", Title: "Reading", FileKind: "reading",
		Summary: "A Markdown page in a module, added to its items.",
		Doc:     "Readings are Markdown: headings, lists, code, tables, callouts and mermaid diagrams. Link images from assets/; other relative links won't resolve.",
		Fields:  []Field{moduleTarget, in("name", "id", "File name without .md, e.g. tongs."), in("title", "string", "The reading's title (its first heading).")},
		Sample:  map[string]string{"module": "02-plain", "name": "tongs", "title": "Tongs"},
		Insert: Insert{Open: "modules/{{.module}}/reading/{{.name}}.md",
			Files:   map[string]string{"modules/{{.module}}/reading/{{.name}}.md": "# {{.title}}\n\nWrite the reading here.\n"},
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "reading: reading/{{.name}}.md"}}}},

	{ID: "quiz", Group: "Quiz", Title: "Quiz file", FileKind: "quiz",
		Summary: "The module's quiz.yaml with a first single-choice question, added to the module's items.",
		Fields: []Field{moduleTarget, doc("Quiz.pass_threshold", "pass_threshold", "number", false), doc("Quiz.max_attempts", "max_attempts", "integer", false),
			doc("Question.prompt", "prompt", "text", true), options, in("answer", "integer", "Number of the right option, counting from 0.")},
		Sample: map[string]string{"module": "02-plain", "prompt": "Which iron do you strike?", "options": "Cold iron\nHot iron", "answer": "1"},
		Insert: Insert{Open: "modules/{{.module}}/quiz.yaml",
			Files: map[string]string{"modules/{{.module}}/quiz.yaml": "pass_threshold: {{or .pass_threshold \"0.8\"}}\n{{if .max_attempts}}max_attempts: {{.max_attempts}}\n{{end}}" +
				"questions:\n  - id: q1\n    type: single\n    prompt: {{q .prompt}}\n    options: {{list .options}}\n    answer: {{.answer}}\n"},
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "quiz: quiz.yaml"}}}},
	question("single", "Single choice", "One right option.", []Field{options, in("answer", "integer", "Number of the right option, counting from 0.")},
		"options: {{list .options}}\nanswer: {{.answer}}\n", map[string]string{"options": "Cold iron\nHot iron", "answer": "1"}),
	question("multi", "Multiple choice", "Several right options.", []Field{options, in("answer", "ints", "Numbers of the right options, counting from 0, e.g. 0, 2.")},
		"options: {{list .options}}\nanswer: {{ints .answer}}\n", map[string]string{"options": "Tongs\nHammer\nFlour", "answer": "0, 1"}),
	question("exact", "Exact answer", "Typed text that must match.", []Field{answerString, caseField},
		"answer: {{q .answer}}\n"+caseBody, map[string]string{"answer": "80"}),
	question("regex", "Pattern answer", "Typed text that must match a pattern.", []Field{in("answer", "string", "A regular expression the whole answer must match, e.g. \\d+\\.\\d+."), caseField},
		"answer: {{q .answer}}\n"+caseBody, map[string]string{"answer": `\d+\.\d+\.\d+`}),
	question("order", "Put in order", "Items the trainee puts in order.", []Field{in("options", "list", "The items in their right order, one per line.")},
		"options: {{list .options}}\n", map[string]string{"options": "Heat\nStrike\nQuench"}),
	question("match", "Match pairs", "Pairs the trainee matches.", []Field{in("pairs", "pairs", "One pair per line: left = right.")},
		"pairs: {{pairs .pairs}}\n", map[string]string{"pairs": "Hammer = strike\nWater = quench"}),
	question("terminal", "Terminal question", "Answered in the lab and checked by a script; a lab task names it.", []Field{in("check", "path", "Check script, relative to the lab folder, e.g. checks/q-port.sh."), opt("run_in", "id", Fields["Question.run_in"].Description)},
		"check: {{q .check}}\n{{if .run_in}}run_in: {{q .run_in}}\n{{end}}", map[string]string{"check": "checks/q-terminal.sh"}),
	question("text", "Written answer", "A person scores it on the Anvil against the rubric.", []Field{rubric}, "rubric: {{q .rubric}}\n", map[string]string{"rubric": "Names the three heats."}),
	question("upload", "File upload", "A person scores the uploaded file on the Anvil.", []Field{rubric}, "rubric: {{q .rubric}}\n", map[string]string{"rubric": "A photo of the finished blade."}),
	question("signoff", "Sign-off", "A person confirms it in person on the Anvil.", nil, "", nil),

	{ID: "lab.local", Group: "Lab", Title: "Laptop lab with a break-fix task", FileKind: "lab",
		Summary: "A Docker lab on the trainee's laptop: one shell, a setup that breaks something and a check that it's fixed.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-lab"},
		Insert: Insert{Open: "modules/{{.module}}/lab/lab.yaml", Files: breakFix("local"),
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
	{ID: "lab.cluster", Group: "Lab", Title: "Cluster lab", FileKind: "lab",
		Summary: "The same break-fix lab, run on Crucible's cluster: nothing to install on the laptop.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-cluster"},
		Insert: Insert{Open: "modules/{{.module}}/lab/lab.yaml", Files: breakFix("cluster"),
			Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
	{ID: "lab.task.check", Group: "Lab", Title: "Task with a check", FileKind: "lab",
		Summary: "A task whose check script exits 0 when it's done.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.points", "points", "number", false), opt("run_in", "id", "Service the check runs in; defaults to the first terminal's.")},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-cast", "title": "Cast"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc, "modules/{{.module}}/{{.labdir}}/checks/{{.id}}.sh": "#!/bin/sh\n# Exits 0 when the task is done.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "check: {script: checks/{{.id}}.sh, run_in: {{q (or .run_in .service)}}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.task.setup", Group: "Lab", Title: "Break-fix task", FileKind: "lab",
		Summary: "A task whose setup breaks something on purpose and whose check sees it fixed.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.points", "points", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-relight", "title": "Relight"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc,
				"modules/{{.module}}/{{.labdir}}/setup/{{.id}}.sh":  "#!/bin/sh\n# Breaks something on purpose.\nexit 0\n",
				"modules/{{.module}}/{{.labdir}}/checks/{{.id}}.sh": "#!/bin/sh\n# Exits 0 when it's fixed.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "setup: {script: setup/{{.id}}.sh, run_in: {{q .service}}}\ncheck: {script: checks/{{.id}}.sh, run_in: {{q .service}}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.task.quiz", Group: "Lab", Title: "Task answered by a terminal question", FileKind: "lab",
		Summary: "A task scored by a terminal question from the module's quiz.yaml.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), in("question", "id", "The terminal question's id in quiz.yaml.")},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-prove", "title": "Prove it", "question": "q-term"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files:   map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"}, Item: labTask + "quiz: {{q .question}}\n"}}}},
	{ID: "lab.task.review", Group: "Lab", Title: "Human-scored review task", FileKind: "lab",
		Summary: "A task a person scores on the Anvil against a rubric.",
		Fields:  []Field{moduleTarget, doc("Task.id", "id", "id", true), in("title", "string", "The task's title."), doc("Task.rubric", "rubric", "text", true), doc("Task.points", "points", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "id": "t2-proof", "title": "Show your work", "rubric": "The transcript shows the fix and the notes say why.", "points": "3"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/tasks/{{.id}}.md": taskDoc},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks"},
				Item: labTask + "human_review: true\nrubric: {{q .rubric}}\n{{if .points}}points: {{.points}}\n{{end}}"}}}},
	{ID: "lab.hint", Group: "Lab", Title: "Hint", FileKind: "lab",
		Summary: "A hint the trainee can reveal for a task, at a cost.",
		Fields:  []Field{moduleTarget, taskTarget, doc("Hint.text", "text", "text", true), doc("Hint.cost", "cost", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "task": "t1", "text": "Try `echo`.", "cost": "0.5"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml", Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Path: []string{"tasks", "id={{.task}}", "hints"}, Item: "text: {{q .text}}\n{{if .cost}}cost: {{.cost}}\n{{end}}"}}}},
	{ID: "lab.hint.file", Group: "Lab", Title: "Hint from a file", FileKind: "lab",
		Summary: "A longer hint kept in its own Markdown file, e.g. a full solution.",
		Fields:  []Field{moduleTarget, taskTarget, in("name", "id", "File name without .md, e.g. t1-solution."), doc("Hint.cost", "cost", "number", false)},
		Sample:  map[string]string{"module": "01-welcome", "task": "t1", "name": "t1-solution"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/hints/{{.name}}.md",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/hints/{{.name}}.md": "Spell out the solution here.\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"tasks", "id={{.task}}", "hints"},
				Item: "file: hints/{{.name}}.md\n{{if .cost}}cost: {{.cost}}\n{{end}}"}}}},
	{ID: "lab.terminal", Group: "Lab", Title: "Terminal", FileKind: "lab",
		Summary: "Another terminal tab, attached to a service of the lab.",
		Fields:  []Field{moduleTarget, doc("Terminal.name", "name", "id", true), doc("Terminal.service", "service", "id", true)},
		Sample:  map[string]string{"module": "01-welcome", "name": "second", "service": "shell"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/lab.yaml", Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml",
			Path: []string{"terminals"}, Item: "name: {{q .name}}\nservice: {{q .service}}"}}}},
	{ID: "lab.setup", Group: "Lab", Title: "Lab setup script", FileKind: "script",
		Summary: "A script run once when the lab starts.",
		Fields:  []Field{moduleTarget, opt("run_in", "id", "Service it runs in; defaults to the first terminal's.")},
		Sample:  map[string]string{"module": "01-welcome"},
		Insert: Insert{NeedsLab: true, Open: "modules/{{.module}}/{{.labdir}}/setup/lab.sh",
			Files: map[string]string{"modules/{{.module}}/{{.labdir}}/setup/lab.sh": "#!/bin/sh\n# Runs once when the lab starts.\nexit 0\n"},
			Appends: []Append{{File: "modules/{{.module}}/{{.labdir}}/lab.yaml", Path: []string{"setup"}, Set: true,
				Item: "script: setup/lab.sh\nrun_in: {{q (or .run_in .service)}}"}}}},

	{ID: "template.lab.aws", Group: "AWS", Title: "AWS lab with an S3 bucket", FileKind: "lab",
		Summary: "A lab in the shared AWS account: Terraform creates a bucket, a check looks for a file in it.",
		Doc:     "AWS labs cost money: each start needs an approval by its estimate, and budgets fail closed. " + Fields["AWSConfig.max_hourly_usd"].Description + " Terraform files can't be edited in the browser.",
		Fields:  []Field{moduleTarget, doc("Lab.id", "id", "id", true), doc("AWSConfig.region", "region", "string", true), doc("AWSConfig.max_hourly_usd", "max_hourly_usd", "number", true)},
		Sample:  map[string]string{"module": "02-plain", "id": "plain-aws", "region": "eu-west-1", "max_hourly_usd": "0.05"},
		Insert: Insert{GitOnly: true, Open: "modules/{{.module}}/lab/lab.yaml", Files: map[string]string{
			"modules/{{.module}}/lab/lab.yaml": "id: {{q .id}}\nruntime: aws\nttl: 1h\nidle_timeout: 30m\nterminals:\n  - {name: workspace, service: workspace}\ntasks:\n" +
				"  - id: t1-bucket\n    instructions: tasks/01-bucket.md\n    check: {script: checks/01-bucket.sh, run_in: workspace, timeout: 60s}\naws:\n  region: {{q .region}}\n  max_hourly_usd: {{.max_hourly_usd}}\n",
			"modules/{{.module}}/lab/terraform/main.tf": "# The lab's own bucket. Crucible adds the provider and the state backend.\nresource \"aws_s3_bucket\" \"forge\" {\n  bucket        = \"crucible-lab-${var.crucible_lab_id}\"\n  force_destroy = true\n}\n",
			"modules/{{.module}}/lab/tasks/01-bucket.md": "# Fill the bucket\n\nPut a file named forged.txt in your lab's bucket.\n",
			"modules/{{.module}}/lab/checks/01-bucket.sh": "#!/bin/sh\naws s3api head-object --bucket \"crucible-lab-$CRUCIBLE_LAB_ID\" --key forged.txt >/dev/null 2>&1\n",
		}, Appends: []Append{{File: "modules/{{.module}}/module.yaml", Path: []string{"items"}, Item: "lab: lab"}}}},
}

func init() {
	for i := range Catalog {
		b := &Catalog[i]
		b.GitOnly = b.Insert.GitOnly
		b.Example = example(*b)
	}
}

// example is what the catalog shows, rendered from Sample: the file the block opens if it creates it, else the YAML
// entry it adds.
func example(b Block) string {
	data := map[string]string{"labdir": "lab", "service": "shell"}
	for k, v := range b.Sample {
		data[k] = v
	}
	for _, f := range b.Fields { // optional fields the sample leaves out render as empty
		if _, ok := data[f.Name]; !ok {
			data[f.Name] = ""
		}
	}
	open, _ := render(b.Insert.Open, data)
	for p, body := range b.Insert.Files {
		if rp, _ := render(p, data); rp == open {
			s, _ := render(body, data)
			return s
		}
	}
	if len(b.Insert.Appends) > 0 {
		s, _ := render(b.Insert.Appends[0].Item, data)
		return s
	}
	return ""
}

func Find(id string) (Block, bool) {
	i := slices.IndexFunc(Catalog, func(b Block) bool { return b.ID == id })
	if i < 0 {
		return Block{}, false
	}
	return Catalog[i], true
}

func invalidf(format string, a ...any) error { return apperr.Wrap(apperr.Invalid, fmt.Sprintf(format, a...)) }

var funcs = template.FuncMap{
	"q": func(s string) string { b, _ := json.Marshal(s); return string(b) }, // a JSON string is a YAML double-quoted scalar
	"list": func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				b, _ := json.Marshal(l)
				out = append(out, string(b))
			}
		}
		return "[" + strings.Join(out, ", ") + "]"
	},
	"ints": func(s string) string { return "[" + strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), ", ") + "]" },
	"pairs": func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if left, right, ok := strings.Cut(l, "="); ok {
				a, _ := json.Marshal(strings.TrimSpace(left))
				b, _ := json.Marshal(strings.TrimSpace(right))
				out = append(out, "["+string(a)+", "+string(b)+"]")
			}
		}
		return "[" + strings.Join(out, ", ") + "]"
	},
}

func render(tpl string, data map[string]string) (string, error) {
	t, err := template.New("").Funcs(funcs).Option("missingkey=error").Parse(tpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

var (
	idRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,62}$`)
	pathRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*\.sh$`)
)

// checkValues validates form values against b's fields: known names only, required ones present, every value of its
// type. Ids and paths are plain names, so they can go into paths and YAML unquoted; text goes through q.
func checkValues(b Block, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k := range values {
		if !slices.ContainsFunc(b.Fields, func(f Field) bool { return f.Name == k }) {
			return nil, invalidf("%s is not a field of %s", k, b.Title)
		}
	}
	for _, f := range b.Fields {
		v := strings.TrimSpace(values[f.Name])
		if v == "" {
			v = f.Default
		}
		out[f.Name] = v
		if v == "" {
			if f.Required {
				return nil, invalidf("%s is required", f.Name)
			}
			continue
		}
		if len(v) > 4096 || strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
			return nil, invalidf("%s: text of at most 4 KiB", f.Name)
		}
		bad := false
		switch f.Type {
		case "id", "module", "task":
			bad = !idRE.MatchString(v)
		case "path":
			bad = !pathRE.MatchString(v) || strings.Contains(v, "..")
		case "string", "enum":
			bad = strings.ContainsAny(v, "\r\n") || len(v) > 200 || (f.Type == "enum" && !slices.Contains(f.Enum, v))
		case "number":
			n, err := strconv.ParseFloat(v, 64)
			bad = err != nil || n != n || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) || strings.ContainsAny(v, "xXpP_")
		case "integer":
			n, err := strconv.Atoi(v)
			bad = err != nil || n < 0
		case "ints":
			for _, s := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
				if n, err := strconv.Atoi(s); err != nil || n < 0 {
					bad = true
				}
			}
		case "bool":
			bad = v != "true" && v != "false"
		case "duration":
			_, err := time.ParseDuration(v)
			bad = err != nil
		case "list", "pairs":
			lines := strings.Split(v, "\n")
			bad = len(lines) > 20 || (f.Type == "pairs" && slices.ContainsFunc(lines, func(l string) bool { return !strings.Contains(l, "=") }))
		}
		if bad {
			return nil, invalidf("%s: %q isn't a valid %s", f.Name, v, f.Type)
		}
	}
	return out, nil
}

// labOf is a module's lab folder from its module.yaml ("" when it has none) and the first terminal's service.
func labOf(dir, module string) (string, string) {
	var m struct {
		Items []map[string]string `yaml:"items"`
	}
	if yamlx.ReadLoose(filepath.Join(dir, "modules", module, "module.yaml"), &m) != nil {
		return "", ""
	}
	for _, it := range m.Items {
		if lab, ok := it["lab"]; ok {
			if c := path.Clean(lab); c != "." && filepath.IsLocal(c) {
				var l struct {
					Terminals []content.Terminal `yaml:"terminals"`
				}
				_ = yamlx.ReadLoose(filepath.Join(dir, "modules", module, filepath.FromSlash(c), "lab.yaml"), &l)
				svc := ""
				if len(l.Terminals) > 0 {
					svc = l.Terminals[0].Service
				}
				return c, svc
			}
		}
	}
	return "", ""
}

// Plan renders b with values against the repo at dir (read only): the files it changes (path → new text), the file to
// open, and the line and line count of the insertion there.
func Plan(dir string, b Block, values map[string]string) (map[string]string, string, int, int, error) {
	vals, err := checkValues(b, values)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if m := vals["module"]; m != "" {
		vals["labdir"], vals["service"] = labOf(dir, m)
		if _, err := os.Stat(filepath.Join(dir, "modules", m, "module.yaml")); err != nil {
			return nil, "", 0, 0, invalidf("there is no module %s", m)
		}
	}
	if b.Insert.NeedsLab && vals["labdir"] == "" {
		return nil, "", 0, 0, invalidf("module %s has no lab yet: add a lab first", vals["module"])
	}
	for _, k := range []string{"labdir", "service"} {
		if _, ok := vals[k]; !ok {
			vals[k] = ""
		}
	}
	open, err := render(b.Insert.Open, vals)
	if err != nil {
		return nil, "", 0, 0, err
	}
	changed := map[string]string{}
	line, lines := 1, 0
	for p, body := range b.Insert.Files {
		rp, err := render(p, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rp))); err == nil {
			return nil, "", 0, 0, invalidf("%s already exists", rp)
		}
		if changed[rp], err = render(body, vals); err != nil {
			return nil, "", 0, 0, err
		}
		if rp == open {
			lines = strings.Count(changed[rp], "\n")
		}
	}
	for _, a := range b.Insert.Appends {
		file, err := render(a.File, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		segs := make([]string, len(a.Path))
		for i, s := range a.Path {
			if segs[i], err = render(s, vals); err != nil {
				return nil, "", 0, 0, err
			}
		}
		item, err := render(a.Item, vals)
		if err != nil {
			return nil, "", 0, 0, err
		}
		cur, ok := changed[file]
		if !ok {
			if gitsync.CheckPath(file) != nil && !b.Insert.GitOnly {
				return nil, "", 0, 0, invalidf("%s can't be edited here", file)
			}
			raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
			if err != nil {
				return nil, "", 0, 0, invalidf("%s doesn't exist", file)
			}
			cur = string(raw)
		}
		out, at, err := yamlx.Insert([]byte(cur), segs, item, a.Set)
		if err != nil {
			return nil, "", 0, 0, invalidf("%s: %v", file, err)
		}
		if file == open {
			line, lines = at, strings.Count(string(out), "\n")-strings.Count(cur, "\n")
		}
		changed[file] = string(out)
	}
	return changed, open, line, max(lines, 1), nil
}

// Apply is Plan for the editor: refused for git-only blocks, and returned as put ops that pass gitsync.CheckOps.
func Apply(dir, id string, values map[string]string) (*Result, error) {
	b, ok := Find(id)
	if !ok {
		return nil, apperr.Wrap(apperr.NotFound, "no such block")
	}
	if b.GitOnly {
		return nil, invalidf("%s can't be added in the browser: %s", b.Title, gitOnlyWhy)
	}
	changed, open, line, lines, err := Plan(dir, b, values)
	if err != nil {
		return nil, err
	}
	ops := gitsync.PutOps(changed)
	if err := gitsync.CheckOps(ops); err != nil {
		return nil, err
	}
	return &Result{Ops: ops, Open: open, Line: line, Lines: lines}, nil
}
```

`template.training` renders into an empty directory in the test. Its `Plan` has no appends and no module, so it needs no fixture. A `checkValues` failure returns before anything is rendered.

- [ ] **Step 3: Run the catalog tests**

Run: `go test ./internal/content/blocks`
Expected: PASS. When a sample leaves problems, the failure names the block and lint's message. Fix the template, never the lint. The terraform lint (`awsModule`) stays as it is; if `template.lab.aws` trips it, copy `examples/forge-401/modules/01-cloud-heat/lab/terraform/main.tf` verbatim.

- [ ] **Step 4: The authoring endpoints, with failing tests first**

`internal/authoring/insert_test.go`:

```go
package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/gitsync"
)

func TestInsertReturnsOpsForTheDraft(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	res, err := f.s.Insert(ctx, f.leader, InsertReq{Training: "t1", BaseSHA: f.head(), Block: "reading", Values: map[string]string{"module": "m1", "name": "tongs", "title": "Tongs"}})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, op := range res.Ops {
		paths = append(paths, op.Path)
	}
	if strings.Join(paths, " ") != "modules/m1/module.yaml modules/m1/reading/tongs.md" || res.Open != "modules/m1/reading/tongs.md" {
		t.Fatalf("result: %+v", res)
	}
	if probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: res.Ops}); err != nil || len(probs) != 0 {
		t.Fatalf("the insert is valid: %v %v", probs, err)
	}
	if _, err := f.s.Insert(ctx, f.trainee, InsertReq{Training: "t1", BaseSHA: f.head(), Block: "reading"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("enrolled: %v", err)
	}
	if _, err := f.s.Blocks(f.trainee, "t1"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("blocks for the enrolled: %v", err)
	}
}

// Review Focus 4: the author's draft has broken YAML in the target file.
func TestInsertIntoBrokenFile(t *testing.T) {
	f := setup(t)
	_, err := f.s.Insert(context.Background(), f.leader, InsertReq{Training: "t1", BaseSHA: f.head(),
		Ops:   []gitsync.Op{{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: [\n"}},
		Block: "reading", Values: map[string]string{"module": "m1", "name": "tongs", "title": "Tongs"}})
	if !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "fix it first") || !strings.Contains(err.Error(), "modules/m1/module.yaml") {
		t.Fatalf("broken target: %v", err)
	}
}
```

Add HTTP rows to `http_test.go`:
- `GET /api/authoring/blocks?training=t1` → 200 for leader, 403 for trainee.
- `POST /api/authoring/insert` with an unknown block → 404.
- `POST /api/authoring/insert` with `template.lab.aws` → 400.

`internal/authoring/insert.go`:

```go
package authoring

import (
	"context"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/content/blocks"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

type InsertReq struct {
	Training string            `json:"training"`
	BaseSHA  string            `json:"base_sha"`
	Ops      []gitsync.Op      `json:"ops"`
	Block    string            `json:"block"`
	Values   map[string]string `json:"values"`
}

// Blocks is the catalog, for someone allowed to edit training (it holds no training content).
func (s *Service) Blocks(u *auth.User, training string) ([]blocks.Block, error) {
	if _, _, err := s.Edits.Authorize(u, training); err != nil {
		return nil, err
	}
	return blocks.Catalog, nil
}

// Insert renders a block against the draft (its base with in.Ops applied) and returns the resulting file changes as
// put ops for the editor to apply. Nothing is saved: the draft's next autosave carries them.
func (s *Service) Insert(ctx context.Context, u *auth.User, in InsertReq) (*blocks.Result, error) {
	t, err := s.base(ctx, u, in.Training, in.BaseSHA)
	if err != nil {
		return nil, err
	}
	if len(in.Ops) > 0 {
		if err := gitsync.CheckOps(in.Ops); err != nil {
			return nil, err
		}
	}
	if len(in.Values) > 30 {
		return nil, apperr.Wrap(apperr.Invalid, "too many values")
	}
	var res *blocks.Result
	var ferr error
	if err := s.bounded(ctx, u.Email, func() {
		dir, _, cleanup, err := edits.Workspace(t, in.Ops)
		if err != nil {
			ferr = err
			return
		}
		defer cleanup()
		res, ferr = blocks.Apply(dir, in.Block, in.Values)
	}); err != nil {
		return nil, err
	}
	return res, ferr
}
```

Routes in `http.go`:

```go
	r.Get("/api/authoring/blocks", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Blocks(user(r), r.URL.Query().Get("training"))
		reply(w, map[string]any{"groups": blocks.Groups, "blocks": v}, err)
	})
	r.Post("/api/authoring/insert", func(w http.ResponseWriter, r *http.Request) {
		var in InsertReq
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Insert(r.Context(), user(r), in)
		reply(w, v, err)
	})
```

Run: `go test -race ./internal/authoring`. Expected: PASS.

- [ ] **Step 5: Generated block pages and block coverage**

In `internal/docs/generated.go`, append one page per catalog group to `Generated()`:

```go
	for i, g := range blocks.Groups {
		var b strings.Builder
		var covers []string
		fmt.Fprintf(&b, "The %s blocks of the editor's Blocks panel. Each one is a form; what it writes is shown below.\n", strings.ToLower(g))
		for _, bl := range blocks.Catalog {
			if bl.Group != g {
				continue
			}
			covers = append(covers, "block:"+bl.ID)
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", bl.Title, bl.Summary)
			if bl.Doc != "" {
				fmt.Fprintf(&b, "\n%s\n", bl.Doc)
			}
			if bl.GitOnly {
				b.WriteString("\n> [!NOTE]\n> Add this in git: the browser editor writes only .md, .yaml, .yml and .sh files.\n")
			}
			b.WriteString("\n| Field | Required | What it is |\n|---|---|---|\n")
			for _, f := range bl.Fields {
				req := ""
				if f.Required {
					req = "yes"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", f.Name, req, strings.ReplaceAll(f.Description, "|", `\|`))
			}
			lang := map[string]string{"reading": "markdown", "script": "sh"}[bl.FileKind]
			if lang == "" {
				lang = "yaml"
			}
			fmt.Fprintf(&b, "\nExample:\n\n```%s\n%s\n```\n", lang, strings.TrimRight(bl.Example, "\n"))
		}
		body := b.String()
		pages = append(pages, Page{Slug: "authors/blocks/" + strings.ToLower(g), Section: "authors", Title: "Building blocks: " + g,
			Roles: []string{"author"}, Covers: covers, Order: 100 + i, Headings: headings(body), Body: body})
	}
```

Restructure `Generated()` so the reference page is built into a `pages` slice first and both parts are returned together. Check that the callout syntax (`> [!NOTE]`) is what `web/src/lib/callouts.ts` expects; if not, use plain bold text.

In `coverage_test.go`, add a `case "block":` that records `blocksCovered[v] = true` and fails on an id that `blocks.Find` doesn't know. After the loop, add: every `blocks.Catalog` id must be covered. Import `crucible/internal/content/blocks`.

- [ ] **Step 6: Update docs/user in the same commit**

In `docs/user/authors/repo-layout.md`, link the generated pages: "Every block, with its form and an example: [Training](/docs/authors/blocks/training), [Module](/docs/authors/blocks/module), [Reading](/docs/authors/blocks/reading), [Quiz](/docs/authors/blocks/quiz), [Lab](/docs/authors/blocks/lab), [AWS](/docs/authors/blocks/aws)."

- [ ] **Step 7: Run everything Go**

Run: `gofmt -l cmd internal docs && go vet ./... && go test -race ./internal/content/... ./internal/authoring ./internal/docs ./internal/yamlx`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -- internal/content/blocks/catalog.go internal/content/blocks/catalog_test.go internal/authoring/insert.go internal/authoring/insert_test.go
git commit -m "feat(blocks): catalog of blocks and starter templates, form inserts that keep files intact, generated block pages" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- internal/content/blocks internal/authoring internal/docs docs/user/authors/repo-layout.md
```

---

### Task 16: The Blocks panel: forms from the registry, target pickers, inserting into the draft

**Files:**
- Create: `web/src/pages/editor/forms.ts`, `web/src/pages/editor/forms.test.ts`, `web/src/pages/editor/BlocksPanel.tsx`, `web/src/pages/editor/BlocksPanel.test.tsx`
- Modify: `web/src/pages/editor/Ide.tsx`, `web/src/pages/editor/CodeEditor.tsx` (select the inserted lines), `web/src/types.ts`, `docs/user/authors/editing-content.md`

**Interfaces:**
- Consumes: `GET /api/authoring/blocks` returns `{groups, blocks: BlockInfo[]}`; `POST /api/authoring/insert` returns `{ops, open, line, lines}` (Task 15); `parseLab` (Task 12).
- Produces:
  - TS `type BlockField = { name; type; required?; enum?; default?; description; min?; max? }`
  - TS `type BlockInfo = { id; group; title; summary; doc?; file_kind; fields: BlockField[]; example; git_only?: boolean }`
  - `initialValues(fields): Record<string, string>`
  - `formProblem(fields, values): string | undefined`
  - `modulesOf(paths): string[]`
  - `tasksOf(paths, module, read): string[]`
  - `BlocksPanel({ groups, blocks, paths, read, need(path), preselect?: string, onInsert(block, values): Promise<void> })`
  - `CodeEditor.reveal` becomes `{ line: number; lines?: number; n: number }`: it selects `lines` lines starting at `line`.

- [ ] **Step 1: Write the failing tests**

`web/src/pages/editor/forms.test.ts`:

```ts
import { expect, test } from 'vitest'
import { formProblem, initialValues, modulesOf, tasksOf } from './forms'
import type { BlockField } from '../../types'

const fields: BlockField[] = [
  { name: 'module', type: 'module', required: true, description: 'Which module?' },
  { name: 'id', type: 'id', required: true, description: 'Id' },
  { name: 'points', type: 'number', description: 'Points', min: 0 },
  { name: 'progression', type: 'enum', enum: ['linear', 'free'], default: 'linear', description: 'Order' },
]

test('defaults fill the form', () => {
  expect(initialValues(fields)).toEqual({ module: '', id: '', points: '', progression: 'linear' })
})

test('the form mirrors the server checks', () => {
  expect(formProblem(fields, { module: '', id: 'x' })).toMatch(/module is required/)
  expect(formProblem(fields, { module: 'm1', id: 'a b' })).toMatch(/id/)
  expect(formProblem(fields, { module: 'm1', id: 'q1', points: '-1' })).toMatch(/points/)
  expect(formProblem(fields, { module: 'm1', id: 'q1', points: '2', progression: 'linear' })).toBeUndefined()
})

test('targets come from the draft: modules, and the tasks of a module lab', () => {
  const paths = ['training.yaml', 'modules/01-a/module.yaml', 'modules/01-a/lab/lab.yaml', 'modules/02-b/module.yaml']
  expect(modulesOf(paths)).toEqual(['01-a', '02-b'])
  const lab = 'id: l\nruntime: local\nterminals: [{name: s, service: s}]\ntasks:\n  - {id: t1, instructions: a.md}\n  - {id: t2, instructions: b.md}\n'
  expect(tasksOf(paths, '01-a', (p) => (p.endsWith('lab.yaml') ? lab : undefined))).toEqual(['t1', 't2'])
  expect(tasksOf(paths, '02-b', () => undefined)).toEqual([])
})
```

`web/src/pages/editor/BlocksPanel.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { BlocksPanel } from './BlocksPanel'
import type { BlockInfo } from '../../types'

const blocks: BlockInfo[] = [
  { id: 'reading', group: 'Reading', title: 'Reading', summary: 'A Markdown page.', file_kind: 'reading', example: '# Tongs',
    fields: [{ name: 'module', type: 'module', required: true, description: 'Which module?' }, { name: 'title', type: 'string', required: true, description: 'Title' }] },
  { id: 'template.lab.aws', group: 'AWS', title: 'AWS lab with an S3 bucket', summary: 'S3.', file_kind: 'lab', example: 'runtime: aws', git_only: true, fields: [] },
]
const render = (preselect?: string) => renderToStaticMarkup(<BlocksPanel groups={['Reading', 'AWS']} blocks={blocks} paths={['modules/01-a/module.yaml']}
  read={() => undefined} need={() => {}} preselect={preselect} onInsert={async () => {}} />)

test('blocks are listed by group with their summary', () => {
  const html = render()
  expect(html).toContain('<h3>Reading</h3>')
  expect(html).toContain('A Markdown page.')
})

test('a form is generated from the fields: required markers, a module picker, help text', () => {
  const html = render('reading')
  expect(html).toContain('<select')
  expect(html).toContain('01-a')
  expect(html).toContain('aria-required="true"')
  expect(html).toContain('Which module?')
  expect(html).toContain('Add to the draft')
})

test('git-only blocks show their example and say where to add them', () => {
  const html = render('template.lab.aws')
  expect(html).toContain('runtime: aws')
  expect(html).toContain('Add this in git')
  expect(html).not.toContain('Add to the draft')
})
```

Run: `cd web && npx vitest run src/pages/editor/forms.test.ts src/pages/editor/BlocksPanel.test.tsx`. Expected: FAIL.

- [ ] **Step 2: Implement `forms.ts` and the types**

`types.ts`:

```ts
export type BlockField = { name: string; type: string; required?: boolean; enum?: string[]; default?: string; description: string; min?: number; max?: number }
export type BlockInfo = { id: string; group: string; title: string; summary: string; doc?: string; file_kind: string; fields: BlockField[]; example: string; git_only?: boolean }
```

`forms.ts`:

```ts
import type { BlockField } from '../../types'
import { parseLab } from './preview'

export const initialValues = (fields: BlockField[]) => Object.fromEntries(fields.map((f) => [f.name, f.default ?? '']))

const id = /^[A-Za-z0-9_][A-Za-z0-9_-]{0,62}$/

// formProblem mirrors blocks.checkValues so the form says what's wrong before the server does.
export function formProblem(fields: BlockField[], values: Record<string, string>): string | undefined {
  for (const f of fields) {
    const v = (values[f.name] ?? '').trim()
    if (!v) { if (f.required) return `${f.name} is required.`; continue }
    if (['id', 'module', 'task'].includes(f.type) && !id.test(v)) return `${f.name}: letters, digits, '-' and '_' only.`
    if ((f.type === 'string' || f.type === 'enum') && /[\r\n]/.test(v)) return `${f.name}: one line.`
    if (f.type === 'number' && (isNaN(Number(v)) || (f.min !== undefined && Number(v) < f.min) || (f.max !== undefined && Number(v) > f.max)))
      return `${f.name}: a number${f.min !== undefined ? ` from ${f.min}` : ''}${f.max !== undefined ? ` to ${f.max}` : ''}.`
    if (f.type === 'integer' && !/^\d+$/.test(v)) return `${f.name}: a whole number.`
    if (f.type === 'ints' && !/^\d+(\s*,\s*\d+)*$/.test(v)) return `${f.name}: numbers separated by commas, e.g. 0, 2.`
    if (f.type === 'pairs' && v.split('\n').some((l) => !l.includes('='))) return `${f.name}: one "left = right" per line.`
    if (f.type === 'path' && !/^[A-Za-z0-9_-]+(\/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*\.sh$/.test(v)) return `${f.name}: a .sh path inside the lab folder.`
  }
}

export const modulesOf = (paths: string[]) =>
  paths.flatMap((p) => /^modules\/([^/]+)\/module\.yaml$/.exec(p)?.slice(1, 2) ?? []).sort()

export function tasksOf(paths: string[], module: string, read: (p: string) => string | undefined): string[] {
  const lab = paths.find((p) => new RegExp(`^modules/${module}/[^/]+/lab\\.yaml$`).test(p))
  const text = lab ? read(lab) : undefined
  return text ? (parseLab(text).lab?.tasks ?? []).map((t) => t.id) : []
}
```

- [ ] **Step 3: Implement `BlocksPanel.tsx`**

```tsx
import { useEffect, useState } from 'react'
import type { BlockInfo } from '../../types'
import { formProblem, initialValues, modulesOf, tasksOf } from './forms'

type Props = {
  groups: string[]; blocks: BlockInfo[]; paths: string[]; read: (p: string) => string | undefined; need: (p: string) => void
  preselect?: string; onInsert: (block: BlockInfo, values: Record<string, string>) => Promise<void>
}

// BlocksPanel: pick a block, fill the form generated from its registry fields, and the server writes the files.
export function BlocksPanel({ groups, blocks, paths, read, need, preselect, onInsert }: Props) {
  const [pick, setPick] = useState(preselect ?? '')
  const block = blocks.find((b) => b.id === pick)
  const [values, setValues] = useState<Record<string, string>>(block ? initialValues(block.fields) : {})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => { if (preselect) { setPick(preselect); const b = blocks.find((x) => x.id === preselect); if (b) setValues(initialValues(b.fields)) } }, [preselect, blocks])
  const module = values.module ?? ''
  useEffect(() => { // the task picker needs the module's lab.yaml
    const lab = paths.find((p) => p.startsWith(`modules/${module}/`) && p.endsWith('/lab.yaml'))
    if (module && lab) need(lab)
  }, [module, paths, need])

  if (!block) {
    return (
      <div className="blocks" role="region" aria-label="Blocks">
        {groups.map((g) => (
          <div key={g}>
            <h3>{g}</h3>
            <ul>
              {blocks.filter((b) => b.group === g).map((b) => (
                <li key={b.id}>
                  <button className="ghost" onClick={() => { setPick(b.id); setValues(initialValues(b.fields)); setErr('') }}>{b.title}</button>
                  <span className="muted"> {b.summary}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    )
  }
  const problem = formProblem(block.fields, values)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (problem) return setErr(problem)
    setBusy(true); setErr('')
    try { await onInsert(block, values) } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <div className="blocks" role="region" aria-label={`Block: ${block.title}`}>
      <p><button className="ghost" onClick={() => setPick('')}>← All blocks</button></p>
      <h3>{block.title}</h3>
      <p>{block.summary}</p>
      {block.doc && <p className="muted">{block.doc}</p>}
      <p><a href={`/docs/authors/blocks/${block.group.toLowerCase()}`}>{`Reference: ${block.group} blocks`}</a></p>
      <pre aria-label="Example">{block.example}</pre>
      {block.git_only ? <p role="note">Add this in git: the browser editor writes only .md, .yaml, .yml and .sh files.</p> : (
        <form onSubmit={submit}>
          {block.fields.map((f) => {
            const common = { id: `bf-${f.name}`, value: values[f.name] ?? '', 'aria-required': f.required ? true : undefined, 'aria-describedby': `bf-${f.name}-help`,
              onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => setValues((v) => ({ ...v, [f.name]: e.target.value })) }
            const choices = f.type === 'module' ? modulesOf(paths) : f.type === 'task' ? tasksOf(paths, module, read) : f.type === 'enum' ? f.enum ?? [] : f.type === 'bool' ? ['false', 'true'] : undefined
            return (
              <p key={f.name}>
                <label htmlFor={`bf-${f.name}`}>{f.name}{f.required && ' *'}</label><br />
                {choices ? (
                  <select {...common}><option value="">Choose…</option>{choices.map((c) => <option key={c} value={c}>{c}</option>)}</select>
                ) : ['text', 'list', 'pairs'].includes(f.type) ? <textarea rows={3} {...common} /> : <input {...common} />}
                <br /><small id={`bf-${f.name}-help`} className="muted">{f.description}</small>
              </p>
            )
          })}
          <p><button type="submit" disabled={busy}>Add to the draft</button> <span role="alert" className="error">{err}</span></p>
        </form>
      )}
    </div>
  )
}
```

The labels read `id *` and `title *`, so the e2e uses `getByLabel(/^id/)`.

- [ ] **Step 4: Wire it into `Ide.tsx`; select the inserted lines**

- Add `'blocks'` to the `panel` union and a **Blocks** button to the activity bar.
- Load the catalog once: `const [catalog, setCatalog] = useState<{ groups: string[]; blocks: BlockInfo[] }>()`, fetched in `load()` from `/api/authoring/blocks?training=…`.
- Track the preselected block: `const [preselect, setPreselect] = useState<string>()`. Explorer's `onNewModule={() => { setPanel('blocks'); setPreselect('module') }}`.
- Refactor the validate effect's body into `const runValidate = useCallback((ops: EditOp[]) => …, [draft])`. The debounced effect calls it, and the insert below calls it at once.

```tsx
  const insert = async (block: BlockInfo, values: Record<string, string>) => {
    const r = await api<{ ops: EditOp[]; open: string; line: number; lines: number }>('/api/authoring/insert',
      { method: 'POST', json: { training: draft!.training, base_sha: draft!.base_sha, ops, block: block.id, values } })
    for (const p of r.ops.flatMap((o) => (o.op === 'put' ? [o.path] : []))) await loadBase(origin(base, work, p)).catch(() => {})
    let next = work
    for (const o of r.ops) if (o.op === 'put') next = putText(next, o.path, o.content, originalOf(o.path))
    change(next)
    await open(r.open)
    setReveal({ line: r.line, lines: r.lines, n: Date.now() })
    runValidate(toOps(next)) // at once, not after the debounce
    setPreselect(undefined)
    setPanel('explorer') // the new files are in view; Blocks reopens on its list
  }
```

Render it when `panel === 'blocks'`: `<BlocksPanel groups={catalog.groups} blocks={catalog.blocks} paths={paths} read={(p) => (paths.includes(p) ? (work.puts[p] ?? originalOf(p)) : undefined)} need={(p) => { loadBase(origin(base, work, p)).catch(() => {}) }} preselect={preselect} onInsert={insert} />`. `need` must be stable across renders or the panel's effect loops: wrap it in `useCallback` with `[base, work, baseText]`, or keep a ref.

`originalOf` reads `baseText` from the closure. After `loadBase` resolves, the closure is stale, so read freshly loaded text from the value `loadBase` returns: change `loadBase` to return the content, and use it. Make sure `putText` gets the real original, or a no-op put would stay in the draft.

In `CodeEditor.tsx`, after `revealLineInCenter`:

```tsx
    if (reveal.lines && reveal.lines > 1) {
      const m = ed.current.getModel()
      const endLine = Math.min(reveal.line + reveal.lines - 1, m?.getLineCount() ?? reveal.line)
      ed.current.setSelection(new monaco.Range(reveal.line, 1, endLine, m?.getLineMaxColumn(endLine) ?? 1))
    }
```

and widen the prop type to `{ line: number; lines?: number; n: number }`.

- [ ] **Step 5: Run the checks**

Run: `cd web && npm test && npx tsc -b && npm run build && npm run lint`
Expected: PASS, and check-chunks is still clean.

- [ ] **Step 6: Update docs/user in the same commit**

Add **Blocks** to `docs/user/authors/editing-content.md`:
- The panel lists every block by group.
- A block's form marks required fields, offers a picker for the module or task, and shows help under each field.
- **Add to the draft** writes the files: the file opens with the new lines selected, and problems are checked at once.
- Typing YAML by hand stays available.
- Git-only blocks (new training, AWS lab) show their example to copy into git.
- **New module** in the explorer opens the Module block.
- Link `/docs/authors/blocks/quiz` and the other group pages.

- [ ] **Step 7: Commit**

```bash
git add -- web/src/pages/editor/forms.ts web/src/pages/editor/forms.test.ts web/src/pages/editor/BlocksPanel.tsx web/src/pages/editor/BlocksPanel.test.tsx
git commit -m "feat(web): Blocks panel with registry-generated forms and target pickers; inserts open highlighted and validate at once" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- web/src/pages/editor web/src/types.ts docs/user/authors/editing-content.md
```

---

## Delivery step 6: end-to-end, docs, release checks

### Task 17: E2E: `authoring.spec.ts` and `docs.spec.ts`, with zero CSP violations

**Files:**
- Create: `examples/forge-103/training.yaml`, `examples/forge-103/modules/01-anvil/module.yaml`, `examples/forge-103/modules/01-anvil/reading/scroll.md`, `examples/forge-103/modules/01-anvil/hints/h-seed.md`, `examples/platform/teams/forge/programs/forge-103.yaml`, `e2e/tests/authoring.spec.ts`, `e2e/tests/docs.spec.ts`
- Modify: `examples/platform/trainings.yaml`, `scripts/seed-git.sh`, `scripts/local-check.sh`, `e2e/tests/helpers.ts`

**Interfaces:**
- Consumes: everything above. Accessible names used: Explorer region; buttons `Start an edit`, `Blocks`, `Module with a reading`, `Quiz file`, `Add to the draft`, `Rename <path>`, `Delete <path>`, `New file`, `Create`, `Rename`, `Problems (N)`, `Changes (N)`, `Submit for review`, `Approve and merge`, `Help for this page`. Labels `Title`, `Training`, `New path for <path>`, `Path of the new file`. Test ids `edit-preview`, `edit-status`, `diff`, `code-editor`.
- Produces: `watchCsp(page): Promise<string[]>` in `helpers.ts`.

- [ ] **Step 1: The Forge 103 fixture**

`examples/forge-103/training.yaml`:

```yaml
id: forge-103
title: "Forge 103: The Smithy"
description: The authoring end-to-end test reshapes this training on every run.
maintainers: [senior@crucible.local]
progression: free
estimated_hours: 0.5
modules: [01-anvil]
```

- `examples/forge-103/modules/01-anvil/module.yaml`: `title: The Anvil\nitems:\n  - reading: reading/scroll.md\n`
- `examples/forge-103/modules/01-anvil/reading/scroll.md`: `# The Scroll\n\nEvery smith keeps a scroll of what the anvil taught.\n`
- `examples/forge-103/modules/01-anvil/hints/h-seed.md`: `A spare hint the authoring test deletes and replaces on every run.\n`
- `examples/platform/teams/forge/programs/forge-103.yaml`: `training: forge-103\nenrolled: [trainee@crucible.local]\n`
- `examples/platform/trainings.yaml`, appended:

```yaml
  forge-103:
    repo: file:///git/forge-103.git   # authoring e2e fixture; the test reshapes it on every run (idempotent on KEEP=1)
    branch: main
```

`scripts/seed-git.sh`: add `forge-103` to the `for name in …` list and to the header comment. `scripts/local-check.sh`: add `./bin/crucible lint examples/forge-103` to the lint block.

Run: `make build && ./bin/crucible lint examples/forge-103 && ./bin/crucible lint examples/platform && go test ./...`
Expected: clean. A Go test that counts examples' programs or trainings may need its expected numbers updated: fix the count, never the fixture.

- [ ] **Step 2: The CSP watcher**

Append to `e2e/tests/helpers.ts`:

```ts
// watchCsp collects Content-Security-Policy violations on this page from now on: Chromium's console report and the
// securitypolicyviolation event, on the current document and every later one.
export async function watchCsp(page: Page): Promise<string[]> {
  const seen: string[] = []
  page.on('console', (m) => { if (/Content.Security.Policy/i.test(m.text())) seen.push(m.text()) })
  const listen = () => document.addEventListener('securitypolicyviolation', (e) => console.error(`Content Security Policy violation: ${e.violatedDirective} ${e.blockedURI}`))
  await page.addInitScript(listen)
  await page.evaluate(listen)
  return seen
}
```

- [ ] **Step 3: `authoring.spec.ts`**

```ts
import { expect, test } from '@playwright/test'
import { login, setEditorText, watchCsp } from './helpers'

// Idempotent on a KEEP=1 stack: every name carries a run id; the reading renamed and the hint deleted are whichever
// exist now, and a fresh hint file is left for the next run.
const run = Date.now().toString(36)

test('a leader shapes Forge 103 in the editor, a maintainer merges it, the trainee reads it', async ({ browser }) => {
  const leader = await login(browser, 'leader')
  const csp = await watchCsp(leader)
  const workers: string[] = []
  leader.on('worker', (w) => workers.push(w.url()))
  const files: string[] = (await (await leader.request.get('/api/content/forge-103/files')).json()).files.map((f: { path: string }) => f.path)
  const scroll = files.find((p) => /^modules\/01-anvil\/reading\/scroll[^/]*\.md$/.test(p))!
  const hint = files.find((p) => /^modules\/01-anvil\/hints\/h-[^/]*\.md$/.test(p))!
  const renamed = `modules/01-anvil/reading/scroll-${run}.md`
  const mod = `02-m${run}`

  await leader.getByRole('link', { name: 'Edits', exact: true }).click()
  await leader.getByLabel('Training').selectOption('forge-103')
  await leader.getByRole('button', { name: 'Start an edit' }).click()
  await expect(leader.getByRole('region', { name: 'Explorer' })).toBeVisible()
  await leader.getByLabel('Title').fill(`Shape the smithy ${run}`)

  // Blocks: a module, then a quiz in it. The quiz opens with the insertion selected; the preview marks the answer.
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Module with a reading', exact: true }).click()
  await leader.getByLabel(/^id/).fill(mod)
  await leader.getByLabel(/^title/).fill(`Embers ${run}`)
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(leader.getByRole('button', { name: `modules/${mod}/module.yaml`, exact: true })).toBeVisible()
  await leader.getByRole('button', { name: 'Blocks', exact: true }).click()
  await leader.getByRole('button', { name: 'Quiz file', exact: true }).click()
  await leader.getByLabel(/^module/).selectOption(mod)
  await leader.getByLabel(/^prompt/).fill('Which iron do you strike?')
  await leader.getByLabel(/^options/).fill('Cold iron\nHot iron')
  await leader.getByLabel(/^answer/).fill('1')
  await leader.getByRole('button', { name: 'Add to the draft' }).click()
  await expect(leader.getByTestId('edit-preview')).toContainText('Which iron do you strike?')
  await expect(leader.getByTestId('edit-preview')).toContainText('✓ right answer')

  // Rename the reading: module.yaml now names a missing file. The problem squiggles; the leader fixes the item.
  await leader.getByRole('button', { name: 'Explorer', exact: true }).click()
  await leader.getByRole('button', { name: `Rename ${scroll}` }).click()
  await leader.getByLabel(`New path for ${scroll}`).fill(renamed)
  await leader.getByRole('button', { name: 'Rename', exact: true }).click()
  await leader.getByRole('button', { name: /^Problems \([1-9]/ }).click()
  await leader.getByRole('button', { name: /modules\/01-anvil\/module\.yaml:3/ }).click()
  await expect(leader.locator('.squiggly-error').first()).toBeVisible()
  const moduleYaml: string = (await (await leader.request.get('/api/content/forge-103/file?path=modules/01-anvil/module.yaml')).json()).content
  await setEditorText(leader, moduleYaml.replace(scroll.replace('modules/01-anvil/', ''), renamed.replace('modules/01-anvil/', '')))
  await expect(leader.getByRole('button', { name: 'Problems (0)' })).toBeVisible()

  // Delete the spare hint and leave a fresh one for the next run.
  await leader.getByRole('button', { name: 'Explorer', exact: true }).click()
  await leader.getByRole('button', { name: `Delete ${hint}` }).click() // the confirm dialog is accepted by login()
  await leader.getByRole('button', { name: 'New file' }).click()
  await leader.getByLabel('Path of the new file').fill(`modules/01-anvil/hints/h-${run}.md`)
  await leader.getByRole('button', { name: 'Create' }).click()
  await setEditorText(leader, 'A spare hint the authoring test deletes and replaces on every run.\n')

  await leader.getByRole('button', { name: /^Changes \(/ }).click()
  await expect(leader.getByRole('list', { name: 'Changes' })).toContainText('Renamed')
  await expect(leader.getByRole('list', { name: 'Changes' })).toContainText('Deleted')
  await leader.getByRole('button', { name: 'Submit for review' }).click()
  await expect(leader.getByTestId('edit-status')).toHaveText('pending')
  await expect(leader.getByTestId('diff')).toContainText(`rename to ${renamed}`)
  await expect(leader.getByTestId('diff')).toContainText('deleted file mode')

  expect(workers.length, 'Monaco and the YAML worker started').toBeGreaterThan(0)
  expect(workers.every((u) => u.startsWith('http://localhost:8080/')), workers.join(' ')).toBe(true)
  expect(csp, csp.join('\n')).toEqual([])

  const senior = await login(browser, 'senior')
  await senior.getByRole('link', { name: 'Edits', exact: true }).click()
  await senior.getByRole('link', { name: `Shape the smithy ${run}` }).click()
  await senior.getByRole('button', { name: 'Approve and merge' }).click()
  await expect(senior.getByTestId('edit-status')).toHaveText('merged', { timeout: 30_000 })

  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 103/ }).click()
  await expect(trainee.getByText(`Embers ${run}`)).toBeVisible({ timeout: 30_000 })
})
```

- [ ] **Step 4: `docs.spec.ts`**

```ts
import { expect, test } from '@playwright/test'
import { login, watchCsp } from './helpers'

test('each role opens Docs on its own section; ? links open the page about the screen; no CSP violations', async ({ browser }) => {
  for (const [user, section] of [['trainee', /\/docs\/trainees\//], ['leader', /\/docs\/leaders\//], ['admin', /\/docs\/admins\//], ['senior', /\/docs\/(scorers|authors)\//]] as const) {
    const page = await login(browser, user)
    const csp = await watchCsp(page)
    await page.getByRole('link', { name: 'Docs', exact: true }).click()
    await expect(page).toHaveURL(section)
    await expect(page.getByRole('navigation', { name: 'Docs' })).toBeVisible()
    await expect(page.getByText(/Last updated with Crucible/)).toBeVisible()
    expect(csp, `${user}: ${csp.join('\n')}`).toEqual([])
  }
  const trainee = await login(browser, 'trainee')
  const csp = await watchCsp(trainee)
  for (const [path, doc] of [['/', 'getting-started/the-hearth'], ['/trainings', 'getting-started/the-hearth'], ['/labs', 'trainees/cluster-and-aws-labs'],
    ['/connect', 'trainees/connect-your-laptop'], ['/settings', 'getting-started/themes-and-calm']] as const) {
    await trainee.goto(path)
    await trainee.getByRole('link', { name: 'Help for this page' }).click()
    await expect(trainee).toHaveURL(new RegExp(`/docs/${doc}$`))
    await expect(trainee.locator('.prose h1')).toBeVisible()
  }
  expect(csp, csp.join('\n')).toEqual([])
})
```

- [ ] **Step 5: Run the browser suite**

Run: `KEYCLOAK_PORT=8082 make local-check`
Expected: ends with `🔥 Local check passed. The forge holds.` Then run it again with `KEEP=1` and run `cd e2e && npx playwright test --project=local tests/authoring.spec.ts tests/docs.spec.ts` against the kept stack, to prove a second run is idempotent. Tear the stack down afterwards: `docker compose -f deploy/compose/docker-compose.yml down -v`.

If `.squiggly-error` is not Monaco 0.57's class, look for the marker class in the live DOM (`page.locator('.monaco-editor [class*="squiggly"]')`) and use that.

- [ ] **Step 6: Update docs/user in the same commit**

Check that every accessible name the e2e clicks matches the words the authoring docs use (button and panel names). Fix the page wherever it differs.

- [ ] **Step 7: Commit**

```bash
git add -- examples/forge-103 examples/platform/teams/forge/programs/forge-103.yaml e2e/tests/authoring.spec.ts e2e/tests/docs.spec.ts
git commit -m "test(e2e): authoring journey through the IDE and Blocks, Docs by role with ? links, zero CSP violations" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- examples/forge-103 examples/platform e2e/tests scripts/seed-git.sh scripts/local-check.sh docs/user
```

---

### Task 18: Docs pass, roadmap, final review and release checks

**Files:**
- Modify: `docs/user/**` (accuracy pass), `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`, `README.md`

- [ ] **Step 1: Docs accuracy pass**

Read every page under `docs/user` against the code it describes, and in the running app (`KEEP=1` stack). For each page, confirm every number, limit and button name. Confirm that every `/docs/…` link resolves: `grep -rhoE '\(/docs/[a-z0-9/-]+\)' docs/user | sort -u`, then check each slug against `curl -s localhost:8080/api/docs` while signed in, or against `go test ./internal/docs` plus the generated slugs.

- [ ] **Step 2: Roadmap and README**

In `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`:
- Add a "Docs & editor" coverage table: each section of `2026-10-07-docs-and-editor-design.md`, mapped to its proving tests (the test names from this plan).
- Add to **Known decisions to revisit**:
  - the files-map translation in `edits.NewEdit` goes after one release;
  - problem lines are heuristics (`authoring.locate`);
  - case-only renames are refused;
  - the AWS template and "new training" are git-only;
  - draft images have no preview asset base.

In `README.md` under **Authoring**, add one paragraph: the in-app editor (drafts autosaved, Blocks, review) and the Docs tab.

- [ ] **Step 3: Full verification**

Run, in order, one heavy suite at a time:

```bash
cd /Users/adelin/Projects/Crucible && export PATH=$PWD/.local/tools/go/bin:$PWD/.local/tools:$PATH
gofmt -l cmd internal docs && go vet ./... && go vet -tags cluster ./internal/labs && go test -race ./...
(cd web && npm test && npx tsc -b && npm run build && npm run lint)
bash deploy/helm/test.sh
KEYCLOAK_PORT=8082 make local-check
KEYCLOAK_PORT=8082 make cluster-check
```

Expected: all green. `npm run build` prints the check-chunks line. local-check ends with `🔥 Local check passed. The forge holds.` cluster-check ends with `🔥 Cluster check passed. The crucible holds.`

- [ ] **Step 4: Whole-branch review**

Use superpowers:requesting-code-review on `main..feat/docs-and-editor`. Give the reviewer the spec, this plan's **Review Focus** and **Rulings**, and ask them to check, in particular:
- every authoring route refuses enrolled users;
- no client-supplied sha reaches `edits.At` unvetted;
- `ApplyOps` and `PushEdit` never write through symlinks or outside `training.yaml`/`modules/<id>/`;
- the exec-bit rules;
- the CSP is unchanged;
- Monaco stays out of the main chunk.

Fix what they find, each fix with its test, and commit with pathspecs.

- [ ] **Step 5: Commit**

```bash
git commit -m "docs: Docs and editor coverage in the roadmap, README authoring note, Docs accuracy pass" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" -- docs/superpowers/plans/2026-10-05-crucible-roadmap.md README.md docs/user
```
