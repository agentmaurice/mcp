package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/logging"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/platform/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	mcptransport "github.com/agentmaurice/mcpchatui/mcp/memory/internal/transport/mcp"
	"go.uber.org/zap"
)

// App coordinates the application lifecycle.
type App struct {
	cfg      *config.Config
	logger   *zap.Logger
	storage  storage.Manager
	mcp      *mcptransport.Server
	managers []shared.Manager
}

// New creates a new App instance.
func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	logger, err := logging.NewWithConfig(logging.Config{
		Level:      cfg.Logging.Level,
		EnableFile: cfg.Logging.EnableFile,
		LogDir:     cfg.Logging.LogDir,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	return &App{cfg: cfg, logger: logger}, nil
}

// Initialize builds dependencies and transports.
func (a *App) Initialize(ctx context.Context, transport string) error {
	a.logger.Info("initializing memory server")

	manager, err := storage.NewManager(a.cfg.Storage, a.logger)
	if err != nil {
		return fmt.Errorf("failed to initialize storage backend: %w", err)
	}
	a.storage = manager

	var responseWrapper *mcptransport.ResponseWrapper
	if a.cfg.Buffer.Enabled {
		a.logger.Info("initializing buffer client",
			zap.String("service_url", a.cfg.Buffer.ServiceURL),
			zap.String("namespace", a.cfg.Buffer.Namespace),
			zap.Int64("soft_threshold", a.cfg.Buffer.SoftThreshold),
			zap.Int64("hard_threshold", a.cfg.Buffer.HardThreshold),
		)

		bufferClient := buffer.NewClient(buffer.ClientConfig{
			ServiceURL:    a.cfg.Buffer.ServiceURL,
			Token:         a.cfg.Buffer.Token,
			Namespace:     a.cfg.Buffer.Namespace,
			SoftThreshold: a.cfg.Buffer.SoftThreshold,
			HardThreshold: a.cfg.Buffer.HardThreshold,
			DefaultTTL:    a.cfg.Buffer.DefaultTTL,
			Logger:        a.logger,
		})

		responseWrapper = mcptransport.NewResponseWrapper(bufferClient, a.logger)
		a.logger.Info("buffer service enabled for large payload handling")
	} else {
		a.logger.Info("buffer service disabled - large payloads will be returned directly")
		responseWrapper = mcptransport.NewResponseWrapper(nil, a.logger)
	}

	mcpServer := mcptransport.NewServer(a.storage, a.cfg, responseWrapper, a.logger)
	if err := mcpServer.Build(); err != nil {
		return fmt.Errorf("failed to build MCP server: %w", err)
	}
	a.mcp = mcpServer

	mode := strings.ToLower(strings.TrimSpace(transport))
	switch mode {
	case "sse":
		a.managers = []shared.Manager{
			mcptransport.NewSSEManager(a.mcp, a.cfg, a.logger),
		}
	case "stdio":
		a.managers = []shared.Manager{
			mcptransport.NewStdioManager(a.mcp, a.logger),
		}
	case "both":
		a.managers = []shared.Manager{
			mcptransport.NewSSEManager(a.mcp, a.cfg, a.logger),
			mcptransport.NewStdioManager(a.mcp, a.logger),
		}
	default:
		return fmt.Errorf("unknown transport mode: %s", transport)
	}

	return nil
}

// Run starts managers and blocks until shutdown.
func (a *App) Run(ctx context.Context, transport string) error {
	if err := a.Initialize(ctx, transport); err != nil {
		return err
	}

	for _, manager := range a.managers {
		a.logger.Info("starting manager", zap.String("name", manager.Name()))
		if err := manager.Start(ctx); err != nil {
			return fmt.Errorf("failed to start %s: %w", manager.Name(), err)
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		a.logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	case <-ctx.Done():
		a.logger.Info("context cancelled")
	}

	return a.Stop()
}

// Stop stops managers and closes resources.
func (a *App) Stop() error {
	for i := len(a.managers) - 1; i >= 0; i-- {
		mgr := a.managers[i]
		if err := mgr.Stop(); err != nil {
			a.logger.Warn("failed to stop manager", zap.String("name", mgr.Name()), zap.Error(err))
		}
	}

	if a.storage != nil {
		_ = a.storage.Close()
	}

	return nil
}
