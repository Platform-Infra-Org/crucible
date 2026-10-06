package labs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"crucible/internal/awscloud"
	"crucible/internal/notify"
)

// Reap is the periodic reaper (spec §8.2). For each allowed region it lists every resource tagged crucible:lab-id.
// Resources of a lab this database knows and that ended over an hour ago are deleted with that lab's own role
// (the destroy-time sweep had its chance, and the tag inventory lags; this also catches aws labs whose namespace
// reconcileCluster removed without terraform destroy). Resources with an unknown or malformed lab id are reported,
// never deleted: they may belong to another deployment or to a restored database. Successful create calls by lab
// sessions without the lab tag (CloudTrail) are reported. If the database cannot be read, nothing is deleted.
// Deletes AWS refuses for now (awscloud.ErrNotYet) are retried by the next run. Admins get one summary when
// something new turns up.
func (s *Service) Reap(ctx context.Context) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now()
	var fresh []string
	var errs []error
	report := func(source, labID, arn, detail string) {
		if s.finding(ctx, source, labID, arn, "reported", detail) {
			fresh = append(fresh, arn)
		}
	}
	for _, region := range s.AWSRegions {
		res, err := s.Cloud.Tagged(ctx, region, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: listing tagged resources: %w", region, err))
			continue
		}
		byLab := map[string][]awscloud.Resource{}
		var ids []string
		for _, r := range res {
			if !validLabID(r.LabID) {
				report("reaper", "", r.ARN, fmt.Sprintf("tagged crucible:lab-id=%q, which is not a Crucible lab id: check it by hand", r.LabID))
				continue
			}
			if byLab[r.LabID] == nil {
				ids = append(ids, r.LabID)
			}
			byLab[r.LabID] = append(byLab[r.LabID], r)
		}
		ended, known, err := s.labsEnded(ctx, ids, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: reading labs: %w", region, err))
			continue // never guess: without the database nothing is deleted
		}
		for _, id := range ids {
			switch {
			case !known[id]:
				for _, r := range byLab[id] {
					report("reaper", id, r.ARN, "no lab with this id in this Crucible (another deployment, or a restored database?): delete it by hand if nobody uses it")
				}
			case !ended[id].IsZero() && ended[id].Before(now.Add(-time.Hour)):
				creds, err := s.Cloud.AssumeLab(ctx, awscloud.Session{LabID: id})
				if err != nil {
					errs = append(errs, fmt.Errorf("lab %s: credentials: %w", id, err))
					continue
				}
				stuck := ended[id].Before(now.Add(-notYetGrace))
				fresh = append(fresh, s.deleteAll(ctx, region, creds, "reaper", byLab[id], stuck)...)
			}
		}
		events, err := s.Cloud.LabWrites(ctx, region, now.Add(-24*time.Hour))
		if err != nil {
			// ErrTruncated still returns what it read: report that, and mark the run incomplete.
			errs = append(errs, fmt.Errorf("%s: CloudTrail: %w", region, err))
		}
		for _, e := range events {
			report("trail", e.LabID, "cloudtrail:"+e.ID, fmt.Sprintf("%s at %s by lab %s created %s without the crucible:lab-id tag: check the resource and the lab role's policy",
				e.Event, e.At.UTC().Format(time.RFC3339), e.LabID, strings.Join(e.Resources, ", ")))
		}
	}
	if err := errors.Join(errs...); err != nil {
		s.Log.Warn("reaper run incomplete", "err", err)
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET reap_error = $1, reap_error_at = $2`, cleanText(err.Error()), now)
	} else {
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET reap_ok_at = $1, reap_error = ''`, now)
	}
	if len(fresh) > 0 {
		if st, err := s.platform(); err == nil {
			s.notify(ctx, notify.Event{Kind: notify.ReaperReport, To: st.Platform.Admins, Link: "/ledger",
				Subject: fmt.Sprintf("The reaper found %d leftover AWS lab resource(s)", len(fresh)),
				Text:    "Deleted, failed, or waiting for a human: see Reaper findings on the Ledger.\n\n" + strings.Join(fresh, "\n")})
		}
	}
	return nil
}

// notYetGrace is how long after a lab ended a delete AWS still refuses (ErrNotYet) stays a quiet retry; after it,
// the reaper records it as failed so admins hear of it once.
const notYetGrace = 24 * time.Hour

// labsEnded looks lab ids up: known = this database has the lab; ended = when it ended (zero while it is not over;
// a failed lab without destroyed_at counts from its creation).
func (s *Service) labsEnded(ctx context.Context, ids []string, now time.Time) (ended map[string]time.Time, known map[string]bool, err error) {
	ended, known = map[string]time.Time{}, map[string]bool{}
	if len(ids) == 0 {
		return ended, known, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT id, CASE WHEN state IN ('pending_approval', 'provisioning', 'ready', 'destroying')
		THEN NULL ELSE least(coalesce(destroyed_at, created_at), $2) END FROM lab_instances WHERE id = ANY($1)`, ids, now)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var at *time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, nil, err
		}
		known[id] = true
		if at != nil {
			ended[id] = *at
		}
	}
	return ended, known, rows.Err()
}

// IngestCosts stores Cost Explorer's daily cost per lab (spec §9.3): month to date plus the three days before,
// because a day's figure keeps changing for a while. Days are UTC, the end is exclusive. Every row in the window is
// upserted and ingest_ok_at moves only when the whole run committed (the spend settle rule relies on it). A failure
// is recorded for the Ledger ("actuals stale") and retried by the next periodic run; caps keep using estimates
// meanwhile (spec §14).
func (s *Service) IngestCosts(ctx context.Context) error {
	if s.Cloud == nil {
		return nil
	}
	now := s.Now().UTC()
	from, to := monthStart(now.AddDate(0, 0, -3)), now.Truncate(24*time.Hour).AddDate(0, 0, 1)
	err := s.ingestCosts(ctx, from, to, now)
	if err != nil {
		s.Log.Warn("cost explorer ingestion failed; the Ledger marks actuals stale", "err", err)
		_, _ = s.DB.Exec(ctx, `UPDATE aws_ops SET ingest_error = $1, ingest_error_at = $2`, cleanText(err.Error()), now)
	}
	return nil
}

func (s *Service) ingestCosts(ctx context.Context, from, to, now time.Time) error {
	costs, err := s.Cloud.Costs(ctx, from, to)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	for _, c := range costs {
		if !validLabID(c.LabID) {
			continue
		}
		usd := c.USD
		if !(usd > 0) { // credits and refunds net below zero (and NaN): cost_actuals.usd is never negative
			usd = 0
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cost_actuals (lab_id, day, usd, updated_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (lab_id, day) DO UPDATE SET usd = excluded.usd, updated_at = excluded.updated_at`,
			c.LabID, c.Day.UTC(), usd, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE aws_ops SET ingest_ok_at = $1, ingest_error = ''`, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
