package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/artifact/internal/mcpserver"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/shared/sidecar"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar registration failed: %v\n", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.New().Serve(ctx, modernmcp.ConfigFromEnv("/mcp/artifact")); err != nil {
		fmt.Fprintf(os.Stderr, "Artifact MCP server exited: %v\n", err)
		os.Exit(1)
	}
}
