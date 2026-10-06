package labs

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/rbac"
)

type LedgerTeam struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Spend    Spend           `json:"spend"`
	Programs []LedgerProgram `json:"programs"`
}

type LedgerProgram struct {
	Training string `json:"training"`
	Spend    Spend  `json:"spend"`
}

type LedgerDay struct {
	Day         string  `json:"day"`          // 2026-10-04
	EstimateUSD float64 `json:"estimate_usd"` // estimated cost of the labs started that day
	ActualUSD   float64 `json:"actual_usd"`   // Cost Explorer's cost on that day
}

type LedgerLab struct {
	ID          string     `json:"id"`
	Team        string     `json:"team"`
	Training    string     `json:"training"`
	Module      string     `json:"module"`
	Requester   string     `json:"requester"`
	Runtime     string     `json:"runtime"`
	State       State      `json:"state"`
	HourlyUSD   float64    `json:"hourly_usd"`
	EstimateUSD float64    `json:"estimate_usd"`
	CostUSD     float64    `json:"cost_usd"`             // hourly estimate × time run so far
	ActualUSD   *float64   `json:"actual_usd,omitempty"` // Cost Explorer's total so far
	Settled     bool       `json:"settled"`              // the actual replaces the estimate in spend
	CreatedAt   time.Time  `json:"created_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
}

func (l LedgerLab) usd() float64 {
	if l.Settled {
		return *l.ActualUSD
	}
	return l.CostUSD
}

type Spender struct {
	Requester string  `json:"requester"`
	Team      string  `json:"team"`
	USD       float64 `json:"usd"`
}

type Accuracy struct {
	Labs        int     `json:"labs"`
	EstimateUSD float64 `json:"estimate_usd"`
	ActualUSD   float64 `json:"actual_usd"`
}

type Finding struct {
	Source  string    `json:"source"`
	LabID   string    `json:"lab_id"`
	ARN     string    `json:"arn"`
	Action  string    `json:"action"`
	Detail  string    `json:"detail"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// Ledger is the FinOps page (spec §9.3), scoped to the teams the viewer may see spend for.
type Ledger struct {
	Month        string       `json:"month"`
	AWS          bool         `json:"aws"`
	ActualsAsOf  *time.Time   `json:"actuals_as_of,omitempty"`
	ActualsStale bool         `json:"actuals_stale"`
	ActualsError string       `json:"actuals_error,omitempty"` // admins only
	ReapedAt     *time.Time   `json:"reaped_at,omitempty"`
	ReaperStale  bool         `json:"reaper_stale"`           // no successful reaper run for 12 hours (or a newer failure)
	ReaperError  string       `json:"reaper_error,omitempty"` // admins only
	CanRefresh   bool         `json:"can_refresh"`
	Teams        []LedgerTeam `json:"teams"`
	Daily        []LedgerDay  `json:"daily"`
	Running      []LedgerLab  `json:"running"`
	Labs         []LedgerLab  `json:"labs"` // this month, most expensive first (at most 100)
	TopSpenders  []Spender    `json:"top_spenders"`
	Accuracy     Accuracy     `json:"accuracy"`
	Findings     []Finding    `json:"findings,omitempty"` // admins only
}

func (s *Service) Ledger(ctx context.Context, u *auth.User) (*Ledger, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	p, c := st.Platform, rbac.Checker{P: st.Platform}
	admin, teams := c.IsAdmin(u.Email), c.SpendTeams(u.Email)
	if len(teams) == 0 {
		return nil, apperr.Wrap(apperr.Forbidden, "the Ledger is for team leaders, program managers, approvers and admins")
	}
	now := s.Now()
	month := monthStart(now)
	l := &Ledger{Month: month.Format("2006-01"), AWS: s.Cloud != nil, CanRefresh: admin && s.Cloud != nil,
		Teams: []LedgerTeam{}, Daily: []LedgerDay{}, Running: []LedgerLab{}, Labs: []LedgerLab{}, TopSpenders: []Spender{}}
	for _, id := range teams {
		t := p.Teams[id]
		sp, err := s.spend(ctx, p, id, "")
		if err != nil {
			return nil, err
		}
		lt := LedgerTeam{ID: id, Name: t.Name, Spend: sp, Programs: []LedgerProgram{}}
		for _, tr := range slices.Sorted(maps.Keys(t.Programs)) {
			psp, err := s.spend(ctx, p, id, tr)
			if err != nil {
				return nil, err
			}
			lt.Programs = append(lt.Programs, LedgerProgram{Training: tr, Spend: psp})
		}
		l.Teams = append(l.Teams, lt)
	}

	var ingestOK, ingestErrAt *time.Time
	var ingestErr, reapErr string
	var reapErrAt *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT ingest_ok_at, ingest_error, ingest_error_at, reap_ok_at, reap_error, reap_error_at FROM aws_ops`).
		Scan(&ingestOK, &ingestErr, &ingestErrAt, &l.ReapedAt, &reapErr, &reapErrAt); err != nil {
		return nil, err
	}
	l.ActualsAsOf = ingestOK
	l.ActualsStale = l.AWS && (ingestOK == nil || ingestOK.Before(now.Add(-36*time.Hour)) ||
		(ingestErrAt != nil && ingestErrAt.After(*ingestOK)))
	l.ReaperStale = l.AWS && (l.ReapedAt == nil || l.ReapedAt.Before(now.Add(-12*time.Hour)) ||
		(reapErrAt != nil && reapErrAt.After(*l.ReapedAt)))
	if admin {
		l.ReaperError = reapErr
		l.ActualsError = ingestErr
	}

	rows, err := s.DB.Query(ctx, `WITH labs AS (`+labCostSQL+` WHERE l.team = ANY($2) AND l.created_at >= $3)
		SELECT labs.id, labs.team, labs.training, labs.module, coalesce(u.email, ''), labs.runtime, labs.state,
			labs.hourly_usd, labs.estimate_usd, labs.est_spent, labs.actual, labs.settled, labs.created_at, labs.ends_at
		FROM labs LEFT JOIN users u ON u.id = labs.user_id
		WHERE labs.ready_at IS NOT NULL OR labs.state = 'provisioning' OR labs.actual IS NOT NULL
			OR (labs.runtime = 'aws' AND labs.state = 'destroying')`, now, teams, month)
	if err != nil {
		return nil, err
	}
	var all []LedgerLab
	for rows.Next() {
		var x LedgerLab
		if err := rows.Scan(&x.ID, &x.Team, &x.Training, &x.Module, &x.Requester, &x.Runtime, &x.State, &x.HourlyUSD,
			&x.EstimateUSD, &x.CostUSD, &x.ActualUSD, &x.Settled, &x.CreatedAt, &x.EndsAt); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	day := map[string]int{} // "2026-10-04" → index in l.Daily: one entry per day of the month so far
	for d := month; !d.After(now); d = d.AddDate(0, 0, 1) {
		day[d.Format(time.DateOnly)] = len(l.Daily)
		l.Daily = append(l.Daily, LedgerDay{Day: d.Format(time.DateOnly)})
	}
	spenders := map[[2]string]float64{}
	for _, x := range all {
		if x.State == Provisioning || x.State == Ready || x.State == Destroying && x.Runtime == "aws" { // aws: live resources until terraform destroy ends
			l.Running = append(l.Running, x)
		}
		if i, ok := day[x.CreatedAt.UTC().Format(time.DateOnly)]; ok {
			l.Daily[i].EstimateUSD += x.CostUSD
		}
		spenders[[2]string{x.Requester, x.Team}] += x.usd()
		if x.Settled {
			l.Accuracy.Labs++
			l.Accuracy.EstimateUSD += x.CostUSD
			l.Accuracy.ActualUSD += *x.ActualUSD
		}
	}
	arows, err := s.DB.Query(ctx, `SELECT c.day, sum(c.usd)::float8 FROM cost_actuals c JOIN lab_instances l ON l.id = c.lab_id
		WHERE l.team = ANY($1) AND c.day >= $2 GROUP BY c.day`, teams, month)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var d time.Time
		var usd float64
		if err := arows.Scan(&d, &usd); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := day[d.Format(time.DateOnly)]; ok {
			l.Daily[i].ActualUSD = usd
		}
	}
	arows.Close()

	slices.SortStableFunc(all, func(a, b LedgerLab) int {
		if a.usd() != b.usd() {
			if a.usd() > b.usd() {
				return -1
			}
			return 1
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	l.Labs = append(l.Labs, all[:min(len(all), 100)]...)
	for k, usd := range spenders {
		if usd > 0 {
			l.TopSpenders = append(l.TopSpenders, Spender{Requester: k[0], Team: k[1], USD: usd})
		}
	}
	slices.SortFunc(l.TopSpenders, func(a, b Spender) int {
		if a.USD > b.USD {
			return -1
		}
		if a.USD < b.USD {
			return 1
		}
		return 0
	})
	l.TopSpenders = l.TopSpenders[:min(len(l.TopSpenders), 5)]

	if admin {
		frows, err := s.DB.Query(ctx, `SELECT source, lab_id, arn, action, detail, first_at, last_at FROM reaper_findings
			ORDER BY last_at DESC LIMIT 100`)
		if err != nil {
			return nil, err
		}
		for frows.Next() {
			var x Finding
			if err := frows.Scan(&x.Source, &x.LabID, &x.ARN, &x.Action, &x.Detail, &x.FirstAt, &x.LastAt); err != nil {
				frows.Close()
				return nil, err
			}
			l.Findings = append(l.Findings, x)
		}
		frows.Close()
	}
	return l, nil
}

// refreshEvery is the least time between two refresh requests (per process).
const refreshEvery = 30 * time.Second

// RefreshFinOps (admin) queues cost ingestion and the reaper now, instead of waiting for the 6-hourly jobs, and
// returns the Ledger as it is: the numbers move once the jobs finish. Queued jobs are unique by kind while one is
// waiting or running, so repeated clicks never stack.
func (s *Service) RefreshFinOps(ctx context.Context, u *auth.User) (*Ledger, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "only admins can refresh costs and run the reaper")
	}
	if s.Jobs == nil {
		return nil, apperr.Wrap(apperr.Unavailable, "the job queue is not running")
	}
	s.refreshMu.Lock()
	wait := refreshEvery - s.Now().Sub(s.refreshedAt)
	if wait <= 0 {
		s.refreshedAt = s.Now()
	}
	s.refreshMu.Unlock()
	if wait > 0 {
		return nil, apperr.Wrap(apperr.Conflict, "a refresh was requested a moment ago: give it a few seconds")
	}
	if err := audit.Log(ctx, s.DB, u.Email, "finops.refresh", "", nil, ""); err != nil {
		return nil, err
	}
	once := &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
	for _, args := range []river.JobArgs{CostArgs{}, ReapArgs{}} {
		if _, err := s.Jobs.Insert(ctx, args, once); err != nil {
			return nil, err
		}
	}
	return s.Ledger(ctx, u)
}
