package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ListDocumentsTool implements the rag_list_documents MCP tool
type ListDocumentsTool struct {
	docRepo    *repository.DocumentRepository
	tenantRepo *repository.TenantRepository
	logger     *zap.Logger
}

// NewListDocumentsTool creates a new list documents tool
func NewListDocumentsTool(docRepo *repository.DocumentRepository, tenantRepo *repository.TenantRepository, logger *zap.Logger) *ListDocumentsTool {
	return &ListDocumentsTool{
		docRepo:    docRepo,
		tenantRepo: tenantRepo,
		logger:     logger.Named("list-documents-tool"),
	}
}

// Definition returns the tool definition
func (t *ListDocumentsTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_list_documents",
		Description: "List all documents ingested in a tenant with pagination and optional filters",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required)",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID or name (optional, defaults to deployment default tenant)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of documents to return (default: 50, max: 200)",
					"default":     50,
				},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "Number of documents to skip for pagination",
					"default":     0,
				},
				"doc_type": map[string]interface{}{
					"type":        "string",
					"description": "Filter by document type (e.g., generic, cv, contract)",
				},
				"title_query": map[string]interface{}{
					"type":        "string",
					"description": "Filter by title (case-insensitive substring match)",
				},
				"include_metadata": map[string]interface{}{
					"type":        "boolean",
					"description": "Include document metadata in the response",
					"default":     false,
				},
				"include_pii_summary": map[string]interface{}{
					"type":        "boolean",
					"description": "Include native PII summary when available",
					"default":     false,
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

// Handler returns the tool handler function
func (t *ListDocumentsTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_list_documents", zap.Any("arguments", request.Params.Arguments))

		// Parse arguments
		var args struct {
			DeploymentID      string `json:"deployment_id"`
			TenantID          string `json:"tenant_id"`
			Limit             int    `json:"limit"`
			Offset            int    `json:"offset"`
			DocType           string `json:"doc_type"`
			TitleQuery        string `json:"title_query"`
			IncludeMetadata   bool   `json:"include_metadata"`
			IncludePiiSummary bool   `json:"include_pii_summary"`
		}

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		// Validate
		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}

		// Set defaults
		if args.Limit <= 0 {
			args.Limit = 50
		}
		if args.Limit > 200 {
			args.Limit = 200
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		// Resolve tenant
		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			t.logger.Error("failed to resolve tenant", zap.String("tenant_id", args.TenantID), zap.Error(err))
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}

		// List documents
		docs, total, err := t.docRepo.ListByTenantPaginated(ctx, repository.DocumentListOptions{
			TenantID:   tenantID,
			Limit:      args.Limit,
			Offset:     args.Offset,
			DocType:    args.DocType,
			TitleQuery: args.TitleQuery,
		})
		if err != nil {
			t.logger.Error("failed to list documents", zap.Error(err))
			return errorResult("failed to list documents: " + err.Error()), nil
		}

		// Build response
		documents := make([]map[string]interface{}, 0, len(docs))
		for _, doc := range docs {
			docInfo := map[string]interface{}{
				"id":         doc.ID.String(),
				"title":      doc.Title,
				"doc_type":   doc.DocType,
				"created_at": doc.CreatedAt.Format("2006-01-02T15:04:05Z"),
			}
			if doc.SourceURL != "" {
				docInfo["source_url"] = doc.SourceURL
			}
			if doc.ContentType != nil {
				docInfo["content_type"] = *doc.ContentType
			}
			if doc.Size != nil {
				docInfo["size"] = *doc.Size
			}
			if doc.HasPii {
				docInfo["has_pii"] = true
			}
			if args.IncludeMetadata && len(doc.Metadata) > 0 {
				docInfo["metadata"] = doc.Metadata
			}
			if args.IncludePiiSummary && len(doc.PiiSummary) > 0 {
				docInfo["pii_summary"] = doc.PiiSummary
			}
			documents = append(documents, docInfo)
		}

		response := map[string]interface{}{
			"tenant_id": tenantID.String(),
			"documents": documents,
			"pagination": map[string]interface{}{
				"total":  total,
				"limit":  args.Limit,
				"offset": args.Offset,
			},
		}

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
