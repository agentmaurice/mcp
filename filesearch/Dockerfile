# syntax=docker/dockerfile:1

ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /app/bin/filesearch ./cmd/filesearch

FROM gcr.io/distroless/static-debian12:nonroot

ENV MCP_FILESEARCH_ADDR=:8080
EXPOSE 8080

VOLUME ["/data"]

COPY --from=builder /app/bin/filesearch /usr/local/bin/filesearch

ENTRYPOINT ["/usr/local/bin/filesearch"]
