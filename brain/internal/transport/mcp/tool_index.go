package mcp

import (
	"context"
	"encoding/json"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/indexer"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/storage"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type IndexTool struct {
	storage  storage.Manager
	cfg      *config.Config
	pipeline *indexer.Pipeline
	logger   *zap.Logger
}

func NewIndexTool(storage storage.Manager, cfg *config.Config, pipeline *indexer.Pipeline, logger *zap.Logger) *IndexTool {
	return &IndexTool{storage: storage, cfg: cfg, pipeline: pipeline, logger: logger.Named("brain.index")}
}

func (t *IndexTool) Name() string { return "brain.index" }

func (t *IndexTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.index",
		Description: "Index a data source. Launches indexation in the background and returns a job ID for tracking.",
		InputSchema: mcplib.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"source_type":  map[string]interface{}{"type": "string", "description": "Source type: 'filesystem'", "enum": []string{"filesystem"}},
				"source_uri":   map[string]interface{}{"type": "string", "description": "Path or URL of the source"},
				"display_name": map[string]interface{}{"type": "string", "description": "Human-readable name for the source"},
				"config":       map[string]interface{}{"type": "object", "description": "Connector-specific configuration"},
				"tenant_id":    map[string]interface{}{"type": "string"},
				"allow_empty":  map[string]interface{}{"type": "boolean", "default": false, "description": "Treat an empty source as a successful no-op instead of source_empty"},
			},
			Required: []string{"source_type", "source_uri"},
		},
		Annotations: mcplib.ToolAnnotation{
			ReadOnlyHint:    boolPtr(false),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
		},
	}
}

func (t *IndexTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		var args struct {
			SourceType  string          `json:"source_type"`
			SourceURI   string          `json:"source_uri"`
			DisplayName string          `json:"display_name"`
			Config      json.RawMessage `json:"config"`
			TenantID    string          `json:"tenant_id"`
			AllowEmpty  bool            `json:"allow_empty"`
		}
		if err := parseArgs(request.Params.Arguments, &args); err != nil {
			return errorResult(err.Error()), nil
		}

		if args.SourceType == "" {
			return errorResult("source_type is required"), nil
		}
		if args.SourceURI == "" {
			return errorResult("source_uri is required"), nil
		}

		tenantID, err := resolveTenantID(ctx, t.cfg, args.TenantID, request.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		// Auto-build config for filesystem if not provided
		if args.Config == nil && args.SourceType == "filesystem" {
			cfg := map[string]interface{}{
				"path": args.SourceURI,
			}
			args.Config, _ = json.Marshal(cfg)
		}
		if args.SourceType == "filesystem" && args.AllowEmpty {
			var cfg map[string]any
			if len(args.Config) > 0 {
				_ = json.Unmarshal(args.Config, &cfg)
			}
			if cfg == nil {
				cfg = map[string]any{"path": args.SourceURI}
			}
			cfg["allow_empty"] = true
			args.Config, _ = json.Marshal(cfg)
		}

		if args.DisplayName == "" {
			args.DisplayName = args.SourceURI
		}

		jobID, err := t.pipeline.IndexSource(ctx, tenantID, args.SourceType, args.SourceURI, args.DisplayName, args.Config)
		if err != nil {
			return errorResult(err.Error()), nil
		}

		return structuredResult(map[string]any{
			"job_id":      jobID,
			"source_type": args.SourceType,
			"source_uri":  args.SourceURI,
			"status":      "indexing",
			"message":     "Indexation started. Use brain.index.status to track progress.",
		}), nil
	}
}
