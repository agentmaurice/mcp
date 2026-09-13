package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// ClickTool handles click requests
type ClickTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewClickTool creates a new click tool
func NewClickTool(browserManager *business.BrowserManager, logger *zap.Logger) *ClickTool {
	return &ClickTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-click"),
	}
}

// Definition returns the tool definition
func (t *ClickTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_click",
		Description: "Click on an element in the page using a CSS selector. The element must be visible and clickable.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the element to click (e.g., 'button.submit', '#login-btn', '[data-testid=\"submit\"]'). Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds to wait for the element (default: 30000)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *ClickTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		if selector == "" && ref == "" {
			return createErrorResult(shared.ErrValidation("either selector or ref is required")), nil
		}

		timeout, _ := getIntArg(args, "timeout", 0)
		t.logger.Debug("executing click",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Int("timeout", timeout))

		result, err := t.browserManager.Click(ctx, shared.ClickRequest{
			Selector: selector,
			Ref:      ref,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("click failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
