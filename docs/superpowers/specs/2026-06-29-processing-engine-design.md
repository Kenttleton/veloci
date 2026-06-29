# Veloci — Processing Engine & Data Model Design

**Date:** 2026-06-29
**Status:** Approved
**Scope:** Rust processing engine pipeline, full financial data model, review queue flow, import deduplication algorithm

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
Stage 1  Rule matching (pre/post, boolean)    →  transaction_entry_assignments
Stage 2  Pattern detection (unmatched only)   →  entries (status: pending_review)
Stage 3  Rate computation + smoothing         →  per-entry rates  [active only]
Stage 4  DAG aggregation (set-union)          →  per-group rates  [active only]
Stage 5  Slope + drift (linear regression)    →  trends           [active only]
Stage 6  Snapshot write (batch INSERT)        →  computed_snapshots
```

### Job Types and Entry Points

| Job Type | Stages Run | Trigger |
|---|---|---|
| `import.process` | 0 → 1 → 2 → 3 → 4 → 5 → 6 | New CSV uploaded |
| `rules.reprocess` | 1 → 2 → 3 → 4 → 5 → 6 | Rule created, modified, or deleted |
| `account.analyze` | 3 → 4 → 5 → 6 | Entry approved from review queue; manual recalculate |

`import.process` is the only job that writes to `raw_transactions`. All other jobs read from it.

---

## 3. Review Queue Gate

New entries detected in Stage 2 start in `status: pending_review`. Stages 3–6 filter exclusively on `status = 'active'`. Until the user approves a pending entry, it does not contribute to any rate, slope, or snapshot calculation.

```
Stage 2 detects pattern → entry created with status: pending_review
User sees review queue in UI → entry shown with:
  - suggested name
  - suggested entry_type
  - sample merchants that triggered it
  - calculated rate preview (what it would be if approved)

User approves → entry.status = 'active'
                 → job published: account.analyze
                 → engine runs stages 3–6
                 → Pulse view updates with new Margin impact
```

This makes the review queue the live preview: the engine has already done the work, the user sees the full picture before committing. No dry-run mode needed in the Go API.

---

## 4. Stage 0: CSV Normalization + 4-Pass Deduplication

**Input:** `pending_imports` record (contains `csv_bytes`, `date_range_start`, `date_range_end`, `institution_mapping_id`, `account_id`)

**Output:** New rows in `raw_transactions`; updated `import_batches` record with counts

### Normalization

1. Apply `institution_mappings` to identify date, amount, merchant, and `imported_id` columns.
2. Parse date strings to `DATE`. Parse amount to `BIGINT cents` using the mapping's `amount_sign_convention`.
3. Normalize merchant: strip leading/trailing whitespace; collapse internal whitespace; strip punctuation except hyphens and ampersands; title-case. Store raw bank string as `imported_payee` (immutable). Store normalized result as `merchant_normalized`.

### 4-Pass Deduplication

Borrowed from Actual Budget. Passes run in order; a transaction matched in an earlier pass is not re-evaluated in later passes.

**Pass 1 — Exact imported_id match**
- Query: find any `raw_transaction` in the same account where `imported_id = candidate.imported_id` (only when the bank provides `imported_id`)
- If found: skip (duplicate)

**Pass 2 — Date range + approximate amount**
- Query: find any `raw_transaction` in the same account where:
  - `date BETWEEN candidate.date - 7 AND candidate.date + 7`
  - `ABS(amount_cents - candidate.amount_cents) <= candidate.amount_cents * 0.02` (within 2%)
- If found: skip (likely the same transaction with slight date/amount drift)

**Pass 3 — Merchant partial match**
- Query: find any `raw_transaction` in the same account within ±7 days where the `merchant_normalized` strings share at least a 70% longest common subsequence ratio (computed in Rust, no SQL LIKE required)
- If found: skip (same merchant, same window)

**Pass 4 — Fallback insert**
- If no pass matched: insert as new `raw_transaction`

### Dedup Scope

The date range query is bounded to `pending_import.date_range_start - 7 days` through `pending_import.date_range_end + 7 days`. This prevents unbounded full-table scans on large transaction histories while covering edge cases at range boundaries.

---

## 5. Stage 1: Rule Matching

**Input:** `raw_transactions` (new rows from Stage 0, or all rows for `rules.reprocess`)

**Output:** `transaction_entry_assignments` rows

### Rule Structure

Each `entry_rule` contains a boolean expression tree stored as JSONB:

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

1. For each transaction, build a candidate set from rules ordered by `stage ASC, priority ASC`.
2. Evaluate each rule's condition tree recursively.
3. A transaction may match more than one rule (multiple entry assignments are valid — e.g., Netflix under "Streaming" and under "Kent's expenses").
4. Each match produces one `transaction_entry_assignments` row with the `rule_id` and a `confidence` of 1.0.
5. Unmatched transactions pass through to Stage 2.

---

## 6. Stage 2: Pattern Detection

**Input:** Transactions from Stage 1 that produced no assignments

**Output:** Candidate `entries` with `status: pending_review`, linked `review_queue` records

### Clustering Algorithm

The engine clusters unmatched transactions into candidate entries using three signals:

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
- Create one `entries` row with `status: pending_review`, `created_by: 'engine'`
- Create one `review_queue` row with the suggested name, type, rate preview, sample merchants, and confidence
- Create `transaction_entry_assignments` rows for each matched transaction with the confidence score (no `rule_id` — these are engine-detected, not rule-matched)

---

## 7. Stage 3: Rate Computation

**Input:** `transaction_entry_assignments` joined to `entries` WHERE `entries.status = 'active'`

**Output:** Per-entry `{actual_rate_per_day, projected_rate_per_day, window_days_used, transaction_count}`

### Rate Computation by Entry Type

**Standing**
```
actual_rate = amount_cents / detected_period_days
```
Period is derived from the median inter-transaction interval. If only one transaction exists, period defaults to the `smoothing_window_days` on the entry.

**Variable**
- If `variable_method = 'avg'`: `actual_rate = rolling_window_total_cents / window_days`
- If `variable_method = 'max'`: `actual_rate = max_transaction_cents / window_days`
- Rolling window width = `entry.smoothing_window_days`

**Single, Hit, Boost**
```
actual_rate = amount_cents / smoothing_window_days
```
For a $150 car repair with `smoothing_window_days = 30`: `actual_rate = 150_00 / 30 = 500 cents/day ($5.00/day)`.

**Projected Rate**

If the entry has a user-set `projected_rate_per_day`, use it directly.
If no projected rate is set, the engine uses the `actual_rate` from the most recent prior `computed_snapshot` for that entry as the projection baseline. For brand-new entries (no prior snapshot), `projected_rate = actual_rate` (drift = 0 on first appearance).

### Adaptive Window Width

For entries with fewer transactions than the window would normally expect, the window narrows to the actual data span. This prevents an entry with one transaction from being treated as a near-zero rate across a 365-day window. The `window_days_used` column records the actual window applied.

---

## 8. Stage 4: DAG Aggregation

**Input:** Per-entry rates from Stage 3; `groups` and `group_members` tables

**Output:** Per-group `{actual_rate_per_day, projected_rate_per_day, contributing_entry_count}`

### The Deduplication Problem

An entry can belong to multiple groups. Example:
- Netflix → Streaming (group)
- Netflix → Kent's Expenses (group)
- Both groups → Total Expenses (root group)

Naively summing rates through all paths would count Netflix twice at Total Expenses. The engine prevents this with memoized set-union rollup.

### Algorithm

1. **Build the DAG** from `group_members` (member_type = 'entry' or 'group').
2. **Topological sort** (Kahn's algorithm). If a cycle is detected, the job fails with an error — cycles are a data integrity violation.
3. **Process bottom-up** (leaves to root):
   - For each leaf entry node: `reachable_entry_set = {entry_id}`
   - For each group node: `reachable_entry_set = UNION(children's reachable_entry_sets)`
   - Group rate = `SUM of actual_rate_per_day for all entries in reachable_entry_set`
4. **Memoize** each node's `reachable_entry_set` — each set is computed once.

Because a set can only contain each `entry_id` once, Netflix contributes exactly once to Total Expenses regardless of how many groups it belongs to.

---

## 9. Stage 5: Slope + Drift

**Input:** Current per-node rates from Stages 3 and 4; prior `computed_snapshots` (last 90 days, all nodes)

**Output:** Per-node `{drift_per_day, slope_per_day, r_squared}`

### Drift

```
drift_per_day = actual_rate_per_day - projected_rate_per_day
```

Computed at every node (entry and group). Positive = spending more than projected. Negative = spending less than projected. Applies equally to income entries (positive = earning more than projected).

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

## 11. Full Data Model

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

### entries

The atomic financial unit. Each income source or expense is one entry.

```sql
CREATE TABLE entries (
  id                   UUID PRIMARY KEY,
  entity_id            UUID NOT NULL REFERENCES entities(id),
  name                 TEXT NOT NULL,
  direction            TEXT NOT NULL CHECK (direction IN ('income','expense')),
  entry_type           TEXT NOT NULL CHECK (entry_type IN ('standing','single','hit','boost','variable')),
  smoothing_window_days INTEGER NOT NULL DEFAULT 30,
  variable_method      TEXT CHECK (variable_method IN ('avg','max')),  -- variable entries only
  projected_rate_per_day NUMERIC(12,4),                                -- null = derive from history
  status               TEXT NOT NULL DEFAULT 'pending_review' CHECK (status IN ('pending_review','active','inactive')),
  confidence           NUMERIC(4,3),                                   -- engine-assigned; null if user-created
  created_by           TEXT NOT NULL CHECK (created_by IN ('user','engine'))
);
```

### entry_rules

Boolean expression tree for matching transactions to entries.

```sql
CREATE TABLE entry_rules (
  id          UUID PRIMARY KEY,
  entity_id   UUID NOT NULL REFERENCES entities(id),
  entry_id    UUID NOT NULL REFERENCES entries(id),
  stage       TEXT NOT NULL CHECK (stage IN ('pre','post')),
  priority    INTEGER NOT NULL DEFAULT 100,
  conditions  JSONB NOT NULL   -- boolean expression tree; see Section 5
);

CREATE INDEX ON entry_rules (entity_id, entry_id);
```

**Condition tree shape (JSONB):**

```
Node:  { "op": "AND"|"OR"|"NOT"|"XOR", "children": [Node|Leaf, ...] }
Leaf:  { "type": "<leaf_type>", "value": <string|number|array|{min,max}> }
```

`NOT` and `XOR` nodes require exactly 1 and exactly 2 children respectively. `AND` / `OR` accept 1 or more.

### transaction_entry_assignments

Many-to-many join between transactions and entries. A transaction can match multiple entries (legitimate — see DAG deduplication in Stage 4).

```sql
CREATE TABLE transaction_entry_assignments (
  transaction_id  UUID NOT NULL REFERENCES raw_transactions(id),
  entry_id        UUID NOT NULL REFERENCES entries(id),
  rule_id         UUID REFERENCES entry_rules(id),   -- null if engine-detected (Stage 2)
  confidence      NUMERIC(4,3) NOT NULL DEFAULT 1.0,
  assigned_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (transaction_id, entry_id)
);
```

### groups

User-defined grouping nodes in the DAG. Contain entries and/or other groups.

```sql
CREATE TABLE groups (
  id          UUID PRIMARY KEY,
  entity_id   UUID NOT NULL REFERENCES entities(id),
  name        TEXT NOT NULL,
  created_by  UUID NOT NULL REFERENCES users(id)
);
```

### group_members

DAG edges. `member_type` distinguishes entry leaves from group nodes.

```sql
CREATE TABLE group_members (
  group_id     UUID NOT NULL REFERENCES groups(id),
  member_id    UUID NOT NULL,
  member_type  TEXT NOT NULL CHECK (member_type IN ('entry','group')),
  PRIMARY KEY (group_id, member_id)
);
```

Cycle prevention is enforced at the application layer in `veloci-api` before publishing a job. The engine will error on cycle detection as a safeguard.

### review_queue

One record per engine-detected candidate entry awaiting user review.

```sql
CREATE TABLE review_queue (
  id                        UUID PRIMARY KEY,
  entity_id                 UUID NOT NULL REFERENCES entities(id),
  entry_id                  UUID NOT NULL REFERENCES entries(id),
  job_id                    UUID NOT NULL REFERENCES processing_jobs(id),
  suggested_name            TEXT NOT NULL,
  suggested_entry_type      TEXT NOT NULL,
  suggested_rate_per_day    NUMERIC(12,4) NOT NULL,
  matched_transaction_count INTEGER NOT NULL,
  confidence                NUMERIC(4,3) NOT NULL,
  sample_merchants          TEXT[] NOT NULL,           -- up to 5 examples
  status                    TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','modified')),
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
  node_id                UUID NOT NULL,        -- entry_id or group_id
  node_type              TEXT NOT NULL CHECK (node_type IN ('entry','group')),
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

## 12. Key Invariants

These invariants are structural constraints on the system, not implementation details. Violations are bugs.

1. **Engine never calls `NOW()`** — `computed_as_of` is always derived from `MAX(raw_transactions.date)`. The engine has no concept of the current date.

2. **`raw_transactions` is immutable** — the engine never UPDATE or DELETE rows in this table. All re-runs read the same source data.

3. **Stages 3–6 filter on `status = 'active'`** — entries in `pending_review` or `inactive` are invisible to all calculation stages.

4. **Snapshot writes are atomic** — all snapshots for a given job commit in one transaction or not at all.

5. **DAG is acyclic** — cycle check runs in the Go API before publishing. The engine errors on cycle detection as a defense-in-depth check.

6. **Deduplication is idempotent** — running Stage 0 twice on the same CSV produces the same `raw_transactions` row count the second time (zero new inserts).

7. **UUIDs are generated by the Go API only** — the Rust engine never generates UUIDs. It reads them from Postgres or from the RabbitMQ job message.

8. **`amount_cents` sign convention** — positive = inflow (income, credit), negative = outflow (expense, debit). Applied during Stage 0 normalization based on `institution_mappings.amount_sign_convention`.

---

## 13. RabbitMQ Job Message Shape

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

## 14. Out of Scope for This Spec

- Go API endpoints for importing, reviewing, and approving entries (covered in import pipeline spec)
- UI views: Pulse, Stack, Horizon (covered in UI spec)
- Transfer detection (two debits/credits that cancel at the budget level — deferred to v1.1)
- Debt account rate calculations (APR-adjusted payoff projections — deferred to v1.1)
- Projected-only entries (entries in the Projection lane with no transaction history — deferred to v1.1)
