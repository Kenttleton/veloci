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
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     UUID        NOT NULL REFERENCES auth_credentials(id) ON DELETE CASCADE,
  jti         TEXT        NOT NULL UNIQUE,
  token_type  TEXT        NOT NULL DEFAULT 'access'
              CHECK (token_type IN ('access', 'refresh')),
  -- links an access token back to the refresh token that issued it;
  -- cascade delete means revoking a refresh token kills all its access tokens
  parent_id   UUID        REFERENCES tokens(id) ON DELETE CASCADE,
  claims      JSONB       NOT NULL,
  issued_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at  TIMESTAMPTZ NOT NULL,
  -- set when this refresh token is rotated; allows a short grace window
  -- so two-tab concurrent rotation requests don't force a re-login
  rotated_at  TIMESTAMPTZ
);

CREATE INDEX ON tokens (jti);
CREATE INDEX ON tokens (user_id);
CREATE INDEX ON tokens (expires_at);
CREATE INDEX ON tokens (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE invite_tokens (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash  TEXT        NOT NULL UNIQUE,
  created_by  UUID        NOT NULL REFERENCES auth_credentials(id) ON DELETE CASCADE,
  claims      JSONB       NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  accepted_at TIMESTAMPTZ
);

CREATE INDEX ON invite_tokens (token_hash);
