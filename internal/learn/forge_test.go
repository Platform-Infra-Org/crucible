package learn

import (
	"context"
	"slices"
	"sync"
	"testing"

	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/notify"
)

type fakeNotify struct {
	mu  sync.Mutex
	evs []notify.Event
}

func (f *fakeNotify) Notify(_ context.Context, ev notify.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, ev)
	return nil
}

func (f *fakeNotify) kind(k notify.Kind) []notify.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notify.Event
	for _, e := range f.evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// completeAll marks every item of the given Forge 101 modules complete.
func completeAll(t *testing.T, s *Service, userID int64, modules ...string) {
	t.Helper()
	tr := s.State().Trainings["forge-101@abc"]
	for _, id := range modules {
		for _, it := range tr.Module(id).Items {
			if err := s.SetItem(context.Background(), userID, "forge", "forge-101", id, it.ID, "complete", 1); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// enrolExtra enrols the trainee in a one-module training whose lab is worth `points`.
func enrolExtra(s *Service, points float64) {
	st := s.State()
	extra := &content.Training{ID: "extra", Title: "Extra Heat", Progression: "free", Modules: []*content.Module{{
		ID: "x1", Title: "X", Completion: "all_items", Items: []content.Item{{Kind: "lab", ID: "lab"}},
		Lab: &content.Lab{Tasks: []*content.Task{{ID: "t", Points: points}}}}}}
	st.Trainings["extra@x"] = extra
	st.ProgramSHAs["forge/extra"] = "x"
	st.Platform.Teams["forge"].Programs["extra"] = &config.Program{Training: "extra", Enrolled: []string{"trainee@crucible.local"}}
}

func TestRankIsNeverLost(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || v.Level < 1 {
		t.Fatalf("two of three modules reach at least Ingot: %+v %v", v, err)
	}
	earned := v.Level
	enrolExtra(s, 1000)
	v, err = s.UpdateForge(ctx, u.ID)
	if err != nil || v.Percent >= 20 || v.Level != earned || v.Rank != v.Ladder[earned].Name {
		t.Fatalf("a new enrolment lowers the %% but never the rank: %+v %v", v, err)
	}
}

func TestRankUpNotifiesOnceTraineeAndMentor(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	// Write progress straight to the table so only the concurrent UpdateForge calls below can raise the rank.
	tr := s.State().Trainings["forge-101@abc"]
	for _, id := range []string{"01-welcome", "02-first-lab"} {
		for _, it := range tr.Module(id).Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, id, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.UpdateForge(ctx, u.ID) }()
	}
	wg.Wait()
	ups := notes.kind(notify.RankUp)
	if len(ups) != 1 {
		t.Fatalf("exactly one rank-up notification, got %d", len(ups))
	}
	if ev := ups[0]; !slices.Equal(ev.To, []string{"trainee@crucible.local", "senior@crucible.local"}) || ev.Team != "" {
		t.Fatalf("to the trainee and their mentor, never a team channel: %+v", ev)
	}
	v, _ := s.UpdateForge(ctx, u.ID)
	if !v.RankUp {
		t.Fatal("the rank-up waits to be shown")
	}
	if err := s.SeenRankUp(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UpdateForge(ctx, u.ID); v.RankUp {
		t.Fatal("shown once")
	}
}

func TestForgeWithNoEnrolments(t *testing.T) {
	s, _, leader := fixture(t)
	v, err := s.UpdateForge(context.Background(), leader.ID)
	if err != nil || v.Percent != 0 || v.Level != 0 || v.Rank != "Ore" || len(v.Badges) != 0 || v.RankUp {
		t.Fatalf("no enrolments: %+v %v", v, err)
	}
}

func TestBadgeForACompletedTraining(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	enrolExtra(s, 2)
	if err := s.SetItem(ctx, u.ID, "forge", "extra", "x1", "lab", "complete", 1); err != nil { // SetItem runs UpdateForge
		t.Fatal(err)
	}
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || len(v.Badges) != 1 || v.Badges[0].Training != "extra" || v.Badges[0].Title != "Extra Heat" {
		t.Fatalf("badge: %+v %v", v, err)
	}
}
