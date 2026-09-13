package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps a SQL database connection.
type DB struct {
	*sql.DB
}

// NewDB creates a new database connection and ensures the directory exists.
func NewDB(path string) (*DB, error) {
	// Ensure the directory exists
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, fmt.Errorf("failed to enable WAL: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	return &DB{DB: db}, nil
}

// Migrate runs the SQL schema migration.
func (db *DB) Migrate(schemaSQL string) error {
	_, err := db.Exec(schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to run migration: %w", err)
	}
	return nil
}
