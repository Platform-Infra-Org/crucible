-- +goose Up
-- AWS lab costs and clean-up (spec §8.2, §9.3).
-- Daily cost per lab from Cost Explorer, grouped by the crucible:lab-id tag. No foreign key: Cost Explorer can
-- report labs this database no longer has.
CREATE TABLE cost_actuals (
  lab_id     TEXT NOT NULL,
  day        DATE NOT NULL,
  usd        DOUBLE PRECISION NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (lab_id, day)
);
CREATE INDEX cost_actuals_day ON cost_actuals (day);

-- One row: the last cost ingestion and reaper run, and their last errors.
CREATE TABLE aws_ops (
  id              BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  ingest_ok_at    TIMESTAMPTZ,
  ingest_error    TEXT NOT NULL DEFAULT '',
  ingest_error_at TIMESTAMPTZ,
  reap_ok_at      TIMESTAMPTZ,
  reap_error      TEXT NOT NULL DEFAULT '',
  reap_error_at   TIMESTAMPTZ
);
INSERT INTO aws_ops DEFAULT VALUES;

-- What the destroy-time tag sweep, the reaper and the CloudTrail check found. Seeing the same thing again updates it.
CREATE TABLE reaper_findings (
  source   TEXT NOT NULL CHECK (source IN ('destroy', 'reaper', 'trail')),
  arn      TEXT NOT NULL,             -- resource ARN, or cloudtrail:<event id>
  lab_id   TEXT NOT NULL DEFAULT '',
  action   TEXT NOT NULL CHECK (action IN ('deleted', 'failed', 'reported')),
  detail   TEXT NOT NULL DEFAULT '',
  first_at TIMESTAMPTZ NOT NULL,
  last_at  TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (source, arn)
);
CREATE INDEX reaper_findings_last ON reaper_findings (last_at DESC);

-- +goose Down
DROP TABLE reaper_findings, aws_ops, cost_actuals;
