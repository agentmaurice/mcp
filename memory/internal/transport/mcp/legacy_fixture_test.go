//go:build duckdb

package mcp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	stypes "github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage/types"
)

// legacyFixture is a tenant file written by a DuckDB 1.4 build, gzipped: twelve
// customers, four decision facts and three documents. DuckDB 2.x must open it as it is.
const legacyFixture = "testdata/legacy-duckdb14/memory.duckdb.gz"

const legacyFixtureTenant = "tenant-legacy"

// unpackLegacyFixture writes the fixture as tenants/<tenant>/memory.duckdb under a
// new base directory and returns that directory.
func unpackLegacyFixture(t testing.TB) string {
	t.Helper()
	in, err := os.Open(legacyFixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = in.Close() }()
	zr, err := gzip.NewReader(in)
	if err != nil {
		t.Fatalf("gunzip fixture: %v", err)
	}
	base := t.TempDir()
	dir := filepath.Join(base, "tenants", legacyFixtureTenant)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "memory.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, zr); err != nil {
		t.Fatalf("unpack fixture: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return base
}

// TestWriteLegacyFixture regenerates the fixture. It only runs on a DuckDB 1.4
// build (the v1.1.x line of this module), which is the point of the fixture, and
// only when MEMORY_WRITE_LEGACY_FIXTURE names the output file:
//
//	MEMORY_WRITE_LEGACY_FIXTURE=$PWD/internal/transport/mcp/testdata/legacy-duckdb14/memory.duckdb.gz \
//	  go test -tags duckdb -run TestWriteLegacyFixture ./internal/transport/mcp
func TestWriteLegacyFixture(t *testing.T) {
	target := os.Getenv("MEMORY_WRITE_LEGACY_FIXTURE")
	if target == "" {
		t.Skip("set MEMORY_WRITE_LEGACY_FIXTURE on a DuckDB 1.4 build to regenerate the fixture")
	}
	base := t.TempDir()
	cfg := testConfig(base)
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	day := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	err = manager.WithWriter(context.Background(), legacyFixtureTenant, func(ctx context.Context, q stypes.Querier) error {
		names := []string{"Alma", "Bastide", "Cormier", "Delmas", "Estève", "Fabre", "Gaillard", "Hamon", "Imbert", "Jouve", "Klein", "Lemaire"}
		for i, name := range names {
			id := "e" + string(rune('a'+i))
			if _, err := q.ExecContext(ctx,
				"INSERT INTO entities (entity_id, entity_type, external_id, name, attributes, created_at, updated_at) VALUES (?, 'customer', ?, ?, ?, ?, ?)",
				id, "ext-"+id, name, `{"ville":"Rennes","rang":`+string(rune('1'+i%9))+`}`, day, day); err != nil {
				return err
			}
		}
		decisions := []string{"plafond six chantiers", "tarif annuel un mois offert", "pas de rotation de clés", "swarm et canaux disponibles"}
		for i, d := range decisions {
			if _, err := q.ExecContext(ctx,
				"INSERT INTO facts (fact_id, fact_type, subject_entity_id, payload, effective_at, ingested_at, source_id) VALUES (?, 'decision', ?, ?, ?, ?, 's1')",
				"f"+string(rune('a'+i)), "e"+string(rune('a'+i)), `{"decision":"`+d+`","poids":`+string(rune('1'+i))+`}`, day, day); err != nil {
				return err
			}
		}
		for i, title := range []string{"Plan qualité", "Grille tarifaire", "Journal de décisions"} {
			if _, err := q.ExecContext(ctx,
				"INSERT INTO documents (document_id, uri, mime_type, title, created_at) VALUES (?, ?, 'text/markdown', ?, ?)",
				"d"+string(rune('a'+i)), "file:///"+string(rune('a'+i))+".md", title, day); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}

	in, err := os.Open(filepath.Join(base, "tenants", legacyFixtureTenant, "memory.duckdb"))
	if err != nil {
		t.Fatalf("open written file: %v", err)
	}
	defer func() { _ = in.Close() }()
	// The point of the fixture is a file written by DuckDB 1.4: refuse any other build.
	header := make([]byte, 52+32)
	if _, err := io.ReadFull(in, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	if version := string(bytes.TrimRight(header[52:], "\x00")); !strings.HasPrefix(version, "v1.4") {
		t.Fatalf("the file was written by DuckDB %q, not 1.4: run this on a v1.x build of the module", version)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// legacyQueries are memory.query statements and the rows DuckDB 1.4.1 returned
// from the fixture; DuckDB 2.x must return the same.
var legacyQueries = []struct{ sql, want string }{
	{"SELECT count(*) AS n FROM entities", `[[12]]`},
	{"SELECT name FROM v_customers ORDER BY name LIMIT 3", `[["Alma"],["Bastide"],["Cormier"]]`},
	{"SELECT json_extract_string(payload, '$.decision') AS d FROM facts ORDER BY fact_id", `[["plafond six chantiers"],["tarif annuel un mois offert"],["pas de rotation de clés"],["swarm et canaux disponibles"]]`},
	{"SELECT title FROM documents WHERE title LIKE 'Gr%'", `[["Grille tarifaire"]]`},
	{"SELECT entity_type, count(*) AS n FROM entities GROUP BY entity_type", `[["customer",12]]`},
}

func TestLegacyDuckDB14TenantAnswersMemoryQuery(t *testing.T) {
	base := unpackLegacyFixture(t)
	dbPath := filepath.Join(base, "tenants", legacyFixtureTenant, "memory.duckdb")
	original, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(base)
	cfg.Security.RequireViewsOnly = false
	manager, err := storage.NewManager(cfg.Storage, zap.NewNop())
	if err != nil {
		t.Fatalf("init storage: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	for _, tc := range legacyQueries {
		result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), tc.sql, nil, 500, 2000, legacyFixtureTenant)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		got, _ := json.Marshal(result.Rows)
		if string(got) != tc.want {
			t.Errorf("%s\n got  %s\n want %s", tc.sql, got, tc.want)
		}
		if result.TenantEmpty == nil || *result.TenantEmpty {
			t.Errorf("%s: the legacy tenant must not read as empty", tc.sql)
		}
	}

	// A write after the upgrade works on the migrated file.
	err = manager.WithWriter(context.Background(), legacyFixtureTenant, func(ctx context.Context, q stypes.Querier) error {
		_, err := q.ExecContext(ctx, "INSERT INTO entities (entity_id, entity_type, external_id, name, created_at, updated_at) VALUES ('ez', 'customer', 'ext-ez', 'Zola', now(), now())")
		return err
	})
	if err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	result, err := executeQuery(context.Background(), manager, cfg, zap.NewNop(), "SELECT count(*) AS n FROM entities", nil, 500, 2000, legacyFixtureTenant)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(result.Rows); string(got) != `[[13]]` {
		t.Fatalf("count after write = %s, want [[13]]", got)
	}

	backup, err := os.ReadFile(dbPath + ".pre-duckdb2")
	if err != nil {
		t.Fatalf("no backup taken before opening the 1.4 file: %v", err)
	}
	if string(backup) != string(original) {
		t.Fatal("the backup differs from the file written by DuckDB 1.4")
	}
}
