package labs

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// overCap reports whether starting a lab of estimateUSD would push the team's or the program's month past its
// hard cap. Committed spend counts labs still running to their end.
func (s *Service) overCap(ctx context.Context, p *config.Platform, team, training string, estimateUSD float64) (bool, error) {
	for _, scope := range []string{"", training} {
		sp, err := s.spend(ctx, p, team, scope)
		if err != nil {
			return false, err
		}
		if sp.CapUSD > 0 && sp.CommittedUSD+estimateUSD > sp.CapUSD {
			return true, nil
		}
	}
	return false, nil
}

// alertLevels: 80 once spend reaches 80% of the budget, 100 once it reaches the hard cap.
func alertLevels(sp Spend) []int {
	var out []int
	if sp.BudgetUSD > 0 && sp.SpentUSD >= 0.8*sp.BudgetUSD {
		out = append(out, 80)
	}
	if sp.CapUSD > 0 && sp.SpentUSD >= sp.CapUSD {
		out = append(out, 100)
	}
	return out
}

// CheckBudgets sends the 80% and hard-cap alerts, once per scope, month and level (spec §9.2).
func (s *Service) CheckBudgets(ctx context.Context) error {
	st, err := s.platform()
	if err != nil {
		return nil // nothing to check until config is loaded
	}
	p := st.Platform
	month := monthStart(s.Now())
	for _, teamID := range slices.Sorted(maps.Keys(p.Teams)) {
		t := p.Teams[teamID]
		for _, tr := range append([]string{""}, slices.Sorted(maps.Keys(t.Programs))...) {
			sp, err := s.spend(ctx, p, teamID, tr)
			if err != nil {
				return err
			}
			scope, who, to := teamID, "Team "+t.Name, append([]string{t.Leader}, p.Admins...)
			if tr != "" {
				scope, who, to = teamID+"/"+tr, "Program "+teamID+"/"+tr, append(to, t.Programs[tr].Roles.Manager...)
			}
			to = slices.DeleteFunc(slices.Clone(to), func(e string) bool { return e == "" })
			for _, lvl := range alertLevels(sp) {
				tag, err := s.DB.Exec(ctx, `INSERT INTO budget_alerts (scope, month, level) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, scope, month, lvl)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					continue
				}
				subject := fmt.Sprintf("%s used 80%% of its monthly lab budget", who)
				text := fmt.Sprintf("%s has spent $%.2f of its $%.2f lab budget this month.", who, sp.SpentUSD, sp.BudgetUSD)
				if lvl == 100 {
					subject = fmt.Sprintf("%s reached its hard cap", who)
					text = fmt.Sprintf("%s has spent $%.2f, at its $%.2f hard cap. New paid lab requests now need an admin.", who, sp.SpentUSD, sp.CapUSD)
				}
				s.notify(ctx, notify.Event{Kind: notify.BudgetAlert, To: to, Team: teamID, Subject: subject, Text: text, Link: "/teams/" + teamID})
			}
		}
	}
	return nil
}

type KillSwitch struct {
	Enabled   bool       `json:"enabled"`
	ChangedBy string     `json:"changed_by,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

func (s *Service) KillSwitch(ctx context.Context) (KillSwitch, error) {
	var k KillSwitch
	err := s.DB.QueryRow(ctx, `SELECT enabled, changed_by, changed_at FROM kill_switch`).Scan(&k.Enabled, &k.ChangedBy, &k.ChangedAt)
	return k, err
}

// SetKillSwitch (admin): on destroys every starting or running lab and blocks new requests until turned off.
func (s *Service) SetKillSwitch(ctx context.Context, u *auth.User, enabled bool) (KillSwitch, error) {
	st, err := s.platform()
	if err != nil {
		return KillSwitch{}, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return KillSwitch{}, apperr.Wrap(apperr.Forbidden, "only admins can use the kill switch")
	}
	action := map[bool]string{true: "kill_switch.on", false: "kill_switch.off"}[enabled]
	tx, err := s.DB.Begin(ctx) // the change and its audit entry commit together
	if err != nil {
		return KillSwitch{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var was bool
	var since *time.Time
	if err := tx.QueryRow(ctx, `SELECT enabled, changed_at FROM kill_switch FOR UPDATE`).Scan(&was, &since); err != nil {
		return KillSwitch{}, err
	}
	if was && !enabled && since != nil { // escalation was paused: give pending requests their remaining time back
		if _, err := tx.Exec(ctx, `UPDATE lab_instances SET escalate_at = escalate_at + ($1::timestamptz - $2::timestamptz)
			WHERE state = 'pending_approval'`, s.Now(), *since); err != nil {
			return KillSwitch{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE kill_switch SET enabled = $1, changed_by = lower($2), changed_at = $3`, enabled, u.Email, s.Now()); err != nil {
		return KillSwitch{}, err
	}
	if err := audit.Log(ctx, tx, u.Email, action, "", nil, ""); err != nil {
		return KillSwitch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return KillSwitch{}, err
	}
	if enabled {
		go s.killAll(context.WithoutCancel(ctx)) // the sweep repeats it every 15 s while the switch is on
	}
	return s.KillSwitch(ctx)
}

// killAll destroys every provisioning or ready lab. destroy() is guarded by state, so overlapping runs are harmless.
// ponytail: sequential; offline agents fail fast, so this stays quick below ~100 labs.
func (s *Service) killAll(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE state IN ('provisioning', 'ready')`)
	if err != nil {
		s.Log.Error("kill switch: listing labs failed", "err", err)
		return
	}
	all, err := collectInst(rows)
	if err != nil {
		s.Log.Error("kill switch: listing labs failed", "err", err)
		return
	}
	for _, inst := range all {
		s.destroy(ctx, inst, "kill_switch")
	}
}

// budgetLimit is when this lab's running cost would reach the team's or the program's hard cap (spec §8.6): the
// headroom other labs leave, divided by this lab's hourly rate, from when it is ready. Called while the lab is
// still provisioning, so spend() counts its whole estimate (if it falls in this month): take that back out. Labs an
// admin approved over the cap and free labs have no budget limit. It fails closed: if spend can't be read, or no
// headroom is left, it returns an error and the caller must not hand the lab out (an instantly expired lab is worse).
func (s *Service) budgetLimit(ctx context.Context, inst *Instance, now time.Time) (Limit, error) {
	st := s.Learn.State()
	if inst.HourlyUSD <= 0 || inst.OverCap || st == nil || st.Platform == nil {
		return Limit{}, nil
	}
	var best Limit
	for _, scope := range []string{"", inst.Training} {
		sp, err := s.spend(ctx, st.Platform, inst.Team, scope)
		if err != nil {
			s.Log.Warn("budget limit: reading spend failed", "lab", inst.ID, "err", err)
			return Limit{}, apperr.Wrap(apperr.Unavailable, "couldn't check the budget cap just now; try again shortly")
		}
		if sp.CapUSD <= 0 {
			continue
		}
		committed := sp.CommittedUSD
		if !inst.CreatedAt.Before(monthStart(now)) { // only a lab in this month's sum is in committed
			committed -= inst.EstimateUSD
		}
		headroom := sp.CapUSD - committed
		if headroom <= 0 {
			return Limit{}, apperr.Wrap(apperr.Conflict, "this lab would pass the budget hard cap; ask an admin")
		}
		d := min(headroom/inst.HourlyUSD, 24*365*10) * float64(time.Hour) // clamp: no Duration overflow
		best = EffectiveEnd(best, Limit{At: now.Add(time.Duration(d)), Reason: "budget"})
	}
	return best, nil
}
