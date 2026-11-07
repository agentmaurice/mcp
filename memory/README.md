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

`memory.query` returns `truncated: true` only when the row cap (`maxRows`) or the
response size limit dropped rows; an explicit or added `LIMIT` does not set it.
For a tenant with no entity, document or fact, the result carries
`tenant_empty: true` and a `warnings` entry, whatever the number of rows. If the check
itself fails, `tenant_empty` is `null` and the warning says so.
A row whose `is_deleted` flag is NULL is invisible, as in the schema views (`WHERE is_deleted = false`), so it does not count as data.

`memory.indexes.ensure` refuses a single-column index: the defect described below applies
to it, so an index needs two or more columns.

## Logs (AGENT-1093)

Above Debug, a log line never carries a SQL text, query parameters, tool arguments, a
fact or entity value or a result summary: it carries the tool, the tenant, sizes
(`sql_bytes`, `params_bytes`, `arguments_bytes`), counts and durations. The content is
written at Debug only, masked (tokens, `key=value` credentials, e-mail addresses, values
of sensitive keys) and cut at 512 bytes, through `logging.Debug` and `logging.DebugValue`.
DuckDB errors, which can quote the statement, are logged through `logging.ErrorText`
(first line, masked, cut). `TestNoPayloadAboveDebug` fails on any zap field with a payload
key built outside a Debug call; `TestNoPayloadInLogsAboveDebug` calls the tools with
markers in the SQL, parameters and arguments and fails if one reaches Info or above.
Measured on a 21 KB `memory.query`: 64 KB of Info logs before, 582 bytes after.

## DuckDB 2.0 (AGENT-1058)

Version: DuckDB 2.0.0, embedded by `github.com/duckdb/duckdb-go/v2` v2.20000.0
(`go.mod`; the former `marcboeker/go-duckdb` module stops at 1.4.1). The defect
below was found on 1.4.1; its fix is kept.

- **Backup before the first open.** A tenant file written by DuckDB 1.x cannot be
  read by a 1.x image once 2.x has opened it. Before the first open, the server
  copies `<tenant>/memory.duckdb` and its WAL to `memory.duckdb.pre-duckdb2` and
  `memory.duckdb.wal.pre-duckdb2`, and records its size and SHA-256 in `memory.duckdb.pre-duckdb2.sum`
  (written last: a copy without it is redone). The copy is taken once and never overwritten;
  it is refused while another process (a 1.x sidecar) holds the file; a file created by 2.x is
  not copied; if the copy fails or an existing copy does not match its `.sum`, the tenant is
  not opened.
  Plan for the disk space: one extra copy of each tenant file.
- **Rolling back to a 1.x image** (memory up to v1.1.x). Stop the sidecar, then in the
  tenant directory: `shasum -a 256 memory.duckdb.pre-duckdb2` (compare with the `.sum` file), `mv memory.duckdb.pre-duckdb2 memory.duckdb`, `mv memory.duckdb.wal.pre-duckdb2
  memory.duckdb.wal` (when it exists), `rm -f memory.duckdb.wal` otherwise, and start the
  1.x image. Writes made after the upgrade are lost. Remove the `.pre-duckdb2` files
  only once the upgrade is accepted.
- **SQL.** `memory.query` is checked by a MySQL-dialect parser that accepts no lambda,
  so DuckDB's 2.0 change (single-arrow lambda rejected, removed in 2.1) cannot reach
  a caller's query; `TestNoSingleArrowLambdaInSource` keeps the module's own SQL free
  of it.
- **Tests.** `TestLegacyDuckDB14TenantAnswersMemoryQuery` opens a tenant file written
  by 1.4.1 (`internal/transport/mcp/testdata/legacy-duckdb14`, regenerated on a 1.4
  build by `TestWriteLegacyFixture`) and runs the same `memory.query` statements.

## Known DuckDB defect (1.4.1) and its fix

Version: DuckDB 1.4.1, embedded by go-duckdb v2.4.3 (`go.mod`).

Symptom: after a checkpoint and a reopen by another process (a server restart),
an equality filter on a single-column secondary index returns no row. Every query
that filters on such a column comes back empty without an error. The primary keys
and the composite unique keys keep working. Confirmed on this repository, with the
server's own write and reopen sequence.

Fix: the schema creates no single-column secondary index. On every open, the
server lists the secondary indexes of the kernel schema (`main`, not primary keys and not
unique constraints) and drops every one that covers a single column, whatever its name,
and logs each drop (`dropped single-column index affected by the DuckDB 1.4.1 defect`).
Indexes of other schemas are not touched, and a failed drop is logged without failing
the open.
`memory.indexes.ensure` refuses a single-column index with an explicit error. Primary
keys, the composite unique key and composite indexes are not affected. Regression
tests: `reopen_test.go` and `index_drop_test.go` (a manual single-column index is
dropped, a composite one is kept, and an equality lookup by name still finds the row).

Consequence to keep in mind: a process that stops after the schema creation, before
the checkpoint that `ensureTenant` runs right after it, leaves a write-ahead log that
DuckDB 1.4.1 cannot replay. The server checkpoints immediately, so this is a window of
milliseconds at startup. A failed open then triggers the WAL recovery path, which
renames the log: check this path before relying on it for data safety.

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
