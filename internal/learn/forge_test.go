package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	"crucible/internal/smith"
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

func sword(v *ForgeView, training string) *Sword {
	for i := range v.Swords {
		if v.Swords[i].Training == training {
			return &v.Swords[i]
		}
	}
	return nil
}

func TestSwordRankPerTraining(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	enrolExtra(s, 5)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, x := sword(v, "forge-101"), sword(v, "extra")
	if f == nil || x == nil || f.Level < 1 || x.Level != 0 || x.Rank != "Ore" || f.Next == nil {
		t.Fatalf("each training forges its own sword: %+v", v.Swords)
	}
	if len(v.RankUp.Swords) != 1 || v.RankUp.Swords[0].Training != "forge-101" {
		t.Fatalf("the forge-101 rise waits to be shown: %+v", v.RankUp)
	}
}

func TestExperienceIsWorkPlusMilestones(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab", "03-cluster-heat")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := s.Standing(ctx, u.ID, "forge", "forge-101")
	if err != nil {
		t.Fatal(err)
	}
	want := int(math.Round(sd.Done*10)) + 350 // work + a Masterwork sword's milestones (190 + 350 for Forge 101's 19 points)
	if f := sword(v, "forge-101"); f == nil || f.Rank != "Masterwork" || v.Smith.XP != want || v.Smith.Level != smith.LevelFor(want, v.SmithLadder) {
		t.Fatalf("want %d: %+v %+v", want, v.Smith, v.Swords)
	}
}

func TestExperienceNeverDrops(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	xp, lvl, sw := v.Smith.XP, v.Smith.Level, sword(v, "forge-101").Level
	if _, err := s.DB.Exec(ctx, `DELETE FROM item_progress WHERE user_id = $1`, u.ID); err != nil { // as an admin reset would
		t.Fatal(err)
	}
	st := s.State()
	st.Platform.Teams["forge"].Programs["forge-101"].Enrolled = nil // and the training taken away
	v, err = s.UpdateForge(ctx, u.ID)
	if err != nil || v.Smith.XP != xp || v.Smith.Level != lvl {
		t.Fatalf("experience and smith rank keep their high-water: %+v %v", v.Smith, err)
	}
	var stored int
	if err := s.DB.QueryRow(ctx, `SELECT level FROM sword_ranks WHERE user_id = $1 AND training = 'forge-101'`, u.ID).Scan(&stored); err != nil || stored != sw {
		t.Fatalf("the sword rank stays: %d %v", stored, err)
	}
}

func TestSmithRankUpNotifiesOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil { // the silent first computation
		t.Fatal(err)
	}
	tr := s.State().Trainings["forge-101@abc"]
	for _, m := range tr.Modules { // straight to the table: only the UpdateForge calls below may raise
		for _, it := range m.Items {
			if _, err := s.DB.Exec(ctx, `INSERT INTO item_progress (user_id, team, training, module, item, status, score)
				VALUES ($1, 'forge', 'forge-101', $2, $3, 'complete', 1)`, u.ID, m.ID, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.UpdateForge(ctx, u.ID) }()
	}
	wg.Wait()
	ups := notes.kind(notify.RankUp)
	if len(ups) != 1 || !strings.Contains(ups[0].Subject, "is now a Journeyman") || !slices.Equal(ups[0].To, []string{"trainee@crucible.local"}) {
		t.Fatalf("one smith notification naming the top rank: %+v", ups)
	}
	v, _ := s.UpdateForge(ctx, u.ID)
	if v.RankUp.Smith == nil || v.RankUp.Smith.Title != "Journeyman" {
		t.Fatalf("the smith rise waits to be shown: %+v", v.RankUp)
	}
	if err := s.SeenRankUp(ctx, u.ID, "smith", "", v.Smith.Level); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UpdateForge(ctx, u.ID); v.RankUp.Smith != nil || len(v.RankUp.Swords) != 1 {
		t.Fatalf("seen clears the smith banner only: %+v", v.RankUp)
	}
}

func TestSwordRankUpNeverEmails(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	s.State().Platform.Settings.Smith = config.SmithThresholds{Apprentice: 100000, Journeyman: 200000, Smith: 300000, MasterSmith: 400000, Grandmaster: 500000}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab") // a sword rise (at least Ingot); the smith can't rise here
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || len(v.RankUp.Swords) == 0 || v.Smith.Level != 0 || len(notes.evs) != 0 {
		t.Fatalf("in-app only: %+v %v %+v", v.RankUp, err, notes.evs)
	}
}

func TestFirstComputationIsSilent(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab", "03-cluster-heat")                 // progress before any forge row
	if _, err := s.DB.Exec(ctx, `DELETE FROM sword_ranks; DELETE FROM smiths`); err != nil { // as just after the upgrade
		t.Fatal(err)
	}
	notes.evs = nil
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || v.Smith.Level == 0 || v.RankUp.Smith != nil || len(v.RankUp.Swords) != 0 || len(notes.evs) != 0 {
		t.Fatalf("backfilled silently: %+v %v", v, err)
	}
}

func TestSmithThresholdChanges(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, _ := s.UpdateForge(ctx, u.ID)
	lvl := v.Smith.Level
	st := s.State()
	st.Platform.Settings.Smith = config.SmithThresholds{Apprentice: 1, Journeyman: 2, Smith: 3, MasterSmith: 4, Grandmaster: v.Smith.XP}
	v, _ = s.UpdateForge(ctx, u.ID)
	if v.Smith.Level != 5 || len(notes.kind(notify.RankUp)) == 0 {
		t.Fatalf("a lowered threshold raises and announces: %+v", v.Smith)
	}
	st.Platform.Settings.Smith = config.SmithThresholds{Apprentice: 100000, Journeyman: 200000, Smith: 300000, MasterSmith: 400000, Grandmaster: 500000}
	if v, _ = s.UpdateForge(ctx, u.ID); v.Smith.Level != 5 || v.Smith.Next != nil || lvl >= 5 {
		t.Fatalf("a raised threshold never lowers: %+v", v.Smith)
	}
}

func TestForgeWithNoEnrolments(t *testing.T) {
	s, _, leader := fixture(t)
	v, err := s.UpdateForge(context.Background(), leader.ID)
	if err != nil || len(v.Swords) != 0 || len(v.Badges) != 0 || v.RankUp.Smith != nil || len(v.RankUp.Swords) != 0 ||
		v.Smith.XP != 0 || v.Smith.Level != 0 || v.Smith.Title != "Bellows Hand" || v.Smith.Next == nil || v.Smith.Next.Title != "Apprentice" || v.Smith.Next.At != 150 {
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
	f := sword(&v, "forge-101")
	if f == nil || f.Level < 1 || len(v.RankUp.Swords) != 1 || len(get(leader).Swords) != 0 {
		t.Fatalf("own data only: %+v", v)
	}
	seen := func(who *auth.User, level int) *httptest.ResponseRecorder {
		return do(who, "POST", "/api/me/forge/seen", fmt.Sprintf(`{"kind":"sword","training":"forge-101","level":%d}`, level))
	}
	if w := seen(leader, f.Level); w.Code != 204 || len(get(u).RankUp.Swords) != 1 {
		t.Fatalf("another user's seen must not clear mine: %d", w.Code)
	}
	if w := seen(u, 0); w.Code != 204 || len(get(u).RankUp.Swords) != 1 {
		t.Fatalf("a stale level leaves the rank unseen: %d", w.Code)
	}
	for range 2 { // idempotent
		if w := seen(u, f.Level); w.Code != 204 || len(get(u).RankUp.Swords) != 0 {
			t.Fatalf("seen at the current level clears it: %d", w.Code)
		}
	}
	for _, b := range []string{`{"kind":"nope"}`, `nope`} {
		if w := do(u, "POST", "/api/me/forge/seen", b); w.Code != http.StatusBadRequest {
			t.Fatalf("bad body %s: %d", b, w.Code)
		}
	}
}

func TestNewTrainingSwordRiseIsAnnounced(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil { // the person's first computation is behind them
		t.Fatal(err)
	}
	enrolExtra(s, 2)
	if err := s.SetItem(ctx, u.ID, "forge", "extra", "x1", "lab", "complete", 1); err != nil { // runs UpdateForge
		t.Fatal(err)
	}
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil || len(v.RankUp.Swords) != 1 || v.RankUp.Swords[0].Training != "extra" || v.RankUp.Swords[0].Rank != "Masterwork" {
		t.Fatalf("a new training's first rise is shown: %+v %v", v.RankUp, err)
	}
}

func TestSmithRisesNameTheirOwnLevel(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	notes := &fakeNotify{}
	s.Notify = notes
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, _ := s.UpdateForge(ctx, u.ID)
	xp := v.Smith.XP
	notes.evs = nil
	set := func(a, j, sm, m, g int) {
		s.State().Platform.Settings.Smith = config.SmithThresholds{Apprentice: a, Journeyman: j, Smith: sm, MasterSmith: m, Grandmaster: g}
	}
	set(1, 2, 100000, 200000, 300000)
	_, _ = s.UpdateForge(ctx, u.ID)
	set(1, 2, xp, 200000, 300000)
	_, _ = s.UpdateForge(ctx, u.ID)
	ups := notes.kind(notify.RankUp)
	if len(ups) != 2 || !strings.Contains(ups[0].Subject, "a Journeyman") || !strings.Contains(ups[1].Subject, "a Smith") {
		t.Fatalf("each raise names the level it set, once: %+v", ups)
	}
}

func TestMilestonesOfADroppedTrainingStillCount(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	v, err := s.UpdateForge(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	fl := sword(v, "forge-101").Level
	s.State().Platform.Teams["forge"].Programs["forge-101"].Enrolled = nil
	enrolExtra(s, 5)
	if err := s.SetItem(ctx, u.ID, "forge", "extra", "x1", "lab", "complete", 1); err != nil {
		t.Fatal(err)
	}
	if v, err = s.UpdateForge(ctx, u.ID); err != nil || v.Smith.XP != 50+smith.MilestoneXP(5)+smith.MilestoneXP(fl) {
		t.Fatalf("forge-101's milestones are kept (level %d): %+v %v", fl, v.Smith, err)
	}
}

func TestSeenOnOneSwordLeavesTheOther(t *testing.T) {
	ctx := context.Background()
	s, u, _ := fixture(t)
	if _, err := s.UpdateForge(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	enrolExtra(s, 2)
	completeAll(t, s, u.ID, "01-welcome", "02-first-lab")
	if err := s.SetItem(ctx, u.ID, "forge", "extra", "x1", "lab", "complete", 1); err != nil {
		t.Fatal(err)
	}
	v, _ := s.UpdateForge(ctx, u.ID)
	if len(v.RankUp.Swords) != 2 || v.RankUp.Swords[0].Training != "extra" {
		t.Fatalf("two banners, in training order: %+v", v.RankUp.Swords)
	}
	if err := s.SeenRankUp(ctx, u.ID, "sword", "extra", v.RankUp.Swords[0].Level); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.UpdateForge(ctx, u.ID); len(v.RankUp.Swords) != 1 || v.RankUp.Swords[0].Training != "forge-101" {
		t.Fatalf("the other banner stays: %+v", v.RankUp.Swords)
	}
	got, err := s.SwordRanks(ctx, []int64{u.ID})
	if err != nil || got[u.ID]["extra"] != 5 || got[u.ID]["forge-101"] < 1 {
		t.Fatalf("SwordRanks: %v %v", got, err)
	}
}
