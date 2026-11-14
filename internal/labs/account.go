package labs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"crucible/internal/apperr"
	"crucible/internal/audit"
	"crucible/internal/auth"
	"crucible/internal/rbac"
)

const (
	accountReadEvery    = 10 * time.Minute // the periodic job (an AWS Budget read is free)
	ceReadEvery         = time.Hour        // Cost Explorer costs $0.01 a call
	accountRefreshEvery = 5 * time.Minute  // Refresh now, shared by every admin
	accountStaleAfter   = 36 * time.Hour   // past it, the limit fails closed
)

// AccountBill is the account's month-to-date bill as last read (aws_ops).
type AccountBill struct {
	USD                          float64
	ReadAt, AttemptAt, RefreshAt *time.Time
	Error                        string
}

// Stale: never read successfully, or not for accountStaleAfter. With a limit set, a stale bill sends paid requests to an admin.
func (b AccountBill) Stale(now time.Time) bool {
	return b.ReadAt == nil || now.Sub(*b.ReadAt) > accountStaleAfter
}

func (s *Service) accountBill(ctx context.Context) (AccountBill, error) {
	var b AccountBill
	var usd *float64
	err := s.DB.QueryRow(ctx, `SELECT account_usd, account_read_at, account_attempt_at, account_refresh_at, account_error FROM aws_ops`).
		Scan(&usd, &b.ReadAt, &b.AttemptAt, &b.RefreshAt, &b.Error)
	if usd != nil {
		b.USD = *usd
	}
	return b, err
}

// ReadAccountSpend reads the account's bill into aws_ops. The periodic run reads Cost Explorer at most hourly; a manual
// one (Refresh now, already past claimAccountRefresh) always reads. A failure keeps the last good figure.
func (s *Service) ReadAccountSpend(ctx context.Context, manual bool) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now()
	if !manual && s.SpendSource == "cost_explorer" {
		b, err := s.accountBill(ctx)
		if err != nil {
			return err
		}
		if b.AttemptAt != nil && now.Sub(*b.AttemptAt) < ceReadEvery {
			return nil
		}
	}
	usd, err := s.Cloud.AccountSpend(ctx)
	if err != nil {
		s.Log.Warn("reading the account's bill failed", "err", err)
		wctx, cancel := finalCtx(ctx)
		defer cancel()
		_, err = s.DB.Exec(wctx, `UPDATE aws_ops SET account_attempt_at = $1, account_error = $2, account_error_at = $1`, now, cleanText(err.Error()))
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE aws_ops SET account_usd = $1, account_read_at = $2, account_attempt_at = $2, account_error = ''`, usd, now)
	return err
}

// claimAccountRefresh takes the shared 5-minute refresh slot atomically; wait is how long until the next one when it was taken.
func (s *Service) claimAccountRefresh(ctx context.Context) (ok bool, wait time.Duration, err error) {
	now := s.Now()
	tag, err := s.DB.Exec(ctx, `UPDATE aws_ops SET account_refresh_at = $1::timestamptz
		WHERE account_refresh_at IS NULL OR account_refresh_at <= $1::timestamptz - make_interval(secs => $2::float8)`, now, accountRefreshEvery.Seconds())
	if err != nil || tag.RowsAffected() == 1 {
		return err == nil, 0, err
	}
	b, err := s.accountBill(ctx)
	if err != nil || b.RefreshAt == nil {
		return false, accountRefreshEvery, err
	}
	return false, b.RefreshAt.Add(accountRefreshEvery).Sub(now), nil
}

const (
	unbilledWindow = 24 * time.Hour   // Crucible's own meter covers this much behind AWS's figure
	markKeep       = 48 * time.Hour   // the sweep drops older marks
	markSkew       = 2 * time.Minute  // a browser clock this far off is clamped, not refused
	markStep       = 15 * time.Minute // the page snaps and steps marks by this much
)

type UsageInterval struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	HourlyUSD float64   `json:"hourly_usd"`
}

type UnbilledLab struct {
	ID          string          `json:"id"`
	Team        string          `json:"team"`
	Training    string          `json:"training"`
	Module      string          `json:"module"`
	Requester   string          `json:"requester"`
	HourlyUSD   float64         `json:"hourly_usd"`
	Intervals   []UsageInterval `json:"intervals"`
	OwnMark     *time.Time      `json:"own_mark,omitempty"`
	UnbilledUSD float64         `json:"unbilled_usd"`
}

type UnbilledWindow struct {
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	SleptFrom   *time.Time    `json:"slept_from,omitempty"`
	SleptTo     *time.Time    `json:"slept_to,omitempty"`
	AccountMark *time.Time    `json:"account_mark,omitempty"`
	Labs        []UnbilledLab `json:"labs"`
	USD         float64       `json:"unbilled_usd"`
}

// unbilled is Crucible's meter over the last 24 hours, after each lab's cut: its own mark, else the account mark, else
// the window's start (never earlier). Labs are ordered by their first usage in the window.
func (s *Service) unbilled(ctx context.Context) (UnbilledWindow, error) {
	now := s.Now()
	w := UnbilledWindow{From: now.Add(-unbilledWindow), To: now, Labs: []UnbilledLab{}}
	if err := s.DB.QueryRow(ctx, `SELECT forge_slept_at, forge_woke_at FROM aws_ops`).Scan(&w.SleptFrom, &w.SleptTo); err != nil {
		return w, err
	}
	if w.SleptFrom != nil && (w.SleptTo == nil || w.SleptTo.Before(w.From) || w.SleptTo.Before(*w.SleptFrom)) {
		w.SleptFrom, w.SleptTo = nil, nil // no night in this window
	}
	if err := s.DB.QueryRow(ctx, `SELECT billed_to FROM billed_marks WHERE lab_id = ''`).Scan(&w.AccountMark); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	rows, err := s.DB.Query(ctx, `SELECT r.lab_id, l.team, l.training, l.module, u.email, l.hourly_usd,
			greatest(r.started_at, $2), least(coalesce(r.ended_at, $1), $1), r.hourly_usd + r.usage_hourly_usd, m.billed_to
		FROM lab_resources r JOIN lab_instances l ON l.id = r.lab_id JOIN users u ON u.id = l.user_id
		LEFT JOIN billed_marks m ON m.lab_id = r.lab_id
		WHERE coalesce(r.ended_at, $1) > $2 AND r.started_at < $1
		ORDER BY r.lab_id, r.started_at`, now, w.From)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var l UnbilledLab
		var iv UsageInterval
		if err := rows.Scan(&l.ID, &l.Team, &l.Training, &l.Module, &l.Requester, &l.HourlyUSD, &iv.From, &iv.To, &iv.HourlyUSD, &l.OwnMark); err != nil {
			return w, err
		}
		i, ok := idx[l.ID]
		if !ok {
			i, idx[l.ID] = len(w.Labs), len(w.Labs)
			w.Labs = append(w.Labs, l)
		}
		w.Labs[i].Intervals = append(w.Labs[i].Intervals, iv)
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	for i := range w.Labs {
		l := &w.Labs[i]
		cut := w.From
		if l.OwnMark != nil {
			cut = maxTime(cut, *l.OwnMark)
		} else if w.AccountMark != nil {
			cut = maxTime(cut, *w.AccountMark)
		}
		for _, iv := range l.Intervals {
			if from := maxTime(iv.From, cut); iv.To.After(from) {
				l.UnbilledUSD += iv.HourlyUSD * iv.To.Sub(from).Hours()
			}
		}
		w.USD += l.UnbilledUSD
	}
	return w, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

type MarkBody struct {
	LabID    string     `json:"lab_id"`
	BilledTo *time.Time `json:"billed_to"`
	Reset    bool       `json:"reset"`
}

// SetBilledMark sets or removes one "AWS has billed this up to" mark (lab_id "" is the account's), or all of them
// (reset). Admins only, audited; the reply is the account block as the server now sees it.
func (s *Service) SetBilledMark(ctx context.Context, u *auth.User, b MarkBody) (*AccountBlock, error) {
	st, err := s.platform()
	if err != nil {
		return nil, err
	}
	if !(rbac.Checker{P: st.Platform}).IsAdmin(u.Email) {
		return nil, apperr.Wrap(apperr.Forbidden, "only admins can mark what AWS has billed")
	}
	now := s.Now()
	actor := strings.ToLower(u.Email)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	detail := map[string]any{"lab_id": b.LabID, "reset": b.Reset}
	switch {
	case b.Reset:
		_, err = tx.Exec(ctx, `DELETE FROM billed_marks`)
	case b.BilledTo == nil:
		_, err = tx.Exec(ctx, `DELETE FROM billed_marks WHERE lab_id = $1`, b.LabID)
	default:
		at := b.BilledTo.UTC()
		lo := now.Add(-unbilledWindow)
		if at.Before(lo.Add(-markSkew)) || at.After(now.Add(markSkew)) {
			return nil, apperr.Wrap(apperr.Invalid, "Pick a time in the last 24 hours")
		}
		at = minTime(maxTime(at, lo), now) // a browser clock a little off is clamped to the window
		if b.LabID != "" {
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_resources WHERE lab_id = $1 AND coalesce(ended_at, $2) > $3)`,
				b.LabID, now, now.Add(-unbilledWindow)).Scan(&used); err != nil {
				return nil, err
			}
			if !used {
				return nil, apperr.Wrap(apperr.Invalid, "this lab used nothing in the last 24 hours")
			}
		}
		detail["billed_to"] = at
		_, err = tx.Exec(ctx, `INSERT INTO billed_marks (lab_id, billed_to, set_by, set_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (lab_id) DO UPDATE SET billed_to = excluded.billed_to, set_by = excluded.set_by, set_at = excluded.set_at`,
			b.LabID, at, actor, now)
	}
	if err != nil {
		return nil, err
	}
	if err := audit.Log(ctx, tx, actor, "account.billed_mark", b.LabID, detail, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.accountBlock(ctx, u)
}

// AccountBlock is the Ledger's account line (Task 7 adds the bill, the limit and the rest).
type AccountBlock struct {
	Window UnbilledWindow `json:"window"`
}

func (s *Service) accountBlock(ctx context.Context, _ *auth.User) (*AccountBlock, error) {
	w, err := s.unbilled(ctx)
	if err != nil {
		return nil, err
	}
	return &AccountBlock{Window: w}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
