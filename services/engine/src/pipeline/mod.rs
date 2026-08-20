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
pub mod eol;
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
use chrono::NaiveDate;
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
    let computed_as_of = stage0_out.computed_as_of;
    tracing::info!(%entity_id, imported = stage0_out.imported_count, skipped = stage0_out.skipped_count, %computed_as_of, "stage 0 complete");
    notify_stage(&pools.write, entity_id, job_id, "import.process", 0, &stage_labels).await;

    if stage0_out.imported_count == 0 {
        tracing::info!(%entity_id, "stage 0 imported nothing new — skipping stages 1–7");
        return Ok(());
    }

    let stage1_out = stage1::run(entity_id, &pools.read).await?;
    tracing::info!(%entity_id, assignments = stage1_out.total_assignments, unmatched = stage1_out.unmatched_tx_ids.len(), "stage 1 complete");

    let stage2_out = stage2::run(entity_id, computed_as_of, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");
    notify_stage(&pools.write, entity_id, job_id, "import.process", 2, &stage_labels).await;

    sync_entry_start_dates(entity_id, &pools.read).await?;
    correct_entry_time_chunks(entity_id, &pools.read).await?;

    let dirty_input = dirty::DirtyDetectionInput {
        superseded_entry_ids:  stage0_out.superseded_entry_ids,
        new_entry_assignments: stage1_out.new_entry_assignments,
    };

    run_from_stage3(entity_id, job_id, "import.process", computed_as_of, Some(dirty_input), &stage_labels, pools).await
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

    let stage2_out = stage2::run(entity_id, computed_as_of, &stage1_out.unmatched_tx_ids, &pools.read).await?;
    tracing::info!(%entity_id, clusters = stage2_out.clusters_created, "stage 2 complete");
    notify_stage(&pools.write, entity_id, job_id, "entries.reprocess", 2, &stage_labels).await;

    sync_entry_start_dates(entity_id, &pools.read).await?;
    correct_entry_time_chunks(entity_id, &pools.read).await?;

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

/// Pull every entry's start_date back to the minimum date of its assigned
/// transactions when that minimum is earlier than the current start_date.
///
/// This corrects two cases:
///   - System entries seeded with a placeholder date (2000-01-01).
///   - Regular entries that receive older assignments when a new account
///     with historical data is uploaded.
///
/// `correct_entry_time_chunks` runs immediately after to repair any gaps
/// introduced by this backward extension.
async fn sync_entry_start_dates(entity_id: Uuid, pool: &sqlx::PgPool) -> Result<()> {
    sqlx::query(
        r#"
        UPDATE entries e
        SET start_date = LEAST(e.start_date, sub.min_date)
        FROM (
            SELECT tea.entry_id, MIN(t.date) AS min_date
            FROM transaction_entry_assignments tea
            JOIN transactions t ON t.id = tea.transaction_id
            WHERE t.entity_id = $1
            GROUP BY tea.entry_id
        ) sub
        WHERE e.id = sub.entry_id
          AND sub.min_date < e.start_date
        "#,
    )
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to sync entry start_dates")?;
    Ok(())
}

/// After `sync_entry_start_dates` naively extends start_date, detect
/// non-system entries whose assigned transactions contain a temporal gap
/// exceeding `period_days + TIMING_VARIANCE_THRESHOLD_DAYS`.
///
/// For each gap found, the transactions before it are split into a new
/// pending entry (same label and conditions) and the original entry's
/// start_date is advanced to the post-gap transaction date.  Multiple
/// gaps in one entry are handled left-to-right, creating one new entry
/// per prior chunk.
async fn correct_entry_time_chunks(entity_id: Uuid, pool: &sqlx::PgPool) -> Result<()> {
    #[derive(sqlx::FromRow)]
    struct TxRow {
        entry_id:    Uuid,
        period_days: i32,
        label_id:    Uuid,
        direction:   String,
        entry_type:  String,
        conditions:  serde_json::Value,
        tx_id:       Uuid,
        tx_date:     NaiveDate,
    }

    let rows: Vec<TxRow> = sqlx::query_as(
        r#"
        SELECT
            e.id          AS entry_id,
            e.period_days,
            e.label_id,
            e.direction,
            e.entry_type,
            e.conditions,
            t.id          AS tx_id,
            t.date        AS tx_date
        FROM entries e
        JOIN transaction_entry_assignments tea ON tea.entry_id = e.id
        JOIN transactions t ON t.id = tea.transaction_id
        WHERE e.entity_id = $1
          AND e.source != 'system'
          AND e.period_days IS NOT NULL
        ORDER BY e.id, t.date
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load entry transactions for gap correction")?;

    // Group rows by entry preserving the date-sorted order.
    let mut by_entry: Vec<(Uuid, Vec<TxRow>)> = Vec::new();
    for row in rows {
        if by_entry.last().map(|(id, _)| *id) == Some(row.entry_id) {
            by_entry.last_mut().unwrap().1.push(row);
        } else {
            by_entry.push((row.entry_id, vec![row]));
        }
    }

    let tolerance = TIMING_VARIANCE_THRESHOLD_DAYS as i64;

    for (entry_id, txs) in by_entry {
        let gap_threshold = txs[0].period_days as i64 + tolerance;

        // Locate all gap positions (index of the tx just BEFORE the gap).
        let gap_indices: Vec<usize> = (0..txs.len().saturating_sub(1))
            .filter(|&i| {
                (txs[i + 1].tx_date - txs[i].tx_date).num_days() > gap_threshold
            })
            .collect();

        if gap_indices.is_empty() {
            continue;
        }

        // Slice boundaries: one chunk per gap, plus the final current chunk.
        // chunks[k] = txs[starts[k]..=ends[k]]
        let starts: Vec<usize> = std::iter::once(0)
            .chain(gap_indices.iter().map(|&i| i + 1))
            .collect();
        let ends: Vec<usize> = gap_indices
            .iter()
            .copied()
            .chain(std::iter::once(txs.len() - 1))
            .collect();

        // All chunks except the last become new pending entries.
        let prior_count = starts.len() - 1;
        for k in 0..prior_count {
            let chunk = &txs[starts[k]..=ends[k]];
            let chunk_start = chunk.first().unwrap().tx_date;
            let chunk_end   = chunk.last().unwrap().tx_date;
            let tx_ids: Vec<Uuid> = chunk.iter().map(|r| r.tx_id).collect();

            let new_entry_id: Uuid = sqlx::query_scalar(
                r#"
                INSERT INTO entries (
                    entity_id, label_id, direction, entry_type, period_days,
                    conditions, status, source, start_date, end_date,
                    rate_method, matched_transaction_count
                ) VALUES (
                    $1, $2, $3, $4, $5,
                    $6::jsonb, 'pending', 'engine', $7, $8,
                    'median', $9
                ) RETURNING id
                "#,
            )
            .bind(entity_id)
            .bind(txs[0].label_id)
            .bind(&txs[0].direction)
            .bind(&txs[0].entry_type)
            .bind(txs[0].period_days)
            .bind(&txs[0].conditions)
            .bind(chunk_start)
            .bind(chunk_end)
            .bind(chunk.len() as i32)
            .fetch_one(pool)
            .await
            .context("failed to create prior-chunk entry")?;

            // Re-assign the chunk's transactions to the new entry.
            sqlx::query(
                "UPDATE transaction_entry_assignments
                 SET entry_id = $1
                 WHERE entry_id = $2 AND transaction_id = ANY($3::uuid[])",
            )
            .bind(new_entry_id)
            .bind(entry_id)
            .bind(&tx_ids)
            .execute(pool)
            .await
            .context("failed to re-assign chunk transactions")?;
        }

        // Advance the original entry's start_date to the first tx of the last chunk.
        let current_start = txs[*starts.last().unwrap()].tx_date;
        sqlx::query("UPDATE entries SET start_date = $2 WHERE id = $1")
            .bind(entry_id)
            .bind(current_start)
            .execute(pool)
            .await
            .context("failed to update entry start_date after gap correction")?;
    }

    Ok(())
}

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

    let ended_count = stage7::detect_ended_entries(entity_id, computed_as_of, &pools.write).await?;
    if ended_count > 0 {
        tracing::info!(%entity_id, ended_count, "stage 7: flagged lapsed entries for review");
    }

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
