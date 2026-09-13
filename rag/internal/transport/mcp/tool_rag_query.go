package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// RAGQueryTool implements the rag_query MCP tool
type RAGQueryTool struct {
	ragManager      *business.RAGManager
	tenantRepo      *repository.TenantRepository
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

// NewRAGQueryTool creates a new RAG query tool
func NewRAGQueryTool(ragManager *business.RAGManager, tenantRepo *repository.TenantRepository, responseWrapper *ResponseWrapper, logger *zap.Logger) *RAGQueryTool {
	return &RAGQueryTool{
		ragManager:      ragManager,
		tenantRepo:      tenantRepo,
		responseWrapper: responseWrapper,
		logger:          logger.Named("rag-query-tool"),
	}
}

// Definition returns the tool definition
func (t *RAGQueryTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_query",
		Description: "Execute a RAG query to retrieve and generate answers from the knowledge base",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required). If tenant_id is omitted, the default tenant of the deployment is used",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID for multi-tenant isolation",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The question or query to answer",
				},
				"max_tokens": map[string]interface{}{
					"type":        "number",
					"description": "Maximum tokens for the answer (optional, default: 500)",
				},
				"metadata_filters": map[string]interface{}{
					"type":        "object",
					"description": "Optional metadata filters to restrict retrieval to matching documents. Keys are metadata field names (e.g. offre_id, trigramme, doc_type), values are the expected string values. Example: {\"offre_id\": \"123\"} to only retrieve chunks from offer 123.",
					"additionalProperties": map[string]interface{}{
						"type": "string",
					},
				},
			},
			Required: []string{"deployment_id", "query"},
		},
	}
}

// Handler returns the tool handler function
func (t *RAGQueryTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_query", zap.Any("arguments", request.Params.Arguments))

		// Parse arguments
		var args struct {
			DeploymentID    string            `json:"deployment_id"`
			TenantID        string            `json:"tenant_id"`
			Query           string            `json:"query"`
			MaxTokens       int               `json:"max_tokens"`
			MetadataFilters map[string]string  `json:"metadata_filters"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		// Validate
		if args.Query == "" {
			return errorResult("query is required"), nil
		}
		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}

		if args.MaxTokens == 0 {
			args.MaxTokens = 500
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		// Resolve tenant ID - accept both xid format and tenant names
		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			t.logger.Error("failed to resolve tenant", zap.String("tenant_id", args.TenantID), zap.Error(err))
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}

		// Build query
		query := shared.Query{
			Text:            args.Query,
			MaxTokens:       args.MaxTokens,
			TenantID:        tenantID,
			DeploymentID:    deploymentID,
			MetadataFilters: args.MetadataFilters,
		}

		// Execute query
		answer, err := t.ragManager.Query(ctx, query)
		if err != nil {
			t.logger.Error("query failed", zap.Error(err))
			return errorResult("query execution failed: " + err.Error()), nil
		}

		// Build response
		response := map[string]interface{}{
			"answer":    answer.Text,
			"citations": answer.Citations,
		}

		// Use ResponseWrapper for automatic buffering of large responses
		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("RAG query result for: %s", truncateQueryForSummary(args.Query, 50))
			return t.responseWrapper.WrapToolResult(ctx, "rag_query", response, summary)
		}

		// Fallback: direct response
		responseBytes, _ := json.Marshal(response)

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: string(responseBytes),
				},
			},
		}, nil
	}
}

// truncateQueryForSummary truncates a query string for summary display
func truncateQueryForSummary(query string, maxLen int) string {
	if len(query) <= maxLen {
		return query
	}
	return query[:maxLen] + "..."
}

// errorResult creates an error result
func errorResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: `{"error": "` + message + `"}`,
			},
		},
		IsError: true,
	}
}
