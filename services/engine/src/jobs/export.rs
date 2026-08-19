//! Export artifact generation. Handles `export.report` jobs — bypasses the
//! pipeline entirely and produces a downloadable artifact stored in `exports`.

use anyhow::{bail, Result};
use chrono::NaiveDate;
use serde::Deserialize;
use uuid::Uuid;

use crate::db::Pools;

// ── Wire types ───────────────────────────────────────────────────────────────

/// Parameters embedded in `export.report` job metadata by the Go web service.
#[derive(Debug, Deserialize)]
pub struct ExportMeta {
    pub export_type: String,
    pub format: String,
    #[serde(default)]
    pub parameters: ExportParameters,
    /// Optional user-supplied filename; engine generates one if absent.
    #[serde(default)]
    pub filename: Option<String>,
}

/// The generation inputs for an export. Stored in the `exports.parameters`
/// column verbatim, enabling reruns against fresher data.
#[derive(Debug, Default, Deserialize, serde::Serialize)]
pub struct ExportParameters {
    pub date_from: Option<NaiveDate>,
    pub date_to: Option<NaiveDate>,
    /// "day" | "month" | "year" — controls CSV column values and headers.
    /// Defaults to "month" when absent.
    #[serde(default)]
    pub granularity: String,
}

// ── Snapshot row fetched from Postgres ───────────────────────────────────────

#[derive(Debug, sqlx::FromRow)]
struct DaySummaryRow {
    snapshot_date: NaiveDate,
    income_rate: f64,
    spend_rate: f64,
    margin_rate: f64,
    drift_rate: f64,
    computed_as_of: NaiveDate,
}

// ── Entry point ──────────────────────────────────────────────────────────────

/// Generate the export artifact and store it in the `exports` table.
pub async fn run(
    entity_id: Uuid,
    job_id: Uuid,
    meta: ExportMeta,
    pools: &Pools,
) -> Result<()> {
    match meta.export_type.as_str() {
        "report" => generate_report(entity_id, job_id, meta, pools).await,
        other => bail!("unknown export_type: {other}"),
    }
}

// ── Report export ─────────────────────────────────────────────────────────────

async fn generate_report(
    entity_id: Uuid,
    job_id: Uuid,
    meta: ExportMeta,
    pools: &Pools,
) -> Result<()> {
    if meta.format != "csv" {
        bail!("unsupported format for report export: {}", meta.format);
    }

    let rows = fetch_report_rows(entity_id, &meta.parameters, &pools.read).await?;
    if rows.is_empty() {
        bail!("no snapshot data found for entity {entity_id}");
    }

    // computed_as_of is the anchor date from the data — used in the filename.
    let computed_as_of = rows
        .iter()
        .map(|r| r.computed_as_of)
        .max()
        .unwrap_or(rows[0].snapshot_date);

    let filename = meta.filename.unwrap_or_else(|| {
        build_filename("report", &meta.parameters, computed_as_of, "csv")
    });

    let (data, size_bytes) = build_csv(&rows, &meta.parameters.granularity)?;
    let params_json = serde_json::to_value(&meta.parameters)?;

    sqlx::query(
        r#"
        INSERT INTO exports (
            entity_id, job_id, export_type, format, parameters,
            storage_type, data, size_bytes, filename
        ) VALUES ($1, $2, 'report', 'csv', $3, 'inline', $4, $5, $6)
        "#,
    )
    .bind(entity_id)
    .bind(job_id)
    .bind(params_json)
    .bind(&data)
    .bind(size_bytes)
    .bind(&filename)
    .execute(&pools.write)
    .await?;

    Ok(())
}

// ── Data query ────────────────────────────────────────────────────────────────

async fn fetch_report_rows(
    entity_id: Uuid,
    params: &ExportParameters,
    pool: &sqlx::PgPool,
) -> Result<Vec<DaySummaryRow>> {
    // Build the query dynamically based on optional date filters.
    // computed_as_of is MAX per snapshot_date to anchor the filename.
    let mut q = String::from(
        r#"
        SELECT
            s.snapshot_date,
            COALESCE(SUM(CASE WHEN e.direction = 'income' THEN s.actual_rate_per_day ELSE 0 END), 0)::float8                    AS income_rate,
            COALESCE(SUM(CASE WHEN e.direction = 'spend'  THEN s.actual_rate_per_day ELSE 0 END), 0)::float8                    AS spend_rate,
            COALESCE(SUM(CASE WHEN e.direction = 'income' THEN s.actual_rate_per_day ELSE -s.actual_rate_per_day END), 0)::float8 AS margin_rate,
            COALESCE(SUM(s.drift_per_day), 0)::float8                                                                            AS drift_rate,
            COALESCE(MAX(s.computed_as_of)::date, s.snapshot_date)                                                       AS computed_as_of
        FROM snapshots s
        JOIN entries e ON e.id = s.node_id AND s.node_type = 'entry'
        WHERE s.entity_id = $1
        "#,
    );

    let mut bind_idx = 2usize;

    if params.date_from.is_some() {
        q.push_str(&format!("  AND s.snapshot_date >= ${bind_idx}\n"));
        bind_idx += 1;
    }
    if params.date_to.is_some() {
        q.push_str(&format!("  AND s.snapshot_date <= ${bind_idx}\n"));
        bind_idx += 1;
    }
    let _ = bind_idx;

    q.push_str("GROUP BY s.snapshot_date\nORDER BY s.snapshot_date DESC");

    let mut query = sqlx::query_as::<_, DaySummaryRow>(&q).bind(entity_id);
    if let Some(df) = params.date_from {
        query = query.bind(df);
    }
    if let Some(dt) = params.date_to {
        query = query.bind(dt);
    }

    let rows = query.fetch_all(pool).await?;
    Ok(rows)
}

// ── CSV generation ────────────────────────────────────────────────────────────

fn gran_suffix(gran: &str) -> &'static str {
    match gran {
        "day"  => "/day",
        "year" => "/yr",
        _      => "/mo",
    }
}

/// Convert a cents-per-day rate to the requested granularity, in dollars.
fn to_rate(rate_per_day: f64, gran: &str) -> f64 {
    match gran {
        "day"  => rate_per_day / 100.0,
        "year" => rate_per_day / 100.0 * 365.0,
        _      => rate_per_day / 100.0 * 30.44,
    }
}

fn build_csv(rows: &[DaySummaryRow], gran: &str) -> Result<(Vec<u8>, i64)> {
    let sfx = gran_suffix(gran);
    let mut buf = Vec::new();
    {
        let mut w = csv::Writer::from_writer(&mut buf);
        w.write_record([
            "Date",
            &format!("Income{sfx}"),
            &format!("Spend{sfx}"),
            &format!("Margin{sfx}"),
            &format!("Drift{sfx}"),
        ])?;
        for row in rows {
            w.write_record([
                row.snapshot_date.to_string(),
                format!("{:.2}", to_rate(row.income_rate, gran)),
                format!("{:.2}", to_rate(row.spend_rate, gran)),
                format!("{:.2}", to_rate(row.margin_rate, gran)),
                format!("{:.2}", to_rate(row.drift_rate, gran)),
            ])?;
        }
        w.flush()?;
    }
    let size = buf.len() as i64;
    Ok((buf, size))
}

// ── Filename generation ────────────────────────────────────────────────────────

fn build_filename(
    export_type: &str,
    params: &ExportParameters,
    computed_as_of: NaiveDate,
    ext: &str,
) -> String {
    match (params.date_from, params.date_to) {
        (Some(df), Some(dt)) => format!(
            "veloci-{export_type}-{df}--{dt}-as-of-{computed_as_of}.{ext}"
        ),
        (Some(df), None) => format!(
            "veloci-{export_type}-from-{df}-as-of-{computed_as_of}.{ext}"
        ),
        (None, Some(dt)) => format!(
            "veloci-{export_type}-to-{dt}-as-of-{computed_as_of}.{ext}"
        ),
        (None, None) => format!(
            "veloci-{export_type}-as-of-{computed_as_of}.{ext}"
        ),
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filename_with_range() {
        let df = NaiveDate::from_ymd_opt(2025, 1, 1).unwrap();
        let dt = NaiveDate::from_ymd_opt(2025, 12, 31).unwrap();
        let as_of = NaiveDate::from_ymd_opt(2025, 12, 15).unwrap();
        let params = ExportParameters { date_from: Some(df), date_to: Some(dt) };
        assert_eq!(
            build_filename("report", &params, as_of, "csv"),
            "veloci-report-2025-01-01--2025-12-31-as-of-2025-12-15.csv"
        );
    }

    #[test]
    fn filename_no_range() {
        let as_of = NaiveDate::from_ymd_opt(2025, 12, 15).unwrap();
        let params = ExportParameters { date_from: None, date_to: None };
        assert_eq!(
            build_filename("report", &params, as_of, "csv"),
            "veloci-report-as-of-2025-12-15.csv"
        );
    }

    #[test]
    fn csv_output_has_header_and_rows() {
        let rows = vec![DaySummaryRow {
            snapshot_date: NaiveDate::from_ymd_opt(2025, 6, 1).unwrap(),
            income_rate: 1000.0,
            spend_rate: 600.0,
            margin_rate: 400.0,
            drift_rate: -10.0,
            computed_as_of: NaiveDate::from_ymd_opt(2025, 6, 15).unwrap(),
        }];
        let (data, size) = build_csv(&rows).unwrap();
        let text = String::from_utf8(data).unwrap();
        assert!(text.starts_with("Date,Income/mo"));
        assert!(text.contains("2025-06-01"));
        assert_eq!(size as usize, text.len());
    }
}
