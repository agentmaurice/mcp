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

	keyGeminiAPIKey = "gemini.api_key"
	keyGeminiURL    = "gemini.url"
	keyGeminiTimeout = "gemini.timeout"

	keyDatabasePath = "database.path"

	defaultServerAddr            = ":8080"
	defaultServerBasePath        = "/mcp/filesearch"
	defaultServerKeepAlive       = true
	defaultServerKeepAlivePeriod = "15s"

	defaultLoggingLevel = "info"

	defaultGeminiURL     = "https://generativelanguage.googleapis.com"
	defaultGeminiTimeout = "30s"

	defaultDatabasePath = "/data/filesearch.db"
)

// Config aggregates runtime configuration for the filesearch service.
type Config struct {
	Server   ServerConfig
	Logging  LoggingConfig
	Gemini   GeminiConfig
	Database DatabaseConfig
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

// GeminiConfig defines the Gemini API settings.
type GeminiConfig struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

// DatabaseConfig defines the database settings.
type DatabaseConfig struct {
	Path string
}

// Load reads configuration from environment variables, applying defaults and validation.
func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Server configuration defaults and bindings.
	v.SetDefault(keyServerAddr, defaultServerAddr)
	_ = v.BindEnv(keyServerAddr, "MCP_FILESEARCH_ADDR", "SERVER_ADDR")

	v.SetDefault(keyServerBasePath, defaultServerBasePath)
	_ = v.BindEnv(keyServerBasePath, "MCP_FILESEARCH_BASE_PATH", "SERVER_BASE_PATH")

	v.SetDefault(keyServerPublicURL, "")
	_ = v.BindEnv(keyServerPublicURL, "MCP_FILESEARCH_PUBLIC_URL", "SERVER_PUBLIC_URL")

	v.SetDefault(keyServerKeepAlive, defaultServerKeepAlive)
	_ = v.BindEnv(keyServerKeepAlive, "MCP_FILESEARCH_KEEP_ALIVE", "SERVER_KEEP_ALIVE")

	v.SetDefault(keyServerKeepAlivePeriod, defaultServerKeepAlivePeriod)
	_ = v.BindEnv(keyServerKeepAlivePeriod, "MCP_FILESEARCH_KEEP_ALIVE_INTERVAL", "SERVER_KEEP_ALIVE_INTERVAL")

	// Logging configuration.
	v.SetDefault(keyLoggingLevel, defaultLoggingLevel)
	_ = v.BindEnv(keyLoggingLevel, "MCP_FILESEARCH_LOG_LEVEL", "LOG_LEVEL")

	// Gemini configuration defaults and bindings.
	v.SetDefault(keyGeminiURL, defaultGeminiURL)
	_ = v.BindEnv(keyGeminiURL, "MCP_FILESEARCH_GEMINI_URL", "GEMINI_API_URL")

	v.SetDefault(keyGeminiAPIKey, "")
	_ = v.BindEnv(keyGeminiAPIKey, "MCP_FILESEARCH_GEMINI_API_KEY", "GEMINI_API_KEY")

	v.SetDefault(keyGeminiTimeout, defaultGeminiTimeout)
	_ = v.BindEnv(keyGeminiTimeout, "MCP_FILESEARCH_GEMINI_TIMEOUT", "GEMINI_TIMEOUT")

	// Database configuration.
	v.SetDefault(keyDatabasePath, defaultDatabasePath)
	_ = v.BindEnv(keyDatabasePath, "MCP_FILESEARCH_DB_PATH", "DATABASE_PATH")

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
		Gemini: GeminiConfig{
			BaseURL: strings.TrimSpace(v.GetString(keyGeminiURL)),
			APIKey:  strings.TrimSpace(v.GetString(keyGeminiAPIKey)),
		},
		Database: DatabaseConfig{
			Path: strings.TrimSpace(v.GetString(keyDatabasePath)),
		},
	}

	// Parse durations
	geminiTimeout, err := time.ParseDuration(v.GetString(keyGeminiTimeout))
	if err != nil {
		return nil, fmt.Errorf("invalid duration for %q: %w", keyGeminiTimeout, err)
	}
	if geminiTimeout <= 0 {
		return nil, fmt.Errorf("%s must be positive", keyGeminiTimeout)
	}
	cfg.Gemini.Timeout = geminiTimeout

	keepAliveInterval, err := time.ParseDuration(v.GetString(keyServerKeepAlivePeriod))
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
	if cfg.Gemini.APIKey == "" {
		return fmt.Errorf("Gemini API key is required (set MCP_FILESEARCH_GEMINI_API_KEY or GEMINI_API_KEY)")
	}
	if cfg.Server.Addr == "" {
		return fmt.Errorf("server listen address cannot be empty")
	}
	if !strings.HasPrefix(cfg.Server.BasePath, "/") {
		return fmt.Errorf("server base path must start with '/'")
	}
	if cfg.Database.Path == "" {
		return fmt.Errorf("database path cannot be empty")
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
