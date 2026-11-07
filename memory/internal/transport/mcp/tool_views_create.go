package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/logging"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/security"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var viewNameRe = regexp.MustCompile(`^app_[A-Za-z0-9_]+__[_A-Za-z0-9]+$`)

// ViewsCreateTool implements memory.views.create_or_replace.
type ViewsCreateTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewViewsCreateTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *ViewsCreateTool {
	return &ViewsCreateTool{storage: storage, cfg: cfg, logger: logger.Named("memory.views.create_or_replace")}
}

func (t *ViewsCreateTool) Name() string {
	return "memory.views.create_or_replace"
}

func (t *ViewsCreateTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.views.create_or_replace",
		Description: "Create or replace an app-scoped view (app_<appId>__*)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"appId":    map[string]interface{}{"type": "string", "minLength": 1},
				"viewName": map[string]interface{}{"type": "string", "minLength": 1},
				"sql":      map[string]interface{}{"type": "string", "minLength": 1},
			},
			Required: []string{"appId", "viewName", "sql"},
		},
	}
}

func (t *ViewsCreateTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Info("tool called", zap.String("tool", t.Name()), logging.SizeOf("arguments", request.Params.Arguments))
		logging.DebugValue(t.logger, "tool arguments", "arguments", request.Params.Arguments, zap.String("tool", t.Name()))

		var args struct {
			TenantID string `json:"tenant_id"`
			AppID    string `json:"appId"`
			ViewName string `json:"viewName"`
			SQL      string `json:"sql"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			t.logger.Warn("failed to parse arguments", zap.String("tool", t.Name()), logging.ErrorClass(err), logging.SizeOf("raw_arguments", request.Params.Arguments))
			logging.DebugValue(t.logger, "unparsed arguments", "raw_arguments", request.Params.Arguments, zap.String("tool", t.Name()))
			return errorResult("failed to parse arguments"), nil
		}

		t.logger.Debug("parsed arguments",
			logging.Text("app_id", args.AppID),
			logging.Text("view_name", args.ViewName),
			logging.Size("sql", args.SQL),
		)
		logging.Debug(t.logger, "view SQL", "sql", args.SQL, logging.Text("view_name", args.ViewName))

		args.ViewName = strings.TrimSpace(args.ViewName)
		if args.AppID == "" || args.ViewName == "" || args.SQL == "" {
			t.logger.Warn("missing required fields", logging.Text("app_id", args.AppID), logging.Text("view_name", args.ViewName))
			return errorResult("appId, viewName, and sql are required"), nil
		}
		if !viewNameRe.MatchString(args.ViewName) {
			t.logger.Warn("invalid view name format", logging.Text("view_name", args.ViewName))
			return errorResult("viewName must match app_<appId>__*"), nil
		}
		prefix := "app_" + args.AppID + "__"
		if !strings.HasPrefix(args.ViewName, prefix) {
			t.logger.Warn("view name not scoped to app", logging.Text("view_name", args.ViewName), logging.Text("expected_prefix", prefix))
			return errorResult("viewName must be scoped to appId"), nil
		}

		if err := security.ValidateReadOnly(args.SQL, t.cfg.Security.Denylist); err != nil {
			t.logger.Warn("SQL validation failed: read-only check", logging.ErrorClass(err), logging.Size("sql", args.SQL))
			return errorResult(err.Error()), nil
		}
		info, err := security.ValidatePortableSQL(args.SQL, security.PortableSQLRules{
			AllowSelectStar:    !t.cfg.Query.DisallowSelectStar,
			AllowJSONOperators: false,
		})
		if err != nil {
			t.logger.Warn("SQL validation failed: portable SQL check", logging.ErrorClass(err), logging.Size("sql", args.SQL))
			return errorResult(err.Error()), nil
		}
		if t.cfg.Security.RequireViewsOnly || len(t.cfg.Security.AllowedObjects) > 0 {
			for _, table := range info.Tables {
				lower := strings.ToLower(table)
				if t.cfg.Security.RequireViewsOnly && !(strings.HasPrefix(lower, "v_") || strings.HasPrefix(lower, "app_")) {
					t.logger.Warn("table not allowed (views only)", logging.Text("table", table))
					return errorResult(fmt.Sprintf("table %s is not allowed (views only)", table)), nil
				}
				if !security.IsAllowedObject(table, t.cfg.Security.AllowedObjects) {
					t.logger.Warn("object not allowed", logging.Text("table", table))
					return errorResult(fmt.Sprintf("object %s is not allowed", table)), nil
				}
			}
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			t.logger.Warn("failed to resolve tenant ID", logging.ErrorClass(err))
			return errorResult(err.Error()), nil
		}

		t.logger.Debug("creating view", logging.Text("view_name", args.ViewName), zap.String("tenant_id", tenantID))

		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			ddl := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", args.ViewName, args.SQL)
			logging.Debug(t.logger, "executing DDL", "ddl", ddl)
			_, err := q.ExecContext(ctx, ddl)
			return err
		})
		if err != nil {
			t.logger.Error("failed to create view",
				logging.ErrorClass(err),
				logging.Text("view_name", args.ViewName),
				logging.Size("sql", args.SQL),
				zap.String("tenant_id", tenantID),
			)
			logging.DebugError(t.logger, "view creation error text", err)
			return errorResult(err.Error()), nil
		}

		t.logger.Info("view created successfully", logging.Text("view_name", args.ViewName))

		return structuredResult(map[string]any{"viewName": args.ViewName, "created": true}), nil
	}
}
