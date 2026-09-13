# MCP Brain

Go MCP server that indexes an organization's knowledge (code, documentation,
configuration) and retrieves the relevant context for AgentMaurice agents. It
combines BM25 full-text search, vector search, a knowledge graph and hybrid
fusion, with AST-aware chunking for code. Brain returns results, not generated
answers: an agent uses it to locate context, and can then hand that context to
MCP RAG for a reasoned answer.

Multi-tenant: one DuckDB file per tenant (or one PostgreSQL schema per
tenant), tenant resolved from the `X-Tenant-Id` header or `MCP_TENANT_ID` in
stdio mode, with `BRAIN_DEFAULT_TENANT_ID` as fallback.

## Tools

Search:

- `brain.search` (auto mode selection)
- `brain.keyword` (BM25)
- `brain.semantic` (vector)
- `brain.hybrid` (alpha-weighted BM25 + vector)

Indexing:

- `brain.index`
- `brain.document.upsert`
- `brain.index.status`
- `brain.sources`

Administration:

- `brain.health`
- `brain.stats`
- `brain.schema`

The tool contract also defines `brain.graph` (relation traversal) and
`brain.multi` (reciprocal rank fusion of all modes).

## Build and run

```bash
go build -tags duckdb -o brain ./cmd/brain
go test -tags duckdb ./...
./brain -transport=sse      # or -transport=stdio
docker build -f Dockerfile -t brain-server ..
```

Or with Task: `task build`, `task test`, `task lint`, `task run`,
`task run-stdio`, `task docker-build`. The published image is
`ghcr.io/agentmaurice/mcp/brain:<version>`.

## Configuration

```text
BRAIN_SERVER_ADDRESS=:8085
BRAIN_SERVER_BASE_PATH=/mcp/brain
BRAIN_STORAGE_BACKEND=duckdb          # duckdb|postgres
BRAIN_BASE_DIR=./data
BRAIN_DEFAULT_TENANT_ID=default
BRAIN_POSTGRES_DSN=                   # postgres backend only
BRAIN_EMBEDDING_PROVIDER=none         # none|ollama|openai|openai-compatible
BRAIN_EMBEDDING_MODEL=nomic-embed-text
BRAIN_EMBEDDING_DIMENSIONS=768
BRAIN_OLLAMA_URL=http://localhost:11434
BRAIN_OPENAI_BASE_URL=                # OpenAI-compatible /v1 endpoint
BRAIN_OPENAI_API_KEY=
BRAIN_INDEX_WORKERS=2
BRAIN_SEARCH_DEFAULT_MAX_RESULTS=20
BRAIN_LOGGING_LEVEL=info
```

`brain.document.upsert` can load content through a `buffer_ref` when
`BUFFER_ENABLED=true`, using `BUFFER_SERVICE_URL`, `BUFFER_TOKEN` and
`BUFFER_NAMESPACE` (default `brain`). References are opaque keys; Brain never
follows a caller-supplied URL. Without an embedding provider, only keyword
search is available.

## License

Apache-2.0.
