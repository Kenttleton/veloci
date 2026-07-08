# Veloci — API Service Design

**Date:** 2026-06-30
**Status:** Approved
**Scope:** veloci-api service — REST endpoints, RBAC enforcement, job orchestration, SSE job streaming, data model

---

## 1. Overview

`veloci-api` is the single entry point for the frontend. It has four responsibilities:

1. **Auth proxy** — transparent passthrough to veloci-auth for credential and token operations
2. **Financial data CRUD** — all permanent configuration and transient read access
3. **Job orchestration** — publish jobs to RabbitMQ, stream status via SSE
4. **RBAC** — own and enforce the permissions model; seed roles and permissions at migration

### Data model: permanent vs transient

Two categories of data live in `veloci_app`:

| Category | Tables | Characteristics |
| --- | --- | --- |
| Permanent | `rules`, `labels`, `institution_mappings`, `accounts`, `users`, `entities`, `entity_users` | User-owned configuration. Full CRUD. |
| Transient | `raw_transactions`, `computed_snapshots`, `processing_jobs`, `review_queue` | Engine output. Read-only via API. Rebuilt when rules change. |

**`raw_transactions` is append-only** — never modified after import.

**Entries are not a stored table** — an "entry" in the UI is the join of a `rule` (permanent config) with its most recent `computed_snapshot`. `GET /entries` is a read-only computed view, not a writable resource.

> **Note:** This spec supersedes the data model in the engine spec (`2026-06-29-processing-engine-design.md`) for the following tables: `entries` and `entry_rules` are replaced by `rules`; `groups` and `group_members` are replaced by `labels`; `label_members` and `label_rules` are removed — the label hierarchy is expressed via `rules.label_id` and post-stage JSONB conditions; `transaction_entry_assignments` is renamed `transaction_rule_assignments`; `computed_snapshots.node_type` values change from `entry|group` to `rule|label`.

---

## 2. Request Lifecycle

### Auth proxy routes

Forwarded directly to veloci-auth. No JWT validation, no RBAC check:

```text
POST  /auth/register
POST  /auth/login
POST  /auth/refresh
POST  /auth/logout
POST  /auth/users/invite/:token/accept
```

### Protected routes — middleware chain

```text
1. Extract Bearer token from Authorization header → 401 if missing
2. POST veloci-auth /tokens/validate → claims attached to request context → 401 if invalid
3. Load permission set for claims.entity_role from startup cache
4. Check required permission for route → 403 if missing
5. Check claims.system_role for /admin/* routes → 403 if not server_admin
6. Handler executes — all queries scoped by claims.entity_id
```

Permissions are seeded by `veloci-api migrate` and cached at startup. No per-request DB lookup for permission definitions; the role→permission mapping is a startup-time read.

### Cobra subcommands

```bash
veloci-api serve     — start HTTP server, load permission cache
veloci-api migrate   — run migrations + seed roles, permissions, role_permissions
```

### Viper config

```yaml
port: 8080
auth_service_url: http://veloci-auth:8081
database_url: postgres://veloci_app_user:...@postgres/veloci_app
rabbitmq_url: amqp://...
```

---

## 3. Response Shape

### Envelope

All responses use a consistent envelope:

```json
{
  "data": { ... },
  "meta": {}
}
```

For paginated lists, `meta` carries cursor fields:

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

`meta` is an empty object on non-paginated responses. It is never omitted.

### Pagination

Cursor pagination everywhere — `?limit=50&after=<cursor>`. The cursor is a base64-encoded `{id, created_at}` pair. Consistent across all list endpoints regardless of resource type. Default limit: 50. Maximum limit: 200.

### Errors

```json
{
  "code": "RULE_NOT_FOUND",
  "message": "rule not found",
  "details": [
    { "field": "conditions.value", "issue": "must be a non-empty string" }
  ]
}
```

`details` is omitted when empty. `code` is a machine-readable screaming snake case string. Standard HTTP status codes apply.

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
  id         UUID PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Matched to `auth_credentials` by email. Created by veloci-api on first login and invite acceptance.

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

Seeded by `veloci-api migrate`. Not modified at runtime in v1.

```sql
CREATE TABLE roles (
  id   UUID PRIMARY KEY,
  name TEXT NOT NULL UNIQUE   -- 'entity_admin', 'entity_user'
);

CREATE TABLE permissions (
  id   UUID PRIMARY KEY,
  name TEXT NOT NULL UNIQUE   -- 'accounts:read', 'rules:write', etc.
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
  id                     UUID PRIMARY KEY,
  entity_id              UUID NOT NULL,
  institution_name       TEXT NOT NULL,
  date_col               TEXT NOT NULL,
  amount_col             TEXT NOT NULL,
  merchant_col           TEXT NOT NULL,
  imported_id_col        TEXT,
  balance_col            TEXT,
  debit_credit_col       TEXT,
  amount_sign_convention TEXT NOT NULL
    CHECK (amount_sign_convention IN ('positive_is_credit', 'positive_is_debit'))
);
```

### accounts

```sql
CREATE TABLE accounts (
  id                 UUID PRIMARY KEY,
  entity_id          UUID NOT NULL,
  institution_id     UUID REFERENCES institution_mappings(id),
  name               TEXT NOT NULL,
  account_type       TEXT NOT NULL
    CHECK (account_type IN ('checking','savings','credit','loan','mortgage','investment')),
  status             TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','passive')),
  interest_rate      NUMERIC(8,4),
  balance_cents      BIGINT,
  credit_limit_cents BIGINT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### rules

Replaces `entries` + `entry_rules` from the engine spec. Permanent configuration for how transactions are identified and converted to a $/day rate. Each rule outputs exactly one label (`label_id`). Rules are composable: post-stage rule conditions may reference other label UUIDs as inputs, building aggregate labels without a separate membership table.

```sql
CREATE TABLE rules (
  id                     UUID PRIMARY KEY,
  entity_id              UUID NOT NULL,
  name                   TEXT NOT NULL,
  direction              TEXT NOT NULL CHECK (direction IN ('income','expense')),
  entry_type             TEXT NOT NULL
    CHECK (entry_type IN ('standing','hit','boost','variable')),
  period_days            INTEGER NOT NULL DEFAULT 30,
  variable_method        TEXT CHECK (variable_method IN ('avg','max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB NOT NULL,   -- boolean expression tree; leaf nodes may reference label UUIDs
  label_id               UUID REFERENCES labels(id) ON DELETE SET NULL,  -- one output label per rule
  stage                  TEXT NOT NULL DEFAULT 'pre' CHECK (stage IN ('pre','post')),
  priority               INTEGER NOT NULL DEFAULT 100,
  status                 TEXT NOT NULL DEFAULT 'pending_review'
    CHECK (status IN ('pending_review','active','inactive')),
  source                 TEXT NOT NULL DEFAULT 'user'
    CHECK (source IN ('user','engine')),
  created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON rules (entity_id, status);
CREATE INDEX ON rules (entity_id, priority);
```

### transaction_rule_assignments

```sql
CREATE TABLE transaction_rule_assignments (
  transaction_id UUID NOT NULL REFERENCES raw_transactions(id),
  rule_id        UUID NOT NULL REFERENCES rules(id),
  confidence     NUMERIC(4,3) NOT NULL DEFAULT 1.0,
  assigned_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (transaction_id, rule_id)
);
```

### labels

User-facing named groupings. Each label is the output of exactly one rule (`rules.label_id`). The label hierarchy (leaf → aggregate) is implicit in post-stage rule conditions that reference other label UUIDs — no separate membership or automation table.

```sql
CREATE TABLE labels (
  id         UUID PRIMARY KEY,
  entity_id  UUID NOT NULL,
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);
```

Name is user-visible and freely mutable (rename cascades in the UI automatically because all references use `id`). Calculations always reference `id`, never `name`.

To find all rules that feed into a label: `SELECT * FROM rules WHERE label_id = $label_id`.
To find the aggregate labels a label participates in: query the JSONB conditions of post-stage rules for references to this `label_id`.

### computed_snapshots

Updated `node_type` values from engine spec (`entry|group` → `rule|label`). One row per calendar day per node — not one per import. The engine crawls the flux window on each import and UPSERTs affected days.

`snapshot_date` is the calendar day identity key. `computed_as_of` is the import's data horizon (MAX transaction date), denormalized here so Stage 5 regression can read the settlement boundary without joining to import_batches.

`epoch_id` is internal — not returned in API responses. Snapshot responses join `rule_epochs` and return `epoch_start`/`epoch_end` directly. OHLC candlestick high/low are **not stored** — the API computes them from the daily `actual_rate_per_day` series at query time (see Snapshots endpoints).

```sql
CREATE TABLE computed_snapshots (
  id                     UUID          PRIMARY KEY,
  entity_id              UUID          NOT NULL,
  node_id                UUID          NOT NULL,   -- rule_id or label_id
  node_type              TEXT          NOT NULL CHECK (node_type IN ('rule','label')),
  snapshot_date          DATE          NOT NULL,   -- calendar day this row represents
  computed_as_of         DATE          NOT NULL,   -- MAX(raw_transactions.date) from the import run
  job_id                 UUID          NOT NULL,
  actual_rate_per_day    NUMERIC(12,4) NOT NULL,
  projected_rate_per_day NUMERIC(12,4) NOT NULL,
  drift_per_day          NUMERIC(12,4) NOT NULL,
  slope_per_day          NUMERIC(14,6) NOT NULL,
  r_squared              NUMERIC(4,3)  NOT NULL,
  transaction_count      INTEGER       NOT NULL,
  window_days_used       INTEGER       NOT NULL,
  balance_cents          BIGINT        NOT NULL DEFAULT 0,
  epoch_id               UUID          REFERENCES rule_epochs(id),   -- internal; not returned by API
  UNIQUE (entity_id, node_id, snapshot_date)
);

CREATE INDEX ON computed_snapshots (entity_id, node_id, snapshot_date DESC);
```

---

## 5. RBAC Permissions

Seeded at migration time. TBD permissions are provisioned but assignment to `entity_user` is deferred.

| Permission | `entity_admin` | `entity_user` |
| --- | --- | --- |
| `accounts:read` | ✓ | ✓ |
| `accounts:write` | ✓ | — |
| `import:create` | ✓ | TBD |
| `rules:write` | ✓ | TBD |
| `labels:write` | ✓ | ✓ |
| `entries:write` | ✓ | TBD |
| `reports:read` | ✓ | ✓ |
| `users:manage` | ✓ | — |
| `entity:configure` | ✓ | — |

---

## 6. Endpoints

### Auth proxy

No JWT validation. Forwarded directly to veloci-auth.

```text
POST   /auth/register
POST   /auth/login
POST   /auth/refresh
POST   /auth/logout
POST   /auth/users/invite/:token/accept
```

### Users

Proxied writes to veloci-auth; app-side reads from `veloci_app`.

```text
GET    /users/me                        accounts:read    own profile
PUT    /users/me                        accounts:read    update own name
GET    /users                           users:manage     list entity members
POST   /users                           users:manage     admin creates user
PUT    /users/:id/password              users:manage     admin resets password
DELETE /users/:id                       users:manage     remove user + revoke tokens
POST   /users/invite                    users:manage     generate invite link
```

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

### Rules (permanent — match configuration)

```text
GET    /rules                           accounts:read    all rules for entity
POST   /rules                           rules:write      create rule
GET    /rules/:id                       accounts:read
PUT    /rules/:id                       rules:write
DELETE /rules/:id                       rules:write
POST   /rules/preview                   accounts:read    which transactions match?
```

`POST /rules/preview` accepts a partial or complete rule conditions object and returns matching `transaction_id` list and count without persisting anything.

### Labels (permanent)

Labels are named groupings created independently and then referenced by `rules.label_id`. Label membership (which rules output this label) is managed through the rule, not the label.

```text
GET    /labels                          accounts:read    all labels for entity
POST   /labels                          labels:write     create label (name only)
GET    /labels/:id                      accounts:read
PUT    /labels/:id                      labels:write     rename label
DELETE /labels/:id                      labels:write

GET    /labels/:id/rules                accounts:read    rules that output this label (reads rules.label_id)
```

### Imports

```text
POST   /imports                         import:create    upload CSV → pending_import + job_id
GET    /imports                         accounts:read    paginated import batch history
GET    /imports/:id                     accounts:read
```

### Transactions (transient — read only)

```text
GET    /transactions                    accounts:read    filter: account_id, date, rule_id, unmatched
GET    /transactions/:id                accounts:read
```

### Entries (transient — computed view of rules + snapshots)

```text
GET    /entries                         accounts:read    rules joined with latest computed_snapshot
GET    /entries/:id                     accounts:read    rule_id used as entry id
```

### Review queue

Engine-generated rule proposals. User edits the match conditions before approving.

```text
GET    /review                          accounts:read    pending rule proposals
PUT    /review/:id                      entries:write    edit rule conditions, name, type before approving
POST   /review/:id/approve              entries:write    activate rule → triggers account.analyze
POST   /review/:id/reject               entries:write    discard proposal
```

### Snapshots (transient — read only)

```text
GET    /snapshots                       reports:read     current snapshot, all nodes
GET    /snapshots/summary               reports:read     entity total: income/expense/margin/drift
GET    /snapshots/:node_id/history      reports:read     historical daily series for one node (paginated)
```

#### Snapshot history pagination

`GET /snapshots/:node_id/history` supports cursor-based lazy loading for the Horizon graph. The UI fetches the most recent chunk on load and pages backward as the user scrolls.

Query parameters:

| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `before` | date | latest | Return rows with `snapshot_date < before` (cursor) |
| `limit` | int | 60 | Rows per page. Max 180. |
| `granularity` | string | `day` | `day`, `week`, `month`, `year` — controls OHLC aggregation |

When `granularity` is `day`, the response returns raw daily snapshot rows. For `week`, `month`, or `year`, the API aggregates the daily series into OHLC candles:

```json
{
  "data": [
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
      "epoch_start": "2025-01-01",
      "epoch_end": null
    }
  ],
  "next_cursor": "2026-01-31",
  "has_more": true
}
```

`epoch_start`/`epoch_end` are joined from `rule_epochs` on `epoch_id`. `epoch_id` is not returned. `next_cursor` is the `snapshot_date` of the last row returned — pass it as `before` on the next request. `has_more: false` means the full history has been loaded.

### Jobs

```text
GET    /jobs                            accounts:read    paginated history
GET    /jobs/stream                     accounts:read    SSE — all entity jobs, entity-scoped
POST   /jobs/reprocess                  rules:write      trigger rules.reprocess
POST   /jobs/analyze                    entries:write    trigger account.analyze
```

### Server admin

Read-only in v1. `system_role: server_admin` required; entity_role ignored.

```text
GET    /admin/status                    server version, uptime, Postgres + RabbitMQ health
GET    /admin/entities                  list all entities (v2: create, configure, DNS)
```

---

## 7. SSE Job Stream

`GET /jobs/stream` opens a persistent Server-Sent Events connection scoped to the authenticated entity.

### Server-side ordering (race condition prevention)

```text
1. Register LISTEN on Postgres NOTIFY channel for entity_id  ← before any query
2. Query current state of all active jobs (queued + processing) for entity
3. Send snapshot events to client
4. Forward buffered NOTIFY events received during steps 2–3
5. Forward all subsequent NOTIFY events
```

The LISTEN is registered before the snapshot query. Notifications fired during the query are buffered by Postgres and replayed after the snapshot — nothing falls through the gap.

### Event shape

```json
{
  "job_id": "uuid",
  "job_type": "import.process | rules.reprocess | account.analyze",
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

veloci-api publishes to RabbitMQ after any operation that requires engine processing.

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

### rules.reprocess

Published after `POST /jobs/reprocess`, or after a rule is created, updated, or deleted (status changed to `active`).

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "rules.reprocess",
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

Before publishing any job, veloci-api checks `processing_jobs` for an existing `queued` or `processing` job of the same type for the same entity. If one exists, the new job is deferred — no duplicate concurrent jobs per entity.

---

## 9. Out of Scope for This Spec

- veloci-engine pipeline changes required by this data model (covered in engine spec update)
- UI component design (covered in UI spec)
- Server admin write endpoints (v2 — entity management, DNS, version control)
- Invite email delivery (v2)
- Custom role creation and per-user permission overrides (v2)
- Postgres RLS enforcement (v2)
- Rate limiting and brute-force protection (v2)
- File size limits and CSV validation error shapes (covered in import pipeline spec)
