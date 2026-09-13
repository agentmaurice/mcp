# AgentMaurice MCP Shared

Go module shared by the AgentMaurice MCP servers.

## Module

```text
github.com/agentmaurice/mcpchatui/mcp/shared
```

MCP repositories that use it declare the module and resolve it, in local
development, through the `../shared` sibling directory:

```go
require github.com/agentmaurice/mcpchatui/mcp/shared v0.0.0

replace github.com/agentmaurice/mcpchatui/mcp/shared => ../shared
```

## Packages

- `duckhttp`: minimal HTTP client for the DuckDB httpserver.
- `sidecar`: self-registration of MCP binaries launched by the sidecar.
- `version`: build metadata injected through `ldflags`.

## License

Apache-2.0.
