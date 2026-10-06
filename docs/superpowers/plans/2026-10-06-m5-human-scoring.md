# M5 Human Scoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A trainee answers `text`, `upload` and `signoff` questions and submits `human_review` lab tasks; a scorer grades them in the **Anvil** queue (rubric, points, written feedback, return for rework, live sign-offs, audited overrides of auto-checks, terminal transcripts and uploaded files as evidence); the trainee sees the feedback, and linear progression waits for pending human scores.

**Architecture:** A new `internal/scoring` package owns the `submissions` table and every human decision (submit, score, return, sign off, override, queue, detail, file download). It imports neither `learn` nor `labs`: those two call `scoring.Service.Submit` and implement small interfaces (`scoring.Progress`, `scoring.Labs`) that scoring calls back after a decision to recompute the trainee's item. A new `internal/blob` package stores uploads and transcripts behind a two-method `Store` interface: a directory on local disk, or S3 (aws-sdk-go-v2, tested only against an `httptest` fake). The lab terminal bridge keeps the last 512 KiB of each session's output and saves it as a transcript when the session closes. The SPA gets human-question inputs on the quiz page, a review form on lab tasks, feedback boxes, and the Anvil page.

**Tech Stack:** Go 1.26, pgx v5, goose, `github.com/aws/aws-sdk-go-v2/{config,service/s3}` (new), `mime/multipart`, React 19 + Vite, `@xterm/xterm` (already installed) for read-only transcripts, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-05-crucible-design.md` (§4.4 human question types and `rubric`, §4.5 `human_review` tasks, §5.2–5.3 scorer role, "nobody … scores their own submission", "A trainee sees only their own scores", §7 human scoring + progression, §8.4 hint costs, §10 "submission awaiting scoring; scored/returned", §12 navigation "Anvil (scoring queue)", §13 `submissions`, `scores`, `terminal_transcripts`, "Files (uploads, transcripts, snapshots) → S3-compatible object storage", §14 security and "Playwright e2e for … scoring queue"). Program context: `.superpowers/sdd/program-context.md`.

## Global Constraints

- Every shell starts with `export PATH=/Users/adelin/Projects/Crucible/.local/tools/go/bin:/Users/adelin/Projects/Crucible/.local/tools:$PATH` (go1.26.8). Never install anything system-wide.
- NEVER touch real AWS: no terraform plan/apply against AWS, no aws CLI calls, no docker push. The S3 store is tested only against an `httptest` server; `terraform test` uses the existing `mock_provider`.
- `gofmt -l .` prints nothing, `go vet ./...` is clean, `go test -race ./...` passes (Docker Desktop must run: dbtest uses testcontainers Postgres 18).
- Acceptance: `KEYCLOAK_PORT=8082 make local-check` ends with `🔥 Local check passed. The forge holds.` (run at the end of Task 11; Task 10 uses it with `KEEP=1` for a visual check; it tears the stack down unless `KEEP=1`).
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Start after M4 is merged on `feat/m3-m7`.** M4 edits `internal/labs/service.go`, `internal/content`, `cmd/crucible-api/main.go`, `deploy/helm/crucible/templates/crucible.yaml`, `deploy/aws/main/*`, `scripts/local-check.sh`, `e2e/playwright.config.ts` and adds `examples/forge-101/modules/03-cluster-heat`. M5 touches several of the same files; rebase onto M4 first and keep M4's additions intact.
- **Migration number:** M3 used up to `00006`; M4 may add one. Run `ls internal/db/migrations` and name the new file with the next free number. This plan calls it `000NN_scoring.sql`. Never edit an existing migration.
- Spec §5.3: "nobody approves their own lab request or scores their own submission. A trainee sees only their own scores; mentors see their mentees." Use `rbac.Score` (it already refuses actor == subject) and `rbac.ViewProgress`.
- **Rubrics are answer keys.** Like quiz answers, check scripts and hint text, a rubric never reaches a trainee's browser. Trainee JSON carries `scoring.Feedback`, which has no rubric field.
- **Score privacy:** scored/returned notifications go to the trainee only (`Event.Team` stays empty, so nothing is posted to team Slack/Teams). Only "waiting for a scorer" (no points) may go to the team webhook.
- Emails are lowercased wherever stored or compared. Text written to Postgres goes through `scoring.Clean` (valid UTF-8, no NUL bytes).
- Uploads are always served as `Content-Disposition: attachment`, `Content-Type: application/octet-stream`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`. No inline preview.
- Multipart endpoints require the header `X-Crucible-Upload: 1` (a cross-site HTML form cannot set it).
- UI: themes forge|anvil|quench|contrast via existing tokens; no new animation that ignores `useCalm`; no leaderboards or cross-trainee comparisons (the queue lists submissions, never rankings).
- `e2e/tests/forge-101.spec.ts`, `e2e/tests/approvals.spec.ts` and M4's cluster e2e pass unchanged.

## Rulings made in this plan (read before starting)

1. **One table, `submissions`, also holds the score.** Spec §13 lists `submissions` and `scores`; a human score is one decision on one submission, so `status/points/feedback/scored_by/scored_at` are columns. Return-for-rework keeps the old row (`status = returned`) and the trainee's next answer is a new row, so history is free. Auto-check overrides are audit entries plus a `lab_events` row (rule 6). No `scores` table.
2. **A scored submission is final.** Only `pending` rows can be decided (`UPDATE … WHERE status = 'pending'`), so two scorers can never both win and nobody re-scores. A partial unique index allows one `pending` or `scored` row per trainee × item.
3. **Human questions are answered one at a time**, each with its own "Submit for scoring" button, separate from the instant questions' "Submit answers". The quiz item is `complete` when there is an instant attempt (if the quiz has instant questions), every human question is scored, and (best instant points + human points) / total ≥ `pass_threshold`. It is `pending_review` while the only things missing are a scorer's decisions (pending answers and not-yet-given sign-offs), otherwise `in_progress`. `pending_review` is not `complete`, so `progression: linear` keeps the next module locked (spec §7).
4. **Inside a lab, a submitted review task does not block later tasks** (status `submitted`; with `task_order: linear` the next task opens). Making a trainee wait hours with a TTL running would be wrong. The **lab item** is `pending_review` until the scorer decides, so module progression still waits.
5. **Hint costs apply to review tasks too:** awarded = max(0, scorer's points − hint costs), computed by labs. The Anvil detail shows the hint cost so the scorer knows.
6. **Overrides** (spec §7: "scorers can override auto-check results with a reason (audited)") are made from a lab submission's detail page, on any non-review task of that module: the scorer sets the awarded points (0 … task points), the task becomes `passed`, the lab item's score is recomputed (it may go down), and `score.override` with the reason and the previous points is written to `audit_log` in the same transaction. The trainee sees the new points, not the reason.
7. **Sign-offs** are created by a scorer from the Anvil "Live sign-offs" list (one row per enrolled trainee × `signoff` question not yet signed off) as an already-`scored` submission with the scorer's notes as feedback.
8. **Transcripts** record terminal *output* (which includes echoed keystrokes), keep the last 512 KiB per terminal session, and are saved when the session closes. A session still open is not visible to scorers yet (`// ponytail:` note). The lab header tells trainees their terminal output is recorded.
9. **Blob storage:** `blob.Disk` under `$CRUCIBLE_DATA_DIR/blobs` when `CRUCIBLE_BLOB_BUCKET` is unset (local dev and compose, where `/data` is a named volume); `blob.S3` otherwise. In AWS the chart points it at the existing data bucket (`backup.bucket`) under `uploads/`, which has no lifecycle rule and survives teardown. The node role gets `s3:PutObject` on `uploads/*` (it already has `GetObject` on the bucket). Keys are server-generated (`uploads/files/<32 hex>`, `uploads/transcripts/<lab>/<12 hex>`); the store rejects any other shape.
10. **Limits:** at most 5 files × 20 MiB per submission, answers ≤ 20 000 characters, feedback ≤ 5 000, override reasons ≤ 500.
11. **Rubrics are required** for `text` and `upload` questions and for `human_review` tasks (lint error otherwise); `signoff` rubrics are optional. A `human_review` task must not also have `check` or `quiz`.
12. **Fixture:** a new training `examples/forge-301` ("Under the Hammer", linear) with a quiz (single + text + upload + signoff) and a local lab (one checked task + one review task). It is registered in `examples/platform/trainings.yaml` but **not enrolled** in git: the e2e enrols the trainee through the UI like the approvals e2e does, so Go tests that load `examples/platform` keep their enrolment counts. Forge 101 is not touched (M4 is adding its module 03).
13. **Who is told:** a new submission notifies the program's scorers (falling back to admins when a program has none) by email and posts "waiting on the anvil" to the team webhook; a decision notifies the trainee by email only. New mutable kinds: `submission_pending`, `submission_scored`.
14. **Deferred:** spec §8.2 "a program can require human review for [self-reported] results" (a program flag that turns every completed local lab into a submission) is not built in M5; it is listed for the M7 coverage pass.

## Review Focus

1. **Two scorers press Score on the same submission at the same moment, or a trainee resubmits while their answer is still pending.** Exactly one decision is stored; the other person gets "someone already scored or returned this submission" (409) and a resubmission gets "this answer is already with a scorer". Pinned by `TestOnlyOneScorerWins` and `TestResubmitWhilePendingIsRefused` (Task 3).
2. **A hostile upload or link:** a file named `..\..\evil.html` containing `<script>`, a 21 MiB file, six files, or a link `javascript:alert(1)`. The name is stored as `evil.html`, the file is only ever served as an attachment with `nosniff` and `sandbox`, oversize/too many files get a 400 (never a 500 or memory blow-up), and the link is refused. Pinned by `TestFileNamesAreSanitised`, `TestReadFormLimits` (Task 3), `TestUploadLinkMustBeHTTP` (Task 4) and `TestDownloadIsAnAttachmentForViewersOnly` (Task 7).
3. **Someone who is both a scorer and a trainee of the same program, or a stranger with a guessed id.** Their own submission never appears in their queue and cannot be scored; a stranger gets 404 for another trainee's files and transcripts. Pinned by `TestScoringRules` (Task 3), `TestQueueIsScopedToScorers` (Task 7) and `TestTranscriptOnlyForViewers` (Task 6).
4. **A linear training with an answer still pending, or returned for rework.** The next module stays locked until every human item is scored and the threshold is met; a returned answer re-opens only that question; an all-human quiz needs no "Submit answers" click. Pinned by `TestQuizOutcome` and `TestHumanQuestionsHoldTheModuleUntilScored` (Task 4), `TestReviewTaskWaitsForScorer` (Task 5).
5. **Score privacy and answer keys.** Rubric text never appears in any trainee JSON (quiz view, lab view, feedback), and scored/returned notifications never go to a team webhook. Pinned by `TestTraineeNeverSeesRubric` (Task 4), the rubric assertion in `TestReviewTaskWaitsForScorer` (Task 5) and the `Team == ""` assertion in `TestScoringRules` (Task 3).

---

## File Structure

```
examples/forge-301/**                                   Task 1  fixture training: quiz with text/upload/signoff, lab with a review task
examples/platform/trainings.yaml                        Task 1  register forge-301 (not enrolled)
internal/content/types.go, load.go, load_test.go        Task 1  Task.Rubric; lint rules for human items
go.mod, go.sum                                          Task 2  aws-sdk-go-v2 config + s3
internal/blob/blob.go, blob_test.go                     Task 2  Store, Disk, S3
internal/db/migrations/000NN_scoring.sql                Task 3  submissions, terminal_transcripts
internal/notify/notify.go                               Task 3  SubmissionPending, SubmissionScored kinds
internal/scoring/scoring.go                             Task 3  types, Submit, Latest, Score, Return, SignOff, CanView, notifications
internal/scoring/form.go                                Task 3  ReadForm (multipart, limits, X-Crucible-Upload)
internal/scoring/scoring_test.go                        Task 3
internal/learn/quiz.go, quiz_test.go                    Task 4  quizOutcome; Score no longer decides Passed alone
internal/learn/service.go, http.go                      Task 4  AnswerHuman, refreshQuiz, Refresh, ForceScore, submissions on the quiz view
internal/learn/human_test.go                            Task 4
internal/labs/service.go, http.go                       Task 5  submitted status, recompute, review in TaskView, routes
internal/labs/review.go, review_test.go                 Task 5  SubmitReview, Refresh, Override, Evidence
internal/labs/transcript.go, transcript_test.go         Task 6  tail recorder, saveTranscript, Transcript download
internal/labs/http.go                                   Task 6  record terminal output; transcript route
internal/scoring/anvil.go, http.go, anvil_test.go       Task 7  Queue, Detail, SignOffs, Override, File, Routes
cmd/crucible-api/main.go                                Task 8  blob store + scoring wiring
internal/httpapi/server.go                              Task 8  Deps.Scoring, can_score on /api/me
deploy/helm/crucible/templates/crucible.yaml, test.sh   Task 8  CRUCIBLE_BLOB_BUCKET/REGION
deploy/aws/main/main.tf, main.tftest.hcl                Task 8  PutObject on uploads/*
docs/runbooks/aws.md                                    Task 8  where uploads live
web/src/types.ts, api.ts                                Task 9  new types, upload()
web/src/components/Feedback.tsx                         Task 9  FeedbackBox
web/src/pages/Quiz.tsx, Training.tsx, Lab.tsx           Task 9  human questions, pending badge, review form, transcript notice
web/src/theme/app.css                                   Tasks 9, 10
web/src/components/Transcript.tsx                       Task 10 read-only xterm
web/src/pages/Anvil.tsx, App.tsx, components/Nav.tsx    Task 10 queue, detail, sign-offs, overrides
scripts/seed-git.sh, scripts/local-check.sh             Task 11 seed + lint forge-301
e2e/tests/scoring.spec.ts                               Task 11
```

Dependencies: Tasks 1 and 2 stand alone. Task 3 needs 2. Tasks 4 and 5 need 1 and 3. Task 6 needs 5. Task 7 needs 3–6. Task 8 needs 7. Task 9 needs 8. Task 10 needs 9. Task 11 needs 10.

---

### Task 1: Content: rubrics, lint rules for human items, the Forge 301 fixture

**Files:**
- Modify: `internal/content/types.go` (add `Rubric` to `Task`)
- Modify: `internal/content/load.go` (quiz `case "text", "upload", "signoff":` and the lab task checks near `needs a check, a quiz or human_review`)
- Modify: `internal/content/load_test.go`
- Create: `examples/forge-301/training.yaml`
- Create: `examples/forge-301/modules/01-temper/{module.yaml,quiz.yaml}`
- Create: `examples/forge-301/modules/02-review-lab/module.yaml`
- Create: `examples/forge-301/modules/02-review-lab/lab/{lab.yaml,compose.yaml}`
- Create: `examples/forge-301/modules/02-review-lab/lab/tasks/{01-light.md,02-proof.md}`
- Create: `examples/forge-301/modules/02-review-lab/lab/checks/01-light.sh` (mode 0755)
- Modify: `examples/platform/trainings.yaml`

**Interfaces:**
- Produces: `content.Task.Rubric string` (yaml `rubric`). Fixture ids used by every later task: training `forge-301`; module `01-temper` with questions `q-quench` (single, answer 0 "Hardens it", 1 pt), `q-why` (text, 5 pts), `q-log` (upload, 2 pts), `q-demo` (signoff, 3 pts), `pass_threshold: 0.6`; module `02-review-lab` with lab `review-heat` (runtime local, terminal `shell`), tasks `t1-light` (check, 2 pts, output `The forge is lit.`) and `t2-proof` (human_review, 3 pts). Task titles: `Light the forge`, `Prove your work`.

- [ ] **Step 1: Write the failing tests**

Append these cases to the `cases` map in `TestLoadProblems` (`internal/content/load_test.go`):

```go
		"text needs rubric":        {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: text, prompt: Why?}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "needs a rubric"},
		"upload needs rubric":      {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: upload, prompt: Attach}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "needs a rubric"},
		"review needs rubric":      {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    check: {script: checks/t1.sh, run_in: box}\n", "    human_review: true\n", 1)}, "human_review tasks need a rubric"},
		"review with check":        {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    points: 2\n", "    points: 2\n    human_review: true\n    rubric: R\n", 1)}, "scored by a person: drop check and quiz"},
		"rubric on a checked task": {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    points: 2\n", "    points: 2\n    rubric: R\n", 1)}, "rubric is only read for human_review tasks"},
```

Append a positive test:

```go
func TestHumanItemsLoad(t *testing.T) {
	tr, probs := Load(tree(t, map[string]string{
		"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: text, prompt: Why?, rubric: Mentions X, points: 5}\n" +
			"  - {id: q3, type: signoff, prompt: Demo}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n",
		"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    check: {script: checks/t1.sh, run_in: box}\n",
			"    human_review: true\n    rubric: The transcript shows it\n", 1),
	}))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if got := tr.Modules[0].Lab.Task("t1").Rubric; got != "The transcript shows it" {
		t.Fatalf("task rubric = %q", got)
	}
}

func TestForge301Loads(t *testing.T) {
	tr, probs := Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatalf("forge-301: %v", probs)
	}
	q := tr.Module("01-temper").Quiz
	if q.Question("q-why").Type != "text" || q.Question("q-log").Type != "upload" || q.Question("q-demo").Type != "signoff" {
		t.Fatal("forge-301 must have text, upload and signoff questions")
	}
	if lab := tr.Module("02-review-lab").Lab; !lab.Task("t2-proof").HumanReview || lab.Task("t1-light").Check == nil {
		t.Fatal("forge-301's lab needs one checked task and one review task")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/content/ -run 'TestLoadProblems|TestHumanItemsLoad|TestForge301Loads' -v`
Expected: FAIL (`unknown field "rubric"` on the task, missing problems, and `examples/forge-301` does not exist).

- [ ] **Step 3: Implement the schema and lint rules**

In `internal/content/types.go`, add to `Task` after `HumanReview`:

```go
	Rubric       string  `yaml:"rubric"` // human_review only: what the scorer looks for; never sent to trainees
```

In `internal/content/load.go`, replace the quiz case `case "text", "upload", "signoff":` with:

```go
		case "text", "upload":
			if strings.TrimSpace(x.Rubric) == "" {
				bad("needs a rubric: scorers grade against it")
			}
		case "signoff":
```

In `lab()`, directly after the existing `if t.Check == nil && t.Quiz == "" && !t.HumanReview {…}` block, add:

```go
		if t.HumanReview && (t.Check != nil || t.Quiz != "") {
			l.add(lf, "%s: human_review tasks are scored by a person: drop check and quiz", where)
		}
		if t.HumanReview && strings.TrimSpace(t.Rubric) == "" {
			l.add(lf, "%s: human_review tasks need a rubric for the scorer", where)
		}
		if !t.HumanReview && t.Rubric != "" {
			l.add(lf, "%s: rubric is only read for human_review tasks", where)
		}
```

(`strings` is already imported by `load.go`; if not, add it.)

- [ ] **Step 4: Create the Forge 301 fixture**

`examples/forge-301/training.yaml`:

```yaml
id: forge-301
title: "Forge 301: Under the Hammer"
description: Answers a person reads and a lab task a person reviews. The local check uses it to prove human scoring end to end.
maintainers: [senior@crucible.local]
progression: linear
modules: [01-temper, 02-review-lab]
```

`examples/forge-301/modules/01-temper/module.yaml`:

```yaml
title: "Temper"
items:
  - quiz: quiz.yaml
```

`examples/forge-301/modules/01-temper/quiz.yaml`:

```yaml
pass_threshold: 0.6
questions:
  - id: q-quench
    type: single
    prompt: What does quenching do to hot steel?
    options: ["Hardens it", "Melts it", "Paints it"]
    answer: 0
  - id: q-why
    type: text
    prompt: In two sentences, why do we temper steel after quenching?
    rubric: "Full marks: quenched steel is hard but brittle, and tempering trades a little hardness for toughness. Half marks for 'less brittle' alone."
    points: 5
  - id: q-log
    type: upload
    prompt: Attach the log of your last forge run (a file or a link).
    rubric: "A log file or a link to one that shows a full heat cycle. Return screenshots: we want text we can search."
    points: 2
  - id: q-demo
    type: signoff
    prompt: Show a scorer how you would recover a cracked blade (live demo).
    points: 3
```

`examples/forge-301/modules/02-review-lab/module.yaml`:

```yaml
title: "Under Review"
items:
  - lab: lab
```

`examples/forge-301/modules/02-review-lab/lab/lab.yaml`:

```yaml
id: review-heat
runtime: local
ttl: 1h
idle_timeout: 20m
terminals:
  - { name: shell, service: shell }
tasks:
  - id: t1-light
    instructions: tasks/01-light.md
    check: { script: checks/01-light.sh, run_in: shell }
    points: 2
  - id: t2-proof
    instructions: tasks/02-proof.md
    human_review: true
    rubric: "The transcript shows /tmp/proof being written with the word tempered, and the notes say what was done."
    points: 3
```

`examples/forge-301/modules/02-review-lab/lab/compose.yaml`:

```yaml
services:
  shell:
    image: alpine:3.22
    command: ["sleep", "infinity"]
```

`examples/forge-301/modules/02-review-lab/lab/tasks/01-light.md`:

```markdown
# Light the forge

Run `touch /tmp/lit` in the **shell** terminal, then press **Check**.
```

`examples/forge-301/modules/02-review-lab/lab/tasks/02-proof.md`:

```markdown
# Prove your work

Write the word `tempered` into `/tmp/proof` in the **shell** terminal and print it back.
Then tell the scorer what you did in the notes and press **Submit for review**.
A scorer reads your notes and your terminal transcript.
```

`examples/forge-301/modules/02-review-lab/lab/checks/01-light.sh` (then `chmod 755`):

```sh
#!/bin/sh
if [ -f /tmp/lit ]; then
  echo "The forge is lit."
else
  echo "No fire yet: /tmp/lit is missing."
  exit 1
fi
```

Append to `examples/platform/trainings.yaml`:

```yaml
  forge-301:
    repo: file:///git/forge-301.git   # human-scoring e2e fixture; the e2e enrolls the trainee through the UI
    branch: main
```

- [ ] **Step 5: Run the tests and lint**

Run: `go test ./internal/content/ ./internal/config/ ./internal/rbac/ ./internal/configapi/ -v -run 'Load|Forge|Enroll' && go run ./cmd/crucible lint examples/forge-301 && go run ./cmd/crucible lint examples/platform`
Expected: PASS; both lints report no problems.

- [ ] **Step 6: Commit**

```bash
git add internal/content examples/forge-301 examples/platform/trainings.yaml
git commit -m "feat(content): rubrics for human items and the Forge 301 scoring fixture

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Blob storage: local disk and S3

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/blob/blob.go`
- Create: `internal/blob/blob_test.go`

**Interfaces:**
- Produces:
  ```go
  package blob
  type Store interface {
      Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
      Get(ctx context.Context, key string) (io.ReadCloser, error) // apperr.NotFound when missing
  }
  type Disk struct{ Dir string }
  type S3 struct{ Client *s3.Client; Bucket string }
  func NewS3(ctx context.Context, bucket, region, endpoint string) (*S3, error)
  ```
  Keys match `^uploads/[a-z0-9/_-]+$` without `//`; anything else is an error.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/aws/aws-sdk-go-v2/config@latest github.com/aws/aws-sdk-go-v2/service/s3@latest github.com/aws/aws-sdk-go-v2@latest`
Expected: `go.mod` lists the three modules.

- [ ] **Step 2: Write the failing tests**

Create `internal/blob/blob_test.go`:

```go
package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"crucible/internal/apperr"
)

func roundTrip(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	body := []byte("heat 1200C\nquench\n")
	if err := s.Put(ctx, "uploads/files/0a1b2c", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, "uploads/files/0a1b2c")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("got %q", got)
	}
	if _, err := s.Get(ctx, "uploads/files/missing"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("missing key: %v", err)
	}
	for _, bad := range []string{"../etc/passwd", "uploads/../x", "uploads//x", "/uploads/x", "snapshots/x", "uploads/x.html", "uploads/X"} {
		if err := s.Put(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
}

func TestDisk(t *testing.T) { roundTrip(t, Disk{Dir: t.TempDir()}) }

// fakeS3 is just enough of the S3 REST API for PutObject and GetObject (path-style).
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []*http.Request
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = b
		f.puts = append(f.puts, r)
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>gone</Message></Error>`)
			return
		}
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestS3AgainstFake(t *testing.T) {
	// Never reach real AWS: static fake credentials, no IMDS, no shared config.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAFAKE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s, err := NewS3(context.Background(), "crucible-data", "eu-west-1", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.puts) != 1 {
		t.Fatalf("puts = %d", len(fake.puts))
	}
	p := fake.puts[0]
	if p.URL.Path != "/crucible-data/uploads/files/0a1b2c" {
		t.Fatalf("path %q (want path-style bucket/key)", p.URL.Path)
	}
	if p.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" || !strings.HasPrefix(p.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		t.Fatalf("headers: %v", p.Header)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/blob/ -v`
Expected: FAIL (`undefined: Disk`, `undefined: NewS3`).

- [ ] **Step 4: Implement**

Create `internal/blob/blob.go`:

```go
// Package blob stores uploaded files and terminal transcripts (spec §13: "Files … → S3-compatible object storage"):
// S3 in production, a directory locally.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"crucible/internal/apperr"
)

type Store interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Keys are generated by the server; anything else is a bug or an attack.
var validKey = regexp.MustCompile(`^uploads/[a-z0-9_-]+(/[a-z0-9_-]+)*$`)

func check(key string) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("blob: invalid key %q", key)
	}
	return nil
}

var notFound = apperr.Wrap(apperr.NotFound, "file not found")

// Disk keeps blobs under Dir. ponytail: one directory on one node; use S3 when the API runs anywhere it can lose its disk.
type Disk struct{ Dir string }

func (d Disk) Put(_ context.Context, key string, body io.ReadSeeker, _ int64) error {
	if err := check(key); err != nil {
		return err
	}
	p := filepath.Join(d.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after the rename
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

func (d Disk) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := check(key); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(d.Dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, notFound
	}
	return f, err
}

type S3 struct {
	Client *s3.Client
	Bucket string
}

// NewS3 uses the default AWS credential chain (the node's instance role in production). endpoint is for tests and
// S3-compatible stores; it switches to path-style addressing.
func NewS3(ctx context.Context, bucket, region, endpoint string) (*S3, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &S3{Bucket: bucket, Client: s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint, o.UsePathStyle = aws.String(endpoint), true
		}
		// No flexible checksums: bodies are seekable and SigV4 signs the payload hash; trailers break plain-HTTP fakes.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})}, nil
}

func (s *S3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	if err := check(key); err != nil {
		return err
	}
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: body,
		ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
		ServerSideEncryption: types.ServerSideEncryptionAes256})
	return err
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := check(key); err != nil {
		return nil, err
	}
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/blob/ -v && go vet ./internal/blob/`
Expected: PASS. If the fake sees a chunked or `aws-chunked` body, the checksum options above were not applied.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/blob
git commit -m "feat(blob): disk and S3 stores for uploads and transcripts

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: Submissions: table, submit, score, return, sign off, notifications

**Files:**
- Create: `internal/db/migrations/000NN_scoring.sql` (next free number, see Global Constraints)
- Modify: `internal/notify/notify.go` (two kinds)
- Create: `internal/scoring/scoring.go`
- Create: `internal/scoring/form.go`
- Create: `internal/scoring/scoring_test.go`

**Interfaces:**
- Consumes: `blob.Store` (Task 2); `audit.Log`; `rbac.Checker`; `notify.Event`; `gitsync.State`.
- Produces (package `scoring`, used by Tasks 4–8):
  ```go
  const (KindQuestion = "question"; KindTask = "task"; Pending = "pending"; Scored = "scored"; Returned = "returned")
  const (MaxFiles = 5; MaxFileBytes = 20 << 20)
  type File struct{ Name string `json:"name"`; Size int64 `json:"size"` }
  type Submission struct {
      ID int64; UserID int64; Email, Name, Team, Training, Module, SHA, Kind, Item, LabID string
      QType, Prompt, Rubric string; MaxPoints float64; Answer string; Files []File; Keys []string // json:"-"
      Status string; Points float64; Note, ScoredBy string; ScoredAt *time.Time; CreatedAt time.Time
  } // Note is the feedback text. JSON: id trainee trainee_name team training module kind item lab_id type prompt rubric max_points answer files status points feedback scored_by scored_at created_at
  type Feedback struct{ ID int64; Status, Answer string; Files []File; Points, Max float64; Feedback, ScoredBy string } // JSON max_points; no rubric
  func (x *Submission) Feedback() *Feedback
  type Progress interface{ Refresh(ctx context.Context, sub *Submission) error }
  type Labs interface {
      Progress
      Evidence(ctx context.Context, labID string) (*LabEvidence, error)
      Override(ctx context.Context, labID, task string, points float64, record func(context.Context, pgx.Tx, float64) error) error
  }
  type LabEvidence, TaskEvidence, CheckRun, Transcript // see scoring.go below
  type Notifier interface{ Notify(ctx context.Context, ev notify.Event) error }
  type Service struct{ DB *pgxpool.Pool; Blobs blob.Store; State func() *gitsync.State; Notify Notifier; Quiz Progress; Labs Labs; Log *slog.Logger; Now func() time.Time }
  func (s *Service) Submit(ctx context.Context, u *auth.User, sub *Submission, files []*multipart.FileHeader) (*Submission, error)
  func (s *Service) Latest(ctx context.Context, userID int64, team, training, module, kind string) (map[string]*Submission, error)
  func (s *Service) Score(ctx context.Context, u *auth.User, id int64, points float64, feedback string) (*Submission, error)
  func (s *Service) Return(ctx context.Context, u *auth.User, id int64, feedback string) (*Submission, error)
  type SignOffInput struct{ Team, Training, Module, Question, Trainee string; Points *float64; Notes string }
  func (s *Service) SignOff(ctx context.Context, u *auth.User, in SignOffInput) (*Submission, error)
  func (s *Service) CanView(u *auth.User, team, training, owner string) bool
  func Clean(s string) string
  func ReadForm(w http.ResponseWriter, r *http.Request) (answer string, files []*multipart.FileHeader, done func(), err error)
  ```
  `notify.SubmissionPending` (`"submission_pending"`) and `notify.SubmissionScored` (`"submission_scored"`).

- [ ] **Step 1: Write the migration**

Create `internal/db/migrations/000NN_scoring.sql`:

```sql
-- +goose Up
-- Human-scored work (spec §7, §13). A row is one answer, review-task submission or live sign-off; its score is a
-- decision on that row. Return-for-rework keeps the row (status returned) and the next answer is a new row.
CREATE TABLE submissions (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  sha        TEXT NOT NULL,                   -- content version the trainee answered
  kind       TEXT NOT NULL,                   -- question | task
  item       TEXT NOT NULL,                   -- question id or lab task id
  lab_id     TEXT REFERENCES lab_instances ON DELETE SET NULL,
  qtype      TEXT NOT NULL,                   -- text | upload | signoff | review
  prompt     TEXT NOT NULL,                   -- snapshots: the scorer sees what the trainee saw
  rubric     TEXT NOT NULL DEFAULT '',
  max_points DOUBLE PRECISION NOT NULL,
  answer     TEXT NOT NULL DEFAULT '',
  files      JSONB NOT NULL DEFAULT '[]',     -- [{name, size}] shown to people
  file_keys  TEXT[] NOT NULL DEFAULT '{}',    -- blob keys, same order; never sent to browsers
  status     TEXT NOT NULL,                   -- pending | scored | returned
  points     DOUBLE PRECISION NOT NULL DEFAULT 0,
  feedback   TEXT NOT NULL DEFAULT '',
  scored_by  TEXT NOT NULL DEFAULT '',
  scored_at  TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- At most one live answer per trainee and item: a pending one waits, a scored one is final.
CREATE UNIQUE INDEX one_live_submission ON submissions (user_id, team, training, module, kind, item)
  WHERE status IN ('pending', 'scored');
CREATE INDEX submissions_queue ON submissions (created_at) WHERE status = 'pending';

-- Recorded terminal output per session (spec §7, §13), stored in blob storage.
CREATE TABLE terminal_transcripts (
  id         BIGSERIAL PRIMARY KEY,
  lab_id     TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  terminal   TEXT NOT NULL,
  blob_key   TEXT NOT NULL,
  bytes      INT NOT NULL,
  truncated  BOOLEAN NOT NULL,                -- only the tail was kept
  started_at TIMESTAMPTZ NOT NULL,
  ended_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX terminal_transcripts_lab ON terminal_transcripts (lab_id);

-- +goose Down
DROP TABLE terminal_transcripts, submissions;
```

- [ ] **Step 2: Add the notification kinds**

In `internal/notify/notify.go`, replace the comment line `// Submission and rank-up kinds arrive with scoring (M5) and ranks (M7).` with:

```go
	SubmissionPending Kind = "submission_pending"
	SubmissionScored  Kind = "submission_scored" // also returned for rework
	// Rank-up kinds arrive with ranks (M7).
```

and append to `Kinds`:

```go
	{SubmissionPending, "A submission is waiting for my score"},
	{SubmissionScored, "My submission was scored or returned"},
```

- [ ] **Step 3: Write the failing tests**

Create `internal/scoring/scoring_test.go`:

```go
package scoring

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
)

type fakeNotes struct {
	mu     sync.Mutex
	events []notify.Event
}

func (n *fakeNotes) Notify(_ context.Context, ev notify.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
	return nil
}

func (n *fakeNotes) last(kind notify.Kind) *notify.Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := len(n.events) - 1; i >= 0; i-- {
		if n.events[i].Kind == kind {
			return &n.events[i]
		}
	}
	return nil
}

type fakeProgress struct {
	mu  sync.Mutex
	got []Submission
}

func (p *fakeProgress) Refresh(_ context.Context, sub *Submission) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, *sub)
	return nil
}

type fx struct {
	s                                         *Service
	notes                                     *fakeNotes
	quiz                                      *fakeProgress
	trainee, senior, leader, admin, stranger *auth.User
	plat                                      *config.Platform
}

// fixture: forge-301 enrolled for the trainee, senior is its scorer (as the seniors default would make them).
func fixture(t *testing.T) *fx {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	plat.Teams["forge"].Programs["forge-301"] = &config.Program{Training: "forge-301", Enrolled: []string{"trainee@crucible.local"},
		Roles: config.Roles{Scorers: []string{"senior@crucible.local"}}}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-301@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-301": "abc"}}
	store := auth.Store{DB: pool}
	mk := func(sub, email, name string) *auth.User {
		u, err := store.UpsertUser(ctx, sub, email, name)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	f := &fx{notes: &fakeNotes{}, quiz: &fakeProgress{}, plat: plat,
		trainee: mk("s1", "trainee@crucible.local", "Tara"), senior: mk("s2", "senior@crucible.local", "Sam"),
		leader: mk("s3", "leader@crucible.local", "Lee"), admin: mk("s4", "admin@crucible.local", "Ada"),
		stranger: mk("s5", "stranger@crucible.local", "Stan")}
	f.s = &Service{DB: pool, Blobs: blob.Disk{Dir: t.TempDir()}, State: func() *gitsync.State { return st },
		Notify: f.notes, Quiz: f.quiz, Log: slog.Default(), Now: time.Now}
	return f
}

func (f *fx) submitText(t *testing.T) *Submission {
	t.Helper()
	sub, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-why", QType: "text", Prompt: "Why temper?", Rubric: "brittle → tough", MaxPoints: 5,
		Answer: "  Quenched steel is brittle.\x00  "}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// formFiles builds real multipart file headers, as ReadForm would hand them over.
func formFiles(t *testing.T, files map[string]string) []*multipart.FileHeader {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for name, body := range files {
		fw, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(body))
	}
	_ = w.Close()
	form, err := multipart.NewReader(&b, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	return form.File["file"]
}

func TestScoringRules(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	sub := f.submitText(t)
	if sub.Status != Pending || sub.Answer != "Quenched steel is brittle." || sub.ID == 0 {
		t.Fatalf("stored submission: %+v", sub)
	}
	ev := f.notes.last(notify.SubmissionPending)
	if ev == nil || len(ev.To) != 1 || ev.To[0] != "senior@crucible.local" || ev.Team != "forge" || !strings.Contains(ev.Link, "/anvil/") {
		t.Fatalf("scorers must hear about it: %+v", ev)
	}
	if _, err := f.s.Score(ctx, f.trainee, sub.ID, 5, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("nobody scores their own submission: %v", err)
	}
	if _, err := f.s.Score(ctx, f.leader, sub.ID, 5, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("the leader is not a scorer of this program: %v", err)
	}
	if _, err := f.s.Score(ctx, f.senior, sub.ID, 6, ""); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("points over max: %v", err)
	}
	got, err := f.s.Score(ctx, f.senior, sub.ID, 4, "Name toughness.")
	if err != nil || got.Status != Scored || got.Points != 4 || got.ScoredBy != "senior@crucible.local" {
		t.Fatalf("score: %+v %v", got, err)
	}
	if len(f.quiz.got) != 1 || f.quiz.got[0].Status != Scored {
		t.Fatalf("progress must be refreshed after scoring: %+v", f.quiz.got)
	}
	ev = f.notes.last(notify.SubmissionScored)
	if ev == nil || ev.To[0] != "trainee@crucible.local" || ev.Team != "" || !strings.Contains(ev.Text, "4/5") {
		t.Fatalf("the trainee (and only the trainee) hears the score: %+v", ev)
	}
	entries, _ := audit.Recent(ctx, f.s.DB, 5)
	if len(entries) == 0 || entries[0].Action != "submission.score" || entries[0].Actor != "senior@crucible.local" {
		t.Fatalf("scoring is audited: %+v", entries)
	}
	if _, err := f.s.Return(ctx, f.senior, sub.ID, "again"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a scored submission is final: %v", err)
	}
	if fb := got.Feedback(); fb.Points != 4 || fb.Feedback != "Name toughness." {
		t.Fatalf("feedback view: %+v", fb)
	}
}

func TestResubmitWhilePendingIsRefused(t *testing.T) {
	f := fixture(t)
	f.submitText(t)
	_, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-why", QType: "text", Prompt: "p", MaxPoints: 5, Answer: "again"}, nil)
	if !errors.Is(err, apperr.Conflict) {
		t.Fatalf("second live answer: %v", err)
	}
}

func TestReturnForReworkAllowsANewAnswer(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	sub := f.submitText(t)
	if _, err := f.s.Return(ctx, f.senior, sub.ID, "  "); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("returning needs a reason: %v", err)
	}
	if _, err := f.s.Return(ctx, f.senior, sub.ID, "Say why it is brittle."); err != nil {
		t.Fatal(err)
	}
	latest, err := f.s.Latest(ctx, f.trainee.ID, "forge", "forge-301", "01-temper", KindQuestion)
	if err != nil || latest["q-why"].Status != Returned {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if ev := f.notes.last(notify.SubmissionScored); ev == nil || !strings.Contains(ev.Text, "returned for rework") {
		t.Fatalf("trainee told: %+v", ev)
	}
	again := f.submitText(t)
	if again.ID == sub.ID || again.Status != Pending {
		t.Fatalf("rework is a new row: %+v", again)
	}
}

func TestOnlyOneScorerWins(t *testing.T) {
	f := fixture(t)
	sub := f.submitText(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, who := range []*auth.User{f.senior, f.admin} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.s.Score(context.Background(), who, sub.ID, 3, "")
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, apperr.Conflict):
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d", wins)
	}
}

func TestFileNamesAreSanitised(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	sub, err := f.s.Submit(ctx, f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", MaxPoints: 2},
		formFiles(t, map[string]string{`..\..\evil.html`: "<script>alert(1)</script>"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Files) != 1 || sub.Files[0].Name != "evil.html" || sub.Files[0].Size != 25 || !strings.HasPrefix(sub.Keys[0], "uploads/files/") {
		t.Fatalf("files: %+v keys %v", sub.Files, sub.Keys)
	}
	rc, err := f.s.Blobs.Get(ctx, sub.Keys[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "<script>alert(1)</script>" {
		t.Fatalf("stored %q", b)
	}
	if !f.s.CanView(f.trainee, "forge", "forge-301", "trainee@crucible.local") || !f.s.CanView(f.senior, "forge", "forge-301", "trainee@crucible.local") ||
		f.s.CanView(f.stranger, "forge", "forge-301", "trainee@crucible.local") {
		t.Fatal("CanView: owner and scorer yes, stranger no")
	}
}

func TestSignOff(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	in := SignOffInput{Team: "forge", Training: "forge-301", Module: "01-temper", Question: "q-demo", Trainee: "TRAINEE@crucible.local", Notes: "Recovered it cleanly."}
	if _, err := f.s.SignOff(ctx, f.leader, in); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("leader is not a scorer: %v", err)
	}
	bad := in
	bad.Question = "q-why"
	if _, err := f.s.SignOff(ctx, f.senior, bad); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("only signoff questions: %v", err)
	}
	sub, err := f.s.SignOff(ctx, f.senior, in)
	if err != nil || sub.Status != Scored || sub.Points != 3 || sub.Note != "Recovered it cleanly." || sub.QType != "signoff" {
		t.Fatalf("sign-off: %+v %v", sub, err)
	}
	if _, err := f.s.SignOff(ctx, f.senior, in); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("second sign-off: %v", err)
	}
	if _, err := f.s.Submit(ctx, f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-demo", QType: "signoff", Prompt: "p", MaxPoints: 3, Answer: "x"}, nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a signed-off item takes no answer: %v", err)
	}
}

func TestReadFormLimits(t *testing.T) {
	build := func(n int, size int, header bool) *http.Request {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		_ = w.WriteField("answer", "notes")
		for i := 0; i < n; i++ {
			fw, _ := w.CreateFormFile("file", "f.log")
			_, _ = fw.Write(bytes.Repeat([]byte("x"), size))
		}
		_ = w.Close()
		r := httptest.NewRequest(http.MethodPost, "/x", &b)
		r.Header.Set("Content-Type", w.FormDataContentType())
		if header {
			r.Header.Set("X-Crucible-Upload", "1")
		}
		return r
	}
	if _, _, done, err := ReadForm(httptest.NewRecorder(), build(1, 10, false)); !errors.Is(err, apperr.Forbidden) {
		done()
		t.Fatalf("missing header: %v", err)
	}
	answer, files, done, err := ReadForm(httptest.NewRecorder(), build(2, 10, true))
	done()
	if err != nil || answer != "notes" || len(files) != 2 {
		t.Fatalf("ok form: %q %d %v", answer, len(files), err)
	}
	f := fixture(t)
	_, files, done, err = ReadForm(httptest.NewRecorder(), build(MaxFiles+1, 10, true))
	defer done()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", MaxPoints: 2}, files); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("too many files: %v", err)
	}
	defer func(old int64) { maxBody = old }(maxBody)
	maxBody = 4 << 10 // same code path as a 101 MiB body, without allocating one
	_, _, done2, err := ReadForm(httptest.NewRecorder(), build(1, 8<<10, true))
	done2()
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("oversized body must be a 400, got %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/scoring/ -v`
Expected: FAIL (`undefined: Service`, …).

- [ ] **Step 5: Implement `scoring.go`**

Create `internal/scoring/scoring.go`:

```go
// Package scoring is the Anvil (spec §7): human-scored submissions (text and upload answers, review tasks, live
// sign-offs), the scoring queue, feedback, return-for-rework and audited overrides. It owns the submissions table.
// learn and labs call Submit; scoring calls them back through Progress/Labs after each decision.
package scoring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"mime/multipart"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

const (
	KindQuestion = "question"
	KindTask     = "task"

	Pending  = "pending"
	Scored   = "scored"
	Returned = "returned"

	MaxFiles     = 5
	MaxFileBytes = 20 << 20
	maxAnswer    = 20000
	maxFeedback  = 5000
)

type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Submission struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"-"`
	Email     string     `json:"trainee"`
	Name      string     `json:"trainee_name"`
	Team      string     `json:"team"`
	Training  string     `json:"training"`
	Module    string     `json:"module"`
	SHA       string     `json:"-"`
	Kind      string     `json:"kind"`
	Item      string     `json:"item"`
	LabID     string     `json:"lab_id,omitempty"`
	QType     string     `json:"type"`
	Prompt    string     `json:"prompt"`
	Rubric    string     `json:"rubric"` // scorer views only: trainees get Feedback()
	MaxPoints float64    `json:"max_points"`
	Answer    string     `json:"answer"`
	Files     []File     `json:"files"`
	Keys      []string   `json:"-"`
	Status    string     `json:"status"`
	Points    float64    `json:"points"`
	Note      string     `json:"feedback"` // the scorer's written feedback
	ScoredBy  string     `json:"scored_by,omitempty"`
	ScoredAt  *time.Time `json:"scored_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
```

Continue `scoring.go`:

```go
// Feedback is what trainees see of their own submission. It deliberately has no rubric (an answer key).
type Feedback struct {
	ID       int64   `json:"id"`
	Status   string  `json:"status"`
	Answer   string  `json:"answer"`
	Files    []File  `json:"files"`
	Points   float64 `json:"points"`
	Max      float64 `json:"max_points"`
	Feedback string  `json:"feedback"`
	ScoredBy string  `json:"scored_by,omitempty"`
}

func (x *Submission) Feedback() *Feedback {
	if x == nil {
		return nil
	}
	return &Feedback{ID: x.ID, Status: x.Status, Answer: x.Answer, Files: x.Files, Points: x.Points, Max: x.MaxPoints,
		Feedback: x.Note, ScoredBy: x.ScoredBy}
}

// Progress recomputes the trainee's item (quiz or lab) after a submission was scored or returned.
type Progress interface {
	Refresh(ctx context.Context, sub *Submission) error
}

// Labs is what the Anvil needs from the lab module (implemented by *labs.Service).
type Labs interface {
	Progress
	Evidence(ctx context.Context, labID string) (*LabEvidence, error)
	// Override sets a non-review task's awarded points; record runs inside the same transaction (the audit entry)
	// and receives the previous points.
	Override(ctx context.Context, labID, task string, points float64, record func(context.Context, pgx.Tx, float64) error) error
}

type LabEvidence struct {
	Runtime      string         `json:"runtime"`
	SelfReported bool           `json:"self_reported"`
	Tasks        []TaskEvidence `json:"tasks"`
	Transcripts  []Transcript   `json:"transcripts"`
}

type TaskEvidence struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Kind      string     `json:"kind"` // check | quiz | review
	Status    string     `json:"status"`
	Points    float64    `json:"points"`
	Awarded   float64    `json:"awarded"`
	HintsUsed int        `json:"hints_used"`
	HintCost  float64    `json:"hint_cost"`
	Checks    []CheckRun `json:"checks"`
}

type CheckRun struct {
	LabID        string    `json:"lab_id"`
	At           time.Time `json:"at"`
	ExitCode     int       `json:"exit_code"`
	Output       string    `json:"output"`
	Answer       string    `json:"answer,omitempty"`
	SelfReported bool      `json:"self_reported"`
}

type Transcript struct {
	ID        int64     `json:"id"`
	LabID     string    `json:"lab_id"`
	Terminal  string    `json:"terminal"`
	Bytes     int       `json:"bytes"`
	Truncated bool      `json:"truncated"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Notifier queues notifications (implemented by *notify.Service).
type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type Service struct {
	DB     *pgxpool.Pool
	Blobs  blob.Store
	State  func() *gitsync.State
	Notify Notifier
	Quiz   Progress // *learn.Service
	Labs   Labs     // *labs.Service
	Log    *slog.Logger
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

func (s *Service) checker() (rbac.Checker, *gitsync.State, error) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return rbac.Checker{}, nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return rbac.Checker{P: st.Platform}, st, nil
}

// Clean makes people's text safe for Postgres TEXT: valid UTF-8, no NUL bytes.
func Clean(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// fileName keeps only the last path element of what the browser sent (either slash), cleaned and at most 200 runes.
func fileName(name string) string {
	n := path.Base(strings.ReplaceAll(Clean(name), `\`, "/"))
	if n == "." || n == "/" || n == "" {
		n = "file"
	}
	if r := []rune(n); len(r) > 200 {
		n = string(r[:200])
	}
	return n
}

func short(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

const subCols = `s.id, s.user_id, u.email, u.name, s.team, s.training, s.module, s.sha, s.kind, s.item, coalesce(s.lab_id, ''),
	s.qtype, s.prompt, s.rubric, s.max_points, s.answer, s.files, s.file_keys, s.status, s.points, s.feedback, s.scored_by,
	s.scored_at, s.created_at`
const subFrom = ` FROM submissions s JOIN users u ON u.id = s.user_id `

func scanSub(row pgx.Row) (*Submission, error) {
	var x Submission
	err := row.Scan(&x.ID, &x.UserID, &x.Email, &x.Name, &x.Team, &x.Training, &x.Module, &x.SHA, &x.Kind, &x.Item, &x.LabID,
		&x.QType, &x.Prompt, &x.Rubric, &x.MaxPoints, &x.Answer, &x.Files, &x.Keys, &x.Status, &x.Points, &x.Note, &x.ScoredBy,
		&x.ScoredAt, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "submission not found")
	}
	return &x, err
}

func collectSubs(rows pgx.Rows, err error) ([]*Submission, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Submission, error) { return scanSub(r) })
}

func (s *Service) get(ctx context.Context, id int64) (*Submission, error) {
	return scanSub(s.DB.QueryRow(ctx, `SELECT `+subCols+subFrom+`WHERE s.id = $1`, id))
}

// Latest returns the newest submission per item of one kind for a trainee's module.
func (s *Service) Latest(ctx context.Context, userID int64, team, training, module, kind string) (map[string]*Submission, error) {
	list, err := collectSubs(s.DB.Query(ctx, `SELECT DISTINCT ON (s.item) `+subCols+subFrom+`
		WHERE s.user_id = $1 AND s.team = $2 AND s.training = $3 AND s.module = $4 AND s.kind = $5
		ORDER BY s.item, s.id DESC`, userID, team, training, module, kind))
	if err != nil {
		return nil, err
	}
	out := map[string]*Submission{}
	for _, x := range list {
		out[x.Item] = x
	}
	return out, nil
}

func uniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

const alreadyLive = "this answer is already with a scorer or scored"

// Submit stores the files and the submission (pending), then tells the program's scorers. Callers have already checked
// that u may answer this item; Submit fills in the user and validates sizes.
func (s *Service) Submit(ctx context.Context, u *auth.User, sub *Submission, files []*multipart.FileHeader) (*Submission, error) {
	sub.Answer = Clean(strings.TrimSpace(sub.Answer))
	if utf8.RuneCountInString(sub.Answer) > maxAnswer {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("answers are limited to %d characters", maxAnswer))
	}
	if len(files) > MaxFiles {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("attach at most %d files", MaxFiles))
	}
	for _, fh := range files {
		if fh.Size > MaxFileBytes {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("%s is larger than 20 MiB", fileName(fh.Filename)))
		}
	}
	var live bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM submissions WHERE user_id = $1 AND team = $2 AND training = $3
		AND module = $4 AND kind = $5 AND item = $6 AND status IN ('pending', 'scored'))`,
		u.ID, sub.Team, sub.Training, sub.Module, sub.Kind, sub.Item).Scan(&live); err != nil {
		return nil, err
	}
	if live { // checked before storing files so a refused answer leaves no blobs behind
		return nil, apperr.Wrap(apperr.Conflict, alreadyLive)
	}
	sub.UserID, sub.Email, sub.Name = u.ID, strings.ToLower(u.Email), u.Name
	sub.Files, sub.Keys = []File{}, []string{}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		key := "uploads/files/" + randHex(16)
		err = s.Blobs.Put(ctx, key, f, fh.Size)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("storing %s: %w", fileName(fh.Filename), err)
		}
		sub.Files = append(sub.Files, File{Name: fileName(fh.Filename), Size: fh.Size})
		sub.Keys = append(sub.Keys, key)
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, lab_id, qtype, prompt,
		rubric, max_points, answer, files, file_keys, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10, $11, $12, $13, $14, $15, 'pending') RETURNING id, created_at`,
		sub.UserID, sub.Team, sub.Training, sub.Module, sub.SHA, sub.Kind, sub.Item, sub.LabID, sub.QType, Clean(sub.Prompt),
		Clean(sub.Rubric), sub.MaxPoints, sub.Answer, sub.Files, sub.Keys).Scan(&sub.ID, &sub.CreatedAt)
	if uniqueViolation(err) {
		return nil, apperr.Wrap(apperr.Conflict, alreadyLive) // ponytail: a lost race leaves its blobs orphaned
	}
	if err != nil {
		return nil, err
	}
	sub.Status = Pending
	s.tellScorers(ctx, sub)
	return sub, nil
}

func (s *Service) canScore(u *auth.User, team, training, owner string) (bool, error) {
	c, _, err := s.checker()
	if err != nil {
		return false, err
	}
	return c.Can(u.Email, rbac.Score, team, training, owner), nil
}

// CanView: the trainee themself, their program's scorers and managers, team leader/seniors, their mentor, admins
// (spec §5.3 "View trainee progress").
func (s *Service) CanView(u *auth.User, team, training, owner string) bool {
	c, _, err := s.checker()
	return err == nil && c.Can(u.Email, rbac.ViewProgress, team, training, owner)
}

func (s *Service) mayScore(u *auth.User, sub *Submission) error {
	ok, err := s.canScore(u, sub.Team, sub.Training, sub.Email)
	switch {
	case err != nil:
		return err
	case strings.EqualFold(u.Email, sub.Email):
		return apperr.Wrap(apperr.Forbidden, "nobody scores their own submission")
	case !ok:
		return apperr.Wrap(apperr.Forbidden, "you are not a scorer of this program")
	}
	return nil
}

func (s *Service) Score(ctx context.Context, u *auth.User, id int64, points float64, feedback string) (*Submission, error) {
	return s.decide(ctx, u, id, Scored, points, feedback)
}

func (s *Service) Return(ctx context.Context, u *auth.User, id int64, feedback string) (*Submission, error) {
	if strings.TrimSpace(feedback) == "" {
		return nil, apperr.Wrap(apperr.Invalid, "say what to rework")
	}
	return s.decide(ctx, u, id, Returned, 0, feedback)
}

// decide stores one decision on a pending submission. The WHERE status = 'pending' makes concurrent decisions safe:
// exactly one wins (spec §5.3 rules are checked first).
func (s *Service) decide(ctx context.Context, u *auth.User, id int64, status string, points float64, feedback string) (*Submission, error) {
	feedback = Clean(strings.TrimSpace(feedback))
	if utf8.RuneCountInString(feedback) > maxFeedback {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("feedback is limited to %d characters", maxFeedback))
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayScore(u, sub); err != nil {
		return nil, err
	}
	if status == Scored && (math.IsNaN(points) || points < 0 || points > sub.MaxPoints) {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("points must be between 0 and %g", sub.MaxPoints))
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE submissions SET status = $2, points = $3, feedback = $4, scored_by = lower($5), scored_at = $6
		WHERE id = $1 AND status = 'pending'`, id, status, points, feedback, u.Email, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "someone already scored or returned this submission")
	}
	action := map[string]string{Scored: "submission.score", Returned: "submission.return"}[status]
	if err := audit.Log(ctx, tx, u.Email, action, fmt.Sprintf("submission/%d", id), map[string]any{"trainee": sub.Email,
		"item": sub.Team + "/" + sub.Training + "/" + sub.Module + "/" + sub.Item, "points": points, "max": sub.MaxPoints}, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sub.Status, sub.Points, sub.Note, sub.ScoredBy, sub.ScoredAt = status, points, feedback, strings.ToLower(u.Email), &now
	s.refresh(ctx, sub)
	s.tellTrainee(ctx, sub)
	return sub, nil
}

type SignOffInput struct {
	Team     string   `json:"team"`
	Training string   `json:"training"`
	Module   string   `json:"module"`
	Question string   `json:"question"`
	Trainee  string   `json:"trainee"`
	Points   *float64 `json:"points"` // nil = full marks
	Notes    string   `json:"notes"`
}

// SignOff records a live demo a scorer watched (spec §7 "Mark passed after live demo") as a scored submission.
func (s *Service) SignOff(ctx context.Context, u *auth.User, in SignOffInput) (*Submission, error) {
	c, st, err := s.checker()
	if err != nil {
		return nil, err
	}
	trainee := strings.ToLower(strings.TrimSpace(in.Trainee))
	if strings.EqualFold(u.Email, trainee) {
		return nil, apperr.Wrap(apperr.Forbidden, "nobody signs off their own demo")
	}
	if !c.Can(u.Email, rbac.Score, in.Team, in.Training, trainee) {
		return nil, apperr.Wrap(apperr.Forbidden, "you are not a scorer of this program")
	}
	if !c.Can(trainee, rbac.TakeTraining, in.Team, in.Training, "") {
		return nil, apperr.Wrap(apperr.Invalid, "that person is not enrolled in this training")
	}
	t, sha := st.ProgramTraining(in.Team, in.Training)
	if t == nil || t.Module(in.Module) == nil || t.Module(in.Module).Quiz == nil {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	q := t.Module(in.Module).Quiz.Question(in.Question)
	if q == nil || q.Type != "signoff" {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	points := q.Points
	if in.Points != nil {
		points = *in.Points
		if math.IsNaN(points) || points < 0 || points > q.Points {
			return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("points must be between 0 and %g", q.Points))
		}
	}
	notes := Clean(strings.TrimSpace(in.Notes))
	if utf8.RuneCountInString(notes) > maxFeedback {
		return nil, apperr.Wrap(apperr.Invalid, fmt.Sprintf("notes are limited to %d characters", maxFeedback))
	}
	var userID int64
	err = s.DB.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 ORDER BY id LIMIT 1`, trainee).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "that trainee has not signed in to Crucible yet")
	}
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype, prompt, rubric,
		max_points, status, points, feedback, scored_by, scored_at)
		VALUES ($1, $2, $3, $4, $5, 'question', $6, 'signoff', $7, $8, $9, 'scored', $10, $11, lower($12), $13) RETURNING id`,
		userID, in.Team, in.Training, in.Module, sha, in.Question, q.Prompt, q.Rubric, q.Points, points, notes, u.Email, s.now()).Scan(&id)
	if uniqueViolation(err) {
		return nil, apperr.Wrap(apperr.Conflict, "this demo is already signed off")
	}
	if err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, u.Email, "submission.signoff", fmt.Sprintf("submission/%d", id), map[string]any{"trainee": trainee,
		"item": in.Team + "/" + in.Training + "/" + in.Module + "/" + in.Question, "points": points}, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refresh(ctx, sub)
	s.tellTrainee(ctx, sub)
	return sub, nil
}

// refresh recomputes the trainee's item. A failure is logged, never shown to the scorer: the decision is stored.
func (s *Service) refresh(ctx context.Context, sub *Submission) {
	var p Progress = s.Quiz
	if sub.Kind == KindTask {
		p = s.Labs
	}
	if p == nil {
		return
	}
	if err := p.Refresh(ctx, sub); err != nil {
		s.log().Error("updating progress after a scoring decision failed", "submission", sub.ID, "err", err)
	}
}

func (s *Service) send(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || len(ev.To) == 0 {
		return
	}
	if err := s.Notify.Notify(ctx, ev); err != nil {
		s.log().Error("queueing a scoring notification failed", "kind", ev.Kind, "err", err)
	}
}

// tellScorers: the program's scorers, or the admins when a program has none (spec §10 "submission awaiting scoring").
func (s *Service) tellScorers(ctx context.Context, sub *Submission) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return
	}
	var to []string
	if t := st.Platform.Teams[sub.Team]; t != nil && t.Programs[sub.Training] != nil {
		to = slices.Clone(t.Programs[sub.Training].Roles.Scorers)
	}
	if len(to) == 0 {
		to = slices.Clone(st.Platform.Admins)
	}
	to = slices.DeleteFunc(to, func(e string) bool { return strings.EqualFold(e, sub.Email) })
	who := sub.Name
	if who == "" {
		who = sub.Email
	}
	s.send(ctx, notify.Event{Kind: notify.SubmissionPending, To: to, Team: sub.Team, // no points: safe for the team channel
		Subject: "Waiting on the anvil: " + sub.Training,
		Text:    fmt.Sprintf("%s submitted %q (%s / %s) for scoring.", who, short(sub.Prompt), sub.Training, sub.Module),
		Link:    fmt.Sprintf("/anvil/%d", sub.ID)})
}

// tellTrainee: by email only. Scores are private (spec §2 "Score privacy"), so never to a team webhook.
func (s *Service) tellTrainee(ctx context.Context, sub *Submission) {
	verb := "was returned for rework"
	if sub.Status == Scored {
		verb = fmt.Sprintf("was scored %g/%g", sub.Points, sub.MaxPoints)
	}
	page := "quiz"
	if sub.Kind == KindTask {
		page = "lab"
	}
	s.send(ctx, notify.Event{Kind: notify.SubmissionScored, To: []string{sub.Email},
		Subject: "From the anvil: " + sub.Training,
		Text:    fmt.Sprintf("Your answer to %q %s. The feedback is on the page.", short(sub.Prompt), verb),
		Link:    fmt.Sprintf("/p/%s/%s/m/%s/%s", sub.Team, sub.Training, sub.Module, page)})
}
```

- [ ] **Step 6: Implement `form.go`**

Create `internal/scoring/form.go`:

```go
package scoring

import (
	"mime/multipart"
	"net/http"

	"crucible/internal/apperr"
)

// maxBody caps a whole multipart request: every file at its limit plus room for the fields. A var for tests.
var maxBody int64 = MaxFiles*MaxFileBytes + 1<<20

// ReadForm parses a multipart submission: an "answer" field and up to MaxFiles "file" parts. done removes the temp
// files multipart spooled to disk; always call it. The X-Crucible-Upload header is required: a cross-site HTML form
// cannot set it, so these endpoints are no easier to forge than the JSON ones.
func ReadForm(w http.ResponseWriter, r *http.Request) (string, []*multipart.FileHeader, func(), error) {
	done := func() {}
	if r.Header.Get("X-Crucible-Upload") != "1" {
		return "", nil, done, apperr.Wrap(apperr.Forbidden, "uploads must come from the Crucible app")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		return "", nil, done, apperr.Wrap(apperr.Invalid, "the upload is malformed or too large (at most 5 files of 20 MiB each)")
	}
	f := r.MultipartForm
	return r.FormValue("answer"), f.File["file"], func() { _ = f.RemoveAll() }, nil
}
```

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/scoring/ ./internal/notify/ ./internal/db/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/db/migrations internal/notify/notify.go internal/scoring
git commit -m "feat(scoring): submissions with scoring, return for rework, sign-offs and notifications

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: Quizzes: answering human questions, and progression that waits for scores

**Files:**
- Modify: `internal/learn/quiz.go` (`PublicQuestion.Submission`, `Result.Status`, `Score`, new `quizOutcome`)
- Modify: `internal/learn/quiz_test.go` (append `TestQuizOutcome`)
- Modify: `internal/learn/service.go` (`Service.Scoring`, `Quiz`, `SubmitQuiz`, new `AnswerHuman`, `refreshQuiz`, `latest`, `Refresh`, `ForceScore`, `httpLink`)
- Modify: `internal/learn/http.go` (answer route)
- Create: `internal/learn/human_test.go`

**Interfaces:**
- Consumes: `scoring.Service.Submit/Latest/Score/Return/SignOff`, `scoring.Submission`, `scoring.Feedback`, `scoring.ReadForm` (Task 3); Forge 301 ids (Task 1).
- Produces:
  - `learn.Service.Scoring *scoring.Service`
  - `func (s *Service) AnswerHuman(ctx context.Context, u *auth.User, team, training, module, question, answer string, files []*multipart.FileHeader) (*QuizView, error)`
  - `func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error` (implements `scoring.Progress`)
  - `func (s *Service) ForceScore(ctx context.Context, userID int64, team, training, module, item string, score float64) error` (used by labs in Task 5)
  - `PublicQuestion.Submission *scoring.Feedback` (JSON `submission`), `Result.Status string` (JSON `status`: `in_progress | pending_review | complete`)
  - Item status `pending_review` in outlines.
  - Route `POST /api/programs/{team}/{training}/modules/{module}/quiz/questions/{question}/answer` (multipart: `answer`, `file`×≤5; header `X-Crucible-Upload: 1`) → `QuizView`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/learn/quiz_test.go` (add `"crucible/internal/scoring"` to its imports):

```go
func TestQuizOutcome(t *testing.T) {
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	q := tr.Module("01-temper").Quiz // instant 1 pt; human: q-why 5, q-log 2, q-demo (signoff) 3; threshold 0.6
	sub := func(status string, pts float64) *scoring.Submission { return &scoring.Submission{Status: status, Points: pts} }
	cases := []struct {
		name      string
		best      float64
		attempted bool
		subs      map[string]*scoring.Submission
		want      string
		pct       float64
	}{
		{"nothing yet", 0, false, nil, "in_progress", 0},
		{"answers pending, sign-off not given", 1, true, map[string]*scoring.Submission{"q-why": sub("pending", 0), "q-log": sub("pending", 0)}, "pending_review", 1.0 / 11},
		{"one returned", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("returned", 0)}, "in_progress", 6.0 / 11},
		{"one unanswered", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5)}, "in_progress", 6.0 / 11},
		{"all scored", 1, true, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("scored", 2), "q-demo": sub("scored", 3)}, "complete", 1},
		{"all scored, too low", 0, true, map[string]*scoring.Submission{"q-why": sub("scored", 1), "q-log": sub("scored", 0), "q-demo": sub("scored", 3)}, "in_progress", 4.0 / 11},
		{"instant part never tried", 0, false, map[string]*scoring.Submission{"q-why": sub("scored", 5), "q-log": sub("scored", 2), "q-demo": sub("scored", 3)}, "in_progress", 10.0 / 11},
	}
	for _, c := range cases {
		got, pct := quizOutcome(q, c.best, c.attempted, c.subs)
		if got != c.want || math.Abs(pct-c.pct) > 1e-9 {
			t.Errorf("%s: got %s %.3f, want %s %.3f", c.name, got, pct, c.want, c.pct)
		}
	}
	allHuman := &content.Quiz{PassThreshold: 0.5, Questions: []*content.Question{{ID: "a", Type: "text", Points: 2}}}
	if got, _ := quizOutcome(allHuman, 0, false, map[string]*scoring.Submission{"a": sub("scored", 2)}); got != "complete" {
		t.Errorf("an all-human quiz needs no instant attempt: %s", got)
	}
}
```

(Add `"math"` to the imports of `quiz_test.go` if it is not there.)

Create `internal/learn/human_test.go`:

```go
package learn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/scoring"
)

const team, f301, temper = "forge", "forge-301", "01-temper"

func fixture301(t *testing.T) (*Service, *scoring.Service, *auth.User, *auth.User) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	plat.Teams["forge"].Programs["forge-301"] = &config.Program{Training: "forge-301", Enrolled: []string{"trainee@crucible.local"},
		Roles: config.Roles{Scorers: []string{"senior@crucible.local"}}}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-301@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-301": "abc"}}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	senior, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	s := &Service{DB: pool, QuizSecret: "test-secret", State: func() *gitsync.State { return st }}
	sc := &scoring.Service{DB: pool, Blobs: blob.Disk{Dir: t.TempDir()}, State: s.State, Quiz: s, Log: slog.Default()}
	s.Scoring = sc
	return s, sc, trainee, senior
}

func submissionIDs(v *QuizView) map[string]int64 {
	ids := map[string]int64{}
	for _, q := range v.Questions {
		if q.Submission != nil {
			ids[q.ID] = q.Submission.ID
		}
	}
	return ids
}

func TestHumanQuestionsHoldTheModuleUntilScored(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	quiz := s.State().Trainings["forge-301@abc"].Module(temper).Quiz
	res, err := s.SubmitQuiz(ctx, u, team, f301, temper, asPublic(quiz, s.seedFor(u.ID, team, f301, temper), map[string]json.RawMessage{"q-quench": raw("0")}))
	if err != nil || res.Passed || res.Status != "in_progress" || !res.PendingHuman {
		t.Fatalf("instant part alone: %+v %v", res, err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Quenched steel is brittle; tempering makes it tough.", nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-log", "https://logs.example.com/run/42", nil)
	if err != nil || v.Status != "pending_review" {
		t.Fatalf("only scorers are missing now: %+v %v", v, err)
	}
	o, _ := s.Outline(ctx, u, team, f301)
	if !o.Modules[1].Locked || o.Modules[0].Items[0].Status != "pending_review" {
		t.Fatalf("linear progression waits for the scorer: %+v", o.Modules)
	}
	ids := submissionIDs(v)
	if _, err := sc.Score(ctx, senior, ids["q-why"], 5, "Good."); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Score(ctx, senior, ids["q-log"], 2, ""); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.Outline(ctx, u, team, f301); !o.Modules[1].Locked {
		t.Fatal("the live sign-off is still missing")
	}
	if _, err := sc.SignOff(ctx, senior, scoring.SignOffInput{Team: team, Training: f301, Module: temper, Question: "q-demo", Trainee: u.Email}); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Outline(ctx, u, team, f301)
	if o.Modules[1].Locked || !o.Modules[0].Complete {
		t.Fatalf("all scored and over the threshold: %+v", o.Modules)
	}
	v, _ = s.Quiz(ctx, u, team, f301, temper)
	for _, q := range v.Questions {
		if q.ID == "q-why" && (q.Submission == nil || q.Submission.Feedback != "Good." || q.Submission.Points != 5) {
			t.Fatalf("trainee sees the feedback: %+v", q.Submission)
		}
	}
}

func TestReturnedAnswerReopensTheQuestion(t *testing.T) {
	ctx := context.Background()
	s, sc, u, senior := fixture301(t)
	v, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Because.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Return(ctx, senior, submissionIDs(v)["q-why"], "Say what quenching does to the steel."); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Quiz(ctx, u, team, f301, temper)
	if v.Status != "in_progress" {
		t.Fatalf("returned work is the trainee's move again: %s", v.Status)
	}
	v, err = s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Quenching makes it brittle; tempering restores toughness.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Score(ctx, senior, submissionIDs(v)["q-why"], 5, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "again", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a scored answer is final: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-demo", "I did it", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("sign-offs come from scorers: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-quench", "0", nil); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("instant questions go through attempts: %v", err)
	}
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "x", []*multipart.FileHeader{{Filename: "a.txt", Size: 1}}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("text questions take no files: %v", err)
	}
}

func TestUploadLinkMustBeHTTP(t *testing.T) {
	ctx := context.Background()
	s, _, u, _ := fixture301(t)
	for _, bad := range []string{"", "javascript:alert(1)", "ftp://logs.example.com/x", "https://", "https://a b"} {
		if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-log", bad, nil); !errors.Is(err, apperr.Invalid) {
			t.Errorf("link %q: %v", bad, err)
		}
	}
}

func TestTraineeNeverSeesRubric(t *testing.T) {
	ctx := context.Background()
	s, _, u, _ := fixture301(t)
	if _, err := s.AnswerHuman(ctx, u, team, f301, temper, "q-why", "Brittle.", nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.Quiz(ctx, u, team, f301, temper)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "Half marks") || strings.Contains(string(b), "Return screenshots") || strings.Contains(string(b), "rubric") {
		t.Fatalf("rubric leaked: %s", b)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/learn/ -run 'TestQuizOutcome|Human|Returned|UploadLink|Rubric' -v`
Expected: FAIL (`undefined: quizOutcome`, `s.Scoring undefined`, `undefined: (*Service).AnswerHuman`).

- [ ] **Step 3: Implement the quiz rules**

In `internal/learn/quiz.go`, import `"crucible/internal/scoring"` and:

Add to `PublicQuestion`:

```go
	Submission *scoring.Feedback `json:"submission,omitempty"` // the trainee's latest answer to a human question
```

Add to `Result`:

```go
	Status string `json:"status"` // the quiz item after this attempt: in_progress | pending_review | complete
```

In `Score`, replace `res.Passed = res.Max > 0 && !res.PendingHuman && res.Percent >= q.PassThreshold-1e-9` with:

```go
	res.Passed = res.Max > 0 && res.Percent >= q.PassThreshold-1e-9 // instant part only; SubmitQuiz decides the item
```

and change the `res.PendingHuman = true // scored by people in M5` comment to `// answered and scored separately (AnswerHuman)`.

Append:

```go
// quizOutcome decides the quiz item from the best instant attempt and the latest human submissions (spec §7):
// complete only when every human question is scored and the total reaches the pass threshold; pending_review when the
// only things missing are scorers' decisions; otherwise in_progress.
func quizOutcome(q *content.Quiz, bestInstant float64, attempted bool, subs map[string]*scoring.Submission) (string, float64) {
	var instantMax, humanMax, humanScore float64
	waiting, open := false, false
	for _, x := range q.Questions {
		switch {
		case x.Type == "terminal":
		case content.IsHuman(x.Type):
			humanMax += x.Points
			sub := subs[x.ID]
			switch {
			case sub != nil && sub.Status == scoring.Scored:
				humanScore += sub.Points
			case sub != nil && sub.Status == scoring.Pending, sub == nil && x.Type == "signoff":
				waiting = true
			default: // unanswered, or returned for rework
				open = true
			}
		default:
			instantMax += x.Points
		}
	}
	if instantMax > 0 && !attempted {
		open = true
	}
	pct := 0.0
	if total := instantMax + humanMax; total > 0 {
		pct = (bestInstant + humanScore) / total
	}
	switch {
	case open:
		return "in_progress", pct
	case waiting:
		return "pending_review", pct
	case pct >= q.PassThreshold-1e-9:
		return "complete", pct
	}
	return "in_progress", pct
}
```

- [ ] **Step 4: Implement the service side**

In `internal/learn/service.go`, add imports `"mime/multipart"`, `"net/url"`, `"strings"`, `"crucible/internal/scoring"`, and the field:

```go
	// Scoring stores human-scored answers (M5). nil in tests that only exercise instant quizzes.
	Scoring *scoring.Service
```

In `Quiz`, replace the final `return &QuizView{…}` with:

```go
	subs, err := s.latest(ctx, u.ID, team, t.ID, module)
	if err != nil {
		return nil, err
	}
	qs := PublicQuiz(m.Quiz, s.seedFor(u.ID, team, training, module))
	for i := range qs {
		qs[i].Submission = subs[qs[i].ID].Feedback() // nil-safe; Feedback never carries the rubric
	}
	return &QuizView{PassThreshold: m.Quiz.PassThreshold, Questions: qs, Status: status}, nil
```

In `SubmitQuiz`, replace everything after the `INSERT INTO quiz_attempts` statement with:

```go
	status, pct, err := s.refreshQuiz(ctx, u.ID, team, t, module)
	if err != nil {
		return nil, err
	}
	res.Status, res.Percent, res.Passed = status, pct, status == "complete"
	return &res, nil
```

Append:

```go
func (s *Service) latest(ctx context.Context, userID int64, team, training, module string) (map[string]*scoring.Submission, error) {
	if s.Scoring == nil {
		return nil, nil
	}
	return s.Scoring.Latest(ctx, userID, team, training, module, scoring.KindQuestion)
}

// refreshQuiz recomputes the quiz item from the best instant attempt ("best score counts", spec §7) and human scores.
func (s *Service) refreshQuiz(ctx context.Context, userID int64, team string, t *content.Training, module string) (string, float64, error) {
	m := t.Module(module)
	if m == nil || m.Quiz == nil {
		return "", 0, apperr.Wrap(apperr.NotFound, "this module has no quiz")
	}
	var best float64
	var attempts int
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(max(score), 0), count(*) FROM quiz_attempts
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, userID, team, t.ID, module).Scan(&best, &attempts); err != nil {
		return "", 0, err
	}
	subs, err := s.latest(ctx, userID, team, t.ID, module)
	if err != nil {
		return "", 0, err
	}
	status, pct := quizOutcome(m.Quiz, best, attempts > 0, subs)
	return status, pct, s.SetItem(ctx, userID, team, t.ID, module, "quiz", status, pct)
}

// AnswerHuman hands a text or upload answer to the program's scorers (spec §4.4, §7). Sign-offs come from scorers.
func (s *Service) AnswerHuman(ctx context.Context, u *auth.User, team, training, module, question, answer string, files []*multipart.FileHeader) (*QuizView, error) {
	t, sha, m, err := s.quizModule(ctx, u, team, training, module)
	if err != nil {
		return nil, err
	}
	x := m.Quiz.Question(question)
	if x == nil || !content.IsHuman(x.Type) {
		return nil, apperr.Wrap(apperr.NotFound, "question not found")
	}
	answer = strings.TrimSpace(answer)
	switch x.Type {
	case "signoff":
		return nil, apperr.Wrap(apperr.Conflict, "a scorer signs this off after a live demo")
	case "text":
		if answer == "" || len(files) > 0 {
			return nil, apperr.Wrap(apperr.Invalid, "write your answer (text questions take no files)")
		}
	case "upload":
		if answer == "" && len(files) == 0 {
			return nil, apperr.Wrap(apperr.Invalid, "attach a file or paste a link")
		}
		if answer != "" && !httpLink(answer) {
			return nil, apperr.Wrap(apperr.Invalid, "a link must start with http:// or https://")
		}
	}
	if s.Scoring == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "scoring is not available")
	}
	if _, err := s.Scoring.Submit(ctx, u, &scoring.Submission{Team: team, Training: t.ID, Module: module, SHA: sha,
		Kind: scoring.KindQuestion, Item: x.ID, QType: x.Type, Prompt: x.Prompt, Rubric: x.Rubric, MaxPoints: x.Points,
		Answer: answer}, files); err != nil {
		return nil, err
	}
	if _, _, err := s.refreshQuiz(ctx, u.ID, team, t, module); err != nil {
		return nil, err
	}
	return s.Quiz(ctx, u, team, training, module)
}

// httpLink accepts only absolute http(s) URLs: scorers click these, so javascript: and friends never get stored.
func httpLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\r\n")
}

// Refresh implements scoring.Progress: a scorer decided on one of this trainee's answers.
func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error {
	st, err := s.state()
	if err != nil {
		return err
	}
	t, _ := st.ProgramTraining(sub.Team, sub.Training)
	if t == nil || t.Module(sub.Module) == nil {
		t = st.Training(sub.Training, sub.SHA)
	}
	if t == nil {
		return apperr.Wrap(apperr.Unavailable, "this training's content is unavailable right now")
	}
	_, _, err = s.refreshQuiz(ctx, sub.UserID, sub.Team, t, sub.Module)
	return err
}

// ForceScore sets an item's score exactly. SetItem only ever raises it; an override may lower it (labs, Task 5).
func (s *Service) ForceScore(ctx context.Context, userID int64, team, training, module, item string, score float64) error {
	_, err := s.DB.Exec(ctx, `UPDATE item_progress SET score = $6, updated_at = now()
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 AND item = $5`, userID, team, training, module, item, score)
	return err
}
```

- [ ] **Step 5: Add the route**

In `internal/learn/http.go` (import `"crucible/internal/scoring"`), inside `Routes` after the quiz attempts route:

```go
	r.Post(p+"/modules/{module}/quiz/questions/{question}/answer", func(w http.ResponseWriter, r *http.Request) {
		answer, files, done, err := scoring.ReadForm(w, r)
		defer done()
		if err != nil {
			httpx.Error(w, err)
			return
		}
		q, err := s.AnswerHuman(r.Context(), auth.UserFrom(r.Context()), param(r, "team"), param(r, "training"),
			param(r, "module"), param(r, "question"), answer, files)
		reply(w, q, err)
	})
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/learn/ ./internal/scoring/ -v`
Expected: PASS, including the existing Forge 101 tests (`TestProgressionUnlocksModuleTwo`, `TestFailedQuizKeepsLockAndPassedQuizStaysPassed`).

- [ ] **Step 7: Commit**

```bash
git add internal/learn
git commit -m "feat(learn): text and upload answers for scorers; progression waits for human scores

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Labs: review tasks, recompute, evidence and overrides

**Files:**
- Modify: `internal/labs/service.go` (`Service.Scoring`, `TaskView.Review`, `taskStatuses`, `view`, `readyTask`, `finishTask` → `recompute`, `instByID`, `reviews`)
- Create: `internal/labs/review.go` (`SubmitReview`, `Refresh`, `Override`, `Evidence`)
- Modify: `internal/labs/http.go` (submit route)
- Modify: `examples/forge-301/modules/02-review-lab/lab/lab.yaml` (a paid hint on `t2-proof`)
- Create: `internal/labs/review_test.go`
- Modify: any existing test that calls `taskStatuses(` directly (add a trailing `nil` argument)

**Interfaces:**
- Consumes: `scoring.Service.Submit/Latest/Score/Return`, `scoring.LabEvidence/TaskEvidence/CheckRun/Transcript`, `scoring.ReadForm` (Task 3); `learn.Service.ForceScore` (Task 4).
- Produces:
  - `labs.Service.Scoring *scoring.Service`
  - `TaskView.Review *scoring.Feedback` (JSON `review`); task status `submitted`
  - `func (s *Service) SubmitReview(ctx context.Context, u *auth.User, labID, taskID, notes string, files []*multipart.FileHeader) (*View, error)`
  - `func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error`
  - `func (s *Service) Override(ctx context.Context, labID, taskID string, points float64, record func(context.Context, pgx.Tx, float64) error) error`
  - `func (s *Service) Evidence(ctx context.Context, labID string) (*scoring.LabEvidence, error)` (transcripts list filled from Task 6's table; empty until then)
  - Route `POST /api/labs/{id}/tasks/{task}/submit` (multipart `answer` = notes, `file`×≤5) → `View`.

- [ ] **Step 1: Give the review task a paid hint (exercises rule 5)**

In `examples/forge-301/modules/02-review-lab/lab/lab.yaml`, add under `t2-proof` (after `points: 3`):

```yaml
    hints:
      - text: "`echo tempered > /tmp/proof && cat /tmp/proof`"
        cost: 1
```

- [ ] **Step 2: Write the failing tests**

Create `internal/labs/review_test.go`:

```go
package labs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/scoring"
)

// setupReview adds Forge 301 (enrolled, senior = f.other scores it, module 01 done) and a real scoring service.
func setupReview(t *testing.T) *fx {
	t.Helper()
	f := setup(t, true)
	tr, probs := content.Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := f.s.Learn.State()
	st.Trainings["forge-301@def"] = tr
	st.ProgramSHAs["forge/forge-301"] = "def"
	f.plat.Teams["forge"].Programs["forge-301"] = &config.Program{Training: "forge-301", Enrolled: []string{"trainee@crucible.local"},
		Roles: config.Roles{Scorers: []string{"senior@crucible.local"}}, LabDefaults: f.plat.Teams["forge"].Programs["forge-101"].LabDefaults}
	f.sc = &scoring.Service{DB: f.s.DB, Blobs: blob.Disk{Dir: t.TempDir()}, State: f.s.Learn.State, Notify: f.notes,
		Quiz: f.s.Learn, Labs: f.s, Log: slog.Default(), Now: f.clk.Now}
	f.s.Scoring, f.s.Learn.Scoring, f.s.Blobs = f.sc, f.sc, f.sc.Blobs
	if err := f.s.Learn.SetItem(context.Background(), f.u.ID, "forge", "forge-301", "01-temper", "quiz", "complete", 1); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) startReviewLab(t *testing.T) *View {
	t.Helper()
	ctx := context.Background()
	v, err := f.s.Start(ctx, f.u, "forge", "forge-301", "02-review-lab")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if got, _ := f.s.Get(ctx, f.u, v.ID); got.State == Ready {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lab never became ready")
	return nil
}

func taskOf(v *View, id string) TaskView {
	for _, tv := range v.Tasks {
		if tv.ID == id {
			return tv
		}
	}
	return TaskView{}
}

func (f *fx) item(t *testing.T, module string) (string, float64) {
	t.Helper()
	var st string
	var score float64
	if err := f.s.DB.QueryRow(context.Background(), `SELECT status, score FROM item_progress WHERE user_id = $1 AND team = 'forge'
		AND training = 'forge-301' AND module = $2 AND item = 'lab'`, f.u.ID, module).Scan(&st, &score); err != nil {
		t.Fatal(err)
	}
	return st, score
}

func TestReviewTaskWaitsForScorer(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	if statusOf(v, "t2-proof") != "locked" {
		t.Fatalf("linear: %+v", v.Tasks)
	}
	if _, err := f.s.Check(ctx, f.u, v.ID, "t1-light", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Check(ctx, f.u, v.ID, "t2-proof", ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("review tasks have no Check: %v", err)
	}
	if _, err := f.s.SubmitReview(ctx, f.u, v.ID, "t1-light", "notes", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("checked tasks are not submitted: %v", err)
	}
	if _, err := f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "  ", nil); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("empty submission: %v", err)
	}
	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t2-proof"); err != nil { // costs 1 point
		t.Fatal(err)
	}
	v, err := f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "Wrote /tmp/proof and printed it.", nil)
	if err != nil {
		t.Fatal(err)
	}
	tv := taskOf(v, "t2-proof")
	if tv.Status != "submitted" || tv.Review == nil || tv.Review.Status != scoring.Pending || v.Complete {
		t.Fatalf("submitted: %+v complete=%v", tv, v.Complete)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "The transcript shows /tmp/proof") {
		t.Fatal("rubric leaked into the trainee's lab view")
	}
	if st, _ := f.item(t, "02-review-lab"); st != "pending_review" {
		t.Fatalf("lab item: %s", st)
	}
	if _, err := f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "again", nil); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("second submission: %v", err)
	}
	ev, err := f.s.Evidence(ctx, v.ID)
	if err != nil || !ev.SelfReported || len(ev.Tasks) != 2 || len(ev.Tasks[0].Checks) != 1 || ev.Tasks[0].Checks[0].ExitCode != 0 ||
		ev.Tasks[1].Kind != "review" || ev.Tasks[1].HintCost != 1 {
		t.Fatalf("evidence: %+v %v", ev, err)
	}
	if _, err := f.sc.Score(ctx, f.other, tv.Review.ID, 3, "Clean proof."); err != nil {
		t.Fatal(err)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	tv = taskOf(v, "t2-proof")
	if tv.Status != "passed" || tv.Awarded != 2 || tv.Review.Feedback != "Clean proof." || !v.Complete {
		t.Fatalf("after scoring (3 − 1 hint): %+v", tv)
	}
	if st, score := f.item(t, "02-review-lab"); st != "complete" || math.Abs(score-0.8) > 1e-9 {
		t.Fatalf("lab item: %s %.2f", st, score)
	}
}

func TestReturnedReviewReopensTheTask(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-light", "")
	v, err := f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "done", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sc.Return(ctx, f.other, taskOf(v, "t2-proof").Review.ID, "Show the file contents."); err != nil {
		t.Fatal(err)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if tv := taskOf(v, "t2-proof"); tv.Status != "open" || tv.Review.Status != scoring.Returned || tv.Review.Feedback != "Show the file contents." {
		t.Fatalf("returned: %+v", tv)
	}
	if st, _ := f.item(t, "02-review-lab"); st != "in_progress" {
		t.Fatalf("lab item: %s", st)
	}
	if _, err := f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "cat /tmp/proof prints tempered", nil); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
}

func TestOverrideIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-light", "")
	v, _ = f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "done", nil)
	if _, err := f.sc.Score(ctx, f.other, taskOf(v, "t2-proof").Review.ID, 3, ""); err != nil {
		t.Fatal(err)
	}
	noop := func(context.Context, pgx.Tx, float64) error { return nil }
	prev := -1.0
	if err := f.s.Override(ctx, v.ID, "t1-light", 1, func(_ context.Context, _ pgx.Tx, p float64) error { prev = p; return nil }); err != nil {
		t.Fatal(err)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if prev != 2 || taskOf(v, "t1-light").Awarded != 1 {
		t.Fatalf("override: prev %v, %+v", prev, taskOf(v, "t1-light"))
	}
	if _, score := f.item(t, "02-review-lab"); math.Abs(score-0.8) > 1e-9 { // (1 + 3) / 5: the score may go down
		t.Fatalf("lab score after override: %.2f", score)
	}
	if err := f.s.Override(ctx, v.ID, "t1-light", 0, func(context.Context, pgx.Tx, float64) error { return errors.New("audit down") }); err == nil {
		t.Fatal("a failed audit must fail the override")
	}
	if v, _ = f.s.Get(ctx, f.u, v.ID); taskOf(v, "t1-light").Awarded != 1 {
		t.Fatal("a failed audit must roll the override back")
	}
	if err := f.s.Override(ctx, v.ID, "t2-proof", 1, noop); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("review tasks are scored, not overridden: %v", err)
	}
	for _, p := range []float64{3, -1, math.NaN()} {
		if err := f.s.Override(ctx, v.ID, "t1-light", p, noop); !errors.Is(err, apperr.Invalid) {
			t.Fatalf("points %v: %v", p, err)
		}
	}
}

func TestSubmittedTaskDoesNotBlockTheNext(t *testing.T) {
	lab := &content.Lab{TaskOrder: "linear", Tasks: []*content.Task{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	got := taskStatuses(lab, map[string]taskRow{}, nil, map[string]*scoring.Submission{"a": {Status: scoring.Pending}})
	if got["a"] != "submitted" || got["b"] != "open" || got["c"] != "locked" {
		t.Fatalf("statuses: %v", got)
	}
}
```

Add the field `sc *scoring.Service` to the `fx` struct in `internal/labs/service_test.go` (import `"crucible/internal/scoring"` there).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/labs/ -run 'Review|Override|Submitted' -v`
Expected: FAIL (`undefined: (*Service).SubmitReview`, `too many arguments in call to taskStatuses`, …).

- [ ] **Step 4: Change `service.go`**

Import `"crucible/internal/scoring"`. Add to `Service`:

```go
	Scoring *scoring.Service // human review (M5); nil in tests that don't need it
	Blobs   blob.Store       // transcripts (Task 6)
```

(import `"crucible/internal/blob"` too; Task 6 uses `Blobs`.)

Add to `TaskView`:

```go
	Review        *scoring.Feedback `json:"review,omitempty"` // review tasks: the latest submission, never the rubric
```

and extend the `Status` comment to `// locked | open | setup_failed | submitted | passed | skipped`.

Replace `taskStatuses` with:

```go
func taskStatuses(lab *content.Lab, done map[string]taskRow, setups map[string]string, reviews map[string]*scoring.Submission) map[string]string {
	out := map[string]string{}
	opened := false
	for _, t := range lab.Tasks {
		if r, ok := done[t.ID]; ok {
			out[t.ID] = r.Status
			continue
		}
		if sub := reviews[t.ID]; sub != nil && sub.Status == scoring.Pending {
			out[t.ID] = "submitted" // with a scorer; later tasks go on (M5 ruling 4)
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
```

Add helpers:

```go
func (s *Service) instByID(ctx context.Context, labID string) (*Instance, error) {
	return scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
}

// reviews returns the latest review submission per task of this trainee's module.
func (s *Service) reviews(ctx context.Context, inst *Instance) (map[string]*scoring.Submission, error) {
	if s.Scoring == nil {
		return nil, nil
	}
	return s.Scoring.Latest(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, scoring.KindTask)
}
```

In `view`, after the `setups` lookup add:

```go
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return nil, err
	}
	statuses := taskStatuses(lab, done, setups, reviews)
```

(replacing the old `statuses :=` line), and in the task loop replace the `default:` branch with:

```go
		default:
			tv.Kind, tv.Review = "review", reviews[t.ID].Feedback()
```

In `readyTask`, fetch `reviews` the same way after `setups` and call `taskStatuses(lab, done, setups, reviews)`.

Replace `finishTask` with:

```go
func (s *Service) finishTask(ctx context.Context, inst *Instance, lab *content.Lab, taskID, status string, points float64) error {
	if _, err := s.DB.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status, points)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, status, points); err != nil {
		return err
	}
	return s.recompute(ctx, inst, lab)
}

// recompute sets the module's lab item from the task results: complete (with its score) when every task is done,
// pending_review when only scorers' decisions are missing (spec §7: progression waits on pending human scores).
func (s *Service) recompute(ctx context.Context, inst *Instance, lab *content.Lab) error {
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return err
	}
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return err
	}
	var score, maxScore float64
	waiting := false
	for _, t := range lab.Tasks {
		r, ok := done[t.ID]
		if !ok {
			if sub := reviews[t.ID]; sub != nil && sub.Status == scoring.Pending {
				waiting = true
				continue
			}
			return nil // not finished yet
		}
		score += r.Points
		if r.Status != "skipped" {
			maxScore += t.Points
		}
	}
	if waiting {
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "pending_review", 0)
	}
	if maxScore == 0 {
		maxScore = 1 // everything skipped
	}
	s.event(ctx, inst.ID, "completed", fmt.Sprintf("%.2f/%.2f", score, maxScore))
	if err := s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "complete", score/maxScore); err != nil {
		return err
	}
	return s.Learn.ForceScore(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", score/maxScore)
}
```

Fix any existing test that calls `taskStatuses(` with three arguments by adding `nil` as the fourth (`grep -n 'taskStatuses(' internal/labs/*_test.go`).

- [ ] **Step 5: Create `review.go`**

```go
package labs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"strings"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/scoring"
)

// SubmitReview hands a human_review task to the program's scorers with the trainee's notes and files (spec §4.5, §7).
func (s *Service) SubmitReview(ctx context.Context, u *auth.User, labID, taskID, notes string, files []*multipart.FileHeader) (*View, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if !task.HumanReview {
		return nil, apperr.Wrap(apperr.Conflict, "this task is checked automatically")
	}
	switch statuses[taskID] {
	case "open":
	case "locked":
		return nil, apperr.Wrap(apperr.Locked, "finish the earlier tasks first")
	case "setup_failed":
		return nil, apperr.Wrap(apperr.Conflict, "this scenario couldn't be prepared; skip the task instead")
	default:
		return nil, apperr.Wrap(apperr.Conflict, "this task is already with a scorer")
	}
	if task.Setup != nil && statuses["_setup:"+taskID] != "ok" {
		return nil, apperr.Wrap(apperr.Conflict, "the scenario is still being prepared")
	}
	if strings.TrimSpace(notes) == "" && len(files) == 0 {
		return nil, apperr.Wrap(apperr.Invalid, "tell the scorer what you did, or attach a file")
	}
	if s.Scoring == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "scoring is not available")
	}
	if _, err := s.Scoring.Submit(ctx, u, &scoring.Submission{Team: inst.Team, Training: inst.Training, Module: inst.Module,
		SHA: inst.SHA, Kind: scoring.KindTask, Item: taskID, LabID: inst.ID, QType: "review", Prompt: taskTitle(lab, task),
		Rubric: task.Rubric, MaxPoints: task.Points, Answer: notes}, files); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	if err := s.recompute(ctx, inst, lab); err != nil {
		return nil, err
	}
	return s.view(ctx, inst)
}

// Refresh implements scoring.Progress for review tasks. Scored: the task passes with the scorer's points minus the
// hint costs (M5 ruling 5). Returned: the task re-opens (taskStatuses sees no pending submission) and the item is
// back in progress.
func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error {
	inst, err := s.instByID(ctx, sub.LabID)
	if err != nil {
		return err
	}
	lab, _, err := s.labContent(inst)
	if err != nil {
		return err
	}
	if sub.Status == scoring.Returned {
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "in_progress", 0)
	}
	_, costs, err := s.hintCounts(ctx, inst)
	if err != nil {
		return err
	}
	return s.finishTask(ctx, inst, lab, sub.Item, "passed", max(0, sub.Points-costs[sub.Item]))
}

// Override sets the awarded points of an auto-checked task (spec §7). record writes the audit entry inside the same
// transaction, so an override is never stored unaudited.
func (s *Service) Override(ctx context.Context, labID, taskID string, points float64, record func(context.Context, pgx.Tx, float64) error) error {
	inst, err := s.instByID(ctx, labID)
	if err != nil {
		return err
	}
	lab, _, err := s.labContent(inst)
	if err != nil {
		return err
	}
	t := lab.Task(taskID)
	if t == nil {
		return apperr.Wrap(apperr.NotFound, "task not found")
	}
	if t.HumanReview {
		return apperr.Wrap(apperr.Conflict, "review tasks are scored from their submission")
	}
	if math.IsNaN(points) || points < 0 || points > t.Points {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("points must be between 0 and %g", t.Points))
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var prev float64
	err = tx.QueryRow(ctx, `SELECT points FROM lab_task_progress WHERE user_id = $1 AND team = $2 AND training = $3
		AND module = $4 AND task = $5 FOR UPDATE`, inst.UserID, inst.Team, inst.Training, inst.Module, taskID).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status, points)
		VALUES ($1, $2, $3, $4, $5, 'passed', $6)
		ON CONFLICT (user_id, team, training, module, task) DO UPDATE SET status = 'passed', points = EXCLUDED.points, updated_at = now()`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, points); err != nil {
		return err
	}
	if err := record(ctx, tx, prev); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.event(ctx, inst.ID, "override", fmt.Sprintf("%s %.2f→%.2f", taskID, prev, points))
	return s.recompute(ctx, inst, lab)
}

// Evidence is what a scorer sees next to a lab submission (spec §7): every task's result, check runs and hints across
// all of this trainee's labs for the module, and the recorded terminal sessions.
func (s *Service) Evidence(ctx context.Context, labID string) (*scoring.LabEvidence, error) {
	inst, err := s.instByID(ctx, labID)
	if err != nil {
		return nil, err
	}
	lab, _, err := s.labContent(inst)
	if err != nil {
		return nil, err
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return nil, err
	}
	counts, costs, err := s.hintCounts(ctx, inst)
	if err != nil {
		return nil, err
	}
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return nil, err
	}
	setups, err := s.setupStatus(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	statuses := taskStatuses(lab, done, setups, reviews)
	ev := &scoring.LabEvidence{Runtime: inst.Runtime, SelfReported: inst.Runtime == "local", Tasks: []scoring.TaskEvidence{},
		Transcripts: []scoring.Transcript{}}
	idx := map[string]int{}
	for _, t := range lab.Tasks {
		kind := "check"
		switch {
		case t.Quiz != "":
			kind = "quiz"
		case t.HumanReview:
			kind = "review"
		}
		idx[t.ID] = len(ev.Tasks)
		ev.Tasks = append(ev.Tasks, scoring.TaskEvidence{ID: t.ID, Title: taskTitle(lab, t), Kind: kind, Status: statuses[t.ID],
			Points: t.Points, Awarded: done[t.ID].Points, HintsUsed: counts[t.ID], HintCost: costs[t.ID], Checks: []scoring.CheckRun{}})
	}
	const module = `SELECT id FROM lab_instances WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`
	args := []any{inst.UserID, inst.Team, inst.Training, inst.Module}
	// ponytail: newest 200 check runs per module; page them if scorers ever need more.
	rows, err := s.DB.Query(ctx, `SELECT * FROM (SELECT id, lab_id, task, exit_code, output, answer, self_reported, at FROM check_runs
		WHERE lab_id IN (`+module+`) ORDER BY id DESC LIMIT 200) r ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var task string
		var c scoring.CheckRun
		if err := rows.Scan(&id, &c.LabID, &task, &c.ExitCode, &c.Output, &c.Answer, &c.SelfReported, &c.At); err != nil {
			return nil, err
		}
		if i, ok := idx[task]; ok {
			ev.Tasks[i].Checks = append(ev.Tasks[i].Checks, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := s.DB.Query(ctx, `SELECT id, lab_id, terminal, bytes, truncated, started_at, ended_at FROM terminal_transcripts
		WHERE lab_id IN (`+module+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var x scoring.Transcript
		if err := trows.Scan(&x.ID, &x.LabID, &x.Terminal, &x.Bytes, &x.Truncated, &x.StartedAt, &x.EndedAt); err != nil {
			return nil, err
		}
		ev.Transcripts = append(ev.Transcripts, x)
	}
	return ev, trows.Err()
}
```

- [ ] **Step 6: Add the route**

In `internal/labs/http.go` (import `"crucible/internal/scoring"`), after the `/skip` route:

```go
	r.Post("/api/labs/{id}/tasks/{task}/submit", func(w http.ResponseWriter, r *http.Request) {
		notes, files, done, err := scoring.ReadForm(w, r)
		defer done()
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SubmitReview(r.Context(), user(r), p(r, "id"), p(r, "task"), notes, files)
		reply(w, v, err)
	})
```

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/labs/ ./internal/learn/ ./internal/scoring/ -v && go run ./cmd/crucible lint examples/forge-301`
Expected: PASS, including every existing labs test (finishTask behaviour for checked labs is unchanged; completion now also calls `ForceScore`).

- [ ] **Step 8: Commit**

```bash
git add internal/labs examples/forge-301
git commit -m "feat(labs): review tasks go to scorers; lab evidence and audited overrides

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Terminal transcripts

**Files:**
- Create: `internal/labs/transcript.go`
- Create: `internal/labs/transcript_test.go`
- Modify: `internal/labs/http.go` (`terminal` records output; transcript route)

**Interfaces:**
- Consumes: `blob.Store` (Task 2), `terminal_transcripts` (Task 3 migration), `scoring.Service.CanView` (Task 3), `Service.Blobs` (Task 5).
- Produces: `func (s *Service) Transcript(ctx context.Context, u *auth.User, labID string, id int64) (io.ReadCloser, error)`; route `GET /api/labs/{id}/transcripts/{tid}` (raw bytes, `application/octet-stream`). Evidence (Task 5) now lists transcripts.

- [ ] **Step 1: Write the failing tests**

Create `internal/labs/transcript_test.go`:

```go
package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/blob"
	"crucible/internal/scoring"
)

func TestTailKeepsTheEnd(t *testing.T) {
	var tl tail
	tl.Write(bytes.Repeat([]byte("a"), transcriptMax))
	tl.Write([]byte("b"))
	got, truncated := tl.Bytes()
	if len(got) != transcriptMax || !truncated || got[len(got)-1] != 'b' || got[0] != 'a' {
		t.Fatalf("len %d truncated %v", len(got), truncated)
	}
	for i := 0; i < 5; i++ {
		tl.Write(bytes.Repeat([]byte("c"), transcriptMax))
	}
	if got, _ := tl.Bytes(); len(got) != transcriptMax || len(tl.buf) > 2*transcriptMax {
		t.Fatalf("memory must stay bounded: kept %d, buffer %d", len(got), len(tl.buf))
	}
	var small tail
	small.Write([]byte("hi"))
	if got, truncated := small.Bytes(); string(got) != "hi" || truncated {
		t.Fatalf("small: %q %v", got, truncated)
	}
}

func TestTerminalSessionIsRecorded(t *testing.T) {
	f := setup(t, true)
	f.s.Blobs = blob.Disk{Dir: t.TempDir()}
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
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/labs/"+v.ID+"/terminals/shell/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("echo tempered\n"))
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	ws.CloseNow()
	var key, terminal string
	for i := 0; i < 200; i++ {
		if err := f.s.DB.QueryRow(ctx, `SELECT blob_key, terminal FROM terminal_transcripts WHERE lab_id = $1`, v.ID).Scan(&key, &terminal); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if key == "" || terminal != "shell" {
		t.Fatal("no transcript saved when the session closed")
	}
	rc, err := f.s.Blobs.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(b), "echo tempered") {
		t.Fatalf("transcript %q", b)
	}
}

func TestTranscriptOnlyForViewers(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	f.s.Blobs = blob.Disk{Dir: t.TempDir()}
	f.s.Scoring = &scoring.Service{DB: f.s.DB, State: f.s.Learn.State}
	v := f.start(t)
	var tl tail
	tl.Write([]byte("$ ls\r\nforge\r\n"))
	f.s.saveTranscript(ctx, v.ID, "shell", f.clk.Now(), &tl)
	var id int64
	if err := f.s.DB.QueryRow(ctx, `SELECT id FROM terminal_transcripts WHERE lab_id = $1`, v.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, who := range []*auth.User{f.u, f.other} { // the trainee and a senior of the team
		rc, err := f.s.Transcript(ctx, who, v.ID, id)
		if err != nil {
			t.Fatalf("%s: %v", who.Email, err)
		}
		rc.Close()
	}
	store := auth.Store{DB: f.s.DB}
	stranger, _ := store.UpsertUser(ctx, "s9", "stranger@crucible.local", "Stan")
	if _, err := f.s.Transcript(ctx, stranger, v.ID, id); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := f.s.Transcript(ctx, f.u, "000000000000", id); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("wrong lab: %v", err)
	}
	ev, err := f.s.Evidence(ctx, v.ID)
	if err != nil || len(ev.Transcripts) != 1 || ev.Transcripts[0].Terminal != "shell" {
		t.Fatalf("evidence lists the transcript: %+v %v", ev, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/labs/ -run 'Tail|Transcript|Recorded' -v`
Expected: FAIL (`undefined: tail`).

- [ ] **Step 3: Implement `transcript.go`**

```go
package labs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
)

const transcriptMax = 512 << 10

// tail keeps the last transcriptMax bytes of one terminal session's output. Memory stays under 2 × transcriptMax.
type tail struct {
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*transcriptMax { // amortised: one copy per transcriptMax bytes of output
		t.buf = append([]byte(nil), t.buf[len(t.buf)-transcriptMax:]...)
		t.dropped = true
	}
}

func (t *tail) Bytes() ([]byte, bool) {
	if len(t.buf) > transcriptMax {
		return t.buf[len(t.buf)-transcriptMax:], true
	}
	return t.buf, t.dropped
}

// saveTranscript stores a finished session for scorers (spec §7, §13). Failures are logged: a lost transcript must not
// break the trainee's terminal. ponytail: saved when the session closes, so a still-open session is not visible yet.
func (s *Service) saveTranscript(ctx context.Context, labID, terminal string, started time.Time, t *tail) {
	b, truncated := t.Bytes()
	if len(b) == 0 || s.Blobs == nil {
		return
	}
	key := "uploads/transcripts/" + labID + "/" + newLabID()
	if err := s.Blobs.Put(ctx, key, bytes.NewReader(b), int64(len(b))); err != nil {
		s.Log.Error("saving terminal transcript failed", "lab", labID, "err", err)
		return
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO terminal_transcripts (lab_id, terminal, blob_key, bytes, truncated, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, labID, terminal, key, len(b), truncated, started, s.Now()); err != nil {
		s.Log.Error("recording terminal transcript failed", "lab", labID, "err", err)
	}
}

// Transcript opens a recorded session for the trainee or anyone who may view their progress; others get NotFound.
func (s *Service) Transcript(ctx context.Context, u *auth.User, labID string, id int64) (io.ReadCloser, error) {
	var key, team, training, owner string
	err := s.DB.QueryRow(ctx, `SELECT t.blob_key, l.team, l.training, u.email FROM terminal_transcripts t
		JOIN lab_instances l ON l.id = t.lab_id JOIN users u ON u.id = l.user_id
		WHERE t.id = $1 AND t.lab_id = $2`, id, labID).Scan(&key, &team, &training, &owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (s.Scoring == nil || !s.Scoring.CanView(u, team, training, owner))) {
		return nil, apperr.Wrap(apperr.NotFound, "transcript not found")
	}
	if err != nil {
		return nil, err
	}
	return s.Blobs.Get(ctx, key)
}
```

- [ ] **Step 4: Record in the terminal bridge and add the route**

In `internal/labs/http.go`'s `terminal` function, right after `ws.SetReadLimit(1 << 20)` add:

```go
	rec, started := &tail{}, s.Now() // spec §7: the scorer sees what the terminal printed
```

In the reader goroutine, replace

```go
			if n > 0 && ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
				return
			}
```

with

```go
			if n > 0 {
				rec.Write(buf[:n]) // only this goroutine writes rec until done is closed
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
```

and replace the deferred cleanup with:

```go
	defer func() {
		_ = pty.Close()
		ws.CloseNow()
		<-done
		s.saveTranscript(context.WithoutCancel(ctx), inst.ID, chi.URLParam(r, "name"), started, rec)
	}()
```

(import `"context"` and `"io"` in `http.go`). Add the route in `Routes`, after the terminal route:

```go
	r.Get("/api/labs/{id}/transcripts/{tid}", func(w http.ResponseWriter, r *http.Request) {
		tid, err := strconv.ParseInt(p(r, "tid"), 10, 64)
		if err != nil {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "transcript not found"))
			return
		}
		rc, err := s.Transcript(r.Context(), user(r), p(r, "id"), tid)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		_, _ = io.Copy(w, rc)
	})
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/labs/ -v`
Expected: PASS (including `TestTerminalBridge` and `TestTerminalOriginAndCleanup`).

- [ ] **Step 6: Commit**

```bash
git add internal/labs
git commit -m "feat(labs): record terminal output as transcripts for scorers

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 7: The Anvil API: queue, detail, sign-off list, overrides, file downloads

**Files:**
- Create: `internal/scoring/anvil.go`
- Create: `internal/scoring/http.go`
- Create: `internal/scoring/anvil_test.go`

**Interfaces:**
- Consumes: everything in `scoring.go` (Task 3); `scoring.Labs` is implemented by `*labs.Service` (Tasks 5–6) but tested here with a fake.
- Produces:
  ```go
  type Filter struct{ Training, Trainee, Type string }
  func (s *Service) Queue(ctx context.Context, u *auth.User, f Filter) ([]*Submission, error)
  type Detail struct{ Submission *Submission `json:"submission"`; History []*Submission `json:"history"`; Lab *LabEvidence `json:"lab,omitempty"` }
  func (s *Service) Detail(ctx context.Context, u *auth.User, id int64) (*Detail, error)
  type SignOff struct{ Team, Training, Module, Question, Prompt string; Points float64; Trainee, TraineeName string } // JSON snake_case
  func (s *Service) SignOffs(ctx context.Context, u *auth.User) ([]SignOff, error)
  func (s *Service) Override(ctx context.Context, u *auth.User, id int64, task string, points float64, reason string) (*LabEvidence, error)
  func (s *Service) File(ctx context.Context, u *auth.User, id int64, n int) (io.ReadCloser, string, error)
  func (s *Service) Routes(r chi.Router)
  ```
  Routes: `GET /api/anvil?training=&trainee=&type=`, `GET|POST /api/anvil/signoffs`, `GET /api/anvil/{id}`, `POST /api/anvil/{id}/score {points, feedback}`, `POST /api/anvil/{id}/return {feedback}`, `POST /api/anvil/{id}/override {task, points, reason}`, `GET /api/submissions/{id}/files/{n}`.

- [ ] **Step 1: Write the failing tests**

Create `internal/scoring/anvil_test.go`:

```go
package scoring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
)

type fakeLabs struct {
	fakeProgress
	db  *pgxpool.Pool
	err error
	got []string
}

func (l *fakeLabs) Evidence(context.Context, string) (*LabEvidence, error) {
	return &LabEvidence{Runtime: "local", SelfReported: true, Transcripts: []Transcript{},
		Tasks: []TaskEvidence{{ID: "t1-light", Kind: "check", Points: 2, Awarded: 2, Checks: []CheckRun{}}}}, nil
}

func (l *fakeLabs) Override(ctx context.Context, labID, task string, points float64, record func(context.Context, pgx.Tx, float64) error) error {
	if l.err != nil {
		return l.err
	}
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := record(ctx, tx, 2); err != nil {
		return err
	}
	l.got = append(l.got, fmt.Sprintf("%s %s %g", labID, task, points))
	return tx.Commit(ctx)
}

// insertLab adds a bare lab row for the FK. If M4 added NOT NULL columns without defaults to lab_instances, add them here.
func (f *fx) insertLab(t *testing.T) string {
	t.Helper()
	const id = "abcdefabcdef"
	if _, err := f.s.DB.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime,
		state, created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s)
		VALUES ($1, $2, 'forge', 'forge-301', '02-review-lab', 'abc', 'local', 'destroyed', now(), now(), 3600, 1200, 300, 0)`,
		id, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fx) submitReview(t *testing.T, labID string) *Submission {
	t.Helper()
	sub, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "02-review-lab",
		SHA: "abc", Kind: KindTask, Item: "t2-proof", LabID: labID, QType: "review", Prompt: "Prove your work", Rubric: "tempered",
		MaxPoints: 3, Answer: "wrote it"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestQueueIsScopedToScorers(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	f.submitText(t)
	// The senior is also enrolled as a trainee and answers the same question: never in their own queue.
	p := f.plat.Teams["forge"].Programs["forge-301"]
	p.Enrolled = append(p.Enrolled, "senior@crucible.local")
	if _, err := f.s.Submit(ctx, f.senior, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-why", QType: "text", Prompt: "Why?", MaxPoints: 5, Answer: "mine"}, nil); err != nil {
		t.Fatal(err)
	}
	count := func(u *auth.User, f2 Filter) int {
		list, err := f.s.Queue(ctx, u, f2)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			if strings.EqualFold(x.Email, u.Email) {
				t.Fatalf("%s sees their own submission", u.Email)
			}
		}
		return len(list)
	}
	if n := count(f.senior, Filter{}); n != 1 {
		t.Fatalf("senior queue = %d", n)
	}
	if n := count(f.admin, Filter{}); n != 2 {
		t.Fatalf("admin queue = %d", n)
	}
	if n := count(f.leader, Filter{}); n != 0 {
		t.Fatalf("leader queue = %d", n)
	}
	if n := count(f.admin, Filter{Type: "upload"}); n != 0 {
		t.Fatalf("type filter = %d", n)
	}
	if n := count(f.admin, Filter{Trainee: "TRAINEE@crucible.local", Training: "forge-301"}); n != 1 {
		t.Fatalf("trainee filter = %d", n)
	}
}

func TestDetailHistoryAndOverride(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	labs := &fakeLabs{db: f.s.DB}
	f.s.Labs = labs
	labID := f.insertLab(t)
	first := f.submitReview(t, labID)
	if _, err := f.s.Detail(ctx, f.trainee, first.ID); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainees don't open the scorer view: %v", err)
	}
	if _, err := f.s.Return(ctx, f.senior, first.ID, "Show the file."); err != nil {
		t.Fatal(err)
	}
	second := f.submitReview(t, labID)
	d, err := f.s.Detail(ctx, f.senior, second.ID)
	if err != nil || d.Lab == nil || len(d.History) != 1 || d.History[0].Note != "Show the file." || d.Submission.Rubric != "tempered" {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if _, err := f.s.Override(ctx, f.senior, second.ID, "t1-light", 1, "  "); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("override needs a reason: %v", err)
	}
	if _, err := f.s.Override(ctx, f.trainee, second.ID, "t1-light", 1, "mine"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee overriding own lab: %v", err)
	}
	if _, err := f.s.Override(ctx, f.senior, second.ID, "t1-light", 1, "Self-reported; transcript shows one touch."); err != nil {
		t.Fatal(err)
	}
	entries, _ := audit.Recent(ctx, f.s.DB, 1)
	if len(labs.got) != 1 || entries[0].Action != "score.override" || entries[0].Detail["reason"] != "Self-reported; transcript shows one touch." ||
		entries[0].Detail["from"] != float64(2) {
		t.Fatalf("override audited: %v %+v", labs.got, entries)
	}
	labs.err = apperr.Wrap(apperr.Conflict, "review tasks are scored from their submission")
	if _, err := f.s.Override(ctx, f.senior, second.ID, "t2-proof", 1, "x"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("labs refusal passes through: %v", err)
	}
	if after, _ := audit.Recent(ctx, f.s.DB, 1); !after[0].At.Equal(entries[0].At) {
		t.Fatal("a refused override must not be audited")
	}
	text := f.submitText(t)
	if _, err := f.s.Override(ctx, f.senior, text.ID, "t1-light", 1, "x"); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("questions have no checks: %v", err)
	}
}

func TestSignOffList(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	p := f.plat.Teams["forge"].Programs["forge-301"]
	p.Enrolled = append(p.Enrolled, "ghost@crucible.local") // never signed in: cannot be signed off yet
	list, err := f.s.SignOffs(ctx, f.senior)
	if err != nil || len(list) != 1 || list[0].Trainee != "trainee@crucible.local" || list[0].Question != "q-demo" || list[0].TraineeName != "Tara" {
		t.Fatalf("sign-offs: %+v %v", list, err)
	}
	if list, _ := f.s.SignOffs(ctx, f.leader); len(list) != 0 {
		t.Fatalf("leader is not a scorer: %+v", list)
	}
	if _, err := f.s.SignOff(ctx, f.senior, SignOffInput{Team: "forge", Training: "forge-301", Module: "01-temper", Question: "q-demo", Trainee: "trainee@crucible.local"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.s.SignOffs(ctx, f.senior); len(list) != 0 {
		t.Fatalf("signed-off demos leave the list: %+v", list)
	}
}

func TestDownloadIsAnAttachmentForViewersOnly(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	sub, err := f.s.Submit(ctx, f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", MaxPoints: 2},
		formFiles(t, map[string]string{"forge log.html": "<script>alert(1)</script>"}))
	if err != nil {
		t.Fatal(err)
	}
	get := func(u *auth.User, path string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), u)))
			})
		})
		f.s.Routes(r)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	path := fmt.Sprintf("/api/submissions/%d/files/0", sub.ID)
	for _, u := range []*auth.User{f.trainee, f.senior} {
		w := get(u, path)
		body, _ := io.ReadAll(w.Body)
		if w.Code != 200 || string(body) != "<script>alert(1)</script>" || w.Header().Get("Content-Type") != "application/octet-stream" ||
			!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
			w.Header().Get("Content-Security-Policy") != "sandbox" {
			t.Fatalf("%s: %d %v", u.Email, w.Code, w.Header())
		}
	}
	if w := get(f.stranger, path); w.Code != 404 {
		t.Fatalf("stranger: %d", w.Code)
	}
	if w := get(f.trainee, fmt.Sprintf("/api/submissions/%d/files/7", sub.ID)); w.Code != 404 {
		t.Fatalf("bad index: %d", w.Code)
	}
	if w := get(f.senior, "/api/anvil"); w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"upload"`) {
		t.Fatalf("queue over HTTP: %d %s", w.Code, w.Body)
	}
	if w := get(f.leader, "/api/anvil"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("an empty queue is [] (not null): %s", w.Body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/scoring/ -run 'Queue|Detail|SignOffList|Download' -v`
Expected: FAIL (`undefined: Filter`, …).

- [ ] **Step 3: Implement `anvil.go`**

```go
package scoring

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/rbac"
)

type Filter struct{ Training, Trainee, Type string }

// Queue lists pending submissions the user may score, oldest first (spec §7 "Scoring Queue … filter by
// training/trainee/type"). Nobody sees their own.
func (s *Service) Queue(ctx context.Context, u *auth.User, f Filter) ([]*Submission, error) {
	c, _, err := s.checker()
	if err != nil {
		return nil, err
	}
	list, err := collectSubs(s.DB.Query(ctx, `SELECT `+subCols+subFrom+`WHERE s.status = 'pending'
		AND ($1 = '' OR s.training = $1) AND ($2 = '' OR u.email = lower($2)) AND ($3 = '' OR s.qtype = $3)
		ORDER BY s.created_at, s.id LIMIT 500`, f.Training, strings.TrimSpace(f.Trainee), f.Type))
	if err != nil {
		return nil, err
	}
	// ponytail: RBAC filtered in Go over at most 500 rows (< 100 users); push into SQL if queues ever get long.
	list = slices.DeleteFunc(list, func(x *Submission) bool { return !c.Can(u.Email, rbac.Score, x.Team, x.Training, x.Email) })
	if list == nil {
		list = []*Submission{}
	}
	return list, nil
}

type Detail struct {
	Submission *Submission   `json:"submission"`
	History    []*Submission `json:"history"` // earlier answers to the same item, newest first
	Lab        *LabEvidence  `json:"lab,omitempty"`
}

func (s *Service) Detail(ctx context.Context, u *auth.User, id int64) (*Detail, error) {
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayScore(u, sub); err != nil {
		return nil, err
	}
	hist, err := collectSubs(s.DB.Query(ctx, `SELECT `+subCols+subFrom+`WHERE s.user_id = $1 AND s.team = $2 AND s.training = $3
		AND s.module = $4 AND s.kind = $5 AND s.item = $6 AND s.id <> $7 ORDER BY s.id DESC`,
		sub.UserID, sub.Team, sub.Training, sub.Module, sub.Kind, sub.Item, sub.ID))
	if err != nil {
		return nil, err
	}
	if hist == nil {
		hist = []*Submission{}
	}
	d := &Detail{Submission: sub, History: hist}
	if sub.LabID != "" && s.Labs != nil {
		if d.Lab, err = s.Labs.Evidence(ctx, sub.LabID); err != nil {
			return nil, err
		}
	}
	return d, nil
}

type SignOff struct {
	Team        string  `json:"team"`
	Training    string  `json:"training"`
	Module      string  `json:"module"`
	Question    string  `json:"question"`
	Prompt      string  `json:"prompt"`
	Points      float64 `json:"points"`
	Trainee     string  `json:"trainee"`
	TraineeName string  `json:"trainee_name"`
}

// SignOffs lists live demos the user can sign off: enrolled trainees (who have signed in) × signoff questions in the
// program's pinned version, without a pending or scored submission yet.
func (s *Service) SignOffs(ctx context.Context, u *auth.User) ([]SignOff, error) {
	c, st, err := s.checker()
	if err != nil {
		return nil, err
	}
	var cands []SignOff
	var emails []string
	for teamID, team := range st.Platform.Teams {
		for trID, p := range team.Programs {
			t, _ := st.ProgramTraining(teamID, trID)
			if t == nil {
				continue
			}
			for _, m := range t.Modules {
				if m.Quiz == nil {
					continue
				}
				for _, q := range m.Quiz.Questions {
					if q.Type != "signoff" {
						continue
					}
					for _, e := range p.Enrolled {
						if c.Can(u.Email, rbac.Score, teamID, trID, e) {
							cands = append(cands, SignOff{Team: teamID, Training: trID, Module: m.ID, Question: q.ID, Prompt: q.Prompt, Points: q.Points, Trainee: e})
							emails = append(emails, e)
						}
					}
				}
			}
		}
	}
	out := []SignOff{}
	if len(cands) == 0 {
		return out, nil
	}
	names := map[string]string{}
	rows, err := s.DB.Query(ctx, `SELECT email, name FROM users WHERE email = ANY($1)`, emails)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e, n string
		if err := rows.Scan(&e, &n); err != nil {
			rows.Close()
			return nil, err
		}
		names[e] = n
	}
	rows.Close()
	type key struct{ email, team, training, module, item string }
	done := map[key]bool{}
	rows, err = s.DB.Query(ctx, `SELECT u.email, s.team, s.training, s.module, s.item FROM submissions s JOIN users u ON u.id = s.user_id
		WHERE s.qtype = 'signoff' AND s.status IN ('pending', 'scored') AND u.email = ANY($1)`, emails)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (key, error) {
		var k key
		err := r.Scan(&k.email, &k.team, &k.training, &k.module, &k.item)
		return k, err
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		done[k] = true
	}
	for _, x := range cands {
		name, signedIn := names[x.Trainee]
		if !signedIn || done[key{x.Trainee, x.Team, x.Training, x.Module, x.Question}] {
			continue
		}
		x.TraineeName = name
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return strings.Join([]string{a.Team, a.Training, a.Trainee, a.Module, a.Question}, "\x00") <
			strings.Join([]string{b.Team, b.Training, b.Trainee, b.Module, b.Question}, "\x00")
	})
	return out, nil
}

// Override changes an auto-checked task's points in a lab submission's module, with a reason (spec §7, audited in the
// same transaction as the change).
func (s *Service) Override(ctx context.Context, u *auth.User, id int64, task string, points float64, reason string) (*LabEvidence, error) {
	reason = Clean(strings.TrimSpace(reason))
	if reason == "" {
		return nil, apperr.Wrap(apperr.Invalid, "an override needs a reason")
	}
	if utf8.RuneCountInString(reason) > 500 {
		return nil, apperr.Wrap(apperr.Invalid, "keep the reason under 500 characters")
	}
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if sub.LabID == "" || s.Labs == nil {
		return nil, apperr.Wrap(apperr.Conflict, "only lab submissions have check results to override")
	}
	if err := s.mayScore(u, sub); err != nil {
		return nil, err
	}
	err = s.Labs.Override(ctx, sub.LabID, task, points, func(ctx context.Context, tx pgx.Tx, prev float64) error {
		return audit.Log(ctx, tx, u.Email, "score.override", "lab/"+sub.LabID+"/"+task, map[string]any{"trainee": sub.Email,
			"submission": sub.ID, "from": prev, "to": points, "reason": reason}, "")
	})
	if err != nil {
		return nil, err
	}
	return s.Labs.Evidence(ctx, sub.LabID)
}

// File opens one uploaded file for someone who may view the trainee's progress; everyone else gets NotFound.
func (s *Service) File(ctx context.Context, u *auth.User, id int64, n int) (io.ReadCloser, string, error) {
	sub, err := s.get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !s.CanView(u, sub.Team, sub.Training, sub.Email) || n < 0 || n >= len(sub.Keys) || n >= len(sub.Files) {
		return nil, "", apperr.Wrap(apperr.NotFound, "file not found")
	}
	rc, err := s.Blobs.Get(ctx, sub.Keys[n])
	if err != nil {
		return nil, "", fmt.Errorf("opening %s: %w", sub.Files[n].Name, err)
	}
	return rc, sub.Files[n].Name, nil
}
```

- [ ] **Step 4: Implement `http.go`**

```go
package scoring

import (
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/httpx"
)

func (s *Service) Routes(r chi.Router) {
	user := func(r *http.Request) *auth.User { return auth.UserFrom(r.Context()) }
	id := func(r *http.Request) (int64, error) {
		v, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			return 0, apperr.Wrap(apperr.NotFound, "submission not found")
		}
		return v, nil
	}
	r.Get("/api/anvil", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := s.Queue(r.Context(), user(r), Filter{Training: q.Get("training"), Trainee: q.Get("trainee"), Type: q.Get("type")})
		reply(w, v, err)
	})
	r.Get("/api/anvil/signoffs", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.SignOffs(r.Context(), user(r))
		reply(w, v, err)
	})
	r.Post("/api/anvil/signoffs", func(w http.ResponseWriter, r *http.Request) {
		var in SignOffInput
		if err := httpx.Read(r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.SignOff(r.Context(), user(r), in)
		reply(w, v, err)
	})
	r.Get("/api/anvil/{id}", func(w http.ResponseWriter, r *http.Request) {
		n, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Detail(r.Context(), user(r), n)
		reply(w, v, err)
	})
	r.Post("/api/anvil/{id}/score", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Points   *float64 `json:"points"` // required: {} must never score 0
			Feedback string   `json:"feedback"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err == nil && body.Points == nil {
			err = apperr.Wrap(apperr.Invalid, "points are required")
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Score(r.Context(), user(r), n, *body.Points, body.Feedback)
		reply(w, v, err)
	})
	r.Post("/api/anvil/{id}/return", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Feedback string `json:"feedback"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Return(r.Context(), user(r), n, body.Feedback)
		reply(w, v, err)
	})
	r.Post("/api/anvil/{id}/override", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Task   string   `json:"task"`
			Points *float64 `json:"points"`
			Reason string   `json:"reason"`
		}
		n, err := id(r)
		if err == nil {
			err = httpx.Read(r, &body)
		}
		if err == nil && body.Points == nil {
			err = apperr.Wrap(apperr.Invalid, "points are required")
		}
		if err != nil {
			httpx.Error(w, err)
			return
		}
		v, err := s.Override(r.Context(), user(r), n, body.Task, *body.Points, body.Reason)
		reply(w, v, err)
	})
	r.Get("/api/submissions/{id}/files/{n}", func(w http.ResponseWriter, r *http.Request) {
		sid, err := id(r)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		n, err := strconv.Atoi(chi.URLParam(r, "n"))
		if err != nil {
			httpx.Error(w, apperr.Wrap(apperr.NotFound, "file not found"))
			return
		}
		rc, name, err := s.File(r.Context(), user(r), sid, n)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		defer rc.Close()
		// Uploads are trainee-controlled: never rendered in our origin.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		_, _ = io.Copy(w, rc)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/scoring/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/scoring
git commit -m "feat(scoring): Anvil queue, detail with evidence, sign-off list, overrides and downloads

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Wire it up: API, `/api/me`, Helm, node IAM

**Files:**
- Modify: `cmd/crucible-api/main.go`
- Modify: `internal/httpapi/server.go` (`Deps.Scoring`, routes, `can_score`)
- Create: `internal/httpapi/me_test.go`
- Modify: `deploy/helm/crucible/templates/crucible.yaml`, `deploy/helm/test.sh`
- Modify: `deploy/aws/main/main.tf`, `deploy/aws/main/main.tftest.hcl`
- Modify: `docs/runbooks/aws.md`

**Interfaces:**
- Consumes: `blob.Disk`, `blob.NewS3` (Task 2); `scoring.Service` (Tasks 3, 7); `learn.Service.Scoring` (Task 4); `labs.Service.Scoring/Blobs` (Tasks 5–6).
- Produces: env `CRUCIBLE_BLOB_BUCKET` (empty = disk), `CRUCIBLE_BLOB_REGION`, `CRUCIBLE_BLOB_ENDPOINT` (tests/S3-compatible only); `/api/me` gains `can_score`; `func canScore(p *config.Platform, email string) bool` in `httpapi`.

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/me_test.go`:

```go
package httpapi

import (
	"testing"

	"crucible/internal/config"
)

func TestCanScore(t *testing.T) {
	p, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	for email, want := range map[string]bool{
		"senior@crucible.local":  true, // seniors are scorers by default
		"admin@crucible.local":   true,
		"trainee@crucible.local": false,
		"leader@crucible.local":  false,
	} {
		if got := canScore(p, email); got != want {
			t.Errorf("%s: %v", email, got)
		}
	}
}
```

Run: `go test ./internal/httpapi/ -run TestCanScore -v`
Expected: FAIL (`undefined: canScore`).

- [ ] **Step 2: Implement `server.go` changes**

Import `"crucible/internal/scoring"` and `"crucible/internal/config"` (if not already). Add to `Deps`:

```go
	Scoring    *scoring.Service
```

Add:

```go
// canScore: admins, and anyone listed as a scorer of some program (the Anvil link in the nav).
func canScore(p *config.Platform, email string) bool {
	if (rbac.Checker{P: p}).IsAdmin(email) {
		return true
	}
	for _, t := range p.Teams {
		for _, pr := range t.Programs {
			if slices.Contains(pr.Roles.Scorers, strings.ToLower(email)) {
				return true
			}
		}
	}
	return false
}
```

In the `/api/me` handler, declare `scorer := false`, set `scorer = canScore(st.Platform, u.Email)` inside the `if st := state(d); …` block, and add `"can_score": scorer` to the JSON map. After `d.Labs.Routes(r)` add:

```go
		if d.Scoring != nil {
			d.Scoring.Routes(r)
		}
```

- [ ] **Step 3: Wire `main.go`**

Import `"crucible/internal/blob"` and `"crucible/internal/scoring"`. After `labSvc := &labs.Service{…}` add:

```go
	var blobs blob.Store = blob.Disk{Dir: filepath.Join(env("CRUCIBLE_DATA_DIR", "/data"), "blobs")}
	if bucket := os.Getenv("CRUCIBLE_BLOB_BUCKET"); bucket != "" {
		s3store, err := blob.NewS3(ctx, bucket, os.Getenv("CRUCIBLE_BLOB_REGION"), os.Getenv("CRUCIBLE_BLOB_ENDPOINT"))
		if err != nil {
			return fmt.Errorf("blob storage: %w", err)
		}
		blobs = s3store
	} else {
		slog.Warn("CRUCIBLE_BLOB_BUCKET is not set: uploads and terminal transcripts are kept under CRUCIBLE_DATA_DIR/blobs; use a persistent volume or S3 in production")
	}
	scoreSvc := &scoring.Service{DB: pool, Blobs: blobs, State: syncer.Current, Notify: notifySvc, Quiz: learnSvc, Labs: labSvc,
		Log: slog.Default(), Now: time.Now}
	learnSvc.Scoring = scoreSvc
	labSvc.Scoring, labSvc.Blobs = scoreSvc, blobs
```

and pass `Scoring: scoreSvc` in `httpapi.Deps{…}`.

- [ ] **Step 4: Helm and IAM**

In `deploy/helm/crucible/templates/crucible.yaml`, add to the `api` container's `env` (after `CRUCIBLE_QUIZ_SECRET`):

```yaml
            - { name: CRUCIBLE_BLOB_BUCKET, value: {{ .Values.backup.bucket | quote }} }   # uploads/ and transcripts (M5)
            - { name: CRUCIBLE_BLOB_REGION, value: {{ .Values.backup.region | quote }} }
```

In `deploy/helm/test.sh`, before `echo "helm chart OK"`:

```bash
need 'CRUCIBLE_BLOB_BUCKET, value: "b"'                    # uploads go to the data bucket, not the emptyDir
need 'CRUCIBLE_BLOB_REGION, value: "eu-west-1"'
```

In `deploy/aws/main/main.tf`, extend the `s3:PutObject` statement's `resources` with `"arn:aws:s3:::${var.data_bucket}/uploads/*"`.

In `deploy/aws/main/main.tftest.hcl`, rename `run "backups_may_write_only_snapshots_and_latest"` to `run "node_may_write_only_snapshots_latest_and_uploads"` and make its expected set:

```hcl
    condition = toset(one([for s in data.aws_iam_policy_document.node.statement : s.resources if contains(s.actions, "s3:PutObject")])) == toset([
      "arn:aws:s3:::crucible-123456789012-data/snapshots/*",
      "arn:aws:s3:::crucible-123456789012-data/latest/crucible-latest.dump",
      "arn:aws:s3:::crucible-123456789012-data/uploads/*",
    ])
    error_message = "node may PutObject only on snapshots/*, latest/crucible-latest.dump and uploads/*"
```

In `docs/runbooks/aws.md`, add a short paragraph where the data bucket is described:

```markdown
**Uploads and transcripts.** Files trainees attach for scorers and recorded terminal sessions live under
`s3://<data bucket>/uploads/`. That prefix has no lifecycle rule and is not touched by `crucible aws teardown`, so a
rebuilt platform (snapshot restore) still serves them. The node role may write only there, to `snapshots/` and to
`latest/crucible-latest.dump`.
```

- [ ] **Step 5: Run everything offline**

Run: `go build ./... && go vet ./... && go test -race ./... && bash deploy/helm/test.sh && (cd deploy/aws/main && terraform init -backend=false -input=false >/dev/null && terraform test)`
Expected: all PASS; `helm chart OK`; terraform tests pass with the mock provider (no AWS calls).

- [ ] **Step 6: Commit**

```bash
git add cmd/crucible-api internal/httpapi deploy/helm deploy/aws/main docs/runbooks/aws.md
git commit -m "feat: wire scoring and blob storage; uploads bucket in Helm and node IAM

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: Web, trainee side: human questions, review tasks, feedback

**Files:**
- Modify: `web/src/types.ts`, `web/src/api.ts`
- Create: `web/src/components/Feedback.tsx`
- Modify: `web/src/pages/Quiz.tsx`, `web/src/pages/Training.tsx`, `web/src/pages/Lab.tsx`
- Modify: `web/src/theme/app.css`

**Interfaces:**
- Consumes: the JSON from Tasks 4–5 (`PublicQuestion.submission`, `QuizResult.status`, item status `pending_review`, `TaskView.review`, task status `submitted`, `POST …/quiz/questions/{q}/answer`, `POST /api/labs/{id}/tasks/{task}/submit`, `GET /api/submissions/{id}/files/{n}`).
- Produces (for Task 10 and the e2e in Task 11): `upload<T>(path, form)` in `api.ts`; `<FeedbackBox f>` with `data-testid="feedback"`; quiz human questions in `<fieldset data-testid="human-<qid>">` with inputs labelled `Answer to <qid>`, `Link for <qid>`, `Files for <qid>` and buttons `Submit for scoring` / `Resubmit for scoring`; result messages `Choices scored. Answer the questions a person scores below.` and `Submitted: a scorer still has to read your answers.`; outline badge `Awaiting the hammer`; lab review form with `Notes for the scorer`, `Files for the scorer`, button `Submit for review`; lab header text `Terminal output is recorded`.

- [ ] **Step 1: Types and the upload helper**

In `web/src/types.ts`:
- change `ItemView.status` to `'new' | 'in_progress' | 'pending_review' | 'complete'`;
- add `submission?: Feedback` to `PublicQuestion`; add `status: 'in_progress' | 'pending_review' | 'complete'` to `QuizResult`;
- change `TaskStatus` to `'locked' | 'open' | 'setup_failed' | 'submitted' | 'passed' | 'skipped'`; add `review?: Feedback` to `TaskView`;
- add `can_score: boolean` to `Me`;
- append:

```ts
export type SubmissionType = 'text' | 'upload' | 'signoff' | 'review'
export type SubmissionFile = { name: string; size: number }
export type Feedback = {
  id: number; status: 'pending' | 'scored' | 'returned'; answer: string; files: SubmissionFile[]
  points: number; max_points: number; feedback: string; scored_by?: string
}
export type Submission = {
  id: number; trainee: string; trainee_name: string; team: string; training: string; module: string
  kind: 'question' | 'task'; item: string; lab_id?: string; type: SubmissionType; prompt: string; rubric: string
  max_points: number; answer: string; files: SubmissionFile[]; status: Feedback['status']; points: number
  feedback: string; scored_by?: string; scored_at?: string; created_at: string
}
export type CheckRun = { lab_id: string; at: string; exit_code: number; output: string; answer?: string; self_reported: boolean }
export type TaskEvidence = {
  id: string; title: string; kind: 'check' | 'quiz' | 'review'; status: string; points: number; awarded: number
  hints_used: number; hint_cost: number; checks: CheckRun[]
}
export type TranscriptInfo = { id: number; lab_id: string; terminal: string; bytes: number; truncated: boolean; started_at: string; ended_at: string }
export type LabEvidence = { runtime: string; self_reported: boolean; tasks: TaskEvidence[]; transcripts: TranscriptInfo[] }
export type AnvilDetail = { submission: Submission; history: Submission[]; lab?: LabEvidence }
export type SignOff = { team: string; training: string; module: string; question: string; prompt: string; points: number; trainee: string; trainee_name: string }
```

Append to `web/src/api.ts`:

```ts
// upload posts a multipart form. The header proves the request came from this app (the server refuses forms without it).
export function upload<T>(path: string, form: FormData): Promise<T> {
  return api<T>(path, { method: 'POST', body: form, headers: { 'X-Crucible-Upload': '1' } })
}
```

- [ ] **Step 2: FeedbackBox**

Create `web/src/components/Feedback.tsx`:

```tsx
import type { Feedback } from '../types'

const kb = (n: number) => (n < 1024 ? `${n} B` : `${Math.ceil(n / 1024)} KiB`)

// What a trainee sees of their own human-scored work. Feedback is plain text (pre-wrap), never HTML or Markdown.
export function FeedbackBox({ f }: { f: Feedback }) {
  const label = { pending: 'Waiting for a scorer', scored: `Scored ${f.points} / ${f.max_points}`, returned: 'Returned for rework' }[f.status]
  return (
    <div className={`feedback ${f.status}`} role="status" data-testid="feedback">
      <strong>{label}</strong>
      {f.scored_by && f.status !== 'pending' && <span className="muted"> · {f.scored_by}</span>}
      {f.feedback && <p className="pre">{f.feedback}</p>}
      {f.answer && <p className="muted pre">You wrote: {f.answer}</p>}
      {f.files.map((file, i) => (
        <p key={i}><a href={`/api/submissions/${f.id}/files/${i}`}>{file.name}</a> <span className="muted">({kb(file.size)})</span></p>
      ))}
    </div>
  )
}
```

- [ ] **Step 3: Quiz page**

In `web/src/pages/Quiz.tsx`: import `upload` from `'../api'` and `FeedbackBox` from `'../components/Feedback'`; replace the `default:` branch of `Question` with `default:\n      return null`; add this component:

```tsx
function HumanQuestion({ q, n, base, onSaved }: { q: PublicQuestion; n: number; base: string; onSaved: () => void }) {
  const sub = q.submission
  const [text, setText] = useState('')
  const [files, setFiles] = useState<FileList | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const canAnswer = q.type !== 'signoff' && (!sub || sub.status === 'returned')
  const send = async () => {
    const form = new FormData()
    form.set('answer', text)
    for (const f of Array.from(files ?? [])) form.append('file', f)
    setBusy(true)
    setErr(undefined)
    try {
      await upload(`${base}/questions/${q.id}/answer`, form)
      setText('')
      setFiles(null)
      onSaved()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset className="question human" data-testid={`human-${q.id}`}>
      <legend>{n}. {q.prompt} <span className="muted">({q.points} pts, scored by a person)</span></legend>
      {sub && <FeedbackBox f={sub} />}
      {q.type === 'signoff' && !sub && <p className="muted">A scorer signs this off after you show them live.</p>}
      {canAnswer && (
        <>
          {q.type === 'text' ? (
            <textarea aria-label={`Answer to ${q.id}`} rows={5} maxLength={20000} value={text} onChange={(e) => setText(e.target.value)} />
          ) : (
            <>
              <input type="url" aria-label={`Link for ${q.id}`} placeholder="https://… (or attach files)" value={text} onChange={(e) => setText(e.target.value)} />
              <input type="file" multiple aria-label={`Files for ${q.id}`} onChange={(e) => setFiles(e.target.files)} />
            </>
          )}
          <div className="row">
            <button className="primary" disabled={busy} onClick={send}>{sub ? 'Resubmit for scoring' : 'Submit for scoring'}</button>
          </div>
          {err && <p className="error" role="alert">{err}</p>}
        </>
      )}
    </fieldset>
  )
}
```

In `QuizPage`: take `reload` from `useFetch` (`const { data, error, reload } = useFetch<QuizView>(base)`); after the loader guard add `const instant = data.questions.filter((q) => !q.human)`; render

```tsx
      {data.status === 'pending_review' && <p className="banner">Waiting on the anvil: a scorer still has to read some of your answers.</p>}
      {instant.map((q, i) => (
        <Question key={q.id} q={q} n={i + 1} value={answers[q.id]} onChange={(v) => { setResult(undefined); setAnswers((a) => ({ ...a, [q.id]: v })) }} verdict={result?.correct[q.id]} />
      ))}
      {instant.length > 0 && (
        <div className="row">
          <button className="primary" disabled={busy} onClick={submit}>Submit answers</button>
          <SparkBurst trigger={spark} />
        </div>
      )}
      {data.questions.filter((q) => q.human).map((q, i) => (
        <HumanQuestion key={q.id} q={q} n={instant.length + i + 1} base={base} onSaved={reload} />
      ))}
```

in place of the old questions map and button row, and replace the result text inside the `role="status"` div with:

```tsx
        {result && (<>
          {result.status === 'complete'
            ? `Passed: ${pct}%. Tempered!`
            : result.status === 'pending_review'
              ? 'Submitted: a scorer still has to read your answers.'
              : result.pending_human
                ? 'Choices scored. Answer the questions a person scores below.'
                : `Not yet: ${pct}%. Reheat and try again.`}{' '}
          {result.passed && <Link to={`/p/${team}/${training}`}>Back to the training</Link>}
        </>)}
```

- [ ] **Step 4: Training badge**

In `web/src/pages/Training.tsx` change `statusLabel` to:

```ts
const statusLabel = { new: 'Cold', in_progress: 'Heating', pending_review: 'Awaiting the hammer', complete: 'Forged' } as const
```

- [ ] **Step 5: Lab page**

In `web/src/pages/Lab.tsx`: import `upload` alongside `api`, and `FeedbackBox` from `'../components/Feedback'`. Add:

```tsx
function ReviewForm({ base, onLab, onAdvance }: { base: string; onLab: (l: LabView) => void; onAdvance: (l: LabView) => void }) {
  const [notes, setNotes] = useState('')
  const [files, setFiles] = useState<FileList | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const send = async () => {
    const form = new FormData()
    form.set('answer', notes)
    for (const f of Array.from(files ?? [])) form.append('file', f)
    setBusy(true)
    setErr(undefined)
    try {
      const l = await upload<LabView>(`${base}/submit`, form)
      onLab(l)
      onAdvance(l)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="review-form">
      <label>Notes for the scorer <textarea rows={4} maxLength={20000} value={notes} onChange={(e) => setNotes(e.target.value)} /></label>
      <label>Files for the scorer <input type="file" multiple onChange={(e) => setFiles(e.target.files)} /></label>
      <button className="primary" disabled={busy} onClick={send}>{busy ? 'Submitting…' : 'Submit for review'}</button>
      {err && <p className="error" role="alert">{err}</p>}
    </div>
  )
}
```

In `TaskPanel`, replace `{task.kind === 'review' && <span className="muted">A scorer reviews this task.</span>}` with
`{task.kind === 'review' && task.status === 'open' && <ReviewForm base={base} onLab={onLab} onAdvance={onAdvance} />}`
and directly after the `.row` div add `{task.review && <FeedbackBox f={task.review} />}`.

In the lab header, after the self-reported badge, add:

```tsx
        <span className="muted small" title="Scorers can read what your terminals printed">Terminal output is recorded</span>
```

In the lobby's "The forge has cooled" block, after the score paragraph, add:

```tsx
          {lab.tasks.filter((t) => t.review).map((t) => (
            <div key={t.id}><h3>{t.title}</h3><FeedbackBox f={t.review!} /></div>
          ))}
```

- [ ] **Step 6: Styles**

Append to `web/src/theme/app.css` (tokens only, so all four themes work):

```css
.badge.pending_review { color: var(--accent); }
.pip.submitted { border-style: dashed; border-color: var(--accent-2); }
.feedback { border-left: 3px solid var(--accent-2); padding: 0.5rem 0.75rem; margin: 0.5rem 0; background: var(--surface-2); }
.feedback.scored { border-left-color: var(--ok); }
.feedback.returned { border-left-color: var(--accent); }
.pre { white-space: pre-wrap; overflow-wrap: anywhere; }
.question.human textarea, .review-form textarea { width: 100%; }
.review-form { display: grid; gap: 0.5rem; margin: 0.75rem 0; }
.small { font-size: 0.8rem; }
```

(If a class already exists with the same name, such as `.small` or `.pre`, keep the existing rule and drop the duplicate.)

- [ ] **Step 7: Build and test**

Run: `cd web && npm run build && npm test`
Expected: type-check and build succeed; existing Vitest suites pass.

- [ ] **Step 8: Commit**

```bash
git add web/src
git commit -m "feat(web): human questions, review tasks and feedback for trainees

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Web, the Anvil: queue, detail, sign-offs, overrides, transcripts

**Files:**
- Create: `web/src/components/Transcript.tsx`
- Create: `web/src/pages/Anvil.tsx`
- Modify: `web/src/App.tsx` (routes), `web/src/components/Nav.tsx` (link)
- Modify: `web/src/theme/app.css`

**Interfaces:**
- Consumes: Task 7's routes, `GET /api/labs/{id}/transcripts/{tid}` (Task 6), types from Task 9.
- Produces (used by the e2e): nav link `Anvil`; page heading `Anvil`; queue links named `Submission from <email>: <prompt>`; detail with `data-testid="rubric"`, `data-testid="answer"`, inputs labelled exactly `Points` and `Feedback`, buttons `Score` and `Return for rework`; evidence sections (`region`) named `Task <title>` holding `data-testid="awarded"` (`"<awarded> / <points>"`), inputs `Override points` and `Reason`, button `Override`; transcript buttons `Show transcript: <terminal>` revealing `data-testid="transcript-<id>"`; sign-off list items named `Sign-off for <email>: <prompt>` with button `Mark passed`; empty texts `Nothing on the anvil. Every piece is scored.` and `No live sign-offs waiting.`

- [ ] **Step 1: Read-only transcript view**

Create `web/src/components/Transcript.tsx`:

```tsx
import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'

function cssVar(name: string) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

// Replays a recorded terminal session (raw PTY bytes, colours and all) in a terminal nobody can type into.
export function TranscriptView({ labId, id }: { labId: string; id: number }) {
  const el = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const term = new XTerm({
      disableStdin: true, cursorBlink: false, scrollback: 20000, rows: 24, fontSize: 13,
      fontFamily: "'JetBrains Mono', Menlo, monospace",
      theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg') },
    })
    term.open(el.current!)
    let gone = false
    fetch(`/api/labs/${labId}/transcripts/${id}`, { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((b) => { if (!gone) term.write(new Uint8Array(b)) })
      .catch((e: Error) => { if (!gone) term.write(`[transcript unavailable: ${e.message}]`) })
    return () => { gone = true; term.dispose() }
  }, [labId, id])
  return <div ref={el} className="terminal transcript" data-testid={`transcript-${id}`} />
}
```

- [ ] **Step 2: The Anvil page**

Create `web/src/pages/Anvil.tsx`:

```tsx
import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../api'
import { useFetch } from '../useFetch'
import type { AnvilDetail, LabEvidence, SignOff, Submission, SubmissionType, TaskEvidence } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'
import { TranscriptView } from '../components/Transcript'
import { toast } from '../lib/alerts'

const typeLabel: Record<SubmissionType, string> = { text: 'Written answer', upload: 'Upload', signoff: 'Live sign-off', review: 'Lab review' }
const kb = (n: number) => (n < 1024 ? `${n} B` : `${Math.ceil(n / 1024)} KiB`)

export function AnvilPage() {
  const [draft, setDraft] = useState({ training: '', trainee: '', type: '' })
  const [filter, setFilter] = useState(draft)
  const { data, error } = useFetch<Submission[]>(`/api/anvil?${new URLSearchParams(filter)}`, 30_000)
  const signoffs = useFetch<SignOff[]>('/api/anvil/signoffs')
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Heating the anvil…" />
  return (
    <section className="page anvil">
      <h1>Anvil</h1>
      <form className="row filters" onSubmit={(e) => { e.preventDefault(); setFilter(draft) }}>
        <label>Training <input value={draft.training} placeholder="any" onChange={(e) => setDraft({ ...draft, training: e.target.value })} /></label>
        <label>Trainee <input value={draft.trainee} placeholder="email" onChange={(e) => setDraft({ ...draft, trainee: e.target.value })} /></label>
        <label>Type
          <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })}>
            <option value="">any</option>
            {Object.entries(typeLabel).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </label>
        <button className="ghost" type="submit">Filter</button>
      </form>
      {data.length === 0 && <p className="muted">Nothing on the anvil. Every piece is scored.</p>}
      <ul className="queue">
        {data.map((s) => (
          <li key={s.id} className="card">
            <Link to={`/anvil/${s.id}`} aria-label={`Submission from ${s.trainee}: ${s.prompt}`}>
              <strong>{s.trainee_name || s.trainee}</strong> · {typeLabel[s.type]} · {s.training} / {s.module}
              <span className="muted"> · {new Date(s.created_at).toLocaleString()}</span>
              <p>{s.prompt}</p>
            </Link>
          </li>
        ))}
      </ul>
      <h2>Live sign-offs</h2>
      {signoffs.error && <ErrorBox error={signoffs.error} />}
      {signoffs.data?.length === 0 && <p className="muted">No live sign-offs waiting.</p>}
      <ul className="queue">
        {signoffs.data?.map((o) => <SignOffRow key={`${o.team}/${o.training}/${o.module}/${o.question}/${o.trainee}`} o={o} onDone={signoffs.reload} />)}
      </ul>
    </section>
  )
}

function SignOffRow({ o, onDone }: { o: SignOff; onDone: () => void }) {
  const [notes, setNotes] = useState('')
  const [busy, setBusy] = useState(false)
  const sign = async () => {
    setBusy(true)
    try {
      await api('/api/anvil/signoffs', { method: 'POST', json: { team: o.team, training: o.training, module: o.module, question: o.question, trainee: o.trainee, notes } })
      toast(`Signed off: ${o.trainee_name || o.trainee}.`)
      onDone()
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="card" aria-label={`Sign-off for ${o.trainee}: ${o.prompt}`}>
      <p><strong>{o.trainee_name || o.trainee}</strong> · {o.training} / {o.module} · {o.points} pts</p>
      <p>{o.prompt}</p>
      <div className="row">
        <label>Notes <input value={notes} maxLength={5000} onChange={(e) => setNotes(e.target.value)} /></label>
        <button className="primary" disabled={busy} onClick={sign}>Mark passed</button>
      </div>
    </li>
  )
}

export function AnvilDetailPage() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data, error, reload } = useFetch<AnvilDetail>(`/api/anvil/${id}`)
  const [points, setPoints] = useState('')
  const [feedback, setFeedback] = useState('')
  const [busy, setBusy] = useState(false)
  if (error) return <ErrorBox error={error} />
  if (!data) return <Loader label="Laying the piece on the anvil…" />
  const s = data.submission
  const decide = async (what: 'score' | 'return') => {
    setBusy(true)
    try {
      await api(`/api/anvil/${s.id}/${what}`, { method: 'POST', json: what === 'score' ? { points: Number(points), feedback } : { feedback } })
      toast(what === 'score' ? 'Scored. The trainee has been told.' : 'Returned for rework.')
      nav('/anvil')
    } catch (e) {
      toast((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const isLink = s.type === 'upload' && /^https?:\/\//i.test(s.answer) // the server only stores http(s) links
  const hintCost = data.lab?.tasks.find((t) => t.id === s.item)?.hint_cost ?? 0
  return (
    <section className="page anvil">
      <Link to="/anvil">← Back to the Anvil</Link>
      <h1>{typeLabel[s.type]} from {s.trainee_name || s.trainee}</h1>
      <p className="muted">{s.trainee} · {s.training} / {s.module} · submitted {new Date(s.created_at).toLocaleString()}</p>
      <h2>Prompt</h2>
      <p className="pre">{s.prompt}</p>
      <h2>Rubric</h2>
      <p className="rubric pre" data-testid="rubric">{s.rubric || 'No rubric: use your judgement.'}</p>
      <h2>{s.type === 'review' ? 'Notes' : 'Answer'}</h2>
      <div className="answer" data-testid="answer">
        {isLink ? <a href={s.answer} target="_blank" rel="noopener noreferrer">{s.answer}</a> : <p className="pre">{s.answer || '—'}</p>}
        {s.files.map((f, i) => (
          <p key={i}><a href={`/api/submissions/${s.id}/files/${i}`}>{f.name}</a> <span className="muted">({kb(f.size)})</span></p>
        ))}
      </div>
      {data.history.length > 0 && (
        <>
          <h2>Earlier attempts</h2>
          <ul>
            {data.history.map((h) => (
              <li key={h.id}>
                <span className="muted">{new Date(h.created_at).toLocaleString()} · {h.status}{h.scored_by ? ` by ${h.scored_by}` : ''}</span>
                {h.feedback && <p className="pre">{h.feedback}</p>}
              </li>
            ))}
          </ul>
        </>
      )}
      {data.lab && <LabEvidenceView id={s.id} ev={data.lab} onChange={reload} />}
      {s.status === 'pending' ? (
        <div className="card score-form">
          <div className="row">
            <label>Points <input type="number" min={0} max={s.max_points} step="0.5" value={points} onChange={(e) => setPoints(e.target.value)} /></label>
            <span className="muted">of {s.max_points}</span>
          </div>
          {hintCost > 0 && <p className="muted">Hints the trainee revealed cost {hintCost} points; they are taken off what you award.</p>}
          <label>Feedback <textarea rows={4} maxLength={5000} value={feedback} onChange={(e) => setFeedback(e.target.value)} /></label>
          <div className="row">
            <button className="primary" disabled={busy || points === ''} onClick={() => decide('score')}>Score</button>
            <button className="ghost" disabled={busy || !feedback.trim()} onClick={() => decide('return')}>Return for rework</button>
          </div>
        </div>
      ) : (
        <p className="muted">Already {s.status}{s.scored_by ? ` by ${s.scored_by}` : ''}.</p>
      )}
    </section>
  )
}

function LabEvidenceView({ id, ev, onChange }: { id: number; ev: LabEvidence; onChange: () => void }) {
  const [shown, setShown] = useState<number>()
  return (
    <>
      <h2>Lab evidence {ev.self_reported && <span className="badge warn">self-reported</span>}</h2>
      {ev.tasks.map((t) => <TaskEvidenceView key={t.id} id={id} t={t} onChange={onChange} />)}
      <h2>Terminal transcripts</h2>
      {ev.transcripts.length === 0 && <p className="muted">No transcripts yet: a session is saved when its terminal closes.</p>}
      <ul className="transcripts">
        {ev.transcripts.map((tr) => (
          <li key={tr.id}>
            <button className="ghost" onClick={() => setShown(shown === tr.id ? undefined : tr.id)}>
              {shown === tr.id ? 'Hide' : 'Show'} transcript: {tr.terminal}
            </button>
            <span className="muted"> {new Date(tr.started_at).toLocaleString()} · {kb(tr.bytes)}{tr.truncated ? ' · earlier output trimmed' : ''}</span>
            {shown === tr.id && <TranscriptView labId={tr.lab_id} id={tr.id} />}
          </li>
        ))}
      </ul>
    </>
  )
}

function TaskEvidenceView({ id, t, onChange }: { id: number; t: TaskEvidence; onChange: () => void }) {
  const [points, setPoints] = useState(String(t.awarded))
  const [reason, setReason] = useState('')
  const override = async () => {
    try {
      await api(`/api/anvil/${id}/override`, { method: 'POST', json: { task: t.id, points: Number(points), reason } })
      toast('Override saved and audited.')
      setReason('')
      onChange()
    } catch (e) {
      toast((e as Error).message)
    }
  }
  return (
    <section className="card evidence" aria-label={`Task ${t.title}`}>
      <h3>{t.title} <span className="muted">({t.kind}, {t.status})</span></h3>
      <p>Awarded <span data-testid="awarded">{t.awarded} / {t.points}</span>{t.hints_used > 0 && ` · ${t.hints_used} hint(s), −${t.hint_cost} pts`}</p>
      {t.kind !== 'review' && t.checks.length === 0 && <p className="muted">No checks run.</p>}
      <ol className="checks">
        {t.checks.map((c, i) => (
          <li key={i}>
            <span className={c.exit_code === 0 ? 'pass' : 'warn'}>{c.exit_code === 0 ? 'passed' : `exit ${c.exit_code}`}</span>
            <span className="muted"> {new Date(c.at).toLocaleString()}{c.self_reported ? ' · self-reported' : ''}{c.answer ? ` · answer "${c.answer}"` : ''}</span>
            <pre className="check-output">{c.output}</pre>
          </li>
        ))}
      </ol>
      {t.kind !== 'review' && (
        <div className="row">
          <label>Override points <input type="number" min={0} max={t.points} step="0.5" value={points} onChange={(e) => setPoints(e.target.value)} /></label>
          <label>Reason <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} /></label>
          <button className="ghost" disabled={!reason.trim()} onClick={override}>Override</button>
        </div>
      )}
    </section>
  )
}
```

- [ ] **Step 3: Routes and nav**

In `web/src/App.tsx` import `{ AnvilDetailPage, AnvilPage }` from `'./pages/Anvil'` and add, next to the approvals route:

```tsx
          <Route path="/anvil" element={<AnvilPage />} />
          <Route path="/anvil/:id" element={<AnvilDetailPage />} />
```

In `web/src/components/Nav.tsx`, after the Approvals link:

```tsx
      {me.can_score && <NavLink to="/anvil">Anvil</NavLink>}
```

Append to `web/src/theme/app.css`:

```css
.queue { list-style: none; padding: 0; display: grid; gap: 0.75rem; }
.queue a { color: inherit; text-decoration: none; display: block; }
.rubric { border-left: 3px solid var(--accent); padding-left: 0.75rem; }
.evidence .checks { padding-left: 1.2rem; }
.transcript { height: 24rem; margin: 0.5rem 0; }
.score-form { display: grid; gap: 0.5rem; margin-top: 1rem; }
```

- [ ] **Step 4: Build, then look at it**

Run: `cd web && npm run build && npm test`
Expected: success.

Then `KEYCLOAK_PORT=8082 KEEP=1 make local-check`, log in as `senior`/`senior`, open **Anvil**, and check the page in the `forge`, `anvil`, `quench` and `contrast` themes (Settings) and with Calm forge on. Tear down with `docker compose -f deploy/compose/docker-compose.yml down -v`.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): the Anvil scoring queue with rubric, evidence, transcripts, overrides and sign-offs

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: End to end: a scorer grades a free-text answer and a lab submission; the trainee sees the feedback

**Files:**
- Modify: `scripts/seed-git.sh` (seed `forge-301`)
- Modify: `scripts/local-check.sh` (lint `forge-301`)
- Create: `e2e/tests/scoring.spec.ts`

**Interfaces:**
- Consumes: every selector listed under Tasks 9 and 10; Forge 301 ids and texts from Tasks 1 and 5; Keycloak users `leader`, `senior`, `trainee` (password = username).

- [ ] **Step 1: Seed and lint the fixture**

In `scripts/seed-git.sh`, change the loop to `for name in platform forge-101 forge-201 forge-301; do` and the header comment to list `forge-301`. In `scripts/local-check.sh`, after `./bin/crucible lint examples/forge-201` add `./bin/crucible lint examples/forge-301`. (Keep whatever M4 added to these scripts.)

- [ ] **Step 2: Write the e2e**

Create `e2e/tests/scoring.spec.ts`:

```ts
import { expect, test, type Browser, type Page } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'

let agent: ChildProcess | undefined
test.afterAll(() => {
  agent?.kill('SIGTERM') // the agent tears down its compose projects on exit
})

async function login(browser: Browser, user: string): Promise<Page> {
  const page = await (await browser.newContext()).newPage()
  page.on('dialog', (d) => d.accept()) // "End lab" confirm
  await page.goto('/')
  await page.locator('#username').fill(user)
  await page.locator('#password').fill(user)
  await page.locator('#kc-login').click()
  await expect(page.getByRole('heading', { name: 'Hearth' })).toBeVisible()
  return page
}

async function typeIn(page: Page, tab: string, command: string) {
  await page.getByRole('tab', { name: tab, exact: true }).click()
  await page.locator(`[data-terminal="${tab}"]`).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test('a scorer grades a free-text answer and a lab submission; the trainee sees the feedback', async ({ browser }) => {
  // The leader enrolls the trainee in Forge 301; seniors become its scorers by default.
  const leader = await login(browser, 'leader')
  await leader.getByRole('link', { name: 'Team', exact: true }).click()
  await leader.getByLabel('Training to enroll').selectOption('forge-301')
  await leader.getByRole('button', { name: 'Enroll the team' }).click()
  await expect(leader.getByRole('heading', { name: /Program settings/ })).toBeVisible()
  await leader.getByRole('checkbox', { name: 'trainee@crucible.local' }).check()
  await leader.getByRole('button', { name: 'Save program' }).click()
  await expect(leader.getByRole('status').filter({ hasText: /Saved to git/ })).toBeVisible()

  // The trainee answers the choice question, writes an answer and attaches a screenshot. Module 2 stays locked.
  const trainee = await login(browser, 'trainee')
  await trainee.getByRole('link', { name: /Forge 301/ }).click()
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'true')
  await trainee.getByTestId('module-01-temper').getByRole('link', { name: 'Quiz' }).click()
  await trainee.getByLabel('Hardens it').check()
  await trainee.getByRole('button', { name: 'Submit answers' }).click()
  await expect(trainee.getByRole('status').filter({ hasText: 'Choices scored.' })).toBeVisible()
  const why = trainee.getByTestId('human-q-why')
  await why.getByLabel('Answer to q-why').fill('Quenched steel is hard but brittle. Tempering trades a little hardness for toughness.')
  await why.getByRole('button', { name: 'Submit for scoring' }).click()
  await expect(why.getByTestId('feedback')).toContainText('Waiting for a scorer')
  const log = trainee.getByTestId('human-q-log')
  await log.getByLabel('Files for q-log').setInputFiles({ name: 'forge.png', mimeType: 'image/png', buffer: Buffer.from('not really a png') })
  await log.getByRole('button', { name: 'Submit for scoring' }).click()
  await expect(log.getByTestId('feedback')).toContainText('Waiting for a scorer')
  await trainee.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(trainee.getByTestId('module-01-temper')).toContainText('Awaiting the hammer')
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'true')

  // The senior scores the free-text answer, returns the screenshot for rework and signs off the live demo.
  const senior = await login(browser, 'senior')
  await senior.getByRole('link', { name: 'Anvil' }).click()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: In two sentences/ }).click()
  await expect(senior.getByTestId('rubric')).toContainText('hard but brittle')
  await expect(senior.getByTestId('answer')).toContainText('Tempering trades')
  await senior.getByLabel('Points', { exact: true }).fill('4')
  await senior.getByLabel('Feedback', { exact: true }).fill('Good. Name toughness as the goal, not just "less brittle".')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Attach the log/ }).click()
  await expect(senior.getByRole('link', { name: 'forge.png' })).toBeVisible()
  await senior.getByLabel('Feedback', { exact: true }).fill('Attach the log itself, not a screenshot.')
  await senior.getByRole('button', { name: 'Return for rework' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()
  await senior.getByRole('listitem', { name: /Sign-off for trainee@crucible.local: Show a scorer/ }).getByRole('button', { name: 'Mark passed' }).click()
  await expect(senior.getByText('No live sign-offs waiting.')).toBeVisible()

  // The trainee sees the score and feedback, and resubmits the log.
  await trainee.goto('/p/forge/forge-301/m/01-temper/quiz')
  await expect(trainee.getByTestId('human-q-why').getByTestId('feedback')).toContainText('Scored 4 / 5')
  await expect(trainee.getByTestId('human-q-why').getByTestId('feedback')).toContainText('Name toughness as the goal')
  await expect(trainee.getByTestId('human-q-log').getByTestId('feedback')).toContainText('Attach the log itself')
  await trainee.getByLabel('Files for q-log').setInputFiles({ name: 'forge.log', mimeType: 'text/plain', buffer: Buffer.from('heat 1200C\nquench\ntemper 200C\n') })
  await trainee.getByTestId('human-q-log').getByRole('button', { name: 'Resubmit for scoring' }).click()
  await expect(trainee.getByTestId('human-q-log').getByTestId('feedback')).toContainText('Waiting for a scorer')

  await senior.reload()
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Attach the log/ }).click()
  await expect(senior.getByText('Attach the log itself, not a screenshot.')).toBeVisible() // earlier attempt
  await senior.getByLabel('Points', { exact: true }).fill('2')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()

  // All human items scored: module 2 unlocks. The trainee runs the lab on their laptop and submits the review task.
  await trainee.goto('/p/forge/forge-301')
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveAttribute('data-locked', 'false')
  await trainee.getByRole('link', { name: 'Connect your laptop' }).click()
  await trainee.getByRole('button', { name: 'Generate pairing token' }).click()
  const token = (await trainee.getByTestId('pairing-token').textContent())!.trim()
  agent = spawn(process.env.CRUCIBLE_AGENT!, ['--server', 'http://localhost:8080'], { stdio: 'inherit', env: { ...process.env, CRUCIBLE_TOKEN: token } })
  await expect(trainee.getByRole('status').filter({ hasText: /Agent connected/ })).toBeVisible({ timeout: 30_000 })
  await trainee.goto('/p/forge/forge-301/m/02-review-lab/lab')
  await trainee.getByRole('button', { name: 'Ignite the forge' }).click()
  await expect(trainee.getByRole('tab', { name: 'shell', exact: true })).toBeVisible({ timeout: 5 * 60_000 })
  await expect(trainee.getByText('Terminal output is recorded')).toBeVisible()
  await typeIn(trainee, 'shell', 'touch /tmp/lit')
  await trainee.getByRole('button', { name: 'Check' }).click()
  await expect(trainee.getByText('The forge is lit.')).toBeVisible()
  await expect(trainee.getByRole('heading', { name: 'Prove your work' })).toBeVisible({ timeout: 10_000 })
  await typeIn(trainee, 'shell', 'echo tempered > /tmp/proof && cat /tmp/proof')
  await trainee.getByLabel('Notes for the scorer').fill('Wrote the proof file and printed it back.')
  await trainee.getByRole('button', { name: 'Submit for review' }).click()
  await expect(trainee.getByTestId('feedback')).toContainText('Waiting for a scorer')
  await trainee.getByRole('button', { name: 'End lab' }).click() // closes the terminal: its transcript is saved
  await expect(trainee.getByRole('heading', { name: 'The forge has cooled' })).toBeVisible({ timeout: 60_000 })

  // The senior reviews the lab: check results, the transcript, an audited override, then the score.
  await senior.goto('/anvil')
  await senior.getByRole('link', { name: /Submission from trainee@crucible.local: Prove your work/ }).click()
  await expect(senior.getByTestId('rubric')).toContainText('tempered')
  await expect(senior.getByTestId('answer')).toContainText('Wrote the proof file')
  const light = senior.getByRole('region', { name: 'Task Light the forge' })
  await expect(light).toContainText('The forge is lit.')
  await light.getByLabel('Override points').fill('1')
  await light.getByLabel('Reason').fill('Self-reported on a laptop; the transcript shows the file but no check of it.')
  await light.getByRole('button', { name: 'Override' }).click()
  await expect(light.getByTestId('awarded')).toHaveText('1 / 2')
  await senior.getByRole('button', { name: /Show transcript: shell/ }).first().click()
  await expect(senior.getByTestId(/^transcript-/).first()).toContainText('tempered', { timeout: 10_000 })
  await senior.getByLabel('Points', { exact: true }).fill('3')
  await senior.getByLabel('Feedback', { exact: true }).fill('Clean proof. Next time show the file permissions too.')
  await senior.getByRole('button', { name: 'Score' }).click()
  await expect(senior.getByRole('heading', { name: 'Anvil' })).toBeVisible()

  // The trainee sees the feedback on the cooled lab, and the module is forged.
  await trainee.reload()
  await expect(trainee.getByTestId('feedback')).toContainText('Scored 3 / 3')
  await expect(trainee.getByTestId('feedback')).toContainText('Clean proof.')
  await trainee.getByRole('link', { name: 'Back to the training' }).first().click()
  await expect(trainee.getByTestId('module-02-review-lab')).toHaveClass(/complete/)
})
```

- [ ] **Step 3: Run the full local check**

Run: `KEYCLOAK_PORT=8082 make local-check`
Expected: all three (or, with M4's cluster project excluded, all local) Playwright tests pass and the script ends with `🔥 Local check passed. The forge holds.`

If the transcript assertion is flaky because xterm renders to canvas in this browser, assert on the raw bytes instead: `expect(await senior.evaluate(async (u) => (await fetch(u)).text(), transcriptUrl)).toContain('tempered')`, reading `transcriptUrl` from the detail JSON (`/api/anvil/<id>` → `lab.transcripts[0]`). Do not weaken the check to "a transcript exists".

- [ ] **Step 4: Commit**

```bash
git add scripts/seed-git.sh scripts/local-check.sh e2e/tests/scoring.spec.ts
git commit -m "test(e2e): a scorer grades a written answer and a lab review; the trainee sees the feedback

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Spec coverage (M5)

| Spec | Where |
|---|---|
| §4.4 `text`, `upload`, `signoff` human-scored; `rubric` | Tasks 1, 3, 4, 9 |
| §4.5 `human_review` task ("scorer reviews / may override") | Tasks 1, 5, 7, 9, 10 |
| §5.2–5.3 scorer role; nobody scores own submission; trainees see only their own scores | Tasks 3, 7 (`rbac.Score`, `rbac.ViewProgress`) |
| §7 Scoring Queue: filter by training/trainee/type, rubric, score + feedback, return-for-rework | Tasks 3, 7, 10 |
| §7 lab submissions show check results, terminal transcript, uploaded artifacts; overrides with a reason (audited) | Tasks 5, 6, 7, 10 |
| §7 live sign-offs created by a scorer with notes | Tasks 3, 7, 10 |
| §7 progression (`linear`) waits on human scores | Tasks 4, 5 |
| §8.4 hint costs on review tasks | Task 5 |
| §10 submission awaiting scoring; scored/returned (mutable per kind) | Task 3 |
| §12 navigation: Anvil | Task 10 |
| §13 `submissions`, `scores` (folded in, ruling 1), `terminal_transcripts`, files in S3-compatible storage | Tasks 2, 3, 6, 8 |
| §14 Playwright e2e for the scoring queue; uploads served safely | Tasks 7, 11 |
| §8.2 program option "require human review for self-reported results" | Deferred (ruling 14) |
