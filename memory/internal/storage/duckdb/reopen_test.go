//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The writer, the reopener and the reader run as child processes: the server
// restarts between them, and the writer exits without closing its database, as a
// killed server does. With DuckDB 1.4.1, an equality filter on a single-column
// secondary index returned no row in this sequence, so queries by entity type,
// document type, link kind, and the other indexed columns came back empty.
// Primary keys and the composite unique constraint were not affected.

const memoryWriterEnv = "BRAIN_TEST_REOPEN_WRITE"
const memoryReaderEnv = "BRAIN_TEST_REOPEN_CHECK"

// TestReopenWriterChild runs only as a child process.
func TestReopenWriterChild(t *testing.T) {
	path := os.Getenv(memoryWriterEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		os.Exit(3)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(4)
	}
	// As ensureTenant does right after opening the database.
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		fmt.Fprintln(os.Stderr, "checkpoint:", err)
		os.Exit(6)
	}
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		id := fmt.Sprint("x", i)
		stmts := []struct {
			q string
			a []any
		}{
			{`INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, 'customer', ?, ?, ?, ?)`, []any{"e" + id, "ext" + id, "nom" + id, now, now}},
			{`INSERT INTO facts (fact_id, fact_type, subject_entity_id, payload, effective_at, ingested_at, source_id) VALUES (?, 'decision', ?, '{}', ?, ?, 's')`, []any{"f" + id, "e" + id, now, now}},
			{`INSERT INTO documents (document_id, uri, mime_type, title, created_at) VALUES (?, ?, 'text/markdown', ?, ?)`, []any{"d" + id, "u" + id, "t" + id, now}},
			{`INSERT INTO document_links (link_id, document_id, entity_id, fact_id, relationship, created_at) VALUES (?, ?, ?, ?, 'r', ?)`, []any{"l" + id, "d" + id, "e" + id, "f" + id, now}},
			{`INSERT INTO links (link_id, link_type, from_kind, to_kind, created_at) VALUES (?, 'represents', 'document', 'entity', ?)`, []any{"k" + id, now}},
			{`INSERT INTO ingest_receipts (receipt_id, tenant_id, kind, privacy_mode, target_type, payload_hash, created_at) VALUES (?, 'zz', 'fact', 'none', 'entity', 'h', ?)`, []any{"r" + id, now}},
		}
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s.q, s.a...); err != nil {
				fmt.Fprintln(os.Stderr, "insert:", err)
				os.Exit(5)
			}
		}
	}
	os.Exit(0)
}

// TestReopenReaderChild runs only as a child process: it reopens like the server
// does (schema, checkpoint on open) and exits without closing.
func TestReopenReaderChild(t *testing.T) {
	path := os.Getenv(memoryReaderEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(3)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(4)
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		fmt.Fprintln(os.Stderr, "checkpoint:", err)
		os.Exit(6)
	}
	os.Exit(0)
}

// TestReopenKeepsFilteredRows writes in one process, reopens in a second one, and
// checks the filters the tools rely on: primary keys, the composite unique key, and
// every column that had a secondary index.
func TestReopenKeepsFilteredRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.duckdb")
	runChild := func(name string, env string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
		cmd.Env = append(os.Environ(), env)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %s: %v\n%s", name, err, out)
		}
	}
	runChild("TestReopenWriterChild", memoryWriterEnv+"="+path)
	runChild("TestReopenReaderChild", memoryReaderEnv+"="+path)

	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	checks := []struct {
		name string
		q    string
		want int
	}{
		{"entities by primary key", "SELECT count(*) FROM entities WHERE entity_id = 'ex7'", 1},
		{"entities by unique key", "SELECT count(*) FROM entities WHERE entity_type = 'customer' AND external_id = 'extx7'", 1},
		{"entities by type", "SELECT count(*) FROM entities WHERE entity_type = 'customer'", 100},
		{"entities by name", "SELECT count(*) FROM entities WHERE name = 'nomx7'", 1},
		{"facts by primary key", "SELECT count(*) FROM facts WHERE fact_id = 'fx7'", 1},
		{"facts by type", "SELECT count(*) FROM facts WHERE fact_type = 'decision'", 100},
		{"facts by subject", "SELECT count(*) FROM facts WHERE subject_entity_id = 'ex7'", 1},
		{"documents by mime type", "SELECT count(*) FROM documents WHERE mime_type = 'text/markdown'", 100},
		{"document_links by document", "SELECT count(*) FROM document_links WHERE document_id = 'dx7'", 1},
		{"links by primary key", "SELECT count(*) FROM links WHERE link_id = 'kx7'", 1},
		{"links by type", "SELECT count(*) FROM links WHERE link_type = 'represents'", 100},
		{"links by from_kind", "SELECT count(*) FROM links WHERE from_kind = 'document'", 100},
		{"ingest_receipts by tenant", "SELECT count(*) FROM ingest_receipts WHERE tenant_id = 'zz'", 100},
		{"ingest_receipts by kind", "SELECT count(*) FROM ingest_receipts WHERE kind = 'fact'", 100},
	}
	for _, c := range checks {
		var n int
		if err := db.QueryRow(c.q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("%s = %d, want %d", c.name, n, c.want)
		}
	}
}
