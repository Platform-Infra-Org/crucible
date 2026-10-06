package labs

import (
	"context"
	"testing"
)

func TestMineListsOnlyMyLabsActiveFirst(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	mine, err := f.s.Mine(ctx, f.u)
	if err != nil || len(mine) != 1 || mine[0].ID != v.ID || mine[0].Title == "" || mine[0].Link != "/p/forge/forge-101/m/02-first-lab/lab" {
		t.Fatalf("mine %+v %v", mine, err)
	}
	if other, _ := f.s.Mine(ctx, f.leader); len(other) != 0 {
		t.Fatal("nobody sees someone else's labs here")
	}
}
