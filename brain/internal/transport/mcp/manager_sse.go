package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// SSEManager manages the SSE transport lifecycle.
type SSEManager struct {
	srv    *Server
	cfg    *config.Config
	logger *zap.Logger
	http   *http.Server
}

// NewSSEManager creates a new SSE transport manager.
func NewSSEManager(srv *Server, cfg *config.Config, logger *zap.Logger) *SSEManager {
	return &SSEManager{srv: srv, cfg: cfg, logger: logger.Named("sse")}
}

func (m *SSEManager) Name() string { return "sse" }

func (m *SSEManager) Start(ctx context.Context) error {
	if m.srv == nil || m.srv.MCP() == nil || m.srv.Streamable() == nil {
		return fmt.Errorf("sse/streamable server not initialized")
	}
	basePath := m.cfg.Server.BasePath
	if basePath == "" {
		basePath = "/mcp/brain"
	}

	sseOptions := []server.SSEOption{
		server.WithStaticBasePath(basePath),
		server.WithKeepAlive(m.cfg.Server.KeepAlive),
		server.WithKeepAliveInterval(time.Duration(m.cfg.Server.KeepAliveInterval) * time.Second),
	}

	sseOptions = append(sseOptions, server.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		id := shared.IdentityFromRequest(r, m.cfg.Storage.DefaultTenantID)
		return shared.ContextWithIdentity(ctx, id)
	}))

	sseServer := server.NewSSEServer(m.srv.MCP(), sseOptions...)
	m.srv.sseServer = sseServer
	ssePath := sseServer.CompleteSsePath()
	messagePath := sseServer.CompleteMessagePath()
	streamablePath := normalizeRoute(basePath)
	mux := http.NewServeMux()
	mux.Handle(ssePath, sseServer.SSEHandler())
	mux.Handle(messagePath, sseServer.MessageHandler())
	mux.Handle(streamablePath, corsMiddleware(m.srv.Streamable()))

	addr := m.cfg.Server.Address
	if addr == "" {
		addr = ":8085"
	}

	m.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	m.logger.Info("starting MCP HTTP transport",
		zap.String("address", addr),
		zap.String("sse_path", ssePath),
		zap.String("message_path", messagePath),
		zap.String("streamable_path", streamablePath))

	go func() {
		if err := m.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			m.logger.Error("SSE server error", zap.Error(err))
		}
	}()

	return nil
}

func normalizeRoute(route string) string {
	if route == "" {
		return "/mcp/brain"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	if len(route) > 1 {
		route = strings.TrimSuffix(route, "/")
	}
	return route
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

func (m *SSEManager) Stop() error {
	if m.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.http.Shutdown(ctx); err != nil {
			return fmt.Errorf("SSE server shutdown error: %w", err)
		}
	}
	return nil
}
