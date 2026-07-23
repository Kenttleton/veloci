# Veloci

*Personal Financial Velocity*

Veloci is a local-first personal finance app built around a single idea: every income source and spend expressed as a daily rate. Not a monthly budget snapshot — a continuous rate, like a speedometer for money.

> A $20/month Netflix subscription costs $0.66/day. You work 1.3 hours a month to pay for it. Neither fact is hidden — they just aren't shown. Veloci shows them.

---

## How it works

Every income source and spend lives as a **/day rate**. Monthly, quarterly, and yearly figures are just that rate scaled — the model never changes units. Your true daily position is income rate minus all spend rates, continuously.

Two lanes run in parallel:

- **Projection** — the expected rate from known spend and estimated income
- **Actual** — the real rate from imported transaction data

**Drift** is the delta between them — the primary diagnostic signal.

---

## Import cycle

Veloci is local-first and import-driven. No bank sync, no credentials to third-party services. You export CSV from your bank, import it, and Veloci processes it:

1. Normalizes merchant names and clusters transactions by pattern
2. Matches transactions to known entries automatically
3. Detects transfers by matching debit/credit pairs across accounts
4. Surfaces new patterns as candidates for your review
5. After approval, future matching is automatic

Most people have fewer than 50 true recurring items. After two or three import cycles, the review queue shrinks to only genuinely new activity.

---

## Views

**Pulse** — where you are right now  
Rate snapshot dashboard. Income at the top, all spend cascading below, Margin at the bottom. Every figure leads with the /day rate.

**Stack** — why your Margin is what it is  
Waterfall cascade showing how income is consumed by each spend entry in sequence. Thin bars at the bottom are the gut-punch insight moment.

**Horizon** — where you are going  
Line graph of Projection vs. Actual over time, with Drift shaded between them. Supports passive account overlays (debt payoff curves, savings projections) without affecting the core budget picture.

---

## Spend types

| Type | Description |
|------|-------------|
| **Standing** | Recurring spend — rent, subscriptions, loan minimums. Rate is exact. Treated as a savings goal for payoff day. |
| **Variable** | Recurring spend with fluctuating amounts — groceries, utilities. Rate by average or maximum. Treated as a savings goal for payoff day. |
| **Hit** | Unexpected negative event. Smoothed as debt to be paid off short-term (30 days default). |
| **Boost** | Unexpected positive event — refund, gift, bonus. Smoothed forward short-term (30 days). |


**Smoothing** amortizes large infrequent payments over a time window so your /day rate always reflects your true ongoing cost, including obligations that haven't billed yet.

---

## Design principles

- **Matter of fact, not prescriptive.** The app shows numbers. Users draw conclusions.
- **The /day rate is always primary.** Monthly translations are secondary context, never the lead figure.
- **Local-first.** Your financial data stays on your hardware.
- **Self-healing.** Cancelled subscriptions disappear from Actual as transactions stop arriving. Errors and gaps correct themselves over time without manual intervention.
- **Friction decreases over time.** The first import cycle is the hardest. Each subsequent cycle requires less effort.

---

## Architecture

Veloci runs as multiple processes but is not a microservices system. It is a **distributed monolith** — a single application decomposed into processes for specific technical reasons, deployed as a unit, sharing a single config file and the same Postgres and RabbitMQ instances. Nothing is independently deployable or scalable in isolation.

### API and engine are one logical unit

The `veloci-api` (Go) and `veloci-engine` (Rust) processes share the same application database and database user. The split exists for one reason: Go is a poor fit for CPU-bound pipeline work. The engine handles transaction clustering, pattern matching, and rate calculations — stages that benefit from Rust's performance characteristics. Everything else lives in the API.

From the application's perspective, the engine is the API's compute backend. They communicate asynchronously over RabbitMQ: the API publishes jobs when data changes, the engine processes them and writes results directly to Postgres. There is no API boundary between them — the engine reads and writes the app database directly.

### Auth is a deliberate boundary

`veloci-auth` is different. It owns its own database (`veloci_auth`) and is the only service that touches credential and token data. The API calls auth for every login and every token validation — auth is never called by the frontend directly and is not exposed outside the internal Docker network.

This separation is intentional: auth holds the one concern that is genuinely independent from the rest of the application. It is designed to be replaceable — a future deployment can swap `veloci-auth` for an external provider (Keycloak, Auth0, etc.) without touching the API or financial data services.

### Deployment

The full system ships as a single `docker-compose.yml`: web (React SPA), API, engine, auth, Postgres, and RabbitMQ. The only ports exposed to the host are the API and web. Everything else runs on the internal Docker network.

As the product expands to cover more account types and eventually a hosted offering, the architecture is designed to evolve — but v1 is intentionally a single, self-contained unit that runs on one machine.

---

## Configuration

Veloci uses a single TOML file shared across all backend services. Services are deployed together and share infrastructure (Postgres, RabbitMQ), so a single config file is simpler than per-service config. Before starting, copy the example and fill in your values:

```bash
cp config/veloci.toml.example config/veloci.toml
```

### Config file location

By default every service looks for `config/veloci.toml` relative to its working directory (the project root when run via `just`). To use a different path, set:

```bash
VELOCI_CONFIG_PATH=/path/to/your/veloci.toml
```

### Environment variable overrides

Every config value can also be set or overridden via an environment variable. The naming pattern is:

```text
VELOCI_<SECTION>_<KEY>
```

Dots in the config key path become underscores. Examples:

| Config key | Environment variable |
| --- | --- |
| `database.host` | `VELOCI_DATABASE_HOST` |
| `database.app.password` | `VELOCI_DATABASE_APP_PASSWORD` |
| `auth.jwt_secret` | `VELOCI_AUTH_JWT_SECRET` |
| `auth.port` | `VELOCI_AUTH_PORT` |
| `veloci.port` | `VELOCI_PORT` |

Env vars take precedence over the config file. This is useful in containerized deployments where you'd rather inject secrets than mount a file.

---

### Required values

These have no safe default and **must be set** before the services will start correctly.

| Key | Env var | Description |
| --- | --- | --- |
| `auth.jwt_secret` | `VELOCI_AUTH_JWT_SECRET` | Secret used to sign and verify JWTs. Must be at least 32 characters. Generate one with `openssl rand -hex 32`. |
| `auth.admin.email` | `VELOCI_AUTH_ADMIN_EMAIL` | Email for the initial admin account, seeded on first startup. |
| `auth.admin.password` | `VELOCI_AUTH_ADMIN_PASSWORD` | Password for the initial admin account. Only the bcrypt hash is stored in the database. |

---

### Full reference

#### `[database]` — shared by all backend services

| Key | Default | Description |
| --- | --- | --- |
| `database.host` | `localhost` | Postgres hostname |
| `database.port` | `5432` | Postgres port |
| `database.app.name` | `veloci_app` | Database used by the API and engine |
| `database.app.user` | `veloci_app_user` | Postgres user for the app database |
| `database.app.password` | `changeme` | Password for the app database user |
| `database.auth.name` | `veloci_auth` | Database used by the auth service |
| `database.auth.user` | `veloci_auth_user` | Postgres user for the auth database |
| `database.auth.password` | `changeme` | Password for the auth database user |

#### `[rabbitmq]` — used by the API and engine

| Key | Default | Description |
| --- | --- | --- |
| `rabbitmq.host` | `localhost` | RabbitMQ hostname |
| `rabbitmq.port` | `5672` | AMQP port |
| `rabbitmq.user` | `guest` | RabbitMQ user |
| `rabbitmq.password` | `guest` | RabbitMQ password |

#### `[auth]` — veloci-auth service

| Key | Default | Description |
| --- | --- | --- |
| `auth.port` | `8081` | Port the auth service listens on |
| `auth.jwt_secret` | *(none)* | **Required.** JWT signing secret, min 32 chars |
| `auth.admin.email` | *(none)* | **Required.** Seed admin email |
| `auth.admin.password` | *(none)* | **Required.** Seed admin password |

#### `[api]` — veloci-api service

| Key | Default | Description |
| --- | --- | --- |
| `api.port` | `8080` | Port the API service listens on |
| `api.auth.host` | `localhost` | Hostname where the API can reach the auth service |
| `api.auth.port` | `8081` | Port where the API can reach the auth service |

#### `[engine]` — veloci-engine service

| Key | Default | Description |
| --- | --- | --- |
| `engine.pool.read_max` | `12` | Max Postgres read connections |
| `engine.pool.write_max` | `4` | Max Postgres write connections |
| `engine.pipeline.import_concurrency` | `4` | Concurrent workers during a transaction import |
| `engine.pipeline.snapshot_chunk_size` | `500` | Rows per batch when writing computed snapshots |

---

## Roadmap

Veloci is currently in active development toward **v1**.

### v1 — Active development

Checking and savings accounts as the core account types. The full /day rate model, import/detect/review cycle, and Pulse/Stack/Horizon views. All accounts in v1 are Active. A HYSA can be added as an Active account — interest payments appear as Boost events, but there is no dedicated interest modeling yet.

### v1.1 — Interest on debt

Debt accounts: credit cards, personal loans, auto loans, and mortgages. Adds three calculations not available on standard accounts:

- **Minimum payment rate** — the committed /day outflow from your active budget
- **True cost rate** — principal plus interest over remaining term, as /day
- **Payoff projection** — what adding $X/day does to payoff date and total interest paid

> Instead of "should I pay an extra $100/month on this debt," Veloci asks "what does $3.29/day do to this debt?"

### v1.2 — Interest on accrual

Dedicated modeling for high-yield savings and other interest-bearing accounts. Yield expressed as a /day rate directly comparable to spend and debt costs.

### v1.3 — Market-based accounts

Money market, investment accounts, and crypto. All producing comparable /day rates through the same engine as every other account.

### v2 and beyond

Multi-currency support is planned before v2. v2 targets a paid hosted offering of the full v1 suite and begins expanding the Active/Passive model toward liquidity-based classification. v3 completes that — accounts that can initiate an immediate transfer become eligible to be Active regardless of type.

### Not planned

- AI-generated financial advice or recommendations
- Tax preparation or reporting
- Bill payment or financial transaction initiation
