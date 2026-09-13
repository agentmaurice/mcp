package main

import (
	"context"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/ocr/internal/bridge"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestExtractHandlerRequiresExactlyOneSource(t *testing.T) {
	handler := extractHandler((*bridge.Client)(nil))
	for _, arguments := range []map[string]interface{}{
		{},
		{"source_ref": "storage://scan.pdf", "source_url": "https://example.com/scan.pdf"},
	} {
		result, err := handler(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: arguments}})
		if err != nil {
			t.Fatalf("handler returned transport error: %v", err)
		}
		if !result.IsError {
			t.Fatalf("expected arguments to be rejected: %#v", arguments)
		}
	}
}

func TestCapabilitiesDescribeHostedSynchronousV1(t *testing.T) {
	result, err := capabilitiesHandler()(context.Background(), mcplib.CallToolRequest{})
	if err != nil {
		t.Fatalf("capabilities handler returned error: %v", err)
	}
	content, ok := result.StructuredContent.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected capabilities payload: %#v", result.StructuredContent)
	}
	if content["mode"] != "hosted" || content["synchronous"] != true || content["page_indexes"] != "zero_based" {
		t.Fatalf("unexpected capabilities: %#v", content)
	}
}
