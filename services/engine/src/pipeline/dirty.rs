use std::collections::{HashMap, HashSet};

use anyhow::{Context, Result};
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub struct DirtyEntry {
    pub id:         Uuid,
    pub start_date: NaiveDate,
    pub end_date:   Option<NaiveDate>,
}

pub struct DirtyDetectionInput {
    pub superseded_entry_ids:  Vec<Uuid>,
    pub new_entry_assignments: Vec<(Uuid, NaiveDate)>,
}

pub struct DirtyContext {
    pub entries:            Vec<DirtyEntry>,
    pub dirty_from:         HashMap<Uuid, NaiveDate>,
    pub dirty_to:           HashMap<Uuid, NaiveDate>,
    pub existing_snapshots: HashSet<(Uuid, NaiveDate)>,
    pub crawl_start:        NaiveDate,
    pub history_start:      NaiveDate,
    pub bypass_mode:        bool,
}

impl DirtyContext {
    pub async fn from_import(
        entity_id:      Uuid,
        computed_as_of: NaiveDate,
        flux_start:     NaiveDate,
        input:          &DirtyDetectionInput,
        pool:           &PgPool,
    ) -> Result<Self> {
        let entries            = query_entries(entity_id, pool).await?;
        let history_start      = query_history_start(entity_id, pool).await?;
        let last_snapshots     = query_last_snapshot_per_entry(entity_id, pool).await?;
        let existing_snapshots = query_existing_snapshots(entity_id, pool).await?;
        let source_c_ids       = query_source_c_entry_ids(entity_id, pool).await?;

        let entry_start: HashMap<Uuid, NaiveDate> =
            entries.iter().map(|e| (e.id, e.start_date)).collect();

        let mut dirty_from: HashMap<Uuid, NaiveDate> = HashMap::new();

        let touched: HashSet<Uuid> = input
            .new_entry_assignments
            .iter()
            .map(|(id, _)| *id)
            .chain(input.superseded_entry_ids.iter().copied())
            .collect();

        for entry_id in &touched {
            let df = last_snapshots
                .get(entry_id)
                .copied()
                .unwrap_or_else(|| entry_start.get(entry_id).copied().unwrap_or(history_start));
            merge_dirty_from(&mut dirty_from, *entry_id, df);
        }

        for entry_id in &source_c_ids {
            let df = entry_start.get(entry_id).copied().unwrap_or(history_start);
            merge_dirty_from(&mut dirty_from, *entry_id, df);
        }

        let dirty_to    = compute_dirty_to(&entries, computed_as_of);
        let crawl_start = compute_crawl_start(flux_start, history_start, &dirty_from, &existing_snapshots);

        Ok(Self {
            entries,
            dirty_from,
            dirty_to,
            existing_snapshots,
            crawl_start,
            history_start,
            bypass_mode: false,
        })
    }

    pub fn full_rerun(
        entries:        Vec<DirtyEntry>,
        history_start:  NaiveDate,
        computed_as_of: NaiveDate,
    ) -> Self {
        let dirty_from: HashMap<Uuid, NaiveDate> = entries
            .iter()
            .map(|e| (e.id, e.start_date.min(history_start)))
            .collect();
        let dirty_to = compute_dirty_to(&entries, computed_as_of);
        Self {
            entries,
            dirty_from,
            dirty_to,
            existing_snapshots: HashSet::new(),
            crawl_start: history_start,
            history_start,
            bypass_mode: true,
        }
    }

    pub fn is_dirty_for_date(&self, entry_id: Uuid, date: NaiveDate, flux_start: NaiveDate) -> bool {
        match self.dirty_to.get(&entry_id) {
            None           => return false,
            Some(&ceiling) if date > ceiling => return false,
            _ => {}
        }

        if self.bypass_mode {
            return true;
        }

        if date >= flux_start {
            return true;
        }

        if let Some(&df) = self.dirty_from.get(&entry_id) {
            if date >= df {
                return true;
            }
        }

        !self.existing_snapshots.contains(&(entry_id, date))
    }

    pub fn dirty_entry_ids_for_date(&self, date: NaiveDate, flux_start: NaiveDate) -> Vec<Uuid> {
        self.entries
            .iter()
            .filter(|e| date >= e.start_date && self.is_dirty_for_date(e.id, date, flux_start))
            .map(|e| e.id)
            .collect()
    }
}

// ---------------------------------------------------------------------------
// Pure helper functions (unit-testable without DB)
// ---------------------------------------------------------------------------

fn merge_dirty_from(map: &mut HashMap<Uuid, NaiveDate>, entry_id: Uuid, dirty_from: NaiveDate) {
    map.entry(entry_id)
        .and_modify(|d| *d = (*d).min(dirty_from))
        .or_insert(dirty_from);
}

fn compute_dirty_to(entries: &[DirtyEntry], computed_as_of: NaiveDate) -> HashMap<Uuid, NaiveDate> {
    entries
        .iter()
        .map(|e| {
            let ceiling = e
                .end_date
                .filter(|&d| d <= computed_as_of)
                .unwrap_or(computed_as_of);
            (e.id, ceiling)
        })
        .collect()
}

fn compute_crawl_start(
    flux_start:         NaiveDate,
    history_start:      NaiveDate,
    dirty_from:         &HashMap<Uuid, NaiveDate>,
    existing_snapshots: &HashSet<(Uuid, NaiveDate)>,
) -> NaiveDate {
    let min_dirty = dirty_from.values().copied().min();

    let min_existing = existing_snapshots.iter().map(|(_, d)| *d).min();
    let history_candidate = match min_existing {
        None => Some(history_start),
        Some(earliest) if history_start < earliest => Some(history_start),
        _ => None,
    };

    [Some(flux_start), min_dirty, history_candidate]
        .into_iter()
        .flatten()
        .min()
        .unwrap_or(flux_start)
}

// ---------------------------------------------------------------------------
// DB queries
// ---------------------------------------------------------------------------

pub async fn query_entries(entity_id: Uuid, pool: &PgPool) -> Result<Vec<DirtyEntry>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        id:         Uuid,
        start_date: NaiveDate,
        end_date:   Option<NaiveDate>,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT id, start_date, end_date FROM entries WHERE entity_id = $1",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load entries for dirty detection")?;

    Ok(rows
        .into_iter()
        .map(|r| DirtyEntry { id: r.id, start_date: r.start_date, end_date: r.end_date })
        .collect())
}

pub async fn query_history_start(entity_id: Uuid, pool: &PgPool) -> Result<NaiveDate> {
    let row: (Option<NaiveDate>,) =
        sqlx::query_as("SELECT MIN(date) FROM transactions WHERE entity_id = $1")
            .bind(entity_id)
            .fetch_one(pool)
            .await
            .context("failed to query history_start")?;

    row.0.ok_or_else(|| anyhow::anyhow!("no transactions found for entity {entity_id}"))
}

async fn query_last_snapshot_per_entry(
    entity_id: Uuid,
    pool:      &PgPool,
) -> Result<HashMap<Uuid, NaiveDate>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:       Uuid,
        last_snapshot: NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        r#"
        SELECT node_id, MAX(snapshot_date) AS last_snapshot
        FROM snapshots
        WHERE entity_id = $1 AND node_type = 'entry'
        GROUP BY node_id
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to query last snapshot per entry")?;

    Ok(rows.into_iter().map(|r| (r.node_id, r.last_snapshot)).collect())
}

async fn query_existing_snapshots(
    entity_id: Uuid,
    pool:      &PgPool,
) -> Result<HashSet<(Uuid, NaiveDate)>> {
    #[derive(sqlx::FromRow)]
    struct Row {
        node_id:       Uuid,
        snapshot_date: NaiveDate,
    }

    let rows: Vec<Row> = sqlx::query_as(
        "SELECT node_id, snapshot_date FROM snapshots WHERE entity_id = $1 AND node_type = 'entry'",
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to load existing snapshots for gap-fill detection")?;

    Ok(rows.into_iter().map(|r| (r.node_id, r.snapshot_date)).collect())
}

async fn query_source_c_entry_ids(entity_id: Uuid, pool: &PgPool) -> Result<Vec<Uuid>> {
    let rows: Vec<(Uuid,)> = sqlx::query_as(
        r#"
        SELECT e.id
        FROM entries e
        LEFT JOIN (
            SELECT node_id, MAX(computed_as_of) AS max_coa
            FROM snapshots
            WHERE entity_id = $1 AND node_type = 'entry'
            GROUP BY node_id
        ) s ON s.node_id = e.id
        WHERE e.entity_id = $1
          AND (s.max_coa IS NULL OR e.updated_at > s.max_coa)
        "#,
    )
    .bind(entity_id)
    .fetch_all(pool)
    .await
    .context("failed to query Source C (entry metadata changes)")?;

    Ok(rows.into_iter().map(|(id,)| id).collect())
}

// ---------------------------------------------------------------------------
// Tests (pure — no DB)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn d(s: &str) -> NaiveDate {
        NaiveDate::parse_from_str(s, "%Y-%m-%d").unwrap()
    }

    fn entry(id: Uuid, start: &str, end: Option<&str>) -> DirtyEntry {
        DirtyEntry {
            id,
            start_date: d(start),
            end_date:   end.map(d),
        }
    }

    #[test]
    fn merge_dirty_from_keeps_earliest() {
        let mut map = HashMap::new();
        let id = Uuid::nil();
        merge_dirty_from(&mut map, id, d("2025-06-01"));
        merge_dirty_from(&mut map, id, d("2025-03-01"));
        merge_dirty_from(&mut map, id, d("2025-09-01"));
        assert_eq!(map[&id], d("2025-03-01"));
    }

    #[test]
    fn compute_dirty_to_live_entry_returns_computed_as_of() {
        let id = Uuid::nil();
        let entries = vec![entry(id, "2024-01-01", None)];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], coa);
    }

    #[test]
    fn compute_dirty_to_ended_entry_before_coa_returns_end_date() {
        let id = Uuid::nil();
        let end = "2025-06-30";
        let entries = vec![entry(id, "2024-01-01", Some(end))];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], d(end));
    }

    #[test]
    fn compute_dirty_to_end_date_after_coa_returns_coa() {
        let id = Uuid::nil();
        let entries = vec![entry(id, "2024-01-01", Some("2030-01-01"))];
        let coa = d("2025-12-31");
        let map = compute_dirty_to(&entries, coa);
        assert_eq!(map[&id], coa);
    }

    #[test]
    fn crawl_start_no_snapshots_returns_history_start() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let dirty_from    = HashMap::new();
        let existing      = HashSet::new();
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, history_start);
    }

    #[test]
    fn crawl_start_with_dirty_entry_earlier_than_flux() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let id            = Uuid::nil();
        let mut dirty_from = HashMap::new();
        dirty_from.insert(id, d("2025-06-01"));
        let mut existing = HashSet::new();
        existing.insert((id, d("2025-01-01")));
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, d("2025-06-01"));
    }

    #[test]
    fn crawl_start_history_before_earliest_snapshot_triggers_gap_fill() {
        let flux_start    = d("2025-12-01");
        let history_start = d("2025-01-01");
        let id            = Uuid::nil();
        let dirty_from    = HashMap::new();
        let mut existing  = HashSet::new();
        existing.insert((id, d("2025-03-01")));
        let cs = compute_crawl_start(flux_start, history_start, &dirty_from, &existing);
        assert_eq!(cs, history_start);
    }

    #[test]
    fn is_dirty_before_entry_start_returns_false() {
        let id  = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-06-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        let ids = ctx.dirty_entry_ids_for_date(d("2025-05-31"), d("2025-12-01"));
        assert!(ids.is_empty());
    }

    #[test]
    fn is_dirty_past_ceiling_returns_false() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2024-01-01", Some("2025-06-30"))],
            dirty_from:         [(id, d("2024-01-01"))].into_iter().collect(),
            dirty_to:           [(id, d("2025-06-30"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2024-01-01"),
            history_start:      d("2024-01-01"),
            bypass_mode:        false,
        };
        assert!(!ctx.is_dirty_for_date(id, d("2025-07-01"), d("2025-12-01")));
    }

    #[test]
    fn is_dirty_gap_fill_for_missing_snapshot() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-01-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        let flux_start = d("2025-12-01");
        assert!(ctx.is_dirty_for_date(id, d("2025-03-01"), flux_start));
    }

    #[test]
    fn is_dirty_existing_snapshot_not_in_dirty_range_returns_false() {
        let id = Uuid::nil();
        let date = d("2025-03-01");
        let mut existing = HashSet::new();
        existing.insert((id, date));
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2025-01-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: existing,
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };
        let flux_start = d("2025-12-01");
        assert!(!ctx.is_dirty_for_date(id, date, flux_start));
    }

    #[test]
    fn bypass_mode_returns_true_within_ceiling() {
        let id = Uuid::nil();
        let ctx = DirtyContext {
            entries:            vec![entry(id, "2024-01-01", None)],
            dirty_from:         HashMap::new(),
            dirty_to:           [(id, d("2025-12-31"))].into_iter().collect(),
            existing_snapshots: HashSet::new(),
            crawl_start:        d("2024-01-01"),
            history_start:      d("2024-01-01"),
            bypass_mode:        true,
        };
        assert!(ctx.is_dirty_for_date(id, d("2024-06-15"), d("2025-12-01")));
    }

    #[test]
    fn dirty_entry_ids_for_date_returns_only_dirty() {
        let id_a = Uuid::parse_str("aaaaaaaa-0000-0000-0000-000000000000").unwrap();
        let id_b = Uuid::parse_str("bbbbbbbb-0000-0000-0000-000000000000").unwrap();
        let date  = d("2025-06-01");
        let flux_start = d("2025-12-01");

        let mut existing = HashSet::new();
        existing.insert((id_a, date));

        let ctx = DirtyContext {
            entries:            vec![
                entry(id_a, "2025-01-01", None),
                entry(id_b, "2025-01-01", None),
            ],
            dirty_from:         HashMap::new(),
            dirty_to:           [
                (id_a, d("2025-12-31")),
                (id_b, d("2025-12-31")),
            ].into_iter().collect(),
            existing_snapshots: existing,
            crawl_start:        d("2025-01-01"),
            history_start:      d("2025-01-01"),
            bypass_mode:        false,
        };

        let ids = ctx.dirty_entry_ids_for_date(date, flux_start);
        assert_eq!(ids.len(), 1);
        assert!(ids.contains(&id_b));
    }
}
