use anyhow::Result;
use serde::{Deserialize, Serialize};

/// Envelope for every job consumed from the `veloci.jobs` queue.
#[derive(Debug, Deserialize, Serialize)]
pub struct Job {
    pub job_id: String,
    pub r#type: String,
    pub entity_id: String,
    pub metadata: serde_json::Value,
}

/// Route a [`Job`] to the appropriate stub handler.
///
/// Unknown job types are logged as warnings and silently dropped — they do not
/// cause the consumer to NACK or restart.
pub async fn dispatch(job: Job) -> Result<()> {
    match job.r#type.as_str() {
        "import.process" => import_process(&job.entity_id).await,
        "rules.reprocess" => rules_reprocess(&job.entity_id).await,
        "account.analyze" => account_analyze(&job.entity_id).await,
        other => {
            tracing::warn!("unknown job type: {}", other);
            Ok(())
        }
    }
}

async fn import_process(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "import.process stub");
    Ok(())
}

async fn rules_reprocess(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "rules.reprocess stub");
    Ok(())
}

async fn account_analyze(entity_id: &str) -> Result<()> {
    tracing::info!(entity_id, "account.analyze stub");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[tokio::test]
    async fn known_job_types_dispatch_ok() {
        for t in &["import.process", "rules.reprocess", "account.analyze"] {
            let job = Job {
                job_id: "j".into(),
                r#type: t.to_string(),
                entity_id: "e".into(),
                metadata: json!({}),
            };
            assert!(dispatch(job).await.is_ok(), "failed for {}", t);
        }
    }

    #[tokio::test]
    async fn unknown_job_type_is_dropped_not_errored() {
        let job = Job {
            job_id: "j".into(),
            r#type: "unknown".into(),
            entity_id: "e".into(),
            metadata: json!({}),
        };
        assert!(dispatch(job).await.is_ok());
    }
}
