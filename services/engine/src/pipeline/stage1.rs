//! Stage 1: Entry matching (boolean condition trees against transactions).
//!
//! **Input:** `transactions` for an entity; all active `entries` with their
//! JSONB condition trees.
//!
//! **Output:** `transaction_entry_assignments` rows (many-to-many — intentional).
//!
//! ## Algorithm
//!
//! 1. Load all active entries from DB (status='active', end_date IS NULL).
//! 2. Pre-compile each entry's JSONB conditions into a [`CompiledEntry`] struct.
//!    Regex patterns are compiled once here; malformed entries are logged and
//!    skipped without aborting the job.
//! 3. Sort `compiled_entries` by `priority ASC`.
//! 4. `rayon::par_iter` over all transactions, evaluate each entry in order.
//! 5. Each match → one `transaction_entry_assignments` row with `confidence = 1.0`.
//! 6. Collect unmatched transaction IDs for Stage 2.
//! 7. Bulk INSERT assignments (delete-then-insert for idempotency).

use std::collections::HashMap;

use anyhow::{bail, Context, Result};
use chrono::Datelike;
use chrono::NaiveDate;
use rayon::prelude::*;
use regex::Regex;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::Stage1Output;

// ---------------------------------------------------------------------------
// Canonical alias map
// ---------------------------------------------------------------------------

/// Map from `merchant_normalized` to `canonical_merchant_id`.
///
/// Loaded once per Stage 1 run and shared (read-only) across rayon worker
/// threads. Used to evaluate [`CompiledConditionTree::CanonicalMerchant`]
/// conditions without any per-transaction DB calls.
pub type CanonicalAliasMap = HashMap<String, Uuid>;

// ---------------------------------------------------------------------------
// Compiled entry tree
// ---------------------------------------------------------------------------

/// An entry with its condition tree pre-compiled for zero-allocation evaluation.
///
/// `regex::Regex` is `Send + Sync` — compiled patterns are shared across
/// rayon worker threads with no cloning or locking.
#[derive(Debug)]
pub struct CompiledEntry {
    pub entry_id:   Uuid,
    pub priority:   i32,
    pub conditions: CompiledConditionTree,
}

/// A compiled, recursively-evaluatable condition tree node.
///
/// Leaf node types mirror the JSONB schema from the spec exactly.
#[derive(Debug)]
pub enum CompiledConditionTree {
    And(Vec<CompiledConditionTree>),
    Or(Vec<CompiledConditionTree>),
    Not(Box<CompiledConditionTree>),
    Xor(Box<CompiledConditionTree>, Box<CompiledConditionTree>),
    PayeeExact(String),
    PayeeContains(String),
    PayeeRegex(Regex),
    PayeeOneOf(Vec<String>),
    AmountRange {
        min: Option<i64>,
        max: Option<i64>,
    },
    DateDayOfMonth {
        day:            u8,
        tolerance_days: u8,
    },
    DateRange {
        start: NaiveDate,
        end:   NaiveDate,
    },
    AccountId(Uuid),
    /// Post-stage only: matches when the transaction has been assigned to any
    /// entry whose `label_id` equals the referenced label UUID.
    LabelMatched(Uuid),
    /// Matches when the transaction's `merchant_normalized` is mapped to the
    /// given canonical merchant UUID in the [`CanonicalAliasMap`].
    CanonicalMerchant(Uuid),
}

// ---------------------------------------------------------------------------
// Raw DB row for an entry
// ---------------------------------------------------------------------------

struct EntryRow {
    id:         Uuid,
    priority:   i32,
    conditions: serde_json::Value,
}

// ---------------------------------------------------------------------------
// Transaction row (loaded once for the full entity)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct TransactionRow {
    id:                  Uuid,
    account_id:          Uuid,
    date:                NaiveDate,
    amount_cents:        i64,
    merchant_normalized: String,
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 1 for an entity: load transactions + entries, compile, match.
pub async fn run(entity_id: Uuid, pool: &PgPool) -> Result<Stage1Output> {
    // Load all transactions for the entity.
    let txns = load_transactions(entity_id, pool).await?;

    // Load all active entries.
    let entry_rows = load_entries(entity_id, pool).await?;

    // Load canonical alias map once — shared across all rayon workers below.
    let canonical_aliases = load_canonical_aliases(pool).await?;

    // Pre-compile entries; skip malformed entries (log and continue).
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

    // Sort by priority ascending.
    compiled_entries.sort_by_key(|e| e.priority);

    tracing::debug!(entries = compiled_entries.len(), txns = txns.len(), "stage 1: compiled entries, starting match");

    // Parallel match: each transaction is independent.
    // Returns (matched_entry_ids, was_unmatched) per transaction.
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

    // Persist assignments — idempotent: delete existing for this entity first.
    persist_assignments(entity_id, &results, pool).await?;

    // Update next_due_date on entries that received new assignments this run.
    // next_due_date = max(matched_tx.date) + period_days; used by Stage 7 for
    // absence detection. Stage 2 handles this for newly created entries.
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

// ---------------------------------------------------------------------------
// Evaluation (pure — no I/O)
// ---------------------------------------------------------------------------

/// Evaluate a compiled condition tree against a transaction.
///
/// `aliases` maps `merchant_normalized` → `canonical_merchant_id` and is used
/// to evaluate [`CompiledConditionTree::CanonicalMerchant`] leaves.
pub fn evaluate_entry(
    tree:    &CompiledConditionTree,
    txn:     &TransactionRow,
    aliases: &CanonicalAliasMap,
) -> bool {
    evaluate(tree, txn, aliases)
}

fn evaluate(node: &CompiledConditionTree, txn: &TransactionRow, aliases: &CanonicalAliasMap) -> bool {
    match node {
        CompiledConditionTree::And(children) => children.iter().all(|c| evaluate(c, txn, aliases)),
        CompiledConditionTree::Or(children)  => children.iter().any(|c| evaluate(c, txn, aliases)),
        CompiledConditionTree::Not(child)    => !evaluate(child, txn, aliases),
        CompiledConditionTree::Xor(a, b)    => evaluate(a, txn, aliases) ^ evaluate(b, txn, aliases),

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
            // Wrap around month end (naive — good enough for matching purposes).
            let diff = (txn_day - target).abs();
            diff <= tol
        }

        CompiledConditionTree::DateRange { start, end } => {
            txn.date >= *start && txn.date <= *end
        }

        CompiledConditionTree::AccountId(id) => txn.account_id == *id,

        // LabelMatched is for classification rules (post-stage): evaluated after
        // pre-stage entry assignments are accumulated. In the current single-pass
        // implementation this always returns false during transaction matching.
        CompiledConditionTree::LabelMatched(_label_id) => false,

        // CanonicalMerchant: look up the transaction's merchant_normalized in the
        // alias map. True only when the mapped canonical_merchant_id equals the
        // condition's canonical_merchant_id.
        CompiledConditionTree::CanonicalMerchant(canonical_id) => {
            aliases
                .get(&txn.merchant_normalized)
                .map_or(false, |id| id == canonical_id)
        }
    }
}

// ---------------------------------------------------------------------------
// Compilation (pure — can panic on invalid JSONB, returns Result)
// ---------------------------------------------------------------------------

fn compile_entry(row: EntryRow, aliases: &CanonicalAliasMap) -> Result<CompiledEntry> {
    let conditions = compile_tree(&row.conditions, aliases)
        .with_context(|| format!("failed to compile conditions for entry {}", row.id))?;
    Ok(CompiledEntry {
        entry_id: row.id,
        priority: row.priority,
        conditions,
    })
}

/// Compile a JSONB condition tree value into an evaluatable [`CompiledConditionTree`].
///
/// `aliases` is threaded through but not consumed during compilation — it is
/// only used at evaluation time via [`evaluate_entry`].
pub fn compile_tree(v: &serde_json::Value, aliases: &CanonicalAliasMap) -> Result<CompiledConditionTree> {
    if let Some(op) = v.get("op").and_then(|o| o.as_str()) {
        // Logical node.
        let children_val = v
            .get("children")
            .and_then(|c| c.as_array())
            .ok_or_else(|| anyhow::anyhow!("logical node missing 'children' array"))?;

        let children: Vec<CompiledConditionTree> = children_val
            .iter()
            .map(|c| compile_tree(c, aliases))
            .collect::<Result<_>>()?;

        return match op {
            "AND" => Ok(CompiledConditionTree::And(children)),
            "OR"  => Ok(CompiledConditionTree::Or(children)),
            "NOT" => {
                if children.len() != 1 {
                    bail!("NOT node requires exactly 1 child, got {}", children.len());
                }
                let mut it = children.into_iter();
                Ok(CompiledConditionTree::Not(Box::new(it.next().unwrap())))
            }
            "XOR" => {
                if children.len() != 2 {
                    bail!("XOR node requires exactly 2 children, got {}", children.len());
                }
                let mut it = children.into_iter();
                Ok(CompiledConditionTree::Xor(
                    Box::new(it.next().unwrap()),
                    Box::new(it.next().unwrap()),
                ))
            }
            other => bail!("unknown logical op: {other}"),
        };
    }

    // Leaf node.
    let leaf_type = v
        .get("type")
        .and_then(|t| t.as_str())
        .ok_or_else(|| anyhow::anyhow!("leaf node missing 'type' field"))?;

    match leaf_type {
        "imported_payee_exact" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeExact(value))
        }
        "imported_payee_contains" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeContains(value))
        }
        "imported_payee_regex" => {
            let pattern = string_value(v, "value")?;
            let re = Regex::new(&pattern)
                .with_context(|| format!("invalid regex pattern: {pattern}"))?;
            Ok(CompiledConditionTree::PayeeRegex(re))
        }
        "imported_payee_one_of" => {
            let arr = v
                .get("value")
                .and_then(|v| v.as_array())
                .ok_or_else(|| anyhow::anyhow!("imported_payee_one_of missing array 'value'"))?;
            let list: Vec<String> = arr
                .iter()
                .filter_map(|e| e.as_str().map(str::to_string))
                .collect();
            Ok(CompiledConditionTree::PayeeOneOf(list))
        }
        "amount_range" => {
            let min = v.get("min_cents").and_then(|v| v.as_i64());
            let max = v.get("max_cents").and_then(|v| v.as_i64());
            Ok(CompiledConditionTree::AmountRange { min, max })
        }
        "date_day_of_month" => {
            let day = v
                .get("day")
                .and_then(|d| d.as_u64())
                .ok_or_else(|| anyhow::anyhow!("date_day_of_month missing 'day'"))? as u8;
            let tolerance_days = v
                .get("tolerance_days")
                .and_then(|t| t.as_u64())
                .unwrap_or(0) as u8;
            Ok(CompiledConditionTree::DateDayOfMonth {
                day,
                tolerance_days,
            })
        }
        "date_range" => {
            let start_str = string_value(v, "start")?;
            let end_str   = string_value(v, "end")?;
            let start = NaiveDate::parse_from_str(&start_str, "%Y-%m-%d")
                .with_context(|| format!("invalid date_range start: {start_str}"))?;
            let end = NaiveDate::parse_from_str(&end_str, "%Y-%m-%d")
                .with_context(|| format!("invalid date_range end: {end_str}"))?;
            Ok(CompiledConditionTree::DateRange { start, end })
        }
        "account_id" => {
            let id_str = string_value(v, "value")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in account_id leaf: {id_str}"))?;
            Ok(CompiledConditionTree::AccountId(id))
        }
        "label" => {
            let id_str = string_value(v, "label_id")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in label leaf: {id_str}"))?;
            Ok(CompiledConditionTree::LabelMatched(id))
        }
        "canonical_merchant" => {
            let id_str = string_value(v, "canonical_merchant_id")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in canonical_merchant leaf: {id_str}"))?;
            Ok(CompiledConditionTree::CanonicalMerchant(id))
        }
        other => bail!("unknown leaf type: {other}"),
    }
}

fn string_value(v: &serde_json::Value, key: &str) -> Result<String> {
    v.get(key)
        .and_then(|s| s.as_str())
        .map(str::to_string)
        .ok_or_else(|| anyhow::anyhow!("missing string field '{key}' in condition node"))
}

// ---------------------------------------------------------------------------
// DB loaders
// ---------------------------------------------------------------------------

async fn load_transactions(entity_id: Uuid, pool: &PgPool) -> Result<Vec<TransactionRow>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                  Uuid,
        account_id:          Uuid,
        date:                NaiveDate,
        amount_cents:        i64,
        merchant_normalized: String,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, account_id, date, amount_cents, merchant_normalized
        FROM transactions
        WHERE entity_id = $1
        ORDER BY date ASC
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load transactions for stage 1")?;

    Ok(rows
        .into_iter()
        .map(|r| TransactionRow {
            id:                  r.id,
            account_id:          r.account_id,
            date:                r.date,
            amount_cents:        r.amount_cents,
            merchant_normalized: r.merchant_normalized,
        })
        .collect())
}

async fn load_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<EntryRow>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:         Uuid,
        priority:   i32,
        conditions: serde_json::Value,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, priority, conditions
        FROM entries
        WHERE entity_id = $1
          AND status = 'active'
          AND end_date IS NULL
          AND conditions IS NOT NULL
        ORDER BY priority ASC
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load active entries for stage 1")?;

    Ok(rows
        .into_iter()
        .map(|r| EntryRow {
            id:         r.id,
            priority:   r.priority,
            conditions: r.conditions,
        })
        .collect())
}

/// Load the full `canonical_merchant_aliases` table as a `CanonicalAliasMap`.
///
/// The result is read-only after loading and shared across rayon workers
/// during evaluation. Loading the entire table is intentional: entries are
/// global, not scoped to a single entity.
pub async fn load_canonical_aliases(pool: &PgPool) -> Result<CanonicalAliasMap> {
    #[derive(sqlx::FromRow)]
    struct Row {
        normalized_name:       String,
        canonical_merchant_id: Uuid,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT normalized_name, canonical_merchant_id FROM canonical_merchant_aliases",
    )
    .fetch_all(pool)
    .await
    .context("failed to load canonical_merchant_aliases for stage 1")?;

    Ok(rows
        .into_iter()
        .map(|r| (r.normalized_name, r.canonical_merchant_id))
        .collect())
}

// ---------------------------------------------------------------------------
// Persist assignments
// ---------------------------------------------------------------------------

async fn persist_assignments(
    entity_id: Uuid,
    results: &[(Uuid, Vec<Uuid>, bool)],
    pool: &PgPool,
) -> Result<()> {
    // Delete all existing assignments for transactions of this entity.
    // This makes Stage 1 idempotent on re-runs (entries.reprocess).
    sqlx::query(
        r#"
        DELETE FROM transaction_entry_assignments
        WHERE transaction_id IN (
            SELECT id FROM transactions WHERE entity_id = $1
        )
        "#,
    )
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to delete existing assignments")?;

    // Collect all (transaction_id, entry_id) pairs.
    let mut txn_ids:   Vec<Uuid> = Vec::new();
    let mut entry_ids: Vec<Uuid> = Vec::new();

    for (txn_id, matched_entries, _) in results {
        for entry_id in matched_entries {
            txn_ids.push(*txn_id);
            entry_ids.push(*entry_id);
        }
    }

    if txn_ids.is_empty() {
        return Ok(());
    }

    sqlx::query(
        r#"
        INSERT INTO transaction_entry_assignments (transaction_id, entry_id, confidence)
        SELECT t, e, 1.0
        FROM UNNEST($1::uuid[], $2::uuid[]) AS u(t, e)
        ON CONFLICT (transaction_id, entry_id) DO NOTHING
        "#,
    )
    .bind(&txn_ids)
    .bind(&entry_ids)
    .execute(pool)
    .await
    .context("failed to insert entry assignments")?;

    Ok(())
}

async fn update_next_due_dates(
    entity_id: Uuid,
    entry_ids: &[Uuid],
    pool: &PgPool,
) -> Result<()> {
    if entry_ids.is_empty() {
        return Ok(());
    }
    sqlx::query(
        r#"
        UPDATE entries e
        SET next_due_date = (
            SELECT (MAX(t.date) + (e.period_days * INTERVAL '1 day'))::date
            FROM transaction_entry_assignments tea
            JOIN transactions t ON t.id = tea.transaction_id
            WHERE tea.entry_id = e.id
        )
        WHERE e.id = ANY($1)
          AND e.entity_id = $2
          AND e.status = 'active'
        "#,
    )
    .bind(entry_ids)
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to update next_due_date for matched entries")?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use serde_json::json;

    fn make_txn(
        id: Uuid,
        account_id: Uuid,
        date: &str,
        amount_cents: i64,
        merchant: &str,
    ) -> TransactionRow {
        TransactionRow {
            id,
            account_id,
            date: NaiveDate::parse_from_str(date, "%Y-%m-%d").unwrap(),
            amount_cents,
            merchant_normalized: merchant.to_string(),
        }
    }

    fn any_uuid() -> Uuid {
        Uuid::nil()
    }

    // Helper: compile a JSONB condition and evaluate with an empty alias map.
    fn eval(json: serde_json::Value, txn: &TransactionRow) -> bool {
        let aliases = CanonicalAliasMap::default();
        let tree = compile_tree(&json, &aliases).unwrap();
        evaluate(&tree, txn, &aliases)
    }

    #[test]
    fn payee_exact_case_insensitive() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        assert!(eval(json!({"type": "imported_payee_exact", "value": "NETFLIX"}), &txn));
        assert!(eval(json!({"type": "imported_payee_exact", "value": "netflix"}), &txn));
        assert!(!eval(json!({"type": "imported_payee_exact", "value": "Hulu"}), &txn));
    }

    #[test]
    fn payee_contains_substring() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Amazon Prime");
        assert!(eval(json!({"type": "imported_payee_contains", "value": "AMAZON"}), &txn));
        assert!(!eval(json!({"type": "imported_payee_contains", "value": "NETFLIX"}), &txn));
    }

    #[test]
    fn payee_regex_matches() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -999, "Amz-Prime-123");
        assert!(eval(json!({"type": "imported_payee_regex", "value": "^Amz"}), &txn));
        assert!(!eval(json!({"type": "imported_payee_regex", "value": "^Netflix"}), &txn));
    }

    #[test]
    fn payee_one_of() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Hulu");
        let cond = json!({"type": "imported_payee_one_of", "value": ["Netflix", "Hulu", "Disney"]});
        assert!(eval(cond.clone(), &txn));
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Spotify");
        assert!(!eval(cond, &txn2));
    }

    #[test]
    fn amount_range_both_bounds() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Test");
        let cond = json!({"type": "amount_range", "min_cents": -2000, "max_cents": -1000});
        assert!(eval(cond.clone(), &txn));
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-01", -500, "Test");
        assert!(!eval(cond, &txn2));
    }

    #[test]
    fn amount_range_open_bounds() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -99999, "Test");
        assert!(eval(json!({"type": "amount_range"}), &txn)); // no bounds = match all
        assert!(eval(json!({"type": "amount_range", "min_cents": -100000}), &txn));
    }

    #[test]
    fn date_day_of_month_exact() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-15", -1000, "Test");
        assert!(eval(json!({"type": "date_day_of_month", "day": 15, "tolerance_days": 0}), &txn));
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-13", -1000, "Test");
        assert!(eval(json!({"type": "date_day_of_month", "day": 15, "tolerance_days": 2}), &txn2));
        let txn3 = make_txn(any_uuid(), any_uuid(), "2026-03-10", -1000, "Test");
        assert!(!eval(json!({"type": "date_day_of_month", "day": 15, "tolerance_days": 2}), &txn3));
    }

    #[test]
    fn date_range_in_and_out() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-15", -1000, "Test");
        let cond = json!({"type": "date_range", "start": "2026-03-01", "end": "2026-03-31"});
        assert!(eval(cond.clone(), &txn));
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-04-01", -1000, "Test");
        assert!(!eval(cond, &txn2));
    }

    #[test]
    fn account_id_leaf() {
        let acct = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
        let other = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();
        let txn = make_txn(any_uuid(), acct, "2026-03-15", -1000, "Test");
        assert!(eval(json!({"type": "account_id", "value": acct.to_string()}), &txn));
        assert!(!eval(json!({"type": "account_id", "value": other.to_string()}), &txn));
    }

    #[test]
    fn and_short_circuits_all_must_match() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        let cond = json!({
            "op": "AND",
            "children": [
                {"type": "imported_payee_exact", "value": "Netflix"},
                {"type": "amount_range", "min_cents": -2000, "max_cents": -1000}
            ]
        });
        assert!(eval(cond.clone(), &txn));
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-01", -500, "Netflix");
        assert!(!eval(cond, &txn2));
    }

    #[test]
    fn or_any_child_matches() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Hulu");
        let cond = json!({
            "op": "OR",
            "children": [
                {"type": "imported_payee_exact", "value": "Netflix"},
                {"type": "imported_payee_exact", "value": "Hulu"}
            ]
        });
        assert!(eval(cond, &txn));
    }

    #[test]
    fn not_inverts() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Netflix");
        let cond = json!({
            "op": "NOT",
            "children": [{"type": "imported_payee_exact", "value": "Hulu"}]
        });
        assert!(eval(cond, &txn));
    }

    #[test]
    fn xor_exclusive() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-15", -1500, "Netflix");
        let cond_both = json!({
            "op": "XOR",
            "children": [
                {"type": "imported_payee_exact", "value": "Netflix"},
                {"type": "amount_range", "min_cents": -2000, "max_cents": -1000}
            ]
        });
        assert!(!eval(cond_both, &txn));
        let cond_one = json!({
            "op": "XOR",
            "children": [
                {"type": "imported_payee_exact", "value": "Netflix"},
                {"type": "imported_payee_exact", "value": "Hulu"}
            ]
        });
        assert!(eval(cond_one, &txn));
    }

    #[test]
    fn malformed_regex_compile_error() {
        let aliases = CanonicalAliasMap::default();
        let result = compile_tree(&json!({"type": "imported_payee_regex", "value": "[invalid"}), &aliases);
        assert!(result.is_err(), "invalid regex should return Err");
    }

    #[test]
    fn unknown_op_compile_error() {
        let aliases = CanonicalAliasMap::default();
        let result = compile_tree(&json!({"op": "NAND", "children": []}), &aliases);
        assert!(result.is_err());
    }

    #[test]
    fn not_requires_exactly_one_child() {
        let aliases = CanonicalAliasMap::default();
        let result = compile_tree(&json!({
            "op": "NOT",
            "children": [
                {"type": "imported_payee_exact", "value": "A"},
                {"type": "imported_payee_exact", "value": "B"}
            ]
        }), &aliases);
        assert!(result.is_err());
    }

    #[test]
    fn xor_requires_exactly_two_children() {
        let aliases = CanonicalAliasMap::default();
        let result = compile_tree(&json!({
            "op": "XOR",
            "children": [{"type": "imported_payee_exact", "value": "A"}]
        }), &aliases);
        assert!(result.is_err());
    }

    // Entries sort by priority ascending — lower number runs first.
    #[test]
    fn entries_sort_by_priority() {
        let mut entries = vec![
            CompiledEntry {
                entry_id:   Uuid::nil(),
                priority:   200,
                conditions: CompiledConditionTree::And(vec![]),
            },
            CompiledEntry {
                entry_id:   Uuid::nil(),
                priority:   10,
                conditions: CompiledConditionTree::And(vec![]),
            },
        ];
        entries.sort_by_key(|e| e.priority);
        assert_eq!(entries[0].priority, 10);
        assert_eq!(entries[1].priority, 200);
    }

    /// `canonical_merchant` leaf compiles and evaluates via alias map lookup.
    #[test]
    fn canonical_merchant_leaf_matches_via_alias_map() {
        let canonical_id = Uuid::parse_str("00000000-0000-0000-0000-000000000099").unwrap();
        let mut aliases = CanonicalAliasMap::default();
        aliases.insert("Netflix".to_string(), canonical_id);

        let cond_json = json!({
            "type": "canonical_merchant",
            "canonical_merchant_id": canonical_id.to_string()
        });
        let tree = compile_tree(&cond_json, &aliases).unwrap();

        // Transaction whose merchant_normalized is aliased to canonical_id → match.
        let txn_match = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        assert!(evaluate_entry(&tree, &txn_match, &aliases));

        // Transaction whose merchant_normalized maps to a different canonical → no match.
        let other_canonical = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();
        let mut aliases2 = CanonicalAliasMap::default();
        aliases2.insert("Hulu".to_string(), other_canonical);
        let txn_other = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Hulu");
        assert!(!evaluate_entry(&tree, &txn_other, &aliases));

        // Transaction with no alias → no match.
        let txn_unknown = make_txn(any_uuid(), any_uuid(), "2026-03-01", -900, "UnknownStore");
        assert!(!evaluate_entry(&tree, &txn_unknown, &aliases));
    }

    /// `canonical_merchant` with an empty alias map never matches.
    #[test]
    fn canonical_merchant_no_aliases_never_matches() {
        let canonical_id = Uuid::parse_str("00000000-0000-0000-0000-000000000099").unwrap();
        let aliases = CanonicalAliasMap::default();
        let cond_json = json!({
            "type": "canonical_merchant",
            "canonical_merchant_id": canonical_id.to_string()
        });
        let tree = compile_tree(&cond_json, &aliases).unwrap();
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        assert!(!evaluate_entry(&tree, &txn, &aliases));
    }

    /// `canonical_merchant` embedded in an AND tree still evaluates correctly.
    #[test]
    fn canonical_merchant_in_and_tree() {
        let canonical_id = Uuid::parse_str("00000000-0000-0000-0000-000000000099").unwrap();
        let mut aliases = CanonicalAliasMap::default();
        aliases.insert("Netflix".to_string(), canonical_id);

        let cond_json = json!({
            "op": "AND",
            "children": [
                {
                    "type": "canonical_merchant",
                    "canonical_merchant_id": canonical_id.to_string()
                },
                {
                    "type": "amount_range",
                    "min_cents": -2000,
                    "max_cents": -1000
                }
            ]
        });
        let tree = compile_tree(&cond_json, &aliases).unwrap();

        // Matches: correct canonical AND amount in range.
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        assert!(evaluate_entry(&tree, &txn, &aliases));

        // Amount out of range → no match.
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-01", -500, "Netflix");
        assert!(!evaluate_entry(&tree, &txn2, &aliases));
    }
}
