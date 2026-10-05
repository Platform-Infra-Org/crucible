-- +goose Up
CREATE TABLE users (
  id          BIGSERIAL PRIMARY KEY,
  sub         TEXT NOT NULL UNIQUE,
  email       TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  theme       TEXT NOT NULL DEFAULT '',
  calm_motion BOOLEAN NOT NULL DEFAULT false,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX users_email ON users (email);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,               -- sha256 of the cookie value
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE agent_tokens (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  revoked_at   TIMESTAMPTZ
);

CREATE TABLE item_progress (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  item       TEXT NOT NULL,
  status     TEXT NOT NULL,                  -- in_progress | complete
  score      DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, item)
);

CREATE TABLE quiz_attempts (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  sha        TEXT NOT NULL,
  answers    JSONB NOT NULL,
  score      DOUBLE PRECISION NOT NULL,
  max_score  DOUBLE PRECISION NOT NULL,
  passed     BOOLEAN NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE lab_instances (
  id               TEXT PRIMARY KEY,
  user_id          BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team             TEXT NOT NULL,
  training         TEXT NOT NULL,
  module           TEXT NOT NULL,
  sha              TEXT NOT NULL,
  runtime          TEXT NOT NULL,
  state            TEXT NOT NULL,            -- provisioning | ready | destroying | destroyed | failed
  error            TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL,
  ready_at         TIMESTAMPTZ,
  ends_at          TIMESTAMPTZ,
  limit_reason     TEXT NOT NULL DEFAULT '', -- which limit sets ends_at (ttl, …)
  end_reason       TEXT NOT NULL DEFAULT '', -- why the lab was destroyed
  last_activity_at TIMESTAMPTZ NOT NULL,
  ttl_s            INT NOT NULL,
  idle_timeout_s   INT NOT NULL,
  idle_warning_s   INT NOT NULL,
  max_extension_s  INT NOT NULL,
  extended         BOOLEAN NOT NULL DEFAULT false,
  destroyed_at     TIMESTAMPTZ
);
CREATE UNIQUE INDEX one_active_lab ON lab_instances (user_id, team, training, module)
  WHERE state IN ('provisioning', 'ready', 'destroying');

CREATE TABLE lab_events (
  id     BIGSERIAL PRIMARY KEY,
  lab_id TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind   TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);

-- Task results survive lab re-creation (spec §8.6: "passed tasks stay passed").
CREATE TABLE lab_task_progress (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  task       TEXT NOT NULL,
  status     TEXT NOT NULL,                  -- passed | skipped
  points     DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, task)
);

CREATE TABLE check_runs (
  id            BIGSERIAL PRIMARY KEY,
  lab_id        TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  task          TEXT NOT NULL,
  exit_code     INT NOT NULL,
  output        TEXT NOT NULL,
  answer        TEXT NOT NULL DEFAULT '',
  self_reported BOOLEAN NOT NULL,
  at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE setup_runs (
  id          BIGSERIAL PRIMARY KEY,
  lab_id      TEXT NOT NULL REFERENCES lab_instances ON DELETE CASCADE,
  task        TEXT NOT NULL DEFAULT '',      -- '' = lab-level setup
  attempt     INT NOT NULL,
  exit_code   INT NOT NULL,
  output      TEXT NOT NULL,
  duration_ms INT NOT NULL,
  at          TIMESTAMPTZ NOT NULL
);

-- Keyed by user/module/task so a fresh lab never charges for the same hint twice.
CREATE TABLE hint_reveals (
  user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  team       TEXT NOT NULL,
  training   TEXT NOT NULL,
  module     TEXT NOT NULL,
  task       TEXT NOT NULL,
  hint_index INT NOT NULL,
  cost       DOUBLE PRECISION NOT NULL,
  lab_id     TEXT NOT NULL,
  at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, team, training, module, task, hint_index)
);

-- +goose Down
DROP TABLE hint_reveals, setup_runs, check_runs, lab_task_progress, lab_events,
  lab_instances, quiz_attempts, item_progress, agent_tokens, sessions, users;
