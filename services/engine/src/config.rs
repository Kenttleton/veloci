//! Layered configuration for the Veloci engine.
//!
//! Config is loaded in two layers (later layers override earlier ones):
//!
//! 1. TOML file at the path given by `VELOCI_CONFIG_PATH` env var, defaulting
//!    to `config/veloci.toml`.
//! 2. Environment variables with the `VELOCI_` prefix; path separators are
//!    single underscores (e.g. `VELOCI_DATABASE_APP_PASSWORD`).
//!
//! DSNs are built in code from components — no raw connection strings are
//! accepted in the config file or environment.

use anyhow::{Context, Result};
use config::{Config, Environment, File, FileFormat};
use serde::Deserialize;

// ---------------------------------------------------------------------------
// Config key: engine.pool.read_max / write_max defaults
// ---------------------------------------------------------------------------
fn default_read_max() -> u32 {
    12
}

fn default_write_max() -> u32 {
    4
}

fn default_import_concurrency() -> usize {
    4
}

fn default_snapshot_chunk_size() -> usize {
    500
}

fn default_db_host() -> String {
    "localhost".into()
}

fn default_db_port() -> u16 {
    5432
}

fn default_db_name() -> String {
    "veloci_app".into()
}

fn default_db_user() -> String {
    "veloci_app_user".into()
}

fn default_db_password() -> String {
    "changeme".into()
}

fn default_mq_host() -> String {
    "localhost".into()
}

fn default_mq_port() -> u16 {
    5672
}

fn default_mq_user() -> String {
    "guest".into()
}

fn default_mq_password() -> String {
    "guest".into()
}

// ---------------------------------------------------------------------------
// Sub-structs
// ---------------------------------------------------------------------------

/// `[database]` section.
#[derive(Debug, Deserialize, Clone)]
pub struct DatabaseConfig {
    #[serde(default = "default_db_host")]
    pub host: String,
    #[serde(default = "default_db_port")]
    pub port: u16,
    #[serde(default)]
    pub app: DatabaseAppConfig,
}

/// `[database.app]` section.
#[derive(Debug, Deserialize, Clone, Default)]
pub struct DatabaseAppConfig {
    #[serde(default = "default_db_name")]
    pub name: String,
    #[serde(default = "default_db_user")]
    pub user: String,
    #[serde(default = "default_db_password")]
    pub password: String,
}

impl Default for DatabaseConfig {
    fn default() -> Self {
        Self {
            host: default_db_host(),
            port: default_db_port(),
            app: DatabaseAppConfig::default(),
        }
    }
}

/// `[rabbitmq]` section.
#[derive(Debug, Deserialize, Clone)]
pub struct RabbitMqConfig {
    #[serde(default = "default_mq_host")]
    pub host: String,
    #[serde(default = "default_mq_port")]
    pub port: u16,
    #[serde(default = "default_mq_user")]
    pub user: String,
    #[serde(default = "default_mq_password")]
    pub password: String,
}

impl Default for RabbitMqConfig {
    fn default() -> Self {
        Self {
            host: default_mq_host(),
            port: default_mq_port(),
            user: default_mq_user(),
            password: default_mq_password(),
        }
    }
}

/// `[engine.pool]` section.
#[derive(Debug, Deserialize, Clone)]
pub struct PoolConfig {
    #[serde(default = "default_read_max")]
    pub read_max: u32,
    #[serde(default = "default_write_max")]
    pub write_max: u32,
}

impl Default for PoolConfig {
    fn default() -> Self {
        Self {
            read_max: default_read_max(),
            write_max: default_write_max(),
        }
    }
}

/// `[engine.pipeline]` section.
#[derive(Debug, Deserialize, Clone)]
#[allow(dead_code)]
pub struct PipelineConfig {
    #[serde(default = "default_import_concurrency")]
    pub import_concurrency: usize,
    #[serde(default = "default_snapshot_chunk_size")]
    pub snapshot_chunk_size: usize,
}

impl Default for PipelineConfig {
    fn default() -> Self {
        Self {
            import_concurrency: default_import_concurrency(),
            snapshot_chunk_size: default_snapshot_chunk_size(),
        }
    }
}

/// `[engine]` section.
#[derive(Debug, Deserialize, Clone, Default)]
#[allow(dead_code)]
pub struct EngineConfig {
    #[serde(default)]
    pub pool: PoolConfig,
    #[serde(default)]
    pub pipeline: PipelineConfig,
}

/// Root configuration struct.
///
/// # Example
///
/// ```no_run
/// # use veloci_engine::config::AppConfig;
/// let cfg = AppConfig::load().expect("failed to load config");
/// println!("DB host: {}", cfg.database.host);
/// ```
#[derive(Debug, Deserialize, Clone, Default)]
pub struct AppConfig {
    #[serde(default)]
    pub database: DatabaseConfig,
    #[serde(default)]
    pub rabbitmq: RabbitMqConfig,
    #[serde(default)]
    pub engine: EngineConfig,
}

impl AppConfig {
    /// Load configuration from the TOML file and environment variables.
    ///
    /// The config file path is read from `VELOCI_CONFIG_PATH` (NOT through
    /// config-rs, to avoid chicken-and-egg dependency). Defaults to
    /// `config/veloci.toml`.
    ///
    /// Environment variables override file values using `VELOCI_` prefix with
    /// single underscore separators (e.g. `VELOCI_DATABASE_APP_PASSWORD`).
    pub fn load() -> Result<Self> {
        let config_path = std::env::var("VELOCI_CONFIG_PATH")
            .unwrap_or_else(|_| "config/veloci.toml".to_string());

        let cfg = Config::builder()
            // Layer 1: TOML file (optional — missing file is not an error)
            .add_source(
                File::new(&config_path, FileFormat::Toml).required(false),
            )
            // Layer 2: env vars with VELOCI_ prefix, single-underscore separator
            .add_source(
                Environment::with_prefix("VELOCI")
                    .separator("_")
                    .try_parsing(true),
            )
            .build()
            .context("failed to build configuration")?;

        cfg.try_deserialize::<AppConfig>()
            .context("failed to deserialize configuration")
    }

    /// Build the Postgres DSN from config components.
    ///
    /// Format: `postgres://user:password@host:port/dbname`
    ///
    /// # Example
    ///
    /// ```
    /// # use veloci_engine::config::{AppConfig, DatabaseConfig, DatabaseAppConfig};
    /// let mut cfg = AppConfig::default();
    /// cfg.database.host = "db.example.com".into();
    /// cfg.database.port = 5432;
    /// cfg.database.app.name = "veloci_app".into();
    /// cfg.database.app.user = "veloci_app_user".into();
    /// cfg.database.app.password = "secret".into();
    /// let dsn = cfg.postgres_dsn();
    /// assert_eq!(dsn, "postgres://veloci_app_user:secret@db.example.com:5432/veloci_app");
    /// ```
    #[must_use]
    pub fn postgres_dsn(&self) -> String {
        format!(
            "postgres://{}:{}@{}:{}/{}",
            self.database.app.user,
            self.database.app.password,
            self.database.host,
            self.database.port,
            self.database.app.name,
        )
    }

    /// Build the RabbitMQ AMQP URI from config components.
    ///
    /// Format: `amqp://user:password@host:port/%2f`
    ///
    /// # Example
    ///
    /// ```
    /// # use veloci_engine::config::{AppConfig, RabbitMqConfig};
    /// let mut cfg = AppConfig::default();
    /// cfg.rabbitmq.host = "mq.example.com".into();
    /// cfg.rabbitmq.port = 5672;
    /// cfg.rabbitmq.user = "guest".into();
    /// cfg.rabbitmq.password = "guest".into();
    /// let uri = cfg.amqp_uri();
    /// assert_eq!(uri, "amqp://guest:guest@mq.example.com:5672/%2f");
    /// ```
    #[must_use]
    pub fn amqp_uri(&self) -> String {
        format!(
            "amqp://{}:{}@{}:{}/%2f",
            self.rabbitmq.user,
            self.rabbitmq.password,
            self.rabbitmq.host,
            self.rabbitmq.port,
        )
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn default_cfg() -> AppConfig {
        AppConfig::default()
    }

    #[test]
    fn postgres_dsn_format() {
        let cfg = default_cfg();
        let dsn = cfg.postgres_dsn();
        assert!(dsn.starts_with("postgres://"), "DSN scheme wrong: {dsn}");
        assert!(dsn.contains('@'), "DSN missing @: {dsn}");
    }

    #[test]
    fn amqp_uri_format() {
        let cfg = default_cfg();
        let uri = cfg.amqp_uri();
        assert!(uri.starts_with("amqp://"), "URI scheme wrong: {uri}");
        assert!(uri.ends_with("/%2f"), "URI missing vhost: {uri}");
    }

    #[test]
    fn postgres_dsn_embeds_components() {
        let mut cfg = AppConfig::default();
        cfg.database.host = "db.example.com".into();
        cfg.database.port = 5433;
        cfg.database.app.name = "mydb".into();
        cfg.database.app.user = "myuser".into();
        cfg.database.app.password = "mypass".into();
        let dsn = cfg.postgres_dsn();
        assert_eq!(
            dsn,
            "postgres://myuser:mypass@db.example.com:5433/mydb"
        );
    }

    #[test]
    fn amqp_uri_embeds_components() {
        let mut cfg = AppConfig::default();
        cfg.rabbitmq.host = "mq.example.com".into();
        cfg.rabbitmq.port = 5673;
        cfg.rabbitmq.user = "admin".into();
        cfg.rabbitmq.password = "secret".into();
        let uri = cfg.amqp_uri();
        assert_eq!(uri, "amqp://admin:secret@mq.example.com:5673/%2f");
    }

    #[test]
    fn default_pool_config() {
        let cfg = AppConfig::default();
        assert_eq!(cfg.engine.pool.read_max, 12);
        assert_eq!(cfg.engine.pool.write_max, 4);
    }

    #[test]
    fn default_pipeline_config() {
        let cfg = AppConfig::default();
        assert_eq!(cfg.engine.pipeline.import_concurrency, 4);
        assert_eq!(cfg.engine.pipeline.snapshot_chunk_size, 500);
    }

    #[test]
    fn load_falls_back_to_defaults_when_no_file() {
        // Point to a nonexistent file — should not error, should use defaults.
        std::env::set_var("VELOCI_CONFIG_PATH", "/nonexistent/veloci.toml");
        let result = AppConfig::load();
        std::env::remove_var("VELOCI_CONFIG_PATH");
        assert!(result.is_ok(), "load() should not fail on missing file");
        let cfg = result.unwrap();
        assert_eq!(cfg.database.host, "localhost");
        assert_eq!(cfg.rabbitmq.port, 5672);
    }
}
