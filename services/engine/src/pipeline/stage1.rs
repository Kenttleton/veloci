//! Stage 1: Entry matching (boolean condition trees against transactions).
//!
//! **Input:** `transactions` for an entity; all `live` and `pending`
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
//! 1. Load entries (`status IN ('live', 'pending')`, `end_date IS NULL`).
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
pub(crate) struct FitnessGate {
    min: Option<f64>,
    max: Option<f64>,
}

impl FitnessGate {
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
    fitness:             Option<f64>,
    merchant_fit:        Option<f64>,
    timing_fit:          Option<f64>,
    amount_fit:          Option<f64>,
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
    pub fitness:             Option<f64>,
    pub merchant_fit:        Option<f64>,
    pub timing_fit:          Option<f64>,
    pub amount_fit:          Option<f64>,
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
/// `EntrySource`, `EntryFitness`, `EntryProjectedRate`, `EntryRecurrenceAnchor`.
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
    InstitutionId(Uuid),
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
    /// Matches when any single accumulated entry satisfies ALL specified fitness gates.
    EntryFitness {
        overall:  Option<FitnessGate>,
        merchant: Option<FitnessGate>,
        timing:   Option<FitnessGate>,
        amount:   Option<FitnessGate>,
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
    fitness:             Option<f64>,
    merchant_fit:        Option<f64>,
    timing_fit:          Option<f64>,
    amount_fit:          Option<f64>,
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
    institution_id:      Option<Uuid>,
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

    // Load live + pending entries (pending entries are matched
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
    // Returns (txn_id, matched_entries_with_fit, was_unmatched) per transaction.
    let results: Vec<(Uuid, Vec<(Uuid, f64)>, bool)> = txns
        .par_iter()
        .map(|txn| {
            let mut matched: Vec<(Uuid, f64)> = Vec::new();
            let mut accumulated: Vec<AccumulatedEntryMeta> = Vec::new();
            let mut label_index: HashSet<Uuid> = HashSet::new();

            // --- Pass 1: transaction-target entries ---
            // Evaluated against transaction fields only. Each match contributes
            // (entry_id, fit) to `matched`, label_id to `label_index`, and full
            // metadata to `accumulated`.
            for entry in &txn_entries {
                if let Some(fit) = evaluate(&entry.conditions, txn, &label_index, &accumulated) {
                    matched.push((entry.entry_id, fit));
                    if let Some(label_id) = entry.label_id {
                        label_index.insert(label_id);
                    }
                    accumulated.push(AccumulatedEntryMeta {
                        label_id:               entry.label_id,
                        direction:              entry.direction,
                        entry_type:             entry.entry_type,
                        period_days:            entry.period_days,
                        source:                 entry.source,
                        fitness:             entry.fitness,
                        merchant_fit:        entry.merchant_fit,
                        timing_fit:          entry.timing_fit,
                        amount_fit:          entry.amount_fit,
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
                HashSet::from_iter(matched.iter().map(|(id, _)| *id));

            loop {
                // Snapshot the accumulated state at the start of this pass.
                let pass_accumulated = accumulated.clone();
                let pass_label_index = label_index.clone();
                let mut newly_matched: Vec<(Uuid, f64)> = Vec::new();
                let mut new_meta: Vec<AccumulatedEntryMeta> = Vec::new();
                let mut new_labels: HashSet<Uuid> = HashSet::new();
                let mut cycle_detected = false;

                for entry in &entry_entries {
                    // Skip entries already assigned in a prior pass.
                    if matched_set.contains(&entry.entry_id) {
                        continue;
                    }

                    if let Some(fit) = evaluate(&entry.conditions, txn, &pass_label_index, &pass_accumulated) {
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
                        newly_matched.push((entry.entry_id, fit));
                        new_meta.push(AccumulatedEntryMeta {
                            label_id:               entry.label_id,
                            direction:              entry.direction,
                            entry_type:             entry.entry_type,
                            period_days:            entry.period_days,
                            source:                 entry.source,
                            fitness:             entry.fitness,
                            merchant_fit:        entry.merchant_fit,
                            timing_fit:          entry.timing_fit,
                            amount_fit:          entry.amount_fit,
                            projected_rate_per_day: entry.projected_rate_per_day,
                            recurrence_anchor:      entry.recurrence_anchor.clone(),
                        });
                    }
                }

                // Commit newly matched entries from this pass.
                for (entry_id, fit) in newly_matched {
                    if matched_set.insert(entry_id) {
                        matched.push((entry_id, fit));
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

    // Update next_due_date on live entries that received new assignments this
    // run. Only live entries are updated — pending entries are excluded
    // by the SQL WHERE clause in update_next_due_dates.
    let matched_entry_ids: Vec<Uuid> = results
        .iter()
        .flat_map(|(_, entries, _)| entries.iter().map(|(id, _)| *id))
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
        | CompiledConditionTree::EntryType(_)
        | CompiledConditionTree::EntryPeriod { .. }
        | CompiledConditionTree::EntrySource(_)
        | CompiledConditionTree::EntryFitness { .. }
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
) -> Option<f64> {
    evaluate(tree, txn, accumulated_labels, accumulated)
}

/// Evaluate a condition tree against a transaction.
///
/// Returns `Some(fit)` when the conditions match, where `fit` is a score in
/// [0.0, 1.0] representing match quality. Returns `None` when the conditions
/// do not match.
///
/// Payee-string conditions produce graded scores based on match coverage.
/// All other conditions (amount, date, account, entry-target) are binary gates
/// that return `Some(1.0)` on a match.
///
/// Logical operators propagate fit:
/// - AND → `min` of child fits (fails fast on first None)
/// - OR  → `max` of matching child fits
/// - NOT → inverts presence (None→Some(1.0), Some(f)→None)
/// - XOR → matched child's fit when exactly one child matches
fn evaluate(
    node:          &CompiledConditionTree,
    txn:           &TransactionRow,
    label_index:   &HashSet<Uuid>,
    accumulated:   &[AccumulatedEntryMeta],
) -> Option<f64> {
    match node {
        CompiledConditionTree::And(children) => {
            let mut min_fit = f64::MAX;
            for c in children {
                match evaluate(c, txn, label_index, accumulated) {
                    None    => return None,
                    Some(f) => min_fit = min_fit.min(f),
                }
            }
            Some(if min_fit == f64::MAX { 1.0 } else { min_fit })
        }
        CompiledConditionTree::Or(children) => {
            let mut max_fit: Option<f64> = None;
            for c in children {
                if let Some(f) = evaluate(c, txn, label_index, accumulated) {
                    max_fit = Some(max_fit.map_or(f, |m: f64| m.max(f)));
                }
            }
            max_fit
        }
        CompiledConditionTree::Not(child) => {
            match evaluate(child, txn, label_index, accumulated) {
                None    => Some(1.0),
                Some(_) => None,
            }
        }
        CompiledConditionTree::Xor(a, b) => {
            let fa = evaluate(a, txn, label_index, accumulated);
            let fb = evaluate(b, txn, label_index, accumulated);
            match (fa, fb) {
                (Some(f), None) => Some(f),
                (None, Some(f)) => Some(f),
                _               => None,
            }
        }

        // --- Payee conditions (all case-insensitive except Regex) ---
        //
        // Exact match → 1.0 (full fit).
        // Starts-with and ends-with → 0.85 + 0.15 × coverage (string-length ratio).
        // Contains → 0.75 + 0.15 × coverage.
        // Other payee gates (not-contains, regex, one-of) → binary 1.0.
        CompiledConditionTree::PayeeExact(s) => {
            if txn.merchant_normalized.eq_ignore_ascii_case(s) { Some(1.0) } else { None }
        }
        CompiledConditionTree::PayeeContains(s) => {
            let haystack = txn.merchant_normalized.to_ascii_lowercase();
            let needle   = s.to_ascii_lowercase();
            if haystack.contains(&needle) {
                let coverage = needle.len() as f64 / haystack.len().max(1) as f64;
                Some((0.75 + 0.15 * coverage).min(1.0))
            } else {
                None
            }
        }
        CompiledConditionTree::PayeeNotContains(s) => {
            let haystack = txn.merchant_normalized.to_ascii_lowercase();
            if !haystack.contains(&s.to_ascii_lowercase()) { Some(1.0) } else { None }
        }
        CompiledConditionTree::PayeeStartsWith(s) => {
            let haystack = txn.merchant_normalized.to_ascii_lowercase();
            let needle   = s.to_ascii_lowercase();
            if haystack.starts_with(&needle) {
                let coverage = needle.len() as f64 / haystack.len().max(1) as f64;
                Some((0.85 + 0.15 * coverage).min(1.0))
            } else {
                None
            }
        }
        CompiledConditionTree::PayeeEndsWith(s) => {
            let haystack = txn.merchant_normalized.to_ascii_lowercase();
            let needle   = s.to_ascii_lowercase();
            if haystack.ends_with(&needle) {
                let coverage = needle.len() as f64 / haystack.len().max(1) as f64;
                Some((0.85 + 0.15 * coverage).min(1.0))
            } else {
                None
            }
        }
        CompiledConditionTree::PayeeRegex(re) => {
            if re.is_match(&txn.merchant_normalized) { Some(1.0) } else { None }
        }
        CompiledConditionTree::PayeeOneOf(list) => {
            if list.iter().any(|s| txn.merchant_normalized.eq_ignore_ascii_case(s)) {
                Some(1.0)
            } else {
                None
            }
        }

        // --- Amount conditions (binary gate) ---
        CompiledConditionTree::AmountRange { min, max } => {
            let a = txn.amount_cents;
            if min.map_or(true, |m| a >= m) && max.map_or(true, |m| a <= m) {
                Some(1.0)
            } else {
                None
            }
        }

        // --- Date conditions (binary gates) ---
        CompiledConditionTree::DateDayOfMonth { day, tolerance_days } => {
            let txn_day = txn.date.day() as i32;
            let target  = i32::from(*day);
            let tol     = i32::from(*tolerance_days);
            // Wrap-around month end is not handled (naive — sufficient for matching).
            if (txn_day - target).abs() <= tol { Some(1.0) } else { None }
        }
        CompiledConditionTree::DateRange { start, end } => {
            if txn.date >= *start && txn.date <= *end { Some(1.0) } else { None }
        }

        // --- Account / institution conditions (binary gates) ---
        CompiledConditionTree::AccountId(id) => {
            if txn.account_id == *id { Some(1.0) } else { None }
        }
        CompiledConditionTree::InstitutionId(id) => {
            if txn.institution_id.map_or(false, |iid| iid == *id) { Some(1.0) } else { None }
        }

        // --- Entry-target conditions (Pass 2+ only, binary gates) ---
        //
        // All of these evaluate against `accumulated`, the list of metadata for
        // entries already matched in prior passes. They are always None in Pass 1
        // (empty accumulated slice).
        CompiledConditionTree::LabelMatched(label_id) => {
            if label_index.contains(label_id) { Some(1.0) } else { None }
        }

        // Transaction-target: matches sign of amount_cents to entry direction.
        CompiledConditionTree::EntryDirection(dir) => {
            use crate::pipeline::types::Direction;
            match dir {
                Direction::Income => if txn.amount_cents > 0 { Some(1.0) } else { None },
                Direction::Spend  => if txn.amount_cents < 0 { Some(1.0) } else { None },
                Direction::Mixed  => Some(1.0),
            }
        }

        CompiledConditionTree::EntryType(et) => {
            if accumulated.iter().any(|e| e.entry_type == *et) { Some(1.0) } else { None }
        }

        CompiledConditionTree::EntryPeriod { min_days, max_days } => {
            let matched = accumulated.iter().any(|e| {
                min_days.map_or(true, |m| e.period_days >= m)
                    && max_days.map_or(true, |m| e.period_days <= m)
            });
            if matched { Some(1.0) } else { None }
        }

        CompiledConditionTree::EntrySource(src) => {
            if accumulated.iter().any(|e| e.source == *src) { Some(1.0) } else { None }
        }

        // All specified gates must be satisfied by the SAME accumulated entry.
        CompiledConditionTree::EntryFitness { overall, merchant, timing, amount } => {
            let matched = accumulated.iter().any(|e| {
                overall.as_ref().map_or(true, |g| {
                    e.fitness.map_or(false, |v| g.matches(v))
                }) && merchant.as_ref().map_or(true, |g| {
                    e.merchant_fit.map_or(false, |v| g.matches(v))
                }) && timing.as_ref().map_or(true, |g| {
                    e.timing_fit.map_or(false, |v| g.matches(v))
                }) && amount.as_ref().map_or(true, |g| {
                    e.amount_fit.map_or(false, |v| g.matches(v))
                })
            });
            if matched { Some(1.0) } else { None }
        }

        CompiledConditionTree::EntryProjectedRate { min, max } => {
            let matched = accumulated.iter().any(|e| {
                e.projected_rate_per_day.map_or(false, |r| {
                    min.map_or(true, |m| r >= m) && max.map_or(true, |m| r <= m)
                })
            });
            if matched { Some(1.0) } else { None }
        }

        CompiledConditionTree::EntryRecurrenceAnchor(anchor) => {
            let matched = accumulated
                .iter()
                .any(|e| e.recurrence_anchor.as_deref() == Some(anchor.as_str()));
            if matched { Some(1.0) } else { None }
        }
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
        fitness:             row.fitness,
        merchant_fit:        row.merchant_fit,
        timing_fit:          row.timing_fit,
        amount_fit:          row.amount_fit,
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
/// - `"entry_fitness"` (field: `"score"` object) → [`CompiledConditionTree::EntryFitness`]
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
        "institution_id" | "institution" => {
            let id_str = string_value(v, "value")?;
            let id: Uuid = id_str
                .parse()
                .with_context(|| format!("invalid UUID in institution leaf: {id_str}"))?;
            Ok(CompiledConditionTree::InstitutionId(id))
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
        "entry_fitness" => {
            let score_obj = v.get("score").and_then(|s| s.as_object());
            let parse_gate = |key: &str| -> Option<FitnessGate> {
                let obj = score_obj?.get(key)?.as_object()?;
                Some(FitnessGate {
                    min: obj.get("min").and_then(|v| v.as_f64()),
                    max: obj.get("max").and_then(|v| v.as_f64()),
                })
            };
            Ok(CompiledConditionTree::EntryFitness {
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
        institution_id:      Option<Uuid>,
        date:                NaiveDate,
        amount_cents:        i64,
        merchant_normalized: String,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT t.id, t.account_id, a.institution_id,
               t.date, t.amount_cents, t.merchant_normalized
        FROM transactions t
        LEFT JOIN accounts a ON a.id = t.account_id AND a.entity_id = $1
        WHERE t.entity_id = $1
        ORDER BY t.date ASC
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
            institution_id:      r.institution_id,
            date:                r.date,
            amount_cents:        r.amount_cents,
            merchant_normalized: r.merchant_normalized,
        })
        .collect())
}

/// Load all entries eligible for Stage 1 matching.
///
/// Includes both `live` and `pending` entries so that `pending`
/// entries receive `transaction_entry_assignments` rows after a reprocess run.
/// Stage 3+ independently filters to `status = 'live'` and is not affected.
async fn load_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<EntryRow>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                     Uuid,
        label_id:               Option<Uuid>,
        priority:               i32,
        conditions:             serde_json::Value,
        direction:              String,
        entry_type:             String,
        period_days:            Option<i32>,
        source:                 String,
        fitness:             Option<sqlx::types::BigDecimal>,
        merchant_fit:        Option<sqlx::types::BigDecimal>,
        timing_fit:          Option<sqlx::types::BigDecimal>,
        amount_fit:          Option<sqlx::types::BigDecimal>,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        recurrence_anchor:      Option<String>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, priority, conditions,
               direction, entry_type, period_days, source,
               fitness, merchant_fit, timing_fit, amount_fit,
               projected_rate_per_day, recurrence_anchor
        FROM entries
        WHERE entity_id = $1
          AND status IN ('live', 'pending')
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
            period_days:            r.period_days.unwrap_or(30),
            source:                 r.source,
            fitness:             r.fitness
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            merchant_fit:        r.merchant_fit
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            timing_fit:          r.timing_fit
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            amount_fit:          r.amount_fit
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
    results: &[(Uuid, Vec<(Uuid, f64)>, bool)],
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

    // Collect all (transaction_id, entry_id, fit) triples.
    let mut txn_ids:    Vec<Uuid> = Vec::new();
    let mut entry_ids:  Vec<Uuid> = Vec::new();
    let mut fit_scores: Vec<f64>  = Vec::new();

    for (txn_id, matched_entries, _) in results {
        for (entry_id, fit) in matched_entries {
            txn_ids.push(*txn_id);
            entry_ids.push(*entry_id);
            fit_scores.push(*fit);
        }
    }

    if txn_ids.is_empty() {
        return Ok(());
    }

    sqlx::query(
        r#"
        INSERT INTO transaction_entry_assignments (transaction_id, entry_id, fit)
        SELECT t, e, f
        FROM UNNEST($1::uuid[], $2::uuid[], $3::float8[]) AS u(t, e, f)
        ON CONFLICT (transaction_id, entry_id) DO UPDATE SET fit = EXCLUDED.fit
        "#,
    )
    .bind(&txn_ids)
    .bind(&entry_ids)
    .bind(&fit_scores)
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
    // Only live entries receive next_due_date updates. pending entries
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
          AND e.status = 'live'
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
            institution_id:      None,
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
        evaluate(&tree, txn, &HashSet::new(), &[]).is_some()
    }

    /// Like `eval` but returns the raw fit score for assertions on graded conditions.
    fn eval_fit(json: serde_json::Value, txn: &TransactionRow) -> Option<f64> {
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
        evaluate(&tree, txn, labels, &[]).is_some()
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
        evaluate(&tree, txn, &label_index, accumulated).is_some()
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
            fitness:             None,
            merchant_fit:        None,
            timing_fit:          None,
            amount_fit:          None,
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
    fn institution_id_leaf() {
        let inst  = Uuid::parse_str("00000000-0000-0000-0000-000000000010").unwrap();
        let other = Uuid::parse_str("00000000-0000-0000-0000-000000000011").unwrap();
        let cond  = json!({"type": "institution_id", "value": inst.to_string()});
        // Matches when institution_id is set and equals the target.
        let txn = TransactionRow { institution_id: Some(inst), ..any_txn() };
        assert!(eval(cond.clone(), &txn));
        // No match when institution differs.
        let txn_other = TransactionRow { institution_id: Some(other), ..any_txn() };
        assert!(!eval(cond.clone(), &txn_other));
        // No match when account has no institution.
        let txn_none = TransactionRow { institution_id: None, ..any_txn() };
        assert!(!eval(cond, &txn_none));
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
            direction:              Direction::Spend,
            entry_type:             EntryType::Standing,
            period_days:            30,
            source:                 EntrySource::User,
            fitness:             None,
            merchant_fit:        None,
            timing_fit:          None,
            amount_fit:          None,
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
    fn compile_entry_direction_spend() {
        let tree = compile_tree(&json!({"type": "entry_direction", "direction": "spend"}));
        assert!(tree.is_ok(), "should compile: {tree:?}");
        assert!(matches!(
            tree.unwrap(),
            CompiledConditionTree::EntryDirection(Direction::Spend)
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
    fn compile_entry_fitness_with_score() {
        let tree = compile_tree(&json!({
            "type": "entry_fitness",
            "score": {
                "overall":  {"min": 0.8},
                "merchant": {"min": 0.7, "max": 1.0}
            }
        }));
        assert!(tree.is_ok(), "{tree:?}");
        let CompiledConditionTree::EntryFitness { overall, merchant, timing, amount } =
            tree.unwrap()
        else {
            panic!("expected EntryFitness variant");
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
    fn compile_entry_fitness_no_score_object() {
        // Missing "score" — all gates None but still compiles (spec: score is optional).
        let tree = compile_tree(&json!({"type": "entry_fitness"}));
        assert!(tree.is_ok());
        let CompiledConditionTree::EntryFitness { overall, merchant, timing, amount } =
            tree.unwrap()
        else {
            panic!("expected EntryFitness");
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
            json!({"type": "entry_direction", "direction": "spend"}),
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
    fn entry_fitness_false_with_empty_accumulated() {
        let txn = any_txn();
        assert!(!eval_with_accumulated(
            json!({"type": "entry_fitness", "score": {"overall": {"min": 0.8}}}),
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

    // entry_direction is now a transaction-target: matches amount_cents sign.
    #[test]
    fn entry_direction_spend_matches_negative_txn() {
        // any_txn() has amount_cents = -1000 (negative = spend)
        let txn = any_txn();
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "spend"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_direction_spend_no_match_positive_txn() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", 1000, "Paycheck");
        assert!(!eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "spend"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_direction_income_matches_positive_txn() {
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", 5000, "Paycheck");
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "income"}),
            &txn,
            &[]
        ));
    }

    #[test]
    fn entry_direction_mixed_always_matches() {
        let spend_txn = any_txn(); // amount_cents = -1000
        let income_txn = make_txn(any_uuid(), any_uuid(), "2026-03-01", 5000, "Paycheck");
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "mixed"}),
            &spend_txn,
            &[]
        ));
        assert!(eval_with_accumulated(
            json!({"type": "entry_direction", "direction": "mixed"}),
            &income_txn,
            &[]
        ));
    }

    #[test]
    fn entry_type_matches_when_present() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Variable, 30, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_type", "entry_type": "variable"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_type_no_match_wrong_type() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Irregular, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_type", "entry_type": "standing"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_matches_exact() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_period", "min_days": 25, "max_days": 35}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_no_match_out_of_range() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Standing, 60, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_period", "min_days": 25, "max_days": 35}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_period_open_bounds_match_all() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Standing, 365, EntrySource::User);
        assert!(eval_with_accumulated(
            json!({"type": "entry_period"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_source_matches_engine() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::Engine);
        assert!(eval_with_accumulated(
            json!({"type": "entry_source", "source": "engine"}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_source_no_match_wrong_source() {
        let txn = any_txn();
        let meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_source", "source": "engine"}),
            &txn,
            &[meta]
        ));
    }

    // --- entry_fitness gate semantics ---

    fn make_meta_with_fitness(
        fitness: Option<f64>,
        merchant:   Option<f64>,
        timing:     Option<f64>,
        amount:     Option<f64>,
    ) -> AccumulatedEntryMeta {
        AccumulatedEntryMeta {
            label_id:               None,
            direction:              Direction::Spend,
            entry_type:             EntryType::Standing,
            period_days:            30,
            source:                 EntrySource::Engine,
            fitness,
            merchant_fit:    merchant,
            timing_fit:      timing,
            amount_fit:      amount,
            projected_rate_per_day: None,
            recurrence_anchor:      None,
        }
    }

    #[test]
    fn entry_fitness_matches_overall_above_min() {
        let txn = any_txn();
        let meta = make_meta_with_fitness(Some(0.9), None, None, None);
        assert!(eval_with_accumulated(
            json!({"type": "entry_fitness", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_fitness_no_match_overall_below_min() {
        let txn = any_txn();
        let meta = make_meta_with_fitness(Some(0.7), None, None, None);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_fitness", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_fitness_no_match_when_score_is_null() {
        // Gate specified but fitness field is None → no match.
        let txn = any_txn();
        let meta = make_meta_with_fitness(None, None, None, None);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_fitness", "score": {"overall": {"min": 0.8}}}),
            &txn,
            &[meta]
        ));
    }

    /// ALL gates must be satisfied by the SAME single entry — not different entries.
    #[test]
    fn entry_fitness_all_gates_must_be_same_entry() {
        let txn = any_txn();
        // entry A: overall=0.9 but merchant=0.5
        // entry B: overall=0.5 but merchant=0.9
        // Gate: overall >= 0.8 AND merchant >= 0.8
        // Neither single entry satisfies both → false.
        let entry_a = make_meta_with_fitness(Some(0.9), Some(0.5), None, None);
        let entry_b = make_meta_with_fitness(Some(0.5), Some(0.9), None, None);
        assert!(!eval_with_accumulated(
            json!({
                "type": "entry_fitness",
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
    fn entry_fitness_single_entry_satisfies_all_gates() {
        let txn = any_txn();
        // Both overall and merchant meet their gates on entry A alone.
        let entry_a = make_meta_with_fitness(Some(0.9), Some(0.85), None, None);
        assert!(eval_with_accumulated(
            json!({
                "type": "entry_fitness",
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
        let mut meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
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
        let meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
        assert!(!eval_with_accumulated(
            json!({"type": "entry_projected_rate", "min": 1.5}),
            &txn,
            &[meta]
        ));
    }

    #[test]
    fn entry_projected_rate_no_match_out_of_range() {
        let txn = any_txn();
        let mut meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
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
        let mut meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
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
        let meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
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
        let mut meta = make_meta(Direction::Spend, EntryType::Standing, 30, EntrySource::User);
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
            Direction::Spend
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
        assert!(tree_has_entry_targets(&CompiledConditionTree::EntryFitness {
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
