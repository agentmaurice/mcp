package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/xid"
	"github.com/yosida95/uritemplate/v3"
	"go.uber.org/zap"
)

const (
	serviceName    = "rag-server"
	serviceVersion = "1.0.0"
)

// Server implements MCP server with SSE transport
type Server struct {
	mcpServer       *server.MCPServer
	sseServer       *server.SSEServer
	modernServer    *officialmcp.Server
	modernHandler   http.Handler
	stdioServer     *server.StdioServer
	ragManager      *business.RAGManager
	ingestManager   *business.IngestManager
	tenantRepo      *repository.TenantRepository
	documentRepo    *repository.DocumentRepository
	chunkRepo       *repository.ChunkRepository
	jobRepo         *repository.IngestJobRepository
	deployRepo      *repository.DeploymentRepository
	vectorStore     shared.VectorStore
	llmClient       shared.LLMClient
	bufferClient    *buffer.Client
	inspector       inspect.ContentInspector
	responseWrapper *ResponseWrapper
	queryCache      cache.QueryCache
	config          config.ServerConfig
	logger          *zap.Logger

	// Activity tracking
	lastRequestAt atomic.Value // stores time.Time
	requestCount  atomic.Int64
}

// NewServer creates a new MCP server
func NewServer(
	ragManager *business.RAGManager,
	ingestManager *business.IngestManager,
	tenantRepo *repository.TenantRepository,
	documentRepo *repository.DocumentRepository,
	chunkRepo *repository.ChunkRepository,
	jobRepo *repository.IngestJobRepository,
	deployRepo *repository.DeploymentRepository,
	vectorStore shared.VectorStore,
	llmClient shared.LLMClient,
	bufferClient *buffer.Client,
	inspector inspect.ContentInspector,
	responseWrapper *ResponseWrapper,
	queryCache cache.QueryCache,
	cfg config.ServerConfig,
	logger *zap.Logger,
) *Server {
	return &Server{
		ragManager:      ragManager,
		ingestManager:   ingestManager,
		tenantRepo:      tenantRepo,
		documentRepo:    documentRepo,
		chunkRepo:       chunkRepo,
		jobRepo:         jobRepo,
		deployRepo:      deployRepo,
		vectorStore:     vectorStore,
		llmClient:       llmClient,
		bufferClient:    bufferClient,
		inspector:       inspector,
		responseWrapper: responseWrapper,
		queryCache:      queryCache,
		config:          cfg,
		logger:          logger.Named("mcp-server"),
	}
}

// Build builds the MCP server with tools and SSE transport
func (s *Server) Build() error {
	s.logger.Info("building MCP server")

	// Create MCP server
	mcpServer := server.NewMCPServer(
		serviceName,
		serviceVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithInstructions(buildInstructions()),
	)
	modernServer := newModernMCPServer(serviceName, serviceVersion, "")
	s.mcpServer = mcpServer
	s.modernServer = modernServer

	// Register tools
	s.registerTools()

	// Register resources
	s.registerResources()

	// Create SSE server with options
	basePath := s.config.BasePath
	if basePath == "" {
		basePath = "/mcp"
	}

	options := []server.SSEOption{
		server.WithStaticBasePath(basePath),
		server.WithKeepAlive(true),
		server.WithKeepAliveInterval(15 * time.Second),
	}

	s.sseServer = server.NewSSEServer(mcpServer, options...)
	s.modernHandler = officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	s.stdioServer = server.NewStdioServer(mcpServer)

	s.logger.Info("MCP SSE server built",
		zap.String("base_path", basePath),
		zap.Bool("keep_alive", true),
		zap.Duration("keep_alive_interval", 15*time.Second))

	return nil
}

// Start starts the MCP server
func (s *Server) Start(ctx context.Context) error {
	s.logger.Info("starting MCP server")
	return nil
}

// Stop stops the MCP server
func (s *Server) Stop() error {
	s.logger.Info("stopping MCP server")
	return nil
}

// Name returns the server name
func (s *Server) Name() string {
	return "mcp-server"
}

// Health checks the server health
func (s *Server) Health(ctx context.Context) error {
	return nil
}

// GetServer returns the underlying MCP server
func (s *Server) GetServer() *server.MCPServer {
	return s.mcpServer
}

// GetSSEServer returns the SSE server for HTTP integration
func (s *Server) GetSSEServer() *server.SSEServer {
	return s.sseServer
}

// GetStreamableHandler returns the stateless modern MCP HTTP handler.
func (s *Server) GetStreamableHandler() http.Handler {
	return s.modernHandler
}

// GetStdioServer returns the stdio server for direct process integration.
func (s *Server) GetStdioServer() *server.StdioServer {
	return s.stdioServer
}

// buildInstructions returns the MCP server instructions
func buildInstructions() string {
	return "RAG MCP server:\n" +
		"- rag_ingest_start: ingest a document (deployment_id required; tenant_id optional -> default if missing)\n" +
		"- rag_ingest_status: check ingestion job status\n" +
		"- rag_query: query the knowledge base (deployment_id required; tenant_id optional -> default if missing)\n" +
		"- rag_score_document: score a document against a mission/requirement text and return structured matching results\n" +
		"- rag_compare_documents: compare two full documents and estimate if they represent the same candidate\n" +
		"- rag_extract_document_text: extract raw chunk text for a document or metadata-filtered document set\n" +
		"- rag_scan_document: scan a document or metadata-filtered document set exhaustively for PII with structured matches and ignore rules\n" +
		"- rag_list_tenants: list tenants for a deployment (to discover defaults or switch tenant)\n" +
		"- rag_list_documents: list all documents for a tenant with pagination and filters\n" +
		"- rag_check_document: check if a document already exists by URL or hash (before ingesting)\n" +
		"- rag_purge_tenant: purge documents, chunks, and vectors for a tenant (confirm required)\n" +
		"- Resources: rag://document/{id}, rag://chunk/{id}, rag://job/{id}, rag://deployment/{id}, rag://tenant/{id}\n" +
		"Multi-tenant isolation is enforced per deployment/tenant; tenant defaults are created/resolved automatically when absent."
}

// LastRequestAt returns the time of the last tool call request.
func (s *Server) LastRequestAt() time.Time {
	if v := s.lastRequestAt.Load(); v != nil {
		return v.(time.Time)
	}
	return time.Time{}
}

// RequestCount returns the total number of tool call requests processed.
func (s *Server) RequestCount() int64 {
	return s.requestCount.Load()
}

// wrapToolHandler wraps a tool handler with logging for request/response tracing
func (s *Server) wrapToolHandler(toolName string, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		startTime := time.Now()
		requestID := xid.New().String()

		// Track activity
		s.lastRequestAt.Store(time.Now())
		s.requestCount.Add(1)

		// Log incoming request
		s.logger.Info("MCP tool call started",
			zap.String("request_id", requestID),
			zap.String("tool", toolName),
			zap.Any("arguments", request.Params.Arguments),
		)

		// Execute the handler
		result, err := handler(ctx, request)
		duration := time.Since(startTime)

		// Log the result
		if err != nil {
			s.logger.Error("MCP tool call failed with error",
				zap.String("request_id", requestID),
				zap.String("tool", toolName),
				zap.Duration("duration", duration),
				zap.Error(err),
			)
			return result, err
		}

		if result != nil && result.IsError {
			// Extract error message from content if available
			errorMsg := "unknown error"
			if len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(mcp.TextContent); ok {
					errorMsg = textContent.Text
				}
			}
			s.logger.Warn("MCP tool call returned error result",
				zap.String("request_id", requestID),
				zap.String("tool", toolName),
				zap.Duration("duration", duration),
				zap.String("error_content", errorMsg),
			)
			return result, nil
		}

		// Success - log response summary
		responseSize := 0
		if result != nil && len(result.Content) > 0 {
			if textContent, ok := result.Content[0].(mcp.TextContent); ok {
				responseSize = len(textContent.Text)
			}
		}

		s.logger.Info("MCP tool call completed successfully",
			zap.String("request_id", requestID),
			zap.String("tool", toolName),
			zap.Duration("duration", duration),
			zap.Int("response_size_bytes", responseSize),
		)

		return result, nil
	}
}

// registerTools registers MCP tools
func (s *Server) registerTools() {
	// Register rag_query tool (with response wrapper for large results)
	ragQueryTool := NewRAGQueryTool(s.ragManager, s.tenantRepo, s.responseWrapper, s.logger)
	s.addTool(ragQueryTool.Definition(), s.wrapToolHandler("rag_query", ragQueryTool.Handler()))

	// Register document scoring tool
	scoreDocTool := NewScoreDocumentTool(s.documentRepo, s.chunkRepo, s.tenantRepo, s.llmClient, s.responseWrapper, s.logger)
	s.addTool(scoreDocTool.Definition(), s.wrapToolHandler("rag_score_document", scoreDocTool.Handler()))

	// Register document comparison tool
	compareDocTool := NewCompareDocumentsTool(s.documentRepo, s.chunkRepo, s.tenantRepo, s.llmClient, s.responseWrapper, s.logger)
	s.addTool(compareDocTool.Definition(), s.wrapToolHandler("rag_compare_documents", compareDocTool.Handler()))

	// Register raw extraction tool (with response wrapper for large results)
	extractDocTool := NewExtractDocumentTextTool(s.documentRepo, s.chunkRepo, s.tenantRepo, s.responseWrapper, s.logger)
	s.addTool(extractDocTool.Definition(), s.wrapToolHandler("rag_extract_document_text", extractDocTool.Handler()))

	// Register rag_ingest_start tool
	ingestStartTool := NewIngestStartTool(s.ingestManager, s.tenantRepo, s.bufferClient, s.logger)
	s.addTool(ingestStartTool.Definition(), s.wrapToolHandler("rag_ingest_start", ingestStartTool.Handler()))

	// Register rag_ingest_status tool
	ingestStatusTool := NewIngestStatusTool(s.ingestManager, s.logger)
	s.addTool(ingestStatusTool.Definition(), s.wrapToolHandler("rag_ingest_status", ingestStatusTool.Handler()))

	// Register tenant listing tool
	listTenantsTool := NewListTenantsTool(s.tenantRepo, s.logger)
	s.addTool(listTenantsTool.Definition(), s.wrapToolHandler("rag_list_tenants", listTenantsTool.Handler()))

	// Register scan document tool (with response wrapper for large scan results)
	scanDocTool := NewScanDocumentTool(s.documentRepo, s.chunkRepo, s.tenantRepo, s.inspector, s.responseWrapper, s.logger)
	s.addTool(scanDocTool.Definition(), s.wrapToolHandler("rag_scan_document", scanDocTool.Handler()))

	// Register check document tool (to verify if document already exists)
	checkDocTool := NewCheckDocumentTool(s.documentRepo, s.tenantRepo, s.logger)
	s.addTool(checkDocTool.Definition(), s.wrapToolHandler("rag_check_document", checkDocTool.Handler()))

	// Register list documents tool
	listDocsTool := NewListDocumentsTool(s.documentRepo, s.tenantRepo, s.logger)
	s.addTool(listDocsTool.Definition(), s.wrapToolHandler("rag_list_documents", listDocsTool.Handler()))

	// Register purge tenant tool
	purgeTenantTool := NewPurgeTenantTool(s.tenantRepo, s.documentRepo, s.chunkRepo, s.jobRepo, s.vectorStore, s.queryCache, s.logger)
	s.addTool(purgeTenantTool.Definition(), s.wrapToolHandler("rag_purge_tenant", purgeTenantTool.Handler()))

	// Register reindex embeddings tool
	reindexTool := NewReindexEmbeddingsTool(s.ingestManager, s.tenantRepo, s.logger)
	s.addTool(reindexTool.Definition(), s.wrapToolHandler("rag_reindex_embeddings", reindexTool.Handler()))

	s.logger.Info("registered MCP tools", zap.Int("count", 12))
}

func (s *Server) addTool(definition mcp.Tool, handler server.ToolHandlerFunc) {
	s.mcpServer.AddTool(definition, handler)
	s.modernServer.AddTool(toOfficialTool(definition), adaptToolHandler(handler))
}

func (s *Server) registerResources() {
	templates := []mcp.ResourceTemplate{
		newResourceTemplate("rag_document", "rag://document/{id}", "RAG document by ID"),
		newResourceTemplate("rag_chunk", "rag://chunk/{id}", "RAG chunk by ID"),
		newResourceTemplate("rag_job", "rag://job/{id}", "Ingestion job status by ID"),
		newResourceTemplate("rag_deployment", "rag://deployment/{id}", "Deployment metadata by ID"),
		newResourceTemplate("rag_tenant", "rag://tenant/{id}", "Tenant metadata by ID"),
	}
	for _, tmpl := range templates {
		s.mcpServer.AddResourceTemplate(tmpl, s.readResource)
		s.modernServer.AddResourceTemplate(toOfficialResourceTemplate(tmpl), adaptResourceHandler(s.readResource))
	}
}

// newResourceTemplate builds a ResourceTemplate with a parsed URI template.
func newResourceTemplate(name, uriTemplate, desc string) mcp.ResourceTemplate {
	tpl, _ := uritemplate.New(uriTemplate)
	return mcp.ResourceTemplate{
		Name:        name,
		URITemplate: &mcp.URITemplate{Template: tpl},
		Description: desc,
		MIMEType:    "application/json",
	}
}

// readResource handles MCP resource reads for RAG entities.
func (s *Server) readResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	startTime := time.Now()
	requestID := xid.New().String()
	uri := req.Params.URI

	// Determine resource type for logging
	resourceType := "unknown"
	switch {
	case strings.HasPrefix(uri, "rag://document/"):
		resourceType = "document"
	case strings.HasPrefix(uri, "rag://chunk/"):
		resourceType = "chunk"
	case strings.HasPrefix(uri, "rag://job/"):
		resourceType = "job"
	case strings.HasPrefix(uri, "rag://deployment/"):
		resourceType = "deployment"
	case strings.HasPrefix(uri, "rag://tenant/"):
		resourceType = "tenant"
	}

	s.logger.Info("MCP resource read started",
		zap.String("request_id", requestID),
		zap.String("resource_type", resourceType),
		zap.String("uri", uri),
		zap.Any("arguments", req.Params.Arguments),
	)

	var result []mcp.ResourceContents
	var err error

	switch {
	case strings.HasPrefix(uri, "rag://document/"):
		result, err = s.readDocument(ctx, uri)
	case strings.HasPrefix(uri, "rag://chunk/"):
		result, err = s.readChunk(ctx, uri)
	case strings.HasPrefix(uri, "rag://job/"):
		result, err = s.readJob(ctx, uri)
	case strings.HasPrefix(uri, "rag://deployment/"):
		result, err = s.readDeployment(ctx, uri)
	case strings.HasPrefix(uri, "rag://tenant/"):
		result, err = s.readTenant(ctx, req)
	default:
		err = fmt.Errorf("unsupported resource: %s", uri)
	}

	duration := time.Since(startTime)

	if err != nil {
		s.logger.Error("MCP resource read failed",
			zap.String("request_id", requestID),
			zap.String("resource_type", resourceType),
			zap.String("uri", uri),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return nil, err
	}

	// Calculate response size
	responseSize := 0
	if len(result) > 0 {
		if textContent, ok := result[0].(mcp.TextResourceContents); ok {
			responseSize = len(textContent.Text)
		}
	}

	s.logger.Info("MCP resource read completed successfully",
		zap.String("request_id", requestID),
		zap.String("resource_type", resourceType),
		zap.String("uri", uri),
		zap.Duration("duration", duration),
		zap.Int("response_size_bytes", responseSize),
	)

	return result, nil
}

func (s *Server) readDocument(ctx context.Context, uri string) ([]mcp.ResourceContents, error) {
	idStr := strings.TrimPrefix(uri, "rag://document/")
	docID, err := xid.FromString(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid document id")
	}
	doc, err := s.documentRepo.Get(ctx, docID)
	if err != nil {
		return nil, err
	}
	chunks, _ := s.chunkRepo.ListByDocument(ctx, docID)

	// Build document payload
	docPayload := map[string]interface{}{
		"id":         doc.ID.String(),
		"title":      doc.Title,
		"tenant_id":  doc.TenantID.String(),
		"deployment": nil,
		"source_url": doc.SourceURL,
		"metadata":   doc.Metadata,
		"doc_type":   doc.DocType,
	}
	if doc.DuplicateOf != nil {
		docPayload["duplicate_of"] = doc.DuplicateOf.String()
	}
	if doc.HasPii {
		docPayload["pii_info"] = doc.PiiSummary
	}

	// Build chunks payload
	chunkPayload := make([]interface{}, 0, len(chunks))
	for _, ch := range chunks {
		chunkPayload = append(chunkPayload, map[string]interface{}{
			"id":          ch.ID.String(),
			"text":        ch.Text,
			"metadata":    ch.Metadata,
			"document_id": ch.DocumentID.String(),
		})
	}

	// Use ResponseWrapper for smart document response with auto-buffering
	if s.responseWrapper != nil && s.responseWrapper.IsEnabled() {
		summary := fmt.Sprintf("Document '%s' with %d chunks", doc.Title, len(chunks))
		return s.responseWrapper.WrapDocumentResource(ctx, uri, docPayload, chunkPayload, summary)
	}

	// Fallback: return full payload directly
	payload := map[string]interface{}{
		"document":    docPayload,
		"chunks":      chunkPayload,
		"chunk_count": len(chunkPayload),
	}
	bytes, _ := json.Marshal(payload)
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "application/json", Text: string(bytes)},
	}, nil
}

func (s *Server) readChunk(ctx context.Context, uri string) ([]mcp.ResourceContents, error) {
	idStr := strings.TrimPrefix(uri, "rag://chunk/")
	chunkID, err := xid.FromString(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid chunk id")
	}
	ch, err := s.chunkRepo.Get(ctx, chunkID)
	if err != nil {
		return nil, err
	}

	payload := map[string]interface{}{
		"id":              ch.ID.String(),
		"text":            ch.Text,
		"metadata":        ch.Metadata,
		"document_id":     ch.DocumentID.String(),
		"vector_id":       ch.VectorID,
		"embedding_model": ch.EmbeddingModel,
	}

	bytes, _ := json.Marshal(payload)
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "application/json", Text: string(bytes)},
	}, nil
}

func (s *Server) readJob(ctx context.Context, uri string) ([]mcp.ResourceContents, error) {
	idStr := strings.TrimPrefix(uri, "rag://job/")
	jobID, err := xid.FromString(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid job id")
	}
	job, err := s.jobRepo.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}

	payload := map[string]interface{}{
		"id":                   job.ID.String(),
		"tenant_id":            job.TenantID.String(),
		"status":               job.Status,
		"progress":             job.Progress,
		"message":              job.Message,
		"created_at":           job.CreatedAt,
		"updated_at":           job.UpdatedAt,
		"source_type":          job.SourceType,
		"has_content_findings": job.HasContentFindings,
		"content_findings":     job.ContentFindings,
	}

	bytes, _ := json.Marshal(payload)
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "application/json", Text: string(bytes)},
	}, nil
}

func (s *Server) readDeployment(ctx context.Context, uri string) ([]mcp.ResourceContents, error) {
	idStr := strings.TrimPrefix(uri, "rag://deployment/")
	depID, err := xid.FromString(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid deployment id")
	}
	dep, err := s.deployRepo.Get(ctx, depID)
	if err != nil {
		return nil, err
	}
	tenants, _ := s.tenantRepo.ListByDeployment(ctx, depID)
	tenantPayload := make([]map[string]interface{}, 0, len(tenants))
	for _, t := range tenants {
		depIDStr := ""
		if t.DeploymentID != nil {
			depIDStr = t.DeploymentID.String()
		}
		tenantPayload = append(tenantPayload, map[string]interface{}{
			"id":         t.ID.String(),
			"name":       t.Name,
			"deployment": depIDStr,
			"is_default": t.IsDefault,
		})
	}

	payload := map[string]interface{}{
		"id":         dep.ID.String(),
		"name":       dep.Name,
		"tenants":    tenantPayload,
		"created_at": dep.CreatedAt,
	}

	bytes, _ := json.Marshal(payload)
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "application/json", Text: string(bytes)},
	}, nil
}

func (s *Server) readTenant(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	uri := req.Params.URI

	idStr := strings.TrimPrefix(uri, "rag://tenant/")

	// Extract deployment_id from path segment (encoded by the AgentMaurice server as __dep__ separator)
	// This is needed because mcp-go overwrites Arguments when parsing URI templates
	// Note: Using __ instead of :: because : is a reserved URI character
	var deploymentIDFromPath string
	if idx := strings.Index(idStr, "__dep__"); idx != -1 {
		deploymentIDFromPath = idStr[idx+7:] // 7 = len("__dep__")
		idStr = idStr[:idx]                  // Remove deployment_id suffix from idStr
	}

	s.logger.Debug("readTenant called",
		zap.String("uri", uri),
		zap.String("idStr", idStr),
		zap.String("deployment_id_from_path", deploymentIDFromPath),
		zap.Any("arguments", req.Params.Arguments))

	// Try to parse as XID first
	tenantID, err := xid.FromString(idStr)
	var t *ent.Tenant

	if err == nil {
		// Valid XID - lookup by ID
		t, err = s.tenantRepo.Get(ctx, tenantID)
		if err != nil {
			return nil, err
		}
	} else {
		// Not a valid XID - try lookup by name
		// Extract deployment_id from path segment first, then fallback to arguments
		deploymentIDStr := deploymentIDFromPath
		if deploymentIDStr == "" && req.Params.Arguments != nil {
			deploymentIDStr, _ = req.Params.Arguments["deployment_id"].(string)
		}
		if deploymentIDStr == "" {
			s.logger.Warn("tenant name lookup missing deployment_id",
				zap.String("tenant_name", idStr),
				zap.Any("arguments", req.Params.Arguments))
			return nil, fmt.Errorf("tenant name lookup requires deployment_id in arguments (got: %v)", req.Params.Arguments)
		}
		deploymentID, parseErr := xid.FromString(deploymentIDStr)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid deployment_id: %s", deploymentIDStr)
		}
		t, err = s.tenantRepo.GetByName(ctx, deploymentID, idStr)
		if err != nil {
			return nil, fmt.Errorf("tenant not found: %s", idStr)
		}
	}
	depID := ""
	if t.DeploymentID != nil {
		depID = t.DeploymentID.String()
	}

	payload := map[string]interface{}{
		"id":         t.ID.String(),
		"name":       t.Name,
		"deployment": depID,
		"is_default": t.IsDefault,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}

	bytes, _ := json.Marshal(payload)
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: "application/json", Text: string(bytes)},
	}, nil
}
