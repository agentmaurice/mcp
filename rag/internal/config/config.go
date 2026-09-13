package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config represents the application configuration
type Config struct {
	Server      ServerConfig      `mapstructure:"server"`
	Database    DatabaseConfig    `mapstructure:"database"`
	VectorStore VectorStoreConfig `mapstructure:"vectorstore"`
	LLM         LLMConfig         `mapstructure:"llm"`
	Logging     LoggingConfig     `mapstructure:"logging"`
	Detection   DetectionConfig   `mapstructure:"detection"`
	Buffer      BufferConfig      `mapstructure:"buffer"`
	Retrieval   RetrievalConfig   `mapstructure:"retrieval"`
	Cache       CacheConfig       `mapstructure:"cache"`
	Runtime     RuntimeConfig     `mapstructure:"runtime"`
	Queue       QueueConfig       `mapstructure:"queue"`
	PDF         PDFConfig         `mapstructure:"pdf"`
}

// BufferConfig represents buffer service configuration for large payload handling
type BufferConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	ServiceURL    string `mapstructure:"service_url"`    // URL of the buffer service (e.g., http://agentmaurice:3000/api/v1/buffer)
	Token         string `mapstructure:"token"`          // MCP Buffer authentication token
	Namespace     string `mapstructure:"namespace"`      // Default namespace for buffered content (default: "rag")
	SoftThreshold int64  `mapstructure:"soft_threshold"` // Size in bytes above which buffering is recommended (default: 512KB)
	HardThreshold int64  `mapstructure:"hard_threshold"` // Size in bytes above which buffering is mandatory (default: 2MB)
	DefaultTTL    int    `mapstructure:"default_ttl"`    // TTL in seconds for buffered content (default: 600)
}

// RetrievalConfig represents retrieval configuration.
type RetrievalConfig struct {
	Mode                string `mapstructure:"mode"`                  // "float" or "binary_int8"
	BinaryCandidates    int    `mapstructure:"binary_candidates"`     // Candidate pool size for binary retrieval
	EnableFloatFallback bool   `mapstructure:"enable_float_fallback"` // Fallback to float retriever on error/empty
}

// CacheConfig represents query caching configuration
type CacheConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	StoreType string `mapstructure:"store_type"` // "memory", "redis", or "chain"

	// Redis configuration (used when store_type is "redis" or "chain")
	Redis RedisCacheConfig `mapstructure:"redis"`

	// Per-tier configuration
	Embedding CacheTierConfig `mapstructure:"embedding"`
	Search    CacheTierConfig `mapstructure:"search"`
	Answer    CacheTierConfig `mapstructure:"answer"`
}

// RedisCacheConfig represents Redis connection settings for cache
type RedisCacheConfig struct {
	Address  string `mapstructure:"address"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// CacheTierConfig represents configuration for a single cache tier
type CacheTierConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	TTLSeconds int  `mapstructure:"ttl_seconds"` // TTL in seconds
	MaxSize    int  `mapstructure:"max_size"`    // Maximum number of entries
	SlidingTTL bool `mapstructure:"sliding_ttl"` // If true, TTL is reset on each cache hit
}

// RuntimeConfig represents runtime role configuration.
type RuntimeConfig struct {
	Role string `mapstructure:"role"` // "api", "worker", or "all"
}

// QueueConfig represents async ingest queue configuration.
type QueueConfig struct {
	Enabled bool       `mapstructure:"enabled"`
	Type    string     `mapstructure:"type"` // "nats" or "nats_jetstream"
	NATS    NATSConfig `mapstructure:"nats"`
}

// NATSConfig represents NATS queue configuration.
type NATSConfig struct {
	URL              string `mapstructure:"url"`
	Subject          string `mapstructure:"subject"`
	QueueGroup       string `mapstructure:"queue_group"`
	Stream           string `mapstructure:"stream"`
	Durable          string `mapstructure:"durable"`
	CreateStream     bool   `mapstructure:"create_stream"`
	AckWaitSeconds   int    `mapstructure:"ack_wait_seconds"`
	MaxDeliver       int    `mapstructure:"max_deliver"`
	DLQEnabled       bool   `mapstructure:"dlq_enabled"`
	DLQSubject       string `mapstructure:"dlq_subject"`
	DLQStream        string `mapstructure:"dlq_stream"`
	DLQCreateStream  bool   `mapstructure:"dlq_create_stream"`
	DLQAdvisoryGroup string `mapstructure:"dlq_advisory_group"`
}

// ServerConfig represents server configuration
type ServerConfig struct {
	Address           string `mapstructure:"address"`
	BasePath          string `mapstructure:"base_path"`
	KeepAlive         bool   `mapstructure:"keep_alive"`
	KeepAliveInterval int    `mapstructure:"keep_alive_interval"` // in seconds
}

// DatabaseConfig represents database configuration
type DatabaseConfig struct {
	DSN         string `mapstructure:"dsn"`
	AutoMigrate bool   `mapstructure:"auto_migrate"`
}

// VectorStoreConfig represents vector store configuration
type VectorStoreConfig struct {
	URL                  string `mapstructure:"url"`
	APIKey               string `mapstructure:"api_key"`
	Collection           string `mapstructure:"collection"`
	DestructiveMigration bool   `mapstructure:"destructive_migration"`
}

// LLMConfig represents LLM provider configuration
type LLMConfig struct {
	Provider                  string `mapstructure:"provider"` // openai, anthropic, etc.
	APIKey                    string `mapstructure:"api_key"`
	BaseURL                   string `mapstructure:"base_url"`
	Model                     string `mapstructure:"model"`
	EmbeddingModel            string `mapstructure:"embedding_model"`
	EmbeddingDim              int    `mapstructure:"embedding_dim"` // override embedding dimension (0 = auto-detect)
	EmbeddingRetryMaxAttempts int    `mapstructure:"embedding_retry_max_attempts"`
	EmbeddingRetryBaseDelayMS int    `mapstructure:"embedding_retry_base_delay_ms"`
	EmbeddingRetryMaxDelayMS  int    `mapstructure:"embedding_retry_max_delay_ms"`
}

// LoggingConfig represents logging configuration
type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	EnableFile bool   `mapstructure:"enable_file"`
	LogDir     string `mapstructure:"log_dir"`
}

// PDFConfig holds configuration for PDF text extraction via pdftotext.
type PDFConfig struct {
	Enabled        bool  `mapstructure:"enabled"`          // Enable PDF extraction (default: true)
	MaxFileSize    int64 `mapstructure:"max_file_size"`    // Maximum PDF file size in bytes (default: 50MB)
	TimeoutSeconds int   `mapstructure:"timeout_seconds"`  // Timeout for pdftotext execution (default: 120s)
	PreserveLayout bool  `mapstructure:"preserve_layout"`  // Preserve original text layout (default: true)
}

// DetectionConfig holds thresholds/policies for content inspection and duplicates
type DetectionConfig struct {
	SemanticThreshold    float64 `mapstructure:"semantic_threshold"`
	CVThreshold          float64 `mapstructure:"cv_threshold"`
	IdentifiabilityWarn  float64 `mapstructure:"identifiability_warn"`
	IdentifiabilityBlock float64 `mapstructure:"identifiability_block"`
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("json")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")

	// Explicitly bind environment variables
	// This is more reliable than AutomaticEnv for nested structures
	viper.BindEnv("server.address", "SERVER_ADDRESS")
	viper.BindEnv("server.base_path", "SERVER_BASE_PATH")
	viper.BindEnv("server.keep_alive", "SERVER_KEEP_ALIVE")
	viper.BindEnv("server.keep_alive_interval", "SERVER_KEEP_ALIVE_INTERVAL")
	viper.BindEnv("database.dsn", "DATABASE_DSN")
	viper.BindEnv("database.auto_migrate", "DATABASE_AUTO_MIGRATE")
	viper.BindEnv("vectorstore.url", "VECTORSTORE_URL")
	viper.BindEnv("vectorstore.api_key", "VECTORSTORE_API_KEY")
	viper.BindEnv("vectorstore.collection", "VECTORSTORE_COLLECTION")
	viper.BindEnv("vectorstore.destructive_migration", "VECTORSTORE_DESTRUCTIVE_MIGRATION")
	viper.BindEnv("llm.provider", "LLM_PROVIDER")
	viper.BindEnv("llm.api_key", "LLM_API_KEY")
	viper.BindEnv("llm.base_url", "LLM_BASE_URL")
	viper.BindEnv("llm.model", "LLM_MODEL")
	viper.BindEnv("llm.embedding_model", "LLM_EMBEDDING_MODEL")
	viper.BindEnv("llm.embedding_dim", "LLM_EMBEDDING_DIM")
	viper.BindEnv("llm.embedding_retry_max_attempts", "LLM_EMBEDDING_RETRY_MAX_ATTEMPTS")
	viper.BindEnv("llm.embedding_retry_base_delay_ms", "LLM_EMBEDDING_RETRY_BASE_DELAY_MS")
	viper.BindEnv("llm.embedding_retry_max_delay_ms", "LLM_EMBEDDING_RETRY_MAX_DELAY_MS")
	viper.BindEnv("logging.level", "LOGGING_LEVEL")
	viper.BindEnv("logging.enable_file", "LOGGING_ENABLE_FILE")
	viper.BindEnv("logging.log_dir", "LOGGING_LOG_DIR")
	viper.BindEnv("detection.semantic_threshold", "DETECTION_SEMANTIC_THRESHOLD")
	viper.BindEnv("detection.cv_threshold", "DETECTION_CV_THRESHOLD")
	viper.BindEnv("detection.identifiability_warn", "DETECTION_IDENT_WARN")
	viper.BindEnv("detection.identifiability_block", "DETECTION_IDENT_BLOCK")

	// PDF extraction configuration
	viper.BindEnv("pdf.enabled", "PDF_ENABLED")
	viper.BindEnv("pdf.max_file_size", "PDF_MAX_FILE_SIZE")
	viper.BindEnv("pdf.timeout_seconds", "PDF_TIMEOUT_SECONDS")
	viper.BindEnv("pdf.preserve_layout", "PDF_PRESERVE_LAYOUT")

	// Buffer configuration
	viper.BindEnv("buffer.enabled", "BUFFER_ENABLED")
	viper.BindEnv("buffer.service_url", "BUFFER_SERVICE_URL")
	viper.BindEnv("buffer.token", "BUFFER_TOKEN")
	viper.BindEnv("buffer.namespace", "BUFFER_NAMESPACE")
	viper.BindEnv("buffer.soft_threshold", "BUFFER_SOFT_THRESHOLD")
	viper.BindEnv("buffer.hard_threshold", "BUFFER_HARD_THRESHOLD")
	viper.BindEnv("buffer.default_ttl", "BUFFER_DEFAULT_TTL")

	// Set defaults for buffer
	viper.SetDefault("buffer.namespace", "rag")
	viper.SetDefault("buffer.soft_threshold", 512*1024)    // 512 KB
	viper.SetDefault("buffer.hard_threshold", 2*1024*1024) // 2 MB
	viper.SetDefault("buffer.default_ttl", 600)            // 10 minutes

	// Retrieval configuration
	viper.BindEnv("retrieval.mode", "RAG_RETRIEVAL_MODE")
	viper.BindEnv("retrieval.binary_candidates", "RAG_BINARY_CANDIDATES")
	viper.BindEnv("retrieval.enable_float_fallback", "RAG_ENABLE_FLOAT_FALLBACK")

	// Set defaults for retrieval
	viper.SetDefault("retrieval.mode", "float")
	viper.SetDefault("retrieval.binary_candidates", 1000)
	viper.SetDefault("retrieval.enable_float_fallback", true)

	// Cache configuration
	viper.BindEnv("cache.enabled", "CACHE_ENABLED")
	viper.BindEnv("cache.store_type", "CACHE_STORE_TYPE")
	viper.BindEnv("cache.redis.address", "CACHE_REDIS_ADDRESS")
	viper.BindEnv("cache.redis.password", "CACHE_REDIS_PASSWORD")
	viper.BindEnv("cache.redis.db", "CACHE_REDIS_DB")
	viper.BindEnv("cache.embedding.enabled", "CACHE_EMBEDDING_ENABLED")
	viper.BindEnv("cache.embedding.ttl_seconds", "CACHE_EMBEDDING_TTL_SECONDS")
	viper.BindEnv("cache.embedding.max_size", "CACHE_EMBEDDING_MAX_SIZE")
	viper.BindEnv("cache.embedding.sliding_ttl", "CACHE_EMBEDDING_SLIDING_TTL")
	viper.BindEnv("cache.search.enabled", "CACHE_SEARCH_ENABLED")
	viper.BindEnv("cache.search.ttl_seconds", "CACHE_SEARCH_TTL_SECONDS")
	viper.BindEnv("cache.search.max_size", "CACHE_SEARCH_MAX_SIZE")
	viper.BindEnv("cache.search.sliding_ttl", "CACHE_SEARCH_SLIDING_TTL")
	viper.BindEnv("cache.answer.enabled", "CACHE_ANSWER_ENABLED")
	viper.BindEnv("cache.answer.ttl_seconds", "CACHE_ANSWER_TTL_SECONDS")
	viper.BindEnv("cache.answer.max_size", "CACHE_ANSWER_MAX_SIZE")
	viper.BindEnv("cache.answer.sliding_ttl", "CACHE_ANSWER_SLIDING_TTL")

	// Runtime configuration
	viper.BindEnv("runtime.role", "APP_ROLE")

	// Queue configuration
	viper.BindEnv("queue.enabled", "QUEUE_ENABLED")
	viper.BindEnv("queue.type", "QUEUE_TYPE")
	viper.BindEnv("queue.nats.url", "NATS_URL")
	viper.BindEnv("queue.nats.subject", "QUEUE_NATS_SUBJECT")
	viper.BindEnv("queue.nats.queue_group", "QUEUE_NATS_QUEUE_GROUP")
	viper.BindEnv("queue.nats.stream", "QUEUE_NATS_STREAM")
	viper.BindEnv("queue.nats.durable", "QUEUE_NATS_DURABLE")
	viper.BindEnv("queue.nats.create_stream", "QUEUE_NATS_CREATE_STREAM")
	viper.BindEnv("queue.nats.ack_wait_seconds", "QUEUE_NATS_ACK_WAIT_SECONDS")
	viper.BindEnv("queue.nats.max_deliver", "QUEUE_NATS_MAX_DELIVER")
	viper.BindEnv("queue.nats.dlq_enabled", "QUEUE_NATS_DLQ_ENABLED")
	viper.BindEnv("queue.nats.dlq_subject", "QUEUE_NATS_DLQ_SUBJECT")
	viper.BindEnv("queue.nats.dlq_stream", "QUEUE_NATS_DLQ_STREAM")
	viper.BindEnv("queue.nats.dlq_create_stream", "QUEUE_NATS_DLQ_CREATE_STREAM")
	viper.BindEnv("queue.nats.dlq_advisory_group", "QUEUE_NATS_DLQ_ADVISORY_GROUP")

	// Set defaults for PDF extraction
	viper.SetDefault("pdf.enabled", true)
	viper.SetDefault("pdf.max_file_size", 50*1024*1024) // 50 MB
	viper.SetDefault("pdf.timeout_seconds", 120)
	viper.SetDefault("pdf.preserve_layout", true)

	// Set defaults for cache
	viper.SetDefault("cache.enabled", false)
	viper.SetDefault("cache.store_type", "memory")
	viper.SetDefault("cache.redis.address", "localhost:6379")
	viper.SetDefault("cache.redis.db", 0)
	viper.SetDefault("cache.embedding.enabled", true)
	viper.SetDefault("cache.embedding.ttl_seconds", 86400) // 24 hours
	viper.SetDefault("cache.embedding.max_size", 1000)
	viper.SetDefault("cache.embedding.sliding_ttl", false)
	viper.SetDefault("cache.search.enabled", true)
	viper.SetDefault("cache.search.ttl_seconds", 14400) // 4 hours
	viper.SetDefault("cache.search.max_size", 500)
	viper.SetDefault("cache.search.sliding_ttl", false)
	viper.SetDefault("cache.answer.enabled", true)
	viper.SetDefault("cache.answer.ttl_seconds", 7200) // 2 hours
	viper.SetDefault("cache.answer.max_size", 200)
	viper.SetDefault("cache.answer.sliding_ttl", false)
	viper.SetDefault("llm.embedding_retry_max_attempts", 4)
	viper.SetDefault("llm.embedding_retry_base_delay_ms", 400)
	viper.SetDefault("llm.embedding_retry_max_delay_ms", 8000)

	// Set defaults for runtime
	viper.SetDefault("runtime.role", "all")

	// Set defaults for queue
	viper.SetDefault("queue.enabled", false)
	viper.SetDefault("queue.type", "nats_jetstream")
	viper.SetDefault("queue.nats.url", "nats://localhost:4222")
	viper.SetDefault("queue.nats.subject", "rag.ingest.jobs")
	viper.SetDefault("queue.nats.queue_group", "rag.ingest.workers")
	viper.SetDefault("queue.nats.stream", "RAG_INGEST_JOBS")
	viper.SetDefault("queue.nats.durable", "rag-ingest-consumer")
	viper.SetDefault("queue.nats.create_stream", true)
	viper.SetDefault("queue.nats.ack_wait_seconds", 900)
	viper.SetDefault("queue.nats.max_deliver", 10)
	viper.SetDefault("queue.nats.dlq_enabled", true)
	viper.SetDefault("queue.nats.dlq_subject", "rag.ingest.jobs.dlq")
	viper.SetDefault("queue.nats.dlq_stream", "RAG_INGEST_JOBS_DLQ")
	viper.SetDefault("queue.nats.dlq_create_stream", true)
	viper.SetDefault("queue.nats.dlq_advisory_group", "rag.ingest.dlq.republisher")

	// Try to read config file, but don't fail if not found
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
		// Config file not found, will use env vars only
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}
