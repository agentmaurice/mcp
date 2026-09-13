# AgentMaurice OCR MCP

Managed, synchronous OCR MCP server. The service holds no provider key: it
calls the authenticated bridge of the AgentMaurice instance, which forwards the
request to the hosted LLM Gateway.

Tools:

- `ocr_health_v1`
- `ocr_capabilities_v1`
- `ocr_extract_v1`

`ocr_extract_v1` accepts exactly one `source_ref` or one `source_url`. Page
indexes are zero-based and `include_images` defaults to `false`. Large results
and images are returned as opaque Buffer references.

## License

Apache-2.0.
