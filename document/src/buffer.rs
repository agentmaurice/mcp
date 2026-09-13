use crate::config::Config;
use anyhow::Context;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

#[derive(Clone, Debug, Deserialize, JsonSchema, Serialize)]
pub struct BufferReference {
    #[serde(rename = "type")]
    pub reference_type: String,
    pub key: String,
    pub size: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preview: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expires_in: Option<i64>,
}

impl BufferReference {
    pub fn uri(&self) -> String {
        format!("buffer://{}", self.key)
    }
}

#[derive(Serialize)]
struct StoreRequest<'a> {
    namespace: &'a str,
    content: Value,
    mime_type: &'static str,
    ttl_seconds: u64,
    summary: &'a str,
    tool_name: &'static str,
    text: &'a str,
    metadata: Value,
}

#[derive(Deserialize)]
struct StoreResponse {
    reference: BufferReference,
}

pub async fn store_markdown(
    config: &Config,
    markdown: &str,
    summary: &str,
    metadata: Value,
) -> anyhow::Result<BufferReference> {
    let service_url = config
        .buffer_service_url
        .as_deref()
        .context("buffer service URL is not configured")?;
    let token = config
        .buffer_token
        .as_deref()
        .context("buffer token is not configured")?;
    let body = StoreRequest {
        namespace: &config.buffer_namespace,
        content: json!({"kind": "document_markdown", "version": "v1"}),
        mime_type: "text/markdown",
        ttl_seconds: config.buffer_ttl_seconds,
        summary,
        tool_name: "document_parse_v1",
        text: markdown,
        metadata,
    };
    let response = reqwest::Client::builder()
        .timeout(config.parse_timeout)
        .build()
        .context("build buffer HTTP client")?
        .post(service_url.trim_end_matches('/'))
        .bearer_auth(token)
        .json(&body)
        .send()
        .await
        .context("store parsed document in buffer")?
        .error_for_status()
        .context("buffer service returned an error status")?
        .json::<StoreResponse>()
        .await
        .context("decode buffer store response")?;
    Ok(response.reference)
}
