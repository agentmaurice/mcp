use anyhow::{Context, bail};
use std::{collections::HashSet, env, time::Duration};

pub const DEFAULT_MAX_INPUT_BYTES: usize = 64 * 1024 * 1024;
pub const DEFAULT_SOFT_THRESHOLD_BYTES: usize = 512 * 1024;
pub const DEFAULT_HARD_THRESHOLD_BYTES: usize = 2 * 1024 * 1024;
pub const DEFAULT_BUFFER_TTL_SECONDS: u64 = 600;

#[derive(Clone, Debug)]
pub struct Config {
    pub max_input_bytes: usize,
    pub parse_timeout: Duration,
    pub allowed_source_hosts: HashSet<String>,
    pub allow_insecure_http: bool,
    pub buffer_service_url: Option<String>,
    pub buffer_token: Option<String>,
    pub buffer_namespace: String,
    pub buffer_soft_threshold_bytes: usize,
    pub buffer_hard_threshold_bytes: usize,
    pub buffer_ttl_seconds: u64,
}

impl Config {
    pub fn from_env() -> anyhow::Result<Self> {
        let config = Self {
            max_input_bytes: parse_usize("DOCUMENT_MAX_INPUT_BYTES", DEFAULT_MAX_INPUT_BYTES)?,
            parse_timeout: Duration::from_secs(parse_u64("DOCUMENT_PARSE_TIMEOUT_SECONDS", 60)?),
            allowed_source_hosts: parse_hosts(env::var("DOCUMENT_SOURCE_ALLOWED_HOSTS").ok()),
            allow_insecure_http: parse_bool("DOCUMENT_ALLOW_INSECURE_HTTP", false)?,
            buffer_service_url: first_env(&["DOCUMENT_BUFFER_SERVICE_URL", "BUFFER_SERVICE_URL"]),
            buffer_token: first_env(&["DOCUMENT_BUFFER_TOKEN", "BUFFER_TOKEN"]),
            buffer_namespace: first_env(&["DOCUMENT_BUFFER_NAMESPACE", "BUFFER_NAMESPACE"])
                .unwrap_or_else(|| "document".to_string()),
            buffer_soft_threshold_bytes: parse_usize(
                "DOCUMENT_BUFFER_SOFT_THRESHOLD_BYTES",
                DEFAULT_SOFT_THRESHOLD_BYTES,
            )?,
            buffer_hard_threshold_bytes: parse_usize(
                "DOCUMENT_BUFFER_HARD_THRESHOLD_BYTES",
                DEFAULT_HARD_THRESHOLD_BYTES,
            )?,
            buffer_ttl_seconds: parse_u64(
                "DOCUMENT_BUFFER_TTL_SECONDS",
                DEFAULT_BUFFER_TTL_SECONDS,
            )?,
        };
        config.validate()?;
        Ok(config)
    }

    pub fn buffer_configured(&self) -> bool {
        self.buffer_service_url.is_some() && self.buffer_token.is_some()
    }

    fn validate(&self) -> anyhow::Result<()> {
        if self.max_input_bytes == 0 {
            bail!("DOCUMENT_MAX_INPUT_BYTES must be greater than zero");
        }
        if self.parse_timeout.is_zero() {
            bail!("DOCUMENT_PARSE_TIMEOUT_SECONDS must be greater than zero");
        }
        if self.buffer_soft_threshold_bytes == 0 {
            bail!("DOCUMENT_BUFFER_SOFT_THRESHOLD_BYTES must be greater than zero");
        }
        if self.buffer_hard_threshold_bytes < self.buffer_soft_threshold_bytes {
            bail!("document buffer hard threshold must be greater than or equal to soft threshold");
        }
        if self.buffer_service_url.is_some() != self.buffer_token.is_some() {
            bail!("buffer URL and token must be configured together");
        }
        Ok(())
    }
}

fn first_env(names: &[&str]) -> Option<String> {
    names
        .iter()
        .filter_map(|name| env::var(name).ok())
        .map(|value| value.trim().to_string())
        .find(|value| !value.is_empty())
}

fn parse_hosts(raw: Option<String>) -> HashSet<String> {
    raw.unwrap_or_default()
        .split(',')
        .map(|host| host.trim().trim_end_matches('.').to_ascii_lowercase())
        .filter(|host| !host.is_empty())
        .collect()
}

fn parse_usize(name: &str, default: usize) -> anyhow::Result<usize> {
    match env::var(name) {
        Ok(value) if !value.trim().is_empty() => value
            .trim()
            .parse::<usize>()
            .with_context(|| format!("{name} must be an unsigned integer")),
        _ => Ok(default),
    }
}

fn parse_u64(name: &str, default: u64) -> anyhow::Result<u64> {
    match env::var(name) {
        Ok(value) if !value.trim().is_empty() => value
            .trim()
            .parse::<u64>()
            .with_context(|| format!("{name} must be an unsigned integer")),
        _ => Ok(default),
    }
}

fn parse_bool(name: &str, default: bool) -> anyhow::Result<bool> {
    match env::var(name) {
        Ok(value) => match value.trim().to_ascii_lowercase().as_str() {
            "1" | "true" | "yes" | "on" => Ok(true),
            "0" | "false" | "no" | "off" => Ok(false),
            _ => bail!("{name} must be a boolean"),
        },
        Err(_) => Ok(default),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_source_host_allowlist() {
        let hosts = parse_hosts(Some("storage.example.com, CDN.EXAMPLE.COM. ,".into()));
        assert!(hosts.contains("storage.example.com"));
        assert!(hosts.contains("cdn.example.com"));
        assert_eq!(hosts.len(), 2);
    }
}
