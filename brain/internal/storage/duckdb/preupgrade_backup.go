//go:build duckdb

package duckdb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// PreUpgradeBackupSuffix is appended to a tenant file (and its WAL) to name the copy
// taken before DuckDB 2.x opens a file written by 1.x. A file written by 2.x cannot
// be read by a 1.x image, so the copy is the way back (see README, "Rolling back").
const PreUpgradeBackupSuffix = ".pre-duckdb2"

// Offsets of the DuckDB main header: 8-byte checksum, "DUCK", storage version, four
// flags, then the 32-byte version string of the library that created the file.
const (
	headerMagicOffset   = 8
	headerLibVerOffset  = 52
	headerLibVerLength  = 32
	headerProbeLength   = headerLibVerOffset + headerLibVerLength
	legacyVersionPrefix = "v1."
)

// backupBeforeUpgrade copies the database file and its WAL next to them, once, before
// the first open by DuckDB 2.x. It does nothing when the file does not exist yet, when
// a complete backup already exists (it is never overwritten: the first copy is the 1.x
// one), or when the file was created by DuckDB 2.x. A copy failure is an error: the
// caller must not open the file without its backup. It reports whether a backup was
// taken.
//
// A backup is complete when its manifest (<backup>.sum: size and SHA-256 of the
// database copy, size of the WAL copy or -1) exists and both sizes match; the manifest
// is written last. The SHA-256 is for the operator to check before a restore. A copy without manifest is the
// leftover of an attempt that stopped before the open, so the source is unchanged and
// the copy is redone.
func backupBeforeUpgrade(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat tenant database: %w", err)
	}
	backup := path + PreUpgradeBackupSuffix
	complete, err := backupIsComplete(backup)
	if err != nil {
		return false, err
	}
	if complete {
		return false, nil
	}
	if createdByDuckDB2(path) {
		return false, nil
	}
	// A 1.x process that still holds the file would write during or after the copy:
	// refuse, and keep the lock until the copy is done. The database is read through
	// the locked descriptor, because closing any descriptor of a file drops the
	// process's POSIX locks on it.
	locked, err := lockForBackup(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = locked.Close() }()
	// An earlier attempt may have stopped after the WAL copy. A 1.x image can have
	// replayed and removed that WAL since: only a copy made now matches the database.
	walBackup := path + ".wal" + PreUpgradeBackupSuffix
	for _, stale := range []string{walBackup, backup + manifestSuffix} {
		if err := os.RemoveAll(stale); err != nil {
			return false, fmt.Errorf("remove stale backup file: %w", err)
		}
	}
	// The WAL first, the manifest of the database copy last: it marks the backup complete.
	walSize := int64(-1) // -1: there was no WAL
	switch _, err := os.Stat(path + ".wal"); {
	case err == nil:
		in, err := os.Open(path + ".wal")
		if err != nil {
			return false, fmt.Errorf("back up WAL before upgrade: %w", err)
		}
		walSize, _, err = copyReaderAtomic(in, walBackup)
		_ = in.Close()
		if err != nil {
			return false, fmt.Errorf("back up WAL before upgrade: %w", err)
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("stat WAL before upgrade: %w", err)
	}
	size, sum, err := copyReaderAtomic(locked, backup)
	if err != nil {
		return false, fmt.Errorf("back up database before upgrade: %w", err)
	}
	if err := writeFileAtomic(backup+manifestSuffix, []byte(fmt.Sprintf("%d %s\nwal %d\n", size, sum, walSize))); err != nil {
		return false, fmt.Errorf("write backup manifest: %w", err)
	}
	return true, nil
}

// manifestSuffix names the file that records the size and SHA-256 of a backup.
const manifestSuffix = ".sum"

// backupIsComplete reads the state of the database backup: absent or unfinished (false,
// nil), complete (true, nil), or damaged (an error: the operator decides, because the
// source may be the only intact copy).
func backupIsComplete(backup string) (bool, error) {
	info, err := os.Stat(backup)
	switch {
	case os.IsNotExist(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("stat pre-upgrade backup: %w", err)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("pre-upgrade backup %s is not a regular file; move it away to take a new one", backup)
	}
	manifest, err := os.ReadFile(backup + manifestSuffix)
	if os.IsNotExist(err) {
		return false, nil // the attempt stopped before the manifest: redo the copy
	}
	if err != nil {
		return false, fmt.Errorf("read backup manifest: %w", err)
	}
	var size, walSize int64
	var sum string
	damaged := fmt.Errorf("pre-upgrade backup %s does not match its manifest (%s); move both away to take a new one", backup, backup+manifestSuffix)
	if _, err := fmt.Sscanf(string(manifest), "%d %s\nwal %d", &size, &sum, &walSize); err != nil || size != info.Size() || !looksLikeDuckDBFile(backup) {
		return false, damaged
	}
	walInfo, err := os.Stat(strings.TrimSuffix(backup, PreUpgradeBackupSuffix) + ".wal" + PreUpgradeBackupSuffix)
	switch {
	case walSize < 0 && err == nil, walSize >= 0 && (err != nil || walInfo.Size() != walSize):
		return false, damaged
	}
	return true, nil
}

// createdByDuckDB2 reports whether the main header of the file names a library that
// is not 1.x. An unreadable or unrecognized header counts as 1.x: the copy is cheap,
// a file lost in an upgrade is not.
func createdByDuckDB2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, headerProbeLength)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	if !bytes.Equal(header[headerMagicOffset:headerMagicOffset+4], []byte("DUCK")) {
		return false
	}
	version := string(bytes.TrimRight(header[headerLibVerOffset:headerLibVerOffset+headerLibVerLength], "\x00"))
	return version != "" && !bytes.HasPrefix([]byte(version), []byte(legacyVersionPrefix))
}

// duckDBBlockSize is the block granularity of a DuckDB file: the main header and the
// two database headers take one block each, and every other block has the same size.
const duckDBBlockSize = 4096

// looksLikeDuckDBFile reports whether the file has the shape of a DuckDB database: the
// header magic, at least the three header blocks, and a whole number of blocks. An
// empty, foreign or truncated file is not a backup.
func looksLikeDuckDBFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() < 3*duckDBBlockSize || info.Size()%duckDBBlockSize != 0 {
		return false
	}
	header := make([]byte, headerMagicOffset+4)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return bytes.Equal(header[headerMagicOffset:], []byte("DUCK"))
}

// copyFileAtomic copies src to dst through a temporary file, flushed to disk, then
// renamed: dst never exists half written.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, _, err = copyReaderAtomic(in, dst)
	return err
}

// copyReaderAtomic is copyFileAtomic from an open file, read from its start. It
// returns the number of bytes copied and their SHA-256.
func copyReaderAtomic(in *os.File, dst string) (size int64, sum string, err error) {
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
		}
	}()
	hash := sha256.New()
	if size, err = io.Copy(io.MultiWriter(out, hash), in); err != nil {
		return 0, "", err
	}
	if err = out.Sync(); err != nil {
		return 0, "", err
	}
	if err = out.Close(); err != nil {
		return 0, "", err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

// writeFileAtomic writes data to path through a temporary file, flushed, then renamed.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// discardBackupIfUnchanged removes the backup taken by this start when the open then
// failed and the files still equal their copies: nothing was written, so a later
// attempt must copy the state it will actually open, not this one. Files that differ
// (2.x started a migration) keep their backup, which is then the only way back.
func discardBackupIfUnchanged(path string) {
	backup := path + PreUpgradeBackupSuffix
	if !sameContent(path, backup) {
		return
	}
	walBackup := path + ".wal" + PreUpgradeBackupSuffix
	if _, err := os.Stat(path + ".wal"); err == nil {
		if !sameContent(path+".wal", walBackup) {
			return
		}
	} else if _, statErr := os.Stat(walBackup); statErr == nil {
		return
	}
	_ = os.Remove(backup + manifestSuffix)
	_ = os.Remove(backup)
	_ = os.Remove(walBackup)
}

func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer func() { _ = fa.Close() }()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer func() { _ = fb.Close() }()
	ia, errA := fa.Stat()
	ib, errB := fb.Stat()
	if errA != nil || errB != nil || ia.Size() != ib.Size() {
		return false
	}
	bufA, bufB := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false
		}
		if errA != nil || errB != nil {
			// Equal sizes: both reached the end together.
			return (errA == io.EOF || errA == io.ErrUnexpectedEOF) && (errB == io.EOF || errB == io.ErrUnexpectedEOF)
		}
	}
}
