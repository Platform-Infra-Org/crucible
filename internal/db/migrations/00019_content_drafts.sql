-- +goose Up
-- Work in progress in the content editor: autosaved ops on a base commit. "Submit for review" turns one into an edit.
CREATE TABLE content_drafts (
  id                BIGSERIAL PRIMARY KEY,
  author            TEXT NOT NULL,                  -- email, lowercased
  training          TEXT NOT NULL,
  title             TEXT NOT NULL DEFAULT '',
  base_sha          TEXT NOT NULL,                  -- the tracked-branch head when the draft started or was last rebased
  ops               JSONB NOT NULL DEFAULT '[]',
  submitted_edit_id BIGINT REFERENCES content_edits (id) ON DELETE SET NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now() -- autosave's compare-and-set token
);
CREATE INDEX content_drafts_author ON content_drafts (author, training);

-- +goose Down
DROP TABLE content_drafts;
