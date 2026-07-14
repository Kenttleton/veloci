# Veloci — System Architecture Design

**Date:** 2026-06-26
**Status:** Approved
**Scope:** Overall system architecture, service decomposition, auth model, deployment

---

## 1. Overview

Veloci is a local-first personal finance application targeting the self-hosting community. The architecture is microservice-oriented from day one so that the path from single-tenant self-hosted (v1) to multi-tenant SaaS (v2) requires evolving the auth layer only — the financial data services are unchanged.

---

## 2. Services

| Service | Language | Responsibility |
| --- | --- | --- |
| `veloci-web` | TypeScript / React | Static SPA — UI only, no server-side logic |
| `veloci-api` | Go + Cobra + Viper | REST API, auth proxy, RBAC enforcement, all CRUD, job publishing. Cobra subcommands: `serve`, `migrate`, `seed` |
| `veloci-engine` | Rust | Pattern clustering, rule evaluation, rate/slope calculations. Health via CLI subcommand |
| `veloci-auth` | Go + Cobra + Viper | Credential validation, token minting and validation, invite lifecycle. Cobra subcommands: `serve` |
| `postgres` | — | All persistent data |
| `rabbitmq` | — | Durable job queue between api and engine |

---

## 3. Communication

### API ↔ Frontend

REST over HTTP. The React SPA communicates exclusively with `veloci-api`. No service is exposed directly to the frontend except the API.

### API ↔ Engine

Asynchronous via RabbitMQ. `veloci-api` publishes jobs to a queue. `veloci-engine` consumes jobs, reads and writes Postgres directly for analysis workloads, and acknowledges completion. The API never calls the engine synchronously.

**v1 job types:**

- `import.process` — new transactions imported, run clustering and matching
- `rules.reprocess` — rules changed, reprocess affected transactions for an entity
- `account.analyze` — recompute rates and slopes for an account
- `balance.project` — account balance updated manually, rerun Stage 7 projection only

**Engine reads Postgres directly** for large dataset analysis (rate/slope calculations over full transaction history). Writing results back to Postgres directly avoids routing large payloads through the API.

**Engine health check:** The engine binary supports a `health` subcommand — `veloci-engine health` — that independently connects to Postgres and RabbitMQ and exits `0` or `1`. Docker runs this as a separate short-lived process for its healthcheck. No HTTP server is added to the engine; it remains a pure queue consumer.

### API ↔ Auth

`veloci-auth` is an internal service — not exposed outside the Docker network. `veloci-api` is its only caller.

At login, `veloci-api` validates credentials via `veloci-auth`, then mints a token with full claims (entity_id, roles). On every subsequent protected request, `veloci-api` calls `veloci-auth POST /tokens/validate` to verify the token is signed, unexpired, and not revoked. Claims are returned verbatim from the auth DB.

**Two Postgres databases:**

- `veloci_auth` — `auth_credentials`, `tokens`, `invite_tokens`. Owned exclusively by `veloci-auth`.
- `veloci_app` — all financial data, RBAC, entity and user management. Shared by `veloci-api` and `veloci-engine` under a single DB user (`veloci_app_user`). No cross-database queries between the two.

**Intentional shared DB user:** `veloci-engine` is a performance-split extension of `veloci-api`, not an independent service. The Rust engine exists solely because Go is a poor fit for the CPU-bound pipeline stages; the application is logically one unit with shared state. A single `veloci_app_user` correctly expresses this. **v2 note:** when Veloci moves to SaaS, split DB users (`veloci_api_user` / `veloci_engine_user`) become worthwhile for audit logging and distributed tracing — distinguishing at the Postgres level which process wrote a given row is valuable in a paid, multi-tenant product. That change is additive (a new role + GRANT migration) and does not require schema changes.

---

## 4. Financial Entity Model

A **financial entity** is the unit of financial ownership. A family, household, or individual is one entity. All financial data in `veloci_app` is keyed by `entity_id`.

```text
entity
  └── users (via entity_users)
        └── entity_role → permissions
  └── accounts         (active + passive — see below)
  └── raw_transactions
  └── rules (each rule outputs one label via rules.label_id)
  └── labels
  └── computed_snapshots  (transient — rebuilt by engine)
```

**v1:** One entity per self-hosted deployment. Multiple users (family members) per entity.
**v2:** Multiple entities per deployment. API gains multi-entity admin routes; auth service is unchanged.

### Account Status: Active vs Passive

`accounts.status` determines whether an account contributes to the **main budget** or functions as a **standalone account budget**.

**Active accounts** (`status = 'active'`) are cash-flow accounts — checking and savings. These form the main budget. The main budget is seeded with three system labels at entity setup: **Income**, **Commitments**, and **Margin**. Rules on active accounts auto-join Income or Commitments based on their `direction` field. Margin is the parent of both.

**Passive accounts** (`status = 'passive'`) are all other account types: credit cards, investment accounts, loans, mortgages. Each passive account operates as its own independent mini-budget. Rules and labels for passive accounts are user-managed. The engine runs the full pipeline against them identically — same stages, same rule/label model, same $/day output. No special-casing per account type.

This means every account in Veloci — regardless of type — produces comparable $/day rates. A credit card's Netflix spending rate, an investment account's contribution rate, and a mortgage's payment rate are all in the same unit and directly comparable in the UI.

**Why passive accounts are not in the main budget:** Transactions on a credit card are purchases the user made using credit — the cash-flow event that matters to the main budget is the payment from checking, not the individual charges. Similarly, an investment contribution is tracked as a checking outflow; the investment account tracks what that money is doing once it arrives. This prevents double-counting at the entity level without requiring transfer detection logic in the engine.

**Interest** (HYSA yield, CC interest charges, investment returns) is a known gap for v1. Interest income/expense will be modeled as rules in a future spec.

### Cross-Account Rate Comparison

Because all accounts produce $/day rates through the same engine, the UI can surface comparisons across budget contexts without any special API aggregation:

- "Netflix ($0.50/day) consumes 7.5% of my CC payment rate ($6.67/day from checking)"
- "My Vanguard contribution cluster is growing at +$1.20/day faster than my individual stock cluster"

The engine produces the numbers; labels define the groupings; the UI handles the comparison.

---

## 5. Auth and RBAC

### JWT Payload

```json
{
  "sub": "user_id",
  "email": "user@example.com",
  "system_role": "user",
  "entity_id": "entity_uuid",
  "entity_role": "entity_admin"
}
```

`veloci-api` receives claims back from `veloci-auth /tokens/validate` on every request. It uses `entity_id` to scope all queries and `entity_role` to enforce permissions. `system_role` gates `/admin/*` routes — only `server_admin` may access them.

`system_role` is a credential-level concern owned by `veloci-auth`. `entity_role` and all RBAC is owned by `veloci-api`.

### RBAC Schema

```text
roles             — named roles (entity_admin, entity_user in v1; custom in v2)
permissions       — named permission strings (accounts:write, import:create, ...)
role_permissions  — join: role → [permissions]
entity_users      — join: user → entity → entity_role
```

Seeded by `veloci-api migrate`. The role→permission mapping is cached at startup — no per-request DB lookup.

### v1 Seeded Roles

| Permission | `entity_admin` | `entity_user` |
| --- | --- | --- |
| `accounts:read` | ✓ | ✓ |
| `accounts:write` | ✓ | — |
| `import:create` | ✓ | TBD |
| `rules:write` | ✓ | TBD |
| `labels:write` | ✓ | ✓ |
| `entries:write` | ✓ | TBD |
| `review:write` | ✓ | TBD |
| `reports:read` | ✓ | ✓ |
| `users:manage` | ✓ | — |
| `entity:configure` | ✓ | — |

### v2 Expansion

Custom role creation, additional permissions, per-user permission overrides. No schema changes required — the tables already support it.

---

## 6. Auth Flow

### Login

1. Client POSTs credentials to `veloci-api POST /auth/login`
2. `veloci-api` calls `veloci-auth POST /credentials/validate` — returns `credential_id` + `system_role`
3. `veloci-api` looks up `entity_id` + `entity_role` for that user in `veloci_app`
4. `veloci-api` calls `veloci-auth POST /tokens/mint` with the full claims object
5. `veloci-auth` signs the JWT, stores it, returns the token to `veloci-api`
6. `veloci-api` returns the token to the client

### Per-Request Validation

1. Client sends `Authorization: Bearer <token>` on every request
2. `veloci-api` calls `veloci-auth POST /tokens/validate` — verifies signature, DB presence, and expiry; returns claims
3. `veloci-api` extracts `entity_id` to scope all queries and `entity_role` to enforce permissions

### Token Refresh

Veloci uses a standard OAuth2 access + refresh token flow. Access tokens are short-lived (60 min). Refresh tokens are long-lived (30 days) and stored in the `tokens` table with `token_type = 'refresh'`.

The frontend detects when ~15 minutes remain on the access token and calls `POST /auth/refresh` with the current access token. `veloci-auth` validates the token, mints a new access token, soft-deletes the old refresh token (sets `rotated_at`), and issues a new refresh token. A 60-second grace window on `rotated_at` prevents two-tab concurrent rotation requests from forcing a re-login.

Revoking a refresh token cascades via `parent_id` FK to all access tokens it issued. Expired or missing tokens force re-login. This flow is designed to be swappable — `veloci-auth` can be replaced with an external OAuth2 provider (Keycloak, Auth0) in v2 without changes to `veloci-api`'s auth middleware.

---

## 7. Deployment

Single `docker-compose.yml` for self-hosted. Six containers sharing an internal Docker network. External ports exposed: API (REST), web (static files). Postgres and RabbitMQ are internal only.

```yaml
services:
  veloci-web       # nginx serving React SPA
  veloci-api       # Go, exposes :8080
  veloci-engine    # Rust, internal only (consumes queue)
  veloci-auth      # Go, exposes :8081
  postgres         # internal only
  rabbitmq         # internal only (management UI optional)
```

Persistent volumes: one for Postgres data, one for RabbitMQ data.

---

## 8. v1 → v2 Upgrade Path

| Concern | v1 | v2 |
| --- | --- | --- |
| Entities | One per deployment | Many per deployment |
| Auth | Single-tenant JWT | Multi-tenant JWT, entity selection; auth service unchanged |
| Postgres | One schema, `entity_id` scoping | Row-level security (already designed in; enabled for v2) |
| Engine | Single consumer | Horizontal scaling — add consumers |
| API | Unchanged | Unchanged |

The financial data model does not change between v1 and v2. Only the auth service and Postgres isolation strategy evolve.

---

## 9. Out of Scope for This Spec

- Database schema (covered in data model spec)
- CSV import pipeline (covered in import spec)
- Processing engine algorithms (covered in engine spec)
- UI component design (covered in UI spec)
- CI/CD and production hardening
