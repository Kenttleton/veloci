//! Shared domain types used across pipeline stages.
//!
//! All types in this module are pure data — no async, no DB calls, no I/O.
//! This makes them directly unit-testable without a database.

use anyhow::Result;
use chrono::NaiveDate;
use sqlx::PgPool;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Rule domain types
// ---------------------------------------------------------------------------

/// The execution stage of a rule (determines ordering in Stage 1).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum RuleStage {
    /// Runs first. Matches transaction attributes (merchant, amount, date).
    Pre,
    /// Runs after `Pre`. Can match on label UUIDs from pre-stage output.
    Post,
}

impl RuleStage {
    /// Parse from the DB string representation.
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "pre"  => Some(Self::Pre),
            "post" => Some(Self::Post),
            _      => None,
        }
    }
}

/// Entry type of a rule — determines rate computation semantics.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EntryType {
    /// Recurring commitment with consistent timing. Rate = amount / period_days.
    Standing,
    /// Recurring commitment with variable amounts.
    Variable,
    /// One-time expense, amortized over `period_days`.
    Hit,
    /// One-time income, amortized over `period_days`.
    Boost,
}

impl EntryType {
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "standing" => Some(Self::Standing),
            "variable" => Some(Self::Variable),
            "hit"      => Some(Self::Hit),
            "boost"    => Some(Self::Boost),
            _          => None,
        }
    }
}

/// Variable rule projection method.
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
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Direction {
    Income,
    Expense,
}

impl Direction {
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "income"  => Some(Self::Income),
            "expense" => Some(Self::Expense),
            _         => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Rate output types
// ---------------------------------------------------------------------------

/// Per-rule rate computed by Stage 3.
#[derive(Debug, Clone)]
pub struct RuleRate {
    pub rule_id:                  Uuid,
    pub label_id:                 Option<Uuid>,
    pub direction:                Direction,
    pub entry_type:               EntryType,
    pub period_days:              i32,
    pub epoch_id:                 Option<Uuid>,
    pub actual_rate_per_day:      f64,
    pub projected_rate_per_day:   f64,
    pub transaction_count:        i32,
    pub window_days_used:         i32,
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
    pub contributing_rule_count:   i32,
}

/// Trend values computed by Stage 5 for a single node.
#[derive(Debug, Clone)]
pub struct NodeTrend {
    pub node_id:            Uuid,
    pub node_type:          NodeType,
    pub drift_per_day:      f64,
    pub slope_per_day:      f64,
    pub r_squared:          f64,
}

/// Discriminates rule nodes from label nodes in `computed_snapshots`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodeType {
    Rule,
    Label,
}

impl NodeType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Rule  => "rule",
            Self::Label => "label",
        }
    }

    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "rule"  => Some(Self::Rule),
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
    pub node_id:              Uuid,
    pub node_type:            NodeType,
    pub snapshot_date:        NaiveDate,
    pub computed_as_of:       NaiveDate,
    pub actual_rate_per_day:  f64,
    pub epoch_id:             Option<Uuid>,
}

// ---------------------------------------------------------------------------
// Stage output summary types
// ---------------------------------------------------------------------------

/// Output from Stage 0.
#[derive(Debug, Clone)]
pub struct Stage0Output {
    /// The `MAX(date)` from raw_transactions — used as the flux window anchor.
    pub computed_as_of:   NaiveDate,
    pub imported_count:   u32,
    pub skipped_count:    u32,
}

/// Output from Stage 1.
#[derive(Debug, Clone)]
pub struct Stage1Output {
    pub total_assignments: u64,
    /// UUIDs of transactions that matched no rule — passed to Stage 2.
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
    pub rule_rates: Vec<RuleRate>,
}

/// Output from Stage 4.
#[derive(Debug, Clone)]
pub struct Stage4Output {
    pub label_rates: Vec<LabelRate>,
}

/// Output from Stage 5.
#[derive(Debug, Clone)]
pub struct Stage5Output {
    pub rule_trends:  Vec<NodeTrend>,
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
    fn rule_stage_ordering() {
        // Pre must sort before Post for Stage 1 ordering.
        assert!(RuleStage::Pre < RuleStage::Post);
    }

    #[test]
    fn rule_stage_from_str() {
        assert_eq!(RuleStage::from_str("pre"),  Some(RuleStage::Pre));
        assert_eq!(RuleStage::from_str("post"), Some(RuleStage::Post));
        assert_eq!(RuleStage::from_str("PRE"),  None);
    }

    #[test]
    fn entry_type_from_str() {
        assert_eq!(EntryType::from_str("standing"), Some(EntryType::Standing));
        assert_eq!(EntryType::from_str("variable"), Some(EntryType::Variable));
        assert_eq!(EntryType::from_str("hit"),      Some(EntryType::Hit));
        assert_eq!(EntryType::from_str("boost"),    Some(EntryType::Boost));
        assert_eq!(EntryType::from_str("single"),   None); // deprecated
    }

    #[test]
    fn direction_from_str() {
        assert_eq!(Direction::from_str("income"),  Some(Direction::Income));
        assert_eq!(Direction::from_str("expense"), Some(Direction::Expense));
        assert_eq!(Direction::from_str(""),        None);
    }

    #[test]
    fn node_type_roundtrip() {
        assert_eq!(NodeType::Rule.as_str(),  "rule");
        assert_eq!(NodeType::Label.as_str(), "label");
        assert_eq!(NodeType::from_str("rule"),  Some(NodeType::Rule));
        assert_eq!(NodeType::from_str("label"), Some(NodeType::Label));
        assert_eq!(NodeType::from_str("entry"), None);
    }
}
