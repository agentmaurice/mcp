package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConnectorKeepsRootsIsolatedAcrossConcurrentJobs(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "same.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "same.txt"), []byte("bravo"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgA, _ := json.Marshal(Config{Path: rootA})
	cfgB, _ := json.Marshal(Config{Path: rootB})
	connector := New()

	var gotA, gotB []byte
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = connector.ListFiles(context.Background(), cfgA)
		if errA == nil {
			gotA, errA = connector.ReadFile(context.Background(), cfgA, "same.txt")
		}
	}()
	go func() {
		defer wg.Done()
		_, errB = connector.ListFiles(context.Background(), cfgB)
		if errB == nil {
			gotB, errB = connector.ReadFile(context.Background(), cfgB, "same.txt")
		}
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("concurrent reads failed: %v / %v", errA, errB)
	}
	if string(gotA) != "alpha" || string(gotB) != "bravo" {
		t.Fatalf("roots were mixed: %q / %q", gotA, gotB)
	}
}
