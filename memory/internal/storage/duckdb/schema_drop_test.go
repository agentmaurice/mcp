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

const schemaWriterEnv = "BRAIN_TEST_SCHEMA_WRITE"

// TestSchemaWriterChild runs only as a child process. It creates a second schema with a
// table and a single-column index, a single-column index in main, rows, and checkpoints,
// then exits without closing.
func TestSchemaWriterChild(t *testing.T) {
	path := os.Getenv(schemaWriterEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		os.Exit(3)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		os.Exit(4)
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		os.Exit(6)
	}
	for _, stmt := range []string{
		"CREATE SCHEMA archive",
		"CREATE TABLE archive.notes (titre VARCHAR, body VARCHAR)",
		"CREATE INDEX idx_notes_titre ON archive.notes(titre)",
		"CREATE INDEX idx_main_name ON entities(name)",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			fmt.Fprintln(os.Stderr, stmt, err)
			os.Exit(7)
		}
	}
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, 'customer', ?, ?, ?, ?)",
			fmt.Sprint("e", i), fmt.Sprint("ext", i), fmt.Sprint("nom-", i), now, now); err != nil {
			os.Exit(5)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO archive.notes VALUES (?, 'corps')", fmt.Sprint("titre-", i)); err != nil {
			os.Exit(5)
		}
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

// TestOpenWithForeignSchemaIndex: the database has a single-column index in another
// schema. Opening it must succeed. The index of main is dropped, the archive index is
// left alone.
func TestOpenWithForeignSchemaIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.duckdb")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSchemaWriterChild$")
	cmd.Env = append(os.Environ(), schemaWriterEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer child: %v\n%s", err, out)
	}

	ctx := context.Background()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := EnsureSchemaLogged(ctx, db, nil); err != nil {
		t.Fatalf("opening must succeed with an index in another schema: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_indexes() WHERE index_name = 'idx_main_name'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the single-column index of main must be dropped, still present: %d", n)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_indexes() WHERE index_name = 'idx_notes_titre'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the archive index is not ours and must be left alone, count = %d", n)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM archive.notes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Errorf("archive rows = %d, want 50", n)
	}
}
