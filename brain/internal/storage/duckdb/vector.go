//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureVSSExtension installs and loads the vss extension.
func EnsureVSSExtension(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "INSTALL vss"); err != nil {
		// May already be installed, try to continue
		_ = err
	}
	if _, err := db.ExecContext(ctx, "LOAD vss"); err != nil {
		return fmt.Errorf("failed to load vss extension: %w", err)
	}
	return nil
}

// CreateHNSWIndex creates an HNSW index on chunks.embedding for vector search.
func CreateHNSWIndex(ctx context.Context, db *sql.DB, dimensions int) error {
	// Drop existing index if any
	_, _ = db.ExecContext(ctx, "DROP INDEX IF EXISTS idx_chunks_embedding")

	query := fmt.Sprintf(`
		CREATE INDEX idx_chunks_embedding ON chunks
		USING HNSW (embedding)
		WITH (metric = 'cosine')
	`)
	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to create HNSW index: %w", err)
	}
	return nil
}
