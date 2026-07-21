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

use crate::pipeline::types::{Direction, EntryType, Stage1Output};

// ---------------------------------------------------------------------------
// Entry source — origin of an entry record
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub(crate) enum EntrySource {
    User,
    Engine,
}

impl EntrySource {
    fn from_str(s: &str) -> Option<Self> {
        match s {
            "user"   => Some(Self::User),
            "engine" => Some(Self::Engine),
            _        => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Confidence gate — inclusive range filter for a single confidence score
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct ConfidenceGate {
    min: Option<f64>,
    max: Option<f64>,
}

impl ConfidenceGate {
    fn matches(&self, value: f64) -> bool {
        self.min.map_or(true, |m| value >= m) && self.max.map_or(true, |m| value <= m)
    }
}

// ---------------------------------------------------------------------------
// Accumulated entry metadata — per-matched-entry context carried through passes
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct AccumulatedEntryMeta {
    label_id:               Option<Uuid>,
    direction:              Direction,
    entry_type:             EntryType,
    period_days:            i32,
    source:                 EntrySource,
    confidence:             Option<f64>,
    merchant_confidence:    Option<f64>,
    timing_confidence:      Option<f64>,
    amount_confidence:      Option<f64>,
    projected_rate_per_day: Option<f64>,
    recurrence_anchor:      Option<String>,
}

// ---------------------------------------------------------------------------
// Compiled entry tree
// ---------------------------------------------------------------------------

/// An entry with its condition tree pre-compiled for zero-allocation evaluation.
///
/// `regex::Regex` is `Send + Sync` — compiled patterns are shared across
/// rayon worker threads with no cloning or locking.
#[derive(Debug)]
pub struct CompiledEntry {
    pub entry_id:               Uuid,
    /// The label this entry applies to a matched transaction (`entries.label_id`).
    /// Accumulated per transaction during the two-pass evaluation.
    pub label_id:               Option<Uuid>,
    pub priority:               i32,
    pub conditions:             CompiledConditionTree,
    // Fields carried into Pass 2+ entry-target evaluation.
    pub direction:              Direction,
    pub entry_type:             EntryType,
    pub period_days:            i32,
    pub source:                 EntrySource,
    pub confidence:             Option<f64>,
    pub merchant_confidence:    Option<f64>,
    pub timing_confidence:      Option<f64>,
    pub amount_confidence:      Option<f64>,
    pub projected_rate_per_day: Option<f64>,
    pub recurrence_anchor:      Option<String>,
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
/// **Pass 2+ targets** (entry context — evaluated against accumulated matched-entry
/// metadata): `LabelMatched`, `EntryDirection`, `EntryType`, `EntryPeriod`,
/// `EntrySource`, `EntryConfidence`, `EntryProjectedRate`, `EntryRecurrenceAnchor`.
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
    /// DB format (written by Go's ResolveConditions): `{ "type": "label_matched", "label_id": "<uuid>" }`.
    /// The `"label"` field (human-readable name) lives only in the enriched display layer.
    LabelMatched(Uuid),
    /// Matches when any accumulated entry has the specified direction.
    EntryDirection(Direction),
    /// Matches when any accumulated entry has the specified entry type.
    EntryType(EntryType),
    /// Matches when any accumulated entry's period_days falls within [min_days, max_days].
    EntryPeriod {
        min_days: Option<i32>,
        max_days: Option<i32>,
    },
    /// Matches when any accumulated entry has the specified source.
    EntrySource(EntrySource),
    /// Matches when any single accumulated entry satisfies ALL specified confidence gates.
    EntryConfidence {
        overall:  Option<ConfidenceGate>,
        merchant: Option<ConfidenceGate>,
        timing:   Option<ConfidenceGate>,
        amount:   Option<ConfidenceGate>,
    },
    /// Matches when any accumulated entry's projected_rate_per_day falls within [min, max].
    EntryProjectedRate {
        min: Option<f64>,
        max: Option<f64>,
    },
    /// Matches when any accumulated entry's recurrence_anchor equals the given string.
    EntryRecurrenceAnchor(String),
}

// ---------------------------------------------------------------------------
// Raw DB row for an entry
// ---------------------------------------------------------------------------

struct EntryRow {
    id:                     Uuid,
    label_id:               Option<Uuid>,
    priority:               i32,
    conditions:             serde_json::Value,
    direction:              String,
    entry_type:             String,
    period_days:            i32,
    source:                 String,
    confidence:             Option<f64>,
    merchant_confidence:    Option<f64>,
    timing_confidence:      Option<f64>,
    amount_confidence:      Option<f64>,
    projected_rate_per_day: Option<f64>,
    recurrence_anchor:      Option<String>,
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
            let mut accumulated: Vec<AccumulatedEntryMeta> = Vec::new();
            let mut label_index: HashSet<Uuid> = HashSet::new();

            // --- Pass 1: transaction-target entries ---
            // Evaluated against transaction fields only. Each match contributes
            // its entry_id to `matched`, its label_id to `label_index`, and its
            // full metadata to `accumulated`.
            for entry in &txn_entries {
                if evaluate(&entry.conditions, txn, &label_index, &accumulated) {
                    matched.push(entry.entry_id);
                    if let Some(label_id) = entry.label_id {
                        label_index.insert(label_id);
                    }
                    accumulated.push(AccumulatedEntryMeta {
                        label_id:               entry.label_id,
                        direction:              entry.direction,
                        entry_type:             entry.entry_type,
                        period_days:            entry.period_days,
                        source:                 entry.source,
                        confidence:             entry.confidence,
                        merchant_confidence:    entry.merchant_confidence,
                        timing_confidence:      entry.timing_confidence,
                        amount_confidence:      entry.amount_confidence,
                        projected_rate_per_day: entry.projected_rate_per_day,
                        recurrence_anchor:      entry.recurrence_anchor.clone(),
                    });
                }
            }

            // --- Pass 2+: iterative expansion via entry-target conditions ---
            //
            // Evaluate entry-target entries against the accumulated metadata.
            // Batched semantics: metadata accumulated in pass N becomes visible in
            // pass N+1, not mid-pass. This prevents order-dependency within a
            // single iteration.
            //
            // `matched_set` tracks already-assigned entries across passes to
            // prevent duplicate assignments when an entry's conditions remain
            // satisfied in subsequent iterations.
            let mut matched_set: HashSet<Uuid> =
                HashSet::from_iter(matched.iter().copied());

            loop {
                // Snapshot the accumulated state at the start of this pass.
                let pass_accumulated = accumulated.clone();
                let pass_label_index = label_index.clone();
                let mut newly_matched: Vec<Uuid> = Vec::new();
                let mut new_meta: Vec<AccumulatedEntryMeta> = Vec::new();
                let mut new_labels: HashSet<Uuid> = HashSet::new();
                let mut cycle_detected = false;

                for entry in &entry_entries {
                    // Skip entries already assigned in a prior pass.
                    if matched_set.contains(&entry.entry_id) {
                        continue;
                    }

                    if evaluate(&entry.conditions, txn, &pass_label_index, &pass_accumulated) {
                        if let Some(label_id) = entry.label_id {
                            if pass_label_index.contains(&label_id) {
                                // Cycle: this entry would re-add a label already
                                // in the accumulated set. Per spec, log and
                                // terminate expansion for this transaction.
                                // Pre-cycle matches from this pass are committed.
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
                        new_meta.push(AccumulatedEntryMeta {
                            label_id:               entry.label_id,
                            direction:              entry.direction,
                            entry_type:             entry.entry_type,
                            period_days:            entry.period_days,
                            source:                 entry.source,
                            confidence:             entry.confidence,
                            merchant_confidence:    entry.merchant_confidence,
                            timing_confidence:      entry.timing_confidence,
                            amount_confidence:      entry.amount_confidence,
                            projected_rate_per_day: entry.projected_rate_per_day,
                            recurrence_anchor:      entry.recurrence_anchor.clone(),
                        });
                    }
                }

                // Commit newly matched entries from this pass.
                for entry_id in newly_matched {
                    if matched_set.insert(entry_id) {
                        matched.push(entry_id);
                    }
                }
                label_index.extend(new_labels);
                accumulated.extend(new_meta);

                // Termination: cycle detected OR accumulated set is stable (no growth).
                if cycle_detected || accumulated.len() == pass_accumulated.len() {
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
/// Entry-target nodes reference the accumulated matched-entry metadata for a
/// transaction and can only be evaluated in Pass 2+. Entries whose trees return
/// `true` here are partitioned into the Pass 2+ group and skipped during Pass 1.
fn tree_has_entry_targets(tree: &CompiledConditionTree) -> bool {
    match tree {
        CompiledConditionTree::And(children) | CompiledConditionTree::Or(children) => {
            children.iter().any(tree_has_entry_targets)
        }
        CompiledConditionTree::Not(child) => tree_has_entry_targets(child),
        CompiledConditionTree::Xor(a, b) => {
            tree_has_entry_targets(a) || tree_has_entry_targets(b)
        }
        CompiledConditionTree::LabelMatched(_)
        | CompiledConditionTree::EntryDirection(_)
        | CompiledConditionTree::EntryType(_)
        | CompiledConditionTree::EntryPeriod { .. }
        | CompiledConditionTree::EntrySource(_)
        | CompiledConditionTree::EntryConfidence { .. }
        | CompiledConditionTree::EntryProjectedRate { .. }
        | CompiledConditionTree::EntryRecurrenceAnchor(_) => true,
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
/// `accumulated` carries the full metadata for every entry matched in prior passes,
/// enabling Pass 2+ entry-target conditions (`EntryDirection`, `EntryType`, etc.).
///
/// All `payee_*` string comparisons (except [`CompiledConditionTree::PayeeRegex`])
/// are case-insensitive. `PayeeRegex` case-sensitivity is user-controlled via
/// inline flags (e.g., `(?i)NETFLIX` for case-insensitive).
pub fn evaluate_entry(
    tree:               &CompiledConditionTree,
    txn:                &TransactionRow,
    accumulated_labels: &HashSet<Uuid>,
    accumulated:        &[AccumulatedEntryMeta],
) -> bool {
    evaluate(tree, txn, accumulated_labels, accumulated)
}

fn evaluate(
    node:          &CompiledConditionTree,
    txn:           &TransactionRow,
    label_index:   &HashSet<Uuid>,
    accumulated:   &[AccumulatedEntryMeta],
) -> bool {
    match node {
        CompiledConditionTree::And(children) => {
            children.iter().all(|c| evaluate(c, txn, label_index, accumulated))
        }
        CompiledConditionTree::Or(children) => {
            children.iter().any(|c| evaluate(c, txn, label_index, accumulated))
        }
        CompiledConditionTree::Not(child) => !evaluate(child, txn, label_index, accumulated),
        CompiledConditionTree::Xor(a, b) => {
            evaluate(a, txn, label_index, accumulated) ^ evaluate(b, txn, label_index, accumulated)
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
        // All of these evaluate against `accumulated`, the list of metadata for
        // entries already matched in prior passes. They are always false in Pass 1
        // (empty accumulated slice).
        CompiledConditionTree::LabelMatched(label_id) => label_index.contains(label_id),

        CompiledConditionTree::EntryDirection(dir) => {
            accumulated.iter().any(|e| e.direction == *dir)
        }

        CompiledConditionTree::EntryType(et) => {
            accumulated.iter().any(|e| e.entry_type == *et)
        }

        CompiledConditionTree::EntryPeriod { min_days, max_days } => {
            accumulated.iter().any(|e| {
                min_days.map_or(true, |m| e.period_days >= m)
                    && max_days.map_or(true, |m| e.period_days <= m)
            })
        }

        CompiledConditionTree::EntrySource(src) => {
            accumulated.iter().any(|e| e.source == *src)
        }

        // All specified gates must be satisfied by the SAME accumulated entry.
        CompiledConditionTree::EntryConfidence { overall, merchant, timing, amount } => {
            accumulated.iter().any(|e| {
                overall.as_ref().map_or(true, |g| {
                    e.confidence.map_or(false, |v| g.matches(v))
                }) && merchant.as_ref().map_or(true, |g| {
                    e.merchant_confidence.map_or(false, |v| g.matches(v))
                }) && timing.as_ref().map_or(true, |g| {
                    e.timing_confidence.map_or(false, |v| g.matches(v))
                }) && amount.as_ref().map_or(true, |g| {
                    e.amount_confidence.map_or(false, |v| g.matches(v))
                })
            })
        }

        CompiledConditionTree::EntryProjectedRate { min, max } => {
            accumulated.iter().any(|e| {
                e.projected_rate_per_day.map_or(false, |r| {
                    min.map_or(true, |m| r >= m) && max.map_or(true, |m| r <= m)
                })
            })
        }

        CompiledConditionTree::EntryRecurrenceAnchor(anchor) => accumulated
            .iter()
            .any(|e| e.recurrence_anchor.as_deref() == Some(anchor.as_str())),
    }
}

// ---------------------------------------------------------------------------
// Compilation (pure — returns Err on invalid JSONB)
// ---------------------------------------------------------------------------

fn compile_entry(row: EntryRow) -> Result<CompiledEntry> {
    let conditions = compile_tree(&row.conditions)
        .with_context(|| format!("failed to compile conditions for entry {}", row.id))?;
    let direction = Direction::from_str(&row.direction)
        .ok_or_else(|| anyhow::anyhow!("unknown direction: {}", row.direction))?;
    let entry_type = EntryType::from_str(&row.entry_type)
        .ok_or_else(|| anyhow::anyhow!("unknown entry_type: {}", row.entry_type))?;
    let source = EntrySource::from_str(&row.source).unwrap_or(EntrySource::User);
    Ok(CompiledEntry {
        entry_id:               row.id,
        label_id:               row.label_id,
        priority:               row.priority,
        conditions,
        direction,
        entry_type,
        period_days:            row.period_days,
        source,
        confidence:             row.confidence,
        merchant_confidence:    row.merchant_confidence,
        timing_confidence:      row.timing_confidence,
        amount_confidence:      row.amount_confidence,
        projected_rate_per_day: row.projected_rate_per_day,
        recurrence_anchor:      row.recurrence_anchor,
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
/// - `"label_matched"` (field: `"label_id"`) → [`CompiledConditionTree::LabelMatched`]
/// - `"entry_direction"` (field: `"direction"`) → [`CompiledConditionTree::EntryDirection`]
/// - `"entry_type"` (field: `"entry_type"`) → [`CompiledConditionTree::EntryType`]
/// - `"entry_period"` (fields: `"min_days"`, `"max_days"` — both optional) → [`CompiledConditionTree::EntryPeriod`]
/// - `"entry_source"` (field: `"source"`) → [`CompiledConditionTree::EntrySource`]
/// - `"entry_confidence"` (field: `"score"` object) → [`CompiledConditionTree::EntryConfidence`]
/// - `"entry_projected_rate"` (fields: `"min"`, `"max"` — both optional) → [`CompiledConditionTree::EntryProjectedRate`]
/// - `"entry_recurrence_anchor"` (field: `"recurrence_anchor"`) → [`CompiledConditionTree::EntryRecurrenceAnchor`]
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
        "label_matched" => {
            let id_str = string_value(v, "label_id")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in label_matched leaf: {id_str}"))?;
            Ok(CompiledConditionTree::LabelMatched(id))
        }
        "entry_direction" => {
            let s = string_value(v, "direction")?;
            let dir = Direction::from_str(&s)
                .ok_or_else(|| anyhow::anyhow!("unknown direction: {s}"))?;
            Ok(CompiledConditionTree::EntryDirection(dir))
        }
        "entry_type" => {
            let s = string_value(v, "entry_type")?;
            let et = EntryType::from_str(&s)
                .ok_or_else(|| anyhow::anyhow!("unknown entry_type: {s}"))?;
            Ok(CompiledConditionTree::EntryType(et))
        }
        "entry_period" => {
            let min_days = v.get("min_days").and_then(|v| v.as_i64()).map(|n| n as i32);
            let max_days = v.get("max_days").and_then(|v| v.as_i64()).map(|n| n as i32);
            Ok(CompiledConditionTree::EntryPeriod { min_days, max_days })
        }
        "entry_source" => {
            let s = string_value(v, "source")?;
            let src = EntrySource::from_str(&s)
                .ok_or_else(|| anyhow::anyhow!("unknown source: {s}"))?;
            Ok(CompiledConditionTree::EntrySource(src))
        }
        "entry_confidence" => {
            let score_obj = v.get("score").and_then(|s| s.as_object());
            let parse_gate = |key: &str| -> Option<ConfidenceGate> {
                let obj = score_obj?.get(key)?.as_object()?;
                Some(ConfidenceGate {
                    min: obj.get("min").and_then(|v| v.as_f64()),
                    max: obj.get("max").and_then(|v| v.as_f64()),
                })
            };
            Ok(CompiledConditionTree::EntryConfidence {
                overall:  parse_gate("overall"),
                merchant: parse_gate("merchant"),
                timing:   parse_gate("timing"),
                amount:   parse_gate("amount"),
            })
        }
        "entry_projected_rate" => {
            let min = v.get("min").and_then(|v| v.as_f64());
            let max = v.get("max").and_then(|v| v.as_f64());
            Ok(CompiledConditionTree::EntryProjectedRate { min, max })
        }
        "entry_recurrence_anchor" => {
            let anchor = string_value(v, "recurrence_anchor")?;
            Ok(CompiledConditionTree::EntryRecurrenceAnchor(anchor))
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
        id:                     Uuid,
        label_id:               Option<Uuid>,
        priority:               i32,
        conditions:             serde_json::Value,
        direction:              String,
        entry_type:             String,
        period_days:            i32,
        source:                 String,
        confidence:             Option<sqlx::types::BigDecimal>,
        merchant_confidence:    Option<sqlx::types::BigDecimal>,
        timing_confidence:      Option<sqlx::types::BigDecimal>,
        amount_confidence:      Option<sqlx::types::BigDecimal>,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        recurrence_anchor:      Option<String>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, priority, conditions,
               direction, entry_type, period_days, source,
               confidence, merchant_confidence, timing_confidence, amount_confidence,
               projected_rate_per_day, recurrence_anchor
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
            id:                     r.id,
            label_id:               r.label_id,
            priority:               r.priority,
            conditions:             r.conditions,
            direction:              r.direction,
            entry_type:             r.entry_type,
            period_days:            r.period_days,
            source:                 r.source,
            confidence:             r.confidence
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            merchant_confidence:    r.merchant_confidence
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            timing_confidence:      r.timing_confidence
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            amount_confidence:      r.amount_confidence
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            projected_rate_per_day: r.projected_rate_per_day
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            recurrence_anchor:      r.recurrence_anchor,
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

    /// Compile a JSONB condition and evaluate with empty accumulated state.
    ///
    /// Covers all transaction-target conditions (Pass 1 semantics). For
    /// entry-target conditions use `eval_with_accumulated`.
    fn eval(json: serde_json::Value, txn: &TransactionRow) -> bool {
        let tree = compile_tree(&json).unwrap();
        evaluate(&tree, txn, &HashSet::new(), &[])
    }

    /// Compile a JSONB condition and evaluate with the given accumulated label set
    /// and an empty accumulated-metadata slice.
    ///
    /// Used to test Pass 2+ `LabelMatched` conditions that need only the label index.
    fn eval_with_labels(
        json:   serde_json::Value,
        txn:    &TransactionRow,
        labels: &HashSet<Uuid>,
    ) -> bool {
        let tree = compile_tree(&json).unwrap();
        evaluate(&tree, txn, labels, &[])
    }

    /// Compile a JSONB condition and evaluate with the given accumulated entry metadata.
    ///
    /// Used to test Pass 2+ entry-target conditions.
    fn eval_with_accumulated(
        json:        serde_json::Value,
        txn:         &TransactionRow,
        accumulated: &[AccumulatedEntryMeta],
    ) -> bool {
        let tree = compile_tree(&json).unwrap();
        // Build label_index from accumulated.
        let label_index: HashSet<Uuid> = accumulated
            .iter()
            .filter_map(|e| e.label_id)
            .collect();
        evaluate(&tree, txn, &label_index, accumulated)
    }

    /// Build a minimal `AccumulatedEntryMeta` for use in tests.
    fn make_meta(
        direction:   Direction,
        entry_type:  EntryType,
        period_days: i32,
        source:      EntrySource,
    ) -> AccumulatedEntryMeta {
        AccumulatedEntryMeta {
            label_id:               None,
            direction,
            entry_type,
            period_days,
            source,
            confidence:             None,
            merchant_confidence:    None,
            timing_confidence:      None,
            amount_confidence:      None,
            projected_rate_per_day: None,
            recurrence_anchor:      None,
        }
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
        let base = || CompiledEntry {
            entry_id:               Uuid::nil(),
            label_id:               None,
            priority:               100,
            conditions:             CompiledConditionTree::And(vec![]),
            direction:              Direction::Expense,
            entry_type:             EntryType::Standing,
            period_days:            30,
            source:                 EntrySource::User,
            confidence:             None,
            merchant_confidence:    None,
            timing_confidence:      None,
            amount_confidence:      None,
            projected_rate_per_day: None,
            recurrence_anchor:      None,
        };
        let mut entries = vec![
            CompiledEntry { priority: 200, ..base() },
            CompiledEntry { priority: 10,  ..base() },
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
        let cond = json!({"type": "label_matched", "label_id": label_id.to_string()});
        assert!(!eval(cond, &txn));
    }

    /// `LabelMatched` returns true when the label is in the accumulated set.
    #[test]
    fn label_matched_true_with_label_in_set() {
        let label_id = Uuid::parse_str("00000000-0000-0000-0000-000000000001").unwrap();
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Netflix");
        let cond = json!({"type": "label_matched", "label_id": label_id.to_string()});
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

    // -----------------------------------------------------------------------
    // New entry-target condition tests
    // -----------------------------------------------------------------------

    fn any_txn() -> TransactionRow {
        make_txn(any_uuid(), any_uuid(), "2026-03-01", -1000, "Test")
    }

    // --- compile_tree: happy-path parsing ---

    #[test]
    fn compile_entry_direction_expense() {
        let tree = compile_tree(&json!({"type": "entry_direction", "direction": "expense"}));
        assert!(tree.is_ok(), "should compile: {tree:?}");
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryDirection(Direction::Expense)
        ));
    }

    #[test]
    fn compile_entry_direction_mixed() {
        let tree = compile_tree(&json!({"type": "entry_direction", "direction": "mixed"}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryDirection(Direction::Mixed)
        ));
    }

    #[test]
    fn compile_entry_direction_missing_field_error() {
        let result = compile_tree(&json!({"type": "entry_direction"}));
        assert!(result.is_err(), "missing 'direction' field should fail");
    }

    #[test]
    fn compile_entry_direction_unknown_value_error() {
        let result = compile_tree(&json!({"type": "entry_direction", "direction": "sideways"}));
        assert!(result.is_err());
    }

    #[test]
    fn compile_entry_type_standing() {
        let tree = compile_tree(&json!({"type": "entry_type", "entry_type": "standing"}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryType(EntryType::Standing)
        ));
    }

    #[test]
    fn compile_entry_type_missing_field_error() {
        let result = compile_tree(&json!({"type": "entry_type"}));
        assert!(result.is_err());
    }

    #[test]
    fn compile_entry_type_unknown_value_error() {
        let result = compile_tree(&json!({"type": "entry_type", "entry_type": "once"}));
        assert!(result.is_err());
    }

    #[test]
    fn compile_entry_period_both_bounds() {
        let tree = compile_tree(&json!({"type": "entry_period", "min_days": 25, "max_days": 35}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryPeriod { min_days: Some(25), max_days: Some(35) }
        ));
    }

    #[test]
    fn compile_entry_period_no_bounds() {
        // Both bounds are optional — no fields still compiles.
        let tree = compile_tree(&json!({"type": "entry_period"}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryPeriod { min_days: None, max_days: None }
        ));
    }

    #[test]
    fn compile_entry_source_engine() {
        let tree = compile_tree(&json!({"type": "entry_source", "source": "engine"}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntrySource(EntrySource::Engine)
        ));
    }

    #[test]
    fn compile_entry_source_missing_field_error() {
        let result = compile_tree(&json!({"type": "entry_source"}));
        assert!(result.is_err());
    }

    #[test]
    fn compile_entry_source_unknown_value_error() {
        let result = compile_tree(&json!({"type": "entry_source", "source": "robot"}));
        assert!(result.is_err());
    }

    #[test]
    fn compile_entry_confidence_with_score() {
        let tree = compile_tree(&json!({
            "type": "entry_confidence",
            "score": {
                "overall":  {"min": 0.8},
                "merchant": {"min": 0.7, "max": 1.0}
            }
        }));
        assert!(tree.is_ok(), "{tree:?}");
        let CompiledConditionTree::EntryConfidence { overall, merchant, timing, amount } =
            tree.unwrap()
        else {
            panic!("expected EntryConfidence variant");
        };
        let overall = overall.expect("overall should be Some");
        assert!((overall.min.unwrap() - 0.8).abs() < 1e-9);
        assert!(overall.max.is_none());
        let merchant = merchant.expect("merchant should be Some");
        assert!((merchant.min.unwrap() - 0.7).abs() < 1e-9);
        assert!((merchant.max.unwrap() - 1.0).abs() < 1e-9);
        assert!(timing.is_none());
        assert!(amount.is_none());
    }

    #[test]
    fn compile_entry_confidence_no_score_object() {
        // Missing "score" — all gates None but still compiles (spec: score is optional).
        let tree = compile_tree(&json!({"type": "entry_confidence"}));
        assert!(tree.is_ok());
        let CompiledConditionTree::EntryConfidence { overall, merchant, timing, amount } =
            tree.unwrap()
        else {
            panic!("expected EntryConfidence");
        };
        assert!(overall.is_none() && merchant.is_none() && timing.is_none() && amount.is_none());
    }

    #[test]
    fn compile_entry_projected_rate() {
        let tree =
            compile_tree(&json!({"type": "entry_projected_rate", "min": 1.5, "max": 5.0}));
        assert!(tree.is_ok());
        let CompiledConditionTree::EntryProjectedRate { min, max } = tree.unwrap() else {
            panic!("expected EntryProjectedRate");
        };
        assert!((min.unwrap() - 1.5).abs() < 1e-9);
        assert!((max.unwrap() - 5.0).abs() < 1e-9);
    }

    #[test]
    fn compile_entry_projected_rate_no_bounds() {
        let tree = compile_tree(&json!({"type": "entry_projected_rate"}));
        assert!(tree.is_ok());
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryProjectedRate { min: None, max: None }
        ));
    }

    #[test]
    fn compile_entry_recurrence_anchor() {
        let tree = compile_tree(
            &json!({"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}),
        );
        assert!(tree.is_ok());
        let CompiledConditionTree::EntryRecurrenceAnchor(anchor) = tree.unwrap() else {
            panic!("expected EntryRecurrenceAnchor");
        };
        assert_eq!(anchor, "dom:15");
    }

    #[test]
    fn compile_entry_recurrence_anchor_missing_field_error() {
        let result = compile_tree(&json!({"type": "entry_recurrence_anchor"}));
        assert!(result.is_err());
    }

    // --- evaluate: false with empty accumulated ---

    #[test]
    fn entry_direction_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "expense"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_type_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_type", "entry_type": "standing"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_period_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_period", "min_days": 25, "max_days": 35}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_source_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_source", "source": "engine"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_confidence_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_confidence", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_projected_rate_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_projected_rate", "min": 1.0}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_recurrence_anchor_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}),
            &txn,
            &[]
        ));
    }

    // --- evaluate: true when a matching entry is in accumulated ---

    #[test]
    fn entry_direction_matches_when_present() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "expense"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_direction_no_match_wrong_direction() {
        let txn = any_txn();
        let meta = make_meta(Direction::Income, EntryType::Standing, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "expense"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_direction_mixed_matches() {
        let txn = any_txn();
        let meta = make_meta(Direction::Mixed, EntryType::Irregular, 7, EntrySource::Engine);
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "mixed"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_type_matches_when_present() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Variable, 30, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_type", "entry_type": "variable"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_type_no_match_wrong_type() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Irregular, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_type", "entry_type": "standing"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_matches_exact() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_period", "min_days": 25, "max_days": 35}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_no_match_out_of_range() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 60, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_period", "min_days": 25, "max_days": 35}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_open_bounds_match_all() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 365, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_period"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_source_matches_engine() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::Engine);
        assert!(eval_with_accumulated(
            json!({"type": "entry_source", "source": "engine"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_source_no_match_wrong_source() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_source", "source": "engine"}),
            &txn,
            &[meta]
        ));
    }

    // --- entry_confidence gate semantics ---

    fn make_meta_with_confidence(
        confidence: Option<f64>,
        merchant:   Option<f64>,
        timing:     Option<f64>,
        amount:     Option<f64>,
    ) -> AccumulatedEntryMeta {
        AccumulatedEntryMeta {
            label_id:               None,
            direction:              Direction::Expense,
            entry_type:             EntryType::Standing,
            period_days:            30,
            source:                 EntrySource::Engine,
            confidence,
            merchant_confidence:    merchant,
            timing_confidence:      timing,
            amount_confidence:      amount,
            projected_rate_per_day: None,
            recurrence_anchor:      None,
        }
    }

    #[test]
    fn entry_confidence_matches_overall_above_min() {
        let txn = any_txn();
        let meta = make_meta_with_confidence(Some(0.9), None, None, None);
        assert!(eval_with_accumulated(
            json!({"type": "entry_confidence", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_confidence_no_match_overall_below_min() {
        let txn = any_txn();
        let meta = make_meta_with_confidence(Some(0.7), None, None, None);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_confidence", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_confidence_no_match_when_score_is_null() {
        // Gate specified but confidence field is None → no match.
        let txn = any_txn();
        let meta = make_meta_with_confidence(None, None, None, None);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_confidence", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    /// ALL gates must be satisfied by the SAME single entry — not different entries.
    #[test]
    fn entry_confidence_all_gates_must_be_same_entry() {
        let txn = any_txn();
        // entry A: overall=0.9 but merchant=0.5
        // entry B: overall=0.5 but merchant=0.9
        // Gate: overall >= 0.8 AND merchant >= 0.8
        // Neither single entry satisfies both → false.
        let entry_a = make_meta_with_confidence(Some(0.9), Some(0.5), None, None);
        let entry_b = make_meta_with_confidence(Some(0.5), Some(0.9), None, None);
        assert!(!eval_with_accumulated(
            json!({
                "type": "entry_confidence",
                "score": {
                    "overall":  {"min": 0.8},
                    "merchant": {"min": 0.8}
                }
            }),
            &txn,
            &[entry_a, entry_b]
        ));
    }

    #[test]
    fn entry_confidence_single_entry_satisfies_all_gates() {
        let txn = any_txn();
        // Both overall and merchant meet their gates on entry A alone.
        let entry_a = make_meta_with_confidence(Some(0.9), Some(0.85), None, None);
        assert!(eval_with_accumulated(
            json!({
                "type": "entry_confidence",
                "score": {
                    "overall":  {"min": 0.8},
                    "merchant": {"min": 0.8}
                }
            }),
            &txn,
            &[entry_a]
        ));
    }

    #[test]
    fn entry_projected_rate_matches_in_range() {
        let txn = any_txn();
        let mut meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        meta.projected_rate_per_day = Some(3.0);
        assert!(eval_with_accumulated(
            json!({"type": "entry_projected_rate", "min": 1.5, "max": 5.0}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_projected_rate_no_match_null_rate() {
        // projected_rate_per_day is None → never matches a rate gate.
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_projected_rate", "min": 1.5}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_projected_rate_no_match_out_of_range() {
        let txn = any_txn();
        let mut meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        meta.projected_rate_per_day = Some(10.0);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_projected_rate", "min": 1.5, "max": 5.0}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_recurrence_anchor_matches() {
        let txn = any_txn();
        let mut meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        meta.recurrence_anchor = Some("dom:15".to_string());
        assert!(eval_with_accumulated(
            json!({"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_recurrence_anchor_no_match_null() {
        let txn = any_txn();
        let meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        // recurrence_anchor is None → never matches.
        assert!(!eval_with_accumulated(
            json!({"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_recurrence_anchor_no_match_wrong_anchor() {
        let txn = any_txn();
        let mut meta = make_meta(Direction::Expense, EntryType::Standing, 30, EntrySource::User);
        meta.recurrence_anchor = Some("dom:1".to_string());
        assert!(!eval_with_accumulated(
            json!({"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}),
            &txn,
            &[meta]
        ));
    }

    // --- tree_has_entry_targets: all 7 new variants return true ---

    #[test]
    fn tree_has_entry_targets_all_new_variants() {
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryDirection(
            Direction::Expense
        )));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryType(
            EntryType::Standing
        )));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryPeriod {
            min_days: None,
            max_days: None,
        }));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntrySource(
            EntrySource::User
        )));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryConfidence {
            overall:  None,
            merchant: None,
            timing:   None,
            amount:   None,
        }));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryProjectedRate {
            min: None,
            max: None,
        }));
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryRecurrenceAnchor(
            "dom:15".to_string()
        )));
    }

    /// Transaction-only trees still return false (regression guard).
    #[test]
    fn tree_has_entry_targets_false_for_txn_only() {
        let txn_only = CompiledConditionTree::And(vec![
            CompiledConditionTree::PayeeExact("Netflix".into()),
            CompiledConditionTree::AmountRange { min: None, max: None },
        ]);
        assert!(!tree_has_entry_targets(&txn_only));
    }

    /// New variants nested inside logical nodes are detected.
    #[test]
    fn tree_has_entry_targets_nested_detection() {
        let nested = CompiledConditionTree::Or(vec![
            CompiledConditionTree::PayeeContains("test".into()),
            CompiledConditionTree::EntryDirection(Direction::Income),
        ]);
        assert!(tree_has_entry_targets(&nested));
    }
}
