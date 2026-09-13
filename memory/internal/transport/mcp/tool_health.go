package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// HealthTool implements memory.health.
type HealthTool struct {
	logger *zap.Logger
}

func NewHealthTool(logger *zap.Logger) *HealthTool {
	return &HealthTool{logger: logger.Named("memory.health")}
}

func (t *HealthTool) Name() string {
	return "memory.health"
}

func (t *HealthTool) Definition() mcp.Tool {
	return mcp.Tool{
		Name:        "memory.health",
		Description: "Health check",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}},
	}
}

func (t *HealthTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return structuredResult(map[string]any{"status": "ok"}), nil
	}
}
