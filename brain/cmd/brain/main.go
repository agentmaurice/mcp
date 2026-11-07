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

	transport := flag.String("transport", "sse", "Transport mode: sse, stdio, both")
	prefetch := flag.Bool("prefetch-extensions", false, "install the DuckDB fts extension into $HOME/.duckdb and exit (image build step)")
	flag.Parse()

	if *prefetch {
		if err := runPrefetchExtensions(); err != nil {
			fmt.Fprintf(os.Stderr, "prefetch-extensions: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Sidecar self-registration (no-op in standalone mode).
	if _, err := sidecar.RegisterIfConfigured(context.Background()); err != nil {
		log.Printf("[sidecar] registration failed: %v", err)
	}

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
