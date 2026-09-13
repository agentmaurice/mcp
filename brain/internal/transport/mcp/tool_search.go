package mcp

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type SearchTool struct {
	orchestrator *search.Orchestrator
	cfg          *config.Config
	wrapper      *ResponseWrapper
	logger       *zap.Logger
}

func NewSearchTool(orchestrator *search.Orchestrator, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *SearchTool {
	return &SearchTool{orchestrator: orchestrator, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.search")}
}

func (t *SearchTool) Name() string { return "brain.search" }

func (t *SearchTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.search",
		Description: "Intelligent search across indexed codebases and documentation. Automatically selects the best search mode (keyword, semantic, or hybrid) based on the query.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"query":       map[string]interface{}{"type": "string", "description": "Search query or question"},
				"mode":        map[string]interface{}{"type": "string", "description": "Search mode: 'auto', 'bm25', 'vector', 'hybrid'. Default: 'auto'", "enum": []string{"auto", "bm25", "vector", "hybrid"}},
				"max_results": map[string]interface{}{"type": "integer", "description": "Maximum results to return (default 20, max 100)"},
				"alpha":       map[string]interface{}{"type": "number", "description": "Weight for vector vs BM25 in hybrid mode (0.0=full BM25, 1.0=full vector, default 0.6)"},
				"doc_types":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Filter by document type: 'code', 'markdown', 'config'"},
				"languages":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Filter by programming language"},
				"source_ids":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Filter by source IDs"},
				"tenant_id":   map[string]interface{}{"type": "string", "description": "Tenant ID (optional)"},
			},
			Required: []string{"query"},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *SearchTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			Query      string   `json:"query"`
			Mode       string   `json:"mode"`
			MaxResults int      `json:"max_results"`
			Alpha      float64  `json:"alpha"`
			DocTypes   []string `json:"doc_types"`
			Languages  []string `json:"languages"`
			SourceIDs  []string `json:"source_ids"`
			TenantID   string   `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}
		if args.Query == "" {
			return errorResult("query is required"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		opts := shared.SearchOpts{
			Mode:       args.Mode,
			MaxResults: args.MaxResults,
			Alpha:      args.Alpha,
			DocTypes:   args.DocTypes,
			Languages:  args.Languages,
			SourceIDs:  args.SourceIDs,
		}

		result, err := t.orchestrator.Search(ctx, tenantID, args.Query, opts)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return t.wrapper.Wrap(buildCompactSearchResponse(result)), nil
	}
}

func boolPtr(b bool) *bool { return &b }
