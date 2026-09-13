package mcp

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type KeywordTool struct {
	orchestrator *search.Orchestrator
	cfg          *config.Config
	wrapper      *ResponseWrapper
	logger       *zap.Logger
}

func NewKeywordTool(orchestrator *search.Orchestrator, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *KeywordTool {
	return &KeywordTool{orchestrator: orchestrator, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.keyword")}
}

func (t *KeywordTool) Name() string { return "brain.keyword" }

func (t *KeywordTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.keyword",
		Description: "BM25 full-text keyword search. Best for exact identifiers, function names, error codes, and specific terms.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"query":       map[string]interface{}{"type": "string", "description": "Keywords to search for"},
				"max_results": map[string]interface{}{"type": "integer", "description": "Maximum results (default 20)"},
				"doc_types":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"languages":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"source_ids":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"tenant_id":   map[string]interface{}{"type": "string"},
			},
			Required: []string{"query"},
		},
		Annotations: mcplib.ToolAnnotation{ReadOnlyHint: boolPtr(true)},
	}
}

func (t *KeywordTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			Query      string   `json:"query"`
			MaxResults int      `json:"max_results"`
			DocTypes   []string `json:"doc_types"`
			Languages  []string `json:"languages"`
			SourceIDs  []string `json:"source_ids"`
			TenantID   string   `json:"tenant_id"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		result, err := t.orchestrator.Search(ctx, tenantID, args.Query, shared.SearchOpts{
			Mode:       "bm25",
			MaxResults: args.MaxResults,
			DocTypes:   args.DocTypes,
			Languages:  args.Languages,
			SourceIDs:  args.SourceIDs,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return t.wrapper.Wrap(result), nil
	}
}
