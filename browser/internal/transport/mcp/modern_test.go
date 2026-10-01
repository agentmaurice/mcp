package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

const modernProtocolVersion = "2026-07-28"
const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

func TestModernHTTPProtocol(t *testing.T) {
	modernServer := newModernMCPServer("browser-mcp", "1.0.0")
	for _, name := range []string{"zeta", "alpha"} {
		def := legacymcp.Tool{
			Name:        name,
			Description: "test tool",
			InputSchema: legacymcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"value": map[string]any{"type": "string"},
				},
			},
		}
		modernServer.AddTool(toOfficialTool(def), adaptToolHandler(
			func(_ context.Context, request legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
				if request.Params.Meta == nil {
					t.Error("forwarded protocol metadata is nil")
				} else {
					if got := request.Params.Meta.AdditionalFields["io.modelcontextprotocol/protocolVersion"]; got != modernProtocolVersion {
						t.Errorf("forwarded protocol metadata = %#v, want %q", got, modernProtocolVersion)
					}
				}
				if request.Header == nil {
					t.Error("forwarded HTTP header is nil")
				} else {
					if got := request.Header.Get("Mcp-Protocol-Version"); got != modernProtocolVersion {
						t.Errorf("forwarded protocol header = %q, want %q", got, modernProtocolVersion)
					}
				}
				return &legacymcp.CallToolResult{
					Content: []legacymcp.Content{legacymcp.NewTextContent("ok")},
					StructuredContent: map[string]any{
						"tool":  request.Params.Name,
						"value": request.GetArguments()["value"],
					},
				}, nil
			},
		))
	}
	handler := modernServer.Handler()

	t.Run("discover", func(t *testing.T) {
		status, header, response := postMCP(t, handler, modernProtocolVersion, "server/discover", "", map[string]any{
			"_meta": modernMeta(modernProtocolVersion),
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := responseResult(t, response)
		assertCompleteCacheable(t, result)
		versions := anySlice(t, result["supportedVersions"])
		if !contains(versions, modernProtocolVersion) {
			t.Fatalf("supportedVersions = %#v, want %q", versions, modernProtocolVersion)
		}
		meta := anyMap(t, result["_meta"])
		serverInfo := anyMap(t, meta["io.modelcontextprotocol/serverInfo"])
		if got := serverInfo["name"]; got != "browser-mcp" {
			t.Fatalf("server info name = %#v, want browser-mcp", got)
		}
		capabilities := anyMap(t, result["capabilities"])
		for _, name := range []string{"tools", "prompts", "resources"} {
			capability := anyMap(t, capabilities[name])
			if got := capability["listChanged"]; got != nil {
				t.Fatalf("capabilities.%s.listChanged = %#v, want absent", name, got)
			}
		}
		if got := capabilities["logging"]; got != nil {
			t.Fatalf("capabilities.logging = %#v, want absent on modern stateless transport", got)
		}
	})

	t.Run("tools are deterministic and use JSON Schema 2020-12", func(t *testing.T) {
		status, _, response := postMCP(t, handler, modernProtocolVersion, "tools/list", "", map[string]any{
			"_meta": modernMeta(modernProtocolVersion),
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		result := responseResult(t, response)
		assertCompleteCacheable(t, result)
		tools := anySlice(t, result["tools"])
		if len(tools) != 2 {
			t.Fatalf("len(tools) = %d, want 2", len(tools))
		}
		var names []string
		for _, item := range tools {
			tool := anyMap(t, item)
			names = append(names, tool["name"].(string))
			schema := anyMap(t, tool["inputSchema"])
			if got := schema["$schema"]; got != jsonSchema202012 {
				t.Fatalf("inputSchema.$schema = %#v, want %q", got, jsonSchema202012)
			}
		}
		if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("tool names = %#v, want %#v", names, want)
		}
	})

	t.Run("tool result is complete and structured", func(t *testing.T) {
		status, header, response := postMCP(t, handler, modernProtocolVersion, "tools/call", "alpha", map[string]any{
			"_meta":     modernMeta(modernProtocolVersion),
			"name":      "alpha",
			"arguments": map[string]any{"value": "kept"},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := responseResult(t, response)
		if got := result["resultType"]; got != "complete" {
			t.Fatalf("resultType = %#v, want complete", got)
		}
		if got, want := anyMap(t, result["structuredContent"]), map[string]any{"tool": "alpha", "value": "kept"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("structuredContent = %#v, want %#v", got, want)
		}
	})

	t.Run("removed method is rejected", func(t *testing.T) {
		status, _, response := postMCP(t, handler, modernProtocolVersion, "initialize", "", map[string]any{
			"_meta":           modernMeta(modernProtocolVersion),
			"protocolVersion": modernProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		})
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := responseErrorCode(t, response); got != -32601 {
			t.Fatalf("error code = %d, want -32601", got)
		}
	})

	t.Run("unsupported version is negotiated explicitly", func(t *testing.T) {
		const unsupported = "2099-01-01"
		status, _, response := postMCP(t, handler, unsupported, "tools/list", "", map[string]any{
			"_meta": modernMeta(unsupported),
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := responseErrorCode(t, response); got != -32022 {
			t.Fatalf("error code = %d, want -32022", got)
		}
	})

	t.Run("header mismatch is rejected", func(t *testing.T) {
		status, _, response := postMCP(t, handler, modernProtocolVersion, "tools/call", "zeta", map[string]any{
			"_meta":     modernMeta(modernProtocolVersion),
			"name":      "alpha",
			"arguments": map[string]any{},
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := responseErrorCode(t, response); got != -32020 {
			t.Fatalf("error code = %d, want -32020", got)
		}
	})

	t.Run("legacy initialize remains accepted", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "legacy", "version": "1"},
			},
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
	})
}

func TestAdaptToolHandlerSupportsAnyJSONStructuredContent(t *testing.T) {
	values := []any{
		map[string]any{"ok": true},
		[]any{"a", float64(2)},
		"value",
		float64(42),
		true,
		json.RawMessage("null"),
	}
	for _, value := range values {
		legacyHandler := func(context.Context, legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
			return &legacymcp.CallToolResult{
				Content:           []legacymcp.Content{legacymcp.NewTextContent("ok")},
				StructuredContent: value,
			}, nil
		}
		result, err := adaptToolHandler(server.ToolHandlerFunc(legacyHandler))(context.Background(), legacymcp.CallToolRequest{Params: legacymcp.CallToolParams{Name: "test", Arguments: map[string]any{}}})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		got, present := wire["structuredContent"]
		if !present {
			t.Fatalf("structuredContent absent for %T", value)
		}
		var want any
		encoded, _ := json.Marshal(value)
		if err := json.Unmarshal(encoded, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("structuredContent = %#v, want %#v", got, want)
		}
	}
}

func TestAllBrowserToolsRegisterOnModernServer(t *testing.T) {
	modernServer := newModernMCPServer("browser-mcp", "1.0.0")
	s := &Server{
		mcpServer:    server.NewMCPServer("browser-mcp", "1.0.0", server.WithToolCapabilities(true)),
		modernServer: modernServer,
		logger:       zap.NewNop(),
	}
	s.registerTools()

	handler := modernServer.Handler()
	status, _, response := postMCP(t, handler, modernProtocolVersion, "tools/list", "", map[string]any{
		"_meta": modernMeta(modernProtocolVersion),
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
	tools := anySlice(t, responseResult(t, response)["tools"])
	if len(tools) != 32 {
		t.Fatalf("len(tools) = %d, want 32", len(tools))
	}
	for _, item := range tools {
		tool := anyMap(t, item)
		schema := anyMap(t, tool["inputSchema"])
		if got := schema["$schema"]; got != jsonSchema202012 {
			t.Fatalf("tool %q inputSchema.$schema = %#v, want %q", tool["name"], got, jsonSchema202012)
		}
	}
}

func TestCreateTextResultPreservesStructuredData(t *testing.T) {
	want := map[string]any{"status": "ok"}
	result, err := createTextResult(want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.StructuredContent, want) {
		t.Fatalf("StructuredContent = %#v, want %#v", result.StructuredContent, want)
	}
}

func postMCP(t *testing.T, handler http.Handler, version, method, name string, params map[string]any) (int, http.Header, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", version)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, recorder.Body.String())
	}
	return recorder.Code, recorder.Header(), response
}

func modernMeta(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func responseResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if errValue := response["error"]; errValue != nil {
		t.Fatalf("unexpected JSON-RPC error: %#v", errValue)
	}
	return anyMap(t, response["result"])
}

func responseErrorCode(t *testing.T, response map[string]any) int {
	t.Helper()
	errValue := anyMap(t, response["error"])
	code, ok := errValue["code"].(float64)
	if !ok {
		t.Fatalf("error code = %#v", errValue["code"])
	}
	return int(code)
}

func assertCompleteCacheable(t *testing.T, result map[string]any) {
	t.Helper()
	if got := result["resultType"]; got != "complete" {
		t.Fatalf("resultType = %#v, want complete", got)
	}
	if got := result["ttlMs"]; got != float64(0) {
		t.Fatalf("ttlMs = %#v, want 0", got)
	}
	if got := result["cacheScope"]; got != "public" {
		t.Fatalf("cacheScope = %#v, want public", got)
	}
}

func anyMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map[string]any", value)
	}
	return result
}

func anySlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want []any", value)
	}
	return result
}

func contains(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
