package scoring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/notify"
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
	// The senior is also enrolled as a trainee and answers the same question: they score nothing in it (rbac.Score).
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
	if n := count(f.senior, Filter{}); n != 0 {
		t.Fatalf("an enrolled senior scores nothing in the training: %d", n)
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
	if _, err := f.s.Override(ctx, f.leader, second.ID, "t1-light", 1, "x"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("non-scorers can't tell a submission exists: %v", err)
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

// A scorer who is also enrolled in the training (admins included) never sees its rubrics or peers' answers: queue,
// detail, sign-offs, transcripts (CanScore) and uploaded evidence all refuse, and they are not told about submissions.
// Their own submission is still scored by someone else.
func TestEnrolledScorerNeverSeesRubric(t *testing.T) {
	ctx := context.Background()
	f := fixture(t)
	p := f.plat.Teams["forge"].Programs["forge-301"]
	p.Enrolled = append(p.Enrolled, "senior@crucible.local")
	sub, err := f.s.Submit(ctx, f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", Rubric: "the key", MaxPoints: 2},
		formFiles(t, map[string]string{"log.txt": "my answer"}))
	if err != nil {
		t.Fatal(err)
	}
	if ev := f.notes.last(notify.SubmissionPending); ev == nil || slices.Contains(ev.To, "senior@crucible.local") || !slices.Contains(ev.To, "admin@crucible.local") {
		t.Fatalf("only people who may open it are told: %+v", ev)
	}
	refuses := func(u *auth.User) {
		t.Helper()
		if list, err := f.s.Queue(ctx, u, Filter{}); err != nil || len(list) != 0 {
			t.Fatalf("%s queue: %d %v", u.Email, len(list), err)
		}
		if w := serve(f, u, http.MethodGet, fmt.Sprintf("/api/anvil/%d", sub.ID), ""); w.Code != 404 || strings.Contains(w.Body.String(), "the key") {
			t.Fatalf("%s detail: %d %s", u.Email, w.Code, w.Body)
		}
		if list, err := f.s.SignOffs(ctx, u); err != nil || len(list) != 0 {
			t.Fatalf("%s sign-offs: %+v %v", u.Email, list, err)
		}
		if f.s.CanScore(u, "forge", "forge-301", "trainee@crucible.local") {
			t.Fatalf("%s may open transcripts", u.Email)
		}
		if w := serve(f, u, http.MethodGet, fmt.Sprintf("/api/submissions/%d/files/0", sub.ID), ""); w.Code != 404 {
			t.Fatalf("%s evidence: %d", u.Email, w.Code)
		}
		if _, err := f.s.Score(ctx, u, sub.ID, 2, "ok"); err == nil {
			t.Fatalf("%s scored", u.Email)
		}
	}
	refuses(f.senior)
	mine, err := f.s.Submit(ctx, f.senior, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper", SHA: "abc",
		Kind: KindQuestion, Item: "q-why", QType: "text", Prompt: "Why?", MaxPoints: 5, Answer: "mine"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Score(ctx, f.admin, mine.ID, 4, "good"); err != nil {
		t.Fatalf("someone else scores the enrolled senior: %v", err)
	}
	p.Enrolled = append(p.Enrolled, "admin@crucible.local")
	refuses(f.admin)
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
			w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("Cache-Control") != "private, no-store" {
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

func serve(f *fx, u *auth.User, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithUser(req.Context(), u)))
		})
	})
	f.s.Routes(r)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestRubricOnlyReachesScorerViews(t *testing.T) {
	f := fixture(t)
	sub := f.submitText(t)
	if w := serve(f, f.senior, http.MethodGet, "/api/anvil", ""); strings.Contains(w.Body.String(), "rubric") {
		t.Fatalf("the queue list carries no rubric: %s", w.Body)
	}
	path := fmt.Sprintf("/api/anvil/%d", sub.ID)
	if w := serve(f, f.senior, http.MethodGet, path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"rubric":"brittle → tough"`) {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	if w := serve(f, f.trainee, http.MethodGet, path, ""); w.Code != 403 || strings.Contains(w.Body.String(), "brittle") {
		t.Fatalf("trainee detail: %d %s", w.Code, w.Body)
	}
	if w := serve(f, f.stranger, http.MethodGet, path, ""); w.Code != 404 {
		t.Fatalf("stranger detail: %d", w.Code)
	}
	if w := serve(f, f.senior, http.MethodPost, path+"/score", `{}`); w.Code != 400 {
		t.Fatalf("{} must not score 0: %d %s", w.Code, w.Body)
	}
	if w := serve(f, f.senior, http.MethodPost, path+"/score", `{"points":4,"feedback":"good"}`); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"rubric"`) || !strings.Contains(w.Body.String(), `"status":"scored"`) {
		t.Fatalf("score: %d %s", w.Code, w.Body)
	}
}

func TestDownloadEncodesNonASCIINames(t *testing.T) {
	f := fixture(t)
	sub, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", MaxPoints: 2},
		formFiles(t, map[string]string{"härte \"log\".txt": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	w := serve(f, f.senior, http.MethodGet, fmt.Sprintf("/api/submissions/%d/files/0", sub.ID), "")
	if cd := w.Header().Get("Content-Disposition"); w.Code != 200 || !strings.HasPrefix(cd, "attachment; filename*=utf-8''h%C3%A4rte") {
		t.Fatalf("%d %q", w.Code, cd)
	}
}
