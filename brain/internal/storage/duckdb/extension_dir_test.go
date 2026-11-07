//go:build duckdb

package duckdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
)

// offlineSettings disable automatic install and load, as in a container without egress.
var offlineSettings = []string{
	"SET autoinstall_known_extensions = false",
	"SET autoload_known_extensions = false",
}

func TestConnectionSettingsFollowExtensionDirConfig(t *testing.T) {
	if got := connectionSettings(config.StorageConfig{}); len(got) != 1 || got[0] != lambdaSyntaxSetting {
		t.Fatalf("without BRAIN_DUCKDB_EXTENSION_DIR only the lambda syntax may be applied, got %v", got)
	}
	got := connectionSettings(config.StorageConfig{DuckDBExtensionDir: "/opt/brain/it's here"})
	want := []string{lambdaSyntaxSetting, "SET extension_directories = ['/opt/brain/it''s here']"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("connectionSettings = %q, want %q", got, want)
	}
}

// TestManagerBM25FromConfiguredExtensionDirOffline is the sidecar contract: the
// extension lives in BRAIN_DUCKDB_EXTENSION_DIR and the server runs offline. HOME
// points to an empty directory, so the only place that can provide fts is the
// configured one. If the setting were ignored, LOAD would fail.
func TestManagerBM25FromConfiguredExtensionDirOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	extensionDir := filepath.Join(t.TempDir(), "duckdb-extensions")

	if _, err := PrefetchFullTextExtension(ctx, extensionDir); err != nil {
		t.Skipf("cannot prefetch fts into the fixture directory (network needed): %v", err)
	}

	m := NewManager(config.StorageConfig{
		BaseDir:            t.TempDir(),
		TenantsDirName:     "tenants",
		DBFilename:         "brain.duckdb",
		WriteQueueSize:     16,
		DuckDBExtensionDir: extensionDir,
	}, zap.NewNop())
	t.Cleanup(func() { _ = m.Close() })
	// Test-only switch: production settings come from configuration alone.
	m.connSettings = append(m.connSettings, offlineSettings...)

	const tenant = "tenant-ext"
	writeChunk(t, m, tenant, "c1", "Le ticket AGENT-530 décrit la tarification.", true)
	if err := m.EnsureFullTextIndex(ctx, tenant, testFTSOptions); err != nil {
		t.Fatalf("offline index build from configured directory: %v", err)
	}
	if ids := matchIDs(t, m, tenant, "AGENT-530"); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("offline BM25 returned %v, want [c1]", ids)
	}
}

// TestConfiguredEmptyExtensionDirProvidesNoFTS is the control: a configured
// directory without fts, offline, makes LOAD fail, so the setting alone is not
// enough and the preinstall is what enables BM25.
func TestConfiguredEmptyExtensionDirProvidesNoFTS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	empty := filepath.Join(t.TempDir(), "empty")
	settings := append(connectionSettings(config.StorageConfig{DuckDBExtensionDir: empty}), offlineSettings...)

	db, err := openDuckDB("", settings)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "LOAD fts"); err == nil {
		t.Fatal("LOAD fts must fail when the configured directory holds no preinstalled extension")
	}
	var current string
	if err := db.QueryRowContext(ctx, "SELECT CAST(current_setting('extension_directories') AS VARCHAR)").Scan(&current); err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if !strings.Contains(current, empty) {
		t.Fatalf("extension_directories = %q, want %q", current, empty)
	}
}
