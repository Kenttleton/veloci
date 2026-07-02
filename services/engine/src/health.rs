use anyhow::Result;

/// Verify connectivity to both Postgres and RabbitMQ, then print `"ok"` and
/// return.  Exits with a descriptive error if either connection fails.
///
/// Intended for use as `veloci-engine health` in Docker health-check scripts
/// and Kubernetes liveness probes.
pub async fn check(database_url: &str, rabbitmq_url: &str) -> Result<()> {
    let _pool = sqlx::PgPool::connect(database_url)
        .await
        .map_err(|e| anyhow::anyhow!("postgres: {}", e))?;

    let _conn = lapin::Connection::connect(rabbitmq_url, lapin::ConnectionProperties::default())
        .await
        .map_err(|e| anyhow::anyhow!("rabbitmq: {}", e))?;

    println!("ok");
    Ok(())
}
