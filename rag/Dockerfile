# Build stage
ARG GO_VERSION=1.26.0
FROM golang:${GO_VERSION}-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /src/mcp

# Copy go mod files
COPY shared ./shared
COPY rag/go.mod rag/go.sum ./rag/
WORKDIR /src/mcp/rag

# Download dependencies (allow automatic toolchain download for newer Go versions)
ENV GOTOOLCHAIN=auto
RUN go mod download

# Copy source code
WORKDIR /src/mcp
COPY rag ./rag
WORKDIR /src/mcp/rag

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o rag ./cmd/rag

# Final stage
FROM alpine:latest

# Install runtime dependencies (poppler-utils provides pdftotext for PDF extraction)
RUN apk --no-cache add ca-certificates poppler-utils

# Create app user
RUN addgroup -g 1000 app && \
    adduser -D -u 1000 -G app app

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /src/mcp/rag/rag .

# Change ownership
RUN chown -R app:app /app

# Switch to app user
USER app

# Expose port
EXPOSE 8080

# Run binary
CMD ["./rag"]
