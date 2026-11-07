//go:build duckdb

package search

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// legacyFixture is a tenant file written by a DuckDB 1.4 build, gzipped: the French
// corpus, indexed, with its full-text index. DuckDB 2.x must open it as it is.
const legacyFixture = "testdata/legacy-duckdb14/brain.duckdb.gz"

const legacyFixtureTenant = "tenant-fr"

// unpackLegacyFixture writes the fixture as tenants/<tenant>/brain.duckdb under a
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
	out, err := os.Create(filepath.Join(dir, "brain.duckdb"))
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
// build (the v1.2.x line of this module), which is the point of the fixture, and
// only when BRAIN_WRITE_LEGACY_FIXTURE names the output file:
//
//	BRAIN_WRITE_LEGACY_FIXTURE=$PWD/internal/search/testdata/legacy-duckdb14/brain.duckdb.gz \
//	  go test -tags duckdb -run TestWriteLegacyFixture ./internal/search
func TestWriteLegacyFixture(t *testing.T) {
	target := os.Getenv("BRAIN_WRITE_LEGACY_FIXTURE")
	if target == "" {
		t.Skip("set BRAIN_WRITE_LEGACY_FIXTURE on a DuckDB 1.4 build to regenerate the fixture")
	}
	base := t.TempDir()
	env := newSearchEnvAt(t, base)
	env.ingestFrenchCorpus(t, legacyFixtureTenant)
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	env.keyword(t, o, legacyFixtureTenant, "AGENT-530", 5) // builds the full-text index
	if err := env.manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}

	in, err := os.Open(filepath.Join(base, "tenants", legacyFixtureTenant, "brain.duckdb"))
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
