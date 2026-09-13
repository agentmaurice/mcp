package mcp

import (
	"context"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type SourcesTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewSourcesTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *SourcesTool {
	return &SourcesTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.sources")}
}

func (t *SourcesTool) Name() string { return "brain.sources" }

func (t *SourcesTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.sources",
		Description: "List all indexed sources with their status and statistics.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{"type": "string"},
			},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *SourcesTool) Handler() ToolHandler {
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

		qr, err := db.QueryContext(ctx, `
			SELECT s.id, s.source_type, s.source_uri, s.display_name, s.status, s.last_indexed_at, s.error_message,
				   COUNT(DISTINCT d.id) AS doc_count,
				   COUNT(DISTINCT c.id) AS chunk_count
			FROM sources s
			LEFT JOIN documents d ON d.source_id = s.id
			LEFT JOIN chunks c ON c.document_id = d.id
			WHERE s.tenant_id = ?
			GROUP BY s.id, s.source_type, s.source_uri, s.display_name, s.status, s.last_indexed_at, s.error_message
			ORDER BY s.created_at DESC
		`, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		var sources []map[string]interface{}
		for _, row := range qr.Rows {
			source := map[string]interface{}{
				"id":             row["id"],
				"source_type":    row["source_type"],
				"source_uri":     row["source_uri"],
				"status":         row["status"],
				"document_count": row["doc_count"],
				"chunk_count":    row["chunk_count"],
			}
			if v := row["display_name"]; v != nil && fmt.Sprintf("%v", v) != "" {
				source["display_name"] = v
			}
			if v := row["last_indexed_at"]; v != nil {
				source["last_indexed_at"] = v
			}
			if v := row["error_message"]; v != nil && fmt.Sprintf("%v", v) != "" {
				source["error_message"] = v
			}
			sources = append(sources, source)
		}

		return t.wrapper.Wrap(map[string]any{"sources": sources}), nil
	}
}
