-- +goose Up
-- The single personal rank is gone: sword_ranks and smiths (00027) replaced it and nothing reads or writes it now.
DROP TABLE ranks;

-- +goose Down
CREATE TABLE ranks (
  user_id   BIGINT PRIMARY KEY REFERENCES users ON DELETE CASCADE,
  level     INT NOT NULL,                      -- 0 Ore … 5 Masterwork
  earned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  seen      BOOLEAN NOT NULL                   -- false until the trainee has been shown the rank-up
);
