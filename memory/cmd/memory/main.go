package main

import (
	"context"
	"flag"
	"log"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/app"
	"github.com/agentmaurice/mcpchatui/mcp/shared/sidecar"
	"github.com/agentmaurice/mcpchatui/mcp/shared/version"
)

func main() {
	if version.HandleVersionFlag() {
		return
	}

	// Sidecar self-registration (no-op in standalone mode).
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		log.Printf("[sidecar] registration failed: %v", err)
	}

	transport := flag.String("transport", "sse", "transport mode: sse|stdio|both")
	flag.Parse()

	ctx := context.Background()
	application, err := app.New()
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}

	if err := application.Run(ctx, *transport); err != nil {
		log.Fatalf("memory server exited: %v", err)
	}
}
