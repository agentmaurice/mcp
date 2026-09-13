package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// SwitchFrameTool handles frame switching requests
type SwitchFrameTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

func NewSwitchFrameTool(browserManager *business.BrowserManager, logger *zap.Logger) *SwitchFrameTool {
	return &SwitchFrameTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-switch-frame"),
	}
}

func (t *SwitchFrameTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_switch_frame",
		Description: "Switch execution context to an iframe or return to the top frame. Call with no arguments to reset to top frame.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector of the iframe element",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1')",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "name or id attribute of the iframe",
				},
			},
		},
	}
}

func (t *SwitchFrameTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		name, _ := getStringArg(args, "name", false)

		t.logger.Debug("executing switch_frame",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.String("name", name))

		result, err := t.browserManager.SwitchFrame(ctx, shared.SwitchFrameRequest{
			Selector: selector,
			Ref:      ref,
			Name:     name,
		})
		if err != nil {
			t.logger.Error("switch_frame failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// ListFramesTool handles frame listing requests
type ListFramesTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

func NewListFramesTool(browserManager *business.BrowserManager, logger *zap.Logger) *ListFramesTool {
	return &ListFramesTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-list-frames"),
	}
}

func (t *ListFramesTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_list_frames",
		Description: "List all frames (iframes) on the current page with their selectors, names, URLs and visibility.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

func (t *ListFramesTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("executing list_frames")

		result, err := t.browserManager.ListFrames(ctx)
		if err != nil {
			t.logger.Error("list_frames failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
