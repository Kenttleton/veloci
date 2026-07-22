//! Shared domain types used across pipeline stages.
//!
//! All types in this module are pure data — no async, no DB calls, no I/O.
//! This makes them directly unit-testable without a database.

use anyhow::Result;
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Entry domain types
// ---------------------------------------------------------------------------

/// Entry type of an entry — determines rate computation semantics.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum EntryType {
    /// Recurring spend with consistent timing and amount. Rate = amount / period_days.
    Standing,
    /// Recurring spend with variable amounts. Rate = rolling_window_total / window_days.
    Variable,
    /// No detectable cadence or consistent amount. Groups by merchant; amortized over period_days.
    Irregular,
}

impl EntryType {
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "standing"  => Some(Self::Standing),
            "variable"  => Some(Self::Variable),
            "irregular" => Some(Self::Irregular),
            _           => None,
        }
    }
}

/// Variable entry projection method.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VariableMethod {
    /// Project the mean of recent observed amounts.
    Avg,
    /// Project the maximum of recent observed amounts (conservative).
    Max,
}

impl VariableMethod {
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "avg" => Some(Self::Avg),
            "max" => Some(Self::Max),
            _     => None,
        }
    }
}

/// Cash flow direction.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Direction {
    Income,
    Spend,
    /// Both income and spend flows — used for entries that span both directions.
    Mixed,
}

impl Direction {
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "income"  => Some(Self::Income),
            "spend" => Some(Self::Spend),
            "mixed"   => Some(Self::Mixed),
            _         => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Rate output types
// ---------------------------------------------------------------------------

/// Per-entry rate computed by Stage 3.
#[derive(Debug, Clone)]
pub struct EntryRate {
    pub entry_id:                   Uuid,
    pub label_id:                   Option<Uuid>,
    pub direction:                  Direction,
    pub entry_type:                 EntryType,
    pub period_days:                i32,
    pub actual_rate_per_day:        f64,
    pub projected_rate_per_day:     f64,
    pub transaction_count:          i32,
    pub window_days_used:           i32,
    pub rolling_window_total_cents: i64,
}

/// Per-label rate computed by Stage 4.
#[derive(Debug, Clone)]
pub struct LabelRate {
    pub label_id:                  Uuid,
    pub direction:                 Direction,
    pub period_days:               i32,
    pub actual_rate_per_day:       f64,
    pub projected_rate_per_day:    f64,
    pub contributing_entry_count:  i32,
}

/// Trend values computed by Stage 5 for a single node.
#[derive(Debug, Clone)]
pub struct NodeTrend {
    pub node_id:       Uuid,
    pub node_type:     NodeType,
    pub drift_per_day: f64,
    pub slope_per_day: f64,
    pub r_squared:     f64,
}

/// Discriminates entry nodes from label nodes in `snapshots`.
///
/// The `node_type` column in the `snapshots` table stores the string form of
/// this discriminant (`"entry"` or `"label"`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodeType {
    Entry,
    Label,
}

impl NodeType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Entry => "entry",
            Self::Label => "label",
        }
    }

    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "entry" => Some(Self::Entry),
            "label" => Some(Self::Label),
            _       => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Snapshot history (used by Stage 5)
// ---------------------------------------------------------------------------

/// A single historical snapshot row, as bulk-loaded before the Stage 5 par_iter.
#[derive(Debug, Clone)]
pub struct SnapshotRow {
    pub node_id:             Uuid,
    pub node_type:           NodeType,
    pub snapshot_date:       NaiveDate,
    pub computed_as_of:      NaiveDate,
    pub actual_rate_per_day: f64,
}

// ---------------------------------------------------------------------------
// Stage output summary types
// ---------------------------------------------------------------------------

/// Output from Stage 0.
#[derive(Debug, Clone)]
pub struct Stage0Output {
    /// The `MAX(date)` from transactions — used as the flux window anchor.
    pub computed_as_of: NaiveDate,
    pub imported_count: u32,
    pub skipped_count:  u32,
}

/// Output from Stage 1.
#[derive(Debug, Clone)]
pub struct Stage1Output {
    pub total_assignments: u64,
    /// UUIDs of transactions that matched no entry — passed to Stage 2.
    pub unmatched_tx_ids:  Vec<Uuid>,
}

/// Output from Stage 2.
#[derive(Debug, Clone)]
pub struct Stage2Output {
    pub clusters_created: u32,
}

/// Output from Stage 3.
#[derive(Debug, Clone)]
pub struct Stage3Output {
    pub entry_rates: Vec<EntryRate>,
}

/// Output from Stage 4.
#[derive(Debug, Clone)]
pub struct Stage4Output {
    pub label_rates: Vec<LabelRate>,
}

/// Output from Stage 5.
#[derive(Debug, Clone)]
pub struct Stage5Output {
    pub entry_trends: Vec<NodeTrend>,
    pub label_trends: Vec<NodeTrend>,
}

// ---------------------------------------------------------------------------
// Settlement config (loaded once, used for flux window calculation)
// ---------------------------------------------------------------------------

/// Settlement configuration for an entity, sourced from `institution_mappings`.
///
/// Used to determine the flux window for the day-crawl in the pipeline.
#[derive(Debug, Clone)]
pub struct SettlementConfig {
    pub settlement_window_days: i32,
}

impl SettlementConfig {
    /// Query the settlement window for an entity.
    ///
    /// Uses the maximum `settlement_window_days` across all institution
    /// mappings for accounts belonging to this entity. Defaults to 7 days
    /// if no institution mappings are configured.
    pub async fn query(entity_id: Uuid, pool: &PgPool) -> Result<Self> {
        let row: (i32,) = sqlx::query_as(
            r#"
            SELECT COALESCE(MAX(im.settlement_window_days), 7)
            FROM accounts a
            JOIN institution_mappings im ON im.id = a.institution_id
            WHERE a.entity_id = $1
              AND a.status = 'active'
            "#,
        )
        .bind(entity_id)
        .fetch_one(pool)
        .await?;

        Ok(Self {
            settlement_window_days: row.0,
        })
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn entry_type_from_str() {
        assert_eq!(EntryType::from_str("standing"),  Some(EntryType::Standing));
        assert_eq!(EntryType::from_str("variable"),  Some(EntryType::Variable));
        assert_eq!(EntryType::from_str("irregular"), Some(EntryType::Irregular));
        assert_eq!(EntryType::from_str("single"),    None); // old name removed
        assert_eq!(EntryType::from_str("one_time"),  None); // older name removed
        assert_eq!(EntryType::from_str("hit"),       None);
        assert_eq!(EntryType::from_str("boost"),     None);
    }

    #[test]
    fn direction_from_str() {
        assert_eq!(Direction::from_str("income"),  Some(Direction::Income));
        assert_eq!(Direction::from_str("spend"), Some(Direction::Spend));
        assert_eq!(Direction::from_str("mixed"),   Some(Direction::Mixed));
        assert_eq!(Direction::from_str(""),        None);
    }

    #[test]
    fn node_type_roundtrip() {
        assert_eq!(NodeType::Entry.as_str(), "entry");
        assert_eq!(NodeType::Label.as_str(), "label");
        assert_eq!(NodeType::from_str("entry"), Some(NodeType::Entry));
        assert_eq!(NodeType::from_str("label"), Some(NodeType::Label));
        assert_eq!(NodeType::from_str("rule"),           None);
        assert_eq!(NodeType::from_str("classification"), None);
    }
}
