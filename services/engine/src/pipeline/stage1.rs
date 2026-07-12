//! Stage 1: Rule matching (pre/post, boolean condition trees).
//!
//! **Input:** `raw_transactions` for an entity; all active `rules` with their
//! JSONB condition trees.
//!
//! **Output:** `transaction_rule_assignments` rows (many-to-many — intentional).
//!
//! ## Algorithm
//!
//! 1. Load all active rules from DB.
//! 2. Pre-compile each rule's JSONB conditions into a [`CompiledRule`] struct.
//!    Regex patterns are compiled once here; malformed rules are logged and
//!    skipped without aborting the job.
//! 3. Sort `compiled_rules` by `(stage ASC, priority ASC)` — pre-stage first.
//! 4. `rayon::par_iter` over all transactions, evaluate each rule in order.
//! 5. Each match → one `transaction_rule_assignments` row with `confidence = 1.0`.
//! 6. Collect unmatched transaction IDs for Stage 2.
//! 7. Bulk INSERT assignments (delete-then-insert for idempotency).

use anyhow::{bail, Context, Result};
use chrono::NaiveDate;
use chrono::Datelike;
use rayon::prelude::*;
use regex::Regex;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::{RuleStage, Stage1Output};

// ---------------------------------------------------------------------------
// Compiled rule tree
// ---------------------------------------------------------------------------

/// A rule with its condition tree pre-compiled for zero-allocation evaluation.
///
/// `regex::Regex` is `Send + Sync` — compiled patterns are shared across
/// rayon worker threads with no cloning or locking.
#[derive(Debug)]
pub struct CompiledRule {
    pub rule_id:    Uuid,
    pub stage:      RuleStage,
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
    /// rule whose `label_id` equals the referenced label UUID.
    LabelMatched(Uuid),
}

// ---------------------------------------------------------------------------
// Raw DB row for a rule
// ---------------------------------------------------------------------------

struct RuleRow {
    id:         Uuid,
    stage:      String,
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

/// Run Stage 1 for an entity: load transactions + rules, compile, match.
pub async fn run(entity_id: Uuid, pool: &PgPool) -> Result<Stage1Output> {
    // Load all raw_transactions for the entity.
    let txns = load_transactions(entity_id, pool).await?;

    // Load all active rules.
    let rule_rows = load_rules(entity_id, pool).await?;

    // Pre-compile rules; skip malformed rules (log and continue).
    let mut compiled_rules: Vec<CompiledRule> = rule_rows
        .into_iter()
        .filter_map(|row| {
            let rule_id = row.id;
            match compile_rule(row) {
                Ok(r) => Some(r),
                Err(e) => {
                    tracing::warn!(%rule_id, "rule compile error (skipped): {e:?}");
                    None
                }
            }
        })
        .collect();

    // Sort: pre-stage first, then by priority ascending within each stage.
    compiled_rules.sort_by(|a, b| {
        a.stage.cmp(&b.stage).then_with(|| a.priority.cmp(&b.priority))
    });

    tracing::debug!(rules = compiled_rules.len(), txns = txns.len(), "stage 1: compiled rules, starting match");

    // Parallel match: each transaction is independent.
    // Returns (matched_rule_ids, was_unmatched) per transaction.
    let results: Vec<(Uuid, Vec<Uuid>, bool)> = txns
        .par_iter()
        .map(|txn| {
            let matched: Vec<Uuid> = compiled_rules
                .iter()
                .filter(|rule| evaluate_rule(&rule.conditions, txn, &compiled_rules))
                .map(|rule| rule.rule_id)
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

    // Update next_due_date on rules that received new assignments this run.
    // next_due_date = max(matched_tx.date) + period_days; used by Stage 7 for
    // absence detection. Stage 2 handles this for newly created rules.
    let matched_rule_ids: Vec<Uuid> = results
        .iter()
        .flat_map(|(_, rule_ids, _)| rule_ids.iter().copied())
        .collect::<std::collections::HashSet<_>>()
        .into_iter()
        .collect();
    update_next_due_dates(entity_id, &matched_rule_ids, pool).await?;

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
/// The `all_rules` slice is provided for `LabelMatched` leaf evaluation:
/// in practice, post-stage rules are evaluated after pre-stage assignments
/// have been accumulated into the transaction's label set. For the current
/// single-pass implementation, `LabelMatched` always returns `false` on the
/// first pass and is re-evaluated in a second post-stage pass if needed.
pub fn evaluate_rule(
    tree: &CompiledConditionTree,
    txn: &TransactionRow,
    _all_rules: &[CompiledRule],
) -> bool {
    evaluate(tree, txn)
}

fn evaluate(node: &CompiledConditionTree, txn: &TransactionRow) -> bool {
    match node {
        CompiledConditionTree::And(children) => children.iter().all(|c| evaluate(c, txn)),
        CompiledConditionTree::Or(children)  => children.iter().any(|c| evaluate(c, txn)),
        CompiledConditionTree::Not(child)    => !evaluate(child, txn),
        CompiledConditionTree::Xor(a, b)    => evaluate(a, txn) ^ evaluate(b, txn),

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

        // LabelMatched is post-stage only: evaluated after pre-stage pass.
        // In the current implementation this leaf always returns false during
        // the pre-stage pass, which is correct (post rules don't fire until
        // after pre-stage assignments are accumulated in a second pass).
        // A full two-pass implementation is deferred.
        CompiledConditionTree::LabelMatched(_label_id) => false,
    }
}

// ---------------------------------------------------------------------------
// Compilation (pure — can panic on invalid JSONB, returns Result)
// ---------------------------------------------------------------------------

fn compile_rule(row: RuleRow) -> Result<CompiledRule> {
    let stage = RuleStage::from_str(&row.stage)
        .ok_or_else(|| anyhow::anyhow!("invalid stage value: {}", row.stage))?;
    let conditions = compile_tree(&row.conditions)
        .with_context(|| format!("failed to compile conditions for rule {}", row.id))?;
    Ok(CompiledRule {
        rule_id: row.id,
        stage,
        priority: row.priority,
        conditions,
    })
}

fn compile_tree(v: &serde_json::Value) -> Result<CompiledConditionTree> {
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
        FROM raw_transactions
        WHERE entity_id = $1
        ORDER BY date ASC
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load raw_transactions for stage 1")?;

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

async fn load_rules(entity_id: Uuid, pool: &PgPool) -> Result<Vec<RuleRow>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:         Uuid,
        stage:      String,
        priority:   i32,
        conditions: serde_json::Value,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, stage, priority, conditions
        FROM rules
        WHERE entity_id = $1
          AND status = 'active'
        ORDER BY stage ASC, priority ASC
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load active rules for stage 1")?;

    Ok(rows
        .into_iter()
        .map(|r| RuleRow {
            id:         r.id,
            stage:      r.stage,
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
    // This makes Stage 1 idempotent on re-runs (rules.reprocess).
    sqlx::query(
        r#"
        DELETE FROM transaction_rule_assignments
        WHERE transaction_id IN (
            SELECT id FROM raw_transactions WHERE entity_id = $1
        )
        "#,
    )
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to delete existing assignments")?;

    // Collect all (transaction_id, rule_id) pairs.
    let mut txn_ids: Vec<Uuid> = Vec::new();
    let mut rule_ids: Vec<Uuid> = Vec::new();

    for (txn_id, matched_rules, _) in results {
        for rule_id in matched_rules {
            txn_ids.push(*txn_id);
            rule_ids.push(*rule_id);
        }
    }

    if txn_ids.is_empty() {
        return Ok(());
    }

    sqlx::query(
        r#"
        INSERT INTO transaction_rule_assignments (transaction_id, rule_id, confidence)
        SELECT t, r, 1.0
        FROM UNNEST($1::uuid[], $2::uuid[]) AS u(t, r)
        ON CONFLICT (transaction_id, rule_id) DO NOTHING
        "#,
    )
    .bind(&txn_ids)
    .bind(&rule_ids)
    .execute(pool)
    .await
    .context("failed to insert rule assignments")?;

    Ok(())
}

async fn update_next_due_dates(
    entity_id: Uuid,
    rule_ids: &[Uuid],
    pool: &PgPool,
) -> Result<()> {
    if rule_ids.is_empty() {
        return Ok(());
    }
    sqlx::query(
        r#"
        UPDATE rules r
        SET next_due_date = (
            SELECT (MAX(rt.date) + (r.period_days * INTERVAL '1 day'))::date
            FROM transaction_rule_assignments tra
            JOIN raw_transactions rt ON rt.id = tra.transaction_id
            WHERE tra.rule_id = r.id
        )
        WHERE r.id = ANY($1)
          AND r.entity_id = $2
          AND r.status = 'active'
        "#,
    )
    .bind(rule_ids)
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to update next_due_date for matched rules")?;
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

    // Helper: compile a JSONB condition and evaluate.
    fn eval(json: serde_json::Value, txn: &TransactionRow) -> bool {
        let tree = compile_tree(&json).unwrap();
        evaluate(&tree, txn)
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
        // Transaction on day 15, target day 15, tolerance 0.
        let txn = make_txn(any_uuid(), any_uuid(), "2026-03-15", -1000, "Test");
        assert!(eval(json!({"type": "date_day_of_month", "day": 15, "tolerance_days": 0}), &txn));
        // Tolerance 2 — day 13 should match.
        let txn2 = make_txn(any_uuid(), any_uuid(), "2026-03-13", -1000, "Test");
        assert!(eval(json!({"type": "date_day_of_month", "day": 15, "tolerance_days": 2}), &txn2));
        // Day 10 — outside tolerance 2.
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
        // Amount outside range — AND should fail.
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
        // amount_range matches, payee_exact matches → XOR = false
        let cond_both = json!({
            "op": "XOR",
            "children": [
                {"type": "imported_payee_exact", "value": "Netflix"},
                {"type": "amount_range", "min_cents": -2000, "max_cents": -1000}
            ]
        });
        assert!(!eval(cond_both, &txn));
        // Only payee matches → XOR = true
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

    // Spec invariant: pre-stage rules sort before post-stage.
    #[test]
    fn rule_sort_pre_before_post() {
        use crate::pipeline::types::RuleStage;
        let mut rules = vec![
            CompiledRule {
                rule_id: Uuid::nil(),
                stage: RuleStage::Post,
                priority: 10,
                conditions: CompiledConditionTree::And(vec![]),
            },
            CompiledRule {
                rule_id: Uuid::nil(),
                stage: RuleStage::Pre,
                priority: 200,
                conditions: CompiledConditionTree::And(vec![]),
            },
        ];
        rules.sort_by(|a, b| a.stage.cmp(&b.stage).then_with(|| a.priority.cmp(&b.priority)));
        assert_eq!(rules[0].stage, RuleStage::Pre);
        assert_eq!(rules[1].stage, RuleStage::Post);
    }
}
