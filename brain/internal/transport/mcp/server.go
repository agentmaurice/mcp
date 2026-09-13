package mcp

import (
	"context"
	"net/http"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const (
	serviceName    = "brain-server"
	serviceVersion = "1.1.4"
)

// Server builds MCP server and transports.
type Server struct {
	mcpServer    *server.MCPServer
	sseServer    *server.SSEServer
	modernServer *officialmcp.Server
	streamable   http.Handler
	stdioServer  *server.StdioServer
	storage      storage.Manager
	cfg          *config.Config
	wrapper      *ResponseWrapper
	orchestrator *search.Orchestrator
	pipeline     *indexer.Pipeline
	bufferClient documentBufferReader
	logger       *zap.Logger
}

// NewServer creates a new MCP server wrapper.
func NewServer(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, orchestrator *search.Orchestrator, pipeline *indexer.Pipeline, bufferClient documentBufferReader, logger *zap.Logger) *Server {
	return &Server{
		storage:      storage,
		cfg:          cfg,
		wrapper:      wrapper,
		orchestrator: orchestrator,
		pipeline:     pipeline,
		bufferClient: bufferClient,
		logger:       logger.Named("mcp"),
	}
}

// Build builds the MCP server and registers tools.
func (s *Server) Build() error {
	s.logger.Info("building MCP server")

	mcpServer := server.NewMCPServer(
		serviceName,
		serviceVersion,
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, false),
		server.WithLogging(),
		server.WithInstructions(buildInstructions()),
	)
	s.modernServer = newModernMCPServer(serviceName, serviceVersion, buildInstructions())

	s.registerTools(mcpServer)
	s.mcpServer = mcpServer
	modernHandler := officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return s.modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	s.streamable = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := shared.IdentityFromRequest(r, s.cfg.Storage.DefaultTenantID)
		ctx := shared.ContextWithIdentity(r.Context(), identity)
		modernHandler.ServeHTTP(w, r.WithContext(ctx))
	})

	s.logger.Info("MCP server built",
		zap.String("base_path", s.cfg.Server.BasePath),
		zap.Bool("keep_alive", s.cfg.Server.KeepAlive))

	return nil
}

// MCP returns the underlying MCP server.
func (s *Server) MCP() *server.MCPServer {
	return s.mcpServer
}

// Streamable returns the stateless modern MCP HTTP handler.
func (s *Server) Streamable() http.Handler {
	return s.streamable
}

func buildInstructions() string {
	return "brain is an intelligent code and documentation search engine.\n" +
		"It indexes codebases, documentation, and configurations, then provides\n" +
		"multi-modal search (keyword BM25, semantic vector, hybrid fusion).\n" +
		"Use it to find relevant code, understand dependencies, and locate documentation.\n" +
		"Recommended workflow: brain.search to discover, brain.timeline for nearby context, brain.get for exact details.\n\n" +
		"Available tools:\n" +
		"- brain.search: intelligent search with auto mode selection and compact results\n" +
		"- brain.timeline: fetch nearby chunks around search hits\n" +
		"- brain.get: fetch exact chunk or document details by id\n" +
		"- brain.keyword: BM25 full-text keyword search\n" +
		"- brain.semantic: vector-based semantic search\n" +
		"- brain.hybrid: combined BM25 + vector search with configurable alpha\n" +
		"- brain.index: scan a filesystem visible to the Brain process\n" +
		"- brain.document.upsert: index one document from inline content or an authenticated buffer key\n" +
		"- brain.index.status: check indexation job status\n" +
		"- brain.sources: list indexed sources\n" +
		"- brain.schema: describe indexed content schema\n" +
		"- brain.stats: storage and indexation statistics\n" +
		"- brain.health: health check"
}

// registerTools registers MCP tools.
func (s *Server) registerTools(mcpServer *server.MCPServer) {
	tools := []Tool{
		NewHealthTool(s.logger),
		NewStatsTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewSourcesTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewSchemaTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewIndexTool(s.storage, s.cfg, s.pipeline, s.logger),
		NewDocumentUpsertTool(s.cfg, s.pipeline, s.bufferClient, s.logger),
		NewIndexStatusTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewSearchTool(s.orchestrator, s.cfg, s.wrapper, s.logger),
		NewTimelineTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewGetTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewKeywordTool(s.orchestrator, s.cfg, s.wrapper, s.logger),
		NewSemanticTool(s.orchestrator, s.cfg, s.wrapper, s.logger),
		NewHybridTool(s.orchestrator, s.cfg, s.wrapper, s.logger),
	}

	for _, tool := range tools {
		definition := tool.Definition()
		handler := s.wrapToolHandler(tool.Name(), tool.Handler())
		mcpServer.AddTool(definition, handler)
		s.modernServer.AddTool(toOfficialTool(definition), adaptToolHandler(handler))
	}

	s.logger.Info("registered MCP tools", zap.Int("count", len(tools)))
}

func (s *Server) wrapToolHandler(toolName string, handler ToolHandler) ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		start := time.Now()
		result, err := handler(ctx, request)
		elapsed := time.Since(start)
		if err != nil {
			s.logger.Error("tool call failed", zap.String("tool", toolName), zap.Duration("elapsed", elapsed), zap.Error(err))
			return result, err
		}
		if result != nil && result.IsError {
			s.logger.Warn("tool call returned error", zap.String("tool", toolName), zap.Duration("elapsed", elapsed))
		} else {
			s.logger.Debug("tool call completed", zap.String("tool", toolName), zap.Duration("elapsed", elapsed))
		}
		return result, nil
	}
}

// Tool defines the interface for MCP tools.
type Tool interface {
	Name() string
	Definition() mcplib.Tool
	Handler() ToolHandler
}

// ToolHandler defines the MCP tool handler signature.
type ToolHandler = server.ToolHandlerFunc
