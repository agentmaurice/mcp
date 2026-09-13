# MCP FileSearch

Go-based MCP server that provides file search capabilities using Google Gemini FileSearch API. It is designed to plug into MCPChatUI and follows the same architecture patterns as the `moderator` service.

## Features

- **File Upload**: Import files via public URLs or local filesystem into dedicated FileSearch stores
- **Local Testing**: Upload local files directly for testing without public URLs
- **Semantic Search**: Perform semantic queries across indexed documents
- **Store Management**: One store per deployment for isolated data management
- **Automatic Indexing**: Files are automatically indexed by Gemini
- **Usage Tracking**: Track search queries and usage statistics
- **Health Monitoring**: Built-in health and readiness endpoints

## Architecture

This service uses:
- **Official MCP Go SDK** for the modern stateless Streamable HTTP transport
- **mark3labs/mcp-go** for the legacy SSE transport
- **Google Gemini API** for file search and semantic queries
- **SQLite** for local metadata storage (CGO-free with modernc.org/sqlite)
- **net/http** standard library for HTTP server
- **Viper** for configuration management
- **Zap** for structured logging

## Configuration

| Environment variable | Description | Default |
|----------------------|-------------|---------|
| `MCP_FILESEARCH_ADDR` / `SERVER_ADDR` | HTTP listen address (`host:port`) | `:8080` |
| `MCP_FILESEARCH_BASE_PATH` / `SERVER_BASE_PATH` | Base path for the modern endpoint and legacy `/sse` and `/message` endpoints | `/mcp/filesearch` |
| `MCP_FILESEARCH_PUBLIC_URL` / `SERVER_PUBLIC_URL` | Public URL advertised to MCP clients (optional) | *(empty)* |
| `MCP_FILESEARCH_KEEP_ALIVE` | Enables periodic SSE ping messages | `true` |
| `MCP_FILESEARCH_KEEP_ALIVE_INTERVAL` | Interval between SSE ping messages | `15s` |
| `MCP_FILESEARCH_LOG_LEVEL` / `LOG_LEVEL` | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `MCP_FILESEARCH_GEMINI_API_KEY` / `GEMINI_API_KEY` | **Gemini API key** (required) | *(none)* |
| `MCP_FILESEARCH_GEMINI_URL` / `GEMINI_API_URL` | Base URL for the Gemini API | `https://generativelanguage.googleapis.com` |
| `MCP_FILESEARCH_GEMINI_TIMEOUT` / `GEMINI_TIMEOUT` | HTTP timeout when calling Gemini | `30s` |
| `MCP_FILESEARCH_DB_PATH` / `DATABASE_PATH` | Path to SQLite database file | `/data/filesearch.db` |

## Local Development

```bash
export MCP_FILESEARCH_GEMINI_API_KEY=your-api-key
export DATABASE_PATH=./filesearch.db
go build ./cmd/filesearch
./filesearch
```

Endpoints exposed by default:

- `GET  /health` – liveness probe
- `GET  /ready` – readiness probe
- `POST /mcp/filesearch` – stateless Streamable HTTP JSON-RPC endpoint
- `GET  /mcp/filesearch/sse` – SSE stream for MCP messages
- `POST /mcp/filesearch/message` – JSON-RPC endpoint for tool calls

## Available Tools

This MCP server exposes 12 tools:

### 1. `get_store_info`
Get information about the FileSearch store for a deployment.

**Parameters:**
- `deployment_id` (string, required): Deployment ID to query

**Returns:** Store information including ID, Gemini store name, and timestamps.

### 2. `init_or_repair_store`
Initialize or repair a FileSearch store for a deployment.

**Parameters:**
- `deployment_id` (string, required): Deployment ID
- `display_name` (string, required): Display name for the store

**Returns:** Created store information.

### 3. `upload_file`
Upload a file from a public URL to the FileSearch store.

**Parameters:**
- `deployment_id` (string, required): Deployment ID
- `file_name` (string, required): File name
- `public_url` (string, required): Public URL of the file
- `mime_type` (string, required): MIME type (e.g., `application/pdf`)
- `metadata` (object, optional): Custom metadata

**Returns:** File ID and upload status.

### 4. `upload_local_file`
Upload a file from the local filesystem to the FileSearch store (useful for testing without public URLs).

**Parameters:**
- `deployment_id` (string, required): Deployment ID
- `file_path` (string, required): Local file path to upload
- `display_name` (string, optional): Display name for the file (defaults to filename)
- `metadata` (object, optional): Custom metadata

**Returns:** File ID, upload status, and detected MIME type.

**Example:**
```json
{
  "deployment_id": "my-deployment",
  "file_path": "/path/to/document.pdf",
  "display_name": "Important Document"
}
```

### 5. `get_file_status`
Get the status of a file upload/indexation.

**Parameters:**
- `file_id` (string, required): File ID

**Returns:** Current file status (pending, active, failed) and details.

### 6. `list_files`
List all files in a deployment's store.

**Parameters:**
- `deployment_id` (string, required): Deployment ID

**Returns:** Array of files with their status and metadata.

### 7. `delete_file`
Delete a file from the store.

**Parameters:**
- `file_id` (string, required): File ID to delete

**Returns:** Confirmation message.

### 8. `semantic_query`
Perform a semantic search query across indexed files.

**Parameters:**
- `deployment_id` (string, required): Deployment ID
- `query` (string, required): Search query

**Returns:** Search results with text, citations, and grounding metadata.

### 9. `raw_file_search`
Perform a raw file search returning only chunks and citations.

**Parameters:**
- `deployment_id` (string, required): Deployment ID
- `query` (string, required): Search query

**Returns:** Raw search results with chunks.

### 10. `get_full_extracted_text`
Extract the full text content of a file (placeholder).

**Parameters:**
- `file_id` (string, required): File ID

**Returns:** Full extracted text (not yet implemented).

### 11. `health_check`
Check the health of the FileSearch service.

**Parameters:** None

**Returns:** Service health status and checks.

### 12. `get_usage_stats`
Get usage statistics for a deployment.

**Parameters:**
- `deployment_id` (string, required): Deployment ID

**Returns:** File counts and search statistics.

## Building

```bash
# Build binary
go build -o filesearch ./cmd/filesearch

# Build Docker image
docker build -t mcp-filesearch:latest .

# Run Docker container
docker run -d \
  -e GEMINI_API_KEY=your-api-key \
  -v filesearch-data:/data \
  -p 8080:8080 \
  mcp-filesearch:latest
```

## Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package
go test ./internal/storage/...
```

## Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-filesearch
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mcp-filesearch
  template:
    metadata:
      labels:
        app: mcp-filesearch
    spec:
      containers:
        - name: filesearch
          image: ghcr.io/your-org/mcp-filesearch:latest
          env:
            - name: MCP_FILESEARCH_GEMINI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: gemini-credentials
                  key: apiKey
            - name: MCP_FILESEARCH_PUBLIC_URL
              value: "https://filesearch.example.com"
          ports:
            - containerPort: 8080
          volumeMounts:
            - name: data
              mountPath: /data
          livenessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 5
            periodSeconds: 10
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: filesearch-data
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: filesearch-data
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Service
metadata:
  name: mcp-filesearch
spec:
  selector:
    app: mcp-filesearch
  ports:
    - name: http
      port: 80
      targetPort: 8080
```

## Project Structure

```
/filesearch
├── cmd/
│   └── filesearch/           # Main entry point
├── internal/
│   ├── app/                  # Application coordinator
│   ├── config/               # Configuration management
│   ├── gemini/               # Gemini API client
│   ├── logging/              # Logging setup
│   ├── mcpserver/            # MCP server and tools
│   ├── storage/              # SQLite database layer
│   └── web/                  # HTTP server wrapper
├── migrations/               # SQL schema migrations
├── Dockerfile               # Container image definition
└── README.md                # This file
```

## License

See LICENSE file for details.
