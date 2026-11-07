//go:build duckdb && !windows

package duckdb

import (
	"fmt"
	"os"
	"syscall"
)

// lockForBackup opens the database file and takes a write lock on it, as DuckDB does
// while it has the file open. It fails when another process holds the file, for
// example a 1.x sidecar that is still running. The lock lasts until the returned file
// is closed.
func lockForBackup(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open tenant database for the backup: %w", err)
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("tenant database %s is in use by another process (stop the 1.x sidecar first): %w", path, err)
	}
	return f, nil
}
