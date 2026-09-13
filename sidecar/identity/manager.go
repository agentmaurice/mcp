package identity

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/agentmaurice/mcpchatui/mcp/sidecar/client"
	"github.com/agentmaurice/mcpchatui/mcp/sidecar/config"
	"go.uber.org/zap"
)

// Manager handles MCP identity lifecycle.
type Manager struct {
	config *config.SidecarConfig
	store  *Store
	client *client.APIClient
	creds  *Credentials
	logger *zap.Logger
	mu     sync.RWMutex
}

// NewManager creates a new identity manager.
func NewManager(cfg *config.SidecarConfig, logger *zap.Logger) *Manager {
	return &Manager{
		config: cfg,
		store:  NewStore(cfg.CredentialsPath),
		client: client.NewAPIClient(cfg.MauriceURL),
		logger: logger.Named("identity"),
	}
}

// HasCredentials checks if credentials exist locally.
func (m *Manager) HasCredentials() bool {
	if m.store.Exists() {
		creds, err := m.store.Load()
		if err == nil && creds != nil && creds.APIKey != "" {
			m.mu.Lock()
			m.creds = creds
			m.mu.Unlock()
			return true
		}
	}
	return false
}

// LoadCredentials loads credentials from the store.
func (m *Manager) LoadCredentials() error {
	creds, err := m.store.Load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.creds = creds
	m.mu.Unlock()
	return nil
}

// Register performs initial registration with AgentMaurice.
func (m *Manager) Register(ctx context.Context) error {
	if m.config.BootstrapToken == "" {
		return fmt.Errorf("no bootstrap token configured")
	}

	m.logger.Info("Starting registration",
		zap.String("maurice_url", m.config.MauriceURL),
	)

	req := &client.SelfRegisterRequest{
		PublicKey: m.config.PublicKey,
		Metadata:  m.config.Metadata,
	}

	resp, err := m.client.SelfRegister(ctx, m.config.BootstrapToken, req)
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	// Store credentials
	m.mu.Lock()
	m.creds = &Credentials{
		MCPID:    resp.MCPID,
		APIKey:   resp.APIKey,
		TenantID: resp.TenantID,
	}
	m.mu.Unlock()

	// Persist to disk
	if err := m.store.Save(m.creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	// Optional runtime config injection returned by server at self-register time.
	if strings.TrimSpace(m.config.DeploymentID) == "" {
		if injected := strings.TrimSpace(resp.DeploymentID); injected != "" {
			m.config.DeploymentID = injected
		} else if tenant := strings.TrimSpace(resp.TenantID); tenant != "" {
			m.config.DeploymentID = tenant
		}
	}
	if strings.TrimSpace(m.config.MQTTBroker) == "" && strings.TrimSpace(resp.MQTTBroker) != "" {
		if strings.TrimSpace(m.config.LocalMCPCommand) == "" {
			m.logger.Warn("Skipping injected MQTT broker because local_mcp_command is empty")
		} else {
			m.config.MQTTBroker = strings.TrimSpace(resp.MQTTBroker)
			m.logger.Info("Injected MQTT broker from registration response",
				zap.String("mqtt_broker", m.config.MQTTBroker),
				zap.String("deployment_id", m.config.DeploymentID),
			)
		}
	}

	m.logger.Info("Registration successful",
		zap.String("mcp_id", resp.MCPID),
	)

	return nil
}

// Validate validates current credentials with the server.
func (m *Manager) Validate(ctx context.Context) (*client.ValidateResponse, error) {
	m.mu.RLock()
	apiKey := ""
	if m.creds != nil {
		apiKey = m.creds.APIKey
	}
	m.mu.RUnlock()

	if apiKey == "" {
		return &client.ValidateResponse{Valid: false}, nil
	}

	return m.client.ValidateCredentials(ctx, apiKey)
}

// Renew renews the current credentials.
func (m *Manager) Renew(ctx context.Context) error {
	m.mu.RLock()
	oldAPIKey := ""
	mcpID := ""
	if m.creds != nil {
		oldAPIKey = m.creds.APIKey
		mcpID = m.creds.MCPID
	}
	m.mu.RUnlock()

	if oldAPIKey == "" {
		return fmt.Errorf("no credentials to renew")
	}

	m.logger.Info("Renewing credentials",
		zap.String("mcp_id", mcpID),
	)

	resp, err := m.client.RenewCredentials(ctx, oldAPIKey)
	if err != nil {
		return fmt.Errorf("renewal failed: %w", err)
	}

	// Update credentials
	m.mu.Lock()
	m.creds.APIKey = resp.APIKey
	m.mu.Unlock()

	// Persist to disk
	if err := m.store.Save(m.creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	m.logger.Info("Credentials renewed successfully")

	return nil
}

// GetAPIKey returns the current API key.
func (m *Manager) GetAPIKey() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.creds != nil {
		return m.creds.APIKey
	}
	return ""
}

// GetMCPID returns the MCP server ID.
func (m *Manager) GetMCPID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.creds != nil {
		return m.creds.MCPID
	}
	return ""
}

// GetTenantID returns the tenant/deployment ID associated with the current credentials.
func (m *Manager) GetTenantID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.creds != nil {
		return m.creds.TenantID
	}
	return ""
}

// ClearCredentials removes local credentials.
func (m *Manager) ClearCredentials() error {
	m.mu.Lock()
	m.creds = nil
	m.mu.Unlock()
	return m.store.Delete()
}
