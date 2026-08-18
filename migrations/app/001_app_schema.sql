-- migrations/app/001_app_schema.sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- label_id: no FK constraint here — labels.entity_id references entities,
-- creating a circular dependency. Referential integrity is maintained at the
-- application layer by EnsureSystemLabels, which sets label_id immediately
-- after seeding the entity identity label.
CREATE TABLE entities (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  label_id   UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── LABELS ──────────────────────────────────────────────────────────────────
-- Moved here (before 002) so entities.label_id can be populated immediately
-- after EnsureSystemLabels seeds the entity identity label.
-- source='system': engine-managed, immutable via normal label API.
--   Includes entity identity label, Income, Spend, All, and pipeline stage labels.
-- source='user': user-created.

CREATE TABLE labels (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id  UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  name       TEXT        NOT NULL,
  source     TEXT        NOT NULL DEFAULT 'engine'
             CHECK (source IN ('user', 'engine', 'system')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);

CREATE INDEX ON labels (entity_id);

-- Maps pipeline stage numbers to a label UUID so the engine can emit label_id
-- in pg_notify payloads without ever referencing label names.
-- Seeded per entity by EnsureSystemLabels alongside the system labels.
CREATE TABLE pipeline_stage_labels (
  stage_num  INTEGER NOT NULL,
  entity_id  UUID    NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  label_id   UUID    NOT NULL REFERENCES labels(id)   ON DELETE CASCADE,
  PRIMARY KEY (stage_num, entity_id)
);

CREATE INDEX ON pipeline_stage_labels (entity_id);

CREATE TABLE users (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  auth_credential_id UUID        NOT NULL UNIQUE,
  email              TEXT        NOT NULL UNIQUE,
  first_name         TEXT        NOT NULL DEFAULT '',
  last_name          TEXT        NOT NULL DEFAULT '',
  preferred_name     TEXT        NOT NULL DEFAULT '',
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
