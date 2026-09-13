package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHealthUsesAuthenticatedNoConsumptionEndpoint(t *testing.T) {
	credentialsPath := writeTestCredentials(t, `{"mcp_id":"ocr-1","api_key":"secret"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/mcp/ocr/health" {
			t.Fatalf("unexpected health request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, credentialsPath: credentialsPath, httpClient: server.Client()}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
}

func TestExtractForwardsV1ContractAndReturnsStructuredResponse(t *testing.T) {
	credentialsPath := writeTestCredentials(t, `{"api_key":"secret"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/mcp/ocr" {
			t.Fatalf("unexpected extract request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		var input ExtractRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if input.SourceRef != "storage://scan.pdf" || input.SourceURL != "" || len(input.Pages) != 2 || input.Pages[0] != 0 || input.Pages[1] != 2 || !input.IncludeImages || input.RequestID != "req-1" {
			t.Fatalf("unexpected forwarded input: %+v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"markdown":"# Scan","pages_processed":2,"credits_consumed":1040,"request_id":"req-1"}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, credentialsPath: credentialsPath, httpClient: server.Client()}
	result, err := client.Extract(context.Background(), ExtractRequest{
		SourceRef: "storage://scan.pdf", Pages: []int{0, 2}, IncludeImages: true, RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if result["markdown"] != "# Scan" || result["request_id"] != "req-1" || result["pages_processed"] != float64(2) {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExtractRejectsCredentialsWithoutAPIKey(t *testing.T) {
	client := &Client{baseURL: "http://example.invalid", credentialsPath: writeTestCredentials(t, `{"mcp_id":"ocr-1"}`), httpClient: http.DefaultClient}
	if _, err := client.Extract(context.Background(), ExtractRequest{SourceRef: "data:image/png;base64,AA=="}); err == nil {
		t.Fatal("expected missing api_key to be rejected")
	}
}

func writeTestCredentials(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return path
}
