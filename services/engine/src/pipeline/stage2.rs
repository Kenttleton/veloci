//! Stage 2: Pattern detection on unmatched transactions.
//!
//! **Input:** UUIDs of transactions that produced no Stage 1 assignments.
//!
//! **Output:** Candidate `entries` with `status = 'pending_review'` and review
//! metadata (alert_type, confidence, sample_merchants, etc.) written directly
//! to the entries row.
//!
//! ## Algorithm
//!
//! 1. Load the full unmatched transaction rows.
//! 2. Extract a canonical brand name from each `merchant_normalized` string by
//!    stripping noise words (Com, Store, Supercenter, etc.) and numeric tokens.
//! 3. Group by canonical brand — each distinct canonical becomes one cluster.
//! 4. `rayon::par_iter` over clusters for confidence scoring.
//! 5. Clusters above 0.3 confidence produce one `entries` row (with review
//!    metadata) and `transaction_entry_assignments` rows with the cluster
//!    confidence score.

use anyhow::{Context, Result};
use chrono::{Datelike, Duration, NaiveDate};
use rayon::prelude::*;
use sqlx::PgPool;
use std::collections::HashMap;
use uuid::Uuid;

use crate::pipeline::types::Stage2Output;

// ---------------------------------------------------------------------------
// Classification constants
// ---------------------------------------------------------------------------

/// Clusters below this fitness are not surfaced to the user.
const MIN_FITNESS: f64 = 0.3;

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
// irregular: fallthrough — no periodic cadence detected.

/// Minimum timing_fit required to classify as Standing.
const STANDING_TIMING_GATE: f64 = 0.75;

/// Minimum amount_fit required to classify as Standing.
const STANDING_AMOUNT_GATE: f64 = 0.80;

/// Minimum observations needed for Standing. 2 transactions produce 1 interval
/// with std_dev = 0, which would always pass the timing gate.
const STANDING_MIN_OBSERVATIONS: usize = 3;

/// Minimum timing_fit required to classify as Variable.
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

/// A group of unmatched transactions sharing the same canonical brand name.
#[derive(Debug)]
pub(crate) struct Cluster {
    /// The canonical brand name (derived from `extract_canonical`).
    merchant:     String,
    transactions: Vec<UnmatchedTxn>,
}

/// Score computed for a cluster.
///
/// The three component scores answer distinct questions shown to the user:
/// - `merchant_fit`: are all transactions from the same business?
/// - `timing_fit`: is there a consistent cadence?
/// - `amount_fit`: are amounts similar across transactions?
///
/// `fitness` is a type-weighted blend of the three components used for gating.
#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct ClusterScore {
    pub entry_type:           &'static str,
    pub fitness:              f64,
    pub merchant_fit:         f64,
    pub timing_fit:           f64,
    pub amount_fit:           f64,
    pub suggested_name:       String,
    pub median_amount_cents:  i64,
    pub sample_merchants:     Vec<String>,
    /// Mean days between transactions (≥ 2 txns). Drives `rate_per_day` so
    /// biweekly patterns aren't halved by a hardcoded 30-day denominator.
    pub mean_interval_days:   Option<f64>,
}

// ---------------------------------------------------------------------------
// Canonical brand extraction
// ---------------------------------------------------------------------------

/// Extract a canonical brand name from a `merchant_normalized` string.
///
/// Strips noise words (generic suffixes, TLDs, marketplace shorthands, legal
/// entity types) and pure-numeric tokens, then title-cases the result.
/// Falls back to the original string when all tokens are noise.
///
/// This is a runtime-only computation — the result is never stored on the
/// transaction row.
pub(crate) fn extract_canonical(merchant: &str) -> String {
    const NOISE: &[&str] = &[
        // TLDs and online variants
        "com", "net", "org",
        // Amazon marketplace codes
        "mktp",
        // Walmart-specific store type suffixes
        "supercenter", "neighborhood",
        // Legal entity suffixes
        "inc", "llc", "corp", "ltd",
        // Country abbreviations appended to merchant names
        "us", "usa",
    ];

    // Filter tokens that are not noise words and not pure digits.
    // Reconstruct from original casing (merchant is already title-cased).
    let filtered: Vec<&str> = merchant
        .split_whitespace()
        .filter(|w| {
            let lw = w.to_ascii_lowercase();
            !NOISE.contains(&lw.as_str()) && !lw.chars().all(|c| c.is_ascii_digit())
        })
        .collect();

    if filtered.is_empty() {
        merchant.to_string()
    } else {
        filtered.join(" ")
    }
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 2 for the given unmatched transaction IDs.
pub async fn run(entity_id: Uuid, unmatched_tx_ids: &[Uuid], pool: &PgPool) -> Result<Stage2Output> {
    if unmatched_tx_ids.is_empty() {
        return Ok(Stage2Output { clusters_created: 0 });
    }

    let txns = load_unmatched(entity_id, unmatched_tx_ids, pool).await?;

    // Group by canonical brand name — folds store variants and online vs.
    // brick-and-mortar into one cluster per merchant.
    let mut groups: HashMap<String, Vec<UnmatchedTxn>> = HashMap::new();
    for txn in txns {
        let canonical = extract_canonical(&txn.merchant_normalized);
        groups.entry(canonical).or_default().push(txn);
    }
    let clusters: Vec<Cluster> = groups
        .into_iter()
        .map(|(merchant, transactions)| Cluster { merchant, transactions })
        .collect();

    let scored: Vec<(Cluster, ClusterScore)> = clusters
        .into_par_iter()
        .map(|c| {
            let s = score_cluster(&c);
            (c, s)
        })
        .collect();

    let mut clusters_created = 0u32;
    for (cluster, score) in scored {
        if score.fitness < MIN_FITNESS {
            continue;
        }
        persist_cluster(entity_id, &cluster, &score, pool).await?;
        clusters_created += 1;
    }

    // Remove engine pending_review entries that have no transaction assignments —
    // they have no backing data and the pattern is no longer present.
    sqlx::query(
        "DELETE FROM entries
         WHERE entity_id = $1
           AND source = 'engine'
           AND status = 'pending_review'
           AND NOT EXISTS (
               SELECT 1 FROM transaction_entry_assignments
               WHERE entry_id = entries.id
           )",
    )
    .bind(entity_id)
    .execute(pool)
    .await
    .context("failed to prune orphaned engine entries")?;

    Ok(Stage2Output { clusters_created })
}


// ---------------------------------------------------------------------------
// Amount fit (pure — used by score_cluster)
// ---------------------------------------------------------------------------

/// How consistent amounts are within the cluster (1.0 = identical, decays toward 0).
fn compute_amount_fit(txns: &[UnmatchedTxn], median: i64) -> f64 {
    if txns.len() == 1 || median == 0 { return 1.0; }
    let denom = median.unsigned_abs() as f64;
    let max_dev = txns.iter()
        .map(|t| (t.amount_cents - median).unsigned_abs() as f64 / denom)
        .fold(0.0_f64, f64::max);
    (1.0 - max_dev).clamp(0.0, 1.0)
}

// ---------------------------------------------------------------------------
// Confidence scoring (pure — parallel)
// ---------------------------------------------------------------------------

/// Compute three component scores and classify the cluster.
///
/// Classification cascade: Standing → Variable → Irregular (fallthrough).
///
/// Component weights per type:
/// | type      | merchant | timing | amount |
/// |-----------|----------|--------|--------|
/// | standing  | 0.20     | 0.40   | 0.40   |
/// | variable  | 0.30     | 0.55   | 0.15   |
/// | irregular | 0.60     | 0.20   | 0.20   |
///
/// A single-transaction cluster scores merchant=1.0, timing=0.0, amount=1.0
/// → irregular confidence=0.80, with timing=0.0 visibly signalling no cadence.
pub fn score_cluster(cluster: &Cluster) -> ClusterScore {
    let n = cluster.transactions.len();
    let suggested_name = cluster.merchant.clone();
    let sample_merchants: Vec<String> = cluster
        .transactions
        .iter()
        .map(|t| t.merchant_normalized.clone())
        .collect::<std::collections::HashSet<_>>()
        .into_iter()
        .take(5)
        .collect();

    let median_amount_cents = median_amount(&cluster.transactions);

    let merchant_fit = 1.0_f64;
    let amount_fit   = compute_amount_fit(&cluster.transactions, median_amount_cents);

    // timing_fit: 1.0 when interval std dev ≤ threshold, decays as
    // threshold/std_dev. Zero for single-transaction clusters (no interval).
    let (timing_fit, mean_interval_days): (f64, Option<f64>) = if n < 2 {
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
    let (entry_type, fitness) =
        if n >= STANDING_MIN_OBSERVATIONS
            && timing_fit >= STANDING_TIMING_GATE
            && amount_fit >= STANDING_AMOUNT_GATE
        {
            let c = (merchant_fit * 0.20
                   + timing_fit  * 0.40
                   + amount_fit  * 0.40).clamp(0.0, 1.0);
            ("standing", c)
        } else if n >= 2 && timing_fit >= VARIABLE_TIMING_GATE {
            let c = (merchant_fit * 0.30
                   + timing_fit  * 0.55
                   + amount_fit  * 0.15).clamp(0.0, 1.0);
            ("variable", c)
        } else {
            let c = (merchant_fit * 0.60
                   + timing_fit  * 0.20
                   + amount_fit  * 0.20).clamp(0.0, 1.0);
            ("irregular", c)
        };

    ClusterScore {
        entry_type,
        fitness,
        merchant_fit,
        timing_fit,
        amount_fit,
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
// Recurrence anchor detection (pure)
// ---------------------------------------------------------------------------

/// Number of days in a given year/month.
fn days_in_month(year: i32, month: u32) -> u32 {
    let next = if month == 12 {
        NaiveDate::from_ymd_opt(year + 1, 1, 1)
    } else {
        NaiveDate::from_ymd_opt(year, month + 1, 1)
    }
    .expect("valid date arithmetic");
    let first = NaiveDate::from_ymd_opt(year, month, 1).expect("valid date arithmetic");
    next.signed_duration_since(first).num_days() as u32
}

/// Normalise a transaction date to a dom anchor value.
///
/// Days > 28 are converted to a negative index from month-end:
/// `anchor = day − days_in_month − 1`, e.g. last day of any month → `-1`.
/// Days ≤ 28 are returned as-is (positive).
fn normalize_dom(date: NaiveDate) -> i32 {
    let day = date.day() as i32;
    if day > 28 {
        let dim = days_in_month(date.year(), date.month()) as i32;
        day - dim - 1
    } else {
        day
    }
}

/// Return the most frequently occurring value in `doms`.
fn modal_dom(doms: &[i32]) -> i32 {
    let mut counts: HashMap<i32, usize> = HashMap::new();
    for &d in doms {
        *counts.entry(d).or_insert(0) += 1;
    }
    counts.into_iter().max_by_key(|&(_, c)| c).map(|(d, _)| d).unwrap_or(0)
}

/// Detect the recurrence anchor string from a sorted slice of transaction dates
/// and the precomputed mean interval.
///
/// Returns `None` for fewer than 2 dates (no cadence data).
///
/// Detection priority:
/// 1. `dow:N`      — weekly cadence (mean interval 5–9 d) with invariant weekday.
/// 2. `dom:N,M`    — semi-monthly (mean interval 13–17 d) with exactly 2 dom clusters.
/// 3. `dom:N`      — monthly (mean interval 20–45 d) with all doms within ±5 of modal.
/// 4. `interval:N` — fallback for any other detected cadence.
/// 5. `None`       — fewer than 2 dates.
pub(crate) fn detect_anchor(dates: &[NaiveDate], mean_interval_days: f64) -> Option<String> {
    if dates.len() < 2 {
        return None;
    }

    // ── 1. Weekly: dow:N ─────────────────────────────────────────────────────
    if (5.0..=9.0).contains(&mean_interval_days) {
        let weekdays: Vec<u32> = dates.iter().map(|d| d.weekday().num_days_from_monday()).collect();
        let first = weekdays[0];
        if weekdays.iter().all(|&w| w == first) {
            return Some(format!("dow:{first}"));
        }
    }

    // ── 2. Semi-monthly: dom:N,M ─────────────────────────────────────────────
    if (13.0..=17.0).contains(&mean_interval_days) {
        let doms: Vec<i32> = dates.iter().map(|d| normalize_dom(*d)).collect();
        let unique: std::collections::HashSet<i32> = doms.iter().copied().collect();
        if unique.len() == 2 {
            let mut anchors: Vec<i32> = unique.into_iter().collect();
            anchors.sort_unstable();
            return Some(format!("dom:{},{}", anchors[0], anchors[1]));
        }
    }

    // ── 3. Monthly: dom:N ────────────────────────────────────────────────────
    if (20.0..=45.0).contains(&mean_interval_days) {
        let doms: Vec<i32> = dates.iter().map(|d| normalize_dom(*d)).collect();
        let modal = modal_dom(&doms);
        if doms.iter().all(|&d| (d - modal).abs() <= 5) {
            return Some(format!("dom:{modal}"));
        }
    }

    // ── 4. Interval fallback ─────────────────────────────────────────────────
    Some(format!("interval:{}", mean_interval_days.round() as i64))
}

// ---------------------------------------------------------------------------
// Next-due-date helpers (pure)
// ---------------------------------------------------------------------------

/// Return the next occurrence of `weekday` (0=Mon … 6=Sun) strictly after `last`.
fn next_dow_after(weekday: u32, last: NaiveDate) -> NaiveDate {
    let start = last + Duration::days(1);
    let wd = start.weekday().num_days_from_monday();
    let days_until = (7 + weekday - wd) % 7;
    start + Duration::days(i64::from(days_until))
}

/// Resolve a dom anchor (positive = 1-indexed, negative = from month-end) to a
/// concrete day for the given year/month. Returns `None` when the day exceeds
/// the number of days in that month.
fn resolve_dom_day(dom: i32, year: i32, month: u32) -> Option<u32> {
    let dim = days_in_month(year, month) as i32;
    if dom > 0 {
        if dom <= dim { Some(dom as u32) } else { None }
    } else {
        let resolved = dim + dom + 1;
        if resolved >= 1 { Some(resolved as u32) } else { None }
    }
}

/// Return the next occurrence of dom anchor `dom` strictly after `last`.
///
/// Advances month-by-month until a valid calendar date at or after `last + 1`
/// is found. Returns `None` if no valid date exists within 25 months (safety
/// guard; in practice this should always resolve within 2 months).
fn next_dom_after(dom: i32, last: NaiveDate) -> Option<NaiveDate> {
    let start = last + Duration::days(1);
    let mut year = start.year();
    let mut month = start.month();

    for _ in 0..25 {
        if let Some(day) = resolve_dom_day(dom, year, month) {
            if let Some(date) = NaiveDate::from_ymd_opt(year, month, day) {
                if date >= start {
                    return Some(date);
                }
            }
        }
        // Advance to next month.
        if month == 12 {
            year += 1;
            month = 1;
        } else {
            month += 1;
        }
    }
    None
}

/// Compute `next_due_date` from the detected anchor string and the last
/// transaction date.
///
/// - `dow:N`   → next occurrence of that weekday after `last`.
/// - `dom:N`   → next occurrence of that dom after `last`.
/// - `dom:N,M` → earliest next occurrence among both anchors after `last`.
/// - `interval:N` / `None` → `last + period_days`.
fn compute_next_due_date(
    anchor: Option<&str>,
    last_tx_date: Option<NaiveDate>,
    period_days: i32,
) -> Option<NaiveDate> {
    let last = last_tx_date?;
    let Some(anchor) = anchor else {
        return Some(last + Duration::days(i64::from(period_days)));
    };

    if let Some(rest) = anchor.strip_prefix("dow:") {
        let n: u32 = rest.parse().ok()?;
        return Some(next_dow_after(n, last));
    }

    if let Some(rest) = anchor.strip_prefix("dom:") {
        if rest.contains(',') {
            let doms: Vec<i32> = rest.split(',').filter_map(|s| s.trim().parse().ok()).collect();
            let next_dates: Vec<NaiveDate> =
                doms.iter().filter_map(|&d| next_dom_after(d, last)).collect();
            return next_dates.into_iter().min();
        }
        let dom: i32 = rest.parse().ok()?;
        return next_dom_after(dom, last);
    }

    if let Some(rest) = anchor.strip_prefix("interval:") {
        let n: i64 = rest.parse().ok()?;
        return Some(last + Duration::days(n));
    }

    // Unknown anchor format — fall back to period_days.
    Some(last + Duration::days(i64::from(period_days)))
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
        FROM transactions
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
    cluster: &Cluster,
    score: &ClusterScore,
    pool: &PgPool,
) -> Result<Uuid> {
    // Determine condition type by checking whether all merchant_normalized values
    // in the cluster start with the canonical brand name.
    // payee_starts_with  → higher fit score (0.85–1.0) when canonical is a prefix.
    // payee_contains     → lower fit score (0.75–0.90) for broader matching.
    let canonical_lower = cluster.merchant.to_ascii_lowercase();
    let all_start_with = cluster.transactions.iter().all(|t| {
        t.merchant_normalized.to_ascii_lowercase().starts_with(&canonical_lower)
    });
    let condition_type = if all_start_with { "payee_starts_with" } else { "payee_contains" };
    let conditions = serde_json::json!({
        "op": "AND",
        "children": [{"type": condition_type, "value": &cluster.merchant}]
    });

    // Use the detected interval as period_days; fall back to 30 for single-
    // transaction clusters where no interval can be measured.
    let period_days = score.mean_interval_days.unwrap_or(30.0).round().max(1.0) as i32;
    let rate_per_day = score.median_amount_cents.abs() as f64 / period_days as f64;
    let direction = if score.median_amount_cents > 0 { "income" } else { "spend" };

    // start_date = earliest transaction in the cluster.
    let start_date = cluster.transactions.iter().map(|t| t.date).min()
        .unwrap_or_else(|| NaiveDate::from_ymd_opt(1970, 1, 1).unwrap());

    // Sort transaction dates for anchor detection.
    let mut sorted_dates: Vec<NaiveDate> = cluster.transactions.iter().map(|t| t.date).collect();
    sorted_dates.sort_unstable();
    let last_tx_date = sorted_dates.last().copied();

    // Detect recurrence anchor; skip for irregular entries (no reliable cadence).
    let anchor: Option<String> = if score.entry_type == "irregular" {
        None
    } else {
        score.mean_interval_days.and_then(|mid| detect_anchor(&sorted_dates, mid))
    };

    // Compute next_due_date based on anchor type.
    let next_due_date = compute_next_due_date(anchor.as_deref(), last_tx_date, period_days);

    // Upsert a label using the merchant name so the entry has a human-readable
    // display name in the ledger.
    let (label_id,): (Uuid,) = sqlx::query_as(
        r#"
        INSERT INTO labels (entity_id, name)
        VALUES ($1, $2)
        ON CONFLICT (entity_id, name) DO UPDATE SET name = EXCLUDED.name
        RETURNING id
        "#,
    )
    .bind(entity_id)
    .bind(&cluster.merchant)
    .fetch_one(pool)
    .await
    .context("failed to upsert label for merchant entry")?;

    // Look up an existing engine-generated pending_review entry for this label.
    // If one exists, update it in place to preserve the stable UUID.
    // Only create a new entry if none exists yet.
    let existing: Option<(Uuid,)> = sqlx::query_as(
        "SELECT id FROM entries
         WHERE entity_id = $1 AND label_id = $2 AND source = 'engine' AND status = 'pending_review'",
    )
    .bind(entity_id)
    .bind(label_id)
    .fetch_optional(pool)
    .await
    .context("failed to look up existing pending_review entry")?;

    let entry_id = if let Some((id,)) = existing {
        sqlx::query(
            "UPDATE entries SET
               direction = $2, entry_type = $3, period_days = $4, next_due_date = $5,
               recurrence_anchor = $6, conditions = $7, projected_rate_per_day = $8,
               start_date = $9, alert_type = 'new',
               fitness = $10, merchant_fit = $11, timing_fit = $12,
               amount_fit = $13, sample_merchants = $14, matched_transaction_count = $15
             WHERE id = $1",
        )
        .bind(id)
        .bind(direction)
        .bind(score.entry_type)
        .bind(period_days)
        .bind(next_due_date)
        .bind(anchor.as_deref())
        .bind(&conditions)
        .bind(rate_per_day)
        .bind(start_date)
        .bind(score.fitness)
        .bind(score.merchant_fit)
        .bind(score.timing_fit)
        .bind(score.amount_fit)
        .bind(&score.sample_merchants)
        .bind(cluster.transactions.len() as i32)
        .execute(pool)
        .await
        .context("failed to update existing pending_review entry")?;
        id
    } else {
        let (id,): (Uuid,) = sqlx::query_as(
            "INSERT INTO entries (
               entity_id, label_id, direction, entry_type, period_days, next_due_date,
               recurrence_anchor, conditions, projected_rate_per_day,
               status, source, project_tentatively, start_date,
               alert_type, fitness, merchant_fit, timing_fit, amount_fit,
               sample_merchants, matched_transaction_count
             ) VALUES (
               $1, $2, $3, $4, $5, $6,
               $7, $8, $9,
               'pending_review', 'engine', false, $10,
               'new', $11, $12, $13, $14,
               $15, $16
             )
             RETURNING id",
        )
        .bind(entity_id)
        .bind(label_id)
        .bind(direction)
        .bind(score.entry_type)
        .bind(period_days)
        .bind(next_due_date)
        .bind(anchor.as_deref())
        .bind(&conditions)
        .bind(rate_per_day)
        .bind(start_date)
        .bind(score.fitness)
        .bind(score.merchant_fit)
        .bind(score.timing_fit)
        .bind(score.amount_fit)
        .bind(&score.sample_merchants)
        .bind(cluster.transactions.len() as i32)
        .fetch_one(pool)
        .await
        .context("failed to insert pending_review entry")?;
        id
    };

    let tx_ids: Vec<Uuid> = cluster.transactions.iter().map(|t| t.id).collect();
    let entry_ids: Vec<Uuid> = vec![entry_id; tx_ids.len()];
    let fit_scores: Vec<f64> = vec![score.fitness; tx_ids.len()];

    sqlx::query(
        "INSERT INTO transaction_entry_assignments (transaction_id, entry_id, fit)
         SELECT t, e, c
         FROM UNNEST($1::uuid[], $2::uuid[], $3::float8[]) AS u(t, e, c)
         ON CONFLICT (transaction_id, entry_id) DO UPDATE SET fit = EXCLUDED.fit",
    )
    .bind(&tx_ids)
    .bind(&entry_ids)
    .bind(&fit_scores)
    .execute(pool)
    .await
    .context("failed to upsert stage 2 entry assignments")?;

    Ok(entry_id)
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

    fn make_unmatched_txn(date: &str, amount_cents: i64, merchant: &str) -> UnmatchedTxn {
        UnmatchedTxn {
            id: Uuid::new_v4(),
            date: NaiveDate::parse_from_str(date, "%Y-%m-%d").unwrap(),
            amount_cents,
            merchant_normalized: merchant.to_string(),
        }
    }

    fn date(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    // ── Grouping ──────────────────────────────────────────────────────────────

    #[test]
    fn group_by_canonical_groups_correctly() {
        let txns = vec![
            make_unmatched_txn("2026-01-07", -1499, "Netflix"),
            make_unmatched_txn("2026-02-07", -1499, "Netflix Com"),
            make_unmatched_txn("2026-01-15", -899,  "Spotify"),
        ];
        let mut groups: HashMap<String, Vec<UnmatchedTxn>> = HashMap::new();
        for txn in txns {
            let canonical = extract_canonical(&txn.merchant_normalized);
            groups.entry(canonical).or_default().push(txn);
        }
        // "Netflix" and "Netflix Com" both canonicalize to "Netflix"
        assert_eq!(groups.len(), 2);
        assert_eq!(groups["Netflix"].len(), 2);
        assert_eq!(groups["Spotify"].len(), 1);
    }

    #[test]
    fn extract_canonical_strips_noise_words() {
        assert_eq!(extract_canonical("Walmart Supercenter"), "Walmart");
        assert_eq!(extract_canonical("Netflix Com"), "Netflix");
        assert_eq!(extract_canonical("Amazon Mktp"), "Amazon");
        assert_eq!(extract_canonical("Target Store"), "Target Store"); // "Store" not in noise list
        assert_eq!(extract_canonical("Home Depot"), "Home Depot");     // "Depot" not in noise list
        assert_eq!(extract_canonical("Trader Joes"), "Trader Joes");
        // All-noise fallback returns original
        assert_eq!(extract_canonical("Com Org"), "Com Org");
    }

    // ── detect_anchor ─────────────────────────────────────────────────────────

    #[test]
    fn detect_anchor_none_for_single_date() {
        assert_eq!(detect_anchor(&[date("2026-01-15")], 30.0), None);
    }

    #[test]
    fn detect_anchor_dow_monday() {
        // Every Monday — weekday 0
        let dates = vec![
            date("2026-07-06"), // Monday
            date("2026-07-13"),
            date("2026-07-20"),
            date("2026-07-27"),
        ];
        let anchor = detect_anchor(&dates, 7.0).unwrap();
        assert_eq!(anchor, "dow:0");
    }

    #[test]
    fn detect_anchor_dow_friday() {
        // Every Friday — weekday 4
        let dates = vec![
            date("2026-07-03"), // Friday
            date("2026-07-10"),
            date("2026-07-17"),
        ];
        let anchor = detect_anchor(&dates, 7.0).unwrap();
        assert_eq!(anchor, "dow:4");
    }

    #[test]
    fn detect_anchor_dom_monthly_positive() {
        // 15th of each month
        let dates = vec![
            date("2026-01-15"),
            date("2026-02-15"),
            date("2026-03-15"),
        ];
        let anchor = detect_anchor(&dates, 30.0).unwrap();
        assert_eq!(anchor, "dom:15");
    }

    #[test]
    fn detect_anchor_dom_monthly_last_day() {
        // Last day of months with > 28 days each (Jan, Mar, May)
        let dates = vec![
            date("2026-01-31"), // day 31, dim=31 → anchor = 31-31-1 = -1
            date("2026-03-31"),
            date("2026-05-31"),
        ];
        let anchor = detect_anchor(&dates, 30.0).unwrap();
        assert_eq!(anchor, "dom:-1");
    }

    #[test]
    fn detect_anchor_dom_monthly_with_tolerance() {
        // Slight drift around the 1st: 1st, 2nd, 1st — all within ±5 of modal (1).
        let dates = vec![
            date("2026-01-01"),
            date("2026-02-02"),
            date("2026-03-01"),
        ];
        let anchor = detect_anchor(&dates, 30.0).unwrap();
        assert_eq!(anchor, "dom:1");
    }

    #[test]
    fn detect_anchor_dom_semi_monthly() {
        // 1st and 15th of each month
        let dates = vec![
            date("2026-01-01"),
            date("2026-01-15"),
            date("2026-02-01"),
            date("2026-02-15"),
        ];
        let anchor = detect_anchor(&dates, 14.5).unwrap();
        assert_eq!(anchor, "dom:1,15");
    }

    #[test]
    fn detect_anchor_interval_fallback() {
        // Quarterly — ~91 days, doesn't fit dom/dow
        let dates = vec![
            date("2026-01-01"),
            date("2026-04-02"),
            date("2026-07-02"),
        ];
        let anchor = detect_anchor(&dates, 91.0).unwrap();
        assert_eq!(anchor, "interval:91");
    }

    #[test]
    fn detect_anchor_interval_biweekly() {
        // ~14 day interval outside the semi-monthly dom range (mixed weekdays)
        // mean ~14 days and two distinct doms: 1 and 15 would be semi-monthly;
        // here we test with non-dom-friendly dates to force interval fallback.
        let dates = vec![
            date("2026-01-05"),
            date("2026-01-19"),
            date("2026-02-02"),
        ];
        // mean interval = (14 + 14) / 2 = 14 days — in semi-monthly range.
        // Normalized doms: 5, 19, 2 → 3 unique values → no semi-monthly match.
        // Falls through to interval:14.
        let anchor = detect_anchor(&dates, 14.0).unwrap();
        assert_eq!(anchor, "interval:14");
    }

    // ── compute_next_due_date ────────────────────────────────────────────────

    #[test]
    fn next_due_date_dow_advances_to_next_weekday() {
        // Last tx on Monday 2026-07-06, anchor dow:0 (Monday).
        // Next Monday after 2026-07-06 is 2026-07-13.
        let due = compute_next_due_date(Some("dow:0"), Some(date("2026-07-06")), 7);
        assert_eq!(due, Some(date("2026-07-13")));
    }

    #[test]
    fn next_due_date_dom_positive() {
        // Last tx on 2026-01-15 (15th Jan), anchor dom:15 → next is Feb 15.
        let due = compute_next_due_date(Some("dom:15"), Some(date("2026-01-15")), 30);
        assert_eq!(due, Some(date("2026-02-15")));
    }

    #[test]
    fn next_due_date_dom_negative_last_day() {
        // Last tx on 2026-01-31 (last of Jan), anchor dom:-1 → next is Feb 28.
        let due = compute_next_due_date(Some("dom:-1"), Some(date("2026-01-31")), 30);
        assert_eq!(due, Some(date("2026-02-28")));
    }

    #[test]
    fn next_due_date_dom_multi_anchor() {
        // Last tx on 2026-01-15, anchor dom:1,-1. Next is Jan 31.
        let due = compute_next_due_date(Some("dom:1,-1"), Some(date("2026-01-15")), 15);
        assert_eq!(due, Some(date("2026-01-31")));
    }

    #[test]
    fn next_due_date_interval() {
        // Last tx on 2026-01-01, anchor interval:14 → next is Jan 15.
        let due = compute_next_due_date(Some("interval:14"), Some(date("2026-01-01")), 14);
        assert_eq!(due, Some(date("2026-01-15")));
    }

    #[test]
    fn next_due_date_no_anchor_uses_period_days() {
        // No anchor → last + period_days.
        let due = compute_next_due_date(None, Some(date("2026-01-01")), 30);
        assert_eq!(due, Some(date("2026-01-31")));
    }

    #[test]
    fn next_due_date_no_last_tx_returns_none() {
        let due = compute_next_due_date(Some("dom:15"), None, 30);
        assert_eq!(due, None);
    }

    // ── Existing cluster scoring tests (struct updated, logic unchanged) ──────

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
    fn score_irregular_income_detection() {
        let cluster = Cluster {
            merchant: "IRS Treas 310".to_string(),
            transactions: vec![make_txn(
                "00000000-0000-0000-0000-000000000001",
                "2026-04-08",
                124300, // positive = income
                "IRS Treas 310",
            )],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "irregular", "no-cadence income should be irregular");
    }

    #[test]
    fn score_irregular_single_transaction_low_confidence() {
        let cluster = Cluster {
            merchant: "OneTime".to_string(),
            transactions: vec![make_txn(
                "00000000-0000-0000-0000-000000000001",
                "2026-01-15",
                -15000,
                "OneTime",
            )],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "irregular");
        // Irregular observation: merchant=1.0, timing=0.0 (no cadence), amount=1.0
        // → fitness = 1.0*0.60 + 0.0*0.20 + 1.0*0.20 = 0.80.
        // timing=0.0 is the honest signal — no cadence data yet.
        assert!(score.fitness >= MIN_FITNESS, "irregular observation dropped below creation threshold: {}", score.fitness);
        assert_eq!(score.timing_fit, 0.0, "irregular txn must have timing=0.0 (no cadence)");
        assert_eq!(score.merchant_fit, 1.0, "irregular txn must have merchant=1.0");
    }

    #[test]
    fn score_regular_transactions_standing() {
        // Three monthly Netflix charges — should classify as standing.
        let cluster = Cluster {
            merchant: "Netflix".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-07", -1499, "Netflix"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-02-07", -1499, "Netflix"),
                make_txn("00000000-0000-0000-0000-000000000003", "2026-03-07", -1499, "Netflix"),
            ],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "standing");
        assert!(score.fitness > MIN_FITNESS);
    }

    #[test]
    fn score_variable_amounts_with_regular_timing_classify_as_variable() {
        // Weekly grocery runs with varying amounts — timing is regular, amounts differ.
        let cluster = Cluster {
            merchant: "Grocery".to_string(),
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
            merchant: "Payroll".to_string(),
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
            merchant: "Uber Eats".to_string(),
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
            merchant: "DoorDash".to_string(),
            transactions: vec![
                make_txn("00000000-0000-0000-0000-000000000001", "2026-01-10", -3850, "DoorDash"),
                make_txn("00000000-0000-0000-0000-000000000002", "2026-02-02", -3850, "DoorDash"), // 23d
                make_txn("00000000-0000-0000-0000-000000000003", "2026-04-08", -3850, "DoorDash"), // 65d
                make_txn("00000000-0000-0000-0000-000000000004", "2026-06-19", -3850, "DoorDash"), // 72d
            ],
        };
        let score = score_cluster(&cluster);
        assert_eq!(score.entry_type, "irregular", "consistent amount with irregular timing should be irregular");
    }

    #[test]
    fn confidence_is_clamped_between_zero_and_one() {
        let cluster = Cluster {
            merchant: "Test".to_string(),
            transactions: (0..100)
                .map(|_| make_txn(
                    "00000000-0000-0000-0000-000000000001",
                    "2026-01-01",
                    -1000,
                    "Test",
                ))
                .collect(),
        };
        let score = score_cluster(&cluster);
        assert!(score.fitness >= 0.0 && score.fitness <= 1.0);
    }

    // ── normalize_dom ─────────────────────────────────────────────────────────

    #[test]
    fn normalize_dom_day_within_28_unchanged() {
        assert_eq!(normalize_dom(date("2026-02-14")), 14);
        assert_eq!(normalize_dom(date("2026-01-28")), 28);
        assert_eq!(normalize_dom(date("2026-03-01")), 1);
    }

    #[test]
    fn normalize_dom_last_day_of_31day_month() {
        // Jan 31: dim=31, anchor = 31-31-1 = -1
        assert_eq!(normalize_dom(date("2026-01-31")), -1);
    }

    #[test]
    fn normalize_dom_second_to_last_of_30day_month() {
        // Apr 29 in 30-day April: dim=30, anchor = 29-30-1 = -2
        assert_eq!(normalize_dom(date("2026-04-29")), -2);
    }

    #[test]
    fn normalize_dom_day_28_in_28day_feb_stays_positive() {
        // Feb 28 (non-leap year): day=28, NOT > 28 → stays as 28
        assert_eq!(normalize_dom(date("2026-02-28")), 28);
    }
}
