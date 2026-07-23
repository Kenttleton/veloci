//! Stage 0: CSV normalization and volatility-aware deduplication.
//!
//! **Input:** `pending_imports` record containing `csv_bytes`, date range, and
//! institution mapping configuration.
//!
//! **Output:** New rows in `raw_transactions`; updated `import_batches` record.
//!
//! ## Algorithm summary
//!
//! 1. Load institution mapping to find column positions and sign convention.
//! 2. Parse CSV rows into candidate transactions (normalize merchant name,
//!    parse date + amount).
//! 3. For each candidate, run five dedup passes in order:
//!    - Pass 1: Exact `imported_id` match (only when bank provides IDs)
//!    - Pass 2: New-territory check (date beyond all prior data)
//!    - Pass 3: Volatility-aware exact merchant match
//!    - Pass 4: Volatility-aware fuzzy LCS merchant match
//!    - Pass 5: Fallback insert
//! 4. Batch INSERT all accepted candidates in a single sequential write.
//!
//! Dedup lookups are issued concurrently via
//! `futures::StreamExt::buffer_unordered(import_concurrency)`.
//! All Stage 0 DB access uses the write pool.

use anyhow::{anyhow, bail, Context, Result};
use chrono::NaiveDate;
use futures::StreamExt;
use sqlx::PgPool;
use uuid::Uuid;

use crate::pipeline::types::Stage0Output;

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Run Stage 0 for an `import.process` job.
pub async fn run(
    entity_id: Uuid,
    job_id: Uuid,
    pending_import_id: Uuid,
    pools: &crate::db::Pools,
) -> Result<Stage0Output> {
    let pool = &pools.write;

    // Load the pending import record.
    let pending = load_pending_import(pending_import_id, entity_id, pool).await?;

    // Load institution mapping.
    let mapping = load_institution_mapping(pending.institution_id, pool).await?;

    // Compute the existing import boundary: MAX(date_range_end) of prior batches.
    let existing_boundary = query_existing_boundary(entity_id, pending.account_id, pool).await?;

    // Parse and normalize CSV bytes.
    let candidates = parse_csv(&pending.csv_bytes, &mapping)?;

    tracing::debug!(
        %entity_id,
        candidate_count = candidates.len(),
        ?existing_boundary,
        "stage 0: parsed CSV candidates"
    );

    let import_concurrency = 4usize; // TODO: thread through from config

    // Classify each candidate via the five dedup passes.
    let classified: Vec<ClassifiedCandidate> = classify_candidates(
        candidates,
        entity_id,
        pending.account_id,
        &mapping,
        existing_boundary,
        import_concurrency,
        pool,
    )
    .await?;

    let imported: Vec<_> = classified
        .iter()
        .filter(|c| matches!(c.action, DedupAction::Insert | DedupAction::Supersede(_)))
        .collect();
    let skipped_count = classified
        .iter()
        .filter(|c| matches!(c.action, DedupAction::Skip))
        .count() as u32;
    let imported_count = imported.len() as u32;

    // Record the import batch first — raw_transactions has a NOT NULL FK to it.
    let batch_id = Uuid::new_v4();
    sqlx::query(
        r#"
        INSERT INTO import_batches
          (id, pending_import_id, entity_id, account_id, processed_at,
           date_range_start, date_range_end,
           transactions_imported, transactions_skipped_duplicate)
        VALUES ($1, $2, $3, $4, NOW(), $5, $6, $7, $8)
        "#,
    )
    .bind(batch_id)
    .bind(pending_import_id)
    .bind(entity_id)
    .bind(pending.account_id)
    .bind(pending.date_range_start)
    .bind(pending.date_range_end)
    .bind(imported_count as i32)
    .bind(skipped_count as i32)
    .execute(pool)
    .await
    .context("failed to record import batch")?;

    // Batch INSERT accepted candidates (sequential — no partial writes).
    batch_insert(
        entity_id,
        pending.account_id,
        batch_id,
        &imported,
        pool,
    )
    .await?;

    // Compute computed_as_of = MAX(raw_transactions.date) for this entity.
    let computed_as_of = query_computed_as_of(entity_id, pool).await?;

    let _ = job_id; // job_id available for audit if needed

    Ok(Stage0Output {
        computed_as_of,
        imported_count,
        skipped_count,
    })
}

/// Query `MAX(transactions.date)` for an entity.
///
/// Returns an error when no transactions exist yet (caller handles the
/// first-import case at the pipeline level).
pub async fn query_computed_as_of(entity_id: Uuid, pool: &PgPool) -> Result<NaiveDate> {
    let row: (Option<NaiveDate>,) =
        sqlx::query_as("SELECT MAX(date) FROM transactions WHERE entity_id = $1")
            .bind(entity_id)
            .fetch_one(pool)
            .await
            .context("failed to query MAX(transactions.date)")?;

    row.0.ok_or_else(|| anyhow!("no transactions found for entity {entity_id}"))
}

// ---------------------------------------------------------------------------
// Merchant normalization (pure function — unit testable)
// ---------------------------------------------------------------------------

/// Normalize a raw bank payee string.
///
/// Steps:
/// 1. Replace separator punctuation (`.`, `*`, `/`) with a space so they
///    tokenize correctly ("AMAZON.COM" → "AMAZON COM", not "AMAZONCOM").
/// 2. Strip leading and trailing whitespace, then collapse internal runs.
/// 3. Strip remaining punctuation except hyphens `-` and ampersands `&`.
/// 4. Title-case the result.
///
/// The raw string is preserved as `imported_payee`; this function returns
/// the `merchant_normalized` value.
///
/// # Examples
///
/// ```
/// use veloci_engine::pipeline::stage0::normalize_merchant;
/// // Dots become word separators
/// assert_eq!(normalize_merchant("NETFLIX.COM"), "Netflix Com");
/// // Stars become word separators
/// assert_eq!(normalize_merchant("AMAZON.COM*MK7AMZN.COM"), "Amazon Com Mk7amzn Com");
/// // Ampersands kept
/// assert_eq!(normalize_merchant("JOHNSON & JOHNSON"), "Johnson & Johnson");
/// // Hyphens kept
/// assert_eq!(normalize_merchant("WALK-IN CLINIC"), "Walk-In Clinic");
/// // Internal whitespace collapsed
/// assert_eq!(normalize_merchant("AMAZON   PRIME"), "Amazon Prime");
/// ```
pub fn normalize_merchant(raw: &str) -> String {
    // Replace separator punctuation with spaces before stripping.
    let with_spaces: String = raw.chars().map(|c| match c {
        '.' | '*' | '/' => ' ',
        other => other,
    }).collect();

    // Strip leading/trailing whitespace, then collapse internal runs.
    let collapsed: String = with_spaces
        .trim()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");

    // Strip punctuation except hyphens and ampersands.
    let stripped: String = collapsed
        .chars()
        .filter(|c| c.is_alphanumeric() || c.is_whitespace() || *c == '-' || *c == '&')
        .collect();

    // Title-case: capitalize first letter of each space-separated and
    // hyphen-separated word segment.
    stripped
        .split(' ')
        .map(|word| {
            // Handle hyphen-separated sub-words (e.g. "WALK-IN" → "Walk-In").
            word.split('-')
                .map(|segment| {
                    let mut chars = segment.chars();
                    match chars.next() {
                        None    => String::new(),
                        Some(f) => {
                            let upper: String = f.to_uppercase().collect();
                            upper + &chars.as_str().to_lowercase()
                        }
                    }
                })
                .collect::<Vec<_>>()
                .join("-")
        })
        .collect::<Vec<_>>()
        .join(" ")
}

// ---------------------------------------------------------------------------
// LCS deduplication helpers (pure — no I/O)
// ---------------------------------------------------------------------------

/// Compute the length of the Longest Common Subsequence of two strings.
///
/// Uses the standard O(mn) DP algorithm. Characters are compared by Unicode
/// scalar value (byte-level for ASCII — adequate for normalized merchant names).
pub fn lcs_length(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let (m, n) = (a.len(), b.len());
    if m == 0 || n == 0 {
        return 0;
    }

    // Space-optimized two-row DP.
    let mut prev = vec![0usize; n + 1];
    let mut curr = vec![0usize; n + 1];

    for i in 1..=m {
        for j in 1..=n {
            curr[j] = if a[i - 1] == b[j - 1] {
                prev[j - 1] + 1
            } else {
                prev[j].max(curr[j - 1])
            };
        }
        std::mem::swap(&mut prev, &mut curr);
        curr.fill(0);
    }

    prev[n]
}

/// Compute the LCS ratio between two strings.
///
/// `lcs_ratio(a, b) = lcs_length(a, b) / max(len(a), len(b))`
///
/// Returns 0.0 when both strings are empty. The threshold used in Stage 0
/// Pass 4 and Stage 2 clustering is 0.70.
///
/// # Examples
///
/// ```
/// use veloci_engine::pipeline::stage0::lcs_ratio;
/// assert!((lcs_ratio("NETFLIX", "NETFLIX") - 1.0).abs() < 1e-6);
/// assert!(lcs_ratio("NETFLIX", "HULU") < 0.5);
/// assert!(lcs_ratio("", "") < 1e-6);
/// ```
pub fn lcs_ratio(a: &str, b: &str) -> f64 {
    let max_len = a.chars().count().max(b.chars().count());
    if max_len == 0 {
        return 0.0;
    }
    lcs_length(a, b) as f64 / max_len as f64
}

// ---------------------------------------------------------------------------
// Amount parsing
// ---------------------------------------------------------------------------

/// Parse a decimal amount string into integer cents.
///
/// Handles formats like `"12.50"`, `"1,234.56"`, `"-0.99"`.
/// Returns an error on unparseable input.
///
/// # Examples
///
/// ```
/// use veloci_engine::pipeline::stage0::parse_amount_cents;
/// assert_eq!(parse_amount_cents("12.50").unwrap(), 1250);
/// assert_eq!(parse_amount_cents("1,234.56").unwrap(), 123456);
/// assert_eq!(parse_amount_cents("-9.99").unwrap(), -999);
/// ```
pub fn parse_amount_cents(s: &str) -> Result<i64> {
    // Remove commas and whitespace.
    let cleaned: String = s.chars().filter(|c| !c.is_whitespace() && *c != ',').collect();

    // Split on decimal point.
    let (integer_part, fractional_part) = match cleaned.split_once('.') {
        Some((i, f)) => (i, f),
        None         => (cleaned.as_str(), ""),
    };

    let negative = integer_part.starts_with('-');
    let abs_int: i64 = integer_part
        .trim_start_matches('-')
        .parse::<i64>()
        .with_context(|| format!("invalid integer part in amount: {s}"))?;

    // Fractional part: pad or truncate to 2 digits.
    let frac_str = match fractional_part.len() {
        0 => "00".to_string(),
        1 => format!("{}0", fractional_part),
        _ => fractional_part[..2].to_string(),
    };
    let frac: i64 = frac_str
        .parse()
        .with_context(|| format!("invalid fractional part in amount: {s}"))?;

    let cents = abs_int * 100 + frac;
    Ok(if negative { -cents } else { cents })
}

/// Apply the institution's sign convention to a raw parsed amount.
///
/// - `positive_is_credit`: positive = inflow (our convention), no change needed.
/// - `positive_is_debit`:  positive raw = outflow → negate to get our convention.
pub fn apply_sign_convention(cents: i64, convention: &str) -> i64 {
    match convention {
        "positive_is_credit" => cents,
        "positive_is_debit"  => -cents,
        _                    => cents,
    }
}

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

struct PendingImport {
    account_id:       Uuid,
    institution_id:   Uuid,
    csv_bytes:        Vec<u8>,
    date_range_start: NaiveDate,
    date_range_end:   NaiveDate,
}

struct InstitutionMapping {
    // which parsing strategy to use
    layout:                 String,  // "signed" | "indicator" | "split"
    // common to all layouts
    date_col:               String,
    merchant_col:           String,
    imported_id_col:        Option<String>,
    // layout = "signed"
    amount_col:             Option<String>,
    sign_convention:        Option<String>, // "positive_is_credit" | "positive_is_debit"
    // layout = "indicator"
    dc_indicator_col:       Option<String>,
    // layout = "split"
    debit_col:              Option<String>,
    credit_col:             Option<String>,
    // operational params (separate DB columns)
    settlement_window_days: i32,
    dedup_window_days:      i32,
    amount_tolerance_pct:   f64,
}

/// A candidate transaction parsed from the CSV.
#[derive(Debug, Clone)]
struct Candidate {
    date:                NaiveDate,
    amount_cents:        i64,
    imported_payee:      String,
    merchant_normalized: String,
    imported_id:         Option<String>,
}

#[derive(Debug)]
enum DedupAction {
    Insert,
    Supersede(Uuid),
    Skip,
}

#[derive(Debug)]
struct ClassifiedCandidate {
    candidate:         Candidate,
    action:            DedupAction,
    settlement_status: &'static str,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// DB loaders (using runtime-checked query instead of macros)
// ---------------------------------------------------------------------------

async fn load_pending_import(id: Uuid, entity_id: Uuid, pool: &PgPool) -> Result<PendingImport> {
    let row: (Uuid, Option<Uuid>, Vec<u8>, NaiveDate, NaiveDate) =
        sqlx::query_as(
            r#"
            SELECT account_id, institution_id, csv_bytes,
                   date_range_start, date_range_end
            FROM pending_imports
            WHERE id = $1 AND entity_id = $2
            "#,
        )
        .bind(id)
        .bind(entity_id)
        .fetch_one(pool)
        .await
        .context("pending_import not found")?;

    Ok(PendingImport {
        account_id:       row.0,
        institution_id:   row.1.ok_or_else(|| anyhow!("pending_import.institution_id is null"))?,
        csv_bytes:        row.2,
        date_range_start: row.3,
        date_range_end:   row.4,
    })
}

async fn load_institution_mapping(id: Uuid, pool: &PgPool) -> Result<InstitutionMapping> {
    let row: (Option<serde_json::Value>, Option<i32>, Option<i32>, Option<f64>) =
        sqlx::query_as(
            r#"
            SELECT mapping_config,
                   settlement_window_days,
                   dedup_window_days,
                   amount_tolerance_pct
            FROM institution_mappings
            WHERE id = $1
            "#,
        )
        .bind(id)
        .fetch_one(pool)
        .await
        .context("institution_mapping not found")?;

    let (cfg_opt, settlement, dedup, tolerance) = row;
    let cfg = cfg_opt.unwrap_or(serde_json::Value::Null);

    if cfg.is_null() {
        return Err(anyhow!(
            "Mapping error: this institution has no column mapping configured. \
             Upload a CSV and use the mapping dialog to set it up first."
        ));
    }

    let layout = cfg["layout"].as_str().unwrap_or("signed").to_string();

    let get = |key: &str| -> Option<String> {
        cfg["fields"][key]
            .as_str()
            .filter(|s| !s.is_empty())
            .map(str::to_string)
    };

    let date_col = get("date")
        .ok_or_else(|| anyhow!("Mapping error: required field 'date' not configured in institution mapping"))?;
    let merchant_col = get("merchant")
        .ok_or_else(|| anyhow!("Mapping error: required field 'merchant' not configured in institution mapping"))?;

    Ok(InstitutionMapping {
        layout,
        date_col,
        merchant_col,
        imported_id_col:        get("imported_id"),
        amount_col:             get("amount"),
        sign_convention:        get("sign_convention"),
        dc_indicator_col:       get("dc_indicator"),
        debit_col:              get("debit"),
        credit_col:             get("credit"),
        settlement_window_days: settlement.unwrap_or(14),
        dedup_window_days:      dedup.unwrap_or(3),
        amount_tolerance_pct:   tolerance.unwrap_or(0.005),
    })
}

async fn query_existing_boundary(
    entity_id: Uuid,
    account_id: Uuid,
    pool: &PgPool,
) -> Result<Option<NaiveDate>> {
    let row: (Option<NaiveDate>,) = sqlx::query_as(
        "SELECT MAX(date_range_end) FROM import_batches WHERE entity_id = $1 AND account_id = $2",
    )
    .bind(entity_id)
    .bind(account_id)
    .fetch_one(pool)
    .await
    .context("failed to query existing import boundary")?;

    Ok(row.0)
}

// ---------------------------------------------------------------------------
// CSV parsing
// ---------------------------------------------------------------------------

fn parse_csv(bytes: &[u8], mapping: &InstitutionMapping) -> Result<Vec<Candidate>> {
    match mapping.layout.as_str() {
        "indicator" => parse_csv_indicator(bytes, mapping),
        "split"     => parse_csv_split(bytes, mapping),
        _           => parse_csv_signed(bytes, mapping), // "signed" is the default
    }
}

fn col_idx(headers: &csv::StringRecord, col: &str, cols_list: &str, field_label: &str) -> Result<usize> {
    headers
        .iter()
        .position(|h| h.trim() == col.trim())
        .ok_or_else(|| anyhow!(
            "Mapping error: {} column '{}' not found in your CSV. \
             Columns found: {}. \
             Update the institution mapping to match your CSV headers.",
            field_label, col, cols_list
        ))
}

fn common_idxs(
    headers: &csv::StringRecord,
    mapping: &InstitutionMapping,
    cols_list: &str,
) -> Result<(usize, usize, Option<usize>)> {
    let date_idx     = col_idx(headers, &mapping.date_col, cols_list, "date")?;
    let merchant_idx = col_idx(headers, &mapping.merchant_col, cols_list, "merchant")?;
    let id_idx = mapping
        .imported_id_col
        .as_deref()
        .and_then(|col| headers.iter().position(|h| h.trim() == col.trim()));
    Ok((date_idx, merchant_idx, id_idx))
}

fn parse_csv_signed(bytes: &[u8], mapping: &InstitutionMapping) -> Result<Vec<Candidate>> {
    let mut reader = csv::Reader::from_reader(bytes);
    let headers    = reader.headers().context("failed to read CSV headers")?.clone();
    let cols_list  = headers.iter().collect::<Vec<_>>().join(", ");

    let (date_idx, merchant_idx, id_idx) = common_idxs(&headers, mapping, &cols_list)?;

    let amount_col = mapping.amount_col.as_deref()
        .ok_or_else(|| anyhow!("Mapping error: 'amount' field not configured for signed layout"))?;
    let amount_idx = col_idx(&headers, amount_col, &cols_list, "amount")?;

    let convention = mapping.sign_convention.as_deref().unwrap_or("positive_is_credit");

    let mut candidates = Vec::new();
    for result in reader.records() {
        let record     = result.context("CSV parse error")?;
        let date_str   = record.get(date_idx).unwrap_or("").trim().to_string();
        let amount_str = record.get(amount_idx).unwrap_or("").trim().to_string();
        let payee_raw  = record.get(merchant_idx).unwrap_or("").to_string();

        let date       = parse_date(&date_str).with_context(|| format!("unparseable date '{date_str}'"))?;
        let raw_cents  = parse_amount_cents(&amount_str).with_context(|| format!("unparseable amount '{amount_str}'"))?;
        let amount_cents = apply_sign_convention(raw_cents, convention);
        let imported_id  = id_idx.and_then(|i| record.get(i).filter(|s| !s.is_empty()).map(str::to_string));

        candidates.push(Candidate {
            date,
            amount_cents,
            merchant_normalized: normalize_merchant(&payee_raw),
            imported_payee: payee_raw,
            imported_id,
        });
    }
    Ok(candidates)
}

fn parse_csv_indicator(bytes: &[u8], mapping: &InstitutionMapping) -> Result<Vec<Candidate>> {
    let mut reader = csv::Reader::from_reader(bytes);
    let headers    = reader.headers().context("failed to read CSV headers")?.clone();
    let cols_list  = headers.iter().collect::<Vec<_>>().join(", ");

    let (date_idx, merchant_idx, id_idx) = common_idxs(&headers, mapping, &cols_list)?;

    let amount_col = mapping.amount_col.as_deref()
        .ok_or_else(|| anyhow!("Mapping error: 'amount' field not configured for indicator layout"))?;
    let amount_idx = col_idx(&headers, amount_col, &cols_list, "amount")?;

    let dc_col = mapping.dc_indicator_col.as_deref()
        .ok_or_else(|| anyhow!("Mapping error: 'dc_indicator' field not configured for indicator layout"))?;
    let dc_idx = col_idx(&headers, dc_col, &cols_list, "debit/credit indicator")?;

    let mut candidates = Vec::new();
    for result in reader.records() {
        let record     = result.context("CSV parse error")?;
        let date_str   = record.get(date_idx).unwrap_or("").trim().to_string();
        let amount_str = record.get(amount_idx).unwrap_or("").trim().to_string();
        let payee_raw  = record.get(merchant_idx).unwrap_or("").to_string();
        let indicator  = record.get(dc_idx).unwrap_or("").trim().to_uppercase();

        let date      = parse_date(&date_str).with_context(|| format!("unparseable date '{date_str}'"))?;
        let abs_cents = parse_amount_cents(&amount_str).with_context(|| format!("unparseable amount '{amount_str}'"))?.abs();

        // DEBIT / DR / D → money out (negative); CREDIT / CR / C → money in (positive)
        let amount_cents = match indicator.as_str() {
            "DEBIT" | "DR" | "D" | "WITHDRAWAL" => -abs_cents,
            _ => abs_cents,
        };

        let imported_id = id_idx.and_then(|i| record.get(i).filter(|s| !s.is_empty()).map(str::to_string));

        candidates.push(Candidate {
            date,
            amount_cents,
            merchant_normalized: normalize_merchant(&payee_raw),
            imported_payee: payee_raw,
            imported_id,
        });
    }
    Ok(candidates)
}

fn parse_csv_split(bytes: &[u8], mapping: &InstitutionMapping) -> Result<Vec<Candidate>> {
    let mut reader = csv::Reader::from_reader(bytes);
    let headers    = reader.headers().context("failed to read CSV headers")?.clone();
    let cols_list  = headers.iter().collect::<Vec<_>>().join(", ");

    let (date_idx, merchant_idx, id_idx) = common_idxs(&headers, mapping, &cols_list)?;

    let debit_col  = mapping.debit_col.as_deref()
        .ok_or_else(|| anyhow!("Mapping error: 'debit' field not configured for split layout"))?;
    let credit_col = mapping.credit_col.as_deref()
        .ok_or_else(|| anyhow!("Mapping error: 'credit' field not configured for split layout"))?;
    let debit_idx  = col_idx(&headers, debit_col, &cols_list, "debit")?;
    let credit_idx = col_idx(&headers, credit_col, &cols_list, "credit")?;

    let mut candidates = Vec::new();
    for result in reader.records() {
        let record    = result.context("CSV parse error")?;
        let date_str  = record.get(date_idx).unwrap_or("").trim().to_string();
        let payee_raw = record.get(merchant_idx).unwrap_or("").to_string();
        let debit_str = record.get(debit_idx).unwrap_or("").trim().to_string();
        let credit_str = record.get(credit_idx).unwrap_or("").trim().to_string();

        let date = parse_date(&date_str).with_context(|| format!("unparseable date '{date_str}'"))?;

        // whichever column has a value wins; debit → negative, credit → positive
        let amount_cents = if !debit_str.is_empty() && debit_str != "0" && debit_str != "0.00" {
            let c = parse_amount_cents(&debit_str)
                .with_context(|| format!("unparseable debit amount '{debit_str}'"))?;
            -c.abs()
        } else if !credit_str.is_empty() && credit_str != "0" && credit_str != "0.00" {
            let c = parse_amount_cents(&credit_str)
                .with_context(|| format!("unparseable credit amount '{credit_str}'"))?;
            c.abs()
        } else {
            continue; // blank row — skip
        };

        let imported_id = id_idx.and_then(|i| record.get(i).filter(|s| !s.is_empty()).map(str::to_string));

        candidates.push(Candidate {
            date,
            amount_cents,
            merchant_normalized: normalize_merchant(&payee_raw),
            imported_payee: payee_raw,
            imported_id,
        });
    }
    Ok(candidates)
}

/// Parse a date string from common US bank formats.
fn parse_date(s: &str) -> Result<NaiveDate> {
    let formats = ["%Y-%m-%d", "%m/%d/%Y", "%m/%d/%y", "%d-%b-%Y", "%B %d, %Y"];
    for fmt in &formats {
        if let Ok(d) = NaiveDate::parse_from_str(s, fmt) {
            return Ok(d);
        }
    }
    bail!("unrecognized date format: {s}")
}

// ---------------------------------------------------------------------------
// Dedup classification (concurrent per candidate)
// ---------------------------------------------------------------------------

async fn classify_candidates(
    candidates: Vec<Candidate>,
    entity_id: Uuid,
    account_id: Uuid,
    mapping: &InstitutionMapping,
    existing_boundary: Option<NaiveDate>,
    import_concurrency: usize,
    pool: &PgPool,
) -> Result<Vec<ClassifiedCandidate>> {
    let settlement_window = chrono::Duration::days(i64::from(mapping.settlement_window_days));
    let dedup_window      = chrono::Duration::days(i64::from(mapping.dedup_window_days));
    let tolerance_pct     = mapping.amount_tolerance_pct;

    // Anchor settlement to the latest date in the data, not wall-clock upload time.
    // This keeps classification deterministic: re-running with the same CSV always
    // produces the same result regardless of when the import runs.
    // Fallback (empty CSV): use existing boundary or epoch — no candidates means nothing
    // is inserted, so the anchor value doesn't affect any stored row.
    let settlement_anchor = candidates
        .iter()
        .map(|c| c.date)
        .max()
        .or(existing_boundary)
        .unwrap_or(NaiveDate::from_ymd_opt(1970, 1, 1).unwrap());

    let results: Vec<Result<ClassifiedCandidate>> = futures::stream::iter(candidates)
        .map(|candidate| {
            let pool = pool.clone();
            async move {
                classify_one(
                    candidate,
                    entity_id,
                    account_id,
                    existing_boundary,
                    settlement_anchor,
                    settlement_window,
                    dedup_window,
                    tolerance_pct,
                    &pool,
                )
                .await
            }
        })
        .buffer_unordered(import_concurrency)
        .collect()
        .await;

    results.into_iter().collect()
}

#[derive(sqlx::FromRow)]
struct ExistingRowDb {
    id:                  Uuid,
    date:                NaiveDate,
    merchant_normalized: String,
    amount_cents:        i64,
    settlement_status:   String,
}

// Determine whether an existing row can be superseded by a new import.
// Uses the data-derived settlement anchor (MAX candidate date), not wall-clock time.
fn effective_status(
    row: &ExistingRowDb,
    settlement_anchor: NaiveDate,
    settlement_window: chrono::Duration,
) -> &'static str {
    if row.settlement_status == "settled" {
        return "settled";
    }
    let window_days = settlement_window.num_days();
    if row.date < settlement_anchor - chrono::Duration::days(window_days) {
        "aged_flux"
    } else {
        "young_flux"
    }
}

async fn classify_one(
    candidate: Candidate,
    entity_id: Uuid,
    account_id: Uuid,
    existing_boundary: Option<NaiveDate>,
    settlement_anchor: NaiveDate,
    settlement_window: chrono::Duration,
    dedup_window: chrono::Duration,
    tolerance_pct: f64,
    pool: &PgPool,
) -> Result<ClassifiedCandidate> {
    // Settled = this transaction's date is older than the settlement window
    // relative to the latest date in the import data (settlement_anchor).
    let settlement_status: &'static str =
        if candidate.date < (settlement_anchor - settlement_window) {
            "settled"
        } else {
            "flux"
        };

    // -- Pass 1: Exact imported_id match --
    if let Some(ref iid) = candidate.imported_id {
        let existing: Option<ExistingRowDb> = sqlx::query_as(
            r#"
            SELECT id, date, merchant_normalized, amount_cents, settlement_status
            FROM transactions
            WHERE entity_id = $1 AND account_id = $2 AND imported_id = $3
            LIMIT 1
            "#,
        )
        .bind(entity_id)
        .bind(account_id)
        .bind(iid)
        .fetch_optional(pool)
        .await?;

        if let Some(row) = existing {
            let eff = effective_status(&row, settlement_anchor, settlement_window);
            let action = if eff == "young_flux" {
                DedupAction::Supersede(row.id)
            } else {
                DedupAction::Skip
            };
            return Ok(ClassifiedCandidate { candidate, action, settlement_status });
        }
    }

    // -- Pass 2: New territory check --
    if let Some(boundary) = existing_boundary {
        if candidate.date > boundary + dedup_window {
            return Ok(ClassifiedCandidate {
                candidate,
                action: DedupAction::Insert,
                settlement_status,
            });
        }
    } else {
        return Ok(ClassifiedCandidate {
            candidate,
            action: DedupAction::Insert,
            settlement_status,
        });
    }

    let dedup_days = dedup_window.num_days() as i32;
    let amount_tolerance = (candidate.amount_cents.abs() as f64 * tolerance_pct) as i64;

    // -- Pass 3: Volatility-aware exact merchant match --
    let exact_match: Option<ExistingRowDb> = sqlx::query_as(
        r#"
        SELECT id, date, merchant_normalized, amount_cents, settlement_status
        FROM transactions
        WHERE entity_id = $1
          AND account_id = $2
          AND merchant_normalized = $3
          AND date BETWEEN ($4::date - $5 * INTERVAL '1 day')
                       AND ($4::date + $5 * INTERVAL '1 day')
          AND ABS(amount_cents - $6) <= $7
        LIMIT 1
        "#,
    )
    .bind(entity_id)
    .bind(account_id)
    .bind(&candidate.merchant_normalized)
    .bind(candidate.date)
    .bind(dedup_days)
    .bind(candidate.amount_cents)
    .bind(amount_tolerance)
    .fetch_optional(pool)
    .await?;

    if let Some(row) = exact_match {
        let eff = effective_status(&row, settlement_anchor, settlement_window);
        let action = if eff == "young_flux" {
            DedupAction::Supersede(row.id)
        } else {
            DedupAction::Skip
        };
        return Ok(ClassifiedCandidate { candidate, action, settlement_status });
    }

    // -- Pass 4: Volatility-aware fuzzy LCS merchant match --
    let fuzzy_candidates: Vec<ExistingRowDb> = sqlx::query_as(
        r#"
        SELECT id, date, merchant_normalized, amount_cents, settlement_status
        FROM transactions
        WHERE entity_id = $1
          AND account_id = $2
          AND date BETWEEN ($3::date - $4 * INTERVAL '1 day')
                       AND ($3::date + $4 * INTERVAL '1 day')
          AND ABS(amount_cents - $5) <= $6
        "#,
    )
    .bind(entity_id)
    .bind(account_id)
    .bind(candidate.date)
    .bind(dedup_days)
    .bind(candidate.amount_cents)
    .bind(amount_tolerance)
    .fetch_all(pool)
    .await?;

    let fuzzy_match = fuzzy_candidates.into_iter().find(|row| {
        lcs_ratio(&row.merchant_normalized, &candidate.merchant_normalized) >= 0.70
    });

    if let Some(row) = fuzzy_match {
        let eff = effective_status(&row, settlement_anchor, settlement_window);
        let action = if eff == "young_flux" {
            DedupAction::Supersede(row.id)
        } else {
            DedupAction::Skip
        };
        return Ok(ClassifiedCandidate { candidate, action, settlement_status });
    }

    // -- Pass 5: Fallback insert --
    Ok(ClassifiedCandidate {
        candidate,
        action: DedupAction::Insert,
        settlement_status,
    })
}

// ---------------------------------------------------------------------------
// Batch INSERT
// ---------------------------------------------------------------------------

async fn batch_insert(
    entity_id: Uuid,
    account_id: Uuid,
    batch_id: Uuid,
    classified: &[&ClassifiedCandidate],
    pool: &PgPool,
) -> Result<()> {
    if classified.is_empty() {
        return Ok(());
    }

    let mut tx = pool.begin().await.context("failed to begin import transaction")?;

    // Delete superseded rows.
    let supersede_ids: Vec<Uuid> = classified
        .iter()
        .filter_map(|c| {
            if let DedupAction::Supersede(id) = c.action {
                Some(id)
            } else {
                None
            }
        })
        .collect();

    if !supersede_ids.is_empty() {
        sqlx::query("DELETE FROM transactions WHERE id = ANY($1)")
            .bind(&supersede_ids)
            .execute(&mut *tx)
            .await
            .context("failed to delete superseded rows")?;
    }

    let mut dates:     Vec<NaiveDate>       = Vec::with_capacity(classified.len());
    let mut amounts:   Vec<i64>             = Vec::with_capacity(classified.len());
    let mut payees:    Vec<String>          = Vec::with_capacity(classified.len());
    let mut merchants: Vec<String>          = Vec::with_capacity(classified.len());
    let mut imp_ids:   Vec<Option<String>>  = Vec::with_capacity(classified.len());
    let mut statuses:  Vec<String>          = Vec::with_capacity(classified.len());

    for c in classified {
        dates.push(c.candidate.date);
        amounts.push(c.candidate.amount_cents);
        payees.push(c.candidate.imported_payee.clone());
        merchants.push(c.candidate.merchant_normalized.clone());
        imp_ids.push(c.candidate.imported_id.clone());
        statuses.push(c.settlement_status.to_string());
    }

    sqlx::query(
        r#"
        INSERT INTO transactions
          (entity_id, account_id, import_batch_id, date, amount_cents,
           imported_payee, merchant_normalized, imported_id, settlement_status)
        SELECT $1, $2, $9, d, a, p, m, i, s
        FROM UNNEST(
          $3::date[],
          $4::bigint[],
          $5::text[],
          $6::text[],
          $7::text[],
          $8::text[]
        ) AS t(d, a, p, m, i, s)
        "#,
    )
    .bind(entity_id)
    .bind(account_id)
    .bind(&dates)
    .bind(&amounts)
    .bind(&payees)
    .bind(&merchants)
    .bind(&imp_ids as &[Option<String>])
    .bind(&statuses)
    .bind(batch_id)
    .execute(&mut *tx)
    .await
    .context("batch insert into transactions failed")?;

    tx.commit().await.context("failed to commit import transaction")?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // Merchant normalization
    #[test]
    fn normalize_strips_punctuation_keeps_hyphen_ampersand() {
        assert_eq!(normalize_merchant("JOHNSON & JOHNSON"), "Johnson & Johnson");
        assert_eq!(normalize_merchant("WALK-IN CLINIC"), "Walk-In Clinic");
        // Dots and stars are now word separators, not stripped inline.
        assert_eq!(normalize_merchant("NETFLIX.COM"), "Netflix Com");
        assert_eq!(normalize_merchant("AMAZON.COM*MK7AMZN.COM"), "Amazon Com Mk7amzn Com");
    }

    #[test]
    fn normalize_collapses_whitespace() {
        assert_eq!(normalize_merchant("AMAZON   PRIME"), "Amazon Prime");
        assert_eq!(normalize_merchant("  HULU  "), "Hulu");
    }

    #[test]
    fn normalize_title_cases() {
        assert_eq!(normalize_merchant("netflix"), "Netflix");
        assert_eq!(normalize_merchant("SPOTIFY AB"), "Spotify Ab");
    }

    // LCS
    #[test]
    fn lcs_length_identical() {
        assert_eq!(lcs_length("NETFLIX", "NETFLIX"), 7);
    }

    #[test]
    fn lcs_length_empty() {
        assert_eq!(lcs_length("", "NETFLIX"), 0);
        assert_eq!(lcs_length("NETFLIX", ""), 0);
        assert_eq!(lcs_length("", ""), 0);
    }

    #[test]
    fn lcs_ratio_identical_strings_is_one() {
        let r = lcs_ratio("NETFLIX", "NETFLIX");
        assert!((r - 1.0).abs() < 1e-6, "expected 1.0, got {r}");
    }

    #[test]
    fn lcs_ratio_empty_strings() {
        assert!(lcs_ratio("", "").abs() < 1e-6);
    }

    #[test]
    fn lcs_ratio_amazon_variants_above_threshold() {
        let r = lcs_ratio("AMZ PRIME", "AMAZON PRIME");
        assert!(r >= 0.70, "expected >= 0.70, got {r}");
    }

    #[test]
    fn lcs_ratio_unrelated_below_threshold() {
        let r = lcs_ratio("NETFLIX", "STARBUCKS");
        assert!(r < 0.70, "expected < 0.70, got {r}");
    }

    // Amount parsing
    #[test]
    fn parse_amount_whole_cents() {
        assert_eq!(parse_amount_cents("12.50").unwrap(), 1250);
        assert_eq!(parse_amount_cents("0.99").unwrap(), 99);
        assert_eq!(parse_amount_cents("100").unwrap(), 10000);
    }

    #[test]
    fn parse_amount_commas() {
        assert_eq!(parse_amount_cents("1,234.56").unwrap(), 123456);
    }

    #[test]
    fn parse_amount_negative() {
        assert_eq!(parse_amount_cents("-9.99").unwrap(), -999);
        assert_eq!(parse_amount_cents("-0.01").unwrap(), -1);
    }

    #[test]
    fn parse_amount_one_decimal() {
        assert_eq!(parse_amount_cents("12.5").unwrap(), 1250);
    }

    #[test]
    fn apply_sign_positive_is_credit_no_change() {
        assert_eq!(apply_sign_convention(500, "positive_is_credit"), 500);
        assert_eq!(apply_sign_convention(-500, "positive_is_credit"), -500);
    }

    #[test]
    fn apply_sign_positive_is_debit_negates() {
        assert_eq!(apply_sign_convention(500, "positive_is_debit"), -500);
        assert_eq!(apply_sign_convention(-500, "positive_is_debit"), 500);
    }

    // Date parsing
    #[test]
    fn parse_date_iso() {
        let d = parse_date("2026-03-15").unwrap();
        assert_eq!(d.to_string(), "2026-03-15");
    }

    #[test]
    fn parse_date_us_slash() {
        let d = parse_date("03/15/2026").unwrap();
        assert_eq!(d.to_string(), "2026-03-15");
    }

    #[test]
    fn parse_date_invalid_errors() {
        assert!(parse_date("not-a-date").is_err());
        assert!(parse_date("").is_err());
    }

    // Spec §13 point 8: amount_cents sign convention
    #[test]
    fn sign_convention_inflow_is_positive() {
        let raw: i64 = 5000;
        let as_credit = apply_sign_convention(raw, "positive_is_credit");
        assert!(as_credit > 0, "inflow must be positive cents");

        let as_debit = apply_sign_convention(raw, "positive_is_debit");
        assert!(as_debit < 0, "spend must be negative cents");
    }

}
