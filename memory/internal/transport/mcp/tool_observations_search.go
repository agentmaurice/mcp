package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type ObservationsSearchTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewObservationsSearchTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *ObservationsSearchTool {
	return &ObservationsSearchTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("memory.observations.search")}
}

func (t *ObservationsSearchTool) Name() string {
	return "memory.observations.search"
}

func (t *ObservationsSearchTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.observations.search",
		Description: "Search derived observation facts (`observation.*`) through the v_observations view.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{"type": "string"},
				"query":     map[string]interface{}{"type": "string"},
				"fact_types": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
				"limit": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"maximum": 200,
					"default": 20,
				},
			},
		},
	}
}

func (t *ObservationsSearchTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID  string   `json:"tenant_id"`
			Query     string   `json:"query"`
			FactTypes []string `json:"fact_types"`
			Limit     int      `json:"limit"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		whereClauses := []string{"1=1"}
		queryArgs := make([]any, 0, 4)

		if strings.TrimSpace(args.Query) != "" {
			whereClauses = append(whereClauses, "LOWER(COALESCE(title, '') || ' ' || COALESCE(summary, '') || ' ' || COALESCE(fact_type, '')) LIKE ?")
			queryArgs = append(queryArgs, "%"+strings.ToLower(strings.TrimSpace(args.Query))+"%")
		}

		if len(args.FactTypes) > 0 {
			placeholders := make([]string, 0, len(args.FactTypes))
			for _, factType := range args.FactTypes {
				placeholders = append(placeholders, "?")
				queryArgs = append(queryArgs, factType)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("fact_type IN (%s)", strings.Join(placeholders, ",")))
		}

		queryArgs = append(queryArgs, limit)
		sql := fmt.Sprintf(`
			SELECT fact_id, fact_type, effective_at, title, summary, confidence, labels, subject_entity_id, subject_name, source_system
			FROM v_observations
			WHERE %s
			ORDER BY effective_at DESC, fact_id DESC
			LIMIT ?
		`, strings.Join(whereClauses, " AND "))

		result, err := db.QueryContext(ctx, rebindQuery(t.storage, sql), queryArgs...)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		items := make([]map[string]any, 0, len(result.Rows))
		for _, row := range result.Rows {
			items = append(items, map[string]any{
				"fact_id":           row["fact_id"],
				"fact_type":         row["fact_type"],
				"effective_at":      row["effective_at"],
				"title":             row["title"],
				"summary":           row["summary"],
				"confidence":        row["confidence"],
				"labels":            parseJSONValue(row["labels"]),
				"subject_entity_id": row["subject_entity_id"],
				"subject_name":      row["subject_name"],
				"source_system":     row["source_system"],
			})
		}

		payload := map[string]any{
			"query":      args.Query,
			"limit":      limit,
			"count":      len(items),
			"items":      items,
			"fact_types": args.FactTypes,
		}

		if t.wrapper != nil && t.wrapper.IsEnabled() {
			summary := fmt.Sprintf("memory.observations.search result for: %s", truncateForSummary(args.Query, 80))
			return t.wrapper.WrapToolResult(ctx, t.Name(), payload, summary)
		}

		return structuredResult(payload), nil
	}
}
