mod config;
mod consumer;
mod db;
mod health;
mod jobs;
mod pipeline;

use anyhow::Result;
use config::AppConfig;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    let cfg = AppConfig::load()?;

    match std::env::args().nth(1).as_deref() {
        Some("health") => {
            health::check(&cfg.postgres_dsn(), &cfg.amqp_uri()).await
        }
        _ => {
            let pools = db::connect(&cfg).await?;
            tracing::info!("connected to postgres (read + write pools)");
            consumer::run(&cfg, pools).await
        }
    }
}
