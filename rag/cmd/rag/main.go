package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/app"
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

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	transport := flag.String("transport", "sse", "transport mode: sse|stdio|both")
	flag.Parse()

	ctx := context.Background()

	// Create app
	application, err := app.New()
	if err != nil {
		return fmt.Errorf("failed to create app: %w", err)
	}

	// Run app
	if err := application.Run(ctx, *transport); err != nil {
		return fmt.Errorf("app execution failed: %w", err)
	}

	return nil
}
