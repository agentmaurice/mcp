//go:build duckdb

package duckdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"testing"

	godb "github.com/duckdb/duckdb-go/v2"

	stypes "github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/types"
)

// openFTSConnection opens an in-memory DuckDB. When extensionDir is set, the
// extension directory points there. When offline is set, automatic install and
// automatic load of extensions are disabled, as in a container without egress.
func openFTSConnection(t *testing.T, extensionDir string, offline bool) *sql.DB {
	t.Helper()
	connector, err := godb.NewConnector("", func(execer driver.ExecerContext) error {
		var statements []string
		if extensionDir != "" {
			statements = append(statements, "SET extension_directories = ['"+strings.ReplaceAll(extensionDir, "'", "''")+"']")
		}
		if offline {
			statements = append(statements,
				"SET autoinstall_known_extensions = false",
				"SET autoload_known_extensions = false")
		}
		for _, stmt := range statements {
			if _, err := execer.ExecContext(context.Background(), stmt, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create duckdb connector: %v", err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestBM25WorksOfflineWithPreinstalledFTSExtension is the runtime contract of
// the sidecar image: the fts extension is already present in the extension
// directory, and the server runs with automatic install and load disabled.
func TestBM25WorksOfflineWithPreinstalledFTSExtension(t *testing.T) {
	ctx := context.Background()
	extensionDir := filepath.Join(t.TempDir(), "extensions")

	// Fixture step: copy the extension into a dedicated directory. This needs
	// network or a cached copy; without either, the offline check cannot run.
	fixture := openFTSConnection(t, extensionDir, false)
	if _, err := fixture.ExecContext(ctx, "INSTALL fts"); err != nil {
		t.Skipf("cannot install fts into a fixture directory (network or cached copy needed): %v", err)
	}

	db := openFTSConnection(t, extensionDir, true)
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	q := newSQLQuerier(db)
	if _, err := q.ExecContext(ctx,
		`INSERT INTO chunks (id, tenant_id, document_id, chunk_index, content, chunk_type, bm25_content, created_at)
		 VALUES ('c1', 't', 'd', 0, 'Le ticket AGENT-530 décrit la tarification.', 'paragraph', 'Le ticket AGENT-530 décrit la tarification.', now())`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The preinstalled file must be loadable on its own, with no install fallback.
	if _, err := q.ExecContext(ctx, "LOAD fts"); err != nil {
		t.Fatalf("offline LOAD fts from the preinstalled directory: %v", err)
	}
	if err := loadFTSExtension(ctx, q); err != nil {
		t.Fatalf("loadFTSExtension offline: %v", err)
	}
	if err := rebuildFullTextIndex(ctx, q, stypes.FullTextOptions{Stemmer: "porter", Stopwords: "none"}); err != nil {
		t.Fatalf("rebuild offline: %v", err)
	}

	qr, err := q.QueryContext(ctx, `
		SELECT c.id FROM chunks c
		JOIN (SELECT id, fts_main_chunks.match_bm25(id, ?) AS score FROM chunks) fts ON c.id = fts.id
		WHERE fts.score IS NOT NULL ORDER BY fts.score DESC`, "AGENT-530")
	if err != nil {
		t.Fatalf("match_bm25 offline: %v", err)
	}
	if len(qr.Rows) != 1 || toStringForTest(qr.Rows[0]["id"]) != "c1" {
		t.Fatalf("offline BM25 returned %v, want [c1]", qr.Rows)
	}
}

// TestFTSExtensionIsNotAvailableOfflineWithoutPreinstall is the control for the
// test above: with an empty extension directory and automatic load disabled,
// LOAD fts must fail. This shows the preinstall step is what makes offline work.
func TestFTSExtensionIsNotAvailableOfflineWithoutPreinstall(t *testing.T) {
	ctx := context.Background()
	db := openFTSConnection(t, filepath.Join(t.TempDir(), "empty-extensions"), true)
	if _, err := db.ExecContext(ctx, "LOAD fts"); err == nil {
		t.Fatal("LOAD fts must fail without a preinstalled extension when automatic install is disabled")
	}
}

// TestPrefetchInstallsIntoHomeExtensionDirectory checks the path that the image
// build relies on: the extension lands under $HOME/.duckdb/extensions.
func TestPrefetchInstallsIntoHomeExtensionDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := PrefetchFullTextExtension(context.Background(), "")
	if err != nil {
		t.Skipf("prefetch needs network to install fts: %v", err)
	}
	want := filepath.Join(home, ".duckdb", "extensions")
	if !strings.HasPrefix(path, want+string(filepath.Separator)) || !strings.HasSuffix(path, "fts.duckdb_extension") {
		t.Fatalf("prefetch path = %q, want it under %q", path, want)
	}
}
