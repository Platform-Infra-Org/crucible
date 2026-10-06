package journey

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/learn"
)

type fx struct {
	s                       *Service
	ls                      *learn.Service
	st                      *gitsync.State
	trainee, senior, leader *auth.User
	now                     time.Time
}

func setup(t *testing.T) *fx {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	tr, probs := content.Load("../../examples/forge-101")
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	st := &gitsync.State{Platform: plat, Trainings: map[string]*content.Training{"forge-101@abc": tr},
		ProgramSHAs: map[string]string{"forge/forge-101": "abc"}, Heads: map[string]string{"forge-101": "abc"}}
	ls := &learn.Service{DB: pool, State: func() *gitsync.State { return st }}
	store := auth.Store{DB: pool}
	trainee, _ := store.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	senior, _ := store.UpsertUser(ctx, "s2", "senior@crucible.local", "Sam")
	leader, _ := store.UpsertUser(ctx, "s3", "leader@crucible.local", "Lee")
	now := time.Now().UTC() // rows written with now() by SetItem must not look days old; TestInactiveAfterFiveBusinessDays pins its own clock
	return &fx{s: &Service{DB: pool, Learn: ls, Now: func() time.Time { return now }}, ls: ls, st: st,
		trainee: trainee, senior: senior, leader: leader, now: now}
}

func insertLab(t *testing.T, db *pgxpool.Pool, id string, userID int64, module, state string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s)
		VALUES ($1, $2, 'forge', 'forge-101', $3, 'abc', 'local', $4, $5, $5, 3600, 1800, 300, 0)`, id, userID, module, state, at); err != nil {
		t.Fatal(err)
	}
}

func flagKinds(r Row) map[string]Flag {
	out := map[string]Flag{}
	for _, f := range r.Flags {
		out[f.Kind] = f
	}
	return out
}

func TestHeatMapAndStuckFlags(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	db := f.s.DB
	for _, it := range []string{"how-we-work", "quiz"} {
		if err := f.ls.SetItem(ctx, f.trainee.ID, "forge", "forge-101", "01-welcome", it, "complete", 1); err != nil {
			t.Fatal(err)
		}
	}
	insertLab(t, db, "aaaaaaaaaaaa", f.trainee.ID, "02-first-lab", "ready", f.now.Add(-time.Hour))
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO check_runs (lab_id, task, exit_code, output, self_reported, at) VALUES ('aaaaaaaaaaaa', 't1', 1, 'no', true, $1)`, f.now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	lab := f.st.Trainings["forge-101@abc"].Module("02-first-lab").Lab
	var hinted *content.Task
	for _, tk := range lab.Tasks {
		if len(tk.Hints) > 0 {
			hinted = tk
		}
	}
	if hinted == nil {
		t.Fatal("Forge 101's first lab needs a task with hints for this test")
	}
	if _, err := db.Exec(ctx, `INSERT INTO hint_reveals (user_id, team, training, module, task, hint_index, cost, lab_id)
		VALUES ($1, 'forge', 'forge-101', '02-first-lab', $2, $3, 0, 'aaaaaaaaaaaa')`, f.trainee.ID, hinted.ID, len(hinted.Hints)-1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype, prompt, max_points, status)
			VALUES ($1, 'forge', 'forge-101', '02-first-lab', 'abc', 'task', 't9', 'review', 'p', 1, 'returned')`, f.trainee.ID); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := f.s.Team(ctx, f.leader, "forge")
	if err != nil || len(rows) != 1 || rows[0].Email != "trainee@crucible.local" {
		t.Fatalf("leader sees the trainee: %+v %v", rows, err)
	}
	r := rows[0]
	heat := map[string]string{}
	for _, c := range r.Cells {
		heat[c.Module] = c.Heat
	}
	if heat["01-welcome"] != "forged" || heat["02-first-lab"] != "glowing" || heat["03-cluster-heat"] != "cold" {
		t.Fatalf("heat map %+v", r.Cells)
	}
	k := flagKinds(r)
	if k["failed_checks"].Item != "t1" || k["final_hint"].Item != hinted.ID || k["returned_twice"].Item != "t9" {
		t.Fatalf("stuck flags %+v", r.Flags)
	}
	if _, ok := k["inactive"]; ok {
		t.Fatal("active an hour ago is not inactive")
	}

	if _, err := db.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status) VALUES ($1, 'forge', 'forge-101', '02-first-lab', 't1', 'passed')`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = f.s.Team(ctx, f.leader, "forge")
	if _, ok := flagKinds(rows[0])["failed_checks"]; ok {
		t.Fatal("a task passed since is no longer stuck")
	}
}

func TestInactiveAfterFiveBusinessDays(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.s.Now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) } // a Wednesday
	// Last touched Monday 5 Oct; "now" is Wednesday 14 Oct: Tue–Fri + Mon–Wed = 7 business days.
	if _, err := f.s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, updated_at)
		VALUES ($1, 'forge', 'forge-101', '01-welcome', 'how-we-work', 'complete', '2026-10-05T10:00:00Z')`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.s.Team(ctx, f.leader, "forge")
	if fl, ok := flagKinds(rows[0])["inactive"]; !ok || fl.Detail != "no activity for 7 business days" {
		t.Fatalf("inactive flag: %+v", rows[0].Flags)
	}
}

func TestBusinessDays(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 15, 0, 0, 0, time.UTC) } // Oct 2026: the 9th is a Friday
	for _, c := range []struct{ from, to, want int }{{9, 16, 5}, {9, 12, 1}, {10, 11, 0}, {12, 12, 0}, {5, 14, 7}} {
		if got := BusinessDays(d(c.from), d(c.to)); got != c.want {
			t.Errorf("%d→%d: got %d want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestNeverSignedIn(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	team := f.st.Platform.Teams["forge"]
	team.Trainees = append(team.Trainees, "newbie@crucible.local")
	team.Programs["forge-101"].Enrolled = append(team.Programs["forge-101"].Enrolled, "newbie@crucible.local")
	rows, _ := f.s.Team(ctx, f.leader, "forge")
	var nb *Row
	for i := range rows {
		if rows[i].Email == "newbie@crucible.local" {
			nb = &rows[i]
		}
	}
	if nb == nil || flagKinds(*nb)["inactive"].Detail != "has not signed in yet" || nb.Cells[0].Heat != "cold" {
		t.Fatalf("never signed in: %+v", nb)
	}
}

func TestJourneyVisibilityAndMentees(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	if rows, err := f.s.Team(ctx, f.trainee, "forge"); err != nil || len(rows) != 0 {
		t.Fatalf("a trainee sees no one else's journey (and not their own here): %+v %v", rows, err)
	}
	if rows, _ := f.s.Team(ctx, f.senior, "forge"); len(rows) != 1 {
		t.Fatal("a senior (and mentor) sees the trainee")
	}
	ms, err := f.s.Mentees(ctx, f.senior)
	if err != nil || len(ms) != 1 || ms[0].Email != "trainee@crucible.local" || ms[0].Rank != "Ore" || len(ms[0].Programs) != 1 {
		t.Fatalf("mentor dashboard: %+v %v", ms, err)
	}
	if ms, _ := f.s.Mentees(ctx, f.leader); len(ms) != 0 {
		t.Fatal("the leader mentors no one in the fixture")
	}
}

func TestTeamScopedToCaller(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	team := f.st.Platform.Teams["forge"]
	// a member who mentors only one of two trainees sees that one, never the other
	team.Members = append(team.Members, "member@crucible.local")
	team.Trainees = append(team.Trainees, "other@crucible.local")
	team.Programs["forge-101"].Enrolled = append(team.Programs["forge-101"].Enrolled, "other@crucible.local")
	team.Mentors["other@crucible.local"] = "member@crucible.local"
	member := &auth.User{Email: "member@crucible.local"}
	rows, err := f.s.Team(ctx, member, "forge")
	if err != nil || len(rows) != 1 || rows[0].Email != "other@crucible.local" {
		t.Fatalf("member sees only their mentee: %+v %v", rows, err)
	}
	ms, err := f.s.Mentees(ctx, member)
	if err != nil || len(ms) != 1 || ms[0].Email != "other@crucible.local" || len(ms[0].Pending) != 0 {
		t.Fatalf("member's mentees: %+v %v", ms, err)
	}
	if _, err := f.s.Team(ctx, f.leader, "no-such-team"); err == nil {
		t.Fatal("unknown team is an error")
	}
}

func TestMenteeDetail(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	db := f.s.DB
	if _, err := db.Exec(ctx, `INSERT INTO submissions (user_id, team, training, module, sha, kind, item, qtype, prompt, rubric, answer, max_points, status)
		VALUES ($1, 'forge', 'forge-101', '02-first-lab', 'abc', 'task', 't9', 'review', 'p', 'SECRET-RUBRIC', 'SECRET-ANSWER', 1, 'pending')`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ranks (user_id, level, seen) VALUES ($1, 2, true)`, f.trainee.ID); err != nil {
		t.Fatal(err)
	}
	insertLab(t, db, "bbbbbbbbbbbb", f.trainee.ID, "02-first-lab", "failed", f.now.Add(-time.Hour))
	if _, err := db.Exec(ctx, `UPDATE lab_instances SET error = 'boom' WHERE id = 'bbbbbbbbbbbb'`); err != nil {
		t.Fatal(err)
	}
	ms, err := f.s.Mentees(ctx, f.senior)
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	m := ms[0]
	if m.Rank != "Tempered" || m.Name != "Tara" || len(m.Pending) != 1 || m.Pending[0].Item != "t9" || m.Pending[0].Type != "review" {
		t.Fatalf("mentee detail: %+v", m)
	}
	if len(m.Failures) != 1 || m.Failures[0].Kind != "lab_failed" || m.Failures[0].Detail != "boom" {
		t.Fatalf("failures: %+v", m.Failures)
	}
	b, _ := json.Marshal(ms)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("rubric or answer leaked: %s", b)
	}
}
