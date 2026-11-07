//go:build duckdb

package duckdb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
)

// writeHeader writes a file that starts like a DuckDB main header created by the
// given library version, followed by payload.
func writeHeader(t *testing.T, path, version string, payload []byte) {
	t.Helper()
	// Whole blocks, as a real file: the header block, two database headers, then payload.
	file := make([]byte, 3*duckDBBlockSize, 4*duckDBBlockSize)
	copy(file[headerMagicOffset:], "DUCK")
	copy(file[headerLibVerOffset:], version)
	block := make([]byte, duckDBBlockSize)
	copy(block, payload)
	if err := os.WriteFile(path, append(file, block...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBackupBeforeUpgradeCopiesFileAndWALOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if err := os.WriteFile(path+".wal", []byte("pending writes"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)

	taken, err := backupBeforeUpgrade(path)
	if err != nil || !taken {
		t.Fatalf("backup = %v, %v; want taken", taken, err)
	}
	if got, _ := os.ReadFile(path + PreUpgradeBackupSuffix); !bytes.Equal(got, original) {
		t.Fatal("database backup differs from the original")
	}
	if got, _ := os.ReadFile(path + ".wal" + PreUpgradeBackupSuffix); string(got) != "pending writes" {
		t.Fatalf("WAL backup = %q", got)
	}

	// 2.x rewrites the file; the next start must keep the 1.x copy.
	writeHeader(t, path, "v1.4.1", []byte("changed by 2.x"))
	if taken, err := backupBeforeUpgrade(path); err != nil || taken {
		t.Fatalf("second backup = %v, %v; want none", taken, err)
	}
	if got, _ := os.ReadFile(path + PreUpgradeBackupSuffix); !bytes.Equal(got, original) {
		t.Fatal("the existing backup was overwritten")
	}
	if _, err := os.Stat(path + PreUpgradeBackupSuffix + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary backup file left behind")
	}
}

func TestBackupBeforeUpgradeSkipsMissingAndDuckDB2Files(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.duckdb")
	if taken, err := backupBeforeUpgrade(missing); err != nil || taken {
		t.Fatalf("missing file: %v, %v", taken, err)
	}

	created := filepath.Join(dir, "created-by-2x.duckdb")
	db, err := sql.Open("duckdb", created)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t(i INT); INSERT INTO t VALUES (1); CHECKPOINT"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if !createdByDuckDB2(created) {
		t.Fatalf("a file created by the embedded library must read as 2.x")
	}
	if taken, err := backupBeforeUpgrade(created); err != nil || taken {
		t.Fatalf("2.x file: %v, %v", taken, err)
	}
	if _, err := os.Stat(created + PreUpgradeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("a backup was taken of a file created by 2.x")
	}
}

func TestBackupBeforeUpgradeTreatsUnknownHeaderAsLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "odd.duckdb")
	if err := os.WriteFile(path, []byte("not a duckdb file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("unknown header: %v, %v; want a backup", taken, err)
	}
}

// The tenant file is not opened when its backup cannot be written.
func TestEnsureTenantRefusesToOpenWithoutBackup(t *testing.T) {
	m := newBackupTestManager(t)
	const tenant = "tenant-nobackup"
	path := m.TenantPath(tenant)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	// A directory where the backup file must go makes the copy fail.
	if err := os.MkdirAll(path+PreUpgradeBackupSuffix+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := m.OpenTenant(context.Background(), tenant); err == nil {
		t.Fatal("OpenTenant must fail when the pre-upgrade backup cannot be written")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("the tenant file was modified although its backup failed")
	}
}

func newBackupTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(config.StorageConfig{
		BaseDir:        t.TempDir(),
		TenantsDirName: "tenants",
		DBFilename:     "memory.duckdb",
		WriteQueueSize: 8,
	}, zap.NewNop())
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// A path that is not a regular file is not a backup, and the file is not opened.
func TestBackupBeforeUpgradeRejectsNonRegularBackupPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if err := os.Mkdir(path+PreUpgradeBackupSuffix, 0o755); err != nil {
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err == nil || taken {
		t.Fatalf("a directory at the backup path: %v, %v; want an error", taken, err)
	}
}

// An attempt that stopped after the WAL copy leaves a WAL that a 1.x image may have
// replayed and removed since; the next attempt must not keep it.
func TestBackupBeforeUpgradeDropsStaleWALBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if err := os.WriteFile(path+".wal"+PreUpgradeBackupSuffix, []byte("stale wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No WAL now: the stale copy must disappear instead of pairing with the new backup.
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("backup = %v, %v", taken, err)
	}
	if _, err := os.Stat(path + ".wal" + PreUpgradeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("a stale WAL backup was kept next to the new database backup")
	}
}

// A failed open leaves no backup when the files still equal it: a later attempt
// copies the state it will really open.
func TestFailedOpenDiscardsAnUnchangedBackup(t *testing.T) {
	m := newBackupTestManager(t)
	const tenant = "tenant-garbage"
	path := m.TenantPath(tenant)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A 1.x header followed by junk: DuckDB cannot open it.
	writeHeader(t, path, "v1.4.1", bytes.Repeat([]byte{0xAB}, 4096))
	if _, err := m.OpenTenant(context.Background(), tenant); err == nil {
		t.Fatal("a corrupt file must not open")
	}
	if _, err := os.Stat(path + PreUpgradeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("the backup of an unchanged file was kept after the failed open")
	}
}

func TestDiscardBackupKeepsACopyOfAChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("state A"))
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("backup = %v, %v", taken, err)
	}
	writeHeader(t, path, "v1.4.1", []byte("state B written after the copy"))
	discardBackupIfUnchanged(path)
	if _, err := os.Stat(path + PreUpgradeBackupSuffix); err != nil {
		t.Fatalf("the backup of a file that changed must stay: %v", err)
	}
}

func TestBackupBeforeUpgradeFailsOnWALStatError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if err := os.Symlink(filepath.Base(path)+".wal", path+".wal"); err != nil { // loops on itself
		t.Skipf("symlinks unavailable: %v", err)
	}
	if taken, err := backupBeforeUpgrade(path); err == nil || taken {
		t.Fatalf("an unreadable WAL path: %v, %v; want an error", taken, err)
	}
	if _, err := os.Stat(path + PreUpgradeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("a database backup without its WAL backup was left behind")
	}
}

// A copy without manifest is the leftover of an attempt that stopped before the open:
// it is redone, not trusted.
func TestBackupBeforeUpgradeRedoesACopyWithoutManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(path+PreUpgradeBackupSuffix, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("copy without manifest: %v, %v; want a new backup", taken, err)
	}
	if got, _ := os.ReadFile(path + PreUpgradeBackupSuffix); !bytes.Equal(got, original) {
		t.Fatal("the unfinished copy was not replaced")
	}
	manifest, err := os.ReadFile(path + PreUpgradeBackupSuffix + manifestSuffix)
	if err != nil || !bytes.HasPrefix(manifest, []byte(fmt.Sprintf("%d ", len(original)))) {
		t.Fatalf("manifest = %q, %v", manifest, err)
	}
}

// A backup that no longer matches its manifest (cut short, even on a block boundary,
// or altered) refuses the open: the source may be the only intact copy.
func TestBackupBeforeUpgradeRejectsABackupThatDoesNotMatchItsManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("backup = %v, %v", taken, err)
	}
	backup := path + PreUpgradeBackupSuffix
	data, _ := os.ReadFile(backup)
	if err := os.WriteFile(backup, data[:3*duckDBBlockSize], 0o600); err != nil { // cut on a block boundary
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err == nil || taken {
		t.Fatalf("truncated backup: %v, %v; want an error", taken, err)
	}
}

// The manifest also records the WAL copy: a backup whose WAL copy was lost is not complete.
func TestBackupBeforeUpgradeRejectsABackupThatLostItsWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))
	if err := os.WriteFile(path+".wal", []byte("writes not yet checkpointed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("backup = %v, %v", taken, err)
	}
	if err := os.Remove(path + ".wal" + PreUpgradeBackupSuffix); err != nil {
		t.Fatal(err)
	}
	if taken, err := backupBeforeUpgrade(path); err == nil || taken {
		t.Fatalf("backup without its WAL copy: %v, %v; want an error", taken, err)
	}
}
