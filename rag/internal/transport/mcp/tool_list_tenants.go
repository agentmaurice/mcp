package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ListTenantsTool implements the rag_list_tenants MCP tool
type ListTenantsTool struct {
	tenantRepo *repository.TenantRepository
	logger     *zap.Logger
}

// NewListTenantsTool creates a new list tenants tool
func NewListTenantsTool(tenantRepo *repository.TenantRepository, logger *zap.Logger) *ListTenantsTool {
	return &ListTenantsTool{
		tenantRepo: tenantRepo,
		logger:     logger.Named("list-tenants-tool"),
	}
}

// Definition returns the tool definition
func (t *ListTenantsTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_list_tenants",
		Description: "List tenants for a given deployment_id",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

// Handler returns the tool handler function
func (t *ListTenantsTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_list_tenants", zap.Any("arguments", request.Params.Arguments))

		// Parse arguments
		var args struct {
			DeploymentID string `json:"deployment_id"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		tenants, err := t.tenantRepo.ListByDeployment(ctx, deploymentID)
		if err != nil {
			t.logger.Error("failed to list tenants", zap.Error(err))
			return errorResult("failed to list tenants: " + err.Error()), nil
		}

		resp := make([]map[string]interface{}, 0, len(tenants))
		for _, tn := range tenants {
			resp = append(resp, map[string]interface{}{
				"id":            tn.ID.String(),
				"name":          tn.Name,
				"deployment_id": deploymentID.String(),
				"is_default":    tn.IsDefault,
			})
		}

		respBytes, _ := json.Marshal(resp)

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: string(respBytes),
				},
			},
		}, nil
	}
}
