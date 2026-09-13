package mcp

import (
	"context"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type IndexStatusTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewIndexStatusTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *IndexStatusTool {
	return &IndexStatusTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.index.status")}
}

func (t *IndexStatusTool) Name() string { return "brain.index.status" }

func (t *IndexStatusTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.index.status",
		Description: "Check the status of indexation jobs.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"job_id":    map[string]interface{}{"type": "string", "description": "Specific job ID to check"},
				"source_id": map[string]interface{}{"type": "string", "description": "List jobs for a specific source"},
				"tenant_id": map[string]interface{}{"type": "string"},
			},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *IndexStatusTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			JobID    string `json:"job_id"`
			SourceID string `json:"source_id"`
			TenantID string `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		var query string
		var queryArgs []interface{}

		if args.JobID != "" {
			query = `SELECT id, source_id, status, progress, total_files, processed_files, total_chunks, error_message, started_at, completed_at, created_at FROM index_jobs WHERE tenant_id = ? AND id = ?`
			queryArgs = []interface{}{tenantID, args.JobID}
		} else if args.SourceID != "" {
			query = `SELECT id, source_id, status, progress, total_files, processed_files, total_chunks, error_message, started_at, completed_at, created_at FROM index_jobs WHERE tenant_id = ? AND source_id = ? ORDER BY created_at DESC LIMIT 10`
			queryArgs = []interface{}{tenantID, args.SourceID}
		} else {
			query = `SELECT id, source_id, status, progress, total_files, processed_files, total_chunks, error_message, started_at, completed_at, created_at FROM index_jobs WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 20`
			queryArgs = []interface{}{tenantID}
		}

		qr, err := db.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		var jobs []map[string]interface{}
		for _, row := range qr.Rows {
			job := map[string]interface{}{
				"id":              row["id"],
				"source_id":       row["source_id"],
				"status":          row["status"],
				"progress":        row["progress"],
				"total_files":     row["total_files"],
				"processed_files": row["processed_files"],
				"total_chunks":    row["total_chunks"],
			}
			if v := row["error_message"]; v != nil && fmt.Sprintf("%v", v) != "" {
				job["error_message"] = v
			}
			if v := row["started_at"]; v != nil {
				job["started_at"] = v
			}
			if v := row["completed_at"]; v != nil {
				job["completed_at"] = v
			}
			if v := row["created_at"]; v != nil {
				job["created_at"] = v
			}
			jobs = append(jobs, job)
		}

		return t.wrapper.Wrap(map[string]any{"jobs": jobs}), nil
	}
}
