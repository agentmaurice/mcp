package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/platform/buffer"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ResponseWrapper encapsulates buffering logic for MCP responses
type ResponseWrapper struct {
	bufferClient *buffer.Client
	enabled      bool
	logger       *zap.Logger
}

// NewResponseWrapper creates a new response wrapper
func NewResponseWrapper(client *buffer.Client, logger *zap.Logger) *ResponseWrapper {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ResponseWrapper{
		bufferClient: client,
		enabled:      client != nil,
		logger:       logger.Named("response-wrapper"),
	}
}

// IsEnabled returns whether buffering is enabled
func (w *ResponseWrapper) IsEnabled() bool {
	return w.enabled
}

// GetMetrics returns buffer metrics if available
func (w *ResponseWrapper) GetMetrics() map[string]interface{} {
	if !w.enabled {
		return map[string]interface{}{"enabled": false}
	}
	metrics := w.bufferClient.GetMetrics()
	metrics["enabled"] = true
	return metrics
}

// WrapToolResult creates a CallToolResult with automatic buffering if needed
func (w *ResponseWrapper) WrapToolResult(
	ctx context.Context,
	toolName string,
	response interface{},
	summary string,
) (*mcpgo.CallToolResult, error) {
	// Serialize to measure size
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}

	size := int64(len(responseBytes))

	// Check if buffering is needed
	if w.enabled && w.bufferClient.ShouldBuffer(size) {
		result, err := w.createBufferedResult(ctx, toolName, response, responseBytes, summary)
		if err != nil {
			// Log error but fallback to direct response
			w.logger.Warn("Buffer storage failed, falling back to direct response",
				zap.String("tool", toolName),
				zap.Int64("size", size),
				zap.Error(err),
			)
			return w.createDirectResult(responseBytes), nil
		}
		w.logger.Info("Tool response buffered",
			zap.String("tool", toolName),
			zap.Int64("size", size),
			zap.String("summary", truncateString(summary, 100)),
		)
		return result, nil
	}

	// Direct response
	return w.createDirectResult(responseBytes), nil
}

// WrapResourceContents creates ResourceContents with automatic buffering
func (w *ResponseWrapper) WrapResourceContents(
	ctx context.Context,
	uri string,
	resourceType string,
	payload interface{},
	summary string,
) ([]mcpgo.ResourceContents, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	size := int64(len(payloadBytes))

	// Check if buffering is needed
	if w.enabled && w.bufferClient.ShouldBuffer(size) {
		result, err := w.createBufferedResource(ctx, uri, resourceType, payload, payloadBytes, summary)
		if err != nil {
			// Log error but fallback to direct response
			w.logger.Warn("Buffer storage failed for resource, falling back to direct response",
				zap.String("uri", uri),
				zap.Int64("size", size),
				zap.Error(err),
			)
			return w.createDirectResource(uri, payloadBytes), nil
		}
		w.logger.Info("Resource content buffered",
			zap.String("uri", uri),
			zap.String("type", resourceType),
			zap.Int64("size", size),
		)
		return result, nil
	}

	// Direct response
	return w.createDirectResource(uri, payloadBytes), nil
}

// WrapDocumentResource creates a smart document response with pagination info
// For large documents, returns summary + buffer reference instead of all chunks
func (w *ResponseWrapper) WrapDocumentResource(
	ctx context.Context,
	uri string,
	document interface{},
	chunks []interface{},
	summary string,
) ([]mcpgo.ResourceContents, error) {
	// Build the full payload
	fullPayload := map[string]interface{}{
		"document": document,
		"chunks":   chunks,
	}

	payloadBytes, err := json.Marshal(fullPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal document: %w", err)
	}

	size := int64(len(payloadBytes))

	// If small enough, return directly
	if !w.enabled || !w.bufferClient.ShouldBuffer(size) {
		return w.createDirectResource(uri, payloadBytes), nil
	}

	// For large documents: store full content in buffer, return summary + preview
	ref, err := w.bufferClient.Store(ctx, buffer.StoreRequest{
		Content:  fullPayload,
		ToolName: "resource:document",
		Summary:  summary,
		Metadata: map[string]interface{}{
			"original_uri":  uri,
			"resource_type": "document",
			"chunk_count":   len(chunks),
		},
	})
	if err != nil {
		w.logger.Warn("Failed to buffer document, returning truncated response",
			zap.String("uri", uri),
			zap.Error(err),
		)
		// Fallback: return document metadata + first few chunks
		return w.createTruncatedDocumentResponse(uri, document, chunks), nil
	}

	// Build smart response with buffer reference
	smartResponse := map[string]interface{}{
		"document": document,
		"chunks_summary": map[string]interface{}{
			"total":         len(chunks),
			"preview_count": min(5, len(chunks)),
		},
		"chunks_preview": getFirstN(chunks, 5), // First 5 chunks as preview
		"buffer_ref":     ref,
		"full_content":   "Available via buffer reference",
	}

	responseBytes, _ := json.Marshal(smartResponse)

	w.logger.Info("Document resource buffered with smart response",
		zap.String("uri", uri),
		zap.Int("total_chunks", len(chunks)),
		zap.Int64("original_size", size),
		zap.Int("response_size", len(responseBytes)),
	)

	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(responseBytes),
		},
	}, nil
}

func (w *ResponseWrapper) createDirectResult(responseBytes []byte) *mcpgo.CallToolResult {
	var structured any
	if err := json.Unmarshal(responseBytes, &structured); err != nil {
		structured = json.RawMessage(responseBytes)
	}
	if structured == nil {
		structured = json.RawMessage("null")
	}
	return &mcpgo.CallToolResult{
		Content: []mcpgo.Content{
			mcpgo.TextContent{
				Type: "text",
				Text: string(responseBytes),
			},
		},
		StructuredContent: structured,
	}
}

func (w *ResponseWrapper) createDirectResource(uri string, payloadBytes []byte) []mcpgo.ResourceContents {
	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(payloadBytes),
		},
	}
}

func (w *ResponseWrapper) createBufferedResult(
	ctx context.Context,
	toolName string,
	response interface{},
	responseBytes []byte,
	summary string,
) (*mcpgo.CallToolResult, error) {
	ref, err := w.bufferClient.Store(ctx, buffer.StoreRequest{
		Content:  response,
		ToolName: toolName,
		Summary:  summary,
	})
	if err != nil {
		return nil, err
	}

	// Build response with buffer reference
	preview := buildPreview(responseBytes, 400)
	fallbackText := fmt.Sprintf("%s result buffered: %s", toolName, ref.Key)
	if summary != "" {
		fallbackText += ". " + summary
	}

	bufferedResponse := map[string]interface{}{
		"buffer_ref":   ref,
		"summary":      summary,
		"text_preview": preview,
		"tool":         toolName,
	}

	bufferedBytes, _ := json.Marshal(bufferedResponse)

	return &mcpgo.CallToolResult{
		Content: []mcpgo.Content{
			mcpgo.TextContent{
				Type: "text",
				Text: string(bufferedBytes),
			},
		},
		StructuredContent: bufferedResponse,
	}, nil
}

func (w *ResponseWrapper) createBufferedResource(
	ctx context.Context,
	uri string,
	resourceType string,
	payload interface{},
	payloadBytes []byte,
	summary string,
) ([]mcpgo.ResourceContents, error) {
	ref, err := w.bufferClient.Store(ctx, buffer.StoreRequest{
		Content:  payload,
		ToolName: "resource:" + resourceType,
		Summary:  summary,
		Metadata: map[string]interface{}{
			"original_uri":  uri,
			"resource_type": resourceType,
		},
	})
	if err != nil {
		return nil, err
	}

	// Build response with buffer reference
	bufferedResponse := map[string]interface{}{
		"buffer_ref":    ref,
		"original_uri":  uri,
		"resource_type": resourceType,
		"summary":       summary,
		"text_preview":  buildPreview(payloadBytes, 400),
	}

	bufferedBytes, _ := json.Marshal(bufferedResponse)

	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(bufferedBytes),
		},
	}, nil
}

func (w *ResponseWrapper) createTruncatedDocumentResponse(
	uri string,
	document interface{},
	chunks []interface{},
) []mcpgo.ResourceContents {
	// Return document + first 10 chunks with truncation notice
	previewChunks := getFirstN(chunks, 10)

	truncatedPayload := map[string]interface{}{
		"document":     document,
		"chunks":       previewChunks,
		"truncated":    true,
		"total_chunks": len(chunks),
		"message":      fmt.Sprintf("Response truncated. Showing %d of %d chunks.", len(previewChunks), len(chunks)),
	}

	payloadBytes, _ := json.Marshal(truncatedPayload)

	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(payloadBytes),
		},
	}
}

// Helper functions

func buildPreview(data []byte, limit int) string {
	s := strings.TrimSpace(string(data))
	return truncateString(s, limit)
}

func truncateString(s string, limit int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}

func getFirstN[T any](slice []T, n int) []T {
	if len(slice) <= n {
		return slice
	}
	return slice[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
