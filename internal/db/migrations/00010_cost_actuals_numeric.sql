-- +goose Up
-- Cost Explorer reports decimal strings: store them exactly, and never negative (a credit row must not lower spend).
ALTER TABLE cost_actuals ALTER COLUMN usd TYPE NUMERIC(14,6);
ALTER TABLE cost_actuals ADD CONSTRAINT cost_actuals_usd_nonneg CHECK (usd >= 0);

-- +goose Down
ALTER TABLE cost_actuals DROP CONSTRAINT cost_actuals_usd_nonneg;
ALTER TABLE cost_actuals ALTER COLUMN usd TYPE DOUBLE PRECISION;
