# Conditions & Cadence Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `date_day_of_month` with a general `recurrence_anchor` transaction-target condition, refactor stage1's evaluation to run in ordered phases (payee → timing → amount), update stage2 to generate full conditions from empirical cluster data (payee + anchor + amount clamp), and make `TIMING_VARIANCE_THRESHOLD_DAYS` the single source of truth for timing tolerance everywhere.

**Architecture:** Four layers change together. The shared `TIMING_VARIANCE_THRESHOLD_DAYS` constant moves to `pipeline/mod.rs`. Stage2's `persist_cluster` gains full condition generation: payee + `recurrence_anchor` (dom/dow/interval) + `amount_range(observed_min, observed_max)` for standing entries; payee only for variable. Stage1's evaluation loop inverts — from per-transaction to per-entry — with ordered phases that give the timing phase the payee-scoped candidate set it needs; `interval:N` is evaluated by checking consecutive spacing within that set. Stage7 gains an `entry_has_lapsed` helper. The Go store gains `cadence` ↔ `recurrence_anchor` Schema B ↔ A translation.

**Tech Stack:** Rust (sqlx, serde_json, chrono, rayon), Go 1.26, Templ, PostgreSQL

## Global Constraints

- No schema changes — this plan touches no SQL migrations
- All tests must pass before each commit: `cargo test` in `services/engine`, `go test ./...` in `services/web`
- `TIMING_VARIANCE_THRESHOLD_DAYS = 5.0` is the single timing tolerance constant — never redefined elsewhere
- `date_day_of_month` is deprecated — evaluator NOT removed; no new generation; existing user entries continue to work
- Stage2 amount clamp: raw observed `min(amount_cents)` / `max(amount_cents)` — no buffer; drift proposals handle future expansion
- Standing entry conditions always include all three: payee + recurrence_anchor + amount_range
- Variable entry conditions: payee only
- `interval:N` evaluation in stage1 uses consecutive-spacing chain detection on the payee-scoped candidate set — no `next_due_date` involved
- Schema B cadence forms: `"monthly:N"` (dom 1–28), `"monthly:last"` (dom:-1), `"semimonthly:N,M"`, `"weekly:<dayname>"`, `"every:N"`. Tolerance always implicit (5 days); no `tolerance_days` in Schema B
- `next_due_date` belongs to projections only (stage7); no pipeline logic reads it for condition evaluation

---

## File Map

| File | Change |
|---|---|
| `services/engine/src/pipeline/mod.rs` | Add `pub const TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0` |
| `services/engine/src/pipeline/stage2.rs` | Remove local constant; import from `super`; update `persist_cluster` to generate full conditions |
| `services/engine/src/pipeline/stage1.rs` | Add `RecurrenceAnchor` variant; refactor `run()` to per-entry ordered phases; add phase helper functions |
| `services/engine/src/pipeline/stage7.rs` | Add `pub(crate) fn entry_has_lapsed` |
| `services/web/store/conditions.go` | Split `entry_recurrence_anchor` alias; add `cadence` B→A and A→B; add helpers |
| `docs/veloci-ref.md` | Add `recurrence_anchor` leaf, `cadence` Schema B; deprecate `date_day_of_month`; update timing_fit |
| `docs/conditions-editor.md` | Add `cadence` section; add DDOM deprecation notice |
| `services/web/page/glossary.templ` | Add Timing Tolerance and Cadence entries |

---

## Task 1: Promote TIMING_VARIANCE_THRESHOLD_DAYS to shared pipeline constant

**Files:**
- Modify: `services/engine/src/pipeline/mod.rs`
- Modify: `services/engine/src/pipeline/stage2.rs`

**Interfaces:**
- Produces: `crate::pipeline::TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0` — import in any stage with `use super::TIMING_VARIANCE_THRESHOLD_DAYS`

- [ ] **Step 1: Add the shared constant to mod.rs**

In `services/engine/src/pipeline/mod.rs`, add after the `pub mod types;` line:

```rust
// ---------------------------------------------------------------------------
// Pipeline-wide constants
// ---------------------------------------------------------------------------

/// Timing tolerance in days used uniformly across all pipeline stages:
/// - Stage 2: `timing_fit` scoring and `interval:N` chain detection
/// - Stage 1: `RecurrenceAnchor` condition evaluation
/// - Stage 7: ended-entry lapse detection
///
/// Chosen to absorb billing cycle drift: weekend shifts, bank settlement
/// delays, and month-end rounding. ±5 days matches the window used in
/// `detect_anchor` DOM grouping.
pub const TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0;
```

- [ ] **Step 2: Remove local constant from stage2.rs and import shared one**

In `services/engine/src/pipeline/stage2.rs`, remove the block around line 42:

```rust
/// Base timing sensitivity: std dev ≤ this many days → timing_score = 1.0.
/// Chosen to absorb billing cycle drift (weekend shifts, month-end rounding).
const TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0;
```

Add at the top of the imports section:

```rust
use super::TIMING_VARIANCE_THRESHOLD_DAYS;
```

- [ ] **Step 3: Run engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass. Value is unchanged so no behavior change.

- [ ] **Step 4: Commit**

```bash
git add services/engine/src/pipeline/mod.rs services/engine/src/pipeline/stage2.rs
git commit -m "refactor: promote TIMING_VARIANCE_THRESHOLD_DAYS to shared pipeline constant"
```

---

## Task 2: Add RecurrenceAnchor condition type to stage1

**Files:**
- Modify: `services/engine/src/pipeline/stage1.rs`

**Interfaces:**
- Consumes: `super::TIMING_VARIANCE_THRESHOLD_DAYS` (Task 1)
- Produces:
  - `CompiledConditionTree::RecurrenceAnchor { anchor: String }` — transaction-target, Pass 1
  - `compile_tree` handles `"recurrence_anchor"` Schema A type
  - `tree_has_entry_targets` returns `false` for `RecurrenceAnchor`
  - `days_in_month_local(year: i32, month: u32) -> u32` private helper
  - Per-transaction calendar evaluation for `dom:N`, `dom:-1`, `dom:N,M`, `dow:N`
  - `interval:N` evaluation is NOT done here — handled by the group timing pass in Task 3

Note: `interval:N` in a `RecurrenceAnchor` condition node returns `None` when evaluated per-transaction. The Task 3 timing phase handles it via group evaluation. This is intentional — do not return `Some(1.0)` for `interval:N` in the per-transaction evaluator.

- [ ] **Step 1: Write failing unit tests**

In `services/engine/src/pipeline/stage1.rs`, find the test module. Add after existing `date_day_of_month` tests:

```rust
#[cfg(test)]
mod recurrence_anchor_cond_tests {
    use super::*;
    use serde_json::json;

    fn make_txn(date_str: &str) -> TransactionRow {
        TransactionRow {
            id:                  uuid::Uuid::new_v4(),
            account_id:          uuid::Uuid::new_v4(),
            institution_id:      None,
            date:                date_str.parse().unwrap(),
            amount_cents:        -5000,
            merchant_normalized: "Test".to_string(),
        }
    }

    fn eval_anchor(anchor: &str, date_str: &str) -> bool {
        let v = json!({"type": "recurrence_anchor", "recurrence_anchor": anchor});
        let tree = compile_tree(&v).unwrap();
        let txn = make_txn(date_str);
        evaluate(&tree, &txn, &std::collections::HashSet::new(), &[]).is_some()
    }

    #[test]
    fn dom_exact() { assert!(eval_anchor("dom:15", "2026-03-15")); }

    #[test]
    fn dom_within_tolerance() {
        assert!(eval_anchor("dom:15", "2026-03-18")); // 3 days after
    }

    #[test]
    fn dom_outside_tolerance() {
        assert!(!eval_anchor("dom:15", "2026-03-21")); // 6 days after
    }

    #[test]
    fn dom_last_march() {
        assert!(eval_anchor("dom:-1", "2026-03-31"));
        assert!(eval_anchor("dom:-1", "2026-03-29")); // within 5 of 31st
        assert!(!eval_anchor("dom:-1", "2026-03-24")); // 7 days before 31st
    }

    #[test]
    fn dom_last_february() {
        assert!(eval_anchor("dom:-1", "2026-02-28"));
    }

    #[test]
    fn dom_semimonthly_first() { assert!(eval_anchor("dom:1,15", "2026-03-01")); }

    #[test]
    fn dom_semimonthly_second() { assert!(eval_anchor("dom:1,15", "2026-03-15")); }

    #[test]
    fn dom_semimonthly_within_tolerance_of_second() {
        assert!(eval_anchor("dom:1,15", "2026-03-17")); // 2 after 15th
    }

    #[test]
    fn dom_semimonthly_between_no_match() {
        // March 10: 9 days after 1st (outside), 5 days before 15th (boundary — exclusive)
        assert!(!eval_anchor("dom:1,15", "2026-03-10"));
    }

    #[test]
    fn dow_monday() {
        assert!(eval_anchor("dow:0", "2026-03-16")); // Monday
        assert!(!eval_anchor("dow:0", "2026-03-17")); // Tuesday
    }

    #[test]
    fn dow_friday() { assert!(eval_anchor("dow:4", "2026-03-20")); }

    #[test]
    fn interval_returns_none_per_transaction() {
        // interval:N cannot evaluate per-transaction; group timing pass handles it
        assert!(!eval_anchor("interval:91", "2026-03-15"));
    }

    #[test]
    fn compile_succeeds() {
        assert!(compile_tree(&json!({"type":"recurrence_anchor","recurrence_anchor":"dom:15"})).is_ok());
    }

    #[test]
    fn compile_missing_field_errors() {
        assert!(compile_tree(&json!({"type":"recurrence_anchor"})).is_err());
    }

    #[test]
    fn is_not_entry_target() {
        let tree = compile_tree(&json!({"type":"recurrence_anchor","recurrence_anchor":"dom:15"})).unwrap();
        assert!(!tree_has_entry_targets(&tree));
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/engine && cargo test recurrence_anchor_cond_tests
```

Expected: compile error — `RecurrenceAnchor` not defined.

- [ ] **Step 3: Add days_in_month_local helper**

In `services/engine/src/pipeline/stage1.rs`, find the `string_value` helper. Add before it:

```rust
fn days_in_month_local(year: i32, month: u32) -> u32 {
    let start = NaiveDate::from_ymd_opt(year, month, 1).unwrap();
    let next = if month == 12 {
        NaiveDate::from_ymd_opt(year + 1, 1, 1).unwrap()
    } else {
        NaiveDate::from_ymd_opt(year, month + 1, 1).unwrap()
    };
    next.signed_duration_since(start).num_days() as u32
}
```

- [ ] **Step 4: Add RecurrenceAnchor variant to CompiledConditionTree**

In the `CompiledConditionTree` enum, find the `InstitutionId` variant. Add after it and before the `// --- Entry targets ---` comment:

```rust
    // --- Transaction-target cadence condition (Pass 1) ---
    /// Matches transactions whose `date` falls within `TIMING_VARIANCE_THRESHOLD_DAYS`
    /// of the anchor's expected fire date.
    ///
    /// Calendar anchors (`dom:N`, `dow:N`) are evaluated per-transaction.
    /// Interval anchors (`interval:N`) return `None` here — the ordered timing
    /// phase in stage1's `run()` handles them via group evaluation.
    ///
    /// Anchor formats:
    /// - `dom:N`   — day N of month; -1 = last day
    /// - `dom:N,M` — semi-monthly; within tolerance of either
    /// - `dow:N`   — weekday 0=Mon … 6=Sun; exact match, no tolerance
    /// - `interval:N` — every N days; not evaluated per-transaction
    RecurrenceAnchor {
        anchor: String,
    },
```

- [ ] **Step 5: Add evaluator arm in evaluate()**

Find the `evaluate` function, the `DateRange` arm. Add after it (before `AccountId`):

```rust
        CompiledConditionTree::RecurrenceAnchor { anchor } => {
            let tol     = super::TIMING_VARIANCE_THRESHOLD_DAYS as i32;
            let txn_day = txn.date.day() as i32;
            let year    = txn.date.year();
            let month   = txn.date.month();

            if let Some(rest) = anchor.strip_prefix("dom:") {
                if let Some(comma) = rest.find(',') {
                    let a: i32 = rest[..comma].parse().ok()?;
                    let b: i32 = rest[comma + 1..].parse().ok()?;
                    let resolve = |t: i32| -> i32 {
                        if t > 0 { t } else { days_in_month_local(year, month) as i32 + t + 1 }
                    };
                    if (txn_day - resolve(a)).abs() <= tol || (txn_day - resolve(b)).abs() <= tol {
                        Some(1.0)
                    } else {
                        None
                    }
                } else {
                    let target: i32 = rest.parse().ok()?;
                    let resolved = if target > 0 {
                        target
                    } else {
                        days_in_month_local(year, month) as i32 + target + 1
                    };
                    if (txn_day - resolved).abs() <= tol { Some(1.0) } else { None }
                }
            } else if let Some(rest) = anchor.strip_prefix("dow:") {
                let target_dow: u32 = rest.parse().ok()?;
                if txn.date.weekday().num_days_from_monday() == target_dow { Some(1.0) } else { None }
            } else {
                // interval:N — group evaluation in the ordered timing phase; None here
                None
            }
        }
```

- [ ] **Step 6: Add compile case in compile_tree()**

Find the `"date_day_of_month"` compile arm. Add directly after it:

```rust
        "recurrence_anchor" => {
            let anchor = string_value(v, "recurrence_anchor")?;
            Ok(CompiledConditionTree::RecurrenceAnchor { anchor })
        }
```

- [ ] **Step 7: Verify tree_has_entry_targets catch-all**

Find `tree_has_entry_targets`. Confirm the final arm is `_ => false`. No change required — `RecurrenceAnchor` falls through to `false` automatically, keeping it in Pass 1.

- [ ] **Step 8: Run tests**

```bash
cd services/engine && cargo test recurrence_anchor_cond_tests && cargo test
```

Expected: all pass.

- [ ] **Step 9: Commit**

```bash
git add services/engine/src/pipeline/stage1.rs
git commit -m "feat: add RecurrenceAnchor transaction-target condition to stage1 (dom/dow per-transaction; interval:N deferred to group timing phase)"
```

---

## Task 3: Refactor stage1 to per-entry ordered phase evaluation

**Files:**
- Modify: `services/engine/src/pipeline/stage1.rs`

**Interfaces:**
- Consumes: `RecurrenceAnchor` variant (Task 2), `TIMING_VARIANCE_THRESHOLD_DAYS` (Task 1)
- Produces:
  - `evaluate_entry_phases(entry, txns) -> Vec<(Uuid, f64)>` — evaluates one entry against all transactions using ordered phases; returns `(txn_id, fit)` pairs
  - `apply_interval_timing(n: i64, tol: i64, candidates) -> Vec<(usize, f64)>` — finds chains of transactions spaced ~N days apart (±tol) in a sorted candidate slice
  - Stage1 `run()` changes outer loop from per-transaction `par_iter` to per-entry `par_iter` for Pass 1, then reassembles per-transaction for Pass 2+

**Evaluation order:** payee conditions → timing conditions → amount conditions. Each phase receives only candidates that passed the previous phase. Timing phase uses group evaluation for `interval:N` and per-candidate evaluation for all other anchor types.

- [ ] **Step 1: Write failing tests for the interval timing group evaluation**

In the stage1 test module, add:

```rust
#[cfg(test)]
mod interval_timing_tests {
    use super::*;
    use chrono::NaiveDate;

    fn d(s: &str) -> NaiveDate { s.parse().unwrap() }

    fn make_txns(dates: &[&str]) -> Vec<TransactionRow> {
        dates.iter().map(|s| TransactionRow {
            id:                  uuid::Uuid::new_v4(),
            account_id:          uuid::Uuid::new_v4(),
            institution_id:      None,
            date:                d(s),
            amount_cents:        -50000,
            merchant_normalized: "quarterly".to_string(),
        }).collect()
    }

    #[test]
    fn chain_of_three_quarterly() {
        let txns = make_txns(&["2026-01-05", "2026-04-03", "2026-07-02"]);
        let refs: Vec<(&TransactionRow, f64)> = txns.iter().map(|t| (t, 1.0)).collect();
        let result = apply_interval_timing(91, 5, refs);
        assert_eq!(result.len(), 3);
    }

    #[test]
    fn random_interloper_excluded() {
        // Regular quarterly + one random charge in between
        let txns = make_txns(&["2026-01-05", "2026-03-01", "2026-04-03", "2026-07-02"]);
        let refs: Vec<(&TransactionRow, f64)> = txns.iter().map(|t| (t, 1.0)).collect();
        let result = apply_interval_timing(91, 5, refs);
        // 2026-03-01 is 55 days after Jan 5 and 33 days before Apr 3 — not ~91 from either
        assert_eq!(result.len(), 3);
        let matched_dates: Vec<_> = result.iter().map(|(t, _)| t.date.to_string()).collect();
        assert!(!matched_dates.contains(&"2026-03-01".to_string()));
    }

    #[test]
    fn within_tolerance_matches() {
        // 93 days apart — within ±5 of 91
        let txns = make_txns(&["2026-01-05", "2026-04-08"]);
        let refs: Vec<(&TransactionRow, f64)> = txns.iter().map(|t| (t, 1.0)).collect();
        let result = apply_interval_timing(91, 5, refs);
        assert_eq!(result.len(), 2);
    }

    #[test]
    fn outside_tolerance_no_chain() {
        // 120 days apart — outside ±5 of 91
        let txns = make_txns(&["2026-01-05", "2026-05-05"]);
        let refs: Vec<(&TransactionRow, f64)> = txns.iter().map(|t| (t, 1.0)).collect();
        let result = apply_interval_timing(91, 5, refs);
        assert_eq!(result.len(), 0);
    }

    #[test]
    fn single_candidate_returns_empty() {
        let txns = make_txns(&["2026-01-05"]);
        let refs: Vec<(&TransactionRow, f64)> = txns.iter().map(|t| (t, 1.0)).collect();
        let result = apply_interval_timing(91, 5, refs);
        assert_eq!(result.len(), 0);
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/engine && cargo test interval_timing_tests
```

Expected: compile error — `apply_interval_timing` not defined.

- [ ] **Step 3: Add apply_interval_timing**

Add before the `evaluate_entry` public wrapper function:

```rust
/// Find all transactions in `candidates` that participate in an interval:N chain.
///
/// Sorts candidates by date, then marks both members of any pair whose date gap
/// is within [n - tol, n + tol]. Transactions not paired with any adjacent
/// interval-spaced neighbour are excluded.
///
/// O(k²) in the number of payee-scoped candidates — acceptable because
/// per-entry candidate sets are small (typically < 20 transactions).
pub(crate) fn apply_interval_timing<'a>(
    n:          i64,
    tol:        i64,
    mut candidates: Vec<(&'a TransactionRow, f64)>,
) -> Vec<(&'a TransactionRow, f64)> {
    if n <= 0 || candidates.len() < 2 {
        return vec![];
    }

    candidates.sort_by_key(|(txn, _)| txn.date);
    let k = candidates.len();
    let mut in_chain = vec![false; k];

    for i in 0..k {
        for j in (i + 1)..k {
            let gap = (candidates[j].0.date - candidates[i].0.date).num_days();
            if gap > n + tol {
                break; // sorted — no further j can be closer to n
            }
            if (gap - n).abs() <= tol {
                in_chain[i] = true;
                in_chain[j] = true;
            }
        }
    }

    candidates
        .into_iter()
        .zip(in_chain)
        .filter_map(|(c, keep)| if keep { Some(c) } else { None })
        .collect()
}
```

- [ ] **Step 4: Run interval timing tests**

```bash
cd services/engine && cargo test interval_timing_tests
```

Expected: all pass.

- [ ] **Step 5: Add phase helper functions**

Add these helper functions (before `evaluate_entry`). They extract conditions from an AND tree by category and evaluate each phase:

```rust
/// Extract the `RecurrenceAnchor` node from the top-level AND tree, if present.
/// Returns `None` if no timing condition exists or the tree is not a flat AND.
fn extract_anchor_node(tree: &CompiledConditionTree) -> Option<&str> {
    let children = match tree {
        CompiledConditionTree::And(c) => c,
        CompiledConditionTree::RecurrenceAnchor { anchor } => return Some(anchor.as_str()),
        _ => return None,
    };
    for child in children {
        if let CompiledConditionTree::RecurrenceAnchor { anchor } = child {
            return Some(anchor.as_str());
        }
    }
    None
}

/// Evaluate all payee-type conditions in `tree` against `txn`.
/// Returns `Some(fit)` if all payee conditions pass, `None` if any fail.
/// Non-payee conditions in the tree are ignored (treated as pass).
fn eval_payee_conditions(tree: &CompiledConditionTree, txn: &TransactionRow) -> Option<f64> {
    match tree {
        CompiledConditionTree::And(children) => {
            let mut fit = 1.0_f64;
            for child in children {
                match child {
                    CompiledConditionTree::PayeeExact(_)
                    | CompiledConditionTree::PayeeContains(_)
                    | CompiledConditionTree::PayeeNotContains(_)
                    | CompiledConditionTree::PayeeStartsWith(_)
                    | CompiledConditionTree::PayeeEndsWith(_)
                    | CompiledConditionTree::PayeeRegex(_)
                    | CompiledConditionTree::PayeeOneOf(_) => {
                        match evaluate(child, txn, &Default::default(), &[]) {
                            None    => return None,
                            Some(f) => fit = fit.min(f),
                        }
                    }
                    _ => {} // non-payee conditions skipped in this phase
                }
            }
            Some(if fit == 1.0 { 1.0 } else { fit })
        }
        // Bare payee leaf
        CompiledConditionTree::PayeeExact(_)
        | CompiledConditionTree::PayeeContains(_)
        | CompiledConditionTree::PayeeStartsWith(_)
        | CompiledConditionTree::PayeeEndsWith(_)
        | CompiledConditionTree::PayeeNotContains(_)
        | CompiledConditionTree::PayeeRegex(_)
        | CompiledConditionTree::PayeeOneOf(_) => {
            evaluate(tree, txn, &Default::default(), &[])
        }
        _ => Some(1.0), // no payee condition → pass all
    }
}

/// Evaluate all amount-type conditions in `tree` against `txn`.
fn eval_amount_conditions(tree: &CompiledConditionTree, txn: &TransactionRow) -> Option<f64> {
    match tree {
        CompiledConditionTree::And(children) => {
            let mut fit = 1.0_f64;
            for child in children {
                if let CompiledConditionTree::AmountRange { .. } = child {
                    match evaluate(child, txn, &Default::default(), &[]) {
                        None    => return None,
                        Some(f) => fit = fit.min(f),
                    }
                }
            }
            Some(fit)
        }
        CompiledConditionTree::AmountRange { .. } => {
            evaluate(tree, txn, &Default::default(), &[])
        }
        _ => Some(1.0),
    }
}

/// Evaluate one entry against all transactions using ordered phases:
/// payee → timing → amount.
///
/// Returns matched `(txn_id, fit)` pairs.
pub(crate) fn evaluate_entry_phases(
    entry: &CompiledEntry,
    txns:  &[TransactionRow],
) -> Vec<(Uuid, f64)> {
    // Phase 1: payee filter
    let mut candidates: Vec<(&TransactionRow, f64)> = txns
        .iter()
        .filter_map(|txn| eval_payee_conditions(&entry.conditions, txn).map(|f| (txn, f)))
        .collect();

    if candidates.is_empty() {
        return vec![];
    }

    // Phase 2: timing
    let anchor = extract_anchor_node(&entry.conditions);
    candidates = match anchor {
        None => candidates,
        Some(a) if a.starts_with("interval:") => {
            let n: i64 = a[9..].parse().unwrap_or(0);
            let tol = super::TIMING_VARIANCE_THRESHOLD_DAYS as i64;
            apply_interval_timing(n, tol, candidates)
        }
        Some(_) => {
            // Calendar anchor (dom:N, dow:N) — per-candidate evaluation
            candidates
                .into_iter()
                .filter_map(|(txn, fit)| {
                    evaluate(&entry.conditions, txn, &Default::default(), &[])
                        .map(|tf| (txn, fit.min(tf)))
                })
                .collect()
        }
    };

    if candidates.is_empty() {
        return vec![];
    }

    // Phase 3: amount filter
    candidates
        .into_iter()
        .filter_map(|(txn, fit)| {
            eval_amount_conditions(&entry.conditions, txn).map(|af| (txn.id, fit.min(af)))
        })
        .collect()
}
```

Note: Phase 2 for calendar anchors uses `evaluate` on the full condition tree. This is slightly redundant (it re-evaluates payee too), but the payee already passed Phase 1, so the AND will still return the same result. An optimisation can remove the redundancy later if needed.

- [ ] **Step 6: Update run() to use per-entry evaluation for Pass 1**

In the `run()` function, find the section that partitions entries and runs the `par_iter` over transactions. The current code:

```rust
let (txn_entries, entry_entries): (Vec<&CompiledEntry>, Vec<&CompiledEntry>) =
    compiled_entries.iter().partition(|e| !tree_has_entry_targets(&e.conditions));

let results: Vec<(Uuid, Vec<(Uuid, f64)>, bool)> = txns
    .par_iter()
    .map(|txn| { ... })
    .collect();
```

Replace with:

```rust
let (txn_entries, entry_entries): (Vec<&CompiledEntry>, Vec<&CompiledEntry>) =
    compiled_entries.iter().partition(|e| !tree_has_entry_targets(&e.conditions));

// Pass 1: per-entry ordered phase evaluation (payee → timing → amount).
// Parallelises over entries; each entry independently evaluates against all transactions.
use std::collections::HashMap;
use std::sync::Mutex;

let pass1_map: Mutex<HashMap<Uuid, Vec<(Uuid, f64)>>> = Mutex::new(HashMap::new());

txn_entries.par_iter().for_each(|entry| {
    let matches = evaluate_entry_phases(entry, &txns);
    if !matches.is_empty() {
        let mut map = pass1_map.lock().unwrap();
        for (txn_id, fit) in matches {
            map.entry(txn_id)
               .or_default()
               .push((entry.entry_id, fit));
        }
    }
});

let pass1_assignments = pass1_map.into_inner().unwrap();

// Pass 2+: per-transaction, evaluate entry-target conditions using accumulated
// Pass 1 metadata as context.
let results: Vec<(Uuid, Vec<(Uuid, f64)>, bool)> = txns
    .par_iter()
    .map(|txn| {
        let mut matched: Vec<(Uuid, f64)> = pass1_assignments
            .get(&txn.id)
            .cloned()
            .unwrap_or_default();

        // Reconstruct accumulated metadata from Pass 1 entries that matched this txn.
        let mut accumulated: Vec<AccumulatedEntryMeta> = matched
            .iter()
            .filter_map(|(entry_id, _)| {
                txn_entries.iter().find(|e| e.entry_id == *entry_id).map(|e| AccumulatedEntryMeta {
                    label_id:               e.label_id,
                    direction:              e.direction,
                    entry_type:             e.entry_type,
                    period_days:            e.period_days,
                    source:                 e.source,
                    fitness:             e.fitness,
                    merchant_fit:        e.merchant_fit,
                    timing_fit:          e.timing_fit,
                    amount_fit:          e.amount_fit,
                    projected_rate_per_day: e.projected_rate_per_day,
                    recurrence_anchor:      e.recurrence_anchor.clone(),
                })
            })
            .collect();

        let mut label_index: HashSet<Uuid> = accumulated
            .iter()
            .filter_map(|m| m.label_id)
            .collect();

        // Pass 2+ iterations: entry-target conditions
        let mut matched_set: HashSet<Uuid> = matched.iter().map(|(id, _)| *id).collect();

        loop {
            let pass_accumulated = accumulated.len();
            let mut newly_matched: Vec<(Uuid, f64)> = Vec::new();
            let mut new_meta: Vec<AccumulatedEntryMeta> = Vec::new();
            let mut new_labels: HashSet<Uuid> = HashSet::new();
            let mut cycle_detected = false;

            for entry in &entry_entries {
                if let Some(fit) = evaluate(&entry.conditions, txn, &label_index, &accumulated) {
                    if let Some(label_id) = entry.label_id {
                        if label_index.contains(&label_id) {
                            tracing::info!(
                                txn_id   = %txn.id,
                                entry_id = %entry.entry_id,
                                %label_id,
                                "stage 1: label cycle detected, terminating expansion"
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

            for (entry_id, fit) in newly_matched {
                if matched_set.insert(entry_id) {
                    matched.push((entry_id, fit));
                }
            }
            label_index.extend(new_labels);
            accumulated.extend(new_meta);

            if cycle_detected || accumulated.len() == pass_accumulated {
                break;
            }
        }

        let unmatched = matched.is_empty();
        (txn.id, matched, unmatched)
    })
    .collect();
```

- [ ] **Step 7: Run all engine tests**

```bash
cd services/engine && cargo test
```

Expected: all pass. Pay attention to the existing integration tests in stage1 — confirm assignment counts are consistent.

- [ ] **Step 8: Commit**

```bash
git add services/engine/src/pipeline/stage1.rs
git commit -m "feat: stage1 per-entry ordered phase evaluation (payee→timing→amount); interval:N group chain detection"
```

---

## Task 4: Update stage2 to generate full conditions

**Files:**
- Modify: `services/engine/src/pipeline/stage2.rs`

**Interfaces:**
- Consumes: `TIMING_VARIANCE_THRESHOLD_DAYS` (Task 1), `RecurrenceAnchor` Schema A format (Task 2)
- Produces: `persist_cluster` generates:
  - Standing: `{"op":"AND","children":[payee_node, recurrence_anchor_node, amount_range_node]}`
  - Variable: `{"op":"AND","children":[payee_node]}` (single child AND, unchanged structure for schema compat)
  - `recurrence_anchor` node: `{"type":"recurrence_anchor","recurrence_anchor":"dom:15","tolerance_days":5}`
  - `amount_range` node: `{"type":"amount_range","min_cents":<observed_min>,"max_cents":<observed_max>}`

- [ ] **Step 1: Write failing tests for condition generation**

In `services/engine/src/pipeline/stage2.rs`, find the test module. Add:

```rust
#[cfg(test)]
mod condition_generation_tests {
    use super::*;
    use chrono::NaiveDate;

    fn d(s: &str) -> NaiveDate { s.parse().unwrap() }

    fn cluster(merchant: &str, txns: Vec<(&str, i64)>) -> Cluster {
        Cluster {
            merchant: merchant.to_string(),
            transactions: txns.into_iter().map(|(date, amount)| UnmatchedTxn {
                id:                  uuid::Uuid::new_v4(),
                date:                d(date),
                amount_cents:        amount,
                merchant_normalized: merchant.to_string(),
            }).collect(),
        }
    }

    #[test]
    fn standing_dom_conditions_include_all_three() {
        // Monthly cluster on the 15th, consistent amounts
        let c = cluster("NETFLIX", vec![
            ("2026-01-15", -1599),
            ("2026-02-15", -1599),
            ("2026-03-15", -1599),
        ]);
        let conditions = build_conditions(&c, &score_cluster(&c));
        let children = conditions["children"].as_array().unwrap();
        assert_eq!(children.len(), 3, "standing entry must have payee + timing + amount");

        let types: Vec<_> = children.iter()
            .filter_map(|c| c["type"].as_str())
            .collect();
        assert!(types.iter().any(|&t| t.starts_with("payee_")));
        assert!(types.contains(&"recurrence_anchor"));
        assert!(types.contains(&"amount_range"));
    }

    #[test]
    fn standing_amount_clamp_is_observed_min_max() {
        let c = cluster("PSEG", vec![
            ("2026-01-01", -8000),
            ("2026-02-01", -12000),
            ("2026-03-01", -9500),
        ]);
        let conds = build_conditions(&c, &score_cluster(&c));
        let amount = conds["children"].as_array().unwrap()
            .iter().find(|c| c["type"] == "amount_range").unwrap();
        assert_eq!(amount["min_cents"].as_i64().unwrap(), -12000);
        assert_eq!(amount["max_cents"].as_i64().unwrap(), -8000);
    }

    #[test]
    fn variable_conditions_payee_only() {
        // Single transaction — no interval, falls to variable
        let c = cluster("AMAZON", vec![
            ("2026-01-10", -2999),
        ]);
        let conds = build_conditions(&c, &score_cluster(&c));
        let children = conds["children"].as_array().unwrap();
        assert_eq!(children.len(), 1);
        assert!(children[0]["type"].as_str().unwrap().starts_with("payee_"));
    }

    #[test]
    fn interval_anchor_included_for_standing() {
        // Quarterly cluster
        let c = cluster("AWS ANNUAL", vec![
            ("2026-01-05", -50000),
            ("2026-04-07", -52000),
        ]);
        let score = score_cluster(&c);
        let conds = build_conditions(&c, &score);
        let anchor_node = conds["children"].as_array().unwrap()
            .iter().find(|c| c["type"] == "recurrence_anchor");
        assert!(anchor_node.is_some());
        let anchor = anchor_node.unwrap()["recurrence_anchor"].as_str().unwrap();
        assert!(anchor.starts_with("interval:"));
        assert_eq!(anchor_node.unwrap()["tolerance_days"].as_i64().unwrap(), 5);
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/engine && cargo test condition_generation_tests
```

Expected: compile error — `build_conditions` not defined.

- [ ] **Step 3: Extract build_conditions from persist_cluster**

In `services/engine/src/pipeline/stage2.rs`, extract condition building from `persist_cluster` into a standalone pure function:

```rust
/// Build the conditions JSONB for a cluster entry.
///
/// Standing entries get all three conditions: payee + recurrence_anchor + amount_range.
/// Variable entries get payee only.
///
/// Amount clamp: raw observed min/max from the cluster — no buffer.
/// Drift proposals handle future expansion if a new transaction falls outside the clamp.
pub(crate) fn build_conditions(cluster: &Cluster, score: &ClusterScore) -> serde_json::Value {
    let canonical_lower = cluster.merchant.to_ascii_lowercase();
    let all_start_with = cluster.transactions.iter().all(|t| {
        t.merchant_normalized.to_ascii_lowercase().starts_with(&canonical_lower)
    });
    let payee_type = if all_start_with { "payee_starts_with" } else { "payee_contains" };

    let payee_node = serde_json::json!({"type": payee_type, "value": &cluster.merchant});

    if score.entry_type == "variable" {
        return serde_json::json!({"op": "AND", "children": [payee_node]});
    }

    // Standing: add timing + amount.
    let mut children = vec![payee_node];

    // Timing: recurrence_anchor if an anchor was detected.
    if let Some(mean_interval) = score.mean_interval_days {
        let mut dates: Vec<_> = cluster.transactions.iter().map(|t| t.date).collect();
        dates.sort_unstable();
        if let Some(anchor) = detect_anchor(&dates, mean_interval) {
            children.push(serde_json::json!({
                "type":              "recurrence_anchor",
                "recurrence_anchor": anchor,
                "tolerance_days":    TIMING_VARIANCE_THRESHOLD_DAYS as u8,
            }));
        }
    }

    // Amount: observed min/max clamp (cents, always negative for spend).
    let amounts: Vec<i64> = cluster.transactions.iter().map(|t| t.amount_cents).collect();
    let min_cents = *amounts.iter().min().unwrap();
    let max_cents = *amounts.iter().max().unwrap();
    children.push(serde_json::json!({
        "type":      "amount_range",
        "min_cents": min_cents,
        "max_cents": max_cents,
    }));

    serde_json::json!({"op": "AND", "children": children})
}
```

- [ ] **Step 4: Update persist_cluster to use build_conditions**

In `persist_cluster`, replace the existing condition building block:

```rust
let condition_type = if all_start_with { "payee_starts_with" } else { "payee_contains" };
let conditions = serde_json::json!({
    "op": "AND",
    "children": [{"type": condition_type, "value": &cluster.merchant}]
});
```

With:

```rust
let conditions = build_conditions(cluster, score);
```

Remove any now-redundant local variables (`canonical_lower`, `all_start_with`, `condition_type`).

- [ ] **Step 5: Run tests**

```bash
cd services/engine && cargo test condition_generation_tests && cargo test
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add services/engine/src/pipeline/stage2.rs
git commit -m "feat: stage2 generates full conditions (payee+anchor+amount clamp) for standing entries"
```

---

## Task 5: Add entry_has_lapsed helper to stage7

**Files:**
- Modify: `services/engine/src/pipeline/stage7.rs`

**Interfaces:**
- Consumes: `super::TIMING_VARIANCE_THRESHOLD_DAYS` (Task 1)
- Produces: `pub(crate) fn entry_has_lapsed(next_due: NaiveDate, computed_as_of: NaiveDate, tolerance_days: i64) -> bool`

Phase 1's `detect_ended_entries` will call `entry_has_lapsed(next_due, computed_as_of, super::TIMING_VARIANCE_THRESHOLD_DAYS as i64)`.

- [ ] **Step 1: Write the failing test**

```rust
#[cfg(test)]
mod lapse_tests {
    use super::*;
    use chrono::NaiveDate;

    fn d(s: &str) -> NaiveDate { s.parse().unwrap() }

    #[test]
    fn on_next_due_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-15"), 5)); }

    #[test]
    fn within_tolerance_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-18"), 5)); }

    #[test]
    fn at_boundary_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-20"), 5)); }

    #[test]
    fn one_past_tolerance_lapsed() { assert!(entry_has_lapsed(d("2026-03-15"), d("2026-03-21"), 5)); }

    #[test]
    fn far_past_lapsed() { assert!(entry_has_lapsed(d("2026-03-01"), d("2026-04-15"), 5)); }

    #[test]
    fn before_next_due_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-01"), 5)); }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd services/engine && cargo test lapse_tests
```

Expected: compile error.

- [ ] **Step 3: Add entry_has_lapsed**

After `const PROJECTION_DAYS: i64 = 90;`, add:

```rust
/// Returns true when `computed_as_of` has passed `next_due_date` by more than
/// `tolerance_days`. Called by Phase 1's `detect_ended_entries`.
/// Pass `super::TIMING_VARIANCE_THRESHOLD_DAYS as i64` for the tolerance.
pub(crate) fn entry_has_lapsed(
    next_due:       chrono::NaiveDate,
    computed_as_of: chrono::NaiveDate,
    tolerance_days: i64,
) -> bool {
    computed_as_of > next_due + chrono::Duration::days(tolerance_days)
}
```

- [ ] **Step 4: Run all tests**

```bash
cd services/engine && cargo test
```

- [ ] **Step 5: Commit**

```bash
git add services/engine/src/pipeline/stage7.rs
git commit -m "feat: add entry_has_lapsed helper to stage7 using TIMING_VARIANCE_THRESHOLD_DAYS"
```

---

## Task 6: Go store — cadence Schema B ↔ recurrence_anchor Schema A translation

**Files:**
- Modify: `services/web/store/conditions.go`
- Modify: `services/web/store/conditions_test.go`

**Interfaces:**
- Produces: `cadenceToAnchor(cadence string) (string, error)`, `anchorToCadence(anchor string) (string, error)`, `timingVarianceThresholdDays = 5`
- Schema B `{"cadence":"monthly:15"}` → Schema A `{"type":"recurrence_anchor","recurrence_anchor":"dom:15","tolerance_days":5}`
- Schema A `{"type":"recurrence_anchor",...}` → Schema B `{"cadence":"monthly:15"}`
- Schema A `{"type":"entry_recurrence_anchor",...}` → Schema B `{"entry_recurrence_anchor":...}` (unchanged)

- [ ] **Step 1: Write failing tests**

In `services/web/store/conditions_test.go`, add:

```go
func TestCadenceToAnchor(t *testing.T) {
    cases := []struct{ cadence, want string }{
        {"monthly:15", "dom:15"},
        {"monthly:1", "dom:1"},
        {"monthly:last", "dom:-1"},
        {"semimonthly:1,15", "dom:1,15"},
        {"weekly:monday", "dow:0"},
        {"weekly:tuesday", "dow:1"},
        {"weekly:wednesday", "dow:2"},
        {"weekly:thursday", "dow:3"},
        {"weekly:friday", "dow:4"},
        {"weekly:saturday", "dow:5"},
        {"weekly:sunday", "dow:6"},
        {"every:30", "interval:30"},
        {"every:91", "interval:91"},
    }
    for _, tc := range cases {
        got, err := cadenceToAnchor(tc.cadence)
        if err != nil { t.Errorf("cadenceToAnchor(%q): %v", tc.cadence, err); continue }
        if got != tc.want { t.Errorf("cadenceToAnchor(%q) = %q, want %q", tc.cadence, got, tc.want) }
    }
}

func TestAnchorToCadence(t *testing.T) {
    cases := []struct{ anchor, want string }{
        {"dom:15", "monthly:15"},
        {"dom:-1", "monthly:last"},
        {"dom:1,15", "semimonthly:1,15"},
        {"dow:0", "weekly:monday"},
        {"dow:4", "weekly:friday"},
        {"interval:91", "every:91"},
    }
    for _, tc := range cases {
        got, err := anchorToCadence(tc.anchor)
        if err != nil { t.Errorf("anchorToCadence(%q): %v", tc.anchor, err); continue }
        if got != tc.want { t.Errorf("anchorToCadence(%q) = %q, want %q", tc.anchor, got, tc.want) }
    }
}

func TestStorageNodeCadence(t *testing.T) {
    lu := storageLookups{
        accountsByName: map[string]string{},
        accountsByID:   map[string]bool{},
        instByName:     map[string]string{},
        instByID:       map[string]bool{},
    }
    var resolveErr error
    noLabel := func(string) (string, error) { return "", fmt.Errorf("no label") }

    cases := []struct{ schemaB map[string]any; wantAnchor string }{
        {map[string]any{"cadence": "monthly:15"}, "dom:15"},
        {map[string]any{"cadence": "monthly:last"}, "dom:-1"},
        {map[string]any{"cadence": "semimonthly:1,15"}, "dom:1,15"},
        {map[string]any{"cadence": "weekly:monday"}, "dow:0"},
        {map[string]any{"cadence": "every:91"}, "interval:91"},
    }
    for _, tc := range cases {
        resolveErr = nil
        got := storageNode(tc.schemaB, lu, noLabel, &resolveErr)
        if resolveErr != nil { t.Fatalf("%v: %v", tc.schemaB, resolveErr) }
        if got["type"] != "recurrence_anchor" { t.Errorf("%v: type = %v", tc.schemaB, got["type"]) }
        if got["recurrence_anchor"] != tc.wantAnchor { t.Errorf("%v: anchor = %v, want %v", tc.schemaB, got["recurrence_anchor"], tc.wantAnchor) }
        if got["tolerance_days"] != timingVarianceThresholdDays { t.Errorf("%v: tolerance_days = %v", tc.schemaB, got["tolerance_days"]) }
    }
}

func TestDisplayNodeRecurrenceAnchor(t *testing.T) {
    lu := displayLookups{labelsByID: map[string]string{}, accountsByID: map[string]string{}, instByID: map[string]string{}}
    cases := []struct{ schemaA map[string]any; want string }{
        {map[string]any{"type": "recurrence_anchor", "recurrence_anchor": "dom:15"}, "monthly:15"},
        {map[string]any{"type": "recurrence_anchor", "recurrence_anchor": "dom:-1"}, "monthly:last"},
        {map[string]any{"type": "recurrence_anchor", "recurrence_anchor": "dom:1,15"}, "semimonthly:1,15"},
        {map[string]any{"type": "recurrence_anchor", "recurrence_anchor": "dow:0"}, "weekly:monday"},
        {map[string]any{"type": "recurrence_anchor", "recurrence_anchor": "interval:91"}, "every:91"},
    }
    for _, tc := range cases {
        got := displayNode(tc.schemaA, lu)
        if got["cadence"] != tc.want { t.Errorf("%v: cadence = %v, want %v", tc.schemaA, got["cadence"], tc.want) }
        if _, has := got["recurrence_anchor"]; has { t.Errorf("%v: should not have recurrence_anchor key", tc.schemaA) }
    }
}

func TestEntryRecurrenceAnchorUnchanged(t *testing.T) {
    lu := displayLookups{labelsByID: map[string]string{}, accountsByID: map[string]string{}, instByID: map[string]string{}}
    node := map[string]any{"type": "entry_recurrence_anchor", "recurrence_anchor": "dom:15"}
    got := displayNode(node, lu)
    if got["entry_recurrence_anchor"] != "dom:15" { t.Errorf("entry_recurrence_anchor should pass through, got %v", got) }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/web && go test ./store/... -run "TestCadence|TestAnchorTo|TestStorageNode|TestDisplayNode|TestEntry" -v 2>&1 | head -20
```

- [ ] **Step 3: Add helpers and constant to conditions.go**

Add imports `"strconv"` if missing. After the import block, add:

```go
const timingVarianceThresholdDays = 5

var dowNames = [7]string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

func cadenceToAnchor(cadence string) (string, error) {
    switch {
    case strings.HasPrefix(cadence, "monthly:"):
        val := cadence[len("monthly:"):]
        if val == "last" { return "dom:-1", nil }
        if _, err := strconv.Atoi(val); err != nil {
            return "", fmt.Errorf("invalid monthly cadence %q", val)
        }
        return "dom:" + val, nil
    case strings.HasPrefix(cadence, "semimonthly:"):
        return "dom:" + cadence[len("semimonthly:"):], nil
    case strings.HasPrefix(cadence, "weekly:"):
        day := strings.ToLower(cadence[len("weekly:"):])
        for i, name := range dowNames {
            if name == day { return fmt.Sprintf("dow:%d", i), nil }
        }
        return "", fmt.Errorf("unknown weekday %q", day)
    case strings.HasPrefix(cadence, "every:"):
        n := cadence[len("every:"):]
        if _, err := strconv.Atoi(n); err != nil {
            return "", fmt.Errorf("invalid every cadence %q", n)
        }
        return "interval:" + n, nil
    default:
        return "", fmt.Errorf("unrecognised cadence %q", cadence)
    }
}

func anchorToCadence(anchor string) (string, error) {
    switch {
    case strings.HasPrefix(anchor, "dom:"):
        parts := anchor[4:]
        if strings.Contains(parts, ",") { return "semimonthly:" + parts, nil }
        if parts == "-1" { return "monthly:last", nil }
        return "monthly:" + parts, nil
    case strings.HasPrefix(anchor, "dow:"):
        idx, err := strconv.Atoi(anchor[4:])
        if err != nil || idx < 0 || idx > 6 { return "", fmt.Errorf("invalid dow %q", anchor) }
        return "weekly:" + dowNames[idx], nil
    case strings.HasPrefix(anchor, "interval:"):
        return "every:" + anchor[len("interval:"):], nil
    default:
        return "", fmt.Errorf("unrecognised anchor %q", anchor)
    }
}
```

- [ ] **Step 4: Update displayNode — split entry_recurrence_anchor alias**

Find:

```go
// Both old ("recurrence_anchor") and new ("entry_recurrence_anchor") Schema A types.
case "entry_recurrence_anchor", "recurrence_anchor":
    anchor, _ := node["recurrence_anchor"].(string)
    return map[string]any{"entry_recurrence_anchor": anchor}
```

Replace with two cases:

```go
case "entry_recurrence_anchor":
    anchor, _ := node["recurrence_anchor"].(string)
    return map[string]any{"entry_recurrence_anchor": anchor}

case "recurrence_anchor":
    anchor, _ := node["recurrence_anchor"].(string)
    cadence, err := anchorToCadence(anchor)
    if err != nil { return node }
    return map[string]any{"cadence": cadence}
```

- [ ] **Step 5: Add cadence case to storageNode**

Find the `"date_day_of_month"` case. Add after it:

```go
case "cadence":
    cadenceStr, _ := val.(string)
    anchor, err := cadenceToAnchor(cadenceStr)
    if err != nil { *resolveErr = fmt.Errorf("invalid cadence: %w", err); return node }
    return map[string]any{
        "type":               "recurrence_anchor",
        "recurrence_anchor":  anchor,
        "tolerance_days":     timingVarianceThresholdDays,
    }
```

- [ ] **Step 6: Run tests**

```bash
cd services/web && go test ./store/... && go test ./...
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add services/web/store/conditions.go services/web/store/conditions_test.go
git commit -m "feat: cadence Schema B key translates to recurrence_anchor Schema A; entry_recurrence_anchor unchanged"
```

---

## Task 7: Update veloci-ref.md and conditions-editor.md

**Files:**
- Modify: `docs/veloci-ref.md`
- Modify: `docs/conditions-editor.md`

- [ ] **Step 1: Update conditions Schema A table in veloci-ref.md**

In the "Transaction-target leaves" table, add after `date_day_of_month`:

```
recurrence_anchor | `{"type":"recurrence_anchor","recurrence_anchor":"dom:15","tolerance_days":5}` |
```

Below the table, add:

```markdown
- `recurrence_anchor`: transaction-target cadence condition — matches transactions whose
  `date` falls within `tolerance_days` of the anchor's expected fire date. Replaces
  `date_day_of_month`. Supported anchors: `dom:N` (1–28; `-1` = last day), `dom:N,M`
  (semi-monthly), `dow:N` (0=Mon … 6=Sun; exact), `interval:N` (every N days — evaluated
  via consecutive-spacing chain detection in stage1's group timing phase, not per-transaction).
  `tolerance_days` should always equal `TIMING_VARIANCE_THRESHOLD_DAYS` (5).
- `date_day_of_month` is **deprecated** — do not use in new conditions. Evaluator is kept
  for existing user entries.
```

- [ ] **Step 2: Add cadence to Schema B table in veloci-ref.md**

Add row and notes:

```markdown
- `cadence`: human-readable scheduling condition. Maps to `recurrence_anchor` Schema A at
  the store boundary. `tolerance_days` always 5 (implicit). Forms:
  - `"monthly:N"` → `dom:N`   — day N of month
  - `"monthly:last"` → `dom:-1` — last day of month
  - `"semimonthly:N,M"` → `dom:N,M`
  - `"weekly:monday"` … `"weekly:sunday"` → `dow:0` … `dow:6`
  - `"every:N"` → `interval:N` — every N days
```

- [ ] **Step 3: Update timing_fit description**

Replace hardcoded `5 days` references with `TIMING_VARIANCE_THRESHOLD_DAYS (5)`.

- [ ] **Step 4: Update stage1 algorithm description**

Update the stage1 description to reflect the new per-entry ordered phase evaluation: payee → timing → amount. Note that `interval:N` anchors use group chain detection on the payee-scoped candidate set.

- [ ] **Step 5: Update conditions-editor.md**

Add before the `date_day_of_month` section:

```markdown
> **Deprecated:** `date_day_of_month` — use `cadence` instead. Existing entries evaluate correctly; no new entries should use this type.

### cadence — Recurring schedule (transaction-target, Pass 1)

Matches transactions whose date falls within ±5 days of a recurring schedule anchor.

**Schema B** (editor): `{"cadence": "monthly:15"}`

**Schema A** (storage/engine): `{"type":"recurrence_anchor","recurrence_anchor":"dom:15","tolerance_days":5}`

| Schema B | Schema A | Meaning |
|---|---|---|
| `"monthly:N"` | `dom:N` | Day N of the month |
| `"monthly:last"` | `dom:-1` | Last day of the month |
| `"semimonthly:N,M"` | `dom:N,M` | Either day N or M |
| `"weekly:monday"` | `dow:0` | Every Monday |
| `"every:N"` | `interval:N` | Every N days (group chain detection) |

`tolerance_days` is always 5 and not exposed in Schema B.
```

- [ ] **Step 6: Commit**

```bash
git add docs/veloci-ref.md docs/conditions-editor.md
git commit -m "docs: add recurrence_anchor and cadence; update stage1 algorithm; deprecate date_day_of_month"
```

---

## Task 8: Glossary update

**Files:**
- Modify: `services/web/page/glossary.templ`
- Regenerate: `services/web/page/glossary_templ.go`

- [ ] **Step 1: Read glossary.templ to find structure**

Open `services/web/page/glossary.templ` and note the `<dt>/<dd>` structure and where to insert alphabetically for "C" and "T".

- [ ] **Step 2: Add Cadence entry (under "C")**

```templ
<dt>Cadence</dt>
<dd>
    The human-readable form of a recurring schedule in the conditions editor.
    Maps to a <code>recurrence_anchor</code> Schema A condition at the store boundary.
    Forms: <code>monthly:15</code>, <code>monthly:last</code>,
    <code>semimonthly:1,15</code>, <code>weekly:monday</code>, <code>every:91</code>.
    The 5-day timing tolerance is always implied — not configurable per-condition.
    See also: <strong>Timing Tolerance</strong>.
</dd>
```

- [ ] **Step 3: Add Timing Tolerance entry (under "T")**

```templ
<dt>Timing Tolerance</dt>
<dd>
    A 5-day window (<code>TIMING_VARIANCE_THRESHOLD_DAYS</code>) applied uniformly
    across all schedule-aware operations:
    <ul>
        <li><strong>Condition matching:</strong> <code>recurrence_anchor</code> conditions match transactions within ±5 days of the expected anchor date.</li>
        <li><strong>Interval chain detection:</strong> consecutive transactions are considered part of the same <code>interval:N</code> chain when their gap is within N ± 5 days.</li>
        <li><strong>Fitness scoring:</strong> <code>timing_fit = 1.0</code> when matched transaction interval std dev ≤ 5 days.</li>
        <li><strong>Ended detection:</strong> an entry lapses when <code>computed_as_of &gt; next_due_date + 5 days</code>.</li>
    </ul>
</dd>
```

- [ ] **Step 4: Regenerate and build**

```bash
cd services/web && templ generate && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add services/web/page/glossary.templ services/web/page/glossary_templ.go
git commit -m "docs: add Cadence and Timing Tolerance glossary entries"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|---|---|
| `TIMING_VARIANCE_THRESHOLD_DAYS` shared constant | Task 1 |
| Stage2 imports shared constant | Task 1 |
| `RecurrenceAnchor` variant in `CompiledConditionTree` | Task 2 |
| `dom:N`, `dom:-1`, `dom:N,M` per-transaction evaluation | Task 2 |
| `dow:N` per-transaction evaluation | Task 2 |
| `interval:N` per-transaction returns None (group phase handles it) | Task 2 |
| `date_day_of_month` evaluator kept, no new generation | Task 2 + 4 |
| Stage1 per-entry ordered phases: payee → timing → amount | Task 3 |
| `apply_interval_timing` chain detection | Task 3 |
| Pass 2+ (entry-target) preserved | Task 3 |
| Stage2 generates payee + recurrence_anchor + amount_range for standing | Task 4 |
| Amount clamp = observed min/max, no buffer | Task 4 |
| Variable entries get payee only | Task 4 |
| All anchor types generated (dom/dow/interval) | Task 4 |
| `entry_has_lapsed` helper in stage7 | Task 5 |
| `cadenceToAnchor` / `anchorToCadence` helpers | Task 6 |
| Schema B `cadence` → Schema A `recurrence_anchor` via `storageNode` | Task 6 |
| Schema A `recurrence_anchor` → Schema B `cadence` via `displayNode` | Task 6 |
| `entry_recurrence_anchor` Schema A still → `entry_recurrence_anchor` Schema B | Task 6 |
| `veloci-ref.md` updated | Task 7 |
| `conditions-editor.md` updated | Task 7 |
| Glossary entries: Cadence, Timing Tolerance | Task 8 |

### Not in this plan (deferred)

- **Stage2 `AMOUNT_VARIANCE_THRESHOLD_PCT` constant** — currently used to gate "standing" classification but amount variance doesn't affect the type per the new spec (timing_fit gate is the only standing classifier). This constant can be removed in a follow-up cleanup.
- **Conditions editor UI for `cadence`** — autocomplete widget for the `cadence` condition type in `conditions-editor.js` is a follow-up.
- **Phase 1 `detect_ended_entries`** — added in the entry proposals Phase 1 plan; calls `entry_has_lapsed(next_due, computed_as_of, super::TIMING_VARIANCE_THRESHOLD_DAYS as i64)`.
- **`next_due_date` removal from stage2** — currently still computed and stored by `persist_cluster`. It remains for UI display convenience. Its use in any pipeline condition evaluation is already eliminated by this plan.
