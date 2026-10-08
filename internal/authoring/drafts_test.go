package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"crucible/internal/apperr"
	"crucible/internal/config"
	"crucible/internal/edits"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
)

var moveIntro = []gitsync.Op{
	{Op: "rename", From: "modules/m1/reading/intro.md", To: "modules/m1/reading/start.md"},
	{Op: "put", Path: "modules/m1/module.yaml", Content: "title: M1\nitems:\n  - reading: reading/start.md\n  - quiz: quiz.yaml\n"},
}

func TestDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Move the intro"})
	if err != nil || d.State != "editing" || d.BaseSHA != f.head() || d.HeadSHA != f.head() {
		t.Fatalf("create: %+v %v", d, err)
	}
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: d.Title, BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	e, err := f.s.Submit(ctx, f.leader, d.ID, d.UpdatedAt)
	if err != nil || e.Status != "pending" {
		t.Fatalf("submit: %+v %v", e, err)
	}
	d, _ = f.s.Get(ctx, f.leader, d.ID)
	if d.State != "in_review" || d.EditID == nil || *d.EditID != e.ID {
		t.Fatalf("in review: %+v", d)
	}
	if _, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: d.Title, BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt}); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("no saves while in review: %v", err)
	}
	if _, err := f.edits.Reject(ctx, f.senior, e.ID, "Keep intro.md"); err != nil {
		t.Fatal(err)
	}
	d, _ = f.s.Get(ctx, f.leader, d.ID)
	if d.State != "returned" {
		t.Fatalf("a rejected edit returns its draft: %+v", d)
	}
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Move the intro, take 2", BaseSHA: d.BaseSHA, Ops: moveIntro, UpdatedAt: d.UpdatedAt})
	if err != nil || d.State != "editing" || d.EditID != nil {
		t.Fatalf("saving a returned draft unlinks it: %+v %v", d, err)
	}
	e, err = f.s.Submit(ctx, f.leader, d.ID, d.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.edits.Approve(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	if list, err := f.s.List(ctx, f.leader); err != nil || len(list) != 0 {
		t.Fatalf("a merged draft is done: %+v %v", list, err)
	}
	other, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Scrap"})
	if err := f.s.Discard(ctx, f.leader, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Get(ctx, f.leader, other.ID); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("discarded: %v", err)
	}
	var n int
	if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('content_draft.submit', 'content_draft.discard')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("submit ×2 and discard are audited, autosaves are not: %d %v", n, err)
	}
}

// Review Focus 2: two tabs on one draft. The stale tab can't overwrite the other's work.
func TestDraftCompareAndSet(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Warmer"})
	tabA, tabB := d.UpdatedAt, d.UpdatedAt
	put := []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nA.\n"}}
	a, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: d.BaseSHA, Ops: put, UpdatedAt: tabA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: d.BaseSHA, UpdatedAt: tabB}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "another tab") {
		t.Fatalf("the stale tab must not overwrite: %v", err)
	}
	got, _ := f.s.Get(ctx, f.leader, d.ID)
	if len(got.Ops) != 1 || !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("tab A's save stands: %+v", got)
	}
}

func TestDraftLimitsAndAccess(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if _, err := f.s.Create(ctx, f.trainee, NewDraft{Training: "t1"}); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("enrolled users get no drafts: %v", err)
	}
	var ids []int64
	for range maxDrafts {
		d, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	if _, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "open drafts") {
		t.Fatalf("draft limit: %v", err)
	}
	// Discarding frees a slot under the cap (the editor's Discard button).
	if err := f.s.Discard(ctx, f.senior, ids[0]); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"})
	if err != nil {
		t.Fatalf("a discarded draft frees its slot: %v", err)
	}
	ids[0] = d.ID
	if _, err := f.s.Get(ctx, f.leader, ids[0]); !errors.Is(err, apperr.NotFound) {
		t.Fatalf("someone else's draft: %v", err)
	}
	if _, err := f.s.Save(ctx, f.senior, ids[0], SaveDraft{BaseSHA: strings.Repeat("b", 40), UpdatedAt: d.UpdatedAt}); !errors.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "rebase it") {
		t.Fatalf("a base that is neither the draft's nor the head: %v", err)
	}
	// The senior becomes enrolled: editing rights do not depend on enrollment, so their drafts, files and checks stay open.
	f.st.Platform.Teams["forge"].Programs["t1"] = &config.Program{Training: "t1", Enrolled: []string{"trainee@crucible.local", "senior@crucible.local"}}
	for name, err := range map[string]error{
		"get":      second(f.s.Get(ctx, f.senior, ids[0])),
		"files":    second(f.s.Files(ctx, f.senior, ids[0])),
		"file":     second(f.s.File(ctx, f.senior, ids[0], "modules/m1/quiz.yaml")),
		"validate": second(f.s.Validate(ctx, f.senior, ValidateReq{Training: "t1", BaseSHA: f.head()})),
	} {
		if err != nil {
			t.Errorf("%s while enrolled: %v", name, err)
		}
	}
	if list, _ := f.s.List(ctx, f.senior); len(list) == 0 {
		t.Fatal("drafts of a training I'm enrolled in stay listed")
	}
}

func second[T any](_ T, err error) error { return err }

func TestDraftFromAReturnedEdit(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	e, err := f.edits.Create(ctx, f.leader, edits.NewEdit{Training: "t1", BaseSHA: f.head(), Title: "Move", Ops: moveIntro})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", FromEdit: e.ID}); !errors.Is(err, apperr.Invalid) {
		t.Fatalf("a pending edit can't be reopened: %v", err)
	}
	if _, err := f.edits.Reject(ctx, f.senior, e.ID, ""); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", FromEdit: e.ID})
	if err != nil || d.Title != "Move" || d.BaseSHA != e.BaseSHA || len(d.Ops) != 2 {
		t.Fatalf("reopened: %+v %v", d, err)
	}
}

func TestRebaseFollowsUntouchedFiles(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Intro"})
	d, _ = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: d.BaseSHA, UpdatedAt: d.UpdatedAt,
		Ops: []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\nWarmer.\n"}}})
	old := d.BaseSHA
	head := f.advance(t, map[string]string{"modules/m1/quiz.yaml": seed["modules/m1/quiz.yaml"] + "pass_threshold: 1\n"})
	if probs, err := f.s.Validate(ctx, f.leader, ValidateReq{Training: "t1", BaseSHA: old, Ops: d.Ops}); err != nil || len(probs) != 0 {
		t.Fatalf("validate works on my draft's older base: %v %v", probs, err)
	}
	r, err := f.s.Rebase(ctx, f.leader, d.ID)
	if err != nil || len(r.Conflicts) != 0 || r.Draft.BaseSHA != head {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	if _, err := f.s.Submit(ctx, f.leader, d.ID, r.Draft.UpdatedAt); err != nil {
		t.Fatalf("a rebased draft submits: %v", err)
	}
}

// Review Focus 5: upstream deleted a file the draft changes. That's a conflict for the author, never a 500 or a loss.
func TestRebaseConflictWhenUpstreamDeleted(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Intro"})
	mine := "# Intro\n\nMine.\n"
	d, _ = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: d.BaseSHA, UpdatedAt: d.UpdatedAt,
		Ops: []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: mine}}})
	head := f.advance(t, map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - quiz: quiz.yaml\n"}, "modules/m1/reading/intro.md")
	r, err := f.s.Rebase(ctx, f.leader, d.ID)
	if err != nil || len(r.Conflicts) != 1 {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	c := r.Conflicts[0]
	if c.Path != "modules/m1/reading/intro.md" || !c.HeadMissing || c.Mine != mine || c.Base != seed["modules/m1/reading/intro.md"] {
		t.Fatalf("conflict: %+v", c)
	}
	if got, _ := f.s.Get(ctx, f.leader, d.ID); got.BaseSHA == head {
		t.Fatal("nothing moves while there are conflicts")
	}
	// The author drops their change: saved on the new head.
	d, err = f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Intro", BaseSHA: head, UpdatedAt: r.Draft.UpdatedAt})
	if err != nil || d.BaseSHA != head {
		t.Fatalf("resolved: %+v %v", d, err)
	}
}

// The cap counts only drafts I can still open: drafts of a training I'm now enrolled in (or that left the platform)
// are hidden from List and refused by Discard, so they must not hold my slots.
func TestDraftCapCountsVisibleDraftsOnly(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	for range maxDrafts {
		if _, err := f.s.DB.Exec(ctx, `INSERT INTO content_drafts (author, training, base_sha) VALUES ('senior@crucible.local', 'gone', 'x')`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Create(ctx, f.senior, NewDraft{Training: "t1"}); err != nil {
		t.Fatalf("hidden drafts hold no slot: %v", err)
	}
}

type notifyFunc func()

func (n notifyFunc) Notify(context.Context, notify.Event) error { n(); return nil }

func pending(t *testing.T, f *fx) int {
	t.Helper()
	var n int
	if err := f.s.DB.QueryRow(context.Background(), `SELECT count(*) FROM content_edits WHERE status = 'pending'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Submit is compare-and-set too: a stale tab, a double click, or an autosave racing the submit never links an edit
// holding older ops, and never leaves a second pending edit behind.
func TestSubmitCompareAndSet(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	put := func(body string) []gitsync.Op {
		return []gitsync.Op{{Op: "put", Path: "modules/m1/reading/intro.md", Content: "# Intro\n\n" + body + "\n"}}
	}
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Warmer"})
	stale := d.UpdatedAt
	d, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: d.BaseSHA, Ops: put("A."), UpdatedAt: d.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Submit(ctx, f.leader, d.ID, stale); !errors.Is(err, apperr.Conflict) || pending(t, f) != 0 {
		t.Fatalf("a stale tab can't submit: %v", err)
	}

	// An autosave lands while the edit is being created: the edit holds older ops, so it is withdrawn.
	f.edits.Notify = notifyFunc(func() {
		got, _ := f.s.Get(ctx, f.leader, d.ID)
		if _, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Warmer", BaseSHA: got.BaseSHA, Ops: put("B."), UpdatedAt: got.UpdatedAt}); err != nil {
			t.Error(err)
		}
	})
	if _, err := f.s.Submit(ctx, f.leader, d.ID, d.UpdatedAt); !errors.Is(err, apperr.Conflict) || pending(t, f) != 0 {
		t.Fatalf("save between read and link: %v, %d pending", err, pending(t, f))
	}
	f.edits.Notify = nil
	d, _ = f.s.Get(ctx, f.leader, d.ID)
	if d.State != "editing" || d.Ops[0].Content != put("B.")[0].Content {
		t.Fatalf("the newer work stands, unlinked: %+v", d)
	}

	// Double submit: exactly one pending edit.
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, err := f.s.Submit(ctx, f.leader, d.ID, d.UpdatedAt); errs <- err }()
	}
	var ok int
	for range 2 {
		if err := <-errs; err == nil {
			ok++
		} else if !errors.Is(err, apperr.Conflict) {
			t.Fatalf("the losing submit: %v", err)
		}
	}
	if ok != 1 || pending(t, f) != 1 {
		t.Fatalf("double submit: %d succeeded, %d pending", ok, pending(t, f))
	}
}

// A renamed file that was also edited (rename c→d + put d) whose source changed upstream: the put on d becomes a
// conflict against upstream's new c, so the author merges it instead of Keep silently overwriting upstream's change.
func TestRebaseConflictWhenRenamedAndEditedSourceChanged(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	d, _ := f.s.Create(ctx, f.leader, NewDraft{Training: "t1", Title: "Move"})
	from, to, mine := "modules/m1/reading/intro.md", "modules/m1/reading/start.md", "# Start\n\nMine.\n"
	d, err := f.s.Save(ctx, f.leader, d.ID, SaveDraft{Title: "Move", BaseSHA: d.BaseSHA, UpdatedAt: d.UpdatedAt, Ops: []gitsync.Op{
		{Op: "rename", From: from, To: to}, {Op: "put", Path: to, Content: mine}}})
	if err != nil {
		t.Fatal(err)
	}
	theirs := "# Intro\n\nTheirs.\n"
	f.advance(t, map[string]string{from: theirs})
	r, err := f.s.Rebase(ctx, f.leader, d.ID)
	if err != nil || len(r.Conflicts) != 2 {
		t.Fatalf("rebase: %+v %v", r, err)
	}
	c := r.Conflicts[1]
	if c.Path != to || c.Op.Op != "put" || c.Head != theirs || c.Mine != mine || c.Base != seed[from] || c.HeadMissing {
		t.Fatalf("put conflict: %+v", c)
	}
}
