package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
)

// NetworkCaptureStartTool handles network capture start requests
type NetworkCaptureStartTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewNetworkCaptureStartTool creates a new network capture start tool
func NewNetworkCaptureStartTool(browserManager *business.BrowserManager, logger *zap.Logger) *NetworkCaptureStartTool {
	return &NetworkCaptureStartTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-network-capture-start"),
	}
}

// Definition returns the tool definition
func (t *NetworkCaptureStartTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_network_capture_start",
		Description: "Start capturing network requests and responses. Returns a capture_id for tracking.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *NetworkCaptureStartTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing network_capture_start")

		captureID, err := t.browserManager.NetworkCaptureStart(ctx)
		if err != nil {
			t.logger.Error("network_capture_start failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"capture_id": captureID,
			"message":    "Network capture started",
		})
	}
}

// NetworkCaptureStopTool handles network capture stop requests
type NetworkCaptureStopTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewNetworkCaptureStopTool creates a new network capture stop tool
func NewNetworkCaptureStopTool(browserManager *business.BrowserManager, logger *zap.Logger) *NetworkCaptureStopTool {
	return &NetworkCaptureStopTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-network-capture-stop"),
	}
}

// Definition returns the tool definition
func (t *NetworkCaptureStopTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_network_capture_stop",
		Description: "Stop capturing network requests and return all captured entries.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *NetworkCaptureStopTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing network_capture_stop")

		entries, err := t.browserManager.NetworkCaptureStop(ctx)
		if err != nil {
			t.logger.Error("network_capture_stop failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"entries": entries,
			"count":   len(entries),
		})
	}
}

// NetworkMockTool handles network mock requests
type NetworkMockTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewNetworkMockTool creates a new network mock tool
func NewNetworkMockTool(browserManager *business.BrowserManager, logger *zap.Logger) *NetworkMockTool {
	return &NetworkMockTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-network-mock"),
	}
}

// Definition returns the tool definition
func (t *NetworkMockTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_network_mock",
		Description: "Register a network mock rule to intercept and respond to requests matching a URL pattern.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"url_pattern": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern for URL matching (case-sensitive). * matches non-slash chars, ** matches everything.",
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "HTTP method to match (e.g., GET, POST, PUT, DELETE). Leave empty to match any method.",
				},
				"response_status": map[string]interface{}{
					"type":        "integer",
					"description": "HTTP status code for the mocked response (default: 200)",
				},
				"response_headers": map[string]interface{}{
					"type":        "object",
					"description": "Response headers as key-value pairs",
				},
				"response_body": map[string]interface{}{
					"type":        "string",
					"description": "Response body content",
				},
				"once": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, rule is removed after first match (default: false)",
				},
			},
			Required: []string{"url_pattern"},
		},
	}
}

// Handler returns the tool handler function
func (t *NetworkMockTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		urlPattern, err := getStringArg(args, "url_pattern", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		method, _ := getStringArg(args, "method", false)
		responseStatus, _ := getIntArg(args, "response_status", 200)
		responseBody, _ := getStringArg(args, "response_body", false)
		once, _ := getBoolArg(args, "once", false)

		// Get response headers as object
		responseHeaders, _ := getObjectArg(args, "response_headers", false)

		t.logger.Debug("executing network_mock",
			zap.String("url_pattern", urlPattern),
			zap.String("method", method),
			zap.Int("response_status", responseStatus))

		mockID, err := t.browserManager.NetworkMock(ctx, urlPattern, method, responseStatus, responseHeaders, responseBody, once)
		if err != nil {
			t.logger.Error("network_mock failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"mock_id":   mockID,
			"message":   "Mock rule registered",
			"url_pattern": urlPattern,
			"method":    method,
		})
	}
}

// NetworkMockClearTool handles network mock clear requests
type NetworkMockClearTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewNetworkMockClearTool creates a new network mock clear tool
func NewNetworkMockClearTool(browserManager *business.BrowserManager, logger *zap.Logger) *NetworkMockClearTool {
	return &NetworkMockClearTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-network-mock-clear"),
	}
}

// Definition returns the tool definition
func (t *NetworkMockClearTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_network_mock_clear",
		Description: "Clear all network mock rules for the current session.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *NetworkMockClearTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing network_mock_clear")

		err := t.browserManager.NetworkMockClear(ctx)
		if err != nil {
			t.logger.Error("network_mock_clear failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"message": "All mock rules cleared",
		})
	}
}
