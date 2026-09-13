# AgentMaurice MCP servers

Open-source [Model Context Protocol](https://modelcontextprotocol.io) servers
built by [AgentMaurice](https://agentmaurice.ai), written in Go, published
under the Apache-2.0 license. Anyone can build, run and embed them for free.

This repository is a read-only snapshot: the canonical development happens on
private GitLab repositories, and every tagged release is synchronised here
(see `MANIFEST.json` for the exact upstream commit of each server).
Issues and pull requests are welcome; maintainers will port accepted changes
upstream and re-publish.

Last sync: 2026-09-13.

## Servers

| Server | Version | Container images | Description |
|--------|---------|------------------|-------------|
| [api](api) | v0.1.3 | `ghcr.io/agentmaurice/mcp/api*:v0.1.3` | Contract-first HTTP API calls through approved OpenAPI operation identifiers and bounded egress controls. |
| [artifact](artifact) | v0.1.3 | `ghcr.io/agentmaurice/mcp/artifact*:v0.1.3` | Deterministic creation, patching, rendering and inspection of bounded business artifacts. |
| [brain](brain) | v1.1.5 | `ghcr.io/agentmaurice/mcp/brain*:v1.1.5` | Go MCP server that indexes an organization's knowledge (code, documentation, |
| [browser](browser) | v1.0.29 | `ghcr.io/agentmaurice/mcp/browser*:v1.0.29` | A Model Context Protocol (MCP) server for browser automation, built in Go. This server enables AI agents to control a web browser through a  |
| [data](data) | v0.1.3 | `ghcr.io/agentmaurice/mcp/data*:v0.1.3` | Deterministic profiling, closed-plan querying and export of bounded CSV and tabular JSON data. |
| [document](document) | v0.1.1 | `ghcr.io/agentmaurice/mcp/document*:v0.1.1` | MCP Document converts office documents to Markdown locally for AgentMaurice |
| [filesearch](filesearch) | v0.1.0 | `ghcr.io/agentmaurice/mcp/filesearch*:v0.1.0` | Go-based MCP server that provides file search capabilities using Google Gemini FileSearch API. It is designed to plug into MCPChatUI and fol |
| [guard](guard) | v0.1.3 | `ghcr.io/agentmaurice/mcp/guard*:v0.1.3` | Local detection, redaction and classification of PII, secrets and sensitive business data. |
| [memory](memory) | v1.0.14 | `ghcr.io/agentmaurice/mcp/memory*:v1.0.14` | MCP server providing a multi-tenant, read-safe SQL memory backed by DuckDB or PostgreSQL. Writes are handled by strict ingestion tools (no a |
| [moderator](moderator) | v1.0.1 | `ghcr.io/agentmaurice/mcp/moderator*:v1.0.1` | Go-based MCP server that exposes a moderation tool powered by the Mistral API. It uses the official MCP Go SDK for modern stateless Streamab |
| [observe](observe) | v0.1.3 | `ghcr.io/agentmaurice/mcp/observe*:v0.1.3` | Bounded and redacted runtime trace normalization, comparison and deterministic diagnostics. |
| [ocr](ocr) | v0.1.2 | `ghcr.io/agentmaurice/mcp/ocr*:v0.1.2` | Managed, synchronous OCR MCP server. The service holds no provider key: it |
| [rag](rag) | v1.0.18 | `ghcr.io/agentmaurice/mcp/rag*:v1.0.18` | A production-ready RAG (Retrieval-Augmented Generation) server implementing the Model Context Protocol (MCP). |
| [search](search) | v0.1.0 | `ghcr.io/agentmaurice/mcp/search*:v0.1.0` | Recherche hybride Meilisearch et passages citables. Configuration opérateur requise : moteur séparé, clé search et processus dédié à un principal/corpus homogène en droits. |
| [shared](shared) | main | — | Go module shared by the AgentMaurice MCP servers. |
| [sidecar](sidecar) | v0.1.0 | `ghcr.io/agentmaurice/mcp/sidecar*:v0.1.0` | Runtime sidecar des serveurs MCP AgentMaurice : un binaire Go autonome qui |
| [ssh](ssh) | v0.1.1 | `ghcr.io/agentmaurice/mcp/ssh*:v0.1.1` | Governed synchronous SSH administration over pre-published remote targets with strict host-key verification. |
| [system](system) | v0.1.2 | `ghcr.io/agentmaurice/mcp/system*:v0.1.2` | Bounded VM diagnostics and governed allowlisted system actions through a local Unix-socket host agent. |

`shared` is the common library used by the other servers; it is not a
server by itself.

Machine-readable index of every shipped server, sidecar image and One
compatibility flag: [`catalog.json`](catalog.json). AgentMaurice One and
other local runtimes should read that file instead of calling Console.

## Container images

Every release is published on the GitHub Container Registry under
`ghcr.io/agentmaurice/mcp/<image>:<version>`, with the **same digest** as the image
run in production by AgentMaurice.

Two kinds of images exist:

- **standalone** images (for example `memory-duckdb`, `memory-postgres`,
  `brain`, `browser`, `rag`, `filesearch`, `moderator`) run the server alone,
  over stdio or HTTP, for any MCP client;
- **`-sidecar`** images bundle the server with the AgentMaurice
  **MCP sidecar runtime**, which registers the server to an AgentMaurice
  instance (credentials, transport, health). The sidecar runtime is published
  as `ghcr.io/agentmaurice/mcp/mcp-sidecar:<version>` (plus the `-python`, `-node`,
  `-go` and `-docker` variants used to run third-party MCP servers).

```bash
docker run --rm -i ghcr.io/agentmaurice/mcp/memory-duckdb:<version>
```

Configuration is documented in each server's `README.md` and `configs/`.

## Building from source

Go 1.26 or later. The repository ships a `go.work` so that every module
resolves `shared` locally:

```bash
git clone https://github.com/agentmaurice/mcp.git
cd mcp
go build -o bin/mcp-memory ./memory/cmd/memory
```

Container images build from the repository root (the Dockerfiles copy
`shared/` next to the server):

```bash
docker build -f memory/Dockerfile -t mcp-memory .
docker build -f api/Dockerfile.sidecar \
  --build-arg SIDECAR_IMAGE=ghcr.io/agentmaurice/mcp/mcp-sidecar:<sidecar-version> \
  -t mcp-api-sidecar .
```

Run the tests of a server from its directory:

```bash
cd memory && go test ./...
```

## Versioning

Tags follow `<name>/vX.Y.Z` and mirror the upstream release tags. The
container image for a server is tagged with the same `vX.Y.Z`.

## License

Copyright AgentMaurice. Licensed under the Apache License, Version 2.0 — see
[LICENSE](LICENSE).
