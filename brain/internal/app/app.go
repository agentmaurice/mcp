package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/connector/filesystem"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer/embedder"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/logging"
	bufferclient "github.com/agentmaurice/mcpchatui/mcp/brain/internal/platform/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcptransport "github.com/agentmaurice/mcpchatui/mcp/brain/internal/transport/mcp"
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
	a.logger.Info("initializing brain server")

	// Storage
	manager, err := storage.NewManager(a.cfg.Storage, a.logger)
	if err != nil {
		return fmt.Errorf("failed to initialize storage backend: %w", err)
	}
	a.storage = manager

	// Embedding provider
	var embProvider embedder.Provider
	switch strings.ToLower(strings.TrimSpace(a.cfg.Embedding.Provider)) {
	case "ollama":
		embProvider = embedder.NewOllamaProvider(embedder.OllamaConfig{
			BaseURL:    a.cfg.Embedding.OllamaURL,
			Model:      a.cfg.Embedding.Model,
			Dimensions: a.cfg.Embedding.Dimensions,
			BatchSize:  a.cfg.Embedding.BatchSize,
		})
		a.logger.Info("embedding provider initialized",
			zap.String("provider", "ollama"),
			zap.String("model", a.cfg.Embedding.Model),
			zap.Int("dimensions", a.cfg.Embedding.Dimensions))
	case "openai", "openai-compatible":
		embProvider = embedder.NewOpenAIProvider(embedder.OpenAIConfig{
			BaseURL:    a.cfg.Embedding.OpenAIBaseURL,
			APIKey:     a.cfg.Embedding.OpenAIKey,
			Model:      a.cfg.Embedding.Model,
			Dimensions: a.cfg.Embedding.Dimensions,
			BatchSize:  a.cfg.Embedding.BatchSize,
		})
		a.logger.Info("embedding provider initialized",
			zap.String("provider", "openai-compatible"),
			zap.String("model", a.cfg.Embedding.Model),
			zap.Int("dimensions", a.cfg.Embedding.Dimensions))
	default:
		a.logger.Warn("no embedding provider configured, vector search will be unavailable")
	}

	// Connectors
	connectors := map[string]connector.Connector{
		"filesystem": filesystem.New(),
	}

	// Pipeline
	pipeline := indexer.NewPipeline(a.storage, connectors, embProvider, a.cfg, a.logger)

	var brainBufferClient *bufferclient.Client
	if a.cfg.Buffer.Enabled {
		brainBufferClient = bufferclient.NewClient(a.cfg.Buffer.ServiceURL, a.cfg.Buffer.Token)
		if brainBufferClient.Configured() {
			a.logger.Info("authenticated buffer client initialized")
		} else {
			a.logger.Warn("buffer is enabled but BUFFER_SERVICE_URL or BUFFER_TOKEN is missing")
		}
	}

	// Search orchestrator
	orchestrator := search.NewOrchestrator(a.storage, embProvider, a.cfg, a.logger)

	// Response wrapper
	responseWrapper := mcptransport.NewResponseWrapper(a.logger)

	// MCP Server
	mcpServer := mcptransport.NewServer(a.storage, a.cfg, responseWrapper, orchestrator, pipeline, brainBufferClient, a.logger)
	if err := mcpServer.Build(); err != nil {
		return fmt.Errorf("failed to build MCP server: %w", err)
	}
	a.mcp = mcpServer

	// Transport managers
	mode := strings.ToLower(strings.TrimSpace(transport))
	switch mode {
	case "sse":
		a.managers = []shared.Manager{
			mcptransport.NewSSEManager(a.mcp, a.cfg, a.logger),
		}
	case "stdio":
		a.managers = []shared.Manager{
			mcptransport.NewStdioManager(a.mcp, a.cfg.Storage.DefaultTenantID, a.logger),
		}
	case "both":
		a.managers = []shared.Manager{
			mcptransport.NewSSEManager(a.mcp, a.cfg, a.logger),
			mcptransport.NewStdioManager(a.mcp, a.cfg.Storage.DefaultTenantID, a.logger),
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

	for _, mgr := range a.managers {
		a.logger.Info("starting manager", zap.String("name", mgr.Name()))
		if err := mgr.Start(ctx); err != nil {
			return fmt.Errorf("failed to start %s: %w", mgr.Name(), err)
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
