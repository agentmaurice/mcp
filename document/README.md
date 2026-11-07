# MCP Document

MCP Document converts office documents to Markdown locally for AgentMaurice
agents. The service uses AnyDoc and exposes an MCP server over STDIO, designed
to run behind the standard MCP sidecar.

## When to use this server

Use Document to read one uploaded office file or PDF as Markdown before an
agent summarizes, compares or extracts its contents. Conversion runs locally;
no LLM API key is required. Use RAG for persistent indexing and retrieval across
a corpus, and an OCR service when Document returns `needs_ocr`. Document does
not create office files or perform OCR itself.

## Run with One

Requires Docker with `linux/amd64` support (emulation on Apple Silicon), a
running One instance and its administrator CLI context. The release image is
available as `ghcr.io/agentmaurice/mcp/document-sidecar:v0.1.1`, digest
`sha256:e1ffca334be0f54678f708c99ce71872c01935470c65743f34fa534b08b895c9`.
If the GHCR package is private, authenticate Docker with a GitHub account that
has package access and a token with `read:packages`. `unauthorized` during pull
is an image-access failure, before the MCP starts.

Pull the pinned image first:

```sh
docker pull --platform linux/amd64 ghcr.io/agentmaurice/mcp/document-sidecar@sha256:e1ffca334be0f54678f708c99ce71872c01935470c65743f34fa534b08b895c9
maurice test setup --fresh --save=false --deployment-name document-check --json
```

Keep the returned `agent_id` and `agent_name`. Save this as `document-mcp.json`,
replacing `storage.example.com` with the exact host that serves your source:

```json
{
  "name": "document",
  "command": "docker",
  "arguments": [
    "run", "--rm", "-i", "--platform", "linux/amd64",
    "--entrypoint", "/usr/local/bin/mcp-document",
    "-e", "DOCUMENT_SOURCE_ALLOWED_HOSTS=storage.example.com",
    "ghcr.io/agentmaurice/mcp/document-sidecar@sha256:e1ffca334be0f54678f708c99ce71872c01935470c65743f34fa534b08b895c9"
  ],
  "env": []
}
```

```sh
maurice test mcp install --agent-id <agent_id> --file document-mcp.json --wait --wait-timeout 90s --json
maurice tools list --deployment <agent_id> --json
```

The image normally starts the managed network sidecar. The explicit entrypoint
above starts the Document STDIO server for One. Do not add `-t`: MCP uses pipes,
not a terminal. Keep Docker flags such as `-e` in `arguments` so they reach the
container. For the currently qualified One backend, start One with
`MCP_MODERN_CLIENT_ENABLED=true`.

Use the qualified tool name returned by discovery for `document_parse_v1`.
Save `parse.json` using a source available to the container:

```json
{
  "source_ref": "storage://file/example.csv",
  "source_url": "storage://file/example.csv",
  "filename": "example.csv",
  "deployment_id": "<agent_id>"
}
```

```sh
maurice tools call <qualified-document_parse_v1-name> --deployment <agent_id> --input-file parse.json --json
maurice test cleanup --agent-id <agent_id> --expect-name <agent_name> --apply --json
```

For an integration fixture without Storage, `source_ref` may be a provenance
label such as `audit:example.csv` and `source_url` an allowlisted URL. On Docker
Desktop, a host fixture is reachable through `host.docker.internal`, not the
container's `localhost`; allowlist that exact hostname and enable
`DOCUMENT_ALLOW_INSECURE_HTTP=true` only for a controlled local HTTP fixture.
The production default is HTTPS with no document egress until configured.

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

A successful parse returns `status=ready`, `format`, `markdown` (when inline),
`preview`, input/output byte counts and the source SHA-256. For example,
`name,amount\nÉlodie,42\n` becomes a Markdown table containing `Élodie` and `42`.
Check the MCP error flag as well as the CLI exit code: failed downloads,
unsupported files and missing required Buffer produce tool errors. A scanned
PDF returns `status=needs_ocr` and `ocr_pages`, without a Markdown result.
`source_ref` and `deployment_id` are required; provide `filename` for CSV.
Temporary source URLs are not returned in the result.

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

Install Rust 1.91.1 as pinned by `rust-toolchain.toml`, plus CMake, Clang and
pkg-config. The published v0.1.1 image still reports the crate version `0.1.0`
in health responses; use the image digest above to identify the tested release.

```bash
task check
task build
```

## License

Apache-2.0. The AnyDoc dependency is distributed under the MIT license.
