package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
	mcpserver "github.com/agentmaurice/mcpchatui/mcp/browser/internal/transport/mcp"
)

// App represents the main application
type App struct {
	config         *config.Config
	logger         *zap.Logger
	transportMode  string
	browserManager *business.BrowserManager
	mcpServer      *mcpserver.Server
	managers       []shared.Manager
}

// New creates a new application instance
func New(configPath string, transportMode string) (*App, error) {
	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	mode := normalizeTransportMode(transportMode)

	// Initialize logger
	logger, err := initLogger(cfg.Logging, mode)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	return &App{
		config:        cfg,
		logger:        logger,
		transportMode: mode,
	}, nil
}

// Initialize initializes all application components
func (a *App) Initialize() error {
	a.logger.Info("initializing application")

	// Initialize browser manager
	a.browserManager = business.NewBrowserManager(&a.config.Browser, a.logger)
	a.managers = append(a.managers, a.browserManager)

	// Initialize MCP server
	a.mcpServer = mcpserver.NewServer(a.browserManager, &a.config.Server, &a.config.Buffer, a.transportMode, a.logger)
	a.managers = append(a.managers, a.mcpServer)

	a.logger.Info("application initialized", zap.String("transport_mode", a.transportMode))
	return nil
}

// Start starts all managers in order
func (a *App) Start(ctx context.Context) error {
	a.logger.Info("starting application")

	for _, manager := range a.managers {
		a.logger.Info("starting manager", zap.String("name", manager.Name()))
		if err := manager.Start(ctx); err != nil {
			return fmt.Errorf("failed to start %s: %w", manager.Name(), err)
		}
	}

	a.logger.Info("application started",
		zap.String("address", a.config.Server.Address),
		zap.String("sse_path", a.config.Server.SSEPath),
		zap.String("transport_mode", a.transportMode))
	return nil
}

// Stop stops all managers in reverse order
func (a *App) Stop() error {
	a.logger.Info("stopping application")

	// Stop managers in reverse order
	for i := len(a.managers) - 1; i >= 0; i-- {
		manager := a.managers[i]
		a.logger.Info("stopping manager", zap.String("name", manager.Name()))
		if err := manager.Stop(); err != nil {
			a.logger.Error("failed to stop manager",
				zap.String("name", manager.Name()),
				zap.Error(err))
		}
	}

	a.logger.Info("application stopped")
	_ = a.logger.Sync()
	return nil
}

// Run runs the application until interrupted
func (a *App) Run() error {
	// Initialize components
	if err := a.Initialize(); err != nil {
		return err
	}
	defer func() { _ = a.logger.Sync() }()

	// Create root context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start application
	if err := a.Start(ctx); err != nil {
		return err
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	a.logger.Info("received shutdown signal", zap.String("signal", sig.String()))

	// Stop application
	return a.Stop()
}

// Health checks the health of all managers
func (a *App) Health(ctx context.Context) error {
	for _, manager := range a.managers {
		if err := manager.Health(ctx); err != nil {
			return fmt.Errorf("%s unhealthy: %w", manager.Name(), err)
		}
	}
	return nil
}

// initLogger initializes the zap logger
func initLogger(cfg config.LoggingConfig, transportMode string) (*zap.Logger, error) {
	var level zapcore.Level
	switch cfg.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	default:
		level = zapcore.InfoLevel
	}

	var zapConfig zap.Config
	if cfg.Format == "console" {
		zapConfig = zap.NewDevelopmentConfig()
	} else {
		zapConfig = zap.NewProductionConfig()
	}

	zapConfig.Level = zap.NewAtomicLevelAt(level)

	// stdio MCP uses stdout for the protocol — never write logs there.
	defaultOutput := "stdout"
	if transportMode == "stdio" || transportMode == "both" {
		defaultOutput = "stderr"
	}

	outputPaths := []string{defaultOutput}
	if cfg.OutputPath != "" && cfg.OutputPath != "stdout" && cfg.OutputPath != "stderr" {
		outputPaths = []string{cfg.OutputPath}
	} else if cfg.OutputPath == "stderr" {
		outputPaths = []string{"stderr"}
	} else if cfg.OutputPath == "stdout" && (transportMode == "stdio" || transportMode == "both") {
		outputPaths = []string{"stderr"}
	}

	// In debug, also copy logs to a file under ./logs (or /tmp/logs as fallback
	// for read-only filesystems such as Docker containers with --read-only).
	// Append across restarts so crash investigation is not wiped on boot.
	if level == zapcore.DebugLevel {
		logsDir := "logs"
		if err := os.MkdirAll(logsDir, 0o755); err != nil {
			// Fallback to /tmp/logs when the working directory is read-only
			logsDir = filepath.Join(os.TempDir(), "logs")
			if err2 := os.MkdirAll(logsDir, 0o755); err2 != nil {
				return nil, fmt.Errorf("failed to create logs directory: %w (fallback: %w)", err, err2)
			}
		}
		debugLogPath := filepath.Join(logsDir, "browser-mcp.debug.log")
		outputPaths = append(outputPaths, debugLogPath)
	}

	zapConfig.OutputPaths = outputPaths
	zapConfig.ErrorOutputPaths = []string{"stderr"}

	logger, err := zapConfig.Build()
	if err != nil {
		return nil, err
	}

	return logger.Named("browser-mcp"), nil
}

func normalizeTransportMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "sse", "stdio", "both":
		return normalized
	default:
		return "sse"
	}
}
