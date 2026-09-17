package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentmaurice/mcpchatui/mcp/decision/pkg/systemone"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPTransportsAndTools(t *testing.T) {
	for _, transport := range []string{"streamable", "sse"} {
		t.Run(transport, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					Questions map[string]struct {
						Type string `json:"type"`
					} `json:"questions"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid provider request")
					w.WriteHeader(422)
					return
				}
				answers := map[string]any{}
				for id, q := range req.Questions {
					switch q.Type {
					case "choice":
						answers[id] = map[string]any{"type": "choice", "choice": "article", "confidence": 0.8, "probabilities": map[string]float64{"article": 0.9, "product": 0.1}}
					case "score":
						answers[id] = map[string]any{"type": "score", "score": 0.75, "confidence": 0.8, "probabilities": map[string]float64{"0": 0.25, "1": 0.75}, "legend": map[string]string{"0": "Low", "1": "High"}}
					case "noul":
						answers[id] = map[string]any{"type": "noul", "noul": 0.9}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-latest", "answers": answers, "usage": map[string]int{"input_tokens": 10, "output_tokens": 20}})
			}))
			defer provider.Close()
			client, e := systemone.New(systemone.Config{APIKey: "secret", URL: provider.URL})
			if e != nil {
				t.Fatal(e)
			}
			srv := httptest.NewServer(Handler(New(client, "test")))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var tr mcp.Transport = &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}
			if transport == "sse" {
				tr = &mcp.SSEClientTransport{Endpoint: srv.URL + "/sse"}
			}
			session, e := mcp.NewClient(&mcp.Implementation{Name: "qualification", Version: "test"}, nil).Connect(ctx, tr, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer session.Close()
			tools, e := session.ListTools(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(tools.Tools) != 6 {
				t.Fatalf("want six tools, got %d", len(tools.Tools))
			}
			names := map[string]bool{}
			for _, tool := range tools.Tools {
				names[tool.Name] = true
				if tool.Description == "" || tool.InputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
					t.Fatalf("incomplete tool %s", tool.Name)
				}
			}
			for _, name := range []string{"health", "capabilities", "ask", "choice", "score", "noul"} {
				if !names["decision_"+name+"_v1"] {
					t.Fatal("missing tool", name)
				}
			}
			call := func(name string, args any) *mcp.CallToolResult {
				t.Helper()
				out, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "decision_" + name + "_v1", Arguments: args})
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			for _, name := range []string{"health", "capabilities"} {
				if call(name, map[string]any{}).IsError {
					t.Fatal("local tool failed")
				}
			}
			if calls.Load() != 0 {
				t.Fatal("health/capabilities spent provider tokens")
			}
			choice := map[string]any{"instructions": "Classify", "criteria": map[string]string{"article": "News", "product": "Product"}}
			for _, kind := range []string{"choice", "score", "noul"} {
				args := map[string]any{"state": "Synthetic public example", "instructions": "Evaluate"}
				if kind == "choice" {
					args["criteria"] = choice["criteria"]
					args["min_confidence"] = 0.9
				}
				if kind == "score" {
					args["criteria"] = []string{"Low", "High"}
					args["min_confidence"] = 0.9
				}
				out := call(kind, args)
				if out.IsError || out.StructuredContent == nil {
					t.Fatalf("%s failed: %+v", kind, out)
				}
				b, _ := json.Marshal(out.StructuredContent)
				if kind == "choice" && !strings.Contains(string(b), `"choice":"uncertain"`) {
					t.Fatal("choice threshold ignored")
				}
				if kind == "score" && !strings.Contains(string(b), `"score":null`) {
					t.Fatal("score threshold ignored")
				}
			}
			before := calls.Load()
			out := call("ask", map[string]any{"state": map[string]any{"text": "Synthetic"}, "questions": map[string]any{
				"kind":    map[string]any{"type": "choice", "instructions": "Classify", "criteria": choice["criteria"]},
				"quality": map[string]any{"type": "score", "instructions": "Quality", "criteria": []string{"Low", "High"}},
				"urgent":  map[string]any{"type": "noul", "instructions": "Urgent?"},
			}})
			if out.IsError || calls.Load() != before+1 {
				t.Fatal("MCP batch did not preserve one call")
			}
			before = calls.Load()
			for _, args := range []any{
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": map[string]string{"article": "A", "uncertain": "U"}},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "confidence_kind": "calibrated"},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "PRIVATE_KEY": "SECRET"},
				map[string]any{"state": "PRIVATE_STATE", "instructions": "Q", "criteria": choice["criteria"], "min_confidence": 2},
			} {
				out := call("choice", args)
				if !out.IsError {
					t.Fatal("invalid arguments accepted")
				}
				b, _ := json.Marshal(out)
				if strings.Contains(string(b), "PRIVATE_") || strings.Contains(string(b), "SECRET") {
					t.Fatal("secret in tool error")
				}
			}
			if calls.Load() != before {
				t.Fatal("invalid MCP calls reached provider")
			}
		})
	}
}

func TestDecodeStrictObject(t *testing.T) {
	for _, b := range []string{"null", "[]", "{} {}", `{"unknown":"private"}`} {
		var v struct{}
		if decode([]byte(b), &v) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestHealthEndpoints(t *testing.T) {
	c, _ := systemone.New(systemone.Config{APIKey: "secret"})
	s := httptest.NewServer(Handler(New(c, "test")))
	defer s.Close()
	for _, p := range []string{"/health", "/ready"} {
		r, e := http.Get(s.URL + p)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(fmt.Sprint(r.StatusCode))
		}
	}
}
