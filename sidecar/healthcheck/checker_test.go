package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCheckerCheckUnhealthyWithoutCredentials(t *testing.T) {
	mgr := identity.NewManager(&config.SidecarConfig{
		MauriceURL:      "https://example.com",
		CredentialsPath: t.TempDir(),
	}, zap.NewNop())

	checker := NewChecker(mgr, time.Hour, zap.NewNop())

	var unhealthyCalls int32
	checker.SetOnUnhealthy(func() {
		atomic.AddInt32(&unhealthyCalls, 1)
	})

	checker.check(context.Background())
	require.Equal(t, StateUnhealthy, checker.State())
	require.False(t, checker.LastCheck().IsZero())
	require.False(t, checker.IsHealthy())
	require.Equal(t, int32(1), atomic.LoadInt32(&unhealthyCalls))
}

func TestCheckerCheckHandlesValidateError(t *testing.T) {
	cfg := &config.SidecarConfig{
		MauriceURL:      "http://127.0.0.1:1",
		CredentialsPath: t.TempDir(),
	}
	mgr := identity.NewManager(cfg, zap.NewNop())
	require.NoError(t, identity.NewStore(cfg.CredentialsPath).Save(&identity.Credentials{
		MCPID:  "mcp-1",
		APIKey: "api-1",
	}))
	require.NoError(t, mgr.LoadCredentials())

	checker := NewChecker(mgr, time.Hour, zap.NewNop())
	checker.check(context.Background())

	require.Equal(t, StateUnhealthy, checker.State())
	// LastCheck is set only on successful Validate call.
	require.True(t, checker.LastCheck().IsZero())
}

func TestCheckerCheckRenewFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/mcp/auth/validate":
			_, _ = w.Write([]byte(`{"valid":true,"renew_required":true}`))
		case "/api/v1/mcp/auth/renew":
			_, _ = w.Write([]byte(`{"api_key":"new-key"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := &config.SidecarConfig{
		MauriceURL:      srv.URL,
		CredentialsPath: t.TempDir(),
	}
	mgr := identity.NewManager(cfg, zap.NewNop())
	require.NoError(t, identity.NewStore(cfg.CredentialsPath).Save(&identity.Credentials{
		MCPID:  "mcp-1",
		APIKey: "old-key",
	}))
	require.NoError(t, mgr.LoadCredentials())

	checker := NewChecker(mgr, time.Hour, zap.NewNop())
	checker.check(context.Background())

	require.Equal(t, StateHealthy, checker.State())
	require.True(t, checker.IsHealthy())
	require.Equal(t, "new-key", mgr.GetAPIKey())
}

func TestCheckerStartStopsOnContextCancel(t *testing.T) {
	mgr := identity.NewManager(&config.SidecarConfig{
		MauriceURL:      "https://example.com",
		CredentialsPath: t.TempDir(),
	}, zap.NewNop())
	checker := NewChecker(mgr, 10*time.Millisecond, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		checker.Start(ctx)
		close(done)
	}()

	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("checker did not stop after context cancellation")
	}
}
