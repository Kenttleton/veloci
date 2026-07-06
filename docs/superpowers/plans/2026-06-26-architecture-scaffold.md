# Veloci Architecture Scaffold — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the six-service architecture with working auth flow, per-request token validation, RabbitMQ job publishing/consuming, and entity-scoped API scaffolding.

**Architecture:** Monorepo under `services/`. Two isolated Postgres databases (`veloci_auth`, `veloci_app`) with separate DB users. `veloci-auth` is internal-only — never exposed to the frontend. `veloci-api` owns all client-facing routes including login; it calls `veloci-auth` HTTP endpoints to validate credentials and mint/validate tokens on every protected request.

**Tech Stack:**

- Go 1.26 — `chi/v5` router, `pgx/v5` Postgres driver, `amqp091-go` RabbitMQ client, `golang-jwt/jwt/v5`, `golang.org/x/crypto` bcrypt, `cobra`, `viper`
- Rust 1.95 — `tokio`, `sqlx`, `lapin`, `serde`/`serde_json`, `anyhow`, `tracing`, `backon`, `chrono`, `futures-lite`
- React 19 + Vite 8 + TypeScript 6.0
- Postgres 18, RabbitMQ 4.3-alpine

## Global Constraints

- Two Postgres databases: `veloci_auth` (owned by `veloci_auth_user`) and `veloci_app` (owned by `veloci_app_user`) — no cross-database queries
- `veloci-auth` is Docker-network-internal only — no exposed host port
- JWT payload carries: `sub`, `email`, `system_role`, `entity_id`, `entity_role` — claims are opaque JSONB in `veloci-auth`
- Passwords: bcrypt cost 12
- All Go services use `chi/v5`; all Rust async uses tokio multi-thread runtime
- `veloci-auth` reads `veloci-auth.yaml` via Viper for `server_admin` credentials and `jwt_secret`
- `veloci-api` calls `veloci-auth POST /tokens/validate` on every protected request — no local JWT parsing
- All financial table rows carry `entity_id UUID NOT NULL`
- No Postgres or RabbitMQ ports exposed externally

---

## File Map

```text
veloci/
├── docker-compose.yml
├── .env.example
├── config/
│   └── veloci-auth.yaml.example
├── scripts/
│   └── init-db.sh
├── migrations/
│   ├── auth/
│   │   └── 001_auth_schema.sql
│   └── app/
│       ├── 001_app_schema.sql
│       ├── 002_financial_schema.sql
│       └── 002_rbac_seed.sql
└── services/
    ├── auth/
    │   ├── go.mod
    │   ├── main.go
    │   ├── Dockerfile
    │   └── internal/
    │       ├── tokens/
    │       │   ├── jwt.go
    │       │   └── jwt_test.go
    │       ├── db/
    │       │   └── db.go
    │       ├── sync/
    │       │   └── admin.go
    │       └── handlers/
    │           ├── credentials.go
    │           ├── credentials_test.go
    │           ├── helpers.go
    │           ├── tokens.go
    │           └── tokens_test.go
    ├── api/
    │   ├── go.mod
    │   ├── main.go
    │   ├── Dockerfile
    │   └── internal/
    │       ├── authclient/
    │       │   ├── client.go
    │       │   └── client_test.go
    │       ├── middleware/
    │       │   ├── auth.go
    │       │   └── auth_test.go
    │       ├── queue/
    │       │   ├── publisher.go
    │       │   └── publisher_test.go
    │       └── handlers/
    │           ├── auth.go
    │           ├── auth_test.go
    │           └── health.go
    ├── engine/
    │   ├── Cargo.toml
    │   ├── Dockerfile
    │   └── src/
    │       ├── main.rs
    │       ├── consumer.rs
    │       ├── db.rs
    │       ├── health.rs
    │       └── jobs/
    │           └── mod.rs
    └── web/
        ├── package.json
        ├── vite.config.ts
        ├── tsconfig.json
        ├── index.html
        ├── nginx.conf
        ├── Dockerfile
        └── src/
            ├── main.tsx
            ├── App.tsx
            ├── api/
            │   └── client.ts
            └── auth/
                ├── AuthProvider.tsx
                └── LoginPage.tsx
```

---

## Task 1: Infrastructure

**Files:**

- Create: `.env.example`
- Create: `config/veloci-auth.yaml.example`
- Create: `scripts/init-db.sh`
- Create: `migrations/auth/001_auth_schema.sql`
- Create: `migrations/app/001_app_schema.sql`
- Create: `migrations/app/002_rbac_seed.sql`
- Create: `docker-compose.yml`

**Interfaces:**

- Produces: two isolated Postgres databases with correct schemas and seeded roles; RabbitMQ; environment contract for all services

---

- [ ] **Step 1: Create `.env.example`**

```bash
# .env.example
POSTGRES_USER=postgres
POSTGRES_PASSWORD=changeme

VELOCI_AUTH_DB=veloci_auth
VELOCI_AUTH_DB_USER=veloci_auth_user
VELOCI_AUTH_DB_PASSWORD=changeme_auth

VELOCI_APP_DB=veloci_app
VELOCI_APP_DB_USER=veloci_app_user
VELOCI_APP_DB_PASSWORD=changeme_app

RABBITMQ_USER=veloci
RABBITMQ_PASSWORD=changeme
```

Copy to `.env` before running:

```bash
cp .env.example .env
```

- [ ] **Step 2: Create `config/veloci-auth.yaml.example`**

```yaml
# config/veloci-auth.yaml.example
# Copy to config/veloci-auth.yaml and fill in values.
# This file is bind-mounted into the veloci-auth container.
# server_admin password is stored in plaintext here so self-hosters
# can recover it without CLI access. Only the bcrypt hash is stored in DB.
server_admin:
  email: admin@veloci.local
  password: changeme
jwt_secret: change-this-to-a-long-random-string-at-least-32-chars
port: 8081
```

```bash
mkdir -p config
cp config/veloci-auth.yaml.example config/veloci-auth.yaml
```

- [ ] **Step 3: Create `scripts/init-db.sh`**

```bash
#!/usr/bin/env bash
# scripts/init-db.sh
# Runs inside the postgres container as the superuser.
# Creates both databases and their dedicated users.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<-EOSQL
  CREATE USER ${VELOCI_AUTH_DB_USER} WITH PASSWORD '${VELOCI_AUTH_DB_PASSWORD}';
  CREATE DATABASE ${VELOCI_AUTH_DB} OWNER ${VELOCI_AUTH_DB_USER};

  CREATE USER ${VELOCI_APP_DB_USER} WITH PASSWORD '${VELOCI_APP_DB_PASSWORD}';
  CREATE DATABASE ${VELOCI_APP_DB} OWNER ${VELOCI_APP_DB_USER};
EOSQL

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_AUTH_DB_USER}" \
  --dbname "${VELOCI_AUTH_DB}" \
  -f /migrations/auth/001_auth_schema.sql

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_APP_DB_USER}" \
  --dbname "${VELOCI_APP_DB}" \
  -f /migrations/app/001_app_schema.sql

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_APP_DB_USER}" \
  --dbname "${VELOCI_APP_DB}" \
  -f /migrations/app/002_rbac_seed.sql
```

```bash
chmod +x scripts/init-db.sh
```

- [ ] **Step 4: Create `migrations/auth/001_auth_schema.sql`**

```sql
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
  created_by  UUID        NOT NULL REFERENCES auth_credentials(id),
  claims      JSONB       NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  accepted_at TIMESTAMPTZ
);

CREATE INDEX ON invite_tokens (token_hash);
```

- [ ] **Step 5: Create `migrations/app/001_app_schema.sql`**

```sql
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
```

- [ ] **Step 6: Create `migrations/app/002_rbac_seed.sql`**

```sql
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
```

- [ ] **Step 7: Create `docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      VELOCI_AUTH_DB: ${VELOCI_AUTH_DB}
      VELOCI_AUTH_DB_USER: ${VELOCI_AUTH_DB_USER}
      VELOCI_AUTH_DB_PASSWORD: ${VELOCI_AUTH_DB_PASSWORD}
      VELOCI_APP_DB: ${VELOCI_APP_DB}
      VELOCI_APP_DB_USER: ${VELOCI_APP_DB_USER}
      VELOCI_APP_DB_PASSWORD: ${VELOCI_APP_DB_PASSWORD}
    volumes:
      - postgres_data:/var/lib/postgresql
      - ./scripts/init-db.sh:/docker-entrypoint-initdb.d/init-db.sh
      - ./migrations:/migrations
    networks:
      - veloci
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER}"]
      interval: 5s
      retries: 5

  rabbitmq:
    image: rabbitmq:4.3-alpine
    environment:
      RABBITMQ_DEFAULT_USER: ${RABBITMQ_USER}
      RABBITMQ_DEFAULT_PASS: ${RABBITMQ_PASSWORD}
    volumes:
      - rabbitmq_data:/var/lib/rabbitmq
    networks:
      - veloci
    healthcheck:
      test: ["CMD", "rabbitmq-diagnostics", "ping"]
      interval: 10s
      retries: 5

  veloci-auth:
    build: ./services/auth
    environment:
      DATABASE_URL: postgres://${VELOCI_AUTH_DB_USER}:${VELOCI_AUTH_DB_PASSWORD}@postgres:5432/${VELOCI_AUTH_DB}
      CONFIG_PATH: /etc/veloci-auth/veloci-auth.yaml
    volumes:
      - ./config/veloci-auth.yaml:/etc/veloci-auth/veloci-auth.yaml:ro
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - veloci
    # No external ports — internal only

  veloci-api:
    build: ./services/api
    environment:
      DATABASE_URL: postgres://${VELOCI_APP_DB_USER}:${VELOCI_APP_DB_PASSWORD}@postgres:5432/${VELOCI_APP_DB}
      VELOCI_AUTH_URL: http://veloci-auth:8081
      RABBITMQ_URL: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/%2F
      PORT: "8080"
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy
      veloci-auth:
        condition: service_started
    networks:
      - veloci
    ports:
      - "8080:8080"

  veloci-engine:
    build: ./services/engine
    environment:
      DATABASE_URL: postgres://${VELOCI_APP_DB_USER}:${VELOCI_APP_DB_PASSWORD}@postgres:5432/${VELOCI_APP_DB}
      RABBITMQ_URL: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/%2F
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy
    networks:
      - veloci
    healthcheck:
      test: ["/veloci-engine", "health"]
      interval: 30s
      timeout: 5s
      retries: 3

  veloci-web:
    build: ./services/web
    networks:
      - veloci
    ports:
      - "3000:80"

volumes:
  postgres_data:
  rabbitmq_data:

networks:
  veloci:
```

- [ ] **Step 8: Commit**

```bash
git add docker-compose.yml .env.example config/ scripts/ migrations/
git commit -m "feat: project scaffold — two-database postgres, migrations, docker-compose"
```

---

## Task 2: veloci-auth Service

**Files:**

- Create: `services/auth/go.mod`
- Create: `services/auth/internal/tokens/jwt.go`
- Create: `services/auth/internal/tokens/jwt_test.go`
- Create: `services/auth/internal/db/db.go`
- Create: `services/auth/internal/sync/admin.go`
- Create: `services/auth/internal/handlers/credentials.go`
- Create: `services/auth/internal/handlers/credentials_test.go`
- Create: `services/auth/internal/handlers/tokens.go`
- Create: `services/auth/internal/handlers/tokens_test.go`
- Create: `services/auth/main.go`
- Create: `services/auth/Dockerfile`

**Interfaces:**

- Produces:
  - `POST /credentials/validate` → `{credential_id, system_role}` or 401
  - `POST /credentials/create` → `{credential_id}` or 409
  - `POST /tokens/mint` → `{token, jti, expires_at}`
  - `POST /tokens/validate` → `{jti, credential_id, claims}` or 401
  - `DELETE /tokens/:jti` → 204

---

- [ ] **Step 1: Initialize Go module**

```bash
mkdir -p services/auth/internal/{tokens,db,sync,handlers}
cd services/auth
go mod init github.com/veloci/auth
go get github.com/go-chi/chi/v5
go get github.com/jackc/pgx/v5
go get github.com/golang-jwt/jwt/v5
go get golang.org/x/crypto
go get github.com/spf13/cobra
go get github.com/spf13/viper
go get github.com/google/uuid
```

- [ ] **Step 2: Write failing tests for JWT**

```go
// services/auth/internal/tokens/jwt_test.go
package tokens_test

import (
    "encoding/json"
    "testing"
    "time"
    "github.com/veloci/auth/internal/tokens"
)

func TestMintAndVerify(t *testing.T) {
    secret := []byte("test-secret-at-least-32-characters!!")
    claims := json.RawMessage(`{"sub":"user-1","entity_id":"ent-1","entity_role":"entity_admin"}`)
    jti := "test-jti-1"
    exp := time.Now().Add(time.Hour)

    tok, err := tokens.Mint(secret, jti, claims, exp)
    if err != nil {
        t.Fatalf("Mint: %v", err)
    }

    gotJTI, gotClaims, err := tokens.Verify(secret, tok)
    if err != nil {
        t.Fatalf("Verify: %v", err)
    }
    if gotJTI != jti {
        t.Errorf("jti: got %q want %q", gotJTI, jti)
    }

    var m map[string]interface{}
    json.Unmarshal(gotClaims, &m)
    if m["sub"] != "user-1" {
        t.Errorf("sub: got %v want user-1", m["sub"])
    }
}

func TestVerifyRejectsExpired(t *testing.T) {
    secret := []byte("test-secret-at-least-32-characters!!")
    tok, _ := tokens.Mint(secret, "j", json.RawMessage(`{}`), time.Now().Add(-time.Minute))
    _, _, err := tokens.Verify(secret, tok)
    if err == nil {
        t.Error("expected error for expired token")
    }
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
    tok, _ := tokens.Mint([]byte("secret-a-at-least-32-characters!!"), "j", json.RawMessage(`{}`), time.Now().Add(time.Hour))
    _, _, err := tokens.Verify([]byte("secret-b-at-least-32-characters!!"), tok)
    if err == nil {
        t.Error("expected error for wrong secret")
    }
}
```

- [ ] **Step 3: Run tests — verify they fail**

```bash
cd services/auth && go test ./internal/tokens/... 2>&1 | head -5
```

Expected: compile error — package not found.

- [ ] **Step 4: Implement `tokens/jwt.go`**

```go
// services/auth/internal/tokens/jwt.go
package tokens

import (
    "encoding/json"
    "fmt"
    "time"
    "github.com/golang-jwt/jwt/v5"
)

// Mint signs a JWT. Claims are opaque — veloci-auth embeds them as-is.
// jti, iat, exp are added by this function; callers must not include them in claims.
func Mint(secret []byte, jti string, claims json.RawMessage, expiresAt time.Time) (string, error) {
    var m map[string]interface{}
    if err := json.Unmarshal(claims, &m); err != nil {
        return "", fmt.Errorf("invalid claims JSON: %w", err)
    }
    m["jti"] = jti
    m["iat"] = time.Now().Unix()
    m["exp"] = expiresAt.Unix()
    return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(m)).SignedString(secret)
}

// Verify validates signature and expiry. Returns jti and the original claims
// (jti/iat/exp stripped). Does NOT check the token DB — that is the caller's job.
func Verify(secret []byte, tokenStr string) (jti string, claims json.RawMessage, err error) {
    token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
        if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
        }
        return secret, nil
    })
    if err != nil {
        return "", nil, err
    }
    mc, ok := token.Claims.(jwt.MapClaims)
    if !ok || !token.Valid {
        return "", nil, fmt.Errorf("invalid token")
    }
    jtiVal, _ := mc["jti"].(string)
    delete(mc, "jti")
    delete(mc, "iat")
    delete(mc, "exp")
    raw, err := json.Marshal(map[string]interface{}(mc))
    return jtiVal, raw, err
}
```

- [ ] **Step 5: Run tests — verify they pass**

```bash
cd services/auth && go test ./internal/tokens/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 6: Implement `db/db.go`**

```go
// services/auth/internal/db/db.go
package db

import (
    "context"
    "encoding/json"
    "time"
    "github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ pool *pgxpool.Pool }

type Credential struct {
    ID           string
    PasswordHash string
    SystemRole   string
}

func New(ctx context.Context, dsn string) (*DB, error) {
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil {
        return nil, err
    }
    return &DB{pool: pool}, nil
}

func (d *DB) FindCredentialByEmail(ctx context.Context, email string) (*Credential, error) {
    c := &Credential{}
    err := d.pool.QueryRow(ctx,
        `SELECT id, password_hash, system_role FROM auth_credentials WHERE email = $1`,
        email,
    ).Scan(&c.ID, &c.PasswordHash, &c.SystemRole)
    return c, err
}

func (d *DB) CreateCredential(ctx context.Context, id, email, hash, role string) error {
    _, err := d.pool.Exec(ctx,
        `INSERT INTO auth_credentials (id, email, password_hash, system_role) VALUES ($1,$2,$3,$4)`,
        id, email, hash, role,
    )
    return err
}

func (d *DB) UpsertCredential(ctx context.Context, id, email, hash, role string) error {
    _, err := d.pool.Exec(ctx, `
        INSERT INTO auth_credentials (id, email, password_hash, system_role)
        VALUES ($1,$2,$3,$4)
        ON CONFLICT (email) DO UPDATE
          SET password_hash = EXCLUDED.password_hash,
              system_role   = EXCLUDED.system_role
    `, id, email, hash, role)
    return err
}

func (d *DB) StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, expiresAt time.Time) error {
    _, err := d.pool.Exec(ctx,
        `INSERT INTO tokens (id, user_id, jti, claims, expires_at) VALUES ($1,$2,$3,$4,$5)`,
        id, userID, jti, claims, expiresAt,
    )
    return err
}

type TokenRow struct {
    CredentialID string
    Claims       json.RawMessage
    ExpiresAt    time.Time
}

func (d *DB) FindToken(ctx context.Context, jti string) (*TokenRow, error) {
    row := &TokenRow{}
    err := d.pool.QueryRow(ctx,
        `SELECT user_id, claims, expires_at FROM tokens WHERE jti = $1`,
        jti,
    ).Scan(&row.CredentialID, &row.Claims, &row.ExpiresAt)
    return row, err
}

func (d *DB) DeleteToken(ctx context.Context, jti string) error {
    _, err := d.pool.Exec(ctx, `DELETE FROM tokens WHERE jti = $1`, jti)
    return err
}
```

- [ ] **Step 7: Implement `sync/admin.go`**

```go
// services/auth/internal/sync/admin.go
package authsync

import (
    "context"
    "errors"
    "log"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/veloci/auth/internal/db"
    "golang.org/x/crypto/bcrypt"
)

// SyncServerAdmin ensures a server_admin credential exists for the given email/password.
// On first run it hashes and inserts. On subsequent restarts it compares the config
// password against the stored hash — bcrypt work only runs when the password has changed.
// Changing the config password and restarting is the intentional admin-reset UX.
func SyncServerAdmin(ctx context.Context, d *db.DB, email, password string) error {
    existing, err := d.FindCredentialByEmail(ctx, email)
    if err != nil && !errors.Is(err, pgx.ErrNoRows) {
        return err
    }
    if existing != nil {
        compareErr := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(password))
        if compareErr == nil {
            log.Printf("sync: server_admin credential unchanged for %s", email)
            return nil
        }
        if !errors.Is(compareErr, bcrypt.ErrMismatchedHashAndPassword) {
            log.Printf("sync: server_admin hash comparison error for %s: %v", email, compareErr)
        }
        // password changed — fall through to rehash + upsert
    }

    hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
    if err != nil {
        return err
    }
    if err := d.UpsertCredential(ctx, uuid.New().String(), email, string(hash), "server_admin"); err != nil {
        return err
    }
    log.Printf("sync: server_admin credential synced for %s", email)
    return nil
}
```

Note: The sync skips bcrypt on every restart — it first checks whether the config password matches the stored hash. Only if the password has changed does it rehash and upsert. The `id` passed to `UpsertCredential` is only used when inserting a new row; existing rows keep their original UUID.

- [ ] **Step 8: Write failing tests for credential handlers**

```go
// services/auth/internal/handlers/credentials_test.go
package handlers_test

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/auth/internal/handlers"
    "golang.org/x/crypto/bcrypt"
)

type stubCredDB struct {
    hash string
    role string
    miss bool
}

func (s *stubCredDB) FindCredentialByEmail(_ context.Context, _ string) (*handlers.CredentialRow, error) {
    if s.miss {
        return nil, handlers.ErrNotFound
    }
    return &handlers.CredentialRow{ID: "cred-1", PasswordHash: s.hash, SystemRole: s.role}, nil
}

func (s *stubCredDB) CreateCredential(_ context.Context, id, email, hash, role string) error {
    return nil
}

func TestValidateCredential_Success(t *testing.T) {
    hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), 12)
    db := &stubCredDB{hash: string(hash), role: "user"}
    h := handlers.NewCredentials(db)

    body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "correct"})
    req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    h.Validate(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status: got %d want 200; body: %s", w.Code, w.Body)
    }
    var resp map[string]string
    json.NewDecoder(w.Body).Decode(&resp)
    if resp["credential_id"] != "cred-1" {
        t.Errorf("credential_id: got %q", resp["credential_id"])
    }
    if resp["system_role"] != "user" {
        t.Errorf("system_role: got %q", resp["system_role"])
    }
}

func TestValidateCredential_WrongPassword(t *testing.T) {
    hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), 12)
    db := &stubCredDB{hash: string(hash), role: "user"}
    h := handlers.NewCredentials(db)

    body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
    req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    h.Validate(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Errorf("status: got %d want 401", w.Code)
    }
}
```

- [ ] **Step 9: Run tests — verify they fail**

```bash
cd services/auth && go test ./internal/handlers/... 2>&1 | head -5
```

Expected: compile error — package not found.

- [ ] **Step 10: Implement `handlers/credentials.go`**

```go
// services/auth/internal/handlers/credentials.go
package handlers

import (
    "context"
    "encoding/json"
    "errors"
    "net/http"
    "github.com/google/uuid"
    "golang.org/x/crypto/bcrypt"
)

var ErrNotFound = errors.New("not found")

type CredentialRow struct {
    ID           string
    PasswordHash string
    SystemRole   string
}

type credentialStore interface {
    FindCredentialByEmail(ctx context.Context, email string) (*CredentialRow, error)
    CreateCredential(ctx context.Context, id, email, hash, role string) error
}

type Credentials struct{ db credentialStore }

func NewCredentials(db credentialStore) *Credentials { return &Credentials{db: db} }

func (h *Credentials) Validate(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Email    string `json:"email"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
        return
    }
    cred, err := h.db.FindCredentialByEmail(r.Context(), req.Email)
    if err != nil || bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(req.Password)) != nil {
        http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "credential_id": cred.ID,
        "system_role":   cred.SystemRole,
    })
}

func (h *Credentials) Create(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Email    string `json:"email"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
        return
    }
    hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
    if err != nil {
        http.Error(w, `{"code":"INTERNAL"}`, http.StatusInternalServerError)
        return
    }
    id := uuid.New().String()
    if err := h.db.CreateCredential(r.Context(), id, req.Email, string(hash), "user"); err != nil {
        http.Error(w, `{"code":"CONFLICT"}`, http.StatusConflict)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]string{"credential_id": id})
}
```

- [ ] **Step 11: Write failing tests for token handlers**

```go
// services/auth/internal/handlers/tokens_test.go
package handlers_test

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
    "github.com/veloci/auth/internal/handlers"
    "github.com/veloci/auth/internal/tokens"
)

type stubTokenDB struct {
    stored map[string]*handlers.TokenRow
}

func newStubTokenDB() *stubTokenDB {
    return &stubTokenDB{stored: map[string]*handlers.TokenRow{}}
}

func (s *stubTokenDB) StoreToken(_ context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error {
    s.stored[jti] = &handlers.TokenRow{CredentialID: userID, Claims: claims, ExpiresAt: exp}
    return nil
}

func (s *stubTokenDB) FindToken(_ context.Context, jti string) (*handlers.TokenRow, error) {
    row, ok := s.stored[jti]
    if !ok {
        return nil, handlers.ErrNotFound
    }
    return row, nil
}

func (s *stubTokenDB) DeleteToken(_ context.Context, jti string) error {
    delete(s.stored, jti)
    return nil
}

func TestMintAndValidateToken(t *testing.T) {
    secret := []byte("test-secret-at-least-32-characters!!")
    db := newStubTokenDB()
    h := handlers.NewTokens(db, secret)

    mintBody, _ := json.Marshal(map[string]interface{}{
        "credential_id": "cred-1",
        "claims": map[string]string{
            "sub": "user-1", "entity_id": "ent-1", "entity_role": "entity_admin",
        },
    })
    req := httptest.NewRequest(http.MethodPost, "/tokens/mint", bytes.NewReader(mintBody))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    h.Mint(w, req)

    if w.Code != http.StatusCreated {
        t.Fatalf("mint status: got %d; body: %s", w.Code, w.Body)
    }
    var mintResp map[string]string
    json.NewDecoder(w.Body).Decode(&mintResp)
    tok := mintResp["token"]
    if tok == "" {
        t.Fatal("expected token in mint response")
    }

    validateBody, _ := json.Marshal(map[string]string{"token": tok})
    req2 := httptest.NewRequest(http.MethodPost, "/tokens/validate", bytes.NewReader(validateBody))
    req2.Header.Set("Content-Type", "application/json")
    w2 := httptest.NewRecorder()
    h.Validate(w2, req2)

    if w2.Code != http.StatusOK {
        t.Fatalf("validate status: got %d; body: %s", w2.Code, w2.Body)
    }
    var validateResp map[string]interface{}
    json.NewDecoder(w2.Body).Decode(&validateResp)
    if validateResp["credential_id"] != "cred-1" {
        t.Errorf("credential_id: got %v", validateResp["credential_id"])
    }
}
```

- [ ] **Step 12: Implement `handlers/tokens.go`**

```go
// services/auth/internal/handlers/tokens.go
package handlers

import (
    "context"
    "encoding/json"
    "net/http"
    "time"
    "github.com/go-chi/chi/v5"
    "github.com/google/uuid"
    "github.com/veloci/auth/internal/tokens"
)

type TokenRow struct {
    CredentialID string
    Claims       json.RawMessage
    ExpiresAt    time.Time
}

type tokenStore interface {
    StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error
    FindToken(ctx context.Context, jti string) (*TokenRow, error)
    DeleteToken(ctx context.Context, jti string) error
}

type Tokens struct {
    db     tokenStore
    secret []byte
}

func NewTokens(db tokenStore, secret []byte) *Tokens { return &Tokens{db: db, secret: secret} }

func (h *Tokens) Mint(w http.ResponseWriter, r *http.Request) {
    var req struct {
        CredentialID string          `json:"credential_id"`
        Claims       json.RawMessage `json:"claims"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
        return
    }
    jti := uuid.New().String()
    expiresAt := time.Now().Add(60 * time.Minute)

    tok, err := tokens.Mint(h.secret, jti, req.Claims, expiresAt)
    if err != nil {
        http.Error(w, `{"code":"INTERNAL"}`, http.StatusInternalServerError)
        return
    }
    id := uuid.New().String()
    if err := h.db.StoreToken(r.Context(), id, req.CredentialID, jti, req.Claims, expiresAt); err != nil {
        http.Error(w, `{"code":"INTERNAL"}`, http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]interface{}{
        "token":      tok,
        "jti":        jti,
        "expires_at": expiresAt.UTC().Format(time.RFC3339),
    })
}

func (h *Tokens) Validate(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Token string `json:"token"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
        return
    }
    jti, _, err := tokens.Verify(h.secret, req.Token)
    if err != nil {
        http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
        return
    }
    row, err := h.db.FindToken(r.Context(), jti)
    if err != nil || time.Now().After(row.ExpiresAt) {
        http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "jti":           jti,
        "credential_id": row.CredentialID,
        "claims":        json.RawMessage(row.Claims),
    })
}

func (h *Tokens) Revoke(w http.ResponseWriter, r *http.Request) {
    jti := chi.URLParam(r, "jti")
    h.db.DeleteToken(r.Context(), jti)
    w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 13: Run all auth handler tests — verify they pass**

```bash
cd services/auth && go test ./internal/... -v
```

Expected: all tests PASS.

- [ ] **Step 14: Implement `main.go`**

```go
// services/auth/main.go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
    "github.com/veloci/auth/internal/db"
    "github.com/veloci/auth/internal/handlers"
    authsync "github.com/veloci/auth/internal/sync"
)

// dbCredAdapter adapts *db.DB to satisfy handlers.credentialStore.
type dbCredAdapter struct{ d *db.DB }

func (a *dbCredAdapter) FindCredentialByEmail(ctx context.Context, email string) (*handlers.CredentialRow, error) {
    c, err := a.d.FindCredentialByEmail(ctx, email)
    if err != nil {
        return nil, err
    }
    return &handlers.CredentialRow{ID: c.ID, PasswordHash: c.PasswordHash, SystemRole: c.SystemRole}, nil
}

func (a *dbCredAdapter) CreateCredential(ctx context.Context, id, email, hash, role string) error {
    return a.d.CreateCredential(ctx, id, email, hash, role)
}

// dbTokenAdapter adapts *db.DB to satisfy handlers.tokenStore.
type dbTokenAdapter struct{ d *db.DB }

func (a *dbTokenAdapter) StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error {
    return a.d.StoreToken(ctx, id, userID, jti, claims, exp)
}

func (a *dbTokenAdapter) FindToken(ctx context.Context, jti string) (*handlers.TokenRow, error) {
    row, err := a.d.FindToken(ctx, jti)
    if err != nil {
        return nil, err
    }
    return &handlers.TokenRow{CredentialID: row.CredentialID, Claims: row.Claims, ExpiresAt: row.ExpiresAt}, nil
}

func (a *dbTokenAdapter) DeleteToken(ctx context.Context, jti string) error {
    return a.d.DeleteToken(ctx, jti)
}

func main() {
    if err := rootCmd.Execute(); err != nil {
        log.Fatal(err)
    }
}

var rootCmd = &cobra.Command{Use: "veloci-auth", Short: "Veloci auth service"}

var serveCmd = &cobra.Command{
    Use:  "serve",
    RunE: runServe,
}

func init() {
    rootCmd.AddCommand(serveCmd)
    viper.AutomaticEnv()
    viper.SetDefault("PORT", "8081")
    // Env var overrides for secrets — AutomaticEnv doesn't map nested keys.
    // VELOCI_SERVER_ADMIN_EMAIL / VELOCI_SERVER_ADMIN_PASSWORD / VELOCI_JWT_SECRET
    // take precedence over veloci-auth.yaml values when set.
    viper.BindEnv("server_admin.email", "VELOCI_SERVER_ADMIN_EMAIL")
    viper.BindEnv("server_admin.password", "VELOCI_SERVER_ADMIN_PASSWORD")
    viper.BindEnv("jwt_secret", "VELOCI_JWT_SECRET")
}

func runServe(_ *cobra.Command, _ []string) error {
    configPath := viper.GetString("CONFIG_PATH")
    if configPath != "" {
        viper.SetConfigFile(configPath)
        if err := viper.ReadInConfig(); err != nil {
            return fmt.Errorf("config: %w", err)
        }
    }

    ctx := context.Background()
    database, err := db.New(ctx, viper.GetString("DATABASE_URL"))
    if err != nil {
        return fmt.Errorf("db: %w", err)
    }

    adminEmail := viper.GetString("server_admin.email")
    adminPass := viper.GetString("server_admin.password")
    if adminEmail != "" && adminPass != "" {
        if err := authsync.SyncServerAdmin(ctx, database, adminEmail, adminPass); err != nil {
            return fmt.Errorf("admin sync: %w", err)
        }
    }

    secret := []byte(viper.GetString("jwt_secret"))
    if len(secret) < 32 {
        return fmt.Errorf("jwt_secret must be at least 32 characters")
    }

    creds := handlers.NewCredentials(&dbCredAdapter{database})
    toks := handlers.NewTokens(&dbTokenAdapter{database}, secret)

    r := chi.NewRouter()
    r.Post("/credentials/validate", creds.Validate)
    r.Post("/credentials/create", creds.Create)
    r.Post("/tokens/mint", toks.Mint)
    r.Post("/tokens/validate", toks.Validate)
    r.Delete("/tokens/{jti}", toks.Revoke)

    port := viper.GetString("PORT")
    log.Printf("veloci-auth listening on :%s", port)
    return http.ListenAndServe(":"+port, r)
}
```

Note: `main.go` uses adapter structs (`dbCredAdapter`, `dbTokenAdapter`) to bridge `*db.DB` to the handler interfaces. This keeps `db.DB` decoupled from the handler layer and keeps handlers testable with stubs. `SyncServerAdmin` from the `authsync` package is called directly rather than an inline helper.

- [ ] **Step 15: Create `Dockerfile`**

```dockerfile
# services/auth/Dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o auth .

FROM alpine:3.24
COPY --from=build /app/auth /auth
ENTRYPOINT ["/auth", "serve"]
```

- [ ] **Step 16: Commit**

```bash
git add services/auth/
git commit -m "feat: veloci-auth service — credential management, token lifecycle, admin sync"
```

---

## Task 3: veloci-api Scaffolding

**Files:**

- Create: `services/api/go.mod`
- Create: `services/api/internal/authclient/client.go`
- Create: `services/api/internal/authclient/client_test.go`
- Create: `services/api/internal/middleware/auth.go`
- Create: `services/api/internal/middleware/auth_test.go`
- Create: `services/api/internal/handlers/auth.go`
- Create: `services/api/internal/handlers/auth_test.go`
- Create: `services/api/internal/handlers/health.go`
- Create: `services/api/internal/queue/publisher.go`
- Create: `services/api/main.go`
- Create: `services/api/Dockerfile`

**Interfaces:**

- Consumes: `veloci-auth` HTTP endpoints
- Produces:
  - `POST /auth/login` → `{token, expires_at}`
  - `GET /health` → `{status: "ok"}`
  - `middleware.Authenticate(client)` — calls `/tokens/validate`, injects claims into context
  - `middleware.EntityID(ctx)`, `middleware.EntityRole(ctx)`, `middleware.SystemRole(ctx)`

---

- [ ] **Step 1: Initialize Go module**

```bash
mkdir -p services/api/internal/{authclient,middleware,handlers,queue}
cd services/api
go mod init github.com/veloci/api
go get github.com/go-chi/chi/v5
go get github.com/jackc/pgx/v5
go get github.com/rabbitmq/amqp091-go
go get github.com/spf13/cobra
go get github.com/spf13/viper
```

- [ ] **Step 2: Implement `authclient/client.go`**

```go
// services/api/internal/authclient/client.go
package authclient

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
)

type Client struct {
    baseURL    string
    httpClient *http.Client
}

func New(baseURL string) *Client {
    return &Client{baseURL: baseURL, httpClient: &http.Client{}}
}

type ValidateResult struct {
    JTI          string          `json:"jti"`
    CredentialID string          `json:"credential_id"`
    Claims       json.RawMessage `json:"claims"`
}

type ValidateCredResult struct {
    CredentialID string `json:"credential_id"`
    SystemRole   string `json:"system_role"`
}

type MintResult struct {
    Token     string `json:"token"`
    JTI       string `json:"jti"`
    ExpiresAt string `json:"expires_at"`
}

func (c *Client) ValidateToken(ctx context.Context, token string) (*ValidateResult, error) {
    body, _ := json.Marshal(map[string]string{"token": token})
    resp, err := c.post(ctx, "/tokens/validate", body)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("auth: validate returned %d", resp.StatusCode)
    }
    var result ValidateResult
    return &result, json.NewDecoder(resp.Body).Decode(&result)
}

func (c *Client) ValidateCredential(ctx context.Context, email, password string) (*ValidateCredResult, error) {
    body, _ := json.Marshal(map[string]string{"email": email, "password": password})
    resp, err := c.post(ctx, "/credentials/validate", body)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("auth: credential validate returned %d", resp.StatusCode)
    }
    var result ValidateCredResult
    return &result, json.NewDecoder(resp.Body).Decode(&result)
}

func (c *Client) MintToken(ctx context.Context, credentialID string, claims map[string]interface{}) (*MintResult, error) {
    body, _ := json.Marshal(map[string]interface{}{
        "credential_id": credentialID,
        "claims":        claims,
    })
    resp, err := c.post(ctx, "/tokens/mint", body)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusCreated {
        return nil, fmt.Errorf("auth: mint returned %d", resp.StatusCode)
    }
    var result MintResult
    return &result, json.NewDecoder(resp.Body).Decode(&result)
}

func (c *Client) RevokeToken(ctx context.Context, jti string) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/tokens/"+jti, nil)
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    resp.Body.Close()
    return nil
}

func (c *Client) CreateCredential(ctx context.Context, email, password string) (string, error) {
    body, _ := json.Marshal(map[string]string{"email": email, "password": password})
    resp, err := c.post(ctx, "/credentials/create", body)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusCreated {
        return "", fmt.Errorf("auth: create credential returned %d", resp.StatusCode)
    }
    var result map[string]string
    json.NewDecoder(resp.Body).Decode(&result)
    return result["credential_id"], nil
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    return c.httpClient.Do(req)
}
```

- [ ] **Step 3: Write and verify tests for authclient**

```go
// services/api/internal/authclient/client_test.go
package authclient_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/api/internal/authclient"
)

func TestValidateToken_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/tokens/validate" || r.Method != http.MethodPost {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]any{
            "jti":           "test-jti",
            "credential_id": "cred-1",
            "claims":        map[string]string{"sub": "user-1", "entity_id": "ent-1"},
        })
    }))
    defer srv.Close()

    c := authclient.New(srv.URL)
    result, err := c.ValidateToken(context.Background(), "some-token")
    if err != nil {
        t.Fatalf("ValidateToken: %v", err)
    }
    if result.JTI != "test-jti" {
        t.Errorf("jti: got %q want test-jti", result.JTI)
    }
    if result.CredentialID != "cred-1" {
        t.Errorf("credential_id: got %q want cred-1", result.CredentialID)
    }
}

func TestValidateToken_Unauthorized(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
    }))
    defer srv.Close()

    c := authclient.New(srv.URL)
    _, err := c.ValidateToken(context.Background(), "bad-token")
    if err == nil {
        t.Error("expected error for 401 response")
    }
}

func TestValidateCredential_Success(t *testing.T) { /* see file for full test */ }
func TestMintToken_Success(t *testing.T)          { /* see file for full test */ }
func TestCreateCredential_Success(t *testing.T)   { /* see file for full test */ }
func TestCreateCredential_Conflict(t *testing.T)  { /* see file for full test */ }
```

```bash
cd services/api && go test ./internal/authclient/... -v
```

Expected: all tests PASS.

- [ ] **Step 4: Write failing tests for auth middleware**

```go
// services/api/internal/middleware/auth_test.go
package middleware_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/api/internal/authclient"
    "github.com/veloci/api/internal/middleware"
)

// mockAuthServer simulates veloci-auth /tokens/validate
func mockAuthServer(claims map[string]interface{}) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/tokens/validate" {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
            "jti":           "test-jti",
            "credential_id": "cred-1",
            "claims":        claims,
        })
    }))
}

func TestAuthMiddlewareInjectsClaims(t *testing.T) {
    srv := mockAuthServer(map[string]interface{}{
        "sub": "user-1", "entity_id": "ent-1",
        "entity_role": "entity_admin", "system_role": "user",
    })
    defer srv.Close()

    client := authclient.New(srv.URL)
    var gotEntityID, gotEntityRole string

    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotEntityID = middleware.EntityID(r.Context())
        gotEntityRole = middleware.EntityRole(r.Context())
        w.WriteHeader(http.StatusOK)
    })

    req := httptest.NewRequest(http.MethodGet, "/", nil)
    req.Header.Set("Authorization", "Bearer sometoken")
    w := httptest.NewRecorder()
    middleware.Authenticate(client)(next).ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status: got %d want 200", w.Code)
    }
    if gotEntityID != "ent-1" {
        t.Errorf("entity_id: got %q", gotEntityID)
    }
    if gotEntityRole != "entity_admin" {
        t.Errorf("entity_role: got %q", gotEntityRole)
    }
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
    client := authclient.New("http://unused")
    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    w := httptest.NewRecorder()
    middleware.Authenticate(client)(next).ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Errorf("status: got %d want 401", w.Code)
    }
}
```

- [ ] **Step 4: Run middleware tests — verify they fail**

```bash
cd services/api && go test ./internal/middleware/... 2>&1 | head -5
```

Expected: compile error — package not found.

- [ ] **Step 5: Implement `middleware/auth.go`**

```go
// services/api/internal/middleware/auth.go
package middleware

import (
    "context"
    "encoding/json"
    "net/http"
    "strings"
)

type tokenValidator interface {
    ValidateToken(ctx context.Context, token string) (interface{ GetClaims() json.RawMessage }, error)
}

type contextKey string

const (
    ctxEntityID   contextKey = "entity_id"
    ctxEntityRole contextKey = "entity_role"
    ctxSystemRole contextKey = "system_role"
    ctxUserID     contextKey = "sub"
)

type authClient interface {
    ValidateToken(ctx context.Context, token string) (*validateResult, error)
}

// validateResult mirrors authclient.ValidateResult without importing it
// (keeps middleware testable with any compatible client)
type validateResult struct {
    Claims json.RawMessage
}

// Authenticate calls veloci-auth /tokens/validate on every request.
// Injects entity_id, entity_role, system_role, and sub into context.
func Authenticate(client interface {
    ValidateToken(ctx context.Context, token string) (interface{ GetEntityID() string; GetEntityRole() string; GetSystemRole() string; GetSub() string }, error)
}) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            header := r.Header.Get("Authorization")
            if !strings.HasPrefix(header, "Bearer ") {
                http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
                return
            }
            token := strings.TrimPrefix(header, "Bearer ")
            result, err := client.ValidateToken(r.Context(), token)
            if err != nil {
                http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
                return
            }
            ctx := context.WithValue(r.Context(), ctxEntityID, result.GetEntityID())
            ctx = context.WithValue(ctx, ctxEntityRole, result.GetEntityRole())
            ctx = context.WithValue(ctx, ctxSystemRole, result.GetSystemRole())
            ctx = context.WithValue(ctx, ctxUserID, result.GetSub())
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func EntityID(ctx context.Context) string   { v, _ := ctx.Value(ctxEntityID).(string); return v }
func EntityRole(ctx context.Context) string { v, _ := ctx.Value(ctxEntityRole).(string); return v }
func SystemRole(ctx context.Context) string { v, _ := ctx.Value(ctxSystemRole).(string); return v }
func UserID(ctx context.Context) string     { v, _ := ctx.Value(ctxUserID).(string); return v }
```

Note: The interface approach above is overly complex. Simplify by accepting `*authclient.Client` directly and extracting claims from the returned `json.RawMessage`:

```go
// Simplified middleware/auth.go — replace the Authenticate function above with:

import "github.com/veloci/api/internal/authclient"

func Authenticate(client *authclient.Client) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            header := r.Header.Get("Authorization")
            if !strings.HasPrefix(header, "Bearer ") {
                http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
                return
            }
            result, err := client.ValidateToken(r.Context(), strings.TrimPrefix(header, "Bearer "))
            if err != nil {
                http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
                return
            }
            var claims map[string]interface{}
            json.Unmarshal(result.Claims, &claims)
            ctx := r.Context()
            for key, ctxK := range map[string]contextKey{
                "entity_id": ctxEntityID, "entity_role": ctxEntityRole,
                "system_role": ctxSystemRole, "sub": ctxUserID,
            } {
                if v, ok := claims[key].(string); ok {
                    ctx = context.WithValue(ctx, ctxK, v)
                }
            }
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

Update the test's mock server to return the correct shape (already done in Step 3), and update `Authenticate`'s parameter to `*authclient.Client`.

- [ ] **Step 6: Run middleware tests — verify they pass**

```bash
cd services/api && go test ./internal/middleware/... -v
```

Expected: both tests PASS.

- [ ] **Step 7: Write failing test for login handler**

```go
// services/api/internal/handlers/auth_test.go
package handlers_test

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/api/internal/handlers"
)

// stubAuthForLogin simulates veloci-auth /credentials/validate and /tokens/mint
func stubAuthForLogin(t *testing.T) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/credentials/validate":
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]string{
                "credential_id": "cred-1",
                "system_role":   "user",
            })
        case "/tokens/mint":
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusCreated)
            json.NewEncoder(w).Encode(map[string]string{
                "token":      "test-token",
                "jti":        "jti-1",
                "expires_at": "2099-01-01T00:00:00Z",
            })
        default:
            t.Errorf("unexpected auth call: %s", r.URL.Path)
            http.NotFound(w, r)
        }
    }))
}

// stubAppDB simulates the veloci_app lookup for entity+role
type stubAppDB struct{}

func (s *stubAppDB) FindUserEntity(email string) (handlers.UserEntity, error) {
    return handlers.UserEntity{UserID: "user-1", EntityID: "ent-1", EntityRole: "entity_admin"}, nil
}

func TestLoginSuccess(t *testing.T) {
    authSrv := stubAuthForLogin(t)
    defer authSrv.Close()

    h := handlers.NewAuth(authSrv.URL, &stubAppDB{})
    body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "pw"})
    req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    h.Login(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status: got %d; body: %s", w.Code, w.Body)
    }
    var resp map[string]string
    json.NewDecoder(w.Body).Decode(&resp)
    if resp["token"] == "" {
        t.Error("expected token in response")
    }
}
```

- [ ] **Step 8: Run — verify test fails**

```bash
cd services/api && go test ./internal/handlers/... 2>&1 | head -5
```

Expected: compile error.

- [ ] **Step 9: Implement `handlers/auth.go`**

```go
// services/api/internal/handlers/auth.go
package handlers

import (
    "context"
    "encoding/json"
    "net/http"
    "github.com/veloci/api/internal/authclient"
)

type UserEntity struct {
    UserID     string
    EntityID   string
    EntityRole string
}

type appDB interface {
    FindUserEntity(email string) (UserEntity, error)
}

type Auth struct {
    auth *authclient.Client
    db   appDB
}

func NewAuth(authURL string, db appDB) *Auth {
    return &Auth{auth: authclient.New(authURL), db: db}
}

func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Email    string `json:"email"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
        return
    }

    cred, err := h.auth.ValidateCredential(r.Context(), req.Email, req.Password)
    if err != nil {
        http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
        return
    }

    ue, err := h.db.FindUserEntity(req.Email)
    if err != nil {
        http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
        return
    }

    claims := map[string]interface{}{
        "sub":         ue.UserID,
        "email":       req.Email,
        "system_role": cred.SystemRole,
        "entity_id":   ue.EntityID,
        "entity_role": ue.EntityRole,
    }
    minted, err := h.auth.MintToken(r.Context(), cred.CredentialID, claims)
    if err != nil {
        http.Error(w, `{"code":"INTERNAL"}`, http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{
        "token":      minted.Token,
        "expires_at": minted.ExpiresAt,
    })
}

func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) {
    var req struct{ JTI string `json:"jti"` }
    json.NewDecoder(r.Body).Decode(&req)
    if req.JTI != "" {
        h.auth.RevokeToken(context.Background(), req.JTI)
    }
    w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 10: Run all API handler tests — verify they pass**

```bash
cd services/api && go test ./internal/... -v
```

Expected: all tests PASS.

- [ ] **Step 11: Implement `queue/publisher.go`, health handler, and `main.go`**

```go
// services/api/internal/queue/publisher.go
package queue

import (
    "context"
    "encoding/json"
    amqp "github.com/rabbitmq/amqp091-go"
)

const QueueName = "veloci.jobs"

type Job struct {
    JobID    string          `json:"job_id"`
    Type     string          `json:"type"`
    EntityID string          `json:"entity_id"`
    Metadata json.RawMessage `json:"metadata"`
}

type Publisher struct {
    ch    *amqp.Channel
    queue string
}

func NewPublisher(url string) (*Publisher, error) {
    conn, err := amqp.Dial(url)
    if err != nil {
        return nil, err
    }
    ch, err := conn.Channel()
    if err != nil {
        return nil, err
    }
    _, err = ch.QueueDeclare(QueueName, true, false, false, false, nil)
    if err != nil {
        return nil, err
    }
    return &Publisher{ch: ch, queue: QueueName}, nil
}

func (p *Publisher) Publish(ctx context.Context, job Job) error {
    body, err := json.Marshal(job)
    if err != nil {
        return err
    }
    return p.ch.PublishWithContext(ctx, "", p.queue, false, false, amqp.Publishing{
        ContentType:  "application/json",
        DeliveryMode: amqp.Persistent,
        Body:         body,
    })
}
```

```go
// services/api/internal/handlers/health.go
package handlers

import (
    "encoding/json"
    "net/http"
)

func Health(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

```go
// services/api/main.go
package main

import (
    "context"
    "fmt"
    "log"
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"

    "github.com/veloci/api/internal/authclient"
    "github.com/veloci/api/internal/handlers"
    "github.com/veloci/api/internal/middleware"
    "github.com/veloci/api/internal/queue"
)

type appDBImpl struct {
    pool *pgxpool.Pool
}

func (d *appDBImpl) FindUserEntity(ctx context.Context, email string) (handlers.UserEntity, error) {
    var ue handlers.UserEntity
    err := d.pool.QueryRow(ctx, `
        SELECT u.id::text, eu.entity_id::text, eu.entity_role
        FROM users u
        JOIN entity_users eu ON eu.user_id = u.id
        WHERE u.email = $1
        LIMIT 1
    `, email).Scan(&ue.UserID, &ue.EntityID, &ue.EntityRole)
    return ue, err
}

func main() {
    if err := rootCmd.Execute(); err != nil {
        log.Fatal(err)
    }
}

var rootCmd = &cobra.Command{Use: "veloci-api", Short: "Veloci API service"}

var serveCmd = &cobra.Command{Use: "serve", Short: "Start the HTTP server", RunE: runServe}

func init() {
    rootCmd.AddCommand(serveCmd)
    viper.AutomaticEnv()
    viper.SetDefault("PORT", "8080")
}

func runServe(_ *cobra.Command, _ []string) error {
    authURL := viper.GetString("VELOCI_AUTH_URL")
    if authURL == "" {
        return fmt.Errorf("VELOCI_AUTH_URL required")
    }

    pub, err := queue.NewPublisher(viper.GetString("RABBITMQ_URL"))
    if err != nil {
        return fmt.Errorf("queue: %w", err)
    }
    _ = pub // passed to financial route handlers in service implementation plans

    pool, err := pgxpool.New(context.Background(), viper.GetString("DATABASE_URL"))
    if err != nil {
        return fmt.Errorf("database: %w", err)
    }
    defer pool.Close()

    authClient := authclient.New(authURL)

    authHandler := handlers.NewAuth(authURL, &appDBImpl{pool: pool})

    r := chi.NewRouter()
    r.Get("/health", handlers.Health)
    r.Post("/auth/login", authHandler.Login)
    r.Post("/auth/logout", authHandler.Logout)

    r.Group(func(r chi.Router) {
        r.Use(middleware.Authenticate(authClient))
        // Financial routes added in service-specific implementation plans
    })

    port := viper.GetString("PORT")
    log.Printf("veloci-api listening on :%s", port)
    return http.ListenAndServe(":"+port, r)
}
```

- [ ] **Step 12: Write tests for queue publisher**

```go
// services/api/internal/queue/publisher_test.go
package queue_test

import (
    "encoding/json"
    "testing"
    "github.com/veloci/api/internal/queue"
)

func TestJobSerializesCorrectly(t *testing.T) {
    job := queue.Job{
        JobID:    "job-123",
        Type:     "import.process",
        EntityID: "ent-1",
        Metadata: json.RawMessage(`{"pending_import_id":"imp-1"}`),
    }
    body, err := json.Marshal(job)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var m map[string]any
    json.Unmarshal(body, &m)
    if m["job_id"] != "job-123" {
        t.Errorf("job_id: got %v", m["job_id"])
    }
    if m["type"] != "import.process" {
        t.Errorf("type: got %v", m["type"])
    }
}

func TestNewPublisher_FailsWithUnreachableHost(t *testing.T) {
    _, err := queue.NewPublisher("amqp://localhost:1/")
    if err == nil {
        t.Error("expected error connecting to unreachable host")
    }
}
```

```bash
cd services/api && go test ./internal/queue/... -v
```

Expected: serialization tests PASS; `TestNewPublisher_FailsWithUnreachableHost` PASS (connection refused).

- [ ] **Step 13: Create `Dockerfile`**

```dockerfile
# services/api/Dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o api .

FROM alpine:3.24
COPY --from=build /app/api /api
ENTRYPOINT ["/api", "serve"]
```

- [ ] **Step 14: Commit**

```bash
git add services/api/
git commit -m "feat: veloci-api scaffold — auth proxy, per-request token validation, RabbitMQ publisher"
```

---

## Task 4: veloci-engine Scaffolding

**Files:**

- Create: `services/engine/Cargo.toml`
- Create: `services/engine/src/main.rs`
- Create: `services/engine/src/consumer.rs`
- Create: `services/engine/src/db.rs`
- Create: `services/engine/src/health.rs`
- Create: `services/engine/src/jobs/mod.rs`
- Create: `services/engine/Dockerfile`

**Interfaces:**

- Consumes: `queue.Job` envelope — `{"job_id":"...","type":"...","entity_id":"...","metadata":{...}}`
- Produces: persistent RabbitMQ consumer dispatching to stub job handlers

---

- [ ] **Step 1: Initialize Rust project**

```bash
mkdir -p services/engine/src/jobs
cd services/engine
cargo init --name veloci-engine
```

- [ ] **Step 2: Set `Cargo.toml` dependencies**

```toml
# services/engine/Cargo.toml
[package]
name = "veloci-engine"
version = "0.1.0"
edition = "2021"

[dependencies]
tokio       = { version = "1",   features = ["full"] }
lapin       = "2"
sqlx        = { version = "0.8", features = ["postgres", "runtime-tokio", "uuid", "chrono"] }
serde       = { version = "1",   features = ["derive"] }
serde_json  = "1"
anyhow      = "1"
tracing     = "0.1"
tracing-subscriber = { version = "0.3", features = ["env-filter"] }
backon      = "1"
futures-lite = "2"
```

- [ ] **Step 3: Write test for job dispatch routing**

```rust
// Add to services/engine/src/jobs/mod.rs
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[tokio::test]
    async fn known_job_types_dispatch_ok() {
        for t in &["import.process", "rules.reprocess", "account.analyze"] {
            let job = Job { job_id: "j".into(), r#type: t.to_string(),
                            entity_id: "e".into(), metadata: json!({}) };
            assert!(dispatch(job).await.is_ok(), "failed for {}", t);
        }
    }

    #[tokio::test]
    async fn unknown_job_type_is_dropped_not_errored() {
        let job = Job { job_id: "j".into(), r#type: "unknown".into(),
                        entity_id: "e".into(), metadata: json!({}) };
        assert!(dispatch(job).await.is_ok());
    }
}
```

- [ ] **Step 4: Run — verify compile fails**

```bash
cd services/engine && cargo test 2>&1 | tail -5
```

Expected: compile error — `Job` and `dispatch` not defined.

- [ ] **Step 5: Implement `jobs/mod.rs`**

```rust
// services/engine/src/jobs/mod.rs
use anyhow::Result;
use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize, Serialize)]
pub struct Job {
    pub job_id:    String,
    pub r#type:    String,
    pub entity_id: String,
    pub metadata:  serde_json::Value,
}

pub async fn dispatch(job: Job) -> Result<()> {
    match job.r#type.as_str() {
        "import.process"   => import_process(&job.entity_id).await,
        "rules.reprocess"  => rules_reprocess(&job.entity_id).await,
        "account.analyze"  => account_analyze(&job.entity_id).await,
        other => { tracing::warn!("unknown job type: {}", other); Ok(()) }
    }
}

async fn import_process(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "import.process stub");
    Ok(())
}

async fn rules_reprocess(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "rules.reprocess stub");
    Ok(())
}

async fn account_analyze(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "account.analyze stub");
    Ok(())
}
```

- [ ] **Step 6: Run tests — verify they pass**

```bash
cd services/engine && cargo test -- --nocapture
```

Expected:

```text
test jobs::tests::known_job_types_dispatch_ok ... ok
test jobs::tests::unknown_job_type_is_dropped_not_errored ... ok
```

- [ ] **Step 7: Implement `consumer.rs`, `db.rs`, `health.rs`, `main.rs`**

```rust
// services/engine/src/consumer.rs
use anyhow::Result;
use backon::{ExponentialBuilder, Retryable};
use futures_lite::StreamExt;
use lapin::{Connection, ConnectionProperties,
    options::{BasicAckOptions, BasicConsumeOptions, QueueDeclareOptions},
    types::FieldTable};
use crate::jobs::{self, Job};

const QUEUE: &str = "veloci.jobs";

pub async fn run(rabbitmq_url: &str) -> Result<()> {
    let url = rabbitmq_url.to_string();
    let conn = (|| async {
        Connection::connect(&url, ConnectionProperties::default()).await
    })
    .retry(ExponentialBuilder::default().with_max_times(10))
    .await?;

    let ch = conn.create_channel().await?;
    ch.queue_declare(QUEUE, QueueDeclareOptions { durable: true, ..Default::default() },
        FieldTable::default()).await?;

    let mut consumer = ch.basic_consume(QUEUE, "veloci-engine",
        BasicConsumeOptions::default(), FieldTable::default()).await?;

    tracing::info!("consuming from {}", QUEUE);
    while let Some(delivery) = consumer.next().await {
        let d = delivery?;
        match serde_json::from_slice::<Job>(&d.data) {
            Ok(job) => {
                let (eid, jt) = (job.entity_id.clone(), job.r#type.clone());
                if let Err(e) = jobs::dispatch(job).await {
                    tracing::error!(entity_id=%eid, job_type=%jt, "job failed: {:?}", e);
                }
            }
            Err(e) => tracing::error!("malformed job: {:?}", e),
        }
        d.ack(BasicAckOptions::default()).await?;
    }
    Ok(())
}
```

```rust
// services/engine/src/db.rs
use anyhow::Result;
use sqlx::PgPool;

pub async fn connect(database_url: &str) -> Result<PgPool> {
    Ok(PgPool::connect(database_url).await?)
}
```

```rust
// services/engine/src/health.rs
use anyhow::Result;

pub async fn check(database_url: &str, rabbitmq_url: &str) -> Result<()> {
    sqlx::PgPool::connect(database_url).await
        .map_err(|e| anyhow::anyhow!("postgres: {}", e))?;
    lapin::Connection::connect(rabbitmq_url, lapin::ConnectionProperties::default()).await
        .map_err(|e| anyhow::anyhow!("rabbitmq: {}", e))?;
    println!("ok");
    Ok(())
}
```

```rust
// services/engine/src/main.rs
mod consumer;
mod db;
mod health;
mod jobs;

use anyhow::Result;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let db_url  = std::env::var("DATABASE_URL").expect("DATABASE_URL required");
    let mq_url  = std::env::var("RABBITMQ_URL").expect("RABBITMQ_URL required");

    match std::env::args().nth(1).as_deref() {
        Some("health") => health::check(&db_url, &mq_url).await,
        _ => {
            let _pool = db::connect(&db_url).await?;
            tracing::info!("connected to postgres");
            consumer::run(&mq_url).await
        }
    }
}
```

- [ ] **Step 8: Create `Dockerfile`**

```dockerfile
# services/engine/Dockerfile
FROM rust:1.95-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main() {}" > src/main.rs \
    && cargo build --release && rm src/main.rs
COPY src ./src
RUN touch src/main.rs && cargo build --release

FROM alpine:3.24
COPY --from=build /app/target/release/veloci-engine /veloci-engine
ENTRYPOINT ["/veloci-engine"]
```

- [ ] **Step 9: Commit**

```bash
git add services/engine/
git commit -m "feat: veloci-engine scaffold — RabbitMQ consumer, job dispatch stubs, health check"
```

---

## Task 5: veloci-web Scaffolding

**Files:**

- Create: `services/web/package.json` (via Vite scaffold)
- Create: `services/web/vite.config.ts`
- Create: `services/web/src/api/client.ts`
- Create: `services/web/src/auth/AuthProvider.tsx`
- Create: `services/web/src/auth/LoginPage.tsx`
- Create: `services/web/src/App.tsx`
- Create: `services/web/src/main.tsx`
- Create: `services/web/nginx.conf`
- Create: `services/web/Dockerfile`

**Interfaces:**

- Consumes: `POST /auth/login` on `veloci-api` (port 8080) — all auth through the API, never directly to veloci-auth
- Produces: login page → JWT in `localStorage` → subsequent API calls include `Authorization: Bearer`

---

- [ ] **Step 1: Scaffold and install dependencies**

```bash
cd services/web
npm create vite@8 . -- --template react-ts
npm install
npm install axios
```

- [ ] **Step 2: Configure Vite proxy — all traffic through veloci-api**

```typescript
// services/web/vite.config.ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
    },
  },
})
```

Note: all requests — including login — go through `/api` to `veloci-api`. The frontend never talks to `veloci-auth` directly.

- [ ] **Step 3: Implement `api/client.ts`**

```typescript
// services/web/src/api/client.ts
const BASE = import.meta.env.VITE_API_URL ?? '/api'

function authHeader(): Record<string, string> {
  const token = localStorage.getItem('token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function login(email: string, password: string): Promise<void> {
  const res = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  if (!res.ok) throw new Error('Invalid credentials')
  const { token } = await res.json()
  localStorage.setItem('token', token)
}

export function logout(): void {
  localStorage.removeItem('token')
}

export function isAuthenticated(): boolean {
  return !!localStorage.getItem('token')
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...authHeader(), ...init?.headers },
  })
  if (res.status === 401) {
    logout()
    window.location.href = '/login'
    throw new Error('Session expired')
  }
  if (!res.ok) throw new Error(`${res.status}`)
  return res.json()
}
```

- [ ] **Step 4: Implement `auth/AuthProvider.tsx`**

```typescript
// services/web/src/auth/AuthProvider.tsx
import React, { createContext, useContext, useState, useCallback } from 'react'
import { login as apiLogin, logout as apiLogout, isAuthenticated } from '../api/client'

interface AuthContextValue {
  authenticated: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(isAuthenticated)

  const login = useCallback(async (email: string, password: string) => {
    await apiLogin(email, password)
    setAuthenticated(true)
  }, [])

  const logout = useCallback(() => {
    apiLogout()
    setAuthenticated(false)
  }, [])

  return (
    <AuthContext.Provider value={{ authenticated, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
```

- [ ] **Step 5: Implement `auth/LoginPage.tsx`, `App.tsx`, `main.tsx`**

```typescript
// services/web/src/auth/LoginPage.tsx
import React, { useState, FormEvent } from 'react'
import { useAuth } from './AuthProvider'

export function LoginPage() {
  const { login } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try { await login(email, password) }
    catch { setError('Invalid email or password') }
    finally { setLoading(false) }
  }

  return (
    <main style={{ display: 'flex', justifyContent: 'center', paddingTop: '20vh' }}>
      <form onSubmit={handleSubmit} style={{ display: 'flex', flexDirection: 'column', gap: 12, width: 320 }}>
        <h1 style={{ margin: 0 }}>Veloci</h1>
        {error && <p style={{ color: 'red', margin: 0 }}>{error}</p>}
        <input type="email" value={email} onChange={e => setEmail(e.target.value)}
          placeholder="Email" required autoFocus />
        <input type="password" value={password} onChange={e => setPassword(e.target.value)}
          placeholder="Password" required />
        <button type="submit" disabled={loading}>{loading ? 'Signing in…' : 'Sign in'}</button>
      </form>
    </main>
  )
}
```

```typescript
// services/web/src/App.tsx
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { LoginPage } from './auth/LoginPage'

function Inner() {
  const { authenticated, logout } = useAuth()
  if (!authenticated) return <LoginPage />
  return (
    <main>
      <p>Authenticated — financial views coming soon.</p>
      <button onClick={logout}>Sign out</button>
    </main>
  )
}

export default function App() {
  return <AuthProvider><Inner /></AuthProvider>
}
```

```typescript
// services/web/src/main.tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode><App /></React.StrictMode>
)
```

- [ ] **Step 6: Create `nginx.conf` and `Dockerfile`**

```nginx
# services/web/nginx.conf
server {
  listen 80;
  root /usr/share/nginx/html;
  index index.html;
  location / { try_files $uri $uri/ /index.html; }
}
```

```dockerfile
# services/web/Dockerfile
FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
```

- [ ] **Step 7: Verify dev server starts**

```bash
cd services/web && npm run dev
```

Expected: Vite dev server at `http://localhost:5173`. Open browser — login form renders, no console errors.

- [ ] **Step 8: Commit**

```bash
git add services/web/
git commit -m "feat: veloci-web — React SPA, login through veloci-api, authenticated API client"
```

---

## Task 6: Integration Smoke Test

**Goal:** Verify all services start, migrations run, and the full login flow works end-to-end through `veloci-api`.

---

- [ ] **Step 1: Copy env files and start all services**

```bash
cp .env.example .env
cp config/veloci-auth.yaml.example config/veloci-auth.yaml
docker compose --env-file .env up --build -d
```

- [ ] **Step 2: Wait for health checks**

```bash
docker compose ps
```

Wait ~30 seconds. Expected: postgres `healthy`, rabbitmq `healthy`, all others `running`.

- [ ] **Step 3: Verify both databases exist**

```bash
docker compose exec postgres psql -U postgres -c "\l" | grep veloci
```

Expected output includes both `veloci_auth` and `veloci_app`.

- [ ] **Step 4: Verify auth schema**

```bash
docker compose exec postgres psql -U veloci_auth_user -d veloci_auth -c "\dt"
```

Expected: `auth_credentials`, `tokens`, `invite_tokens`

- [ ] **Step 5: Verify app schema and seeded roles**

```bash
docker compose exec postgres psql -U veloci_app_user -d veloci_app -c "\dt"
docker compose exec postgres psql -U veloci_app_user -d veloci_app \
  -c "SELECT name FROM roles ORDER BY name;"
```

Expected tables: `entities`, `entity_users`, `permissions`, `role_permissions`, `roles`, `users`

Expected roles:

```text
    name
-----------
 entity_admin
 entity_user
```

- [ ] **Step 6: Verify server_admin credential was synced by veloci-auth startup**

```bash
docker compose exec postgres psql -U veloci_auth_user -d veloci_auth \
  -c "SELECT email, system_role FROM auth_credentials;"
```

Expected:

```text
       email        | system_role
--------------------+-------------
 admin@veloci.local | server_admin
```

- [ ] **Step 7: Create a test entity and user in veloci_app**

The server admin credential exists in `veloci_auth`. We need a matching user + entity in `veloci_app` for the login flow to complete:

```bash
docker compose exec postgres psql -U veloci_app_user -d veloci_app <<'SQL'
INSERT INTO entities (name) VALUES ('Test Family');

-- auth_credential_id must match the ID from veloci_auth.auth_credentials
-- Fetch it first:
\! docker compose exec postgres psql -U veloci_auth_user -d veloci_auth \
     -t -c "SELECT id FROM auth_credentials WHERE email='admin@veloci.local';"
SQL
```

Then insert the user with the returned UUID:

```bash
CRED_ID=$(docker compose exec -T postgres psql -U veloci_auth_user -d veloci_auth \
  -t -c "SELECT id FROM auth_credentials WHERE email='admin@veloci.local';" | tr -d ' ')

docker compose exec postgres psql -U veloci_app_user -d veloci_app <<SQL
INSERT INTO users (auth_credential_id, email) VALUES ('${CRED_ID}', 'admin@veloci.local');

INSERT INTO entity_users (user_id, entity_id, entity_role)
SELECT u.id, e.id, 'entity_admin'
FROM users u, entities e
WHERE u.email = 'admin@veloci.local' AND e.name = 'Test Family';
SQL
```

- [ ] **Step 8: Test login through veloci-api**

```bash
curl -s -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@veloci.local","password":"changeme"}' | jq .
```

Expected:

```json
{ "token": "<jwt string>", "expires_at": "..." }
```

- [ ] **Step 9: Test authenticated request**

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@veloci.local","password":"changeme"}' | jq -r .token)

curl -s http://localhost:8080/health \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Expected: `{ "status": "ok" }`

- [ ] **Step 10: Verify engine is consuming**

```bash
docker compose logs veloci-engine | tail -5
```

Expected: `connected to postgres` and `consuming from veloci.jobs`

- [ ] **Step 11: Final commit**

```bash
git add .
git commit -m "feat: architecture scaffold complete — all six services integrated and smoke tested"
```
