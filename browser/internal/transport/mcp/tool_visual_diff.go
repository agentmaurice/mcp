package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// CaptureTool handles capture requests
type CaptureTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewCaptureTool creates a new capture tool
func NewCaptureTool(browserManager *business.BrowserManager, logger *zap.Logger) *CaptureTool {
	return &CaptureTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-capture"),
	}
}

// Definition returns the tool definition
func (t *CaptureTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_capture",
		Description: "Capture a named screenshot for later visual diffing. Store screenshots with unique names to compare them later using browser_visual_diff.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Unique name for this capture (e.g., 'before_changes', 'after_update')",
				},
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for a specific element to capture. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"full_page": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to capture the full scrollable page (default: false)",
				},
			},
			Required: []string{"name"},
		},
	}
}

// Handler returns the tool handler function
func (t *CaptureTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		name, err := getStringArg(args, "name", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		fullPage, _ := getBoolArg(args, "full_page", false)

		t.logger.Debug("executing capture",
			zap.String("name", name),
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Bool("full_page", fullPage))

		result, err := t.browserManager.Capture(ctx, shared.CaptureRequest{
			Name:     name,
			Selector: selector,
			Ref:      ref,
			FullPage: fullPage,
		})
		if err != nil {
			t.logger.Error("capture failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// VisualDiffTool handles visual diff requests
type VisualDiffTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewVisualDiffTool creates a new visual diff tool
func NewVisualDiffTool(browserManager *business.BrowserManager, logger *zap.Logger) *VisualDiffTool {
	return &VisualDiffTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-visual-diff"),
	}
}

// Definition returns the tool definition
func (t *VisualDiffTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_visual_diff",
		Description: "Compare two captured screenshots and detect visual changes. Returns regions of the page that changed, with diff percentage and pixel counts.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"before": map[string]interface{}{
					"type":        "string",
					"description": "Capture ID or name of the 'before' screenshot",
				},
				"after": map[string]interface{}{
					"type":        "string",
					"description": "Capture ID or name of the 'after' screenshot",
				},
				"threshold": map[string]interface{}{
					"type":        "number",
					"description": "Pixel difference threshold 0-100 for detecting changes (default: 10). Higher values ignore minor color differences.",
				},
			},
			Required: []string{"before", "after"},
		},
	}
}

// Handler returns the tool handler function
func (t *VisualDiffTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		before, err := getStringArg(args, "before", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		after, err := getStringArg(args, "after", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		threshold, _ := getFloatArg(args, "threshold", 10.0)

		t.logger.Debug("executing visual diff",
			zap.String("before", before),
			zap.String("after", after),
			zap.Float64("threshold", threshold))

		result, err := t.browserManager.VisualDiff(ctx, shared.VisualDiffRequest{
			Before:    before,
			After:     after,
			Threshold: threshold,
		})
		if err != nil {
			t.logger.Error("visual diff failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
