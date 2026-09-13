package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const (
	serviceName    = "memory-server"
	serviceVersion = "1.0.0"
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
	logger       *zap.Logger
}

// NewServer creates a new MCP server wrapper.
func NewServer(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *Server {
	return &Server{
		storage: storage,
		cfg:     cfg,
		wrapper: wrapper,
		logger:  logger.Named("mcp"),
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
	s.registerResources(mcpServer)
	s.mcpServer = mcpServer

	basePath := s.cfg.Server.BasePath
	if basePath == "" {
		basePath = "/mcp"
	}

	sseOptions := []server.SSEOption{
		server.WithStaticBasePath(basePath),
		server.WithKeepAlive(s.cfg.Server.KeepAlive),
		server.WithKeepAliveInterval(time.Duration(s.cfg.Server.KeepAliveInterval) * time.Second),
	}

	sseOptions = append(sseOptions, server.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		id := shared.IdentityFromRequest(r, s.cfg.Storage.DefaultTenantID)
		return shared.ContextWithIdentity(ctx, id)
	}))

	s.sseServer = server.NewSSEServer(mcpServer, sseOptions...)
	modernHandler := officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return s.modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	s.streamable = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := shared.IdentityFromRequest(r, s.cfg.Storage.DefaultTenantID)
		ctx := shared.ContextWithIdentity(r.Context(), identity)
		modernHandler.ServeHTTP(w, r.WithContext(ctx))
	})
	stdioServer := server.NewStdioServer(mcpServer)
	stdioServer.SetContextFunc(func(ctx context.Context) context.Context {
		id := shared.IdentityFromEnv(s.cfg.Storage.DefaultTenantID)
		return shared.ContextWithIdentity(ctx, id)
	})
	s.stdioServer = stdioServer

	s.logger.Info("MCP server built",
		zap.String("base_path", basePath),
		zap.Bool("keep_alive", s.cfg.Server.KeepAlive),
		zap.Int("keep_alive_interval", s.cfg.Server.KeepAliveInterval))

	return nil
}

// MCP returns the underlying MCP server.
func (s *Server) MCP() *server.MCPServer {
	return s.mcpServer
}

// SSE returns the SSE server.
func (s *Server) SSE() *server.SSEServer {
	return s.sseServer
}

// Streamable returns the streamable HTTP server.
func (s *Server) Streamable() http.Handler {
	return s.streamable
}

// Stdio returns the stdio server.
func (s *Server) Stdio() *server.StdioServer {
	return s.stdioServer
}

func buildInstructions() string {
	return "memory is a persistent, structured project memory.\n" +
		"Use it to store and retrieve facts, entities, documents, and decisions across sessions.\n" +
		"Do not use it for transient reasoning. Prefer reading from kernel views and app projections.\n" +
		"Use memory.private_ingest when payloads need sanitation before persistence.\n\n" +
		"CompanyMemory MCP server:\n" +
		"- memory.query: run SELECT-only SQL with guardrails (LIMIT/timeout/RBAC)\n" +
		"- memory.schema.describe: list tables/views and columns\n" +
		"- memory.preview: preview a table/view with optional WHERE/columns\n" +
		"- memory.entities.upsert: upsert generic entities with provenance\n" +
		"- memory.facts.append: append-only facts/events\n" +
		"- memory.documents.register: register external documents with metadata\n" +
		"- memory.private_ingest: sanitize a fact/entity/document payload before persistence\n" +
		"- memory.links.upsert: upsert links between entities/facts/documents\n" +
		"- memory.views.create_or_replace: create app-scoped views\n" +
		"- memory.indexes.ensure: ensure app-scoped indexes (whitelisted)\n" +
		"- memory.observations.search: search derived observation facts through v_observations\n" +
		"- memory.capabilities: describe backend capabilities\n" +
		"- memory.health: health check\n" +
		"- memory.stats: storage stats and counts"
}

// registerTools registers MCP tools.
func (s *Server) registerTools(mcpServer *server.MCPServer) {
	tools := []Tool{
		NewQueryTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewSchemaDescribeTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewPreviewTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewUpsertEntitiesTool(s.storage, s.cfg, s.logger),
		NewAppendFactTool(s.storage, s.cfg, s.logger),
		NewAttachDocumentTool(s.storage, s.cfg, s.logger),
		NewPrivateIngestTool(s.storage, s.cfg, s.logger),
		NewLinksUpsertTool(s.storage, s.cfg, s.logger),
		NewViewsCreateTool(s.storage, s.cfg, s.logger),
		NewIndexesEnsureTool(s.storage, s.cfg, s.logger),
		NewObservationsSearchTool(s.storage, s.cfg, s.wrapper, s.logger),
		NewCapabilitiesTool(s.storage, s.cfg, s.logger),
		NewHealthTool(s.logger),
		NewStatsTool(s.storage, s.cfg, s.wrapper, s.logger),
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
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	Definition() mcp.Tool
	Handler() ToolHandler
}

// ToolHandler defines the MCP tool handler signature.
type ToolHandler = server.ToolHandlerFunc

// parseArgs unmarshals arguments into the target struct.
func parseArgs(input any, target any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal arguments: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("failed to unmarshal arguments: %w", err)
	}
	return nil
}
