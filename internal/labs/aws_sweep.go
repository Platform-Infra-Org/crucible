package labs

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"crucible/internal/awscloud"
)

// sweepLab deletes whatever still carries this lab's tag after terraform destroy (spec §8.2): resources the trainee
// made by hand in the workspace, or what a failed destroy left. What it deletes, or cannot, is recorded; what AWS
// refuses for now (ErrNotYet) is left to the reaper's next run.
func (s *Service) sweepLab(ctx context.Context, labID string) {
	if s.Cloud == nil {
		return
	}
	creds, err := s.Cloud.AssumeLab(ctx, awscloud.Session{LabID: labID})
	if err != nil {
		s.Log.Warn("tag sweep: no lab credentials; the reaper will retry", "lab", labID, "err", err)
		return
	}
	for _, region := range s.AWSRegions {
		res, err := s.Cloud.Tagged(ctx, region, labID)
		if err != nil {
			s.Log.Warn("tag sweep: listing failed; the reaper will retry", "lab", labID, "region", region, "err", err)
			continue
		}
		s.deleteAll(ctx, region, creds, "destroy", res)
	}
}

// deleteAll deletes resources with one lab's credentials, instances first (their volumes and security groups are
// only free once they are gone), and records each result. Already-gone resources (terraform got them; the tag
// inventory lags) are not findings, nor are deletes AWS refuses for now. It returns the ARNs recorded for the first
// time.
func (s *Service) deleteAll(ctx context.Context, region string, creds awscloud.Credentials, source string, res []awscloud.Resource) []string {
	first := func(r awscloud.Resource) int {
		if strings.Contains(r.ARN, ":instance/") {
			return 0
		}
		return 1
	}
	res = slices.Clone(res)
	slices.SortStableFunc(res, func(a, b awscloud.Resource) int { return cmp.Compare(first(a), first(b)) })
	var fresh []string
	for _, r := range res {
		deleted, err := s.Cloud.Delete(ctx, region, creds, r.ARN)
		action, detail := "deleted", ""
		switch {
		case errors.Is(err, awscloud.ErrNotYet):
			s.Log.Info("tag sweep: not deletable yet; retried on the next run", "arn", r.ARN, "err", err)
			continue
		case errors.Is(err, awscloud.ErrUnsupported):
			action, detail = "reported", err.Error()
		case err != nil:
			action, detail = "failed", err.Error()
		case !deleted:
			continue
		}
		if s.finding(ctx, source, r.LabID, r.ARN, action, detail) {
			fresh = append(fresh, r.ARN)
		}
	}
	return fresh
}

// finding records one result; the same (source, ARN) again updates its row. It reports whether the row is new.
func (s *Service) finding(ctx context.Context, source, labID, arn, action, detail string) bool {
	var inserted bool
	err := s.DB.QueryRow(ctx, `INSERT INTO reaper_findings (source, arn, lab_id, action, detail, first_at, last_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (source, arn) DO UPDATE SET lab_id = excluded.lab_id, action = excluded.action,
			detail = excluded.detail, last_at = excluded.last_at
		RETURNING xmax = 0`, source, cleanText(arn), cleanText(labID), action, cleanText(detail), s.Now()).Scan(&inserted)
	if err != nil {
		s.Log.Error("recording a reaper finding failed", "arn", arn, "err", err)
	}
	return inserted
}

// refreshAWS keeps the credentials of running aws labs fresh (spec §14). Every sweep calls it; Refresh only
// calls STS when less than 15 minutes are left.
func (s *Service) refreshAWS(ctx context.Context) {
	r, _ := s.runner("aws")
	ar, ok := r.(*AWSRunner)
	if !ok {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("aws credential refresh query failed", "err", err)
		return
	}
	labs, err := collectInst(rows)
	if err != nil {
		s.Log.Error("aws credential refresh query failed", "err", err)
		return
	}
	for _, inst := range labs {
		if err := ar.Refresh(ctx, inst); err != nil {
			s.Log.Warn("refreshing aws lab credentials failed", "lab", inst.ID, "err", err)
		}
	}
}
