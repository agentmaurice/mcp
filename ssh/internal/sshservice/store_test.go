package sshservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTargetStoreEmptyAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	store := NewFileTargetStore(path)
	empty, err := store.Snapshot(context.Background())
	if err != nil || empty.Revision != "empty" || len(empty.Targets) != 0 {
		t.Fatalf("unexpected empty projection: %#v %v", empty, err)
	}
	writeTestFile(t, path, `{"revision":"one","targets":[{"target_id":"prod","label":"Production","host":"server.internal","port":22,"username":"agent","host_key_fingerprint":"SHA256:test","credential_ref":"prod-key","enabled":true,"published":true}]}`, 0o644)
	first, err := store.Snapshot(context.Background())
	if err != nil || first.Revision != "one" || len(first.Targets) != 1 {
		t.Fatalf("unexpected first projection: %#v %v", first, err)
	}
	writeTestFile(t, path, `{"revision":"two","targets":[]}`, 0o644)
	second, err := store.Snapshot(context.Background())
	if err != nil || second.Revision != "two" || len(second.Targets) != 0 {
		t.Fatalf("projection was not reloaded: %#v %v", second, err)
	}
}

func TestFileTargetStoreRejectsUnsafeProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	writeTestFile(t, path, `{"targets":[{"target_id":"prod","label":"Production","host":"ssh://server","port":22,"username":"agent","credential_ref":"key","enabled":true,"published":true}]}`, 0o644)
	_, err := NewFileTargetStore(path).Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected invalid host to be rejected")
	}
}

func TestFileCredentialProviderPermissionsAndResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	writeTestFile(t, path, `{"credentials":{"prod":{"method":"password","password":"fixture"}}}`, 0o600)
	provider := NewFileCredentialProvider(path)
	credential, err := provider.Resolve(context.Background(), "prod", Purpose{})
	if err != nil || credential.Method != "password" {
		t.Fatalf("unexpected credential resolution: %#v %v", credential, err)
	}
	_, err = provider.Resolve(context.Background(), "missing", Purpose{})
	assertCode(t, err, "ssh_credential_unavailable")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Resolve(context.Background(), "prod", Purpose{})
	assertCode(t, err, "ssh_credential_unavailable")
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
