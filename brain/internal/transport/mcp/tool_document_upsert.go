package mcp

import (
	"context"
	"errors"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer"
	bufferclient "github.com/agentmaurice/mcpchatui/mcp/brain/internal/platform/buffer"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type documentBufferReader interface {
	Configured() bool
	LoadText(ctx context.Context, key string, maxBytes int64) ([]byte, string, error)
}

type DocumentUpsertTool struct {
	cfg          *config.Config
	pipeline     *indexer.Pipeline
	bufferClient documentBufferReader
	logger       *zap.Logger
}

func NewDocumentUpsertTool(cfg *config.Config, pipeline *indexer.Pipeline, bufferClient documentBufferReader, logger *zap.Logger) *DocumentUpsertTool {
	return &DocumentUpsertTool{cfg: cfg, pipeline: pipeline, bufferClient: bufferClient, logger: logger.Named("brain.document.upsert")}
}

func (t *DocumentUpsertTool) Name() string { return "brain.document.upsert" }

func (t *DocumentUpsertTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        t.Name(),
		Description: "Create or update one textual document from inline content or an opaque authenticated buffer reference. Use this instead of brain.index when the producer is another MCP tool.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"source_uri":   map[string]any{"type": "string", "description": "Stable logical collection URI"},
				"document_uri": map[string]any{"type": "string", "description": "Stable unique document URI"},
				"content_type": map[string]any{"type": "string", "default": "text/plain", "description": "text/* or application/json"},
				"content":      map[string]any{"type": "string", "description": "Inline UTF-8 content; mutually exclusive with buffer_ref"},
				"buffer_ref":   map[string]any{"type": "string", "description": "Opaque raw buffer key; URLs and buffer:// references are rejected"},
				"metadata":     map[string]any{"type": "object"},
				"wait_for_job": map[string]any{"type": "boolean", "default": false},
				"tenant_id":    map[string]any{"type": "string"},
			},
			Required: []string{"source_uri", "document_uri"},
		},
		Annotations: mcplib.ToolAnnotation{
			ReadOnlyHint:    boolPtr(false),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
		},
	}
}

func (t *DocumentUpsertTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			SourceURI   string         `json:"source_uri"`
			DocumentURI string         `json:"document_uri"`
			ContentType string         `json:"content_type"`
			Content     string         `json:"content"`
			BufferRef   string         `json:"buffer_ref"`
			Metadata    map[string]any `json:"metadata"`
			WaitForJob  bool           `json:"wait_for_job"`
			TenantID    string         `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return codedErrorResult("invalid_arguments", "Arguments do not match the tool schema.", false), nil
		}
		args.SourceURI = strings.TrimSpace(args.SourceURI)
		args.DocumentURI = strings.TrimSpace(args.DocumentURI)
		args.BufferRef = strings.TrimSpace(args.BufferRef)
		if args.SourceURI == "" || args.DocumentURI == "" {
			return codedErrorResult("invalid_arguments", "source_uri and document_uri are required.", false), nil
		}
		hasContent := args.Content != ""
		hasBuffer := args.BufferRef != ""
		if hasContent == hasBuffer {
			return codedErrorResult("invalid_arguments", "Provide exactly one of content or buffer_ref.", false), nil
		}
		if hasBuffer && (strings.Contains(args.BufferRef, "://") || strings.ContainsAny(args.BufferRef, "\r\n")) {
			return codedErrorResult("invalid_arguments", "buffer_ref must be an opaque raw buffer key, not a URL.", false), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return codedErrorResult("invalid_arguments", "tenant_id is invalid or missing.", false), nil
		}
		maxBytes := t.cfg.Indexing.MaxDocumentBytes
		if maxBytes <= 0 {
			maxBytes = 10 * 1024 * 1024
		}

		content := []byte(args.Content)
		if hasBuffer {
			if t.bufferClient == nil || !t.bufferClient.Configured() {
				return codedErrorResult("buffer_unavailable", "The authenticated buffer client is not configured.", true), nil
			}
			resolved, detectedType, loadErr := t.bufferClient.LoadText(ctx, args.BufferRef, maxBytes)
			if loadErr != nil {
				switch {
				case errors.Is(loadErr, bufferclient.ErrNotFound):
					return codedErrorResult("buffer_not_found", "The buffer content was not found or has expired.", false), nil
				case errors.Is(loadErr, bufferclient.ErrTooLarge):
					return codedErrorResult("payload_too_large", "The buffered document exceeds the configured size limit.", false), nil
				case errors.Is(loadErr, bufferclient.ErrBinaryContent):
					return codedErrorResult("unsupported_content_type", "Binary buffer content is not supported.", false), nil
				case errors.Is(loadErr, bufferclient.ErrInvalidPayload):
					return codedErrorResult("buffer_payload_invalid", "The buffer payload is invalid or empty.", false), nil
				default:
					return codedErrorResult("buffer_unavailable", "The buffer service could not be reached.", true), nil
				}
			}
			content = resolved
			if args.ContentType == "" {
				args.ContentType = detectedType
			}
		}
		if int64(len(content)) > maxBytes {
			return codedErrorResult("payload_too_large", "The document exceeds the configured size limit.", false), nil
		}
		if len(content) == 0 || !utf8.Valid(content) {
			return codedErrorResult("unsupported_content_type", "The document must contain valid UTF-8 text.", false), nil
		}
		if args.ContentType == "" {
			args.ContentType = "text/plain"
		}
		mediaType, _, mimeErr := mime.ParseMediaType(args.ContentType)
		if mimeErr != nil || (mediaType != "application/json" && !strings.HasPrefix(mediaType, "text/")) {
			return codedErrorResult("unsupported_content_type", "Only text/* and application/json documents are supported.", false), nil
		}
		if args.Metadata == nil {
			args.Metadata = make(map[string]any)
		}
		args.Metadata["content_type"] = mediaType

		upsert, err := t.pipeline.UpsertDocument(ctx, indexer.DocumentUpsertRequest{
			TenantID: tenantID, SourceURI: args.SourceURI, DocumentURI: args.DocumentURI,
			ContentType: mediaType, Content: content, Metadata: args.Metadata,
		})
		if err != nil {
			t.logger.Error("document upsert preparation failed", zap.Error(err))
			return codedErrorResult("indexation_failed", "The document indexation could not be scheduled.", true), nil
		}
		if args.WaitForJob && upsert.Status != "completed" {
			status, waitErr := t.pipeline.WaitForJob(ctx, tenantID, upsert.JobID)
			if waitErr != nil {
				return codedErrorResult("indexation_failed", "The indexation job could not be observed.", true), nil
			}
			upsert.Status = status
		}
		return structuredResult(map[string]any{
			"job_id": upsert.JobID, "source_id": upsert.SourceID, "document_id": upsert.DocumentID,
			"status": upsert.Status, "outcome": upsert.Outcome,
			"source_uri": args.SourceURI, "document_uri": args.DocumentURI,
		}), nil
	}
}
