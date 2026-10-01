package modernmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	legacymcp "github.com/mark3labs/mcp-go/mcp"
	legacyserver "github.com/mark3labs/mcp-go/server"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

// Server keeps the historical STDIO server while exposing the same tools on a
// stateless MCP 2026 HTTP endpoint.
type Server struct {
	legacy *legacyserver.MCPServer
	modern *legacyserver.StreamableHTTPServer
}

// Config selects the transport exposed by Serve.
type Config struct {
	Transport string
	Address   string
	BasePath  string
}

// New creates a dual-era tool server. Tool catalogs are static, so neither
// protocol advertises listChanged or logging support.
func New(name, version, instructions string) *Server {
	legacyServer := legacyserver.NewMCPServer(
		name,
		version,
		legacyserver.WithToolCapabilities(false),
		legacyserver.WithPromptCapabilities(false),
		legacyserver.WithResourceCapabilities(false, false),
		legacyserver.WithCacheHints(0, legacymcp.CacheScopePublic),
		legacyserver.WithInstructions(instructions),
	)
	return &Server{
		legacy: legacyServer,
		modern: legacyserver.NewStreamableHTTPServer(legacyServer,
			legacyserver.WithStateLess(true), legacyserver.WithDisableStreaming(true), legacyserver.WithEndpointPath("/mcp")),
	}
}

// AddTool registers one definition and handler on both protocol eras.
func (s *Server) AddTool(definition legacymcp.Tool, handler legacyserver.ToolHandlerFunc) {
	if definition.RawInputSchema == nil {
		if raw, err := json.Marshal(definition.InputSchema); err == nil {
			var schema map[string]any
			if json.Unmarshal(raw, &schema) == nil {
				schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
				if raw, err = json.Marshal(schema); err == nil {
					definition.RawInputSchema = raw
					definition.InputSchema = legacymcp.ToolInputSchema{}
				}
			}
		}
	}
	s.legacy.AddTool(definition, func(ctx context.Context, request legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
		result, err := handler(ctx, request)
		if result != nil && result.StructuredContent != nil && len(result.RawStructuredContent) == 0 {
			if raw, marshalErr := json.Marshal(result.StructuredContent); marshalErr == nil {
				result.RawStructuredContent = raw
			}
		}
		return result, err
	})
}

// Legacy returns the historical server for callers that own their transport.
func (s *Server) Legacy() *legacyserver.MCPServer {
	return s.legacy
}

// Handler returns the stateless modern HTTP handler.
func (s *Server) Handler() http.Handler {
	return resourceErrorHandler(s.modern)
}

// ConfigFromEnv reads the common sidecar transport settings. Existing images
// remain STDIO by default.
func ConfigFromEnv(defaultBasePath string) Config {
	transport := strings.ToLower(strings.TrimSpace(os.Getenv("MCP_TRANSPORT")))
	if transport == "" {
		transport = "stdio"
	}
	address := strings.TrimSpace(os.Getenv("MCP_ADDRESS"))
	if address == "" {
		address = "127.0.0.1:8080"
	}
	basePath := strings.TrimSpace(os.Getenv("MCP_BASE_PATH"))
	if basePath == "" {
		basePath = defaultBasePath
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if len(basePath) > 1 {
		basePath = strings.TrimSuffix(basePath, "/")
	}
	return Config{Transport: transport, Address: address, BasePath: basePath}
}

// Serve runs STDIO, modern HTTP, or both until the context is cancelled or a
// transport exits.
func (s *Server) Serve(ctx context.Context, cfg Config) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if mode != "stdio" && mode != "http" && mode != "both" {
		return fmt.Errorf("unsupported MCP transport %q", cfg.Transport)
	}

	errCh := make(chan error, 2)
	var httpServer *http.Server
	if mode == "http" || mode == "both" {
		mux := http.NewServeMux()
		mux.Handle(cfg.BasePath, s.Handler())
		httpServer = &http.Server{
			Addr:              cfg.Address,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			err := httpServer.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errCh <- err
		}()
	}

	if mode == "stdio" || mode == "both" {
		stdio := legacyserver.NewStdioServer(s.legacy)
		go func() { errCh <- stdio.Listen(ctx, os.Stdin, os.Stdout) }()
	}

	select {
	case <-ctx.Done():
		if httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shut down modern MCP HTTP server: %w", err)
			}
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	}
}

// AddResourceTemplate registers a typed resource on the same SDK server.
func (s *Server) AddResourceTemplate(definition legacymcp.ResourceTemplate, handler legacyserver.ResourceHandlerFunc) {
	s.legacy.AddResourceTemplate(definition, func(ctx context.Context, req legacymcp.ReadResourceRequest) ([]legacymcp.ResourceContents, error) {
		contents, err := handler(ctx, req)
		if errors.Is(err, ErrResourceNotFound) {
			if state, ok := ctx.Value(resourceErrorKey{}).(*resourceErrorState); ok {
				state.missing = true
			}
		}
		return contents, err
	})
}
