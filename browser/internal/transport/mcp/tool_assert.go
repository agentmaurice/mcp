package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// AssertTool handles DOM assertion requests
type AssertTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

func NewAssertTool(browserManager *business.BrowserManager, logger *zap.Logger) *AssertTool {
	return &AssertTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-assert"),
	}
}

func (t *AssertTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_assert",
		Description: "Assert a condition on the page. Synchronous check, no polling. Supports: visible, hidden, text_contains, text_equals, attribute_equals, url_contains, title_contains, element_count.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"assertion": map[string]interface{}{
					"type":        "string",
					"description": "Assertion type: visible, hidden, text_contains, text_equals, attribute_equals, url_contains, title_contains, element_count",
				},
				"selector": map[string]interface{}{
					"type":        "string",
					"description": "CSS selector (required for element-based assertions, not needed for url_contains/title_contains)",
				},
				"ref": map[string]interface{}{
					"type":        "string",
					"description": "Element reference from browser_snapshot (e.g., '@e1'). Alternative to selector.",
				},
				"expected": map[string]interface{}{
					"type":        "string",
					"description": "Expected value for comparison assertions",
				},
				"attribute": map[string]interface{}{
					"type":        "string",
					"description": "Attribute name (required for attribute_equals)",
				},
			},
			Required: []string{"assertion"},
		},
	}
}

func (t *AssertTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		assertion, err := getStringArg(args, "assertion", true)
		if err != nil {
			return createErrorResult(err), nil
		}

		selector, _ := getStringArg(args, "selector", false)
		ref, _ := getStringArg(args, "ref", false)
		expected, _ := getStringArg(args, "expected", false)
		attribute, _ := getStringArg(args, "attribute", false)

		t.logger.Debug("executing assert",
			zap.String("assertion", assertion),
			zap.String("selector", selector),
			zap.String("ref", ref),
			zap.String("expected", expected))

		result, err := t.browserManager.Assert(ctx, shared.AssertRequest{
			Assertion: assertion,
			Selector:  selector,
			Ref:       ref,
			Expected:  expected,
			Attribute: attribute,
		})
		if err != nil {
			t.logger.Error("assert failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
