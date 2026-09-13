package mcp

import (
	"context"
	"os"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// StatsTool implements memory.stats.
type StatsTool struct {
	storage         storage.Manager
	cfg             *config.Config
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

func NewStatsTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *StatsTool {
	return &StatsTool{storage: storage, cfg: cfg, responseWrapper: wrapper, logger: logger.Named("memory.stats")}
}

func (t *StatsTool) Name() string {
	return "memory.stats"
}

func (t *StatsTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.stats",
		Description: "Return database stats",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"includeTableCounts": map[string]interface{}{"type": "boolean", "default": false},
			},
		},
	}
}

func (t *StatsTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID           string `json:"tenant_id"`
			IncludeTableCounts bool   `json:"includeTableCounts"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		stats := map[string]any{"tenantId": tenantID}

		if path := t.storage.TenantPath(tenantID); path != "" {
			if info, err := os.Stat(path); err == nil {
				stats["dbSizeBytes"] = info.Size()
			}
		}

		stats["tablesAndViews"] = countQuery(ctx, db, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='main'")

		if args.IncludeTableCounts {
			stats["entities"] = countQuery(ctx, db, "SELECT COUNT(*) FROM entities")
			stats["facts"] = countQuery(ctx, db, "SELECT COUNT(*) FROM facts")
			stats["documents"] = countQuery(ctx, db, "SELECT COUNT(*) FROM documents")
			stats["links"] = countQuery(ctx, db, "SELECT COUNT(*) FROM links")
		}

		if qr, err := db.QueryContext(ctx, "SELECT MAX(timestamp) FROM sources"); err == nil && len(qr.Rows) > 0 {
			for _, v := range qr.Rows[0] {
				if t, ok := v.(time.Time); ok && !t.IsZero() {
					stats["lastIngestion"] = t.UTC().Format(time.RFC3339)
				} else if s, ok := v.(string); ok && s != "" {
					stats["lastIngestion"] = s
				}
				break
			}
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := "memory.stats"
			if tenantID != "" {
				summary = "memory.stats for " + truncateForSummary(tenantID, 80)
			}
			return t.responseWrapper.WrapToolResult(ctx, t.Name(), stats, summary)
		}

		return structuredResult(stats), nil
	}
}
