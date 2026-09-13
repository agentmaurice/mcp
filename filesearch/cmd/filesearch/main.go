package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/app"
	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/filesearch/internal/logging"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	logger, err := logging.New(cfg.Logging.Level)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	defer func() {
		_ = logger.Sync()
	}()

	application, err := app.New(*cfg, logger)
	if err != nil {
		logger.Fatal("failed to initialize application", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
		logger.Fatal("application stopped with error", zap.Error(err))
	}
}
