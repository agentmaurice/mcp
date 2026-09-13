package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var identifierReSimple = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IndexesEnsureTool implements memory.indexes.ensure.
type IndexesEnsureTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewIndexesEnsureTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *IndexesEnsureTool {
	return &IndexesEnsureTool{storage: storage, cfg: cfg, logger: logger.Named("memory.indexes.ensure")}
}

func (t *IndexesEnsureTool) Name() string {
	return "memory.indexes.ensure"
}

func (t *IndexesEnsureTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.indexes.ensure",
		Description: "Ensure indexes for app-scoped objects (whitelisted)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"appId": map[string]interface{}{"type": "string", "minLength": 1},
				"items": map[string]interface{}{
					"type":     "array",
					"minItems": 1,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"indexName": map[string]interface{}{"type": "string", "minLength": 1},
							"table":     map[string]interface{}{"type": "string", "minLength": 1},
							"columns": map[string]interface{}{
								"type":     "array",
								"items":    map[string]interface{}{"type": "string"},
								"minItems": 1,
							},
						},
						"required": []string{"indexName", "table", "columns"},
					},
				},
			},
			Required: []string{"appId", "items"},
		},
	}
}

func (t *IndexesEnsureTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID string `json:"tenant_id"`
			AppID    string `json:"appId"`
			Items    []struct {
				IndexName string   `json:"indexName"`
				Table     string   `json:"table"`
				Columns   []string `json:"columns"`
			} `json:"items"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if args.AppID == "" || len(args.Items) == 0 {
			return errorResult("appId and items are required"), nil
		}
		if len(t.cfg.Security.AllowedIndexes) == 0 {
			return errorResult("indexes are not enabled"), nil
		}

		prefix := "app_" + args.AppID + "__"

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		var ensured int
		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			for _, item := range args.Items {
				if item.IndexName == "" || item.Table == "" {
					return fmt.Errorf("indexName and table are required")
				}
				if !strings.HasPrefix(item.IndexName, prefix) || !strings.HasPrefix(item.Table, prefix) {
					return fmt.Errorf("indexName and table must be scoped to appId")
				}
				if !security.IsAllowedObject(item.IndexName, t.cfg.Security.AllowedIndexes) {
					return fmt.Errorf("index %s is not allowed", item.IndexName)
				}
				for _, col := range item.Columns {
					if !identifierReSimple.MatchString(col) {
						return fmt.Errorf("invalid column name")
					}
				}
				cols := strings.Join(item.Columns, ",")
				stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", item.IndexName, item.Table, cols)
				if _, err := q.ExecContext(ctx, stmt); err != nil {
					return err
				}
				ensured++
			}
			return nil
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(map[string]any{"ensured": ensured}), nil
	}
}
