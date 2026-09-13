package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentmaurice/mcpchatui/mcp/search/internal/mcpserver"
	"github.com/agentmaurice/mcpchatui/mcp/search/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/shared/modernmcp"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}
	if t := os.Getenv("MCP_TRANSPORT"); t != "" && t != "stdio" {
		fmt.Fprintln(os.Stderr, "Search requires dedicated STDIO transport")
		os.Exit(1)
	}
	service, err := search.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, search.Code(err))
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.New(service).Serve(ctx, modernmcp.Config{Transport: "stdio"}); err != nil {
		fmt.Fprintln(os.Stderr, "Search transport failed")
		os.Exit(1)
	}
}
