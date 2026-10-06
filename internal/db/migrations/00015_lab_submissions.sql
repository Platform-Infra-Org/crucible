-- +goose Up
-- A whole self-reported (local) lab can be filed for a scorer (spec §8.2).
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check,
  ADD CONSTRAINT submissions_kind_check CHECK (kind IN ('question', 'task', 'lab'));

-- +goose Down
-- Lab items left in pending_review by those submissions stay stuck after a downgrade; reset them by hand if you roll back.
DELETE FROM submissions WHERE kind = 'lab';
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check,
  ADD CONSTRAINT submissions_kind_check CHECK (kind IN ('question', 'task'));
