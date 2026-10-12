-- +goose Up
-- What each Terraform lab really created and for how long, priced at list price (spec 2026-10-12 §3).
CREATE TABLE lab_resources (
  lab_id           TEXT NOT NULL,
  address          TEXT NOT NULL,
  type             TEXT NOT NULL,
  hourly_usd       DOUBLE PRECISION NOT NULL CHECK (hourly_usd >= 0),
  usage_hourly_usd DOUBLE PRECISION NOT NULL CHECK (usage_hourly_usd >= 0),
  started_at       TIMESTAMPTZ NOT NULL,
  ended_at         TIMESTAMPTZ,
  PRIMARY KEY (lab_id, address, started_at)
);
CREATE INDEX lab_resources_open ON lab_resources (lab_id) WHERE ended_at IS NULL;

-- +goose Down
DROP TABLE lab_resources;
