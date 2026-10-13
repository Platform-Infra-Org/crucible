-- +goose Up
-- The host keys ssh pinned for repositories connected over SSH: its known_hosts file, kept here because the file
-- itself lives in the data directory, which a restart may wipe. Crucible restores the file from this row before ssh
-- runs and saves it back when ssh has added a host, so a pinned host is never trusted afresh. One row at most.
CREATE TABLE ssh_known_hosts (
  id   BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  body TEXT NOT NULL
);

-- +goose Down
DROP TABLE ssh_known_hosts;
