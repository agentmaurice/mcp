package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/shared/sidecar"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/agentclient"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/system/internal/mcpserver"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar registration failed: %v\n", err)
	}
	cfg := config.SidecarFromEnv()
	client := agentclient.New(cfg.SocketPath, 10*time.Second, agentclient.Identity{
		Actor: cfg.Actor, OrganizationID: cfg.OrganizationID, DeploymentID: cfg.DeploymentID,
	})
	healthCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := client.Health(healthCtx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "System host agent is unavailable: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.New(client).Serve(ctx, modernmcp.ConfigFromEnv("/mcp/system")); err != nil {
		fmt.Fprintf(os.Stderr, "System MCP server exited: %v\n", err)
		os.Exit(1)
	}
}
