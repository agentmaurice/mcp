# MCP Sidecar

Runtime sidecar for AgentMaurice MCP servers: a standalone Go binary that
registers an MCP server with an AgentMaurice instance (bootstrap token), stores
and renews its credentials, exposes a health status, and relays MCP calls
between the instance (MQTT) and the local server (stdio).

The `*-sidecar` images of the other MCP servers are built `FROM ${SIDECAR_IMAGE}`
on top of the base image produced here.

## Images

| Image | Contents |
|-------|----------|
| `mcp-sidecar` | binary only (Alpine, user `10001`) — base of the `<server>-sidecar` images |
| `mcp-sidecar-python` | + `python3`, `pip`, `git`: runs a Python MCP server |
| `mcp-sidecar-node` | + `nodejs`, `npm`, `git`: runs a Node MCP server |
| `mcp-sidecar-go` | + `go`, `git`: builds and runs a Go MCP server |
| `mcp-sidecar-docker` | + `docker-cli`, `git`: runs a containerized MCP server |

Published as `ghcr.io/agentmaurice/mcp/<image>:<version>`.

## Build

```bash
go build -o bin/mcp-sidecar ./cmd/sidecar
go test ./...

docker build -t mcp-sidecar:dev .
docker build --build-arg SIDECAR_BASE_IMAGE=mcp-sidecar:dev \
  -f docker/python.Dockerfile -t mcp-sidecar-python:dev docker
```

## Usage

```bash
mcp-sidecar register --maurice-url https://<instance> --bootstrap-token <token> --mcp-type memory
mcp-sidecar start
mcp-sidecar status
```

Every option is also available as an `MCP_SIDECAR_*` environment variable
(`mcp-sidecar --help`).

## Layout

- `cmd/sidecar` — entry point
- `cli` — `register`, `start`, `status`, `revoke` commands
- `config` — configuration (file, env, flags), TLS/mTLS
- `identity` — credential registration, storage and renewal
- `client` — HTTP client for the AgentMaurice API
- `mqtt` — MQTT ↔ local MCP runtime (zstd compression, payload limits)
- `bootstrap` — preparation of a third-party runtime (python/node/go/docker)
- `healthcheck` — health probe
- `docker/` — Dockerfiles of the runtime variants

## License

Apache-2.0.
