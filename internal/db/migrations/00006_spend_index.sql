-- +goose Up
-- Month-to-date spend: WHERE team = $1 AND created_at >= month start (lobby polls, approval cards).
CREATE INDEX lab_instances_team_month ON lab_instances (team, created_at);

-- +goose Down
DROP INDEX lab_instances_team_month;
