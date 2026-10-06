package labs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/notify"
	"crucible/internal/yamlx"

	"github.com/go-chi/chi/v5"
)

// readyPaidLab: $4.50/h for the 1h first-heat lab = tier 1; +30m = $6.75 = tier 2 (the leader).
func readyPaidLab(t *testing.T, f *fx) *View {
	t.Helper()
	f.rates["first-heat"] = 4.5
	v := f.request(t, f.u)
	if _, err := f.s.Decide(context.Background(), f.leader, v.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	return f.waitState(t, f.u, v.ID, Ready)
}

func TestExtensionGoesToApproval(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	ready := readyPaidLab(t, f)
	got, err := f.s.Extend(ctx, f.u, ready.ID)
	if err != nil || !got.ExtensionPending || got.CanExtend || !got.EndsAt.Equal(*ready.EndsAt) {
		t.Fatalf("a tier-raising extension waits for approval: %+v %v", got, err)
	}
	if ev := f.notes.last(notify.LabPending); ev == nil || !strings.Contains(ev.Subject, "extension") || strings.Join(ev.To, ",") != "leader@crucible.local" {
		t.Fatalf("the tier-2 decider hears about it: %+v", ev)
	}
	list, err := f.s.Approvals(ctx, f.leader)
	if err != nil || len(list) != 1 || list[0].Kind != "extension" || list[0].EstimateUSD != 6.75 ||
		list[0].ExtendUntil == nil || !list[0].ExtendUntil.Equal(ready.EndsAt.Add(30*time.Minute)) {
		t.Fatalf("approvals list: %+v %v", list, err)
	}
	if err := f.s.DecideExtension(ctx, f.u, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("nobody approves their own extension: %v", err)
	}
	if err := f.s.DecideExtension(ctx, f.other, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
		t.Fatalf("a senior who is not an approver: %v", err)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, "go on"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.Get(ctx, f.u, ready.ID)
	if after.ExtensionPending || !after.EndsAt.Equal(ready.EndsAt.Add(30*time.Minute)) || after.EstimateUSD != 6.75 {
		t.Fatalf("approved: %+v", after)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Conflict) {
		t.Fatalf("decided once: %v", err)
	}
	var action string
	_ = f.s.DB.QueryRow(ctx, `SELECT action FROM audit_log WHERE target = $1 ORDER BY id DESC LIMIT 1`, ready.ID).Scan(&action)
	if action != "lab.extension.approve" {
		t.Fatalf("audit %q", action)
	}
	if ev := f.notes.last(notify.LabApproved); ev == nil || ev.To[0] != "trainee@crucible.local" || !strings.Contains(ev.Text, "extension") {
		t.Fatalf("the trainee hears the answer: %+v", ev)
	}
}

func TestExtensionDecisionRules(t *testing.T) {
	ctx := context.Background()
	t.Run("reject keeps the end and uses up the extension", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, false, "not today"); err != nil {
			t.Fatal(err)
		}
		v, _ := f.s.Get(ctx, f.u, ready.ID)
		if v.ExtensionPending || v.CanExtend || !v.EndsAt.Equal(*ready.EndsAt) {
			t.Fatalf("rejected: %+v", v)
		}
		if ev := f.notes.last(notify.LabRejected); ev == nil || ev.To[0] != "trainee@crucible.local" || !strings.Contains(ev.Text, "not today") {
			t.Fatalf("the trainee hears the no: %+v", ev)
		}
	})
	t.Run("two approvers at once: one decision", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, u := range []*auth.User{f.leader, f.admin} {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = f.s.DecideExtension(ctx, u, ready.ID, true, "") }()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) || !(errors.Is(errs[0], apperr.Conflict) || errors.Is(errs[1], apperr.Conflict)) {
			t.Fatalf("exactly one wins: %v", errs)
		}
	})
	t.Run("an ended lab drops out of the queue", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		if _, err := f.s.End(ctx, f.u, ready.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ := f.s.Approvals(ctx, f.leader); len(list) != 0 {
			t.Fatalf("queue: %+v", list)
		}
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Conflict) {
			t.Fatalf("nothing to decide: %v", err)
		}
	})
	t.Run("the kill switch blocks approval", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		if _, err := f.s.DB.Exec(ctx, `UPDATE kill_switch SET enabled = true`); err != nil {
			t.Fatal(err)
		}
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Conflict) {
			t.Fatalf("paused: %v", err)
		}
		if v, _ := f.s.Get(ctx, f.u, ready.ID); !v.ExtensionPending {
			t.Fatalf("still pending: %+v", v)
		}
	})
	t.Run("over the hard cap only an admin approves, audited", func(t *testing.T) {
		f := setup(t, true)
		ready := readyPaidLab(t, f)
		_, _ = f.s.Extend(ctx, f.u, ready.ID)
		f.spent(t, "000000000099", "forge-101", 244) // team cap 250: 244 + 4.50 fits, + the extra 2.25 does not
		if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); !errors.Is(err, apperr.Forbidden) {
			t.Fatalf("leader over cap: %v", err)
		}
		if err := f.s.DecideExtension(ctx, f.admin, ready.ID, true, "ok"); err != nil {
			t.Fatal(err)
		}
		var over bool
		_ = f.s.DB.QueryRow(ctx, `SELECT (detail->>'over_cap')::bool FROM audit_log WHERE target = $1 ORDER BY id DESC LIMIT 1`, ready.ID).Scan(&over)
		if !over {
			t.Fatal("the override is audited")
		}
	})
}

func TestExtensionApprovalNeverPassesTheWindow(t *testing.T) {
	ctx := context.Background()
	f := setup(t, true)
	onSchedule(f)
	f.plat.Teams["forge"].Programs["forge-101"].LabDefaults.MaxExtension = yamlx.Duration(45 * time.Minute)
	f.clk.Set(time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC)) // Wed 17:30 local; TTL 1h → 18:30; window closes 19:00 (16:00 UTC)
	ready := readyPaidLab(t, f)
	v, err := f.s.Extend(ctx, f.u, ready.ID)
	if err != nil || !v.ExtensionPending {
		t.Fatalf("request: %+v %v", v, err)
	}
	if err := f.s.DecideExtension(ctx, f.leader, ready.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.Get(ctx, f.u, ready.ID)
	if got.LimitReason != "schedule" || !got.EndsAt.Equal(time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("clamped at window close: %+v", got)
	}
}

func TestExtensionDecisionRoute(t *testing.T) {
	f := setup(t, true)
	ready := readyPaidLab(t, f)
	_, _ = f.s.Extend(context.Background(), f.u, ready.ID)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), f.leader)))
		})
	})
	f.s.Routes(router)
	for _, c := range []struct {
		body string
		want int
	}{{`{}`, http.StatusBadRequest}, {`{"approve": true}`, http.StatusNoContent}} {
		body, want := c.body, c.want
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/approvals/"+ready.ID+"/extension", strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("%s: %d, want %d", body, rec.Code, want)
		}
		if want == http.StatusBadRequest {
			if v, _ := f.s.Get(context.Background(), f.u, ready.ID); !v.ExtensionPending {
				t.Fatal("{} decided the extension")
			}
		}
	}
}
