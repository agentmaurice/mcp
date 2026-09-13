# MCP Document

MCP Document converts office documents to Markdown locally for AgentMaurice
agents. The service uses AnyDoc and exposes an MCP server over STDIO, designed
to run behind the standard MCP sidecar.

## MCP tools

- `document_health_v1`: status of the service and of its configured dependencies;
- `document_capabilities_v1`: formats, limits, network policy and thresholds;
- `document_parse_v1`: conversion of a document to Markdown.

`document_parse_v1` receives the stable `storage://` reference in `source_ref`.
The AgentMaurice gateway duplicates this reference into `source_url`, then
resolves it to a temporary URL immediately before the MCP call: this URL is
neither exposed to the model nor returned in the result. Its host must be
declared in `DOCUMENT_SOURCE_ALLOWED_HOSTS`; without an allowlist, all document
egress is refused.

## Buffer

The source binary stays in Storage. Only the Markdown result uses the Buffer:

- up to 512 KiB: inline response;
- above 512 KiB: stored in the Buffer when configured;
- above 2 MiB: the Buffer becomes mandatory, with no inline fallback.

The response then contains a `buffer://document:...` reference, a preview and
the scope metadata. Thresholds and TTL are configurable through environment
variables.

## Formats

Word, PowerPoint, Excel, OpenDocument, RTF, EPUB, CSV and text-based PDF are
supported. A scanned PDF returns `status=needs_ocr` with the affected pages;
no remote OCR is enabled by this service.

## Configuration

| Variable | Default | Role |
| --- | ---: | --- |
| `DOCUMENT_SOURCE_ALLOWED_HOSTS` | empty | Allowed HTTPS hosts, comma-separated |
| `DOCUMENT_ALLOW_INSECURE_HTTP` | `false` | Allows HTTP for a controlled local environment |
| `DOCUMENT_MAX_INPUT_BYTES` | `67108864` | Maximum size of the source document |
| `DOCUMENT_PARSE_TIMEOUT_SECONDS` | `60` | Maximum download/conversion time |
| `DOCUMENT_BUFFER_SERVICE_URL` | — | `POST /api/v1/buffer` endpoint |
| `DOCUMENT_BUFFER_TOKEN` | — | Bearer token scoped to the `document` namespace |
| `DOCUMENT_BUFFER_NAMESPACE` | `document` | Buffer namespace |
| `DOCUMENT_BUFFER_SOFT_THRESHOLD_BYTES` | `524288` | Recommended threshold |
| `DOCUMENT_BUFFER_HARD_THRESHOLD_BYTES` | `2097152` | Mandatory threshold |
| `DOCUMENT_BUFFER_TTL_SECONDS` | `600` | TTL of the result |

The generic variables `BUFFER_SERVICE_URL`, `BUFFER_TOKEN` and
`BUFFER_NAMESPACE` remain accepted for integration with existing MCP
deployments.

## Development

```bash
task check
task build
```

## License

Apache-2.0. The AnyDoc dependency is distributed under the MIT license.
