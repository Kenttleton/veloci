//! Stage 4: Label rate mapping.
//!
//! **Input:** Per-rule rates from Stage 3; `rules.label_id` FK.
//!
//! **Output:** Per-label `LabelRate` structs.
//!
//! ## Model
//!
//! Each rule has exactly one output label (`rules.label_id`). Stage 4 groups
//! Stage 3 rule rates by their `label_id` to produce label-level rates.
//!
//! Since each rule maps to exactly one label, and a transaction can match
//! multiple rules at different hierarchy levels (pre → leaf label, post →
//! aggregate label), there is no set-union deduplication needed. Each rule's
//! rate is mapped to its label independently.
//!
//! `contributing_rule_count` is the count of transactions assigned to the
//! rule in Stage 3.

use anyhow::Result;
use rayon::prelude::*;
use uuid::Uuid;

use crate::pipeline::types::{Direction, LabelRate, RuleRate, Stage4Output};

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 4: map rule rates to label rates.
///
/// This is a pure in-memory transformation — no DB access required.
/// Labels with no active rule (label_id not referenced by any active rule)
/// produce no output.
pub async fn run(
    entity_id: Uuid,
    rule_rates: &[RuleRate],
    _pool: &sqlx::PgPool,
) -> Result<Stage4Output> {
    let _ = entity_id; // entity context implicit in rule_rates (all scoped to entity)

    // Group rule rates by label_id. Rules without a label_id are skipped.
    let mut label_map: std::collections::HashMap<Uuid, Vec<&RuleRate>> =
        std::collections::HashMap::new();

    for rate in rule_rates {
        if let Some(label_id) = rate.label_id {
            label_map.entry(label_id).or_default().push(rate);
        }
    }

    // Parallel computation over labels.
    let label_rates: Vec<LabelRate> = label_map
        .into_par_iter()
        .map(|(label_id, rates)| compute_label_rate(label_id, &rates))
        .collect();

    Ok(Stage4Output { label_rates })
}

// ---------------------------------------------------------------------------
// Label rate computation (pure)
// ---------------------------------------------------------------------------

/// Compute the aggregate rate for a label from its contributing rule rates.
///
/// Each label is defined by exactly one rule (`rules.label_id = $label_id`).
/// The spec states: "for each active label, read the rate of the rule whose
/// `label_id` matches." This means there should be exactly one rule per label.
///
/// If multiple rules share the same label_id (which should not happen per
/// spec invariants, but handled defensively), their rates are summed.
pub fn compute_label_rate(label_id: Uuid, rates: &[&RuleRate]) -> LabelRate {
    let actual_rate_per_day: f64 = rates.iter().map(|r| r.actual_rate_per_day).sum();
    let projected_rate_per_day: f64 = rates.iter().map(|r| r.projected_rate_per_day).sum();
    let contributing_rule_count: i32 = rates.iter().map(|r| r.transaction_count).sum();
    let period_days: i32 = rates
        .iter()
        .map(|r| r.period_days)
        .max()
        .unwrap_or(30);

    // Direction: income if ANY rule is income (short-circuit, spec §9).
    let direction = if rates.iter().any(|r| r.direction == Direction::Income) {
        Direction::Income
    } else {
        Direction::Expense
    };

    LabelRate {
        label_id,
        direction,
        period_days,
        actual_rate_per_day,
        projected_rate_per_day,
        contributing_rule_count,
    }
}

// ---------------------------------------------------------------------------
// Cycle detection (defense-in-depth)
// ---------------------------------------------------------------------------

/// Check for cycles in the label hierarchy via rule conditions.
///
/// A cycle occurs when label A is an input condition to a rule that outputs
/// label B, and label B feeds back to label A. The Go API prevents cycles at
/// write time; this is a defense-in-depth check on the engine side.
///
/// Returns `Err` if any cycle is detected.
///
/// This function operates on a map of `label_id → set of input label UUIDs
/// referenced in its defining rule's conditions`. The caller builds this map
/// from the loaded rules + their JSONB conditions.
pub fn detect_label_cycles(
    label_inputs: &std::collections::HashMap<Uuid, Vec<Uuid>>,
) -> Result<(), Vec<Uuid>> {
    // DFS cycle detection.
    let mut visited: std::collections::HashSet<Uuid> = std::collections::HashSet::new();
    let mut in_stack: std::collections::HashSet<Uuid> = std::collections::HashSet::new();

    fn dfs(
        node: Uuid,
        graph: &std::collections::HashMap<Uuid, Vec<Uuid>>,
        visited: &mut std::collections::HashSet<Uuid>,
        in_stack: &mut std::collections::HashSet<Uuid>,
    ) -> bool {
        if in_stack.contains(&node) {
            return true; // cycle found
        }
        if visited.contains(&node) {
            return false; // already fully explored
        }
        visited.insert(node);
        in_stack.insert(node);

        if let Some(inputs) = graph.get(&node) {
            for &input in inputs {
                if dfs(input, graph, visited, in_stack) {
                    return true;
                }
            }
        }
        in_stack.remove(&node);
        false
    }

    let mut cycle_nodes = Vec::new();
    for &node in label_inputs.keys() {
        if dfs(node, label_inputs, &mut visited, &mut in_stack) {
            cycle_nodes.push(node);
        }
    }

    if cycle_nodes.is_empty() {
        Ok(())
    } else {
        Err(cycle_nodes)
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pipeline::types::{Direction, EntryType, RuleRate};
    use uuid::Uuid;

    fn rule_rate(
        rule_id: &str,
        label_id: Option<&str>,
        direction: Direction,
        actual: f64,
        projected: f64,
        period_days: i32,
        tx_count: i32,
    ) -> RuleRate {
        RuleRate {
            rule_id:                    Uuid::parse_str(rule_id).unwrap_or(Uuid::nil()),
            label_id:                   label_id.and_then(|s| Uuid::parse_str(s).ok()),
            direction,
            entry_type:                 EntryType::Standing,
            period_days,
            epoch_id:                   None,
            actual_rate_per_day:        actual,
            projected_rate_per_day:     projected,
            transaction_count:          tx_count,
            window_days_used:           period_days,
            rolling_window_total_cents: 0,
        }
    }

    const LABEL_A: &str = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
    const LABEL_B: &str = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";

    #[test]
    fn single_rule_per_label() {
        let rates = vec![
            rule_rate(
                "00000000-0000-0000-0000-000000000001",
                Some(LABEL_A),
                Direction::Expense,
                100.0,
                100.0,
                30,
                3,
            ),
        ];
        let label_id = Uuid::parse_str(LABEL_A).unwrap();
        let rate_refs: Vec<&RuleRate> = rates.iter().collect();
        let label = compute_label_rate(label_id, &rate_refs);
        assert!((label.actual_rate_per_day - 100.0).abs() < 0.01);
        assert_eq!(label.contributing_rule_count, 3);
    }

    #[test]
    fn rules_without_label_id_are_skipped() {
        // Rule with no label_id should not appear in label output.
        let rates = vec![rule_rate(
            "00000000-0000-0000-0000-000000000001",
            None, // no label
            Direction::Expense,
            50.0,
            50.0,
            30,
            1,
        )];
        // There are no label_id-bearing rates — label_map should be empty.
        let mut label_map: std::collections::HashMap<Uuid, Vec<&RuleRate>> =
            std::collections::HashMap::new();
        for r in &rates {
            if let Some(lid) = r.label_id {
                label_map.entry(lid).or_default().push(r);
            }
        }
        assert!(label_map.is_empty(), "rule with no label_id should produce no label rate");
    }

    #[test]
    fn direction_income_short_circuits() {
        // Mix of income + expense rules → income wins.
        let rates = vec![
            rule_rate(
                "00000000-0000-0000-0000-000000000001",
                Some(LABEL_A),
                Direction::Expense,
                100.0,
                100.0,
                30,
                2,
            ),
            rule_rate(
                "00000000-0000-0000-0000-000000000002",
                Some(LABEL_A),
                Direction::Income,
                200.0,
                200.0,
                30,
                3,
            ),
        ];
        let label_id = Uuid::parse_str(LABEL_A).unwrap();
        let rate_refs: Vec<&RuleRate> = rates.iter().collect();
        let label = compute_label_rate(label_id, &rate_refs);
        assert_eq!(label.direction, Direction::Income);
        // Rates are summed across both rules.
        assert!((label.actual_rate_per_day - 300.0).abs() < 0.01);
    }

    #[test]
    fn all_expense_direction_is_expense() {
        let rates = vec![rule_rate(
            "00000000-0000-0000-0000-000000000001",
            Some(LABEL_A),
            Direction::Expense,
            50.0,
            50.0,
            30,
            1,
        )];
        let label_id = Uuid::parse_str(LABEL_A).unwrap();
        let rate_refs: Vec<&RuleRate> = rates.iter().collect();
        let label = compute_label_rate(label_id, &rate_refs);
        assert_eq!(label.direction, Direction::Expense);
    }

    // Cycle detection tests
    #[test]
    fn no_cycle_returns_ok() {
        let mut graph = std::collections::HashMap::new();
        let a = Uuid::parse_str(LABEL_A).unwrap();
        let b = Uuid::parse_str(LABEL_B).unwrap();
        graph.insert(a, vec![b]);
        graph.insert(b, vec![]);
        assert!(detect_label_cycles(&graph).is_ok());
    }

    #[test]
    fn direct_cycle_detected() {
        let mut graph = std::collections::HashMap::new();
        let a = Uuid::parse_str(LABEL_A).unwrap();
        let b = Uuid::parse_str(LABEL_B).unwrap();
        // A → B → A is a cycle.
        graph.insert(a, vec![b]);
        graph.insert(b, vec![a]);
        assert!(detect_label_cycles(&graph).is_err(), "should detect cycle");
    }

    #[test]
    fn self_cycle_detected() {
        let mut graph = std::collections::HashMap::new();
        let a = Uuid::parse_str(LABEL_A).unwrap();
        graph.insert(a, vec![a]); // A references itself
        assert!(detect_label_cycles(&graph).is_err());
    }
}
