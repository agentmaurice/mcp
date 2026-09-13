package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/config"
	"github.com/agentmaurice/mcpchatui/mcp/rag/internal/shared"
	legacymcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"
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
		tools := anySlice(t, result["tools"])
		var names []string
		for _, item := range tools {
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
		want := map[string]any{"tool": "alpha", "value": "kept"}
		if got := anyMap(t, result["structuredContent"]); !reflect.DeepEqual(got, want) {
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
		if status != http.StatusNotFound || responseErrorCode(t, response) != -32601 {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
	})

	t.Run("unsupported version is negotiated explicitly", func(t *testing.T) {
		const unsupported = "2099-01-01"
		status, _, response := postMCP(t, handler, unsupported, "tools/list", "", map[string]any{
			"_meta": modernMeta(unsupported),
		})
		if status != http.StatusBadRequest || responseErrorCode(t, response) != -32022 {
			t.Fatalf("status = %d, response = %#v", status, response)
		}
	})

	t.Run("header mismatch is rejected", func(t *testing.T) {
		status, _, response := postMCP(t, handler, modernProtocolVersion, "tools/call", "zeta", map[string]any{
			"_meta":     modernMeta(modernProtocolVersion),
			"name":      "alpha",
			"arguments": map[string]any{},
		})
		if status != http.StatusBadRequest || responseErrorCode(t, response) != -32020 {
			t.Fatalf("status = %d, response = %#v", status, response)
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

func TestAllRAGCapabilitiesRegisterOnModernServer(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, config.ServerConfig{}, zap.NewNop())
	if err := s.Build(); err != nil {
		t.Fatal(err)
	}
	handler := s.GetStreamableHandler()
	if handler == nil {
		t.Fatal("modern streamable handler is nil")
	}

	status, _, response := postMCP(t, handler, modernProtocolVersion, "tools/list", "", map[string]any{
		"_meta": modernMeta(modernProtocolVersion),
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
	tools := anySlice(t, responseResult(t, response)["tools"])
	if len(tools) != 12 {
		t.Fatalf("len(tools) = %d, want 12", len(tools))
	}
	var toolNames []string
	for _, item := range tools {
		tool := anyMap(t, item)
		toolNames = append(toolNames, tool["name"].(string))
		if got := anyMap(t, tool["inputSchema"])["$schema"]; got != jsonSchema202012 {
			t.Fatalf("tool %q inputSchema.$schema = %#v, want %q", tool["name"], got, jsonSchema202012)
		}
	}
	wantToolNames := []string{
		"rag_check_document",
		"rag_compare_documents",
		"rag_extract_document_text",
		"rag_ingest_start",
		"rag_ingest_status",
		"rag_list_documents",
		"rag_list_tenants",
		"rag_purge_tenant",
		"rag_query",
		"rag_reindex_embeddings",
		"rag_scan_document",
		"rag_score_document",
	}
	if !reflect.DeepEqual(toolNames, wantToolNames) {
		t.Fatalf("tool names = %#v, want %#v", toolNames, wantToolNames)
	}

	status, _, response = postMCP(t, handler, modernProtocolVersion, "resources/templates/list", "", map[string]any{
		"_meta": modernMeta(modernProtocolVersion),
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
	templates := anySlice(t, responseResult(t, response)["resourceTemplates"])
	if len(templates) != 5 {
		t.Fatalf("len(resourceTemplates) = %d, want 5", len(templates))
	}
	var names []string
	for _, item := range templates {
		names = append(names, anyMap(t, item)["name"].(string))
	}
	want := []string{"rag_chunk", "rag_deployment", "rag_document", "rag_job", "rag_tenant"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("resource template names = %#v, want %#v", names, want)
	}
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
		assertStructuredValue(t, result, value)
	}
}

func TestAdaptToolHandlerPromotesJSONText(t *testing.T) {
	values := []string{`{"ok":true}`, `["a",2]`, `"value"`, `42`, `true`, `null`}
	for _, value := range values {
		legacyHandler := func(context.Context, legacymcp.CallToolRequest) (*legacymcp.CallToolResult, error) {
			return &legacymcp.CallToolResult{Content: []legacymcp.Content{legacymcp.NewTextContent(value)}}, nil
		}
		result, err := adaptToolHandler(server.ToolHandlerFunc(legacyHandler))(
			context.Background(),
			&officialmcp.CallToolRequest{Params: &officialmcp.CallToolParamsRaw{Name: "test", Arguments: json.RawMessage(`{}`)}},
		)
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal([]byte(value), &want); err != nil {
			t.Fatal(err)
		}
		assertStructuredValue(t, result, want)
	}
}

func TestAdaptResourceHandler(t *testing.T) {
	template := newResourceTemplate("test", "rag://test/{id}", "test resource")
	modernServer := newModernMCPServer(serviceName, serviceVersion, "")
	modernServer.AddResourceTemplate(toOfficialResourceTemplate(template), adaptResourceHandler(
		func(_ context.Context, request legacymcp.ReadResourceRequest) ([]legacymcp.ResourceContents, error) {
			if request.Params.URI == "rag://test/missing" {
				return nil, shared.ErrNotFound("test resource")
			}
			return []legacymcp.ResourceContents{legacymcp.TextResourceContents{
				URI: request.Params.URI, MIMEType: "application/json", Text: `{"ok":true}`,
			}}, nil
		},
	))
	handler := officialmcp.NewStreamableHTTPHandler(
		func(*http.Request) *officialmcp.Server { return modernServer },
		&officialmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	status, _, response := postMCP(t, handler, modernProtocolVersion, "resources/read", "rag://test/one", map[string]any{
		"_meta": modernMeta(modernProtocolVersion), "uri": "rag://test/one",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
	contents := anySlice(t, responseResult(t, response)["contents"])
	if got := anyMap(t, contents[0])["text"]; got != `{"ok":true}` {
		t.Fatalf("resource text = %#v", got)
	}

	status, _, response = postMCP(t, handler, modernProtocolVersion, "resources/read", "rag://test/missing", map[string]any{
		"_meta": modernMeta(modernProtocolVersion), "uri": "rag://test/missing",
	})
	if status != http.StatusBadRequest || responseErrorCode(t, response) != -32602 {
		t.Fatalf("status = %d, response = %#v", status, response)
	}
}

func TestResponseWrapperDirectResultIsStructured(t *testing.T) {
	wrapper := NewResponseWrapper(nil, zap.NewNop())
	result, err := wrapper.WrapToolResult(context.Background(), "test", map[string]any{"status": "ok"}, "")
	if err != nil {
		t.Fatal(err)
	}
	assertStructuredValue(t, mustOfficialResult(t, result), map[string]any{"status": "ok"})
}

func postMCP(t *testing.T, handler http.Handler, version, method, name string, params map[string]any) (int, http.Header, map[string]any) {
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

func assertStructuredValue(t *testing.T, result *officialmcp.CallToolResult, want any) {
	t.Helper()
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

func mustOfficialResult(t *testing.T, result *legacymcp.CallToolResult) *officialmcp.CallToolResult {
	t.Helper()
	converted, err := toOfficialToolResult(result)
	if err != nil {
		t.Fatal(err)
	}
	return converted
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

func TestResourceTemplateConversion(t *testing.T) {
	template, err := uritemplate.New("rag://document/{id}")
	if err != nil {
		t.Fatal(err)
	}
	definition := legacymcp.ResourceTemplate{Name: "document", URITemplate: &legacymcp.URITemplate{Template: template}}
	if got := toOfficialResourceTemplate(definition).Name; got != "document" {
		t.Fatalf("name = %q, want document", got)
	}
}
