//! RabbitMQ consumer: connects with exponential backoff, declares the queue,
//! sets `basic_qos(prefetch_count=1)` for correct horizontal scaling, and
//! dispatches each delivery to [`jobs::dispatch`].
//!
//! `prefetch_count = 1` is critical: it ensures each engine instance holds at
//! most one in-flight job at a time, which prevents a slow import from
//! blocking progress on other entities' jobs.

use anyhow::Result;
use backon::{ExponentialBuilder, Retryable};
use futures_lite::StreamExt;
use lapin::{
    options::{
        BasicAckOptions, BasicConsumeOptions, BasicQosOptions, QueueDeclareOptions,
    },
    types::FieldTable,
    Connection, ConnectionProperties,
};
use uuid::Uuid;

use crate::{config::AppConfig, db::Pools, jobs};

const QUEUE: &str = "veloci.jobs";

/// Connect to RabbitMQ with exponential backoff, declare `veloci.jobs` as a
/// durable queue, enforce `prefetch_count=1`, then consume indefinitely.
///
/// Each delivery is dispatched to [`jobs::dispatch`]. On job error the message
/// is ACKed (logged and dropped) to prevent poison-pill loops. Production
/// deployments should configure a dead-letter exchange for retry semantics.
pub async fn run(cfg: &AppConfig, pools: Pools) -> Result<()> {
    let uri = cfg.amqp_uri();

    let conn = (|| async {
        Connection::connect(&uri, ConnectionProperties::default()).await
    })
    .retry(ExponentialBuilder::default().with_max_times(10))
    .await?;

    let ch = conn.create_channel().await?;

    // Declare the queue as durable so it survives broker restarts.
    ch.queue_declare(
        QUEUE,
        QueueDeclareOptions {
            durable: true,
            ..Default::default()
        },
        FieldTable::default(),
    )
    .await?;

    // Critical for horizontal scaling: each consumer processes exactly one job
    // at a time. Without this, RabbitMQ round-robins all pending messages to
    // the first consumer that connects, regardless of its load.
    ch.basic_qos(1, BasicQosOptions::default()).await?;

    let mut consumer = ch
        .basic_consume(
            QUEUE,
            "veloci-engine",
            BasicConsumeOptions::default(),
            FieldTable::default(),
        )
        .await?;

    tracing::info!("consuming from {QUEUE} (prefetch=1)");
    while let Some(delivery) = consumer.next().await {
        let d = delivery?;

        // Two-phase parse: extract job_id first so we can mark failed even on
        // a malformed payload, then attempt the full deserialization.
        let raw: serde_json::Value = match serde_json::from_slice(&d.data) {
            Ok(v) => v,
            Err(e) => {
                tracing::error!("unparseable job payload (not valid JSON): {:?}", e);
                d.ack(BasicAckOptions::default()).await?;
                continue;
            }
        };
        let job_id_str = raw.get("job_id").and_then(|v| v.as_str()).unwrap_or("");
        let fallback_jid: Option<Uuid> = job_id_str.parse().ok();

        match serde_json::from_value::<jobs::JobMessage>(raw) {
            Ok(msg) => {
                let (eid, jt, jid) = (msg.entity_id, msg.job_type.clone(), msg.job_id);
                tracing::info!(entity_id = %eid, job_type = %jt, job_id = %jid, "job received");
                set_job_status(jid, "processing", None, &pools).await;
                match jobs::dispatch(&jt, eid, jid, msg.metadata, &pools).await {
                    Ok(()) => {
                        tracing::info!(entity_id = %eid, job_type = %jt, job_id = %jid, "job succeeded");
                        set_job_status(jid, "complete", None, &pools).await;
                    }
                    Err(e) => {
                        let msg = format!("{e:?}");
                        tracing::error!(entity_id = %eid, job_type = %jt, job_id = %jid, "job failed: {}", msg);
                        set_job_status(jid, "failed", Some(&msg), &pools).await;
                    }
                }
            }
            Err(e) => {
                let detail = format!("malformed job payload: {e:?}");
                tracing::error!("{}", detail);
                if let Some(jid) = fallback_jid {
                    set_job_status(jid, "failed", Some(&detail), &pools).await;
                }
            }
        }
        d.ack(BasicAckOptions::default()).await?;
    }
    Ok(())
}

async fn set_job_status(job_id: Uuid, status: &str, error: Option<&str>, pools: &Pools) {
    let result = sqlx::query(
        "UPDATE processing_jobs SET status = $1, started_at = CASE WHEN $1 = 'processing' THEN NOW() ELSE started_at END, completed_at = CASE WHEN $1 IN ('complete', 'failed') THEN NOW() ELSE NULL END, error = $3 WHERE id = $2",
    )
    .bind(status)
    .bind(job_id)
    .bind(error)
    .execute(&pools.write)
    .await;

    if let Err(e) = result {
        tracing::warn!(job_id = %job_id, status, "failed to update job status: {e}");
    }
}
