package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// SchemaDescribeTool implements memory.schema.describe.
type SchemaDescribeTool struct {
	storage         storage.Manager
	cfg             *config.Config
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

func NewSchemaDescribeTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *SchemaDescribeTool {
	return &SchemaDescribeTool{storage: storage, cfg: cfg, responseWrapper: wrapper, logger: logger.Named("memory.schema.describe")}
}

func (t *SchemaDescribeTool) Name() string {
	return "memory.schema.describe"
}

func (t *SchemaDescribeTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.schema.describe",
		Description: "Describe tables/views and their columns",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Filter like/glob",
				},
				"includeViews": map[string]interface{}{
					"type":    "boolean",
					"default": true,
				},
				"includeTables": map[string]interface{}{
					"type":    "boolean",
					"default": true,
				},
			},
		},
	}
}

func (t *SchemaDescribeTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID      string `json:"tenant_id"`
			Pattern       string `json:"pattern"`
			IncludeViews  *bool  `json:"includeViews"`
			IncludeTables *bool  `json:"includeTables"`
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

		pattern := strings.TrimSpace(args.Pattern)
		if pattern == "" {
			pattern = "%"
		} else {
			pattern = strings.ReplaceAll(pattern, "*", "%")
		}

		includeViews := true
		if args.IncludeViews != nil {
			includeViews = *args.IncludeViews
		}
		includeTables := true
		if args.IncludeTables != nil {
			includeTables = *args.IncludeTables
		}

		dialect := ""
		if t.storage != nil {
			dialect = strings.ToLower(t.storage.Dialect())
		}
		schemaClause := "table_schema='main'"
		if dialect == "postgres" {
			schemaClause = "table_schema = current_schema()"
		}
		query := fmt.Sprintf("SELECT table_name, table_type FROM information_schema.tables WHERE %s AND table_name LIKE ?", schemaClause)
		if !includeTables && !includeViews {
			return structuredResult(map[string]any{"objects": []any{}}), nil
		}
		if !includeTables {
			query += " AND table_type = 'VIEW'"
		} else if !includeViews {
			query += " AND table_type = 'BASE TABLE'"
		}
		query += " ORDER BY table_name"

		qr, err := db.QueryContext(ctx, rebindQuery(t.storage, query), pattern)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		type schemaObject struct {
			Name    string          `json:"name"`
			Type    string          `json:"type"`
			Columns []shared.Column `json:"columns"`
		}
		var objects []schemaObject

		for _, row := range qr.Rows {
			name, _ := row["table_name"].(string)
			objType, _ := row["table_type"].(string)
			if name == "" {
				continue
			}
			lowerName := strings.ToLower(name)
			if t.cfg.Security.RequireViewsOnly && !(strings.HasPrefix(lowerName, "v_") || strings.HasPrefix(lowerName, "app_")) {
				continue
			}
			if !security.IsAllowedObject(name, t.cfg.Security.AllowedObjects) {
				continue
			}

			cols, err := describeColumns(ctx, t.storage, db, name, t.cfg.Security.AllowedColumns)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			objects = append(objects, schemaObject{Name: name, Type: strings.ToLower(objType), Columns: cols})
		}

		response := map[string]any{"objects": objects}
		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summaryPattern := pattern
			if summaryPattern == "%" {
				summaryPattern = "*"
			}
			summary := fmt.Sprintf("memory.schema.describe pattern=%s", truncateForSummary(summaryPattern, 80))
			return t.responseWrapper.WrapToolResult(ctx, t.Name(), response, summary)
		}

		return structuredResult(response), nil
	}
}

func describeColumns(ctx context.Context, manager storage.Manager, q storage.Querier, table string, allowed map[string][]string) ([]shared.Column, error) {
	dialect := ""
	if manager != nil {
		dialect = strings.ToLower(manager.Dialect())
	}
	schemaClause := "table_schema='main'"
	if dialect == "postgres" {
		schemaClause = "table_schema = current_schema()"
	}
	query := fmt.Sprintf("SELECT column_name, data_type FROM information_schema.columns WHERE %s AND table_name = ? ORDER BY ordinal_position", schemaClause)
	qr, err := q.QueryContext(ctx, rebindQuery(manager, query), table)
	if err != nil {
		return nil, err
	}

	allowedSet := make(map[string]struct{})
	if allowed != nil {
		if list, ok := allowed[table]; ok {
			for _, col := range list {
				allowedSet[strings.ToLower(col)] = struct{}{}
			}
		}
	}

	var cols []shared.Column
	for _, row := range qr.Rows {
		name, _ := row["column_name"].(string)
		dataType, _ := row["data_type"].(string)
		if name == "" {
			continue
		}
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[strings.ToLower(name)]; !ok {
				continue
			}
		}
		cols = append(cols, shared.Column{Name: name, Type: dataType})
	}
	return cols, nil
}
