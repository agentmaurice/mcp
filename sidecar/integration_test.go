package mcp_sidecar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/client"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/healthcheck"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/identity"
	"go.uber.org/zap"
)

// mockServer simulates the AgentMaurice API for sidecar integration tests.
type mockServer struct {
	mu             sync.Mutex
	registered     bool
	mcpID          string
	apiKey         string
	renewedAPIKey  string
	revoked        bool
	renewRequired  bool
	validateCount  int
	renewCount     int
	registerCount  int
	bootstrapToken string
}

func newMockServer(bootstrapToken string) *mockServer {
	return &mockServer{
		bootstrapToken: bootstrapToken,
		mcpID:          "mock-mcp-id-12345",
		apiKey:         "msk_testkey_abcdef1234567890abcdef1234567890",
		renewedAPIKey:  "msk_renewed_abcdef1234567890abcdef1234567890",
	}
}

func (m *mockServer) handler() http.Handler {
	mux := http.NewServeMux()

	// POST /api/v1/mcp/self-register
	mux.HandleFunc("/api/v1/mcp/self-register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		// Validate bootstrap token
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+m.bootstrapToken {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "invalid_token",
				"message": "invalid bootstrap token",
			})
			return
		}

		var req client.SelfRegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		_ = req

		m.registered = true
		m.registerCount++

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.SelfRegisterResponse{
			MCPID:    m.mcpID,
			APIKey:   m.apiKey,
			TenantID: "dep-1",
		})
	})

	// POST /api/v1/mcp/auth/validate
	mux.HandleFunc("/api/v1/mcp/auth/validate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		auth := r.Header.Get("Authorization")
		currentKey := m.apiKey
		if m.renewCount > 0 {
			currentKey = m.renewedAPIKey
		}

		if auth != "Bearer "+currentKey {
			// Revoked or invalid credentials
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(client.ValidateResponse{
				Valid:         false,
				RenewRequired: false,
			})
			return
		}

		if m.revoked {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(client.ValidateResponse{
				Valid:         false,
				RenewRequired: false,
			})
			return
		}

		m.validateCount++

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.ValidateResponse{
			Valid:         true,
			RenewRequired: m.renewRequired,
		})
	})

	// POST /api/v1/mcp/auth/renew
	mux.HandleFunc("/api/v1/mcp/auth/renew", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+m.apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "unauthorized",
				"message": "invalid credentials",
			})
			return
		}

		m.renewCount++
		m.renewRequired = false

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.RenewResponse{
			APIKey: m.renewedAPIKey,
		})
	})

	return mux
}

func setupTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	return logger
}

func setupTestCredentialsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("failed to create credentials dir: %v", err)
	}
	return dir
}

// TestSidecarRegistration tests that the sidecar registers with a bootstrap token
// and persists credentials locally.
func TestSidecarRegistration(t *testing.T) {
	bootstrapToken := "test-bootstrap-token-123"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
		CheckInterval:   1 * time.Second,
	}

	mgr := identity.NewManager(cfg, logger)

	// Should have no credentials initially
	if mgr.HasCredentials() {
		t.Fatal("expected no credentials initially")
	}

	// Register
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Verify credentials are persisted
	if !mgr.HasCredentials() {
		t.Fatal("expected credentials after registration")
	}

	if got := mgr.GetMCPID(); got != mock.mcpID {
		t.Errorf("MCP ID = %q, want %q", got, mock.mcpID)
	}

	if got := mgr.GetAPIKey(); got != mock.apiKey {
		t.Errorf("API key = %q, want %q", got, mock.apiKey)
	}

	// Verify the mock received the registration
	mock.mu.Lock()
	if !mock.registered {
		t.Error("mock server did not register the sidecar")
	}
	if mock.registerCount != 1 {
		t.Errorf("register count = %d, want 1", mock.registerCount)
	}
	mock.mu.Unlock()

	// Verify credentials are persisted on disk
	store := identity.NewStore(credDir)
	if !store.Exists() {
		t.Fatal("credentials file does not exist on disk")
	}
}

// TestSidecarRegistrationInvalidToken tests that registration fails with an invalid token.
func TestSidecarRegistrationInvalidToken(t *testing.T) {
	bootstrapToken := "valid-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  "wrong-token",
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := mgr.Register(ctx)
	if err == nil {
		t.Fatal("expected registration to fail with invalid token")
	}

	if mgr.HasCredentials() {
		t.Error("should not have credentials after failed registration")
	}
}

// TestSidecarValidation tests that credentials can be validated after registration.
func TestSidecarValidation(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register first
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Validate
	resp, err := mgr.Validate(ctx)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !resp.Valid {
		t.Error("expected credentials to be valid")
	}
	if resp.RenewRequired {
		t.Error("expected no renewal required")
	}

	mock.mu.Lock()
	if mock.validateCount != 1 {
		t.Errorf("validate count = %d, want 1", mock.validateCount)
	}
	mock.mu.Unlock()
}

// TestSidecarRenewal tests the credential renewal flow.
func TestSidecarRenewal(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	mock.renewRequired = true
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Validate -- should indicate renewal required
	resp, err := mgr.Validate(ctx)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !resp.RenewRequired {
		t.Error("expected renewal required")
	}

	// Renew
	if err := mgr.Renew(ctx); err != nil {
		t.Fatalf("renewal failed: %v", err)
	}

	// API key should be updated
	if got := mgr.GetAPIKey(); got != mock.renewedAPIKey {
		t.Errorf("API key after renewal = %q, want %q", got, mock.renewedAPIKey)
	}

	// Validate again with new key -- should succeed
	resp, err = mgr.Validate(ctx)
	if err != nil {
		t.Fatalf("validation after renewal failed: %v", err)
	}
	if !resp.Valid {
		t.Error("expected credentials to be valid after renewal")
	}
	if resp.RenewRequired {
		t.Error("expected no renewal required after renewing")
	}

	mock.mu.Lock()
	if mock.renewCount != 1 {
		t.Errorf("renew count = %d, want 1", mock.renewCount)
	}
	mock.mu.Unlock()
}

// TestSidecarCredentialsPersistence tests that credentials survive a manager restart.
func TestSidecarCredentialsPersistence(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	// Register with first manager
	mgr1 := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := mgr1.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Create a second manager with the same credentials path
	mgr2 := identity.NewManager(cfg, logger)

	// Should load credentials from disk
	if !mgr2.HasCredentials() {
		t.Fatal("second manager should find persisted credentials")
	}

	if got := mgr2.GetMCPID(); got != mock.mcpID {
		t.Errorf("second manager MCP ID = %q, want %q", got, mock.mcpID)
	}

	if got := mgr2.GetAPIKey(); got != mock.apiKey {
		t.Errorf("second manager API key = %q, want %q", got, mock.apiKey)
	}
}

// TestSidecarClearCredentials tests that credentials can be cleared.
func TestSidecarClearCredentials(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if !mgr.HasCredentials() {
		t.Fatal("expected credentials after registration")
	}

	// Clear
	if err := mgr.ClearCredentials(); err != nil {
		t.Fatalf("clear credentials failed: %v", err)
	}

	// Verify cleared in memory
	if mgr.GetAPIKey() != "" {
		t.Error("expected empty API key after clear")
	}
	if mgr.GetMCPID() != "" {
		t.Error("expected empty MCP ID after clear")
	}

	// Verify cleared on disk
	store := identity.NewStore(credDir)
	if store.Exists() {
		t.Error("credentials file should not exist after clear")
	}
}

// TestSidecarRevokedCredentials tests that validation detects revoked credentials.
func TestSidecarRevokedCredentials(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Revoke on server side
	mock.mu.Lock()
	mock.revoked = true
	mock.mu.Unlock()

	// Validate -- should return invalid
	resp, err := mgr.Validate(ctx)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if resp.Valid {
		t.Error("expected credentials to be invalid after revocation")
	}
}

// TestHealthCheckerLifecycle tests the health checker start/stop and state transitions.
func TestHealthCheckerLifecycle(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
		CheckInterval:   200 * time.Millisecond,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Start health checker
	checker := healthcheck.NewChecker(mgr, cfg.CheckInterval, logger)

	// Initial state should be unknown
	if got := checker.State(); got != healthcheck.StateUnknown {
		t.Errorf("initial state = %q, want %q", got, healthcheck.StateUnknown)
	}

	checkerCtx, checkerCancel := context.WithCancel(ctx)
	go checker.Start(checkerCtx)

	// Wait for at least two check cycles
	time.Sleep(500 * time.Millisecond)

	// Should be healthy
	if !checker.IsHealthy() {
		t.Errorf("expected healthy state, got %q", checker.State())
	}

	// Verify multiple validations occurred
	mock.mu.Lock()
	count := mock.validateCount
	mock.mu.Unlock()

	if count < 2 {
		t.Errorf("expected at least 2 validations, got %d", count)
	}

	// LastCheck should be recent
	if checker.LastCheck().IsZero() {
		t.Error("expected non-zero last check time")
	}

	// Stop the checker
	checkerCancel()
	time.Sleep(100 * time.Millisecond)
}

// TestHealthCheckerUnhealthyCallback tests that the unhealthy callback fires.
func TestHealthCheckerUnhealthyCallback(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
		CheckInterval:   200 * time.Millisecond,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	checker := healthcheck.NewChecker(mgr, cfg.CheckInterval, logger)

	var callbackCalled bool
	var callbackMu sync.Mutex
	checker.SetOnUnhealthy(func() {
		callbackMu.Lock()
		callbackCalled = true
		callbackMu.Unlock()
	})

	checkerCtx, checkerCancel := context.WithCancel(ctx)
	defer checkerCancel()
	go checker.Start(checkerCtx)

	// Let it validate once successfully
	time.Sleep(300 * time.Millisecond)

	// Revoke credentials on the server
	mock.mu.Lock()
	mock.revoked = true
	mock.mu.Unlock()

	// Wait for the next check cycle
	time.Sleep(400 * time.Millisecond)

	callbackMu.Lock()
	if !callbackCalled {
		t.Error("expected unhealthy callback to be called after revocation")
	}
	callbackMu.Unlock()

	if checker.IsHealthy() {
		t.Error("expected unhealthy state after revocation")
	}
}

// TestHealthCheckerRenewalCycle tests automatic renewal via the health checker.
func TestHealthCheckerRenewalCycle(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	mock.renewRequired = true
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	credDir := setupTestCredentialsDir(t)
	logger := setupTestLogger(t)

	cfg := &config.SidecarConfig{
		MauriceURL:      server.URL,
		BootstrapToken:  bootstrapToken,
		PublicURL:       "https://my-mcp.example.com",
		MCPType:         "custom",
		CredentialsPath: credDir,
		CheckInterval:   200 * time.Millisecond,
	}

	mgr := identity.NewManager(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register
	if err := mgr.Register(ctx); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	checker := healthcheck.NewChecker(mgr, cfg.CheckInterval, logger)

	checkerCtx, checkerCancel := context.WithCancel(ctx)
	defer checkerCancel()
	go checker.Start(checkerCtx)

	// Wait for the health checker to detect renewRequired and perform renewal
	time.Sleep(500 * time.Millisecond)

	mock.mu.Lock()
	renewCount := mock.renewCount
	mock.mu.Unlock()

	if renewCount < 1 {
		t.Errorf("expected at least 1 renewal, got %d", renewCount)
	}

	// After renewal, the API key should be updated
	if got := mgr.GetAPIKey(); got != mock.renewedAPIKey {
		t.Errorf("API key after auto-renewal = %q, want %q", got, mock.renewedAPIKey)
	}

	// Should be healthy after renewal
	if !checker.IsHealthy() {
		t.Errorf("expected healthy state after renewal, got %q", checker.State())
	}
}

// TestAPIClientSelfRegister tests the API client self-register method directly.
func TestAPIClientSelfRegister(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	apiClient := client.NewAPIClient(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := apiClient.SelfRegister(ctx, bootstrapToken, &client.SelfRegisterRequest{
		PublicURL: "https://my-mcp.example.com",
		Metadata:  map[string]string{"region": "eu-west-1"},
	})
	if err != nil {
		t.Fatalf("self-register failed: %v", err)
	}

	if resp.MCPID != mock.mcpID {
		t.Errorf("MCP ID = %q, want %q", resp.MCPID, mock.mcpID)
	}
	if resp.APIKey != mock.apiKey {
		t.Errorf("API key = %q, want %q", resp.APIKey, mock.apiKey)
	}
}

// TestAPIClientValidateCredentials tests the API client validate method directly.
func TestAPIClientValidateCredentials(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	apiClient := client.NewAPIClient(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Valid key
	resp, err := apiClient.ValidateCredentials(ctx, mock.apiKey)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !resp.Valid {
		t.Error("expected valid = true for correct API key")
	}

	// Invalid key
	resp, err = apiClient.ValidateCredentials(ctx, "invalid-key")
	if err != nil {
		t.Fatalf("validate with invalid key failed: %v", err)
	}
	if resp.Valid {
		t.Error("expected valid = false for invalid API key")
	}
}

// TestAPIClientRenewCredentials tests the API client renew method directly.
func TestAPIClientRenewCredentials(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	apiClient := client.NewAPIClient(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := apiClient.RenewCredentials(ctx, mock.apiKey)
	if err != nil {
		t.Fatalf("renew failed: %v", err)
	}

	if resp.APIKey != mock.renewedAPIKey {
		t.Errorf("renewed API key = %q, want %q", resp.APIKey, mock.renewedAPIKey)
	}
}

// TestAPIClientRenewInvalidKey tests that renewal fails with an invalid key.
func TestAPIClientRenewInvalidKey(t *testing.T) {
	bootstrapToken := "test-token"
	mock := newMockServer(bootstrapToken)
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	apiClient := client.NewAPIClient(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := apiClient.RenewCredentials(ctx, "invalid-key")
	if err == nil {
		t.Fatal("expected renewal to fail with invalid key")
	}
}

// TestConfigValidation tests configuration validation.
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.SidecarConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: &config.SidecarConfig{
				MauriceURL: "https://api.example.com",
			},
			wantErr: false,
		},
		{
			name: "missing maurice_url",
			cfg: &config.SidecarConfig{
				BootstrapToken: "x",
			},
			wantErr: true,
		},
		{
			name:    "empty config",
			cfg:     &config.SidecarConfig{},
			wantErr: true,
		},
		{
			name: "valid mqtt + stdio local transport",
			cfg: &config.SidecarConfig{
				MauriceURL:        "https://api.example.com",
				DeploymentID:      "dep-1",
				MQTTBroker:        "ssl://127.0.0.1:8883",
				LocalMCPTransport: config.LocalMCPTransportSTDIO,
				LocalMCPCommand:   "/usr/local/bin/mcp-server",
			},
			wantErr: false,
		},
		{
			name: "valid mqtt with tcp scheme",
			cfg: &config.SidecarConfig{
				MauriceURL:        "https://api.example.com",
				DeploymentID:      "dep-1",
				MQTTBroker:        "tcp://127.0.0.1:1883",
				LocalMCPTransport: config.LocalMCPTransportSTDIO,
				LocalMCPCommand:   "/usr/local/bin/mcp-server",
			},
			wantErr: false,
		},
		{
			name: "invalid mqtt with unsupported scheme",
			cfg: &config.SidecarConfig{
				MauriceURL:        "https://api.example.com",
				DeploymentID:      "dep-1",
				MQTTBroker:        "http://127.0.0.1:1883",
				LocalMCPTransport: config.LocalMCPTransportSTDIO,
				LocalMCPCommand:   "/usr/local/bin/mcp-server",
			},
			wantErr: true,
		},
		{
			name: "invalid mqtt + stdio local transport missing command",
			cfg: &config.SidecarConfig{
				MauriceURL:        "https://api.example.com",
				DeploymentID:      "dep-1",
				MQTTBroker:        "ssl://127.0.0.1:8883",
				LocalMCPTransport: config.LocalMCPTransportSTDIO,
			},
			wantErr: true,
		},
		{
			name: "invalid local transport value",
			cfg: &config.SidecarConfig{
				MauriceURL:        "https://api.example.com",
				LocalMCPTransport: "grpc",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestConfigValidateForRegistration tests registration-specific validation.
func TestConfigValidateForRegistration(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.SidecarConfig
		wantErr bool
	}{
		{
			name: "valid for registration",
			cfg: &config.SidecarConfig{
				MauriceURL:     "https://api.example.com",
				BootstrapToken: "test-token",
			},
			wantErr: false,
		},
		{
			name: "missing bootstrap token",
			cfg: &config.SidecarConfig{
				MauriceURL: "https://api.example.com",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateForRegistration()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateForRegistration() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestDefaultConfig tests that NewDefaultConfig returns sensible defaults.
func TestDefaultConfig(t *testing.T) {
	cfg := config.NewDefaultConfig()

	if cfg.MCPType != "custom" {
		t.Errorf("MCPType = %q, want %q", cfg.MCPType, "custom")
	}
	if cfg.CredentialsPath != "/var/mcp/credentials" {
		t.Errorf("CredentialsPath = %q, want %q", cfg.CredentialsPath, "/var/mcp/credentials")
	}
	if cfg.CheckInterval != 5*time.Minute {
		t.Errorf("CheckInterval = %v, want %v", cfg.CheckInterval, 5*time.Minute)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want %d", cfg.RetryAttempts, 3)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogEncoding != "console" {
		t.Errorf("LogEncoding = %q, want %q", cfg.LogEncoding, "console")
	}
	if cfg.LocalMCPTransport != config.LocalMCPTransportSTDIO {
		t.Errorf("LocalMCPTransport = %q, want %q", cfg.LocalMCPTransport, config.LocalMCPTransportSTDIO)
	}
}

// TestIdentityStoreRoundTrip tests save/load cycle for the credential store.
func TestIdentityStoreRoundTrip(t *testing.T) {
	dir := setupTestCredentialsDir(t)
	store := identity.NewStore(dir)

	if store.Exists() {
		t.Fatal("store should not exist initially")
	}

	// Save
	creds := &identity.Credentials{
		MCPID:  "test-mcp-id",
		APIKey: "test-api-key",
	}
	if err := store.Save(creds); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	if !store.Exists() {
		t.Fatal("store should exist after save")
	}

	// Load
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if loaded.MCPID != creds.MCPID {
		t.Errorf("loaded MCPID = %q, want %q", loaded.MCPID, creds.MCPID)
	}
	if loaded.APIKey != creds.APIKey {
		t.Errorf("loaded APIKey = %q, want %q", loaded.APIKey, creds.APIKey)
	}
	if loaded.SavedAt == "" {
		t.Error("expected SavedAt to be set after save")
	}

	// Delete
	if err := store.Delete(); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if store.Exists() {
		t.Error("store should not exist after delete")
	}
}

// TestIdentityStoreLoadNonExistent tests loading from a non-existent store.
func TestIdentityStoreLoadNonExistent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	store := identity.NewStore(dir)

	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error when loading from non-existent store")
	}
}
