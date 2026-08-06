//! Drift proposal detection.
//!
//! Runs after the flux-window day-crawl. For each live standing entry whose
//! actual rate (from the latest snapshot) has diverged more than DRIFT_THRESHOLD
//! from its stored projected rate, an `entry_proposals` row of type 'drift' is
//! inserted so the user can review.

use anyhow::{Context, Result};
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

/// Minimum relative delta to trigger a drift proposal (20%).
const DRIFT_THRESHOLD: f64 = 0.20;

/// Minimum matched transaction count required before proposing drift.
const MIN_DRIFT_MATCHES: i32 = 3;

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Detect drift for all live standing entries of an entity.
///
/// Compares the latest `snapshots.actual_rate_per_day` at `computed_as_of`
/// against `entries.projected_rate_per_day`. Inserts an `entry_proposals` row
/// when the delta ratio exceeds DRIFT_THRESHOLD and the entry has enough
/// transaction evidence, unless a proposal is already pending or recently dismissed.
pub async fn detect_drift_proposals(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    pool: &PgPool,
) -> Result<()> {
    let candidates = load_drift_candidates(entity_id, computed_as_of, pool).await?;

    for c in candidates {
        if !should_propose_drift(c.projected_rate, c.actual_rate, c.matched_count) {
            continue;
        }

        let already_blocked: Option<(i32,)> = sqlx::query_as(
            "SELECT 1 FROM entry_proposals
             WHERE reference_entry_id = $1
               AND proposal_type = 'drift'
               AND (status = 'pending'
                    OR (status = 'dismissed'
                        AND dismissed_transaction_count >= $2))",
        )
        .bind(c.entry_id)
        .bind(c.matched_count)
        .fetch_optional(pool)
        .await
        .context("failed to check existing drift proposal")?;

        if already_blocked.is_some() {
            continue;
        }

        let delta_ratio = (c.actual_rate - c.projected_rate).abs() / c.projected_rate.abs();

        sqlx::query(
            "INSERT INTO entry_proposals
               (entity_id, proposal_type, status, reference_entry_id,
                proposed_rate_per_day, drift_delta_ratio)
             VALUES ($1, 'drift', 'pending', $2, $3, $4)",
        )
        .bind(entity_id)
        .bind(c.entry_id)
        .bind(c.actual_rate)
        .bind(delta_ratio)
        .execute(pool)
        .await
        .context("failed to insert drift proposal")?;
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// Pure predicate (unit-testable)
// ---------------------------------------------------------------------------

/// Returns true when a drift proposal should be created.
pub(crate) fn should_propose_drift(projected: f64, actual: f64, matched_count: i32) -> bool {
    if projected == 0.0 {
        return false;
    }
    if matched_count < MIN_DRIFT_MATCHES {
        return false;
    }
    let delta_ratio = (actual - projected).abs() / projected.abs();
    delta_ratio > DRIFT_THRESHOLD
}

// ---------------------------------------------------------------------------
// DB loader
// ---------------------------------------------------------------------------

#[derive(Debug)]
struct DriftCandidate {
    entry_id:       Uuid,
    projected_rate: f64,
    actual_rate:    f64,
    matched_count:  i32,
}

async fn load_drift_candidates(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    pool: &PgPool,
) -> Result<Vec<DriftCandidate>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        entry_id:                 Uuid,
        projected_rate_per_day:   sqlx::types::BigDecimal,
        actual_rate_per_day:      sqlx::types::BigDecimal,
        matched_transaction_count: i32,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT
            e.id                         AS entry_id,
            e.projected_rate_per_day,
            s.actual_rate_per_day,
            COALESCE(e.matched_transaction_count, 0) AS matched_transaction_count
        FROM entries e
        JOIN LATERAL (
            SELECT actual_rate_per_day
            FROM snapshots
            WHERE entity_id = $1
              AND node_id = e.id
              AND node_type = 'entry'
              AND snapshot_date <= $2
            ORDER BY snapshot_date DESC
            LIMIT 1
        ) s ON true
        WHERE e.entity_id = $1
          AND (e.end_date IS NULL OR e.end_date >= $2)
          AND e.entry_type = 'standing'
          AND e.source != 'system'
          AND e.projected_rate_per_day IS NOT NULL
        "#,
    )
    .bind(entity_id)
    .bind(computed_as_of)
    .fetch_all(pool)
    .await
    .context("failed to load drift candidates")?;

    Ok(rows
        .into_iter()
        .filter_map(|r| {
            use std::str::FromStr;
            let projected = f64::from_str(&r.projected_rate_per_day.to_string()).ok()?;
            let actual    = f64::from_str(&r.actual_rate_per_day.to_string()).ok()?;
            Some(DriftCandidate {
                entry_id:      r.entry_id,
                projected_rate: projected,
                actual_rate:    actual,
                matched_count:  r.matched_transaction_count,
            })
        })
        .collect())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drift_above_threshold_triggers() {
        // 25% delta exceeds 20% threshold with enough matches.
        assert!(should_propose_drift(100.0, 125.0, 3));
    }

    #[test]
    fn drift_below_threshold_skips() {
        // 10% delta is under threshold.
        assert!(!should_propose_drift(100.0, 110.0, 3));
    }

    #[test]
    fn drift_exact_threshold_skips() {
        // 20% delta is not strictly greater than threshold.
        assert!(!should_propose_drift(100.0, 120.0, 3));
    }

    #[test]
    fn drift_insufficient_matches_skips() {
        // 50% delta but only 2 matches — not enough evidence.
        assert!(!should_propose_drift(100.0, 150.0, 2));
    }

    #[test]
    fn drift_zero_projected_skips() {
        // Divide-by-zero guard: if projected is zero, no drift.
        assert!(!should_propose_drift(0.0, 50.0, 5));
    }
}
