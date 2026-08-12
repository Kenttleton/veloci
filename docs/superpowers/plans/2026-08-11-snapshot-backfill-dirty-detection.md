# Snapshot Backfill & Dirty Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the pipeline to generate full-history snapshots on first import and recompute only affected (entry, date) pairs on subsequent imports using per-entry dirty detection.

**Architecture:** A new `dirty.rs` module runs pre-flight queries between stages 1 and 3 to build a `DirtyContext` (per-entry dirty ranges + existing snapshot set). The day-crawl in `run_from_stage3` extends back to `crawl_start` (the earliest dirty date) and skips any `(entry, date)` pair that needs no recomputation. Stage 3 accepts `dirty_entry_ids: &[Uuid]` and scopes all DB queries to those entries only.

**Tech Stack:** Rust, sqlx, PostgreSQL, rayon

## Global Constraints

- All new DB queries use parameterized sqlx — no string interpolation
- Stage 6 is already a true UPSERT (`ON CONFLICT DO UPDATE`) — clean entries are untouched when not in stage 3 output; do not modify stage 6
- `entries.reprocess` and `account.analyze` bypass dirty detection entirely — full crawl from `history_start`
- `dirty_to` for any entry = `entry.end_date` (if set and ≤ `computed_as_of`) else `computed_as_of`
- `dirty_from` for Sources A+B = `MAX(snapshot_date)` for that entry, or `entry.start_date` if no snapshots exist
- `dirty_from` for Source C = always `entry.start_date`
- `is_dirty_for_date` must check `date >= entry.start_date` before any other logic

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Modify | `services/engine/src/pipeline/types.rs` | Add `superseded_entry_ids` to `Stage0Output`; add `new_entry_assignments` to `Stage1Output` |
| Modify | `services/engine/src/pipeline/stage0.rs` | Query `transaction_entry_assignments` before supersede DELETE |
| Modify | `services/engine/src/pipeline/stage1.rs` | Collect `(entry_id, txn_date)` from match results |
| **Create** | `services/engine/src/pipeline/dirty.rs` | `DirtyContext`, all pre-flight queries, `is_dirty_for_date`, unit tests |
| Modify | `services/engine/src/pipeline/mod.rs` | Declare `pub mod dirty`; refactor entry points; wire `DirtyContext` into day-crawl |
| Modify | `services/engine/src/pipeline/stage3.rs` | Accept `dirty_entry_ids: &[Uuid]`; scope all queries to those IDs |

---

### Task 1: Extend Stage0Output and Stage1Output

**Files:**
- Modify: `services/engine/src/pipeline/types.rs:159-174`

**Interfaces:**
- Produces: `Stage0Output::superseded_entry_ids: Vec<Uuid>` consumed by Task 5
- Produces: `Stage1Output::new_entry_assignments: Vec<(Uuid, NaiveDate)>` consumed by Task 5

- [ ] **Step 1: Write the failing test**

Add to the `tests` block at the bottom of `types.rs`:

```rust
#[test]
fn stage0_output_has_superseded_entry_ids() {
    use chrono::NaiveDate;
    let out = Stage0Output {
        computed_as_of:       NaiveDate::from_ymd_opt(2025, 12, 31).unwrap(),
        imported_count:       1,
        skipped_count:        0,
        superseded_entry_ids: vec![Uuid::nil()],
    };
    assert_eq!(out.superseded_entry_ids.len(), 1);
}

#[test]
fn stage1_output_has_new_entry_assignments() {
    let out = Stage1Output {
        total_assignments:     1,
        unmatched_tx_ids:      vec![],
        new_entry_assignments: vec![(Uuid::nil(), chrono::NaiveDate::from_ymd_opt(2025, 6, 1).unwrap())],
    };
    assert_eq!(out.new_entry_assignments.len(), 1);
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd services/engine && cargo test --lib pipeline::types 2>&1 | tail -20
```

Expected: compile error — `superseded_entry_ids` and `new_entry_assignments` fields don't exist yet.

- [ ] **Step 3: Add the fields to both structs**

In `types.rs`, replace the `Stage0Output` and `Stage1Output` struct definitions:

```rust
/// Output from Stage 0.
#[derive(Debug, Clone)]
pub struct Stage0Output {
    /// The `MAX(date)` from transactions — used as the flux window anchor.
    pub computed_as_of:       NaiveDate,
    pub imported_count:       u32,
    pub skipped_count:        u32,
    /// Entry IDs whose assignments were about to be removed by a supersede DELETE.
    /// Captured by Stage 0 before the DELETE executes.
    pub superseded_entry_ids: Vec<Uuid>,
}

/// Output from Stage 1.
#[derive(Debug, Clone)]
pub struct Stage1Output {
    pub total_assignments: u64,
    /// UUIDs of transactions that matched no entry — passed to Stage 2.
    pub unmatched_tx_ids:  Vec<Uuid>,
    /// (entry_id, txn_date) for every entry that received at least one assignment.
    /// Dates are for diagnostics only — dirty_from is computed from last snapshot, not tx date.
    pub new_entry_assignments: Vec<(Uuid, NaiveDate)>,
}
```

- [ ] **Step 4: Fix the construction sites that break**

The compiler will now error on:
- `stage0.rs`: `Stage0Output { computed_as_of, imported_count, skipped_count }` — add `superseded_entry_ids: Vec::new()`
- `stage1.rs`: `Stage1Output { total_assignments, unmatched_tx_ids }` — add `new_entry_assignments: Vec::new()`

Find and patch both files minimally to restore compilation (the real data collection comes in Tasks 2 and 3).

- [ ] **Step 5: Run tests to confirm they pass**

```bash
cd services/engine && cargo test --lib pipeline::types 2>&1 | tail -20
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/types.rs services/engine/src/pipeline/stage0.rs services/engine/src/pipeline/stage1.rs
git commit -m "feat(engine): extend Stage0Output/Stage1Output for dirty detection fields"
```

---

### Task 2: Stage 0 — Capture Superseded Entry IDs Before Deletion

**Files:**
- Modify: `services/engine/src/pipeline/stage0.rs:79-131` (the `run()` function)

**Interfaces:**
- Consumes: `classified: Vec<ClassifiedCandidate>` (available after `classify_candidates`)
- Produces: `Stage0Output::superseded_entry_ids` populated with DISTINCT entry IDs from `transaction_entry_assignments`

- [ ] **Step 1: Write the failing test**

Add to the tests block in `stage0.rs`. This tests the pure logic that extracts supersede IDs from classified candidates — no DB needed:

```rust
#[test]
fn supersede_ids_extracted_from_classified() {
    let id1 = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
    let id2 = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();

    let classified = vec![
        ClassifiedCandidate {
            candidate: Candidate {
                date:                NaiveDate::from_ymd_opt(2025, 1, 1).unwrap(),
                amount_cents:        -1000,
                imported_payee:      "Netflix".into(),
                merchant_normalized: "Netflix".into(),
                imported_id:         None,
            },
            action:            DedupAction::Supersede(id1),
            settlement_status: "flux",
        },
        ClassifiedCandidate {
            candidate: Candidate {
                date:                NaiveDate::from_ymd_opt(2025, 1, 2).unwrap(),
                amount_cents:        -500,
                imported_payee:      "Spotify".into(),
                merchant_normalized: "Spotify".into(),
                imported_id:         None,
            },
            action:            DedupAction::Insert,
            settlement_status: "settled",
        },
        ClassifiedCandidate {
            candidate: Candidate {
                date:                NaiveDate::from_ymd_opt(2025, 1, 3).unwrap(),
                amount_cents:        -200,
                imported_payee:      "Hulu".into(),
                merchant_normalized: "Hulu".into(),
                imported_id:         None,
            },
            action:            DedupAction::Supersede(id2),
            settlement_status: "flux",
        },
    ];

    let supersede_ids: Vec<Uuid> = classified
        .iter()
        .filter_map(|c| if let DedupAction::Supersede(id) = c.action { Some(id) } else { None })
        .collect();

    assert_eq!(supersede_ids.len(), 2);
    assert!(supersede_ids.contains(&id1));
    assert!(supersede_ids.contains(&id2));
}
```

- [ ] **Step 2: Run test to confirm it passes** (this is pure logic — it should pass immediately)

```bash
cd services/engine && cargo test --lib pipeline::stage0::tests::supersede_ids_extracted 2>&1 | tail -10
```

- [ ] **Step 3: Add the DB query to `stage0::run()` before `batch_insert`**

In `stage0::run()`, after `classify_candidates` and before `batch_insert`, add:

```rust
// Capture entry IDs assigned to transactions that are about to be superseded.
// Must happen BEFORE batch_insert deletes the old transaction rows (which cascades
// or causes FK violations on transaction_entry_assignments).
let supersede_tx_ids: Vec<Uuid> = classified
    .iter()
    .filter_map(|c| if let DedupAction::Supersede(id) = c.action { Some(id) } else { None })
    .collect();

let superseded_entry_ids: Vec<Uuid> = if !supersede_tx_ids.is_empty() {
    let rows: Vec<(Uuid,)> = sqlx::query_as(
        "SELECT DISTINCT entry_id FROM transaction_entry_assignments WHERE transaction_id = ANY($1)",
    )
    .bind(&supersede_tx_ids)
    .fetch_all(&pools.read)
    .await
    .context("failed to query entry assignments for superseded transactions")?;
    rows.into_iter().map(|(id,)| id).collect()
} else {
    Vec::new()
};
```

Then update the `Stage0Output` construction at the bottom of `run()`:

```rust
Ok(Stage0Output {
    computed_as_of,
    imported_count,
    skipped_count,
    superseded_entry_ids,
})
```

- [ ] **Step 4: Compile**

```bash
cd services/engine && cargo build 2>&1 | grep -E "error|warning: unused" | head -20
```

Expected: clean build. Fix any borrow/ownership issues if the compiler flags them.

- [ ] **Step 5: Run all stage0 tests**

```bash
cd services/engine && cargo test --lib pipeline::stage0 2>&1 | tail -15
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/stage0.rs
git commit -m "feat(engine): capture superseded entry IDs in Stage0 before DELETE"
```

---

### Task 3: Stage 1 — Collect New Entry Assignments

**Files:**
- Modify: `services/engine/src/pipeline/stage1.rs:441-645` (the `run()` function)

**Interfaces:**
- Consumes: `results: Vec<(Uuid, Vec<(Uuid, f64)>, bool)>` and `txns: Vec<TransactionRow>` (both in scope at end of `run()`)
- Produces: `Stage1Output::new_entry_assignments: Vec<(Uuid, NaiveDate)>` — deduplicated (entry_id, txn_date) pairs

- [ ] **Step 1: Write the failing test**

Add to `stage1.rs` tests block. This tests the pure collection logic:

```rust
#[test]
fn new_entry_assignments_collected_from_results() {
    use chrono::NaiveDate;
    use std::collections::HashMap;

    let txn_a = Uuid::parse_str("aaaaaaaa-0000-0000-0000-000000000000").unwrap();
    let txn_b = Uuid::parse_str("bbbbbbbb-0000-0000-0000-000000000000").unwrap();
    let entry_1 = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
    let entry_2 = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();

    let date_a = NaiveDate::from_ymd_opt(2025, 3, 15).unwrap();
    let date_b = NaiveDate::from_ymd_opt(2025, 6, 1).unwrap();

    let results: Vec<(Uuid, Vec<(Uuid, f64)>, bool)> = vec![
        (txn_a, vec![(entry_1, 1.0)], false),
        (txn_b, vec![(entry_1, 0.9), (entry_2, 1.0)], false),
    ];

    let txn_dates: HashMap<Uuid, NaiveDate> = [
        (txn_a, date_a),
        (txn_b, date_b),
    ].into_iter().collect();

    let mut seen = std::collections::HashSet::new();
    let assignments: Vec<(Uuid, NaiveDate)> = results.iter()
        .flat_map(|(txn_id, matched, _)| {
            let date = *txn_dates.get(txn_id).unwrap();
            matched.iter().map(move |(entry_id, _)| (*entry_id, date))
        })
        .filter(|pair| seen.insert(*pair))
        .collect();

    // entry_1 appears in txn_a and txn_b → two records (different dates)
    // entry_2 appears in txn_b → one record
    assert_eq!(assignments.len(), 3);
    assert!(assignments.contains(&(entry_1, date_a)));
    assert!(assignments.contains(&(entry_1, date_b)));
    assert!(assignments.contains(&(entry_2, date_b)));
}
```

- [ ] **Step 2: Run test to confirm it passes**

```bash
cd services/engine && cargo test --lib pipeline::stage1::tests::new_entry_assignments 2>&1 | tail -10
```

Expected: PASS (pure logic, no DB)

- [ ] **Step 3: Add collection to `stage1::run()` before the `Ok(Stage1Output {...})` return**

In `run()`, just before `Ok(Stage1Output { ... })`, add:

```rust
let txn_dates: std::collections::HashMap<Uuid, NaiveDate> =
    txns.iter().map(|t| (t.id, t.date)).collect();

let new_entry_assignments: Vec<(Uuid, NaiveDate)> = {
    let mut seen = std::collections::HashSet::new();
    results
        .iter()
        .flat_map(|(txn_id, matched_entries, _)| {
            let date = *txn_dates.get(txn_id)
                .unwrap_or(&NaiveDate::from_ymd_opt(1970, 1, 1).unwrap());
            matched_entries.iter().map(move |(entry_id, _)| (*entry_id, date))
        })
        .filter(|pair| seen.insert(*pair))
        .collect()
};
```

Then update the return statement:

```rust
Ok(Stage1Output {
    total_assignments,
    unmatched_tx_ids,
    new_entry_assignments,
})
```

- [ ] **Step 4: Compile**

```bash
cd services/engine && cargo build 2>&1 | grep "error" | head -20
```

- [ ] **Step 5: Run all stage1 tests**

```bash
cd services/engine && cargo test --lib pipeline::stage1 2>&1 | tail -15
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/stage1.rs
git commit -m "feat(engine): collect new_entry_assignments from Stage1 match results"
```

---

### Task 4: Create `dirty.rs` — Dirty Detection Module

**Files:**
- Create: `services/engine/src/pipeline/dirty.rs`

**Interfaces:**
- Produces: `DirtyDetectionInput { superseded_entry_ids, new_entry_assignments }` (consumed by Task 5)
- Produces: `DirtyEntry { id, start_date, end_date }` (used internally)
- Produces: `DirtyContext` with `from_import()`, `full_rerun()`, `dirty_entry_ids_for_date()`, `is_dirty_for_date()`
- Produces: `pub fn query_entries()` and `pub async fn query_history_start()` (called from mod.rs for bypass mode)

- [ ] **Step 1: Write the unit tests first (pure functions only — no DB)**

Create `services/engine/src/pipeline/dirty.rs` with just the test module to start:

```rust
//! Dirty detection for the snapshot backfill pipeline.
//!
//! Determines which (entry_id, snapshot_date) pairs need recomputation
//! based on three touch sources and gap-fill logic. See design spec §4–9.

use std::collections::{HashMap, HashSet};

use anyhow::{Context, Result};
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/// Minimal entry descriptor for dirty detection.
#[derive(Debug, Clone)]
pub struct DirtyEntry {
    pub id:         Uuid,
    pub start_date: NaiveDate,
    pub end_date:   Option<NaiveDate>,
}

/// Touch sources from stage 0 and stage 1, passed into dirty detection.
pub struct DirtyDetectionInput {
    /// Entry IDs whose old transaction assignments were about to be deleted
    /// by stage 0's supersede logic.
    pub superseded_entry_ids:  Vec<Uuid>,
    /// (entry_id, txn_date) for all entries that received assignments in stage 1.
    pub new_entry_assignments: Vec<(Uuid, NaiveDate)>,
}

/// Pre-flight query results and computed dirty ranges for a pipeline run.
pub struct DirtyContext {
    pub entries:            Vec<DirtyEntry>,
    /// entry_id → earliest date from which recomputation is needed.
    pub dirty_from:         HashMap<Uuid, NaiveDate>,
    /// entry_id → latest date through which snapshots should be computed.
    /// = entry.end_date (if set and ≤ computed_as_of), else computed_as_of.
    pub dirty_to:           HashMap<Uuid, NaiveDate>,
    /// Full set of existing (entry_id, snapshot_date) pairs — used for gap-fill.
    pub existing_snapshots: HashSet<(Uuid, NaiveDate)>,
    /// The earliest date the day-crawl must start from.
    pub crawl_start:        NaiveDate,
    /// MIN(transactions.date) for the entity.
    pub history_start:      NaiveDate,
    /// When true: all entries are dirty for all dates (bypass for reprocess/analyze jobs).
    pub bypass_mode:        bool,
}

impl DirtyContext {
    /// Build dirty context for an `import.process` run.
    ///
    /// Sources A+B: entries touched by new/superseded transactions.
    /// Source C: entries whose `updated_at` exceeds their last `computed_as_of`.
    /// Gap-fill: any (entry, date) pair with no existing snapshot row.
    pub async fn from_import(
        entity_id:      Uuid,
        computed_as_of: NaiveDate,
        flux_start:     NaiveDate,
        input:          &DirtyDetectionInput,
        pool:           &PgPool,
    ) -> Result<Self> {
        let entries          = query_entries(entity_id, pool).await?;
        let history_start    = query_history_start(entity_id, pool).await?;
        let last_snapshots   = query_last_snapshot_per_entry(entity_id, pool).await?;
        let existing_snapshots = query_existing_snapshots(entity_id, pool).await?;
        let source_c_ids     = query_source_c_entry_ids(entity_id, pool).await?;

        let entry_start: HashMap<Uuid, NaiveDate> =
            entries.iter().map(|e| (e.id, e.start_date)).collect();

        let mut dirty_from: HashMap<Uuid, NaiveDate> = HashMap::new();

        // Sources A + B — entries touched by new or superseded transactions.
        let touched: HashSet<Uuid> = input
            .new_entry_assignments
            .iter()
            .map(|(id, _)| *id)
            .chain(input.superseded_entry_ids.iter().copied())
            .collect();

        for entry_id in &touched {
            let df = last_snapshots
                .get(entry_id)
                .copied()
                .unwrap_or_else(|| entry_start.get(entry_id).copied().unwrap_or(history_start));
            merge_dirty_from(&mut dirty_from, *entry_id, df);
        }

        // Source C — entry config changed since last computed snapshot.
        for entry_id in &source_c_ids {
            let df = entry_start.get(entry_id).copied().unwrap_or(history_start);
            merge_dirty_from(&mut dirty_from, *entry_id, df);
        }

        let dirty_to        = compute_dirty_to(&entries, computed_as_of);
        let crawl_start     = compute_crawl_start(flux_start, history_start, &dirty_from, &existing_snapshots);

        Ok(Self {
            entries,
            dirty_from,
            dirty_to,
            existing_snapshots,
            crawl_start,
            history_start,
            bypass_mode: false,
        })
    }

    /// Build dirty context for a full re-run (bypass dirty detection).
    ///
    /// Used by `entries.reprocess` and `account.analyze` jobs. All entries are
    /// considered dirty for all dates from `history_start` to `computed_as_of`.
    pub fn full_rerun(
        entries:        Vec<DirtyEntry>,
        history_start:  NaiveDate,
        computed_as_of: NaiveDate,
    ) -> Self {
        let dirty_from: HashMap<Uuid, NaiveDate> = entries
            .iter()
            .map(|e| (e.id, e.start_date.min(history_start)))
            .collect();
        let dirty_to = compute_dirty_to(&entries, computed_as_of);
        Self {
            entries,
            dirty_from,
            dirty_to,
            existing_snapshots: HashSet::new(),
            crawl_start: history_start,
            history_start,
            bypass_mode: true,
        }
    }

    /// Returns `true` if `(entry_id, date)` needs snapshot recomputation.
    pub fn is_dirty_for_date(&self, entry_id: Uuid, date: NaiveDate, flux_start: NaiveDate) -> bool {
        // Upper-bound ceiling: never compute past end_date or computed_as_of.
        match self.dirty_to.get(&entry_id) {
            None          => return false,
            Some(&ceiling) if date > ceiling => return false,
            _ => {}
        }

        if self.bypass_mode {
            return true;
        }

        // Flux window: always recompute (settlement volatility).
        if date >= flux_start {
            return true;
        }

        // Touched entry (Sources A, B, C): dirty from the gap-start date forward.
        if let Some(&df) = self.dirty_from.get(&entry_id) {
            if date >= df {
                return true;
            }
        }

        // Gap fill: no existing snapshot for this (entry, date) pair.
        !self.existing_snapshots.contains(&(entry_id, date))
    }

    /// Returns all entry IDs that need snapshot recomputation for `date`.
    pub fn dirty_entry_ids_for_date(&self, date: NaiveDate, flux_start: NaiveDate) -> Vec<Uuid> {
        self.entries
            .iter()
            .filter(|e| date >= e.start_date && self.is_dirty_for_date(e.id, date, flux_start))
            .map(|e| e.id)
            .collect()
    }
}

// ---------------------------------------------------------------------------
// Pure helper functions (unit-testable without DB)
// ---------------------------------------------------------------------------

fn merge_dirty_from(map: &mut HashMap<Uuid, NaiveDate>, entry_id: Uuid, dirty_from: NaiveDate) {
    map.entry(entry_id)
        .and_modify(|d| *d = (*d).min(dirty_from))
        .or_insert(dirty_from);
}

fn compute_dirty_to(entries: &[DirtyEntry], computed_as_of: NaiveDate) -> HashMap<Uuid, NaiveDate> {
    entries
        .iter()
        .map(|e| {
            let ceiling = e
                .end_date
                .filter(|&d| d <= computed_as_of)
                .unwrap_or(computed_as_of);
            (e.id, ceiling)
        })
        .collect()
}

fn compute_crawl_start(
    flux_start:         NaiveDate,
    history_start:      NaiveDate,
    dirty_from:         &HashMap<Uuid, NaiveDate>,
    existing_snapshots: &HashSet<(Uuid, NaiveDate)>,
) -> NaiveDate {
    let min_dirty = dirty_from.values().copied().min();

    let min_existing = existing_snapshots.iter().map(|(_, d)| *d).min();
    let history_candidate = match min_existing {
        // No snapshots at all: must crawl from history_start.
        None => Some(history_start),
        // History predates earliest snapshot: fill the gap.
        Some(earliest) if history_start < earliest => Some(history_start),
        _ => None,
    };

    [Some(flux_start), min_dirty, history_candidate]
        .into_iter()
        .flatten()
        .min()
        .unwrap_or(flux_start)
}

// ---------------------------------------------------------------------------
// DB queries
// ---------------------------------------------------------------------------

/// Load all entries for an entity (including ended — dirty detection scopes by ceiling).
pub async fn query_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<DirtyEntry>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:         Uuid,
        start_date: NaiveDate,
        end_date:   Option<NaiveDate>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT id, start_date, end_date FROM entries WHERE entity_id = $1",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load entries for dirty detection")?;

    Ok(rows
        .into_iter()
        .map(|r| DirtyEntry { id: r.id, start_date: r.start_date, end_date: r.end_date })
        .collect())
}

/// Query MIN(transactions.date) for the entity — the left boundary of history.
pub async fn query_history_start(entity_id: Uuid, pool: &PgPool) -> Result<NaiveDate> {
    let row: (Option<NaiveDate>,) =
        sqlx::query_as("SELECT MIN(date) FROM transactions WHERE entity_id = $1")
            .bind(entity_id)
            .fetch_one(pool)
            .await
            .context("failed to query history_start")?;

    row.0.ok_or_else(|| anyhow::anyhow!("no transactions found for entity {entity_id}"))
}

/// Query the most recent snapshot_date per entry — used as dirty_from for Sources A+B.
async fn query_last_snapshot_per_entry(
    entity_id: Uuid,
    pool:      &PgPool,
) -> Result<HashMap<Uuid, NaiveDate>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:       Uuid,
        last_snapshot: NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT node_id, MAX(snapshot_date) AS last_snapshot
        FROM snapshots
        WHERE entity_id = $1 AND node_type = 'entry'
        GROUP BY node_id
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to query last snapshot per entry")?;

    Ok(rows.into_iter().map(|r| (r.node_id, r.last_snapshot)).collect())
}

/// Load all existing (entry_id, snapshot_date) pairs — used for gap-fill detection.
///
/// For households with 10-year histories and ~20 entries this is ~73 k rows ≈ 1.5 MB.
async fn query_existing_snapshots(
    entity_id: Uuid,
    pool:      &PgPool,
) -> Result<HashSet<(Uuid, NaiveDate)>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:       Uuid,
        snapshot_date: NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT node_id, snapshot_date FROM snapshots WHERE entity_id = $1 AND node_type = 'entry'",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load existing snapshots for gap-fill detection")?;

    Ok(rows.into_iter().map(|r| (r.node_id, r.snapshot_date)).collect())
}

/// Source C: entry IDs whose config changed after their last computed snapshot.
///
/// A NULL `max_computed_as_of` (no snapshots yet) counts as dirty.
async fn query_source_c_entry_ids(entity_id: Uuid, pool: &PgPool) -> Result<Vec<Uuid>> {
    let rows: Vec<(Uuid,)> = sqlx::query_as(
        r#"
        SELECT e.id
        FROM entries e
        LEFT JOIN (
            SELECT node_id, MAX(computed_as_of) AS max_coa
            FROM snapshots
            WHERE entity_id = $1 AND node_type = 'entry'
            GROUP BY node_id
        ) s ON s.node_id = e.id
        WHERE e.entity_id = $1
          AND (s.max_coa IS NULL OR e.updated_at > s.max_coa)
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to query Source C (entry metadata changes)")?;

    Ok(rows.into_iter().map(|(id,)| id).collect())
}

// ---------------------------------------------------------------------------
// Tests (pure — no DB)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn d(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    fn entry(id: Uuid, start: &str, end: Option<&str>) -> DirtyEntry {
        DirtyEntry {
            id,
            start_date: d(start),
            end_date:   end.map(d),
        }
    }

    #[test]
    fn merge_dirty_from_keeps_earliest() {
        let mut map = HashMap::new();
        let id = Uuid::nil();
        merge_dirty_from(&mut map, id, d("2025-06-01"));
        merge_dirty_from(&mut map, id, d("2025-03-01")); // earlier — wins
        merge_dirty_from(&mut map, id, d("2025-09-01")); // later — ignored
        assert_eq!(map[&id], d("2025-03-01"));
    }

    #[test]
    fn compute_dirty_to_live_entry_returns_computed_as_of() {
        let id = Uuid::nil();
        let entries = vec![entry(id, "2024-01-01", None)];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], coa);
    }

    #[test]
    fn compute_dirty_to_ended_entry_before_coa_returns_end_date() {
        let id = Uuid::nil();
        let end = "2025-06-30";
        let entries = vec![entry(id, "2024-01-01", Some(end))];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], d(end));
    }

    #[test]
    fn compute_dirty_to_end_date_after_coa_returns_coa() {
        let id = Uuid::nil();
        let entries = vec![entry(id, "2024-01-01", Some("2030-01-01"))];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], coa);
    }

    #[test]
    fn crawl_start_no_snapshots_returns_history_start() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let dirty_from    = HashMap::new();
        let existing      = HashSet::new();
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, history_start);
    }

    #[test]
    fn crawl_start_with_dirty_entry_earlier_than_flux() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let id            = Uuid::nil();
        let mut dirty_from = HashMap::new();
        dirty_from.insert(id, d("2025-06-01")); // earlier than flux, later than history
        // Existing snapshot on 2025-01-01 — history_start not before existing min.
        let mut existing = HashSet::new();
        existing.insert((id, d("2025-01-01")));
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, d("2025-06-01"));
    }

    #[test]
    fn crawl_start_history_before_earliest_snapshot_triggers_gap_fill() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let id            = Uuid::nil();
        let dirty_from    = HashMap::new();
        // Earliest existing snapshot is March — history precedes it.
        let mut existing  = HashSet::new();
        existing.insert((id, d("2025-03-01")));
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, history_start);
    }

    #[test]
    fn is_dirty_before_entry_start_returns_false() {
        let id  = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-06-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        // date < entry.start_date → dirty_entry_ids_for_date returns empty
        let ids = ctx.dirty_entry_ids_for_date(d("2025-05-31"), d("2025-12-01"));
        assert!(ids.is_empty());
    }

    #[test]
    fn is_dirty_past_ceiling_returns_false() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2024-01-01", Some("2025-06-30"))],
            dirty_from:         [(id, d("2024-01-01"))].into_iter().collect(),
            dirty_to:           [(id, d("2025-06-30"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2024-01-01"),
            history_start:      d("2024-01-01"),
            bypass_mode:        false,
        };
        assert!(!ctx.is_dirty_for_date(id, d("2025-07-01"), d("2025-12-01")));
    }

    #[test]
    fn is_dirty_gap_fill_for_missing_snapshot() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-01-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            // snapshot_date 2025-03-01 is NOT in existing_snapshots → gap fill
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        let flux_start = d("2025-12-01");
        assert!(ctx.is_dirty_for_date(id, d("2025-03-01"), flux_start));
    }

    #[test]
    fn is_dirty_existing_snapshot_not_in_dirty_range_returns_false() {
        let id = Uuid::nil();
        let date = d("2025-03-01");
        let mut existing = HashSet::new();
        existing.insert((id, date)); // snapshot exists
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-01-01", None)],
            dirty_from:         HashMap::new(),  // entry not touched
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: existing,
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        let flux_start = d("2025-12-01"); // date is before flux_start
        assert!(!ctx.is_dirty_for_date(id, date, flux_start));
    }

    #[test]
    fn bypass_mode_returns_true_within_ceiling() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2024-01-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2024-01-01"),
            history_start:      d("2024-01-01"),
            bypass_mode:        true,
        };
        assert!(ctx.is_dirty_for_date(id, d("2024-06-15"), d("2025-12-01")));
    }

    #[test]
    fn dirty_entry_ids_for_date_returns_only_dirty() {
        let id_a = Uuid::parse_str("aaaaaaaa-0000-0000-0000-000000000000").unwrap();
        let id_b = Uuid::parse_str("bbbbbbbb-0000-0000-0000-000000000000").unwrap();
        let date  = d("2025-06-01");
        let flux_start = d("2025-12-01");

        let mut existing = HashSet::new();
        existing.insert((id_a, date)); // id_a already has snapshot for this date
        // id_b has no snapshot → gap fill

        let ctx = DirtyContext {
            entries:            vec![
                entry(id_a, "2025-01-01", None),
                entry(id_b, "2025-01-01", None),
            ],
            dirty_from:         HashMap::new(),
            dirty_to:           [
                (id_a, d("2025-12-31")),
                (id_b, d("2025-12-31")),
            ].into_iter().collect(),
            existing_snapshots: existing,
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };

        let ids = ctx.dirty_entry_ids_for_date(date, flux_start);
        assert_eq!(ids.len(), 1);
        assert!(ids.contains(&id_b));
    }
}
```

- [ ] **Step 2: Run the tests — they should all pass (pure functions, no DB)**

```bash
cd services/engine && cargo test --lib pipeline::dirty 2>&1 | tail -25
```

Expected: all PASS. Fix any typos (the `cao`/`coa` typo on line ~`let map = compute_dirty_to(&entries, cao)` — correct to `coa`).

- [ ] **Step 3: Add `pub mod dirty;` to mod.rs**

In `services/engine/src/pipeline/mod.rs`, add after the other `pub mod` lines:

```rust
pub mod dirty;
```

- [ ] **Step 4: Compile the full crate**

```bash
cd services/engine && cargo build 2>&1 | grep "error" | head -20
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/dirty.rs services/engine/src/pipeline/mod.rs
git commit -m "feat(engine): add dirty detection module with DirtyContext and pre-flight queries"
```

---

### Task 5: Wire Dirty Detection into `mod.rs`

**Files:**
- Modify: `services/engine/src/pipeline/mod.rs`

**Interfaces:**
- Consumes: `dirty::DirtyDetectionInput`, `dirty::DirtyContext`, `Stage0Output::superseded_entry_ids`, `Stage1Output::new_entry_assignments`
- Produces: Updated `run_from_stage3` signature; `dirty_entry_ids: Vec<Uuid>` passed to `stage3::run` each iteration

- [ ] **Step 1: Write a compile-only test to verify the call chain**

In `mod.rs` tests block (add one if absent), add a compile-time type check:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    // Verify DirtyDetectionInput is accessible from this module.
    fn _check_dirty_input_type(_: dirty::DirtyDetectionInput) {}
}
```

- [ ] **Step 2: Refactor `run_import` — inline stages 1+2, build DirtyDetectionInput**

Replace the existing `run_import` body:

```rust
pub async fn run_import(
    entity_id: Uuid,
    job_id: Uuid,
    pending_import_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, %pending_import_id, "import.process starting");

    let stage0_out = stage0::run(entity_id, job_id, pending_import_id, pools).await?;
    tracing::info!(%entity_id, imported = stage0_out.imported_count, skipped = stage0_out.skipped_count, computed_as_of = %stage0_out.computed_as_of, "stage 0 complete");

    if stage0_out.imported_count == 0 {
        tracing::info!(%entity_id, "stage 0 imported nothing new — skipping stages 1–7");
        return Ok(());
    }

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, unmatched = stage1_out.unmatched_tx_ids.len(), "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");

    let dirty_input = dirty::DirtyDetectionInput {
        superseded_entry_ids:  stage0_out.superseded_entry_ids,
        new_entry_assignments: stage1_out.new_entry_assignments,
    };

    run_from_stage3(entity_id, job_id, stage0_out.computed_as_of, Some(dirty_input), pools).await
}
```

- [ ] **Step 3: Refactor `run_entries_reprocess` — inline stages 1+2, pass None for dirty_input**

```rust
pub async fn run_entries_reprocess(
    entity_id: Uuid,
    job_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, "entries.reprocess starting");

    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");

    // Bypass dirty detection — full re-run from history_start.
    run_from_stage3(entity_id, job_id, computed_as_of, None, pools).await
}
```

- [ ] **Step 4: Delete `run_from_stage1` (now inlined)**

Remove the private `run_from_stage1` function entirely.

- [ ] **Step 5: Update `run_account_analyze` to pass `None`**

```rust
pub async fn run_account_analyze(
    entity_id: Uuid,
    job_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, "account.analyze starting");
    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;
    run_from_stage3(entity_id, job_id, computed_as_of, None, pools).await
}
```

- [ ] **Step 6: Rewrite `run_from_stage3` with dirty detection**

Replace the existing `run_from_stage3` body:

```rust
async fn run_from_stage3(
    entity_id:      Uuid,
    job_id:         Uuid,
    computed_as_of: chrono::NaiveDate,
    dirty_input:    Option<dirty::DirtyDetectionInput>,
    pools:          &Pools,
) -> Result<()> {
    use crate::pipeline::types::SettlementConfig;

    let settlement_cfg = SettlementConfig::query(entity_id, &pools.read).await?;
    let flux_start = computed_as_of
        - chrono::Duration::days(i64::from(settlement_cfg.settlement_window_days));

    // Build dirty context — either from import touch sources or full bypass.
    let dirty_ctx = match dirty_input {
        Some(ref input) => {
            dirty::DirtyContext::from_import(
                entity_id,
                computed_as_of,
                flux_start,
                input,
                &pools.read,
            )
            .await?
        }
        None => {
            let entries       = dirty::query_entries(entity_id, &pools.read).await?;
            let history_start = dirty::query_history_start(entity_id, &pools.read).await?;
            dirty::DirtyContext::full_rerun(entries, history_start, computed_as_of)
        }
    };

    tracing::info!(
        %entity_id,
        crawl_start    = %dirty_ctx.crawl_start,
        %computed_as_of,
        bypass         = dirty_ctx.bypass_mode,
        "beginning day-crawl"
    );

    let mut snapshot_date = dirty_ctx.crawl_start;
    while snapshot_date <= computed_as_of {
        let dirty_entry_ids = dirty_ctx.dirty_entry_ids_for_date(snapshot_date, flux_start);

        if dirty_entry_ids.is_empty() {
            snapshot_date += chrono::Duration::days(1);
            continue;
        }

        let stage3_out =
            stage3::run(entity_id, snapshot_date, &dirty_entry_ids, &pools.read).await?;

        let stage4_out =
            stage4::run(entity_id, &stage3_out.entry_rates, &pools.read).await?;

        let stage5_out = stage5::run(
            entity_id,
            snapshot_date,
            computed_as_of,
            &stage3_out,
            &stage4_out,
            &pools.read,
        )
        .await?;

        stage6::run(
            entity_id,
            job_id,
            snapshot_date,
            computed_as_of,
            &stage3_out,
            &stage4_out,
            &stage5_out,
            &pools.write,
        )
        .await?;

        snapshot_date += chrono::Duration::days(1);
    }

    tracing::info!(%entity_id, "day-crawl complete");

    // Sync projected_rate_per_day back to system entries from their latest snapshot.
    sqlx::query(
        r#"
        UPDATE entries e
        SET projected_rate_per_day = s.projected_rate_per_day
        FROM (
            SELECT DISTINCT ON (node_id) node_id, projected_rate_per_day
            FROM snapshots
            WHERE entity_id = $1 AND node_type = 'entry'
            ORDER BY node_id, snapshot_date DESC
        ) s
        WHERE e.id = s.node_id
          AND e.entity_id = $1
          AND e.source = 'system'
          AND e.status = 'live'
        "#,
    )
    .bind(entity_id)
    .execute(&pools.write)
    .await
    .context("failed to sync projected_rate for system entries")?;

    run_stage7(entity_id, job_id, computed_as_of, pools).await
}
```

- [ ] **Step 7: Compile**

```bash
cd services/engine && cargo build 2>&1 | grep "error" | head -30
```

The compiler will error on `stage3::run` because it now expects `dirty_entry_ids`. That is intentional — Task 6 fixes it. If there are other errors, fix them now.

- [ ] **Step 8: Commit (even if stage3 compile error remains — commit what compiles)**

If the only error is the stage3 signature mismatch, commit the mod.rs changes with a note:

```bash
git add services/engine/src/pipeline/mod.rs
git commit -m "feat(engine): wire dirty detection into pipeline orchestration (stage3 update pending)"
```

---

### Task 6: Update Stage 3 to Filter by Dirty Entry IDs

**Files:**
- Modify: `services/engine/src/pipeline/stage3.rs`

**Interfaces:**
- Consumes: `dirty_entry_ids: &[Uuid]` from `mod.rs`
- Produces: `Stage3Output` containing only rates for dirty entries

- [ ] **Step 1: Write a unit test for the new signature shape**

Add to `stage3.rs` tests:

```rust
#[test]
fn empty_dirty_entry_ids_produces_empty_rates() {
    // compute_entry_rate is pure — verify an entry with no txns returns zero rate.
    let entry = ActiveEntry {
        id:                     Uuid::nil(),
        label_id:               None,
        direction:              "spend".into(),
        entry_type:             "standing".into(),
        source:                 "user".into(),
        period_days:            Some(30),
        rate_method:            "median".into(),
        projected_rate_per_day: None,
        start_date:             NaiveDate::from_ymd_opt(2025, 1, 1).unwrap(),
    };
    let rate = compute_entry_rate(&entry, &[], NaiveDate::from_ymd_opt(2025, 6, 1).unwrap(), None, 90);
    assert_eq!(rate.transaction_count, 0);
    assert_eq!(rate.actual_rate_per_day, 0.0);
}
```

- [ ] **Step 2: Run existing stage3 tests to confirm baseline**

```bash
cd services/engine && cargo test --lib pipeline::stage3 2>&1 | tail -15
```

Expected: all PASS

- [ ] **Step 3: Update `run()` signature**

Change:

```rust
pub async fn run(
    entity_id: Uuid,
    snapshot_date: NaiveDate,
    pool: &PgPool,
) -> Result<Stage3Output>
```

To:

```rust
pub async fn run(
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Stage3Output>
```

- [ ] **Step 4: Update `run()` body to use dirty_entry_ids**

Replace the two loader calls inside `run()`:

```rust
let entries = load_entries_by_ids(entity_id, dirty_entry_ids, pool).await?;
let txns    = load_assigned_txns(entity_id, snapshot_date, dirty_entry_ids, pool).await?;
let prior_rates = load_prior_snapshot_rates(entity_id, snapshot_date, dirty_entry_ids, pool).await?;
```

- [ ] **Step 5: Replace `load_active_entries` with `load_entries_by_ids`**

Add a new loader (keep `load_active_entries` if it was used elsewhere — it isn't, so replace it):

```rust
async fn load_entries_by_ids(
    entity_id:       Uuid,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<ActiveEntry>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

    #[derive(sqlx::FromRow)]
    struct Row {
        id:                     Uuid,
        label_id:               Option<Uuid>,
        direction:              String,
        entry_type:             String,
        source:                 String,
        period_days:            Option<i32>,
        rate_method:            String,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        start_date:             NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, direction, entry_type, source, period_days,
               rate_method, projected_rate_per_day, start_date
        FROM entries
        WHERE entity_id = $1
          AND id = ANY($2)
        "#,
    )
    .bind(entity_id)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load dirty entries for stage 3")?;

    Ok(rows.into_iter().map(|r| ActiveEntry {
        id:                     r.id,
        label_id:               r.label_id,
        direction:              r.direction,
        entry_type:             r.entry_type,
        source:                 r.source,
        period_days:            r.period_days,
        rate_method:            r.rate_method,
        projected_rate_per_day: r.projected_rate_per_day
            .and_then(|v| v.to_string().parse::<f64>().ok()),
        start_date:             r.start_date,
    }).collect())
}
```

- [ ] **Step 6: Update `load_assigned_txns` to filter by dirty_entry_ids**

Replace the `load_assigned_txns` signature and query:

```rust
async fn load_assigned_txns(
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<AssignedTxn>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

    #[derive(sqlx::FromRow)]
    struct Row {
        entry_id:     Uuid,
        txn_date:     NaiveDate,
        amount_cents: i64,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT tea.entry_id, t.date AS txn_date, t.amount_cents
        FROM transaction_entry_assignments tea
        JOIN transactions t ON t.id = tea.transaction_id
        JOIN entries e ON e.id = tea.entry_id
        WHERE t.entity_id = $1
          AND tea.entry_id = ANY($3)
          AND t.date <= $2
          AND t.date >= e.start_date
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load assigned transactions for stage 3")?;

    Ok(rows.into_iter().map(|r| AssignedTxn {
        entry_id:     r.entry_id,
        txn_date:     r.txn_date,
        amount_cents: r.amount_cents,
    }).collect())
}
```

- [ ] **Step 7: Update `load_prior_snapshot_rates` to scope to dirty entries**

Replace the function:

```rust
async fn load_prior_snapshot_rates(
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<(Uuid, f64)>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:             Uuid,
        actual_rate_per_day: sqlx::types::BigDecimal,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT DISTINCT ON (node_id)
          node_id,
          actual_rate_per_day
        FROM snapshots
        WHERE entity_id = $1
          AND node_type = 'entry'
          AND node_id = ANY($3)
          AND snapshot_date < $2
        ORDER BY node_id, snapshot_date DESC
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load prior snapshot rates for stage 3")?;

    Ok(rows.into_iter().map(|r| {
        let rate = r.actual_rate_per_day.to_string().parse::<f64>().unwrap_or(0.0);
        (r.node_id, rate)
    }).collect())
}
```

- [ ] **Step 8: Remove `load_active_entries` (now replaced)**

Delete the old `load_active_entries` function entirely.

- [ ] **Step 9: Full compile**

```bash
cd services/engine && cargo build 2>&1 | grep "error" | head -20
```

Expected: clean build.

- [ ] **Step 10: Run all tests**

```bash
cd services/engine && cargo test --lib 2>&1 | tail -30
```

Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add services/engine/src/pipeline/stage3.rs services/engine/src/pipeline/mod.rs
git commit -m "feat(engine): scope Stage3 queries to dirty_entry_ids; extend crawl to history_start"
```

---

### Task 7: Smoke Test — Verify End-to-End Behavior

**Files:**
- No new files — manual verification against the running dev environment

**Goal:** Confirm that importing `transactions_2025.csv` (spanning 2025-01-01 → 2025-12-31) produces snapshots from January through December, and that a second import recomputes only the flux window + changed entries.

- [ ] **Step 1: Reset the test entity to a clean state**

Drop existing snapshots for the test entity (check your dev DB):

```sql
DELETE FROM snapshots WHERE entity_id = '<your-test-entity-id>';
DELETE FROM transactions WHERE entity_id = '<your-test-entity-id>';
```

- [ ] **Step 2: Import `transactions_2025.csv`**

Trigger an `import.process` job as you normally would (UI or direct job enqueue). Watch the engine logs for:

```
beginning day-crawl crawl_start=2025-01-01 computed_as_of=2025-12-31 bypass=false
```

The crawl_start should be `2025-01-01` (history_start, since no snapshots exist).

- [ ] **Step 3: Verify snapshot coverage**

```sql
SELECT MIN(snapshot_date), MAX(snapshot_date), COUNT(DISTINCT snapshot_date)
FROM snapshots
WHERE entity_id = '<your-test-entity-id>'
  AND node_type = 'entry';
```

Expected:
- `MIN` = 2025-01-01 (or entry.start_date if later)
- `MAX` = 2025-12-31
- `COUNT` ≈ 365

- [ ] **Step 4: Verify Budget and Reports pages**

Open the Budget and Reports pages in the browser. Confirm that history from January through December is visible, not just December.

- [ ] **Step 5: Re-import the same CSV — verify only flux window recomputes**

Import the CSV a second time (simulating a routine monthly upload). Watch the logs. The crawl_start should be much later (approximately `computed_as_of - settlement_window_days`), and most days should be skipped.

```
beginning day-crawl crawl_start=2025-12-17 computed_as_of=2025-12-31 bypass=false
```

Confirm day-crawl executes only ~14 days (the flux window), not 365.

- [ ] **Step 6: Commit if any fixes were needed during smoke test**

```bash
git add -p  # stage only the relevant fixes
git commit -m "fix(engine): <describe what was fixed during smoke test>"
```

---

## Self-Review

**Spec coverage check:**

| Spec section | Covered by |
|---|---|
| §4 Source A (new tx assignment) | Task 3 (stage1 new_entry_assignments) + Task 5 (DirtyDetectionInput) |
| §4 Source B (superseded) | Task 2 (stage0 captures entry IDs before DELETE) |
| §4 Source C (entry metadata change) | Task 4 (query_source_c_entry_ids in dirty.rs) |
| §5 dirty_from = last_snapshot or start_date | Task 4 (from_import logic) |
| §5 dirty_to = end_date or computed_as_of | Task 4 (compute_dirty_to) |
| §6 Entry ceilings | Task 4 (is_dirty_for_date ceiling check) |
| §7 Pre-flight queries | Task 4 (all five queries in dirty.rs) |
| §8 Crawl range | Task 4 (compute_crawl_start) + Task 5 (crawl_start used in day-crawl) |
| §9 Per-day dirty set + skip | Task 5 (dirty_entry_ids_for_date + `if empty { continue }`) |
| §10 Stage output changes | Tasks 1–3 |
| §11 Stage 3 filter | Task 6 |
| §11 Stage 6 UPSERT (no change) | Confirmed — already ON CONFLICT DO UPDATE |
| §12 bypass for reprocess/analyze | Task 5 (None → full_rerun) |
| §13 First import (no snapshots) | Covered — existing_snapshots is empty → all gap-fill → crawl_start = history_start |
| §13 New account edge case | Covered — new txns → Source A → dirty_from = entry.start_date (no prior snapshots) |
| §13 Ended entry complete | Covered — dirty_to ceiling + no gap-fill if snapshots exist |

**Placeholder scan:** None found.

**Type consistency:** `dirty_entry_ids: &[Uuid]` flows from mod.rs Task 5 to stage3::run Task 6. `DirtyDetectionInput` defined once in dirty.rs, used in mod.rs. `DirtyEntry` defined once in dirty.rs — not re-defined in mod.rs.
