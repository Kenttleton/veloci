//! Stage 5: Slope + drift computation using linear regression.
//!
//! **Input:** Current per-node rates from Stages 3 and 4; prior
//! `snapshots` bulk-loaded for all nodes.
//!
//! **Output:** Per-node `NodeTrend` structs with `drift_per_day`, `slope_per_day`,
//! and `r_squared`.
//!
//! ## Algorithm
//!
//! 1. Determine `max_window_days` = max(3 × period_days) across all nodes.
//! 2. Bulk-load all snapshot history for all nodes in ONE query (before par_iter).
//! 3. Group into `HashMap<Uuid, Vec<SnapshotRow>>`.
//! 4. `rayon::par_iter` over all nodes — no DB access inside the loop.
//!    - Entry nodes: filter by date window (start_date absorbed into entries table).
//!    - Label nodes: filter by date window only.
//! 5. Linear regression (OLS) over `(days_since_first, actual_rate)` pairs.
//! 6. Compute drift using direction-aware sign convention.

use anyhow::{Context, Result};
use chrono::NaiveDate;
use rayon::prelude::*;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::{
    Direction, EntryRate, LabelRate, NodeTrend, NodeType, SnapshotRow, Stage3Output, Stage4Output,
    Stage5Output,
};

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 5: compute slope, drift, and r² for all active nodes.
pub async fn run(
    entity_id: Uuid,
    snapshot_date: NaiveDate,
    computed_as_of: NaiveDate,
    stage3: &Stage3Output,
    stage4: &Stage4Output,
    pool: &PgPool,
) -> Result<Stage5Output> {
    let _ = computed_as_of; // used by stage3/4 callers

    // Collect all node IDs + their regression windows.
    let entry_nodes: Vec<(Uuid, i32)> = stage3
        .entry_rates
        .iter()
        .map(|r| (r.entry_id, r.period_days * 3))
        .collect();

    let label_nodes: Vec<(Uuid, i32)> = stage4
        .label_rates
        .iter()
        .map(|l| (l.label_id, l.period_days * 3))
        .collect();

    // Compute max window for the bulk SQL query.
    let max_window_days = entry_nodes
        .iter()
        .map(|(_, w)| *w)
        .chain(label_nodes.iter().map(|(_, w)| *w))
        .max()
        .unwrap_or(90);

    // Collect all node IDs for the bulk query.
    let all_node_ids: Vec<Uuid> = entry_nodes
        .iter()
        .map(|(id, _)| *id)
        .chain(label_nodes.iter().map(|(id, _)| *id))
        .collect();

    if all_node_ids.is_empty() {
        return Ok(Stage5Output {
            entry_trends: Vec::new(),
            label_trends: Vec::new(),
        });
    }

    // Bulk-load all snapshot history before par_iter (spec §9).
    let history_map = bulk_load_history(entity_id, &all_node_ids, snapshot_date, max_window_days, pool).await?;

    // Build a lookup from entry_id → current Direction (from Stage 3).
    let entry_direction: std::collections::HashMap<Uuid, Direction> = stage3
        .entry_rates
        .iter()
        .map(|r| (r.entry_id, r.direction))
        .collect();

    // Build lookup for label → direction (from Stage 4).
    let label_direction: std::collections::HashMap<Uuid, Direction> = stage4
        .label_rates
        .iter()
        .map(|l| (l.label_id, l.direction))
        .collect();

    // Build lookup for current rates.
    let entry_rate_map: std::collections::HashMap<Uuid, &EntryRate> = stage3
        .entry_rates
        .iter()
        .map(|r| (r.entry_id, r))
        .collect();

    let label_rate_map: std::collections::HashMap<Uuid, &LabelRate> = stage4
        .label_rates
        .iter()
        .map(|l| (l.label_id, l))
        .collect();

    // Parallel computation for entry nodes.
    let entry_trends: Vec<NodeTrend> = entry_nodes
        .par_iter()
        .map(|(node_id, window_days)| {
            let node_history = history_map.get(node_id).map(Vec::as_slice).unwrap_or(&[]);

            // Filter: date window only (epochs absorbed into entries.start_date).
            let history: Vec<&SnapshotRow> = node_history
                .iter()
                .filter(|r| r.snapshot_date >= snapshot_date - chrono::Duration::days(i64::from(*window_days)))
                .collect();

            let current_rate = entry_rate_map
                .get(node_id)
                .map(|r| r.actual_rate_per_day)
                .unwrap_or(0.0);
            let current_projected = entry_rate_map
                .get(node_id)
                .map(|r| r.projected_rate_per_day)
                .unwrap_or(0.0);
            let direction = entry_direction.get(node_id).copied().unwrap_or(Direction::Expense);

            let (slope, r_squared) = linear_regression_from_history(&history, snapshot_date, current_rate);
            let drift = compute_drift(current_rate, current_projected, direction);

            NodeTrend {
                node_id:       *node_id,
                node_type:     NodeType::Entry,
                drift_per_day: drift,
                slope_per_day: slope,
                r_squared,
            }
        })
        .collect();

    // Parallel computation for label nodes (label aggregates).
    let label_trends: Vec<NodeTrend> = label_nodes
        .par_iter()
        .map(|(node_id, window_days)| {
            let node_history = history_map.get(node_id).map(Vec::as_slice).unwrap_or(&[]);

            let history: Vec<&SnapshotRow> = node_history
                .iter()
                .filter(|r| r.snapshot_date >= snapshot_date - chrono::Duration::days(i64::from(*window_days)))
                .collect();

            let current_rate = label_rate_map
                .get(node_id)
                .map(|l| l.actual_rate_per_day)
                .unwrap_or(0.0);
            let current_projected = label_rate_map
                .get(node_id)
                .map(|l| l.projected_rate_per_day)
                .unwrap_or(0.0);
            let direction = label_direction.get(node_id).copied().unwrap_or(Direction::Expense);

            let (slope, r_squared) = linear_regression_from_history(&history, snapshot_date, current_rate);
            let drift = compute_drift(current_rate, current_projected, direction);

            NodeTrend {
                node_id:       *node_id,
                node_type:     NodeType::Label,
                drift_per_day: drift,
                slope_per_day: slope,
                r_squared,
            }
        })
        .collect();

    Ok(Stage5Output { entry_trends, label_trends })
}

// ---------------------------------------------------------------------------
// Drift computation (pure)
// ---------------------------------------------------------------------------

/// Compute drift with direction-aware sign convention.
///
/// Positive drift always means financially ahead of projection:
/// - Expense: `projected - actual` (spent less = positive = ahead)
/// - Income:  `actual - projected` (earned more = positive = ahead)
///
/// # Examples
///
/// ```
/// use veloci_engine::pipeline::stage5::compute_drift;
/// use veloci_engine::pipeline::types::Direction;
///
/// // Expense: spent less than projected = positive drift
/// let drift = compute_drift(80.0, 100.0, Direction::Expense);
/// assert!((drift - 20.0).abs() < 0.01);
///
/// // Income: earned more than projected = positive drift
/// let drift = compute_drift(120.0, 100.0, Direction::Income);
/// assert!((drift - 20.0).abs() < 0.01);
/// ```
pub fn compute_drift(actual: f64, projected: f64, direction: Direction) -> f64 {
    match direction {
        Direction::Expense => projected - actual,
        Direction::Income  => actual - projected,
        // Mixed entries span both directions; use the Expense convention
        // (under-spending relative to projection = positive drift).
        Direction::Mixed   => projected - actual,
    }
}

// ---------------------------------------------------------------------------
// Linear regression (pure OLS — no external crate)
// ---------------------------------------------------------------------------

/// Run OLS linear regression over historical snapshot rates + the current point.
///
/// Returns `(slope_per_day, r_squared)`.
/// - `slope_per_day`: rate of change of actual_rate (units: $/day per day).
/// - `r_squared`: goodness of fit (0.0–1.0).
///
/// Minimum 2 data points required. With 0 or 1 data points in history,
/// returns `(0.0, 0.0)`.
///
/// # Examples
///
/// ```
/// use veloci_engine::pipeline::stage5::ols_regression;
/// let points = vec![(0.0_f64, 100.0_f64), (1.0, 110.0), (2.0, 120.0)];
/// let (slope, r2) = ols_regression(&points);
/// assert!((slope - 10.0).abs() < 0.01);
/// assert!((r2 - 1.0).abs() < 0.01); // perfect linear fit
/// ```
pub fn ols_regression(points: &[(f64, f64)]) -> (f64, f64) {
    let n = points.len();
    if n < 2 {
        return (0.0, 0.0);
    }

    let n_f = n as f64;
    let sum_x: f64  = points.iter().map(|(x, _)| x).sum();
    let sum_y: f64  = points.iter().map(|(_, y)| y).sum();
    let sum_xx: f64 = points.iter().map(|(x, _)| x * x).sum();
    let sum_xy: f64 = points.iter().map(|(x, y)| x * y).sum();

    let denom = n_f * sum_xx - sum_x * sum_x;
    if denom.abs() < 1e-12 {
        // All x values are identical — no meaningful slope.
        return (0.0, 0.0);
    }

    let slope     = (n_f * sum_xy - sum_x * sum_y) / denom;
    let intercept = (sum_y - slope * sum_x) / n_f;

    // R² = 1 - SS_res / SS_tot
    let y_mean = sum_y / n_f;
    let ss_tot: f64 = points.iter().map(|(_, y)| (y - y_mean).powi(2)).sum();
    if ss_tot < 1e-12 {
        // All y values are identical — perfect fit (constant function).
        return (slope, 1.0);
    }
    let ss_res: f64 = points
        .iter()
        .map(|(x, y)| (y - (slope * x + intercept)).powi(2))
        .sum();
    let r_squared = (1.0 - ss_res / ss_tot).clamp(0.0, 1.0);

    (slope, r_squared)
}

/// Build regression points from historical snapshot rows + current rate.
///
/// The x-axis is `(snapshot_date - first_snapshot_date).num_days()` (integer
/// days elapsed since the earliest data point in the regression window).
/// The y-axis is `actual_rate_per_day`.
fn linear_regression_from_history(
    history: &[&SnapshotRow],
    snapshot_date: NaiveDate,
    current_rate: f64,
) -> (f64, f64) {
    // Include the current snapshot_date + current_rate as the final point.
    // Sort history by date ascending.
    let mut sorted: Vec<&SnapshotRow> = history.to_vec();
    sorted.sort_by_key(|r| r.snapshot_date);

    let first_date = sorted
        .first()
        .map(|r| r.snapshot_date)
        .unwrap_or(snapshot_date);

    let mut points: Vec<(f64, f64)> = sorted
        .iter()
        .map(|r| {
            let days = (r.snapshot_date - first_date).num_days() as f64;
            (days, r.actual_rate_per_day)
        })
        .collect();

    // Add current point.
    let current_x = (snapshot_date - first_date).num_days() as f64;
    points.push((current_x, current_rate));

    ols_regression(&points)
}

// ---------------------------------------------------------------------------
// DB: bulk load snapshot history
// ---------------------------------------------------------------------------

async fn bulk_load_history(
    entity_id: Uuid,
    node_ids: &[Uuid],
    snapshot_date: NaiveDate,
    max_window_days: i32,
    pool: &PgPool,
) -> Result<std::collections::HashMap<Uuid, Vec<SnapshotRow>>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:             Uuid,
        node_type:           String,
        snapshot_date:       NaiveDate,
        computed_as_of:      NaiveDate,
        actual_rate_per_day: sqlx::types::BigDecimal,
    }

    let window_start = snapshot_date - chrono::Duration::days(i64::from(max_window_days));

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT node_id, node_type, snapshot_date, computed_as_of,
               actual_rate_per_day
        FROM snapshots
        WHERE entity_id = $1
          AND node_id = ANY($2)
          AND snapshot_date >= $3
        ORDER BY node_id, snapshot_date ASC
        "#,
    )
    .bind(entity_id)
    .bind(node_ids)
    .bind(window_start)
    .fetch_all(pool)
    .await
    .context("failed to bulk-load snapshot history for stage 5")?;

    let mut map: std::collections::HashMap<Uuid, Vec<SnapshotRow>> =
        std::collections::HashMap::new();

    for r in rows {
        let node_type = NodeType::from_str(&r.node_type).unwrap_or(NodeType::Entry);
        let rate = r.actual_rate_per_day.to_string().parse::<f64>().unwrap_or(0.0);
        map.entry(r.node_id).or_default().push(SnapshotRow {
            node_id:             r.node_id,
            node_type,
            snapshot_date:       r.snapshot_date,
            computed_as_of:      r.computed_as_of,
            actual_rate_per_day: rate,
        });
    }

    Ok(map)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pipeline::types::Direction;

    // OLS regression tests
    #[test]
    fn ols_perfect_linear_fit() {
        let points: Vec<(f64, f64)> = (0..5).map(|i| (i as f64, 10.0 * i as f64)).collect();
        let (slope, r2) = ols_regression(&points);
        assert!((slope - 10.0).abs() < 0.001, "slope should be 10.0, got {slope}");
        assert!((r2 - 1.0).abs() < 0.001, "r² should be 1.0, got {r2}");
    }

    #[test]
    fn ols_constant_returns_zero_slope_one_r2() {
        let points: Vec<(f64, f64)> = (0..5).map(|i| (i as f64, 50.0)).collect();
        let (slope, r2) = ols_regression(&points);
        assert!(slope.abs() < 0.001, "slope of constant should be ~0, got {slope}");
        // Constant function = perfect fit
        assert!((r2 - 1.0).abs() < 0.001, "r² of constant should be 1.0, got {r2}");
    }

    #[test]
    fn ols_single_point_returns_zeros() {
        let (slope, r2) = ols_regression(&[(1.0, 100.0)]);
        assert_eq!(slope, 0.0);
        assert_eq!(r2, 0.0);
    }

    #[test]
    fn ols_empty_returns_zeros() {
        let (slope, r2) = ols_regression(&[]);
        assert_eq!(slope, 0.0);
        assert_eq!(r2, 0.0);
    }

    #[test]
    fn ols_noisy_fit_r2_below_one() {
        // Points that don't form a perfect line.
        let points = vec![
            (0.0, 10.0),
            (1.0, 30.0),
            (2.0, 15.0),
            (3.0, 35.0),
        ];
        let (_, r2) = ols_regression(&points);
        assert!(r2 < 1.0, "noisy data should have r² < 1.0, got {r2}");
        assert!(r2 >= 0.0, "r² should be non-negative, got {r2}");
    }

    #[test]
    fn ols_r2_clamped_to_zero_to_one() {
        // Adversarial case: artificially bad fit shouldn't produce negative r².
        let points = vec![
            (0.0, 100.0),
            (1.0, 1.0),
            (2.0, 100.0),
            (3.0, 1.0),
        ];
        let (_, r2) = ols_regression(&points);
        assert!(r2 >= 0.0 && r2 <= 1.0, "r² out of range: {r2}");
    }

    // Drift tests
    #[test]
    fn drift_expense_spent_less_is_positive() {
        // Actual < projected for expense → ahead → positive drift.
        let drift = compute_drift(80.0, 100.0, Direction::Expense);
        assert!((drift - 20.0).abs() < 0.01, "expense ahead drift should be +20, got {drift}");
    }

    #[test]
    fn drift_expense_spent_more_is_negative() {
        // Actual > projected for expense → behind → negative drift.
        let drift = compute_drift(120.0, 100.0, Direction::Expense);
        assert!((drift + 20.0).abs() < 0.01, "expense behind drift should be -20, got {drift}");
    }

    #[test]
    fn drift_income_earned_more_is_positive() {
        // Actual > projected for income → ahead → positive drift.
        let drift = compute_drift(120.0, 100.0, Direction::Income);
        assert!((drift - 20.0).abs() < 0.01, "income ahead drift should be +20, got {drift}");
    }

    #[test]
    fn drift_income_earned_less_is_negative() {
        // Actual < projected for income → behind → negative drift.
        let drift = compute_drift(80.0, 100.0, Direction::Income);
        assert!((drift + 20.0).abs() < 0.01, "income behind drift should be -20, got {drift}");
    }

    #[test]
    fn drift_on_target_is_zero() {
        assert!((compute_drift(100.0, 100.0, Direction::Expense)).abs() < 0.001);
        assert!((compute_drift(100.0, 100.0, Direction::Income)).abs() < 0.001);
    }

    // Spec §9: minimum 2 data points for slope
    #[test]
    fn regression_needs_two_points() {
        let (slope, r2) = ols_regression(&[(0.0, 100.0)]);
        assert_eq!(slope, 0.0);
        assert_eq!(r2, 0.0);
    }

    // Two data points — should produce exact slope.
    #[test]
    fn regression_two_points_exact() {
        let (slope, r2) = ols_regression(&[(0.0, 100.0), (10.0, 200.0)]);
        assert!((slope - 10.0).abs() < 0.001, "slope should be 10.0, got {slope}");
        assert!((r2 - 1.0).abs() < 0.001, "r² should be 1.0 for two points, got {r2}");
    }
}
