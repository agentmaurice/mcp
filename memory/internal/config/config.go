package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config represents the application configuration.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Query    QueryConfig    `mapstructure:"query"`
	Buffer   BufferConfig   `mapstructure:"buffer"`
	Security SecurityConfig `mapstructure:"security"`
	Logging  LoggingConfig  `mapstructure:"logging"`
}

// ServerConfig represents MCP server configuration.
type ServerConfig struct {
	Address           string `mapstructure:"address"`
	BasePath          string `mapstructure:"base_path"`
	KeepAlive         bool   `mapstructure:"keep_alive"`
	KeepAliveInterval int    `mapstructure:"keep_alive_interval"` // in seconds
}

// StorageConfig represents tenant storage configuration.
type StorageConfig struct {
	Backend             string `mapstructure:"backend"`
	BaseDir             string `mapstructure:"base_dir"`
	TenantsDirName      string `mapstructure:"tenants_dir_name"`
	DBFilename          string `mapstructure:"db_filename"`
	DocumentsDirName    string `mapstructure:"documents_dir_name"`
	DefaultTenantID     string `mapstructure:"default_tenant_id"`
	ValidateLocalFiles  bool   `mapstructure:"validate_local_files"`
	MaxDocumentSizeByte int64  `mapstructure:"max_document_size_bytes"`
	ReadMaxOpenConns    int    `mapstructure:"read_max_open_conns"`
	ReadMaxIdleConns    int    `mapstructure:"read_max_idle_conns"`
	WriteQueueSize      int    `mapstructure:"write_queue_size"`

	PostgresDSN          string `mapstructure:"postgres_dsn"`
	PostgresSchemaPrefix string `mapstructure:"postgres_schema_prefix"`
	DuckDBHTTPURL        string `mapstructure:"duckdb_http_url"`
}

// QueryConfig controls query limits and timeouts.
type QueryConfig struct {
	MaxRowsDefault     int  `mapstructure:"max_rows_default"`
	MaxRowsLimit       int  `mapstructure:"max_rows_limit"`
	TimeoutMsDefault   int  `mapstructure:"timeout_ms_default"`
	TimeoutMsMax       int  `mapstructure:"timeout_ms_max"`
	MaxResponseBytes   int  `mapstructure:"max_response_bytes"`
	DisallowSelectStar bool `mapstructure:"disallow_select_star"`
}

// BufferConfig represents buffer service configuration for large payload handling.
type BufferConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	ServiceURL    string `mapstructure:"service_url"`    // URL of the buffer service (e.g., http://agentmaurice:3000/api/v1/buffer)
	Token         string `mapstructure:"token"`          // MCP Buffer authentication token
	Namespace     string `mapstructure:"namespace"`      // Default namespace for buffered content (default: "memory")
	SoftThreshold int64  `mapstructure:"soft_threshold"` // Size in bytes above which buffering is recommended (default: 512KB)
	HardThreshold int64  `mapstructure:"hard_threshold"` // Size in bytes above which buffering is mandatory (default: 2MB)
	DefaultTTL    int    `mapstructure:"default_ttl"`    // TTL in seconds for buffered content (default: 600)
}

// SecurityConfig holds RBAC/ABAC and redaction settings.
type SecurityConfig struct {
	Denylist         []string            `mapstructure:"denylist"`
	AllowedObjects   []string            `mapstructure:"allowed_objects"`
	AllowedColumns   map[string][]string `mapstructure:"allowed_columns"`
	AllowedIndexes   []string            `mapstructure:"allowed_indexes"`
	RequireViewsOnly bool                `mapstructure:"require_views_only"`
	RedactionKeys    []string            `mapstructure:"redaction_keys"`
	RedactValue      string              `mapstructure:"redact_value"`
}

// LoggingConfig represents logging configuration.
type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	EnableFile bool   `mapstructure:"enable_file"`
	LogDir     string `mapstructure:"log_dir"`
}

// Load loads configuration from file and environment variables.
func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("json")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")

	// Server env
	_ = viper.BindEnv("server.address", "SERVER_ADDRESS")
	_ = viper.BindEnv("server.base_path", "SERVER_BASE_PATH")
	_ = viper.BindEnv("server.keep_alive", "SERVER_KEEP_ALIVE")
	_ = viper.BindEnv("server.keep_alive_interval", "SERVER_KEEP_ALIVE_INTERVAL")

	// Storage env
	_ = viper.BindEnv("storage.backend", "MEMORY_STORAGE_BACKEND")
	_ = viper.BindEnv("storage.base_dir", "MEMORY_BASE_DIR")
	_ = viper.BindEnv("storage.tenants_dir_name", "MEMORY_TENANTS_DIR_NAME")
	_ = viper.BindEnv("storage.db_filename", "MEMORY_DB_FILENAME")
	_ = viper.BindEnv("storage.documents_dir_name", "MEMORY_DOCUMENTS_DIR_NAME")
	_ = viper.BindEnv("storage.default_tenant_id", "MEMORY_DEFAULT_TENANT_ID")
	_ = viper.BindEnv("storage.validate_local_files", "MEMORY_VALIDATE_LOCAL_FILES")
	_ = viper.BindEnv("storage.max_document_size_bytes", "MEMORY_MAX_DOCUMENT_SIZE_BYTES")
	_ = viper.BindEnv("storage.read_max_open_conns", "MEMORY_READ_MAX_OPEN_CONNS")
	_ = viper.BindEnv("storage.read_max_idle_conns", "MEMORY_READ_MAX_IDLE_CONNS")
	_ = viper.BindEnv("storage.write_queue_size", "MEMORY_WRITE_QUEUE_SIZE")
	_ = viper.BindEnv("storage.postgres_dsn", "MEMORY_POSTGRES_DSN")
	_ = viper.BindEnv("storage.postgres_schema_prefix", "MEMORY_POSTGRES_SCHEMA_PREFIX")
	_ = viper.BindEnv("storage.duckdb_http_url", "MEMORY_STORAGE_DUCKDB_HTTP_URL")

	// Query env
	_ = viper.BindEnv("query.max_rows_default", "MEMORY_QUERY_MAX_ROWS_DEFAULT")
	_ = viper.BindEnv("query.max_rows_limit", "MEMORY_QUERY_MAX_ROWS_LIMIT")
	_ = viper.BindEnv("query.timeout_ms_default", "MEMORY_QUERY_TIMEOUT_MS_DEFAULT")
	_ = viper.BindEnv("query.timeout_ms_max", "MEMORY_QUERY_TIMEOUT_MS_MAX")
	_ = viper.BindEnv("query.max_response_bytes", "MEMORY_QUERY_MAX_RESPONSE_BYTES")
	_ = viper.BindEnv("query.disallow_select_star", "MEMORY_QUERY_DISALLOW_SELECT_STAR")

	// Buffer env
	_ = viper.BindEnv("buffer.enabled", "BUFFER_ENABLED")
	_ = viper.BindEnv("buffer.service_url", "BUFFER_SERVICE_URL")
	_ = viper.BindEnv("buffer.token", "BUFFER_TOKEN")
	_ = viper.BindEnv("buffer.namespace", "BUFFER_NAMESPACE")
	_ = viper.BindEnv("buffer.soft_threshold", "BUFFER_SOFT_THRESHOLD")
	_ = viper.BindEnv("buffer.hard_threshold", "BUFFER_HARD_THRESHOLD")
	_ = viper.BindEnv("buffer.default_ttl", "BUFFER_DEFAULT_TTL")

	// Security env
	_ = viper.BindEnv("security.denylist", "MEMORY_SECURITY_DENYLIST")
	_ = viper.BindEnv("security.allowed_objects", "MEMORY_SECURITY_ALLOWED_OBJECTS")
	_ = viper.BindEnv("security.allowed_columns", "MEMORY_SECURITY_ALLOWED_COLUMNS")
	_ = viper.BindEnv("security.allowed_indexes", "MEMORY_SECURITY_ALLOWED_INDEXES")
	_ = viper.BindEnv("security.require_views_only", "MEMORY_SECURITY_REQUIRE_VIEWS_ONLY")
	_ = viper.BindEnv("security.redaction_keys", "MEMORY_SECURITY_REDACTION_KEYS")
	_ = viper.BindEnv("security.redact_value", "MEMORY_SECURITY_REDACT_VALUE")

	// Logging env
	_ = viper.BindEnv("logging.level", "LOGGING_LEVEL")
	_ = viper.BindEnv("logging.enable_file", "LOGGING_ENABLE_FILE")
	_ = viper.BindEnv("logging.log_dir", "LOGGING_LOG_DIR")

	// Defaults
	viper.SetDefault("server.address", ":8080")
	viper.SetDefault("server.base_path", "/mcp")
	viper.SetDefault("server.keep_alive", true)
	viper.SetDefault("server.keep_alive_interval", 15)

	viper.SetDefault("storage.backend", "duckdb")
	viper.SetDefault("storage.base_dir", "./data")
	viper.SetDefault("storage.tenants_dir_name", "tenants")
	viper.SetDefault("storage.db_filename", "memory.duckdb")
	viper.SetDefault("storage.documents_dir_name", "documents")
	viper.SetDefault("storage.default_tenant_id", "default")
	viper.SetDefault("storage.validate_local_files", false)
	viper.SetDefault("storage.max_document_size_bytes", int64(50*1024*1024))
	viper.SetDefault("storage.read_max_open_conns", 8)
	viper.SetDefault("storage.read_max_idle_conns", 4)
	viper.SetDefault("storage.write_queue_size", 128)
	viper.SetDefault("storage.postgres_schema_prefix", "tenant_")
	viper.SetDefault("storage.duckdb_http_url", "http://127.0.0.1:9802")

	viper.SetDefault("query.max_rows_default", 500)
	viper.SetDefault("query.max_rows_limit", 5000)
	viper.SetDefault("query.timeout_ms_default", 2000)
	viper.SetDefault("query.timeout_ms_max", 10000)
	viper.SetDefault("query.max_response_bytes", 2*1024*1024)
	viper.SetDefault("query.disallow_select_star", false)

	viper.SetDefault("buffer.namespace", "memory")
	viper.SetDefault("buffer.soft_threshold", 512*1024)
	viper.SetDefault("buffer.hard_threshold", 2*1024*1024)
	viper.SetDefault("buffer.default_ttl", 600)

	viper.SetDefault("security.denylist", []string{
		"insert", "update", "delete", "create", "drop", "alter", "copy", "attach", "detach", "pragma", "export", "import",
	})
	viper.SetDefault("security.allowed_objects", []string{})
	viper.SetDefault("security.allowed_columns", map[string][]string{})
	viper.SetDefault("security.allowed_indexes", []string{})
	viper.SetDefault("security.require_views_only", true)
	viper.SetDefault("security.redaction_keys", []string{"email", "phone", "iban", "ssn", "password"})
	viper.SetDefault("security.redact_value", "***redacted***")

	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.enable_file", false)
	viper.SetDefault("logging.log_dir", "./logs")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}
