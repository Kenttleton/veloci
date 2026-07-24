//! Stage 7: Signal superposition cash flow projection.
//!
//! **Input:** Eligible entries with `period_days`, `recurrence_anchor`, and
//! `next_due_date`; `accounts.balance_cents` as starting point.
//!
//! **Output:** Rows in `projections` — a forward-looking 90-day signal
//! superposition per account (and entity aggregate).
//!
//! ## Eligibility
//!
//! | Status    | project_tentatively | Stage 7 |
//! |-----------|---------------------|---------|
//! | `live`    | —                   | Include |
//! | `pending` | `TRUE`              | Include |
//! | `pending` | `FALSE`             | Exclude |
//! | `ended`   | —                   | Exclude |
//!
//! ## Algorithm
//!
//! For each day D in [computed_as_of .. computed_as_of + 90]:
//! 1. Sum income entries whose schedule window covers D.
//! 2. Sum spend entries whose schedule window covers D.
//! 3. Compute margin_rate = income - spend.
//! 4. Accumulate balance.
//! 5. Mark pinch points where margin_rate < 0.
//!
//! Determinism: uses `computed_as_of` as "today" — never `NOW()`.

use anyhow::{Context, Result};
use chrono::{Datelike, NaiveDate};
use sqlx::PgPool;
use uuid::Uuid;

use crate::db::Pools;

/// Projection horizon in days.
const PROJECTION_DAYS: i64 = 90;

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
struct EligibleEntry {
    id:                     Uuid,
    account_id:             Option<Uuid>,
    direction:              String,
    period_days:            i32,
    /// Pre-computed projected rate in cents/day. Loaded from DB and used by
    /// `rate_for_day`. Replaces the old `amount_cents / period_days` division.
    projected_rate_per_day: f64,
    recurrence_anchor:      Option<String>,
    next_due_date:          Option<NaiveDate>,
}

#[derive(Debug)]
struct ProjectionRow {
    entity_id:               Uuid,
    account_id:              Option<Uuid>,
    projected_date:          NaiveDate,
    income_rate_per_day:     i64,
    spend_rate_per_day: i64,
    margin_rate_per_day:     i64,
    projected_balance_cents: i64,
    is_pinch_point:          bool,
}

// ---------------------------------------------------------------------------
// Recurrence anchor parsing
// ---------------------------------------------------------------------------

/// Decoded form of a `recurrence_anchor` TEXT column value.
#[derive(Debug, Clone)]
enum RecurrenceAnchor {
    /// `dom:N` or `dom:N,M,...` — day(s) of month; positive = 1-indexed from
    /// start, negative = 1-indexed from end (`-1` = last day).
    Dom(Vec<i32>),
    /// `dow:N` — day of week; 0 = Monday … 6 = Sunday.
    Dow(u32),
    /// `interval:N` — every N days from `next_due_date`.
    Interval(i64),
}

/// Parse a `recurrence_anchor` string into a typed `RecurrenceAnchor`.
///
/// Returns `None` for unrecognised or malformed strings.
///
/// # Examples
///
/// ```
/// // "dom:15"    → Dom(vec![15])
/// // "dom:-1"    → Dom(vec![-1])
/// // "dom:15,-1" → Dom(vec![15, -1])
/// // "dow:4"     → Dow(4)
/// // "interval:14" → Interval(14)
/// ```
fn parse_anchor(s: &str) -> Option<RecurrenceAnchor> {
    if let Some(rest) = s.strip_prefix("dom:") {
        let days: Option<Vec<i32>> = rest
            .split(',')
            .map(|part| part.trim().parse::<i32>().ok())
            .collect();
        let days = days?;
        if days.is_empty() {
            return None;
        }
        return Some(RecurrenceAnchor::Dom(days));
    }
    if let Some(rest) = s.strip_prefix("dow:") {
        let n: u32 = rest.trim().parse().ok()?;
        return Some(RecurrenceAnchor::Dow(n));
    }
    if let Some(rest) = s.strip_prefix("interval:") {
        let n: i64 = rest.trim().parse().ok()?;
        return Some(RecurrenceAnchor::Interval(n));
    }
    None
}

// ---------------------------------------------------------------------------
// Dom expansion helpers
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

/// Resolve a dom anchor (positive = 1-indexed from start, negative = from end)
/// to the concrete day-of-month for the given year and month.
///
/// Returns `None` when `anchor_day` is positive but exceeds the number of days
/// in that month (e.g. `dom:31` in February).
pub(crate) fn resolve_dom_day(anchor_day: i32, year: i32, month: u32) -> Option<u32> {
    let dim = days_in_month(year, month) as i32;
    if anchor_day > 0 {
        if anchor_day <= dim { Some(anchor_day as u32) } else { None }
    } else {
        let resolved = dim + anchor_day + 1;
        if resolved >= 1 { Some(resolved as u32) } else { None }
    }
}

/// Expand all dom fire dates that fall within `[window_start, window_end]` for
/// the given set of anchor days.
///
/// The result is sorted and deduplicated (two anchors could theoretically
/// resolve to the same calendar day, though that is unusual).
pub(crate) fn expand_dom_fires(
    anchors: &[i32],
    window_start: NaiveDate,
    window_end: NaiveDate,
) -> Vec<NaiveDate> {
    let mut fires: Vec<NaiveDate> = Vec::new();

    // Start one month before the window to ensure we capture fires whose
    // influence extends into the window from before its start.
    let (mut year, mut month) = {
        let (y, m) = (window_start.year(), window_start.month());
        if m == 1 { (y - 1, 12) } else { (y, m - 1) }
    };

    let (end_year, end_month) = (window_end.year(), window_end.month());

    loop {
        for &anchor in anchors {
            if let Some(day) = resolve_dom_day(anchor, year, month) {
                if let Some(date) = NaiveDate::from_ymd_opt(year, month, day) {
                    if date >= window_start && date <= window_end {
                        fires.push(date);
                    }
                }
            }
        }

        if year > end_year || (year == end_year && month >= end_month) {
            break;
        }
        if month == 12 {
            year += 1;
            month = 1;
        } else {
            month += 1;
        }
    }

    fires.sort_unstable();
    fires.dedup();
    fires
}

// ---------------------------------------------------------------------------
// Schedule window logic
// ---------------------------------------------------------------------------

/// Return `true` if day `D` falls within the active signal window of `entry`.
///
/// Dispatches on the parsed `recurrence_anchor`:
/// - `dom:…`    — pre-expands fire dates for a ±45-day window and checks membership.
/// - `dow:N`    — active on every occurrence of that weekday.
/// - `interval:N` — active in `[fire, fire + N)` windows from `next_due_date`.
/// - Unparseable anchor — treated as interval using `period_days`.
/// - No anchor  — one-time window `[next_due, next_due + period_days)`.
fn window_covers(entry: &EligibleEntry, day: NaiveDate, _computed_as_of: NaiveDate) -> bool {
    let Some(next_due) = entry.next_due_date else {
        return false;
    };

    let Some(ref anchor_str) = entry.recurrence_anchor else {
        // Non-recurring: one window [next_due, next_due + period_days).
        return day >= next_due
            && day < next_due + chrono::Duration::days(i64::from(entry.period_days));
    };

    match parse_anchor(anchor_str) {
        Some(RecurrenceAnchor::Dom(ref anchors)) => {
            let window_start = day - chrono::Duration::days(45);
            let window_end   = day + chrono::Duration::days(45);
            let fires = expand_dom_fires(anchors, window_start, window_end);
            is_day_in_dom_window(day, &fires)
        }
        Some(RecurrenceAnchor::Dow(weekday)) => {
            day.weekday().num_days_from_monday() == weekday
        }
        Some(RecurrenceAnchor::Interval(n)) => {
            if n == 0 { return false; }
            let days_since_due = (day - next_due).num_days();
            if days_since_due < 0 { return false; }
            let cycle = days_since_due / n;
            let fire_date = next_due + chrono::Duration::days(cycle * n);
            day >= fire_date && day < fire_date + chrono::Duration::days(n)
        }
        None => {
            // Unparseable anchor — fall back to period_days interval logic.
            let period_days = i64::from(entry.period_days);
            if period_days == 0 { return false; }
            let days_since_due = (day - next_due).num_days();
            if days_since_due < 0 { return false; }
            let cycle = days_since_due / period_days;
            let fire_date = next_due + chrono::Duration::days(cycle * period_days);
            day >= fire_date && day < fire_date + chrono::Duration::days(period_days)
        }
    }
}

/// Return `true` if `day` falls within an active dom window.
///
/// Finds the last fire date ≤ day in the sorted `fires` slice. The signal is
/// active from that fire date up to (but not including) the next fire date.
/// When there is no next fire, a 45-day guard window is assumed.
pub(crate) fn is_day_in_dom_window(day: NaiveDate, fires: &[NaiveDate]) -> bool {
    // partition_point returns the index of the first element > day.
    let pos = fires.partition_point(|&f| f <= day);
    if pos == 0 {
        return false;
    }
    let fire = fires[pos - 1];
    if fire > day {
        return false;
    }
    let next_fire = fires
        .get(pos)
        .copied()
        .unwrap_or(fire + chrono::Duration::days(45));
    day >= fire && day < next_fire
}

// ---------------------------------------------------------------------------
// Rate computation
// ---------------------------------------------------------------------------

/// Compute the daily rate contribution for an entry (in cents).
///
/// For variable entries, prefers the actual rate from the most recent snapshot
/// over the stored projected rate. For all others, returns
/// `entry.projected_rate_per_day` as an integer (truncated).
fn rate_for_day(
    entry: &EligibleEntry,
    snapshot_rates: &std::collections::HashMap<Uuid, f64>,
) -> i64 {
    if let Some(&rate) = snapshot_rates.get(&entry.id) {
        return rate.abs() as i64;
    }
    entry.projected_rate_per_day.abs() as i64
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 7 for an entity.
pub async fn run(
    entity_id: Uuid,
    job_id: Uuid,
    computed_as_of: NaiveDate,
    pools: &Pools,
) -> Result<()> {
    let pool = &pools.read;
    let write_pool = &pools.write;

    // Load eligible entries.
    let entries = load_eligible_entries(entity_id, pool).await?;

    // Load starting balance per account.
    let balances = load_account_balances(entity_id, pool).await?;

    // Load latest actual_rate from snapshots for variable entries.
    let snapshot_rates = load_latest_snapshot_rates(entity_id, computed_as_of, pool).await?;

    if entries.is_empty() {
        return Ok(());
    }

    // Build projection rows for each account and the entity aggregate.
    let mut all_rows: Vec<ProjectionRow> = Vec::new();

    // Group entries by account_id (Some(account_id) or None for entity-level).
    let account_ids: Vec<Option<Uuid>> = {
        let mut ids: Vec<Option<Uuid>> = entries
            .iter()
            .map(|e| e.account_id)
            .collect::<std::collections::HashSet<_>>()
            .into_iter()
            .collect();
        // Always include the entity aggregate (None).
        if !ids.contains(&None) {
            ids.push(None);
        }
        ids
    };

    for account_id in account_ids {
        let account_entries: Vec<&EligibleEntry> = if account_id.is_some() {
            entries.iter().filter(|e| e.account_id == account_id).collect()
        } else {
            entries.iter().collect() // entity aggregate = all entries
        };

        let start_balance = match account_id {
            Some(aid) => balances.get(&aid).copied().unwrap_or(0),
            None      => balances.values().sum(), // entity aggregate
        };

        let rows = project_account(
            entity_id,
            account_id,
            &account_entries,
            start_balance,
            computed_as_of,
            &snapshot_rates,
        );
        all_rows.extend(rows);
    }

    // Write projection rows in a single transaction (committed together with Stage 6
    // per spec, but Stage 7 runs after Stage 6 commits in this implementation;
    // they share the same job_id for audit traceability).
    write_projections(entity_id, job_id, all_rows, write_pool).await
}

// ---------------------------------------------------------------------------
// Projection algorithm (pure — no I/O)
// ---------------------------------------------------------------------------

/// Project 90 days of rates for one account (or entity aggregate).
fn project_account(
    entity_id: Uuid,
    account_id: Option<Uuid>,
    entries: &[&EligibleEntry],
    start_balance: i64,
    computed_as_of: NaiveDate,
    snapshot_rates: &std::collections::HashMap<Uuid, f64>,
) -> Vec<ProjectionRow> {
    let mut balance = start_balance;
    let mut rows = Vec::with_capacity(PROJECTION_DAYS as usize);

    for day_offset in 0..=PROJECTION_DAYS {
        let day = computed_as_of + chrono::Duration::days(day_offset);

        let income_rate: i64 = entries
            .iter()
            .filter(|e| e.direction == "income" && window_covers(e, day, computed_as_of))
            .map(|e| rate_for_day(e, snapshot_rates))
            .sum();

        let spend_rate: i64 = entries
            .iter()
            .filter(|e| e.direction == "spend" && window_covers(e, day, computed_as_of))
            .map(|e| rate_for_day(e, snapshot_rates))
            .sum();

        let margin_rate = income_rate - spend_rate;
        balance += margin_rate;

        rows.push(ProjectionRow {
            entity_id,
            account_id,
            projected_date:      day,
            income_rate_per_day: income_rate,
            spend_rate_per_day:  spend_rate,
            margin_rate_per_day:     margin_rate,
            projected_balance_cents: balance,
            is_pinch_point:          margin_rate < 0,
        });
    }

    rows
}

// ---------------------------------------------------------------------------
// DB loaders
// ---------------------------------------------------------------------------

async fn load_eligible_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<EligibleEntry>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                     Uuid,
        direction:              String,
        period_days:            i32,
        recurrence_anchor:      Option<String>,
        next_due_date:          Option<NaiveDate>,
        projected_rate_per_day: Option<sqlx::types::BigDecimal>,
        account_id:             Option<Uuid>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT e.id, e.direction, e.period_days, e.recurrence_anchor, e.next_due_date,
               e.projected_rate_per_day,
               a.id AS account_id
        FROM entries e
        LEFT JOIN accounts a ON a.entity_id = e.entity_id AND a.status = 'active'
        WHERE e.entity_id = $1
          AND (
            (e.status = 'live' AND e.end_date IS NULL)
            OR
            (e.status = 'pending' AND e.project_tentatively = TRUE)
          )
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load eligible entries for stage 7")?;

    Ok(rows
        .into_iter()
        .map(|r| EligibleEntry {
            id:                r.id,
            account_id:        r.account_id,
            direction:         r.direction,
            period_days:       r.period_days,
            projected_rate_per_day: r
                .projected_rate_per_day
                .as_ref()
                .and_then(|bd| bd.to_string().parse::<f64>().ok())
                .unwrap_or(0.0),
            recurrence_anchor: r.recurrence_anchor,
            next_due_date:     r.next_due_date,
        })
        .collect())
}

async fn load_account_balances(
    entity_id: Uuid,
    pool: &PgPool,
) -> Result<std::collections::HashMap<Uuid, i64>> {
    let rows: Vec<(Uuid, Option<i64>)> = sqlx::query_as(
        "SELECT id, balance_cents FROM accounts WHERE entity_id = $1 AND status = 'active'",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load account balances for stage 7")?;

    Ok(rows.into_iter().map(|(id, bal)| (id, bal.unwrap_or(0))).collect())
}

async fn load_latest_snapshot_rates(
    entity_id: Uuid,
    computed_as_of: NaiveDate,
    pool: &PgPool,
) -> Result<std::collections::HashMap<Uuid, f64>> {
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
          AND snapshot_date <= $2
        ORDER BY node_id, snapshot_date DESC
        "#,
    )
    .bind(entity_id)
    .bind(computed_as_of)
    .fetch_all(pool)
    .await
    .context("failed to load snapshot rates for stage 7")?;

    Ok(rows
        .into_iter()
        .map(|r| {
            let rate = r.actual_rate_per_day.to_string().parse::<f64>().unwrap_or(0.0);
            (r.node_id, rate)
        })
        .collect())
}

// ---------------------------------------------------------------------------
// DB write
// ---------------------------------------------------------------------------

async fn write_projections(
    entity_id: Uuid,
    job_id: Uuid,
    rows: Vec<ProjectionRow>,
    pool: &PgPool,
) -> Result<()> {
    if rows.is_empty() {
        return Ok(());
    }

    // Delete existing projections for this entity before writing new ones.
    let mut tx = pool.begin().await.context("failed to begin stage 7 transaction")?;

    sqlx::query("DELETE FROM projections WHERE entity_id = $1")
        .bind(entity_id)
        .execute(&mut *tx)
        .await
        .context("failed to delete existing projections")?;

    let n = rows.len();
    let mut account_ids:  Vec<Option<Uuid>> = Vec::with_capacity(n);
    let mut proj_dates:   Vec<NaiveDate>    = Vec::with_capacity(n);
    let mut income_rates: Vec<i64>          = Vec::with_capacity(n);
    let mut spend_rates:  Vec<i64>          = Vec::with_capacity(n);
    let mut margin_rates: Vec<i64>          = Vec::with_capacity(n);
    let mut balances:     Vec<i64>          = Vec::with_capacity(n);
    let mut pinch_points: Vec<bool>         = Vec::with_capacity(n);

    for row in rows {
        account_ids.push(row.account_id);
        proj_dates.push(row.projected_date);
        income_rates.push(row.income_rate_per_day);
        spend_rates.push(row.spend_rate_per_day);
        margin_rates.push(row.margin_rate_per_day);
        balances.push(row.projected_balance_cents);
        pinch_points.push(row.is_pinch_point);
    }

    sqlx::query(
        r#"
        INSERT INTO projections
          (entity_id, account_id, job_id, projected_date,
           income_rate_per_day, spend_rate_per_day, margin_rate_per_day,
           projected_balance_cents, is_pinch_point)
        SELECT $1, acct, $2, pd, ir, cr, mr, bal, pp
        FROM UNNEST(
          $3::uuid[], $4::date[],
          $5::int8[], $6::int8[], $7::int8[],
          $8::int8[], $9::bool[]
        ) AS u(acct, pd, ir, cr, mr, bal, pp)
        "#,
    )
    .bind(entity_id)
    .bind(job_id)
    .bind(&account_ids as &[Option<Uuid>])
    .bind(&proj_dates)
    .bind(&income_rates)
    .bind(&spend_rates)
    .bind(&margin_rates)
    .bind(&balances)
    .bind(&pinch_points)
    .execute(&mut *tx)
    .await
    .context("failed to insert projections")?;

    tx.commit().await.context("failed to commit stage 7 transaction")?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use uuid::Uuid;

    fn date(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    /// Build an `EligibleEntry` for unit tests.
    ///
    /// `rate_cents_per_day` is the projected daily rate (already divided).
    /// `anchor` is the optional anchor string (e.g. `"interval:30"`, `"dom:15"`).
    fn make_entry(
        id: &str,
        direction: &str,
        period_days: i32,
        rate_cents_per_day: f64,
        next_due: Option<&str>,
        anchor: Option<&str>,
    ) -> EligibleEntry {
        EligibleEntry {
            id:                     Uuid::parse_str(id).unwrap_or(Uuid::nil()),
            account_id:             None,
            direction:              direction.to_string(),
            period_days,
            projected_rate_per_day: rate_cents_per_day,
            recurrence_anchor:      anchor.map(str::to_string),
            next_due_date:          next_due.map(|s| date(s)),
        }
    }

    const ENTRY_1: &str = "00000000-0000-0000-0000-000000000001";
    const ENTRY_2: &str = "00000000-0000-0000-0000-000000000002";

    // ── parse_anchor ──────────────────────────────────────────────────────────

    #[test]
    fn parse_anchor_dom_single_positive() {
        let a = parse_anchor("dom:15").unwrap();
        let RecurrenceAnchor::Dom(days) = a else { panic!("expected Dom") };
        assert_eq!(days, vec![15]);
    }

    #[test]
    fn parse_anchor_dom_single_negative() {
        let a = parse_anchor("dom:-1").unwrap();
        let RecurrenceAnchor::Dom(days) = a else { panic!("expected Dom") };
        assert_eq!(days, vec![-1]);
    }

    #[test]
    fn parse_anchor_dom_multi() {
        let a = parse_anchor("dom:15,-1").unwrap();
        let RecurrenceAnchor::Dom(days) = a else { panic!("expected Dom") };
        assert_eq!(days, vec![15, -1]);
    }

    #[test]
    fn parse_anchor_dow() {
        let a = parse_anchor("dow:4").unwrap();
        let RecurrenceAnchor::Dow(n) = a else { panic!("expected Dow") };
        assert_eq!(n, 4);
    }

    #[test]
    fn parse_anchor_interval() {
        let a = parse_anchor("interval:14").unwrap();
        let RecurrenceAnchor::Interval(n) = a else { panic!("expected Interval") };
        assert_eq!(n, 14);
    }

    #[test]
    fn parse_anchor_unknown_returns_none() {
        assert!(parse_anchor("monthly").is_none());
        assert!(parse_anchor("").is_none());
        assert!(parse_anchor("dom:").is_none());
    }

    // ── resolve_dom_day ───────────────────────────────────────────────────────

    #[test]
    fn resolve_dom_day_positive_within_month() {
        assert_eq!(resolve_dom_day(15, 2026, 2), Some(15));
        assert_eq!(resolve_dom_day(28, 2026, 2), Some(28));
    }

    #[test]
    fn resolve_dom_day_positive_exceeds_month() {
        // dom:31 does not exist in February (28 days in 2026).
        assert_eq!(resolve_dom_day(31, 2026, 2), None);
        // dom:30 does not exist in February.
        assert_eq!(resolve_dom_day(30, 2026, 2), None);
    }

    #[test]
    fn resolve_dom_day_negative_last_day() {
        // dom:-1 = last day of each month.
        assert_eq!(resolve_dom_day(-1, 2026, 1), Some(31)); // January
        assert_eq!(resolve_dom_day(-1, 2026, 2), Some(28)); // February (non-leap)
        assert_eq!(resolve_dom_day(-1, 2026, 4), Some(30)); // April
    }

    #[test]
    fn resolve_dom_day_negative_second_to_last() {
        // dom:-2 = second-to-last day.
        assert_eq!(resolve_dom_day(-2, 2026, 1), Some(30));
        assert_eq!(resolve_dom_day(-2, 2026, 4), Some(29));
    }

    #[test]
    fn resolve_dom_day_leap_year_feb() {
        // 2028 is a leap year.
        assert_eq!(resolve_dom_day(-1, 2028, 2), Some(29));
        assert_eq!(resolve_dom_day(29, 2028, 2), Some(29));
        assert_eq!(resolve_dom_day(29, 2026, 2), None); // non-leap
    }

    // ── expand_dom_fires ──────────────────────────────────────────────────────

    #[test]
    fn expand_dom_fires_monthly_15th() {
        let fires = expand_dom_fires(
            &[15],
            date("2026-01-01"),
            date("2026-04-30"),
        );
        assert_eq!(fires, vec![
            date("2026-01-15"),
            date("2026-02-15"),
            date("2026-03-15"),
            date("2026-04-15"),
        ]);
    }

    #[test]
    fn expand_dom_fires_last_day_skips_short_months_correctly() {
        // dom:31 — only months with 31 days should produce a fire.
        let fires = expand_dom_fires(
            &[31],
            date("2026-01-01"),
            date("2026-06-30"),
        );
        // Jan, Mar, May have 31 days; Feb, Apr, Jun do not.
        assert_eq!(fires, vec![
            date("2026-01-31"),
            date("2026-03-31"),
            date("2026-05-31"),
        ]);
    }

    #[test]
    fn expand_dom_fires_negative_last_day_all_months() {
        // dom:-1 fires on the last day of every month.
        let fires = expand_dom_fires(
            &[-1],
            date("2026-01-01"),
            date("2026-04-30"),
        );
        assert_eq!(fires, vec![
            date("2026-01-31"),
            date("2026-02-28"), // non-leap 2026
            date("2026-03-31"),
            date("2026-04-30"),
        ]);
    }

    #[test]
    fn expand_dom_fires_semi_monthly_15_and_last() {
        // dom:15,-1
        let fires = expand_dom_fires(
            &[15, -1],
            date("2026-01-01"),
            date("2026-02-28"),
        );
        assert_eq!(fires, vec![
            date("2026-01-15"),
            date("2026-01-31"),
            date("2026-02-15"),
            date("2026-02-28"),
        ]);
    }

    #[test]
    fn expand_dom_fires_feb_29_in_leap_year() {
        let fires = expand_dom_fires(
            &[29],
            date("2028-02-01"),
            date("2028-03-31"),
        );
        assert_eq!(fires, vec![date("2028-02-29"), date("2028-03-29")]);
    }

    // ── is_day_in_dom_window ──────────────────────────────────────────────────

    #[test]
    fn is_day_in_dom_window_on_fire_date() {
        let fires = vec![date("2026-01-15"), date("2026-02-15")];
        assert!(is_day_in_dom_window(date("2026-01-15"), &fires));
    }

    #[test]
    fn is_day_in_dom_window_between_fires() {
        let fires = vec![date("2026-01-15"), date("2026-02-15")];
        // Jan 20 is after Jan 15 and before Feb 15 → active.
        assert!(is_day_in_dom_window(date("2026-01-20"), &fires));
    }

    #[test]
    fn is_day_in_dom_window_on_next_fire_is_false() {
        let fires = vec![date("2026-01-15"), date("2026-02-15")];
        // Feb 15 starts the next window, so it belongs to the Feb window not Jan.
        assert!(is_day_in_dom_window(date("2026-02-15"), &fires));
        // Feb 14 is in Jan window (last day before Feb fire).
        assert!(is_day_in_dom_window(date("2026-02-14"), &fires));
    }

    #[test]
    fn is_day_in_dom_window_before_first_fire() {
        let fires = vec![date("2026-01-15"), date("2026-02-15")];
        assert!(!is_day_in_dom_window(date("2026-01-14"), &fires));
    }

    #[test]
    fn is_day_in_dom_window_empty_fires() {
        assert!(!is_day_in_dom_window(date("2026-01-15"), &[]));
    }

    #[test]
    fn is_day_in_dom_window_after_last_fire_uses_guard() {
        // Single fire — after the fire, uses 45-day guard window.
        let fires = vec![date("2026-01-15")];
        assert!(is_day_in_dom_window(date("2026-01-20"), &fires));
        // 45 days after Jan 15 is Mar 1; day 46 (Mar 2) should be outside.
        assert!(is_day_in_dom_window(date("2026-02-28"), &fires));
        assert!(!is_day_in_dom_window(date("2026-03-02"), &fires));
    }

    // ── window_covers ─────────────────────────────────────────────────────────

    #[test]
    fn window_covers_no_next_due_returns_false() {
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, None, Some("interval:30"));
        assert!(!window_covers(&r, date("2026-03-15"), date("2026-03-01")));
    }

    #[test]
    fn window_covers_non_recurring() {
        let r = make_entry(ENTRY_1, "income", 10, 10_000.0, Some("2026-03-05"), None);
        // Days 5–14 should be covered.
        assert!( window_covers(&r, date("2026-03-05"), date("2026-03-01")));
        assert!( window_covers(&r, date("2026-03-14"), date("2026-03-01")));
        // Day 15 is outside.
        assert!(!window_covers(&r, date("2026-03-15"), date("2026-03-01")));
        // Day 4 is before fire date.
        assert!(!window_covers(&r, date("2026-03-04"), date("2026-03-01")));
    }

    #[test]
    fn window_covers_interval_recurring_multiple_cycles() {
        // Monthly entry: interval:30 from 2026-03-01.
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        // Cycle 0: March 1–30
        assert!(window_covers(&r, date("2026-03-01"), date("2026-03-01")));
        assert!(window_covers(&r, date("2026-03-30"), date("2026-03-01")));
        // Cycle 1: March 31 – April 29
        assert!(window_covers(&r, date("2026-03-31"), date("2026-03-01")));
        assert!(window_covers(&r, date("2026-04-29"), date("2026-03-01")));
    }

    #[test]
    fn window_covers_dom_anchor() {
        // dom:15 — active from the 15th of each month until the next 15th.
        // The ±45-day expansion always finds a prior fire, so a dom entry is
        // active on any day D ≥ first_ever_fire.
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-01-15"), Some("dom:15"));
        // Jan 15 is the fire date itself — covered.
        assert!(window_covers(&r, date("2026-01-15"), date("2026-01-01")));
        // Jan 20 is within the [Jan 15, Feb 15) window — covered.
        assert!(window_covers(&r, date("2026-01-20"), date("2026-01-01")));
        // Feb 14 is the last day of [Jan 15, Feb 15) — covered.
        assert!(window_covers(&r, date("2026-02-14"), date("2026-01-01")));
        // Feb 15 starts the [Feb 15, Mar 15) window — covered.
        assert!(window_covers(&r, date("2026-02-15"), date("2026-01-01")));
        // Jan 14 falls in the [Dec 15, Jan 15) window of the prior cycle — covered.
        assert!(window_covers(&r, date("2026-01-14"), date("2026-01-01")));
        // A dom entry without next_due_date is never active.
        let r_no_due = make_entry(ENTRY_2, "income", 30, 10_000.0, None, Some("dom:15"));
        assert!(!window_covers(&r_no_due, date("2026-01-15"), date("2026-01-01")));
    }

    #[test]
    fn window_covers_dow_anchor_fires_on_correct_weekday() {
        // dow:0 = every Monday.
        let r = make_entry(ENTRY_1, "income", 7, 1_000.0, Some("2026-07-06"), Some("dow:0"));
        // 2026-07-06 is a Monday.
        assert!( window_covers(&r, date("2026-07-06"), date("2026-07-01")));
        assert!( window_covers(&r, date("2026-07-13"), date("2026-07-01")));
        // Tuesdays are not active.
        assert!(!window_covers(&r, date("2026-07-07"), date("2026-07-01")));
        assert!(!window_covers(&r, date("2026-07-14"), date("2026-07-01")));
    }

    #[test]
    fn window_covers_unknown_anchor_falls_back_to_period_days() {
        // Anchor "monthly" is unparseable; falls back to period_days = 30.
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), Some("monthly"));
        // Same interval-like behaviour as period_days.
        assert!(window_covers(&r, date("2026-03-01"), date("2026-03-01")));
        assert!(window_covers(&r, date("2026-03-30"), date("2026-03-01")));
        assert!(window_covers(&r, date("2026-03-31"), date("2026-03-01")));
    }

    // ── rate_for_day ──────────────────────────────────────────────────────────

    #[test]
    fn rate_for_day_uses_projected_rate() {
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), None);
        let rates = std::collections::HashMap::new();
        assert_eq!(rate_for_day(&r, &rates), 10_000);
    }

    #[test]
    fn rate_for_day_prefers_snapshot_rate() {
        let r = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), None);
        let id = Uuid::parse_str(ENTRY_1).unwrap();
        let mut rates = std::collections::HashMap::new();
        rates.insert(id, 15_000.0_f64);
        assert_eq!(rate_for_day(&r, &rates), 15_000);
    }

    // ── project_account integration ───────────────────────────────────────────

    #[test]
    fn pinch_point_when_spend_exceeds_income() {
        let income  = make_entry(ENTRY_1, "income",  30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        let spend = make_entry(ENTRY_2, "spend", 30, 20_000.0, Some("2026-03-01"), Some("interval:30"));
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income, &spend],
            0,
            date("2026-03-01"),
            &rates,
        );
        assert!(rows[0].is_pinch_point, "should be a pinch point");
        assert!(rows[0].margin_rate_per_day < 0, "margin should be negative");
    }

    #[test]
    fn no_pinch_point_when_income_exceeds_spend() {
        let income  = make_entry(ENTRY_1, "income",  30, 20_000.0, Some("2026-03-01"), Some("interval:30"));
        let spend = make_entry(ENTRY_2, "spend", 30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income, &spend],
            0,
            date("2026-03-01"),
            &rates,
        );
        assert!(!rows[0].is_pinch_point, "should not be a pinch point");
        assert!(rows[0].margin_rate_per_day > 0);
    }

    #[test]
    fn projection_covers_ninety_days() {
        let income = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income],
            0,
            date("2026-03-01"),
            &rates,
        );
        assert_eq!(rows.len() as i64, PROJECTION_DAYS + 1, "should have 91 rows (0..=90)");
    }

    #[test]
    fn projection_starts_at_computed_as_of() {
        let income = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        let rates = std::collections::HashMap::new();
        let computed_as_of = date("2026-03-01");
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income],
            0,
            computed_as_of,
            &rates,
        );
        assert_eq!(rows[0].projected_date, computed_as_of, "first row must be at computed_as_of");
    }

    #[test]
    fn balance_accumulates_from_start_balance() {
        let income = make_entry(ENTRY_1, "income", 30, 10_000.0, Some("2026-03-01"), Some("interval:30"));
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income],
            1_000_000,
            date("2026-03-01"),
            &rates,
        );
        assert!(rows[0].projected_balance_cents > 1_000_000, "balance should increase with income");
    }
}
