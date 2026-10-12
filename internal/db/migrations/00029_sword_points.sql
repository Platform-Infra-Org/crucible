-- +goose Up
-- The work done on each training, at its highest, so experience keeps the work of a training taken away. Existing rows
-- start at 0 and rise to the current work on the person's next computation.
ALTER TABLE sword_ranks ADD COLUMN points DOUBLE PRECISION NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sword_ranks DROP COLUMN points;
