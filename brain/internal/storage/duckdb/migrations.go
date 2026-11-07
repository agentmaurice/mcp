//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureSchema creates the Brain tables and indexes.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	migrations := []string{
		// Sources table
		`CREATE TABLE IF NOT EXISTS sources (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_type     TEXT NOT NULL,
			source_uri      TEXT NOT NULL,
			display_name    TEXT,
			config          JSON,
			last_indexed_at TIMESTAMP,
			status          TEXT DEFAULT 'pending',
			error_message   TEXT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// Documents table
		`CREATE TABLE IF NOT EXISTS documents (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_id       TEXT NOT NULL,
			file_path       TEXT NOT NULL,
			title           TEXT,
			doc_type        TEXT NOT NULL,
			language        TEXT,
			content_hash    TEXT NOT NULL,
			metadata        JSON,
			size_bytes      BIGINT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// Chunks table
		`CREATE TABLE IF NOT EXISTS chunks (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			document_id     TEXT NOT NULL,
			chunk_index     INTEGER NOT NULL,
			content         TEXT NOT NULL,
			summary         TEXT,
			context         TEXT,
			chunk_type      TEXT NOT NULL,
			symbol_name     TEXT,
			start_line      INTEGER,
			end_line        INTEGER,
			metadata        JSON,
			embedding       FLOAT[],
			embedding_model TEXT,
			bm25_content    TEXT,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// Index jobs table
		`CREATE TABLE IF NOT EXISTS index_jobs (
			id              TEXT PRIMARY KEY,
			tenant_id       TEXT NOT NULL,
			source_id       TEXT NOT NULL,
			status          TEXT DEFAULT 'pending',
			progress        DOUBLE DEFAULT 0.0,
			total_files     INTEGER DEFAULT 0,
			processed_files INTEGER DEFAULT 0,
			total_chunks    INTEGER DEFAULT 0,
			error_message   TEXT,
			started_at      TIMESTAMP,
			completed_at    TIMESTAMP,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,

		// Indexes. Single-column indexes are not created: with DuckDB 1.4.1, an equality
		// filter on them returns no row after a checkpoint and a reopen (AGENT-1049).
		// Each tenant has its own file, so full scans stay correct. The composite
		// indexes below survive the same cycle (verified). Single-column indexes left by
		// earlier versions are dropped on open.
		`CREATE INDEX IF NOT EXISTS idx_documents_hash ON documents(tenant_id, source_id, content_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks(tenant_id, chunk_type)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_symbol ON chunks(tenant_id, symbol_name)`,
		`DROP INDEX IF EXISTS idx_sources_tenant`,
		`DROP INDEX IF EXISTS idx_documents_tenant`,
		`DROP INDEX IF EXISTS idx_documents_source`,
		`DROP INDEX IF EXISTS idx_chunks_tenant`,
		`DROP INDEX IF EXISTS idx_chunks_document`,
		`DROP INDEX IF EXISTS idx_index_jobs_tenant`,
		`DROP INDEX IF EXISTS idx_index_jobs_source`,
	}

	for _, migration := range migrations {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, migration)
		}
	}

	return nil
}
