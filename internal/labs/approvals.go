package labs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/gitsync"
	"crucible/internal/notify"
	"crucible/internal/rbac"
)

// Spend is month-to-date lab spend for a team or one program: estimates, with Cost Explorer actuals swapped in for
// labs whose actual cost has settled (spec §9.3).
type Spend struct {
	SpentUSD     float64 `json:"spent_usd"`     // per lab: settled actual, else hourly estimate × time run this month
	CommittedUSD float64 `json:"committed_usd"` // spent + the rest of every lab still starting or running, to its end
	ActualUSD    float64 `json:"actual_usd"`    // the part of spent that comes from settled actuals
	BudgetUSD    float64 `json:"budget_usd"`    // 0 = no budget
	CapUSD       float64 `json:"cap_usd"`       // 0 = no hard cap
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// labCostSQL selects lab_instances (alias l) with three more columns: actual (Cost Explorer total, NULL when never
// reported), settled (use the actual instead of the estimate: reported, and the lab ended 48 h before the last
// successful ingestion, so Cost Explorer has caught up) and est_spent (hourly estimate × time run). $1 is now.
// Conservative on purpose: a lab Cost Explorer never reported keeps its estimate (tags not activated != free).
const labCostSQL = `SELECT l.*, a.usd AS actual,
	coalesce(a.usd IS NOT NULL AND l.destroyed_at < (SELECT ingest_ok_at FROM aws_ops) - interval '48 hours', false) AS settled,
	CASE WHEN l.ready_at IS NULL THEN 0
		ELSE l.hourly_usd * extract(epoch FROM least(coalesce(l.destroyed_at, $1), $1) - l.ready_at)::float8 / 3600 END AS est_spent
	FROM lab_instances l LEFT JOIN (SELECT lab_id, sum(usd) AS usd FROM cost_actuals GROUP BY lab_id) a ON a.lab_id = l.id`

// spend sums this calendar month (UTC) for a team, or one program when training != "".
// ponytail: a lab counts in the month it was requested; one running across midnight on the 1st stays in the old month.
func (s *Service) spend(ctx context.Context, p *config.Platform, team, training string) (Spend, error) {
	now := s.Now()
	var sp Spend
	err := s.DB.QueryRow(ctx, `WITH labs AS (`+labCostSQL+`
		WHERE l.team = $2 AND ($3 = '' OR l.training = $3) AND l.created_at >= $4)
		SELECT
			coalesce(sum(CASE WHEN settled THEN actual ELSE est_spent END), 0),
			coalesce(sum(CASE
				WHEN settled THEN actual
				WHEN state = 'provisioning' THEN estimate_usd
				WHEN state = 'ready' THEN hourly_usd * extract(epoch FROM greatest(ends_at, $1) - ready_at)::float8 / 3600
				ELSE est_spent END), 0),
			coalesce(sum(CASE WHEN settled THEN actual ELSE 0 END), 0)
		FROM labs`, now, team, training, monthStart(now)).Scan(&sp.SpentUSD, &sp.CommittedUSD, &sp.ActualUSD)
	if err != nil {
		return sp, err
	}
	if t := p.Teams[team]; t != nil {
		if training == "" {
			sp.BudgetUSD, sp.CapUSD = t.Budget.MonthlyUSD, t.Budget.HardCapUSD
		} else if pr := t.Programs[training]; pr != nil {
			sp.BudgetUSD, sp.CapUSD = pr.BudgetUSDMonth, pr.BudgetUSDMonth
		}
	}
	return sp, nil
}

type RecentLab struct {
	Module      string    `json:"module"`
	State       State     `json:"state"`
	EndReason   string    `json:"end_reason,omitempty"`
	EstimateUSD float64   `json:"estimate_usd"`
	CreatedAt   time.Time `json:"created_at"`
}

type ScheduleInfo struct {
	Name     string     `json:"name"` // "" = any time
	Text     string     `json:"text"`
	Open     bool       `json:"open"`
	ClosesAt *time.Time `json:"closes_at,omitempty"`
	NextOpen *time.Time `json:"next_open,omitempty"`
}

// Approval is one pending request with what an approver needs to decide (spec §9.1.3).
type Approval struct {
	ID            string       `json:"id"`
	Requester     string       `json:"requester"`
	RequesterName string       `json:"requester_name"`
	Team          string       `json:"team"`
	Training      string       `json:"training"`
	Module        string       `json:"module"`
	LabTitle      string       `json:"lab_title"`
	Runtime       string       `json:"runtime"`
	HourlyUSD     float64      `json:"hourly_usd"`
	EstimateUSD   float64      `json:"estimate_usd"`
	TTLS          int          `json:"ttl_s"`
	Tier          string       `json:"tier"`
	OverCap       bool         `json:"over_cap"`
	RequestedAt   time.Time    `json:"requested_at"`
	EscalateAt    *time.Time   `json:"escalate_at,omitempty"`
	TeamSpend     Spend        `json:"team_spend"`
	ProgramSpend  Spend        `json:"program_spend"`
	Recent        []RecentLab  `json:"recent"`
	Schedule      ScheduleInfo `json:"schedule"`
}

func scheduleInfo(p *config.Platform, team, training string, now time.Time) ScheduleInfo {
	sc := p.ProgramSchedule(team, training)
	info := ScheduleInfo{Text: sc.String(), Open: sc.Open(now)}
	if t := p.Teams[team]; t != nil && t.Programs[training] != nil {
		info.Name = t.Programs[training].Schedule
	}
	if end := sc.End(now); !end.IsZero() {
		info.ClosesAt = &end
	}
	if !info.Open {
		if n := sc.NextOpen(now); !n.IsZero() {
			info.NextOpen = &n
		}
	}
	return info
}

func (s *Service) requester(ctx context.Context, userID int64) (email, name string, err error) {
	err = s.DB.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1`, userID).Scan(&email, &name)
	return
}

func labLink(inst *Instance) string {
	return fmt.Sprintf("/p/%s/%s/m/%s/lab", inst.Team, inst.Training, inst.Module)
}

// notifyRequest tells the people at the request's current tier (and the team channel) that it waits for them.
func (s *Service) notifyRequest(ctx context.Context, p *config.Platform, inst *Instance, requester string, kind notify.Kind) {
	verb := "is waiting for approval"
	if kind == notify.LabEscalated {
		verb = "was escalated: nobody answered in time"
	}
	s.notify(ctx, notify.Event{Kind: kind, To: rbac.Checker{P: p}.TierApprovers(inst.Tier, inst.Team, inst.Training, requester),
		Team:    inst.Team,
		Subject: fmt.Sprintf("Lab request from %s (%s, est. $%.2f)", requester, inst.Training, inst.EstimateUSD),
		Text: fmt.Sprintf("%s requested the %s lab in %s/%s, estimated at $%.2f. It %s.",
			requester, inst.Module, inst.Team, inst.Training, inst.EstimateUSD, verb),
		Link: "/approvals"})
}

func (s *Service) platform() (*gitsync.State, error) {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "content is still syncing, try again in a moment")
	}
	return st, nil
}

// Approvals lists the pending requests the user may decide, oldest first.
func (s *Service) Approvals(ctx context.Context, u *auth.User) ([]Approval, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE state = 'pending_approval' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	pending, err := collectInst(rows)
	if err != nil {
		return nil, err
	}
	c := rbac.Checker{P: st.Platform}
	out := []Approval{}
	for _, inst := range pending {
		email, name, err := s.requester(ctx, inst.UserID)
		if err != nil {
			return nil, err
		}
		if !c.MayApprove(u.Email, email, inst.Team, inst.Training, inst.EstimateUSD, inst.OverCap) {
			continue
		}
		a, err := s.approval(ctx, st, inst, email, name)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

func (s *Service) approval(ctx context.Context, st *gitsync.State, inst *Instance, email, name string) (*Approval, error) {
	a := &Approval{ID: inst.ID, Requester: email, RequesterName: name, Team: inst.Team, Training: inst.Training,
		Module: inst.Module, LabTitle: inst.Module, Runtime: inst.Runtime, HourlyUSD: inst.HourlyUSD, EstimateUSD: inst.EstimateUSD,
		TTLS: int(inst.TTL.Seconds()), Tier: inst.Tier, OverCap: inst.OverCap, RequestedAt: inst.CreatedAt, EscalateAt: inst.EscalateAt,
		Schedule: scheduleInfo(st.Platform, inst.Team, inst.Training, s.Now())}
	if t := st.Training(inst.Training, inst.SHA); t != nil {
		if m := t.Module(inst.Module); m != nil {
			a.LabTitle = m.Title
		}
	}
	var err error
	if a.TeamSpend, err = s.spend(ctx, st.Platform, inst.Team, ""); err != nil {
		return nil, err
	}
	if a.ProgramSpend, err = s.spend(ctx, st.Platform, inst.Team, inst.Training); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT module, state, end_reason, estimate_usd, created_at FROM lab_instances
		WHERE user_id = $1 AND id <> $2 ORDER BY created_at DESC LIMIT 5`, inst.UserID, inst.ID)
	if err != nil {
		return nil, err
	}
	if a.Recent, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RecentLab]); err != nil {
		return nil, err
	}
	if a.Recent == nil {
		a.Recent = []RecentLab{}
	}
	return a, nil
}

// Decide approves (provisioning starts at once) or rejects a pending request.
func (s *Service) Decide(ctx context.Context, u *auth.User, labID string, approve bool, note string) (State, error) {
	st, err := s.platform()
	if err != nil {
		return "", err
	}
	p := st.Platform
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
	if err != nil {
		return "", err
	}
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		return "", err
	}
	if !(rbac.Checker{P: p}).MayApprove(u.Email, email, inst.Team, inst.Training, inst.EstimateUSD, inst.OverCap) {
		return "", apperr.Wrap(apperr.Forbidden, "you can't decide this request")
	}
	if inst.State != PendingApproval {
		return "", apperr.Wrap(apperr.Conflict, "this request was already decided")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	note = strings.TrimSpace(cleanText(note))
	next := Rejected
	var lab *content.Lab
	now := s.Now()
	// the decision and its audit entry commit together: no unaudited approvals
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if approve {
		// serialize cap re-check + approve per team so two approvals can't both fit under one cap
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, inst.Team); err != nil {
			return "", err
		}
		if sc := p.ProgramSchedule(inst.Team, inst.Training); !sc.Open(s.Now()) {
			return "", apperr.Wrap(apperr.Conflict, "The program's schedule window is closed; approve it when it opens.")
		}
		if tp := p.Teams[inst.Team]; tp == nil || tp.Programs[inst.Training] == nil || !slices.Contains(tp.Programs[inst.Training].Enrolled, strings.ToLower(email)) {
			return "", apperr.Wrap(apperr.Conflict, "This person is no longer enrolled in the program.")
		}
		// FOR SHARE conflicts with SetKillSwitch's FOR UPDATE, so a pause and this approval can't interleave
		var paused bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM kill_switch FOR SHARE`).Scan(&paused); err != nil {
			return "", err
		} else if paused {
			return "", apperr.Wrap(apperr.Conflict, "Labs are paused by an admin.")
		}
		if !inst.OverCap && inst.EstimateUSD > 0 {
			over, err := s.overCap(ctx, p, inst.Team, inst.Training, inst.EstimateUSD)
			if err != nil {
				return "", err
			}
			if over {
				tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET over_cap = true, tier = 'admin', escalate_at = $2
					WHERE id = $1 AND state = 'pending_approval'`, inst.ID, escalateAt(p, inst.Team, inst.Training, now))
				if err != nil {
					return "", err
				}
				if tag.RowsAffected() == 0 {
					return "", apperr.Wrap(apperr.Conflict, "this request was already decided")
				}
				if !(rbac.Checker{P: p}).IsAdmin(u.Email) {
					inst.Tier = string(rbac.TierAdmin)
					s.notifyRequest(ctx, p, inst, email, notify.LabPending)
					return "", apperr.Wrap(apperr.Conflict, "approving this would now pass a budget cap, so it has been passed to an admin")
				}
				inst.OverCap = true // an admin approving it now is an audited override
			}
		}
		if lab, _, err = s.labContent(inst); err != nil {
			return "", err
		}
		if _, err := s.runner(inst.Runtime); err != nil {
			return "", err // stays pending: nothing can provision it
		}
		next = Provisioning
	}
	action := "lab.reject"
	if approve {
		action = "lab.approve"
		if inst.OverCap {
			action = "lab.budget_override"
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE lab_instances SET state = $2, decided_by = $3, decided_at = $4, decision_note = $5,
		destroyed_at = CASE WHEN $2 = 'rejected' THEN $4::timestamptz END
		WHERE id = $1 AND state = 'pending_approval'`, inst.ID, string(next), strings.ToLower(u.Email), now, note)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", apperr.Wrap(apperr.Conflict, "this request was already decided")
	}
	if err := audit.Log(ctx, tx, u.Email, action, inst.ID, map[string]any{"requester": email, "team": inst.Team,
		"training": inst.Training, "module": inst.Module, "estimate_usd": inst.EstimateUSD, "note": note}, ""); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	verb, kind := "rejected", notify.LabRejected
	if approve {
		verb, kind = "approved", notify.LabApproved
	}
	s.event(ctx, inst.ID, verb, strings.ToLower(u.Email)+": "+note)
	text := fmt.Sprintf("%s %s your request for the %s lab (%s/%s).", u.Email, verb, inst.Module, inst.Team, inst.Training)
	if note != "" {
		text += " Note: " + note
	}
	s.notify(ctx, notify.Event{Kind: kind, To: []string{email}, Subject: fmt.Sprintf("Your %s lab request was %s", inst.Training, verb),
		Text: text, Link: labLink(inst)})
	if approve {
		inst.State = Provisioning
		go s.provision(context.WithoutCancel(ctx), inst, lab)
	}
	return next, nil
}

// escalate moves an unanswered request one tier up (approver → leader → admin, skipping tiers with nobody but the
// requester) or, after the admin tier also timed out, expires it (spec §9.1.5). Guarded by the current tier so a
// concurrent decision or a second sweep never applies it twice.
func (s *Service) escalate(ctx context.Context, inst *Instance) {
	st, err := s.platform()
	if err != nil {
		return
	}
	p := st.Platform
	email, _, err := s.requester(ctx, inst.UserID)
	if err != nil {
		s.Log.Error("escalation: requester lookup failed", "lab", inst.ID, "err", err)
		return
	}
	now := s.Now()
	next := rbac.NextTier(inst.Tier)
	if next != "" {
		next = rbac.Checker{P: p}.Route(next, inst.Team, inst.Training, email)
	}
	if next == "" {
		tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'expired', end_reason = 'unanswered', destroyed_at = $2
			WHERE id = $1 AND state = 'pending_approval' AND tier = $3`, inst.ID, now, inst.Tier)
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		s.event(ctx, inst.ID, "expired", "nobody answered at any tier")
		s.notify(ctx, notify.Event{Kind: notify.LabRejected, To: []string{email},
			Subject: fmt.Sprintf("Your %s lab request expired", inst.Training),
			Text:    fmt.Sprintf("Nobody answered your request for the %s lab (%s/%s) in time. You can request it again.", inst.Module, inst.Team, inst.Training),
			Link:    labLink(inst)})
		return
	}
	at := escalateAt(p, inst.Team, inst.Training, now)
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET tier = $2, escalate_at = $3
		WHERE id = $1 AND state = 'pending_approval' AND tier = $4`, inst.ID, next, at, inst.Tier)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	s.event(ctx, inst.ID, "escalated", inst.Tier+" → "+next)
	inst.Tier = next
	s.notifyRequest(ctx, p, inst, email, notify.LabEscalated)
}
