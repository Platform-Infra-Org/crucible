-- +goose Up
-- The forge icon a person picked for their user card ('' = their initials).
ALTER TABLE users ADD COLUMN avatar TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN avatar;
