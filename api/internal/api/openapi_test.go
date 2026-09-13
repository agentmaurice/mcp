package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const testSpec = `openapi: 3.1.0
paths:
  /items/{id}:
    get:
      operationId: getItem
      summary: Read one item
    post:
      operationId: updateItem`

func TestParseAndListOperations(t *testing.T) {
	document, err := Parse(testSpec)
	if err != nil {
		t.Fatal(err)
	}
	operations := Operations(document)
	if len(operations) != 2 || operations[0].OperationID != "getItem" {
		t.Fatalf("unexpected operations: %#v", operations)
	}
}

func TestCallEnforcesAllowlistAndOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/items/42" || r.URL.Query().Get("view") != "short" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42}`))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	t.Setenv("MCP_API_ALLOW_HTTP", "true")
	t.Setenv("MCP_API_ALLOWED_HOSTS", "localhost")
	localURL := "http://localhost:" + parsed.Port()
	result, err := Call(context.Background(), CallInput{Spec: testSpec, BaseURL: localURL, OperationID: "getItem", PathParams: map[string]interface{}{"id": 42}, Query: map[string]interface{}{"view": "short"}})
	if err != nil || result.StatusCode != 200 {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}

func TestCallRefusesMutationAndLiteralIP(t *testing.T) {
	if _, err := Call(context.Background(), CallInput{Spec: testSpec, BaseURL: "https://example.com", OperationID: "updateItem", PathParams: map[string]interface{}{"id": 1}}); err == nil {
		t.Fatal("expected mutation refusal")
	}
	if err := validateTarget(&url.URL{Scheme: "https", Host: "127.0.0.1"}); err == nil {
		t.Fatal("expected literal IP refusal")
	}
}
