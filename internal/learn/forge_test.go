package learn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"crucible/internal/auth"
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
	if _, err := s.UpdateForge(ctx, u.ID); err != nil { // no progress yet: creates the forge row at Ore
		t.Fatal(err)
	}
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
	if err := s.SeenRankUp(ctx, u.ID, v.Level); err != nil {
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

func TestRankBoundaries(t *testing.T) {
	ladder := Ladder(config.DefaultRanks)
	for _, c := range []struct {
		done, total float64
		want        int
	}{{20, 100, 1}, {19.9, 100, 0}, {99.9, 100, 4}, {100, 100, 5}, {0.22, 1.1, 1}, {0.45, 1, 2}, {0.9, 1, 4}, {0.75, 1, 3}, {0.495, 1.1, 2}, {0, 1, 0}} {
		if got := RankFor(forgePercent(c.done, c.total), ladder); got != c.want {
			t.Errorf("%v/%v: rank %d, want %d (percent %v)", c.done, c.total, got, c.want, forgePercent(c.done, c.total))
		}
	}
	// every fractional point weight that lands exactly on a threshold must reach it
	for tot := 1.0; tot <= 50; tot += 0.1 {
		for _, th := range []float64{20, 45, 75, 90} {
			if got := RankFor(forgePercent(tot*th/100, tot), ladder); got < 1 {
				t.Fatalf("%v%% of %v fell to Ore", th, tot)
			}
		}
	}
}

func TestFirstForgeComputationBackfillsSilently(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	tr := s.State().Trainings["forge-101@abc"]
	for _, id := range []string{"01-welcome", "02-first-lab"} { // progress that predates the forge row
		for _, it := range tr.Module(id).Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, id, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || v.Level < 1 || v.RankUp || len(notes.kind(notify.RankUp)) != 0 {
		t.Fatalf("backfill keeps the earned level, unseen flag clear, no notification: %+v %v %d", v, err, len(notes.kind(notify.RankUp)))
	}
}

func TestTwoRankJumpNotifiesOnceWithTopRank(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	tr := s.State().Trainings["forge-101@abc"]
	for _, m := range tr.Modules {
		for _, it := range m.Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, m.ID, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	v, err := s.UpdateForge(ctx, u.ID)
	ups := notes.kind(notify.RankUp)
	if err != nil || v.Level < 2 || len(ups) != 1 || !strings.Contains(ups[0].Subject, v.Rank) {
		t.Fatalf("one notification naming the top rank %q: %+v %v %+v", v.Rank, v, err, ups)
	}
}

func TestForgeHTTP(t *testing.T) {
	ctx := context.Background()
	s, u, leader := fixture(t)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	r := chi.NewRouter()
	s.Routes(r)
	do := func(who *auth.User, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), who))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	get := func(who *auth.User) ForgeView {
		w := do(who, "GET", "/api/me/forge", "")
		var v ForgeView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("GET forge: %d %s", w.Code, w.Body)
		}
		return v
	}
	v := get(u)
	if v.Level < 1 || !v.RankUp || get(leader).Level != 0 {
		t.Fatalf("own data only: %+v", v)
	}
	if w := do(leader, "POST", "/api/me/forge/seen", `{"level":`+string(rune('0'+v.Level))+`}`); w.Code != 204 || !get(u).RankUp {
		t.Fatalf("another user's seen must not clear mine: %d", w.Code)
	}
	if w := do(u, "POST", "/api/me/forge/seen", `{"level":0}`); w.Code != 204 || !get(u).RankUp {
		t.Fatalf("a stale level leaves the rank unseen: %d", w.Code)
	}
	body := `{"level":` + string(rune('0'+v.Level)) + `}`
	for i := 0; i < 2; i++ { // idempotent
		if w := do(u, "POST", "/api/me/forge/seen", body); w.Code != 204 || get(u).RankUp {
			t.Fatalf("seen at the current level clears it: %d", w.Code)
		}
	}
	if w := do(u, "POST", "/api/me/forge/seen", `nope`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
}
