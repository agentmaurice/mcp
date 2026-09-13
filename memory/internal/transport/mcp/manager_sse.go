package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"go.uber.org/zap"
)

// SSEManager wraps the SSE server lifecycle.
type SSEManager struct {
	server *Server
	cfg    *config.Config
	logger *zap.Logger
	cancel context.CancelFunc
	http   *http.Server
	done   chan struct{}
}

// NewSSEManager creates a new SSE manager.
func NewSSEManager(server *Server, cfg *config.Config, logger *zap.Logger) *SSEManager {
	return &SSEManager{
		server: server,
		cfg:    cfg,
		logger: logger.Named("sse"),
		done:   make(chan struct{}),
	}
}

func (m *SSEManager) Name() string {
	return "mcp-sse"
}

func (m *SSEManager) Start(ctx context.Context) error {
	if m.server == nil || m.server.SSE() == nil || m.server.Streamable() == nil {
		return fmt.Errorf("sse/streamable server not initialized")
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	ssePath := m.server.SSE().CompleteSsePath()
	messagePath := m.server.SSE().CompleteMessagePath()
	streamablePath := normalizeRoute(m.cfg.Server.BasePath)
	addr := m.cfg.Server.Address

	mux := http.NewServeMux()
	mux.Handle(ssePath, m.server.SSE().SSEHandler())
	mux.Handle(messagePath, m.server.SSE().MessageHandler())
	mux.Handle(streamablePath, m.server.Streamable())

	m.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		defer close(m.done)
		m.logger.Info("starting MCP HTTP transport",
			zap.String("address", addr),
			zap.String("sse_path", ssePath),
			zap.String("message_path", messagePath),
			zap.String("streamable_path", streamablePath))
		if err := m.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			m.logger.Error("SSE server stopped", zap.Error(err))
		}
	}()

	go func() {
		<-ctx.Done()
		if m.http != nil {
			_ = m.http.Shutdown(context.Background())
		}
	}()

	return nil
}

func (m *SSEManager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	if m.http != nil {
		_ = m.http.Shutdown(context.Background())
	}
	if m.done != nil {
		<-m.done
	}
	return nil
}

func normalizeRoute(route string) string {
	if route == "" {
		return "/mcp"
	}
	if !strings.HasPrefix(route, "/") {
		return "/" + route
	}
	return route
}
