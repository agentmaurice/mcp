package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/brain/internal/shared"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

const modernProtocolVersion = "2026-07-28"

func TestBrainModernHTTPProtocol(t *testing.T) {
	cfg := &config.Config{
		Server:  config.ServerConfig{BasePath: "/mcp/brain"},
		Storage: config.StorageConfig{DefaultTenantID: "default-tenant"},
	}
	brainServer := NewServer(nil, cfg, NewResponseWrapper(zap.NewNop()), nil, nil, nil, zap.NewNop())
	if err := brainServer.Build(); err != nil {
		t.Fatal(err)
	}

	definition := legacymcp.Tool{
		Name:        "test.identity",
		Description: "test request metadata and identity",
		InputSchema: legacymcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}
	brainServer.modernServer.AddTool(toOfficialTool(definition), adaptToolHandler(
		func(ctx context.Context, request legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
			if request.Params.Meta == nil {
				t.Error("forwarded protocol metadata is nil")
			} else if got := request.Params.Meta.AdditionalFields["io.modelcontextprotocol/protocolVersion"]; got != modernProtocolVersion {
				t.Errorf("forwarded protocol metadata = %#v, want %q", got, modernProtocolVersion)
			}
			if got := request.Header.Get("Mcp-Protocol-Version"); got != modernProtocolVersion {
				t.Errorf("forwarded protocol header = %q, want %q", got, modernProtocolVersion)
			}
			identity := shared.IdentityFromContext(ctx)
			return structuredResult(map[string]any{
				"tenant": identity.TenantID,
				"user":   identity.UserID,
				"scopes": identity.Scopes,
			}), nil
		},
	))
	handler := brainServer.Streamable()

	t.Run("discover", func(t *testing.T) {
		status, header, response := postBrainMCP(t, handler, modernProtocolVersion, "server/discover", "", map[string]any{
			"_meta": modernBrainMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := brainResponseResult(t, response)
		assertBrainCacheable(t, result)
		if !containsBrain(anyBrainSlice(t, result["supportedVersions"]), modernProtocolVersion) {
			t.Fatalf("supportedVersions = %#v", result["supportedVersions"])
		}
		serverInfo := anyBrainMap(t, anyBrainMap(t, result["_meta"])["io.modelcontextprotocol/serverInfo"])
		if got := serverInfo["name"]; got != serviceName {
			t.Fatalf("server info name = %#v, want %q", got, serviceName)
		}
		assertBrainStaticCapabilities(t, result)
	})

	t.Run("tools use exact deterministic baseline and JSON Schema 2020-12", func(t *testing.T) {
		status, _, response := postBrainMCP(t, handler, modernProtocolVersion, "tools/list", "", map[string]any{
			"_meta": modernBrainMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		result := brainResponseResult(t, response)
		assertBrainCacheable(t, result)
		tools := anyBrainSlice(t, result["tools"])
		var names []string
		for _, item := range tools {
			tool := anyBrainMap(t, item)
			names = append(names, tool["name"].(string))
			if got := anyBrainMap(t, tool["inputSchema"])["$schema"]; got != jsonSchema202012 {
				t.Fatalf("tool %q inputSchema.$schema = %#v", tool["name"], got)
			}
		}
		want := []string{
			"brain.document.upsert",
			"brain.get",
			"brain.health",
			"brain.hybrid",
			"brain.index",
			"brain.index.status",
			"brain.keyword",
			"brain.schema",
			"brain.search",
			"brain.semantic",
			"brain.sources",
			"brain.stats",
			"brain.timeline",
			"test.identity",
		}
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("tool names = %#v, want %#v", names, want)
		}
	})

	t.Run("tool result and request identity", func(t *testing.T) {
		status, header, response := postBrainMCP(t, handler, modernProtocolVersion, "tools/call", "test.identity", map[string]any{
			"_meta":     modernBrainMeta(modernProtocolVersion),
			"name":      "test.identity",
			"arguments": map[string]any{},
		}, http.Header{
			"X-Tenant-Id": []string{"tenant-a"},
			"X-User-Id":   []string{"user-a"},
			"X-Scopes":    []string{"read,write"},
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
		if got := header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("Mcp-Session-Id = %q, want empty", got)
		}
		result := brainResponseResult(t, response)
		if got := result["resultType"]; got != "complete" {
			t.Fatalf("resultType = %#v, want complete", got)
		}
		structured := anyBrainMap(t, result["structuredContent"])
		if structured["tenant"] != "tenant-a" || structured["user"] != "user-a" {
			t.Fatalf("identity = %#v", structured)
		}
		if got := anyBrainSlice(t, structured["scopes"]); !reflect.DeepEqual(got, []any{"read", "write"}) {
			t.Fatalf("scopes = %#v", got)
		}
	})

	t.Run("wire errors", func(t *testing.T) {
		status, _, response := postBrainMCP(t, handler, modernProtocolVersion, "tools/call", "wrong-name", map[string]any{
			"_meta":     modernBrainMeta(modernProtocolVersion),
			"name":      "test.identity",
			"arguments": map[string]any{},
		}, nil)
		if status != http.StatusBadRequest || brainResponseErrorCode(t, response) != -32020 {
			t.Fatalf("header mismatch: status=%d response=%#v", status, response)
		}

		const unsupported = "2099-01-01"
		status, _, response = postBrainMCP(t, handler, unsupported, "tools/list", "", map[string]any{
			"_meta": modernBrainMeta(unsupported),
		}, nil)
		if status != http.StatusBadRequest || brainResponseErrorCode(t, response) != -32022 {
			t.Fatalf("unsupported version: status=%d response=%#v", status, response)
		}

		status, _, response = postBrainMCP(t, handler, modernProtocolVersion, "ping", "", map[string]any{
			"_meta": modernBrainMeta(modernProtocolVersion),
		}, nil)
		if status != http.StatusNotFound || brainResponseErrorCode(t, response) != -32601 {
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
		req := httptest.NewRequest(http.MethodPost, "/mcp/brain", bytes.NewReader(body))
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

func assertBrainStaticCapabilities(t *testing.T, result map[string]any) {
	t.Helper()
	capabilities := anyBrainMap(t, result["capabilities"])
	for _, name := range []string{"tools", "prompts", "resources"} {
		capability := anyBrainMap(t, capabilities[name])
		if got := capability["listChanged"]; got != nil {
			t.Fatalf("capabilities.%s.listChanged = %#v, want absent", name, got)
		}
	}
	if got := capabilities["logging"]; got != nil {
		t.Fatalf("capabilities.logging = %#v, want absent", got)
	}
}

func TestBrainAdapterPreservesAnyJSON(t *testing.T) {
	values := []any{
		map[string]any{"ok": true},
		[]any{"a", float64(2)},
		"value",
		float64(42),
		true,
		json.RawMessage("null"),
	}
	for _, value := range values {
		handler := server.ToolHandlerFunc(func(context.Context, legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
			return &legacymcp.CallToolResult{
				Content:           []legacymcp.Content{legacymcp.NewTextContent("ok")},
				StructuredContent: value,
			}, nil
		})
		result, err := adaptToolHandler(handler)(context.Background(), legacymcp.CallToolRequest{
			Params: legacymcp.CallToolParams{Name: "test", Arguments: map[string]any{}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertBrainStructuredValue(t, result, value)
	}
}

func TestBrainResponseWrapperPreservesAnyJSON(t *testing.T) {
	wrapper := NewResponseWrapper(zap.NewNop())
	for _, value := range []any{map[string]any{"ok": true}, []any{"a"}, "value", float64(1), true, nil} {
		result := wrapper.Wrap(value)
		converted, err := toOfficialToolResult(result)
		if err != nil {
			t.Fatal(err)
		}
		assertBrainStructuredValue(t, converted, value)
	}
}

func TestBrainHTTPHelpers(t *testing.T) {
	if got := normalizeRoute("mcp/brain/"); got != "/mcp/brain" {
		t.Fatalf("normalizeRoute = %q", got)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/mcp/brain", nil)
	corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("OPTIONS must not reach next handler")
	})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	allowed := recorder.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"Mcp-Protocol-Version", "Mcp-Method", "Mcp-Name", "X-Tenant-Id"} {
		if !strings.Contains(allowed, header) {
			t.Fatalf("Access-Control-Allow-Headers = %q, missing %s", allowed, header)
		}
	}
}

func postBrainMCP(t *testing.T, handler http.Handler, version, method, name string, params map[string]any, extra http.Header) (int, http.Header, map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/brain", bytes.NewReader(body))
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

func modernBrainMeta(version string) map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

func brainResponseResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if response["error"] != nil {
		t.Fatalf("unexpected JSON-RPC error: %#v", response["error"])
	}
	return anyBrainMap(t, response["result"])
}

func brainResponseErrorCode(t *testing.T, response map[string]any) int {
	t.Helper()
	return int(anyBrainMap(t, response["error"])["code"].(float64))
}

func assertBrainCacheable(t *testing.T, result map[string]any) {
	t.Helper()
	if result["resultType"] != "complete" || result["ttlMs"] != float64(0) || result["cacheScope"] != "public" {
		t.Fatalf("incomplete cache metadata: %#v", result)
	}
}

func assertBrainStructuredValue(t *testing.T, result *legacymcp.CallToolResult, want any) {
	t.Helper()
	wire := marshalBrainMap(t, result)
	got, present := wire["structuredContent"]
	if !present {
		t.Fatalf("structuredContent absent for %T", want)
	}
	encoded, _ := json.Marshal(want)
	var normalizedWant any
	if err := json.Unmarshal(encoded, &normalizedWant); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, normalizedWant) {
		t.Fatalf("structuredContent = %#v, want %#v", got, normalizedWant)
	}
}

func marshalBrainMap(t *testing.T, value any) map[string]any {
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

func anyBrainMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map[string]any", value)
	}
	return result
}

func anyBrainSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want []any", value)
	}
	return result
}

func containsBrain(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
