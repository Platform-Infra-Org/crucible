-- +goose Up
-- Global lab kill switch (spec §9.2): exactly one row.
CREATE TABLE kill_switch (
  id         BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  enabled    BOOLEAN NOT NULL DEFAULT false,
  changed_by TEXT NOT NULL DEFAULT '',
  changed_at TIMESTAMPTZ
);
INSERT INTO kill_switch DEFAULT VALUES;

-- Budget alerts already sent: once per scope, month and level.
CREATE TABLE budget_alerts (
  scope TEXT NOT NULL,         -- "<team>" or "<team>/<training>"
  month DATE NOT NULL,
  level INT NOT NULL,          -- 80 | 100
  at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, month, level)
);

-- +goose Down
DROP TABLE budget_alerts, kill_switch;
