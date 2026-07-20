//! Job message deserialization and dispatch.
//!
//! The Go API publishes JSON to `veloci.jobs`. This module owns the wire
//! format, validates the job type, and routes to the appropriate pipeline
//! entry point.
//!
//! # Wire format
//!
//! ```json
//! {
//!   "job_id":    "uuid",
//!   "entity_id": "uuid",
//!   "job_type":  "import.process",
//!   "metadata":  { "pending_import_id": "uuid" }
//! }
//! ```

use anyhow::{bail, Result};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::{db::Pools, pipeline};

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

/// Raw job message deserialized from the RabbitMQ delivery payload.
#[derive(Debug, Deserialize, Serialize)]
pub struct JobMessage {
    pub job_id:    Uuid,
    pub entity_id: Uuid,
    pub job_type:  String,
    #[serde(default)]
    pub metadata:  serde_json::Value,
}

/// Validated job types the engine recognises.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum JobType {
    /// Stages 0 → 7. Requires `metadata.pending_import_id`.
    ImportProcess,
    /// Stages 1 → 7. Triggered on entry create/modify/delete or canonical merchant change.
    EntriesReprocess,
    /// Stages 3 → 7. Triggered on entry approval or manual recalculate.
    AccountAnalyze,
    /// Stage 7 only. Triggered on manual balance update.
    BalanceProject,
}

impl JobType {
    /// Parse from the wire string representation.
    pub fn from_str(s: &str) -> Option<Self> {
        match s {
            "import.process"   => Some(Self::ImportProcess),
            "entries.reprocess" => Some(Self::EntriesReprocess),
            "account.analyze"  => Some(Self::AccountAnalyze),
            "balance.project"  => Some(Self::BalanceProject),
            _                  => None,
        }
    }
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

/// Route a validated job message to the appropriate pipeline stages.
///
/// Unknown job types are logged as warnings and silently dropped — they do
/// not cause the consumer to NACK or restart.
pub async fn dispatch(
    job_type: &str,
    entity_id: Uuid,
    job_id: Uuid,
    metadata: serde_json::Value,
    pools: &Pools,
) -> Result<()> {
    let Some(jt) = JobType::from_str(job_type) else {
        tracing::warn!(job_type, "unknown job type — dropping");
        return Ok(());
    };

    match jt {
        JobType::ImportProcess => {
            let pending_import_id = metadata
                .get("pending_import_id")
                .and_then(|v| v.as_str())
                .and_then(|s| s.parse::<Uuid>().ok());

            let Some(import_id) = pending_import_id else {
                bail!(
                    "import.process job {job_id} missing valid metadata.pending_import_id"
                );
            };

            pipeline::run_import(entity_id, job_id, import_id, pools).await
        }

        JobType::EntriesReprocess => {
            pipeline::run_entries_reprocess(entity_id, job_id, pools).await
        }

        JobType::AccountAnalyze => {
            pipeline::run_account_analyze(entity_id, job_id, pools).await
        }

        JobType::BalanceProject => {
            pipeline::run_balance_project(entity_id, job_id, pools).await
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn job_type_from_str_known() {
        assert_eq!(JobType::from_str("import.process"),  Some(JobType::ImportProcess));
        assert_eq!(JobType::from_str("entries.reprocess"), Some(JobType::EntriesReprocess));
        assert_eq!(JobType::from_str("account.analyze"), Some(JobType::AccountAnalyze));
        assert_eq!(JobType::from_str("balance.project"), Some(JobType::BalanceProject));
    }

    #[test]
    fn job_type_from_str_unknown_is_none() {
        assert!(JobType::from_str("").is_none());
        assert!(JobType::from_str("import").is_none());
        assert!(JobType::from_str("IMPORT.PROCESS").is_none());
    }

    #[test]
    fn job_message_deserializes() {
        let raw = json!({
            "job_id":    "00000000-0000-0000-0000-000000000001",
            "entity_id": "00000000-0000-0000-0000-000000000002",
            "job_type":  "import.process",
            "metadata":  { "pending_import_id": "00000000-0000-0000-0000-000000000003" }
        });
        let msg: JobMessage = serde_json::from_value(raw).unwrap();
        assert_eq!(msg.job_type, "import.process");
    }

    #[test]
    fn job_message_metadata_defaults_to_null_object() {
        let raw = json!({
            "job_id":    "00000000-0000-0000-0000-000000000001",
            "entity_id": "00000000-0000-0000-0000-000000000002",
            "job_type":  "entries.reprocess"
        });
        let msg: JobMessage = serde_json::from_value(raw).unwrap();
        assert!(msg.metadata.is_null() || msg.metadata.is_object());
    }
}
