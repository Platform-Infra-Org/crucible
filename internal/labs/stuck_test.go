package labs

import (
	"context"
	"testing"
	"time"

	"crucible/internal/notify"
)

func TestStuckDestroyAlertsAdminsOnce(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	v := f.start(t)
	stick := func() {
		if _, err := f.s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', destroyed_at = $2 WHERE id = $1`,
			v.ID, f.clk.Now().Add(-20*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	stick()
	f.s.Sweep(ctx)
	stick()
	f.s.Sweep(ctx)
	n := 0
	for _, ev := range f.notes.events {
		if ev.Kind == notify.LabStuck {
			n++
			if len(ev.To) != 1 || ev.To[0] != "admin@crucible.local" || ev.Link != "/admin" {
				t.Fatalf("to the admins, linking Forge Status: %+v", ev)
			}
		}
	}
	if n != 1 {
		t.Fatalf("one alert per stuck lab, got %d", n)
	}
}
