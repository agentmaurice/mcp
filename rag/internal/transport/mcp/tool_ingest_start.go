package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/business"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/platform/buffer"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/rag/inspect"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/storage/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var errBufferClientNotConfigured = errors.New("buffer client is not configured")

// IngestStartTool implements the rag_ingest_start MCP tool
type IngestStartTool struct {
	ingestManager *business.IngestManager
	tenantRepo    *repository.TenantRepository
	bufferClient  *buffer.Client
	logger        *zap.Logger
}

type ingestStartArgs struct {
	DeploymentID            string                   `json:"deployment_id"`
	TenantID                string                   `json:"tenant_id"`
	Title                   string                   `json:"title"`
	Content                 string                   `json:"content"`
	URL                     string                   `json:"url"`
	DocRef                  string                   `json:"doc_ref"`
	Metadata                map[string]interface{}   `json:"metadata"`
	DetectDuplicates        bool                     `json:"detect_duplicates"`
	DuplicateStrategy       string                   `json:"duplicate_strategy"`
	DocType                 string                   `json:"doc_type"`
	ContentType             string                   `json:"content_type"`
	Size                    int64                    `json:"size"`
	DetectContent           bool                     `json:"detect_content"`
	ContentDetectionProfile string                   `json:"content_detection_profile"`
	CustomDetectionRules    []map[string]interface{} `json:"custom_detection_rules"`
	ForceIngestion          bool                     `json:"force_ingestion"`
}

// NewIngestStartTool creates a new ingest start tool
func NewIngestStartTool(ingestManager *business.IngestManager, tenantRepo *repository.TenantRepository, bufferClient *buffer.Client, logger *zap.Logger) *IngestStartTool {
	return &IngestStartTool{
		ingestManager: ingestManager,
		tenantRepo:    tenantRepo,
		bufferClient:  bufferClient,
		logger:        logger.Named("ingest-start-tool"),
	}
}

// Definition returns the tool definition
func (t *IngestStartTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "rag_ingest_start",
		Description: "Start ingesting a document into the knowledge base. Provide either 'content' (text) or 'url' (to fetch content from URL)",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"deployment_id": map[string]interface{}{
					"type":        "string",
					"description": "Deployment ID (required). If tenant_id is omitted, the deployment default tenant is used",
				},
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID or name for multi-tenant isolation. Accepts XID format or a custom name (will be created if not exists)",
				},
				"title": map[string]interface{}{
					"type":        "string",
					"description": "Document title",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Document content (text). Required if 'url' is not provided",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "Source URL to fetch content from. Required if 'content' is not provided",
				},
				"doc_ref": map[string]interface{}{
					"type":        "string",
					"description": "Buffer reference key (for example cv-content:abc123). If provided, RAG resolves it to content before ingestion.",
				},
				"metadata": map[string]interface{}{
					"type":        "object",
					"description": "Additional metadata (optional)",
				},
				"detect_duplicates": map[string]interface{}{
					"type":        "boolean",
					"description": "Enable duplicate detection for this ingestion",
					"default":     false,
				},
				"duplicate_strategy": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"auto", "semantic", "hash", "cv", "none"},
					"default":     "auto",
					"description": "Duplicate detection strategy",
				},
				"doc_type": map[string]interface{}{
					"type":        "string",
					"default":     "generic",
					"description": "Document type (generic, cv, contract, invoice, etc.)",
				},
				"content_type": map[string]interface{}{
					"type":        "string",
					"description": "MIME type of the document (e.g., text/plain, application/pdf). Auto-detected from URL if not provided.",
				},
				"size": map[string]interface{}{
					"type":        "integer",
					"description": "Size of the document in bytes. Auto-calculated from content if not provided.",
				},
				"detect_content": map[string]interface{}{
					"type":        "boolean",
					"description": "Enable content inspection (PII/sensitive) at ingestion",
					"default":     false,
				},
				"content_detection_profile": map[string]interface{}{
					"type":    "string",
					"enum":    []string{"none", "pii_basic", "pii_strict", "cv_identifiability"},
					"default": "none",
				},
				"custom_detection_rules": map[string]interface{}{
					"type":        "array",
					"description": "Optional custom detection rules",
					"items": map[string]interface{}{
						"type": "object",
					},
				},
				"force_ingestion": map[string]interface{}{
					"type":        "boolean",
					"description": "Force re-ingestion even if the document was already successfully ingested (by URL match). Default false: skip if already completed.",
					"default":     false,
				},
			},
			Required: []string{"deployment_id", "title"},
		},
	}
}

// Handler returns the tool handler function
func (t *IngestStartTool) Handler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.logger.Debug("handling rag_ingest_start", zap.Any("arguments", request.Params.Arguments))

		if rawArgs, ok := request.Params.Arguments.(map[string]interface{}); ok {
			if ref, rawRef, found := extractBufferRef(rawArgs); found {
				t.logger.Debug("ingest request includes buffer reference",
					zap.String("buffer_ref", ref),
					zap.Any("buffer_ref_raw", rawRef))
			}
		}

		// Parse arguments
		var args ingestStartArgs

		argsBytes, err := json.Marshal(request.Params.Arguments)
		if err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}

		t.logger.Debug("ingest request parsed",
			zap.String("deployment_id", args.DeploymentID),
			zap.String("tenant_id", args.TenantID),
			zap.String("title", args.Title),
			zap.String("doc_ref", args.DocRef),
			zap.Bool("has_content", args.Content != ""),
			zap.Int("content_length", len(args.Content)),
			zap.String("content_preview", previewText(args.Content, 200)),
			zap.Bool("has_url", args.URL != ""),
			zap.String("url", args.URL),
			zap.Strings("metadata_keys", metadataKeys(args.Metadata)),
			zap.Bool("detect_duplicates", args.DetectDuplicates),
			zap.String("duplicate_strategy", args.DuplicateStrategy),
			zap.String("doc_type", args.DocType),
			zap.Bool("detect_content", args.DetectContent),
			zap.Bool("force_ingestion", args.ForceIngestion))

		var resolvedBufferRef string
		if rawArgs, ok := request.Params.Arguments.(map[string]interface{}); ok {
			if ref, _, found := extractBufferRef(rawArgs); found {
				resolvedBufferRef = normalizeBufferRef(ref)
			}
		}
		if resolvedBufferRef == "" {
			resolvedBufferRef = normalizeBufferRef(args.DocRef)
		}

		// Resolve doc_ref/_buffer_ref directly in RAG when available.
		if resolvedBufferRef != "" {
			if err := t.resolveBufferContent(ctx, resolvedBufferRef, &args); err != nil {
				if strings.TrimSpace(args.Content) == "" {
					t.logger.Warn("failed to resolve doc_ref in RAG", zap.String("doc_ref", resolvedBufferRef), zap.Error(err))
					return errorResult("failed to resolve doc_ref: " + err.Error()), nil
				}
				if errors.Is(err, errBufferClientNotConfigured) {
					t.logger.Debug("doc_ref provided but buffer client is not configured; using provided content",
						zap.String("doc_ref", resolvedBufferRef))
				} else {
					t.logger.Warn("failed to resolve doc_ref in RAG; falling back to provided content",
						zap.String("doc_ref", resolvedBufferRef),
						zap.Error(err))
				}
			}
		}

		// Validate
		if args.Title == "" {
			return errorResult("title is required"), nil
		}
		if args.DeploymentID == "" {
			return errorResult("deployment_id is required"), nil
		}
		if strings.TrimSpace(args.Content) == "" && strings.TrimSpace(args.URL) == "" {
			return errorResult("either 'content', 'url' or 'doc_ref' is required"), nil
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

		var customRules []inspect.DetectionRule
		if len(args.CustomDetectionRules) > 0 {
			b, _ := json.Marshal(args.CustomDetectionRules)
			_ = json.Unmarshal(b, &customRules)
		}

		// Determine source type
		sourceType := "text"
		if args.Content == "" && args.URL != "" {
			sourceType = "url"
		}

		// Build source
		source := business.IngestSource{
			Type:                    sourceType,
			URL:                     args.URL,
			Content:                 args.Content,
			Title:                   args.Title,
			Metadata:                args.Metadata,
			DetectDuplicates:        args.DetectDuplicates,
			DuplicateStrategy:       args.DuplicateStrategy,
			DocType:                 args.DocType,
			ContentType:             args.ContentType,
			Size:                    args.Size,
			DetectContent:           args.DetectContent,
			ContentDetectionProfile: args.ContentDetectionProfile,
			CustomDetectionRules:    customRules,
			ForceIngestion:          args.ForceIngestion,
		}

		// Start ingestion
		result, err := t.ingestManager.StartIngest(ctx, deploymentID, tenantID, source)
		if err != nil {
			t.logger.Error("ingestion failed", zap.Error(err))
			return errorResult("ingestion start failed: " + err.Error()), nil
		}

		// Build response
		response := map[string]interface{}{
			"status": result.Status,
		}
		if result.JobID.String() != "00000000000000000000" {
			response["job_id"] = result.JobID.String()
		}
		if result.DocumentID.String() != "00000000000000000000" {
			response["document_id"] = result.DocumentID.String()
		}
		if result.Message != "" {
			response["message"] = result.Message
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

func previewText(input string, limit int) string {
	if limit <= 0 || input == "" {
		return ""
	}
	trimmed := strings.TrimSpace(input)
	trimmed = strings.ReplaceAll(trimmed, "\n", " ")
	trimmed = strings.ReplaceAll(trimmed, "\r", " ")
	trimmed = strings.ReplaceAll(trimmed, "\t", " ")
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit]) + "..."
}

func metadataKeys(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func extractBufferRef(args map[string]interface{}) (string, interface{}, bool) {
	for _, key := range []string{"doc_ref", "_buffer_ref", "buffer_ref", "buffer"} {
		if raw, ok := args[key]; ok {
			switch val := raw.(type) {
			case string:
				return val, raw, true
			case map[string]interface{}:
				if ref, ok := val["key"].(string); ok {
					return ref, raw, true
				}
				if ref, ok := val["ref"].(string); ok {
					return ref, raw, true
				}
			}
			return "", raw, true
		}
	}
	return "", nil, false
}

func normalizeBufferRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "buffer://")
	ref = strings.TrimPrefix(ref, "buffer:/")
	return strings.TrimSpace(ref)
}

func (t *IngestStartTool) resolveBufferContent(ctx context.Context, bufferRef string, args *ingestStartArgs) error {
	if t.bufferClient == nil {
		return errBufferClientNotConfigured
	}

	payload, err := t.bufferClient.Load(ctx, bufferRef)
	if err != nil {
		return err
	}

	// Handle PDF binary content from buffer
	if payload.MimeType == "application/pdf" && payload.Binary != "" {
		t.logger.Info("resolved PDF binary from buffer",
			zap.String("buffer_ref", bufferRef),
			zap.Int("binary_length", len(payload.Binary)),
			zap.String("mime_type", payload.MimeType))

		args.Content = payload.Binary // base64-encoded PDF data
		args.ContentType = "application/pdf"
		if args.Size == 0 {
			args.Size = int64(len(payload.Binary))
		}
		if args.Metadata == nil {
			args.Metadata = make(map[string]interface{})
		}
		args.Metadata["buffer_ref"] = bufferRef
		args.Metadata["source_format"] = "pdf"
		return nil
	}

	content := strings.TrimSpace(payload.Text)
	if content == "" && payload.Structured != nil {
		if asString, ok := payload.Structured.(string); ok {
			content = strings.TrimSpace(asString)
		} else {
			raw, marshalErr := json.Marshal(payload.Structured)
			if marshalErr == nil {
				content = strings.TrimSpace(string(raw))
			}
		}
	}
	if content == "" {
		return fmt.Errorf("buffer payload is empty")
	}

	args.Content = content
	if args.ContentType == "" && payload.MimeType != "" {
		args.ContentType = payload.MimeType
	}
	if args.Size == 0 {
		args.Size = int64(len(content))
	}
	if args.Metadata == nil {
		args.Metadata = make(map[string]interface{})
	}
	args.Metadata["buffer_ref"] = bufferRef

	t.logger.Debug("resolved buffer ref in RAG",
		zap.String("buffer_ref", bufferRef),
		zap.Int("content_length", len(args.Content)),
		zap.String("content_preview", previewText(args.Content, 200)))

	return nil
}
