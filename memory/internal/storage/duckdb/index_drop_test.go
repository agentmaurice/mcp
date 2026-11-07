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

const indexWriterEnv = "BRAIN_TEST_INDEX_WRITE"
const indexReaderEnv = "BRAIN_TEST_INDEX_READ"

// TestExpressionCount covers the parsing of duckdb_indexes().expressions.
func TestExpressionCount(t *testing.T) {
	cases := map[string]int{
		"[]":                        0,
		"[name]":                    1,
		"['\"name\"']":              1,
		"[entity_type, '\"name\"']": 2,
		"[a, 'x, y']":               2,
	}
	for in, want := range cases {
		if got := expressionCount(in); got != want {
			t.Errorf("expressionCount(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestIndexWriterChild runs only as a child process. It creates a single-column index
// by hand, with rows and checkpoints, and exits without closing.
func TestIndexWriterChild(t *testing.T) {
	path := os.Getenv(indexWriterEnv)
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
	if _, err := db.ExecContext(ctx, "CREATE INDEX idx_manual_name ON entities(name)"); err != nil {
		os.Exit(7)
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX idx_manual_pair ON entities(entity_type, name)"); err != nil {
		os.Exit(8)
	}
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES (?, 'customer', ?, ?, ?, ?)",
			fmt.Sprint("e", i), fmt.Sprint("ext", i), fmt.Sprint("nom-", i), now, now); err != nil {
			os.Exit(5)
		}
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

// TestIndexReaderChild runs only as a child process: it reopens like the server, with
// the schema and the checkpoint on open, and exits without closing.
func TestIndexReaderChild(t *testing.T) {
	path := os.Getenv(indexReaderEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		os.Exit(3)
	}
	if err := EnsureSchemaLogged(ctx, db, nil); err != nil {
		os.Exit(4)
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

// TestReopenDropsManualSingleColumnIndex: a single-column index created in SQL is
// dropped on open, the composite index is kept, and an equality lookup by name still
// finds the entity after a reopen in another process.
func TestReopenDropsManualSingleColumnIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.duckdb")
	run := func(name, env string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
		cmd.Env = append(os.Environ(), env)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %s: %v\n%s", name, err, out)
		}
	}
	run("TestIndexWriterChild", indexWriterEnv+"="+path)
	run("TestIndexReaderChild", indexReaderEnv+"="+path)

	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_indexes() WHERE index_name = 'idx_manual_name'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the manual single-column index must be dropped on open, still present: %d", n)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_indexes() WHERE index_name = 'idx_manual_pair'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the composite index must be kept, count = %d", n)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM entities WHERE name = 'nom-7'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("equality lookup by name after reopen = %d rows, want 1", n)
	}
}
