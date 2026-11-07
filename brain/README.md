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
- `brain.keyword` (BM25, ranked on the DuckDB full-text index)
- `brain.semantic` (vector)
- `brain.hybrid` (alpha-weighted BM25 + vector)

## Keyword search and response format

`brain.keyword` ranks chunks with BM25 on the DuckDB FTS index of each tenant.
The index is rebuilt synchronously before the first search that follows a
write to chunks or documents; concurrent searches share one rebuild.

- Identifiers in the query (`AGENT-530`, `MEM-G3`, `2026.10.19-3015`,
  `calcul_taux_marge`) are also matched verbatim, as whole tokens, and their
  hits are listed before the ranked hits. Digits and underscores are kept in
  tokens. A token is a run of letters, digits, underscores, dots and hyphens,
  without trailing dots or hyphens: `AGENT-530.` matches, `AGENT-530--1` does not.
  One query searches at most 1000 identifiers verbatim; the others are named in
  `search_metadata.warnings`.
- If the index is unavailable (the `duckhttp` backend, or an index error), the
  search falls back to case-insensitive substring matching in document order.
  The response says so: `search_metadata.bm25_mode_effective` is
  `substring_fallback` and `search_metadata.warnings` explains why. The
  effective mode is `fts` otherwise.
- A tenant with no document returns no result with
  `search_metadata.tenant_empty: true` and a warning. The warning names only
  the requested tenant.

`brain.keyword`, `brain.hybrid` and `brain.search` accept
`response_format`:

- `detailed` (default): the full result, with the complete chunk content for
  `brain.keyword` and `brain.hybrid`. `brain.search` keeps its compact
  envelope. Existing consumers are unaffected.
- `concise`: a bounded envelope, about 300 characters per result. Each result
  has `rank`, `score`, `match_type` (`exact_identifier`, `bm25` or
  `substring`), `chunk_id`, `document_id`, `uri` (the document file path),
  `title`, `start_line`, `end_line` and `snippet`. The snippet is centred on
  the first query term found and is marked with `…` at a cut end. The
  envelope keeps `mode`, `query`, `total_results` and `search_metadata`.

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

## Logs (AGENT-1093)

Brain never logs a search query, a document or a result above Debug: its log lines carry
tenants, counts, durations and technical identifiers. `TestNoPayloadAboveDebug`
(`internal/logging`) fails on any zap field with a payload key (`query`, `content`,
`sql`, `arguments`…) built outside a Debug call, so a future log line cannot bring it back.

## DuckDB 2.0 (AGENT-1058)

Version: DuckDB 2.0.0, embedded by `github.com/duckdb/duckdb-go/v2` v2.20000.0
(`go.mod`; the former `marcboeker/go-duckdb` module stops at 1.4.1). The defect
below was found on 1.4.1; its fix is kept.

- **Backup before the first open.** A tenant file written by DuckDB 1.x cannot be
  read by a 1.x image once 2.x has opened it. Before the first open, the server
  copies `<tenant>/brain.duckdb` and its WAL to `brain.duckdb.pre-duckdb2` and
  `brain.duckdb.wal.pre-duckdb2`, and records its size and SHA-256 in `brain.duckdb.pre-duckdb2.sum`
  (written last: a copy without it is redone). The copy is taken once and never overwritten;
  it is refused while another process (a 1.x sidecar) holds the file; a file created by 2.x is
  not copied; if the copy fails or an existing copy does not match its `.sum`, the tenant is
  not opened.
  Plan for the disk space: one extra copy of each tenant file.
- **Rolling back to a 1.x image** (brain up to v1.2.x). Stop the sidecar, then in the
  tenant directory: `shasum -a 256 brain.duckdb.pre-duckdb2` (compare with the `.sum` file), `mv brain.duckdb.pre-duckdb2 brain.duckdb`, `mv brain.duckdb.wal.pre-duckdb2
  brain.duckdb.wal` (when it exists), `rm -f brain.duckdb.wal` otherwise, and start the
  1.x image. Writes made after the upgrade are lost. Remove the `.pre-duckdb2` files
  only once the upgrade is accepted.
- **Full-text index.** The index written by 1.4 is read by 2.0; it is rebuilt on the
  first write of a tenant (`fts` for 2.0 is installed at image build,
  `-prefetch-extensions`, and loaded without network).
- **SQL.** The single-arrow lambda is gone (AGENT-1057); the connection setting
  `lambda_syntax = DISABLE_SINGLE_ARROW` keeps it out. `extension_directories`
  replaces the deprecated singular setting.
- **Tests.** `TestLegacyDuckDB14TenantOpensAndKeepsItsBackup` opens a tenant file
  written by 1.4.1 (`internal/search/testdata/legacy-duckdb14`, regenerated on a 1.4
  build by `TestWriteLegacyFixture`). `BenchmarkKeywordSearch` logs a digest of the
  ranked results: 1.4.1 and 2.0.0 give the same one on the same corpus.

## Known DuckDB defect (1.4.1) and its fix

Version: DuckDB 1.4.1, embedded by go-duckdb v2.4.3 (`go.mod`).

Symptom: after a checkpoint and a reopen by another process (a server restart),
an equality filter on a single-column index returns no row, without an error. Every
query that filters on such a column comes back empty, for example the tenant filter of
`brain.stats`, `brain.keyword` or `brain.search`. Composite indexes such as
`(tenant_id, chunk_type)` keep working. Confirmed on this repository, with the server's
own write and reopen sequence. The original code (before AGENT-1049) is affected too.

Fix: the DuckDB and duckhttp schemas create no single-column index. Indexes left by
earlier versions are dropped on each open (`DROP INDEX IF EXISTS`). Full scans stay
correct, because each tenant has its own database file. Regression test:
`internal/storage/duckdb/reopen_test.go` (write in one process, reopen in another).

Consequence to keep in mind: a process that stops after the schema creation, before
the checkpoint that `ensureTenant` runs right after it, leaves a write-ahead log that
DuckDB 1.4.1 cannot replay. The server checkpoints immediately, so this is a window of
milliseconds at startup. A failed open then triggers the WAL recovery path, which
renames the log: check this path before relying on it for data safety.

## Build and run

```bash
go build -tags duckdb -o brain ./cmd/brain
go test -tags duckdb ./...
./brain -transport=sse      # or -transport=stdio
./brain -prefetch-extensions  # install the DuckDB fts extension into $HOME/.duckdb (image build step)
docker build -f Dockerfile -t brain-server ..
```

Keyword search needs the DuckDB `fts` extension. Images install it at build time
with the shipped binary (`BRAIN_DUCKDB_EXTENSION_DIR=/out/ext ./brain -prefetch-extensions`)
and copy it to `/opt/brain/duckdb-extensions`, which the image sets as
`BRAIN_DUCKDB_EXTENSION_DIR`, so the runtime does not download it. Outside an image,
the first keyword search downloads it when network access is available; otherwise
keyword search falls back to substring matching and reports it.

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
BRAIN_DUCKDB_EXTENSION_DIR=           # DuckDB extension_directory root (empty: $HOME/.duckdb/extensions); fts is loaded from <dir>/<duckdb version>/<platform>/
BRAIN_SEARCH_FTS_STEMMER=porter       # DuckDB FTS stemmer: porter (default), french, english, none, ...
BRAIN_SEARCH_FTS_STOPWORDS=none       # none (default), english, or a stopword table name
BRAIN_LOGGING_LEVEL=info
```

`brain.document.upsert` can load content through a `buffer_ref` when
`BUFFER_ENABLED=true`, using `BUFFER_SERVICE_URL`, `BUFFER_TOKEN` and
`BUFFER_NAMESPACE` (default `brain`). References are opaque keys; Brain never
follows a caller-supplied URL. Without an embedding provider, only keyword
search is available.

## License

Apache-2.0.
