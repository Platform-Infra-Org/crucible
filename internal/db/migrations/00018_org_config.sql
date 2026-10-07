-- +goose Up
-- Configuration and org move out of the git platform repo (spec: 2026-10-07-db-owned-config-design.md).
CREATE TABLE settings (
  id                   int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  default_theme        text NOT NULL DEFAULT 'forge',
  auto_approve_usd     numeric,          -- NULL together with the tiers below: tiers unset, paid labs refused
  tier1_usd            numeric,
  tier2_usd            numeric,
  cluster_usd_per_hour numeric,          -- NULL: cluster labs unavailable, never free
  escalation_hours     numeric NOT NULL DEFAULT 4,
  rank_ingot           numeric NOT NULL DEFAULT 20,
  rank_tempered        numeric NOT NULL DEFAULT 45,
  rank_blade           numeric NOT NULL DEFAULT 75,
  rank_sword           numeric NOT NULL DEFAULT 90,
  rank_masterwork      numeric NOT NULL DEFAULT 100,
  version              bigint  NOT NULL DEFAULT 1
);
INSERT INTO settings (id) VALUES (1);

CREATE TABLE schedules (
  name     text PRIMARY KEY,
  timezone text  NOT NULL,
  windows  jsonb NOT NULL                -- [{days:[mon..sun], start:"08:00", end:"19:00"}]
);

CREATE TABLE quotes (id bigserial PRIMARY KEY, text text NOT NULL);

CREATE TABLE admins (email text PRIMARY KEY);

CREATE TABLE trainings (
  id     text PRIMARY KEY,
  repo   text NOT NULL,
  branch text NOT NULL DEFAULT 'main'
);

CREATE TABLE teams (
  id      text PRIMARY KEY,
  name    text   NOT NULL,
  version bigint NOT NULL DEFAULT 1
);

CREATE TABLE team_members (
  team  text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  email text NOT NULL,
  role  text NOT NULL CHECK (role IN ('leader', 'senior', 'member', 'trainee')),
  PRIMARY KEY (team, email)              -- one role per person per team (spec 5.2)
);

CREATE TABLE mentors (
  team    text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  trainee text NOT NULL,
  mentor  text NOT NULL,
  PRIMARY KEY (team, trainee)
);

CREATE TABLE team_webhooks (
  team text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('slack', 'teams')),
  url  text NOT NULL,
  PRIMARY KEY (team, kind)
);

CREATE TABLE team_budgets (
  team         text PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,
  monthly_usd  numeric NOT NULL DEFAULT 0,
  hard_cap_usd numeric NOT NULL DEFAULT 0,
  version      bigint  NOT NULL DEFAULT 1
);

CREATE TABLE programs (
  team                 text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  training             text NOT NULL,
  pinned_ref           text,             -- NULL: track the tracked branch head
  schedule_name        text,
  inline_schedule      jsonb,
  ttl                  text,
  idle_timeout         text,
  max_extension        text,
  budget_usd_month     numeric NOT NULL DEFAULT 0,
  review_self_reported boolean NOT NULL DEFAULT false,
  version              bigint  NOT NULL DEFAULT 1,
  PRIMARY KEY (team, training)
);

CREATE TABLE program_roles (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL,
  role     text NOT NULL CHECK (role IN ('manager', 'scorer', 'approver')),
  PRIMARY KEY (team, training, email, role),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);

CREATE TABLE enrollments (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL,
  PRIMARY KEY (team, training, email),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE enrollments, program_roles, programs, team_budgets, team_webhooks, mentors,
  team_members, teams, trainings, admins, quotes, schedules, settings;
