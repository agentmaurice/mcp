package mcp

import (
	"encoding/json"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ResponseWrapper handles response wrapping for large payloads.
// In Phase 1, it simply returns responses directly.
type ResponseWrapper struct {
	logger *zap.Logger
}

// NewResponseWrapper creates a new ResponseWrapper.
func NewResponseWrapper(logger *zap.Logger) *ResponseWrapper {
	return &ResponseWrapper{logger: logger}
}

// Wrap wraps a payload into a CallToolResult, potentially buffering large payloads.
func (w *ResponseWrapper) Wrap(payload any) *mcplib.CallToolResult {
	data, _ := json.Marshal(payload)
	structured := payload
	if payload == nil {
		structured = json.RawMessage("null")
	}
	return &mcplib.CallToolResult{
		Content: []mcplib.Content{
			mcplib.TextContent{
				Type: "text",
				Text: string(data),
			},
		},
		StructuredContent: structured,
	}
}
