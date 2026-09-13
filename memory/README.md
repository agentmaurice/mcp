# CompanyMemory MCP Server (DuckDB / Postgres)

MCP server providing a multi-tenant, read-safe SQL memory backed by DuckDB or PostgreSQL. Writes are handled by strict ingestion tools (no arbitrary SQL writes).

The `/mcp` endpoint supports MCP 2026-07-28 over stateless Streamable HTTP and accepts legacy Streamable HTTP requests. Legacy SSE and STDIO transports remain available.

## Build

DuckDB build:

```
go build -tags duckdb ./cmd/memory
```

Postgres build:

```
CGO_ENABLED=0 go build -tags postgres ./cmd/memory
```

## Run

```
./memory -transport=sse
./memory -transport=stdio
./memory -transport=both
```

## Docker

- DuckDB image: `docker build -t memory-duckdb .`
- Postgres image: `docker build -f Dockerfile.postgres -t memory-postgres .`

## Tools (kernel)

- `memory.query`, `memory.schema.describe`, `memory.preview`
- `memory.entities.upsert`, `memory.facts.append`, `memory.documents.register`, `memory.links.upsert`
- `memory.views.create_or_replace`, `memory.indexes.ensure`
- `memory.capabilities`, `memory.health`, `memory.stats`

## Tenant routing

- **Streamable HTTP / SSE**: pass `X-Tenant-Id` header (fallback to `storage.default_tenant_id`).
- **Stdio**: set `MCP_TENANT_ID` env var (fallback to `storage.default_tenant_id`).

## Key env vars

- `MEMORY_STORAGE_BACKEND`: `duckdb` or `postgres`.
- `MEMORY_POSTGRES_DSN`: PostgreSQL DSN (required for postgres backend).
- `MEMORY_POSTGRES_SCHEMA_PREFIX`: schema prefix for tenants (default: `tenant_`).
- `MEMORY_BASE_DIR`: base directory for tenant files (DuckDB + documents).
- `MEMORY_DB_FILENAME`: DuckDB filename (default: `memory.duckdb`).
- `MEMORY_DEFAULT_TENANT_ID`: fallback tenant id.
- `SERVER_ADDRESS`, `SERVER_BASE_PATH`: HTTP address and MCP base path (`/mcp` by default).
- `LOGGING_LEVEL`, `LOGGING_ENABLE_FILE`, `LOGGING_LOG_DIR`.

## Config

Copy `configs/config.example.json` to `configs/config.json` and adjust if needed. Env vars override config.

## Acceptance script

Run `./scripts/acceptance.sh` to execute a quick entities.upsert + facts.append + query flow against stdio transport.

## Postgres integration tests

Run `go test -tags postgres ./...` with `POSTGRES_DSN` or `MEMORY_POSTGRES_DSN` set.

## PII policy

The `pii_policy` table can drive redaction at query time using `column_path` rules such as:

- `entities.attributes.email` with `action=redact|hash|drop`
- `entities.name` with `action=drop`

Rules are applied on top of the static `security.redaction_keys` list.

## Example: memory.links.upsert

```json
{
  "jsonrpc": "2.0",
  "id": 102,
  "method": "tools/call",
  "params": {
    "name": "memory.links.upsert",
    "arguments": {
      "items": [
        {
          "linkType": "represents",
          "from": {"kind": "document", "ref": "doc_INV-2026-001"},
          "to": {"kind": "entity", "ref": "invoice|INV-2026-001"}
        }
      ],
      "source": {"system": "billing-app", "timestamp": "2026-01-31T09:01:00Z"},
      "writer": {"appId": "billing", "actorId": "AgentMaurice"}
    }
  }
}
```
