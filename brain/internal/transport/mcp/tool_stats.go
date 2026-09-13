package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type StatsTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewStatsTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *StatsTool {
	return &StatsTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.stats")}
}

func (t *StatsTool) Name() string { return "brain.stats" }

func (t *StatsTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.stats",
		Description: "Get storage and indexation statistics for the tenant.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{"type": "string"},
			},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *StatsTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			TenantID string `json:"tenant_id"`
		}
		_ = parseArgs(request.Params.Arguments, &args)

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		embeddingProvider := strings.ToLower(strings.TrimSpace(t.cfg.Embedding.Provider))
		embeddingModel := t.cfg.Embedding.Model
		if embeddingProvider == "" || embeddingProvider == "none" {
			embeddingModel = ""
		}
		stats := map[string]interface{}{
			"tenant_id":          tenantID,
			"backend":            t.storage.Backend(),
			"embedding_provider": embeddingProvider,
			"embedding_model":    embeddingModel,
		}

		stats["source_count"] = countQuery(ctx, db, "SELECT COUNT(*) FROM sources WHERE tenant_id = ?", tenantID)
		stats["document_count"] = countQuery(ctx, db, "SELECT COUNT(*) FROM documents WHERE tenant_id = ?", tenantID)
		stats["chunk_count"] = countQuery(ctx, db, "SELECT COUNT(*) FROM chunks WHERE tenant_id = ?", tenantID)
		stats["embedded_chunk_count"] = countQuery(ctx, db, "SELECT COUNT(*) FROM chunks WHERE tenant_id = ? AND embedding IS NOT NULL", tenantID)

		return t.wrapper.Wrap(stats), nil
	}
}

func countQuery(ctx context.Context, q storage.Querier, query string, args ...any) int {
	qr, err := q.QueryContext(ctx, query, args...)
	if err != nil || len(qr.Rows) == 0 {
		return 0
	}
	for _, v := range qr.Rows[0] {
		switch n := v.(type) {
		case int64:
			return int(n)
		case float64:
			return int(n)
		case int:
			return n
		default:
			var i int
			fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &i)
			return i
		}
	}
	return 0
}
