package mcp

import (
	"context"
	"fmt"
	"sort"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type TimelineTool struct {
	storage storage.Manager
	cfg     *config.Config
	wrapper *ResponseWrapper
	logger  *zap.Logger
}

func NewTimelineTool(storage storage.Manager, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *TimelineTool {
	return &TimelineTool{storage: storage, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.timeline")}
}

func (t *TimelineTool) Name() string { return "brain.timeline" }

func (t *TimelineTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.timeline",
		Description: "Fetch nearby chunks around one or more search hits, grouped by document. Use after brain.search to add local context without loading full content.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"hit_ids":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "minItems": 1},
				"window":    map[string]interface{}{"type": "integer", "description": "Neighbor chunk window around each hit (default 2, max 5)"},
				"group_by":  map[string]interface{}{"type": "string", "enum": []string{"document"}, "description": "Only 'document' is supported in V1"},
				"include":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional fields: summary, metadata, content"},
				"tenant_id": map[string]interface{}{"type": "string"},
			},
			Required: []string{"hit_ids"},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *TimelineTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			HitIDs   []string `json:"hit_ids"`
			Window   int      `json:"window"`
			GroupBy  string   `json:"group_by"`
			Include  []string `json:"include"`
			TenantID string   `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}
		if len(args.HitIDs) == 0 {
			return errorResult("hit_ids is required"), nil
		}
		if args.GroupBy != "" && args.GroupBy != "document" {
			return errorResult("group_by must be 'document'"), nil
		}

		window := args.Window
		if window <= 0 {
			window = 2
		}
		if window > 5 {
			return errorResult("window must be <= 5"), nil
		}

		if len(args.Include) == 0 {
			args.Include = []string{"summary", "location", "metadata"}
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		db, err := t.storage.OpenTenant(ctx, tenantID)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		type docState struct {
			document  compactDocument
			source    minimalSource
			anchors   map[string]struct{}
			neighbors map[string]timelineItem
		}

		documents := map[string]*docState{}
		for _, hitID := range args.HitIDs {
			kind, value, err := parseBrainID(hitID)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if kind != "chunk" {
				return errorResult(fmt.Sprintf("unsupported hit id kind: %s", kind)), nil
			}

			anchor, err := fetchChunkByID(ctx, db, tenantID, value)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			state := documents[anchor.DocumentID]
			if state == nil {
				state = &docState{
					document:  anchor.Document,
					source:    anchor.Source,
					anchors:   map[string]struct{}{},
					neighbors: map[string]timelineItem{},
				}
				documents[anchor.DocumentID] = state
			}
			state.anchors[anchor.ChunkID] = struct{}{}

			chunks, err := fetchChunkWindow(ctx, db, tenantID, anchor.DocumentID, anchor.ChunkIndex-window, anchor.ChunkIndex+window)
			if err != nil {
				return errorResult(err.Error()), nil
			}

			for _, chunk := range chunks {
				role := "before"
				switch {
				case chunk.ChunkID == anchor.ChunkID:
					role = "anchor"
				case chunk.ChunkIndex > anchor.ChunkIndex:
					role = "after"
				}

				item := timelineItem{
					ID:         canonicalBrainID("chunk", chunk.ChunkID),
					Role:       role,
					ChunkIndex: chunk.ChunkIndex,
					ChunkType:  chunk.ChunkType,
					SymbolName: chunk.SymbolName,
					StartLine:  chunk.StartLine,
					EndLine:    chunk.EndLine,
				}
				if includeField(args.Include, "summary") {
					item.Summary = buildSummary(chunk.Summary, chunk.Content, defaultSearchSummaryLimit)
				}
				if includeField(args.Include, "metadata") {
					item.Metadata = chunk.ChunkMeta
				}
				if includeField(args.Include, "content") {
					item.Content = chunk.Content
				}

				existing, ok := state.neighbors[item.ID]
				if !ok || item.Role == "anchor" || (existing.Role == "before" && item.Role == "after") {
					state.neighbors[item.ID] = item
				}
			}
		}

		response := timelineResponse{Groups: make([]timelineGroup, 0, len(documents))}
		docIDs := make([]string, 0, len(documents))
		for docID := range documents {
			docIDs = append(docIDs, docID)
		}
		sort.Strings(docIDs)

		for _, docID := range docIDs {
			state := documents[docID]
			items := make([]timelineItem, 0, len(state.neighbors))
			for _, item := range state.neighbors {
				items = append(items, item)
			}
			sortTimelineItems(items)

			estimatedChars := 0
			for _, item := range items {
				estimatedChars += len(item.Summary) + len(item.Content)
			}

			response.Groups = append(response.Groups, timelineGroup{
				Document:        state.document,
				Source:          state.source,
				EstimatedChars:  estimatedChars,
				EstimatedTokens: estimateTokens(estimatedChars),
				Items:           items,
			})
		}

		return t.wrapper.Wrap(response), nil
	}
}
