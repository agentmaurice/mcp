package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	bufferclient "github.com/agentmaurice/mcpchatui/mcp/brain/internal/platform/buffer"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type failingBufferReader struct {
	err error
}

func (f failingBufferReader) Configured() bool { return true }
func (f failingBufferReader) LoadText(context.Context, string, int64) ([]byte, string, error) {
	return nil, "", f.err
}

func TestDocumentUpsertRejectsURLBufferReferenceBeforeLoading(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewDocumentUpsertTool(cfg, nil, failingBufferReader{}, zap.NewNop())
	result, err := tool.Handler()(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{
			"source_uri": "storage://brain-sources", "document_uri": "storage://brain-sources/a.json",
			"buffer_ref": "buffer://storage:file:secret-token",
		},
	}})
	if err != nil || !result.IsError {
		t.Fatalf("expected validation error: result=%#v err=%v", result, err)
	}
	if strings.Contains(result.Content[0].(mcplib.TextContent).Text, "secret-token") {
		t.Fatal("opaque reference leaked in validation error")
	}
}

func TestDocumentUpsertMapsExpiredBufferWithoutLeakingReference(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{DefaultTenantID: "tenant"}}
	tool := NewDocumentUpsertTool(cfg, nil, failingBufferReader{err: bufferclient.ErrNotFound}, zap.NewNop())
	result, err := tool.Handler()(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name: tool.Name(), Arguments: map[string]any{
			"source_uri": "storage://brain-sources", "document_uri": "storage://brain-sources/a.json",
			"buffer_ref": "storage:file:secret-token",
		},
	}})
	if err != nil || !result.IsError {
		t.Fatalf("expected not-found error: result=%#v err=%v", result, err)
	}
	text := result.Content[0].(mcplib.TextContent).Text
	if !strings.Contains(text, "buffer_not_found") || strings.Contains(text, "secret-token") {
		t.Fatalf("unexpected safe error: %s", text)
	}
}
