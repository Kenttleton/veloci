//! Stage 1: Entry matching (boolean condition trees against transactions).
//!
//! **Input:** `transactions` for an entity; all `active` and `pending_review`
//! `entries` with their JSONB condition trees.
//!
//! **Output:** `transaction_entry_assignments` rows (many-to-many — intentional).
//!
//! ## Algorithm
//!
//! Two-pass iterative matching per transaction (transactions are evaluated in
//! parallel via rayon; the two-pass logic runs sequentially per transaction):
//!
//! **Pass 1:** Evaluate all entries whose conditions contain *only* transaction
//! targets (`payee_*`, `amount_*`, `date_*`, `account`). For each match, record
//! the entry ID and accumulate the entry's `label_id` into the transaction's
//! earned-label set.
//!
//! **Pass 2+:** Iteratively evaluate entries whose conditions contain *entry
//! targets* ([`CompiledConditionTree::LabelMatched`]) against each transaction's
//! accumulated label set. Each iteration applies batched semantics — new labels
//! earned in pass N become available in pass N+1. Iterations continue per
//! transaction until:
//! - stable: no new labels added in the last iteration, or
//! - cycle: an entry matches whose `label_id` is already in the accumulated set
//!   (logged, not an error — intentional for mutual/reflexive label references).
//!
//! 1. Load entries (`status IN ('active', 'pending_review')`, `end_date IS NULL`).
//! 2. Pre-compile JSONB conditions — malformed entries are logged and skipped.
//! 3. Sort compiled entries by `priority ASC`.
//! 4. Partition entries: transaction-target-only → Pass 1; entry-target → Pass 2+.
//! 5. `rayon::par_iter` over transactions — each runs full two-pass evaluation.
//! 6. Each match → one `transaction_entry_assignments` row with `confidence = 1.0`.
//! 7. Collect unmatched transaction IDs for Stage 2.
//! 8. Bulk INSERT assignments (delete-then-insert for idempotency).

use std::collections::HashSet;

use anyhow::{bail, Context, Result};
use chrono::Datelike;
use chrono::NaiveDate;
use rayon::prelude::*;
use regex::Regex;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::Stage1Output;

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
    /// The label this entry applies to a matched transaction (`entries.label_id`).
    /// Accumulated per transaction during the two-pass evaluation.
    pub label_id:   Option<Uuid>,
    pub priority:   i32,
    pub conditions: CompiledConditionTree,
}

/// A compiled, recursively-evaluatable condition tree node.
///
/// Leaf node types mirror the JSONB schema from the spec.
///
/// **Pass 1 targets** (transaction fields — evaluated in Pass 1):
/// `PayeeExact`, `PayeeContains`, `PayeeNotContains`, `PayeeStartsWith`,
/// `PayeeEndsWith`, `PayeeRegex`, `PayeeOneOf`, `AmountRange`,
/// `DateDayOfMonth`, `DateRange`, `AccountId`.
///
/// **Pass 2+ targets** (entry context — only valid in Pass 2+):
/// `LabelMatched`.
///
/// SPEC QUESTION: The spec defines `entry_direction` and `entry_type` as Pass 2+
/// entry targets, but they are not yet implemented as variants. When added,
/// `tree_has_entry_targets` must return `true` for those variants, and `evaluate`
/// must receive the direction/type of Pass 1 matched entries as additional context.
#[derive(Debug)]
pub enum CompiledConditionTree {
    And(Vec<CompiledConditionTree>),
    Or(Vec<CompiledConditionTree>),
    Not(Box<CompiledConditionTree>),
    Xor(Box<CompiledConditionTree>, Box<CompiledConditionTree>),
    // --- Transaction targets (Pass 1) ---
    PayeeExact(String),
    PayeeContains(String),
    PayeeNotContains(String),
    PayeeStartsWith(String),
    PayeeEndsWith(String),
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
    // --- Entry targets (Pass 2+) ---
    /// Matches when the transaction's accumulated label set contains this label UUID.
    ///
    /// SPEC NOTE: The spec JSON key is `"label_matched"` with a `"label"` UUID field.
    /// Existing DB data uses type `"label"` with field `"label_id"`. Both forms are
    /// accepted in `compile_tree` for backward compatibility.
    LabelMatched(Uuid),
}

// ---------------------------------------------------------------------------
// Raw DB row for an entry
// ---------------------------------------------------------------------------

struct EntryRow {
    id:         Uuid,
    label_id:   Option<Uuid>,
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

    // Load active + pending_review entries (pending_review entries are matched
    // so their transactions can be previewed in the UI after a reprocess run).
    let entry_rows = load_entries(entity_id, pool).await?;

    // Pre-compile entries; skip malformed entries (log and continue).
    let mut compiled_entries: Vec<CompiledEntry> = entry_rows
        .into_iter()
        .filter_map(|row| {
            let entry_id = row.id;
            match compile_entry(row) {
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

    tracing::debug!(
        entries = compiled_entries.len(),
        txns    = txns.len(),
        "stage 1: compiled entries, starting two-pass match"
    );

    // Partition entries into Pass 1 (transaction-target-only) and Pass 2+
    // (contains entry targets — evaluated iteratively against accumulated label sets).
    let (txn_entries, entry_entries): (Vec<&CompiledEntry>, Vec<&CompiledEntry>) =
        compiled_entries.iter().partition(|e| !tree_has_entry_targets(&e.conditions));

    // Parallel two-pass evaluation — each transaction is fully independent.
    // Returns (txn_id, matched_entry_ids, was_unmatched) per transaction.
    let results: Vec<(Uuid, Vec<Uuid>, bool)> = txns
        .par_iter()
        .map(|txn| {
            let mut matched: Vec<Uuid> = Vec::new();
            let mut labels: HashSet<Uuid> = HashSet::new();

            // --- Pass 1: transaction-target entries ---
            // Evaluated against transaction fields only. Each match contributes
            // its entry_id to `matched` and its label_id to `labels`.
            for entry in &txn_entries {
                if evaluate(&entry.conditions, txn, &labels) {
                    matched.push(entry.entry_id);
                    if let Some(label_id) = entry.label_id {
                        labels.insert(label_id);
                    }
                }
            }

            // --- Pass 2+: iterative label expansion ---
            //
            // Evaluate entry-target entries against the accumulated label set.
            // Batched semantics: new labels earned in pass N become visible in
            // pass N+1, not mid-pass. This prevents order-dependency within a
            // single iteration.
            //
            // `matched_set` tracks already-assigned entries across passes to
            // prevent duplicate assignments when an entry's conditions remain
            // satisfied in subsequent iterations.
            let mut matched_set: HashSet<Uuid> =
                HashSet::from_iter(matched.iter().copied());

            loop {
                // Snapshot the label set at the start of this pass.
                let pass_labels = labels.clone();
                let mut newly_matched: Vec<Uuid> = Vec::new();
                let mut new_labels: HashSet<Uuid> = HashSet::new();
                let mut cycle_detected = false;

                for entry in &entry_entries {
                    // Skip entries already assigned in a prior pass.
                    if matched_set.contains(&entry.entry_id) {
                        continue;
                    }

                    if evaluate(&entry.conditions, txn, &pass_labels) {
                        if let Some(label_id) = entry.label_id {
                            if pass_labels.contains(&label_id) {
                                // Cycle: this entry would re-add a label already
                                // in the accumulated set. Per spec, log and
                                // terminate expansion for this transaction.
                                //
                                // SPEC QUESTION: The spec does not specify whether
                                // matches collected *before* the cycle entry within
                                // the same pass should be committed. We commit all
                                // pre-cycle matches from this pass and then stop.
                                tracing::info!(
                                    txn_id   = %txn.id,
                                    entry_id = %entry.entry_id,
                                    %label_id,
                                    "stage 1: label cycle detected, terminating expansion for transaction"
                                );
                                cycle_detected = true;
                                break;
                            }
                            new_labels.insert(label_id);
                        }
                        newly_matched.push(entry.entry_id);
                    }
                }

                // Commit newly matched entries from this pass.
                for entry_id in newly_matched {
                    if matched_set.insert(entry_id) {
                        matched.push(entry_id);
                    }
                }
                labels.extend(new_labels);

                // Termination: cycle detected OR label set is stable (no growth).
                if cycle_detected || labels.len() == pass_labels.len() {
                    break;
                }
            }

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

    // Update next_due_date on active entries that received new assignments this
    // run. Only active entries are updated — pending_review entries are excluded
    // by the SQL WHERE clause in update_next_due_dates.
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
// Entry-target classifier
// ---------------------------------------------------------------------------

/// Returns `true` if the tree contains any entry-target leaf nodes.
///
/// Entry-target nodes ([`CompiledConditionTree::LabelMatched`]) reference the
/// accumulated label set for a transaction and can only be evaluated in Pass 2+.
/// Entries whose trees return `true` here are partitioned into the Pass 2+ group
/// and skipped during Pass 1.
///
/// SPEC QUESTION: `entry_direction` and `entry_type` are described in the spec as
/// entry-target nodes but are not yet implemented as `CompiledConditionTree` variants.
/// When added, this function must return `true` for those variants.
fn tree_has_entry_targets(tree: &CompiledConditionTree) -> bool {
    match tree {
        CompiledConditionTree::And(children) | CompiledConditionTree::Or(children) => {
            children.iter().any(tree_has_entry_targets)
        }
        CompiledConditionTree::Not(child) => tree_has_entry_targets(child),
        CompiledConditionTree::Xor(a, b) => {
            tree_has_entry_targets(a) || tree_has_entry_targets(b)
        }
        CompiledConditionTree::LabelMatched(_) => true,
        _ => false,
    }
}

// ---------------------------------------------------------------------------
// Evaluation (pure — no I/O)
// ---------------------------------------------------------------------------

/// Evaluate a compiled condition tree against a transaction.
///
/// `accumulated_labels` is the set of label UUIDs earned by this transaction in
/// prior passes. Pass an empty set during Pass 1; pass the accumulated set during
/// Pass 2+ iterations to enable [`CompiledConditionTree::LabelMatched`] evaluation.
///
/// All `payee_*` string comparisons (except [`CompiledConditionTree::PayeeRegex`])
/// are case-insensitive. `PayeeRegex` case-sensitivity is user-controlled via
/// inline flags (e.g., `(?i)NETFLIX` for case-insensitive).
pub fn evaluate_entry(
    tree:               &CompiledConditionTree,
    txn:                &TransactionRow,
    accumulated_labels: &HashSet<Uuid>,
) -> bool {
    evaluate(tree, txn, accumulated_labels)
}

fn evaluate(
    node:               &CompiledConditionTree,
    txn:                &TransactionRow,
    accumulated_labels: &HashSet<Uuid>,
) -> bool {
    match node {
        CompiledConditionTree::And(children) => {
            children.iter().all(|c| evaluate(c, txn, accumulated_labels))
        }
        CompiledConditionTree::Or(children) => {
            children.iter().any(|c| evaluate(c, txn, accumulated_labels))
        }
        CompiledConditionTree::Not(child) => !evaluate(child, txn, accumulated_labels),
        CompiledConditionTree::Xor(a, b) => {
            evaluate(a, txn, accumulated_labels) ^ evaluate(b, txn, accumulated_labels)
        }

        // --- Payee conditions (all case-insensitive except Regex) ---
        CompiledConditionTree::PayeeExact(s) => {
            txn.merchant_normalized.eq_ignore_ascii_case(s)
        }
        CompiledConditionTree::PayeeContains(s) => txn
            .merchant_normalized
            .to_ascii_lowercase()
            .contains(&s.to_ascii_lowercase()),
        CompiledConditionTree::PayeeNotContains(s) => !txn
            .merchant_normalized
            .to_ascii_lowercase()
            .contains(&s.to_ascii_lowercase()),
        CompiledConditionTree::PayeeStartsWith(s) => txn
            .merchant_normalized
            .to_ascii_lowercase()
            .starts_with(&s.to_ascii_lowercase()),
        CompiledConditionTree::PayeeEndsWith(s) => txn
            .merchant_normalized
            .to_ascii_lowercase()
            .ends_with(&s.to_ascii_lowercase()),
        CompiledConditionTree::PayeeRegex(re) => re.is_match(&txn.merchant_normalized),
        CompiledConditionTree::PayeeOneOf(list) => list
            .iter()
            .any(|s| txn.merchant_normalized.eq_ignore_ascii_case(s)),

        // --- Amount conditions ---
        CompiledConditionTree::AmountRange { min, max } => {
            let a = txn.amount_cents;
            min.map_or(true, |m| a >= m) && max.map_or(true, |m| a <= m)
        }

        // --- Date conditions ---
        CompiledConditionTree::DateDayOfMonth { day, tolerance_days } => {
            let txn_day = txn.date.day() as i32;
            let target  = i32::from(*day);
            let tol     = i32::from(*tolerance_days);
            // Wrap-around month end is not handled (naive — sufficient for matching).
            let diff = (txn_day - target).abs();
            diff <= tol
        }
        CompiledConditionTree::DateRange { start, end } => {
            txn.date >= *start && txn.date <= *end
        }

        // --- Account condition ---
        CompiledConditionTree::AccountId(id) => txn.account_id == *id,

        // --- Entry-target conditions (Pass 2+ only) ---
        //
        // `LabelMatched` evaluates against the accumulated label set, which is
        // empty in Pass 1 (always false there). In Pass 2+ it is populated from
        // prior-pass matches.
        CompiledConditionTree::LabelMatched(label_id) => {
            accumulated_labels.contains(label_id)
        }
    }
}

// ---------------------------------------------------------------------------
// Compilation (pure — returns Err on invalid JSONB)
// ---------------------------------------------------------------------------

fn compile_entry(row: EntryRow) -> Result<CompiledEntry> {
    let conditions = compile_tree(&row.conditions)
        .with_context(|| format!("failed to compile conditions for entry {}", row.id))?;
    Ok(CompiledEntry {
        entry_id:   row.id,
        label_id:   row.label_id,
        priority:   row.priority,
        conditions,
    })
}

/// Compile a JSONB condition tree value into an evaluatable [`CompiledConditionTree`].
///
/// # Supported JSON keys
///
/// **Logical** — `"op"` field with `"AND"` / `"OR"` / `"NOT"` / `"XOR"`, plus
/// `"children"` array.
///
/// **Transaction-target leaves** — `"type"` field:
/// - `"imported_payee_exact"` / `"payee_exact"` → [`CompiledConditionTree::PayeeExact`]
/// - `"imported_payee_contains"` / `"payee_contains"` → [`CompiledConditionTree::PayeeContains`]
/// - `"payee_not_contains"` → [`CompiledConditionTree::PayeeNotContains`]
/// - `"payee_starts_with"` → [`CompiledConditionTree::PayeeStartsWith`]
/// - `"payee_ends_with"` → [`CompiledConditionTree::PayeeEndsWith`]
/// - `"imported_payee_regex"` / `"payee_regex"` → [`CompiledConditionTree::PayeeRegex`]
/// - `"imported_payee_one_of"` → [`CompiledConditionTree::PayeeOneOf`]
/// - `"amount_range"` → [`CompiledConditionTree::AmountRange`]
/// - `"date_day_of_month"` → [`CompiledConditionTree::DateDayOfMonth`]
/// - `"date_range"` → [`CompiledConditionTree::DateRange`]
/// - `"account_id"` / `"account"` → [`CompiledConditionTree::AccountId`]
///
/// **Entry-target leaves** — `"type"` field:
/// - `"label"` (legacy, field: `"label_id"`) → [`CompiledConditionTree::LabelMatched`]
/// - `"label_matched"` (spec, field: `"label"`) → [`CompiledConditionTree::LabelMatched`]
///
/// SPEC QUESTION: The spec defines `"entry_direction"` and `"entry_type"` as
/// entry-target leaf types, but they are not yet implemented as variants. Until
/// they are, an entry containing those types will fail compilation and be skipped
/// with a log warning. This is the safe-fallback behavior (undefined → skip entry).
pub fn compile_tree(v: &serde_json::Value) -> Result<CompiledConditionTree> {
    if let Some(op) = v.get("op").and_then(|o| o.as_str()) {
        // Logical node.
        let children_val = v
            .get("children")
            .and_then(|c| c.as_array())
            .ok_or_else(|| anyhow::anyhow!("logical node missing 'children' array"))?;

        let children: Vec<CompiledConditionTree> = children_val
            .iter()
            .map(compile_tree)
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
        // Payee exact — both legacy ("imported_payee_exact") and spec ("payee_exact") keys.
        "imported_payee_exact" | "payee_exact" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeExact(value))
        }
        // Payee contains — both legacy ("imported_payee_contains") and spec keys.
        "imported_payee_contains" | "payee_contains" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeContains(value))
        }
        // Payee not-contains (spec key only — new condition type).
        "payee_not_contains" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeNotContains(value))
        }
        // Payee starts-with (spec key only — new condition type).
        "payee_starts_with" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeStartsWith(value))
        }
        // Payee ends-with (spec key only — new condition type).
        "payee_ends_with" => {
            let value = string_value(v, "value")?;
            Ok(CompiledConditionTree::PayeeEndsWith(value))
        }
        // Payee regex — both legacy ("imported_payee_regex") and spec ("payee_regex") keys.
        "imported_payee_regex" | "payee_regex" => {
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
        // Account — both legacy ("account_id") and spec ("account") keys.
        "account_id" | "account" => {
            let id_str = string_value(v, "value")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in account leaf: {id_str}"))?;
            Ok(CompiledConditionTree::AccountId(id))
        }
        // LabelMatched — legacy key "label" uses field "label_id".
        "label" => {
            let id_str = string_value(v, "label_id")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in label leaf: {id_str}"))?;
            Ok(CompiledConditionTree::LabelMatched(id))
        }
        // LabelMatched — spec key "label_matched" uses field "label".
        "label_matched" => {
            let id_str = string_value(v, "label")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in label_matched leaf: {id_str}"))?;
            Ok(CompiledConditionTree::LabelMatched(id))
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

/// Load all entries eligible for Stage 1 matching.
///
/// Includes both `active` and `pending_review` entries so that `pending_review`
/// entries receive `transaction_entry_assignments` rows after a reprocess run.
/// Stage 3+ independently filters to `status = 'active'` and is not affected.
async fn load_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<EntryRow>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:         Uuid,
        label_id:   Option<Uuid>,
        priority:   i32,
        conditions: serde_json::Value,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, priority, conditions
        FROM entries
        WHERE entity_id = $1
          AND status IN ('active', 'pending_review')
          AND end_date IS NULL
          AND conditions IS NOT NULL
        ORDER BY priority ASC
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load entries for stage 1")?;

    Ok(rows
        .into_iter()
        .map(|r| EntryRow {
            id:         r.id,
            label_id:   r.label_id,
            priority:   r.priority,
            conditions: r.conditions,
        })
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
    // Only active entries receive next_due_date updates. pending_review entries
    // are excluded — they do not participate in absence detection (Stage 7).
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

    /// Compile a JSONB condition and evaluate with an empty label set.
    ///
    /// Covers all transaction-target conditions (Pass 1 semantics). For
    /// `LabelMatched` tests use `eval_with_labels`.
    fn eval(json: serde_json::Value, txn: &TransactionRow) -> bool {
        let tree = compile_tree(&json).unwrap();
        evaluate(&tree, txn, &HashSet::new())
    }

    /// Compile a JSONB condition and evaluate with the given accumulated label set.
    ///
    /// Used to test Pass 2+ `LabelMatched` conditions.
    fn eval_with_labels(
        json:   serde_json::Value,
        txn:    &TransactionRow,
        labels: &HashSet<Uuid>,
    ) -> bool {
        let tree = compile_tree(&json).unwrap();
        evaluate(&tree, txn, labels)
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
    fn payee_not_contains_case_insensitive() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Amazon REFUND");
        // Contains "REFUND" → not_contains("REFUND") = false.
        assert!(!eval(json!({"type": "payee_not_contains", "value": "REFUND"}), &txn));
        // Case-insensitive: lowercase pattern also matches.
        assert!(!eval(json!({"type": "payee_not_contains", "value": "refund"}), &txn));

        let clean = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Amazon Prime");
        // Does not contain "REFUND" → not_contains("REFUND") = true.
        assert!(eval(json!({"type": "payee_not_contains", "value": "REFUND"}), &clean));
    }

    #[test]
    fn payee_starts_with_case_insensitive() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "NETFLIX.COM");
        assert!(eval(json!({"type": "payee_starts_with", "value": "NETFLIX"}), &txn));
        assert!(eval(json!({"type": "payee_starts_with", "value": "netflix"}), &txn));
        assert!(!eval(json!({"type": "payee_starts_with", "value": ".COM"}), &txn));
    }

    #[test]
    fn payee_ends_with_case_insensitive() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "NETFLIX.COM");
        assert!(eval(json!({"type": "payee_ends_with", "value": ".COM"}), &txn));
        assert!(eval(json!({"type": "payee_ends_with", "value": ".com"}), &txn));
        assert!(!eval(json!({"type": "payee_ends_with", "value": "NETFLIX"}), &txn));
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
        let acct  = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
        let other = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();
        let txn   = make_txn(any_uuid(), acct, "2026-03-15", -1000, "Test");
        assert!(eval(json!({"type": "account_id", "value": acct.to_string()}), &txn));
        assert!(!eval(json!({"type": "account_id", "value": other.to_string()}), &txn));
    }

    #[test]
    fn and_short_circuits_all_must_match() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1500, "Netflix");
        let cond = json!({
            "op": "AND",
            "children": [
                {"type": "imported_payee_exact",  "value": "Netflix"},
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
        let result = compile_tree(&json!({"type": "imported_payee_regex", "value": "[invalid"}));
        assert!(result.is_err(), "invalid regex should return Err");
    }

    #[test]
    fn unknown_op_compile_error() {
        let result = compile_tree(&json!({"op": "NAND", "children": []}));
        assert!(result.is_err());
    }

    #[test]
    fn not_requires_exactly_one_child() {
        let result = compile_tree(&json!({
            "op": "NOT",
            "children": [
                {"type": "imported_payee_exact", "value": "A"},
                {"type": "imported_payee_exact", "value": "B"}
            ]
        }));
        assert!(result.is_err());
    }

    #[test]
    fn xor_requires_exactly_two_children() {
        let result = compile_tree(&json!({
            "op": "XOR",
            "children": [{"type": "imported_payee_exact", "value": "A"}]
        }));
        assert!(result.is_err());
    }

    // Entries sort by priority ascending — lower number runs first.
    #[test]
    fn entries_sort_by_priority() {
        let mut entries = vec![
            CompiledEntry {
                entry_id:   Uuid::nil(),
                label_id:   None,
                priority:   200,
                conditions: CompiledConditionTree::And(vec![]),
            },
            CompiledEntry {
                entry_id:   Uuid::nil(),
                label_id:   None,
                priority:   10,
                conditions: CompiledConditionTree::And(vec![]),
            },
        ];
        entries.sort_by_key(|e| e.priority);
        assert_eq!(entries[0].priority, 10);
        assert_eq!(entries[1].priority, 200);
    }

    /// `LabelMatched` returns false with an empty label set (Pass 1 semantics).
    #[test]
    fn label_matched_false_with_empty_set() {
        let label_id = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Netflix");
        // Legacy key form ("label" + "label_id" field).
        let cond = json!({"type": "label", "label_id": label_id.to_string()});
        assert!(!eval(cond, &txn));
    }

    /// `LabelMatched` (legacy key) returns true when the label is in the accumulated set.
    #[test]
    fn label_matched_true_with_label_in_set() {
        let label_id = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Netflix");
        let cond = json!({"type": "label", "label_id": label_id.to_string()});
        let mut labels = HashSet::new();
        labels.insert(label_id);
        assert!(eval_with_labels(cond, &txn, &labels));
    }

    /// The spec `"label_matched"` key (with `"label"` field) is also accepted.
    #[test]
    fn label_matched_spec_key_accepted() {
        let label_id = Uuid::parse_str("00000000-0000-0000-0000-000000000002").unwrap();
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Test");
        let cond = json!({"type": "label_matched", "label": label_id.to_string()});
        let mut labels = HashSet::new();
        labels.insert(label_id);
        assert!(eval_with_labels(cond, &txn, &labels));
    }

    /// `tree_has_entry_targets` correctly identifies trees with and without `LabelMatched`.
    #[test]
    fn tree_has_entry_targets_detection() {
        let label_id = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();

        let txn_only = CompiledConditionTree::PayeeExact("Netflix".into());
        assert!(!tree_has_entry_targets(&txn_only));

        let entry_target = CompiledConditionTree::LabelMatched(label_id);
        assert!(tree_has_entry_targets(&entry_target));

        let mixed = CompiledConditionTree::And(vec![
            CompiledConditionTree::PayeeContains("NETFLIX".into()),
            CompiledConditionTree::LabelMatched(label_id),
        ]);
        assert!(tree_has_entry_targets(&mixed));

        let nested_txn_only = CompiledConditionTree::Or(vec![
            CompiledConditionTree::PayeeStartsWith("NETFLIX".into()),
            CompiledConditionTree::PayeeEndsWith(".COM".into()),
        ]);
        assert!(!tree_has_entry_targets(&nested_txn_only));
    }

    /// Spec-key aliases ("payee_exact", "payee_contains", "payee_regex") all compile and eval.
    #[test]
    fn spec_key_aliases_compile() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "NETFLIX.COM");
        assert!(eval(json!({"type": "payee_exact",    "value": "NETFLIX.COM"}), &txn));
        assert!(eval(json!({"type": "payee_contains", "value": "NETFLIX"}),    &txn));
        assert!(eval(json!({"type": "payee_regex",    "value": "^NETFLIX"}),   &txn));
    }
}
