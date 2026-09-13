use crate::{
    buffer::{BufferReference, store_markdown},
    config::Config,
    converter::{Conversion, convert},
    source,
};
use anydoc::ConvertError;
use rmcp::{
    Json, ServerHandler,
    handler::server::{router::tool::ToolRouter, wrapper::Parameters},
    model::{Implementation, ServerCapabilities, ServerInfo},
    tool, tool_handler, tool_router,
};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use serde_json::json;
use tokio::time::timeout;

const SERVICE_VERSION: &str = env!("CARGO_PKG_VERSION");
const PARSER_VERSION: &str = "anydoc/0.2.4";
const FORMATS: &[&str] = &[
    "doc", "docx", "docm", "ppt", "pps", "pot", "pptx", "pptm", "ppsx", "ppsm", "xls", "xlsx",
    "xlsm", "xlsb", "odt", "ods", "odp", "rtf", "epub", "csv", "pdf",
];

#[derive(Debug, Deserialize, JsonSchema)]
pub struct ParseRequest {
    #[schemars(description = "Stable provenance reference, normally storage://file/<path>")]
    pub source_ref: String,
    #[schemars(
        description = "Source locator resolved by the AgentMaurice gateway: pass source_ref again for storage:// inputs; the service receives an allowlisted temporary HTTPS URL"
    )]
    pub source_url: Option<String>,
    #[schemars(description = "Original filename; required for signature-less formats such as CSV")]
    pub filename: Option<String>,
    #[schemars(description = "Original MIME type, retained as metadata")]
    pub mime_type: Option<String>,
    #[schemars(description = "AgentMaurice deployment scope")]
    pub deployment_id: String,
    #[schemars(description = "Optional organization scope")]
    pub organization_id: Option<String>,
    #[schemars(description = "Optional workspace/space scope")]
    pub space_id: Option<String>,
    #[schemars(description = "Optional user scope")]
    pub user_id: Option<String>,
}

#[derive(Debug, JsonSchema, Serialize)]
pub struct HealthResponse {
    pub status: &'static str,
    pub service: &'static str,
    pub version: &'static str,
    pub parser: &'static str,
    pub buffer_configured: bool,
    pub source_egress_enabled: bool,
}

#[derive(Debug, JsonSchema, Serialize)]
pub struct CapabilitiesResponse {
    pub service: &'static str,
    pub version: &'static str,
    pub parser: &'static str,
    pub output: &'static str,
    pub supported_extensions: &'static [&'static str],
    pub max_input_bytes: usize,
    pub parse_timeout_seconds: u64,
    pub buffer_soft_threshold_bytes: usize,
    pub buffer_hard_threshold_bytes: usize,
    pub scanned_pdf_ocr: &'static str,
    pub source_policy: &'static str,
}

#[derive(Debug, JsonSchema, Serialize)]
pub struct ParseResponse {
    pub status: &'static str,
    pub source_ref: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub filename: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_mime_type: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub format: Option<String>,
    pub output_mime_type: &'static str,
    pub parser: &'static str,
    pub input_bytes: usize,
    pub output_bytes: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sha256: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub markdown: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub buffer_uri: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub buffer_ref: Option<BufferReference>,
    pub preview: String,
    pub warnings: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ocr_pages: Option<Vec<u32>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page_count: Option<u32>,
}

#[derive(Debug, Clone)]
pub struct DocumentServer {
    config: Config,
    tool_router: ToolRouter<Self>,
}

impl DocumentServer {
    pub fn new(config: Config) -> Self {
        Self {
            config,
            tool_router: Self::tool_router(),
        }
    }

    async fn parse_document(&self, request: ParseRequest) -> Result<ParseResponse, String> {
        validate_request(&request)?;
        let bytes = source::fetch(
            &self.config,
            request.source_ref.trim(),
            request.source_url.as_deref(),
        )
        .await
        .map_err(tool_error)?;
        let input_bytes = bytes.len();
        let filename_for_parse = request.filename.clone();
        let parse_timeout = self.config.parse_timeout;
        let parse =
            tokio::task::spawn_blocking(move || convert(&bytes, filename_for_parse.as_deref()));
        let conversion = timeout(parse_timeout, parse)
            .await
            .map_err(|_| {
                "document_parse_timeout: document conversion exceeded its time limit".to_string()
            })?
            .map_err(|error| format!("document_parse_join_error: {error}"))?;

        match conversion {
            Ok(conversion) => {
                self.build_success_response(request, input_bytes, conversion)
                    .await
            }
            Err(ConvertError::NeedsOcr { pages, page_count }) => Ok(ParseResponse {
                status: "needs_ocr",
                source_ref: request.source_ref,
                filename: request.filename,
                source_mime_type: request.mime_type,
                format: Some("pdf".into()),
                output_mime_type: "text/markdown",
                parser: PARSER_VERSION,
                input_bytes,
                output_bytes: 0,
                sha256: None,
                markdown: None,
                buffer_uri: None,
                buffer_ref: None,
                preview: String::new(),
                warnings: vec![
                    "The PDF contains scanned or image-only pages; local OCR is not enabled."
                        .into(),
                ],
                ocr_pages: Some(pages),
                page_count: Some(page_count),
            }),
            Err(error) => Err(format!("document_parse_{}: {error}", error.code())),
        }
    }

    async fn build_success_response(
        &self,
        request: ParseRequest,
        input_bytes: usize,
        conversion: Conversion,
    ) -> Result<ParseResponse, String> {
        let output_bytes = conversion.markdown.len();
        let preview = preview(&conversion.markdown, 600);
        let summary = request
            .filename
            .as_deref()
            .map(|name| format!("Parsed document {name}"))
            .unwrap_or_else(|| "Parsed document".into());
        let metadata = json!({
            "source_ref": request.source_ref,
            "source_mime_type": request.mime_type,
            "filename": request.filename,
            "format": conversion.format,
            "sha256": conversion.sha256,
            "parser": PARSER_VERSION,
            "organization_id": request.organization_id,
            "deployment_id": request.deployment_id,
            "space_id": request.space_id,
            "user_id": request.user_id,
        });

        let should_buffer = output_bytes > self.config.buffer_soft_threshold_bytes;
        let must_buffer = output_bytes > self.config.buffer_hard_threshold_bytes;
        let mut warnings = Vec::new();
        let mut markdown = Some(conversion.markdown);
        let mut buffer_ref = None;
        let mut buffer_uri = None;

        if should_buffer {
            if self.config.buffer_configured() {
                let content = markdown.as_deref().unwrap_or_default();
                match store_markdown(&self.config, content, &summary, metadata).await {
                    Ok(reference) => {
                        buffer_uri = Some(reference.uri());
                        buffer_ref = Some(reference);
                        markdown = None;
                    }
                    Err(error) if must_buffer => {
                        return Err(format!("document_buffer_required: {error}"));
                    }
                    Err(error) => warnings.push(format!(
                        "Buffer storage failed below the hard threshold; content was returned inline: {error}"
                    )),
                }
            } else if must_buffer {
                return Err(
                    "document_buffer_required: parsed content exceeds the hard threshold but Buffer is not configured"
                        .into(),
                );
            } else {
                warnings.push(
                    "Parsed content exceeds the soft threshold but Buffer is not configured; content was returned inline."
                        .into(),
                );
            }
        }

        Ok(ParseResponse {
            status: "ready",
            source_ref: request.source_ref,
            filename: request.filename,
            source_mime_type: request.mime_type,
            format: Some(conversion.format.into()),
            output_mime_type: "text/markdown",
            parser: PARSER_VERSION,
            input_bytes,
            output_bytes,
            sha256: Some(conversion.sha256),
            markdown,
            buffer_uri,
            buffer_ref,
            preview,
            warnings,
            ocr_pages: None,
            page_count: None,
        })
    }
}

#[tool_router(router = tool_router)]
impl DocumentServer {
    #[tool(
        name = "document_health_v1",
        description = "Check the MCP Document service and its local parser configuration"
    )]
    async fn health(&self) -> Json<HealthResponse> {
        Json(HealthResponse {
            status: "healthy",
            service: "mcp-document",
            version: SERVICE_VERSION,
            parser: PARSER_VERSION,
            buffer_configured: self.config.buffer_configured(),
            source_egress_enabled: !self.config.allowed_source_hosts.is_empty(),
        })
    }

    #[tool(
        name = "document_capabilities_v1",
        description = "List supported document formats, safety limits, OCR behavior, and Buffer thresholds"
    )]
    async fn capabilities(&self) -> Json<CapabilitiesResponse> {
        Json(CapabilitiesResponse {
            service: "mcp-document",
            version: SERVICE_VERSION,
            parser: PARSER_VERSION,
            output: "GitHub-Flavored Markdown",
            supported_extensions: FORMATS,
            max_input_bytes: self.config.max_input_bytes,
            parse_timeout_seconds: self.config.parse_timeout.as_secs(),
            buffer_soft_threshold_bytes: self.config.buffer_soft_threshold_bytes,
            buffer_hard_threshold_bytes: self.config.buffer_hard_threshold_bytes,
            scanned_pdf_ocr: "not_enabled; returns status=needs_ocr with affected pages",
            source_policy: "HTTPS only, no redirects, exact host allowlist, no IP literals",
        })
    }

    #[tool(
        name = "document_parse_v1",
        description = "Convert an uploaded office document, EPUB, CSV, or text-based PDF to Markdown. Pass storage_uri as source_ref; the AgentMaurice gateway resolves the temporary source_url immediately before execution."
    )]
    async fn parse(
        &self,
        Parameters(request): Parameters<ParseRequest>,
    ) -> Result<Json<ParseResponse>, String> {
        self.parse_document(request).await.map(Json)
    }
}

#[tool_handler(router = self.tool_router)]
impl ServerHandler for DocumentServer {
    fn get_info(&self) -> ServerInfo {
        ServerInfo::new(ServerCapabilities::builder().enable_tools().build())
            .with_server_info(Implementation::new("mcp-document", SERVICE_VERSION))
            .with_instructions(
                "Use document_parse_v1 for office documents, EPUB, CSV, and text-based PDFs. Keep source_ref as the stable storage:// provenance; the AgentMaurice gateway resolves source_url without exposing the temporary signed URL to the model. Large Markdown results are returned as buffer:// references. Scanned PDFs return needs_ocr and must be routed to an OCR-capable service.",
            )
    }
}

fn validate_request(request: &ParseRequest) -> Result<(), String> {
    if request.source_ref.trim().is_empty() {
        return Err("document_invalid_request: source_ref is required".into());
    }
    if request.deployment_id.trim().is_empty() {
        return Err("document_invalid_request: deployment_id is required".into());
    }
    Ok(())
}

fn tool_error(error: anyhow::Error) -> String {
    format!("document_source_error: {error:#}")
}

fn preview(value: &str, limit: usize) -> String {
    value.chars().take(limit).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{collections::HashSet, time::Duration};

    fn config(soft_threshold: usize, hard_threshold: usize) -> Config {
        Config {
            max_input_bytes: 1024,
            parse_timeout: Duration::from_secs(1),
            allowed_source_hosts: HashSet::new(),
            allow_insecure_http: false,
            buffer_service_url: None,
            buffer_token: None,
            buffer_namespace: "document".into(),
            buffer_soft_threshold_bytes: soft_threshold,
            buffer_hard_threshold_bytes: hard_threshold,
            buffer_ttl_seconds: 600,
        }
    }

    fn request() -> ParseRequest {
        ParseRequest {
            source_ref: "storage://file/report.csv".into(),
            source_url: None,
            filename: Some("report.csv".into()),
            mime_type: Some("text/csv".into()),
            deployment_id: "deployment-1".into(),
            organization_id: Some("organization-1".into()),
            space_id: Some("space-1".into()),
            user_id: Some("user-1".into()),
        }
    }

    fn conversion(markdown: &str) -> Conversion {
        Conversion {
            markdown: markdown.into(),
            format: "csv",
            sha256: "a".repeat(64),
        }
    }

    #[test]
    fn preview_does_not_split_utf8() {
        assert_eq!(preview("éclair", 1), "é");
    }

    #[test]
    fn deployment_scope_is_required() {
        let request = ParseRequest {
            deployment_id: String::new(),
            ..request()
        };
        assert!(validate_request(&request).is_err());
    }

    #[tokio::test]
    async fn keeps_small_results_inline() {
        let server = DocumentServer::new(config(10, 20));
        let response = server
            .build_success_response(request(), 5, conversion("short"))
            .await
            .unwrap();

        assert_eq!(response.markdown.as_deref(), Some("short"));
        assert!(response.buffer_ref.is_none());
        assert!(response.warnings.is_empty());
    }

    #[tokio::test]
    async fn warns_when_soft_threshold_cannot_use_buffer() {
        let server = DocumentServer::new(config(10, 20));
        let response = server
            .build_success_response(request(), 11, conversion("eleven bytes"))
            .await
            .unwrap();

        assert!(response.markdown.is_some());
        assert!(response.buffer_ref.is_none());
        assert_eq!(response.warnings.len(), 1);
    }

    #[tokio::test]
    async fn refuses_inline_fallback_above_hard_threshold() {
        let server = DocumentServer::new(config(10, 20));
        let error = server
            .build_success_response(request(), 21, conversion("content beyond twenty"))
            .await
            .unwrap_err();

        assert!(error.starts_with("document_buffer_required:"));
    }
}
