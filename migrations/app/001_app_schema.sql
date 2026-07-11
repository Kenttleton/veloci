-- migrations/app/001_app_schema.sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE entities (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE users (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  auth_credential_id UUID        NOT NULL UNIQUE,
  email              TEXT        NOT NULL UNIQUE,
  name               TEXT        NOT NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE roles (
  id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE permissions (
  id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE role_permissions (
  role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE entity_users (
  user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  entity_id   UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  entity_role TEXT NOT NULL CHECK (entity_role IN ('entity_admin', 'entity_user')),
  PRIMARY KEY (user_id, entity_id)
);

GRANT ALL ON ALL TABLES IN SCHEMA public TO veloci_app_user;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO veloci_app_user;
