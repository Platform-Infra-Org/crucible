package smith

import (
	"context"
	"testing"

	"crucible/internal/config"
	"crucible/internal/db/dbtest"
)

func TestLadderAndLevels(t *testing.T) {
	l := Ladder(config.DefaultSmith)
	if len(l) != 6 || l[0] != (Step{"Bellows Hand", 0}) || l[5] != (Step{"Grandmaster", 5000}) {
		t.Fatalf("%+v", l)
	}
	for xp, want := range map[int]int{0: 0, 149: 0, 150: 1, 499: 1, 500: 2, 1200: 3, 2499: 3, 2500: 4, 5000: 5, 99999: 5} {
		if got := LevelFor(xp, l); got != want {
			t.Errorf("%d xp: level %d, want %d", xp, got, want)
		}
	}
}

func TestMilestonesAreCumulative(t *testing.T) {
	for lvl, want := range []int{0, 20, 60, 120, 200, 350} {
		if got := MilestoneXP(lvl); got != want {
			t.Errorf("sword level %d: %d, want %d", lvl, got, want)
		}
	}
	if Article("Apprentice") != "an" || Article("Journeyman") != "a" {
		t.Fatal("article")
	}
}

func TestLookupDefaultsToBellowsHand(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO users (sub, email, name) VALUES ('s', 'Smith@Crucible.Local', 'S') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO smiths (user_id, xp, level, seen) VALUES ($1, 600, 2, true)`, id); err != nil {
		t.Fatal(err)
	}
	refs, err := Lookup(ctx, db, []string{"smith@crucible.local", "never@crucible.local"})
	if err != nil {
		t.Fatal(err)
	}
	if refs.Of("SMITH@crucible.local") != (Ref{2, "Journeyman"}) || refs.Of("never@crucible.local") != (Ref{0, "Bellows Hand"}) || refs.Of("") != (Ref{0, "Bellows Hand"}) {
		t.Fatalf("%+v", refs)
	}
}
