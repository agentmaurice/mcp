# mcp-office

MCP server (Go) that creates, edits, compares and inspects Word (DOCX) and
Excel (XLSX) files. It drives a document engine CLI as a separate process; the
server itself never parses Office packages.

Module: `github.com/agentmaurice/mcpchatui/mcp/office`. Stdio and HTTP
transports come from `mcp/shared/modernmcp`; the image is a sidecar image
(`Dockerfile.sidecar`).

## Tools

| Tool | Purpose |
|---|---|
| `office_health_v1` | `{status:"ok", version}` |
| `office_capabilities_v1` | version, engine (name, version, license, availability), formats, supported operations, limits, error codes |
| `office_create_v1` | `{format: docx\|xlsx, source_format: markdown\|html\|json, content, filename?}`. DOCX from `markdown` or `html` (restricted HTML fragment); XLSX from `json` (2-D array, or `{"sheets":[{"name","rows"}]}`; strings starting with `=` are formulas) |
| `office_edit_v1` | `{format, file, ops (1..200), track?, author?, filename?}`; ordered, atomic batch. DOCX ops: `findReplace`, `deleteBlocks`, `moveBlocks`, `insert_content`, table, style, comment, header/footer, page setup... (full list in `office_capabilities_v1`); `track: true` records content edits as tracked changes. XLSX ops: cell entries `{cell, value\|formula}` or `{op: set_cell\|set_range\|format_range\|...}` (not mixed) |
| `office_compare_v1` | `{before, after, author?, filename?}` returns a DOCX whose differences are tracked changes, with `changes: {insertions, deletions, changed_paragraphs}` |
| `office_inspect_v1` | `{format, file}` returns `text` (Markdown for DOCX, TSV per sheet for XLSX), `structure` and `issues` from the document checks |

`office_edit_v1` returns `applied` as reported by the engine; failed, skipped or fewer operations than requested give `op_rejected` (no file), and an answer without a batch report (or without a cells count for cell entries) gives `invalid_output`.

Not supported: PPTX, rendering to PDF or images, pictures and any operation that
fetches external content.

### Files

Input files are `{content_base64}` (20 MiB decoded at most) or `{ref}` (a
`buffer://` reference read from the Buffer service; without a Buffer the error is
`buffer_unavailable`). Output files: a Buffer upload when configured (`file.ref`
plus an `outputs[]` entry with `ref`, `mime_type`, `bytes`, `sha256`,
`metadata.filename`), otherwise `file.content_base64` up to 2 MiB, beyond that
`storage_required`. `file` always carries `filename`, `mime_type`, `bytes`,
`sha256`.

### Errors

An `isError` result with `{error, message}` (and `details.rejected_ops` for a
rejected batch). Stable codes: `invalid_arguments`, `unsupported_format`,
`file_too_large`, `invalid_document`, `op_rejected`, `engine_unavailable`,
`engine_timeout`, `engine_failed`, `engine_busy`, `storage_required`,
`buffer_unavailable`, `buffer_not_found`, `buffer_failed`.

## Configuration

| Variable | Default | Role |
|---|---|---|
| `OFFICE_CLI_PATH` | none | executable of the engine CLI (required) |
| `OFFICE_XLSX_SIDECAR_PATH` (or `XLSX_SIDECAR_PATH`) | none | workbook helper; without it XLSX edit and inspect are not offered |
| `OFFICE_CLI_TIMEOUT_SECONDS` | 60 | budget of one tool call |
| `OFFICE_MAX_CONCURRENCY` | 2 | simultaneous engine processes |
| `OFFICE_MAX_INPUT_BYTES` | 20 MiB | decoded input limit |
| `OFFICE_ENGINE_VERSION` | CLI version | engine version reported by the capabilities |
| `OFFICE_BUFFER_SERVICE_URL`, `OFFICE_BUFFER_TOKEN`, `OFFICE_BUFFER_NAMESPACE`, `OFFICE_BUFFER_TTL_SECONDS` | none, namespace `office`, 3600 | Buffer; the generic `BUFFER_*` names are accepted |

Each call runs the CLI in its own temporary directory (working directory, home
and the only path root the engine may use), under a bounded deadline, with a
minimal environment: no key, token or proxy variable is passed;
`ELECTRON_SKIP_BINARY_DOWNLOAD=1` and `DO_NOT_TRACK=1` are set. The process
group is killed on timeout. The server itself cannot enforce a network policy:
run the container without outbound access.

## Engine and licenses

The engine is the Apache-2.0 GenOffice CLI, built from the sources of tag
`v0.12.852` (commit `4b55db9ed1994575a695160ffc6cfd0bfc866211`, verified at
build time), plus its Rust workbook helper. The image copies `LICENSE`, `NOTICE`
and the third-party license files to `/usr/share/doc/office-engine`. Only
`office_capabilities_v1` names the engine (attribution).

## Develop

```sh
task check            # gofmt, go vet, go test, go build
OFFICE_CLI_PATH=/path/to/cli XLSX_SIDECAR_PATH=/path/to/xlsx-sidecar \
  OFFICE_TEST_OUT=/tmp/office-out go test ./internal/mcpserver -run Integration -v
```

Unit tests use `testdata/fake-cli.sh` (simulated outputs and errors). The
integration test runs only when `OFFICE_CLI_PATH` is set.

Local image (never pushed), build context = parent folder with `shared` and `office`:

```sh
docker build -f office/Dockerfile.sidecar --build-arg SIDECAR_IMAGE=<mcp sidecar image> -t office-sidecar:dev .
```
