//! Stage 6: Atomic snapshot write.
//!
//! **Input:** Per-node computed values from Stages 3–5.
//!
//! **Output:** Rows in `snapshots`.
//!
//! All snapshots for a given job run commit in a single Postgres transaction.
//! Partial writes are not possible — either all snapshots commit or none do.
//!
//! The UPSERT pattern (`ON CONFLICT DO UPDATE`) makes Stage 6 idempotent:
//! re-running the same job overwrites the same rows with identical values.

use anyhow::{Context, Result};
use chrono::NaiveDate;
use rayon::prelude::*;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::{NodeType, Stage3Output, Stage4Output, Stage5Output};

// ---------------------------------------------------------------------------
// Snapshot row struct (built in parallel, inserted sequentially)
// ---------------------------------------------------------------------------

#[derive(Debug)]
struct SnapshotInsert {
    entity_id:                  Uuid,
    node_id:                    Uuid,
    node_type:                  &'static str,
    snapshot_date:              NaiveDate,
    computed_as_of:             NaiveDate,
    job_id:                     Uuid,
    actual_rate_per_day:        f64,
    projected_rate_per_day:     f64,
    drift_per_day:              f64,
    slope_per_day:              f64,
    r_squared:                  f64,
    transaction_count:          i32,
    window_days_used:           i32,
    rolling_window_total_cents: i64,
    balance_cents:              i64,
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 6: build snapshot rows in parallel, UPSERT in a single transaction.
///
/// Uses the write pool — all DB access here uses `pool` (write pool).
pub async fn run(
    entity_id: Uuid,
    job_id: Uuid,
    snapshot_date: NaiveDate,
    computed_as_of: NaiveDate,
    stage3: &Stage3Output,
    stage4: &Stage4Output,
    stage5: &Stage5Output,
    pool: &PgPool,
) -> Result<()> {
    // Build lookup maps for Stage 5 trends.
    let entry_trends: std::collections::HashMap<Uuid, &crate::pipeline::types::NodeTrend> =
        stage5.entry_trends.iter().map(|t| (t.node_id, t)).collect();
    let label_trends: std::collections::HashMap<Uuid, &crate::pipeline::types::NodeTrend> =
        stage5.label_trends.iter().map(|t| (t.node_id, t)).collect();

    // Build all snapshot rows in parallel (CPU work).
    let mut rows: Vec<SnapshotInsert> = Vec::new();

    // Entry snapshots.
    let entry_rows: Vec<SnapshotInsert> = stage3
        .entry_rates
        .par_iter()
        .map(|rate| {
            let trend = entry_trends.get(&rate.entry_id);
            SnapshotInsert {
                entity_id,
                node_id:                    rate.entry_id,
                node_type:                  NodeType::Entry.as_str(),
                snapshot_date,
                computed_as_of,
                job_id,
                actual_rate_per_day:        rate.actual_rate_per_day,
                projected_rate_per_day:     rate.projected_rate_per_day,
                drift_per_day:              trend.map(|t| t.drift_per_day).unwrap_or(0.0),
                slope_per_day:              trend.map(|t| t.slope_per_day).unwrap_or(0.0),
                r_squared:                  trend.map(|t| t.r_squared).unwrap_or(0.0),
                transaction_count:          rate.transaction_count,
                window_days_used:           rate.window_days_used,
                rolling_window_total_cents: rate.rolling_window_total_cents,
                balance_cents:              0,
            }
        })
        .collect();

    // Label snapshots (label aggregates).
    let label_rows: Vec<SnapshotInsert> = stage4
        .label_rates
        .par_iter()
        .map(|rate| {
            let trend = label_trends.get(&rate.label_id);
            SnapshotInsert {
                entity_id,
                node_id:                    rate.label_id,
                node_type:                  NodeType::Label.as_str(),
                snapshot_date,
                computed_as_of,
                job_id,
                actual_rate_per_day:        rate.actual_rate_per_day,
                projected_rate_per_day:     rate.projected_rate_per_day,
                drift_per_day:              trend.map(|t| t.drift_per_day).unwrap_or(0.0),
                slope_per_day:              trend.map(|t| t.slope_per_day).unwrap_or(0.0),
                r_squared:                  trend.map(|t| t.r_squared).unwrap_or(0.0),
                transaction_count:          rate.contributing_entry_count,
                window_days_used:           rate.period_days,
                rolling_window_total_cents: 0,
                balance_cents:              0,
            }
        })
        .collect();

    rows.extend(entry_rows);
    rows.extend(label_rows);

    if rows.is_empty() {
        return Ok(());
    }

    // Single atomic UPSERT — spec invariant: all snapshots for a job commit together.
    upsert_snapshots(rows, pool).await
}

// ---------------------------------------------------------------------------
// Batch UPSERT (sequential DB write)
// ---------------------------------------------------------------------------

async fn upsert_snapshots(rows: Vec<SnapshotInsert>, pool: &PgPool) -> Result<()> {
    // Decompose into column vectors for unnest-based batch insert.
    let n = rows.len();
    let mut entity_ids:     Vec<Uuid>      = Vec::with_capacity(n);
    let mut node_ids:       Vec<Uuid>      = Vec::with_capacity(n);
    let mut node_types:     Vec<String>    = Vec::with_capacity(n);
    let mut snapshot_dates: Vec<NaiveDate> = Vec::with_capacity(n);
    let mut computed_as_ofs: Vec<NaiveDate> = Vec::with_capacity(n);
    let mut job_ids:        Vec<Uuid>      = Vec::with_capacity(n);
    let mut actuals:        Vec<f64>       = Vec::with_capacity(n);
    let mut projecteds:     Vec<f64>       = Vec::with_capacity(n);
    let mut drifts:         Vec<f64>       = Vec::with_capacity(n);
    let mut slopes:         Vec<f64>       = Vec::with_capacity(n);
    let mut r_squareds:     Vec<f64>       = Vec::with_capacity(n);
    let mut tx_counts:      Vec<i32>       = Vec::with_capacity(n);
    let mut window_days:    Vec<i32>       = Vec::with_capacity(n);
    let mut rolling_totals: Vec<i64>       = Vec::with_capacity(n);
    let mut balances:       Vec<i64>       = Vec::with_capacity(n);

    for row in rows {
        entity_ids.push(row.entity_id);
        node_ids.push(row.node_id);
        node_types.push(row.node_type.to_string());
        snapshot_dates.push(row.snapshot_date);
        computed_as_ofs.push(row.computed_as_of);
        job_ids.push(row.job_id);
        actuals.push(row.actual_rate_per_day);
        projecteds.push(row.projected_rate_per_day);
        drifts.push(row.drift_per_day);
        slopes.push(row.slope_per_day);
        r_squareds.push(row.r_squared);
        tx_counts.push(row.transaction_count);
        window_days.push(row.window_days_used);
        rolling_totals.push(row.rolling_window_total_cents);
        balances.push(row.balance_cents);
    }

    // Single transaction for atomicity (spec invariant §13 point 4).
    let mut tx = pool.begin().await.context("failed to begin snapshot transaction")?;

    sqlx::query(
        r#"
        INSERT INTO snapshots (
          entity_id, node_id, node_type, snapshot_date, computed_as_of, job_id,
          actual_rate_per_day, projected_rate_per_day, drift_per_day,
          slope_per_day, r_squared,
          transaction_count, window_days_used, rolling_window_total_cents,
          balance_cents
        )
        SELECT e, n, nt, sd, ca, j, a, p, d, sl, r2, tc, wd, rt, b
        FROM UNNEST(
          $1::uuid[], $2::uuid[], $3::text[], $4::date[], $5::date[], $6::uuid[],
          $7::float8[], $8::float8[], $9::float8[],
          $10::float8[], $11::float8[],
          $12::int4[], $13::int4[], $14::int8[],
          $15::int8[]
        ) AS u(e, n, nt, sd, ca, j, a, p, d, sl, r2, tc, wd, rt, b)
        ON CONFLICT (entity_id, node_id, snapshot_date) DO UPDATE SET
          computed_as_of                = EXCLUDED.computed_as_of,
          job_id                        = EXCLUDED.job_id,
          actual_rate_per_day           = EXCLUDED.actual_rate_per_day,
          projected_rate_per_day        = EXCLUDED.projected_rate_per_day,
          drift_per_day                 = EXCLUDED.drift_per_day,
          slope_per_day                 = EXCLUDED.slope_per_day,
          r_squared                     = EXCLUDED.r_squared,
          transaction_count             = EXCLUDED.transaction_count,
          window_days_used              = EXCLUDED.window_days_used,
          rolling_window_total_cents    = EXCLUDED.rolling_window_total_cents,
          balance_cents                 = EXCLUDED.balance_cents
        "#,
    )
    .bind(&entity_ids)
    .bind(&node_ids)
    .bind(&node_types)
    .bind(&snapshot_dates)
    .bind(&computed_as_ofs)
    .bind(&job_ids)
    .bind(&actuals)
    .bind(&projecteds)
    .bind(&drifts)
    .bind(&slopes)
    .bind(&r_squareds)
    .bind(&tx_counts)
    .bind(&window_days)
    .bind(&rolling_totals)
    .bind(&balances)
    .execute(&mut *tx)
    .await
    .context("failed to upsert snapshots")?;

    tx.commit().await.context("failed to commit snapshot transaction")?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pipeline::types::{Direction, EntryRate, EntryType, LabelRate, NodeType};
    use chrono::NaiveDate;
    use uuid::Uuid;

    fn date(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    // Spec invariant: snapshot writes are atomic (verified by integration tests).
    // This test verifies node_type strings match the DB constraint.
    #[test]
    fn entry_snapshot_uses_entry_node_type() {
        assert_eq!(NodeType::Entry.as_str(), "entry");
        assert_eq!(NodeType::Label.as_str(), "label");
    }

    #[test]
    fn label_snapshot_built_from_label_rate() {
        let label_rate = LabelRate {
            label_id:                 Uuid::nil(),
            direction:                Direction::Expense,
            period_days:              30,
            actual_rate_per_day:      100.0,
            projected_rate_per_day:   100.0,
            contributing_entry_count: 3,
        };
        let snapshot = SnapshotInsert {
            entity_id:                  Uuid::nil(),
            node_id:                    label_rate.label_id,
            node_type:                  NodeType::Label.as_str(),
            snapshot_date:              date("2026-03-01"),
            computed_as_of:             date("2026-03-01"),
            job_id:                     Uuid::nil(),
            actual_rate_per_day:        label_rate.actual_rate_per_day,
            projected_rate_per_day:     label_rate.projected_rate_per_day,
            drift_per_day:              0.0,
            slope_per_day:              0.0,
            r_squared:                  0.0,
            transaction_count:          label_rate.contributing_entry_count,
            window_days_used:           label_rate.period_days,
            rolling_window_total_cents: 0,
            balance_cents:              0,
        };
        assert_eq!(snapshot.node_type, "label");
        assert_eq!(snapshot.transaction_count, 3);
    }
}
