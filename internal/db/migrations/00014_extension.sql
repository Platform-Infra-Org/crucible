-- +goose Up
-- A lab extension that lifts the estimate into a higher tier waits for approval (spec §8.6 "Extension pending").
ALTER TABLE lab_instances
  ADD COLUMN ext_until        TIMESTAMPTZ,                          -- requested end; NULL = nothing pending
  ADD COLUMN ext_estimate_usd DOUBLE PRECISION NOT NULL DEFAULT 0,  -- the lab's estimate if approved
  ADD COLUMN ext_tier         TEXT NOT NULL DEFAULT '',
  ADD COLUMN ext_requested_at TIMESTAMPTZ;
CREATE INDEX lab_instances_ext_pending ON lab_instances (ext_requested_at) WHERE ext_until IS NOT NULL;

-- +goose Down
DROP INDEX lab_instances_ext_pending;
ALTER TABLE lab_instances DROP COLUMN ext_until, DROP COLUMN ext_estimate_usd, DROP COLUMN ext_tier, DROP COLUMN ext_requested_at;
