-- +goose Up
-- Smith and sword ranks (spec 2026-10-11): a sword rank per training and the smith's high-water experience and level.
-- Both are raised only. The old single personal rank (ranks) is dropped by a later migration, once nothing writes it.
ALTER TABLE settings
  ADD COLUMN smith_apprentice  INT NOT NULL DEFAULT 150,
  ADD COLUMN smith_journeyman  INT NOT NULL DEFAULT 500,
  ADD COLUMN smith_smith       INT NOT NULL DEFAULT 1200,
  ADD COLUMN smith_master      INT NOT NULL DEFAULT 2500,
  ADD COLUMN smith_grandmaster INT NOT NULL DEFAULT 5000;
CREATE TABLE sword_ranks (
  user_id   BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  training  TEXT NOT NULL,
  level     INT NOT NULL,                      -- 0 Ore … 5 Masterwork
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL,                  -- false until the trainee has been shown the rise
  PRIMARY KEY (user_id, training)
);
CREATE TABLE smiths (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  xp        INT NOT NULL,                      -- high-water experience
  level     INT NOT NULL,                      -- 0 Bellows Hand … 5 Grandmaster
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL
);

-- +goose Down
DROP TABLE smiths, sword_ranks;
ALTER TABLE settings DROP COLUMN smith_apprentice, DROP COLUMN smith_journeyman, DROP COLUMN smith_smith,
  DROP COLUMN smith_master, DROP COLUMN smith_grandmaster;
