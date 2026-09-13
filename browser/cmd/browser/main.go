package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/app"
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

	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: ./configs/config.json)")
	transport := flag.String("transport", "sse", "transport mode: sse|stdio|both")
	flag.Parse()

	// Create and run application
	application, err := app.New(*configPath, *transport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create application: %v\n", err)
		os.Exit(1)
	}

	if err := application.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Application error: %v\n", err)
		os.Exit(1)
	}
}
