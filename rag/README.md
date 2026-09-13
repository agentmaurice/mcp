# RAG MCP Server

A production-ready RAG (Retrieval-Augmented Generation) server implementing the Model Context Protocol (MCP).

## Features

- **5-Layer RAG Pipeline**
  - Layer 0: Document Intelligence (smart chunking)
  - Layer 1: Query Intelligence (intent detection)
  - Layer 2: Hybrid Retrieval (vector + full-text)
  - Layer 3: Reasoning (LLM-based answers)
  - Layer 4: Experience (interaction logging in the database)

- **Multi-tenant Support** - Isolated data per tenant
- **MCP Protocol** - MCP 2026-07-28 over stateless Streamable HTTP, with legacy SSE and STDIO compatibility
- **Asynchronous Ingestion** - Background job processing
- **Vector Search** - Qdrant integration
- **Duplicate Detection** - Hash/semantic option (configurable doc_type) to avoid duplicates at ingestion
- **MCP Resources** - Direct access to documents/chunks/jobs/deployments/tenants via `rag://...` URIs
- **Analytics** - Interactions/feedback persisted in the DB (`interactions` table) with automatic purge (default retention: 30 days)
- **HTTP + SSE Transport** - Modern MCP on `/mcp`, legacy SSE on `/mcp/sse` and RESTful APIs

## Concepts

### Tenant ID (Multi-tenancy)

The RAG server supports multi-tenancy, allowing complete data isolation between different users, applications, or organizations.

**What is a Tenant ID?**

A `tenant_id` is a unique identifier that isolates all data (documents, chunks, embeddings) for a specific user or application. Each tenant has its own:
- Document collection
- Vector embeddings
- Query history

**Format:**

The `tenant_id` uses the [xid](https://github.com/rs/xid) format - a globally unique, sortable identifier (20 characters). Example: `cq5k4g4s8v0c6k8m8m9g`

**Usage:**

- **Ingestion**: All documents are stored under a specific tenant
- **Query**: Searches only return results from the specified tenant's documents
- **API Key**: Each tenant can have its own API key for authentication

**Examples:**
```bash
# Using an xid format tenant_id
curl -X POST http://localhost:8084/api/ingest \
  -H "Content-Type: application/json" \
  -d '{"tenant_id": "cq5k4g4s8v0c6k8m8m9g", "title": "Doc", "content": "..."}'

# The tenant is auto-created if it doesn't exist
```

**Best Practices:**
- Use a consistent `tenant_id` for all operations related to the same user/application
- Generate a new xid once and reuse it for all subsequent requests
- Store the `tenant_id` in your application configuration

## Architecture

```
├── cmd/rag/              # Entry point
├── internal/
│   ├── app/              # Application coordinator
│   ├── business/         # Business logic (managers, workers)
│   ├── config/           # Configuration
│   ├── logging/          # Logging setup
│   ├── platform/         # External services (LLM, vector store)
│   ├── rag/              # RAG pipeline layers
│   ├── shared/           # Shared types and interfaces
│   ├── storage/          # Database and repositories
│   └── transport/        # MCP and HTTP servers
└── configs/              # Configuration files
```

## Quick Start

### Prerequisites

- Go 1.23+
- Docker & Docker Compose
- PostgreSQL 16+
- Qdrant vector database
- OpenAI API key

### Installation

1. Clone the repository
2. Copy environment file:
   ```bash
   cp .env.example .env
   ```
3. Edit `.env` and set your OpenAI API key

### Running with Docker Compose

```bash
task compose-up
```

This starts:
- PostgreSQL (port 5432)
- Qdrant (port 6333)
- RAG Server (port 8080)

### Running Locally

```bash
# Install dependencies
task install

# Run database migrations
task migrate

# Run the server
task run
```

### Migrations / interaction logging

- The `database.auto_migrate` flag (config) automatically applies the Ent migrations at startup.
- Interactions and feedback are persisted in the `interactions` table. An opportunistic purge deletes entries older than 30 days (purge interval ~24h). Adjust the retention or the scheduler in `experience.Logger` if needed.

### Deduplication (ingestion)
- `detect_duplicates` (bool) in `rag_ingest_start` enables deduplication.
- `duplicate_strategy`: `auto | semantic | hash | cv | none` (default `auto`).
- `doc_type`: `generic` (default) or other (cv, contract, invoice…).
- Current pipeline: normalization + hash (SHA256) and doc-level semantic search in Qdrant (`<collection>_docs`, threshold 0.9). If a duplicate is found, the job is marked failed with `duplicate of <id>`.
- Documents store `normalized_hash`, `doc_type`, `duplicate_of`. A dedicated Qdrant collection for document embeddings is created automatically.

### Content Inspection (PII/sensitive data)
- Ingestion options: `detect_content` (bool), `content_detection_profile` (`none|pii_basic|pii_strict|cv_identifiability`), `custom_detection_rules` (array of inline rules).
- Profiles: basic/strict PII, CV identifiability (LLM). Blocked if severity=block or identifiability score > threshold (configurable).
- Findings are stored on the job (`has_content_findings`, `content_findings`), with a possible PII flag on the document.
- MCP tool `rag_scan_document` to scan an existing document (same profiles/custom rules).
- Configurable thresholds in `detection` (config): `semantic_threshold`, `cv_threshold`, `identifiability_warn`, `identifiability_block`.

### MCP Resources
- `rag://document/{id}` (full text, metadata, doc_type, duplicate_of, chunks)
- `rag://chunk/{id}`
- `rag://job/{id}`
- `rag://deployment/{id}`
- `rag://tenant/{id}`

The `/mcp` endpoint uses the stateless Streamable HTTP transport and exposes the 12 tools as well as the five resource templates. Legacy clients can keep using SSE (`/mcp/sse` and `/mcp/message`) or STDIO.

## REST API

The server exposes a REST API for direct HTTP access.

### Health Check

```bash
GET /health
```

**Response:**
```json
{"status": "ok", "server": "rag-mcp-server"}
```

### Document Ingestion

Start ingesting a document into the knowledge base. Provide either `content` (text) or `source_url` (to fetch content).

```bash
POST /api/ingest
Content-Type: application/json

# Option 1: Direct content
{
  "tenant_id": "cq5k4g4s8v0c6k8m8m9g",
  "title": "Document Title",
  "content": "Full document text content...",
  "source_metadata": {"author": "John Doe"}
}

# Option 2: Fetch from URL
{
  "tenant_id": "cq5k4g4s8v0c6k8m8m9g",
  "title": "Document Title",
  "source_url": "https://example.com/doc.txt",
  "source_metadata": {"author": "John Doe"}
}
```

**Parameters:**
| Field | Required | Description |
|-------|----------|-------------|
| `tenant_id` | Yes | Tenant ID for multi-tenant isolation |
| `title` | Yes | Document title |
| `content` | Yes* | Document text content (*required if `source_url` not provided) |
| `source_url` | Yes* | URL to fetch content from (*required if `content` not provided) |
| `source_type` | No | `text` or `url` (auto-detected if not specified) |
| `source_metadata` | No | Custom metadata to attach to the document |

**Response:**
```json
{
  "job_id": "cq5k4h4s8v0c6k8m8ma0",
  "tenant_id": "cq5k4g4s8v0c6k8m8m9g",
  "status": "processing"
}
```

### Ingestion Status

Check the status of an ingestion job.

```bash
GET /api/ingest/:job_id
```

**Response:**
```json
{
  "job_id": "cq5k4h4s8v0c6k8m8ma0",
  "status": "completed",
  "progress": 100,
  "message": ""
}
```

**Status values:** `pending`, `processing`, `completed`, `failed`

### Query

Execute a RAG query to retrieve and generate answers.

```bash
POST /api/query
Content-Type: application/json

{
  "tenant_id": "cq5k4g4s8v0c6k8m8m9g",
  "query": "What is RAG?",
  "language": "en",
  "max_tokens": 500
}
```

**Response:**
```json
{
  "answer": "RAG (Retrieval-Augmented Generation) is...",
  "citations": [
    {
      "chunk_id": "abc123",
      "document_id": "doc456",
      "snippet": "...relevant text excerpt...",
      "metadata": {
        "document_title": "Introduction to RAG",
        "author": "John Doe",
        "section_type": "content"
      }
    }
  ]
}
```

**Note:** The `metadata` field in citations contains:
- `document_title`: Title of the source document
- Any custom metadata provided during ingestion via `source_metadata`
- `section_type`, `level`: Internal chunking metadata

## MCP Protocol (Remote Access)

The server implements the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) for integration with AI systems like Claude, ChatGPT, and other LLM-based applications.

### MCP Transports

The primary transport is stateless Streamable HTTP on `POST /mcp`. The legacy SSE and STDIO transports remain available for existing clients.

**Endpoints:**
- `POST /mcp` - MCP 2026-07-28 Streamable HTTP
- `GET /mcp/sse` - Legacy SSE connection
- `POST /mcp/message` - Legacy SSE messages

### Connecting an MCP Client

#### Claude Desktop Configuration

Add to your Claude Desktop configuration (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "rag-server": {
      "command": "npx",
      "args": [
        "mcp-remote",
        "http://your-server:8084/mcp/sse"
      ]
    }
  }
}
```

#### Using mcp-remote (SSE to stdio bridge)

For remote MCP access, use `mcp-remote` to bridge SSE transport to stdio:

```bash
# Install mcp-remote
npm install -g mcp-remote

# Connect to remote RAG server
npx mcp-remote http://your-server:8084/mcp/sse
```

#### Direct SSE Connection (JavaScript)

```javascript
const eventSource = new EventSource('http://your-server:8084/mcp/sse');

eventSource.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log('Received:', data);
};

// Send a message
fetch('http://your-server:8084/mcp/message', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({
    jsonrpc: '2.0',
    method: 'tools/call',
    params: {
      name: 'rag_query',
      arguments: {
        tenant_id: 'cq5k4g4s8v0c6k8m8m9g',
        query: 'What is RAG?'
      }
    },
    id: 1
  })
});
```

### MCP Tools

The server exposes 12 MCP tools. The three core workflow tools are documented below; clients can retrieve the complete deterministic catalogue with `tools/list`.

#### rag_query

Execute a RAG query to retrieve and generate answers.

**Input Schema:**
```json
{
  "tenant_id": "string (required) - Tenant ID for multi-tenant isolation",
  "query": "string (required) - The question to answer",
  "max_tokens": "number (optional) - Maximum tokens for response"
}
```

**Output:**
```json
{
  "answer": "RAG stands for...",
  "citations": [
    {"chunk_id": "...", "document_id": "...", "snippet": "..."}
  ]
}
```

#### rag_ingest_start

Start ingesting a document into the knowledge base. Provide either `content` or `url`.

**Input Schema:**
```json
{
  "tenant_id": "string (required) - Tenant ID for multi-tenant isolation",
  "title": "string (required) - Document title",
  "content": "string (required if url not provided) - Document content (text)",
  "url": "string (required if content not provided) - URL to fetch content from",
  "metadata": "object (optional) - Additional metadata"
}
```

**Output:**
```json
{
  "job_id": "cq5k4h4s8v0c6k8m8ma0",
  "status": "pending"
}
```

#### rag_ingest_status

Get the status of an ingestion job.

**Input Schema:**
```json
{
  "job_id": "string (required) - The job ID returned by rag_ingest_start"
}
```

**Output:**
```json
{
  "job_id": "cq5k4h4s8v0c6k8m8ma0",
  "status": "completed",
  "progress": 100,
  "message": ""
}
```

## Complete Usage Example

Here's a complete workflow example using curl:

```bash
# 1. Generate a tenant ID (do this once, save it for reuse)
TENANT_ID=$(curl -s https://xid.dev/new)
echo "Your tenant ID: $TENANT_ID"

# 2. Ingest a document
JOB_RESPONSE=$(curl -s -X POST http://localhost:8084/api/ingest \
  -H "Content-Type: application/json" \
  -d "{
    \"tenant_id\": \"$TENANT_ID\",
    \"title\": \"Introduction to RAG\",
    \"content\": \"RAG (Retrieval-Augmented Generation) is a technique that combines information retrieval with text generation. It first retrieves relevant documents from a knowledge base, then uses them as context for generating accurate answers.\"
  }")

JOB_ID=$(echo $JOB_RESPONSE | jq -r '.job_id')
echo "Ingestion job started: $JOB_ID"

# 3. Wait for ingestion to complete (poll status)
while true; do
  STATUS=$(curl -s http://localhost:8084/api/ingest/$JOB_ID | jq -r '.status')
  echo "Status: $STATUS"
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ]; then
    break
  fi
  sleep 2
done

# 4. Query the knowledge base
curl -s -X POST http://localhost:8084/api/query \
  -H "Content-Type: application/json" \
  -d "{
    \"tenant_id\": \"$TENANT_ID\",
    \"query\": \"What is RAG and how does it work?\",
    \"max_tokens\": 500
  }" | jq
```

## Configuration

Configuration can be provided via:
1. `configs/rag.json` file
2. Environment variables (override file config)

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LLM_API_KEY` | Yes | - | OpenAI API key for embeddings and generation |
| `DATABASE_DSN` | Yes | - | PostgreSQL connection string |
| `VECTOR_STORE_URL` | Yes | - | Qdrant URL (e.g., `http://localhost:6333`) |
| `SERVER_ADDRESS` | No | `:8084` | Server bind address |
| `LOGGING_LEVEL` | No | `info` | Log level (`debug`, `info`, `warn`, `error`) |

### Example .env file

```bash
# LLM Configuration
LLM_API_KEY=sk-your-openai-api-key

# Database
DATABASE_DSN=postgres://rag:rag@localhost:5432/rag?sslmode=disable

# Vector Store
VECTOR_STORE_URL=http://localhost:6333
VECTOR_STORE_COLLECTION=rag_documents

# Server
SERVER_ADDRESS=:8084
LOGGING_LEVEL=info
```

## Development

### Available Tasks

```bash
task --list
```

Key tasks:
- `task build` - Build binary
- `task run` - Run server
- `task test` - Run tests
- `task lint` - Run linters
- `task generate` - Generate Ent code
- `task compose-up` - Start with Docker Compose

### Adding New MCP Tools

1. Create tool file in `internal/transport/mcp/tool_*.go`
2. Implement `Definition()` and `Handler()` methods
3. Register in `internal/transport/mcp/server.go`

Example:
```go
package mcp

type MyTool struct {
    logger *zap.Logger
}

func (t *MyTool) Definition() mcp.Tool {
    return mcp.Tool{
        Name:        "my_tool",
        Description: "Description of what the tool does",
        InputSchema: mcp.ToolInputSchema{
            Type: "object",
            Properties: map[string]interface{}{
                "param1": map[string]interface{}{
                    "type":        "string",
                    "description": "Parameter description",
                },
            },
            Required: []string{"param1"},
        },
    }
}

func (t *MyTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        // Implementation
        return &mcp.CallToolResult{...}, nil
    }
}
```

## Production Deployment

### Local Docker Deployment

```bash
# Build and run with Docker Compose
task compose-up

# Or manually
task docker-build
docker run -p 8084:8084 --env-file .env rag-server:latest
```

### Remote Server Deployment

For deploying the RAG server on a remote machine accessible via MCP:

1. **Deploy the server**
```bash
# On the remote server
docker-compose up -d
```

2. **Configure firewall**
```bash
# Allow port 8084 for HTTP/SSE access
ufw allow 8084/tcp
```

3. **Configure reverse proxy (optional but recommended)**

Nginx configuration example:
```nginx
server {
    listen 443 ssl;
    server_name rag.yourdomain.com;

    ssl_certificate /etc/ssl/certs/your-cert.pem;
    ssl_certificate_key /etc/ssl/private/your-key.pem;

    location / {
        proxy_pass http://localhost:8084;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;

        # SSE specific settings
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 86400s;
    }
}
```

4. **Configure Claude Desktop for remote access**

Update `~/Library/Application Support/Claude/claude_desktop_config.json`:
```json
{
  "mcpServers": {
    "rag-server": {
      "command": "npx",
      "args": [
        "mcp-remote",
        "https://rag.yourdomain.com/sse"
      ]
    }
  }
}
```

### Docker Compose Full Stack

```yaml
version: '3.8'
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: rag
      POSTGRES_PASSWORD: rag
      POSTGRES_DB: rag
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  qdrant:
    image: qdrant/qdrant:latest
    volumes:
      - qdrant_data:/qdrant/storage
    ports:
      - "6333:6333"
      - "6334:6334"

  rag-server:
    build: .
    ports:
      - "8084:8084"
    environment:
      - LLM_API_KEY=${LLM_API_KEY}
      - DATABASE_DSN=postgres://rag:rag@postgres:5432/rag?sslmode=disable
      - VECTOR_STORE_URL=http://qdrant:6333
      - SERVER_ADDRESS=:8084
    depends_on:
      - postgres
      - qdrant

volumes:
  postgres_data:
  qdrant_data:
```

## Troubleshooting

### Common Issues

**1. "failed to generate embedding" errors**
- Check that `LLM_API_KEY` is set correctly
- Verify the API key has access to the embeddings API

**2. "connection refused" to Qdrant**
- Ensure Qdrant is running on port 6334 (gRPC)
- Check firewall rules if running in Docker

**3. "tenant not found" errors**
- Use a valid xid format for tenant_id
- The tenant is auto-created on first ingestion

**4. SSE connection drops**
- Configure your reverse proxy for long-lived connections
- Disable buffering in nginx/Apache

### Logs

Enable debug logging for troubleshooting:
```bash
LOGGING_LEVEL=debug ./rag-server
```

## License

Apache-2.0.
