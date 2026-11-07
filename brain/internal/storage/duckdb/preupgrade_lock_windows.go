//go:build duckdb && windows

package duckdb

import (
	"fmt"
	"os"
)

// lockForBackup opens the database file. There is no portable byte-range lock on
// Windows here: the 2.x library fails to open a file that another process holds.
func lockForBackup(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open tenant database for the backup: %w", err)
	}
	return f, nil
}
