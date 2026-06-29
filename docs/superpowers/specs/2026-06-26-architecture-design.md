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
|---|---|---|
| `veloci-web` | TypeScript / React | Static SPA — UI only, no server-side logic |
| `veloci-api` | Go + Cobra + Viper | REST API, all CRUD, JWT validation, job publishing. Cobra subcommands: `serve`, `migrate` |
| `veloci-engine` | Rust | Pattern clustering, rule evaluation, rate/slope calculations. Health via CLI subcommand |
| `veloci-auth` | Go + Cobra + Viper | Credential validation, JWT issuance, entity membership, RBAC. Cobra subcommands: `serve` |
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

**Engine reads Postgres directly** for large dataset analysis (rate/slope calculations over full transaction history). Writing results back to Postgres directly avoids routing large payloads through the API.

**Engine health check:** The engine binary supports a `health` subcommand — `veloci-engine health` — that independently connects to Postgres and RabbitMQ and exits `0` or `1`. Docker runs this as a separate short-lived process for its healthcheck. No HTTP server is added to the engine; it remains a pure queue consumer.

### API ↔ Auth
Auth is called once at login. It issues a signed JWT. All subsequent requests carry the JWT — `veloci-api` validates the signature locally without calling `veloci-auth` on each request. This keeps per-request latency low.

---

## 4. Financial Entity Model

A **financial entity** is the unit of financial ownership. It owns all accounts, transactions, entries, and rules. A family, household, or individual is one entity. All financial data in Postgres is keyed by `entity_id`.

```
entity
  └── users (via entity_users)
        └── role → permissions
  └── accounts
  └── transactions
  └── entries
  └── rules
```

**v1:** One entity per self-hosted deployment. Multiple users (family members) per entity.
**v2:** Multiple entities per deployment. Auth service gains multi-entity management.

---

## 5. Auth and RBAC

### JWT Payload
```json
{
  "user_id": "u_abc",
  "entity_id": "e_xyz",
  "role": "admin"
}
```

Every API request carries this JWT. `veloci-api` validates the signature and extracts `entity_id` to scope all queries, and `role` to enforce permissions.

### RBAC Schema

```
roles             — named roles (admin, member in v1; custom in v2)
permissions       — named permission strings (accounts:write, import:create, ...)
role_permissions  — join: role → [permissions]
entity_users      — join: user → entity → role
```

### v1 Seeded Roles

| Role | Permissions |
|---|---|
| `admin` | `accounts:write`, `accounts:read`, `import:create`, `rules:write`, `entries:write`, `reports:read`, `users:manage` |
| `member` | `accounts:read`, `entries:write`, `reports:read` |

### v2 Expansion
Custom role creation, additional permissions, per-user permission overrides. No schema changes required — the tables already support it.

---

## 6. Auth Flow

1. User POSTs credentials to `veloci-auth`
2. Auth validates against the users table, checks entity membership
3. Auth returns a signed JWT containing `user_id`, `entity_id`, `role`
4. Client stores JWT and sends it as `Authorization: Bearer <token>` on all requests
5. `veloci-api` validates JWT signature — no round-trip to auth service per request
6. API scopes all queries by `entity_id`, checks permissions for write/admin operations

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
|---|---|---|
| Entities | One per deployment | Many per deployment |
| Auth | Single-tenant JWT | Multi-tenant JWT, entity selection |
| Postgres | One schema, `entity_id` scoping | Row-level security or schema-per-entity |
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
