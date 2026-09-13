# MCP Moderator

Go-based MCP server that exposes a moderation tool powered by the Mistral API. It uses the official MCP Go SDK for modern stateless Streamable HTTP and keeps `mark3labs/mcp-go` for legacy SSE compatibility.

## Configuration

| Environment variable | Description | Default |
|----------------------|-------------|---------|
| `MCP_MODERATOR_ADDR` / `SERVER_ADDR` | HTTP listen address (`host:port`) | `:8080` |
| `MCP_MODERATOR_BASE_PATH` / `SERVER_BASE_PATH` | Base path for the modern endpoint and legacy `/sse` and `/message` endpoints | `/mcp/moderator` |
| `MCP_MODERATOR_PUBLIC_URL` / `SERVER_PUBLIC_URL` | Public URL advertised to MCP clients (optional) | *(empty)* |
| `MCP_MODERATOR_KEEP_ALIVE` | Enables periodic SSE ping messages | `true` |
| `MCP_MODERATOR_KEEP_ALIVE_INTERVAL` | Interval between SSE ping messages | `15s` |
| `MCP_MODERATOR_LOG_LEVEL` / `LOG_LEVEL` | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `MCP_MODERATOR_MISTRAL_URL` / `MISTRAL_API_URL` | Base URL for the Mistral API | `https://api.mistral.ai` |
| `MCP_MODERATOR_MISTRAL_API_KEY` / `MISTRAL_API_KEY` | **Mistral API key** (required) | *(none)* |
| `MCP_MODERATOR_MISTRAL_MODEL` / `MISTRAL_MODEL` | Moderation model to use | `mistral-moderation-latest` |
| `MCP_MODERATOR_MISTRAL_TIMEOUT` / `MISTRAL_TIMEOUT` | HTTP timeout when calling Mistral | `10s` |

## Local run

```bash
export MCP_MODERATOR_MISTRAL_API_KEY=sk-...
go build ./cmd/moderator
./moderator
```

Endpoints exposed by default:

- `GET  /health` – liveness probe.
- `GET  /ready` – readiness probe.
- `POST /mcp/moderator` – stateless Streamable HTTP JSON-RPC endpoint.
- `GET  /mcp/moderator/sse` – SSE stream for MCP messages.
- `POST /mcp/moderator/message` – JSON-RPC endpoint for `call_tool` requests.

## Tests

```
go test ./...
```

### Integration suite

An optional integration test calls the real Mistral moderation API for every `.txt` file inside an input directory and stores JSON responses in an output directory. It is guarded by the `integration` build tag and skipped unless an API key is available.

```
export MCP_MODERATOR_MISTRAL_API_KEY=sk-...
# optional overrides:
# export MCP_MODERATOR_IT_INPUT_DIR=custom/input
# export MCP_MODERATOR_IT_OUTPUT_DIR=custom/output
# export MCP_MODERATOR_IT_TABLE_FILE=custom/table.json
go test -tags=integration ./internal/integration/...
```

Default locations are `integration/testdata/input` and `integration/testdata/output`.
If your Mistral subscription exposes a different moderation model, set `MCP_MODERATOR_IT_MODEL` accordingly before running the test. A ready-made dataset is available in `integration/testdata/table.json`; override the path with `MCP_MODERATOR_IT_TABLE_FILE` if you maintain your own catalogue.

## Kubernetes deployment example

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-moderator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mcp-moderator
  template:
    metadata:
      labels:
        app: mcp-moderator
    spec:
      containers:
        - name: moderator
          image: ghcr.io/your-org/mcp-moderator:latest
          env:
            - name: MCP_MODERATOR_MISTRAL_API_KEY
              valueFrom:
                secretKeyRef:
                  name: mistral-credentials
                  key: apiKey
            - name: MCP_MODERATOR_PUBLIC_URL
              value: "https://moderator.example.com"
          ports:
            - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: mcp-moderator
spec:
  selector:
    app: mcp-moderator
  ports:
    - name: http
      port: 80
      targetPort: 8080
```

Expose the service URL (via Ingress, Gateway, etc.) through `MCP_MODERATOR_PUBLIC_URL` so MCP clients can build proper `/sse` and `/message` endpoints.
