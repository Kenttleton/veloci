use anyhow::Result;
use sqlx::PgPool;

/// Open a `PgPool` connected to `database_url`.
///
/// `sqlx` validates the URL format eagerly; network errors surface here.
pub async fn connect(database_url: &str) -> Result<PgPool> {
    Ok(PgPool::connect(database_url).await?)
}
