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
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

// Server keeps the historical STDIO server while exposing the same tools on a
// stateless MCP 2026 HTTP endpoint.
type Server struct {
	legacy *legacyserver.MCPServer
	modern *officialmcp.Server
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
	return &Server{
		legacy: legacyserver.NewMCPServer(
			name,
			version,
			legacyserver.WithToolCapabilities(false),
			legacyserver.WithInstructions(instructions),
		),
		modern: officialmcp.NewServer(
			&officialmcp.Implementation{Name: name, Version: version},
			&officialmcp.ServerOptions{
				Instructions: instructions,
				Capabilities: &officialmcp.ServerCapabilities{
					Tools:     &officialmcp.ToolCapabilities{},
					Prompts:   &officialmcp.PromptCapabilities{},
					Resources: &officialmcp.ResourceCapabilities{},
				},
			},
		),
	}
}

// AddTool registers one definition and handler on both protocol eras.
func (s *Server) AddTool(definition legacymcp.Tool, handler legacyserver.ToolHandlerFunc) {
	s.legacy.AddTool(definition, handler)
	s.modern.AddTool(toOfficialTool(definition), adaptToolHandler(handler))
}

// Legacy returns the historical server for callers that own their transport.
func (s *Server) Legacy() *legacyserver.MCPServer {
	return s.legacy
}

// Handler returns the stateless modern HTTP handler.
func (s *Server) Handler() http.Handler {
	return officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return s.modern },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
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

func toOfficialTool(definition legacymcp.Tool) *officialmcp.Tool {
	data, err := json.Marshal(definition)
	if err != nil {
		panic(fmt.Errorf("marshal tool %q for modern MCP: %w", definition.Name, err))
	}
	var tool officialmcp.Tool
	if err := json.Unmarshal(data, &tool); err != nil {
		panic(fmt.Errorf("convert tool %q for modern MCP: %w", definition.Name, err))
	}
	inputSchema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		panic(fmt.Errorf("tool %q input schema is not a JSON object", definition.Name))
	}
	inputSchema["$schema"] = jsonSchema202012
	tool.InputSchema = inputSchema
	if outputSchema, ok := tool.OutputSchema.(map[string]any); ok {
		outputSchema["$schema"] = jsonSchema202012
		tool.OutputSchema = outputSchema
	}
	return &tool
}

func adaptToolHandler(handler legacyserver.ToolHandlerFunc) officialmcp.ToolHandler {
	return func(ctx context.Context, request *officialmcp.CallToolRequest) (*officialmcp.CallToolResult, error) {
		var arguments any
		if len(request.Params.Arguments) > 0 {
			if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
				return nil, fmt.Errorf("decode tool arguments: %w", err)
			}
		}
		var meta *legacymcp.Meta
		if request.Params.Meta != nil {
			fields := make(map[string]any, len(request.Params.Meta))
			for key, value := range request.Params.Meta {
				fields[key] = value
			}
			meta = legacymcp.NewMetaFromMap(fields)
		}
		var header map[string][]string
		if request.Extra != nil {
			header = request.Extra.Header
		}
		result, err := handler(ctx, legacymcp.CallToolRequest{
			Header: header,
			Params: legacymcp.CallToolParams{
				Name:      request.Params.Name,
				Arguments: arguments,
				Meta:      meta,
			},
		})
		if err != nil || result == nil {
			return nil, err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("marshal tool result: %w", err)
		}
		var converted officialmcp.CallToolResult
		if err := json.Unmarshal(data, &converted); err != nil {
			return nil, fmt.Errorf("convert tool result: %w", err)
		}
		var raw struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("inspect tool result: %w", err)
		}
		if string(raw.StructuredContent) == "null" {
			converted.StructuredContent = json.RawMessage("null")
		}
		return &converted, nil
	}
}
