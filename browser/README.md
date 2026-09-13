# Browser MCP Server

A Model Context Protocol (MCP) server for browser automation, built in Go. This server enables AI agents to control a web browser through a standardized protocol.

## Features

- **Full browser control** via Chrome DevTools Protocol (CDP)
- **MCP 2026-07-28** over stateless Streamable HTTP, with legacy SSE and STDIO compatibility
- **32 browser automation tools** for navigation, interaction, and content extraction
- **Self-hosted** with Docker support
- **Lightpanda integration** for lightweight headless browsing

## Architecture

```
┌─────────────────────────┐
│   AI Client (Claude)    │
└────────────┬────────────┘
             │ MCP (SSE/HTTP)
             ▼
┌─────────────────────────────────┐
│   Browser MCP Server (Go)       │
│   ├── MCP Tools                 │
│   └── CDP Client                │
└────────────┬────────────────────┘
             │ Chrome DevTools Protocol
             ▼
┌─────────────────────────────────┐
│   Lightpanda / Chrome / Chromium│
│   (Headless Browser)            │
└─────────────────────────────────┘
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `browser_navigate` | Navigate to a URL |
| `browser_go_back` | Navigate back in history |
| `browser_go_forward` | Navigate forward in history |
| `browser_reload` | Reload the current page |
| `browser_get_page_info` | Get current page URL and title |
| `browser_click` | Click on an element |
| `browser_fill` | Fill a form field with text |
| `browser_select_option` | Select an option from a dropdown |
| `browser_get_text` | Extract text content from an element |
| `browser_get_html` | Extract HTML content |
| `browser_get_attribute` | Get an attribute value from an element |
| `browser_screenshot` | Take a screenshot (inline image or buffered reference with `buffer_options`) |
| `browser_scroll` | Scroll the page or element |
| `browser_evaluate` | Execute JavaScript in the page |
| `browser_wait_for_selector` | Wait for an element to appear/disappear |
| `browser_snapshot` | Capture semantic refs (`@e1`, `@e2`, ...) for interactive elements |
| `browser_find` | Find semantic elements from the current snapshot |
| `browser_status` | Get browser and snapshot status |
| `browser_reconnect` | Reconnect to browser CDP endpoint |

All tools accept an optional `session_key` argument. Calls with the same `session_key` stay sticky on the same pooled browser instance and share snapshot refs.

## External Pool Orchestrator

Use the dedicated `pool-manager` process when you want orchestration outside the sidecar/container.

### Start pool-manager

```bash
BROWSER_POOL_MANAGER_ENABLE_DEFAULT_POOL=true \
BROWSER_POOL_MANAGER_DEFAULT_PROVIDER=static \
BROWSER_POOL_MANAGER_ENDPOINTS="ws://lightpanda-1:9222,ws://lightpanda-2:9222" \
BROWSER_POOL_MANAGER_ADDRESS=":8090" \
go run ./cmd/pool-manager
```

Optional hardening:

- `BROWSER_POOL_MANAGER_AUTH_TOKEN`
- `BROWSER_POOL_MANAGER_MAX_SESSIONS_PER_INSTANCE`
- `BROWSER_POOL_MANAGER_SESSION_IDLE_TTL`
- `BROWSER_POOL_MANAGER_SELECTION_POLICY`

Pool manager HTTP API:

- `POST /v1/pools/reconcile`
- `POST /v1/pools/{pool_id}/allocate-session`
- `POST /v1/pools/{pool_id}/release-session`
- `GET /v1/pools/{pool_id}/status`
- `DELETE /v1/pools/{pool_id}`

### Point browser-mcp to the orchestrator

```bash
BROWSER_BROWSER_POOL_MODE=external \
BROWSER_BROWSER_POOL_MANAGER_URL=http://pool-manager:8090 \
BROWSER_BROWSER_POOL_ID=default \
BROWSER_BROWSER_POOL_MANAGER_TOKEN=... \
go run ./cmd/browser
```

## Quick Start

### Prerequisites

- Go 1.24+
- Docker (optional, for containerized deployment)
- A CDP-compatible browser (Lightpanda, Chrome, Chromium)

### Local Development

1. Clone the repository:
```bash
cd mcp/browser
```

2. Install dependencies:
```bash
go mod download
```

3. Start a browser with CDP enabled:
```bash
# Option 1: Chrome/Chromium
google-chrome --remote-debugging-port=9222 --headless

# Option 2: Lightpanda (if installed)
lightpanda serve --host 127.0.0.1 --port 9222
```

4. Run the server:
```bash
go run ./cmd/browser
```

### Docker Deployment

1. Build and run with Docker:
```bash
cd docker
docker build -t browser-mcp -f Dockerfile ..
docker run -p 3000:3000 -p 9222:9222 browser-mcp
```

2. Or use Docker Compose:
```bash
cd docker
docker-compose up
```

## Configuration

Configuration is loaded from `configs/config.json` and can be overridden with environment variables.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `BROWSER_SERVER_ADDRESS` | Server bind address | `:3000` |
| `BROWSER_SERVER_SSE_PATH` | SSE endpoint path | `/mcp/sse` |
| `BROWSER_SERVER_MESSAGE_PATH` | Message endpoint path | `/mcp/message` |
| `BROWSER_BROWSER_CDP_ENDPOINT` | CDP WebSocket URL | `ws://127.0.0.1:9222` |
| `BROWSER_BROWSER_POOL_MODE` | Pool mode (`single`, `internal`, `external`) | `single` |
| `BROWSER_BROWSER_POOL_SIZE` | Number of local browser instances in `internal` mode | `1` |
| `BROWSER_BROWSER_POOL_ENDPOINTS` | Comma-separated CDP endpoints in `external` mode | `` |
| `BROWSER_BROWSER_POOL_MANAGER_URL` | Dedicated external pool-manager URL (e.g. `http://pool-manager:8090`) | `` |
| `BROWSER_BROWSER_POOL_MANAGER_TOKEN` | Optional bearer token for pool-manager API | `` |
| `BROWSER_BROWSER_POOL_MANAGER_TIMEOUT` | Timeout for pool-manager API calls | `2s` |
| `BROWSER_BROWSER_POOL_CLIENT_ID` | Optional client identifier used in pool-manager calls | `` |
| `BROWSER_BROWSER_POOL_ID` | Logical pool identifier when using external pool-manager | `default` |
| `BROWSER_BROWSER_MAX_SESSIONS_PER_INSTANCE` | Sticky sessions per instance (`0` = unlimited) | `0` |
| `BROWSER_BROWSER_SESSION_IDLE_TTL` | Idle TTL before session eviction | `10m` |
| `BROWSER_BROWSER_ACQUIRE_TIMEOUT` | Timeout to acquire a pooled session | `2s` |
| `BROWSER_BROWSER_MAX_QUEUE_DEPTH` | Max waiting acquire requests (`0` = unlimited) | `128` |
| `BROWSER_BROWSER_SELECTION_POLICY` | Pool selection policy (`least_loaded`, `round_robin`) | `least_loaded` |
| `BROWSER_BROWSER_DEFAULT_TIMEOUT` | Default operation timeout | `30s` |
| `BROWSER_BROWSER_NAVIGATION_TIMEOUT` | Navigation timeout | `60s` |
| `BROWSER_BUFFER_URL` | Buffer service base URL | `` |
| `BROWSER_BUFFER_NAMESPACE` | Default buffer namespace | `browser` |
| `BROWSER_BUFFER_SOFT_THRESHOLD_BYTES` | Buffer trigger threshold | `500000` |
| `BROWSER_LOGGING_LEVEL` | Log level (debug/info/warn/error) | `info` |
| `BROWSER_LOGGING_FORMAT` | Log format (json/console) | `json` |

### Config File Example

```json
{
  "server": {
    "address": ":3000",
    "sse_path": "/mcp/sse",
    "message_path": "/mcp/message",
    "health_path": "/health"
  },
  "browser": {
    "cdp_endpoint": "ws://127.0.0.1:9222",
    "headless": true,
    "pool_mode": "single",
    "pool_size": 1,
    "pool_endpoints": [],
    "pool_manager_url": "",
    "pool_manager_token": "",
    "pool_manager_timeout": "2s",
    "pool_client_id": "",
    "pool_id": "default",
    "max_sessions_per_instance": 0,
    "session_idle_ttl": "10m",
    "acquire_timeout": "2s",
    "max_queue_depth": 128,
    "selection_policy": "least_loaded",
    "default_timeout": "30s",
    "navigation_timeout": "60s",
    "screenshot_format": "png",
    "screenshot_quality": 80,
    "window_width": 1920,
    "window_height": 1080
  },
  "buffer": {
    "enabled": false,
    "url": "",
    "auth_token": "",
    "namespace": "browser",
    "soft_threshold_bytes": 500000,
    "hard_threshold_bytes": 1000000,
    "max_upload_bytes": 20000000,
    "ttl_seconds": 600,
    "network_retries": 1,
    "timeout": "10s"
  },
  "logging": {
    "level": "info",
    "format": "json",
    "output_path": "stdout"
  }
}
```

### Screenshot `buffer_options`

`browser_screenshot` accepts an optional `buffer_options` object.  
When `buffer_options.enabled=true` and payload size is above `soft_threshold_bytes`, image data is uploaded to `POST {url}/buffer` and the tool returns a `buffer://<key>` reference instead of inline base64.

Required fields when enabled:

- `buffer_options.url`
- `buffer_options.auth_token` (sent as `Authorization: Bearer <token>`)

Optional overrides:

- `namespace`, `summary`
- `soft_threshold_bytes`, `hard_threshold_bytes`, `max_upload_bytes`
- `ttl_seconds`, `network_retries`, `timeout_ms`

## Integration with AI Agents

### Claude Desktop Configuration

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "browser": {
      "url": "http://localhost:3000/mcp"
    }
  }
}
```

### AgentMaurice Integration

The Browser MCP server can be registered as a remote MCP server in AgentMaurice:

```go
// In your agent configuration
mcpClient := mcp.NewClient("http://localhost:3000")
tools, _ := mcpClient.ListTools(ctx)
```

## Project Structure

```
browser/
├── cmd/browser/           # Main entry point
│   └── main.go
├── internal/
│   ├── app/              # Application bootstrap
│   │   └── app.go
│   ├── browser/          # CDP client
│   │   └── client.go
│   ├── business/         # Business logic
│   │   └── browser_manager.go
│   ├── config/           # Configuration
│   │   └── config.go
│   ├── shared/           # Shared types & interfaces
│   │   ├── errors.go
│   │   ├── interfaces.go
│   │   └── types.go
│   └── transport/
│       └── mcp/          # MCP server & tools
│           ├── server.go
│           ├── helpers.go
│           ├── tool_navigate.go
│           ├── tool_click.go
│           ├── tool_fill.go
│           ├── tool_get_text.go
│           └── tool_screenshot.go
├── configs/
│   └── config.json
├── docker/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── start.sh
├── go.mod
├── go.sum
├── README.md
└── agent.md
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/mcp` | POST | Stateless Streamable HTTP (MCP 2026-07-28 and legacy requests) |
| `/mcp/sse` | GET | SSE connection for MCP protocol |
| `/mcp/message` | POST | Message endpoint for MCP requests |
| `/health` | GET | Health check endpoint |

## Health Check

```bash
curl http://localhost:3000/health
# {"status":"healthy"}
```

## Development

### Building

```bash
go build -o browser-mcp ./cmd/browser
```

### Testing

```bash
go test ./...
```

### Running in Debug Mode

```bash
BROWSER_LOGGING_LEVEL=debug BROWSER_LOGGING_FORMAT=console go run ./cmd/browser
```

## Dependencies

| Library | Purpose |
|---------|---------|
| `github.com/chromedp/chromedp` | CDP client for browser control |
| `github.com/modelcontextprotocol/go-sdk` | MCP 2026-07-28 Streamable HTTP implementation |
| `github.com/mark3labs/mcp-go` | Legacy SSE and STDIO compatibility |
| `github.com/spf13/viper` | Configuration management |
| `go.uber.org/zap` | Structured logging |
| `github.com/rs/xid` | Distributed unique IDs |

## License

MIT License - see LICENSE file for details.
