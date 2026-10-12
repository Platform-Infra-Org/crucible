-- +goose Up
-- Managing a training used to come from being on the owning team and not a trainee (rbac.ManageProgram), so a
-- training could name nobody and still be managed. It is now the named admins only, which makes removing one mean
-- something — so the people who manage a training today are written down before that rule takes effect.
--
-- A training that already names admins keeps them and gains the owning team's other working ranks, so nobody loses
-- what they have. One that names none gets the team's seniors and leaders, which is what registering as a senior
-- would give it today.
INSERT INTO training_roles (training, ref, role)
SELECT p.training, p.team || s.suffix, 'admin'
  FROM programs p
  CROSS JOIN (VALUES ('_leaders'), ('_seniors'), ('_juniors')) AS s(suffix)
 WHERE EXISTS (SELECT 1 FROM training_roles r WHERE r.training = p.training AND r.role = 'admin')
ON CONFLICT DO NOTHING;

INSERT INTO training_roles (training, ref, role)
SELECT p.training, p.team || s.suffix, 'admin'
  FROM programs p
  CROSS JOIN (VALUES ('_leaders'), ('_seniors')) AS s(suffix)
 WHERE NOT EXISTS (SELECT 1 FROM training_roles r WHERE r.training = p.training AND r.role = 'admin')
ON CONFLICT DO NOTHING;

-- +goose Down
-- The rank groups this added are indistinguishable from ones somebody chose, so Down leaves them: with the old rule
-- back, an extra admin group grants what being on the team granted anyway.
SELECT 1;
