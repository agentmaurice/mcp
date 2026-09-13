package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
	"go.uber.org/zap"
)

// ExtractDocumentTextTool returns raw chunk text for documents matching metadata filters.
type ExtractDocumentTextTool struct {
	docRepo         *repository.DocumentRepository
	chunkRepo       *repository.ChunkRepository
	tenantRepo      *repository.TenantRepository
	responseWrapper *ResponseWrapper
	logger          *zap.Logger
}

func NewExtractDocumentTextTool(docRepo *repository.DocumentRepository, chunkRepo *repository.ChunkRepository, tenantRepo *repository.TenantRepository, responseWrapper *ResponseWrapper, logger *zap.Logger) *ExtractDocumentTextTool {
	return &ExtractDocumentTextTool{
		docRepo:         docRepo,
		chunkRepo:       chunkRepo,
		tenantRepo:      tenantRepo,
		responseWrapper: responseWrapper,
		logger:          logger.Named("extract-document-text-tool"),
	}
}

func (t *ExtractDocumentTextTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_extract_document_text",
		Description: "Return raw chunk text for a document or for documents matching metadata filters",
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
				"document_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional document ID to extract directly",
				},
				"metadata_filters": map[string]interface{}{
					"type":        "object",
					"description": "Optional metadata filters to restrict extraction to matching chunks/documents. Example: {\"offre_id\": \"18889\"}",
					"additionalProperties": map[string]interface{}{
						"type": "string",
					},
				},
				"max_documents": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of documents to return (default: 10)",
				},
				"max_chunks": map[string]interface{}{
					"type":        "number",
					"description": "Maximum number of chunks to return across all documents (default: 500)",
				},
				"include_chunk_metadata": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to include chunk metadata in the response (default: false)",
				},
			},
			Required: []string{"deployment_id"},
		},
	}
}

func (t *ExtractDocumentTextTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			DeploymentID         string            `json:"deployment_id"`
			TenantID             string            `json:"tenant_id"`
			DocumentID           string            `json:"document_id"`
			MetadataFilters      map[string]string `json:"metadata_filters"`
			MaxDocuments         int               `json:"max_documents"`
			MaxChunks            int               `json:"max_chunks"`
			IncludeChunkMetadata bool              `json:"include_chunk_metadata"`
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
		if args.DocumentID == "" && len(args.MetadataFilters) == 0 {
			return errorResult("document_id or metadata_filters is required"), nil
		}
		if args.MaxDocuments <= 0 {
			args.MaxDocuments = 10
		}
		if args.MaxChunks <= 0 {
			args.MaxChunks = 500
		}

		deploymentID, err := parseDeploymentID(args.DeploymentID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		tenantID, err := resolveTenant(ctx, t.tenantRepo, deploymentID, args.TenantID, t.logger)
		if err != nil {
			t.logger.Error("failed to resolve tenant", zap.String("tenant_id", args.TenantID), zap.Error(err))
			return errorResult("failed to resolve tenant: " + err.Error()), nil
		}

		documents := make([]map[string]interface{}, 0)
		totalChunks := 0

		if args.DocumentID != "" {
			documents, totalChunks, err = t.extractByDocumentID(ctx, tenantID, args.DocumentID, args.MetadataFilters, args.MaxChunks, args.IncludeChunkMetadata)
		} else {
			documents, totalChunks, err = t.extractByMetadata(ctx, tenantID, args.MetadataFilters, args.MaxDocuments, args.MaxChunks, args.IncludeChunkMetadata)
		}
		if err != nil {
			t.logger.Error("document extraction failed", zap.Error(err))
			return errorResult("document extraction failed: " + err.Error()), nil
		}

		contentParts := make([]string, 0, len(documents))
		documentIDs := make([]string, 0, len(documents))
		for _, doc := range documents {
			if content, ok := doc["content"].(string); ok && strings.TrimSpace(content) != "" {
				contentParts = append(contentParts, content)
			}
			if documentID, ok := doc["document_id"].(string); ok && documentID != "" {
				documentIDs = append(documentIDs, documentID)
			}
		}

		response := map[string]interface{}{
			"content":          strings.Join(contentParts, "\n\n---\n\n"),
			"document_count":   len(documents),
			"chunk_count":      totalChunks,
			"document_ids":     documentIDs,
			"documents":        documents,
			"metadata_filters": args.MetadataFilters,
		}

		if t.responseWrapper != nil && t.responseWrapper.IsEnabled() {
			summary := fmt.Sprintf("Extracted raw text from %d documents and %d chunks", len(documents), totalChunks)
			return t.responseWrapper.WrapToolResult(ctx, "rag_extract_document_text", response, summary)
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

func (t *ExtractDocumentTextTool) extractByDocumentID(ctx context.Context, tenantID xid.ID, documentID string, metadataFilters map[string]string, maxChunks int, includeChunkMetadata bool) ([]map[string]interface{}, int, error) {
	docID, err := xid.FromString(documentID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid document_id")
	}
	doc, err := t.docRepo.Get(ctx, docID)
	if err != nil {
		return nil, 0, err
	}
	if doc.TenantID != tenantID {
		return nil, 0, fmt.Errorf("document does not belong to tenant")
	}

	chunks, err := t.chunkRepo.ListByDocument(ctx, docID)
	if err != nil {
		return nil, 0, err
	}

	filtered := make([]*repositoryChunkView, 0, len(chunks))
	for _, ch := range chunks {
		if len(metadataFilters) > 0 && !metadataMatchesFilters(ch.Metadata, metadataFilters) && !metadataMatchesFilters(doc.Metadata, metadataFilters) {
			continue
		}
		filtered = append(filtered, &repositoryChunkView{
			ID:       ch.ID.String(),
			Text:     ch.Text,
			Metadata: ch.Metadata,
		})
		if maxChunks > 0 && len(filtered) >= maxChunks {
			break
		}
	}

	if len(metadataFilters) > 0 && len(filtered) == 0 {
		return []map[string]interface{}{}, 0, nil
	}

	payload := buildExtractedDocumentPayload(doc.ID.String(), doc.Title, doc.DocType, doc.Metadata, filtered, includeChunkMetadata)
	return []map[string]interface{}{payload}, len(filtered), nil
}

func (t *ExtractDocumentTextTool) extractByMetadata(ctx context.Context, tenantID xid.ID, metadataFilters map[string]string, maxDocuments, maxChunks int, includeChunkMetadata bool) ([]map[string]interface{}, int, error) {
	const pageSize = 500

	type docAccumulator struct {
		documentID string
		doc        map[string]interface{}
		chunks     []*repositoryChunkView
	}

	docAccumulators := make(map[string]*docAccumulator)
	docOrder := make([]string, 0)
	totalChunks := 0
	docCache := make(map[string]map[string]interface{})

	loadDoc := func(documentID xid.ID) (map[string]interface{}, error) {
		key := documentID.String()
		if cached, ok := docCache[key]; ok {
			return cached, nil
		}
		doc, err := t.docRepo.Get(ctx, documentID)
		if err != nil {
			return nil, err
		}
		if doc.TenantID != tenantID {
			return nil, fmt.Errorf("document %s does not belong to tenant", key)
		}
		payload := map[string]interface{}{
			"document_id": key,
			"title":       doc.Title,
			"doc_type":    doc.DocType,
			"metadata":    doc.Metadata,
		}
		docCache[key] = payload
		return payload, nil
	}

	for offset := 0; ; offset += pageSize {
		page, err := t.chunkRepo.ListByTenant(ctx, tenantID, offset, pageSize)
		if err != nil {
			return nil, 0, err
		}
		if len(page) == 0 {
			break
		}

		for _, ch := range page {
			if !metadataMatchesFilters(ch.Metadata, metadataFilters) {
				continue
			}

			key := ch.DocumentID.String()
			acc, ok := docAccumulators[key]
			if !ok {
				if maxDocuments > 0 && len(docOrder) >= maxDocuments {
					continue
				}
				docPayload, err := loadDoc(ch.DocumentID)
				if err != nil {
					return nil, 0, err
				}
				acc = &docAccumulator{
					documentID: key,
					doc:        docPayload,
					chunks:     make([]*repositoryChunkView, 0),
				}
				docAccumulators[key] = acc
				docOrder = append(docOrder, key)
			}

			if maxChunks > 0 && totalChunks >= maxChunks {
				break
			}

			acc.chunks = append(acc.chunks, &repositoryChunkView{
				ID:       ch.ID.String(),
				Text:     ch.Text,
				Metadata: ch.Metadata,
			})
			totalChunks++
		}

		if maxChunks > 0 && totalChunks >= maxChunks {
			break
		}
	}

	documents := make([]map[string]interface{}, 0, len(docOrder))
	for _, key := range docOrder {
		acc := docAccumulators[key]
		if acc == nil || len(acc.chunks) == 0 {
			continue
		}
		documents = append(documents, buildExtractedDocumentPayload(
			acc.documentID,
			stringValue(acc.doc["title"]),
			stringValue(acc.doc["doc_type"]),
			mapValue(acc.doc["metadata"]),
			acc.chunks,
			includeChunkMetadata,
		))
	}

	return documents, totalChunks, nil
}

type repositoryChunkView struct {
	ID       string
	Text     string
	Metadata map[string]interface{}
}

func buildExtractedDocumentPayload(documentID, title, docType string, metadata map[string]interface{}, chunks []*repositoryChunkView, includeChunkMetadata bool) map[string]interface{} {
	contentParts := make([]string, 0, len(chunks))
	chunkPayload := make([]map[string]interface{}, 0, len(chunks))
	for _, ch := range chunks {
		contentParts = append(contentParts, ch.Text)
		payload := map[string]interface{}{
			"chunk_id": ch.ID,
			"text":     ch.Text,
		}
		if includeChunkMetadata {
			payload["metadata"] = ch.Metadata
		}
		chunkPayload = append(chunkPayload, payload)
	}

	return map[string]interface{}{
		"document_id": documentID,
		"title":       title,
		"doc_type":    docType,
		"metadata":    metadata,
		"chunk_count": len(chunks),
		"content":     strings.Join(contentParts, "\n\n"),
		"chunks":      chunkPayload,
	}
}

func metadataMatchesFilters(metadata map[string]interface{}, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}
	if metadata == nil {
		return false
	}

	aliases := map[string][]string{
		"offre_id":        {"offre_id", "offreId"},
		"offreId":         {"offre_id", "offreId"},
		"consultation_id": {"consultation_id", "consultationId"},
		"consultationId":  {"consultation_id", "consultationId"},
		"trigramme":       {"trigramme"},
		"doc_type":        {"doc_type"},
		"file_name":       {"file_name"},
	}

	for filterKey, filterVal := range filters {
		keys := aliases[filterKey]
		if len(keys) == 0 {
			keys = []string{filterKey}
		}

		matched := false
		for _, key := range keys {
			if val, ok := metadata[key]; ok && metadataValueMatches(val, filterVal) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

func metadataValueMatches(value interface{}, expected string) bool {
	switch v := value.(type) {
	case string:
		return v == expected
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v)) == expected
		}
		return fmt.Sprintf("%g", v) == expected
	case json.Number:
		return v.String() == expected
	default:
		return fmt.Sprintf("%v", v) == expected
	}
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func mapValue(value interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}
