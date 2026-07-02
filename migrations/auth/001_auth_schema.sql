-- migrations/auth/001_auth_schema.sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE auth_credentials (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  email         TEXT        NOT NULL UNIQUE,
  password_hash TEXT        NOT NULL,
  system_role   TEXT        NOT NULL DEFAULT 'user'
                            CHECK (system_role IN ('server_admin', 'user')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE tokens (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id    UUID        NOT NULL REFERENCES auth_credentials(id) ON DELETE CASCADE,
  jti        TEXT        NOT NULL UNIQUE,
  claims     JSONB       NOT NULL,
  issued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ON tokens (jti);
CREATE INDEX ON tokens (user_id);
CREATE INDEX ON tokens (expires_at);

CREATE TABLE invite_tokens (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash  TEXT        NOT NULL UNIQUE,
  created_by  UUID        NOT NULL REFERENCES auth_credentials(id),
  claims      JSONB       NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  accepted_at TIMESTAMPTZ
);

CREATE INDEX ON invite_tokens (token_hash);
