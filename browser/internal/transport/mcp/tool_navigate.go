package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// NavigateTool handles navigation requests
type NavigateTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewNavigateTool creates a new navigate tool
func NewNavigateTool(browserManager *business.BrowserManager, logger *zap.Logger) *NavigateTool {
	return &NavigateTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-navigate"),
	}
}

// Definition returns the tool definition
func (t *NavigateTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_navigate",
		Description: "Navigate to a URL in the browser. Returns the final URL and page title after navigation.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "The URL to navigate to",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Navigation timeout in milliseconds (default: 60000)",
				},
			},
			Required: []string{"url"},
		},
	}
}

// Handler returns the tool handler function
func (t *NavigateTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		url, err := getStringArg(args, "url", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		timeout, _ := getIntArg(args, "timeout", 0)

		t.logger.Debug("executing navigate",
			zap.String("url", url),
			zap.Int("timeout", timeout))

		result, err := t.browserManager.Navigate(ctx, shared.NavigateRequest{
			URL:     url,
			Timeout: timeout,
		})
		if err != nil {
			t.logger.Error("navigate failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// GoBackTool handles go back requests
type GoBackTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGoBackTool creates a new go back tool
func NewGoBackTool(browserManager *business.BrowserManager, logger *zap.Logger) *GoBackTool {
	return &GoBackTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-go-back"),
	}
}

// Definition returns the tool definition
func (t *GoBackTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_go_back",
		Description: "Navigate back in browser history",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *GoBackTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing go back")

		if err := t.browserManager.GoBack(ctx); err != nil {
			t.logger.Error("go back failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"success": true,
			"message": "navigated back",
		})
	}
}

// GoForwardTool handles go forward requests
type GoForwardTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGoForwardTool creates a new go forward tool
func NewGoForwardTool(browserManager *business.BrowserManager, logger *zap.Logger) *GoForwardTool {
	return &GoForwardTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-go-forward"),
	}
}

// Definition returns the tool definition
func (t *GoForwardTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_go_forward",
		Description: "Navigate forward in browser history",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *GoForwardTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing go forward")

		if err := t.browserManager.GoForward(ctx); err != nil {
			t.logger.Error("go forward failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"success": true,
			"message": "navigated forward",
		})
	}
}

// ReloadTool handles reload requests
type ReloadTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewReloadTool creates a new reload tool
func NewReloadTool(browserManager *business.BrowserManager, logger *zap.Logger) *ReloadTool {
	return &ReloadTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-reload"),
	}
}

// Definition returns the tool definition
func (t *ReloadTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_reload",
		Description: "Reload the current page",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *ReloadTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing reload")

		if err := t.browserManager.Reload(ctx); err != nil {
			t.logger.Error("reload failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(map[string]interface{}{
			"success": true,
			"message": "page reloaded",
		})
	}
}

// GetPageInfoTool handles page info requests
type GetPageInfoTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGetPageInfoTool creates a new get page info tool
func NewGetPageInfoTool(browserManager *business.BrowserManager, logger *zap.Logger) *GetPageInfoTool {
	return &GetPageInfoTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-get-page-info"),
	}
}

// Definition returns the tool definition
func (t *GetPageInfoTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_get_page_info",
		Description: "Get information about the current page including URL, title, and viewport dimensions",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

// Handler returns the tool handler function
func (t *GetPageInfoTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing get page info")

		result, err := t.browserManager.GetPageInfo(ctx)
		if err != nil {
			t.logger.Error("get page info failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
