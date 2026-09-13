package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func writeCredsFile(t *testing.T, dir string, apiKey string) {
	t.Helper()
	contents := `{"mcp_id":"mcp-1","api_key":"` + apiKey + `","saved_at":"2026-01-01T00:00:00Z"}`
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(contents), 0600))
}

func TestInitLogger(t *testing.T) {
	l, err := initLogger("debug", "console")
	require.NoError(t, err)
	require.NotNil(t, l)

	l, err = initLogger("invalid", "json")
	require.NoError(t, err)
	require.NotNil(t, l)
}

func TestRunRegisterValidationAndExistingCreds(t *testing.T) {
	logger = zap.NewNop()
	cfg = config.NewDefaultConfig()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().StringToString("metadata", nil, "")

	cfg.BootstrapToken = "bootstrap"
	cfg.MauriceURL = ""
	err := runRegister(cmd, nil)
	require.ErrorContains(t, err, "--maurice-url is required")

	cfg.MauriceURL = "http://127.0.0.1:1"
	cfg.BootstrapToken = ""
	err = runRegister(cmd, nil)
	require.ErrorContains(t, err, "--bootstrap-token is required")

	credDir := t.TempDir()
	writeCredsFile(t, credDir, "api-key")
	cfg.CredentialsPath = credDir
	cfg.BootstrapToken = "bootstrap"

	err = runRegister(cmd, nil)
	require.NoError(t, err)
}

func TestRunStatusAndRevokeNoCredentialsPaths(t *testing.T) {
	logger = zap.NewNop()
	cfg = config.NewDefaultConfig()
	cfg.CredentialsPath = t.TempDir()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("confirm", false, "")

	// No credentials path.
	require.NoError(t, runStatus(cmd, nil))
	require.NoError(t, runRevoke(cmd, nil))

	// Credentials present but no maurice URL: still no error.
	writeCredsFile(t, cfg.CredentialsPath, "api-key")
	cfg.MauriceURL = ""
	require.NoError(t, runStatus(cmd, nil))
}

func TestRunStartEarlyErrors(t *testing.T) {
	logger = zap.NewNop()
	cfg = config.NewDefaultConfig()

	cmd := &cobra.Command{}
	cmd.Flags().StringToString("metadata", nil, "")

	// Missing maurice_url -> config validation error.
	cfg.MauriceURL = ""
	err := runStart(cmd, nil)
	require.ErrorContains(t, err, "maurice_url is required")

	// No credentials + no bootstrap token.
	cfg.MauriceURL = "https://example.com"
	cfg.BootstrapToken = ""
	cfg.CredentialsPath = t.TempDir()
	err = runStart(cmd, nil)
	require.ErrorContains(t, err, "no credentials found and no bootstrap token provided")
}
