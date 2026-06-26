# Veloci Architecture Scaffold — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the six-service architecture (veloci-api, veloci-auth, veloci-engine, veloci-web, Postgres, RabbitMQ) with working auth flow, JWT validation middleware, RabbitMQ job publishing/consuming, and entity-scoped API scaffolding.

**Architecture:** Monorepo under `services/`. Each service is independently buildable and deployable via Docker. Postgres migrations run at container init. `veloci-auth` issues JWTs; `veloci-api` validates them locally on each request without calling auth. The frontend talks to both auth (login) and api (everything else).

**Tech Stack:**
- Go 1.25 — `chi/v5` router, `pgx/v5` Postgres driver, `amqp091-go` RabbitMQ client, `golang-jwt/jwt/v5`, `golang.org/x/crypto` bcrypt
- Rust 1.87 — `tokio` async runtime, `sqlx` Postgres, `lapin` RabbitMQ, `serde`/`serde_json`, `anyhow`, `tracing`
- React 19 + Vite 6 + TypeScript 5.8
- Postgres 17, RabbitMQ 4.0-alpine

## Global Constraints
- All financial data rows must include `entity_id UUID NOT NULL` (enforced in data model spec — scaffolded here as a contract)
- JWT payload always carries exactly: `user_id`, `entity_id`, `role`
- Passwords stored as bcrypt hashes, cost 12
- All Go services use `chi/v5` for routing
- All Rust async code uses `tokio` multi-thread runtime
- No Postgres or RabbitMQ ports exposed externally in docker-compose
- All secrets via environment variables — zero hardcoded credentials
- `veloci-engine` is internal only — no exposed port

---

## File Map

```
veloci/
├── docker-compose.yml
├── .env.example
├── migrations/
│   ├── 001_auth_schema.sql
│   └── 002_rbac_seed.sql
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
    │       └── handlers/
    │           ├── login.go
    │           └── login_test.go
    ├── api/
    │   ├── go.mod
    │   ├── main.go
    │   ├── Dockerfile
    │   └── internal/
    │       ├── middleware/
    │       │   ├── auth.go
    │       │   └── auth_test.go
    │       ├── queue/
    │       │   ├── publisher.go
    │       │   └── publisher_test.go
    │       └── handlers/
    │           └── health.go
    ├── engine/
    │   ├── Cargo.toml
    │   ├── Dockerfile
    │   └── src/
    │       ├── main.rs
    │       ├── consumer.rs
    │       ├── db.rs
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

## Task 1: Project Scaffold + Infrastructure

**Files:**
- Create: `docker-compose.yml`
- Create: `.env.example`
- Create: `migrations/001_auth_schema.sql`
- Create: `migrations/002_rbac_seed.sql`

**Interfaces:**
- Produces: running Postgres with auth schema and seeded roles/permissions; running RabbitMQ; environment contract for all services

---

- [ ] **Step 1: Create `.env.example`**

```bash
# .env.example
POSTGRES_USER=veloci
POSTGRES_PASSWORD=changeme
POSTGRES_DB=veloci
JWT_SECRET=change-this-to-a-long-random-secret
RABBITMQ_USER=veloci
RABBITMQ_PASSWORD=changeme
```

Copy to `.env` and fill in real values before running:
```bash
cp .env.example .env
```

- [ ] **Step 2: Create `migrations/001_auth_schema.sql`**

```sql
-- migrations/001_auth_schema.sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        UNIQUE NOT NULL,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE entities (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE permissions (
    id   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE role_permissions (
    role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE entity_users (
    user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entity_id UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    role_id   UUID NOT NULL REFERENCES roles(id),
    PRIMARY KEY (user_id, entity_id)
);
```

- [ ] **Step 3: Create `migrations/002_rbac_seed.sql`**

```sql
-- migrations/002_rbac_seed.sql
INSERT INTO roles (name) VALUES ('admin'), ('member');

INSERT INTO permissions (name) VALUES
    ('accounts:read'),
    ('accounts:write'),
    ('import:create'),
    ('rules:write'),
    ('entries:write'),
    ('reports:read'),
    ('users:manage');

-- admin gets every permission
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r CROSS JOIN permissions p
WHERE r.name = 'admin';

-- member gets read + entries + reports
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('accounts:read', 'entries:write', 'reports:read')
WHERE r.name = 'member';
```

- [ ] **Step 4: Create `docker-compose.yml`**

```yaml
version: '3.9'

services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./migrations:/docker-entrypoint-initdb.d
    networks:
      - veloci
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}"]
      interval: 5s
      retries: 5

  rabbitmq:
    image: rabbitmq:4.0-alpine
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
      DATABASE_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}
      JWT_SECRET: ${JWT_SECRET}
      PORT: "8081"
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - veloci
    ports:
      - "8081:8081"

  veloci-api:
    build: ./services/api
    environment:
      DATABASE_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}
      JWT_SECRET: ${JWT_SECRET}
      RABBITMQ_URL: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/
      PORT: "8080"
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy
    networks:
      - veloci
    ports:
      - "8080:8080"

  veloci-engine:
    build: ./services/engine
    environment:
      DATABASE_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}
      RABBITMQ_URL: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/
    depends_on:
      postgres:
        condition: service_healthy
      rabbitmq:
        condition: service_healthy
    networks:
      - veloci

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

- [ ] **Step 5: Verify migrations are ordered correctly**

```bash
ls migrations/
```
Expected: `001_auth_schema.sql  002_rbac_seed.sql`

Postgres init runs files in alphabetical order — the `00N_` prefix guarantees this.

- [ ] **Step 6: Commit**

```bash
git add docker-compose.yml .env.example migrations/
git commit -m "feat: project scaffold, docker-compose, postgres migrations"
```

---

## Task 2: veloci-auth Service

**Files:**
- Create: `services/auth/go.mod`
- Create: `services/auth/main.go`
- Create: `services/auth/internal/tokens/jwt.go`
- Create: `services/auth/internal/tokens/jwt_test.go`
- Create: `services/auth/internal/db/db.go`
- Create: `services/auth/internal/handlers/login.go`
- Create: `services/auth/internal/handlers/login_test.go`
- Create: `services/auth/Dockerfile`

**Interfaces:**
- Consumes: Postgres `users`, `entities`, `entity_users`, `roles` tables from Task 1
- Produces:
  - `POST /login` → `{"token": "<jwt>"}` where JWT payload is `{"user_id": "...", "entity_id": "...", "role": "..."}`
  - `tokens.Issue(secret []byte, userID, entityID, role string) (string, error)`
  - `tokens.Parse(secret []byte, tokenStr string) (*tokens.Claims, error)`
  - `tokens.Claims` struct with fields `UserID string`, `EntityID string`, `Role string`

---

- [ ] **Step 1: Initialize Go module**

```bash
mkdir -p services/auth/internal/{tokens,db,handlers}
cd services/auth
go mod init github.com/veloci/auth
go get github.com/go-chi/chi/v5
go get github.com/jackc/pgx/v5
go get github.com/golang-jwt/jwt/v5
go get golang.org/x/crypto
```

- [ ] **Step 2: Write failing tests for JWT**

```go
// services/auth/internal/tokens/jwt_test.go
package tokens_test

import (
    "testing"
    "github.com/veloci/auth/internal/tokens"
)

func TestIssueAndParse(t *testing.T) {
    secret := []byte("test-secret")
    tok, err := tokens.Issue(secret, "user-1", "entity-1", "admin")
    if err != nil {
        t.Fatalf("Issue: %v", err)
    }
    claims, err := tokens.Parse(secret, tok)
    if err != nil {
        t.Fatalf("Parse: %v", err)
    }
    if claims.UserID != "user-1" {
        t.Errorf("UserID: got %q, want %q", claims.UserID, "user-1")
    }
    if claims.EntityID != "entity-1" {
        t.Errorf("EntityID: got %q, want %q", claims.EntityID, "entity-1")
    }
    if claims.Role != "admin" {
        t.Errorf("Role: got %q, want %q", claims.Role, "admin")
    }
}

func TestParseRejectsWrongSecret(t *testing.T) {
    tok, _ := tokens.Issue([]byte("secret-a"), "u", "e", "admin")
    _, err := tokens.Parse([]byte("secret-b"), tok)
    if err == nil {
        t.Error("expected error with wrong secret, got nil")
    }
}
```

- [ ] **Step 3: Run tests — verify they fail**

```bash
cd services/auth && go test ./internal/tokens/...
```
Expected: `cannot find package "github.com/veloci/auth/internal/tokens"`

- [ ] **Step 4: Implement `tokens/jwt.go`**

```go
// services/auth/internal/tokens/jwt.go
package tokens

import (
    "fmt"
    "time"
    "github.com/golang-jwt/jwt/v5"
)

type Claims struct {
    UserID   string `json:"user_id"`
    EntityID string `json:"entity_id"`
    Role     string `json:"role"`
    jwt.RegisteredClaims
}

func Issue(secret []byte, userID, entityID, role string) (string, error) {
    claims := Claims{
        UserID:   userID,
        EntityID: entityID,
        Role:     role,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
        },
    }
    return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func Parse(secret []byte, tokenStr string) (*Claims, error) {
    token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
        if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
        }
        return secret, nil
    })
    if err != nil {
        return nil, err
    }
    claims, ok := token.Claims.(*Claims)
    if !ok || !token.Valid {
        return nil, fmt.Errorf("invalid token")
    }
    return claims, nil
}
```

- [ ] **Step 5: Run tests — verify they pass**

```bash
cd services/auth && go test ./internal/tokens/... -v
```
Expected:
```
--- PASS: TestIssueAndParse (0.00s)
--- PASS: TestParseRejectsWrongSecret (0.00s)
PASS
```

- [ ] **Step 6: Implement `db/db.go`**

```go
// services/auth/internal/db/db.go
package db

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ pool *pgxpool.Pool }

type UserRow struct {
    ID           string
    PasswordHash string
    EntityID     string
    RoleName     string
}

func New(ctx context.Context, dsn string) (*DB, error) {
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil {
        return nil, err
    }
    return &DB{pool: pool}, nil
}

func (d *DB) FindUserByEmail(ctx context.Context, email string) (*UserRow, error) {
    row := &UserRow{}
    err := d.pool.QueryRow(ctx, `
        SELECT u.id, u.password_hash, eu.entity_id, r.name
        FROM users u
        JOIN entity_users eu ON eu.user_id = u.id
        JOIN roles r ON r.id = eu.role_id
        WHERE u.email = $1
        LIMIT 1
    `, email).Scan(&row.ID, &row.PasswordHash, &row.EntityID, &row.RoleName)
    if err != nil {
        return nil, err
    }
    return row, nil
}
```

- [ ] **Step 7: Write failing test for login handler**

```go
// services/auth/internal/handlers/login_test.go
package handlers_test

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/auth/internal/handlers"
    "github.com/veloci/auth/internal/tokens"
    "golang.org/x/crypto/bcrypt"
)

type stubDB struct{ fail bool }

func (s *stubDB) FindUserByEmail(_ context.Context, email string) (*handlers.UserLookup, error) {
    if s.fail {
        return nil, fmt.Errorf("not found")
    }
    // generate bcrypt hash of "password" at cost 12 for the stub
    hash, _ := bcrypt.GenerateFromPassword([]byte("password"), 12)
    return &handlers.UserLookup{
        ID:           "user-1",
        PasswordHash: string(hash),
        EntityID:     "entity-1",
        RoleName:     "admin",
    }, nil
}

func TestLoginSuccess(t *testing.T) {
    secret := []byte("test-secret")
    h := handlers.NewLogin(&stubDB{fail: false}, secret)

    body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "password"})
    req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()

    h.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status: got %d, want 200", w.Code)
    }
    var resp map[string]string
    json.NewDecoder(w.Body).Decode(&resp)
    if resp["token"] == "" {
        t.Error("expected token in response")
    }
    claims, err := tokens.Parse(secret, resp["token"])
    if err != nil {
        t.Fatalf("token invalid: %v", err)
    }
    if claims.UserID != "user-1" || claims.EntityID != "entity-1" || claims.Role != "admin" {
        t.Errorf("unexpected claims: %+v", claims)
    }
}

func TestLoginBadCredentials(t *testing.T) {
    h := handlers.NewLogin(&stubDB{fail: true}, []byte("s"))
    body, _ := json.Marshal(map[string]string{"email": "x@x.com", "password": "wrong"})
    req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    h.ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Errorf("status: got %d, want 401", w.Code)
    }
}
```

- [ ] **Step 8: Run tests — verify they fail**

```bash
cd services/auth && go test ./internal/handlers/... 2>&1 | head -5
```
Expected: compile error — `handlers` package not found.

- [ ] **Step 9: Implement `handlers/login.go`**

```go
// services/auth/internal/handlers/login.go
package handlers

import (
    "context"
    "encoding/json"
    "net/http"
    "github.com/veloci/auth/internal/tokens"
    "golang.org/x/crypto/bcrypt"
)

type UserLookup struct {
    ID           string
    PasswordHash string
    EntityID     string
    RoleName     string
}

type userDB interface {
    FindUserByEmail(ctx context.Context, email string) (*UserLookup, error)
}

type loginHandler struct {
    db     userDB
    secret []byte
}

func NewLogin(db userDB, secret []byte) http.Handler {
    return &loginHandler{db: db, secret: secret}
}

func (h *loginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Email    string `json:"email"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid request", http.StatusBadRequest)
        return
    }

    user, err := h.db.FindUserByEmail(r.Context(), req.Email)
    if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
        http.Error(w, "invalid credentials", http.StatusUnauthorized)
        return
    }

    tok, err := tokens.Issue(h.secret, user.ID, user.EntityID, user.RoleName)
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"token": tok})
}
```

- [ ] **Step 10: Run tests — verify they pass**

```bash
cd services/auth && go test ./internal/... -v
```
Expected: all tests PASS.

- [ ] **Step 11: Implement `main.go`**

```go
// services/auth/main.go
package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "github.com/go-chi/chi/v5"
    "github.com/veloci/auth/internal/db"
    "github.com/veloci/auth/internal/handlers"
)

func main() {
    ctx := context.Background()
    database, err := db.New(ctx, os.Getenv("DATABASE_URL"))
    if err != nil {
        log.Fatalf("db: %v", err)
    }
    secret := []byte(os.Getenv("JWT_SECRET"))
    port := os.Getenv("PORT")
    if port == "" {
        port = "8081"
    }

    r := chi.NewRouter()
    r.Post("/login", handlers.NewLogin(database, secret).ServeHTTP)

    log.Printf("veloci-auth listening on :%s", port)
    log.Fatal(http.ListenAndServe(":"+port, r))
}
```

- [ ] **Step 12: Create `Dockerfile`**

```dockerfile
# services/auth/Dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o auth .

FROM alpine:3.21
COPY --from=build /app/auth /auth
ENTRYPOINT ["/auth"]
```

- [ ] **Step 13: Commit**

```bash
git add services/auth/
git commit -m "feat: veloci-auth service with JWT issuance and login endpoint"
```

---

## Task 3: veloci-api Scaffolding

**Files:**
- Create: `services/api/go.mod`
- Create: `services/api/main.go`
- Create: `services/api/internal/middleware/auth.go`
- Create: `services/api/internal/middleware/auth_test.go`
- Create: `services/api/internal/queue/publisher.go`
- Create: `services/api/internal/queue/publisher_test.go`
- Create: `services/api/internal/handlers/health.go`
- Create: `services/api/Dockerfile`

**Interfaces:**
- Consumes: `tokens.Claims` shape from Task 2 — same `user_id`/`entity_id`/`role` JWT payload
- Produces:
  - `middleware.Authenticate(secret []byte) func(http.Handler) http.Handler` — validates JWT, injects claims into context
  - `middleware.EntityID(ctx context.Context) string` — extracts entity_id from context
  - `middleware.UserID(ctx context.Context) string` — extracts user_id from context
  - `queue.Publisher` — `Publish(ctx, job) error`
  - `queue.Job` struct with fields `Type string`, `EntityID string`, `Payload json.RawMessage`
  - `GET /health` → `{"status":"ok"}`

---

- [ ] **Step 1: Initialize Go module**

```bash
mkdir -p services/api/internal/{middleware,queue,handlers}
cd services/api
go mod init github.com/veloci/api
go get github.com/go-chi/chi/v5
go get github.com/jackc/pgx/v5
go get github.com/rabbitmq/amqp091-go
go get github.com/golang-jwt/jwt/v5
```

Note: copy `tokens` package from `services/auth` — or extract to a shared module. For v1, duplicate it in `services/api/internal/tokens/` to keep services independently deployable.

```bash
mkdir -p services/api/internal/tokens
cp services/auth/internal/tokens/jwt.go services/api/internal/tokens/
# Update package declaration: change `github.com/veloci/auth/internal/tokens` imports to `github.com/veloci/api/internal/tokens`
sed -i '' 's|github.com/veloci/auth|github.com/veloci/api|g' services/api/internal/tokens/jwt.go
```

- [ ] **Step 2: Write failing tests for auth middleware**

```go
// services/api/internal/middleware/auth_test.go
package middleware_test

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    "github.com/veloci/api/internal/middleware"
    "github.com/veloci/api/internal/tokens"
)

func TestAuthMiddlewareInjectsClaims(t *testing.T) {
    secret := []byte("test-secret")
    tok, _ := tokens.Issue(secret, "user-1", "entity-1", "admin")

    var capturedEntityID, capturedUserID string
    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedEntityID = middleware.EntityID(r.Context())
        capturedUserID = middleware.UserID(r.Context())
        w.WriteHeader(http.StatusOK)
    })

    req := httptest.NewRequest(http.MethodGet, "/", nil)
    req.Header.Set("Authorization", "Bearer "+tok)
    w := httptest.NewRecorder()

    middleware.Authenticate(secret)(next).ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("status: got %d, want 200", w.Code)
    }
    if capturedEntityID != "entity-1" {
        t.Errorf("EntityID: got %q, want %q", capturedEntityID, "entity-1")
    }
    if capturedUserID != "user-1" {
        t.Errorf("UserID: got %q, want %q", capturedUserID, "user-1")
    }
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    w := httptest.NewRecorder()
    middleware.Authenticate([]byte("s"))(next).ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Errorf("status: got %d, want 401", w.Code)
    }
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    req.Header.Set("Authorization", "Bearer not-a-token")
    w := httptest.NewRecorder()
    middleware.Authenticate([]byte("s"))(next).ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Errorf("status: got %d, want 401", w.Code)
    }
}
```

- [ ] **Step 3: Run tests — verify they fail**

```bash
cd services/api && go test ./internal/middleware/... 2>&1 | head -5
```
Expected: compile error — `middleware` package not found.

- [ ] **Step 4: Implement `middleware/auth.go`**

```go
// services/api/internal/middleware/auth.go
package middleware

import (
    "context"
    "net/http"
    "strings"
    "github.com/veloci/api/internal/tokens"
)

type contextKey string

const (
    ctxUserID   contextKey = "user_id"
    ctxEntityID contextKey = "entity_id"
    ctxRole     contextKey = "role"
)

func Authenticate(secret []byte) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            header := r.Header.Get("Authorization")
            if !strings.HasPrefix(header, "Bearer ") {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            claims, err := tokens.Parse(secret, strings.TrimPrefix(header, "Bearer "))
            if err != nil {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
            ctx = context.WithValue(ctx, ctxEntityID, claims.EntityID)
            ctx = context.WithValue(ctx, ctxRole, claims.Role)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func EntityID(ctx context.Context) string {
    v, _ := ctx.Value(ctxEntityID).(string)
    return v
}

func UserID(ctx context.Context) string {
    v, _ := ctx.Value(ctxUserID).(string)
    return v
}

func Role(ctx context.Context) string {
    v, _ := ctx.Value(ctxRole).(string)
    return v
}
```

- [ ] **Step 5: Run middleware tests — verify they pass**

```bash
cd services/api && go test ./internal/middleware/... -v
```
Expected: all 3 tests PASS.

- [ ] **Step 6: Write failing test for queue publisher**

```go
// services/api/internal/queue/publisher_test.go
package queue_test

import (
    "context"
    "encoding/json"
    "testing"
    "github.com/veloci/api/internal/queue"
)

func TestJobMarshal(t *testing.T) {
    payload, _ := json.Marshal(map[string]string{"import_id": "abc"})
    job := queue.Job{
        Type:     "import.process",
        EntityID: "entity-1",
        Payload:  payload,
    }
    b, err := json.Marshal(job)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var out queue.Job
    if err := json.Unmarshal(b, &out); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if out.Type != "import.process" || out.EntityID != "entity-1" {
        t.Errorf("roundtrip failed: %+v", out)
    }
}
```

- [ ] **Step 7: Implement `queue/publisher.go`**

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
    Type     string          `json:"type"`
    EntityID string          `json:"entity_id"`
    Payload  json.RawMessage `json:"payload"`
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

- [ ] **Step 8: Run queue tests — verify they pass**

```bash
cd services/api && go test ./internal/queue/... -v
```
Expected: `TestJobMarshal PASS`

- [ ] **Step 9: Implement health handler and `main.go`**

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
    "log"
    "net/http"
    "os"
    "github.com/go-chi/chi/v5"
    "github.com/veloci/api/internal/handlers"
    "github.com/veloci/api/internal/middleware"
)

func main() {
    secret := []byte(os.Getenv("JWT_SECRET"))
    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }

    r := chi.NewRouter()
    r.Get("/health", handlers.Health)

    r.Group(func(r chi.Router) {
        r.Use(middleware.Authenticate(secret))
        // Financial routes added in subsequent specs
    })

    log.Printf("veloci-api listening on :%s", port)
    log.Fatal(http.ListenAndServe(":"+port, r))
}
```

- [ ] **Step 10: Create `Dockerfile`**

```dockerfile
# services/api/Dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o api .

FROM alpine:3.21
COPY --from=build /app/api /api
ENTRYPOINT ["/api"]
```

- [ ] **Step 11: Commit**

```bash
git add services/api/
git commit -m "feat: veloci-api scaffolding with JWT middleware and RabbitMQ publisher"
```

---

## Task 4: veloci-engine Scaffolding

**Files:**
- Create: `services/engine/Cargo.toml`
- Create: `services/engine/src/main.rs`
- Create: `services/engine/src/consumer.rs`
- Create: `services/engine/src/db.rs`
- Create: `services/engine/src/jobs/mod.rs`
- Create: `services/engine/Dockerfile`

**Interfaces:**
- Consumes: `queue.Job` envelope from Task 3 — `{"type": "...", "entity_id": "...", "payload": {...}}`
- Produces: persistent RabbitMQ consumer that dispatches to job handlers; handlers are stubs returning `Ok(())` (real logic added in engine spec)

---

- [ ] **Step 1: Initialize Rust project**

```bash
cd services/engine
cargo init --name veloci-engine
mkdir -p src/jobs
```

- [ ] **Step 2: Set `Cargo.toml` dependencies**

```toml
# services/engine/Cargo.toml
[package]
name = "veloci-engine"
version = "0.1.0"
edition = "2021"

[dependencies]
tokio = { version = "1", features = ["full"] }
lapin = "2"
sqlx = { version = "0.8", features = ["postgres", "runtime-tokio", "uuid", "chrono"] }
serde = { version = "1", features = ["derive"] }
serde_json = "1"
anyhow = "1"
tracing = "0.1"
tracing-subscriber = { version = "0.3", features = ["env-filter"] }
```

- [ ] **Step 3: Write test for job dispatch routing**

```rust
// services/engine/src/jobs/mod.rs (start with tests)
#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[tokio::test]
    async fn test_known_job_types_dispatch_without_error() {
        // Stubs return Ok — we're testing routing only, not logic
        for job_type in &["import.process", "rules.reprocess", "account.analyze"] {
            let job = Job {
                r#type: job_type.to_string(),
                entity_id: "entity-1".to_string(),
                payload: json!({}),
            };
            let result = dispatch(job).await;
            assert!(result.is_ok(), "dispatch failed for {}: {:?}", job_type, result);
        }
    }

    #[tokio::test]
    async fn test_unknown_job_type_returns_ok() {
        let job = Job {
            r#type: "unknown.type".to_string(),
            entity_id: "entity-1".to_string(),
            payload: json!({}),
        };
        // Unknown types are logged and dropped — not an error
        assert!(dispatch(job).await.is_ok());
    }
}
```

- [ ] **Step 4: Run tests — verify they fail**

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
    pub r#type: String,
    pub entity_id: String,
    pub payload: serde_json::Value,
}

pub async fn dispatch(job: Job) -> Result<()> {
    match job.r#type.as_str() {
        "import.process" => import_process(&job.entity_id, job.payload).await,
        "rules.reprocess" => rules_reprocess(&job.entity_id, job.payload).await,
        "account.analyze" => account_analyze(&job.entity_id, job.payload).await,
        other => {
            tracing::warn!("unknown job type: {}", other);
            Ok(())
        }
    }
}

async fn import_process(_entity_id: &str, _payload: serde_json::Value) -> Result<()> {
    tracing::info!("import.process stub");
    Ok(())
}

async fn rules_reprocess(_entity_id: &str, _payload: serde_json::Value) -> Result<()> {
    tracing::info!("rules.reprocess stub");
    Ok(())
}

async fn account_analyze(_entity_id: &str, _payload: serde_json::Value) -> Result<()> {
    tracing::info!("account.analyze stub");
    Ok(())
}
```

- [ ] **Step 6: Run tests — verify they pass**

```bash
cd services/engine && cargo test -- --nocapture
```
Expected:
```
test jobs::tests::test_known_job_types_dispatch_without_error ... ok
test jobs::tests::test_unknown_job_type_returns_ok ... ok
test result: ok. 2 passed
```

- [ ] **Step 7: Implement `consumer.rs`**

```rust
// services/engine/src/consumer.rs
use anyhow::Result;
use lapin::{
    Connection, ConnectionProperties,
    options::{BasicAckOptions, BasicConsumeOptions, QueueDeclareOptions},
    types::FieldTable,
};
use crate::jobs::{self, Job};

const QUEUE: &str = "veloci.jobs";

pub async fn run(rabbitmq_url: &str) -> Result<()> {
    let conn = Connection::connect(rabbitmq_url, ConnectionProperties::default()).await?;
    let channel = conn.create_channel().await?;

    channel.queue_declare(
        QUEUE,
        QueueDeclareOptions { durable: true, ..Default::default() },
        FieldTable::default(),
    ).await?;

    let mut consumer = channel.basic_consume(
        QUEUE,
        "veloci-engine",
        BasicConsumeOptions::default(),
        FieldTable::default(),
    ).await?;

    tracing::info!("veloci-engine consuming from {}", QUEUE);

    use futures_lite::StreamExt;
    while let Some(delivery) = consumer.next().await {
        let delivery = delivery?;
        match serde_json::from_slice::<Job>(&delivery.data) {
            Ok(job) => {
                let entity_id = job.entity_id.clone();
                let job_type = job.r#type.clone();
                if let Err(e) = jobs::dispatch(job).await {
                    tracing::error!("job failed entity={} type={}: {:?}", entity_id, job_type, e);
                }
            }
            Err(e) => tracing::error!("malformed job: {:?}", e),
        }
        delivery.ack(BasicAckOptions::default()).await?;
    }
    Ok(())
}
```

Add `futures-lite = "2"` to `Cargo.toml` dependencies.

- [ ] **Step 8: Implement `db.rs` and `main.rs`**

```rust
// services/engine/src/db.rs
use anyhow::Result;
use sqlx::PgPool;

pub async fn connect(database_url: &str) -> Result<PgPool> {
    let pool = PgPool::connect(database_url).await?;
    Ok(pool)
}
```

```rust
// services/engine/src/main.rs
mod consumer;
mod db;
mod jobs;

use anyhow::Result;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required");
    let rabbitmq_url = std::env::var("RABBITMQ_URL").expect("RABBITMQ_URL required");

    let _pool = db::connect(&database_url).await?;
    tracing::info!("veloci-engine connected to postgres");

    consumer::run(&rabbitmq_url).await
}
```

- [ ] **Step 9: Create `Dockerfile`**

```dockerfile
# services/engine/Dockerfile
FROM rust:1.87-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main() {}" > src/main.rs && cargo build --release && rm src/main.rs
COPY src ./src
RUN touch src/main.rs && cargo build --release

FROM alpine:3.21
COPY --from=build /app/target/release/veloci-engine /veloci-engine
ENTRYPOINT ["/veloci-engine"]
```

- [ ] **Step 10: Commit**

```bash
git add services/engine/
git commit -m "feat: veloci-engine scaffolding with RabbitMQ consumer and job dispatch stubs"
```

---

## Task 5: veloci-web Scaffolding

**Files:**
- Create: `services/web/package.json`
- Create: `services/web/vite.config.ts`
- Create: `services/web/tsconfig.json`
- Create: `services/web/index.html`
- Create: `services/web/src/main.tsx`
- Create: `services/web/src/App.tsx`
- Create: `services/web/src/api/client.ts`
- Create: `services/web/src/auth/AuthProvider.tsx`
- Create: `services/web/src/auth/LoginPage.tsx`
- Create: `services/web/nginx.conf`
- Create: `services/web/Dockerfile`

**Interfaces:**
- Consumes: `POST /login` on `veloci-auth` (port 8081), `GET /health` on `veloci-api` (port 8080)
- Produces: working login flow — user enters credentials, JWT stored in `localStorage`, subsequent API calls include `Authorization: Bearer <token>`

---

- [ ] **Step 1: Initialize project**

```bash
cd services/web
npm create vite@6 . -- --template react-ts
npm install
npm install axios
# Vite 6 scaffolds React 19 + TypeScript 5.8 by default
```

- [ ] **Step 2: Configure Vite with API proxy**

```typescript
// services/web/vite.config.ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', rewrite: (p) => p.replace(/^\/api/, '') },
      '/auth': { target: 'http://localhost:8081', rewrite: (p) => p.replace(/^\/auth/, '') },
    },
  },
})
```

- [ ] **Step 3: Implement `api/client.ts`**

```typescript
// services/web/src/api/client.ts
const AUTH_URL = import.meta.env.VITE_AUTH_URL ?? '/auth'
const API_URL  = import.meta.env.VITE_API_URL  ?? '/api'

function authHeader(): Record<string, string> {
  const token = localStorage.getItem('token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function login(email: string, password: string): Promise<void> {
  const res = await fetch(`${AUTH_URL}/login`, {
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
  const res = await fetch(`${API_URL}${path}`, {
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

- [ ] **Step 5: Implement `auth/LoginPage.tsx`**

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
    try {
      await login(email, password)
    } catch {
      setError('Invalid email or password')
    } finally {
      setLoading(false)
    }
  }

  return (
    <main style={{ display: 'flex', justifyContent: 'center', paddingTop: '20vh' }}>
      <form onSubmit={handleSubmit} style={{ display: 'flex', flexDirection: 'column', gap: 12, width: 320 }}>
        <h1 style={{ margin: 0 }}>Veloci</h1>
        {error && <p style={{ color: 'red', margin: 0 }}>{error}</p>}
        <input
          type="email" value={email} onChange={e => setEmail(e.target.value)}
          placeholder="Email" required autoFocus
        />
        <input
          type="password" value={password} onChange={e => setPassword(e.target.value)}
          placeholder="Password" required
        />
        <button type="submit" disabled={loading}>
          {loading ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
```

- [ ] **Step 6: Implement `App.tsx` and `main.tsx`**

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

- [ ] **Step 7: Create `nginx.conf` and `Dockerfile`**

```nginx
# services/web/nginx.conf
server {
    listen 80;
    root /usr/share/nginx/html;
    index index.html;
    location / {
        try_files $uri $uri/ /index.html;
    }
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

- [ ] **Step 8: Verify dev server starts**

```bash
cd services/web && npm run dev
```
Expected: Vite dev server at `http://localhost:5173`. Open browser, see login form. No console errors.

- [ ] **Step 9: Commit**

```bash
git add services/web/
git commit -m "feat: veloci-web React SPA with login flow and authenticated API client"
```

---

## Task 6: Integration Smoke Test

**Goal:** Verify all six services come up healthy, migrations run, and the login → JWT → API flow works end-to-end.

---

- [ ] **Step 1: Build and start all services**

```bash
docker compose --env-file .env up --build -d
```

- [ ] **Step 2: Wait for health checks**

```bash
docker compose ps
```
Expected: all services `healthy` or `running`. Postgres and RabbitMQ show `healthy`. Give it ~30 seconds.

- [ ] **Step 3: Verify migrations ran**

```bash
docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "\dt"
```
Expected output includes: `users`, `entities`, `roles`, `permissions`, `role_permissions`, `entity_users`

- [ ] **Step 4: Verify seeded roles**

```bash
docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB -c "SELECT name FROM roles;"
```
Expected:
```
 name
--------
 admin
 member
```

- [ ] **Step 5: Create a test user + entity (required for login)**

First generate a real bcrypt hash for 'testpassword':
```bash
# Requires htpasswd (brew install httpd) or use the Go one-liner:
docker run --rm golang:1.25-alpine sh -c \
  'go run -e "import (\"fmt\";\"golang.org/x/crypto/bcrypt\"); func main() { h,_:=bcrypt.GenerateFromPassword([]byte(\"testpassword\"),12); fmt.Println(string(h)) }"'
```

Alternatively, run this Go snippet locally and copy the output hash:
```go
// run: go run hash.go
package main
import ("fmt"; "golang.org/x/crypto/bcrypt")
func main() {
    h, _ := bcrypt.GenerateFromPassword([]byte("testpassword"), 12)
    fmt.Println(string(h))
}
```

Then insert with that hash:
```bash
HASH='<paste bcrypt hash here>'
docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB << SQL
INSERT INTO users (email, password_hash)
VALUES ('admin@veloci.local', '$HASH');

INSERT INTO entities (name) VALUES ('Test Family') RETURNING id;
SQL
```

Note the returned entity UUID, then:

```bash
docker compose exec postgres psql -U $POSTGRES_USER -d $POSTGRES_DB << 'SQL'
INSERT INTO entity_users (user_id, entity_id, role_id)
SELECT u.id, e.id, r.id
FROM users u, entities e, roles r
WHERE u.email = 'admin@veloci.local'
  AND e.name = 'Test Family'
  AND r.name = 'admin';
SQL
```

- [ ] **Step 6: Test login endpoint**

```bash
curl -s -X POST http://localhost:8081/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@veloci.local","password":"testpassword"}' | jq .
```
Expected:
```json
{ "token": "<jwt string>" }
```

- [ ] **Step 7: Test API health with JWT**

```bash
TOKEN=$(curl -s -X POST http://localhost:8081/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@veloci.local","password":"testpassword"}' | jq -r .token)

curl -s http://localhost:8080/health \
  -H "Authorization: Bearer $TOKEN" | jq .
```
Expected:
```json
{ "status": "ok" }
```

- [ ] **Step 8: Verify engine is consuming (check logs)**

```bash
docker compose logs veloci-engine | tail -5
```
Expected: `veloci-engine connected to postgres` and `veloci-engine consuming from veloci.jobs`

- [ ] **Step 9: Final commit**

```bash
git add .
git commit -m "feat: architecture scaffold complete — all six services integrated and smoke tested"
```
