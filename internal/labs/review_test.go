package labs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/auth"
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

// A decision whose progress refresh failed (it runs after the scoring transaction) is applied when the lab is next viewed.
func TestDecisionsSettleOnPageLoad(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	f.sc.Labs = nil // the refresh after a decision never happens
	v := f.startReviewLab(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-light", "")
	v, _ = f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "done", nil)
	if _, err := f.sc.Return(ctx, f.other, taskOf(v, "t2-proof").Review.ID, "More."); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.item(t, "02-review-lab"); st != "pending_review" {
		t.Fatalf("refresh was skipped, so the item is stale: %s", st)
	}
	v, _ = f.s.Get(ctx, f.u, v.ID)
	if st, _ := f.item(t, "02-review-lab"); st != "in_progress" || taskOf(v, "t2-proof").Status != "open" {
		t.Fatalf("returned, settled on load: %s %+v", st, taskOf(v, "t2-proof"))
	}
	v, _ = f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "more", nil)
	if _, err := f.sc.Score(ctx, f.other, taskOf(v, "t2-proof").Review.ID, 2, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		v, _ = f.s.Get(ctx, f.u, v.ID)
	}
	if tv := taskOf(v, "t2-proof"); tv.Status != "passed" || tv.Awarded != 2 || !v.Complete {
		t.Fatalf("scored, settled on load: %+v", tv)
	}
	if st, score := f.item(t, "02-review-lab"); st != "complete" || math.Abs(score-0.8) > 1e-9 {
		t.Fatalf("lab item: %s %.2f", st, score)
	}
}

// A pin bump plus an API restart drops the lab's content version from memory; a pending review must still be scorable.
func TestReviewOnAnUnloadedVersionCanStillBeScored(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-light", "")
	v, _ = f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "done", nil)
	id := taskOf(v, "t2-proof").Review.ID
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET sha = 'gone' WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	d, err := f.sc.Detail(ctx, f.other, id)
	if err != nil || d.Lab == nil || len(d.Lab.Tasks) != 2 {
		t.Fatalf("the program's current version still has this lab: %+v %v", d, err)
	}
	if _, err := f.sc.Score(ctx, f.other, id, 3, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.item(t, "02-review-lab"); st != "complete" {
		t.Fatalf("lab item: %s", st)
	}
}

func TestDetailWithoutLabContentStillLoads(t *testing.T) {
	ctx := context.Background()
	f := setupReview(t)
	v := f.startReviewLab(t)
	_, _ = f.s.Check(ctx, f.u, v.ID, "t1-light", "")
	v, _ = f.s.SubmitReview(ctx, f.u, v.ID, "t2-proof", "done", nil)
	// neither the lab's version nor any program version has this training any more
	if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET sha = 'gone', training = 'retired' WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	d, err := f.sc.Detail(ctx, f.other, taskOf(v, "t2-proof").Review.ID)
	if err != nil || d.Lab != nil || d.Submission == nil {
		t.Fatalf("detail without evidence: %+v %v", d, err)
	}
}

func TestReturnedReviewDoesNotRelockLaterTasks(t *testing.T) {
	lab := &content.Lab{TaskOrder: "linear", Tasks: []*content.Task{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	got := taskStatuses(lab, map[string]taskRow{}, nil, map[string]*scoring.Submission{"a": {Status: scoring.Returned}})
	if got["a"] != "open" || got["b"] != "open" || got["c"] != "locked" {
		t.Fatalf("statuses: %v", got)
	}
	got = taskStatuses(lab, map[string]taskRow{}, map[string]string{"a": "failed"}, map[string]*scoring.Submission{"a": {Status: scoring.Returned}})
	if got["a"] != "setup_failed" || got["b"] != "open" {
		t.Fatalf("statuses with a failed setup: %v", got)
	}
}

type tripwire struct{ t *testing.T }

func (r tripwire) Read([]byte) (int, error) {
	r.t.Error("the body was read before ownership was checked")
	return 0, io.EOF
}

func TestSubmitChecksOwnerBeforeReadingTheBody(t *testing.T) {
	f := setupReview(t)
	v := f.startReviewLab(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.other)))
		})
	})
	f.s.Routes(router)
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+v.ID+"/tasks/t2-proof/submit", tripwire{t})
	req.Header.Set("X-Crucible-Upload", "1")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("someone else's lab: %d", w.Code)
	}
}
