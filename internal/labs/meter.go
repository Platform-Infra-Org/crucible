package labs

import (
	"context"
	"errors"
	"time"

	"crucible/internal/content"
	"crucible/internal/pricing"
)

// Meter records what a Terraform lab's state holds now (spec 2026-10-12 §3): new priced resources start now, ones
// gone from the state end now. Spend uses these rows in place of the estimate until billed actuals settle. A lab that
// is no longer provisioning or ready is left alone: its destroy ends the meter, and a late Meter must not reopen it.
func (s *Service) Meter(ctx context.Context, inst *Instance, lab *content.Lab) error {
	ar, ok := s.Runners["aws"].(*AWSRunner)
	if !ok || lab.AWS == nil || s.Prices == nil {
		return errors.New("metering needs the aws runner, an aws lab and list prices")
	}
	raw, err := ar.cloud().ReadState(ctx, ar.StateRegion, ar.StateBucket, "labs/"+inst.ID+".tfstate")
	if err != nil {
		return err
	}
	res, err := pricing.FromState(raw)
	if err != nil {
		return err
	}
	snap, err := s.Prices.Snapshot(ctx)
	if err != nil {
		return err
	}
	b, err := pricing.PriceAWS(res, lab.AWS.Region, lab.AWS.Prices, lab.AWS.Usage, snap)
	if err != nil {
		var u *pricing.Unpriced
		if !errors.As(err, &u) {
			return err
		}
		// ponytail: lint and the estimate already refuse unpriced types; anything else here is metered at $0 and logged
		s.Log.Warn("metering found unpriced resources", "lab", inst.ID, "types", u.Types)
	}
	now := s.Now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var state State
	// FOR SHARE: a destroy's state change waits for this commit, so its EndMeter sees every row written here
	if err := tx.QueryRow(ctx, `SELECT state FROM lab_instances WHERE id = $1 FOR SHARE`, inst.ID).Scan(&state); err != nil {
		return err
	}
	if state != Provisioning && state != Ready {
		return nil
	}
	seen := []string{}
	for _, l := range b.Lines {
		seen = append(seen, l.Address)
		if _, err := tx.Exec(ctx, `INSERT INTO lab_resources (lab_id, address, type, hourly_usd, usage_hourly_usd, started_at)
			SELECT $1, $2, $3, $4, $5, $6 WHERE NOT EXISTS (
				SELECT 1 FROM lab_resources WHERE lab_id = $1 AND address = $2 AND ended_at IS NULL)`,
			inst.ID, l.Address, l.Type, l.HourlyUSD, l.UsageHourlyUSD, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE lab_resources SET ended_at = $3
		WHERE lab_id = $1 AND ended_at IS NULL AND NOT (address = ANY($2))`, inst.ID, seen, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EndMeter ends every resource still metered for the lab: its destroy succeeded, or the reaper cleaned up after it.
func (s *Service) EndMeter(ctx context.Context, labID string, at time.Time) error {
	_, err := s.DB.Exec(ctx, `UPDATE lab_resources SET ended_at = $2 WHERE lab_id = $1 AND ended_at IS NULL`, labID, at)
	return err
}

// provisionLab runs a lab provisioner. An aws lab's state is metered afterwards whether the apply worked or not: a
// failed apply may have created resources. Metering never fails the lab; without rows, spend keeps the estimate.
func (s *Service) provisionLab(ctx context.Context, lp labProvisioner, inst *Instance, lab *content.Lab) error {
	err := lp.ProvisionLab(ctx, inst, lab)
	if _, ok := lp.(*AWSRunner); ok {
		mctx, cancel := finalCtx(ctx) // the apply may have used up ctx
		defer cancel()
		if merr := s.Meter(mctx, inst, lab); merr != nil {
			s.Log.Warn("metering the lab's terraform state failed; its spend stays on the estimate", "lab", inst.ID, "err", merr)
		}
	}
	return err
}
