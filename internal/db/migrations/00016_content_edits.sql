-- +goose Up
-- Content edits made in the UI (spec §6): pushed to a branch, reviewed by a maintainer in Crucible, merged by the bot.
CREATE TABLE content_edits (
  id         BIGSERIAL PRIMARY KEY,
  training   TEXT NOT NULL,
  title      TEXT NOT NULL,
  author     TEXT NOT NULL,                -- email, lowercased
  base_sha   TEXT NOT NULL,                -- tracked-branch head the author edited
  branch     TEXT NOT NULL DEFAULT '',     -- crucible/edit/<id>
  head_sha   TEXT NOT NULL DEFAULT '',     -- the edit commit that was pushed and is reviewed; the only one merged
  files      JSONB NOT NULL,               -- {path: new content}, changed files only
  diff       TEXT NOT NULL DEFAULT '',     -- unified diff vs base, computed by git at push time
  status     TEXT NOT NULL CHECK (status IN ('pending', 'merged', 'rejected', 'withdrawn', 'stale')),
  reviewer   TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  merge_sha  TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);
CREATE INDEX content_edits_pending ON content_edits (training) WHERE status = 'pending';
CREATE INDEX content_edits_author ON content_edits (author, created_at);

-- +goose Down
DROP TABLE content_edits;
