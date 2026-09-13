package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// FillTool handles fill requests
type FillTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewFillTool creates a new fill tool
func NewFillTool(browserManager *business.BrowserManager, logger *zap.Logger) *FillTool {
	return &FillTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-fill"),
	}
}

// Definition returns the tool definition
func (t *FillTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_fill",
		Description: "Fill a form field with text. Use for input fields, textareas, and contenteditable elements.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the input element (e.g., 'input[name=\"email\"]', '#username', '.search-box'). Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "The text value to fill in",
				},
				"clear": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to clear the field before typing (default: false)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds to wait for the element (default: 30000)",
				},
			},
			Required: []string{"value"},
		},
	}
}

// Handler returns the tool handler function
func (t *FillTool) Handler() server.ToolHandlerFunc {
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

		value, err := getStringArg(args, "value", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		clear, _ := getBoolArg(args, "clear", false)
		timeout, _ := getIntArg(args, "timeout", 0)

		t.logger.Debug("executing fill",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Bool("clear", clear),
			zap.Int("timeout", timeout))

		result, err := t.browserManager.Fill(ctx, shared.FillRequest{
			Selector: selector,
			Ref:      ref,
			Value:    value,
			Clear:    clear,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("fill failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// SelectOptionTool handles select option requests
type SelectOptionTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewSelectOptionTool creates a new select option tool
func NewSelectOptionTool(browserManager *business.BrowserManager, logger *zap.Logger) *SelectOptionTool {
	return &SelectOptionTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-select-option"),
	}
}

// Definition returns the tool definition
func (t *SelectOptionTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_select_option",
		Description: "Select an option from a dropdown select element",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the select element. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"values": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Array of option values to select",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
				},
			},
			Required: []string{"values"},
		},
	}
}

// Handler returns the tool handler function
func (t *SelectOptionTool) Handler() server.ToolHandlerFunc {
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

		values, err := getStringSliceArg(args, "values", true)
		if err != nil {
			return createErrorResult(err), nil
		}
		if len(values) == 0 {
			return createErrorResult(shared.ErrValidation("values must contain at least one option value")), nil
		}
		for _, value := range values {
			if value == "" {
				return createErrorResult(shared.ErrValidation("values must not contain empty strings")), nil
			}
		}

		timeout, _ := getIntArg(args, "timeout", 0)

		t.logger.Debug("executing select option",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.Strings("values", values))

		result, err := t.browserManager.SelectOption(ctx, shared.SelectOptionRequest{
			Selector: selector,
			Ref:      ref,
			Values:   values,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("select option failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
