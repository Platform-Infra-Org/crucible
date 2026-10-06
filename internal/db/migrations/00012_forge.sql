-- +goose Up
-- Forge ranks (spec §7, §13): the highest rank a user ever earned. It is never lowered.
CREATE TABLE ranks (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  level     INT NOT NULL,                      -- 0 Ore … 5 Masterwork
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL                   -- false until the trainee has been shown the rank-up
);
-- One badge per completed training, whichever team the completion came through.
CREATE TABLE badges (
  user_id   BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  training  TEXT NOT NULL,
  team      TEXT NOT NULL,
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, training)
);

-- +goose Down
DROP TABLE badges, ranks;
