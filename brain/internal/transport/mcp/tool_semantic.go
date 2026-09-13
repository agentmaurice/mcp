package mcp

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/search"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type SemanticTool struct {
	orchestrator *search.Orchestrator
	cfg          *config.Config
	wrapper      *ResponseWrapper
	logger       *zap.Logger
}

func NewSemanticTool(orchestrator *search.Orchestrator, cfg *config.Config, wrapper *ResponseWrapper, logger *zap.Logger) *SemanticTool {
	return &SemanticTool{orchestrator: orchestrator, cfg: cfg, wrapper: wrapper, logger: logger.Named("brain.semantic")}
}

func (t *SemanticTool) Name() string { return "brain.semantic" }

func (t *SemanticTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.semantic",
		Description: "Semantic vector search using embeddings. Best for natural language questions and conceptual queries.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"query":       map[string]interface{}{"type": "string", "description": "Natural language query"},
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

func (t *SemanticTool) Handler() ToolHandler {
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
			Mode:       "vector",
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
