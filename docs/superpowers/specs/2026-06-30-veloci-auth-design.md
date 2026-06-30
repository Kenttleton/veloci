# Veloci — Auth Service Design

**Date:** 2026-06-30
**Status:** Approved
**Scope:** veloci-auth service — credential management, token minting and validation, invite lifecycle, startup sync

---

## 1. Overview

`veloci-auth` is a minimal crypto and token service. It has two jobs:

1. **Credential management** — store hashed passwords, validate credentials on login, update hashes on password change or reset
2. **Token lifecycle** — mint signed JWTs, store them, validate them, revoke them

It does not own users, entities, roles, or permissions. It does not interpret claims. It treats the `claims` object as opaque JSONB — whatever veloci-api passes in gets stored and returned verbatim. All identity context and access control logic lives in veloci-api.

---

## 2. Database

`veloci-auth` connects exclusively to the `veloci_auth` Postgres database. It has no access to `veloci_app`.

```
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
  issued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ON tokens (jti);
CREATE INDEX ON tokens (user_id);
CREATE INDEX ON tokens (expires_at);        -- for periodic cleanup of expired rows
```

`ON DELETE CASCADE` on `user_id` means deleting an `auth_credentials` row removes all their tokens automatically.

### invite_tokens

One row per outstanding invite. Consumed on acceptance.

```sql
CREATE TABLE invite_tokens (
  id           UUID PRIMARY KEY,
  token_hash   TEXT NOT NULL UNIQUE,        -- SHA-256 of the raw token sent in the URL
  created_by   UUID NOT NULL REFERENCES auth_credentials(id),
  claims       JSONB NOT NULL,              -- opaque; set by veloci-api at creation time
  expires_at   TIMESTAMPTZ NOT NULL,
  accepted_at  TIMESTAMPTZ                  -- null until consumed
);

CREATE INDEX ON invite_tokens (token_hash);
```

The raw token is a cryptographically random 32-byte value encoded as URL-safe base64. Only the SHA-256 hash is stored — the raw token is never persisted.

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
```

**Sync logic on every startup:**

1. Read `server_admin.email` and `server_admin.password` from config
2. Query `auth_credentials` for a row with that email and `system_role = 'server_admin'`
3. Not found → create the row, bcrypt the password at cost 12
4. Found, `bcrypt.CompareHashAndPassword` fails → update `password_hash` with new bcrypt hash
5. Found, hash matches → no-op

**Password reset for server admin:** edit `password` in the config file, restart the container. The next startup sync re-hashes and updates the DB. No other action needed.

---

## 5. Endpoints

All endpoints are internal. veloci-auth is not exposed outside the Docker network. veloci-api is the only caller.

### Credentials

```
POST   /credentials/validate
POST   /credentials/create
PUT    /credentials/:id/password
DELETE /credentials/:id
```

**POST /credentials/validate**

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

**POST /credentials/create**

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

---

**PUT /credentials/:id/password**

Update the password hash. Called by veloci-api when an entity admin resets a user's password.

Request:
```json
{ "password": "newplaintext" }
```

Response `204`: no body

---

**DELETE /credentials/:id**

Remove credentials and all associated tokens (cascades). Called by veloci-api on user deletion.

Response `204`: no body

---

### Tokens

```
POST   /tokens/mint
POST   /tokens/validate
DELETE /tokens/:jti
DELETE /tokens/user/:credential_id
```

**POST /tokens/mint**

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
  "token": "eyJ...",
  "jti": "uuid",
  "expires_at": "2026-06-30T15:00:00Z"
}
```

The service adds `jti`, `iat`, and `exp` to the claims before signing. Token lifetime is fixed at 60 minutes.

---

**POST /tokens/validate**

Verify the JWT signature, check the token exists in the DB (not logged out), check it is not expired. Returns the stored claims on success.

Request:
```json
{ "token": "eyJ..." }
```

Response `200`:
```json
{
  "jti": "uuid",
  "credential_id": "uuid",
  "claims": { ... }
}
```

Response `401`: invalid signature, token not in DB, or expired

---

**DELETE /tokens/:jti**

Revoke a single token. Called on logout or token refresh (old token out before new token in).

Response `204`: no body

---

**DELETE /tokens/user/:credential_id**

Revoke all tokens for a user. Called by veloci-api when a user is removed from an entity.

Response `204`: no body

---

### Invite Tokens

```
POST   /invite
GET    /invite/:token
DELETE /invite/:token
```

**POST /invite**

Create an invite token. The `claims` object is set by veloci-api and will be used to build the new user's session when the invite is accepted.

Request:
```json
{
  "created_by": "credential_id",
  "claims": {
    "entity_id": "uuid",
    "entity_role": "entity_user"
  },
  "ttl_hours": 72
}
```

Response `201`:
```json
{ "token": "<raw-url-safe-base64>", "expires_at": "2026-07-03T12:00:00Z" }
```

The raw token is returned once and never stored. veloci-api builds the invite URL from it.

---

**GET /invite/:token**

Validate the invite token (not expired, not already accepted) and return its claims.

Response `200`:
```json
{ "claims": { "entity_id": "uuid", "entity_role": "entity_user" } }
```

Response `404`: token not found, already accepted, or expired

---

**DELETE /invite/:token**

Consume the invite token after successful acceptance. Sets `accepted_at`.

Response `204`: no body

---

## 6. JWT Signing

- Algorithm: **HS256**
- Secret: `jwt_secret` from Viper config file
- Token lifetime: **60 minutes** (hardcoded; not configurable per-token)
- `jti`: UUID generated by veloci-auth at mint time
- `iat` and `exp`: set by veloci-auth at mint time; not accepted from the caller

Rotating the signing secret invalidates all active tokens (all existing JWTs will fail signature verification). This is intentional — rotating the secret is the mechanism for a full forced re-login of all users.

---

## 7. Cobra Subcommands

```
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
