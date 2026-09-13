package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/config"
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

	// Health endpoints
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Mount SSE endpoints supplied by the MCP server.
	ssePath := sanitizeRoute(path.Join(cfg.BasePath, "sse"))
	messagePath := sanitizeRoute(path.Join(cfg.BasePath, "message"))
	streamablePath := sanitizeRoute(cfg.BasePath)

	mux.Handle(ssePath, sse.SSEHandler())
	mux.Handle(messagePath, sse.MessageHandler())
	mux.Handle(streamablePath, corsMiddleware(streamable))

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // No timeout for SSE
		WriteTimeout:      0, // No timeout for SSE
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
