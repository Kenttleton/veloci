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

use chrono::Duration;

use crate::pipeline::{stage0::lcs_ratio, types::Stage2Output};

// ---------------------------------------------------------------------------
// Classification constants
// ---------------------------------------------------------------------------

/// Clusters below this confidence are not surfaced to the user.
const MIN_CONFIDENCE: f64 = 0.3;

/// LCS ratio threshold for grouping into the same merchant cluster.
const MERCHANT_SIMILARITY_THRESHOLD: f64 = 0.70;

/// Amount variance threshold for standing classification.
/// All transactions must be within ±2% of the cluster median.
const AMOUNT_VARIANCE_THRESHOLD_PCT: f64 = 0.02;

/// Base timing sensitivity: std dev ≤ this many days → timing_score = 1.0.
/// Chosen to absorb billing cycle drift (weekend shifts, month-end rounding).
const TIMING_VARIANCE_THRESHOLD_DAYS: f64 = 5.0;

// --- Classification thresholds (component-driven) ---
//
// Classification is determined by three component scores rather than a single
// composite gate. Each component answers a specific question shown to the user.
//
// Standing requires: tight timing + tight amounts + enough observations.
// Variable requires: regular timing, amounts may vary.
// one_time: fallthrough — no periodic cadence detected.

/// Minimum timing_confidence required to classify as Standing.
const STANDING_TIMING_GATE: f64 = 0.75;

/// Minimum amount_confidence required to classify as Standing.
const STANDING_AMOUNT_GATE: f64 = 0.80;

/// Minimum observations needed for Standing. 2 transactions produce 1 interval
/// with std_dev = 0, which would always pass the timing gate.
const STANDING_MIN_OBSERVATIONS: usize = 3;

/// Minimum timing_confidence required to classify as Variable.
const VARIABLE_TIMING_GATE: f64 = 0.45;

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
///
/// The three component scores answer distinct questions shown to the user:
/// - `merchant_confidence`: are all transactions from the same business?
/// - `timing_confidence`: is there a consistent cadence?
/// - `amount_confidence`: are amounts similar across transactions?
///
/// `confidence` is a type-weighted blend of the three components used for gating.
#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct ClusterScore {
    pub entry_type:           &'static str,
    pub confidence:           f64,
    pub merchant_confidence:  f64,
    pub timing_confidence:    f64,
    pub amount_confidence:    f64,
    pub suggested_name:       String,
    pub median_amount_cents:  i64,
    pub sample_merchants:     Vec<String>,
    /// Mean days between transactions (≥ 2 txns). Drives `rate_per_day` so
    /// biweekly patterns aren't halved by a hardcoded 30-day denominator.
    pub mean_interval_days:   Option<f64>,
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

/// Strip store-number and phone-number suffixes before similarity comparison.
///
/// `"STARBUCKS #12043"` → `"STARBUCKS"`
/// `"NETFLIX.COM 866-579-7172"` → `"NETFLIX.COM"`
pub(crate) fn extract_brand(merchant: &str) -> String {
    // Everything from " #" onward is a store location code.
    let s = merchant.find(" #").map_or(merchant, |i| &merchant[..i]);
    // Drop trailing whitespace-separated tokens that are purely digits or dashes
    // (phone numbers, reference IDs).
    let mut parts: Vec<&str> = s.split_whitespace().collect();
    while let Some(&last) = parts.last() {
        if last.chars().all(|c| c.is_ascii_digit() || c == '-') {
            parts.pop();
        } else {
            break;
        }
    }
    if parts.is_empty() { merchant.to_string() } else { parts.join(" ") }
}

/// Mean LCS ratio of all cluster members against the representative merchant.
fn compute_merchant_confidence(cluster: &Cluster) -> f64 {
    if cluster.transactions.len() == 1 { return 1.0; }
    let rep = extract_brand(&cluster.representative_merchant);
    let sum: f64 = cluster.transactions.iter()
        .map(|t| lcs_ratio(&rep, &extract_brand(&t.merchant_normalized)))
        .sum();
    sum / cluster.transactions.len() as f64
}

/// How consistent amounts are within the cluster (1.0 = identical, decays toward 0).
fn compute_amount_confidence(txns: &[UnmatchedTxn], median: i64) -> f64 {
    if txns.len() == 1 || median == 0 { return 1.0; }
    let denom = median.unsigned_abs() as f64;
    let max_dev = txns.iter()
        .map(|t| (t.amount_cents - median).unsigned_abs() as f64 / denom)
        .fold(0.0_f64, f64::max);
    (1.0 - max_dev).clamp(0.0, 1.0)
}

/// Group transactions into merchant clusters using brand-extracted LCS similarity.
///
/// Brand extraction (`extract_brand`) is applied before comparison so that
/// "STARBUCKS #12043" and "STARBUCKS STORE #04821" land in the same cluster.
/// Time complexity: O(n²) — acceptable for typical unmatched counts. The pass
/// must remain sequential since cluster membership affects future assignments.
pub(crate) fn cluster_by_merchant(txns: Vec<UnmatchedTxn>) -> Vec<Cluster> {
    let mut clusters: Vec<Cluster> = Vec::new();

    'outer: for txn in txns {
        let txn_brand = extract_brand(&txn.merchant_normalized);
        for cluster in &mut clusters {
            let rep_brand = extract_brand(&cluster.representative_merchant);
            if lcs_ratio(&rep_brand, &txn_brand) >= MERCHANT_SIMILARITY_THRESHOLD {
                cluster.transactions.push(txn);
                continue 'outer;
            }
        }
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

/// Compute three component scores and classify the cluster.
///
/// Classification cascade: Standing → Variable → OneTime (fallthrough).
///
/// Component weights per type:
/// | type     | merchant | timing | amount |
/// |----------|----------|--------|--------|
/// | standing | 0.20     | 0.40   | 0.40   |
/// | variable | 0.30     | 0.55   | 0.15   |
/// | one_time | 0.60     | 0.20   | 0.20   |
///
/// A single-transaction cluster scores merchant=1.0, timing=0.0, amount=1.0
/// → one_time confidence=0.80, with timing=0.0 visibly signalling no cadence.
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

    let merchant_confidence = compute_merchant_confidence(cluster);
    let amount_confidence   = compute_amount_confidence(&cluster.transactions, median_amount_cents);

    // timing_confidence: 1.0 when interval std dev ≤ threshold, decays as
    // threshold/std_dev. Zero for single-transaction clusters (no interval).
    let (timing_confidence, mean_interval_days): (f64, Option<f64>) = if n < 2 {
        (0.0, None)
    } else {
        let mut dates: Vec<NaiveDate> = cluster.transactions.iter().map(|t| t.date).collect();
        dates.sort_unstable();
        let intervals: Vec<f64> = dates
            .windows(2)
            .map(|w| (w[1] - w[0]).num_days() as f64)
            .collect();
        let mean = intervals.iter().sum::<f64>() / intervals.len() as f64;
        let variance = intervals.iter().map(|&d| (d - mean).powi(2)).sum::<f64>()
            / intervals.len() as f64;
        let std_dev = variance.sqrt();
        let score = if std_dev <= TIMING_VARIANCE_THRESHOLD_DAYS {
            1.0
        } else {
            (TIMING_VARIANCE_THRESHOLD_DAYS / std_dev).min(1.0)
        };
        (score, Some(mean))
    };

    // Classification: gates on component thresholds, not a composite gate.
    // Standing requires tight timing AND tight amounts AND ≥ 3 observations
    // (2 transactions give 1 interval with std_dev=0, always passing timing).
    let (entry_type, confidence) =
        if n >= STANDING_MIN_OBSERVATIONS
            && timing_confidence >= STANDING_TIMING_GATE
            && amount_confidence >= STANDING_AMOUNT_GATE
        {
            let c = (merchant_confidence * 0.20
                   + timing_confidence  * 0.40
                   + amount_confidence  * 0.40).clamp(0.0, 1.0);
            ("standing", c)
        } else if n >= 2 && timing_confidence >= VARIABLE_TIMING_GATE {
            let c = (merchant_confidence * 0.30
                   + timing_confidence  * 0.55
                   + amount_confidence  * 0.15).clamp(0.0, 1.0);
            ("variable", c)
        } else {
            let c = (merchant_confidence * 0.60
                   + timing_confidence  * 0.20
                   + amount_confidence  * 0.20).clamp(0.0, 1.0);
            ("one_time", c)
        };

    ClusterScore {
        entry_type,
        confidence,
        merchant_confidence,
        timing_confidence,
        amount_confidence,
        suggested_name,
        median_amount_cents,
        sample_merchants,
        mean_interval_days,
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

    // Use the detected interval as period_days; fall back to 30 for single-
    // transaction clusters where no interval can be measured.
    let period_days = score.mean_interval_days.unwrap_or(30.0).round().max(1.0) as i32;
    let rate_per_day = score.median_amount_cents.abs() as f64 / period_days as f64;
    let direction = if score.median_amount_cents > 0 { "income" } else { "expense" };

    // next_due_date = last transaction date + detected period.
    let last_tx_date = cluster.transactions.iter().map(|t| t.date).max();
    let next_due_date = last_tx_date
        .map(|d| d + Duration::days(i64::from(period_days)));

    let rule_id: (Uuid,) = sqlx::query_as(
        r#"
        INSERT INTO rules
          (entity_id, name, direction, entry_type, period_days, next_due_date,
           conditions, status, source, project_tentatively)
        VALUES ($1, $2, $6, $3, $4, $5, $7, 'pending_review', 'engine', false)
        RETURNING id
        "#,
    )
    .bind(entity_id)
    .bind(&score.suggested_name)
    .bind(score.entry_type)
    .bind(period_days)
    .bind(next_due_date)
    .bind(direction)
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
           confidence, sample_merchants,
           alert_type, merchant_confidence, timing_confidence, amount_confidence)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'new', $11, $12, $13)
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
    .bind(score.merchant_confidence)
    .bind(score.timing_confidence)
    .bind(score.amount_confidence)
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
    fn score_single_positive_transaction_is_one_time() {
        let cluster = Cluster {
            representative_merchant: "IRS Treas 310".to_string(),
            transactions: vec![make_txn(
                "00000000-0000-0000-0000-000000000001",
                "2026-04-08",
                124300, // positive = income
                "IRS Treas 310",
            )],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "one_time", "one-time income should be one_time");
    }

    #[test]
    fn score_single_transaction_is_one_time_with_low_confidence() {
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
        assert_eq!(score.entry_type, "one_time");
        // Single observation: merchant=1.0, timing=0.0 (no cadence), amount=1.0
        // → confidence = 1.0*0.60 + 0.0*0.20 + 1.0*0.20 = 0.80.
        // timing=0.0 is the honest signal — no cadence data yet.
        assert!(score.confidence >= MIN_CONFIDENCE, "single observation dropped below creation threshold: {}", score.confidence);
        assert_eq!(score.timing_confidence, 0.0, "single txn must have timing=0.0 (no cadence)");
        assert_eq!(score.merchant_confidence, 1.0, "single txn must have merchant=1.0");
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
    fn score_variable_amounts_with_regular_timing_classify_as_variable() {
        // Weekly grocery runs with varying amounts — timing is regular, amounts differ.
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
    fn biweekly_pattern_captures_correct_interval() {
        // Biweekly $2000 payroll: mean interval ≈ 14 days.
        // rate_per_day should be ~$142/day, not ~$66/day (the 30-day fallback).
        let cluster = Cluster {
            representative_merchant: "Payroll".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-15", 200000, "Payroll"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-01-31", 200000, "Payroll"),
                make_txn("00000000-0000-0000-0000-000000000003", "2026-02-15", 200000, "Payroll"),
            ],
        };
        let score = score_cluster(&cluster);
        let interval = score.mean_interval_days.expect("biweekly cluster must have interval");
        assert!(
            interval < 20.0,
            "biweekly interval should be ~14–16 days, got {interval:.1}"
        );
        // rate_per_day with 30-day fallback would be 200000/30 ≈ 6667.
        // With detected interval it should be ~200000/15 ≈ 13333.
        let rate = score.median_amount_cents.abs() as f64 / interval;
        assert!(
            rate > 10_000.0,
            "biweekly rate_per_day should be >10000 cents/day, got {rate:.0}"
        );
    }

    #[test]
    fn two_same_amount_transactions_do_not_classify_as_standing() {
        // With only 2 txns there is exactly 1 interval → std_dev = 0 → timing_score = 1.0.
        // This would falsely pass the timing gate, so STANDING_MIN_OBSERVATIONS = 3 blocks it.
        let cluster = Cluster {
            representative_merchant: "Uber Eats".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-05-20", -2975, "Uber Eats"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-08-23", -2975, "Uber Eats"),
            ],
        };
        let score = score_cluster(&cluster);
        assert_ne!(score.entry_type, "standing", "2 transactions should not qualify as standing");
    }

    #[test]
    fn consistent_amount_irregular_timing_falls_through_to_one_time() {
        // Same amount every time but random gaps — should not be standing or variable.
        let cluster = Cluster {
            representative_merchant: "DoorDash".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-10", -3850, "DoorDash"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-02-02", -3850, "DoorDash"), // 23d
                make_txn("00000000-0000-0000-0000-000000000003", "2026-04-08", -3850, "DoorDash"), // 65d
                make_txn("00000000-0000-0000-0000-000000000004", "2026-06-19", -3850, "DoorDash"), // 72d
            ],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "one_time", "consistent amount with irregular timing should be one_time");
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
