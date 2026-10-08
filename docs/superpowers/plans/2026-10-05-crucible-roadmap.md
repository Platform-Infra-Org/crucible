# Crucible Implementation Roadmap

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md`

The spec asks for one release containing everything (§2, "Release"). We still build it as seven milestones in dependency order. Each milestone ends with working, tested software, so problems surface early, and the release is cut after M7.

Detailed task-by-task plans exist for **M1** and **M2**. M3–M7 get their own plans when they start. That way each plan is written against the code that actually exists by then, rather than against guesses.

| # | Milestone | Plan | Done when | Status |
|---|---|---|---|---|
| **M1** | **Local Forge**: SSO; git-sourced config and content; reading; instant quizzes; linear/free progression; local labs via `crucible-agent`; KodeKloud-style lab UI with tabbed terminals, checks, setup scripts, hints, timer and idle prompt; themes, motion, quotes; `crucible lint` | `2026-10-05-m1-local-forge.md` (20 tasks) | `make local-check` passes: a trainee completes Forge 101 end to end using only a laptop lab | Done |
| **M2** | **AWS Deploy**: single-node k3s on EC2 (~$50/month on schedule); Cognito sign-in; S3 backups and restore; scheduled sleep/wake; `crucible aws` CLI | `2026-10-05-m2-aws-deploy.md` (6 tasks) | Sandbox acceptance A1–A6: live HTTPS URL, sleep/wake, teardown → rebuild restores data | Done offline; A1–A6 on a real sandbox is a human runbook step (`docs/runbooks/aws.md`) |
| **M3** | **Approvals & FinOps core**: River job queue; lab request → cost-tiered approval → escalation after 4 business hours; schedules (lab windows + effective end); budgets and hard caps; kill switch; email + Slack/Teams notifications; config write-back to the platform repo (program roles, schedules) | `2026-10-06-m3-approvals-finops.md` (12 tasks) | A cloud-tier request needs an approver, escalates when ignored, is blocked over the cap; labs die at window close | Done (approvals.spec.ts; `TestEscalationClimbsTiersThenExpires`, `TestHardCapRoutesToAdminAndTheOverrideIsAudited`, `TestLabEndsAtScheduleClose`) |
| **M4** | **Cluster runtime**: sysbox on k3s; namespace per lab with quota and default-deny NetworkPolicy (blocks IMDS); exec-based PTYs and server-side checks | `2026-10-06-m4-cluster-runtime.md` (9 tasks) | Forge 101's lab runs with `runtime: cluster` and checks are not self-reported | Done (`make cluster-check`, cluster-lab.spec.ts; sysbox itself by runbook) |
| **M5** | **Human scoring**: text/upload/sign-off questions; review tasks; Anvil scoring queue with rubric, feedback, return-for-rework, audited overrides; terminal transcripts; S3 uploads (local disk in dev); progression waits on pending scores; Forge 301 fixture | `2026-10-06-m5-human-scoring.md` (11 tasks) | A scorer grades a free-text answer and a lab submission; the trainee sees the feedback | Done (scoring.spec.ts) |
| **M6** | **AWS labs & Ledger**: shared lab account roles with permission boundary + session tags; terraform runner pods + workspace pod; infracost estimates; reaper; Cost Explorer ingestion; Ledger (FinOps) page | `2026-10-06-m6-aws-labs-ledger.md` (14 tasks) | An AWS lab is estimated, approved, provisioned, checked, destroyed and swept; spend shows on the Ledger | Done against fakes (aws-lab.spec.ts in `cluster-check`, `TestAWSLabFromRequestToTagSweep`); real-account checklist in the runbook |
| **M7** | **Forge & people**: forge ranks (Ore → Masterwork) and badges; mentor dashboard; journey heat map with stuck signals; in-app content edit + maintainer review (branch + bot merge on plain git); pinned-ref bump with diff summary; `crucible preview`; Forge Status additions; Trainings/Labs navigation; reading and motion polish, accessibility; deferred items; spec coverage audit + RC verification | `2026-10-06-m7-forge-people.md` (22 tasks) | Release candidate: every spec section is checked off in the coverage table below, and go test -race, web tests/build, terraform tests, helm test, `local-check` and `cluster-check` all pass | Tasks 1–21 done: every row below is P or an accepted deviation. Task 22 (RC run of every suite, by-hand checks, final review) pending |
| **M8a** | **Config in Postgres**: settings, tiers, cluster rate, schedules, quotes, admins, trainings registry, teams, membership, mentors, webhooks, budgets, programs, roles, enrollments and pins move behind `internal/org` with validated, audited, versioned writes and admin routes; startup picks Postgres (no `CRUCIBLE_PLATFORM_REPO`) or the git platform repo; admin pages Forge settings and Registry; Team and Program settings write through the new routes on Postgres. Training content stays in git. Export/import, moving the e2e fixtures off the git platform repo and deleting the git config write-back are **M8b** (no plan yet) | `2026-10-07-m8-config-in-db.md` (spec: `../specs/2026-10-07-db-owned-config-design.md`) | A fresh instance runs on `DATABASE_URL` + `CRUCIBLE_BOOTSTRAP_ADMIN` alone; git mode unchanged | M8a done: proven by `TestStoreDrivesTheSnapshotWithoutGit`, `TestPlatformMatchesConfigLoad`, `TestSetSettingsStoresAndAudits`, `TestEnrollStoresExplicitRoles`, `TestOrgWriteRoutesAreAbsentInGitMode`, `TestSeedAdminOnlyIntoAnEmptyTable` |

## Spec coverage

Audited in M7 Task 21 (`.superpowers/sdd/2026-10-06-m7-forge-people/coverage-audit.md` has the claim-by-claim walk with line numbers). Proof names Go tests (`Test…`), Vitest files (`*.test.ts[x]`) and Playwright specs (`*.spec.ts`, run by `make local-check`; cluster-lab and aws-lab by `make cluster-check`).

Status: **P** proven by the named tests · **D** accepted deviation (see below) · **Hand** checked by a person in Task 22 or on a real AWS sandbox (runbook).

| Spec section | Built in | Proof | Status |
|---|---|---|---|
| §1 Goals: content by git push, KodeKloud labs, approval for cost, every lab dies | M1, M3, M4 | `TestSyncLoadsHead`; forge-101.spec.ts; cluster-lab.spec.ts; `TestPaidLabNeedsApprovalAndNobodyApprovesTheirOwn`; `TestClusterLabAboveAutoApproveNeedsApproval`; `TestSweepExtendAndOwnership`; `TestLabEndsAtScheduleClose`; `TestKillSwitch` | P |
| §1 Tear down and rebuild from git + DB snapshot | M2 | `TestTeardownRequiresYes`, `TestInitAndUp`, `TestSleepSnapshotsBeforeStop`; main.tftest.hcl; deploy/helm/test.sh | P offline; Hand (A1–A6) |
| §1 Non-goals: no leaderboards, no overseer, no host PR integration | M1, M7 | RankCard.test.tsx (personal rank only); edits use plain git branches (`TestPushEditAndMerge`) | P; D (Ledger top spenders) |
| §2 Decisions: Helm, OIDC, bot commits vs reviewed edits, themes, score privacy, cost tiers, escalation, rank %, AWS isolation, timer | M1–M7 | deploy/helm/test.sh; every e2e signs in via Keycloak; `TestWriterCommitsWithTrailer`; `TestMetaServesThemeAndQuotes`; `TestTraineeNeverSeesRubric`; `TestLoadRejectsBadConfig`; `TestEscalationCountsBusinessHoursOnly`; `TestStanding`; labs.tftest.hcl, `TestAssumeLabTagsTheSession`; `TestEffectiveEndPicksEarliest` | P |
| §3 Architecture: Go monolith, River, SPA, runner interface, CLIs | M1, M3 | `TestPeriodicAndInsertedJobsRun`; `TestHealthzAndSPAFallback`; local/cluster/aws runner tests; cmd/crucible main_test.go, preview_test.go; awsops tests | P; D (sync and provisioning are goroutines) |
| §4.1 Platform repo: platform.yaml (theme, tiers, escalation, rank thresholds, schedules, cluster rate card), admins, quotes, trainings, teams, budget.yaml | M1, M3, M6, M7 | `TestLoadExamplePlatform`; `TestRankLadderFromConfig`; `TestScheduleValidation`; `TestClusterRateFromConfig`; `TestLoadRejectsBadConfig` | P; D (webhooks per team, snapshot bucket in Helm, sub-budgets in program files) |
| §4.2 Training repo: training.yaml, module.yaml completion, estimated hours | M1, M7 | `TestLoadMinimalAppliesDefaults`; `TestLoadProblems`; `TestCompletionAndAttempts` | P |
| §4.3 Program file: pinned ref, role defaults, enrolment, named or inline schedule, lab defaults, budget | M3, M7 | `TestPinBump`; `TestInlineProgramScheduleAndReviewFlag`; `TestSetProgramKeepsInlineSchedule`; `TestResolveTimingPrecedence`; approvals.spec.ts | P |
| §4.1/§4.3 (M8a) Config and org data in Postgres: settings, schedules, quotes, registry, teams, programs, budgets; snapshot equals `config.Load`; role defaults at read time | M8a | `TestPlatformMatchesConfigLoad`; `TestStoreDrivesTheSnapshotWithoutGit`; `TestPlatformOnAFreshDatabase`; `TestEnrollStoresExplicitRoles`; `TestGetThenPutBackStoresNoRoles`; `TestSetBudgetVersions`; `TestPlatformIgnoresAProgramWhoseTrainingWasRemoved` | P; D (git bridge until M8b; orphan programs skipped) |
| §5.1 (M8a) Bootstrap admin into the database, once; admins admin-only; last admin kept | M8a | `TestSeedAdminOnlyIntoAnEmptyTable`; `TestRemoveLastAdminIsRefused`; `TestAdminRoutesAreAdminOnly`; `TestOrgWriteRoutesAreAbsentInGitMode` | P |
| §6 (M8a) Validated, audited, versioned org writes; stale version is 409; registry refuses bad repos, credentials masked | M8a | `TestSetSettingsRefusesAStaleVersion`; `TestStaleVersionIs409AndWritesNothing`; `TestTrainingWritesAreAudited`; `TestRemoveTrainingRefusedWhileAProgramUsesIt`; `TestRepoCredentialsAreStoredButNeverShownOrAudited`; `TestRedactRepo`; `TestInTxRollsBackTheChangeWhenTheAuditWriteFails` | P; D (repoints allowed, `programs_affected` audited) |
| Web (M8a) Administrator menu, Forge settings and Registry pages | M8a | AdminMenu.test.tsx; AdminSettings.test.tsx | P |
| §4.4 Quiz schema, every question type | M1, M5 | `TestScore`; forge-101.spec.ts (single/multi/exact/regex/order/match); human_test.go; scoring.spec.ts | P |
| §4.5 Lab manifest: tasks, hints, setup, aws block, `task_order: free` | M1, M6, M7 | `TestLoadProblems`; `TestAWSLabModuleRules`; `TestPriceCheck`; `TestFreeTaskOrderOpensEveryTask` | P; D (egress allowlist) |
| §5.1 OIDC + PKCE, auto-provision by verified email, pairing tokens (hashed, revocable), bootstrap admin | M1, M7 | `TestClaimsRequireVerifiedEmail`; `TestUsersSessionsAndAgentTokens`; `TestRevokeAgentTokensIsAuditedAndIdempotent`; `TestRevokedAgentStopsWithItsOwnMessage`; `TestSeedAdminOnlyIntoAnEmptyAdminsFile`, `TestBootstrapAdminRunsOnce` | P; D (pairing token, not device code) |
| §5.2–5.3 Roles, permission matrix, nobody approves or scores their own, trainees see only their own scores | M1, M3, M5, M7 | `TestMatrix`, `TestTierRouting`, `TestApprovalRules`, `TestSpendTeams`; `TestPermissions`; `TestEditPermissions`; `TestScoringRules`; `TestMaintainerNeverReviewsOwnEdit`; `TestJourneyVisibilityAndMentees` | P |
| §6.1 Poll every 60 s; webhook with shared secret | M1, M7 | `CRUCIBLE_SYNC_INTERVAL` default; `TestGitHookNeedsTheSecret` | P; D (one hook URL for every repo) |
| §6.2 Invalid commit keeps the last good version, Forge Status, maintainers notified | M1, M3, M7 | `TestBadHeadKeepsLastGood`; `TestBadPinKeepsLastGood`; `TestForgeStatusShowsAttention`; `TestSyncProblemReachesMaintainersAndAdmins` | P |
| §6.3 Pins, attempts finish on their version, bump with diff summary | M7 | `TestVersionLoadsAnOldSHAAfterARestart`; `TestCheckOnAnOldLabNeverRunsNewerScripts`; `TestChangesAndCheckPin`; `TestPinBump` | P |
| §6.4 Config writes: trailer, retry, conflict message | M3 | writer_test.go; `TestStaleEditIsRefused`; `TestLeaderRemovedInGitCannotWrite` | P |
| §6.5 Content edits: branch, preview + diff, maintainer ≠ author, bot merge, stale on conflict | M7 | content_repo_test.go (incl. `TestEditDiffIgnoresRepoAttributes`, `TestLabDirDotIsNotALabDir`); `TestOnlyOneApprovalMerges`; `TestApproveStaleEdit`; `TestMergeRefusesAnEditThatMovedSinceReview`; forge-people.spec.ts | P |
| §6.6 Lint: schema, links/assets, executable scripts, answer ranges, infracost | M1, M6, M7 | `TestLoadProblems`; `TestLinksThatResolve`; `TestLintReportsProblems`; `TestPriceCheck` | P; shellcheck runs only when installed (untested) |
| §6.7 `crucible preview` | M7 | preview_test.go; `TestPreviewGuard` | P; Hand (hardened container run) |
| §7.1 Progression linear/free, completion rule | M1, M7 | `TestProgressionUnlocksModuleTwo`; `TestFreeProgressionNeverLocks`; `TestCompletionIsWeightedAndHonoursTheScoreRule`; `TestScoreRuleIgnoresPendingItems` | P |
| §7.2 Reading: highlighting, mermaid, callouts, mark as read, progress | M1, M7 | Markdown.test.tsx; Mermaid.test.ts; forge-101.spec.ts; forge-people.spec.ts | P |
| §7.3 Instant quizzes: server-side, answers never sent, attempt limit + cooldown, best score | M1, M7 | `TestPublicQuizHidesAnswers`; `TestPublicIDsDoNotRevealOrderOrMatch`; `TestQuizAttemptLimitAndCooldown`; `TestConcurrentAttemptsRespectTheLimit` | P |
| §7.4 Scoring queue: rubric, feedback, return, transcripts, audited overrides, sign-offs; admin reset | M5, M7 | scoring_test.go, anvil_test.go; `TestOverrideIsOneTransaction`; `TestTerminalSessionIsRecorded`; `TestSignOff`; `TestAdminResetIsAdminOnlyAuditedAndTellsTheTrainee`; `TestAdminResetRecomputesProgressAndKeepsRank`; `TestAdminQuizResetReopensAnInstantQuiz` (instant-only quiz at max_attempts, by trainee and module); `TestEnrolledScorerNeverSeesRubric`; scoring.spec.ts | P; D (scores final, attempts count across versions) |
| §7.5 Forge ranks, badges, never lost, rank-up notice | M7 | forge_test.go (`TestRankIsNeverLost`, `TestBadgeForACompletedTraining`, `TestRankBoundaries`); RankCard.test.tsx; forge-people.spec.ts | P |
| §8.1 Lifecycle, `lab_events`, failed always destroys, stuck destroys alert | M1, M3, M7 | `TestEndDuringProvisioningDestroysLateContainers`; `TestStuckDestroyOfAFailedProvisionEndsFailed`; `TestStuckDestroyAlertsAdminsOnce` | P; D (goroutines, not River) |
| §8.2 Runtimes: local (self-reported, optional review), cluster (namespace, quota, deny NetworkPolicy, exec checks), aws (terraform pods, STS tags, boundary, reaper, sweep) | M1, M4, M6, M7 | forge-101.spec.ts; `TestSelfReportedLabWaitsForAScorer`; `TestProvisionCreatesIsolatedLab`, `TestLabNetworkPolicy`, `TestClusterLabOnKind`; cluster-lab.spec.ts; `TestAWSLabFromRequestToTagSweep`; `TestReaperDeletesOnlyEndedKnownLabs`; aws-lab.spec.ts | P; Hand (sysbox, real AWS); D (tamper-resistant wording) |
| §8.3 Lab UI: tasks panel, Check, header, terminal tabs, splitter, full screen, copy/paste, loader with provisioning log | M1, M7 | forge-101.spec.ts (incl. Full screen `aria-pressed`); `TestProvisioningViewCarriesTheEventLog`; Ctrl+Shift+C/V in Terminal.tsx | P; Hand (full screen and copy/paste in Chrome) |
| §8.4 Hints | M1 | forge-101.spec.ts; `TestFullLabFlow`; `TestHeatMapAndStuckFlags` | P |
| §8.5 Task and lab-level setup scripts | M1, M7 | `TestSetupFailureAllowsSkipAndResetIsRateLimited`; `TestConcurrentOpenTaskRunsSetupOnce`; `TestLabLevelSetupRunsOnceBeforeReady`; `TestLabLevelSetupFailureFailsTheLab` | P |
| §8.6 Timer, limit label, extend once + Extension pending, closed/lapsed notices, cooled summary, idle check and end messages | M1, M3, M6, M7 | timer.test.ts; Timer.test.tsx; `TestExtensionIsCappedByTheWindow`; `TestExtensionGoesToApproval`; `TestClosedExtensionTellsTheTrainee`; `TestPendingExtensionLapsesWithTheLab`; forge-101.spec.ts | P; Hand (browser notification) |
| §9.1 Estimates (cluster rate card, infracost, local $0), tier routing, hard cap, escalation | M3, M6, M7 | `TestPlatformRatePricesClusterLabs`; `TestInfracostEstimatorCachesPerContentVersion`; `TestTierRouting`; `TestHardCapRoutesToAdminAndTheOverrideIsAudited`; `TestEscalationClimbsTiersThenExpires`; approvals.spec.ts | P |
| §9.2 TTL/idle, schedules, budgets 80/100, kill switch | M3 | `TestScheduleGatesRequestsAndApprovals`; `TestBudgetAlertsOncePerLevelPerMonth`; `TestKillSwitch`; approvals.spec.ts | P |
| §9.3 Live estimates, Cost Explorer actuals, staleness, Ledger | M6 | `TestLedgerNumbersAndStaleness`; `TestIngestCostsUpsertsAndMarksFailures`; aws-lab.spec.ts | P |
| §9.4 k3s on EC2, sleep/wake, teardown/restore, IMDS blocked | M2, M4, M6 | awsops tests; tftests; `TestLabPodWaitsForTheIMDSBlock` | P offline; Hand |
| §10 Email + Slack/Teams, every event, mutes, envelope sender | M3, M7 | `TestEmailIsSentWithoutHeaderInjection`; `TestWebhooksPostSlackTextAndTeamsCards`; `TestWebhookSSRFGuard`; `TestRankUpNotifiesOnceTraineeAndMentor`; `TestNotifyQueuesUnmutedEmailsAndTeamPosts`; `TestSMTPEnvelopeSenderIsTheBareAddress` | P; D (no notifications table) |
| §11 Mentor dashboard, journey heat map, stuck signals | M7 | `TestMenteeDetail`; `TestHeatMapAndStuckFlags`; `TestInactiveAfterFiveBusinessDays`; HeatMap.test.tsx; forge-people.spec.ts | P |
| §12 Themes, motion, quotes, navigation, accessibility | M1, M7 | contrast.test.ts (every colour pair app.css draws, plus a no-literal-colours guard); advanceFocus.test.ts; RankCard.test.tsx and HeatMap.test.tsx (calm); `TestMetaServesThemeAndQuotes`; Nav.tsx; Trainings.test.tsx | P; Hand (keyboard/screen-reader sweep) |
| §13 Data model | every milestone | `TestMigrationsCreateTablesAndAreIdempotent` | P; D (caches, folded tables, outbox) |
| §14 Errors: provision failure, sync failure, push conflict, agent disconnect, Cost Explorer down | M1, M3, M6 | `TestReRequestAfterFailedStartSkipsApprovalForAnHour`; `TestBadHeadKeepsLastGood`; `TestWriterStaleBase`; `TestPendingCallFailsFastOnDisconnect`; `TestIngestCostsFailureRecordedOnSpentCtx` | P |
| §14 Security: no checks in the API pod, short-lived AWS creds, secrets never to the browser, audit, CSRF/Origin, CSP | M1–M7 | `TestClusterPSAPolicy`; `TestSweepRefreshesReadyAWSLabCredentials`; `TestRubricOnlyReachesScorerViews`; `TestAssetsServeOnlySandboxedAllowlistedFiles`; `TestLogRollsBackWithTheTransaction`; `TestStateChangingRequestsNeedSameOrigin`; `TestSecurityHeaders` (CSP is `'self'` only; fonts are self-hosted) | P |
| §14 Testing: Go + testcontainers, kind runner tests, Vitest, Playwright, lint fixtures | every milestone | `go test -race ./...`; `make cluster-check`; `npm test`; `make local-check`; `TestLintExamplesPass` | P; D (AWS sandbox nightly, Forge 401) |

## Docs & editor coverage

Branch `feat/docs-and-editor`, spec `2026-10-07-docs-and-editor-design.md`, plan `2026-10-07-docs-and-editor.md`.
Proof names Go tests, Vitest files (`web/src/...`) and Playwright specs (`authoring.spec.ts`, `docs.spec.ts`, run by
`make local-check`).

| Spec section | Proof | Status |
|---|---|---|
| §1 Goals: Docs tab kept current, IDE editor, building blocks | `TestDocsCoverEveryRouteAndRole`; authoring.spec.ts; docs.spec.ts | P |
| §2 Architecture: blocks registry, authoring package, Monaco lazily loaded | `TestAuthoringRoutes`; `web/scripts/check-chunks.mjs` (Monaco stays out of the entry chunk, run by `npm run build`) | P |
| §3 Block registry: every content field described, schema from the structs, hover text | `TestEveryContentFieldIsDescribed`; `TestSchemaAcceptsEveryExample`; `TestSchemaRejects`; `TestSchemaHasHoverText`; `TestValidateReportsProblemsWithLines`; `TestOneCheckAtATimePerUser`; `TestBackToBackValidateSameUser` | P; D (problem lines are heuristics) |
| §4 Docs tab: pages by role, search, ? links, version, generated reference | `TestLoadParsesAndOrders`; `TestLoadRefusesBadPages`; `TestRoutes`; `TestEmptyListsAreArrays`; `TestEveryPageStartsWithItsTitle`; `TestDocsCoverEveryRouteAndRole`; lib/docs.test.ts; docs.spec.ts | P |
| §5 Editor: explorer, tabs, YAML completion and hover, Problems, Changes, go to file, preview, small screens | Explorer.test.tsx; panels.test.tsx; Preview.test.tsx; previewModel.test.ts; diff.test.ts; DiffView.test.tsx; model.test.ts; authoring.spec.ts (hover link, Ctrl/Cmd+Z after an insert) | P; Hand (keyboard and screen-reader sweep) |
| §6 Drafts: server-side autosave, compare-and-set, limits, rebase, reopen a returned edit, discard | `TestDraftLifecycle`; `TestDraftCompareAndSet`; `TestSubmitCompareAndSet`; `TestDraftLimitsAndAccess`; `TestDraftCapCountsVisibleDraftsOnly`; `TestDraftFromAReturnedEdit`; `TestRebaseFollowsUntouchedFiles`; `TestRebaseConflictWhenUpstreamDeleted`; `TestRebaseConflictWhenRenamedAndEditedSourceChanged`; autosave.test.ts; rebase.test.ts; editLimits.test.ts; authoring.spec.ts (newer content) | P |
| §7 Edits as operations: put, rename, delete; exec bits; rename-aware stored diff; legacy files map | `TestCheckOps`; `TestApplyOps`; `TestPushEditRenameDeleteAndModes`; `TestPushEditOpsRefusals`; `TestEditOpsRenameAndDelete`; `TestEditOpsAreValidated`; `TestFilesMapIsTranslatedToPuts`; `TestFilesListsEverythingWithWhatIsEditable`; `TestMigrationsCreateTablesAndAreIdempotent` | P; D (files map for one release, case-only renames refused) |
| §8 Blocks catalog: forms, targets, literal values, comment-preserving inserts, git-only blocks | `TestEveryBlockSampleLoadsAndLints`; `TestCatalogCoversTheContentModel`; `TestInsertValuesAreLiteral`; `TestInsertWritesWhatWasTyped`; `TestInsertReturnsOpsForTheDraft`; `TestInsertIntoBrokenFile`; `TestLabBlockInBrokenModule`; yamlx `TestInsert…` and `FuzzInsert`; forms.test.ts; BlocksPanel.test.tsx; authoring.spec.ts | P; D (AWS template and new training are git-only) |
| §9 Security: enrolled users refused, isolated checks, editor-only answer marks, CSP unchanged, ops path tricks | `TestAuthoringRefusesEnrolledAndBadInput`; `TestReadRefusesOversizeBodies`; `TestCSPStaysStrict`; `TestCheckOps`; `TestPushEditOpsRefusals`; Preview.test.tsx; docs.spec.ts and authoring.spec.ts (zero CSP violations) | P |
| §10 Testing: Go, web and end-to-end as listed | `go test -race ./...`; `npm test`; `make local-check` | P |

## Deviations (accepted)

Every one of these was ruled on in a milestone ledger (`.superpowers/sdd/*/progress.md`) or in the M7 coverage audit.

**Data model (§13)**
- `teams_cache`, `programs_cache`, `content_versions` and `enrollments` are the in-memory `gitsync.State`, rebuilt from git on start; old SHAs reload from the mirror. **Superseded by M8a in Postgres mode:** teams, programs and enrollments are rows behind `internal/org` and the snapshot is built from them; the in-memory state remains for git mode.
- `scores` is folded into `submissions` (M5); `lab_requests`/`approvals` and `cost_samples` into `lab_instances` (M3, M6).
- No `notifications` table: the outbox is River `notify_*` jobs plus `notification_mutes`; in-app notification history is not built.

**Config and content (§4, §5, §6)**
- Webhooks live per team in `team.yaml` (as §10 says), not in platform.yaml (**superseded in Postgres mode:** `team_webhooks` rows); the snapshot bucket is a Helm/terraform value.
- Per-training sub-budgets are the program file's `budget_usd_month`, not a block in budget.yaml.
- `cluster_usd_per_hour` in platform.yaml is the whole cluster rate card (one lab size); unset means cluster labs are unavailable (fail closed), `0` means free on the node.
- The agent signs in with a pairing token (§5.1), not a device code (§8.2's wording).
- One webhook URL, `/api/git/hook`, triggers a sync of every repo.
- Who may propose content edits: admins, the training's maintainers, and leaders/seniors of teams with a program for it, never anyone enrolled. Editable files are `.md/.yaml/.yml/.sh` under `training.yaml` and `modules/<id>/`, up to 20 files of 256 KiB, no deletes or binaries; only lab-directory scripts become executable. Branches are deleted after merge, reject, withdraw or stale.
- A program manager may pin any validated earlier commit (rollback).
- The bootstrap admin is seeded into admins.yaml only while it lists no admin. (**Superseded in Postgres mode by M8a:** seeded into the admins table only while it is empty; the git rule still holds in git mode.)

**Learning and scoring (§7)**
- Scores are final; quiz attempts count across content versions. The escape hatch is the audited admin reset (Anvil for one submission; Journey or `POST /api/admin/quiz-reset` for a trainee's module quiz, including instant-only quizzes) or raising `max_attempts` in git.
- Under the score completion rule an item counts only when it is complete; a review task does not block later lab tasks; a returned review never re-locks tasks already reached; overriding a never-passed auto task marks it passed.
- Turning on review of self-reported results does not reopen completed labs; a whole-lab score wins over later per-task overrides; "return" clears the module's task results but hint charges stay.
- Reading progress is a progress bar; mark-as-read is not scroll-gated.

**Labs (§8)**
- Git sync and lab provisioning are goroutines with idempotent creates and a sweep, not River jobs.
- The lab.yaml egress allowlist is replaced by a deny-private NetworkPolicy (public internet minus RFC1918, link-local, CGNAT; DNS to kube-dns), for lab pods and terraform runner pods alike.
- Cluster checks are tamper-resistant, not tamper-proof: they run server-side via exec in the trainee's `run_in` service.
- One lab size for every cluster lab (500m/1Gi requests, 2 CPU/4Gi limits).
- On kind, lab pods run privileged dind (dev-only flag); sysbox is proven on the AWS node by runbook, which is amd64-only.
- Extension requests do not escalate; a rejected extension uses up the one extension.
- Local labs can reach the internet and `host.docker.internal` (documented in the README); use cluster labs for isolation.
- The newest agent connection wins; a second agent for the same user replaces the first.

**FinOps, deploy and testing (§9, §14)**
- Over-cap requests go to an admin, and the approval is audited as an override; approval authority follows the amount; escalation is sweep-driven; spend is estimate-based until actuals replace it 48 h after the lab ends.
- "Top spenders" on the Ledger is cost data for admins and leaders, not a leaderboard.
- AWS runner pods are one-shot pods, not Jobs; the lab account defaults to the Crucible account; no infracost key or no `aws_regions` makes AWS labs unavailable (fail closed); the reaper deletes only labs that are known and ended over an hour ago.
- Business days are Monday to Friday in UTC, with no holidays or team time zones.
- No LocalStack and no nightly AWS sandbox job: httptest fakes, a dry run in `cluster-check`, and a runbook checklist.
- The AWS sample lab lives in Forge 401, not Forge 101; Forge 101 reaches 100% only with its cluster lab.
- One Crucible deployment per cluster.

## Known decisions to revisit

**One API replica** (`replicas: 1`, `strategy: Recreate`, pinned by deploy/helm/test.sh). Several things assume a single process; each would need a Postgres advisory lock or shared state to scale out, which is not needed below about 100 users:
- the agent hub (live laptop connections) lives in process memory: a shared hub such as Postgres LISTEN/NOTIFY or Redis;
- the content-edit decision lock (edits.go), per-lab setup locks (labs/service.go), per-lab AWS apply locks and credential expiry map (labs/aws.go), and the sweep mutex;
- gitsync keeps every loaded content version in memory (running labs keep their content after a pin bump); it never shrinks;
- River's periodic sweep has no uniqueness options.

**Sizing and simple scans that are fine at today's scale**
- One size for every cluster lab: about 3 memory-safe labs on a t3a.xlarge. Add a per-lab size in lab.yaml if labs need more.
- The infracost estimate caches never shrink (one entry per lab version).
- The Anvil queue filters RBAC in Go over at most 500 rows; the edits history filters after a 200-row window; lab review shows the newest 200 check runs; journey Standing is one query per person and program; budget checks run sequentially over labs. Push into SQL or page them if they grow.
- CloudTrail lookups stop at 20 pages per region per run.

**Behaviour with a known edge**
- Business days have no holidays and no team time zones: add a holiday list to platform.yaml if teams ask.
- A lab counts in the month it was requested; one running across midnight on the 1st stays in the old month.
- Extension requests do not escalate (the lab ends within hours).
- The PTY relay drops output on overflow instead of applying back-pressure: fine for interactive shells, revisit for bulk output.
- Killing a `docker compose exec` or a cluster exec stops the client; the process inside may linger until the lab is destroyed.
- A terminal transcript is saved when the session closes, so an open session is not visible to scorers yet.
- A returned review recomputes on every load until resubmitted.
- A lost submission race leaves its uploaded blobs orphaned (no blob sweep yet).
- The `Disk` blob store is one directory on one node: use S3 wherever the API can lose its disk.
- The lint's code-block detection is a line scanner, not a Markdown parser.
- Unavailable content neither helps nor hurts a forge rank until it syncs again.
- A lab started in the instant between an agent reconnect and its reconcile could be ended too (one round trip).

**Docs & editor**
- The legacy `files` map on `POST /api/edits` is translated to puts by `edits.NewEdit`, and `content_edits.files`
  keeps the puts, for one release. Drop both after it.
- A rename- or delete-only edit stores `files = {}`, so after the 00018 Down the old code would refuse to restore it
  (it wants 1 to 20 files).
- Problem lines are heuristics: `authoring.locate` reads YAML's "line N", a question's or task's `id:`, or the line
  that names a missing file; anything else points at line 1. Give `content.Problem` real positions if authors find
  them wrong.
- A rename that only changes case is refused: the case-collision check sees the old name still at HEAD.
- The AWS settings block (spec §1.3 and §3; plan Ruling 6) was dropped from the catalog (progress ruling P7):
  authors write those keys by hand. Only `template.lab.aws` and "new training" are git-only.
- The AWS lab template and "new training" are git-only blocks. The server's insert refusal for them
  (`gitOnlyWhy` in `internal/content/blocks/catalog.go`) still gives the file-types reason, which is wrong for a new
  training (that is a new repo).
- Draft previews have no asset base, so images in a reading don't show in the editor preview.
- Docker images don't set `CRUCIBLE_VERSION` (no build passes the build arg), so deployed builds show the docs
  version as `dev`.

**Platform**
- **No container registry.** Releases are S3 tarballs imported into containerd. When more nodes are added (M4 upgrade path), switch to ECR with the kubelet credential provider.
- **Identity provider.** AWS uses an invite-only Amazon Cognito user pool, created by M2's persistent stack (guide: `docs/runbooks/cognito.md`). Local development uses Keycloak. Crucible only speaks standard OIDC, so a company identity provider (Entra, Okta, IAM Identity Center) can later be federated into Cognito without code changes.
- **AWS lab account guardrails.** Standalone `CreateNetworkInterface` is denied (a detached ENI gap is documented); S3 permissions have no `s3:ResourceAccount` pin because bucket names are global. A restart in the middle of a destroy is picked up by the sweep after about 70 minutes. The provider is pinned through the deprecated provider-block `version`. Terraform pods do not use `hostUsers: false` yet (kind and Docker Desktop lack user namespaces).
- **Egress by hostname** for lab and runner pods needs Cilium/Calico or a proxy.
