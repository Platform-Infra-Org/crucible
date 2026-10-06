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
	"crucible/internal/content"
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
	if s.Scoring == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "scoring is not available")
	}
	if _, err := s.Scoring.Submit(ctx, u, &scoring.Submission{Team: inst.Team, Training: inst.Training, Module: inst.Module,
		SHA: inst.SHA, Kind: scoring.KindTask, Item: taskID, LabID: inst.ID, QType: "review", Prompt: taskTitle(lab, task),
		Rubric: task.Rubric, MaxPoints: task.Points, Answer: notes}, files); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	// The submission is stored: a failed recompute is logged (view settles it), never a 500 that invites a retry.
	if err := s.recompute(ctx, inst, lab); err != nil {
		s.Log.Error("recomputing a lab after a review submission failed", "lab", inst.ID, "err", err)
	}
	return s.view(ctx, inst)
}

// Refresh implements scoring.Progress for review tasks. It runs after the scoring transaction, so it is idempotent and
// view repeats it (settle) for a decision whose refresh failed.
func (s *Service) Refresh(ctx context.Context, sub *scoring.Submission) error {
	inst, err := s.instByID(ctx, sub.LabID)
	if err != nil {
		return err
	}
	lab, _, err := s.labContent(ctx, inst)
	if err != nil {
		return err
	}
	if sub.Kind == scoring.KindLab {
		return s.applyLab(ctx, inst, lab, sub)
	}
	return s.apply(ctx, inst, lab, sub)
}

// applyLab records a decision on a whole self-reported lab. Returned: the doubted results are cleared so the trainee
// redoes the lab; hint reveals stay, so no hint is charged twice. Scored: recompute uses the scorer's points.
func (s *Service) applyLab(ctx context.Context, inst *Instance, lab *content.Lab, sub *scoring.Submission) error {
	if sub.Status != scoring.Returned {
		return s.recompute(ctx, inst, lab)
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM lab_task_progress WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`,
		inst.UserID, inst.Team, inst.Training, inst.Module); err != nil {
		return err
	}
	s.event(ctx, inst.ID, "returned", "self-reported results returned for a redo")
	return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "in_progress", 0)
}

// labReview returns the latest whole-lab submission of this trainee's module (nil for labs that are not local).
func (s *Service) labReview(ctx context.Context, inst *Instance) (*scoring.Submission, error) {
	if s.Scoring == nil || inst.Runtime != "local" {
		return nil, nil
	}
	latest, err := s.Scoring.Latest(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, scoring.KindLab)
	return latest["lab"], err
}

// selfReportReview files a whole local lab for a scorer when its program asks for self-reported results to be
// reviewed (spec §8.2). wait reports that the lab item must stay pending_review; points is the scorer's decision once
// one exists.
func (s *Service) selfReportReview(ctx context.Context, inst *Instance, lab *content.Lab, maxScore float64, done map[string]taskRow) (wait bool, points *float64, err error) {
	st := s.Learn.State()
	if maxScore == 0 || st == nil || st.Platform == nil {
		return false, nil, nil
	}
	t := st.Platform.Teams[inst.Team]
	if t == nil || t.Programs[inst.Training] == nil || !t.Programs[inst.Training].ReviewSelfReported {
		return false, nil, nil
	}
	sub, err := s.labReview(ctx, inst)
	switch {
	case err != nil || s.Scoring == nil || inst.Runtime != "local":
		return false, nil, err
	case sub != nil && sub.Status == scoring.Scored:
		return false, &sub.Points, nil
	case sub != nil && sub.Status == scoring.Pending:
		return true, nil, nil
	}
	email, name, err := s.requester(ctx, inst.UserID)
	if err != nil {
		return false, nil, err
	}
	var b strings.Builder
	b.WriteString("Results the laptop agent reported (self-reported, local runtime):\n")
	for _, tk := range lab.Tasks {
		r := done[tk.ID]
		fmt.Fprintf(&b, "- %s (%s): %s, %.2f / %g points\n", tk.ID, taskTitle(lab, tk), r.Status, r.Points, tk.Points)
	}
	_, err = s.Scoring.Submit(ctx, &auth.User{ID: inst.UserID, Email: email, Name: name}, &scoring.Submission{
		Team: inst.Team, Training: inst.Training, Module: inst.Module, SHA: inst.SHA, Kind: scoring.KindLab, Item: "lab", LabID: inst.ID,
		QType: "self_reported", Prompt: "Self-reported lab results",
		Rubric: "Check these self-reported results against the check output and terminal transcripts. Award the points the " +
			"evidence supports (hint costs are already taken off). Return the lab if the trainee should run it again: " +
			"their task results are cleared, their hint charges stay.",
		MaxPoints: maxScore, Answer: b.String()}, nil)
	if errors.Is(err, apperr.Conflict) { // a concurrent recompute filed it first
		err = nil
	}
	return err == nil, nil, err
}

// apply records a decision. Scored: the task passes with the scorer's points minus the hint costs (M5 ruling 5).
// Returned: no task row, so the task re-opens and recompute puts the item back in progress.
func (s *Service) apply(ctx context.Context, inst *Instance, lab *content.Lab, sub *scoring.Submission) error {
	if sub.Status != scoring.Scored {
		return s.recompute(ctx, inst, lab)
	}
	_, costs, err := s.hintCounts(ctx, inst)
	if err != nil {
		return err
	}
	return s.finishTask(ctx, inst, lab, sub.Item, "passed", max(0, sub.Points-costs[sub.Item]))
}

// settle applies decided reviews that have no task result yet (their Refresh failed or never ran) and reports whether
// it wrote anything. Failures are logged: the page still renders from what is stored.
// ponytail: a returned review re-runs recompute on every load until resubmitted (one upsert); track it if that matters.
func (s *Service) settle(ctx context.Context, inst *Instance, lab *content.Lab, done map[string]taskRow, reviews map[string]*scoring.Submission) bool {
	wrote := false
	for _, t := range lab.Tasks {
		sub := reviews[t.ID]
		if _, ok := done[t.ID]; ok || sub == nil || sub.Status == scoring.Pending {
			continue
		}
		if err := s.apply(ctx, inst, lab, sub); err != nil {
			s.Log.Error("applying a review decision failed", "lab", inst.ID, "submission", sub.ID, "err", err)
			continue
		}
		wrote = true
	}
	return wrote
}

// settleLab applies a decided whole-lab review whose Refresh never ran: the item still waits (pending_review) although
// the lab submission is decided and no review task is waiting or was submitted after it (that would be redo work).
func (s *Service) settleLab(ctx context.Context, inst *Instance, lab *content.Lab, reviews map[string]*scoring.Submission, rv *scoring.Submission) bool {
	if rv == nil || rv.Status == scoring.Pending {
		return false
	}
	for _, r := range reviews {
		if r.Status == scoring.Pending || r.ID > rv.ID {
			return false
		}
	}
	var st string
	err := s.DB.QueryRow(ctx, `SELECT status FROM item_progress WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4
		AND item = 'lab'`, inst.UserID, inst.Team, inst.Training, inst.Module).Scan(&st)
	if err != nil || st != "pending_review" {
		return false
	}
	if err := s.applyLab(ctx, inst, lab, rv); err != nil {
		s.Log.Error("applying a lab review decision failed", "lab", inst.ID, "submission", rv.ID, "err", err)
		return false
	}
	return true
}

// Override sets the awarded points of an auto-checked task (spec §7). record writes the audit entry inside the same
// transaction, so an override is never stored unaudited.
func (s *Service) Override(ctx context.Context, labID, taskID string, points float64, record func(context.Context, pgx.Tx, float64) error) error {
	inst, err := s.instByID(ctx, labID)
	if err != nil {
		return err
	}
	lab, _, err := s.labContent(ctx, inst)
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
	if err := s.recompute(ctx, inst, lab); err != nil { // committed and audited; the next page load recomputes
		s.Log.Error("recomputing a lab after an override failed", "lab", inst.ID, "err", err)
	}
	return nil
}

// Evidence is what a scorer sees next to a lab submission (spec §7): every task's result, check runs and hints across
// all of this trainee's labs for the module, and the recorded terminal sessions.
func (s *Service) Evidence(ctx context.Context, labID string) (*scoring.LabEvidence, error) {
	inst, err := s.instByID(ctx, labID)
	if err != nil {
		return nil, err
	}
	lab, _, err := s.labContent(ctx, inst)
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
