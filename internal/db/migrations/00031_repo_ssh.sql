-- +goose Up
-- An SSH deploy key is the other way to reach a repository: Crucible generates the pair, keeps the private half
-- sealed like any other credential, and shows the public half, which is not a secret and may be shown again at any
-- time. Deploy keys do not expire, and a host grants read or write per key.
ALTER TABLE repositories DROP CONSTRAINT repositories_auth_check;
ALTER TABLE repositories ADD CONSTRAINT repositories_auth_check CHECK (auth IN ('none', 'token', 'ssh'));
ALTER TABLE repositories ADD COLUMN public_key TEXT NOT NULL DEFAULT '';

-- +goose Down
UPDATE repositories SET auth = 'none', secret = '' WHERE auth = 'ssh';
ALTER TABLE repositories DROP COLUMN public_key;
ALTER TABLE repositories DROP CONSTRAINT repositories_auth_check;
ALTER TABLE repositories ADD CONSTRAINT repositories_auth_check CHECK (auth IN ('none', 'token'));
