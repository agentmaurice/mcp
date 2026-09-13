package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mcpserver"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mistral"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/web"
	"go.uber.org/zap"
)

// App wires configuration, logging, and the MCP server together.
type App struct {
	cfg     config.Config
	logger  *zap.Logger
	client  *mistral.Client
	server  Server
	started bool
}

// Server abstracts the subset of server operations needed by the app.
type Server interface {
	Start(addr string) error
	Shutdown(ctx context.Context) error
}

// New initialises the application state and underlying services.
func New(cfg config.Config, logger *zap.Logger) (*App, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	client, err := mistral.NewClient(cfg.Mistral, logger.Named("mistral"))
	if err != nil {
		return nil, fmt.Errorf("failed to create Mistral client: %w", err)
	}

	builder := mcpserver.NewBuilder(cfg, client, logger.Named("mcp"))
	sseServer, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to assemble MCP server: %w", err)
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		client: client,
		server: web.NewHTTPServer(cfg.Server, sseServer, builder.Streamable(), logger.Named("http")),
	}, nil
}

// Run starts the SSE server and blocks until the provided context is cancelled or the server fails.
func (a *App) Run(ctx context.Context) error {
	if a.started {
		return errors.New("app already running")
	}
	a.started = true

	errCh := make(chan error, 1)

	go func() {
		a.logger.Info("Starting MCP SSE server",
			zap.String("addr", a.cfg.Server.Addr),
			zap.String("base_path", a.cfg.Server.BasePath),
		)
		if err := a.server.Start(a.cfg.Server.Addr); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			a.logger.Error("graceful shutdown failed", zap.Error(err))
			return fmt.Errorf("failed to shutdown server: %w", err)
		}
		a.logger.Info("MCP SSE server stopped")
		return <-errCh
	case err := <-errCh:
		if err != nil {
			a.logger.Error("MCP SSE server crashed", zap.Error(err))
		}
		return err
	}
}
