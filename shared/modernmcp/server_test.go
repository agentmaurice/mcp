package modernmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	legacymcp "github.com/mark3labs/mcp-go/mcp"
)

const modernProtocolVersion = "2026-07-28"

func TestModernHandlerDeclaresStaticToolsAndPreservesResults(t *testing.T) {
	server := New("test-server", "1.0.0", "test instructions")
	server.AddTool(legacymcp.Tool{
		Name:        "test.echo",
		Description: "echo",
		InputSchema: legacymcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}, func(context.Context, legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
		return &legacymcp.CallToolResult{
			StructuredContent: map[string]any{"status": "ok"},
		}, nil
	})

	discovery := post(t, server.Handler(), map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server/discover",
		"params": map[string]any{
			"_meta": modernMeta(),
		},
	})
	result := object(t, discovery["result"])
	capabilities := object(t, result["capabilities"])
	for _, name := range []string{"tools", "prompts", "resources"} {
		capability := object(t, capabilities[name])
		if _, found := capability["listChanged"]; found {
			t.Fatalf("%s.listChanged must be absent: %#v", name, capability)
		}
	}
	if _, found := capabilities["logging"]; found {
		t.Fatalf("logging capability must be absent: %#v", capabilities)
	}

	listed := post(t, server.Handler(), map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{"_meta": modernMeta()},
	})
	listedTools := array(t, object(t, listed["result"])["tools"])
	if len(listedTools) != 1 {
		t.Fatalf("tools length = %d, want 1", len(listedTools))
	}
	definition := object(t, listedTools[0])
	inputSchema := object(t, definition["inputSchema"])
	if got := inputSchema["$schema"]; got != jsonSchema202012 {
		t.Fatalf("inputSchema.$schema = %#v", got)
	}

	called := post(t, server.Handler(), map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": "test.echo", "arguments": map[string]any{}, "_meta": modernMeta()},
	})
	structured := object(t, object(t, called["result"])["structuredContent"])
	if got := structured["status"]; got != "ok" {
		t.Fatalf("structuredContent.status = %#v", got)
	}
}

func modernMeta() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    modernProtocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1.0.0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", " HTTP ")
	t.Setenv("MCP_ADDRESS", "127.0.0.1:19000")
	t.Setenv("MCP_BASE_PATH", "mcp/test/")
	cfg := ConfigFromEnv("/ignored")
	if cfg.Transport != "http" || cfg.Address != "127.0.0.1:19000" || cfg.BasePath != "/mcp/test" {
		t.Fatalf("config = %#v", cfg)
	}
}

func post(t *testing.T, handler http.Handler, payload map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", modernProtocolVersion)
	method, _ := payload["method"].(string)
	request.Header.Set("Mcp-Method", method)
	if params, ok := payload["params"].(map[string]any); ok {
		if name, ok := params["name"].(string); ok {
			request.Header.Set("Mcp-Name", name)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, recorder.Body.String())
	}
	return response
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want object", value)
	}
	return result
}

func array(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want array", value)
	}
	return result
}
