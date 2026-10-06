-- +goose Up
-- The journey view's failed-check and lab-failure joins.
CREATE INDEX IF NOT EXISTS check_runs_lab ON check_runs (lab_id);
CREATE INDEX IF NOT EXISTS lab_instances_user ON lab_instances (user_id);

-- +goose Down
DROP INDEX IF EXISTS lab_instances_user;
DROP INDEX IF EXISTS check_runs_lab;
