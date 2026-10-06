# M7 Forge & People (Release Candidate) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Trainees earn forge ranks (Ore → Masterwork) and per-training badges. Mentors get a dashboard, and leaders, managers and mentors get a journey heat map with "stuck" flags. Content can be edited in the app and goes through maintainer review before a bot merges it. Managers bump a program's pinned content with a diff summary first. Authors run `crucible preview`. The Forge Status page gains the operational views it was missing. The spec items deferred from earlier milestones land here ("Extension pending", review of self-reported results, and the smaller gaps listed below). Finally, a spec-coverage audit checks off every section of the spec, and a release-candidate verification run proves the whole thing.

**Architecture:**
- **Ranks and badges** live in `internal/learn` (the "assessment" module, which already owns `item_progress`). They are recomputed on every progress write. Two new tables hold them: `ranks`, which stores only the highest rank ever earned, and `badges`.
- **Journey and mentor views** are a new read-only package, `internal/journey`. It reads `learn`'s standings plus the lab, hint and submission tables.
- **Content edits** (spec §6) work on plain git, without a forge API:
  - `gitsync.ContentRepo` pushes an edit branch `crucible/edit/<id>` to the content repo, computes the diff, and later merges the branch into the tracked branch as a bot merge commit, with optimistic retry.
  - A new `internal/edits` package owns the `content_edits` table and the review queue. Its permission rules follow §5.3 plus "maintainers".
- **`crucible preview`** runs the real Crucible image in Docker Compose (Postgres + API) against a git snapshot of the author's working tree. It signs in through a token-guarded, loopback-only preview login and runs the local-lab agent in-process.
- **Everything else** is a focused change to an existing package: `labs` (extension approval, self-reported review, stuck-destroy alert), `configapi` (pin bump, Forge Status data, bootstrap admin), `config` and `content` (schema gaps), and `web/src` (pages, motion, accessibility).

**Tech Stack:** Go 1.26, pgx v5, goose, River, git CLI, React 19 + Vite + Vitest (`react-dom/server` for component tests: no new test deps), `motion`, `rehype-highlight` + `mermaid` (new, mermaid lazy-loaded), Playwright, Helm 3.17, Terraform 1.16 `terraform test` with mock providers only.

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md`. The parts this plan implements:
- §5.1 bootstrap admin, revocable pairing tokens
- §5.3 "Review/merge content edits" + maintainers
- §6 content edits, review, `preview`, pinned-ref bump with diff summary
- §7 reading polish, attempt limits, completion rule, forge ranks and badges
- §8.2 self-reported review
- §8.6 Extension pending
- §10 rank-up
- §11 mentor and journey view
- §12 navigation, motion, accessibility, Forge Status
- §13 `ranks`
- §14 component tests and the release check

Program context: `.superpowers/sdd/program-context.md`. Roadmap: `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`. Predecessor plans (M5, M6 not yet implemented when this was written): `docs/superpowers/plans/2026-10-06-m{3,4,5,6}-*.md`.

## Global Constraints

- Every shell starts with `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH` (go1.26.8, terraform 1.16.5). Never install anything system-wide. Extra tools go in `.local/tools/`.
- **NEVER touch real AWS.** No `terraform plan/apply` outside `terraform test` with `mock_provider`. No `aws` CLI calls. No `docker push`. Never set `CRUCIBLE_AWS_LABS=1` (only `dryrun` exists on this machine), and never set `INFRACOST_API_KEY`.
- `gofmt -l .` prints nothing, `go vet ./...` is clean, and `go test -race ./...` passes. Docker Desktop must run, because dbtest uses testcontainers Postgres 18.
- Acceptance: `KEYCLOAK_PORT=8082 make local-check` ends with `🔥 Local check passed. The forge holds.`, and `KEYCLOAK_PORT=8082 make cluster-check` ends with `🔥 Cluster check passed. The crucible holds.` `local-check` tears the stack down unless `KEEP=1`.
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Start after M5 and M6 are merged on `feat/m3-m7`.** Rebase first and keep their changes. See *Conflicts with M5/M6* below. Never edit an existing migration. Name new ones with the next free number (`ls internal/db/migrations`). This plan writes them as `000NN_forge.sql` (Task 4), `000NN_extension.sql` (Task 8) and `000NN_content_edits.sql` (Task 11). Expected numbers are 00009–00011.
- Spec §1/§7: **no leaderboards or comparisons between trainees.** Ranks are personal. Journey and mentor views list people one by one; they never rank them, sort by score, or show a "top" anything. Rank-up notifications go to the trainee and their mentor(s) only (`Event.Team` stays empty).
- Spec §5.3: "A trainee sees only their own scores; mentors see their mentees." Every journey/mentor query is gated by `rbac.Checker.Can(actor, rbac.ViewProgress, team, training, subject)`.
- **Answer keys never reach a trainee.** Quiz answers, rubrics, check and setup scripts and hint text stay hidden from trainees, as before. This now applies to the content-edit file API too: **nobody enrolled in a training (in any team) may read its files, propose edits to it, or review them.**
- Emails are lowercased wherever they are stored or compared. Text written to Postgres is valid UTF-8 without NUL. Use `scoring.Clean` from M5 if it exists; otherwise add `func clean(s string) string { return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "") }` locally.
- UI:
  - Themes `forge|anvil|quench|contrast` through the existing tokens.
  - Every new animation respects `useCalm()` and `[data-calm='true']`. Under calm or `prefers-reduced-motion`, essential state changes still show statically (spec §12).
  - New nav labels must not collide with link names used in `e2e/tests/*.spec.ts`. Run `grep -n "getByRole('link'" e2e/tests/*.ts` before naming links.
- These e2e specs keep passing **unchanged**, except where a task says otherwise: `forge-101.spec.ts`, `approvals.spec.ts` (M6 added Ledger lines), `scoring.spec.ts` (M5), `cluster-lab.spec.ts` (M4) and `aws-lab.spec.ts` (M6).
- **CARRY (M4):** no rank or percent assertion in `local-check` may assume Forge 101 reaches 100%. Its module 03 is a cluster lab, which only `cluster-check` can run. Rank e2e assertions use the new Forge 102 fixture and regexes over rank names, never a specific rank.

## Conflicts with M5/M6 (read before each task that touches these files)

M5 and M6 are planned but not built at the time of writing. This plan builds on their *planned* interfaces. If the real code differs, follow the real code and keep this plan's behaviour.

| File | M5/M6 change | What M7 does there |
|---|---|---|
| `internal/learn/service.go` | M5: `AnswerHuman`, `refreshQuiz`, `Refresh`, `ForceScore`, status `pending_review`, `Service.Scoring` | Task 3 changes `progress` to also return scores and makes `percent` weighted. Task 4 calls `UpdateForge` at the end of `SetItem` **and** `ForceScore`. Keep every M5 status (`pending_review` is never `complete`). |
| `internal/learn/quiz.go` | M5: `quizOutcome`, `Result.Status` | Task 3's attempt limit runs before scoring an instant attempt only. Human answers are not attempts. |
| `internal/labs/service.go` | M5: `finishTask`→`recompute`, `reviews`, `Refresh`. M6: `budgetLimit`, `CanExtend` adds `LimitReason != "budget"`, new `Extend` ponytail text, `labProvisioner`, async aws destroy | Task 8 replaces M6's ponytail branch in `Extend` with the extension request and keeps M6's budget clamp. Task 9 adds the self-reported branch inside M5's `recompute`. Task 15 adds the stuck alert in `Sweep` next to M6's 30-minute aws retry. |
| `internal/labs/approvals.go` | M6: `spend()` uses settled actuals | Task 8 adds extension rows to `Approvals`. Use `spend()` as-is. |
| `internal/scoring/*` | M5 creates it (`KindQuestion`, `KindTask`, `Submit`, `Latest`, `Labs.Refresh`) | Task 9 adds `KindLab = "lab"` and lets `Submit` accept it. |
| `internal/notify/notify.go` | M5: `SubmissionPending`, `SubmissionScored`. M6: `ReaperReport` | Append `RankUp` (Task 4), `ContentEdit` (Task 11) and `LabStuck` (Task 15) after them. |
| `internal/httpapi/server.go` (`/api/me`) | M5 `can_score`, M6 `can_view_spend` | Add `is_mentor` (Task 6) and `can_edit_content` (Task 11) the same way. |
| `cmd/crucible-api/main.go` | M5 blob/scoring wiring, M6 aws wiring | Add journey, edits and preview wiring next to them. |
| `web/src/{App.tsx,components/Nav.tsx,types.ts,pages/Lab.tsx,pages/Approvals.tsx}` | M5 Anvil, M6 Ledger | Task 16 sets the final nav order from spec §12. |
| `scripts/seed-git.sh`, `scripts/local-check.sh`, `examples/platform/trainings.yaml` | M5 `forge-301`, M6 `forge-401` | Task 20 adds `forge-102` the same way. |
| `deploy/helm/crucible/*`, `deploy/helm/test.sh` | M5 blob env, M6 awsLabs values/RBAC | Task 14 adds an assertion that the preview env is never rendered. Task 19 adds `bootstrapAdmin`. |

## Rulings made in this plan (read before starting)

1. **Ranks belong to `learn`.** `learn` already owns `item_progress`, and every progress write goes through `SetItem` (and M5's `ForceScore`). Both call `UpdateForge(ctx, userID)` after writing, and log its error instead of failing the write. No hook interface or job. `ranks` keeps only the highest rank ever earned (spec §7: "a rank once earned is never lost"). Its `seen` flag drives the one-time hammer-strike animation. `badges` keeps one row per user × training (spec: "Per-training completion grants a badge"), whichever team the completion came from.
2. **Weighted completion** (spec §7, "weighted by item points"):
   - An item's weight is its points: a reading counts 1, a quiz the sum of its question points, a lab the sum of its task points (points default to 1).
   - A program's completion is the weight of its `complete` items divided by its total weight. The rank % adds these up across every enrolled program whose content is available.
   - The program card and outline `percent` use the same function, so the Hearth bar and the rank never disagree. Existing tests that assert a percent are updated to the weighted value.
3. **Rank thresholds** come from `platform.yaml`, e.g. `ranks: {ingot: 20, tempered: 45, blade: 75, sword: 90, masterwork: 100}`. Any key may be omitted and falls back to its default. Values must strictly increase and stay ≤ 100, or the platform config is invalid. Rank names are fixed.
4. **Stuck flags** (spec §11), computed on read:
   - **`failed_checks`:** ≥ 3 failed `check_runs` on one task that is still not passed or skipped.
   - **`final_hint`:** the task's last hint was revealed. Shown even after the task passes: it says the trainee needed the solution.
   - **`inactive`:** no activity for ≥ 5 business days on a program that is not 100% done. Activity is the newest of `item_progress.updated_at`, `quiz_attempts.created_at`, `lab_instances.last_activity_at`, `hint_reveals.at` and `submissions.created_at`, falling back to the user's first sign-in. Business days are Mon–Fri in UTC, with no holidays (`// ponytail:` note).
   - **`returned_twice`:** ≥ 2 `returned` submissions for one item.
   - A trainee who never signed in shows all-cold cells and `inactive` with the detail "has not signed in yet".

   **Heat** per module:
   - `forged`: the module is complete under its completion rule.
   - `glowing`: any of its items has a progress row, or it has an active lab.
   - `cold`: otherwise.
5. **Content edits on plain git** (spec §6, §1 non-goal "PR integration with specific git hosts"):
   - **Push:** an edit is pushed at once to branch `crucible/edit/<id>`, based on the tracked branch head the author saw. That makes it visible to anyone with repo access. The commit's author is the user and its committer is the bot, with a `Crucible-Actor:` trailer.
   - **Review lives in Crucible:** the queue, a rendered preview of changed Markdown, the unified diff (computed by git at push time and stored on the row, capped at 256 KiB), and the lint result (`content.Load` of the edited tree; an invalid edit cannot be created).
   - **Merge:** on approval the bot runs `git merge --no-ff` of the branch into the tracked branch in its own working clone. It re-validates the merged tree with `content.Load` and pushes. A rejected push (the branch moved) re-fetches and re-merges, up to 3 retries.
   - **Conflict:** a merge conflict, or a merged tree that no longer validates, marks the edit **stale**. The author redoes it on the fresh base: the editor is pre-filled from the stale edit.
   - **Branch cleanup:** the edit branch is deleted on any final state (merged, rejected, withdrawn, stale). A failed delete is only logged.
   - **Who:** proposers are admins, the training's maintainers, and team leaders/seniors. Reviewers are admins and maintainers, **never the author**. Maintainers are read from the training version at the tracked head, never from the edit itself. Nobody enrolled in the training anywhere may propose, review, or read files through this API.
   - **What:** existing or new text files ending `.md .yaml .yml .sh`, ≤ 256 KiB each, ≤ 20 files per edit. No deletes or binaries (do those in git: YAGNI). New `.sh` files are committed `0755`.
   - **Who is told:** reviewers are notified (kind `content_edit`), and the author is notified of the decision.
6. **Pinned-ref bump** (spec §6, M3 deferral):
   - Program settings show the version the program runs and the tracked head. "Show changes" lists `git log --oneline` (≤ 50) and `git diff --stat` between them, read from the syncer's mirror. "Pin to head" writes `pinned_ref` to the program file through the existing `gitsync.Writer`.
   - Only a version the syncer validated can be pinned.
   - "Track the branch head" clears `pinned_ref`.
7. **Extension pending** (spec §8.6, deferred from M3/M6):
   - **Request:** when an extension would lift the estimate into a higher tier, `Extend` does not refuse. It records a request on the lab: `ext_until`, `ext_estimate_usd`, `ext_tier`, `ext_requested_at`. It sets `extended = true`, so a lab gets one extension attempt, as the spec says, and notifies that tier's deciders. The timer shows "Extension pending".
   - **Decision:** the request appears in Approvals as `kind: "extension"`. `POST /api/approvals/{id}/extension` decides it. Eligibility uses `MayApprove` with the new estimate, and over-cap means admin only, audited.
   - **Approval:** sets `ends_at` to the requested end, clamped by the schedule (and, after M6, the budget) limit, and updates `estimate_usd`.
   - **Ending:** a rejection or the lab's end clears the request. No escalation: the lab is gone within hours (`// ponytail:` note).
8. **Self-reported review** (spec §8.2, deferred from M5): a program flag `review_self_reported: true` in the program file, editable in Program settings.
   - **Submit:** when a `local` lab in such a program completes, the lab item becomes `pending_review` and one `scoring` submission is filed: `kind: "lab"`, `item: "lab"`, `qtype: "self_reported"`, `max_points` = the lab's total, and an answer summarising the self-reported task results. Scorers see the usual lab evidence (check runs, transcripts, hints).
   - **Score:** completes the lab item with the scorer's points / max.
   - **Return:** deletes that module's `lab_task_progress` rows, so the trainee redoes the lab. Hint reveals are kept, so hints are never charged twice.
9. **`crucible preview <dir>`** (spec §6):
   - **Image:** runs the shipped image (`--image`, default `crucible:dev`, built with `docker build -t crucible:dev .`) plus Postgres through a generated compose file in a temp dir, published on `127.0.0.1:<port>` (default 8090).
   - **Repos:** the working tree, including uncommitted files, is snapshotted into a temp bare repo every time it changes (2 s poll). Each new snapshot triggers an instant sync through the git webhook. A generated platform repo makes `preview@crucible.local` an admin and enrols them. `--free` rewrites `progression: free` in the snapshot only.
   - **Login:** the API gets `CRUCIBLE_PREVIEW_TOKEN` and skips OIDC. `GET /auth/preview?token=…` creates the session. The API **refuses to start** in preview mode unless `CRUCIBLE_PUBLIC_URL` is `http://localhost|127.0.0.1|[::1]` and the token is ≥ 32 characters. The Helm chart never renders the variable.
   - **Labs:** the CLI signs in with the token, creates a pairing token through the normal API, and runs `agent.Client` in-process, so `local` labs and their setup scripts work.
   - **Lint:** problems are printed to the terminal on every change.
10. **Forge Status** additions:
    - edits waiting for review (count + link);
    - labs needing attention: failed in the last 24 h, or destroying for > 10 min;
    - every program's running version vs head.

    Stuck destroys alert admins once per lab (spec §8.1): notify kind `lab_stuck`, recorded as a `lab_events` row `stuck_alerted` so the alert is never repeated.
11. **Navigation** (spec §12: "Hearth · Trainings · Labs · Anvil · Ledger · Forge Status · Team"):
    - **Trainings** is the global catalog: title, description, estimated hours, module count, and links for the teams you are enrolled through.
    - **Labs** lists your labs, active first, then the last 20.
    - Approvals, Mentor, Edits, Connect your laptop and Settings follow, each shown only to people who can use them.
    - Hearth becomes the personal dashboard: rank, badges, and program cards.
12. **Reading** (spec §7):
    - syntax highlighting with `rehype-highlight`;
    - fenced `mermaid` blocks rendered by a lazily imported `mermaid` with `securityLevel: 'strict'`, so it costs nothing on pages without diagrams;
    - GitHub-style callouts (`> [!NOTE]`, `[!TIP]`, `[!WARNING]`) through a blockquote renderer;
    - **scroll tracking** as a reading-progress bar (a `MoltenBar` pinned under the nav that follows scroll depth). "Mark as read" stays enabled: gating it on scroll would break keyboard and short-page users and the existing e2e.
13. **Smaller spec gaps closed here:**
    - inline schedule windows in program files (§4.3, deferred from M3);
    - quiz `max_attempts` and `cooldown` (§7);
    - module `completion: all_items | score` with `threshold` (§7);
    - `estimated_hours` in `training.yaml` (§4.2);
    - lint for broken `assets/` links and unresolvable relative links (§6);
    - bootstrap admin from Helm (§5.1);
    - "Revoke" for pairing tokens (§5.1);
    - Vitest component tests (§14).
14. **Accepted deviations** (recorded by Task 21 in the roadmap, not built):
    - `teams_cache`, `programs_cache` and `content_versions` are the in-memory `gitsync.State`, rebuilt from git on start;
    - `scores` is folded into `submissions` (M5), `lab_requests`/`approvals` into `lab_instances` (M3), and `cost_samples` into `lab_instances` (M6);
    - git sync and provisioning are goroutines, not River jobs (M4);
    - the lab egress allowlist is replaced by a deny-private NetworkPolicy (M4);
    - the AWS lab lives in Forge 401, not Forge 101 (M6);
    - the nightly AWS sandbox job is a runbook checklist (M6);
    - checks are tamper-resistant, not tamper-proof (M4 wording).

## Review Focus

1. **Someone gets around content-edit authority:**
   - an author adds themselves to `maintainers` inside their own edit and approves it;
   - a trainee enrolled in the training reads `quiz.yaml` answers through the file API;
   - a path such as `../platform.yaml`, `.git/hooks/post-merge` or `modules/x/../../.git/config`;
   - a symlinked folder in the repo;
   - two maintainers press Approve at the same moment.

   Expected: the author can never approve; enrolled users get 403 for files and proposals and cannot even see edits (404); bad paths get 400 before any git command; symlinks are refused; exactly one merge commit appears and the other approver gets 409. Pinned by `TestEditPermissions`, `TestEditPathRules`, `TestOnlyOneApprovalMerges` (Task 11) and `TestPushEditRefusesSymlinks` (Task 10).
2. **The tracked branch moves under an edit.** A conflicting push since the edit marks it stale, pushes nothing and leaves the branch tip unchanged. A non-conflicting push since the edit merges on top after a retry. A merge whose result no longer validates is refused and the edit marked stale. Pinned by `TestMergeConflictPushesNothing`, `TestMergeLandsOnTopOfNewerCommits` and `TestMergeRefusesInvalidResult` (Task 10), and `TestApproveStaleEdit` (Task 11).
3. **Ranks misbehave:**
   - enrolling in a new program halves the %;
   - an admin raises the thresholds in git;
   - two progress writes land at once;
   - a trainee has no enrolments, or only unavailable content.

   Expected: the rank never drops, the % is shown honestly, exactly one rank-up notification and one unseen animation per rank raise, naming the top rank crossed (one notice even when a single update crosses several levels), Ore at 0% with no division by zero, and no notification goes to a team webhook. Pinned by `TestRankIsNeverLost`, `TestRankUpNotifiesOnceTraineeAndMentor`, `TestForgeWithNoEnrolments` (Task 4) and `TestRankLadderFromConfig` (Task 1).
4. **An extension request decided by the wrong person or at the wrong time:**
   - the requester approves their own;
   - two approvers press at once;
   - the lab ends while the request is pending;
   - the new estimate passes the hard cap;
   - an approval that would run past the schedule window.

   Expected: self-approval forbidden; exactly one decision; ended labs drop out of the queue; over-cap is admin-only and audited; an approved end is clamped at the window close, and one that cannot move the end clears the request with a clear 409. Pinned by `TestExtensionGoesToApproval`, `TestExtensionDecisionRules` and `TestExtensionApprovalNeverPassesTheWindow` (Task 8).
5. **Preview mode reaches production.** Cases: `CRUCIBLE_PREVIEW_TOKEN` set with an `https://` public URL, a non-loopback host, or a 10-character token; a wrong token on `/auth/preview`; the Helm chart. Expected: the API refuses to start with a clear message; a wrong token gets 404 and no session; the chart never contains `CRUCIBLE_PREVIEW`. Pinned by `TestPreviewAllowed`, `TestPreviewLogin` (Task 14) and the `deploy/helm/test.sh` assertion (Task 14).

---

## File Structure

```
internal/config/config.go, config_test.go                    Task 1  ranks thresholds, review_self_reported, inline schedules
internal/configapi/configapi.go, configapi_test.go           Tasks 1, 9, 13, 15, 19  keep inline schedule; flag; pin; Forge Status data; SeedAdmin
internal/content/types.go, load.go, load_test.go             Task 2  estimated_hours, completion rule, max_attempts/cooldown, link lint, AssetTypes
internal/learn/service.go, quiz.go, http.go, *_test.go       Task 3  weighted percent, completion rule, attempt limits, Standing
internal/db/migrations/000NN_forge.sql                       Task 4  ranks, badges
internal/learn/forge.go, forge_test.go                       Task 4  ranks, badges, rank-up notify, /api/me/forge
internal/notify/notify.go                                    Tasks 4, 11, 15  RankUp, ContentEdit, LabStuck
web/src/components/{RankCard,RankUp}.tsx, pages/Hearth.tsx   Task 5  rank, badges, hammer strike
web/src/components/*.test.tsx, web/vite.config.ts            Tasks 5, 7, 8, 18  component tests (react-dom/server)
internal/journey/journey.go, flags.go, http.go, *_test.go    Task 6  heat map, stuck flags, mentor dashboard
internal/httpapi/server.go                                   Tasks 6, 11, 14  is_mentor, can_edit_content, preview login
web/src/pages/{Journey,Mentor}.tsx, components/HeatMap.tsx   Task 7
internal/db/migrations/000NN_extension.sql                   Task 8  ext_* columns
internal/labs/{model.go,service.go,approvals.go,http.go,extension_test.go}  Task 8
web/src/components/Timer.tsx, pages/Approvals.tsx            Task 8
internal/scoring/scoring.go, internal/labs/review.go, selfreport_test.go  Task 9
web/src/pages/ProgramSettings.tsx                            Tasks 9, 13
internal/gitsync/content_repo.go, content_repo_test.go, writer.go  Task 10  ContentRepo, shared clone helper, NoSymlinks
internal/db/migrations/000NN_content_edits.sql               Task 11
internal/edits/edits.go, http.go, edits_test.go              Task 11
web/src/pages/{Edits,EditReview,EditFiles}.tsx, pages/Reading.tsx  Task 12
internal/gitsync/syncer.go (Changes), syncer_test.go         Task 13
internal/auth/preview.go, preview_test.go                    Task 14
cmd/crucible/preview.go, preview_test.go, main.go            Task 14
cmd/crucible-api/main.go                                     Tasks 4, 6, 11, 14, 19
deploy/helm/test.sh, deploy/helm/crucible/{values.yaml,templates/crucible.yaml}  Tasks 14, 19
internal/labs/service.go (Sweep), stuck_test.go              Task 15
web/src/pages/ForgeStatus.tsx                                Task 15
internal/learn/catalog.go, internal/labs/mine.go, web/src/pages/{Trainings,Labs}.tsx, components/Nav.tsx, App.tsx  Task 16
web/src/components/Markdown.tsx, pages/Reading.tsx, theme/app.css, package.json  Task 17
web/src/pages/Lab.tsx, theme/{app.css,tokens.css}, lib/contrast.ts(+test)  Task 18
internal/auth/store.go, web/src/pages/Connect.tsx            Task 19  revoke pairing token
examples/forge-102/**, examples/platform/trainings.yaml, scripts/{seed-git,local-check}.sh, e2e/tests/forge-people.spec.ts  Task 20
docs/superpowers/plans/2026-10-05-crucible-roadmap.md         Tasks 21, 22  coverage table with proofs; M7 row
README.md, docs/runbooks/aws.md                              Tasks 14, 19, 21  preview + bootstrap admin docs
```

Order and dependencies:
- Tasks 1 → 2 → 3 → 4 → 5.
- Task 6 needs 3; Task 7 needs 6.
- Task 8 stands alone (labs). Task 9 needs 1 (flag) and M5.
- Task 10 → 11 → 12. Task 13 needs 1.
- Task 14 stands alone. Task 15 needs 11 (edit count). Task 16 needs 2.
- Tasks 17, 18 and 19 stand alone.
- Task 20 needs 5, 7, 12 and 16. Task 21 needs everything. Task 22 is last.
- Parallel lanes: {1–5}, {8}, {10–12}, {14}, {17, 18, 19}.

---
### Task 1: Config: rank thresholds, `review_self_reported`, inline program schedules

**Files:**
- Modify: `internal/config/config.go` (`Settings.Ranks`, `RankThresholds`, `Program.ScheduleSpec/Inline/ReviewSelfReported`, `ScheduleRef`, `ProgramSchedule`, `loadTeam`)
- Modify: `internal/config/config_test.go`
- Modify: `internal/configapi/configapi.go` (`ProgramView.InlineSchedule`; `SetProgram` keeps inline windows)
- Modify: `internal/configapi/configapi_test.go`
- Modify: every caller that *displays* `Program.Schedule` as a name (run `grep -rn '\.Schedule\b' internal --include=*.go | grep -v _test`). Show `inline` when `Inline != nil`.

**Interfaces:**
- Produces:
  ```go
  type RankThresholds struct{ Ingot, Tempered, Blade, Sword, Masterwork float64 } // yaml/json: ingot tempered blade sword masterwork
  var DefaultRanks = RankThresholds{Ingot: 20, Tempered: 45, Blade: 75, Sword: 90, Masterwork: 100}
  func (r RankThresholds) Steps() []float64 // [0, ingot, tempered, blade, sword, masterwork]
  Settings.Ranks RankThresholds            // yaml "ranks"; missing keys take DefaultRanks
  Program.Inline *Schedule                 // inline windows (validated); Program.Schedule stays the *name* ("" when inline)
  Program.ReviewSelfReported bool          // yaml "review_self_reported"
  type ScheduleRef struct{ Name string; Inline *Schedule } // yaml "schedule": a scalar name or a mapping
  ProgramView.InlineSchedule string        // json "inline_schedule,omitempty": Inline.String()
  ```

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go` (add `slices` to the imports if missing):

```go
// platformDir writes a minimal valid platform repo plus overrides.
func platformDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"platform.yaml":            "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n",
		"trainings.yaml":           "trainings:\n  t1: {repo: file:///nowhere}\n",
		"teams/a/team.yaml":        "name: A\nleader: l@x\ntrainees: [u@x]\n",
		"teams/a/programs/t1.yaml": "enrolled: [u@x]\n",
	}
	for k, v := range files {
		all[k] = v
	}
	for rel, body := range all {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRankLadderFromConfig(t *testing.T) {
	tiers := "cost_tiers: {auto_approve_usd: 0, tier1_usd: 5, tier2_usd: 25}\n"
	p, err := Load(platformDir(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Settings.Ranks.Steps(); !slices.Equal(got, []float64{0, 20, 45, 75, 90, 100}) {
		t.Fatalf("defaults: %v", got)
	}
	p, err = Load(platformDir(t, map[string]string{"platform.yaml": tiers + "ranks: {blade: 70}\n"}))
	if err != nil || !slices.Equal(p.Settings.Ranks.Steps(), []float64{0, 20, 45, 70, 90, 100}) {
		t.Fatalf("one override: %v %v", p.Settings.Ranks.Steps(), err)
	}
	for _, bad := range []string{"ranks: {ingot: 50, tempered: 40}\n", "ranks: {masterwork: 120}\n", "ranks: {ingot: -1}\n", "ranks: {blade: 95}\n"} {
		if _, err := Load(platformDir(t, map[string]string{"platform.yaml": tiers + bad})); err == nil || !strings.Contains(err.Error(), "ranks") {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
}

func TestInlineProgramScheduleAndReviewFlag(t *testing.T) {
	prog := "enrolled: [u@x]\nreview_self_reported: true\nschedule:\n  timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	p, err := Load(platformDir(t, map[string]string{"teams/a/programs/t1.yaml": prog}))
	if err != nil {
		t.Fatal(err)
	}
	pr := p.Teams["a"].Programs["t1"]
	if pr.Schedule != "" || pr.Inline == nil || !pr.ReviewSelfReported || p.ProgramSchedule("a", "t1") != pr.Inline {
		t.Fatalf("inline schedule: %+v", pr)
	}
	if !strings.Contains(pr.Inline.String(), "08:00") {
		t.Fatalf("inline schedule is validated and printable: %q", pr.Inline.String())
	}
	bad := "enrolled: [u@x]\nschedule:\n  timezone: Mars/Olympus\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	if _, err := Load(platformDir(t, map[string]string{"teams/a/programs/t1.yaml": bad})); err == nil || !strings.Contains(err.Error(), "programs/t1.yaml: schedule") {
		t.Fatalf("a bad inline schedule names the file: %v", err)
	}
}
```

Append to `internal/configapi/configapi_test.go`:

```go
func TestSetProgramKeepsInlineSchedule(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	inline := "training: forge-101\nenrolled: [trainee@crucible.local]\nschedule:\n  timezone: Europe/Bucharest\n  windows: [{days: [mon], start: \"08:00\", end: \"10:00\"}]\n"
	f.push(t, map[string]string{"teams/forge/programs/forge-101.yaml": inline})
	tv, err := f.s.Team(f.leader, "forge")
	if err != nil || tv.Programs[0].InlineSchedule == "" || tv.Programs[0].Schedule != "" {
		t.Fatalf("view shows the inline schedule: %+v %v", tv.Programs, err)
	}
	b := emptyProgram(f.sha())
	b.Enrolled = []string{"trainee@crucible.local"}
	if _, err := f.s.SetProgram(ctx, f.leader, "forge", "forge-101", b); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:teams/forge/programs/forge-101.yaml"); !strings.Contains(got, "timezone: Europe/Bucharest") {
		t.Fatalf("saving without picking a named schedule must keep the inline one:\n%s", got)
	}
}
```

(`f.push` exists in this file and re-syncs; if it does not resync, call `f.sync.SyncOnce(ctx)` after it.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config/ ./internal/configapi/ -run 'RankLadder|InlineProgram|KeepsInline' -v`
Expected: compile errors (`Settings.Ranks`, `Inline`, `ReviewSelfReported`, `InlineSchedule` undefined).

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add `"gopkg.in/yaml.v3"` to the imports, then add next to `CostTiers`:

```go
// RankThresholds are the % of enrolled training completed at which each forge rank is earned (spec §7). Ore is 0.
type RankThresholds struct {
	Ingot      float64 `yaml:"ingot" json:"ingot"`
	Tempered   float64 `yaml:"tempered" json:"tempered"`
	Blade      float64 `yaml:"blade" json:"blade"`
	Sword      float64 `yaml:"sword" json:"sword"`
	Masterwork float64 `yaml:"masterwork" json:"masterwork"`
}

// DefaultRanks are spec §7's thresholds: Ore 0% → Ingot 20% → Tempered 45% → Blade 75% → Sword 90% → Masterwork 100%.
var DefaultRanks = RankThresholds{Ingot: 20, Tempered: 45, Blade: 75, Sword: 90, Masterwork: 100}

// Steps lists the thresholds from Ore to Masterwork.
func (r RankThresholds) Steps() []float64 {
	return []float64{0, r.Ingot, r.Tempered, r.Blade, r.Sword, r.Masterwork}
}

// fill applies the defaults to unset keys and checks that every rank needs more than the one before.
func (r *RankThresholds) fill() error {
	d := DefaultRanks
	for _, f := range []struct {
		v   *float64
		def float64
	}{{&r.Ingot, d.Ingot}, {&r.Tempered, d.Tempered}, {&r.Blade, d.Blade}, {&r.Sword, d.Sword}, {&r.Masterwork, d.Masterwork}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	s := r.Steps()
	for i := 1; i < len(s); i++ {
		if s[i] <= s[i-1] {
			return fmt.Errorf("each rank must need more than the one before (0 < ingot < tempered < blade < sword < masterwork), got %v", s[1:])
		}
	}
	if r.Masterwork > 100 {
		return errors.New("masterwork must be at most 100")
	}
	return nil
}
```

Add `Ranks RankThresholds \`yaml:"ranks"\`` to `Settings`. In `Load`, after the escalation checks:

```go
	if err := p.Settings.Ranks.fill(); err != nil {
		errs = append(errs, fmt.Errorf("platform.yaml: ranks: %w", err))
	}
```

Replace the `Program` struct's `Schedule` line and add the flag and the reference type:

```go
type Program struct {
	Training    string      `yaml:"training"`
	PinnedRef   string      `yaml:"pinned_ref"`
	Roles       Roles       `yaml:"roles"`
	Enrolled    []string    `yaml:"enrolled"`
	LabDefaults LabDefaults `yaml:"lab_defaults"`

	ScheduleSpec       ScheduleRef `yaml:"schedule"`             // a schedule name from platform.yaml, or inline windows (spec §4.3)
	Schedule           string      `yaml:"-"`                    // the named schedule; "" = inline or any time
	Inline             *Schedule   `yaml:"-"`                    // inline windows, validated
	BudgetUSDMonth     float64     `yaml:"budget_usd_month"`     // the program's monthly budget and hard cap; 0 = none
	ReviewSelfReported bool        `yaml:"review_self_reported"` // spec §8.2: completed local labs wait for a scorer
}

// ScheduleRef is a program's `schedule:` value: a name, or a mapping with timezone and windows.
type ScheduleRef struct {
	Name   string
	Inline *Schedule
}

func (r *ScheduleRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&r.Name)
	}
	r.Inline = &Schedule{}
	return n.Decode(r.Inline)
}
```

In `loadTeam`, right after the program file is read (`yamlx.ReadFile(f, pr, true)` succeeded), add:

```go
		pr.Schedule, pr.Inline = pr.ScheduleSpec.Name, pr.ScheduleSpec.Inline
		if pr.Inline != nil {
			if err := pr.Inline.validate(); err != nil {
				bad("programs/%s.yaml: schedule: %v", name, err)
			}
		}
```

Change `ProgramSchedule`:

```go
func (p *Platform) ProgramSchedule(team, training string) *Schedule {
	t := p.Teams[team]
	if t == nil || t.Programs[training] == nil {
		return nil
	}
	if pr := t.Programs[training]; pr.Inline != nil {
		return pr.Inline
	}
	return p.Settings.Schedules[t.Programs[training].Schedule]
}
```

In `internal/configapi/configapi.go`, add `InlineSchedule string \`json:"inline_schedule,omitempty"\`` to `ProgramView`. In `Team()`, set it: `if p.Inline != nil { pv.InlineSchedule = p.Inline.String() }` (build the `ProgramView` into a variable first). In `SetProgram`, just before `rel := path.Join(...)`:

```go
	if b.Schedule == "" && exists && t.Programs[training].Inline != nil {
		delete(set, "schedule") // inline windows live in git; the UI only picks named schedules, so keep them
	}
```

In the web Program settings page, show `inline_schedule` as read-only text ("Inline schedule (edit in git): …") above the schedule select when it is set. Add `inline_schedule?: string` to `ProgramConfig` in `web/src/types.ts`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config/ ./internal/configapi/ ./internal/labs/ -race`
Expected: PASS. The labs tests prove that `ProgramSchedule` still resolves named schedules (`onSchedule` sets `Program.Schedule`).

- [ ] **Step 5: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/config internal/configapi internal/labs web/src
git commit -m "feat(config): rank thresholds, review_self_reported, inline program schedules

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Content: estimated hours, completion rule, quiz attempts and cooldown, link lint

**Files:**
- Modify: `internal/content/types.go` (`Training.EstimatedHours`, `Module.Completion/Threshold`, `Quiz.MaxAttempts/Cooldown`, `AssetTypes`)
- Modify: `internal/content/load.go` (validation + `links`)
- Modify: `internal/content/load_test.go`
- Modify: `internal/learn/http.go` (use `content.AssetTypes`; delete the local `assetTypes`)

**Interfaces:**
- Produces:
  ```go
  Training.EstimatedHours float64 // yaml estimated_hours, >= 0
  Module.Completion string        // yaml completion: all_items (default) | score
  Module.Threshold float64        // yaml threshold: required for score, 0 < t <= 1
  Quiz.MaxAttempts int            // yaml max_attempts: 0 = unlimited
  Quiz.Cooldown yamlx.Duration    // yaml cooldown: 0 = none
  var AssetTypes map[string]bool  // extensions served from a training's assets/
  ```

- [ ] **Step 1: Write the failing tests**

Add these entries to the `cases` map in `TestLoadProblems` (`internal/content/load_test.go`). Each value is `{files to overwrite, expected substring}`; keep the existing table's field names:

```go
		"negative hours":          {map[string]string{"training.yaml": "id: t\ntitle: T\nmodules: [m1]\nestimated_hours: -1\n"}, "estimated_hours"},
		"unknown completion":      {map[string]string{"modules/m1/module.yaml": "title: M\ncompletion: vibes\nitems:\n  - reading: reading/r.md\n"}, "completion must be all_items or score"},
		"score needs threshold":   {map[string]string{"modules/m1/module.yaml": "title: M\ncompletion: score\nitems:\n  - reading: reading/r.md\n"}, "threshold"},
		"threshold without score": {map[string]string{"modules/m1/module.yaml": "title: M\nthreshold: 0.5\nitems:\n  - reading: reading/r.md\n"}, "threshold only applies"},
		"negative attempts":       {map[string]string{"modules/m1/quiz.yaml": "max_attempts: -1\nquestions:\n  - {id: q1, type: exact, prompt: X, answer: \"1\"}\n"}, "max_attempts"},
		"broken asset link":       {map[string]string{"modules/m1/reading/r.md": "# R\n\n![x](assets/missing.png)\n"}, "broken asset link"},
		"relative link":           {map[string]string{"modules/m1/reading/r.md": "# R\n\nSee [the lab](../m2/lab/tasks/01.md).\n"}, "won't resolve"},
```

If the table's base repo differs (module id, reading path), adapt the paths to it: the base repo is defined at the top of `TestLoadProblems`. Add one passing case:

```go
func TestLinksThatResolve(t *testing.T) {
	dir := writeRepo(t, map[string]string{ // writeRepo: the helper TestLoadProblems uses to build its base repo
		"modules/m1/reading/r.md": "# R\n\n![logo](assets/logo.png) [docs](https://example.com) [top](#r) [mail](mailto:a@b)\n\n```md\n[not a link](nowhere.md)\n```\n",
		"assets/logo.png":         "png",
	})
	if _, probs := Load(dir); len(probs) > 0 {
		t.Fatalf("valid links flagged: %v", probs)
	}
}
```

The fenced example `[not a link](nowhere.md)` *will* be flagged by the simple scanner below. Strip fenced blocks before scanning (Step 3 does), which is why this case is in the test.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/content/ -run 'LoadProblems|LinksThatResolve' -v`
Expected: FAIL. The new cases report "expected problem … got none".

- [ ] **Step 3: Implement**

`internal/content/types.go`:

```go
// AssetTypes are the only files served from a training's assets/ (images and fonts).
var AssetTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true, ".woff2": true}
```

Add `EstimatedHours float64 \`yaml:"estimated_hours" json:"estimated_hours"\`` to `Training`, `Completion string \`yaml:"completion"\`` and `Threshold float64 \`yaml:"threshold"\`` to `Module`, and `MaxAttempts int \`yaml:"max_attempts"\`` and `Cooldown yamlx.Duration \`yaml:"cooldown"\`` to `Quiz`.

`internal/content/load.go`:
- In `Load`, after the progression check: `if t.EstimatedHours < 0 { l.add(tf, "estimated_hours must not be negative") }`.
- Just before `if len(l.probs) > 0 { return nil, l.probs }`, add `l.links(dir)`.
- In `module`, after the title check:

```go
	switch {
	case m.Completion == "":
		m.Completion = "all_items"
		if m.Threshold != 0 {
			l.add(mf, "threshold only applies to completion: score")
		}
	case m.Completion == "all_items" && m.Threshold != 0:
		l.add(mf, "threshold only applies to completion: score")
	case m.Completion == "score":
		if m.Threshold <= 0 || m.Threshold > 1 {
			l.add(mf, "completion: score needs a threshold between 0 and 1 (e.g. 0.7)")
		}
	case m.Completion != "all_items":
		l.add(mf, "completion must be all_items or score")
	}
```

- In `quiz`, after the pass-threshold check: `if q.MaxAttempts < 0 { l.add(path, "max_attempts must not be negative (0 = unlimited)") }` and `if q.Cooldown < 0 { l.add(path, "cooldown must not be negative") }`.
- Add the link checker:

```go
var (
	mdLink  = regexp.MustCompile(`!?\[[^\]]*\]\(\s*<?([^)\s>]+)`)
	mdFence = regexp.MustCompile("(?ms)^(```|~~~).*?^(```|~~~)")
)

// links checks Markdown links and images under modules/ (spec §6 "broken links/assets"). assets/… must exist under the
// repo's assets/ with a type the app serves. Any other relative target can't resolve inside Crucible, so it is
// reported. Fenced code blocks are skipped. ponytail: a regex scan, not a Markdown parser; inline code spans are not
// skipped.
func (l *loader) links(dir string) {
	_ = filepath.WalkDir(filepath.Join(dir, "modules"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range mdLink.FindAllSubmatch(mdFence.ReplaceAll(b, nil), -1) {
			target := string(m[1])
			switch {
			case strings.HasPrefix(target, "#"), strings.HasPrefix(target, "/"), strings.Contains(target, "://"), strings.HasPrefix(target, "mailto:"):
			case strings.HasPrefix(target, "assets/"):
				rel, _, _ := strings.Cut(strings.TrimPrefix(target, "assets/"), "#")
				if !filepath.IsLocal(filepath.FromSlash(rel)) {
					l.add(p, "asset link %q leaves assets/", target)
					continue
				}
				fp := filepath.Join(dir, "assets", filepath.FromSlash(rel))
				if fi, err := os.Stat(fp); err != nil || !fi.Mode().IsRegular() {
					l.add(p, "broken asset link %q", target)
				} else if !AssetTypes[strings.ToLower(filepath.Ext(fp))] {
					l.add(p, "asset %q is not an image or font Crucible serves", target)
				}
			default:
				l.add(p, "relative link %q won't resolve in Crucible; use assets/… or a full URL", target)
			}
		}
		return nil
	})
}
```

In `internal/learn/http.go`, delete `assetTypes` and use `content.AssetTypes`.

- [ ] **Step 4: Run the tests, then lint every fixture**

Run:
```bash
go test ./internal/content/ ./internal/learn/ -race
go run ./cmd/crucible lint examples/forge-101 && go run ./cmd/crucible lint examples/forge-201 && go run ./cmd/crucible lint examples/forge-301 && go run ./cmd/crucible lint examples/forge-401
```
Expected: PASS, and every lint prints no problems. If a fixture has a relative link, fix the fixture, not the rule.

- [ ] **Step 5: Commit**

```bash
git add internal/content internal/learn examples
git commit -m "feat(content): estimated hours, module completion rule, quiz attempt limits, link lint

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Learn: weighted percent, the completion rule, attempt limits, `Standing`

**Files:**
- Modify: `internal/learn/service.go` (`progress` returns scores too; `Weight`, `moduleComplete`, `completion`, `percent`, `outline`, `Standing`, `attemptGate`, `QuizView` fields, `SubmitQuiz`)
- Modify: `internal/learn/service_test.go` (the existing `o.Percent != 40` assertion becomes the weighted value)
- Create: `internal/learn/weight_test.go`
- Modify: `web/src/pages/Quiz.tsx`, `web/src/types.ts` (attempts left, next attempt)

**Interfaces:**
- Consumes: Task 2 fields.
- Produces:
  ```go
  func Weight(m *content.Module, it content.Item) float64
  type Standing struct {
      Training    *content.Training
      Modules     []ModuleView
      Percent     int
      Done, Total float64           // weighted
      Status      map[string]string // "module/item" → status
  }
  func (s *Service) Standing(ctx context.Context, userID int64, team, training string) (*Standing, error) // nil, nil when content is unavailable
  QuizView.AttemptsLeft *int        // json attempts_left,omitempty
  QuizView.NextAttemptAt *time.Time // json next_attempt_at,omitempty
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/learn/weight_test.go`:

```go
package learn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

func TestCompletionIsWeightedAndHonoursTheScoreRule(t *testing.T) {
	m1 := &content.Module{ID: "m1", Completion: "all_items", Items: []content.Item{{Kind: "reading", ID: "r"}, {Kind: "quiz", ID: "quiz"}},
		Quiz: &content.Quiz{Questions: []*content.Question{{Points: 3}}}}
	m2 := &content.Module{ID: "m2", Completion: "score", Threshold: 0.5, Items: []content.Item{{Kind: "lab", ID: "lab"}},
		Lab: &content.Lab{Tasks: []*content.Task{{Points: 2}, {Points: 2}}}}
	tr := &content.Training{Progression: "linear", Modules: []*content.Module{m1, m2}}

	done, total := completion(tr, progress{"m1/r": "complete"}, scores{"m1/r": 1})
	if done != 1 || total != 8 {
		t.Fatalf("reading 1 + quiz 3 + lab 4 = 8; done %v total %v", done, total)
	}
	if p := percent(tr, progress{"m1/r": "complete"}, scores{"m1/r": 1}); p != 12 {
		t.Fatalf("1/8 = 12%%, got %d", p)
	}
	prog := progress{"m1/r": "complete", "m1/quiz": "complete", "m2/lab": "in_progress"}
	sc := scores{"m1/r": 1, "m1/quiz": 1, "m2/lab": 0.6}
	o := outline(tr, prog, sc)
	if !o[0].Complete || o[1].Locked || !o[1].Complete {
		t.Fatalf("score rule: a lab at 0.6 forges a 0.5-threshold module: %+v", o)
	}
	if p := percent(tr, prog, sc); p != 100 {
		t.Fatalf("a forged module counts fully: %d", p)
	}
	sc["m2/lab"] = 0.4
	if o := outline(tr, prog, sc); o[1].Complete {
		t.Fatal("0.4 is under the threshold")
	}
}

func TestQuizAttemptLimitAndCooldown(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	_ = s.MarkRead(ctx, u, "forge", "forge-101", "01-welcome", "how-we-work")
	q := s.State().Trainings["forge-101@abc"].Module("01-welcome").Quiz
	q.MaxAttempts = 2
	bad := correctAnswers()
	bad["q-port"] = raw(`"1"`)
	for i := 0; i < 2; i++ {
		if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, correctAnswers())); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "every attempt") {
		t.Fatalf("third attempt: %v", err)
	}
	v, _ := s.Quiz(ctx, u, "forge", "forge-101", "01-welcome")
	if v.AttemptsLeft == nil || *v.AttemptsLeft != 0 {
		t.Fatalf("view shows no attempts left: %+v", v.AttemptsLeft)
	}
	q.MaxAttempts, q.Cooldown = 0, yamlx.Duration(time.Hour)
	if _, err := s.SubmitQuiz(ctx, u, "forge", "forge-101", "01-welcome", welcomeAnswers(s, u, bad)); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "next attempt") {
		t.Fatalf("inside the cooldown: %v", err)
	}
	if v, _ := s.Quiz(ctx, u, "forge", "forge-101", "01-welcome"); v.NextAttemptAt == nil {
		t.Fatal("view shows when the next attempt opens")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/learn/ -run 'Weighted|AttemptLimit' -v`
Expected: compile errors (`scores`, `completion`, `AttemptsLeft` undefined).

- [ ] **Step 3: Implement**

In `internal/learn/service.go`:

```go
type scores map[string]float64 // "module/item" → best score 0..1

func (s *Service) progress(ctx context.Context, userID int64, team, training string) (progress, scores, error) {
	rows, err := s.DB.Query(ctx, `SELECT module, item, status, score FROM item_progress
		WHERE user_id = $1 AND team = $2 AND training = $3`, userID, team, training)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	p, sc := progress{}, scores{}
	for rows.Next() {
		var m, i, st string
		var v float64
		if err := rows.Scan(&m, &i, &st, &v); err != nil {
			return nil, nil, err
		}
		p[m+"/"+i], sc[m+"/"+i] = st, v
	}
	return p, sc, rows.Err()
}

// Weight is an item's share of its training (spec §7 "weighted by item points"): a reading counts 1, a quiz its
// question points, a lab its task points.
func Weight(m *content.Module, it content.Item) float64 {
	w := 0.0
	switch it.Kind {
	case "quiz":
		if m.Quiz != nil {
			for _, q := range m.Quiz.Questions {
				w += q.Points
			}
		}
	case "lab":
		if m.Lab != nil {
			for _, t := range m.Lab.Tasks {
				w += t.Points
			}
		}
	}
	if w <= 0 {
		return 1
	}
	return w
}

// moduleComplete applies the module's completion rule (spec §7): every item complete, or the weighted score reaching
// the threshold.
func moduleComplete(m *content.Module, prog progress, sc scores) bool {
	if m.Completion == "score" {
		var got, total float64
		for _, it := range m.Items {
			w := Weight(m, it)
			total += w
			got += w * sc[m.ID+"/"+it.ID]
		}
		return total > 0 && got/total >= m.Threshold-1e-9
	}
	for _, it := range m.Items {
		if prog[m.ID+"/"+it.ID] != "complete" {
			return false
		}
	}
	return true
}

// completion is the weighted share of t the user has finished. A forged module counts fully.
func completion(t *content.Training, prog progress, sc scores) (done, total float64) {
	for _, m := range t.Modules {
		forged := moduleComplete(m, prog, sc)
		for _, it := range m.Items {
			w := Weight(m, it)
			total += w
			if forged || prog[m.ID+"/"+it.ID] == "complete" {
				done += w
			}
		}
	}
	return done, total
}

func percent(t *content.Training, prog progress, sc scores) int {
	done, total := completion(t, prog, sc)
	if total == 0 {
		return 0
	}
	return int(done * 100 / total)
}
```

Change `outline(t, prog)` to `outline(t *content.Training, prog progress, sc scores)` and set `mv.Complete = moduleComplete(m, prog, sc)` instead of deriving it from item statuses. Keep the item `Status` loop as it is, minus the `mv.Complete = false` line. Update every caller (`Programs`, `Outline`, `EnsureUnlocked`, `Quiz` and M5's callers): `prog, sc, err := s.progress(...)`, then pass `sc` on.

Add `Standing`:

```go
// Standing is one user's position in one program, for ranks (Task 4) and journey views (Task 6).
type Standing struct {
	Training    *content.Training
	Modules     []ModuleView
	Percent     int
	Done, Total float64
	Status      map[string]string
}

// Standing returns nil, nil when the program's content is unavailable (still syncing or invalid).
func (s *Service) Standing(ctx context.Context, userID int64, team, training string) (*Standing, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	t, _ := st.ProgramTraining(team, training)
	if t == nil {
		return nil, nil
	}
	prog, sc, err := s.progress(ctx, userID, team, t.ID)
	if err != nil {
		return nil, err
	}
	done, total := completion(t, prog, sc)
	return &Standing{Training: t, Modules: outline(t, prog, sc), Percent: percent(t, prog, sc), Done: done, Total: total, Status: prog}, nil
}
```

Attempt limits. Add to `QuizView`:

```go
	AttemptsLeft  *int       `json:"attempts_left,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
```

and the gate:

```go
// attemptGate reports how many instant attempts remain and when the cooldown ends (spec §7; defaults: unlimited, no
// cooldown). ponytail: two attempts in the same instant can both pass; best score counts, so the extra one is harmless.
func (s *Service) attemptGate(ctx context.Context, userID int64, team, training, module string, q *content.Quiz) (left *int, next *time.Time, err error) {
	if q.MaxAttempts == 0 && q.Cooldown == 0 {
		return nil, nil, nil
	}
	var n int
	var last *time.Time
	var now time.Time
	if err := s.DB.QueryRow(ctx, `SELECT count(*), max(created_at), now() FROM quiz_attempts
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, userID, team, training, module).Scan(&n, &last, &now); err != nil {
		return nil, nil, err
	}
	if q.MaxAttempts > 0 {
		l := max(q.MaxAttempts-n, 0)
		left = &l
	}
	if q.Cooldown > 0 && last != nil {
		if at := last.Add(q.Cooldown.D()); at.After(now) {
			next = &at
		}
	}
	return left, next, nil
}
```

In `Quiz`, fill `AttemptsLeft, NextAttemptAt` from `attemptGate` (training id `t.ID`). In `SubmitQuiz`, before scoring the instant answers (after M5's all-human early return, if any):

```go
	left, next, err := s.attemptGate(ctx, u.ID, team, t.ID, module, m.Quiz)
	if err != nil {
		return nil, err
	}
	if left != nil && *left == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "you have used every attempt for this quiz")
	}
	if next != nil {
		return nil, apperr.Wrap(apperr.Conflict, "the next attempt opens at "+next.UTC().Format("15:04 UTC"))
	}
```

In `service_test.go`, `TestProgressionUnlocksModuleTwo` asserts `o.Percent != 40`. Replace 40 with the weighted value, worked out by hand from the fixture: (1 + the summed question points of `01-welcome/quiz.yaml`) × 100 / (the sum of `Weight` over every Forge 101 item), floored. Write the arithmetic in a comment above the assertion.

`web/src/types.ts`: add `attempts_left?: number; next_attempt_at?: string` to `QuizView`. In `web/src/pages/Quiz.tsx`, under the pass mark:

```tsx
      {data.attempts_left !== undefined && <p className="muted" data-testid="attempts-left">Attempts left: {data.attempts_left}</p>}
      {data.next_attempt_at && <p className="warn">Next attempt opens at {new Date(data.next_attempt_at).toLocaleTimeString()}</p>}
```

Disable the "Submit answers" button when `data.attempts_left === 0 || (data.next_attempt_at && new Date(data.next_attempt_at) > new Date())`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/learn/ ./internal/labs/ -race && (cd web && npm test && npx tsc -b)`
Expected: PASS. Labs tests that assert a module `percent` are updated in the same way, with the arithmetic in a comment.

- [ ] **Step 5: Commit**

```bash
git add internal/learn internal/labs web/src
git commit -m "feat(learn): weighted completion, module completion rule, quiz attempt limits and cooldown

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Forge ranks and badges

**Files:**
- Create: `internal/db/migrations/000NN_forge.sql`
- Modify: `internal/notify/notify.go` (`RankUp`)
- Create: `internal/learn/forge.go`, `internal/learn/forge_test.go`
- Modify: `internal/learn/service.go` (`Service.Notify`; `SetItem` and M5's `ForceScore` call `UpdateForge`)
- Modify: `internal/learn/http.go` (`GET /api/me/forge`, `POST /api/me/forge/seen`)
- Modify: `cmd/crucible-api/main.go` (`learnSvc.Notify = notifySvc`)

**Interfaces:**
- Consumes: `config.Settings.Ranks` (Task 1), `Service.Standing` (Task 3).
- Produces:
  ```go
  type Notifier interface{ Notify(ctx context.Context, ev notify.Event) error } // learn.Service.Notify (nil = off)
  type RankStep struct{ Name string; At float64 }           // json name, at
  func Ladder(r config.RankThresholds) []RankStep           // Ore … Masterwork
  func RankFor(pct float64, ladder []RankStep) int          // level 0..5
  type Badge struct{ Training, Title string; EarnedAt time.Time } // json training, title, earned_at
  type ForgeView struct{ Percent float64; Level int; Rank string; Ladder []RankStep; Badges []Badge; RankUp bool }
  // json percent, level, rank, ladder, badges, rank_up
  func (s *Service) UpdateForge(ctx context.Context, userID int64) (*ForgeView, error)
  func (s *Service) SeenRankUp(ctx context.Context, userID int64) error
  notify.RankUp = "rank_up"
  GET /api/me/forge → ForgeView; POST /api/me/forge/seen → 204
  ```

- [ ] **Step 1: Write the migration and the notification kind**

`internal/db/migrations/000NN_forge.sql`:

```sql
-- +goose Up
-- Forge ranks (spec §7, §13): the highest rank a user ever earned. It is never lowered.
CREATE TABLE ranks (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  level     INT NOT NULL,                      -- 0 Ore … 5 Masterwork
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL                   -- false until the trainee has been shown the rank-up
);
-- One badge per completed training, whichever team the completion came through.
CREATE TABLE badges (
  user_id   BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  training  TEXT NOT NULL,
  team      TEXT NOT NULL,
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, training)
);

-- +goose Down
DROP TABLE badges, ranks;
```

In `internal/notify/notify.go`, append `RankUp Kind = "rank_up"` to the kinds, after M5/M6's kinds, and remove the "rank-up kinds arrive with ranks (M7)" comment. Append `{RankUp, "I or one of my mentees reached a new forge rank"}` to `Kinds`.

- [ ] **Step 2: Write the failing tests**

`internal/learn/forge_test.go`:

```go
package learn

import (
	"context"
	"slices"
	"sync"
	"testing"

	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/notify"
)

type fakeNotify struct {
	mu  sync.Mutex
	evs []notify.Event
}

func (f *fakeNotify) Notify(_ context.Context, ev notify.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, ev)
	return nil
}

func (f *fakeNotify) kind(k notify.Kind) []notify.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notify.Event
	for _, e := range f.evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// completeAll marks every item of the given Forge 101 modules complete.
func completeAll(t *testing.T, s *Service, userID int64, modules ...string) {
	t.Helper()
	tr := s.State().Trainings["forge-101@abc"]
	for _, id := range modules {
		for _, it := range tr.Module(id).Items {
			if err := s.SetItem(context.Background(), userID, "forge", "forge-101", id, it.ID, "complete", 1); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// enrolExtra enrols the trainee in a one-module training whose lab is worth `points`.
func enrolExtra(s *Service, points float64) {
	st := s.State()
	extra := &content.Training{ID: "extra", Title: "Extra Heat", Progression: "free", Modules: []*content.Module{{
		ID: "x1", Title: "X", Completion: "all_items", Items: []content.Item{{Kind: "lab", ID: "lab"}},
		Lab: &content.Lab{Tasks: []*content.Task{{ID: "t", Points: points}}}}}}
	st.Trainings["extra@x"] = extra
	st.ProgramSHAs["forge/extra"] = "x"
	st.Platform.Teams["forge"].Programs["extra"] = &config.Program{Training: "extra", Enrolled: []string{"trainee@crucible.local"}}
}

func TestRankIsNeverLost(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || v.Level < 1 {
		t.Fatalf("two of three modules reach at least Ingot: %+v %v", v, err)
	}
	earned := v.Level
	enrolExtra(s, 1000)
	v, err = s.UpdateForge(ctx, u.ID)
	if err != nil || v.Percent >= 20 || v.Level != earned || v.Rank != v.Ladder[earned].Name {
		t.Fatalf("a new enrolment lowers the %% but never the rank: %+v %v", v, err)
	}
}

func TestRankUpNotifiesOnceTraineeAndMentor(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	// Write progress straight to the table so only the concurrent UpdateForge calls below can raise the rank.
	tr := s.State().Trainings["forge-101@abc"]
	for _, id := range []string{"01-welcome", "02-first-lab"} {
		for _, it := range tr.Module(id).Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, id, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.UpdateForge(ctx, u.ID) }()
	}
	wg.Wait()
	ups := notes.kind(notify.RankUp)
	if len(ups) != 1 {
		t.Fatalf("exactly one rank-up notification, got %d", len(ups))
	}
	if ev := ups[0]; !slices.Equal(ev.To, []string{"trainee@crucible.local", "senior@crucible.local"}) || ev.Team != "" {
		t.Fatalf("to the trainee and their mentor, never a team channel: %+v", ev)
	}
	v, _ := s.UpdateForge(ctx, u.ID)
	if !v.RankUp {
		t.Fatal("the rank-up waits to be shown")
	}
	if err := s.SeenRankUp(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UpdateForge(ctx, u.ID); v.RankUp {
		t.Fatal("shown once")
	}
}

func TestForgeWithNoEnrolments(t *testing.T) {
	s, _, leader := fixture(t)
	v, err := s.UpdateForge(context.Background(), leader.ID)
	if err != nil || v.Percent != 0 || v.Level != 0 || v.Rank != "Ore" || len(v.Badges) != 0 || v.RankUp {
		t.Fatalf("no enrolments: %+v %v", v, err)
	}
}

func TestBadgeForACompletedTraining(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	enrolExtra(s, 2)
	if err := s.SetItem(ctx, u.ID, "forge", "extra", "x1", "lab", "complete", 1); err != nil { // SetItem runs UpdateForge
		t.Fatal(err)
	}
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || len(v.Badges) != 1 || v.Badges[0].Training != "extra" || v.Badges[0].Title != "Extra Heat" {
		t.Fatalf("badge: %+v %v", v, err)
	}
}
```

The examples platform (`examples/platform/teams/forge/team.yaml`) maps `trainee@crucible.local` to mentor `senior@crucible.local`, which is what the mentor assertion relies on.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/learn/ -run 'Rank|Forge|Badge' -v`
Expected: compile errors (`UpdateForge` undefined).

- [ ] **Step 4: Implement**

`internal/learn/forge.go`:

```go
package learn

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/config"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// Forge ranks and badges (spec §7). They are personal: nothing here compares one trainee with another.

type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type RankStep struct {
	Name string  `json:"name"`
	At   float64 `json:"at"` // % of enrolled training needed
}

var rankNames = []string{"Ore", "Ingot", "Tempered", "Blade", "Sword", "Masterwork"}

func Ladder(r config.RankThresholds) []RankStep {
	out := make([]RankStep, len(rankNames))
	for i, at := range r.Steps() {
		out[i] = RankStep{Name: rankNames[i], At: at}
	}
	return out
}

// RankFor is the highest level whose threshold pct reaches.
func RankFor(pct float64, ladder []RankStep) int {
	lvl := 0
	for i, s := range ladder {
		if pct >= s.At-1e-9 {
			lvl = i
		}
	}
	return lvl
}

type Badge struct {
	Training string    `json:"training"`
	Title    string    `json:"title"`
	EarnedAt time.Time `json:"earned_at"`
}

type ForgeView struct {
	Percent float64    `json:"percent"` // of all enrolled training, weighted by item points, one decimal
	Level   int        `json:"level"`   // highest ever earned
	Rank    string     `json:"rank"`
	Ladder  []RankStep `json:"ladder"`
	Badges  []Badge    `json:"badges"`
	RankUp  bool       `json:"rank_up"` // a new rank the trainee has not been shown yet
}

// UpdateForge recomputes the user's % and badges and raises the stored rank when a higher one is reached. The rank is
// never lowered. Crossing into a higher rank notifies the trainee and their mentors exactly once: the conditional
// upsert lets only one concurrent caller raise a level.
func (s *Service) UpdateForge(ctx context.Context, userID int64) (*ForgeView, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var email, name string
	if err := s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name); err != nil {
		return nil, err
	}
	var done, total float64
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(email) {
		sd, err := s.Standing(ctx, userID, e.Team.ID, e.Program.Training)
		if err != nil {
			return nil, err
		}
		if sd == nil || sd.Total == 0 {
			continue // ponytail: unavailable content neither helps nor hurts the rank until it syncs again
		}
		done, total = done+sd.Done, total+sd.Total
		if sd.Done >= sd.Total-1e-9 {
			if _, err := s.DB.Exec(ctx, `INSERT INTO badges (user_id, training, team) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				userID, sd.Training.ID, e.Team.ID); err != nil {
				return nil, err
			}
		}
	}
	v := &ForgeView{Ladder: Ladder(st.Platform.Settings.Ranks), Badges: []Badge{}}
	if total > 0 {
		v.Percent = math.Floor(done*1000/total) / 10
	}
	var raised int
	err = s.DB.QueryRow(ctx, `INSERT INTO ranks AS r (user_id, level, seen) VALUES ($1, $2, $2 = 0)
		ON CONFLICT (user_id) DO UPDATE SET level = EXCLUDED.level, earned_at = now(), seen = false WHERE r.level < EXCLUDED.level
		RETURNING level`, userID, RankFor(v.Percent, v.Ladder)).Scan(&raised)
	switch {
	case errors.Is(err, pgx.ErrNoRows): // not above the stored rank
	case err != nil:
		return nil, err
	case raised > 0:
		s.rankUp(ctx, email, name, v.Ladder[raised].Name)
	}
	if err := s.DB.QueryRow(ctx, `SELECT level, NOT seen FROM ranks WHERE user_id = $1`, userID).Scan(&v.Level, &v.RankUp); err != nil {
		return nil, err
	}
	v.Rank = v.Ladder[min(v.Level, len(v.Ladder)-1)].Name
	rows, err := s.DB.Query(ctx, `SELECT training, earned_at FROM badges WHERE user_id = $1 ORDER BY earned_at`, userID)
	if err != nil {
		return nil, err
	}
	badges, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Badge, error) {
		var b Badge
		err := r.Scan(&b.Training, &b.EarnedAt)
		b.Title = b.Training
		for _, t := range st.Trainings { // any loaded version; the tracked head wins below
			if t.ID == b.Training {
				b.Title = t.Title
			}
		}
		if t := st.Training(b.Training, st.Heads[b.Training]); t != nil {
			b.Title = t.Title
		}
		return b, err
	})
	if err != nil {
		return nil, err
	}
	v.Badges = append(v.Badges, badges...)
	return v, nil
}

func (s *Service) rankUp(ctx context.Context, email, name, rank string) {
	if s.Notify == nil {
		return
	}
	st := s.State()
	to := []string{email}
	if st != nil && st.Platform != nil {
		for _, t := range st.Platform.Teams {
			if m := t.Mentors[email]; m != "" && !slices.Contains(to, m) {
				to = append(to, m)
			}
		}
	}
	who := name
	if who == "" {
		who = email
	}
	ev := notify.Event{Kind: notify.RankUp, To: to, Subject: "⚒ " + who + " reached " + rank,
		Text: who + " is now " + rank + " in the Crucible. The forge remembers.", Link: "/"}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		slog.Error("queueing the rank-up notification failed", "err", err)
	}
}

func (s *Service) SeenRankUp(ctx context.Context, userID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE ranks SET seen = true WHERE user_id = $1`, userID)
	return err
}
```

In `internal/learn/service.go`, add `Notify Notifier // rank-up notifications; nil = off` to `Service`. Make `SetItem` (and M5's `ForceScore`) end with:

```go
	if err != nil {
		return err
	}
	if _, ferr := s.UpdateForge(ctx, userID); ferr != nil {
		slog.Warn("forge rank update failed", "user", userID, "err", ferr) // progress is saved; the rank catches up on the next write or read
	}
	return nil
```

In `internal/learn/http.go`'s `Routes`:

```go
	r.Get("/api/me/forge", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.UpdateForge(r.Context(), auth.UserFrom(r.Context()).ID)
		reply(w, v, err)
	})
	r.Post("/api/me/forge/seen", func(w http.ResponseWriter, r *http.Request) {
		reply(w, nil, s.SeenRankUp(r.Context(), auth.UserFrom(r.Context()).ID))
	})
```

In `cmd/crucible-api/main.go`, after `notifySvc` is built: `learnSvc.Notify = notifySvc`.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/learn/ ./internal/labs/ ./internal/notify/ -race`
Expected: PASS. Labs tests still pass: their `learn.Service` has no `Notify`, and `UpdateForge` only reads and upserts.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations internal/notify internal/learn cmd/crucible-api
git commit -m "feat(learn): forge ranks that are never lost, per-training badges, rank-up notifications

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Web: rank card, badges, the hammer strike, heat shimmer, and component tests

**Files:**
- Create: `web/src/components/RankCard.tsx`, `web/src/components/RankCard.test.tsx`
- Modify: `web/src/components/MoltenBar.tsx` (optional `label`/`caption`)
- Modify: `web/src/pages/Hearth.tsx`, `web/src/types.ts`, `web/src/theme/app.css`

**Interfaces:**
- Consumes: `GET /api/me/forge`, `POST /api/me/forge/seen` (Task 4).
- Produces: `type Forge = { percent: number; level: number; rank: string; ladder: { name: string; at: number }[]; badges: { training: string; title: string; earned_at: string }[]; rank_up: boolean }`; `<RankCard forge={Forge} />`; `<RankUp rank={string} onDone={() => void} />`. Component tests render with `react-dom/server`'s `renderToStaticMarkup` (no new dependency).

**Rule for this task:** the rank-up celebration is a **non-blocking banner** (`role="status"`), never a modal. Existing e2e flows land on the Hearth right after crossing a rank, and an overlay would swallow their clicks. It marks itself seen when it mounts, so it plays once.

- [ ] **Step 1: Write the failing test**

`web/src/components/RankCard.test.tsx`:

```tsx
import { describe, expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { RankCard, RankUp } from './RankCard'
import type { Forge } from '../types'

const ladder = [
  { name: 'Ore', at: 0 }, { name: 'Ingot', at: 20 }, { name: 'Tempered', at: 45 },
  { name: 'Blade', at: 75 }, { name: 'Sword', at: 90 }, { name: 'Masterwork', at: 100 },
]
const forge: Forge = { percent: 50, level: 2, rank: 'Tempered', ladder, badges: [{ training: 'forge-102', title: 'Forge 102: Sparks', earned_at: '2026-10-06T10:00:00Z' }], rank_up: false }

describe('RankCard', () => {
  test('shows the rank, the way to the next one, and badges', () => {
    const html = renderToStaticMarkup(<RankCard forge={forge} />)
    expect(html).toContain('data-testid="rank"')
    expect(html).toContain('>Tempered<')
    expect(html).toContain('Blade at 75%')
    expect(html).toContain('Forge 102: Sparks')
    expect(html).not.toMatch(/leaderboard|rank #|position/i) // ranks are personal (spec §7)
  })
  test('masterwork has no next rank', () => {
    const html = renderToStaticMarkup(<RankCard forge={{ ...forge, percent: 100, level: 5, rank: 'Masterwork' }} />)
    expect(html).toContain('The highest rank')
  })
  test('a rank-up is a status banner, not a dialog', () => {
    const html = renderToStaticMarkup(<RankUp rank="Blade" onDone={() => {}} />)
    expect(html).toContain('role="status"')
    expect(html).not.toContain('role="dialog"')
    expect(html).toContain('You reached Blade')
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/RankCard.test.tsx`
Expected: FAIL (`Cannot find module './RankCard'`). If Vitest reports no JSX transform for tests, add `test: { environment: 'node' }` to `web/vite.config.ts`; the React plugin already handles TSX.

- [ ] **Step 3: Implement**

`web/src/types.ts`: add the `Forge` type from **Interfaces**.

`web/src/components/MoltenBar.tsx`: accept `{ percent, label = 'Progress', caption }` and render `<small className="muted">{caption ?? \`${percent}% forged\`}</small>`. Pass `aria-label={label}`.

`web/src/components/RankCard.tsx`:

```tsx
import { useEffect } from 'react'
import { motion } from 'motion/react'
import { api } from '../api'
import { useCalm } from '../me'
import type { Forge } from '../types'
import { MoltenBar } from './MoltenBar'

export function RankCard({ forge }: { forge: Forge }) {
  const next = forge.ladder[forge.level + 1]
  return (
    <section className="rank-card" aria-labelledby="rank-heading">
      <h2 id="rank-heading" className="sr-only">Your forge rank</h2>
      <p className="rank-name"><span aria-hidden="true">⚒</span> <strong data-testid="rank">{forge.rank}</strong></p>
      <MoltenBar percent={Math.floor(forge.percent)} label="Training forged" caption={`${forge.percent}% of your training forged`} />
      <p className="muted">{next ? `Next: ${next.name} at ${next.at}%` : 'The highest rank. The forge salutes you.'}</p>
      {forge.badges.length > 0 && (
        <ul className="badges" data-testid="badges" aria-label="Badges">
          {forge.badges.map((b) => <li key={b.training} className="badge-token" title={`Earned ${new Date(b.earned_at).toLocaleDateString()}`}>🏅 {b.title}</li>)}
        </ul>
      )}
    </section>
  )
}

// RankUp celebrates a new rank once: hammer strike + glow, or a static line under calm motion. It never blocks the page.
export function RankUp({ rank, onDone }: { rank: string; onDone: () => void }) {
  const calm = useCalm()
  useEffect(() => {
    api('/api/me/forge/seen', { method: 'POST' }).catch(() => {})
  }, [])
  return (
    <section className="rank-up" role="status" aria-live="polite">
      {!calm && (
        <motion.span className="hammer" aria-hidden="true" initial={{ rotate: -50, y: -10 }} animate={{ rotate: [-50, 8, 0], y: [-10, 2, 0] }} transition={{ duration: 0.6, ease: 'easeIn' }}>
          🔨
        </motion.span>
      )}
      {!calm && <motion.span className="strike-glow" aria-hidden="true" initial={{ scale: 0.4, opacity: 0.9 }} animate={{ scale: 2.2, opacity: 0 }} transition={{ duration: 0.9, delay: 0.45 }} />}
      <strong>You reached {rank}.</strong> <span className="muted">Steel is forged in fire.</span>
      <button className="ghost" onClick={onDone}>Nice</button>
    </section>
  )
}
```

`web/src/pages/Hearth.tsx`: fetch `useFetch<Forge>('/api/me/forge')`. Render `{forge.data?.rank_up && !dismissed && <RankUp rank={forge.data.rank} onDone={() => setDismissed(true)} />}` above the lede, and `<RankCard forge={forge.data} />` before the program cards. A forge-fetch error shows nothing; the cards still render.

`web/src/theme/app.css`:

```css
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
.rank-card { background: var(--surface); border: 1px solid var(--border); border-radius: 14px; padding: 1rem 1.25rem; margin: 1rem 0; }
.rank-name { font-family: Cinzel, serif; font-size: 1.4rem; margin: 0; }
.badges { display: flex; flex-wrap: wrap; gap: 0.5rem; list-style: none; padding: 0; }
.badge-token { padding: 0.2rem 0.7rem; border-radius: 999px; border: 1px solid var(--accent-2); }
.rank-up { position: relative; display: flex; gap: 0.75rem; align-items: center; padding: 0.75rem 1rem; border: 1px solid var(--accent); border-radius: 12px; background: var(--surface); box-shadow: var(--glow); overflow: hidden; }
.hammer { display: inline-block; font-size: 1.6rem; transform-origin: 80% 80%; }
.strike-glow { position: absolute; left: 1.2rem; top: 50%; width: 2rem; height: 2rem; margin-top: -1rem; border-radius: 50%; background: radial-gradient(var(--accent-2), transparent 70%); pointer-events: none; }
/* Heat shimmer on hovering cards (spec §12); the calm rules below switch every animation off. */
@keyframes shimmer { 0%, 100% { filter: none; } 50% { filter: brightness(1.07) saturate(1.15); } }
.card:hover, .card:focus-visible { animation: shimmer 1.6s ease-in-out infinite; }
```

Check that the existing `@media (prefers-reduced-motion: reduce)` block and the `[data-calm='true']` rule set `animation: none`, so the shimmer stops under calm. If the media block only covers specific classes, extend it to `*`.

- [ ] **Step 4: Run the tests and the build**

Run: `cd web && npm test && npm run build && npm run lint`
Expected: PASS, and the build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): forge rank card, badges, one-time hammer-strike banner, heat shimmer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: Journey and mentor API: heat map and stuck flags

**Files:**
- Create: `internal/journey/journey.go` (types, `Team`, `Mentees`, `row`)
- Create: `internal/journey/flags.go` (`flags`, `BusinessDays`)
- Create: `internal/journey/http.go` (`Routes`)
- Create: `internal/journey/journey_test.go`
- Modify: `internal/httpapi/server.go` (`Deps.Journey`, routes, `is_mentor` on `/api/me`)
- Modify: `cmd/crucible-api/main.go` (`journey.Service{DB: pool, Learn: learnSvc, Now: time.Now}`)

**Interfaces:**
- Consumes: `learn.Service.Standing`, `learn.Ladder` (Tasks 3–4); M5's `submissions` table; `rbac.ViewProgress`.
- Produces:
  ```go
  type Cell struct{ Module, Title, Heat string }                    // json module, title, heat: cold|glowing|forged
  type Flag struct{ Kind, Module, Item, Detail string; At *time.Time } // kind: failed_checks|final_hint|inactive|returned_twice
  type Row struct{ Email, Name, Team, Training, Title string; Percent int; Cells []Cell; Flags []Flag; LastActive *time.Time }
  type Pending struct{ ID int64; Training, Module, Item, Type string; CreatedAt time.Time }
  type Failure struct{ Training, Module, Task, Kind, Detail string; At time.Time } // kind lab_failed|check_failed
  type Mentee struct{ Email, Name, Team, Rank string; Programs []Row; Pending []Pending; Failures []Failure }
  func (s *Service) Team(ctx context.Context, u *auth.User, team string) ([]Row, error)
  func (s *Service) Mentees(ctx context.Context, u *auth.User) ([]Mentee, error)
  func BusinessDays(from, to time.Time) int
  GET /api/teams/{team}/journey → []Row
  GET /api/mentor → []Mentee
  /api/me gains is_mentor: bool
  ```
  JSON field names are the snake_case of the Go names (`last_active`, `created_at`, …). `Mentee.Programs` is `programs`.

- [ ] **Step 1: Write the failing tests**

`internal/journey/journey_test.go`:

```go
package journey

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
)

type fx struct {
	s                        *Service
	ls                       *learn.Service
	st                       *gitsync.State
	trainee, senior, leader  *auth.User
	now                      time.Time
}

func setup(t *testing.T) *fx {
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
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}, Heads: map[string]string{"forge-101": "abc"}}
	ls := &learn.Service{DB: pool, State: func() *gitsync.State { return st }}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	senior, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	leader, _ := store.UpsertUser(ctx, "s3", "leader@crucible.local", "Lee")
	now := time.Now().UTC() // rows written with now() by SetItem must not look days old; TestInactiveAfterFiveBusinessDays pins its own clock
	return &fx{s: &Service{DB: pool, Learn: ls, Now: func() time.Time { return now }}, ls: ls, st: st,
		trainee: trainee, senior: senior, leader: leader, now: now}
}

func insertLab(t *testing.T, db *pgxpool.Pool, id string, userID int64, module, state string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s)
		VALUES ($1, $2, 'forge', 'forge-101', $3, 'abc', 'local', $4, $5, $5, 3600, 1800, 300, 0)`, id, userID, module, state, at); err != nil {
		t.Fatal(err)
	}
}

func flagKinds(r Row) map[string]Flag {
	out := map[string]Flag{}
	for _, f := range r.Flags {
		out[f.Kind] = f
	}
	return out
}

func TestHeatMapAndStuckFlags(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	db := f.s.DB
	for _, it := range []string{"how-we-work", "quiz"} {
		if err := f.ls.SetItem(ctx, f.trainee.ID, "forge", "forge-101", "01-welcome", it, "complete", 1); err != nil {
			t.Fatal(err)
		}
	}
	insertLab(t, db, "aaaaaaaaaaaa", f.trainee.ID, "02-first-lab", "ready", f.now.Add(-time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO check_runs (lab_id, task, exit_code, output, self_reported, at) VALUES ('aaaaaaaaaaaa', 't1', 1, 'no', true, $1)`, f.now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	lab := f.st.Trainings["forge-101@abc"].Module("02-first-lab").Lab
	var hinted *content.Task
	for _, tk := range lab.Tasks {
		if len(tk.Hints) > 0 {
			hinted = tk
		}
	}
	if hinted == nil {
		t.Fatal("Forge 101's first lab needs a task with hints for this test")
	}
	if _, err := db.Exec(ctx, `INSERT INTO hint_reveals (user_id, team, training, module, task, hint_index, cost, lab_id)
		VALUES ($1, 'forge', 'forge-101', '02-first-lab', $2, $3, 0, 'aaaaaaaaaaaa')`, f.trainee.ID, hinted.ID, len(hinted.Hints)-1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype, prompt, max_points, status)
			VALUES ($1, 'forge', 'forge-101', '02-first-lab', 'abc', 'task', 't9', 'review', 'p', 1, 'returned')`, f.trainee.ID); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := f.s.Team(ctx, f.leader, "forge")
	if err != nil || len(rows) != 1 || rows[0].Email != "trainee@crucible.local" {
		t.Fatalf("leader sees the trainee: %+v %v", rows, err)
	}
	r := rows[0]
	heat := map[string]string{}
	for _, c := range r.Cells {
		heat[c.Module] = c.Heat
	}
	if heat["01-welcome"] != "forged" || heat["02-first-lab"] != "glowing" || heat["03-cluster-heat"] != "cold" {
		t.Fatalf("heat map %+v", r.Cells)
	}
	k := flagKinds(r)
	if k["failed_checks"].Item != "t1" || k["final_hint"].Item != hinted.ID || k["returned_twice"].Item != "t9" {
		t.Fatalf("stuck flags %+v", r.Flags)
	}
	if _, ok := k["inactive"]; ok {
		t.Fatal("active an hour ago is not inactive")
	}

	if _, err := db.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status) VALUES ($1, 'forge', 'forge-101', '02-first-lab', 't1', 'passed')`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.s.Team(ctx, f.leader, "forge")
	if _, ok := flagKinds(rows[0])["failed_checks"]; ok {
		t.Fatal("a task passed since is no longer stuck")
	}
}

func TestInactiveAfterFiveBusinessDays(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.s.Now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) } // a Wednesday
	// Last touched Monday 5 Oct; "now" is Wednesday 14 Oct: Tue–Fri + Mon–Wed = 7 business days.
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, updated_at)
		VALUES ($1, 'forge', 'forge-101', '01-welcome', 'how-we-work', 'complete', '2026-10-05T10:00:00Z')`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.s.Team(ctx, f.leader, "forge")
	if fl, ok := flagKinds(rows[0])["inactive"]; !ok || fl.Detail != "no activity for 7 business days" {
		t.Fatalf("inactive flag: %+v", rows[0].Flags)
	}
}

func TestBusinessDays(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 15, 0, 0, 0, time.UTC) } // Oct 2026: the 9th is a Friday
	for _, c := range []struct{ from, to, want int }{{9, 16, 5}, {9, 12, 1}, {10, 11, 0}, {12, 12, 0}, {5, 14, 7}} {
		if got := BusinessDays(d(c.from), d(c.to)); got != c.want {
			t.Errorf("%d→%d: got %d want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestNeverSignedIn(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	team := f.st.Platform.Teams["forge"]
	team.Trainees = append(team.Trainees, "newbie@crucible.local")
	team.Programs["forge-101"].Enrolled = append(team.Programs["forge-101"].Enrolled, "newbie@crucible.local")
	rows, _ := f.s.Team(ctx, f.leader, "forge")
	var nb *Row
	for i := range rows {
		if rows[i].Email == "newbie@crucible.local" {
			nb = &rows[i]
		}
	}
	if nb == nil || flagKinds(*nb)["inactive"].Detail != "has not signed in yet" || nb.Cells[0].Heat != "cold" {
		t.Fatalf("never signed in: %+v", nb)
	}
}

func TestJourneyVisibilityAndMentees(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if rows, err := f.s.Team(ctx, f.trainee, "forge"); err != nil || len(rows) != 0 {
		t.Fatalf("a trainee sees no one else's journey (and not their own here): %+v %v", rows, err)
	}
	if rows, _ := f.s.Team(ctx, f.senior, "forge"); len(rows) != 1 {
		t.Fatal("a senior (and mentor) sees the trainee")
	}
	ms, err := f.s.Mentees(ctx, f.senior)
	if err != nil || len(ms) != 1 || ms[0].Email != "trainee@crucible.local" || ms[0].Rank != "Ore" || len(ms[0].Programs) != 1 {
		t.Fatalf("mentor dashboard: %+v %v", ms, err)
	}
	if ms, _ := f.s.Mentees(ctx, f.leader); len(ms) != 0 {
		t.Fatal("the leader mentors no one in the fixture")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/journey/ -v`
Expected: compile errors (package `journey` has no `Service`).

- [ ] **Step 3: Implement**

`internal/journey/journey.go`:

```go
// Package journey answers "how are my people doing?" for leaders, managers, scorers and mentors (spec §11): a module
// heat map per trainee and program, with stuck flags, and a mentor dashboard. It only reads. It lists people one by
// one, sorted by email, and never ranks or compares them (spec §1).
package journey

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
	"crucible/internal/rbac"
)

type Service struct {
	DB    *pgxpool.Pool
	Learn *learn.Service
	Now   func() time.Time
}

type Cell struct {
	Module string `json:"module"`
	Title  string `json:"title"`
	Heat   string `json:"heat"` // cold | glowing | forged
}

type Flag struct {
	Kind   string     `json:"kind"` // failed_checks | final_hint | inactive | returned_twice
	Module string     `json:"module,omitempty"`
	Item   string     `json:"item,omitempty"`
	Detail string     `json:"detail"`
	At     *time.Time `json:"at,omitempty"`
}

type Row struct {
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Team       string     `json:"team"`
	Training   string     `json:"training"`
	Title      string     `json:"title"`
	Percent    int        `json:"percent"`
	Cells      []Cell     `json:"cells"`
	Flags      []Flag     `json:"flags"`
	LastActive *time.Time `json:"last_active,omitempty"`
}

type Pending struct {
	ID        int64     `json:"id"`
	Training  string    `json:"training"`
	Module    string    `json:"module"`
	Item      string    `json:"item"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
}

type Failure struct {
	Training string    `json:"training"`
	Module   string    `json:"module"`
	Task     string    `json:"task,omitempty"`
	Kind     string    `json:"kind"` // lab_failed | check_failed
	Detail   string    `json:"detail"`
	At       time.Time `json:"at"`
}

type Mentee struct {
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Team     string    `json:"team"`
	Rank     string    `json:"rank"`
	Programs []Row     `json:"programs"`
	Pending  []Pending `json:"pending"`
	Failures []Failure `json:"failures"`
}

func (s *Service) state() (*gitsync.State, error) {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Team lists every enrolled person the actor may follow (spec §5.3 "View trainee progress"), program by program,
// excluding the actor themselves.
func (s *Service) Team(ctx context.Context, u *auth.User, team string) ([]Row, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	t := st.Platform.Teams[team]
	if t == nil {
		return nil, apperr.Wrap(apperr.NotFound, "team not found")
	}
	c, me, out := rbac.Checker{P: st.Platform}, strings.ToLower(u.Email), []Row{}
	for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
		for _, email := range slices.Sorted(slices.Values(t.Programs[tr].Enrolled)) {
			if email == me || !c.Can(me, rbac.ViewProgress, team, tr, email) {
				continue
			}
			r, err := s.row(ctx, st, team, tr, email)
			if err != nil {
				return nil, err
			}
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *Service) user(ctx context.Context, email string) (id int64, name string, created time.Time, ok bool, err error) {
	err = s.DB.QueryRow(ctx, `SELECT id, name, created_at FROM users WHERE email = $1 ORDER BY id DESC LIMIT 1`, email).Scan(&id, &name, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", time.Time{}, false, nil
	}
	return id, name, created, err == nil, err
}

func (s *Service) row(ctx context.Context, st *gitsync.State, team, training, email string) (*Row, error) {
	r := &Row{Email: email, Team: team, Training: training, Title: training, Cells: []Cell{}, Flags: []Flag{}}
	t, _ := st.ProgramTraining(team, training)
	if t == nil {
		return r, nil // content unavailable: an honest empty row
	}
	r.Title = t.Title
	uid, name, created, ok, err := s.user(ctx, email)
	if err != nil {
		return nil, err
	}
	if !ok {
		for _, m := range t.Modules {
			r.Cells = append(r.Cells, Cell{Module: m.ID, Title: m.Title, Heat: "cold"})
		}
		r.Flags = append(r.Flags, Flag{Kind: "inactive", Detail: "has not signed in yet"})
		return r, nil
	}
	r.Name = name
	sd, err := s.Learn.Standing(ctx, uid, team, training)
	if err != nil || sd == nil {
		return r, err
	}
	active := map[string]bool{}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT module FROM lab_instances WHERE user_id = $1 AND team = $2 AND training = $3
		AND state IN ('pending_approval', 'provisioning', 'ready', 'destroying')`, uid, team, training)
	if err != nil {
		return nil, err
	}
	mods, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, m := range mods {
		active[m] = true
	}
	for _, mv := range sd.Modules {
		heat := "cold"
		if mv.Complete {
			heat = "forged"
		} else if active[mv.ID] || slices.ContainsFunc(mv.Items, func(it learn.ItemView) bool { return it.Status != "new" }) {
			heat = "glowing"
		}
		r.Cells = append(r.Cells, Cell{Module: mv.ID, Title: mv.Title, Heat: heat})
	}
	r.Percent = sd.Percent
	r.Flags, r.LastActive, err = s.flags(ctx, uid, team, t, created, sd.Percent)
	return r, err
}

// Mentees is the mentor dashboard (spec §11): each mentee's programs, rank, submissions waiting for a scorer, and
// recent lab failures.
func (s *Service) Mentees(ctx context.Context, u *auth.User) ([]Mentee, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	me, out := strings.ToLower(u.Email), []Mentee{}
	ladder := learn.Ladder(st.Platform.Settings.Ranks)
	for _, teamID := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		t := st.Platform.Teams[teamID]
		for _, trainee := range slices.Sorted(maps.Keys(t.Mentors)) {
			if t.Mentors[trainee] != me {
				continue
			}
			m := Mentee{Email: trainee, Team: teamID, Rank: ladder[0].Name, Programs: []Row{}, Pending: []Pending{}, Failures: []Failure{}}
			for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
				if !slices.Contains(t.Programs[tr].Enrolled, trainee) {
					continue
				}
				r, err := s.row(ctx, st, teamID, tr, trainee)
				if err != nil {
					return nil, err
				}
				m.Programs, m.Name = append(m.Programs, *r), r.Name
			}
			uid, _, _, ok, err := s.user(ctx, trainee)
			if err != nil {
				return nil, err
			}
			if ok {
				if err := s.menteeDetail(ctx, uid, teamID, ladder, &m); err != nil {
					return nil, err
				}
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Service) menteeDetail(ctx context.Context, uid int64, team string, ladder []learn.RankStep, m *Mentee) error {
	var lvl int
	if err := s.DB.QueryRow(ctx, `SELECT level FROM ranks WHERE user_id = $1`, uid).Scan(&lvl); err == nil {
		m.Rank = ladder[min(lvl, len(ladder)-1)].Name
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, training, module, item, qtype, created_at FROM submissions
		WHERE user_id = $1 AND team = $2 AND status = 'pending' ORDER BY created_at`, uid, team)
	if err != nil {
		return err
	}
	if m.Pending, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Pending]); err != nil {
		return err
	}
	rows, err = s.DB.Query(ctx, `
		SELECT training, module, '' AS task, 'lab_failed' AS kind, left(error, 200) AS detail, created_at AS at
		  FROM lab_instances WHERE user_id = $1 AND team = $2 AND state = 'failed' AND created_at > $3::timestamptz - interval '14 days'
		UNION ALL
		SELECT li.training, li.module, cr.task, 'check_failed', count(*)::text || ' failed checks', max(cr.at)
		  FROM check_runs cr JOIN lab_instances li ON li.id = cr.lab_id
		 WHERE li.user_id = $1 AND li.team = $2 AND cr.exit_code <> 0 AND cr.at > $3::timestamptz - interval '7 days'
		 GROUP BY li.training, li.module, cr.task
		ORDER BY 6 DESC LIMIT 20`, uid, team, s.Now())
	if err != nil {
		return err
	}
	m.Failures, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Failure])
	if m.Pending == nil {
		m.Pending = []Pending{}
	}
	if m.Failures == nil {
		m.Failures = []Failure{}
	}
	return err
}
```

`internal/journey/flags.go`:

```go
package journey

import (
	"context"
	"fmt"
	"time"

	"crucible/internal/content"
)

// flags computes spec §11's stuck signals for one trainee in one program, and when they were last active.
func (s *Service) flags(ctx context.Context, uid int64, team string, t *content.Training, signedUp time.Time, percent int) ([]Flag, *time.Time, error) {
	out := []Flag{}
	// ≥ 3 failed checks on a task that is still neither passed nor skipped.
	rows, err := s.DB.Query(ctx, `SELECT li.module, cr.task, count(*), max(cr.at) FROM check_runs cr
		JOIN lab_instances li ON li.id = cr.lab_id
		WHERE li.user_id = $1 AND li.team = $2 AND li.training = $3 AND cr.exit_code <> 0
		  AND NOT EXISTS (SELECT 1 FROM lab_task_progress p WHERE p.user_id = li.user_id AND p.team = li.team
		                  AND p.training = li.training AND p.module = li.module AND p.task = cr.task)
		GROUP BY li.module, cr.task HAVING count(*) >= 3 ORDER BY li.module, cr.task`, uid, team, t.ID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var m, task string
		var n int
		var at time.Time
		if err := rows.Scan(&m, &task, &n, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, Flag{Kind: "failed_checks", Module: m, Item: task, Detail: fmt.Sprintf("%d failed checks on %s", n, task), At: &at})
	}
	rows.Close()
	// The final (solution) hint revealed (spec §8.4).
	rows, err = s.DB.Query(ctx, `SELECT module, task, max(hint_index), max(at) FROM hint_reveals
		WHERE user_id = $1 AND team = $2 AND training = $3 GROUP BY module, task ORDER BY module, task`, uid, team, t.ID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var m, task string
		var idx int
		var at time.Time
		if err := rows.Scan(&m, &task, &idx, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if mod := t.Module(m); mod != nil && mod.Lab != nil {
			if tk := mod.Lab.Task(task); tk != nil && len(tk.Hints) > 0 && idx == len(tk.Hints)-1 {
				out = append(out, Flag{Kind: "final_hint", Module: m, Item: task, Detail: "revealed the final hint for " + task, At: &at})
			}
		}
	}
	rows.Close()
	// A submission returned for rework twice.
	rows, err = s.DB.Query(ctx, `SELECT module, item, count(*), max(created_at) FROM submissions
		WHERE user_id = $1 AND team = $2 AND training = $3 AND status = 'returned'
		GROUP BY module, item HAVING count(*) >= 2 ORDER BY module, item`, uid, team, t.ID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var m, item string
		var n int
		var at time.Time
		if err := rows.Scan(&m, &item, &n, &at); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, Flag{Kind: "returned_twice", Module: m, Item: item, Detail: fmt.Sprintf("returned for rework %d times", n), At: &at})
	}
	rows.Close()
	// No activity for 5 business days on an unfinished program.
	var last *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT greatest(
		(SELECT max(updated_at) FROM item_progress WHERE user_id = $1 AND team = $2 AND training = $3),
		(SELECT max(created_at) FROM quiz_attempts WHERE user_id = $1 AND team = $2 AND training = $3),
		(SELECT max(last_activity_at) FROM lab_instances WHERE user_id = $1 AND team = $2 AND training = $3),
		(SELECT max(at) FROM hint_reveals WHERE user_id = $1 AND team = $2 AND training = $3),
		(SELECT max(created_at) FROM submissions WHERE user_id = $1 AND team = $2 AND training = $3))`, uid, team, t.ID).Scan(&last); err != nil {
		return nil, nil, err
	}
	since := signedUp
	if last != nil {
		since = *last
	}
	if n := BusinessDays(since, s.Now()); percent < 100 && n >= 5 {
		out = append(out, Flag{Kind: "inactive", Detail: fmt.Sprintf("no activity for %d business days", n), At: last})
	}
	return out, last, nil
}

// BusinessDays counts Mon–Fri dates after from's date, up to and including to's date, in UTC.
// ponytail: no holidays and no team time zones; add a holiday list to platform.yaml if teams ask for it.
func BusinessDays(from, to time.Time) int {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}
```

`internal/journey/http.go`:

```go
package journey

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	r.Get("/api/teams/{team}/journey", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.Team(r.Context(), auth.UserFrom(r.Context()), chi.URLParam(r, "team"))
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, rows)
	})
	r.Get("/api/mentor", func(w http.ResponseWriter, r *http.Request) {
		ms, err := s.Mentees(r.Context(), auth.UserFrom(r.Context()))
		if err != nil {
			httpx.Error(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, ms)
	})
}
```

`internal/httpapi/server.go`:
- add `Journey *journey.Service` to `Deps`, and `if d.Journey != nil { d.Journey.Routes(r) }` inside the authenticated group;
- in `/api/me`, compute `mentor := false` and, inside the team loop, `for _, m := range t.Mentors { mentor = mentor || m == strings.ToLower(u.Email) }`;
- add `"is_mentor": mentor` to the JSON.

`cmd/crucible-api/main.go`: `journeySvc := &journey.Service{DB: pool, Learn: learnSvc, Now: time.Now}` and pass `Journey: journeySvc` in `Deps`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/journey/ ./internal/httpapi/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/journey internal/httpapi cmd/crucible-api
git commit -m "feat(journey): heat map with stuck flags and a mentor dashboard API

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Web: Journey heat map and Mentor dashboard

**Files:**
- Create: `web/src/components/HeatMap.tsx`, `web/src/components/HeatMap.test.tsx`
- Create: `web/src/pages/Journey.tsx`, `web/src/pages/Mentor.tsx`
- Modify: `web/src/App.tsx` (routes `/teams/:team/journey`, `/mentor`), `web/src/pages/Team.tsx` (a "Journey" link), `web/src/types.ts`, `web/src/theme/app.css`
- Nav (`Mentor` link for `me.is_mentor`) is finished in Task 16. Add the link now after Approvals; Task 16 only reorders it.

**Interfaces:**
- Consumes: `GET /api/teams/{team}/journey`, `GET /api/mentor`, `/api/me` `is_mentor` (Task 6).
- Produces: types `JourneyRow`, `JourneyCell`, `JourneyFlag`, `Mentee` matching Task 6's JSON. `<HeatMap rows={JourneyRow[]} />` renders one row per trainee × program. Each cell has `aria-label="<module title>: <heat>"`, a visible glyph (`·` cold, `◐` glowing, `●` forged), and the class `heat-<heat>`. Flags render as `<li role="note">⚠ <detail></li>`.

- [ ] **Step 1: Write the failing test**

`web/src/components/HeatMap.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { HeatMap } from './HeatMap'
import type { JourneyRow } from '../types'

const row: JourneyRow = {
  email: 'trainee@crucible.local', name: 'Tara', team: 'forge', training: 'forge-101', title: 'Forge 101: First Heat', percent: 40,
  cells: [
    { module: '01-welcome', title: 'Welcome', heat: 'forged' },
    { module: '02-first-lab', title: 'First Lab', heat: 'glowing' },
    { module: '03-cluster-heat', title: 'Cluster Heat', heat: 'cold' },
  ],
  flags: [{ kind: 'failed_checks', module: '02-first-lab', item: 't1', detail: '3 failed checks on t1' }],
}

test('cells carry their heat in text and aria, flags are notes', () => {
  const html = renderToStaticMarkup(<HeatMap rows={[row]} />)
  expect(html).toContain('aria-label="Welcome: forged"')
  expect(html).toContain('aria-label="First Lab: glowing"')
  expect(html).toContain('aria-label="Cluster Heat: cold"')
  expect(html).toContain('role="note"')
  expect(html).toContain('3 failed checks on t1')
  expect(html).not.toMatch(/rank #|top /i) // no comparisons between trainees
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/HeatMap.test.tsx`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement**

`web/src/types.ts`:

```ts
export type Heat = 'cold' | 'glowing' | 'forged'
export type JourneyCell = { module: string; title: string; heat: Heat }
export type JourneyFlag = { kind: 'failed_checks' | 'final_hint' | 'inactive' | 'returned_twice'; module?: string; item?: string; detail: string; at?: string }
export type JourneyRow = { email: string; name: string; team: string; training: string; title: string; percent: number; cells: JourneyCell[]; flags: JourneyFlag[]; last_active?: string }
export type Mentee = { email: string; name: string; team: string; rank: string; programs: JourneyRow[]
  pending: { id: number; training: string; module: string; item: string; type: string; created_at: string }[]
  failures: { training: string; module: string; task?: string; kind: string; detail: string; at: string }[] }
```

Add `is_mentor: boolean` to `Me`.

`web/src/components/HeatMap.tsx`:

```tsx
import type { Heat, JourneyRow } from '../types'

const glyph: Record<Heat, string> = { cold: '·', glowing: '◐', forged: '●' }

export function HeatMap({ rows }: { rows: JourneyRow[] }) {
  if (rows.length === 0) return <p className="muted">Nobody to show here yet.</p>
  return (
    <div className="journey">
      {rows.map((r) => (
        <section key={r.email + r.training} className="journey-row" aria-label={`${r.name || r.email} in ${r.title}`} data-testid={`journey-${r.email}-${r.training}`}>
          <h3>{r.name || r.email} <span className="muted">· {r.title} · {r.percent}% forged</span></h3>
          <ol className="heat-cells">
            {r.cells.map((c) => (
              <li key={c.module} className={`heat heat-${c.heat}`} aria-label={`${c.title}: ${c.heat}`} title={`${c.title}: ${c.heat}`}>
                <span aria-hidden="true">{glyph[c.heat]}</span>
              </li>
            ))}
          </ol>
          {r.flags.length > 0 && (
            <ul className="flags">
              {r.flags.map((f, i) => <li key={i} role="note" className="warn">⚠ {f.detail}{f.module ? ` (${f.module})` : ''}</li>)}
            </ul>
          )}
        </section>
      ))}
    </div>
  )
}
```

`web/src/pages/Journey.tsx`: `useParams().team`; `useFetch<JourneyRow[]>(\`/api/teams/${team}/journey\`)`; heading `Journey`; a legend ("· cold — not started, ◐ glowing — in progress, ● forged — complete"); then `<HeatMap rows={data} />`. Use `ErrorBox`/`Loader` like the other pages.

`web/src/pages/Mentor.tsx`: `useFetch<Mentee[]>('/api/mentor')`; heading `Mentor`. For each mentee, a `<section data-testid={\`mentee-${m.email}\`}>` containing: name, `Rank: {m.rank}`, `<HeatMap rows={m.programs} />`, a "Waiting for a scorer" list from `m.pending` (type and item only, no points), and a "Recent lab trouble" list from `m.failures` (`detail`, module, and date). Empty lists say "Nothing waiting." and "No recent failures."

`web/src/pages/Team.tsx`: next to the team heading, `<Link to={\`/teams/${team.id}/journey\`}>Journey</Link>`, shown when the user has a team role (the API returns an empty list otherwise anyway).

`web/src/App.tsx`: add the two routes. `Nav.tsx`: `{me.is_mentor && <NavLink to="/mentor">Mentor</NavLink>}`.

`web/src/theme/app.css`:

```css
.journey-row { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 0.75rem 1rem; margin: 0.75rem 0; }
.heat-cells { display: flex; gap: 0.35rem; list-style: none; padding: 0; margin: 0.5rem 0; }
.heat { width: 2rem; height: 2rem; display: grid; place-items: center; border-radius: 6px; border: 1px solid var(--border); }
.heat-cold { background: var(--surface-2); color: var(--muted); }
.heat-glowing { background: linear-gradient(135deg, var(--surface-2), var(--accent)); color: var(--text); }
.heat-forged { background: linear-gradient(135deg, var(--accent), var(--accent-2)); color: #1a0f05; box-shadow: var(--glow); }
.flags { list-style: none; padding: 0; margin: 0; }
```

- [ ] **Step 4: Run the tests and the build**

Run: `cd web && npm test && npm run build && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): journey heat map with stuck flags and the mentor dashboard

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: "Extension pending": extensions that need a new approval (spec §8.6)

**Files:**
- Create: `internal/db/migrations/000NN_extension.sql`
- Modify: `internal/labs/model.go` (`Instance.ExtUntil/ExtEstimateUSD/ExtTier/ExtRequestedAt`)
- Modify: `internal/labs/service.go` (`instCols`, `scanInst`, `View.ExtensionPending`, `view`, `Extend`, new `requestExtension`)
- Modify: `internal/labs/approvals.go` (`Approval.Kind/ExtendUntil`, `Approvals` lists extensions, new `DecideExtension`)
- Modify: `internal/labs/http.go` (`POST /api/approvals/{id}/extension`)
- Modify: `internal/labs/schedule_test.go` (replace `TestExtensionThatRaisesTheTierIsRefused`)
- Create: `internal/labs/extension_test.go`
- Modify: `web/src/components/Timer.tsx`, `web/src/pages/Approvals.tsx`, `web/src/types.ts`
- Create: `web/src/components/Timer.test.tsx`

**Interfaces:**
- Produces:
  ```go
  View.ExtensionPending bool          // json extension_pending
  Approval.Kind string                // json kind: "request" | "extension"
  Approval.ExtendUntil *time.Time     // json extend_until,omitempty
  func (s *Service) DecideExtension(ctx context.Context, u *auth.User, labID string, approve bool, note string) error
  POST /api/approvals/{id}/extension {approve: bool (required), note} → 204
  ```

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- A lab extension that lifts the estimate into a higher tier waits for approval (spec §8.6 "Extension pending").
ALTER TABLE lab_instances
  ADD COLUMN ext_until        TIMESTAMPTZ,                          -- requested end; NULL = nothing pending
  ADD COLUMN ext_estimate_usd DOUBLE PRECISION NOT NULL DEFAULT 0,  -- the lab's estimate if approved
  ADD COLUMN ext_tier         TEXT NOT NULL DEFAULT '',
  ADD COLUMN ext_requested_at TIMESTAMPTZ;
CREATE INDEX lab_instances_ext_pending ON lab_instances (ext_requested_at) WHERE ext_until IS NOT NULL;

-- +goose Down
DROP INDEX lab_instances_ext_pending;
ALTER TABLE lab_instances DROP COLUMN ext_until, DROP COLUMN ext_estimate_usd, DROP COLUMN ext_tier, DROP COLUMN ext_requested_at;
```

- [ ] **Step 2: Write the failing tests**

In `internal/labs/schedule_test.go`, delete `TestExtensionThatRaisesTheTierIsRefused`. Create `internal/labs/extension_test.go`:

```go
package labs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/notify"
	"crucible/internal/yamlx"
)

// readyPaidLab: $4.50/h for the 1h first-heat lab = tier 1; +30m = $6.75 = tier 2 (the leader).
func readyPaidLab(t *testing.T, f *fx) *View {
	t.Helper()
	f.rates["first-heat"] = 4.5
	v := f.request(t, f.u)
	if _, err := f.s.Decide(context.Background(), f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	return f.waitState(t, f.u, v.ID, Ready)
}

func TestExtensionGoesToApproval(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	ready := readyPaidLab(t, f)
	got, err := f.s.Extend(ctx, f.u, ready.ID)
	if err != nil || !got.ExtensionPending || got.CanExtend || !got.EndsAt.Equal(*ready.EndsAt) {
		t.Fatalf("a tier-raising extension waits for approval: %+v %v", got, err)
	}
	if ev := f.notes.last(notify.LabPending); ev == nil || !strings.Contains(ev.Subject, "extension") || strings.Join(ev.To, ",") != "leader@crucible.local" {
		t.Fatalf("the tier-2 decider hears about it: %+v", ev)
	}
	list, err := f.s.Approvals(ctx, f.leader)
	if err != nil || len(list) != 1 || list[0].Kind != "extension" || list[0].EstimateUSD != 6.75 ||
		list[0].ExtendUntil == nil || !list[0].ExtendUntil.Equal(ready.EndsAt.Add(30*time.Minute)) {
		t.Fatalf("approvals list: %+v %v", list, err)
	}
	if err := f.s.DecideExtension(ctx, f.u, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("nobody approves their own extension: %v", err)
	}
	if err := f.s.DecideExtension(ctx, f.other, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("a senior who is not an approver: %v", err)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, "go on"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.Get(ctx, f.u, ready.ID)
	if after.ExtensionPending || !after.EndsAt.Equal(ready.EndsAt.Add(30*time.Minute)) || after.EstimateUSD != 6.75 {
		t.Fatalf("approved: %+v", after)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("decided once: %v", err)
	}
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1 ORDER BY id DESC LIMIT 1`, ready.ID).Scan(&action)
	if action != "lab.extension.approve" {
		t.Fatalf("audit %q", action)
	}
	if ev := f.notes.last(notify.LabApproved); ev == nil || ev.To[0] != "trainee@crucible.local" || !strings.Contains(ev.Text, "extension") {
		t.Fatalf("the trainee hears the answer: %+v", ev)
	}
}

func TestExtensionDecisionRules(t *testing.T) {
	ctx := context.Background()
	t.Run("reject keeps the end and uses up the extension", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, false, "not today"); err != nil {
			t.Fatal(err)
		}
		v, _ := f.s.Get(ctx, f.u, ready.ID)
		if v.ExtensionPending || v.CanExtend || !v.EndsAt.Equal(*ready.EndsAt) {
			t.Fatalf("rejected: %+v", v)
		}
	})
	t.Run("two approvers at once: one decision", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, u := range []*auth.User{f.leader, f.admin} {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = f.s.DecideExtension(ctx, u, ready.ID, true, "") }()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) || !(errors.Is(errs[0], apperr.Conflict) || errors.Is(errs[1], apperr.Conflict)) {
			t.Fatalf("exactly one wins: %v", errs)
		}
	})
	t.Run("an ended lab drops out of the queue", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		if _, err := f.s.End(ctx, f.u, ready.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
			t.Fatalf("queue: %+v", list)
		}
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Conflict) {
			t.Fatalf("nothing to decide: %v", err)
		}
	})
	t.Run("over the hard cap only an admin approves, audited", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		f.spent(t, "000000000099", "forge-101", 243) // team cap 250: 243 + 4.50 fits, + the extra 2.25 does not
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
			t.Fatalf("leader over cap: %v", err)
		}
		if err := f.s.DecideExtension(ctx, f.admin, ready.ID, true, "ok"); err != nil {
			t.Fatal(err)
		}
		var over bool
		_ = f.s.DB.QueryRow(ctx, `SELECT (detail->>'over_cap')::bool FROM audit_log WHERE target = $1 ORDER BY id DESC LIMIT 1`, ready.ID).Scan(&over)
		if !over {
			t.Fatal("the override is audited")
		}
	})
}

func TestExtensionApprovalNeverPassesTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.plat.Teams["forge"].Programs["forge-101"].LabDefaults.MaxExtension = yamlx.Duration(45 * time.Minute)
	f.clk.Set(time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC)) // Wed 17:30 local; TTL 1h → 18:30; window closes 19:00 (16:00 UTC)
	ready := readyPaidLab(t, f)
	v, err := f.s.Extend(ctx, f.u, ready.ID)
	if err != nil || !v.ExtensionPending {
		t.Fatalf("request: %+v %v", v, err)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.Get(ctx, f.u, ready.ID)
	if got.LimitReason != "schedule" || !got.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("clamped at window close: %+v", got)
	}
}
```

Before relying on the over-cap numbers, check `f.spent`'s signature and the fixture's budget (`budget.yaml`: monthly 200, cap 250). If M6 changed how spend is counted, adjust 243 so that the lab alone fits under the cap and the extension does not. If the 45-minute extension does not raise the tier at $4.50/h in the window test, raise `f.rates` in that test so it does.

`web/src/components/Timer.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Timer } from './Timer'
import type { LabView } from '../types'

test('the timer says when an extension is waiting for approval', () => {
  const lab = { id: 'a', state: 'ready', ends_at: new Date(Date.now() + 3_600_000).toISOString(), server_now: new Date().toISOString(),
    can_extend: false, extension_pending: true, limit_reason: 'ttl' } as unknown as LabView
  const html = renderToStaticMarkup(<Timer lab={lab} offset={0} onExtend={() => {}} />)
  expect(html).toContain('Extension pending')
  expect(html).not.toContain('>Extend<')
})
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/labs/ -run 'Extension' -v; (cd web && npx vitest run src/components/Timer.test.tsx)`
Expected: compile errors (`ExtensionPending`, `DecideExtension` undefined), and the web test fails.

- [ ] **Step 4: Implement**

`internal/labs/model.go`, in `Instance`:

```go
	ExtUntil, ExtRequestedAt *time.Time // a pending extension (spec §8.6); nil = none
	ExtEstimateUSD           float64
	ExtTier                  string
```

`internal/labs/service.go`:
- append `, ext_until, ext_estimate_usd, ext_tier, ext_requested_at` to `instCols`, and `&in.ExtUntil, &in.ExtEstimateUSD, &in.ExtTier, &in.ExtRequestedAt` to `scanInst`'s `Scan` in the same order;
- add `ExtensionPending bool \`json:"extension_pending"\`` to `View` and set `v.ExtensionPending = inst.ExtUntil != nil` in `view`;
- in `Extend`, replace the tier-raising `return nil, apperr.Wrap(apperr.Conflict, "this extension would need a new approval; …")` branch and its ponytail comment (M6 reworded it) with `return s.requestExtension(ctx, u, st.Platform, inst, end, more)`. Keep M6's budget clamp of `end` *above* this point, so `end` is already clamped.

Add:

```go
// requestExtension sends an extension that lifts the estimate into a higher tier back through approval (spec §8.6).
// It uses up the lab's one extension; the timer shows "Extension pending" until someone decides.
// ponytail: extension requests do not escalate; the lab, and the request with it, ends within hours.
func (s *Service) requestExtension(ctx context.Context, u *auth.User, p *config.Platform, inst *Instance, until time.Time, estimate float64) (*View, error) {
	c := rbac.Checker{P: p}
	tier := c.Route(rbac.Tier(inst.Runtime, estimate, *p.Settings.CostTiers), inst.Team, inst.Training, u.Email)
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET ext_until = $2, ext_estimate_usd = $3, ext_tier = $4, ext_requested_at = $5,
		extended = true, last_activity_at = $5 WHERE id = $1 AND NOT extended AND state = 'ready'`, inst.ID, until, estimate, tier, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	s.event(ctx, inst.ID, "extension_requested", fmt.Sprintf("until %s, estimate %.2f USD, tier %s", until.UTC().Format(time.RFC3339), estimate, tier))
	s.notify(ctx, notify.Event{Kind: notify.LabPending, To: c.TierApprovers(tier, inst.Team, inst.Training, u.Email), Team: inst.Team,
		Subject: "A lab extension is waiting for approval",
		Text:    fmt.Sprintf("%s asks to extend a %s lab until %s (estimate %.2f USD).", strings.ToLower(u.Email), inst.Training, until.UTC().Format("15:04 UTC"), estimate),
		Link:    "/approvals"})
	return s.Get(ctx, u, inst.ID)
}
```

(Use the same `Team` setting as the existing `notifyRequest` for pending requests. If that one leaves `Team` empty, leave it empty here too.)

`internal/labs/approvals.go`: add to `Approval`:

```go
	Kind        string     `json:"kind"` // request | extension
	ExtendUntil *time.Time `json:"extend_until,omitempty"`
```

Set `Kind: "request"` in `approval()`. At the end of `Approvals`, before `return out, nil`:

```go
	rows, err = s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE state = 'ready' AND ext_until IS NOT NULL ORDER BY ext_requested_at`)
	if err != nil {
		return nil, err
	}
	exts, err := collectInst(rows)
	if err != nil {
		return nil, err
	}
	for _, inst := range exts {
		email, name, err := s.requester(ctx, inst.UserID)
		if err != nil {
			return nil, err
		}
		over, err := s.overCap(ctx, st.Platform, inst.Team, inst.Training, inst.ExtEstimateUSD-inst.EstimateUSD)
		if err != nil {
			return nil, err
		}
		if !c.MayApprove(u.Email, email, inst.Team, inst.Training, inst.ExtEstimateUSD, over) {
			continue
		}
		a, err := s.approval(ctx, st, inst, email, name)
		if err != nil {
			return nil, err
		}
		a.Kind, a.ExtendUntil, a.EstimateUSD, a.Tier, a.OverCap, a.EscalateAt = "extension", inst.ExtUntil, inst.ExtEstimateUSD, inst.ExtTier, over, nil
		a.RequestedAt = *inst.ExtRequestedAt
		out = append(out, *a)
	}
```

(`rows` and `pending` are the names the function already uses; rename if they differ.) Add:

```go
// DecideExtension approves or rejects a pending extension. Who may decide follows the new estimate, as for a request;
// past the hard cap only an admin may, and that override is audited. Approval moves ends_at to the requested end,
// clamped by the schedule window (and M6's budget limit).
func (s *Service) DecideExtension(ctx context.Context, u *auth.User, labID string, approve bool, note string) error {
	st, err := s.platform()
	if err != nil {
		return err
	}
	p := st.Platform
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
	if err != nil {
		return err
	}
	if inst.State != Ready || inst.ExtUntil == nil || inst.EndsAt == nil {
		return apperr.Wrap(apperr.Conflict, "there is no extension waiting on this lab")
	}
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		return err
	}
	over, err := s.overCap(ctx, p, inst.Team, inst.Training, inst.ExtEstimateUSD-inst.EstimateUSD)
	if err != nil {
		return err
	}
	if !(rbac.Checker{P: p}).MayApprove(u.Email, email, inst.Team, inst.Training, inst.ExtEstimateUSD, over) {
		return apperr.Wrap(apperr.Forbidden, "you can't decide this extension")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	note = strings.TrimSpace(cleanText(note))
	end, reason := *inst.ExtUntil, "ttl"
	if lim := s.scheduleLimit(inst, s.Now()); !lim.At.IsZero() && lim.At.Before(end) {
		end, reason = lim.At, lim.Reason
	}
	// M6: if budgetLimit exists, clamp end/reason by it here exactly as Extend does.
	closed := approve && !end.After(*inst.EndsAt)
	q := `UPDATE lab_instances SET ext_until = NULL, ext_requested_at = NULL, ext_tier = '' WHERE id = $1 AND state = 'ready' AND ext_until IS NOT NULL`
	args := []any{inst.ID}
	if approve && !closed {
		q = `UPDATE lab_instances SET ends_at = $2, limit_reason = $3, estimate_usd = ext_estimate_usd,
			ext_until = NULL, ext_requested_at = NULL, ext_tier = '' WHERE id = $1 AND state = 'ready' AND ext_until IS NOT NULL`
		args = append(args, end, reason)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Wrap(apperr.Conflict, "this extension was already decided")
	}
	action := "lab.extension.reject"
	if approve && !closed {
		action = "lab.extension.approve"
	}
	if err := audit.Log(ctx, tx, u.Email, action, inst.ID, map[string]any{"until": end, "estimate_usd": inst.ExtEstimateUSD, "over_cap": over, "note": note}, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if closed {
		s.event(ctx, inst.ID, "extension_closed", "the schedule window closes first")
		return apperr.Wrap(apperr.Conflict, "the schedule window closes before this extension would start; the request was closed")
	}
	kind, text := notify.LabRejected, "Your lab extension was not approved."
	if approve {
		kind, text = notify.LabApproved, "Your lab extension was approved: the lab now ends at "+end.UTC().Format("15:04 UTC")+"."
		s.event(ctx, inst.ID, "extended", end.Sub(*inst.EndsAt).String())
	} else {
		s.event(ctx, inst.ID, "extension_rejected", note)
	}
	if note != "" {
		text += " " + note
	}
	s.notify(ctx, notify.Event{Kind: kind, To: []string{email}, Subject: "Lab extension: " + map[bool]string{true: "approved", false: "not approved"}[approve], Text: text, Link: labLink(inst)})
	return nil
}
```

`internal/labs/http.go`, next to the decide route:

```go
	r.Post("/api/approvals/{id}/extension", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approve *bool  `json:"approve"` // required, as for requests
			Note    string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		if body.Approve == nil {
			httpx.Error(w, apperr.Wrap(apperr.Invalid, "approve (true or false) is required"))
			return
		}
		if err := s.DecideExtension(r.Context(), user(r), p(r, "id"), *body.Approve, body.Note); err != nil {
			httpx.Error(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
```

Web:
- `types.ts`: `extension_pending: boolean` on `LabView`; `kind: 'request' | 'extension'; extend_until?: string` on `Approval`.
- `Timer.tsx`: after the limit label, `{lab.extension_pending && <span className="badge warn" role="status">Extension pending</span>}`. The `Extend` button already hides because `can_extend` is false.
- `Approvals.tsx`:
  - For `a.kind === 'extension'`, title the card "Extension: {lab_title}" and show "until {time}".
  - Post to `/api/approvals/${a.id}/extension` with the same body.
  - Keep `key={a.kind + a.id}`, because one lab id can appear as both kinds in a race.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/labs/ -race && (cd web && npm test && npx tsc -b)`
Expected: PASS, including every existing approvals/schedule/extend test.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations internal/labs web/src
git commit -m "feat(labs): tier-raising extensions go back through approval (Extension pending)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Human review of self-reported lab results (spec §8.2)

**Depends on M5** (`internal/scoring`, M5's `labs.recompute` and `labs.Refresh`). Read those functions in the real code first; the snippets below use M5's planned names.

**Files:**
- Modify: `internal/scoring/scoring.go` (`KindLab`; `Submit` accepts it; detail shows lab evidence for any submission with a `LabID`)
- Modify: `internal/labs/review.go` (`selfReportReview`; `Refresh` handles `KindLab`)
- Modify: `internal/labs/service.go` (`recompute` consults `selfReportReview`)
- Modify: `internal/configapi/configapi.go` (`ProgramView/ProgramBody.ReviewSelfReported`, `SetProgram` writes it)
- Modify: `web/src/pages/ProgramSettings.tsx`, `web/src/types.ts`, `web/src/pages/Anvil.tsx` (label `self_reported` submissions "Self-reported lab")
- Create: `internal/labs/selfreport_test.go`

**Interfaces:**
- Consumes: `config.Program.ReviewSelfReported` (Task 1); `scoring.Service.Submit/Latest/Score/Return`, `scoring.Scored/Pending/Returned` (M5).
- Produces: `scoring.KindLab = "lab"`, `qtype "self_reported"`, item `"lab"`. `ProgramBody.ReviewSelfReported bool` (json `review_self_reported`).

- [ ] **Step 1: Write the failing test**

`internal/labs/selfreport_test.go` (M5's `setup` wires `f.s.Scoring`, and `completeFirstLab` exists in `cluster_test.go`):

```go
package labs

import (
	"context"
	"strings"
	"testing"

	"crucible/internal/scoring"
)

func (f *fx) labItem(t *testing.T) (string, float64) {
	t.Helper()
	var st string
	var sc float64
	_ = f.s.DB.QueryRow(context.Background(), `SELECT status, score FROM item_progress WHERE user_id = $1 AND module = '02-first-lab' AND item = 'lab'`, f.u.ID).Scan(&st, &sc)
	return st, sc
}

func TestSelfReportedLabWaitsForAScorer(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = true
	f.start(t)
	f.completeFirstLab(t)
	if st, _ := f.labItem(t); st != "pending_review" {
		t.Fatalf("a flagged program's local lab waits for review, got %q", st)
	}
	subs, err := f.s.Scoring.Latest(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", scoring.KindLab)
	sub := subs["lab"]
	if err != nil || sub == nil || sub.Status != scoring.Pending || sub.QType != "self_reported" || !strings.Contains(sub.Answer, "self-reported") {
		t.Fatalf("submission %+v %v", sub, err)
	}
	if _, err := f.s.Scoring.Score(ctx, f.other, sub.ID, sub.MaxPoints/2, "half of it was really done"); err != nil {
		t.Fatal(err)
	}
	if st, sc := f.labItem(t); st != "complete" || sc < 0.49 || sc > 0.51 {
		t.Fatalf("scored half: %q %v", st, sc)
	}
}

func TestReturnedSelfReportedLabMustBeRedone(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = true
	f.start(t)
	f.completeFirstLab(t)
	subs, _ := f.s.Scoring.Latest(ctx, f.u.ID, "forge", "forge-101", "02-first-lab", scoring.KindLab)
	if _, err := f.s.Scoring.Return(ctx, f.other, subs["lab"].ID, "the transcript does not show task 2"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_task_progress WHERE user_id = $1 AND module = '02-first-lab'`, f.u.ID).Scan(&n)
	if st, _ := f.labItem(t); st != "in_progress" || n != 0 {
		t.Fatalf("returned: item %q, %d task results left", st, n)
	}
}
```

The existing tests prove an unflagged program still completes at once. They must keep passing.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/labs/ -run 'SelfReported' -v`
Expected: FAIL (`ReviewSelfReported` is ignored, so the item is `complete`), or a compile error on `scoring.KindLab`.

- [ ] **Step 3: Implement**

`internal/scoring/scoring.go`:
- `KindLab = "lab"` next to `KindQuestion`/`KindTask`.
- In `Submit`'s validation, accept `sub.Kind == KindLab` only with `sub.LabID != ""`, `sub.Item == "lab"` and `sub.QType == "self_reported"`. Skip the human-question content lookup for it, and use `sub.MaxPoints` as given (the caller computed it from content).
- Wherever the Anvil detail attaches lab evidence for `KindTask`, do it for any submission whose `LabID != ""`.

`internal/labs/review.go`:

```go
// selfReportReview files a whole local lab for a scorer when its program asks for self-reported results to be
// reviewed (spec §8.2). wait reports that the lab item must stay pending_review; points is the scorer's decision once
// one exists.
func (s *Service) selfReportReview(ctx context.Context, inst *Instance, lab *content.Lab, maxScore float64, done map[string]taskRow) (wait bool, points *float64, err error) {
	st := s.Learn.State()
	if inst.Runtime != "local" || s.Scoring == nil || st == nil || st.Platform == nil {
		return false, nil, nil
	}
	t := st.Platform.Teams[inst.Team]
	if t == nil || t.Programs[inst.Training] == nil || !t.Programs[inst.Training].ReviewSelfReported {
		return false, nil, nil
	}
	latest, err := s.Scoring.Latest(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, scoring.KindLab)
	if err != nil {
		return false, nil, err
	}
	switch sub := latest["lab"]; {
	case sub != nil && sub.Status == scoring.Scored:
		return false, &sub.Points, nil
	case sub != nil && sub.Status == scoring.Pending:
		return true, nil, nil
	}
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		return false, nil, err
	}
	var b strings.Builder
	b.WriteString("Self-reported results from a laptop lab (local runtime):\n")
	for _, tk := range lab.Tasks {
		r := done[tk.ID]
		fmt.Fprintf(&b, "- %s: %s, %.2f / %.2f points\n", taskTitle(lab, tk), r.Status, r.Points, tk.Points)
	}
	_, err = s.Scoring.Submit(ctx, &auth.User{ID: inst.UserID, Email: email}, &scoring.Submission{
		Team: inst.Team, Training: inst.Training, Module: inst.Module, SHA: inst.SHA, Kind: scoring.KindLab, Item: "lab", LabID: inst.ID,
		QType: "self_reported", Prompt: "Review these self-reported lab results against the check output and transcripts.",
		Rubric:    "Award the points the evidence supports. Return the lab if the trainee should run it again.",
		MaxPoints: maxScore, Answer: b.String()}, nil)
	return true, nil, err
}
```

In M5's `recompute`, after the loop has summed `score`/`maxScore` and `waiting` is false (just before the `maxScore == 0` guard):

```go
	wait, decided, err := s.selfReportReview(ctx, inst, lab, maxScore, done)
	if err != nil {
		return err
	}
	if wait {
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "pending_review", 0)
	}
	if decided != nil {
		score = *decided // the scorer's points replace the self-reported total
	}
```

At the top of M5's `Refresh` (after `inst`/`lab` are loaded):

```go
	if sub.Kind == scoring.KindLab {
		if sub.Status == scoring.Returned {
			// Redo: the doubted results are cleared. Hint reveals stay, so no hint is charged twice.
			if _, err := s.DB.Exec(ctx, `DELETE FROM lab_task_progress WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`,
				inst.UserID, inst.Team, inst.Training, inst.Module); err != nil {
				return err
			}
			return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "in_progress", 0)
		}
		return s.recompute(ctx, inst, lab)
	}
```

`internal/configapi/configapi.go`:
- `ReviewSelfReported bool \`json:"review_self_reported"\`` on `ProgramView` (filled from `p.ReviewSelfReported`) and on `ProgramBody`;
- in `SetProgram`, `set["review_self_reported"] = nil`, then `if b.ReviewSelfReported { set["review_self_reported"] = true }`;
- add it to the audit detail.

`web/src/pages/ProgramSettings.tsx`: a checkbox labelled "Scorers review self-reported (laptop) lab results", bound to `review_self_reported`. Add the field to `ProgramConfig` in `types.ts`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/labs/ ./internal/scoring/ ./internal/configapi/ -race && (cd web && npm test && npx tsc -b)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scoring internal/labs internal/configapi web/src
git commit -m "feat(labs): programs can require a scorer to review self-reported lab results

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 10: `gitsync.ContentRepo`: edit branches, diffs and bot merges on plain git

**Files:**
- Modify: `internal/gitsync/writer.go` (extract `syncClone` and `raced`; `Writer` uses them)
- Create: `internal/gitsync/content_repo.go`, `internal/gitsync/content_repo_test.go`
- Create: `internal/gitsync/paths.go` (`NoSymlinks`, moved from `configapi`)
- Modify: `internal/configapi/configapi.go` (call `gitsync.NoSymlinks`; delete the local copy)

**Interfaces:**
- Produces:
  ```go
  type ContentRepo struct{ URL, Branch, Dir, Name, Email string } // + unexported lock
  var ErrMergeConflict error // apperr.Conflict
  var ErrMergeInvalid error  // apperr.Conflict; wrapped with the first lint problem
  func (c *ContentRepo) PushEdit(ctx context.Context, branch, base string, files map[string]string, author, msg string) (sha, diff string, err error)
  func (c *ContentRepo) Merge(ctx context.Context, branch, msg, actor string) (sha string, err error)
  func (c *ContentRepo) DeleteBranch(ctx context.Context, branch string) error
  func NoSymlinks(dir, rel string) error
  ```
  Branch names must match `^crucible/edit/[0-9]+$`. `base` must be a 40-hex commit.

- [ ] **Step 1: Write the failing tests**

`internal/gitsync/content_repo_test.go` (reuses `bare`, `gitOut`, `run` and `newRepo` from `writer_test.go`):

```go
package gitsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/apperr"
)

func trainingFiles() map[string]string {
	return map[string]string{
		"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [m@x]\nmodules: [m1]\n",
		"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n",
		"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
	}
}

func contentRepo(t *testing.T, remote string) *ContentRepo {
	return &ContentRepo{URL: remote, Branch: "main", Dir: filepath.Join(t.TempDir(), "edits"), Name: "Crucible", Email: "bot@x"}
}

// pushTo commits files to the remote's main from a separate clone (someone else pushing).
func pushTo(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "other")
	run(t, "", "clone", "-q", remote, work)
	for rel, body := range files {
		if err := writeFile(work, rel, body); err != nil {
			t.Fatal(err)
		}
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qm", "other change")
	run(t, work, "push", "-q", "origin", "HEAD:main")
}

func TestPushEditAndMerge(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	sha, diff, err := c.PushEdit(ctx, "crucible/edit/1", base, map[string]string{
		"modules/m1/reading/intro.md":   "# Intro\n\nHello, smith.\n",
		"modules/m1/checks/new-check.sh": "#!/bin/sh\nexit 0\n",
	}, "A@X", "Greet the smith")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+Hello, smith.") || gitOut(t, remote, "rev-parse", "crucible/edit/1") != sha {
		t.Fatalf("diff %q / branch", diff)
	}
	if got := gitOut(t, remote, "log", "-1", "--format=%an <%ae>|%cn|%B", sha); !strings.Contains(got, "a@x <a@x>|Crucible|Greet the smith") || !strings.Contains(got, "Crucible-Actor: a@x") {
		t.Fatalf("author is the user, committer the bot: %q", got)
	}
	if mode := gitOut(t, remote, "ls-tree", sha, "modules/m1/checks/new-check.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("new scripts are executable: %q", mode)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("pushing an edit never touches main")
	}
	merged, err := c.Merge(ctx, "crucible/edit/1", "crucible: merge edit 1", "m@x")
	if err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(gitOut(t, remote, "rev-list", "--parents", "-n1", "main")); len(parents) != 3 || parents[0] != merged {
		t.Fatalf("a merge commit on main: %v", parents)
	}
	if body := gitOut(t, remote, "show", "main:modules/m1/reading/intro.md"); !strings.Contains(body, "smith") {
		t.Fatal("merged content")
	}
	if err := c.DeleteBranch(ctx, "crucible/edit/1"); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, remote, "branch", "--list", "crucible/edit/1"); out != "" {
		t.Fatalf("branch deleted: %q", out)
	}
}

func TestMergeConflictPushesNothing(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/2", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine"); err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nTheirs.\n"})
	tip := gitOut(t, remote, "rev-parse", "main")
	if _, err := c.Merge(ctx, "crucible/edit/2", "merge", "m@x"); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != tip {
		t.Fatal("a conflicting merge pushes nothing")
	}
}

func TestMergeLandsOnTopOfNewerCommits(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/3", base, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"}, "a@x", "mine"); err != nil {
		t.Fatal(err)
	}
	pushTo(t, remote, map[string]string{"modules/m1/reading/extra.md": "# Extra\n"})
	if _, err := c.Merge(ctx, "crucible/edit/3", "merge", "m@x"); err != nil {
		t.Fatal(err)
	}
	if gitOut(t, remote, "show", "main:modules/m1/reading/extra.md") == "" || !strings.Contains(gitOut(t, remote, "show", "main:modules/m1/reading/intro.md"), "Mine.") {
		t.Fatal("both changes on main")
	}
}

func TestMergeRefusesInvalidResult(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/4", base, map[string]string{"training.yaml": "id: t1\ntitle: T1\nmodules: [m1, missing]\n"}, "a@x", "break it"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, "crucible/edit/4", "merge", "m@x"); !errors.Is(err, ErrMergeInvalid) || !errors.Is(err, apperr.Conflict) {
		t.Fatalf("invalid result: %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") != base {
		t.Fatal("nothing pushed")
	}
}

func TestPushEditRefusesSymlinksAndBadNames(t *testing.T) {
	ctx := context.Background()
	remote := bare(t, trainingFiles())
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "clone", "-q", remote, work)
	if err := os.Symlink("/etc", filepath.Join(work, "modules", "m1", "linked")); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.name=o", "-c", "user.email=o@x", "commit", "-qm", "symlink")
	run(t, work, "push", "-q", "origin", "HEAD:main")
	base := gitOut(t, remote, "rev-parse", "main")
	c := contentRepo(t, remote)
	if _, _, err := c.PushEdit(ctx, "crucible/edit/5", base, map[string]string{"modules/m1/linked/passwd.md": "x"}, "a@x", "x"); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("writing through a symlinked folder: %v", err)
	}
	for _, b := range []string{"main", "crucible/edit/x", "--upload-pack=evil"} {
		if _, _, err := c.PushEdit(ctx, b, base, map[string]string{"a.md": "x"}, "a@x", "x"); err == nil {
			t.Fatalf("branch %q accepted", b)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/gitsync/ -run 'PushEdit|Merge' -v`
Expected: compile errors (`ContentRepo` undefined).

- [ ] **Step 3: Implement**

`internal/gitsync/paths.go`: move `noSymlinks` from `internal/configapi/configapi.go` here, as exported `NoSymlinks` with the same body and doc comment. In `configapi.go`, replace both uses with `gitsync.NoSymlinks` and delete the local function.

`internal/gitsync/writer.go`: replace `reset` and `checkout` with a shared helper, plus a `raced` helper used by `try`:

```go
// syncClone makes dir a clean checkout of url's branch tip, cloning on first use and re-cloning a broken copy once.
func syncClone(ctx context.Context, url, branch, dir string) error {
	if strings.HasPrefix(url, "-") || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid repo %q or branch %q", url, branch)
	}
	err := checkoutTip(ctx, url, branch, dir)
	if err != nil && ctx.Err() == nil {
		_ = os.RemoveAll(dir)
		err = checkoutTip(ctx, url, branch, dir)
	}
	return err
}

func checkoutTip(ctx context.Context, url, branch, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, "", "clone", "-q", "--branch", branch, "--", url, dir); err != nil {
			return err
		}
	}
	if _, err := git(ctx, dir, "fetch", "-q", "origin", branch); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	_, err := git(ctx, dir, "clean", "-qfdx")
	return err
}

// raced reports a push rejected because the remote branch moved.
func raced(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "[rejected]")
}
```

`Writer.try` calls `syncClone(ctx, w.URL, w.Branch, w.Dir)` instead of `w.reset(ctx)`, and `if raced(err) { return "", false, errRaced }` on push failure. The `reset` and `checkout` methods are deleted.

`internal/gitsync/content_repo.go`:

```go
package gitsync

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"crucible/internal/apperr"
	"crucible/internal/content"
)

// ContentRepo is the bot's working clone of one content repo for edits made in the UI (spec §6). An edit is pushed to its
// own branch; once a maintainer approves it, the bot merges that branch into the tracked branch with a merge commit.
// It needs nothing but plain git, so it works with any host.
type ContentRepo struct {
	URL, Branch string // the training's repo and tracked branch (trainings.yaml)
	Dir         string // working clone, owned by this value
	Name, Email string // bot identity (committer)

	once sync.Once
	sem  chan struct{} // one git operation at a time per repo; a channel so waiting respects ctx
}

var (
	ErrMergeConflict = apperr.Wrap(apperr.Conflict, "this edit no longer applies cleanly to the current content")
	ErrMergeInvalid  = apperr.Wrap(apperr.Conflict, "merged with the current content, this edit would be invalid")
	editBranchRE     = regexp.MustCompile(`^crucible/edit/[0-9]+$`)
)

const maxDiff = 256 << 10

func (c *ContentRepo) lock(ctx context.Context) (func(), error) {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PushEdit commits files (repo-relative path → new content) on top of base as the user, pushes them as branch, and
// returns the commit and its unified diff against base (capped at 256 KiB). New *.sh files are executable; existing
// files keep their mode.
func (c *ContentRepo) PushEdit(ctx context.Context, branch, base string, files map[string]string, author, msg string) (string, string, error) {
	if !editBranchRE.MatchString(branch) || !shaRE.MatchString(base) {
		return "", "", fmt.Errorf("invalid edit branch %q or base %q", branch, base)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, c.Dir, "checkout", "-q", "--detach", base); err != nil {
		return "", "", apperr.Wrap(apperr.Conflict, "the content changed since you opened it; reload and redo your change")
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if err := NoSymlinks(c.Dir, rel); err != nil {
			return "", "", apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s can't be edited: %v", rel, err))
		}
		p := filepath.Join(c.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", "", err
		}
		mode := os.FileMode(0o644)
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		} else if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(files[rel]), mode); err != nil {
			return "", "", err
		}
		if err := os.Chmod(p, mode); err != nil {
			return "", "", err
		}
	}
	author = strings.ToLower(author)
	if _, err := git(ctx, c.Dir, "add", "-A"); err != nil {
		return "", "", err
	}
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "commit", "-q",
		"--author", author+" <"+author+">", "-m", msg, "-m", "Crucible-Actor: "+author); err != nil {
		return "", "", err
	}
	sha, err := git(ctx, c.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	diff, err := git(ctx, c.Dir, "diff", "--no-color", base, sha)
	if err != nil {
		return "", "", err
	}
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n… (diff truncated)"
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", "", err
	}
	return sha, diff, nil
}

// Merge merges branch into the tracked branch with a bot merge commit and pushes it. When the push is rejected because
// the branch moved, it starts again from the new tip, up to 3 retries. A conflict (ErrMergeConflict) or a merged tree
// that content.Load rejects (ErrMergeInvalid) pushes nothing.
func (c *ContentRepo) Merge(ctx context.Context, branch, msg, actor string) (string, error) {
	if !editBranchRE.MatchString(branch) {
		return "", fmt.Errorf("invalid edit branch %q", branch)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	for attempt := 0; ; attempt++ {
		sha, err := c.tryMerge(ctx, branch, msg, strings.ToLower(actor))
		if err != errRaced {
			return sha, err
		}
		if attempt == 3 {
			return "", apperr.Wrap(apperr.Conflict, "the content repo is busy; try again")
		}
	}
}

func (c *ContentRepo) tryMerge(ctx context.Context, branch, msg, actor string) (string, error) {
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return "", err
	}
	if _, err := git(ctx, c.Dir, "fetch", "-q", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return "", err
	}
	if _, err := git(ctx, c.Dir, "-c", "user.name="+c.Name, "-c", "user.email="+c.Email, "merge", "-q", "--no-ff", "--no-edit",
		"-m", msg, "-m", "Crucible-Actor: "+actor, "origin/"+branch); err != nil {
		_, _ = git(ctx, c.Dir, "merge", "--abort")
		return "", ErrMergeConflict
	}
	if _, probs := content.Load(c.Dir); len(probs) > 0 {
		return "", fmt.Errorf("%w: %s", ErrMergeInvalid, probs[0])
	}
	if _, err := git(ctx, c.Dir, "push", "-q", "origin", "HEAD:refs/heads/"+c.Branch); err != nil {
		if raced(err) {
			return "", errRaced
		}
		return "", err
	}
	return git(ctx, c.Dir, "rev-parse", "HEAD")
}

// DeleteBranch removes an edit branch from the remote once the edit is decided.
func (c *ContentRepo) DeleteBranch(ctx context.Context, branch string) error {
	if !editBranchRE.MatchString(branch) {
		return fmt.Errorf("invalid edit branch %q", branch)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := syncClone(ctx, c.URL, c.Branch, c.Dir); err != nil {
		return err
	}
	_, err = git(ctx, c.Dir, "push", "-q", "origin", "--delete", branch)
	return err
}
```

`content.Load` on the working clone ignores `.git`, because its symlink walk skips it. Check that `content` does not import `gitsync`: it must not, or this is an import cycle. `syncer.go` already imports `content`, so the direction is right.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gitsync/ ./internal/configapi/ -race`
Expected: PASS, including every existing `Writer` test (the refactor keeps behaviour).

- [ ] **Step 5: Commit**

```bash
git add internal/gitsync internal/configapi
git commit -m "feat(gitsync): content edit branches, stored diffs and validated bot merges on plain git

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: The edits service: proposals, review queue, merge (spec §6, §5.3)

**Files:**
- Create: `internal/db/migrations/000NN_content_edits.sql`
- Modify: `internal/notify/notify.go` (`ContentEdit`)
- Create: `internal/edits/edits.go`, `internal/edits/http.go`, `internal/edits/edits_test.go`
- Modify: `internal/httpapi/server.go` (`Deps.Edits`, routes, `can_edit_content` on `/api/me`)
- Modify: `cmd/crucible-api/main.go` (a `ContentRepo` per training, cached)

**Interfaces:**
- Consumes: `gitsync.ContentRepo` (Task 10), `content.Load`, `audit.Log`, `rbac.Checker.IsAdmin`.
- Produces:
  ```go
  type Edit struct{ ID int64; Training, Title, Author, BaseSHA, Branch, Status, Reviewer, Note, MergeSHA string
      CreatedAt time.Time; DecidedAt *time.Time; Files map[string]string; Diff string; CanReview, CanWithdraw bool }
  // json: id training title author base_sha branch status reviewer note merge_sha created_at decided_at files diff can_review can_withdraw
  type NewEdit struct{ Training, BaseSHA, Title string; Files map[string]string } // json training base_sha title files
  type FileInfo struct{ Path string; Size int64 }                               // json path size
  type Service struct{ DB; State func() *gitsync.State; Repo func(training string) *gitsync.ContentRepo; Notify Notifier; Resync func(context.Context) error; Log *slog.Logger }
  func (s *Service) Trainings(u *auth.User) []TrainingRef // json [{id,title}] the user may edit
  func (s *Service) Files(u *auth.User, training string) (headSHA string, files []FileInfo, err error)
  func (s *Service) File(u *auth.User, training, rel string) (string, error)
  func (s *Service) Create(ctx context.Context, u *auth.User, in NewEdit) (*Edit, error)
  func (s *Service) List(ctx context.Context, u *auth.User) ([]Edit, error)
  func (s *Service) Get(ctx context.Context, u *auth.User, id int64) (*Edit, error)
  func (s *Service) Approve / Reject (ctx, u, id int64, note string) (*Edit, error); Withdraw(ctx, u, id int64) (*Edit, error)
  func (s *Service) CanUse(email string) bool // for /api/me can_edit_content
  Routes: GET /api/content (trainings), GET /api/content/{training}/files, GET /api/content/{training}/file?path=,
          GET|POST /api/edits, GET /api/edits/{id}, POST /api/edits/{id}/{approve|reject|withdraw} {note}
  notify.ContentEdit = "content_edit"
  ```
- Statuses: `pending | merged | rejected | withdrawn | stale`. Limits: ≤ 20 files, each ≤ 256 KiB, the whole request ≤ 1 MiB (`httpx.Read`'s cap), title ≤ 200, note ≤ 2000.

- [ ] **Step 1: Write the migration and the notification kind**

```sql
-- +goose Up
-- Content edits made in the UI (spec §6): pushed to a branch, reviewed by a maintainer in Crucible, merged by the bot.
CREATE TABLE content_edits (
  id         BIGSERIAL PRIMARY KEY,
  training   TEXT NOT NULL,
  title      TEXT NOT NULL,
  author     TEXT NOT NULL,                -- email, lowercased
  base_sha   TEXT NOT NULL,                -- tracked-branch head the author edited
  branch     TEXT NOT NULL DEFAULT '',     -- crucible/edit/<id>
  head_sha   TEXT NOT NULL DEFAULT '',     -- the edit commit
  files      JSONB NOT NULL,               -- {path: new content}, changed files only
  diff       TEXT NOT NULL DEFAULT '',     -- unified diff vs base, computed by git at push time
  status     TEXT NOT NULL,                -- pending | merged | rejected | withdrawn | stale
  reviewer   TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  merge_sha  TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);
CREATE INDEX content_edits_pending ON content_edits (training) WHERE status = 'pending';

-- +goose Down
DROP TABLE content_edits;
```

Append to `notify.go`: `ContentEdit Kind = "content_edit"` and `{ContentEdit, "A content edit waits for my review, or mine was decided"}`.

- [ ] **Step 2: Write the failing tests**

`internal/edits/edits_test.go`:

```go
package edits

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
)

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

type notes struct {
	mu  sync.Mutex
	evs []notify.Event
}

func (n *notes) Notify(_ context.Context, ev notify.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evs = append(n.evs, ev)
	return nil
}

type fx struct {
	s                                *Service
	remote                           string
	notes                            *notes
	admin, leader, senior, trainee   *auth.User
}

// setup: training t1 (maintainer senior@) in a bare repo; trainee@ is enrolled in it through team forge.
func setup(t *testing.T) *fx {
	t.Helper()
	work := t.TempDir()
	files := map[string]string{
		"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local]\nprogression: free\nmodules: [m1]\n",
		"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
		"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
		"modules/m1/quiz.yaml":        "questions:\n  - {id: q1, type: single, prompt: Hot?, options: [no, yes], answer: 1}\n",
	}
	for rel, body := range files {
		p := filepath.Join(work, rel)
		_ = exec.Command("mkdir", "-p", filepath.Dir(p)).Run()
		if err := writeFile(p, body); err != nil {
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
	n := &notes{}
	s := &Service{DB: dbtest.New(t), State: func() *gitsync.State { return st }, Notify: n,
		Repo: func(id string) *gitsync.ContentRepo {
			if id == "t1" {
				return repo
			}
			return nil
		}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: s, remote: remote, notes: n, admin: u("admin@crucible.local"), leader: u("leader@crucible.local"),
		senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) head() string { return f.s.State().Heads["t1"] }

func (f *fx) propose(t *testing.T, u *auth.User, files map[string]string) (*Edit, error) {
	t.Helper()
	return f.s.Create(context.Background(), u, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Warmer intro", Files: files})
}

func TestEditPermissions(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, _, err := f.s.Files(f.trainee, "t1"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("an enrolled trainee must not read the answer keys: %v", err)
	}
	if _, err := f.s.File(f.trainee, "t1", "modules/m1/quiz.yaml"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("file: %v", err)
	}
	if _, err := f.propose(t, f.trainee, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee proposing: %v", err)
	}
	// The leader proposes an edit that also names themselves a maintainer.
	e, err := f.propose(t, f.leader, map[string]string{
		"modules/m1/reading/intro.md": "# Intro\n\nHello, smith.\n",
		"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local, leader@crucible.local]\nprogression: free\nmodules: [m1]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "pending" || !strings.Contains(e.Diff, "+Hello, smith.") || e.Branch != "crucible/edit/"+itoa(e.ID) {
		t.Fatalf("created %+v", e)
	}
	if _, err := f.s.Approve(ctx, f.leader, e.ID, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("the author never approves, even as a maintainer named inside the edit: %v", err)
	}
	if _, err := f.s.Approve(ctx, f.trainee, e.ID, ""); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("an enrolled trainee can't even see the edit: %v", err)
	}
	got, err := f.s.Approve(ctx, f.senior, e.ID, "lovely")
	if err != nil || got.Status != "merged" || got.MergeSHA == "" {
		t.Fatalf("maintainer approves: %+v %v", got, err)
	}
	if git(t, f.remote, "rev-parse", "main") != got.MergeSHA {
		t.Fatal("merge pushed")
	}
	var notified []string
	for _, ev := range f.notes.evs {
		if ev.Kind == notify.ContentEdit && ev.Team != "" {
			t.Fatal("edit notifications never go to a team channel")
		}
		notified = append(notified, strings.Join(ev.To, ","))
	}
	if len(notified) < 2 || strings.Contains(notified[0], "leader@") || !strings.Contains(notified[0], "senior@") || notified[len(notified)-1] != "leader@crucible.local" {
		t.Fatalf("reviewers then the author are told: %v", notified)
	}
}

func TestEditPathRules(t *testing.T) {
	f := setup(t)
	for _, p := range []string{"../platform.yaml", ".git/hooks/post-merge", "modules/m1/../../.git/config", "modules/m1/reading/x.png",
		`modules\m1\x.md`, "/etc/passwd.md", "", "modules/.hidden/x.md"} {
		if _, err := f.propose(t, f.leader, map[string]string{p: "x"}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%q: %v", p, err)
		}
	}
	if refs := git(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads"); refs != "refs/heads/main" {
		t.Fatalf("no branch was pushed for a refused path: %q", refs)
	}
}

func TestCreateValidates(t *testing.T) {
	f := setup(t)
	if _, err := f.propose(t, f.leader, map[string]string{"training.yaml": "id: t1\ntitle: T1\nmodules: [m1, nope]\n"}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "would break") {
		t.Fatalf("invalid content: %v", err)
	}
	if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHello.\n"}); !errors.Is(err, apperr.Invalid) || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("no-op: %v", err)
	}
	if _, err := f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: strings.Repeat("0", 40), Title: "x",
		Files: map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"}}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("stale base: %v", err)
	}
}

func TestOnlyOneApprovalMerges(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, u := range []*auth.User{f.senior, f.admin} {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = f.s.Approve(ctx, u, e.ID, "") }()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("exactly one approval wins: %v", errs)
	}
	if merges := git(t, f.remote, "rev-list", "--merges", "--count", "main"); merges != "1" {
		t.Fatalf("one merge commit, got %s", merges)
	}
}

func TestApproveStaleEdit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"})
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "o")
	git(t, "", "clone", "-q", f.remote, other)
	_ = writeFile(filepath.Join(other, "modules/m1/reading/intro.md"), "# Intro\n\nTheirs.\n")
	git(t, other, "commit", "-qam", "theirs")
	git(t, other, "push", "-q", "origin", "HEAD:main")
	tip := git(t, f.remote, "rev-parse", "main")
	if _, err := f.s.Approve(ctx, f.senior, e.ID, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("stale: %v", err)
	}
	got, _ := f.s.Get(ctx, f.leader, e.ID)
	if got.Status != "stale" || git(t, f.remote, "rev-parse", "main") != tip {
		t.Fatalf("marked stale, main untouched: %+v", got)
	}
	if refs := git(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/crucible"); refs != "" {
		t.Fatalf("branch removed: %q", refs)
	}
}

func TestRejectAndWithdraw(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e1, _ := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nA.\n"})
	if got, err := f.s.Reject(ctx, f.senior, e1.ID, "not this way"); err != nil || got.Status != "rejected" {
		t.Fatalf("reject: %+v %v", got, err)
	}
	e2, _ := f.s.Create(ctx, f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "B", Files: map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nB.\n"}})
	if _, err := f.s.Withdraw(ctx, f.senior, e2.ID); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only the author withdraws: %v", err)
	}
	if got, err := f.s.Withdraw(ctx, f.leader, e2.ID); err != nil || got.Status != "withdrawn" {
		t.Fatalf("withdraw: %+v %v", got, err)
	}
	if list, _ := f.s.List(ctx, f.trainee); len(list) != 0 {
		t.Fatal("enrolled people see no edits of that training")
	}
}
```

Add the two tiny helpers at the bottom of the test file: `func writeFile(p, body string) error { return os.WriteFile(p, []byte(body), 0o644) }` (import `os`) and `func itoa(n int64) string { return strconv.FormatInt(n, 10) }` (import `strconv`). Replace the `exec.Command("mkdir"…)` with `os.MkdirAll(filepath.Dir(p), 0o755)`.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./internal/edits/ -v`
Expected: compile errors (package `edits` has no `Service`).

- [ ] **Step 4: Implement**

`internal/edits/edits.go`:

```go
// Package edits lets maintainers and leads change training content from the UI (spec §6): every edit is pushed to its
// own branch, reviewed in Crucible by a maintainer other than its author, and merged by the bot. Plain git, any host.
package edits

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type Service struct {
	DB     *pgxpool.Pool
	State  func() *gitsync.State
	Repo   func(training string) *gitsync.ContentRepo // the bot's clone of a training's repo; nil when unknown
	Notify Notifier
	Resync func(ctx context.Context) error // re-read git after a merge so trainees see it at once
	Log    *slog.Logger
}

type Edit struct {
	ID          int64             `json:"id"`
	Training    string            `json:"training"`
	Title       string            `json:"title"`
	Author      string            `json:"author"`
	BaseSHA     string            `json:"base_sha"`
	Branch      string            `json:"branch"`
	Status      string            `json:"status"`
	Reviewer    string            `json:"reviewer,omitempty"`
	Note        string            `json:"note,omitempty"`
	MergeSHA    string            `json:"merge_sha,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	DecidedAt   *time.Time        `json:"decided_at,omitempty"`
	Files       map[string]string `json:"files,omitempty"` // detail only
	Diff        string            `json:"diff,omitempty"`  // detail only
	CanReview   bool              `json:"can_review"`
	CanWithdraw bool              `json:"can_withdraw"`
}

type NewEdit struct {
	Training string            `json:"training"`
	BaseSHA  string            `json:"base_sha"`
	Title    string            `json:"title"`
	Files    map[string]string `json:"files"`
}

type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type TrainingRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

const (
	maxFiles     = 20
	maxFileBytes = 256 << 10
	maxTitle     = 200
	maxNote      = 2000
	cols         = `id, training, title, author, base_sha, branch, status, reviewer, note, merge_sha, created_at, decided_at, files, diff`
)

var editable = map[string]bool{".md": true, ".yaml": true, ".yml": true, ".sh": true}

func clean(s string) string { return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "") }

func (s *Service) state() (*gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// head is the training version at its tracked branch head: edits are based on it, and its maintainers review them.
// Never the edit's own training.yaml, so an author cannot make themselves a reviewer.
func head(st *gitsync.State, training string) (*content.Training, string) {
	sha := st.Heads[training]
	return st.Training(training, sha), sha
}

// enrolled: anyone enrolled in the training (any team) could read its answer keys through this API, so they get none.
func enrolled(p *config.Platform, training, email string) bool {
	for _, t := range p.Teams {
		if pr := t.Programs[training]; pr != nil && slices.Contains(pr.Enrolled, email) {
			return true
		}
	}
	return false
}

func canPropose(p *config.Platform, t *content.Training, email string) bool {
	email = strings.ToLower(email)
	if enrolled(p, t.ID, email) {
		return false
	}
	if (rbac.Checker{P: p}).IsAdmin(email) || slices.Contains(t.Maintainers, email) {
		return true
	}
	for _, team := range p.Teams {
		if r := team.RoleOf(email); r == "leader" || r == "senior" {
			return true
		}
	}
	return false
}

// canReview: admins and the head version's maintainers (spec §5.3), never the author, never someone enrolled.
func canReview(p *config.Platform, t *content.Training, email, author string) bool {
	email = strings.ToLower(email)
	return email != author && !enrolled(p, t.ID, email) && ((rbac.Checker{P: p}).IsAdmin(email) || slices.Contains(t.Maintainers, email))
}

func validPath(rel string) error {
	bad := func(why string) error { return apperr.Wrap(apperr.Invalid, fmt.Sprintf("%q: %s", rel, why)) }
	if rel == "" || strings.Contains(rel, `\`) || path.IsAbs(rel) || path.Clean(rel) != rel || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return bad("must be a plain path inside the repo")
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return bad("hidden files and folders can't be edited here")
		}
	}
	if !editable[strings.ToLower(path.Ext(rel))] {
		return bad("only .md, .yaml, .yml and .sh files can be edited here; change other files in git")
	}
	return nil
}

func (s *Service) training(u *auth.User, id string, propose bool) (*gitsync.State, *content.Training, string, error) {
	st, err := s.state()
	if err != nil {
		return nil, nil, "", err
	}
	t, sha := head(st, id)
	if t == nil {
		return nil, nil, "", apperr.Wrap(apperr.Unavailable, "this training has no valid content at its branch head; fix it in git first")
	}
	if propose && !canPropose(st.Platform, t, u.Email) {
		return nil, nil, "", apperr.Wrap(apperr.Forbidden, "you can't edit this training")
	}
	return st, t, sha, nil
}

// Trainings lists the trainings the user may propose edits to.
func (s *Service) Trainings(u *auth.User) []TrainingRef {
	out := []TrainingRef{}
	st, err := s.state()
	if err != nil {
		return out
	}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		if t, _ := head(st, id); t != nil && canPropose(st.Platform, t, u.Email) {
			out = append(out, TrainingRef{ID: id, Title: t.Title})
		}
	}
	return out
}

// CanUse reports whether the user may propose or review any edit (the nav link).
func (s *Service) CanUse(email string) bool {
	st, err := s.state()
	if err != nil {
		return false
	}
	for id := range st.Platform.Trainings {
		if t, _ := head(st, id); t != nil && (canPropose(st.Platform, t, email) || canReview(st.Platform, t, email, "")) {
			return true
		}
	}
	return false
}

func (s *Service) Files(u *auth.User, training string) (string, []FileInfo, error) {
	_, t, sha, err := s.training(u, training, true)
	if err != nil {
		return "", nil, err
	}
	out := []FileInfo{}
	err = filepath.WalkDir(t.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != t.Dir {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(t.Dir, p)
		rel = filepath.ToSlash(rel)
		if d.Type().IsRegular() && validPath(rel) == nil {
			fi, _ := d.Info()
			out = append(out, FileInfo{Path: rel, Size: fi.Size()})
		}
		return nil
	})
	return sha, out, err
}

func (s *Service) File(u *auth.User, training, rel string) (string, error) {
	if err := validPath(rel); err != nil {
		return "", err
	}
	_, t, _, err := s.training(u, training, true)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	return string(b), nil
}

func (s *Service) Create(ctx context.Context, u *auth.User, in NewEdit) (*Edit, error) {
	st, t, sha, err := s.training(u, in.Training, true)
	if err != nil {
		return nil, err
	}
	if in.BaseSHA != sha {
		return nil, apperr.Wrap(apperr.Conflict, "the content changed since you opened it; reload and redo your change")
	}
	title := strings.TrimSpace(clean(in.Title))
	if title == "" || len(title) > maxTitle {
		return nil, apperr.Wrap(apperr.Invalid, "give the edit a title of at most 200 characters")
	}
	if len(in.Files) == 0 || len(in.Files) > maxFiles {
		return nil, apperr.Wrap(apperr.Invalid, "an edit changes 1 to 20 files")
	}
	tmp, err := os.MkdirTemp("", "crucible-edit-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, os.DirFS(t.Dir)); err != nil {
		return nil, err
	}
	changed := map[string]string{}
	for rel, body := range in.Files {
		if err := validPath(rel); err != nil {
			return nil, err
		}
		if len(body) > maxFileBytes || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s must be UTF-8 text of at most 256 KiB", rel))
		}
		if old, err := os.ReadFile(filepath.Join(t.Dir, filepath.FromSlash(rel))); err == nil && string(old) == body {
			continue
		}
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil { // lint wants scripts executable; mode is not committed from here
			return nil, err
		}
		changed[rel] = body
	}
	if len(changed) == 0 {
		return nil, apperr.Wrap(apperr.Invalid, "nothing changed")
	}
	nt, probs := content.Load(tmp)
	if len(probs) == 0 && nt.ID != in.Training {
		probs = []content.Problem{{File: "training.yaml", Msg: "the training id must stay " + in.Training}}
	}
	if len(probs) > 0 {
		msgs := []string{}
		for _, p := range probs[:min(len(probs), 10)] {
			msgs = append(msgs, p.String())
		}
		return nil, apperr.Wrap(apperr.Invalid, "this edit would break the training: "+strings.Join(msgs, "; "))
	}
	repo := s.Repo(in.Training)
	if repo == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "this training's repo is not available for edits")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO content_edits (training, title, author, base_sha, files, status)
		VALUES ($1, $2, lower($3), $4, $5, 'pending') RETURNING id`, in.Training, title, u.Email, sha, changed).Scan(&id); err != nil {
		return nil, err
	}
	branch := fmt.Sprintf("crucible/edit/%d", id)
	// ponytail: the push happens inside the transaction; if the commit then fails, an orphan branch is left in the repo.
	headSHA, diff, err := repo.PushEdit(ctx, branch, sha, changed, u.Email, "crucible: "+title)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE content_edits SET branch = $2, head_sha = $3, diff = $4 WHERE id = $1`, id, branch, headSHA, diff); err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, u.Email, "content_edit.propose", in.Training, map[string]any{"edit": id, "title": title, "files": slices.Sorted(maps.Keys(changed))}, headSHA); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	var to []string
	for _, e := range slices.Concat(t.Maintainers, st.Platform.Admins) {
		if canReview(st.Platform, t, e, strings.ToLower(u.Email)) && !slices.Contains(to, e) {
			to = append(to, e)
		}
	}
	s.notify(ctx, notify.Event{Kind: notify.ContentEdit, To: to, Subject: "Content edit waiting for review: " + title,
		Text: fmt.Sprintf("%s proposed a change to %s.", strings.ToLower(u.Email), t.Title), Link: fmt.Sprintf("/edits/%d", id)})
	return s.Get(ctx, u, id)
}

func (s *Service) notify(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || len(ev.To) == 0 {
		return
	}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil && s.Log != nil {
		s.Log.Error("queueing a content-edit notification failed", "err", err)
	}
}

func scan(row pgx.Row) (*Edit, error) {
	var e Edit
	err := row.Scan(&e.ID, &e.Training, &e.Title, &e.Author, &e.BaseSHA, &e.Branch, &e.Status, &e.Reviewer, &e.Note, &e.MergeSHA,
		&e.CreatedAt, &e.DecidedAt, &e.Files, &e.Diff)
	if err == pgx.ErrNoRows {
		return nil, apperr.Wrap(apperr.NotFound, "edit not found")
	}
	return &e, err
}

// visible: the author and anyone who may review it; nobody enrolled in the training.
func (s *Service) visible(st *gitsync.State, u *auth.User, e *Edit) bool {
	me := strings.ToLower(u.Email)
	if enrolled(st.Platform, e.Training, me) {
		return false
	}
	t, _ := head(st, e.Training)
	e.CanReview = e.Status == "pending" && t != nil && canReview(st.Platform, t, me, e.Author)
	e.CanWithdraw = e.Status == "pending" && e.Author == me
	return e.Author == me || (t != nil && canReview(st.Platform, t, me, "")) || (rbac.Checker{P: st.Platform}).IsAdmin(me)
}

func (s *Service) List(ctx context.Context, u *auth.User) ([]Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM content_edits ORDER BY (status = 'pending') DESC, id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Edit{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if s.visible(st, u, e) {
			e.Files, e.Diff = nil, ""
			out = append(out, *e)
		}
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, u *auth.User, id int64) (*Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	e, err := scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM content_edits WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	if !s.visible(st, u, e) {
		return nil, apperr.Wrap(apperr.NotFound, "edit not found")
	}
	return e, nil
}

func (s *Service) Approve(ctx context.Context, u *auth.User, id int64, note string) (*Edit, error) {
	return s.decide(ctx, u, id, "merged", note)
}

func (s *Service) Reject(ctx context.Context, u *auth.User, id int64, note string) (*Edit, error) {
	return s.decide(ctx, u, id, "rejected", note)
}

func (s *Service) Withdraw(ctx context.Context, u *auth.User, id int64) (*Edit, error) {
	return s.decide(ctx, u, id, "withdrawn", "")
}

// decide moves a pending edit to merged, rejected or withdrawn. The row stays locked for the whole merge, so two
// approvers can never both merge: the second waits, then finds the edit decided. A merge that conflicts, or would be
// invalid on the current content, marks the edit stale instead.
func (s *Service) decide(ctx context.Context, u *auth.User, id int64, to, note string) (*Edit, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	me := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	e, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM content_edits WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !s.visible(st, u, e) {
		return nil, apperr.Wrap(apperr.NotFound, "edit not found")
	}
	t, _ := head(st, e.Training)
	switch {
	case to == "withdrawn" && e.Author != me:
		return nil, apperr.Wrap(apperr.Forbidden, "only the author can withdraw an edit")
	case to != "withdrawn" && (t == nil || !canReview(st.Platform, t, me, e.Author)):
		return nil, apperr.Wrap(apperr.Forbidden, "only the training's maintainers or an admin, other than the author, can review this edit")
	case e.Status != "pending":
		return nil, apperr.Wrap(apperr.Conflict, "this edit was already decided")
	}
	note = strings.TrimSpace(clean(note))
	if len(note) > maxNote {
		note = note[:maxNote]
	}
	status, mergeSHA, repo := to, "", s.Repo(e.Training)
	if repo == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "this training's repo is not available for edits")
	}
	if to == "merged" {
		mergeSHA, err = repo.Merge(ctx, e.Branch, fmt.Sprintf("crucible: merge edit %d %q by %s", e.ID, e.Title, e.Author), me)
		switch {
		case errors.Is(err, gitsync.ErrMergeConflict), errors.Is(err, gitsync.ErrMergeInvalid):
			status, note, mergeSHA = "stale", err.Error(), ""
		case err != nil:
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE content_edits SET status = $2, reviewer = $3, note = $4, merge_sha = $5, decided_at = now() WHERE id = $1`,
		e.ID, status, me, note, mergeSHA); err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, me, "content_edit."+status, e.Training, map[string]any{"edit": e.ID, "title": e.Title, "author": e.Author, "note": note}, mergeSHA); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if err := repo.DeleteBranch(ctx, e.Branch); err != nil && s.Log != nil {
		s.Log.Warn("deleting a decided edit branch failed", "branch", e.Branch, "err", err)
	}
	if status == "merged" && s.Resync != nil {
		if err := s.Resync(ctx); err != nil && s.Log != nil {
			s.Log.Warn("re-sync after a content merge failed; the poller will pick it up", "err", err)
		}
	}
	if me != e.Author {
		s.notify(ctx, notify.Event{Kind: notify.ContentEdit, To: []string{e.Author}, Subject: "Your content edit was " + status + ": " + e.Title,
			Text: strings.TrimSpace(fmt.Sprintf("%s marked your edit %s. %s", me, status, note)), Link: fmt.Sprintf("/edits/%d", e.ID)})
	}
	if status == "stale" {
		return nil, apperr.Wrap(apperr.Conflict, "this edit no longer applies to the current content; the author can redo it on the fresh version")
	}
	return s.Get(ctx, u, e.ID)
}
```

(pgx scans the `files` JSONB column straight into `map[string]string` and encodes the map on insert.)

`internal/edits/http.go`:

```go
package edits

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
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
	id := func(r *http.Request) (int64, error) {
		n, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			return 0, apperr.Wrap(apperr.NotFound, "edit not found")
		}
		return n, nil
	}
	r.Get("/api/content", func(w http.ResponseWriter, r *http.Request) { reply(w, s.Trainings(user(r)), nil) })
	r.Get("/api/content/{training}/files", func(w http.ResponseWriter, r *http.Request) {
		sha, files, err := s.Files(user(r), chi.URLParam(r, "training"))
		reply(w, map[string]any{"head_sha": sha, "files": files}, err)
	})
	r.Get("/api/content/{training}/file", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		body, err := s.File(user(r), chi.URLParam(r, "training"), p)
		reply(w, map[string]string{"path": p, "content": body}, err)
	})
	r.Get("/api/edits", func(w http.ResponseWriter, r *http.Request) { v, err := s.List(r.Context(), user(r)); reply(w, v, err) })
	r.Post("/api/edits", func(w http.ResponseWriter, r *http.Request) {
		var in NewEdit
		if err := httpx.Read(r, &in); err != nil { // 1 MiB cap for the whole edit
			httpx.Error(w, err)
			return
		}
		e, err := s.Create(r.Context(), user(r), in)
		reply(w, e, err)
	})
	r.Get("/api/edits/{id}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		e, err := s.Get(r.Context(), user(r), n)
		reply(w, e, err)
	})
	r.Post("/api/edits/{id}/{action:approve|reject|withdraw}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		var body struct {
			Note string `json:"note"`
		}
		if err := httpx.Read(r, &body); err != nil {
			httpx.Error(w, err)
			return
		}
		var e *Edit
		switch chi.URLParam(r, "action") {
		case "approve":
			e, err = s.Approve(r.Context(), user(r), n, body.Note)
		case "reject":
			e, err = s.Reject(r.Context(), user(r), n, body.Note)
		default:
			e, err = s.Withdraw(r.Context(), user(r), n)
		}
		reply(w, e, err)
	})
}
```

`cmd/crucible-api/main.go`: one cached `ContentRepo` per training, sharing the bot identity with the config `writer`:

```go
	var repoMu sync.Mutex
	repos := map[string]*gitsync.ContentRepo{}
	editsSvc := &edits.Service{DB: pool, State: syncer.Current, Notify: notifySvc, Resync: syncer.SyncOnce, Log: slog.Default(),
		Repo: func(id string) *gitsync.ContentRepo {
			st := syncer.Current()
			if st == nil || st.Platform == nil {
				return nil
			}
			ref, ok := st.Platform.Trainings[id]
			if !ok {
				return nil
			}
			repoMu.Lock()
			defer repoMu.Unlock()
			if r := repos[id]; r != nil && r.URL == ref.Repo && r.Branch == ref.Branch {
				return r
			}
			r := &gitsync.ContentRepo{URL: ref.Repo, Branch: ref.Branch, Dir: filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "edits", id),
				Name: writer.Name, Email: writer.Email}
			repos[id] = r
			return r
		}}
```

`internal/httpapi/server.go`: `Edits *edits.Service` in `Deps`, `if d.Edits != nil { d.Edits.Routes(r) }`, and `"can_edit_content": d.Edits != nil && d.Edits.CanUse(u.Email)` in `/api/me`.

The Dockerfile already configures the bot's credential helper for every repo on the git server (spec §2 "one bot credential"). `docs/runbooks/aws.md` must say the bot needs **push** access to the content repos too, not only the platform repo. Add one line under the existing git credentials section.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/edits/ ./internal/gitsync/ ./internal/httpapi/ -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations internal/notify internal/edits internal/httpapi cmd/crucible-api docs/runbooks/aws.md
git commit -m "feat(edits): in-app content edits reviewed by maintainers and merged by the bot

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Web: the editor, the review queue and the review page

**Files:**
- Create: `web/src/pages/Edits.tsx` (queue + "start an edit"), `web/src/pages/EditFiles.tsx` (editor), `web/src/pages/EditReview.tsx` (detail)
- Create: `web/src/components/DiffView.tsx`, `web/src/components/DiffView.test.tsx`
- Modify: `web/src/App.tsx` (routes `/edits`, `/edits/new`, `/edits/:id`), `web/src/components/Nav.tsx` (`Edits` for `me.can_edit_content`), `web/src/types.ts`, `web/src/theme/app.css`

**Interfaces:**
- Consumes: Task 11's routes and JSON.
- Produces (labels the e2e in Task 20 relies on):
  - On `/edits`: a select labelled `Training` (from `GET /api/content`) and a button `Start an edit` → `/edits/new?training=<id>`. The queue is a list of links named after each edit's title, with its status.
  - On `/edits/new`: one button per file named by its path; a textarea labelled `Content of <path>` for each opened file; a live preview (`data-testid="edit-preview"`) rendering the open `.md` file with `<Markdown>`; an input labelled `Title`; a button `Submit for review`. `?from=<id>` pre-fills title and files from that (stale) edit.
  - On `/edits/:id`: an `h1` with the title, `data-testid="edit-status"` with the status, per-file previews (Markdown for `.md`, `<pre>` otherwise), `<DiffView data-testid="diff">`, a textarea labelled `Review note`, buttons `Approve and merge` / `Reject` (only if `can_review`), `Withdraw` (only if `can_withdraw`), and `Redo on the current version` (author, status `stale`).

- [ ] **Step 1: Write the failing test**

`web/src/components/DiffView.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { DiffView } from './DiffView'

test('diff lines are marked added, removed or context', () => {
  const html = renderToStaticMarkup(<DiffView diff={'diff --git a/x.md b/x.md\n@@ -1,2 +1,2 @@\n # Intro\n-Hello.\n+Hello, smith.\n'} />)
  expect(html).toContain('data-testid="diff"')
  expect(html).toContain('class="diff-del"')
  expect(html).toContain('class="diff-add"')
  expect(html).toContain('class="diff-hunk"')
  expect(html).toContain('+Hello, smith.')
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/DiffView.test.tsx`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement**

`web/src/components/DiffView.tsx`:

```tsx
const kind = (l: string) =>
  l.startsWith('@@') ? 'diff-hunk' : l.startsWith('+') && !l.startsWith('+++') ? 'diff-add' : l.startsWith('-') && !l.startsWith('---') ? 'diff-del' : 'diff-ctx'

export function DiffView({ diff }: { diff: string }) {
  return (
    <pre className="diff" data-testid="diff" aria-label="Changes">
      {diff.split('\n').map((l, i) => <div key={i} className={kind(l)}>{l || ' '}</div>)}
    </pre>
  )
}
```

`web/src/types.ts`:

```ts
export type EditStatus = 'pending' | 'merged' | 'rejected' | 'withdrawn' | 'stale'
export type ContentEdit = { id: number; training: string; title: string; author: string; base_sha: string; branch: string; status: EditStatus
  reviewer?: string; note?: string; merge_sha?: string; created_at: string; decided_at?: string; files?: Record<string, string>; diff?: string
  can_review: boolean; can_withdraw: boolean }
```

Add `can_edit_content: boolean` to `Me`.

`web/src/pages/Edits.tsx`:
- `useFetch<{ id: string; title: string }[]>('/api/content')` fills the `Training` select;
- `Start an edit` navigates to `/edits/new?training=…`;
- `useFetch<ContentEdit[]>('/api/edits')` renders `<ul>`, each `<li><Link to={\`/edits/${e.id}\`}>{e.title}</Link> <span className={\`badge ${e.status}\`}>{e.status}</span> <span className="muted">{e.training} · {e.author}</span></li>`;
- empty state: "No edits yet."

`web/src/pages/EditFiles.tsx` (state: `files: Record<path, string>`, `open: string`, `title`, `head_sha`):
- Load `GET /api/content/${training}/files` for the file list and `head_sha`.
- Clicking a path loads `GET /api/content/${training}/file?path=…` into `files` (unless already loaded) and opens it.
- `<textarea aria-label={\`Content of ${open}\`} value={files[open]} onChange={…} rows={24} spellCheck={false} />`. Next to it, `<div data-testid="edit-preview">{open.endsWith('.md') ? <Markdown text={files[open]} /> : <pre>{files[open]}</pre>}</div>`.
- Submit: `api<ContentEdit>('/api/edits', { method: 'POST', json: { training, base_sha, title, files: changedOnly } })`, where `changedOnly` keeps the files whose text differs from what was loaded. Then navigate to `/edits/${e.id}`. Show API errors in `role="alert"`. The lint problems arrive in the message.
- With `?from=<id>`: load `GET /api/edits/${from}` and seed `title` and `files` from it, so a stale edit can be redone on the fresh `head_sha`.

`web/src/pages/EditReview.tsx`:
- Load `GET /api/edits/${id}`. Render the `h1` title, status (`data-testid="edit-status"`), author, reviewer, note, and the previews per file plus `<DiffView diff={e.diff ?? ''} />`.
- The buttons post `{ note }` to `/api/edits/${id}/approve|reject|withdraw`, then reload. On a 409, show the message (stale) and reload.
- "Redo on the current version" links to `/edits/new?training=${e.training}&from=${e.id}`.

`web/src/theme/app.css`:

```css
.diff { background: var(--term-bg); color: var(--term-fg); padding: 0.75rem; border-radius: 8px; overflow: auto; font-size: 0.85rem; }
.diff-add { color: var(--ok); }
.diff-del { color: var(--danger); }
.diff-hunk { color: var(--accent-2); }
.editor { display: grid; grid-template-columns: 220px 1fr 1fr; gap: 1rem; }
.editor textarea { width: 100%; font-family: 'JetBrains Mono', monospace; }
@media (max-width: 900px) { .editor { grid-template-columns: 1fr; } }
```

- [ ] **Step 4: Run the tests and the build**

Run: `cd web && npm test && npm run build && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): content editor with live preview, review queue, diff and maintainer review

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Bump a program's pinned content, with a diff summary first (spec §6)

**Files:**
- Modify: `internal/gitsync/syncer.go` (`Changes`, `Syncer.Changes`)
- Modify: `internal/gitsync/syncer_test.go`
- Modify: `internal/configapi/configapi.go` (`Service.Changes`, `ProgramView.RunningSHA/HeadSHA/PinnedRef`, `ProgramChanges`, `SetPin`, routes)
- Modify: `internal/configapi/configapi_test.go` (`fx.content`, `TestPinBump`)
- Modify: `cmd/crucible-api/main.go` (`cfgSvc.Changes = syncer.Changes`)
- Modify: `web/src/pages/ProgramSettings.tsx`, `web/src/types.ts`

**Interfaces:**
- Produces:
  ```go
  type Changes struct{ From, To string; Commits []string; Stat string } // json from to commits stat
  func (s *Syncer) Changes(ctx context.Context, training, from, to string) (*Changes, error)
  configapi.Service.Changes func(ctx context.Context, training, from, to string) (*gitsync.Changes, error)
  ProgramView.RunningSHA, HeadSHA, PinnedRef string // json running_sha head_sha pinned_ref
  type PinBody struct{ BaseSHA, Ref string }        // json base_sha ref; ref "" = track the branch head
  GET /api/teams/{team}/programs/{training}/changes → Changes (running → head)
  PUT /api/teams/{team}/programs/{training}/pin {base_sha, ref} → {sha}
  ```

- [ ] **Step 1: Write the failing tests**

In `internal/gitsync/syncer_test.go`, add a test that builds a content repo with two commits through the existing helpers, syncs a platform registering it (copy the setup another test in that file uses), and checks:

```go
	ch, err := s.Changes(ctx, "t1", first, second)
	if err != nil || len(ch.Commits) != 1 || !strings.HasSuffix(ch.Commits[0], "second change") || !strings.Contains(ch.Stat, "intro.md") {
		t.Fatalf("changes: %+v %v", ch, err)
	}
	if _, err := s.Changes(ctx, "t1", "HEAD~1", second); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("only full commit ids: %v", err)
	}
	if _, err := s.Changes(ctx, "nope", first, second); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training: %v", err)
	}
```

In `internal/configapi/configapi_test.go`, keep the content remote on the fixture (`content string` in `fx`, set from `bareFrom(t, "../../examples/forge-101", nil)`), and add:

```go
func TestPinBump(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	head := f.sync.Current().Heads["forge-101"]
	if _, err := f.s.SetPin(ctx, f.trainee, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: head}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees can't pin: %v", err)
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: head}); err != nil {
		t.Fatal(err)
	}
	// Someone pushes new content; the program stays on its pin.
	work := t.TempDir()
	sh(t, "", "clone", "-q", f.content, work)
	_ = os.WriteFile(filepath.Join(work, "modules/01-welcome/reading/how-we-work.md"), []byte("# How We Work\n\nNew words.\n"), 0o644)
	sh(t, work, "commit", "-qam", "new words")
	sh(t, work, "push", "-q", "origin", "HEAD:main")
	if err := f.sync.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	tv, _ := f.s.Team(f.leader, "forge")
	pv := tv.Programs[0]
	if pv.RunningSHA != head || pv.HeadSHA == head || pv.PinnedRef != head {
		t.Fatalf("pinned program view: %+v", pv)
	}
	ch, err := f.s.ProgramChanges(ctx, f.leader, "forge", "forge-101")
	if err != nil || len(ch.Commits) != 1 || !strings.Contains(ch.Commits[0], "new words") {
		t.Fatalf("diff summary: %+v %v", ch, err)
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: strings.Repeat("a", 40)}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("only validated versions can be pinned: %v", err)
	}
	if _, err := f.s.SetPin(ctx, f.leader, "forge", "forge-101", PinBody{BaseSHA: f.sha(), Ref: pv.HeadSHA}); err != nil {
		t.Fatal(err)
	}
	if got := f.sync.Current().ProgramSHAs["forge/forge-101"]; got != pv.HeadSHA {
		t.Fatalf("bumped: running %s", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/gitsync/ ./internal/configapi/ -run 'Changes|PinBump' -v`
Expected: compile errors.

- [ ] **Step 3: Implement**

`internal/gitsync/syncer.go`:

```go
// Changes summarises what moved in a training between two commits, shown to a manager before a pin bump (spec §6).
type Changes struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Commits []string `json:"commits"` // "abc1234 subject", newest first, at most 50
	Stat    string   `json:"stat"`    // git diff --stat
}

func (s *Syncer) Changes(ctx context.Context, training, from, to string) (*Changes, error) {
	st := s.Current()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing")
	}
	ref, ok := st.Platform.Trainings[training]
	if !ok {
		return nil, apperr.Wrap(apperr.NotFound, "unknown training")
	}
	if !shaRE.MatchString(from) || !shaRE.MatchString(to) {
		return nil, apperr.Wrap(apperr.Invalid, "from and to must be 40-character commit ids")
	}
	m := s.mirror(ref.Repo)
	out := &Changes{From: from, To: to, Commits: []string{}}
	if from == to {
		return out, nil
	}
	log, err := git(ctx, m.Dir, "log", "--format=%h %s", "-n", "50", from+".."+to)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(log, "\n") {
		if l != "" {
			out.Commits = append(out.Commits, l)
		}
	}
	if out.Stat, err = git(ctx, m.Dir, "diff", "--stat", from, to); err != nil {
		return nil, err
	}
	return out, nil
}
```

(Add the `strings` and `apperr` imports.)

`internal/configapi/configapi.go`:
- add `Changes func(ctx context.Context, training, from, to string) (*gitsync.Changes, error)` to `Service`;
- add `RunningSHA string \`json:"running_sha"\``, `HeadSHA string \`json:"head_sha"\`` and `PinnedRef string \`json:"pinned_ref"\`` to `ProgramView`, filled from `st.ProgramSHAs[id+"/"+tr]`, `st.Heads[tr]` and `p.PinnedRef`.

Add:

```go
type PinBody struct {
	BaseSHA string `json:"base_sha"`
	Ref     string `json:"ref"` // "" = track the branch head
}

var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ProgramChanges is the diff summary from the version a program runs to its training's branch head.
func (s *Service) ProgramChanges(ctx context.Context, u *auth.User, team, training string) (*gitsync.Changes, error) {
	st, t, err := s.team(team)
	if err != nil {
		return nil, err
	}
	if t.Programs[training] == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this team is not enrolled in that training")
	}
	if !(rbac.Checker{P: st.Platform}).Can(u.Email, rbac.ManageProgram, team, training, "") {
		return nil, apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can bump content")
	}
	return s.Changes(ctx, training, st.ProgramSHAs[team+"/"+training], st.Heads[training])
}

// SetPin pins a program to a validated content version, or back to tracking the branch head (spec §6).
func (s *Service) SetPin(ctx context.Context, u *auth.User, team, training string, b PinBody) (string, error) {
	st, t, err := s.team(team)
	if err != nil {
		return "", err
	}
	if t.Programs[training] == nil {
		return "", apperr.Wrap(apperr.NotFound, "this team is not enrolled in that training")
	}
	allow := func(p *config.Platform) error {
		if !(rbac.Checker{P: p}).Can(u.Email, rbac.ManageProgram, team, training, "") {
			return apperr.Wrap(apperr.Forbidden, "only the team leader, the program's managers or an admin can bump content")
		}
		return nil
	}
	if err := allow(st.Platform); err != nil {
		return "", err
	}
	ref := strings.ToLower(strings.TrimSpace(b.Ref))
	if ref != "" && (!commitRE.MatchString(ref) || st.Training(training, ref) == nil) {
		return "", apperr.Wrap(apperr.Invalid, "only a content version Crucible has validated can be pinned; pick the current head")
	}
	set := map[string]any{"pinned_ref": nil}
	action := "track the head of " + training + " in " + team
	if ref != "" {
		set["pinned_ref"], action = ref, "pin "+team+"/"+training+" to "+ref[:7]
	}
	rel := path.Join("teams", team, "programs", training+".yaml")
	return s.write(ctx, u, gitsync.Change{Action: action, Base: b.BaseSHA, Paths: []string{rel}, Allow: allow, Edit: edit(rel, set)},
		"program.pin", team+"/"+training, map[string]any{"from": st.ProgramSHAs[team+"/"+training], "to": ref})
}
```

Routes (`regexp` import):

```go
	r.Get("/api/teams/{team}/programs/{training}/changes", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ProgramChanges(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"))
		reply(w, v, err)
	})
	r.Put("/api/teams/{team}/programs/{training}/pin", func(w http.ResponseWriter, r *http.Request) {
		var b PinBody
		if err := httpx.Read(r, &b); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SetPin(r.Context(), user(r), chi.URLParam(r, "team"), chi.URLParam(r, "training"), b)
		sha(w, v, err)
	})
```

`cmd/crucible-api/main.go`: `cfgSvc := &configapi.Service{…, Changes: syncer.Changes}`. In the configapi test fixture, set `Changes: syncer.Changes` too.

Web `ProgramSettings.tsx`, a "Content version" section shown when `can_manage`:
- "Runs `<running_sha[:7]>`", plus "(pinned)" when `pinned_ref` is set, otherwise "(follows the branch head)".
- When `running_sha !== head_sha`: a button `Show changes` loads `/changes` and renders the commit list and a `<pre>` with the stat. Then `Pin to <head[:7]>` sends `PUT …/pin { base_sha: platform_sha, ref: head_sha }`.
- When pinned: a ghost button `Follow the branch head` sends `ref: ''`.
- Errors show inline. On success, reload the team.

Add `running_sha`, `head_sha` and `pinned_ref` to `ProgramConfig` in `types.ts`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gitsync/ ./internal/configapi/ -race && (cd web && npm test && npx tsc -b)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitsync internal/configapi cmd/crucible-api web/src
git commit -m "feat(config): bump a program's pinned content after a diff summary

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 14: `crucible preview <dir>` (spec §6, §8.5)

**Files:**
- Create: `internal/auth/preview.go`, `internal/auth/preview_test.go`
- Modify: `internal/httpapi/server.go` (`Deps.PreviewToken`; `/auth/preview` and a preview `/auth/login`)
- Modify: `cmd/crucible-api/main.go` (preview mode skips OIDC after `auth.PreviewAllowed`)
- Create: `cmd/crucible/preview.go`, `cmd/crucible/preview_test.go`
- Modify: `cmd/crucible/main.go` (usage + `preview` subcommand)
- Modify: `deploy/helm/test.sh` (the chart never renders `CRUCIBLE_PREVIEW`)
- Modify: `README.md` ("Previewing content")

**Interfaces:**
- Produces:
  ```go
  const auth.PreviewEmail = "preview@crucible.local"
  func auth.PreviewAllowed(publicURL, token string) error
  func (s auth.Store) PreviewLogin(token string, secure bool) http.HandlerFunc // GET /auth/preview?token=…
  // cmd/crucible
  crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]
  func snapshot(src, work string, free bool) (changed bool, err error) // work/src → work/git/content.git
  func writePlatform(dir, training string) error
  func fingerprint(dir string) (string, error)
  ```

- [ ] **Step 1: Write the failing tests**

`internal/auth/preview_test.go`:

```go
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crucible/internal/db/dbtest"
)

const goodToken = "0123456789abcdef0123456789abcdef"

func TestPreviewAllowed(t *testing.T) {
	for _, ok := range []string{"http://localhost:8090", "http://127.0.0.1:8090", "http://[::1]:8090"} {
		if err := PreviewAllowed(ok, goodToken); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for url, tok := range map[string]string{
		"https://crucible.example.com": goodToken, "http://crucible.example.com": goodToken, "http://10.0.0.5:8080": goodToken,
		"http://localhost:8090": "short-token", "not a url": goodToken,
	} {
		if err := PreviewAllowed(url, tok); err == nil {
			t.Errorf("%s with %q must be refused", url, tok)
		}
	}
}

func TestPreviewLogin(t *testing.T) {
	s := Store{DB: dbtest.New(t)}
	h := s.PreviewLogin(goodToken, false)
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/auth/preview?token=wrong", nil))
	if w.Code != http.StatusNotFound || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("wrong token: %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/auth/preview?token="+goodToken, nil))
	c := w.Result().Cookies()
	if w.Code != http.StatusFound || len(c) != 1 || c[0].Name != SessionCookie || !c[0].HttpOnly {
		t.Fatalf("login: %d %+v", w.Code, c)
	}
	u, err := s.UserBySession(context.Background(), c[0].Value)
	if err != nil || u == nil || !strings.EqualFold(u.Email, PreviewEmail) {
		t.Fatalf("session user %+v %v", u, err)
	}
}
```

`cmd/crucible/preview_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"crucible/internal/config"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSnapshotCopiesTheWorkingTree(t *testing.T) {
	src, work := t.TempDir(), t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		_ = os.MkdirAll(filepath.Join(src, filepath.Dir(rel)), 0o755)
		if err := os.WriteFile(filepath.Join(src, rel), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("training.yaml", "id: t1\ntitle: T1\nprogression: linear\nmodules: [m1]\n", 0o644)
	write("modules/m1/checks/c.sh", "#!/bin/sh\n", 0o755)
	write(".git/HEAD", "not copied\n", 0o644)
	changed, err := snapshot(src, work, false)
	if err != nil || !changed {
		t.Fatalf("first snapshot: %v %v", changed, err)
	}
	bare := filepath.Join(work, "git", "content.git")
	if mode := gitIn(t, bare, "ls-tree", "main", "modules/m1/checks/c.sh"); !strings.HasPrefix(mode, "100755") {
		t.Fatalf("exec bit kept: %q", mode)
	}
	if out := gitIn(t, bare, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, ".git/") {
		t.Fatalf("the author's .git is never copied: %s", out)
	}
	if changed, _ := snapshot(src, work, false); changed {
		t.Fatal("no change, no commit")
	}
	_ = os.Remove(filepath.Join(src, "modules/m1/checks/c.sh"))
	if changed, _ := snapshot(src, work, true); !changed {
		t.Fatal("a deleted file is a change")
	}
	if got := gitIn(t, bare, "show", "main:training.yaml"); !strings.Contains(got, "progression: free") {
		t.Fatalf("--free rewrites progression in the snapshot only: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "training.yaml")); !strings.Contains(string(b), "linear") {
		t.Fatal("the author's file is untouched")
	}
}

func TestPreviewPlatformIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := writePlatform(dir, "forge-101"); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Admins, ","), "preview@crucible.local") || len((p.Teams["preview"].Programs["forge-101"]).Enrolled) != 1 {
		t.Fatalf("preview platform %+v", p)
	}
	if p.Trainings["forge-101"].Repo != "file:///git/content.git" {
		t.Fatal("content comes from the snapshot repo")
	}
}

func TestFingerprintNoticesEdits(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0o644)
	a, _ := fingerprint(dir)
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("ab"), 0o644)
	b, _ := fingerprint(dir)
	if a == b {
		t.Fatal("an edit changes the fingerprint")
	}
}
```

Add one line to `deploy/helm/test.sh`, after the other `grep -q` guards on `$out`:

```bash
if grep -q CRUCIBLE_PREVIEW <<<"$out"; then echo "preview mode must never be rendered by the chart"; exit 1; fi
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/auth/ ./cmd/crucible/ -run 'Preview|Snapshot|Fingerprint' -v`
Expected: compile errors.

- [ ] **Step 3: Implement the API side**

`internal/auth/preview.go`:

```go
package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// PreviewEmail is the author crucible preview signs in as.
const PreviewEmail = "preview@crucible.local"

// PreviewAllowed refuses preview mode anywhere but an author's own machine: plain http on a loopback host and a long
// random token. crucible preview sets both; the Helm chart has no way to set CRUCIBLE_PREVIEW_TOKEN at all.
func PreviewAllowed(publicURL, token string) error {
	if len(token) < 32 {
		return errors.New("CRUCIBLE_PREVIEW_TOKEN must be at least 32 characters")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "http" {
		return errors.New("preview mode needs CRUCIBLE_PUBLIC_URL on http://localhost")
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	return errors.New("preview mode needs CRUCIBLE_PUBLIC_URL on http://localhost")
}

// PreviewLogin signs the browser in as the preview author when ?token= matches; anything else is a plain 404.
func (s Store) PreviewLogin(token string, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			http.NotFound(w, r)
			return
		}
		u, err := s.UpsertUser(r.Context(), "crucible-preview", PreviewEmail, "Preview author")
		if err != nil {
			http.Error(w, "preview sign-in failed", http.StatusInternalServerError)
			return
		}
		sess, err := s.CreateSession(r.Context(), u.ID, 12*time.Hour)
		if err != nil {
			http.Error(w, "preview sign-in failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
```

(Match the cookie attributes the OIDC callback uses. If it sets more, such as `MaxAge`, copy them.)

`internal/httpapi/server.go`: add `PreviewToken string` to `Deps`, and before the `/api/meta` route:

```go
	if d.PreviewToken != "" {
		r.Get("/auth/preview", d.Auth.PreviewLogin(d.PreviewToken, false))
		r.Get("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Crucible preview: open the sign-in link that `crucible preview` printed in your terminal.\n"))
		})
	}
```

`cmd/crucible-api/main.go`: read `previewToken := os.Getenv("CRUCIBLE_PREVIEW_TOKEN")` before the OIDC loop and wrap the loop:

```go
	var oidcH *auth.OIDC
	if previewToken != "" {
		if err := auth.PreviewAllowed(public, previewToken); err != nil {
			return fmt.Errorf("preview mode: %w", err)
		}
		slog.Warn("PREVIEW MODE: OIDC is off and /auth/preview signs in with the preview token. Only crucible preview sets this")
	} else {
		// the existing OIDC retry loop, unchanged, using must("OIDC_ISSUER") etc.
	}
```

and pass `PreviewToken: previewToken` in `httpapi.Deps`.

- [ ] **Step 4: Implement the CLI side**

`cmd/crucible/preview.go`:

```go
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"crucible/internal/agent"
	"crucible/internal/content"
	"crucible/internal/yamlx"
)

// preview runs the real Crucible image (API + Postgres) against a git snapshot of an author's working tree (spec §6):
// readings, quizzes and local labs with their setup scripts, re-synced within seconds of every save.
func preview(args []string) int {
	flags := flag.NewFlagSet("preview", flag.ExitOnError)
	port := flags.Int("port", 8090, "local port for the preview (bound to 127.0.0.1)")
	image := flags.String("image", "crucible:dev", "Crucible image (build it with: docker build -t crucible:dev .)")
	free := flags.Bool("free", false, "preview with progression: free so every module is open")
	_ = flags.Parse(args)
	if flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]")
		return 2
	}
	src, _ := filepath.Abs(flags.Arg(0))
	t, probs := content.Load(src)
	printProblems(probs)
	if t == nil {
		fmt.Fprintln(os.Stderr, "fix training.yaml first: preview needs the training id")
		return 1
	}
	if err := exec.Command("docker", "image", "inspect", *image).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "image %s not found; build it from the Crucible repo with: docker build -t %s .\n", *image, *image)
		return 1
	}
	work, err := os.MkdirTemp("", "crucible-preview-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(work)
	_ = os.Chmod(work, 0o755) // the API container runs as uid 10001 and must read the repos
	if _, err := snapshot(src, work, *free); err != nil {
		fmt.Fprintln(os.Stderr, "snapshot:", err)
		return 1
	}
	if err := writePlatform(filepath.Join(work, "platform-src"), t.ID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := bareFromDir(filepath.Join(work, "platform-src"), filepath.Join(work, "git", "platform.git")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := openUp(filepath.Join(work, "git")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	token, hook := randHex(24), randHex(24)
	compose := filepath.Join(work, "compose.yml")
	if err := os.WriteFile(compose, []byte(composeFile(*image, *port, token, hook, filepath.Join(work, "git"))), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dc := func(a ...string) error {
		cmd := exec.Command("docker", append([]string{"compose", "-f", compose}, a...)...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd.Run()
	}
	defer func() { _ = dc("down", "-v") }()
	fmt.Fprintln(os.Stderr, "starting Crucible preview…")
	if err := dc("up", "-d", "--wait"); err != nil {
		fmt.Fprintln(os.Stderr, "docker compose up failed:", err)
		return 1
	}
	base := fmt.Sprintf("http://localhost:%d", *port)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pairing, err := pairingToken(base, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preview sign-in failed:", err)
		return 1
	}
	go func() {
		c := &agent.Client{Server: base, Token: pairing, Exec: agent.Compose{Dir: filepath.Join(work, "labs")}, Log: slog.Default()}
		if err := c.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("preview agent stopped; local labs will not start", "err", err)
		}
	}()
	fmt.Printf("\n🔥 Crucible preview of %q is up.\n   Open: %s/auth/preview?token=%s\n   Edits are picked up within a few seconds. Ctrl-C stops it.\n\n", t.Title, base, token)
	last, _ := fingerprint(src)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "stopping the preview…")
			return 0
		case <-tick.C:
		}
		fp, err := fingerprint(src)
		if err != nil || fp == last {
			continue
		}
		last = fp
		_, probs := content.Load(src)
		printProblems(probs)
		if changed, err := snapshot(src, work, *free); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
		} else if changed {
			req, _ := http.NewRequest(http.MethodPost, base+"/api/git/hook", nil)
			req.Header.Set("X-Crucible-Secret", hook)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
			fmt.Fprintln(os.Stderr, "↻ synced", time.Now().Format("15:04:05"))
		}
	}
}

func printProblems(probs []content.Problem) {
	for _, p := range probs {
		fmt.Fprintln(os.Stderr, "✗", p)
	}
	if len(probs) == 0 {
		fmt.Fprintln(os.Stderr, "✓ lint clean")
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// snapshot copies src (minus .git) into work/src, commits any change and pushes it to work/git/content.git.
func snapshot(src, work string, free bool) (bool, error) {
	repo, bare := filepath.Join(work, "src"), filepath.Join(work, "git", "content.git")
	if _, err := os.Stat(bare); err != nil {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			return false, err
		}
		if err := run(repo, "init", "-q", "-b", "main"); err != nil {
			return false, err
		}
		if err := run("", "init", "-q", "--bare", "-b", "main", bare); err != nil {
			return false, err
		}
		if err := run(repo, "remote", "add", "origin", bare); err != nil {
			return false, err
		}
	}
	entries, _ := os.ReadDir(repo)
	for _, e := range entries {
		if e.Name() != ".git" {
			_ = os.RemoveAll(filepath.Join(repo, e.Name()))
		}
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if rel == "." || d.Type()&fs.ModeSymlink != 0 { // symlinks are a lint error anyway
			return nil
		}
		dst := filepath.Join(repo, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, fi.Mode().Perm())
	})
	if err != nil {
		return false, err
	}
	if free {
		if err := yamlx.Update(filepath.Join(repo, "training.yaml"), map[string]any{"progression": "free"}); err != nil {
			return false, err
		}
	}
	if err := run(repo, "add", "-A"); err != nil {
		return false, err
	}
	if run(repo, "diff", "--cached", "--quiet") == nil && run(repo, "rev-parse", "--verify", "HEAD") == nil {
		return false, nil
	}
	if err := run(repo, "-c", "user.name=crucible preview", "-c", "user.email=preview@crucible.local", "commit", "-q", "--allow-empty", "-m", "preview snapshot"); err != nil {
		return false, err
	}
	return true, run(repo, "push", "-q", "--force", "origin", "HEAD:main")
}

func run(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// writePlatform makes a one-team platform repo in which the preview author is an admin and enrolled in training.
func writePlatform(dir, training string) error {
	files := map[string]string{
		"platform.yaml":  "default_theme: forge\ncost_tiers: {auto_approve_usd: 0, tier1_usd: 1, tier2_usd: 2}\n",
		"admins.yaml":    "admins: [preview@crucible.local]\n",
		"trainings.yaml": fmt.Sprintf("trainings:\n  %s: {repo: file:///git/content.git, branch: main}\n", training),
		"teams/preview/team.yaml":                     "name: Preview\nleader: preview@crucible.local\n",
		"teams/preview/programs/" + training + ".yaml": "enrolled: [preview@crucible.local]\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func bareFromDir(dir, bare string) error {
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.name=crucible preview", "-c", "user.email=preview@crucible.local", "commit", "-q", "-m", "preview platform"}} {
		if err := run(dir, a...); err != nil {
			return err
		}
	}
	return run("", "clone", "-q", "--bare", dir, bare)
}

// openUp makes the bare repos readable by the container's user.
func openUp(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if d.IsDir() {
			mode = 0o755
		}
		return os.Chmod(p, mode)
	})
}

// fingerprint changes whenever any file's path, size, mode or mtime changes (cheap enough to poll every 2 s).
func fingerprint(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s|%d|%o|%d\n", p, fi.Size(), fi.Mode(), fi.ModTime().UnixNano())
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func composeFile(image string, port int, token, hook, gitDir string) string {
	return fmt.Sprintf(`name: crucible-preview
services:
  postgres:
    image: postgres:18-alpine
    environment: { POSTGRES_USER: crucible, POSTGRES_PASSWORD: crucible, POSTGRES_DB: crucible }
    tmpfs: [/var/lib/postgresql]
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U crucible"], interval: 2s, retries: 30 }
  api:
    image: %q
    environment:
      DATABASE_URL: postgres://crucible:crucible@postgres:5432/crucible?sslmode=disable
      CRUCIBLE_PLATFORM_REPO: file:///git/platform.git
      CRUCIBLE_PUBLIC_URL: http://localhost:%d
      CRUCIBLE_SYNC_INTERVAL: 30s
      CRUCIBLE_PREVIEW_TOKEN: %q
      CRUCIBLE_GIT_HOOK_SECRET: %q
      CRUCIBLE_QUIZ_SECRET: crucible-preview
    ports: ["127.0.0.1:%d:8080"]
    volumes: [%q]
    depends_on: { postgres: { condition: service_healthy } }
    healthcheck: { test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8080/healthz"], interval: 2s, retries: 60 }
`, image, port, token, hook, port, gitDir+":/git:ro")
}

// pairingToken signs in through /auth/preview and asks for an agent pairing token the normal way.
func pairingToken(base, token string) (string, error) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	resp, err := c.Get(base + "/auth/preview?token=" + token)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	resp, err = c.Post(base+"/api/agent/tokens", "application/json", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("pairing token: %s %s", resp.Status, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	return out.Token, json.NewDecoder(resp.Body).Decode(&out)
}
```

Notes for the implementer:
- `run` may already be defined in `cmd/crucible/main.go` or `main_test.go`. If it is, rename this one `gitRun`.
- The `/api/agent/tokens` POST passes the CSRF/origin guard, if one exists, because it is a same-origin JSON request from Go. If the API checks `Origin`, set `Origin: base` on the request.

`cmd/crucible/main.go`: add `crucible preview <content-dir> [--port 8090] [--image crucible:dev] [--free]` to `usage`, and `case "preview": os.Exit(preview(os.Args[2:]))`.

`README.md`, a new section:

````markdown
## Previewing content (`crucible preview`)

```bash
docker build -t crucible:dev .                   # once, from this repo
./bin/crucible preview path/to/your-training --free
```

It prints a sign-in link for http://localhost:8090. Readings, quizzes and laptop (`local`) labs run against your
working tree, including setup scripts for break-fix tasks. Save a file and the preview picks it up within seconds;
lint problems print in the terminal. `--free` opens every module. Ctrl-C removes everything.
````

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/auth/ ./cmd/crucible/ ./internal/httpapi/ -race && deploy/helm/test.sh`
Expected: PASS.

- [ ] **Step 6: Try it by hand (acceptance)**

```bash
make build && docker build -t crucible:dev .
./bin/crucible preview examples/forge-101 --free > /tmp/preview.log 2>&1 &
PREVIEW_PID=$!
for i in $(seq 60); do grep -q 'Open:' /tmp/preview.log && break; sleep 2; done
URL=$(grep -o 'http://localhost:8090/auth/preview?token=[0-9a-f]*' /tmp/preview.log)
curl -s -c /tmp/pv.jar -o /dev/null "$URL"
curl -s -b /tmp/pv.jar http://localhost:8090/api/programs | grep -q forge-101 && echo "programs ok"
printf '\nPreview edit %s.\n' "$(date +%s)" >> examples/forge-101/modules/01-welcome/reading/how-we-work.md
sleep 8
curl -s -b /tmp/pv.jar http://localhost:8090/api/programs/preview/forge-101/modules/01-welcome/reading/how-we-work | grep -q 'Preview edit' && echo "live reload ok"
git checkout examples/forge-101/modules/01-welcome/reading/how-we-work.md
kill -INT $PREVIEW_PID; wait $PREVIEW_PID
docker compose ls | grep -q crucible-preview && echo "LEFTOVER STACK" || echo "cleaned up"
```

Expected: `programs ok`, `live reload ok`, `cleaned up`. A local lab can be checked the same way through the browser at the printed link.

- [ ] **Step 7: Commit**

```bash
git add internal/auth internal/httpapi cmd deploy/helm/test.sh README.md
git commit -m "feat(cli): crucible preview runs the real image against your working tree

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: Forge Status: edits waiting, labs needing attention, program versions; stuck-destroy alerts

**Files:**
- Modify: `internal/configapi/configapi.go` (`PlatformView.PendingEdits/Attention/Programs`, filled in the `/api/admin/platform` handler)
- Modify: `internal/configapi/configapi_test.go`
- Modify: `internal/notify/notify.go` (`LabStuck`)
- Modify: `internal/labs/service.go` (`alertStuck`, called from `Sweep` when it retries a destroy)
- Create: `internal/labs/stuck_test.go`
- Modify: `web/src/pages/ForgeStatus.tsx`, `web/src/types.ts`

**Interfaces:**
- Produces:
  ```go
  type AttentionLab struct{ ID, Trainee, Team, Training, Module, State, Error string; Since time.Time } // json snake_case
  type ProgramPin struct{ Team, Training, Running, Head string }
  PlatformView.PendingEdits int; Attention []AttentionLab; Programs []ProgramPin // json pending_edits, attention, programs
  notify.LabStuck = "lab_stuck"
  ```

- [ ] **Step 1: Write the failing tests**

`internal/labs/stuck_test.go`:

```go
package labs

import (
	"context"
	"testing"
	"time"

	"crucible/internal/notify"
)

func TestStuckDestroyAlertsAdminsOnce(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	stick := func() {
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', destroyed_at = $2 WHERE id = $1`,
			v.ID, f.clk.Now().Add(-20*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	stick()
	f.s.Sweep(ctx)
	stick()
	f.s.Sweep(ctx)
	n := 0
	for _, ev := range f.notes.events {
		if ev.Kind == notify.LabStuck {
			n++
			if ev.To[0] != "admin@crucible.local" || ev.Link != "/admin" {
				t.Fatalf("to the admins, linking Forge Status: %+v", ev)
			}
		}
	}
	if n != 1 {
		t.Fatalf("one alert per stuck lab, got %d", n)
	}
}
```

(`Sweep` may need the clock to have moved past the retry threshold; with M6 that is 10 minutes for non-aws labs. The 20-minute `destroyed_at` covers it. If `Sweep`'s signature differs, call the method the River worker calls.)

Append to `internal/configapi/configapi_test.go`:

```go
func TestForgeStatusShowsAttention(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	var uid int64
	if err := f.s.DB.QueryRow(ctx, `INSERT INTO users (sub, email) VALUES ('s9', 'trainee@crucible.local') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state, error, created_at,
		last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s) VALUES
		('aaaaaaaaaaaa', $1, 'forge', 'forge-101', '02-first-lab', 'x', 'local', 'failed', 'compose up failed', now(), now(), 3600, 1800, 300, 0)`, uid); err != nil {
		t.Fatal(err)
	}
	v, err := f.s.Status(ctx, f.admin)
	if err != nil || len(v.Attention) != 1 || v.Attention[0].Trainee != "trainee@crucible.local" || v.PendingEdits != 0 || len(v.Programs) == 0 {
		t.Fatalf("status %+v %v", v, err)
	}
	if _, err := f.s.Status(ctx, f.leader); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("admins only: %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/labs/ ./internal/configapi/ -run 'Stuck|ForgeStatus' -v`
Expected: FAIL (no `LabStuck` event) and compile errors (`Status` undefined).

- [ ] **Step 3: Implement**

`notify.go`: `LabStuck Kind = "lab_stuck"` and `{LabStuck, "A lab is stuck while being destroyed (admins)"}`.

`internal/labs/service.go`:

```go
// alertStuck tells the admins, once per lab, that a destroy keeps failing (spec §8.1 "stuck destroys alert admins").
// ponytail: the NOT EXISTS guard is not atomic; the sweep holds its single-runner lock, so two sweeps never race here.
func (s *Service) alertStuck(ctx context.Context, inst *Instance) {
	tag, err := s.DB.Exec(ctx, `INSERT INTO lab_events (lab_id, kind, detail) SELECT $1, 'stuck_alerted', ''
		WHERE NOT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'stuck_alerted')`, inst.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return
	}
	since := inst.CreatedAt
	if inst.DestroyedAt != nil { // destroyed_at doubles as "destroying since"
		since = *inst.DestroyedAt
	}
	s.notify(ctx, notify.Event{Kind: notify.LabStuck, To: st.Platform.Admins, Subject: "A lab is stuck while being destroyed",
		Text: fmt.Sprintf("Lab %s (%s, %s/%s, %s) has been destroying since %s. Crucible keeps retrying; see Forge Status.",
			inst.ID, inst.Runtime, inst.Team, inst.Training, inst.Module, since.UTC().Format(time.RFC3339)), Link: "/admin"})
}
```

If `Instance` has no `DestroyedAt` field, add it to `instCols`/`scanInst` (`destroyed_at`), or read it with one extra query here. In `Sweep`, where a row in state `destroying` is picked for a retry, call `s.alertStuck(ctx, inst)` right before the retry.

`internal/configapi/configapi.go`:

```go
type AttentionLab struct {
	ID       string    `json:"id"`
	Trainee  string    `json:"trainee"`
	Team     string    `json:"team"`
	Training string    `json:"training"`
	Module   string    `json:"module"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"since"`
}

type ProgramPin struct {
	Team     string `json:"team"`
	Training string `json:"training"`
	Running  string `json:"running"`
	Head     string `json:"head"`
}
```

Add `PendingEdits int \`json:"pending_edits"\``, `Attention []AttentionLab \`json:"attention"\`` and `Programs []ProgramPin \`json:"programs"\`` to `PlatformView`. Add `Status`, which wraps `Platform`, fills those fields, and adds the audit log the handler adds today:

```go
// Status is the admin's Forge Status page: sync health, platform settings, recent privileged actions, edits waiting,
// labs that need a look, and the content version each program runs.
func (s *Service) Status(ctx context.Context, u *auth.User) (*PlatformView, error) {
	v, err := s.Platform(u)
	if err != nil {
		return nil, err
	}
	if v.Audit, err = audit.Recent(ctx, s.DB, 25); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM content_edits WHERE status = 'pending'`).Scan(&v.PendingEdits); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT li.id, u.email, li.team, li.training, li.module, li.state, li.error, coalesce(li.destroyed_at, li.created_at)
		FROM lab_instances li JOIN users u ON u.id = li.user_id
		WHERE (li.state = 'failed' AND li.created_at > now() - interval '24 hours')
		   OR (li.state = 'destroying' AND li.destroyed_at < now() - interval '10 minutes')
		ORDER BY 8 DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	if v.Attention, err = pgx.CollectRows(rows, pgx.RowToStructByPos[AttentionLab]); err != nil {
		return nil, err
	}
	if v.Attention == nil {
		v.Attention = []AttentionLab{}
	}
	st := s.State()
	v.Programs = []ProgramPin{}
	for _, team := range slices.Sorted(maps.Keys(st.Platform.Teams)) {
		for _, tr := range slices.Sorted(maps.Keys(st.Platform.Teams[team].Programs)) {
			v.Programs = append(v.Programs, ProgramPin{Team: team, Training: tr, Running: st.ProgramSHAs[team+"/"+tr], Head: st.Heads[tr]})
		}
	}
	return v, nil
}
```

The `/api/admin/platform` handler becomes `v, err := s.Status(r.Context(), user(r)); reply(w, v, err)`. Add `"github.com/jackc/pgx/v5"` to the imports.

`web/src/pages/ForgeStatus.tsx`, after the kill switch:

```tsx
      <h2>Needs attention</h2>
      {p.pending_edits > 0 && <p><Link to="/edits">{p.pending_edits} content edit{p.pending_edits === 1 ? '' : 's'} waiting for review</Link></p>}
      {p.attention.length === 0 ? <p className="pass">No failed or stuck labs.</p> : (
        <table className="grid">
          <thead><tr><th>Lab</th><th>Trainee</th><th>Program</th><th>State</th><th>Since</th><th>Error</th></tr></thead>
          <tbody>{p.attention.map((a) => (
            <tr key={a.id}><td><code>{a.id}</code></td><td>{a.trainee}</td><td>{a.team}/{a.training} · {a.module}</td>
              <td className={a.state === 'failed' ? 'error' : 'warn'}>{a.state}</td><td>{new Date(a.since).toLocaleString()}</td><td>{a.error}</td></tr>
          ))}</tbody>
        </table>
      )}
      <h2>Program versions</h2>
      <table className="grid">
        <thead><tr><th>Program</th><th>Runs</th><th>Branch head</th></tr></thead>
        <tbody>{p.programs.map((g) => (
          <tr key={g.team + g.training}><td>{g.team}/{g.training}</td><td><code>{g.running.slice(0, 7)}</code></td>
            <td><code>{g.head.slice(0, 7)}</code>{g.running !== g.head && <span className="badge warn"> behind</span>}</td></tr>
        ))}</tbody>
      </table>
```

Add the fields to `PlatformView` in `types.ts`, and import `Link`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/labs/ ./internal/configapi/ -race && (cd web && npm test && npx tsc -b)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify internal/labs internal/configapi web/src
git commit -m "feat(status): Forge Status shows waiting edits, failing labs and program versions; stuck destroys alert admins

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 16: Navigation from spec §12: Trainings catalog, My labs, nav order

**Files:**
- Create: `internal/learn/catalog.go` (+ a test in `internal/learn/catalog_test.go`)
- Create: `internal/labs/mine.go` (+ a test in `internal/labs/mine_test.go`)
- Modify: `internal/learn/http.go` (`GET /api/catalog`), `internal/labs/http.go` (`GET /api/labs`)
- Create: `web/src/pages/Trainings.tsx`, `web/src/pages/Labs.tsx`
- Modify: `web/src/App.tsx`, `web/src/components/Nav.tsx`, `web/src/types.ts`

**Interfaces:**
- Produces:
  ```go
  type CatalogEntry struct{ ID, Title, Description string; EstimatedHours float64; Modules int; Enrolled []TeamRef; Available bool }
  // json id title description estimated_hours modules enrolled available
  type TeamRef struct{ ID, Name string } // json id name
  func (s *learn.Service) Catalog(u *auth.User) ([]CatalogEntry, error)
  type MyLab struct{ ID, Team, Training, Module, Title, Runtime string; State State; CreatedAt time.Time; EndsAt *time.Time; Link string }
  func (s *labs.Service) Mine(ctx context.Context, u *auth.User) ([]MyLab, error) // active first, then newest, 20 max
  ```
  Nav order: **Hearth · Trainings · Labs · Anvil · Ledger · Forge Status · Team**, then Approvals · Mentor · Edits · Connect your laptop · Settings. Each link appears only for people who can use it, using the same `me` flags as today.

- [ ] **Step 1: Write the failing tests**

`internal/learn/catalog_test.go`:

```go
package learn

import "testing"

func TestCatalogListsEveryTrainingAndYourTeams(t *testing.T) {
	s, u, leader := fixture(t)
	s.State().Heads = map[string]string{"forge-101": "abc"}
	cat, err := s.Catalog(u)
	if err != nil || len(cat) == 0 {
		t.Fatalf("catalog %v %v", cat, err)
	}
	var f101 *CatalogEntry
	for i := range cat {
		if cat[i].ID == "forge-101" {
			f101 = &cat[i]
		}
	}
	if f101 == nil || !f101.Available || f101.Modules != 3 || len(f101.Enrolled) != 1 || f101.Enrolled[0].ID != "forge" {
		t.Fatalf("forge-101 entry %+v", f101)
	}
	cat, _ = s.Catalog(leader)
	for _, c := range cat {
		if len(c.Enrolled) != 0 {
			t.Fatalf("the leader is enrolled in nothing: %+v", c)
		}
	}
}
```

`internal/labs/mine_test.go`:

```go
package labs

import (
	"context"
	"testing"
)

func TestMineListsOnlyMyLabsActiveFirst(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	mine, err := f.s.Mine(ctx, f.u)
	if err != nil || len(mine) != 1 || mine[0].ID != v.ID || mine[0].Title == "" || mine[0].Link != "/p/forge/forge-101/m/02-first-lab/lab" {
		t.Fatalf("mine %+v %v", mine, err)
	}
	if other, _ := f.s.Mine(ctx, f.leader); len(other) != 0 {
		t.Fatal("nobody sees someone else's labs here")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/learn/ ./internal/labs/ -run 'Catalog|Mine' -v`
Expected: compile errors.

- [ ] **Step 3: Implement**

`internal/learn/catalog.go`:

```go
package learn

import (
	"maps"
	"slices"
	"strings"

	"crucible/internal/auth"
	"crucible/internal/rbac"
)

type TeamRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CatalogEntry struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	EstimatedHours float64   `json:"estimated_hours"`
	Modules        int       `json:"modules"`
	Enrolled       []TeamRef `json:"enrolled"` // the teams you take it through
	Available      bool      `json:"available"`
}

// Catalog is the global training catalog (spec §2 "Trainings scope: global catalog"): titles and descriptions from each
// training's branch head, plus the teams the user is enrolled through. No content, answers or progress.
func (s *Service) Catalog(u *auth.User) ([]CatalogEntry, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	mine := map[string][]TeamRef{}
	for _, e := range (rbac.Checker{P: st.Platform}).Enrollments(u.Email) {
		mine[e.Program.Training] = append(mine[e.Program.Training], TeamRef{ID: e.Team.ID, Name: e.Team.Name})
	}
	out := []CatalogEntry{}
	for _, id := range slices.Sorted(maps.Keys(st.Platform.Trainings)) {
		c := CatalogEntry{ID: id, Title: id, Enrolled: mine[id]}
		if c.Enrolled == nil {
			c.Enrolled = []TeamRef{}
		}
		if t := st.Training(id, st.Heads[id]); t != nil {
			c.Title, c.Description, c.EstimatedHours, c.Modules, c.Available = t.Title, t.Description, t.EstimatedHours, len(t.Modules), true
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b CatalogEntry) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}
```

`internal/labs/mine.go`:

```go
package labs

import (
	"context"
	"time"

	"crucible/internal/auth"
)

type MyLab struct {
	ID        string     `json:"id"`
	Team      string     `json:"team"`
	Training  string     `json:"training"`
	Module    string     `json:"module"`
	Title     string     `json:"title"`
	Runtime   string     `json:"runtime"`
	State     State      `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	Link      string     `json:"link"`
}

// Mine lists the user's labs for the Labs page: active ones first, then the newest, at most 20.
func (s *Service) Mine(ctx context.Context, u *auth.User) ([]MyLab, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE user_id = $1
		ORDER BY (state IN ('pending_approval', 'provisioning', 'ready', 'destroying')) DESC, created_at DESC LIMIT 20`, u.ID)
	if err != nil {
		return nil, err
	}
	insts, err := collectInst(rows)
	if err != nil {
		return nil, err
	}
	out := []MyLab{}
	for _, in := range insts {
		m := MyLab{ID: in.ID, Team: in.Team, Training: in.Training, Module: in.Module, Title: in.Module, Runtime: in.Runtime,
			State: in.State, CreatedAt: in.CreatedAt, EndsAt: in.EndsAt, Link: labLink(in)}
		if t := s.trainingOf(in); t != nil {
			if mod := t.Module(in.Module); mod != nil {
				m.Title = mod.Title
			}
		}
		out = append(out, m)
	}
	return out, nil
}
```

Routes: in `learn/http.go`, `r.Get("/api/catalog", func(w, r) { c, err := s.Catalog(auth.UserFrom(r.Context())); reply(w, c, err) })`. In `labs/http.go`, `r.Get("/api/labs", …)` calling `Mine` and writing JSON. Register it before `/api/labs/{id}`; chi matches both correctly either way.

Web:
- `Trainings.tsx`: heading `Trainings`. Cards for every catalog entry (title, description, "≈ N h" when `estimated_hours > 0`, "N modules"). For each enrolled team, a link "Open (Team Name)" to `/p/<team>/<id>`. If none, a muted "Ask your team leader to enrol you." **Card titles are not links**, so `getByRole('link', { name: /Forge 101/ })` on the Hearth stays unique.
- `Labs.tsx`: heading `Labs`. A table of `MyLab` with title, training, state badge, started, and ends (for active labs), plus an "Open" link to `link`. Empty state: "No labs yet."
- `App.tsx`: routes `/trainings` and `/labs`.
- `Nav.tsx`, in the order from **Interfaces**:

```tsx
      <NavLink to="/" end>Hearth</NavLink>
      <NavLink to="/trainings">Trainings</NavLink>
      <NavLink to="/labs">Labs</NavLink>
      {me.can_score && <NavLink to="/anvil">Anvil</NavLink>}
      {me.can_view_spend && <NavLink to="/ledger">Ledger</NavLink>}
      {me.is_admin && <NavLink to="/admin">Forge Status</NavLink>}
      {(me.teams.length > 0 || me.is_admin) && <NavLink to="/teams">Team</NavLink>}
      {me.can_approve && <NavLink to="/approvals">Approvals</NavLink>}
      {me.is_mentor && <NavLink to="/mentor">Mentor</NavLink>}
      {me.can_edit_content && <NavLink to="/edits">Edits</NavLink>}
      <NavLink to="/connect">Connect your laptop</NavLink>
      <NavLink to="/settings">Settings</NavLink>
```

Then run `grep -n "getByRole('link'" e2e/tests/*.ts`. No existing spec may match two of these links. In particular, "Labs" must not match a `{ name: 'Lab' }` lookup that lacks `exact: true`; the existing ones are scoped to a module's test id.

- [ ] **Step 4: Run the tests and the build**

Run: `go test ./internal/learn/ ./internal/labs/ -race && (cd web && npm test && npm run build && npm run lint)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/learn internal/labs web
git commit -m "feat(web): Trainings catalog, Labs page and the spec's navigation order

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 17: Reading: syntax highlighting, mermaid, callouts, reading progress

**Files:**
- Modify: `web/package.json`, `web/package-lock.json` (`rehype-highlight`, `mermaid`)
- Modify: `web/src/components/Markdown.tsx`
- Create: `web/src/components/Mermaid.tsx`, `web/src/lib/callouts.ts`, `web/src/components/Markdown.test.tsx`
- Modify: `web/src/pages/Reading.tsx`, `web/src/theme/app.css`
- Modify: `examples/forge-101/modules/01-welcome/reading/how-we-work.md` (one callout and one fenced code block, so the e2e path exercises them; no mermaid, which keeps local-check fast)

**Interfaces:**
- Produces: `remarkCallouts()` (a remark plugin, no new dependency). Callout blockquotes get `class="callout callout-<type>"` and `data-callout="<TYPE>"`. Fenced `mermaid` blocks render through `<Mermaid source>`, which lazy-imports `mermaid` with `securityLevel: 'strict'` and falls back to the source in a `<pre>`.

- [ ] **Step 1: Install and write the failing test**

Run: `cd web && npm install rehype-highlight@^7 mermaid@^11`

`web/src/components/Markdown.test.tsx`:

```tsx
import { expect, test } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { Markdown } from './Markdown'

test('callouts, highlighted code and mermaid placeholders', () => {
  const md = '> [!WARNING]\n> Hot metal.\n\n```go\nfunc main() {}\n```\n\n```mermaid\ngraph TD; A-->B\n```\n'
  const html = renderToStaticMarkup(<Markdown text={md} />)
  expect(html).toContain('class="callout callout-warning"')
  expect(html).toContain('data-callout="WARNING"')
  expect(html).toContain('Hot metal.')
  expect(html).not.toContain('[!WARNING]')
  expect(html).toContain('hljs-keyword')          // highlighted
  expect(html).toContain('graph TD; A--&gt;B')    // mermaid source shown until the diagram renders (client only)
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/components/Markdown.test.tsx`
Expected: FAIL (no callout class, no `hljs-` spans).

- [ ] **Step 3: Implement**

`web/src/lib/callouts.ts`:

```ts
type MdNode = { type: string; value?: string; children?: MdNode[]; data?: { hProperties?: Record<string, string> } }
const CALLOUT = /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/

// remarkCallouts turns GitHub-style `> [!NOTE]` blockquotes into styled callouts (spec §7).
export function remarkCallouts() {
  return (tree: MdNode) => {
    const walk = (n: MdNode) => {
      if (n.type === 'blockquote') {
        const p = n.children?.[0]
        const t = p?.type === 'paragraph' ? p.children?.[0] : undefined
        const m = t?.type === 'text' ? CALLOUT.exec(t.value ?? '') : null
        if (m && t) {
          t.value = (t.value ?? '').slice(m[0].length)
          n.data = { hProperties: { className: `callout callout-${m[1].toLowerCase()}`, 'data-callout': m[1] } }
        }
      }
      n.children?.forEach(walk)
    }
    walk(tree)
  }
}
```

`web/src/components/Mermaid.tsx`:

```tsx
import { useEffect, useId, useState } from 'react'

// Mermaid renders a diagram on the client; mermaid is imported only when a page has one.
export function Mermaid({ source }: { source: string }) {
  const id = 'mmd' + useId().replace(/[^a-zA-Z0-9]/g, '')
  const [svg, setSvg] = useState<string>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let live = true
    import('mermaid')
      .then(async ({ default: m }) => {
        const dark = document.documentElement.dataset.theme !== 'anvil'
        m.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark ? 'dark' : 'default' })
        const { svg } = await m.render(id, source)
        if (live) setSvg(svg)
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
    }
  }, [id, source])
  if (!svg) return <pre className={failed ? 'mermaid-failed' : 'mermaid-pending'}>{source}</pre>
  return <div className="mermaid" role="img" aria-label="Diagram" dangerouslySetInnerHTML={{ __html: svg }} />
}
```

`web/src/components/Markdown.tsx`:

```tsx
import ReactMarkdown, { defaultUrlTransform, type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { remarkCallouts } from '../lib/callouts'
import { Mermaid } from './Mermaid'

const components: Components = {
  code({ className, children, ...rest }) {
    if (className?.includes('language-mermaid')) return <Mermaid source={String(children).trim()} />
    return <code className={className} {...rest}>{children}</code>
  },
}

// assetBase rewrites `assets/x.png` links to the training's asset endpoint.
export function Markdown({ text, assetBase }: { text: string; assetBase?: string }) {
  return (
    <div className="prose">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkCallouts]}
        rehypePlugins={[[rehypeHighlight, { plainText: ['mermaid'] }]]}
        components={components}
        urlTransform={(url) => (assetBase && url.startsWith('assets/') ? `${assetBase}/${url.slice('assets/'.length)}` : defaultUrlTransform(url))}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}
```

`web/src/pages/Reading.tsx`, the reading progress bar (scroll tracking):

```tsx
  const [depth, setDepth] = useState(0)
  useEffect(() => {
    const on = () => {
      const el = document.documentElement
      const max = el.scrollHeight - el.clientHeight
      setDepth(max <= 0 ? 100 : Math.min(100, Math.round((el.scrollTop / max) * 100)))
    }
    on()
    window.addEventListener('scroll', on, { passive: true })
    return () => window.removeEventListener('scroll', on)
  }, [data])
```

Render `<div className="reading-progress"><MoltenBar percent={depth} label="Reading progress" caption={`${depth}% read`} /></div>` as the first child of the `<article>`. Place these hooks **before** the early returns.

`web/src/theme/app.css`:

```css
.reading-progress { position: sticky; top: 0; z-index: 5; background: var(--bg); padding-top: 0.25rem; }
.callout { border-left: 4px solid var(--accent); background: var(--surface); padding: 0.5rem 1rem; border-radius: 6px; color: var(--text); }
.callout::before { content: attr(data-callout); display: block; font-weight: 700; letter-spacing: 0.05em; color: var(--accent); }
.callout-warning, .callout-caution { border-color: var(--danger); }
.callout-warning::before, .callout-caution::before { color: var(--danger); }
.callout-tip::before { color: var(--ok); }
.hljs-keyword, .hljs-selector-tag, .hljs-built_in { color: var(--accent); }
.hljs-string, .hljs-attr { color: var(--ok); }
.hljs-number, .hljs-literal, .hljs-title { color: var(--accent-2); }
.hljs-comment { color: var(--muted); font-style: italic; }
.mermaid svg { max-width: 100%; height: auto; }
```

In `how-we-work.md`, append:

````markdown
> [!TIP]
> Small changes are easier to review than big ones.

```bash
git switch -c my-first-change
```
````

- [ ] **Step 4: Run the tests and the build; check the bundle**

Run: `cd web && npm test && npm run build && npm run lint && ls -la dist/assets | sort -k5 -n | tail -5`
Expected: PASS. Mermaid lands in its own lazily loaded chunk(s), and the main `index-*.js` grows only by `rehype-highlight`'s core and common languages, under ~150 KiB gzip extra. If the main chunk swallowed mermaid, the `import('mermaid')` is not dynamic: fix that.

- [ ] **Step 5: Commit**

```bash
git add web examples/forge-101
git commit -m "feat(web): syntax highlighting, mermaid diagrams, callouts and reading progress

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 18: Accessibility and motion polish (spec §12)

**Files:**
- Modify: `web/src/pages/Lab.tsx` (keyboard terminal tabs; live region for check results; spark burst respects calm)
- Create: `web/src/lib/contrast.ts`, `web/src/lib/contrast.test.ts`
- Modify: `web/src/theme/tokens.css` (only if the contrast test finds a failing pair), `web/src/theme/app.css` (focus rings)

**Interfaces:**
- Produces:
  - `contrast(fg: string, bg: string): number`, the WCAG 2.x ratio for `#rgb`/`#rrggbb`.
  - Terminal tabs follow the WAI-ARIA tabs pattern: `role="tab"`, `aria-selected`, roving `tabIndex`, and ArrowLeft/ArrowRight/Home/End to move between tabs.
  - Check feedback sits inside a `role="status" aria-live="polite"` region.

- [ ] **Step 1: Write the failing test**

`web/src/lib/contrast.test.ts`:

```ts
import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'vitest'
import { contrast } from './contrast'

const css = readFileSync(new URL('../theme/tokens.css', import.meta.url), 'utf8')
function tokens(theme: string): Record<string, string> {
  const block = css.split('}').find((b) => b.includes(`[data-theme='${theme}']`))!
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{3,6})/g)].map((m) => [m[1], m[2]]))
}

describe('theme contrast (spec §12: contrast checked per theme; High Contrast = WCAG AAA)', () => {
  for (const theme of ['forge', 'anvil', 'quench', 'contrast']) {
    const t = tokens(theme)
    const min = theme === 'contrast' ? 7 : 4.5
    for (const [fg, bg] of [['text', 'bg'], ['text', 'surface'], ['muted', 'bg'], ['muted', 'surface'], ['accent', 'bg'], ['danger', 'surface'], ['ok', 'surface']]) {
      test(`${theme}: ${fg} on ${bg} ≥ ${min}`, () => {
        expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(min)
      })
    }
  }
  test('the formula', () => {
    expect(contrast('#000', '#fff')).toBeCloseTo(21, 0)
    expect(contrast('#777', '#fff')).toBeCloseTo(4.48, 1)
  })
})
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd web && npx vitest run src/lib/contrast.test.ts`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement**

`web/src/lib/contrast.ts`:

```ts
function lum(hex: string): number {
  let h = hex.replace('#', '')
  if (h.length === 3) h = h.split('').map((c) => c + c).join('')
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

// contrast is the WCAG 2.x contrast ratio between two colours.
export function contrast(fg: string, bg: string): number {
  const [a, b] = [lum(fg), lum(bg)].sort((x, y) => y - x)
  return (a + 0.05) / (b + 0.05)
}
```

Run the test. For every failing pair, adjust **only the failing token** in `tokens.css`, moving it the smallest step that passes and keeping its hue. Note the old and new value in the commit message.

`web/src/theme/app.css`:

```css
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
[data-theme='contrast'] :focus-visible { outline: 3px solid var(--accent-2); }
```

`web/src/pages/Lab.tsx`. Make the terminal tablist keyboard-navigable (adapt the names to the existing `tabs`/`active`/`setActive` state):

```tsx
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([])
  const onTabKey = (e: React.KeyboardEvent, i: number) => {
    const n = tabs.length
    const next = e.key === 'ArrowRight' ? (i + 1) % n : e.key === 'ArrowLeft' ? (i - 1 + n) % n : e.key === 'Home' ? 0 : e.key === 'End' ? n - 1 : -1
    if (next < 0) return
    e.preventDefault()
    setActive(tabs[next].key)
    tabRefs.current[next]?.focus()
  }
```

Each tab button gets `role="tab"`, `aria-selected={active === t.key}`, `tabIndex={active === t.key ? 0 : -1}`, `ref={(el) => { tabRefs.current[i] = el }}`, `onKeyDown={(e) => onTabKey(e, i)}` and `aria-controls` pointing at its panel id. Each terminal panel gets `role="tabpanel"` and `id`. Wrap the check feedback (pass/fail text and output) in `<div role="status" aria-live="polite">`. If `SparkBurst` renders without checking `useCalm()`, make it return `null` under calm. The static "Passed" text is the essential state change.

The forge-101 e2e clicks tabs with `getByRole('tab', { name, exact: true })`, so names must not change.

- [ ] **Step 4: Run the tests, the build, and the existing e2e locally**

Run: `cd web && npm test && npm run build && npm run lint`, then `KEYCLOAK_PORT=8082 make local-check`.
Expected: PASS and `🔥 Local check passed. The forge holds.`

- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "feat(web): keyboard terminal tabs, live check results, focus rings, per-theme contrast tests

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 19: Bootstrap admin from Helm, revocable pairing tokens (spec §5.1)

**Files:**
- Modify: `internal/configapi/configapi.go` (`SeedAdmin`), `internal/configapi/configapi_test.go`
- Modify: `cmd/crucible-api/main.go` (`CRUCIBLE_BOOTSTRAP_ADMIN`)
- Modify: `deploy/helm/crucible/values.yaml` (`bootstrapAdmin: ""`), `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/test.sh`
- Modify: `deploy/aws/main/{variables.tf,bootstrap.sh.tftpl,main.tftest.hcl}` (`bootstrap_admin` → `--set bootstrapAdmin=…`; follow how M3 wired `git_bot_name`)
- Modify: `internal/auth/store.go`, `internal/auth/store_test.go` (`RevokeAgentTokens`)
- Modify: `internal/agenthub/hub.go` (`Drop(userID)`, if no such method exists), `internal/httpapi/server.go` (`DELETE /api/agent/tokens`)
- Modify: `web/src/pages/Connect.tsx`, `docs/runbooks/aws.md`, `docs/runbooks/cognito.md`

**Interfaces:**
- Produces:
  ```go
  func configapi.SeedAdmin(ctx context.Context, w *gitsync.Writer, email string) (seeded bool, err error)
  func (s auth.Store) RevokeAgentTokens(ctx context.Context, userID int64) error
  func (h *agenthub.Hub) Drop(userID int64)  // closes the user's agent connection, if any
  DELETE /api/agent/tokens → 204
  ```

- [ ] **Step 1: Write the failing tests**

`internal/configapi/configapi_test.go`:

```go
func TestSeedAdminOnlyIntoAnEmptyAdminsFile(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if ok, err := SeedAdmin(ctx, f.s.Writer, "Boss@Example.com"); err != nil || ok {
		t.Fatalf("the examples platform already has admins: %v %v", ok, err)
	}
	f.push(t, map[string]string{"admins.yaml": "admins: []\n"})
	if ok, err := SeedAdmin(ctx, f.s.Writer, "Boss@Example.com"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if got := sh(t, "", "--git-dir", f.remote, "show", "main:admins.yaml"); !strings.Contains(got, "boss@example.com") {
		t.Fatalf("admins.yaml:\n%s", got)
	}
	if ok, _ := SeedAdmin(ctx, f.s.Writer, "other@example.com"); ok {
		t.Fatal("never twice")
	}
	if _, err := SeedAdmin(ctx, f.s.Writer, "not an email"); err == nil {
		t.Fatal("must look like an email")
	}
}
```

`internal/auth/store_test.go`:

```go
func TestRevokeAgentTokens(t *testing.T) {
	ctx := context.Background()
	s := Store{DB: dbtest.New(t)}
	u, _ := s.UpsertUser(ctx, "s1", "a@x", "A")
	tok, err := s.CreateAgentToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentTokens(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserByAgentToken(ctx, tok); got != nil {
		t.Fatal("a revoked token no longer pairs")
	}
}
```

Add to `deploy/helm/test.sh`:

```bash
if grep -q CRUCIBLE_BOOTSTRAP_ADMIN <<<"$out"; then echo "bootstrap admin is opt-in"; exit 1; fi
boot=$(helm template t "$chart" $base --set bootstrapAdmin=boss@example.com)
grep -q 'name: CRUCIBLE_BOOTSTRAP_ADMIN, value: "boss@example.com"' <<<"$boot" || { echo "missing: bootstrap admin env"; exit 1; }
```

(Put the first line after `$out` is rendered and the rest after `$base` is defined.)

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/configapi/ ./internal/auth/ -run 'SeedAdmin|Revoke' -v; deploy/helm/test.sh`
Expected: compile errors, then `missing: bootstrap admin env`.

- [ ] **Step 3: Implement**

`internal/configapi/configapi.go`:

```go
var errSeeded = errors.New("admins already set")

// SeedAdmin writes the bootstrap admin into admins.yaml when the platform repo names no admin yet (spec §5.1: the Helm
// value exists only to seed admins.yaml on first start). A repo that already has an admin is never touched.
func SeedAdmin(ctx context.Context, w *gitsync.Writer, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if err := checkEmail("bootstrap admin", email); err != nil {
		return false, err
	}
	_, changed, err := w.Apply(ctx, gitsync.Change{Action: "seed the bootstrap admin", Actor: email, Paths: []string{"admins.yaml"},
		Allow: func(p *config.Platform) error {
			if len(p.Admins) > 0 {
				return errSeeded
			}
			return nil
		},
		Edit: edit("admins.yaml", map[string]any{"admins": []string{email}})})
	if errors.Is(err, errSeeded) {
		return false, nil
	}
	return changed, err
}
```

`cmd/crucible-api/main.go`, after the writer is built and the first sync ran:

```go
	if email := os.Getenv("CRUCIBLE_BOOTSTRAP_ADMIN"); email != "" {
		if st := syncer.Current(); st != nil && st.Platform != nil && len(st.Platform.Admins) == 0 {
			if ok, err := configapi.SeedAdmin(ctx, writer, email); err != nil {
				slog.Warn("seeding the bootstrap admin failed; add them to admins.yaml in git", "err", err)
			} else if ok {
				slog.Info("seeded admins.yaml with the bootstrap admin", "email", strings.ToLower(email))
				_ = syncer.SyncOnce(ctx)
			}
		}
	}
```

Helm `templates/crucible.yaml`, in the api env list:

```yaml
            {{- with .Values.bootstrapAdmin }}
            - { name: CRUCIBLE_BOOTSTRAP_ADMIN, value: {{ . | quote }} }
            {{- end }}
```

`values.yaml`: `bootstrapAdmin: ""   # first start only: seeds admins.yaml when it names no admin (spec §5.1)`. Terraform: add a `bootstrap_admin` variable (default `""`, a validation that it is empty or contains `@`) passed to the Helm install the way `git_bot_name` is. Add a `main.tftest.hcl` run asserting that the rendered bootstrap script contains `bootstrapAdmin=` when the variable is set.

`internal/auth/store.go`:

```go
// RevokeAgentTokens revokes every pairing token of the user (spec §5.1 "revocable").
func (s Store) RevokeAgentTokens(ctx context.Context, userID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE agent_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}
```

`internal/agenthub/hub.go`: if the hub has no way to close one user's connection, add `Drop(userID int64)`. It closes that user's websocket the way the hub already does when a newer agent connects (M3's "newest agent wins"); reuse that code path. `internal/httpapi/server.go`:

```go
		r.Delete("/api/agent/tokens", func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context())
			if err := d.Auth.RevokeAgentTokens(r.Context(), u.ID); err != nil {
				httpx.Error(w, err)
				return
			}
			d.Hub.Drop(u.ID) // the laptop's labs keep running until idle/TTL, as on any disconnect (spec §14)
			w.WriteHeader(http.StatusNoContent)
		})
```

`web/src/pages/Connect.tsx`: a `Revoke pairing` button (class `ghost`) with a confirm. It calls `DELETE /api/agent/tokens`, then shows "Pairing revoked. Your laptop agent is disconnected." in `role="status"`.

Docs:
- `docs/runbooks/aws.md`: one paragraph on `bootstrap_admin`, which seeds `admins.yaml` once. After that, admins are edited in git.
- `docs/runbooks/cognito.md`: invite that same email first.

- [ ] **Step 4: Run the tests**

Run:
```bash
go test ./internal/configapi/ ./internal/auth/ ./internal/agenthub/ ./internal/httpapi/ -race
deploy/helm/test.sh
(cd deploy/aws/main && terraform init -backend=false >/dev/null && terraform test)
(cd web && npm test && npx tsc -b)
```
Expected: PASS. `terraform test` uses only the existing `mock_provider` and never contacts AWS.

- [ ] **Step 5: Commit**

```bash
git add internal cmd deploy web docs
git commit -m "feat: bootstrap admin from Helm seeds admins.yaml once; pairing tokens can be revoked

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 20: Forge 102 fixture and the end-to-end check for M7

**Files:**
- Create: `examples/forge-102/training.yaml`, `examples/forge-102/modules/01-sparks/{module.yaml,quiz.yaml,reading/sparks.md}`
- Modify: `examples/platform/trainings.yaml` (register `forge-102`, **not enrolled**: the e2e enrols through the UI, so Go tests that load `examples/platform` keep their enrolment counts)
- Modify: `scripts/seed-git.sh` (add `forge-102` to the seeded repos), `scripts/local-check.sh` (lint `forge-102`)
- Create: `e2e/tests/forge-people.spec.ts`

**Interfaces:**
- Consumes: every UI label named in Tasks 5, 7, 12 and 16.
- Fixture ids: training `forge-102` ("Forge 102: Sparks", `progression: free`, maintainers `[senior@crucible.local]`, `estimated_hours: 0.25`), module `01-sparks` (title `Sparks`), reading `sparks.md` (heading `A Spark`), quiz with one single-choice question (`Cold iron` / `Hot iron`, answer 1).

- [ ] **Step 1: Write the fixture**

`examples/forge-102/training.yaml`:

```yaml
id: forge-102
title: "Forge 102: Sparks"
description: A short spark of a training. One reading, one question, one badge.
maintainers: [senior@crucible.local]
progression: free
estimated_hours: 0.25
modules: [01-sparks]
```

`examples/forge-102/modules/01-sparks/module.yaml`:

```yaml
title: Sparks
items:
  - reading: reading/sparks.md
  - quiz: quiz.yaml
```

`examples/forge-102/modules/01-sparks/reading/sparks.md`:

```markdown
# A Spark

Every blade starts as a spark. Read this, answer one question, and earn your first badge.

> [!NOTE]
> Maintainers can improve this page from Crucible's **Edits** screen.
```

`examples/forge-102/modules/01-sparks/quiz.yaml`:

```yaml
pass_threshold: 1
questions:
  - id: q-hot
    type: single
    prompt: Which iron do you strike?
    options: ["Cold iron", "Hot iron"]
    answer: 1
```

`examples/platform/trainings.yaml`, append:

```yaml
  forge-102:
    repo: file:///git/forge-102.git   # fixture for the M7 e2e (ranks, badges, journey, content edits); enrolled through the UI
    branch: main
```

`scripts/seed-git.sh`: add `forge-102` to the `for name in …` list, after whatever M5 and M6 added. `scripts/local-check.sh`: add `./bin/crucible lint examples/forge-102` next to the other lints.

Run: `go run ./cmd/crucible lint examples/forge-102 && go test ./internal/config/ ./internal/learn/ ./internal/labs/`
Expected: lint clean. Go tests still pass, because nobody is enrolled in Forge 102.

- [ ] **Step 2: Write the e2e**

`e2e/tests/forge-people.spec.ts`:

```ts
import { expect, test, type Browser, type Page } from '@playwright/test'

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept())
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

test('a content edit is reviewed and merged; the trainee earns a badge; mentor and leader see the journey', async ({ browser }) => {
  // The leader enrols the team, and the trainee, in Forge 102 (two bot commits to the platform repo).
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await leader.getByLabel('Training to enroll').selectOption('forge-102')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // The leader proposes an edit; authors never approve their own.
  await leader.getByRole('link', { name: 'Edits', exact: true }).click()
  await leader.getByLabel('Training').selectOption('forge-102')
  await leader.getByRole('button', { name: 'Start an edit' }).click()
  await leader.getByRole('button', { name: 'modules/01-sparks/reading/sparks.md' }).click()
  const editor = leader.getByLabel('Content of modules/01-sparks/reading/sparks.md')
  await expect(editor).toHaveValue(/Every blade starts as a spark/)
  await editor.fill((await editor.inputValue()) + '\nThe anvil remembers.\n')
  await expect(leader.getByTestId('edit-preview')).toContainText('The anvil remembers.')
  await leader.getByLabel('Title').fill('Add a line about the anvil')
  await leader.getByRole('button', { name: 'Submit for review' }).click()
  await expect(leader.getByRole('heading', { name: 'Add a line about the anvil' })).toBeVisible()
  await expect(leader.getByTestId('edit-status')).toHaveText('pending')
  await expect(leader.getByRole('button', { name: 'Approve and merge' })).toHaveCount(0)

  // The senior (a maintainer of Forge 102) reviews the diff and merges.
  const senior = await login(browser, 'senior')
  await senior.getByRole('link', { name: 'Edits', exact: true }).click()
  await senior.getByRole('link', { name: 'Add a line about the anvil' }).click()
  await expect(senior.getByTestId('diff')).toContainText('+The anvil remembers.')
  await senior.getByLabel('Review note').fill('Lovely.')
  await senior.getByRole('button', { name: 'Approve and merge' }).click()
  await expect(senior.getByTestId('edit-status')).toHaveText('merged', { timeout: 30_000 })

  // The trainee reads the merged text, passes the quiz, and earns the badge.
  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 102/ }).click()
  await trainee.getByRole('link', { name: 'A Spark' }).click()
  await expect(trainee.getByText('The anvil remembers.')).toBeVisible()
  await expect(trainee.getByRole('progressbar', { name: 'Reading progress' })).toBeVisible()
  await trainee.getByRole('button', { name: 'Mark as read' }).click()
  await trainee.getByTestId('module-01-sparks').getByRole('link', { name: 'Quiz' }).click()
  await trainee.getByLabel('Hot iron').check()
  await trainee.getByRole('button', { name: 'Submit answers' }).click()
  await expect(trainee.getByRole('status').filter({ hasText: 'Passed' })).toBeVisible()
  await trainee.getByRole('link', { name: 'Hearth', exact: true }).click()
  await expect(trainee.getByTestId('badges')).toContainText('Forge 102: Sparks')
  // CARRY (M4): Forge 101's cluster module can't be finished in local-check, so never assert a particular rank.
  await expect(trainee.getByTestId('rank')).toHaveText(/^(Ore|Ingot|Tempered|Blade|Sword|Masterwork)$/)

  // The trainee's mentor (the senior) and the team leader see Forge 102 forged.
  await senior.getByRole('link', { name: 'Mentor', exact: true }).click()
  await expect(senior.getByTestId('mentee-trainee@crucible.local').getByLabel('Sparks: forged')).toBeVisible()
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await leader.getByRole('link', { name: 'Journey', exact: true }).click()
  await expect(leader.getByTestId('journey-trainee@crucible.local-forge-102').getByLabel('Sparks: forged')).toBeVisible()

  // The catalog lists Forge 102 with its estimate, and the trainee's labs page answers.
  await trainee.getByRole('link', { name: 'Trainings', exact: true }).click()
  await expect(trainee.getByText('Forge 102: Sparks')).toBeVisible()
  await trainee.getByRole('link', { name: 'Labs', exact: true }).click()
  await expect(trainee.getByRole('heading', { name: 'Labs' })).toBeVisible()
})
```

- [ ] **Step 3: Run the full local check**

Run: `KEYCLOAK_PORT=8082 make local-check`
Expected: every Playwright test in the `local` project passes (forge-101, approvals, scoring, forge-people), and the script ends with `🔥 Local check passed. The forge holds.` If the merge assertion is slow, the cause is the re-sync after approval. Raise only that timeout, never the global one.

- [ ] **Step 4: Commit**

```bash
git add examples scripts e2e/tests/forge-people.spec.ts
git commit -m "test(e2e): content edit review and merge, badge, mentor and journey views on Forge 102

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 21: Spec coverage audit: walk every section, point at proof, fix gaps

This task decides the M7 done-when: "every spec section is checked off". Treat it as a review, not paperwork. For each row below:

1. Find the proof (a test name via `grep -rn 'func Test<Name>\|test(' …`, an e2e step, or a runbook section).
2. Run it.
3. Tick it.

When a claim has **no** proof, or the proof does not actually test the claim, fix it TDD-style:
- a failing test;
- the smallest fix;
- one commit per gap, titled `fix(<area>): <spec §> …`.

When the spec is deliberately not followed, the row must point at a documented ruling. That covers the deviations in Rulings §14 plus anything new; get the user's agreement for new ones (see Open decisions).

**Files:**
- Modify: `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`:
  - replace the "Spec coverage" table with the audited table, which has the columns *Spec section · Built in · Proof · Status* (✓ or *deviation → ruling*);
  - add a "Deviations (accepted)" list;
  - refresh "Known decisions to revisit" from `grep -rn 'ponytail:' internal cmd web/src deploy`.
- Modify: whatever code or tests the gaps need (each in its own commit).

- [ ] **Step 1: Walk the spec with this checklist** (pre-filled with the expected proof; confirm each one exists and passes)

| § | Claim | Expected proof |
|---|---|---|
| 1 | Add content by pushing to git | `internal/gitsync/syncer_test.go` sync tests; e2e enrol-via-git in `approvals.spec.ts` |
| 1 | KodeKloud-style lab with Check per task | `forge-101.spec.ts`, `cluster-lab.spec.ts` |
| 1 | No cloud lab without cost-aware approval; every lab dies on TTL/idle/schedule | `TestPaidLabNeedsApproval…`, `TestLabEndsAtScheduleClose`, sweep/idle tests in `internal/labs` |
| 1 | Tear down and rebuild from git + DB snapshot | M2 runbook A1–A6 (human), `deploy/aws/*` `terraform test`, helm restore assertions |
| 1 non-goal | No leaderboards | `RankCard.test.tsx`, `HeatMap.test.tsx`; Ledger "top spenders" is admin/leader cost data only (M6 ruling) |
| 2 | Every decisions-log row | covered by the rows below; check "Themes: admin default" (`/api/me` `default_theme`), "Score privacy" (M5 `TestTraineeNeverSeesRubric`, Task 6 `TestJourneyVisibilityAndMentees`), "Cost tiers: no defaults" (config test), "Escalation default 4 business hours" (`TestEscalationCountsBusinessHoursOnly`) |
| 3 | Monolith modules, River, SPA served by the API, Runner interface (local/cluster/aws), CLIs (lint, preview, aws, agent) | package tests; `httpapi` SPA test; Task 14; deviation: sync/provisioning not River jobs (M4 ruling 5) |
| 4.1 | platform.yaml: theme, escalation, tiers, **rank thresholds**, schedules; admins, quotes, trainings, teams, budgets, programs | `config_test.go` incl. `TestRankLadderFromConfig`; deviation: notification webhooks live per team in `team.yaml` (spec §10 says so), snapshot bucket in Helm/terraform values |
| 4.2 | training.yaml (maintainers, progression, modules, pass thresholds, **estimated hours**), module.yaml (**completion rule**), readings/quiz/lab/assets | `internal/content/load_test.go` (Task 2) |
| 4.3 | program file: **pinned_ref**, roles + defaults, enrolled, schedule **named or inline**, lab_defaults, budget | `configapi_test.go` (`TestPinBump`, `TestSetProgramKeepsInlineSchedule`), `config_test.go` |
| 4.4 | every question type; answers in range | `learn/quiz_test.go`, M5 human types, lint cases |
| 4.5 | lab manifest fields, hints, setup, aws block | `content/load_test.go`; deviation: `egress allowlist` → deny-private NetworkPolicy (M4 ruling 3) |
| 5.1 | OIDC code + PKCE, auto-provision by `sub`, email match, **pairing token hashed + revocable**, **bootstrap admin** | `auth/oidc_test.go`, `store_test.go` (`TestRevokeAgentTokens`), `TestSeedAdminOnlyIntoAnEmptyAdminsFile`, helm test |
| 5.2 | global/team/program roles, defaults on enrolment, mentor pairing | `config_test.go`, `rbac_test.go` |
| 5.3 | every matrix row incl. **review/merge content edits** (admin + maintainers), "nobody approves/scores own", trainee sees own scores, mentors see mentees | `rbac_test.go`, `TestEditPermissions`, `TestExtensionGoesToApproval`, M5 `TestScoringRules`, `TestJourneyVisibilityAndMentees` |
| 6 | poll 60 s + webhook with shared secret; invalid → last good + Forge Status + maintainers notified | `syncer_test.go`, `httpapi` hook test, `notify` `ReportSyncProblem` test |
| 6 | pins; in-progress attempts finish on their version; **bump with diff summary** | `TestPinBump`, labs tests that keep `sha` |
| 6 | config writes: message, trailer, retry ×3, conflict message | `writer_test.go` |
| 6 | **content edits**: branch, preview + diff, maintainer ≠ author approves, bot merge commit, stale on conflict | Tasks 10–12 tests, `forge-people.spec.ts` |
| 6 | lint: schema, **broken links/assets**, scripts exist + executable, shellcheck, answer ranges, infracost | `load_test.go`, `cmd/crucible/main_test.go`, M6 lint price check |
| 6 | **preview**: SPA + content against the working tree, local labs, setups | Task 14 tests + its by-hand acceptance |
| 7 | progression linear/free; **completion rule** | `learn` tests incl. `TestCompletionIsWeightedAndHonoursTheScoreRule` |
| 7 | reading: **highlighting, mermaid, callouts**, mark as read + **scroll tracking** | `Markdown.test.tsx`, `forge-people.spec.ts` (reading progress bar) |
| 7 | instant quizzes server-side, answers never sent, **attempt limit + cooldown**, best score counts | `learn/quiz_test.go`, `TestQuizAttemptLimitAndCooldown`, `TestFailedQuizKeepsLockAndPassedQuizStaysPassed` |
| 7 | human scoring queue, rubric, feedback, return, transcripts, overrides, sign-offs | M5 tests + `scoring.spec.ts` |
| 7 | **forge ranks** (weighted %, defaults, overridable, never lost, badges, hammer strike, personal) | Task 4 tests, `RankCard.test.tsx`, `forge-people.spec.ts` |
| 8.1 | lifecycle states, lab_events, failed → destroy, **stuck destroys alert admins** | labs tests, `TestStuckDestroyAlertsAdminsOnce` |
| 8.2 | cluster / local / aws; **program may require review of self-reported results** | M4/M6 tests, `TestSelfReportedLabWaitsForAScorer`, `TestReturnedSelfReportedLabMustBeRedone` |
| 8.3 | lab UI: split, tabs, "+", reconnect, copy/paste, font size, full-screen, collapse, loader with quotes + log | `forge-101.spec.ts`; check full-screen exists in `Lab.tsx`. If not, it is a gap to fix here |
| 8.4 | hints: one at a time, cost confirm, server-side text, `hint_reveals`, lint rules | labs + content tests |
| 8.5 | setups: first open, retries once, setup failed + skip, reset once per 5 min, never in browser, lint | labs tests |
| 8.6 | timer (server-authoritative, cooling/critical, warnings at 15/5, browser notification, title badge), extend once, **Extension pending**, at zero summary, idle modal | `web/src/lib/timer.test.ts`, `Timer.test.tsx`, Task 8 tests, labs idle tests |
| 9.1–9.2 | estimates, tiers, approver context, hard cap admin override, escalation; TTL/idle, schedules, budgets 80/100, kill switch | M3 tests + `approvals.spec.ts` |
| 9.3 | cost data, staleness, Ledger page | M6 tests + `aws-lab.spec.ts` |
| 9.4 | k3s on EC2, sleep/wake, teardown/restore, snapshots, IMDS blocked from labs | M2 runbook, `terraform test`, M4 `TestLabNetworkPolicy` + cluster-check |
| 10 | email + Slack/Teams; every listed event incl. **rank-up**; mute per kind | `notify_test.go`, Task 4 test; check `Kinds` lists every kind |
| 11 | **mentor dashboard**; **journey heat map** with the four stuck signals | Task 6 tests, `forge-people.spec.ts` |
| 12 | four themes, per-user + admin default; motion list incl. **hammer strike**, **heat shimmer**; calm toggle + reduced motion; quotes built-in + quotes.yaml; **navigation**; **accessibility** | Settings page, `RankCard.test.tsx`, `contrast.test.ts`, Task 18, Task 16 nav |
| 13 | each table exists or has a ruling | the migrations; deviations list |
| 14 | errors (provision fail log tail + re-request, gitsync failure, git conflict, agent disconnect, Cost Explorer down) | labs/configapi/M6 tests |
| 14 | security (never in API pod, quotas/policies, sysbox, short-lived AWS creds, answers/scripts never to client, secrets in K8s Secrets, privileged actions audited, self-reported) | M4/M6 tests, helm test, `TestTraineeNeverSeesRubric`, audit tests |
| 14 | testing: unit, testcontainers, kind + sysbox, AWS sandbox, lint fixture, **Vitest component tests**, Playwright for SSO / read→quiz→lab→check / approval / scoring | everything above; the sysbox and AWS sandbox parts are runbook checklists (M4 Task 7, M6 Task 13) |

- [ ] **Step 2: Fix every gap found** (TDD, one commit each). Known candidates to check first:
  - full-screen terminal mode (§8.3);
  - "copy/paste" in the terminal (§8.3): xterm defaults may suffice; confirm by hand;
  - browser notification permission explanation (§8.6);
  - `content_sync_failed` reaching *maintainers*, not only admins (§6);
  - every `notify.Kinds` entry shown on the Settings page (§10).

- [ ] **Step 3: Rewrite the roadmap's coverage table** with the audited rows (*Spec section · Built in · Proof · Status*), add "Deviations (accepted)" (Rulings §14 plus any agreed in Step 2), and refresh "Known decisions to revisit" from the `ponytail:` notes.

- [ ] **Step 4: Run everything touched**

Run: `gofmt -l . ; go vet ./... ; go test -race ./... ; (cd web && npm test && npm run build)`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add -A docs internal cmd web
git commit -m "docs: spec coverage audit — every section points at its proof

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 22: Release-candidate verification

Nothing new is built here. Run every check from a clean tree, in this order, and record the results. **Never real AWS.** Every Terraform command below is `init -backend=false`/`validate`/`test` with the stacks' `mock_provider` tests. If any `*.tftest.hcl` turns out to run without a mock provider, **stop and report**; do not run it.

- [ ] **Step 1: Hygiene and Go**

```bash
export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH
cd /Users/adelin/Projects/Crucible
git status --short            # expect: nothing but intended files
df -h . | tail -1             # M4 was blocked once by a full disk: need ≥ 20 GiB free for kind + images
gofmt -l .                    # expect: no output
go vet ./...                  # expect: no output
go test -race ./... 2>&1 | grep -v -E '^(ok|\?)'   # expect: no output
```

- [ ] **Step 2: Web**

```bash
(cd web && npm ci && npm test && npm run lint && npm run build)
```
Expected: all Vitest suites pass (lib + component tests), lint clean, build succeeds.

- [ ] **Step 3: Infrastructure tests (offline)**

```bash
for d in deploy/aws/persistent deploy/aws/main deploy/aws/labs; do
  (cd "$d" && terraform init -backend=false -input=false >/dev/null && terraform validate && terraform test) || exit 1
done
deploy/helm/test.sh
```
Expected: every `terraform test` run passes (mock providers only), and `deploy/helm/test.sh` exits 0.

- [ ] **Step 4: CLIs and fixtures**

```bash
make build
for f in examples/platform examples/forge-101 examples/forge-102 examples/forge-201 examples/forge-301 examples/forge-401; do ./bin/crucible lint "$f" || exit 1; done
```
Expected: no problems. The infracost price check reports "skipped" because there is no key.

- [ ] **Step 5: End to end**

```bash
KEYCLOAK_PORT=8082 make local-check
KEYCLOAK_PORT=8082 make cluster-check
kind get clusters             # expect: none left behind
```
Expected: `🔥 Local check passed. The forge holds.` and `🔥 Cluster check passed. The crucible holds.`

- [ ] **Step 6: Preview, by hand**

Repeat Task 14 Step 6 (with `docker build -t crucible:dev .` first).
Expected: `programs ok`, `live reload ok`, `cleaned up`.

- [ ] **Step 7: Record the release candidate**

In `docs/superpowers/plans/2026-10-05-crucible-roadmap.md`, set the M7 row's done-when cell to note the RC: "**RC verified <date>**: go test -race, web tests/build, terraform tests, helm test, local-check, cluster-check, preview". Append a short "Release candidate" section with the commit SHA and the command list above. List the human-only checks still owed before a production cut:
- M2 sandbox acceptance A1–A6;
- M4 sysbox-on-node runbook;
- M6 AWS lab sandbox checklist.

```bash
git add docs/superpowers/plans/2026-10-05-crucible-roadmap.md
git commit -m "docs: M7 release candidate verified

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage (M7)

| Spec item | Task |
|---|---|
| §4.1 rank thresholds in platform.yaml | 1 |
| §4.2 `estimated_hours`; §7 module completion rule | 2, 3 |
| §4.3 inline schedule windows (deferred from M3) | 1 |
| §5.1 bootstrap admin from Helm; pairing tokens revocable | 19 |
| §5.3 "Review/merge content edits" (admin) + maintainers; nobody reviews their own | 11 |
| §6 content edits from UI (branch, preview + diff, maintainer approval, bot merge, stale) | 10, 11, 12, 20 |
| §6 pinned-ref bump with diff summary (deferred from M3) | 13 |
| §6 lint: broken links/assets | 2 |
| §6 `crucible preview` (reading, quizzes, local labs, setups) | 14 |
| §7 reading: syntax highlighting, mermaid, callouts, scroll tracking | 17 |
| §7 quiz attempt limits and cooldown | 2, 3 |
| §7 forge ranks (weighted, thresholds, never lost), badges, hammer strike, personal | 1, 3, 4, 5, 20 |
| §8.1 stuck destroys alert admins | 15 |
| §8.2 "a program can require human review for self-reported results" (deferred from M5) | 9 |
| §8.6 "Extension pending" (deferred from M3/M6) | 8 |
| §10 rank-up to trainee + mentor | 4 |
| §11 mentor dashboard; journey heat map with stuck flags | 6, 7, 20 |
| §12 heat shimmer, hammer strike, calm-safe motion; navigation order; Trainings and Labs pages; Forge Status additions; keyboard tabs, live regions, focus rings, contrast per theme | 5, 15, 16, 18 |
| §13 `ranks` (+ `badges`, `content_edits`) | 4, 11 |
| §14 Vitest component tests; release check | 5, 7, 8, 12, 17, 18, 22 |
| Done-when: every spec section checked off | 21 |

**Deliberately not in M7:** deleting files or uploading binaries through content edits (git only); escalation of extension requests; holidays and team time zones in "business days"; in-app notifications (spec §10 names email and Slack/Teams only); a forge-hosted pull-request integration (spec §1 non-goal).

## Open decisions (defaults applied in this plan)

1. **Who may propose content edits.** Default: admins, the training's maintainers, and team leaders/seniors, never anyone enrolled in that training. Alternative: anyone with a team role. That widens who can read answer keys through the editor.
2. **Content-edit branches after a decision.** Default: deleted on merged, rejected, withdrawn and stale; the merge commit keeps history. Alternative: keep them for audit, which clutters the repo.
3. **Editable file types in the UI.** Default: `.md .yaml .yml .sh`, no deletes, no binaries. Alternative: allow deletes, at the cost of more review risk.
4. **Extension requests do not escalate.** Default: they wait at their tier until decided or until the lab ends. Alternative: reuse request escalation, which is extra code for a request that lives hours at most.
5. **A rejected extension uses up the lab's one extension.** Default: yes, which matches "once per lab" and prevents re-request spam.
6. **Returned self-reported lab** clears that module's task results, so the trainee redoes it. Alternative: let the scorer only score. Simpler, but "return" would then mean nothing for labs.
7. **`crucible preview` needs Docker and the Crucible image** (`docker build -t crucible:dev .`). Default: yes; it reuses the exact production image. Alternative: an all-in-process preview that bundles the SPA and an embedded DB into the `crucible` binary, which is much more code.
8. **Scroll tracking** means a reading-progress bar. Default: "Mark as read" is not gated on scrolling. Alternative: gating, which hurts keyboard users and short pages and breaks the existing e2e.
9. **Business days** are Mon–Fri UTC without holidays. Alternative: per-team zone and a holiday list in `platform.yaml`.
10. **Global catalog visibility.** Default: every signed-in user sees all training titles and descriptions, but no content. Alternative: only trainings someone in your teams is enrolled in.
11. **Percent shown on Hearth and outlines** switches to the weighted value, so it matches the rank. Alternative: keep item-count percent in the UI and use weights only for ranks, which shows two different numbers.

## Self-review notes

- **Spec coverage:** every spec section is either owned by a task above or by M1–M6. Task 21 makes that a checked table in the roadmap, with deviations recorded.
- **Type consistency:**
  - `learn.Standing`, `learn.Ladder`, `learn.RankStep` (Tasks 3–4) are used by `journey` (Task 6).
  - `gitsync.ContentRepo`, `ErrMergeConflict`, `ErrMergeInvalid` (Task 10) are used by `edits` (Task 11).
  - `config.Program.Inline` and `ReviewSelfReported` (Task 1) are used by Tasks 8, 9 and 13.
  - `notify` kinds `RankUp`, `ContentEdit` and `LabStuck` are added in Tasks 4, 11 and 15.
  - `/api/me` flags `is_mentor` and `can_edit_content` come from Tasks 6 and 11 and are read by Tasks 7, 12 and 16.
- **M5/M6 names** (`scoring.Submit`, `labs.recompute`, `labs.Refresh`, `budgetLimit`, `can_score`, `can_view_spend`) come from their plans. Tasks 8, 9, 15 and 16 say to follow the real code where it differs.
