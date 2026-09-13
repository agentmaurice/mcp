package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/jobqueue"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	mcptransport "github.com/agentmaurice/mcpchatui/mcp/rag/internal/transport/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// Server implements HTTP server with SSE for MCP
type Server struct {
	httpServer        *http.Server
	sseServer         *server.SSEServer
	mcpServer         *mcptransport.Server
	ragManager        *business.RAGManager
	ingestManager     *business.IngestManager
	tenantRepo        *repository.TenantRepository
	deployRepo        *repository.DeploymentRepository
	dlqReplayer       jobqueue.DLQReplayer
	dlqInspector      jobqueue.DLQInspector
	db                *sql.DB
	activeSSESessions atomic.Int64
	config            config.ServerConfig
	logger            *zap.Logger
}

// NewServer creates a new HTTP server
func NewServer(
	mcpServer *mcptransport.Server,
	ragManager *business.RAGManager,
	ingestManager *business.IngestManager,
	tenantRepo *repository.TenantRepository,
	deployRepo *repository.DeploymentRepository,
	dlqReplayer jobqueue.DLQReplayer,
	dlqInspector jobqueue.DLQInspector,
	db *sql.DB,
	cfg config.ServerConfig,
	zapLogger *zap.Logger,
) *Server {
	return &Server{
		sseServer:     mcpServer.GetSSEServer(),
		mcpServer:     mcpServer,
		ragManager:    ragManager,
		ingestManager: ingestManager,
		tenantRepo:    tenantRepo,
		deployRepo:    deployRepo,
		dlqReplayer:   dlqReplayer,
		dlqInspector:  dlqInspector,
		db:            db,
		config:        cfg,
		logger:        zapLogger.Named("http-server"),
	}
}

// Build builds the HTTP server and routes
func (s *Server) Build() error {
	s.logger.Info("building HTTP server")

	mux := http.NewServeMux()

	// Health endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)

	// Mount SSE endpoints supplied by the MCP server
	basePath := s.config.BasePath
	if basePath == "" {
		basePath = "/mcp"
	}

	ssePath := sanitizeRoute(path.Join(basePath, "sse"))
	messagePath := sanitizeRoute(path.Join(basePath, "message"))
	streamablePath := sanitizeRoute(basePath)

	s.logger.Info("mounting MCP SSE endpoints",
		zap.String("sse_path", ssePath),
		zap.String("message_path", messagePath),
		zap.String("streamable_path", streamablePath))

	// Use the native SSE handlers from mcp-go, wrapped with session tracking
	mux.Handle(ssePath, s.trackSSEConnections(s.sseServer.SSEHandler()))
	mux.Handle(messagePath, s.sseServer.MessageHandler())
	mux.Handle(streamablePath, corsMiddleware(s.mcpServer.GetStreamableHandler()))

	// REST API endpoints with CORS wrapper
	mux.Handle("/api/ingest", corsMiddleware(http.HandlerFunc(s.handleIngestAPI)))
	mux.Handle("/api/ingest/", corsMiddleware(http.HandlerFunc(s.handleIngestStatusAPI)))
	mux.Handle("/api/query", corsMiddleware(http.HandlerFunc(s.handleQueryAPI)))
	mux.Handle("/api/dlq/messages", corsMiddleware(http.HandlerFunc(s.handleDLQMessagesAPI)))
	mux.Handle("/api/dlq/replay", corsMiddleware(http.HandlerFunc(s.handleDLQReplayAPI)))
	mux.Handle("/api/deployments/", corsMiddleware(http.HandlerFunc(s.handleDeploymentTenantsAPI)))

	// HTTP server configuration optimized for SSE
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // No timeout for SSE
		WriteTimeout:      0, // No timeout for SSE
		IdleTimeout:       120 * time.Second,
	}

	s.logger.Info("HTTP server built successfully")

	return nil
}

// Start starts the HTTP server
func (s *Server) Start(ctx context.Context) error {
	addr := s.config.Address
	if addr == "" {
		addr = ":8084"
	}

	s.logger.Info("starting HTTP server", zap.String("address", addr))

	s.httpServer.Addr = addr

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	return nil
}

// Stop stops the HTTP server
func (s *Server) Stop() error {
	s.logger.Info("stopping HTTP server")

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(ctx)
	}

	return nil
}

// Name returns the server name
func (s *Server) Name() string {
	return "http-server"
}

// Health checks the server health
func (s *Server) Health(ctx context.Context) error {
	return nil
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"server": "rag-mcp-server",
	})
}

// handleReady handles readiness check requests with deep health verification.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	healthy := true

	// Check database connectivity
	if s.db != nil {
		if err := s.db.PingContext(ctx); err != nil {
			checks["database"] = err.Error()
			healthy = false
		} else {
			checks["database"] = "ok"
		}
	} else {
		checks["database"] = "not_configured"
	}

	// Report MCP activity metrics
	if s.mcpServer != nil {
		lastReq := s.mcpServer.LastRequestAt()
		if !lastReq.IsZero() {
			checks["last_request_at"] = lastReq.Format(time.RFC3339)
		} else {
			checks["last_request_at"] = "never"
		}
		checks["requests_total"] = strconv.FormatInt(s.mcpServer.RequestCount(), 10)
	}

	// Report active SSE sessions
	checks["active_sse_sessions"] = strconv.FormatInt(s.activeSSESessions.Load(), 10)

	if !healthy {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"checks": checks,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"checks": checks,
	})
}

// trackSSEConnections wraps an HTTP handler to track active SSE session count.
func (s *Server) trackSSEConnections(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.activeSSESessions.Add(1)
		s.logger.Info("SSE session connected",
			zap.Int64("active_sessions", s.activeSSESessions.Load()))
		defer func() {
			s.activeSSESessions.Add(-1)
			s.logger.Info("SSE session disconnected",
				zap.Int64("active_sessions", s.activeSSESessions.Load()))
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// sanitizeRoute ensures route has proper format
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

// corsMiddleware adds CORS headers to responses
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Mcp-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Session-Id, Last-Event-ID")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
