//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureFTSIndex creates or recreates the DuckDB FTS index on chunks.bm25_content.
func EnsureFTSIndex(ctx context.Context, db *sql.DB) error {
	// Drop existing index if it exists (DuckDB FTS doesn't support IF NOT EXISTS well)
	_, _ = db.ExecContext(ctx, "PRAGMA drop_fts_index('chunks')")

	// Create FTS index on bm25_content column
	_, err := db.ExecContext(ctx,
		"PRAGMA create_fts_index('chunks', 'id', 'bm25_content', overwrite=1)")
	if err != nil {
		return fmt.Errorf("failed to create FTS index: %w", err)
	}
	return nil
}
