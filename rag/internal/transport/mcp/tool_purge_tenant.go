package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/cache"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/db/ent"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// PurgeTenantTool implements the rag_purge_tenant MCP tool.
type PurgeTenantTool struct {
	tenantRepo  *repository.TenantRepository
	docRepo     *repository.DocumentRepository
	chunkRepo   *repository.ChunkRepository
	jobRepo     *repository.IngestJobRepository
	vectorStore shared.VectorStore
	queryCache  cache.QueryCache
	logger      *zap.Logger
}

// NewPurgeTenantTool creates a new purge tenant tool.
func NewPurgeTenantTool(
	tenantRepo *repository.TenantRepository,
	docRepo *repository.DocumentRepository,
	chunkRepo *repository.ChunkRepository,
	jobRepo *repository.IngestJobRepository,
	vectorStore shared.VectorStore,
	queryCache cache.QueryCache,
	logger *zap.Logger,
) *PurgeTenantTool {
	return &PurgeTenantTool{
		tenantRepo:  tenantRepo,
		docRepo:     docRepo,
		chunkRepo:   chunkRepo,
		jobRepo:     jobRepo,
		vectorStore: vectorStore,
		queryCache:  queryCache,
		logger:      logger.Named("purge-tenant-tool"),
	}
}

// Definition returns the tool definition.
func (t *PurgeTenantTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_purge_tenant",
		Description: "Purge all documents, chunks, and vector embeddings for a tenant. Requires confirm=true or dry_run=true.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required). If tenant_id is omitted, the deployment default tenant is used",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID or name to purge. If omitted, uses the deployment default tenant",
				},
				"dry_run": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, returns counts without deleting anything",
					"default":     false,
				},
				"confirm": map[string]interface{}{
					"type":        "boolean",
					"description": "Must be true to perform deletion (ignored if dry_run=true)",
					"default":     false,
				},
				"delete_jobs": map[string]interface{}{
					"type":        "boolean",
					"description": "Also delete ingest jobs for the tenant",
					"default":     true,
				},
				"delete_tenant": map[string]interface{}{
					"type":        "boolean",
					"description": "Also delete the tenant entity itself after purging data",
					"default":     false,
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

// Handler returns the tool handler function.
func (t *PurgeTenantTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_purge_tenant", zap.Any("arguments", request.Params.Arguments))

		var args struct {
			DeploymentID string `json:"deployment_id"`
			TenantID     string `json:"tenant_id"`
			DryRun       bool   `json:"dry_run"`
			Confirm      bool   `json:"confirm"`
			DeleteJobs   bool   `json:"delete_jobs"`
			DeleteTenant bool   `json:"delete_tenant"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if rawArgs, ok := request.Params.Arguments.(map[string]interface{}); ok {
			if _, found := rawArgs["delete_jobs"]; !found {
				args.DeleteJobs = true
			}
		}

		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}
		if !args.DryRun && !args.Confirm {
			return errorResult("confirm=true is required to purge (or use dry_run=true)"), nil
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		tenant, err := resolveExistingTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			t.logger.Error("failed to resolve tenant", zap.String("tenant_id", args.TenantID), zap.Error(err))
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}

		docCount, err := t.docRepo.CountByTenant(ctx, tenant.ID)
		if err != nil {
			return errorResult("failed to count documents: " + err.Error()), nil
		}
		chunkCount, err := t.chunkRepo.CountByTenant(ctx, tenant.ID)
		if err != nil {
			return errorResult("failed to count chunks: " + err.Error()), nil
		}
		jobCount, err := t.jobRepo.CountByTenant(ctx, tenant.ID)
		if err != nil {
			return errorResult("failed to count ingest jobs: " + err.Error()), nil
		}

		response := map[string]interface{}{
			"tenant_id":   tenant.ID.String(),
			"tenant_name": tenant.Name,
			"counts": map[string]interface{}{
				"documents": docCount,
				"chunks":    chunkCount,
				"jobs":      jobCount,
			},
		}

		if args.DryRun {
			response["status"] = "dry_run"
			return toolResult(response), nil
		}

		t.logger.Warn("purging tenant data",
			zap.String("tenant_id", tenant.ID.String()),
			zap.String("tenant_name", tenant.Name))

		if err := t.vectorStore.DeleteByTenant(ctx, tenant.ID); err != nil {
			return errorResult("failed to delete vectors: " + err.Error()), nil
		}

		deletedChunks, err := t.chunkRepo.DeleteByTenant(ctx, tenant.ID)
		if err != nil {
			return errorResult("failed to delete chunks: " + err.Error()), nil
		}
		deletedDocs, err := t.docRepo.DeleteByTenant(ctx, tenant.ID)
		if err != nil {
			return errorResult("failed to delete documents: " + err.Error()), nil
		}
		deletedJobs := 0
		if args.DeleteJobs {
			deletedJobs, err = t.jobRepo.DeleteByTenant(ctx, tenant.ID)
			if err != nil {
				return errorResult("failed to delete ingest jobs: " + err.Error()), nil
			}
		}

		tenantDeleted := false
		if args.DeleteTenant {
			if err := t.tenantRepo.Delete(ctx, tenant.ID); err != nil {
				return errorResult("failed to delete tenant: " + err.Error()), nil
			}
			tenantDeleted = true
		}

		// Invalidate cache for the tenant
		if t.queryCache != nil && t.queryCache.IsEnabled() {
			if err := t.queryCache.InvalidateTenant(ctx, tenant.ID); err != nil {
				t.logger.Warn("failed to invalidate cache for tenant", zap.Error(err))
			} else {
				t.logger.Debug("cache invalidated for tenant", zap.String("tenant_id", tenant.ID.String()))
			}
		}

		response["status"] = "purged"
		response["deleted"] = map[string]interface{}{
			"documents": deletedDocs,
			"chunks":    deletedChunks,
			"jobs":      deletedJobs,
			"tenant":    tenantDeleted,
		}

		return toolResult(response), nil
	}
}

func resolveExistingTenant(ctx context.Context, tenantRepo *repository.TenantRepository, deploymentID xid.ID, tenantIDOrName string, logger *zap.Logger) (*ent.Tenant, error) {
	if deploymentID == xid.NilID() {
		return nil, shared.ErrValidation("deployment_id is required")
	}

	if tenantIDOrName == "" {
		defaultTenant, err := tenantRepo.GetDefaultByDeployment(ctx, deploymentID)
		if err != nil {
			if isNotFound(err) {
				return nil, shared.ErrNotFound("default tenant")
			}
			return nil, err
		}
		logger.Debug("using default tenant", zap.String("tenant_id", defaultTenant.ID.String()))
		return defaultTenant, nil
	}

	if parsed, err := xid.FromString(tenantIDOrName); err == nil {
		tenant, err := tenantRepo.Get(ctx, parsed)
		if err != nil {
			return nil, err
		}
		if tenant.DeploymentID == nil || *tenant.DeploymentID != deploymentID {
			return nil, shared.ErrValidation("tenant does not belong to deployment")
		}
		return tenant, nil
	}

	tenant, err := tenantRepo.GetByName(ctx, deploymentID, tenantIDOrName)
	if err != nil {
		return nil, err
	}
	return tenant, nil
}

func toolResult(payload map[string]interface{}) *mcp.CallToolResult {
	responseBytes, _ := json.Marshal(payload)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: string(responseBytes),
			},
		},
	}
}
