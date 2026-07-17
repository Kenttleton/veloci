//! Postgres connection pool factory.
//!
//! The engine maintains two separate `sqlx::PgPool` instances:
//!
//! - **Read pool** (`read_max` connections): used by Stages 1–5 for all query
//!   work. Read-only in practice; no pool-level enforcement.
//! - **Write pool** (`write_max` connections): used by Stage 0 (import dedup +
//!   batch INSERT) and Stage 6 (snapshot UPSERT). Kept small to prevent
//!   write-amplification under concurrent imports.
//!
//! DSNs are always built from config components — never from raw connection
//! strings in environment variables.

use anyhow::{Context, Result};
use backon::{ExponentialBuilder, Retryable};
use sqlx::{
    postgres::{PgConnectOptions, PgPoolOptions},
    PgPool,
};

use crate::config::AppConfig;

/// The two pools the engine uses.
#[derive(Clone, Debug)]
pub struct Pools {
    /// Stages 1–5: read-heavy analysis work.
    pub read: PgPool,
    /// Stages 0 and 6: writes to `raw_transactions` and `computed_snapshots`.
    pub write: PgPool,
}

/// Build both Postgres pools from the application config.
///
/// Pool sizes are taken from `engine.pool.read_max` and `engine.pool.write_max`.
pub async fn connect(cfg: &AppConfig) -> Result<Pools> {
    let dsn = cfg.postgres_dsn();
    let connect_opts: PgConnectOptions = dsn
        .parse()
        .context("failed to parse postgres DSN")?;

    let backoff = ExponentialBuilder::default().with_max_times(10);

    let read = (|| async {
        PgPoolOptions::new()
            .max_connections(cfg.engine.pool.read_max)
            .connect_with(connect_opts.clone())
            .await
    })
    .retry(backoff.clone())
    .await
    .context("failed to connect read pool")?;

    let write = (|| async {
        PgPoolOptions::new()
            .max_connections(cfg.engine.pool.write_max)
            .connect_with(connect_opts.clone())
            .await
    })
    .retry(backoff)
    .await
    .context("failed to connect write pool")?;

    Ok(Pools { read, write })
}

/// Open a single pool from a raw DSN string.
///
/// Used by the health-check subcommand and legacy code paths.
#[allow(dead_code)]
pub async fn connect_single(database_url: &str) -> Result<PgPool> {
    PgPool::connect(database_url)
        .await
        .context("failed to connect to postgres")
}
