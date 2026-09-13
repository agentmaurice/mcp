package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// GetMarkdownTool handles markdown extraction requests
type GetMarkdownTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewGetMarkdownTool creates a new get markdown tool
func NewGetMarkdownTool(browserManager *business.BrowserManager, logger *zap.Logger) *GetMarkdownTool {
	return &GetMarkdownTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-get-markdown"),
	}
}

// Definition returns the tool definition
func (t *GetMarkdownTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_get_markdown",
		Description: "Extract the current page content as clean Markdown. Useful for reading articles, documentation, or any text-heavy page in a format optimized for LLM consumption.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"strategy": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"auto", "article", "dom", "accessibility"},
					"description": "Extraction strategy: 'auto' (recommended, automatically detects best approach), 'article' (focuses on main content), 'dom' (full page structure), 'accessibility' (uses accessibility tree)",
				},
				"include_metadata": map[string]interface{}{
					"type":        "boolean",
					"description": "Include page metadata like title, URL, language (default: true)",
				},
				"include_links": map[string]interface{}{
					"type":        "boolean",
					"description": "Include hyperlinks in markdown format (default: true)",
				},
				"include_tables": map[string]interface{}{
					"type":        "boolean",
					"description": "Convert HTML tables to markdown tables (default: true)",
				},
				"max_length": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum length of returned markdown in characters (optional, 0 = no limit)",
				},
			},
		},
	}
}

// Handler returns the tool handler function
func (t *GetMarkdownTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		strategy, _ := getStringArg(args, "strategy", false)
		includeMetadata, _ := getBoolArg(args, "include_metadata", true)
		includeLinks, _ := getBoolArg(args, "include_links", true)
		includeTables, _ := getBoolArg(args, "include_tables", true)
		maxLength, _ := getIntArg(args, "max_length", 0)

		t.logger.Debug("executing get markdown",
			zap.String("strategy", strategy),
			zap.Bool("include_metadata", includeMetadata),
			zap.Bool("include_links", includeLinks),
			zap.Bool("include_tables", includeTables),
			zap.Int("max_length", maxLength))

		result, err := t.browserManager.GetMarkdown(ctx, shared.GetMarkdownRequest{
			Strategy:        strategy,
			IncludeMetadata: includeMetadata,
			IncludeLinks:    includeLinks,
			IncludeTables:   includeTables,
			MaxLength:       maxLength,
		})
		if err != nil {
			t.logger.Error("get markdown failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		return createTextResult(result)
	}
}
