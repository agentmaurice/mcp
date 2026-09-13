package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// AttachDocumentTool implements memory.documents.register.
type AttachDocumentTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewAttachDocumentTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *AttachDocumentTool {
	return &AttachDocumentTool{storage: storage, cfg: cfg, logger: logger.Named("memory.documents.register")}
}

func (t *AttachDocumentTool) Name() string {
	return "memory.documents.register"
}

func (t *AttachDocumentTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.documents.register",
		Description: "Register an external document with metadata",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"tenant_id": map[string]interface{}{
					"type":        "string",
					"description": "Tenant ID (optional, defaults to context/default tenant)",
				},
				"documentId": map[string]interface{}{"type": "string", "minLength": 1},
				"uri":        map[string]interface{}{"type": "string", "minLength": 1},
				"mimeType":   map[string]interface{}{"type": "string", "minLength": 1},
				"title":      map[string]interface{}{"type": "string"},
				"tags": map[string]interface{}{
					"type":    "array",
					"items":   map[string]interface{}{"type": "string"},
					"default": []string{},
				},
				"metadata": map[string]interface{}{"type": "object", "additionalProperties": true},
				"source": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"system":    map[string]interface{}{"type": "string"},
						"timestamp": map[string]interface{}{"type": "string"},
						"traceId":   map[string]interface{}{"type": "string"},
					},
					"required": []string{"system", "timestamp"},
				},
				"writer": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"appId":     map[string]interface{}{"type": "string"},
						"actorId":   map[string]interface{}{"type": "string"},
						"actorType": map[string]interface{}{"type": "string", "enum": []string{"agent", "human", "system"}, "default": "agent"},
					},
					"required": []string{"appId", "actorId"},
				},
			},
			Required: []string{"documentId", "uri", "mimeType", "source", "writer"},
		},
	}
}

func (t *AttachDocumentTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			TenantID   string         `json:"tenant_id"`
			DocumentID string         `json:"documentId"`
			URI        string         `json:"uri"`
			MimeType   string         `json:"mimeType"`
			Title      string         `json:"title"`
			Tags       []string       `json:"tags"`
			Metadata   map[string]any `json:"metadata"`
			Source     SourceInput    `json:"source"`
			Writer     WriterInput    `json:"writer"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult("failed to parse arguments"), nil
		}
		if args.DocumentID == "" || args.URI == "" || args.MimeType == "" {
			return errorResult("documentId, uri, and mimeType are required"), nil
		}
		if !strings.Contains(args.MimeType, "/") {
			return errorResult("invalid mimeType"), nil
		}
		if args.Source.System == "" || args.Source.Timestamp == "" {
			return errorResult("source.system and source.timestamp are required"), nil
		}
		if args.Writer.AppID == "" || args.Writer.ActorID == "" {
			return errorResult("writer.appId and writer.actorId are required"), nil
		}

		if err := validateLocalDocument(t.cfg, args.URI); err != nil {
			return errorResult(err.Error()), nil
		}

		timestamp, err := time.Parse(time.RFC3339, args.Source.Timestamp)
		if err != nil {
			return errorResult("invalid source.timestamp"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		err = t.storage.WithWriter(ctx, tenantID, func(ctx context.Context, q storage.Querier) error {
			exists, err := queryScalarString(ctx, q, rebindQuery(t.storage, "SELECT document_id FROM documents WHERE document_id = ?"), args.DocumentID)
			if err != nil {
				return err
			}
			if exists != "" {
				return fmt.Errorf("document already exists")
			}

			sourceID, err := insertSource(ctx, t.storage, q, args.Source, timestamp)
			if err != nil {
				return err
			}
			writerID, err := ensureWriter(ctx, t.storage, q, args.Writer)
			if err != nil {
				return err
			}

			tagsJSON := "[]"
			if len(args.Tags) > 0 {
				data, _ := json.Marshal(args.Tags)
				tagsJSON = string(data)
			}
			metadataJSON := "{}"
			if args.Metadata != nil {
				data, _ := json.Marshal(args.Metadata)
				metadataJSON = string(data)
			}

			if _, err := q.ExecContext(ctx,
				rebindQuery(t.storage, "INSERT INTO documents (document_id, uri, mime_type, title, tags, metadata, created_at, source_id, writer_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"),
				args.DocumentID, args.URI, args.MimeType, args.Title, tagsJSON, metadataJSON, time.Now().UTC(), sourceID, writerID); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(map[string]any{"documentId": args.DocumentID, "registered": true}), nil
	}
}

func validateLocalDocument(cfg *config.Config, uri string) error {
	if cfg == nil || !cfg.Storage.ValidateLocalFiles {
		return nil
	}
	path, ok := localPathFromURI(uri)
	if !ok {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("document not found")
	}
	if cfg.Storage.MaxDocumentSizeByte > 0 && info.Size() > cfg.Storage.MaxDocumentSizeByte {
		return fmt.Errorf("document exceeds max size")
	}
	return nil
}

func localPathFromURI(uri string) (string, bool) {
	if strings.HasPrefix(uri, "file://") {
		parsed, err := url.Parse(uri)
		if err != nil {
			return "", false
		}
		return filepath.FromSlash(parsed.Path), true
	}
	if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, ".") {
		return uri, true
	}
	return "", false
}
