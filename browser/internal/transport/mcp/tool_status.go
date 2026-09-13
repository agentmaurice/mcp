package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
)

// StatusTool handles browser status requests
type StatusTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewStatusTool creates a new status tool
func NewStatusTool(browserManager *business.BrowserManager, logger *zap.Logger) *StatusTool {
	return &StatusTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-status"),
	}
}

// Definition returns the tool definition
func (t *StatusTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_status",
		Description: "Get the current status of the browser connection. Returns whether the MCP server is running and if the headless browser (Chrome/Lightpanda) is connected via CDP.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *StatusTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("getting browser status")

		status := t.browserManager.GetStatus(ctx)

		return createTextResult(status)
	}
}

// ReconnectTool handles browser reconnection requests
type ReconnectTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewReconnectTool creates a new reconnect tool
func NewReconnectTool(browserManager *business.BrowserManager, logger *zap.Logger) *ReconnectTool {
	return &ReconnectTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-reconnect"),
	}
}

// Definition returns the tool definition
func (t *ReconnectTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_reconnect",
		Description: "Attempt to connect or reconnect to the headless browser via CDP. Use this if the browser connection was lost or if starting with browser_status showing disconnected.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *ReconnectTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("attempting browser reconnection")

		err := t.browserManager.Reconnect(ctx)
		if err != nil {
			t.logger.Error("reconnection failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		status := t.browserManager.GetStatus(ctx)
		return createTextResult(map[string]interface{}{
			"success": true,
			"message": "Successfully connected to browser",
			"status":  status,
		})
	}
}
