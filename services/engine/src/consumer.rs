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

    tracing::info!("consuming from {} (prefetch=1)", QUEUE);
    while let Some(delivery) = consumer.next().await {
        let d = delivery?;
        match serde_json::from_slice::<jobs::JobMessage>(&d.data) {
            Ok(msg) => {
                let (eid, jt) = (msg.entity_id, msg.job_type.clone());
                if let Err(e) = jobs::dispatch(&jt, eid, msg.job_id, msg.metadata, &pools).await {
                    tracing::error!(entity_id = %eid, job_type = %jt, "job failed: {:?}", e);
                }
            }
            Err(e) => tracing::error!("malformed job payload: {:?}", e),
        }
        d.ack(BasicAckOptions::default()).await?;
    }
    Ok(())
}
