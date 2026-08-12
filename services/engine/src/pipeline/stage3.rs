//! Stage 3: Rate computation per dirty entry (day-crawl).
//!
//! **Input:** `dirty_entry_ids` from the dirty-detection pass; queries
//! `transaction_entry_assignments` joined to those specific entries only.
//!
//! **Output:** Per-entry `EntryRate` structs containing actual_rate, projected_rate,
//! window_days_used, and rolling_window_total_cents.
//!
//! ## Algorithm
//!
//! 1. Load dirty entries by ID (no status/end_date filter — supports ended entries).
//! 2. Load transaction assignments scoped to dirty entry IDs and `snapshot_date`.
//! 3. `rayon::par_iter` over entries — each rate is computed independently.
//!
//! This stage is read-only with respect to entry metadata. `next_due_date` is
//! maintained by Stage 1 (live entries) and Stage 2 (new detections).

use anyhow::{Context, Result};
use chrono::NaiveDate;
use rayon::prelude::*;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::{Direction, EntryRate, EntryType, Stage3Output};

const DEFAULT_SYSTEM_WINDOW_DAYS: i32 = 90;

// ---------------------------------------------------------------------------
// Internal DB row types
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct ActiveEntry {
    id:                     Uuid,
    label_id:               Option<Uuid>,
    direction:              String,
    entry_type:             String,
    source:                 String,
    period_days:            Option<i32>,
    rate_method:            String,
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
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Stage3Output> {
    let system_window_days = load_system_window_days(entity_id, pool).await?;
    let entries     = load_entries_by_ids(entity_id, dirty_entry_ids, pool).await?;
    let txns        = load_assigned_txns(entity_id, snapshot_date, dirty_entry_ids, pool).await?;
    let prior_rates = load_prior_snapshot_rates(entity_id, snapshot_date, dirty_entry_ids, pool).await?;

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
            compute_entry_rate(entry, entry_txns, snapshot_date, prior_projected, system_window_days)
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
    system_window_days: i32,
) -> EntryRate {
    let entry_type = EntryType::from_str(&entry.entry_type).unwrap_or(EntryType::Standing);
    let direction  = Direction::from_str(&entry.direction).unwrap_or(Direction::Spend);

    // W: system entries use entity_config window; named entries use period_days.
    let period_days = if entry.source == "system" {
        system_window_days
    } else {
        entry.period_days.unwrap_or(30)
    };

    // Transactions where date <= snapshot_date (DB already filtered by start_date).
    let active_txns: Vec<&AssignedTxn> = txns
        .iter()
        .copied()
        .filter(|t| t.txn_date <= snapshot_date)
        .collect();

    // Rolling window: transactions in [snapshot_date - W, snapshot_date].
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

    let actual_rate_per_day = compute_actual_rate(rolling_window_total_cents, window_days_used);

    // User-set projected rate takes precedence. When no user override and transactions
    // exist, use rate_method (median or max of matched amounts / period_days) as the
    // forward-looking projection. With no transactions fall back to prior snapshot or actual.
    let projected_rate_per_day = if let Some(user_rate) = entry.projected_rate_per_day {
        user_rate
    } else if active_txns.is_empty() {
        prior_projected_rate.unwrap_or(actual_rate_per_day)
    } else {
        match entry.rate_method.as_str() {
            "max" => max_rate(&active_txns, period_days),
            _     => median_rate(&active_txns, period_days), // "median" is the default
        }
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

/// Unified rolling window rate: Σ amount_i for t_i in [t−W, t] / W.
/// Rates are signed: income entries produce positive rates, spend entries produce
/// negative rates, mixed entries produce the signed net margin.
fn compute_actual_rate(rolling_window_total_cents: i64, window_days_used: i32) -> f64 {
    if window_days_used == 0 {
        return 0.0;
    }
    rolling_window_total_cents as f64 / f64::from(window_days_used)
}

/// Projected rate using the median of matched transaction absolute amounts ÷ period_days.
/// Signed by the entry's direction separately; this returns the absolute magnitude.
fn median_rate(txns: &[&AssignedTxn], period_days: i32) -> f64 {
    if txns.is_empty() || period_days <= 0 {
        return 0.0;
    }
    let mut amounts: Vec<i64> = txns.iter().map(|t| t.amount_cents.abs()).collect();
    amounts.sort_unstable();
    let n = amounts.len();
    let median = if n % 2 == 0 {
        (amounts[n / 2 - 1] + amounts[n / 2]) / 2
    } else {
        amounts[n / 2]
    };
    median as f64 / f64::from(period_days)
}

/// Projected rate using the maximum matched transaction absolute amount ÷ period_days.
/// Conservative upper-bound projection for budget planning.
fn max_rate(txns: &[&AssignedTxn], period_days: i32) -> f64 {
    if txns.is_empty() || period_days <= 0 {
        return 0.0;
    }
    let max = txns.iter().map(|t| t.amount_cents.abs()).max().unwrap_or(0);
    max as f64 / f64::from(period_days)
}

// ---------------------------------------------------------------------------
// DB loaders
// ---------------------------------------------------------------------------

async fn load_entries_by_ids(
    entity_id:       Uuid,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<ActiveEntry>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

    #[derive(sqlx::FromRow)]
    struct Row {
        id:                     Uuid,
        label_id:               Option<Uuid>,
        direction:              String,
        entry_type:             String,
        source:                 String,
        period_days:            Option<i32>,
        rate_method:            String,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        start_date:             NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, label_id, direction, entry_type, source, period_days,
               rate_method, projected_rate_per_day, start_date
        FROM entries
        WHERE entity_id = $1
          AND id = ANY($2)
        "#,
    )
    .bind(entity_id)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load dirty entries for stage 3")?;

    Ok(rows.into_iter().map(|r| ActiveEntry {
        id:                     r.id,
        label_id:               r.label_id,
        direction:              r.direction,
        entry_type:             r.entry_type,
        source:                 r.source,
        period_days:            r.period_days,
        rate_method:            r.rate_method,
        projected_rate_per_day: r.projected_rate_per_day
            .and_then(|v| v.to_string().parse::<f64>().ok()),
        start_date:             r.start_date,
    }).collect())
}

async fn load_system_window_days(entity_id: Uuid, pool: &PgPool) -> Result<i32> {
    let row: Option<(i32,)> = sqlx::query_as(
        "SELECT system_window_days FROM entity_config WHERE entity_id = $1",
    )
    .bind(entity_id)
    .fetch_optional(pool)
    .await
    .context("failed to load entity_config for stage 3")?;

    Ok(row.map(|(days,)| days).unwrap_or(DEFAULT_SYSTEM_WINDOW_DAYS))
}

async fn load_assigned_txns(
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<AssignedTxn>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

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
          AND tea.entry_id = ANY($3)
          AND t.date <= $2
          AND t.date >= e.start_date
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load assigned transactions for stage 3")?;

    Ok(rows.into_iter().map(|r| AssignedTxn {
        entry_id:     r.entry_id,
        txn_date:     r.txn_date,
        amount_cents: r.amount_cents,
    }).collect())
}

async fn load_prior_snapshot_rates(
    entity_id:       Uuid,
    snapshot_date:   NaiveDate,
    dirty_entry_ids: &[Uuid],
    pool:            &PgPool,
) -> Result<Vec<(Uuid, f64)>> {
    if dirty_entry_ids.is_empty() {
        return Ok(Vec::new());
    }

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
          AND node_id = ANY($3)
          AND snapshot_date < $2
        ORDER BY node_id, snapshot_date DESC
        "#,
    )
    .bind(entity_id)
    .bind(snapshot_date)
    .bind(dirty_entry_ids)
    .fetch_all(pool)
    .await
    .context("failed to load prior snapshot rates for stage 3")?;

    Ok(rows.into_iter().map(|r| {
        let rate = r.actual_rate_per_day.to_string().parse::<f64>().unwrap_or(0.0);
        (r.node_id, rate)
    }).collect())
}

// ---------------------------------------------------------------------------
// Tests (pure rate computation — no DB)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // rolling window: Σ amount_i / W (signed — spend entries produce negative rates)
    #[test]
    fn rolling_window_single_txn() {
        let rate = compute_actual_rate(-3000, 30);
        assert!((rate - (-100.0)).abs() < 0.01, "expected -100.0, got {rate}");
    }

    #[test]
    fn rolling_window_multi_txn() {
        let rate = compute_actual_rate(-8000, 30);
        assert!((rate - (-8000.0 / 30.0)).abs() < 0.01, "got {rate}");
    }

    #[test]
    fn rolling_window_zero_days() {
        let rate = compute_actual_rate(-15000, 0);
        assert!((rate - 0.0).abs() < 0.01, "expected 0.0 for zero window");
    }

    #[test]
    fn rate_computation_is_deterministic() {
        let r1 = compute_actual_rate(-3000, 30);
        let r2 = compute_actual_rate(-3000, 30);
        assert!((r1 - r2).abs() < 1e-10, "rate must be deterministic");
    }

    fn make_txn(amount_cents: i64) -> AssignedTxn {
        AssignedTxn {
            entry_id:     Uuid::nil(),
            txn_date:     chrono::NaiveDate::from_ymd_opt(2026, 1, 1).unwrap(),
            amount_cents,
        }
    }

    #[test]
    fn median_rate_odd() {
        let txns = vec![make_txn(-1000), make_txn(-3000), make_txn(-2000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // median of [1000, 2000, 3000] = 2000; rate = 2000 / 30
        assert!((median_rate(&refs, 30) - 2000.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn median_rate_even() {
        let txns = vec![make_txn(-1000), make_txn(-2000), make_txn(-3000), make_txn(-4000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // median of [1000, 2000, 3000, 4000] = (2000+3000)/2 = 2500; rate = 2500 / 30
        assert!((median_rate(&refs, 30) - 2500.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn max_rate_picks_largest() {
        let txns = vec![make_txn(-1000), make_txn(-5000), make_txn(-2000)];
        let refs: Vec<&AssignedTxn> = txns.iter().collect();
        // max = 5000; rate = 5000 / 30
        assert!((max_rate(&refs, 30) - 5000.0 / 30.0).abs() < 0.01);
    }

    #[test]
    fn median_rate_empty_returns_zero() {
        assert_eq!(median_rate(&[], 30), 0.0);
    }

    #[test]
    fn empty_dirty_entry_ids_produces_empty_rates() {
        let entry = ActiveEntry {
            id:                     Uuid::nil(),
            label_id:               None,
            direction:              "spend".into(),
            entry_type:             "standing".into(),
            source:                 "user".into(),
            period_days:            Some(30),
            rate_method:            "median".into(),
            projected_rate_per_day: None,
            start_date:             NaiveDate::from_ymd_opt(2025, 1, 1).unwrap(),
        };
        let rate = compute_entry_rate(&entry, &[], NaiveDate::from_ymd_opt(2025, 6, 1).unwrap(), None, 90);
        assert_eq!(rate.transaction_count, 0);
        assert_eq!(rate.actual_rate_per_day, 0.0);
    }
}
