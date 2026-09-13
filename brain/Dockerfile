FROM golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS builder

WORKDIR /src/mcp
COPY shared ./shared
COPY brain/go.mod brain/go.sum ./brain/
WORKDIR /src/mcp/brain
RUN --mount=type=cache,target=/go/pkg/mod go mod download

WORKDIR /src/mcp
COPY brain ./brain
WORKDIR /src/mcp/brain
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -tags duckdb -ldflags="-s -w" -o brain ./cmd/brain

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN groupadd -g 1000 app && \
    useradd -u 1000 -g app -m app && \
    mkdir -p /var/lib/brain && \
    chown app:app /var/lib/brain
COPY --chown=app:app --chmod=755 --from=builder /src/mcp/brain/brain .
COPY --chown=app:app --from=builder /src/mcp/brain/configs ./configs

EXPOSE 8085
ENV BRAIN_BASE_DIR=/var/lib/brain
USER app

ENTRYPOINT ["./brain"]
CMD ["-transport=sse"]
