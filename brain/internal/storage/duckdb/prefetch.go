//go:build duckdb

package duckdb

import (
	"context"
	"fmt"
	"os"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
)

// PrefetchFullTextExtension installs the DuckDB fts extension and returns the
// installed path. With extensionDir set, it installs under that extension_directories
// root (DuckDB adds <duckdb version>/<platform>/); otherwise under the default
// $HOME/.duckdb/extensions. It runs at image build time, with the same DuckDB
// library as the server, so the runtime can LOAD fts without network access. The
// check runs on an in-memory database and does not touch any tenant.
func PrefetchFullTextExtension(ctx context.Context, extensionDir string) (string, error) {
	var settings []string
	if extensionDir != "" {
		if err := os.MkdirAll(extensionDir, 0o755); err != nil {
			return "", fmt.Errorf("create extension directory: %w", err)
		}
		settings = connectionSettings(config.StorageConfig{DuckDBExtensionDir: extensionDir})
	}
	db, err := openDuckDB("", settings)
	if err != nil {
		return "", fmt.Errorf("open in-memory duckdb: %w", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, "INSTALL fts"); err != nil {
		return "", fmt.Errorf("install fts: %w", err)
	}
	if _, err := db.ExecContext(ctx, "LOAD fts"); err != nil {
		return "", fmt.Errorf("load fts: %w", err)
	}

	var path string
	if err := db.QueryRowContext(ctx,
		"SELECT install_path FROM duckdb_extensions() WHERE extension_name = 'fts' AND loaded").Scan(&path); err != nil {
		return "", fmt.Errorf("locate fts extension: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("fts extension is loaded without an install path")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("fts extension file missing: %w", err)
	}
	return path, nil
}
