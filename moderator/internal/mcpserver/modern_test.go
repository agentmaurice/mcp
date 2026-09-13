package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/moderator/internal/mistral"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const modernProtocolVersion = "2026-07-28"

func TestModernHTTPProtocol(t *testing.T) {
	modernServer := newModernMCPServer(serviceName, serviceVersion, "")
	for _, name := range []string{"zeta", "alpha"} {
		definition := legacymcp.Tool{
			Name:        name,
			Description: "test tool",
			InputSchema: legacymcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{"value": map[string]any{"type": "string"}},
			},
		}
		modernServer.AddTool(toOfficialTool(definition), adaptToolHandler(
			func(_ context.Context, request legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
				if request.Params.Meta == nil {
					t.Error("forwarded protocol metadata is nil")
				} else if got := request.Params.Meta.AdditionalFields["io.modelcontextprotocol/protocolVersion"]; got != modernProtocolVersion {
					t.Errorf("forwarded protocol metadata = %#v, want %q", got, modernProtocolVersion)
				}
				if got := request.Header.Get("Mcp-Protocol-Version"); got != modernProtocolVersion {
					t.Errorf("forwarded protocol header = %q, want %q", got, modernProtocolVersion)
				}
				if got := request.Header.Get("X-User-Id"); got != "user-test" {
					t.Errorf("forwarded user header = %q, want user-test", got)
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
	handler := officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

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
		if !contains(anySlice(t, result["supportedVersions"]), modernProtocolVersion) {
			t.Fatalf("supportedVersions = %#v, want %q", result["supportedVersions"], modernProtocolVersion)
		}
		serverInfo := anyMap(t, anyMap(t, result["_meta"])["io.modelcontextprotocol/serverInfo"])
		if got := serverInfo["name"]; got != serviceName {
			t.Fatalf("server info name = %#v, want %q", got, serviceName)
		}
		assertStaticCapabilities(t, result)
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
		var names []string
		for _, item := range anySlice(t, result["tools"]) {
			tool := anyMap(t, item)
			names = append(names, tool["name"].(string))
			if got := anyMap(t, tool["inputSchema"])["$schema"]; got != jsonSchema202012 {
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
		result, err := adaptToolHandler(server.ToolHandlerFunc(legacyHandler))(
			context.Background(),
			&officialmcp.CallToolRequest{Params: &officialmcp.CallToolParamsRaw{Name: "test", Arguments: json.RawMessage(`{}`)}},
		)
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

func TestModeratorToolRegistersOnModernServer(t *testing.T) {
	builder := NewBuilder(config.Config{Server: config.ServerConfig{
		BasePath:          "/mcp/moderator",
		KeepAlive:         true,
		KeepAliveInterval: time.Second,
	}}, &mistral.Client{}, zap.NewNop())
	if _, err := builder.Build(); err != nil {
		t.Fatal(err)
	}

	status, _, response := postMCP(t, builder.Streamable(), modernProtocolVersion, "tools/list", "", map[string]any{
		"_meta": modernMeta(modernProtocolVersion),
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
	result := responseResult(t, response)
	assertCompleteCacheable(t, result)
	tools := anySlice(t, result["tools"])
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool := anyMap(t, tools[0])
	if got := tool["name"]; got != "mistral_moderate_text" {
		t.Fatalf("tool name = %#v, want mistral_moderate_text", got)
	}
	if got := anyMap(t, tool["inputSchema"])["$schema"]; got != jsonSchema202012 {
		t.Fatalf("inputSchema.$schema = %#v, want %q", got, jsonSchema202012)
	}
	annotations := anyMap(t, tool["annotations"])
	if got := annotations["readOnlyHint"]; got != true {
		t.Fatalf("readOnlyHint = %#v, want true", got)
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
	req.Header.Set("X-User-Id", "user-test")
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

func assertStaticCapabilities(t *testing.T, result map[string]any) {
	t.Helper()
	capabilities := anyMap(t, result["capabilities"])
	for _, name := range []string{"tools", "prompts", "resources"} {
		capability := anyMap(t, capabilities[name])
		if got := capability["listChanged"]; got != nil {
			t.Fatalf("capabilities.%s.listChanged = %#v, want absent", name, got)
		}
	}
	if got := capabilities["logging"]; got != nil {
		t.Fatalf("capabilities.logging = %#v, want absent", got)
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
