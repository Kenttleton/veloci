mod consumer;
mod db;
mod health;
mod jobs;

use anyhow::Result;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let db_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required");
    let mq_url = std::env::var("RABBITMQ_URL").expect("RABBITMQ_URL required");

    match std::env::args().nth(1).as_deref() {
        Some("health") => health::check(&db_url, &mq_url).await,
        _ => {
            let _pool = db::connect(&db_url).await?;
            tracing::info!("connected to postgres");
            consumer::run(&mq_url).await
        }
    }
}
