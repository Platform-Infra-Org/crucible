-- +goose Up
-- Admins are emailed a finding once; notified_at is set only after the send worked, so a failed send is retried.
ALTER TABLE reaper_findings ADD COLUMN notified_at TIMESTAMPTZ;
UPDATE reaper_findings SET notified_at = last_at; -- history is not news

-- +goose Down
ALTER TABLE reaper_findings DROP COLUMN notified_at;
