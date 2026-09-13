package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	keyServerAddr            = "server.addr"
	keyServerBasePath        = "server.base_path"
	keyServerPublicURL       = "server.public_url"
	keyServerKeepAlive       = "server.keep_alive"
	keyServerKeepAlivePeriod = "server.keep_alive_interval"

	keyLoggingLevel = "logging.level"

	keyMistralURL     = "mistral.url"
	keyMistralAPIKey  = "mistral.api_key"
	keyMistralModel   = "mistral.model"
	keyMistralTimeout = "mistral.timeout"

	defaultServerAddr            = ":8080"
	defaultServerBasePath        = "/mcp/moderator"
	defaultServerKeepAlive       = true
	defaultServerKeepAlivePeriod = "15s"

	defaultLoggingLevel = "info"

	defaultMistralURL     = "https://api.mistral.ai"
	defaultMistralModel   = "mistral-moderation-latest"
	defaultMistralTimeout = "10s"
)

// Config aggregates runtime configuration for the moderator service.
type Config struct {
	Server  ServerConfig
	Logging LoggingConfig
	Mistral MistralConfig
}

// ServerConfig controls how the SSE server is exposed.
type ServerConfig struct {
	Addr              string
	BasePath          string
	PublicURL         string
	KeepAlive         bool
	KeepAliveInterval time.Duration
}

// LoggingConfig captures logging preferences.
type LoggingConfig struct {
	Level string
}

// MistralConfig defines the remote moderation API settings.
type MistralConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// Load reads configuration from environment variables, applying defaults and validation.
func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Server configuration defaults and bindings.
	v.SetDefault(keyServerAddr, defaultServerAddr)
	_ = v.BindEnv(keyServerAddr, "MCP_MODERATOR_ADDR", "SERVER_ADDR")

	v.SetDefault(keyServerBasePath, defaultServerBasePath)
	_ = v.BindEnv(keyServerBasePath, "MCP_MODERATOR_BASE_PATH", "SERVER_BASE_PATH")

	v.SetDefault(keyServerPublicURL, "")
	_ = v.BindEnv(keyServerPublicURL, "MCP_MODERATOR_PUBLIC_URL", "SERVER_PUBLIC_URL")

	v.SetDefault(keyServerKeepAlive, defaultServerKeepAlive)
	_ = v.BindEnv(keyServerKeepAlive, "MCP_MODERATOR_KEEP_ALIVE", "SERVER_KEEP_ALIVE")

	v.SetDefault(keyServerKeepAlivePeriod, defaultServerKeepAlivePeriod)
	_ = v.BindEnv(keyServerKeepAlivePeriod, "MCP_MODERATOR_KEEP_ALIVE_INTERVAL", "SERVER_KEEP_ALIVE_INTERVAL")

	// Logging configuration.
	v.SetDefault(keyLoggingLevel, defaultLoggingLevel)
	_ = v.BindEnv(keyLoggingLevel, "MCP_MODERATOR_LOG_LEVEL", "LOG_LEVEL")

	// Mistral configuration defaults and bindings.
	v.SetDefault(keyMistralURL, defaultMistralURL)
	_ = v.BindEnv(keyMistralURL, "MCP_MODERATOR_MISTRAL_URL", "MISTRAL_API_URL")

	v.SetDefault(keyMistralAPIKey, "")
	_ = v.BindEnv(keyMistralAPIKey, "MCP_MODERATOR_MISTRAL_API_KEY", "MISTRAL_API_KEY")

	v.SetDefault(keyMistralModel, defaultMistralModel)
	_ = v.BindEnv(keyMistralModel, "MCP_MODERATOR_MISTRAL_MODEL", "MISTRAL_MODEL")

	v.SetDefault(keyMistralTimeout, defaultMistralTimeout)
	_ = v.BindEnv(keyMistralTimeout, "MCP_MODERATOR_MISTRAL_TIMEOUT", "MISTRAL_TIMEOUT", "MISTRAL_REQUEST_TIMEOUT")

	cfg := &Config{
		Server: ServerConfig{
			Addr:      strings.TrimSpace(v.GetString(keyServerAddr)),
			BasePath:  normalizePath(v.GetString(keyServerBasePath)),
			PublicURL: strings.TrimSpace(v.GetString(keyServerPublicURL)),
			KeepAlive: v.GetBool(keyServerKeepAlive),
		},
		Logging: LoggingConfig{
			Level: strings.ToLower(strings.TrimSpace(v.GetString(keyLoggingLevel))),
		},
		Mistral: MistralConfig{
			BaseURL: strings.TrimSpace(v.GetString(keyMistralURL)),
			APIKey:  strings.TrimSpace(v.GetString(keyMistralAPIKey)),
			Model:   strings.TrimSpace(v.GetString(keyMistralModel)),
		},
	}

	timeoutRaw := v.GetString(keyMistralTimeout)
	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid duration for %q: %w", keyMistralTimeout, err)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("%s must be positive", keyMistralTimeout)
	}
	cfg.Mistral.Timeout = timeout

	keepAliveRaw := v.GetString(keyServerKeepAlivePeriod)
	keepAliveInterval, err := time.ParseDuration(keepAliveRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid duration for %q: %w", keyServerKeepAlivePeriod, err)
	}
	if keepAliveInterval <= 0 {
		return nil, fmt.Errorf("%s must be positive", keyServerKeepAlivePeriod)
	}
	cfg.Server.KeepAliveInterval = keepAliveInterval

	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Mistral.APIKey == "" {
		return fmt.Errorf("Mistral API key is required (set MCP_MODERATOR_MISTRAL_API_KEY or MISTRAL_API_KEY)")
	}
	if cfg.Server.Addr == "" {
		return fmt.Errorf("server listen address cannot be empty")
	}
	if !strings.HasPrefix(cfg.Server.BasePath, "/") {
		return fmt.Errorf("server base path must start with '/'")
	}
	return nil
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}
