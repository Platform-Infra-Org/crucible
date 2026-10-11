-- +goose Up
-- Mentors are gone: a team's leaders and seniors follow its people (owner's call, 2026-10-11).
DROP TABLE mentors;

-- +goose Down
CREATE TABLE mentors (
  team    text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  trainee text NOT NULL CHECK (trainee = lower(btrim(trainee))),
  mentor  text NOT NULL CHECK (mentor = lower(btrim(mentor))),
  PRIMARY KEY (team, trainee)
);
