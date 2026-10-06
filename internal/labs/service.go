package labs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"crucible/internal/agenthub"
	"crucible/internal/apperr"
	"crucible/internal/auth"
	"crucible/internal/awscloud"
	"crucible/internal/blob"
	"crucible/internal/config"
	"crucible/internal/content"
	"crucible/internal/learn"
	"crucible/internal/notify"
	"crucible/internal/rbac"
	"crucible/internal/scoring"
)

// Notifier queues notifications (implemented by *notify.Service).
type Notifier interface {
	Notify(ctx context.Context, ev notify.Event) error
}

type Service struct {
	Notify     Notifier
	DB         *pgxpool.Pool
	Learn      *learn.Service
	Runners    map[string]Runner
	Estimators map[string]Estimator // by runtime; a runtime without one cannot be requested
	Now        func() time.Time
	Log        *slog.Logger
	Scoring    *scoring.Service // human review (M5); nil in tests that don't need it
	Blobs      blob.Store       // transcripts (Task 6)
	Cloud      awscloud.Cloud   // the shared AWS lab account; nil when aws labs are off
	AWSRegions []string         // regions aws labs may run in (the lab account's allowed_regions); empty blocks every aws lab

	Jobs *river.Client[pgx.Tx] // queue for "Refresh now" on the Ledger; nil in tests that don't need it

	refreshMu   sync.Mutex
	refreshedAt time.Time

	sweepMu  sync.Mutex // one sweep at a time in this process
	inflight sync.Map   // lab ids with a slow (aws) destroy running in this process

	touchMu sync.Mutex
	touched map[string]time.Time

	setupMu    sync.Mutex
	setupLocks map[string]*sync.Mutex // per lab: one setup decision at a time; dropped on destroy
}

// setupLock serialises setup runs for one lab so concurrent opens/resets cannot run a scenario twice.
// ponytail: in-process lock; a second API replica would need a DB advisory lock.
func (s *Service) setupLock(labID string) *sync.Mutex {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupLocks == nil {
		s.setupLocks = map[string]*sync.Mutex{}
	}
	if s.setupLocks[labID] == nil {
		s.setupLocks[labID] = &sync.Mutex{}
	}
	return s.setupLocks[labID]
}

type TaskView struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"` // locked | open | setup_failed | submitted | passed | skipped
	Kind          string            `json:"kind"`   // check | quiz | review
	Points        float64           `json:"points"`
	Awarded       float64           `json:"awarded"`
	QuizPrompt    string            `json:"quiz_prompt,omitempty"`
	HasSetup      bool              `json:"has_setup"`
	HintsTotal    int               `json:"hints_total"`
	HintsRevealed int               `json:"hints_revealed"`
	NextHintCost  float64           `json:"next_hint_cost"`
	Review        *scoring.Feedback `json:"review,omitempty"` // review tasks: the latest submission, never the rubric
}

type View struct {
	ID               string             `json:"id"`
	State            State              `json:"state"`
	Error            string             `json:"error,omitempty"`
	Runtime          string             `json:"runtime"`
	Team             string             `json:"team"`
	Training         string             `json:"training"`
	Module           string             `json:"module"`
	Terminals        []content.Terminal `json:"terminals"`
	TaskOrder        string             `json:"task_order"`
	Tasks            []TaskView         `json:"tasks"`
	ServerNow        time.Time          `json:"server_now"`
	EndsAt           *time.Time         `json:"ends_at,omitempty"`
	LimitReason      string             `json:"limit_reason,omitempty"`
	EndReason        string             `json:"end_reason,omitempty"`
	IdleDeadline     *time.Time         `json:"idle_deadline,omitempty"`
	IdleWarningS     int                `json:"idle_warning_s"`
	CanExtend        bool               `json:"can_extend"`
	ExtensionPending bool               `json:"extension_pending"`
	SelfReported     bool               `json:"self_reported"`
	LabReview        *scoring.Feedback  `json:"lab_review,omitempty"` // a scorer's review of the whole self-reported lab
	Complete         bool               `json:"complete"`
	Score            float64            `json:"score"`
	MaxScore         float64            `json:"max_score"`
	EstimateUSD      float64            `json:"estimate_usd"`
	Tier             string             `json:"tier"`
	OverCap          bool               `json:"over_cap"`
	EscalateAt       *time.Time         `json:"escalate_at,omitempty"`
	DecidedBy        string             `json:"decided_by,omitempty"`
	DecisionNote     string             `json:"decision_note,omitempty"`
}

type TaskDetail struct {
	TaskView
	Instructions string   `json:"instructions"`
	Hints        []string `json:"hints"`
	SetupError   string   `json:"setup_error,omitempty"`
}

type CheckResult struct {
	Passed   bool    `json:"passed"`
	Output   string  `json:"output"`
	TimedOut bool    `json:"timed_out"`
	Awarded  float64 `json:"awarded"`
	Lab      *View   `json:"lab"`
}

type HintResult struct {
	Index int     `json:"index"`
	Text  string  `json:"text"`
	Cost  float64 `json:"cost"`
	Lab   *View   `json:"lab"`
}

type ModuleLab struct {
	Title          string  `json:"title"`
	Runtime        string  `json:"runtime"`
	RuntimeReady   bool    `json:"runtime_ready"`
	RuntimeMessage string  `json:"runtime_message,omitempty"`
	EstimateUSD    float64 `json:"estimate_usd"`
	NeedsApproval  bool    `json:"needs_approval"`
	Blocked        string  `json:"blocked,omitempty"` // why a request can't be made now (schedule, kill switch)
	Lab            *View   `json:"lab"`
}

const instCols = `id, user_id, team, training, module, sha, runtime, state, error, created_at, ready_at, ends_at,
	limit_reason, end_reason, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s, extended,
	hourly_usd, estimate_usd, tier, over_cap, escalate_at, decided_by, decided_at, decision_note,
	ext_until, ext_estimate_usd, ext_tier, ext_requested_at`

func scanInst(row pgx.Row) (*Instance, error) {
	var in Instance
	var ttl, idle, warn, ext int
	err := row.Scan(&in.ID, &in.UserID, &in.Team, &in.Training, &in.Module, &in.SHA, &in.Runtime, &in.State, &in.Error,
		&in.CreatedAt, &in.ReadyAt, &in.EndsAt, &in.LimitReason, &in.EndReason, &in.LastActivityAt, &ttl, &idle, &warn, &ext, &in.Extended,
		&in.HourlyUSD, &in.EstimateUSD, &in.Tier, &in.OverCap, &in.EscalateAt, &in.DecidedBy, &in.DecidedAt, &in.DecisionNote,
		&in.ExtUntil, &in.ExtEstimateUSD, &in.ExtTier, &in.ExtRequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	if err != nil {
		return nil, err
	}
	in.TTL, in.IdleTimeout = time.Duration(ttl)*time.Second, time.Duration(idle)*time.Second
	in.IdleWarning, in.MaxExtension = time.Duration(warn)*time.Second, time.Duration(ext)*time.Second
	return &in, nil
}

func collectInst(rows pgx.Rows) ([]*Instance, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Instance, error) { return scanInst(r) })
}

func newLabID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) event(ctx context.Context, labID, kind, detail string) {
	if _, err := s.DB.Exec(ctx, `INSERT INTO lab_events (lab_id, kind, detail) VALUES ($1, $2, $3)`, labID, kind, detail); err != nil {
		s.Log.Warn("lab event not recorded", "lab", labID, "kind", kind, "err", err)
	}
}

// finalCtx is for a lab's last state write: the ctx that ran terraform or a namespace delete may be used up.
func finalCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// endRow writes a lab's final state; a failure is logged (the sweep retries a row left 'destroying').
func (s *Service) endRow(ctx context.Context, labID, sql string, args ...any) {
	if _, err := s.DB.Exec(ctx, sql, append([]any{labID}, args...)...); err != nil {
		s.Log.Error("lab final state not written", "lab", labID, "err", err)
	}
}

func (s *Service) runnerErr(err error) error {
	if errors.Is(err, agenthub.ErrOffline) {
		return errAgentOffline
	}
	return err
}

// notify queues ev; a failure is logged, never returned: the lab action itself already happened.
func (s *Service) notify(ctx context.Context, ev notify.Event) {
	if s.Notify == nil || (len(ev.To) == 0 && ev.Team == "") {
		return
	}
	if err := s.Notify.Notify(context.WithoutCancel(ctx), ev); err != nil {
		s.Log.Error("queueing notification failed", "kind", ev.Kind, "err", err)
	}
}

// trainingOf returns exactly the content version a lab runs (loaded from the mirror if a restart dropped it), or nil
// while syncing / if it can't be loaded. Never another version: its tasks, points and scripts may differ.
func (s *Service) trainingOf(ctx context.Context, inst *Instance) *content.Training {
	return s.Learn.Version(ctx, inst.Training, inst.SHA)
}

func (s *Service) labContent(ctx context.Context, inst *Instance) (*content.Lab, *content.Quiz, error) {
	if s.Learn.State() == nil {
		return nil, nil, apperr.Wrap(apperr.Unavailable, "content is still syncing")
	}
	t := s.trainingOf(ctx, inst)
	if t == nil {
		return nil, nil, apperr.Wrap(apperr.Unavailable, "this lab's content is no longer available")
	}
	m := t.Module(inst.Module)
	if m == nil || m.Lab == nil {
		return nil, nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	return m.Lab, m.Quiz, nil
}

func (s *Service) owned(ctx context.Context, u *auth.User, labID string) (*Instance, error) {
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
	if err != nil {
		return nil, err
	}
	if inst.UserID != u.ID {
		return nil, apperr.Wrap(apperr.NotFound, "lab not found")
	}
	return inst, nil
}

type taskRow struct {
	Status string
	Points float64
}

func (s *Service) taskRows(ctx context.Context, inst *Instance) (map[string]taskRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT task, status, points FROM lab_task_progress
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4`, inst.UserID, inst.Team, inst.Training, inst.Module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]taskRow{}
	for rows.Next() {
		var id string
		var r taskRow
		if err := rows.Scan(&id, &r.Status, &r.Points); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

func (s *Service) hintCounts(ctx context.Context, inst *Instance) (map[string]int, map[string]float64, error) {
	rows, err := s.DB.Query(ctx, `SELECT task, count(*), coalesce(sum(cost), 0) FROM hint_reveals
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 GROUP BY task`, inst.UserID, inst.Team, inst.Training, inst.Module)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	counts, costs := map[string]int{}, map[string]float64{}
	for rows.Next() {
		var id string
		var n int
		var c float64
		if err := rows.Scan(&id, &n, &c); err != nil {
			return nil, nil, err
		}
		counts[id], costs[id] = n, c
	}
	return counts, costs, rows.Err()
}

// setupStatus returns task → "ok" | "failed" from the latest setup run in this lab instance.
func (s *Service) setupStatus(ctx context.Context, labID string) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT ON (task) task, exit_code FROM setup_runs
		WHERE lab_id = $1 ORDER BY task, id DESC`, labID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var task string
		var code int
		if err := rows.Scan(&task, &code); err != nil {
			return nil, err
		}
		out[task] = map[bool]string{true: "ok", false: "failed"}[code == 0]
	}
	return out, rows.Err()
}

func taskStatuses(lab *content.Lab, done map[string]taskRow, setups map[string]string, reviews map[string]*scoring.Submission) map[string]string {
	out := map[string]string{}
	opened := false
	for _, t := range lab.Tasks {
		if r, ok := done[t.ID]; ok {
			out[t.ID] = r.Status
			continue
		}
		sub := reviews[t.ID]
		if sub != nil && sub.Status == scoring.Pending {
			out[t.ID] = "submitted" // with a scorer; later tasks go on (M5 ruling 4)
			continue
		}
		returned := sub != nil && sub.Status == scoring.Returned // needs rework, but never re-locks what it unlocked
		if !returned && lab.TaskOrder == "linear" && opened {
			out[t.ID] = "locked"
			continue
		}
		opened = opened || !returned
		if setups[t.ID] == "failed" {
			out[t.ID] = "setup_failed"
		} else {
			out[t.ID] = "open"
		}
	}
	return out
}

func taskTitle(lab *content.Lab, t *content.Task) string {
	b, _ := os.ReadFile(filepath.Join(lab.Dir, t.Instructions))
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return t.ID
}

func (s *Service) view(ctx context.Context, inst *Instance) (*View, error) {
	lab, quiz, err := s.labContent(ctx, inst)
	if err != nil {
		return nil, err
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return nil, err
	}
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return nil, err
	}
	wrote := s.settle(ctx, inst, lab, done, reviews)
	rv, err := s.labReview(ctx, inst)
	if err != nil {
		return nil, err
	}
	if s.settleLab(ctx, inst, lab, reviews, rv) || wrote {
		if done, err = s.taskRows(ctx, inst); err != nil {
			return nil, err
		}
	}
	counts, _, err := s.hintCounts(ctx, inst)
	if err != nil {
		return nil, err
	}
	setups, err := s.setupStatus(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	statuses := taskStatuses(lab, done, setups, reviews)
	v := &View{ID: inst.ID, State: inst.State, Error: inst.Error, Runtime: inst.Runtime, Team: inst.Team,
		Training: inst.Training, Module: inst.Module, Terminals: lab.Terminals, TaskOrder: lab.TaskOrder,
		ServerNow: s.Now(), EndsAt: inst.EndsAt, LimitReason: inst.LimitReason, EndReason: inst.EndReason,
		IdleWarningS: int(inst.IdleWarning.Seconds()),
		EstimateUSD:  inst.EstimateUSD, Tier: inst.Tier, OverCap: inst.OverCap, EscalateAt: inst.EscalateAt, DecidedBy: inst.DecidedBy, DecisionNote: inst.DecisionNote, SelfReported: inst.Runtime == "local", Complete: true,
		LabReview: rv.Feedback()}
	for _, t := range lab.Tasks {
		tv := TaskView{ID: t.ID, Title: taskTitle(lab, t), Status: statuses[t.ID], Points: t.Points,
			Awarded: done[t.ID].Points, HasSetup: t.Setup != nil, HintsTotal: len(t.Hints), HintsRevealed: counts[t.ID]}
		switch {
		case t.Quiz != "":
			tv.Kind, tv.QuizPrompt = "quiz", quiz.Question(t.Quiz).Prompt
		case t.Check != nil:
			tv.Kind = "check"
		default:
			tv.Kind, tv.Review = "review", reviews[t.ID].Feedback()
		}
		if n := counts[t.ID]; n < len(t.Hints) {
			tv.NextHintCost = t.Hints[n].EffectiveCost(lab)
		}
		if tv.Status != "passed" && tv.Status != "skipped" {
			v.Complete = false
		}
		v.Score += tv.Awarded
		if tv.Status != "skipped" { // skipping is without penalty (spec §8.5)
			v.MaxScore += t.Points
		}
		v.Tasks = append(v.Tasks, tv)
	}
	if rv != nil && rv.Status == scoring.Scored { // the scorer's points are the lab's score, not the self-reported sum
		v.Score = rv.Points
	}
	if rv != nil && rv.Status == scoring.Pending { // the lab is with a scorer, not forged yet
		v.Complete = false
	}
	if inst.State == Ready {
		dl := inst.LastActivityAt.Add(inst.IdleTimeout)
		v.IdleDeadline = &dl
		v.ExtensionPending = inst.ExtUntil != nil
		v.CanExtend = !inst.Extended && inst.MaxExtension > 0 && inst.LimitReason != "schedule" && inst.LimitReason != "budget"
	}
	return v, nil
}

func (s *Service) active(ctx context.Context, userID int64, team, training, module string) (*Instance, error) {
	inst, err := scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE user_id = $1 AND team = $2 AND training = $3 AND module = $4 ORDER BY created_at DESC LIMIT 1`,
		userID, team, training, module))
	if errors.Is(err, apperr.NotFound) {
		return nil, nil
	}
	return inst, err
}

func (s *Service) ModuleLab(ctx context.Context, u *auth.User, team, training, module string) (*ModuleLab, error) {
	st, t, _, err := s.Learn.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	m, err := s.Learn.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, err
	}
	if m.Lab == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this module has no lab")
	}
	out := &ModuleLab{Title: m.Title, Runtime: m.Lab.Runtime, RuntimeReady: true}
	if r := s.Runners[m.Lab.Runtime]; r == nil {
		out.RuntimeReady, out.RuntimeMessage = false, fmt.Sprintf("%s labs are not available yet", m.Lab.Runtime)
	} else if err := r.Available(&Instance{UserID: u.ID}); err != nil {
		out.RuntimeReady, out.RuntimeMessage = false, strings.TrimSuffix(err.Error(), ": "+apperr.Unavailable.Error())
	}
	if q, err := s.quote(ctx, st.Platform, u, team, training, module, m.Lab); err != nil {
		// only our own user-facing messages reach the trainee; anything else (estimator, DB) is logged
		switch {
		case errors.Is(err, apperr.Unavailable):
			out.Blocked = strings.TrimSuffix(err.Error(), ": "+apperr.Unavailable.Error())
		case errors.Is(err, apperr.Conflict):
			out.Blocked = strings.TrimSuffix(err.Error(), ": "+apperr.Conflict.Error())
		default:
			s.Log.Error("lab quote failed", "team", team, "training", training, "module", module, "err", err)
			out.Blocked = "the lab can't be requested right now"
		}
	} else {
		out.EstimateUSD, out.NeedsApproval, out.Blocked = q.EstimateUSD, q.Tier != rbac.TierAuto, q.Blocked
	}
	inst, err := s.active(ctx, u.ID, team, training, module)
	if err != nil {
		return nil, err
	}
	if inst != nil {
		if out.Lab, err = s.view(ctx, inst); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// quote is what a request for this lab would cost and who would decide it. The lobby preview and the real request
// use the same function so they never disagree. Tasks 7 and 8 add the schedule, cap and kill-switch checks here.
type quote struct {
	Timing      Timing
	HourlyUSD   float64
	EstimateUSD float64
	Tier        string
	OverCap     bool   // would pass a team or program hard cap (Task 8)
	Blocked     string // why no request can be made right now (Tasks 7, 8); "" = it can
}

func (s *Service) quote(ctx context.Context, p *config.Platform, u *auth.User, team, training, module string, lab *content.Lab) (*quote, error) {
	est := s.Estimators[lab.Runtime]
	if est == nil {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("no cost estimate is available for %s labs", lab.Runtime))
	}
	if p.Settings.CostTiers == nil { // config.Load requires them; guards hand-built states
		return nil, apperr.Wrap(apperr.Unavailable, "cost tiers are not configured")
	}
	hourly, err := est.HourlyUSD(ctx, lab)
	if errors.Is(err, apperr.Unavailable) {
		return nil, err // "estimate pending, try again shortly" reaches the trainee as is
	} else if err != nil {
		return nil, fmt.Errorf("estimating lab cost: %w", err)
	}
	q := &quote{Timing: ResolveTiming(lab, p.Teams[team].Programs[training].LabDefaults), HourlyUSD: hourly}
	q.EstimateUSD = math.Round(hourly*q.Timing.TTL.Hours()*100) / 100
	q.Tier = rbac.Tier(lab.Runtime, q.EstimateUSD, *p.Settings.CostTiers)
	if q.Tier != rbac.TierAuto {
		again, err := s.recentlyApproved(ctx, u.ID, team, training, module)
		if err != nil {
			return nil, err
		}
		if again {
			q.Tier = rbac.TierAuto // spec §14: re-request after a failed start needs no re-approval within 1 h
		} else {
			q.Tier = rbac.Checker{P: p}.Route(q.Tier, team, training, u.Email)
		}
	}
	if q.EstimateUSD > 0 { // a free lab never costs money, so a spent budget never blocks it; over-cap routes to an admin, even after a recent approval
		over, err := s.overCap(ctx, p, team, training, q.EstimateUSD)
		if err != nil {
			return nil, err
		}
		if over {
			q.OverCap, q.Tier = true, rbac.TierAdmin
		}
	}
	// Blocked precedence: kill switch, then schedule window.
	now := s.Now()
	ks, err := s.KillSwitch(ctx)
	if err != nil {
		return nil, err
	}
	if ks.Enabled {
		q.Blocked = "Labs are paused by an admin."
	} else if sc := p.ProgramSchedule(team, training); !sc.Open(now) {
		q.Blocked = "Labs for this program run " + sc.String() + "."
		if n := sc.NextOpen(now); !n.IsZero() {
			q.Blocked += " Next window opens " + n.In(sc.Location()).Format("Mon 15:04") + "."
		}
	}
	if lab.Runtime == "aws" && lab.AWS != nil && q.Blocked == "" {
		switch {
		case hourly > lab.AWS.MaxHourlyUSD:
			q.Blocked = fmt.Sprintf("This lab is priced at $%.4f/h, above its $%g/h limit; its maintainers need to make it cheaper.",
				hourly, lab.AWS.MaxHourlyUSD)
		case !slices.Contains(s.AWSRegions, lab.AWS.Region):
			q.Blocked = fmt.Sprintf("This lab runs in %s, which this server does not allow.", lab.AWS.Region)
		}
	}
	return q, nil
}

// escalateAt is when a request waiting since now moves up a tier: escalation hours counted inside the schedule.
// A schedule too sparse for AddOpen (zero) falls back to plain wall-clock time rather than a zero deadline.
func escalateAt(p *config.Platform, team, training string, now time.Time) time.Time {
	if at := p.ProgramSchedule(team, training).AddOpen(now, p.Settings.Escalation()); !at.IsZero() {
		return at
	}
	return now.Add(p.Settings.Escalation())
}

// scheduleLimit is the end of the program's open window (spec §8.6). Zero when the program runs any time; now
// when the window is already closed (a lab approved just before close must not run on overnight).
func (s *Service) scheduleLimit(inst *Instance, now time.Time) Limit {
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return Limit{}
	}
	sc := st.Platform.ProgramSchedule(inst.Team, inst.Training)
	if sc != nil && !sc.Open(now) {
		return Limit{At: now, Reason: "schedule"}
	}
	return Limit{At: sc.End(now), Reason: "schedule"}
}

func (s *Service) recentlyApproved(ctx context.Context, userID int64, team, training, module string) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_instances WHERE user_id = $1 AND team = $2 AND training = $3
		AND module = $4 AND (state = 'failed' OR (state = 'destroying' AND end_reason = 'failed')) AND decided_by <> '' AND decided_at > $5)`,
		userID, team, training, module, s.Now().Add(-time.Hour)).Scan(&ok)
	return ok, err
}

func (s *Service) insert(ctx context.Context, in *Instance) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO lab_instances (id, user_id, team, training, module, sha, runtime, state,
		created_at, last_activity_at, ttl_s, idle_timeout_s, idle_warning_s, max_extension_s,
		hourly_usd, estimate_usd, tier, over_cap, escalate_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		in.ID, in.UserID, in.Team, in.Training, in.Module, in.SHA, in.Runtime, in.State, in.CreatedAt,
		int(in.TTL.Seconds()), int(in.IdleTimeout.Seconds()), int(in.IdleWarning.Seconds()), int(in.MaxExtension.Seconds()),
		in.HourlyUSD, in.EstimateUSD, in.Tier, in.OverCap, in.EscalateAt)
	return err
}

// Start requests a lab: free labs within auto_approve_usd start at once; others wait for an approver (spec §9.1).
func (s *Service) Start(ctx context.Context, u *auth.User, team, training, module string) (*View, error) {
	st, t, sha, err := s.Learn.Program(u, team, training)
	if err != nil {
		return nil, err
	}
	m, err := s.Learn.EnsureUnlocked(ctx, u, team, t, module)
	if err != nil {
		return nil, err
	}
	if m.Lab == nil {
		return nil, apperr.Wrap(apperr.NotFound, "this module has no lab")
	}
	r := s.Runners[m.Lab.Runtime]
	if r == nil {
		return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs are not available yet", m.Lab.Runtime))
	}
	if inst, err := s.active(ctx, u.ID, team, training, module); err != nil {
		return nil, err
	} else if inst != nil && (inst.State == PendingApproval || inst.State == Provisioning || inst.State == Ready) {
		return s.view(ctx, inst)
	}
	q, err := s.quote(ctx, st.Platform, u, team, training, module, m.Lab)
	if err != nil {
		return nil, err
	}
	if q.Blocked != "" {
		return nil, apperr.Wrap(apperr.Conflict, q.Blocked)
	}
	now := s.Now()
	inst := &Instance{ID: newLabID(), UserID: u.ID, Team: team, Training: training, Module: module, SHA: sha,
		Runtime: m.Lab.Runtime, State: Provisioning, CreatedAt: now, LastActivityAt: now,
		TTL: q.Timing.TTL, IdleTimeout: q.Timing.IdleTimeout, IdleWarning: q.Timing.IdleWarning, MaxExtension: q.Timing.MaxExtension,
		HourlyUSD: q.HourlyUSD, EstimateUSD: q.EstimateUSD, Tier: q.Tier, OverCap: q.OverCap}
	if q.Tier == rbac.TierAuto {
		if err := r.Available(inst); err != nil {
			return nil, err
		}
	} else {
		inst.State = PendingApproval
		at := escalateAt(st.Platform, team, training, now)
		inst.EscalateAt = &at
	}
	err = s.insert(ctx, inst)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // a concurrent Start won the race
		existing, err := s.active(ctx, u.ID, team, training, module)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, apperr.Wrap(apperr.Conflict, "the lab is changing state; try again")
		}
		return s.view(ctx, existing)
	}
	if err != nil {
		return nil, err
	}
	s.event(ctx, inst.ID, "requested", fmt.Sprintf("%s, estimate $%.2f, tier %s", inst.Runtime, inst.EstimateUSD, inst.Tier))
	if inst.State == Provisioning {
		s.event(ctx, inst.ID, "approved", "auto")
		// ponytail: a goroutine, not a River job: creates are idempotent, the sweep fails provisioning stuck > 15 min
		// and removes orphaned lab namespaces (M4 ruling 5). Make it a job if API restarts mid-provision become common.
		go s.provision(context.WithoutCancel(ctx), inst, m.Lab)
	} else {
		s.notifyRequest(ctx, st.Platform, inst, u.Email, notify.LabPending)
	}
	return s.view(ctx, inst)
}

// runner is the one way to reach a runtime: a row can outlive its runtime (cluster labs switched off after rows exist).
func (s *Service) runner(runtime string) (Runner, error) {
	if r := s.Runners[runtime]; r != nil {
		return r, nil
	}
	return nil, apperr.Wrap(apperr.Unavailable, fmt.Sprintf("%s labs are not enabled on this server", runtime))
}

// once runs fn in the background unless a run for this lab is already in flight in this process.
func (s *Service) once(labID string, fn func()) bool {
	if _, busy := s.inflight.LoadOrStore(labID, true); busy {
		return false
	}
	go func() {
		defer s.inflight.Delete(labID)
		fn()
	}()
	return true
}

// labProvisioner is a runner that needs the lab itself, not a bundle (aws: module, region, workspace compose).
type labProvisioner interface {
	ProvisionLab(ctx context.Context, inst *Instance, lab *content.Lab) error
}

// Time limits per runtime. An aws lab runs terraform in pods with a 50-minute deadline (tfPod), so its provisioning
// and destroy get longer than that, and the sweep calls them hung or stuck only after they had their full time.
// The SQL in Sweep mirrors these.
func provisionTimeout(runtime string) time.Duration {
	if runtime == "aws" {
		return 60 * time.Minute // sweep: hung after 65
	}
	return 15 * time.Minute // sweep: hung after 15
}

// destroyTimeout bounds one destroy: terraform destroy for aws labs (the namespace delete and the tag sweep get
// their own few minutes afterwards), a namespace or a compose project otherwise.
func destroyTimeout(runtime string) time.Duration {
	if runtime == "aws" {
		return 55 * time.Minute // sweep: stuck after 70
	}
	return 2 * time.Minute // sweep: stuck after 10
}

func (s *Service) provision(ctx context.Context, inst *Instance, lab *content.Lab) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout(inst.Runtime))
	defer cancel()
	r, err := s.runner(inst.Runtime)
	if lp, ok := r.(labProvisioner); ok {
		err = lp.ProvisionLab(ctx, inst, lab) // aws: builds its own workspace bundle and terraform module
	} else if err == nil {
		var bundle []byte
		if bundle, err = Bundle(lab.Dir, lab.Runtime); err == nil {
			err = r.Provision(ctx, inst, bundle, lab.Compose)
		}
	}
	if err == nil && lab.Setup != nil {
		err = s.runSetup(ctx, inst, lab, "", lab.Setup)
	}
	if err != nil {
		s.Log.Warn("lab provisioning failed", "lab", inst.ID, "err", err)
		s.failProvision(ctx, inst, cleanText(s.runnerErr(err).Error()), cleanText(err.Error()))
		return
	}
	// Apply and setup may have used up ctx: the rest runs on a fresh one, or a lab with live resources would sit in
	// 'provisioning' until the hung sweep destroys it.
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	now := s.Now()
	bl, berr := s.budgetLimit(wctx, inst, now)
	if berr != nil { // no budget headroom (or it can't be read): don't hand out a lab that would expire at once
		s.failProvision(ctx, inst, cleanText(berr.Error()), cleanText(berr.Error()))
		return
	}
	end := EffectiveEnd(Limit{At: now.Add(inst.TTL), Reason: "ttl"}, s.scheduleLimit(inst, now), bl)
	tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'ready', ready_at = $2, ends_at = $3, limit_reason = $4,
		last_activity_at = $2 WHERE id = $1 AND state = 'provisioning'`, inst.ID, now, end.At, end.Reason)
	if err == nil && tag.RowsAffected() == 0 {
		if inst.Runtime == "aws" {
			return // ended while provisioning: that destroy interrupts the apply and runs terraform destroy itself
		}
		// the lab was ended while provisioning: nobody else will clean these containers up
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
		defer dcancel()
		_ = s.destroyRuntime(dctx, inst)
		return
	}
	s.event(wctx, inst.ID, "ready", "")
}

// failProvision cleans up after a failed provisioning and ends the row 'failed'. An aws lab shows its error at once
// as 'destroying' (terraform destroy can take most of an hour, and the hung-provisioning sweep must not take the
// failure over), then ends 'failed'. An aws lab ended meanwhile is left to the destroy that ended it, which
// interrupted the apply; a local or cluster runtime is cleaned up again, since it may have started after that destroy.
func (s *Service) failProvision(ctx context.Context, inst *Instance, errText, detail string) {
	from := Provisioning
	if inst.Runtime == "aws" {
		wctx, wcancel := finalCtx(ctx)
		tag, err := s.DB.Exec(wctx, `UPDATE lab_instances SET state = 'destroying', error = $2, end_reason = 'failed',
			destroyed_at = $3 WHERE id = $1 AND state = 'provisioning'`, inst.ID, errText, s.Now())
		if err == nil && tag.RowsAffected() == 1 {
			s.event(wctx, inst.ID, "failed", detail)
		}
		wcancel()
		if err != nil {
			s.Log.Error("lab failure not recorded", "lab", inst.ID, "err", err)
		}
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		from = Destroying
	}
	// a fresh ctx: after a provisioning timeout ctx is already expired and would leak the namespace
	dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
	_ = s.destroyRuntime(dctx, inst)
	dcancel()
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	s.endRow(wctx, inst.ID, `UPDATE lab_instances SET state = 'failed', error = $2, destroyed_at = $3
		WHERE id = $1 AND state = $4`, errText, s.Now(), string(from))
	if from == Provisioning {
		s.event(wctx, inst.ID, "failed", detail)
	}
}

func (s *Service) runScript(ctx context.Context, inst *Instance, lab *content.Lab, sc *content.Script, env map[string]string) (ScriptResult, error) {
	body, err := os.ReadFile(filepath.Join(lab.Dir, sc.Script))
	if err != nil {
		return ScriptResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, sc.Timeout.D()+15*time.Second)
	defer cancel()
	r, err := s.runner(inst.Runtime)
	if err != nil {
		return ScriptResult{}, err
	}
	res, err := r.RunScript(ctx, inst, ScriptSpec{Service: sc.RunIn, Script: body, Env: env, Timeout: sc.Timeout.D()})
	res.Output = cleanText(res.Output) // every runner: output is inserted into TEXT columns
	return res, err
}

// runSetup runs a setup script, retrying once (spec §8.5). It returns an error if the scenario could not be prepared.
func (s *Service) runSetup(ctx context.Context, inst *Instance, lab *content.Lab, taskID string, sc *content.Script) error {
	var last error
	for attempt := 1; attempt <= 2; attempt++ {
		start := s.Now()
		res, err := s.runScript(ctx, inst, lab, sc, nil)
		if err != nil {
			return s.runnerErr(err) // could not run at all: not a scenario failure
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO setup_runs (lab_id, task, attempt, exit_code, output, duration_ms, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, inst.ID, taskID, attempt, res.ExitCode, res.Output,
			s.Now().Sub(start).Milliseconds(), s.Now()); err != nil {
			s.Log.Error("recording setup run failed", "lab", inst.ID, "task", taskID, "err", err)
			return fmt.Errorf("recording setup run: %w", err)
		}
		if res.ExitCode == 0 {
			return nil
		}
		last = fmt.Errorf("setup exited with %d", res.ExitCode)
	}
	s.Log.Warn("setup script failed twice", "lab", inst.ID, "task", taskID)
	if t := s.trainingOf(ctx, inst); t != nil {
		step := "the lab-level setup"
		if taskID != "" {
			step = "the setup for task " + taskID
		}
		s.notify(ctx, notify.Event{Kind: notify.SetupFailed, To: t.Maintainers,
			Subject: fmt.Sprintf("Lab scenario failed to prepare: %s / %s", inst.Training, inst.Module),
			Text: fmt.Sprintf("%s in %s/%s exited non-zero twice (lab %s, team %s). The trainee was offered a skip. Script output is stored in setup_runs.",
				step, inst.Training, inst.Module, inst.ID, inst.Team)})
	}
	s.event(ctx, inst.ID, "setup_failed", taskID)
	return last
}

// readyTask loads a lab the user owns, requires it to be ready, and resolves the task.
func (s *Service) readyTask(ctx context.Context, u *auth.User, labID, taskID string) (*Instance, *content.Lab, *content.Quiz, *content.Task, map[string]string, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if inst.State != Ready {
		return nil, nil, nil, nil, nil, apperr.Wrap(apperr.Conflict, "the lab is not ready")
	}
	lab, quiz, err := s.labContent(ctx, inst)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	task := lab.Task(taskID)
	if task == nil {
		return nil, nil, nil, nil, nil, apperr.Wrap(apperr.NotFound, "task not found")
	}
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	setups, err := s.setupStatus(ctx, inst.ID)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	statuses := taskStatuses(lab, done, setups, reviews)
	statuses["_setup:"+taskID] = setups[taskID]
	return inst, lab, quiz, task, statuses, nil
}

func (s *Service) instByID(ctx context.Context, labID string) (*Instance, error) {
	return scanInst(s.DB.QueryRow(ctx, `SELECT `+instCols+` FROM lab_instances WHERE id = $1`, labID))
}

// reviews returns the latest review submission per task of this trainee's module.
func (s *Service) reviews(ctx context.Context, inst *Instance) (map[string]*scoring.Submission, error) {
	if s.Scoring == nil {
		return nil, nil
	}
	return s.Scoring.Latest(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, scoring.KindTask)
}

func (s *Service) Get(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, inst)
}

func (s *Service) OpenTask(ctx context.Context, u *auth.User, labID, taskID string) (*TaskDetail, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] == "locked" {
		return nil, apperr.Wrap(apperr.Locked, "finish the earlier tasks first")
	}
	const prepFailed = "This scenario couldn't be prepared. You can skip this task without penalty, or restart the lab."
	setupErr := ""
	if statuses[taskID] == "open" && task.Setup != nil && statuses["_setup:"+taskID] == "" {
		mu := s.setupLock(inst.ID)
		mu.Lock()
		setups, err := s.setupStatus(ctx, inst.ID) // re-check: another request may have run it meanwhile
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		switch setups[taskID] {
		case "":
			err = s.runSetup(ctx, inst, lab, taskID, task.Setup)
		case "failed":
			setupErr = prepFailed
		}
		mu.Unlock()
		if errors.Is(err, apperr.Unavailable) {
			return nil, err
		}
		if err != nil {
			setupErr = prepFailed
		}
	}
	if statuses[taskID] == "setup_failed" {
		setupErr = prepFailed
	}
	s.Touch(ctx, inst.ID)
	v, err := s.view(ctx, inst)
	if err != nil {
		return nil, err
	}
	d := &TaskDetail{SetupError: setupErr}
	for _, tv := range v.Tasks {
		if tv.ID == taskID {
			d.TaskView = tv
		}
	}
	b, err := os.ReadFile(filepath.Join(lab.Dir, task.Instructions))
	if err != nil {
		return nil, err
	}
	d.Instructions = string(b)
	for i := 0; i < d.HintsRevealed && i < len(task.Hints); i++ {
		d.Hints = append(d.Hints, hintText(lab, task.Hints[i]))
	}
	return d, nil
}

func hintText(lab *content.Lab, h *content.Hint) string {
	if h.File == "" {
		return h.Text
	}
	b, _ := os.ReadFile(filepath.Join(lab.Dir, h.File))
	return string(b)
}

const maxAnswer = 4 << 10

func (s *Service) Check(ctx context.Context, u *auth.User, labID, taskID, answer string) (*CheckResult, error) {
	if len(answer) > maxAnswer {
		return nil, apperr.Wrap(apperr.Invalid, "the answer is too long (4 KiB at most)")
	}
	answer = cleanText(answer) // stored in TEXT and passed as an env var
	inst, lab, quiz, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	switch statuses[taskID] {
	case "passed", "skipped":
		v, err := s.view(ctx, inst)
		return &CheckResult{Passed: statuses[taskID] == "passed", Lab: v}, err
	case "locked":
		return nil, apperr.Wrap(apperr.Locked, "finish the earlier tasks first")
	case "setup_failed":
		return nil, apperr.Wrap(apperr.Conflict, "this scenario couldn't be prepared; skip the task instead")
	}
	if task.Setup != nil && statuses["_setup:"+taskID] != "ok" {
		return nil, apperr.Wrap(apperr.Conflict, "the scenario is still being prepared")
	}
	var sc *content.Script
	env := map[string]string{}
	switch {
	case task.Quiz != "":
		sc = quiz.Question(task.Quiz).Script
		env["CRUCIBLE_ANSWER"] = answer
	case task.Check != nil:
		sc = task.Check
	default:
		return nil, apperr.Wrap(apperr.Conflict, "this task is reviewed by a scorer")
	}
	res, err := s.runScript(ctx, inst, lab, sc, env)
	if err != nil {
		return nil, s.runnerErr(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO check_runs (lab_id, task, exit_code, output, answer, self_reported)
		VALUES ($1, $2, $3, $4, $5, $6)`, inst.ID, taskID, res.ExitCode, res.Output, answer, inst.Runtime == "local"); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	out := &CheckResult{Passed: res.ExitCode == 0, Output: res.Output, TimedOut: res.TimedOut}
	if out.Passed {
		_, costs, err := s.hintCounts(ctx, inst)
		if err != nil {
			return nil, err
		}
		out.Awarded = max(0, task.Points-costs[taskID])
		if err := s.finishTask(ctx, inst, lab, taskID, "passed", out.Awarded); err != nil {
			return nil, err
		}
	}
	out.Lab, err = s.view(ctx, inst)
	return out, err
}

func (s *Service) finishTask(ctx context.Context, inst *Instance, lab *content.Lab, taskID, status string, points float64) error {
	if _, err := s.DB.Exec(ctx, `INSERT INTO lab_task_progress (user_id, team, training, module, task, status, points)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, status, points); err != nil {
		return err
	}
	return s.recompute(ctx, inst, lab)
}

// recompute sets the module's lab item from the task results: complete (with its exact score) when every task is
// done, pending_review when only scorers' decisions are missing (spec §7: progression waits on pending human scores),
// in_progress otherwise. It is idempotent: Refresh, overrides and page loads (settle) all end here.
func (s *Service) recompute(ctx context.Context, inst *Instance, lab *content.Lab) error {
	done, err := s.taskRows(ctx, inst)
	if err != nil {
		return err
	}
	reviews, err := s.reviews(ctx, inst)
	if err != nil {
		return err
	}
	var score, maxScore float64
	waiting, missing := false, false
	for _, t := range lab.Tasks {
		r, ok := done[t.ID]
		if !ok {
			if sub := reviews[t.ID]; sub != nil && sub.Status == scoring.Pending {
				waiting = true
			} else {
				missing = true
			}
			continue
		}
		score += r.Points
		if r.Status != "skipped" {
			maxScore += t.Points
		}
	}
	switch {
	case missing: // SetItem never downgrades a complete item
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "in_progress", 0)
	case waiting:
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "pending_review", 0)
	}
	wait, decided, err := s.selfReportReview(ctx, inst, lab, maxScore, done)
	if err != nil {
		return err
	}
	if wait {
		return s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "pending_review", 0)
	}
	if decided != nil {
		score = *decided // the scorer's points replace the self-reported total
	}
	if maxScore == 0 {
		maxScore = 1 // everything skipped
	}
	s.event(ctx, inst.ID, "completed", fmt.Sprintf("%.2f/%.2f", score, maxScore))
	if err := s.Learn.SetItem(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", "complete", score/maxScore); err != nil {
		return err
	}
	return s.Learn.ForceScore(ctx, inst.UserID, inst.Team, inst.Training, inst.Module, "lab", score/maxScore) // overrides may lower it
}

func (s *Service) RevealHint(ctx context.Context, u *auth.User, labID, taskID string) (*HintResult, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] != "open" {
		return nil, apperr.Wrap(apperr.Conflict, "hints are available for the current task only")
	}
	counts, _, err := s.hintCounts(ctx, inst)
	if err != nil {
		return nil, err
	}
	n := counts[taskID]
	if n >= len(task.Hints) {
		return nil, apperr.Wrap(apperr.Conflict, "no more hints for this task")
	}
	h := task.Hints[n]
	cost := h.EffectiveCost(lab)
	if _, err := s.DB.Exec(ctx, `INSERT INTO hint_reveals (user_id, team, training, module, task, hint_index, cost, lab_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		inst.UserID, inst.Team, inst.Training, inst.Module, taskID, n, cost, inst.ID); err != nil {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	v, err := s.view(ctx, inst)
	return &HintResult{Index: n, Text: hintText(lab, h), Cost: cost, Lab: v}, err
}

func (s *Service) ResetTask(ctx context.Context, u *auth.User, labID, taskID string) (*View, error) {
	inst, lab, _, task, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if task.Setup == nil {
		return nil, apperr.Wrap(apperr.Conflict, "this task has no scenario to reset")
	}
	if st := statuses[taskID]; st == "passed" || st == "skipped" || st == "locked" || st == "submitted" {
		return nil, apperr.Wrap(apperr.Conflict, "only the current task can be reset")
	}
	mu := s.setupLock(inst.ID)
	mu.Lock()
	defer mu.Unlock()
	var last time.Time
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(max(at), 'epoch') FROM setup_runs WHERE lab_id = $1 AND task = $2`,
		inst.ID, taskID).Scan(&last); err != nil {
		return nil, err
	}
	if wait := 5*time.Minute - s.Now().Sub(last); wait > 0 {
		return nil, apperr.Wrap(apperr.Conflict, fmt.Sprintf("you can reset this scenario again in %s", wait.Round(time.Second)))
	}
	if err := s.runSetup(ctx, inst, lab, taskID, task.Setup); errors.Is(err, apperr.Unavailable) {
		return nil, err
	}
	s.Touch(ctx, inst.ID)
	return s.view(ctx, inst)
}

func (s *Service) Skip(ctx context.Context, u *auth.User, labID, taskID string) (*View, error) {
	inst, lab, _, _, statuses, err := s.readyTask(ctx, u, labID, taskID)
	if err != nil {
		return nil, err
	}
	if statuses[taskID] != "setup_failed" {
		return nil, apperr.Wrap(apperr.Conflict, "only tasks whose scenario failed can be skipped")
	}
	if err := s.finishTask(ctx, inst, lab, taskID, "skipped", 0); err != nil {
		return nil, err
	}
	return s.view(ctx, inst)
}

// Touch records trainee activity, at most every 30 seconds per lab.
func (s *Service) Touch(ctx context.Context, labID string) {
	now := s.Now()
	s.touchMu.Lock()
	if s.touched == nil {
		s.touched = map[string]time.Time{}
	}
	if now.Sub(s.touched[labID]) < 30*time.Second {
		s.touchMu.Unlock()
		return
	}
	s.touched[labID] = now
	s.touchMu.Unlock()
	_, _ = s.DB.Exec(ctx, `UPDATE lab_instances SET last_activity_at = $2 WHERE id = $1 AND state = 'ready'`, labID, now)
}

// Activity is the explicit "I'm here" from the idle prompt; it always writes.
func (s *Service) Activity(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	s.touchMu.Lock()
	delete(s.touched, labID)
	s.touchMu.Unlock()
	s.Touch(ctx, labID)
	return s.Get(ctx, u, inst.ID)
}

func (s *Service) Extend(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State != Ready || inst.Extended || inst.MaxExtension == 0 || inst.EndsAt == nil {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	if inst.LimitReason == "budget" {
		return nil, apperr.Wrap(apperr.Conflict, "this lab ends at the budget cap; it can't be extended")
	}
	end, reason := inst.EndsAt.Add(inst.MaxExtension), "ttl"
	if lim := s.scheduleLimit(inst, s.Now()); !lim.At.IsZero() && lim.At.Before(end) {
		end, reason = lim.At, lim.Reason
	}
	if !end.After(*inst.EndsAt) {
		return nil, apperr.Wrap(apperr.Conflict, "the schedule window closes first; this lab can't be extended")
	}
	if st := s.Learn.State(); inst.HourlyUSD > 0 && st != nil && st.Platform != nil && st.Platform.Settings.CostTiers != nil {
		tiers := *st.Platform.Settings.CostTiers
		more := inst.EstimateUSD + inst.HourlyUSD*end.Sub(*inst.EndsAt).Hours()
		if rbac.Tier(inst.Runtime, more, tiers) != rbac.Tier(inst.Runtime, inst.EstimateUSD, tiers) {
			return s.requestExtension(ctx, u, st.Platform, inst, end, more)
		}
	}
	if st := s.Learn.State(); inst.HourlyUSD > 0 && st != nil && st.Platform != nil {
		extra := inst.HourlyUSD * end.Sub(*inst.EndsAt).Hours()
		if over, err := s.overCap(ctx, st.Platform, inst.Team, inst.Training, extra); err != nil {
			return nil, err
		} else if over {
			return nil, apperr.Wrap(apperr.Conflict, "this extension would pass the budget hard cap; ask an admin")
		}
	}
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET ends_at = $2, limit_reason = $3, extended = true, last_activity_at = $4
		WHERE id = $1 AND NOT extended AND state = 'ready'`, inst.ID, end, reason, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	s.event(ctx, inst.ID, "extended", end.Sub(*inst.EndsAt).String())
	return s.Get(ctx, u, labID)
}

// requestExtension sends an extension that lifts the estimate into a higher tier back through approval (spec §8.6).
// It uses up the lab's one extension, even if rejected; the timer shows "Extension pending" until someone decides.
// ponytail: extension requests do not escalate; the lab, and the request with it, ends within hours.
func (s *Service) requestExtension(ctx context.Context, u *auth.User, p *config.Platform, inst *Instance, until time.Time, estimate float64) (*View, error) {
	c := rbac.Checker{P: p}
	tier := rbac.Tier(inst.Runtime, estimate, *p.Settings.CostTiers)
	if over, err := s.overCap(ctx, p, inst.Team, inst.Training, estimate-inst.EstimateUSD); err != nil {
		return nil, err
	} else if over {
		tier = rbac.TierAdmin
	}
	tier = c.Route(tier, inst.Team, inst.Training, u.Email)
	now := s.Now()
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET ext_until = $2, ext_estimate_usd = $3, ext_tier = $4, ext_requested_at = $5,
		extended = true, last_activity_at = $5 WHERE id = $1 AND NOT extended AND state = 'ready'`, inst.ID, until, estimate, tier, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperr.Wrap(apperr.Conflict, "this lab can't be extended further")
	}
	s.event(ctx, inst.ID, "extension_requested", fmt.Sprintf("until %s, estimate %.2f USD, tier %s", until.UTC().Format(time.RFC3339), estimate, tier))
	s.notify(ctx, notify.Event{Kind: notify.LabPending, To: c.TierApprovers(tier, inst.Team, inst.Training, u.Email), Team: inst.Team,
		Subject: fmt.Sprintf("Lab extension from %s (%s, est. $%.2f)", strings.ToLower(u.Email), inst.Training, estimate),
		Text: fmt.Sprintf("%s asks to extend the %s lab in %s/%s until %s, which lifts its estimate to $%.2f. It is waiting for approval.",
			strings.ToLower(u.Email), inst.Module, inst.Team, inst.Training, until.UTC().Format("15:04 UTC"), estimate),
		Link: "/approvals"})
	return s.Get(ctx, u, inst.ID)
}

func (s *Service) End(ctx context.Context, u *auth.User, labID string) (*View, error) {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return nil, err
	}
	if inst.State == PendingApproval {
		return s.withdraw(ctx, u, inst)
	}
	s.destroy(ctx, inst, "user")
	return s.Get(ctx, u, labID)
}

// withdraw cancels a pending request. If an approval won the race the row is no longer pending: re-read it and end the
// lab it became like any other.
func (s *Service) withdraw(ctx context.Context, u *auth.User, inst *Instance) (*View, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'expired', end_reason = 'withdrawn', destroyed_at = $2
		WHERE id = $1 AND state = 'pending_approval'`, inst.ID, s.Now())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() > 0 {
		s.event(ctx, inst.ID, "withdrawn", "")
		return s.Get(ctx, u, inst.ID)
	}
	if inst, err = s.owned(ctx, u, inst.ID); err != nil {
		return nil, err
	}
	s.destroy(ctx, inst, "user")
	return s.Get(ctx, u, inst.ID)
}

func (s *Service) destroy(ctx context.Context, inst *Instance, reason string) {
	// the request may be cancelled mid-way; a half-finished destroy would wedge the lab in 'destroying'
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
	// destroyed_at doubles as "destroying since" until the final update (see Sweep)
	tag, err := s.DB.Exec(ctx, `UPDATE lab_instances SET state = 'destroying', end_reason = $2, destroyed_at = $3
		WHERE id = $1 AND state IN ('provisioning', 'ready')`, inst.ID, reason, s.Now())
	if err != nil || tag.RowsAffected() == 0 {
		cancel()
		return
	}
	if inst.Runtime == "aws" { // terraform destroy takes minutes: End, the sweep and the kill switch must not wait
		if !s.once(inst.ID, func() { defer cancel(); s.finishDestroy(ctx, inst, reason) }) {
			cancel()
		}
		return
	}
	defer cancel()
	s.finishDestroy(ctx, inst, reason)
}

// destroyRuntime tears a lab's environment down. For aws labs, terraform destroy is followed by a tag sweep (spec
// §8.2) with its own few minutes: terraform may have used up ctx.
func (s *Service) destroyRuntime(ctx context.Context, inst *Instance) error {
	r, err := s.runner(inst.Runtime)
	if err != nil {
		return err
	}
	err = r.Destroy(ctx, inst)
	if inst.Runtime == "aws" {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		s.sweepLab(sctx, inst.ID)
	}
	return err
}

func (s *Service) finishDestroy(ctx context.Context, inst *Instance, reason string) {
	note := ""
	if err := s.destroyRuntime(ctx, inst); err != nil {
		note = "cleanup failed: " + err.Error()
		if errors.Is(err, apperr.Unavailable) {
			note = "cleanup skipped: " + err.Error() + "; delete the lab namespace by hand"
		} else if errors.Is(err, agenthub.ErrOffline) {
			note = "agent offline; its containers are removed when the agent next starts or stops"
		}
		s.Log.Warn("lab destroy incomplete", "lab", inst.ID, "err", err)
	}
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	s.endRow(wctx, inst.ID, `UPDATE lab_instances SET state = 'destroyed', destroyed_at = $2, error = $3 WHERE id = $1`,
		s.Now(), note)
	s.setupMu.Lock()
	delete(s.setupLocks, inst.ID)
	s.setupMu.Unlock()
	s.event(wctx, inst.ID, "destroyed", reason)
}

// Sweep destroys labs past their end time or idle deadline, and provisioning that hung.
func (s *Service) Sweep(ctx context.Context) {
	if !s.sweepMu.TryLock() {
		return // a slow sweep is still running; the next periodic job picks up whatever it missed
	}
	defer s.sweepMu.Unlock()
	now := s.Now()
	paused := false
	if ks, err := s.KillSwitch(ctx); err == nil && ks.Enabled {
		s.killAll(ctx)
		paused = true // escalation and expiry of pending requests wait until labs are re-enabled
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE (state = 'ready' AND (ends_at <= $1 OR last_activity_at + idle_timeout_s * interval '1 second' <= $1))
		   OR (state = 'provisioning' AND coalesce(decided_at, created_at) < $1 -
		       CASE WHEN runtime = 'aws' THEN interval '65 minutes' ELSE interval '15 minutes' END)
		   OR (state = 'destroying' AND destroyed_at < $1 -
		       CASE WHEN runtime = 'aws' THEN interval '70 minutes' ELSE interval '10 minutes' END)
		   OR (state = 'pending_approval' AND escalate_at <= $1)`, now)
	if err != nil {
		s.Log.Error("lab sweep query failed", "err", err)
		return
	}
	var due []*Instance
	for rows.Next() {
		inst, err := scanInst(rows)
		if err == nil {
			due = append(due, inst)
		}
	}
	rows.Close()
	for _, inst := range due {
		reason := "idle"
		switch {
		case inst.State == PendingApproval:
			if !paused {
				s.escalate(ctx, inst)
			}
			continue
		case inst.State == "destroying": // a destroy that never finished: one more attempt, then give up
			s.alertStuck(ctx, inst)
			if inst.Runtime == "aws" {
				s.once(inst.ID, func() { s.retryDestroy(context.WithoutCancel(ctx), inst) }) // never hold up the sweep
			} else {
				s.retryDestroy(ctx, inst)
			}
			continue
		case inst.State == Provisioning:
			reason = "provision_timeout"
		case inst.EndsAt != nil && !inst.EndsAt.After(now):
			reason = inst.LimitReason // ttl or schedule
			if reason == "" {
				reason = "ttl"
			}
		}
		s.destroy(ctx, inst, reason)
	}
	s.reconcileCluster(ctx)
	s.refreshAWS(ctx)
}

// alertStuck tells the admins, once per lab, that a destroy never finished (spec §8.1). The conditional UPDATE
// claims the alert atomically, so concurrent sweeps (or API replicas) send it once. No resource ids beyond the lab id.
func (s *Service) alertStuck(ctx context.Context, inst *Instance) {
	var since time.Time
	if err := s.DB.QueryRow(ctx, `UPDATE lab_instances SET stuck_alerted_at = $2 WHERE id = $1 AND stuck_alerted_at IS NULL
		RETURNING coalesce(destroyed_at, created_at)`, inst.ID, s.Now()).Scan(&since); err != nil {
		return // already alerted, or the row is gone
	}
	st := s.Learn.State()
	if st == nil || st.Platform == nil {
		return
	}
	s.notify(ctx, notify.Event{Kind: notify.LabStuck, To: st.Platform.Admins, Subject: "A lab is stuck while being destroyed",
		Text: fmt.Sprintf("Lab %s (%s, %s/%s, %s) has been destroying since %s. Crucible keeps retrying; see Forge Status.",
			inst.ID, inst.Runtime, inst.Team, inst.Training, inst.Module, since.UTC().Format(time.RFC3339)), Link: "/admin"})
}

// retryDestroy is the last attempt for a destroy that never finished; the row ends 'destroyed' either way.
func (s *Service) retryDestroy(ctx context.Context, inst *Instance) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout(inst.Runtime))
	defer cancel()
	err := s.destroyRuntime(ctx, inst)
	note := map[bool]string{true: "cleanup timed out", false: ""}[err != nil]
	if errors.Is(err, apperr.Unavailable) {
		note = "cleanup skipped: " + err.Error() + "; delete the lab namespace by hand"
	}
	wctx, wcancel := finalCtx(ctx)
	defer wcancel()
	// a failed provision (failProvision's destroy, cut short by a restart) stays 'failed' with its own error
	s.endRow(wctx, inst.ID, `UPDATE lab_instances SET destroyed_at = $2,
		state = CASE WHEN end_reason = 'failed' THEN 'failed' ELSE 'destroyed' END,
		error = CASE WHEN end_reason = 'failed' THEN concat_ws('; ', nullif(error, ''), nullif($3, '')) ELSE $3 END
		WHERE id = $1 AND state = 'destroying'`, s.Now(), note)
}

// reconcileCluster deletes lab namespaces whose lab is over or unknown: a destroy that failed, a lab ended while
// the API restarted mid-provision, or a leftover from a deleted database. Namespaces of labs that are provisioning,
// ready or being destroyed are never touched.
func (s *Service) reconcileCluster(ctx context.Context) {
	cr, ok := s.Runners["cluster"].(*ClusterRunner)
	if !ok {
		return
	}
	ids, err := cr.Live(ctx)
	if err != nil {
		s.Log.Warn("listing lab namespaces failed", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM lab_instances WHERE id = ANY($1) AND state IN ('provisioning', 'ready', 'destroying')`, ids)
	if err != nil {
		s.Log.Error("lab namespace reconcile query failed", "err", err)
		return
	}
	active, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.Log.Error("lab namespace reconcile query failed", "err", err)
		return
	}
	for _, id := range ids {
		if slices.Contains(active, id) {
			continue
		}
		if err := cr.Destroy(ctx, &Instance{ID: id, Runtime: "cluster"}); err != nil {
			s.Log.Warn("removing an orphaned lab namespace failed", "lab", id, "err", err)
			continue
		}
		s.Log.Info("removed an orphaned lab namespace", "lab", id)
	}
}

// ReconcileAgent runs when a user's laptop agent connects and reports the labs it has (agentproto.THello).
// Labs the server thinks are running but the laptop lost (the agent removes all labs when it starts) end with
// "agent_restarted"; labs on the laptop the server no longer runs are destroyed there.
// ponytail: a lab started in the instant between connect and this call could be ended too; the window is one round trip.
func (s *Service) ReconcileAgent(ctx context.Context, userID int64, liveIDs []string) {
	live := map[string]bool{}
	for _, id := range liveIDs {
		live[id] = true
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances
		WHERE user_id = $1 AND runtime = 'local' AND state IN ('provisioning', 'ready')`, userID)
	if err != nil {
		s.Log.Error("agent reconcile query failed", "user", userID, "err", err)
		return
	}
	var lost []*Instance
	active := map[string]bool{}
	for rows.Next() {
		inst, err := scanInst(rows)
		if err != nil {
			continue
		}
		active[inst.ID] = true
		if !live[inst.ID] {
			lost = append(lost, inst)
		}
	}
	rows.Close()
	for _, inst := range lost {
		s.destroy(ctx, inst, "agent_restarted")
	}
	for id := range live {
		if !active[id] {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			if err := s.destroyRuntime(dctx, &Instance{ID: id, UserID: userID, Runtime: "local"}); err != nil {
				s.Log.Warn("removing a stale lab from the laptop failed", "lab", id, "err", err)
			}
			cancel()
		}
	}
}
