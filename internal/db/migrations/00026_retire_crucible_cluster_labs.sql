-- +goose Up
-- Cluster labs moved to trainees' own clusters (spec 2026-10-11): labs still running on Crucible's cluster end here;
-- reconcileCluster then deletes their lab-<id> namespaces. The rate card for them goes.
UPDATE lab_instances SET state = 'destroyed', end_reason = 'cluster labs moved to your own cluster', destroyed_at = now()
  WHERE runtime = 'cluster' AND state IN ('pending_approval', 'provisioning', 'ready', 'destroying');
ALTER TABLE settings DROP COLUMN cluster_usd_per_hour;

-- +goose Down
ALTER TABLE settings ADD COLUMN cluster_usd_per_hour numeric;
