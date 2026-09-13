# ---- STAGE 1: Build Go binary ----
FROM golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS builder

WORKDIR /src/mcp

# Copy go mod files
COPY shared ./shared
COPY browser/go.mod browser/go.sum ./browser/
WORKDIR /src/mcp/browser
RUN go mod download

# Copy source code
WORKDIR /src/mcp
COPY browser ./browser
WORKDIR /src/mcp/browser

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /browser-mcp ./cmd/browser

# ---- STAGE 2: Final image ----
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

# Install dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    wget \
    curl \
    gnupg \
    --no-install-recommends && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ARG LIGHTPANDA_URL=https://github.com/lightpanda-io/browser/releases/download/0.3.6/lightpanda-x86_64-linux
ARG LIGHTPANDA_SHA256=e438c0ad44e0f6916c14cf13beb003512c60438d8fd200738d2e596e73f652d6
RUN mkdir -p /opt/lightpanda && \
    wget -q -O /opt/lightpanda/lightpanda "${LIGHTPANDA_URL}" && \
    echo "${LIGHTPANDA_SHA256}  /opt/lightpanda/lightpanda" | sha256sum -c - && \
    chmod +x /opt/lightpanda/lightpanda

# Copy Go binary
RUN groupadd -g 1000 app && useradd -u 1000 -g app -m app
COPY --chmod=755 --from=builder /browser-mcp /usr/local/bin/browser-mcp

# Copy configuration
WORKDIR /app
COPY --chown=app:app browser/configs/config.json ./configs/config.json

# Copy start script
COPY --chmod=755 browser/docker/start.sh /start.sh

# Expose ports
# 3000 - MCP Server (SSE)
# 9222 - CDP (Chrome DevTools Protocol) - for debug
EXPOSE 3000 9222

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:3000/health || exit 1

# Run
USER app
CMD ["/start.sh"]
