package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// Server represents the MCP server
type Server struct {
	mcpServer      *server.MCPServer
	sseServer      *server.SSEServer
	modernServer   *officialmcp.Server
	modernHandler  http.Handler
	stdioServer    *server.StdioServer
	browserManager *business.BrowserManager
	config         *config.ServerConfig
	bufferConfig   *config.BufferConfig
	transportMode  string
	logger         *zap.Logger
	httpServer     *http.Server
	stdioCancel    context.CancelFunc
	stdioDone      chan struct{}
	running        bool
	mu             sync.RWMutex
}

// NewServer creates a new MCP server
func NewServer(browserManager *business.BrowserManager, cfg *config.ServerConfig, bufferCfg *config.BufferConfig, transportMode string, logger *zap.Logger) *Server {
	return &Server{
		browserManager: browserManager,
		config:         cfg,
		bufferConfig:   bufferCfg,
		transportMode:  normalizeTransportMode(transportMode),
		logger:         logger.Named("mcp-server"),
	}
}

// Start starts the MCP server
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	s.logger.Info("starting MCP server")

	// Create MCP server with capabilities
	mcpServer := server.NewMCPServer(
		"browser-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithLogging(),
	)

	s.mcpServer = mcpServer
	s.modernServer = newModernMCPServer("browser-mcp", "1.0.0")

	// Register all tools
	s.registerTools()

	// Create SSE server
	sseServer := server.NewSSEServer(mcpServer,
		server.WithSSEEndpoint(s.config.SSEPath),
		server.WithMessageEndpoint(s.config.MessagePath),
	)
	s.sseServer = sseServer
	s.modernHandler = officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return s.modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	s.stdioServer = server.NewStdioServer(mcpServer)

	if supportsHTTPTransport(s.transportMode) {
		// Create HTTP server
		mux := http.NewServeMux()

		// MCP SSE handler
		mux.HandleFunc(s.config.SSEPath, s.corsMiddleware(sseServer.ServeHTTP))

		// MCP message handler
		mux.HandleFunc(s.config.MessagePath, s.corsMiddleware(sseServer.ServeHTTP))

		// MCP streamable HTTP handler (stateless JSON-RPC) for sidecar integration.
		mux.HandleFunc("/mcp", s.corsMiddleware(s.modernHandler.ServeHTTP))

		// Health check handler
		mux.HandleFunc(s.config.HealthPath, s.corsMiddleware(s.healthHandler))

		s.httpServer = &http.Server{
			Addr:    s.config.Address,
			Handler: mux,
			// No timeouts for SSE
			ReadHeaderTimeout: 10 * time.Second,
		}

		// Start HTTP server in goroutine
		go func() {
			s.logger.Info("HTTP server starting",
				zap.String("address", s.config.Address),
				zap.String("sse_path", s.config.SSEPath),
				zap.String("message_path", s.config.MessagePath),
				zap.String("streamable_path", "/mcp"),
			)
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("HTTP server error", zap.Error(err))
			}
		}()
	}

	if supportsSTDIOTransport(s.transportMode) {
		stdioCtx, stdioCancel := context.WithCancel(ctx)
		s.stdioCancel = stdioCancel
		s.stdioDone = make(chan struct{})
		go func() {
			defer close(s.stdioDone)
			s.logger.Info("STDIO server starting")
			if err := s.stdioServer.Listen(stdioCtx, os.Stdin, os.Stdout); err != nil && stdioCtx.Err() == nil {
				s.logger.Error("STDIO server error", zap.Error(err))
			}
		}()
	}

	s.running = true
	s.logger.Info("MCP server started", zap.String("transport_mode", s.transportMode))
	return nil
}

// Stop stops the MCP server
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.logger.Info("stopping MCP server")

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.logger.Error("failed to shutdown HTTP server", zap.Error(err))
		}
	}
	if s.stdioCancel != nil {
		s.stdioCancel()
		s.stdioCancel = nil
	}
	if s.stdioDone != nil {
		select {
		case <-s.stdioDone:
		case <-time.After(3 * time.Second):
			s.logger.Warn("timeout waiting for STDIO server shutdown")
		}
		s.stdioDone = nil
	}

	s.running = false
	s.logger.Info("MCP server stopped")
	return nil
}

// Name returns the server name
func (s *Server) Name() string {
	return "MCPServer"
}

// Health checks server health
func (s *Server) Health(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.running {
		return fmt.Errorf("MCP server not running")
	}

	return nil
}

// registerTools registers all MCP tools
func (s *Server) registerTools() {
	s.logger.Info("registering MCP tools")

	navigateTool := NewNavigateTool(s.browserManager, s.logger)
	s.addTool(navigateTool.Definition(), navigateTool.Handler())

	clickTool := NewClickTool(s.browserManager, s.logger)
	s.addTool(clickTool.Definition(), clickTool.Handler())

	fillTool := NewFillTool(s.browserManager, s.logger)
	s.addTool(fillTool.Definition(), fillTool.Handler())

	getTextTool := NewGetTextTool(s.browserManager, s.logger)
	s.addTool(getTextTool.Definition(), getTextTool.Handler())

	getHTMLTool := NewGetHTMLTool(s.browserManager, s.logger)
	s.addTool(getHTMLTool.Definition(), getHTMLTool.Handler())

	screenshotTool := NewScreenshotTool(s.browserManager, s.bufferConfig, s.logger)
	s.addTool(screenshotTool.Definition(), screenshotTool.Handler())

	evaluateTool := NewEvaluateTool(s.browserManager, s.logger)
	s.addTool(evaluateTool.Definition(), evaluateTool.Handler())

	waitForSelectorTool := NewWaitForSelectorTool(s.browserManager, s.logger)
	s.addTool(waitForSelectorTool.Definition(), waitForSelectorTool.Handler())

	getPageInfoTool := NewGetPageInfoTool(s.browserManager, s.logger)
	s.addTool(getPageInfoTool.Definition(), getPageInfoTool.Handler())

	goBackTool := NewGoBackTool(s.browserManager, s.logger)
	s.addTool(goBackTool.Definition(), goBackTool.Handler())

	goForwardTool := NewGoForwardTool(s.browserManager, s.logger)
	s.addTool(goForwardTool.Definition(), goForwardTool.Handler())

	reloadTool := NewReloadTool(s.browserManager, s.logger)
	s.addTool(reloadTool.Definition(), reloadTool.Handler())

	selectOptionTool := NewSelectOptionTool(s.browserManager, s.logger)
	s.addTool(selectOptionTool.Definition(), selectOptionTool.Handler())

	scrollTool := NewScrollTool(s.browserManager, s.logger)
	s.addTool(scrollTool.Definition(), scrollTool.Handler())

	getAttributeTool := NewGetAttributeTool(s.browserManager, s.logger)
	s.addTool(getAttributeTool.Definition(), getAttributeTool.Handler())

	getMarkdownTool := NewGetMarkdownTool(s.browserManager, s.logger)
	s.addTool(getMarkdownTool.Definition(), getMarkdownTool.Handler())

	snapshotTool := NewSnapshotTool(s.browserManager, s.logger)
	s.addTool(snapshotTool.Definition(), snapshotTool.Handler())

	findTool := NewFindTool(s.browserManager, s.logger)
	s.addTool(findTool.Definition(), findTool.Handler())

	statusTool := NewStatusTool(s.browserManager, s.logger)
	s.addTool(statusTool.Definition(), statusTool.Handler())

	reconnectTool := NewReconnectTool(s.browserManager, s.logger)
	s.addTool(reconnectTool.Definition(), reconnectTool.Handler())

	captureTool := NewCaptureTool(s.browserManager, s.logger)
	s.addTool(captureTool.Definition(), captureTool.Handler())

	visualDiffTool := NewVisualDiffTool(s.browserManager, s.logger)
	s.addTool(visualDiffTool.Definition(), visualDiffTool.Handler())

	networkCaptureStartTool := NewNetworkCaptureStartTool(s.browserManager, s.logger)
	s.addTool(networkCaptureStartTool.Definition(), networkCaptureStartTool.Handler())

	networkCaptureStopTool := NewNetworkCaptureStopTool(s.browserManager, s.logger)
	s.addTool(networkCaptureStopTool.Definition(), networkCaptureStopTool.Handler())

	networkMockTool := NewNetworkMockTool(s.browserManager, s.logger)
	s.addTool(networkMockTool.Definition(), networkMockTool.Handler())

	networkMockClearTool := NewNetworkMockClearTool(s.browserManager, s.logger)
	s.addTool(networkMockClearTool.Definition(), networkMockClearTool.Handler())

	hoverTool := NewHoverTool(s.browserManager, s.logger)
	s.addTool(hoverTool.Definition(), hoverTool.Handler())

	pressKeyTool := NewPressKeyTool(s.browserManager, s.logger)
	s.addTool(pressKeyTool.Definition(), pressKeyTool.Handler())

	dragDropTool := NewDragDropTool(s.browserManager, s.logger)
	s.addTool(dragDropTool.Definition(), dragDropTool.Handler())

	assertTool := NewAssertTool(s.browserManager, s.logger)
	s.addTool(assertTool.Definition(), assertTool.Handler())

	switchFrameTool := NewSwitchFrameTool(s.browserManager, s.logger)
	s.addTool(switchFrameTool.Definition(), switchFrameTool.Handler())

	listFramesTool := NewListFramesTool(s.browserManager, s.logger)
	s.addTool(listFramesTool.Definition(), listFramesTool.Handler())

	s.logger.Info("MCP tools registered", zap.Int("count", 32))
}

func (s *Server) addTool(def mcp.Tool, handler server.ToolHandlerFunc) {
	def = withSessionKeySchema(def)
	toolName := def.Name
	wrapped := func(ctx context.Context, request mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("tool handler panicked",
					zap.String("tool", toolName),
					zap.Any("panic", recovered))
				result = createErrorResult(fmt.Errorf("internal error in tool %s", toolName))
				err = nil
			}
		}()
		sessionKey := extractSessionKeyFromArgs(request.Params.Arguments)
		ctx = shared.WithSessionKey(ctx, sessionKey)
		return handler(ctx, request)
	}
	s.mcpServer.AddTool(def, wrapped)
	s.modernServer.AddTool(toOfficialTool(def), adaptToolHandler(wrapped))
}

func withSessionKeySchema(def mcp.Tool) mcp.Tool {
	if def.InputSchema.Type != "object" {
		return def
	}
	if def.InputSchema.Properties == nil {
		def.InputSchema.Properties = map[string]interface{}{}
	}
	if _, exists := def.InputSchema.Properties["session_key"]; !exists {
		def.InputSchema.Properties["session_key"] = map[string]interface{}{
			"type":        "string",
			"description": "Optional sticky browser session key. Calls using the same key reuse the same pooled browser instance and snapshot refs.",
		}
	}
	return def
}

func extractSessionKeyFromArgs(args any) string {
	argMap, err := getArgsMap(args)
	if err != nil {
		return shared.DefaultSessionKey
	}
	sessionKey, err := getStringArg(argMap, "session_key", false)
	if err != nil {
		return shared.DefaultSessionKey
	}
	return strings.TrimSpace(sessionKey)
}

// corsMiddleware adds CORS headers to responses
func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Protocol-Version, Mcp-Session-Id, Mcp-Method, Mcp-Name")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// healthHandler handles health check requests
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.browserManager.Health(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(fmt.Sprintf(`{"status":"unhealthy","error":"%s"}`, err.Error())))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
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
