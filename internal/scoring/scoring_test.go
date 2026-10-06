package scoring

import (
	"bytes"
	"context"
	"encoding/json"
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
	s                                        *Service
	notes                                    *fakeNotes
	quiz                                     *fakeProgress
	trainee, senior, leader, admin, stranger *auth.User
	plat                                     *config.Platform
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
	if _, err := f.s.Score(ctx, f.leader, sub.ID, 5, ""); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("the leader is not a scorer of this program: %v", err)
	}
	if _, err := f.s.Return(ctx, f.stranger, sub.ID, "no"); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("strangers don't learn the submission exists: %v", err)
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

func TestRubricNeverSerializesByDefault(t *testing.T) {
	sub := &Submission{ID: 1, Prompt: "p", Rubric: "SECRET-KEY"}
	b, _ := json.Marshal(sub)
	if strings.Contains(string(b), "SECRET-KEY") || strings.Contains(string(b), "rubric") {
		t.Fatalf("rubric leaked: %s", b)
	}
	b, _ = json.Marshal(sub.ScorerView())
	if !strings.Contains(string(b), `"rubric":"SECRET-KEY"`) || !strings.Contains(string(b), `"prompt":"p"`) {
		t.Fatalf("scorer view: %s", b)
	}
}

func TestReadFormIgnoresTheQuery(t *testing.T) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("answer", "body")
	_ = w.Close()
	r := httptest.NewRequest(http.MethodPost, "/x?answer=query", &b)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("X-Crucible-Upload", "1")
	answer, _, done, err := ReadForm(httptest.NewRecorder(), r)
	done()
	if err != nil || answer != "body" {
		t.Fatalf("answer = %q %v", answer, err)
	}
}

func TestFileNameHardening(t *testing.T) {
	for in, want := range map[string]string{
		"..": "file", "a/..": "file", "evil\u202elmth.exe": "evillmth.exe", "a\x01b\x7f.txt": "ab.txt", "": "file",
		"\u202e": "file", strings.Repeat("é", 300): strings.Repeat("é", 128),
	} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubmitNeedsSomething(t *testing.T) {
	f := fixture(t)
	_, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-why", QType: "text", Prompt: "p", MaxPoints: 5, Answer: "  "}, nil)
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("empty answer: %v", err)
	}
}

func TestFileOverLimitIsRefused(t *testing.T) {
	f := fixture(t)
	fh := formFiles(t, map[string]string{"big.bin": "x"})
	fh[0].Size = MaxFileBytes + 1
	_, err := f.s.Submit(context.Background(), f.trainee, &Submission{Team: "forge", Training: "forge-301", Module: "01-temper",
		SHA: "abc", Kind: KindQuestion, Item: "q-log", QType: "upload", Prompt: "p", MaxPoints: 2}, fh)
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("21 MiB file: %v", err)
	}
}

func TestScoreRacingReturnOneWins(t *testing.T) {
	f := fixture(t)
	sub := f.submitText(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, errs[0] = f.s.Score(context.Background(), f.senior, sub.ID, 3, "") }()
	go func() { defer wg.Done(); _, errs[1] = f.s.Return(context.Background(), f.admin, sub.ID, "redo") }()
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
		t.Fatalf("wins = %d (%v)", wins, errs)
	}
}

func TestStatusAndKindAreConstrained(t *testing.T) {
	f := fixture(t)
	for _, col := range []string{"status", "kind"} {
		_, err := f.s.DB.Exec(context.Background(), `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype,
			prompt, max_points, status) VALUES ($1, 't', 't', 'm', 's', $2, 'i', 'text', 'p', 1, $3)`, f.trainee.ID,
			map[string]string{"status": "question", "kind": "bogus"}[col], map[string]string{"status": "typo", "kind": "pending"}[col])
		if err == nil {
			t.Fatalf("bad %s accepted", col)
		}
	}
}
