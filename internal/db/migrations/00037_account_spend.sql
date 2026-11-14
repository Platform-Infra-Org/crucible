-- +goose Up
-- A monthly limit on the AWS account's real bill (spec 2026-11-14).
ALTER TABLE aws_ops
  ADD COLUMN account_usd DOUBLE PRECISION CHECK (account_usd >= 0), -- month-to-date bill, last good read
  ADD COLUMN account_read_at TIMESTAMPTZ,                              -- last successful read
  ADD COLUMN account_attempt_at TIMESTAMPTZ,                           -- last attempt (Cost Explorer's hourly gate)
  ADD COLUMN account_error TEXT NOT NULL DEFAULT '',
  ADD COLUMN account_error_at TIMESTAMPTZ,
  ADD COLUMN account_refresh_at TIMESTAMPTZ,                           -- last manual refresh (the 5-minute cooldown)
  ADD COLUMN forge_slept_at TIMESTAMPTZ,                               -- the timeline's night band
  ADD COLUMN forge_woke_at TIMESTAMPTZ;
ALTER TABLE settings ADD COLUMN account_monthly_limit_usd DOUBLE PRECISION NOT NULL DEFAULT 0
  CHECK (account_monthly_limit_usd >= 0);
ALTER TABLE lab_instances ADD COLUMN cap_reason TEXT NOT NULL DEFAULT '' CHECK (cap_reason IN ('', 'team', 'account'));
UPDATE lab_instances SET cap_reason = 'team' WHERE over_cap;
-- "AWS has billed this up to billed_to": lab_id '' is the account mark.
CREATE TABLE billed_marks (
  lab_id    TEXT PRIMARY KEY,
  billed_to TIMESTAMPTZ NOT NULL,
  set_by    TEXT NOT NULL,
  set_at    TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE billed_marks;
ALTER TABLE lab_instances DROP COLUMN cap_reason;
ALTER TABLE settings DROP COLUMN account_monthly_limit_usd;
ALTER TABLE aws_ops DROP COLUMN account_usd, DROP COLUMN account_read_at, DROP COLUMN account_attempt_at,
  DROP COLUMN account_error, DROP COLUMN account_error_at, DROP COLUMN account_refresh_at,
  DROP COLUMN forge_slept_at, DROP COLUMN forge_woke_at;
