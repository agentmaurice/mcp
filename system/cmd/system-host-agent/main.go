package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/hostagent"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}
	cfg, err := config.HostFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	agent := hostagent.New(cfg, hostagent.NewSystemBackend(cfg), logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Serve(ctx); err != nil {
		logger.Error("system host agent stopped", "error", err)
		os.Exit(1)
	}
}
