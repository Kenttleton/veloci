-- migrations/app/002_rbac_seed.sql
INSERT INTO roles (name) VALUES ('entity_admin'), ('entity_user');

INSERT INTO permissions (name) VALUES
  ('accounts:read'),
  ('accounts:write'),
  ('import:create'),
  ('rules:write'),
  ('labels:write'),
  ('entries:write'),
  ('reports:read'),
  ('users:manage'),
  ('entity:configure');

-- entity_admin gets all permissions
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.name = 'entity_admin';

-- entity_user gets read + labels + reports
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r
JOIN permissions p ON p.name IN ('accounts:read', 'labels:write', 'reports:read')
WHERE r.name = 'entity_user';
