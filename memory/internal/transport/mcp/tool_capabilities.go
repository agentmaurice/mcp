package mcp

import (
	"context"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/storage"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// CapabilitiesTool implements memory.capabilities.
type CapabilitiesTool struct {
	storage storage.Manager
	cfg     *config.Config
	logger  *zap.Logger
}

func NewCapabilitiesTool(storage storage.Manager, cfg *config.Config, logger *zap.Logger) *CapabilitiesTool {
	return &CapabilitiesTool{storage: storage, cfg: cfg, logger: logger.Named("memory.capabilities")}
}

func (t *CapabilitiesTool) Name() string {
	return "memory.capabilities"
}

func (t *CapabilitiesTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.capabilities",
		Description: "Describe backend capabilities and SQL subset",
		InputSchema: mcp.ToolInputSchema{Type: "object"},
	}
}

func (t *CapabilitiesTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backend := "unknown"
		dialect := "unknown"
		if t.storage != nil {
			backend = t.storage.Backend()
			dialect = t.storage.Dialect()
		}

		payload := map[string]any{
			"backend": backend,
			"dialect": dialect,
			"sqlSubset": map[string]any{
				"portable":           true,
				"allowCTE":           false,
				"allowWindows":       false,
				"allowUnion":         false,
				"allowJsonOperators": false,
				"requireViewAccess":  t.cfg.Security.RequireViewsOnly,
				"allowSelectStar":    !t.cfg.Query.DisallowSelectStar,
			},
			"limits": map[string]any{
				"maxRows":   t.cfg.Query.MaxRowsDefault,
				"timeoutMs": t.cfg.Query.TimeoutMsDefault,
			},
		}

		return structuredResult(payload), nil
	}
}
