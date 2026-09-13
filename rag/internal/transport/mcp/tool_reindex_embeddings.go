package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ReindexEmbeddingsTool implements the rag_reindex_embeddings MCP tool.
type ReindexEmbeddingsTool struct {
	ingestManager *business.IngestManager
	tenantRepo    *repository.TenantRepository
	logger        *zap.Logger
}

// NewReindexEmbeddingsTool creates a new reindex embeddings tool.
func NewReindexEmbeddingsTool(ingestManager *business.IngestManager, tenantRepo *repository.TenantRepository, logger *zap.Logger) *ReindexEmbeddingsTool {
	return &ReindexEmbeddingsTool{
		ingestManager: ingestManager,
		tenantRepo:    tenantRepo,
		logger:        logger.Named("reindex-embeddings-tool"),
	}
}

// Definition returns the tool definition.
func (t *ReindexEmbeddingsTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_reindex_embeddings",
		Description: "Queue a background reindex of embeddings (int8 + binary) for a deployment or tenant.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required).",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID or name (optional). If omitted, reindex all tenants in the deployment.",
				},
				"batch_size": map[string]interface{}{
					"type":        "integer",
					"description": "Batch size for reindexing (default: 100).",
				},
				"force": map[string]interface{}{
					"type":        "boolean",
					"description": "Force reindex even if embeddings already exist.",
					"default":     false,
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

// Handler returns the tool handler function.
func (t *ReindexEmbeddingsTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			DeploymentID string `json:"deployment_id"`
			TenantID     string `json:"tenant_id"`
			BatchSize    int    `json:"batch_size"`
			Force        bool   `json:"force"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if args.BatchSize <= 0 {
			args.BatchSize = 100
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		req := business.ReindexRequest{
			BatchSize: args.BatchSize,
			Force:     args.Force,
			Mode:      "int8_binary",
		}

		if args.TenantID != "" {
			tenant, err := resolveExistingTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
			if err != nil {
				return errorResult("failed to resolve tenant: " + err.Error()), nil
			}
			result, err := t.ingestManager.StartReindex(ctx, deploymentID, tenant.ID, req)
			if err != nil {
				return errorResult("failed to create reindex job: " + err.Error()), nil
			}
			resp := map[string]interface{}{
				"status":    "queued",
				"tenant_id": tenant.ID.String(),
				"job_id":    result.JobID.String(),
			}
			return toolResult(resp), nil
		}

		tenants, err := t.tenantRepo.ListByDeployment(ctx, deploymentID)
		if err != nil {
			return errorResult("failed to list tenants: " + err.Error()), nil
		}
		if len(tenants) == 0 {
			return errorResult("no tenants found for deployment"), nil
		}

		jobs := make([]map[string]interface{}, 0, len(tenants))
		for _, tenant := range tenants {
			result, err := t.ingestManager.StartReindex(ctx, deploymentID, tenant.ID, req)
			if err != nil {
				t.logger.Warn("failed to create reindex job",
					zap.String("tenant_id", tenant.ID.String()),
					zap.Error(err))
				continue
			}
			jobs = append(jobs, map[string]interface{}{
				"tenant_id":   tenant.ID.String(),
				"tenant_name": tenant.Name,
				"job_id":      result.JobID.String(),
			})
		}

		resp := map[string]interface{}{
			"status": "queued",
			"count":  len(jobs),
			"jobs":   jobs,
		}
		return toolResult(resp), nil
	}
}
