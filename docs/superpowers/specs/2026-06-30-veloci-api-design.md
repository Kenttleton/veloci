# Veloci — API Service Design

**Date:** 2026-06-30  
**Revised:** 2026-07-13  
**Status:** Approved  
**Scope:** veloci-api service — REST endpoints, RBAC enforcement, job orchestration, SSE job streaming, data model

---

## 1. Overview

`veloci-api` is the single entry point for the frontend. It has four responsibilities:

1. **Auth orchestration** — veloci-api exposes all auth-facing routes; the UI never calls veloci-auth directly
2. **Financial data CRUD** — all permanent configuration and transient read access
3. **Job orchestration** — publish jobs to RabbitMQ, stream status via SSE
4. **RBAC** — own and enforce the permissions model; seed roles and permissions at migration

### Data model: permanent vs transient

Two categories of data live in `veloci_app`:

| Category | Tables | Characteristics |
| --- | --- | --- |
| Permanent | `entries`, `classifications`, `labels`, `institution_mappings`, `accounts`, `users`, `entities`, `entity_users` | User-owned configuration. Full CRUD. |
| Transient | `transactions`, `snapshots`, `projections`, `processing_jobs`, `review_queue` | Engine output. Read-only via API. Rebuilt when entries change. |

**`transactions` is append-only** — never modified after import.

**`entries`** is the core financial signal table — one row per continuous rate signal instance. Absorbs the old `rules` + `rule_epochs` two-table design into a single unified table with `start_date`/`end_date` for lifecycle management.

---

## 2. Package Structure

Each veloci service is its own Go module. There is no `internal/` directory — module boundaries already prevent cross-service imports.

### veloci-auth (domain-based)

Auth is a focused service with four cohesive domains; one package per domain is natural.

```text
services/auth/
  credentials/      ← credential CRUD handlers + store interface
  sessions/         ← token mint/refresh/revoke handlers + JWT
  invites/          ← invite create/consume handlers
  store/            ← all veloci_auth DB queries; *store.DB satisfies domain interfaces
  admin/            ← server-admin seeding utility (CLI only)
  cmd/specgen/      ← generates api/openapi.json from registered routes
  main.go
```

### veloci-api (layer-based)

The API surface is wide (14 route groups). A domain-per-package approach would produce 14+ packages with thin files. The layer-based layout keeps related concerns together and scales without proliferating packages.

```text
services/api/
  handler/          ← all HTTP route handlers; one file per domain group
  store/            ← all veloci_app DB queries; one file per domain group
  middleware/       ← HTTP middleware (auth token extraction + claim injection)
  authclient/       ← ogen-generated client for veloci-auth (never edit by hand)
  queue/            ← RabbitMQ publisher
  response/         ← Envelope[T] shared response type
  main.go
```

Handler files mirror the route groups in Section 7:

```text
handler/
  auth.go             ← login, logout, refresh, invite/accept
  users.go
  institutions.go
  accounts.go
  entries.go          ← CRUD for entries
  classifications.go  ← CRUD for classifications
  labels.go
  imports.go
  transactions.go
  review.go
  snapshots.go
  projections.go
  jobs.go             ← includes SSE stream handler
  admin.go
  health.go
```

Store files mirror the handler files and are the only code that touches `pgxpool.Pool` directly. Handlers receive a `*store.Store` (or a narrow interface for testability) and call store methods — no SQL outside `store/`.

---

## 3. Request Lifecycle

### Auth-facing routes (no JWT required)

These routes are exposed by veloci-api. The UI calls these — never veloci-auth directly.

```text
POST  /auth/login               validate credentials → mint token pair → return access token
POST  /auth/logout              revoke token by JTI
POST  /auth/refresh             exchange refresh token for new token pair
POST  /auth/invite/accept       consume invite token + create credential + create user record
```

`POST /auth/invite/accept` orchestrates three calls to veloci-auth:

1. `ValidateToken(invite_token)` — extract claims (email, entity_id, entity_role)
2. `CreateCredential(email, password)` — create the credential
3. `ConsumeInvite(token)` — mark invite as consumed (one-time use enforcement)

Then creates `users` + `entity_users` rows in veloci_app and returns an access token.

### Protected routes — middleware chain

```text
1. Extract Bearer token from Authorization header → 401 if missing
2. POST veloci-auth /tokens/validate → claims attached to request context → 401 if invalid
3. Load permission set for claims.entity_role from startup cache
4. Check required permission for route → 403 if missing
5. Check claims.system_role for /admin/* routes → 403 if not server_admin
6. Handler executes — all queries scoped by claims.entity_id
```

Permissions are seeded by `veloci-api migrate` and cached at startup. No per-request DB lookup.

### Cobra subcommands

```bash
veloci-api serve     — start HTTP server, load permission cache
veloci-api migrate   — run migrations + seed roles, permissions, role_permissions
veloci-api seed      — create server admin from config (idempotent; safe to re-run)
```

`veloci-api seed` reads `admin.email` and `admin.password` from `veloci.toml` and calls `veloci-auth /credentials/create`. It is idempotent — re-running against an existing credential is a no-op.

### Viper config (veloci.toml)

```toml
[api]
port = 8080

[api.auth]
host = "veloci-auth"
port = 8081

[database]
host     = "postgres"
port     = 5432

[database.app]
name     = "veloci_app"
user     = "veloci_app_user"
password = "..."

[rabbitmq]
host     = "rabbitmq"
port     = 5672
user     = "veloci"
password = "..."

[admin]
email    = "admin@example.com"
password = "changeme"
```

---

## 3. Response Shape

### Envelope

All non-empty responses use a consistent envelope implemented as a Go generic:

```go
// internal/response/envelope.go
type Envelope[T any] struct {
    Data T    `json:"data"`
    Meta Meta `json:"meta"`
}

type Meta struct {
    NextCursor *string `json:"next_cursor,omitempty"`
    Limit      *int    `json:"limit,omitempty"`
    HasMore    *bool   `json:"has_more,omitempty"`
}
```

Wire format — single resource:

```json
{ "data": { ... }, "meta": {} }
```

Wire format — paginated list:

```json
{
  "data": [ ... ],
  "meta": {
    "next_cursor": "base64encodedvalue",
    "limit": 50,
    "has_more": true
  }
}
```

204 No Content responses (DELETE, logout, etc.) carry no body and no envelope.

Errors use Huma's native RFC 7807 `application/problem+json` — no envelope wrapper.

### Pagination

Cursor pagination everywhere — `?limit=50&after=<cursor>`. The cursor is a base64-encoded `{id, created_at}` pair. Default limit: 50. Maximum limit: 200.

Helpers:

```go
response.Single(data)                           // non-paginated
response.Page(data, nextCursor, limit, hasMore) // paginated list
```

---

## 4. Data Model

### entities

```sql
CREATE TABLE entities (
  id         UUID PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### users

```sql
CREATE TABLE users (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  auth_credential_id UUID        NOT NULL UNIQUE,
  email              TEXT        NOT NULL UNIQUE,
  name               TEXT        NOT NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Linked to `auth_credentials` via `auth_credential_id`. Created by veloci-api during invite acceptance; the server admin user is seeded by `veloci-api seed`.

### entity_users

```sql
CREATE TABLE entity_users (
  user_id     UUID NOT NULL REFERENCES users(id),
  entity_id   UUID NOT NULL REFERENCES entities(id),
  entity_role TEXT NOT NULL CHECK (entity_role IN ('entity_admin', 'entity_user')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (user_id, entity_id)
);
```

### RBAC tables

Seeded by `veloci-api migrate`.

```sql
CREATE TABLE roles (
  id   UUID PRIMARY KEY,
  name TEXT NOT NULL UNIQUE   -- 'entity_admin', 'entity_user'
);

CREATE TABLE permissions (
  id   UUID PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE role_permissions (
  role_id       UUID NOT NULL REFERENCES roles(id),
  permission_id UUID NOT NULL REFERENCES permissions(id),
  PRIMARY KEY (role_id, permission_id)
);
```

### institution_mappings

```sql
CREATE TABLE institution_mappings (
  id                     UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID         NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  institution_name       TEXT         NOT NULL,
  source_type            TEXT         NOT NULL DEFAULT 'csv'
                         CHECK (source_type IN ('csv', 'integration')),
  settlement_window_days INTEGER      NOT NULL DEFAULT 14,
  dedup_window_days      INTEGER      NOT NULL DEFAULT 3,
  amount_tolerance_pct   FLOAT8       NOT NULL DEFAULT 0.005,
  date_col               TEXT         NOT NULL,
  amount_col             TEXT         NOT NULL,
  merchant_col           TEXT         NOT NULL,
  imported_id_col        TEXT,
  balance_col            TEXT,
  debit_credit_col       TEXT,
  amount_sign_convention TEXT         NOT NULL
    CHECK (amount_sign_convention IN ('positive_is_credit', 'positive_is_debit')),
  UNIQUE (entity_id, institution_name)
);
```

### accounts

```sql
CREATE TABLE accounts (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id          UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  institution_id     UUID        REFERENCES institution_mappings(id),
  name               TEXT        NOT NULL,
  account_type       TEXT        NOT NULL
    CHECK (account_type IN ('checking','savings','credit','loan','mortgage','investment')),
  status             TEXT        NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','passive')),
  interest_rate      NUMERIC(8,4),
  balance_cents      BIGINT,
  credit_limit_cents BIGINT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);
```

### processing_jobs

```sql
CREATE TABLE processing_jobs (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id    UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  job_type     TEXT        NOT NULL
               CHECK (job_type IN ('import.process', 'entries.reprocess', 'account.analyze', 'balance.project')),
  triggered_by UUID        NOT NULL REFERENCES users(id),
  status       TEXT        NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued', 'processing', 'complete', 'failed')),
  queued_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at   TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  error        TEXT,
  metadata     JSONB
);

-- at most one active job per (entity, type)
CREATE UNIQUE INDEX processing_jobs_one_active
  ON processing_jobs (entity_id, job_type)
  WHERE status IN ('queued', 'processing');
```

### pending_imports

```sql
CREATE TABLE pending_imports (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id        UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id       UUID        NOT NULL REFERENCES accounts(id),
  institution_id   UUID        REFERENCES institution_mappings(id),
  uploaded_by      UUID        NOT NULL REFERENCES users(id),
  uploaded_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  csv_bytes        BYTEA       NOT NULL,
  date_range_start DATE        NOT NULL,
  date_range_end   DATE        NOT NULL,
  row_count        INTEGER,
  status           TEXT        NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'processing', 'complete', 'failed')),
  job_id           UUID        REFERENCES processing_jobs(id),
  error            TEXT
);
```

### import_batches

```sql
CREATE TABLE import_batches (
  id                             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  pending_import_id              UUID        NOT NULL REFERENCES pending_imports(id),
  entity_id                      UUID        NOT NULL REFERENCES entities(id),
  account_id                     UUID        NOT NULL REFERENCES accounts(id),
  processed_at                   TIMESTAMPTZ NOT NULL,
  date_range_start               DATE        NOT NULL,
  date_range_end                 DATE        NOT NULL,
  transactions_imported          INTEGER     NOT NULL DEFAULT 0,
  transactions_skipped_duplicate INTEGER     NOT NULL DEFAULT 0,
  transactions_superseded        INTEGER     NOT NULL DEFAULT 0
);
```

### labels

Global name registry — no `entity_id`. Used by entries (canonical merchant/signal name) and classifications (output label). Entity-scoping is on operational tables; labels are pure display names referenced by ID. Renaming a label requires no recalculation — only a UI refresh.

```sql
CREATE TABLE labels (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT        NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (name)
);
```

### entries

One row per continuous rate signal instance. Absorbs the old `rules` + `rule_epochs` two-table design. `start_date`/`end_date` handle lifecycle. Many entries may share one label (e.g. Netflix at $15.99 closed, Netflix at $18.99 active — both reference the same `labels.id`). `conditions` is nullable for user-created entries that skip auto-matching.

```sql
CREATE TABLE entries (
  id                     UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id              UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  label_id               UUID          REFERENCES labels(id) ON DELETE SET NULL,
  direction              TEXT          NOT NULL CHECK (direction IN ('income', 'expense')),
  entry_type             TEXT          NOT NULL
                         CHECK (entry_type IN ('standing', 'variable', 'irregular')),
  period_days            INTEGER       NOT NULL DEFAULT 30,
  variable_method        TEXT          CHECK (variable_method IN ('avg', 'max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB,
  priority               INTEGER       NOT NULL DEFAULT 100,
  status                 TEXT          NOT NULL DEFAULT 'pending_review'
                         CHECK (status IN ('pending_review', 'active', 'inactive')),
  source                 TEXT          NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'engine')),
  recurrence_anchor      TEXT,
  next_due_date          DATE,
  project_tentatively    BOOLEAN       NOT NULL DEFAULT FALSE,
  pending_amount_cents   BIGINT,
  pending_effective_date DATE,
  start_date             DATE          NOT NULL,
  end_date               DATE,
  created_at             TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
```

### classifications

Post-stage rules: apply labels to entries based on entry attributes and existing label assignments. Entirely user-defined. Do not affect rate calculations — display and grouping only. Conditions reference label UUIDs, enabling aggregate labels built from leaf labels without a separate membership table. The API enforces cycle detection at write time.

```sql
CREATE TABLE classifications (
  id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id  UUID        NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  label_id   UUID        NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  conditions JSONB       NOT NULL,
  priority   INTEGER     NOT NULL DEFAULT 100,
  status     TEXT        NOT NULL DEFAULT 'active'
             CHECK (status IN ('active', 'inactive')),
  source     TEXT        NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'engine')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### transaction_entry_assignments

```sql
CREATE TABLE transaction_entry_assignments (
  transaction_id UUID         NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
  entry_id       UUID         NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  confidence     NUMERIC(4,3) NOT NULL DEFAULT 1.0,
  PRIMARY KEY (transaction_id, entry_id)
);
```

### review_queue

Engine-detected candidate entries awaiting user approval. `alert_type` distinguishes first detection (`new`), rate drift (`drift`), and signal loss (`ended`). Three-component confidence scores are nullable on older jobs.

```sql
CREATE TABLE review_queue (
  id                        UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                 UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  entry_id                  UUID          NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  job_id                    UUID          NOT NULL REFERENCES processing_jobs(id),
  suggested_name            TEXT          NOT NULL,
  suggested_entry_type      TEXT          NOT NULL,
  suggested_conditions      JSONB         NOT NULL,
  suggested_rate_per_day    NUMERIC(12,4) NOT NULL,
  matched_transaction_count INTEGER       NOT NULL,
  alert_type                TEXT          NOT NULL DEFAULT 'new'
                            CHECK (alert_type IN ('new', 'drift', 'ended')),
  confidence                NUMERIC(4,3)  NOT NULL,
  merchant_confidence       NUMERIC(4,3),
  timing_confidence         NUMERIC(4,3),
  amount_confidence         NUMERIC(4,3),
  sample_merchants          TEXT[]        NOT NULL,
  status                    TEXT          NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'rejected', 'modified')),
  reviewed_by               UUID          REFERENCES users(id),
  reviewed_at               TIMESTAMPTZ
);
```

### snapshots

Rebuildable engine output. Safe to truncate and recompute at any time. One row per calendar day per node. The engine crawls the flux window on each import and UPSERTs all days in `[computed_as_of − settlement_window_days .. computed_as_of]`. Days outside the flux window have only settled transactions and are not recomputed.

`node_type = 'entry'` → entry-level rate signal (Stage 3 output).  
`node_type = 'classification'` → classification-level aggregate (Stage 4 output).

OHLC candlestick high/low are **not stored** — the API computes `MAX/MIN(actual_rate_per_day)` over the daily series at query time. The API joins `entries` to include `entry_start_date`/`entry_end_date` in snapshot history responses.

```sql
CREATE TABLE snapshots (
  id                         UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id                  UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  node_id                    UUID          NOT NULL,
  node_type                  TEXT          NOT NULL CHECK (node_type IN ('entry', 'classification')),
  snapshot_date              DATE          NOT NULL,
  computed_as_of             DATE          NOT NULL,
  job_id                     UUID          NOT NULL REFERENCES processing_jobs(id),
  actual_rate_per_day        NUMERIC(12,4) NOT NULL,
  projected_rate_per_day     NUMERIC(12,4) NOT NULL,
  drift_per_day              NUMERIC(12,4) NOT NULL,
  slope_per_day              NUMERIC(14,6) NOT NULL,
  r_squared                  NUMERIC(4,3)  NOT NULL,
  transaction_count          INTEGER       NOT NULL,
  window_days_used           INTEGER       NOT NULL,
  rolling_window_total_cents BIGINT        NOT NULL DEFAULT 0,
  balance_cents              BIGINT        NOT NULL DEFAULT 0,
  UNIQUE (entity_id, node_id, snapshot_date)
);
```

### projections

Forward-looking signal superposition produced by Stage 7. One row per (entity, optional account, projected date) per job run. Safe to truncate and recompute. `account_id NULL` = entity-level aggregate across all active accounts.

```sql
CREATE TABLE projections (
  id                      UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id               UUID          NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  account_id              UUID          REFERENCES accounts(id),  -- NULL = entity-level aggregate
  job_id                  UUID          NOT NULL REFERENCES processing_jobs(id),
  projected_date          DATE          NOT NULL,
  income_rate_per_day     NUMERIC(12,4) NOT NULL DEFAULT 0,
  commitment_rate_per_day NUMERIC(12,4) NOT NULL DEFAULT 0,
  margin_rate_per_day     NUMERIC(12,4) NOT NULL,
  projected_balance_cents BIGINT        NOT NULL,
  is_pinch_point          BOOLEAN       NOT NULL DEFAULT FALSE,
  UNIQUE (entity_id, account_id, job_id, projected_date)
);
```

---

## 5. RBAC Permissions

Seeded at migration time. TBD permissions are provisioned but assignment to `entity_user` is deferred.

| Permission | `entity_admin` | `entity_user` |
| --- | --- | --- |
| `accounts:read` | ✓ | ✓ |
| `accounts:write` | ✓ | — |
| `import:create` | ✓ | TBD |
| `entries:write` | ✓ | TBD |
| `classifications:write` | ✓ | TBD |
| `labels:write` | ✓ | ✓ |
| `review:write` | ✓ | TBD |
| `reports:read` | ✓ | ✓ |
| `users:manage` | ✓ | — |
| `entity:configure` | ✓ | — |

---

## 6. Endpoints

### Auth (no JWT required)

```text
POST   /auth/login              validate credentials → mint token pair → return access token + expires_at
POST   /auth/logout             revoke token by JTI (body: { jti })
POST   /auth/refresh            exchange refresh token for new pair (body: { refresh_token })
POST   /auth/invite/accept      consume invite + create credential + create user (body: { token, password })
```

### Users (protected)

```text
GET    /users/me                        accounts:read    own profile
PUT    /users/me                        accounts:read    update own name
GET    /users                           users:manage     list entity members
PUT    /users/:id/password              users:manage     admin resets password
DELETE /users/:id                       users:manage     remove user + revoke all tokens
POST   /users/invite                    users:manage     create invite link (body: { email, entity_role })
```

`POST /users/invite` calls `auth.CreateInvite` with claims `{ email, entity_id, entity_role }` and returns `{ token, expires_at }`.

`DELETE /users/:id` calls `auth.DeleteCredential` + `auth.RevokeUserTokens`, then removes veloci_app rows.

### Institutions and Accounts

```text
GET    /institutions                    accounts:read
POST   /institutions                    accounts:write
GET    /institutions/:id                accounts:read
PUT    /institutions/:id                accounts:write
DELETE /institutions/:id                accounts:write

GET    /institutions/:id/accounts       accounts:read
POST   /institutions/:id/accounts       accounts:write

GET    /accounts/:id                    accounts:read
PUT    /accounts/:id                    accounts:write
DELETE /accounts/:id                    accounts:write
```

### Entries

```text
GET    /entries                         accounts:read    all entries for entity
POST   /entries                         entries:write    create entry
GET    /entries/:id                     accounts:read
PUT    /entries/:id                     entries:write
DELETE /entries/:id                     entries:write
POST   /entries/preview                 accounts:read    match-test conditions without persisting
```

`POST /entries/preview` accepts a partial or complete entry conditions object and returns matching `transaction_id` list and count.

### Classifications

```text
GET    /classifications                 accounts:read    all classifications for entity
POST   /classifications                 classifications:write    create classification
GET    /classifications/:id             accounts:read
PUT    /classifications/:id             classifications:write
DELETE /classifications/:id             classifications:write
```

### Labels

```text
GET    /labels                          accounts:read    all labels (global)
POST   /labels                          labels:write     create label (name only)
GET    /labels/:id                      accounts:read
PUT    /labels/:id                      labels:write     rename label
DELETE /labels/:id                      labels:write

GET    /labels/:id/entries              accounts:read    entries that reference this label
```

### Imports

```text
POST   /imports                         import:create    upload CSV → pending_import + job_id
GET    /imports                         accounts:read    paginated import batch history
GET    /imports/:id                     accounts:read
```

### Transactions (read only)

```text
GET    /transactions                    accounts:read    filter: account_id, date, entry_id, unmatched
GET    /transactions/:id                accounts:read
```

### Review queue

```text
GET    /review                          accounts:read    pending review items; includes alert_type + confidence scores
PUT    /review/:id                      review:write     edit conditions/name/type/end_date before approving
POST   /review/:id/approve              review:write     activate entry → triggers account.analyze
POST   /review/:id/reject               review:write     discard proposal
```

`GET /review` response shape per item:

```json
{
  "id": "uuid",
  "entry_id": "uuid",
  "suggested_name": "Netflix",
  "suggested_entry_type": "standing",
  "suggested_conditions": { ... },
  "suggested_rate_per_day": 0.6667,
  "matched_transaction_count": 12,
  "alert_type": "new",
  "confidence": 0.91,
  "merchant_confidence": 0.95,
  "timing_confidence": 0.88,
  "amount_confidence": 0.90,
  "sample_merchants": ["NETFLIX.COM", "Netflix"],
  "status": "pending"
}
```

### Snapshots (read only)

```text
GET    /snapshots                       reports:read     current snapshot, all nodes
GET    /snapshots/summary               reports:read     entity total: income/expense/margin/drift
GET    /snapshots/:node_id/history      reports:read     historical daily series (paginated backward)
```

`GET /snapshots/:node_id/history` query parameters:

| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `before` | date | latest | Return rows with `snapshot_date < before` |
| `limit` | int | 60 | Rows per page. Max 180. |
| `granularity` | string | `day` | `day`, `week`, `month`, `year` — controls OHLC aggregation |

Response row shape (OHLC granularity):

```json
{
  "period_start": "2026-02-01",
  "period_end": "2026-02-28",
  "open": 0.6667,
  "close": 0.6667,
  "high": 0.8000,
  "low": 0.6667,
  "actual_rate_per_day": 0.6667,
  "projected_rate_per_day": 0.6667,
  "drift_per_day": 0.0000,
  "slope_per_day": 0.000001,
  "entry_start_date": "2025-01-01",
  "entry_end_date": null
}
```

`next_cursor` is the `snapshot_date` of the last row — pass as `before` on the next request.

### Projections (read only)

Stage 7 forward-looking output. Powers the Horizon forward view.

```text
GET    /projections                     reports:read     forward projection series
```

Query parameters:

| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `account_id` | uuid | — | Scope to one account; omit for entity-level aggregate |
| `from` | date | today | Start of projection window |
| `to` | date | today + 90d | End of projection window |

Response row shape:

```json
{
  "projected_date": "2026-08-01",
  "income_rate_per_day": 136.89,
  "commitment_rate_per_day": 98.23,
  "margin_rate_per_day": 38.66,
  "projected_balance_cents": 118240,
  "is_pinch_point": false
}
```

### Jobs

```text
GET    /jobs                            accounts:read    paginated history
GET    /jobs/stream                     accounts:read    SSE — all entity jobs, entity-scoped
POST   /jobs/reprocess                  entries:write    trigger entries.reprocess
POST   /jobs/analyze                    entries:write    trigger account.analyze
POST   /jobs/project                    reports:read     trigger balance.project
```

### Server admin

`system_role: server_admin` required; entity_role ignored.

```text
GET    /admin/status                    server version, uptime, Postgres + RabbitMQ health
GET    /admin/entities                  list all entities
```

---

## 7. SSE Job Stream

`GET /jobs/stream` opens a persistent Server-Sent Events connection scoped to the authenticated entity.

### Server-side ordering

```text
1. Register LISTEN on Postgres NOTIFY channel for entity_id  ← before any query
2. Query current state of all active jobs (queued + processing) for entity
3. Send snapshot events to client
4. Forward buffered NOTIFY events received during steps 2–3
5. Forward all subsequent NOTIFY events
```

### Event shape

```json
{
  "job_id": "uuid",
  "job_type": "import.process | entries.reprocess | account.analyze | balance.project",
  "status": "queued | processing | complete | failed",
  "error": null,
  "queued_at": "2026-06-30T12:00:00Z",
  "completed_at": "2026-06-30T12:01:23Z"
}
```

### Client pattern

```text
1. GET /jobs          → populate full jobs table (REST)
2. GET /jobs/stream   → server sends current active job states, then streams deltas
3. On each event      → update row by job_id; last-write-wins handles REST/SSE overlap
4. Component unmounts → close SSE connection
```

---

## 8. Job Publishing

veloci-api publishes to RabbitMQ after any operation that requires engine processing. Before publishing, it checks `processing_jobs` for an existing `queued` or `processing` job of the same type for the same entity — the partial unique index makes this a conflict rather than a race.

### import.process

Published after `POST /imports` stores the `pending_import` record.

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "import.process",
  "metadata": { "pending_import_id": "uuid" }
}
```

### entries.reprocess

Published after `POST /jobs/reprocess`, or after an entry is created, updated, or deleted.

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "entries.reprocess",
  "metadata": {}
}
```

### account.analyze

Published after `POST /review/:id/approve` or `POST /jobs/analyze`.

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "account.analyze",
  "metadata": {}
}
```

### balance.project

Published after `POST /jobs/project`. Also triggered automatically after any `account.analyze` job completes.

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "balance.project",
  "metadata": {}
}
```

---

## 9. Out of Scope for This Spec

- veloci-engine pipeline changes (covered in engine spec)
- UI component design (covered in UI specs)
- Server admin write endpoints (v2 — entity management, DNS, version control)
- Invite email delivery (v2)
- Custom role creation and per-user permission overrides (v2)
- Postgres RLS enforcement (v2)
- Rate limiting and brute-force protection (v2)
- File size limits and CSV validation error shapes (covered in import pipeline spec)
