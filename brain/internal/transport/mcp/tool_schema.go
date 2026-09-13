package mcp

import (
	"context"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type SchemaTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewSchemaTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *SchemaTool {
	return &SchemaTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.schema")}
}

func (t *SchemaTool) Name() string { return "brain.schema" }

func (t *SchemaTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.schema",
		Description: "Describe the indexed content schema: document types, languages, chunk types, and sources present.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{"type": "string"},
			},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *SchemaTool) Handler() ToolHandler {
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

		schema := map[string]interface{}{}

		// Document types
		docTypes := queryDistinct(ctx, db, "SELECT DISTINCT doc_type FROM documents WHERE tenant_id = ?", tenantID)
		schema["doc_types"] = docTypes

		// Languages
		languages := queryDistinct(ctx, db, "SELECT DISTINCT language FROM documents WHERE tenant_id = ? AND language IS NOT NULL AND language != ''", tenantID)
		schema["languages"] = languages

		// Chunk types
		chunkTypes := queryDistinct(ctx, db, "SELECT DISTINCT chunk_type FROM chunks WHERE tenant_id = ?", tenantID)
		schema["chunk_types"] = chunkTypes

		// Sources
		sourceTypes := queryDistinct(ctx, db, "SELECT DISTINCT source_type FROM sources WHERE tenant_id = ?", tenantID)
		schema["source_types"] = sourceTypes

		return t.wrapper.Wrap(schema), nil
	}
}

func queryDistinct(ctx context.Context, db storage.Querier, query, tenantID string) []string {
	qr, err := db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil
	}

	var values []string
	for _, row := range qr.Rows {
		for _, v := range row {
			if v != nil {
				values = append(values, fmt.Sprintf("%v", v))
			}
			break
		}
	}
	return values
}
