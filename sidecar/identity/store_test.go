package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreSaveLoadExistsDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	require.False(t, s.Exists())
	require.Equal(t, dir, s.Path())

	in := &Credentials{
		MCPID:    "mcp-1",
		APIKey:   "api-1",
		TenantID: "tenant-1",
	}
	require.NoError(t, s.Save(in))
	require.True(t, s.Exists())
	require.NotEmpty(t, in.SavedAt)

	out, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "mcp-1", out.MCPID)
	require.Equal(t, "api-1", out.APIKey)
	require.Equal(t, "tenant-1", out.TenantID)
	require.NotEmpty(t, out.SavedAt)

	require.NoError(t, s.Delete())
	require.False(t, s.Exists())
	// Idempotent delete
	require.NoError(t, s.Delete())
}

func TestStoreLoadErrors(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Load()
	require.ErrorContains(t, err, "credentials not found")

	badDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "credentials.json"), []byte("{"), 0600))
	_, err = NewStore(badDir).Load()
	require.ErrorContains(t, err, "parse credentials")
}
