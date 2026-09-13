use anyhow::Context;
use mcp_document::{Config, DocumentServer};
use rmcp::{ServiceExt, transport::stdio};
use tracing_subscriber::{EnvFilter, fmt};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    fmt()
        .with_env_filter(EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()))
        .with_writer(std::io::stderr)
        .with_ansi(false)
        .init();

    let config = Config::from_env().context("invalid document service configuration")?;
    let service = DocumentServer::new(config)
        .serve(stdio())
        .await
        .context("failed to start MCP stdio service")?;

    service
        .waiting()
        .await
        .context("MCP stdio service stopped")?;
    Ok(())
}
