package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var (
	identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	columnRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// PreviewTool implements memory.preview.
type PreviewTool struct {
	storage         storage.Manager
	cfg             *config.Config
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

func NewPreviewTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *PreviewTool {
	return &PreviewTool{storage: storage, cfg: cfg, responseWrapper: wrapper, logger: logger.Named("memory.preview")}
}

func (t *PreviewTool) Name() string {
	return "memory.preview"
}

func (t *PreviewTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.preview",
		Description: "Preview a table or view with optional filters",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"object": map[string]interface{}{
					"type":      "string",
					"minLength": 1,
				},
				"columns": map[string]interface{}{
					"type":    "array",
					"items":   map[string]interface{}{"type": "string"},
					"default": []string{},
				},
				"where": map[string]interface{}{
					"type":        "string",
					"description": "WHERE clause without WHERE",
				},
				"limit": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"maximum": 500,
					"default": 50,
				},
			},
			Required: []string{"object"},
		},
	}
}

func (t *PreviewTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID string   `json:"tenant_id"`
			Object   string   `json:"object"`
			Columns  []string `json:"columns"`
			Where    string   `json:"where"`
			Limit    int      `json:"limit"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if !identifierRe.MatchString(args.Object) {
			return errorResult("invalid object name"), nil
		}

		selectCols := "*"
		if len(args.Columns) > 0 {
			cols := make([]string, 0, len(args.Columns))
			for _, col := range args.Columns {
				col = strings.TrimSpace(col)
				if col == "" {
					continue
				}
				if !columnRe.MatchString(col) {
					return errorResult(fmt.Sprintf("invalid column name: %s", col)), nil
				}
				cols = append(cols, col)
			}
			if len(cols) > 0 {
				selectCols = strings.Join(cols, ", ")
			}
		}

		query := fmt.Sprintf("SELECT %s FROM %s", selectCols, args.Object)
		if strings.TrimSpace(args.Where) != "" {
			where := strings.TrimSpace(args.Where)
			if strings.Contains(where, ";") {
				return errorResult("invalid where clause"), nil
			}
			query = fmt.Sprintf("%s WHERE %s", query, where)
		}

		if args.Limit <= 0 {
			args.Limit = 50
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		result, err := executeQuery(ctx, t.storage, t.cfg, t.logger, query, nil, args.Limit, t.cfg.Query.TimeoutMsDefault, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("memory.preview for %s", truncateForSummary(args.Object, 80))
			return t.responseWrapper.WrapToolResult(ctx, t.Name(), result, summary)
		}

		return structuredResult(result), nil
	}
}
