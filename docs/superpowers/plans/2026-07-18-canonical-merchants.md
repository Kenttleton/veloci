# Canonical Merchants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global canonical merchant layer that maps normalized merchant strings to a single identity, feeds that identity into the entry conditions system via a new `canonical_merchant` condition type, and exposes full CRUD management in the configuration page.

**Architecture:** Two new global DB tables (`canonical_merchants`, `canonical_merchant_aliases`) drive a new `canonical_merchant` JSONB condition type evaluated by Stage 1. Stage 0 resolves canonical identity after dedup (exact alias lookup → LCS auto-create). Stage 2 replaces its O(n²) LCS clustering with canonical grouping. The Go web layer exposes 9 API endpoints and a new config page tab.

**Tech Stack:** Rust (sqlx, uuid, rayon, futures), Go (pgx/v5, huma/v2, chi/v5), Templ (Go templates), Postgres 15+

## Global Constraints

- Canonical merchants are **global** (no `entity_id`) — same as labels
- No FK added to `transactions` — canonical identity lives in the conditions system
- Dedup passes in Stage 0 are unchanged — canonical resolution runs **after** dedup classification, only for Insert + Supersede candidates
- Merge and split operations must queue an `entries.reprocess` job (`job_type = "rules.reprocess"`)
- `extract_brand()` in stage2.rs is removed entirely
- New Stage 2 entries use `{"type": "canonical_merchant", "canonical_merchant_id": "uuid"}` conditions, not `imported_payee_contains`
- Existing entries with `imported_payee_contains` conditions continue to work — no migration of existing entry conditions

---

## File Map

**New files:**
- `migrations/app/003_canonical_merchants.sql` — DB schema
- `services/web/store/canonical_merchants.go` — Go store: CRUD + merge + split
- `services/web/handler/canonical_merchants.go` — Go handler: 9 API endpoints

**Modified files:**
- `services/engine/src/pipeline/stage1.rs` — new `CanonicalMerchant` condition type + evaluation
- `services/engine/src/pipeline/stage0.rs` — canonical resolution after dedup
- `services/engine/src/pipeline/stage2.rs` — replace LCS clustering with canonical grouping; remove `extract_brand()`
- `services/web/page/handler.go` — load canonical merchants in `Configuration()`; update `ConfigurationData`
- `services/web/page/configuration.templ` — new Merchants tab
- `services/web/main.go` — register canonical merchants routes

---

## Task 1: DB Migration

**Files:**
- Create: `migrations/app/003_canonical_merchants.sql`

**Interfaces:**
- Produces: `canonical_merchants(id, name, source, created_at, updated_at)` table; `canonical_merchant_aliases(normalized_name, canonical_merchant_id, source, created_at)` table

- [ ] **Step 1: Write the migration file**

```sql
-- migrations/app/003_canonical_merchants.sql
-- Global canonical merchant registry. Maps normalized merchant strings to a
-- single canonical identity used by the entry conditions system.
-- Global like labels — no entity_id.

CREATE TABLE canonical_merchants (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL,
    source     TEXT        NOT NULL DEFAULT 'engine'
                           CHECK (source IN ('engine', 'user')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (name)
);

-- Normalized merchant strings that map to a canonical merchant.
-- normalized_name is the PRIMARY KEY — one string maps to exactly one canonical.
CREATE TABLE canonical_merchant_aliases (
    normalized_name       TEXT        PRIMARY KEY,
    canonical_merchant_id UUID        NOT NULL
                          REFERENCES canonical_merchants(id) ON DELETE CASCADE,
    source                TEXT        NOT NULL DEFAULT 'engine'
                          CHECK (source IN ('engine', 'user')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_canonical_merchant_aliases_canonical_id
    ON canonical_merchant_aliases (canonical_merchant_id);
```

- [ ] **Step 2: Apply the migration**

```bash
# From the repo root — adjust to your migration runner
psql "$DATABASE_URL" -f migrations/app/003_canonical_merchants.sql
```

Expected: no errors, two new tables visible in `\dt`.

- [ ] **Step 3: Verify the schema**

```bash
psql "$DATABASE_URL" -c "\d canonical_merchants"
psql "$DATABASE_URL" -c "\d canonical_merchant_aliases"
```

Expected output includes: `id uuid`, `name text`, `source text`, `UNIQUE constraint on name` for canonical_merchants; `normalized_name text (PK)`, `canonical_merchant_id uuid (FK → canonical_merchants.id ON DELETE CASCADE)` for aliases.

- [ ] **Step 4: Commit**

```bash
git add migrations/app/003_canonical_merchants.sql
git commit -m "feat: add canonical_merchants and canonical_merchant_aliases tables"
```

---

## Task 2: Engine — Stage 1 `CanonicalMerchant` Condition Type

**Files:**
- Modify: `services/engine/src/pipeline/stage1.rs`

**Interfaces:**
- Consumes: `canonical_merchant_aliases` table (loaded at stage start via `load_canonical_aliases()`)
- Produces: `CompiledConditionTree::CanonicalMerchant(Uuid)` variant; `evaluate_entry(tree, txn, aliases)` updated signature; `CanonicalAliasMap` type alias

- [ ] **Step 1: Write the failing tests**

Add to the `#[cfg(test)]` block at the bottom of `services/engine/src/pipeline/stage1.rs`:

```rust
#[test]
fn canonical_merchant_leaf_compiles_valid_uuid() {
    use std::collections::{HashMap, HashSet};
    use uuid::Uuid;
    let id = Uuid::new_v4();
    let mut aliases: HashMap<Uuid, HashSet<String>> = HashMap::new();
    aliases.insert(id, ["Netflix".to_string()].into());
    let json = serde_json::json!({
        "type": "canonical_merchant",
        "canonical_merchant_id": id.to_string()
    });
    let tree = compile_tree(&json, &aliases).unwrap();
    assert!(matches!(tree, CompiledConditionTree::CanonicalMerchant(_)));
}

#[test]
fn canonical_merchant_evaluates_exact_alias_hit() {
    use std::collections::{HashMap, HashSet};
    use uuid::Uuid;
    let id = Uuid::new_v4();
    let mut aliases: HashMap<Uuid, HashSet<String>> = HashMap::new();
    aliases.insert(id, ["Netflixcom".to_string(), "Netflix Llc".to_string()].into());
    let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1499, "Netflixcom");
    let json = serde_json::json!({
        "type": "canonical_merchant",
        "canonical_merchant_id": id.to_string()
    });
    let tree = compile_tree(&json, &aliases).unwrap();
    assert!(evaluate_entry(&tree, &txn, &aliases));
}

#[test]
fn canonical_merchant_evaluates_miss_when_not_in_aliases() {
    use std::collections::{HashMap, HashSet};
    use uuid::Uuid;
    let id = Uuid::new_v4();
    let mut aliases: HashMap<Uuid, HashSet<String>> = HashMap::new();
    aliases.insert(id, ["Netflixcom".to_string()].into());
    let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1499, "Spotify");
    let json = serde_json::json!({
        "type": "canonical_merchant",
        "canonical_merchant_id": id.to_string()
    });
    let tree = compile_tree(&json, &aliases).unwrap();
    assert!(!evaluate_entry(&tree, &txn, &aliases));
}

#[test]
fn canonical_merchant_unknown_id_returns_false() {
    use std::collections::{HashMap, HashSet};
    use uuid::Uuid;
    // Alias map has no entry for this UUID — evaluates to false, does not panic.
    let id = Uuid::new_v4();
    let aliases: HashMap<Uuid, HashSet<String>> = HashMap::new();
    let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1499, "Netflix");
    let json = serde_json::json!({
        "type": "canonical_merchant",
        "canonical_merchant_id": id.to_string()
    });
    let tree = compile_tree(&json, &aliases).unwrap();
    assert!(!evaluate_entry(&tree, &txn, &aliases));
}
```

- [ ] **Step 2: Run failing tests**

```bash
cd services/engine && cargo test stage1 2>&1 | grep -E "FAILED|error\[" | head -20
```

Expected: compile errors (CanonicalMerchant variant not yet defined, compile_tree signature mismatch).

- [ ] **Step 3: Add `CanonicalMerchant` variant and update `evaluate`**

In `services/engine/src/pipeline/stage1.rs`:

**a) Add type alias after the `use` block:**
```rust
/// Pre-loaded canonical merchant aliases keyed by canonical_merchant_id.
/// Built from `canonical_merchant_aliases` table at Stage 1 start.
pub type CanonicalAliasMap = std::collections::HashMap<Uuid, std::collections::HashSet<String>>;
```

**b) Add variant to `CompiledConditionTree`:**
```rust
/// Matches when the transaction's merchant_normalized is in the alias set
/// for this canonical merchant. Alias set is resolved at compile time from
/// the pre-loaded CanonicalAliasMap.
CanonicalMerchant(Uuid),
```

**c) Update `evaluate` to accept `canonical_aliases` and handle the new variant:**

Change the signature of `evaluate` and `evaluate_entry`:
```rust
pub fn evaluate_entry(
    tree: &CompiledConditionTree,
    txn: &TransactionRow,
    canonical_aliases: &CanonicalAliasMap,
) -> bool {
    evaluate(tree, txn, canonical_aliases)
}

fn evaluate(
    node: &CompiledConditionTree,
    txn: &TransactionRow,
    canonical_aliases: &CanonicalAliasMap,
) -> bool {
    match node {
        CompiledConditionTree::And(children) => children.iter().all(|c| evaluate(c, txn, canonical_aliases)),
        CompiledConditionTree::Or(children)  => children.iter().any(|c| evaluate(c, txn, canonical_aliases)),
        CompiledConditionTree::Not(child)    => !evaluate(child, txn, canonical_aliases),
        CompiledConditionTree::Xor(a, b)    => evaluate(a, txn, canonical_aliases) ^ evaluate(b, txn, canonical_aliases),

        CompiledConditionTree::PayeeExact(s) => {
            txn.merchant_normalized.eq_ignore_ascii_case(s)
        }
        CompiledConditionTree::PayeeContains(s) => {
            txn.merchant_normalized
                .to_ascii_lowercase()
                .contains(&s.to_ascii_lowercase())
        }
        CompiledConditionTree::PayeeRegex(re) => re.is_match(&txn.merchant_normalized),
        CompiledConditionTree::PayeeOneOf(list) => list
            .iter()
            .any(|s| txn.merchant_normalized.eq_ignore_ascii_case(s)),

        CompiledConditionTree::AmountRange { min, max } => {
            let a = txn.amount_cents;
            min.map_or(true, |m| a >= m) && max.map_or(true, |m| a <= m)
        }

        CompiledConditionTree::DateDayOfMonth { day, tolerance_days } => {
            let txn_day = txn.date.day() as i32;
            let target  = i32::from(*day);
            let tol     = i32::from(*tolerance_days);
            let diff = (txn_day - target).abs();
            diff <= tol
        }

        CompiledConditionTree::DateRange { start, end } => {
            txn.date >= *start && txn.date <= *end
        }

        CompiledConditionTree::AccountId(id) => txn.account_id == *id,

        CompiledConditionTree::LabelMatched(_label_id) => false,

        CompiledConditionTree::CanonicalMerchant(id) => {
            canonical_aliases
                .get(id)
                .map_or(false, |aliases| aliases.contains(&txn.merchant_normalized))
        }
    }
}
```

**d) Update `compile_tree` to accept `canonical_aliases` and parse the new leaf:**

Change signature:
```rust
fn compile_tree(v: &serde_json::Value, canonical_aliases: &CanonicalAliasMap) -> Result<CompiledConditionTree>
```

Add new match arm in the `leaf_type` match block (after the `"label"` arm):
```rust
"canonical_merchant" => {
    let id_str = string_value(v, "canonical_merchant_id")?;
    let id: Uuid = id_str
        .parse()
        .with_context(|| format!("invalid UUID in canonical_merchant leaf: {id_str}"))?;
    Ok(CompiledConditionTree::CanonicalMerchant(id))
}
```

Update all recursive calls inside `compile_tree` to pass `canonical_aliases`:
```rust
// In AND/OR/NOT/XOR branches, change:
let children: Vec<CompiledConditionTree> = children_val
    .iter()
    .map(|c| compile_tree(c, canonical_aliases))
    .collect::<Result<_>>()?;
```

**e) Update `compile_entry` to accept and pass through `canonical_aliases`:**
```rust
fn compile_entry(row: EntryRow, canonical_aliases: &CanonicalAliasMap) -> Result<CompiledEntry> {
    let conditions = compile_tree(&row.conditions, canonical_aliases)
        .with_context(|| format!("failed to compile conditions for entry {}", row.id))?;
    Ok(CompiledEntry {
        entry_id: row.id,
        priority: row.priority,
        conditions,
    })
}
```

- [ ] **Step 4: Add `load_canonical_aliases` DB function and update `run()`**

Add the loader function after `load_entries`:
```rust
/// Load all canonical merchant aliases into a map keyed by canonical_merchant_id.
/// Returns HashMap<canonical_merchant_id, HashSet<normalized_name>>.
async fn load_canonical_aliases(pool: &PgPool) -> Result<CanonicalAliasMap> {
    #[derive(sqlx::FromRow)]
    struct Row {
        canonical_merchant_id: Uuid,
        normalized_name:       String,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT canonical_merchant_id, normalized_name FROM canonical_merchant_aliases",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical merchant aliases for stage 1")?;

    let mut map: CanonicalAliasMap = std::collections::HashMap::new();
    for row in rows {
        map.entry(row.canonical_merchant_id)
            .or_default()
            .insert(row.normalized_name);
    }
    Ok(map)
}
```

Update `run()` to load canonical aliases, pass to compile, and pass to evaluate:
```rust
pub async fn run(entity_id: Uuid, pool: &PgPool) -> Result<Stage1Output> {
    let txns = load_transactions(entity_id, pool).await?;
    let entry_rows = load_entries(entity_id, pool).await?;
    let canonical_aliases = load_canonical_aliases(pool).await?;

    let mut compiled_entries: Vec<CompiledEntry> = entry_rows
        .into_iter()
        .filter_map(|row| {
            let entry_id = row.id;
            match compile_entry(row, &canonical_aliases) {
                Ok(e) => Some(e),
                Err(e) => {
                    tracing::warn!(%entry_id, "entry compile error (skipped): {e:?}");
                    None
                }
            }
        })
        .collect();

    compiled_entries.sort_by_key(|e| e.priority);

    tracing::debug!(entries = compiled_entries.len(), txns = txns.len(), "stage 1: compiled entries, starting match");

    let results: Vec<(Uuid, Vec<Uuid>, bool)> = txns
        .par_iter()
        .map(|txn| {
            let matched: Vec<Uuid> = compiled_entries
                .iter()
                .filter(|entry| evaluate_entry(&entry.conditions, txn, &canonical_aliases))
                .map(|entry| entry.entry_id)
                .collect();
            let unmatched = matched.is_empty();
            (txn.id, matched, unmatched)
        })
        .collect();

    let total_assignments: u64 = results.iter().map(|(_, m, _)| m.len() as u64).sum();
    let unmatched_tx_ids: Vec<Uuid> = results
        .iter()
        .filter(|(_, _, u)| *u)
        .map(|(id, _, _)| *id)
        .collect();

    persist_assignments(entity_id, &results, pool).await?;

    let matched_entry_ids: Vec<Uuid> = results
        .iter()
        .flat_map(|(_, entry_ids, _)| entry_ids.iter().copied())
        .collect::<std::collections::HashSet<_>>()
        .into_iter()
        .collect();
    update_next_due_dates(entity_id, &matched_entry_ids, pool).await?;

    Ok(Stage1Output {
        total_assignments,
        unmatched_tx_ids,
    })
}
```

**Also update all existing tests** that call `compile_tree(...)` to pass `&HashMap::default()` as the second argument, and all tests that call `evaluate_entry(tree, txn)` to pass `&HashMap::default()` as the third argument. For example:

```rust
// In every test helper `eval()`:
fn eval(json: serde_json::Value, txn: &TransactionRow) -> bool {
    use std::collections::HashMap;
    let tree = compile_tree(&json, &HashMap::default()).unwrap();
    evaluate(&tree, txn, &HashMap::default())
}
```

- [ ] **Step 5: Run all stage1 tests**

```bash
cd services/engine && cargo test stage1 -- --nocapture 2>&1 | tail -20
```

Expected: all tests pass including the four new canonical_merchant tests.

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/stage1.rs
git commit -m "feat(engine): add CanonicalMerchant condition type to stage 1"
```

---

## Task 3: Engine — Stage 0 Canonical Resolution

**Files:**
- Modify: `services/engine/src/pipeline/stage0.rs`

**Interfaces:**
- Consumes: `canonical_merchants` + `canonical_merchant_aliases` tables
- Produces: `ClassifiedCandidate.canonical_merchant_id: Option<Uuid>` field (Some for Insert/Supersede, None for Skip); new `CanonicalSnapshot` struct; `resolve_canonical()` function

- [ ] **Step 1: Write the failing tests**

Add to the `#[cfg(test)]` block in `services/engine/src/pipeline/stage0.rs`:

```rust
#[test]
fn canonical_snapshot_exact_hit_returns_existing_id() {
    use std::collections::HashMap;
    let existing_id = uuid::Uuid::new_v4();
    let mut snapshot = CanonicalSnapshot {
        aliases: HashMap::from([("Netflixcom".to_string(), existing_id)]),
        names: vec![(existing_id, "Netflix".to_string())],
    };
    let result = snapshot.resolve_in_memory("Netflixcom");
    assert_eq!(result, CanonicalResolution::ExactHit(existing_id));
}

#[test]
fn canonical_snapshot_lcs_hit_returns_fuzzy_match() {
    use std::collections::HashMap;
    let existing_id = uuid::Uuid::new_v4();
    let snapshot = CanonicalSnapshot {
        aliases: HashMap::new(),
        names: vec![(existing_id, "Starbucks".to_string())],
    };
    // "Starbuckscom" vs "Starbucks": LCS = 9, max(9,12) = 12, ratio = 9/12 = 0.75 >= 0.70
    let result = snapshot.resolve_in_memory("Starbuckscom");
    assert!(matches!(result, CanonicalResolution::FuzzyHit(_)));
    if let CanonicalResolution::FuzzyHit(id) = result {
        assert_eq!(id, existing_id);
    }
}

#[test]
fn canonical_snapshot_miss_below_threshold() {
    use std::collections::HashMap;
    let existing_id = uuid::Uuid::new_v4();
    let snapshot = CanonicalSnapshot {
        aliases: HashMap::new(),
        names: vec![(existing_id, "Walmart".to_string())],
    };
    // "Walgreens" LCS ratio against "Walmart" ≈ 0.44 — below 0.70
    let result = snapshot.resolve_in_memory("Walgreens");
    assert_eq!(result, CanonicalResolution::Miss);
}

#[test]
fn canonical_snapshot_exact_hit_takes_priority_over_fuzzy() {
    use std::collections::HashMap;
    let exact_id = uuid::Uuid::new_v4();
    let fuzzy_id = uuid::Uuid::new_v4();
    let snapshot = CanonicalSnapshot {
        aliases: HashMap::from([("Starbucks".to_string(), exact_id)]),
        names: vec![
            (exact_id, "Starbucks".to_string()),
            (fuzzy_id, "Starbucks Corp".to_string()),
        ],
    };
    let result = snapshot.resolve_in_memory("Starbucks");
    assert_eq!(result, CanonicalResolution::ExactHit(exact_id));
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd services/engine && cargo test canonical_snapshot 2>&1 | grep -E "FAILED|error\[" | head -10
```

Expected: compile errors (CanonicalSnapshot, CanonicalResolution not defined).

- [ ] **Step 3: Add `CanonicalSnapshot` struct and in-memory resolution**

Add after the existing internal types block in `services/engine/src/pipeline/stage0.rs`:

```rust
/// In-memory snapshot of all canonical merchants and their aliases.
/// Loaded once per Stage 0 run; mutated as new aliases/merchants are created.
pub(crate) struct CanonicalSnapshot {
    /// normalized_name → canonical_merchant_id (exact alias lookup)
    pub aliases: std::collections::HashMap<String, Uuid>,
    /// All canonical merchants as (id, name) for LCS fallback search
    pub names: Vec<(Uuid, String)>,
}

/// Result of resolving a normalized merchant name against the canonical snapshot.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum CanonicalResolution {
    ExactHit(Uuid),
    FuzzyHit(Uuid),
    Miss,
}

/// LCS ratio threshold for auto-grouping a normalized name into an existing canonical.
const CANONICAL_LCS_THRESHOLD: f64 = 0.70;

impl CanonicalSnapshot {
    /// Resolve a normalized merchant name in memory (no I/O).
    /// Exact alias match takes priority; falls back to LCS against canonical names.
    pub fn resolve_in_memory(&self, normalized_name: &str) -> CanonicalResolution {
        // Pass 1: exact alias lookup O(1)
        if let Some(&id) = self.aliases.get(normalized_name) {
            return CanonicalResolution::ExactHit(id);
        }
        // Pass 2: LCS against all canonical names
        let best = self.names.iter().find(|(_, name)| {
            lcs_ratio(name, normalized_name) >= CANONICAL_LCS_THRESHOLD
        });
        match best {
            Some((id, _)) => CanonicalResolution::FuzzyHit(*id),
            None          => CanonicalResolution::Miss,
        }
    }

    /// Register a new alias in memory (call after persisting to DB).
    pub fn add_alias(&mut self, normalized_name: String, canonical_id: Uuid) {
        self.aliases.insert(normalized_name, canonical_id);
    }

    /// Register a new canonical merchant in memory (call after persisting to DB).
    pub fn add_canonical(&mut self, id: Uuid, name: String) {
        self.names.push((id, name));
    }
}
```

- [ ] **Step 4: Add DB loaders and persist helpers**

Add after the `CanonicalSnapshot` impl block:

```rust
/// Load all canonical merchants and aliases into a CanonicalSnapshot.
async fn load_canonical_snapshot(pool: &PgPool) -> Result<CanonicalSnapshot> {
    #[derive(sqlx::FromRow)]
    struct AliasRow {
        normalized_name:       String,
        canonical_merchant_id: Uuid,
    }
    #[derive(sqlx::FromRow)]
    struct NameRow {
        id:   Uuid,
        name: String,
    }

    let alias_rows: Vec<AliasRow> = sqlx::query_as(
        "SELECT normalized_name, canonical_merchant_id FROM canonical_merchant_aliases",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical merchant aliases for stage 0")?;

    let name_rows: Vec<NameRow> = sqlx::query_as(
        "SELECT id, name FROM canonical_merchants",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical merchants for stage 0")?;

    let aliases = alias_rows
        .into_iter()
        .map(|r| (r.normalized_name, r.canonical_merchant_id))
        .collect();
    let names = name_rows
        .into_iter()
        .map(|r| (r.id, r.name))
        .collect();

    Ok(CanonicalSnapshot { aliases, names })
}

/// Persist a new alias to the DB and register it in the snapshot.
async fn persist_canonical_alias(
    normalized_name: &str,
    canonical_id: Uuid,
    snapshot: &mut CanonicalSnapshot,
    pool: &PgPool,
) -> Result<()> {
    sqlx::query(
        r#"
        INSERT INTO canonical_merchant_aliases (normalized_name, canonical_merchant_id, source)
        VALUES ($1, $2, 'engine')
        ON CONFLICT (normalized_name) DO NOTHING
        "#,
    )
    .bind(normalized_name)
    .bind(canonical_id)
    .execute(pool)
    .await
    .context("failed to persist canonical merchant alias")?;

    snapshot.add_alias(normalized_name.to_string(), canonical_id);
    Ok(())
}

/// Create a new canonical merchant and its first alias; register both in snapshot.
async fn create_canonical_merchant(
    normalized_name: &str,
    snapshot: &mut CanonicalSnapshot,
    pool: &PgPool,
) -> Result<Uuid> {
    let (id,): (Uuid,) = sqlx::query_as(
        r#"
        INSERT INTO canonical_merchants (name, source)
        VALUES ($1, 'engine')
        ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
        RETURNING id
        "#,
    )
    .bind(normalized_name)
    .fetch_one(pool)
    .await
    .context("failed to create canonical merchant")?;

    snapshot.add_canonical(id, normalized_name.to_string());
    persist_canonical_alias(normalized_name, id, snapshot, pool).await?;
    Ok(id)
}

/// Resolve and persist the canonical merchant for a single normalized name.
/// Returns the canonical_merchant_id (always Some — creates one if needed).
async fn resolve_canonical(
    normalized_name: &str,
    snapshot: &mut CanonicalSnapshot,
    pool: &PgPool,
) -> Result<Uuid> {
    match snapshot.resolve_in_memory(normalized_name) {
        CanonicalResolution::ExactHit(id) => Ok(id),
        CanonicalResolution::FuzzyHit(id) => {
            persist_canonical_alias(normalized_name, id, snapshot, pool).await?;
            Ok(id)
        }
        CanonicalResolution::Miss => {
            create_canonical_merchant(normalized_name, snapshot, pool).await
        }
    }
}
```

- [ ] **Step 5: Add `canonical_merchant_id` field to `ClassifiedCandidate` and update `run()`**

**a) Update `ClassifiedCandidate`:**
```rust
#[derive(Debug)]
struct ClassifiedCandidate {
    candidate:             Candidate,
    action:                DedupAction,
    settlement_status:     &'static str,
    /// Populated after dedup for Insert/Supersede; None for Skip.
    canonical_merchant_id: Option<Uuid>,
}
```

**b) Update all construction sites in `classify_one()` to include `canonical_merchant_id: None`:**

In every `return Ok(ClassifiedCandidate { candidate, action, settlement_status });` statement, add `canonical_merchant_id: None`:
```rust
return Ok(ClassifiedCandidate { candidate, action, settlement_status, canonical_merchant_id: None });
```
(There are 6 such return statements in classify_one — update all of them.)

**c) Add canonical resolution step in `run()` between classification and batch insert:**

After the `let imported: Vec<_> = classified.iter().filter(...).collect();` lines, add:

```rust
// Load canonical snapshot once for the whole batch.
let mut canonical_snapshot = load_canonical_snapshot(pool).await?;

// Resolve canonical merchant for each Insert/Supersede candidate.
// Sequential — snapshot is mutated as new merchants/aliases are created.
let mut classified = classified;
for c in classified.iter_mut() {
    if matches!(c.action, DedupAction::Insert | DedupAction::Supersede(_)) {
        let id = resolve_canonical(
            &c.candidate.merchant_normalized,
            &mut canonical_snapshot,
            pool,
        )
        .await?;
        c.canonical_merchant_id = Some(id);
    }
}

// Re-derive imported slice after mutation.
let imported: Vec<_> = classified
    .iter()
    .filter(|c| matches!(c.action, DedupAction::Insert | DedupAction::Supersede(_)))
    .collect();
```

Note: Remove the existing `let imported: Vec<_>` line that came before the batch insert call (it is now re-derived above).

- [ ] **Step 6: Run the new canonical snapshot tests**

```bash
cd services/engine && cargo test canonical_snapshot -- --nocapture 2>&1 | tail -20
```

Expected: all 4 canonical_snapshot tests pass.

- [ ] **Step 7: Run all stage0 tests to check for regressions**

```bash
cd services/engine && cargo test stage0 -- --nocapture 2>&1 | tail -20
```

Expected: all existing stage0 tests pass.

- [ ] **Step 8: Commit**

```bash
git add services/engine/src/pipeline/stage0.rs
git commit -m "feat(engine): canonical merchant resolution in stage 0 after dedup"
```

---

## Task 4: Engine — Stage 2 Canonical Grouping

**Files:**
- Modify: `services/engine/src/pipeline/stage2.rs`

**Interfaces:**
- Consumes: `canonical_merchant_aliases` + `canonical_merchants` tables (loaded at stage start)
- Produces: `pending_review` entries with `canonical_merchant` conditions; `extract_brand()` removed; `cluster_by_merchant()` removed

- [ ] **Step 1: Write the failing test**

Add to the `#[cfg(test)]` block in `services/engine/src/pipeline/stage2.rs`:

```rust
#[test]
fn group_by_canonical_groups_correctly() {
    use std::collections::HashMap;
    use uuid::Uuid;

    let id_netflix  = Uuid::new_v4();
    let id_spotify  = Uuid::new_v4();

    let alias_map: HashMap<String, Uuid> = [
        ("Netflixcom".to_string(),    id_netflix),
        ("Netflix Llc".to_string(),   id_netflix),
        ("Spotify".to_string(),       id_spotify),
    ].into();

    let txns = vec![
        make_txn("00000000-0000-0000-0000-000000000001", "2026-01-07", -1499, "Netflixcom"),
        make_txn("00000000-0000-0000-0000-000000000002", "2026-02-07", -1499, "Netflix Llc"),
        make_txn("00000000-0000-0000-0000-000000000003", "2026-01-15", -899,  "Spotify"),
    ];

    let groups = group_by_canonical(txns, &alias_map);

    assert_eq!(groups.len(), 2, "Netflix variants and Spotify should form 2 groups");
    let netflix_group = groups.iter().find(|(id, _)| *id == id_netflix).unwrap();
    assert_eq!(netflix_group.1.len(), 2, "both Netflix variants in one group");
    let spotify_group = groups.iter().find(|(id, _)| *id == id_spotify).unwrap();
    assert_eq!(spotify_group.1.len(), 1);
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd services/engine && cargo test group_by_canonical 2>&1 | grep -E "FAILED|error\[" | head -10
```

Expected: compile error (group_by_canonical not defined).

- [ ] **Step 3: Remove `extract_brand()` and `cluster_by_merchant()`, add `group_by_canonical()`**

**a) Delete** the `extract_brand()` function (lines ~162–175) and `cluster_by_merchant()` function (lines ~204–222) and `compute_merchant_confidence()` (lines ~178–185) entirely.

**b) Remove** the `MERCHANT_SIMILARITY_THRESHOLD` constant.

**c) Update the `Cluster` struct** to include `canonical_merchant_id`:

```rust
/// A group of unmatched transactions sharing the same canonical merchant.
#[derive(Debug)]
pub(crate) struct Cluster {
    pub canonical_merchant_id:    uuid::Uuid,
    pub representative_merchant:  String,  // canonical merchant name
    pub transactions:             Vec<UnmatchedTxn>,
}
```

**d) Add the new grouping function:**

```rust
/// Group unmatched transactions by their canonical merchant.
///
/// `alias_map` is keyed by normalized_name → canonical_merchant_id and is
/// loaded fresh from DB at Stage 2 start (Stage 0 already populated it).
/// Transactions whose normalized name has no alias are grouped under a
/// `Uuid::nil()` sentinel and skipped during persist.
pub(crate) fn group_by_canonical(
    txns: Vec<UnmatchedTxn>,
    alias_map: &std::collections::HashMap<String, uuid::Uuid>,
) -> Vec<(uuid::Uuid, Vec<UnmatchedTxn>)> {
    let mut groups: std::collections::HashMap<uuid::Uuid, Vec<UnmatchedTxn>> =
        std::collections::HashMap::new();

    for txn in txns {
        let canonical_id = alias_map
            .get(&txn.merchant_normalized)
            .copied()
            .unwrap_or(uuid::Uuid::nil()); // nil = unresolved; skipped at persist
        groups.entry(canonical_id).or_default().push(txn);
    }

    groups.into_iter().collect()
}
```

**e) Add DB loader** for canonical names (needed to build Cluster.representative_merchant):

```rust
#[derive(sqlx::FromRow)]
struct CanonicalNameRow {
    id:   uuid::Uuid,
    name: String,
}

async fn load_canonical_for_stage2(
    pool: &sqlx::PgPool,
) -> anyhow::Result<(
    std::collections::HashMap<String, uuid::Uuid>,   // alias_map: normalized_name → id
    std::collections::HashMap<uuid::Uuid, String>,   // name_map:  id → canonical name
)> {
    let alias_rows: Vec<(String, uuid::Uuid)> = sqlx::query_as(
        "SELECT normalized_name, canonical_merchant_id FROM canonical_merchant_aliases",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical aliases for stage 2")?;

    let name_rows: Vec<CanonicalNameRow> = sqlx::query_as(
        "SELECT id, name FROM canonical_merchants",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical names for stage 2")?;

    let alias_map = alias_rows.into_iter().collect();
    let name_map = name_rows.into_iter().map(|r| (r.id, r.name)).collect();
    Ok((alias_map, name_map))
}
```

- [ ] **Step 4: Rewrite `run()` to use canonical grouping**

Replace the body of the `run()` function:

```rust
pub async fn run(
    entity_id: Uuid,
    unmatched_tx_ids: &[Uuid],
    pool: &PgPool,
) -> Result<Stage2Output> {
    // Delete stale engine-created pending_review entries before recreating.
    sqlx::query(
        "DELETE FROM entries WHERE entity_id = $1 AND source = 'engine' AND status = 'pending_review'",
    )
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to delete stale engine pending_review entries")?;

    if unmatched_tx_ids.is_empty() {
        return Ok(Stage2Output { clusters_created: 0 });
    }

    let txns = load_unmatched(entity_id, unmatched_tx_ids, pool).await?;

    let (alias_map, name_map) = load_canonical_for_stage2(pool).await?;

    let grouped = group_by_canonical(txns, &alias_map);

    // Build Cluster structs (skip nil canonical — unresolved names).
    let clusters: Vec<Cluster> = grouped
        .into_iter()
        .filter(|(id, _)| !id.is_nil())
        .map(|(canonical_id, transactions)| {
            let name = name_map
                .get(&canonical_id)
                .cloned()
                .unwrap_or_default();
            Cluster {
                canonical_merchant_id:   canonical_id,
                representative_merchant: name,
                transactions,
            }
        })
        .collect();

    let scored: Vec<(Cluster, ClusterScore)> = clusters
        .into_par_iter()
        .map(|cluster| {
            let score = score_cluster(&cluster);
            (cluster, score)
        })
        .collect();

    let mut clusters_created: u32 = 0;
    for (cluster, score) in scored {
        if score.confidence < MIN_CONFIDENCE {
            continue;
        }
        persist_cluster(entity_id, &cluster, &score, pool).await?;
        clusters_created += 1;
    }

    Ok(Stage2Output { clusters_created })
}
```

- [ ] **Step 5: Update `persist_cluster()` to use `canonical_merchant` condition**

Replace the `conditions` construction in `persist_cluster()`:

```rust
// Replace the existing conditions block:
let conditions = serde_json::json!({
    "op": "AND",
    "children": [{
        "type": "canonical_merchant",
        "canonical_merchant_id": cluster.canonical_merchant_id.to_string()
    }]
});
```

Remove the label upsert block (the `INSERT INTO labels` query) — Stage 2 no longer creates labels. The entry no longer has a `label_id` from Stage 2. Update the `INSERT INTO entries` accordingly:

```rust
let entry_id: (Uuid,) = sqlx::query_as(
    r#"
    INSERT INTO entries (
      entity_id, label_id, direction, entry_type, period_days, next_due_date,
      conditions, projected_rate_per_day, status, source, project_tentatively, start_date,
      alert_type, confidence, merchant_confidence, timing_confidence, amount_confidence,
      sample_merchants, matched_transaction_count
    ) VALUES (
      $1, NULL, $2, $3, $4, $5,
      $6, $7, 'pending_review', 'engine', false, $8,
      'new', $9, $10, $11, $12,
      $13, $14
    )
    RETURNING id
    "#,
)
.bind(entity_id)
.bind(direction)
.bind(score.entry_type)
.bind(period_days)
.bind(next_due_date)
.bind(&conditions)
.bind(rate_per_day)
.bind(start_date)
.bind(score.confidence)
.bind(score.merchant_confidence)
.bind(score.timing_confidence)
.bind(score.amount_confidence)
.bind(&score.sample_merchants)
.bind(cluster.transactions.len() as i32)
.fetch_one(pool)
.await
.context("failed to insert pending_review entry")?;
```

- [ ] **Step 6: Update `score_cluster()` — `compute_merchant_confidence()` is gone**

`score_cluster()` calls `compute_merchant_confidence(cluster)` which we removed. Replace it with a constant 1.0 (canonical grouping already guarantees merchant identity):

```rust
// Replace: let merchant_confidence = compute_merchant_confidence(cluster);
let merchant_confidence = 1.0_f64;
```

Also update `ClusterScore.suggested_name` to use `cluster.representative_merchant.clone()` — this was already the case, no change needed.

- [ ] **Step 7: Run all stage2 tests**

```bash
cd services/engine && cargo test stage2 -- --nocapture 2>&1 | tail -30
```

The `clustering_groups_similar_merchants` and `clustering_groups_fuzzy_matches` tests reference `cluster_by_merchant` which is now removed — delete those two tests. All other tests should pass.

- [ ] **Step 8: Run the full engine test suite**

```bash
cd services/engine && cargo test -- --nocapture 2>&1 | tail -20
```

Expected: all tests pass, no warnings about unused functions.

- [ ] **Step 9: Commit**

```bash
git add services/engine/src/pipeline/stage2.rs
git commit -m "feat(engine): replace LCS clustering with canonical merchant grouping in stage 2"
```

---

## Task 5: Web Store — Canonical Merchants CRUD

**Files:**
- Create: `services/web/store/canonical_merchants.go`

**Interfaces:**
- Produces: `CanonicalMerchant`, `CanonicalMerchantAlias`, `CanonicalMerchantWithCounts` types; `ListCanonicalMerchants`, `GetCanonicalMerchant`, `CreateCanonicalMerchant`, `RenameCanonicalMerchant`, `DeleteCanonicalMerchant`, `ListCanonicalMerchantAliases`, `AddCanonicalMerchantAlias`, `DeleteCanonicalMerchantAlias`, `MergeCanonicalMerchants`, `SplitCanonicalMerchant` methods on `*Store`

- [ ] **Step 1: Create `services/web/store/canonical_merchants.go`**

```go
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// CanonicalMerchant represents a row from the canonical_merchants table.
// Global — no entity_id.
type CanonicalMerchant struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	Source    string    `db:"source"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// CanonicalMerchantAlias represents a row from the canonical_merchant_aliases table.
type CanonicalMerchantAlias struct {
	NormalizedName      string    `db:"normalized_name"`
	CanonicalMerchantID string    `db:"canonical_merchant_id"`
	Source              string    `db:"source"`
	CreatedAt           time.Time `db:"created_at"`
}

// CanonicalMerchantWithCounts extends CanonicalMerchant with alias and transaction counts.
type CanonicalMerchantWithCounts struct {
	CanonicalMerchant
	AliasCount int `db:"alias_count"`
}

const canonicalMerchantCols = `id::text, name, source, created_at, updated_at`

// ListCanonicalMerchants returns all canonical merchants with alias counts, ordered by name.
func (s *Store) ListCanonicalMerchants(ctx context.Context) ([]CanonicalMerchantWithCounts, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT cm.id::text, cm.name, cm.source, cm.created_at, cm.updated_at,
		       COUNT(cma.normalized_name)::int AS alias_count
		FROM canonical_merchants cm
		LEFT JOIN canonical_merchant_aliases cma ON cma.canonical_merchant_id = cm.id
		GROUP BY cm.id, cm.name, cm.source, cm.created_at, cm.updated_at
		ORDER BY cm.name
	`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantWithCounts])
}

// GetCanonicalMerchant fetches a single canonical merchant by id.
func (s *Store) GetCanonicalMerchant(ctx context.Context, id string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+canonicalMerchantCols+` FROM canonical_merchants WHERE id = $1`, id)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// CreateCanonicalMerchant inserts a new canonical merchant with source='user'.
func (s *Store) CreateCanonicalMerchant(ctx context.Context, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO canonical_merchants (name, source)
		VALUES ($1, 'user')
		RETURNING `+canonicalMerchantCols,
		name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// RenameCanonicalMerchant updates the name of a canonical merchant.
func (s *Store) RenameCanonicalMerchant(ctx context.Context, id, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE canonical_merchants SET name = $2, updated_at = NOW() WHERE id = $1
		RETURNING `+canonicalMerchantCols,
		id, name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// DeleteCanonicalMerchant removes a canonical merchant (aliases cascade).
func (s *Store) DeleteCanonicalMerchant(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM canonical_merchants WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListCanonicalMerchantAliases returns all aliases for a canonical merchant.
func (s *Store) ListCanonicalMerchantAliases(ctx context.Context, id string) ([]CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT normalized_name, canonical_merchant_id::text, source, created_at
		FROM canonical_merchant_aliases
		WHERE canonical_merchant_id = $1
		ORDER BY normalized_name
	`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// AddCanonicalMerchantAlias inserts a new alias with source='user'.
// Returns pgx.ErrNoRows if the normalized_name already exists for another canonical.
func (s *Store) AddCanonicalMerchantAlias(ctx context.Context, canonicalID, normalizedName string) (CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO canonical_merchant_aliases (normalized_name, canonical_merchant_id, source)
		VALUES ($1, $2, 'user')
		RETURNING normalized_name, canonical_merchant_id::text, source, created_at
	`, normalizedName, canonicalID)
	if err != nil {
		return CanonicalMerchantAlias{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// DeleteCanonicalMerchantAlias removes a single alias.
func (s *Store) DeleteCanonicalMerchantAlias(ctx context.Context, canonicalID, normalizedName string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM canonical_merchant_aliases
		WHERE canonical_merchant_id = $1 AND normalized_name = $2
	`, canonicalID, normalizedName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// MergeCanonicalMerchants moves all aliases from otherID into targetID,
// updates any entry conditions referencing otherID to use targetID,
// then deletes otherID. Runs in a single transaction.
func (s *Store) MergeCanonicalMerchants(ctx context.Context, targetID, otherID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Move aliases.
	if _, err := tx.Exec(ctx, `
		UPDATE canonical_merchant_aliases
		SET canonical_merchant_id = $1
		WHERE canonical_merchant_id = $2
	`, targetID, otherID); err != nil {
		return err
	}

	// Update entry conditions JSONB: text-replace otherID UUID string with targetID.
	if _, err := tx.Exec(ctx, `
		UPDATE entries
		SET conditions = REPLACE(conditions::text, $1, $2)::jsonb
		WHERE conditions IS NOT NULL
		  AND conditions::text LIKE '%' || $1 || '%'
	`, otherID, targetID); err != nil {
		return err
	}

	// Delete the merged canonical.
	if _, err := tx.Exec(ctx, `DELETE FROM canonical_merchants WHERE id = $1`, otherID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SplitCanonicalMerchant moves the given aliases to a new canonical merchant with newName.
// Returns the new CanonicalMerchant. Runs in a single transaction.
func (s *Store) SplitCanonicalMerchant(ctx context.Context, sourceID, newName string, aliases []string) (CanonicalMerchant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Create the new canonical merchant.
	var newMerchant CanonicalMerchant
	rows, err := tx.Query(ctx, `
		INSERT INTO canonical_merchants (name, source)
		VALUES ($1, 'user')
		RETURNING `+canonicalMerchantCols,
		newName)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	newMerchant, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
	if err != nil {
		return CanonicalMerchant{}, err
	}

	// Move selected aliases to the new canonical.
	if _, err := tx.Exec(ctx, `
		UPDATE canonical_merchant_aliases
		SET canonical_merchant_id = $1
		WHERE canonical_merchant_id = $2
		  AND normalized_name = ANY($3)
	`, newMerchant.ID, sourceID, aliases); err != nil {
		return CanonicalMerchant{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return CanonicalMerchant{}, err
	}
	return newMerchant, nil
}
```

- [ ] **Step 2: Verify the file compiles**

```bash
cd services/web && go build ./store/... 2>&1
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add services/web/store/canonical_merchants.go
git commit -m "feat(web/store): canonical merchants CRUD + merge + split"
```

---

## Task 6: Web Handler — Canonical Merchants API

**Files:**
- Create: `services/web/handler/canonical_merchants.go`
- Modify: `services/web/main.go`

**Interfaces:**
- Consumes: `store.CanonicalMerchant`, `store.CanonicalMerchantAlias`, `store.CanonicalMerchantWithCounts` from Task 5; `queue.Publisher` for enqueuing `rules.reprocess` jobs
- Produces: 9 endpoints registered under `/api/canonical-merchants`

- [ ] **Step 1: Create `services/web/handler/canonical_merchants.go`**

```go
package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// CanonicalMerchantsHandler handles canonical merchant endpoints.
type CanonicalMerchantsHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewCanonicalMerchantsHandler creates a CanonicalMerchantsHandler.
func NewCanonicalMerchantsHandler(s *store.Store, pub *queue.Publisher) *CanonicalMerchantsHandler {
	return &CanonicalMerchantsHandler{s: s, pub: pub}
}

// ── View types ────────────────────────────────────────────────────────────────

type canonicalMerchantView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	AliasCount int    `json:"alias_count,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type canonicalMerchantAliasView struct {
	NormalizedName      string `json:"normalized_name"`
	CanonicalMerchantID string `json:"canonical_merchant_id"`
	Source              string `json:"source"`
	CreatedAt           string `json:"created_at"`
}

func toCanonicalMerchantView(m store.CanonicalMerchantWithCounts) canonicalMerchantView {
	return canonicalMerchantView{
		ID:         m.ID,
		Name:       m.Name,
		Source:     m.Source,
		AliasCount: m.AliasCount,
		CreatedAt:  m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toCanonicalMerchantSingleView(m store.CanonicalMerchant) canonicalMerchantView {
	return canonicalMerchantView{
		ID:        m.ID,
		Name:      m.Name,
		Source:    m.Source,
		CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toCanonicalMerchantAliasView(a store.CanonicalMerchantAlias) canonicalMerchantAliasView {
	return canonicalMerchantAliasView{
		NormalizedName:      a.NormalizedName,
		CanonicalMerchantID: a.CanonicalMerchantID,
		Source:              a.Source,
		CreatedAt:           a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ── Input/output types ────────────────────────────────────────────────────────

type listCanonicalMerchantsOutput struct {
	Body response.Envelope[[]canonicalMerchantView]
}

type getCanonicalMerchantInput struct {
	PathID string `path:"id"`
}
type getCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

type createCanonicalMerchantInput struct {
	Body struct {
		Name string `json:"name" required:"true"`
	}
}
type createCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

type updateCanonicalMerchantInput struct {
	PathID string `path:"id"`
	Body   struct {
		Name string `json:"name" required:"true"`
	}
}
type updateCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

type deleteCanonicalMerchantInput struct {
	PathID string `path:"id"`
}

type listCanonicalMerchantAliasesInput struct {
	PathID string `path:"id"`
}
type listCanonicalMerchantAliasesOutput struct {
	Body response.Envelope[[]canonicalMerchantAliasView]
}

type addCanonicalMerchantAliasInput struct {
	PathID string `path:"id"`
	Body   struct {
		NormalizedName string `json:"normalized_name" required:"true"`
	}
}
type addCanonicalMerchantAliasOutput struct {
	Body response.Envelope[canonicalMerchantAliasView]
}

type deleteCanonicalMerchantAliasInput struct {
	PathID         string `path:"id"`
	NormalizedName string `path:"normalized_name"`
}

type mergeCanonicalMerchantsInput struct {
	PathID      string `path:"id"`
	PathOtherID string `path:"other_id"`
}

type splitCanonicalMerchantInput struct {
	PathID string `path:"id"`
	Body   struct {
		NewName string   `json:"new_name" required:"true"`
		Aliases []string `json:"aliases" required:"true"`
	}
}
type splitCanonicalMerchantOutput struct {
	Body response.Envelope[canonicalMerchantView]
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (h *CanonicalMerchantsHandler) ListCanonicalMerchants(ctx context.Context, _ *struct{}) (*listCanonicalMerchantsOutput, error) {
	items, err := h.s.ListCanonicalMerchants(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	views := make([]canonicalMerchantView, len(items))
	for i, item := range items {
		views[i] = toCanonicalMerchantView(item)
	}
	out := &listCanonicalMerchantsOutput{}
	out.Body = response.Single(views)
	return out, nil
}

func (h *CanonicalMerchantsHandler) GetCanonicalMerchant(ctx context.Context, input *getCanonicalMerchantInput) (*getCanonicalMerchantOutput, error) {
	item, err := h.s.GetCanonicalMerchant(ctx, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantSingleView(item))
	return out, nil
}

func (h *CanonicalMerchantsHandler) CreateCanonicalMerchant(ctx context.Context, input *createCanonicalMerchantInput) (*createCanonicalMerchantOutput, error) {
	item, err := h.s.CreateCanonicalMerchant(ctx, input.Body.Name)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantSingleView(item))
	return out, nil
}

func (h *CanonicalMerchantsHandler) UpdateCanonicalMerchant(ctx context.Context, input *updateCanonicalMerchantInput) (*updateCanonicalMerchantOutput, error) {
	item, err := h.s.RenameCanonicalMerchant(ctx, input.PathID, input.Body.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantSingleView(item))
	return out, nil
}

func (h *CanonicalMerchantsHandler) DeleteCanonicalMerchant(ctx context.Context, input *deleteCanonicalMerchantInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	if err := h.s.DeleteCanonicalMerchant(ctx, input.PathID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("not found")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	_ = h.pub.EnqueueRulesReprocess(ctx, entityID)
	return nil, nil
}

func (h *CanonicalMerchantsHandler) ListCanonicalMerchantAliases(ctx context.Context, input *listCanonicalMerchantAliasesInput) (*listCanonicalMerchantAliasesOutput, error) {
	items, err := h.s.ListCanonicalMerchantAliases(ctx, input.PathID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	views := make([]canonicalMerchantAliasView, len(items))
	for i, a := range items {
		views[i] = toCanonicalMerchantAliasView(a)
	}
	out := &listCanonicalMerchantAliasesOutput{}
	out.Body = response.Single(views)
	return out, nil
}

func (h *CanonicalMerchantsHandler) AddCanonicalMerchantAlias(ctx context.Context, input *addCanonicalMerchantAliasInput) (*addCanonicalMerchantAliasOutput, error) {
	alias, err := h.s.AddCanonicalMerchantAlias(ctx, input.PathID, input.Body.NormalizedName)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("alias already assigned to a canonical merchant")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &addCanonicalMerchantAliasOutput{}
	out.Body = response.Single(toCanonicalMerchantAliasView(alias))
	return out, nil
}

func (h *CanonicalMerchantsHandler) DeleteCanonicalMerchantAlias(ctx context.Context, input *deleteCanonicalMerchantAliasInput) (*struct{}, error) {
	if err := h.s.DeleteCanonicalMerchantAlias(ctx, input.PathID, input.NormalizedName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("not found")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *CanonicalMerchantsHandler) MergeCanonicalMerchants(ctx context.Context, input *mergeCanonicalMerchantsInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	if err := h.s.MergeCanonicalMerchants(ctx, input.PathID, input.PathOtherID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("not found")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	_ = h.pub.EnqueueRulesReprocess(ctx, entityID)
	return nil, nil
}

func (h *CanonicalMerchantsHandler) SplitCanonicalMerchant(ctx context.Context, input *splitCanonicalMerchantInput) (*splitCanonicalMerchantOutput, error) {
	entityID := middleware.EntityID(ctx)
	newMerchant, err := h.s.SplitCanonicalMerchant(ctx, input.PathID, input.Body.NewName, input.Body.Aliases)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("canonical merchant name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	_ = h.pub.EnqueueRulesReprocess(ctx, entityID)
	out := &splitCanonicalMerchantOutput{}
	out.Body = response.Single(toCanonicalMerchantSingleView(newMerchant))
	return out, nil
}

// RegisterCanonicalMerchantsRoutes registers all canonical merchant endpoints.
func RegisterCanonicalMerchantsRoutes(api huma.API, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewCanonicalMerchantsHandler(s, pub)

	huma.Register(api, huma.Operation{
		OperationID: "list-canonical-merchants",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants",
		Summary:     "List all canonical merchants",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListCanonicalMerchants)

	huma.Register(api, huma.Operation{
		OperationID: "create-canonical-merchant",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants",
		Summary:     "Create a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.CreateCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "get-canonical-merchant",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants/{id}",
		Summary:     "Get a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "update-canonical-merchant",
		Method:      http.MethodPut,
		Path:        "/canonical-merchants/{id}",
		Summary:     "Rename a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.UpdateCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-canonical-merchant",
		Method:        http.MethodDelete,
		Path:          "/canonical-merchants/{id}",
		Summary:       "Delete a canonical merchant",
		Tags:          []string{"canonical-merchants"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.DeleteCanonicalMerchant)

	huma.Register(api, huma.Operation{
		OperationID: "list-canonical-merchant-aliases",
		Method:      http.MethodGet,
		Path:        "/canonical-merchants/{id}/aliases",
		Summary:     "List aliases for a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListCanonicalMerchantAliases)

	huma.Register(api, huma.Operation{
		OperationID: "add-canonical-merchant-alias",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants/{id}/aliases",
		Summary:     "Add alias to a canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.AddCanonicalMerchantAlias)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-canonical-merchant-alias",
		Method:        http.MethodDelete,
		Path:          "/canonical-merchants/{id}/aliases/{normalized_name}",
		Summary:       "Remove alias from a canonical merchant",
		Tags:          []string{"canonical-merchants"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.DeleteCanonicalMerchantAlias)

	huma.Register(api, huma.Operation{
		OperationID:   "merge-canonical-merchants",
		Method:        http.MethodPost,
		Path:          "/canonical-merchants/{id}/merge/{other_id}",
		Summary:       "Merge another canonical merchant into this one",
		Tags:          []string{"canonical-merchants"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.MergeCanonicalMerchants)

	huma.Register(api, huma.Operation{
		OperationID: "split-canonical-merchant",
		Method:      http.MethodPost,
		Path:        "/canonical-merchants/{id}/split",
		Summary:     "Split aliases into a new canonical merchant",
		Tags:        []string{"canonical-merchants"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.SplitCanonicalMerchant)
}
```

- [ ] **Step 2: Check what `EnqueueRulesReprocess` looks like in the queue package**

```bash
grep -rn "EnqueueRules\|rules.reprocess\|RulesReprocess" /Users/utterback/Documents/personal/veloci/services/web/ | head -20
```

If `EnqueueRulesReprocess(ctx, entityID)` does not exist on `*queue.Publisher`, find the correct method name (likely `Enqueue` with a job type parameter) and update the three calls in the handler accordingly. The job type string to use is `"rules.reprocess"`.

- [ ] **Step 3: Register routes in `main.go`**

In `services/web/main.go`, add after the `handler.RegisterJobsRoutes(...)` line:

```go
handler.RegisterCanonicalMerchantsRoutes(subAPI, s, pub, perms)
```

- [ ] **Step 4: Build the web service**

```bash
cd services/web && go build ./... 2>&1
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add services/web/handler/canonical_merchants.go services/web/main.go
git commit -m "feat(web): canonical merchants API handler + route registration"
```

---

## Task 7: Config Page — Merchants Tab

**Files:**
- Modify: `services/web/page/handler.go`
- Modify: `services/web/page/configuration.templ`
- Modify: `services/web/page/configuration_templ.go` (regenerated)

**Interfaces:**
- Consumes: `store.CanonicalMerchantWithCounts` from Task 5; `/api/canonical-merchants` endpoints from Task 6
- Produces: new "Merchants" tab in the Configuration page between Labels and Institution Mappings

- [ ] **Step 1: Update `ConfigurationData` and `Configuration()` handler**

In `services/web/page/handler.go`, update `ConfigurationData`:

```go
type ConfigurationData struct {
	Tab                string
	Labels             []store.LabelWithCount
	CanonicalMerchants []store.CanonicalMerchantWithCounts
	Institutions       []store.Institution
}
```

Update the `Configuration()` handler to load canonical merchants:

```go
func (s *Server) Configuration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)

	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "labels"
	}

	labels, _ := s.store.ListLabelsWithEntryCount(ctx, entityID)
	merchants, _ := s.store.ListCanonicalMerchants(ctx)
	institutions, _ := s.store.ListInstitutions(ctx, entityID)

	data := ConfigurationData{
		Tab:                tab,
		Labels:             labels,
		CanonicalMerchants: merchants,
		Institutions:       institutions,
	}
	s.render(w, r, ConfigurationPage(s.buildShellData(r), data))
}
```

- [ ] **Step 2: Build to verify handler compiles**

```bash
cd services/web && go build ./page/... 2>&1
```

Expected: error about `CanonicalMerchants` not being used in the template yet — that's fine, continue.

- [ ] **Step 3: Add the Merchants tab to `configuration.templ`**

Open `services/web/page/configuration.templ`. Find the tab bar section (it renders "labels" and "institutions" tab buttons). Add a "merchants" tab button between them:

```templ
<a href="/configuration?tab=merchants"
   class={ "cfg-tab", templ.KV("cfg-tab--active", data.Tab == "merchants") }>
  Merchants
</a>
```

Add the merchants section at the same level as `cfgLabelsSection` and `cfgInstitutionsSection`:

```templ
if data.Tab == "merchants" {
  @cfgMerchantsSection(data.CanonicalMerchants)
}
```

Add the new component at the bottom of the file (before the final `}`):

```templ
templ cfgMerchantsSection(merchants []store.CanonicalMerchantWithCounts) {
  <div class="cfg-section">
    <div class="cfg-section__header">
      <h2>Canonical Merchants</h2>
      <button
        class="btn btn--sm btn--primary"
        data-on-click="$newMerchantOpen = !$newMerchantOpen">
        New merchant
      </button>
    </div>

    <div data-show="$newMerchantOpen" style="display:none" class="cfg-inline-form">
      <input
        id="new-merchant-name"
        type="text"
        placeholder="Canonical name (e.g. Netflix)"
        class="input input--sm" />
      <button
        class="btn btn--sm btn--primary"
        data-on-click={`
          fetch('/api/canonical-merchants', {
            method: 'POST',
            headers: {'Content-Type':'application/json'},
            body: JSON.stringify({name: document.getElementById('new-merchant-name').value})
          }).then(() => location.reload())
        `}>
        Create
      </button>
      <button
        class="btn btn--sm"
        data-on-click="$newMerchantOpen = false">
        Cancel
      </button>
    </div>

    <table class="cfg-table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Aliases</th>
          <th>Source</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        for _, m := range merchants {
          @cfgMerchantRow(m)
        }
        if len(merchants) == 0 {
          <tr>
            <td colspan="4" class="cfg-table__empty">
              No canonical merchants yet. Import transactions or create one above.
            </td>
          </tr>
        }
      </tbody>
    </table>
  </div>
}

templ cfgMerchantRow(m store.CanonicalMerchantWithCounts) {
  <tr>
    <td>
      <span class="cfg-merchant__name" id={ "merchant-name-" + m.ID }>{ m.Name }</span>
    </td>
    <td>
      <details>
        <summary>{ strconv.Itoa(m.AliasCount) } alias(es)</summary>
        <div id={ "aliases-" + m.ID }
             hx-get={ "/api/canonical-merchants/" + m.ID + "/aliases" }
             hx-trigger="toggle once"
             hx-target={ "#aliases-" + m.ID }
             hx-swap="innerHTML">
          Loading…
        </div>
      </details>
    </td>
    <td>
      <span class={ "badge", templ.KV("badge--engine", m.Source == "engine"), templ.KV("badge--user", m.Source == "user") }>
        { m.Source }
      </span>
    </td>
    <td class="cfg-table__actions">
      <button
        class="btn btn--xs"
        data-on-click={ `
          const newName = prompt('Rename canonical merchant:', '` + m.Name + `');
          if (newName) fetch('/api/canonical-merchants/` + m.ID + `', {
            method: 'PUT',
            headers: {'Content-Type':'application/json'},
            body: JSON.stringify({name: newName})
          }).then(() => location.reload());
        ` }>
        Rename
      </button>
      <button
        class="btn btn--xs btn--danger"
        data-on-click={ `
          if (confirm('Delete "` + m.Name + `"? Aliases will also be removed and entries.reprocess will run.')) {
            fetch('/api/canonical-merchants/` + m.ID + `', {method:'DELETE'}).then(() => location.reload());
          }
        ` }>
        Delete
      </button>
    </td>
  </tr>
}
```

Add `"strconv"` to the import block at the top of `configuration.templ` if not already present.

- [ ] **Step 4: Regenerate the templ Go file**

```bash
cd services/web && templ generate 2>&1
```

Expected: `configuration_templ.go` is regenerated with no errors.

- [ ] **Step 5: Build the full web service**

```bash
cd services/web && go build ./... 2>&1
```

Expected: no errors.

- [ ] **Step 6: Start the service and verify the Merchants tab renders**

```bash
# Adjust to your local dev start command
just dev
```

Navigate to `http://localhost:<port>/configuration?tab=merchants`. Expected: Merchants tab is visible, renders "No canonical merchants yet" when the table is empty. Create a merchant via the "New merchant" button and verify it appears in the list with 0 aliases and `source = user`.

- [ ] **Step 7: Commit**

```bash
git add services/web/page/handler.go \
        services/web/page/configuration.templ \
        services/web/page/configuration_templ.go
git commit -m "feat(web): Merchants tab in configuration page"
```
