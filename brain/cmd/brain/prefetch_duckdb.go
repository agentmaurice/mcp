//go:build duckdb

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/duckdb"
)

// runPrefetchExtensions installs the fts extension for this binary and reports
// where it landed. It uses the same configuration source as the server, so
// BRAIN_DUCKDB_EXTENSION_DIR selects the target directory. Failure must make the
// image build fail.
func runPrefetchExtensions() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	path, err := duckdb.PrefetchFullTextExtension(ctx, cfg.Storage.DuckDBExtensionDir)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}
