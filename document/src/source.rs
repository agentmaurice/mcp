use crate::config::Config;
use anyhow::{Context, bail};
use bytes::Bytes;
use futures_util::StreamExt;
use reqwest::{Client, redirect::Policy};
use url::{Host, Url};

pub async fn fetch(
    config: &Config,
    source_ref: &str,
    source_url: Option<&str>,
) -> anyhow::Result<Bytes> {
    let raw_url = source_url
        .filter(|value| !value.trim().is_empty())
        .or_else(|| source_ref.starts_with("https://").then_some(source_ref))
        .or_else(|| source_ref.starts_with("http://").then_some(source_ref))
        .context("storage:// references require the associated source_url")?;

    let url = Url::parse(raw_url).context("source_url is not a valid URL")?;
    validate_url(config, &url)?;

    let client = Client::builder()
        .redirect(Policy::none())
        .timeout(config.parse_timeout)
        .build()
        .context("build source HTTP client")?;
    let response = client
        .get(url)
        .send()
        .await
        .context("download source document")?
        .error_for_status()
        .context("source document returned an error status")?;

    if response
        .content_length()
        .is_some_and(|length| length > config.max_input_bytes as u64)
    {
        bail!("source document exceeds {} bytes", config.max_input_bytes);
    }

    let mut body = Vec::with_capacity(
        response
            .content_length()
            .unwrap_or_default()
            .min(config.max_input_bytes as u64) as usize,
    );
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.context("read source document body")?;
        if body.len().saturating_add(chunk.len()) > config.max_input_bytes {
            bail!("source document exceeds {} bytes", config.max_input_bytes);
        }
        body.extend_from_slice(&chunk);
    }
    Ok(Bytes::from(body))
}

fn validate_url(config: &Config, url: &Url) -> anyhow::Result<()> {
    match url.scheme() {
        "https" => {}
        "http" if config.allow_insecure_http => {}
        "http" => bail!("insecure HTTP sources are disabled"),
        _ => bail!("source_url must use HTTPS"),
    }
    if !url.username().is_empty() || url.password().is_some() {
        bail!("source_url must not contain credentials");
    }
    let host = match url.host() {
        Some(Host::Domain(host)) => host.trim_end_matches('.').to_ascii_lowercase(),
        Some(Host::Ipv4(_)) | Some(Host::Ipv6(_)) => {
            bail!("IP literal source hosts are not allowed")
        }
        None => bail!("source_url has no host"),
    };
    if !config.allowed_source_hosts.contains(&host) {
        bail!("source host {host} is not allowlisted");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{collections::HashSet, time::Duration};

    fn config() -> Config {
        Config {
            max_input_bytes: 1024,
            parse_timeout: Duration::from_secs(1),
            allowed_source_hosts: HashSet::from(["storage.example.com".into()]),
            allow_insecure_http: false,
            buffer_service_url: None,
            buffer_token: None,
            buffer_namespace: "document".into(),
            buffer_soft_threshold_bytes: 512,
            buffer_hard_threshold_bytes: 1024,
            buffer_ttl_seconds: 600,
        }
    }

    #[test]
    fn accepts_allowlisted_https_host() {
        let url = Url::parse("https://storage.example.com/report.docx").unwrap();
        validate_url(&config(), &url).unwrap();
    }

    #[test]
    fn rejects_redirect_targets_and_ip_literals_before_fetch() {
        let url = Url::parse("https://127.0.0.1/report.docx").unwrap();
        assert!(validate_url(&config(), &url).is_err());
    }

    #[test]
    fn rejects_unlisted_hosts() {
        let url = Url::parse("https://example.net/report.docx").unwrap();
        assert!(validate_url(&config(), &url).is_err());
    }
}
