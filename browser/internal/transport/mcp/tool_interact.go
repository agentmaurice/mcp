package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// HoverTool handles hover requests
type HoverTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewHoverTool creates a new hover tool
func NewHoverTool(browserManager *business.BrowserManager, logger *zap.Logger) *HoverTool {
	return &HoverTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-hover"),
	}
}

// Definition returns the tool definition
func (t *HoverTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_hover",
		Description: "Hover over an element to trigger :hover styles or tooltips",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the element to hover over. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *HoverTool) Handler() server.ToolHandlerFunc {
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

		t.logger.Debug("executing hover",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Int("timeout", timeout))

		result, err := t.browserManager.Hover(ctx, shared.HoverRequest{
			Selector: selector,
			Ref:      ref,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("hover failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// DragDropTool handles drag-and-drop requests
type DragDropTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

func NewDragDropTool(browserManager *business.BrowserManager, logger *zap.Logger) *DragDropTool {
	return &DragDropTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-drag-drop"),
	}
}

func (t *DragDropTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_drag_drop",
		Description: "Drag and drop one element onto another. Uses CDP mouse events with HTML5 drag API fallback.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"source_selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the source element. Optional if source_ref is provided.",
				},
				"source_ref": map[string]interface{}{
					"type":        "string",
					"description": "Snapshot ref for the source element (e.g., '@e1'). Optional if source_selector is provided.",
				},
				"target_selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the target element. Optional if target_ref is provided.",
				},
				"target_ref": map[string]interface{}{
					"type":        "string",
					"description": "Snapshot ref for the target element (e.g., '@e2'). Optional if target_ref is provided.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
				},
			},
		},
	}
}

func (t *DragDropTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		sourceSelector, _ := getStringArg(args, "source_selector", false)
		sourceRef, _ := getStringArg(args, "source_ref", false)
		targetSelector, _ := getStringArg(args, "target_selector", false)
		targetRef, _ := getStringArg(args, "target_ref", false)

		if (sourceSelector == "" && sourceRef == "") || (targetSelector == "" && targetRef == "") {
			return createErrorResult(shared.ErrValidation("source and target required (selector or ref)")), nil
		}

		t.logger.Debug("executing drag_drop",
			zap.String("source_selector", sourceSelector),
			zap.String("source_ref", sourceRef),
			zap.String("target_selector", targetSelector),
			zap.String("target_ref", targetRef))

		result, err := t.browserManager.DragDrop(ctx, shared.DragDropRequest{
			SourceSelector: sourceSelector,
			SourceRef:      sourceRef,
			TargetSelector: targetSelector,
			TargetRef:      targetRef,
		})
		if err != nil {
			t.logger.Error("drag_drop failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// PressKeyTool handles press key requests
type PressKeyTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewPressKeyTool creates a new press key tool
func NewPressKeyTool(browserManager *business.BrowserManager, logger *zap.Logger) *PressKeyTool {
	return &PressKeyTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-press-key"),
	}
}

// Definition returns the tool definition
func (t *PressKeyTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_press_key",
		Description: "Send keyboard input. Supports modifier combos like 'Control+a', 'Shift+Enter'.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]interface{}{
					"type":        "string",
					"description": "Key name or combo (e.g., 'Enter', 'Tab', 'Control+a', 'Shift+Enter')",
				},
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector to focus before sending key. Optional.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional.",
				},
				"repeat": map[string]interface{}{
					"type":        "integer",
					"description": "Number of times to repeat the key press (max 100, default 1)",
				},
			},
			Required: []string{"key"},
		},
	}
}

// Handler returns the tool handler function
func (t *PressKeyTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		key, err := getStringArg(args, "key", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		repeat, _ := getIntArg(args, "repeat", 1)

		t.logger.Debug("executing press_key",
			zap.String("key", key),
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Int("repeat", repeat))

		result, err := t.browserManager.PressKey(ctx, shared.PressKeyRequest{
			Key:      key,
			Selector: selector,
			Ref:      ref,
			Repeat:   repeat,
		})
		if err != nil {
			t.logger.Error("press_key failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
