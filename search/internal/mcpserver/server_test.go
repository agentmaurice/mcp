package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/search/internal/search"
)

func TestToolsRejectInjectedScopeAndInvalidOptionalArguments(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid call reached backend") }))
	defer backend.Close()
	service, _ := search.New(search.Config{URL: backend.URL, OrganizationID: "o", DeploymentID: "d", PrincipalID: "p", Corpus: "c", IndexUID: "i", IndexRevision: "r", Embedder: "e"}, "key")
	s := New(service).Legacy()
	for _, args := range []string{`{"corpus":"c","query":"x","organization_id":"other"}`, `{"corpus":"c","query":"x","limit":0}`, `{"corpus":"c","query":"x","limit":null}`, `{"corpus":"c","query":"x","limit":1.5}`, `{"corpus":"c","query":"x","mode":"auto"}`} {
		raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_query_v1","arguments":` + args + `}}`)
		response := s.HandleMessage(context.Background(), raw)
		b, _ := json.Marshal(response)
		var parsed struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(b, &parsed); err != nil {
			t.Fatal(err)
		}
		if !parsed.Result.IsError && parsed.Error == nil {
			t.Fatalf("accepted %s: %s", args, b)
		}
	}
}
