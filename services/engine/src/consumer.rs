use anyhow::Result;
use backon::{ExponentialBuilder, Retryable};
use futures_lite::StreamExt;
use lapin::{
    options::{BasicAckOptions, BasicConsumeOptions, QueueDeclareOptions},
    types::FieldTable,
    Connection, ConnectionProperties,
};

use crate::jobs::{self, Job};

const QUEUE: &str = "veloci.jobs";

/// Connect to RabbitMQ with exponential backoff, then consume `veloci.jobs`
/// indefinitely, dispatching each message to [`jobs::dispatch`].
///
/// ACKs every delivery regardless of dispatch outcome — failed jobs are logged
/// and dropped rather than re-queued, which prevents poison-pill loops in stub
/// handlers.  Real handlers should NACK on transient errors and rely on a
/// dead-letter exchange.
pub async fn run(rabbitmq_url: &str) -> Result<()> {
    let url = rabbitmq_url.to_string();

    let conn = (|| async {
        Connection::connect(&url, ConnectionProperties::default()).await
    })
    .retry(ExponentialBuilder::default().with_max_times(10))
    .await?;

    let ch = conn.create_channel().await?;
    ch.queue_declare(
        QUEUE,
        QueueDeclareOptions {
            durable: true,
            ..Default::default()
        },
        FieldTable::default(),
    )
    .await?;

    let mut consumer = ch
        .basic_consume(
            QUEUE,
            "veloci-engine",
            BasicConsumeOptions::default(),
            FieldTable::default(),
        )
        .await?;

    tracing::info!("consuming from {}", QUEUE);
    while let Some(delivery) = consumer.next().await {
        let d = delivery?;
        match serde_json::from_slice::<Job>(&d.data) {
            Ok(job) => {
                let (eid, jt) = (job.entity_id.clone(), job.r#type.clone());
                if let Err(e) = jobs::dispatch(job).await {
                    tracing::error!(entity_id = %eid, job_type = %jt, "job failed: {:?}", e);
                }
            }
            Err(e) => tracing::error!("malformed job payload: {:?}", e),
        }
        d.ack(BasicAckOptions::default()).await?;
    }
    Ok(())
}
