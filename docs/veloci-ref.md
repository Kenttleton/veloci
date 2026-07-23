# Veloci — AI Reference Card

Concept-centric quick reference. Each section is self-contained: definition, schema fields, and pipeline role together. Load only the concepts relevant to your task.

---

## Financial Model

**Core unit:** `/day` rate — every income and spend is stored and calculated as a daily rate. Display layers scale (×30.44 monthly, ×91.31 quarterly, ×365 yearly).

**Rolling window formula (universal):**
```
actual_rate_per_day(t) = Σ |amount_i| for t_i in [t − W, t] / W
```
- `W = period_days` for named entries; `W = entity_config.system_window_days` for system entries
- Entry type (standing/variable/irregular) affects how `period_days` is detected, not the formula

**Three lanes:**
- `Projection` — expected rate from detected patterns
- `Actual` — observed rate from transactions
- `Drift` — actual minus projected; positive = ahead, negative = behind

**Margin** = income rate − spend rate (all Active accounts)

**Entry types** — affect pattern detection only, not rate formula:
- `standing` — regular cadence, consistent amount (≥3 obs); `period_days` from recurrence interval
- `variable` — regular cadence, inconsistent amount; `variable_method` = `avg` or `max`
- `irregular` — no detectable cadence; `period_days` from mean observed interval (default 30)

**Account status:**
- `active` — flows contribute to Income/Spend/Margin
- `passive` — tracked and projected, does not affect Margin

---

## Label

**What it is:** Entity-scoped name registry. Labels are the pivot point of the data model — entries reference labels, snapshots are keyed by label. Renaming a label requires no recalculation.

**Schema (`labels`):**
- `id` UUID PK
- `entity_id` UUID FK → entity
- `name` TEXT UNIQUE per entity
- `scope` TEXT — NULL = user-created; `system` = built-in (Income, Spend)

**System labels:** Two created per entity automatically: `Income` and `Spend`. Cannot be edited or deleted.

**In the pipeline:** Stage 4 aggregates entry rates → label rates. Stage 6 snapshots at label level (`node_id` = label id). Stage 5 computes trend per label.

**Key rule:** A label aggregates across multiple entry instances (e.g. subscription that changed price). The Label → Entry relationship is 1:many.

---

## Entry

**What it is:** One continuous rate signal instance. The atomic unit of the financial model. Multiple entries may share one label.

**Schema (`entries`):**
- `id` UUID PK
- `entity_id` UUID FK → entity
- `label_id` UUID FK → labels — required
- `direction` TEXT — `income` | `spend`
- `entry_type` TEXT — `standing` | `variable` | `irregular`
- `scope` TEXT — NULL = user/engine; `system` = built-in
- `period_days` INTEGER — rolling window W; default 30
- `variable_method` TEXT — `avg` | `max`; variable entries only
- `projected_rate_per_day` NUMERIC(12,4)
- `conditions` JSONB — auto-match rules; nullable for manual entries
- `priority` INTEGER — lower = matched first; default 100
- `status` TEXT — `pending` | `live` | `ended`
- `source` TEXT — `user` | `engine`
- `recurrence_anchor` TEXT — expected day/pattern for recurrence
- `next_due_date` DATE — engine-computed
- `project_tentatively` BOOLEAN — if TRUE, Stage 7 projects this pending entry
- `pending_amount_cents` BIGINT — forward-versioned amount
- `pending_effective_date` DATE — when pending amount activates
- `start_date` DATE — earliest matching transaction
- `end_date` DATE — NULL = currently active

**System entries:** Two per entity (`scope = 'system'`): Income and Spend. Conditions: `{"entry_direction":"income"}` / `{"entry_direction":"spend"}`. Use `system_window_days` from `entity_config` as W. Cannot be edited or deleted.

**In the pipeline:**
- Stage 1: entries are evaluated in `priority` order for transaction matching
- Stage 2: creates new `pending` entries from unmatched transaction clusters
- Stage 3: computes `actual_rate_per_day` per entry (not persisted directly)
- Stage 4: aggregates entry rates → label rates
- Stage 7: sets `alert_type = 'ended'` + `status = 'pending'` on entries with overdue `next_due_date`

**Conditions JSONB:** Evaluated per-transaction in Stage 1. Can match on merchant string, amount range, `entry_direction`, account. System entries use `entry_direction` conditions to catch everything.

---

## Conditions

**What it is:** A JSONB boolean tree stored on `entries.conditions` that Stage 1 evaluates against each transaction to determine a match. `NULL` means the entry has no auto-matching rules (manual-only entries).

**Tree structure:** Every node is either a logical operator or a leaf. Logical nodes have `"op"` and `"children"`. Children can be leaves or other logical operators — fully recursive. Compose operators to express any boolean logic: NOR = `NOT` wrapping `OR`, NAND = `NOT` wrapping `AND`, etc.

**Logical operators:**
```json
{"op": "AND", "children": [...]}
{"op": "OR",  "children": [...]}
{"op": "NOT", "children": [one_node]}
{"op": "XOR", "children": [exactly_two_nodes]}
```

**Transaction-target leaves** (Pass 1 — evaluated against `merchant_normalized`, `amount_cents`, `date`, `account_id`):
```
payee_exact           {"type":"payee_exact",          "value":"Netflix"}
payee_contains        {"type":"payee_contains",        "value":"NETFLIX"}
payee_not_contains    {"type":"payee_not_contains",    "value":"REFUND"}
payee_starts_with     {"type":"payee_starts_with",     "value":"AMZ"}
payee_ends_with       {"type":"payee_ends_with",       "value":".COM"}
payee_regex           {"type":"payee_regex",           "value":"^Netflix"}
imported_payee_one_of {"type":"imported_payee_one_of", "value":["Netflix","Hulu"]}
amount_range          {"type":"amount_range",          "min_cents":-2000, "max_cents":-1000}
date_day_of_month     {"type":"date_day_of_month",     "day":15, "tolerance_days":2}
date_range            {"type":"date_range",            "start":"2026-01-01","end":"2026-12-31"}
account_id            {"type":"account_id",            "value":"<uuid>"}
institution_id        {"type":"institution_id",        "value":"<uuid>"}
```
- All `payee_*` comparisons are case-insensitive except `payee_regex` (case controlled by inline flags, e.g. `(?i)`)
- `amount_range` bounds are both optional; omitting a bound leaves it open
- `date_day_of_month` `tolerance_days` defaults to 0; wrap-around month-end not handled

**Entry-target leaves** (Pass 2+ — evaluated against entries already matched for this transaction in earlier passes):
```
label_matched          {"type":"label_matched",           "label_id":"<uuid>"}
entry_direction        {"type":"entry_direction",          "direction":"spend"}
entry_type             {"type":"entry_type",               "entry_type":"standing"}
entry_period           {"type":"entry_period",             "min_days":25, "max_days":35}
entry_source           {"type":"entry_source",             "source":"engine"}
entry_fitness          {"type":"entry_fitness",             "score":{"overall":{"min":0.8},"merchant":{"min":0.7,"max":1.0}}}
entry_projected_rate   {"type":"entry_projected_rate",     "min":1.5, "max":5.0}
entry_recurrence_anchor{"type":"entry_recurrence_anchor",  "recurrence_anchor":"dom:15"}
```
- `label_matched`: true if that label UUID is in the transaction's accumulated matched-label set
- `entry_direction`: `income` | `spend` | `mixed`
- `entry_period`, `entry_projected_rate`: both bounds optional
- `entry_fitness`: all specified gates must be satisfied by the **same single** accumulated entry — a high `overall` on entry A and high `merchant` on entry B does not satisfy a gate requiring both

**Two-pass evaluation model:**
- **Pass 1**: Entries whose conditions contain only transaction-target leaves are evaluated first, in ascending `priority` order. Each match adds its `label_id` to the transaction's accumulated label set.
- **Pass 2+**: Entries whose conditions contain any entry-target leaf are evaluated iteratively against the accumulated state. Labels earned in pass N are available in pass N+1 (batched, not mid-pass). Terminates when the accumulated set is stable or a cycle is detected (logged, not an error).

**Priority:** Lower value matches first. Default 100. System entries match everything of their direction and should have the highest priority number so named entries take precedence.

**System entry conditions:**
- Income entry: `{"entry_direction": "income"}`
- Spend entry: `{"entry_direction": "spend"}`

**Assignment fit:** `transaction_entry_assignments.fit` = 1.0 for Stage 1 condition matches; = cluster composite fitness for Stage 2 engine-detected entries.

---

## Confidence Scores

**What it is:** A quality signal measuring how well an entry's configured metadata (`entry_type`, `recurrence_anchor`, `period_days`, `projected_rate_per_day`) fits the transactions actually matched by its conditions — measured over the entry's active lifespan (`start_date` to `end_date`). Applies to all entries, not just engine-detected ones. Stage 2 seeds initial values for new engine entries; values should be updated on all entries as transaction data accumulates across import cycles.

**The three components:**

| Field | Question it answers | How it's computed |
|-------|--------------------|--------------------|
| `merchant_fit` | Does this entry's payee pattern consistently resolve to one business? | 1.0 when all matched transactions share the same normalized merchant; Stage 2 always starts at 1.0 because it clusters by exact `merchant_normalized` |
| `timing_fit` | Do matched transactions arrive on the cadence described by `period_days` and `recurrence_anchor`? | 1.0 when interval std dev ≤ 5 days; decays as `5 / std_dev`; 0.0 for single-transaction entries (no interval yet) |
| `amount_fit` | Are matched transaction amounts consistent with `projected_rate_per_day`? | 1.0 when identical or single transaction; decays as `1 − max_deviation_from_median / median`; clamped to [0, 1] |

**Composite (`fitness`):** A type-weighted blend of the three components. Weights are higher on the dimensions that matter most for each entry type:

| Type | merchant | timing | amount |
|------|----------|--------|--------|
| standing | 0.20 | 0.40 | 0.40 |
| variable | 0.30 | 0.55 | 0.15 |
| irregular | 0.60 | 0.20 | 0.20 |

**Stage 2 classification thresholds** (component-driven, not a composite gate):

| Type | Requires |
|------|----------|
| `standing` | `timing ≥ 0.75` AND `amount ≥ 0.80` AND `observations ≥ 3` |
| `variable` | `observations ≥ 2` AND `timing ≥ 0.45` |
| `irregular` | Fallthrough — no reliable cadence |

Minimum 3 observations for standing prevents two identical transactions (1 interval, std_dev = 0) from falsely passing the timing gate with a perfect score.

**Stage 2 surfacing gate:** New clusters with `fitness < 0.3` are silently discarded — no entry created. A single new irregular transaction scores `merchant_fit=1.0, timing_fit=0.0, amount_fit=1.0` → fitness = 0.80, which clears the gate.

**Recurrence anchor format** (stored in `entries.recurrence_anchor`):

| Format | Meaning | Example |
|--------|---------|---------|
| `dow:N` | Weekly; N = 0(Mon)–6(Sun) | `dow:0` = every Monday |
| `dom:N` | Monthly; positive = day-of-month, negative = from month-end | `dom:15` = 15th; `dom:-1` = last day |
| `dom:N,M` | Semi-monthly; two DOM anchors | `dom:1,15` = 1st and 15th |
| `interval:N` | Arbitrary N-day cadence | `interval:91` = quarterly |

Days > 28 are normalized to negative month-end indices so anchors survive months of varying length (Jan 31 and Feb 28 both resolve to `dom:-1`).

---

## Transaction

**What it is:** Immutable record of a financial event. Source of truth for all calculations.

**Schema (`transactions`):**
- `id` UUID PK
- `entity_id` UUID FK → entity
- `account_id` UUID FK → accounts
- `import_batch_id` UUID FK → import_batches
- `date` DATE
- `amount_cents` BIGINT — positive = inflow (income/credit); negative = outflow (spend/debit)
- `imported_payee` TEXT — raw bank string; immutable
- `merchant_normalized` TEXT — Stage 0 output
- `imported_id` TEXT — bank dedup ID from CSV; nullable
- `settlement_status` TEXT — `flux` | `settled`; set at insert, never changed
- `imported_at` TIMESTAMPTZ

**Immutability rules:** `date`, `amount_cents`, `imported_payee` never change after insert. `settlement_status` set at insert and never changed.

**Flux vs settled:** `settled` = `transaction.date < computed_as_of − settlement_window_days`, where `computed_as_of` is `MAX(date)` of the transactions in the current import batch. Anchored to the latest date in the data, not wall-clock upload time — classification is deterministic and re-runnable. Flux rows from overlapping imports may be superseded (deleted + replaced). Settled rows are never deleted. `imported_at` on the transaction row is audit-only and never used in calculations.

**In the pipeline:**
- Stage 0: creates rows; sets `merchant_normalized` and `settlement_status`
- Stage 1: matched to entries → written to `transaction_entry_assignments`
- Stage 2: unmatched transactions → pattern detection → new pending entries
- Stage 3: reads transactions with date ranges for rate computation

---

## Transaction Entry Assignment

**Schema (`transaction_entry_assignments`):** Many-to-many join between transactions and entries.
- `transaction_id` UUID FK → transactions
- `entry_id` UUID FK → entries
- `fit` NUMERIC — 1.0 for Stage 1 condition matches; 0.0–1.0 for Stage 2 engine matches

---

## Account / Institution

**Schema (`accounts`):**
- `id` UUID PK
- `entity_id` UUID FK → entity
- `institution_id` UUID FK → institution_mappings (nullable)
- `name` TEXT UNIQUE per entity
- `account_type` TEXT — `checking` | `savings` | `credit` | `loan` | `mortgage` | `investment`
- `status` TEXT — `active` | `passive`
- `interest_rate` NUMERIC(8,4) — APY for savings / APR for debt
- `balance_cents` BIGINT — latest known balance snapshot
- `credit_limit_cents` BIGINT — credit accounts only

**Schema (`institution_mappings`):** CSV column config per bank. One per institution per entity.
- `id`, `entity_id`, `institution_name` TEXT UNIQUE per entity
- `source_type` TEXT — `csv` | `integration`
- `settlement_window_days` INTEGER (default 14)
- `dedup_window_days` INTEGER — date tolerance for overlapping CSV dedup
- `amount_tolerance_pct` FLOAT8 (default 0.005 = 0.5%)
- `date_col`, `amount_col`, `merchant_col` TEXT — CSV column names
- `amount_sign_convention` TEXT — `positive_is_credit` | `positive_is_debit`

---

## Snapshot

**What it is:** Rebuildable engine output. One row per label per calendar day. Safe to truncate and recompute at any time.

**Schema (`snapshots`):**
- `node_id` UUID FK → labels.id
- `node_type` TEXT — always `label`
- `snapshot_date` DATE — calendar day this row represents
- `computed_as_of` DATE — MAX(transactions.date) from the import run that wrote this row
- `actual_rate_per_day` NUMERIC(12,4)
- `projected_rate_per_day` NUMERIC(12,4)
- `drift_per_day` NUMERIC(12,4) — actual minus projected
- `slope_per_day` NUMERIC(14,6) — linear regression slope
- `r_squared` NUMERIC(4,3) — regression fit quality
- `rolling_window_total_cents` BIGINT

**Key rules:**
- OHLC candlestick high/low are NOT stored — API computes MAX/MIN at query time
- `snapshot_date` ≠ `computed_as_of`: an import covering historical data records its computation horizon separately
- Entry rates (Stage 3) are intermediate and NOT persisted; only label-level snapshots are stored

---

## Projection

**What it is:** Forward-looking signal superposition timeline. One row per (entity, account, projected_date) per job. Safe to truncate and recompute.

**Schema (`projections`):**

- `entity_id` UUID FK → entity
- `account_id` UUID FK → accounts — NULL = entity-level aggregate across all active accounts
- `job_id` UUID FK → processing_jobs
- `projected_date` DATE
- `income_rate_per_day` NUMERIC(12,4)
- `spend_rate_per_day` NUMERIC(12,4)
- `margin_rate_per_day` NUMERIC(12,4) — income minus spend
- `projected_balance_cents` BIGINT — running integral of margin; for bank comparison only
- `is_pinch_point` BOOLEAN — TRUE when margin < 0

**Computed at display layer (not stored):**
- `discretionary_rate` = `margin_rate_per_day / income_rate_per_day × 100`

---

## Entity / User / RBAC

**Schema (`entities`):** The highest-level domain scope in Veloci. A generic container for a household, family, or business — the right word depends on context, but the concept is the same. Every user, account, label, entry, and transaction belongs to an entity. The `name` is mutable (user-configurable); the `id` is an immutable UUID PK. Deleting an entity cascade-deletes all its data — this is intentional and must only be done via a direct DB query, never through the application.

- `id` UUID PK
- `name` TEXT NOT NULL — mutable display name; no uniqueness constraint
- `created_at` TIMESTAMPTZ

**Schema (`users`):**
- `id` UUID PK
- `auth_credential_id` UUID — bridge FK to `auth_credentials.id` in `veloci_auth` database
- `email` TEXT — denormalized from auth for display
- `name` TEXT

**Schema (`entity_config`):** One row per entity, created with defaults on first setup.

- `entity_id` UUID PK FK → entity
- `system_window_days` INTEGER (default 90) — rolling window W for system entries

**RBAC tables:** `roles`, `permissions`, `role_permissions`, `entity_users`
- `entity_users.entity_role` — `entity_admin` | `entity_user`
- Key permission strings: `entries:write`, `classifications:write`, `accounts:write`

---

## Auth Database (`veloci_auth`)

Separate Postgres database. No financial data.

**Schema (`auth_credentials`):** One row per registered user.
- `id` UUID PK — referenced as `user_id` in token chain
- `email` TEXT UNIQUE
- `password_hash` TEXT — bcrypt
- `system_role` TEXT — `server_admin` | `user`

**Schema (`tokens`):** JWT access and refresh tokens.
- `jti` TEXT UNIQUE — JWT ID claim; used for lookup and revocation
- `token_type` TEXT — `access` | `refresh`
- `parent_id` UUID FK → tokens(id) — access tokens only; cascade delete revokes all child access tokens
- `claims` JSONB — full JWT claims payload
- `expires_at` TIMESTAMPTZ
- `rotated_at` TIMESTAMPTZ — set on refresh token rotation; enables short grace window for concurrent requests

**Schema (`invite_tokens`):** One-time invite links. `accepted_at` set on first use; subsequent use rejected.

---

## Processing Job / Pipeline

**Schema (`processing_jobs`):** Audit log for every engine run.
- `id` UUID PK
- `entity_id` UUID FK → entity
- `job_type` TEXT — `import.process` | `entries.reprocess` | `account.analyze` | `balance.project`
- `status` TEXT — `queued` | `processing` | `complete` | `failed`
- `triggered_by` UUID FK → users
- Partial unique index: only one `queued` or `processing` job per `(entity_id, job_type)` at a time

**Job type → stages:**
| Job | Stages |
|-----|--------|
| `import.process` | 0→1→2→3→4→5→6→7 |
| `entries.reprocess` | 1→2→3→4→5→6→7 |
| `account.analyze` | 3→4→5→6→7 |
| `balance.project` | 7 only |

**Flux window (Stages 3–6 day-crawl):**
```
flux_start = computed_as_of − settlement_window_days
flux_end   = computed_as_of
```
Stages 3–6 run once per calendar day in `[flux_start .. flux_end]`. Stage 7 runs once after the crawl.

**Pipeline invariants:**
- All writes use UPSERT or DELETE+INSERT — idempotent; re-running produces identical output
- Stages 0–5 use read pool; Stages 6–7 use write pool
- Snapshots/projections are rebuildable from transactions + entries at any time
- Every query scoped to `entity_id = $1` — no cross-entity reads possible

---

## Stage Reference (I/O only)

**Stage 0 — CSV Normalization:**
- Input: pending CSV bytes + institution mapping
- Output: rows in `transactions`; `computed_as_of = MAX(date)` from inserted rows
- Deduplicates against existing; supersedes flux rows from overlapping imports; never deletes settled rows

**Stage 1 — Entry Matching:**
- Input: entity ID, transactions
- Output: rows in `transaction_entry_assignments`; list of unmatched tx IDs
- Evaluates live entries (status=`live`, end_date IS NULL, has conditions) in ascending `priority` order
- Updates `next_due_date` on entries where new matches advance recurrence

**Stage 2 — Pattern Detection:**
- Input: unmatched tx IDs from Stage 1
- Output: new `pending` entries; review metadata written to entries row
- Clusters by merchant similarity, timing regularity, amount consistency
- Creates/upserts label (by name), sets `source='engine'`, `start_date` = earliest tx in cluster
- Sets `project_tentatively=TRUE` when recurrence pattern is clear enough

**Stage 3 — Rate Computation (per day in flux window):**
- Input: entity ID, snapshot date, entries, assignments, transactions
- Output: `EntryRate` per active entry (not persisted)
- Pure calculation; no writes
- For system entries: W = `entity_config.system_window_days`; for named entries: W = `period_days`

**Stage 4 — Label Rate Aggregation:**
- Input: entry rates from Stage 3
- Output: `LabelRate` per label (not persisted)
- Sums actual and projected rates across all entries per label

**Stage 5 — Trend Regression:**
- Input: Stage 4 output + snapshot history from `snapshots`
- Output: `slope_per_day`, `r_squared`, `drift_per_day` per label (not persisted)
- Linear regression on `actual_rate_per_day` time series; parallelized with rayon

**Stage 6 — Snapshot Upsert:**
- Input: Stages 3, 4, 5 outputs; entity ID, job ID, snapshot date
- Output: upserted rows in `snapshots` (one per label per day)
- Atomic: all snapshots for this day commit or none do
- UPSERT on `(entity_id, node_id, snapshot_date)` — fully idempotent

**Stage 7 — Cash Flow Projection:**
- Input: entity ID, computed_as_of, active entries + pending entries where `project_tentatively=TRUE`
- Output: rows in `projections` (DELETE all + INSERT for 90 days forward); `alert_type='ended'` on overdue entries
- Runs ONCE after day-crawl completes
- Sets `alert_type='ended'` + `status='pending'` on entries whose `next_due_date` has passed without a match

---

## Import Pipeline (non-engine)

**Schema (`pending_imports`):** Staging area for uploaded CSVs. Retained after processing for audit.

**Schema (`import_batches`):** One record per completed `import.process` run. Records dedup counts: rows imported, skipped as duplicate, superseded.

**Import flow:**
1. User uploads CSV → `pending_imports`
2. Stage 0 normalizes + deduplicates → `transactions`
3. Stages 1–7 run via `import.process` job
4. User reviews pending entries in Ledger (approve / edit / reject)
5. Approved entries → `entries.reprocess` job runs stages 1–7 again

---

## Budget Views

Three views of the same snapshot/projection data — switching views does not reload data.

- **Pulse** — rate snapshot dashboard. Latest `actual_rate_per_day` per entry, ranked Income → entries → Margin. Drift indicator when actual diverges from projected.
- **Stack** — waterfall cascade. Income bar consumed by each spend entry in sequence, arriving at Margin.
- **Horizon** — candlestick history + projection line. OHLC computed at query time from daily snapshot series (not stored). Projection line from `projections` table. Drift shading (green/red) between actual and projected.

---

## Two-Database Architecture

| Database | Schema files | Contents |
|----------|-------------|----------|
| `veloci_auth` | `migrations/auth/001_auth_schema.sql` | credentials, tokens, invite_tokens |
| `veloci_app` | `migrations/app/001_app_schema.sql` | entities, users, RBAC |
| `veloci_app` | `migrations/app/002_financial_schema.sql` | financial data model |
| `veloci_app` | `migrations/app/002_rbac_seed.sql` | RBAC seed data |

The two databases share no schema. Bridge: `users.auth_credential_id` → `auth_credentials.id`, joined at the application layer.
