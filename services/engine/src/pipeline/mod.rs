//! Pipeline entry points — one per job type.
// Allow dead_code on pipeline items — many are wired up for future
// integration tests and will all be exercised once a test DB is available.
#![allow(dead_code)]
//!
//! Each function runs a contiguous suffix of the pipeline:
//!
//! | Job type              | Stages                          |
//! |-----------------------|---------------------------------|
//! | `import.process`      | 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 |
//! | `entries.reprocess`   | 1 → 2 → 3 → 4 → 5 → 6 → 7     |
//! | `account.analyze`     | 3 → 4 → 5 → 6 → 7              |
//! | `balance.project`     | 7                               |
//!
//! Stage responsibilities:
//!   0 — CSV dedup + normalization → transactions
//!   1 — Live entry matching → transaction_entry_assignments; updates next_due_date
//!   2 — Pattern detection on unmatched txns → pending entries (with review metadata); sets next_due_date
//!   3 — Per-entry rate computation (day-crawl) — pure calculation, no entry metadata writes
//!   4 — Label rate aggregation from entry rates
//!   5 — Slope + drift regression over snapshot history
//!   6 — Snapshot UPSERT into `snapshots`
//!   7 — Cash flow projection into `projections`; raises drift/ended alerts on entries for missed expectations

pub mod dirty;
pub mod stage0;
pub mod stage1;
pub mod stage2;
pub mod stage3;
pub mod stage4;
pub mod stage5;
pub mod stage6;
pub mod stage7;
pub mod types;

use anyhow::{Context, Result};
use uuid::Uuid;

use crate::db::Pools;

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

// ---------------------------------------------------------------------------
// Pipeline entry points
// ---------------------------------------------------------------------------

/// Run all 8 stages for an `import.process` job.
///
/// Stage 0 writes to `transactions`. All subsequent stages are read-then-
/// write. The final commit (Stage 6 + 7) is a single Postgres transaction.
pub async fn run_import(
    entity_id: Uuid,
    job_id: Uuid,
    pending_import_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, %pending_import_id, "import.process starting");

    let stage0_out = stage0::run(entity_id, job_id, pending_import_id, pools).await?;
    tracing::info!(%entity_id, imported = stage0_out.imported_count, skipped = stage0_out.skipped_count, computed_as_of = %stage0_out.computed_as_of, "stage 0 complete");

    if stage0_out.imported_count == 0 {
        tracing::info!(%entity_id, "stage 0 imported nothing new — skipping stages 1–7");
        return Ok(());
    }

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, unmatched = stage1_out.unmatched_tx_ids.len(), "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");

    let dirty_input = dirty::DirtyDetectionInput {
        superseded_entry_ids:  stage0_out.superseded_entry_ids,
        new_entry_assignments: stage1_out.new_entry_assignments,
    };

    run_from_stage3(entity_id, job_id, stage0_out.computed_as_of, Some(dirty_input), pools).await
}

/// Run stages 1 → 7 for an `entries.reprocess` job.
///
/// Re-reads all `transactions` for the entity; rebuilds assignments,
/// patterns, rates, trends, snapshots, and projections.
pub async fn run_entries_reprocess(
    entity_id: Uuid,
    job_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, "entries.reprocess starting");

    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");

    // Bypass dirty detection — full re-run from history_start.
    run_from_stage3(entity_id, job_id, computed_as_of, None, pools).await
}

/// Run stages 3 → 7 for an `account.analyze` job.
///
/// Used after a rule is approved from the review queue, or on manual
/// recalculate. Stage 0 and Stage 2 are skipped — no new transactions,
/// no new pattern detection.
pub async fn run_account_analyze(
    entity_id: Uuid,
    job_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, "account.analyze starting");
    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;
    run_from_stage3(entity_id, job_id, computed_as_of, None, pools).await
}

/// Run stage 7 only for a `balance.project` job.
///
/// Triggered when an account's balance is updated manually. Rebuilds the
/// 90-day cash flow projection using the existing `snapshots`.
pub async fn run_balance_project(
    entity_id: Uuid,
    job_id: Uuid,
    pools: &Pools,
) -> Result<()> {
    tracing::info!(%entity_id, %job_id, "balance.project starting");

    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;
    run_stage7(entity_id, job_id, computed_as_of, pools).await
}

// ---------------------------------------------------------------------------
// Internal stage chains
// ---------------------------------------------------------------------------

/// Run stages 3 → 7.
async fn run_from_stage3(
    entity_id:      Uuid,
    job_id:         Uuid,
    computed_as_of: chrono::NaiveDate,
    dirty_input:    Option<dirty::DirtyDetectionInput>,
    pools:          &Pools,
) -> Result<()> {
    use crate::pipeline::types::SettlementConfig;

    let settlement_cfg = SettlementConfig::query(entity_id, &pools.read).await?;
    let flux_start = computed_as_of
        - chrono::Duration::days(i64::from(settlement_cfg.settlement_window_days));

    // Build dirty context — either from import touch sources or full bypass.
    let dirty_ctx = match dirty_input {
        Some(ref input) => {
            dirty::DirtyContext::from_import(
                entity_id,
                computed_as_of,
                flux_start,
                input,
                &pools.read,
            )
            .await?
        }
        None => {
            let entries       = dirty::query_entries(entity_id, &pools.read).await?;
            let history_start = dirty::query_history_start(entity_id, &pools.read).await?;
            dirty::DirtyContext::full_rerun(entries, history_start, computed_as_of)
        }
    };

    tracing::info!(
        %entity_id,
        crawl_start    = %dirty_ctx.crawl_start,
        %computed_as_of,
        bypass         = dirty_ctx.bypass_mode,
        "beginning day-crawl"
    );

    let mut snapshot_date = dirty_ctx.crawl_start;
    while snapshot_date <= computed_as_of {
        let dirty_entry_ids = dirty_ctx.dirty_entry_ids_for_date(snapshot_date, flux_start);

        if dirty_entry_ids.is_empty() {
            snapshot_date += chrono::Duration::days(1);
            continue;
        }

        let stage3_out =
            stage3::run(entity_id, snapshot_date, &dirty_entry_ids, &pools.read).await?;

        let stage4_out =
            stage4::run(entity_id, &stage3_out.entry_rates, &pools.read).await?;

        let stage5_out = stage5::run(
            entity_id,
            snapshot_date,
            computed_as_of,
            &stage3_out,
            &stage4_out,
            &pools.read,
        )
        .await?;

        stage6::run(
            entity_id,
            job_id,
            snapshot_date,
            computed_as_of,
            &stage3_out,
            &stage4_out,
            &stage5_out,
            &pools.write,
        )
        .await?;

        snapshot_date += chrono::Duration::days(1);
    }

    tracing::info!(%entity_id, "day-crawl complete");

    sqlx::query(
        r#"
        UPDATE entries e
        SET projected_rate_per_day = s.projected_rate_per_day
        FROM (
            SELECT DISTINCT ON (node_id) node_id, projected_rate_per_day
            FROM snapshots
            WHERE entity_id = $1 AND node_type = 'entry'
            ORDER BY node_id, snapshot_date DESC
        ) s
        WHERE e.id = s.node_id
          AND e.entity_id = $1
          AND e.source = 'system'
          AND e.status = 'live'
        "#,
    )
    .bind(entity_id)
    .execute(&pools.write)
    .await
    .context("failed to sync projected_rate for system entries")?;

    run_stage7(entity_id, job_id, computed_as_of, pools).await
}

async fn run_stage7(
    entity_id: Uuid,
    job_id: Uuid,
    computed_as_of: chrono::NaiveDate,
    pools: &Pools,
) -> Result<()> {
    stage7::run(entity_id, job_id, computed_as_of, pools).await?;
    tracing::info!(%entity_id, %job_id, "pipeline complete");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    // Verify DirtyDetectionInput is accessible from this module.
    fn _check_dirty_input_type(_: dirty::DirtyDetectionInput) {}
}
