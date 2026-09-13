package mcp

import (
	"context"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// HealthTool implements brain.health.
type HealthTool struct {
	logger *zap.Logger
}

func NewHealthTool(logger *zap.Logger) *HealthTool {
	return &HealthTool{logger: logger.Named("brain.health")}
}

func (t *HealthTool) Name() string { return "brain.health" }

func (t *HealthTool) Definition() mcplib.Tool {
	return mcplib.Tool{
		Name:        "brain.health",
		Description: "Health check for the Brain server",
		InputSchema: mcplib.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}},
	}
}

func (t *HealthTool) Handler() ToolHandler {
	return func(ctx context.Context, request mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		return structuredResult(map[string]any{
			"status":  "ok",
			"version": serviceVersion,
		}), nil
	}
}
