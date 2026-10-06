-- +goose Up
-- Set once, atomically, when admins are told a lab's destroy is stuck (one alert per lab).
ALTER TABLE lab_instances ADD COLUMN stuck_alerted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE lab_instances DROP COLUMN stuck_alerted_at;
