# Rulings made while building M3–M7

Decisions taken during subagent-driven execution (from the per-milestone ledgers). The roadmap's "Deviations (accepted)" section summarises the ones that differ from the spec.

## M3

- accept planner rulings (requests in lab_instances; over-cap → admin approval audited as override; approval authority by amount; sweep-driven escalation; estimate-based spend; newest agent wins; dev-only price env; writable local git mount) — consistent with spec §8.1/§9.1–9.2 — cost if wrong: rework in M6 for spend actuals
- accept deferrals (inline schedules, extension re-approval, budget timer limit, River provisioning, in-app notifications) — keep M3 shippable — cost if wrong: features land in later milestones
- River API written from memory in plan — implementers fix to the real API (go get works here) — cost: none
- Task 1: Ruling: fix Critical multi-doc YAML bypass + Important rw relative binds (require read-only binds for local) + minors (Windows drive-letter src, HOME/USER scrub → HOME=lab dir, drop dead "<<" arm) — trainee laptop safety
- Task 1: Ruling: host-network reachability from local labs (host.docker.internal/gateway) accepted and documented in the content-author docs/README — labs need egress; stricter isolation via cluster runtime (M4) — cost if wrong: a malicious lab could probe the trainee's localhost services
- Task 5: Ruling: carry minors 1 (withdraw race), 2 (approve flag required), 3 (audit in same tx as decision), 4 (don't leak internal errors to trainee) into Task 6's dispatch — cheap, touch the same files — cost: slightly larger Task 6 diff
- Task 7: complete (commits 3368be1..c550398, review clean); Ruling: degenerate-schedule fallback to wall-clock escalation instead of Unavailable is intended — cost: none
- kill switch read via GET /api/kill-switch (plan's admin GET route never existed)
- deferred M3 minors 3 (config audit not atomic with git commit; trailer records actor), 7 (running labs keep old end on schedule change), 8 (non-admin→admin cap hand-off not audited)
- over-cap hand-off UPDATE stays on the pool, not tx — Decide returns Conflict and rolls back the tx, so the hand-off must persist outside it.
- no concurrent kill-switch race test; FOR SHARE vs FOR UPDATE serialization verified by review.

## M4

- separate `make cluster-check` (kind ≥ v0.24 in .local/tools); local-check stays kind-free (--project=local)
- on kind, lab pods run privileged dind behind dev-only CRUCIBLE_CLUSTER_PRIVILEGED=1; sysbox proven only on AWS node by a human via runbook
- lab.yaml egress allowlist not implemented (NetworkPolicy can't match hostnames); egress = 0.0.0.0/0 minus link-local/RFC1918/CGNAT, DNS to kube-dns
- provisioning stays a goroutine (idempotent creates + sweep), not River
- cluster labs priced by FixedRates (default $0) until M6 rate card; fixed lab size 500m/1Gi req, 2/4Gi limits
- checks run in trainee's run_in service server-side via exec = tamper-resistant, not tamper-proof (spec wording)
- Forge 101 needs a cluster lab for 100% (M4 done-when requires the Forge 101 lab on cluster). CARRY → M7: rank/percent e2e under local-check must not assume Forge 101 can reach 100% without kind.
- ErrImagePull not fail-fast (transient; becomes ImagePullBackOff).
- Task 7: committed f42fc35 (sysbox 0.7.1 amd64 pinned by sha256; brief's URL wrong). Ruling: amd64-only node (t3a); arm64 deb/sha out of scope.
- one Crucible deployment per cluster (documented); no owner label on lab namespaces.
- sysbox always installed (no gate variable) but non-fatal.
- remaining M4 minors fine to leave (reasons in final-review.md).

## M5

- M5 plan defaults accepted (no scores table; scored = final; review task doesn't block later lab tasks; hint costs apply; override sets points; transcripts output-only, visible on close; rubrics mandatory + never sent to trainees; §8.2 self-report review deferred to M7; uploads under s3://<data bucket>/uploads/; new forge-301 fixture).
- orphaned blobs on failed submit accepted until a blob sweep exists (Store has no Delete).
- Task 5: committed 8c64075. CARRY → Task 8: wire labs.Scoring + scoring.Labs in main.go. Ruling: override on a never-passed/skipped auto task marks it passed (plan ruling 5 stands).
- a returned review must NOT re-lock later lab tasks the trainee already reached (consistent with ruling 3) → final wave.
- scores stay final (I4) — warn on the Anvil form; admin reset → M7 candidate.

## M6

- M6 plan defaults accepted (one-shot pods not Jobs; no LocalStack — httptest fakes + runbook sandbox checklist; AWS e2e in cluster-check dry-run; extension-pending → M7; lab account = crucible account by default; workspace image amazon/aws-cli; actuals replace estimate 48h after end; no infracost key → AWS labs unavailable; Forge 401 fixture; reaper deletes only known-ended labs >1h).
- Tasks 3+4: complete (2562599, a8cd51e; review Approved, 0 Critical/Important). Ruling: no tag re-check inside Delete (IAM tag condition + bucket-name scoping is the control).
- no FQDN egress allowlist for runner pods (needs Cilium/Calico or proxy); egress = public internet minus private/IMDS ranges, same as lab pods; runbook note.
- Task 6: review Needs fixes (1 Important: terraform colour/control codes in termination message). Fix round 1 dispatched (+ digest pin, CHECKPOINT_DISABLE, kill reason, volume assertions). Ruling: hostUsers:false on tf pods deferred (kind/Docker Desktop userns).
- lint parser work is now bounded per file by size (128 KiB), tokens (50k), depth (64), unary runs (64), openers (4000); parsing is sequential so peak ≈ one file. No further re-review round; covered by the M6 final review.
- Task 5: committed 4c3f6f3 (infracost: --no-cache, temp copy, env allowlist, 2m timeout, output caps; not wired in crucible-api until the runner task). Ruling: empty AWSRegions blocks all aws labs (fail closed).
- restart mid-destroy picked up by the sweep after 70 min — acceptable for M6 (MaxHourlyUSD caps cost); upgrade path owner/heartbeat column; runbook line.
- labs must not share the VPC default SG — deny RunInstances/CreateNetworkInterface on security groups with no crucible:lab-id tag (labs create their own SG) → M6 final wave.
- keep IMDS hop limit 2 (crucible-api pod needs the node role for S3/STS); instead add an IMDS-guard initContainer to every lab pod (cluster dind + aws workspace) → M6 final wave (also covers M4 cluster labs).
- provider pin via provider-block version (deprecated but works; author required_providers allowed) — revisit if terraform drops it.

## M7

- M7 plan defaults accepted (who may propose edits: admins, training maintainers, team leaders/seniors — never anyone enrolled in that training; edit branches deleted after merge/reject/withdraw/stale; editable .md/.yaml/.yml/.sh only, ≤20 files ≤256 KiB, no deletes/binaries; extension requests don't escalate; rejected extension uses the one extension; self-reported review "return" clears module task results, hint charges stay; crucible preview needs Docker + locally built image; reading progress bar, mark-as-read not scroll-gated; business days Mon–Fri UTC; catalog shows titles/descriptions to all; Hearth % becomes weighted).
- under the score completion rule an item's score counts only when the item is complete.
- quiz attempt limits are counted across content versions (no reset on new version); admin reset (scores + attempts) is the escape hatch → candidate for M7 Task 21 audit / gap fix.
- final_hint and returned_twice clear once the task passes / the item is scored (like failed_checks).
- Tasks 5+7: review Approved with fixes (Important: HeatMap cell labels hover-only + tab stop per cell). Queued web fix (after M6 verification): visible/focus labels or text module names, drop per-cell tabIndex; aria-hidden ⚠ glyph; banner via persistent empty role=status + focus restore on dismiss; rank-up flex-wrap + nav wrap at 320px; heading order; Mentor key email+team; shimmer not looping on focus; anvil-theme contrast for heat glyphs; soften Journey empty state. Ruling: no global view-progress flag on /api/me.
- Merge must take the head sha reviewed at push time and refuse if the edit branch moved (compare-and-merge) — add in the Task 10 fix round or Task 11.
- editable paths limited to training.yaml and modules/<id>/…, no dot components, safe charset, case-collision check, non-executable unless a lab script location.
- Task 9: committed 0bbe848 (whole-lab Anvil submission for local labs when review_self_reported; Return clears module task results, hints stay; migration 00015; settleLab; lab_review in view). Rulings: turning the flag on doesn't reopen completed labs; whole-lab score wins over later per-task overrides.
- proposers = admins, the training's maintainers, leaders/seniors of teams that have a program for that training; never enrolled users.
- ManageProgram may pin any validated earlier commit (rollback intended).
- Task 19: committed 00f9863. Ruling: bootstrap admin seeds admins.yaml (via Writer, re-checked at tip) only while it lists no admins; ignored after; audited admin.bootstrap. Revoke: own tokens only; Hub.Drop closes websocket 4001.
- admins can revoke any user's agent tokens (offboarding), audited, drops live connections.
- build the cluster-lab rate card (§9.1) — no "free cluster labs".
- accept new deviations — sub-budgets in program files; agent pairing token (not device code); River jobs + mutes as notification outbox (no notifications table); single /api/git/hook URL for all repos.
- Origin check AND CSP are both Must.
- build admin reset of a trainee's quiz attempts/scores in M7.
- enrolled users never score or see Anvil data for a training they're enrolled in — admins included.
- admin reset keyed by person+module (clears instant attempts) — built now.
- enrolled non-owners can't download peers' uploads either (stricter than I1; consistent with "enrolled never see peers' answers") — accepted.
