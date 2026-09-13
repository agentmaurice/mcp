package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterIfConfigured_StandaloneMode(t *testing.T) {
	// No env vars set — should return nil, nil.
	t.Setenv("MCP_SIDECAR_BOOTSTRAP_TOKEN", "")
	t.Setenv("MCP_SIDECAR_MAURICE_URL", "")

	resp, err := RegisterIfConfigured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response in standalone mode")
	}
}

func TestRegister_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mcp/self-register" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("missing or wrong auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"mcp_id":"mcp-123","api_key":"key-456","tenant_id":"tenant-789"}`))
	}))
	defer srv.Close()

	resp, err := Register(context.Background(), srv.URL, "test-token", map[string]string{"foo": "bar"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.MCPID != "mcp-123" || resp.APIKey != "key-456" || resp.TenantID != "tenant-789" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRegister_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Register(context.Background(), srv.URL, "bad-token", nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestParseMetadata(t *testing.T) {
	m := parseMetadata("registry_name=mcp-rag,runtime_provider=binary,port=5300")
	if m["registry_name"] != "mcp-rag" || m["runtime_provider"] != "binary" || m["port"] != "5300" {
		t.Fatalf("unexpected metadata: %v", m)
	}

	// Empty string.
	m = parseMetadata("")
	if len(m) != 0 {
		t.Fatalf("expected empty map, got: %v", m)
	}
}
