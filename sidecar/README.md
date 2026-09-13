# MCP Sidecar

Runtime sidecar des serveurs MCP AgentMaurice : un binaire Go autonome qui
enregistre un serveur MCP auprès d'une instance AgentMaurice (token
bootstrap), conserve et renouvelle ses credentials, expose un état de santé
et relaie les appels MCP entre l'instance (MQTT) et le serveur local (stdio).

Les images `*-sidecar` des autres serveurs MCP sont construites
`FROM ${SIDECAR_IMAGE}` à partir de l'image de base produite ici.

## Images

| Image | Contenu |
|-------|---------|
| `mcp-sidecar` | binaire seul (Alpine, utilisateur `10001`) — base des images `<serveur>-sidecar` |
| `mcp-sidecar-python` | + `python3`, `pip`, `git` : exécute un serveur MCP Python |
| `mcp-sidecar-node` | + `nodejs`, `npm`, `git` : exécute un serveur MCP Node |
| `mcp-sidecar-go` | + `go`, `git` : compile et exécute un serveur MCP Go |
| `mcp-sidecar-docker` | + `docker-cli`, `git` : lance un serveur MCP conteneurisé |

Publiées sous `ghcr.io/agentmaurice/mcp/<image>:<version>`.

## Construire

```bash
go build -o bin/mcp-sidecar ./cmd/sidecar
go test ./...

docker build -t mcp-sidecar:dev .
docker build --build-arg SIDECAR_BASE_IMAGE=mcp-sidecar:dev \
  -f docker/python.Dockerfile -t mcp-sidecar-python:dev docker
```

## Utiliser

```bash
mcp-sidecar register --maurice-url https://<instance> --bootstrap-token <token> --mcp-type memory
mcp-sidecar start
mcp-sidecar status
```

Toutes les options existent en variables d'environnement `MCP_SIDECAR_*`
(`mcp-sidecar --help`). Le guide complet est dans [docs/guide.md](docs/guide.md).

## Layout

- `cmd/sidecar` — point d'entrée
- `cli` — commandes `register`, `start`, `status`, `revoke`
- `config` — configuration (fichier, env, flags), TLS/mTLS
- `identity` — enregistrement, stockage et renouvellement des credentials
- `client` — client HTTP de l'API AgentMaurice
- `mqtt` — runtime MQTT ↔ MCP local (compression zstd, limites de payload)
- `bootstrap` — préparation d'un runtime tiers (python/node/go/docker)
- `healthcheck` — sonde de santé
- `docker/` — Dockerfiles des variantes runtime

## Licence

Apache-2.0.
