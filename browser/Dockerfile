# Build stage
ARG GO_VERSION=1.26.0
FROM golang:${GO_VERSION}-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /src/mcp

# Copy go mod files
COPY shared ./shared
COPY browser/go.mod browser/go.sum ./browser/
WORKDIR /src/mcp/browser

# Download dependencies (allow automatic toolchain download for newer Go versions)
ENV GOTOOLCHAIN=auto
RUN go mod download

# Copy source code
WORKDIR /src/mcp
COPY browser ./browser
WORKDIR /src/mcp/browser

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o browser ./cmd/browser

# Final stage
FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
ARG LIGHTPANDA_URL=https://github.com/lightpanda-io/browser/releases/download/0.3.6/lightpanda-x86_64-linux
ARG LIGHTPANDA_SHA256=e438c0ad44e0f6916c14cf13beb003512c60438d8fd200738d2e596e73f652d6

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    wget \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Download and install Lightpanda
RUN mkdir -p /opt/lightpanda && \
    wget -q -O /opt/lightpanda/lightpanda "${LIGHTPANDA_URL}" && \
    echo "${LIGHTPANDA_SHA256}  /opt/lightpanda/lightpanda" | sha256sum -c - && \
    chmod +x /opt/lightpanda/lightpanda

# Create app user
RUN groupadd -g 1000 app && \
    useradd -u 1000 -g app -m app

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --chown=app:app --chmod=755 --from=builder /src/mcp/browser/browser .

# Copy configuration
COPY --chown=app:app browser/configs/config.json ./configs/config.json

# Copy start script
COPY --chown=app:app --chmod=755 browser/docker/start.sh ./start.sh

# Switch to app user
USER app

# Expose ports
# 3000 - MCP Server (SSE)
# 9222 - CDP (Chrome DevTools Protocol)
EXPOSE 3000 9222

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:3000/health || exit 1

# Run
CMD ["./start.sh"]
