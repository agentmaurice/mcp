package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/memory/internal/shared"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

const modernProtocolVersion = "2026-07-28"

func TestMemoryModernHTTPProtocol(t *testing.T) {
	cfg := &config.Config{
		Server:  config.ServerConfig{BasePath: "/mcp"},
		Storage: config.StorageConfig{DefaultTenantID: "default-tenant"},
	}
	memoryServer := NewServer(nil, cfg, NewResponseWrapper(nil, zap.NewNop()), zap.NewNop())
	if err := memoryServer.Build(); err != nil {
		t.Fatal(err)
	}

	memoryServer.modernServer.AddTool(&officialmcp.Tool{
		Name:        "test.identity",
		InputSchema: map[string]any{"type": "object", "$schema": jsonSchema202012},
	}, func(ctx context.Context, _ *officialmcp.CallToolRequest) (*officialmcp.CallToolResult, error) {
		identity := shared.IdentityFromContext(ctx)
		return &officialmcp.CallToolResult{
			Content:           []officialmcp.Content{&officialmcp.TextContent{Text: identity.TenantID}},
			StructuredContent: map[string]any{"tenant": identity.TenantID},
		}, nil
	})
	handler := memoryServer.Streamable()

	t.Run("discover", func(t *testing.T) {
		status, header, response := postMemoryMCP(t, handler, modernProtocolVersion, "server/discover", "", map[string]any{
			"_meta": modernMemoryMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := memoryResponseResult(t, response)
		assertMemoryCacheable(t, result)
		if !containsMemory(anyMemorySlice(t, result["supportedVersions"]), modernProtocolVersion) {
			t.Fatalf("supportedVersions = %#v", result["supportedVersions"])
		}
		assertMemoryStaticCapabilities(t, result)
	})

	t.Run("tools are complete deterministic schemas", func(t *testing.T) {
		status, _, response := postMemoryMCP(t, handler, modernProtocolVersion, "tools/list", "", map[string]any{
			"_meta": modernMemoryMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		result := memoryResponseResult(t, response)
		assertMemoryCacheable(t, result)
		tools := anyMemorySlice(t, result["tools"])
		if len(tools) != 15 {
			t.Fatalf("len(tools) = %d, want 15 including test identity tool", len(tools))
		}
		previous := ""
		for _, item := range tools {
			tool := anyMemoryMap(t, item)
			name := tool["name"].(string)
			if name < previous {
				t.Fatalf("tools are not sorted: %q before %q", previous, name)
			}
			previous = name
			schema := anyMemoryMap(t, tool["inputSchema"])
			if got := schema["$schema"]; got != jsonSchema202012 {
				t.Fatalf("tool %q inputSchema.$schema = %#v", name, got)
			}
		}
	})

	t.Run("tool result and request identity", func(t *testing.T) {
		status, header, response := postMemoryMCP(t, handler, modernProtocolVersion, "tools/call", "test.identity", map[string]any{
			"_meta":     modernMemoryMeta(modernProtocolVersion),
			"name":      "test.identity",
			"arguments": map[string]any{},
		}, http.Header{"X-Tenant-Id": []string{"tenant-a"}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := memoryResponseResult(t, response)
		if got := result["resultType"]; got != "complete" {
			t.Fatalf("resultType = %#v, want complete", got)
		}
		if got := anyMemoryMap(t, result["structuredContent"])["tenant"]; got != "tenant-a" {
			t.Fatalf("tenant = %#v, want tenant-a", got)
		}
	})

	t.Run("resource templates are deterministic", func(t *testing.T) {
		status, _, response := postMemoryMCP(t, handler, modernProtocolVersion, "resources/templates/list", "", map[string]any{
			"_meta": modernMemoryMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		result := memoryResponseResult(t, response)
		assertMemoryCacheable(t, result)
		templates := anyMemorySlice(t, result["resourceTemplates"])
		if len(templates) != 5 {
			t.Fatalf("len(resourceTemplates) = %d, want 5", len(templates))
		}
		previous := ""
		for _, item := range templates {
			uri := anyMemoryMap(t, item)["uriTemplate"].(string)
			if uri < previous {
				t.Fatalf("resource templates are not sorted: %q before %q", previous, uri)
			}
			previous = uri
		}
	})

	t.Run("unknown resource returns invalid params", func(t *testing.T) {
		uri := "memory://unknown/value"
		status, _, response := postMemoryMCP(t, handler, modernProtocolVersion, "resources/read", uri, map[string]any{
			"_meta": modernMemoryMeta(modernProtocolVersion),
			"uri":   uri,
		}, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := memoryResponseErrorCode(t, response); got != -32602 {
			t.Fatalf("error code = %d, want -32602", got)
		}
	})

	t.Run("modern wire errors", func(t *testing.T) {
		status, _, response := postMemoryMCP(t, handler, modernProtocolVersion, "tools/call", "wrong-name", map[string]any{
			"_meta":     modernMemoryMeta(modernProtocolVersion),
			"name":      "test.identity",
			"arguments": map[string]any{},
		}, nil)
		if status != http.StatusBadRequest || memoryResponseErrorCode(t, response) != -32020 {
			t.Fatalf("header mismatch: status=%d response=%#v", status, response)
		}

		const unsupported = "2099-01-01"
		status, _, response = postMemoryMCP(t, handler, unsupported, "tools/list", "", map[string]any{
			"_meta": modernMemoryMeta(unsupported),
		}, nil)
		if status != http.StatusBadRequest || memoryResponseErrorCode(t, response) != -32022 {
			t.Fatalf("unsupported version: status=%d response=%#v", status, response)
		}

		status, _, response = postMemoryMCP(t, handler, modernProtocolVersion, "ping", "", map[string]any{
			"_meta": modernMemoryMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusNotFound || memoryResponseErrorCode(t, response) != -32601 {
			t.Fatalf("removed method: status=%d response=%#v", status, response)
		}
	})

	t.Run("legacy initialize remains accepted", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "legacy", "version": "1"},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
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

func assertMemoryStaticCapabilities(t *testing.T, result map[string]any) {
	t.Helper()
	capabilities := anyMemoryMap(t, result["capabilities"])
	for _, name := range []string{"tools", "prompts", "resources"} {
		capability := anyMemoryMap(t, capabilities[name])
		if got := capability["listChanged"]; got != nil {
			t.Fatalf("capabilities.%s.listChanged = %#v, want absent", name, got)
		}
	}
	if got := capabilities["logging"]; got != nil {
		t.Fatalf("capabilities.logging = %#v, want absent", got)
	}
}

func TestMemoryAdaptersPreserveJSONAndResources(t *testing.T) {
	t.Run("structured content", func(t *testing.T) {
		values := []any{map[string]any{"ok": true}, []any{"a", float64(2)}, "value", float64(42), true, json.RawMessage("null")}
		for _, value := range values {
			handler := server.ToolHandlerFunc(func(context.Context, legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
				return &legacymcp.CallToolResult{Content: []legacymcp.Content{legacymcp.NewTextContent("ok")}, StructuredContent: value}, nil
			})
			result, err := adaptToolHandler(handler)(context.Background(), &officialmcp.CallToolRequest{
				Params: &officialmcp.CallToolParamsRaw{Name: "test", Arguments: json.RawMessage(`{}`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			wire := marshalMemoryMap(t, result)
			got, present := wire["structuredContent"]
			if !present {
				t.Fatalf("structuredContent absent for %T", value)
			}
			var want any
			encoded, _ := json.Marshal(value)
			_ = json.Unmarshal(encoded, &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("structuredContent = %#v, want %#v", got, want)
			}
		}
	})

	t.Run("resource contents", func(t *testing.T) {
		handler := server.ResourceHandlerFunc(func(_ context.Context, request legacymcp.ReadResourceRequest) ([]legacymcp.ResourceContents, error) {
			return []legacymcp.ResourceContents{legacymcp.TextResourceContents{URI: request.Params.URI, MIMEType: "application/json", Text: `{"ok":true}`}}, nil
		})
		result, err := adaptResourceHandler(handler)(context.Background(), &officialmcp.ReadResourceRequest{
			Params: &officialmcp.ReadResourceParams{URI: "memory://documents"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Contents) != 1 || result.Contents[0].URI != "memory://documents" || result.Contents[0].Text != `{"ok":true}` {
			t.Fatalf("unexpected resource result: %#v", result.Contents)
		}
	})

	t.Run("resource not found error", func(t *testing.T) {
		handler := server.ResourceHandlerFunc(func(context.Context, legacymcp.ReadResourceRequest) ([]legacymcp.ResourceContents, error) {
			return nil, errEntityResourceNotFound
		})
		_, err := adaptResourceHandler(handler)(context.Background(), &officialmcp.ReadResourceRequest{
			Params: &officialmcp.ReadResourceParams{URI: "memory://entity/missing"},
		})
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != officialmcp.CodeResourceNotFound {
			t.Fatalf("error = %#v, want resource not found code %d", err, officialmcp.CodeResourceNotFound)
		}
	})

	t.Run("response wrapper", func(t *testing.T) {
		wrapper := NewResponseWrapper(nil, zap.NewNop())
		for _, value := range []any{map[string]any{"ok": true}, []any{"a"}, "value", float64(1), true, nil} {
			data, _ := json.Marshal(value)
			result := wrapper.createDirectResult(data)
			wire := marshalMemoryMap(t, result)
			if _, present := wire["structuredContent"]; !present {
				t.Fatalf("structuredContent absent for %T", value)
			}
		}
	})
}

func postMemoryMCP(t *testing.T, handler http.Handler, version, method, name string, params map[string]any, extra http.Header) (int, http.Header, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
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
	for key, values := range extra {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, recorder.Body.String())
	}
	return recorder.Code, recorder.Header(), response
}

func modernMemoryMeta(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func memoryResponseResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if response["error"] != nil {
		t.Fatalf("unexpected JSON-RPC error: %#v", response["error"])
	}
	return anyMemoryMap(t, response["result"])
}

func memoryResponseErrorCode(t *testing.T, response map[string]any) int {
	t.Helper()
	return int(anyMemoryMap(t, response["error"])["code"].(float64))
}

func assertMemoryCacheable(t *testing.T, result map[string]any) {
	t.Helper()
	if result["resultType"] != "complete" || result["ttlMs"] != float64(0) || result["cacheScope"] != "public" {
		t.Fatalf("incomplete cache metadata: %#v", result)
	}
}

func marshalMemoryMap(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func anyMemoryMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map[string]any", value)
	}
	return result
}

func anyMemorySlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want []any", value)
	}
	return result
}

func containsMemory(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
