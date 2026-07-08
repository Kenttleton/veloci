# Veloci — Processing Engine & Data Model Design

**Date:** 2026-06-29
**Status:** Approved — data model revised 2026-06-30 by veloci-api spec
**Scope:** Rust processing engine pipeline, financial data model, review queue flow, import deduplication algorithm

> **Data model supersession:** `entries` and `entry_rules` are replaced by `rules` (defined in `2026-06-30-veloci-api-design.md`). `groups` and `group_members` are replaced by `labels`. `label_members` and `label_rules` are removed — the label hierarchy is now expressed through `rules.label_id` (one output label per rule) and post-stage JSONB conditions that reference label UUIDs as inputs. `transaction_entry_assignments` is renamed `transaction_rule_assignments`. `computed_snapshots.node_type` values change from `entry|group` to `rule|label`. `entities` lives in `veloci_app` — FK constraints on `entity_id` are valid throughout.

---

## 1. Overview

The Veloci processing engine is a pure function: given (raw_transactions, rules, labels), it produces computed_snapshots. It never calls `NOW()`, never generates UUIDs, and never owns state — all state lives in Postgres. The Go API orchestrates job dispatch; the Rust engine owns all computation.

Two Postgres tables are the source of truth:

| Table | Role |
| --- | --- |
| `raw_transactions` | Immutable, normalized transaction records. Written once at import time. Never modified. |
| `computed_snapshots` | Rebuildable calculations. Written by the engine after every analysis run. Safe to drop and recompute. |

This separation means rule changes can be applied retroactively with zero re-upload: the engine re-reads `raw_transactions`, applies the new rules, and overwrites `computed_snapshots`.

---

## 2. The 7-Stage Linear Pipeline

Every job type runs a contiguous suffix of these seven stages. No branching, no parallel tracks.

```text
Stage 0  CSV normalization + deduplication     →  raw_transactions
Stage 1  Rule matching (pre/post, boolean)    →  transaction_rule_assignments
Stage 2  Pattern detection (unmatched only)   →  rules (status: pending_review)
Stage 3  Rate computation                     →  per-rule rates   [active only]
Stage 4  Label rate mapping                   →  per-label rates  [active only]
Stage 5  Slope + drift + rolling range        →  trends           [active only]
Stage 6  Snapshot write (batch INSERT)        →  computed_snapshots
Stage 7  Cash flow projection                 →  rate_projections
```

### Job Types and Entry Points

| Job Type | Stages Run | Trigger |
| --- | --- | --- |
| `import.process` | 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 | New CSV uploaded |
| `rules.reprocess` | 1 → 2 → 3 → 4 → 5 → 6 → 7 | Rule created, modified, or deleted |
| `account.analyze` | 3 → 4 → 5 → 6 → 7 | Rule approved from review queue; manual recalculate |
| `balance.project` | 7 | Account balance updated manually |

`import.process` is the only job that writes to `raw_transactions`. All other jobs read from it.

---

## 3. Review Queue Gate

New rules detected in Stage 2 start in `status: pending_review`. Stages 3–6 filter exclusively on `status = 'active'`. Until the user approves a pending rule, it does not contribute to any rate, slope, or snapshot calculation.

```text
Stage 2 detects pattern → rule created with status: pending_review
User sees review queue in UI → rule shown with:
  - suggested name
  - suggested entry_type
  - suggested match conditions (transparent + editable before approving)
  - sample merchants that triggered it
  - calculated rate preview (what it would be if approved)

User approves → rule.status = 'active'
                → rule_epochs row created: epoch_start = first_matched_tx_date
                → job published: account.analyze
                → engine runs stages 3–6
                → Pulse view updates with new Margin impact

User rejects  → rule.project_tentatively = FALSE (removes from Stage 7 immediately)
                → rule.status = 'inactive'
```

Stage 2 sets `project_tentatively = TRUE` on any `pending_review` rule that has both `next_due_date` and `recurrence_anchor` populated — enough schedule data to produce a meaningful projection. This allows the pending rule to appear in the Stage 7 forecast before the user approves it. No `rule_epochs` row is created for pending rules.

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
| --- | --- | --- |
| **Settled** | `candidate.date < T - settlement_window_days` | Final and authoritative. No further changes expected. |
| **Flux** | `candidate.date >= T - settlement_window_days` | Pending or recently posted. Date or amount may still differ across exports. |
| **New** | `candidate.date > existing_boundary + dedup_window_days` | Beyond all previously imported data. Cannot be a duplicate. |

`settlement_window_days`, `dedup_window_days`, and `amount_tolerance_pct` are read from the `institution_mappings` record for this import.

`existing_boundary` = `MAX(date_range_end)` across all prior `import_batches` for this account (NULL on first import).

### Effective Settlement Status of Existing Rows

Before checking each candidate against the database, the engine computes the effective settlement status of any existing row it finds:

```text
effective_status =
  if existing.settlement_status = 'settled'                              → settled
  if existing.settlement_status = 'flux'
     AND NOW() - existing.imported_at > settlement_window_days           → effectively settled (aged)
  if existing.settlement_status = 'flux'
     AND NOW() - existing.imported_at <= settlement_window_days          → young flux (supersedeable)
```

Aged flux rows are treated identically to settled rows for dedup purposes — they represent transactions that have had sufficient time to resolve without a newer import superseding them.

> **Determinism note:** `NOW()` appears only in this effective-status check, which is part of the import utility (Stage 0), not the financial calculation stages (3–5). Stages 3–6 include all rows regardless of settlement status and never branch on this field.

### Deduplication Passes

Passes run in order. A candidate matched in an earlier pass is not re-evaluated in later passes.

#### Pass 1 — Exact imported_id match *(primary path when bank provides IDs)*

- Only runs when `candidate.imported_id` is non-null.
- Query: find any `raw_transaction` in the same account where `imported_id = candidate.imported_id`.
- If found and effective_status = settled or aged flux → **skip** (genuine duplicate).
- If found and effective_status = young flux → **supersede**: delete old row (cascades `transaction_rule_assignments`), insert candidate.

#### Pass 2 — New territory check

- If `candidate.date > existing_boundary + dedup_window_days` → **insert directly**. No existing data can overlap this date range.

#### Pass 3 — Volatility-aware exact merchant match

- Query: find any `raw_transaction` in the same account where:
  - `merchant_normalized = candidate.merchant_normalized` (exact match)
  - `ABS(date - candidate.date) <= dedup_window_days`
  - `ABS(amount_cents - candidate.amount_cents) <= candidate.amount_cents * amount_tolerance_pct`
- If found and effective_status = settled or aged flux → **skip**.
- If found and effective_status = young flux → **supersede**.

#### Pass 4 — Volatility-aware fuzzy merchant match

LCS ratio is computed in Rust, not in SQL. The SQL fetch retrieves date+amount-bounded candidates; Rust filters by LCS ratio. No `pg_trgm` or database extension is required.

**SQL fetch:**

```sql
SELECT id, merchant_normalized, amount_cents, settlement_status, imported_at
FROM raw_transactions
WHERE account_id = $account_id
  AND date BETWEEN $candidate_date - $dedup_window_days
                AND $candidate_date + $dedup_window_days
  AND ABS(amount_cents - $candidate_amount) <= $candidate_amount * $amount_tolerance_pct
```

**Rust filter (on the fetched set):**

```rust
let match = candidates.iter().find(|existing| {
    lcs_ratio(&existing.merchant_normalized, &candidate.merchant_normalized) >= 0.70
});
```

`lcs_ratio(a, b) = lcs_length(a, b) / max(a.len(), b.len())` — computed in Rust, no DB round-trip per row.

- If `match` found and effective_status = settled or aged flux → **skip**.
- If `match` found and effective_status = young flux → **supersede**.

#### Pass 5 — Fallback insert

- If no pass matched: insert as new `raw_transaction`.

### Setting settlement_status at Insert Time

When inserting a new row (Pass 2 fallback, Pass 5), `settlement_status` is determined once and never changed:

```text
settlement_status =
  if candidate.date < T - settlement_window_days → 'settled'
  else                                           → 'flux'
```

### Dedup Query Scope

All dedup queries are bounded to `date BETWEEN candidate.date - dedup_window_days AND candidate.date + dedup_window_days`. This prevents unbounded full-table scans on large transaction histories.

### Stage 0 Concurrency

Candidate rows are processed concurrently using `buffer_unordered(import_concurrency)` from `futures::StreamExt`. `import_concurrency` is read from `engine.pipeline.import_concurrency` in `veloci.toml` (default: 4). This caps the number of simultaneous dedup lookups to prevent exhausting the write connection pool.

All Stage 0 DB access — dedup reads and the final batch INSERT — uses the write pool (`engine.pool.write_max`). The dedup check and insert share the same connection context to keep the import transactionally consistent. `import_concurrency` should not exceed `engine.pool.write_max`.

The final batch INSERT is always sequential and runs after all candidates are classified — no partial writes are possible.

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
| --- | --- |
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

### Pre-compilation Pass

Before the parallel matching loop, all rules are compiled into `CompiledRule` structs. This happens once on the main thread:

```rust
struct CompiledRule {
    rule_id:    Uuid,
    stage:      Stage,
    priority:   i32,
    conditions: CompiledConditionTree,
}

enum CompiledConditionTree {
    And(Vec<CompiledConditionTree>),
    Or(Vec<CompiledConditionTree>),
    Not(Box<CompiledConditionTree>),
    Xor(Vec<CompiledConditionTree>),
    PayeeExact(String),
    PayeeContains(String),
    PayeeRegex(regex::Regex),   // compiled once here; Regex is Send + Sync
    PayeeOneOf(Vec<String>),
    AmountRange { min: Option<i64>, max: Option<i64> },
    DateDayOfMonth { day: u8, tolerance_days: u8 },
    DateRange { start: NaiveDate, end: NaiveDate },
    AccountId(Uuid),
}

let compiled_rules: Vec<CompiledRule> = rules
    .iter()
    .map(CompiledRule::try_from)   // fails fast on malformed JSONB or invalid regex
    .collect::<Result<_, _>>()?;
```

`regex::Regex` is `Send + Sync` — compiled patterns are shared across rayon worker threads with no cloning or locking. A malformed regex in a rule's JSONB causes a compile-time error for that rule (logged and skipped); it does not abort the job.

### Matching Algorithm

1. Sort `compiled_rules` by `stage ASC, priority ASC` (done once after pre-compilation).
2. `par_iter` over transactions; for each transaction evaluate all compiled rules in order.
3. Evaluate each rule's compiled condition tree recursively — no regex compilation at this point.
4. A transaction may match more than one rule. **This is intentional, not a bug.** Rules exist to apply labels — a Netflix charge legitimately belongs to both a "Netflix" rule (specific) and a "Streaming Subscriptions" rule (broad). Both assignments are correct.
5. Each match produces one `transaction_rule_assignments` row with the `rule_id` and a `confidence` of 1.0.
6. Unmatched transactions pass through to Stage 2.

### Rate Overlap is Intentional

Because a transaction can match multiple rules, the same transaction contributes to multiple rules' rates in Stage 3. This is by design:

- **Netflix rule** `actual_rate` reflects only Netflix charges.
- **Streaming rule** `actual_rate` reflects all streaming charges — including Netflix.

Both rates are independently correct at the rule level. **Label rates (Stage 4) are the authoritative user-facing view** — each rule has exactly one output label, so each transaction contributes to a label's rate exactly once regardless of how many rules matched it.

A derived metric available at the API/UI layer: a rule's share of its parent label's budget is `rule.actual_rate / parent_label.actual_rate`. The engine produces both values in `computed_snapshots`; the percentage is a simple division the API can compute on read.

---

## 6. Stage 2: Pattern Detection

**Input:** Transactions from Stage 1 that produced no assignments

**Output:** Candidate `rules` with `status: pending_review`, `source: engine`; linked `review_queue` records

### Clustering Algorithm

The engine clusters unmatched transactions into candidate rules using three signals:

1. **Merchant similarity** — transactions with `merchant_normalized` values sharing ≥70% LCS ratio are grouped together. This catches "AMZ*PRIME", "AMAZON PRIME", "AMZN/BILL" as one cluster.

2. **Amount regularity** — within a merchant cluster, the engine checks whether amounts are consistent (within ±2% of the cluster median). Consistent amounts suggest a `standing` entry; variable amounts suggest `variable`.

3. **Timing regularity** — the engine computes inter-transaction intervals within a cluster. Near-constant intervals (±5 days variance) suggest `standing`. Irregular intervals with consistent amounts suggest `hit`.

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

**Input:** `transaction_rule_assignments` joined to `rules` WHERE `rules.status = 'active'`; current open `rule_epochs` (epoch_start, epoch_end) for each rule

**Output:** Per-rule `{actual_rate_per_day, projected_rate_per_day, window_days_used, transaction_count}`

### Data Horizon

Every rate computation is scoped to the rule's current signal epoch. Only transactions where `date >= epoch_start` are included:

```sql
SELECT tra.* FROM transaction_rule_assignments tra
JOIN raw_transactions rt ON rt.id = tra.transaction_id
JOIN rule_epochs re ON re.rule_id = tra.rule_id
  AND re.epoch_end IS NULL
  AND re.epoch_start <= rt.date
WHERE tra.rule_id = $1
  AND rt.date >= re.epoch_start
```

This is the data horizon: transactions predating the current epoch (e.g. the prior Netflix subscription) are excluded from live rate computation. They remain in `raw_transactions` (immutable) and in prior `computed_snapshots` (available to Horizon chart via `epoch_id`).

### Signal Expiry

The engine auto-terminates signals that have gone quiet for `EPOCH_TERMINATION_MULTIPLIER * period_days` days. This constant is defined once in the engine codebase and used everywhere the check runs — never as a magic number.

```rust
const EPOCH_TERMINATION_MULTIPLIER: u32 = 3;
```

```text
staleness = snapshot_date - next_due_date

staleness < period_days                              → normal; use computed actual_rate
staleness in [period_days, MULTIPLIER*period_days)   → warning: actual_rate = 0, projected_rate persists
staleness >= MULTIPLIER * period_days                → auto-terminate: write epoch_end (terminated_by='auto')
                                                       actual_rate = 0, projected_rate = 0
```

For `hit` and `boost` rules, expiry uses amortization completion rather than the 3-strike mechanism:

```text
snapshot_date >= last_tx_date + period_days  →  actual_rate = 0, projected_rate = 0
```

Hit/Boost amortization is a single window — no recurring epochs.

### The `period_days` Field

`period_days` carries different semantics depending on entry type:

| Entry type | `period_days` meaning |
| --- | --- |
| `standing` | Expected recurrence cycle — how often this commitment comes due. With 2+ transactions the engine uses the detected median interval; with 1 transaction `period_days` is authoritative. |
| `variable` | Expected recurrence cycle — same as standing. Amount varies; timing does not. |
| `hit` | Amortization window — how many days to spread this one-time impact. |
| `boost` | Amortization window — same as hit, positive direction. |

> **`single` entry type is deprecated and removed.** Hit and Boost cover all single-transaction cases.

### Rate Computation by Entry Type

#### Standing

```text
actual_rate = amount_cents / period_days
```

Represents the savings-reservation rate: "I need to set aside X cents/day so I have enough when this commitment comes due." With 2+ matched transactions, the engine uses the detected median inter-transaction interval instead of the configured `period_days`.

#### Variable

Actual rate is always amortized — the same formula as all other types. `variable_method` does not affect actuals; it only affects the projected rate. The line is expected to fluctuate across snapshots; that variance is the signal.

```text
// Actual — amortized total over the period window. Same for avg and max methods.
actual_rate = rolling_window_total_cents / period_days

// rolling_window_total_cents = SUM(matched amount_cents)
//   WHERE date IN [snapshot_date - period_days, snapshot_date]
// Stored in computed_snapshots. Represents the true dollar flow in the current window.
```

Projected rate uses `variable_method` to pick a representative amount from a recent history window, then amortizes it:

```text
projection_lookback_start = MAX(snapshot_date - (3 * period_days), epoch_start)

projected_rate (avg) = MEAN(matched amounts in [projection_lookback_start, snapshot_date]) / period_days
projected_rate (max) = MAX(matched amounts  in [projection_lookback_start, snapshot_date]) / period_days
```

`3 * period_days` gives three full cycles of recent history — enough to capture seasonal variation for monthly bills while staying recent enough to reflect current reality. The `epoch_start` floor prevents the window from reaching across a signal lifecycle boundary into data that belongs to a prior signal instance. If the epoch is newer than `3 * period_days`, the window narrows to what the current epoch has.

`avg` projects the mean observed amount forward — balanced planning. `max` projects the worst recent occurrence forward — conservative planning. Both apply to income and expense directions.

#### Hit

```text
actual_rate = amount_cents / period_days
```

A one-time expense treated as a short-term debt. `period_days` is the amortization window — how long this hit reduces the margin. A $150 car repair with `period_days = 30` registers as $5.00/day until the window closes.

#### Boost

```text
actual_rate = amount_cents / period_days
```

The positive mirror of Hit — a one-time income event (bonus, tax refund) that temporarily lifts the margin for `period_days` days.

#### Projected Rate

If the rule has a user-set `projected_rate_per_day`, use it directly.
If no projected rate is set, the engine uses the `actual_rate` from the most recent prior `computed_snapshot` for that rule as the projection baseline. For brand-new rules (no prior snapshot), `projected_rate = actual_rate` (drift = 0 on first appearance).

### Adaptive Window Width

For rules with fewer transactions than the window would normally expect, the window narrows to the actual data span. This prevents a rule with one transaction from being treated as a near-zero rate across a 365-day window. The `window_days_used` column records the actual window applied.

---

## 8. Stage 4: Label Rate Mapping

**Input:** Per-rule rates from Stage 3; `rules.label_id`

**Output:** Per-label `{actual_rate_per_day, projected_rate_per_day, contributing_rule_count}`

### Model

Each rule has exactly one output label (`rules.label_id`). The label hierarchy is built implicitly through pre/post rule staging:

- **Pre-stage rules** match transaction attributes (merchant, amount, date pattern) and output a leaf label. Example: a Netflix rule with `period_days = 30`, `dom:7` outputs the "Netflix LLC" label.
- **Post-stage rules** match label conditions in their JSONB and output an aggregate label. Example: a Streaming rule with `period_days = 30`, `dom:-1` has conditions referencing the Netflix LLC, Disney+, and Hulu label UUIDs; it outputs the "Streaming" label.

Stage 1 already runs pre-stage rules before post-stage rules. By the time Stage 4 runs, `transaction_rule_assignments` contains the full rule-to-transaction mapping at every level of the hierarchy. A Netflix transaction is assigned to:

1. Netflix LLC rule (pre-stage, fires first) → "Netflix LLC" label
2. Streaming rule (post-stage) → "Streaming" label
3. Commitments rule (post-stage) → "Commitments" label

Each transaction is assigned to each rule once — no path duplication. No set-union deduplication is needed.

### Aggregation

Stage 4 is a flat mapping: for each active label, read the rate of the rule whose `label_id` matches:

```text
label_rate = rate of the rule WHERE rules.label_id = $label_id AND status = 'active'
```

`contributing_rule_count` is the count of transactions assigned to that rule in Stage 3.

### Cycle Detection

A cycle occurs when label A is an input to a rule that outputs label B, and label B is an input to a rule that outputs label A. The Go API validates acyclicity by traversing rule conditions before saving any rule. The engine errors on cycle detection as a defense-in-depth check.

---

## 9. Stage 5: Slope + Drift + Rolling Range

**Input:** Current per-node rates from Stages 3 and 4; prior `computed_snapshots` bulk-loaded for all nodes (variable window per node)

**Output:** Per-node `{drift_per_day, slope_per_day, r_squared}`

### Drift

Drift direction is determined by the `direction` of the rule(s) reachable through the label hierarchy. The sign convention ensures **positive drift always means financially ahead of projection**.

```text
if ALL reachable rules have direction = 'expense':
    drift_per_day = projected_rate_per_day - actual_rate_per_day
    // positive = spent less than projected = ahead
else (ANY income rule reachable — short-circuit on first found):
    drift_per_day = actual_rate_per_day - projected_rate_per_day
    // positive = earned more than projected = ahead
```

For a rule node: read `rule.direction` directly.
For a label node: read the direction of its defining rule (`rules WHERE label_id = $label_id`). For aggregate labels whose defining rule's conditions reference sub-labels, traverse those sub-label rules recursively until a `direction` is found — short-circuit on the first income rule. The Margin label's hierarchy contains income rules, so it always hits the income branch and correctly produces positive drift = net ahead of projection.

### Slope (Rate of Change)

The slope measures how fast the actual rate is changing over time. It is a linear regression over each node's variable regression window (see Regression Window Selection below).

```text
inputs:   [(snapshot.computed_as_of - first_snapshot.computed_as_of, actual_rate_per_day), ...]
outputs:  slope_per_day   — regression coefficient (units: $/day per day — a rate of rate change)
          r_squared       — goodness of fit (0.0–1.0 confidence in the trend line)
```

Minimum 2 data points required. With 0 or 1 prior snapshot: `slope_per_day = 0.0`, `r_squared = 0.0`.

### Rolling Range (Candlestick OHLC)

The engine does **not** pre-compute or store high/low range values. Because `computed_snapshots` is a daily series (one row per calendar day per node), the API computes OHLC for any candlestick period as a window aggregate over `actual_rate_per_day` at query time:

- **Open**: `actual_rate_per_day` of the earliest snapshot in the period
- **Close**: `actual_rate_per_day` of the latest snapshot in the period
- **High**: `MAX(actual_rate_per_day)` over all snapshots in the period
- **Low**: `MIN(actual_rate_per_day)` over all snapshots in the period

This is a single indexed range scan on `(entity_id, node_id, snapshot_date DESC)` — no joins, no pre-computation. The API supports chunked/lazy-load queries over this index (see API spec). Removing the stored columns keeps the snapshot row self-describing: it represents the state on one calendar day and nothing else.

### Regression Window Selection

Each node's regression window is `3 * period_days`, sourced from the defining rule:

| Node type | `period_days` source | Regression window |
| --- | --- | --- |
| Rule | `rule.period_days` | `3 * period_days` |
| Label | `rules WHERE label_id = $label_id AND status = 'active'` | `3 * period_days` |

Every label has exactly one defining rule. That rule's `period_days` defines the label's regression window regardless of how many sub-rules or sub-labels it aggregates. The three major system labels (Income, Commitments, Margin) each have a defining rule with `period_days = 30`, giving them a 90-day window.

### Bulk Load Before par_iter

Stage 5 runs as a `rayon::par_iter` over all active nodes. Before the parallel loop, all snapshot history is loaded in a single query:

```sql
SELECT node_id, node_type, snapshot_date, actual_rate_per_day, epoch_id
FROM computed_snapshots
WHERE entity_id = $entity_id
  AND node_id = ANY($all_node_ids)
  AND snapshot_date >= $snapshot_date - $max_window_days
ORDER BY node_id, snapshot_date ASC
```

`$max_window_days` is the maximum `3 * period_days` across all nodes. Results are grouped into a `HashMap<Uuid, Vec<SnapshotRow>>` in Rust before `par_iter` begins. No database access occurs inside the parallel loop.

### Snapshot-Epoch Relationship and Regression Scoping

Each snapshot row represents one calendar day. The `epoch_id` column records which epoch was open on that calendar day — snapshots contain epoch context, epochs do not own snapshots. A snapshot on March 4th carries the epoch that was open that day (e.g. the Netflix epoch closing on March 4th); the March 5th snapshot for the same rule carries a different epoch_id (a new epoch) or no snapshot exists at all if no epoch was open.

For **rule nodes**, Stage 5 must not regress across epoch boundaries — a prior epoch's snapshots reflect different data scope and corrupt the slope. After grouping the bulk-loaded rows, Rust filters each rule's history to the current epoch and its regression window:

```rust
let history: Vec<&SnapshotRow> = node_history
    .iter()
    .filter(|r| r.epoch_id == Some(current_epoch_id))
    .filter(|r| r.snapshot_date >= snapshot_date - window_days)
    .collect();
```

For **label nodes**, `epoch_id IS NULL` on all their snapshots. Labels carry no independent epoch lifecycle — their historical rates already reflect which member rules had open epochs on each past calendar day (Stage 4 only mapped active-epoch rules to labels when those snapshots were written). Only the date window filter applies:

```rust
let history: Vec<&SnapshotRow> = node_history
    .iter()
    .filter(|r| r.snapshot_date >= snapshot_date - window_days)
    .collect();
```

### snapshot_date vs computed_as_of

Each snapshot row carries two date fields with distinct purposes:

- **`snapshot_date`** — the calendar day this snapshot represents. Row identity key. The UI queries by this field to retrieve a specific point on the Horizon timeline.
- **`computed_as_of`** — `MAX(date) FROM raw_transactions WHERE entity_id = $entity_id`, computed at import time. The import's data horizon. Used by Stage 3 signal expiry and Stage 7 as the projection anchor. Denormalized onto each snapshot row so Stage 5 regression can read the settlement boundary of historical rows without joining back through import_batches.

These are different things. An import uploaded on March 15 whose transactions extend through March 10 produces snapshot rows for every day in the flux window, each with `computed_as_of = March 10` and a distinct `snapshot_date`.

The engine never calls `NOW()`. `computed_as_of` is always derived from `MAX(raw_transactions.date)`.

### Flux Window Day-Crawl

Stages 3–6 run as a day-loop over the import's flux window — not once per import. The flux window is:

```text
flux_start    = computed_as_of - settlement_window_days
flux_end      = computed_as_of

for each snapshot_date D in [flux_start .. flux_end]:
    Stage 3: compute per-rule rates using transactions WHERE rt.date <= D
    Stage 4: aggregate via DAG
    Stage 5: compute slope/drift using prior snapshots WHERE snapshot_date <= D
    Stage 6: UPSERT computed_snapshots row for (entity_id, node_id, D)
```

Days where `snapshot_date < flux_start` have only settled transactions — those snapshot rows are frozen and not recomputed. A new import only touches the rows its flux window covers.

---

## 10. Stage 6: Snapshot Write

**Input:** All per-node computed values from Stages 3–5

**Output:** Rows in `computed_snapshots`

The engine writes all snapshots for this run in a single Postgres transaction. Partial writes are not possible — either all snapshots commit together or none do.

```sql
INSERT INTO computed_snapshots (
  entity_id, node_id, node_type, snapshot_date, computed_as_of, job_id,
  actual_rate_per_day, projected_rate_per_day, drift_per_day,
  slope_per_day, r_squared,
  transaction_count, window_days_used, rolling_window_total_cents, balance_cents, epoch_id
)
VALUES ...
ON CONFLICT (entity_id, node_id, snapshot_date) DO UPDATE SET
  computed_as_of                = EXCLUDED.computed_as_of,
  job_id                        = EXCLUDED.job_id,
  actual_rate_per_day           = EXCLUDED.actual_rate_per_day,
  projected_rate_per_day        = EXCLUDED.projected_rate_per_day,
  drift_per_day                 = EXCLUDED.drift_per_day,
  slope_per_day                 = EXCLUDED.slope_per_day,
  r_squared                     = EXCLUDED.r_squared,
  transaction_count             = EXCLUDED.transaction_count,
  window_days_used              = EXCLUDED.window_days_used,
  rolling_window_total_cents    = EXCLUDED.rolling_window_total_cents,
  balance_cents                 = EXCLUDED.balance_cents,
  epoch_id                      = EXCLUDED.epoch_id;
```

The UPSERT covers the flux window. Rows outside the flux window (`snapshot_date < flux_start`) are untouched — they were written by a prior import and their transactions are fully settled.

---

## 11. Stage 7: Signal Superposition Projection

**Input:** Eligible rules (see eligibility table below) with `period_days`, `recurrence_anchor`, and `next_due_date`; `accounts.balance_cents` as starting point for derived balance; `computed_as_of` from Stage 6

**Output:** Rows in `rate_projections` — a forward-looking 90-day signal superposition per account (and entity aggregate)

### Rule Eligibility for Projection

Stage 7 uses two axes — `status` and epoch state — to determine which rules contribute to the projection:

| `status` | Epoch state | `project_tentatively` | Stage 7 |
| --- | --- | --- | --- |
| `active` | open epoch (`epoch_end IS NULL`) | — | **Include** — live signal |
| `active` | terminated or no epoch | — | **Exclude** — signal absent from projection |
| `pending_review` | no epoch | `TRUE` | **Include** — tentative signal |
| `pending_review` | no epoch | `FALSE` | **Exclude** |
| `inactive` | any | — | **Exclude** always |

A terminated epoch signals absence — the cancelled Netflix subscription disappears from the projection timeline, which is the correct representation. Projecting it as $0 would inflate `rate_projections` row counts unboundedly as signals accumulate.

```sql
SELECT r.*
FROM rules r
LEFT JOIN rule_epochs re ON re.rule_id = r.id AND re.epoch_end IS NULL
WHERE r.entity_id = $1
  AND r.status IN ('active', 'pending_review')
  AND (
    (r.status = 'active'         AND re.id IS NOT NULL)
    OR
    (r.status = 'pending_review' AND r.project_tentatively = TRUE)
  )
```

The entity-aggregate row in `rate_projections` (`account_id IS NULL`) is the sum of all rules that passed this eligibility check for each day — no separate eligibility pass.

### Purpose

Rate comparison tells you whether you are accumulating or falling behind on average. Stage 7 answers the timing question: do the signals have the right phase alignment? Even if income rate > commitments rate overall, a commitment signal firing before enough income signal has accumulated creates a gap.

- "Will I make rent on the 1st if my paycheck lands on the 15th?"
- "My bar tab last night — does that rate spike push margin negative before next payday?"

### Projection Algorithm

Each rule contributes a rate signal active on day D when D falls within a scheduled activation window: `[fire_date, fire_date + period_days)`. The engine expands `recurrence_anchor` into fire dates for the full 90-day window using `next_due_date` as the starting phase point.

```text
balance = accounts.balance_cents   // starting point for derived balance only

for each day D in [computed_as_of .. computed_as_of + 90]:

    income_rate_D      = Σ { r.amount_cents / r.period_days
                             for income rules r whose window covers D }

    commitment_rate_D  = Σ { r.amount_cents / r.period_days
                             for commitment rules r whose window covers D }

    margin_rate_D      = income_rate_D - commitment_rate_D

    balance            = balance + margin_rate_D   // derived running total

    write rate_projections row:
      income_rate_per_day     = income_rate_D
      commitment_rate_per_day = commitment_rate_D
      margin_rate_per_day     = margin_rate_D
      projected_balance_cents = balance            // secondary; for bank account comparison
      is_pinch_point          = (margin_rate_D < 0)

// Advance next_due_date for rules that fired during the window.
// Staged write AFTER projection completes — not in-loop — to preserve determinism.
for each rule r with recurrence_anchor:
    r.next_due_date = last_fire_date(r, window) + r.period_days
```

Pinch points are rate-native: `is_pinch_point = TRUE` when commitment signals exceed income signals at that phase offset — not when the derived balance crosses a threshold.

### Determinism

Stage 7 uses `computed_as_of` (the import's data horizon, `MAX(raw_transactions.date)`) as its "today" anchor — never `NOW()`. `next_due_date` is a stored field. Running the same job twice produces identical `rate_projections` rows.

### Variable Rule Projection

For `variable` rules, Stage 7 uses `actual_rate_per_day` from the most recent `computed_snapshot` as the amplitude estimate. This is a best-current-estimate; rows are rebuilt on every job run.

### Stage 7 in the Pipeline

Stage 7 commits its `rate_projections` rows in the same Postgres transaction as the Stage 6 snapshot write — projections are always consistent with the snapshots that produced them.

---

## 12. Entity Isolation

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
  entry_type             TEXT NOT NULL CHECK (entry_type IN ('standing','hit','boost','variable')),
  period_days            INTEGER NOT NULL DEFAULT 30,
  variable_method        TEXT CHECK (variable_method IN ('avg','max')),
  projected_rate_per_day NUMERIC(12,4),
  conditions             JSONB NOT NULL,
  -- label_id: the one label this rule outputs when it fires.
  -- Pre-stage rules output leaf labels; post-stage rules output aggregate labels.
  -- Conditions may reference other label UUIDs as inputs (composability).
  label_id               UUID REFERENCES labels(id) ON DELETE SET NULL,
  stage                  TEXT NOT NULL DEFAULT 'pre' CHECK (stage IN ('pre','post')),
  priority               INTEGER NOT NULL DEFAULT 100,
  status                 TEXT NOT NULL DEFAULT 'pending_review'
                         CHECK (status IN ('pending_review','active','inactive')),
  source                 TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user','engine')),
  recurrence_anchor      TEXT,
  next_due_date          DATE,
  project_tentatively    BOOLEAN NOT NULL DEFAULT FALSE,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON rules (entity_id, status);
CREATE INDEX ON rules (entity_id, priority);
CREATE INDEX ON rules (entity_id, label_id);
CREATE INDEX ON rules (entity_id, next_due_date);
```

### rule_epochs

Signal lifecycle records. One row per active instance of a rule's signal. Append-only — epoch_start is never modified. Reactivation (e.g. Netflix restart) writes a new row; prior epoch is preserved with its epoch_end for historical chart queries.

```sql
CREATE TABLE rule_epochs (
  id                      UUID        PRIMARY KEY,
  entity_id               UUID        NOT NULL REFERENCES entities(id),
  rule_id                 UUID        NOT NULL REFERENCES rules(id),
  epoch_start             DATE        NOT NULL,   -- Stage 3 data horizon: transactions WHERE date >= epoch_start
  epoch_end               DATE,                   -- NULL = signal currently live
  epoch_transaction_count INTEGER     NOT NULL DEFAULT 0,
  terminated_by           TEXT        CHECK (terminated_by IN ('auto', 'manual')),
  created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (rule_id, epoch_start)
);

CREATE INDEX ON rule_epochs (rule_id, epoch_end);
CREATE INDEX ON rule_epochs (entity_id, epoch_end);
```

**Condition tree shape (JSONB):**

```json
// Node:  { "op": "AND"|"OR"|"NOT"|"XOR", "children": [Node|Leaf, ...] }
// Leaf:  { "type": "<leaf_type>", "value": <string|number|array|{min,max}> }
// Label leaf (post-stage rules only):  { "type": "label", "label_id": "<uuid>" }
```

`NOT` and `XOR` nodes require exactly 1 and exactly 2 children respectively. `AND` / `OR` accept 1 or more. Label leaf nodes reference a label by UUID — names are never embedded in JSONB so renames require no JSONB migration.

### transaction_rule_assignments

Many-to-many join between transactions and rules. A transaction legitimately matches multiple rules across pre/post stages — each rule outputs a distinct label.

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

Named user-facing groupings. Replaces `groups`. Full definition in `2026-06-30-veloci-api-design.md`.

Each label is the output of exactly one rule (`rules.label_id → labels.id`). The label hierarchy (leaf → aggregate) is expressed through post-stage rule conditions referencing other label UUIDs — no separate membership table.

Name is freely mutable; all downstream references (rules, snapshots, projections) use `labels.id`.

```sql
CREATE TABLE labels (
  id         UUID PRIMARY KEY,
  entity_id  UUID NOT NULL REFERENCES entities(id),
  name       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (entity_id, name)
);
```

Cycle prevention (label A → rule → label B → rule → label A) is enforced by `veloci-api` before saving any rule. The engine errors on cycle detection as a defense-in-depth check.

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

Rebuildable output from the engine. One row per calendar day per node. Safe to truncate and recompute at any time.

```sql
CREATE TABLE computed_snapshots (
  id                     UUID          PRIMARY KEY,
  entity_id              UUID          NOT NULL REFERENCES entities(id),
  node_id                UUID          NOT NULL,   -- rule_id or label_id
  node_type              TEXT          NOT NULL CHECK (node_type IN ('rule','label')),
  snapshot_date          DATE          NOT NULL,   -- calendar day this row represents
  computed_as_of         DATE          NOT NULL,   -- MAX(raw_transactions.date) from import run; projection anchor
  job_id                 UUID          NOT NULL REFERENCES processing_jobs(id),
  actual_rate_per_day    NUMERIC(12,4) NOT NULL,
  projected_rate_per_day NUMERIC(12,4) NOT NULL,
  drift_per_day          NUMERIC(12,4) NOT NULL,
  slope_per_day          NUMERIC(14,6) NOT NULL,   -- $/day per day
  r_squared              NUMERIC(4,3)  NOT NULL,
  transaction_count             INTEGER       NOT NULL,
  window_days_used              INTEGER       NOT NULL,
  -- SUM(matched amount_cents WHERE date IN [snapshot_date - period_days, snapshot_date]).
  -- The raw dollar flow in the actual window. Basis for actual_rate_per_day on all rule types.
  -- For variable rules, also used with variable_method to compute projected_rate_per_day
  -- over the 3*period_days projection lookback window.
  rolling_window_total_cents    BIGINT        NOT NULL DEFAULT 0,
  balance_cents                 BIGINT        NOT NULL DEFAULT 0,
  epoch_id                      UUID          REFERENCES rule_epochs(id),   -- NULL for label nodes

  UNIQUE (entity_id, node_id, snapshot_date)
);

CREATE INDEX ON computed_snapshots (entity_id, node_id, snapshot_date DESC);
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

1. **Engine never calls `NOW()`** — `computed_as_of` (the import data horizon) is always derived from `MAX(raw_transactions.date)`; `snapshot_date` is a calendar day derived from the flux window crawl. Neither field is wall-clock time.

2. **`raw_transactions` is immutable** — the engine never UPDATE or DELETE rows in this table. All re-runs read the same source data.

3. **Stages 3–6 filter on `status = 'active'` with open epoch** — rules in `pending_review` or `inactive` are invisible to historical calculation stages. Stage 7 additionally projects `pending_review` rules where `project_tentatively = TRUE`; active rules with no open epoch are excluded from Stage 7.

4. **Snapshot writes are atomic** — all snapshots for a given job commit in one transaction or not at all.

5. **Label hierarchy is acyclic** — the Go API validates that no rule's conditions (directly or transitively) reference a label that is also downstream of this rule. The engine errors on cycle detection as a defense-in-depth check.

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
| 0 — CSV norm + dedup | Normalize all rows in parallel; dedup lookups concurrently up to `import_concurrency`; batch INSERT sequentially at end | `rayon::par_iter` for normalization; `buffer_unordered(import_concurrency)` for concurrent Postgres dedup reads |
| 1 — Rule matching | Pre-compile all rules into `CompiledRule` structs (single-threaded, once); then evaluate each transaction independently | Sequential pre-compilation → `rayon::par_iter` over transaction slice |
| 2 — Pattern detection | Global clustering pass is sequential (must see all unmatched at once); cluster scoring is parallel | Sequential LCS clustering → `rayon::par_iter` over candidate clusters for confidence scoring |
| 3 — Rate computation | Each rule's rate computed from its own transactions only; fully independent | `rayon::par_iter` over active rules |
| 4 — Label rate mapping | Each label's rate reads only its own rule's Stage 3 output; fully independent | `rayon::par_iter` over all active labels |
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

- **Stage ordering** — Stages 3 → 4 → 5 → 6 are a strict dependency chain. Stage 4 (label rate mapping) cannot start until all per-rule rates from Stage 3 are complete.
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
- Transfer detection — not required. Cross-account transfers (e.g. a CC payment from checking) do not double-count because active accounts (checking/savings) and passive accounts (credit, investment, loan) are separate budget contexts with separate label hierarchies. The CC payment appears as a Commitments entry in the main budget; individual CC charges appear in the CC's own label structure. No shared label ancestry means no summing conflict.
- Interest calculations (high-yield savings account yield, CC interest charges, investment returns) — deferred to v1.1. Interest will be modeled as income/expense rules once the rate computation for time-varying balances is spec'd.
- Debt account rate calculations (APR-adjusted payoff projections — deferred to v1.1)
- Projected-only rules (rules in the Projection lane with no transaction history — deferred to v1.1)
