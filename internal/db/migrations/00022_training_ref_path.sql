-- +goose Up
-- A training follows any git ref (a branch, a tag or a commit), not only a branch, from a folder inside its repo ('' = the root).
ALTER TABLE trainings RENAME COLUMN branch TO ref;
ALTER TABLE trainings ADD COLUMN path text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE trainings DROP COLUMN path;
ALTER TABLE trainings RENAME COLUMN ref TO branch;
