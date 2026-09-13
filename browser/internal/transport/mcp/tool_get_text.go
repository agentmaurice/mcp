package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// GetTextTool handles get text requests
type GetTextTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGetTextTool creates a new get text tool
func NewGetTextTool(browserManager *business.BrowserManager, logger *zap.Logger) *GetTextTool {
	return &GetTextTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-get-text"),
	}
}

// Definition returns the tool definition
func (t *GetTextTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_get_text",
		Description: "Extract the text content of an element. Returns the visible text within the element and its children.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the element (e.g., 'article', '.content', '#main-text'). Optional if ref is provided.",
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
func (t *GetTextTool) Handler() server.ToolHandlerFunc {
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

		t.logger.Debug("executing get text",
			zap.String("selector", selector),
			zap.String("ref", ref))

		result, err := t.browserManager.GetText(ctx, shared.GetTextRequest{
			Selector: selector,
			Ref:      ref,
			Timeout:  timeout,
		})
		if err != nil {
			t.logger.Error("get text failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// GetHTMLTool handles get HTML requests
type GetHTMLTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGetHTMLTool creates a new get HTML tool
func NewGetHTMLTool(browserManager *business.BrowserManager, logger *zap.Logger) *GetHTMLTool {
	return &GetHTMLTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-get-html"),
	}
}

// Definition returns the tool definition
func (t *GetHTMLTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_get_html",
		Description: "Extract the HTML content of an element or the entire page",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the element. If empty, returns full page HTML.",
				},
				"outer": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to include the element's outer HTML (default: false for inner HTML)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *GetHTMLTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		outer, _ := getBoolArg(args, "outer", false)

		t.logger.Debug("executing get HTML",
			zap.String("selector", selector),
			zap.Bool("outer", outer))

		result, err := t.browserManager.GetHTML(ctx, shared.GetHTMLRequest{
			Selector: selector,
			Outer:    outer,
		})
		if err != nil {
			t.logger.Error("get HTML failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}

// GetAttributeTool handles get attribute requests
type GetAttributeTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGetAttributeTool creates a new get attribute tool
func NewGetAttributeTool(browserManager *business.BrowserManager, logger *zap.Logger) *GetAttributeTool {
	return &GetAttributeTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-get-attribute"),
	}
}

// Definition returns the tool definition
func (t *GetAttributeTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_get_attribute",
		Description: "Get the value of an attribute from an element",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector for the element. Optional if ref is provided.",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Optional if selector is provided.",
				},
				"attribute": map[string]interface{}{
					"type":        "string",
					"description": "The attribute name to get (e.g., 'href', 'src', 'data-id')",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
				},
			},
			Required: []string{"attribute"},
		},
	}
}

// Handler returns the tool handler function
func (t *GetAttributeTool) Handler() server.ToolHandlerFunc {
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

		attribute, err := getStringArg(args, "attribute", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		timeout, _ := getIntArg(args, "timeout", 0)

		t.logger.Debug("executing get attribute",
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.String("attribute", attribute))

		result, err := t.browserManager.GetAttribute(ctx, shared.GetAttributeRequest{
			Selector:  selector,
			Ref:       ref,
			Attribute: attribute,
			Timeout:   timeout,
		})
		if err != nil {
			t.logger.Error("get attribute failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
