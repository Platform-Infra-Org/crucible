-- +goose Up
-- Teams, tags and training roles (docs/superpowers/specs/2026-10-11-teams-and-training-roles-design.md).

-- Tags: member becomes junior, a team may have several leaders.
ALTER TABLE team_members DROP CONSTRAINT team_members_role_check;
DROP INDEX team_one_leader;

-- One team per person: keep the strongest membership, then the first team by id.
DELETE FROM team_members m USING (
  SELECT team, email, row_number() OVER (PARTITION BY email ORDER BY
    CASE role WHEN 'leader' THEN 0 WHEN 'senior' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, team) AS n
  FROM team_members) d
WHERE m.team = d.team AND m.email = d.email AND d.n > 1;
UPDATE team_members SET role = 'junior' WHERE role = 'member';
ALTER TABLE team_members ADD CONSTRAINT team_members_role_check CHECK (role IN ('leader', 'senior', 'junior', 'trainee'));
CREATE UNIQUE INDEX team_members_one_team ON team_members(email);

-- A team that lost its only leader to another team promotes its strongest remaining member.
UPDATE team_members m SET role = 'leader' FROM (
  SELECT DISTINCT ON (team) team, email FROM team_members t
  WHERE NOT EXISTS (SELECT 1 FROM team_members l WHERE l.team = t.team AND l.role = 'leader')
  ORDER BY team, CASE role WHEN 'senior' THEN 0 WHEN 'junior' THEN 1 ELSE 2 END, email) p
WHERE m.team = p.team AND m.email = p.email;

-- Mentor pairs whose people left the team.
DELETE FROM mentors x
WHERE NOT EXISTS (SELECT 1 FROM team_members m WHERE m.team = x.team AND m.email = x.trainee AND m.role = 'trainee')
   OR NOT EXISTS (SELECT 1 FROM team_members m WHERE m.team = x.team AND m.email = x.mentor AND m.role <> 'trainee');

-- A training's roles, given to people (emails) and groups (all, all_seniors, <team>, <team>_seniors, …).
CREATE TABLE training_roles (
  training text NOT NULL REFERENCES trainings(id) ON DELETE CASCADE,
  ref      text NOT NULL CHECK (ref = lower(btrim(ref)) AND ref <> ''),
  role     text NOT NULL CHECK (role IN ('participant', 'editor', 'admin')),
  PRIMARY KEY (training, ref, role)
);
INSERT INTO training_roles (training, ref, role)
  SELECT DISTINCT training, email, 'participant' FROM enrollments ON CONFLICT DO NOTHING;
INSERT INTO training_roles (training, ref, role)
  SELECT DISTINCT training, email, CASE role WHEN 'scorer' THEN 'editor' ELSE 'admin' END FROM program_roles
  ON CONFLICT DO NOTHING;

-- One run per training: keep the team with the most participants (then the first by id) as its owner.
DELETE FROM programs p USING (
  SELECT p.team, p.training, row_number() OVER (PARTITION BY p.training ORDER BY
    (SELECT count(*) FROM enrollments e WHERE e.team = p.team AND e.training = p.training) DESC, p.team) AS n
  FROM programs p) d
WHERE p.team = d.team AND p.training = d.training AND d.n > 1;
ALTER TABLE programs ADD CONSTRAINT programs_one_run UNIQUE (training);

-- The owning team's leaders and seniors scored and could propose edits before: they stay editors.
INSERT INTO training_roles (training, ref, role)
  SELECT training, team || '_leaders', 'editor' FROM programs
  UNION SELECT training, team || '_seniors', 'editor' FROM programs
  ON CONFLICT DO NOTHING;

DROP TABLE enrollments, program_roles;

-- +goose Down
CREATE TABLE program_roles (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL CHECK (email = lower(btrim(email))),
  role     text NOT NULL CHECK (role IN ('manager', 'scorer', 'approver')),
  PRIMARY KEY (team, training, email, role),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);
CREATE TABLE enrollments (
  team     text NOT NULL,
  training text NOT NULL,
  email    text NOT NULL CHECK (email = lower(btrim(email))),
  PRIMARY KEY (team, training, email),
  FOREIGN KEY (team, training) REFERENCES programs(team, training) ON DELETE CASCADE
);
-- Only roles given to people come back; group refs have no equivalent.
INSERT INTO enrollments (team, training, email)
  SELECT p.team, r.training, r.ref FROM training_roles r JOIN programs p ON p.training = r.training
  WHERE r.role = 'participant' AND r.ref LIKE '%@%';
INSERT INTO program_roles (team, training, email, role)
  SELECT p.team, r.training, r.ref, x.role FROM training_roles r JOIN programs p ON p.training = r.training
  CROSS JOIN (VALUES ('manager'), ('approver')) x(role)
  WHERE r.role = 'admin' AND r.ref LIKE '%@%'
  UNION SELECT p.team, r.training, r.ref, 'scorer' FROM training_roles r JOIN programs p ON p.training = r.training
  WHERE r.role = 'editor' AND r.ref LIKE '%@%';
DROP TABLE training_roles;
ALTER TABLE programs DROP CONSTRAINT programs_one_run;
DROP INDEX team_members_one_team;
ALTER TABLE team_members DROP CONSTRAINT team_members_role_check;
UPDATE team_members SET role = 'member' WHERE role = 'junior';
-- One leader per team again: the first by email stays, the others become seniors.
UPDATE team_members m SET role = 'senior' FROM (
  SELECT team, email, row_number() OVER (PARTITION BY team ORDER BY email) AS n FROM team_members WHERE role = 'leader') d
WHERE m.team = d.team AND m.email = d.email AND d.n > 1;
ALTER TABLE team_members ADD CONSTRAINT team_members_role_check CHECK (role IN ('leader', 'senior', 'member', 'trainee'));
CREATE UNIQUE INDEX team_one_leader ON team_members(team) WHERE role = 'leader';
