# Veloci — Processing Engine & Data Model Design

**Date:** 2026-06-29
**Status:** Approved — data model revised 2026-06-30 by veloci-api spec
**Scope:** Rust processing engine pipeline, financial data model, review queue flow, import deduplication algorithm

> **Data model supersession:** `entries` and `entry_rules` are replaced by `rules` (defined in `2026-06-30-veloci-api-design.md`). `groups` and `group_members` are replaced by `labels`, `label_members`, and `label_rules`. `transaction_entry_assignments` is renamed `transaction_rule_assignments`. `computed_snapshots.node_type` values change from `entry|group` to `rule|label`. `entities` lives in `veloci_app` — FK constraints on `entity_id` are valid throughout.

---

## 1. Overview

The Veloci processing engine is a pure function: given (raw_transactions, rules, DAG), it produces computed_snapshots. It never calls `NOW()`, never generates UUIDs, and never owns state — all state lives in Postgres. The Go API orchestrates job dispatch; the Rust engine owns all computation.

Two Postgres tables are the source of truth:

| Table | Role |
|---|---|
| `raw_transactions` | Immutable, normalized transaction records. Written once at import time. Never modified. |
| `computed_snapshots` | Rebuildable calculations. Written by the engine after every analysis run. Safe to drop and recompute. |

This separation means rule changes can be applied retroactively with zero re-upload: the engine re-reads `raw_transactions`, applies the new rules, and overwrites `computed_snapshots`.

---

## 2. The 7-Stage Linear Pipeline

Every job type runs a contiguous suffix of these seven stages. No branching, no parallel tracks.

```
Stage 0  CSV normalization + 4-pass dedup     →  raw_transactions
Stage 1  Rule matching (pre/post, boolean)    →  transaction_rule_assignments
Stage 2  Pattern detection (unmatched only)   →  rules (status: pending_review)
Stage 3  Rate computation + smoothing         →  per-rule rates   [active only]
Stage 4  DAG aggregation (set-union)          →  per-label rates  [active only]
Stage 5  Slope + drift (linear regression)    →  trends           [active only]
Stage 6  Snapshot write (batch INSERT)        →  computed_snapshots
```

### Job Types and Entry Points

| Job Type | Stages Run | Trigger |
|---|---|---|
| `import.process` | 0 → 1 → 2 → 3 → 4 → 5 → 6 | New CSV uploaded |
| `rules.reprocess` | 1 → 2 → 3 → 4 → 5 → 6 | Rule created, modified, or deleted |
| `account.analyze` | 3 → 4 → 5 → 6 | Rule approved from review queue; manual recalculate |

`import.process` is the only job that writes to `raw_transactions`. All other jobs read from it.

---

## 3. Review Queue Gate

New rules detected in Stage 2 start in `status: pending_review`. Stages 3–6 filter exclusively on `status = 'active'`. Until the user approves a pending rule, it does not contribute to any rate, slope, or snapshot calculation.

```
Stage 2 detects pattern → rule created with status: pending_review
User sees review queue in UI → rule shown with:
  - suggested name
  - suggested entry_type
  - suggested match conditions (transparent + editable before approving)
  - sample merchants that triggered it
  - calculated rate preview (what it would be if approved)

User approves → rule.status = 'active'
                → job published: account.analyze
                → engine runs stages 3–6
                → Pulse view updates with new Margin impact
```

This makes the review queue the live preview: the engine has already done the work, the user sees the full picture before committing. No dry-run mode needed in the Go API.

---

## 4. Stage 0: CSV Normalization + Volatility-Aware Deduplication

**Input:** `pending_imports` record (contains `csv_bytes`, `date_range_start`, `date_range_end`, `institution_mapping_id`, `account_id`)

**Output:** New rows in `raw_transactions`; updated `import_batches` record with counts

### Normalization

1. Apply `institution_mappings` to identify date, amount, merchant, and `imported_id` columns.
2. Parse date strings to `DATE`. Parse amount to `BIGINT cents` using the mapping's `amount_sign_convention`.
3. Normalize merchant: strip leading/trailing whitespace; collapse internal whitespace; strip punctuation except hyphens and ampersands; title-case. Store raw bank string as `imported_payee` (immutable). Store normalized result as `merchant_normalized`.

### Volatility Model

Every transaction exists in one of three zones relative to the import timestamp `T` (`pending_imports.uploaded_at`):

| Zone | Condition | Meaning |
|---|---|---|
| **Settled** | `candidate.date < T - settlement_window_days` | Final and authoritative. No further changes expected. |
| **Flux** | `candidate.date >= T - settlement_window_days` | Pending or recently posted. Date or amount may still differ across exports. |
| **New** | `candidate.date > existing_boundary + dedup_window_days` | Beyond all previously imported data. Cannot be a duplicate. |

`settlement_window_days`, `dedup_window_days`, and `amount_tolerance_pct` are read from the `institution_mappings` record for this import.

`existing_boundary` = `MAX(date_range_end)` across all prior `import_batches` for this account (NULL on first import).

### Effective Settlement Status of Existing Rows

Before checking each candidate against the database, the engine computes the effective settlement status of any existing row it finds:

```
effective_status =
  if existing.settlement_status = 'settled'          → settled
  if existing.settlement_status = 'flux'
     AND NOW() - existing.imported_at > settlement_window_days  → effectively settled (aged)
  if existing.settlement_status = 'flux'
     AND NOW() - existing.imported_at <= settlement_window_days → young flux (supersedeable)
```

Aged flux rows are treated identically to settled rows for dedup purposes — they represent transactions that have had sufficient time to resolve without a newer import superseding them.

> **Determinism note:** `NOW()` appears only in this effective-status check, which is part of the import utility (Stage 0), not the financial calculation stages (3–5). Stages 3–6 include all rows regardless of settlement status and never branch on this field.

### Deduplication Passes

Passes run in order. A candidate matched in an earlier pass is not re-evaluated in later passes.

**Pass 1 — Exact imported_id match** *(primary path when bank provides IDs)*
- Only runs when `candidate.imported_id` is non-null.
- Query: find any `raw_transaction` in the same account where `imported_id = candidate.imported_id`.
- If found and effective_status = settled or aged flux → **skip** (genuine duplicate).
- If found and effective_status = young flux → **supersede**: delete old row (cascades `transaction_rule_assignments`), insert candidate.

**Pass 2 — New territory check**
- If `candidate.date > existing_boundary + dedup_window_days` → **insert directly**. No existing data can overlap this date range.

**Pass 3 — Volatility-aware exact merchant match**
- Query: find any `raw_transaction` in the same account where:
  - `merchant_normalized = candidate.merchant_normalized` (exact match)
  - `ABS(date - candidate.date) <= dedup_window_days`
  - `ABS(amount_cents - candidate.amount_cents) <= candidate.amount_cents * amount_tolerance_pct`
- If found and effective_status = settled or aged flux → **skip**.
- If found and effective_status = young flux → **supersede**.

**Pass 4 — Volatility-aware fuzzy merchant match**
- Query: find any `raw_transaction` in the same account within `dedup_window_days` where:
  - `merchant_normalized` shares ≥70% LCS ratio with `candidate.merchant_normalized`
  - `ABS(amount_cents - candidate.amount_cents) <= candidate.amount_cents * amount_tolerance_pct`
- If found and effective_status = settled or aged flux → **skip**.
- If found and effective_status = young flux → **supersede**.

**Pass 5 — Fallback insert**
- If no pass matched: insert as new `raw_transaction`.

### Setting settlement_status at Insert Time

When inserting a new row (Pass 2 fallback, Pass 5), `settlement_status` is determined once and never changed:

```
settlement_status =
  if candidate.date < T - settlement_window_days → 'settled'
  else                                           → 'flux'
```

### Dedup Query Scope

All dedup queries are bounded to `date BETWEEN candidate.date - dedup_window_days AND candidate.date + dedup_window_days`. This prevents unbounded full-table scans on large transaction histories.

---

## 5. Stage 1: Rule Matching

**Input:** `raw_transactions` (new rows from Stage 0, or all rows for `rules.reprocess`)

**Output:** `transaction_rule_assignments` rows

### Rule Structure

Each `rule` contains a boolean expression tree stored as JSONB in `rules.conditions`:

```jsonb
{
  "op": "AND",
  "children": [
    { "type": "imported_payee_contains", "value": "NETFLIX" },
    { "type": "amount_range", "min": 1000, "max": 2000 }
  ]
}
```

**Leaf node types:**

| Type | Description |
|---|---|
| `imported_payee_exact` | Case-insensitive exact match against `merchant_normalized` |
| `imported_payee_contains` | Substring match against `merchant_normalized` |
| `imported_payee_regex` | PCRE regex against `merchant_normalized` |
| `imported_payee_one_of` | Membership test against a list of normalized strings |
| `amount_range` | `min_cents` ≤ `amount_cents` ≤ `max_cents` (either bound optional) |
| `date_day_of_month` | Day of month falls within ± N days of the given day |
| `date_range` | Transaction date falls between `start` and `end` |
| `account_id` | Transaction belongs to a specific account |

**Logical operators:** `AND`, `OR`, `NOT`, `XOR`

Rules have a `stage` field (`pre` or `post`). Pre-stage rules run first in priority order; post-stage rules run after, allowing override patterns. Rules with lower `priority` integer values run first within each stage.

### Matching Algorithm

1. For each transaction, build a candidate set from `rules` ordered by `stage ASC, priority ASC`.
2. Evaluate each rule's condition tree recursively.
3. A transaction may match more than one rule (multiple assignments are valid — e.g., a Netflix charge matched by both a Netflix rule and a standing subscription rule).
4. Each match produces one `transaction_rule_assignments` row with the `rule_id` and a `confidence` of 1.0.
5. Unmatched transactions pass through to Stage 2.

---

## 6. Stage 2: Pattern Detection

**Input:** Transactions from Stage 1 that produced no assignments

**Output:** Candidate `rules` with `status: pending_review`, `source: engine`; linked `review_queue` records

### Clustering Algorithm

The engine clusters unmatched transactions into candidate rules using three signals:

1. **Merchant similarity** — transactions with `merchant_normalized` values sharing ≥70% LCS ratio are grouped together. This catches "AMZ*PRIME", "AMAZON PRIME", "AMZN/BILL" as one cluster.

2. **Amount regularity** — within a merchant cluster, the engine checks whether amounts are consistent (within ±2% of the cluster median). Consistent amounts suggest a `standing` entry; variable amounts suggest `variable`.

3. **Timing regularity** — the engine computes inter-transaction intervals within a cluster. Near-constant intervals (±5 days variance) suggest `standing`. Irregular intervals with consistent amounts suggest `single` or `hit`.

### Confidence Scoring

Each cluster gets a confidence score (0.0–1.0) based on:

- Number of observations (more = higher confidence)
- Amount consistency (lower variance = higher confidence)
- Timing regularity (lower interval variance = higher confidence)

Clusters below 0.3 confidence are not surfaced to the user — they remain unmatched and unqueued until more transactions arrive.

### Output

For each cluster above 0.3 confidence:

- Create one `rules` row with `status: pending_review`, `source: 'engine'`, with auto-generated `conditions` JSONB
- Create one `review_queue` row with the suggested name, type, conditions, rate preview, sample merchants, and confidence
- Create `transaction_rule_assignments` rows for each matched transaction with the confidence score

---

## 7. Stage 3: Rate Computation

**Input:** `transaction_rule_assignments` joined to `rules` WHERE `rules.status = 'active'`

**Output:** Per-rule `{actual_rate_per_day, projected_rate_per_day, window_days_used, transaction_count}`

### Rate Computation by Entry Type

**Standing**
```
actual_rate = amount_cents / detected_period_days
```
Period is derived from the median inter-transaction interval. If only one transaction exists, period defaults to the `smoothing_window_days` on the rule.

**Variable**
- If `variable_method = 'avg'`: `actual_rate = rolling_window_total_cents / window_days`
- If `variable_method = 'max'`: `actual_rate = max_transaction_cents / window_days`
- Rolling window width = `rule.smoothing_window_days`

**Single, Hit, Boost**
```
actual_rate = amount_cents / smoothing_window_days
```
For a $150 car repair with `smoothing_window_days = 30`: `actual_rate = 150_00 / 30 = 500 cents/day ($5.00/day)`.

**Projected Rate**

If the rule has a user-set `projected_rate_per_day`, use it directly.
If no projected rate is set, the engine uses the `actual_rate` from the most recent prior `computed_snapshot` for that rule as the projection baseline. For brand-new rules (no prior snapshot), `projected_rate = actual_rate` (drift = 0 on first appearance).

### Adaptive Window Width

For rules with fewer transactions than the window would normally expect, the window narrows to the actual data span. This prevents a rule with one transaction from being treated as a near-zero rate across a 365-day window. The `window_days_used` column records the actual window applied.

---

## 8. Stage 4: DAG Aggregation

**Input:** Per-rule rates from Stage 3; `labels` and `label_members` tables

**Output:** Per-label `{actual_rate_per_day, projected_rate_per_day, contributing_rule_count}`

### The Deduplication Problem

A rule can belong to multiple labels. Example:

- Netflix rule → Streaming (label)
- Netflix rule → Kent's Expenses (label)
- Both labels → Total Expenses (root label)

Naively summing rates through all paths would count Netflix twice at Total Expenses. The engine prevents this with memoized set-union rollup.

### Algorithm

1. **Build the DAG** from `label_members` (member_type = 'rule' or 'label').
2. **Topological sort** (Kahn's algorithm). If a cycle is detected, the job fails with an error — cycles are a data integrity violation.
3. **Process bottom-up** (leaves to root):
   - For each leaf rule node: `reachable_rule_set = {rule_id}`
   - For each label node: `reachable_rule_set = UNION(children's reachable_rule_sets)`
   - Label rate = `SUM of actual_rate_per_day for all rules in reachable_rule_set`
4. **Memoize** each node's `reachable_rule_set` — each set is computed once.

Because a set can only contain each `rule_id` once, Netflix contributes exactly once to Total Expenses regardless of how many labels it belongs to.

---

## 9. Stage 5: Slope + Drift

**Input:** Current per-node rates from Stages 3 and 4; prior `computed_snapshots` (last 90 days, all nodes)

**Output:** Per-node `{drift_per_day, slope_per_day, r_squared}`

### Drift

```
drift_per_day = actual_rate_per_day - projected_rate_per_day
```

Computed at every node (rule and label). Positive = spending more than projected. Negative = spending less than projected. Applies equally to income rules (positive = earning more than projected).

### Slope (Rate of Change)

The slope measures how fast the actual rate is changing over time. It is a linear regression over the most recent 90 days of snapshots for each node.

```
inputs:   [(snapshot.computed_as_of - first_snapshot.computed_as_of, actual_rate_per_day), ...]
outputs:  slope_per_day   — regression coefficient (units: $/day per day — a rate of rate change)
          r_squared       — goodness of fit (0.0–1.0 confidence in the trend line)
```

Minimum 2 data points required. With 0 or 1 prior snapshot: `slope_per_day = 0.0`, `r_squared = 0.0`.

### computed_as_of

The `computed_as_of` field on every snapshot is:

```sql
SELECT MAX(date) FROM raw_transactions WHERE entity_id = $1
```

Never `NOW()`. The engine is time-agnostic — it reports the state of the data as of the most recent transaction it has seen. The UI is responsible for interpreting what "now" means relative to that date.

---

## 10. Stage 6: Snapshot Write

**Input:** All per-node computed values from Stages 3–5

**Output:** Rows in `computed_snapshots`

The engine writes all snapshots for this run in a single Postgres transaction. Partial writes are not possible — either all snapshots commit together or none do.

```sql
INSERT INTO computed_snapshots (
  entity_id, node_id, node_type, computed_as_of, job_id,
  actual_rate_per_day, projected_rate_per_day, drift_per_day,
  slope_per_day, r_squared, transaction_count, window_days_used
)
VALUES ...
ON CONFLICT (entity_id, node_id, computed_as_of) DO UPDATE SET ...;
```

Prior snapshots for the same `(entity_id, node_id)` are retained — they form the historical series that Stage 5 reads in future runs.

---

## 11. Entity Isolation

### Design

Every financial table carries `entity_id` as a non-nullable foreign key. No query in `veloci-api` or `veloci-engine` ever returns rows without a `WHERE entity_id = $1` clause. The engine receives exactly one `entity_id` per job message and scopes every read and write to that value.

This is sufficient for v1 (single entity per deployment). For v2 (SaaS, many entities per deployment), Postgres Row-Level Security is layered on top — no schema changes required.

### v2: Row-Level Security

A single RLS policy on each financial table enforces isolation at the database layer, making it impossible for a query to leak cross-entity data even if application code has a bug:

```sql
-- Applied to every financial table (accounts, raw_transactions, rules, labels, etc.)
ALTER TABLE <table> ENABLE ROW LEVEL SECURITY;

CREATE POLICY entity_isolation ON <table>
  USING (entity_id = current_setting('app.current_entity_id')::uuid);
```

The Go API sets the session variable on every connection before executing queries:

```sql
SET LOCAL app.current_entity_id = '<entity_id_from_jwt>';
```

The Rust engine sets it per job, immediately after acquiring a connection from the pool:

```sql
SET LOCAL app.current_entity_id = '<entity_id_from_job_message>';
```

### Super Admin Access (v2)

A super admin is a user with the `super_admin` system role (stored on the `users` table, not on `entity_users`). Super admins do not bypass RLS — they impersonate a specific entity:

```sql
-- Support session: admin sets entity context to the customer's entity
SET LOCAL app.current_entity_id = '<target_entity_id>';
-- All subsequent queries see exactly what that entity sees, nothing more
```

This means:

- A super admin accessing entity A cannot see entity B's data in the same session
- Support access is scoped and auditable — each support action is tied to a specific entity
- No `BYPASSRLS` role ever exists in production; the policy cannot be disabled at runtime

### entities Table

```sql
CREATE TABLE entities (
  id          UUID PRIMARY KEY,
  name        TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

All financial tables reference `entities(id)`. In v1 this table has one row. In v2 it has one row per customer.

---

## 12. Full Data Model

### accounts

```sql
CREATE TABLE accounts (
  id              UUID PRIMARY KEY,
  entity_id       UUID NOT NULL REFERENCES entities(id),
  name            TEXT NOT NULL,
  account_type    TEXT NOT NULL CHECK (account_type IN ('checking','savings','credit','loan','mortgage','investment')),
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','passive')),
  interest_rate   NUMERIC(8,4),          -- APY for savings; APR for debt
  balance_cents   BIGINT,                -- latest snapshot from import
  credit_limit_cents BIGINT,            -- credit accounts only
  institution_id  UUID REFERENCES institution_mappings(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### institution_mappings

```sql
CREATE TABLE institution_mappings (
  id                    UUID PRIMARY KEY,
  entity_id             UUID NOT NULL REFERENCES entities(id),
  institution_name      TEXT NOT NULL,
  date_col              TEXT NOT NULL,
  amount_col            TEXT NOT NULL,
  merchant_col          TEXT NOT NULL,
  imported_id_col       TEXT,              -- nullable; bank may not provide
  balance_col           TEXT,
  debit_credit_col      TEXT,              -- nullable; for debit/credit split format
  amount_sign_convention TEXT NOT NULL CHECK (amount_sign_convention IN ('positive_is_credit','positive_is_debit'))
);
```

### pending_imports

Staging area for uploaded CSVs. The engine reads `csv_bytes` and processes it during `import.process`. Record is retained after processing for audit.

```sql
CREATE TABLE pending_imports (
  id               UUID PRIMARY KEY,
  entity_id        UUID NOT NULL REFERENCES entities(id),
  account_id       UUID NOT NULL REFERENCES accounts(id),
  institution_id   UUID REFERENCES institution_mappings(id),
  uploaded_by      UUID NOT NULL REFERENCES users(id),
  uploaded_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  csv_bytes        BYTEA NOT NULL,
  date_range_start DATE NOT NULL,
  date_range_end   DATE NOT NULL,
  row_count        INTEGER,
  status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','complete','failed')),
  job_id           UUID REFERENCES processing_jobs(id),
  error            TEXT
);
```

### import_batches

One record per completed `import.process` run. Tracks what was imported and what was skipped.

```sql
CREATE TABLE import_batches (
  id                             UUID PRIMARY KEY,
  pending_import_id              UUID NOT NULL REFERENCES pending_imports(id),
  entity_id                      UUID NOT NULL REFERENCES entities(id),
  account_id                     UUID NOT NULL REFERENCES accounts(id),
  processed_at                   TIMESTAMPTZ NOT NULL,
  date_range_start               DATE NOT NULL,
  date_range_end                 DATE NOT NULL,
  transactions_imported          INTEGER NOT NULL DEFAULT 0,
  transactions_skipped_duplicate INTEGER NOT NULL DEFAULT 0
);
```

### raw_transactions

Immutable. Written once by Stage 0. Never modified. The engine reads this table for all analysis jobs.

```sql
CREATE TABLE raw_transactions (
  id                  UUID PRIMARY KEY,
  entity_id           UUID NOT NULL REFERENCES entities(id),
  account_id          UUID NOT NULL REFERENCES accounts(id),
  import_batch_id     UUID NOT NULL REFERENCES import_batches(id),
  date                DATE NOT NULL,
  amount_cents        BIGINT NOT NULL,       -- positive = inflow; negative = outflow
  imported_payee      TEXT NOT NULL,         -- raw bank string; immutable
  merchant_normalized TEXT NOT NULL,         -- title-cased, stripped
  imported_id         TEXT                   -- bank's own dedup ID; nullable
);

CREATE INDEX ON raw_transactions (entity_id, account_id, date);
CREATE INDEX ON raw_transactions (entity_id, date);
```

### rules

Permanent match configuration. Replaces `entries` + `entry_rules` from the original spec. Full definition in `2026-06-30-veloci-api-design.md`; reproduced here for engine reference.

```sql
CREATE TABLE rules (
  id                     UUID PRIMARY KEY,
  entity_id              UUID NOT NULL REFERENCES entities(id),
  name                   TEXT NOT NULL,
  direction              TEXT NOT NULL CHECK (direction IN ('income','expense')),
  entry_type             TEXT NOT NULL CHECK (entry_type IN ('standing','single','hit','boost','variable')),
  smoothing_window_days  INTEGER NOT NULL DEFAULT 30,
  variable_method        TEXT CHECK (variable_method IN ('avg','max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB NOT NULL,
  stage                  TEXT NOT NULL DEFAULT 'pre' CHECK (stage IN ('pre','post')),
  priority               INTEGER NOT NULL DEFAULT 100,
  status                 TEXT NOT NULL DEFAULT 'pending_review'
                         CHECK (status IN ('pending_review','active','inactive')),
  source                 TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user','engine')),
  created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON rules (entity_id, status);
CREATE INDEX ON rules (entity_id, priority);
```

**Condition tree shape (JSONB):**

```
Node:  { "op": "AND"|"OR"|"NOT"|"XOR", "children": [Node|Leaf, ...] }
Leaf:  { "type": "<leaf_type>", "value": <string|number|array|{min,max}> }
```

`NOT` and `XOR` nodes require exactly 1 and exactly 2 children respectively. `AND` / `OR` accept 1 or more.

### transaction_rule_assignments

Many-to-many join between transactions and rules. A transaction can match multiple rules (legitimate — see DAG deduplication in Stage 4).

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

Named DAG nodes. Replaces `groups`. Full definition in `2026-06-30-veloci-api-design.md`.

```sql
CREATE TABLE labels (
  id         UUID PRIMARY KEY,
  entity_id  UUID NOT NULL REFERENCES entities(id),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### label_members

DAG edges. `member_type` distinguishes rule leaves from label nodes.

```sql
CREATE TABLE label_members (
  label_id    UUID NOT NULL REFERENCES labels(id),
  member_id   UUID NOT NULL,
  member_type TEXT NOT NULL CHECK (member_type IN ('rule','label')),
  PRIMARY KEY (label_id, member_id)
);
```

Cycle prevention is enforced at the application layer in `veloci-api` before publishing a job. The engine will error on cycle detection as a safeguard.

### label_rules

Automated conditions for applying a label to matching rules. Full definition in `2026-06-30-veloci-api-design.md`.

```sql
CREATE TABLE label_rules (
  id         UUID PRIMARY KEY,
  label_id   UUID NOT NULL REFERENCES labels(id),
  entity_id  UUID NOT NULL REFERENCES entities(id),
  conditions JSONB NOT NULL,
  priority   INTEGER NOT NULL DEFAULT 100,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### review_queue

One record per engine-detected candidate rule awaiting user review. The suggested `conditions` JSONB is transparent and editable before the user approves.

```sql
CREATE TABLE review_queue (
  id                        UUID PRIMARY KEY,
  entity_id                 UUID NOT NULL REFERENCES entities(id),
  rule_id                   UUID NOT NULL REFERENCES rules(id),
  job_id                    UUID NOT NULL REFERENCES processing_jobs(id),
  suggested_name            TEXT NOT NULL,
  suggested_entry_type      TEXT NOT NULL,
  suggested_conditions      JSONB NOT NULL,
  suggested_rate_per_day    NUMERIC(12,4) NOT NULL,
  matched_transaction_count INTEGER NOT NULL,
  confidence                NUMERIC(4,3) NOT NULL,
  sample_merchants          TEXT[] NOT NULL,
  status                    TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending','approved','rejected','modified')),
  reviewed_by               UUID REFERENCES users(id),
  reviewed_at               TIMESTAMPTZ
);
```

### computed_snapshots

Rebuildable output from the engine. Safe to truncate and recompute at any time.

```sql
CREATE TABLE computed_snapshots (
  id                     UUID PRIMARY KEY,
  entity_id              UUID NOT NULL REFERENCES entities(id),
  node_id                UUID NOT NULL,        -- rule_id or label_id
  node_type              TEXT NOT NULL CHECK (node_type IN ('rule','label')),
  computed_as_of         DATE NOT NULL,        -- MAX(raw_transaction.date) for this entity
  job_id                 UUID NOT NULL REFERENCES processing_jobs(id),
  actual_rate_per_day    NUMERIC(12,4) NOT NULL,
  projected_rate_per_day NUMERIC(12,4) NOT NULL,
  drift_per_day          NUMERIC(12,4) NOT NULL,
  slope_per_day          NUMERIC(14,6) NOT NULL,   -- $/day per day
  r_squared              NUMERIC(4,3) NOT NULL,
  transaction_count      INTEGER NOT NULL,
  window_days_used       INTEGER NOT NULL,

  UNIQUE (entity_id, node_id, computed_as_of)
);

CREATE INDEX ON computed_snapshots (entity_id, node_id, computed_as_of DESC);
```

### processing_jobs

Audit log for every job published and processed.

```sql
CREATE TABLE processing_jobs (
  id            UUID PRIMARY KEY,
  entity_id     UUID NOT NULL REFERENCES entities(id),
  job_type      TEXT NOT NULL CHECK (job_type IN ('import.process','rules.reprocess','account.analyze')),
  triggered_by  UUID NOT NULL REFERENCES users(id),
  queued_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  started_at    TIMESTAMPTZ,
  completed_at  TIMESTAMPTZ,
  status        TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','processing','complete','failed')),
  error         TEXT,
  metadata      JSONB      -- e.g., { "pending_import_id": "..." } for import jobs
);
```

---

## 13. Key Invariants

These invariants are structural constraints on the system, not implementation details. Violations are bugs.

1. **Engine never calls `NOW()`** — `computed_as_of` is always derived from `MAX(raw_transactions.date)`. The engine has no concept of the current date.

2. **`raw_transactions` is immutable** — the engine never UPDATE or DELETE rows in this table. All re-runs read the same source data.

3. **Stages 3–6 filter on `status = 'active'`** — rules in `pending_review` or `inactive` are invisible to all calculation stages.

4. **Snapshot writes are atomic** — all snapshots for a given job commit in one transaction or not at all.

5. **DAG is acyclic** — cycle check runs in the Go API before publishing. The engine errors on cycle detection as a defense-in-depth check.

6. **Deduplication is idempotent** — running Stage 0 twice on the same CSV produces the same `raw_transactions` row count the second time (zero new inserts).

7. **UUIDs are generated by the Go API only** — the Rust engine never generates UUIDs. It reads them from Postgres or from the RabbitMQ job message.

8. **`amount_cents` sign convention** — positive = inflow (income, credit), negative = outflow (expense, debit). Applied during Stage 0 normalization based on `institution_mappings.amount_sign_convention`.

---

## 14. Execution Model

The engine is built for speed at two levels: parallelism within a single job (intra-job), and parallelism across multiple entities' jobs running simultaneously (inter-job).

### Intra-Job Parallelism

Each stage has a defined parallelism strategy. The stage order is a hard dependency chain — Stage N's output feeds Stage N+1 — but within each stage the work is independent wherever possible.

| Stage | Strategy | Rust primitive |
| --- | --- | --- |
| 0 — CSV norm + dedup | Normalize all rows in parallel; dedup lookups in parallel; batch INSERT sequentially at end | `rayon::par_iter` for normalization; `FuturesUnordered` for concurrent Postgres dedup reads |
| 1 — Rule matching | Each transaction evaluated independently; shard transactions across threads | `rayon::par_iter` over transaction slice |
| 2 — Pattern detection | Global clustering pass is sequential (must see all unmatched at once); cluster scoring is parallel | Sequential LCS clustering → `rayon::par_iter` over candidate clusters for confidence scoring |
| 3 — Rate computation | Each rule's rate computed from its own transactions only; fully independent | `rayon::par_iter` over active rules |
| 4 — DAG aggregation | Topological sort is sequential; nodes within each level of the DAG are independent | Sequential toposort → level-parallel: `rayon::par_iter` over each level's node set |
| 5 — Slope + drift | Each node's regression reads only its own snapshot history; fully independent | `rayon::par_iter` over all nodes |
| 6 — Snapshot write | Data serialization parallel; single atomic `INSERT` at the end is sequential | `rayon::par_iter` to build row structs → single `sqlx` batch execute |

**Why rayon over Tokio for CPU work:** Tokio is async I/O — its threads are optimized for waiting, not computing. Rayon is a work-stealing CPU thread pool. Stages 1, 3, 4, and 5 are pure computation (no I/O mid-stage); rayon saturates all cores without blocking the async runtime. Stages 0 and 2 mix I/O and CPU — those use `spawn_blocking` to hand rayon work off without blocking Tokio's executor.

### Inter-Job Parallelism

Every job is entity-scoped and stateless. Multiple engine processes consume from the same RabbitMQ queue simultaneously. Each consumer locks one job at a time (via RabbitMQ acknowledgment); no coordination or shared mutable state exists between consumers.

```text
RabbitMQ queue
  ├── Consumer 1 (engine instance): entity_A → import.process  ─┐
  ├── Consumer 2 (engine instance): entity_B → account.analyze  ├── fully parallel, zero contention
  └── Consumer 3 (engine instance): entity_C → rules.reprocess ─┘
```

For v1, a single engine container is sufficient. For v2, scaling is horizontal: add engine containers and throughput increases linearly. No code changes required — RabbitMQ handles distribution.

### What Cannot Be Parallelized

- **Stage ordering** — Stages 3 → 4 → 5 → 6 are a strict dependency chain. Stage 4 (DAG aggregation) cannot start until all per-entry rates from Stage 3 are complete.
- **Stage 2 clustering** — The LCS similarity pass must see all unmatched transactions before any cluster can be formed. The global scan is sequential.
- **Stage 6 commit** — The final snapshot INSERT must be a single Postgres transaction. The commit point is inherently sequential.
- **Same-entity concurrent jobs** — Two jobs for the same `entity_id` must not run simultaneously; they would produce conflicting snapshot writes. The Go API enforces a per-entity job lock before publishing. If a job for entity X is already in `processing` state, a new job for entity X is queued and deferred until the current one completes.

### Connection Pool Sizing

Each engine instance maintains one `sqlx` async connection pool. The pool size should be set to match the number of Tokio worker threads (`TOKIO_WORKER_THREADS`, default = CPU count). Rayon `spawn_blocking` tasks each acquire a connection for their batch I/O phase; the pool prevents connection exhaustion under parallel load.

Recommended pool configuration per engine instance:

```toml
min_connections = 2
max_connections = TOKIO_WORKER_THREADS + 2   # headroom for health check and job ack
acquire_timeout = 5s
idle_timeout    = 10m
```

---

## 15. RabbitMQ Job Message Shape

The Go API publishes JSON to RabbitMQ. The engine deserializes this into its job context.

```json
{
  "job_id": "uuid",
  "entity_id": "uuid",
  "job_type": "import.process",
  "metadata": {
    "pending_import_id": "uuid"
  }
}
```

For `rules.reprocess` and `account.analyze`, `metadata` is empty. The engine uses `entity_id` to scope all Postgres queries.

---

## 16. Out of Scope for This Spec

- Go API endpoints for importing, reviewing, and approving rules (covered in import pipeline spec)
- UI views: Pulse, Stack, Horizon (covered in UI spec)
- Transfer detection (two debits/credits that cancel at the budget level — deferred to v1.1)
- Debt account rate calculations (APR-adjusted payoff projections — deferred to v1.1)
- Projected-only rules (rules in the Projection lane with no transaction history — deferred to v1.1)
