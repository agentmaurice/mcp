package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the browser MCP server
type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Browser BrowserConfig `mapstructure:"browser"`
	Buffer  BufferConfig  `mapstructure:"buffer"`
	Logging LoggingConfig `mapstructure:"logging"`
}

// ServerConfig holds server-related configuration
type ServerConfig struct {
	Address     string `mapstructure:"address"`
	SSEPath     string `mapstructure:"sse_path"`
	MessagePath string `mapstructure:"message_path"`
	HealthPath  string `mapstructure:"health_path"`
}

// BrowserConfig holds browser-related configuration
type BrowserConfig struct {
	CDPEndpoint        string        `mapstructure:"cdp_endpoint"`              // WebSocket endpoint for CDP
	Headless           bool          `mapstructure:"headless"`                  // Run in headless mode
	DefaultTimeout     time.Duration `mapstructure:"default_timeout"`           // Default timeout for operations
	NavigationTimeout  time.Duration `mapstructure:"navigation_timeout"`        // Timeout for navigation
	AutoReconnect      bool          `mapstructure:"auto_reconnect"`            // Reconnect and retry transient browser failures
	ReconnectRetries   int           `mapstructure:"reconnect_retries"`         // Additional retry attempts after reconnect
	ScreenshotFormat   string        `mapstructure:"screenshot_format"`         // png or jpeg
	ScreenshotQuality  int           `mapstructure:"screenshot_quality"`        // 0-100 for jpeg
	UserAgent          string        `mapstructure:"user_agent"`                // Custom user agent
	WindowWidth        int           `mapstructure:"window_width"`              // Browser window width
	WindowHeight       int           `mapstructure:"window_height"`             // Browser window height
	PoolMode           string        `mapstructure:"pool_mode"`                 // single, internal, external
	PoolSize           int           `mapstructure:"pool_size"`                 // Number of local browser instances for internal mode
	PoolEndpoints      []string      `mapstructure:"pool_endpoints"`            // Explicit external CDP endpoints
	PoolManagerURL     string        `mapstructure:"pool_manager_url"`          // Dedicated external pool-manager endpoint
	PoolManagerToken   string        `mapstructure:"pool_manager_token"`        // Optional bearer token for pool-manager
	PoolManagerTimeout time.Duration `mapstructure:"pool_manager_timeout"`      // Timeout for pool-manager API calls
	PoolClientID       string        `mapstructure:"pool_client_id"`            // Optional client identifier for pool-manager
	PoolID             string        `mapstructure:"pool_id"`                   // Logical pool identifier for external orchestrator
	MaxSessionsPerInst int           `mapstructure:"max_sessions_per_instance"` // Max sticky sessions per instance (0 = unlimited)
	SessionIdleTTL     time.Duration `mapstructure:"session_idle_ttl"`          // Session eviction TTL
	AcquireTimeout     time.Duration `mapstructure:"acquire_timeout"`           // Timeout while waiting for a free pool slot
	MaxQueueDepth      int           `mapstructure:"max_queue_depth"`           // Max waiting acquires before rejecting (0 = unlimited)
	SelectionPolicy    string        `mapstructure:"selection_policy"`          // least_loaded or round_robin
	CaptureMaxPerSess  int           `mapstructure:"capture_max_per_session"`  // Max named captures per session
	CaptureMaxBytes    int64         `mapstructure:"capture_max_bytes"`        // Max capture memory per session (bytes)
	NetworkMaxBodyBytes int          `mapstructure:"network_max_body_bytes"`   // Max captured network body size (bytes)
}

// BufferConfig holds buffer upload configuration.
type BufferConfig struct {
	Enabled            bool          `mapstructure:"enabled"`              // Enable buffer uploads by default
	URL                string        `mapstructure:"url"`                  // Base buffer service URL
	AuthToken          string        `mapstructure:"auth_token"`           // Bearer token for buffer service
	Namespace          string        `mapstructure:"namespace"`            // Buffer namespace
	SoftThresholdBytes int           `mapstructure:"soft_threshold_bytes"` // Try buffering above this size
	HardThresholdBytes int           `mapstructure:"hard_threshold_bytes"` // Warn above this size if not buffered
	MaxUploadBytes     int           `mapstructure:"max_upload_bytes"`     // Maximum upload request size
	TTLSeconds         int           `mapstructure:"ttl_seconds"`          // Buffer entry TTL in seconds
	NetworkRetries     int           `mapstructure:"network_retries"`      // Retry count for network failures
	Timeout            time.Duration `mapstructure:"timeout"`              // HTTP timeout for upload
}

// LoggingConfig holds logging-related configuration
type LoggingConfig struct {
	Level      string `mapstructure:"level"`       // debug, info, warn, error
	Format     string `mapstructure:"format"`      // json or console
	OutputPath string `mapstructure:"output_path"` // stdout or file path
}

// Load loads configuration from file and environment variables
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	setDefaults(v)

	// Set config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("json")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found, use defaults and env vars
	}

	// Environment variable override
	v.SetEnvPrefix("BROWSER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Environment overrides for list fields are provided as comma-separated values.
	if len(config.Browser.PoolEndpoints) == 0 {
		rawEndpoints := strings.TrimSpace(v.GetString("browser.pool_endpoints"))
		if rawEndpoints != "" {
			parts := strings.Split(rawEndpoints, ",")
			endpoints := make([]string, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					endpoints = append(endpoints, part)
				}
			}
			config.Browser.PoolEndpoints = endpoints
		}
	}

	// Validate configuration
	if err := validate(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// setDefaults sets default configuration values
func setDefaults(v *viper.Viper) {
	// Server defaults
	v.SetDefault("server.address", ":3000")
	v.SetDefault("server.sse_path", "/mcp/sse")
	v.SetDefault("server.message_path", "/mcp/message")
	v.SetDefault("server.health_path", "/health")

	// Browser defaults
	v.SetDefault("browser.cdp_endpoint", "ws://127.0.0.1:9222")
	v.SetDefault("browser.headless", true)
	v.SetDefault("browser.default_timeout", "30s")
	v.SetDefault("browser.navigation_timeout", "60s")
	v.SetDefault("browser.auto_reconnect", true)
	v.SetDefault("browser.reconnect_retries", 1)
	v.SetDefault("browser.screenshot_format", "png")
	v.SetDefault("browser.screenshot_quality", 80)
	v.SetDefault("browser.user_agent", "")
	v.SetDefault("browser.window_width", 1920)
	v.SetDefault("browser.window_height", 1080)
	v.SetDefault("browser.pool_mode", "single")
	v.SetDefault("browser.pool_size", 1)
	v.SetDefault("browser.pool_endpoints", []string{})
	v.SetDefault("browser.pool_manager_url", "")
	v.SetDefault("browser.pool_manager_token", "")
	v.SetDefault("browser.pool_manager_timeout", "2s")
	v.SetDefault("browser.pool_client_id", "")
	v.SetDefault("browser.pool_id", "default")
	v.SetDefault("browser.max_sessions_per_instance", 0)
	v.SetDefault("browser.session_idle_ttl", "10m")
	v.SetDefault("browser.acquire_timeout", "2s")
	v.SetDefault("browser.max_queue_depth", 128)
	v.SetDefault("browser.selection_policy", "least_loaded")

	// Visual diffing defaults
	v.SetDefault("browser.capture_max_per_session", 20)
	v.SetDefault("browser.capture_max_bytes", 50000000) // 50MB

	// Network interception defaults
	v.SetDefault("browser.network_max_body_bytes", 1000000) // 1MB

	// Buffer defaults
	v.SetDefault("buffer.enabled", false)
	v.SetDefault("buffer.url", "")
	v.SetDefault("buffer.auth_token", "")
	v.SetDefault("buffer.namespace", "browser")
	v.SetDefault("buffer.soft_threshold_bytes", 500000)
	v.SetDefault("buffer.hard_threshold_bytes", 1000000)
	v.SetDefault("buffer.max_upload_bytes", 20000000)
	v.SetDefault("buffer.ttl_seconds", 600)
	v.SetDefault("buffer.network_retries", 1)
	v.SetDefault("buffer.timeout", "10s")

	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output_path", "stdout")
}

// validate validates the configuration
func validate(config *Config) error {
	if config.Server.Address == "" {
		return fmt.Errorf("server.address is required")
	}

	if config.Browser.CDPEndpoint == "" {
		return fmt.Errorf("browser.cdp_endpoint is required")
	}

	if config.Browser.DefaultTimeout <= 0 {
		return fmt.Errorf("browser.default_timeout must be positive")
	}

	if config.Browser.NavigationTimeout <= 0 {
		return fmt.Errorf("browser.navigation_timeout must be positive")
	}

	if config.Browser.ReconnectRetries < 0 {
		return fmt.Errorf("browser.reconnect_retries must be >= 0")
	}

	if config.Browser.ScreenshotFormat != "png" && config.Browser.ScreenshotFormat != "jpeg" {
		return fmt.Errorf("browser.screenshot_format must be 'png' or 'jpeg'")
	}

	if config.Browser.ScreenshotQuality < 0 || config.Browser.ScreenshotQuality > 100 {
		return fmt.Errorf("browser.screenshot_quality must be between 0 and 100")
	}

	switch config.Browser.PoolMode {
	case "single", "internal", "external":
	default:
		return fmt.Errorf("browser.pool_mode must be one of: single, internal, external")
	}

	if config.Browser.PoolSize <= 0 {
		return fmt.Errorf("browser.pool_size must be > 0")
	}

	if config.Browser.MaxSessionsPerInst < 0 {
		return fmt.Errorf("browser.max_sessions_per_instance must be >= 0")
	}

	if config.Browser.PoolManagerTimeout <= 0 {
		return fmt.Errorf("browser.pool_manager_timeout must be positive")
	}

	if config.Browser.SessionIdleTTL < 0 {
		return fmt.Errorf("browser.session_idle_ttl must be >= 0")
	}

	if config.Browser.AcquireTimeout <= 0 {
		return fmt.Errorf("browser.acquire_timeout must be positive")
	}

	if config.Browser.MaxQueueDepth < 0 {
		return fmt.Errorf("browser.max_queue_depth must be >= 0")
	}

	switch config.Browser.SelectionPolicy {
	case "least_loaded", "round_robin":
	default:
		return fmt.Errorf("browser.selection_policy must be one of: least_loaded, round_robin")
	}

	if config.Browser.PoolMode == "external" && strings.TrimSpace(config.Browser.PoolManagerURL) == "" && len(config.Browser.PoolEndpoints) == 0 {
		return fmt.Errorf("browser.pool_manager_url or browser.pool_endpoints must be set when browser.pool_mode=external")
	}

	if config.Buffer.SoftThresholdBytes <= 0 {
		return fmt.Errorf("buffer.soft_threshold_bytes must be positive")
	}

	if config.Buffer.HardThresholdBytes <= 0 {
		return fmt.Errorf("buffer.hard_threshold_bytes must be positive")
	}

	if config.Buffer.HardThresholdBytes < config.Buffer.SoftThresholdBytes {
		return fmt.Errorf("buffer.hard_threshold_bytes must be greater than or equal to buffer.soft_threshold_bytes")
	}

	if config.Buffer.MaxUploadBytes <= 0 {
		return fmt.Errorf("buffer.max_upload_bytes must be positive")
	}

	if config.Buffer.TTLSeconds <= 0 {
		return fmt.Errorf("buffer.ttl_seconds must be positive")
	}

	if config.Buffer.NetworkRetries < 0 {
		return fmt.Errorf("buffer.network_retries must be >= 0")
	}

	if config.Buffer.Timeout <= 0 {
		return fmt.Errorf("buffer.timeout must be positive")
	}

	return nil
}
