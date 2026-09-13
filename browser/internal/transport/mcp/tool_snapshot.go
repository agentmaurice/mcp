package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/browser/internal/shared"
)

// SnapshotTool handles compact semantic snapshots.
type SnapshotTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewSnapshotTool creates a new snapshot tool.
func NewSnapshotTool(browserManager *business.BrowserManager, logger *zap.Logger) *SnapshotTool {
	return &SnapshotTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-snapshot"),
	}
}

// Definition returns the tool definition.
func (t *SnapshotTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_snapshot",
		Description: "Capture a compact semantic snapshot of interactive page elements and return reusable refs like @e1, @e2.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"format": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"compact", "json"},
					"description": "Output format (default: compact). Use json for structured consumption.",
				},
				"max_elements": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of elements in the snapshot (default: 200).",
				},
				"include_hidden": map[string]interface{}{
					"type":        "boolean",
					"description": "Include hidden elements (default: false).",
				},
			},
		},
	}
}

// Handler returns the tool handler function.
func (t *SnapshotTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		format, _ := getStringArg(args, "format", false)
		if format == "" {
			format = "compact"
		}
		maxElements, _ := getIntArg(args, "max_elements", 200)
		includeHidden, _ := getBoolArg(args, "include_hidden", false)

		result, err := t.browserManager.Snapshot(ctx, shared.SnapshotRequest{
			Format:        format,
			MaxElements:   maxElements,
			IncludeHidden: includeHidden,
		})
		if err != nil {
			t.logger.Error("snapshot failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		if format == "json" {
			return createTextResult(result)
		}

		return createTextResult(formatSnapshotCompact(result))
	}
}

func formatSnapshotCompact(result *shared.SnapshotResult) string {
	if result == nil {
		return "snapshot failed: empty result"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "snapshot_id=%s url=%s title=%q count=%d total=%d truncated=%t\n",
		result.SnapshotID, result.URL, result.Title, len(result.Elements), result.Total, result.Truncated)

	for _, el := range result.Elements {
		label := strings.TrimSpace(el.Name)
		if label == "" {
			label = strings.TrimSpace(el.Text)
		}
		fmt.Fprintf(&b, "%s role=%s name=%q selector=%q\n", el.Ref, el.Role, label, el.Selector)
	}

	return strings.TrimSpace(b.String())
}

// FindTool handles semantic search in the current snapshot.
type FindTool struct {
	browserManager *business.BrowserManager
	logger         *zap.Logger
}

// NewFindTool creates a new find tool.
func NewFindTool(browserManager *business.BrowserManager, logger *zap.Logger) *FindTool {
	return &FindTool{
		browserManager: browserManager,
		logger:         logger.Named("tool-find"),
	}
}

// Definition returns the tool definition.
func (t *FindTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "browser_find",
		Description: "Find semantic elements from the current snapshot (role, text, label, placeholder, testid, title, alt).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"by": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"role", "text", "label", "placeholder", "testid", "title", "alt"},
					"description": "Find mode.",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "Query value. For by=role this is the role value (e.g. textbox, button).",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Optional name filter used with by=role (e.g. Email).",
				},
				"exact": map[string]interface{}{
					"type":        "boolean",
					"description": "Require exact match (default: false).",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of matches (default: 10).",
				},
				"format": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"compact", "json"},
					"description": "Output format (default: compact).",
				},
			},
			Required: []string{"by", "value"},
		},
	}
}

// Handler returns the tool handler function.
func (t *FindTool) Handler() server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := getArgsMap(request.Params.Arguments)
		if err != nil {
			return createErrorResult(err), nil
		}

		by, err := getStringArg(args, "by", true)
		if err != nil {
			return createErrorResult(err), nil
		}
		value, err := getStringArg(args, "value", true)
		if err != nil {
			return createErrorResult(err), nil
		}
		name, _ := getStringArg(args, "name", false)
		exact, _ := getBoolArg(args, "exact", false)
		maxResults, _ := getIntArg(args, "max_results", 10)
		format, _ := getStringArg(args, "format", false)
		if format == "" {
			format = "compact"
		}

		result, err := t.browserManager.Find(ctx, shared.FindRequest{
			By:         by,
			Value:      value,
			Name:       name,
			Exact:      exact,
			MaxResults: maxResults,
		})
		if err != nil {
			t.logger.Error("find failed", zap.Error(err))
			return createErrorResult(err), nil
		}

		if format == "json" {
			return createTextResult(result)
		}
		return createTextResult(formatFindCompact(result))
	}
}

func formatFindCompact(result *shared.FindResult) string {
	if result == nil {
		return "find failed: empty result"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "query by=%s value=%q name=%q count=%d\n", result.By, result.Value, result.Name, result.Count)

	for _, m := range result.Matches {
		fmt.Fprintf(&b, "%s score=%.2f role=%s name=%q selector=%q\n", m.Ref, m.Score, m.Role, m.Name, m.Selector)
	}
	if result.Count == 0 {
		b.WriteString("no matches")
	}

	return strings.TrimSpace(b.String())
}
