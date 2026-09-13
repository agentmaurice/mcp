# ---- builder ----
ARG GO_VERSION=1.26.0
FROM golang:${GO_VERSION}-bookworm AS builder

# deps CGO
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl unzip \
    build-essential pkg-config \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src/mcp

# 1) deps Go (cache)
COPY shared ./shared
COPY memory/go.mod memory/go.sum ./memory/
WORKDIR /src/mcp/memory
ENV GOTOOLCHAIN=auto
RUN go mod download

# 2) sources
WORKDIR /src/mcp
COPY memory ./memory
WORKDIR /src/mcp/memory

# 3) build Go (CGO, static duckdb from module deps)
ENV CGO_ENABLED=1
RUN go build -trimpath -tags duckdb -ldflags="-s -w" -o /out/mcp-memory ./cmd/memory

# ---- runtime ----
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# app
COPY --from=builder /out/mcp-memory /usr/local/bin/mcp-memory

# data volume
RUN mkdir -p /var/lib/agentmaurice && useradd -m appuser \
 && chown -R appuser:appuser /var/lib/agentmaurice

USER appuser
ENV MEMORY_BASE_DIR=/var/lib/agentmaurice
ENV MEMORY_STORAGE_BACKEND=duckdb
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mcp-memory"]
