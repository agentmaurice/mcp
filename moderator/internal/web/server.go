package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// HTTPServer exposes MCP transports alongside health probes using the standard net/http stack.
type HTTPServer struct {
	server *http.Server
	logger *zap.Logger
}

// NewHTTPServer assembles the HTTP mux and wires the health endpoints plus the MCP routes.
func NewHTTPServer(cfg config.ServerConfig, sse *server.SSEServer, streamable http.Handler, logger *zap.Logger) *HTTPServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("health check request",
			zap.String("method", r.Method),
			zap.String("remote_addr", r.RemoteAddr),
		)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("readiness check request",
			zap.String("method", r.Method),
			zap.String("remote_addr", r.RemoteAddr),
		)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Mount SSE endpoints supplied by the MCP server.
	ssePath := sanitizeRoute(path.Join(cfg.BasePath, "sse"))
	messagePath := sanitizeRoute(path.Join(cfg.BasePath, "message"))
	streamablePath := sanitizeRoute(cfg.BasePath)

	logger.Info("registering MCP endpoints",
		zap.String("sse_path", ssePath),
		zap.String("message_path", messagePath),
		zap.String("streamable_path", streamablePath),
	)

	mux.Handle(ssePath, loggingMiddleware(logger, "sse", sse.SSEHandler()))
	mux.Handle(messagePath, loggingMiddleware(logger, "message", sse.MessageHandler()))
	mux.Handle(streamablePath, corsMiddleware(loggingMiddleware(logger, "streamable", streamable)))

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	return &HTTPServer{
		server: httpServer,
		logger: logger,
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Mcp-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Session-Id, Last-Event-ID, X-Tenant-Id, X-User-Id, X-Scopes")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start runs the HTTP server and blocks until it exits.
func (s *HTTPServer) Start(addr string) error {
	s.logger.Info("HTTP server listening", zap.String("addr", addr))
	s.server.Addr = addr
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down HTTP server")
	return s.server.Shutdown(ctx)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func sanitizeRoute(route string) string {
	if route == "" {
		return "/"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	if len(route) > 1 && strings.HasSuffix(route, "/") {
		route = strings.TrimSuffix(route, "/")
	}
	return route
}

// loggingMiddleware wraps an http.Handler to log incoming requests with detailed information.
func loggingMiddleware(logger *zap.Logger, endpoint string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Read and log request body for POST requests (JSON-RPC messages)
		var bodyPreview string
		if r.Method == http.MethodPost && r.Body != nil {
			bodyBytes, err := io.ReadAll(r.Body)
			if err == nil {
				// Restore body for the next handler
				r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
				bodyPreview = truncateString(string(bodyBytes), 1000)
			}
		}

		logger.Debug("incoming request",
			zap.String("endpoint", endpoint),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("query", r.URL.RawQuery),
			zap.String("remote_addr", r.RemoteAddr),
			zap.String("user_agent", r.UserAgent()),
			zap.String("content_type", r.Header.Get("Content-Type")),
			zap.Int64("content_length", r.ContentLength),
			zap.String("x_forwarded_for", r.Header.Get("X-Forwarded-For")),
			zap.String("x_real_ip", r.Header.Get("X-Real-IP")),
			zap.String("body", bodyPreview),
		)

		// Wrap response writer to capture status code and response body
		wrapped := &responseWriterWrapper{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
			body:           &bytes.Buffer{},
		}

		next.ServeHTTP(wrapped, r)

		logger.Debug("request completed",
			zap.String("endpoint", endpoint),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status_code", wrapped.statusCode),
			zap.Duration("duration", time.Since(start)),
			zap.String("response_body", truncateString(wrapped.body.String(), 1000)),
		)
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture the status code and response body
// while preserving http.Flusher interface for SSE support.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriterWrapper) Write(b []byte) (int, error) {
	// Capture response body (only for non-SSE, limit size)
	if w.body != nil && w.body.Len() < 10000 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher to support SSE streaming.
func (w *responseWriterWrapper) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// truncateString truncates a string to maxLen characters for logging purposes.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
