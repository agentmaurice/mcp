package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestManagerRegisterAndLifecycle(t *testing.T) {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/mcp/self-register":
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"mcp_id":"mcp-1","api_key":"api-1","tenant_id":"tenant-1","deployment_id":"tenant-1","mqtt_broker":"tcp://host.docker.internal:1883"}`))
		case "/api/v1/mcp/auth/validate":
			require.Equal(t, "Bearer api-1", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"valid":true,"renew_required":false}`))
		case "/api/v1/mcp/auth/renew":
			require.Equal(t, "Bearer api-1", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"api_key":"api-2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := &config.SidecarConfig{
		MauriceURL:      srv.URL,
		BootstrapToken:  "bootstrap-token",
		CredentialsPath: t.TempDir(),
		LocalMCPCommand: "mcp-server",
	}
	m := NewManager(cfg, zap.NewNop())

	require.ErrorContains(t, (&Manager{
		config: &config.SidecarConfig{},
		store:  NewStore(t.TempDir()),
		logger: zap.NewNop(),
	}).Register(context.Background()), "no bootstrap token configured")

	require.NoError(t, m.Register(context.Background()))
	require.Equal(t, "Bearer bootstrap-token", gotAuth)
	require.Equal(t, "api-1", m.GetAPIKey())
	require.Equal(t, "mcp-1", m.GetMCPID())
	require.Equal(t, "tenant-1", m.GetTenantID())
	require.Equal(t, "tenant-1", cfg.DeploymentID)
	require.Equal(t, "tcp://host.docker.internal:1883", cfg.MQTTBroker)

	validateResp, err := m.Validate(context.Background())
	require.NoError(t, err)
	require.True(t, validateResp.Valid)

	require.NoError(t, m.Renew(context.Background()))
	require.Equal(t, "api-2", m.GetAPIKey())

	loaded, err := m.store.Load()
	require.NoError(t, err)
	require.Equal(t, "api-2", loaded.APIKey)

	require.NoError(t, m.ClearCredentials())
	require.Empty(t, m.GetAPIKey())
}

func TestManagerCredentialLoadingHelpers(t *testing.T) {
	cfg := &config.SidecarConfig{
		MauriceURL:      "http://127.0.0.1:1",
		CredentialsPath: t.TempDir(),
	}
	m := NewManager(cfg, zap.NewNop())

	require.False(t, m.HasCredentials())
	require.Error(t, m.LoadCredentials())

	require.ErrorContains(t, m.Renew(context.Background()), "no credentials to renew")

	resp, err := m.Validate(context.Background())
	require.NoError(t, err)
	require.False(t, resp.Valid)

	require.NoError(t, NewStore(cfg.CredentialsPath).Save(&Credentials{
		MCPID:    "mcp-1",
		APIKey:   "api-1",
		TenantID: "tenant-1",
	}))
	require.True(t, m.HasCredentials())
	require.Equal(t, "api-1", m.GetAPIKey())
	require.Equal(t, "mcp-1", m.GetMCPID())
	require.Equal(t, "tenant-1", m.GetTenantID())

	// API key empty should not be considered usable credentials.
	badPath := t.TempDir()
	require.NoError(t, NewStore(badPath).Save(&Credentials{MCPID: "mcp-2", APIKey: ""}))
	m2 := NewManager(&config.SidecarConfig{
		MauriceURL:      "http://127.0.0.1:1",
		CredentialsPath: filepath.Clean(badPath),
	}, zap.NewNop())
	require.False(t, m2.HasCredentials())
}
