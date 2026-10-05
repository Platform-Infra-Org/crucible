-- +goose Up
-- Email kinds a user turned off (spec §10: "Users can mute per event type (email) in settings").
CREATE TABLE notification_mutes (
  user_id BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
  kind    TEXT NOT NULL,
  PRIMARY KEY (user_id, kind)
);

-- +goose Down
DROP TABLE notification_mutes;
