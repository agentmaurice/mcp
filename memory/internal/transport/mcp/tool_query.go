package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/logging"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// QueryTool implements memory.query.
type QueryTool struct {
	storage         storage.Manager
	cfg             *config.Config
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

func NewQueryTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *QueryTool {
	return &QueryTool{storage: storage, cfg: cfg, responseWrapper: wrapper, logger: logger.Named("memory.query")}
}

func (t *QueryTool) Name() string {
	return "memory.query"
}

func (t *QueryTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.query",
		Description: "Execute a read-only SQL SELECT query with guardrails. The result sets truncated only when the row cap dropped rows; for a tenant with no entity, document or fact it sets tenant_empty and returns a warning.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"sql": map[string]interface{}{
					"type":      "string",
					"minLength": 1,
				},
				"params": map[string]interface{}{
					"type":        "object",
					"description": "Named parameters.",
					"additionalProperties": map[string]interface{}{
						"type": []string{"string", "number", "boolean", "null"},
					},
				},
				"maxRows": map[string]interface{}{
					"type":    "integer",
					"minimum": 1,
					"maximum": 5000,
					"default": 500,
				},
				"timeoutMs": map[string]interface{}{
					"type":    "integer",
					"minimum": 50,
					"maximum": 10000,
					"default": 2000,
				},
			},
			Required: []string{"sql"},
		},
	}
}

func (t *QueryTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Info("tool called", zap.String("tool", t.Name()), logging.SizeOf("arguments", request.Params.Arguments))
		logging.DebugValue(t.logger, "tool arguments", "arguments", request.Params.Arguments, zap.String("tool", t.Name()))

		var args struct {
			TenantID  string         `json:"tenant_id"`
			SQL       string         `json:"sql"`
			Params    map[string]any `json:"params"`
			MaxRows   int            `json:"maxRows"`
			TimeoutMs int            `json:"timeoutMs"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			t.logger.Warn("failed to parse arguments", zap.String("tool", t.Name()), logging.ErrorClass(err), logging.SizeOf("raw_arguments", request.Params.Arguments))
			logging.DebugValue(t.logger, "unparsed arguments", "raw_arguments", request.Params.Arguments, zap.String("tool", t.Name()))
			return errorResult("failed to parse arguments"), nil
		}

		logging.Debug(t.logger, "parsed sql", "sql", args.SQL, zap.String("tenant_id", args.TenantID))
		logging.DebugValue(t.logger, "parsed params", "params", args.Params)
		t.logger.Debug("parsed arguments",
			zap.String("tenant_id", args.TenantID),
			logging.Size("sql", args.SQL),
			logging.SizeOf("params", args.Params),
			zap.Int("maxRows", args.MaxRows),
			zap.Int("timeoutMs", args.TimeoutMs),
		)

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			t.logger.Warn("failed to resolve tenant ID", logging.ErrorClass(err))
			return errorResult(err.Error()), nil
		}

		result, err := executeQuery(ctx, t.storage, t.cfg, t.logger, args.SQL, args.Params, args.MaxRows, args.TimeoutMs, tenantID)
		if err != nil {
			t.logger.Error("query execution failed",
				logging.ErrorClass(err),
				logging.Size("sql", args.SQL),
				zap.String("tenant_id", tenantID),
			)
			logging.DebugError(t.logger, "query execution error text", err)
			return errorResult(err.Error()), nil
		}

		t.logger.Info("query completed successfully",
			zap.String("tool", t.Name()),
			zap.String("tenant_id", tenantID),
			logging.Size("sql", args.SQL),
			zap.Int("row_count", len(result.Rows)),
			zap.Int64("elapsed_ms", result.QueryStats.ElapsedMs),
		)

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("memory.query result for: %s", truncateForSummary(strings.ReplaceAll(args.SQL, "\n", " "), 80))
			return t.responseWrapper.WrapToolResult(ctx, t.Name(), result, summary)
		}

		return structuredResult(result), nil
	}
}
