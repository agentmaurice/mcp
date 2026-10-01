package modernmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestResourceErrorsPreserveTheDomainContract(t *testing.T) {
	s := New("resource-test", "1", "")
	s.AddResourceTemplate(mcp.NewResourceTemplate("test://record/{id}", "records"), func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		switch req.Params.URI {
		case "test://record/missing":
			return nil, ErrResourceNotFound
		case "test://record/failure":
			return nil, errors.New("store failed")
		default:
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: "application/json", Text: `{"ok":true}`}}, nil
		}
	})
	for _, tc := range []struct {
		name         string
		status, code int
	}{{"one", http.StatusOK, 0}, {"missing", http.StatusBadRequest, -32602}, {"failure", http.StatusOK, -32603}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": tc.name, "method": "resources/read", "params": map[string]any{"uri": "test://record/" + tc.name, "_meta": modernMeta()}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Protocol-Version", modernProtocolVersion)
			req.Header.Set("Mcp-Method", "resources/read")
			req.Header.Set("Mcp-Name", "test://record/"+tc.name)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("HTTP=%d want%d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var response struct {
				ID    string `json:"id"`
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
				Result struct {
					Contents []mcp.TextResourceContents `json:"contents"`
				} `json:"result"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != tc.name {
				t.Fatalf("response identity changed: %s", rec.Body.String())
			}
			if tc.code != 0 {
				if response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("error changed: %s", rec.Body.String())
				}
			} else if len(response.Result.Contents) != 1 || response.Result.Contents[0].Text != `{"ok":true}` {
				t.Fatalf("resource lost: %s", rec.Body.String())
			}
		})
	}
}
