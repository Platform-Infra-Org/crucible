package labs

import (
	"context"
	"time"
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
