//go:build duckdb && !windows

package duckdb

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

const lockHolderEnv = "TEST_LOCK_HOLDER_PATH"

// TestLockHolderChild runs only as a child process: it holds a write lock on the
// file, as a 1.x sidecar that still has the tenant open does, until stdin closes.
func TestLockHolderChild(t *testing.T) {
	path := os.Getenv(lockHolderEnv)
	if path == "" {
		t.Skip("child process only")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		os.Exit(3)
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		os.Exit(4)
	}
	println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// No copy is taken while another process holds the file: the copy would not be the
// state that opens later.
func TestBackupBeforeUpgradeRefusesALockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant.duckdb")
	writeHeader(t, path, "v1.4.1", []byte("tenant data"))

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHolderChild$")
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	if line, _ := bufio.NewReader(stderr).ReadString('\n'); line != "locked\n" {
		t.Fatalf("the child did not take the lock: %q", line)
	}

	if taken, err := backupBeforeUpgrade(path); err == nil || taken {
		t.Fatalf("locked file: %v, %v; want an error", taken, err)
	}
	if _, err := os.Stat(path + PreUpgradeBackupSuffix); !os.IsNotExist(err) {
		t.Fatal("a backup was taken of a file another process holds")
	}

	_ = stdin.Close()
	_ = cmd.Wait()
	if taken, err := backupBeforeUpgrade(path); err != nil || !taken {
		t.Fatalf("after the other process exits: %v, %v", taken, err)
	}
}
