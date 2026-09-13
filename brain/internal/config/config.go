package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config represents the application configuration.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Storage   StorageConfig   `mapstructure:"storage"`
	Embedding EmbeddingConfig `mapstructure:"embedding"`
	Indexing  IndexingConfig  `mapstructure:"indexing"`
	Search    SearchConfig    `mapstructure:"search"`
	Buffer    BufferConfig    `mapstructure:"buffer"`
	Logging   LoggingConfig   `mapstructure:"logging"`
}

// ServerConfig represents MCP server configuration.
type ServerConfig struct {
	Address           string `mapstructure:"address"`
	BasePath          string `mapstructure:"base_path"`
	KeepAlive         bool   `mapstructure:"keep_alive"`
	KeepAliveInterval int    `mapstructure:"keep_alive_interval"` // in seconds
}

// StorageConfig represents storage configuration.
type StorageConfig struct {
	Backend              string `mapstructure:"backend"`
	BaseDir              string `mapstructure:"base_dir"`
	TenantsDirName       string `mapstructure:"tenants_dir_name"`
	DBFilename           string `mapstructure:"db_filename"`
	DefaultTenantID      string `mapstructure:"default_tenant_id"`
	ReadMaxOpenConns     int    `mapstructure:"read_max_open_conns"`
	ReadMaxIdleConns     int    `mapstructure:"read_max_idle_conns"`
	WriteQueueSize       int    `mapstructure:"write_queue_size"`
	PostgresDSN          string `mapstructure:"postgres_dsn"`
	PostgresSchemaPrefix string `mapstructure:"postgres_schema_prefix"`
	DuckDBHTTPURL        string `mapstructure:"duckdb_http_url" json:"duckdb_http_url"`
}

// EmbeddingConfig represents embedding provider configuration.
type EmbeddingConfig struct {
	Provider      string `mapstructure:"provider"` // 'none', 'ollama', 'openai', 'openai-compatible'
	Model         string `mapstructure:"model"`
	Dimensions    int    `mapstructure:"dimensions"`
	BatchSize     int    `mapstructure:"batch_size"`
	OllamaURL     string `mapstructure:"ollama_url"`
	OpenAIBaseURL string `mapstructure:"openai_base_url"`
	OpenAIKey     string `mapstructure:"openai_api_key"`
	CohereKey     string `mapstructure:"cohere_api_key"`
}

// IndexingConfig represents indexation configuration.
type IndexingConfig struct {
	Workers            int    `mapstructure:"workers"`
	ChunkMaxTokens     int    `mapstructure:"chunk_max_tokens"`
	ChunkOverlap       int    `mapstructure:"chunk_overlap_tokens"`
	MaxDocumentBytes   int64  `mapstructure:"max_document_bytes"`
	EnrichmentEnabled  bool   `mapstructure:"enrichment_enabled"`
	EnrichmentProvider string `mapstructure:"enrichment_provider"`
	EnrichmentModel    string `mapstructure:"enrichment_model"`
}

// SearchConfig represents search configuration.
type SearchConfig struct {
	DefaultMaxResults  int     `mapstructure:"default_max_results"`
	MaxResultsLimit    int     `mapstructure:"max_results_limit"`
	TimeoutMs          int     `mapstructure:"timeout_ms"`
	HybridDefaultAlpha float64 `mapstructure:"hybrid_default_alpha"`
	RRFK               int     `mapstructure:"rrf_k"`
}

// BufferConfig represents buffer service configuration for large payload handling.
type BufferConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	ServiceURL    string `mapstructure:"service_url"`
	Token         string `mapstructure:"token"`
	Namespace     string `mapstructure:"namespace"`
	SoftThreshold int64  `mapstructure:"soft_threshold"`
	HardThreshold int64  `mapstructure:"hard_threshold"`
	DefaultTTL    int    `mapstructure:"default_ttl"`
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
	_ = viper.BindEnv("server.address", "BRAIN_SERVER_ADDRESS")
	_ = viper.BindEnv("server.base_path", "BRAIN_SERVER_BASE_PATH")
	_ = viper.BindEnv("server.keep_alive", "BRAIN_SERVER_KEEP_ALIVE")
	_ = viper.BindEnv("server.keep_alive_interval", "BRAIN_SERVER_KEEP_ALIVE_INTERVAL")

	// Storage env
	_ = viper.BindEnv("storage.backend", "BRAIN_STORAGE_BACKEND")
	_ = viper.BindEnv("storage.base_dir", "BRAIN_BASE_DIR")
	_ = viper.BindEnv("storage.tenants_dir_name", "BRAIN_TENANTS_DIR_NAME")
	_ = viper.BindEnv("storage.db_filename", "BRAIN_DB_FILENAME")
	_ = viper.BindEnv("storage.default_tenant_id", "BRAIN_DEFAULT_TENANT_ID")
	_ = viper.BindEnv("storage.read_max_open_conns", "BRAIN_READ_MAX_OPEN_CONNS")
	_ = viper.BindEnv("storage.read_max_idle_conns", "BRAIN_READ_MAX_IDLE_CONNS")
	_ = viper.BindEnv("storage.write_queue_size", "BRAIN_WRITE_QUEUE_SIZE")
	_ = viper.BindEnv("storage.postgres_dsn", "BRAIN_POSTGRES_DSN")
	_ = viper.BindEnv("storage.postgres_schema_prefix", "BRAIN_POSTGRES_SCHEMA_PREFIX")
	_ = viper.BindEnv("storage.duckdb_http_url", "BRAIN_STORAGE_DUCKDB_HTTP_URL")

	// Embedding env
	_ = viper.BindEnv("embedding.provider", "BRAIN_EMBEDDING_PROVIDER")
	_ = viper.BindEnv("embedding.model", "BRAIN_EMBEDDING_MODEL")
	_ = viper.BindEnv("embedding.dimensions", "BRAIN_EMBEDDING_DIMENSIONS")
	_ = viper.BindEnv("embedding.batch_size", "BRAIN_EMBEDDING_BATCH_SIZE")
	_ = viper.BindEnv("embedding.ollama_url", "BRAIN_OLLAMA_URL")
	_ = viper.BindEnv("embedding.openai_base_url", "BRAIN_OPENAI_BASE_URL")
	_ = viper.BindEnv("embedding.openai_api_key", "BRAIN_OPENAI_API_KEY")
	_ = viper.BindEnv("embedding.cohere_api_key", "BRAIN_COHERE_API_KEY")

	// Indexing env
	_ = viper.BindEnv("indexing.workers", "BRAIN_INDEX_WORKERS")
	_ = viper.BindEnv("indexing.chunk_max_tokens", "BRAIN_INDEX_CHUNK_MAX_TOKENS")
	_ = viper.BindEnv("indexing.chunk_overlap_tokens", "BRAIN_INDEX_CHUNK_OVERLAP_TOKENS")
	_ = viper.BindEnv("indexing.max_document_bytes", "BRAIN_INDEX_MAX_DOCUMENT_BYTES")
	_ = viper.BindEnv("indexing.enrichment_enabled", "BRAIN_ENRICHMENT_ENABLED")
	_ = viper.BindEnv("indexing.enrichment_provider", "BRAIN_ENRICHMENT_PROVIDER")
	_ = viper.BindEnv("indexing.enrichment_model", "BRAIN_ENRICHMENT_MODEL")

	// Search env
	_ = viper.BindEnv("search.default_max_results", "BRAIN_SEARCH_DEFAULT_MAX_RESULTS")
	_ = viper.BindEnv("search.max_results_limit", "BRAIN_SEARCH_MAX_RESULTS_LIMIT")
	_ = viper.BindEnv("search.timeout_ms", "BRAIN_SEARCH_TIMEOUT_MS")
	_ = viper.BindEnv("search.hybrid_default_alpha", "BRAIN_SEARCH_HYBRID_DEFAULT_ALPHA")
	_ = viper.BindEnv("search.rrf_k", "BRAIN_SEARCH_RRF_K")

	// Buffer env
	_ = viper.BindEnv("buffer.enabled", "BUFFER_ENABLED")
	_ = viper.BindEnv("buffer.service_url", "BUFFER_SERVICE_URL")
	_ = viper.BindEnv("buffer.token", "BUFFER_TOKEN")
	_ = viper.BindEnv("buffer.namespace", "BUFFER_NAMESPACE")
	_ = viper.BindEnv("buffer.soft_threshold", "BUFFER_SOFT_THRESHOLD")
	_ = viper.BindEnv("buffer.hard_threshold", "BUFFER_HARD_THRESHOLD")
	_ = viper.BindEnv("buffer.default_ttl", "BUFFER_DEFAULT_TTL")

	// Logging env
	_ = viper.BindEnv("logging.level", "BRAIN_LOGGING_LEVEL")
	_ = viper.BindEnv("logging.enable_file", "BRAIN_LOGGING_ENABLE_FILE")
	_ = viper.BindEnv("logging.log_dir", "BRAIN_LOGGING_LOG_DIR")

	// Defaults
	viper.SetDefault("server.address", ":8085")
	viper.SetDefault("server.base_path", "/mcp/brain")
	viper.SetDefault("server.keep_alive", true)
	viper.SetDefault("server.keep_alive_interval", 15)

	viper.SetDefault("storage.backend", "duckdb")
	viper.SetDefault("storage.base_dir", "./data")
	viper.SetDefault("storage.tenants_dir_name", "tenants")
	viper.SetDefault("storage.db_filename", "brain.duckdb")
	viper.SetDefault("storage.default_tenant_id", "default")
	viper.SetDefault("storage.read_max_open_conns", 8)
	viper.SetDefault("storage.read_max_idle_conns", 4)
	viper.SetDefault("storage.write_queue_size", 128)
	viper.SetDefault("storage.postgres_schema_prefix", "brain_")
	viper.SetDefault("storage.duckdb_http_url", "http://127.0.0.1:9801")

	viper.SetDefault("embedding.provider", "ollama")
	viper.SetDefault("embedding.model", "nomic-embed-text")
	viper.SetDefault("embedding.dimensions", 768)
	viper.SetDefault("embedding.batch_size", 32)
	viper.SetDefault("embedding.ollama_url", "http://localhost:11434")

	viper.SetDefault("indexing.workers", 2)
	viper.SetDefault("indexing.chunk_max_tokens", 1000)
	viper.SetDefault("indexing.chunk_overlap_tokens", 100)
	viper.SetDefault("indexing.max_document_bytes", 10*1024*1024)
	viper.SetDefault("indexing.enrichment_enabled", false)

	viper.SetDefault("search.default_max_results", 20)
	viper.SetDefault("search.max_results_limit", 100)
	viper.SetDefault("search.timeout_ms", 5000)
	viper.SetDefault("search.hybrid_default_alpha", 0.6)
	viper.SetDefault("search.rrf_k", 60)

	viper.SetDefault("buffer.namespace", "brain")
	viper.SetDefault("buffer.soft_threshold", 512*1024)
	viper.SetDefault("buffer.hard_threshold", 2*1024*1024)
	viper.SetDefault("buffer.default_ttl", 600)

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
