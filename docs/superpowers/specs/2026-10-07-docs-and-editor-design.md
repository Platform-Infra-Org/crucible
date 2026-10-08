# Docs tab, VS Code-style editor and building-blocks catalog

**Status:** approved design, 2026-10-07 · **Branch:** `feat/docs-and-editor`
**Builds on:** `2026-10-05-crucible-design.md` (§6 content editing, §12 UI) and the M7 content-edit work
(`internal/edits`, `internal/gitsync` ContentRepo).

## 1. Goals

1. **Docs tab.** An in-app guide that explains every Crucible capability, organised by role, and stays current:
   any change to a capability updates the docs in the same commit, enforced by a test.
2. **Better editing.** Replace the textarea editor with a VS Code-like IDE for training repos.
3. **Building blocks.** Let authors see every built-in block (modules, readings, quizzes and each question type,
   labs and their parts, AWS settings) and add them through guided forms.

### Decisions taken with the user

| Topic | Decision |
|---|---|
| Docs audience | Everyone, by role (trainee, author/maintainer, scorer, approver/leader, admin); opens on the signed-in user's roles, everything else one click away |
| Docs source | Markdown in this code repo, embedded in the app; updated in the same change as the feature; a coverage test fails on gaps |
| Editor engine | Monaco (the VS Code editor), loaded only on the editor page |
| Editor features | Code editor core, schema-aware YAML, file explorer, live preview |
| File operations | New file, rename/move and delete inside `modules/<id>/`; whole new modules; `training.yaml` cannot be deleted or renamed |
| Catalog UX | Blocks panel with registry-generated forms that write valid files; typing YAML stays available |
| Work in progress | Server-side drafts (Postgres), autosaved; "Submit for review" turns a draft into an edit |
| Running draft labs | Not in v1; authors use `crucible preview` (documented) |

### Non-goals (v1)

- Running a draft lab from the browser (laptop, cluster or AWS).
- Image or binary uploads into training repos.
- Org-specific docs in the platform repo.
- Real-time collaborative editing of one draft by several people.
- Full-text search service for Docs (title and heading search only).

## 2. Architecture

```
internal/content/blocks          block registry (Go): the single description of every content block
   ├─ schema.go                  → JSON Schema per file kind (training.yaml, module.yaml, quiz.yaml, lab.yaml)
   ├─ catalog.go                 → catalog entries, form definitions, examples, starter templates
   └─ registry_test.go           → coverage + example validity
internal/authoring               HTTP: schema, blocks, validate, drafts (new package)
internal/edits                   edits become ops (put / rename / delete); drafts submit through it
internal/gitsync                 ContentRepo applies ops; diff detects renames
docs/user/**.md                  Docs pages (embedded with go:embed), served by internal/docs
web/src/pages/Docs.tsx           Docs tab
web/src/pages/editor/…           Monaco IDE (lazy chunk), Blocks panel, preview
```

The content loader and lint (`internal/content`) remain the authority on validity. The registry only describes;
tests keep the two in step.

## 3. Block registry

Each block entry:

```go
type Block struct {
    ID       string   // "quiz.question.multi", "lab.task", "lab.hint", "lab.aws", …
    Group    string   // Training | Module | Reading | Quiz | Lab | AWS
    Title    string
    Summary  string   // one line, shown in the catalog
    Doc      string   // longer markdown, shown on hover and on the Docs reference page
    FileKind string   // training | module | quiz | lab | reading | script
    Fields   []Field  // name, type, required, enum, default, description, min/max
    Example  string   // valid YAML/Markdown snippet
    Insert   Insert   // how a form submission is written: new files, or a merge into an existing file at a path
}
```

Coverage: all of `content.Training`, `Module`, `Item`, `Quiz`, `Question` (single, multi, exact, regex, order,
match, terminal, text, upload, signoff), `Lab`, `Task` (check, setup, quiz, review), `Script`, `Hint`, `Terminal`,
`AWSConfig`, plus lab compose files and check/setup scripts.

Generated outputs:

- `GET /api/authoring/schema` — JSON Schema per file kind, used by Monaco's YAML support.
- `GET /api/authoring/blocks` — catalog entries, form definitions, examples, starter templates.
- Docs reference pages under "Authors → Building blocks", generated from the registry at build time.

Tests:

- Reflection test: every exported, YAML-tagged field on the content types above has a registry `Field` with a
  description. Adding a content field without describing it fails the build.
- Every `Example` and every starter template passes `content.Load` and lint when placed in a minimal training repo.
- Generated schema validates every example in `examples/`.

## 4. Docs tab

### Pages

Markdown files in `docs/user/`, embedded into `crucible-api`, each with front matter:

```yaml
title: Running a laptop lab
roles: [trainee]                 # trainee | author | scorer | approver | leader | admin | everyone
covers: [route:/p/:team/:training/m/:module/lab, feature:lab.local, feature:agent]
order: 20
```

Starter set (about 25 pages):

- **Getting started** (everyone): signing in, the Hearth, themes and calm mode, notification settings.
- **Trainees:** readings; quizzes and attempts; laptop, cluster and AWS labs; connecting the laptop agent; hints and
  their cost; extensions; ranks and badges; feedback from scorers.
- **Authors:** repo layout; the editor; drafts and review; building blocks (generated); `crucible lint`;
  `crucible preview` (how to run a draft lab locally).
- **Scorers:** the Anvil; rubrics; scoring, returning and overriding; sign-offs.
- **Leaders and approvers:** teams and programs; enrolment; schedules; budgets and caps; approvals and extensions;
  Journey and Mentor; the Ledger; content versions.
- **Admins:** Forge Status; kill switch; agent tokens and revoking them; the first admin; audit log; links to the
  AWS and Cognito runbooks.

### In the app

- `GET /api/docs` returns the page index (title, roles, covers, order, headings); `GET /api/docs/{slug}` returns one
  page's markdown. Any signed-in user can read every page; pages never contain training content.
- **Docs** nav link for everyone. Sidebar lists the user's role sections first (roles from `/api/me` flags), with an
  "All" filter. Search filters by title and heading on the client.
- Pages render through the existing Markdown pipeline (no raw HTML; mermaid strict; highlighting).
- Every app page shows a small **?** link to the doc whose `covers` includes its route. Editor hovers and catalog
  entries link to their building-block reference page.
- Each page shows "Last updated with Crucible `<version>`".

### Keeping it current

- `docs_coverage_test`: fails when any SPA route in the nav, any registry block, or any role has no page whose
  `covers`/`roles` reference it, or when a page's `covers` names a route or block that no longer exists.
- `CLAUDE.md` rule: "Any capability change updates `docs/user` in the same commit." The pull-request checklist
  repeats it.

## 5. The editor

Layout (inside **Edits → New** and **Edits → Open draft**):

```
activity bar │ sidebar (Explorer | Blocks | Problems | Changes) │ editor tabs (Monaco) │ preview (toggle)
status bar: draft saved Ns ago · N problems · base commit · Submit for review
```

- **Monaco** is a lazily loaded chunk used only by the editor route; a build test asserts the main chunk does not
  include it. Its web workers are bundled as same-origin assets, so the CSP keeps `script-src 'self'` and no new
  hosts. Themes map to Crucible's: dark editor for Forge, Quench and High contrast; light for Anvil.
- **Explorer:** the training repo at the draft's base commit plus draft changes. New file / new module (optionally
  from a catalog template); rename/move and delete inside `modules/<id>/`; disallowed paths are greyed with the
  reason; `training.yaml` cannot be deleted or renamed.
- **Editing:** tabs with unsaved-change markers; find and replace; command palette (Ctrl/Cmd+Shift+P); go to file
  (Ctrl/Cmd+P). YAML gets autocomplete, enum suggestions and hover docs from the generated schema. Markdown links to
  assets and other readings are checked.
- **Problems panel:** `POST /api/authoring/validate` runs about a second after typing stops; problems show as
  squiggles and in the panel; clicking one jumps to the line.
- **Changes panel:** changed, added, renamed and deleted files with the same diff view reviewers use (hidden and
  control characters shown as markers).
- **Preview:** readings via the real reading renderer; `quiz.yaml` as a playable quiz with answers marked (visible
  only to people allowed to edit the training); `lab.yaml` as its task list, terminals and hints with costs. Nothing
  executes.
- **Accessibility:** every panel reachable by keyboard; Monaco's screen-reader mode on; Ctrl+Alt+↑ or Ctrl+Shift+F6
  leaves the editor (same as the lab terminal); focus rings and contrast follow the existing token tests.
- **Narrow screens:** below tablet width, explorer, editor and preview become tabs.

## 6. Drafts

Table `content_drafts` (next free migration, with Down): id, author, team, training, base_sha, ops (JSONB),
updated_at, submitted_edit_id.

- `GET/POST/PUT/DELETE /api/authoring/drafts[/{id}]`; autosave via `PUT` debounced about 2 s, with an
  `updated_at` compare-and-set so two tabs can't overwrite each other silently.
- Limits: 5 open drafts per author; the same file-count and size caps as edits; only people allowed to propose
  edits to that training (never enrolled users).
- **Submit:** creates an edit through the existing path (validation, push to its own branch, review). The draft is
  kept, linked to the edit, until the edit is merged, rejected or withdrawn; a returned edit reopens in the editor.
- **Discard:** deletes the draft.
- **Rebase:** when the training's tracked branch has moved, the editor offers "Newer content: rebase draft".
  Untouched files update silently; files changed on both sides open in Monaco's diff editor for the author to
  resolve.
- Audit: submit and discard are audited; autosaves are not.

## 7. Edits as operations

An edit becomes an ordered list of operations:

```json
[{"op":"put","path":"modules/03-x/quiz.yaml","content":"…"},
 {"op":"rename","from":"modules/01-a/reading/old.md","to":"modules/01-a/reading/new.md"},
 {"op":"delete","path":"modules/02-b/lab/hints/h2.md"}]
```

- Every path in every op passes the existing guards: `training.yaml` or `modules/<id>/…`; safe characters; no dot
  components; case-collision check against the tree and within the edit; executable only inside the lab directory
  named by `module.yaml`; no change to `maintainers`.
- `training.yaml` cannot be deleted or renamed. Deleting the last item of a module is allowed only if the module's
  `module.yaml` is also updated or the module folder is removed in the same edit (lint decides).
- The stored diff uses rename detection (`git diff -M --text --no-ext-diff --no-textconv`), shows deleted files in
  full, and stays under the existing full-diff size cap (edits whose diff would be truncated are refused).
- Merge rules unchanged: the bot merges exactly the reviewed head sha; conflicts mark the edit stale.
- Existing `files`-map requests are accepted and translated to `put` ops for one release, then removed.

## 8. Blocks catalog

- **Blocks panel:** groups (Training, Module, Reading, Quiz with each question type, Lab with tasks, checks, setup
  scripts, hints, terminals, review tasks, AWS). Each entry shows its summary and example.
- **Insert flow:** choose a block → a form generated from its registry fields (required markers, enums as selects,
  help text) → a target picker when needed ("Which module?", "Which lab?", listing the draft's own) → the server
  returns the resulting file changes as ops → they're applied to the draft and the affected file opens with the
  insertion highlighted; validation runs at once.
- **Merging into existing YAML** happens on the server with the existing `yamlx` node editing, preserving comments
  and formatting (for example appending a question to `quiz.yaml` or a task to `lab.yaml`).
- **Starter templates:** new training; module with a reading and a quiz; laptop lab with a break-fix task; cluster
  lab; AWS lab with an S3 bucket; human-scored review task.

## 9. Security

- Authoring endpoints (`schema`, `blocks`, `validate`, `drafts`, insert) require being allowed to propose edits for
  the training in question; enrolled users are refused (the existing `rbac.Enrolled` rule). Schema and blocks
  contain no training content.
- `validate` and insert run `content.Load` and lint in a temporary copy: no network, isolated git config, the
  existing terraform-lint parser caps, a per-user concurrency limit of one and a timeout.
- The playable quiz preview shows answers only to editors, who can already read the raw files.
- Monaco and its workers are same-origin; an e2e asserts zero CSP violations on the editor and Docs pages.
- Rename/delete ops get table and fuzz-style tests for path tricks, case collisions and executable-bit tricks.

## 10. Testing

- **Go:** registry coverage (reflection); examples and templates load and lint; schema generation and validation
  against `examples/`; validate endpoint (problems with line and column, caps, auth); drafts lifecycle,
  compare-and-set and rebase; ops guards; rename-aware stored diff; insert merges preserve comments; docs coverage.
- **Web:** editor layout and keyboard navigation; Blocks forms produce the expected ops; preview renderers; Docs
  sidebar ordering by role; ? links resolve.
- **End-to-end:** `authoring.spec.ts` — a leader opens the IDE, adds a module and a quiz question through Blocks,
  sees the preview, fixes a squiggled lint problem, renames a reading, deletes a hint file, submits; a maintainer
  approves; the trainee reads the result. `docs.spec.ts` — each role opens Docs and lands on its own section;
  ? links from five pages open the right doc; no CSP violations.

## 11. Delivery order

1. Block registry, generated schema, validate endpoint, coverage tests.
2. Docs tab: page set, nav link, ? links, coverage test, `CLAUDE.md` rule.
3. Edits as operations (rename/delete) and drafts with rebase.
4. Editor: Monaco layout, explorer, autocomplete, Problems, Changes, preview.
5. Blocks catalog: forms, inserts, starter templates.
6. End-to-end tests, Docs pages for the new editor, final review and release checks.

From step 2 on, every step updates `docs/user` in the same change.
