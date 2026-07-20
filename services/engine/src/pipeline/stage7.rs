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
//! | Status           | Epoch state              | project_tentatively | Stage 7    |
//! |------------------|--------------------------|---------------------|------------|
//! | `active`         | open epoch               | —                   | Include    |
//! | `active`         | terminated or no epoch   | —                   | Exclude    |
//! | `pending_review` | no epoch                 | `TRUE`              | Include    |
//! | `pending_review` | no epoch                 | `FALSE`             | Exclude    |
//! | `inactive`       | any                      | —                   | Exclude    |
//!
//! ## Algorithm
//!
//! For each day D in [computed_as_of .. computed_as_of + 90]:
//! 1. Sum income entries whose schedule window covers D.
//! 2. Sum commitment entries whose schedule window covers D.
//! 3. Compute margin_rate = income - commitments.
//! 4. Accumulate balance.
//! 5. Mark pinch points where margin_rate < 0.
//!
//! Determinism: uses `computed_as_of` as "today" — never `NOW()`.

use anyhow::{Context, Result};
use chrono::NaiveDate;
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
    id:               Uuid,
    account_id:       Option<Uuid>,
    direction:        String,
    period_days:      i32,
    amount_cents:     i64,
    recurrence_anchor: Option<String>,
    next_due_date:    Option<NaiveDate>,
}

#[derive(Debug)]
struct ProjectionRow {
    entity_id:               Uuid,
    account_id:              Option<Uuid>,
    projected_date:          NaiveDate,
    income_rate_per_day:     i64,
    commitment_rate_per_day: i64,
    margin_rate_per_day:     i64,
    projected_balance_cents: i64,
    is_pinch_point:          bool,
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
            .filter(|e| e.direction == "income" && window_covers(e, day, computed_as_of, snapshot_rates))
            .map(|e| rate_for_day(e, snapshot_rates))
            .sum();

        let commitment_rate: i64 = entries
            .iter()
            .filter(|e| e.direction == "expense" && window_covers(e, day, computed_as_of, snapshot_rates))
            .map(|e| rate_for_day(e, snapshot_rates))
            .sum();

        let margin_rate = income_rate - commitment_rate;
        balance += margin_rate;

        rows.push(ProjectionRow {
            entity_id,
            account_id,
            projected_date:          day,
            income_rate_per_day:     income_rate,
            commitment_rate_per_day: commitment_rate,
            margin_rate_per_day:     margin_rate,
            projected_balance_cents: balance,
            is_pinch_point:          margin_rate < 0,
        });
    }

    rows
}

/// Determine whether an entry's schedule window covers day D.
///
/// An entry's window covers day D when D falls within `[fire_date, fire_date + period_days)`.
/// `next_due_date` is the phase anchor; `recurrence_anchor` determines periodicity.
fn window_covers(
    entry: &EligibleEntry,
    day: NaiveDate,
    _computed_as_of: NaiveDate,
    _snapshot_rates: &std::collections::HashMap<Uuid, f64>,
) -> bool {
    // Entries without scheduling data don't contribute to the projection.
    let Some(next_due) = entry.next_due_date else {
        return false;
    };

    let period = chrono::Duration::days(i64::from(entry.period_days));

    // Walk forward from next_due_date to find the fire date covering `day`.
    // We need to check if `day` falls in [fire, fire + period) for any fire date.
    // Efficient: find the fire date closest to `day`.
    if entry.recurrence_anchor.is_some() {
        // Recurring entry: compute fire_date = next_due + N * period_days such that
        // fire_date <= day < fire_date + period.
        let days_since_due = (day - next_due).num_days();
        if days_since_due < 0 {
            // `day` is before the first fire date — not yet scheduled.
            return false;
        }
        let period_days = i64::from(entry.period_days);
        if period_days == 0 {
            return false;
        }
        let cycle = days_since_due / period_days;
        let fire_date = next_due + chrono::Duration::days(cycle * period_days);
        day >= fire_date && day < fire_date + period
    } else {
        // Non-recurring (hit/boost): one window starting at next_due.
        day >= next_due && day < next_due + period
    }
}

/// Compute the daily rate for an entry (in cents).
///
/// For variable entries, uses the actual_rate from the most recent snapshot.
/// For all others, uses `amount_cents / period_days`.
fn rate_for_day(
    entry: &EligibleEntry,
    snapshot_rates: &std::collections::HashMap<Uuid, f64>,
) -> i64 {
    if entry.period_days == 0 {
        return 0;
    }
    // Check if there's a snapshot-derived rate (variable entries).
    if let Some(&rate) = snapshot_rates.get(&entry.id) {
        return rate.abs() as i64;
    }
    (entry.amount_cents.abs() / i64::from(entry.period_days)).max(0)
}

// ---------------------------------------------------------------------------
// DB loaders
// ---------------------------------------------------------------------------

async fn load_eligible_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<EligibleEntry>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:                Uuid,
        direction:         String,
        period_days:       i32,
        recurrence_anchor: Option<String>,
        next_due_date:     Option<NaiveDate>,
        account_id:        Option<Uuid>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT e.id, e.direction, e.period_days, e.recurrence_anchor, e.next_due_date,
               a.id AS account_id
        FROM entries e
        LEFT JOIN accounts a ON a.entity_id = e.entity_id AND a.status = 'active'
        WHERE e.entity_id = $1
          AND (
            (e.status = 'active' AND e.end_date IS NULL)
            OR
            (e.status = 'pending_review' AND e.project_tentatively = TRUE)
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
            amount_cents:      0,
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
    let mut commit_rates: Vec<i64>          = Vec::with_capacity(n);
    let mut margin_rates: Vec<i64>          = Vec::with_capacity(n);
    let mut balances:     Vec<i64>          = Vec::with_capacity(n);
    let mut pinch_points: Vec<bool>         = Vec::with_capacity(n);

    for row in rows {
        account_ids.push(row.account_id);
        proj_dates.push(row.projected_date);
        income_rates.push(row.income_rate_per_day);
        commit_rates.push(row.commitment_rate_per_day);
        margin_rates.push(row.margin_rate_per_day);
        balances.push(row.projected_balance_cents);
        pinch_points.push(row.is_pinch_point);
    }

    sqlx::query(
        r#"
        INSERT INTO projections
          (entity_id, account_id, job_id, projected_date,
           income_rate_per_day, commitment_rate_per_day, margin_rate_per_day,
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
    .bind(&commit_rates)
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

    fn make_entry(
        id: &str,
        direction: &str,
        period_days: i32,
        amount_cents: i64,
        next_due: Option<&str>,
        recurrence: bool,
    ) -> EligibleEntry {
        EligibleEntry {
            id:                Uuid::parse_str(id).unwrap_or(Uuid::nil()),
            account_id:        None,
            direction:         direction.to_string(),
            period_days,
            amount_cents,
            recurrence_anchor: if recurrence { Some("monthly".to_string()) } else { None },
            next_due_date:     next_due.map(|s| date(s)),
        }
    }

    const ENTRY_1: &str = "00000000-0000-0000-0000-000000000001";
    const ENTRY_2: &str = "00000000-0000-0000-0000-000000000002";

    // Spec §11: is_pinch_point = margin < 0
    #[test]
    fn pinch_point_when_commitments_exceed_income() {
        let income = make_entry(ENTRY_1, "income", 30, 300_000, Some("2026-03-01"), true);   // $100/day
        let expense = make_entry(ENTRY_2, "expense", 30, 600_000, Some("2026-03-01"), true); // $200/day
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income, &expense],
            0,
            date("2026-03-01"),
            &rates,
        );
        // Income < commitments → all days are pinch points.
        assert!(rows[0].is_pinch_point, "should be a pinch point");
        assert!(rows[0].margin_rate_per_day < 0, "margin should be negative");
    }

    #[test]
    fn no_pinch_point_when_income_exceeds_commitments() {
        let income = make_entry(ENTRY_1, "income", 30, 600_000, Some("2026-03-01"), true);   // $200/day
        let expense = make_entry(ENTRY_2, "expense", 30, 300_000, Some("2026-03-01"), true); // $100/day
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income, &expense],
            0,
            date("2026-03-01"),
            &rates,
        );
        assert!(!rows[0].is_pinch_point, "should not be a pinch point");
        assert!(rows[0].margin_rate_per_day > 0);
    }

    // Spec §11: projection horizon is 90 days
    #[test]
    fn projection_covers_ninety_days() {
        let income = make_entry(ENTRY_1, "income", 30, 300_000, Some("2026-03-01"), true);
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

    // Spec §11: uses computed_as_of — never NOW()
    #[test]
    fn projection_starts_at_computed_as_of() {
        let income = make_entry(ENTRY_1, "income", 30, 300_000, Some("2026-03-01"), true);
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

    // window_covers: entry with no next_due_date never fires
    #[test]
    fn window_covers_no_next_due_returns_false() {
        let r = make_entry(ENTRY_1, "income", 30, 300_000, None, true);
        let rates = std::collections::HashMap::new();
        assert!(!window_covers(&r, date("2026-03-15"), date("2026-03-01"), &rates));
    }

    // Non-recurring entry fires once in its window.
    #[test]
    fn window_covers_non_recurring() {
        let r = make_entry(ENTRY_1, "income", 10, 100_000, Some("2026-03-05"), false);
        let rates = std::collections::HashMap::new();
        // Day 5–14 should be covered.
        assert!(window_covers(&r, date("2026-03-05"), date("2026-03-01"), &rates));
        assert!(window_covers(&r, date("2026-03-14"), date("2026-03-01"), &rates));
        // Day 15 is outside.
        assert!(!window_covers(&r, date("2026-03-15"), date("2026-03-01"), &rates));
        // Day 4 is before fire date.
        assert!(!window_covers(&r, date("2026-03-04"), date("2026-03-01"), &rates));
    }

    // Recurring entry fires in multiple cycles.
    #[test]
    fn window_covers_recurring_multiple_cycles() {
        // Monthly entry: fires on day 1 of each month window.
        let r = make_entry(ENTRY_1, "income", 30, 300_000, Some("2026-03-01"), true);
        let rates = std::collections::HashMap::new();
        // Cycle 0: March 1–30
        assert!(window_covers(&r, date("2026-03-01"), date("2026-03-01"), &rates));
        assert!(window_covers(&r, date("2026-03-30"), date("2026-03-01"), &rates));
        // Cycle 1: March 31 – April 29
        assert!(window_covers(&r, date("2026-03-31"), date("2026-03-01"), &rates));
        assert!(window_covers(&r, date("2026-04-29"), date("2026-03-01"), &rates));
    }

    // Balance accumulates correctly.
    #[test]
    fn balance_accumulates_from_start_balance() {
        let income = make_entry(ENTRY_1, "income", 30, 300_000, Some("2026-03-01"), true); // 10000/day
        let rates = std::collections::HashMap::new();
        let rows = project_account(
            Uuid::nil(),
            None,
            &[&income],
            1_000_000,
            date("2026-03-01"),
            &rates,
        );
        // Each day adds income rate cents.
        assert!(rows[0].projected_balance_cents > 1_000_000, "balance should increase with income");
    }
}
