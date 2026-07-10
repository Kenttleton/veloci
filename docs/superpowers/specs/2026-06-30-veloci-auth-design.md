# Veloci — Auth Service Design

**Date:** 2026-06-30
**Status:** Approved
**Scope:** veloci-auth service — credential management, token minting and validation, invite lifecycle, startup sync

---

## 1. Overview

`veloci-auth` is a minimal credential and token service. It has two jobs:

1. **Credential management** — store hashed passwords, validate credentials on login, update hashes on password change or reset
2. **Token lifecycle** — mint signed JWTs, store them, validate them, revoke them

It does not own users, entities, roles, or permissions. It does not interpret claims. It treats the `claims` object as opaque JSONB — whatever veloci-api passes in gets stored and returned verbatim. All identity context and access control logic lives in veloci-api.

**Architectural intent:** veloci-auth is a deliberate seam, not a permanent solution. It exists to get username/password authentication working immediately without requiring SSO or OAuth provider integrations. veloci-api holds all user profile data and constructs all claims — auth only signs, stores, and validates tokens. When SSO providers (Google, etc.) are added in v2, veloci-auth is replaced or supplemented at the auth layer only; veloci-api requires no changes because it does not care how tokens are minted, only that valid ones come back. Designs that couple veloci-api to veloci-auth's internals work against this goal.

---

## 2. Database

`veloci-auth` connects exclusively to the `veloci_auth` Postgres database. It has no access to `veloci_app`.

```text
veloci_auth DB  (user: veloci_auth_user)
  auth_credentials
  tokens
  invite_tokens
```

The `veloci_app` database is owned by veloci-api and veloci-engine. No cross-database queries exist between the two.

---

## 3. Data Model

### auth_credentials

One row per registered user. Stores the minimum needed to verify identity.

```sql
CREATE TABLE auth_credentials (
  id            UUID PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,              -- bcrypt, cost factor 12
  system_role   TEXT NOT NULL DEFAULT 'user'
                CHECK (system_role IN ('server_admin', 'user')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

`system_role` is a credential-level concern: `server_admin` is seeded from the config file at startup. All other users are `user`. veloci-api reads `system_role` from validated token claims to gate server administration routes.

### tokens

One row per active issued token. Revocation is deletion.

```sql
CREATE TABLE tokens (
  id         UUID PRIMARY KEY,
  user_id    UUID NOT NULL REFERENCES auth_credentials(id) ON DELETE CASCADE,
  jti        TEXT NOT NULL UNIQUE,          -- JWT ID; index lookup on validation
  claims     JSONB NOT NULL,               -- opaque; set by veloci-api at mint time
  token_type TEXT NOT NULL DEFAULT 'access'
             CHECK (token_type IN ('access', 'refresh')),
  parent_id  UUID REFERENCES tokens(id),   -- refresh→access chain for audit + cascade revoke
  rotated_at TIMESTAMPTZ,                  -- set when this refresh token is rotated; replay detection after 30s
  issued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ON tokens (jti);
CREATE INDEX ON tokens (user_id);
CREATE INDEX ON tokens (expires_at);        -- for periodic cleanup of expired rows
```

`ON DELETE CASCADE` on `user_id` means deleting an `auth_credentials` row removes all their tokens automatically. `parent_id` cascade means revoking a refresh token row removes all access tokens descended from it — logout via `DELETE /tokens/:jti` on the refresh JTI cleans up the full session chain.

### invite_tokens

One row per outstanding invite. Consumed on acceptance. This is the reference shape for all one-time-use (OTU) token tables — password reset, permissions change, and any future flow each follow the same pattern with their own table.

```sql
CREATE TABLE invite_tokens (
  id           UUID PRIMARY KEY,
  token_hash   TEXT NOT NULL UNIQUE,        -- SHA-256 of raw 32-byte URL-safe base64 token
  created_by   UUID NOT NULL REFERENCES auth_credentials(id) ON DELETE CASCADE,
  claims       JSONB NOT NULL,              -- opaque; set by veloci-api at creation time
  expires_at   TIMESTAMPTZ NOT NULL,
  accepted_at  TIMESTAMPTZ                  -- null until consumed
);

CREATE INDEX ON invite_tokens (token_hash);
```

The raw token is a cryptographically random 32-byte value encoded as URL-safe base64. Only the SHA-256 hash is stored — the raw token is never persisted.

`ON DELETE CASCADE` on `created_by` means deleting a credential automatically invalidates any OTU tokens they created.

**OTU table pattern:** each flow gets its own table rather than a shared `otu_tokens` table. This keeps the CHECK constraint off the hot-path `tokens` table, preserves the `accepted_at` audit trail per flow, and means adding a new flow requires only a new migration — no schema change to existing tables. Future tables (`password_reset_tokens`, `permissions_change_tokens`) follow the same shape.

---

## 4. Startup Config Sync

veloci-auth reads a config file at startup via Viper and syncs the server admin credentials. This is the only mechanism for server admin password management — no endpoint, no CLI exec required.

```yaml
# veloci-auth.yaml (bind-mounted host volume)
server_admin:
  email: admin@example.com
  password: plaintextpassword        # readable on the host; bcrypt hash stored in DB
jwt_secret: <long-random-string>     # HS256 signing key; rotate by changing and restarting
port: 8081
session:
  access_token_ttl_minutes: 15       # how long an access JWT is valid
  refresh_token_ttl_hours: 24        # how long a refresh JWT is valid
invite:
  ttl_hours: 72                      # invite link expiry
```

If `session` is absent from config, auth uses hardcoded defaults: 15-minute access tokens, 24-hour refresh tokens.

**Sync logic on every startup:**

1. Read `server_admin.email` and `server_admin.password` from config
2. Query `auth_credentials` for a row with that email and `system_role = 'server_admin'`
3. Not found → create the row, bcrypt the password at cost 12
4. Found, `bcrypt.CompareHashAndPassword` fails → update `password_hash` with new bcrypt hash
5. Found, hash matches → no-op

**Password reset for server admin:** edit `password` in the config file, restart the container. The next startup sync re-hashes and updates the DB. No other action needed.

**JWT secret warning:** at startup, auth logs a prominent warning if `jwt_secret` matches known placeholder patterns or appears low-entropy (all lowercase, no digits or symbols). The warning is non-fatal — local dev can run with a weak secret, but self-hosters get a clear signal to rotate before exposing the service to a network.

**DB connection retry:** after `pgxpool.New`, auth pings postgres with exponential backoff before running the admin sync or accepting HTTP traffic. Initial delay 500ms, doubles each attempt, capped at 30s per attempt, maximum 2 minutes total. If postgres is not reachable within 2 minutes, the process exits with a fatal log. Uses `cenkalti/backoff/v4` — the same library used by veloci-api and veloci-engine for consistency.

---

## 5. Endpoints

All endpoints are internal. veloci-auth is not exposed outside the Docker network. veloci-api is the only caller.

### Credentials

```text
POST   /credentials/validate
POST   /credentials/create
PUT    /credentials/:id/password
DELETE /credentials/:id
```

#### POST /credentials/validate

Verify email and password. Returns the credential ID on success.

Request:

```json
{ "email": "user@example.com", "password": "plaintext" }
```

Response `200`:

```json
{ "credential_id": "uuid", "system_role": "server_admin | user" }
```

Response `401`: invalid credentials (same response shape for wrong email or wrong password — no enumeration)

---

#### POST /credentials/create

Create a new credential record. Called by veloci-api during user creation or invite acceptance.

Request:

```json
{ "email": "user@example.com", "password": "plaintext" }
```

Response `201`:

```json
{ "credential_id": "uuid" }
```

Response `409`: email already registered

Response `500`: `{"code":"INTERNAL_ERROR"}` — any non-conflict DB error (connection failure, timeout, etc.)

---

#### PUT /credentials/:id/password

Update the password hash. Called by veloci-api when an entity admin resets a user's password.

Request:

```json
{ "password": "newplaintext" }
```

Response `204`: no body

---

#### DELETE /credentials/:id

Permanently remove a credential and all associated tokens via FK cascade (`tokens.user_id ON DELETE CASCADE`). Called by veloci-api on full user deletion — the user is gone and their sessions go with them.

**Do not use this to force a logout while keeping the account.** Use `DELETE /tokens/user/:credential_id` for that purpose.

Response `204`: no body

Response `403`: `{"code":"FORBIDDEN","reason":"cannot delete server_admin credential"}` — auth guards against deleting the server admin credential; only veloci-api can trigger this and it should never target an admin credential

---

### Tokens

```text
POST   /tokens/mint
POST   /tokens/refresh
POST   /tokens/validate
DELETE /tokens/:jti
DELETE /tokens/user/:credential_id
```

#### Token types

`POST /tokens/validate` is the single validation endpoint for all token types. Auth detects the token type from its structure:

- **Session tokens** are JWTs — three dot-separated base64url segments
- **OTU tokens** are raw 32-byte URL-safe base64 strings — no dots, no structure

Auth routes to the appropriate table internally. The caller passes the raw token; the response always includes `token_type` so veloci-api can select the correct middleware and claim parsing path.

#### POST /tokens/mint

Create, sign, and store a JWT. The `claims` object is passed in by veloci-api — auth stores it verbatim and embeds it in the signed JWT.

Request:

```json
{
  "credential_id": "uuid",
  "claims": {
    "sub": "user_id",
    "email": "user@example.com",
    "system_role": "user",
    "entity_id": "uuid",
    "entity_role": "entity_admin"
  }
}
```

Response `201`:

```json
{
  "access_token":  "eyJ...",
  "refresh_token": "eyJ...",
  "jti":           "uuid",
  "expires_in":    900,
  "expires_at":    "2026-06-30T15:15:00Z"
}
```

Auth adds `jti`, `iat`, `exp`, and `token_type` to the claims before signing. Access token lifetime is `session.access_token_ttl_minutes` (default 15 minutes). Refresh token lifetime is `session.refresh_token_ttl_hours` (default 24 hours). Both tokens are issued atomically — there is no separate refresh mint endpoint.

`claims` must be a non-null JSON object. An empty object `{}` is valid. A JSON `null` is rejected with `400`.

---

#### POST /tokens/validate

Validate any token — session or OTU. Auth detects the token type from its structure, queries the appropriate table, and returns a typed response. veloci-api uses `token_type` to select the correct middleware and claim parsing path.

Request:

```json
{ "token": "<raw token — JWT or OTU>" }
```

Response `200` — access token:

```json
{
  "token_type": "access",
  "jti": "uuid",
  "credential_id": "uuid",
  "claims": {
    "sub": "user_id",
    "email": "user@example.com",
    "system_role": "user",
    "entity_id": "uuid",
    "entity_role": "entity_admin"
  }
}
```

Response `200` — invite token:

```json
{
  "token_type": "invite",
  "claims": {
    "email": "invited@example.com",
    "entity_id": "uuid",
    "entity_role": "entity_user"
  }
}
```

Refresh tokens are never presented to this endpoint — they are only valid at `POST /tokens/refresh`. `token_type` values match the JWT claim values injected at mint time.

Response `401`: invalid token, not in DB, already consumed (`accepted_at` set), or expired

---

#### POST /tokens/refresh

Present a valid refresh token to receive a new access+refresh pair. The old refresh token is rotated out atomically within a 30-second grace window to handle concurrent requests (two-tab race condition).

Request:

```json
{ "refresh_token": "eyJ..." }
```

Response `200`:

```json
{
  "access_token":  "eyJ...",
  "refresh_token": "eyJ...",
  "jti":           "uuid-of-new-access-token",
  "expires_in":    900,
  "expires_at":    "2026-06-30T16:15:00Z"
}
```

Response `401 REFRESH_TOKEN_INVALID`: unrecognized, bad signature, or presented outside the 30-second grace window after rotation

Response `401 REFRESH_TOKEN_EXPIRED`: expired

Rotation sequence (single transaction):

1. Look up refresh token row by JTI
2. If `rotated_at` is set and `NOW() - rotated_at > 30s` → reject as replay
3. Stamp `rotated_at = NOW()` on the old refresh row
4. Insert new refresh token row with `parent_id = old_refresh.id`
5. Insert new access token row with `parent_id = new_refresh.id`
6. Commit

veloci-api calls this transparently on receiving a `401` from `/tokens/validate`, then retries the original request with the new access token.

---

#### DELETE /tokens/:jti

Revoke a single token. On logout, pass the refresh token JTI — the `parent_id` cascade deletes all access tokens descended from it. Always returns `204` regardless of whether the token existed (idempotent).

Response `204`: no body

---

#### DELETE /tokens/user/:credential_id

Revoke all active sessions for a user **without deleting their credential**. Use this for security responses — forced logout while preserving the account (e.g. a self-hoster revoking a shared or restricted account's sessions after a suspected breach, or a parent locking out a child's read-only account). The user can log back in immediately with their existing password.

This is distinct from `DELETE /credentials/:id`, which removes the account permanently and cascades tokens as a side effect. These two endpoints serve different goals and must not be used interchangeably.

Response `204`: no body

---

### Invite Tokens

```text
POST   /invite
POST   /invite/consume
```

Validation of invite tokens goes through `POST /tokens/validate` — auth detects the token type by structure (raw base64url vs JWT) and queries `invite_tokens` internally.

Future OTU flows (password reset, permissions change) follow the same pattern with their own tables and endpoint prefixes. Each is independent — no shared OTU endpoint.

#### POST /invite

Create an invite token. TTL is taken from `invite.ttl_hours` in config (default 72 hours if absent).

Request:

```json
{
  "created_by": "credential_id",
  "claims": {
    "email": "invited@example.com",
    "entity_id": "uuid",
    "entity_role": "entity_user"
  }
}
```

Response `201`:

```json
{ "token": "<raw-url-safe-base64>", "expires_at": "2026-07-12T12:00:00Z" }
```

The raw token is returned once and never stored. veloci-api embeds it in the invite link sent to the recipient.

---

#### POST /invite/consume

Atomically consume an invite token after the registration flow completes. Auth sets `accepted_at` only if the token is still unconsumed (`UPDATE ... WHERE accepted_at IS NULL RETURNING *`). The consume step is the commit point in the invite saga: validate → create credential → create user → **consume invite** → mint session.

Request:

```json
{ "token": "<raw-url-safe-base64>" }
```

Response `204`: consumed successfully

Response `409`: already consumed

Response `410`: expired

---

## 6. JWT Signing

- Algorithm: **HS256**
- Secret: `jwt_secret` from Viper config file
- Access token lifetime: `session.access_token_ttl_minutes` (default **15 minutes**)
- Refresh token lifetime: `session.refresh_token_ttl_hours` (default **24 hours**)
- `jti`: UUID generated by veloci-auth at mint time
- `iat`, `exp`, `token_type`: set by veloci-auth at mint time; not accepted from the caller

`token_type` is injected into the JWT claims by auth on every mint:

| Token           | `token_type` value |
| --------------- | ------------------ |
| Session access  | `"access"`         |
| Session refresh | `"refresh"`        |
| Invite          | `"invite"`         |

The `POST /tokens/validate` response includes `token_type` in the envelope so veloci-api can select the correct middleware without inspecting claim fields. Refresh tokens are never routed through `/tokens/validate` — they are only valid at `POST /tokens/refresh`.

Rotating the signing secret invalidates all active tokens (all existing JWTs will fail signature verification). This is intentional — rotating the secret is the mechanism for a full forced re-login of all users.

---

## 7. Cobra Subcommands

```text
veloci-auth serve     — start the HTTP server (default and only runtime subcommand)
```

No other subcommands. Server admin management is handled entirely through the config file and startup sync, not CLI commands.

---

## 8. Out of Scope for This Spec

- Claims semantics, RBAC, and permission enforcement (veloci-api spec)
- User profiles, entities, entity membership (veloci-api spec)
- Email delivery for invite links (v2)
- SSO / OAuth2 provider integration (v2)
- Multi-factor authentication (v2)
- Audit logging of auth events (v2)
- Token cleanup job for expired rows (implementation detail)
