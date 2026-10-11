-- +goose Up
-- The kubeconfig context a cluster lab ran on, as the trainee's agent named it. Shown only; the server never connects.
ALTER TABLE lab_instances ADD COLUMN cluster_context text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE lab_instances DROP COLUMN cluster_context;
