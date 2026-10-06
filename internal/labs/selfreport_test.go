package labs

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"crucible/internal/blob"
	"crucible/internal/scoring"
)

// setupSelfReport: Forge 101 asks scorers to review its self-reported (local) labs; senior = f.other scores it.
func setupSelfReport(t *testing.T) *fx {
	t.Helper()
	f := setup(t, true)
	p := f.plat.Teams["forge"].Programs["forge-101"]
	p.ReviewSelfReported, p.Roles.Scorers = true, []string{"senior@crucible.local"}
	f.sc = &scoring.Service{DB: f.s.DB, Blobs: blob.Disk{Dir: t.TempDir()}, State: f.s.Learn.State, Notify: f.notes,
		Quiz: f.s.Learn, Labs: f.s, Log: slog.Default(), Now: f.clk.Now}
	f.s.Scoring, f.s.Learn.Scoring, f.s.Blobs = f.sc, f.sc, f.sc.Blobs
	f.run.exit = func(sp ScriptSpec) int {
		if a, ok := sp.Env["CRUCIBLE_ANSWER"]; ok && a != "8081" {
			return 1
		}
		return 0
	}
	return f
}

// doFirstLab passes every task of 02-first-lab and returns the lab view.
func (f *fx) doFirstLab(t *testing.T, labID string) *View {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.Check(ctx, f.u, labID, "t1-forge-file", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.OpenTask(ctx, f.u, labID, "t2-find-port"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Check(ctx, f.u, labID, "t2-find-port", "8081"); err != nil {
		t.Fatal(err)
	}
	res, err := f.s.Check(ctx, f.u, labID, "t3-fix-nginx", "")
	if err != nil || !res.Passed {
		t.Fatalf("t3: %+v %v", res, err)
	}
	return res.Lab
}

func (f *fx) labItem(t *testing.T) (string, float64) {
	t.Helper()
	var st string
	var sc float64
	if err := f.s.DB.QueryRow(context.Background(), `SELECT status, score FROM item_progress WHERE user_id = $1 AND training = 'forge-101'
		AND module = '02-first-lab' AND item = 'lab'`, f.u.ID).Scan(&st, &sc); err != nil {
		t.Fatal(err)
	}
	return st, sc
}

func (f *fx) labSub(t *testing.T) *scoring.Submission {
	t.Helper()
	subs, err := f.sc.Latest(context.Background(), f.u.ID, "forge", "forge-101", "02-first-lab", scoring.KindLab)
	if err != nil {
		t.Fatal(err)
	}
	return subs["lab"]
}

func TestSelfReportedLabWaitsForAScorer(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	v = f.doFirstLab(t, v.ID)
	if st, _ := f.labItem(t); st != "pending_review" {
		t.Fatalf("a flagged program's local lab waits for review, got %q", st)
	}
	sub := f.labSub(t)
	if sub == nil || sub.Status != scoring.Pending || sub.QType != "self_reported" || sub.LabID != v.ID || sub.MaxPoints != v.MaxScore ||
		!strings.Contains(sub.Answer, "self-reported") || !strings.Contains(sub.Answer, "t1-forge-file") {
		t.Fatalf("submission %+v", sub)
	}
	if v.LabReview == nil || v.LabReview.Status != scoring.Pending {
		t.Fatalf("the trainee sees the lab is with a scorer: %+v", v.LabReview)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "Return the lab") {
		t.Fatal("rubric leaked into the trainee's lab view")
	}
	// page loads do not file a second submission
	if _, err := f.s.Get(ctx, f.u, v.ID); err != nil {
		t.Fatal(err)
	}
	if q, _ := f.sc.Queue(ctx, f.other, scoring.Filter{Type: "self_reported"}); len(q) != 1 {
		t.Fatalf("queue: %d", len(q))
	}
	d, err := f.sc.Detail(ctx, f.other, sub.ID)
	if err != nil || d.Lab == nil || !d.Lab.SelfReported || len(d.Lab.Tasks) != 3 {
		t.Fatalf("detail evidence: %+v %v", d, err)
	}
	if _, err := f.sc.Score(ctx, f.other, sub.ID, sub.MaxPoints/2, "half of it was really done"); err != nil {
		t.Fatal(err)
	}
	if st, sc := f.labItem(t); st != "complete" || math.Abs(sc-0.5) > 1e-9 {
		t.Fatalf("scored half: %q %v", st, sc)
	}
	if v, _ = f.s.Get(ctx, f.u, v.ID); v.LabReview == nil || v.LabReview.Feedback != "half of it was really done" {
		t.Fatalf("feedback on the lab page: %+v", v.LabReview)
	}
}

func TestReturnedSelfReportedLabMustBeRedone(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	if _, err := f.s.RevealHint(ctx, f.u, v.ID, "t1-forge-file"); err != nil {
		t.Fatal(err)
	}
	f.doFirstLab(t, v.ID)
	sub := f.labSub(t)
	if _, err := f.sc.Return(ctx, f.other, sub.ID, "the transcript does not show task 2"); err != nil {
		t.Fatal(err)
	}
	var n, hints int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM lab_task_progress WHERE user_id = $1 AND module = '02-first-lab'`, f.u.ID).Scan(&n)
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM hint_reveals WHERE user_id = $1`, f.u.ID).Scan(&hints)
	if st, _ := f.labItem(t); st != "in_progress" || n != 0 || hints != 1 {
		t.Fatalf("returned: item %q, %d task results left, %d hints", st, n, hints)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if statusOf(v, "t1-forge-file") != "open" || v.LabReview == nil || v.LabReview.Status != scoring.Returned {
		t.Fatalf("after return: %+v %+v", v.Tasks, v.LabReview)
	}
	v = f.doFirstLab(t, v.ID)
	again := f.labSub(t)
	if st, _ := f.labItem(t); st != "pending_review" || again.ID == sub.ID || again.Status != scoring.Pending {
		t.Fatalf("redone: item %q, submission %+v", st, again)
	}
}

// A decision whose Refresh never ran (e.g. the database blinked) is applied by the next page load.
func TestSelfReportDecisionSettlesOnPageLoad(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	f.sc.Labs = nil // the Refresh is lost
	if _, err := f.sc.Return(ctx, f.other, f.labSub(t).ID, "redo it"); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.labItem(t); st != "pending_review" {
		t.Fatalf("precondition: %q", st)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if st, _ := f.labItem(t); st != "in_progress" || statusOf(v, "t1-forge-file") != "open" {
		t.Fatalf("settled: item %q tasks %+v", st, v.Tasks)
	}
}

func TestUnflaggedLocalLabCompletesAtOnce(t *testing.T) {
	f := setupSelfReport(t)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = false
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	if st, _ := f.labItem(t); st != "complete" || f.labSub(t) != nil {
		t.Fatalf("unflagged: %q", st)
	}
}

// The flag only decides whether a NEW submission is filed: a waiting or decided one still counts after it is turned off.
func TestSelfReportDecisionHonouredAfterFlagTurnedOff(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	sub := f.labSub(t)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = false
	if _, err := f.s.Get(ctx, f.u, v.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.labItem(t); st != "pending_review" {
		t.Fatalf("a waiting submission keeps the item waiting with the flag off: %q", st)
	}
	if _, err := f.sc.Score(ctx, f.other, sub.ID, sub.MaxPoints/2, "half"); err != nil {
		t.Fatal(err)
	}
	if st, sc := f.labItem(t); st != "complete" || math.Abs(sc-0.5) > 1e-9 {
		t.Fatalf("the scorer's points count: %q %v", st, sc)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if math.Abs(v.Score-sub.MaxPoints/2) > 1e-9 || !v.Complete {
		t.Fatalf("the lab page shows the scorer's score: %v complete %v", v.Score, v.Complete)
	}
}

func TestPendingSelfReportedLabIsNotShownComplete(t *testing.T) {
	f := setupSelfReport(t)
	v := f.start(t)
	if v = f.doFirstLab(t, v.ID); v.Complete {
		t.Fatal("a lab with a scorer is not complete yet")
	}
}

func TestCompleteLabIsNotFiledLater(t *testing.T) {
	f := setupSelfReport(t)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = false
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	f.plat.Teams["forge"].Programs["forge-101"].ReviewSelfReported = true
	if err := f.s.Override(context.Background(), v.ID, "t1-forge-file", 0, func(context.Context, pgx.Tx, float64) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.labItem(t); st != "complete" || f.labSub(t) != nil {
		t.Fatalf("a finished lab stays unfiled: %q %+v", st, f.labSub(t))
	}
}

func TestSelfReportFilingRaceFilesOnce(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	// back to the moment before filing
	if _, err := f.s.DB.Exec(ctx, `DELETE FROM submissions WHERE kind = 'lab'`); err != nil {
		t.Fatal(err)
	}
	inst, err := f.s.instByID(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	lab, _, _ := f.s.labContent(ctx, inst)
	done, _ := f.s.taskRows(ctx, inst)
	var wg sync.WaitGroup
	waits := make([]bool, 6)
	errs := make([]error, 6)
	for i := range waits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waits[i], _, errs[i] = f.s.selfReportReview(ctx, inst, lab, v.MaxScore, done)
		}()
	}
	wg.Wait()
	for i := range waits {
		if errs[i] != nil || !waits[i] {
			t.Fatalf("caller %d: wait %v err %v", i, waits[i], errs[i])
		}
	}
	var n int
	_ = f.s.DB.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE kind = 'lab'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d lab submissions", n)
	}
}

func TestWholeLabScoreWinsOverLaterOverride(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	v := f.start(t)
	f.doFirstLab(t, v.ID)
	sub := f.labSub(t)
	if _, err := f.sc.Score(ctx, f.other, sub.ID, sub.MaxPoints/2, "half"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sc.Override(ctx, f.other, sub.ID, "t1-forge-file", 0, "x"); err != nil {
		t.Fatal(err)
	}
	if st, sc := f.labItem(t); st != "complete" || math.Abs(sc-0.5) > 1e-9 {
		t.Fatalf("override after a whole-lab score: %q %v", st, sc)
	}
}

func TestClusterLabInFlaggedProgramCompletesAtOnce(t *testing.T) {
	ctx := context.Background()
	f := setupSelfReport(t)
	f.s.Runners["cluster"] = f.run
	v := f.start(t)
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET runtime = 'cluster' WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	if v = f.doFirstLab(t, v.ID); !v.Complete || v.SelfReported {
		t.Fatalf("cluster results are not self-reported: %+v", v)
	}
	if st, _ := f.labItem(t); st != "complete" || f.labSub(t) != nil {
		t.Fatalf("cluster lab: %q", st)
	}
}
