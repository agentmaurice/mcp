package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/shared/sidecar"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/mcpserver"
	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar registration failed: %v\n", err)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "SSH configuration is invalid: %v\n", err)
		os.Exit(1)
	}
	service, err := sshservice.New(
		cfg.Service,
		sshservice.NewFileTargetStore(cfg.TargetsFile),
		sshservice.NewFileCredentialProvider(cfg.CredentialsFile),
		sshservice.NewSSHConnector(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SSH service initialization failed: %v\n", err)
		os.Exit(1)
	}
	defer service.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.New(service).Serve(ctx, modernmcp.ConfigFromEnv("/mcp/ssh")); err != nil {
		fmt.Fprintf(os.Stderr, "SSH MCP server exited: %v\n", err)
		os.Exit(1)
	}
}
