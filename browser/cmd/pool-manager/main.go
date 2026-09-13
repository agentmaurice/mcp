package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/poolmanager"
)

func main() {
	cfg, err := poolmanager.LoadServiceConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load pool-manager config: %v\n", err)
		os.Exit(1)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	server, err := poolmanager.NewServer(cfg, logger)
	if err != nil {
		logger.Fatal("failed to create pool-manager", zap.Error(err))
	}

	go func() {
		if err := server.ListenAndServe(); err != nil {
			logger.Fatal("pool-manager stopped unexpectedly", zap.Error(err))
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown pool-manager", zap.Error(err))
		os.Exit(1)
	}
}
