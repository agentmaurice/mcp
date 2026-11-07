//go:build duckdb

package duckdb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The writer, reopener and reader run as child processes: the server restarts
// between them, and the writer exits without closing its database, as a killed
// server does. DuckDB 1.4.1 returned no row for equality filters on single-column
// indexes in this sequence, so the tenant filter of every query came back empty.

const reopenWriterEnv = "BRAIN_TEST_REOPEN_WRITE"
const reopenCheckEnv = "BRAIN_TEST_REOPEN_CHECK"

// TestReopenWriterChild is run only as a child process.
func TestReopenWriterChild(t *testing.T) {
	path := os.Getenv(reopenWriterEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := openDuckDB(path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(3)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(4)
	}
	for i := 0; i < 100; i++ {
		id := fmt.Sprint("x", i)
		stmts := []struct {
			q    string
			args []any
		}{
			{`INSERT INTO sources (id, tenant_id, source_type, source_uri, status, created_at, updated_at) VALUES (?, 'bench', 'filesystem', ?, 'ready', now(), now())`, []any{"s" + id, "u" + id}},
			{`INSERT INTO documents (id, tenant_id, source_id, file_path, doc_type, content_hash, created_at, updated_at) VALUES (?, 'bench', ?, ?, 'markdown', 'h', now(), now())`, []any{"d" + id, "s" + id, "f" + id}},
			{`INSERT INTO chunks (id, tenant_id, document_id, chunk_index, content, chunk_type, symbol_name, created_at) VALUES (?, 'bench', ?, ?, 'c', 'section', ?, now())`, []any{"c" + id, "d" + id, i, "sym" + id}},
			{`INSERT INTO index_jobs (id, tenant_id, source_id, status, created_at) VALUES (?, 'bench', ?, 'completed', now())`, []any{"j" + id, "s" + id}},
		}
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s.q, s.args...); err != nil {
				fmt.Fprintln(os.Stderr, "insert:", err)
				os.Exit(5)
			}
		}
	}
	os.Exit(0)
}

// TestReopenCheckChild is run only as a child process: it reopens like the server
// does (schema, checkpoint on open) and exits without closing.
func TestReopenCheckChild(t *testing.T) {
	path := os.Getenv(reopenCheckEnv)
	if path == "" {
		t.Skip("child process only")
	}
	ctx := context.Background()
	db, err := openDuckDB(path, nil)
	if err != nil {
		os.Exit(3)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		os.Exit(4)
	}
	_, _ = db.ExecContext(ctx, "CHECKPOINT")
	os.Exit(0)
}

// TestReopenKeepsTenantFilteredRows writes through one process, reopens through a
// second one, and checks the tenant-filtered counts that the server relies on.
func TestReopenKeepsTenantFilteredRows(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "tenants", "bench", "brain.duckdb")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runChild := func(name string, env ...string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %s: %v\n%s", name, err, out)
		}
	}
	runChild("TestReopenWriterChild", reopenWriterEnv+"="+path)
	runChild("TestReopenCheckChild", reopenCheckEnv+"="+path)

	db, err := openDuckDB(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	checks := []struct {
		name string
		q    string
		want int
	}{
		{"sources by tenant", "SELECT count(*) FROM sources WHERE tenant_id = 'bench'", 100},
		{"documents by tenant", "SELECT count(*) FROM documents WHERE tenant_id = 'bench'", 100},
		{"documents by source_id", "SELECT count(*) FROM documents WHERE source_id = 'sx7'", 1},
		{"chunks by tenant", "SELECT count(*) FROM chunks WHERE tenant_id = 'bench'", 100},
		{"chunks by document_id", "SELECT count(*) FROM chunks WHERE document_id = 'dx7'", 1},
		{"chunks by tenant and type", "SELECT count(*) FROM chunks WHERE tenant_id = 'bench' AND chunk_type = 'section'", 100},
		{"chunks by tenant and symbol", "SELECT count(*) FROM chunks WHERE tenant_id = 'bench' AND symbol_name = 'symx7'", 1},
		{"index_jobs by tenant", "SELECT count(*) FROM index_jobs WHERE tenant_id = 'bench'", 100},
		{"index_jobs by source_id", "SELECT count(*) FROM index_jobs WHERE source_id = 'sx7'", 1},
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
