-- +goose Up
-- Content edits become ordered operations (put, rename, delete). files keeps the puts for one release, so a Down
-- loses no content it had.
ALTER TABLE content_edits ADD COLUMN ops JSONB NOT NULL DEFAULT '[]';
UPDATE content_edits SET ops = coalesce((SELECT jsonb_agg(jsonb_build_object('op', 'put', 'path', f.key, 'content', f.value) ORDER BY f.key)
  FROM jsonb_each_text(files) f), '[]'::jsonb);
ALTER TABLE content_edits ALTER COLUMN files SET DEFAULT '{}';

-- +goose Down
ALTER TABLE content_edits ALTER COLUMN files DROP DEFAULT;
ALTER TABLE content_edits DROP COLUMN ops;
