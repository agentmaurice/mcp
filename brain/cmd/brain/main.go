package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/app"
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

	transport := flag.String("transport", "sse", "Transport mode: sse, stdio, both")
	flag.Parse()

	application, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create application: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := application.Run(ctx, *transport); err != nil {
		fmt.Fprintf(os.Stderr, "application error: %v\n", err)
		os.Exit(1)
	}
}
