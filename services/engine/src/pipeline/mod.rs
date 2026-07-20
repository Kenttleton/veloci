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
//!   1 — Active entry matching → transaction_entry_assignments; updates next_due_date
//!   2 — Pattern detection on unmatched txns → pending_review entries (with review metadata); sets next_due_date
//!   3 — Per-entry rate computation (day-crawl) — pure calculation, no entry metadata writes
//!   4 — Label rate aggregation from entry rates
//!   5 — Slope + drift regression over snapshot history
//!   6 — Snapshot UPSERT into `snapshots`
//!   7 — Cash flow projection into `projections`; raises drift/ended alerts on entries for missed expectations

pub mod stage0;
pub mod stage1;
pub mod stage2;
pub mod stage3;
pub mod stage4;
pub mod stage5;
pub mod stage6;
pub mod stage7;
pub mod types;

use anyhow::Result;
use uuid::Uuid;

use crate::db::Pools;

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

    // Stage 0: CSV normalization + dedup → transactions
    let stage0_out = stage0::run(entity_id, job_id, pending_import_id, pools).await?;

    tracing::info!(%entity_id, imported = stage0_out.imported_count, skipped = stage0_out.skipped_count, computed_as_of = %stage0_out.computed_as_of, "stage 0 complete");

    if stage0_out.imported_count == 0 {
        tracing::info!(%entity_id, "stage 0 imported nothing new — skipping stages 1–7");
        return Ok(());
    }

    // Stages 1–7 share the same computed_as_of horizon from Stage 0.
    run_from_stage1(entity_id, job_id, stage0_out.computed_as_of, pools).await
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
    run_from_stage1(entity_id, job_id, computed_as_of, pools).await
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
    run_from_stage3(entity_id, job_id, computed_as_of, pools).await
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

/// Run stages 1 → 7.
async fn run_from_stage1(
    entity_id: Uuid,
    job_id: Uuid,
    computed_as_of: chrono::NaiveDate,
    pools: &Pools,
) -> Result<()> {
    // Stage 1: Entry matching → transaction_entry_assignments
    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, unmatched = stage1_out.unmatched_tx_ids.len(), "stage 1 complete");

    // Stage 2: Pattern detection on unmatched transactions → pending_review entries
    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");

    run_from_stage3(entity_id, job_id, computed_as_of, pools).await
}

/// Run stages 3 → 7.
async fn run_from_stage3(
    entity_id: Uuid,
    job_id: Uuid,
    computed_as_of: chrono::NaiveDate,
    pools: &Pools,
) -> Result<()> {
    use crate::pipeline::types::SettlementConfig;

    // Fetch settlement window for the flux day-crawl.
    let settlement_cfg = SettlementConfig::query(entity_id, &pools.read).await?;

    let flux_start = computed_as_of - chrono::Duration::days(i64::from(settlement_cfg.settlement_window_days));

    tracing::info!(%entity_id, %flux_start, %computed_as_of, window_days = settlement_cfg.settlement_window_days, "beginning flux window day-crawl");

    // Day-crawl: Stages 3–6 run once per calendar day in the flux window.
    // Stage 7 runs once at the end with the final computed_as_of.
    let mut snapshot_date = flux_start;
    while snapshot_date <= computed_as_of {
        // Stage 3: Rate computation per active entry
        let stage3_out = stage3::run(entity_id, snapshot_date, &pools.read).await?;

        // Stage 4: Label rate aggregation from entry rates
        let stage4_out = stage4::run(entity_id, &stage3_out.entry_rates, &pools.read).await?;

        // Stage 5: Slope + drift
        let stage5_out = stage5::run(
            entity_id,
            snapshot_date,
            computed_as_of,
            &stage3_out,
            &stage4_out,
            &pools.read,
        )
        .await?;

        // Stage 6: Atomic snapshot UPSERT (write pool)
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

    tracing::info!(%entity_id, "flux window day-crawl complete");

    // Stage 7: Cash flow projection (write pool for final INSERT)
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
