package edits

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
)

// TestMain allows the file transport: these tests use local bare repos as git remotes.
func TestMain(m *testing.M) {
	gitsync.AllowFileTransport = true
	os.Exit(m.Run())
}

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
	s                              *Service
	remote                         string
	notes                          *notes
	admin, leader, senior, trainee *auth.User
}

// setup: training t1 (maintainer senior@) in a bare repo; trainee@ is enrolled in it through team forge.
func setup(t *testing.T) *fx {
	t.Helper()
	work := t.TempDir()
	files := map[string]string{
		"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local]\nprogression: free\nmodules: [m1]\n",
		"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
		"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
		"modules/m1/quiz.yaml":        "questions:\n  - {id: q1, type: single, prompt: \"Hot?\", options: [\"no\", \"yes\"], answer: 1}\n",
	}
	for rel, body := range files {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
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
	if _, err := f.s.File(context.Background(), f.trainee, "t1", "", "modules/m1/quiz.yaml"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("file: %v", err)
	}
	if body, err := f.s.File(context.Background(), f.leader, "t1", "", "modules/m1/quiz.yaml"); err != nil || !strings.Contains(body, "answer: 1") {
		t.Fatalf("a leader reads the file: %q %v", body, err)
	}
	if _, err := f.propose(t, f.trainee, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("trainee proposing: %v", err)
	}
	if f.s.CanUse("trainee@crucible.local") || !f.s.CanUse("leader@crucible.local") || len(f.s.Trainings(f.trainee)) != 0 {
		t.Fatal("can_edit_content")
	}
	// An edit can't name its author a maintainer (and so a reviewer).
	if _, err := f.propose(t, f.leader, map[string]string{
		"training.yaml": "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local, leader@crucible.local]\nprogression: free\nmodules: [m1]\n",
	}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("maintainers change: %v", err)
	}
	e, err := f.propose(t, f.leader, map[string]string{
		"modules/m1/reading/intro.md": "# Intro\n\nHello, smith.\n",
		"training.yaml":               "id: t1\ntitle: T1 warm\nmaintainers: [senior@crucible.local]\nprogression: free\nmodules: [m1]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "pending" || !strings.Contains(e.Diff, "+Hello, smith.") || e.Branch != "crucible/edit/"+itoa(e.ID) || !e.CanWithdraw || e.CanReview {
		t.Fatalf("created %+v", e)
	}
	if _, err := f.s.Approve(ctx, f.leader, e.ID, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("the author never approves: %v", err)
	}
	if _, err := f.s.Approve(ctx, f.trainee, e.ID, ""); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("an enrolled trainee can't even see the edit: %v", err)
	}
	got, err := f.s.Approve(ctx, f.senior, e.ID, "lovely")
	if err != nil || got.Status != "merged" || got.MergeSHA == "" || got.Reviewer != "senior@crucible.local" {
		t.Fatalf("maintainer approves: %+v %v", got, err)
	}
	if git(t, f.remote, "rev-parse", "main") != got.MergeSHA {
		t.Fatal("merge pushed")
	}
	if refs := git(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/crucible"); refs != "" {
		t.Fatalf("branch removed after merge: %q", refs)
	}
	var notified []string
	for _, ev := range f.notes.evs {
		if ev.Kind != notify.ContentEdit || ev.Team != "" {
			t.Fatal("edit notifications are content_edit and never go to a team channel")
		}
		notified = append(notified, strings.Join(ev.To, ","))
	}
	if len(notified) < 2 || strings.Contains(notified[0], "leader@") || !strings.Contains(notified[0], "senior@") || notified[len(notified)-1] != "leader@crucible.local" {
		t.Fatalf("reviewers then the author are told: %v", notified)
	}
	var audits int
	if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('content_edit.propose', 'content_edit.merged')`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audited: %d %v", audits, err)
	}
}

func TestMaintainerNeverReviewsOwnEdit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.propose(t, f.senior, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Reject(ctx, f.senior, e.ID, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("own edit: %v", err)
	}
	for _, ev := range f.notes.evs {
		if strings.Contains(strings.Join(ev.To, ","), "senior@") {
			t.Fatal("the author is not asked to review their own edit")
		}
	}
	if got, err := f.s.Approve(ctx, f.admin, e.ID, ""); err != nil || got.Status != "merged" {
		t.Fatalf("admin approves: %+v %v", got, err)
	}
}

func TestEditPathRules(t *testing.T) {
	f := setup(t)
	for _, p := range []string{"../platform.yaml", ".git/hooks/post-merge", "modules/m1/../../.git/config", "modules/m1/reading/x.png",
		`modules\m1\x.md`, "/etc/passwd.md", "", "modules/.hidden/x.md", ".github/workflows/x.yml", "README.md"} {
		if _, err := f.propose(t, f.leader, map[string]string{p: "x"}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%q: %v", p, err)
		}
		if _, err := f.s.File(context.Background(), f.leader, "t1", "", p); !errors.Is(err, apperr.Invalid) {
			t.Errorf("file %q: %v", p, err)
		}
	}
	if refs := git(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads"); refs != "refs/heads/main" {
		t.Fatalf("no branch was pushed for a refused path: %q", refs)
	}
	_, files, err := f.s.Files(f.leader, "t1")
	if err != nil || len(files) != 4 {
		t.Fatalf("files: %+v %v", files, err)
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

func TestVolumeLimits(t *testing.T) {
	f := setup(t)
	for i := range maxOpen {
		if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\n" + itoa(int64(i)) + "\n"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nmore\n"}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "open edits") {
		t.Fatalf("open limit: %v", err)
	}
	if _, err := f.s.DB.Exec(context.Background(), `UPDATE content_edits SET status = 'withdrawn'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(context.Background(), `INSERT INTO content_edits (training, title, author, base_sha, files, status)
		SELECT 't1', 'x', 'leader@crucible.local', 'x', '{}', 'withdrawn' FROM generate_series(1, $1)`, maxPerHour-maxOpen); err != nil {
		t.Fatal(err)
	}
	if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nmore\n"}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "last hour") {
		t.Fatalf("hourly limit: %v", err)
	}
	if _, err := f.propose(t, f.senior, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nmore\n"}); err != nil {
		t.Fatalf("limits are per user: %v", err)
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

// Someone pushed to the edit branch after it was reviewed: nothing merges, the bot restores the reviewed change and
// it needs a fresh approval.
func TestApproveMovedEdit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nMine.\n"})
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "o")
	git(t, "", "clone", "-q", "-b", e.Branch, f.remote, other)
	_ = writeFile(filepath.Join(other, "modules/m1/reading/intro.md"), "# Intro\n\nSneaky.\n")
	git(t, other, "commit", "-qam", "sneaky")
	git(t, other, "push", "-q", "origin", "HEAD:"+e.Branch)
	main := git(t, f.remote, "rev-parse", "main")
	if _, err := f.s.Approve(ctx, f.senior, e.ID, ""); !errors.Is(err, gitsync.ErrEditMoved) {
		t.Fatalf("moved: %v", err)
	}
	got, _ := f.s.Get(ctx, f.leader, e.ID)
	if got.Status != "pending" || got.HeadSHA == e.HeadSHA || git(t, f.remote, "rev-parse", "main") != main ||
		git(t, f.remote, "rev-parse", e.Branch) != got.HeadSHA || !strings.Contains(got.Diff, "+Mine.") {
		t.Fatalf("still pending, reviewed change restored, main untouched: %+v", got)
	}
	if got, err := f.s.Approve(ctx, f.senior, e.ID, ""); err != nil || got.Status != "merged" {
		t.Fatalf("approved again: %+v %v", got, err)
	}
	if body := git(t, f.remote, "show", "main:modules/m1/reading/intro.md"); !strings.Contains(body, "Mine.") {
		t.Fatalf("merged the reviewed content: %q", body)
	}
}

func TestRejectAndWithdraw(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e1, _ := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nA.\n"})
	if got, err := f.s.Reject(ctx, f.senior, e1.ID, "not this way"); err != nil || got.Status != "rejected" || got.Note != "not this way" {
		t.Fatalf("reject: %+v %v", got, err)
	}
	e2, _ := f.s.Create(ctx, f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "B", Files: map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nB.\n"}})
	if _, err := f.s.Withdraw(ctx, f.senior, e2.ID); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("only the author withdraws: %v", err)
	}
	if got, err := f.s.Withdraw(ctx, f.leader, e2.ID); err != nil || got.Status != "withdrawn" {
		t.Fatalf("withdraw: %+v %v", got, err)
	}
	if refs := git(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/crucible"); refs != "" {
		t.Fatalf("branches removed: %q", refs)
	}
	if list, _ := f.s.List(ctx, f.trainee); len(list) != 0 {
		t.Fatal("enrolled people see no edits of that training")
	}
	if list, _ := f.s.List(ctx, f.senior); len(list) != 2 || list[0].Diff != "" {
		t.Fatalf("reviewers see both, without the diff: %+v", list)
	}
}

// Leaders and seniors propose only for trainings a team of theirs has a program for.
func TestProposeIsTeamScoped(t *testing.T) {
	f := setup(t)
	plat := f.s.State().Platform
	plat.Teams["other"] = &config.Team{ID: "other", Name: "Other", Leader: "other@crucible.local", Programs: map[string]*config.Program{}}
	other := &auth.User{Email: "other@crucible.local"}
	if _, _, err := f.s.Files(other, "t1"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("files: %v", err)
	}
	if _, err := f.s.File(context.Background(), other, "t1", "", "modules/m1/quiz.yaml"); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("file: %v", err)
	}
	if _, err := f.propose(t, other, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("propose: %v", err)
	}
	if f.s.CanUse(other.Email) || len(f.s.Trainings(other)) != 0 {
		t.Fatal("can_edit_content for an unrelated leader")
	}
	if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"}); err != nil {
		t.Fatalf("forge's leader still proposes: %v", err)
	}
}

// hold makes every push to the bare remote wait in a pre-receive hook until release is called (or the test ends).
// waiting blocks until a push is held.
func hold(t *testing.T, remote string) (waiting, release func()) {
	t.Helper()
	dir := t.TempDir()
	gate, entered := filepath.Join(dir, "gate"), filepath.Join(dir, "entered")
	if err := writeFile(gate, ""); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\ntouch '" + entered + "'\nwhile [ -e '" + gate + "' ]; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	release = func() { _ = os.Remove(gate) }
	t.Cleanup(release)
	return func() {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(entered); err == nil {
				_ = os.Remove(entered)
				return
			}
		}
		t.Fatal("no push reached the remote")
	}, release
}

func openTxs(t *testing.T, f *fx) int {
	t.Helper()
	var n int
	if err := f.s.DB.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND state LIKE 'idle in transaction%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// No DB transaction stays open while git talks to the remote, and a reviewer who goes away once the merge is pushed
// still gets it recorded: merged, audited, the author told.
func TestGitRunsOutsideTransactions(t *testing.T) {
	f := setup(t)
	waiting, release := hold(t, f.remote)
	done := make(chan error, 1)
	var e *Edit
	go func() {
		var err error
		e, err = f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHeld.\n"})
		done <- err
	}()
	waiting()
	if n := openTxs(t, f); n != 0 {
		t.Fatalf("%d transactions open while the edit is pushed", n)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waiting, release = hold(t, f.remote)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, err := f.s.Approve(ctx, f.senior, e.ID, "ok"); done <- err }()
	waiting()
	if n := openTxs(t, f); n != 0 {
		t.Fatalf("%d transactions open while the merge is pushed", n)
	}
	cancel() // the reviewer closes the tab with the merge on its way
	release()
	<-done
	got, err := f.s.Get(context.Background(), f.leader, e.ID)
	if err != nil || got.Status != "merged" || got.MergeSHA != git(t, f.remote, "rev-parse", "main") {
		t.Fatalf("the landed merge is recorded: %+v %v", got, err)
	}
	var as string
	if err := f.s.DB.QueryRow(context.Background(), `SELECT detail->>'as' FROM audit_log WHERE action = 'content_edit.merged'`).Scan(&as); err != nil || as != "maintainer" {
		t.Fatalf("audited: %q %v", as, err)
	}
	if last := f.notes.evs[len(f.notes.evs)-1]; strings.Join(last.To, ",") != "leader@crucible.local" {
		t.Fatalf("author told: %+v", last)
	}
}

func TestGitTimeout(t *testing.T) {
	f := setup(t)
	defer func(d time.Duration) { gitTimeout = d }(gitTimeout)
	gitTimeout = 300 * time.Millisecond
	hold(t, f.remote)
	start := time.Now()
	if _, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nSlow.\n"}); err == nil {
		t.Fatal("a hanging push must fail")
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("the timeout didn't bound the push: %v", d)
	}
	var n int
	if err := f.s.DB.QueryRow(context.Background(), `SELECT count(*) FROM content_edits`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nothing recorded: %d %v", n, err)
	}
}

// An admin who isn't a maintainer is an override, and the audit says so; a stale merge keeps the reviewer's note.
func TestAdminOverrideAndStaleNote(t *testing.T) {
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
	if _, err := f.s.Approve(ctx, f.admin, e.ID, "looks good"); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "applies cleanly") {
		t.Fatalf("stale, with the git reason: %v", err)
	}
	got, _ := f.s.Get(ctx, f.leader, e.ID)
	if got.Status != "stale" || got.Note != "looks good" {
		t.Fatalf("the reviewer's note is kept: %+v", got)
	}
	var as, gitErr string
	if err := f.s.DB.QueryRow(ctx, `SELECT detail->>'as', detail->>'error' FROM audit_log WHERE action = 'content_edit.stale'`).Scan(&as, &gitErr); err != nil ||
		as != "admin" || !strings.Contains(gitErr, "applies cleanly") {
		t.Fatalf("audit: %q %q %v", as, gitErr, err)
	}
}

func writeFile(p, body string) error { return os.WriteFile(p, []byte(body), 0o644) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEditOpsRenameAndDelete(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.s.Create(ctx, f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Move intro", Ops: []gitsync.Op{
		{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: reading/start.md\n  - quiz: quiz.yaml\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Diff, "rename from modules/m1/reading/intro.md") || len(e.Ops) != 2 {
		t.Fatalf("rename-aware diff and stored ops: %s %+v", e.Diff, e.Ops)
	}
	if _, err := f.s.Approve(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	if out := git(t, f.remote, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "intro.md") || !strings.Contains(out, "start.md") {
		t.Fatalf("merged tree: %s", out)
	}
}

func TestEditOpsAreValidated(t *testing.T) {
	f := setup(t)
	for name, ops := range map[string][]gitsync.Op{
		"orphaned item":   {{Op: "delete", Path: "modules/m1/reading/intro.md"}}, // module.yaml still lists it: lint refuses
		"delete training": {{Op: "delete", Path: "training.yaml"}},
		"rename training": {{Op: "rename", From: "training.yaml", To: "modules/m1/x.yaml"}},
	} {
		if _, err := f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "x", Ops: ops}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e, err := f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Drop intro", Ops: []gitsync.Op{
		{Op: "delete", Path: "modules/m1/reading/intro.md"},
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - quiz: quiz.yaml\n"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Diff, "deleted file mode") || !strings.Contains(e.Diff, "-Hello.") {
		t.Fatalf("a deleted file shows in full: %s", e.Diff)
	}
}

// The pre-ops request shape keeps working for one release (ponytail: drop Files after it ships; roadmap).
func TestFilesMapIsTranslatedToPuts(t *testing.T) {
	f := setup(t)
	e, err := f.propose(t, f.leader, map[string]string{"modules/m1/reading/intro.md": "# Intro\n\nHi.\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Ops) != 1 || e.Ops[0] != (gitsync.Op{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nHi.\n"}) {
		t.Fatalf("stored ops: %+v", e.Ops)
	}
	_, err = f.s.Create(context.Background(), f.leader, NewEdit{Training: "t1", BaseSHA: f.head(), Title: "x",
		Files: map[string]string{"modules/m1/reading/intro.md": "a"}, Ops: []gitsync.Op{{Op: "delete", Path: "modules/m1/quiz.yaml"}}})
	if !errors.Is(err, apperr.Invalid) {
		t.Fatalf("both shapes at once: %v", err)
	}
}

func TestFilesListsEverythingWithWhatIsEditable(t *testing.T) {
	f := setup(t)
	if err := os.WriteFile(filepath.Join(f.s.State().Training("t1", f.head()).Dir, "modules", "m1", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, files, err := f.s.Files(f.leader, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var txt *FileInfo
	for i := range files {
		if files[i].Path == "modules/m1/notes.txt" {
			txt = &files[i]
		}
	}
	if txt == nil || txt.Editable || !strings.Contains(txt.Reason, "only .md, .yaml, .yml and .sh") {
		t.Fatalf("non-editable files are listed, greyed with the reason: %+v", files)
	}
}
