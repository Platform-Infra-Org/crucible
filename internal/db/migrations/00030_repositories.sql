-- +goose Up
-- A connected repository: the URL, how Crucible authenticates to it, and how that last went. A repository holds any
-- number of trainings (one repo, a folder per training), so the credential is connected once and rotated once.
-- secret is sealed by internal/secrets before it gets here: this column holds ciphertext, never a usable credential.
CREATE TABLE repositories (
  id         BIGSERIAL PRIMARY KEY,
  url        TEXT NOT NULL UNIQUE,
  auth       TEXT NOT NULL DEFAULT 'none' CHECK (auth IN ('none', 'token')),
  username   TEXT NOT NULL DEFAULT '',   -- hosts that want one beside the token (GitLab takes any non-empty name)
  secret     TEXT NOT NULL DEFAULT '',   -- sealed; '' when auth = 'none'
  status     TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'ok', 'failed')),
  can_write  BOOLEAN,                    -- NULL until a connection test says
  last_error TEXT NOT NULL DEFAULT '',   -- redacted before it is stored
  checked_at TIMESTAMPTZ,
  version    BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((auth = 'none') = (secret = ''))
);

-- Carry the trainings already registered over, one repository per distinct URL: trainings that share a repo now
-- share its connection. A URL that already carries a credential keeps it for now; reconnecting moves it out of the URL.
INSERT INTO repositories (url) SELECT DISTINCT repo FROM trainings;

ALTER TABLE trainings ADD COLUMN repository BIGINT REFERENCES repositories;
UPDATE trainings t SET repository = r.id FROM repositories r WHERE r.url = t.repo;
ALTER TABLE trainings ALTER COLUMN repository SET NOT NULL;
ALTER TABLE trainings DROP COLUMN repo;
CREATE INDEX trainings_repository_idx ON trainings (repository);

-- +goose Down
ALTER TABLE trainings ADD COLUMN repo TEXT NOT NULL DEFAULT '';
UPDATE trainings t SET repo = r.url FROM repositories r WHERE r.id = t.repository;
ALTER TABLE trainings ALTER COLUMN repo DROP DEFAULT;
DROP INDEX trainings_repository_idx;
ALTER TABLE trainings DROP COLUMN repository;
DROP TABLE repositories;
