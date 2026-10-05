-- +goose Up
-- Every privileged action (spec §14), with the platform-repo commit when the action wrote config.
CREATE TABLE audit_log (
  id         BIGSERIAL PRIMARY KEY,
  at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor      TEXT NOT NULL,                -- email, lowercased
  action     TEXT NOT NULL,                -- lab.approve, kill_switch.on, program.update, …
  target     TEXT NOT NULL DEFAULT '',
  detail     JSONB NOT NULL DEFAULT '{}',
  commit_sha TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log (at DESC);

-- +goose Down
DROP TABLE audit_log;
