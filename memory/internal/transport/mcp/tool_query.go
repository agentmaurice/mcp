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
		Description: "Execute a read-only SQL SELECT query with guardrails",
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
		t.logger.Info("tool called", zap.Any("arguments", request.Params.Arguments))

		var args struct {
			TenantID  string         `json:"tenant_id"`
			SQL       string         `json:"sql"`
			Params    map[string]any `json:"params"`
			MaxRows   int            `json:"maxRows"`
			TimeoutMs int            `json:"timeoutMs"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			t.logger.Warn("failed to parse arguments", zap.Error(err), zap.Any("raw_arguments", request.Params.Arguments))
			return errorResult("failed to parse arguments"), nil
		}

		t.logger.Debug("parsed arguments",
			zap.String("tenant_id", args.TenantID),
			zap.String("sql", args.SQL),
			zap.Any("params", args.Params),
			zap.Int("maxRows", args.MaxRows),
			zap.Int("timeoutMs", args.TimeoutMs),
		)

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			t.logger.Warn("failed to resolve tenant ID", zap.Error(err))
			return errorResult(err.Error()), nil
		}

		result, err := executeQuery(ctx, t.storage, t.cfg, t.logger, args.SQL, args.Params, args.MaxRows, args.TimeoutMs, tenantID)
		if err != nil {
			t.logger.Error("query execution failed",
				zap.Error(err),
				zap.String("sql", args.SQL),
				zap.String("tenant_id", tenantID),
			)
			return errorResult(err.Error()), nil
		}

		t.logger.Info("query completed successfully",
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
