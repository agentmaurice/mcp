# syntax=docker/dockerfile:1.6
# Dockerfile.sidecar - Multi-stage build for the MCP Sidecar binary
#
# The MCP Sidecar handles autonomous registration, credential management,
# and health monitoring for MCP servers connecting to AgentMaurice.

# Version injection ARGs
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

# ============================================
# Stage 1: Build the sidecar binary
# ============================================
FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder

ARG VERSION
ARG GIT_COMMIT
ARG BUILD_TIME

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY --link . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w" \
    -o /mcp_sidecar ./cmd/sidecar/

# ============================================
# Stage 2: Minimal runtime image
# ============================================
FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && install -d -o app -g app -m 0700 /var/mcp/credentials /tmp/mcp-sidecar-workspace

COPY --chmod=0555 --chown=10001:10001 --from=builder /mcp_sidecar /usr/local/bin/mcp_sidecar

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/mcp_sidecar"]
CMD ["start"]
