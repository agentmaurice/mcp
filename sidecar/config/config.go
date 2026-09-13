package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// LocalMCPTransportHTTP is deprecated and no longer supported.
	LocalMCPTransportHTTP = "http"
	// LocalMCPTransportSTDIO forwards requests to a local MCP process over STDIO.
	LocalMCPTransportSTDIO = "stdio"
)

// SidecarConfig holds configuration for the MCP sidecar.
type SidecarConfig struct {
	// AgentMaurice connection
	MauriceURL     string `mapstructure:"maurice_url"`
	BootstrapToken string `mapstructure:"bootstrap_token"`

	// MCP Server identity
	// Deprecated: not used anymore, kept for config backward compatibility.
	PublicURL string            `mapstructure:"public_url"`
	PublicKey string            `mapstructure:"public_key"`
	MCPType   string            `mapstructure:"mcp_type"`
	Metadata  map[string]string `mapstructure:"metadata"`

	// MQTT gateway bridge (optional)
	DeploymentID string `mapstructure:"deployment_id"`
	MQTTBroker   string `mapstructure:"mqtt_broker"`
	MQTTClientID string `mapstructure:"mqtt_client_id"`
	MCPServerID  string `mapstructure:"mcp_server_id"`

	// Deprecated: only stdio is supported now.
	LocalMCPTransport string `mapstructure:"local_mcp_transport"`
	// Deprecated: HTTP local transport is removed.
	LocalMCPURL      string        `mapstructure:"local_mcp_url"`
	LocalMCPCommand  string        `mapstructure:"local_mcp_command"`
	LocalMCPArgs     []string      `mapstructure:"local_mcp_args"`
	LocalMCPEnv      []string      `mapstructure:"local_mcp_env"`
	RuntimeSpec      string        `mapstructure:"runtime_spec"`
	RuntimeWorkDir   string        `mapstructure:"runtime_workdir"`
	BootstrapTimeout time.Duration `mapstructure:"bootstrap_timeout"`

	// MQTT security
	MQTTCAFile             string `mapstructure:"mqtt_ca_file"`
	MQTTClientCertFile     string `mapstructure:"mqtt_client_cert_file"`
	MQTTClientKeyFile      string `mapstructure:"mqtt_client_key_file"`
	MQTTServerName         string `mapstructure:"mqtt_server_name"`
	MQTTInsecureSkipVerify bool   `mapstructure:"mqtt_insecure_skip_verify"`

	// MQTT/runtime robustness
	MQTTTimeout              time.Duration `mapstructure:"mqtt_timeout"`
	MQTTMaxPayloadBytes      int           `mapstructure:"mqtt_max_payload_bytes"`
	MQTTMaxDecompressedBytes int           `mapstructure:"mqtt_max_decompressed_bytes"`
	CommandTimeout           time.Duration `mapstructure:"command_timeout"`
	HeartbeatInterval        time.Duration `mapstructure:"heartbeat_interval"`
	MCPRestartMax            int           `mapstructure:"mcp_restart_max"`
	MCPRestartBackoffInitial time.Duration `mapstructure:"mcp_restart_backoff_initial"`
	MCPRestartBackoffMax     time.Duration `mapstructure:"mcp_restart_backoff_max"`

	// Credentials storage
	CredentialsPath string `mapstructure:"credentials_path"`

	// Healthcheck
	CheckInterval time.Duration `mapstructure:"check_interval"`
	RetryAttempts int           `mapstructure:"retry_attempts"`
	RetryDelay    time.Duration `mapstructure:"retry_delay"`

	// Logging
	LogLevel    string `mapstructure:"log_level"`
	LogEncoding string `mapstructure:"log_encoding"`
}

// NewDefaultConfig creates a new config with default values.
func NewDefaultConfig() *SidecarConfig {
	return &SidecarConfig{
		MCPType:                  "custom",
		CredentialsPath:          "/var/mcp/credentials",
		CheckInterval:            5 * time.Minute,
		RetryAttempts:            3,
		RetryDelay:               10 * time.Second,
		LocalMCPTransport:        LocalMCPTransportSTDIO,
		LocalMCPCommand:          "",
		RuntimeWorkDir:           "/tmp/mcp-sidecar-workspace",
		BootstrapTimeout:         10 * time.Minute,
		MQTTTimeout:              2 * time.Minute,
		MQTTMaxPayloadBytes:      1 << 20, // 1MB
		MQTTMaxDecompressedBytes: 4 << 20, // 4MB
		CommandTimeout:           2 * time.Minute,
		HeartbeatInterval:        30 * time.Second,
		MCPRestartMax:            5,
		MCPRestartBackoffInitial: 1 * time.Second,
		MCPRestartBackoffMax:     30 * time.Second,
		LogLevel:                 "info",
		LogEncoding:              "console",
	}
}

// Validate validates the configuration.
func (c *SidecarConfig) Validate() error {
	if c.MauriceURL == "" {
		return fmt.Errorf("maurice_url is required")
	}
	if c.MQTTBroker != "" {
		if c.DeploymentID == "" {
			return fmt.Errorf("deployment_id is required when mqtt_broker is configured")
		}
		broker := strings.ToLower(strings.TrimSpace(c.MQTTBroker))
		if !strings.HasPrefix(broker, "ssl://") &&
			!strings.HasPrefix(broker, "tls://") &&
			!strings.HasPrefix(broker, "mqtts://") &&
			!strings.HasPrefix(broker, "tcp://") &&
			!strings.HasPrefix(broker, "mqtt://") {
			return fmt.Errorf("mqtt_broker must use one of: ssl://, tls://, mqtts://, tcp:// or mqtt://")
		}
		if strings.TrimSpace(c.LocalMCPCommand) == "" {
			if strings.TrimSpace(c.RuntimeSpec) == "" {
				return fmt.Errorf("local_mcp_command is required when mqtt_broker is configured")
			}
		}
	}
	if strings.TrimSpace(c.LocalMCPTransport) != "" && strings.ToLower(strings.TrimSpace(c.LocalMCPTransport)) != LocalMCPTransportSTDIO {
		return fmt.Errorf("local_mcp_transport=%s is no longer supported; only stdio is allowed", c.LocalMCPTransport)
	}
	if c.MQTTClientCertFile != "" || c.MQTTClientKeyFile != "" {
		if c.MQTTClientCertFile == "" || c.MQTTClientKeyFile == "" {
			return fmt.Errorf("mqtt_client_cert_file and mqtt_client_key_file must be set together")
		}
	}
	if c.MQTTTimeout <= 0 {
		c.MQTTTimeout = 2 * time.Minute
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = 2 * time.Minute
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 30 * time.Second
	}
	if c.MQTTMaxPayloadBytes <= 0 {
		c.MQTTMaxPayloadBytes = 1 << 20
	}
	if c.MQTTMaxDecompressedBytes <= 0 {
		c.MQTTMaxDecompressedBytes = 4 << 20
	}
	if c.MCPRestartMax < 0 {
		c.MCPRestartMax = 0
	}
	if c.MCPRestartBackoffInitial <= 0 {
		c.MCPRestartBackoffInitial = time.Second
	}
	if c.MCPRestartBackoffMax <= 0 {
		c.MCPRestartBackoffMax = 30 * time.Second
	}
	if strings.TrimSpace(c.RuntimeWorkDir) == "" {
		c.RuntimeWorkDir = "/tmp/mcp-sidecar-workspace"
	}
	if c.BootstrapTimeout <= 0 {
		c.BootstrapTimeout = 10 * time.Minute
	}
	return nil
}

// ValidateForRegistration validates config required for initial registration.
func (c *SidecarConfig) ValidateForRegistration() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.BootstrapToken == "" {
		return fmt.Errorf("bootstrap_token is required for registration")
	}
	return nil
}

func (c *SidecarConfig) TLSConfig() (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.MQTTInsecureSkipVerify,
	}
	if strings.TrimSpace(c.MQTTServerName) != "" {
		tlsConfig.ServerName = strings.TrimSpace(c.MQTTServerName)
	}

	if strings.TrimSpace(c.MQTTCAFile) != "" {
		pem, err := os.ReadFile(c.MQTTCAFile)
		if err != nil {
			return nil, fmt.Errorf("read mqtt_ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid certificate in mqtt_ca_file")
		}
		tlsConfig.RootCAs = pool
	}

	if strings.TrimSpace(c.MQTTClientCertFile) != "" || strings.TrimSpace(c.MQTTClientKeyFile) != "" {
		cert, err := tls.LoadX509KeyPair(c.MQTTClientCertFile, c.MQTTClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load mqtt client cert/key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}
