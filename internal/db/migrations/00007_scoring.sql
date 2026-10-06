-- +goose Up
-- Human-scored work (spec §7, §13). A row is one answer, review-task submission or live sign-off; its score is a
-- decision on that row. Return-for-rework keeps the row (status returned) and the next answer is a new row.
CREATE TABLE submissions (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  sha        TEXT NOT NULL,                   -- content version the trainee answered
  kind       TEXT NOT NULL,                   -- question | task
  item       TEXT NOT NULL,                   -- question id or lab task id
  lab_id     TEXT REFERENCES lab_instances ON DELETE SET NULL,
  qtype      TEXT NOT NULL,                   -- text | upload | signoff | review
  prompt     TEXT NOT NULL,                   -- snapshots: the scorer sees what the trainee saw
  rubric     TEXT NOT NULL DEFAULT '',
  max_points DOUBLE PRECISION NOT NULL,
  answer     TEXT NOT NULL DEFAULT '',
  files      JSONB NOT NULL DEFAULT '[]',     -- [{name, size}] shown to people
  file_keys  TEXT[] NOT NULL DEFAULT '{}',    -- blob keys, same order; never sent to browsers
  status     TEXT NOT NULL,                   -- pending | scored | returned
  points     DOUBLE PRECISION NOT NULL DEFAULT 0,
  feedback   TEXT NOT NULL DEFAULT '',
  scored_by  TEXT NOT NULL DEFAULT '',
  scored_at  TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- At most one live answer per trainee and item: a pending one waits, a scored one is final.
CREATE UNIQUE INDEX one_live_submission ON submissions (user_id, team, training, module, kind, item)
  WHERE status IN ('pending', 'scored');
CREATE INDEX submissions_queue ON submissions (created_at) WHERE status = 'pending';

-- Recorded terminal output per session (spec §7, §13), stored in blob storage.
CREATE TABLE terminal_transcripts (
  id         BIGSERIAL PRIMARY KEY,
  lab_id     TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  terminal   TEXT NOT NULL,
  blob_key   TEXT NOT NULL,
  bytes      INT NOT NULL,
  truncated  BOOLEAN NOT NULL,                -- only the tail was kept
  started_at TIMESTAMPTZ NOT NULL,
  ended_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX terminal_transcripts_lab ON terminal_transcripts (lab_id);

-- +goose Down
DROP TABLE terminal_transcripts, submissions;
