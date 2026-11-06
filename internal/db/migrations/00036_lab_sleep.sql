-- +goose Up
-- AWS labs sleep with the platform (spec 2026-11-05): ready → asleep → waking → ready.
ALTER TABLE lab_instances
  ADD COLUMN slept_at     TIMESTAMPTZ,
  ADD COLUMN slept_as     TEXT NOT NULL DEFAULT '',  -- '' (no machines) | hibernated | stopped (any machine stopped)
  ADD COLUMN woke_at      TIMESTAMPTZ,
  ADD COLUMN waking_since TIMESTAMPTZ;
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'asleep', 'waking', 'destroying');
-- What a resource costs per hour while its lab sleeps: an instance's disks; anything else its full price.
ALTER TABLE lab_resources ADD COLUMN stopped_hourly_usd DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (stopped_hourly_usd >= 0);
UPDATE lab_resources SET stopped_hourly_usd = hourly_usd; -- rows metered before this: the safe side
-- One row while the platform goes to sleep or sleeps; the server deletes it when it starts (labs.Service.Morning).
CREATE TABLE platform_bedtime (
  id           BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  requested_at TIMESTAMPTZ NOT NULL,
  requested_by TEXT NOT NULL,
  phase        TEXT NOT NULL DEFAULT 'settling' CHECK (phase IN ('settling', 'sleeping', 'asleep'))
);

-- +goose Down
DROP TABLE platform_bedtime;
ALTER TABLE lab_resources DROP COLUMN stopped_hourly_usd;
UPDATE lab_instances SET state = 'ready' WHERE state IN ('asleep', 'waking');
DROP INDEX one_active_lab;
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
ALTER TABLE lab_instances DROP COLUMN slept_at, DROP COLUMN slept_as, DROP COLUMN woke_at, DROP COLUMN waking_since;
