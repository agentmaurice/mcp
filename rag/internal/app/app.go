package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/logging"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/jobqueue"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/llm"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/pdfextract"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/vectorstore"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/docint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/experience"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/pipeline"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/queryint"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/reason"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/retrieve"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/transport/http"
	mcptransport "github.com/agentmaurice/mcpchatui/mcp/rag/internal/transport/mcp"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

// App coordinates the application lifecycle
type App struct {
	config    *config.Config
	logger    *zap.Logger
	db        *sql.DB
	entClient *ent.Client
	managers  []shared.Manager
	ingestQ   jobqueue.IngestJobQueue
}

// New creates a new App
func New() (*App, error) {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logger with full configuration
	logger, err := logging.NewWithConfig(logging.Config{
		Level:      cfg.Logging.Level,
		EnableFile: cfg.Logging.EnableFile,
		LogDir:     cfg.Logging.LogDir,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	// Log startup info
	if cfg.Logging.EnableFile {
		logger.Info("file logging enabled",
			zap.String("log_dir", cfg.Logging.LogDir),
			zap.String("json_log", "rag-server.log"),
			zap.String("readable_log", "rag-server-readable.log"))
	}

	return &App{
		config: cfg,
		logger: logger,
	}, nil
}

// Initialize initializes all components
func (a *App) Initialize(ctx context.Context, transportMode string) error {
	a.logger.Info("initializing application")
	transportMode = normalizeTransportMode(transportMode)
	a.logger.Info("mcp transport configured", zap.String("transport_mode", transportMode))

	role := normalizeRole(a.config.Runtime.Role)
	runAPI := role == "api" || role == "all"
	runWorker := role == "worker" || role == "all"
	a.logger.Info("runtime role configured",
		zap.String("role", role),
		zap.Bool("run_api", runAPI),
		zap.Bool("run_worker", runWorker))

	// Initialize database
	if err := a.initDatabase(ctx); err != nil {
		return fmt.Errorf("database initialization failed: %w", err)
	}

	// Initialize platform clients
	vectorStore, llmClient, err := a.initPlatformClients()
	if err != nil {
		return fmt.Errorf("platform clients initialization failed: %w", err)
	}

	// Initialize cache
	queryCache, err := a.initCache()
	if err != nil {
		return fmt.Errorf("cache initialization failed: %w", err)
	}

	// Initialize async ingest queue (optional)
	ingestQueue, err := a.initIngestQueue()
	if err != nil {
		return fmt.Errorf("queue initialization failed: %w", err)
	}
	a.ingestQ = ingestQueue

	// Initialize repositories
	repos := a.initRepositories()

	// Initialize RAG pipeline
	ragPipeline, quantizer, binaryIndex := a.initRAGPipeline(vectorStore, repos.Chunk, repos.Interact, llmClient, queryCache)

	// Initialize business layer
	ragManager, ingestManager, ingestWorker, contentInspector := a.initBusinessLayer(
		repos,
		ragPipeline,
		vectorStore,
		llmClient,
		queryCache,
		quantizer,
		binaryIndex,
		ingestQueue,
	)

	var mcpServer *mcptransport.Server
	var httpServer *http.Server
	if runAPI {
		// Initialize transport layer
		mcpServer, httpServer, err = a.initTransportLayer(ragManager, ingestManager, repos, vectorStore, llmClient, contentInspector, queryCache, ingestQueue, transportMode)
		if err != nil {
			return fmt.Errorf("transport layer initialization failed: %w", err)
		}
	}

	// Register managers
	var managers []shared.Manager
	if runAPI {
		managers = append(managers, ragManager, ingestManager, mcpServer)
		if supportsSTDIOTransport(transportMode) {
			managers = append(managers, mcptransport.NewStdioManager(mcpServer, a.logger))
		}
		if supportsHTTPTransport(transportMode) && httpServer != nil {
			managers = append(managers, httpServer)
		}
	}
	if runWorker {
		managers = append(managers, ingestWorker)
	}
	a.managers = managers

	a.logger.Info("application initialized", zap.Int("managers", len(a.managers)))

	return nil
}

// Start starts all managers
func (a *App) Start(ctx context.Context) error {
	a.logger.Info("starting application")

	for _, manager := range a.managers {
		a.logger.Info("starting manager", zap.String("name", manager.Name()))
		if err := manager.Start(ctx); err != nil {
			return fmt.Errorf("failed to start %s: %w", manager.Name(), err)
		}
	}

	a.logger.Info("application started successfully")
	a.logger.Info("========================================")
	a.logger.Info("RAG MCP SERVER BUILD 2026-03-05-metadata-filtering")
	a.logger.Info("Features: Phase1 post-filter + Phase2 native Qdrant filter by offre_id")
	a.logger.Info("========================================")

	return nil
}

// Stop stops all managers
func (a *App) Stop() error {
	a.logger.Info("stopping application")

	// Stop managers in reverse order
	for i := len(a.managers) - 1; i >= 0; i-- {
		manager := a.managers[i]
		a.logger.Info("stopping manager", zap.String("name", manager.Name()))
		if err := manager.Stop(); err != nil {
			a.logger.Error("failed to stop manager",
				zap.String("name", manager.Name()),
				zap.Error(err))
		}
	}

	// Close database
	if a.entClient != nil {
		if err := a.entClient.Close(); err != nil {
			a.logger.Error("failed to close database", zap.Error(err))
		}
	}
	if a.ingestQ != nil {
		if err := a.ingestQ.Close(); err != nil {
			a.logger.Error("failed to close ingest queue", zap.Error(err))
		}
	}

	a.logger.Info("application stopped")

	return nil
}

// Run runs the application until shutdown signal
func (a *App) Run(ctx context.Context, transportMode string) error {
	// Initialize
	if err := a.Initialize(ctx, transportMode); err != nil {
		return err
	}

	// Start
	if err := a.Start(ctx); err != nil {
		return err
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		a.logger.Info("received shutdown signal", zap.String("signal", sig.String()))
	case <-ctx.Done():
		a.logger.Info("context cancelled")
	}

	// Stop
	return a.Stop()
}

// initDatabase initializes the database connection
func (a *App) initDatabase(ctx context.Context) error {
	a.logger.Info("initializing database", zap.String("dsn", maskDSN(a.config.Database.DSN)))

	// Open database connection
	db, err := sql.Open("postgres", a.config.Database.DSN)
	if err != nil {
		return err
	}

	// Create Ent driver and client
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))

	// Run migrations if enabled
	if a.config.Database.AutoMigrate {
		a.logger.Info("running database migrations")
		ensureTenantIndexes(ctx, db, a.logger)
		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("failed to run migrations: %w", err)
		}
	}

	a.db = db
	a.entClient = client

	return nil
}

// initPlatformClients initializes platform clients
func (a *App) initPlatformClients() (shared.VectorStore, shared.LLMClient, error) {
	a.logger.Info("initializing platform clients")

	// Initialize LLM client
	llmClient, err := llm.NewClient(a.config.LLM, a.logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create LLM client: %w", err)
	}

	// Compute embedding dimension: use explicit config if set, otherwise auto-detect from model name
	embeddingDim := a.config.LLM.EmbeddingDim
	if embeddingDim == 0 {
		embeddingDim = determineEmbeddingDim(a.config.LLM.EmbeddingModel)
	}
	a.logger.Info("embedding dimension configured", zap.Int("dim", embeddingDim), zap.String("model", a.config.LLM.EmbeddingModel))

	// Initialize vector store
	vectorStore, err := vectorstore.NewQdrantClient(
		a.config.VectorStore.URL,
		a.config.VectorStore.APIKey,
		a.config.VectorStore.Collection,
		embeddingDim,
		a.config.VectorStore.DestructiveMigration,
		a.logger,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	return vectorStore, llmClient, nil
}

// initRepositories initializes repositories
func (a *App) initRepositories() *Repositories {
	a.logger.Info("initializing repositories")

	return &Repositories{
		Tenant:     repository.NewTenantRepository(a.entClient),
		Deployment: repository.NewDeploymentRepository(a.entClient),
		Document:   repository.NewDocumentRepository(a.entClient),
		Chunk:      repository.NewChunkRepository(a.entClient),
		IngestJob:  repository.NewIngestJobRepository(a.entClient),
		Interact:   repository.NewInteractionRepository(a.entClient),
	}
}

// initCache initializes the query cache
func (a *App) initCache() (cache.QueryCache, error) {
	a.logger.Info("initializing cache",
		zap.Bool("enabled", a.config.Cache.Enabled),
		zap.String("store_type", a.config.Cache.StoreType))

	queryCache, err := cache.NewManager(&a.config.Cache, a.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache manager: %w", err)
	}

	return queryCache, nil
}

// initRAGPipeline initializes the RAG pipeline
func (a *App) initRAGPipeline(
	vectorStore shared.VectorStore,
	chunkRepo *repository.ChunkRepository,
	interactionRepo *repository.InteractionRepository,
	llmClient shared.LLMClient,
	queryCache cache.QueryCache,
) (*pipeline.Pipeline, retrieve.EmbeddingQuantizer, retrieve.BinaryIndex) {
	a.logger.Info("initializing RAG pipeline")

	// Layer 0: Document Intelligence
	docAnalyzer := docint.NewAnalyzer(a.logger)

	// Layer 1: Query Intelligence
	queryAnalyzer := queryint.NewAnalyzer(a.logger)

	// Layer 2: Retrieval
	floatRetriever := retrieve.NewFloatRetriever(vectorStore, a.logger)
	vectorRetriever := retrieve.VectorRetriever(floatRetriever)
	var quantizer retrieve.EmbeddingQuantizer
	var binaryIndex retrieve.BinaryIndex

	if a.config.Retrieval.Mode == "binary_int8" {
		quantizer = retrieve.NewLinearQuantizer()
		binaryIndex = retrieve.NewInMemoryBinaryIndex(retrieve.NewDBBinaryEmbeddingLoader(chunkRepo), a.logger)
		int8Store := retrieve.NewDBInt8EmbeddingStore(chunkRepo)
		binaryRetriever := retrieve.NewBinaryInt8Retriever(
			quantizer,
			binaryIndex,
			int8Store,
			chunkRepo,
			a.config.Retrieval.BinaryCandidates,
			a.logger,
		)
		vectorRetriever = retrieve.NewFallbackRetriever(binaryRetriever, floatRetriever, a.config.Retrieval.EnableFloatFallback, a.logger)
		a.logger.Info("binary int8 retrieval enabled",
			zap.Int("binary_candidates", a.config.Retrieval.BinaryCandidates),
			zap.Bool("float_fallback", a.config.Retrieval.EnableFloatFallback))
		return pipeline.NewPipeline(
			docAnalyzer,
			queryAnalyzer,
			retrieve.NewRetriever(vectorRetriever, chunkRepo, llmClient, queryCache, a.logger),
			reason.NewAnswerer(llmClient, queryCache, a.logger),
			experience.NewLogger(interactionRepo, a.logger),
			a.logger,
		), quantizer, binaryIndex
	}

	retriever := retrieve.NewRetriever(vectorRetriever, chunkRepo, llmClient, queryCache, a.logger)

	// Layer 3: Reasoning
	answerer := reason.NewAnswerer(llmClient, queryCache, a.logger)

	// Layer 4: Experience
	expLogger := experience.NewLogger(interactionRepo, a.logger)

	// Pipeline orchestrator
	return pipeline.NewPipeline(
		docAnalyzer,
		queryAnalyzer,
		retriever,
		answerer,
		expLogger,
		a.logger,
	), quantizer, binaryIndex
}

// initBusinessLayer initializes the business layer
func (a *App) initBusinessLayer(
	repos *Repositories,
	pipeline *pipeline.Pipeline,
	vectorStore shared.VectorStore,
	llmClient shared.LLMClient,
	queryCache cache.QueryCache,
	quantizer retrieve.EmbeddingQuantizer,
	binaryIndex retrieve.BinaryIndex,
	ingestQueue jobqueue.IngestJobQueue,
) (*business.RAGManager, *business.IngestManager, *business.IngestWorker, inspect.ContentInspector) {
	a.logger.Info("initializing business layer")

	ragManager := business.NewRAGManager(pipeline, repos.Tenant, a.logger)

	ingestManager := business.NewIngestManager(repos.IngestJob, repos.Tenant, repos.Deployment, repos.Document, ingestQueue, a.logger)

	contentInspector := inspect.NewInspector(llmClient, a.logger)
	pollInterval := 5 * time.Second
	if ingestQueue != nil {
		// Keep a slow polling fallback to recover jobs if a queue message is missed.
		pollInterval = 30 * time.Second
	}

	// Initialize PDF extractor if enabled
	var pdfExt *pdfextract.Extractor
	if a.config.PDF.Enabled {
		if pdfextract.Available() {
			pdfExt = pdfextract.NewExtractor(pdfextract.Config{
				Enabled:        a.config.PDF.Enabled,
				MaxFileSize:    a.config.PDF.MaxFileSize,
				TimeoutSeconds: a.config.PDF.TimeoutSeconds,
				PreserveLayout: a.config.PDF.PreserveLayout,
			}, a.logger)
			a.logger.Info("PDF extraction enabled (pdftotext available)",
				zap.Int64("max_file_size", a.config.PDF.MaxFileSize),
				zap.Int("timeout_seconds", a.config.PDF.TimeoutSeconds),
				zap.Bool("preserve_layout", a.config.PDF.PreserveLayout))
		} else {
			a.logger.Warn("PDF extraction enabled in config but pdftotext is not available in PATH; PDF ingestion will fail")
		}
	} else {
		a.logger.Info("PDF extraction disabled")
	}

	ingestWorker := business.NewIngestWorker(
		repos.IngestJob,
		repos.Document,
		repos.Chunk,
		pipeline,
		vectorStore,
		llmClient,
		repos.Tenant,
		contentInspector,
		queryCache,
		quantizer,
		binaryIndex,
		pdfExt,
		ingestQueue,
		pollInterval,
		a.logger,
	)
	ingestWorker.SetThresholds(
		a.config.Detection.SemanticThreshold,
		a.config.Detection.CVThreshold,
		a.config.Detection.IdentifiabilityWarn,
		a.config.Detection.IdentifiabilityBlock,
	)

	return ragManager, ingestManager, ingestWorker, contentInspector
}

func (a *App) initIngestQueue() (jobqueue.IngestJobQueue, error) {
	if !a.config.Queue.Enabled {
		a.logger.Info("async ingest queue disabled")
		return nil, nil
	}

	queueType := strings.ToLower(strings.TrimSpace(a.config.Queue.Type))
	switch queueType {
	case "nats":
		a.logger.Info("initializing NATS ingest queue",
			zap.String("url", a.config.Queue.NATS.URL),
			zap.String("subject", a.config.Queue.NATS.Subject),
			zap.String("queue_group", a.config.Queue.NATS.QueueGroup),
		)
		return jobqueue.NewNATSQueue(jobqueue.NATSConfig{
			URL:        a.config.Queue.NATS.URL,
			Subject:    a.config.Queue.NATS.Subject,
			QueueGroup: a.config.Queue.NATS.QueueGroup,
		}, a.logger)
	case "nats_jetstream", "jetstream":
		a.logger.Info("initializing NATS JetStream ingest queue",
			zap.String("url", a.config.Queue.NATS.URL),
			zap.String("subject", a.config.Queue.NATS.Subject),
			zap.String("queue_group", a.config.Queue.NATS.QueueGroup),
			zap.String("stream", a.config.Queue.NATS.Stream),
			zap.String("durable", a.config.Queue.NATS.Durable),
			zap.Bool("create_stream", a.config.Queue.NATS.CreateStream),
			zap.Int("ack_wait_seconds", a.config.Queue.NATS.AckWaitSeconds),
			zap.Int("max_deliver", a.config.Queue.NATS.MaxDeliver),
			zap.Bool("dlq_enabled", a.config.Queue.NATS.DLQEnabled),
			zap.String("dlq_subject", a.config.Queue.NATS.DLQSubject),
			zap.String("dlq_stream", a.config.Queue.NATS.DLQStream),
			zap.Bool("dlq_create_stream", a.config.Queue.NATS.DLQCreateStream),
			zap.String("dlq_advisory_group", a.config.Queue.NATS.DLQAdvisoryGroup),
		)
		return jobqueue.NewNATSJetStreamQueue(jobqueue.NATSConfig{
			URL:              a.config.Queue.NATS.URL,
			Subject:          a.config.Queue.NATS.Subject,
			QueueGroup:       a.config.Queue.NATS.QueueGroup,
			Stream:           a.config.Queue.NATS.Stream,
			Durable:          a.config.Queue.NATS.Durable,
			CreateStream:     a.config.Queue.NATS.CreateStream,
			AckWaitSeconds:   a.config.Queue.NATS.AckWaitSeconds,
			MaxDeliver:       a.config.Queue.NATS.MaxDeliver,
			DLQEnabled:       a.config.Queue.NATS.DLQEnabled,
			DLQSubject:       a.config.Queue.NATS.DLQSubject,
			DLQStream:        a.config.Queue.NATS.DLQStream,
			DLQCreateStream:  a.config.Queue.NATS.DLQCreateStream,
			DLQAdvisoryGroup: a.config.Queue.NATS.DLQAdvisoryGroup,
		}, a.logger)
	default:
		return nil, fmt.Errorf("unsupported queue type: %s", a.config.Queue.Type)
	}
}

// initTransportLayer initializes the transport layer
func (a *App) initTransportLayer(
	ragManager *business.RAGManager,
	ingestManager *business.IngestManager,
	repos *Repositories,
	vectorStore shared.VectorStore,
	llmClient shared.LLMClient,
	inspector inspect.ContentInspector,
	queryCache cache.QueryCache,
	ingestQueue jobqueue.IngestJobQueue,
	transportMode string,
) (*mcptransport.Server, *http.Server, error) {
	a.logger.Info("initializing transport layer")

	// Initialize buffer client for doc_ref resolution and optional response buffering.
	var bufferClient *buffer.Client
	bufferConfigured := strings.TrimSpace(a.config.Buffer.ServiceURL) != "" && strings.TrimSpace(a.config.Buffer.Token) != ""
	if bufferConfigured {
		a.logger.Info("initializing buffer client",
			zap.String("service_url", a.config.Buffer.ServiceURL),
			zap.String("namespace", a.config.Buffer.Namespace),
			zap.Int64("soft_threshold", a.config.Buffer.SoftThreshold),
			zap.Int64("hard_threshold", a.config.Buffer.HardThreshold),
		)
		bufferClient = buffer.NewClient(buffer.ClientConfig{
			ServiceURL:    a.config.Buffer.ServiceURL,
			Token:         a.config.Buffer.Token,
			Namespace:     a.config.Buffer.Namespace,
			SoftThreshold: a.config.Buffer.SoftThreshold,
			HardThreshold: a.config.Buffer.HardThreshold,
			DefaultTTL:    a.config.Buffer.DefaultTTL,
			Logger:        a.logger,
		})
	} else {
		a.logger.Info("buffer client not configured for doc_ref resolution",
			zap.Bool("has_service_url", strings.TrimSpace(a.config.Buffer.ServiceURL) != ""),
			zap.Bool("has_token", strings.TrimSpace(a.config.Buffer.Token) != ""),
		)
	}

	// Initialize response wrapper if enabled
	var responseWrapper *mcptransport.ResponseWrapper
	if a.config.Buffer.Enabled && bufferClient != nil {
		responseWrapper = mcptransport.NewResponseWrapper(bufferClient, a.logger)
		a.logger.Info("buffer service enabled for large payload handling")
	} else if a.config.Buffer.Enabled && bufferClient == nil {
		a.logger.Warn("buffer response wrapping enabled but buffer client is not configured; disabling response buffering")
		responseWrapper = mcptransport.NewResponseWrapper(nil, a.logger)
	} else {
		a.logger.Info("buffer service disabled - large payloads will be returned directly")
		responseWrapper = mcptransport.NewResponseWrapper(nil, a.logger)
	}

	// MCP server with SSE transport
	mcpServer := mcptransport.NewServer(
		ragManager,
		ingestManager,
		repos.Tenant,
		repos.Document,
		repos.Chunk,
		repos.IngestJob,
		repos.Deployment,
		vectorStore,
		llmClient,
		bufferClient,
		inspector,
		responseWrapper,
		queryCache,
		a.config.Server,
		a.logger,
	)
	if err := mcpServer.Build(); err != nil {
		return nil, nil, fmt.Errorf("failed to build MCP server: %w", err)
	}

	// HTTP server using MCP's native SSE handlers
	var httpServer *http.Server
	if supportsHTTPTransport(transportMode) {
		var dlqReplayer jobqueue.DLQReplayer
		var dlqInspector jobqueue.DLQInspector
		if ingestQueue != nil {
			if replayer, ok := ingestQueue.(jobqueue.DLQReplayer); ok {
				dlqReplayer = replayer
			}
			if inspector, ok := ingestQueue.(jobqueue.DLQInspector); ok {
				dlqInspector = inspector
			}
		}
		httpServer = http.NewServer(mcpServer, ragManager, ingestManager, repos.Tenant, repos.Deployment, dlqReplayer, dlqInspector, a.db, a.config.Server, a.logger)
		if err := httpServer.Build(); err != nil {
			return nil, nil, fmt.Errorf("failed to build HTTP server: %w", err)
		}
	}

	return mcpServer, httpServer, nil
}

// Repositories holds all repositories
type Repositories struct {
	Tenant     *repository.TenantRepository
	Deployment *repository.DeploymentRepository
	Document   *repository.DocumentRepository
	Chunk      *repository.ChunkRepository
	IngestJob  *repository.IngestJobRepository
	Interact   *repository.InteractionRepository
}

// maskDSN masks sensitive parts of DSN
func maskDSN(dsn string) string {
	// Regex to find password in Postgres DSN
	// postgres://user:password@host:port/dbname
	re := regexp.MustCompile(`(postgres://[^:]+):([^@]+)(@.*)`)
	return re.ReplaceAllString(dsn, "${1}:*****${3}")
}

// determineEmbeddingDim returns the expected vector dimension for a given embedding model.
// If the model is not recognized, it returns 1536 as a common default.
// For unsupported models, use LLM_EMBEDDING_DIM environment variable to override.
func determineEmbeddingDim(model string) int {
	switch model {
	// OpenAI models
	case "text-embedding-3-small":
		return 1536
	case "text-embedding-3-large":
		return 3072
	case "text-embedding-ada-002", "", "text-embedding-ada-002-v2":
		return 1536

	// Mistral models
	case "mistral-embed":
		return 1024

	// Nomic models
	case "nomic-embed-text", "nomic-embed-text-v1", "nomic-embed-text-v1.5":
		return 768

	// Cohere models
	case "embed-english-v3.0", "embed-multilingual-v3.0":
		return 1024
	case "embed-english-light-v3.0", "embed-multilingual-light-v3.0":
		return 384

	// Ollama common models
	case "mxbai-embed-large":
		return 1024
	case "all-minilm", "all-minilm:l6-v2":
		return 384
	case "snowflake-arctic-embed":
		return 1024

	// Voyage AI models
	case "voyage-large-2", "voyage-code-2":
		return 1536
	case "voyage-2":
		return 1024

	default:
		// Fallback - users should set LLM_EMBEDDING_DIM for unsupported models
		return 1536
	}
}

func normalizeRole(role string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	switch r {
	case "api", "worker", "all":
		return r
	default:
		return "all"
	}
}

func normalizeTransportMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "sse", "stdio", "both":
		return normalized
	default:
		return "sse"
	}
}

func supportsHTTPTransport(mode string) bool {
	return mode == "sse" || mode == "both"
}

func supportsSTDIOTransport(mode string) bool {
	return mode == "stdio" || mode == "both"
}

func ensureTenantIndexes(ctx context.Context, db *sql.DB, logger *zap.Logger) {
	stmts := []string{
		"ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenant_deployment_id_is_default",
		"DROP INDEX IF EXISTS tenant_deployment_id_is_default",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			logger.Debug("tenant schema compat statement failed", zap.String("stmt", stmt), zap.Error(err))
		}
	}
}
