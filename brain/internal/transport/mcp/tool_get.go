package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type GetTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewGetTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *GetTool {
	return &GetTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.get")}
}

func (t *GetTool) Name() string { return "brain.get" }

func (t *GetTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.get",
		Description: "Fetch exact details for chunk or document IDs discovered via brain.search or brain.timeline.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"ids":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "minItems": 1},
				"include":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional fields: summary, content, context, metadata"},
				"max_chars": map[string]interface{}{"type": "integer", "description": "Soft max serialized response size in characters (default 12000, max 40000)"},
				"tenant_id": map[string]interface{}{"type": "string"},
			},
			Required: []string{"ids"},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *GetTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			IDs      []string `json:"ids"`
			Include  []string `json:"include"`
			MaxChars int      `json:"max_chars"`
			TenantID string   `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}
		if len(args.IDs) == 0 {
			return errorResult("ids is required"), nil
		}
		if len(args.Include) == 0 {
			args.Include = []string{"summary", "content", "context", "metadata"}
		}

		maxChars := args.MaxChars
		if maxChars <= 0 {
			maxChars = 12000
		}
		if maxChars > 40000 {
			return errorResult("max_chars must be <= 40000"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		response := getResponse{Items: make([]getItem, 0, len(args.IDs))}
		for _, rawID := range args.IDs {
			kind, value, err := parseBrainID(rawID)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			switch kind {
			case "chunk":
				record, err := fetchChunkByID(ctx, db, tenantID, value)
				if err != nil {
					return errorResult(err.Error()), nil
				}
				item := getItem{
					Kind:     "chunk",
					ID:       rawID,
					Document: record.Document,
					Source:   record.Source,
					Chunk: &getChunkPayload{
						ID:         canonicalBrainID("chunk", record.ChunkID),
						ChunkIndex: record.ChunkIndex,
						ChunkType:  record.ChunkType,
						SymbolName: record.SymbolName,
						StartLine:  record.StartLine,
						EndLine:    record.EndLine,
					},
				}
				if includeField(args.Include, "summary") {
					item.Chunk.Summary = buildSummary(record.Summary, record.Content, defaultSearchSummaryLimit)
				}
				if includeField(args.Include, "content") {
					item.Chunk.Content = limitString(record.Content, defaultChunkContentLimit)
				}
				if includeField(args.Include, "context") {
					item.Chunk.Context = limitString(record.Context, defaultContextLimit)
				}
				if includeField(args.Include, "metadata") {
					item.Chunk.Metadata = record.ChunkMeta
				}
				response.Items = append(response.Items, item)

			case "document":
				document, err := fetchDocumentByID(ctx, db, tenantID, value)
				if err != nil {
					return errorResult(err.Error()), nil
				}
				chunks, err := fetchDocumentChunks(ctx, db, tenantID, value)
				if err != nil {
					return errorResult(err.Error()), nil
				}

				item := getItem{
					Kind:     "document",
					ID:       rawID,
					Document: document.Document,
					Source:   document.Source,
					Chunks:   make([]getChunkReference, 0, len(chunks)),
				}
				for _, chunk := range chunks {
					entry := getChunkReference{
						ID:         canonicalBrainID("chunk", chunk.ChunkID),
						ChunkIndex: chunk.ChunkIndex,
						ChunkType:  chunk.ChunkType,
						SymbolName: chunk.SymbolName,
						StartLine:  chunk.StartLine,
						EndLine:    chunk.EndLine,
					}
					if includeField(args.Include, "summary") {
						entry.Summary = buildSummary(chunk.Summary, chunk.Content, defaultSearchSummaryLimit)
					}
					if includeField(args.Include, "content") {
						entry.ContentPreview = buildSummary("", chunk.Content, defaultContentPreviewLimit)
					}
					if includeField(args.Include, "metadata") {
						entry.Metadata = chunk.ChunkMeta
					}
					trial := item
					trial.Chunks = append(append([]getChunkReference{}, item.Chunks...), entry)
					if serializedSize(getResponse{Items: append(append([]getItem{}, response.Items...), trial)}) > maxChars {
						response.Truncated = true
						break
					}
					item.Chunks = append(item.Chunks, entry)
				}

				if response.Truncated && len(item.Chunks) == 0 {
					item.Chunks = append(item.Chunks, getChunkReference{
						ID:             "",
						ContentPreview: "truncated before chunks could be included",
					})
				}
				response.Items = append(response.Items, item)

			default:
				return errorResult(fmt.Sprintf("unsupported id kind: %s", kind)), nil
			}

			if exceedsMaxChars(response, maxChars) {
				response.Truncated = true
				break
			}
		}

		response.ReturnedChars = serializedSize(response)
		return t.wrapper.Wrap(response), nil
	}
}

func exceedsMaxChars(response getResponse, maxChars int) bool {
	if maxChars <= 0 {
		return false
	}
	return serializedSize(response) > maxChars
}

func serializedSize(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(data)
}

func limitString(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
