//! Stage 3: Rate computation per active entry (day-crawl).
//!
//! **Input:** `transaction_entry_assignments` joined to `entries WHERE status = 'active'`
//! and `end_date IS NULL`.
//!
//! **Output:** Per-entry `EntryRate` structs containing actual_rate, projected_rate,
//! window_days_used, and rolling_window_total_cents.
//!
//! ## Algorithm
//!
//! 1. Load all active entries (status='active', end_date IS NULL, no epoch join).
//! 2. Load all relevant transaction assignments scoped by `entries.start_date`
//!    and `snapshot_date` for the flux window day-crawl.
//! 3. `rayon::par_iter` over active entries — each entry's rate is computed
//!    independently from its own transaction data.
//!
//! This stage is read-only with respect to entry metadata. `next_due_date` is
//! maintained by Stage 1 (active entries) and Stage 2 (new detections).

use anyhow::{Context, Result};
use chrono::NaiveDate;
use rayon::prelude::*;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::{Direction, EntryRate, EntryType, Stage3Output};

// ---------------------------------------------------------------------------
// Internal DB row types
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct ActiveEntry {
    id:                     Uuid,
    label_id:               Option<Uuid>,
    direction:              String,
    entry_type:             String,
    period_days:            i32,
    variable_method:        Option<String>,
    projected_rate_per_day: Option<f64>,
    start_date:             NaiveDate,
}

#[derive(Debug, Clone)]
pub(crate) struct AssignedTxn {
    entry_id:     Uuid,
    txn_date:     NaiveDate,
    amount_cents: i64,
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 3: compute per-entry rates as of `snapshot_date`.
///
/// Only transactions where `date <= snapshot_date` and `date >= entry.start_date`
/// are included — this is the flux window day-crawl anchor.
pub async fn run(
    entity_id: Uuid,
    snapshot_date: NaiveDate,
    pool: &PgPool,
) -> Result<Stage3Output> {
    let entries = load_active_entries(entity_id, pool).await?;
    let txns = load_assigned_txns(entity_id, snapshot_date, pool).await?;
    let prior_rates = load_prior_snapshot_rates(entity_id, snapshot_date, pool).await?;

    // Index transactions by entry_id for O(1) lookup during par_iter.
    let txns_by_entry: std::collections::HashMap<Uuid, Vec<&AssignedTxn>> = {
        let mut map: std::collections::HashMap<Uuid, Vec<&AssignedTxn>> =
            std::collections::HashMap::new();
        for t in &txns {
            map.entry(t.entry_id).or_default().push(t);
        }
        map
    };

    let prior_by_entry: std::collections::HashMap<Uuid, f64> =
        prior_rates.into_iter().collect();

    // Parallel rate computation — each entry is fully independent.
    let entry_rates: Vec<EntryRate> = entries
        .par_iter()
        .map(|entry| {
            let entry_txns: &[&AssignedTxn] = txns_by_entry
                .get(&entry.id)
                .map(Vec::as_slice)
                .unwrap_or(&[]);
            let prior_projected = prior_by_entry.get(&entry.id).copied();
            compute_entry_rate(entry, entry_txns, snapshot_date, prior_projected)
        })
        .collect();

    Ok(Stage3Output { entry_rates })
}

// ---------------------------------------------------------------------------
// Rate computation (pure — no I/O)
// ---------------------------------------------------------------------------

/// Compute the rate for a single entry.
///
/// This is a pure function: all inputs are in-memory. No database access.
pub(crate) fn compute_entry_rate(
    entry: &ActiveEntry,
    txns: &[&AssignedTxn],
    snapshot_date: NaiveDate,
    prior_projected_rate: Option<f64>,
) -> EntryRate {
    let entry_type = EntryType::from_str(&entry.entry_type).unwrap_or(EntryType::Standing);
    let direction  = Direction::from_str(&entry.direction).unwrap_or(Direction::Spend);
    let period_days = entry.period_days;

    // Transactions where date <= snapshot_date (DB already filtered by start_date).
    let active_txns: Vec<&AssignedTxn> = txns
        .iter()
        .copied()
        .filter(|t| t.txn_date <= snapshot_date)
        .collect();

    // Rolling window: transactions in [snapshot_date - period_days, snapshot_date].
    let window_start = snapshot_date - chrono::Duration::days(i64::from(period_days));
    let window_txns: Vec<&AssignedTxn> = active_txns
        .iter()
        .copied()
        .filter(|t| t.txn_date >= window_start)
        .collect();

    let rolling_window_total_cents: i64 = window_txns.iter().map(|t| t.amount_cents).sum();

    // Adaptive window: use actual data span when fewer transactions than expected.
    let window_days_used = if active_txns.is_empty() {
        period_days
    } else {
        let earliest = active_txns.iter().map(|t| t.txn_date).min().unwrap();
        let span = (snapshot_date - earliest).num_days() as i32;
        span.max(1).min(period_days)
    };

    let transaction_count = active_txns.len() as i32;

    let actual_rate_per_day =
        compute_actual_rate(entry_type, rolling_window_total_cents, period_days, window_days_used, &active_txns, snapshot_date);

    // User-set projected rate takes precedence. Otherwise use the prior snapshot
    // as a baseline (smooths one-import spikes). New entries with no prior default
    // to current actual.
    let projected_rate_per_day = if let Some(user_rate) = entry.projected_rate_per_day {
        user_rate
    } else {
        prior_projected_rate.unwrap_or(actual_rate_per_day)
    };

    EntryRate {
        entry_id:                   entry.id,
        label_id:                   entry.label_id,
        direction,
        entry_type,
        period_days,
        actual_rate_per_day,
        projected_rate_per_day,
        transaction_count,
        window_days_used,
        rolling_window_total_cents,
    }
}

// ---------------------------------------------------------------------------
// Rate formula implementations (pure)
// ---------------------------------------------------------------------------

fn compute_actual_rate(
    entry_type: EntryType,
    rolling_window_total_cents: i64,
    period_days: i32,
    window_days_used: i32,
    active_txns: &[&AssignedTxn],
    _snapshot_date: NaiveDate,
) -> f64 {
    if period_days == 0 || window_days_used == 0 {
        return 0.0;
    }

    match entry_type {
        EntryType::Standing => {
            // Use median interval when 2+ transactions available.
            if active_txns.len() >= 2 {
                let mut dates: Vec<NaiveDate> = active_txns.iter().map(|t| t.txn_date).collect();
                dates.sort_unstable();
                let intervals: Vec<i64> = dates
                    .windows(2)
                    .map(|w| (w[1] - w[0]).num_days())
                    .collect();
                let mut sorted_intervals = intervals.clone();
                sorted_intervals.sort_unstable();
                let mid = sorted_intervals.len() / 2;
                let detected_period = sorted_intervals[mid];
                if detected_period > 0 {
                    // Most recent amount / detected period.
                    let last_amount = active_txns
                        .iter()
                        .max_by_key(|t| t.txn_date)
                        .map(|t| t.amount_cents)
                        .unwrap_or(0)
                        .abs();
                    return last_amount as f64 / detected_period as f64;
                }
            }
            // Single transaction: amount / period_days.
            let amount = active_txns
                .iter()
                .max_by_key(|t| t.txn_date)
                .map(|t| t.amount_cents.abs())
                .unwrap_or(0);
            amount as f64 / f64::from(period_days)
        }

        EntryType::Variable => {
            // Amortized total over the actual window.
            rolling_window_total_cents.abs() as f64 / f64::from(window_days_used)
        }

        EntryType::Irregular => {
            // Irregular entry: most recent amount amortized over period_days.
            let amount = active_txns
                .iter()
                .max_by_key(|t| t.txn_date)
                .map(|t| t.amount_cents.abs())
                .unwrap_or(0);
            amount as f64 / f64::from(period_days)
        }
    }
}

// ---------------------------------------------------------------------------
// DB loaders
// ---------------------------------------------------------------------------

async fn load_active_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<ActiveEntry>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                     Uuid,
        label_id:               Option<Uuid>,
        direction:              String,
        entry_type:             String,
        period_days:            i32,
        variable_method:        Option<String>,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        start_date:             NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, direction, entry_type, period_days,
               variable_method, projected_rate_per_day, start_date
        FROM entries
        WHERE entity_id = $1
          AND status = 'active'
          AND end_date IS NULL
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load active entries for stage 3")?;

    Ok(rows
        .into_iter()
        .map(|r| ActiveEntry {
            id:                     r.id,
            label_id:               r.label_id,
            direction:              r.direction,
            entry_type:             r.entry_type,
            period_days:            r.period_days,
            variable_method:        r.variable_method,
            projected_rate_per_day: r.projected_rate_per_day
                .and_then(|v| v.to_string().parse::<f64>().ok()),
            start_date:             r.start_date,
        })
        .collect())
}

async fn load_assigned_txns(
    entity_id: Uuid,
    snapshot_date: NaiveDate,
    pool: &PgPool,
) -> Result<Vec<AssignedTxn>> {
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
          AND e.status = 'active'
          AND t.date <= $2
          AND t.date >= e.start_date
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .fetch_all(pool)
    .await
    .context("failed to load assigned transactions for stage 3")?;

    Ok(rows
        .into_iter()
        .map(|r| AssignedTxn {
            entry_id:     r.entry_id,
            txn_date:     r.txn_date,
            amount_cents: r.amount_cents,
        })
        .collect())
}

/// Load the most recent prior snapshot rate for each entry — used as projection baseline.
async fn load_prior_snapshot_rates(
    entity_id: Uuid,
    snapshot_date: NaiveDate,
    pool: &PgPool,
) -> Result<Vec<(Uuid, f64)>> {
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
          AND snapshot_date < $2
        ORDER BY node_id, snapshot_date DESC
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .fetch_all(pool)
    .await
    .context("failed to load prior snapshot rates for stage 3")?;

    Ok(rows
        .into_iter()
        .map(|r| {
            let rate = r.actual_rate_per_day.to_string().parse::<f64>().unwrap_or(0.0);
            (r.node_id, rate)
        })
        .collect())
}

// ---------------------------------------------------------------------------
// Tests (pure rate computation — no DB)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn date(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    fn txn(date_str: &str, amount_cents: i64) -> AssignedTxn {
        AssignedTxn {
            entry_id:     Uuid::nil(),
            txn_date:     date(date_str),
            amount_cents,
        }
    }

    // standing rate = last amount / detected period
    #[test]
    fn standing_single_txn_rate() {
        let t = txn("2026-02-01", -3000);
        let txns_ref: Vec<&AssignedTxn> = vec![&t];
        let rate = compute_actual_rate(EntryType::Standing, -3000, 30, 30, &txns_ref, date("2026-03-01"));
        assert!((rate - 100.0).abs() < 0.01, "expected 100.0, got {rate}");
    }

    // variable rate = rolling_window_total / window_days_used
    #[test]
    fn variable_rate_rolling_window() {
        let t1 = txn("2026-02-10", -5000);
        let t2 = txn("2026-02-20", -3000);
        let txns_ref: Vec<&AssignedTxn> = vec![&t1, &t2];
        let rate = compute_actual_rate(EntryType::Variable, -8000, 30, 30, &txns_ref, date("2026-03-01"));
        assert!((rate - (8000.0 / 30.0)).abs() < 0.01, "got {rate}");
    }

    // irregular rate = most recent amount / period_days (amortized)
    #[test]
    fn irregular_rate_amortized() {
        let t = txn("2026-01-15", -15000);
        let txns_ref: Vec<&AssignedTxn> = vec![&t];
        let rate = compute_actual_rate(EntryType::Irregular, -15000, 30, 30, &txns_ref, date("2026-02-01"));
        assert!((rate - 500.0).abs() < 0.01, "expected 500.0, got {rate}");
    }

    // Rate computation is deterministic — engine never calls NOW()
    #[test]
    fn rate_computation_is_deterministic() {
        let t = txn("2026-02-01", -3000);
        let txns_ref: Vec<&AssignedTxn> = vec![&t];
        let r1 = compute_actual_rate(EntryType::Standing, -3000, 30, 30, &txns_ref, date("2026-03-01"));
        let r2 = compute_actual_rate(EntryType::Standing, -3000, 30, 30, &txns_ref, date("2026-03-01"));
        assert!((r1 - r2).abs() < 1e-10, "rate must be deterministic");
    }
}
