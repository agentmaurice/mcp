package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// CheckDocumentTool implements the rag_check_document MCP tool
type CheckDocumentTool struct {
	docRepo    *repository.DocumentRepository
	tenantRepo *repository.TenantRepository
	logger     *zap.Logger
}

// NewCheckDocumentTool creates a new check document tool
func NewCheckDocumentTool(docRepo *repository.DocumentRepository, tenantRepo *repository.TenantRepository, logger *zap.Logger) *CheckDocumentTool {
	return &CheckDocumentTool{
		docRepo:    docRepo,
		tenantRepo: tenantRepo,
		logger:     logger.Named("check-document-tool"),
	}
}

// Definition returns the tool definition
func (t *CheckDocumentTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_check_document",
		Description: "Check if a document has already been ingested in a tenant. Search by URL, content hash, or title to avoid duplicate ingestion.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required)",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to deployment default tenant)",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "Source URL to check for existing document",
				},
				"hash": map[string]interface{}{
					"type":        "string",
					"description": "Normalized content hash (SHA256) to check for existing document",
				},
				"title": map[string]interface{}{
					"type":        "string",
					"description": "Document title to check for existing document",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

// Handler returns the tool handler function
func (t *CheckDocumentTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_check_document", zap.Any("arguments", request.Params.Arguments))

		// Parse arguments
		var args struct {
			DeploymentID string `json:"deployment_id"`
			TenantID     string `json:"tenant_id"`
			URL          string `json:"url"`
			Hash         string `json:"hash"`
			Title        string `json:"title"`
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
		if args.URL == "" && args.Hash == "" && args.Title == "" {
			return errorResult("at least one of 'url', 'hash', or 'title' is required"), nil
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

		// Build response
		response := map[string]interface{}{
			"exists":      false,
			"document_id": nil,
			"title":       nil,
			"tenant_id":   tenantID.String(),
		}

		// Check by URL first
		if args.URL != "" {
			doc, err := t.docRepo.FindByURL(ctx, tenantID, args.URL)
			if err != nil {
				if !isNotFound(err) {
					t.logger.Error("failed to check document by URL",
						zap.String("tenant_id", tenantID.String()),
						zap.String("url", args.URL),
						zap.Error(err))
					return errorResult("failed to check document by url"), nil
				}
			} else if doc != nil {
				response["exists"] = true
				response["document_id"] = doc.ID.String()
				response["title"] = doc.Title
				response["match_type"] = "url"
				response["source_url"] = doc.SourceURL

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

		// Check by hash if URL not found or not provided
		if args.Hash != "" {
			doc, err := t.docRepo.FindByHash(ctx, tenantID, args.Hash)
			if err != nil {
				if !isNotFound(err) {
					t.logger.Error("failed to check document by hash",
						zap.String("tenant_id", tenantID.String()),
						zap.String("hash", args.Hash),
						zap.Error(err))
					return errorResult("failed to check document by hash"), nil
				}
			} else if doc != nil {
				response["exists"] = true
				response["document_id"] = doc.ID.String()
				response["title"] = doc.Title
				response["match_type"] = "hash"
				if doc.SourceURL != "" {
					response["source_url"] = doc.SourceURL
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

		// Check by title if URL/hash not found or not provided
		if args.Title != "" {
			doc, err := t.docRepo.FindByTitle(ctx, tenantID, args.Title)
			if err != nil {
				if !isNotFound(err) {
					t.logger.Error("failed to check document by title",
						zap.String("tenant_id", tenantID.String()),
						zap.String("title", args.Title),
						zap.Error(err))
					return errorResult("failed to check document by title"), nil
				}
			} else if doc != nil {
				response["exists"] = true
				response["document_id"] = doc.ID.String()
				response["title"] = doc.Title
				response["match_type"] = "title"
				if doc.SourceURL != "" {
					response["source_url"] = doc.SourceURL
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

		// Document not found
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
