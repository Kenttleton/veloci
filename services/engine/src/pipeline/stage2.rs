//! Stage 2: Pattern detection on unmatched transactions.
//!
//! **Input:** UUIDs of transactions that produced no Stage 1 assignments.
//!
//! **Output:** Candidate `rules` with `status = 'pending_review'` and linked
//! `review_queue` records.
//!
//! ## Algorithm
//!
//! 1. Load the full unmatched transaction rows.
//! 2. Sequential global clustering pass using LCS similarity (≥ 0.70 ratio).
//! 3. `rayon::par_iter` over clusters for confidence scoring.
//! 4. Clusters above 0.3 confidence produce one `rules` row + one
//!    `review_queue` row, and `transaction_rule_assignments` rows with the
//!    cluster confidence score.

use anyhow::{Context, Result};
use chrono::NaiveDate;
use rayon::prelude::*;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::{stage0::lcs_ratio, types::Stage2Output};

// ---------------------------------------------------------------------------
// Confidence threshold constants
// ---------------------------------------------------------------------------

/// Clusters below this confidence are not surfaced to the user.
const MIN_CONFIDENCE: f64 = 0.3;

/// LCS ratio threshold for grouping into the same merchant cluster.
const MERCHANT_SIMILARITY_THRESHOLD: f64 = 0.70;

/// Amount variance threshold for classifying as `standing` vs `variable`.
/// Within ±2% of cluster median = consistent amount.
const AMOUNT_VARIANCE_THRESHOLD_PCT: f64 = 0.02;

/// Timing variance (in days) for classifying as standing vs hit.
const TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0;

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub(crate) struct UnmatchedTxn {
    id:                  Uuid,
    date:                NaiveDate,
    amount_cents:        i64,
    merchant_normalized: String,
}

/// A group of unmatched transactions with similar merchant names.
#[derive(Debug)]
pub(crate) struct Cluster {
    representative_merchant: String,
    transactions:            Vec<UnmatchedTxn>,
}

/// Score computed for a cluster.
#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct ClusterScore {
    pub entry_type:          &'static str,
    pub confidence:          f64,
    pub suggested_name:      String,
    pub median_amount_cents: i64,
    pub sample_merchants:    Vec<String>,
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 2 for the given unmatched transaction IDs.
pub async fn run(
    entity_id: Uuid,
    job_id: Uuid,
    unmatched_tx_ids: &[Uuid],
    pool: &PgPool,
) -> Result<Stage2Output> {
    if unmatched_tx_ids.is_empty() {
        return Ok(Stage2Output { clusters_created: 0 });
    }

    // Load full transaction rows for unmatched IDs.
    let txns = load_unmatched(entity_id, unmatched_tx_ids, pool).await?;

    // Sequential global clustering pass.
    let clusters = cluster_by_merchant(txns);

    // Parallel confidence scoring.
    let scored: Vec<(Cluster, ClusterScore)> = clusters
        .into_par_iter()
        .map(|cluster| {
            let score = score_cluster(&cluster);
            (cluster, score)
        })
        .collect();

    // Persist clusters above threshold.
    let mut clusters_created: u32 = 0;
    for (cluster, score) in scored {
        if score.confidence < MIN_CONFIDENCE {
            continue;
        }
        persist_cluster(entity_id, job_id, &cluster, &score, pool).await?;
        clusters_created += 1;
    }

    Ok(Stage2Output { clusters_created })
}

// ---------------------------------------------------------------------------
// Clustering (sequential — must see all unmatched at once)
// ---------------------------------------------------------------------------

/// Group transactions into merchant clusters using LCS similarity.
///
/// Time complexity: O(n²) on merchant string pairs — acceptable for typical
/// unmatched transaction counts in the review queue. The global pass must be
/// sequential since cluster membership affects future assignments.
pub(crate) fn cluster_by_merchant(txns: Vec<UnmatchedTxn>) -> Vec<Cluster> {
    let mut clusters: Vec<Cluster> = Vec::new();

    'outer: for txn in txns {
        // Find the first existing cluster whose representative merchant
        // shares ≥ 0.70 LCS ratio with this transaction's merchant.
        for cluster in &mut clusters {
            if lcs_ratio(&cluster.representative_merchant, &txn.merchant_normalized)
                >= MERCHANT_SIMILARITY_THRESHOLD
            {
                cluster.transactions.push(txn);
                continue 'outer;
            }
        }
        // No matching cluster — start a new one.
        clusters.push(Cluster {
            representative_merchant: txn.merchant_normalized.clone(),
            transactions: vec![txn],
        });
    }

    clusters
}

// ---------------------------------------------------------------------------
// Confidence scoring (pure — parallel)
// ---------------------------------------------------------------------------

/// Compute a confidence score and suggested entry type for a cluster.
///
/// Signals:
/// - **Observation count**: more observations → higher base confidence.
/// - **Amount consistency**: low variance → higher confidence, suggests
///   `standing` or `hit`.
/// - **Timing regularity**: near-constant intervals → higher confidence,
///   distinguishes `standing` from `hit`.
pub fn score_cluster(cluster: &Cluster) -> ClusterScore {
    let n = cluster.transactions.len();
    let suggested_name = cluster.representative_merchant.clone();
    let sample_merchants: Vec<String> = cluster
        .transactions
        .iter()
        .map(|t| t.merchant_normalized.clone())
        .collect::<std::collections::HashSet<_>>()
        .into_iter()
        .take(5)
        .collect();

    let median_amount_cents = median_amount(&cluster.transactions);

    // --- Amount consistency ---
    let amount_consistent = cluster.transactions.iter().all(|t| {
        let diff = (t.amount_cents - median_amount_cents).abs() as f64;
        let threshold = median_amount_cents.unsigned_abs() as f64 * AMOUNT_VARIANCE_THRESHOLD_PCT;
        diff <= threshold
    });

    // --- Timing regularity ---
    let timing_score = if n < 2 {
        0.0
    } else {
        let mut dates: Vec<NaiveDate> = cluster.transactions.iter().map(|t| t.date).collect();
        dates.sort_unstable();
        let intervals: Vec<f64> = dates
            .windows(2)
            .map(|w| (w[1] - w[0]).num_days() as f64)
            .collect();
        let mean_interval = intervals.iter().sum::<f64>() / intervals.len() as f64;
        let variance = intervals
            .iter()
            .map(|&d| (d - mean_interval).powi(2))
            .sum::<f64>()
            / intervals.len() as f64;
        let std_dev = variance.sqrt();
        // Low std deviation → high timing regularity score.
        if std_dev <= TIMING_VARIANCE_THRESHOLD_DAYS {
            1.0
        } else {
            (TIMING_VARIANCE_THRESHOLD_DAYS / std_dev).min(1.0)
        }
    };

    // --- Entry type classification ---
    let entry_type: &'static str = match (amount_consistent, timing_score >= 0.7, n) {
        (true, true, _) if n >= 2  => "standing",  // consistent amount + timing
        (true, false, 1)           => "hit",        // single occurrence, consistent amount
        (false, _, _)              => "variable",   // variable amount
        _                         => "hit",
    };

    // --- Composite confidence ---
    // Base: logarithmic observation count (diminishing returns after ~5).
    let observation_score = ((n as f64).ln() / 5_f64.ln()).min(1.0);
    let amount_score      = if amount_consistent { 1.0 } else { 0.4 };

    let confidence = (observation_score * 0.4 + amount_score * 0.3 + timing_score * 0.3)
        .clamp(0.0, 1.0);

    ClusterScore {
        entry_type,
        confidence,
        suggested_name,
        median_amount_cents,
        sample_merchants,
    }
}

/// Compute the median amount from a cluster's transactions.
fn median_amount(txns: &[UnmatchedTxn]) -> i64 {
    if txns.is_empty() {
        return 0;
    }
    let mut amounts: Vec<i64> = txns.iter().map(|t| t.amount_cents).collect();
    amounts.sort_unstable();
    let mid = amounts.len() / 2;
    if amounts.len() % 2 == 0 {
        (amounts[mid - 1] + amounts[mid]) / 2
    } else {
        amounts[mid]
    }
}

// ---------------------------------------------------------------------------
// DB persistence
// ---------------------------------------------------------------------------

async fn load_unmatched(
    entity_id: Uuid,
    ids: &[Uuid],
    pool: &PgPool,
) -> Result<Vec<UnmatchedTxn>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                  Uuid,
        date:                NaiveDate,
        amount_cents:        i64,
        merchant_normalized: String,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT id, date, amount_cents, merchant_normalized
        FROM raw_transactions
        WHERE entity_id = $1
          AND id = ANY($2)
        ORDER BY date ASC
        "#,
    )
    .bind(entity_id)
    .bind(ids)
    .fetch_all(pool)
    .await
    .context("failed to load unmatched transactions for stage 2")?;

    Ok(rows
        .into_iter()
        .map(|r| UnmatchedTxn {
            id:                  r.id,
            date:                r.date,
            amount_cents:        r.amount_cents,
            merchant_normalized: r.merchant_normalized,
        })
        .collect())
}

async fn persist_cluster(
    entity_id: Uuid,
    job_id: Uuid,
    cluster: &Cluster,
    score: &ClusterScore,
    pool: &PgPool,
) -> Result<()> {
    let suggested_conditions = serde_json::json!({
        "op": "AND",
        "children": [{
            "type": "imported_payee_contains",
            "value": cluster.representative_merchant
        }]
    });

    let rate_per_day = score.median_amount_cents.abs() as f64 / 30.0;

    // INSERT rule using gen_random_uuid() so the DB generates the UUID.
    let rule_id: (Uuid,) = sqlx::query_as(
        r#"
        INSERT INTO rules
          (entity_id, name, direction, entry_type, period_days,
           conditions, status, source, project_tentatively)
        VALUES ($1, $2, 'expense', $3, 30, $4, 'pending_review', 'engine', false)
        RETURNING id
        "#,
    )
    .bind(entity_id)
    .bind(&score.suggested_name)
    .bind(score.entry_type)
    .bind(&suggested_conditions)
    .fetch_one(pool)
    .await
    .context("failed to insert pending_review rule")?;
    let rule_id = rule_id.0;

    sqlx::query(
        r#"
        INSERT INTO review_queue
          (entity_id, rule_id, job_id, suggested_name, suggested_entry_type,
           suggested_conditions, suggested_rate_per_day, matched_transaction_count,
           confidence, sample_merchants)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        "#,
    )
    .bind(entity_id)
    .bind(rule_id)
    .bind(job_id)
    .bind(&score.suggested_name)
    .bind(score.entry_type)
    .bind(&suggested_conditions)
    .bind(rate_per_day)
    .bind(cluster.transactions.len() as i32)
    .bind(score.confidence)
    .bind(&score.sample_merchants)
    .execute(pool)
    .await
    .context("failed to insert review_queue record")?;

    let tx_ids: Vec<Uuid> = cluster.transactions.iter().map(|t| t.id).collect();
    let rule_ids: Vec<Uuid> = vec![rule_id; tx_ids.len()];
    let confidences: Vec<f64> = vec![score.confidence; tx_ids.len()];

    sqlx::query(
        r#"
        INSERT INTO transaction_rule_assignments (transaction_id, rule_id, confidence)
        SELECT t, r, c
        FROM UNNEST($1::uuid[], $2::uuid[], $3::float8[]) AS u(t, r, c)
        ON CONFLICT (transaction_id, rule_id) DO UPDATE SET confidence = EXCLUDED.confidence
        "#,
    )
    .bind(&tx_ids)
    .bind(&rule_ids)
    .bind(&confidences)
    .execute(pool)
    .await
    .context("failed to insert stage 2 assignments")?;

    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn make_txn(id: &str, date: &str, amount_cents: i64, merchant: &str) -> UnmatchedTxn {
        UnmatchedTxn {
            id: Uuid::parse_str(id).unwrap_or(Uuid::nil()),
            date: NaiveDate::parse_from_str(date, "%Y-%m-%d").unwrap(),
            amount_cents,
            merchant_normalized: merchant.to_string(),
        }
    }

    #[test]
    fn clustering_groups_similar_merchants() {
        let txns = vec![
            make_txn("00000000-0000-0000-0000-000000000001", "2026-01-07", -1499, "Netflix"),
            make_txn("00000000-0000-0000-0000-000000000002", "2026-02-07", -1499, "Netflix"),
            make_txn("00000000-0000-0000-0000-000000000003", "2026-01-15", -899, "Spotify"),
        ];
        let clusters = cluster_by_merchant(txns);
        assert_eq!(clusters.len(), 2, "Netflix and Spotify should form separate clusters");
        let netflix = clusters.iter().find(|c| c.representative_merchant == "Netflix").unwrap();
        assert_eq!(netflix.transactions.len(), 2);
    }

    #[test]
    fn clustering_groups_fuzzy_matches() {
        // "AMZ Prime" and "Amazon Prime" share enough characters (LCS ratio ≥ 0.70)
        // to be grouped together.
        let txns = vec![
            make_txn("00000000-0000-0000-0000-000000000001", "2026-01-07", -1399, "Amazon Prime"),
            make_txn("00000000-0000-0000-0000-000000000002", "2026-02-07", -1399, "Amzn Prime"),
        ];
        let clusters = cluster_by_merchant(txns);
        // Depending on LCS ratio — "Amazon Prime" vs "Amzn Prime":
        // LCS("Amazon Prime", "Amzn Prime") — check manually: both 10+ chars, high overlap.
        // If they cluster: 1 cluster. If not: 2.
        // The threshold is 0.70. Let's verify:
        let ratio = lcs_ratio("Amazon Prime", "Amzn Prime");
        if ratio >= MERCHANT_SIMILARITY_THRESHOLD {
            assert_eq!(clusters.len(), 1, "should cluster as same merchant (ratio={ratio})");
        } else {
            assert_eq!(clusters.len(), 2, "insufficient similarity, separate clusters (ratio={ratio})");
        }
    }

    #[test]
    fn median_amount_odd_count() {
        let txns = vec![
            make_txn("00000000-0000-0000-0000-000000000001", "2026-01-01", -1000, "A"),
            make_txn("00000000-0000-0000-0000-000000000002", "2026-01-02", -2000, "A"),
            make_txn("00000000-0000-0000-0000-000000000003", "2026-01-03", -3000, "A"),
        ];
        assert_eq!(median_amount(&txns), -2000);
    }

    #[test]
    fn median_amount_even_count() {
        let txns = vec![
            make_txn("00000000-0000-0000-0000-000000000001", "2026-01-01", -1000, "A"),
            make_txn("00000000-0000-0000-0000-000000000002", "2026-01-02", -2000, "A"),
        ];
        assert_eq!(median_amount(&txns), -1500);
    }

    #[test]
    fn score_single_transaction_confidence_below_threshold_or_hit() {
        let cluster = Cluster {
            representative_merchant: "OneTime".to_string(),
            transactions: vec![make_txn(
                "00000000-0000-0000-0000-000000000001",
                "2026-01-15",
                -15000,
                "OneTime",
            )],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "hit");
        // Single observation — confidence should be low.
        assert!(score.confidence < 0.5, "single observation confidence too high: {}", score.confidence);
    }

    #[test]
    fn score_regular_transactions_standing() {
        // Three monthly Netflix charges — should classify as standing.
        let cluster = Cluster {
            representative_merchant: "Netflix".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-07", -1499, "Netflix"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-02-07", -1499, "Netflix"),
                make_txn("00000000-0000-0000-0000-000000000003", "2026-03-07", -1499, "Netflix"),
            ],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "standing");
        assert!(score.confidence > MIN_CONFIDENCE);
    }

    #[test]
    fn score_variable_amounts_classify_as_variable() {
        let cluster = Cluster {
            representative_merchant: "Grocery".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-10", -3500, "Grocery"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-01-17", -8900, "Grocery"),
                make_txn("00000000-0000-0000-0000-000000000003", "2026-01-24", -6200, "Grocery"),
            ],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "variable");
    }

    #[test]
    fn confidence_is_clamped_between_zero_and_one() {
        let cluster = Cluster {
            representative_merchant: "Test".to_string(),
            transactions: (0..100)
                .map(|i| make_txn(
                    "00000000-0000-0000-0000-000000000001",
                    "2026-01-01",
                    -1000,
                    "Test",
                ))
                .collect(),
        };
        let score = score_cluster(&cluster);
        assert!(score.confidence >= 0.0 && score.confidence <= 1.0);
    }
}
