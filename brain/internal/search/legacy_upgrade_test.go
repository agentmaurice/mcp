//go:build duckdb

package search

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage/duckdb"
)

// A tenant file written by DuckDB 1.4 (fixture) is opened as it is by the current
// library: same results on the reference questions, the 1.4 full-text index is
// served, a new document is indexed and found, and the file is backed up first.
func TestLegacyDuckDB14TenantOpensAndKeepsItsBackup(t *testing.T) {
	base := unpackLegacyFixture(t)
	dbPath := filepath.Join(base, "tenants", legacyFixtureTenant, "brain.duckdb")
	original, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read fixture copy: %v", err)
	}

	env := newSearchEnvAt(t, base)
	o := NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	checkFrenchCorpusCases(t, env, o, legacyFixtureTenant)

	backup, err := os.ReadFile(dbPath + duckdb.PreUpgradeBackupSuffix)
	if err != nil {
		t.Fatalf("no backup taken before opening the 1.4 file: %v", err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatal("the backup differs from the file written by DuckDB 1.4")
	}

	env.upsert(t, legacyFixtureTenant, "apres-montee.md", "# Après la montée\n\nLa sentinelleunique prouve que l'index 1.4 se reconstruit.")
	if resp := env.keyword(t, o, legacyFixtureTenant, "sentinelleunique", 5); topPath(resp) != "apres-montee.md" {
		t.Fatalf("document added after the upgrade not found: top = %q", topPath(resp))
	}
	checkFrenchCorpusCases(t, env, o, legacyFixtureTenant)

	if err := env.manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// A second start must not replace the 1.4 copy with the file 2.x has written.
	env = newSearchEnvAt(t, base)
	o = NewOrchestrator(env.manager, nil, env.cfg, zap.NewNop())
	checkFrenchCorpusCases(t, env, o, legacyFixtureTenant)
	if again, err := os.ReadFile(dbPath + duckdb.PreUpgradeBackupSuffix); err != nil || !bytes.Equal(again, original) {
		t.Fatalf("the backup changed on the second start (err=%v)", err)
	}
}
