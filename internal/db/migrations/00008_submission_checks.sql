-- +goose Up
-- The one_live_submission index predicate assumes these values: a typo'd status would silently escape it.
ALTER TABLE submissions
  ADD CONSTRAINT submissions_status_check CHECK (status IN ('pending', 'scored', 'returned')),
  ADD CONSTRAINT submissions_kind_check CHECK (kind IN ('question', 'task'));

-- +goose Down
ALTER TABLE submissions DROP CONSTRAINT submissions_status_check, DROP CONSTRAINT submissions_kind_check;
