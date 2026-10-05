-- +goose Up
-- A lab request is a lab_instances row (spec §8.1): pending_approval → provisioning … or rejected / expired.
ALTER TABLE lab_instances
  ADD COLUMN hourly_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
  ADD COLUMN estimate_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,  -- hourly × TTL when requested
  ADD COLUMN tier          TEXT NOT NULL DEFAULT 'auto',         -- auto | approver | leader | admin (current tier)
  ADD COLUMN over_cap      BOOLEAN NOT NULL DEFAULT false,       -- would pass a hard cap: admin only, audited
  ADD COLUMN escalate_at   TIMESTAMPTZ,                          -- pending: when it moves up a tier (or expires)
  ADD COLUMN decided_by    TEXT NOT NULL DEFAULT '',
  ADD COLUMN decided_at    TIMESTAMPTZ,
  ADD COLUMN decision_note TEXT NOT NULL DEFAULT '';
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
CREATE INDEX lab_instances_pending ON lab_instances (escalate_at) WHERE state = 'pending_approval';

-- +goose Down
DROP INDEX lab_instances_pending;
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('provisioning', 'ready', 'destroying');
ALTER TABLE lab_instances DROP COLUMN hourly_usd, DROP COLUMN estimate_usd, DROP COLUMN tier, DROP COLUMN over_cap,
  DROP COLUMN escalate_at, DROP COLUMN decided_by, DROP COLUMN decided_at, DROP COLUMN decision_note;
