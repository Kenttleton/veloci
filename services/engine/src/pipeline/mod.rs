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

use std::collections::HashMap;

use anyhow::{Context, Result};
use uuid::Uuid;

use crate::db::Pools;

// ---------------------------------------------------------------------------
// Stage-label helpers
// ---------------------------------------------------------------------------

/// Label UUIDs keyed by stage number, loaded once per job from
/// `pipeline_stage_labels`. The engine never touches label names.
type StageLabelMap = HashMap<i32, Uuid>;

/// Load the entity's pipeline stage → label UUID mapping.
/// Returns an empty map (with a warning) if the table is unpopulated —
/// the pipeline continues; only stage notifications are silently skipped.
async fn query_stage_labels(entity_id: Uuid, pool: &sqlx::PgPool) -> StageLabelMap {
    let result = sqlx::query(
        "SELECT stage_num, label_id FROM pipeline_stage_labels WHERE entity_id = $1",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await;

    match result {
        Ok(rows) => {
            use sqlx::Row as _;
            rows.into_iter()
                .filter_map(|r| {
                    let stage_num: i32 = r.try_get("stage_num").ok()?;
                    let label_id: Uuid = r.try_get("label_id").ok()?;
                    Some((stage_num, label_id))
                })
                .collect()
        }
        Err(e) => {
            tracing::warn!(%entity_id, err = %e, "failed to load stage labels — stage notifications disabled");
            HashMap::new()
        }
    }
}

/// Fire a pg_notify on `job:{entity_id}` carrying the label UUID for `stage_num`.
/// Errors are logged but never propagate — stage signals are best-effort.
async fn notify_stage(
    pool:         &sqlx::PgPool,
    entity_id:    Uuid,
    job_id:       Uuid,
    job_type:     &str,
    stage_num:    i32,
    stage_labels: &StageLabelMap,
) {
    let Some(label_id) = stage_labels.get(&stage_num) else {
        tracing::warn!(%entity_id, stage_num, "no stage label mapped — skipping notify");
        return;
    };
    let payload = serde_json::json!({
        "job_id":   job_id,
        "job_type": job_type,
        "status":   "processing",
        "stage":    stage_num,
        "label_id": label_id,
    })
    .to_string();
    if let Err(e) = sqlx::query("SELECT pg_notify($1, $2)")
        .bind(format!("job:{entity_id}"))
        .bind(&payload)
        .execute(pool)
        .await
    {
        tracing::warn!(%entity_id, stage_num, err = %e, "notify_stage pg_notify failed");
    }
}

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

    let stage_labels = query_stage_labels(entity_id, &pools.read).await;

    let stage0_out = stage0::run(entity_id, job_id, pending_import_id, pools).await?;
    tracing::info!(%entity_id, imported = stage0_out.imported_count, skipped = stage0_out.skipped_count, computed_as_of = %stage0_out.computed_as_of, "stage 0 complete");
    notify_stage(&pools.write, entity_id, job_id, "import.process", 0, &stage_labels).await;

    if stage0_out.imported_count == 0 {
        tracing::info!(%entity_id, "stage 0 imported nothing new — skipping stages 1–7");
        return Ok(());
    }

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, unmatched = stage1_out.unmatched_tx_ids.len(), "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");
    notify_stage(&pools.write, entity_id, job_id, "import.process", 2, &stage_labels).await;

    let dirty_input = dirty::DirtyDetectionInput {
        superseded_entry_ids:  stage0_out.superseded_entry_ids,
        new_entry_assignments: stage1_out.new_entry_assignments,
    };

    run_from_stage3(entity_id, job_id, "import.process", stage0_out.computed_as_of, Some(dirty_input), &stage_labels, pools).await
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

    let stage_labels = query_stage_labels(entity_id, &pools.read).await;
    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, "stage 1 complete");

    let stage2_out = stage2::run(entity_id, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");
    notify_stage(&pools.write, entity_id, job_id, "entries.reprocess", 2, &stage_labels).await;

    // Bypass dirty detection — full re-run from history_start.
    run_from_stage3(entity_id, job_id, "entries.reprocess", computed_as_of, None, &stage_labels, pools).await
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
    let stage_labels = query_stage_labels(entity_id, &pools.read).await;
    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;
    run_from_stage3(entity_id, job_id, "account.analyze", computed_as_of, None, &stage_labels, pools).await
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
    let stage_labels = query_stage_labels(entity_id, &pools.read).await;
    let computed_as_of = stage0::query_computed_as_of(entity_id, &pools.read).await?;
    run_stage7(entity_id, job_id, "balance.project", computed_as_of, &stage_labels, pools).await
}

// ---------------------------------------------------------------------------
// Internal stage chains
// ---------------------------------------------------------------------------

/// Run stages 3 → 7.
async fn run_from_stage3(
    entity_id:      Uuid,
    job_id:         Uuid,
    job_type:       &str,
    computed_as_of: chrono::NaiveDate,
    dirty_input:    Option<dirty::DirtyDetectionInput>,
    stage_labels:   &StageLabelMap,
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
    notify_stage(&pools.write, entity_id, job_id, job_type, 6, stage_labels).await;

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

    run_stage7(entity_id, job_id, job_type, computed_as_of, stage_labels, pools).await
}

async fn run_stage7(
    entity_id:    Uuid,
    job_id:       Uuid,
    job_type:     &str,
    computed_as_of: chrono::NaiveDate,
    stage_labels: &StageLabelMap,
    pools:        &Pools,
) -> Result<()> {
    stage7::run(entity_id, job_id, computed_as_of, pools).await?;
    tracing::info!(%entity_id, %job_id, "pipeline complete");
    notify_stage(&pools.write, entity_id, job_id, job_type, 7, stage_labels).await;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dirty_detection_input_constructible() {
        let _ = dirty::DirtyDetectionInput {
            superseded_entry_ids:  vec![],
            new_entry_assignments: vec![],
        };
    }
}
