package authoring

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
)

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

var seed = map[string]string{
	"training.yaml":               "id: t1\ntitle: T1\nmaintainers: [senior@crucible.local]\nprogression: free\nmodules: [m1]\n",
	"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n",
	"modules/m1/reading/intro.md": "# Intro\n\nHello.\n",
	"modules/m1/quiz.yaml":        "questions:\n  - id: q1\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\", \"yes\"]\n    answer: 1\n",
}

type fx struct {
	s                       *Service
	edits                   *edits.Service
	st                      *gitsync.State
	remote, work            string
	leader, senior, trainee *auth.User
}

func setup(t *testing.T) *fx {
	t.Helper()
	work := t.TempDir()
	for rel, body := range seed {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
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
	db := dbtest.New(t)
	es := &edits.Service{DB: db, State: func() *gitsync.State { return st },
		Repo: func(id string) *gitsync.ContentRepo {
			if id == "t1" {
				return repo
			}
			return nil
		}}
	u := func(e string) *auth.User { return &auth.User{Email: e} }
	return &fx{s: &Service{DB: db, Edits: es}, edits: es, st: st, remote: remote, work: work,
		leader: u("leader@crucible.local"), senior: u("senior@crucible.local"), trainee: u("trainee@crucible.local")}
}

func (f *fx) head() string { return f.st.Heads["t1"] }

func TestValidateReportsProblemsWithLines(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head()})
	if err != nil || len(probs) != 0 {
		t.Fatalf("the head is clean: %v %v", probs, err)
	}
	probs, err = f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: []gitsync.Op{
		{Op: "put", Path: "modules/m1/quiz.yaml", Content: "questions:\n  - id: q1\n    type: single\n    prompt: \"Hot?\"\n    options: [\"no\"]\n    answer: 1\n"},
		{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"modules/m1/quiz.yaml": 2, "modules/m1/module.yaml": 3} // "- id: q1" line; the item naming the moved file
	for _, p := range probs {
		if line, ok := want[p.File]; ok && p.Line == line {
			delete(want, p.File)
		}
	}
	if len(want) > 0 {
		t.Fatalf("problems %+v; missing %v", probs, want)
	}
	probs, _ = f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: []gitsync.Op{
		{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: [\n"},
	}})
	if len(probs) == 0 || probs[0].File != "modules/m1/module.yaml" || probs[0].Line < 3 {
		t.Fatalf("a YAML syntax error carries its line: %+v", probs)
	}
}

func TestAuthoringRefusesEnrolledAndBadInput(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.Validate(ctx, f.trainee, ValidateReq{Training: "t1", BaseSHA: f.head()}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("an enrolled trainee may not validate (the problems quote answer keys): %v", err)
	}
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "nope", BaseSHA: f.head()}); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("unknown training: %v", err)
	}
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: strings.Repeat("a", 40)}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a base that is neither the head nor one of my drafts' bases: %v", err)
	}
	many := []gitsync.Op{}
	for i := range 21 {
		many = append(many, gitsync.Op{Op: "put", Path: "modules/m1/reading/" + string(rune('a'+i)) + ".md", Content: "x"})
	}
	for name, ops := range map[string][]gitsync.Op{
		"caps":      many,
		"traversal": {{Op: "put", Path: "modules/m1/../../../etc/x.md", Content: "x"}},
		"exec ext":  {{Op: "put", Path: "modules/m1/x.tf", Content: "x"}},
		"missing":   {{Op: "delete", Path: "modules/m1/reading/nope.md"}},
	} {
		if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head(), Ops: ops}); !errors.Is(err, apperr.Invalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOneCheckAtATimePerUser(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	release := make(chan struct{})
	started := make(chan struct{})
	first := make(chan error)
	go func() { first <- f.s.bounded(ctx, "leader@crucible.local", func() { close(started); <-release }) }()
	<-started
	if _, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: f.head()}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("a second check while one runs: %v", err)
	}
	if _, err := f.s.Validate(ctx, f.senior, ValidateReq{Training: "t1", BaseSHA: f.head()}); err != nil {
		t.Fatalf("other users are not blocked: %v", err)
	}
	close(release)
	if err := <-first; err != nil { // returned before checkTimeout changes below (the race detector watches that var)
		t.Fatal(err)
	}
	old := checkTimeout
	checkTimeout = 20 * time.Millisecond
	defer func() { checkTimeout = old }()
	hold := make(chan struct{})
	defer close(hold)
	if err := f.s.bounded(ctx, "senior@crucible.local", func() { <-hold }); !errors.Is(err, apperr.Unavailable) {
		t.Fatalf("timeout: %v", err)
	}
	if err := f.s.bounded(ctx, "senior@crucible.local", func() {}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("the slot is held until the slow check really ends: %v", err)
	}
}
