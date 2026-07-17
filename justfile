set dotenv-load := true
set shell := ["bash", "-euo", "pipefail", "-c"]

# Load env vars from .env. All defaults match .env.example.
pg_user   := env("POSTGRES_USER",          "postgres")
app_db    := env("VELOCI_APP_DB",           "veloci_app")
app_user  := env("VELOCI_APP_DB_USER",      "veloci_app_user")
app_pass  := env("VELOCI_APP_DB_PASSWORD",  "changeme_app")
auth_db   := env("VELOCI_AUTH_DB",          "veloci_auth")
auth_user := env("VELOCI_AUTH_DB_USER",     "veloci_auth_user")
auth_pass := env("VELOCI_AUTH_DB_PASSWORD", "changeme_auth")
mq_user   := env("RABBITMQ_USER",           "veloci")
mq_pass   := env("RABBITMQ_PASSWORD",       "changeme")

# ─── Help ─────────────────────────────────────────────────────────────────────

default:
    @just --list

# ─── Services (individual) ────────────────────────────────────────────────────

# Start postgres
postgres:
    docker compose up -d postgres

# Start rabbitmq
rabbitmq:
    docker compose up -d rabbitmq

# Start veloci-auth
auth:
    docker compose up -d veloci-auth

# Start veloci (BFF — API + web)
veloci:
    docker compose up -d veloci

# Start veloci-engine (Rust queue consumer)
engine:
    docker compose up -d veloci-engine

# ─── Compound commands ────────────────────────────────────────────────────────

# Start all services (production build)
all:
    docker compose up -d

# Start all services in dev mode with live reload (air for Go, cargo-watch for Rust, Vite HMR for web)
dev:
    docker compose -f docker-compose.yml -f docker-compose.dev.yml up

# Start infrastructure only (postgres + rabbitmq), wait for healthy, then migrate.
# Use this for engine/API development without running the full stack.
infra:
    docker compose up -d postgres rabbitmq
    @just _wait-postgres
    @just _wait-rabbitmq
    @just migrate
    @echo "Infrastructure ready. Management UI: http://localhost:15672"

# Stop all running services (preserves volumes)
down:
    docker compose down

# Stop dev services
dev-down:
    docker compose -f docker-compose.yml -f docker-compose.dev.yml down

# Remove all containers AND volumes — next start is fully fresh
clean:
    docker compose down --volumes

# ─── Migrations ───────────────────────────────────────────────────────────────
# The postgres container also runs these automatically on first start via
# scripts/init-db.sh (mounted as docker-entrypoint-initdb.d). Use these
# recipes to apply migrations manually against an already-running container,
# or to run individual files during development.

# Run all migrations in order (safe on fresh DB; fails if already applied)
migrate: _db-create _migrate-auth-001 _migrate-app-001 _migrate-app-002 _migrate-app-seed
    @echo "All migrations applied."

# Create databases and users (idempotent — duplicate errors are suppressed)
_db-create:
    @echo "→ Creating databases and users..."
    docker compose exec -T postgres psql -U "{{ pg_user }}" \
        -c "CREATE USER {{ auth_user }} WITH PASSWORD '{{ auth_pass }}'" 2>&1 || true
    docker compose exec -T postgres psql -U "{{ pg_user }}" \
        -c "CREATE DATABASE {{ auth_db }} OWNER {{ auth_user }}" 2>&1 || true
    docker compose exec -T postgres psql -U "{{ pg_user }}" \
        -c "CREATE USER {{ app_user }} WITH PASSWORD '{{ app_pass }}'" 2>&1 || true
    docker compose exec -T postgres psql -U "{{ pg_user }}" \
        -c "CREATE DATABASE {{ app_db }} OWNER {{ app_user }}" 2>&1 || true

_migrate-auth-001:
    @echo "→ migrations/auth/001_auth_schema.sql"
    docker compose exec -T postgres psql -U "{{ auth_user }}" -d "{{ auth_db }}" \
        -v ON_ERROR_STOP=1 -f /migrations/auth/001_auth_schema.sql

_migrate-app-001:
    @echo "→ migrations/app/001_app_schema.sql"
    docker compose exec -T postgres psql -U "{{ pg_user }}" -d "{{ app_db }}" \
        -v ON_ERROR_STOP=1 -f /migrations/app/001_app_schema.sql

_migrate-app-002:
    @echo "→ migrations/app/002_financial_schema.sql"
    docker compose exec -T postgres psql -U "{{ pg_user }}" -d "{{ app_db }}" \
        -v ON_ERROR_STOP=1 -f /migrations/app/002_financial_schema.sql

_migrate-app-seed:
    @echo "→ migrations/app/002_rbac_seed.sql"
    docker compose exec -T postgres psql -U "{{ pg_user }}" -d "{{ app_db }}" \
        -v ON_ERROR_STOP=1 -f /migrations/app/002_rbac_seed.sql

# ─── Code generation ─────────────────────────────────────────────────────────

# Generate auth OpenAPI spec → services/auth/api/openapi.json
gen-auth:
    cd services/auth && go run ./cmd/specgen -o api/openapi.json

# Regenerate api authclient (ogen), patch unknown-field handling, and generate api OpenAPI spec
gen-api: gen-auth
    cd services/api && go generate ./generate.go

# Generate web client from api spec (requires gen-api to have run first)
gen-web:
    cd services/web && npx orval

# Run full generation chain (auth spec → api client → api spec → web client)
gen: gen-api gen-web

# ─── sqlx compile-time SQL verification ──────────────────────────────────────
# Requires a running postgres (just infra). Generates .sqlx/ offline query cache.
# Once queries are converted from sqlx::query_as (runtime) to sqlx::query_as!
# (macro), set SQLX_OFFLINE=true in CI to compile without a live DB.

sqlx-prepare:
    cd services/engine && \
        DATABASE_URL="postgresql://{{ app_user }}:{{ app_pass }}@localhost:5432/{{ app_db }}" \
        cargo sqlx prepare

# ─── Dev seed ────────────────────────────────────────────────────────────────

# Seed dev data: entity, user, institution mapping, and test checking account.
# Run after: just migrate (and after logging in once via the API on a fresh volume).
# Prints entity_id and account_id on success — use these with enqueue-import.
dev-seed:
    cat scripts/dev-seed.sql | docker compose exec -T postgres psql -U "{{ pg_user }}" -d "{{ app_db }}" \
        -v ON_ERROR_STOP=1

# ─── Queue ────────────────────────────────────────────────────────────────────

# Check RabbitMQ management API and veloci.jobs queue status
queue-check:
    @echo "Checking RabbitMQ management API..."
    @curl -sf -u "{{ mq_user }}:{{ mq_pass }}" "http://localhost:15672/api/overview" -o /dev/null \
        || (echo "ERROR: RabbitMQ unreachable — run: just rabbitmq"; exit 1)
    @echo "Checking veloci.jobs queue..."
    @curl -sf -u "{{ mq_user }}:{{ mq_pass }}" "http://localhost:15672/api/queues/%2F/veloci.jobs" -o /dev/null \
        && echo "veloci.jobs: declared" \
        || echo "veloci.jobs: not yet declared (start the engine or run enqueue-import)"

# Insert a CSV into pending_imports and publish an import.process job.
# Requires: postgres + rabbitmq running, an entity and user in the DB.
#
# Usage: just enqueue-import transactions.csv <entity-uuid> <account-uuid>
enqueue-import csv entity_id account_id:
    @python3 scripts/enqueue_import.py "{{ csv }}" "{{ entity_id }}" "{{ account_id }}"

# ─── Internal health-wait helpers ────────────────────────────────────────────

_wait-postgres:
    @echo "Waiting for postgres..."
    @until docker compose exec -T postgres pg_isready -U "{{ pg_user }}" -q 2>/dev/null; do \
        printf '.'; sleep 1; \
    done
    @echo " ready."

_wait-rabbitmq:
    @echo "Waiting for RabbitMQ..."
    @until curl -sf -u "{{ mq_user }}:{{ mq_pass }}" "http://localhost:15672/api/overview" -o /dev/null 2>/dev/null; do \
        printf '.'; sleep 2; \
    done
    @echo " ready."
