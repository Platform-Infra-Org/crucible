-- +goose Up
-- List prices Crucible downloads from the free public AWS Price List (spec 2026-10-12 §2).
CREATE TABLE prices (
  service    TEXT NOT NULL,          -- aws
  region     TEXT NOT NULL,
  key        TEXT NOT NULL,          -- ec2:t3.micro, ebs:gp3, nat:hour, …
  usd        DOUBLE PRECISION NOT NULL CHECK (usd > 0),
  unit       TEXT NOT NULL,          -- Hrs, GB-Mo, GB
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (service, region, key)
);
CREATE TABLE price_refresh (
  id       INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  ok_at    TIMESTAMPTZ,
  error    TEXT NOT NULL DEFAULT '',
  error_at TIMESTAMPTZ
);
INSERT INTO price_refresh (id) VALUES (1);

-- +goose Down
DROP TABLE price_refresh, prices;
